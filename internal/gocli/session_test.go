package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSessionSearchArgs(t *testing.T) {
	parsed, err := ParseSessionSearchArgs([]string{"team", "api", "--limit", "5", "--project=current", "--json"})
	if err != nil {
		t.Fatalf("ParseSessionSearchArgs(): %v", err)
	}
	if parsed.Query != "team api" || parsed.Limit != 5 || parsed.Project != "current" || !parsed.JSON {
		t.Fatalf("unexpected parsed args: %+v", parsed)
	}
}

func TestSearchSessionHistory(t *testing.T) {
	cwd := t.TempDir()
	codexHomeDir := filepath.Join(cwd, ".codex-home")
	dir := filepath.Join(codexHomeDir, "sessions", "2026", "03", "10")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lines := []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "session-a", "timestamp": "2026-03-10T12:00:00.000Z", "cwd": cwd}},
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "Show previous discussions of team api in recent runs."}},
	}
	file := filepath.Join(dir, "rollout-2026-03-10T12-00-00-session-a.jsonl")
	handle, err := os.Create(file)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	for _, line := range lines {
		encoded, _ := json.Marshal(line)
		if _, err := handle.Write(append(encoded, '\n')); err != nil {
			t.Fatalf("write line: %v", err)
		}
	}
	_ = handle.Close()

	report, err := SearchSessionHistory(SessionSearchOptions{
		Query:        "team api",
		Project:      "current",
		JSON:         true,
		CWD:          cwd,
		CodexHomeDir: codexHomeDir,
	})
	if err != nil {
		t.Fatalf("SearchSessionHistory(): %v", err)
	}
	if report.Query != "team api" || len(report.Results) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Results[0].SessionID != "session-a" {
		t.Fatalf("unexpected session id: %+v", report.Results[0])
	}
}
