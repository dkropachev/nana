package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkStartUIBuildOverviewRepresentativeRepo(b *testing.B) {
	b.Run("cold", func(b *testing.B) {
		api := setupStartUIRepresentativeOverviewBenchmark(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			api.invalidateOverviewCache()
			if _, err := api.buildOverview(); err != nil {
				b.Fatalf("buildOverview(cold): %v", err)
			}
		}
	})

	b.Run("warm", func(b *testing.B) {
		api := setupStartUIRepresentativeOverviewBenchmark(b)
		previousOverviewInterval := startUIOverviewCacheProbeInterval
		previousSectionInterval := startUISectionCacheProbeInterval
		startUIOverviewCacheProbeInterval = time.Hour
		startUISectionCacheProbeInterval = time.Hour
		defer func() {
			startUIOverviewCacheProbeInterval = previousOverviewInterval
			startUISectionCacheProbeInterval = previousSectionInterval
		}()

		if _, err := api.buildOverview(); err != nil {
			b.Fatalf("seed buildOverview(warm): %v", err)
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := api.buildOverview(); err != nil {
				b.Fatalf("buildOverview(warm): %v", err)
			}
		}
	})

	b.Run("after-hud-change", func(b *testing.B) {
		api := setupStartUIRepresentativeOverviewBenchmark(b)
		previousOverviewInterval := startUIOverviewCacheProbeInterval
		previousSectionInterval := startUISectionCacheProbeInterval
		startUIOverviewCacheProbeInterval = time.Hour
		startUISectionCacheProbeInterval = time.Hour
		defer func() {
			startUIOverviewCacheProbeInterval = previousOverviewInterval
			startUISectionCacheProbeInterval = previousSectionInterval
		}()

		if _, err := api.buildOverview(); err != nil {
			b.Fatalf("seed buildOverview(after-hud-change): %v", err)
		}

		metricsPath := filepath.Join(api.cwd, ".nana", "metrics.json")
		metricsVersion := 0
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			metricsVersion++
			if err := os.WriteFile(metricsPath, []byte(fmt.Sprintf("{\"tokens_used\":%d}\n", metricsVersion)), 0o644); err != nil {
				b.Fatalf("write metrics: %v", err)
			}
			api.overviewCacheMu.Lock()
			api.overviewCache.checkedAt = time.Time{}
			api.overviewCacheMu.Unlock()
			b.StartTimer()

			if _, err := api.buildOverview(); err != nil {
				b.Fatalf("buildOverview(after-hud-change): %v", err)
			}
		}
	})
}

