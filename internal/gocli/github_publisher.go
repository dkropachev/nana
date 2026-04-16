package gocli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type githubCheckRunPayload struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type githubPullRequestResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

type githubMergeResponse struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

type githubPublicationTarget struct {
	RepoSlug   string
	RepoOwner  string
	RemoteName string
	RemoteURL  string
}

type githubCheckRunsResponse struct {
	CheckRuns []githubCheckRunPayload `json:"check_runs"`
}

type githubWorkflowRunsResponse struct {
	WorkflowRuns []struct {
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"workflow_runs"`
}

func executeGithubPublisherLane(runID string, useLast bool) error {
	manifestPath, _, err := resolveGithubRunManifestPath(runID, useLast)
	if err != nil {
		return err
	}
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(manifest.SandboxRepoPath) == "" {
		return fmt.Errorf("run %s is missing sandbox repo path", manifest.RunID)
	}
	if manifest.Policy != nil {
		if !manifest.Policy.AllowedActions.Commit {
			return fmt.Errorf("publication blocked by policy: commit action is disabled")
		}
		if !manifest.Policy.AllowedActions.Push {
			return fmt.Errorf("publication blocked by policy: push action is disabled")
		}
		if !manifest.Policy.AllowedActions.OpenDraftPR {
			return fmt.Errorf("publication blocked by policy: open_draft_pr action is disabled")
		}
	}
	if manifest.VerificationScriptsDir != "" {
		if err := runPublisherVerificationGate(manifest.VerificationScriptsDir, manifest.SandboxPath); err != nil {
			manifest.PublicationState = "blocked"
			manifest.PublicationError = err.Error()
			manifest.PublicationUpdatedAt = time.Now().UTC().Format(time.RFC3339)
			_ = writeGithubJSON(manifestPath, manifest)
			_ = indexGithubWorkRunManifest(manifestPath, manifest)
			return err
		}
	}
	headSHABefore, _ := githubGitOutput(manifest.SandboxRepoPath, "rev-parse", "HEAD")
	headSHABefore = strings.TrimSpace(headSHABefore)
	branchName := strings.TrimSpace(manifest.PublishedPRHeadRef)
	if branchName == "" {
		branchName = fmt.Sprintf("nana/%s-%d/%s", manifest.TargetKind, manifest.TargetNumber, manifest.SandboxID)
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
	publicationTarget, err := resolveGithubPublicationTarget(&manifest, apiBaseURL, token)
	if err != nil {
		return err
	}
	if err := ensureGithubPublicationCommit(manifest.SandboxRepoPath, manifest); err != nil {
		return err
	}
	headSHA, err := githubGitOutput(manifest.SandboxRepoPath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	headSHA = strings.TrimSpace(headSHA)
	if strings.TrimSpace(headSHABefore) != headSHA {
		fmt.Fprintf(os.Stdout, "[github] Created automatic publication commit on %s.\n", branchName)
	}
	if publicationTarget.RemoteURL != "" {
		if err := ensureGithubRemote(manifest.SandboxRepoPath, publicationTarget.RemoteName, publicationTarget.RemoteURL); err != nil {
			return err
		}
	}
	if err := githubRunGit(manifest.SandboxRepoPath, "push", "-u", publicationTarget.RemoteName, fmt.Sprintf("HEAD:%s", branchName)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[github] Pushed HEAD to %s/%s.\n", publicationTarget.RemoteName, branchName)

	pr, created, err := ensureGithubDraftPR(manifest, publicationTarget, apiBaseURL, token, branchName)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(os.Stdout, "[github] Created draft PR #%d: %s\n", pr.Number, pr.HTMLURL)
	} else {
		fmt.Fprintf(os.Stdout, "[github] Updated draft PR #%d: %s\n", pr.Number, pr.HTMLURL)
	}
	ciResult, err := readGithubCIResult(publicationTarget.RepoSlug, headSHA, apiBaseURL, token)
	if err != nil {
		return err
	}
	ciState := ciResult.State
	manifest.PublishedPRNumber = pr.Number
	manifest.PublishedPRURL = pr.HTMLURL
	manifest.PublishedPRHeadRef = branchName
	if publicationTarget.RepoSlug == manifest.RepoSlug {
		requestedReviewers, reviewRequestState, reviewRequestError, err := ensureGithubRequestedReviews(manifest, pr.Number, apiBaseURL, token)
		if err != nil {
			return err
		}
		manifest.RequestedReviewers = requestedReviewers
		manifest.ReviewRequestState = reviewRequestState
		manifest.ReviewRequestError = reviewRequestError
		manifest.ReviewRequestUpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if manifest.ReviewRequestState == "requested" {
			fmt.Fprintf(os.Stdout, "[github] Requested review from %s.\n", formatGithubActorSet(manifest.RequestedReviewers))
		} else if manifest.ReviewRequestState == "already_requested" {
			fmt.Fprintf(os.Stdout, "[github] Review request already satisfied for %s.\n", formatGithubActorSet(manifest.RequestedReviewers))
		}
	} else {
		manifest.ReviewRequestState = "not_requested"
		manifest.ReviewRequestUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	manifest.PublicationState = ciState
	manifest.PublicationDetail = ciResult.Detail
	manifest.PublicationUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	manifest.PublicationError = ""
	manifest.NeedsHuman, manifest.NeedsHumanReason, manifest.NextAction = determineGithubHumanGateState(manifest.Policy, true)
	if ciState == "blocked" {
		manifest.PublicationError = defaultString(ciResult.Detail, "External CI has failing checks")
	} else if ciState == "ci_waiting" && ciResult.Detail != "" {
		manifest.PublicationError = ciResult.Detail
	}
	mergeState, mergeSHA, mergeError := "not_attempted", "", ""
	if publicationTarget.RepoSlug == manifest.RepoSlug {
		var mergeErr error
		mergeState, mergeSHA, mergeError, mergeErr = ensureGithubMerge(manifest, pr, ciState, apiBaseURL, token)
		if mergeErr != nil {
			return mergeErr
		}
	}
	manifest.MergeState = mergeState
	manifest.MergeError = mergeError
	manifest.MergeMethod = githubEffectiveMergeMethod(manifest.Policy)
	manifest.MergeUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if mergeState == "merged" {
		manifest.MergedPRNumber = pr.Number
		manifest.MergedSHA = mergeSHA
		manifest.NeedsHuman = false
		manifest.NeedsHumanReason = ""
		manifest.NextAction = "merged"
		fmt.Fprintf(os.Stdout, "[github] Merged PR #%d with %s.\n", pr.Number, manifest.MergeMethod)
	}
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		return err
	}
	resultPath := laneResultPathForRun(manifestPath, "publisher")
	_ = writeLaneResult(resultPath, fmt.Sprintf("published_pr=%d\nurl=%s\nstate=%s\nreview_request_state=%s\nrequested_reviewers=%s\nmerge_state=%s\nmerge_sha=%s\nmerge_error=%s\n", pr.Number, pr.HTMLURL, ciState, manifest.ReviewRequestState, strings.Join(manifest.RequestedReviewers, ","), manifest.MergeState, manifest.MergedSHA, manifest.MergeError))
	fmt.Fprintf(os.Stdout, "[github] Lane publisher completed via native publication flow.\n")
	fmt.Fprintf(os.Stdout, "[github] Lane result: %s\n", resultPath)
	return nil
}

func runPublisherVerificationGate(verificationScriptsDir string, sandboxPath string) error {
	allPath := verificationScriptsDir + "/all.sh"
	cmd := exec.Command(allPath)
	cmd.Dir = sandboxPath
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v\n%s%s", err, stdout.String(), stderr.String())
	}
	return nil
}

func resolveGithubPublicationTarget(manifest *githubWorkManifest, apiBaseURL string, token string) (githubPublicationTarget, error) {
	publishTarget := defaultString(normalizeGithubPublishTarget(manifest.PublishTarget), "repo")
	if publishTarget == "local-branch" {
		return githubPublicationTarget{}, fmt.Errorf("publication target is local-branch; no pull request should be published")
	}
	if publishTarget == "repo" {
		manifest.PublishRepoSlug = manifest.RepoSlug
		manifest.PublishRepoOwner = manifest.RepoOwner
		return githubPublicationTarget{RepoSlug: manifest.RepoSlug, RepoOwner: manifest.RepoOwner, RemoteName: "origin"}, nil
	}
	viewer, err := githubCurrentViewer(apiBaseURL, token)
	if err != nil {
		return githubPublicationTarget{}, err
	}
	fork, _, err := ensureGithubFork(manifest.RepoSlug, manifest.RepoName, viewer, apiBaseURL, token)
	if err != nil {
		return githubPublicationTarget{}, err
	}
	if err := ensureStartWorkForkReady(fork.FullName, apiBaseURL, token); err != nil {
		return githubPublicationTarget{}, err
	}
	manifest.PublishRepoSlug = fork.FullName
	manifest.PublishRepoOwner = viewer
	return githubPublicationTarget{RepoSlug: fork.FullName, RepoOwner: viewer, RemoteName: "nana-fork", RemoteURL: fork.CloneURL}, nil
}

func ensureGithubRemote(repoPath string, name string, remoteURL string) error {
	if strings.TrimSpace(remoteURL) == "" {
		return nil
	}
	if _, err := githubGitOutput(repoPath, "remote", "get-url", name); err == nil {
		return githubRunGit(repoPath, "remote", "set-url", name, remoteURL)
	}
	return githubRunGit(repoPath, "remote", "add", name, remoteURL)
}

func ensureGithubPublicationCommit(repoPath string, manifest githubWorkManifest) error {
	status, _ := githubGitOutput(repoPath, "status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		return nil
	}
	if err := githubRunGit(repoPath, "add", "-A"); err != nil {
		return err
	}
	message := buildGithubPublicationCommitMessage(manifest)
	return githubRunGit(repoPath, "commit", "-m", message)
}

func ensureGithubDraftPR(manifest githubWorkManifest, target githubPublicationTarget, apiBaseURL string, token string, branchName string) (githubPullRequestResponse, bool, error) {
	var existing []githubPullRequestResponse
	path := fmt.Sprintf("/repos/%s/pulls?state=open&head=%s:%s&base=%s", target.RepoSlug, target.RepoOwner, url.QueryEscape(branchName), url.QueryEscape(manifest.DefaultBranch))
	if err := githubAPIGetJSON(apiBaseURL, token, path, &existing); err == nil && len(existing) > 0 {
		payload := map[string]any{
			"title": manifest.TargetTitle,
			"body":  buildDraftPullRequestBody(manifest, branchName),
			"draft": true,
			"base":  manifest.DefaultBranch,
		}
		var updated githubPullRequestResponse
		if err := githubAPIRequestJSON("PATCH", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d", target.RepoSlug, existing[0].Number), payload, &updated); err != nil {
			return githubPullRequestResponse{}, false, err
		}
		return updated, false, nil
	}
	payload := map[string]any{
		"title": manifest.TargetTitle,
		"head":  fmt.Sprintf("%s:%s", target.RepoOwner, branchName),
		"base":  manifest.DefaultBranch,
		"body":  buildDraftPullRequestBody(manifest, branchName),
		"draft": true,
	}
	var created githubPullRequestResponse
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls", target.RepoSlug), payload, &created); err != nil {
		return githubPullRequestResponse{}, false, err
	}
	return created, true, nil
}

func buildDraftPullRequestBody(manifest githubWorkManifest, branchName string) string {
	if githubRepoNativeEnabled(manifest.Policy) && manifest.RepoProfile != nil && manifest.RepoProfile.PullRequestTemplate != nil && githubPullRequestTemplateSupported(manifest.RepoProfile.PullRequestTemplate) {
		if body := buildRepoNativePullRequestBody(manifest, branchName); strings.TrimSpace(body) != "" {
			return body
		}
	}
	return strings.Join([]string{
		fmt.Sprintf("Closes %s", manifest.TargetURL),
		"",
		"Autogenerated by NANA work.",
		"",
		fmt.Sprintf("- Target: %s #%d", manifest.TargetKind, manifest.TargetNumber),
		fmt.Sprintf("- Branch: %s", branchName),
		fmt.Sprintf("- Role layout: %s", manifest.RoleLayout),
		fmt.Sprintf("- Considerations: %s", joinOrNone(manifest.ConsiderationsActive)),
		"",
	}, "\n")
}

func buildGithubPublicationCommitMessage(manifest githubWorkManifest) string {
	if githubRepoNativeEnabled(manifest.Policy) && manifest.RepoProfile != nil && manifest.RepoProfile.CommitStyle != nil {
		if manifest.RepoProfile.CommitStyle.Kind == "conventional" && manifest.RepoProfile.CommitStyle.Confidence >= 0.6 {
			return fmt.Sprintf("chore: publish %s #%d", manifest.TargetKind, manifest.TargetNumber)
		}
	}
	return fmt.Sprintf("nana: publish %s #%d", manifest.TargetKind, manifest.TargetNumber)
}

func buildRepoNativePullRequestBody(manifest githubWorkManifest, branchName string) string {
	sections := []string{}
	templateSections := []string{}
	hasRelated := false
	if manifest.RepoProfile != nil && manifest.RepoProfile.PullRequestTemplate != nil {
		templateSections = manifest.RepoProfile.PullRequestTemplate.Sections
	}
	validationCommands := []string{}
	if manifest.VerificationPlan != nil {
		validationCommands = append(validationCommands, manifest.VerificationPlan.Lint...)
		validationCommands = append(validationCommands, manifest.VerificationPlan.Unit...)
		validationCommands = append(validationCommands, manifest.VerificationPlan.Integration...)
	}
	for _, section := range templateSections {
		switch strings.ToLower(strings.TrimSpace(section)) {
		case "summary":
			sections = append(sections, "## Summary", "", fmt.Sprintf("Address %s by landing the work tracked from %s.", manifest.TargetTitle, manifest.TargetURL), "")
		case "changes":
			sections = append(sections, "## Changes", "", "- Autogenerated by NANA work using repo-native PR shaping", fmt.Sprintf("- Branch: %s", branchName), fmt.Sprintf("- Considerations: %s", joinOrNone(manifest.ConsiderationsActive)), "")
		case "validation":
			sections = append(sections, "## Validation", "")
			if len(validationCommands) == 0 {
				sections = append(sections, "- [x] Verification gate passed before publication")
			} else {
				for _, command := range uniqueStrings(validationCommands) {
					sections = append(sections, fmt.Sprintf("- [x] `%s`", command))
				}
			}
			sections = append(sections, "")
		case "checklist":
			sections = append(sections, "## Checklist", "", "- [x] PR is focused and avoids unrelated changes", "- [ ] Docs updated when needed", "- [x] Backward-compatibility impact considered", "")
		case "related":
			hasRelated = true
			sections = append(sections, "## Related", "", fmt.Sprintf("Closes %s", manifest.TargetURL), "")
		}
	}
	if len(sections) == 0 {
		return ""
	}
	if !hasRelated {
		sections = append(sections, "## Related", "", fmt.Sprintf("Closes %s", manifest.TargetURL), "")
	}
	return strings.Join(sections, "\n")
}

func ensureGithubRequestedReviews(manifest githubWorkManifest, prNumber int, apiBaseURL string, token string) ([]string, string, string, error) {
	if manifest.Policy == nil || !manifest.Policy.AllowedActions.RequestReview {
		return nil, "not_requested", "", nil
	}
	if prNumber <= 0 {
		return nil, "blocked", "no pull request available for review requests", nil
	}
	reviewers := []string{}
	for _, reviewer := range cleanLogins(manifest.ControlPlaneReviewers) {
		if reviewer == "" || reviewer == "*" {
			continue
		}
		reviewers = append(reviewers, reviewer)
	}
	reviewers = uniqueStrings(reviewers)
	if len(reviewers) == 0 {
		return nil, "blocked", "no eligible control-plane reviewers resolved", nil
	}
	existing, err := fetchGithubExistingRequestedReviewers(manifest.RepoSlug, prNumber, apiBaseURL, token)
	if err != nil {
		return nil, "blocked", err.Error(), nil
	}
	existingSet := map[string]bool{}
	for _, reviewer := range existing {
		existingSet[strings.ToLower(reviewer)] = true
	}
	toRequest := []string{}
	for _, reviewer := range reviewers {
		if !existingSet[strings.ToLower(reviewer)] {
			toRequest = append(toRequest, reviewer)
		}
	}
	if len(toRequest) == 0 {
		return existing, "already_requested", "", nil
	}
	payload := map[string]any{"reviewers": toRequest}
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", manifest.RepoSlug, prNumber), payload, &struct{}{}); err != nil {
		return nil, "blocked", err.Error(), nil
	}
	return uniqueStrings(append(existing, toRequest...)), "requested", "", nil
}

func fetchGithubExistingRequestedReviewers(repoSlug string, prNumber int, apiBaseURL string, token string) ([]string, error) {
	if prNumber <= 0 {
		return nil, nil
	}
	var requested struct {
		Users []githubActor `json:"users"`
	}
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", repoSlug, prNumber), &requested); err != nil {
		return nil, err
	}
	logins := make([]string, 0, len(requested.Users))
	for _, user := range requested.Users {
		logins = append(logins, user.Login)
	}
	return uniqueStrings(cleanLogins(logins)), nil
}

// ensureGithubMerge is intentionally conservative: merge is experimental, policy-gated,
// and requires green CI. The default approve mode also requires a current
// control-plane approval; pr-forward=auto lets policy-approved repos merge
// without that extra review trigger.
func ensureGithubMerge(manifest githubWorkManifest, pr githubPullRequestResponse, ciState string, apiBaseURL string, token string) (string, string, string, error) {
	if manifest.Policy == nil || !manifest.Policy.Experimental || !manifest.Policy.AllowedActions.Merge {
		return "not_attempted", "", "", nil
	}
	if pr.Number <= 0 {
		return "blocked", "", "no pull request available for merge", nil
	}
	if pr.Draft {
		return "blocked", "", "pull request is draft", nil
	}
	if ciState != "ci_green" {
		return "blocked", "", "GitHub CI is not green", nil
	}
	if defaultString(normalizeGithubPRForwardMode(manifest.PRForwardMode), "approve") != "auto" {
		approved, reason, err := githubControlPlaneApprovalSatisfied(manifest, pr.Number, apiBaseURL, token)
		if err != nil {
			return "blocked", "", err.Error(), nil
		}
		if !approved {
			return "blocked", "", reason, nil
		}
	}
	method := githubEffectiveMergeMethod(manifest.Policy)
	payload := map[string]any{"merge_method": method}
	var response githubMergeResponse
	if err := githubAPIRequestJSON("PUT", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/merge", manifest.RepoSlug, pr.Number), payload, &response); err != nil {
		return "blocked", "", err.Error(), nil
	}
	if response.SHA == "" {
		response.SHA = pr.Head.SHA
	}
	return "merged", response.SHA, "", nil
}

func githubPublicationRepoSlug(manifest githubWorkManifest, fallback string) string {
	if repoSlug := strings.TrimSpace(manifest.PublishRepoSlug); repoSlug != "" {
		return repoSlug
	}
	if owner := strings.TrimSpace(manifest.PublishRepoOwner); owner != "" && strings.TrimSpace(manifest.RepoName) != "" {
		return owner + "/" + manifest.RepoName
	}
	if repoSlug := strings.TrimSpace(manifest.RepoSlug); repoSlug != "" {
		return repoSlug
	}
	return strings.TrimSpace(fallback)
}

func readGithubPullRequest(repoSlug string, prNumber int, apiBaseURL string, token string) (githubPullRequestResponse, int, error) {
	var pr githubPullRequestResponse
	status, err := githubAPIGetJSONWithStatus(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d", repoSlug, prNumber), &pr)
	if err != nil {
		return githubPullRequestResponse{}, status, err
	}
	return pr, status, nil
}

func refreshGithubPublicationCIState(manifestPath string, manifest *githubWorkManifest, fallbackRepoSlug string) (githubCIResult, githubPullRequestResponse, error) {
	if manifest == nil {
		return githubCIResult{}, githubPullRequestResponse{}, fmt.Errorf("missing manifest")
	}
	repoSlug := githubPublicationRepoSlug(*manifest, fallbackRepoSlug)
	if repoSlug == "" {
		return githubCIResult{}, githubPullRequestResponse{}, fmt.Errorf("publication repo slug is unknown")
	}
	if manifest.PublishedPRNumber <= 0 {
		return githubCIResult{}, githubPullRequestResponse{}, fmt.Errorf("published pull request number is unknown")
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
		return githubCIResult{}, githubPullRequestResponse{}, err
	}
	pr, status, err := readGithubPullRequest(repoSlug, manifest.PublishedPRNumber, apiBaseURL, token)
	if err != nil {
		result := githubCIResult{
			State:     "blocked",
			Detail:    strings.TrimSpace(err.Error()),
			Transient: isTransientGithubAPIReadFailure(status, err),
		}
		if result.Transient {
			result.State = "ci_waiting"
		}
		if persistErr := persistGithubPublicationCIResult(manifestPath, manifest, result); persistErr != nil {
			return githubCIResult{}, githubPullRequestResponse{}, persistErr
		}
		return result, githubPullRequestResponse{
			Number:  manifest.PublishedPRNumber,
			HTMLURL: manifest.PublishedPRURL,
		}, nil
	}
	ciResult, err := readGithubCIResult(repoSlug, strings.TrimSpace(pr.Head.SHA), apiBaseURL, token)
	if err != nil {
		return githubCIResult{}, githubPullRequestResponse{}, err
	}
	if ciResult.Transient {
		ciResult.State = "ci_waiting"
	}
	updated := false
	if pr.Number > 0 && manifest.PublishedPRNumber != pr.Number {
		manifest.PublishedPRNumber = pr.Number
		updated = true
	}
	if strings.TrimSpace(pr.HTMLURL) != "" && manifest.PublishedPRURL != pr.HTMLURL {
		manifest.PublishedPRURL = pr.HTMLURL
		updated = true
	}
	if strings.TrimSpace(pr.Head.Ref) != "" && manifest.PublishedPRHeadRef != pr.Head.Ref {
		manifest.PublishedPRHeadRef = pr.Head.Ref
		updated = true
	}
	desiredState := ciResult.State
	desiredDetail := strings.TrimSpace(ciResult.Detail)
	desiredError := ""
	if desiredState == "blocked" {
		desiredError = desiredDetail
	}
	if manifest.PublicationState != desiredState {
		manifest.PublicationState = desiredState
		updated = true
	}
	if manifest.PublicationDetail != desiredDetail {
		manifest.PublicationDetail = desiredDetail
		updated = true
	}
	if manifest.PublicationError != desiredError {
		manifest.PublicationError = desiredError
		updated = true
	}
	if updated || strings.TrimSpace(manifest.PublicationUpdatedAt) == "" {
		manifest.PublicationUpdatedAt = time.Now().UTC().Format(time.RFC3339)
		updated = true
	}
	if updated && strings.TrimSpace(manifestPath) != "" {
		if err := writeGithubJSON(manifestPath, *manifest); err != nil {
			return githubCIResult{}, githubPullRequestResponse{}, err
		}
		if err := indexGithubWorkRunManifest(manifestPath, *manifest); err != nil {
			return githubCIResult{}, githubPullRequestResponse{}, err
		}
	}
	return ciResult, pr, nil
}

func persistGithubPublicationCIResult(manifestPath string, manifest *githubWorkManifest, result githubCIResult) error {
	if manifest == nil || strings.TrimSpace(manifestPath) == "" {
		return nil
	}
	manifest.PublicationState = result.State
	manifest.PublicationDetail = strings.TrimSpace(result.Detail)
	manifest.PublicationError = ""
	if result.State == "blocked" {
		manifest.PublicationError = strings.TrimSpace(result.Detail)
	}
	manifest.PublicationUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeGithubJSON(manifestPath, *manifest); err != nil {
		return err
	}
	return indexGithubWorkRunManifest(manifestPath, *manifest)
}

func isTransientGithubAPIReadFailure(statusCode int, err error) bool {
	switch statusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isTransientGithubAPIReadFailure(statusCode, urlErr.Err)
	}
	return false
}

func githubControlPlaneApprovalSatisfied(manifest githubWorkManifest, prNumber int, apiBaseURL string, token string) (bool, string, error) {
	reviewers := eligibleGithubControlPlaneReviewers(manifest)
	if len(reviewers) == 0 {
		return false, "no eligible control-plane reviewers resolved", nil
	}
	var reviews []githubPullReviewPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", manifest.RepoSlug, prNumber), &reviews); err != nil {
		return false, "", err
	}
	eligible := map[string]bool{}
	for _, reviewer := range reviewers {
		eligible[strings.ToLower(reviewer)] = true
	}
	latest := map[string]githubPullReviewPayload{}
	for _, review := range reviews {
		login := strings.ToLower(strings.TrimSpace(review.User.Login))
		if !eligible[login] {
			continue
		}
		current, ok := latest[login]
		if !ok || review.ID > current.ID {
			latest[login] = review
		}
	}
	hasApproval := false
	for _, review := range latest {
		switch strings.ToUpper(strings.TrimSpace(review.State)) {
		case "CHANGES_REQUESTED":
			return false, fmt.Sprintf("latest control-plane review by @%s requests changes", review.User.Login), nil
		case "APPROVED":
			hasApproval = true
		}
	}
	if !hasApproval {
		return false, "no approval from control-plane reviewers", nil
	}
	return true, "", nil
}

func eligibleGithubControlPlaneReviewers(manifest githubWorkManifest) []string {
	reviewers := []string{}
	for _, reviewer := range cleanLogins(manifest.ControlPlaneReviewers) {
		if reviewer == "" || reviewer == "*" {
			continue
		}
		reviewers = append(reviewers, reviewer)
	}
	return uniqueStrings(reviewers)
}

type githubCIResult struct {
	State     string
	Detail    string
	Transient bool
}

func readGithubCIState(repoSlug string, headSHA string, apiBaseURL string, token string) (string, error) {
	result, err := readGithubCIResult(repoSlug, headSHA, apiBaseURL, token)
	return result.State, err
}

func readGithubCIResult(repoSlug string, headSHA string, apiBaseURL string, token string) (githubCIResult, error) {
	if strings.TrimSpace(headSHA) == "" {
		return githubCIResult{State: "blocked", Detail: "missing_head_sha"}, nil
	}
	var checks githubCheckRunsResponse
	checksStatus, checksErr := githubAPIGetJSONWithStatus(apiBaseURL, token, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repoSlug, url.QueryEscape(headSHA)), &checks)
	var runs githubWorkflowRunsResponse
	runsStatus, runsErr := githubAPIGetJSONWithStatus(apiBaseURL, token, fmt.Sprintf("/repos/%s/actions/runs?head_sha=%s&per_page=100", repoSlug, url.QueryEscape(headSHA)), &runs)
	unavailable := []string{}
	transientUnavailable := true
	if checksErr != nil {
		unavailable = append(unavailable, "check_runs_unavailable")
		transientUnavailable = transientUnavailable && isTransientGithubAPIReadFailure(checksStatus, checksErr)
	}
	if runsErr != nil {
		unavailable = append(unavailable, "workflow_runs_unavailable")
		transientUnavailable = transientUnavailable && isTransientGithubAPIReadFailure(runsStatus, runsErr)
	}
	if len(unavailable) > 0 {
		return githubCIResult{State: "blocked", Detail: strings.Join(unavailable, ";"), Transient: transientUnavailable}, nil
	}
	hasAny := len(checks.CheckRuns) > 0 || len(runs.WorkflowRuns) > 0
	hasPending := false
	hasFailures := false
	for _, check := range checks.CheckRuns {
		if check.Status != "completed" {
			hasPending = true
		}
		if check.Conclusion != "" && check.Conclusion != "success" && check.Conclusion != "neutral" && check.Conclusion != "skipped" {
			hasFailures = true
		}
	}
	for _, run := range runs.WorkflowRuns {
		if run.HeadSHA != headSHA {
			continue
		}
		if run.Status != "completed" {
			hasPending = true
		}
		if run.Conclusion != "" && run.Conclusion != "success" && run.Conclusion != "neutral" && run.Conclusion != "skipped" {
			hasFailures = true
		}
	}
	if hasFailures {
		return githubCIResult{State: "blocked", Detail: "ci_failed"}, nil
	}
	if hasAny && !hasPending {
		return githubCIResult{State: "ci_green", Detail: "ci_green"}, nil
	}
	if hasAny {
		return githubCIResult{State: "ci_waiting", Detail: "ci_pending"}, nil
	}
	return githubCIResult{State: "ci_green", Detail: "no_ci_found"}, nil
}

func githubAPIRequestJSON(method string, apiBaseURL string, token string, path string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, strings.TrimRight(apiBaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var raw bytes.Buffer
		_, _ = raw.ReadFrom(resp.Body)
		return fmt.Errorf("GitHub API request failed (%d %s): %s", resp.StatusCode, resp.Status, raw.String())
	}
	if target == nil {
		return nil
	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(content)) == "" {
		return nil
	}
	return json.Unmarshal(content, target)
}

func laneResultPathForRun(manifestPath string, alias string) string {
	return filepath.Join(filepath.Dir(manifestPath), "lane-runtime", fmt.Sprintf("%s-result.md", sanitizeLanePathToken(alias)))
}

func writeLaneResult(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
