package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type githubActor struct {
	Login string `json:"login"`
}

type githubIssueCommentPayload struct {
	ID        int         `json:"id"`
	HTMLURL   string      `json:"html_url"`
	Body      string      `json:"body"`
	UpdatedAt string      `json:"updated_at"`
	User      githubActor `json:"user"`
}

type githubFeedbackSnapshot struct {
	Actors         []string
	IgnoredActors  map[string]int
	IssueComments  []githubIssueCommentPayload
	Reviews        []githubPullReviewPayload
	ReviewComments []githubPullReviewCommentPayload
}

type githubFeedbackResumeState struct {
	Version           int                    `json:"version"`
	Actors            []string               `json:"actors,omitempty"`
	NewFeedback       githubFeedbackSnapshot `json:"new_feedback"`
	FeedbackTargetURL string                 `json:"feedback_target_url,omitempty"`
	PromptFingerprint string                 `json:"prompt_fingerprint,omitempty"`
	UpdatedAt         string                 `json:"updated_at,omitempty"`
}

func syncGithubWork(options githubWorkSyncOptions) error {
	manifestPath, _, err := resolveGithubRunManifestPath(options.RunID, options.UseLast)
	if err != nil {
		return err
	}
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		return err
	}
	if _, err := captureGithubWorkBaselineIfMissing(manifestPath, &manifest); err != nil {
		return err
	}
	runDir := filepath.Dir(manifestPath)
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = manifest.APIBaseURL
	}
	if strings.TrimSpace(apiBaseURL) == "" {
		apiBaseURL = "https://api.github.com"
	}
	resumeStatePath := githubFeedbackResumeStatePath(runDir)
	actors := []string{}
	newFeedback := githubFeedbackSnapshot{}
	finalPrompt := ""
	if options.ResumeLast {
		resumeState, err := readGithubFeedbackResumeState(resumeStatePath)
		if err != nil {
			return err
		}
		actors = append([]string{}, resumeState.Actors...)
		newFeedback = resumeState.NewFeedback
		manifest.ControlPlaneReviewers = append([]string{}, actors...)
		finalPrompt, err = validateGithubFeedbackResumeState(manifest, runDir, resumeState, options.FeedbackTargetURL, options.CodexArgs)
		if err != nil {
			return err
		}
	} else {
		token, err := resolveGithubToken()
		if err != nil {
			return err
		}
		reviewer := strings.TrimSpace(options.Reviewer)
		if reviewer == "" && manifest.Policy == nil && len(manifest.ControlPlaneReviewers) == 0 {
			reviewer = strings.TrimSpace(manifest.ReviewReviewer)
		}
		if reviewer == "" && manifest.Policy == nil && len(manifest.ControlPlaneReviewers) == 0 {
			reviewer = "@me"
		}
		if reviewer == "@me" {
			var viewer struct {
				Login string `json:"login"`
			}
			if err := githubAPIGetJSON(apiBaseURL, token, "/user", &viewer); err == nil && strings.TrimSpace(viewer.Login) != "" {
				reviewer = viewer.Login
			}
		}
		reviewer = strings.TrimPrefix(strings.TrimSpace(reviewer), "@")
		actors, err = buildGithubControlPlaneReviewers(manifest, reviewer, apiBaseURL, token)
		if err != nil {
			return err
		}
		if len(actors) == 0 && reviewer != "" {
			actors = []string{strings.ToLower(reviewer)}
		}
		manifest.ControlPlaneReviewers = append([]string{}, actors...)

		snapshot, err := fetchGithubFeedbackSnapshot(manifest, actors, apiBaseURL, token, options.FeedbackTargetURL)
		if err != nil {
			return err
		}
		manifest.IgnoredFeedbackActors = cloneGithubIgnoredActorMap(snapshot.IgnoredActors)
		newFeedback = filterGithubNewFeedback(snapshot, manifest)
		if len(newFeedback.IssueComments) == 0 && len(newFeedback.Reviews) == 0 && len(newFeedback.ReviewComments) == 0 {
			manifest.NextAction = defaultString(manifest.NextAction, "continue")
			if manifest.NeedsHuman {
				manifest.NextAction = "wait_for_github_feedback"
			}
			_ = writeGithubJSON(manifestPath, manifest)
			fmt.Fprintf(currentWorkStdout(), "[github] No new feedback from %s for %s %s #%d.\n", formatGithubActorSet(actors), manifest.RepoSlug, manifest.TargetKind, manifest.TargetNumber)
			return nil
		}
		cursor := advanceGithubFeedbackCursor(snapshot, manifest)
		manifest.LastSeenIssueCommentID = cursor.issueCommentID
		manifest.LastSeenReviewID = cursor.reviewID
		manifest.LastSeenReviewCommentID = cursor.reviewCommentID
		manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		manifest.NeedsHuman = false
		manifest.NeedsHumanReason = ""
		manifest.NextAction = "continue_after_feedback"
		if err := writeGithubJSON(manifestPath, manifest); err != nil {
			return err
		}
		if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
			return err
		}
		feedbackInstructions := buildGithubFeedbackInstructions(manifest, actors, newFeedback)
		prompt := buildGithubFeedbackContinuationPrompt(manifest, feedbackInstructions)
		if err := writeGithubJSON(resumeStatePath, githubFeedbackResumeState{
			Version:           1,
			Actors:            append([]string{}, actors...),
			NewFeedback:       newFeedback,
			FeedbackTargetURL: options.FeedbackTargetURL,
			PromptFingerprint: githubFeedbackResumeFingerprint(prompt, options.CodexArgs),
			UpdatedAt:         manifest.UpdatedAt,
		}); err != nil {
			return err
		}
		if err := writeGithubJSON(filepath.Join(runDir, "feedback-snapshot.json"), snapshot); err != nil {
			return err
		}
		if err := writeGithubJSON(filepath.Join(runDir, "feedback-summary.json"), map[string]any{
			"actors":                actors,
			"ignored_actors":        snapshot.IgnoredActors,
			"new_issue_comments":    len(newFeedback.IssueComments),
			"new_reviews":           len(newFeedback.Reviews),
			"new_review_comments":   len(newFeedback.ReviewComments),
			"issue_comment_cursor":  manifest.LastSeenIssueCommentID,
			"review_cursor":         manifest.LastSeenReviewID,
			"review_comment_cursor": manifest.LastSeenReviewCommentID,
		}); err != nil {
			return err
		}
	}
	feedbackInstructionsPath := filepath.Join(runDir, "feedback-instructions.md")
	feedbackInstructions := buildGithubFeedbackInstructions(manifest, actors, newFeedback)
	if err := os.WriteFile(feedbackInstructionsPath, []byte(feedbackInstructions), 0o644); err != nil {
		return err
	}
	sandboxLock, err := acquireSandboxWriteLock(manifest.SandboxRepoPath, repoAccessLockOwner{
		Backend: "github-work",
		RunID:   manifest.RunID,
		Purpose: "leader-feedback-sync",
		Label:   "github-work-sync",
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = sandboxLock.Release()
	}()

	laneCodexHome, err := ensureGithubLaneCodexHome(manifest.SandboxPath, "leader")
	if err != nil {
		return err
	}

	if finalPrompt == "" {
		finalPrompt = buildGithubFeedbackContinuationPrompt(manifest, feedbackInstructions)
	}
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
		UsageRunID:       manifest.RunID,
		UsageRepoSlug:    manifest.RepoSlug,
		UsageBackend:     "github",
		UsageSandboxPath: manifest.SandboxPath,
		RecoverySpec:     githubWorkManagedPromptRecoverySpec(manifest, runDir, managedPromptResumeArgv([]string{"work", "sync", "--run-id", manifest.RunID, "--resume-last"}, options.CodexArgs)),
		Env:              append(buildGithubCodexEnv(NotifyTempContract{}, laneCodexHome, apiBaseURL), "NANA_PROJECT_AGENTS_ROOT="+manifest.SandboxRepoPath),
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
	completionErr := error(nil)
	if runErr == nil {
		completionErr = runGithubWorkFollowupLoop(manifestPath, runDir, &manifest, options.CodexArgs)
	}
	if pauseErr, ok := isCodexRateLimitPauseError(runErr); ok {
		manifest.ExecutionStatus = "paused"
		manifest.PauseReason = strings.TrimSpace(pauseErr.Info.Reason)
		manifest.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
		manifest.PausedAt = manifest.UpdatedAt
		manifest.LastError = codexPauseInfoMessage(pauseErr.Info)
	} else if runErr != nil {
		manifest.ExecutionStatus = "failed"
		manifest.LastError = runErr.Error()
	} else if pauseErr, ok := isCodexRateLimitPauseError(completionErr); ok {
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
	if runErr == nil && completionErr == nil {
		if err := os.Remove(resumeStatePath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	fmt.Fprintf(currentWorkStdout(), "[github] Stored new feedback for run %s.\n", manifest.RunID)
	fmt.Fprintf(currentWorkStdout(), "[github] Feedback file: %s\n", feedbackInstructionsPath)
	fmt.Fprintf(currentWorkStdout(), "[github] Feedback actors: %s\n", formatGithubActorSet(actors))
	if strings.TrimSpace(result.Stdout) != "" {
		fmt.Fprint(currentWorkStdout(), result.Stdout)
	}
	if strings.TrimSpace(result.Stderr) != "" {
		fmt.Fprint(currentWorkStdout(), result.Stderr)
	}
	if runErr != nil {
		return runErr
	}
	return completionErr
}

func fetchGithubFeedbackSnapshot(manifest githubWorkManifest, reviewers []string, apiBaseURL string, token string, targetOverrideURL string) (githubFeedbackSnapshot, error) {
	effectiveIssueNumber := manifest.TargetNumber
	effectivePRNumber := 0
	if manifest.TargetKind == "pr" {
		effectivePRNumber = manifest.TargetNumber
	} else {
		effectivePRNumber = manifest.PublishedPRNumber
	}
	if strings.TrimSpace(targetOverrideURL) != "" {
		target, err := parseGithubTargetURL(targetOverrideURL)
		if err != nil {
			return githubFeedbackSnapshot{}, err
		}
		if target.kind == "issue" {
			effectiveIssueNumber = target.number
		} else {
			effectivePRNumber = target.number
		}
	}
	var issueComments []githubIssueCommentPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", manifest.RepoSlug, effectiveIssueNumber), &issueComments); err != nil {
		return githubFeedbackSnapshot{}, err
	}
	filteredIssueComments := []githubIssueCommentPayload{}
	ignoredActors := map[string]int{}
	for _, comment := range issueComments {
		if ok, reason := githubFeedbackActorAllowed(manifest, comment.User.Login, reviewers); ok {
			filteredIssueComments = append(filteredIssueComments, comment)
		} else if reason != "" {
			ignoredActors[reason]++
		}
	}
	if effectivePRNumber == 0 {
		return githubFeedbackSnapshot{Actors: append([]string{}, reviewers...), IgnoredActors: ignoredActors, IssueComments: filteredIssueComments}, nil
	}
	var reviews []githubPullReviewPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", manifest.RepoSlug, effectivePRNumber), &reviews); err != nil {
		return githubFeedbackSnapshot{}, err
	}
	var reviewComments []githubPullReviewCommentPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100", manifest.RepoSlug, effectivePRNumber), &reviewComments); err != nil {
		return githubFeedbackSnapshot{}, err
	}
	filteredReviews := []githubPullReviewPayload{}
	for _, review := range reviews {
		if ok, reason := githubFeedbackActorAllowed(manifest, review.User.Login, reviewers); ok {
			filteredReviews = append(filteredReviews, review)
		} else if reason != "" {
			ignoredActors[reason]++
		}
	}
	filteredReviewComments := []githubPullReviewCommentPayload{}
	for _, comment := range reviewComments {
		if ok, reason := githubFeedbackActorAllowed(manifest, comment.User.Login, reviewers); ok {
			filteredReviewComments = append(filteredReviewComments, comment)
		} else if reason != "" {
			ignoredActors[reason]++
		}
	}
	return githubFeedbackSnapshot{
		Actors:         append([]string{}, reviewers...),
		IgnoredActors:  ignoredActors,
		IssueComments:  filteredIssueComments,
		Reviews:        filteredReviews,
		ReviewComments: filteredReviewComments,
	}, nil
}

