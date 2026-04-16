package legacyshim

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRepoRootPrefersExplicitRepoRootWithGoMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	got := ResolveRepoRoot(root, "")
	if got != root {
		t.Fatalf("ResolveRepoRoot() = %q, want %q", got, root)
	}
}

func TestResolveRepoRootAcceptsPromptSkillMarkerLayout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}

	got := ResolveRepoRoot(root, "")
	if got != root {
		t.Fatalf("ResolveRepoRoot() = %q, want %q", got, root)
	}
}
