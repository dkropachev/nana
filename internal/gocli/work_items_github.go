package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type githubNotificationThreadPayload struct {
	ID         string `json:"id"`
	Reason     string `json:"reason"`
	UpdatedAt  string `json:"updated_at"`
	Unread     bool   `json:"unread"`
	LastReadAt string `json:"last_read_at"`
	Subject    struct {
		Title            string `json:"title"`
		URL              string `json:"url"`
		LatestCommentURL string `json:"latest_comment_url"`
		Type             string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

type githubNotificationCommentContext struct {
	ExternalID string
	APIURL     string
	HTMLURL    string
	Kind       string
	Body       string
	Author     string
	Path       string
	Line       int
}

type githubWorkItemSyncResult struct {
	Queued      int `json:"queued"`
	Created     int `json:"created"`
	Refreshed   int `json:"refreshed"`
	AutoStarted int `json:"auto_started"`
}

func syncGithubWorkItems(options workItemSyncCommandOptions) (githubWorkItemSyncResult, error) {
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return githubWorkItemSyncResult{}, err
	}
	path := fmt.Sprintf("/notifications?all=false&per_page=%d", defaultInt(options.Limit, 50))
	if strings.TrimSpace(options.RepoSlug) != "" {
		path = fmt.Sprintf("/repos/%s/notifications?all=false&per_page=%d", options.RepoSlug, defaultInt(options.Limit, 50))
	}
	var threads []githubNotificationThreadPayload
	if err := githubAPIGetJSON(apiBaseURL, token, path, &threads); err != nil {
		return githubWorkItemSyncResult{}, err
	}
	result := githubWorkItemSyncResult{}
	for _, thread := range threads {
		input, ok, err := buildGithubWorkItemInput(thread, apiBaseURL, token, options.AutoRun)
		if err != nil {
			return result, err
		}
		if !ok {
			continue
		}
		item, created, err := enqueueWorkItem(input, "github-sync")
		if err != nil {
			return result, err
		}
		result.Queued++
		if created {
			result.Created++
		} else {
			result.Refreshed++
		}
		if item.AutoRun && item.Status == workItemStatusQueued {
			if _, err := runWorkItemByID("", item.ID, options.CodexArgs, true); err != nil {
				return result, err
			}
			result.AutoStarted++
		}
	}
	fmt.Fprintf(os.Stdout, "[work-item] GitHub sync queued=%d created=%d refreshed=%d auto-started=%d\n", result.Queued, result.Created, result.Refreshed, result.AutoStarted)
	return result, nil
}

func buildGithubWorkItemInput(thread githubNotificationThreadPayload, apiBaseURL string, token string, autoRun bool) (workItemInput, bool, error) {
	reason := strings.TrimSpace(thread.Reason)
	repoSlug := strings.TrimSpace(thread.Repository.FullName)
	if repoSlug == "" {
		return workItemInput{}, false, nil
	}
	switch reason {
	case "review_requested":
		if !strings.EqualFold(strings.TrimSpace(thread.Subject.Type), "PullRequest") {
			return workItemInput{}, false, nil
		}
		targetURL, err := githubNotificationTargetURL(repoSlug, thread.Subject.Type, thread.Subject.URL, apiBaseURL, token)
		if err != nil {
			return workItemInput{}, false, err
		}
		return workItemInput{
			Source:     "github",
			SourceKind: "review_request",
			ExternalID: "thread:" + strings.TrimSpace(thread.ID),
			ThreadKey:  strings.TrimSpace(thread.ID),
			RepoSlug:   repoSlug,
			TargetURL:  targetURL,
			Subject:    defaultString(strings.TrimSpace(thread.Subject.Title), "GitHub review request"),
			Body:       "GitHub requested a review on this pull request.",
			Author:     "",
			ReceivedAt: defaultString(strings.TrimSpace(thread.UpdatedAt), ISOTimeNow()),
			AutoRun:    autoRun,
			Metadata: map[string]any{
				"notification_reason": reason,
				"thread_id":           thread.ID,
				"subject_type":        thread.Subject.Type,
				"subject_url":         thread.Subject.URL,
			},
		}, true, nil
	case "comment", "mention":
		comment, err := fetchGithubNotificationComment(apiBaseURL, token, thread.Subject.LatestCommentURL)
		if err != nil {
			return workItemInput{}, false, err
		}
		if strings.TrimSpace(comment.ExternalID) == "" || strings.TrimSpace(comment.Body) == "" {
			return workItemInput{}, false, nil
		}
		targetURL, err := githubNotificationTargetURL(repoSlug, thread.Subject.Type, thread.Subject.URL, apiBaseURL, token)
		if err != nil {
			return workItemInput{}, false, err
		}
		metadata := map[string]any{
			"notification_reason": reason,
			"thread_id":           thread.ID,
			"subject_type":        thread.Subject.Type,
			"subject_url":         thread.Subject.URL,
			"comment_api_url":     comment.APIURL,
			"comment_html_url":    comment.HTMLURL,
			"comment_kind":        comment.Kind,
		}
		if comment.Path != "" {
			metadata["comment_path"] = comment.Path
		}
		if comment.Line > 0 {
			metadata["comment_line"] = comment.Line
		}
		return workItemInput{
			Source:     "github",
			SourceKind: "thread_comment",
			ExternalID: comment.ExternalID,
			ThreadKey:  strings.TrimSpace(thread.ID),
			RepoSlug:   repoSlug,
			TargetURL:  targetURL,
			Subject:    defaultString(strings.TrimSpace(thread.Subject.Title), "GitHub thread comment"),
			Body:       comment.Body,
			Author:     comment.Author,
			ReceivedAt: defaultString(strings.TrimSpace(thread.UpdatedAt), ISOTimeNow()),
			AutoRun:    autoRun,
			Metadata:   metadata,
		}, true, nil
	default:
		return workItemInput{}, false, nil
	}
}

func fetchGithubNotificationComment(apiBaseURL string, token string, rawURL string) (githubNotificationCommentContext, error) {
	path, err := githubAPIPathFromURL(apiBaseURL, rawURL)
	if err != nil {
		return githubNotificationCommentContext{}, nil
	}
	switch {
	case strings.Contains(path, "/pulls/comments/"):
		var payload githubPullReviewCommentPayload
		if err := githubAPIGetJSON(apiBaseURL, token, path, &payload); err != nil {
			return githubNotificationCommentContext{}, err
		}
		return githubNotificationCommentContext{
			ExternalID: strconv.Itoa(payload.ID),
			APIURL:     rawURL,
			HTMLURL:    payload.HTMLURL,
			Kind:       "review_comment",
			Body:       strings.TrimSpace(payload.Body),
			Author:     strings.TrimSpace(payload.User.Login),
			Path:       strings.TrimSpace(payload.Path),
			Line:       defaultInt(payload.Line, payload.OriginalLine),
		}, nil
	case strings.Contains(path, "/issues/comments/"):
		var payload githubIssueCommentPayload
		if err := githubAPIGetJSON(apiBaseURL, token, path, &payload); err != nil {
			return githubNotificationCommentContext{}, err
		}
		return githubNotificationCommentContext{
			ExternalID: strconv.Itoa(payload.ID),
			APIURL:     rawURL,
			HTMLURL:    payload.HTMLURL,
			Kind:       "issue_comment",
			Body:       strings.TrimSpace(payload.Body),
			Author:     strings.TrimSpace(payload.User.Login),
		}, nil
	default:
		return githubNotificationCommentContext{}, nil
	}
}

func githubNotificationTargetURL(repoSlug string, subjectType string, rawURL string, apiBaseURL string, token string) (string, error) {
	path, err := githubAPIPathFromURL(apiBaseURL, rawURL)
	if err != nil {
		return "", err
	}
	target, ok := githubParsedTargetFromAPIPath(repoSlug, path, subjectType)
	if ok {
		return githubCanonicalTargetURL(target), nil
	}
	return fmt.Sprintf("https://github.com/%s", repoSlug), nil
}

func githubAPIPathFromURL(apiBaseURL string, rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", nil
	}
	switch {
	case strings.HasPrefix(trimmed, "/"):
		return trimmed, nil
	case strings.HasPrefix(trimmed, strings.TrimRight(apiBaseURL, "/")):
		return strings.TrimPrefix(trimmed, strings.TrimRight(apiBaseURL, "/")), nil
	case strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://"):
		if index := strings.Index(trimmed, "/repos/"); index >= 0 {
			return trimmed[index:], nil
		}
	}
	return "", fmt.Errorf("unsupported GitHub API URL %q", rawURL)
}