func githubFeedbackResumeStatePath(runDir string) string {
	return filepath.Join(runDir, "feedback-resume.json")
}

func readGithubFeedbackResumeState(path string) (githubFeedbackResumeState, error) {
	var state githubFeedbackResumeState
	if err := readGithubJSON(path, &state); err != nil {
		if os.IsNotExist(err) {
			return githubFeedbackResumeState{}, fmt.Errorf("work sync --resume-last requires a stored feedback resume artifact")
		}
		return githubFeedbackResumeState{}, err
	}
	return state, nil
}

func validateGithubFeedbackResumeState(manifest githubWorkManifest, runDir string, state githubFeedbackResumeState, feedbackTargetOverride string, codexArgs []string) (string, error) {
	if len(state.Actors) == 0 || (len(state.NewFeedback.IssueComments) == 0 && len(state.NewFeedback.Reviews) == 0 && len(state.NewFeedback.ReviewComments) == 0) {
		return "", fmt.Errorf("work sync --resume-last requires a stored feedback resume artifact with actors and pending feedback")
	}
	if strings.EqualFold(strings.TrimSpace(manifest.ExecutionStatus), "completed") && strings.TrimSpace(state.UpdatedAt) != "" {
		return "", fmt.Errorf("work sync --resume-last stored feedback artifact is stale because the run is already completed")
	}
	if strings.TrimSpace(feedbackTargetOverride) != "" && strings.TrimSpace(state.FeedbackTargetURL) != "" && strings.TrimSpace(feedbackTargetOverride) != strings.TrimSpace(state.FeedbackTargetURL) {
		return "", fmt.Errorf("work sync --resume-last target override does not match the stored feedback resume artifact")
	}
	checkpoint, err := readCodexStepCheckpoint(filepath.Join(runDir, "leader-checkpoint.json"))
	if err != nil {
		return "", fmt.Errorf("work sync --resume-last requires a leader checkpoint: %w", err)
	}
	if strings.TrimSpace(checkpoint.SessionID) == "" || !checkpoint.ResumeEligible {
		return "", fmt.Errorf("work sync --resume-last requires a resumable leader checkpoint")
	}
	prompt := buildGithubFeedbackContinuationPrompt(manifest, buildGithubFeedbackInstructions(manifest, state.Actors, state.NewFeedback))
	fingerprint := githubFeedbackResumeFingerprint(prompt, codexArgs)
	if strings.TrimSpace(state.PromptFingerprint) == "" || strings.TrimSpace(state.PromptFingerprint) != fingerprint {
		return "", fmt.Errorf("work sync --resume-last stored feedback artifact is stale or inconsistent with the current feedback prompt")
	}
	if strings.TrimSpace(checkpoint.PromptFingerprint) != "" && strings.TrimSpace(checkpoint.PromptFingerprint) != fingerprint {
		return "", fmt.Errorf("work sync --resume-last leader checkpoint does not match the stored feedback artifact")
	}
	return prompt, nil
}

