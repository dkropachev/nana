package docscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveTreeDoesNotContainRetiredVerifyLoopNameOutsideReleaseNotes(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	allowedPrefixes := []string{
		"docs/release-notes-",
	}
	roots := []string{
		"README.md",
		"AGENTS.md",
		"COVERAGE.md",
		"templates",
		"docs",
		"skills",
		"internal",
		"cmd",
		"prompts",
	}

	tokens := retiredVerifyLoopNameTokens()

	isAllowed := func(rel string) bool {
		normalized := filepath.ToSlash(rel)
		for _, prefix := range allowedPrefixes {
			if strings.HasPrefix(normalized, prefix) {
				return true
			}
		}
		return false
	}

	for _, root := range roots {
		path := filepath.Join(repoRoot, root)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", root, err)
		}
		if !info.IsDir() {
			assertFileHasNoRetiredVerifyLoopTokens(t, repoRoot, root, tokens, isAllowed)
			continue
		}
		if err := filepath.WalkDir(path, func(current string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules":
					return filepath.SkipDir
				default:
					return nil
				}
			}
			rel, err := filepath.Rel(repoRoot, current)
			if err != nil {
				return err
			}
			assertFileHasNoRetiredVerifyLoopTokens(t, repoRoot, rel, tokens, isAllowed)
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}

func assertFileHasNoRetiredVerifyLoopTokens(t *testing.T, repoRoot string, rel string, tokens []string, isAllowed func(string) bool) {
	t.Helper()
	if isAllowed(rel) {
		return
	}
	content, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	text := string(content)
	for _, token := range tokens {
		if strings.Contains(text, token) {
			t.Fatalf("live file %s contains forbidden token %q", filepath.ToSlash(rel), token)
		}
	}
}

func retiredVerifyLoopNameTokens() []string {
	rootLower := strings.Join([]string{"ra", "lph"}, "")
	rootUpper := strings.Join([]string{"Ra", "lph"}, "")
	return []string{
		rootLower,
		rootUpper,
		"$" + rootLower,
		rootLower + "-state",
		rootLower + "-progress",
		rootLower + "-verify",
		"team -> " + rootLower,
		"team/" + rootLower,
	}
}
