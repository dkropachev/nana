package gocli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	defer r.Close()
	data, _ := io.ReadAll(r)
	return string(data), runErr
}

func captureOutput(t *testing.T, fn func() error) (string, string, error) {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	runErr := fn()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	defer stdoutR.Close()
	defer stderrR.Close()
	stdoutData, _ := io.ReadAll(stdoutR)
	stderrData, _ := io.ReadAll(stderrR)
	return string(stdoutData), string(stderrData), runErr
}

func TestReadAndUpsertTomlString(t *testing.T) {
	content := "model = \"gpt-5\"\n[tui]\ntheme = \"night\"\n"
	if got := ReadTopLevelTomlString(content, "model"); got != "gpt-5" {
		t.Fatalf("ReadTopLevelTomlString() = %q", got)
	}
	updated := UpsertTopLevelTomlString(content, ReasoningKey, "high")
	if !strings.Contains(updated, `model_reasoning_effort = "high"`) {
		t.Fatalf("missing inserted key in %q", updated)
	}
}

func TestStatusAndCancel(t *testing.T) {
	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state", "sessions", "sess-1")
	logDir := filepath.Join(cwd, ".nana", "logs")
	plansDir := filepath.Join(cwd, ".nana", "plans")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "state", "session.json"), []byte(`{"session_id":"sess-1"}`), 0o644); err != nil {
		t.Fatalf("session.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "nana-verify.json"), []byte(`{"commands":[]}`), 0o644); err != nil {
		t.Fatalf("nana-verify.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "team-state.json"), []byte(`{"active":true,"current_phase":"team-exec"}`), 0o644); err != nil {
		t.Fatalf("team-state.json: %v", err)
	}
	planPath := filepath.Join(plansDir, "prd-recovery.md")
	if err := os.WriteFile(planPath, []byte("# PRD: Recovery\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	hookLog := filepath.Join(logDir, "hooks-2026-04-08.jsonl")
	if err := os.WriteFile(hookLog, []byte(`{"event":"turn-complete"}`), 0o644); err != nil {
		t.Fatalf("write hook log: %v", err)
	}
	if err := RecordRuntimeArtifact(cwd, hookLog); err != nil {
		t.Fatalf("record runtime artifact: %v", err)
	}

	statusOutput, err := captureStdout(t, func() error { return Status(cwd) })
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	if !strings.Contains(statusOutput, "team: ACTIVE (phase: team-exec)") {
		t.Fatalf("unexpected status output: %q", statusOutput)
	}
	for _, needle := range []string{
		"Active mode: team",
		"State file: " + filepath.Join(".nana", "state", "sessions", "sess-1", "team-state.json"),
		"Latest artifact: " + filepath.Join(".nana", "logs", "hooks-2026-04-08.jsonl"),
		"Recovery: Run $cancel",
	} {
		if !strings.Contains(statusOutput, needle) {
			t.Fatalf("expected status output to contain %q, got %q", needle, statusOutput)
		}
	}

	cancelOutput, err := captureStdout(t, func() error { return Cancel(cwd) })
	if err != nil {
		t.Fatalf("Cancel(): %v", err)
	}
	if !strings.Contains(cancelOutput, "Cancelled: team") {
		t.Fatalf("unexpected cancel output: %q", cancelOutput)
	}
	for _, needle := range []string{
		"Recovery summary:",
		"Session: sess-1",
		"Affected state:",
		"team (was phase: team-exec): " + filepath.Join(".nana", "state", "sessions", "sess-1", "team-state.json"),
		"Open artifacts:",
		filepath.Join(".nana", "logs", "hooks-2026-04-08.jsonl"),
		"Pending plans:",
		filepath.Join(".nana", "plans", "prd-recovery.md"),
		"Safe next commands:",
		"nana status",
		"nana doctor",
		"nana verify --json",
	} {
		if !strings.Contains(cancelOutput, needle) {
			t.Fatalf("expected cancel output to contain %q, got %q", needle, cancelOutput)
		}
	}
	updated, err := os.ReadFile(filepath.Join(stateDir, "team-state.json"))
	if err != nil {
		t.Fatalf("read updated state: %v", err)
	}
	if !strings.Contains(string(updated), `"current_phase": "cancelled"`) {
		t.Fatalf("unexpected updated state: %s", updated)
	}
}

func TestCancelVerifyLoopAndLinkedUltrawork(t *testing.T) {
	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state", "sessions", "sess-verify")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "state", "session.json"), []byte(`{"session_id":"sess-verify"}`), 0o644); err != nil {
		t.Fatalf("session.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "verify-loop-state.json"), []byte(`{"active":true,"current_phase":"verifying","linked_mode":"ultrawork"}`), 0o644); err != nil {
		t.Fatalf("verify-loop-state.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ultrawork-state.json"), []byte(`{"active":true,"current_phase":"running"}`), 0o644); err != nil {
		t.Fatalf("ultrawork-state.json: %v", err)
	}

	cancelOutput, err := captureStdout(t, func() error { return Cancel(cwd) })
	if err != nil {
		t.Fatalf("Cancel(): %v", err)
	}
	if !strings.Contains(cancelOutput, "Cancelled: verify-loop") || !strings.Contains(cancelOutput, "Cancelled: ultrawork") {
		t.Fatalf("unexpected cancel output: %q", cancelOutput)
	}
	verifyLoopState, err := os.ReadFile(filepath.Join(stateDir, "verify-loop-state.json"))
	if err != nil {
		t.Fatalf("read verify-loop state: %v", err)
	}
	if !strings.Contains(string(verifyLoopState), `"current_phase": "cancelled"`) {
		t.Fatalf("unexpected verify-loop state: %s", verifyLoopState)
	}
}

func TestNonModeStateFilesAreIgnored(t *testing.T) {
	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "auth-state.json"), []byte(`{"active":"primary"}`), 0o644); err != nil {
		t.Fatalf("auth-state.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "unknown-state.json"), []byte(`{"active":true,"current_phase":"ignored"}`), 0o644); err != nil {
		t.Fatalf("unknown-state.json: %v", err)
	}

	statusOutput, err := captureStdout(t, func() error { return Status(cwd) })
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	if !strings.Contains(statusOutput, "No active modes.") {
		t.Fatalf("expected non-mode state files to be ignored, got %q", statusOutput)
	}
}

func TestReasoning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".nana", "codex-home"))
	if _, err := captureStdout(t, func() error { return Reasoning([]string{"high"}) }); err != nil {
		t.Fatalf("Reasoning(set): %v", err)
	}
	content, err := os.ReadFile(CodexConfigPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(content), `model_reasoning_effort = "high"`) {
		t.Fatalf("unexpected config: %s", content)
	}
	var userConfig nanaUserConfig
	if err := readGithubJSON(filepath.Join(home, ".nana", "config.json"), &userConfig); err != nil {
		t.Fatalf("read nana config: %v", err)
	}
	if userConfig.DefaultReasoningEffort != "high" {
		t.Fatalf("unexpected nana default: %#v", userConfig)
	}
	output, err := captureStdout(t, func() error { return Reasoning(nil) })
	if err != nil {
		t.Fatalf("Reasoning(read): %v", err)
	}
	if !strings.Contains(output, "Current model_reasoning_effort: high") {
		t.Fatalf("unexpected reasoning output: %q", output)
	}
	if !strings.Contains(output, "NANA default model_reasoning_effort: high") {
		t.Fatalf("missing nana default in output: %q", output)
	}
}

