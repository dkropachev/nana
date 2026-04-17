package gocli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestInvestigateOnboardCreatesDedicatedConfigAndNoProfile(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	output, err := captureStdout(t, func() error { return Investigate(cwd, []string{"onboard"}) })
	if err != nil {
		t.Fatalf("Investigate(onboard): %v", err)
	}

	configPath := filepath.Join(home, ".nana", "codex-home-investigate", "config.toml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected investigate config at %s: %v", configPath, err)
	}
	statusPath := filepath.Join(home, ".nana", "codex-home-investigate", "investigate-mcp-status.json")
	if _, err := os.Stat(statusPath); err != nil {
		t.Fatalf("expected investigate status at %s: %v", statusPath, err)
	}
	profilePath := filepath.Join(home, ".nana", "codex-home-investigate", "investigate-sources.json")
	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Fatalf("did not expect source profile at %s: err=%v", profilePath, err)
	}
	if !strings.Contains(output, "Investigate config: "+configPath) || !strings.Contains(output, "No MCP servers are assumed or predeclared.") {
		t.Fatalf("unexpected onboard output: %q", output)
	}
}

func TestInvestigateDoctorReportsNoConfiguredMCPs(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nif [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"list\" ]; then\n  printf 'Name Status\\n'\n  exit 0\nfi\nprintf 'unexpected codex args: %s\\n' \"$*\" >&2\nexit 1\n")

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	if _, err := captureStdout(t, func() error { return Investigate(cwd, []string{"onboard"}) }); err != nil {
		t.Fatalf("Investigate(onboard): %v", err)
	}
	output, err := captureStdout(t, func() error { return Investigate(cwd, []string{"doctor"}) })
	if err != nil {
		t.Fatalf("Investigate(doctor): %v", err)
	}
	if !strings.Contains(output, "No MCP servers configured in the investigate config.") {
		t.Fatalf("unexpected doctor output: %q", output)
	}
}

func TestInvestigateDoctorAsksCodexToProbeConfiguredMCPs(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"list\" ]; then",
		"  printf 'Name Status\\nci-mcp cmd arg enabled auth\\nlogs-mcp cmd arg enabled auth\\n'",
		"  exit 0",
		"fi",
		"if [ \"$1\" = \"exec\" ]; then",
		"  prompt=$(cat)",
		"  case \"$prompt\" in",
		"    *\"Configured MCP server names:\"*) printf '{\"all_ok\":true,\"probe_summary\":\"all MCPs healthy\",\"servers\":[{\"server_name\":\"ci-mcp\",\"ok\":true,\"summary\":\"reachable\"},{\"server_name\":\"logs-mcp\",\"ok\":true,\"summary\":\"reachable\"}]}'; exit 0 ;;",
		"  esac",
		"fi",
		"printf 'unexpected codex args: %s\\n' \"$*\" >&2",
		"exit 1",
		"",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	if _, err := captureStdout(t, func() error { return Investigate(cwd, []string{"onboard"}) }); err != nil {
		t.Fatalf("Investigate(onboard): %v", err)
	}
	output, err := captureStdout(t, func() error { return Investigate(cwd, []string{"doctor"}) })
	if err != nil {
		t.Fatalf("Investigate(doctor): %v", err)
	}
	if !strings.Contains(output, "ci-mcp: reachable") || !strings.Contains(output, "logs-mcp: reachable") || !strings.Contains(output, "All configured investigate MCPs are working.") {
		t.Fatalf("unexpected doctor output: %q", output)
	}
}

func TestInvestigateRunBlocksWhenConfiguredMCPProbeFails(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"list\" ]; then",
		"  printf 'Name Status\\nci-mcp cmd arg enabled auth\\n'",
		"  exit 0",
		"fi",
		"if [ \"$1\" = \"exec\" ]; then",
		"  prompt=$(cat)",
		"  case \"$prompt\" in",
		"    *\"Configured MCP server names:\"*) printf '{\"all_ok\":false,\"probe_summary\":\"one MCP failed\",\"servers\":[{\"server_name\":\"ci-mcp\",\"ok\":false,\"summary\":\"timeout\"}]}'; exit 0 ;;",
		"  esac",
		"fi",
		"printf 'unexpected codex args: %s\\n' \"$*\" >&2",
		"exit 1",
		"",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	if _, err := captureStdout(t, func() error { return Investigate(cwd, []string{"onboard"}) }); err != nil {
		t.Fatalf("Investigate(onboard): %v", err)
	}
	_, err := captureStdout(t, func() error { return Investigate(cwd, []string{"why is CI failing?"}) })
	if err == nil || !strings.Contains(err.Error(), "configured investigate MCPs are not working") {
		t.Fatalf("expected MCP readiness failure, got %v", err)
	}
}

