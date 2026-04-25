package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	liveGithubEnabledEnv             = "NANA_LIVE_GITHUB"
	liveGithubTitlePrefix            = "[nana-live-ci]"
	liveGithubBodyMarker             = "nana-live-ci/"
	liveGithubBranchPrefix           = "nana-live-ci/"
	liveGithubStaleMinAge            = 2 * time.Hour
	liveGithubFakeCodexModeEnv       = "NANA_LIVE_FAKE_CODEX_MODE"
	liveGithubFakeCodexModeSuccess   = "success"
	liveGithubFakeCodexModeImplement = "implement_fail"
	liveGithubFakeCodexModePublisher = "publisher_fail"
)

type liveGithubRepos struct {
	Local      string
	ForkTarget string
	Fork       string
	Repo       string
	Disabled   string
}

type liveGithubTestEnv struct {
	APIBaseURL string
	Token      string
	Viewer     string
	Repos      liveGithubRepos
}

type liveGithubTestConfig struct {
	Enabled    bool
	APIBaseURL string
	Token      string
}

type liveGithubPullDetails struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Head      struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

type liveGithubBranchDetails struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type liveGithubRepoSnapshot struct {
	RepoSlug         string
	DefaultBranch    string
	DefaultBranchSHA string
}

type liveGithubCommitDetails struct {
	Commit struct {
		Author struct {
			Date string `json:"date"`
		} `json:"author"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

var (
	liveGithubStaleCleanupOnce sync.Once
	liveGithubStaleCleanupErr  error
)

func requireLiveGithubTestEnv(t *testing.T) liveGithubTestEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("live GitHub smoke skipped in -short mode")
	}
	config, err := resolveLiveGithubTestConfig(optionalTestEnv)
	if err != nil {
		t.Fatalf("resolve live GitHub smoke config: %v", err)
	}
	if !config.Enabled {
		t.Skip("set NANA_LIVE_GITHUB=1 to run live GitHub smoke tests")
	}
	viewer, err := githubCurrentViewer(config.APIBaseURL, config.Token)
	if err != nil {
		t.Fatalf("resolve GitHub viewer: %v", err)
	}
	env := liveGithubTestEnv{
		APIBaseURL: config.APIBaseURL,
		Token:      config.Token,
		Viewer:     strings.TrimSpace(viewer),
	}
	env.Repos = resolveLiveGithubRepos(optionalTestEnv, env.Viewer)
	if err := liveGithubEnsureForkForSource(env, env.Repos.ForkTarget); err != nil {
		t.Fatalf("ensure live fork repo: %v", err)
	}
	for _, repoSlug := range []string{env.Repos.Local, env.Repos.ForkTarget, env.Repos.Fork, env.Repos.Repo, env.Repos.Disabled} {
		repoSlug = strings.TrimSpace(repoSlug)
		if repoSlug == "" {
			continue
		}
		if _, _, err := liveGithubDefaultBranchSnapshotForRepo(env, repoSlug); err != nil {
			t.Fatalf("live GitHub repo preflight %s: %v", repoSlug, err)
		}
	}
	liveGithubStaleCleanupOnce.Do(func() {
		liveGithubStaleCleanupErr = liveGithubCleanupStaleArtifacts(env)
	})
	if liveGithubStaleCleanupErr != nil {
		t.Fatalf("live GitHub stale artifact cleanup: %v", liveGithubStaleCleanupErr)
	}
	return env
}

func resolveLiveGithubTestConfig(getenv func(string) string) (liveGithubTestConfig, error) {
	value := strings.ToLower(strings.TrimSpace(getenv(liveGithubEnabledEnv)))
	if value != "1" && value != "true" && value != "yes" {
		return liveGithubTestConfig{}, nil
	}
	token := strings.TrimSpace(getenv("GH_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(getenv("GITHUB_TOKEN"))
	}
	if token == "" {
		return liveGithubTestConfig{}, fmt.Errorf("GH_TOKEN or GITHUB_TOKEN is required when %s is enabled", liveGithubEnabledEnv)
	}
	apiBaseURL := strings.TrimSpace(getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	return liveGithubTestConfig{
		Enabled:    true,
		APIBaseURL: apiBaseURL,
		Token:      token,
	}, nil
}

func liveGithubRepoFromEnv(key string, fallback string) string {
	return liveGithubRepoFromEnvWith(optionalTestEnv, key, fallback)
}

func liveGithubRepoFromEnvWith(getenv func(string) string, key string, fallback string) string {
	if value := strings.TrimSpace(getenv(key)); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func resolveLiveGithubRepos(getenv func(string) string, viewer string) liveGithubRepos {
	forkTarget := liveGithubRepoFromEnvWith(getenv, "NANA_LIVE_REPO_FORK_TARGET", "nana-harness/cicd-mock-repo-fork-target")
	return liveGithubRepos{
		Local:      liveGithubRepoFromEnvWith(getenv, "NANA_LIVE_REPO_LOCAL", "nana-harness/cicd-mock-repo-local"),
		ForkTarget: forkTarget,
		Fork:       liveGithubRepoFromEnvWith(getenv, "NANA_LIVE_REPO_FORK", liveGithubDefaultForkRepo(viewer, forkTarget)),
		Repo:       liveGithubRepoFromEnvWith(getenv, "NANA_LIVE_REPO_REPO", "nana-harness/cicd-mock-repo-repo"),
		Disabled:   liveGithubRepoFromEnvWith(getenv, "NANA_LIVE_REPO_DISABLED", "nana-harness/cicd-mock-repo-disabled"),
	}
}

func liveGithubDefaultForkRepo(viewer string, forkTarget string) string {
	viewer = strings.TrimSpace(viewer)
	repoName := liveGithubRepoNameFromSlug(forkTarget)
	if viewer == "" || repoName == "" {
		return ""
	}
	return viewer + "/" + repoName
}

func liveGithubRepoNameFromSlug(repoSlug string) string {
	repoSlug = strings.Trim(strings.TrimSpace(repoSlug), "/")
	if repoSlug == "" {
		return ""
	}
	if index := strings.LastIndex(repoSlug, "/"); index >= 0 {
		repoSlug = repoSlug[index+1:]
	}
	return strings.TrimSpace(repoSlug)
}

func liveGithubSetupIsolatedHome(t *testing.T, env liveGithubTestEnv) string {
	t.Helper()
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	xdgConfigHome := filepath.Join(home, ".config")
	xdgCacheHome := filepath.Join(home, ".cache")
	xdgStateHome := filepath.Join(home, ".local", "state")
	for _, dir := range []string{codexHome, xdgConfigHome, xdgCacheHome, xdgStateHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir isolated home dir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)
	t.Setenv("XDG_CACHE_HOME", xdgCacheHome)
	t.Setenv("XDG_STATE_HOME", xdgStateHome)
	t.Setenv("GH_TOKEN", env.Token)
	t.Setenv("GITHUB_TOKEN", env.Token)
	if env.APIBaseURL != "" {
		t.Setenv("GITHUB_API_URL", env.APIBaseURL)
	}
	liveGithubConfigureGitAuth(t, env, home)
	liveGithubInstallFakeGH(t, home)
	return home
}

func liveGithubConfigureGitAuth(t *testing.T, env liveGithubTestEnv, home string) {
	t.Helper()
	host := liveGithubHostForAPIBase(env.APIBaseURL)
	liveGithubInstallGitAskPass(t, home)
	base := fmt.Sprintf("https://%s/", host)
	for _, source := range []string{
		fmt.Sprintf("git@%s:", host),
		fmt.Sprintf("ssh://git@%s/", host),
	} {
		cmd := exec.Command("git", "config", "--global", "--add", fmt.Sprintf("url.%s.insteadOf", base), source)
		cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git config url rewrite %s: %v\n%s", source, err, output)
		}
	}
}

func liveGithubInstallGitAskPass(t *testing.T, home string) string {
	t.Helper()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir git askpass bin: %v", err)
	}
	scriptPath := filepath.Join(binDir, "git-askpass-live-github")
	writeExecutable(t, scriptPath, strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`prompt="${1:-}"`,
		`case "$prompt" in`,
		`  *[Uu]sername*) printf '%s\n' 'x-access-token' ;;`,
		`  *[Pp]assword*) printf '%s\n' "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ;;`,
		`  *) printf '%s\n' "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ;;`,
		`esac`,
	}, "\n"))
	t.Setenv("GIT_ASKPASS", scriptPath)
	t.Setenv("SSH_ASKPASS", scriptPath)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GCM_INTERACTIVE", "never")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "")
	t.Setenv("GIT_CONFIG_KEY_1", "core.askPass")
	t.Setenv("GIT_CONFIG_VALUE_1", scriptPath)
	return scriptPath
}

