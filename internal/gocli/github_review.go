package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type githubPullReviewManifest struct {
	Version           int    `json:"version"`
	RunID             string `json:"run_id"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	Status            string `json:"status"`
	RepoSlug          string `json:"repo_slug"`
	RepoOwner         string `json:"repo_owner"`
	RepoName          string `json:"repo_name"`
	ManagedRepoRoot   string `json:"managed_repo_root"`
	SourcePath        string `json:"source_path"`
	ReviewRoot        string `json:"review_root"`
	Mode              string `json:"mode"`
	PerItemContext    string `json:"per_item_context"`
	ReviewerLogin     string `json:"reviewer_login"`
	TargetURL         string `json:"target_url"`
	TargetNumber      int    `json:"target_number"`
	TargetTitle       string `json:"target_title"`
	TargetState       string `json:"target_state"`
	DefaultBranch     string `json:"default_branch"`
	DefaultBranchSHA  string `json:"default_branch_sha"`
	PRHeadRef         string `json:"pr_head_ref"`
	PRHeadSHA         string `json:"pr_head_sha"`
	PRBaseRef         string `json:"pr_base_ref"`
	PRBaseSHA         string `json:"pr_base_sha"`
	PostedReviewID    int    `json:"posted_review_id,omitempty"`
	PostedReviewURL   string `json:"posted_review_url,omitempty"`
	PostedReviewEvent string `json:"posted_review_event,omitempty"`
	Iteration         int    `json:"iteration"`
}

type githubPullReviewActiveState struct {
	Version   int    `json:"version"`
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

func reviewGithubPullRequest(options githubReviewExecutionOptions) error {
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}
	target, err := githubFetchPullRequestTargetContext(options.Target, apiBaseURL, token)
	if err != nil {
		return err
	}
	reviewer := strings.TrimPrefix(strings.TrimSpace(options.Reviewer), "@")
	if reviewer == "" || reviewer == "me" {
		var viewer struct {
			Login string `json:"login"`
		}
		if err := githubAPIGetJSON(apiBaseURL, token, "/user", &viewer); err == nil && strings.TrimSpace(viewer.Login) != "" {
			reviewer = viewer.Login
		}
	}
	now := time.Now().UTC()
	paths := githubManagedPaths(options.Target.repoSlug)
	repoMeta, err := ensureGithubManagedRepoMetadata(paths, githubTargetContext{Repository: target.Repository, Issue: target.Issue}, now)
	if err != nil {
		return err
	}
	reviewRoot := filepath.Join(paths.RepoRoot, "reviews", fmt.Sprintf("pr-%d", options.Target.number))
	activePath := filepath.Join(reviewRoot, "active.json")
	runsDir := filepath.Join(reviewRoot, "runs")
	_ = os.MkdirAll(runsDir, 0o755)
	var active githubPullReviewActiveState
	runID := ""
	if err := readGithubJSON(activePath, &active); err == nil && strings.TrimSpace(active.RunID) != "" {
		runID = active.RunID
		fmt.Fprintf(os.Stdout, "[review] Resuming active review run %s for %s.\n", runID, githubCanonicalTargetURL(options.Target))
	} else {
		runID = fmt.Sprintf("gr-%d", now.UnixNano())
	}
	sourceLockOwner := repoAccessLockOwner{
		Backend: "github-review",
		RunID:   runID,
		Purpose: "source-setup",
		Label:   "github-review-source",
	}
	runDir := filepath.Join(runsDir, runID)
	repoPath := filepath.Join(runDir, "repo")
	if err := prepareGithubPullReviewSource(paths, repoMeta, sourceLockOwner, repoPath, nil); err != nil {
		return err
	}
	if err := githubRunGit(repoPath, "fetch", "--all"); err != nil {
		return err
	}
	sandboxLock, err := acquireSandboxReadLock(repoPath, repoAccessLockOwner{
		Backend: "github-review",
		RunID:   runID,
		Purpose: "review-execution",
		Label:   "github-review-sandbox",
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = sandboxLock.Release()
	}()
	defaultBranchSHA, _ := githubGitOutput(repoPath, "rev-parse", "origin/"+repoMeta.DefaultBranch)
	defaultBranchSHA = strings.TrimSpace(defaultBranchSHA)
	manifest := githubPullReviewManifest{
		Version:          1,
		RunID:            runID,
		CreatedAt:        now.Format(time.RFC3339),
		UpdatedAt:        now.Format(time.RFC3339),
		Status:           "running",
		RepoSlug:         repoMeta.RepoSlug,
		RepoOwner:        repoMeta.RepoOwner,
		RepoName:         repoMeta.RepoName,
		ManagedRepoRoot:  paths.RepoRoot,
		SourcePath:       paths.SourcePath,
		ReviewRoot:       runDir,
		Mode:             options.Mode,
		PerItemContext:   options.PerItemContext,
		ReviewerLogin:    reviewer,
		TargetURL:        githubCanonicalTargetURL(options.Target),
		TargetNumber:     options.Target.number,
		TargetTitle:      target.Issue.Title,
		TargetState:      target.Issue.State,
		DefaultBranch:    repoMeta.DefaultBranch,
		DefaultBranchSHA: defaultBranchSHA,
		PRHeadRef:        target.PullRequest.Head.Ref,
		PRHeadSHA:        target.PullRequest.Head.SHA,
		PRBaseRef:        target.PullRequest.Base.Ref,
		PRBaseSHA:        target.PullRequest.Base.SHA,
		Iteration:        1,
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	if err := writeGithubJSON(activePath, githubPullReviewActiveState{Version: 1, RunID: runID, Status: "running", UpdatedAt: now.Format(time.RFC3339)}); err != nil {
		return err
	}

	findings, err := generateGithubPullReviewFindings(manifest, repoPath)
	if err != nil {
		return err
	}
	if err := writeGithubJSON(filepath.Join(runDir, "accepted.json"), findings); err != nil {
		return err
	}
	for _, emptyPath := range []string{"dropped-user.json", "dropped-not-real.json", "dropped-preexisting.json"} {
		if err := writeGithubJSON(filepath.Join(runDir, emptyPath), []githubPullReviewFinding{}); err != nil {
			return err
		}
	}

	postedID, postedURL, event, err := submitGithubPullReview(manifest, findings, apiBaseURL, token)
	if err != nil {
		return err
	}
	manifest.Status = "completed"
	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	manifest.PostedReviewID = postedID
	manifest.PostedReviewURL = postedURL
	manifest.PostedReviewEvent = event
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	_ = os.Remove(activePath)

	fmt.Fprintf(os.Stdout, "[review] Completed review for %s.\n", manifest.TargetURL)
	fmt.Fprintf(os.Stdout, "[review] Accepted=%d user-dropped=0 not-real=0 pre-existing=0.\n", len(findings))
	if manifest.PostedReviewURL != "" {
		fmt.Fprintf(os.Stdout, "[review] GitHub review: %s\n", manifest.PostedReviewURL)
	}
	return nil
}

func prepareGithubPullReviewSource(paths githubManagedRepoPaths, repoMeta *githubManagedRepoMetadata, owner repoAccessLockOwner, repoPath string, observeReadPhase func(sourcePath string) error) error {
	return cloneGithubManagedSourceForSandbox(paths, repoMeta, owner, repoPath, observeReadPhase)
}

type githubPullReviewTargetContext struct {
	Repository  githubRepositoryPayload
	Issue       githubIssuePayload
	PullRequest githubPullRequestPayload
}

func githubFetchPullRequestTargetContext(target parsedGithubTarget, apiBaseURL string, token string) (githubPullReviewTargetContext, error) {
	if target.kind != "pr" {
		return githubPullReviewTargetContext{}, fmt.Errorf("nana review expects a pull request URL.\n%s", GithubReviewHelp)
	}
	var repository githubRepositoryPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s", target.repoSlug), &repository); err != nil {
		return githubPullReviewTargetContext{}, err
	}
	var issue githubIssuePayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/issues/%d", target.repoSlug, target.number), &issue); err != nil {
		return githubPullReviewTargetContext{}, err
	}
	var pull githubPullRequestPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d", target.repoSlug, target.number), &pull); err != nil {
		return githubPullReviewTargetContext{}, err
	}
	return githubPullReviewTargetContext{Repository: repository, Issue: issue, PullRequest: pull}, nil
}

func generateGithubPullReviewFindings(manifest githubPullReviewManifest, repoPath string) ([]githubPullReviewFinding, error) {
	context, err := buildReviewPromptContext(repoPath, []string{manifest.PRBaseSHA, manifest.PRHeadSHA}, reviewPromptContextOptions{
		ChangedFilesLimit: reviewPromptChangedFilesLimit,
		MaxHunksPerFile:   reviewPromptMaxHunksPerFile,
		MaxLinesPerFile:   reviewPromptMaxLinesPerFile,
		MaxCharsPerFile:   reviewPromptMaxCharsPerFile,
	})
	if err != nil {
		return nil, err
	}
	prompt := buildGithubPullReviewPrompt(manifest, context)
	output, err := runGithubReviewCodex(repoPath, prompt)
	if err != nil {
		return nil, err
	}
	findings, err := parseGithubPullReviewFindings(output, manifest)
	if err != nil {
		return nil, err
	}
	return findings, nil
}

func buildGithubPullReviewPrompt(manifest githubPullReviewManifest, context reviewPromptContext) string {
	prompt := strings.Join([]string{
		"Review this pull request and return JSON only.",
		`Schema: {"findings":[{"title":"...","severity":"low|medium|high|critical","path":"...","line":123,"summary":"...","detail":"...","fix":"...","rationale":"..."}]}`,
		"If there are no actionable issues, return {\"findings\":[]}.",
		fmt.Sprintf("Repo: %s", manifest.RepoSlug),
		fmt.Sprintf("PR: #%d", manifest.TargetNumber),
		fmt.Sprintf("Changed files: %s", context.ChangedFilesText),
	}, "\n\n")
	if context.Shortstat != "" {
		prompt += "\n\nShortstat:\n" + context.Shortstat
	}
	prompt += "\n\nDiff summary:\n" + context.DiffSummary
	return capPromptChars(prompt, reviewPromptGithubCharLimit)
}

func runGithubReviewCodex(repoPath string, prompt string) (string, error) {
	args := []string{"exec", "-C", repoPath}
	useStdin := promptTransportForSize(prompt, structuredPromptStdinThreshold) == codexPromptTransportStdin
	if useStdin {
		args = append(args, "-")
	} else {
		args = append(args, prompt)
	}
	cmd := exec.Command("codex", args...)
	cmd.Dir = repoPath
	cmd.Env = buildGithubCodexEnv(NotifyTempContract{}, "", strings.TrimSpace(os.Getenv("GITHUB_API_URL")))
	if useStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := strings.TrimSpace(stdout.String())
	if err != nil {
		return "", fmt.Errorf("%v\n%s", err, stderr.String())
	}
	return output, nil
}

func parseGithubPullReviewFindings(raw string, manifest githubPullReviewManifest) ([]githubPullReviewFinding, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("review output did not contain JSON object")
	}
	var payload struct {
		Findings []struct {
			Title     string `json:"title"`
			Severity  string `json:"severity"`
			Path      string `json:"path"`
			Line      int    `json:"line"`
			Summary   string `json:"summary"`
			Detail    string `json:"detail"`
			Fix       string `json:"fix"`
			Rationale string `json:"rationale"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err != nil {
		return nil, err
	}
	findings := []githubPullReviewFinding{}
	for _, finding := range payload.Findings {
		if strings.TrimSpace(finding.Path) == "" || strings.TrimSpace(finding.Title) == "" || strings.TrimSpace(finding.Detail) == "" {
			continue
		}
		summary := strings.TrimSpace(finding.Summary)
		if summary == "" {
			summary = strings.TrimSpace(finding.Detail)
		}
		fingerprint := buildGithubPullReviewFindingFingerprint(finding.Title, finding.Path, finding.Line, summary)
		findings = append(findings, githubPullReviewFinding{
			Fingerprint:     fingerprint,
			Title:           strings.TrimSpace(finding.Title),
			Path:            strings.TrimSpace(finding.Path),
			Line:            finding.Line,
			Severity:        normalizeGithubSeverity(finding.Severity),
			Summary:         summary,
			Detail:          strings.TrimSpace(finding.Detail),
			Fix:             strings.TrimSpace(finding.Fix),
			Rationale:       strings.TrimSpace(finding.Rationale),
			ChangedInPR:     true,
			ChangedLineInPR: finding.Line > 0,
			MainPermalink:   buildGithubBlobPermalink(manifest.RepoSlug, manifest.DefaultBranchSHA, finding.Path, finding.Line),
			PRPermalink:     buildGithubBlobPermalink(manifest.RepoSlug, manifest.PRHeadSHA, finding.Path, finding.Line),
		})
	}
	return findings, nil
}

