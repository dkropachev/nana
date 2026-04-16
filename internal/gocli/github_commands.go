package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type githubCommandResult struct {
	LegacyArgs []string
	Handled    bool
	RunID      string
}

type githubWorkManifest struct {
	Version                 int                        `json:"version,omitempty"`
	RunID                   string                     `json:"run_id"`
	CreatedAt               string                     `json:"created_at,omitempty"`
	RepoSlug                string                     `json:"repo_slug"`
	RepoOwner               string                     `json:"repo_owner"`
	RepoName                string                     `json:"repo_name"`
	ManagedRepoRoot         string                     `json:"managed_repo_root,omitempty"`
	SourcePath              string                     `json:"source_path,omitempty"`
	TargetURL               string                     `json:"target_url"`
	TargetKind              string                     `json:"target_kind"`
	UpdatedAt               string                     `json:"updated_at"`
	PublishedPRNumber       int                        `json:"published_pr_number"`
	PublishedPRURL          string                     `json:"published_pr_url,omitempty"`
	PublishedPRHeadRef      string                     `json:"published_pr_head_ref,omitempty"`
	PublicationState        string                     `json:"publication_state,omitempty"`
	PublicationDetail       string                     `json:"publication_detail,omitempty"`
	PublicationError        string                     `json:"publication_error,omitempty"`
	PublicationUpdatedAt    string                     `json:"publication_updated_at,omitempty"`
	SandboxID               string                     `json:"sandbox_id"`
	SandboxPath             string                     `json:"sandbox_path"`
	SandboxRepoPath         string                     `json:"sandbox_repo_path"`
	VerificationPlan        *githubVerificationPlan    `json:"verification_plan,omitempty"`
	VerificationScriptsDir  string                     `json:"verification_scripts_dir,omitempty"`
	CreatePROnComplete      bool                       `json:"create_pr_on_complete,omitempty"`
	RepoMode                string                     `json:"repo_mode,omitempty"`
	PRForwardMode           string                     `json:"pr_forward_mode,omitempty"`
	PublishTarget           string                     `json:"publish_target,omitempty"`
	PublishRepoSlug         string                     `json:"publish_repo_slug,omitempty"`
	PublishRepoOwner        string                     `json:"publish_repo_owner,omitempty"`
	ConsiderationPipeline   []githubPipelineLane       `json:"consideration_pipeline,omitempty"`
	LanePromptArtifacts     []githubLanePromptArtifact `json:"lane_prompt_artifacts,omitempty"`
	ConsiderationsActive    []string                   `json:"considerations_active,omitempty"`
	RoleLayout              string                     `json:"role_layout,omitempty"`
	TargetNumber            int                        `json:"target_number,omitempty"`
	TargetTitle             string                     `json:"target_title,omitempty"`
	TargetState             string                     `json:"target_state,omitempty"`
	TargetAuthor            string                     `json:"target_author,omitempty"`
	ReviewReviewer          string                     `json:"review_reviewer,omitempty"`
	EffectiveReviewerPolicy *githubReviewerPolicy      `json:"effective_reviewer_policy,omitempty"`
	APIBaseURL              string                     `json:"api_base_url,omitempty"`
	DefaultBranch           string                     `json:"default_branch,omitempty"`
	LastSeenIssueCommentID  int                        `json:"last_seen_issue_comment_id,omitempty"`
	LastSeenReviewID        int                        `json:"last_seen_review_id,omitempty"`
	LastSeenReviewCommentID int                        `json:"last_seen_review_comment_id,omitempty"`
	Policy                  *githubResolvedWorkPolicy  `json:"policy,omitempty"`
	RepoProfilePath         string                     `json:"repo_profile_path,omitempty"`
	RepoProfileFingerprint  string                     `json:"repo_profile_fingerprint,omitempty"`
	RepoProfile             *githubRepoProfile         `json:"repo_profile,omitempty"`
	ControlPlaneReviewers   []string                   `json:"control_plane_reviewers,omitempty"`
	IgnoredFeedbackActors   map[string]int             `json:"ignored_feedback_actors,omitempty"`
	RequestedReviewers      []string                   `json:"requested_reviewers,omitempty"`
	ReviewRequestState      string                     `json:"review_request_state,omitempty"`
	ReviewRequestError      string                     `json:"review_request_error,omitempty"`
	ReviewRequestUpdatedAt  string                     `json:"review_request_updated_at,omitempty"`
	MergeState              string                     `json:"merge_state,omitempty"`
	MergeError              string                     `json:"merge_error,omitempty"`
	MergeUpdatedAt          string                     `json:"merge_updated_at,omitempty"`
	MergedPRNumber          int                        `json:"merged_pr_number,omitempty"`
	MergedSHA               string                     `json:"merged_sha,omitempty"`
	MergeMethod             string                     `json:"merge_method,omitempty"`
	NeedsHuman              bool                       `json:"needs_human,omitempty"`
	NeedsHumanReason        string                     `json:"needs_human_reason,omitempty"`
	NextAction              string                     `json:"next_action,omitempty"`
}