func liveGithubHostForAPIBase(apiBaseURL string) string {
	host := "github.com"
	if parsed, err := url.Parse(strings.TrimSpace(apiBaseURL)); err == nil && strings.TrimSpace(parsed.Host) != "" {
		host = strings.TrimSpace(parsed.Host)
		if strings.HasPrefix(host, "api.") {
			host = strings.TrimPrefix(host, "api.")
		}
	}
	return host
}

func liveGithubInstallFakeGH(t *testing.T, home string) {
	t.Helper()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir fake gh bin: %v", err)
	}
	writeExecutable(t, filepath.Join(binDir, "gh"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`if [ "$#" -ge 2 ] && [ "$1" = "auth" ] && [ "$2" = "status" ]; then exit 0; fi`,
		`if [ "$#" -ge 2 ] && [ "$1" = "auth" ] && [ "$2" = "token" ]; then printf '%s\n' "${GH_TOKEN:-${GITHUB_TOKEN:-}}"; exit 0; fi`,
		`printf 'unsupported fake gh command: %s\n' "$*" >&2`,
		"exit 1",
	}, "\n"))
	prependPathEnv(t, binDir)
}

func prepareLiveGithubManagedCheckout(t *testing.T, repoSlug string) string {
	t.Helper()
	sourcePath, err := ensureGithubManagedCheckout(repoSlug, repoAccessLockOwner{
		Backend: "github-live-test",
		RunID:   sanitizePathToken(repoSlug),
		Purpose: "managed-checkout",
		Label:   "github-live-test-managed-checkout",
	})
	if err != nil {
		t.Fatalf("ensure managed checkout for %s: %v", repoSlug, err)
	}
	return sourcePath
}

