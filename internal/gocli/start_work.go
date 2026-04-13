package gocli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const StartWorkHelp = `internal start helper

Use:
  nana repo onboard <owner/repo> --repo-mode <local|fork|repo> --issue-pick <manual|label|auto> --pr-forward <approve|auto>
  nana repo explain <owner/repo>
  nana start
`

const (
	startWorkStateVersion         = 2
	startWorkDefaultParallel      = 3
	startWorkDefaultOpenPRCap     = 10
	startWorkStatusQueued         = "queued"
	startWorkStatusInProgress     = "in_progress"
	startWorkStatusReconciling    = "reconciling"
	startWorkStatusCompleted      = "completed"
	startWorkStatusBlocked        = "blocked"
	startWorkStatusCopied         = "copied"
	startWorkStatusPromoted       = "promoted"
	startWorkStatusNotActioned    = "not_actioned"
	startWorkTriageQueued         = "queued"
	startWorkTriageRunning        = "running"
	startWorkTriageCompleted      = "completed"
	startWorkTriageFailed         = "failed"
	startWorkServiceTaskQueued    = "queued"
	startWorkServiceTaskRunning   = "running"
	startWorkServiceTaskCompleted = "completed"
	startWorkServiceTaskFailed    = "failed"
)

type startWorkOptions struct {
	RepoSlug       string
	Parallel       int
	MaxOpenPR      int
	ForkIssuesMode string
	ImplementMode  string
	PublishTarget  string
	RepoMode       string
	IssuePickMode  string
	PRForwardMode  string
	CodexArgs      []string
	JSON           bool
}

type startWorkState struct {
	Version        int                               `json:"version"`
	SourceRepo     string                            `json:"source_repo"`
	ForkRepo       string                            `json:"fork_repo"`
	ForkOwner      string                            `json:"fork_owner"`
	DefaultBranch  string                            `json:"default_branch,omitempty"`
	CreatedAt      string                            `json:"created_at,omitempty"`
	UpdatedAt      string                            `json:"updated_at"`
	Issues         map[string]startWorkIssueState    `json:"issues"`
	ServiceTasks   map[string]startWorkServiceTask   `json:"service_tasks,omitempty"`
	Preferences    startWorkPreferences              `json:"preferences"`
	LastRun        *startWorkLastRun                 `json:"last_run,omitempty"`
	Promotions     map[string]startWorkPromotion     `json:"promotions,omitempty"`
	PromotionSkips map[string]startWorkPromotionSkip `json:"promotion_skips,omitempty"`
}

type startWorkIssueState struct {
	SourceNumber      int      `json:"source_number"`
	ForkNumber        int      `json:"fork_number,omitempty"`
	SourceURL         string   `json:"source_url,omitempty"`
	SourceBody        string   `json:"source_body,omitempty"`
	ForkURL           string   `json:"fork_url,omitempty"`
	Title             string   `json:"title"`
	State             string   `json:"state"`
	Labels            []string `json:"labels,omitempty"`
	SourceFingerprint string   `json:"source_fingerprint,omitempty"`
	Priority          int      `json:"priority"`
	PrioritySource    string   `json:"priority_source,omitempty"`
	Complexity        int      `json:"complexity"`
	Status            string   `json:"status"`
	TriageStatus      string   `json:"triage_status,omitempty"`
	TriageRationale   string   `json:"triage_rationale,omitempty"`
	TriageFingerprint string   `json:"triage_fingerprint,omitempty"`
	TriageUpdatedAt   string   `json:"triage_updated_at,omitempty"`
	TriageError       string   `json:"triage_error,omitempty"`
	LastRunID         string   `json:"last_run_id,omitempty"`
	LastRunUpdatedAt  string   `json:"last_run_updated_at,omitempty"`
	LastRunError      string   `json:"last_run_error,omitempty"`
	PublishedPRNumber int      `json:"published_pr_number,omitempty"`
	PublishedPRURL    string   `json:"published_pr_url,omitempty"`
	PublicationState  string   `json:"publication_state,omitempty"`
	BlockedReason     string   `json:"blocked_reason,omitempty"`
	UpdatedAt         string   `json:"updated_at"`
}

type startWorkPreferences struct {
	UpdatedAt string              `json:"updated_at,omitempty"`
	Artifacts map[string]string   `json:"artifacts,omitempty"`
	Source    *startWorkRepoPrefs `json:"source,omitempty"`
	Fork      *startWorkRepoPrefs `json:"fork,omitempty"`
}

type startWorkRepoPrefs struct {
	DefaultConsiderations []string `json:"default_considerations,omitempty"`
	DefaultRoleLayout     string   `json:"default_role_layout,omitempty"`
	ReviewRulesMode       string   `json:"review_rules_mode,omitempty"`
}

type startWorkLastRun struct {
	StartedIssueNumbers        []int  `json:"started_issue_numbers,omitempty"`
	SkippedReason              string `json:"skipped_reason,omitempty"`
	OpenForkPRs                int    `json:"open_fork_prs"`
	ParallelLimit              int    `json:"parallel_limit"`
	GlobalParallelLimit        int    `json:"global_parallel_limit,omitempty"`
	RepoWorkerLimit            int    `json:"repo_worker_limit,omitempty"`
	OpenPRCap                  int    `json:"open_pr_cap"`
	ServiceStartedCount        int    `json:"service_started_count,omitempty"`
	ImplementationStartedCount int    `json:"implementation_started_count,omitempty"`
	UpdatedAt                  string `json:"updated_at"`
}

type startWorkPromotion struct {
	ForkPRNumber     int    `json:"fork_pr_number"`
	UpstreamPRNumber int    `json:"upstream_pr_number"`
	UpstreamPRURL    string `json:"upstream_pr_url,omitempty"`
	HeadRef          string `json:"head_ref"`
	PromotedAt       string `json:"promoted_at"`
	Reused           bool   `json:"reused,omitempty"`
}

type startWorkPromotionSkip struct {
	ForkPRNumber int    `json:"fork_pr_number"`
	Reason       string `json:"reason"`
	HeadRef      string `json:"head_ref,omitempty"`
	SkippedAt    string `json:"skipped_at"`
}

