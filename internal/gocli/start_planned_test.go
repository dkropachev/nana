package gocli

import (
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"testing"
)

func TestLaunchStartPlannedItemScheduledRunsLocalWorkInline(t *testing.T) {
	_ = setLocalWorkDBProxyTestHome(t)

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
		WorkType:    workTypeRefactor,
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
		"--run-id",
		result.RunID,
		"--work-type",
		workTypeRefactor,
		"--",
		"--model",
		"gpt-5.4",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", gotArgs, wantArgs)
	}
}

func TestLaunchStartPlannedItemScheduledRunsTrackedIssueInline(t *testing.T) {
	_ = setLocalWorkDBProxyTestHome(t)

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
		WorkType:   workTypeBugFix,
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
		"--work-type",
		workTypeBugFix,
		"--",
		"--model",
		"gpt-5.4",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", gotArgs, wantArgs)
	}
}

func TestLaunchStartPlannedItemScheduledRunsInvestigationTask(t *testing.T) {
	repoSlug := "acme/widget"
	repoPath := githubManagedPaths(repoSlug).SourcePath
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo path: %v", err)
	}

	oldRunner := startRunScheduledPlannedInvestigation
	defer func() { startRunScheduledPlannedInvestigation = oldRunner }()

	var gotRepo string
	var gotQuery string
	var gotRunID string
	startRunScheduledPlannedInvestigation = func(repoSlug string, query string, runID string) (startUIBackgroundLaunch, error) {
		gotRepo = repoSlug
		gotQuery = query
		gotRunID = runID
		return startUIBackgroundLaunch{
			Status: "spawned",
			Result: "investigation started",
		}, nil
	}

	result, err := launchStartPlannedItemScheduled("", repoSlug, startWorkOptions{
		RepoMode: "disabled",
	}, startWorkPlannedItem{
		ID:                 "planned-investigation",
		RepoSlug:           repoSlug,
		Title:              "Investigate queue drift",
		LaunchKind:         "investigation",
		InvestigationQuery: "Investigate why approval retry timing drifts between the queue and the drawer.",
	})
	if err != nil {
		t.Fatalf("launchStartPlannedItemScheduled: %v", err)
	}
	if gotRepo != repoSlug {
		t.Fatalf("expected repo %q, got %q", repoSlug, gotRepo)
	}
	if gotQuery == "" {
		t.Fatalf("expected investigation query, got empty string")
	}
	if gotRunID != result.RunID || gotRunID == "" {
		t.Fatalf("expected investigation run id %q, got %q", result.RunID, gotRunID)
	}
	if result.Status != "spawned" || result.Result != "investigation started" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestLaunchStartPlannedItemScheduledRunsManualScoutTask(t *testing.T) {
	oldRunner := startRunScheduledPlannedScout
	defer func() { startRunScheduledPlannedScout = oldRunner }()

	var gotOptions ImproveOptions
	var gotRole string
	var gotPolicy scoutPolicy
	startRunScheduledPlannedScout = func(cwd string, options ImproveOptions, role string, policy scoutPolicy) error {
		gotOptions = options
		gotRole = role
		gotPolicy = policy
		return nil
	}

	result, err := launchStartPlannedItemScheduled("", "acme/widget", startWorkOptions{
		RepoMode:  "disabled",
		CodexArgs: []string{"--model", "gpt-5.4"},
	}, startWorkPlannedItem{
		ID:                "planned-scout",
		RepoSlug:          "acme/widget",
		Title:             "Run UI scout",
		LaunchKind:        "manual_scout",
		ScoutRole:         uiScoutRole,
		ScoutDestination:  improvementDestinationReview,
		ScoutSessionLimit: 3,
		ScoutFocus:        []string{"approvals", "retry"},
	})
	if err != nil {
		t.Fatalf("launchStartPlannedItemScheduled: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if gotRole != uiScoutRole {
		t.Fatalf("expected role %q, got %q", uiScoutRole, gotRole)
	}
	if gotOptions.RunID == "" || gotOptions.RunID != result.RunID {
		t.Fatalf("expected stable scout run id, got options=%q result=%q", gotOptions.RunID, result.RunID)
	}
	if !reflect.DeepEqual(gotOptions.Focus, []string{"approvals", "retry"}) {
		t.Fatalf("unexpected focus: %#v", gotOptions.Focus)
	}
	if !reflect.DeepEqual(gotOptions.CodexArgs, []string{"--model", "gpt-5.4"}) {
		t.Fatalf("unexpected codex args: %#v", gotOptions.CodexArgs)
	}
	if gotPolicy.IssueDestination != improvementDestinationReview || gotPolicy.SessionLimit != 3 {
		t.Fatalf("unexpected scout policy: %+v", gotPolicy)
	}
}

func TestLaunchStartPlannedItemScheduledManualScoutAutoPromoteUsesReviewArtifacts(t *testing.T) {
	oldRunner := startRunScheduledPlannedScout
	defer func() { startRunScheduledPlannedScout = oldRunner }()

	var gotOptions ImproveOptions
	var gotPolicy scoutPolicy
	startRunScheduledPlannedScout = func(cwd string, options ImproveOptions, role string, policy scoutPolicy) error {
		gotOptions = options
		gotPolicy = policy
		return nil
	}

	result, err := launchStartPlannedItemScheduled("", "acme/widget", startWorkOptions{}, startWorkPlannedItem{
		ID:               "planned-scout-auto",
		RepoSlug:         "acme/widget",
		Title:            "Run auto-promote scout",
		LaunchKind:       "manual_scout",
		ScoutRole:        uiScoutRole,
		ScoutDestination: improvementDestinationLocal,
		FindingsHandling: startWorkFindingsHandlingAutoPromote,
	})
	if err != nil {
		t.Fatalf("launchStartPlannedItemScheduled: %v", err)
	}
	if result.RunID == "" || gotOptions.RunID != result.RunID {
		t.Fatalf("expected stable scout run id, got options=%q result=%q", gotOptions.RunID, result.RunID)
	}
	if gotPolicy.IssueDestination != improvementDestinationReview {
		t.Fatalf("expected auto-promote scout to keep review artifacts, got %+v", gotPolicy)
	}
}

func TestLaunchStartPlannedLocalWorkChildInheritsDBProxyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	_ = setLocalWorkDBProxyTestHome(t)

	repoSlug := "acme/widget"
	repoPath := githubManagedPaths(repoSlug).SourcePath
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo path: %v", err)
	}
	supervisor, err := launchLocalWorkDBProxySupervisor()
	if err != nil {
		t.Fatalf("launchLocalWorkDBProxySupervisor: %v", err)
	}
	defer supervisor.Close()
	started := false
	setStartManagedNanaStartForTest(t, func(cmd *exec.Cmd) error {
		started = true
		assertStartManagedNanaLaunchUsesSocketPresence(t, cmd)
		return nil
	})

	result, err := launchStartPlannedLocalWork(repoSlug, startWorkPlannedItem{
		ID:          "planned-local",
		RepoSlug:    repoSlug,
		Title:       "Nightly cleanup",
		Description: "Tighten scheduler defaults",
		WorkType:    workTypeRefactor,
		LaunchKind:  "local_work",
	}, nil)
	if err != nil {
		t.Fatalf("launchStartPlannedLocalWork: %v", err)
	}
	if result.Status != "spawned" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !started {
		t.Fatalf("expected managed child launch to start")
	}
}

