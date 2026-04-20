package gocli

import (
	"os"
	"path/filepath"
	"testing"
)

func writeActiveRuntimeModeState(t *testing.T, cwd string, mode string) {
	t.Helper()
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	writeActiveRuntimeModeStateFile(t, stateDir, mode)
}

func writeActiveRuntimeModeStateFile(t *testing.T, stateDir string, mode string) {
	t.Helper()
	name := ""
	for candidate, candidateMode := range canonicalModeStateFiles {
		if candidateMode == mode {
			name = candidate
			break
		}
	}
	if name == "" {
		t.Fatalf("unknown mode %q", mode)
	}
	if err := os.WriteFile(filepath.Join(stateDir, name), []byte(`{"active":true,"current_phase":"testing"}`), 0o644); err != nil {
		t.Fatalf("write %s state: %v", mode, err)
	}
}

func writeCurrentRuntimeSession(t *testing.T, cwd string, sessionID string) string {
	t.Helper()
	rootStateDir := filepath.Join(cwd, ".nana", "state")
	sessionStateDir := filepath.Join(rootStateDir, "sessions", sessionID)
	if err := os.MkdirAll(sessionStateDir, 0o755); err != nil {
		t.Fatalf("mkdir session state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootStateDir, "session.json"), []byte(`{"session_id":"`+sessionID+`"}`), 0o644); err != nil {
		t.Fatalf("write session.json: %v", err)
	}
	return sessionStateDir
}

func TestBuildRuntimeRecoveryStatusUsesRecordedLatestArtifact(t *testing.T) {
	cwd := t.TempDir()
	writeActiveRuntimeModeState(t, cwd, "team")
	logPath := filepath.Join(cwd, ".nana", "logs", "hooks-2026-04-08.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(logPath, []byte(`{"event":"turn-complete"}`), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := RecordRuntimeArtifact(cwd, logPath); err != nil {
		t.Fatalf("RecordRuntimeArtifact: %v", err)
	}

	status, err := BuildRuntimeRecoveryStatus(cwd)
	if err != nil {
		t.Fatalf("BuildRuntimeRecoveryStatus: %v", err)
	}
	if status == nil {
		t.Fatalf("expected active runtime status")
	}
	want := filepath.Join(".nana", "logs", "hooks-2026-04-08.jsonl")
	if status.LatestArtifact != want {
		t.Fatalf("LatestArtifact = %q, want %q (status=%+v)", status.LatestArtifact, want, status)
	}
}

func TestBuildRuntimeRecoveryStatusScopesLatestArtifactToCurrentSession(t *testing.T) {
	cwd := t.TempDir()
	sessionStateDir := writeCurrentRuntimeSession(t, cwd, "sess-current")
	writeActiveRuntimeModeStateFile(t, sessionStateDir, "team")
	logDir := filepath.Join(cwd, ".nana", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	oldLog := filepath.Join(logDir, "old-session.jsonl")
	currentLog := filepath.Join(logDir, "current-session.jsonl")
	if err := os.WriteFile(oldLog, []byte(`{"event":"old"}`), 0o644); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	if err := os.WriteFile(currentLog, []byte(`{"event":"current"}`), 0o644); err != nil {
		t.Fatalf("write current log: %v", err)
	}
	if err := os.WriteFile(legacyLatestRuntimeArtifactPointerPath(cwd), []byte(`{"path":".nana/logs/old-session.jsonl"}`), 0o644); err != nil {
		t.Fatalf("write legacy root artifact pointer: %v", err)
	}

	status, err := BuildRuntimeRecoveryStatus(cwd)
	if err != nil {
		t.Fatalf("BuildRuntimeRecoveryStatus: %v", err)
	}
	if status == nil {
		t.Fatalf("expected active runtime status")
	}
	if status.LatestArtifact != "" {
		t.Fatalf("LatestArtifact = %q, want empty for legacy root pointer from another session", status.LatestArtifact)
	}

	if err := RecordRuntimeArtifact(cwd, currentLog); err != nil {
		t.Fatalf("RecordRuntimeArtifact: %v", err)
	}
	status, err = BuildRuntimeRecoveryStatus(cwd)
	if err != nil {
		t.Fatalf("BuildRuntimeRecoveryStatus after RecordRuntimeArtifact: %v", err)
	}
	want := filepath.Join(".nana", "logs", "current-session.jsonl")
	if status.LatestArtifact != want {
		t.Fatalf("LatestArtifact = %q, want %q (status=%+v)", status.LatestArtifact, want, status)
	}
	sessionPointerPath := filepath.Join(sessionStateDir, runtimeLatestArtifactFileName)
	if _, err := os.Stat(sessionPointerPath); err != nil {
		t.Fatalf("expected session-scoped artifact pointer at %s: %v", sessionPointerPath, err)
	}
}

func TestBuildRuntimeRecoveryStatusIgnoresUnregisteredLogFiles(t *testing.T) {
	cwd := t.TempDir()
	writeActiveRuntimeModeState(t, cwd, "team")
	unregistered := filepath.Join(cwd, ".nana", "logs", "nested", "newest.log")
	if err := os.MkdirAll(filepath.Dir(unregistered), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(unregistered, []byte("new"), 0o644); err != nil {
		t.Fatalf("write unregistered log: %v", err)
	}

	status, err := BuildRuntimeRecoveryStatus(cwd)
	if err != nil {
		t.Fatalf("BuildRuntimeRecoveryStatus: %v", err)
	}
	if status == nil {
		t.Fatalf("expected active runtime status")
	}
	if status.LatestArtifact != "" {
		t.Fatalf("LatestArtifact = %q, want empty without the recorded pointer", status.LatestArtifact)
	}
}

func TestWriteGithubJSONRecordsRuntimeArtifactsUnderNanaLogs(t *testing.T) {
	cwd := t.TempDir()
	writeActiveRuntimeModeState(t, cwd, "team")
	manifestPath := filepath.Join(cwd, ".nana", "logs", "investigate", "run-1", "manifest.json")
	if err := writeGithubJSON(manifestPath, map[string]string{"status": "running"}); err != nil {
		t.Fatalf("writeGithubJSON: %v", err)
	}

	status, err := BuildRuntimeRecoveryStatus(cwd)
	if err != nil {
		t.Fatalf("BuildRuntimeRecoveryStatus: %v", err)
	}
	if status == nil {
		t.Fatalf("expected active runtime status")
	}
	want := filepath.Join(".nana", "logs", "investigate", "run-1", "manifest.json")
	if status.LatestArtifact != want {
		t.Fatalf("LatestArtifact = %q, want %q (status=%+v)", status.LatestArtifact, want, status)
	}
}
