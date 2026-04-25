package gocli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStartWorkStartCreatesForkCopiesIssuesPrioritizesAndStartsWithinCaps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	started := []string{}
	var startedMu sync.Mutex
	oldRunner := startWorkRunGithubWork
	startWorkRunGithubWork = func(issueURL string, publishTarget string, codexArgs []string) (startWorkLaunchResult, error) {
		startedMu.Lock()
		defer startedMu.Unlock()
		started = append(started, issueURL+"|"+publishTarget)
		if !reflect.DeepEqual(codexArgs, []string{"--model", "gpt-5.4"}) {
			t.Fatalf("unexpected codex args: %#v", codexArgs)
		}
		return startWorkLaunchResult{}, nil
	}
	defer func() { startWorkRunGithubWork = oldRunner }()

	createdByTitle := map[string]int{"Hard P2": 101, "Easy P1": 102}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /user?":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case "GET /repos/acme/widget?":
			_, _ = w.Write([]byte(`{"name":"widget","full_name":"acme/widget","default_branch":"main","clone_url":"https://example.invalid/acme/widget.git","html_url":"https://github.com/acme/widget"}`))
		case "GET /repos/me/widget?":
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		case "POST /repos/acme/widget/forks?":
			_, _ = w.Write([]byte(`{"name":"widget","full_name":"me/widget","default_branch":"main","clone_url":"https://example.invalid/me/widget.git","html_url":"https://github.com/me/widget"}`))
		case "PATCH /repos/me/widget?":
			_, _ = w.Write([]byte(`{"full_name":"me/widget","has_issues":true}`))
		case "PUT /repos/me/widget/actions/permissions?":
			_, _ = w.Write([]byte(`{}`))
		case "GET /repos/acme/widget/issues?state=all&per_page=100&page=1":
			_, _ = w.Write([]byte(`[
{"number":1,"title":"Hard P2","body":"hard body","state":"open","html_url":"https://github.com/acme/widget/issues/1","labels":[{"name":"P2"},{"name":"hard"},{"name":"refactor"}]},
{"number":2,"title":"Easy P1","body":"easy body","state":"open","html_url":"https://github.com/acme/widget/issues/2","labels":[{"name":"P1"},{"name":"easy"},{"name":"bug"}]},
{"number":3,"title":"PR masquerading as issue","state":"open","html_url":"https://github.com/acme/widget/issues/3","labels":[{"name":"P1"}],"pull_request":{}}
]`))
		case "POST /repos/me/widget/issues?":
			var payload struct {
				Title  string   `json:"title"`
				Body   string   `json:"body"`
				Labels []string `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create issue payload: %v", err)
			}
			number := createdByTitle[payload.Title]
			if number == 0 {
				t.Fatalf("unexpected copied issue title %q", payload.Title)
			}
			if !strings.Contains(payload.Body, "Copied from https://github.com/acme/widget/issues/") {
				t.Fatalf("missing source marker in body %q", payload.Body)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"number":%d,"title":%q,"state":"open","html_url":"https://github.com/me/widget/issues/%d"}`, number, payload.Title, number)))
		case "GET /repos/me/widget/pulls?state=open&per_page=100&page=1":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		return StartWork(".", []string{"start", "acme/widget", "--parallel", "2", "--max-open-prs", "10", "--publish", "fork", "--", "--model", "gpt-5.4"})
	})
	if err != nil {
		t.Fatalf("StartWork(start): %v\n%s", err, output)
	}
	state, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.ForkRepo != "me/widget" || len(state.Issues) != 2 {
		t.Fatalf("unexpected state fork/issues: %+v", state)
	}
	if got := state.LastRun.StartedIssueNumbers; !reflect.DeepEqual(got, []int{2, 1}) {
		t.Fatalf("expected priority order [2 1], got %#v", got)
	}
	if state.Issues["2"].Priority != 1 || state.Issues["2"].Complexity != 2 || state.Issues["2"].Status != startWorkStatusInProgress {
		t.Fatalf("unexpected issue 2 state: %+v", state.Issues["2"])
	}
	if state.Preferences.Artifacts["source_settings"] == "" || state.Preferences.Artifacts["fork_settings"] == "" {
		t.Fatalf("expected persisted preference artifact paths, got %+v", state.Preferences)
	}
	startedMu.Lock()
	defer startedMu.Unlock()
	if len(started) != 2 || !slicesContains(started, "https://github.com/me/widget/issues/102|fork") || !slicesContains(started, "https://github.com/me/widget/issues/101|fork") {
		t.Fatalf("unexpected started issue URLs: %#v", started)
	}
}

func TestStartWorkStartHonorsOpenPRCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	oldRunner := startWorkRunGithubWork
	runnerCalls := 0
	startWorkRunGithubWork = func(issueURL string, publishTarget string, codexArgs []string) (startWorkLaunchResult, error) {
		runnerCalls++
		return startWorkLaunchResult{}, nil
	}
	defer func() { startWorkRunGithubWork = oldRunner }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /user?":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case "GET /repos/acme/widget?", "GET /repos/me/widget?":
			_, _ = w.Write([]byte(`{"name":"widget","full_name":"me/widget","default_branch":"main"}`))
		case "PATCH /repos/me/widget?":
			_, _ = w.Write([]byte(`{"full_name":"me/widget","has_issues":true}`))
		case "PUT /repos/me/widget/actions/permissions?":
			_, _ = w.Write([]byte(`{}`))
		case "GET /repos/acme/widget/issues?state=all&per_page=100&page=1":
			_, _ = w.Write([]byte(`[{"number":1,"title":"P1","state":"open","html_url":"https://github.com/acme/widget/issues/1","labels":[{"name":"P1"}]}]`))
		case "POST /repos/me/widget/issues?":
			_, _ = w.Write([]byte(`{"number":101,"title":"P1","state":"open","html_url":"https://github.com/me/widget/issues/101"}`))
		case "GET /repos/me/widget/pulls?state=open&per_page=100&page=1":
			pulls := make([]map[string]any, 10)
			for i := range pulls {
				pulls[i] = map[string]any{"number": i + 1}
			}
			_ = json.NewEncoder(w).Encode(pulls)
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	if err := StartWork(".", []string{"start", "acme/widget", "--max-open-prs", "10"}); err != nil {
		t.Fatalf("StartWork(start cap): %v", err)
	}
	if runnerCalls != 0 {
		t.Fatalf("expected no worker starts when cap reached, got %d", runnerCalls)
	}
	state, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.LastRun == nil || !strings.Contains(state.LastRun.SkippedReason, "open fork PR cap reached") {
		t.Fatalf("expected cap skip reason, got %+v", state.LastRun)
	}
}

func TestStartWorkLabelModeTreatsScoutProposalsAsAutomationOptIn(t *testing.T) {
	for _, role := range []string{improvementScoutRole, enhancementScoutRole, uiScoutRole} {
		if !startWorkAutomationAllowsIssue("labeled", []string{role}, "implement") {
			t.Fatalf("expected %s label to opt into labeled implementation", role)
		}
	}
	if startWorkAutomationAllowsIssue("labeled", []string{"enhancement"}, "implement") {
		t.Fatalf("generic enhancement label should not opt into labeled implementation")
	}
}

func TestStartWorkBuildImplementationQueueRequiresFreshPriorityAndHonorsManualP0(t *testing.T) {
	state := &startWorkState{
		Issues: map[string]startWorkIssueState{
			"1": {
				SourceNumber:   1,
				ForkNumber:     101,
				State:          "open",
				Status:         startWorkStatusQueued,
				Labels:         []string{"nana", "P0"},
				WorkType:       workTypeBugFix,
				Priority:       0,
				PrioritySource: "manual_label",
				Complexity:     3,
			},
			"2": {
				SourceNumber:      2,
				ForkNumber:        102,
				State:             "open",
				Status:            startWorkStatusQueued,
				Labels:            []string{"nana"},
				WorkType:          workTypeFeature,
				Priority:          2,
				PrioritySource:    "triage",
				Complexity:        2,
				SourceFingerprint: "fp-2",
				TriageFingerprint: "fp-2",
				TriageStatus:      startWorkTriageCompleted,
			},
			"3": {
				SourceNumber:      3,
				ForkNumber:        103,
				State:             "open",
				Status:            startWorkStatusQueued,
				Labels:            []string{"nana"},
				WorkType:          workTypeFeature,
				Priority:          1,
				PrioritySource:    "triage",
				Complexity:        1,
				SourceFingerprint: "fp-3-new",
				TriageFingerprint: "fp-3-old",
				TriageStatus:      startWorkTriageCompleted,
			},
		},
	}
	queue, skipped := startWorkBuildImplementationQueue(state, startWorkOptions{ImplementMode: "labeled", MaxOpenPR: 10}, 0)
	if skipped != "" {
		t.Fatalf("unexpected skipped reason: %s", skipped)
	}
	if got := []int{queue[0].SourceNumber, queue[1].SourceNumber}; !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("expected manual P0 then fresh triage order, got %#v", got)
	}
}

func TestStartWorkBuildImplementationQueueSkipsFutureScheduledIssues(t *testing.T) {
	state := &startWorkState{
		Issues: map[string]startWorkIssueState{
			"1": {
				SourceNumber:      1,
				ForkNumber:        101,
				State:             "open",
				Status:            startWorkStatusQueued,
				Labels:            []string{"nana"},
				WorkType:          workTypeFeature,
				Priority:          1,
				PrioritySource:    "triage",
				Complexity:        1,
				SourceFingerprint: "fp-1",
				TriageFingerprint: "fp-1",
				TriageStatus:      startWorkTriageCompleted,
				ScheduleAt:        time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			},
			"2": {
				SourceNumber:      2,
				ForkNumber:        102,
				State:             "open",
				Status:            startWorkStatusQueued,
				Labels:            []string{"nana"},
				WorkType:          workTypeFeature,
				Priority:          2,
				PrioritySource:    "triage",
				Complexity:        2,
				SourceFingerprint: "fp-2",
				TriageFingerprint: "fp-2",
				TriageStatus:      startWorkTriageCompleted,
			},
		},
	}
	queue, skipped := startWorkBuildImplementationQueue(state, startWorkOptions{ImplementMode: "labeled", MaxOpenPR: 10}, 0)
	if skipped != "" {
		t.Fatalf("unexpected skipped reason: %s", skipped)
	}
	if len(queue) != 1 || queue[0].SourceNumber != 2 {
		t.Fatalf("expected only unscheduled issue to be ready, got %+v", queue)
	}
}

func TestStartRepoCoordinatorBuildsSeparateServiceQueueWithDependencies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		workOptions: startWorkOptions{
			ImplementMode: "labeled",
		},
		state: &startWorkState{
			SourceRepo: "acme/widget",
			Issues: map[string]startWorkIssueState{
				"1": {
					SourceNumber:      1,
					ForkNumber:        101,
					State:             "open",
					Status:            startWorkStatusQueued,
					Labels:            []string{"nana"},
					SourceFingerprint: "fp-1",
					TriageStatus:      startWorkTriageQueued,
				},
				"2": {
					SourceNumber:      2,
					ForkNumber:        102,
					State:             "open",
					Status:            startWorkStatusQueued,
					Labels:            []string{"nana", "P1"},
					Priority:          1,
					PrioritySource:    "manual_label",
					SourceFingerprint: "fp-2",
					TriageStatus:      startWorkTriageCompleted,
				},
			},
			ServiceTasks: map[string]startWorkServiceTask{},
		},
		running:    map[string]startRepoTask{},
		scoutRoles: []string{improvementScoutRole},
	}
	if err := coordinator.syncServiceTasks(); err != nil {
		t.Fatalf("syncServiceTasks: %v", err)
	}
	queue := coordinator.buildServiceQueue()
	if len(queue) != 1 {
		t.Fatalf("expected only ready scout task before dependencies clear, got %#v", queue)
	}
	if queue[0].Kind != startTaskKindScout {
		t.Fatalf("unexpected ready service queue ordering: %#v", queue)
	}
	syncTask := coordinator.state.ServiceTasks[startServiceTaskKey(startTaskKindIssueSync, coordinator.cycleID)]
	if syncTask.Kind != startTaskKindIssueSync || len(syncTask.DependencyKeys) != 1 || syncTask.DependencyKeys[0] != startServiceTaskKey(startTaskKindScout, improvementScoutRole) {
		t.Fatalf("unexpected issue-sync task: %+v", syncTask)
	}
	triageTask := coordinator.state.ServiceTasks[startServiceTaskKey(startTaskKindTriage, "1")]
	if len(triageTask.DependencyKeys) != 1 || triageTask.DependencyKeys[0] != syncTask.ID {
		t.Fatalf("expected triage task to depend on issue sync, got %+v", triageTask)
	}
}

