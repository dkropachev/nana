package gocli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResolveLiveGithubTestConfigRequiresTokenWhenEnabled(t *testing.T) {
	config, err := resolveLiveGithubTestConfig(func(key string) string {
		switch key {
		case liveGithubEnabledEnv:
			return "1"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatalf("expected missing token error, got nil (config=%+v)", config)
	}
	if !strings.Contains(err.Error(), "GH_TOKEN or GITHUB_TOKEN is required") {
		t.Fatalf("expected missing token error, got %v", err)
	}
}

func TestResolveLiveGithubTestConfigUsesFallbackTokenAndDefaultAPIBaseURL(t *testing.T) {
	config, err := resolveLiveGithubTestConfig(func(key string) string {
		switch key {
		case liveGithubEnabledEnv:
			return "true"
		case "GITHUB_TOKEN":
			return "fallback-token"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("resolveLiveGithubTestConfig: %v", err)
	}
	if !config.Enabled {
		t.Fatalf("expected live config enabled, got %+v", config)
	}
	if config.Token != "fallback-token" {
		t.Fatalf("expected fallback token, got %q", config.Token)
	}
	if config.APIBaseURL != "https://api.github.com" {
		t.Fatalf("expected default api base url, got %q", config.APIBaseURL)
	}
}

func TestResolveLiveGithubHelpersUsePreservedOptionalTestEnv(t *testing.T) {
	original := preservedOptionalTestEnv
	preservedOptionalTestEnv = map[string]string{
		liveGithubEnabledEnv:         "1",
		"GH_TOKEN":                   "preserved-token",
		"GITHUB_API_URL":             "https://ghe.example.com/api/v3",
		"NANA_LIVE_REPO_FORK_TARGET": "acme/custom-fork-target",
		"NANA_LIVE_REPO_REPO":        "acme/custom-repo",
		"NANA_LIVE_REPO_DISABLED":    "acme/custom-disabled",
	}
	t.Cleanup(func() {
		preservedOptionalTestEnv = original
	})

	config, err := resolveLiveGithubTestConfig(optionalTestEnv)
	if err != nil {
		t.Fatalf("resolveLiveGithubTestConfig(optionalTestEnv): %v", err)
	}
	if !config.Enabled || config.Token != "preserved-token" || config.APIBaseURL != "https://ghe.example.com/api/v3" {
		t.Fatalf("expected preserved live config, got %+v", config)
	}
	if repo := liveGithubRepoFromEnv("NANA_LIVE_REPO_REPO", "fallback/repo"); repo != "acme/custom-repo" {
		t.Fatalf("expected preserved repo slug, got %q", repo)
	}
	if repo := liveGithubRepoFromEnv("NANA_LIVE_REPO_DISABLED", "fallback/disabled"); repo != "acme/custom-disabled" {
		t.Fatalf("expected preserved disabled repo slug, got %q", repo)
	}
	repos := resolveLiveGithubRepos(optionalTestEnv, "viewer")
	if repos.ForkTarget != "acme/custom-fork-target" {
		t.Fatalf("expected preserved fork target slug, got %+v", repos)
	}
	if repos.Fork != "viewer/custom-fork-target" {
		t.Fatalf("expected derived fork slug from fork target repo name, got %+v", repos)
	}
}

func TestResolveLiveGithubReposPrefersExplicitForkOverride(t *testing.T) {
	repos := resolveLiveGithubRepos(func(key string) string {
		switch key {
		case "NANA_LIVE_REPO_FORK_TARGET":
			return "acme/custom-fork-target"
		case "NANA_LIVE_REPO_FORK":
			return "octocat/explicit-fork"
		default:
			return ""
		}
	}, "viewer")
	if repos.ForkTarget != "acme/custom-fork-target" {
		t.Fatalf("expected custom fork target, got %+v", repos)
	}
	if repos.Fork != "octocat/explicit-fork" {
		t.Fatalf("expected explicit fork override to win, got %+v", repos)
	}
}

func TestLiveGithubDefaultForkRepoUsesForkTargetRepoName(t *testing.T) {
	if got := liveGithubDefaultForkRepo("viewer", "acme/custom-fork-target"); got != "viewer/custom-fork-target" {
		t.Fatalf("expected fork target repo name in default fork slug, got %q", got)
	}
	if got := liveGithubDefaultForkRepo(" viewer ", " acme/custom-fork-target "); got != "viewer/custom-fork-target" {
		t.Fatalf("expected trimmed default fork slug, got %q", got)
	}
}

func TestLiveGithubEnsureForkForSourceUsesExplicitForkOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/octocat/explicit-fork":
			_, _ = w.Write([]byte(`{"name":"explicit-fork","full_name":"octocat/explicit-fork","clone_url":"https://example.invalid/octocat/explicit-fork.git"}`))
		case "PATCH /repos/octocat/explicit-fork":
			w.WriteHeader(http.StatusNoContent)
		case "PUT /repos/octocat/explicit-fork/actions/permissions":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s", r.Method, r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	err := liveGithubEnsureForkForSource(liveGithubTestEnv{
		Viewer:     "viewer",
		APIBaseURL: server.URL,
		Token:      "token",
		Repos: liveGithubRepos{
			Fork: "octocat/explicit-fork",
		},
	}, "acme/widget")
	if err != nil {
		t.Fatalf("liveGithubEnsureForkForSource: %v", err)
	}
}

func TestLiveGithubHostForAPIBase(t *testing.T) {
	testCases := []struct {
		name       string
		apiBaseURL string
		want       string
	}{
		{name: "default github", apiBaseURL: "", want: "github.com"},
		{name: "enterprise api prefix", apiBaseURL: "https://api.ghe.example.com:8443/api/v3", want: "ghe.example.com:8443"},
		{name: "enterprise direct host", apiBaseURL: "https://ghe.example.com/api/v3", want: "ghe.example.com"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveGithubHostForAPIBase(tc.apiBaseURL); got != tc.want {
				t.Fatalf("liveGithubHostForAPIBase(%q)=%q want %q", tc.apiBaseURL, got, tc.want)
			}
		})
	}
}

