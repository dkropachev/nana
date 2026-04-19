package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckMcpServersPassesForSetupGeneratedConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	content := strings.Join([]string{
		"[agents]",
		"max_threads = 6",
		"max_depth = 2",
		"",
		"[env]",
		`USE_NANA_EXPLORE_CMD = "1"`,
		"",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	check := checkMcpServers(configPath)
	if check.Status != "pass" {
		t.Fatalf("expected pass, got %#v", check)
	}
	if !strings.Contains(check.Message, "current setup") {
		t.Fatalf("unexpected message: %q", check.Message)
	}
}

func TestCheckMcpServersPassesWhenNanaServersConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	content := strings.Join([]string{
		"[mcp_servers.nana_state]",
		`command = "node"`,
		`args = ["/path/to/state-server.js"]`,
		"",
		"[mcp_servers.nana_memory]",
		`command = "node"`,
		`args = ["/path/to/memory-server.js"]`,
		"",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	check := checkMcpServers(configPath)
	if check.Status != "pass" {
		t.Fatalf("expected pass, got %#v", check)
	}
	if !strings.Contains(check.Message, "NANA present") {
		t.Fatalf("unexpected message: %q", check.Message)
	}
}

func TestCheckMcpServersPassesWhenOnlyNonNanaServersConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	content := strings.Join([]string{
		"[mcp_servers.playwright]",
		`command = "npx"`,
		`args = ["@playwright/mcp@latest"]`,
		"",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	check := checkMcpServers(configPath)
	if check.Status != "pass" {
		t.Fatalf("expected pass, got %#v", check)
	}
	if !strings.Contains(check.Message, "1 servers configured") {
		t.Fatalf("unexpected message: %q", check.Message)
	}
}

func TestCheckManagedAccountsPassesWhenNotConfigured(t *testing.T) {
	check := checkManagedAccounts(t.TempDir())
	if check.Status != "pass" || !strings.Contains(check.Message, "not configured") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckManagedAccountsFailsWhenCredentialMissing(t *testing.T) {
	codexHome := t.TempDir()
	registry := ManagedAuthRegistry{
		Version:   authRegistryVersion,
		Preferred: "primary",
		Accounts: []ManagedAuthAccount{
			{Name: "primary", AuthPath: filepath.Join(codexHome, "auth-accounts", "primary.json"), Enabled: true},
		},
	}
	if err := saveManagedAuthRegistry(codexHome, registry); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	check := checkManagedAccounts(codexHome)
	if check.Status != "fail" || !strings.Contains(check.Message, "credential file missing") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckGithubAutomationReposWarnsWhenEligibleRepoPreflightFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoSlug := "acme/widget"
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:       6,
		RepoMode:      "repo",
		IssuePickMode: "auto",
		PRForwardMode: "auto",
		PublishTarget: "repo",
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	oldPreflight := githubAutomationRepoPreflight
	githubAutomationRepoPreflight = func(repo string, repairOrigin bool) error {
		if repo != repoSlug {
			t.Fatalf("unexpected repo slug: %s", repo)
		}
		if repairOrigin {
			t.Fatalf("doctor should not repair origin during checks")
		}
		return fmt.Errorf("managed source checkout missing")
	}
	defer func() {
		githubAutomationRepoPreflight = oldPreflight
	}()

	check := checkGithubAutomationRepos()
	if check.Status != "warn" {
		t.Fatalf("expected warn, got %#v", check)
	}
	if !strings.Contains(check.Message, repoSlug) || !strings.Contains(check.Message, "managed source checkout missing") {
		t.Fatalf("unexpected message: %q", check.Message)
	}
}

func TestCheckRepoGitDriftWarnsWhenBehindClean(t *testing.T) {
	repo, remote := createDoctorTrackedRepo(t)
	advanceDoctorRemote(t, remote)

	check := checkRepoGitDrift(repo, repo)
	if check.Status != "warn" || !strings.Contains(check.Message, "behind-clean") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckRepoGitDriftWarnsWhenBehindDirty(t *testing.T) {
	repo, remote := createDoctorTrackedRepo(t)
	advanceDoctorRemote(t, remote)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# local work\ndirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	check := checkRepoGitDrift(repo, repo)
	if check.Status != "warn" || !strings.Contains(check.Message, "behind-dirty") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func createDoctorTrackedRepo(t *testing.T) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runLocalWorkTestGit(t, "", "init", "--bare", remote)
	repo := createLocalWorkRepo(t)
	runLocalWorkTestGit(t, repo, "remote", "add", "origin", remote)
	runLocalWorkTestGit(t, repo, "push", "-u", "origin", "HEAD:main")
	return repo, remote
}

func advanceDoctorRemote(t *testing.T, remote string) {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "clone")
	runLocalWorkTestGit(t, "", "clone", remote, clone)
	runLocalWorkTestGit(t, clone, "checkout", "-B", "main", "origin/main")
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("# remote advance\n"), 0o644); err != nil {
		t.Fatalf("write clone README: %v", err)
	}
	runLocalWorkTestGit(t, clone, "add", "README.md")
	runLocalWorkTestGit(t, clone, "commit", "-m", "advance remote")
	runLocalWorkTestGit(t, clone, "push", "origin", "HEAD:main")
}
