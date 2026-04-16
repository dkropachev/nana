package gocli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAskArgs(t *testing.T) {
	parsed, err := ParseAskArgs([]string{"claude", "--agent-prompt", "executor", "review", "this"})
	if err != nil {
		t.Fatalf("ParseAskArgs(): %v", err)
	}
	if parsed.Provider != "claude" || parsed.Prompt != "review this" || parsed.AgentPromptRole != "executor" {
		t.Fatalf("unexpected parsed args: %+v", parsed)
	}
}

func TestResolveAgentPromptContent(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(cwd, ".codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	promptsDir := filepath.Join(codexHome, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(promptsDir, "executor.md"), []byte("You are Executor."), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	content, err := resolveAgentPromptContent(cwd, "executor")
	if err != nil {
		t.Fatalf("resolveAgentPromptContent(): %v", err)
	}
	if content != "You are Executor." {
		t.Fatalf("unexpected content: %q", content)
	}
}
