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
	if err := writeGithubJSON(githubRepoSettingsPath("acme/widget"), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve", ForkIssuesMode: "auto", ImplementMode: "auto", PublishTarget: "fork"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: "acme/widget",
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

	patchBody := strings.NewReader(`{"priority":1,"schedule_at":"2026-04-14T15:00:00Z","deferred_reason":"wait for release train"}`)
	request, err := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/repos/acme/widget/issues/7", patchBody)
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
	createResponse, err := http.Post(server.URL+"/api/v1/repos/acme/widget/planned-items", "application/json", createBody)
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
	overviewResponse, err := http.Get(server.URL + "/api/v1/overview")
	if err != nil {
		t.Fatalf("GET overview after create: %v", err)
	}
	defer overviewResponse.Body.Close()
	var updatedOverview startUIOverview
	if err := json.NewDecoder(overviewResponse.Body).Decode(&updatedOverview); err != nil {
		t.Fatalf("decode updated overview: %v", err)
	}
	if updatedOverview.Totals.PlannedQueued != 2 {
		t.Fatalf("expected cached overview to refresh after planned-item create, got %+v", updatedOverview.Totals)
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

func TestStartUIEventsPayloadCachesOverviewUntilInputsChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	api := &startUIAPI{cwd: cwd}

	firstPayload, err := api.buildEventsPayload()
	if err != nil {
		t.Fatalf("build first events payload: %v", err)
	}
	firstGeneratedAt, ok := firstPayload["generated_at"].(string)
	if !ok || firstGeneratedAt == "" {
		t.Fatalf("missing first generated_at: %+v", firstPayload)
	}

	time.Sleep(1100 * time.Millisecond)
	secondPayload, err := api.buildEventsPayload()
	if err != nil {
		t.Fatalf("build second events payload: %v", err)
	}
	if secondGeneratedAt := secondPayload["generated_at"]; secondGeneratedAt != firstGeneratedAt {
		t.Fatalf("expected unchanged inputs to reuse cached generated_at %q, got %q", firstGeneratedAt, secondGeneratedAt)
	}

	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "team-state.json"), []byte(`{"active":true,"current_phase":"team-exec","agent_count":1}`), 0o644); err != nil {
		t.Fatalf("write team-state: %v", err)
	}
	thirdPayload, err := api.buildEventsPayload()
	if err != nil {
		t.Fatalf("build third events payload: %v", err)
	}
	if thirdGeneratedAt := thirdPayload["generated_at"]; thirdGeneratedAt == firstGeneratedAt {
		t.Fatalf("expected changed inputs to refresh cached overview, generated_at stayed %q", thirdGeneratedAt)
	}
	hud, ok := thirdPayload["hud"].(HUDRenderContext)
	if !ok || hud.Team == nil || hud.Team.AgentCount != 1 {
		t.Fatalf("expected refreshed HUD team state, got %#v", thirdPayload["hud"])
	}
}

func TestStartUIOverviewSignatureWatchesOnlyHUDFilesReadByOverview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	stateRoot := BaseStateDir(cwd)
	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		t.Fatalf("mkdir state root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "session.json"), []byte(`{"session_id":"current-session"}`), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	unwatchedPath := filepath.Join(stateRoot, "sessions", "old-session", "deep", "noise.json")
	if err := os.MkdirAll(filepath.Dir(unwatchedPath), 0o755); err != nil {
		t.Fatalf("mkdir unwatched dir: %v", err)
	}
	if err := os.WriteFile(unwatchedPath, []byte(`{"ignored":1}`), 0o644); err != nil {
		t.Fatalf("write unwatched file: %v", err)
	}

	firstSignature, err := startUIOverviewSignature(cwd)
	if err != nil {
		t.Fatalf("first overview signature: %v", err)
	}
	if err := os.WriteFile(unwatchedPath, []byte(`{"ignored":2,"padding":"changes outside the active HUD state files must not affect the cache"}`), 0o644); err != nil {
		t.Fatalf("rewrite unwatched file: %v", err)
	}
	secondSignature, err := startUIOverviewSignature(cwd)
	if err != nil {
		t.Fatalf("second overview signature: %v", err)
	}
	if secondSignature != firstSignature {
		t.Fatalf("expected unrelated HUD state-tree changes to be ignored, got %q then %q", firstSignature, secondSignature)
	}

	currentTeamPath := filepath.Join(stateRoot, "sessions", "current-session", "team-state.json")
	if err := os.MkdirAll(filepath.Dir(currentTeamPath), 0o755); err != nil {
		t.Fatalf("mkdir current session dir: %v", err)
	}
	if err := os.WriteFile(currentTeamPath, []byte(`{"active":true,"current_phase":"team-exec","agent_count":3}`), 0o644); err != nil {
		t.Fatalf("write current session team state: %v", err)
	}
	thirdSignature, err := startUIOverviewSignature(cwd)
	if err != nil {
		t.Fatalf("third overview signature: %v", err)
	}
	if thirdSignature == secondSignature {
		t.Fatalf("expected active session HUD state changes to refresh overview signature %q", thirdSignature)
	}
}