func buildGithubFeedbackContinuationPrompt(manifest githubWorkManifest, feedbackInstructions string) string {
	return feedbackInstructions + "\n\nTask:\n" + fmt.Sprintf("Continue GitHub %s #%d for %s after new reviewer feedback", manifest.TargetKind, manifest.TargetNumber, manifest.RepoSlug)
}

func githubFeedbackResumeFingerprint(prompt string, codexArgs []string) string {
	_, fastMode := NormalizeCodexLaunchArgsWithFast(codexArgs)
	return sha256Hex(strings.TrimSpace(prefixCodexFastPrompt(prompt, fastMode)))
}

func filterGithubNewFeedback(snapshot githubFeedbackSnapshot, manifest githubWorkManifest) githubFeedbackSnapshot {
	filtered := githubFeedbackSnapshot{}
	for _, comment := range snapshot.IssueComments {
		if comment.ID > manifest.LastSeenIssueCommentID {
			filtered.IssueComments = append(filtered.IssueComments, comment)
		}
	}
	for _, review := range snapshot.Reviews {
		if review.ID > manifest.LastSeenReviewID {
			filtered.Reviews = append(filtered.Reviews, review)
		}
	}
	for _, comment := range snapshot.ReviewComments {
		if comment.ID > manifest.LastSeenReviewCommentID {
			filtered.ReviewComments = append(filtered.ReviewComments, comment)
		}
	}
	return filtered
}