func TestStartRepoCoordinatorQueuesDuePlannedLaunchTask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		workOptions: startWorkOptions{
			ImplementMode: "labeled",
		},
		state: &startWorkState{
			SourceRepo:   "acme/widget",
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			PlannedItems: map[string]startWorkPlannedItem{
				"planned-1": {
					ID:         "planned-1",
					RepoSlug:   "acme/widget",
					Title:      "Nightly cleanup",
					Priority:   2,
					ScheduleAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
					State:      startPlannedItemQueued,
					CreatedAt:  time.Now().UTC().Format(time.RFC3339),
					UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
				},
			},
		},
	}
	if err := coordinator.syncServiceTasks(); err != nil {
		t.Fatalf("syncServiceTasks: %v", err)
	}
	queue := coordinator.buildServiceQueue()
	if len(queue) != 1 || queue[0].Kind != startTaskKindPlannedLaunch || queue[0].PlannedItemID != "planned-1" {
		t.Fatalf("expected planned launch task, got %#v", queue)
	}
}

func TestStartRepoCoordinatorPrioritizesHigherPriorityPlannedLaunchTask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		workOptions: startWorkOptions{
			ImplementMode: "labeled",
		},
		state: &startWorkState{
			SourceRepo:   "acme/widget",
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			PlannedItems: map[string]startWorkPlannedItem{
				"planned-low": {
					ID:         "planned-low",
					RepoSlug:   "acme/widget",
					Title:      "Low priority task",
					Priority:   4,
					ScheduleAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
					State:      startPlannedItemQueued,
					CreatedAt:  time.Now().UTC().Format(time.RFC3339),
					UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
				},
				"planned-high": {
					ID:         "planned-high",
					RepoSlug:   "acme/widget",
					Title:      "High priority task",
					Priority:   1,
					ScheduleAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
					State:      startPlannedItemQueued,
					CreatedAt:  time.Now().UTC().Format(time.RFC3339),
					UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
				},
			},
		},
	}
	if err := coordinator.syncServiceTasks(); err != nil {
		t.Fatalf("syncServiceTasks: %v", err)
	}
	queue := coordinator.buildServiceQueue()
	if len(queue) != 2 {
		t.Fatalf("expected both planned launch tasks, got %#v", queue)
	}
	if queue[0].PlannedItemID != "planned-high" || queue[1].PlannedItemID != "planned-low" {
		t.Fatalf("expected higher priority planned item first, got %#v", queue)
	}
}

func TestStartRepoCoordinatorQueuesImmediatePlannedLaunchTaskWithoutSchedule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	coordinator := &startRepoCoordinator{
		repoSlug: repoSlug,
		cycleID:  "cycle-1",
		workOptions: startWorkOptions{
			ImplementMode: "labeled",
		},
		state: &startWorkState{
			Version:    startWorkStateVersion,
			SourceRepo: repoSlug,
			UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
			Issues:     map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{
				startServiceTaskKey(startTaskKindPlannedLaunch, "planned-1"): {
					ID:            startServiceTaskKey(startTaskKindPlannedLaunch, "planned-1"),
					Kind:          startTaskKindPlannedLaunch,
					Queue:         startTaskQueueService,
					Status:        startWorkServiceTaskQueued,
					PlannedItemID: "planned-1",
				},
			},
			PlannedItems: map[string]startWorkPlannedItem{
				"planned-1": {
					ID:        "planned-1",
					RepoSlug:  repoSlug,
					Title:     "Manual launch only",
					Priority:  2,
					State:     startPlannedItemQueued,
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					UpdatedAt: time.Now().UTC().Format(time.RFC3339),
				},
			},
		},
	}
	if err := coordinator.syncServiceTasks(); err != nil {
		t.Fatalf("syncServiceTasks: %v", err)
	}
	queue := coordinator.buildServiceQueue()
	if len(queue) != 1 || queue[0].Kind != startTaskKindPlannedLaunch || queue[0].PlannedItemID != "planned-1" {
		t.Fatalf("expected immediate planned launch task, got %+v", queue)
	}
	if _, ok := coordinator.state.ServiceTasks[startServiceTaskKey(startTaskKindPlannedLaunch, "planned-1")]; ok {
		t.Fatalf("expected legacy planned-launch task to be removed, got %+v", coordinator.state.ServiceTasks)
	}
}

func TestStartWorkPlannedItemDueUsesExecuteAfterSemantics(t *testing.T) {
	now := time.Now().UTC()
	if !startWorkPlannedItemDue(startWorkPlannedItem{State: startPlannedItemQueued}, now) {
		t.Fatal("expected task with empty execute-after to be due immediately")
	}
	if startWorkPlannedItemDue(startWorkPlannedItem{State: startPlannedItemQueued, ScheduleAt: "not-a-time"}, now) {
		t.Fatal("expected invalid schedule to avoid auto-launch")
	}
	if !startWorkPlannedItemDue(startWorkPlannedItem{
		State:      startPlannedItemQueued,
		ScheduleAt: now.Add(-time.Minute).Format(time.RFC3339),
	}, now) {
		t.Fatal("expected past scheduled item to be due")
	}
}

func TestStartRepoCoordinatorKeepsManualLaunchingPlannedItemsQueued(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoSlug := "acme/widget"
	coordinator := &startRepoCoordinator{
		repoSlug: repoSlug,
		cycleID:  "cycle-1",
		state: &startWorkState{
			Version:      startWorkStateVersion,
			SourceRepo:   repoSlug,
			UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			PlannedItems: map[string]startWorkPlannedItem{
				"planned-1": {
					ID:        "planned-1",
					RepoSlug:  repoSlug,
					Title:     "Manual launch only",
					Priority:  2,
					State:     startPlannedItemLaunching,
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					UpdatedAt: time.Now().UTC().Format(time.RFC3339),
				},
			},
		},
	}
	if err := coordinator.syncServiceTasks(); err != nil {
		t.Fatalf("syncServiceTasks: %v", err)
	}
	if _, ok := coordinator.state.ServiceTasks[startServiceTaskKey(startTaskKindPlannedLaunch, "planned-1")]; ok {
		t.Fatalf("expected launching task recovery to avoid planned-launch service tasks, got %+v", coordinator.state.ServiceTasks)
	}
	queue := coordinator.buildServiceQueue()
	if len(queue) != 1 || queue[0].Key != startServiceTaskKey(startTaskKindPlannedLaunch, "planned-1") {
		t.Fatalf("expected launching task recovery to requeue the planned item directly, got %+v", queue)
	}
}

func TestStartRepoCoordinatorMarkTaskStartedMarksPlannedLaunchRunning(t *testing.T) {
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		state: &startWorkState{
			Version:      startWorkStateVersion,
			SourceRepo:   "acme/widget",
			UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			PlannedItems: map[string]startWorkPlannedItem{
				"planned-1": {
					ID:        "planned-1",
					RepoSlug:  "acme/widget",
					Title:     "Manual launch only",
					Priority:  2,
					State:     startPlannedItemLaunching,
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
					UpdatedAt: time.Now().UTC().Format(time.RFC3339),
				},
			},
		},
	}
	task := startRepoTask{
		Key:           startServiceTaskKey(startTaskKindPlannedLaunch, "planned-1"),
		Kind:          startTaskKindPlannedLaunch,
		Queue:         startTaskQueueService,
		PlannedItemID: "planned-1",
	}
	if err := coordinator.markTaskStarted(task); err != nil {
		t.Fatalf("markTaskStarted: %v", err)
	}
	item := coordinator.state.PlannedItems["planned-1"]
	if item.State != startPlannedItemLaunching || item.ScheduleAt != "" || item.DeferredReason != "" {
		t.Fatalf("expected planned item to be marked launching, got %+v", item)
	}
}

func TestStartRepoCoordinatorMarkTaskStartedMarksScoutJobRunning(t *testing.T) {
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		state: &startWorkState{
			Version:      startWorkStateVersion,
			SourceRepo:   "acme/widget",
			UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			ScoutJobs: map[string]startWorkScoutJob{
				"scout-1": {
					ID:          "scout-1",
					Role:        improvementScoutRole,
					Title:       "Improve help text",
					Summary:     "Make help clearer",
					Destination: improvementDestinationLocal,
					TaskBody:    "Implement local scout proposal: Improve help text",
					Status:      startScoutJobQueued,
					CreatedAt:   time.Now().UTC().Format(time.RFC3339),
					UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
				},
			},
		},
	}
	task := startRepoTask{
		Key:        startServiceTaskKey(startTaskKindScoutJob, "scout-1"),
		Kind:       startTaskKindScoutJob,
		Queue:      startTaskQueueImplementation,
		ScoutJobID: "scout-1",
	}
	if err := coordinator.markTaskStarted(task); err != nil {
		t.Fatalf("markTaskStarted: %v", err)
	}
	job := coordinator.state.ScoutJobs["scout-1"]
	if job.Status != startScoutJobRunning || job.Attempts != 1 {
		t.Fatalf("expected scout job to be marked running, got %+v", job)
	}
}

func TestStartRepoCoordinatorApplyTaskResultKeepsScoutJobRunningAfterLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		state: &startWorkState{
			Version:      startWorkStateVersion,
			SourceRepo:   "acme/widget",
			UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			ScoutJobs: map[string]startWorkScoutJob{
				"scout-1": {
					ID:          "scout-1",
					Role:        improvementScoutRole,
					Title:       "Improve help text",
					Summary:     "Make help clearer",
					Destination: improvementDestinationLocal,
					TaskBody:    "Implement local scout proposal: Improve help text",
					Status:      startScoutJobRunning,
					CreatedAt:   time.Now().UTC().Format(time.RFC3339),
					UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
				},
			},
		},
	}
	if err := writeStartWorkState(*coordinator.state); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	if err := coordinator.applyTaskResult(startRepoTaskResult{
		Task: startRepoTask{
			Key:        startServiceTaskKey(startTaskKindScoutJob, "scout-1"),
			Kind:       startTaskKindScoutJob,
			Queue:      startTaskQueueImplementation,
			ScoutJobID: "scout-1",
		},
		Launch: &startWorkLaunchResult{RunID: "lw-scout-1"},
	}); err != nil {
		t.Fatalf("applyTaskResult: %v", err)
	}

	state, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	job := state.ScoutJobs["scout-1"]
	if job.Status != startScoutJobRunning || job.RunID != "lw-scout-1" {
		t.Fatalf("expected scout job to stay running with run id, got %+v", job)
	}
}

func TestStartRepoCoordinatorPersistsStateWhenNoTasksAreQueued(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		state: &startWorkState{
			Version:      startWorkStateVersion,
			SourceRepo:   "acme/widget",
			UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			PlannedItems: map[string]startWorkPlannedItem{},
		},
	}
	if err := coordinator.syncServiceTasks(); err != nil {
		t.Fatalf("syncServiceTasks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".nana", "work", "repos", "acme", "widget", "start-state.json")); err != nil {
		t.Fatalf("expected persisted start state: %v", err)
	}
}

