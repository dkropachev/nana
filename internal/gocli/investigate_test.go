package gocli

import (
	"os"
	"path/filepath"
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

func TestValidateInvestigateReportRejectsDocumentationPrimaryProof(t *testing.T) {
	report := investigateReport{
		OverallStatus:              investigateStatusConfirmed,
		OverallShortExplanation:    "summary",
		OverallDetailedExplanation: "details",
		OverallProofs: []investigateProof{{
			Kind:        "documentation",
			Title:       "doc",
			Link:        "https://example.com",
			WhyItProves: "doc",
			IsPrimary:   true,
		}},
	}
	violations := validateInvestigateReport(report, t.TempDir())
	found := false
	for _, violation := range violations {
		if violation.Code == "documentation_is_not_primary" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected documentation primary violation, got %#v", violations)
	}
}

func TestCheckInvestigateMCPStatusUsesCachedSummary(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	statusPath := filepath.Join(home, ".nana", "codex-home-investigate", "investigate-mcp-status.json")
	status := investigateMCPStatus{
		Version:           1,
		CheckedAt:         "2026-04-10T00:00:00Z",
		CodexHome:         filepath.Join(home, ".nana", "codex-home-investigate"),
		ConfigPath:        filepath.Join(home, ".nana", "codex-home-investigate", "config.toml"),
		ConfiguredServers: []string{"ci-mcp"},
		Servers:           []investigateMCPServerStatus{{ServerName: "ci-mcp", OK: true, Summary: "reachable"}},
		AllOK:             true,
		ProbeSummary:      "healthy",
	}
	if err := writeInvestigateMCPStatus(statusPath, status); err != nil {
		t.Fatalf("write status file: %v", err)
	}
	check := checkInvestigateMCPStatus(cwd)
	if check.Status != "pass" {
		t.Fatalf("expected pass, got %#v", check)
	}
	if !strings.Contains(check.Message, "healthy") && !strings.Contains(check.Message, "configured MCP") {
		t.Fatalf("unexpected cached MCP status message: %q", check.Message)
	}
}