func TestLiveGithubConfigureGitAuthRewritesSSHRemotesAndOverridesEnvCredentialHelpers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GH_TOKEN", "live-test-token")
	t.Setenv("GITHUB_TOKEN", "live-test-token")
	badHelper := filepath.Join(home, "bad-credential-helper.sh")
	writeExecutable(t, badHelper, strings.Join([]string{
		"#!/bin/sh",
		"printf '%s\\n' 'username=env-helper'",
		"printf '%s\\n' 'password=env-helper-token'",
	}, "\n"))
	t.Setenv("GIT_CONFIG_PARAMETERS", fmt.Sprintf("'credential.helper=%s'", badHelper))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", badHelper)

	liveGithubConfigureGitAuth(t, liveGithubTestEnv{
		APIBaseURL: "https://api.github.com",
		Token:      "live-test-token",
	}, home)

	if got := os.Getenv("GIT_TERMINAL_PROMPT"); got != "0" {
		t.Fatalf("expected terminal prompts disabled, got %q", got)
	}
	askPass := strings.TrimSpace(os.Getenv("GIT_ASKPASS"))
	if askPass == "" {
		t.Fatal("expected GIT_ASKPASS to be configured")
	}
	if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "1" {
		t.Fatalf("expected system git config disabled, got %q", got)
	}
	if got := os.Getenv("GIT_CONFIG_PARAMETERS"); got != "" {
		t.Fatalf("expected git config parameters cleared, got %q", got)
	}
	if got := os.Getenv("GIT_CONFIG_COUNT"); got != "2" {
		t.Fatalf("expected git config override count, got %q", got)
	}

	gotRewriteSources := strings.Fields(runLocalWorkTestGitOutput(t, "", "config", "--global", "--get-all", "url.https://github.com/.insteadOf"))
	if !reflect.DeepEqual(gotRewriteSources, []string{"git@github.com:", "ssh://git@github.com/"}) {
		t.Fatalf("expected SSH rewrite sources, got %v", gotRewriteSources)
	}

	credentialCmd := exec.Command("git", "credential", "fill")
	credentialCmd.Env = os.Environ()
	credentialCmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	credential, err := credentialCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git credential fill failed: %v\n%s", err, credential)
	}
	credentialText := string(credential)
	if !strings.Contains(credentialText, "username=x-access-token\n") {
		t.Fatalf("expected askpass username in credential fill, got %q", credentialText)
	}
	if !strings.Contains(credentialText, "password=live-test-token\n") {
		t.Fatalf("expected askpass token in credential fill, got %q", credentialText)
	}
	if strings.Contains(credentialText, "env-helper") {
		t.Fatalf("expected env helper to be ignored, got %q", credentialText)
	}
}