func TestReadStartWorkStatePreservesRunningServiceTasks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: "acme/widget",
		UpdatedAt:  "now",
		Issues:     map[string]startWorkIssueState{},
		ServiceTasks: map[string]startWorkServiceTask{
			"triage:1": {
				ID:     "triage:1",
				Kind:   startTaskKindTriage,
				Queue:  startTaskQueueService,
				Status: startWorkServiceTaskRunning,
			},
		},
	}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	loaded, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if loaded.ServiceTasks["triage:1"].Status != startWorkServiceTaskRunning {
		t.Fatalf("expected running service task to remain running on read, got %+v", loaded.ServiceTasks["triage:1"])
	}
}

func TestStartRepoCoordinatorQueuedWaitTaskRunsAgainNextCycleAfterReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: "acme/widget",
		UpdatedAt:  "now",
		Issues: map[string]startWorkIssueState{
			"1": {SourceNumber: 1, Status: startWorkStatusReconciling, LastRunID: "gh-1"},
		},
		ServiceTasks: map[string]startWorkServiceTask{
			"reconcile:1": {
				ID:            "reconcile:1",
				Kind:          startTaskKindReconcile,
				Queue:         startTaskQueueService,
				Status:        startWorkServiceTaskQueued,
				IssueKey:      "1",
				RunID:         "gh-1",
				ResultSummary: "waiting",
				LastError:     "waiting for publication state",
				WaitCycle:     "cycle-1",
			},
		},
	}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	loaded, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-2",
		state:    loaded,
		running:  map[string]startRepoTask{},
	}
	queue := coordinator.buildServiceQueue()
	if len(queue) != 1 || queue[0].Key != "reconcile:1" {
		t.Fatalf("expected deferred task to be eligible next cycle, got %+v", queue)
	}
}

func TestReadStartWorkStatePreservesRunningTriageWhenServiceTaskRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	state := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: "acme/widget",
		UpdatedAt:  "now",
		Issues: map[string]startWorkIssueState{
			"1": {
				SourceNumber: 1,
				TriageStatus: startWorkTriageRunning,
			},
		},
		ServiceTasks: map[string]startWorkServiceTask{
			"triage:1": {
				ID:       "triage:1",
				Kind:     startTaskKindTriage,
				Queue:    startTaskQueueService,
				Status:   startWorkServiceTaskRunning,
				IssueKey: "1",
			},
		},
	}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}

	loaded, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if loaded.Issues["1"].TriageStatus != startWorkTriageRunning {
		t.Fatalf("expected running triage to remain running while service task is running, got %+v", loaded.Issues["1"])
	}
}

func TestStartRepoCoordinatorQueuesReconcileForStaleInProgressIssue(t *testing.T) {
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		workOptions: startWorkOptions{
			ImplementMode: "labeled",
		},
		state: &startWorkState{
			SourceRepo: "acme/widget",
			Issues: map[string]startWorkIssueState{
				"1": {
					SourceNumber: 1,
					ForkNumber:   101,
					State:        "open",
					Status:       startWorkStatusInProgress,
					Labels:       []string{"nana", "P1"},
					LastRunID:    "gh-1",
				},
			},
			ServiceTasks: map[string]startWorkServiceTask{},
		},
		running: map[string]startRepoTask{},
	}
	if err := coordinator.syncServiceTasks(); err != nil {
		t.Fatalf("syncServiceTasks: %v", err)
	}
	task, ok := coordinator.state.ServiceTasks[startServiceTaskKey(startTaskKindReconcile, "1")]
	if !ok {
		t.Fatalf("expected reconcile task to be created")
	}
	if task.Status != startWorkServiceTaskQueued || task.RunID != "gh-1" {
		t.Fatalf("unexpected reconcile task: %+v", task)
	}
}

func TestStartRepoCoordinatorRetriesTriageTaskBeforeFailing(t *testing.T) {
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		state: &startWorkState{
			Issues: map[string]startWorkIssueState{
				"1": {
					SourceNumber:   1,
					ForkNumber:     101,
					State:          "open",
					Status:         startWorkStatusQueued,
					Labels:         []string{"nana"},
					TriageStatus:   startWorkTriageRunning,
					TriageError:    "",
					PrioritySource: "",
					UpdatedAt:      "now",
				},
			},
			ServiceTasks: map[string]startWorkServiceTask{
				"triage:1": {
					ID:       "triage:1",
					Kind:     startTaskKindTriage,
					Queue:    startTaskQueueService,
					Status:   startWorkServiceTaskRunning,
					IssueKey: "1",
					Attempts: 1,
				},
			},
		},
	}
	if err := coordinator.applyTaskResult(startRepoTaskResult{
		Task: startRepoTask{Key: "triage:1", Kind: startTaskKindTriage, IssueKey: "1"},
		Err:  fmt.Errorf("temporary triage failure"),
	}); err != nil {
		t.Fatalf("applyTaskResult: %v", err)
	}
	if got := coordinator.state.ServiceTasks["triage:1"].Status; got != startWorkServiceTaskQueued {
		t.Fatalf("expected triage task to requeue, got %s", got)
	}
	if got := coordinator.state.Issues["1"].TriageStatus; got != startWorkTriageQueued {
		t.Fatalf("expected issue triage state to requeue, got %s", got)
	}
}

func TestStartRepoCoordinatorReconcileRequeuesWhileMetadataIsPending(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	initial := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: "acme/widget",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Issues: map[string]startWorkIssueState{
			"1": {
				SourceNumber: 1,
				ForkNumber:   101,
				State:        "open",
				Status:       startWorkStatusReconciling,
				LastRunID:    "gh-1",
			},
		},
		ServiceTasks: map[string]startWorkServiceTask{
			"reconcile:1": {
				ID:       "reconcile:1",
				Kind:     startTaskKindReconcile,
				Queue:    startTaskQueueService,
				Status:   startWorkServiceTaskRunning,
				IssueKey: "1",
				Attempts: 1,
				RunID:    "gh-1",
			},
		},
		PlannedItems: map[string]startWorkPlannedItem{},
	}
	if err := writeStartWorkState(initial); err != nil {
		t.Fatalf("write initial state: %v", err)
	}

	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		state:    &initial,
	}
	if err := coordinator.applyTaskResult(startRepoTaskResult{
		Task: startRepoTask{Key: "reconcile:1", Kind: startTaskKindReconcile, IssueKey: "1"},
		Reconcile: &startWorkReconcileResult{
			Status:        startWorkStatusReconciling,
			BlockedReason: "waiting for publication state",
			RunID:         "gh-1",
			ShouldRetry:   true,
		},
	}); err != nil {
		t.Fatalf("applyTaskResult: %v", err)
	}
	if got := coordinator.state.ServiceTasks["reconcile:1"].Status; got != startWorkServiceTaskQueued {
		t.Fatalf("expected reconcile task to requeue, got %s", got)
	}
	if got := coordinator.state.ServiceTasks["reconcile:1"].WaitCycle; got != "cycle-1" {
		t.Fatalf("expected reconcile task to defer until next cycle, got %+v", coordinator.state.ServiceTasks["reconcile:1"])
	}
	if queue := coordinator.buildServiceQueue(); len(queue) != 0 {
		t.Fatalf("expected deferred reconcile task to skip same-cycle queueing, got %+v", queue)
	}
	if got := coordinator.state.Issues["1"].Status; got != startWorkStatusReconciling {
		t.Fatalf("expected issue to stay reconciling, got %s", got)
	}
}

func TestStartRepoCoordinatorBuildServiceQueueSkipsTasksUntilWaitUntil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		state: &startWorkState{
			Issues: map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{
				"triage:1": {
					ID:        "triage:1",
					Kind:      startTaskKindTriage,
					Queue:     startTaskQueueService,
					Status:    startWorkServiceTaskQueued,
					IssueKey:  "1",
					WaitUntil: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
				},
			},
		},
		running: map[string]startRepoTask{},
	}
	if err := syncCanonicalRepoTasksFromState(coordinator.repoSlug, coordinator.state); err != nil {
		t.Fatalf("syncCanonicalRepoTasksFromState: %v", err)
	}
	if queue := coordinator.buildServiceQueue(); len(queue) != 0 {
		t.Fatalf("expected future wait_until to suppress queueing, got %+v", queue)
	}
	coordinator.state.ServiceTasks["triage:1"] = startWorkServiceTask{
		ID:        "triage:1",
		Kind:      startTaskKindTriage,
		Queue:     startTaskQueueService,
		Status:    startWorkServiceTaskQueued,
		IssueKey:  "1",
		WaitUntil: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}
	if err := syncCanonicalRepoTasksFromState(coordinator.repoSlug, coordinator.state); err != nil {
		t.Fatalf("syncCanonicalRepoTasksFromState: %v", err)
	}
	if queue := coordinator.buildServiceQueue(); len(queue) != 1 || queue[0].Key != "triage:1" {
		t.Fatalf("expected expired wait_until to be runnable, got %+v", queue)
	}
}

func TestStartRepoCoordinatorApplyTaskResultPreservesExternallyAddedPlannedItems(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	initial := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: "acme/widget",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Issues: map[string]startWorkIssueState{
			"1": {
				SourceNumber: 1,
				State:        "open",
				Status:       startWorkStatusQueued,
				TriageStatus: startWorkTriageQueued,
			},
		},
		ServiceTasks: map[string]startWorkServiceTask{
			"triage:1": {
				ID:       "triage:1",
				Kind:     startTaskKindTriage,
				Queue:    startTaskQueueService,
				Status:   startWorkServiceTaskRunning,
				IssueKey: "1",
				Attempts: 1,
			},
		},
	}
	if err := writeStartWorkState(initial); err != nil {
		t.Fatalf("write initial state: %v", err)
	}

	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		state: &startWorkState{
			Issues:       map[string]startWorkIssueState{"1": initial.Issues["1"]},
			ServiceTasks: map[string]startWorkServiceTask{"triage:1": initial.ServiceTasks["triage:1"]},
		},
	}

	external, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	external.PlannedItems = map[string]startWorkPlannedItem{
		"planned-1": {
			ID:        "planned-1",
			RepoSlug:  "acme/widget",
			Title:     "Queued from UI",
			Priority:  3,
			State:     startPlannedItemQueued,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := writeStartWorkState(*external); err != nil {
		t.Fatalf("write external state: %v", err)
	}

	if err := coordinator.applyTaskResult(startRepoTaskResult{
		Task: startRepoTask{Key: "triage:1", Kind: startTaskKindTriage, IssueKey: "1"},
		Err:  fmt.Errorf("temporary triage failure"),
	}); err != nil {
		t.Fatalf("applyTaskResult: %v", err)
	}

	refreshed, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read refreshed state: %v", err)
	}
	if _, ok := refreshed.PlannedItems["planned-1"]; !ok {
		t.Fatalf("expected externally added planned item to survive coordinator write, got %+v", refreshed.PlannedItems)
	}
}

