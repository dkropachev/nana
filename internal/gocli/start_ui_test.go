package gocli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startUITestSetOriginRemote(t *testing.T, repo string, remote string) {
	t.Helper()
	cmd := exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git remote add origin failed: %v\n%s", err, output)
	}
}

func TestStartUIAPIOverviewAndMutations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "team-state.json"), []byte(`{"active":true,"current_phase":"team-exec","agent_count":2}`), 0o644); err != nil {
		t.Fatalf("write team-state: %v", err)
	}
	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, improvementScoutRole, false), improvementPolicy{
		Version:          1,
		Mode:             "auto",
		Schedule:         scoutScheduleWeekly,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{"ux"},
		MaxIssues:        7,
	}); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, enhancementScoutRole, false), improvementPolicy{
		Version:          1,
		Mode:             "manual",
		Schedule:         scoutScheduleDaily,
		IssueDestination: improvementDestinationFork,
		ForkRepo:         "me/widget",
		Labels:           []string{"forward"},
		MaxIssues:        2,
	}); err != nil {
		t.Fatalf("write enhancement policy: %v", err)
	}
	if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, uiScoutRole, false), improvementPolicy{
		Version:      1,
		Mode:         "manual",
		Schedule:     scoutScheduleWhenResolved,
		Labels:       []string{"qa"},
		MaxIssues:    3,
		SessionLimit: 5,
	}); err != nil {
		t.Fatalf("write ui policy: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		ForkRepo:   "me/widget",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Issues: map[string]startWorkIssueState{
			"7": {
				SourceNumber:      7,
				ForkNumber:        107,
				SourceURL:         "https://github.com/acme/widget/issues/7",
				State:             "open",
				Title:             "Fix flaky test",
				Status:            startWorkStatusQueued,
				Labels:            []string{"nana"},
				Priority:          3,
				PrioritySource:    "triage",
				Complexity:        2,
				SourceFingerprint: "fp-7",
				TriageFingerprint: "fp-7",
				TriageStatus:      startWorkTriageCompleted,
				UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
			},
		},
		ServiceTasks: map[string]startWorkServiceTask{
			"triage:7": {ID: "triage:7", Kind: startTaskKindTriage, Queue: startTaskQueueService, Status: startWorkServiceTaskQueued, IssueKey: "7", UpdatedAt: time.Now().UTC().Format(time.RFC3339)},
		},
		PlannedItems: map[string]startWorkPlannedItem{
			"planned-1": {
				ID:        "planned-1",
				RepoSlug:  "acme/widget",
				Title:     "Warm the staging environment",
				Priority:  2,
				State:     startPlannedItemQueued,
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("open work db: %v", err)
	}
	defer store.Close()
	if err := store.writeManifest(localWorkManifest{
		RunID:           "lw-ui",
		RepoRoot:        filepath.Join(cwd, "repo"),
		RepoName:        "widget",
		RepoID:          "repo-ui",
		CreatedAt:       time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		Status:          "running",
		CurrentPhase:    "verify",
		SandboxPath:     filepath.Join(home, "sandbox"),
		SandboxRepoPath: filepath.Join(home, "sandbox", "repo"),
	}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/overview")
	if err != nil {
		t.Fatalf("GET overview: %v", err)
	}
	defer response.Body.Close()
	var overview startUIOverview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(overview.Repos) != 1 || overview.Totals.ActiveWorkRuns != 1 || overview.HUD.Team == nil || overview.HUD.Team.AgentCount != 2 {
		t.Fatalf("unexpected overview payload: %+v", overview)
	}
	if len(overview.ScoutCatalog) != len(supportedScoutRoleOrder) {
		t.Fatalf("expected scout catalog in overview, got %+v", overview.ScoutCatalog)
	}
	if len(overview.WorkRuns) != 1 || !overview.WorkRuns[0].Pending || overview.WorkRuns[0].RepoLabel == "" {
		t.Fatalf("unexpected work run summary: %+v", overview.WorkRuns)
	}
	if !overview.Repos[0].StartParticipation || overview.Repos[0].ForkIssuesMode != "auto" || overview.Repos[0].PublishTarget != "fork" {
		t.Fatalf("unexpected repo config summary: %+v", overview.Repos[0])
	}
	if !overview.Repos[0].Scouts.Improvement.Enabled || overview.Repos[0].Scouts.Improvement.MaxIssues != 7 || overview.Repos[0].Scouts.Improvement.Schedule != scoutScheduleWeekly {
		t.Fatalf("unexpected improvement scout summary: %+v", overview.Repos[0].Scouts.Improvement)
	}
	if strings.HasPrefix(overview.Repos[0].Scouts.Improvement.PolicyPath, sourcePath+string(filepath.Separator)) {
		t.Fatalf("expected managed scout policy path outside source checkout, got %+v", overview.Repos[0].Scouts.Improvement)
	}
	if !overview.Repos[0].Scouts.Enhancement.Enabled || overview.Repos[0].Scouts.Enhancement.IssueDestination != "fork" || overview.Repos[0].Scouts.Enhancement.ForkRepo != "me/widget" || overview.Repos[0].Scouts.Enhancement.Schedule != scoutScheduleDaily {
		t.Fatalf("unexpected enhancement scout summary: %+v", overview.Repos[0].Scouts.Enhancement)
	}
	if !overview.Repos[0].Scouts.UI.Enabled || overview.Repos[0].Scouts.UI.SessionLimit != 5 || overview.Repos[0].Scouts.UI.MaxIssues != 3 || overview.Repos[0].Scouts.UI.Schedule != scoutScheduleWhenResolved {
		t.Fatalf("unexpected ui scout summary: %+v", overview.Repos[0].Scouts.UI)
	}
	if overview.Repos[0].ScoutsByRole["ui"].SessionLimit != 5 || overview.Repos[0].ScoutsByRole["enhancement"].IssueDestination != "fork" || overview.Repos[0].ScoutsByRole["improvement"].Schedule != scoutScheduleWeekly {
		t.Fatalf("expected role-keyed scout configs, got %+v", overview.Repos[0].ScoutsByRole)
	}

	settingsBody := strings.NewReader(`{"repo_mode":"repo","issue_pick_mode":"label","pr_forward_mode":"auto","fork_issues_mode":"labeled","implement_mode":"auto","publish_target":"repo","scouts":{"improvement":{"enabled":true,"mode":"manual","schedule":"daily","issue_destination":"repo","fork_repo":"","labels":["ux"],"max_issues":4},"enhancement":{"enabled":false,"mode":"auto","schedule":"always","issue_destination":"local","fork_repo":"","labels":[],"max_issues":5},"ui":{"enabled":true,"mode":"manual","schedule":"weekly","issue_destination":"local","fork_repo":"","labels":["qa"],"max_issues":3,"session_limit":6}}}`)
	settingsRequest, err := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/repos/"+repoSlug+"/settings", settingsBody)
	if err != nil {
		t.Fatalf("new settings request: %v", err)
	}
	settingsRequest.Header.Set("Content-Type", "application/json")
	settingsResponse, err := http.DefaultClient.Do(settingsRequest)
	if err != nil {
		t.Fatalf("PATCH settings: %v", err)
	}
	defer settingsResponse.Body.Close()
	var settingsPayload struct {
		Repo startUIRepoSummary `json:"repo"`
	}
	if err := json.NewDecoder(settingsResponse.Body).Decode(&settingsPayload); err != nil {
		t.Fatalf("decode settings payload: %v", err)
	}
	if settingsPayload.Repo.RepoMode != "repo" || settingsPayload.Repo.IssuePickMode != "label" || settingsPayload.Repo.PRForwardMode != "auto" || settingsPayload.Repo.ForkIssuesMode != "labeled" || settingsPayload.Repo.ImplementMode != "auto" || settingsPayload.Repo.PublishTarget != "repo" || !settingsPayload.Repo.StartParticipation {
		t.Fatalf("unexpected settings payload: %+v", settingsPayload.Repo)
	}
	if !settingsPayload.Repo.Scouts.Improvement.Enabled || settingsPayload.Repo.Scouts.Improvement.IssueDestination != "repo" || settingsPayload.Repo.Scouts.Improvement.MaxIssues != 4 || settingsPayload.Repo.Scouts.Improvement.Schedule != scoutScheduleDaily {
		t.Fatalf("unexpected patched improvement scout: %+v", settingsPayload.Repo.Scouts.Improvement)
	}
	if settingsPayload.Repo.Scouts.Enhancement.Enabled {
		t.Fatalf("expected enhancement scout to be disabled, got %+v", settingsPayload.Repo.Scouts.Enhancement)
	}
	if !settingsPayload.Repo.Scouts.UI.Enabled || settingsPayload.Repo.Scouts.UI.SessionLimit != 6 || settingsPayload.Repo.Scouts.UI.MaxIssues != 3 || settingsPayload.Repo.Scouts.UI.Schedule != scoutScheduleWeekly {
		t.Fatalf("unexpected patched ui scout: %+v", settingsPayload.Repo.Scouts.UI)
	}
	if settingsPayload.Repo.ScoutsByRole["ui"].SessionLimit != 6 || settingsPayload.Repo.ScoutsByRole["improvement"].Schedule != scoutScheduleDaily {
		t.Fatalf("expected patched role-keyed ui scout config: %+v", settingsPayload.Repo.ScoutsByRole)
	}
	if fileExists(filepath.Join(sourcePath, ".nana", "improvement-policy.json")) {
		t.Fatalf("expected legacy improvement policy to be removed after managed save")
	}
	if fileExists(filepath.Join(sourcePath, ".github", "nana-enhancement-policy.json")) {
		t.Fatalf("expected legacy enhancement scout policies to be removed after disable")
	}
	var patchedImprovementPolicy improvementPolicy
	if err := readGithubJSON(repoScoutPolicyPath(sourcePath, improvementScoutRole, false), &patchedImprovementPolicy); err != nil {
		t.Fatalf("read patched improvement policy: %v", err)
	}
	if patchedImprovementPolicy.Mode != "manual" || patchedImprovementPolicy.Schedule != scoutScheduleDaily || patchedImprovementPolicy.IssueDestination != improvementDestinationTarget || patchedImprovementPolicy.MaxIssues != 4 {
		t.Fatalf("unexpected patched improvement policy: %+v", patchedImprovementPolicy)
	}
	var patchedUIPolicy improvementPolicy
	if err := readGithubJSON(repoScoutPolicyPath(sourcePath, uiScoutRole, false), &patchedUIPolicy); err != nil {
		t.Fatalf("read patched ui policy: %v", err)
	}
	if patchedUIPolicy.Mode != "manual" || patchedUIPolicy.Schedule != scoutScheduleWeekly || patchedUIPolicy.MaxIssues != 3 || patchedUIPolicy.SessionLimit != 6 {
		t.Fatalf("unexpected patched ui policy: %+v", patchedUIPolicy)
	}

	disabledBody := strings.NewReader(`{"repo_mode":"disabled","issue_pick_mode":"manual","pr_forward_mode":"approve","fork_issues_mode":"manual","implement_mode":"manual","publish_target":""}`)
	disabledRequest, err := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/repos/"+repoSlug+"/settings", disabledBody)
	if err != nil {
		t.Fatalf("new disabled settings request: %v", err)
	}
	disabledRequest.Header.Set("Content-Type", "application/json")
	disabledResponse, err := http.DefaultClient.Do(disabledRequest)
	if err != nil {
		t.Fatalf("PATCH disabled settings: %v", err)
	}
	defer disabledResponse.Body.Close()
	var disabledPayload struct {
		Repo startUIRepoSummary `json:"repo"`
	}
	if err := json.NewDecoder(disabledResponse.Body).Decode(&disabledPayload); err != nil {
		t.Fatalf("decode disabled settings payload: %v", err)
	}
	if disabledPayload.Repo.RepoMode != "disabled" || disabledPayload.Repo.PublishTarget != "" || disabledPayload.Repo.StartParticipation {
		t.Fatalf("unexpected disabled settings payload: %+v", disabledPayload.Repo)
	}

	invalidSettingsBody := strings.NewReader(`{"repo_mode":"fork","issue_pick_mode":"bad","pr_forward_mode":"approve","fork_issues_mode":"manual","implement_mode":"manual","publish_target":"fork"}`)
	invalidSettingsRequest, err := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/repos/"+repoSlug+"/settings", invalidSettingsBody)
	if err != nil {
		t.Fatalf("new invalid settings request: %v", err)
	}
	invalidSettingsRequest.Header.Set("Content-Type", "application/json")
	invalidSettingsResponse, err := http.DefaultClient.Do(invalidSettingsRequest)
	if err != nil {
		t.Fatalf("PATCH invalid settings: %v", err)
	}
	defer invalidSettingsResponse.Body.Close()
	if invalidSettingsResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected bad request for invalid settings, got %d", invalidSettingsResponse.StatusCode)
	}

	patchBody := strings.NewReader(`{"priority":1,"schedule_at":"2026-04-14T15:00:00Z","deferred_reason":"wait for release train"}`)
	request, err := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/repos/"+repoSlug+"/issues/7", patchBody)
	if err != nil {
		t.Fatalf("new patch request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	patchResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PATCH issue: %v", err)
	}
	defer patchResponse.Body.Close()
	var patchPayload struct {
		Issue startWorkIssueState `json:"issue"`
	}
	if err := json.NewDecoder(patchResponse.Body).Decode(&patchPayload); err != nil {
		t.Fatalf("decode patch payload: %v", err)
	}
	if patchPayload.Issue.ManualPriority != 1 || patchPayload.Issue.ScheduleAt != "2026-04-14T15:00:00Z" {
		t.Fatalf("unexpected patched issue: %+v", patchPayload.Issue)
	}

	createBody := strings.NewReader(`{"title":"Run smoke after deploy","description":"Schedule it after release","priority":2,"launch_kind":"github_issue"}`)
	createResponse, err := http.Post(server.URL+"/api/v1/repos/"+repoSlug+"/planned-items", "application/json", createBody)
	if err != nil {
		t.Fatalf("POST planned item: %v", err)
	}
	defer createResponse.Body.Close()
	var createPayload struct {
		PlannedItem startWorkPlannedItem `json:"planned_item"`
	}
	if err := json.NewDecoder(createResponse.Body).Decode(&createPayload); err != nil {
		t.Fatalf("decode create payload: %v", err)
	}
	if createPayload.PlannedItem.ID == "" || createPayload.PlannedItem.Title != "Run smoke after deploy" {
		t.Fatalf("unexpected planned item payload: %+v", createPayload.PlannedItem)
	}

	oldLaunch := startLaunchPlannedItem
	startLaunchPlannedItem = func(cwd string, repoSlug string, workOptions startWorkOptions, item startWorkPlannedItem) (startPlannedLaunchResult, error) {
		return startPlannedLaunchResult{Status: "created_issue", Result: "created GitHub issue #77", IssueNumber: 77, IssueURL: "https://github.com/acme/widget/issues/77"}, nil
	}
	defer func() { startLaunchPlannedItem = oldLaunch }()

	launchRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/planned-items/"+createPayload.PlannedItem.ID+"/launch-now", nil)
	if err != nil {
		t.Fatalf("new launch request: %v", err)
	}
	launchResponse, err := http.DefaultClient.Do(launchRequest)
	if err != nil {
		t.Fatalf("POST launch-now: %v", err)
	}
	defer launchResponse.Body.Close()
	var launchPayload struct {
		PlannedItem startWorkPlannedItem     `json:"planned_item"`
		Launch      startPlannedLaunchResult `json:"launch"`
	}
	if err := json.NewDecoder(launchResponse.Body).Decode(&launchPayload); err != nil {
		t.Fatalf("decode launch payload: %v", err)
	}
	if launchPayload.PlannedItem.State != startPlannedItemLaunched || launchPayload.Launch.IssueNumber != 77 {
		t.Fatalf("unexpected launch payload: %+v", launchPayload)
	}
}

func TestStartUIAPIUsage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home", "sessions"), usageRolloutFixture{
		SessionID:      "usage-main",
		Timestamp:      "2026-04-15T12:00:00Z",
		CWD:            cwd,
		Model:          "gpt-5.4",
		TokenSnapshots: []usageTokenSnapshot{{Input: 100, CachedInput: 25, Output: 10, ReasoningOutput: 5, Total: 140}},
	})
	writeUsageRollout(t, filepath.Join(home, ".nana", "work", "sandboxes", "repo-1", "lw-1", ".nana", "work", "codex-home", "leader", "sessions"), usageRolloutFixture{
		SessionID:      "usage-work",
		Timestamp:      "2026-04-15T13:00:00Z",
		CWD:            filepath.Join(home, ".nana", "work", "sandboxes", "repo-1", "lw-1", "repo"),
		Model:          "gpt-5.4",
		AgentRole:      "leader",
		TokenSnapshots: []usageTokenSnapshot{{Input: 200, Output: 20, Total: 220}},
	})

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/usage?root=all")
	if err != nil {
		t.Fatalf("GET usage: %v", err)
	}
	defer response.Body.Close()

	var payload startUIUsageReport
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode usage payload: %v", err)
	}
	if payload.Summary.Totals.TotalTokens != 360 || payload.Summary.Totals.Sessions != 2 {
		t.Fatalf("unexpected usage summary: %+v", payload.Summary.Totals)
	}
	if len(payload.ByRoot) != 2 || payload.ByRoot[0].Key != "work" {
		t.Fatalf("unexpected root breakdown: %+v", payload.ByRoot)
	}
	if len(payload.TopSessions) != 2 || payload.TopSessions[0].SessionID != "usage-work" {
		t.Fatalf("unexpected top sessions: %+v", payload.TopSessions)
	}
}