func TestStartUIOverviewSignatureIgnoresManagedRepoNonSettingsSubtrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	settingsPath := githubRepoSettingsPath("acme/widget")
	if err := writeGithubJSON(settingsPath, githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write repo settings: %v", err)
	}
	nestedSettingsPath := filepath.Join(githubWorkRepoRoot("acme/widget"), "source", "pkg", "settings.json")
	if err := os.MkdirAll(filepath.Dir(nestedSettingsPath), 0o755); err != nil {
		t.Fatalf("mkdir nested source settings dir: %v", err)
	}
	if err := os.WriteFile(nestedSettingsPath, []byte(`{"nested":false}`), 0o644); err != nil {
		t.Fatalf("write nested source settings: %v", err)
	}

	repos, err := listOnboardedGithubRepos()
	if err != nil {
		t.Fatalf("list onboarded repos: %v", err)
	}
	if len(repos) != 1 || repos[0] != "acme/widget" {
		t.Fatalf("expected only the managed repo settings file to define onboarded repos, got %v", repos)
	}

	firstSignature, err := startUIOverviewSignature(cwd)
	if err != nil {
		t.Fatalf("first overview signature: %v", err)
	}
	if err := os.WriteFile(nestedSettingsPath, []byte(`{"nested":true,"padding":"source checkout changes must not invalidate the start UI overview cache"}`), 0o644); err != nil {
		t.Fatalf("rewrite nested source settings: %v", err)
	}
	secondSignature, err := startUIOverviewSignature(cwd)
	if err != nil {
		t.Fatalf("second overview signature: %v", err)
	}
	if secondSignature != firstSignature {
		t.Fatalf("expected source subtree settings changes to be ignored by overview signature, got %q then %q", firstSignature, secondSignature)
	}

	if err := os.WriteFile(settingsPath, []byte(`{"version":6,"repo_mode":"repo","issue_pick":"auto","pr_forward":"approve","changed":true}`), 0o644); err != nil {
		t.Fatalf("rewrite managed repo settings: %v", err)
	}
	thirdSignature, err := startUIOverviewSignature(cwd)
	if err != nil {
		t.Fatalf("third overview signature: %v", err)
	}
	if thirdSignature == secondSignature {
		t.Fatalf("expected managed repo settings changes to refresh overview signature %q", thirdSignature)
	}
}

func TestStartUIOverviewCacheRefreshesGitBranchFromRepoSubdirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
		}
	}
	runGit("init")
	runGit("checkout", "-b", "main")
	runGit("config", "user.email", "nana@example.invalid")
	runGit("config", "user.name", "Nana Test")
	runGit("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "init")
	runGit("checkout", "-b", "feature")
	runGit("checkout", "main")

	subdir := filepath.Join(repo, "pkg", "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	api := &startUIAPI{cwd: subdir}
	first, err := api.buildOverview()
	if err != nil {
		t.Fatalf("build first overview: %v", err)
	}
	if !strings.HasSuffix(first.HUD.GitBranch, "/main") {
		t.Fatalf("expected first HUD branch to come from main, got %q", first.HUD.GitBranch)
	}

	runGit("checkout", "feature")
	second, err := api.buildOverview()
	if err != nil {
		t.Fatalf("build second overview: %v", err)
	}
	if !strings.HasSuffix(second.HUD.GitBranch, "/feature") {
		t.Fatalf("expected cached overview to refresh after branch switch, got %q", second.HUD.GitBranch)
	}
}

func TestStartUIOverviewCacheRefreshesWhenGithubManifestChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	runDir := filepath.Join(githubWorkRepoRoot("acme/widget"), "runs", "gh-ui")
	manifestPath := filepath.Join(runDir, "manifest.json")
	manifest := githubWorkManifest{
		Version:          1,
		RunID:            "gh-ui",
		RepoSlug:         "acme/widget",
		RepoOwner:        "acme",
		RepoName:         "widget",
		TargetURL:        "https://github.com/acme/widget/issues/1",
		TargetKind:       "issue",
		UpdatedAt:        "2026-04-18T10:00:00Z",
		PublicationState: "active",
		NextAction:       "review",
	}
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index manifest: %v", err)
	}

	api := &startUIAPI{cwd: cwd}
	first, err := api.buildOverview()
	if err != nil {
		t.Fatalf("build first overview: %v", err)
	}
	if len(first.WorkRuns) != 1 || first.WorkRuns[0].Status != "active" || first.WorkRuns[0].CurrentPhase != "review" {
		t.Fatalf("unexpected first work run: %+v", first.WorkRuns)
	}

	manifest.RepoName = "widget-renamed"
	manifest.TargetURL = "https://github.com/acme/widget/pull/22"
	manifest.UpdatedAt = "2026-04-18T10:01:00Z"
	manifest.PublicationState = "completed"
	manifest.NextAction = "done"
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	second, err := api.buildOverview()
	if err != nil {
		t.Fatalf("build second overview: %v", err)
	}
	if len(second.WorkRuns) != 1 {
		t.Fatalf("expected one work run after manifest rewrite, got %+v", second.WorkRuns)
	}
	run := second.WorkRuns[0]
	if run.RepoName != "widget-renamed" || run.Status != "completed" || run.CurrentPhase != "done" || run.TargetURL != "https://github.com/acme/widget/pull/22" {
		t.Fatalf("expected cached overview to refresh from rewritten manifest, got %+v", run)
	}
}

