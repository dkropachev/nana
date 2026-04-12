package gocli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStartRunsEnabledOnboardedReposAndSkipsManual(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeGithubJSON(githubRepoSettingsPath("acme/enabled"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "label", PRForwardMode: "auto", ForkIssuesMode: "labeled", ImplementMode: "labeled", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write enabled settings: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath("acme/manual"), githubRepoSettings{Version: 6, RepoMode: "local", IssuePickMode: "manual", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write manual settings: %v", err)
	}
	if err := writeGithubJSON(startWorkStatePath("acme/enabled"), startWorkState{Version: startWorkStateVersion, SourceRepo: "acme/enabled", UpdatedAt: "now", Issues: map[string]startWorkIssueState{}}); err != nil {
		t.Fatalf("write fork state: %v", err)
	}

	oldRun := startRunStartWork
	oldPromote := startPromoteStartWork
	runs := []startWorkOptions{}
	promotes := []startWorkOptions{}
	startRunStartWork = func(options startWorkOptions) error {
		runs = append(runs, options)
		return nil
	}
	startPromoteStartWork = func(options startWorkOptions) error {
		promotes = append(promotes, options)
		return nil
	}
	defer func() {
		startRunStartWork = oldRun
		startPromoteStartWork = oldPromote
	}()

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"--parallel", "2", "--max-open-prs", "7", "--", "--model", "gpt-5.4"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	if len(runs) != 1 || runs[0].RepoSlug != "acme/enabled" {
		t.Fatalf("expected one enabled run, got %#v", runs)
	}
	if runs[0].RepoMode != "fork" || runs[0].IssuePickMode != "label" || runs[0].PRForwardMode != "auto" || runs[0].ForkIssuesMode != "labeled" || runs[0].ImplementMode != "labeled" || runs[0].PublishTarget != "fork" || runs[0].Parallel != 2 || runs[0].MaxOpenPR != 7 || !reflect.DeepEqual(runs[0].CodexArgs, []string{"--model", "gpt-5.4"}) {
		t.Fatalf("unexpected run options: %#v", runs[0])
	}
	if len(promotes) != 1 || promotes[0].RepoSlug != "acme/enabled" {
		t.Fatalf("expected existing fork state to promote first, got %#v", promotes)
	}
	if strings.Contains(output, "acme/manual") {
		t.Fatalf("manual repo should not be selected, output=%q", output)
	}
}

func TestStartRunsScoutsBetweenIssuePickupPasses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeGithubJSON(githubRepoSettingsPath("acme/cycled"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	sourcePath := githubManagedPaths("acme/cycled").SourcePath
	if err := os.MkdirAll(filepath.Join(sourcePath, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir policy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".nana", "improvement-policy.json"), []byte(`{"version":1,"issue_destination":"repo"}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	oldRun := startRunStartWork
	oldPromote := startPromoteStartWork
	oldScout := startRunScoutStart
	events := []string{}
	startRunStartWork = func(options startWorkOptions) error {
		events = append(events, "work:"+options.RepoSlug)
		if options.PublishTarget != "fork" || options.ForkIssuesMode != "auto" || options.ImplementMode != "auto" {
			t.Fatalf("unexpected work options: %#v", options)
		}
		return nil
	}
	startPromoteStartWork = func(options startWorkOptions) error {
		events = append(events, "promote:"+options.RepoSlug)
		return nil
	}
	startRunScoutStart = func(cwd string, options ImproveOptions) error {
		events = append(events, "scout:"+options.Target)
		if options.Target != "acme/cycled" || strings.Join(options.Focus, ",") != "ux,perf" {
			t.Fatalf("unexpected scout options: %#v", options)
		}
		return nil
	}
	defer func() {
		startRunStartWork = oldRun
		startPromoteStartWork = oldPromote
		startRunScoutStart = oldScout
	}()

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"--repo", "acme/cycled"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	expected := []string{"work:acme/cycled", "scout:acme/cycled", "work:acme/cycled"}
	if !reflect.DeepEqual(events, expected) {
		t.Fatalf("expected issue pickup, scouts, issue pickup; got %#v", events)
	}
	if !strings.Contains(output, "scouts finished; refreshing issue queue") {
		t.Fatalf("expected scout refresh output, got %q", output)
	}
}

func TestStartCyclesRepeatRepoAutomationCycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeGithubJSON(githubRepoSettingsPath("acme/repeat"), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "repo"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	oldRun := startRunStartWork
	oldPromote := startPromoteStartWork
	oldScout := startRunScoutStart
	runCount := 0
	startRunStartWork = func(options startWorkOptions) error {
		runCount++
		return nil
	}
	startPromoteStartWork = func(options startWorkOptions) error { return nil }
	startRunScoutStart = func(cwd string, options ImproveOptions) error {
		t.Fatal("scouts should not run without policy files")
		return nil
	}
	defer func() {
		startRunStartWork = oldRun
		startPromoteStartWork = oldPromote
		startRunScoutStart = oldScout
	}()

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"--repo", "acme/repeat", "--cycles", "2"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	if runCount != 2 {
		t.Fatalf("expected one work pass per cycle without scouts, got %d", runCount)
	}
	if !strings.Contains(output, "Cycle 1/2") || !strings.Contains(output, "Cycle 2/2") {
		t.Fatalf("expected cycle progress output, got %q", output)
	}
}

func TestStartBareLocalScoutPoliciesLoopsForeverUntilStopped(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir policy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	oldScout := startRunScoutStart
	oldSleep := startLoopSleep
	oldContinue := startLoopContinue
	runCount := 0
	startRunScoutStart = func(cwd string, options ImproveOptions) error {
		runCount++
		if cwd != repo {
			t.Fatalf("unexpected cwd: %s", cwd)
		}
		return nil
	}
	startLoopSleep = func(duration time.Duration) {}
	startLoopContinue = func() bool { return runCount < 2 }
	defer func() {
		startRunScoutStart = oldScout
		startLoopSleep = oldSleep
		startLoopContinue = oldContinue
	}()

	output, err := captureStdout(t, func() error {
		return Start(repo, nil)
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	if runCount != 2 {
		t.Fatalf("expected two scout startup cycles before test stop, got %d", runCount)
	}
	if !strings.Contains(output, "Cycle 1/forever") || !strings.Contains(output, "Cycle 2/forever") {
		t.Fatalf("expected forever cycle output, got %q", output)
	}
}

func TestRepoConfigAndExplainAutomationModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	output, err := captureStdout(t, func() error {
		return Repo(".", []string{"config", "acme/widget", "--repo-mode", "fork", "--issue-pick", "label", "--pr-forward", "auto"})
	})
	if err != nil {
		t.Fatalf("Repo(config): %v\n%s", err, output)
	}
	settings, err := readGithubRepoSettings(githubRepoSettingsPath("acme/widget"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if settings.RepoMode != "fork" || settings.IssuePickMode != "label" || settings.PRForwardMode != "auto" || settings.ForkIssuesMode != "labeled" || settings.ImplementMode != "labeled" || settings.PublishTarget != "fork" {
		t.Fatalf("unexpected settings modes: %+v", settings)
	}
	if err := writeStartWorkState(startWorkState{Version: startWorkStateVersion, SourceRepo: "acme/widget", ForkRepo: "me/widget", UpdatedAt: "now", Issues: map[string]startWorkIssueState{}, PromotionSkips: map[string]startWorkPromotionSkip{"7": {ForkPRNumber: 7, Reason: "fork PR is draft"}}}); err != nil {
		t.Fatalf("write start state: %v", err)
	}
	explain, err := captureStdout(t, func() error { return Repo(".", []string{"explain", "acme/widget"}) })
	if err != nil {
		t.Fatalf("Repo(explain): %v", err)
	}
	for _, needle := range []string{"repo-mode: fork", "issue-pick: label", "pr-forward: auto", "publish: fork", "nana start", "single opt-in label: nana", "Forwarding: promoted=0 reused=0 active_skips=1", "Forward skips: fork PR #7: fork PR is draft"} {
		if !strings.Contains(explain, needle) {
			t.Fatalf("expected explain to contain %q, got %q", needle, explain)
		}
	}
	if _, err := os.Stat(githubRepoSettingsPath("acme/widget")); err != nil {
		t.Fatalf("expected settings file: %v", err)
	}
}

func TestRepoDefaultsApplyOnlyToManualGithubOnboard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := captureStdout(t, func() error {
		return Repo(".", []string{"defaults", "set", "--repo-mode", "fork", "--issue-pick", "label", "--pr-forward", "auto"})
	}); err != nil {
		t.Fatalf("Repo(defaults set): %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return Repo(".", []string{"onboard", "acme/widget"})
	}); err != nil {
		t.Fatalf("Repo(onboard github): %v", err)
	}
	settings, err := readGithubRepoSettings(githubRepoSettingsPath("acme/widget"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if settings.RepoMode != "fork" || settings.IssuePickMode != "label" || settings.PRForwardMode != "auto" || settings.ForkIssuesMode != "labeled" || settings.ImplementMode != "labeled" || settings.PublishTarget != "fork" {
		t.Fatalf("expected manual defaults to apply, got %+v", settings)
	}

	settings = &githubRepoSettings{Version: 5, UpdatedAt: "now"}
	if err := writeGithubJSON(githubRepoSettingsPath("auto/onboarded"), settings); err != nil {
		t.Fatalf("write automatic settings: %v", err)
	}
	autoSettings, err := readGithubRepoSettings(githubRepoSettingsPath("auto/onboarded"))
	if err != nil {
		t.Fatalf("read automatic settings: %v", err)
	}
	if autoSettings.RepoMode != "" || autoSettings.IssuePickMode != "" || autoSettings.PRForwardMode != "" || autoSettings.ForkIssuesMode != "" || autoSettings.ImplementMode != "" || autoSettings.PublishTarget != "" {
		t.Fatalf("automatic settings should stay system default/manual when no manual onboard applied, got %+v", autoSettings)
	}
}