type githubPipelineLane struct {
	Alias       string   `json:"alias"`
	Role        string   `json:"role"`
	PromptRoles []string `json:"prompt_roles,omitempty"`
	Activation  string   `json:"activation,omitempty"`
	Phase       string   `json:"phase,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Blocking    bool     `json:"blocking,omitempty"`
	Purpose     string   `json:"purpose,omitempty"`
}

type githubLanePromptArtifact struct {
	Alias       string   `json:"alias"`
	Role        string   `json:"role"`
	PromptPath  string   `json:"prompt_path"`
	PromptRoles []string `json:"prompt_roles,omitempty"`
}

type githubPullReviewFinding struct {
	Fingerprint     string `json:"fingerprint"`
	Title           string `json:"title"`
	Path            string `json:"path"`
	Line            int    `json:"line,omitempty"`
	Severity        string `json:"severity,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Detail          string `json:"detail"`
	Fix             string `json:"fix,omitempty"`
	Rationale       string `json:"rationale,omitempty"`
	UserExplanation string `json:"user_explanation,omitempty"`
	ChangedInPR     bool   `json:"changed_in_pr,omitempty"`
	ChangedLineInPR bool   `json:"changed_line_in_pr,omitempty"`
	PRPermalink     string `json:"pr_permalink,omitempty"`
	MainPermalink   string `json:"main_permalink,omitempty"`
}

type githubPullStatePayload struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

type githubWorkStartOptions struct {
	Target                  parsedGithubTarget
	Reviewer                string
	RequestedConsiderations []string
	RoleLayout              string
	NewPR                   bool
	CreatePR                bool
	CreatePRExplicit        bool
	RepoMode                string
	PublishTarget           string
	CodexArgs               []string
}

type githubWorkSyncOptions struct {
	RunID             string
	UseLast           bool
	Reviewer          string
	ResumeLast        bool
	FeedbackTargetURL string
	CodexArgs         []string
}

func GithubWorkCommand(cwd string, args []string) (githubCommandResult, error) {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, GithubWorkHelp)
		return githubCommandResult{Handled: true}, nil
	}

	switch args[0] {
	case "defaults", "stats", "retrospective", "explain":
		if err := GithubWork(cwd, args); err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true}, nil
	case "start":
		options, err := parseGithubWorkStartArgs(args)
		if err != nil {
			return githubCommandResult{}, err
		}
		run, err := startGithubWork(options)
		if err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true, RunID: run.RunID}, nil
	case "sync":
		options, err := parseGithubWorkSyncArgs(args)
		if err != nil {
			return githubCommandResult{}, err
		}
		if err := syncGithubWork(options); err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true}, nil
	case "verify-refresh":
		selection, err := normalizeGithubWorkRunSelectionArgs(args, true)
		if err != nil {
			return githubCommandResult{}, err
		}
		useLast := selection == "<last>"
		runID := ""
		if !useLast {
			runID = selection
		}
		if err := refreshGithubVerificationArtifacts(runID, useLast); err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true}, nil
	case "lane-exec":
		runSelection, laneAlias, task, codexArgs, err := parseGithubWorkLaneExecArgs(args)
		if err != nil {
			return githubCommandResult{}, err
		}
		useLast := runSelection == "<last>"
		runID := ""
		if !useLast {
			runID = runSelection
		}
		if laneAlias == "publisher" {
			if err := executeGithubPublisherLane(runID, useLast); err != nil {
				return githubCommandResult{}, err
			}
			return githubCommandResult{Handled: true}, nil
		}
		if err := executeGithubLane(runID, useLast, laneAlias, task, codexArgs); err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true}, nil
	default:
		return githubCommandResult{}, fmt.Errorf("Unknown work subcommand: %s\n\n%s", args[0], GithubWorkHelp)
	}
}