func setupStartUIRepresentativeOverviewBenchmark(b *testing.B) *startUIAPI {
	b.Helper()
	home := b.TempDir()
	b.Setenv("HOME", home)

	cwd := createLocalWorkRepoAt(b, filepath.Join(home, "workspace"))
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		b.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "team-state.json"), []byte(`{"active":true,"current_phase":"team-exec","agent_count":2}`), 0o644); err != nil {
		b.Fatalf("write team-state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "metrics.json"), []byte("{\"tokens_used\":0}\n"), 0o644); err != nil {
		b.Fatalf("write metrics: %v", err)
	}

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(b, sourcePath)
	if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, improvementScoutRole, false), improvementPolicy{
		Version:          1,
		Mode:             "auto",
		Schedule:         scoutScheduleWeekly,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{"ux"},
	}); err != nil {
		b.Fatalf("write improvement policy: %v", err)
	}
	if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, enhancementScoutRole, false), improvementPolicy{
		Version:          1,
		Mode:             "manual",
		Schedule:         scoutScheduleDaily,
		IssueDestination: improvementDestinationFork,
		ForkRepo:         "me/widget",
		Labels:           []string{"forward"},
	}); err != nil {
		b.Fatalf("write enhancement policy: %v", err)
	}
	if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, uiScoutRole, false), improvementPolicy{
		Version:      1,
		Mode:         "manual",
		Schedule:     scoutScheduleWhenResolved,
		Labels:       []string{"qa"},
		SessionLimit: 5,
	}); err != nil {
		b.Fatalf("write ui policy: %v", err)
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
		b.Fatalf("write settings: %v", err)
	}

	now := time.Now().UTC()
	state := startWorkState{
		Version:      startWorkStateVersion,
		SourceRepo:   repoSlug,
		ForkRepo:     "me/widget",
		UpdatedAt:    now.Format(time.RFC3339),
		Issues:       map[string]startWorkIssueState{},
		ServiceTasks: map[string]startWorkServiceTask{},
		PlannedItems: map[string]startWorkPlannedItem{},
		ScoutJobs:    map[string]startWorkScoutJob{},
	}
	for i := 1; i <= 8; i++ {
		status := startWorkStatusQueued
		if i%3 == 1 {
			status = startWorkStatusInProgress
		} else if i%3 == 2 {
			status = startWorkStatusBlocked
		}
		key := fmt.Sprintf("%d", i)
		timestamp := now.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		state.Issues[key] = startWorkIssueState{
			SourceNumber:      i,
			ForkNumber:        100 + i,
			SourceURL:         fmt.Sprintf("https://github.com/acme/widget/issues/%d", i),
			State:             "open",
			Title:             fmt.Sprintf("Representative issue %d", i),
			Status:            status,
			Labels:            []string{"nana"},
			Priority:          3,
			PrioritySource:    "triage",
			Complexity:        2,
			SourceFingerprint: fmt.Sprintf("issue-fp-%d", i),
			TriageFingerprint: fmt.Sprintf("triage-fp-%d", i),
			TriageStatus:      startWorkTriageCompleted,
			UpdatedAt:         timestamp,
		}
		state.ServiceTasks[fmt.Sprintf("triage:%d", i)] = startWorkServiceTask{
			ID:        fmt.Sprintf("triage:%d", i),
			Kind:      startTaskKindTriage,
			Queue:     startTaskQueueService,
			Status:    startWorkServiceTaskQueued,
			IssueKey:  key,
			UpdatedAt: timestamp,
		}
	}
	state.PlannedItems["planned-1"] = startWorkPlannedItem{
		ID:        "planned-1",
		RepoSlug:  repoSlug,
		Title:     "Warm the staging environment",
		Priority:  2,
		State:     startPlannedItemQueued,
		CreatedAt: now.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
	}
	for i := 0; i < 40; i++ {
		status := startScoutJobDismissed
		if i%10 == 0 {
			status = startScoutJobFailed
		} else if i%10 == 1 {
			status = startScoutJobCompleted
		}
		lastError := ""
		if status == startScoutJobFailed {
			lastError = "review failed"
		}
		id := fmt.Sprintf("scout-%02d", i)
		state.ScoutJobs[id] = startWorkScoutJob{
			ID:          id,
			Role:        improvementScoutRole,
			Title:       fmt.Sprintf("Scout proposal %02d", i),
			Summary:     "Representative overview benchmark scout proposal.",
			Destination: improvementDestinationLocal,
			TaskBody:    "Implement local scout proposal",
			Status:      status,
			CreatedAt:   now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
			UpdatedAt:   now.Add(-time.Duration(i) * time.Second).Format(time.RFC3339),
			LastError:   lastError,
		}
	}
	if err := writeStartWorkState(state); err != nil {
		b.Fatalf("write start state: %v", err)
	}

	store, err := openLocalWorkDB()
	if err != nil {
		b.Fatalf("open work db: %v", err)
	}
	for i := 0; i < 4; i++ {
		status := "completed"
		if i == 0 {
			status = "running"
		}
		if err := store.writeManifest(localWorkManifest{
			RunID:           fmt.Sprintf("lw-ui-%d", i),
			RepoRoot:        sourcePath,
			RepoName:        "widget",
			RepoID:          "repo-ui",
			CreatedAt:       now.Add(-time.Duration(i+1) * time.Minute).Format(time.RFC3339),
			UpdatedAt:       now.Add(-time.Duration(i) * time.Second).Format(time.RFC3339),
			Status:          status,
			CurrentPhase:    "verify",
			SandboxPath:     filepath.Join(home, "sandbox", fmt.Sprintf("%d", i)),
			SandboxRepoPath: filepath.Join(home, "sandbox", fmt.Sprintf("%d", i), "repo"),
		}); err != nil {
			_ = store.Close()
			b.Fatalf("write manifest: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		b.Fatalf("close work db: %v", err)
	}

	for i := 0; i < 8; i++ {
		if _, _, err := enqueueWorkItem(workItemInput{
			Source:     "github",
			SourceKind: "thread_comment",
			ExternalID: fmt.Sprintf("comment-%02d", i),
			RepoSlug:   repoSlug,
			TargetURL:  fmt.Sprintf("https://github.com/acme/widget/issues/%d", i+1),
			Subject:    fmt.Sprintf("Representative work item %02d", i),
			Body:       "Please handle this benchmark work item.",
		}, "benchmark"); err != nil {
			b.Fatalf("enqueue work item: %v", err)
		}
	}

	return &startUIAPI{cwd: cwd}
}
