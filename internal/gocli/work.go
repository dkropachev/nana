package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const WorkHelp = `nana work - Unified local and GitHub-backed implementation runtime

Usage:
  nana work start [<github-issue-or-pr-url>] [--repo <path>] [--task <text> | --plan-file <path>] [--max-iterations <n>] [--integration <final|always|never>] [--grouping-policy <ai|path|singleton>] [--validation-parallelism <1-8>] [--considerations <list>] [--role-layout <split|reviewer+executor>] [--new-pr] [--create-pr | --local-only] [--reviewer <login|@me>] [-- codex-args...]
  nana work resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
  nana work resolve [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work status [--run-id <id> | --last | --global-last] [--repo <path>] [--json]
  nana work logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>] [--json]
  nana work retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work explain [--run-id <id> | --last] [--json]
  nana work db-check [--json]
  nana work db-repair [--json]
  nana work verify-refresh [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work sync [--run-id <id> | --last] [--reviewer <login|@me>] [--resume-last] [codex-args...]
  nana work lane-exec --run-id <id>|--last --lane <alias> [--task <text>] [-- codex-args...]
  nana work items <subcommand>
  nana work help

Behavior:
  - local mode is selected when start does not receive a GitHub issue/PR URL
  - local mode uses --task, --plan-file, or an inferred task from the current branch
  - local mode syncs the target source branch, commits verified sandbox changes after final review gates pass, pushes to the tracked remote when one exists, and uses resolve to recover blocked final-apply states
  - GitHub mode is selected when start receives a GitHub issue/PR URL
  - shared source and sandbox checkouts use heartbeat-backed repo locks: multiple readers are allowed, one writer excludes readers, and stale lock holders are recovered automatically
  - work status --json surfaces current repo lock holders under lock_state when present
  - work items queue inbound GitHub, email, and Slack-style requests into a shared draft/submit workflow
  - authoritative runtime state lives under ~/.nana/work/
  - legacy work-local and work-on entrypoints have been replaced by nana work
`

func WorkLegacyCommandError(command string) error {
	replacement := "nana work"
	switch command {
	case "work-local":
		replacement = "nana work start --task ..."
	case "work-on":
		replacement = "nana work start <github-issue-or-pr-url>"
	}
	return fmt.Errorf("`nana %s` has been removed. Use `%s` instead.", command, replacement)
}

func MaybeHandleWorkHelp(command string, args []string) bool {
	switch command {
	case "work":
		if len(args) < 2 || isHelpToken(args[1]) || (len(args) > 2 && isHelpToken(args[2])) {
			fmt.Fprint(os.Stdout, WorkHelp)
			return true
		}
	case "work-local", "work-on":
		if len(args) < 2 || isHelpToken(args[1]) || (len(args) > 2 && isHelpToken(args[2])) {
			fmt.Fprintf(os.Stdout, "nana %s has been replaced by `nana work`.\n\n%s", command, WorkHelp)
			return true
		}
	}
	return false
}

func Work(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, WorkHelp)
		return nil
	}

	switch args[0] {
	case "start":
		return startWork(cwd, args)
	case "resume":
		return resumeWork(cwd, args[1:])
	case "resolve":
		return resolveWork(cwd, args[1:])
	case "status":
		return workStatus(cwd, args[1:])
	case "logs":
		return workLogs(cwd, args[1:])
	case "retrospective":
		return workRetrospective(cwd, args[1:])
	case "verify-refresh":
		return workVerifyRefresh(cwd, args[1:])
	case "db-check", "db-repair":
		return runWorkDBCommand(args)
	case "items":
		return workItemsCommand(cwd, args[1:])
	case "sync", "lane-exec", "defaults", "stats", "explain":
		_, err := GithubWorkCommand(cwd, args)
		return err
	default:
		return fmt.Errorf("Unknown work subcommand: %s\n\n%s", args[0], WorkHelp)
	}
}

func startWork(cwd string, args []string) error {
	if len(args) > 1 {
		first := strings.TrimSpace(args[1])
		if strings.HasPrefix(first, "https://github.com/") {
			_, err := GithubWorkCommand(cwd, args)
			return err
		}
	}
	return runLocalWorkCommand(cwd, args)
}

