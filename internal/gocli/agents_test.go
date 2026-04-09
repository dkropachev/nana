package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentsList(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	projectAgentsDir := filepath.Join(cwd, ".codex", "agents")
	userAgentsDir := filepath.Join(home, ".codex", "agents")
	if err := os.MkdirAll(projectAgentsDir, 0o755); err != nil {
		t.Fatalf("mkdir project agents: %v", err)
	}
	if err := os.MkdirAll(userAgentsDir, 0o755); err != nil {
		t.Fatalf("mkdir user agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectAgentsDir, "planner.toml"), []byte("name = \"planner\"\ndescription = \"Project planner\"\nmodel = \"gpt-5.4\"\ndeveloper_instructions = \"\"\"plan\"\"\"\n"), 0o644); err != nil {
		t.Fatalf("write project agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userAgentsDir, "reviewer.toml"), []byte("name = \"reviewer\"\ndescription = \"User reviewer\"\ndeveloper_instructions = \"\"\"review\"\"\"\n"), 0o644); err != nil {
		t.Fatalf("write user agent: %v", err)
	}

	output, err := captureStdout(t, func() error { return Agents(cwd, []string{"list"}) })
	if err != nil {
		t.Fatalf("Agents(list): %v", err)
	}
	if !strings.Contains(output, "project  planner") || !strings.Contains(output, "user     reviewer") {
		t.Fatalf("unexpected list output: %q", output)
	}
}

func TestAgentsAdd(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	if _, err := captureStdout(t, func() error { return Agents(cwd, []string{"add", "my-helper", "--scope", "project"}) }); err != nil {
		t.Fatalf("Agents(add): %v", err)
	}

	content, err := os.ReadFile(filepath.Join(cwd, ".codex", "agents", "my-helper.toml"))
	if err != nil {
		t.Fatalf("read added agent: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, `name = "my-helper"`) || !strings.Contains(text, `# model = "gpt-5.4"`) {
		t.Fatalf("unexpected added agent content: %q", text)
	}
}