func TestValidateLiveGithubPublisherManifestMode(t *testing.T) {
	manifest := githubWorkManifest{
		RepoMode:           "repo",
		PublishTarget:      "repo",
		CreatePROnComplete: true,
	}
	if err := validateLiveGithubPublisherManifestMode(manifest, "repo"); err != nil {
		t.Fatalf("validateLiveGithubPublisherManifestMode(match): %v", err)
	}
	manifest.PublishTarget = "local-branch"
	manifest.CreatePROnComplete = false
	if err := validateLiveGithubPublisherManifestMode(manifest, "repo"); err == nil {
		t.Fatal("expected mismatched manifest metadata to fail validation")
	}
}

func TestPatchLiveGithubPublisherManifestOnlySetsHeadRef(t *testing.T) {
	t.Setenv("NANA_LIVE_REPO_FORK", "")
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	initial := githubWorkManifest{
		RunID:              "gh-run-123",
		RepoMode:           "fork",
		PublishTarget:      "fork",
		CreatePROnComplete: true,
		PublishedPRHeadRef: "",
		PublicationState:   "queued",
		PublicationError:   "keep-me",
		ReviewRequestState: "pending",
	}
	if err := writeGithubJSON(manifestPath, initial); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	updated := patchLiveGithubPublisherManifest(t, manifestPath, "marker-branch", "fork")
	if updated.PublishTarget != "fork" || !updated.CreatePROnComplete || updated.RepoMode != "fork" {
		t.Fatalf("expected routing metadata preserved, got %+v", updated)
	}
	if updated.PublishedPRHeadRef != "marker-branch" {
		t.Fatalf("expected published head ref patched, got %q", updated.PublishedPRHeadRef)
	}
	reloaded, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if reloaded.PublishTarget != initial.PublishTarget || reloaded.CreatePROnComplete != initial.CreatePROnComplete || reloaded.RepoMode != initial.RepoMode {
		t.Fatalf("expected routing metadata unchanged on disk, got %+v", reloaded)
	}
	if reloaded.PublicationError != initial.PublicationError || reloaded.ReviewRequestState != initial.ReviewRequestState {
		t.Fatalf("expected unrelated manifest fields preserved, got %+v", reloaded)
	}
	if reloaded.PublishedPRHeadRef != "marker-branch" {
		t.Fatalf("expected marker branch on disk, got %q", reloaded.PublishedPRHeadRef)
	}
}

func TestPatchLiveGithubPublisherManifestAppliesExplicitForkOverride(t *testing.T) {
	t.Setenv("NANA_LIVE_REPO_FORK", "octocat/explicit-fork")
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	initial := githubWorkManifest{
		RunID:              "gh-run-123",
		RepoMode:           "fork",
		PublishTarget:      "fork",
		CreatePROnComplete: true,
	}
	if err := writeGithubJSON(manifestPath, initial); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	updated := patchLiveGithubPublisherManifest(t, manifestPath, "marker-branch", "fork")
	if updated.PublishRepoSlug != "octocat/explicit-fork" || updated.PublishRepoOwner != "octocat" {
		t.Fatalf("expected explicit fork override on manifest, got %+v", updated)
	}
	reloaded, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if reloaded.PublishRepoSlug != "octocat/explicit-fork" || reloaded.PublishRepoOwner != "octocat" {
		t.Fatalf("expected explicit fork override persisted, got %+v", reloaded)
	}
	if reloaded.PublishedPRHeadRef != "marker-branch" {
		t.Fatalf("expected marker branch on disk, got %q", reloaded.PublishedPRHeadRef)
	}
}