type startWorkServiceTask struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Queue          string   `json:"queue"`
	Status         string   `json:"status"`
	IssueKey       string   `json:"issue_key,omitempty"`
	ScoutRole      string   `json:"scout_role,omitempty"`
	Generation     string   `json:"generation,omitempty"`
	Fingerprint    string   `json:"fingerprint,omitempty"`
	RunID          string   `json:"run_id,omitempty"`
	DependencyKeys []string `json:"dependency_keys,omitempty"`
	Attempts       int      `json:"attempts,omitempty"`
	LastError      string   `json:"last_error,omitempty"`
	ResultSummary  string   `json:"result_summary,omitempty"`
	StartedAt      string   `json:"started_at,omitempty"`
	CompletedAt    string   `json:"completed_at,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

type startWorkIssuePayload struct {
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	State       string        `json:"state"`
	HTMLURL     string        `json:"html_url"`
	Labels      []githubLabel `json:"labels"`
	PullRequest *struct{}     `json:"pull_request,omitempty"`
}

type githubLabel struct {
	Name string `json:"name"`
}

type startWorkPullPayload struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	HTMLURL  string `json:"html_url"`
	State    string `json:"state"`
	MergedAt string `json:"merged_at"`
	Draft    bool   `json:"draft"`
	Head     struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type startWorkLaunchResult struct {
	RunID string
}

var startWorkRunGithubWork = func(issueURL string, publishTarget string, codexArgs []string) (startWorkLaunchResult, error) {
	args := []string{"start", issueURL}
	if publishTarget == "local-branch" {
		args = append(args, "--local-only")
	} else {
		args = append(args, "--create-pr")
	}
	if len(codexArgs) > 0 {
		args = append(args, "--")
		args = append(args, codexArgs...)
	}
	result, err := GithubWorkCommand("", args)
	return startWorkLaunchResult{RunID: result.RunID}, err
}

func StartWork(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, StartWorkHelp)
		return nil
	}
	switch args[0] {
	case "start":
		options, err := parseStartWorkStartArgs(args[1:])
		if err != nil {
			return err
		}
		return startWorkStart(options)
	case "promote":
		options, err := parseStartWorkRepoArgs(args[1:])
		if err != nil {
			return err
		}
		return startWorkPromote(options)
	case "status":
		options, err := parseStartWorkStatusArgs(args[1:])
		if err != nil {
			return err
		}
		return startWorkStatus(options)
	default:
		return fmt.Errorf("Unknown start subcommand: %s\n\n%s", args[0], StartWorkHelp)
	}
}

func parseStartWorkStartArgs(args []string) (startWorkOptions, error) {
	options := startWorkOptions{Parallel: startWorkDefaultParallel, MaxOpenPR: startWorkDefaultOpenPRCap, ForkIssuesMode: "auto", ImplementMode: "auto", RepoMode: "local", IssuePickMode: "auto", PRForwardMode: "approve"}
	passthroughIndex := len(args)
	for index, token := range args {
		if token == "--" {
			passthroughIndex = index
			break
		}
	}
	parseArgs := args[:passthroughIndex]
	if passthroughIndex < len(args) {
		options.CodexArgs = append([]string{}, args[passthroughIndex+1:]...)
	}
	if len(parseArgs) == 0 {
		return startWorkOptions{}, fmt.Errorf("Usage: nana start --repo <owner/repo>\n\n%s", StartWorkHelp)
	}
	options.RepoSlug = strings.TrimSpace(parseArgs[0])
	if err := validateStartWorkRepoSlug(options.RepoSlug); err != nil {
		return startWorkOptions{}, err
	}
	for index := 1; index < len(parseArgs); index++ {
		token := parseArgs[index]
		switch {
		case token == "--parallel":
			value, err := requireStartWorkFlagValue(parseArgs, index, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 {
				return startWorkOptions{}, fmt.Errorf("Invalid --parallel value %q.\n%s", value, StartWorkHelp)
			}
			options.Parallel = parsed
			index++
		case strings.HasPrefix(token, "--parallel="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--parallel="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return startWorkOptions{}, fmt.Errorf("Invalid --parallel value %q.\n%s", value, StartWorkHelp)
			}
			options.Parallel = parsed
		case token == "--repo-mode":
			value, err := requireStartWorkFlagValue(parseArgs, index, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			parsed, err := parseGithubRepoMode(value, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			options.RepoMode = parsed
			options.PublishTarget = repoModeToPublishTarget(parsed)
			index++
		case strings.HasPrefix(token, "--repo-mode="):
			parsed, err := parseGithubRepoMode(strings.TrimPrefix(token, "--repo-mode="), "--repo-mode")
			if err != nil {
				return startWorkOptions{}, err
			}
			options.RepoMode = parsed
			options.PublishTarget = repoModeToPublishTarget(parsed)
		case token == "--issue-pick":
			value, err := requireStartWorkFlagValue(parseArgs, index, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			parsed, err := parseGithubIssuePickMode(value, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			options.IssuePickMode = parsed
			options.ForkIssuesMode = issuePickModeToAutomationMode(parsed)
			options.ImplementMode = issuePickModeToAutomationMode(parsed)
			index++
		case strings.HasPrefix(token, "--issue-pick="):
			parsed, err := parseGithubIssuePickMode(strings.TrimPrefix(token, "--issue-pick="), "--issue-pick")
			if err != nil {
				return startWorkOptions{}, err
			}
			options.IssuePickMode = parsed
			options.ForkIssuesMode = issuePickModeToAutomationMode(parsed)
			options.ImplementMode = issuePickModeToAutomationMode(parsed)
		case token == "--pr-forward":
			value, err := requireStartWorkFlagValue(parseArgs, index, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			parsed, err := parseGithubPRForwardMode(value, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			options.PRForwardMode = parsed
			index++
		case strings.HasPrefix(token, "--pr-forward="):
			parsed, err := parseGithubPRForwardMode(strings.TrimPrefix(token, "--pr-forward="), "--pr-forward")
			if err != nil {
				return startWorkOptions{}, err
			}
			options.PRForwardMode = parsed
		case token == "--fork-issues":
			value, err := requireStartWorkFlagValue(parseArgs, index, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			parsed, err := parseGithubAutomationMode(value, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			options.ForkIssuesMode = parsed
			options.IssuePickMode = automationModeToIssuePickMode(parsed)
			index++
		case strings.HasPrefix(token, "--fork-issues="):
			parsed, err := parseGithubAutomationMode(strings.TrimPrefix(token, "--fork-issues="), "--fork-issues")
			if err != nil {
				return startWorkOptions{}, err
			}
			options.ForkIssuesMode = parsed
			options.IssuePickMode = automationModeToIssuePickMode(parsed)
		case token == "--implement":
			value, err := requireStartWorkFlagValue(parseArgs, index, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			parsed, err := parseGithubAutomationMode(value, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			options.ImplementMode = parsed
			options.IssuePickMode = automationModeToIssuePickMode(parsed)
			index++
		case strings.HasPrefix(token, "--implement="):
			parsed, err := parseGithubAutomationMode(strings.TrimPrefix(token, "--implement="), "--implement")
			if err != nil {
				return startWorkOptions{}, err
			}
			options.ImplementMode = parsed
			options.IssuePickMode = automationModeToIssuePickMode(parsed)
		case token == "--publish":
			value, err := requireStartWorkFlagValue(parseArgs, index, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			parsed, err := parseGithubPublishTarget(value, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			options.PublishTarget = parsed
			options.RepoMode = publishTargetToRepoMode(parsed)
			index++
		case strings.HasPrefix(token, "--publish="):
			parsed, err := parseGithubPublishTarget(strings.TrimPrefix(token, "--publish="), "--publish")
			if err != nil {
				return startWorkOptions{}, err
			}
			options.PublishTarget = parsed
			options.RepoMode = publishTargetToRepoMode(parsed)
		case token == "--max-open-prs":
			value, err := requireStartWorkFlagValue(parseArgs, index, token)
			if err != nil {
				return startWorkOptions{}, err
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 {
				return startWorkOptions{}, fmt.Errorf("Invalid --max-open-prs value %q.\n%s", value, StartWorkHelp)
			}
			options.MaxOpenPR = parsed
			index++
		case strings.HasPrefix(token, "--max-open-prs="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--max-open-prs="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return startWorkOptions{}, fmt.Errorf("Invalid --max-open-prs value %q.\n%s", value, StartWorkHelp)
			}
			options.MaxOpenPR = parsed
		default:
			return startWorkOptions{}, fmt.Errorf("Unknown start option: %s\n\n%s", token, StartWorkHelp)
		}
	}
	return options, nil
}

func parseStartWorkRepoArgs(args []string) (startWorkOptions, error) {
	if len(args) != 1 {
		return startWorkOptions{}, fmt.Errorf("Usage: nana start --repo <owner/repo>\n\n%s", StartWorkHelp)
	}
	repoSlug := strings.TrimSpace(args[0])
	if err := validateStartWorkRepoSlug(repoSlug); err != nil {
		return startWorkOptions{}, err
	}
	return startWorkOptions{RepoSlug: repoSlug, Parallel: startWorkDefaultParallel, MaxOpenPR: startWorkDefaultOpenPRCap, ForkIssuesMode: "auto", ImplementMode: "auto"}, nil
}

func parseStartWorkStatusArgs(args []string) (startWorkOptions, error) {
	options := startWorkOptions{}
	for _, token := range args {
		switch token {
		case "--json":
			options.JSON = true
		default:
			if options.RepoSlug != "" {
				return startWorkOptions{}, fmt.Errorf("Usage: nana repo explain <owner/repo> [--json]\n\n%s", StartWorkHelp)
			}
			options.RepoSlug = strings.TrimSpace(token)
		}
	}
	if err := validateStartWorkRepoSlug(options.RepoSlug); err != nil {
		return startWorkOptions{}, err
	}
	return options, nil
}

func validateStartWorkRepoSlug(repoSlug string) error {
	parts := strings.Split(repoSlug, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("expected repo as <owner/repo>, got %q", repoSlug)
	}
	return nil
}

func requireStartWorkFlagValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, StartWorkHelp)
	}
	return args[index+1], nil
}

func startWorkStart(options startWorkOptions) error {
	options, state, openPRCount, created, err := startWorkSyncRepoState(options)
	if err != nil {
		return err
	}
	started, skippedReason, err := startStartWorkQueue(state, options, openPRCount)
	if err != nil {
		return err
	}
	state.LastRun = &startWorkLastRun{
		StartedIssueNumbers:        started,
		SkippedReason:              skippedReason,
		OpenForkPRs:                openPRCount,
		ParallelLimit:              options.Parallel,
		RepoWorkerLimit:            options.Parallel,
		OpenPRCap:                  options.MaxOpenPR,
		ImplementationStartedCount: len(started),
		UpdatedAt:                  time.Now().UTC().Format(time.RFC3339),
	}
	state.UpdatedAt = state.LastRun.UpdatedAt
	if err := writeStartWorkState(*state); err != nil {
		return err
	}

	if created {
		fmt.Fprintf(os.Stdout, "[start] Created fork %s for %s.\n", state.ForkRepo, options.RepoSlug)
	} else {
		fmt.Fprintf(os.Stdout, "[start] Using fork %s for %s.\n", state.ForkRepo, options.RepoSlug)
	}
	fmt.Fprintf(os.Stdout, "[start] Mirrored issues: %d. Open fork PRs: %d/%d.\n", len(state.Issues), openPRCount, options.MaxOpenPR)
	if skippedReason != "" {
		fmt.Fprintf(os.Stdout, "[start] Queue start skipped: %s.\n", skippedReason)
	} else {
		fmt.Fprintf(os.Stdout, "[start] Started fork issue workers: %s.\n", joinIntsOrNone(started))
	}
	fmt.Fprintf(os.Stdout, "[start] State: %s\n", startWorkStatePath(options.RepoSlug))
	return nil
}

func startWorkSyncRepoState(options startWorkOptions) (startWorkOptions, *startWorkState, int, bool, error) {
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return options, nil, 0, false, err
	}
	viewer, err := githubCurrentViewer(apiBaseURL, token)
	if err != nil {
		return options, nil, 0, false, err
	}
	sourceRepo, err := startWorkFetchRepo(options.RepoSlug, apiBaseURL, token)
	if err != nil {
		return options, nil, 0, false, err
	}
	forkRepo, created, err := ensureGithubFork(options.RepoSlug, sourceRepo.Name, viewer, apiBaseURL, token)
	if err != nil {
		return options, nil, 0, false, err
	}
	if err := ensureStartWorkForkReady(forkRepo.FullName, apiBaseURL, token); err != nil {
		return options, nil, 0, false, err
	}

	state, err := readStartWorkState(options.RepoSlug)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return options, nil, 0, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if state == nil {
		state = &startWorkState{Version: startWorkStateVersion, SourceRepo: options.RepoSlug, CreatedAt: now, Issues: map[string]startWorkIssueState{}, Promotions: map[string]startWorkPromotion{}}
	}
	state.Version = startWorkStateVersion
	state.SourceRepo = options.RepoSlug
	state.ForkOwner = viewer
	state.ForkRepo = forkRepo.FullName
	state.DefaultBranch = sourceRepo.DefaultBranch
	state.UpdatedAt = now
	if state.Issues == nil {
		state.Issues = map[string]startWorkIssueState{}
	}
	if state.ServiceTasks == nil {
		state.ServiceTasks = map[string]startWorkServiceTask{}
	}
	if state.Promotions == nil {
		state.Promotions = map[string]startWorkPromotion{}
	}
	if state.PromotionSkips == nil {
		state.PromotionSkips = map[string]startWorkPromotionSkip{}
	}
	options.RepoMode = defaultString(normalizeGithubRepoMode(options.RepoMode), publishTargetToRepoMode(options.PublishTarget))
	if options.RepoMode == "" {
		options.RepoMode = "local"
	}
	options.IssuePickMode = defaultString(normalizeGithubIssuePickMode(options.IssuePickMode), automationModeToIssuePickMode(options.ImplementMode))
	if options.IssuePickMode == "" {
		options.IssuePickMode = "manual"
	}
	options.PRForwardMode = defaultString(normalizeGithubPRForwardMode(options.PRForwardMode), "approve")
	options.ForkIssuesMode = defaultString(normalizeGithubAutomationMode(options.ForkIssuesMode), issuePickModeToAutomationMode(options.IssuePickMode))
	options.ImplementMode = defaultString(normalizeGithubAutomationMode(options.ImplementMode), issuePickModeToAutomationMode(options.IssuePickMode))
	options.PublishTarget = defaultString(normalizeGithubPublishTarget(options.PublishTarget), repoModeToPublishTarget(options.RepoMode))

	if err := mirrorStartWorkIssues(state, forkRepo.FullName, options.ForkIssuesMode, apiBaseURL, token); err != nil {
		return options, nil, 0, false, err
	}
	refreshStartWorkPreferences(state)

	openPRs, err := listStartWorkPulls(forkRepo.FullName, "open", apiBaseURL, token)
	if err != nil {
		return options, nil, 0, false, err
	}
	return options, state, len(openPRs), created, nil
}

func mirrorStartWorkIssues(state *startWorkState, forkRepo string, forkIssuesMode string, apiBaseURL string, token string) error {
	issues, err := listStartWorkIssues(state.SourceRepo, apiBaseURL, token)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		if issue.PullRequest != nil {
			continue
		}
		labels := startWorkIssueLabelNames(issue.Labels)
		key := strconv.Itoa(issue.Number)
		if !startWorkAutomationAllowsIssue(forkIssuesMode, labels, "fork") {
			if existing, ok := state.Issues[key]; ok && existing.Status == "" {
				existing.Status = startWorkStatusQueued
				state.Issues[key] = existing
			}
			continue
		}
		existing := state.Issues[key]
		sourceFingerprint := startWorkIssueFingerprint(issue, labels)
		priority, prioritySource, triageStatus, triageRationale, triageFingerprint, triageUpdatedAt, triageError := startWorkResolvePriority(existing, issue, labels, sourceFingerprint)
		complexity := startWorkComplexity(labels)
		status := existing.Status
		if status == "" {
			status = startWorkStatusQueued
		}
		if issue.State != "open" && (status == startWorkStatusQueued || status == startWorkStatusCopied) {
			status = startWorkStatusNotActioned
		}
		updated := startWorkIssueState{
			SourceNumber:      issue.Number,
			ForkNumber:        existing.ForkNumber,
			SourceURL:         issue.HTMLURL,
			SourceBody:        issue.Body,
			ForkURL:           existing.ForkURL,
			Title:             issue.Title,
			State:             issue.State,
			Labels:            labels,
			SourceFingerprint: sourceFingerprint,
			Priority:          priority,
			PrioritySource:    prioritySource,
			Complexity:        complexity,
			Status:            status,
			TriageStatus:      triageStatus,
			TriageRationale:   triageRationale,
			TriageFingerprint: triageFingerprint,
			TriageUpdatedAt:   triageUpdatedAt,
			TriageError:       triageError,
			UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
		}
		if updated.ForkNumber == 0 {
			created, err := createStartWorkIssue(forkRepo, issue, labels, apiBaseURL, token)
			if err != nil {
				return err
			}
			updated.ForkNumber = created.Number
			updated.ForkURL = created.HTMLURL
			if updated.Status == startWorkStatusQueued && issue.State != "open" {
				updated.Status = startWorkStatusNotActioned
			}
			if issue.State != "open" {
				_ = closeStartWorkIssue(forkRepo, created.Number, apiBaseURL, token)
			}
		}
		state.Issues[key] = updated
	}
	return nil
}

func startStartWorkQueue(state *startWorkState, options startWorkOptions, openForkPRs int) ([]int, string, error) {
	queue, skippedReason := startWorkBuildImplementationQueue(state, options, openForkPRs)
	inProgress := startWorkImplementationInProgress(state)
	if skippedReason != "" {
		return nil, skippedReason, nil
	}
	available := options.Parallel - inProgress
	if available <= 0 {
		return nil, fmt.Sprintf("repo worker capacity exhausted (in_progress=%d)", inProgress), nil
	}
	if available > len(queue) {
		available = len(queue)
	}
	selected := queue[:available]
	var wg sync.WaitGroup
	errs := make(chan error, len(selected))
	started := make([]int, 0, len(selected))
	for _, issue := range selected {
		key := strconv.Itoa(issue.SourceNumber)
		issue.Status = startWorkStatusInProgress
		issue.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		state.Issues[key] = issue
		started = append(started, issue.SourceNumber)
		wg.Add(1)
		go func(issue startWorkIssueState) {
			defer wg.Done()
			issueURL := issue.SourceURL
			if options.PublishTarget == "fork" {
				issueURL = issue.ForkURL
			}
			if _, err := startWorkRunGithubWork(issueURL, options.PublishTarget, options.CodexArgs); err != nil {
				errs <- fmt.Errorf("source issue #%d fork issue #%d: %w", issue.SourceNumber, issue.ForkNumber, err)
			}
		}(issue)
	}
	wg.Wait()
	close(errs)
	collected := []string{}
	for err := range errs {
		collected = append(collected, err.Error())
	}
	if len(collected) > 0 {
		return started, "", fmt.Errorf("fork issue worker failures:\n%s", strings.Join(collected, "\n"))
	}
	return started, "", nil
}

func startWorkPromote(options startWorkOptions) error {
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}
	state, err := readStartWorkState(options.RepoSlug)
	if err != nil {
		return err
	}
	if state.ForkRepo == "" {
		return fmt.Errorf("start state for %s does not know fork repo; run `nana start --repo %s` first", options.RepoSlug, options.RepoSlug)
	}
	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(options.RepoSlug))
	options.PRForwardMode = defaultString(normalizeGithubPRForwardMode(options.PRForwardMode), resolvedGithubPRForwardMode(settings))
	pullState := "closed"
	if options.PRForwardMode == "auto" {
		pullState = "open"
	}
	candidatePRs, err := listStartWorkPulls(state.ForkRepo, pullState, apiBaseURL, token)
	if err != nil {
		return err
	}
	if state.Promotions == nil {
		state.Promotions = map[string]startWorkPromotion{}
	}
	if state.PromotionSkips == nil {
		state.PromotionSkips = map[string]startWorkPromotionSkip{}
	}
	promoted := []int{}
	reused := []int{}
	skipped := []string{}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, pr := range candidatePRs {
		key := strconv.Itoa(pr.Number)
		if state.Promotions[key].UpstreamPRNumber > 0 {
			continue
		}
		if options.PRForwardMode == "auto" {
			if reason, err := startWorkAutoForwardSkipReason(*state, pr, apiBaseURL, token); err != nil {
				return err
			} else if reason != "" {
				state.PromotionSkips[key] = startWorkPromotionSkip{ForkPRNumber: pr.Number, Reason: reason, HeadRef: pr.Head.Ref, SkippedAt: now}
				skipped = append(skipped, fmt.Sprintf("fork PR #%d: %s", pr.Number, reason))
				continue
			}
		} else if strings.TrimSpace(pr.MergedAt) == "" {
			reason := "fork PR closed without merge"
			state.PromotionSkips[key] = startWorkPromotionSkip{ForkPRNumber: pr.Number, Reason: reason, HeadRef: pr.Head.Ref, SkippedAt: now}
			skipped = append(skipped, fmt.Sprintf("fork PR #%d: %s", pr.Number, reason))
			continue
		}
		created, wasReused, err := ensureStartWorkUpstreamPR(*state, pr, apiBaseURL, token)
		if err != nil {
			return err
		}
		delete(state.PromotionSkips, key)
		state.Promotions[key] = startWorkPromotion{ForkPRNumber: pr.Number, UpstreamPRNumber: created.Number, UpstreamPRURL: created.HTMLURL, HeadRef: pr.Head.Ref, PromotedAt: now, Reused: wasReused}
		if wasReused {
			reused = append(reused, created.Number)
		} else {
			promoted = append(promoted, created.Number)
		}
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	refreshStartWorkPreferences(state)
	if err := writeStartWorkState(*state); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[start] Promoted upstream PRs: %s.\n", joinIntsOrNone(promoted))
	if len(reused) > 0 {
		fmt.Fprintf(os.Stdout, "[start] Reused upstream PRs: %s.\n", joinIntsOrNone(reused))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(os.Stdout, "[start] Skipped upstream PRs: %s.\n", strings.Join(skipped, "; "))
	}
	fmt.Fprintf(os.Stdout, "[start] State: %s\n", startWorkStatePath(options.RepoSlug))
	return nil
}

func startWorkPromotionCounts(state *startWorkState) (int, int, int) {
	if state == nil {
		return 0, 0, 0
	}
	promoted := 0
	reused := 0
	for _, promotion := range state.Promotions {
		if promotion.UpstreamPRNumber <= 0 {
			continue
		}
		if promotion.Reused {
			reused++
		} else {
			promoted++
		}
	}
	return promoted, reused, len(state.PromotionSkips)
}

func startWorkAutoForwardSkipReason(state startWorkState, forkPR startWorkPullPayload, apiBaseURL string, token string) (string, error) {
	if strings.TrimSpace(forkPR.State) != "" && strings.TrimSpace(forkPR.State) != "open" {
		return "fork PR is not open", nil
	}
	if forkPR.Draft {
		return "fork PR is draft", nil
	}
	headSHA := strings.TrimSpace(forkPR.Head.SHA)
	if headSHA == "" {
		return "fork PR head SHA is missing", nil
	}
	ciResult, err := readGithubCIResult(state.ForkRepo, headSHA, apiBaseURL, token)
	if err != nil {
		return "", err
	}
	if ciResult.State != "ci_green" {
		detail := defaultString(ciResult.Detail, ciResult.State)
		return "fork PR CI is not green: " + detail, nil
	}
	return "", nil
}

func startWorkStatus(options startWorkOptions) error {
	state, err := readStartWorkState(options.RepoSlug)
	if err != nil {
		return err
	}
	if options.JSON {
		return json.NewEncoder(os.Stdout).Encode(state)
	}
	counts := map[string]int{}
	triageCounts := map[string]int{}
	serviceTaskCounts := map[string]int{}
	serviceTaskByKind := map[string]map[string]int{}
	blockedReasons := map[string]int{}
	for _, issue := range state.Issues {
		counts[issue.Status]++
		triageCounts[issue.TriageStatus]++
		if issue.Status == startWorkStatusBlocked {
			blockedReasons[defaultString(strings.TrimSpace(issue.BlockedReason), "(unspecified)")]++
		}
	}
	for _, task := range state.ServiceTasks {
		serviceTaskCounts[task.Status]++
		if serviceTaskByKind[task.Kind] == nil {
			serviceTaskByKind[task.Kind] = map[string]int{}
		}
		serviceTaskByKind[task.Kind][task.Status]++
	}
	fmt.Fprintf(os.Stdout, "[start] Source: %s\n", state.SourceRepo)
	fmt.Fprintf(os.Stdout, "[start] Fork: %s\n", state.ForkRepo)
	fmt.Fprintf(
		os.Stdout,
		"[start] Issues: queued=%d in_progress=%d reconciling=%d completed=%d blocked=%d promoted=%d not_actioned=%d total=%d\n",
		counts[startWorkStatusQueued],
		counts[startWorkStatusInProgress],
		counts[startWorkStatusReconciling],
		counts[startWorkStatusCompleted],
		counts[startWorkStatusBlocked],
		counts[startWorkStatusPromoted],
		counts[startWorkStatusNotActioned],
		len(state.Issues),
	)
	fmt.Fprintf(os.Stdout, "[start] Triage: queued=%d running=%d completed=%d failed=%d\n", triageCounts[startWorkTriageQueued], triageCounts[startWorkTriageRunning], triageCounts[startWorkTriageCompleted], triageCounts[startWorkTriageFailed])
	fmt.Fprintf(os.Stdout, "[start] Service tasks: queued=%d running=%d completed=%d failed=%d\n", serviceTaskCounts[startWorkServiceTaskQueued], serviceTaskCounts[startWorkServiceTaskRunning], serviceTaskCounts[startWorkServiceTaskCompleted], serviceTaskCounts[startWorkServiceTaskFailed])
	if len(serviceTaskByKind) > 0 {
		kinds := make([]string, 0, len(serviceTaskByKind))
		for kind := range serviceTaskByKind {
			kinds = append(kinds, kind)
		}
		slices.Sort(kinds)
		parts := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			countByStatus := serviceTaskByKind[kind]
			parts = append(parts, fmt.Sprintf("%s(q=%d r=%d c=%d f=%d)", kind, countByStatus[startWorkServiceTaskQueued], countByStatus[startWorkServiceTaskRunning], countByStatus[startWorkServiceTaskCompleted], countByStatus[startWorkServiceTaskFailed]))
		}
		fmt.Fprintf(os.Stdout, "[start] Service task detail: %s\n", strings.Join(parts, " "))
	}
	if len(blockedReasons) > 0 {
		reasons := make([]string, 0, len(blockedReasons))
		for reason, count := range blockedReasons {
			reasons = append(reasons, fmt.Sprintf("%s (%d)", reason, count))
		}
		slices.Sort(reasons)
		fmt.Fprintf(os.Stdout, "[start] Blocked reasons: %s\n", strings.Join(reasons, "; "))
	}
	promoted, reused, activeSkips := startWorkPromotionCounts(state)
	fmt.Fprintf(os.Stdout, "[start] Forwarding: promoted=%d reused=%d active_skips=%d\n", promoted, reused, activeSkips)
	if state.LastRun != nil {
		fmt.Fprintf(
			os.Stdout,
			"[start] Last run: started=%s service_started=%d implementation_started=%d open_prs=%d/%d global_parallel=%d repo_workers=%d skipped=%s\n",
			joinIntsOrNone(state.LastRun.StartedIssueNumbers),
			state.LastRun.ServiceStartedCount,
			state.LastRun.ImplementationStartedCount,
			state.LastRun.OpenForkPRs,
			state.LastRun.OpenPRCap,
			state.LastRun.GlobalParallelLimit,
			defaultInt(state.LastRun.RepoWorkerLimit, state.LastRun.ParallelLimit),
			defaultString(state.LastRun.SkippedReason, "(none)"),
		)
	}
	if len(state.PromotionSkips) > 0 {
		reasons := []string{}
		for _, skipped := range state.PromotionSkips {
			reasons = append(reasons, fmt.Sprintf("fork PR #%d: %s", skipped.ForkPRNumber, skipped.Reason))
		}
		slices.Sort(reasons)
		fmt.Fprintf(os.Stdout, "[start] Forward skips: %s\n", strings.Join(reasons, "; "))
	}
	fmt.Fprintf(os.Stdout, "[start] State: %s\n", startWorkStatePath(options.RepoSlug))
	return nil
}

func defaultGithubAPIBaseURL() string {
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	return apiBaseURL
}

func githubCurrentViewer(apiBaseURL string, token string) (string, error) {
	var viewer struct {
		Login string `json:"login"`
	}
	if err := githubAPIGetJSON(apiBaseURL, token, "/user", &viewer); err != nil {
		return "", err
	}
	if strings.TrimSpace(viewer.Login) == "" {
		return "", fmt.Errorf("GitHub /user response did not include login")
	}
	return strings.TrimSpace(viewer.Login), nil
}

func ensureStartWorkForkReady(forkRepo string, apiBaseURL string, token string) error {
	if strings.TrimSpace(forkRepo) == "" {
		return fmt.Errorf("fork repo is unknown; cannot verify issue and CI settings")
	}
	if err := githubAPIRequestJSON("PATCH", apiBaseURL, token, fmt.Sprintf("/repos/%s", forkRepo), map[string]any{"has_issues": true}, &struct{}{}); err != nil {
		return fmt.Errorf("fork %s does not have Issues enabled and Nana could not enable them: %w. Enable Issues in the fork settings or set repo-mode to local/repo", forkRepo, err)
	}
	if err := githubAPIRequestJSON("PUT", apiBaseURL, token, fmt.Sprintf("/repos/%s/actions/permissions", forkRepo), map[string]any{"enabled": true, "allowed_actions": "all"}, &struct{}{}); err != nil {
		return fmt.Errorf("fork %s does not have GitHub Actions enabled and Nana could not enable them: %w. Enable Actions in the fork settings or set repo-mode to local/repo", forkRepo, err)
	}
	return nil
}

func startWorkFetchRepo(repoSlug string, apiBaseURL string, token string) (githubRepositoryPayload, error) {
	var repo githubRepositoryPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s", repoSlug), &repo); err != nil {
		return githubRepositoryPayload{}, err
	}
	return repo, nil
}

func ensureGithubFork(sourceRepo string, repoName string, viewer string, apiBaseURL string, token string) (githubRepositoryPayload, bool, error) {
	forkSlug := viewer + "/" + repoName
	var fork githubRepositoryPayload
	status, err := githubAPIGetJSONWithStatus(apiBaseURL, token, fmt.Sprintf("/repos/%s", forkSlug), &fork)
	if err == nil {
		return fork, false, nil
	}
	if status != http.StatusNotFound {
		return githubRepositoryPayload{}, false, err
	}
	payload := map[string]any{"default_branch_only": false}
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/forks", sourceRepo), payload, &fork); err != nil {
		return githubRepositoryPayload{}, false, err
	}
	if strings.TrimSpace(fork.FullName) == "" {
		fork.FullName = forkSlug
	}
	if strings.TrimSpace(fork.Name) == "" {
		fork.Name = repoName
	}
	return fork, true, nil
}

func listStartWorkIssues(repoSlug string, apiBaseURL string, token string) ([]startWorkIssuePayload, error) {
	issues := []startWorkIssuePayload{}
	for page := 1; page <= 20; page++ {
		var batch []startWorkIssuePayload
		path := fmt.Sprintf("/repos/%s/issues?state=all&per_page=100&page=%d", repoSlug, page)
		if err := githubAPIGetJSON(apiBaseURL, token, path, &batch); err != nil {
			return nil, err
		}
		issues = append(issues, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return issues, nil
}

func createStartWorkIssue(forkRepo string, issue startWorkIssuePayload, labels []string, apiBaseURL string, token string) (startWorkIssuePayload, error) {
	body := strings.TrimSpace(issue.Body)
	if body != "" {
		body += "\n\n"
	}
	body += fmt.Sprintf("Copied from %s", issue.HTMLURL)
	payload := map[string]any{"title": issue.Title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	var created startWorkIssuePayload
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/issues", forkRepo), payload, &created); err != nil {
		return startWorkIssuePayload{}, err
	}
	return created, nil
}

func closeStartWorkIssue(forkRepo string, issueNumber int, apiBaseURL string, token string) error {
	payload := map[string]any{"state": "closed"}
	return githubAPIRequestJSON("PATCH", apiBaseURL, token, fmt.Sprintf("/repos/%s/issues/%d", forkRepo, issueNumber), payload, &struct{}{})
}

func listStartWorkPulls(repoSlug string, state string, apiBaseURL string, token string) ([]startWorkPullPayload, error) {
	pulls := []startWorkPullPayload{}
	for page := 1; page <= 20; page++ {
		var batch []startWorkPullPayload
		path := fmt.Sprintf("/repos/%s/pulls?state=%s&per_page=100&page=%d", repoSlug, url.QueryEscape(state), page)
		if err := githubAPIGetJSON(apiBaseURL, token, path, &batch); err != nil {
			return nil, err
		}
		pulls = append(pulls, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return pulls, nil
}

func createStartWorkUpstreamPR(state startWorkState, forkPR startWorkPullPayload, apiBaseURL string, token string) (startWorkPullPayload, error) {
	created, _, err := ensureStartWorkUpstreamPR(state, forkPR, apiBaseURL, token)
	return created, err
}

func ensureStartWorkUpstreamPR(state startWorkState, forkPR startWorkPullPayload, apiBaseURL string, token string) (startWorkPullPayload, bool, error) {
	forkOwner := state.ForkOwner
	if forkOwner == "" && strings.Contains(state.ForkRepo, "/") {
		forkOwner = strings.SplitN(state.ForkRepo, "/", 2)[0]
	}
	base := defaultString(forkPR.Base.Ref, state.DefaultBranch)
	head := fmt.Sprintf("%s:%s", forkOwner, forkPR.Head.Ref)
	existing, err := listStartWorkUpstreamPRs(state.SourceRepo, head, base, apiBaseURL, token)
	if err != nil {
		return startWorkPullPayload{}, false, err
	}
	if len(existing) > 0 {
		return existing[0], true, nil
	}
	payload := map[string]any{
		"title": forkPR.Title,
		"head":  head,
		"base":  base,
		"body":  fmt.Sprintf("Promoted from fork PR %s", forkPR.HTMLURL),
	}
	var created startWorkPullPayload
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls", state.SourceRepo), payload, &created); err != nil {
		return startWorkPullPayload{}, false, err
	}
	return created, false, nil
}

func listStartWorkUpstreamPRs(repoSlug string, head string, base string, apiBaseURL string, token string) ([]startWorkPullPayload, error) {
	pulls := []startWorkPullPayload{}
	path := fmt.Sprintf("/repos/%s/pulls?state=open&head=%s&base=%s", repoSlug, url.QueryEscape(head), url.QueryEscape(base))
	if err := githubAPIGetJSON(apiBaseURL, token, path, &pulls); err != nil {
		return nil, err
	}
	return pulls, nil
}

func startWorkIssueLabelNames(labels []githubLabel) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if name := strings.TrimSpace(label.Name); name != "" {
			out = append(out, name)
		}
	}
	return uniqueStrings(out)
}

func startWorkIssueFingerprint(issue startWorkIssuePayload, labels []string) string {
	return sha256Hex(strings.Join([]string{
		strconv.Itoa(issue.Number),
		strings.TrimSpace(issue.Title),
		strings.TrimSpace(issue.Body),
		strings.TrimSpace(issue.State),
		strings.Join(labels, ","),
	}, "\n"))
}

func startWorkResolvePriority(existing startWorkIssueState, issue startWorkIssuePayload, labels []string, sourceFingerprint string) (int, string, string, string, string, string, string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if priority, ok := startWorkManualPriority(labels); ok {
		label := startWorkPriorityLabel(priority)
		return priority, "manual_label", startWorkTriageCompleted, "manual priority label " + label, sourceFingerprint, now, ""
	}
	priority := existing.Priority
	if priority <= 0 || priority > 5 {
		priority = 5
	}
	prioritySource := existing.PrioritySource
	triageStatus := existing.TriageStatus
	triageRationale := existing.TriageRationale
	triageFingerprint := existing.TriageFingerprint
	triageUpdatedAt := existing.TriageUpdatedAt
	triageError := existing.TriageError
	if triageStatus == startWorkTriageRunning {
		triageStatus = startWorkTriageQueued
	}
	if strings.TrimSpace(triageFingerprint) != sourceFingerprint {
		triageStatus = startWorkTriageQueued
		triageError = ""
		if prioritySource == "" {
			prioritySource = "triage"
		}
	}
	if strings.TrimSpace(triageStatus) == "" {
		triageStatus = startWorkTriageQueued
	}
	return priority, prioritySource, triageStatus, triageRationale, triageFingerprint, triageUpdatedAt, triageError
}

func startWorkManualPriority(labels []string) (int, bool) {
	best := 6
	for _, label := range labels {
		upper := strings.ToUpper(strings.TrimSpace(label))
		if len(upper) == 2 && upper[0] == 'P' && upper[1] >= '0' && upper[1] <= '5' {
			parsed := int(upper[1] - '0')
			if parsed < best {
				best = parsed
			}
		}
	}
	if best == 6 {
		return 0, false
	}
	return best, true
}

func startWorkPriorityLabel(priority int) string {
	if priority < 0 || priority > 5 {
		return "P5"
	}
	return fmt.Sprintf("P%d", priority)
}

func startWorkPriority(labels []string) int {
	if priority, ok := startWorkManualPriority(labels); ok && priority > 0 {
		return priority
	}
	return 5
}

func startWorkComplexity(labels []string) int {
	complexity := 3
	for _, label := range labels {
		lower := strings.ToLower(strings.TrimSpace(label))
		switch lower {
		case "xs", "trivial":
			complexity = min(complexity, 1)
		case "s", "small", "easy", "good first issue":
			complexity = min(complexity, 2)
		case "m", "medium":
			complexity = min(complexity, 3)
		case "l", "large", "hard", "complex":
			complexity = max(complexity, 4)
		case "xl", "huge", "epic":
			complexity = max(complexity, 5)
		}
	}
	return complexity
}

func startWorkImplementationInProgress(state *startWorkState) int {
	if state == nil {
		return 0
	}
	count := 0
	for _, issue := range state.Issues {
		if issue.Status == startWorkStatusInProgress {
			count++
		}
	}
	return count
}

func startWorkIssueReadyForImplementation(issue startWorkIssueState, options startWorkOptions) bool {
	if issue.Status != startWorkStatusQueued || issue.State != "open" || issue.ForkNumber <= 0 {
		return false
	}
	if !startWorkAutomationAllowsIssue(options.ImplementMode, issue.Labels, "implement") {
		return false
	}
	if !startWorkIssueHasFreshPriority(issue) {
		return false
	}
	return true
}

func startWorkIssueHasFreshPriority(issue startWorkIssueState) bool {
	if strings.TrimSpace(issue.PrioritySource) == "manual_label" {
		return true
	}
	if issue.TriageStatus != startWorkTriageCompleted {
		return false
	}
	if strings.TrimSpace(issue.SourceFingerprint) == "" {
		return false
	}
	return strings.TrimSpace(issue.TriageFingerprint) == strings.TrimSpace(issue.SourceFingerprint)
}

func startWorkBuildImplementationQueue(state *startWorkState, options startWorkOptions, openForkPRs int) ([]startWorkIssueState, string) {
	if options.ImplementMode == "manual" {
		return nil, "issue-pick mode is manual"
	}
	if openForkPRs >= options.MaxOpenPR {
		return nil, fmt.Sprintf("open fork PR cap reached (%d/%d)", openForkPRs, options.MaxOpenPR)
	}
	queue := []startWorkIssueState{}
	for _, issue := range state.Issues {
		if startWorkIssueReadyForImplementation(issue, options) {
			queue = append(queue, issue)
		}
	}
	if len(queue) == 0 {
		return nil, "no queued fork issues"
	}
	slices.SortFunc(queue, func(a, b startWorkIssueState) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		if a.Complexity != b.Complexity {
			return a.Complexity - b.Complexity
		}
		return a.SourceNumber - b.SourceNumber
	})
	return queue, ""
}

func refreshStartWorkPreferences(state *startWorkState) {
	now := time.Now().UTC().Format(time.RFC3339)
	artifacts := map[string]string{
		"global_work_policy": githubGlobalWorkPolicyPath(),
	}
	if state.SourceRepo != "" {
		artifacts["source_settings"] = githubRepoSettingsPath(state.SourceRepo)
		artifacts["source_review_rules"] = filepath.Join(githubNanaHome(), "repos", filepath.FromSlash(state.SourceRepo), "source", ".nana", "repo-review-rules.json")
		artifacts["source_repo_profile"] = githubRepoProfilePath(state.SourceRepo)
	}
	if state.ForkRepo != "" {
		artifacts["fork_settings"] = githubRepoSettingsPath(state.ForkRepo)
		artifacts["fork_review_rules"] = filepath.Join(githubNanaHome(), "repos", filepath.FromSlash(state.ForkRepo), "source", ".nana", "repo-review-rules.json")
		artifacts["fork_repo_profile"] = githubRepoProfilePath(state.ForkRepo)
	}
	state.Preferences = startWorkPreferences{UpdatedAt: now, Artifacts: artifacts, Source: startWorkRepoPrefsFor(state.SourceRepo), Fork: startWorkRepoPrefsFor(state.ForkRepo)}
}

func startWorkAutomationAllowsIssue(mode string, labels []string, action string) bool {
	switch defaultString(normalizeGithubAutomationMode(mode), "manual") {
	case "auto":
		return true
	case "labeled":
		return startWorkHasAutomationLabel(labels, action)
	default:
		return false
	}
}

func startWorkHasAutomationLabel(labels []string, action string) bool {
	for _, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if normalized == "nana" || normalized == improvementScoutRole || normalized == enhancementScoutRole {
			return true
		}
	}
	return false
}

func startWorkRepoPrefsFor(repoSlug string) *startWorkRepoPrefs {
	if strings.TrimSpace(repoSlug) == "" {
		return nil
	}
	settings, err := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	if err != nil || settings == nil {
		return nil
	}
	return &startWorkRepoPrefs{DefaultConsiderations: append([]string{}, settings.DefaultConsiderations...), DefaultRoleLayout: settings.DefaultRoleLayout, ReviewRulesMode: settings.ReviewRulesMode}
}

func startWorkStatePath(repoSlug string) string {
	return filepath.Join(githubNanaHome(), "start", filepath.FromSlash(repoSlug), "state.json")
}

func readStartWorkState(repoSlug string) (*startWorkState, error) {
	var state startWorkState
	if err := readGithubJSON(startWorkStatePath(repoSlug), &state); err != nil {
		return nil, err
	}
	if state.Issues == nil {
		state.Issues = map[string]startWorkIssueState{}
	}
	if state.ServiceTasks == nil {
		state.ServiceTasks = map[string]startWorkServiceTask{}
	}
	if state.Promotions == nil {
		state.Promotions = map[string]startWorkPromotion{}
	}
	if state.PromotionSkips == nil {
		state.PromotionSkips = map[string]startWorkPromotionSkip{}
	}
	if state.Version < startWorkStateVersion {
		state.Version = startWorkStateVersion
	}
	for key, task := range state.ServiceTasks {
		if task.Status == startWorkServiceTaskRunning {
			task.Status = startWorkServiceTaskQueued
			task.StartedAt = ""
			state.ServiceTasks[key] = task
		}
	}
	for key, issue := range state.Issues {
		if issue.Priority < 0 || issue.Priority > 5 {
			issue.Priority = 5
		}
		if issue.TriageStatus == startWorkTriageRunning {
			issue.TriageStatus = startWorkTriageQueued
		}
		if issue.PrioritySource == "" {
			if priority, ok := startWorkManualPriority(issue.Labels); ok {
				issue.Priority = priority
				issue.PrioritySource = "manual_label"
				issue.TriageStatus = startWorkTriageCompleted
			} else if issue.TriageStatus == "" {
				issue.TriageStatus = startWorkTriageQueued
			}
		}
		if issue.Status == "" {
			issue.Status = startWorkStatusQueued
		}
		if issue.Status == startWorkStatusInProgress && strings.TrimSpace(issue.LastRunID) == "" && strings.TrimSpace(issue.LastRunError) != "" {
			issue.Status = startWorkStatusReconciling
		}
		state.Issues[key] = issue
	}
	return &state, nil
}

func writeStartWorkState(state startWorkState) error {
	return writeGithubJSON(startWorkStatePath(state.SourceRepo), state)
}

func githubAPIGetJSONWithStatus(apiBaseURL string, token string, path string, target interface{}) (int, error) {
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(apiBaseURL, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, fmt.Errorf("GitHub API request failed (%d %s)%s", response.StatusCode, response.Status, renderGithubDetail(body))
	}
	return response.StatusCode, json.NewDecoder(response.Body).Decode(target)
}

func joinIntsOrNone(values []int) string {
	if len(values) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ", ")
}

func defaultInt(value int, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}