func executeGithubReviewRequestWorkItem(item workItem, attemptDir string, codexArgs []string) (workItemExecutionResult, string, error) {
	target, err := parseGithubTargetURL(item.TargetURL)
	if err != nil {
		return workItemExecutionResult{}, "", err
	}
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return workItemExecutionResult{}, "", err
	}
	targetContext, err := githubFetchPullRequestTargetContext(target, apiBaseURL, token)
	if err != nil {
		return workItemExecutionResult{}, "", err
	}
	paths := githubManagedPaths(target.repoSlug)
	now := time.Now().UTC()
	repoMeta, err := ensureGithubManagedRepoMetadata(paths, githubTargetContext{Repository: targetContext.Repository, Issue: targetContext.Issue}, now)
	if err != nil {
		return workItemExecutionResult{}, "", err
	}
	if err := ensureGithubSourceClone(paths, repoMeta); err != nil {
		return workItemExecutionResult{}, "", err
	}
	repoPath := filepath.Join(attemptDir, "repo")
	if err := cloneGithubSourceToSandbox(paths.SourcePath, repoPath); err != nil {
		return workItemExecutionResult{}, "", err
	}
	if err := githubRunGit(repoPath, "fetch", "--all"); err != nil {
		return workItemExecutionResult{}, "", err
	}
	defaultBranchSHA, _ := githubGitOutput(repoPath, "rev-parse", "origin/"+repoMeta.DefaultBranch)
	manifest := githubPullReviewManifest{
		Version:          1,
		RunID:            item.ID,
		CreatedAt:        now.Format(time.RFC3339),
		UpdatedAt:        now.Format(time.RFC3339),
		Status:           "draft_ready",
		RepoSlug:         repoMeta.RepoSlug,
		RepoOwner:        repoMeta.RepoOwner,
		RepoName:         repoMeta.RepoName,
		ManagedRepoRoot:  paths.RepoRoot,
		SourcePath:       paths.SourcePath,
		ReviewRoot:       attemptDir,
		Mode:             "automatic",
		PerItemContext:   "shared",
		ReviewerLogin:    "",
		TargetURL:        item.TargetURL,
		TargetNumber:     target.number,
		TargetTitle:      targetContext.Issue.Title,
		TargetState:      targetContext.Issue.State,
		DefaultBranch:    repoMeta.DefaultBranch,
		DefaultBranchSHA: strings.TrimSpace(defaultBranchSHA),
		PRHeadRef:        targetContext.PullRequest.Head.Ref,
		PRHeadSHA:        targetContext.PullRequest.Head.SHA,
		PRBaseRef:        targetContext.PullRequest.Base.Ref,
		PRBaseSHA:        targetContext.PullRequest.Base.SHA,
		Iteration:        1,
	}
	findings, rawOutput, err := generateGithubPullReviewFindingsWithArgs(manifest, repoPath, codexArgs)
	if err != nil {
		return workItemExecutionResult{}, rawOutput, err
	}
	item.LatestDraft = buildGithubPullReviewDraft(findings)
	item.LinkedRunID = defaultString(item.LinkedRunID, resolveWorkItemLinkedRunID(item))
	result := workItemExecutionResult{
		Item:  item,
		Draft: item.LatestDraft,
		Links: buildDefaultWorkItemLinks(item),
	}
	if err := writeGithubJSON(filepath.Join(attemptDir, "findings.json"), findings); err != nil {
		return workItemExecutionResult{}, rawOutput, err
	}
	if err := writeGithubJSON(filepath.Join(attemptDir, "target-context.json"), targetContext); err != nil {
		return workItemExecutionResult{}, rawOutput, err
	}
	return result, rawOutput, nil
}