func GithubIssue(cwd string, args []string) (githubCommandResult, error) {
	command := ""
	rest := []string{}
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, IssueHelp)
		return githubCommandResult{Handled: true}, nil
	}
	if args[0] == "issue" {
		if len(args) == 1 || isHelpToken(args[1]) {
			fmt.Fprint(os.Stdout, IssueHelp)
			return githubCommandResult{Handled: true}, nil
		}
		command = args[1]
		rest = append([]string{}, args[2:]...)
	} else {
		command = args[0]
		rest = append([]string{}, args[1:]...)
	}
	if len(rest) > 0 && isHelpToken(rest[0]) {
		fmt.Fprint(os.Stdout, IssueHelp)
		return githubCommandResult{Handled: true}, nil
	}

	switch command {
	case "implement":
		if len(rest) == 0 {
			return githubCommandResult{}, fmt.Errorf("Usage: nana issue implement <github-issue-url> [work start flags...]")
		}
		target, err := parseGithubTargetURL(rest[0])
		if err != nil {
			return githubCommandResult{}, err
		}
		if target.kind != "issue" {
			return githubCommandResult{}, fmt.Errorf("nana issue implement expects a GitHub issue URL.\n%s", IssueHelp)
		}
		options, err := parseGithubWorkStartArgs(append([]string{"start"}, rest...))
		if err != nil {
			return githubCommandResult{}, err
		}
		run, err := startGithubWork(options)
		if err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true, RunID: run.RunID}, nil
	case "investigate":
		if len(rest) == 0 {
			return githubCommandResult{}, fmt.Errorf("Usage: nana issue investigate <github-issue-url> [work start flags...]")
		}
		target, err := parseGithubTargetURL(rest[0])
		if err != nil {
			return githubCommandResult{}, err
		}
		if target.kind != "issue" {
			return githubCommandResult{}, fmt.Errorf("nana issue investigate expects a GitHub issue URL.\n%s", IssueHelp)
		}
		if err := githubInvestigateTarget(rest[0]); err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true}, nil
	case "sync":
		syncArgs, err := normalizeGithubIssueSyncArgs(rest)
		if err != nil {
			return githubCommandResult{}, err
		}
		options, err := parseGithubWorkSyncArgs(syncArgs)
		if err != nil {
			return githubCommandResult{}, err
		}
		if err := syncGithubWork(options); err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true}, nil
	default:
		if args[0] == "issue" {
			return githubCommandResult{}, fmt.Errorf("Unknown issue subcommand: %s", command)
		}
		return githubCommandResult{}, fmt.Errorf("nana: unknown command: %s", command)
	}
}