func liveGithubEnsureForkReady(t *testing.T, env liveGithubTestEnv, sourceRepo string) {
	t.Helper()
	if err := liveGithubEnsureForkForSource(env, sourceRepo); err != nil {
		t.Fatalf("ensure fork for %s: %v", sourceRepo, err)
	}
}

func liveGithubEnsureForkForSource(env liveGithubTestEnv, sourceRepo string) error {
	sourceRepo = strings.TrimSpace(sourceRepo)
	if sourceRepo == "" {
		return nil
	}
	if !validRepoSlug(sourceRepo) {
		return fmt.Errorf("invalid source repo %q", sourceRepo)
	}
	parts := strings.SplitN(sourceRepo, "/", 2)
	repoName := parts[1]
	if forkRepo := strings.TrimSpace(env.Repos.Fork); validRepoSlug(forkRepo) {
		if _, err := startWorkFetchRepo(forkRepo, env.APIBaseURL, env.Token); err != nil {
			return err
		}
		return ensureStartWorkForkReady(forkRepo, env.APIBaseURL, env.Token)
	}
	if strings.TrimSpace(env.Viewer) == "" {
		return fmt.Errorf("github viewer is required to ensure fork for %s", sourceRepo)
	}
	fork, _, err := ensureGithubFork(sourceRepo, repoName, env.Viewer, env.APIBaseURL, env.Token)
	if err != nil {
		return err
	}
	return ensureStartWorkForkReady(fork.FullName, env.APIBaseURL, env.Token)
}