func TestConfigEffort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	output, err := captureStdout(t, func() error { return Config([]string{"set", "--effort", "xhigh"}) })
	if err != nil {
		t.Fatalf("Config(set): %v", err)
	}
	if !strings.Contains(output, "Set NANA default model_reasoning_effort=\"xhigh\"") {
		t.Fatalf("unexpected set output: %q", output)
	}
	var config nanaUserConfig
	if err := readGithubJSON(filepath.Join(home, ".nana", "config.json"), &config); err != nil {
		t.Fatalf("read nana config: %v", err)
	}
	if config.DefaultReasoningEffort != "xhigh" {
		t.Fatalf("unexpected config: %#v", config)
	}
	show, err := captureStdout(t, func() error { return Config([]string{"show"}) })
	if err != nil {
		t.Fatalf("Config(show): %v", err)
	}
	if !strings.Contains(show, "default model_reasoning_effort: xhigh") {
		t.Fatalf("unexpected show output: %q", show)
	}
}

func TestRouteExplainImplicitKeyword(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	output, err := captureStdout(t, func() error {
		return Route(t.TempDir(), []string{"--explain", "Please", "ANALYZE", "this"})
	})
	if err != nil {
		t.Fatalf("Route(): %v", err)
	}
	expectedRuntime := filepath.Join(DefaultUserCodexHome(home), "skills", "analyze", "RUNTIME.md")
	for _, expected := range []string{
		"Route preview:",
		"1. $analyze",
		`source: implicit keyword "ANALYZE"`,
		"case-insensitive keyword match anywhere",
		expectedRuntime,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected %q in route output, got %q", expected, output)
		}
	}
}

