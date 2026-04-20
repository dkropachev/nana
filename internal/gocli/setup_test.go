package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupProjectDryRunDoesNotPersistScope(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(filepath.Join(repoRoot, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "skills", "nana-setup"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "prompts", "executor.md"), []byte("# executor\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "skills", "nana-setup", "SKILL.md"), []byte("# skill\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "templates", "AGENTS.md"), []byte("template ~/.codex\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project", "--dry-run"}) })
	if err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !strings.Contains(output, "Using setup scope: project") {
		t.Fatalf("unexpected setup output: %q", output)
	}
	if fileExists(filepath.Join(cwd, ".nana", "setup-scope.json")) {
		t.Fatalf("setup-scope.json should not be written during dry-run")
	}
}

func TestSetupDryRunOutputSummaryReportsPlannedChanges(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project", "--dry-run"}) })
	if err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !strings.Contains(output, "[dry-run mode] No files will be modified.") {
		t.Fatalf("dry-run setup output missing no-modification notice: %q", output)
	}
	if !strings.Contains(output, "Setup outputs:") || !strings.Contains(output, "would_create=") || !strings.Contains(output, "would_update=") || !strings.Contains(output, "unchanged=") {
		t.Fatalf("dry-run setup output missing planned-change summary: %q", output)
	}
	if !strings.Contains(output, "would_create=13") {
		t.Fatalf("dry-run setup summary should include checksum and missing-only planned creates, got %q", output)
	}
	if strings.Contains(output, "Setup outputs: created=") || strings.Contains(output, " updated=") {
		t.Fatalf("dry-run setup output should not report completed writes: %q", output)
	}
}

func TestEnsureNanaDirectoriesDryRunReportsMissingStateFilesWithoutWriting(t *testing.T) {
	cwd := t.TempDir()
	stats := &setupWriteStats{}

	if err := ensureNanaDirectories(cwd, SetupOptions{DryRun: true, stats: stats}); err != nil {
		t.Fatalf("ensureNanaDirectories(): %v", err)
	}
	if stats.Created != 2 || stats.Updated != 0 || stats.Unchanged != 0 {
		t.Fatalf("dry-run should report two planned missing-only file creates, got %+v", stats)
	}
	for _, path := range []string{
		filepath.Join(cwd, ".nana", "project-memory.json"),
		filepath.Join(cwd, ".nana", "notepad.md"),
	} {
		if fileExists(path) {
			t.Fatalf("dry-run should not write %s", path)
		}
	}
}

func TestBootstrapInvestigateAuthDryRunReportsPlannedCreateWithoutWriting(t *testing.T) {
	cwd := t.TempDir()
	sourceCodexHome := filepath.Join(cwd, "source-codex-home")
	investigateCodexHome := filepath.Join(cwd, "investigate-codex-home")
	if err := os.MkdirAll(sourceCodexHome, 0o755); err != nil {
		t.Fatalf("mkdir source codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceCodexHome, "auth.json"), []byte(`{"token":"secret"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write source auth: %v", err)
	}
	stats := &setupWriteStats{}

	if err := bootstrapInvestigateAuth(sourceCodexHome, investigateCodexHome, SetupOptions{DryRun: true, stats: stats}); err != nil {
		t.Fatalf("bootstrapInvestigateAuth(): %v", err)
	}
	if stats.Created != 1 || stats.Updated != 0 || stats.Unchanged != 0 {
		t.Fatalf("dry-run should report planned auth copy create, got %+v", stats)
	}
	if fileExists(filepath.Join(investigateCodexHome, "auth.json")) {
		t.Fatalf("dry-run should not write investigate auth.json")
	}
}

func TestSetupProjectWritesLocalAssets(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(filepath.Join(repoRoot, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "skills", "nana-setup"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "prompts", "executor.md"), []byte("# executor\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "skills", "nana-setup", "SKILL.md"), []byte("# skill\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "templates", "AGENTS.md"), []byte("template ~/.codex\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	if _, err := captureStdout(t, func() error { return Reasoning([]string{"high"}) }); err != nil {
		t.Fatalf("Reasoning(): %v", err)
	}
	if _, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project"}) }); err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !fileExists(filepath.Join(cwd, ".codex", "prompts", "executor.md")) {
		t.Fatalf("project prompt not installed")
	}
	if !fileExists(filepath.Join(cwd, ".codex", "skills", "nana-setup", "SKILL.md")) {
		t.Fatalf("project skill not installed")
	}
	if !fileExists(filepath.Join(cwd, ".codex", "agents", "executor.toml")) {
		t.Fatalf("project agent config not installed")
	}
	if !fileExists(filepath.Join(cwd, ".nana", "codex-home-investigate", "prompts", "executor.md")) {
		t.Fatalf("project investigate prompt not installed")
	}
	if !fileExists(filepath.Join(cwd, ".nana", "codex-home-investigate", "skills", "nana-setup", "SKILL.md")) {
		t.Fatalf("project investigate skill not installed")
	}
	if !fileExists(filepath.Join(cwd, ".nana", "codex-home-investigate", "agents", "executor.toml")) {
		t.Fatalf("project investigate agent config not installed")
	}
	for _, path := range []string{
		filepath.Join(cwd, ".nana", "state"),
		filepath.Join(cwd, ".nana", "plans"),
		filepath.Join(cwd, ".nana", "logs"),
		filepath.Join(cwd, ".nana", "project-memory.json"),
		filepath.Join(cwd, ".nana", "notepad.md"),
	} {
		if !fileExists(path) {
			t.Fatalf("expected setup to create %s", path)
		}
	}
	config, err := os.ReadFile(filepath.Join(cwd, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(config), `model_reasoning_effort = "high"`) || !strings.Contains(string(config), "[agents]") || !strings.Contains(string(config), `USE_NANA_EXPLORE_CMD = "1"`) {
		t.Fatalf("unexpected config content: %q", string(config))
	}
	agentsMd, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsMd), "./.codex") {
		t.Fatalf("expected project AGENTS.md rewrite, got %q", string(agentsMd))
	}
	if !fileExists(filepath.Join(cwd, ".nana", "codex-home-investigate", "AGENTS.md")) {
		t.Fatalf("project investigate AGENTS.md not installed")
	}
}

func TestSetupProjectFallsBackToEmbeddedAssets(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := filepath.Join(cwd, "missing-repo-root")
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	if _, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project"}) }); err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !fileExists(filepath.Join(cwd, ".codex", "prompts", "executor.md")) {
		t.Fatalf("embedded project prompt not installed")
	}
	if !fileExists(filepath.Join(cwd, ".nana", "codex-home-investigate", "prompts", "executor.md")) {
		t.Fatalf("embedded project investigate prompt not installed")
	}
	if !fileExists(filepath.Join(cwd, ".codex", "skills", "deep-interview", "SKILL.md")) {
		t.Fatalf("embedded project skill not installed")
	}
	if !fileExists(filepath.Join(cwd, ".nana", "codex-home-investigate", "skills", "deep-interview", "SKILL.md")) {
		t.Fatalf("embedded project investigate skill not installed")
	}
	agentsMd, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsMd), "./.codex") {
		t.Fatalf("expected embedded AGENTS template rewrite, got %q", string(agentsMd))
	}
	for _, needle := range []string{
		"Prefer `nana verify --json` when `nana-verify.json` exists",
		"otherwise use documented repo verification commands",
	} {
		if !strings.Contains(string(agentsMd), needle) {
			t.Fatalf("expected embedded AGENTS template to include conditional verify guidance %q, got %q", needle, string(agentsMd))
		}
	}
	if strings.Contains(string(agentsMd), "its profile runs lint, typecheck, tests, and static analysis") {
		t.Fatalf("embedded AGENTS template should not imply nana verify works without a nana-verify.json profile, got %q", string(agentsMd))
	}
	if !fileExists(filepath.Join(cwd, ".nana", "codex-home-investigate", "AGENTS.md")) {
		t.Fatalf("embedded investigate AGENTS.md not installed")
	}
}

