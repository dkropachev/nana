package gocli

import (
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

func TestDoctorSummaryPrintsDedupedTargetedNextSteps(t *testing.T) {
	checks := []doctorCheck{
		{Name: "Config", Status: "warn", Message: "config.toml not found", Remediation: "Run: nana setup --scope user --force"},
		{Name: "Prompts", Status: "warn", Message: "prompts directory not found", Remediation: "Run: nana setup --scope user --force"},
		{Name: "Investigate config", Status: "warn", Message: "config.toml not found", Remediation: "Run: nana investigate onboard"},
		{Name: "Node.js", Status: "warn", Message: "v18.0.0"},
	}

	var output strings.Builder
	passCount, warnCount, failCount := printDoctorChecks(&output, checks)
	printDoctorSummary(&output, checks, passCount, warnCount, failCount)
	text := output.String()

	if strings.Count(text, "Run: nana setup --scope user --force") != 1 {
		t.Fatalf("expected setup remediation once, got output:\n%s", text)
	}
	if !strings.Contains(text, "Run: nana setup --scope user --force (Config, Prompts)") {
		t.Fatalf("expected grouped setup checks, got output:\n%s", text)
	}
	if !strings.Contains(text, "Run: nana investigate onboard (Investigate config)") {
		t.Fatalf("expected investigate remediation, got output:\n%s", text)
	}
	if strings.Contains(text, `Run "nana setup --force" to refresh all components.`) {
		t.Fatalf("expected targeted next steps to replace generic setup summary, got output:\n%s", text)
	}
}

func TestDoctorPrioritizedChecksExposeRemediation(t *testing.T) {
	t.Setenv("NANA_EXPLORE_BIN", filepath.Join(t.TempDir(), "missing-harness"))
	explore := checkExploreHarness(t.TempDir())
	if explore.Status != "warn" || !strings.Contains(explore.Remediation, "NANA_EXPLORE_BIN") {
		t.Fatalf("expected explore harness remediation, got %#v", explore)
	}

	investigateCwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(investigateCwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir investigate .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(investigateCwd, ".nana", "setup-scope.json"), []byte(`{"scope":"project"}`), 0o644); err != nil {
		t.Fatalf("write investigate setup-scope: %v", err)
	}

	investigateConfig := checkInvestigateConfig(investigateCwd)
	if investigateConfig.Status != "warn" || investigateConfig.Remediation != "Run: nana investigate onboard" {
		t.Fatalf("expected investigate onboard remediation, got %#v", investigateConfig)
	}

	investigateStatus := checkInvestigateMCPStatus(investigateCwd)
	if investigateStatus.Status != "warn" || investigateStatus.Remediation != "Run: nana investigate doctor" {
		t.Fatalf("expected investigate doctor remediation, got %#v", investigateStatus)
	}
}

func TestCheckAgentsMDWarnsWithRemediationWhenMissingOrStale(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(cwd, ".codex-home")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}

	missingUser := checkAgentsMD("user", cwd, codexHome)
	if missingUser.Status != "warn" || !strings.Contains(missingUser.Remediation, "nana setup --scope user --force") {
		t.Fatalf("expected missing user AGENTS remediation, got %#v", missingUser)
	}

	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("# Old instructions\n"), 0o644); err != nil {
		t.Fatalf("write stale user AGENTS: %v", err)
	}
	staleUser := checkAgentsMD("user", cwd, codexHome)
	if staleUser.Status != "warn" || !strings.Contains(staleUser.Message, "missing current NANA guidance markers") || !strings.Contains(staleUser.Remediation, "nana setup --scope user --force") {
		t.Fatalf("expected stale user AGENTS remediation, got %#v", staleUser)
	}

	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("# nana - Intelligent Multi-Agent Orchestration\n"), 0o644); err != nil {
		t.Fatalf("write title-only user AGENTS: %v", err)
	}
	titleOnlyUser := checkAgentsMD("user", cwd, codexHome)
	if titleOnlyUser.Status != "warn" || !strings.Contains(titleOnlyUser.Message, "missing current NANA guidance markers") {
		t.Fatalf("expected title-only user AGENTS to warn, got %#v", titleOnlyUser)
	}

	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("<!-- nana:generated:agents-md -->\n# nana - Intelligent Multi-Agent Orchestration\n"), 0o644); err != nil {
		t.Fatalf("write current user AGENTS: %v", err)
	}
	currentUser := checkAgentsMD("user", cwd, codexHome)
	if currentUser.Status != "pass" {
		t.Fatalf("expected current user AGENTS to pass, got %#v", currentUser)
	}

	staleProjectPath := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(staleProjectPath, []byte("# Project notes\n"), 0o644); err != nil {
		t.Fatalf("write stale project AGENTS: %v", err)
	}
	staleProject := checkAgentsMD("project", cwd, codexHome)
	if staleProject.Status != "warn" || !strings.Contains(staleProject.Remediation, "nana agents-init . --force") {
		t.Fatalf("expected stale project AGENTS remediation, got %#v", staleProject)
	}
}

func TestAgentsMDLooksCurrentRequiresMarkerOrCurrentStructure(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "generated-marker",
			content: "<!-- nana:generated:agents-md -->\n# partial\n",
			want:    true,
		},
		{
			name:    "agents-init-marker",
			content: managedMarker + "\n# scoped instructions\n",
			want:    true,
		},
		{
			name:    "title-only",
			content: "# nana - Intelligent Multi-Agent Orchestration\n",
			want:    false,
		},
		{
			name: "current-structure",
			content: "# nana - Intelligent Multi-Agent Orchestration\n\n" +
				"<operating_principles>\n" +
				"<!-- NANA:GUIDANCE:OPERATING:START -->\n" +
				"<!-- NANA:GUIDANCE:OPERATING:END -->\n" +
				"</operating_principles>\n",
			want: true,
		},
		{
			name: "missing-guidance-markers",
			content: "# nana - Intelligent Multi-Agent Orchestration\n\n" +
				"<operating_principles>\n</operating_principles>\n",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".md")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write AGENTS fixture: %v", err)
			}
			if got := agentsMDLooksCurrent(path); got != tc.want {
				t.Fatalf("agentsMDLooksCurrent() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLegacySkillRootOverlapExposesArchiveRemediation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	for _, dir := range []string{
		filepath.Join(codexHome, "skills", "trace"),
		filepath.Join(home, ".agents", "skills", "trace"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir skill dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# trace\n"), 0o644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}

	check := checkLegacySkillRootOverlap()
	if check.Status != "warn" || !strings.Contains(check.Remediation, "archive") {
		t.Fatalf("expected legacy skill archive remediation, got %#v", check)
	}
}
