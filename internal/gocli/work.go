package gocli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const WorkHelp = `nana work - Unified local and GitHub-backed implementation runtime

Usage:
  nana work start [<github-issue-or-pr-url>] [--repo <path>] [--task <text> | --plan-file <path>] [--max-iterations <n>] [--integration <final|always|never>] [--grouping-policy <ai|path|singleton>] [--validation-parallelism <1-8>] [--considerations <list>] [--role-layout <split|reviewer+executor>] [--new-pr] [--create-pr | --local-only] [--reviewer <login|@me>] [-- codex-args...]
  nana work resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
  nana work status [--run-id <id> | --last | --global-last] [--repo <path>] [--json]
  nana work logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>] [--json]
  nana work retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work explain [--run-id <id> | --last] [--json]
  nana work verify-refresh [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work sync [--run-id <id> | --last] [--reviewer <login|@me>] [--resume-last] [codex-args...]
  nana work lane-exec --run-id <id>|--last --lane <alias> [--task <text>] [-- codex-args...]
  nana work items <subcommand>
  nana work help

Behavior:
  - local mode is selected when start does not receive a GitHub issue/PR URL
  - local mode uses --task, --plan-file, or an inferred task from the current branch
 - local mode syncs the target source branch, commits verified sandbox changes after final review gates pass, and pushes to the tracked remote when one exists
 - GitHub mode is selected when start receives a GitHub issue/PR URL
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
	case "status":
		return workStatus(cwd, args[1:])
	case "logs":
		return workLogs(cwd, args[1:])
	case "retrospective":
		return workRetrospective(cwd, args[1:])
	case "verify-refresh":
		return workVerifyRefresh(cwd, args[1:])
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
	RunID             string                   `json:"run_id"`
	RepoSlug          string                   `json:"repo_slug"`
	TargetKind        string                   `json:"target_kind"`
	TargetNumber      int                      `json:"target_number"`
	TargetURL         string                   `json:"target_url"`
	Sandbox           string                   `json:"sandbox"`
	RepoCheckout      string                   `json:"repo_checkout"`
	UpdatedAt         string                   `json:"updated_at"`
	ReviewReviewer    string                   `json:"review_reviewer,omitempty"`
	PublicationState  string                   `json:"publication_state,omitempty"`
	PublicationDetail string                   `json:"publication_detail,omitempty"`
	PublicationError  string                   `json:"publication_error,omitempty"`
	Lanes             []githubLaneRuntimeState `json:"lanes,omitempty"`
	FeedbackAvailable bool                     `json:"feedback_available,omitempty"`
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
	return githubWorkStatusSnapshot{
		RunID:             manifest.RunID,
		RepoSlug:          manifest.RepoSlug,
		TargetKind:        manifest.TargetKind,
		TargetNumber:      manifest.TargetNumber,
		TargetURL:         manifest.TargetURL,
		Sandbox:           manifest.SandboxPath,
		RepoCheckout:      manifest.SandboxRepoPath,
		UpdatedAt:         manifest.UpdatedAt,
		ReviewReviewer:    manifest.ReviewReviewer,
		PublicationState:  manifest.PublicationState,
		PublicationDetail: manifest.PublicationDetail,
		PublicationError:  manifest.PublicationError,
		Lanes:             lanes,
		FeedbackAvailable: feedbackErr == nil,
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

	laneCodexHome, err := ensureGithubLaneCodexHome(manifest.SandboxPath, "leader")
	if err != nil {
		return err
	}
	sessionID := fmt.Sprintf("resume-%d", time.Now().UnixNano())
	sessionInstructionsPath, err := writeSessionModelInstructions(manifest.SandboxPath, sessionID, laneCodexHome)
	if err != nil {
		return err
	}
	defer removeSessionInstructionsFile(manifest.SandboxPath, sessionID)

	instructions := buildGithubStartInstructions(manifest)
	prompt := fmt.Sprintf("Resume GitHub %s #%d for %s", manifest.TargetKind, manifest.TargetNumber, manifest.RepoSlug)
	finalPrompt := instructions + "\n\nTask:\n" + prompt
	normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(options.CodexArgs)
	finalPrompt = prefixCodexFastPrompt(finalPrompt, fastMode)
	execArgs := append([]string{"exec", "-C", manifest.SandboxRepoPath}, normalizedCodexArgs...)
	execArgs = append(execArgs, finalPrompt)
	execArgs = injectModelInstructionsArgs(execArgs, sessionInstructionsPath)
	cmd := exec.Command("codex", execArgs...)
	cmd.Dir = manifest.SandboxPath
	cmd.Env = append(buildGithubCodexEnv(NotifyTempContract{}, laneCodexHome, manifest.APIBaseURL), "NANA_PROJECT_AGENTS_ROOT="+manifest.SandboxRepoPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "[github] Resuming run %s for %s %s #%d\n", manifest.RunID, manifest.RepoSlug, manifest.TargetKind, manifest.TargetNumber)
	if stdout.Len() > 0 {
		fmt.Fprint(os.Stdout, stdout.String())
	}
	if stderr.Len() > 0 {
		fmt.Fprint(os.Stdout, stderr.String())
	}
	return runErr
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