func TestWriteSetupAgentsMdInsertsStandaloneGeneratedMarkerWhenTemplateMentionsMarkerInProse(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	if err := os.MkdirAll(filepath.Join(repoRoot, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "skills", "plan"), 0o755); err != nil {
		t.Fatalf("mkdir plan skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "prompts", "executor.md"), []byte("# executor\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "skills", "plan", "SKILL.md"), []byte("# plan\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	template := strings.Join([]string{
		"<!-- AUTONOMY DIRECTIVE — DO NOT REMOVE -->",
		"YOU ARE AN AUTONOMOUS CODING AGENT.",
		"<!-- END AUTONOMY DIRECTIVE -->",
		"",
		"# nana - Compact Runtime Policy",
		"",
		"- Health: expect `" + generatedAgentsMarker + "` plus runtime/model marker pairs above.",
		"- Skill root: `~/.codex/skills`.",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(repoRoot, "templates", "AGENTS.md"), []byte(template), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	if err := writeSetupAgentsMd(repoRoot, cwd, filepath.Join(cwd, ".codex"), SetupOptions{}); err != nil {
		t.Fatalf("writeSetupAgentsMd(): %v", err)
	}

	contentBytes, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(contentBytes)
	lines := strings.Split(content, "\n")
	standaloneMarkerCount := 0
	markerLine := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == generatedAgentsMarker {
			standaloneMarkerCount++
			markerLine = index
		}
	}
	if standaloneMarkerCount != 1 {
		t.Fatalf("generated AGENTS.md should contain exactly one standalone generated marker line, got %d in:\n%s", standaloneMarkerCount, content)
	}
	if markerLine != 3 {
		t.Fatalf("standalone generated marker should be inserted immediately after the autonomy directive near the top, line=%d content:\n%s", markerLine, content)
	}
	if !strings.Contains(content, "`"+generatedAgentsMarker+"`") {
		t.Fatalf("test template should still include prose marker reference, got:\n%s", content)
	}
	if strings.Contains(content, "~/.codex") || !strings.Contains(content, "./.codex/skills") {
		t.Fatalf("project AGENTS.md should rewrite Codex home references, got:\n%s", content)
	}
}

func TestSetupProjectExistingAgentsNonForceSkipsWithoutReadingTarget(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	if err := os.Mkdir(agentsPath, 0o755); err != nil {
		t.Fatalf("create existing AGENTS.md directory: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return writeSetupAgentsMd(repoRoot, cwd, filepath.Join(cwd, ".codex"), SetupOptions{})
	})
	if err != nil {
		t.Fatalf("writeSetupAgentsMd() should skip existing project AGENTS.md without reading it: %v", err)
	}
	if !strings.Contains(output, "Skipped AGENTS.md overwrite") {
		t.Fatalf("expected skip output, got %q", output)
	}
	info, err := os.Stat(agentsPath)
	if err != nil {
		t.Fatalf("stat AGENTS.md: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("existing AGENTS.md should remain untouched")
	}
}

func TestSetupUserExistingUnmanagedAgentsNonForcePreservesGlobalInstructions(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	home := filepath.Join(cwd, "home")
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	agentsPath := filepath.Join(codexHome, "AGENTS.md")
	customAgents := "# custom global instructions\n\nDo not overwrite me.\n"
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(agentsPath, []byte(customAgents), 0o644); err != nil {
		t.Fatalf("write custom AGENTS.md: %v", err)
	}

	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "user"}) })
	if err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !strings.Contains(output, "Skipped AGENTS.md overwrite") {
		t.Fatalf("expected setup to skip unmanaged user AGENTS.md, got %q", output)
	}
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if string(content) != customAgents {
		t.Fatalf("setup overwrote unmanaged user AGENTS.md:\n%s", content)
	}
	if !fileExists(filepath.Join(codexHome, "config.toml")) {
		t.Fatalf("setup should still repair missing user config")
	}
	if !fileExists(filepath.Join(cwd, ".nana", "state")) {
		t.Fatalf("setup should still repair missing NANA state dirs")
	}
}

