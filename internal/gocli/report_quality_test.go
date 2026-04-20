package gocli

import "testing"

func TestLintFinalReportQualityAcceptsEvidenceCompleteReport(t *testing.T) {
	content := `
Changed files:
- internal/gocli/work_local.go

Verification evidence:
- go test ./internal/gocli passed

Simplifications made:
- collapsed duplicate report checks into one linter

Remaining risks:
- none

routing_decision:
- mode: solo
- role_tier: standard executor
- trigger: direct implementation
- confidence: high
`
	issues := lintFinalReportQuality(content, finalReportQualityLintOptions{RequireRoutingDecision: true})
	if len(issues) != 0 {
		t.Fatalf("expected complete report to pass lint, got %#v", issues)
	}
}

func TestLintFinalReportQualityReportsMissingSections(t *testing.T) {
	content := `
Changed files:
- README.md

Verification: not run; documentation-only change

Remaining risks:
- none
`
	issues := lintFinalReportQuality(content, finalReportQualityLintOptions{RequireRoutingDecision: true})
	got := map[string]bool{}
	for _, issue := range issues {
		got[issue.Field] = true
	}
	for _, field := range []string{"simplifications", "routing_decision"} {
		if !got[field] {
			t.Fatalf("expected missing %s issue, got %#v", field, issues)
		}
	}
}

func TestLintFinalReportQualityRequiresRoutingDecisionFields(t *testing.T) {
	content := `
Changed files:
- README.md

Verification evidence:
- go test ./... passed

Simplifications made:
- none

Remaining risks:
- none

routing_decision:
- mode: solo
`
	issues := lintFinalReportQuality(content, finalReportQualityLintOptions{RequireRoutingDecision: true})
	got := map[string]bool{}
	for _, issue := range issues {
		got[issue.Field] = true
	}
	for _, field := range []string{"routing_decision.role_tier", "routing_decision.trigger", "routing_decision.confidence"} {
		if !got[field] {
			t.Fatalf("expected missing %s issue, got %#v", field, issues)
		}
	}
}
