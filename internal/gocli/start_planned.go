package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type startPlannedLaunchResult struct {
	Status      string `json:"status,omitempty"`
	Result      string `json:"result,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	IssueNumber int    `json:"issue_number,omitempty"`
	IssueURL    string `json:"issue_url,omitempty"`
}

var startLaunchPlannedItem = launchStartPlannedItem
var startLaunchScheduledPlannedItem = launchStartPlannedItemScheduled
var startRunScheduledPlannedLocalWork = func(cwd string, args []string) error {
	_, err := runLocalWorkCommandWithOptions(cwd, args, codexRateLimitPolicyReturnPause)
	return err
}

var startRunScheduledPlannedGithubWork = func(cwd string, args []string) (githubCommandResult, error) {
	if len(args) < 2 || args[0] != "start" {
		return GithubWorkCommand(cwd, args)
	}
	target, err := parseGithubTargetURL(args[1])
	if err != nil {
		return githubCommandResult{}, err
	}
	codexArgs := []string{}
	for index := 2; index < len(args); index++ {
		if args[index] == "--" {
			codexArgs = append(codexArgs, args[index+1:]...)
			break
		}
	}
	run, err := startGithubWork(githubWorkStartOptions{
		Target:           target,
		CreatePR:         true,
		CreatePRExplicit: true,
		CodexArgs:        codexArgs,
		RateLimitPolicy:  codexRateLimitPolicyReturnPause,
	})
	return githubCommandResult{Handled: true, RunID: run.RunID}, err
}

var startRunScheduledPlannedInvestigation = func(repoSlug string, query string, runID string) (startUIBackgroundLaunch, error) {
	workspaceRoot, err := ensureStartUIRepoInvestigationWorkspace(repoSlug)
	if err != nil {
		return startUIBackgroundLaunch{}, err
	}
	return startUISpawnInvestigateQueryWithRunID(workspaceRoot, query, runID)
}

var startRunScheduledPlannedScout = func(cwd string, options ImproveOptions, role string, policy scoutPolicy) error {
	return runScoutWithPolicyOverride(cwd, options, role, policy)
}

func launchStartPlannedItem(cwd string, repoSlug string, workOptions startWorkOptions, item startWorkPlannedItem) (startPlannedLaunchResult, error) {
	launchKind := resolveStartPlannedLaunchKind(item, workOptions)
	if normalizeGithubRepoMode(workOptions.RepoMode) == "disabled" && launchKind != "investigation" && launchKind != "manual_scout" {
		return startPlannedLaunchResult{}, fmt.Errorf("repo %s is configured with repo-mode disabled; change it before launching planned items", repoSlug)
	}
	switch launchKind {
	case "local_work":
		return launchStartPlannedLocalWork(repoSlug, item, workOptions.CodexArgs)
	case "github_issue":
		return launchStartPlannedGithubIssue(repoSlug, item)
	case "tracked_issue":
		return launchStartPlannedTrackedIssue(repoSlug, item)
	case "investigation":
		return launchStartPlannedInvestigation(repoSlug, item)
	case "manual_scout":
		return launchStartPlannedManualScout(cwd, repoSlug, item, workOptions.CodexArgs)
	default:
		return startPlannedLaunchResult{}, fmt.Errorf("unsupported planned item launch kind %q", item.LaunchKind)
	}
}

func launchStartPlannedItemScheduled(cwd string, repoSlug string, workOptions startWorkOptions, item startWorkPlannedItem) (startPlannedLaunchResult, error) {
	launchKind := resolveStartPlannedLaunchKind(item, workOptions)
	if normalizeGithubRepoMode(workOptions.RepoMode) == "disabled" && launchKind != "investigation" && launchKind != "manual_scout" {
		return startPlannedLaunchResult{}, fmt.Errorf("repo %s is configured with repo-mode disabled; change it before launching planned items", repoSlug)
	}
	switch launchKind {
	case "local_work":
		return launchStartPlannedLocalWorkScheduled(repoSlug, item, workOptions.CodexArgs)
	case "github_issue":
		return launchStartPlannedGithubIssue(repoSlug, item)
	case "tracked_issue":
		return launchStartPlannedTrackedIssueScheduled(repoSlug, item, workOptions.CodexArgs)
	case "investigation":
		return launchStartPlannedInvestigation(repoSlug, item)
	case "manual_scout":
		return launchStartPlannedManualScout(cwd, repoSlug, item, workOptions.CodexArgs)
	default:
		return startPlannedLaunchResult{}, fmt.Errorf("unsupported planned item launch kind %q", item.LaunchKind)
	}
}

func resolveStartPlannedLaunchKind(item startWorkPlannedItem, workOptions startWorkOptions) string {
	switch strings.TrimSpace(item.LaunchKind) {
	case "local_work", "github_issue", "tracked_issue", "investigation", "manual_scout":
		return strings.TrimSpace(item.LaunchKind)
	}
	if strings.TrimSpace(workOptions.RepoMode) == "local" {
		return "local_work"
	}
	return "github_issue"
}

func resolveStartPlannedRepoPath(repoSlug string) (string, error) {
	repoPath := githubManagedPaths(repoSlug).SourcePath
	if info, err := os.Stat(repoPath); err == nil && info.IsDir() {
		return repoPath, nil
	}
	return ensureImproveGithubCheckout(repoSlug)
}

func startPlannedLocalWorkTask(item startWorkPlannedItem) string {
	task := strings.TrimSpace(item.Title)
	if strings.TrimSpace(item.Description) != "" {
		task += "\n\n" + strings.TrimSpace(item.Description)
	}
	return task
}

func startPlannedGeneratedRunID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func launchStartPlannedInvestigation(repoSlug string, item startWorkPlannedItem) (startPlannedLaunchResult, error) {
	query := strings.TrimSpace(item.InvestigationQuery)
	if query == "" {
		return startPlannedLaunchResult{}, fmt.Errorf("planned item %s is missing investigation_query", item.ID)
	}
	runID := startPlannedGeneratedRunID("investigate")
	launch, err := startRunScheduledPlannedInvestigation(repoSlug, query, runID)
	if err != nil {
		return startPlannedLaunchResult{}, err
	}
	return startPlannedLaunchResult{
		Status: defaultString(strings.TrimSpace(launch.Status), "spawned"),
		Result: defaultString(strings.TrimSpace(launch.Result), "investigation started"),
		RunID:  runID,
	}, nil
}

func launchStartPlannedManualScout(cwd string, repoSlug string, item startWorkPlannedItem, codexArgs []string) (startPlannedLaunchResult, error) {
	role := strings.TrimSpace(item.ScoutRole)
	if !scoutRoleListIncludes(supportedScoutRoleOrder, role) {
		return startPlannedLaunchResult{}, fmt.Errorf("planned item %s has unsupported scout_role %q", item.ID, role)
	}
	destination := normalizeScoutDestination(item.ScoutDestination)
	if destination == improvementDestinationTarget || destination == improvementDestinationFork {
		return startPlannedLaunchResult{}, fmt.Errorf("planned item %s has unsupported scout_destination %q", item.ID, item.ScoutDestination)
	}
	options := ImproveOptions{
		Target:       repoSlug,
		RunID:        startPlannedGeneratedRunID("scout"),
		Focus:        append([]string{}, item.ScoutFocus...),
		SessionLimit: item.ScoutSessionLimit,
		CodexArgs:    append([]string{}, codexArgs...),
	}
	policy := scoutPolicy{
		Version:          1,
		IssueDestination: destination,
	}
	if normalizeFindingsHandling(item.FindingsHandling, item.ScoutDestination, item.LaunchKind) != "" {
		policy.IssueDestination = improvementDestinationReview
	}
	if scoutRoleSupportsSessionLimit(role) && item.ScoutSessionLimit > 0 {
		policy.SessionLimit = item.ScoutSessionLimit
	}
	if err := startRunScheduledPlannedScout(cwd, options, role, policy); err != nil {
		return startPlannedLaunchResult{}, err
	}
	return startPlannedLaunchResult{
		Status: "completed",
		Result: fmt.Sprintf("%s completed", role),
		RunID:  options.RunID,
	}, nil
}

func launchStartPlannedLocalWork(repoSlug string, item startWorkPlannedItem, codexArgs []string) (startPlannedLaunchResult, error) {
	repoPath, err := resolveStartPlannedRepoPath(repoSlug)
	if err != nil {
		return startPlannedLaunchResult{}, err
	}
	if _, err := parseRequiredWorkType(item.WorkType, "planned_item.work_type"); err != nil {
		return startPlannedLaunchResult{}, err
	}
	args := []string{"work", "start", "--repo", repoPath, "--task", startPlannedLocalWorkTask(item), "--work-type", item.WorkType}
	if len(codexArgs) > 0 {
		args = append(args, "--")
		args = append(args, codexArgs...)
	}
	logPath := filepath.Join(githubManagedPaths(repoSlug).PlannedRunsDir, item.ID+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return startPlannedLaunchResult{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return startPlannedLaunchResult{}, err
	}
	cmd, err := startManagedNanaCommand(args...)
	if err != nil {
		_ = logFile.Close()
		return startPlannedLaunchResult{}, err
	}
	cmd.Dir = repoPath
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := startManagedNanaStart(cmd); err != nil {
		_ = logFile.Close()
		return startPlannedLaunchResult{}, err
	}
	go func() {
		defer logFile.Close()
		_ = cmd.Wait()
	}()
	return startPlannedLaunchResult{
		Status: "spawned",
		Result: "local work started; logs at " + logPath,
	}, nil
}

func launchStartPlannedLocalWorkScheduled(repoSlug string, item startWorkPlannedItem, codexArgs []string) (startPlannedLaunchResult, error) {
	repoPath, err := resolveStartPlannedRepoPath(repoSlug)
	if err != nil {
		return startPlannedLaunchResult{}, err
	}
	if _, err := parseRequiredWorkType(item.WorkType, "planned_item.work_type"); err != nil {
		return startPlannedLaunchResult{}, err
	}
	runID := startPlannedGeneratedRunID("lw")
	args := []string{"start", "--repo", repoPath, "--task", startPlannedLocalWorkTask(item), "--run-id", runID, "--work-type", item.WorkType}
	if len(codexArgs) > 0 {
		args = append(args, "--")
		args = append(args, codexArgs...)
	}
	if err := startRunScheduledPlannedLocalWork(repoPath, args); err != nil {
		return startPlannedLaunchResult{}, err
	}
	return startPlannedLaunchResult{
		Status: "completed",
		Result: "local work completed",
		RunID:  runID,
	}, nil
}

func launchStartPlannedTrackedIssue(repoSlug string, item startWorkPlannedItem) (startPlannedLaunchResult, error) {
	targetURL := strings.TrimSpace(item.TargetURL)
	if targetURL == "" {
		return startPlannedLaunchResult{}, fmt.Errorf("planned item %s is missing target_url for tracked issue launch", item.ID)
	}
	if _, err := parseRequiredWorkType(item.WorkType, "planned_item.work_type"); err != nil {
		return startPlannedLaunchResult{}, err
	}
	repoPath, err := resolveStartPlannedRepoPath(repoSlug)
	if err != nil {
		return startPlannedLaunchResult{}, err
	}
	logPath := filepath.Join(githubManagedPaths(repoSlug).PlannedRunsDir, item.ID+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return startPlannedLaunchResult{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return startPlannedLaunchResult{}, err
	}
	cmd, err := startManagedNanaCommand("work", "start", targetURL, "--work-type", item.WorkType)
	if err != nil {
		_ = logFile.Close()
		return startPlannedLaunchResult{}, err
	}
	cmd.Dir = repoPath
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := startManagedNanaStart(cmd); err != nil {
		_ = logFile.Close()
		return startPlannedLaunchResult{}, err
	}
	go func() {
		defer logFile.Close()
		_ = cmd.Wait()
	}()
	return startPlannedLaunchResult{
		Status: "spawned",
		Result: "tracked issue work started; logs at " + logPath,
	}, nil
}

func launchStartPlannedTrackedIssueScheduled(repoSlug string, item startWorkPlannedItem, codexArgs []string) (startPlannedLaunchResult, error) {
	targetURL := strings.TrimSpace(item.TargetURL)
	if targetURL == "" {
		return startPlannedLaunchResult{}, fmt.Errorf("planned item %s is missing target_url for tracked issue launch", item.ID)
	}
	if _, err := parseRequiredWorkType(item.WorkType, "planned_item.work_type"); err != nil {
		return startPlannedLaunchResult{}, err
	}
	repoPath, err := resolveStartPlannedRepoPath(repoSlug)
	if err != nil {
		return startPlannedLaunchResult{}, err
	}
	args := []string{"start", targetURL, "--work-type", item.WorkType}
	if len(codexArgs) > 0 {
		args = append(args, "--")
		args = append(args, codexArgs...)
	}
	run, err := startRunScheduledPlannedGithubWork(repoPath, args)
	if err != nil {
		return startPlannedLaunchResult{RunID: run.RunID}, err
	}
	return startPlannedLaunchResult{
		Status: "completed",
		Result: "tracked issue work completed",
		RunID:  run.RunID,
	}, nil
}

func launchStartPlannedGithubIssue(repoSlug string, item startWorkPlannedItem) (startPlannedLaunchResult, error) {
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return startPlannedLaunchResult{}, err
	}
	if _, err := parseRequiredWorkType(item.WorkType, "planned_item.work_type"); err != nil {
		return startPlannedLaunchResult{}, err
	}
	labels := applyStartWorkPriorityLabel([]string{"nana", workTypeCanonicalLabel(item.WorkType)}, item.Priority)
	if err := ensureStartWorkLabels(repoSlug, labels, apiBaseURL, token); err != nil {
		return startPlannedLaunchResult{}, err
	}
	body := strings.TrimSpace(item.Description)
	if body != "" {
		body += "\n\n"
	}
	body += "Created by Nana Operator Console."
	payload := map[string]any{
		"title":  item.Title,
		"body":   body,
		"labels": labels,
	}
	var created startWorkIssuePayload
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/issues", repoSlug), payload, &created); err != nil {
		return startPlannedLaunchResult{}, err
	}
	return startPlannedLaunchResult{
		Status:      "created_issue",
		Result:      "created GitHub issue #" + strconv.Itoa(created.Number),
		IssueNumber: created.Number,
		IssueURL:    created.HTMLURL,
	}, nil
}

func applyStartWorkPriorityLabel(labels []string, priority int) []string {
	next := make([]string, 0, len(labels)+1)
	for _, label := range labels {
		upper := strings.ToUpper(strings.TrimSpace(label))
		if len(upper) == 2 && upper[0] == 'P' && upper[1] >= '0' && upper[1] <= '5' {
			continue
		}
		if strings.TrimSpace(label) != "" {
			next = append(next, strings.TrimSpace(label))
		}
	}
	if priority >= 0 && priority <= 5 {
		next = append(next, startWorkPriorityLabel(priority))
	}
	return uniqueStrings(next)
}

func ensureStartWorkLabels(repoSlug string, labels []string, apiBaseURL string, token string) error {
	for _, label := range labels {
		if err := ensureStartWorkLabel(repoSlug, label, apiBaseURL, token); err != nil {
			return err
		}
	}
	return nil
}

func ensureStartWorkLabel(repoSlug string, label string, apiBaseURL string, token string) error {
	name := strings.TrimSpace(label)
	if name == "" {
		return nil
	}
	color, description := startWorkLabelStyle(name)
	payload := map[string]any{
		"name":        name,
		"color":       color,
		"description": description,
	}
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/labels", repoSlug), payload, &struct{}{}); err != nil {
		if strings.Contains(err.Error(), "already_exists") || strings.Contains(err.Error(), "Validation Failed") {
			return nil
		}
		return err
	}
	return nil
}

func startWorkLabelStyle(label string) (string, string) {
	switch strings.ToUpper(strings.TrimSpace(label)) {
	case "NANA":
		return "0366d6", "Nana automation issue"
	case "P0":
		return "b60205", "Critical priority"
	case "P1":
		return "d93f0b", "Highest priority"
	case "P2":
		return "fbca04", "High priority"
	case "P3":
		return "0e8a16", "Medium priority"
	case "P4":
		return "5319e7", "Low priority"
	case "P5":
		return "6f42c1", "Lowest priority"
	default:
		return "1d76db", "Nana automation label"
	}
}

func mirrorStartWorkIssuePriority(repoSlug string, issueNumber int, labels []string, priority int) ([]string, error) {
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return nil, err
	}
	nextLabels := applyStartWorkPriorityLabel(labels, priority)
	if err := ensureStartWorkLabels(repoSlug, nextLabels, apiBaseURL, token); err != nil {
		return nil, err
	}
	var response []githubLabel
	if err := githubAPIRequestJSON("PUT", apiBaseURL, token, fmt.Sprintf("/repos/%s/issues/%d/labels", repoSlug, issueNumber), nextLabels, &response); err != nil {
		return nil, err
	}
	return startWorkIssueLabelNames(response), nil
}