func TestStartUIAPIDropRepoRemovesRepoFromOverview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	repoSlug := "acme/widget"
	paths := githubManagedPaths(repoSlug)
	sourcePath := createLocalWorkRepoAt(t, paths.SourcePath)
	if err := writeGithubJSON(paths.RepoSettingsPath, githubRepoSettings{
		Version:       6,
		RepoMode:      "fork",
		IssuePickMode: "auto",
		PRForwardMode: "approve",
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Issues:     map[string]startWorkIssueState{},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	overviewResponse, err := http.Get(server.URL + "/api/v1/overview")
	if err != nil {
		t.Fatalf("GET overview before drop: %v", err)
	}
	defer overviewResponse.Body.Close()
	var before startUIOverview
	if err := json.NewDecoder(overviewResponse.Body).Decode(&before); err != nil {
		t.Fatalf("decode overview before drop: %v", err)
	}
	if len(before.Repos) != 1 || before.Repos[0].RepoSlug != repoSlug {
		t.Fatalf("expected repo in overview before drop, got %+v", before.Repos)
	}

	dropRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/drop", nil)
	if err != nil {
		t.Fatalf("new repo drop request: %v", err)
	}
	dropResponse, err := http.DefaultClient.Do(dropRequest)
	if err != nil {
		t.Fatalf("POST repo drop: %v", err)
	}
	defer dropResponse.Body.Close()
	if dropResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected repo drop status: %d", dropResponse.StatusCode)
	}
	var dropPayload struct {
		RepoSlug string `json:"repo_slug"`
		Dropped  bool   `json:"dropped"`
	}
	if err := json.NewDecoder(dropResponse.Body).Decode(&dropPayload); err != nil {
		t.Fatalf("decode repo drop payload: %v", err)
	}
	if dropPayload.RepoSlug != repoSlug || !dropPayload.Dropped {
		t.Fatalf("unexpected repo drop payload: %+v", dropPayload)
	}

	for _, path := range []string{paths.RepoRoot, sourcePath, paths.StartStatePath, paths.RepoSettingsPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", path, err)
		}
	}

	afterResponse, err := http.Get(server.URL + "/api/v1/overview")
	if err != nil {
		t.Fatalf("GET overview after drop: %v", err)
	}
	defer afterResponse.Body.Close()
	var after startUIOverview
	if err := json.NewDecoder(afterResponse.Body).Decode(&after); err != nil {
		t.Fatalf("decode overview after drop: %v", err)
	}
	if len(after.Repos) != 0 || after.Totals.Repos != 0 {
		t.Fatalf("expected dropped repo to disappear from overview, got totals=%+v repos=%+v", after.Totals, after.Repos)
	}
}

func TestStartUIAPITrackedIssueSchedulerSearchAndMutations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	cwd := t.TempDir()
	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	if err := writeStartWorkState(startWorkState{
		Version:      startWorkStateVersion,
		SourceRepo:   repoSlug,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		Issues:       map[string]startWorkIssueState{},
		ServiceTasks: map[string]startWorkServiceTask{},
		PlannedItems: map[string]startWorkPlannedItem{},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/issues":
			query := r.URL.Query().Get("q")
			if !strings.Contains(query, "repo:acme/widget") || !strings.Contains(query, "is:issue") || !strings.Contains(query, "is:open") || !strings.Contains(query, "label:bug") {
				http.Error(w, "unexpected query: "+query, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"number":42,"title":"Fix flaky widget","body":"Body","state":"open","html_url":"https://github.com/acme/widget/issues/42","updated_at":"2026-04-15T12:00:00Z","labels":[{"name":"bug"},{"name":"P1"}]},{"number":43,"title":"Ignore pull request","body":"Body","state":"open","html_url":"https://github.com/acme/widget/pull/43","labels":[{"name":"P2"}],"pull_request":{}}]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer githubServer.Close()
	t.Setenv("GITHUB_API_URL", githubServer.URL)

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	searchBody := strings.NewReader(`{"query":"label:bug"}`)
	searchRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/issue-search", searchBody)
	if err != nil {
		t.Fatalf("new search request: %v", err)
	}
	searchRequest.Header.Set("Content-Type", "application/json")
	searchResponse, err := http.DefaultClient.Do(searchRequest)
	if err != nil {
		t.Fatalf("POST issue-search: %v", err)
	}
	defer searchResponse.Body.Close()
	var searchPayload startUIIssueSearchResponse
	if err := json.NewDecoder(searchResponse.Body).Decode(&searchPayload); err != nil {
		t.Fatalf("decode search payload: %v", err)
	}
	if len(searchPayload.Items) != 1 || searchPayload.Items[0].Number != 42 || searchPayload.Items[0].Scheduled {
		t.Fatalf("unexpected search payload: %+v", searchPayload)
	}
	if searchPayload.Items[0].PriorityLabel != "P1" {
		t.Fatalf("expected P1 priority label, got %+v", searchPayload.Items[0])
	}

	invalidSearchBody := strings.NewReader(`{"query":"repo:other/project"}`)
	invalidSearchRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/issue-search", invalidSearchBody)
	if err != nil {
		t.Fatalf("new invalid search request: %v", err)
	}
	invalidSearchRequest.Header.Set("Content-Type", "application/json")
	invalidSearchResponse, err := http.DefaultClient.Do(invalidSearchRequest)
	if err != nil {
		t.Fatalf("POST invalid issue-search: %v", err)
	}
	defer invalidSearchResponse.Body.Close()
	if invalidSearchResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected bad request for conflicting repo qualifier, got %d", invalidSearchResponse.StatusCode)
	}

	scheduleBody := strings.NewReader(`{"number":42,"title":"Fix flaky widget","target_url":"https://github.com/acme/widget/issues/42","labels":["bug","P1"]}`)
	scheduleRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/tracked-issues/schedule", scheduleBody)
	if err != nil {
		t.Fatalf("new tracked issue schedule request: %v", err)
	}
	scheduleRequest.Header.Set("Content-Type", "application/json")
	scheduleResponse, err := http.DefaultClient.Do(scheduleRequest)
	if err != nil {
		t.Fatalf("POST tracked issue schedule: %v", err)
	}
	defer scheduleResponse.Body.Close()
	var schedulePayload struct {
		State       *startWorkState      `json:"state"`
		PlannedItem startWorkPlannedItem `json:"planned_item"`
	}
	if err := json.NewDecoder(scheduleResponse.Body).Decode(&schedulePayload); err != nil {
		t.Fatalf("decode schedule payload: %v", err)
	}
	if schedulePayload.State == nil || schedulePayload.PlannedItem.ID == "" || schedulePayload.PlannedItem.LaunchKind != "tracked_issue" || schedulePayload.PlannedItem.ScheduleAt != "" {
		t.Fatalf("unexpected tracked issue planned item payload: %+v", schedulePayload)
	}

	rescheduleAt := "2026-04-16T09:00:00Z"
	upsertBody := strings.NewReader(fmt.Sprintf(`{"number":42,"title":"Fix flaky widget","target_url":"https://github.com/acme/widget/issues/42","labels":["bug","P1"],"priority":0,"schedule_at":%q}`, rescheduleAt))
	upsertRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/tracked-issues/schedule", upsertBody)
	if err != nil {
		t.Fatalf("new tracked issue upsert request: %v", err)
	}
	upsertRequest.Header.Set("Content-Type", "application/json")
	upsertResponse, err := http.DefaultClient.Do(upsertRequest)
	if err != nil {
		t.Fatalf("POST tracked issue upsert: %v", err)
	}
	defer upsertResponse.Body.Close()
	var upsertPayload struct {
		PlannedItem startWorkPlannedItem `json:"planned_item"`
	}
	if err := json.NewDecoder(upsertResponse.Body).Decode(&upsertPayload); err != nil {
		t.Fatalf("decode upsert payload: %v", err)
	}
	if upsertPayload.PlannedItem.ID != schedulePayload.PlannedItem.ID || upsertPayload.PlannedItem.Priority != 0 || upsertPayload.PlannedItem.ScheduleAt != rescheduleAt {
		t.Fatalf("expected tracked issue upsert to reuse planned item, got %+v", upsertPayload.PlannedItem)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	item := state.PlannedItems[schedulePayload.PlannedItem.ID]
	item.State = startPlannedItemFailed
	item.LastError = "launch failed"
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	state.PlannedItems[item.ID] = item
	state.UpdatedAt = item.UpdatedAt
	if err := writeStartWorkState(*state); err != nil {
		t.Fatalf("write failed planned item state: %v", err)
	}

	patchBody := strings.NewReader(`{"title":"Implement tracked issue #42: Fix flaky widget immediately","priority":2,"clear_schedule":true}`)
	patchRequest, err := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/planned-items/"+schedulePayload.PlannedItem.ID, patchBody)
	if err != nil {
		t.Fatalf("new planned item patch request: %v", err)
	}
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse, err := http.DefaultClient.Do(patchRequest)
	if err != nil {
		t.Fatalf("PATCH planned item: %v", err)
	}
	defer patchResponse.Body.Close()
	var patchPayload struct {
		PlannedItem startWorkPlannedItem `json:"planned_item"`
	}
	if err := json.NewDecoder(patchResponse.Body).Decode(&patchPayload); err != nil {
		t.Fatalf("decode patch payload: %v", err)
	}
	if patchPayload.PlannedItem.State != startPlannedItemQueued || patchPayload.PlannedItem.LastError != "" || patchPayload.PlannedItem.ScheduleAt != "" || patchPayload.PlannedItem.Priority != 2 {
		t.Fatalf("expected patch to requeue failed item, got %+v", patchPayload.PlannedItem)
	}

	searchAgainBody := strings.NewReader(`{"query":"label:bug"}`)
	searchAgainRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/issue-search", searchAgainBody)
	if err != nil {
		t.Fatalf("new repeated search request: %v", err)
	}
	searchAgainRequest.Header.Set("Content-Type", "application/json")
	searchAgainResponse, err := http.DefaultClient.Do(searchAgainRequest)
	if err != nil {
		t.Fatalf("POST repeated issue-search: %v", err)
	}
	defer searchAgainResponse.Body.Close()
	var searchAgainPayload startUIIssueSearchResponse
	if err := json.NewDecoder(searchAgainResponse.Body).Decode(&searchAgainPayload); err != nil {
		t.Fatalf("decode repeated search payload: %v", err)
	}
	if len(searchAgainPayload.Items) != 1 || !searchAgainPayload.Items[0].Scheduled || searchAgainPayload.Items[0].PlannedItemID != schedulePayload.PlannedItem.ID {
		t.Fatalf("expected search result to report scheduled tracked issue, got %+v", searchAgainPayload.Items)
	}

	deleteRequest, err := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/planned-items/"+schedulePayload.PlannedItem.ID, nil)
	if err != nil {
		t.Fatalf("new planned item delete request: %v", err)
	}
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatalf("DELETE planned item: %v", err)
	}
	defer deleteResponse.Body.Close()
	var deletePayload struct {
		State *startWorkState `json:"state"`
	}
	if err := json.NewDecoder(deleteResponse.Body).Decode(&deletePayload); err != nil {
		t.Fatalf("decode delete payload: %v", err)
	}
	if deletePayload.State == nil {
		t.Fatalf("expected updated state after delete, got %+v", deletePayload)
	}
	if _, ok := deletePayload.State.PlannedItems[schedulePayload.PlannedItem.ID]; ok {
		t.Fatalf("expected planned item to be removed, got %+v", deletePayload.State.PlannedItems)
	}
}