func resumeWork(cwd string, args []string) error {
	options, err := parseLocalWorkResumeArgs(args)
	if err != nil {
		return err
	}
	backend, err := resolveWorkBackend(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	if backend == "github" {
		return resumeGithubWork(options)
	}
	return resumeLocalWork(cwd, options)
}

func resolveWork(cwd string, args []string) error {
	options, err := parseLocalWorkResolveArgs(args)
	if err != nil {
		return err
	}
	backend, err := resolveWorkBackend(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	if backend == "github" {
		return fmt.Errorf("work resolve is only available for blocked local runs")
	}
	return resolveLocalWork(cwd, options)
}

func workStatus(cwd string, args []string) error {
	options, err := parseLocalWorkStatusArgs(args)
	if err != nil {
		return err
	}
	backend, err := resolveWorkBackend(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	if backend == "github" {
		return githubWorkStatus(options.RunSelection, options.JSON)
	}
	return localWorkStatus(cwd, options)
}

func workLogs(cwd string, args []string) error {
	options, err := parseLocalWorkLogsArgs(args)
	if err != nil {
		return err
	}
	backend, err := resolveWorkBackend(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	if backend == "github" {
		return githubWorkLogs(options.RunSelection, options.TailLines, options.JSON)
	}
	return localWorkLogs(cwd, options)
}

func workRetrospective(cwd string, args []string) error {
	selection, err := parseLocalWorkRunSelection(args, true)
	if err != nil {
		return err
	}
	backend, err := resolveWorkBackend(cwd, selection)
	if err != nil {
		return err
	}
	if backend == "github" {
		githubArgs := []string{}
		if strings.TrimSpace(selection.RunID) != "" {
			githubArgs = append(githubArgs, "--run-id", selection.RunID)
		} else if selection.UseLast {
			githubArgs = append(githubArgs, "--last")
		}
		return githubWorkRetrospective(githubArgs)
	}
	return localWorkRetrospective(cwd, selection)
}

func workVerifyRefresh(cwd string, args []string) error {
	selection, err := parseLocalWorkRunSelection(args, true)
	if err != nil {
		return err
	}
	backend, err := resolveWorkBackend(cwd, selection)
	if err != nil {
		return err
	}
	if backend == "github" {
		return refreshGithubVerificationArtifacts(selection.RunID, selection.UseLast)
	}
	return refreshLocalWorkVerificationArtifacts(cwd, selection)
}

func resolveWorkBackend(cwd string, selection localWorkRunSelection) (string, error) {
	runID := strings.TrimSpace(selection.RunID)
	if runID != "" {
		switch {
		case strings.HasPrefix(runID, "lw-"):
			return "local", nil
		case strings.HasPrefix(runID, "gh-"):
			return "github", nil
		}
		if _, err := readLocalWorkManifestByRunID(runID); err == nil {
			return "local", nil
		}
		if _, _, err := resolveGithubRunManifestPath(runID, false); err == nil {
			return "github", nil
		}
		return "", fmt.Errorf("work run %s was not found", runID)
	}

	if selection.GlobalLast {
		if entry, err := latestAnyWorkRunIndex(); err == nil && strings.TrimSpace(entry.Backend) != "" {
			return entry.Backend, nil
		}
		return "local", nil
	}
	if strings.TrimSpace(selection.RepoPath) != "" {
		return "local", nil
	}
	if selection.UseLast {
		if _, err := resolveLocalWorkRepoRoot(cwd, ""); err == nil {
			if _, _, err := resolveLocalWorkRun(cwd, selection); err == nil {
				return "local", nil
			}
		}
		if _, _, err := resolveGithubRunManifestPath("", true); err == nil {
			return "github", nil
		}
		if _, _, err := resolveLocalWorkRun(cwd, localWorkRunSelection{GlobalLast: true}); err == nil {
			return "local", nil
		}
	}

	return "", fmt.Errorf("no work run matched the provided selection")
}

type githubWorkStatusSnapshot struct {
	RunID                   string                             `json:"run_id"`
	RepoSlug                string                             `json:"repo_slug"`
	TargetKind              string                             `json:"target_kind"`
	TargetNumber            int                                `json:"target_number"`
	TargetURL               string                             `json:"target_url"`
	Sandbox                 string                             `json:"sandbox"`
	RepoCheckout            string                             `json:"repo_checkout"`
	UpdatedAt               string                             `json:"updated_at"`
	ReviewReviewer          string                             `json:"review_reviewer,omitempty"`
	PublicationState        string                             `json:"publication_state,omitempty"`
	PublicationDetail       string                             `json:"publication_detail,omitempty"`
	PublicationError        string                             `json:"publication_error,omitempty"`
	ExecutionStatus         string                             `json:"execution_status,omitempty"`
	CurrentPhase            string                             `json:"current_phase,omitempty"`
	CurrentRound            int                                `json:"current_round,omitempty"`
	CompletionRounds        []githubWorkCompletionRoundSummary `json:"completion_rounds,omitempty"`
	FinalGateStatus         string                             `json:"final_gate_status,omitempty"`
	CandidateAuditStatus    string                             `json:"candidate_audit_status,omitempty"`
	CandidateBlockedPaths   []string                           `json:"candidate_blocked_paths,omitempty"`
	RejectedFindingCount    int                                `json:"rejected_finding_count,omitempty"`
	PreexistingFindingCount int                                `json:"preexisting_finding_count,omitempty"`
	PauseReason             string                             `json:"pause_reason,omitempty"`
	PauseUntil              string                             `json:"pause_until,omitempty"`
	LastError               string                             `json:"last_error,omitempty"`
	LeaderSessionID         string                             `json:"leader_session_id,omitempty"`
	LeaderResumeEligible    bool                               `json:"leader_resume_eligible,omitempty"`
	Lanes                   []githubLaneRuntimeState           `json:"lanes,omitempty"`
	FeedbackAvailable       bool                               `json:"feedback_available,omitempty"`
	LockState               *repoAccessLockStatusSnapshot      `json:"lock_state,omitempty"`
}

func githubWorkStatus(selection localWorkRunSelection, jsonOutput bool) error {
	manifest, runDir, err := resolveGithubWorkRun(selection)
	if err != nil {
		return err
	}
	snapshot, err := buildGithubWorkStatusSnapshot(manifest, runDir)
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err := os.Stdout.Write(mustMarshalJSON(snapshot))
		return err
	}
	fmt.Fprintf(os.Stdout, "[work] Run id: %s\n", snapshot.RunID)
	fmt.Fprintf(os.Stdout, "[work] Repo: %s\n", snapshot.RepoSlug)
	fmt.Fprintf(os.Stdout, "[work] Target: %s #%d\n", snapshot.TargetKind, snapshot.TargetNumber)
	fmt.Fprintf(os.Stdout, "[work] URL: %s\n", snapshot.TargetURL)
	fmt.Fprintf(os.Stdout, "[work] Sandbox: %s\n", snapshot.Sandbox)
	fmt.Fprintf(os.Stdout, "[work] Repo checkout: %s\n", snapshot.RepoCheckout)
	fmt.Fprintf(os.Stdout, "[work] Updated: %s\n", snapshot.UpdatedAt)
	if strings.TrimSpace(snapshot.ReviewReviewer) != "" {
		fmt.Fprintf(os.Stdout, "[work] Reviewer sync user: %s\n", snapshot.ReviewReviewer)
	}
	if strings.TrimSpace(snapshot.PublicationState) != "" {
		fmt.Fprintf(os.Stdout, "[work] Publication state: %s\n", snapshot.PublicationState)
	}
	if strings.TrimSpace(snapshot.PublicationDetail) != "" {
		fmt.Fprintf(os.Stdout, "[work] Publication detail: %s\n", snapshot.PublicationDetail)
	}
	if strings.TrimSpace(snapshot.PublicationError) != "" {
		fmt.Fprintf(os.Stdout, "[work] Publication error: %s\n", snapshot.PublicationError)
	}
	if strings.TrimSpace(snapshot.ExecutionStatus) != "" {
		fmt.Fprintf(os.Stdout, "[work] Execution status: %s\n", snapshot.ExecutionStatus)
	}
	if strings.TrimSpace(snapshot.CurrentPhase) != "" {
		fmt.Fprintf(os.Stdout, "[work] Current phase: %s", snapshot.CurrentPhase)
		if snapshot.CurrentRound > 0 {
			fmt.Fprintf(os.Stdout, " round=%d", snapshot.CurrentRound)
		}
		fmt.Fprintln(os.Stdout)
	}
	if strings.TrimSpace(snapshot.PauseUntil) != "" {
		fmt.Fprintf(os.Stdout, "[work] Pause until: %s", snapshot.PauseUntil)
		if strings.TrimSpace(snapshot.PauseReason) != "" {
			fmt.Fprintf(os.Stdout, " reason=%s", snapshot.PauseReason)
		}
		fmt.Fprintln(os.Stdout)
	}
	if strings.TrimSpace(snapshot.LastError) != "" {
		fmt.Fprintf(os.Stdout, "[work] Last error: %s\n", snapshot.LastError)
	}
	if strings.TrimSpace(snapshot.FinalGateStatus) != "" {
		fmt.Fprintf(os.Stdout, "[work] Final gate: %s\n", snapshot.FinalGateStatus)
	}
	if strings.TrimSpace(snapshot.CandidateAuditStatus) != "" {
		fmt.Fprintf(os.Stdout, "[work] Candidate audit: %s\n", snapshot.CandidateAuditStatus)
	}
	if len(snapshot.CandidateBlockedPaths) > 0 {
		fmt.Fprintf(os.Stdout, "[work] Candidate blocked paths: %s\n", strings.Join(snapshot.CandidateBlockedPaths, ", "))
	}
	if len(snapshot.CompletionRounds) > 0 {
		last := snapshot.CompletionRounds[len(snapshot.CompletionRounds)-1]
		fmt.Fprintf(os.Stdout, "[work] Latest completion round: %d status=%s verification=%s findings=%d\n", last.Round, defaultString(last.Status, "(none)"), defaultString(last.VerificationSummary, "(none)"), last.ReviewFindings)
	}
	if snapshot.LockState != nil {
		if repoAccessLockStateHasHolders(snapshot.LockState.Source) {
			fmt.Fprintf(os.Stdout, "[work] Repo lock (source): %s\n", repoAccessLockStateSummary(snapshot.LockState.Source))
		}
		if repoAccessLockStateHasHolders(snapshot.LockState.Sandbox) {
			fmt.Fprintf(os.Stdout, "[work] Repo lock (sandbox): %s\n", repoAccessLockStateSummary(snapshot.LockState.Sandbox))
		}
	}
	if strings.TrimSpace(snapshot.LeaderSessionID) != "" {
		fmt.Fprintf(os.Stdout, "[work] Leader session: %s", snapshot.LeaderSessionID)
		if snapshot.LeaderResumeEligible {
			fmt.Fprint(os.Stdout, " (resume available)")
		}
		fmt.Fprintln(os.Stdout)
	}
	if snapshot.FeedbackAvailable {
		fmt.Fprintln(os.Stdout, "[work] Feedback instructions are present for this run.")
	}
	for _, lane := range snapshot.Lanes {
		fmt.Fprintf(os.Stdout, "[work] Lane: %s status=%s role=%s\n", lane.Alias, lane.Status, lane.Role)
	}
	return nil
}

func githubWorkLogs(selection localWorkRunSelection, tail int, jsonOutput bool) error {
	manifest, runDir, err := resolveGithubWorkRun(selection)
	if err != nil {
		return err
	}
	files, err := githubWorkLogFiles(runDir)
	if err != nil {
		return err
	}
	snapshot, err := buildGithubWorkStatusSnapshot(manifest, runDir)
	if err != nil {
		return err
	}
	entries := make([]map[string]string, 0, len(files))
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		display := string(content)
		if tail > 0 {
			display = tailLines(display, tail)
		}
		entries = append(entries, map[string]string{
			"name":    filepath.Base(path),
			"path":    path,
			"content": display,
		})
	}
	if jsonOutput {
		payload := map[string]any{
			"run":   snapshot,
			"files": entries,
		}
		_, err := os.Stdout.Write(mustMarshalJSON(payload))
		return err
	}
	fmt.Fprintf(os.Stdout, "[work] Run id: %s\n", snapshot.RunID)
	fmt.Fprintf(os.Stdout, "[work] Repo: %s\n", snapshot.RepoSlug)
	fmt.Fprintf(os.Stdout, "[work] Run artifacts: %s\n", runDir)
	for _, entry := range entries {
		fmt.Fprintf(os.Stdout, "\n== %s ==\n", entry["name"])
		if strings.TrimSpace(entry["content"]) == "" {
			fmt.Fprintln(os.Stdout, "(empty)")
			continue
		}
		fmt.Fprint(os.Stdout, entry["content"])
		if !strings.HasSuffix(entry["content"], "\n") {
			fmt.Fprintln(os.Stdout)
		}
	}
	return nil
}

func resolveGithubWorkRun(selection localWorkRunSelection) (githubWorkManifest, string, error) {
	useLast := selection.UseLast || selection.GlobalLast || strings.TrimSpace(selection.RunID) == ""
	manifestPath, _, err := resolveGithubRunManifestPath(selection.RunID, useLast)
	if err != nil {
		return githubWorkManifest{}, "", err
	}
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		return githubWorkManifest{}, "", err
	}
	return manifest, filepath.Dir(manifestPath), nil
}

func buildGithubWorkStatusSnapshot(manifest githubWorkManifest, runDir string) (githubWorkStatusSnapshot, error) {
	lanes, err := readGithubLaneRuntimeStates(filepath.Join(runDir, "lane-runtime"))
	if err != nil {
		return githubWorkStatusSnapshot{}, err
	}
	_, feedbackErr := os.Stat(filepath.Join(runDir, "feedback-instructions.md"))
	leaderCheckpoint, _ := readCodexStepCheckpoint(filepath.Join(runDir, "leader-checkpoint.json"))
	sourcePath := defaultString(strings.TrimSpace(manifest.SourcePath), githubManagedPaths(manifest.RepoSlug).SourcePath)
	lockState, err := buildRepoAccessLockStatus(sourcePath, repoAccessLockWrite, manifest.SandboxRepoPath, repoAccessLockWrite)
	if err != nil {
		return githubWorkStatusSnapshot{}, err
	}
	return githubWorkStatusSnapshot{
		RunID:                   manifest.RunID,
		RepoSlug:                manifest.RepoSlug,
		TargetKind:              manifest.TargetKind,
		TargetNumber:            manifest.TargetNumber,
		TargetURL:               manifest.TargetURL,
		Sandbox:                 manifest.SandboxPath,
		RepoCheckout:            manifest.SandboxRepoPath,
		UpdatedAt:               manifest.UpdatedAt,
		ReviewReviewer:          manifest.ReviewReviewer,
		PublicationState:        manifest.PublicationState,
		PublicationDetail:       manifest.PublicationDetail,
		PublicationError:        manifest.PublicationError,
		ExecutionStatus:         manifest.ExecutionStatus,
		CurrentPhase:            manifest.CurrentPhase,
		CurrentRound:            manifest.CurrentRound,
		CompletionRounds:        append([]githubWorkCompletionRoundSummary{}, manifest.CompletionRounds...),
		FinalGateStatus:         manifest.FinalGateStatus,
		CandidateAuditStatus:    manifest.CandidateAuditStatus,
		CandidateBlockedPaths:   append([]string{}, manifest.CandidateBlockedPaths...),
		RejectedFindingCount:    len(manifest.RejectedFindingFingerprints),
		PreexistingFindingCount: len(manifest.PreexistingFindings),
		PauseReason:             manifest.PauseReason,
		PauseUntil:              manifest.PauseUntil,
		LastError:               manifest.LastError,
		LeaderSessionID:         strings.TrimSpace(leaderCheckpoint.SessionID),
		LeaderResumeEligible:    leaderCheckpoint.ResumeEligible,
		Lanes:                   lanes,
		FeedbackAvailable:       feedbackErr == nil,
		LockState:               lockState,
	}, nil
}

func readGithubLaneRuntimeStates(runtimeDir string) ([]githubLaneRuntimeState, error) {
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	states := []githubLaneRuntimeState{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(runtimeDir, entry.Name())
		var state githubLaneRuntimeState
		if err := readGithubJSON(path, &state); err != nil {
			continue
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].Alias < states[j].Alias
	})
	return states, nil
}