func TestRepointLiveGithubSandboxOriginToRepoUsesAPIHost(t *testing.T) {
	repo := t.TempDir()
	runLocalWorkTestGit(t, repo, "init")
	runLocalWorkTestGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")

	repointLiveGithubSandboxOriginToRepo(t, liveGithubTestEnv{APIBaseURL: "https://api.ghe.example.com:8443/api/v3"}, githubWorkManifest{
		RepoSlug:        "acme/widget",
		SandboxRepoPath: repo,
	})

	want := "https://ghe.example.com:8443/acme/widget.git"
	if got := strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "remote", "get-url", "origin")); got != want {
		t.Fatalf("expected origin url %q, got %q", want, got)
	}
	if got := strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "remote", "get-url", "--push", "origin")); got != want {
		t.Fatalf("expected push origin url %q, got %q", want, got)
	}
}

func TestLiveGithubCleanupStaleArtifactsPreservesRecentTaggedArtifacts(t *testing.T) {
	now := time.Date(2026, time.April, 24, 18, 0, 0, 0, time.UTC)
	repoSlug := "acme/repo"
	recorder := newLiveGithubCleanupRecorder(repoSlug, "main", "main-sha")
	recorder.pulls[101] = &liveGithubPullDetails{
		Number:    101,
		Title:     liveGithubTaggedTitle("recent-run", "recent pr"),
		Body:      liveGithubTaggedBody("recent-run", "still running"),
		State:     "open",
		UpdatedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
	}
	recorder.issues[202] = &startWorkIssuePayload{
		Number:    202,
		Title:     liveGithubTaggedTitle("recent-run", "recent issue"),
		Body:      liveGithubTaggedBody("recent-run", "still running"),
		State:     "open",
		UpdatedAt: now.Add(-45 * time.Minute).Format(time.RFC3339),
	}
	recorder.branches[liveGithubBranchName("recent", "1")] = liveGithubBranchDetails{Name: liveGithubBranchName("recent", "1"), Commit: struct {
		SHA string `json:"sha"`
	}{SHA: "recent-sha"}}
	recorder.commitDates["recent-sha"] = now.Add(-20 * time.Minute).Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(recorder.handle))
	defer server.Close()

	env := liveGithubTestEnv{APIBaseURL: server.URL, Token: "test-token", Repos: liveGithubRepos{Local: repoSlug}}
	if err := liveGithubCleanupStaleArtifactsAt(env, now); err != nil {
		t.Fatalf("liveGithubCleanupStaleArtifactsAt: %v", err)
	}
	if len(recorder.closedPulls) != 0 {
		t.Fatalf("expected recent pull to remain open, got closed=%v", recorder.closedPulls)
	}
	if len(recorder.closedIssues) != 0 {
		t.Fatalf("expected recent issue to remain open, got closed=%v", recorder.closedIssues)
	}
	if len(recorder.deletedBranches) != 0 {
		t.Fatalf("expected recent branch to remain, got deleted=%v", recorder.deletedBranches)
	}
}