func parseGithubWorkStartArgs(args []string) (githubWorkStartOptions, error) {
	if len(args) < 2 {
		return githubWorkStartOptions{}, fmt.Errorf("Usage: nana work start <github-issue-or-pr-url>\n\n%s", GithubWorkHelp)
	}
	reviewer := "@me"
	requestedConsiderations := []string{}
	roleLayout := ""
	newPR := false
	createPR := false
	createPRExplicit := false
	repoMode := ""
	publishTarget := ""
	codexArgs := []string{}
	targetIndex := 1
	if strings.HasPrefix(args[targetIndex], "-") {
		targetIndex = -1
	}
	repoSlug := ""
	issueNumber := 0
	prNumber := 0
	for index := 1; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			codexArgs = append(codexArgs, args[index+1:]...)
			break
		}
		if isHelpToken(token) {
			return githubWorkStartOptions{}, fmt.Errorf("Usage: nana work start <github-issue-or-pr-url>\n\n%s", GithubWorkHelp)
		}
		switch {
		case token == "--reviewer":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			reviewer = strings.TrimSpace(value)
			if reviewer == "" {
				return githubWorkStartOptions{}, fmt.Errorf("Missing value after --reviewer.\n%s", GithubWorkHelp)
			}
			index++
		case strings.HasPrefix(token, "--reviewer="):
			reviewer = strings.TrimSpace(strings.TrimPrefix(token, "--reviewer="))
			if reviewer == "" {
				return githubWorkStartOptions{}, fmt.Errorf("Missing value after --reviewer.\n%s", GithubWorkHelp)
			}
		case token == "--mode" || strings.HasPrefix(token, "--mode="):
			return githubWorkStartOptions{}, fmt.Errorf("`--mode` has been removed. Use `--considerations` only.")
		case token == "--considerations":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			parsed, err := parseGithubConsiderations(value, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			requestedConsiderations = parsed
			index++
		case strings.HasPrefix(token, "--considerations="):
			parsed, err := parseGithubConsiderations(strings.TrimPrefix(token, "--considerations="), "--considerations")
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			requestedConsiderations = parsed
		case token == "--role-layout":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			parsed, err := parseGithubRoleLayout(value, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			roleLayout = parsed
			index++
		case strings.HasPrefix(token, "--role-layout="):
			parsed, err := parseGithubRoleLayout(strings.TrimPrefix(token, "--role-layout="), "--role-layout")
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			roleLayout = parsed
		case token == "--new-pr":
			newPR = true
		case token == "--create-pr":
			createPR = true
			createPRExplicit = true
		case token == "--local-only":
			createPR = false
			createPRExplicit = true
			publishTarget = "local-branch"
		case token == "--repo-mode":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			parsed, err := parseGithubRepoMode(value, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			repoMode = parsed
			publishTarget = repoModeToPublishTarget(parsed)
			createPR = parsed != "local"
			createPRExplicit = true
			index++
		case strings.HasPrefix(token, "--repo-mode="):
			parsed, err := parseGithubRepoMode(strings.TrimPrefix(token, "--repo-mode="), "--repo-mode")
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			repoMode = parsed
			publishTarget = repoModeToPublishTarget(parsed)
			createPR = parsed != "local"
			createPRExplicit = true
		case token == "--publish":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			parsed, err := parseGithubPublishTarget(value, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			publishTarget = parsed
			repoMode = publishTargetToRepoMode(parsed)
			createPR = publishTarget != "local-branch"
			createPRExplicit = true
			index++
		case strings.HasPrefix(token, "--publish="):
			parsed, err := parseGithubPublishTarget(strings.TrimPrefix(token, "--publish="), "--publish")
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			publishTarget = parsed
			repoMode = publishTargetToRepoMode(parsed)
			createPR = publishTarget != "local-branch"
			createPRExplicit = true
		case token == "--repo":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			repoSlug = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--repo="):
			repoSlug = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
		case token == "--issue":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
			if parseErr != nil || parsed <= 0 {
				return githubWorkStartOptions{}, fmt.Errorf("Invalid --issue value: %s.\n%s", value, GithubWorkHelp)
			}
			issueNumber = parsed
			index++
		case strings.HasPrefix(token, "--issue="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--issue="))
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed <= 0 {
				return githubWorkStartOptions{}, fmt.Errorf("Invalid --issue value: %s.\n%s", value, GithubWorkHelp)
			}
			issueNumber = parsed
		case token == "--pr":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkStartOptions{}, err
			}
			parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
			if parseErr != nil || parsed <= 0 {
				return githubWorkStartOptions{}, fmt.Errorf("Invalid --pr value: %s.\n%s", value, GithubWorkHelp)
			}
			prNumber = parsed
			index++
		case strings.HasPrefix(token, "--pr="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--pr="))
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed <= 0 {
				return githubWorkStartOptions{}, fmt.Errorf("Invalid --pr value: %s.\n%s", value, GithubWorkHelp)
			}
			prNumber = parsed
		}
	}
	var target parsedGithubTarget
	var err error
	if targetIndex >= 0 {
		target, err = parseGithubTargetURL(args[targetIndex])
		if err != nil {
			return githubWorkStartOptions{}, err
		}
	} else {
		if !validRepoSlug(repoSlug) || ((issueNumber > 0) == (prNumber > 0)) {
			return githubWorkStartOptions{}, fmt.Errorf("Usage: nana work start <github-issue-or-pr-url>\n\n%s", GithubWorkHelp)
		}
		resource := "issues"
		number := issueNumber
		if prNumber > 0 {
			resource = "pull"
			number = prNumber
		}
		target, err = parseGithubTargetURL(fmt.Sprintf("https://github.com/%s/%s/%d", repoSlug, resource, number))
		if err != nil {
			return githubWorkStartOptions{}, err
		}
	}
	return githubWorkStartOptions{
		Target:                  target,
		Reviewer:                reviewer,
		RequestedConsiderations: requestedConsiderations,
		RoleLayout:              roleLayout,
		NewPR:                   newPR,
		CreatePR:                createPR,
		CreatePRExplicit:        createPRExplicit,
		RepoMode:                repoMode,
		PublishTarget:           publishTarget,
		CodexArgs:               codexArgs,
	}, nil
}

func parseGithubWorkSyncArgs(args []string) (githubWorkSyncOptions, error) {
	if len(args) < 1 || args[0] != "sync" {
		return githubWorkSyncOptions{}, fmt.Errorf("Usage: nana work sync [--run-id <id> | --last] [--reviewer <login|@me>] [--resume-last] [codex-args...]\n\n%s", GithubWorkHelp)
	}
	runSelection, err := normalizeGithubWorkRunSelectionArgs(args, false)
	if err != nil {
		return githubWorkSyncOptions{}, err
	}
	useLast := runSelection == "<last>"
	runID := ""
	if !useLast {
		runID = runSelection
	}
	reviewer := ""
	resumeLast := false
	feedbackTargetURL := ""
	codexArgs := []string{}
	for index := 1; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			codexArgs = append(codexArgs, args[index+1:]...)
			break
		}
		switch {
		case token == "--reviewer":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubWorkSyncOptions{}, err
			}
			reviewer = strings.TrimSpace(value)
			if reviewer == "" {
				return githubWorkSyncOptions{}, fmt.Errorf("Missing value after --reviewer.\n%s", GithubWorkHelp)
			}
			index++
		case strings.HasPrefix(token, "--reviewer="):
			reviewer = strings.TrimSpace(strings.TrimPrefix(token, "--reviewer="))
			if reviewer == "" {
				return githubWorkSyncOptions{}, fmt.Errorf("Missing value after --reviewer.\n%s", GithubWorkHelp)
			}
		case token == "--resume-last":
			resumeLast = true
		case strings.HasPrefix(token, "https://github.com/"):
			target, err := parseGithubTargetURL(token)
			if err != nil {
				return githubWorkSyncOptions{}, err
			}
			feedbackTargetURL = githubCanonicalTargetURL(target)
		}
	}
	return githubWorkSyncOptions{
		RunID:             runID,
		UseLast:           useLast,
		Reviewer:          reviewer,
		ResumeLast:        resumeLast,
		FeedbackTargetURL: feedbackTargetURL,
		CodexArgs:         codexArgs,
	}, nil
}