func generateGithubPullReviewFindingsWithArgs(manifest githubPullReviewManifest, repoPath string, codexArgs []string) ([]githubPullReviewFinding, string, error) {
	context, err := buildReviewPromptContext(repoPath, []string{manifest.PRBaseSHA, manifest.PRHeadSHA}, reviewPromptContextOptions{
		ChangedFilesLimit: reviewPromptChangedFilesLimit,
		MaxHunksPerFile:   reviewPromptMaxHunksPerFile,
		MaxLinesPerFile:   reviewPromptMaxLinesPerFile,
		MaxCharsPerFile:   reviewPromptMaxCharsPerFile,
	})
	if err != nil {
		return nil, "", err
	}
	prompt := buildGithubPullReviewPrompt(manifest, context)
	rawOutput, err := runWorkItemCodexPrompt(repoPath, filepath.Join(workItemsRoot(), "_review-attempts", sanitizePathToken(manifest.RunID)), prompt, codexArgs)
	if err != nil {
		return nil, rawOutput, err
	}
	findings, err := parseGithubPullReviewFindings(rawOutput, manifest)
	return findings, rawOutput, err
}

func buildGithubPullReviewDraft(findings []githubPullReviewFinding) *workItemDraft {
	event := "APPROVE"
	disposition := "submit"
	if len(findings) > 0 {
		event = "REQUEST_CHANGES"
		disposition = "needs_review"
	}
	inlineComments := make([]workItemDraftInlineComment, 0, len(findings))
	for _, finding := range findings {
		if finding.Line <= 0 || strings.TrimSpace(finding.Path) == "" {
			continue
		}
		inlineComments = append(inlineComments, workItemDraftInlineComment{
			Path: finding.Path,
			Line: finding.Line,
			Body: formatGithubPullReviewFinding(finding),
		})
	}
	return &workItemDraft{
		Kind:                 "review",
		Body:                 formatGithubPullReviewSummary(findings, event),
		ReviewEvent:          event,
		InlineComments:       inlineComments,
		Summary:              defaultString(strings.TrimSpace(formatGithubPullReviewSummary(findings[:workItemMinInt(len(findings), 1)], event)), "Reviewed the pull request."),
		SuggestedDisposition: disposition,
		Confidence:           0.95,
	}
}