type githubFeedbackCursor struct {
	issueCommentID  int
	reviewID        int
	reviewCommentID int
}

func advanceGithubFeedbackCursor(snapshot githubFeedbackSnapshot, manifest githubWorkManifest) githubFeedbackCursor {
	cursor := githubFeedbackCursor{
		issueCommentID:  manifest.LastSeenIssueCommentID,
		reviewID:        manifest.LastSeenReviewID,
		reviewCommentID: manifest.LastSeenReviewCommentID,
	}
	for _, comment := range snapshot.IssueComments {
		if comment.ID > cursor.issueCommentID {
			cursor.issueCommentID = comment.ID
		}
	}
	for _, review := range snapshot.Reviews {
		if review.ID > cursor.reviewID {
			cursor.reviewID = review.ID
		}
	}
	for _, comment := range snapshot.ReviewComments {
		if comment.ID > cursor.reviewCommentID {
			cursor.reviewCommentID = comment.ID
		}
	}
	return cursor
}

func buildGithubFeedbackInstructions(manifest githubWorkManifest, reviewers []string, feedback githubFeedbackSnapshot) string {
	lines := []string{
		"# NANA Work Feedback",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo: %s", manifest.RepoSlug),
		fmt.Sprintf("Sandbox path: %s", manifest.SandboxPath),
		fmt.Sprintf("Repo checkout path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Target: %s #%d", manifest.TargetKind, manifest.TargetNumber),
		fmt.Sprintf("URL: %s", manifest.TargetURL),
		fmt.Sprintf("Reviewers: %s", formatGithubActorSet(reviewers)),
		"",
	}
	lines = append(lines, buildGithubRuntimeContextLines(manifest)...)
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	issueComments := append([]githubIssueCommentPayload(nil), feedback.IssueComments...)
	slices.SortFunc(issueComments, func(left githubIssueCommentPayload, right githubIssueCommentPayload) int {
		return right.ID - left.ID
	})
	reviews := append([]githubPullReviewPayload(nil), feedback.Reviews...)
	slices.SortFunc(reviews, func(left githubPullReviewPayload, right githubPullReviewPayload) int {
		return right.ID - left.ID
	})
	reviewComments := append([]githubPullReviewCommentPayload(nil), feedback.ReviewComments...)
	slices.SortFunc(reviewComments, func(left githubPullReviewCommentPayload, right githubPullReviewCommentPayload) int {
		return right.ID - left.ID
	})
	renderIssueComment := func(comment githubIssueCommentPayload) {
		lines = append(lines,
			fmt.Sprintf("## Issue comment %d", comment.ID),
			"",
			fmt.Sprintf("Author: %s", comment.User.Login),
			compactPromptHeadValue(comment.Body, 0, githubFeedbackBodyCharLimit),
			fmt.Sprintf("Link: %s", comment.HTMLURL),
			"",
		)
	}
	for _, comment := range limitPromptList(issueComments, githubFeedbackIssueCommentLimit) {
		renderIssueComment(comment)
	}
	if len(issueComments) > githubFeedbackIssueCommentLimit {
		lines = append(lines, fmt.Sprintf("- ... %d older issue comments omitted", len(issueComments)-githubFeedbackIssueCommentLimit), "")
	}
	for _, review := range limitPromptList(reviews, githubFeedbackReviewLimit) {
		lines = append(lines,
			fmt.Sprintf("## Review %d", review.ID),
			"",
			fmt.Sprintf("Author: %s", review.User.Login),
			fmt.Sprintf("State: %s", review.State),
			compactPromptHeadValue(strings.TrimSpace(review.Body), 0, githubFeedbackBodyCharLimit),
			fmt.Sprintf("Link: %s", review.HTMLURL),
			"",
		)
	}
	if len(reviews) > githubFeedbackReviewLimit {
		lines = append(lines, fmt.Sprintf("- ... %d older reviews omitted", len(reviews)-githubFeedbackReviewLimit), "")
	}
	for _, comment := range limitPromptList(reviewComments, githubFeedbackReviewCommentLimit) {
		reference := comment.Path
		if comment.Line > 0 {
			reference = fmt.Sprintf("%s:%d", comment.Path, comment.Line)
		} else if comment.OriginalLine > 0 {
			reference = fmt.Sprintf("%s:%d", comment.Path, comment.OriginalLine)
		}
		lines = append(lines,
			fmt.Sprintf("## Review comment %d", comment.ID),
			"",
			fmt.Sprintf("Author: %s", comment.User.Login),
			fmt.Sprintf("Path: %s", reference),
			compactPromptHeadValue(strings.TrimSpace(comment.Body), 0, githubFeedbackBodyCharLimit),
			fmt.Sprintf("Link: %s", comment.HTMLURL),
			"",
		)
	}
	if len(reviewComments) > githubFeedbackReviewCommentLimit {
		lines = append(lines, fmt.Sprintf("- ... %d older review comments omitted", len(reviewComments)-githubFeedbackReviewCommentLimit), "")
	}
	return capPromptChars(strings.Join(lines, "\n")+"\n", githubFeedbackInstructionCharLimit)
}

// githubFeedbackActorAllowed keeps wildcard feedback useful without letting authors,
// bots, or explicitly blocked reviewers become the human control plane.
func githubFeedbackActorAllowed(manifest githubWorkManifest, login string, reviewers []string) (bool, string) {
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(login, "@")))
	if normalized == "" {
		return false, "empty"
	}
	reviewerPolicy := normalizeGithubReviewerPolicy(manifest.EffectiveReviewerPolicy)
	for _, blocked := range reviewerPolicy.GetBlocked() {
		if strings.EqualFold(normalized, blocked) {
			return false, "blocked"
		}
	}
	for _, reviewer := range reviewers {
		if reviewer == "*" {
			if strings.Contains(normalized, "[bot]") || strings.HasSuffix(normalized, "-bot") || normalized == "dependabot" {
				return false, "bot"
			}
			if author := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(manifest.TargetAuthor, "@"))); author != "" && normalized == author {
				return false, "author"
			}
			return true, ""
		}
		if strings.EqualFold(normalized, strings.TrimSpace(strings.TrimPrefix(reviewer, "@"))) {
			return true, ""
		}
	}
	return false, "not_in_control_plane"
}