func normalizeGithubWorkRunSelectionArgs(args []string, requireRunSelection bool) (string, error) {
	runID := ""
	useLast := false
	for index := 1; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			break
		}
		if isHelpToken(token) {
			fmt.Fprint(os.Stdout, GithubWorkHelp)
			return "", nil
		}
		switch {
		case token == "--run-id":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return "", err
			}
			runID = strings.TrimSpace(value)
			if runID == "" {
				return "", fmt.Errorf("Missing value after --run-id.\n%s", GithubWorkHelp)
			}
			index++
		case strings.HasPrefix(token, "--run-id="):
			runID = strings.TrimSpace(strings.TrimPrefix(token, "--run-id="))
			if runID == "" {
				return "", fmt.Errorf("Missing value after --run-id.\n%s", GithubWorkHelp)
			}
		case token == "--last":
			useLast = true
		case token == "--reviewer":
			if _, err := requireFlagValue(args, index, token); err != nil {
				return "", err
			}
			index++
		case strings.HasPrefix(token, "--reviewer="):
		case token == "--resume-last":
		case strings.HasPrefix(token, "https://github.com/"):
			if _, err := parseGithubTargetURL(token); err != nil {
				return "", err
			}
		}
	}
	if runID != "" && useLast {
		return "", fmt.Errorf("Use either --run-id <id> or --last, not both.\n%s", GithubWorkHelp)
	}
	if requireRunSelection && runID == "" && !useLast {
		return "", fmt.Errorf("Usage: nana work verify-refresh [--run-id <id> | --last]\n\n%s", GithubWorkHelp)
	}
	if runID != "" {
		return runID, nil
	}
	if useLast {
		return "<last>", nil
	}
	return "", nil
}