func TestStartUIAPIWorkItems(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "comment-1",
		RepoSlug:   "acme/widget",
		TargetURL:  "https://github.com/acme/widget/pull/7",
		Subject:    "Refine work items",
		Body:       "Please reply in the thread.",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.LatestDraft = &workItemDraft{
		Kind:       "reply",
		Body:       "Draft reply",
		Summary:    "Reply summary",
		Confidence: 0.8,
	}
	item.Status = workItemStatusDraftReady
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
	store.Close()

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	listResponse, err := http.Get(server.URL + "/api/v1/work-items")
	if err != nil {
		t.Fatalf("GET work-items: %v", err)
	}
	defer listResponse.Body.Close()
	var listPayload struct {
		Items []startUIWorkItem `json:"items"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode work-items list: %v", err)
	}
	if len(listPayload.Items) != 1 || listPayload.Items[0].ID != item.ID || !listPayload.Items[0].Pending {
		t.Fatalf("unexpected work-items list: %+v", listPayload.Items)
	}

	detailResponse, err := http.Get(server.URL + "/api/v1/work-items/" + item.ID)
	if err != nil {
		t.Fatalf("GET work item detail: %v", err)
	}
	defer detailResponse.Body.Close()
	var detail workItemDetail
	if err := json.NewDecoder(detailResponse.Body).Decode(&detail); err != nil {
		t.Fatalf("decode work item detail: %v", err)
	}
	if detail.Item.ID != item.ID || detail.Item.LatestDraft == nil || detail.Item.LatestDraft.Body != "Draft reply" {
		t.Fatalf("unexpected work item detail: %+v", detail)
	}

	dropRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/work-items/"+item.ID+"/drop", nil)
	if err != nil {
		t.Fatalf("new drop request: %v", err)
	}
	dropResponse, err := http.DefaultClient.Do(dropRequest)
	if err != nil {
		t.Fatalf("POST drop: %v", err)
	}
	defer dropResponse.Body.Close()
	if dropResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected drop status: %d", dropResponse.StatusCode)
	}

	restoreRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/work-items/"+item.ID+"/restore", nil)
	if err != nil {
		t.Fatalf("new restore request: %v", err)
	}
	restoreResponse, err := http.DefaultClient.Do(restoreRequest)
	if err != nil {
		t.Fatalf("POST restore: %v", err)
	}
	defer restoreResponse.Body.Close()
	if restoreResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected restore status: %d", restoreResponse.StatusCode)
	}
}

func TestStartUIAPIDropLocalWorkRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	repoRoot := filepath.Join(cwd, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo root: %v", err)
	}
	manifest := localWorkManifest{
		Version:         4,
		RunID:           "lw-drop-ui",
		CreatedAt:       "2026-04-13T18:27:02Z",
		UpdatedAt:       "2026-04-13T18:46:31Z",
		Status:          "blocked",
		CurrentPhase:    "apply-blocked",
		RepoRoot:        repoRoot,
		RepoName:        "repo",
		RepoSlug:        "dkropachev/nana",
		RepoID:          "repo-drop-ui",
		SourceBranch:    "main",
		BaselineSHA:     "abc123",
		SandboxPath:     filepath.Join(localWorkSandboxesDir(), "repo-drop-ui", "lw-drop-ui"),
		SandboxRepoPath: filepath.Join(localWorkSandboxesDir(), "repo-drop-ui", "lw-drop-ui", "repo"),
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	if err := store.writeManifest(manifest); err != nil {
		_ = store.Close()
		t.Fatalf("write manifest: %v", err)
	}
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		_ = store.Close()
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "retrospective.md"), []byte("retrospective"), 0o644); err != nil {
		_ = store.Close()
		t.Fatalf("write run artifact: %v", err)
	}
	if err := os.MkdirAll(manifest.SandboxRepoPath, 0o755); err != nil {
		_ = store.Close()
		t.Fatalf("mkdir sandbox repo path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifest.SandboxRepoPath, "README.md"), []byte("sandbox"), 0o644); err != nil {
		_ = store.Close()
		t.Fatalf("write sandbox artifact: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO runtime_states(run_id, iteration, state_json) VALUES(?, ?, ?)`, manifest.RunID, 1, `{"iteration":1}`); err != nil {
		_ = store.Close()
		t.Fatalf("insert runtime state: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO finding_history(run_id, event_json) VALUES(?, ?)`, manifest.RunID, `{"event":"validated"}`); err != nil {
		_ = store.Close()
		t.Fatalf("insert finding history: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closeLocalWorkDB: %v", err)
	}

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "comment",
		ExternalID: "drop-run-local",
		RepoSlug:   manifest.RepoSlug,
		TargetURL:  "https://github.com/dkropachev/nana/issues/1",
		Subject:    "Drop local run",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err = openLocalWorkDB()
	if err != nil {
		t.Fatalf("reopenLocalWorkDB: %v", err)
	}
	item.LinkedRunID = manifest.RunID
	item.UpdatedAt = ISOTimeNow()
	if err := store.updateWorkItem(item); err != nil {
		_ = store.Close()
		t.Fatalf("updateWorkItem: %v", err)
	}
	if err := store.replaceWorkItemLinks(item.ID, buildDefaultWorkItemLinks(item)); err != nil {
		_ = store.Close()
		t.Fatalf("replaceWorkItemLinks: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closeLocalWorkDB after work item update: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	dropRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/work/runs/"+manifest.RunID+"/drop", nil)
	if err != nil {
		t.Fatalf("new drop run request: %v", err)
	}
	dropResponse, err := http.DefaultClient.Do(dropRequest)
	if err != nil {
		t.Fatalf("POST work run drop: %v", err)
	}
	defer dropResponse.Body.Close()
	if dropResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected work run drop status: %d", dropResponse.StatusCode)
	}

	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("expected local run dir to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(manifest.SandboxPath); !os.IsNotExist(err) {
		t.Fatalf("expected local sandbox to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(localWorkRepoDirByID(manifest.RepoID)); !os.IsNotExist(err) {
		t.Fatalf("expected empty local repo runtime dir to be pruned, stat err=%v", err)
	}

	store, err = openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB for verification: %v", err)
	}
	defer store.Close()
	var runCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE run_id = ?`, manifest.RunID).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("expected dropped local run to be removed from runs, got %d", runCount)
	}
	var indexCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM work_run_index WHERE run_id = ?`, manifest.RunID).Scan(&indexCount); err != nil {
		t.Fatalf("count work_run_index: %v", err)
	}
	if indexCount != 0 {
		t.Fatalf("expected dropped local run to be removed from work_run_index, got %d", indexCount)
	}
	var runtimeCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runtime_states WHERE run_id = ?`, manifest.RunID).Scan(&runtimeCount); err != nil {
		t.Fatalf("count runtime_states: %v", err)
	}
	if runtimeCount != 0 {
		t.Fatalf("expected dropped local run runtime state to be removed, got %d", runtimeCount)
	}
	var findingCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM finding_history WHERE run_id = ?`, manifest.RunID).Scan(&findingCount); err != nil {
		t.Fatalf("count finding_history: %v", err)
	}
	if findingCount != 0 {
		t.Fatalf("expected dropped local run finding history to be removed, got %d", findingCount)
	}
	updatedItem, err := store.readWorkItem(item.ID)
	if err != nil {
		t.Fatalf("read updated work item: %v", err)
	}
	if updatedItem.LinkedRunID != "" {
		t.Fatalf("expected linked run id to be cleared, got %+v", updatedItem)
	}
	links, err := store.readWorkItemLinks(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemLinks: %v", err)
	}
	for _, link := range links {
		if link.LinkType == "run" && link.TargetID == manifest.RunID {
			t.Fatalf("expected run link to be removed, got %+v", links)
		}
	}

	runDetailResponse, err := http.Get(server.URL + "/api/v1/work/runs/" + manifest.RunID)
	if err != nil {
		t.Fatalf("GET dropped work run detail: %v", err)
	}
	defer runDetailResponse.Body.Close()
	if runDetailResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("expected dropped work run detail status 404, got %d", runDetailResponse.StatusCode)
	}

	overviewResponse, err := http.Get(server.URL + "/api/v1/overview")
	if err != nil {
		t.Fatalf("GET overview after drop: %v", err)
	}
	defer overviewResponse.Body.Close()
	var overview startUIOverview
	if err := json.NewDecoder(overviewResponse.Body).Decode(&overview); err != nil {
		t.Fatalf("decode overview after drop: %v", err)
	}
	if len(overview.WorkRuns) != 0 || overview.Totals.ActiveWorkRuns != 0 {
		t.Fatalf("expected dropped local run to disappear from overview, got totals=%+v runs=%+v", overview.Totals, overview.WorkRuns)
	}
}