func submitGithubReviewRequestDraft(item workItem) error {
	if item.LatestDraft == nil {
		return fmt.Errorf("work item %s does not have a review draft", item.ID)
	}
	target, err := parseGithubTargetURL(item.TargetURL)
	if err != nil {
		return err
	}
	if target.kind != "pr" {
		return fmt.Errorf("work item %s target is not a pull request", item.ID)
	}
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}
	comments := []map[string]any{}
	for _, comment := range item.LatestDraft.InlineComments {
		if strings.TrimSpace(comment.Path) == "" || comment.Line <= 0 || strings.TrimSpace(comment.Body) == "" {
			continue
		}
		comments = append(comments, map[string]any{
			"path": comment.Path,
			"line": comment.Line,
			"side": "RIGHT",
			"body": comment.Body,
		})
	}
	payload := map[string]any{
		"body":  defaultString(item.LatestDraft.Body, "Reviewed the pull request."),
		"event": defaultString(item.LatestDraft.ReviewEvent, "COMMENT"),
	}
	if len(comments) > 0 {
		payload["comments"] = comments
	}
	var response struct {
		ID      int    `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews", target.repoSlug, target.number)
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, path, payload, &response); err != nil {
		if len(comments) > 0 {
			delete(payload, "comments")
			if retryErr := githubAPIRequestJSON("POST", apiBaseURL, token, path, payload, &response); retryErr != nil {
				return retryErr
			}
		} else {
			return err
		}
	}
	if strings.TrimSpace(item.LatestArtifactRoot) != "" {
		return writeGithubJSON(filepath.Join(item.LatestArtifactRoot, "submit-result.json"), response)
	}
	return nil
}

func submitGithubThreadCommentDraft(item workItem) error {
	if item.LatestDraft == nil {
		return fmt.Errorf("work item %s does not have a reply draft", item.ID)
	}
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}
	commentKind := metadataString(item.Metadata, "comment_kind")
	commentAPIURL := metadataString(item.Metadata, "comment_api_url")
	payload := map[string]any{"body": item.LatestDraft.Body}
	switch commentKind {
	case "review_comment":
		path, err := githubAPIPathFromURL(apiBaseURL, commentAPIURL)
		if err != nil {
			return err
		}
		var response struct {
			ID      int    `json:"id"`
			HTMLURL string `json:"html_url"`
		}
		if err := githubAPIRequestJSON("POST", apiBaseURL, token, path+"/replies", payload, &response); err != nil {
			return err
		}
		if strings.TrimSpace(item.LatestArtifactRoot) != "" {
			return writeGithubJSON(filepath.Join(item.LatestArtifactRoot, "submit-result.json"), response)
		}
		return nil
	default:
		target, err := parseGithubTargetURL(item.TargetURL)
		if err != nil {
			return err
		}
		var response githubIssueCommentPayload
		if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/issues/%d/comments", target.repoSlug, target.number), payload, &response); err != nil {
			return err
		}
		if strings.TrimSpace(item.LatestArtifactRoot) != "" {
			return writeGithubJSON(filepath.Join(item.LatestArtifactRoot, "submit-result.json"), response)
		}
		return nil
	}
}

func workItemMinInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func githubParsedTargetFromAPIPath(repoSlug string, path string, subjectType string) (parsedGithubTarget, bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for index := 0; index < len(segments); index++ {
		switch segments[index] {
		case "issues":
			if index+1 >= len(segments) {
				return parsedGithubTarget{}, false
			}
			number, err := strconv.Atoi(segments[index+1])
			if err != nil || number <= 0 {
				return parsedGithubTarget{}, false
			}
			kind := "issue"
			if strings.EqualFold(strings.TrimSpace(subjectType), "PullRequest") {
				kind = "pr"
			}
			return parsedGithubTarget{repoSlug: repoSlug, kind: kind, number: number}, true
		case "pulls":
			if index+1 >= len(segments) {
				return parsedGithubTarget{}, false
			}
			number, err := strconv.Atoi(segments[index+1])
			if err != nil || number <= 0 {
				return parsedGithubTarget{}, false
			}
			return parsedGithubTarget{repoSlug: repoSlug, kind: "pr", number: number}, true
		}
	}
	return parsedGithubTarget{}, false
}
