package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

func TestSearchSessionHistoryPrunesStandardSessionFilenameCandidates(t *testing.T) {
	cwd := t.TempDir()
	codexHomeDir := filepath.Join(cwd, ".codex-home")
	dir := filepath.Join(codexHomeDir, "sessions", "2026", "04", "18")
	for i := 0; i < 5; i++ {
		writeSessionRollout(t,
			filepath.Join(dir, "rollout-2026-04-18T12-00-0"+strconv.Itoa(i)+"-other-session.jsonl"),
			"other-session",
			"2026-04-18T12:00:00.000Z",
			cwd,
			"needle appears in a different session",
		)
	}
	writeSessionRollout(t,
		filepath.Join(dir, "rollout-2026-04-18T11-59-00-target-session.jsonl"),
		"target-session",
		"2026-04-18T11:59:00.000Z",
		cwd,
		"needle appears in the target session",
	)

	report, err := SearchSessionHistory(SessionSearchOptions{
		Query:        "needle",
		Session:      "target-session",
		Limit:        1,
		CWD:          cwd,
		CodexHomeDir: codexHomeDir,
	})
	if err != nil {
		t.Fatalf("SearchSessionHistory(): %v", err)
	}
	if len(report.Results) != 1 || report.Results[0].SessionID != "target-session" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.SearchedFiles != 1 {
		t.Fatalf("expected session path pruning to search one transcript, searched %d", report.SearchedFiles)
	}
}

func TestSearchSessionHistoryFindsFallbackStandardSessionID(t *testing.T) {
	cwd := t.TempDir()
	codexHomeDir := filepath.Join(cwd, ".codex-home")
	dir := filepath.Join(codexHomeDir, "sessions", "2026", "04", "18")
	sessionID := "2026-04-18T12-00-00-target-session"
	writeSessionRolloutWithoutMeta(t,
		filepath.Join(dir, "rollout-"+sessionID+".jsonl"),
		"needle appears in fallback-named target session",
	)
	writeSessionRolloutWithoutMeta(t,
		filepath.Join(dir, "rollout-2026-04-18T12-00-01-other-session.jsonl"),
		"needle appears in another fallback-named session",
	)

	report, err := SearchSessionHistory(SessionSearchOptions{
		Query:        "needle",
		Session:      sessionID,
		Limit:        1,
		CWD:          cwd,
		CodexHomeDir: codexHomeDir,
	})
	if err != nil {
		t.Fatalf("SearchSessionHistory(): %v", err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("expected one fallback-named result, got %+v", report)
	}
	if report.Results[0].SessionID != sessionID {
		t.Fatalf("expected fallback session id %q, got %+v", sessionID, report.Results[0])
	}
	if report.SearchedFiles != 1 {
		t.Fatalf("expected path pruning to keep only fallback-named target transcript, searched %d", report.SearchedFiles)
	}
}

func TestSearchSessionHistorySinceKeepsLegacyRolloutsInOldDateDirectories(t *testing.T) {
	cwd := t.TempDir()
	codexHomeDir := filepath.Join(cwd, ".codex-home")
	file := filepath.Join(codexHomeDir, "sessions", "2026", "04", "09", "rollout-legacy.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	if err := os.WriteFile(file, []byte(`{"type":"event_msg","payload":{"type":"user_message","message":"needle from legacy transcript"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy rollout: %v", err)
	}
	mtime, err := time.Parse(time.RFC3339, "2026-04-11T12:00:00Z")
	if err != nil {
		t.Fatalf("parse mtime: %v", err)
	}
	if err := os.Chtimes(file, mtime, mtime); err != nil {
		t.Fatalf("set legacy rollout mtime: %v", err)
	}

	report, err := SearchSessionHistory(SessionSearchOptions{
		Query:        "needle",
		Since:        "2026-04-10",
		Limit:        1,
		CWD:          cwd,
		CodexHomeDir: codexHomeDir,
	})
	if err != nil {
		t.Fatalf("SearchSessionHistory(): %v", err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("expected legacy rollout match, got %+v", report)
	}
	if report.Results[0].TranscriptPath != file {
		t.Fatalf("expected match from %s, got %+v", file, report.Results[0])
	}
	if report.Results[0].SessionID != "legacy" || report.Results[0].Timestamp != "" {
		t.Fatalf("expected fallback legacy metadata, got %+v", report.Results[0])
	}
}

func TestWalkRolloutFilesNewestKeepsNonstandardSessionFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	standardMatch := filepath.Join(root, "2026", "04", "18", "rollout-2026-04-18T12-00-00-target-session.jsonl")
	standardOther := filepath.Join(root, "2026", "04", "18", "rollout-2026-04-18T12-01-00-other-session.jsonl")
	fallback := filepath.Join(root, "2026", "04", "18", "rollout-loose.jsonl")
	writeSessionRollout(t, standardMatch, "target-session", "2026-04-18T12:00:00.000Z", t.TempDir(), "target")
	writeSessionRollout(t, standardOther, "other-session", "2026-04-18T12:01:00.000Z", t.TempDir(), "other")
	writeSessionRollout(t, fallback, "target-session", "2026-04-18T12:02:00.000Z", t.TempDir(), "fallback")

	visited := []string{}
	err := walkRolloutFilesNewest(root, rolloutWalkOptions{SessionFilter: "target-session"}, func(path string) (bool, error) {
		visited = append(visited, filepath.Base(path))
		return true, nil
	})
	if err != nil {
		t.Fatalf("walkRolloutFilesNewest(): %v", err)
	}
	expected := []string{"rollout-loose.jsonl", "rollout-2026-04-18T12-00-00-target-session.jsonl"}
	if len(visited) != len(expected) {
		t.Fatalf("expected visited %v, got %v", expected, visited)
	}
	for i := range expected {
		if visited[i] != expected[i] {
			t.Fatalf("expected visited %v, got %v", expected, visited)
		}
	}
}