func TestStartUIAPIDropGithubWorkRunClearsPointers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	managedRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	runID := "gh-drop-ui"
	runDir := filepath.Join(managedRoot, "runs", runID)
	sandboxPath := filepath.Join(managedRoot, "sandboxes", "issue-42-"+runID)
	manifest := githubWorkManifest{
		RunID:            runID,
		RepoSlug:         "acme/widget",
		RepoOwner:        "acme",
		RepoName:         "widget",
		ManagedRepoRoot:  managedRoot,
		SourcePath:       filepath.Join(cwd, "source"),
		TargetURL:        "https://github.com/acme/widget/issues/42",
		TargetKind:       "issue",
		TargetNumber:     42,
		UpdatedAt:        "2026-04-13T17:00:00Z",
		SandboxPath:      sandboxPath,
		SandboxRepoPath:  filepath.Join(sandboxPath, "repo"),
		PublicationState: "blocked",
		NeedsHuman:       true,
		NeedsHumanReason: "approval required",
		NextAction:       "wait_for_github_feedback",
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write github manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index github manifest: %v", err)
	}
	if err := os.MkdirAll(manifest.SandboxRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir github sandbox repo path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifest.SandboxRepoPath, "README.md"), []byte("sandbox"), 0o644); err != nil {
		t.Fatalf("write github sandbox artifact: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(managedRoot, "latest-run.json"), githubLatestRunPointer{RepoRoot: managedRoot, RunID: runID}); err != nil {
		t.Fatalf("write repo latest run pointer: %v", err)
	}
	if err := writeGithubJSON(githubWorkLatestRunPath(), githubLatestRunPointer{RepoRoot: managedRoot, RunID: runID}); err != nil {
		t.Fatalf("write global latest run pointer: %v", err)
	}

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "comment",
		ExternalID: "drop-run-github",
		RepoSlug:   manifest.RepoSlug,
		TargetURL:  manifest.TargetURL,
		Subject:    "Drop github run",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.LinkedRunID = runID
	item.UpdatedAt = ISOTimeNow()
	if err := store.updateWorkItem(item); err != nil {
		_ = store.Close()
		t.Fatalf("updateWorkItem: %v", err)
	}
	if err := store.replaceWorkItemLinks(item.ID, buildDefaultWorkItemLinks(item)); err != nil {
		_ = store.Close()
		t.Fatalf("replaceWorkItemLinks: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closeLocalWorkDB: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	dropRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/work/runs/"+runID+"/drop", nil)
	if err != nil {
		t.Fatalf("new github run drop request: %v", err)
	}
	dropResponse, err := http.DefaultClient.Do(dropRequest)
	if err != nil {
		t.Fatalf("POST github run drop: %v", err)
	}
	defer dropResponse.Body.Close()
	if dropResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected github run drop status: %d", dropResponse.StatusCode)
	}

	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("expected github run dir to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(sandboxPath); !os.IsNotExist(err) {
		t.Fatalf("expected github sandbox to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(managedRoot, "latest-run.json")); !os.IsNotExist(err) {
		t.Fatalf("expected repo latest run pointer to be removed, err=%v", err)
	}
	if _, err := os.Stat(githubWorkLatestRunPath()); !os.IsNotExist(err) {
		t.Fatalf("expected global latest run pointer to be removed, err=%v", err)
	}

	store, err = openLocalWorkDB()
	if err != nil {
		t.Fatalf("reopenLocalWorkDB: %v", err)
	}
	defer store.Close()
	var indexCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM work_run_index WHERE run_id = ?`, runID).Scan(&indexCount); err != nil {
		t.Fatalf("count github work_run_index: %v", err)
	}
	if indexCount != 0 {
		t.Fatalf("expected dropped github run to be removed from work_run_index, got %d", indexCount)
	}
	updatedItem, err := store.readWorkItem(item.ID)
	if err != nil {
		t.Fatalf("read updated work item: %v", err)
	}
	if updatedItem.LinkedRunID != "" {
		t.Fatalf("expected github linked run id to be cleared, got %+v", updatedItem)
	}

	overviewResponse, err := http.Get(server.URL + "/api/v1/overview")
	if err != nil {
		t.Fatalf("GET overview after github drop: %v", err)
	}
	defer overviewResponse.Body.Close()
	var overview startUIOverview
	if err := json.NewDecoder(overviewResponse.Body).Decode(&overview); err != nil {
		t.Fatalf("decode overview after github drop: %v", err)
	}
	if len(overview.WorkRuns) != 0 || overview.Totals.ActiveWorkRuns != 0 {
		t.Fatalf("expected dropped github run to disappear from overview, got totals=%+v runs=%+v", overview.Totals, overview.WorkRuns)
	}
}

func TestStartUIAPIAssistantWorkspaceRoutes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:        6,
		RepoMode:       "fork",
		IssuePickMode:  "auto",
		PRForwardMode:  "approve",
		ForkIssuesMode: "auto",
		ImplementMode:  "auto",
		PublishTarget:  "fork",
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  now,
		Issues: map[string]startWorkIssueState{
			"7": {
				SourceNumber:      7,
				SourceURL:         "https://github.com/acme/widget/issues/7",
				Title:             "Fix flaky test",
				State:             "open",
				Status:            startWorkStatusQueued,
				Priority:          2,
				PrioritySource:    "triage",
				TriageStatus:      startWorkTriageCompleted,
				TriageRationale:   "Tests fail under parallel execution.",
				TriageFingerprint: "fp-7",
				TriageUpdatedAt:   now,
				UpdatedAt:         now,
			},
		},
		ServiceTasks: map[string]startWorkServiceTask{},
		PlannedItems: map[string]startWorkPlannedItem{
			"planned-approval": {
				ID:        "planned-approval",
				RepoSlug:  repoSlug,
				Title:     "Launch smoke run",
				State:     startPlannedItemQueued,
				UpdatedAt: now,
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	reviewItem, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "review_request",
		ExternalID: "review-1",
		RepoSlug:   repoSlug,
		TargetURL:  "https://github.com/acme/widget/pull/11",
		Subject:    "Review feature PR",
		Body:       "Please review this pull request.",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue review work item: %v", err)
	}
	replyItem, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "comment-22",
		RepoSlug:   repoSlug,
		TargetURL:  "https://github.com/acme/widget/pull/11",
		Subject:    "Reply in thread",
		Body:       "Please reply in the thread.",
		Metadata: map[string]any{
			"comment_kind":    "review_comment",
			"comment_path":    "internal/ui.go",
			"comment_line":    42,
			"comment_api_url": "https://api.github.com/repos/acme/widget/pulls/comments/22",
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueue reply work item: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	reviewItem.Status = workItemStatusDraftReady
	reviewItem.LatestDraft = &workItemDraft{
		Kind:        "review",
		Body:        "Please address the flaky assertion.",
		ReviewEvent: "REQUEST_CHANGES",
		Summary:     "Flaky assertion needs a guard.",
		InlineComments: []workItemDraftInlineComment{{
			Path: "internal/ui.go",
			Line: 42,
			Body: "Guard the nil path here.",
		}},
	}
	if err := store.updateWorkItem(reviewItem); err != nil {
		t.Fatalf("update review item: %v", err)
	}
	replyItem.Status = workItemStatusDraftReady
	replyItem.LatestDraft = &workItemDraft{
		Kind:    "reply",
		Body:    "I tightened the null handling and added coverage.",
		Summary: "Reply with fix summary.",
	}
	if err := store.updateWorkItem(replyItem); err != nil {
		t.Fatalf("update reply item: %v", err)
	}
	store.Close()

	githubManagedRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	githubRunDir := filepath.Join(githubManagedRoot, "runs", "gh-ui-blocked")
	if err := os.MkdirAll(githubRunDir, 0o755); err != nil {
		t.Fatalf("mkdir github run dir: %v", err)
	}
	githubManifest := githubWorkManifest{
		RunID:            "gh-ui-blocked",
		RepoSlug:         repoSlug,
		RepoOwner:        "acme",
		RepoName:         "widget",
		ManagedRepoRoot:  githubManagedRoot,
		SourcePath:       sourcePath,
		TargetURL:        "https://github.com/acme/widget/issues/7",
		TargetKind:       "issue",
		TargetNumber:     7,
		UpdatedAt:        now,
		PublicationState: "ci_waiting",
		NeedsHuman:       true,
		NeedsHumanReason: "approval required",
		NextAction:       "waiting for approval",
	}
	githubManifestPath := filepath.Join(githubRunDir, "manifest.json")
	if err := writeGithubJSON(githubManifestPath, githubManifest); err != nil {
		t.Fatalf("write github manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(githubManifestPath, githubManifest); err != nil {
		t.Fatalf("index github manifest: %v", err)
	}

	investigateDir := filepath.Join(sourcePath, ".nana", "logs", "investigate", "investigate-100")
	if err := os.MkdirAll(investigateDir, 0o755); err != nil {
		t.Fatalf("mkdir investigate dir: %v", err)
	}
	finalReportPath := filepath.Join(investigateDir, "final-report.json")
	validatorPath := filepath.Join(investigateDir, "round-1-validator-result.json")
	if err := writeGithubJSON(finalReportPath, investigateReport{
		OverallStatus:              investigateStatusConfirmed,
		OverallShortExplanation:    "Flake reproduced in scheduler path.",
		OverallDetailedExplanation: "The scheduler path races the cleanup branch under parallel execution.",
		OverallProofs: []investigateProof{{
			Kind:        "source_code",
			Title:       "scheduler",
			Link:        "app://local/path",
			WhyItProves: "the guard is missing",
			IsPrimary:   true,
			Path:        filepath.Join(sourcePath, "main.go"),
			Line:        1,
		}},
		Issues: []investigateIssue{{
			ID:                  "issue-1",
			ShortExplanation:    "cleanup races scheduler",
			DetailedExplanation: "detailed explanation",
			Proofs:              []investigateProof{},
		}},
	}); err != nil {
		t.Fatalf("write final report: %v", err)
	}
	if err := writeGithubJSON(validatorPath, investigateValidatorResult{
		Accepted: true,
		Summary:  "accepted",
	}); err != nil {
		t.Fatalf("write validator result: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(investigateDir, "manifest.json"), investigateManifest{
		Version:         1,
		RunID:           "investigate-100",
		CreatedAt:       now,
		UpdatedAt:       now,
		CompletedAt:     now,
		Status:          investigateRunStatusCompleted,
		Query:           "Investigate flaky scheduler failure",
		WorkspaceRoot:   sourcePath,
		CodexHome:       filepath.Join(home, ".codex-investigate"),
		MCPStatusPath:   filepath.Join(investigateDir, "mcp-status.json"),
		RunDir:          investigateDir,
		FinalReportPath: finalReportPath,
		MaxRounds:       investigateMaxRounds,
		AcceptedRound:   1,
		Rounds: []investigateRoundState{{
			Round:               1,
			ValidatorResultPath: validatorPath,
			Status:              "accepted",
		}},
	}); err != nil {
		t.Fatalf("write investigation manifest: %v", err)
	}

	oldInvestigate := startUISpawnIssueInvestigation
	oldLaunchTracked := startUILaunchTrackedIssueWork
	oldLaunchQuery := startUISpawnInvestigateQuery
	oldSyncRun := startUISyncGithubRun
	oldSyncFeedback := startUISyncGithubFeedback
	startUISpawnIssueInvestigation = func(repoSlug string, issue startWorkIssueState) (startUIBackgroundLaunch, error) {
		return startUIBackgroundLaunch{Status: "spawned", Result: "issue investigation started"}, nil
	}
	startUILaunchTrackedIssueWork = func(repoSlug string, issue startWorkIssueState) (startUIBackgroundLaunch, error) {
		return startUIBackgroundLaunch{Status: "spawned", Result: "tracked issue work started"}, nil
	}
	startUISpawnInvestigateQuery = func(workspaceRoot string, query string) (startUIBackgroundLaunch, error) {
		return startUIBackgroundLaunch{Status: "spawned", Result: "investigation started", WorkspaceRoot: workspaceRoot, Query: query}, nil
	}
	startUISyncGithubRun = func(options githubWorkSyncOptions) error {
		return nil
	}
	startUISyncGithubFeedback = func(options workItemSyncCommandOptions) (githubWorkItemSyncResult, error) {
		return githubWorkItemSyncResult{Queued: 2, Created: 1, Refreshed: 1}, nil
	}
	defer func() {
		startUISpawnIssueInvestigation = oldInvestigate
		startUILaunchTrackedIssueWork = oldLaunchTracked
		startUISpawnInvestigateQuery = oldLaunchQuery
		startUISyncGithubRun = oldSyncRun
		startUISyncGithubFeedback = oldSyncFeedback
	}()

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	issuesResponse, err := http.Get(server.URL + "/api/v1/issues")
	if err != nil {
		t.Fatalf("GET issues: %v", err)
	}
	defer issuesResponse.Body.Close()
	var issuesPayload startUIIssueQueueResponse
	if err := json.NewDecoder(issuesResponse.Body).Decode(&issuesPayload); err != nil {
		t.Fatalf("decode issues payload: %v", err)
	}
	if len(issuesPayload.Items) != 1 || issuesPayload.Items[0].TriageRationale != "Tests fail under parallel execution." {
		t.Fatalf("unexpected issues payload: %+v", issuesPayload.Items)
	}

	investigationsResponse, err := http.Get(server.URL + "/api/v1/investigations")
	if err != nil {
		t.Fatalf("GET investigations: %v", err)
	}
	defer investigationsResponse.Body.Close()
	var investigationsPayload struct {
		Items []startUIInvestigationSummary `json:"items"`
	}
	if err := json.NewDecoder(investigationsResponse.Body).Decode(&investigationsPayload); err != nil {
		t.Fatalf("decode investigations payload: %v", err)
	}
	if len(investigationsPayload.Items) != 1 || investigationsPayload.Items[0].OverallStatus != investigateStatusConfirmed {
		t.Fatalf("unexpected investigations payload: %+v", investigationsPayload.Items)
	}

	investigationDetailResponse, err := http.Get(server.URL + "/api/v1/investigations/investigate-100")
	if err != nil {
		t.Fatalf("GET investigation detail: %v", err)
	}
	defer investigationDetailResponse.Body.Close()
	var investigationDetail startUIInvestigationDetail
	if err := json.NewDecoder(investigationDetailResponse.Body).Decode(&investigationDetail); err != nil {
		t.Fatalf("decode investigation detail: %v", err)
	}
	if investigationDetail.FinalReport == nil || investigationDetail.LatestValidatorResult == nil || !investigationDetail.LatestValidatorResult.Accepted {
		t.Fatalf("unexpected investigation detail: %+v", investigationDetail)
	}

	reviewsResponse, err := http.Get(server.URL + "/api/v1/reviews")
	if err != nil {
		t.Fatalf("GET reviews: %v", err)
	}
	defer reviewsResponse.Body.Close()
	var reviewsPayload startUIFeedbackQueueResponse
	if err := json.NewDecoder(reviewsResponse.Body).Decode(&reviewsPayload); err != nil {
		t.Fatalf("decode reviews payload: %v", err)
	}
	if len(reviewsPayload.Items) != 1 || reviewsPayload.Items[0].ReviewEvent != "REQUEST_CHANGES" {
		t.Fatalf("unexpected reviews payload: %+v", reviewsPayload.Items)
	}

	repliesResponse, err := http.Get(server.URL + "/api/v1/replies")
	if err != nil {
		t.Fatalf("GET replies: %v", err)
	}
	defer repliesResponse.Body.Close()
	var repliesPayload startUIFeedbackQueueResponse
	if err := json.NewDecoder(repliesResponse.Body).Decode(&repliesPayload); err != nil {
		t.Fatalf("decode replies payload: %v", err)
	}
	if len(repliesPayload.Items) != 1 || repliesPayload.Items[0].Kind != "reply" {
		t.Fatalf("unexpected replies payload: %+v", repliesPayload.Items)
	}

	approvalsResponse, err := http.Get(server.URL + "/api/v1/approvals")
	if err != nil {
		t.Fatalf("GET approvals: %v", err)
	}
	defer approvalsResponse.Body.Close()
	var approvalsPayload startUIApprovalQueueResponse
	if err := json.NewDecoder(approvalsResponse.Body).Decode(&approvalsPayload); err != nil {
		t.Fatalf("decode approvals payload: %v", err)
	}
	if len(approvalsPayload.Items) < 3 {
		t.Fatalf("expected blocked run, draft-ready work item, and planned item approvals, got %+v", approvalsPayload.Items)
	}
	actionKinds := map[string]string{}
	for _, item := range approvalsPayload.Items {
		actionKinds[item.Kind] = item.ActionKind
		if item.NextAction == "" {
			t.Fatalf("expected approval next action, got %+v", item)
		}
	}
	if actionKinds["work_item"] != "approve_work_item" {
		t.Fatalf("expected work_item approvals to be directly approvable, got %+v", actionKinds)
	}
	if actionKinds["planned_item"] != "approve_planned_item" {
		t.Fatalf("expected planned_item approvals to be directly approvable, got %+v", actionKinds)
	}
	if actionKinds["work_run"] == "" {
		t.Fatalf("expected work_run approval metadata, got %+v", actionKinds)
	}

	runDetailResponse, err := http.Get(server.URL + "/api/v1/work/runs/gh-ui-blocked")
	if err != nil {
		t.Fatalf("GET work run detail: %v", err)
	}
	defer runDetailResponse.Body.Close()
	var runDetail startUIWorkRunDetail
	if err := json.NewDecoder(runDetailResponse.Body).Decode(&runDetail); err != nil {
		t.Fatalf("decode work run detail: %v", err)
	}
	if !runDetail.SyncAllowed || runDetail.GithubManifest == nil || runDetail.GithubStatus == nil || runDetail.HumanGateReason != "approval required" {
		t.Fatalf("unexpected work run detail: %+v", runDetail)
	}

	runSyncRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/work/runs/gh-ui-blocked/sync", nil)
	if err != nil {
		t.Fatalf("new run sync request: %v", err)
	}
	runSyncResponse, err := http.DefaultClient.Do(runSyncRequest)
	if err != nil {
		t.Fatalf("POST run sync: %v", err)
	}
	defer runSyncResponse.Body.Close()
	if runSyncResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected run sync status 200, got %d", runSyncResponse.StatusCode)
	}

	feedbackSyncBody := strings.NewReader(`{}`)
	feedbackSyncResponse, err := http.Post(server.URL+"/api/v1/feedback/sync", "application/json", feedbackSyncBody)
	if err != nil {
		t.Fatalf("POST feedback sync: %v", err)
	}
	defer feedbackSyncResponse.Body.Close()
	var feedbackSyncPayload struct {
		Result  githubWorkItemSyncResult   `json:"result"`
		Reviews []startUIFeedbackQueueItem `json:"reviews"`
		Replies []startUIFeedbackQueueItem `json:"replies"`
	}
	if err := json.NewDecoder(feedbackSyncResponse.Body).Decode(&feedbackSyncPayload); err != nil {
		t.Fatalf("decode feedback sync payload: %v", err)
	}
	if feedbackSyncPayload.Result.Queued != 2 || len(feedbackSyncPayload.Reviews) != 1 || len(feedbackSyncPayload.Replies) != 1 {
		t.Fatalf("unexpected feedback sync payload: %+v", feedbackSyncPayload)
	}

	issueInvestigateRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/issues/7/investigate", nil)
	if err != nil {
		t.Fatalf("new issue investigate request: %v", err)
	}
	issueInvestigateResponse, err := http.DefaultClient.Do(issueInvestigateRequest)
	if err != nil {
		t.Fatalf("POST issue investigate: %v", err)
	}
	defer issueInvestigateResponse.Body.Close()
	if issueInvestigateResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected issue investigate status 200, got %d", issueInvestigateResponse.StatusCode)
	}

	launchWorkRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/issues/7/launch-work", nil)
	if err != nil {
		t.Fatalf("new launch work request: %v", err)
	}
	launchWorkResponse, err := http.DefaultClient.Do(launchWorkRequest)
	if err != nil {
		t.Fatalf("POST launch work: %v", err)
	}
	defer launchWorkResponse.Body.Close()
	if launchWorkResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected launch work status 200, got %d", launchWorkResponse.StatusCode)
	}

	investigationBody := strings.NewReader(`{"query":"Investigate flaky scheduler failure","repo_slug":"acme/widget"}`)
	newInvestigationResponse, err := http.Post(server.URL+"/api/v1/investigations", "application/json", investigationBody)
	if err != nil {
		t.Fatalf("POST investigations: %v", err)
	}
	defer newInvestigationResponse.Body.Close()
	if newInvestigationResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected investigations POST status 200, got %d", newInvestigationResponse.StatusCode)
	}
}

func TestStartUIAPIWorkRunLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	localManifest := localWorkManifest{
		RunID:            "lw-log-ui",
		RepoRoot:         filepath.Join(cwd, "standalone-repo"),
		RepoName:         "standalone-repo",
		RepoID:           "repo-log-ui",
		CreatedAt:        time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
		Status:           "failed",
		CurrentPhase:     "review",
		CurrentIteration: 1,
		SandboxPath:      filepath.Join(home, "sandbox-local"),
		SandboxRepoPath:  filepath.Join(home, "sandbox-local", "repo"),
	}
	if err := writeLocalWorkManifest(localManifest); err != nil {
		t.Fatalf("write local manifest: %v", err)
	}
	localIterationDir := localWorkIterationDir(localWorkRunDirByID(localManifest.RepoID, localManifest.RunID), 1)
	if err := os.MkdirAll(localIterationDir, 0o755); err != nil {
		t.Fatalf("mkdir local iteration dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localIterationDir, "implement-stdout.log"), []byte("line1\nline2\nline3"), 0o644); err != nil {
		t.Fatalf("write local stdout log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localIterationDir, "runtime-state.json"), []byte(`{"status":"failed"}`), 0o644); err != nil {
		t.Fatalf("write local runtime state: %v", err)
	}

	githubManagedRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	githubRunDir := filepath.Join(githubManagedRoot, "runs", "gh-log-ui")
	if err := os.MkdirAll(filepath.Join(githubRunDir, "lane-runtime"), 0o755); err != nil {
		t.Fatalf("mkdir github run dir: %v", err)
	}
	githubManifest := githubWorkManifest{
		RunID:            "gh-log-ui",
		RepoSlug:         "acme/widget",
		RepoOwner:        "acme",
		RepoName:         "widget",
		ManagedRepoRoot:  githubManagedRoot,
		SourcePath:       filepath.Join(cwd, "source"),
		TargetURL:        "https://github.com/acme/widget/issues/42",
		TargetKind:       "issue",
		TargetNumber:     42,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
		SandboxPath:      filepath.Join(home, "sandbox-github"),
		SandboxRepoPath:  filepath.Join(home, "sandbox-github", "repo"),
		PublicationState: "active",
	}
	githubManifestPath := filepath.Join(githubRunDir, "manifest.json")
	if err := writeGithubJSON(githubManifestPath, githubManifest); err != nil {
		t.Fatalf("write github manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(githubManifestPath, githubManifest); err != nil {
		t.Fatalf("index github manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(githubRunDir, "lane-runtime", "executor-stdout.log"), []byte("alpha\nbeta\ngamma"), 0o644); err != nil {
		t.Fatalf("write github stdout log: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	localLogsResponse, err := http.Get(server.URL + "/api/v1/work/runs/lw-log-ui/logs")
	if err != nil {
		t.Fatalf("GET local logs: %v", err)
	}
	defer localLogsResponse.Body.Close()
	var localLogs startUIWorkRunLogsResponse
	if err := json.NewDecoder(localLogsResponse.Body).Decode(&localLogs); err != nil {
		t.Fatalf("decode local logs payload: %v", err)
	}
	if !localLogs.Summary.Pending || localLogs.Summary.AttentionState != "failed" || localLogs.DefaultPath != "implement-stdout.log" {
		t.Fatalf("unexpected local logs payload: %+v", localLogs)
	}
	if localLogs.Summary.RepoSlug != "" || localLogs.Summary.RepoLabel != "standalone-repo" {
		t.Fatalf("unexpected local repo identity: %+v", localLogs.Summary)
	}

	localContentResponse, err := http.Get(server.URL + "/api/v1/work/runs/lw-log-ui/logs/content?path=implement-stdout.log&tail=2")
	if err != nil {
		t.Fatalf("GET local log content: %v", err)
	}
	defer localContentResponse.Body.Close()
	var localContent struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(localContentResponse.Body).Decode(&localContent); err != nil {
		t.Fatalf("decode local content payload: %v", err)
	}
	if localContent.Path != "implement-stdout.log" || localContent.Content != "line2\nline3" {
		t.Fatalf("unexpected local log content: %+v", localContent)
	}

	badPathResponse, err := http.Get(server.URL + "/api/v1/work/runs/lw-log-ui/logs/content?path=../manifest.json")
	if err != nil {
		t.Fatalf("GET bad log path: %v", err)
	}
	defer badPathResponse.Body.Close()
	if badPathResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected bad request for invalid log path, got %d", badPathResponse.StatusCode)
	}

	githubLogsResponse, err := http.Get(server.URL + "/api/v1/work/runs/gh-log-ui/logs")
	if err != nil {
		t.Fatalf("GET github logs: %v", err)
	}
	defer githubLogsResponse.Body.Close()
	var githubLogs startUIWorkRunLogsResponse
	if err := json.NewDecoder(githubLogsResponse.Body).Decode(&githubLogs); err != nil {
		t.Fatalf("decode github logs payload: %v", err)
	}
	if githubLogs.Summary.RepoSlug != "acme/widget" || !githubLogs.Summary.Pending {
		t.Fatalf("unexpected github logs payload: %+v", githubLogs)
	}

	githubContentResponse, err := http.Get(server.URL + "/api/v1/work/runs/gh-log-ui/logs/content?path=lane-runtime/executor-stdout.log&tail=2")
	if err != nil {
		t.Fatalf("GET github log content: %v", err)
	}
	defer githubContentResponse.Body.Close()
	var githubContent struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(githubContentResponse.Body).Decode(&githubContent); err != nil {
		t.Fatalf("decode github content payload: %v", err)
	}
	if githubContent.Path != "lane-runtime/executor-stdout.log" || githubContent.Content != "beta\ngamma" {
		t.Fatalf("unexpected github log content: %+v", githubContent)
	}
}

func TestStartUIWorkRunSummaryUsesLocalRepoSlugFromOriginRemote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repo := createLocalWorkRepoAt(t, filepath.Join(t.TempDir(), "repo"))
	startUITestSetOriginRemote(t, repo, "git@github.com:acme/widget.git")

	now := time.Now().UTC().Format(time.RFC3339)
	manifest := localWorkManifest{
		RunID:           "lw-local-remote",
		RepoRoot:        repo,
		RepoName:        "widget-local",
		RepoID:          "repo-local-remote",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          "running",
		CurrentPhase:    "review",
		SandboxPath:     filepath.Join(home, "sandbox-local"),
		SandboxRepoPath: filepath.Join(home, "sandbox-local", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write local manifest: %v", err)
	}

	entry, err := readWorkRunIndex(manifest.RunID)
	if err != nil {
		t.Fatalf("readWorkRunIndex: %v", err)
	}
	if entry.RepoSlug != "acme/widget" {
		t.Fatalf("expected indexed repo slug, got %+v", entry)
	}

	summary, err := startUIWorkRunFromIndex(entry, nil)
	if err != nil {
		t.Fatalf("startUIWorkRunFromIndex: %v", err)
	}
	if summary.RepoSlug != "acme/widget" || summary.RepoLabel != "acme/widget" {
		t.Fatalf("unexpected local run summary: %+v", summary)
	}
}

func TestStartUIWorkRunSummaryFallsBackToManagedSourcePathSlug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)

	now := time.Now().UTC().Format(time.RFC3339)
	manifest := localWorkManifest{
		RunID:           "lw-managed-path",
		RepoRoot:        sourcePath,
		RepoName:        "source",
		RepoID:          "repo-managed-path",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          "running",
		CurrentPhase:    "review",
		SandboxPath:     filepath.Join(home, "sandbox-managed"),
		SandboxRepoPath: filepath.Join(home, "sandbox-managed", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write local manifest: %v", err)
	}

	entry, err := readWorkRunIndex(manifest.RunID)
	if err != nil {
		t.Fatalf("readWorkRunIndex: %v", err)
	}

	summary, err := startUIWorkRunFromIndex(entry, map[string]string{filepath.Clean(sourcePath): repoSlug})
	if err != nil {
		t.Fatalf("startUIWorkRunFromIndex: %v", err)
	}
	if summary.RepoSlug != repoSlug || summary.RepoLabel != repoSlug {
		t.Fatalf("unexpected managed-path run summary: %+v", summary)
	}
}

func TestStartUIAPIScoutItemsAndActions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	repo := createLocalWorkRepoAt(t, sourcePath)
	if repo != sourcePath {
		t.Fatalf("unexpected repo path: %s", repo)
	}
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:        6,
		RepoMode:       "fork",
		IssuePickMode:  "auto",
		PRForwardMode:  "approve",
		ForkIssuesMode: "auto",
		ImplementMode:  "auto",
		PublishTarget:  "fork",
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	writeScoutPickupFixture(t, repo, improvementScoutRole, "Improve help text", "Make help clearer")
	if err := writeGithubJSON(filepath.Join(repo, ".nana", "improvements", "improve-test", "policy.json"), improvementPolicy{
		Version:          1,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{"improvement"},
		MaxIssues:        5,
	}); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(repo, ".nana", "improvements", "improve-test", "preflight.json"), uiScoutPreflight{
		Version:       1,
		BrowserReady:  false,
		Mode:          "repo_only",
		SurfaceKind:   "storybook",
		SurfaceTarget: "storybook",
		Reason:        "browser not available",
	}); err != nil {
		t.Fatalf("write preflight: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	loadResponse, err := http.Get(server.URL + "/api/v1/repos/" + repoSlug + "/scout-items")
	if err != nil {
		t.Fatalf("GET scout items: %v", err)
	}
	defer loadResponse.Body.Close()
	var loadPayload startUIScoutItemsResponse
	if err := json.NewDecoder(loadResponse.Body).Decode(&loadPayload); err != nil {
		t.Fatalf("decode scout items: %v", err)
	}
	if len(loadPayload.Items) != 1 || loadPayload.Items[0].Status != "pending" || loadPayload.Items[0].Destination != "local" {
		t.Fatalf("unexpected scout items payload: %+v", loadPayload)
	}
	if len(loadPayload.ScoutCatalog) != len(supportedScoutRoleOrder) {
		t.Fatalf("expected scout catalog in scout-items payload: %+v", loadPayload.ScoutCatalog)
	}
	if loadPayload.Items[0].AuditMode != "repo_only" || loadPayload.Items[0].SurfaceTarget != "storybook" || loadPayload.Items[0].PreflightPath == "" {
		t.Fatalf("expected preflight metadata on scout item, got %+v", loadPayload.Items[0])
	}
	itemID := loadPayload.Items[0].ID

	dismissRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/scout-items/"+itemID+"/dismiss", nil)
	if err != nil {
		t.Fatalf("new dismiss request: %v", err)
	}
	dismissResponse, err := http.DefaultClient.Do(dismissRequest)
	if err != nil {
		t.Fatalf("POST dismiss: %v", err)
	}
	defer dismissResponse.Body.Close()
	var dismissPayload startUIScoutItemsResponse
	if err := json.NewDecoder(dismissResponse.Body).Decode(&dismissPayload); err != nil {
		t.Fatalf("decode dismiss payload: %v", err)
	}
	if len(dismissPayload.Items) != 1 || dismissPayload.Items[0].Status != "dismissed" {
		t.Fatalf("unexpected dismiss payload: %+v", dismissPayload)
	}

	resetRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/scout-items/"+itemID+"/reset", nil)
	if err != nil {
		t.Fatalf("new reset request: %v", err)
	}
	resetResponse, err := http.DefaultClient.Do(resetRequest)
	if err != nil {
		t.Fatalf("POST reset: %v", err)
	}
	defer resetResponse.Body.Close()
	var resetPayload startUIScoutItemsResponse
	if err := json.NewDecoder(resetResponse.Body).Decode(&resetPayload); err != nil {
		t.Fatalf("decode reset payload: %v", err)
	}
	if len(resetPayload.Items) != 1 || resetPayload.Items[0].Status != "pending" {
		t.Fatalf("unexpected reset payload: %+v", resetPayload)
	}

	queueRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/scout-items/"+itemID+"/queue-planned", nil)
	if err != nil {
		t.Fatalf("new queue request: %v", err)
	}
	queueResponse, err := http.DefaultClient.Do(queueRequest)
	if err != nil {
		t.Fatalf("POST queue-planned: %v", err)
	}
	defer queueResponse.Body.Close()
	var queuePayload startUIScoutItemsResponse
	if err := json.NewDecoder(queueResponse.Body).Decode(&queuePayload); err != nil {
		t.Fatalf("decode queue payload: %v", err)
	}
	if len(queuePayload.Items) != 1 || queuePayload.Items[0].Status != "planned" || queuePayload.Items[0].PlannedItemID == "" {
		t.Fatalf("unexpected queue payload: %+v", queuePayload)
	}
	if queuePayload.Repo.State == nil || len(queuePayload.Repo.State.PlannedItems) != 1 {
		t.Fatalf("expected planned item in repo state, got %+v", queuePayload.Repo.State)
	}
}

func TestStartUIAPIScoutItemsBatchAction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	repo := createLocalWorkRepoAt(t, sourcePath)
	if repo != sourcePath {
		t.Fatalf("unexpected repo path: %s", repo)
	}
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:        6,
		RepoMode:       "fork",
		IssuePickMode:  "auto",
		PRForwardMode:  "approve",
		ForkIssuesMode: "auto",
		ImplementMode:  "auto",
		PublishTarget:  "fork",
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	writeScoutPickupFixture(t, repo, improvementScoutRole, "Improve help text", "Make help clearer")
	writeScoutPickupFixture(t, repo, enhancementScoutRole, "Add benchmark target", "Expose benchmarks")
	if err := writeGithubJSON(filepath.Join(repo, ".nana", "improvements", "improve-test", "policy.json"), improvementPolicy{
		Version:          1,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{"improvement"},
		MaxIssues:        5,
	}); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(repo, ".nana", "enhancements", "enhance-test", "policy.json"), improvementPolicy{
		Version:          1,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{"enhancement"},
		MaxIssues:        5,
	}); err != nil {
		t.Fatalf("write enhancement policy: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	loadResponse, err := http.Get(server.URL + "/api/v1/repos/" + repoSlug + "/scout-items")
	if err != nil {
		t.Fatalf("GET scout items: %v", err)
	}
	defer loadResponse.Body.Close()
	var loadPayload startUIScoutItemsResponse
	if err := json.NewDecoder(loadResponse.Body).Decode(&loadPayload); err != nil {
		t.Fatalf("decode scout items: %v", err)
	}
	if len(loadPayload.Items) != 2 {
		t.Fatalf("expected two scout items, got %+v", loadPayload.Items)
	}

	body := strings.NewReader(fmt.Sprintf(`{"action":"queue-planned","item_ids":["%s","%s"]}`, loadPayload.Items[0].ID, loadPayload.Items[1].ID))
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/scout-items/batch", body)
	if err != nil {
		t.Fatalf("new batch request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST batch queue: %v", err)
	}
	defer response.Body.Close()
	var payload startUIScoutItemsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode batch payload: %v", err)
	}
	if payload.Action != "queue-planned" || payload.SuccessCount != 2 || payload.FailureCount != 0 {
		t.Fatalf("unexpected batch action summary: %+v", payload)
	}
	if len(payload.Results) != 2 || payload.Results[0].Status != "ok" || payload.Results[1].Status != "ok" {
		t.Fatalf("unexpected batch results: %+v", payload.Results)
	}
	if len(payload.Items) != 2 || payload.Items[0].Status != "planned" || payload.Items[1].Status != "planned" {
		t.Fatalf("expected both scout items to be planned, got %+v", payload.Items)
	}
	if payload.Repo.State == nil || len(payload.Repo.State.PlannedItems) != 2 {
		t.Fatalf("expected planned items in repo state, got %+v", payload.Repo.State)
	}
}

func TestStartUIWebHandlerInjectsAPIBase(t *testing.T) {
	server := httptest.NewServer(startUIWebHandler("http://127.0.0.1:17653"))
	defer server.Close()

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `window.NANA_API_BASE = "http://127.0.0.1:17653"`) {
		t.Fatalf("expected injected API base, got %s", string(body))
	}
	if !strings.Contains(string(body), "Assistant Workspace") || !strings.Contains(string(body), ">Home<") {
		t.Fatalf("expected assistant workspace shell, got %s", string(body))
	}

	appResponse, err := http.Get(server.URL + "/app.js")
	if err != nil {
		t.Fatalf("GET /app.js: %v", err)
	}
	defer appResponse.Body.Close()
	appBody, err := io.ReadAll(appResponse.Body)
	if err != nil {
		t.Fatalf("read app.js body: %v", err)
	}
	if !strings.Contains(string(appBody), `data-repo-tab="config"`) {
		t.Fatalf("expected config tab wiring in app.js, got %s", string(appBody))
	}
	if !strings.Contains(string(appBody), `data-repo-tab="scouts"`) {
		t.Fatalf("expected scouts tab wiring in app.js, got %s", string(appBody))
	}
	if !strings.Contains(string(appBody), `repoScoutCatalog(repo)`) {
		t.Fatalf("expected scout config fields in app.js, got %s", string(appBody))
	}
	if !strings.Contains(string(appBody), `draft.scouts_by_role[entry.config_key]`) {
		t.Fatalf("expected ui scout config fields in app.js, got %s", string(appBody))
	}
	if !strings.Contains(string(appBody), `All Repo Configs`) {
		t.Fatalf("expected raw repo config section in app.js, got %s", string(appBody))
	}
	if !strings.Contains(string(appBody), `prettyJSON(rawSettings)`) {
		t.Fatalf("expected settings.json raw payload rendering in app.js, got %s", string(appBody))
	}
	if !strings.Contains(string(appBody), `data-nav-view="investigations"`) {
		t.Fatalf("expected investigations navigation in app.js, got %s", string(appBody))
	}
	if !strings.Contains(string(appBody), `data-feedback-tab="reviews"`) {
		t.Fatalf("expected feedback tabs in app.js, got %s", string(appBody))
	}
	if !strings.Contains(string(appBody), `data-repo-drop="`) {
		t.Fatalf("expected repo drop control in app.js, got %s", string(appBody))
	}
}