func buildGithubPullReviewFindingFingerprint(title string, path string, line int, summary string) string {
	return fmt.Sprintf("%s|%s|%d|%s", strings.ToLower(strings.TrimSpace(path)), strings.ToLower(strings.TrimSpace(title)), line, strings.ToLower(strings.TrimSpace(summary)))
}

func normalizeGithubSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "high", "medium", "low":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "medium"
	}
}

func buildGithubBlobPermalink(repoSlug string, sha string, path string, line int) string {
	if strings.TrimSpace(sha) == "" || strings.TrimSpace(path) == "" {
		return ""
	}
	ref := fmt.Sprintf("https://github.com/%s/blob/%s/%s", repoSlug, sha, strings.TrimPrefix(path, "/"))
	if line > 0 {
		ref += fmt.Sprintf("#L%d", line)
	}
	return ref
}

func submitGithubPullReview(manifest githubPullReviewManifest, findings []githubPullReviewFinding, apiBaseURL string, token string) (int, string, string, error) {
	event := "APPROVE"
	if len(findings) > 0 {
		event = "REQUEST_CHANGES"
	}
	body := formatGithubPullReviewSummary(findings, event)
	comments := []map[string]any{}
	for _, finding := range findings {
		if finding.ChangedLineInPR && finding.Line > 0 {
			comments = append(comments, map[string]any{
				"path": finding.Path,
				"line": finding.Line,
				"side": "RIGHT",
				"body": formatGithubPullReviewFinding(finding),
			})
		}
	}
	payload := map[string]any{
		"body":  body,
		"event": event,
	}
	if len(comments) > 0 {
		payload["comments"] = comments
	}
	var response struct {
		ID      int    `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/reviews", manifest.RepoSlug, manifest.TargetNumber), payload, &response); err != nil {
		if len(comments) > 0 {
			delete(payload, "comments")
			if retryErr := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/reviews", manifest.RepoSlug, manifest.TargetNumber), payload, &response); retryErr != nil {
				return 0, "", "", retryErr
			}
		} else {
			return 0, "", "", err
		}
	}
	return response.ID, response.HTMLURL, event, nil
}

func formatGithubPullReviewSummary(findings []githubPullReviewFinding, event string) string {
	if len(findings) == 0 {
		return "Reviewed the PR. No actionable issues found."
	}
	lines := []string{"Found actionable issues that should be fixed before merge.", ""}
	for _, finding := range findings {
		lines = append(lines, formatGithubPullReviewFinding(finding), "")
	}
	_ = event
	return strings.Join(lines, "\n")
}

func formatGithubPullReviewFinding(finding githubPullReviewFinding) string {
	reference := finding.Path
	if finding.Line > 0 {
		reference = fmt.Sprintf("%s:%d", finding.Path, finding.Line)
	}
	lines := []string{
		fmt.Sprintf("[%s] %s", strings.ToUpper(finding.Severity), finding.Title),
		fmt.Sprintf("*%s* - %s", reference, finding.Detail),
	}
	if strings.TrimSpace(finding.Fix) != "" {
		lines = append(lines, "Fix: "+finding.Fix)
	}
	link := finding.MainPermalink
	if finding.ChangedLineInPR && strings.TrimSpace(finding.PRPermalink) != "" {
		link = finding.PRPermalink
	}
	if strings.TrimSpace(link) != "" {
		lines = append(lines, "Reference: "+link)
	}
	return strings.Join(lines, "\n")
}