func TestStartRepoCoordinatorRefreshRepoStatePreservesPersistedScoutJobs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("mkdir source path: %v", err)
	}
	persisted := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		ScoutJobs: map[string]startWorkScoutJob{
			"scout-1": {
				ID:          "scout-1",
				Role:        improvementScoutRole,
				Title:       "Improve help text",
				Summary:     "Make help clearer",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Improve help text",
				Status:      startScoutJobRunning,
				RunID:       "lw-scout-1",
				CreatedAt:   time.Now().UTC().Format(time.RFC3339),
				UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := writeStartWorkState(persisted); err != nil {
		t.Fatalf("write persisted state: %v", err)
	}

	oldSync := startSyncRepoState
	defer func() { startSyncRepoState = oldSync }()
	startSyncRepoState = func(options startWorkOptions) (startWorkOptions, *startWorkState, int, bool, error) {
		return options, &startWorkState{
			Version:      startWorkStateVersion,
			SourceRepo:   repoSlug,
			UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			PlannedItems: map[string]startWorkPlannedItem{},
			ScoutJobs:    map[string]startWorkScoutJob{},
		}, 0, false, nil
	}

	coordinator := &startRepoCoordinator{
		repoSlug:      repoSlug,
		cycleID:       "cycle-1",
		workOptions:   startWorkOptions{RepoSlug: repoSlug, Parallel: 1},
		globalOptions: startOptions{Parallel: 1},
	}
	if err := coordinator.refreshRepoState(); err != nil {
		t.Fatalf("refreshRepoState: %v", err)
	}
	job, ok := coordinator.state.ScoutJobs["scout-1"]
	if !ok {
		t.Fatalf("expected persisted scout job to survive refresh, got %+v", coordinator.state.ScoutJobs)
	}
	if job.Status != startScoutJobRunning || job.RunID != "lw-scout-1" {
		t.Fatalf("expected preserved scout job state, got %+v", job)
	}
}

func TestPrepareStartRepoCycleResumesStaleLocalRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)

	manifest := localWorkManifest{
		Version:         1,
		RunID:           "lw-start-clean",
		CreatedAt:       "2026-04-17T00:00:00Z",
		UpdatedAt:       "2026-04-17T00:00:00Z",
		Status:          "running",
		CurrentPhase:    "implement",
		RepoRoot:        sourcePath,
		RepoName:        filepath.Base(sourcePath),
		RepoID:          localWorkRepoID(sourcePath),
		SourceBranch:    "main",
		BaselineSHA:     strings.TrimSpace(runLocalWorkTestGitOutput(t, sourcePath, "rev-parse", "HEAD")),
		SandboxPath:     filepath.Join(home, "sandboxes", "lw-start-clean"),
		SandboxRepoPath: filepath.Join(home, "sandboxes", "lw-start-clean", "repo"),
		MaxIterations:   8,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) { return "", nil }
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	oldDetachedRunner := localWorkStartDetachedRunner
	resumeCalls := []string{}
	localWorkStartDetachedRunner = func(repoRoot string, runID string, codexArgs []string, logPath string) error {
		resumeCalls = append(resumeCalls, repoRoot+"|"+runID+"|"+logPath)
		return nil
	}
	defer func() { localWorkStartDetachedRunner = oldDetachedRunner }()

	if _, err := prepareStartRepoCycle(repoSlug, startOptions{}); err != nil {
		t.Fatalf("prepareStartRepoCycle: %v", err)
	}
	if len(resumeCalls) != 1 || !strings.Contains(resumeCalls[0], manifest.RunID) {
		t.Fatalf("expected detached resume launch for stale local run, got %+v", resumeCalls)
	}

	updated, err := readLocalWorkManifestByRunID("lw-start-clean")
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.Status != "running" || updated.CompletedAt != "" || updated.LastError != "" {
		t.Fatalf("expected stale run resume during start prep, got %+v", updated)
	}
}

