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

func TestCheckWorkSQLiteStateWarnsWhenRepairIsRequired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "doctor-work-db",
		Subject:    "Doctor DB state",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE work_items SET pause_reason = NULL, pause_until = NULL, metadata_json = '{"pause_reason":"rate limited","pause_until":"2026-04-21T00:00:00Z"}' WHERE id = ?`, item.ID); err != nil {
		t.Fatalf("seed legacy metadata: %v", err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("downgrade schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	check := checkWorkSQLiteState()
	if check.Status != "warn" || !strings.Contains(check.Message, "run `nana work db-repair`") {
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

func TestCheckNanaStateSchemasPassesKnownArtifacts(t *testing.T) {
	cwd := t.TempDir()
	for _, dir := range []string{
		filepath.Join(cwd, ".nana", "logs"),
		filepath.Join(cwd, ".nana", "plans"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "project-memory.json"), []byte(`{"version":1,"updated_at":"2026-04-20T00:00:00Z","decisions":[]}`+"\n"), 0o644); err != nil {
		t.Fatalf("write project memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, VerifyProfileFile), []byte(`{"version":1,"name":"demo","stages":[{"name":"test","command":"go test ./..."}]}`+"\n"), 0o644); err != nil {
		t.Fatalf("write verify profile: %v", err)
	}
	telemetry := strings.Join([]string{
		`{"timestamp":"2026-04-20T00:00:00Z","tool":"nana-sparkshell","event":"shell_output_compaction","command_name":"go","argument_count":2,"exit_code":0,"stdout_bytes":8,"stderr_bytes":0,"captured_bytes":8,"stdout_lines":1,"stderr_lines":0,"summary_bytes":12,"summary_lines":1,"summarized":true}`,
		`{"timestamp":"2026-04-20T00:00:01Z","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md","loader":"nana_skill_runtime_cache","schema":"skill_doc_load.v1"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson"), []byte(telemetry), 0o644); err != nil {
		t.Fatalf("write telemetry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "notepad.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatalf("write notepad: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "plans", "prd-demo.md"), []byte("# PRD: Demo\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	check := checkNanaStateSchemas(cwd)
	if check.Status != "pass" || !strings.Contains(check.Message, "5 schema-backed state artifact(s) valid") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckNanaStateSchemasFailsInvalidProjectMemoryShape(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "project-memory.json"), []byte(`{"updated_at":42,"decisions":"later"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write project memory: %v", err)
	}

	check := checkNanaStateSchemas(cwd)
	if check.Status != "fail" || !strings.Contains(check.Message, "updated_at must be a string") || !strings.Contains(check.Message, "decisions must be an array") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckNanaStateSchemasFailsInvalidVerifyProfile(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, VerifyProfileFile), []byte(`{"version":1,"name":"empty","stages":[]}`+"\n"), 0o644); err != nil {
		t.Fatalf("write verify profile: %v", err)
	}

	check := checkNanaStateSchemas(cwd)
	if check.Status != "fail" || !strings.Contains(check.Message, VerifyProfileFile) || !strings.Contains(check.Message, "at least one stage is required") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckNanaStateSchemasFailsExplicitZeroVerifyProfileVersion(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, VerifyProfileFile), []byte(`{"version":0,"stages":[{"name":"noop","command":"true"}]}`+"\n"), 0o644); err != nil {
		t.Fatalf("write verify profile: %v", err)
	}

	check := checkNanaStateSchemas(cwd)
	if check.Status != "fail" || !strings.Contains(check.Message, VerifyProfileFile) || !strings.Contains(check.Message, "version must be >= 1") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestCheckNanaStateSchemasFailsTelemetryRawOutputLeak(t *testing.T) {
	cwd := t.TempDir()
	logsDir := filepath.Join(cwd, ".nana", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	telemetry := `{"timestamp":"2026-04-20T00:00:00Z","tool":"nana-sparkshell","event":"shell_output_compaction","command":"go test ./...","argument_count":2,"exit_code":0,"stdout_bytes":8,"stderr_bytes":0,"captured_bytes":8,"stdout_lines":1,"stderr_lines":0,"summarized":true}` + "\n"
	if err := os.WriteFile(filepath.Join(logsDir, "context-telemetry.ndjson"), []byte(telemetry), 0o644); err != nil {
		t.Fatalf("write telemetry: %v", err)
	}

	check := checkNanaStateSchemas(cwd)
	if check.Status != "fail" || !strings.Contains(check.Message, "must not persist raw command") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestValidateContextTelemetryEventRejectsRawArgumentFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
	}{
		{name: "arguments", field: `"arguments":["test","./...","SECRET_TOKEN"]`},
		{name: "raw_args", field: `"raw_args":"test ./... SECRET_TOKEN"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := fmt.Sprintf(`{"timestamp":"2026-04-20T00:00:00Z","tool":"codex","event":"skill_doc_load",%s}`, tc.field)
			err := validateContextTelemetryEvent([]byte(event))
			if err == nil || !strings.Contains(err.Error(), "must not persist raw arguments") {
				t.Fatalf("validateContextTelemetryEvent() error = %v, want raw arguments rejection", err)
			}
		})
	}
}

