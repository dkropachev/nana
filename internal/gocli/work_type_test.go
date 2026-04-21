package gocli

import "testing"

func TestInferIssueWorkTypeFromLabels(t *testing.T) {
	resolution := inferIssueWorkType([]string{"bug", "P1"}, "Fix flaky widget")
	if resolution.WorkType != workTypeBugFix {
		t.Fatalf("expected bug_fix inference, got %+v", resolution)
	}
}

func TestInferScoutWorkTypeFallsBackByRole(t *testing.T) {
	resolution := inferScoutWorkType(enhancementScoutRole, scoutFinding{
		Title:   "Add command palette",
		Summary: "Introduce a command palette for the start UI",
	})
	if resolution.WorkType != workTypeFeature {
		t.Fatalf("expected enhancement scout fallback to feature, got %+v", resolution)
	}
}

func TestStartWorkIssueReadyForImplementationRequiresWorkType(t *testing.T) {
	issue := startWorkIssueState{
		Status:         startWorkStatusQueued,
		State:          "open",
		ForkNumber:     7,
		Labels:         []string{"nana", "P1"},
		PrioritySource: "manual_label",
	}
	options := startWorkOptions{ImplementMode: "auto"}
	if startWorkIssueReadyForImplementation(issue, options) {
		t.Fatalf("expected untyped issue to stay out of the implementation queue")
	}
	issue.WorkType = workTypeBugFix
	if !startWorkIssueReadyForImplementation(issue, options) {
		t.Fatalf("expected typed issue to be implementation-ready")
	}
}