func TestHashStartUIEventPayloadIgnoresGeneratedAt(t *testing.T) {
	left := map[string]any{
		"generated_at": "2026-04-13T17:00:00Z",
		"totals":       map[string]any{"repos": 2},
		"repos":        []map[string]any{{"repo_slug": "dkropachev/nana"}},
	}
	right := map[string]any{
		"generated_at": "2026-04-13T17:00:02Z",
		"totals":       map[string]any{"repos": 2},
		"repos":        []map[string]any{{"repo_slug": "dkropachev/nana"}},
	}
	if leftHash, rightHash := hashStartUIEventPayload(left), hashStartUIEventPayload(right); leftHash != rightHash {
		t.Fatalf("expected generated_at to be ignored, got %s vs %s", leftHash, rightHash)
	}
}

func TestStartUIPlannedItemLaunchFailureInvalidatesOverviewCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	repoSlug := "acme/widget"
	now := "2026-04-13T17:00:00Z"
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	state := startWorkState{
		Version:      startWorkStateVersion,
		SourceRepo:   repoSlug,
		UpdatedAt:    now,
		Issues:       map[string]startWorkIssueState{},
		ServiceTasks: map[string]startWorkServiceTask{},
		PlannedItems: map[string]startWorkPlannedItem{
			"planned-fail": {
				ID:        "planned-fail",
				RepoSlug:  repoSlug,
				Title:     "Launch should fail",
				State:     startPlannedItemQueued,
				UpdatedAt: now,
			},
		},
	}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	api := &startUIAPI{cwd: cwd}
	startUITestPrimeOverviewCache(t, api)

	oldLaunch := startLaunchPlannedItem
	startLaunchPlannedItem = func(cwd string, repoSlug string, workOptions startWorkOptions, item startWorkPlannedItem) (startPlannedLaunchResult, error) {
		return startPlannedLaunchResult{}, fmt.Errorf("launcher failed")
	}
	defer func() { startLaunchPlannedItem = oldLaunch }()

	server := httptest.NewServer(api.routes())
	defer server.Close()
	response, err := http.Post(server.URL+"/api/v1/planned-items/planned-fail/launch-now", "application/json", nil)
	if err != nil {
		t.Fatalf("POST launch-now: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected launch failure status 400, got %d", response.StatusCode)
	}
	updated, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read updated state: %v", err)
	}
	item := updated.PlannedItems["planned-fail"]
	if item.State != startPlannedItemFailed || !strings.Contains(item.LastError, "launcher failed") {
		t.Fatalf("expected failed planned item side effect, got %+v", item)
	}
	if startUITestOverviewCacheValid(api) {
		t.Fatalf("expected failed planned-item launch side effect to invalidate overview cache")
	}
}