func TestLiveGithubCleanupStaleArtifactsRemovesOnlyOldTaggedArtifacts(t *testing.T) {
	now := time.Date(2026, time.April, 24, 18, 0, 0, 0, time.UTC)
	repoSlug := "acme/repo"
	recorder := newLiveGithubCleanupRecorder(repoSlug, "main", "main-sha")
	recorder.pulls[101] = &liveGithubPullDetails{
		Number:    101,
		Title:     liveGithubTaggedTitle("stale-run", "stale pr"),
		Body:      liveGithubTaggedBody("stale-run", "old artifact"),
		State:     "open",
		UpdatedAt: now.Add(-3 * time.Hour).Format(time.RFC3339),
	}
	recorder.pulls[102] = &liveGithubPullDetails{
		Number:    102,
		Title:     "untagged pr",
		Body:      "leave me alone",
		State:     "open",
		UpdatedAt: now.Add(-4 * time.Hour).Format(time.RFC3339),
	}
	recorder.issues[202] = &startWorkIssuePayload{
		Number:    202,
		Title:     liveGithubTaggedTitle("stale-run", "stale issue"),
		Body:      liveGithubTaggedBody("stale-run", "old artifact"),
		State:     "open",
		UpdatedAt: now.Add(-3 * time.Hour).Format(time.RFC3339),
	}
	recorder.issues[203] = &startWorkIssuePayload{
		Number:    203,
		Title:     "untagged issue",
		Body:      "leave me alone",
		State:     "open",
		UpdatedAt: now.Add(-5 * time.Hour).Format(time.RFC3339),
	}
	recorder.branches[liveGithubBranchName("stale", "1")] = liveGithubBranchDetails{Name: liveGithubBranchName("stale", "1"), Commit: struct {
		SHA string `json:"sha"`
	}{SHA: "stale-sha"}}
	recorder.branches["feature/legacy"] = liveGithubBranchDetails{Name: "feature/legacy", Commit: struct {
		SHA string `json:"sha"`
	}{SHA: "feature-sha"}}
	recorder.commitDates["stale-sha"] = now.Add(-3 * time.Hour).Format(time.RFC3339)
	recorder.commitDates["feature-sha"] = now.Add(-4 * time.Hour).Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(recorder.handle))
	defer server.Close()

	env := liveGithubTestEnv{APIBaseURL: server.URL, Token: "test-token", Repos: liveGithubRepos{Local: repoSlug}}
	if err := liveGithubCleanupStaleArtifactsAt(env, now); err != nil {
		t.Fatalf("liveGithubCleanupStaleArtifactsAt: %v", err)
	}
	sort.Ints(recorder.closedPulls)
	sort.Ints(recorder.closedIssues)
	sort.Strings(recorder.deletedBranches)
	if len(recorder.closedPulls) != 1 || recorder.closedPulls[0] != 101 {
		t.Fatalf("expected only tagged stale pull to close, got %v", recorder.closedPulls)
	}
	if len(recorder.closedIssues) != 1 || recorder.closedIssues[0] != 202 {
		t.Fatalf("expected only tagged stale issue to close, got %v", recorder.closedIssues)
	}
	if len(recorder.deletedBranches) != 1 || recorder.deletedBranches[0] != liveGithubBranchName("stale", "1") {
		t.Fatalf("expected only tagged stale branch to delete, got %v", recorder.deletedBranches)
	}
}

