package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
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
	oldRun := startRunRepoCyclesBatch
	type batchRun struct {
		repos   []string
		options startOptions
	}
	runs := []batchRun{}
	startRunRepoCyclesBatch = func(cwd string, repos []string, options startOptions) error {
		runs = append(runs, batchRun{repos: append([]string{}, repos...), options: options})
		return nil
	}
	defer func() {
		startRunRepoCyclesBatch = oldRun
	}()

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"--parallel", "2", "--max-open-prs", "7", "--", "--model", "gpt-5.4"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	if len(runs) != 1 || !reflect.DeepEqual(runs[0].repos, []string{"acme/enabled"}) {
		t.Fatalf("expected one enabled run, got %#v", runs)
	}
	if runs[0].options.RepoSlug != "" || runs[0].options.Parallel != 2 || runs[0].options.PerRepoWorkers != 2 || runs[0].options.MaxOpenPR != 7 || !reflect.DeepEqual(runs[0].options.CodexArgs, []string{"--model", "gpt-5.4"}) {
		t.Fatalf("unexpected run options: %#v", runs[0])
	}
	if strings.Contains(output, "acme/manual") {
		t.Fatalf("manual repo should not be selected, output=%q", output)
	}
}

func TestStartHelpShowsExplicitModes(t *testing.T) {
	for _, needle := range []string{
		"nana start - Run repo automation or scout startup",
		"Automation mode:",
		"Scout mode:",
		"Mode selection:",
		"automation mode runs onboarded GitHub repo automation",
		"scout mode runs policy-backed improvement/enhancement/ui scout startup",
		"nana start --once --repo owner/repo",
		"nana start --repo . --from-file proposals.json --once",
	} {
		if !strings.Contains(StartHelp, needle) {
			t.Fatalf("expected start help to contain %q:\n%s", needle, StartHelp)
		}
	}
}

func TestStartPrintsAutomationModeBannerBeforeRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeGithubJSON(githubRepoSettingsPath("acme/enabled"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	oldRun := startRunRepoCyclesBatch
	startRunRepoCyclesBatch = func(cwd string, repos []string, options startOptions) error { return nil }
	defer func() { startRunRepoCyclesBatch = oldRun }()

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"--once", "--no-ui"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	banner := "[start] Mode: automation (onboarded repo automation)."
	selected := "[start] Repos selected: acme/enabled"
	if !strings.Contains(output, banner) {
		t.Fatalf("expected automation mode banner, got %q", output)
	}
	selectedIndex := strings.Index(output, selected)
	if selectedIndex < 0 {
		t.Fatalf("expected repo execution output %q, got %q", selected, output)
	}
	if strings.Index(output, banner) > selectedIndex {
		t.Fatalf("expected mode banner before repo execution output, got %q", output)
	}
}

func TestStartPrintsScoutModeBannerBeforeRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir policy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	oldScout := startRunScoutStart
	startRunScoutStart = func(cwd string, options ImproveOptions) error {
		fmt.Fprintln(os.Stdout, "[test] scout execution started")
		return nil
	}
	defer func() { startRunScoutStart = oldScout }()

	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--once", "--from-file", "proposals.json"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	banner := "[start] Mode: scout (policy-backed scout startup)."
	runLine := "[test] scout execution started"
	if !strings.Contains(output, banner) {
		t.Fatalf("expected scout mode banner, got %q", output)
	}
	runIndex := strings.Index(output, runLine)
	if runIndex < 0 {
		t.Fatalf("expected scout execution output %q, got %q", runLine, output)
	}
	if strings.Index(output, banner) > runIndex {
		t.Fatalf("expected mode banner before scout execution output, got %q", output)
	}
}

func TestStartDoesNotAutoSelectScoutModeFromCwdPolicies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir policy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath("acme/enabled"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldRun := startRunRepoCyclesBatch
	oldScout := startRunScoutStart
	startRunRepoCyclesBatch = func(cwd string, repos []string, options startOptions) error {
		for _, repoSlug := range repos {
			fmt.Fprintf(os.Stdout, "[test] repo automation %s\n", repoSlug)
		}
		return nil
	}
	startRunScoutStart = func(cwd string, options ImproveOptions) error {
		fmt.Fprintln(os.Stdout, "[test] scout execution started")
		return nil
	}
	defer func() {
		startRunRepoCyclesBatch = oldRun
		startRunScoutStart = oldScout
	}()

	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--once", "--no-ui"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	if strings.Contains(output, "[start] Mode: scout") || strings.Contains(output, "[test] scout execution started") {
		t.Fatalf("expected bare start in cwd repo to stay in automation mode, got %q", output)
	}
	if !strings.Contains(output, "[start] Mode: automation (onboarded repo automation).") {
		t.Fatalf("expected automation mode banner, got %q", output)
	}
	if !strings.Contains(output, "[test] repo automation acme/enabled") {
		t.Fatalf("expected onboarded repo automation to run, got %q", output)
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

	oldRun := startRunRepoCyclesBatch
	events := []string{}
	startRunRepoCyclesBatch = func(cwd string, repos []string, options startOptions) error {
		for _, repoSlug := range repos {
			events = append(events, "repo:"+repoSlug)
		}
		if !reflect.DeepEqual(repos, []string{"acme/cycled"}) || options.Parallel != startDefaultGlobalParallel || options.PerRepoWorkers != startDefaultGlobalParallel {
			t.Fatalf("unexpected repo cycle options: repos=%#v options=%#v", repos, options)
		}
		return nil
	}
	defer func() {
		startRunRepoCyclesBatch = oldRun
	}()

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"--repo", "acme/cycled"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	expected := []string{"repo:acme/cycled"}
	if !reflect.DeepEqual(events, expected) {
		t.Fatalf("expected repo cycle dispatch, got %#v", events)
	}
}