func TestStartUIWorkItemRunFailureInvalidatesOverviewCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	cwd := t.TempDir()

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "comment-run-fail",
		RepoSlug:   "acme/widget",
		Subject:    "Run should fail",
		Body:       "Please draft a reply.",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	api := &startUIAPI{cwd: cwd}
	startUITestPrimeOverviewCache(t, api)

	server := httptest.NewServer(api.routes())
	defer server.Close()
	response, err := http.Post(server.URL+"/api/v1/work-items/"+item.ID+"/run", "application/json", nil)
	if err != nil {
		t.Fatalf("POST work item run: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected run failure status 400, got %d", response.StatusCode)
	}
	updated := startUITestReadWorkItem(t, item.ID)
	if updated.Status != workItemStatusFailed {
		t.Fatalf("expected failed work-item run side effect, got %+v", updated)
	}
	if startUITestOverviewCacheValid(api) {
		t.Fatalf("expected failed work-item run side effect to invalidate overview cache")
	}
}

func TestStartUIWorkItemFixFailureInvalidatesOverviewCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	cwd := t.TempDir()

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "comment-fix-fail",
		RepoSlug:   "acme/widget",
		Subject:    "Fix should fail",
		Body:       "Please draft a reply.",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	item.LatestDraft = &workItemDraft{Kind: "reply", Body: "Draft reply", Summary: "Draft summary", Confidence: 0.8}
	item.Status = workItemStatusDraftReady
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	if err := store.updateWorkItem(item); err != nil {
		_ = store.Close()
		t.Fatalf("updateWorkItem: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	api := &startUIAPI{cwd: cwd}
	startUITestPrimeOverviewCache(t, api)

	server := httptest.NewServer(api.routes())
	defer server.Close()
	body := strings.NewReader(`{"instruction":"clarify the answer"}`)
	response, err := http.Post(server.URL+"/api/v1/work-items/"+item.ID+"/fix", "application/json", body)
	if err != nil {
		t.Fatalf("POST work item fix: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected fix failure status 400, got %d", response.StatusCode)
	}
	updated := startUITestReadWorkItem(t, item.ID)
	if updated.Status != workItemStatusFailed {
		t.Fatalf("expected failed work-item fix side effect, got %+v", updated)
	}
	if startUITestOverviewCacheValid(api) {
		t.Fatalf("expected failed work-item fix side effect to invalidate overview cache")
	}
}

func TestStartUIScoutQueuePlannedPickupWriteFailureInvalidatesOverviewCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	repo := createLocalWorkRepoAt(t, sourcePath)
	writeScoutPickupFixture(t, repo, improvementScoutRole, "Improve help text", "Make help clearer")
	if err := writeGithubJSON(filepath.Join(repo, ".nana", "improvements", "improve-test", "policy.json"), improvementPolicy{
		Version:          1,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{"improvement"},
		MaxIssues:        5,
	}); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	pickupPath, err := localScoutPickupStatePath(repo)
	if err != nil {
		t.Fatalf("pickup state path: %v", err)
	}
	if err := writeLocalScoutPickupState(pickupPath, localScoutPickupState{Version: 1, Items: map[string]localScoutPickupItem{}}); err != nil {
		t.Fatalf("write pickup state: %v", err)
	}
	if err := os.Chmod(pickupPath, 0o400); err != nil {
		t.Fatalf("chmod pickup state: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(pickupPath, 0o600) })

	items, err := listStartUIScoutItems(repoSlug)
	if err != nil {
		t.Fatalf("list scout items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one scout item, got %+v", items)
	}

	api := &startUIAPI{cwd: cwd}
	startUITestPrimeOverviewCache(t, api)

	server := httptest.NewServer(api.routes())
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/scout-items/"+items[0].ID+"/queue-planned", nil)
	if err != nil {
		t.Fatalf("new queue request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST queue-planned: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected queue-planned failure status 400, got %d", response.StatusCode)
	}
	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if len(state.PlannedItems) != 1 {
		t.Fatalf("expected planned item side effect before pickup write failure, got %+v", state.PlannedItems)
	}
	if startUITestOverviewCacheValid(api) {
		t.Fatalf("expected failed scout queue-planned side effect to invalidate overview cache")
	}
}

func TestStartUIWorkItemSubmitEventWriteFailureInvalidatesOverviewCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	item := startUITestCreateDraftWorkItem(t, "submit-event-fail")
	api := &startUIAPI{cwd: cwd}
	startUITestPrimeOverviewCache(t, api)
	startUITestFailWorkItemEvents(t)

	server := httptest.NewServer(api.routes())
	defer server.Close()
	response, err := http.Post(server.URL+"/api/v1/work-items/"+item.ID+"/submit", "application/json", nil)
	if err != nil {
		t.Fatalf("POST work item submit: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected submit failure status 400, got %d", response.StatusCode)
	}
	updated := startUITestReadWorkItem(t, item.ID)
	if updated.Status != workItemStatusSubmitted {
		t.Fatalf("expected submitted work-item side effect before event failure, got %+v", updated)
	}
	if startUITestOverviewCacheValid(api) {
		t.Fatalf("expected failed work-item submit side effect to invalidate overview cache")
	}
}

func TestStartUIWorkItemDropEventWriteFailureInvalidatesOverviewCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	item := startUITestCreateQueuedWorkItem(t, "drop-event-fail")
	api := &startUIAPI{cwd: cwd}
	startUITestPrimeOverviewCache(t, api)
	startUITestFailWorkItemEvents(t)

	server := httptest.NewServer(api.routes())
	defer server.Close()
	response, err := http.Post(server.URL+"/api/v1/work-items/"+item.ID+"/drop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST work item drop: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected drop failure status 400, got %d", response.StatusCode)
	}
	updated := startUITestReadWorkItem(t, item.ID)
	if updated.Status != workItemStatusDropped {
		t.Fatalf("expected dropped work-item side effect before event failure, got %+v", updated)
	}
	if startUITestOverviewCacheValid(api) {
		t.Fatalf("expected failed work-item drop side effect to invalidate overview cache")
	}
}

