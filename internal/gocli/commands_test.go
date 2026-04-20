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
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "state", "session.json"), []byte(`{"session_id":"sess-1"}`), 0o644); err != nil {
		t.Fatalf("session.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "team-state.json"), []byte(`{"active":true,"current_phase":"team-exec"}`), 0o644); err != nil {
		t.Fatalf("team-state.json: %v", err)
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
