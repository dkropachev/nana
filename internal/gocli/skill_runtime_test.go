package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSkillRuntimeDocCacheAvoidsRereadForUnchangedSkillInSession(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(cwd, "codex-home")
	runtimePath := filepath.Join(codexHome, "skills", "autopilot", "RUNTIME.md")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("runtime v1\n"), 0o644); err != nil {
		t.Fatalf("write runtime: %v", err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	cache := newSkillRuntimeDocCache()
	readCount := 0
	cache.readFile = func(path string) ([]byte, error) {
		readCount++
		return os.ReadFile(path)
	}
	cacheEvents := []skillRuntimeDocTelemetry{}
	cache.appendTelemetry = func(event skillRuntimeDocTelemetry) {
		cacheEvents = append(cacheEvents, event)
	}

	first, err := loadActivatedSkillRuntimeDocsWithCache(cwd, "$autopilot build me a tool", cache)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := loadActivatedSkillRuntimeDocsWithCache(cwd, "continue $autopilot", cache)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected one activated doc on both loads, got %d and %d", len(first), len(second))
	}
	if readCount != 1 {
		t.Fatalf("expected unchanged runtime doc to be read once, got %d reads", readCount)
	}
	if first[0].Content != "runtime v1\n" || first[0].CacheStatus != "miss" {
		t.Fatalf("unexpected first doc: %#v", first[0])
	}
	if second[0].Content != "runtime v1\n" || second[0].CacheStatus != "hit" {
		t.Fatalf("unexpected second doc: %#v", second[0])
	}
	if len(cacheEvents) != 2 || cacheEvents[0].CacheStatus != "miss" || cacheEvents[1].CacheStatus != "hit" {
		t.Fatalf("expected miss then hit telemetry, got %#v", cacheEvents)
	}
	if cacheEvents[0].MatchedKeyword != "$autopilot" || cacheEvents[0].ActivationMode != "explicit" || cacheEvents[0].ActivationSource != routeSourceExplicitInvocation {
		t.Fatalf("expected explicit activation telemetry, got %#v", cacheEvents[0])
	}
}

func TestSkillRuntimeDocCacheInvalidatesWhenRuntimeMTimeChanges(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(cwd, "codex-home")
	runtimePath := filepath.Join(codexHome, "skills", "autopilot", "RUNTIME.md")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("runtime v1\n"), 0o644); err != nil {
		t.Fatalf("write runtime v1: %v", err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	cache := newSkillRuntimeDocCache()
	readCount := 0
	cache.readFile = func(path string) ([]byte, error) {
		readCount++
		return os.ReadFile(path)
	}

	if _, err := loadActivatedSkillRuntimeDocsWithCache(cwd, "$autopilot", cache); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("runtime v2\n"), 0o644); err != nil {
		t.Fatalf("write runtime v2: %v", err)
	}
	changed := time.Now().Add(time.Hour).UTC()
	if err := os.Chtimes(runtimePath, changed, changed); err != nil {
		t.Fatalf("touch runtime: %v", err)
	}
	docs, err := loadActivatedSkillRuntimeDocsWithCache(cwd, "$autopilot", cache)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if readCount != 2 {
		t.Fatalf("expected changed runtime doc to be reread, got %d reads", readCount)
	}
	if len(docs) != 1 || docs[0].Content != "runtime v2\n" || docs[0].CacheStatus != "miss" {
		t.Fatalf("expected refreshed runtime v2 miss, got %#v", docs)
	}
}

func TestWriteSessionModelInstructionsIncludesActivatedSkillRuntimeDocs(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("# User\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS: %v", err)
	}
	path, err := writeSessionModelInstructions(cwd, "session-1", codexHome, loadedSkillRuntimeDoc{
		Skill:            "autopilot",
		Label:            routeDocLabelRuntime,
		DisplayPath:      filepath.Join(codexHome, "skills", "autopilot", "RUNTIME.md"),
		ActualPath:       filepath.Join(codexHome, "skills", "autopilot", "RUNTIME.md"),
		Content:          "runtime rules\n",
		CacheStatus:      "miss",
		MatchedKeyword:   "$autopilot",
		ActivationSource: routeSourceExplicitInvocation,
		ActivationMode:   "explicit",
		SourceRule:       "explicit $name invocations run left-to-right before implicit keyword routing",
	})
	if err != nil {
		t.Fatalf("writeSessionModelInstructions: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session instructions: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"<!-- NANA:SKILL_RUNTIME_DOCS:START -->",
		`<skill name="autopilot" doc="runtime"`,
		`matched_keyword="$autopilot"`,
		`activation_source="explicit invocation"`,
		`activation_mode="explicit"`,
		`implicit_suppressed="false"`,
		"runtime rules",
		"<!-- NANA:RUNTIME:START -->",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected session instructions to contain %q:\n%s", want, text)
		}
	}
}

