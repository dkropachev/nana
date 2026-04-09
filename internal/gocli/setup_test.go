package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupProjectDryRunDoesNotPersistScope(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(filepath.Join(repoRoot, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "skills", "nana-setup"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "prompts", "executor.md"), []byte("# executor\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "skills", "nana-setup", "SKILL.md"), []byte("# skill\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "templates", "AGENTS.md"), []byte("template ~/.codex\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	output, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project", "--dry-run"}) })
	if err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !strings.Contains(output, "Using setup scope: project") {
		t.Fatalf("unexpected setup output: %q", output)
	}
	if fileExists(filepath.Join(cwd, ".nana", "setup-scope.json")) {
		t.Fatalf("setup-scope.json should not be written during dry-run")
	}
}

func TestSetupProjectWritesLocalAssets(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(filepath.Join(repoRoot, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "skills", "nana-setup"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "prompts", "executor.md"), []byte("# executor\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "skills", "nana-setup", "SKILL.md"), []byte("# skill\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "templates", "AGENTS.md"), []byte("template ~/.codex\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	if _, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project"}) }); err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !fileExists(filepath.Join(cwd, ".codex", "prompts", "executor.md")) {
		t.Fatalf("project prompt not installed")
	}
	if !fileExists(filepath.Join(cwd, ".codex", "skills", "nana-setup", "SKILL.md")) {
		t.Fatalf("project skill not installed")
	}
	if !fileExists(filepath.Join(cwd, ".codex", "agents", "executor.toml")) {
		t.Fatalf("project agent config not installed")
	}
	config, err := os.ReadFile(filepath.Join(cwd, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(config), "[agents]") || !strings.Contains(string(config), `USE_NANA_EXPLORE_CMD = "1"`) {
		t.Fatalf("unexpected config content: %q", string(config))
	}
	agentsMd, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsMd), "./.codex") {
		t.Fatalf("expected project AGENTS.md rewrite, got %q", string(agentsMd))
	}
}

func TestSetupProjectFallsBackToEmbeddedAssets(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := filepath.Join(cwd, "missing-repo-root")
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	if _, err := captureStdout(t, func() error { return Setup(repoRoot, cwd, []string{"--scope", "project"}) }); err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if !fileExists(filepath.Join(cwd, ".codex", "prompts", "executor.md")) {
		t.Fatalf("embedded project prompt not installed")
	}
	if !fileExists(filepath.Join(cwd, ".codex", "skills", "deep-interview", "SKILL.md")) {
		t.Fatalf("embedded project skill not installed")
	}
	agentsMd, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsMd), "./.codex") {
		t.Fatalf("expected embedded AGENTS template rewrite, got %q", string(agentsMd))
	}
}