func TestRouteExplainUserScopeRuntimePathHonorsCodexHome(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "custom-codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	output, err := captureStdout(t, func() error {
		return Route(t.TempDir(), []string{"--explain", "Please", "ANALYZE", "this"})
	})
	if err != nil {
		t.Fatalf("Route(): %v", err)
	}
	expected := filepath.Join(codexHome, "skills", "analyze", "RUNTIME.md")
	if !strings.Contains(output, expected) {
		t.Fatalf("expected user runtime path %q in route output, got %q", expected, output)
	}
	if strings.Contains(output, "~/.codex/skills/analyze/RUNTIME.md") {
		t.Fatalf("user route output should not point at legacy runtime path, got %q", output)
	}
}

func TestRouteExplainProjectScopeUsesProjectRuntimePath(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "setup-scope.json"), []byte(`{"scope":"project"}`), 0o644); err != nil {
		t.Fatalf("write setup-scope: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Route(cwd, []string{"--explain", "Please", "ANALYZE", "this"})
	})
	if err != nil {
		t.Fatalf("Route(): %v", err)
	}
	expected := "./.codex/skills/analyze/RUNTIME.md"
	if !strings.Contains(output, expected) {
		t.Fatalf("expected project runtime path %q in route output, got %q", expected, output)
	}
	if strings.Contains(output, "~/.codex/skills/analyze/RUNTIME.md") {
		t.Fatalf("project route output should not point at user runtime path, got %q", output)
	}
}

func TestRouteExplainProjectScopeRuntimePathHonorsExplicitCodexHome(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "setup-scope.json"), []byte(`{"scope":"project"}`), 0o644); err != nil {
		t.Fatalf("write setup-scope: %v", err)
	}
	codexHome := filepath.Join(t.TempDir(), "explicit-codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	output, err := captureStdout(t, func() error {
		return Route(cwd, []string{"--explain", "Please", "ANALYZE", "this"})
	})
	if err != nil {
		t.Fatalf("Route(): %v", err)
	}
	expected := filepath.Join(codexHome, "skills", "analyze", "RUNTIME.md")
	if !strings.Contains(output, expected) {
		t.Fatalf("expected explicit CODEX_HOME runtime path %q in route output, got %q", expected, output)
	}
	if strings.Contains(output, "./.codex/skills/analyze/RUNTIME.md") {
		t.Fatalf("project route output should honor CODEX_HOME instead of project path, got %q", output)
	}
}

func TestRouteExplainImplicitKeywordAfterUnicodeCaseExpansion(t *testing.T) {
	preview := ExplainPromptRoute("Ⱥ ANALYZE this")
	if len(preview.Activations) != 1 {
		t.Fatalf("expected one activation, got %#v", preview.Activations)
	}
	activation := preview.Activations[0]
	if activation.Skill != "analyze" || activation.Source != "implicit keyword" {
		t.Fatalf("expected implicit analyze activation, got %#v", activation)
	}
	if activation.Trigger != "ANALYZE" {
		t.Fatalf("expected trigger to preserve original prompt bytes, got %q", activation.Trigger)
	}
	if activation.Start != len("Ⱥ ") {
		t.Fatalf("expected start index in original prompt, got %d", activation.Start)
	}
}

