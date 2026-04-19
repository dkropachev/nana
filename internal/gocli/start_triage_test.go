package gocli

import (
	"strings"
	"testing"
)

func TestBuildStartWorkTriagePromptCompactsLargeBodyAndOmitsEmptyFields(t *testing.T) {
	prompt := buildStartWorkTriagePrompt("acme/widget", startWorkIssueState{
		SourceNumber: 42,
		Title:        "Trim prompt payloads",
		SourceBody:   strings.Repeat("line\n", 2000),
	})
	if strings.Contains(prompt, "Labels:") || strings.Contains(prompt, "State:") {
		t.Fatalf("expected empty labels/state to be omitted:\n%s", prompt)
	}
	if !strings.Contains(prompt, "... [truncated]") {
		t.Fatalf("expected large body to be truncated:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Respond with JSON only.") {
		t.Fatalf("missing response contract:\n%s", prompt)
	}
}
