package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildTestNanaConfig() string {
	return strings.Join([]string{
		"# nana top-level settings (must be before any [table])",
		`notify = ["node", "/path/to/notify-hook.js"]`,
		`model_reasoning_effort = "high"`,
		`developer_instructions = "You have nana installed."`,
		"",
		"[features]",
		"multi_agent = true",
		"child_agents_md = true",
		"",
		"# ============================================================",
		"# nana (NANA) Configuration",
		"# Managed by nana setup - manual edits preserved on next setup",
		"# ============================================================",
		"",
		"[mcp_servers.nana_state]",
		`command = "node"`,
		`args = ["/path/to/state-server.js"]`,
		"",
		"[mcp_servers.nana_memory]",
		`command = "node"`,
		`args = ["/path/to/memory-server.js"]`,
		"",
		"[agents.executor]",
		`description = "Code implementation"`,
		"",
		"[tui]",
		`status_line = ["model-with-reasoning"]`,
		"",
		"# ============================================================",
		"# End nana",
		"",
	}, "\n")
}

func TestUninstallConfigCleanup(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(buildTestNanaConfig()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".nana", "codex-home-investigate"), 0o755); err != nil {
		t.Fatalf("mkdir investigate home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nana", "codex-home-investigate", "config.toml"), []byte("[agents]\n"), 0o644); err != nil {
		t.Fatalf("write investigate config: %v", err)
	}

	output, err := captureStdout(t, func() error { return Uninstall(repoRoot, cwd, nil) })
	if err != nil {
		t.Fatalf("Uninstall(): %v", err)
	}
	config, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(config)
	if strings.Contains(text, "nana (NANA) Configuration") || strings.Contains(text, "nana_state") || strings.Contains(text, "notify =") {
		t.Fatalf("unexpected cleaned config: %q", text)
	}
	if _, err := os.Stat(filepath.Join(home, ".nana", "codex-home-investigate")); !os.IsNotExist(err) {
		t.Fatalf("investigate home should be removed, got err=%v", err)
	}
	if !strings.Contains(output, "Removed NANA configuration block") {
		t.Fatalf("unexpected uninstall output: %q", output)
	}
}

func TestRemoveAgentsMdRequiresStandaloneGeneratedMarker(t *testing.T) {
	cwd := t.TempDir()
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("- Health: expect `"+generatedAgentsMarker+"`.\n"), 0o644); err != nil {
		t.Fatalf("write prose-only AGENTS.md: %v", err)
	}
	removed, err := removeAgentsMd(agentsPath, UninstallOptions{})
	if err != nil {
		t.Fatalf("removeAgentsMd(prose only): %v", err)
	}
	if removed {
		t.Fatalf("AGENTS.md with marker only in prose should not be removed")
	}
	if !fileExists(agentsPath) {
		t.Fatalf("prose-only AGENTS.md should remain")
	}

	if err := os.WriteFile(agentsPath, []byte(generatedAgentsMarker+"\n# managed\n"), 0o644); err != nil {
		t.Fatalf("write managed AGENTS.md: %v", err)
	}
	removed, err = removeAgentsMd(agentsPath, UninstallOptions{})
	if err != nil {
		t.Fatalf("removeAgentsMd(standalone marker): %v", err)
	}
	if !removed {
		t.Fatalf("AGENTS.md with standalone marker should be removed")
	}
	if fileExists(agentsPath) {
		t.Fatalf("managed AGENTS.md should be removed")
	}
}

func TestUninstallPurgeRemovesNanaDir(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	nanaDir := filepath.Join(cwd, ".nana")
	if err := os.MkdirAll(filepath.Join(nanaDir, "state"), 0o755); err != nil {
		t.Fatalf("mkdir nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nanaDir, "notepad.md"), []byte("# notes"), 0o644); err != nil {
		t.Fatalf("write notepad: %v", err)
	}

	if _, err := captureStdout(t, func() error { return Uninstall(repoRoot, cwd, []string{"--keep-config", "--purge"}) }); err != nil {
		t.Fatalf("Uninstall(purge): %v", err)
	}
	if _, err := os.Stat(nanaDir); !os.IsNotExist(err) {
		t.Fatalf(".nana directory should be removed, got err=%v", err)
	}
}