func TestStartUIWorkItemRestoreEventWriteFailureInvalidatesOverviewCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	item := startUITestCreateQueuedWorkItem(t, "restore-event-fail")
	item.Status = workItemStatusDropped
	item.Hidden = false
	startUITestUpdateWorkItem(t, item)
	api := &startUIAPI{cwd: cwd}
	startUITestPrimeOverviewCache(t, api)
	startUITestFailWorkItemEvents(t)

	server := httptest.NewServer(api.routes())
	defer server.Close()
	response, err := http.Post(server.URL+"/api/v1/work-items/"+item.ID+"/restore", "application/json", nil)
	if err != nil {
		t.Fatalf("POST work item restore: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected restore failure status 400, got %d", response.StatusCode)
	}
	updated := startUITestReadWorkItem(t, item.ID)
	if updated.Status != workItemStatusQueued {
		t.Fatalf("expected restored work-item side effect before event failure, got %+v", updated)
	}
	if startUITestOverviewCacheValid(api) {
		t.Fatalf("expected failed work-item restore side effect to invalidate overview cache")
	}
}

func TestStartUIOverviewCacheReusesSnapshotUntilStateChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	previousInterval := startUIOverviewCacheProbeInterval
	startUIOverviewCacheProbeInterval = time.Hour
	defer func() { startUIOverviewCacheProbeInterval = previousInterval }()

	repoSlug := "acme/widget"
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	state := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-13T17:00:00Z",
		Issues: map[string]startWorkIssueState{
			"1": {SourceNumber: 1, Title: "First", Status: startWorkStatusQueued, UpdatedAt: "2026-04-13T17:00:00Z"},
		},
		ServiceTasks: map[string]startWorkServiceTask{},
		PlannedItems: map[string]startWorkPlannedItem{},
	}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	api := &startUIAPI{cwd: cwd}
	overview, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview: %v", err)
	}
	if overview.Totals.Repos != 1 || overview.Totals.IssuesQueued != 1 {
		t.Fatalf("unexpected initial overview: %+v", overview.Totals)
	}

	api.overviewCacheMu.Lock()
	api.overviewCache.overview.Totals.Repos = 99
	api.overviewCache.overview.Totals.IssuesQueued = 99
	api.overviewCache.events = startUIOverviewEventsPayload(api.overviewCache.overview)
	api.overviewCacheMu.Unlock()

	cached, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview cached: %v", err)
	}
	if cached.Totals.Repos != 99 || cached.Totals.IssuesQueued != 99 {
		t.Fatalf("expected unchanged dependencies to reuse cached snapshot, got %+v", cached.Totals)
	}

	state.Issues["2"] = startWorkIssueState{SourceNumber: 2, Title: "Second", Status: startWorkStatusQueued, UpdatedAt: "2026-04-13T17:00:01Z"}
	state.UpdatedAt = "2026-04-13T17:00:01Z"
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("rewrite start state: %v", err)
	}
	api.overviewCacheMu.Lock()
	api.overviewCache.checkedAt = time.Time{}
	api.overviewCacheMu.Unlock()

	refreshed, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview refreshed: %v", err)
	}
	if refreshed.Totals.Repos != 1 || refreshed.Totals.IssuesQueued != 2 {
		t.Fatalf("expected state change to refresh overview, got %+v", refreshed.Totals)
	}
}

func TestStartUIOverviewCacheRebuildsWhenDependenciesChangeDuringRebuild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	previousInterval := startUIOverviewCacheProbeInterval
	startUIOverviewCacheProbeInterval = time.Hour
	defer func() { startUIOverviewCacheProbeInterval = previousInterval }()

	repoSlug := "acme/widget"
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	state := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-13T17:00:00Z",
		Issues: map[string]startWorkIssueState{
			"1": {SourceNumber: 1, Title: "First", Status: startWorkStatusQueued, UpdatedAt: "2026-04-13T17:00:00Z"},
		},
		ServiceTasks: map[string]startWorkServiceTask{},
		PlannedItems: map[string]startWorkPlannedItem{},
	}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	previousHook := startUIOverviewCacheAfterUncachedBuildHook
	mutatedDuringBuild := false
	var mutationErr error
	startUIOverviewCacheAfterUncachedBuildHook = func() {
		if mutatedDuringBuild {
			return
		}
		mutatedDuringBuild = true
		state.Issues["2"] = startWorkIssueState{SourceNumber: 2, Title: "Second", Status: startWorkStatusQueued, UpdatedAt: "2026-04-13T17:00:01Z"}
		state.UpdatedAt = "2026-04-13T17:00:01Z"
		mutationErr = writeStartWorkState(state)
	}
	defer func() { startUIOverviewCacheAfterUncachedBuildHook = previousHook }()

	api := &startUIAPI{cwd: cwd}
	overview, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview: %v", err)
	}
	if mutationErr != nil {
		t.Fatalf("mutate state during build: %v", mutationErr)
	}
	if !mutatedDuringBuild {
		t.Fatalf("expected dependency mutation hook to run")
	}
	if overview.Totals.Repos != 1 || overview.Totals.IssuesQueued != 2 {
		t.Fatalf("expected rebuild to include dependency change, got %+v", overview.Totals)
	}

	api.overviewCacheMu.Lock()
	cachedValid := api.overviewCache.valid
	cachedQueued := api.overviewCache.overview.Totals.IssuesQueued
	api.overviewCacheMu.Unlock()
	if !cachedValid || cachedQueued != 2 {
		t.Fatalf("expected cache to store only stable rebuilt overview, valid=%v queued=%d", cachedValid, cachedQueued)
	}

	cached, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview cached: %v", err)
	}
	if cached.Totals.IssuesQueued != 2 {
		t.Fatalf("expected cached overview to stay fresh after mid-build dependency change, got %+v", cached.Totals)
	}
}

func TestStartUIOverviewCacheInvalidatesWhenScoutPolicyChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	previousInterval := startUIOverviewCacheProbeInterval
	startUIOverviewCacheProbeInterval = time.Hour
	defer func() { startUIOverviewCacheProbeInterval = previousInterval }()

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	api := &startUIAPI{cwd: cwd}
	overview, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview: %v", err)
	}
	if len(overview.Repos) != 1 || overview.Repos[0].Scouts.Improvement.Enabled {
		t.Fatalf("expected scout to start disabled, got %+v", overview.Repos)
	}

	if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, improvementScoutRole, false), improvementPolicy{
		Version:          1,
		Mode:             "auto",
		IssueDestination: improvementDestinationLocal,
		MaxIssues:        6,
	}); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	api.overviewCacheMu.Lock()
	api.overviewCache.checkedAt = time.Time{}
	api.overviewCacheMu.Unlock()

	refreshed, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview refreshed: %v", err)
	}
	if !refreshed.Repos[0].Scouts.Improvement.Enabled || refreshed.Repos[0].Scouts.Improvement.MaxIssues != 6 {
		t.Fatalf("expected scout policy change to refresh overview, got %+v", refreshed.Repos[0].Scouts.Improvement)
	}
}

