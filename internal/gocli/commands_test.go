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
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "state", "session.json"), []byte(`{"session_id":"sess-1"}`), 0o644); err != nil {
		t.Fatalf("session.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "team-state.json"), []byte(`{"active":true,"current_phase":"team-exec"}`), 0o644); err != nil {
		t.Fatalf("team-state.json: %v", err)
	}

	statusOutput, err := captureStdout(t, func() error { return Status(cwd) })
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	if !strings.Contains(statusOutput, "team: ACTIVE (phase: team-exec)") {
		t.Fatalf("unexpected status output: %q", statusOutput)
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
	output, err := captureStdout(t, func() error { return Reasoning(nil) })
	if err != nil {
		t.Fatalf("Reasoning(read): %v", err)
	}
	if !strings.Contains(output, "Current model_reasoning_effort: high") {
		t.Fatalf("unexpected reasoning output: %q", output)
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

func TestAuthPull(t *testing.T) {
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

	output, err := captureStdout(t, AuthPull)
	if err != nil {
		t.Fatalf("AuthPull(): %v", err)
	}
	if !strings.Contains(output, "Pulled Codex credentials") {
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
