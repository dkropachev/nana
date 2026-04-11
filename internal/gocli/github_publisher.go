package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	Head    struct {
		SHA string `json:"sha"`
	} `json:"head"`
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
	if err := githubRunGit(manifest.SandboxRepoPath, "push", "-u", "origin", fmt.Sprintf("HEAD:%s", branchName)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[github] Pushed HEAD to origin/%s.\n", branchName)

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
	pr, created, err := ensureGithubDraftPR(manifest, apiBaseURL, token, branchName)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(os.Stdout, "[github] Created draft PR #%d: %s\n", pr.Number, pr.HTMLURL)
	} else {
		fmt.Fprintf(os.Stdout, "[github] Updated draft PR #%d: %s\n", pr.Number, pr.HTMLURL)
	}
	ciState, err := readGithubCIState(manifest.RepoSlug, headSHA, apiBaseURL, token)
	if err != nil {
		return err
	}
	manifest.PublishedPRNumber = pr.Number
	manifest.PublishedPRURL = pr.HTMLURL
	manifest.PublishedPRHeadRef = branchName
	manifest.PublicationState = ciState
	manifest.PublicationUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	manifest.PublicationError = ""
	if ciState == "blocked" {
		manifest.PublicationError = "External CI has failing checks"
	}
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		return err
	}
	resultPath := laneResultPathForRun(manifestPath, "publisher")
	_ = writeLaneResult(resultPath, fmt.Sprintf("published_pr=%d\nurl=%s\nstate=%s\n", pr.Number, pr.HTMLURL, ciState))
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

func ensureGithubPublicationCommit(repoPath string, manifest githubWorkManifest) error {
	status, _ := githubGitOutput(repoPath, "status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		return nil
	}
	if err := githubRunGit(repoPath, "add", "-A"); err != nil {
		return err
	}
	message := fmt.Sprintf("nana: publish %s #%d", manifest.TargetKind, manifest.TargetNumber)
	return githubRunGit(repoPath, "commit", "-m", message)
}

func ensureGithubDraftPR(manifest githubWorkManifest, apiBaseURL string, token string, branchName string) (githubPullRequestResponse, bool, error) {
	var existing []githubPullRequestResponse
	path := fmt.Sprintf("/repos/%s/pulls?state=open&head=%s:%s&base=%s", manifest.RepoSlug, manifest.RepoOwner, url.QueryEscape(branchName), url.QueryEscape(manifest.DefaultBranch))
	if err := githubAPIGetJSON(apiBaseURL, token, path, &existing); err == nil && len(existing) > 0 {
		payload := map[string]any{
			"title": manifest.TargetTitle,
			"body":  buildDraftPullRequestBody(manifest, branchName),
			"draft": true,
			"base":  manifest.DefaultBranch,
		}
		var updated githubPullRequestResponse
		if err := githubAPIRequestJSON("PATCH", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d", manifest.RepoSlug, existing[0].Number), payload, &updated); err != nil {
			return githubPullRequestResponse{}, false, err
		}
		return updated, false, nil
	}
	payload := map[string]any{
		"title": manifest.TargetTitle,
		"head":  fmt.Sprintf("%s:%s", manifest.RepoOwner, branchName),
		"base":  manifest.DefaultBranch,
		"body":  buildDraftPullRequestBody(manifest, branchName),
		"draft": true,
	}
	var created githubPullRequestResponse
	if err := githubAPIRequestJSON("POST", apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls", manifest.RepoSlug), payload, &created); err != nil {
		return githubPullRequestResponse{}, false, err
	}
	return created, true, nil
}

func buildDraftPullRequestBody(manifest githubWorkManifest, branchName string) string {
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

func readGithubCIState(repoSlug string, headSHA string, apiBaseURL string, token string) (string, error) {
	var checks githubCheckRunsResponse
	_ = githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repoSlug, url.QueryEscape(headSHA)), &checks)
	var runs githubWorkflowRunsResponse
	_ = githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/actions/runs?head_sha=%s&per_page=100", repoSlug, url.QueryEscape(headSHA)), &runs)
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
	if hasAny && !hasPending && !hasFailures {
		return "ci_green", nil
	}
	if hasFailures {
		return "blocked", nil
	}
	return "ci_waiting", nil
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
	return json.NewDecoder(resp.Body).Decode(target)
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