func TestLiveGithubCleanupStaleArtifactsScansAllPages(t *testing.T) {
	now := time.Date(2026, time.April, 24, 18, 0, 0, 0, time.UTC)
	repoSlug := "acme/repo"
	recorder := newLiveGithubCleanupRecorder(repoSlug, "main", "main-sha")
	for i := 1; i <= 100; i++ {
		recorder.pulls[i] = &liveGithubPullDetails{
			Number:    i,
			Title:     fmt.Sprintf("untagged pull %03d", i),
			Body:      "leave me alone",
			State:     "open",
			UpdatedAt: now.Add(-4 * time.Hour).Format(time.RFC3339),
		}
		recorder.issues[i] = &startWorkIssuePayload{
			Number:    i,
			Title:     fmt.Sprintf("untagged issue %03d", i),
			Body:      "leave me alone",
			State:     "open",
			UpdatedAt: now.Add(-4 * time.Hour).Format(time.RFC3339),
		}
		branchName := fmt.Sprintf("feature/%03d", i)
		recorder.branches[branchName] = liveGithubBranchDetails{Name: branchName, Commit: struct {
			SHA string `json:"sha"`
		}{SHA: fmt.Sprintf("feature-sha-%03d", i)}}
		recorder.commitDates[fmt.Sprintf("feature-sha-%03d", i)] = now.Add(-4 * time.Hour).Format(time.RFC3339)
	}
	recorder.pulls[999] = &liveGithubPullDetails{
		Number:    999,
		Title:     liveGithubTaggedTitle("stale-page-two", "stale pr"),
		Body:      liveGithubTaggedBody("stale-page-two", "page two"),
		State:     "open",
		UpdatedAt: now.Add(-4 * time.Hour).Format(time.RFC3339),
	}
	recorder.issues[999] = &startWorkIssuePayload{
		Number:    999,
		Title:     liveGithubTaggedTitle("stale-page-two", "stale issue"),
		Body:      liveGithubTaggedBody("stale-page-two", "page two"),
		State:     "open",
		UpdatedAt: now.Add(-4 * time.Hour).Format(time.RFC3339),
	}
	staleBranchName := liveGithubBranchName("stale-page-two", "1")
	recorder.branches[staleBranchName] = liveGithubBranchDetails{Name: staleBranchName, Commit: struct {
		SHA string `json:"sha"`
	}{SHA: "stale-page-two-sha"}}
	recorder.commitDates["stale-page-two-sha"] = now.Add(-4 * time.Hour).Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(recorder.handle))
	defer server.Close()

	env := liveGithubTestEnv{APIBaseURL: server.URL, Token: "test-token", Repos: liveGithubRepos{Local: repoSlug}}
	if err := liveGithubCleanupStaleArtifactsAt(env, now); err != nil {
		t.Fatalf("liveGithubCleanupStaleArtifactsAt: %v", err)
	}
	sort.Ints(recorder.closedPulls)
	sort.Ints(recorder.closedIssues)
	sort.Strings(recorder.deletedBranches)
	if len(recorder.closedPulls) != 1 || recorder.closedPulls[0] != 999 {
		t.Fatalf("expected stale pull from page two to close, got %v", recorder.closedPulls)
	}
	if len(recorder.closedIssues) != 1 || recorder.closedIssues[0] != 999 {
		t.Fatalf("expected stale issue from page two to close, got %v", recorder.closedIssues)
	}
	if len(recorder.deletedBranches) != 1 || recorder.deletedBranches[0] != staleBranchName {
		t.Fatalf("expected stale branch from page two to delete, got %v", recorder.deletedBranches)
	}
}

type liveGithubCleanupRecorder struct {
	repoSlug         string
	defaultBranch    string
	defaultBranchSHA string
	pulls            map[int]*liveGithubPullDetails
	issues           map[int]*startWorkIssuePayload
	branches         map[string]liveGithubBranchDetails
	commitDates      map[string]string
	closedPulls      []int
	closedIssues     []int
	deletedBranches  []string
}