func parseGithubWorkLaneExecArgs(args []string) (string, string, string, []string, error) {
	if len(args) < 2 {
		return "", "", "", nil, fmt.Errorf("Usage: nana work lane-exec --run-id <id>|--last --lane <alias> [--task <text>] [-- codex-args...]\n\n%s", GithubWorkHelp)
	}
	laneAlias := ""
	task := ""
	codexArgs := []string{}
	runSelection, err := normalizeGithubWorkRunSelectionArgs(args, true)
	if err != nil {
		return "", "", "", nil, err
	}
	for index := 1; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			codexArgs = append(codexArgs, args[index+1:]...)
			break
		}
		switch {
		case token == "--lane":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return "", "", "", nil, err
			}
			laneAlias = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--lane="):
			laneAlias = strings.TrimSpace(strings.TrimPrefix(token, "--lane="))
		case token == "--task":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return "", "", "", nil, err
			}
			task = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--task="):
			task = strings.TrimSpace(strings.TrimPrefix(token, "--task="))
		}
	}
	if laneAlias == "" {
		return "", "", "", nil, fmt.Errorf("Usage: nana work lane-exec --run-id <id>|--last --lane <alias> [--task <text>] [-- codex-args...]\n\n%s", GithubWorkHelp)
	}
	return runSelection, laneAlias, task, codexArgs, nil
}

func normalizeGithubIssueSyncArgs(args []string) ([]string, error) {
	if len(args) > 0 && strings.HasPrefix(strings.TrimSpace(args[0]), "https://github.com/") {
		runID, err := ResolveGithubRunIDForTargetURL(args[0])
		if err != nil {
			return nil, err
		}
		if runID == "" {
			return nil, fmt.Errorf("No managed NANA run found for %s", args[0])
		}
		return append([]string{"sync", "--run-id", runID, args[0]}, args[1:]...), nil
	}
	return append([]string{"sync"}, args...), nil
}

func GithubReview(cwd string, args []string) (githubCommandResult, error) {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, GithubReviewHelp)
		return githubCommandResult{Handled: true}, nil
	}
	if args[0] != "followup" {
		options, err := parseGithubReviewExecutionArgs(args)
		if err != nil {
			return githubCommandResult{}, err
		}
		if err := reviewGithubPullRequest(options); err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{Handled: true}, nil
	}
	target, allowOpen, err := parseGithubReviewFollowupArgs(args[1:])
	if err != nil {
		return githubCommandResult{}, err
	}
	if err := githubReviewFollowup(target, allowOpen); err != nil {
		return githubCommandResult{}, err
	}
	return githubCommandResult{Handled: true}, nil
}

type githubReviewExecutionOptions struct {
	Target         parsedGithubTarget
	Mode           string
	Reviewer       string
	PerItemContext string
}

func parseGithubReviewExecutionArgs(args []string) (githubReviewExecutionOptions, error) {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, GithubReviewHelp)
		return githubReviewExecutionOptions{}, nil
	}
	target, err := parseGithubTargetURL(args[0])
	if err != nil {
		return githubReviewExecutionOptions{}, err
	}
	if target.kind != "pr" {
		return githubReviewExecutionOptions{}, fmt.Errorf("nana review expects a pull request URL.\n%s", GithubReviewHelp)
	}
	mode := "automatic"
	reviewer := "@me"
	perItemContext := "shared"
	for index := 1; index < len(args); index++ {
		token := args[index]
		switch {
		case isHelpToken(token):
			fmt.Fprint(os.Stdout, GithubReviewHelp)
			return githubReviewExecutionOptions{}, nil
		case token == "--mode":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubReviewExecutionOptions{}, fmt.Errorf("Missing value after --mode.\n%s", GithubReviewHelp)
			}
			if value != "automatic" && value != "manual" {
				return githubReviewExecutionOptions{}, fmt.Errorf("Invalid --mode value: %s.\n%s", value, GithubReviewHelp)
			}
			mode = value
			index++
		case strings.HasPrefix(token, "--mode="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--mode="))
			if value != "automatic" && value != "manual" {
				return githubReviewExecutionOptions{}, fmt.Errorf("Invalid --mode value: %s.\n%s", value, GithubReviewHelp)
			}
			mode = value
		case token == "--reviewer":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubReviewExecutionOptions{}, fmt.Errorf("Missing value after --reviewer.\n%s", GithubReviewHelp)
			}
			if strings.TrimSpace(value) == "" {
				return githubReviewExecutionOptions{}, fmt.Errorf("Missing value after --reviewer.\n%s", GithubReviewHelp)
			}
			reviewer = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--reviewer="):
			if strings.TrimSpace(strings.TrimPrefix(token, "--reviewer=")) == "" {
				return githubReviewExecutionOptions{}, fmt.Errorf("Missing value after --reviewer.\n%s", GithubReviewHelp)
			}
			reviewer = strings.TrimSpace(strings.TrimPrefix(token, "--reviewer="))
		case token == "--per-item-context":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return githubReviewExecutionOptions{}, fmt.Errorf("Missing value after --per-item-context.\n%s", GithubReviewHelp)
			}
			if value != "shared" && value != "isolated" {
				return githubReviewExecutionOptions{}, fmt.Errorf("Invalid --per-item-context value: %s.\n%s", value, GithubReviewHelp)
			}
			perItemContext = value
			index++
		case strings.HasPrefix(token, "--per-item-context="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--per-item-context="))
			if value != "shared" && value != "isolated" {
				return githubReviewExecutionOptions{}, fmt.Errorf("Invalid --per-item-context value: %s.\n%s", value, GithubReviewHelp)
			}
			perItemContext = value
		default:
			return githubReviewExecutionOptions{}, fmt.Errorf("Unknown review option: %s\n%s", token, GithubReviewHelp)
		}
	}
	return githubReviewExecutionOptions{
		Target:         target,
		Mode:           mode,
		Reviewer:       reviewer,
		PerItemContext: perItemContext,
	}, nil
}

