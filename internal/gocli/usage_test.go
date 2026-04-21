package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverUsageSessionRoots(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".nana", "codex-home"))

	rootsToCreate := []string{
		filepath.Join(home, ".nana", "codex-home", "sessions"),
		filepath.Join(home, ".nana", "codex-home-investigate", "sessions"),
		filepath.Join(home, ".nana", "work", "sandboxes", "repo-1", "lw-1", ".nana", "work", "codex-home", "leader", "sessions"),
		filepath.Join(home, ".nana", "work", "sandboxes", "repo-2", "lw-2", ".nana", "work-local", "codex-home", "reviewer", "sessions"),
		filepath.Join(home, ".nana", "work", "repos", "acme", "widget", ".nana", "start", "codex-home", "triage", "sessions"),
		filepath.Join(cwd, ".nana", "state", "investigate-probes", "probe-a", "sessions"),
		filepath.Join(home, ".nana", "work", "sandboxes", "repo-3", "lw-3", ".nana", "state", "sessions"),
	}
	for _, dir := range rootsToCreate {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir root %s: %v", dir, err)
		}
	}

	discovered, err := discoverUsageSessionRoots(cwd)
	if err != nil {
		t.Fatalf("discoverUsageSessionRoots(): %v", err)
	}

	got := map[string]int{}
	for _, root := range discovered {
		got[root.Name]++
		if strings.Contains(filepath.ToSlash(root.SessionsDir), "/.nana/state/sessions") {
			t.Fatalf("unexpected state sessions root included: %s", root.SessionsDir)
		}
	}

	expected := map[string]int{
		"main":        1,
		"investigate": 2,
		"work":        1,
		"local-work":  1,
		"start":       1,
	}
	for key, want := range expected {
		if got[key] != want {
			t.Fatalf("root %s count=%d want=%d discovered=%+v", key, got[key], want, discovered)
		}
	}
}

func TestCollectUsageRecordsClassifiesRootsAndPhases(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".nana", "codex-home"))

	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home", "sessions"), usageRolloutFixture{
		SessionID: "main-scout",
		Timestamp: "2026-04-15T12:00:00Z",
		CWD:       cwd,
		Model:     "gpt-5.4",
		ExtraLines: []map[string]any{
			{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "improvement-scout\n- Inspect repo: nana\n- Max findings/issues to emit: 5"}}}},
		},
		TokenSnapshots: []usageTokenSnapshot{{Input: 100, Output: 20, Total: 120}},
	})
	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home-investigate", "sessions"), usageRolloutFixture{
		SessionID:      "investigate-run",
		Timestamp:      "2026-04-15T13:00:00Z",
		CWD:            cwd,
		Model:          "gpt-5.4",
		AgentRole:      "investigator",
		TokenSnapshots: []usageTokenSnapshot{{Input: 80, Output: 10, Total: 90}},
	})
	writeUsageRollout(t, filepath.Join(home, ".nana", "work", "sandboxes", "repo-1", "lw-1", ".nana", "work", "codex-home", "leader", "sessions"), usageRolloutFixture{
		SessionID:      "work-leader",
		Timestamp:      "2026-04-15T14:00:00Z",
		CWD:            filepath.Join(home, ".nana", "work", "sandboxes", "repo-1", "lw-1", "repo"),
		Model:          "gpt-5.4",
		AgentRole:      "leader",
		TokenSnapshots: []usageTokenSnapshot{{Input: 200, Output: 30, Total: 230}},
	})
	writeUsageRollout(t, filepath.Join(home, ".nana", "work", "sandboxes", "repo-2", "lw-2", ".nana", "work-local", "codex-home", "reviewer", "sessions"), usageRolloutFixture{
		SessionID:      "local-reviewer",
		Timestamp:      "2026-04-15T15:00:00Z",
		CWD:            filepath.Join(home, ".nana", "work", "sandboxes", "repo-2", "lw-2", "repo"),
		Model:          "gpt-5.4",
		AgentRole:      "reviewer",
		TokenSnapshots: []usageTokenSnapshot{{Input: 100, Output: 10, Total: 110}, {Input: 140, Output: 12, Total: 152}},
	})
	writeUsageRollout(t, filepath.Join(home, ".nana", "work", "repos", "acme", "widget", ".nana", "start", "codex-home", "triage", "sessions"), usageRolloutFixture{
		SessionID:      "start-triage",
		Timestamp:      "2026-04-15T16:00:00Z",
		CWD:            filepath.Join(home, ".nana", "work", "repos", "acme", "widget", "source"),
		Model:          "gpt-5.4",
		AgentRole:      "triage",
		TokenSnapshots: []usageTokenSnapshot{{Input: 50, Output: 5, Total: 55}},
	})

	records, scanned, err := collectUsageRecords(usageOptions{View: "summary", Root: "all", Limit: 10, CWD: cwd})
	if err != nil {
		t.Fatalf("collectUsageRecords(): %v", err)
	}
	if scanned != 5 {
		t.Fatalf("sessionRootsScanned=%d want=5", scanned)
	}
	if len(records) != 5 {
		t.Fatalf("len(records)=%d want=5", len(records))
	}

	byID := map[string]usageRecord{}
	for _, record := range records {
		byID[record.SessionID] = record
	}

	if got := byID["main-scout"]; got.Activity != "scout" || got.Phase != "scout" || got.Root != "main" {
		t.Fatalf("unexpected main scout record: %+v", got)
	}
	if got := byID["investigate-run"]; got.Activity != "investigate" || got.Phase != "investigate" || got.Root != "investigate" {
		t.Fatalf("unexpected investigate record: %+v", got)
	}
	if got := byID["work-leader"]; got.Activity != "work" || got.Phase != "implementation" || got.Lane != "leader" {
		t.Fatalf("unexpected work leader record: %+v", got)
	}
	if got := byID["local-reviewer"]; got.Activity != "work" || got.Phase != "review" || got.TotalTokens != 152 {
		t.Fatalf("unexpected local reviewer record: %+v", got)
	}
	if got := byID["start-triage"]; got.Activity != "triage" || got.Phase != "triage" || got.Root != "start" {
		t.Fatalf("unexpected start triage record: %+v", got)
	}
}