func TestPrepareStartRepoCycleResumesRecoverableStaleScoutRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	now := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:          1,
		RunID:            "lw-start-scout-resume",
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           "running",
		CurrentPhase:     "review",
		CurrentIteration: 1,
		MaxIterations:    8,
		RepoRoot:         sourcePath,
		RepoName:         filepath.Base(sourcePath),
		RepoID:           localWorkRepoID(sourcePath),
		SourceBranch:     "main",
		BaselineSHA:      strings.TrimSpace(runLocalWorkTestGitOutput(t, sourcePath, "rev-parse", "HEAD")),
		SandboxPath:      filepath.Join(home, "sandboxes", "lw-start-scout-resume"),
		SandboxRepoPath:  filepath.Join(home, "sandboxes", "lw-start-scout-resume", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  now,
		Issues:     map[string]startWorkIssueState{},
		ScoutJobs: map[string]startWorkScoutJob{
			"proposal-1": {
				ID:          "proposal-1",
				Role:        improvementScoutRole,
				Title:       "Improve help text",
				Summary:     "Make help clearer",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Improve help text",
				WorkType:    workTypeFeature,
				Status:      startScoutJobRunning,
				RunID:       manifest.RunID,
				UpdatedAt:   now,
				CreatedAt:   now,
			},
		},
	}); err != nil {
		t.Fatalf("writeStartWorkState: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) { return "", nil }
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	oldDetachedRunner := localWorkStartDetachedRunner
	resumeCalls := []string{}
	localWorkStartDetachedRunner = func(repoRoot string, runID string, codexArgs []string, logPath string) error {
		resumeCalls = append(resumeCalls, repoRoot+"|"+runID+"|"+logPath)
		return nil
	}
	defer func() { localWorkStartDetachedRunner = oldDetachedRunner }()

	if _, err := prepareStartRepoCycle(repoSlug, startOptions{}); err != nil {
		t.Fatalf("prepareStartRepoCycle: %v", err)
	}
	if len(resumeCalls) != 1 || !strings.Contains(resumeCalls[0], manifest.RunID) {
		t.Fatalf("expected detached resume launch for stale scout run, got %+v", resumeCalls)
	}

	updatedManifest, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updatedManifest.Status != "running" || updatedManifest.CompletedAt != "" || updatedManifest.LastError != "" {
		t.Fatalf("expected manifest to return to running after stale recovery, got %+v", updatedManifest)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("readStartWorkState: %v", err)
	}
	job := state.ScoutJobs["proposal-1"]
	if job.Status != startScoutJobRunning || job.RunID != manifest.RunID || job.LastError != "" {
		t.Fatalf("expected scout job to return to running after stale recovery, got %+v", job)
	}
}

func TestPrepareStartRepoCycleRequeuesUnrecoverableStaleScoutRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	now := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:          1,
		RunID:            "lw-start-scout-requeue",
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           "running",
		CurrentPhase:     "review",
		CurrentIteration: 1,
		MaxIterations:    1,
		RepoRoot:         sourcePath,
		RepoName:         filepath.Base(sourcePath),
		RepoID:           localWorkRepoID(sourcePath),
		SourceBranch:     "main",
		BaselineSHA:      strings.TrimSpace(runLocalWorkTestGitOutput(t, sourcePath, "rev-parse", "HEAD")),
		SandboxPath:      filepath.Join(home, "sandboxes", "lw-start-scout-requeue"),
		SandboxRepoPath:  filepath.Join(home, "sandboxes", "lw-start-scout-requeue", "repo"),
		Iterations:       []localWorkIterationSummary{{Iteration: 1, Status: "failed"}},
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  now,
		Issues:     map[string]startWorkIssueState{},
		ScoutJobs: map[string]startWorkScoutJob{
			"proposal-1": {
				ID:          "proposal-1",
				Role:        improvementScoutRole,
				Title:       "Improve help text",
				Summary:     "Make help clearer",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Improve help text",
				WorkType:    workTypeFeature,
				Status:      startScoutJobRunning,
				RunID:       manifest.RunID,
				UpdatedAt:   now,
				CreatedAt:   now,
			},
		},
	}); err != nil {
		t.Fatalf("writeStartWorkState: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) { return "", nil }
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	oldDetachedRunner := localWorkStartDetachedRunner
	localWorkStartDetachedRunner = func(repoRoot string, runID string, codexArgs []string, logPath string) error {
		t.Fatalf("did not expect detached resume launch for unrecoverable stale scout run")
		return nil
	}
	defer func() { localWorkStartDetachedRunner = oldDetachedRunner }()

	if _, err := prepareStartRepoCycle(repoSlug, startOptions{}); err != nil {
		t.Fatalf("prepareStartRepoCycle: %v", err)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("readStartWorkState: %v", err)
	}
	job := state.ScoutJobs["proposal-1"]
	if job.Status != startScoutJobQueued || job.RunID != "" || !strings.Contains(job.LastError, localWorkStaleCleanupError) {
		t.Fatalf("expected scout job to return to queued after unrecoverable stale cleanup, got %+v", job)
	}
}

func TestPrepareStartRepoCycleKeepsRecoveredStaleScoutRunOutOfApprovals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	now := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:          1,
		RunID:            "lw-start-scout-approval",
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           "running",
		CurrentPhase:     "review",
		CurrentIteration: 1,
		MaxIterations:    8,
		RepoRoot:         sourcePath,
		RepoName:         filepath.Base(sourcePath),
		RepoID:           localWorkRepoID(sourcePath),
		SourceBranch:     "main",
		BaselineSHA:      strings.TrimSpace(runLocalWorkTestGitOutput(t, sourcePath, "rev-parse", "HEAD")),
		SandboxPath:      filepath.Join(home, "sandboxes", "lw-start-scout-approval"),
		SandboxRepoPath:  filepath.Join(home, "sandboxes", "lw-start-scout-approval", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  now,
		Issues:     map[string]startWorkIssueState{},
		ScoutJobs: map[string]startWorkScoutJob{
			"proposal-1": {
				ID:          "proposal-1",
				Role:        improvementScoutRole,
				Title:       "Improve help text",
				Summary:     "Make help clearer",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Improve help text",
				WorkType:    workTypeFeature,
				Status:      startScoutJobRunning,
				RunID:       manifest.RunID,
				UpdatedAt:   now,
				CreatedAt:   now,
			},
		},
	}); err != nil {
		t.Fatalf("writeStartWorkState: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) { return "", nil }
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	oldDetachedRunner := localWorkStartDetachedRunner
	localWorkStartDetachedRunner = func(repoRoot string, runID string, codexArgs []string, logPath string) error { return nil }
	defer func() { localWorkStartDetachedRunner = oldDetachedRunner }()

	if _, err := prepareStartRepoCycle(repoSlug, startOptions{}); err != nil {
		t.Fatalf("prepareStartRepoCycle: %v", err)
	}

	approvals, err := loadStartUIApprovals()
	if err != nil {
		t.Fatalf("loadStartUIApprovals: %v", err)
	}
	for _, item := range approvals {
		if item.ScoutJobID == "proposal-1" {
			t.Fatalf("expected recovered stale scout run to stay out of approvals, got %+v", item)
		}
	}
}

func TestPrepareStartRepoCycleAdvancesDismissedItemsToDeletedAndPurgesExpiredDeletedItems(t *testing.T) {
	home := setLocalWorkDBProxyTestHome(t)
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)

	now := time.Now().UTC()
	oldStamp := now.AddDate(0, 0, -8).Format(time.RFC3339)
	freshStamp := now.AddDate(0, 0, -2).Format(time.RFC3339)
	purgeStamp := now.AddDate(0, 0, -31).Format(time.RFC3339)

	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:                    6,
		RepoMode:                   "repo",
		IssuePickMode:              "auto",
		PRForwardMode:              "approve",
		DismissedItemRetentionDays: intPtr(7),
		DeletedItemRetentionDays:   intPtr(30),
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  now.Format(time.RFC3339),
		Issues:     map[string]startWorkIssueState{},
		ScoutJobs: map[string]startWorkScoutJob{
			"scout-old": {
				ID:          "scout-old",
				Role:        improvementScoutRole,
				Title:       "Old dismissed scout",
				Summary:     "Expired dismissed scout",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Old dismissed scout",
				Status:      startScoutJobDismissed,
				CreatedAt:   oldStamp,
				UpdatedAt:   oldStamp,
			},
			"scout-stale-deleted": {
				ID:          "scout-stale-deleted",
				Role:        improvementScoutRole,
				Title:       "Stale deleted scout",
				Summary:     "Expired deleted scout",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Stale deleted scout",
				Status:      startScoutJobDeleted,
				CreatedAt:   purgeStamp,
				UpdatedAt:   purgeStamp,
			},
			"scout-fresh": {
				ID:          "scout-fresh",
				Role:        improvementScoutRole,
				Title:       "Fresh dismissed scout",
				Summary:     "Recent dismissed scout",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Fresh dismissed scout",
				Status:      startScoutJobDismissed,
				CreatedAt:   freshStamp,
				UpdatedAt:   freshStamp,
			},
		},
		Findings: map[string]startWorkFinding{
			"finding-old": {
				ID:         "finding-old",
				RepoSlug:   repoSlug,
				SourceKind: startWorkFindingSourceKindManualScout,
				SourceID:   "scout-old",
				Title:      "Old dismissed finding",
				Status:     startWorkFindingStatusDismissed,
				CreatedAt:  oldStamp,
				UpdatedAt:  oldStamp,
			},
			"finding-stale-deleted": {
				ID:         "finding-stale-deleted",
				RepoSlug:   repoSlug,
				SourceKind: startWorkFindingSourceKindManualScout,
				SourceID:   "scout-stale-deleted",
				Title:      "Stale deleted finding",
				Status:     startWorkFindingStatusDeleted,
				CreatedAt:  purgeStamp,
				UpdatedAt:  purgeStamp,
			},
			"finding-fresh": {
				ID:         "finding-fresh",
				RepoSlug:   repoSlug,
				SourceKind: startWorkFindingSourceKindManualScout,
				SourceID:   "scout-fresh",
				Title:      "Fresh dismissed finding",
				Status:     startWorkFindingStatusDismissed,
				CreatedAt:  freshStamp,
				UpdatedAt:  freshStamp,
			},
		},
	}); err != nil {
		t.Fatalf("writeStartWorkState: %v", err)
	}

	oldDropped, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "review_request",
		ExternalID: "old-dismissed",
		RepoSlug:   repoSlug,
		Subject:    "Old dropped item",
		Body:       "old",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue old dropped work item: %v", err)
	}
	oldSilenced, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "old-silenced",
		RepoSlug:   repoSlug,
		Subject:    "Old silenced item",
		Body:       "old",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue old silenced work item: %v", err)
	}
	freshDropped, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "review_request",
		ExternalID: "fresh-dismissed",
		RepoSlug:   repoSlug,
		Subject:    "Fresh dropped item",
		Body:       "fresh",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue fresh dropped work item: %v", err)
	}
	oldDeleted, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "stale-deleted",
		RepoSlug:   repoSlug,
		Subject:    "Stale deleted item",
		Body:       "deleted",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue stale deleted work item: %v", err)
	}

	if err := withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		item, err := store.readWorkItem(oldDropped.ID)
		if err != nil {
			return err
		}
		item.Status = workItemStatusDropped
		item.Hidden = false
		item.HiddenReason = ""
		item.UpdatedAt = oldStamp
		item.LatestActionAt = oldStamp
		if err := store.updateWorkItem(item); err != nil {
			return err
		}

		item, err = store.readWorkItem(oldSilenced.ID)
		if err != nil {
			return err
		}
		item.Status = workItemStatusSilenced
		item.Hidden = true
		item.HiddenReason = "retention-test"
		item.UpdatedAt = oldStamp
		item.LatestActionAt = oldStamp
		if err := store.updateWorkItem(item); err != nil {
			return err
		}

		item, err = store.readWorkItem(freshDropped.ID)
		if err != nil {
			return err
		}
		item.Status = workItemStatusDropped
		item.Hidden = false
		item.HiddenReason = ""
		item.UpdatedAt = freshStamp
		item.LatestActionAt = freshStamp
		if err := store.updateWorkItem(item); err != nil {
			return err
		}

		item, err = store.readWorkItem(oldDeleted.ID)
		if err != nil {
			return err
		}
		item.Status = workItemStatusDeleted
		item.Hidden = true
		item.HiddenReason = "deleted-retention-test"
		item.UpdatedAt = purgeStamp
		item.LatestActionAt = purgeStamp
		return store.updateWorkItem(item)
	}); err != nil {
		t.Fatalf("seed dismissed work items: %v", err)
	}

	oldPreflight := githubAutomationRepoPreflight
	githubAutomationRepoPreflight = func(repo string, repairOrigin bool) error { return nil }
	defer func() { githubAutomationRepoPreflight = oldPreflight }()

	prepared, err := prepareStartRepoCycle(repoSlug, startOptions{})
	if err != nil {
		t.Fatalf("prepareStartRepoCycle: %v", err)
	}
	if prepared == nil {
		t.Fatalf("expected prepared repo cycle")
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("readStartWorkState: %v", err)
	}
	if job, ok := state.ScoutJobs["scout-old"]; !ok || job.Status != startScoutJobDeleted {
		t.Fatalf("expected expired dismissed scout job to transition to deleted, got %+v", state.ScoutJobs["scout-old"])
	} else if job.DeletedFromStatus != startScoutJobDismissed || strings.TrimSpace(job.DeletedAt) == "" {
		t.Fatalf("expected deleted scout job provenance, got %+v", job)
	}
	if _, ok := state.ScoutJobs["scout-stale-deleted"]; ok {
		t.Fatalf("expected stale deleted scout job to be purged, got %+v", state.ScoutJobs)
	}
	if finding, ok := state.Findings["finding-old"]; !ok || finding.Status != startWorkFindingStatusDeleted {
		t.Fatalf("expected expired dismissed finding to transition to deleted, got %+v", state.Findings["finding-old"])
	} else if finding.DeletedFromStatus != startWorkFindingStatusDismissed || strings.TrimSpace(finding.DeletedAt) == "" {
		t.Fatalf("expected deleted finding provenance, got %+v", finding)
	}
	if _, ok := state.Findings["finding-stale-deleted"]; ok {
		t.Fatalf("expected stale deleted finding to be purged, got %+v", state.Findings)
	}
	if _, ok := state.ScoutJobs["scout-fresh"]; !ok {
		t.Fatalf("expected fresh dismissed scout job to remain, got %+v", state.ScoutJobs)
	}
	if finding, ok := state.Findings["finding-fresh"]; !ok || finding.Status != startWorkFindingStatusDismissed {
		t.Fatalf("expected fresh dismissed finding to remain, got %+v", state.Findings)
	}

	if detail, err := readWorkItemDetail(oldDropped.ID); err != nil || detail.Item.Status != workItemStatusDeleted {
		t.Fatalf("expected old dropped work item to transition to deleted, got detail=%+v err=%v", detail, err)
	} else if snapshot, ok := readWorkItemDeletedRestoreMetadata(detail.Item.Metadata); !ok || snapshot.Status != workItemStatusDropped || snapshot.Hidden {
		t.Fatalf("expected deleted dropped work item provenance, got metadata=%+v snapshot=%+v", detail.Item.Metadata, snapshot)
	}
	if detail, err := readWorkItemDetail(oldSilenced.ID); err != nil || detail.Item.Status != workItemStatusDeleted {
		t.Fatalf("expected old silenced work item to transition to deleted, got detail=%+v err=%v", detail, err)
	} else if snapshot, ok := readWorkItemDeletedRestoreMetadata(detail.Item.Metadata); !ok || snapshot.Status != workItemStatusSilenced || !snapshot.Hidden {
		t.Fatalf("expected deleted silenced work item provenance, got metadata=%+v snapshot=%+v", detail.Item.Metadata, snapshot)
	}
	if _, err := readWorkItemDetail(freshDropped.ID); err != nil {
		t.Fatalf("expected fresh dropped work item to remain, got err=%v", err)
	}
	if _, err := readWorkItemDetail(oldDeleted.ID); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("expected stale deleted work item to be purged, got err=%v", err)
	}

	items, err := withLocalWorkReadStore(func(store *localWorkDBStore) ([]startUITaskSummary, error) {
		return store.listCanonicalTasksForRepo(repoSlug)
	})
	if err != nil {
		t.Fatalf("listCanonicalTasksForRepo: %v", err)
	}
	ids := []string{}
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if slices.Contains(ids, "scout-job:"+repoSlug+":scout-old") || slices.Contains(ids, "scout-job:"+repoSlug+":scout-stale-deleted") {
		t.Fatalf("expected deleted scout tasks to stay out of canonical tasks, got %+v", ids)
	}
	if slices.Contains(ids, "work-item:"+oldDropped.ID) || slices.Contains(ids, "work-item:"+oldSilenced.ID) || slices.Contains(ids, "work-item:"+oldDeleted.ID) {
		t.Fatalf("expected deleted work item tasks to stay out of canonical tasks, got %+v", ids)
	}
	if !slices.Contains(ids, "scout-job:"+repoSlug+":scout-fresh") || !slices.Contains(ids, "work-item:"+freshDropped.ID) {
		t.Fatalf("expected fresh dismissed items to remain visible, got %+v", ids)
	}
}

func TestStartRepoCoordinatorCapacitySnapshotCountsRunnableAndBlockedServiceTasks(t *testing.T) {
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		workOptions: startWorkOptions{
			Parallel: 10,
		},
		state: &startWorkState{
			SourceRepo: "acme/widget",
			Issues:     map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{
				"planned-launch:1": {
					ID:            "planned-launch:1",
					Kind:          startTaskKindPlannedLaunch,
					Queue:         startTaskQueueService,
					Status:        startWorkServiceTaskRunning,
					PlannedItemID: "1",
				},
				"issue-sync:1": {
					ID:             "issue-sync:1",
					Kind:           startTaskKindIssueSync,
					Queue:          startTaskQueueService,
					Status:         startWorkServiceTaskQueued,
					DependencyKeys: []string{"scout:ui-scout"},
				},
				"scout:ui-scout": {
					ID:        "scout:ui-scout",
					Kind:      startTaskKindScout,
					Queue:     startTaskQueueService,
					Status:    startWorkServiceTaskFailed,
					ScoutRole: uiScoutRole,
				},
			},
			PlannedItems: map[string]startWorkPlannedItem{},
		},
		running: map[string]startRepoTask{},
	}
	active, limit, runnableService, runnableImplementation, blockedService := coordinator.capacitySnapshot()
	if active != 1 || limit != 10 || runnableService != 0 || runnableImplementation != 0 || blockedService != 1 {
		t.Fatalf("unexpected capacity snapshot: active=%d limit=%d runnableService=%d runnableImplementation=%d blockedService=%d", active, limit, runnableService, runnableImplementation, blockedService)
	}
}

func TestStartRepoCoordinatorLogsUnexpectedlyDeadStaleRunDuringCycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)

	manifest := localWorkManifest{
		Version:      1,
		RunID:        "lw-dead-during-cycle",
		CreatedAt:    time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
		Status:       "running",
		CurrentPhase: "review",
		RepoRoot:     sourcePath,
		RepoName:     filepath.Base(sourcePath),
		RepoID:       localWorkRepoID(sourcePath),
		SourceBranch: "main",
		BaselineSHA:  strings.TrimSpace(runLocalWorkTestGitOutput(t, sourcePath, "rev-parse", "HEAD")),
		SandboxPath:  filepath.Join(home, "sandboxes", "lw-dead-during-cycle"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) { return "", nil }
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	oldSync := startSyncRepoState
	startSyncRepoState = func(options startWorkOptions) (startWorkOptions, *startWorkState, int, bool, error) {
		return options, &startWorkState{
			Version:       startWorkStateVersion,
			SourceRepo:    repoSlug,
			DefaultBranch: "main",
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
			Issues:        map[string]startWorkIssueState{},
			ServiceTasks:  map[string]startWorkServiceTask{},
			PlannedItems:  map[string]startWorkPlannedItem{},
		}, 0, false, nil
	}
	defer func() { startSyncRepoState = oldSync }()

	output, err := captureStdout(t, func() error {
		return runStartRepoSchedulerCycle(".", repoSlug, startWorkOptions{RepoSlug: repoSlug, Parallel: 1}, startOptions{Parallel: 1})
	})
	if err != nil {
		t.Fatalf("runStartRepoSchedulerCycle: %v\n%s", err, output)
	}
	if !strings.Contains(output, "stale local work run lw-dead-during-cycle marked failed unexpectedly") {
		t.Fatalf("expected stale-run log in output, got %q", output)
	}

	updated, err := readLocalWorkManifestByRunID("lw-dead-during-cycle")
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.Status != "failed" {
		t.Fatalf("expected stale run to be failed, got %+v", updated)
	}
}