func installLiveGithubFakeCodex(t *testing.T, home string, marker string) string {
	t.Helper()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir fake codex bin: %v", err)
	}
	logPath := filepath.Join(home, "fake-codex.log")
	writeExecutable(t, filepath.Join(binDir, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`printf '%s\n' "$*" >> "${FAKE_LIVE_CODEX_LOG_PATH}"`,
		`repo="$PWD"`,
		`prev=""`,
		`for arg in "$@"; do`,
		`  if [ "$prev" = "-C" ]; then repo="$arg"; cd "$repo"; prev=""; continue; fi`,
		`  prev="$arg"`,
		`done`,
		`session_dir="$CODEX_HOME/sessions/2099/01/01"`,
		`mkdir -p "$session_dir"`,
		`session_id="live-smoke-$$-$(date +%s)"`,
		`printf '{"type":"session_meta","payload":{"id":"%s","timestamp":"2099-01-01T00:00:00Z","cwd":"%s"}}\n' "$session_id" "$repo" > "$session_dir/rollout-$session_id.jsonl"`,
		`payload="$(cat)"`,
		`if [ -z "$payload" ]; then payload="$*"; fi`,
		`mode="${` + liveGithubFakeCodexModeEnv + `:-` + liveGithubFakeCodexModeSuccess + `}"`,
		`case "$payload" in`,
		`  *"Return JSON only with this schema: {\"priority\":\"P1\"|\"P2\"|\"P3\"|\"P4\"|\"P5\""*) printf '{"priority":"P3","rationale":"live smoke"}\n' ;;`,
		`  *"# NANA Work-local Finding Grouping"*) printf '{"groups":[]}\n' ;;`,
		`  *"Plan any in-scope followups for this Nana work run and return JSON only."*) printf '{"decision":"no_followups","items":[]}\n' ;;`,
		`  *"Review this followup plan for scope discipline and return JSON only."*) printf '{"decision":"no_followups","approved_items":[],"rejected_items":[],"summary":"none"}\n' ;;`,
		`  *"Decide each finding as one of:"*) printf '{"group":"live-smoke","decisions":[]}\n' ;;`,
		`  *"Review role:"*|*"Review this local implementation and return JSON only."*) printf '{"findings":[]}\n' ;;`,
		`  *)`,
		`    target="${NANA_PROJECT_AGENTS_ROOT:-$repo}"`,
		`    mkdir -p "$target"`,
		`    case "$mode" in`,
		`      ` + liveGithubFakeCodexModeImplement + `)`,
		`        printf 'live-codex-implement-fail\n' >&2`,
		`        exit 7`,
		`        ;;`,
		`      ` + liveGithubFakeCodexModePublisher + `)`,
		`        mkdir -p "$target/internal/gocli"`,
		`        printf 'package gocli\n\nfunc liveGithubPublishFail(\n' > "$target/internal/gocli/zz_live_github_publish_fail.go"`,
		`        printf 'live-codex-publisher-fail\n'`,
		`        ;;`,
		`      *)`,
		`        printf '%s\n' "${FAKE_LIVE_CODEX_MARKER:-live-smoke}" > "$target/NANA_LIVE_SMOKE.md"`,
		`        printf 'live-codex-implemented\n'`,
		`        ;;`,
		`    esac`,
		`    ;;`,
		`esac`,
	}, "\n"))
	prependPathEnv(t, binDir)
	t.Setenv("FAKE_LIVE_CODEX_LOG_PATH", logPath)
	t.Setenv("FAKE_LIVE_CODEX_MARKER", marker)
	setLiveGithubFakeCodexMode(t, liveGithubFakeCodexModeSuccess)
	return logPath
}

func setLiveGithubFakeCodexMode(t *testing.T, mode string) {
	t.Helper()
	if strings.TrimSpace(mode) == "" {
		mode = liveGithubFakeCodexModeSuccess
	}
	t.Setenv(liveGithubFakeCodexModeEnv, mode)
}

func prependPathEnv(t *testing.T, dir string) {
	t.Helper()
	path := os.Getenv("PATH")
	parts := []string{dir}
	if path != "" {
		parts = append(parts, path)
	}
	t.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
}

