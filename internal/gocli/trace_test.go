package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceChildAgentTelemetryLogsAndSummarizes(t *testing.T) {
	cwd := t.TempDir()
	commands := [][]string{
		{"child-agent", "start", "--agent", "agent-1", "--role", "executor", "--parent", "wf-1", "--at", "2026-04-20T00:00:00Z"},
		{"child-agent", "start", "--agent", "agent-2", "--role", "verifier", "--parent", "wf-1", "--at", "2026-04-20T00:00:01Z"},
		{"child-agent", "queued", "--agent", "agent-3", "--role", "executor", "--parent", "wf-1", "--queue-depth", "1", "--at", "2026-04-20T00:00:02Z"},
		{"child-agent", "complete", "--agent", "agent-1", "--status", "completed", "--parent", "wf-1", "--at", "2026-04-20T00:00:05Z"},
	}
	for _, args := range commands {
		if _, err := captureStdout(t, func() error { return Trace(cwd, args) }); err != nil {
			t.Fatalf("Trace(%v): %v", args, err)
		}
	}

	logPath := filepath.Join(cwd, ".nana", "logs", "child-agents-2026-04-20.jsonl")
	logContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read trace log: %v", err)
	}
	if !strings.Contains(string(logContent), `"parent_workflow_id":"wf-1"`) || !strings.Contains(string(logContent), `"role":"executor"`) {
		t.Fatalf("trace log missing workflow or role: %s", logContent)
	}

	output, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agents", "--parent", "wf-1", "--json"})
	})
	if err != nil {
		t.Fatalf("Trace(summary): %v", err)
	}
	var summary childAgentTraceSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatalf("parse summary JSON: %v\n%s", err, output)
	}
	if summary.Events != 4 || summary.Started != 2 || summary.Completed != 1 || summary.Active != 1 || summary.Queued != 1 {
		t.Fatalf("unexpected summary counts: %+v", summary)
	}
	if summary.MaxActive != 2 || summary.MaxConcurrent != defaultChildAgentMaxConcurrent || summary.MaxQueueDepth != 1 || summary.CurrentQueueDepth != 1 {
		t.Fatalf("unexpected concurrency summary: %+v", summary)
	}
	if summary.AverageDurationMs != 5000 || summary.MaxDurationMs != 5000 {
		t.Fatalf("unexpected duration summary: %+v", summary)
	}
	if summary.Outcomes["completed"] != 1 {
		t.Fatalf("unexpected outcomes: %+v", summary.Outcomes)
	}
}

func TestTraceChildAgentTelemetryRecordsLatestRuntimeArtifact(t *testing.T) {
	cwd := t.TempDir()
	writeActiveRuntimeModeState(t, cwd, "ultrawork")

	if _, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agent", "start", "--agent", "agent-1", "--role", "executor", "--at", "2026-04-20T00:00:00Z"})
	}); err != nil {
		t.Fatalf("Trace(start): %v", err)
	}

	want := filepath.Join(".nana", "logs", "child-agents-2026-04-20.jsonl")
	if got := latestRuntimeArtifactPath(cwd); got != want {
		t.Fatalf("latestRuntimeArtifactPath() = %q, want %q", got, want)
	}
	status, err := BuildRuntimeRecoveryStatus(cwd)
	if err != nil {
		t.Fatalf("BuildRuntimeRecoveryStatus: %v", err)
	}
	if status == nil || status.LatestArtifact != want {
		t.Fatalf("expected trace log in runtime recovery status, got %+v", status)
	}
	if !strings.Contains(strings.Join(status.InspectPaths, "\n"), want) {
		t.Fatalf("InspectPaths missing trace log %q: %#v", want, status.InspectPaths)
	}
}

func TestTraceChildAgentSummaryReportsExplicitCurrentQueueDepth(t *testing.T) {
	cwd := t.TempDir()
	if _, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agent", "queued", "--agent", "agent-queued", "--role", "executor", "--parent", "wf-pressure", "--queue-depth", "5", "--at", "2026-04-20T00:00:00Z"})
	}); err != nil {
		t.Fatalf("Trace(queued): %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agents", "--parent", "wf-pressure", "--json"})
	})
	if err != nil {
		t.Fatalf("Trace(summary): %v", err)
	}
	var summary childAgentTraceSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatalf("parse summary JSON: %v\n%s", err, output)
	}
	if summary.Queued != 1 {
		t.Fatalf("expected one unique queued agent, got summary: %+v", summary)
	}
	if summary.CurrentQueueDepth != 5 || summary.MaxQueueDepth != 5 {
		t.Fatalf("explicit queue depth was not summarized as current pressure: %+v", summary)
	}
}