func TestSetupUserAgentsPathConflictFailsWithActionableRemediation(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	home := filepath.Join(cwd, "home")
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	agentsPath := filepath.Join(codexHome, "AGENTS.md")
	if err := os.MkdirAll(agentsPath, 0o755); err != nil {
		t.Fatalf("create AGENTS.md path conflict: %v", err)
	}

	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "user"}) })
	if err == nil {
		t.Fatalf("expected setup failure for user AGENTS.md path conflict")
	}
	if strings.Contains(output, "Skipped AGENTS.md overwrite") {
		t.Fatalf("path conflict should not be silently skipped, got output %q", output)
	}
	text := err.Error()
	for _, want := range []string{
		`setup phase "write AGENTS.md" failed`,
		"AGENTS.md exists and is a directory",
		"affected path: " + agentsPath,
		"safe automatic fix: no",
		"manual fallback: inspect the affected path",
		"nana setup --scope user",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("setup error missing %q in:\n%s", want, text)
		}
	}
}

func TestSetupUserExistingAgentsInitManagedFileNonForcePreservesManualSection(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	home := filepath.Join(cwd, "home")
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	agentsPath := filepath.Join(codexHome, "AGENTS.md")
	manualAgents := strings.Join([]string{
		managedMarker,
		"# Lightweight local file",
		manualStart,
		"Keep this manual note.",
		manualEnd,
		"",
	}, "\n")
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(agentsPath, []byte(manualAgents), 0o644); err != nil {
		t.Fatalf("write agents-init AGENTS.md: %v", err)
	}

	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "user"}) })
	if err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !strings.Contains(output, "Skipped AGENTS.md overwrite") {
		t.Fatalf("expected setup to skip non-setup-managed user AGENTS.md, got %q", output)
	}
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if string(content) != manualAgents {
		t.Fatalf("setup overwrote user AGENTS.md manual section:\n%s", content)
	}
}