func newLiveGithubMarker(t *testing.T) string {
	t.Helper()
	return liveGithubBranchName(sanitizePathToken(t.Name()), fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
}

func liveGithubTaggedTitle(marker string, title string) string {
	return fmt.Sprintf("%s %s %s", liveGithubTitlePrefix, strings.TrimSpace(title), strings.TrimSpace(marker))
}

func liveGithubTaggedBody(marker string, summary string) string {
	lines := []string{liveGithubBodyMarker + marker}
	if strings.TrimSpace(summary) != "" {
		lines = append(lines, "", strings.TrimSpace(summary))
	}
	return strings.Join(lines, "\n")
}

func liveGithubArtifactMatches(marker string, values ...string) bool {
	needle := strings.ToLower(strings.TrimSpace(marker))
	for _, value := range values {
		if strings.Contains(strings.ToLower(strings.TrimSpace(value)), needle) {
			return true
		}
	}
	return false
}

func liveGithubSnapshotDefaultBranch(t *testing.T, env liveGithubTestEnv, repoSlug string) liveGithubRepoSnapshot {
	t.Helper()
	defaultBranch, sha, err := liveGithubDefaultBranchSnapshotForRepo(env, repoSlug)
	if err != nil {
		t.Fatalf("snapshot default branch for %s: %v", repoSlug, err)
	}
	return liveGithubRepoSnapshot{RepoSlug: repoSlug, DefaultBranch: defaultBranch, DefaultBranchSHA: sha}
}

func liveGithubSnapshots(t *testing.T, env liveGithubTestEnv, repoSlugs ...string) []liveGithubRepoSnapshot {
	t.Helper()
	seen := map[string]bool{}
	snapshots := []liveGithubRepoSnapshot{}
	for _, repoSlug := range repoSlugs {
		repoSlug = strings.TrimSpace(repoSlug)
		if repoSlug == "" || seen[repoSlug] {
			continue
		}
		seen[repoSlug] = true
		snapshots = append(snapshots, liveGithubSnapshotDefaultBranch(t, env, repoSlug))
	}
	return snapshots
}

func liveGithubAssertSnapshotsUnchanged(t *testing.T, env liveGithubTestEnv, snapshots ...liveGithubRepoSnapshot) {
	t.Helper()
	for _, snapshot := range snapshots {
		if err := liveGithubAssertDefaultBranchUnchanged(env, snapshot); err != nil {
			t.Fatalf("default branch assertion for %s: %v", snapshot.RepoSlug, err)
		}
	}
}

func cleanupLiveGithubArtifactsByMarker(t *testing.T, env liveGithubTestEnv, marker string, repos []string, snapshots ...liveGithubRepoSnapshot) {
	t.Helper()
	seen := map[string]bool{}
	for _, repoSlug := range repos {
		repoSlug = strings.TrimSpace(repoSlug)
		if repoSlug == "" || seen[repoSlug] {
			continue
		}
		seen[repoSlug] = true
		pulls, _, err := listLiveGithubPulls(env, repoSlug)
		if err != nil {
			t.Errorf("cleanup list pulls for %s: %v", repoSlug, err)
			continue
		}
		for _, pull := range pulls {
			if !liveGithubArtifactMatches(marker, pull.Title, pull.Body, pull.Head.Ref) {
				continue
			}
			if err := liveGithubClosePullIfOpen(env, repoSlug, pull.Number); err != nil {
				t.Errorf("cleanup close PR %s#%d: %v", repoSlug, pull.Number, err)
			}
		}
		issues, err := liveGithubListOpenIssues(env, repoSlug)
		if err != nil {
			t.Errorf("cleanup list issues for %s: %v", repoSlug, err)
			continue
		}
		for _, issue := range issues {
			if issue.PullRequest != nil || !liveGithubArtifactMatches(marker, issue.Title, issue.Body) {
				continue
			}
			if err := liveGithubCloseIssueIfOpen(env, repoSlug, issue.Number); err != nil {
				t.Errorf("cleanup close issue %s#%d: %v", repoSlug, issue.Number, err)
			}
		}
		branches, err := liveGithubListBranches(env, repoSlug)
		if err != nil {
			t.Errorf("cleanup list branches for %s: %v", repoSlug, err)
			continue
		}
		for _, branch := range branches {
			if !liveGithubArtifactMatches(marker, branch.Name) {
				continue
			}
			if err := liveGithubDeleteBranchIfExists(env, repoSlug, branch.Name); err != nil {
				t.Errorf("cleanup delete branch %s:%s: %v", repoSlug, branch.Name, err)
			}
		}
	}
	for _, snapshot := range snapshots {
		if err := liveGithubAssertDefaultBranchUnchanged(env, snapshot); err != nil {
			t.Errorf("cleanup default branch assertion for %s: %v", snapshot.RepoSlug, err)
		}
	}
}

func patchLiveGithubPublisherManifest(t *testing.T, manifestPath string, marker string, mode string) githubWorkManifest {
	t.Helper()
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest %s: %v", manifestPath, err)
	}
	if err := validateLiveGithubPublisherManifestMode(manifest, mode); err != nil {
		t.Fatal(err)
	}
	manifest.PublishedPRHeadRef = marker
	applyLiveGithubForkOverrideToManifest(&manifest, optionalTestEnv)
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest %s: %v", manifestPath, err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index manifest %s: %v", manifestPath, err)
	}
	return manifest
}

func applyLiveGithubForkOverrideToManifest(manifest *githubWorkManifest, getenv func(string) string) {
	if manifest == nil || normalizeGithubPublishTarget(manifest.PublishTarget) != "fork" {
		return
	}
	forkRepo := liveGithubRepoFromEnvWith(getenv, "NANA_LIVE_REPO_FORK", "")
	if !validRepoSlug(forkRepo) {
		return
	}
	manifest.PublishRepoSlug = forkRepo
	if parts := strings.SplitN(forkRepo, "/", 2); len(parts) == 2 {
		manifest.PublishRepoOwner = parts[0]
	}
}