func TestStartRepoCoordinatorCleansDeadRunEvenWhenAnotherRunSharesRepoRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)

	manifest := localWorkManifest{
		Version:      1,
		RunID:        "lw-dead-shared-root-during-cycle",
		CreatedAt:    time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
		Status:       "running",
		CurrentPhase: "review",
		RepoRoot:     sourcePath,
		RepoName:     filepath.Base(sourcePath),
		RepoID:       localWorkRepoID(sourcePath),
		SourceBranch: "main",
		BaselineSHA:  strings.TrimSpace(runLocalWorkTestGitOutput(t, sourcePath, "rev-parse", "HEAD")),
		SandboxPath:  filepath.Join(home, "sandboxes", "lw-dead-shared-root-during-cycle"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) {
		return "321 /tmp/nana work resume --run-id lw-other --repo " + sourcePath, nil
	}
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	oldSync := startSyncRepoState
	startSyncRepoState = func(options startWorkOptions) (startWorkOptions, *startWorkState, int, bool, error) {
		return options, &startWorkState{
			Version:       startWorkStateVersion,
			SourceRepo:    repoSlug,
			DefaultBranch: "main",
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
			Issues:        map[string]startWorkIssueState{},
			ServiceTasks:  map[string]startWorkServiceTask{},
			PlannedItems:  map[string]startWorkPlannedItem{},
		}, 0, false, nil
	}
	defer func() { startSyncRepoState = oldSync }()

	output, err := captureStdout(t, func() error {
		return runStartRepoSchedulerCycle(".", repoSlug, startWorkOptions{RepoSlug: repoSlug, Parallel: 1}, startOptions{Parallel: 1})
	})
	if err != nil {
		t.Fatalf("runStartRepoSchedulerCycle: %v\n%s", err, output)
	}
	if !strings.Contains(output, "stale local work run lw-dead-shared-root-during-cycle marked failed unexpectedly") {
		t.Fatalf("expected stale-run log in output, got %q", output)
	}

	updated, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.Status != "failed" {
		t.Fatalf("expected stale run to be failed, got %+v", updated)
	}
}

func TestStartRepoCoordinatorRecoversStaleScoutRunDuringCycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	now := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:          1,
		RunID:            "lw-dead-scout-during-cycle",
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           "running",
		CurrentPhase:     "review",
		CurrentIteration: 1,
		MaxIterations:    8,
		RepoRoot:         sourcePath,
		RepoName:         filepath.Base(sourcePath),
		RepoID:           localWorkRepoID(sourcePath),
		SourceBranch:     "main",
		BaselineSHA:      strings.TrimSpace(runLocalWorkTestGitOutput(t, sourcePath, "rev-parse", "HEAD")),
		SandboxPath:      filepath.Join(home, "sandboxes", "lw-dead-scout-during-cycle"),
		SandboxRepoPath:  filepath.Join(home, "sandboxes", "lw-dead-scout-during-cycle", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  now,
		Issues:     map[string]startWorkIssueState{},
		ScoutJobs: map[string]startWorkScoutJob{
			"proposal-1": {
				ID:          "proposal-1",
				Role:        improvementScoutRole,
				Title:       "Improve help text",
				Summary:     "Make help clearer",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Improve help text",
				WorkType:    workTypeFeature,
				Status:      startScoutJobRunning,
				RunID:       manifest.RunID,
				UpdatedAt:   now,
				CreatedAt:   now,
			},
		},
	}); err != nil {
		t.Fatalf("writeStartWorkState: %v", err)
	}

	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) { return "", nil }
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	oldSync := startSyncRepoState
	startSyncRepoState = func(options startWorkOptions) (startWorkOptions, *startWorkState, int, bool, error) {
		return options, &startWorkState{
			Version:       startWorkStateVersion,
			SourceRepo:    repoSlug,
			DefaultBranch: "main",
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
			Issues:        map[string]startWorkIssueState{},
			ServiceTasks:  map[string]startWorkServiceTask{},
			PlannedItems:  map[string]startWorkPlannedItem{},
		}, 0, false, nil
	}
	defer func() { startSyncRepoState = oldSync }()

	oldDetachedRunner := localWorkStartDetachedRunner
	resumeCalls := []string{}
	localWorkStartDetachedRunner = func(repoRoot string, runID string, codexArgs []string, logPath string) error {
		resumeCalls = append(resumeCalls, repoRoot+"|"+runID+"|"+logPath)
		return nil
	}
	defer func() { localWorkStartDetachedRunner = oldDetachedRunner }()

	output, err := captureStdout(t, func() error {
		return runStartRepoSchedulerCycle(".", repoSlug, startWorkOptions{RepoSlug: repoSlug, Parallel: 1}, startOptions{Parallel: 1})
	})
	if err != nil {
		t.Fatalf("runStartRepoSchedulerCycle: %v\n%s", err, output)
	}
	if len(resumeCalls) != 1 || !strings.Contains(resumeCalls[0], manifest.RunID) {
		t.Fatalf("expected detached resume launch for stale scout run during cycle, got %+v", resumeCalls)
	}
	if strings.Contains(output, "marked failed unexpectedly") {
		t.Fatalf("expected stale scout recovery to suppress unexpected-failure log, got %q", output)
	}

	updatedManifest, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updatedManifest.Status != "running" || updatedManifest.LastError != "" {
		t.Fatalf("expected resumed scout manifest to be running, got %+v", updatedManifest)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("readStartWorkState: %v", err)
	}
	job := state.ScoutJobs["proposal-1"]
	if job.Status != startScoutJobRunning || job.RunID != manifest.RunID || job.LastError != "" {
		t.Fatalf("expected scout job to return to running during cycle recovery, got %+v", job)
	}
}

func TestRunStartWorkIssueReconcileRefreshesPublishedPRCIState(t *testing.T) {
	serverState := struct {
		headSHA string
		headRef string
		htmlURL string
	}{
		headRef: "nana/issue-1/fix",
		htmlURL: "https://example.invalid/pr/77",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/acme/widget/pulls/77?":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"number":77,"html_url":%q,"head":{"ref":%q,"sha":%q}}`, serverState.htmlURL, serverState.headRef, serverState.headSHA)))
		case "GET /repos/acme/widget/commits/pending-sha/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[{"status":"queued"}]}`))
		case "GET /repos/acme/widget/actions/runs?head_sha=pending-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "GET /repos/acme/widget/commits/no-ci-sha/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case "GET /repos/acme/widget/actions/runs?head_sha=no-ci-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "GET /repos/acme/widget/commits/unavailable-sha/check-runs?per_page=100":
			http.Error(w, `{"message":"unavailable"}`, http.StatusInternalServerError)
		case "GET /repos/acme/widget/actions/runs?head_sha=unavailable-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GITHUB_API_URL", server.URL)

	cases := []struct {
		name                  string
		headSHA               string
		wantStatus            string
		wantPublicationState  string
		wantPublicationDetail string
		wantBlockedReason     string
		wantShouldRetry       bool
		wantPublicationError  string
	}{
		{
			name:                  "pending",
			headSHA:               "pending-sha",
			wantStatus:            startWorkStatusReconciling,
			wantPublicationState:  "ci_waiting",
			wantPublicationDetail: "ci_pending",
			wantBlockedReason:     "ci_pending",
			wantShouldRetry:       true,
			wantPublicationError:  "",
		},
		{
			name:                  "no ci",
			headSHA:               "no-ci-sha",
			wantStatus:            startWorkStatusCompleted,
			wantPublicationState:  "ci_green",
			wantPublicationDetail: "no_ci_found",
			wantBlockedReason:     "",
			wantShouldRetry:       false,
			wantPublicationError:  "",
		},
		{
			name:                  "unavailable",
			headSHA:               "unavailable-sha",
			wantStatus:            startWorkStatusBlocked,
			wantPublicationState:  "blocked",
			wantPublicationDetail: "check_runs_unavailable",
			wantBlockedReason:     "check_runs_unavailable",
			wantShouldRetry:       false,
			wantPublicationError:  "check_runs_unavailable",
		},
		{
			name:                  "missing head sha",
			headSHA:               "",
			wantStatus:            startWorkStatusBlocked,
			wantPublicationState:  "blocked",
			wantPublicationDetail: "missing_head_sha",
			wantBlockedReason:     "missing_head_sha",
			wantShouldRetry:       false,
			wantPublicationError:  "missing_head_sha",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			serverState.headSHA = tc.headSHA

			managedRepoRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
			runID := "gh-reconcile-" + strings.ReplaceAll(tc.name, " ", "-")
			runDir := filepath.Join(managedRepoRoot, "runs", runID)
			sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-1-"+runID)
			repoCheckoutPath := filepath.Join(sandboxPath, "repo")
			if err := os.MkdirAll(runDir, 0o755); err != nil {
				t.Fatalf("mkdir run dir: %v", err)
			}
			if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
				t.Fatalf("mkdir repo checkout: %v", err)
			}

			manifest := githubWorkManifest{
				Version:            3,
				RunID:              runID,
				CreatedAt:          "2026-04-12T12:00:00Z",
				UpdatedAt:          "2026-04-12T12:00:00Z",
				RepoSlug:           "acme/widget",
				RepoOwner:          "acme",
				RepoName:           "widget",
				ManagedRepoRoot:    managedRepoRoot,
				SandboxID:          "issue-1-" + runID,
				SandboxPath:        sandboxPath,
				SandboxRepoPath:    repoCheckoutPath,
				TargetKind:         "issue",
				TargetNumber:       1,
				TargetURL:          "https://github.com/acme/widget/issues/1",
				CreatePROnComplete: true,
				PublishTarget:      "repo",
				PublishedPRNumber:  77,
				PublishedPRURL:     "https://example.invalid/pr/stale",
				PublicationState:   "ci_waiting",
				PublicationDetail:  "ci_pending",
				PublicationError:   "ci_pending",
				PublishedPRHeadRef: "stale-ref",
			}
			manifestPath := filepath.Join(runDir, "manifest.json")
			if err := writeGithubJSON(manifestPath, manifest); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
				t.Fatalf("index manifest: %v", err)
			}

			result, err := runStartWorkIssueReconcile("acme/widget", "repo", startWorkIssueState{LastRunID: runID})
			if err != nil {
				t.Fatalf("runStartWorkIssueReconcile(): %v", err)
			}
			if result.Status != tc.wantStatus || result.PublicationState != tc.wantPublicationState || result.BlockedReason != tc.wantBlockedReason || result.ShouldRetry != tc.wantShouldRetry {
				t.Fatalf("unexpected reconcile result: %+v", result)
			}

			updatedManifest, err := readGithubWorkManifest(manifestPath)
			if err != nil {
				t.Fatalf("read updated manifest: %v", err)
			}
			if updatedManifest.PublicationState != tc.wantPublicationState || updatedManifest.PublicationDetail != tc.wantPublicationDetail || updatedManifest.PublicationError != tc.wantPublicationError || updatedManifest.PublishedPRHeadRef != serverState.headRef || updatedManifest.PublishedPRURL != serverState.htmlURL {
				t.Fatalf("unexpected updated manifest: %+v", updatedManifest)
			}
		})
	}
}

