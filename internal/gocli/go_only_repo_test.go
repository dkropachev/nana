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
	assertWorkflowStringSetEqual(
		t,
		workflowJobNeeds(t, content, "build"),
		[]string{"fmt", "vet", "test", "docs"},
		"native build job must depend on the Go Test job and the other blocking pre-build checks",
	)
}

func TestCIScheduledBenchmarksStayNonBlockingSnapshots(t *testing.T) {
	root := repoRootFromCaller(t)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	benchmarksJob := workflowJobBlock(t, string(workflow), "benchmarks")
	const nonBlockingSnapshotGate = "continue-on-error: true"
	if !strings.Contains(benchmarksJob, nonBlockingSnapshotGate) {
		t.Fatalf("benchmark job must stay non-blocking because the workflow only publishes benchmark snapshots; job was:\n%s", benchmarksJob)
	}
}

func TestPushAndPRCIStatusDoesNotWaitOnBenchmarks(t *testing.T) {
	root := repoRootFromCaller(t)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	content := string(workflow)
	ciStatusNeeds := workflowJobNeeds(t, content, "ci-status")
	if slices.Contains(ciStatusNeeds, "benchmarks") {
		t.Fatalf("push/PR CI status must not depend on benchmarks because benchmark failures stay non-blocking; needs were %v", ciStatusNeeds)
	}
	assertWorkflowStringSetEqual(
		t,
		ciStatusNeeds,
		[]string{"fmt", "vet", "test", "docs", "build"},
		"push/PR CI status must wait only for the required blocking jobs",
	)
	ciStatusJob := workflowJobBlock(t, content, "ci-status")
	if strings.Contains(ciStatusJob, "needs.benchmarks.result") {
		t.Fatalf("push/PR CI status must not check benchmark results; job was:\n%s", ciStatusJob)
	}
}

func TestWorkflowJobNeedsParsesInlineAndMultilineLists(t *testing.T) {
	t.Run("inline list", func(t *testing.T) {
		content := "jobs:\n  ci-status:\n    needs: [fmt, vet, build]\n"
		assertWorkflowStringSetEqual(
			t,
			workflowJobNeeds(t, content, "ci-status"),
			[]string{"fmt", "vet", "build"},
			"workflow needs parser must support inline lists",
		)
	})

	t.Run("multiline list", func(t *testing.T) {
		content := "jobs:\n  ci-status:\n    needs:\n      - fmt\n      - vet\n      - build\n    runs-on: ubuntu-latest\n"
		assertWorkflowStringSetEqual(
			t,
			workflowJobNeeds(t, content, "ci-status"),
			[]string{"fmt", "vet", "build"},
			"workflow needs parser must support multiline lists",
		)
	})
}

func TestCIWorkflowScopesDefaultPermissionsReadOnly(t *testing.T) {
	root := repoRootFromCaller(t)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	assertWorkflowStringMapEqual(
		t,
		workflowTopLevelPermissions(t, string(workflow)),
		map[string]string{"contents": "read"},
		"ci workflow must scope the default token to read-only contents when scheduled runs execute external actions",
	)
}

func TestWorkflowTopLevelPermissionsParsesInlineAndMultilineMappings(t *testing.T) {
	t.Run("multiline mapping", func(t *testing.T) {
		content := "permissions:\n  contents: read\nconcurrency:\n  cancel-in-progress: true\n"
		assertWorkflowStringMapEqual(
			t,
			workflowTopLevelPermissions(t, content),
			map[string]string{"contents": "read"},
			"workflow permissions parser must support multiline mappings",
		)
	})

	t.Run("inline mapping", func(t *testing.T) {
		content := "permissions: { contents: read }\nconcurrency:\n  cancel-in-progress: true\n"
		assertWorkflowStringMapEqual(
			t,
			workflowTopLevelPermissions(t, content),
			map[string]string{"contents": "read"},
			"workflow permissions parser must support inline mappings",
		)
	})
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

func workflowJobNeeds(t *testing.T, content string, job string) []string {
	t.Helper()
	lines := strings.Split(workflowJobBlock(t, content, job), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "    needs:") {
			continue
		}
		needs, ok := parseWorkflowList(lines, i, strings.TrimSpace(strings.TrimPrefix(line, "    needs:")), "      ")
		if !ok {
			t.Fatalf("ci workflow job %q must declare a non-empty needs list; job was:\n%s", job, strings.Join(lines, "\n"))
		}
		return needs
	}
	t.Fatalf("ci workflow job %q is missing needs", job)
	return nil
}

func workflowTopLevelPermissions(t *testing.T, content string) map[string]string {
	t.Helper()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "permissions:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "permissions:"))
		switch {
		case raw == "":
			permissions := parseWorkflowIndentedMap(lines[i+1:], "  ")
			if len(permissions) == 0 {
				t.Fatalf("ci workflow permissions block must not be empty")
			}
			return permissions
		case strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}"):
			permissions := parseWorkflowInlineMap(t, raw)
			if len(permissions) == 0 {
				t.Fatalf("ci workflow permissions block must not be empty")
			}
			return permissions
		default:
			t.Fatalf("ci workflow permissions must be declared as a mapping; line was %q", line)
		}
	}
	t.Fatal("ci workflow is missing top-level permissions")
	return nil
}

func parseWorkflowList(lines []string, start int, raw string, itemIndent string) ([]string, bool) {
	if raw != "" {
		return parseWorkflowInlineList(raw), true
	}
	var items []string
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		prefix := itemIndent + "- "
		if strings.HasPrefix(line, prefix) {
			items = append(items, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
			continue
		}
		break
	}
	return items, len(items) > 0
}

func parseWorkflowInlineList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "[") {
		return []string{raw}
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
	if inner == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseWorkflowIndentedMap(lines []string, indent string) map[string]string {
	entries := map[string]string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, indent) {
			break
		}
		if strings.HasPrefix(line, indent+"  ") {
			break
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			break
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			break
		}
		entries[key] = value
	}
	return entries
}

func parseWorkflowInlineMap(t *testing.T, raw string) map[string]string {
	t.Helper()
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(raw), "{"), "}"))
	if inner == "" {
		return map[string]string{}
	}
	entries := map[string]string{}
	for _, part := range strings.Split(inner, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			t.Fatalf("workflow inline mapping entry %q is missing a colon", part)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			t.Fatalf("workflow inline mapping entry %q must have a non-empty key and value", part)
		}
		entries[key] = value
	}
	return entries
}

func assertWorkflowStringSetEqual(t *testing.T, got, want []string, message string) {
	t.Helper()
	gotSorted := append([]string(nil), got...)
	wantSorted := append([]string(nil), want...)
	slices.Sort(gotSorted)
	slices.Sort(wantSorted)
	if !slices.Equal(gotSorted, wantSorted) {
		t.Fatalf("%s; got %v want %v", message, got, want)
	}
}

func assertWorkflowStringMapEqual(t *testing.T, got, want map[string]string, message string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s; got %v want %v", message, got, want)
	}
	for key, wantValue := range want {
		if gotValue, ok := got[key]; !ok || gotValue != wantValue {
			t.Fatalf("%s; got %v want %v", message, got, want)
		}
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