func TestRouteExplainImplicitKeywordsRespectTokenBoundaries(t *testing.T) {
	for _, prompt := range []string{
		"Please reconfigure this second step",
		"I want analysis",
	} {
		t.Run(prompt, func(t *testing.T) {
			preview := ExplainPromptRoute(prompt)
			if len(preview.Activations) != 0 {
				t.Fatalf("expected no activations for %q, got %#v", prompt, preview.Activations)
			}
			if preview.NoActivationReason == "" {
				t.Fatalf("expected no-activation reason for %q", prompt)
			}
		})
	}
}

func TestRouteExplainImplicitKeywordAllowsPunctuationDelimiters(t *testing.T) {
	prompt := "Please (ANALYZE), this"
	preview := ExplainPromptRoute(prompt)
	if len(preview.Activations) != 1 {
		t.Fatalf("expected one activation, got %#v", preview.Activations)
	}
	activation := preview.Activations[0]
	if activation.Skill != "analyze" || activation.Source != "implicit keyword" {
		t.Fatalf("expected implicit analyze activation, got %#v", activation)
	}
	if activation.Trigger != "ANALYZE" {
		t.Fatalf("expected trigger to exclude punctuation delimiters, got %q", activation.Trigger)
	}
	if activation.Start != strings.Index(prompt, "ANALYZE") {
		t.Fatalf("expected start index to exclude punctuation delimiters, got %d", activation.Start)
	}
}

func TestRouteExplainExplicitBeforeImplicit(t *testing.T) {
	preview := ExplainPromptRoute("$tdd please analyze the failing test")
	if len(preview.Activations) != 2 {
		t.Fatalf("expected two activations, got %#v", preview.Activations)
	}
	if preview.Activations[0].Skill != "tdd" || preview.Activations[0].Source != "explicit invocation" {
		t.Fatalf("expected explicit tdd first, got %#v", preview.Activations[0])
	}
	if preview.Activations[1].Skill != "analyze" || preview.Activations[1].Source != "implicit keyword" {
		t.Fatalf("expected implicit analyze second, got %#v", preview.Activations[1])
	}
	if len(preview.IgnoredTriggers) != 0 {
		t.Fatalf("explicit $tdd token should not be re-reported as an ignored implicit keyword, got %#v", preview.IgnoredTriggers)
	}
}

func TestRouteExplainExplicitPrecedenceListsIgnoredImplicitTrigger(t *testing.T) {
	preview := ExplainPromptRoute("$tdd please test first")
	if len(preview.Activations) != 1 {
		t.Fatalf("expected one activation, got %#v", preview.Activations)
	}
	if preview.Activations[0].Skill != "tdd" || preview.Activations[0].Source != "explicit invocation" {
		t.Fatalf("expected explicit tdd activation, got %#v", preview.Activations[0])
	}
	if len(preview.IgnoredTriggers) != 1 {
		t.Fatalf("expected ignored implicit trigger for duplicate tdd activation, got %#v", preview.IgnoredTriggers)
	}
	ignored := preview.IgnoredTriggers[0]
	if ignored.Skill != "tdd" || ignored.Source != "implicit keyword" || ignored.Trigger != "test first" {
		t.Fatalf("expected ignored implicit tdd trigger, got %#v", ignored)
	}
	if !strings.Contains(ignored.Reason, "explicit $tdd activation takes precedence") {
		t.Fatalf("expected explicit precedence reason, got %#v", ignored)
	}

	output := FormatRoutePreview(preview)
	for _, expected := range []string{
		"Ignored triggers:",
		"source: implicit keyword \"test first\"",
		"why: ignored because explicit $tdd activation takes precedence for the same skill",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected %q in route output, got %q", expected, output)
		}
	}
}

