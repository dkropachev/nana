package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	prompt := buildStartWorkTriagePrompt(repoSlug, issue)
	normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(codexArgs)
	prompt = prefixCodexFastPrompt(prompt, fastMode)
	transport := promptTransportForSize(prompt, structuredPromptStdinThreshold)
	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       repoPath,
		InstructionsRoot: repoPath,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", repoPath},
		CommonArgs:       normalizedCodexArgs,
		Prompt:           prompt,
		PromptTransport:  transport,
		CheckpointPath:   filepath.Join(githubManagedPaths(repoSlug).RepoRoot, ".nana", "start", "triage-checkpoints", fmt.Sprintf("issue-%d.json", issue.SourceNumber)),
		StepKey:          fmt.Sprintf("triage-issue-%d", issue.SourceNumber),
		ResumeStrategy:   codexResumeSamePrompt,
		Env:              append(buildGithubCodexEnv(NotifyTempContract{}, scopedCodexHome, strings.TrimSpace(os.Getenv("GITHUB_API_URL"))), "NANA_PROJECT_AGENTS_ROOT="+repoPath),
		RateLimitPolicy:  codexRateLimitPolicyReturnPause,
	})
	if err != nil {
		return startWorkTriageResult{}, fmt.Errorf("%w\n%s", err, strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr}, "\n")))
	}
	return parseStartWorkTriageResult([]byte(result.Stdout))
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
		fmt.Sprintf("Repo: %s", repoSlug),
		fmt.Sprintf("Issue: #%d", issue.SourceNumber),
	}
	if title := compactPromptValue(issue.Title, 0, 200); title != "" {
		lines = append(lines, fmt.Sprintf("Title: %s", title))
	}
	if state := strings.TrimSpace(issue.State); state != "" {
		lines = append(lines, fmt.Sprintf("State: %s", state))
	}
	if len(issue.Labels) > 0 {
		lines = append(lines, fmt.Sprintf("Labels: %s", compactPromptValue(strings.Join(issue.Labels, ", "), 0, 400)))
	}
	if body := compactPromptValue(issue.SourceBody, 80, 6000); body != "" {
		lines = append(lines, "Body:", body)
	}
	lines = append(lines, "Respond with JSON only.")
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
