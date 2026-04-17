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

func syncGithubWork(options githubWorkSyncOptions) error {
	manifestPath, _, err := resolveGithubRunManifestPath(options.RunID, options.UseLast)
	if err != nil {
		return err
	}
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		return err
	}
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = manifest.APIBaseURL
	}
	if strings.TrimSpace(apiBaseURL) == "" {
		apiBaseURL = "https://api.github.com"
	}
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
	actors, err := buildGithubControlPlaneReviewers(manifest, reviewer, apiBaseURL, token)
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
	newFeedback := filterGithubNewFeedback(snapshot, manifest)
	if len(newFeedback.IssueComments) == 0 && len(newFeedback.Reviews) == 0 && len(newFeedback.ReviewComments) == 0 {
		manifest.NextAction = defaultString(manifest.NextAction, "continue")
		if manifest.NeedsHuman {
			manifest.NextAction = "wait_for_github_feedback"
		}
		_ = writeGithubJSON(manifestPath, manifest)
		fmt.Fprintf(os.Stdout, "[github] No new feedback from %s for %s %s #%d.\n", formatGithubActorSet(actors), manifest.RepoSlug, manifest.TargetKind, manifest.TargetNumber)
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
	runDir := filepath.Dir(manifestPath)
	feedbackInstructionsPath := filepath.Join(runDir, "feedback-instructions.md")
	if err := os.WriteFile(feedbackInstructionsPath, []byte(buildGithubFeedbackInstructions(manifest, actors, newFeedback)), 0o644); err != nil {
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

	laneCodexHome, err := ensureGithubLaneCodexHome(manifest.SandboxPath, "leader")
	if err != nil {
		return err
	}

	prompt := fmt.Sprintf("Continue GitHub %s #%d for %s after new reviewer feedback", manifest.TargetKind, manifest.TargetNumber, manifest.RepoSlug)
	finalPrompt := buildGithubFeedbackInstructions(manifest, actors, newFeedback) + "\n\nTask:\n" + prompt
	normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(options.CodexArgs)
	finalPrompt = prefixCodexFastPrompt(finalPrompt, fastMode)
	result, runErr := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       manifest.SandboxPath,
		InstructionsRoot: manifest.SandboxPath,
		CodexHome:        laneCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", manifest.SandboxRepoPath},
		CommonArgs:       normalizedCodexArgs,
		Prompt:           finalPrompt,
		PromptTransport:  codexPromptTransportArg,
		CheckpointPath:   filepath.Join(runDir, "leader-checkpoint.json"),
		StepKey:          "github-leader",
		ResumeStrategy:   codexResumeConversation,
		Env:              append(buildGithubCodexEnv(NotifyTempContract{}, laneCodexHome, apiBaseURL), "NANA_PROJECT_AGENTS_ROOT="+manifest.SandboxRepoPath),
	})

	fmt.Fprintf(os.Stdout, "[github] Stored new feedback for run %s.\n", manifest.RunID)
	fmt.Fprintf(os.Stdout, "[github] Feedback file: %s\n", feedbackInstructionsPath)
	fmt.Fprintf(os.Stdout, "[github] Feedback actors: %s\n", formatGithubActorSet(actors))
	if strings.TrimSpace(result.Stdout) != "" {
		fmt.Fprint(os.Stdout, result.Stdout)
	}
	if strings.TrimSpace(result.Stderr) != "" {
		fmt.Fprint(os.Stdout, result.Stderr)
	}
	return runErr
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