func TestRunStartWorkIssueReconcileRefreshesForkPublishedPRCIState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/me/widget/pulls/77?":
			_, _ = w.Write([]byte(`{"number":77,"html_url":"https://example.invalid/me/widget/pull/77","head":{"ref":"nana/issue-2/fix","sha":"no-ci-sha"}}`))
		case "GET /repos/me/widget/commits/no-ci-sha/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case "GET /repos/me/widget/actions/runs?head_sha=no-ci-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GITHUB_API_URL", server.URL)

	managedRepoRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	runID := "gh-reconcile-fork"
	runDir := filepath.Join(managedRepoRoot, "runs", runID)
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-2-"+runID)
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := githubWorkManifest{
		Version:            3,
		RunID:              runID,
		CreatedAt:          "2026-04-12T12:00:00Z",
		UpdatedAt:          "2026-04-12T12:00:00Z",
		RepoSlug:           "acme/widget",
		RepoOwner:          "acme",
		RepoName:           "widget",
		ManagedRepoRoot:    managedRepoRoot,
		SandboxID:          "issue-2-" + runID,
		SandboxPath:        sandboxPath,
		SandboxRepoPath:    repoCheckoutPath,
		TargetKind:         "issue",
		TargetNumber:       2,
		TargetURL:          "https://github.com/acme/widget/issues/2",
		CreatePROnComplete: true,
		PublishTarget:      "fork",
		PublishRepoSlug:    "me/widget",
		PublishRepoOwner:   "me",
		PublishedPRNumber:  77,
		PublishedPRURL:     "https://example.invalid/me/widget/pull/stale",
		PublicationState:   "ci_waiting",
		PublicationDetail:  "ci_pending",
		PublicationError:   "ci_pending",
		PublishedPRHeadRef: "stale-ref",
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index manifest: %v", err)
	}

	result, err := runStartWorkIssueReconcile("acme/widget", "fork", startWorkIssueState{LastRunID: runID})
	if err != nil {
		t.Fatalf("runStartWorkIssueReconcile(): %v", err)
	}
	if result.Status != startWorkStatusCompleted || result.PublicationState != "ci_green" || result.PublishedPRURL != "https://example.invalid/me/widget/pull/77" {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
	updatedManifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read updated manifest: %v", err)
	}
	if updatedManifest.PublicationDetail != "no_ci_found" {
		t.Fatalf("expected no_ci_found publication detail, got %+v", updatedManifest)
	}
}

func TestRunStartWorkIssueReconcileBlocksAndPersistsPRFetchFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/acme/widget/pulls/77?":
			http.Error(w, `{"message":"missing"}`, http.StatusNotFound)
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GITHUB_API_URL", server.URL)

	managedRepoRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	runID := "gh-reconcile-fetch-fail"
	runDir := filepath.Join(managedRepoRoot, "runs", runID)
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-3-"+runID)
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := githubWorkManifest{
		Version:            3,
		RunID:              runID,
		CreatedAt:          "2026-04-12T12:00:00Z",
		UpdatedAt:          "2026-04-12T12:00:00Z",
		RepoSlug:           "acme/widget",
		RepoOwner:          "acme",
		RepoName:           "widget",
		ManagedRepoRoot:    managedRepoRoot,
		SandboxID:          "issue-3-" + runID,
		SandboxPath:        sandboxPath,
		SandboxRepoPath:    repoCheckoutPath,
		TargetKind:         "issue",
		TargetNumber:       3,
		TargetURL:          "https://github.com/acme/widget/issues/3",
		CreatePROnComplete: true,
		PublishTarget:      "repo",
		PublishedPRNumber:  77,
		PublicationState:   "ci_waiting",
		PublicationDetail:  "ci_pending",
		PublicationError:   "ci_pending",
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index manifest: %v", err)
	}

	result, err := runStartWorkIssueReconcile("acme/widget", "repo", startWorkIssueState{LastRunID: runID})
	if err != nil {
		t.Fatalf("runStartWorkIssueReconcile(): %v", err)
	}
	if result.Status != startWorkStatusBlocked || result.PublicationState != "blocked" || !strings.Contains(result.BlockedReason, "404") {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
	updatedManifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read updated manifest: %v", err)
	}
	if updatedManifest.PublicationState != "blocked" || !strings.Contains(updatedManifest.PublicationError, "404") {
		t.Fatalf("expected persisted blocked publication error, got %+v", updatedManifest)
	}
}

func TestRunStartWorkIssueReconcileRetriesTransientPRFetchFailureNextCycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/acme/widget/pulls/77?":
			http.Error(w, `{"message":"busy"}`, http.StatusServiceUnavailable)
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GITHUB_API_URL", server.URL)

	managedRepoRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	runID := "gh-reconcile-fetch-transient"
	runDir := filepath.Join(managedRepoRoot, "runs", runID)
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-4-"+runID)
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := githubWorkManifest{
		Version:            3,
		RunID:              runID,
		CreatedAt:          "2026-04-12T12:00:00Z",
		UpdatedAt:          "2026-04-12T12:00:00Z",
		RepoSlug:           "acme/widget",
		RepoOwner:          "acme",
		RepoName:           "widget",
		ManagedRepoRoot:    managedRepoRoot,
		SandboxID:          "issue-4-" + runID,
		SandboxPath:        sandboxPath,
		SandboxRepoPath:    repoCheckoutPath,
		TargetKind:         "issue",
		TargetNumber:       4,
		TargetURL:          "https://github.com/acme/widget/issues/4",
		CreatePROnComplete: true,
		PublishTarget:      "repo",
		PublishedPRNumber:  77,
		PublishedPRURL:     "https://example.invalid/pr/77",
		PublicationState:   "ci_waiting",
		PublicationDetail:  "ci_pending",
		PublicationError:   "ci_pending",
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index manifest: %v", err)
	}

	result, err := runStartWorkIssueReconcile("acme/widget", "repo", startWorkIssueState{LastRunID: runID})
	if err != nil {
		t.Fatalf("runStartWorkIssueReconcile(): %v", err)
	}
	if result.Status != startWorkStatusReconciling || result.PublicationState != "ci_waiting" || !strings.Contains(result.BlockedReason, "503") || !result.ShouldRetry {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
	updatedManifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read updated manifest: %v", err)
	}
	if updatedManifest.PublicationState != "ci_waiting" || !strings.Contains(updatedManifest.PublicationDetail, "503") || updatedManifest.PublicationError != "" {
		t.Fatalf("expected persisted waiting publication detail, got %+v", updatedManifest)
	}
}

func TestRunStartWorkIssueReconcileRetriesTransientCIFetchFailureNextCycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/acme/widget/pulls/77?":
			_, _ = w.Write([]byte(`{"number":77,"html_url":"https://example.invalid/pr/77","head":{"ref":"nana/issue-5/fix","sha":"transient-ci-sha"}}`))
		case "GET /repos/acme/widget/commits/transient-ci-sha/check-runs?per_page=100":
			http.Error(w, `{"message":"busy"}`, http.StatusServiceUnavailable)
		case "GET /repos/acme/widget/actions/runs?head_sha=transient-ci-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GITHUB_API_URL", server.URL)

	managedRepoRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	runID := "gh-reconcile-ci-transient"
	runDir := filepath.Join(managedRepoRoot, "runs", runID)
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-5-"+runID)
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := githubWorkManifest{
		Version:            3,
		RunID:              runID,
		CreatedAt:          "2026-04-12T12:00:00Z",
		UpdatedAt:          "2026-04-12T12:00:00Z",
		RepoSlug:           "acme/widget",
		RepoOwner:          "acme",
		RepoName:           "widget",
		ManagedRepoRoot:    managedRepoRoot,
		SandboxID:          "issue-5-" + runID,
		SandboxPath:        sandboxPath,
		SandboxRepoPath:    repoCheckoutPath,
		TargetKind:         "issue",
		TargetNumber:       5,
		TargetURL:          "https://github.com/acme/widget/issues/5",
		CreatePROnComplete: true,
		PublishTarget:      "repo",
		PublishedPRNumber:  77,
		PublishedPRURL:     "https://example.invalid/pr/77",
		PublicationState:   "ci_waiting",
		PublicationDetail:  "ci_pending",
		PublicationError:   "ci_pending",
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index manifest: %v", err)
	}

	result, err := runStartWorkIssueReconcile("acme/widget", "repo", startWorkIssueState{LastRunID: runID})
	if err != nil {
		t.Fatalf("runStartWorkIssueReconcile(): %v", err)
	}
	if result.Status != startWorkStatusReconciling || result.PublicationState != "ci_waiting" || result.BlockedReason != "check_runs_unavailable" || !result.ShouldRetry {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
	updatedManifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		t.Fatalf("read updated manifest: %v", err)
	}
	if updatedManifest.PublicationState != "ci_waiting" || updatedManifest.PublicationDetail != "check_runs_unavailable" || updatedManifest.PublicationError != "" {
		t.Fatalf("expected persisted waiting publication detail, got %+v", updatedManifest)
	}
}

func TestStartWorkPromoteCreatesUpstreamPRForMergedForkPR(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	state := startWorkState{Version: startWorkStateVersion, SourceRepo: "acme/widget", ForkRepo: "me/widget", ForkOwner: "me", DefaultBranch: "main", UpdatedAt: "now", Issues: map[string]startWorkIssueState{}, Promotions: map[string]startWorkPromotion{}}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/me/widget/pulls?state=closed&per_page=100&page=1":
			_, _ = w.Write([]byte(`[{"number":7,"title":"Fix bug","html_url":"https://github.com/me/widget/pull/7","merged_at":"2026-04-12T00:00:00Z","head":{"ref":"nana/issue-1/fix"},"base":{"ref":"main"}}]`))
		case "GET /repos/acme/widget/pulls?state=open&head=me%3Anana%2Fissue-1%2Ffix&base=main":
			_, _ = w.Write([]byte(`[]`))
		case "POST /repos/acme/widget/pulls?":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatalf("decode upstream PR payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"number":44,"html_url":"https://github.com/acme/widget/pull/44"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	if err := StartWork(".", []string{"promote", "acme/widget"}); err != nil {
		t.Fatalf("StartWork(promote): %v", err)
	}
	if posted["head"] != "me:nana/issue-1/fix" || posted["base"] != "main" {
		t.Fatalf("unexpected upstream PR payload: %#v", posted)
	}
	updated, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if updated.Promotions["7"].UpstreamPRNumber != 44 {
		t.Fatalf("expected promotion state, got %+v", updated.Promotions)
	}
}

func TestStartWorkPromoteRecordsClosedUnmergedForkPR(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	state := startWorkState{Version: startWorkStateVersion, SourceRepo: "acme/widget", ForkRepo: "me/widget", ForkOwner: "me", DefaultBranch: "main", UpdatedAt: "now", Issues: map[string]startWorkIssueState{}, Promotions: map[string]startWorkPromotion{}, PromotionSkips: map[string]startWorkPromotionSkip{}}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/me/widget/pulls?state=closed&per_page=100&page=1":
			_, _ = w.Write([]byte(`[{"number":7,"title":"Closed","html_url":"https://github.com/me/widget/pull/7","head":{"ref":"nana/issue-7/fix"},"base":{"ref":"main"}}]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	if err := StartWork(".", []string{"promote", "acme/widget"}); err != nil {
		t.Fatalf("StartWork(promote): %v", err)
	}
	updated, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if got := updated.PromotionSkips["7"].Reason; got != "fork PR closed without merge" {
		t.Fatalf("expected closed-without-merge skip, got %+v", updated.PromotionSkips)
	}
}

func TestStartWorkPromoteAutoCreatesUpstreamPRForOpenForkPR(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	state := startWorkState{Version: startWorkStateVersion, SourceRepo: "acme/widget", ForkRepo: "me/widget", ForkOwner: "me", DefaultBranch: "main", UpdatedAt: "now", Issues: map[string]startWorkIssueState{}, Promotions: map[string]startWorkPromotion{}}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath("acme/widget"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	posted := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/me/widget/pulls?state=open&per_page=100&page=1":
			_, _ = w.Write([]byte(`[{"number":8,"title":"Fix fast","state":"open","html_url":"https://github.com/me/widget/pull/8","head":{"ref":"nana/issue-2/fix","sha":"head-sha"},"base":{"ref":"main"}}]`))
		case "GET /repos/me/widget/commits/head-sha/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[{"status":"completed","conclusion":"success"}]}`))
		case "GET /repos/me/widget/actions/runs?head_sha=head-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "GET /repos/acme/widget/pulls?state=open&head=me%3Anana%2Fissue-2%2Ffix&base=main":
			_, _ = w.Write([]byte(`[]`))
		case "POST /repos/acme/widget/pulls?":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatalf("decode upstream PR payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"number":45,"html_url":"https://github.com/acme/widget/pull/45"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	if err := StartWork(".", []string{"promote", "acme/widget"}); err != nil {
		t.Fatalf("StartWork(promote auto): %v", err)
	}
	if posted["head"] != "me:nana/issue-2/fix" {
		t.Fatalf("unexpected upstream PR payload: %#v", posted)
	}
}