func TestStartCyclesRepeatRepoAutomationCycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeGithubJSON(githubRepoSettingsPath("acme/repeat"), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "repo"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	oldRun := startRunRepoCyclesBatch
	runCount := 0
	startRunRepoCyclesBatch = func(cwd string, repos []string, options startOptions) error {
		runCount++
		return nil
	}
	defer func() {
		startRunRepoCyclesBatch = oldRun
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

func TestRunStartRepoCyclesSharedWorkersLimitsTotalConcurrency(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, repoSlug := range []string{"acme/one", "acme/two", "acme/three"} {
		if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "repo"}); err != nil {
			t.Fatalf("write settings for %s: %v", repoSlug, err)
		}
		if err := os.MkdirAll(githubManagedPaths(repoSlug).SourcePath, 0o755); err != nil {
			t.Fatalf("mkdir source path for %s: %v", repoSlug, err)
		}
	}
	oldSync := startSyncRepoState
	oldLaunch := startWorkRunGithubWork
	oldReconcile := startRunIssueReconcile
	defer func() {
		startSyncRepoState = oldSync
		startWorkRunGithubWork = oldLaunch
		startRunIssueReconcile = oldReconcile
	}()

	stateFor := func(repoSlug string, issueNumber int) *startWorkState {
		now := time.Now().UTC().Format(time.RFC3339)
		return &startWorkState{
			Version:    startWorkStateVersion,
			SourceRepo: repoSlug,
			ForkRepo:   "me/" + strings.TrimPrefix(repoSlug, "acme/"),
			UpdatedAt:  now,
			Issues: map[string]startWorkIssueState{
				fmt.Sprintf("%d", issueNumber): {
					SourceNumber:      issueNumber,
					ForkNumber:        issueNumber + 100,
					SourceURL:         fmt.Sprintf("https://github.com/%s/issues/%d", repoSlug, issueNumber),
					ForkURL:           fmt.Sprintf("https://github.com/me/%s/issues/%d", strings.TrimPrefix(repoSlug, "acme/"), issueNumber+100),
					State:             "open",
					Status:            startWorkStatusQueued,
					Labels:            []string{"nana"},
					SourceFingerprint: fmt.Sprintf("fp-%s-%d", repoSlug, issueNumber),
					Priority:          2,
					PrioritySource:    "manual_label",
					UpdatedAt:         now,
				},
			},
			ServiceTasks: map[string]startWorkServiceTask{},
			PlannedItems: map[string]startWorkPlannedItem{},
		}
	}
	states := map[string]*startWorkState{
		"acme/one":   stateFor("acme/one", 1),
		"acme/two":   stateFor("acme/two", 2),
		"acme/three": stateFor("acme/three", 3),
	}
	startSyncRepoState = func(options startWorkOptions) (startWorkOptions, *startWorkState, int, bool, error) {
		state := *states[options.RepoSlug]
		state.Issues = mapsCloneStartWorkIssues(states[options.RepoSlug].Issues)
		state.ServiceTasks = mapsCloneStartWorkServiceTasks(states[options.RepoSlug].ServiceTasks)
		state.PlannedItems = mapsCloneStartWorkPlannedItems(states[options.RepoSlug].PlannedItems)
		return options, &state, 0, false, nil
	}
	gate := make(chan struct{})
	entered := make(chan string, 3)
	var mu sync.Mutex
	current := 0
	maxSeen := 0
	startWorkRunGithubWork = func(issueURL string, publishTarget string, codexArgs []string) (startWorkLaunchResult, error) {
		mu.Lock()
		current++
		if current > maxSeen {
			maxSeen = current
		}
		mu.Unlock()
		entered <- issueURL
		<-gate
		mu.Lock()
		current--
		mu.Unlock()
		return startWorkLaunchResult{RunID: issueURL}, nil
	}
	startRunIssueReconcile = func(repoSlug string, publishTarget string, issue startWorkIssueState) (startWorkReconcileResult, error) {
		return startWorkReconcileResult{Status: startWorkStatusCompleted, RunID: issue.SourceURL}, nil
	}

	done := make(chan error, 1)
	go func() {
		done <- runStartRepoCyclesSharedWorkers(".", []string{"acme/one", "acme/two", "acme/three"}, startOptions{
			Parallel:       2,
			PerRepoWorkers: 2,
			MaxOpenPR:      startWorkDefaultOpenPRCap,
		})
	}()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case issueURL := <-entered:
			seen[issueURL] = true
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for active tasks")
		}
	}
	select {
	case issueURL := <-entered:
		t.Fatalf("expected third task to wait for a shared worker slot, got %s", issueURL)
	case <-time.After(100 * time.Millisecond):
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if maxSeen != 2 {
		t.Fatalf("expected max global concurrency 2, got %d", maxSeen)
	}
}