func TestSetupUserExistingGeneratedAgentsNonForceRefreshes(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	home := filepath.Join(cwd, "home")
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	agentsPath := filepath.Join(codexHome, "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(agentsPath, []byte("<!-- nana:generated:agents-md -->\nstale ~/.codex\n"), 0o644); err != nil {
		t.Fatalf("write stale generated AGENTS.md: %v", err)
	}

	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "user"}) })
	if err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if strings.Contains(output, "Skipped AGENTS.md overwrite") {
		t.Fatalf("generated user AGENTS.md should be refreshable without --force, got %q", output)
	}
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(content)
	if text == "<!-- nana:generated:agents-md -->\nstale ~/.codex\n" ||
		!strings.Contains(text, "<!-- nana:generated:agents-md -->") ||
		!strings.Contains(text, "Compact Runtime Policy") {
		t.Fatalf("expected generated user AGENTS.md refresh, got %q", content)
	}
}

func TestSetupWarmRunKeepsGeneratedArtifactsAndCacheUnchanged(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	if _, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project", "--verbose"}) }); err != nil {
		t.Fatalf("Setup() first run: %v", err)
	}
	paths := []string{
		filepath.Join(cwd, "AGENTS.md"),
		filepath.Join(cwd, ".codex", "config.toml"),
		filepath.Join(cwd, ".codex", "prompts", "executor.md"),
		filepath.Join(cwd, ".codex", "skills", "nana-setup", "SKILL.md"),
		filepath.Join(cwd, ".codex", "agents", "executor.toml"),
		filepath.Join(cwd, ".nana", "codex-home-investigate", "AGENTS.md"),
		filepath.Join(cwd, ".nana", "codex-home-investigate", "prompts", "executor.md"),
		filepath.Join(cwd, ".nana", "codex-home-investigate", "skills", "nana-setup", "SKILL.md"),
		filepath.Join(cwd, ".nana", "setup-scope.json"),
		setupWriteCachePath(cwd),
	}
	before := statSetupFiles(t, paths)
	cache := readSetupWriteCacheForTest(t, setupWriteCachePath(cwd))
	expectedCacheKey := setupCacheKey(filepath.Join(cwd, ".codex", "prompts", "executor.md"))
	entry, ok := cache.Entries[expectedCacheKey]
	if !ok || entry.Checksum == "" {
		t.Fatalf("setup cache missing checksum entry for %s: %+v", expectedCacheKey, cache.Entries)
	}

	time.Sleep(20 * time.Millisecond)
	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project", "--verbose"}) })
	if err != nil {
		t.Fatalf("Setup() warm run: %v", err)
	}
	if !strings.Contains(output, "Setup timings:") || !strings.Contains(output, "install prompts") {
		t.Fatalf("verbose setup output missing phase timings: %q", output)
	}
	if !strings.Contains(output, "Setup outputs:") || !strings.Contains(output, "created=0") || !strings.Contains(output, "updated=0") || !strings.Contains(output, "unchanged=") {
		t.Fatalf("warm setup output missing unchanged summary: %q", output)
	}
	after := statSetupFiles(t, paths)
	for _, path := range paths {
		if before[path] != after[path] {
			t.Fatalf("warm setup rewrote unchanged file %s: before=%+v after=%+v", path, before[path], after[path])
		}
	}
}

