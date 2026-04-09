package gocli

import (
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestRepoContainsNoLegacyImplementationFiles(t *testing.T) {
	root := repoRootFromCaller(t)
	bannedExts := []string{".ts", ".js", ".mjs", ".cts", ".mts", ".py", ".sh"}
	bannedNames := []string{"package.json", "package-lock.json", "tsconfig.json", "tsconfig.no-unused.json", "biome.json"}
	ignoredDirs := map[string]bool{
		".git":         true,
		".nana":        true,
		"node_modules": true,
		"target":       true,
		"dist":         true,
	}

	var failures []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		if slices.Contains(bannedNames, base) {
			failures = append(failures, strings.TrimPrefix(path, root+string(filepath.Separator)))
			return nil
		}
		if slices.Contains(bannedExts, strings.ToLower(filepath.Ext(path))) {
			failures = append(failures, strings.TrimPrefix(path, root+string(filepath.Separator)))
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo: %v", walkErr)
	}
	if len(failures) > 0 {
		t.Fatalf("legacy implementation files remain:\n%s", strings.Join(failures, "\n"))
	}
}

func repoRootFromCaller(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