func validateLiveGithubPublisherManifestMode(manifest githubWorkManifest, mode string) error {
	expectedRepoMode := normalizeGithubRepoMode(mode)
	if expectedRepoMode == "" {
		return fmt.Errorf("invalid repo mode %q", mode)
	}
	expectedPublishTarget := repoModeToPublishTarget(expectedRepoMode)
	expectedCreatePR := expectedPublishTarget != "local-branch"
	if manifest.RepoMode != expectedRepoMode || manifest.PublishTarget != expectedPublishTarget || manifest.CreatePROnComplete != expectedCreatePR {
		return fmt.Errorf(
			"expected start manifest repo_mode=%q publish_target=%q create_pr_on_complete=%t, got repo_mode=%q publish_target=%q create_pr_on_complete=%t",
			expectedRepoMode,
			expectedPublishTarget,
			expectedCreatePR,
			manifest.RepoMode,
			manifest.PublishTarget,
			manifest.CreatePROnComplete,
		)
	}
	return nil
}

func repointLiveGithubSandboxOriginToRepo(t *testing.T, env liveGithubTestEnv, manifest githubWorkManifest) {
	t.Helper()
	remoteURL := fmt.Sprintf("https://%s/%s.git", liveGithubHostForAPIBase(env.APIBaseURL), manifest.RepoSlug)
	if err := githubRunGit(manifest.SandboxRepoPath, "remote", "set-url", "origin", remoteURL); err != nil {
		t.Fatalf("set origin url for repo publication: %v", err)
	}
	if err := githubRunGit(manifest.SandboxRepoPath, "remote", "set-url", "--push", "origin", remoteURL); err != nil {
		t.Fatalf("set push origin url for repo publication: %v", err)
	}
}

func assertLiveGithubCodexNotLaunched(t *testing.T, logPath string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read fake codex log: %v", err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("expected fake codex not to launch, log=%q", string(data))
	}
}

func liveGithubFakeCodexInvocationCount(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read fake codex log: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func liveGithubRunObserved(repoSlug string, issueNumber int) (startWorkIssueState, bool) {
	state, err := readStartWorkState(repoSlug)
	if err != nil || state == nil {
		return startWorkIssueState{}, false
	}
	issue, ok := state.Issues[fmt.Sprintf("%d", issueNumber)]
	if !ok {
		return startWorkIssueState{}, false
	}
	return issue, strings.TrimSpace(issue.LastRunID) != ""
}

func liveGithubReadPull(t *testing.T, env liveGithubTestEnv, repoSlug string, number int) liveGithubPullDetails {
	t.Helper()
	var pull liveGithubPullDetails
	if _, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repoSlug, number), nil, &pull); err != nil {
		t.Fatalf("read pull %s#%d: %v", repoSlug, number, err)
	}
	return pull
}

func listLiveGithubPulls(env liveGithubTestEnv, repoSlug string) ([]liveGithubPullDetails, int, error) {
	pulls := []liveGithubPullDetails{}
	status := 0
	for page := 1; page <= 20; page++ {
		var batch []liveGithubPullDetails
		nextStatus, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s/pulls?state=open&per_page=100&page=%d", repoSlug, page), nil, &batch)
		if err != nil {
			return nil, nextStatus, err
		}
		status = nextStatus
		pulls = append(pulls, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return pulls, status, nil
}

func liveGithubCountMatchingOpenPulls(t *testing.T, env liveGithubTestEnv, marker string, repoSlugs ...string) int {
	t.Helper()
	total := 0
	seen := map[string]bool{}
	for _, repoSlug := range repoSlugs {
		repoSlug = strings.TrimSpace(repoSlug)
		if repoSlug == "" || seen[repoSlug] {
			continue
		}
		seen[repoSlug] = true
		pulls, _, err := listLiveGithubPulls(env, repoSlug)
		if err != nil {
			t.Fatalf("list pulls for %s: %v", repoSlug, err)
		}
		for _, pull := range pulls {
			if liveGithubArtifactMatches(marker, pull.Title, pull.Body, pull.Head.Ref) {
				total++
			}
		}
	}
	return total
}

func liveGithubCountMatchingOpenIssues(t *testing.T, env liveGithubTestEnv, marker string, repoSlugs ...string) int {
	t.Helper()
	total := 0
	seen := map[string]bool{}
	for _, repoSlug := range repoSlugs {
		repoSlug = strings.TrimSpace(repoSlug)
		if repoSlug == "" || seen[repoSlug] {
			continue
		}
		seen[repoSlug] = true
		issues, err := liveGithubListOpenIssues(env, repoSlug)
		if err != nil {
			t.Fatalf("list issues for %s: %v", repoSlug, err)
		}
		for _, issue := range issues {
			if issue.PullRequest == nil && liveGithubArtifactMatches(marker, issue.Title, issue.Body) {
				total++
			}
		}
	}
	return total
}

func liveGithubCreateIssue(t *testing.T, env liveGithubTestEnv, repoSlug string, title string, body string, labels []string) startWorkIssuePayload {
	t.Helper()
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	var issue startWorkIssuePayload
	if _, err := liveGithubAPIRequest(env, http.MethodPost, fmt.Sprintf("/repos/%s/issues", repoSlug), payload, &issue); err != nil {
		t.Fatalf("create issue in %s: %v", repoSlug, err)
	}
	return issue
}

func liveGithubDefaultBranchSnapshotForRepo(env liveGithubTestEnv, repoSlug string) (string, string, error) {
	var repo githubRepositoryPayload
	if _, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s", repoSlug), nil, &repo); err != nil {
		return "", "", err
	}
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if defaultBranch == "" {
		return "", "", fmt.Errorf("repo %s is missing default_branch", repoSlug)
	}
	var branch liveGithubBranchDetails
	if _, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s/branches/%s", repoSlug, url.PathEscape(defaultBranch)), nil, &branch); err != nil {
		return "", "", err
	}
	return defaultBranch, strings.TrimSpace(branch.Commit.SHA), nil
}

