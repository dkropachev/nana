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

func TestCheckConfigReportsReadConflictAsManualRemediation(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Mkdir(configPath, 0o755); err != nil {
		t.Fatalf("mkdir config path: %v", err)
	}

	check := checkConfig(configPath, "project")
	if check.Status != "fail" {
		t.Fatalf("expected fail, got %#v", check)
	}
	if strings.Contains(check.Message, "not found") || !strings.Contains(check.Message, "cannot be read") {
		t.Fatalf("expected read error message, got %q", check.Message)
	}
	if check.Remediation == nil {
		t.Fatalf("expected remediation")
	}
	if !strings.HasPrefix(check.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("read conflict should not be marked safe, got %#v", check.Remediation)
	}
	if !strings.Contains(check.Remediation.ManualFallback, "path conflict") {
		t.Fatalf("expected path conflict fallback, got %#v", check.Remediation)
	}
}

func TestProjectSetupRemediationDoesNotForceOverwriteCustomAgents(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# custom project instructions\n"), 0o644); err != nil {
		t.Fatalf("write custom AGENTS.md: %v", err)
	}

	agentsCheck := checkAgentsMD("project", cwd, filepath.Join(cwd, ".codex"))
	if agentsCheck.Status != "pass" {
		t.Fatalf("expected existing AGENTS.md to pass, got %#v", agentsCheck)
	}

	configCheck := checkConfig(filepath.Join(cwd, ".codex", "config.toml"), "project")
	if configCheck.Status != "warn" || !strings.Contains(configCheck.Message, "config.toml not found") {
		t.Fatalf("expected missing config warning, got %#v", configCheck)
	}
	stateCheck := checkNanaStatePaths(cwd, "project")
	if stateCheck.Status != "warn" || !strings.Contains(stateCheck.Message, "missing .nana") {
		t.Fatalf("expected missing state warning, got %#v", stateCheck)
	}

	var out strings.Builder
	printDoctorRemediations(&out, []doctorCheck{configCheck, stateCheck})
	text := out.String()
	if strings.Contains(text, "--force") {
		t.Fatalf("safe setup remediation must not force-overwrite custom AGENTS.md:\n%s", text)
	}
	if count := strings.Count(text, "Safe automatic fix: yes — run `nana setup --scope project`"); count != 2 {
		t.Fatalf("expected safe non-force project setup for both remediations, count=%d output:\n%s", count, text)
	}
}