func TestTraceChildAgentSummaryRaisesMaxQueueDepthToVisibleQueuedAgents(t *testing.T) {
	cwd := t.TempDir()
	for _, args := range [][]string{
		{"child-agent", "queued", "--agent", "agent-1", "--role", "executor", "--parent", "wf-visible-pressure", "--queue-depth", "1", "--at", "2026-04-20T00:00:00Z"},
		{"child-agent", "queued", "--agent", "agent-2", "--role", "executor", "--parent", "wf-visible-pressure", "--queue-depth", "1", "--at", "2026-04-20T00:00:01Z"},
	} {
		if _, err := captureStdout(t, func() error { return Trace(cwd, args) }); err != nil {
			t.Fatalf("Trace(%v): %v", args, err)
		}
	}

	output, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agents", "--parent", "wf-visible-pressure", "--json"})
	})
	if err != nil {
		t.Fatalf("Trace(summary): %v", err)
	}
	var summary childAgentTraceSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatalf("parse summary JSON: %v\n%s", err, output)
	}
	if summary.Queued != 2 {
		t.Fatalf("expected two visible queued agents, got summary: %+v", summary)
	}
	if summary.CurrentQueueDepth != 2 || summary.MaxQueueDepth != 2 {
		t.Fatalf("visible queued agents should raise current and max queue depth: %+v", summary)
	}
}

func TestSummarizeChildAgentTraceUsesLatestExplicitQueueDepthFromStart(t *testing.T) {
	queueDepthFive := 5
	queueDepthZero := 0
	events := []childAgentTraceEvent{
		{
			SchemaVersion:    childAgentTraceSchemaVersion,
			Timestamp:        "2026-04-20T00:00:00Z",
			Event:            "queued",
			ParentWorkflowID: "wf-pressure",
			AgentID:          "agent-queued",
			QueueDepth:       &queueDepthFive,
		},
		{
			SchemaVersion:    childAgentTraceSchemaVersion,
			Timestamp:        "2026-04-20T00:00:01Z",
			Event:            "start",
			ParentWorkflowID: "wf-pressure",
			AgentID:          "agent-queued",
			QueueDepth:       &queueDepthZero,
		},
	}

	summary := summarizeChildAgentTrace(events)
	if summary.Queued != 0 {
		t.Fatalf("queued agent was not removed by start: %+v", summary)
	}
	if summary.CurrentQueueDepth != 0 || summary.MaxQueueDepth != 5 {
		t.Fatalf("latest explicit start queue depth was not preserved: %+v", summary)
	}
}

func TestSummarizeChildAgentTraceDecrementsQueueDepthWhenQueuedAgentStarts(t *testing.T) {
	queueDepthFive := 5
	events := []childAgentTraceEvent{
		{
			SchemaVersion:    childAgentTraceSchemaVersion,
			Timestamp:        "2026-04-20T00:00:00Z",
			Event:            "queued",
			ParentWorkflowID: "wf-pressure",
			AgentID:          "agent-queued",
			QueueDepth:       &queueDepthFive,
		},
		{
			SchemaVersion:    childAgentTraceSchemaVersion,
			Timestamp:        "2026-04-20T00:00:01Z",
			Event:            "start",
			ParentWorkflowID: "wf-pressure",
			AgentID:          "agent-queued",
		},
	}

	summary := summarizeChildAgentTrace(events)
	if summary.Queued != 0 {
		t.Fatalf("queued agent was not removed by start: %+v", summary)
	}
	if summary.CurrentQueueDepth != 4 || summary.MaxQueueDepth != 5 {
		t.Fatalf("queued-agent start left queue depth stale: %+v", summary)
	}
}