func liveGithubAssertDefaultBranchUnchanged(env liveGithubTestEnv, snapshot liveGithubRepoSnapshot) error {
	_, sha, err := liveGithubDefaultBranchSnapshotForRepo(env, snapshot.RepoSlug)
	if err != nil {
		return err
	}
	if sha != snapshot.DefaultBranchSHA {
		return fmt.Errorf("default branch %s for %s changed: before=%s after=%s", snapshot.DefaultBranch, snapshot.RepoSlug, snapshot.DefaultBranchSHA, sha)
	}
	return nil
}

func liveGithubClosePullIfOpen(env liveGithubTestEnv, repoSlug string, number int) error {
	var pull liveGithubPullDetails
	status, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repoSlug, number), nil, &pull)
	if status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(pull.State), "closed") {
		return nil
	}
	_, err = liveGithubAPIRequest(env, http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", repoSlug, number), map[string]any{"state": "closed"}, &pull)
	return err
}

func liveGithubCloseIssueIfOpen(env liveGithubTestEnv, repoSlug string, number int) error {
	var issue startWorkIssuePayload
	status, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repoSlug, number), nil, &issue)
	if status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(issue.State), "closed") {
		return nil
	}
	_, err = liveGithubAPIRequest(env, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repoSlug, number), map[string]any{"state": "closed"}, &issue)
	return err
}

