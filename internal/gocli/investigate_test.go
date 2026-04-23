package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func TestRunInvestigateSimplePromptInjectsGeneratedTelemetryScopeIntoCodexEnv(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	codexHome := filepath.Join(t.TempDir(), ".codex")
	fakeBin := filepath.Join(t.TempDir(), "bin")

	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("# Codex Home\n"), 0o644); err != nil {
		t.Fatalf("write codex AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"printf 'session-env:%s\\n' \"$NANA_SESSION_ID\"",
		"printf 'turn-env:%s\\n' \"$NANA_TURN_ID\"",
		"cat >/dev/null",
		"printf 'ok\\n'",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	result, err := runInvestigateSimplePrompt(cwd, codexHome, "Inspect this prompt.\n", "mcp-health")
	if err != nil {
		t.Fatalf("runInvestigateSimplePrompt: %v\nstdout=%s\nstderr=%s", err, result.Stdout, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("expected fake codex output, got %q", result.Stdout)
	}

	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	sessionLine := ""
	turnLine := ""
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "session-env:"):
			sessionLine = line
		case strings.HasPrefix(line, "turn-env:"):
			turnLine = line
		}
	}
	if sessionLine == "" || sessionLine == "session-env:" {
		t.Fatalf("expected investigate codex env to include generated run id, got %q", result.Stdout)
	}
	if turnLine == "" || turnLine == "turn-env:" {
		t.Fatalf("expected investigate codex env to include generated turn id, got %q", result.Stdout)
	}
	if !strings.HasPrefix(strings.TrimPrefix(sessionLine, "session-env:"), "investigate-simple-") {
		t.Fatalf("expected generated investigate session prefix, got %q", sessionLine)
	}
	if !strings.HasPrefix(strings.TrimPrefix(turnLine, "turn-env:"), "turn-") {
		t.Fatalf("expected generated investigate turn prefix, got %q", turnLine)
	}
}

func serversNames(servers []investigateMCPServerStatus) []string {
	names := make([]string, 0, len(servers))
	for _, server := range servers {
		names = append(names, server.ServerName)
	}
	return names
}