func githubWorkLogFiles(runDir string) ([]string, error) {
	patterns := []string{
		"manifest.json",
		"start-instructions.md",
		"feedback-instructions.md",
		"retrospective.md",
		"thread-usage.json",
		"completion/*",
		"completion/*/*",
		"lane-runtime/*",
	}
	seen := map[string]bool{}
	files := []string{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(runDir, pattern))
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || info.IsDir() || seen[match] {
				continue
			}
			seen[match] = true
			files = append(files, match)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no work logs found at %s", runDir)
	}
	return files, nil
}

func resumeGithubWork(options localWorkResumeOptions) error {
	manifest, runDir, err := resolveGithubWorkRun(options.RunSelection)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	sandboxLock, err := acquireSandboxWriteLock(manifest.SandboxRepoPath, repoAccessLockOwner{
		Backend: "github-work",
		RunID:   manifest.RunID,
		Purpose: "leader-resume",
		Label:   "github-work-resume",
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = sandboxLock.Release()
	}()
	if strings.TrimSpace(manifest.CurrentPhase) != "" && manifest.CurrentPhase != "completed" && manifest.CurrentPhase != "leader" {
		if err := requireGithubWorkBaselineForCompletionResume(&manifest); err != nil {
			return err
		}
		if err := runGithubWorkCompletionLoop(manifestPath, runDir, &manifest, options.CodexArgs); err != nil {
			manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if pauseErr, ok := isCodexRateLimitPauseError(err); ok {
				manifest.ExecutionStatus = "paused"
				manifest.PauseReason = strings.TrimSpace(pauseErr.Info.Reason)
				manifest.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
				manifest.PausedAt = manifest.UpdatedAt
				manifest.LastError = codexPauseInfoMessage(pauseErr.Info)
			} else if manifest.ExecutionStatus != "blocked" {
				manifest.ExecutionStatus = "failed"
				manifest.LastError = err.Error()
			}
			if writeErr := writeGithubJSON(manifestPath, manifest); writeErr != nil {
				return writeErr
			}
			if writeErr := indexGithubWorkRunManifest(manifestPath, manifest); writeErr != nil {
				return writeErr
			}
			return err
		}
		manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		manifest.ExecutionStatus = "completed"
		manifest.CurrentPhase = "completed"
		manifest.CurrentRound = 0
		manifest.PauseReason = ""
		manifest.PauseUntil = ""
		manifest.PausedAt = ""
		manifest.LastError = ""
		if err := writeGithubJSON(manifestPath, manifest); err != nil {
			return err
		}
		if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "[github] Resuming completion loop for run %s.\n", manifest.RunID)
		return nil
	}
	if _, err := captureGithubWorkBaselineIfMissing(manifestPath, &manifest); err != nil {
		return err
	}

	laneCodexHome, err := ensureGithubLaneCodexHome(manifest.SandboxPath, "leader")
	if err != nil {
		return err
	}
	instructions := buildGithubStartInstructions(manifest)
	prompt := fmt.Sprintf("Resume GitHub %s #%d for %s", manifest.TargetKind, manifest.TargetNumber, manifest.RepoSlug)
	finalPrompt := instructions + "\n\nTask:\n" + prompt
	normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(options.CodexArgs)
	finalPrompt = prefixCodexFastPrompt(finalPrompt, fastMode)
	transport := promptTransportForSize(finalPrompt, structuredPromptStdinThreshold)
	result, runErr := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       manifest.SandboxPath,
		InstructionsRoot: manifest.SandboxPath,
		CodexHome:        laneCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", manifest.SandboxRepoPath},
		CommonArgs:       normalizedCodexArgs,
		Prompt:           finalPrompt,
		PromptTransport:  transport,
		CheckpointPath:   filepath.Join(runDir, "leader-checkpoint.json"),
		StepKey:          "github-leader",
		ResumeStrategy:   codexResumeConversation,
		Env:              append(buildGithubCodexEnv(NotifyTempContract{}, laneCodexHome, manifest.APIBaseURL), "NANA_PROJECT_AGENTS_ROOT="+manifest.SandboxRepoPath),
		OnPause: func(info codexRateLimitPauseInfo) {
			manifest.ExecutionStatus = "paused"
			manifest.PauseReason = strings.TrimSpace(info.Reason)
			manifest.PauseUntil = strings.TrimSpace(info.RetryAfter)
			manifest.PausedAt = ISOTimeNow()
			manifest.LastError = codexPauseInfoMessage(info)
			manifest.UpdatedAt = manifest.PausedAt
			_ = writeGithubJSON(filepath.Join(runDir, "manifest.json"), manifest)
			_ = indexGithubWorkRunManifest(filepath.Join(runDir, "manifest.json"), manifest)
		},
		OnResume: func(info codexRateLimitPauseInfo) {
			manifest.ExecutionStatus = "running"
			manifest.PauseReason = ""
			manifest.PauseUntil = ""
			manifest.PausedAt = ""
			manifest.LastError = ""
			manifest.UpdatedAt = ISOTimeNow()
			_ = writeGithubJSON(filepath.Join(runDir, "manifest.json"), manifest)
			_ = indexGithubWorkRunManifest(filepath.Join(runDir, "manifest.json"), manifest)
		},
	})

	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if pauseErr, ok := isCodexRateLimitPauseError(runErr); ok {
		manifest.ExecutionStatus = "paused"
		manifest.PauseReason = strings.TrimSpace(pauseErr.Info.Reason)
		manifest.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
		manifest.PausedAt = manifest.UpdatedAt
		manifest.LastError = codexPauseInfoMessage(pauseErr.Info)
	} else if runErr != nil {
		manifest.ExecutionStatus = "failed"
		manifest.LastError = runErr.Error()
	} else {
		manifest.ExecutionStatus = "completed"
		manifest.PauseReason = ""
		manifest.PauseUntil = ""
		manifest.PausedAt = ""
		manifest.LastError = ""
	}
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		return err
	}
	completionErr := error(nil)
	if runErr == nil {
		completionErr = runGithubWorkCompletionLoop(manifestPath, runDir, &manifest, options.CodexArgs)
		manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if pauseErr, ok := isCodexRateLimitPauseError(completionErr); ok {
			manifest.ExecutionStatus = "paused"
			manifest.PauseReason = strings.TrimSpace(pauseErr.Info.Reason)
			manifest.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
			manifest.PausedAt = manifest.UpdatedAt
			manifest.LastError = codexPauseInfoMessage(pauseErr.Info)
		} else if completionErr != nil {
			if manifest.ExecutionStatus != "blocked" {
				manifest.ExecutionStatus = "failed"
			}
			manifest.LastError = defaultString(strings.TrimSpace(manifest.LastError), completionErr.Error())
		} else {
			manifest.ExecutionStatus = "completed"
			manifest.CurrentPhase = "completed"
			manifest.CurrentRound = 0
			manifest.PauseReason = ""
			manifest.PauseUntil = ""
			manifest.PausedAt = ""
			manifest.LastError = ""
		}
		if err := writeGithubJSON(manifestPath, manifest); err != nil {
			return err
		}
		if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stdout, "[github] Resuming run %s for %s %s #%d\n", manifest.RunID, manifest.RepoSlug, manifest.TargetKind, manifest.TargetNumber)
	if strings.TrimSpace(result.Stdout) != "" {
		fmt.Fprint(os.Stdout, result.Stdout)
	}
	if strings.TrimSpace(result.Stderr) != "" {
		fmt.Fprint(os.Stdout, result.Stderr)
	}
	if runErr != nil {
		return runErr
	}
	return completionErr
}

func workHomeRoot() string {
	return filepath.Join(githubNanaHome(), "work")
}

func githubWorkReposRoot() string {
	return filepath.Join(workHomeRoot(), "repos")
}

func githubWorkRepoRoot(repoSlug string) string {
	return filepath.Join(githubWorkReposRoot(), filepath.FromSlash(repoSlug))
}

func githubWorkLatestRunPath() string {
	return filepath.Join(workHomeRoot(), "latest-run.json")
}

func githubWorkReviewRulesGlobalConfigPath() string {
	return filepath.Join(workHomeRoot(), "review-rules-config.json")
}

func githubWorkIssueStatsPath(repoSlug string, issueNumber int) string {
	return filepath.Join(githubWorkRepoRoot(repoSlug), "issues", fmt.Sprintf("issue-%d.json", issueNumber))
}

func githubWorkSandboxPath(repoSlug string, sandboxID string) string {
	return filepath.Join(githubWorkRepoRoot(repoSlug), "sandboxes", sandboxID)
}