func BenchmarkSearchSessionHistoryPrunedSession(b *testing.B) {
	cwd := b.TempDir()
	codexHomeDir := filepath.Join(cwd, ".codex-home")
	dir := filepath.Join(codexHomeDir, "sessions", "2026", "04", "18")
	for i := 0; i < 1000; i++ {
		writeSessionRollout(b,
			filepath.Join(dir, "rollout-2026-04-18T12-00-00-other-session-"+strconv.Itoa(i)+".jsonl"),
			"other-session-"+strconv.Itoa(i),
			"2026-04-18T12:00:00.000Z",
			cwd,
			"needle appears in another session",
		)
	}
	writeSessionRollout(b,
		filepath.Join(dir, "rollout-2026-04-18T11-59-00-target-session.jsonl"),
		"target-session",
		"2026-04-18T11:59:00.000Z",
		cwd,
		"needle appears in the target session",
	)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := SearchSessionHistory(SessionSearchOptions{
			Query:        "needle",
			Session:      "target-session",
			Limit:        1,
			CWD:          cwd,
			CodexHomeDir: codexHomeDir,
		}); err != nil {
			b.Fatalf("SearchSessionHistory(): %v", err)
		}
	}
}

func writeSessionRollout(t testing.TB, file string, id string, timestamp string, cwd string, message string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	lines := []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": id, "timestamp": timestamp, "cwd": cwd}},
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": message}},
	}
	handle, err := os.Create(file)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	for _, line := range lines {
		encoded, _ := json.Marshal(line)
		if _, err := handle.Write(append(encoded, '\n')); err != nil {
			_ = handle.Close()
			t.Fatalf("write line: %v", err)
		}
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close rollout: %v", err)
	}
}

func writeSessionRolloutWithoutMeta(t testing.TB, file string, message string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	line := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": message}}
	encoded, _ := json.Marshal(line)
	if err := os.WriteFile(file, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
}
