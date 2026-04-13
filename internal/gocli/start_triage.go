package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type startWorkTriageResult struct {
	Priority  int
	Rationale string
}

func runStartWorkIssueTriage(repoSlug string, issue startWorkIssueState, codexArgs []string) (startWorkTriageResult, error) {
	repoPath, err := ensureImproveGithubCheckout(repoSlug)
	if err != nil {
		return startWorkTriageResult{}, err
	}
	scopedCodexHome, err := ensureScopedCodexHome(
		ResolveCodexHomeForLaunch(repoPath),
		filepath.Join(githubManagedPaths(repoSlug).RepoRoot, ".nana", "start", "codex-home", "triage"),
	)
	if err != nil {
		return startWorkTriageResult{}, err
	}
	sessionID := fmt.Sprintf("triage-%d", time.Now().UnixNano())
	sessionInstructionsPath, err := writeSessionModelInstructions(repoPath, sessionID, scopedCodexHome)
	if err != nil {
		return startWorkTriageResult{}, err
	}
	defer removeSessionInstructionsFile(repoPath, sessionID)
	prompt := buildStartWorkTriagePrompt(repoSlug, issue)
	normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(codexArgs)
	prompt = prefixCodexFastPrompt(prompt, fastMode)
	args := append([]string{"exec", "-C", repoPath}, normalizedCodexArgs...)
	args = append(args, prompt)
	args = injectModelInstructionsArgs(args, sessionInstructionsPath)
	cmd := exec.Command("codex", args...)
	cmd.Dir = repoPath
	cmd.Env = append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+repoPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return startWorkTriageResult{}, fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(output)))
	}
	return parseStartWorkTriageResult(output)
}

func buildStartWorkTriagePrompt(repoSlug string, issue startWorkIssueState) string {
	lines := []string{
		"You are triaging a GitHub issue for Nana's start queue.",
		"Return JSON only with this schema: {\"priority\":\"P1\"|\"P2\"|\"P3\"|\"P4\"|\"P5\",\"rationale\":\"...\"}",
		"Rules:",
		"- Never emit P0.",
		"- Use only P1 through P5.",
		"- Base the answer on urgency, severity, likely user impact, and implementation urgency.",
		"- Keep rationale under 160 characters.",
		"",
		fmt.Sprintf("Repo: %s", repoSlug),
		fmt.Sprintf("Issue: #%d", issue.SourceNumber),
		fmt.Sprintf("Title: %s", issue.Title),
		fmt.Sprintf("State: %s", issue.State),
		fmt.Sprintf("Labels: %s", strings.Join(issue.Labels, ", ")),
		"",
		"Body:",
		strings.TrimSpace(issue.SourceBody),
		"",
		"Respond with JSON only.",
	}
	return strings.Join(lines, "\n")
}

func parseStartWorkTriageResult(content []byte) (startWorkTriageResult, error) {
	trimmed := bytes.TrimSpace(extractImprovementJSONObject(content))
	if len(trimmed) == 0 {
		trimmed = bytes.TrimSpace(content)
	}
	var payload struct {
		Priority  string `json:"priority"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return startWorkTriageResult{}, fmt.Errorf("triage output did not match the JSON schema")
	}
	priority, err := parseStartWorkTriagePriority(payload.Priority)
	if err != nil {
		return startWorkTriageResult{}, err
	}
	return startWorkTriageResult{
		Priority:  priority,
		Rationale: strings.TrimSpace(payload.Rationale),
	}, nil
}

func parseStartWorkTriagePriority(value string) (int, error) {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if len(normalized) == 2 && normalized[0] == 'P' && normalized[1] >= '1' && normalized[1] <= '5' {
		return int(normalized[1] - '0'), nil
	}
	return 0, fmt.Errorf("triage output contained invalid priority %q", value)
}
