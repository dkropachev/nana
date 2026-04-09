package legacyshim

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRepoRootPrefersExplicitRepoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	got := ResolveRepoRoot(root, "")
	if got != root {
		t.Fatalf("ResolveRepoRoot() = %q, want %q", got, root)
	}
}

func TestResolveLegacyCLIEntryFindsPrimaryAndFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist", "cli"), 0o755); err != nil {
		t.Fatalf("mkdir dist/cli: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "cli", "index.js"), []byte(""), 0o644); err != nil {
		t.Fatalf("write fallback: %v", err)
	}
	if got := ResolveLegacyCLIEntry(root); got != filepath.Join(root, "dist", "cli", "index.js") {
		t.Fatalf("ResolveLegacyCLIEntry() = %q", got)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "cli", "nana.js"), []byte(""), 0o644); err != nil {
		t.Fatalf("write primary: %v", err)
	}
	if got := ResolveLegacyCLIEntry(root); got != filepath.Join(root, "dist", "cli", "nana.js") {
		t.Fatalf("ResolveLegacyCLIEntry() = %q", got)
	}
}