func newLiveGithubCleanupRecorder(repoSlug string, defaultBranch string, defaultBranchSHA string) *liveGithubCleanupRecorder {
	recorder := &liveGithubCleanupRecorder{
		repoSlug:         repoSlug,
		defaultBranch:    defaultBranch,
		defaultBranchSHA: defaultBranchSHA,
		pulls:            map[int]*liveGithubPullDetails{},
		issues:           map[int]*startWorkIssuePayload{},
		branches:         map[string]liveGithubBranchDetails{},
		commitDates:      map[string]string{},
	}
	recorder.branches[defaultBranch] = liveGithubBranchDetails{Name: defaultBranch, Commit: struct {
		SHA string `json:"sha"`
	}{SHA: defaultBranchSHA}}
	recorder.commitDates[defaultBranchSHA] = time.Date(2026, time.April, 24, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	return recorder
}

func (r *liveGithubCleanupRecorder) handle(w http.ResponseWriter, req *http.Request) {
	base := "/repos/" + r.repoSlug
	switch {
	case req.Method == http.MethodGet && req.URL.Path == base:
		writeLiveGithubTestJSON(w, map[string]any{"default_branch": r.defaultBranch})
	case req.Method == http.MethodGet && req.URL.Path == base+"/pulls":
		var pulls []liveGithubPullDetails
		for _, pull := range r.pulls {
			if strings.EqualFold(strings.TrimSpace(pull.State), "open") {
				pulls = append(pulls, *pull)
			}
		}
		sort.Slice(pulls, func(i int, j int) bool { return pulls[i].Number < pulls[j].Number })
		writeLiveGithubTestJSON(w, paginateLiveGithubCleanupResults(req, pulls))
	case strings.HasPrefix(req.URL.Path, base+"/pulls/"):
		number, ok := parseLiveGithubNumber(req.URL.Path, base+"/pulls/")
		if !ok {
			http.NotFound(w, req)
			return
		}
		pull, ok := r.pulls[number]
		if !ok {
			http.NotFound(w, req)
			return
		}
		switch req.Method {
		case http.MethodGet:
			writeLiveGithubTestJSON(w, pull)
		case http.MethodPatch:
			pull.State = "closed"
			r.closedPulls = append(r.closedPulls, number)
			writeLiveGithubTestJSON(w, pull)
		default:
			http.NotFound(w, req)
		}
	case req.Method == http.MethodGet && req.URL.Path == base+"/issues":
		var issues []startWorkIssuePayload
		for _, issue := range r.issues {
			if strings.EqualFold(strings.TrimSpace(issue.State), "open") {
				issues = append(issues, *issue)
			}
		}
		sort.Slice(issues, func(i int, j int) bool { return issues[i].Number < issues[j].Number })
		writeLiveGithubTestJSON(w, paginateLiveGithubCleanupResults(req, issues))
	case strings.HasPrefix(req.URL.Path, base+"/issues/"):
		number, ok := parseLiveGithubNumber(req.URL.Path, base+"/issues/")
		if !ok {
			http.NotFound(w, req)
			return
		}
		issue, ok := r.issues[number]
		if !ok {
			http.NotFound(w, req)
			return
		}
		switch req.Method {
		case http.MethodGet:
			writeLiveGithubTestJSON(w, issue)
		case http.MethodPatch:
			issue.State = "closed"
			r.closedIssues = append(r.closedIssues, number)
			writeLiveGithubTestJSON(w, issue)
		default:
			http.NotFound(w, req)
		}
	case req.Method == http.MethodGet && req.URL.Path == base+"/branches":
		var branches []liveGithubBranchDetails
		for _, branch := range r.branches {
			branches = append(branches, branch)
		}
		sort.Slice(branches, func(i int, j int) bool { return branches[i].Name < branches[j].Name })
		writeLiveGithubTestJSON(w, paginateLiveGithubCleanupResults(req, branches))
	case strings.HasPrefix(req.URL.Path, base+"/branches/"):
		name, err := url.PathUnescape(strings.TrimPrefix(req.URL.Path, base+"/branches/"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		branch, ok := r.branches[name]
		if !ok {
			http.NotFound(w, req)
			return
		}
		writeLiveGithubTestJSON(w, branch)
	case strings.HasPrefix(req.URL.Path, base+"/commits/"):
		sha, err := url.PathUnescape(strings.TrimPrefix(req.URL.Path, base+"/commits/"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		date, ok := r.commitDates[sha]
		if !ok {
			http.NotFound(w, req)
			return
		}
		writeLiveGithubTestJSON(w, map[string]any{"commit": map[string]any{"committer": map[string]any{"date": date}}})
	case req.Method == http.MethodDelete && strings.HasPrefix(req.URL.Path, base+"/git/refs/heads/"):
		name, err := url.PathUnescape(strings.TrimPrefix(req.URL.Path, base+"/git/refs/heads/"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok := r.branches[name]; !ok {
			http.NotFound(w, req)
			return
		}
		delete(r.branches, name)
		r.deletedBranches = append(r.deletedBranches, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, req)
	}
}

func paginateLiveGithubCleanupResults[T any](req *http.Request, items []T) []T {
	query := req.URL.Query()
	page := liveGithubCleanupQueryInt(query, "page", 1)
	if page < 1 {
		page = 1
	}
	perPage := liveGithubCleanupQueryInt(query, "per_page", 100)
	if perPage < 1 {
		perPage = 100
	}
	start := (page - 1) * perPage
	if start >= len(items) {
		return []T{}
	}
	end := start + perPage
	if end > len(items) {
		end = len(items)
	}
	return append([]T(nil), items[start:end]...)
}

func liveGithubCleanupQueryInt(query url.Values, key string, fallback int) int {
	value := strings.TrimSpace(query.Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseLiveGithubNumber(path string, prefix string) (int, bool) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(path, prefix))
	number, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}
	return number, true
}

func writeLiveGithubTestJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		panic(err)
	}
}