func TestStartUIOverviewCacheInvalidatesWhenWorkDBChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	previousInterval := startUIOverviewCacheProbeInterval
	startUIOverviewCacheProbeInterval = time.Hour
	defer func() { startUIOverviewCacheProbeInterval = previousInterval }()

	api := &startUIAPI{cwd: cwd}
	overview, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview: %v", err)
	}
	if overview.Totals.ActiveWorkRuns != 0 {
		t.Fatalf("expected no active runs initially, got %+v", overview.Totals)
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	if err := store.writeManifest(localWorkManifest{
		RunID:           "lw-cache",
		RepoRoot:        filepath.Join(cwd, "repo"),
		RepoName:        "repo",
		RepoID:          "repo-cache",
		CreatedAt:       "2026-04-13T17:00:00Z",
		UpdatedAt:       "2026-04-13T17:00:01Z",
		Status:          "running",
		CurrentPhase:    "exec",
		SandboxPath:     filepath.Join(home, "sandbox"),
		SandboxRepoPath: filepath.Join(home, "sandbox", "repo"),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("write manifest: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	api.overviewCacheMu.Lock()
	api.overviewCache.overview.Totals.ActiveWorkRuns = 77
	api.overviewCache.events = startUIOverviewEventsPayload(api.overviewCache.overview)
	api.overviewCache.checkedAt = time.Time{}
	api.overviewCacheMu.Unlock()

	refreshed, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview refreshed: %v", err)
	}
	if refreshed.Totals.ActiveWorkRuns != 1 || len(refreshed.WorkRuns) != 1 {
		t.Fatalf("expected DB change to refresh work runs, got totals=%+v runs=%+v", refreshed.Totals, refreshed.WorkRuns)
	}
}

func TestStartUIOverviewCacheInvalidatesWhenIndexedGithubManifestChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	previousInterval := startUIOverviewCacheProbeInterval
	startUIOverviewCacheProbeInterval = 0
	defer func() { startUIOverviewCacheProbeInterval = previousInterval }()

	managedRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	manifestPath := filepath.Join(managedRoot, "runs", "gh-cache", "manifest.json")
	manifest := githubWorkManifest{
		RunID:            "gh-cache",
		RepoSlug:         "acme/widget",
		RepoOwner:        "acme",
		RepoName:         "widget",
		ManagedRepoRoot:  managedRoot,
		SourcePath:       filepath.Join(cwd, "source"),
		TargetURL:        "https://github.com/acme/widget/issues/42",
		TargetKind:       "issue",
		TargetNumber:     42,
		UpdatedAt:        "2026-04-13T17:00:00Z",
		SandboxPath:      filepath.Join(home, "sandbox-github"),
		SandboxRepoPath:  filepath.Join(home, "sandbox-github", "repo"),
		PublicationState: "active",
		NextAction:       "publish",
	}
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write github manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index github manifest: %v", err)
	}

	api := &startUIAPI{cwd: cwd}
	overview, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview: %v", err)
	}
	if len(overview.WorkRuns) != 1 || overview.WorkRuns[0].Status != "active" || overview.WorkRuns[0].CurrentPhase != "publish" || overview.WorkRuns[0].AttentionState != "active" {
		t.Fatalf("unexpected initial GitHub run summary: %+v", overview.WorkRuns)
	}

	manifest.PublicationState = "blocked"
	manifest.NextAction = "blocked:user"
	manifest.NeedsHuman = true
	manifest.NeedsHumanReason = "approval required"
	manifest.UpdatedAt = "2026-04-13T17:00:02Z"
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("rewrite github manifest without touching DB: %v", err)
	}

	refreshed, err := api.buildOverview()
	if err != nil {
		t.Fatalf("buildOverview after manifest rewrite: %v", err)
	}
	if len(refreshed.WorkRuns) != 1 {
		t.Fatalf("expected one GitHub work run after manifest rewrite, got %+v", refreshed.WorkRuns)
	}
	run := refreshed.WorkRuns[0]
	if run.Status != "blocked" || run.CurrentPhase != "blocked:user" || !run.Pending || run.AttentionState != "blocked" {
		t.Fatalf("expected manifest-only change to refresh GitHub run summary, got %+v", run)
	}
}

func TestStartUIOverviewDependencyRunIndexQueryUsesUpdatedAtIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()

	rows, err := store.db.Query(`EXPLAIN QUERY PLAN SELECT backend, manifest_path FROM work_run_index ORDER BY updated_at DESC LIMIT ?`, startUIOverviewRunLimit)
	if err != nil {
		t.Fatalf("explain dependency query: %v", err)
	}
	defer rows.Close()

	details := []string{}
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read query plan: %v", err)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "idx_work_run_index_updated") {
		t.Fatalf("expected overview dependency query to use updated_at index, got plan:\n%s", plan)
	}
	if strings.Contains(strings.ToUpper(plan), "USE TEMP B-TREE") {
		t.Fatalf("expected overview dependency query to avoid temp sort, got plan:\n%s", plan)
	}
}

func TestStartUIOverviewDependenciesTrackSetupScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	setupScopePath := filepath.Join(cwd, ".nana", "setup-scope.json")
	before := snapshotStartUIOverviewDependencies(cwd)
	if !startUITestHasDependencyPath(before.deps, setupScopePath) {
		t.Fatalf("expected setup scope dependency %s in %+v", setupScopePath, before.deps)
	}

	if err := os.MkdirAll(filepath.Dir(setupScopePath), 0o755); err != nil {
		t.Fatalf("mkdir setup scope dir: %v", err)
	}
	if err := os.WriteFile(setupScopePath, []byte(`{"scope":"project"}`), 0o644); err != nil {
		t.Fatalf("write setup scope: %v", err)
	}
	after := snapshotStartUIOverviewDependencies(cwd)
	if before.token == after.token {
		t.Fatalf("expected setup scope write to change overview dependency fingerprint")
	}
}

func TestStartUIOverviewDependenciesTrackAncestorGitMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	cwd := filepath.Join(repoRoot, "nested", "pkg")
	actualGitDir := filepath.Join(root, "actual-git")
	if err := os.MkdirAll(filepath.Join(actualGitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatalf("mkdir git refs: %v", err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".git"), []byte("gitdir: ../actual-git\n"), 0o644); err != nil {
		t.Fatalf("write gitdir file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(actualGitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	if err := os.WriteFile(filepath.Join(actualGitDir, "config"), []byte("[remote \"origin\"]\n\turl = https://github.com/acme/widget.git\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(actualGitDir, "refs", "heads", "main"), []byte("0123456789012345678901234567890123456789\n"), 0o644); err != nil {
		t.Fatalf("write branch ref: %v", err)
	}

	deps := listStartUIOverviewDependencies(cwd)
	for _, want := range []string{
		filepath.Join(repoRoot, ".git"),
		filepath.Join(actualGitDir, "HEAD"),
		filepath.Join(actualGitDir, "config"),
		filepath.Join(actualGitDir, "refs", "heads", "main"),
	} {
		if !startUITestHasDependencyPath(deps, want) {
			t.Fatalf("expected git dependency %s in %+v", want, deps)
		}
	}
}

func BenchmarkStartUIBuildOverviewSyntheticMultiRepo(b *testing.B) {
	api := setupStartUISyntheticOverviewBenchmark(b, 25, 15)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		api.invalidateOverviewCache()
		if _, err := api.buildOverview(); err != nil {
			b.Fatalf("buildOverview: %v", err)
		}
	}
}

func BenchmarkStartUIBuildEventsPayloadSyntheticMultiRepo(b *testing.B) {
	api := setupStartUISyntheticOverviewBenchmark(b, 25, 15)
	if _, err := api.buildEventsPayload(); err != nil {
		b.Fatalf("warm buildEventsPayload: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := api.buildEventsPayload(); err != nil {
			b.Fatalf("buildEventsPayload: %v", err)
		}
	}
}

func setupStartUISyntheticOverviewBenchmark(b *testing.B, repoCount int, workItemCount int) *startUIAPI {
	b.Helper()
	home := b.TempDir()
	b.Setenv("HOME", home)
	cwd := b.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".nana", "state"), 0o755); err != nil {
		b.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "state", "team-state.json"), []byte(`{"active":true,"current_phase":"team-exec","agent_count":3}`), 0o644); err != nil {
		b.Fatalf("write team state: %v", err)
	}

	now := time.Now().UTC()
	for i := 0; i < repoCount; i++ {
		repoSlug := fmt.Sprintf("acme/widget-%02d", i)
		if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "repo", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
			b.Fatalf("write settings: %v", err)
		}
		state := startWorkState{
			Version:      startWorkStateVersion,
			SourceRepo:   repoSlug,
			UpdatedAt:    now.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			Issues:       map[string]startWorkIssueState{},
			ServiceTasks: map[string]startWorkServiceTask{},
			PlannedItems: map[string]startWorkPlannedItem{},
		}
		for j := 0; j < 6; j++ {
			status := startWorkStatusQueued
			if j%3 == 1 {
				status = startWorkStatusInProgress
			} else if j%3 == 2 {
				status = startWorkStatusBlocked
			}
			key := fmt.Sprintf("%d", j+1)
			state.Issues[key] = startWorkIssueState{SourceNumber: j + 1, Title: fmt.Sprintf("Issue %d", j+1), Status: status, UpdatedAt: state.UpdatedAt}
		}
		for j := 0; j < 3; j++ {
			status := startWorkServiceTaskQueued
			if j == 2 {
				status = startWorkServiceTaskRunning
			}
			key := fmt.Sprintf("task-%d", j)
			state.ServiceTasks[key] = startWorkServiceTask{ID: key, Kind: startTaskKindTriage, Queue: startTaskQueueService, Status: status, UpdatedAt: state.UpdatedAt}
		}
		state.PlannedItems["planned-1"] = startWorkPlannedItem{ID: "planned-1", RepoSlug: repoSlug, Title: "Synthetic planned work", State: startPlannedItemQueued, UpdatedAt: state.UpdatedAt}
		if err := writeStartWorkState(state); err != nil {
			b.Fatalf("write start state: %v", err)
		}
	}

	store, err := openLocalWorkDB()
	if err != nil {
		b.Fatalf("openLocalWorkDB: %v", err)
	}
	for i := 0; i < repoCount; i++ {
		repoSlug := fmt.Sprintf("acme/widget-%02d", i)
		status := "completed"
		if i%5 == 0 {
			status = "running"
		}
		if err := store.writeManifest(localWorkManifest{
			RunID:           fmt.Sprintf("lw-bench-%02d", i),
			RepoRoot:        githubManagedPaths(repoSlug).SourcePath,
			RepoName:        fmt.Sprintf("widget-%02d", i),
			RepoID:          fmt.Sprintf("repo-%02d", i),
			CreatedAt:       now.Add(-time.Duration(i+1) * time.Minute).Format(time.RFC3339),
			UpdatedAt:       now.Add(-time.Duration(i) * time.Second).Format(time.RFC3339),
			Status:          status,
			CurrentPhase:    "verify",
			SandboxPath:     filepath.Join(home, "sandbox", fmt.Sprintf("%02d", i)),
			SandboxRepoPath: filepath.Join(home, "sandbox", fmt.Sprintf("%02d", i), "repo"),
		}); err != nil {
			_ = store.Close()
			b.Fatalf("write manifest: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		b.Fatalf("close store: %v", err)
	}

	for i := 0; i < workItemCount; i++ {
		if _, _, err := enqueueWorkItem(workItemInput{
			Source:     "github",
			SourceKind: "thread_comment",
			ExternalID: fmt.Sprintf("comment-%02d", i),
			RepoSlug:   fmt.Sprintf("acme/widget-%02d", i%repoCount),
			TargetURL:  fmt.Sprintf("https://github.com/acme/widget-%02d/issues/%d", i%repoCount, i+1),
			Subject:    fmt.Sprintf("Synthetic work item %02d", i),
			Body:       "Please handle this synthetic benchmark item.",
		}, "benchmark"); err != nil {
			b.Fatalf("enqueue work item: %v", err)
		}
	}

	return &startUIAPI{cwd: cwd}
}

func startUITestHasDependencyPath(paths []string, want string) bool {
	want = filepath.Clean(want)
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func startUITestPrimeOverviewCache(t *testing.T, api *startUIAPI) {
	t.Helper()
	if _, err := api.buildOverview(); err != nil {
		t.Fatalf("buildOverview: %v", err)
	}
	if !startUITestOverviewCacheValid(api) {
		t.Fatalf("expected overview cache to be valid after warm build")
	}
}

func startUITestOverviewCacheValid(api *startUIAPI) bool {
	api.overviewCacheMu.Lock()
	defer api.overviewCacheMu.Unlock()
	return api.overviewCache.valid
}

func startUITestCreateQueuedWorkItem(t *testing.T, externalID string) workItem {
	t.Helper()
	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "test",
		SourceKind: "reply",
		ExternalID: externalID,
		RepoSlug:   "acme/widget",
		Subject:    "Work item " + externalID,
		Body:       "Please handle this item.",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	return item
}

func startUITestCreateDraftWorkItem(t *testing.T, externalID string) workItem {
	t.Helper()
	item := startUITestCreateQueuedWorkItem(t, externalID)
	item.LatestDraft = &workItemDraft{Kind: "reply", Body: "Draft reply", Summary: "Draft summary", Confidence: 0.8}
	item.Status = workItemStatusDraftReady
	startUITestUpdateWorkItem(t, item)
	return item
}

func startUITestUpdateWorkItem(t *testing.T, item workItem) {
	t.Helper()
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
}

func startUITestFailWorkItemEvents(t *testing.T) {
	t.Helper()
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`CREATE TRIGGER fail_work_item_events BEFORE INSERT ON work_item_events BEGIN SELECT RAISE(FAIL, 'event insert failed'); END;`); err != nil {
		t.Fatalf("create failing work item event trigger: %v", err)
	}
}

func startUITestReadWorkItem(t *testing.T, itemID string) workItem {
	t.Helper()
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	item, err := store.readWorkItem(itemID)
	if err != nil {
		t.Fatalf("readWorkItem: %v", err)
	}
	return item
}