func TestRouteExplainExplicitPrecedenceListsLaterImplicitTrigger(t *testing.T) {
	preview := ExplainPromptRoute("$tdd then tdd")
	if len(preview.Activations) != 1 {
		t.Fatalf("expected one activation, got %#v", preview.Activations)
	}
	if preview.Activations[0].Skill != "tdd" || preview.Activations[0].Source != "explicit invocation" {
		t.Fatalf("expected explicit tdd activation, got %#v", preview.Activations[0])
	}
	if len(preview.IgnoredTriggers) != 1 {
		t.Fatalf("expected later implicit tdd trigger to be ignored, got %#v", preview.IgnoredTriggers)
	}
	ignored := preview.IgnoredTriggers[0]
	if ignored.Skill != "tdd" || ignored.Source != "implicit keyword" || ignored.Trigger != "tdd" {
		t.Fatalf("expected ignored later implicit tdd trigger, got %#v", ignored)
	}
	if !strings.Contains(ignored.Reason, "explicit $tdd activation takes precedence") {
		t.Fatalf("expected explicit precedence reason, got %#v", ignored)
	}
}

func TestRouteExplainAdjacentDuplicateImplicitKeywordListsIgnoredTrigger(t *testing.T) {
	prompt := "tdd tdd"
	preview := ExplainPromptRoute(prompt)
	if len(preview.Activations) != 1 {
		t.Fatalf("expected one activation, got %#v", preview.Activations)
	}
	activation := preview.Activations[0]
	if activation.Skill != "tdd" || activation.Source != "implicit keyword" || activation.Trigger != "tdd" {
		t.Fatalf("expected first implicit tdd activation, got %#v", activation)
	}
	if activation.Start != 0 {
		t.Fatalf("expected first tdd activation at start 0, got %#v", activation.Start)
	}
	if len(preview.IgnoredTriggers) != 1 {
		t.Fatalf("expected duplicate implicit tdd trigger to be ignored, got %#v", preview.IgnoredTriggers)
	}
	ignored := preview.IgnoredTriggers[0]
	if ignored.Skill != "tdd" || ignored.Source != "implicit keyword" || ignored.Trigger != "tdd" {
		t.Fatalf("expected ignored duplicate implicit tdd trigger, got %#v", ignored)
	}
	if ignored.Start != strings.LastIndex(prompt, "tdd") {
		t.Fatalf("expected ignored tdd start index to point at duplicate, got %#v", ignored.Start)
	}
	if !strings.Contains(ignored.Reason, "one activation per skill") {
		t.Fatalf("expected one-activation-per-skill reason, got %#v", ignored)
	}
}

func TestRouteExplainDuplicateExplicitInvocationListsIgnoredTrigger(t *testing.T) {
	preview := ExplainPromptRoute("$tdd then $tdd")
	if len(preview.Activations) != 1 {
		t.Fatalf("expected one activation, got %#v", preview.Activations)
	}
	if len(preview.IgnoredTriggers) != 1 {
		t.Fatalf("expected duplicate explicit invocation to be ignored, got %#v", preview.IgnoredTriggers)
	}
	ignored := preview.IgnoredTriggers[0]
	if ignored.Skill != "tdd" || ignored.Source != "explicit invocation" || ignored.Trigger != "$tdd" {
		t.Fatalf("expected ignored duplicate explicit tdd trigger, got %#v", ignored)
	}
	if !strings.Contains(ignored.Reason, "duplicate explicit invocation") {
		t.Fatalf("expected duplicate explicit reason, got %#v", ignored)
	}
}

func TestRouteExplainExplicitInvocationAllowsPunctuationDelimiters(t *testing.T) {
	prompt := "Please run (`$tdd`) then analyze the failing test"
	preview := ExplainPromptRoute(prompt)
	if len(preview.Activations) != 2 {
		t.Fatalf("expected two activations, got %#v", preview.Activations)
	}
	explicit := preview.Activations[0]
	if explicit.Skill != "tdd" || explicit.Source != "explicit invocation" {
		t.Fatalf("expected punctuation-delimited explicit tdd first, got %#v", explicit)
	}
	if explicit.Trigger != "$tdd" {
		t.Fatalf("expected trigger to exclude punctuation delimiters, got %q", explicit.Trigger)
	}
	if explicit.Start != strings.Index(prompt, "$tdd") {
		t.Fatalf("expected explicit token start index, got %d", explicit.Start)
	}
	if preview.Activations[1].Skill != "analyze" || preview.Activations[1].Source != "implicit keyword" {
		t.Fatalf("expected implicit analyze second, got %#v", preview.Activations[1])
	}
}

