package gocli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCompactInvestigateValidatorReportJSONCapsIssuesAndProofs(t *testing.T) {
	report := investigateReport{
		OverallStatus:              investigateStatusPartiallyConfirmed,
		OverallShortExplanation:    strings.Repeat("short ", 80),
		OverallDetailedExplanation: strings.Repeat("detail ", 300),
		OverallProofs:              []investigateProof{},
		Issues:                     []investigateIssue{},
	}
	for index := 0; index < 8; index++ {
		report.OverallProofs = append(report.OverallProofs, investigateProof{
			Kind:        "source_code",
			Title:       fmt.Sprintf("overall proof %d", index),
			Link:        "https://example.invalid/overall",
			WhyItProves: strings.Repeat("why ", 80),
		})
	}
	for issueIndex := 0; issueIndex < 12; issueIndex++ {
		issue := investigateIssue{
			ID:                  fmt.Sprintf("ISSUE-%d", issueIndex),
			ShortExplanation:    strings.Repeat("short ", 60),
			DetailedExplanation: strings.Repeat("detail ", 180),
			Proofs:              []investigateProof{},
		}
		for proofIndex := 0; proofIndex < 4; proofIndex++ {
			issue.Proofs = append(issue.Proofs, investigateProof{
				Kind:        "github",
				Title:       fmt.Sprintf("issue proof %d-%d", issueIndex, proofIndex),
				Link:        "https://example.invalid/issue",
				WhyItProves: strings.Repeat("because ", 60),
			})
		}
		report.Issues = append(report.Issues, issue)
	}

	payload := compactInvestigateValidatorReportJSON(report)
	if len(payload) > investigateValidatorPayloadCharLimit {
		t.Fatalf("expected validator payload <= %d bytes, got %d", investigateValidatorPayloadCharLimit, len(payload))
	}
	var compacted investigateReport
	if err := json.Unmarshal([]byte(payload), &compacted); err != nil {
		t.Fatalf("validator payload should stay valid JSON: %v\n%s", err, payload)
	}
	if len(compacted.Issues) > investigateMaxValidatorIssues {
		t.Fatalf("expected at most %d issues, got %d", investigateMaxValidatorIssues, len(compacted.Issues))
	}
	totalProofs := len(compacted.OverallProofs)
	for _, issue := range compacted.Issues {
		totalProofs += len(issue.Proofs)
	}
	if totalProofs > investigateMaxValidatorProofs {
		t.Fatalf("expected at most %d proofs, got %d", investigateMaxValidatorProofs, totalProofs)
	}
}

func TestBuildInvestigatePromptCapsServerAndViolationLists(t *testing.T) {
	servers := make([]investigateMCPServerStatus, 0, 25)
	for index := 0; index < 25; index++ {
		servers = append(servers, investigateMCPServerStatus{
			ServerName: fmt.Sprintf("server-%02d", index),
			OK:         true,
			Summary:    strings.Repeat("summary ", 80),
		})
	}
	violations := make([]investigateViolation, 0, 12)
	for index := 0; index < 12; index++ {
		violations = append(violations, investigateViolation{
			Code:    fmt.Sprintf("V-%02d", index),
			Path:    fmt.Sprintf("path/%02d", index),
			Message: strings.Repeat("message ", 50),
		})
	}
	prompt, err := buildInvestigatePrompt(investigateManifest{
		RunID:         "inv-1",
		MaxRounds:     3,
		WorkspaceRoot: "/tmp/repo",
		Query:         "why is this broken?",
	}, investigateMCPStatus{
		ConfiguredServers: serversNames(servers),
		Servers:           servers,
		ProbeSummary:      strings.Repeat("probe ", 100),
	}, 2, violations)
	if err != nil {
		t.Fatalf("buildInvestigatePrompt: %v", err)
	}
	for _, needle := range []string{"... 5 additional MCP servers omitted", "... 2 additional violations omitted"} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected prompt to contain %q:\n%s", needle, prompt)
		}
	}
	if len(prompt) > investigatePromptCharLimit {
		t.Fatalf("expected investigate prompt <= %d bytes, got %d", investigatePromptCharLimit, len(prompt))
	}
}

func serversNames(servers []investigateMCPServerStatus) []string {
	names := make([]string, 0, len(servers))
	for _, server := range servers {
		names = append(names, server.ServerName)
	}
	return names
}