func TestSetupOutputSummaryReportsCreatedUpdatedUnchanged(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project"}) })
	if err != nil {
		t.Fatalf("Setup() first run: %v", err)
	}
	if !strings.Contains(output, "Setup outputs:") || !strings.Contains(output, "created=") || !strings.Contains(output, "updated=") || !strings.Contains(output, "unchanged=") {
		t.Fatalf("setup output missing write summary: %q", output)
	}

	if err := os.WriteFile(filepath.Join(repoRoot, "prompts", "executor.md"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatalf("change prompt source: %v", err)
	}
	output, err = captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project"}) })
	if err != nil {
		t.Fatalf("Setup() update run: %v", err)
	}
	if !strings.Contains(output, "created=0") || !strings.Contains(output, "updated=2") || !strings.Contains(output, "unchanged=") {
		t.Fatalf("setup output missing expected update counts: %q", output)
	}
	content, err := os.ReadFile(filepath.Join(cwd, ".codex", "prompts", "executor.md"))
	if err != nil {
		t.Fatalf("read updated prompt: %v", err)
	}
	if string(content) != "# changed\n" {
		t.Fatalf("expected setup to refresh changed prompt, got %q", content)
	}
}

func TestSetupChecksumCacheRefreshesSameSizeChangedGeneratedFileWithPreservedMetadata(t *testing.T) {
	cwd, repoRoot := setupTestFixture(t)
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	if _, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project"}) }); err != nil {
		t.Fatalf("Setup() first run: %v", err)
	}
	target := filepath.Join(cwd, ".codex", "prompts", "executor.md")
	before, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat installed prompt: %v", err)
	}
	staleContent := []byte("# replaced\n")
	if int64(len(staleContent)) != before.Size() {
		t.Fatalf("test stale content must preserve file size: stale=%d original=%d", len(staleContent), before.Size())
	}
	if err := os.WriteFile(target, staleContent, 0o644); err != nil {
		t.Fatalf("write stale prompt: %v", err)
	}
	if err := os.Chtimes(target, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("restore stale prompt mtime: %v", err)
	}
	staleInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat stale prompt: %v", err)
	}
	if staleInfo.Size() != before.Size() || staleInfo.ModTime().UnixNano() != before.ModTime().UnixNano() {
		t.Fatalf("test setup failed to preserve stale prompt metadata: before=%+v stale=%+v", before, staleInfo)
	}

	if _, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project"}) }); err != nil {
		t.Fatalf("Setup() refresh run: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read refreshed prompt: %v", err)
	}
	if string(content) != "# executor\n" {
		t.Fatalf("expected setup to restore prompt from source, got %q", content)
	}
}

func TestSetupFailureErrorIncludesActionableRemediation(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".codex", "config.toml")
	err := newSetupFailureError("write config", target, "project", os.ErrPermission)
	if err == nil {
		t.Fatalf("expected setup failure error")
	}
	text := err.Error()
	for _, want := range []string{
		`setup phase "write config" failed: permission denied`,
		"affected path: " + target,
		"safe automatic fix: no",
		"manual fallback: inspect the affected path",
		"nana setup --scope project",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("setup failure missing %q in:\n%s", want, text)
		}
	}
}

func TestRuntimeBytesChecksumGuardSkipsIdenticalOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-instructions.md")
	content := []byte("AGENTS\n<!-- NANA:RUNTIME:START -->\nctx\n<!-- NANA:RUNTIME:END -->\n")
	if err := writeRuntimeBytesIfChanged(path, content); err != nil {
		t.Fatalf("write runtime bytes: %v", err)
	}
	before := statSetupFiles(t, []string{path})[path]
	time.Sleep(20 * time.Millisecond)
	if err := writeRuntimeBytesIfChanged(path, content); err != nil {
		t.Fatalf("write identical runtime bytes: %v", err)
	}
	after := statSetupFiles(t, []string{path})[path]
	if before != after {
		t.Fatalf("identical runtime overlay rewrote file: before=%+v after=%+v", before, after)
	}
}

type setupFileFingerprint struct {
	Size            int64
	ModTimeUnixNano int64
}

func statSetupFiles(t *testing.T, paths []string) map[string]setupFileFingerprint {
	t.Helper()
	stats := map[string]setupFileFingerprint{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		stats[path] = setupFileFingerprint{Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}
	}
	return stats
}

func readSetupWriteCacheForTest(t *testing.T, path string) setupWriteCache {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read setup cache: %v", err)
	}
	var cache setupWriteCache
	if err := json.Unmarshal(content, &cache); err != nil {
		t.Fatalf("decode setup cache: %v", err)
	}
	return cache
}

func setupTestFixture(t *testing.T) (string, string) {
	t.Helper()
	cwd := t.TempDir()
	repoRoot := cwd
	if err := os.MkdirAll(filepath.Join(repoRoot, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "skills", "nana-setup"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "prompts", "executor.md"), []byte("# executor\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "skills", "nana-setup", "SKILL.md"), []byte("# skill\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "templates", "AGENTS.md"), []byte("template ~/.codex\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return cwd, repoRoot
}