func TestRouteExplainExplicitInvocationIgnoresUnknownShellVariables(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "")

	output, err := captureStdout(t, func() error {
		return Route(t.TempDir(), []string{"--explain", "Why", "is", "$PATH", "empty?"})
	})
	if err != nil {
		t.Fatalf("Route(): %v", err)
	}
	if !strings.Contains(output, "Activations: none") {
		t.Fatalf("expected no activations for shell variable prompt, got %q", output)
	}
	if strings.Contains(output, "$path") || strings.Contains(output, "skills/path") {
		t.Fatalf("shell variable should not be reported as a route activation, got %q", output)
	}
}

func TestRouteExplainUnknownExplicitTokenDoesNotSuppressImplicitKeyword(t *testing.T) {
	preview := ExplainPromptRoute("Why is $PATH empty? analyze this")
	if len(preview.Activations) != 1 {
		t.Fatalf("expected only the implicit analyze activation, got %#v", preview.Activations)
	}
	activation := preview.Activations[0]
	if activation.Skill != "analyze" || activation.Source != "implicit keyword" {
		t.Fatalf("expected implicit analyze activation, got %#v", activation)
	}
}

func TestRouteExplainUnknownDollarPrefixedKeywordStillAllowsLaterImplicitKeyword(t *testing.T) {
	preview := ExplainPromptRoute("$analyze-this please analyze")
	if len(preview.Activations) != 1 {
		t.Fatalf("expected later implicit analyze activation, got %#v", preview.Activations)
	}
	activation := preview.Activations[0]
	if activation.Skill != "analyze" || activation.Source != "implicit keyword" || activation.Trigger != "analyze" {
		t.Fatalf("expected later implicit analyze activation, got %#v", activation)
	}
	if activation.Start != strings.LastIndex("$analyze-this please analyze", "analyze") {
		t.Fatalf("expected later analyze start index, got %#v", activation.Start)
	}
}

