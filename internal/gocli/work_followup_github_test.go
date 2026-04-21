package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGithubWorkFollowupLoopRunsUntilReviewerSignalsNoFollowups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	followupMarker := filepath.Join(home, "github-followup.marker")
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"repo=\"\"",
		"prompt=\"\"",
		"while [ $# -gt 0 ]; do",
		"  case \"$1\" in",
		"    exec) shift ;;",
		"    -C) repo=\"$2\"; shift 2 ;;",
		"    -) prompt=$(cat); shift ;;",
		"    *) prompt=\"$1\"; shift ;;",
		"  esac",
		"done",
		"case \"$prompt\" in",
		"  *\"Plan any in-scope followups for this Nana work run and return JSON only.\"*)",
		"    if [ -f \"$FAKE_GITHUB_FOLLOWUP_DONE\" ]; then",
		"      printf '{\"decision\":\"no_followups\",\"items\":[]}\\n'",
		"    else",
		"      printf '{\"decision\":\"followups\",\"items\":[{\"title\":\"Add regression coverage for GitHub flow\",\"kind\":\"test_coverage\",\"summary\":\"cover the new GitHub behavior\",\"rationale\":\"stability\",\"goal_alignment\":\"locks the implemented behavior\"}]}\\n'",
		"    fi",
		"    ;;",
		"  *\"Review this followup plan for scope discipline and return JSON only.\"*)",
		"    if [ -f \"$FAKE_GITHUB_FOLLOWUP_DONE\" ]; then",
		"      printf '{\"decision\":\"no_followups\",\"approved_items\":[],\"rejected_items\":[],\"summary\":\"done\"}\\n'",
		"    else",
		"      printf '{\"decision\":\"approved_followups\",\"approved_items\":[{\"title\":\"Add regression coverage for GitHub flow\",\"kind\":\"test_coverage\",\"summary\":\"cover the new GitHub behavior\",\"rationale\":\"stability\",\"goal_alignment\":\"locks the implemented behavior\"}],\"rejected_items\":[],\"summary\":\"approved\"}\\n'",
		"    fi",
		"    ;;",
		"  *\"# NANA Followup Implementation\"*)",
		"    : > \"$FAKE_GITHUB_FOLLOWUP_DONE\"",
		"    printf 'followup\\n' >> \"$repo/README.md\"",
		"    printf 'github-followup-implemented\\n'",
		"    ;;",
		"  *\"Review role:\"*)",
		"    printf '{\"findings\":[]}\\n'",
		"    ;;",
		"  *\"Review this local implementation and return JSON only.\"*)",
		"    printf '{\"findings\":[]}\\n'",
		"    ;;",
		"  *)",
		"    printf 'implemented\\n' >> \"$repo/README.md\"",
		"    printf 'fake-codex:%s\\n' \"$*\"",
		"    ;;",
		"esac",
		"",
	}, "\n"))
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_GITHUB_FOLLOWUP_DONE", followupMarker)

	manifestPath, runDir, repoCheckoutPath := createGithubCompletionRun(t, home, "gh-followup-loop")
	if err := os.WriteFile(filepath.Join(repoCheckoutPath, "README.md"), []byte("# local work\nfeature\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifest.WorkType = workTypeFeature
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return runGithubWorkFollowupLoop(manifestPath, runDir, &manifest, nil)
	})
	if err != nil {
		t.Fatalf("runGithubWorkFollowupLoop: %v\n%s", err, output)
	}
	updated, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read updated manifest: %v", err)
	}
	if updated.FollowupDecision != workFollowupDecisionNoFollowups || len(updated.FollowupRounds) != 2 {
		t.Fatalf("expected two followup rounds ending in no_followups, got %+v", updated)
	}
	if updated.FollowupRounds[0].ReviewDecision != workFollowupDecisionApprovedFollowup || updated.FollowupRounds[1].ReviewDecision != workFollowupDecisionNoFollowups {
		t.Fatalf("unexpected followup round summaries: %+v", updated.FollowupRounds)
	}
	readme, err := os.ReadFile(filepath.Join(repoCheckoutPath, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !strings.Contains(string(readme), "followup") {
		t.Fatalf("expected GitHub followup implementation to update README, got %q", string(readme))
	}
}
