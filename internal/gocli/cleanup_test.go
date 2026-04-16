package gocli

import (
	"strings"
	"testing"
)

var cleanupFixture = []ProcessEntry{
	{PID: 700, PPID: 500, Command: "codex"},
	{PID: 701, PPID: 700, Command: "node /repo/bin/nana.js cleanup --dry-run"},
	{PID: 710, PPID: 700, Command: "node /repo/nana/dist/mcp/state-server.js"},
	{PID: 800, PPID: 1, Command: "node /tmp/nana/dist/mcp/memory-server.js"},
	{PID: 810, PPID: 42, Command: "node /tmp/worktree/dist/mcp/trace-server.js"},
	{PID: 811, PPID: 810, Command: "node /tmp/worktree/dist/mcp/team-server.js"},
	{PID: 900, PPID: 1, Command: "node /tmp/not-nana/other-server.js"},
}

func TestFindCleanupCandidates(t *testing.T) {
	candidates, err := findCleanupCandidates(cleanupFixture, 701)
	if err != nil {
		t.Fatalf("findCleanupCandidates(): %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(candidates))
	}
	if candidates[0].PID != 800 || candidates[0].Reason != "ppid=1" {
		t.Fatalf("unexpected first candidate: %+v", candidates[0])
	}
	if candidates[1].PID != 810 || candidates[1].Reason != "outside-current-session" {
		t.Fatalf("unexpected second candidate: %+v", candidates[1])
	}
	if candidates[2].PID != 811 || candidates[2].Reason != "outside-current-session" {
		t.Fatalf("unexpected third candidate: %+v", candidates[2])
	}
}

func TestFormatCleanupCandidate(t *testing.T) {
	line := formatCleanupCandidate(CleanupCandidate{
		ProcessEntry: ProcessEntry{PID: 800, PPID: 1, Command: "node /tmp/nana/dist/mcp/memory-server.js"},
		Reason:       "ppid=1",
	})
	if !strings.Contains(line, "PID 800 (PPID 1, ppid=1)") {
		t.Fatalf("unexpected format: %q", line)
	}
}
