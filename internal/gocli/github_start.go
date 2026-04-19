package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func startGithubWork(options githubWorkStartOptions) (githubWorkManifest, error) {
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return githubWorkManifest{}, err
	}
	reviewer := strings.TrimSpace(options.Reviewer)
	if reviewer == "@me" {
		var viewer struct {
			Login string `json:"login"`
		}
		if err := githubAPIGetJSON(apiBaseURL, token, "/user", &viewer); err == nil && strings.TrimSpace(viewer.Login) != "" {
			reviewer = viewer.Login
		}
	}
	target, err := githubFetchTargetContext(options.Target, apiBaseURL, token)
	if err != nil {
		return githubWorkManifest{}, err
	}
	now := time.Now().UTC()
	paths := githubManagedPaths(options.Target.repoSlug)
	repoMeta, err := ensureGithubManagedRepoMetadata(paths, target, now)
	if err != nil {
		return githubWorkManifest{}, err
	}
	if err := ensureGithubSourceClone(paths, repoMeta); err != nil {
		return githubWorkManifest{}, err
	}

	repoVerificationPlan := detectGithubVerificationPlan(paths.SourcePath)
	if err := writeGithubJSON(paths.RepoVerificationPlanPath, repoVerificationPlan); err != nil {
		return githubWorkManifest{}, err
	}
	trackedFiles := trackedRepoFiles(paths.SourcePath)
	settings, _ := readGithubRepoSettings(paths.RepoSettingsPath)
	if settings == nil {
		inferred := inferGithubInitialRepoConsiderationsFromFiles(trackedFiles, repoMeta.RepoSlug, repoVerificationPlan)
		settings = &githubRepoSettings{
			Version:               4,
			DefaultConsiderations: inferred.Considerations,
			DefaultRoleLayout:     "split",
			UpdatedAt:             now.Format(time.RFC3339),
		}
	}
	settings.HotPathAPIProfile = inferGithubHotPathProfileFromFiles(trackedFiles, now)
	settings.UpdatedAt = now.Format(time.RFC3339)
	if settings.DefaultRoleLayout == "" {
		settings.DefaultRoleLayout = "split"
	}
	if settings.Version == 0 {
		settings.Version = 4
	}
	if err := writeGithubJSON(paths.RepoSettingsPath, settings); err != nil {
		return githubWorkManifest{}, err
	}
	repoMode := normalizeGithubRepoMode(options.RepoMode)
	publishTarget := normalizeGithubPublishTarget(options.PublishTarget)
	if repoMode == "" && publishTarget != "" {
		repoMode = publishTargetToRepoMode(publishTarget)
	}
	if repoMode == "" {
		if options.CreatePRExplicit {
			if options.CreatePR {
				repoMode = "repo"
			} else {
				repoMode = "local"
			}
		} else {
			repoMode = resolvedGithubRepoMode(settings)
		}
	}
	if repoMode == "disabled" {
		return githubWorkManifest{}, fmt.Errorf("repo %s is configured with repo-mode disabled; change it with `nana repo config %s --repo-mode <local|fork|repo>` or pass --repo-mode for this run", repoMeta.RepoSlug, repoMeta.RepoSlug)
	}
	if publishTarget == "" {
		publishTarget = repoModeToPublishTarget(repoMode)
	}
	if publishTarget == "" {
		publishTarget = "local-branch"
		repoMode = "local"
	}
	prForwardMode := resolvedGithubPRForwardMode(settings)
	createPROnComplete := publishTarget != "local-branch"
	profile, profilePath, err := refreshGithubRepoProfile(repoMeta.RepoSlug, paths.SourcePath, repoVerificationPlan, settings.DefaultConsiderations, now)
	if err != nil {
		return githubWorkManifest{}, err
	}
	policy, err := resolveGithubWorkPolicy(paths.SourcePath)
	if err != nil {
		return githubWorkManifest{}, err
	}
	effectiveReviewerPolicy := resolveGithubEffectiveReviewerPolicy(repoMeta.RepoSlug)

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
		return githubWorkManifest{}, err
	}
	if err := cloneGithubSourceToSandbox(paths.SourcePath, sandboxRepoPath); err != nil {
		return githubWorkManifest{}, err
	}
	verificationPlan := detectGithubVerificationPlan(sandboxRepoPath)
	runDir := filepath.Join(paths.RepoRoot, "runs", runID)
	verificationScriptsDir, err := writeGithubVerificationScripts(sandboxPath, sandboxRepoPath, verificationPlan, runID)
	if err != nil {
		return githubWorkManifest{}, err
	}
	pipeline := buildGithubPipeline(activeConsiderations, roleLayout)
	convertedPipeline := convertGithubLanes(pipeline)
	manifest := githubWorkManifest{
		Version:                 4,
		RunID:                   runID,
		CreatedAt:               now.Format(time.RFC3339),
		UpdatedAt:               now.Format(time.RFC3339),
		ExecutionStatus:         "running",
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
		CreatePROnComplete:      createPROnComplete,
		RepoMode:                repoMode,
		PRForwardMode:           prForwardMode,
		PublishTarget:           publishTarget,
		TargetKind:              options.Target.kind,
		TargetNumber:            options.Target.number,
		TargetTitle:             target.Issue.Title,
		TargetURL:               githubCanonicalTargetURL(options.Target),
		TargetState:             target.Issue.State,
		TargetAuthor:            target.Issue.User.Login,
		ReviewReviewer:          reviewer,
		EffectiveReviewerPolicy: effectiveReviewerPolicy,
		APIBaseURL:              apiBaseURL,
		DefaultBranch:           repoMeta.DefaultBranch,
		LastSeenIssueCommentID:  0,
		LastSeenReviewID:        0,
		LastSeenReviewCommentID: 0,
		Policy:                  &policy,
		RepoProfilePath:         profilePath,
		RepoProfile:             profile,
		RepoProfileFingerprint:  profileFingerprint(profile),
		MergeMethod:             githubEffectiveMergeMethod(&policy),
		MergeState:              "not_attempted",
	}
	reviewerOverride := ""
	if raw := strings.TrimSpace(options.Reviewer); raw != "" && raw != "@me" {
		reviewerOverride = reviewer
	}
	manifest.ControlPlaneReviewers, err = buildGithubControlPlaneReviewers(manifest, reviewerOverride, apiBaseURL, token)
	if err != nil {
		return githubWorkManifest{}, err
	}
	manifest.NeedsHuman, manifest.NeedsHumanReason, manifest.NextAction = determineGithubHumanGateState(manifest.Policy, manifest.CreatePROnComplete)
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return githubWorkManifest{}, err
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		return githubWorkManifest{}, err
	}
	startInstructionsPath := filepath.Join(runDir, "start-instructions.md")
	if err := os.WriteFile(startInstructionsPath, []byte(buildGithubStartInstructions(manifest)), 0o644); err != nil {
		return githubWorkManifest{}, err
	}
	if err := writeGithubJSON(filepath.Join(paths.RepoRoot, "latest-run.json"), map[string]string{"repo_root": paths.RepoRoot, "run_id": runID}); err != nil {
		return githubWorkManifest{}, err
	}
	if err := writeGithubJSON(githubWorkLatestRunPath(), map[string]string{"repo_root": paths.RepoRoot, "run_id": runID}); err != nil {
		return githubWorkManifest{}, err
	}

	laneCodexHome, err := ensureGithubLaneCodexHome(sandboxPath, "leader")
	if err != nil {
		return githubWorkManifest{}, err
	}
	prompt := fmt.Sprintf("Implement GitHub %s #%d for %s", options.Target.kind, options.Target.number, options.Target.repoSlug)
	finalPrompt := buildGithubStartInstructions(manifest) + "\n\nTask:\n" + prompt
	normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(options.CodexArgs)
	finalPrompt = prefixCodexFastPrompt(finalPrompt, fastMode)
	transport := promptTransportForSize(finalPrompt, structuredPromptStdinThreshold)
	result, runErr := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       sandboxPath,
		InstructionsRoot: sandboxPath,
		CodexHome:        laneCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", sandboxRepoPath},
		CommonArgs:       normalizedCodexArgs,
		Prompt:           finalPrompt,
		PromptTransport:  transport,
		CheckpointPath:   filepath.Join(runDir, "leader-checkpoint.json"),
		StepKey:          "github-leader",
		ResumeStrategy:   codexResumeConversation,
		Env:              append(buildGithubCodexEnv(NotifyTempContract{}, laneCodexHome, apiBaseURL), "NANA_PROJECT_AGENTS_ROOT="+sandboxRepoPath),
		RateLimitPolicy:  codexRateLimitPolicyDefault(options.RateLimitPolicy),
		OnPause: func(info codexRateLimitPauseInfo) {
			manifest.ExecutionStatus = "paused"
			manifest.PauseReason = strings.TrimSpace(info.Reason)
			manifest.PauseUntil = strings.TrimSpace(info.RetryAfter)
			manifest.PausedAt = ISOTimeNow()
			manifest.LastError = codexPauseInfoMessage(info)
			manifest.UpdatedAt = manifest.PausedAt
			_ = writeGithubJSON(manifestPath, manifest)
			_ = indexGithubWorkRunManifest(manifestPath, manifest)
		},
		OnResume: func(info codexRateLimitPauseInfo) {
			manifest.ExecutionStatus = "running"
			manifest.PauseReason = ""
			manifest.PauseUntil = ""
			manifest.PausedAt = ""
			manifest.LastError = ""
			manifest.UpdatedAt = ISOTimeNow()
			_ = writeGithubJSON(manifestPath, manifest)
			_ = indexGithubWorkRunManifest(manifestPath, manifest)
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
		return githubWorkManifest{}, err
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		return githubWorkManifest{}, err
	}

	fmt.Fprintf(os.Stdout, "[github] Starting run %s for %s %s #%d\n", runID, manifest.RepoSlug, manifest.TargetKind, manifest.TargetNumber)
	fmt.Fprintf(os.Stdout, "[github] Managed repo root: %s\n", paths.RepoRoot)
	fmt.Fprintf(os.Stdout, "[github] Managed sandbox: %s -> %s\n", sandboxID, sandboxPath)
	fmt.Fprintf(os.Stdout, "[github] Managed repo checkout: %s\n", sandboxRepoPath)
	fmt.Fprintf(os.Stdout, "[github] Reviewer sync user: %s\n", options.Reviewer)
	if strings.TrimSpace(result.Stdout) != "" {
		fmt.Fprint(os.Stdout, result.Stdout)
	}
	if strings.TrimSpace(result.Stderr) != "" {
		fmt.Fprint(os.Stdout, result.Stderr)
	}
	return manifest, runErr
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
		"# NANA Work Start",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo: %s", manifest.RepoSlug),
		fmt.Sprintf("Target: %s #%d", manifest.TargetKind, manifest.TargetNumber),
		fmt.Sprintf("Sandbox path: %s", manifest.SandboxPath),
		fmt.Sprintf("Repo checkout path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Reviewer sync user: %s", manifest.ReviewReviewer),
		"",
	}
	lines = append(lines, buildGithubRuntimeContextLines(manifest)...)
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	lines = append(lines, buildGithubConsiderationInstructionLines(manifest.ConsiderationsActive, manifest.RoleLayout)...)
	return capPromptChars(strings.Join(lines, "\n")+"\n", githubInstructionCharLimit)
}
