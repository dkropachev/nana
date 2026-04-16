package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func TestWalkRolloutFilesNewestFirstAndStops(t *testing.T) {
	codexHomeDir := t.TempDir()
	root := filepath.Join(codexHomeDir, "sessions")
	oldest := writeSessionRollout(t, codexHomeDir, time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC), "session-oldest", codexHomeDir, "older needle")
	middle := writeSessionRollout(t, codexHomeDir, time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC), "session-middle", codexHomeDir, "middle needle")
	newest := writeSessionRollout(t, codexHomeDir, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), "session-newest", codexHomeDir, "newer needle")

	visited := []string{}
	err := walkRolloutFiles(root, 0, func(path string) (bool, error) {
		visited = append(visited, path)
		return len(visited) == 2, nil
	})
	if err != nil {
		t.Fatalf("walkRolloutFiles(): %v", err)
	}
	if len(visited) != 2 {
		t.Fatalf("expected walk to stop after 2 files, visited %d: %#v", len(visited), visited)
	}
	if visited[0] != newest || visited[1] != middle {
		t.Fatalf("unexpected visit order: got %#v, want first two %#v", visited, []string{newest, middle})
	}
	for _, path := range visited {
		if path == oldest {
			t.Fatalf("walk did not stop before oldest candidate: %#v", visited)
		}
	}
}

func TestWalkRolloutFilesPrunesDatedDirsBeforeSince(t *testing.T) {
	useLocalLocation(t, time.UTC)

	codexHomeDir := t.TempDir()
	root := filepath.Join(codexHomeDir, "sessions")
	oldFile := writeSessionRollout(t, codexHomeDir, time.Date(2026, 3, 8, 23, 59, 0, 0, time.UTC), "session-old", codexHomeDir, "old needle")
	newFile := writeSessionRollout(t, codexHomeDir, time.Date(2026, 3, 10, 0, 1, 0, 0, time.UTC), "session-new", codexHomeDir, "new needle")
	cutoff := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC).UnixMilli()

	visited := []string{}
	err := walkRolloutFiles(root, cutoff, func(path string) (bool, error) {
		visited = append(visited, path)
		return false, nil
	})
	if err != nil {
		t.Fatalf("walkRolloutFiles(): %v", err)
	}
	if len(visited) != 1 || visited[0] != newFile {
		t.Fatalf("unexpected visited files with since cutoff: got %#v, want only %q", visited, newFile)
	}
	if visited[0] == oldFile {
		t.Fatalf("old dated directory was not pruned")
	}
}

func TestWalkRolloutFilesDoesNotFollowSymlinkedRoot(t *testing.T) {
	codexHomeDir := t.TempDir()
	outsideHomeDir := t.TempDir()
	outsideRoot := filepath.Join(outsideHomeDir, "sessions")
	outsideFile := writeSessionRollout(t, outsideHomeDir, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), "session-outside", outsideHomeDir, "outside needle")

	if err := os.MkdirAll(codexHomeDir, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	root := filepath.Join(codexHomeDir, "sessions")
	if err := os.Symlink(outsideRoot, root); err != nil {
		t.Skipf("symlink sessions root: %v", err)
	}

	visited := []string{}
	err := walkRolloutFiles(root, 0, func(path string) (bool, error) {
		visited = append(visited, path)
		return false, nil
	})
	if err != nil {
		t.Fatalf("walkRolloutFiles(): %v", err)
	}
	if len(visited) != 0 {
		t.Fatalf("symlinked sessions root escaped into %q; visited %#v", outsideFile, visited)
	}
}

func TestSearchSessionHistoryIncludesPreviousLocalDateAfterSinceCutoff(t *testing.T) {
	westOfUTC := time.FixedZone("UTC-04", -4*60*60)

	codexHomeDir := t.TempDir()
	cwd := filepath.Join(codexHomeDir, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	writeSessionRollout(t, codexHomeDir, time.Date(2026, 3, 9, 20, 30, 0, 0, westOfUTC), "session-local-previous-day", cwd, "timezone cutoff needle")

	useLocalLocation(t, time.UTC)
	report, err := SearchSessionHistory(SessionSearchOptions{
		Query:        "timezone cutoff needle",
		Since:        "2026-03-10",
		Project:      "current",
		CWD:          cwd,
		CodexHomeDir: codexHomeDir,
	})
	if err != nil {
		t.Fatalf("SearchSessionHistory(): %v", err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("expected previous local-date rollout after cutoff to match, got %+v", report)
	}
	if report.Results[0].SessionID != "session-local-previous-day" {
		t.Fatalf("unexpected result: %+v", report.Results[0])
	}
}

func useLocalLocation(t *testing.T, location *time.Location) {
	t.Helper()
	originalLocal := time.Local
	time.Local = location
	t.Cleanup(func() {
		time.Local = originalLocal
	})
}

func BenchmarkSearchSessionHistoryManyRolloutFiles(b *testing.B) {
	codexHomeDir := b.TempDir()
	cwd := filepath.Join(codexHomeDir, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		b.Fatalf("mkdir cwd: %v", err)
	}
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 1200; i++ {
		message := fmt.Sprintf("background message %d", i)
		if i == 0 {
			message = "needle target in newest rollout"
		}
		writeSessionRollout(b, codexHomeDir, base.Add(-time.Duration(i)*time.Hour), fmt.Sprintf("session-%04d", i), cwd, message)
	}

	opts := SessionSearchOptions{
		Query:        "needle target",
		Limit:        1,
		Project:      "current",
		CWD:          cwd,
		CodexHomeDir: codexHomeDir,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		report, err := SearchSessionHistory(opts)
		if err != nil {
			b.Fatalf("SearchSessionHistory(): %v", err)
		}
		if len(report.Results) != 1 {
			b.Fatalf("expected one result, got %+v", report)
		}
	}
}

func writeSessionRollout(tb testing.TB, codexHomeDir string, timestamp time.Time, sessionID string, cwd string, message string) string {
	tb.Helper()
	dir := filepath.Join(codexHomeDir, "sessions", timestamp.Format("2006"), timestamp.Format("01"), timestamp.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("mkdir rollout dir: %v", err)
	}
	file := filepath.Join(dir, fmt.Sprintf("rollout-%s-%s.jsonl", timestamp.Format("2006-01-02T15-04-05"), sessionID))
	lines := []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": sessionID, "timestamp": timestamp.UTC().Format(time.RFC3339), "cwd": cwd}},
		{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": message}},
	}
	handle, err := os.Create(file)
	if err != nil {
		tb.Fatalf("create rollout: %v", err)
	}
	for _, line := range lines {
		encoded, _ := json.Marshal(line)
		if _, err := handle.Write(append(encoded, '\n')); err != nil {
			_ = handle.Close()
			tb.Fatalf("write rollout: %v", err)
		}
	}
	if err := handle.Close(); err != nil {
		tb.Fatalf("close rollout: %v", err)
	}
	if err := os.Chtimes(file, timestamp, timestamp); err != nil {
		tb.Fatalf("chtime rollout: %v", err)
	}
	return file
}