func TestCollectUsageRecordsProjectCurrentMatchesSandboxRepoID(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".nana", "codex-home"))

	repoID := localWorkRepoID(cwd)
	sandboxRepo := filepath.Join(home, ".nana", "work", "sandboxes", repoID, "lw-1", "repo")
	writeUsageRollout(t, filepath.Join(home, ".nana", "work", "sandboxes", repoID, "lw-1", ".nana", "work", "codex-home", "leader", "sessions"), usageRolloutFixture{
		SessionID:      "sandbox-run",
		Timestamp:      "2026-04-15T17:00:00Z",
		CWD:            sandboxRepo,
		Model:          "gpt-5.4",
		AgentRole:      "leader",
		TokenSnapshots: []usageTokenSnapshot{{Input: 60, Output: 6, Total: 66}},
	})

	records, _, err := collectUsageRecords(usageOptions{View: "summary", Root: "all", Limit: 10, Project: "current", CWD: cwd})
	if err != nil {
		t.Fatalf("collectUsageRecords(): %v", err)
	}
	if len(records) != 1 || records[0].SessionID != "sandbox-run" {
		t.Fatalf("unexpected project=current records: %+v", records)
	}
}

func TestUsageSummaryAndTopJSON(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".nana", "codex-home"))

	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home", "sessions"), usageRolloutFixture{
		SessionID:      "summary-a",
		Timestamp:      "2026-04-15T10:00:00Z",
		CWD:            cwd,
		Model:          "gpt-5.4",
		TokenSnapshots: []usageTokenSnapshot{{Input: 10, Output: 2, Total: 12}},
	})
	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home", "sessions"), usageRolloutFixture{
		SessionID:      "summary-b",
		Timestamp:      "2026-04-15T11:00:00Z",
		CWD:            cwd,
		Model:          "gpt-5.4",
		TokenSnapshots: []usageTokenSnapshot{{Input: 20, Output: 3, Total: 23}},
	})

	summaryOutput, err := captureStdout(t, func() error { return Usage(cwd, []string{"summary", "--json"}) })
	if err != nil {
		t.Fatalf("Usage(summary): %v", err)
	}
	var summary usageSummaryReport
	if err := json.Unmarshal([]byte(summaryOutput), &summary); err != nil {
		t.Fatalf("unmarshal summary output: %v\n%s", err, summaryOutput)
	}
	if summary.Totals.TotalTokens != 35 || summary.Totals.Sessions != 2 {
		t.Fatalf("unexpected summary totals: %+v", summary.Totals)
	}

	topOutput, err := captureStdout(t, func() error { return Usage(cwd, []string{"top", "--json"}) })
	if err != nil {
		t.Fatalf("Usage(top): %v", err)
	}
	var top usageTopReport
	if err := json.Unmarshal([]byte(topOutput), &top); err != nil {
		t.Fatalf("unmarshal top output: %v\n%s", err, topOutput)
	}
	if len(top.Sessions) != 2 || top.Sessions[0].SessionID != "summary-b" {
		t.Fatalf("unexpected top sessions: %+v", top.Sessions)
	}
}

func TestUsageSinceUsesWindowDeltas(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".nana", "codex-home"))

	now := time.Now().UTC()
	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home", "sessions"), usageRolloutFixture{
		SessionID: "window-delta",
		Timestamp: now.Add(-2 * time.Hour).Format(time.RFC3339),
		CWD:       cwd,
		Model:     "gpt-5.4",
		TokenSnapshots: []usageTokenSnapshot{
			{Timestamp: now.Add(-2 * time.Hour).Format(time.RFC3339), Input: 100, Total: 100},
			{Timestamp: now.Add(-30 * time.Minute).Format(time.RFC3339), Input: 150, Total: 150},
			{Timestamp: now.Add(-10 * time.Minute).Format(time.RFC3339), Input: 190, Total: 190},
		},
	})

	output, err := captureStdout(t, func() error { return Usage(cwd, []string{"summary", "--since", "1h", "--json"}) })
	if err != nil {
		t.Fatalf("Usage(summary --since): %v", err)
	}
	var summary usageSummaryReport
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatalf("unmarshal summary output: %v\n%s", err, output)
	}
	if summary.TimeBasis != usageTimeBasisWindowDelta || summary.Coverage != usageCoverageFull {
		t.Fatalf("unexpected usage metadata: %+v", summary)
	}
	if summary.Totals.TotalTokens != 90 || summary.Totals.InputTokens != 90 || summary.Totals.Sessions != 1 {
		t.Fatalf("unexpected windowed summary totals: %+v", summary.Totals)
	}
}

