package gocli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
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
{"number":1,"title":"Hard P2","body":"hard body","state":"open","html_url":"https://github.com/acme/widget/issues/1","labels":[{"name":"P2"},{"name":"hard"}]},
{"number":2,"title":"Easy P1","body":"easy body","state":"open","html_url":"https://github.com/acme/widget/issues/2","labels":[{"name":"P1"},{"name":"easy"}]},
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
	for _, role := range []string{improvementScoutRole, enhancementScoutRole} {
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

func TestStartRepoCoordinatorBuildsSeparateServiceQueueWithDependencies(t *testing.T) {
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		cycleID:  "cycle-1",
		workOptions: startWorkOptions{
			ImplementMode: "labeled",
		},
		state: &startWorkState{
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

func TestReadStartWorkStateRequeuesRunningServiceTasks(t *testing.T) {
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
	if loaded.ServiceTasks["triage:1"].Status != startWorkServiceTaskQueued {
		t.Fatalf("expected running service task to requeue, got %+v", loaded.ServiceTasks["triage:1"])
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
	coordinator := &startRepoCoordinator{
		repoSlug: "acme/widget",
		state: &startWorkState{
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
		},
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
	if got := coordinator.state.Issues["1"].Status; got != startWorkStatusReconciling {
		t.Fatalf("expected issue to stay reconciling, got %s", got)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery {
		case "GET /repos/me/widget/pulls?state=open&per_page=100&page=1":
			_, _ = w.Write([]byte(`[
{"number":8,"title":"Draft","state":"open","draft":true,"html_url":"https://github.com/me/widget/pull/8","head":{"ref":"nana/issue-8/fix","sha":"draft-sha"},"base":{"ref":"main"}},
{"number":9,"title":"Failing","state":"open","html_url":"https://github.com/me/widget/pull/9","head":{"ref":"nana/issue-9/fix","sha":"fail-sha"},"base":{"ref":"main"}},
{"number":10,"title":"No CI","state":"open","html_url":"https://github.com/me/widget/pull/10","head":{"ref":"nana/issue-10/fix","sha":"no-ci-sha"},"base":{"ref":"main"}},
{"number":11,"title":"Pending","state":"open","html_url":"https://github.com/me/widget/pull/11","head":{"ref":"nana/issue-11/fix","sha":"pending-sha"},"base":{"ref":"main"}}
]`))
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
	if len(updated.Promotions) != 0 {
		t.Fatalf("expected no promotions, got %+v", updated.Promotions)
	}
	for key, want := range map[string]string{"8": "fork PR is draft", "9": "fork PR CI is not green: ci_failed", "10": "fork PR CI is not green: no_ci_found", "11": "fork PR CI is not green: ci_pending"} {
		if got := updated.PromotionSkips[key].Reason; got != want {
			t.Fatalf("skip %s reason = %q, want %q; state=%+v", key, got, want, updated.PromotionSkips)
		}
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
	state := startWorkState{Version: startWorkStateVersion, SourceRepo: "acme/widget", ForkRepo: "me/widget", UpdatedAt: "now", Issues: map[string]startWorkIssueState{"1": {SourceNumber: 1, Status: startWorkStatusQueued}}, PromotionSkips: map[string]startWorkPromotionSkip{"7": {ForkPRNumber: 7, Reason: "fork PR CI is not green: no_ci_found"}}}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	output, err := captureStdout(t, func() error { return StartWork(".", []string{"status", "acme/widget", "--json"}) })
	if err != nil {
		t.Fatalf("StartWork(status): %v", err)
	}
	if !strings.Contains(output, `"source_repo":"acme/widget"`) || !strings.Contains(output, `"promotion_skips"`) {
		t.Fatalf("expected JSON status, got %q", output)
	}
	textOutput, err := captureStdout(t, func() error { return StartWork(".", []string{"status", "acme/widget"}) })
	if err != nil {
		t.Fatalf("StartWork(status text): %v", err)
	}
	if !strings.Contains(textOutput, "Forwarding: promoted=0 reused=0 active_skips=1") || !strings.Contains(textOutput, "Forward skips: fork PR #7: fork PR CI is not green: no_ci_found") {
		t.Fatalf("expected forward skip text, got %q", textOutput)
	}
	if _, err := os.Stat(filepath.Join(home, ".nana", "start", "acme", "widget", "state.json")); err != nil {
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
