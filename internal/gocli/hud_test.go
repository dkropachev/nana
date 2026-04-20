package gocli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHUDHelp(t *testing.T) {
	output, err := captureStdout(t, func() error {
		return HUD(t.TempDir(), "/tmp/nana", []string{"--help"})
	})
	if err != nil {
		t.Fatalf("HUD(--help): %v", err)
	}
	if !strings.Contains(output, "nana hud") || !strings.Contains(output, "--watch") {
		t.Fatalf("unexpected HUD help output: %q", output)
	}
}

func TestHUDJSONOutput(t *testing.T) {
	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	writeJSON := func(path string, value any) {
		t.Helper()
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", path, err)
		}
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	writeJSON(filepath.Join(stateDir, "team-state.json"), map[string]any{
		"active":      true,
		"agent_count": 3,
	})
	writeJSON(filepath.Join(cwd, ".nana", "metrics.json"), map[string]any{
		"total_turns":    10,
		"session_turns":  4,
		"last_activity":  time.Now().UTC().Format(time.RFC3339Nano),
		"session_tokens": 123,
	})
	writeJSON(filepath.Join(stateDir, "session.json"), map[string]any{
		"session_id": "sess-1",
		"started_at": time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano),
	})
	codexHome := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary": {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
		},
		Active: "primary",
	})

	output, err := captureStdout(t, func() error {
		return HUD(cwd, "/tmp/nana", []string{"--json"})
	})
	if err != nil {
		t.Fatalf("HUD(--json): %v", err)
	}

	var parsed HUDRenderContext
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("parse HUD json: %v\noutput=%s", err, output)
	}
	if parsed.Team == nil || parsed.Team.AgentCount != 3 {
		t.Fatalf("unexpected team payload: %+v", parsed.Team)
	}
	if parsed.Session == nil || parsed.Session.SessionID != "sess-1" {
		t.Fatalf("unexpected session payload: %+v", parsed.Session)
	}
	if parsed.Account == nil || parsed.Account.Active != "primary" {
		t.Fatalf("unexpected account payload: %+v", parsed.Account)
	}
}

func TestHUDSessionScopedModePrecedence(t *testing.T) {
	cwd := t.TempDir()
	rootStateDir := filepath.Join(cwd, ".nana", "state")
	sessionStateDir := filepath.Join(rootStateDir, "sessions", "sess-hud")
	if err := os.MkdirAll(sessionStateDir, 0o755); err != nil {
		t.Fatalf("mkdir session state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootStateDir, "session.json"), []byte(`{"session_id":"sess-hud","started_at":"2026-04-08T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write session.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootStateDir, "verify-loop-state.json"), []byte(`{"active":true,"iteration":9,"max_iterations":10}`), 0o644); err != nil {
		t.Fatalf("write root verify-loop: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionStateDir, "verify-loop-state.json"), []byte(`{"active":true,"iteration":2,"max_iterations":10}`), 0o644); err != nil {
		t.Fatalf("write session verify-loop: %v", err)
	}

	ctx, err := readAllHUDState(cwd, ResolvedHUDConfig{
		Preset: HUDPresetFocused,
		Git: HUDGitConfig{
			Display: "repo-branch",
		},
	})
	if err != nil {
		t.Fatalf("readAllHUDState: %v", err)
	}
	if ctx.VerifyLoop == nil || ctx.VerifyLoop.Iteration != 2 {
		t.Fatalf("expected session-scoped verify-loop state, got %+v", ctx.VerifyLoop)
	}
}

func TestHUDIncludesRuntimeRecoveryHints(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), ".codex"))

	rootStateDir := filepath.Join(cwd, ".nana", "state")
	sessionStateDir := filepath.Join(rootStateDir, "sessions", "sess-hud")
	logDir := filepath.Join(cwd, ".nana", "logs")
	if err := os.MkdirAll(sessionStateDir, 0o755); err != nil {
		t.Fatalf("mkdir session state: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootStateDir, "session.json"), []byte(`{"session_id":"sess-hud","started_at":"2026-04-08T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write session.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootStateDir, "autopilot-state.json"), []byte(`{"active":true,"current_phase":"root-phase"}`), 0o644); err != nil {
		t.Fatalf("write root autopilot state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionStateDir, "autopilot-state.json"), []byte(`{"active":true,"current_phase":"session-phase"}`), 0o644); err != nil {
		t.Fatalf("write session autopilot state: %v", err)
	}

	oldLog := filepath.Join(logDir, "old.log")
	newLog := filepath.Join(logDir, "new.log")
	if err := os.WriteFile(oldLog, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	if err := os.WriteFile(newLog, []byte("new"), 0o644); err != nil {
		t.Fatalf("write new log: %v", err)
	}
	oldTime := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	newTime := oldTime.Add(time.Minute)
	if err := os.Chtimes(oldLog, oldTime, oldTime); err != nil {
		t.Fatalf("chtime old log: %v", err)
	}
	if err := os.Chtimes(newLog, newTime, newTime); err != nil {
		t.Fatalf("chtime new log: %v", err)
	}
	if err := RecordRuntimeArtifact(cwd, newLog); err != nil {
		t.Fatalf("record runtime artifact: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return HUD(cwd, "/tmp/nana", []string{})
	})
	if err != nil {
		t.Fatalf("HUD(): %v", err)
	}

	wantState := filepath.Join(".nana", "state", "sessions", "sess-hud", "autopilot-state.json")
	wantArtifact := filepath.Join(".nana", "logs", "new.log")
	for _, needle := range []string{
		"mode:autopilot",
		"state:" + wantState,
		"artifact:" + wantArtifact,
		"cancel:$cancel",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected HUD output to contain %q, got %q", needle, output)
		}
	}

	jsonOutput, err := captureStdout(t, func() error {
		return HUD(cwd, "/tmp/nana", []string{"--json"})
	})
	if err != nil {
		t.Fatalf("HUD(--json): %v", err)
	}
	var parsed HUDRenderContext
	if err := json.Unmarshal([]byte(jsonOutput), &parsed); err != nil {
		t.Fatalf("parse HUD json: %v\noutput=%s", err, jsonOutput)
	}
	if parsed.Runtime == nil || parsed.Runtime.ActiveMode != "autopilot" || parsed.Runtime.StateFile != wantState || parsed.Runtime.LatestArtifact != wantArtifact || parsed.Runtime.CancelHint != "$cancel" {
		t.Fatalf("unexpected runtime recovery payload: %+v", parsed.Runtime)
	}
}

func TestBuildGitBranchLabelUsesConfiguredRepoLabel(t *testing.T) {
	cwd := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "hud-test@example.com"},
		{"config", "user.name", "HUD Test"},
		{"commit", "--allow-empty", "-m", "init"},
		{"checkout", "-b", "feature/test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}

	label := buildGitBranchLabel(cwd, ResolvedHUDConfig{
		Preset: HUDPresetFocused,
		Git: HUDGitConfig{
			Display:   "repo-branch",
			RepoLabel: "manual",
		},
	})
	if label != "manual/feature/test" {
		t.Fatalf("buildGitBranchLabel() = %q", label)
	}
}

func TestRenderHUDIncludesPendingAccountRestart(t *testing.T) {
	rendered := renderHUD(HUDRenderContext{
		Account: &HUDAccountState{
			Active:          "secondary",
			PendingActive:   "primary",
			RestartRequired: true,
		},
	}, HUDPresetFocused)
	if !strings.Contains(rendered, "account:secondary->primary:restart") {
		t.Fatalf("unexpected rendered HUD: %q", rendered)
	}
}