func parseGithubReviewFollowupArgs(args []string) (parsedGithubTarget, bool, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return parsedGithubTarget{}, false, fmt.Errorf("Usage: nana review followup <github-pr-url> [--allow-open]\n\n%s", GithubReviewHelp)
	}
	target, err := parseGithubTargetURL(args[0])
	if err != nil {
		return parsedGithubTarget{}, false, err
	}
	if target.kind != "pr" {
		return parsedGithubTarget{}, false, fmt.Errorf("nana review followup expects a pull request URL.\n%s", GithubReviewHelp)
	}
	allowOpen := false
	for _, token := range args[1:] {
		if token == "--allow-open" {
			allowOpen = true
			continue
		}
		return parsedGithubTarget{}, false, fmt.Errorf("Unknown review followup option: %s\n%s", token, GithubReviewHelp)
	}
	return target, allowOpen, nil
}

func githubReviewFollowup(target parsedGithubTarget, allowOpen bool) error {
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}
	var pull githubPullStatePayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d", target.repoSlug, target.number), &pull); err != nil {
		return err
	}
	if !allowOpen && !strings.EqualFold(strings.TrimSpace(pull.State), "closed") {
		return fmt.Errorf("PR #%d is still open. Re-run with --allow-open to inspect pre-existing findings before closure.", target.number)
	}
	findings, err := loadPersistedPullReviewPreexistingFindings(target.repoSlug, target.number)
	if err != nil {
		return err
	}
	targetURL := githubCanonicalTargetURL(target)
	if len(findings) == 0 {
		fmt.Fprintf(os.Stdout, "[review] No persisted pre-existing findings for %s.\n", targetURL)
		return nil
	}
	fmt.Fprintf(os.Stdout, "[review] Pre-existing findings for %s:\n", targetURL)
	for _, finding := range findings {
		fmt.Fprintf(os.Stdout, "- %s (%s)\n", finding.Title, renderGithubFindingReference(finding))
		fmt.Fprintf(os.Stdout, "  %s\n", defaultString(strings.TrimSpace(finding.UserExplanation), strings.TrimSpace(finding.Detail)))
		if link := renderGithubFindingLink(finding); link != "" {
			fmt.Fprintf(os.Stdout, "  %s\n", link)
		}
	}
	return nil
}

func loadPersistedPullReviewPreexistingFindings(repoSlug string, prNumber int) ([]githubPullReviewFinding, error) {
	runsDir := filepath.Join(githubManagedPaths(repoSlug).ReviewsRoot, fmt.Sprintf("pr-%d", prNumber), "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	runNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			runNames = append(runNames, entry.Name())
		}
	}
	slices.Sort(runNames)
	findings := []githubPullReviewFinding{}
	seen := map[string]bool{}
	for _, runName := range runNames {
		path := filepath.Join(runsDir, runName, "dropped-preexisting.json")
		var batch []githubPullReviewFinding
		if err := readGithubJSON(path, &batch); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, finding := range batch {
			key := strings.TrimSpace(finding.Fingerprint)
			if key == "" {
				key = fmt.Sprintf("%s|%s|%d", strings.TrimSpace(finding.Title), strings.TrimSpace(finding.Path), finding.Line)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			findings = append(findings, finding)
		}
	}
	return findings, nil
}

