package gocli

import (
	"io/fs"
	"os"
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

func TestCIGoTestJobRunsRepositoryTestSuite(t *testing.T) {
	root := repoRootFromCaller(t)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	content := string(workflow)
	testJob := workflowJobBlock(t, content, "test")
	if !strings.Contains(testJob, "go test ./...") {
		t.Fatalf("Go Test job must run the repository Go test suite before native builds; job was:\n%s", testJob)
	}
	buildJob := workflowJobBlock(t, content, "build")
	if !strings.Contains(buildJob, "needs: [fmt, vet, test, docs]") {
		t.Fatalf("native build job must depend on the Go Test job; job was:\n%s", buildJob)
	}
}

func workflowJobBlock(t *testing.T, content string, job string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	jobHeader := "  " + job + ":"
	start := -1
	for i, line := range lines {
		if line == jobHeader {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("ci workflow is missing job %q", job)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func repoRootFromCaller(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
