package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalWorkRunsFollowupPlannerReviewerCycleUntilDone(t *testing.T) {
	repo := createLocalWorkRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte(strings.Join([]string{
		"lint:",
		"\t@true",
		"build:",
		"\t@true",
		"test:",
		"\t@true",
		"test-integration:",
		"\t@true",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("rewrite Makefile: %v", err)
	}
	runLocalWorkTestGit(t, repo, "add", "Makefile")
	runLocalWorkTestGit(t, repo, "commit", "-m", "quiet verification")
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	followupMarker := filepath.Join(home, "followup-done.marker")
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"Plan any in-scope followups for this Nana work run and return JSON only."*)`,
		`    if [ -f "$FAKE_FOLLOWUP_DONE_PATH" ]; then`,
		`      printf '{"decision":"no_followups","items":[]}\n'`,
		`    else`,
		`      printf '{"decision":"followups","items":[{"title":"Add regression coverage for followup path","kind":"test_coverage","summary":"cover the new behavior","rationale":"stability","goal_alignment":"locks the implemented behavior"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *"Review this followup plan for scope discipline and return JSON only."*)`,
		`    if [ -f "$FAKE_FOLLOWUP_DONE_PATH" ]; then`,
		`      printf '{"decision":"no_followups","approved_items":[],"rejected_items":[],"summary":"done"}\n'`,
		`    else`,
		`      printf '{"decision":"approved_followups","approved_items":[{"title":"Add regression coverage for followup path","kind":"test_coverage","summary":"cover the new behavior","rationale":"stability","goal_alignment":"locks the implemented behavior"}],"rejected_items":[],"summary":"approved"}\n'`,
		`    fi`,
		`    ;;`,
		`  *"Review role: quality-reviewer"*|*"Review role: security-reviewer"*|*"Review role: performance-reviewer"*|*"Review role: qa-tester"*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Iteration"*)`,
		`    if printf '%s' "$PAYLOAD" | grep -q 'Approved followups from round'; then`,
		`      : > "$FAKE_FOLLOWUP_DONE_PATH"`,
		`      printf 'followup\n' >> "$NANA_PROJECT_AGENTS_ROOT/README.md"`,
		`    fi`,
		`    printf 'implemented\n' >> "$NANA_PROJECT_AGENTS_ROOT/README.md"`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		`  *)`,
		`    printf 'noop:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_FOLLOWUP_DONE_PATH", followupMarker)

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"start", "--task", "Implement the local followup cycle", "--work-type", workTypeFeature, "--max-iterations", "4"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v\n%s", err, output)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if manifest.Status != "completed" || manifest.FinalApplyStatus != "committed" {
		t.Fatalf("expected completed committed run, got %+v", manifest)
	}
	if len(manifest.Iterations) != 2 {
		t.Fatalf("expected two iterations after one approved followup round, got %+v", manifest.Iterations)
	}
	first := manifest.Iterations[0]
	if first.FollowupReviewDecision != workFollowupDecisionApprovedFollowup || len(first.ApprovedFollowupItems) != 1 {
		t.Fatalf("expected first iteration to queue one approved followup, got %+v", first)
	}
	second := manifest.Iterations[1]
	if second.FollowupReviewDecision != workFollowupDecisionNoFollowups {
		t.Fatalf("expected second iteration to stop followups, got %+v", second)
	}
	if manifest.FollowupDecision != workFollowupDecisionNoFollowups || len(manifest.FollowupRounds) != 2 {
		t.Fatalf("expected persisted followup summary on manifest, got %+v", manifest)
	}
	readme, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !strings.Contains(string(readme), "followup") {
		t.Fatalf("expected followup implementation to reach source branch, got %q", string(readme))
	}
}