func TestStartUIWorkRunManifestSignatureQueryUsesUpdatedAtIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("open work db: %v", err)
	}
	defer store.Close()

	rows, err := store.db.Query(`EXPLAIN QUERY PLAN SELECT backend, manifest_path FROM work_run_index ORDER BY updated_at DESC LIMIT ?`, startUIWorkRunLimit)
	if err != nil {
		t.Fatalf("explain work run manifest signature query: %v", err)
	}
	defer rows.Close()
	details := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read query plan: %v", err)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "idx_work_run_index_updated") {
		t.Fatalf("expected manifest signature query to use global updated_at index, plan:\n%s", plan)
	}
	if strings.Contains(strings.ToUpper(plan), "USE TEMP B-TREE") {
		t.Fatalf("expected manifest signature query to avoid temp sort, plan:\n%s", plan)
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
}

func TestStartUIWebHandlerServesCalendarWithoutSyntheticScheduleFallback(t *testing.T) {
	server := httptest.NewServer(startUIWebHandler("http://127.0.0.1:17653"))
	defer server.Close()

	response, err := http.Get(server.URL + "/app.js")
	if err != nil {
		t.Fatalf("GET /app.js: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(body)
	if !strings.Contains(script, `const scheduledItems = items.filter((item) => (item.schedule_at || "").trim());`) {
		t.Fatalf("expected scheduled-items filter in calendar renderer, got %s", script)
	}
	if strings.Contains(script, `start: item.schedule_at || new Date().toISOString()`) || strings.Contains(script, `end: item.schedule_at || new Date().toISOString()`) {
		t.Fatalf("expected calendar renderer to avoid synthetic schedule fallback, got %s", script)
	}
	if !strings.Contains(script, `No scheduled planned items`) {
		t.Fatalf("expected empty-state message for unscheduled calendar, got %s", script)
	}
}

func BenchmarkStartUIOverviewMultiRepo(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	cwd := b.TempDir()
	seedStartUIMultiRepoFixture(b, 24, 8)
	api := &startUIAPI{cwd: cwd}

	b.Run("uncached-overview", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := api.buildOverviewUncached(); err != nil {
				b.Fatalf("buildOverviewUncached: %v", err)
			}
		}
	})

	b.Run("cached-events-payload", func(b *testing.B) {
		api.invalidateOverviewCache()
		if _, err := api.buildEventsPayload(); err != nil {
			b.Fatalf("prime buildEventsPayload: %v", err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := api.buildEventsPayload(); err != nil {
				b.Fatalf("buildEventsPayload: %v", err)
			}
		}
	})
}

func BenchmarkStartUIEventsPayloadCacheHitLargeWorkRunHistory(b *testing.B) {
	for _, rowCount := range []int{10_000, 100_000} {
		rowCount := rowCount
		b.Run(fmt.Sprintf("work-run-index-%d", rowCount), func(b *testing.B) {
			home := b.TempDir()
			b.Setenv("HOME", home)
			cwd := b.TempDir()
			seedStartUIWorkRunIndexHistory(b, rowCount)
			api := &startUIAPI{cwd: cwd}
			if _, err := api.buildEventsPayload(); err != nil {
				b.Fatalf("prime buildEventsPayload: %v", err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := api.buildEventsPayload(); err != nil {
					b.Fatalf("buildEventsPayload: %v", err)
				}
			}
		})
	}
}

func seedStartUIMultiRepoFixture(tb testing.TB, repoCount int, runCount int) {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < repoCount; i++ {
		repoSlug := fmt.Sprintf("bench-owner-%02d/bench-repo-%02d", i, i)
		if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
			Version:        6,
			RepoMode:       "fork",
			IssuePickMode:  "auto",
			PRForwardMode:  "approve",
			ForkIssuesMode: "auto",
			ImplementMode:  "auto",
			PublishTarget:  "fork",
		}); err != nil {
			tb.Fatalf("write repo settings: %v", err)
		}
		if err := writeStartWorkState(startWorkState{
			Version:    startWorkStateVersion,
			SourceRepo: repoSlug,
			UpdatedAt:  now,
			Issues: map[string]startWorkIssueState{
				"1": {SourceNumber: 1, Status: startWorkStatusQueued, UpdatedAt: now},
				"2": {SourceNumber: 2, Status: startWorkStatusInProgress, UpdatedAt: now},
			},
			ServiceTasks: map[string]startWorkServiceTask{
				"triage:1": {ID: "triage:1", Status: startWorkServiceTaskQueued, UpdatedAt: now},
			},
			PlannedItems: map[string]startWorkPlannedItem{
				"planned-1": {ID: "planned-1", RepoSlug: repoSlug, Title: "Bench item", State: startPlannedItemQueued, UpdatedAt: now},
			},
		}); err != nil {
			tb.Fatalf("write start state: %v", err)
		}
	}
	store, err := openLocalWorkDB()
	if err != nil {
		tb.Fatalf("open work db: %v", err)
	}
	defer store.Close()
	for i := 0; i < runCount; i++ {
		status := "completed"
		if i%3 == 0 {
			status = "running"
		}
		if err := store.writeManifest(localWorkManifest{
			RunID:           fmt.Sprintf("lw-bench-%02d", i),
			RepoRoot:        filepath.Join(githubNanaHome(), "bench-repos", fmt.Sprintf("repo-%02d", i)),
			RepoName:        fmt.Sprintf("repo-%02d", i),
			RepoID:          fmt.Sprintf("repo-bench-%02d", i),
			CreatedAt:       now,
			UpdatedAt:       now,
			Status:          status,
			CurrentPhase:    "verify",
			SandboxPath:     filepath.Join(githubNanaHome(), "bench-sandboxes", fmt.Sprintf("run-%02d", i)),
			SandboxRepoPath: filepath.Join(githubNanaHome(), "bench-sandboxes", fmt.Sprintf("run-%02d", i), "repo"),
		}); err != nil {
			tb.Fatalf("write manifest: %v", err)
		}
	}
}