func TestValidateContextTelemetryEventAcceptsSkillTelemetryWithoutTool(t *testing.T) {
	event := `{"timestamp":"2026-04-20T00:00:00Z","event":"skill_doc_load","skill":"plan","path":"/home/alice/.codex/skills/plan/SKILL.md","doc_label":"runtime","cache":"miss","loader":"nana_skill_runtime_cache","schema":"skill_doc_load.v1"}`
	if err := validateContextTelemetryEvent([]byte(event)); err != nil {
		t.Fatalf("validateContextTelemetryEvent() rejected existing skill telemetry without tool: %v", err)
	}
}

func TestCheckNanaStateSchemasFailsTelemetryInvalidOptionalFieldOnNonShellEvent(t *testing.T) {
	cwd := t.TempDir()
	logsDir := filepath.Join(cwd, ".nana", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	telemetry := `{"timestamp":"2026-04-20T00:00:00Z","tool":"codex","event":"skill_doc_load","skill":42,"argument_count":-1}` + "\n"
	if err := os.WriteFile(filepath.Join(logsDir, "context-telemetry.ndjson"), []byte(telemetry), 0o644); err != nil {
		t.Fatalf("write telemetry: %v", err)
	}

	check := checkNanaStateSchemas(cwd)
	if check.Status != "fail" || !strings.Contains(check.Message, "skill must be a string") {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestValidateContextTelemetrySchemaBoundsLineScan(t *testing.T) {
	cwd := t.TempDir()
	logsDir := filepath.Join(cwd, ".nana", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	var telemetry strings.Builder
	for range maxContextTelemetrySchemaLines {
		telemetry.WriteString(`{"timestamp":"2026-04-20T00:00:00Z","tool":"codex","event":"skill_doc_load","skill":"plan"}` + "\n")
	}
	telemetry.WriteString(`{"timestamp":"2026-04-20T00:00:01Z","tool":"codex","event":"skill_doc_load","command":"go test ./..."}` + "\n")
	if err := os.WriteFile(filepath.Join(logsDir, "context-telemetry.ndjson"), []byte(telemetry.String()), 0o644); err != nil {
		t.Fatalf("write telemetry: %v", err)
	}

	result := validateContextTelemetrySchema(cwd)
	if len(result.issues) != 0 {
		t.Fatalf("expected bounded scan to ignore lines after %d, got issues: %#v", maxContextTelemetrySchemaLines, result.issues)
	}
	if len(result.notes) != 1 || !strings.Contains(result.notes[0], "checked first") {
		t.Fatalf("expected bounded scan note, got %#v", result.notes)
	}
}

func TestValidateContextTelemetrySchemaBoundsIssueCollection(t *testing.T) {
	cwd := t.TempDir()
	logsDir := filepath.Join(cwd, ".nana", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	var telemetry strings.Builder
	for range maxContextTelemetrySchemaIssues + 5 {
		telemetry.WriteString(`{"timestamp":"2026-04-20T00:00:00Z","tool":"codex","event":"skill_doc_load","skill":42}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(logsDir, "context-telemetry.ndjson"), []byte(telemetry.String()), 0o644); err != nil {
		t.Fatalf("write telemetry: %v", err)
	}

	result := validateContextTelemetrySchema(cwd)
	if len(result.issues) != maxContextTelemetrySchemaIssues+1 {
		t.Fatalf("expected %d bounded issues plus sentinel, got %#v", maxContextTelemetrySchemaIssues, result.issues)
	}
	last := result.issues[len(result.issues)-1]
	if !strings.Contains(last, "stopped after") {
		t.Fatalf("expected bounded issue sentinel, got %#v", result.issues)
	}
}

func TestValidateContextTelemetryEventRejectsKnownOptionalFieldViolations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		field   string
		wantErr string
	}{
		{name: "string field type", field: `"skill":42`, wantErr: "skill must be a string"},
		{name: "boolean field type", field: `"summarized":"yes"`, wantErr: "summarized must be a boolean"},
		{name: "non exit integer minimum", field: `"argument_count":-1`, wantErr: "argument_count must be >= 0"},
		{name: "exit code minimum", field: `"exit_code":-2`, wantErr: "exit_code must be >= -1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := fmt.Sprintf(`{"timestamp":"2026-04-20T00:00:00Z","tool":"codex","event":"skill_doc_load",%s}`, tc.field)
			err := validateContextTelemetryEvent([]byte(event))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateContextTelemetryEvent() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
