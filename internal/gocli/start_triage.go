package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type startWorkTriageResult struct {
	Priority  int
	Rationale string
}

func runStartWorkIssueTriage(repoSlug string, issueKey string, issue startWorkIssueState, codexArgs []string) (startWorkTriageResult, error) {
	repoPath, err := ensureImproveGithubCheckout(repoSlug)
	if err != nil {
		return startWorkTriageResult{}, err
	}
	repoLock, err := acquireSourceReadLock(repoPath, repoAccessLockOwner{
		Backend: "start-triage",
		RunID:   fmt.Sprintf("issue-%d", issue.SourceNumber),
		Purpose: "triage",
		Label:   "start-triage",
	})
	if err != nil {
		return startWorkTriageResult{}, err
	}
	defer func() {
		_ = repoLock.Release()
	}()
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
		RecoverySpec:     startTriageManagedPromptRecoverySpec(repoSlug, issueKey, issue, codexArgs),
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

func recoverStartWorkIssueTriage(args []string) error {
	repoSlug := ""
	issueKey := ""
	passthroughIndex := len(args)
	for index, token := range args {
		if token == "--" {
			passthroughIndex = index
			break
		}
	}
	parseArgs := args[:passthroughIndex]
	codexArgs := []string{}
	if passthroughIndex < len(args) {
		codexArgs = append(codexArgs, args[passthroughIndex+1:]...)
	}
	for index := 0; index < len(parseArgs); index++ {
		token := parseArgs[index]
		switch {
		case token == "--repo-slug":
			value, err := requireFlagValue(parseArgs, index, token)
			if err != nil {
				return fmt.Errorf("missing value after --repo-slug")
			}
			repoSlug = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--repo-slug="):
			repoSlug = strings.TrimSpace(strings.TrimPrefix(token, "--repo-slug="))
		case token == "--issue-key":
			value, err := requireFlagValue(parseArgs, index, token)
			if err != nil {
				return fmt.Errorf("missing value after --issue-key")
			}
			issueKey = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--issue-key="):
			issueKey = strings.TrimSpace(strings.TrimPrefix(token, "--issue-key="))
		default:
			return fmt.Errorf("unknown triage recovery option %q", token)
		}
	}
	if repoSlug == "" || issueKey == "" {
		return fmt.Errorf("triage recovery requires --repo-slug and --issue-key")
	}

	startWorkStateFileMu.Lock()
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		startWorkStateFileMu.Unlock()
		return err
	}
	issue, ok := state.Issues[issueKey]
	if !ok {
		startWorkStateFileMu.Unlock()
		return fmt.Errorf("triage issue %s was not found for %s", issueKey, repoSlug)
	}
	taskKey := startServiceTaskKey(startTaskKindTriage, issueKey)
	task, ok := state.ServiceTasks[taskKey]
	if !ok {
		startWorkStateFileMu.Unlock()
		return fmt.Errorf("triage task %s was not found for %s", taskKey, repoSlug)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	task.Status = startWorkServiceTaskRunning
	task.StartedAt = defaultString(strings.TrimSpace(task.StartedAt), now)
	task.UpdatedAt = now
	task.LastError = ""
	task.ResultSummary = ""
	task.WaitCycle = ""
	task.WaitUntil = ""
	state.ServiceTasks[taskKey] = task
	issue.TriageStatus = startWorkTriageRunning
	issue.TriageError = ""
	issue.TriageUpdatedAt = now
	issue.UpdatedAt = now
	state.Issues[issueKey] = issue
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		startWorkStateFileMu.Unlock()
		return err
	}
	startWorkStateFileMu.Unlock()

	result, runErr := startRunIssueTriage(repoSlug, issueKey, issue, codexArgs)

	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err = readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		return err
	}
	return applyStartWorkTriageResult(state, repoSlug, taskKey, issueKey, &result, runErr, time.Now().UTC().Format(time.RFC3339))
}

func applyStartWorkTriageResult(state *startWorkState, repoSlug string, taskKey string, issueKey string, result *startWorkTriageResult, runErr error, now string) error {
	if state == nil {
		return runErr
	}
	serviceTask, ok := state.ServiceTasks[taskKey]
	if !ok {
		return fmt.Errorf("triage task %s was not found for %s", taskKey, repoSlug)
	}
	issue, ok := state.Issues[issueKey]
	if !ok {
		return fmt.Errorf("triage issue %s was not found for %s", issueKey, repoSlug)
	}
	if runErr != nil {
		if serviceTask.Attempts < serviceTaskRetryLimit(serviceTask.Kind) {
			serviceTask.Status = startWorkServiceTaskQueued
			serviceTask.LastError = runErr.Error()
			serviceTask.ResultSummary = "retrying"
			serviceTask.WaitCycle = ""
			serviceTask.WaitUntil = ""
			serviceTask.StartedAt = ""
			serviceTask.UpdatedAt = now
			state.ServiceTasks[taskKey] = serviceTask
			issue.TriageStatus = startWorkTriageQueued
			issue.TriageError = runErr.Error()
			issue.TriageUpdatedAt = now
			issue.UpdatedAt = now
			state.Issues[issueKey] = issue
			return writeStartWorkStateUnlocked(*state)
		}
		serviceTask.Status = startWorkServiceTaskFailed
		serviceTask.LastError = runErr.Error()
		serviceTask.WaitCycle = ""
		serviceTask.WaitUntil = ""
		serviceTask.CompletedAt = now
		serviceTask.UpdatedAt = now
		state.ServiceTasks[taskKey] = serviceTask
		issue.TriageStatus = startWorkTriageFailed
		issue.TriageError = runErr.Error()
		issue.TriageUpdatedAt = now
		issue.UpdatedAt = now
		state.Issues[issueKey] = issue
		if err := writeStartWorkStateUnlocked(*state); err != nil {
			return err
		}
		return fmt.Errorf("%s %s: %w", repoSlug, taskKey, runErr)
	}
	if result == nil {
		return fmt.Errorf("%s %s: triage result was missing", repoSlug, taskKey)
	}
	serviceTask.Status = startWorkServiceTaskCompleted
	serviceTask.ResultSummary = startWorkPriorityLabel(result.Priority)
	serviceTask.WaitCycle = ""
	serviceTask.WaitUntil = ""
	serviceTask.CompletedAt = now
	serviceTask.UpdatedAt = now
	serviceTask.Fingerprint = issue.SourceFingerprint
	state.ServiceTasks[taskKey] = serviceTask
	issue.Priority = result.Priority
	issue.PrioritySource = "triage"
	issue.TriageStatus = startWorkTriageCompleted
	issue.TriageRationale = result.Rationale
	issue.TriageFingerprint = issue.SourceFingerprint
	issue.TriageUpdatedAt = now
	issue.TriageError = ""
	issue.UpdatedAt = now
	state.Issues[issueKey] = issue
	return writeStartWorkStateUnlocked(*state)
}

func startTriageIssueNumberFromRecoveryArgs(args []string) int {
	for index := 0; index < len(args)-1; index++ {
		if args[index] == "--issue-number" {
			if parsed, err := strconv.Atoi(strings.TrimSpace(args[index+1])); err == nil {
				return parsed
			}
		}
	}
	return 0
}