func TestTraceChildAgentCustomBudgetSurvivesCompleteWithoutMaxConcurrent(t *testing.T) {
	cwd := t.TempDir()
	for _, args := range [][]string{
		{"child-agent", "start", "--agent", "a-budget", "--role", "executor", "--parent", "wf-budget", "--max-concurrent", "3", "--at", "2026-04-20T00:00:00Z"},
		{"child-agent", "complete", "--agent", "a-budget", "--status", "completed", "--parent", "wf-budget", "--at", "2026-04-20T00:00:01Z"},
	} {
		if _, err := captureStdout(t, func() error { return Trace(cwd, args) }); err != nil {
			t.Fatalf("Trace(%v): %v", args, err)
		}
	}

	output, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agents", "--parent", "wf-budget", "--json"})
	})
	if err != nil {
		t.Fatalf("Trace(summary): %v", err)
	}
	var summary childAgentTraceSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatalf("parse summary JSON: %v\n%s", err, output)
	}
	if summary.MaxConcurrent != 3 {
		t.Fatalf("completion without --max-concurrent reset budget: %+v", summary)
	}

	logPath := filepath.Join(cwd, ".nana", "logs", "child-agents-2026-04-20.jsonl")
	logContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read trace log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logContent)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two log lines, got %d: %s", len(lines), logContent)
	}
	var started, completed childAgentTraceEvent
	if err := json.Unmarshal([]byte(lines[0]), &started); err != nil {
		t.Fatalf("parse start event: %v\n%s", err, lines[0])
	}
	if err := json.Unmarshal([]byte(lines[1]), &completed); err != nil {
		t.Fatalf("parse complete event: %v\n%s", err, lines[1])
	}
	if started.MaxConcurrent != 3 {
		t.Fatalf("start event did not persist explicit budget: %+v", started)
	}
	if completed.MaxConcurrent != 0 {
		t.Fatalf("complete event persisted implicit default budget: %+v", completed)
	}
}

func TestSummarizeChildAgentTraceIgnoresCompletionMaxConcurrent(t *testing.T) {
	events := []childAgentTraceEvent{
		{
			SchemaVersion:    childAgentTraceSchemaVersion,
			Timestamp:        "2026-04-20T00:00:00Z",
			Event:            "start",
			ParentWorkflowID: "wf-budget",
			AgentID:          "a-budget",
			MaxConcurrent:    3,
		},
		{
			SchemaVersion:    childAgentTraceSchemaVersion,
			Timestamp:        "2026-04-20T00:00:01Z",
			Event:            "complete",
			ParentWorkflowID: "wf-budget",
			AgentID:          "a-budget",
			Status:           "completed",
			MaxConcurrent:    defaultChildAgentMaxConcurrent,
		},
	}

	summary := summarizeChildAgentTrace(events)
	if summary.MaxConcurrent != 3 {
		t.Fatalf("completion event max_concurrent overwrote start budget: %+v", summary)
	}
}

func TestTraceChildAgentSummaryTextIncludesBudget(t *testing.T) {
	cwd := t.TempDir()
	for _, args := range [][]string{
		{"child-agent", "start", "--agent", "a1", "--role", "executor", "--parent", "wf-text", "--at", "2026-04-20T00:00:00Z"},
		{"child-agent", "complete", "--agent", "a1", "--status", "failed", "--parent", "wf-text", "--at", "2026-04-20T00:00:02Z"},
	} {
		if _, err := captureStdout(t, func() error { return Trace(cwd, args) }); err != nil {
			t.Fatalf("Trace(%v): %v", args, err)
		}
	}

	output, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agents", "--parent=wf-text"})
	})
	if err != nil {
		t.Fatalf("Trace(text summary): %v", err)
	}
	for _, needle := range []string{
		"Child-agent telemetry",
		"events=2 started=1 completed=1",
		"active=0/6 max_active=1",
		"outcomes=failed:1",
		"avg_duration=2s",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("summary missing %q: %s", needle, output)
		}
	}
}

func TestTraceSummaryAcceptsDocumentedTopLevelOptions(t *testing.T) {
	cwd := t.TempDir()

	jsonOutput, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"--json"})
	})
	if err != nil {
		t.Fatalf("Trace(--json): %v", err)
	}
	var summary childAgentTraceSummary
	if err := json.Unmarshal([]byte(jsonOutput), &summary); err != nil {
		t.Fatalf("parse summary JSON: %v\n%s", err, jsonOutput)
	}
	if summary.Events != 0 {
		t.Fatalf("unexpected summary for empty trace log: %+v", summary)
	}

	textOutput, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"--since", "1d"})
	})
	if err != nil {
		t.Fatalf("Trace(--since 1d): %v", err)
	}
	if !strings.Contains(textOutput, "No child-agent telemetry found") {
		t.Fatalf("unexpected text summary: %s", textOutput)
	}
}

func TestTraceChildAgentValidation(t *testing.T) {
	cwd := t.TempDir()
	if _, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agent", "queued", "--agent", "a1", "--role", "executor"})
	}); err == nil || !strings.Contains(err.Error(), "--queue-depth") {
		t.Fatalf("expected queued validation error, got %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return Trace(cwd, []string{"child-agent", "complete", "--agent", "a1"})
	}); err == nil || !strings.Contains(err.Error(), "--status") {
		t.Fatalf("expected complete validation error, got %v", err)
	}
}
