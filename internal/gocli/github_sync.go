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
	IssueComments  []githubIssueCommentPayload
	Reviews        []githubPullReviewPayload
	ReviewComments []githubPullReviewCommentPayload
}

func syncGithubWorkOn(options githubWorkOnSyncOptions) error {
	manifestPath, _, err := resolveGithubRunManifestPath(options.RunID, options.UseLast)
	if err != nil {
		return err
	}
	manifest, err := readGithubWorkonManifest(manifestPath)
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
	if reviewer == "" {
		reviewer = strings.TrimSpace(manifest.ReviewReviewer)
	}
	if reviewer == "" {
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

	snapshot, err := fetchGithubFeedbackSnapshot(manifest, reviewer, apiBaseURL, token, options.FeedbackTargetURL)
	if err != nil {
		return err
	}
	newFeedback := filterGithubNewFeedback(snapshot, manifest)
	if len(newFeedback.IssueComments) == 0 && len(newFeedback.Reviews) == 0 && len(newFeedback.ReviewComments) == 0 {
		fmt.Fprintf(os.Stdout, "[github] No new feedback from @%s for %s %s #%d.\n", reviewer, manifest.RepoSlug, manifest.TargetKind, manifest.TargetNumber)
		return nil
	}
	cursor := advanceGithubFeedbackCursor(snapshot, manifest)
	manifest.LastSeenIssueCommentID = cursor.issueCommentID
	manifest.LastSeenReviewID = cursor.reviewID
	manifest.LastSeenReviewCommentID = cursor.reviewCommentID
	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	runDir := filepath.Dir(manifestPath)
	feedbackInstructionsPath := filepath.Join(runDir, "feedback-instructions.md")
	if err := os.WriteFile(feedbackInstructionsPath, []byte(buildGithubFeedbackInstructions(manifest, reviewer, newFeedback)), 0o644); err != nil {
		return err
	}

	laneCodexHome, err := ensureGithubLaneCodexHome(manifest.SandboxPath, "leader")
	if err != nil {
		return err
	}
	sessionID := fmt.Sprintf("sync-%d", time.Now().UnixNano())
	sessionInstructionsPath, err := writeSessionModelInstructions(manifest.SandboxPath, sessionID, laneCodexHome)
	if err != nil {
		return err
	}
	defer removeSessionInstructionsFile(manifest.SandboxPath, sessionID)

	prompt := fmt.Sprintf("Continue GitHub %s #%d for %s after new reviewer feedback", manifest.TargetKind, manifest.TargetNumber, manifest.RepoSlug)
	finalPrompt := buildGithubFeedbackInstructions(manifest, reviewer, newFeedback) + "\n\nTask:\n" + prompt
	execArgs := append([]string{"exec", "-C", manifest.SandboxRepoPath}, options.CodexArgs...)
	execArgs = append(execArgs, finalPrompt)
	execArgs = injectModelInstructionsArgs(execArgs, sessionInstructionsPath)
	cmd := exec.Command("codex", execArgs...)
	cmd.Dir = manifest.SandboxPath
	cmd.Env = append(buildCodexEnv(NotifyTempContract{}, laneCodexHome), "NANA_PROJECT_AGENTS_ROOT="+manifest.SandboxRepoPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	fmt.Fprintf(os.Stdout, "[github] Stored new feedback for run %s.\n", manifest.RunID)
	fmt.Fprintf(os.Stdout, "[github] Feedback file: %s\n", feedbackInstructionsPath)
	if stdout.Len() > 0 {
		fmt.Fprint(os.Stdout, stdout.String())
	}
	if stderr.Len() > 0 {
		fmt.Fprint(os.Stdout, stderr.String())
	}
	return runErr
}

func fetchGithubFeedbackSnapshot(manifest githubWorkonManifest, reviewer string, apiBaseURL string, token string, targetOverrideURL string) (githubFeedbackSnapshot, error) {
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
	for _, comment := range issueComments {
		if strings.EqualFold(strings.TrimSpace(comment.User.Login), reviewer) {
			filteredIssueComments = append(filteredIssueComments, comment)
		}
	}
	if effectivePRNumber == 0 {
		return githubFeedbackSnapshot{IssueComments: filteredIssueComments}, nil
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
		if strings.EqualFold(strings.TrimSpace(review.User.Login), reviewer) {
			filteredReviews = append(filteredReviews, review)
		}
	}
	filteredReviewComments := []githubPullReviewCommentPayload{}
	for _, comment := range reviewComments {
		if strings.EqualFold(strings.TrimSpace(comment.User.Login), reviewer) {
			filteredReviewComments = append(filteredReviewComments, comment)
		}
	}
	return githubFeedbackSnapshot{
		IssueComments:  filteredIssueComments,
		Reviews:        filteredReviews,
		ReviewComments: filteredReviewComments,
	}, nil
}

func filterGithubNewFeedback(snapshot githubFeedbackSnapshot, manifest githubWorkonManifest) githubFeedbackSnapshot {
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

func advanceGithubFeedbackCursor(snapshot githubFeedbackSnapshot, manifest githubWorkonManifest) githubFeedbackCursor {
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

func buildGithubFeedbackInstructions(manifest githubWorkonManifest, reviewer string, feedback githubFeedbackSnapshot) string {
	lines := []string{
		"# NANA Work-on Feedback",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo: %s", manifest.RepoSlug),
		fmt.Sprintf("Sandbox path: %s", manifest.SandboxPath),
		fmt.Sprintf("Repo checkout path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Target: %s #%d", manifest.TargetKind, manifest.TargetNumber),
		fmt.Sprintf("URL: %s", manifest.TargetURL),
		fmt.Sprintf("Reviewer: @%s", reviewer),
		"",
	}
	renderIssueComment := func(comment githubIssueCommentPayload) {
		lines = append(lines,
			fmt.Sprintf("## Issue comment %d", comment.ID),
			"",
			comment.Body,
			fmt.Sprintf("Link: %s", comment.HTMLURL),
			"",
		)
	}
	for _, comment := range feedback.IssueComments {
		renderIssueComment(comment)
	}
	for _, review := range feedback.Reviews {
		lines = append(lines,
			fmt.Sprintf("## Review %d", review.ID),
			"",
			fmt.Sprintf("State: %s", review.State),
			strings.TrimSpace(review.Body),
			fmt.Sprintf("Link: %s", review.HTMLURL),
			"",
		)
	}
	for _, comment := range feedback.ReviewComments {
		reference := comment.Path
		if comment.Line > 0 {
			reference = fmt.Sprintf("%s:%d", comment.Path, comment.Line)
		} else if comment.OriginalLine > 0 {
			reference = fmt.Sprintf("%s:%d", comment.Path, comment.OriginalLine)
		}
		lines = append(lines,
			fmt.Sprintf("## Review comment %d", comment.ID),
			"",
			fmt.Sprintf("Path: %s", reference),
			strings.TrimSpace(comment.Body),
			fmt.Sprintf("Link: %s", comment.HTMLURL),
			"",
		)
	}
	return strings.Join(lines, "\n") + "\n"
}