func TestInvestigateRunAllowsLocalSourceOnlyWhenNoMCPConfigured(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	repoFile := filepath.Join(cwd, "main.go")
	if err := os.WriteFile(repoFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	reportLink := repoFile + "#L1"
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"list\" ]; then",
		"  printf 'Name Status\\n'",
		"  exit 0",
		"fi",
		"if [ \"$1\" = \"exec\" ]; then",
		"  prompt=$(cat)",
		"  case \"$prompt\" in",
		"    *\"# NANA Investigation Validator\"*) printf '{\"accepted\":true,\"summary\":\"validated\",\"violations\":[]}'; exit 0 ;;",
		"    *\"Previous validator violations to fix:\"*) printf '{\"overall_status\":\"CONFIRMED\",\"overall_short_explanation\":\"fixed\",\"overall_detailed_explanation\":\"fixed with source proof\",\"overall_proofs\":[{\"kind\":\"source_code\",\"title\":\"main\",\"link\":\"" + reportLink + "\",\"why_it_proves\":\"source exists\",\"is_primary\":true,\"path\":\"" + repoFile + "\",\"line\":1}],\"issues\":[{\"id\":\"issue-1\",\"short_explanation\":\"found issue\",\"detailed_explanation\":\"details\",\"proofs\":[{\"kind\":\"source_code\",\"title\":\"main\",\"link\":\"" + reportLink + "\",\"why_it_proves\":\"source exists\",\"is_primary\":true,\"path\":\"" + repoFile + "\",\"line\":1}]}]}'; exit 0 ;;",
		"    *\"# NANA Investigate\"*) printf '{\"overall_status\":\"CONFIRMED\",\"overall_short_explanation\":\"first pass\",\"overall_detailed_explanation\":\"missing proof on purpose\",\"overall_proofs\":[{\"kind\":\"documentation\",\"title\":\"doc\",\"link\":\"https://example.com/doc\",\"why_it_proves\":\"doc only\",\"is_primary\":true}],\"issues\":[]}'; exit 0 ;;",
		"  esac",
		"fi",
		"printf 'unexpected codex args: %s\\n' \"$*\" >&2",
		"exit 1",
		"",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	if _, err := captureStdout(t, func() error { return Investigate(cwd, []string{"onboard"}) }); err != nil {
		t.Fatalf("Investigate(onboard): %v", err)
	}
	output, err := captureStdout(t, func() error { return Investigate(cwd, []string{"why is the build failing?"}) })
	if err != nil {
		t.Fatalf("Investigate(run): %v", err)
	}
	if !strings.Contains(output, "[investigate] Status: CONFIRMED") {
		t.Fatalf("unexpected run output: %q", output)
	}
}

func TestInvestigateResumeReusesFailedSession(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	commandLogPath := filepath.Join(cwd, "codex-commands.log")
	failOncePath := filepath.Join(cwd, "investigate-fail-once.marker")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	repoFile := filepath.Join(cwd, "main.go")
	if err := os.WriteFile(repoFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	reportLink := repoFile + "#L1"
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`printf '%s\n' "$*" >> "${FAKE_CODEX_LOG_PATH}"`,
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"list\" ]; then",
		"  printf 'Name Status\\n'",
		"  exit 0",
		"fi",
		`mkdir -p "$CODEX_HOME/sessions/2026/04/17"`,
		`printf '{"type":"session_meta","payload":{"id":"session-investigate","timestamp":"2099-01-01T00:00:00Z","cwd":"%s"}}\n' "$PWD" > "$CODEX_HOME/sessions/2026/04/17/rollout-session-investigate.jsonl"`,
		"if [ \"$1\" = \"exec\" ]; then",
		"  prompt=$(cat)",
		"  case \"$*\" in",
		"    *\"exec resume session-investigate\"*) printf '{\"overall_status\":\"CONFIRMED\",\"overall_short_explanation\":\"resumed\",\"overall_detailed_explanation\":\"resumed with source proof\",\"overall_proofs\":[{\"kind\":\"source_code\",\"title\":\"main\",\"link\":\"" + reportLink + "\",\"why_it_proves\":\"source exists\",\"is_primary\":true,\"path\":\"" + repoFile + "\",\"line\":1}],\"issues\":[]}'; exit 0 ;;",
		"  esac",
		"  case \"$prompt\" in",
		"    *\"# NANA Investigation Validator\"*) printf '{\"accepted\":true,\"summary\":\"validated\",\"violations\":[]}'; exit 0 ;;",
		"    *\"# NANA Investigate\"*)",
		"      if [ ! -f \"${FAKE_INVESTIGATE_FAIL_ONCE_PATH}\" ]; then",
		"        : > \"${FAKE_INVESTIGATE_FAIL_ONCE_PATH}\"",
		"        printf 'rate limited\\n' >&2",
		"        exit 1",
		"      fi",
		"      printf '{\"overall_status\":\"CONFIRMED\",\"overall_short_explanation\":\"fresh\",\"overall_detailed_explanation\":\"fresh source proof\",\"overall_proofs\":[{\"kind\":\"source_code\",\"title\":\"main\",\"link\":\"" + reportLink + "\",\"why_it_proves\":\"source exists\",\"is_primary\":true,\"path\":\"" + repoFile + "\",\"line\":1}],\"issues\":[]}'",
		"      exit 0",
		"      ;;",
		"  esac",
		"fi",
		"printf 'unexpected codex args: %s\\n' \"$*\" >&2",
		"exit 1",
		"",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG_PATH", commandLogPath)
	t.Setenv("FAKE_INVESTIGATE_FAIL_ONCE_PATH", failOncePath)
	if _, err := captureStdout(t, func() error { return Investigate(cwd, []string{"onboard"}) }); err != nil {
		t.Fatalf("Investigate(onboard): %v", err)
	}
	_, err := captureStdout(t, func() error { return Investigate(cwd, []string{"why did this fail?"}) })
	if err == nil {
		t.Fatal("expected first investigate run to fail")
	}
	output, err := captureStdout(t, func() error { return Investigate(cwd, []string{"--last"}) })
	if err != nil {
		t.Fatalf("Investigate(resume): %v\n%s", err, output)
	}
	if !strings.Contains(output, "[investigate] Status: CONFIRMED") {
		t.Fatalf("unexpected resume output: %q", output)
	}
	commandLog, err := os.ReadFile(commandLogPath)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	if !strings.Contains(string(commandLog), "exec resume session-investigate") {
		t.Fatalf("expected exec resume in log, got %q", string(commandLog))
	}
}

func TestValidateInvestigateReportRejectsDocumentationPrimaryProof(t *testing.T) {
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