func TestStartBareLocalScoutPoliciesLoopsForeverUntilStopped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

func TestStartForeverContinuesAfterCycleError(t *testing.T) {
	oldSleep := startLoopSleep
	oldContinue := startLoopContinue
	runCount := 0
	startLoopSleep = func(duration time.Duration) {}
	startLoopContinue = func() bool { return runCount < 2 }
	defer func() {
		startLoopSleep = oldSleep
		startLoopContinue = oldContinue
	}()

	output, err := captureStdout(t, func() error {
		return runStartLoop(startRuntimeOptions{Forever: true, Interval: time.Second}, func() error {
			runCount++
			if runCount == 1 {
				return fmt.Errorf("temporary failure")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("runStartLoop: %v", err)
	}
	if runCount != 2 {
		t.Fatalf("expected retry after cycle error, got %d run(s)", runCount)
	}
	if !strings.Contains(output, "Cycle 1 failed: temporary failure") || !strings.Contains(output, "Cycle 2/forever") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunStartLoopSleepsOnlyRemainingInterval(t *testing.T) {
	oldNow := startLoopNow
	oldSleep := startLoopSleep
	oldContinue := startLoopContinue
	runCount := 0
	sleepDurations := []time.Duration{}
	times := []time.Time{
		time.Unix(0, 0),
		time.Unix(0, int64(400*time.Millisecond)),
		time.Unix(1, 0),
		time.Unix(1, int64(200*time.Millisecond)),
	}
	startLoopNow = func() time.Time {
		if len(times) == 0 {
			t.Fatal("startLoopNow called more times than expected")
		}
		current := times[0]
		times = times[1:]
		return current
	}
	startLoopSleep = func(duration time.Duration) {
		sleepDurations = append(sleepDurations, duration)
	}
	startLoopContinue = func() bool { return runCount < 2 }
	defer func() {
		startLoopNow = oldNow
		startLoopSleep = oldSleep
		startLoopContinue = oldContinue
	}()

	if err := runStartLoop(startRuntimeOptions{Forever: true, Interval: time.Second}, func() error {
		runCount++
		return nil
	}); err != nil {
		t.Fatalf("runStartLoop: %v", err)
	}
	if !reflect.DeepEqual(sleepDurations, []time.Duration{600 * time.Millisecond}) {
		t.Fatalf("unexpected sleep durations: %+v", sleepDurations)
	}
}

func TestRunStartLoopSkipsSleepWhenCycleExceedsInterval(t *testing.T) {
	oldNow := startLoopNow
	oldSleep := startLoopSleep
	oldContinue := startLoopContinue
	runCount := 0
	sleepDurations := []time.Duration{}
	times := []time.Time{
		time.Unix(0, 0),
		time.Unix(0, int64(1500*time.Millisecond)),
		time.Unix(2, 0),
		time.Unix(2, int64(1500*time.Millisecond)),
	}
	startLoopNow = func() time.Time {
		if len(times) == 0 {
			t.Fatal("startLoopNow called more times than expected")
		}
		current := times[0]
		times = times[1:]
		return current
	}
	startLoopSleep = func(duration time.Duration) {
		sleepDurations = append(sleepDurations, duration)
	}
	startLoopContinue = func() bool { return runCount < 2 }
	defer func() {
		startLoopNow = oldNow
		startLoopSleep = oldSleep
		startLoopContinue = oldContinue
	}()

	if err := runStartLoop(startRuntimeOptions{Forever: true, Interval: time.Second}, func() error {
		runCount++
		return nil
	}); err != nil {
		t.Fatalf("runStartLoop: %v", err)
	}
	if len(sleepDurations) != 0 {
		t.Fatalf("expected no sleep for overlong cycles, got %+v", sleepDurations)
	}
}

func TestStartLaunchesUIByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeGithubJSON(githubRepoSettingsPath("acme/enabled"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	oldRun := startRunRepoCyclesBatch
	startRunRepoCyclesBatch = func(cwd string, repos []string, options startOptions) error { return nil }
	defer func() { startRunRepoCyclesBatch = oldRun }()

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"--once"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	if !strings.Contains(output, "[start-ui] API: http://127.0.0.1:") || !strings.Contains(output, "[start-ui] Web: http://127.0.0.1:") {
		t.Fatalf("expected UI URLs in output, got %q", output)
	}
	var runtime startUIRuntimeState
	if err := readGithubJSON(filepath.Join(home, ".nana", "start", "ui", "runtime.json"), &runtime); err != nil {
		t.Fatalf("read runtime.json: %v", err)
	}
	if runtime.APIURL == "" || runtime.WebURL == "" || runtime.StoppedAt == "" {
		t.Fatalf("unexpected UI runtime state: %+v", runtime)
	}
}

func TestStartNoUISkipsUISupervisor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeGithubJSON(githubRepoSettingsPath("acme/enabled"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	oldRun := startRunRepoCyclesBatch
	startRunRepoCyclesBatch = func(cwd string, repos []string, options startOptions) error { return nil }
	defer func() { startRunRepoCyclesBatch = oldRun }()

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"--once", "--no-ui"})
	})
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, output)
	}
	if strings.Contains(output, "[start-ui] API:") {
		t.Fatalf("did not expect UI output, got %q", output)
	}
	if _, err := os.Stat(filepath.Join(home, ".nana", "start", "ui", "runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no runtime.json, got err=%v", err)
	}
}

func TestStartUIOperatorGuideDocsAndHelp(t *testing.T) {
	if !strings.Contains(StartHelp, "docs/start-ui.html") {
		t.Fatalf("expected start help to link the Start UI guide")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	expected := map[string][]string{
		"docs/start-ui.html": {
			"Nana Assistant Workspace",
			"127.0.0.1",
			"nana start",
			"--no-ui",
			"/api/v1/overview",
			"Issues and investigations",
			"Feedback and approvals",
		},
		"docs/work.md": {
			"Start UI guide",
			"./start-ui.html",
			"--ui-api-port",
			"--ui-web-port",
		},
		"docs/getting-started.html": {
			"Repo Automation Console",
			"./start-ui.html",
			"[start-ui] Web",
		},
		"docs/index.html": {
			"./start-ui.html",
			"Start UI",
		},
		"docs/agents.html": {
			"./start-ui.html",
			"Start UI",
		},
		"docs/skills.html": {
			"./start-ui.html",
			"Start UI",
		},
		"docs/integrations.html": {
			"./start-ui.html",
			"Start UI",
		},
	}
	for rel, needles := range expected {
		content, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(content)
		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				t.Fatalf("expected %s to contain %q", rel, needle)
			}
		}
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

func TestRepoConfigDisabledObservationMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	output, err := captureStdout(t, func() error {
		return Repo(".", []string{"config", "acme/observe", "--repo-mode", "disabled"})
	})
	if err != nil {
		t.Fatalf("Repo(config disabled): %v\n%s", err, output)
	}
	settings, err := readGithubRepoSettings(githubRepoSettingsPath("acme/observe"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if settings.RepoMode != "disabled" || settings.IssuePickMode != "manual" || settings.PublishTarget != "" {
		t.Fatalf("unexpected disabled settings: %+v", settings)
	}
	repos, err := resolveStartRepos("acme/observe")
	if err != nil {
		t.Fatalf("resolveStartRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("expected disabled repo to be skipped by start, got %#v", repos)
	}
	explain, err := captureStdout(t, func() error { return Repo(".", []string{"explain", "acme/observe"}) })
	if err != nil {
		t.Fatalf("Repo(explain disabled): %v", err)
	}
	for _, needle := range []string{
		"repo-mode: disabled",
		"issue-pick: manual",
		"publish: (none)",
		"Start participation: false",
		"observation only",
	} {
		if !strings.Contains(explain, needle) {
			t.Fatalf("expected explain to contain %q, got %q", needle, explain)
		}
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

func mapsCloneStartWorkIssues(source map[string]startWorkIssueState) map[string]startWorkIssueState {
	cloned := make(map[string]startWorkIssueState, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func mapsCloneStartWorkServiceTasks(source map[string]startWorkServiceTask) map[string]startWorkServiceTask {
	cloned := make(map[string]startWorkServiceTask, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func mapsCloneStartWorkPlannedItems(source map[string]startWorkPlannedItem) map[string]startWorkPlannedItem {
	cloned := make(map[string]startWorkPlannedItem, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
