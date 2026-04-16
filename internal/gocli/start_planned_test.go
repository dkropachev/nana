package gocli

import (
	"os"
	"reflect"
	"testing"
)

func TestLaunchStartPlannedItemScheduledRunsLocalWorkInline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repoPath := githubManagedPaths(repoSlug).SourcePath
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo path: %v", err)
	}

	oldRunner := startRunScheduledPlannedLocalWork
	defer func() { startRunScheduledPlannedLocalWork = oldRunner }()

	var gotCWD string
	var gotArgs []string
	startRunScheduledPlannedLocalWork = func(cwd string, args []string) error {
		gotCWD = cwd
		gotArgs = append([]string{}, args...)
		return nil
	}

	result, err := launchStartPlannedItemScheduled("", repoSlug, startWorkOptions{
		RepoMode:  "local",
		CodexArgs: []string{"--model", "gpt-5.4"},
	}, startWorkPlannedItem{
		ID:          "planned-1",
		RepoSlug:    repoSlug,
		Title:       "Nightly cleanup",
		Description: "Tighten scheduler defaults",
		LaunchKind:  "local_work",
	})
	if err != nil {
		t.Fatalf("launchStartPlannedItemScheduled: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("expected completed result, got %+v", result)
	}
	if gotCWD != repoPath {
		t.Fatalf("expected cwd %q, got %q", repoPath, gotCWD)
	}
	wantArgs := []string{
		"start",
		"--repo",
		repoPath,
		"--task",
		"Nightly cleanup\n\nTighten scheduler defaults",
		"--",
		"--model",
		"gpt-5.4",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", gotArgs, wantArgs)
	}
}

func TestLaunchStartPlannedItemScheduledRunsTrackedIssueInline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repoPath := githubManagedPaths(repoSlug).SourcePath
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo path: %v", err)
	}

	oldRunner := startRunScheduledPlannedGithubWork
	defer func() { startRunScheduledPlannedGithubWork = oldRunner }()

	var gotCWD string
	var gotArgs []string
	startRunScheduledPlannedGithubWork = func(cwd string, args []string) (githubCommandResult, error) {
		gotCWD = cwd
		gotArgs = append([]string{}, args...)
		return githubCommandResult{RunID: "gh-42"}, nil
	}

	result, err := launchStartPlannedItemScheduled("", repoSlug, startWorkOptions{
		RepoMode:  "repo",
		CodexArgs: []string{"--model", "gpt-5.4"},
	}, startWorkPlannedItem{
		ID:         "planned-2",
		RepoSlug:   repoSlug,
		Title:      "Implement tracked issue",
		LaunchKind: "tracked_issue",
		TargetURL:  "https://github.com/acme/widget/issues/42",
	})
	if err != nil {
		t.Fatalf("launchStartPlannedItemScheduled: %v", err)
	}
	if result.RunID != "gh-42" || result.Status != "completed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if gotCWD != repoPath {
		t.Fatalf("expected cwd %q, got %q", repoPath, gotCWD)
	}
	wantArgs := []string{
		"start",
		"https://github.com/acme/widget/issues/42",
		"--",
		"--model",
		"gpt-5.4",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", gotArgs, wantArgs)
	}
}
