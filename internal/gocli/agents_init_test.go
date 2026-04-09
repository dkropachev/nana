package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentsInitCreatesManagedFiles(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	if err := os.MkdirAll(filepath.Join(cwd, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "templates", "AGENTS.md"), []byte("root ~/.codex template\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "src", "index.ts"), []byte("export const x = 1;\n"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "docs", "guide.md"), []byte("# guide\n"), 0o644); err != nil {
		t.Fatalf("write docs file: %v", err)
	}

	output, err := captureStdout(t, func() error { return AgentsInit(repoRoot, cwd, nil) })
	if err != nil {
		t.Fatalf("AgentsInit(): %v", err)
	}
	rootContent, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS: %v", err)
	}
	if !strings.Contains(string(rootContent), managedMarker) || !strings.Contains(string(rootContent), "./.codex") {
		t.Fatalf("unexpected root AGENTS content: %q", string(rootContent))
	}
	srcContent, err := os.ReadFile(filepath.Join(cwd, "src", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read src AGENTS: %v", err)
	}
	if !strings.Contains(string(srcContent), "`index.ts`") || !strings.Contains(string(srcContent), "<!-- Parent: ../AGENTS.md -->") {
		t.Fatalf("unexpected src AGENTS content: %q", string(srcContent))
	}
	if !strings.Contains(output, "nana AGENTS bootstrap") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestAgentsInitSkipsUnmanagedWithoutForce(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := cwd
	if err := os.MkdirAll(filepath.Join(cwd, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "templates", "AGENTS.md"), []byte("template\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# unmanaged\n"), 0o644); err != nil {
		t.Fatalf("write unmanaged: %v", err)
	}

	if err := AgentsInit(repoRoot, cwd, nil); err != nil {
		t.Fatalf("AgentsInit(): %v", err)
	}
	content, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read unmanaged: %v", err)
	}
	if string(content) != "# unmanaged\n" {
		t.Fatalf("unexpected root content: %q", string(content))
	}
}

func TestAgentsInitFallsBackToEmbeddedTemplate(t *testing.T) {
	cwd := t.TempDir()
	repoRoot := filepath.Join(cwd, "missing-repo-root")
	if err := os.MkdirAll(filepath.Join(cwd, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "src", "index.ts"), []byte("export const x = 1;\n"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}

	if err := AgentsInit(repoRoot, cwd, nil); err != nil {
		t.Fatalf("AgentsInit(): %v", err)
	}

	rootContent, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS: %v", err)
	}
	if !strings.Contains(string(rootContent), managedMarker) || !strings.Contains(string(rootContent), "./.codex") {
		t.Fatalf("unexpected embedded root AGENTS content: %q", string(rootContent))
	}
}