func TestDoctorWithCustomProjectAgentsDoesNotSuggestForceOverwrite(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	repoRoot := filepath.Join("..", "..")
	if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "setup-scope.json"), []byte(`{"scope":"project"}`), 0o644); err != nil {
		t.Fatalf("write setup scope: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# custom project instructions\n\nKeep local policy.\n"), 0o644); err != nil {
		t.Fatalf("write custom AGENTS.md: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex 0.0.0-test'; exit 0; fi\nexit 0\n")
	writeExecutable(t, filepath.Join(fakeBin, "node"), "#!/bin/sh\necho 'v20.0.0'\n")
	writeExecutable(t, filepath.Join(fakeBin, "gh"), "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'gh version 2.0.0-test'; exit 0; fi\nif [ \"$1\" = \"auth\" ]; then exit 0; fi\nexit 0\n")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	stdout, stderr, err := captureOutput(t, func() error {
		return Doctor(cwd, repoRoot)
	})
	if err != nil {
		t.Fatalf("Doctor(): %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "AGENTS freshness") {
		t.Fatalf("doctor output should include AGENTS freshness check:\n%s", stdout)
	}
	if strings.Contains(stdout, "--force") {
		t.Fatalf("doctor must not suggest force-overwriting custom AGENTS.md:\n%s", stdout)
	}
	for _, want := range []string{
		"Safe automatic fix: no",
		"back up and merge custom instructions",
		"Some warnings require manual remediation",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorFooterManualOnlyFailureDoesNotTellUserToRunSetup(t *testing.T) {
	var out strings.Builder
	checks := []doctorCheck{{
		Name:    "Config",
		Status:  "fail",
		Message: "config.toml cannot be read: is a directory",
		Remediation: manualDoctorRemediation(
			"/tmp/nana/config.toml",
			"move the path conflict aside, then rerun `nana doctor`",
		),
	}}

	printDoctorFooter(&out, "project", checks, 0, 1, true)
	text := out.String()
	if !strings.Contains(text, "Some failures require manual remediation") {
		t.Fatalf("expected manual remediation footer, got:\n%s", text)
	}
	if strings.Contains(text, "fix installation issues") || strings.Contains(text, "Run \"nana setup") {
		t.Fatalf("manual-only failure footer must not tell user to run setup:\n%s", text)
	}
}

func TestDoctorFooterUsesForceCommandForForceOnlySafeWarnings(t *testing.T) {
	var out strings.Builder
	forceCommand := setupForceFixCommand("project")
	checks := []doctorCheck{{
		Name:    "AGENTS runtime guidance",
		Status:  "warn",
		Message: "missing runtime state section; run " + forceCommand,
		Remediation: &doctorRemediation{
			Path:             filepath.Join("/tmp", "repo", "AGENTS.md"),
			SafeAutomaticFix: fmt.Sprintf("yes — run `%s`", forceCommand),
			ManualFallback:   fmt.Sprintf("inspect AGENTS.md, then run `%s`", forceCommand),
		},
	}}

	printDoctorFooter(&out, "project", checks, 1, 0, true)
	text := out.String()
	if !strings.Contains(text, fmt.Sprintf(`run "%s"`, forceCommand)) {
		t.Fatalf("force-only safe warning footer should use exact force command, got:\n%s", text)
	}
	nonForceCommand := setupFixCommand("project")
	if strings.Contains(text, fmt.Sprintf(`run "%s"`, nonForceCommand)) {
		t.Fatalf("force-only safe warning footer must not suggest non-force setup, got:\n%s", text)
	}
}

func TestDoctorFooterMixedSafeCommandsDefersToRemediationCommands(t *testing.T) {
	var out strings.Builder
	forceCommand := setupForceFixCommand("project")
	checks := []doctorCheck{
		{
			Name:    "AGENTS freshness",
			Status:  "warn",
			Message: "stale generated AGENTS.md",
			Remediation: &doctorRemediation{
				Path:             filepath.Join("/tmp", "repo", "AGENTS.md"),
				SafeAutomaticFix: fmt.Sprintf("yes — run `%s`", forceCommand),
				ManualFallback:   fmt.Sprintf("inspect AGENTS.md, then run `%s`", forceCommand),
			},
		},
		{
			Name:        "Config",
			Status:      "warn",
			Message:     "config.toml not found",
			Remediation: setupDoctorRemediation("project", filepath.Join("/tmp", "repo", ".codex", "config.toml"), ""),
		},
	}

	printDoctorFooter(&out, "project", checks, 2, 0, true)
	text := out.String()
	if !strings.Contains(text, "safe automatic fix commands shown") {
		t.Fatalf("mixed safe commands should defer to per-check remediation commands, got:\n%s", text)
	}
	for _, command := range []string{forceCommand, setupFixCommand("project")} {
		if strings.Contains(text, fmt.Sprintf(`run "%s"`, command)) {
			t.Fatalf("mixed safe command footer should not single out %q, got:\n%s", command, text)
		}
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
	if check.Remediation == nil || !strings.HasPrefix(check.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("broken existing AGENTS.md should require manual remediation, got %#v", check.Remediation)
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
	if strings.Contains(check.Message, "run nana setup --force") {
		t.Fatalf("warning should not present force-overwrite as safe guidance: %q", check.Message)
	}
	if check.Remediation == nil {
		t.Fatalf("expected remediation")
	}
	if !strings.HasPrefix(check.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("custom AGENTS.md should not be marked safe to overwrite, got %#v", check.Remediation)
	}
	if !strings.Contains(check.Remediation.ManualFallback, "back up and merge custom instructions") {
		t.Fatalf("expected custom-content fallback, got %#v", check.Remediation)
	}
}

func TestCheckAgentsRuntimeSectionsSafeRefreshesSetupGeneratedWarnings(t *testing.T) {
	cwd := t.TempDir()
	content := strings.Join([]string{
		"<!-- nana:generated:agents-md -->",
		"# stale setup-generated project instructions",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	check := checkAgentsRuntimeSections("project", cwd, filepath.Join(cwd, ".codex"))
	if check.Status != "warn" || !strings.Contains(check.Message, "missing runtime state section") {
		t.Fatalf("unexpected check: %#v", check)
	}
	if strings.Contains(check.Message, "manual merge required") {
		t.Fatalf("setup-generated AGENTS.md should not be reported as manual-only: %q", check.Message)
	}
	if check.Remediation == nil {
		t.Fatalf("expected remediation")
	}
	if !strings.Contains(check.Remediation.SafeAutomaticFix, "nana setup --force --scope project") {
		t.Fatalf("setup-generated AGENTS.md should use force setup remediation, got %#v", check.Remediation)
	}
	if !strings.Contains(check.Remediation.ManualFallback, "refresh setup-generated AGENTS.md content") {
		t.Fatalf("expected setup-generated fallback, got %#v", check.Remediation)
	}
}

func TestDoctorRemediationOutputIncludesActionableFailureDetails(t *testing.T) {
	var out strings.Builder
	printDoctorRemediations(&out, []doctorCheck{{
		Name:    "Config",
		Status:  "fail",
		Message: "invalid config.toml (possible duplicate TOML table such as [tui])",
		Remediation: manualDoctorRemediation(
			"/tmp/nana/config.toml",
			"edit /tmp/nana/config.toml, then run `nana setup --force --scope project`",
		),
	}})

	text := out.String()
	for _, want := range []string{
		"Remediation:",
		"Config (fail)",
		"Path: /tmp/nana/config.toml",
		"Cause: invalid config.toml",
		"Safe automatic fix: no",
		"Manual fallback: edit /tmp/nana/config.toml",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("remediation output missing %q in:\n%s", want, text)
		}
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

	check := checkNanaStatePaths(cwd, "project")
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

	check := checkNanaStatePaths(cwd, "project")
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
	if check.Remediation == nil {
		t.Fatalf("expected remediation")
	}
	if check.Remediation.Path != registry.Accounts[0].AuthPath {
		t.Fatalf("expected credential path remediation, got %#v", check.Remediation)
	}
	if !strings.HasPrefix(check.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("credential repair should be manual, got %#v", check.Remediation)
	}
	if !strings.Contains(check.Remediation.ManualFallback, "nana account add primary") {
		t.Fatalf("expected account re-add fallback, got %#v", check.Remediation)
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