func TestRouteExplainExplicitInvocationAllowsInstalledSkillDocs(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	installedSkillDir := filepath.Join(codexHome, "skills", "pipeline")
	if err := os.MkdirAll(installedSkillDir, 0o755); err != nil {
		t.Fatalf("mkdir installed skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(installedSkillDir, "SKILL.md"), []byte("---\nname: pipeline\n---\n"), 0o644); err != nil {
		t.Fatalf("write installed skill doc: %v", err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	cwd := t.TempDir()
	preview := ExplainPromptRouteForCWD(cwd, "$pipeline run this")
	if len(preview.Activations) != 1 {
		t.Fatalf("expected installed skill activation, got %#v", preview.Activations)
	}
	activation := preview.Activations[0]
	if activation.Skill != "pipeline" || activation.Source != "explicit invocation" {
		t.Fatalf("expected explicit pipeline activation, got %#v", activation)
	}
	expectedSkillPath := filepath.Join(codexHome, "skills", "pipeline", "SKILL.md")
	if activation.RuntimePath != expectedSkillPath || activation.DocLabel != routeDocLabelSkill {
		t.Fatalf("expected skill doc %q, got %#v", expectedSkillPath, activation)
	}

	output, err := captureStdout(t, func() error {
		return Route(cwd, []string{"--explain", "$pipeline", "run", "this"})
	})
	if err != nil {
		t.Fatalf("Route(): %v", err)
	}
	if !strings.Contains(output, "skill: "+expectedSkillPath) {
		t.Fatalf("expected installed skill doc path in route output, got %q", output)
	}
	if strings.Contains(output, filepath.Join("pipeline", "RUNTIME.md")) {
		t.Fatalf("route output should not report nonexistent runtime path, got %q", output)
	}
}

func TestRouteExplainPromptInvocationSuppressesKeywords(t *testing.T) {
	output, err := captureStdout(t, func() error {
		return Route(t.TempDir(), []string{"--explain", "/prompts:executor please analyze this and fix build"})
	})
	if err != nil {
		t.Fatalf("Route(): %v", err)
	}
	for _, expected := range []string{
		"Activations: none",
		"/prompts:executor suppresses implicit keyword routing",
		"Ignored triggers:",
		"$analyze",
		"source: implicit keyword \"analyze\"",
		"$build-fix",
		"source: implicit keyword \"fix build\"",
		"why: suppressed by /prompts:executor because /prompts:<name> disables implicit keyword routing",
		"Implicit keywords: suppressed by /prompts:executor",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected %q in route output, got %q", expected, output)
		}
	}
}

func TestRouteRulesStayInSyncWithAgentsTemplate(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repoRootFromCaller(t), "templates", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read template AGENTS.md: %v", err)
	}
	template := string(content)
	if !strings.Contains(template, "`nana route --explain \"<prompt>\"` to preview routing") {
		t.Fatalf("template AGENTS.md should document route preview CLI guidance")
	}
	if !strings.Contains(template, "Sync trigger tests with this list") {
		t.Fatalf("template AGENTS.md missing trigger synchronization guidance")
	}
	for _, rule := range routeRules {
		if !strings.Contains(template, "- `$"+rule.Skill+"`") {
			t.Fatalf("template AGENTS.md missing route skill %q", rule.Skill)
		}
		for _, keyword := range rule.Keywords {
			if !strings.Contains(template, "`"+keyword+"`") {
				t.Fatalf("template AGENTS.md missing route keyword %q for skill %q", keyword, rule.Skill)
			}
		}
	}
}

func TestResolveCodexHomeForLaunch(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	if got := ResolveCodexHomeForLaunch(cwd); got != DefaultUserCodexHome(home) {
		t.Fatalf("ResolveCodexHomeForLaunch(default) = %q", got)
	}

	if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "setup-scope.json"), []byte(`{"scope":"project"}`), 0o644); err != nil {
		t.Fatalf("write setup-scope: %v", err)
	}
	if got := ResolveCodexHomeForLaunch(cwd); got != filepath.Join(cwd, ".codex") {
		t.Fatalf("ResolveCodexHomeForLaunch(project) = %q", got)
	}

	t.Setenv("CODEX_HOME", filepath.Join(cwd, "explicit-codex-home"))
	if got := ResolveCodexHomeForLaunch(cwd); got != filepath.Join(cwd, "explicit-codex-home") {
		t.Fatalf("ResolveCodexHomeForLaunch(explicit) = %q", got)
	}
}

func TestResolveInvestigateCodexHome(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(cwd, "main-codex-home"))

	if got := ResolveInvestigateCodexHome(cwd); got != DefaultUserInvestigateCodexHome(home) {
		t.Fatalf("ResolveInvestigateCodexHome(default) = %q", got)
	}

	if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "setup-scope.json"), []byte(`{"scope":"project"}`), 0o644); err != nil {
		t.Fatalf("write setup-scope: %v", err)
	}
	if got := ResolveInvestigateCodexHome(cwd); got != filepath.Join(cwd, ".nana", "codex-home-investigate") {
		t.Fatalf("ResolveInvestigateCodexHome(project) = %q", got)
	}
}

func TestAccountPull(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".nana", "codex-home"))
	source := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(source, []byte(`{"token":"abc"}`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	output, err := captureStdout(t, AccountPull)
	if err != nil {
		t.Fatalf("AccountPull(): %v", err)
	}
	if !strings.Contains(output, `Registered Codex credentials as account "primary"`) {
		t.Fatalf("unexpected output: %q", output)
	}
	target, err := os.ReadFile(ResolvedCodexAuthPath())
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(target) != `{"token":"abc"}` {
		t.Fatalf("unexpected target: %s", target)
	}
}