func liveGithubDeleteBranchIfExists(env liveGithubTestEnv, repoSlug string, branchName string) error {
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return nil
	}
	status, err := liveGithubAPIRequest(env, http.MethodDelete, fmt.Sprintf("/repos/%s/git/refs/heads/%s", repoSlug, url.PathEscape(branchName)), nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

func liveGithubListOpenIssues(env liveGithubTestEnv, repoSlug string) ([]startWorkIssuePayload, error) {
	issues := []startWorkIssuePayload{}
	for page := 1; page <= 20; page++ {
		var batch []startWorkIssuePayload
		if _, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s/issues?state=open&per_page=100&page=%d", repoSlug, page), nil, &batch); err != nil {
			return nil, err
		}
		issues = append(issues, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return issues, nil
}

func liveGithubListBranches(env liveGithubTestEnv, repoSlug string) ([]liveGithubBranchDetails, error) {
	branches := []liveGithubBranchDetails{}
	for page := 1; page <= 20; page++ {
		var batch []liveGithubBranchDetails
		if _, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s/branches?per_page=100&page=%d", repoSlug, page), nil, &batch); err != nil {
			return nil, err
		}
		branches = append(branches, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return branches, nil
}

func liveGithubCleanupStaleArtifacts(env liveGithubTestEnv) error {
	return liveGithubCleanupStaleArtifactsAt(env, time.Now().UTC())
}

func liveGithubCleanupStaleArtifactsAt(env liveGithubTestEnv, now time.Time) error {
	cutoff := now.Add(-liveGithubStaleMinAge)
	seen := map[string]bool{}
	for _, repoSlug := range []string{env.Repos.Local, env.Repos.ForkTarget, env.Repos.Fork, env.Repos.Repo, env.Repos.Disabled} {
		repoSlug = strings.TrimSpace(repoSlug)
		if repoSlug == "" || seen[repoSlug] {
			continue
		}
		seen[repoSlug] = true
		defaultBranch, _, err := liveGithubDefaultBranchSnapshotForRepo(env, repoSlug)
		if err != nil {
			return err
		}
		pulls, _, err := listLiveGithubPulls(env, repoSlug)
		if err != nil {
			return err
		}
		for _, pull := range pulls {
			if !liveGithubCleanupCandidatePull(pull, cutoff) {
				continue
			}
			if err := liveGithubClosePullIfOpen(env, repoSlug, pull.Number); err != nil {
				return err
			}
		}
		issues, err := liveGithubListOpenIssues(env, repoSlug)
		if err != nil {
			return err
		}
		for _, issue := range issues {
			if !liveGithubCleanupCandidateIssue(issue, cutoff) {
				continue
			}
			if err := liveGithubCloseIssueIfOpen(env, repoSlug, issue.Number); err != nil {
				return err
			}
		}
		branches, err := liveGithubListBranches(env, repoSlug)
		if err != nil {
			return err
		}
		for _, branch := range branches {
			name := strings.TrimSpace(branch.Name)
			if !strings.HasPrefix(name, liveGithubBranchPrefix) || name == defaultBranch {
				continue
			}
			stale, err := liveGithubBranchSHAOlderThan(env, repoSlug, branch.Commit.SHA, cutoff)
			if err != nil {
				return err
			}
			if !stale {
				continue
			}
			if err := liveGithubDeleteBranchIfExists(env, repoSlug, name); err != nil {
				return err
			}
		}
	}
	return nil
}

func liveGithubCleanupCandidatePull(pull liveGithubPullDetails, cutoff time.Time) bool {
	text := strings.ToLower(strings.TrimSpace(pull.Title + "\n" + pull.Body))
	if !strings.Contains(text, strings.ToLower(liveGithubTitlePrefix)) && !strings.HasPrefix(strings.TrimSpace(pull.Head.Ref), liveGithubBranchPrefix) {
		return false
	}
	return liveGithubTimestampOlderThan(cutoff, pull.UpdatedAt, pull.CreatedAt)
}

func liveGithubCleanupCandidateIssue(issue startWorkIssuePayload, cutoff time.Time) bool {
	if issue.PullRequest != nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(issue.Title + "\n" + issue.Body))
	if !strings.Contains(text, strings.ToLower(liveGithubTitlePrefix)) && !strings.Contains(text, liveGithubBodyMarker) {
		return false
	}
	return liveGithubTimestampOlderThan(cutoff, issue.UpdatedAt)
}

func liveGithubTimestampOlderThan(cutoff time.Time, values ...string) bool {
	for _, value := range values {
		if timestamp, ok := parseArtifactTimestamp(value); ok {
			return !timestamp.After(cutoff)
		}
	}
	return false
}

func liveGithubBranchSHAOlderThan(env liveGithubTestEnv, repoSlug string, sha string, cutoff time.Time) (bool, error) {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return false, nil
	}
	var commit liveGithubCommitDetails
	if _, err := liveGithubAPIRequest(env, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s", repoSlug, url.PathEscape(sha)), nil, &commit); err != nil {
		return false, err
	}
	return liveGithubTimestampOlderThan(cutoff, commit.Commit.Committer.Date, commit.Commit.Author.Date), nil
}

func liveGithubBranchName(label string, suffix string) string {
	return fmt.Sprintf("%s%s-%s", liveGithubBranchPrefix, sanitizePathToken(label), sanitizePathToken(suffix))
}

func liveGithubAPIRequest(env liveGithubTestEnv, method string, path string, payload any, target any) (int, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, strings.TrimRight(env.APIBaseURL, "/")+path, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(env.Token) != "" {
		request.Header.Set("Authorization", "Bearer "+env.Token)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := strings.TrimSpace(string(content))
		if len(detail) > 400 {
			detail = detail[:400]
		}
		if detail != "" {
			detail = ": " + detail
		}
		return response.StatusCode, fmt.Errorf("GitHub API request failed (%d %s)%s", response.StatusCode, response.Status, detail)
	}
	if target == nil || len(bytes.TrimSpace(content)) == 0 {
		return response.StatusCode, nil
	}
	return response.StatusCode, json.Unmarshal(content, target)
}
