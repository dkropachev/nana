package gocli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func startGithubWork(options githubWorkStartOptions) error {
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}
	target, err := githubFetchTargetContext(options.Target, apiBaseURL, token)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	paths := githubManagedPaths(options.Target.repoSlug)
	repoMeta, err := ensureGithubManagedRepoMetadata(paths, target, now)
	if err != nil {
		return err
	}
	if err := ensureGithubSourceClone(paths, repoMeta); err != nil {
		return err
	}

	repoVerificationPlan := detectGithubVerificationPlan(paths.SourcePath)
	if err := writeGithubJSON(paths.RepoVerificationPlanPath, repoVerificationPlan); err != nil {
		return err
	}
	settings, _ := readGithubRepoSettings(paths.RepoSettingsPath)
	if settings == nil {
		inferred := inferGithubInitialRepoConsiderations(paths.SourcePath, repoMeta.RepoSlug, repoVerificationPlan)
		settings = &githubRepoSettings{
			Version:               4,
			DefaultConsiderations: inferred.Considerations,
			DefaultRoleLayout:     "split",
			UpdatedAt:             now.Format(time.RFC3339),
		}
	}
	settings.HotPathAPIProfile = inferGithubHotPathProfile(paths.SourcePath, now)
	settings.UpdatedAt = now.Format(time.RFC3339)
	if settings.DefaultRoleLayout == "" {
		settings.DefaultRoleLayout = "split"
	}
	if settings.Version == 0 {
		settings.Version = 4
	}
	if err := writeGithubJSON(paths.RepoSettingsPath, settings); err != nil {
		return err
	}

	activeConsiderations := uniqueStrings(append(append([]string{}, settings.DefaultConsiderations...), options.RequestedConsiderations...))
	roleLayout := options.RoleLayout
	if roleLayout == "" {
		roleLayout = settings.DefaultRoleLayout
	}
	if roleLayout == "" {
		roleLayout = "split"
	}
	runID := buildGithubRunID(now)
	sandboxID := buildGithubSandboxID(options.Target, runID)
	sandboxPath := filepath.Join(paths.RepoRoot, "sandboxes", sandboxID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(filepath.Join(paths.RepoRoot, "runs", runID), 0o755); err != nil {
		return err
	}
	if err := cloneGithubSourceToSandbox(paths.SourcePath, sandboxRepoPath); err != nil {
		return err
	}
	verificationPlan := detectGithubVerificationPlan(sandboxRepoPath)
	runDir := filepath.Join(paths.RepoRoot, "runs", runID)
	verificationScriptsDir, err := writeGithubVerificationScripts(sandboxPath, sandboxRepoPath, verificationPlan, runID)
	if err != nil {
		return err
	}
	pipeline := buildGithubPipeline(activeConsiderations, roleLayout)
	convertedPipeline := convertGithubLanes(pipeline)
	manifest := githubWorkManifest{
		Version:                 3,
		RunID:                   runID,
		CreatedAt:               now.Format(time.RFC3339),
		UpdatedAt:               now.Format(time.RFC3339),
		RepoSlug:                repoMeta.RepoSlug,
		RepoOwner:               repoMeta.RepoOwner,
		RepoName:                repoMeta.RepoName,
		ManagedRepoRoot:         paths.RepoRoot,
		SourcePath:              paths.SourcePath,
		SandboxID:               sandboxID,
		SandboxPath:             sandboxPath,
		SandboxRepoPath:         sandboxRepoPath,
		VerificationPlan:        &verificationPlan,
		VerificationScriptsDir:  verificationScriptsDir,
		ConsiderationsActive:    activeConsiderations,
		RoleLayout:              roleLayout,
		ConsiderationPipeline:   convertedPipeline,
		LanePromptArtifacts:     []githubLanePromptArtifact{},
		CreatePROnComplete:      options.CreatePR,
		TargetKind:              options.Target.kind,
		TargetNumber:            options.Target.number,
		TargetTitle:             target.Issue.Title,
		TargetURL:               githubCanonicalTargetURL(options.Target),
		TargetState:             target.Issue.State,
		ReviewReviewer:          options.Reviewer,
		APIBaseURL:              apiBaseURL,
		DefaultBranch:           repoMeta.DefaultBranch,
		LastSeenIssueCommentID:  0,
		LastSeenReviewID:        0,
		LastSeenReviewCommentID: 0,
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		return err
	}
	startInstructionsPath := filepath.Join(runDir, "start-instructions.md")
	if err := os.WriteFile(startInstructionsPath, []byte(buildGithubStartInstructions(manifest)), 0o644); err != nil {
		return err
	}
	if err := writeGithubJSON(filepath.Join(paths.RepoRoot, "latest-run.json"), map[string]string{"repo_root": paths.RepoRoot, "run_id": runID}); err != nil {
		return err
	}
	if err := writeGithubJSON(githubWorkLatestRunPath(), map[string]string{"repo_root": paths.RepoRoot, "run_id": runID}); err != nil {
		return err
	}

	laneCodexHome, err := ensureGithubLaneCodexHome(sandboxPath, "leader")
	if err != nil {
		return err
	}
	sessionID := fmt.Sprintf("start-%d", time.Now().UnixNano())
	sessionInstructionsPath, err := writeSessionModelInstructions(sandboxPath, sessionID, laneCodexHome)
	if err != nil {
		return err
	}
	defer removeSessionInstructionsFile(sandboxPath, sessionID)
	prompt := fmt.Sprintf("Implement GitHub %s #%d for %s", options.Target.kind, options.Target.number, options.Target.repoSlug)
	finalPrompt := buildGithubStartInstructions(manifest) + "\n\nTask:\n" + prompt
	execArgs := append([]string{"exec", "-C", sandboxRepoPath}, options.CodexArgs...)
	execArgs = append(execArgs, finalPrompt)
	execArgs = injectModelInstructionsArgs(execArgs, sessionInstructionsPath)
	cmd := exec.Command("codex", execArgs...)
	cmd.Dir = sandboxPath
	cmd.Env = append(buildCodexEnv(NotifyTempContract{}, laneCodexHome), "NANA_PROJECT_AGENTS_ROOT="+sandboxRepoPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	fmt.Fprintf(os.Stdout, "[github] Starting run %s for %s %s #%d\n", runID, manifest.RepoSlug, manifest.TargetKind, manifest.TargetNumber)
	fmt.Fprintf(os.Stdout, "[github] Managed repo root: %s\n", paths.RepoRoot)
	fmt.Fprintf(os.Stdout, "[github] Managed sandbox: %s -> %s\n", sandboxID, sandboxPath)
	fmt.Fprintf(os.Stdout, "[github] Managed repo checkout: %s\n", sandboxRepoPath)
	fmt.Fprintf(os.Stdout, "[github] Reviewer sync user: %s\n", options.Reviewer)
	if stdout.Len() > 0 {
		fmt.Fprint(os.Stdout, stdout.String())
	}
	if stderr.Len() > 0 {
		fmt.Fprint(os.Stdout, stderr.String())
	}
	return runErr
}

func buildGithubRunID(now time.Time) string {
	return fmt.Sprintf("gh-%d", now.UnixNano())
}

func buildGithubSandboxID(target parsedGithubTarget, runID string) string {
	return fmt.Sprintf("%s-%d-%s", target.kind, target.number, runID)
}

func cloneGithubSourceToSandbox(sourcePath string, sandboxRepoPath string) error {
	if err := os.MkdirAll(filepath.Dir(sandboxRepoPath), 0o755); err != nil {
		return err
	}
	return githubRunGit("", "clone", sourcePath, sandboxRepoPath)
}

func convertGithubLanes(lanes []githubLane) []githubPipelineLane {
	out := make([]githubPipelineLane, 0, len(lanes))
	for _, lane := range lanes {
		role := lane.role
		promptRoles := []string{}
		if role == "executor" {
			promptRoles = []string{"executor"}
		} else {
			promptRoles = []string{role}
		}
		out = append(out, githubPipelineLane{
			Alias:       lane.alias,
			Role:        role,
			PromptRoles: promptRoles,
			Activation:  "bootstrap",
			Phase:       "impl",
			Mode:        lane.mode,
			Owner:       lane.owner,
			Blocking:    lane.blocking,
			Purpose:     lane.purpose,
		})
	}
	return out
}

func buildGithubStartInstructions(manifest githubWorkManifest) string {
	lines := []string{
		"# NANA Work-on Start",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo: %s", manifest.RepoSlug),
		fmt.Sprintf("Target: %s #%d", manifest.TargetKind, manifest.TargetNumber),
		fmt.Sprintf("Sandbox path: %s", manifest.SandboxPath),
		fmt.Sprintf("Repo checkout path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Reviewer sync user: %s", manifest.ReviewReviewer),
		"",
	}
	lines = append(lines, buildGithubConsiderationInstructionLines(manifest.ConsiderationsActive, manifest.RoleLayout)...)
	return strings.Join(lines, "\n") + "\n"
}