func TestUsageGroupByDayUsesCheckpointTimestamps(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".nana", "codex-home"))

	now := time.Now().UTC()
	firstCheckpoint := now.Add(-26 * time.Hour)
	secondCheckpoint := now.Add(-2 * time.Hour)
	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home", "sessions"), usageRolloutFixture{
		SessionID: "window-day",
		Timestamp: firstCheckpoint.Add(-time.Hour).Format(time.RFC3339),
		CWD:       cwd,
		Model:     "gpt-5.4",
		TokenSnapshots: []usageTokenSnapshot{
			{Timestamp: firstCheckpoint.Format(time.RFC3339), Input: 100, Total: 100},
			{Timestamp: secondCheckpoint.Format(time.RFC3339), Input: 240, Total: 240},
		},
	})

	output, err := captureStdout(t, func() error { return Usage(cwd, []string{"group", "--by", "day", "--since", "48h", "--json"}) })
	if err != nil {
		t.Fatalf("Usage(group --by day --since): %v", err)
	}
	var report usageTopReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("unmarshal day-group output: %v\n%s", err, output)
	}
	if report.TimeBasis != usageTimeBasisWindowDelta {
		t.Fatalf("unexpected time basis: %+v", report)
	}
	byDay := map[string]int{}
	for _, group := range report.Groups {
		byDay[group.Key] = group.TotalTokens
	}
	if len(byDay) != 2 {
		t.Fatalf("expected 2 day buckets, got %+v", report.Groups)
	}
	if byDay[firstCheckpoint.Format("2006-01-02")] != 100 || byDay[secondCheckpoint.Format("2006-01-02")] != 140 {
		t.Fatalf("unexpected day buckets: %+v", report.Groups)
	}
}

type usageRolloutFixture struct {
	SessionID      string
	Timestamp      string
	CWD            string
	Model          string
	AgentRole      string
	AgentNickname  string
	TokenSnapshots []usageTokenSnapshot
	ExtraLines     []map[string]any
}

type usageTokenSnapshot struct {
	Timestamp       string
	Input           int
	CachedInput     int
	Output          int
	ReasoningOutput int
	Total           int
}

func writeUsageRollout(t testing.TB, sessionsRoot string, fixture usageRolloutFixture) string {
	t.Helper()
	timestamp, err := time.Parse(time.RFC3339, fixture.Timestamp)
	if err != nil {
		t.Fatalf("parse fixture timestamp: %v", err)
	}
	dir := filepath.Join(sessionsRoot, timestamp.Format("2006"), timestamp.Format("01"), timestamp.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	file := filepath.Join(dir, "rollout-"+timestamp.Format("2006-01-02T15-04-05")+"-"+fixture.SessionID+".jsonl")
	handle, err := os.Create(file)
	if err != nil {
		t.Fatalf("create rollout file: %v", err)
	}
	defer handle.Close()

	lines := []map[string]any{
		{
			"type": "session_meta",
			"payload": map[string]any{
				"id":             fixture.SessionID,
				"timestamp":      fixture.Timestamp,
				"cwd":            fixture.CWD,
				"agent_role":     fixture.AgentRole,
				"agent_nickname": fixture.AgentNickname,
			},
		},
		{
			"type": "turn_context",
			"payload": map[string]any{
				"model": fixture.Model,
			},
		},
	}
	lines = append(lines, fixture.ExtraLines...)
	for index, snapshot := range fixture.TokenSnapshots {
		eventTimestamp := strings.TrimSpace(snapshot.Timestamp)
		if eventTimestamp == "" {
			eventTimestamp = timestamp.Add(time.Duration(index+1) * time.Second).UTC().Format(time.RFC3339)
		}
		lines = append(lines, map[string]any{
			"timestamp": eventTimestamp,
			"type":      "event_msg",
			"payload": map[string]any{
				"type": "token_count",
				"info": map[string]any{
					"total_token_usage": map[string]any{
						"input_tokens":            snapshot.Input,
						"cached_input_tokens":     snapshot.CachedInput,
						"output_tokens":           snapshot.Output,
						"reasoning_output_tokens": snapshot.ReasoningOutput,
						"total_tokens":            snapshot.Total,
					},
				},
			},
		})
	}
	for _, line := range lines {
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatalf("marshal line: %v", err)
		}
		if _, err := handle.Write(append(encoded, '\n')); err != nil {
			t.Fatalf("write line: %v", err)
		}
	}
	return file
}