func TestLaunchStartPlannedTrackedIssueChildInheritsDBProxyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	_ = setLocalWorkDBProxyTestHome(t)

	repoSlug := "acme/widget"
	repoPath := githubManagedPaths(repoSlug).SourcePath
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo path: %v", err)
	}
	supervisor, err := launchLocalWorkDBProxySupervisor()
	if err != nil {
		t.Fatalf("launchLocalWorkDBProxySupervisor: %v", err)
	}
	defer supervisor.Close()
	started := false
	setStartManagedNanaStartForTest(t, func(cmd *exec.Cmd) error {
		started = true
		assertStartManagedNanaLaunchUsesSocketPresence(t, cmd)
		return nil
	})

	result, err := launchStartPlannedTrackedIssue(repoSlug, startWorkPlannedItem{
		ID:         "planned-tracked",
		RepoSlug:   repoSlug,
		Title:      "Implement tracked issue",
		WorkType:   workTypeBugFix,
		LaunchKind: "tracked_issue",
		TargetURL:  "https://github.com/acme/widget/issues/42",
	})
	if err != nil {
		t.Fatalf("launchStartPlannedTrackedIssue: %v", err)
	}
	if result.Status != "spawned" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !started {
		t.Fatalf("expected managed child launch to start")
	}
}
