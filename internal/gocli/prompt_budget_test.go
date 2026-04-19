package gocli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMergePromptDocumentsDedupesRepeatedBlocks(t *testing.T) {
	merged := mergePromptDocuments(
		"# Shared\n\nRepeated block.\n\n# User Only\n\nKeep me.",
		"# Shared\n\nRepeated block.\n\n# Project Only\n\nKeep me too.",
	)
	if strings.Count(merged, "# Shared") != 1 {
		t.Fatalf("expected shared block once, got %q", merged)
	}
	for _, needle := range []string{"# User Only", "# Project Only"} {
		if !strings.Contains(merged, needle) {
			t.Fatalf("expected merged prompt to contain %q: %q", needle, merged)
		}
	}
}

func TestPromptTransportForSizeUsesStdinForLargePrompts(t *testing.T) {
	largePrompt := strings.Repeat("x", structuredPromptStdinThreshold+1)
	if got := promptTransportForSize(largePrompt, structuredPromptStdinThreshold); got != codexPromptTransportStdin {
		t.Fatalf("expected stdin transport, got %s", got)
	}
	if got := promptTransportForSize("short prompt", structuredPromptStdinThreshold); got != codexPromptTransportArg {
		t.Fatalf("expected arg transport, got %s", got)
	}
}

func TestCompactPromptHeadValueKeepsLeadingContent(t *testing.T) {
	value := strings.Join([]string{"one", "two", "three", "four"}, "\n")
	got := compactPromptHeadValue(value, 2, 0)
	if got != "one\ntwo\n... [truncated]" {
		t.Fatalf("unexpected head-compacted value: %q", got)
	}
}

func TestSummarizeUnifiedDiffForPromptCapsFilesAndHunks(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/alpha.go b/alpha.go",
		"index 1111111..2222222 100644",
		"--- a/alpha.go",
		"+++ b/alpha.go",
		"@@ -1,2 +1,2 @@",
		"-old1",
		"+new1",
		"@@ -5,2 +5,2 @@",
		"-old2",
		"+new2",
		"@@ -9,2 +9,2 @@",
		"-old3",
		"+new3",
		"diff --git a/beta.go b/beta.go",
		"index 3333333..4444444 100644",
		"--- a/beta.go",
		"+++ b/beta.go",
		"@@ -1 +1 @@",
		"-beta-old",
		"+beta-new",
		"diff --git a/gamma.go b/gamma.go",
		"index 5555555..6666666 100644",
		"--- a/gamma.go",
		"+++ b/gamma.go",
		"@@ -1 +1 @@",
		"-gamma-old",
		"+gamma-new",
	}, "\n")
	summary := summarizeUnifiedDiffForPrompt(diff, reviewPromptContextOptions{
		ChangedFilesLimit: 2,
		MaxHunksPerFile:   2,
		MaxLinesPerFile:   4,
		MaxCharsPerFile:   400,
	})
	for _, needle := range []string{"File: alpha.go", "File: beta.go", "[... omitted 1 additional changed file(s) ...]", "[... omitted 1 additional hunk(s) ...]"} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("expected diff summary to contain %q:\n%s", needle, summary)
		}
	}
	if strings.Contains(summary, "gamma-new") {
		t.Fatalf("expected gamma diff to be omitted:\n%s", summary)
	}
}

func TestSelectLocalWorkFinalGateRolesUsesChangedFilesAndPlan(t *testing.T) {
	roles := selectLocalWorkFinalGateRoles([]string{
		filepath.ToSlash(".github/workflows/ci.yml"),
		filepath.ToSlash("cmd/nana/main.go"),
		filepath.ToSlash("pkg/search/cache.go"),
	}, nil)
	want := []string{"quality-reviewer", "security-reviewer", "qa-tester", "performance-reviewer"}
	if !slices.Equal(roles, want) {
		t.Fatalf("unexpected roles: got=%v want=%v", roles, want)
	}

	planRoles := selectLocalWorkFinalGateRoles([]string{"README.md"}, &githubVerificationPlan{Benchmarks: []string{"go test -bench ./..."}})
	if !slices.Equal(planRoles, []string{"quality-reviewer", "performance-reviewer"}) {
		t.Fatalf("unexpected benchmark-plan roles: %v", planRoles)
	}
}

func TestFreshAgentsMergeStaysWithinBudget(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	templateContent, err := os.ReadFile(filepath.Join(repoRoot, "templates", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read template AGENTS: %v", err)
	}
	rootContent, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS: %v", err)
	}
	freshUser := strings.ReplaceAll(string(templateContent), "~/.codex", "./.codex")
	merged := mergePromptDocuments(
		addGeneratedAgentsMarker(freshUser),
		string(rootContent),
		"<!-- NANA:RUNTIME:START -->\n<session_context>\n**Session:** s | now\n\n**Compaction Protocol:**\nBefore context compaction, preserve critical state.\n</session_context>\n<!-- NANA:RUNTIME:END -->",
	)
	if len(merged) > agentsPromptCharLimit {
		t.Fatalf("expected merged fresh AGENTS <= %d bytes, got %d", agentsPromptCharLimit, len(merged))
	}
}