func TestSkillRuntimeDocLoaderUsesExplicitCodexHome(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	runtimePath := filepath.Join(scopedCodexHome, "skills", "autopilot", "RUNTIME.md")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir scoped runtime dir: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("scoped runtime rules\n"), 0o644); err != nil {
		t.Fatalf("write scoped runtime: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	withoutExplicitHome, err := loadActivatedSkillRuntimeDocsWithCache(cwd, "$autopilot", newSkillRuntimeDocCache())
	if err != nil {
		t.Fatalf("load without explicit codex home: %v", err)
	}
	if len(withoutExplicitHome) != 0 {
		t.Fatalf("expected no runtime docs from default launch home, got %#v", withoutExplicitHome)
	}

	docs, err := loadActivatedSkillRuntimeDocsWithCache(cwd, "$autopilot", newSkillRuntimeDocCache(), scopedCodexHome)
	if err != nil {
		t.Fatalf("load with explicit codex home: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected one scoped runtime doc, got %#v", docs)
	}
	if docs[0].Content != "scoped runtime rules\n" {
		t.Fatalf("expected scoped runtime content, got %#v", docs[0])
	}
	if docs[0].ActualPath != runtimePath {
		t.Fatalf("expected runtime actual path %q, got %q", runtimePath, docs[0].ActualPath)
	}
}

func TestSkillRuntimeDocLoaderAnnotatesPromptSuppressionDecision(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	runtimePath := filepath.Join(codexHome, "skills", "autopilot", "RUNTIME.md")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("runtime rules\n"), 0o644); err != nil {
		t.Fatalf("write runtime: %v", err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	docs, err := loadActivatedSkillRuntimeDocsWithCache(cwd, "/prompts:executor $autopilot please analyze this", newSkillRuntimeDocCache())
	if err != nil {
		t.Fatalf("load activated runtime docs: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected one explicit runtime doc, got %#v", docs)
	}
	doc := docs[0]
	if doc.MatchedKeyword != "$autopilot" || doc.ActivationMode != "explicit" || doc.ActivationSource != routeSourceExplicitInvocation {
		t.Fatalf("expected explicit activation decision, got %#v", doc)
	}
	if doc.ImplicitSuppressedBy != "/prompts:executor" {
		t.Fatalf("expected prompt suppression marker, got %#v", doc)
	}
	if !strings.Contains(doc.SourceRule, "explicit $name invocations") {
		t.Fatalf("expected explicit source rule, got %#v", doc.SourceRule)
	}

	formatted := formatLoadedSkillRuntimeDocs(docs)
	for _, want := range []string{
		`matched_keyword="$autopilot"`,
		`activation_source="explicit invocation"`,
		`activation_mode="explicit"`,
		`implicit_suppressed="true"`,
		`implicit_suppressed_by="/prompts:executor"`,
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("expected formatted runtime docs to contain %q:\n%s", want, formatted)
		}
	}
}

func TestSkillRuntimeDocTelemetryWritesActivationDecisionFields(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	runtimePath := filepath.Join(codexHome, "skills", "autopilot", "RUNTIME.md")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("runtime rules\n"), 0o644); err != nil {
		t.Fatalf("write runtime: %v", err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("NANA_CONTEXT_TELEMETRY", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-route")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")

	if _, err := loadActivatedSkillRuntimeDocsWithCache(cwd, "/prompts:executor $autopilot please analyze this", newSkillRuntimeDocCache()); err != nil {
		t.Fatalf("load activated runtime docs: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson"))
	if err != nil {
		t.Fatalf("read telemetry log: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		`"matched_keyword":"$autopilot"`,
		`"activation_mode":"explicit"`,
		`"activation_source":"explicit invocation"`,
		`"source_rule":"explicit $name invocations run left-to-right before implicit keyword routing"`,
		`"implicit_suppressed":true`,
		`"implicit_suppressed_by":"/prompts:executor"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected telemetry log to contain %q:\n%s", want, text)
		}
	}
}