func renderGithubFindingReference(finding githubPullReviewFinding) string {
	if finding.Line > 0 {
		return fmt.Sprintf("%s:%d", finding.Path, finding.Line)
	}
	return finding.Path
}

func renderGithubFindingLink(finding githubPullReviewFinding) string {
	if finding.ChangedLineInPR && strings.TrimSpace(finding.PRPermalink) != "" {
		return finding.PRPermalink
	}
	return strings.TrimSpace(finding.MainPermalink)
}

func ResolveGithubRunIDForTargetURL(targetURL string) (string, error) {
	target, err := parseGithubTargetURL(targetURL)
	if err != nil {
		return "", err
	}
	manifest, err := findLatestRunManifestForTargetURL(target)
	if err != nil {
		return "", err
	}
	if manifest == nil && target.kind == "pr" {
		manifest, err = findLatestRunManifestForPRSandboxLink(target)
		if err != nil {
			return "", err
		}
	}
	if manifest == nil {
		return "", nil
	}
	return strings.TrimSpace(manifest.RunID), nil
}

func findLatestRunManifestForTargetURL(target parsedGithubTarget) (*githubWorkManifest, error) {
	reposRoot := githubWorkReposRoot()
	entries, err := os.ReadDir(reposRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	normalizedTargetURL := githubCanonicalTargetURL(target)
	var latest *githubWorkManifest
	for _, ownerEntry := range entries {
		if !ownerEntry.IsDir() {
			continue
		}
		repoEntries, err := os.ReadDir(filepath.Join(reposRoot, ownerEntry.Name()))
		if err != nil {
			return nil, err
		}
		for _, repoEntry := range repoEntries {
			if !repoEntry.IsDir() {
				continue
			}
			runsDir := filepath.Join(reposRoot, ownerEntry.Name(), repoEntry.Name(), "runs")
			runEntries, err := os.ReadDir(runsDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			for _, runEntry := range runEntries {
				if !runEntry.IsDir() {
					continue
				}
				manifest, err := readGithubWorkManifest(filepath.Join(runsDir, runEntry.Name(), "manifest.json"))
				if err != nil {
					continue
				}
				exactTargetMatch := strings.TrimSpace(manifest.TargetURL) == normalizedTargetURL
				linkedPRMatch := target.kind == "pr" &&
					strings.EqualFold(strings.TrimSpace(manifest.RepoSlug), strings.TrimSpace(target.repoSlug)) &&
					manifest.PublishedPRNumber == target.number
				if !exactTargetMatch && !linkedPRMatch {
					continue
				}
				if latest == nil || strings.TrimSpace(manifest.UpdatedAt) > strings.TrimSpace(latest.UpdatedAt) {
					copied := manifest
					latest = &copied
				}
			}
		}
	}
	return latest, nil
}

func findLatestRunManifestForPRSandboxLink(target parsedGithubTarget) (*githubWorkManifest, error) {
	prSandboxPath := githubWorkSandboxPath(target.repoSlug, buildGithubTargetSandboxID("pr", target.number))
	info, err := os.Lstat(prSandboxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil, nil
	}
	resolvedSandboxPath, err := filepath.EvalSymlinks(prSandboxPath)
	if err != nil {
		return nil, err
	}
	metadata, err := readGithubSandboxMetadata(resolvedSandboxPath)
	if err != nil || strings.TrimSpace(metadata.SandboxID) == "" {
		return nil, err
	}
	return findLatestRunManifestForSandbox(target.repoSlug, metadata.SandboxID)
}

func findLatestRunManifestForSandbox(repoSlug string, sandboxID string) (*githubWorkManifest, error) {
	runsDir := filepath.Join(githubWorkRepoRoot(repoSlug), "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var latest *githubWorkManifest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := readGithubWorkManifest(filepath.Join(runsDir, entry.Name(), "manifest.json"))
		if err != nil || strings.TrimSpace(manifest.SandboxID) != sandboxID {
			continue
		}
		if latest == nil || strings.TrimSpace(manifest.UpdatedAt) > strings.TrimSpace(latest.UpdatedAt) {
			copied := manifest
			latest = &copied
		}
	}
	return latest, nil
}

func readGithubWorkManifest(path string) (githubWorkManifest, error) {
	var manifest githubWorkManifest
	if err := readGithubJSON(path, &manifest); err != nil {
		return githubWorkManifest{}, err
	}
	hydrateGithubWorkManifestDefaults(&manifest)
	return manifest, nil
}