func TestStartWorkPromoteAutoSkipsUnsafeForkPRs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	state := startWorkState{Version: startWorkStateVersion, SourceRepo: "acme/widget", ForkRepo: "me/widget", ForkOwner: "me", DefaultBranch: "main", UpdatedAt: "now", Issues: map[string]startWorkIssueState{}, Promotions: map[string]startWorkPromotion{}, PromotionSkips: map[string]startWorkPromotionSkip{"8": {ForkPRNumber: 8, Reason: "old skip"}}}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath("acme/widget"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	postedHeads := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/me/widget/pulls?state=open&per_page=100&page=1":
			_, _ = w.Write([]byte(`[
{"number":8,"title":"Draft","state":"open","draft":true,"html_url":"https://github.com/me/widget/pull/8","head":{"ref":"nana/issue-8/fix","sha":"draft-sha"},"base":{"ref":"main"}},
{"number":9,"title":"Failing","state":"open","html_url":"https://github.com/me/widget/pull/9","head":{"ref":"nana/issue-9/fix","sha":"fail-sha"},"base":{"ref":"main"}},
{"number":10,"title":"No CI","state":"open","html_url":"https://github.com/me/widget/pull/10","head":{"ref":"nana/issue-10/fix","sha":"no-ci-sha"},"base":{"ref":"main"}},
{"number":11,"title":"Pending","state":"open","html_url":"https://github.com/me/widget/pull/11","head":{"ref":"nana/issue-11/fix","sha":"pending-sha"},"base":{"ref":"main"}}
]`))
		case "GET /repos/acme/widget/pulls?state=open&head=me%3Anana%2Fissue-10%2Ffix&base=main":
			_, _ = w.Write([]byte(`[]`))
		case "POST /repos/acme/widget/pulls?":
			var payload struct {
				Head string `json:"head"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode upstream PR payload: %v", err)
			}
			postedHeads = append(postedHeads, payload.Head)
			_, _ = w.Write([]byte(`{"number":45,"html_url":"https://github.com/acme/widget/pull/45"}`))
		case "GET /repos/me/widget/commits/fail-sha/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[{"status":"completed","conclusion":"failure"}]}`))
		case "GET /repos/me/widget/actions/runs?head_sha=fail-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "GET /repos/me/widget/commits/no-ci-sha/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case "GET /repos/me/widget/actions/runs?head_sha=no-ci-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "GET /repos/me/widget/commits/pending-sha/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[{"status":"queued"}]}`))
		case "GET /repos/me/widget/actions/runs?head_sha=pending-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error { return StartWork(".", []string{"promote", "acme/widget"}) })
	if err != nil {
		t.Fatalf("StartWork(promote auto): %v\n%s", err, output)
	}
	updated, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(updated.Promotions) != 1 {
		t.Fatalf("expected only no-CI PR to promote, got %+v", updated.Promotions)
	}
	if got := updated.Promotions["10"].UpstreamPRNumber; got != 45 {
		t.Fatalf("expected no-CI PR to promote upstream, got %+v", updated.Promotions)
	}
	for key, want := range map[string]string{"8": "fork PR is draft", "9": "fork PR CI is not green: ci_failed", "11": "fork PR CI is not green: ci_pending"} {
		if got := updated.PromotionSkips[key].Reason; got != want {
			t.Fatalf("skip %s reason = %q, want %q; state=%+v", key, got, want, updated.PromotionSkips)
		}
	}
	if _, ok := updated.PromotionSkips["10"]; ok {
		t.Fatalf("did not expect no-CI PR to remain skipped: %+v", updated.PromotionSkips)
	}
	if len(postedHeads) != 1 || postedHeads[0] != "me:nana/issue-10/fix" {
		t.Fatalf("expected only no-CI PR to open upstream PR, got %+v", postedHeads)
	}
	if !strings.Contains(output, "Skipped upstream PRs") {
		t.Fatalf("expected skipped output, got %q", output)
	}
}

func TestStartWorkPromoteAutoReusesExistingUpstreamPR(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	state := startWorkState{Version: startWorkStateVersion, SourceRepo: "acme/widget", ForkRepo: "me/widget", ForkOwner: "me", DefaultBranch: "main", UpdatedAt: "now", Issues: map[string]startWorkIssueState{}, Promotions: map[string]startWorkPromotion{}, PromotionSkips: map[string]startWorkPromotionSkip{"8": {ForkPRNumber: 8, Reason: "old skip"}}}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath("acme/widget"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/me/widget/pulls?state=open&per_page=100&page=1":
			_, _ = w.Write([]byte(`[{"number":8,"title":"Fix fast","state":"open","html_url":"https://github.com/me/widget/pull/8","head":{"ref":"nana/issue-2/fix","sha":"head-sha"},"base":{"ref":"main"}}]`))
		case "GET /repos/me/widget/commits/head-sha/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[{"status":"completed","conclusion":"success"}]}`))
		case "GET /repos/me/widget/actions/runs?head_sha=head-sha&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "GET /repos/acme/widget/pulls?state=open&head=me%3Anana%2Fissue-2%2Ffix&base=main":
			_, _ = w.Write([]byte(`[{"number":45,"html_url":"https://github.com/acme/widget/pull/45"}]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	if err := StartWork(".", []string{"promote", "acme/widget"}); err != nil {
		t.Fatalf("StartWork(promote auto): %v", err)
	}
	updated, err := readStartWorkState("acme/widget")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if got := updated.Promotions["8"]; got.UpstreamPRNumber != 45 || !got.Reused {
		t.Fatalf("expected reused promotion to upstream #45, got %+v", got)
	}
	if _, ok := updated.PromotionSkips["8"]; ok {
		t.Fatalf("expected stale promotion skip to be cleared, got %+v", updated.PromotionSkips)
	}
}

func slicesContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func TestStartWorkStatusJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: "acme/widget",
		ForkRepo:   "me/widget",
		UpdatedAt:  "now",
		Issues:     map[string]startWorkIssueState{"1": {SourceNumber: 1, Status: startWorkStatusQueued}},
		ServiceTasks: map[string]startWorkServiceTask{
			"reconcile:1": {
				ID:            "reconcile:1",
				Kind:          startTaskKindReconcile,
				Queue:         startTaskQueueService,
				Status:        startWorkServiceTaskQueued,
				IssueKey:      "1",
				ResultSummary: "waiting",
				LastError:     "waiting for publication state",
				WaitCycle:     "cycle-1",
			},
		},
		PromotionSkips: map[string]startWorkPromotionSkip{"7": {ForkPRNumber: 7, Reason: "fork PR CI is not green: ci_pending"}},
	}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	output, err := captureStdout(t, func() error { return StartWork(".", []string{"status", "acme/widget", "--json"}) })
	if err != nil {
		t.Fatalf("StartWork(status): %v", err)
	}
	var payload struct {
		SourceRepo          string `json:"source_repo"`
		WaitingServiceTasks []struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
			Count  int    `json:"count"`
		} `json:"waiting_service_tasks"`
		ServiceTaskDetail []struct {
			Kind      string `json:"kind"`
			Queued    int    `json:"queued"`
			Running   int    `json:"running"`
			Completed int    `json:"completed"`
			Failed    int    `json:"failed"`
		} `json:"service_task_detail"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal start status json: %v\n%s", err, output)
	}
	if payload.SourceRepo != "acme/widget" || len(payload.WaitingServiceTasks) != 1 || payload.WaitingServiceTasks[0].Kind != "reconcile" || payload.WaitingServiceTasks[0].Reason != "waiting for publication state" || payload.WaitingServiceTasks[0].Count != 1 {
		t.Fatalf("unexpected start status payload: %+v", payload)
	}
	if len(payload.ServiceTaskDetail) != 1 || payload.ServiceTaskDetail[0].Kind != "reconcile" || payload.ServiceTaskDetail[0].Queued != 1 {
		t.Fatalf("unexpected service task detail payload: %+v", payload.ServiceTaskDetail)
	}
	if strings.Contains(output, `"wait_cycle"`) {
		t.Fatalf("did not expect internal wait_cycle marker in status JSON, got %q", output)
	}
	textOutput, err := captureStdout(t, func() error { return StartWork(".", []string{"status", "acme/widget"}) })
	if err != nil {
		t.Fatalf("StartWork(status text): %v", err)
	}
	if !strings.Contains(textOutput, "Forwarding: promoted=0 reused=0 active_skips=1") || !strings.Contains(textOutput, "Forward skips: fork PR #7: fork PR CI is not green: ci_pending") {
		t.Fatalf("expected forward skip text, got %q", textOutput)
	}
	if !strings.Contains(textOutput, "Waiting service tasks: reconcile: waiting for publication state (1)") {
		t.Fatalf("expected waiting service task text, got %q", textOutput)
	}
	if _, err := os.Stat(filepath.Join(home, ".nana", "work", "repos", "acme", "widget", "start-state.json")); err != nil {
		t.Fatalf("expected state file: %v", err)
	}
}

func TestStartWorkStartFailsClearlyWhenForkIssuesCannotBeEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /user?":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case "GET /repos/acme/widget?", "GET /repos/me/widget?":
			_, _ = w.Write([]byte(`{"name":"widget","full_name":"me/widget","default_branch":"main"}`))
		case "PATCH /repos/me/widget?":
			http.Error(w, `{"message":"issues disabled by policy"}`, http.StatusForbidden)
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)
	err := StartWork(".", []string{"start", "acme/widget"})
	if err == nil || !strings.Contains(err.Error(), "could not enable") || !strings.Contains(err.Error(), "Enable Issues in the fork settings") {
		t.Fatalf("expected clear issues enable failure, got %v", err)
	}
}

func TestStartWorkStartFailsClearlyWhenForkActionsCannotBeEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /user?":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case "GET /repos/acme/widget?", "GET /repos/me/widget?":
			_, _ = w.Write([]byte(`{"name":"widget","full_name":"me/widget","default_branch":"main"}`))
		case "PATCH /repos/me/widget?":
			_, _ = w.Write([]byte(`{"full_name":"me/widget","has_issues":true}`))
		case "PUT /repos/me/widget/actions/permissions?":
			http.Error(w, `{"message":"actions disabled by policy"}`, http.StatusForbidden)
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)
	err := StartWork(".", []string{"start", "acme/widget"})
	if err == nil || !strings.Contains(err.Error(), "GitHub Actions") || !strings.Contains(err.Error(), "Enable Actions in the fork settings") {
		t.Fatalf("expected clear actions enable failure, got %v", err)
	}
}