func seedStartUIWorkRunIndexHistory(tb testing.TB, count int) {
	tb.Helper()
	store, err := openLocalWorkDB()
	if err != nil {
		tb.Fatalf("open work db: %v", err)
	}
	defer store.Close()
	tx, err := store.db.Begin()
	if err != nil {
		tb.Fatalf("begin seed work run index tx: %v", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO work_run_index(run_id, backend, repo_key, repo_root, repo_name, manifest_path, updated_at, target_kind) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatalf("prepare seed work run index: %v", err)
	}
	baseTime := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	manifestRoot := filepath.Join(githubNanaHome(), "bench-manifests")
	repoRoot := filepath.Join(githubNanaHome(), "bench-repos")
	for i := 0; i < count; i++ {
		backend := "local"
		manifestPath := ""
		if i%2 == 0 {
			backend = "github"
			manifestPath = filepath.Join(manifestRoot, fmt.Sprintf("manifest-%06d.json", i))
		}
		_, err := stmt.Exec(
			fmt.Sprintf("history-run-%06d", i),
			backend,
			"acme/widget",
			filepath.Join(repoRoot, fmt.Sprintf("repo-%03d", i%100)),
			"widget",
			nullableString(manifestPath),
			baseTime.Add(time.Duration(i)*time.Second).Format(time.RFC3339),
			backend,
		)
		if err != nil {
			tb.Fatalf("seed work run index row %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		tb.Fatalf("close seed work run index stmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit seed work run index: %v", err)
	}
}
