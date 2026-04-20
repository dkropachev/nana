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

func TestCheckAgentsRuntimeSectionsPassesForGeneratedAgents(t *testing.T) {
	cwd := t.TempDir()
	content := strings.Join([]string{
		"<!-- nana:generated:agents-md -->",
		"<!-- NANA:GUIDANCE:OPERATING:START -->",
		"guidance",
		"<!-- NANA:GUIDANCE:OPERATING:END -->",
		"<!-- NANA:GUIDANCE:VERIFYSEQ:START -->",
		"verify",
		"<!-- NANA:GUIDANCE:VERIFYSEQ:END -->",
		"## Runtime State and Setup",
		"- NANA state lives under `.nana/`: `.nana/state/`, `.nana/notepad.md`, `.nana/project-memory.json`, `.nana/plans/`, and `.nana/logs/`.",
		"- Keep the runtime overlay markers stable:",
		"- `<!-- NANA:RUNTIME:START --> ... <!-- NANA:RUNTIME:END -->`",
		"- `<!-- NANA:TEAM:WORKER:START --> ... <!-- NANA:TEAM:WORKER:END -->`",
		"<!-- NANA:MODELS:START -->",
		"<!-- NANA:MODELS:END -->",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	check := checkAgentsRuntimeSections("project", cwd, filepath.Join(cwd, ".codex"))
	if check.Status != "pass" {
		t.Fatalf("expected pass, got %#v", check)
	}
}

func TestCheckAgentsRuntimeSectionsRequiresStandaloneGeneratedMarker(t *testing.T) {
	cwd := t.TempDir()
	content := strings.Join([]string{
		"<!-- NANA:GUIDANCE:OPERATING:START -->",
		"guidance",
		"<!-- NANA:GUIDANCE:OPERATING:END -->",
		"<!-- NANA:GUIDANCE:VERIFYSEQ:START -->",
		"verify",
		"<!-- NANA:GUIDANCE:VERIFYSEQ:END -->",
		"## Runtime State and Setup",
		"- NANA state: `.nana/state/`, `.nana/notepad.md`, `.nana/project-memory.json`, `.nana/plans/`, `.nana/logs/`.",
		"- Health: expect `" + generatedAgentsMarker + "` plus runtime/model marker pairs above.",
		"- Keep the runtime overlay markers stable:",
		"- `<!-- NANA:RUNTIME:START --> ... <!-- NANA:RUNTIME:END -->`",
		"- `<!-- NANA:TEAM:WORKER:START --> ... <!-- NANA:TEAM:WORKER:END -->`",
		"<!-- NANA:MODELS:START -->",
		"<!-- NANA:MODELS:END -->",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	check := checkAgentsRuntimeSections("project", cwd, filepath.Join(cwd, ".codex"))
	if check.Status != "warn" || !strings.Contains(check.Message, "missing generated AGENTS marker") {
		t.Fatalf("expected missing generated marker warning, got %#v", check)
	}
}

func TestCheckAgentsRuntimeSectionsFailsBrokenOverlayMarker(t *testing.T) {
	cwd := t.TempDir()
	content := strings.Join([]string{
		"<!-- nana:generated:agents-md -->",
		"<!-- NANA:GUIDANCE:OPERATING:START -->",
		"<!-- NANA:GUIDANCE:OPERATING:END -->",
		"<!-- NANA:GUIDANCE:VERIFYSEQ:START -->",
		"<!-- NANA:GUIDANCE:VERIFYSEQ:END -->",
		"## Runtime State and Setup",
		"- NANA state lives under `.nana/`: `.nana/state/`, `.nana/notepad.md`, `.nana/project-memory.json`, `.nana/plans/`, and `.nana/logs/`.",
		"<!-- NANA:RUNTIME:START -->",
		"<!-- NANA:TEAM:WORKER:START --> ... <!-- NANA:TEAM:WORKER:END -->",
		"<!-- NANA:MODELS:START -->",
		"<!-- NANA:MODELS:END -->",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	check := checkAgentsRuntimeSections("project", cwd, filepath.Join(cwd, ".codex"))
	if check.Status != "fail" || !strings.Contains(check.Message, "NANA runtime marker count mismatch") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckAgentsRuntimeSectionsWarnsMissingGeneratedSections(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# plain project instructions\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	check := checkAgentsRuntimeSections("project", cwd, filepath.Join(cwd, ".codex"))
	if check.Status != "warn" || !strings.Contains(check.Message, "missing generated AGENTS marker") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckNanaStatePathsPassesWhenRequiredPathsExist(t *testing.T) {
	cwd := t.TempDir()
	for _, dir := range []string{
		filepath.Join(cwd, ".nana", "state"),
		filepath.Join(cwd, ".nana", "plans"),
		filepath.Join(cwd, ".nana", "logs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "project-memory.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write project memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "notepad.md"), []byte("# notes\n"), 0o644); err != nil {
		t.Fatalf("write notepad: %v", err)
	}

	check := checkNanaStatePaths(cwd)
	if check.Status != "pass" {
		t.Fatalf("expected pass, got %#v", check)
	}
}

func TestCheckNanaStatePathsWarnsMissingProjectMemory(t *testing.T) {
	cwd := t.TempDir()
	for _, dir := range []string{
		filepath.Join(cwd, ".nana", "state"),
		filepath.Join(cwd, ".nana", "plans"),
		filepath.Join(cwd, ".nana", "logs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "notepad.md"), []byte("# notes\n"), 0o644); err != nil {
		t.Fatalf("write notepad: %v", err)
	}

	check := checkNanaStatePaths(cwd)
	if check.Status != "warn" || !strings.Contains(check.Message, ".nana/project-memory.json") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckNanaJSONStateFilesFailsMalformedProjectMemory(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".nana", "state"), 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "project-memory.json"), []byte("{bad json"), 0o644); err != nil {
		t.Fatalf("write project memory: %v", err)
	}

	check := checkNanaJSONStateFiles(cwd)
	if check.Status != "fail" || !strings.Contains(check.Message, ".nana/project-memory.json") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckNanaJSONStateFilesPassesValidNestedState(t *testing.T) {
	cwd := t.TempDir()
	workerDir := filepath.Join(cwd, ".nana", "state", "team", "alpha", "workers", "one")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatalf("mkdir worker state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "project-memory.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write project memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "status.json"), []byte(`{"state":"working"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write status: %v", err)
	}

	check := checkNanaJSONStateFiles(cwd)
	if check.Status != "pass" || !strings.Contains(check.Message, "2 JSON state file(s) valid") {
		t.Fatalf("unexpected check: %#v", check)
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
