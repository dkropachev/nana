package gocliassets

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPromptAssetsStayInSyncWithPromptFiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	prompts, err := Prompts()
	if err != nil {
		t.Fatalf("Prompts(): %v", err)
	}
	for _, name := range []string{"investigator.md", "investigation-validator.md", "improvement-scout.md", "enhancement-scout.md"} {
		diskContent, err := os.ReadFile(filepath.Join(repoRoot, "prompts", name))
		if err != nil {
			t.Fatalf("read prompt %s: %v", name, err)
		}
		embedded, ok := prompts[name]
		if !ok {
			t.Fatalf("embedded prompts missing %s", name)
		}
		if embedded != string(diskContent) {
			t.Fatalf("embedded prompt %s is out of sync with prompts/%s", name, name)
		}
	}
}
