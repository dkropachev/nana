package gocli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncStartWorkScoutJobsIntoStateBlocksWhenSourceWriteLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repoSlug := "acme/widget"
	repoPath := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	writeScoutPickupFixture(t, repoPath, improvementScoutRole, "Improve help text", "Make help clearer")

	lock, err := acquireManagedSourceWriteLock(repoSlug, repoAccessLockOwner{
		Backend: "test",
		RunID:   "scout-jobs-writer",
		Purpose: "source-setup",
		Label:   "scout-jobs-writer",
	})
	if err != nil {
		t.Fatalf("acquire source write lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	_, err = syncStartWorkScoutJobsIntoState(repoPath, &startWorkState{
		SourceRepo:   repoSlug,
		ScoutJobs:    map[string]startWorkScoutJob{},
		PlannedItems: map[string]startWorkPlannedItem{},
	})
	if err == nil || !strings.Contains(err.Error(), "repo read lock busy") {
		t.Fatalf("expected repo read lock busy, got %v", err)
	}
}

func TestCountOutstandingLegacyLocalScoutItemsBlocksWhenSourceWriteLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repo := createLocalWorkRepoAt(t, t.TempDir())
	writeScoutPickupFixture(t, repo, improvementScoutRole, "Improve help text", "Make help clearer")

	lock, err := acquireSourceWriteLock(repo, repoAccessLockOwner{
		Backend: "test",
		RunID:   "legacy-outstanding-writer",
		Purpose: "source-setup",
		Label:   "legacy-outstanding-writer",
	})
	if err != nil {
		t.Fatalf("acquire source write lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	_, err = countOutstandingLegacyLocalScoutItems(repo, improvementScoutRole)
	if err == nil || !strings.Contains(err.Error(), "repo read lock busy") {
		t.Fatalf("expected repo read lock busy, got %v", err)
	}
}

func TestReconcileStartWorkScoutJobRunStateHealsStaleFailureWhenRunCompletes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repoRoot := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	now := time.Now().UTC().Format(time.RFC3339)
	manifest := localWorkManifest{
		Version:         1,
		RunID:           "lw-scout-complete",
		CreatedAt:       now,
		UpdatedAt:       now,
		CompletedAt:     now,
		Status:          "completed",
		CurrentPhase:    "completed",
		RepoRoot:        repoRoot,
		RepoName:        filepath.Base(repoRoot),
		RepoID:          localWorkRepoID(repoRoot),
		SourceBranch:    "main",
		BaselineSHA:     strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:     filepath.Join(home, "sandboxes", "lw-scout-complete"),
		SandboxRepoPath: filepath.Join(home, "sandboxes", "lw-scout-complete", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	job := startWorkScoutJob{
		ID:                 "proposal-1",
		Status:             startScoutJobFailed,
		RunID:              manifest.RunID,
		LastError:          localWorkStaleCleanupError,
		RecoveryCount:      1,
		LastRecoveryReason: localWorkStaleCleanupError,
		LastRecoveryAt:     now,
		LastRecoveredRunID: manifest.RunID,
		UpdatedAt:          now,
	}
	reconcileStartWorkScoutJobRunState(&job)
	if job.Status != startScoutJobCompleted {
		t.Fatalf("expected completed scout job after reconcile, got %+v", job)
	}
	if job.LastError != "" {
		t.Fatalf("expected stale error to be cleared, got %+v", job)
	}
	if startWorkScoutJobHasRecoveryMetadata(job) {
		t.Fatalf("expected completed scout job to clear recovery metadata, got %+v", job)
	}
}

func TestReconcileStartWorkScoutJobRunStateAutoRequeuesStaleStartupCleanupOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repoRoot := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	now := time.Now().UTC().Format(time.RFC3339)
	manifest := localWorkManifest{
		Version:          1,
		RunID:            "lw-scout-stale-startup",
		CreatedAt:        now,
		UpdatedAt:        now,
		CompletedAt:      now,
		Status:           "failed",
		CurrentPhase:     "bootstrap",
		CurrentIteration: 0,
		RepoRoot:         repoRoot,
		RepoName:         filepath.Base(repoRoot),
		RepoID:           localWorkRepoID(repoRoot),
		SourceBranch:     "main",
		BaselineSHA:      strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:      filepath.Join(home, "sandboxes", "lw-scout-stale-startup"),
		SandboxRepoPath:  filepath.Join(home, "sandboxes", "lw-scout-stale-startup", "repo"),
		LastError:        localWorkStaleCleanupError,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	job := startWorkScoutJob{
		ID:          "proposal-1",
		Destination: improvementDestinationLocal,
		Status:      startScoutJobFailed,
		RunID:       manifest.RunID,
		Attempts:    1,
		LastError:   localWorkStaleCleanupError,
		UpdatedAt:   now,
	}
	before := time.Now().UTC()
	reconcileStartWorkScoutJobRunState(&job)
	after := time.Now().UTC()
	if job.Status != startScoutJobQueued || job.RunID != "" {
		t.Fatalf("expected stale startup cleanup to auto-requeue scout job, got %+v", job)
	}
	if job.PauseReason != startWorkScoutJobStaleRetryPauseReason {
		t.Fatalf("expected stale retry pause reason, got %+v", job)
	}
	if job.LastError != localWorkStaleCleanupError {
		t.Fatalf("expected stale cleanup error to remain visible, got %+v", job)
	}
	if job.RecoveryCount != 1 || job.LastRecoveryReason != localWorkStaleCleanupError || job.LastRecoveredRunID != manifest.RunID || strings.TrimSpace(job.LastRecoveryAt) == "" {
		t.Fatalf("expected structured stale recovery metadata, got %+v", job)
	}
	pauseUntil, err := time.Parse(time.RFC3339Nano, job.PauseUntil)
	if err != nil {
		t.Fatalf("parse pause until: %v", err)
	}
	if pauseUntil.Before(before.Add(startWorkScoutJobStaleRetryCooldown-time.Minute)) || pauseUntil.After(after.Add(startWorkScoutJobStaleRetryCooldown+time.Minute)) {
		t.Fatalf("expected pause until near cooldown window, got %s", job.PauseUntil)
	}
}

func TestReconcileStartWorkScoutJobRunStateRequeuesStaleCleanupWhenManifestMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now().UTC().Format(time.RFC3339)
	job := startWorkScoutJob{
		ID:          "proposal-1",
		Destination: improvementDestinationLocal,
		Status:      startScoutJobFailed,
		RunID:       "lw-missing-stale-manifest",
		Attempts:    79,
		LastError:   localWorkStaleCleanupError,
		UpdatedAt:   now,
	}
	reconcileStartWorkScoutJobRunState(&job)
	if job.Status != startScoutJobQueued || job.RunID != "" {
		t.Fatalf("expected missing-manifest stale scout job to requeue, got %+v", job)
	}
	if job.LastError != localWorkStaleCleanupError {
		t.Fatalf("expected stale cleanup error to remain visible, got %+v", job)
	}
	if job.RecoveryCount != 1 || job.LastRecoveryReason != localWorkStaleCleanupError || job.LastRecoveredRunID != "lw-missing-stale-manifest" || strings.TrimSpace(job.LastRecoveryAt) == "" {
		t.Fatalf("expected recovery metadata for missing-manifest stale scout job, got %+v", job)
	}
}

func TestReconcileStartWorkScoutJobRunStateRequeuesRepeatedStaleStartupCleanup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repoRoot := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	now := time.Now().UTC().Format(time.RFC3339)
	manifest := localWorkManifest{
		Version:          1,
		RunID:            "lw-scout-stale-repeat",
		CreatedAt:        now,
		UpdatedAt:        now,
		CompletedAt:      now,
		Status:           "failed",
		CurrentPhase:     "bootstrap",
		CurrentIteration: 0,
		RepoRoot:         repoRoot,
		RepoName:         filepath.Base(repoRoot),
		RepoID:           localWorkRepoID(repoRoot),
		SourceBranch:     "main",
		BaselineSHA:      strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:      filepath.Join(home, "sandboxes", "lw-scout-stale-repeat"),
		SandboxRepoPath:  filepath.Join(home, "sandboxes", "lw-scout-stale-repeat", "repo"),
		LastError:        localWorkStaleCleanupError,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	job := startWorkScoutJob{
		ID:          "proposal-1",
		Destination: improvementDestinationLocal,
		Status:      startScoutJobFailed,
		RunID:       manifest.RunID,
		Attempts:    2,
		LastError:   localWorkStaleCleanupError,
		UpdatedAt:   now,
	}
	reconcileStartWorkScoutJobRunState(&job)
	if job.Status != startScoutJobQueued || job.RunID != "" {
		t.Fatalf("expected repeated stale cleanup to return to queued, got %+v", job)
	}
	if job.PauseReason != "" || job.PauseUntil != "" {
		t.Fatalf("expected repeated stale cleanup to avoid auto-pause, got %+v", job)
	}
	if job.RecoveryCount != 1 || job.LastRecoveryReason != localWorkStaleCleanupError || job.LastRecoveredRunID != manifest.RunID || job.LastRecoveryAt == "" {
		t.Fatalf("expected repeated stale cleanup to record recovery metadata, got %+v", job)
	}
}

func TestReconcileStartWorkScoutJobRunStateKeepsPausedRunBoundToRunningJob(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repoRoot := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	now := time.Now().UTC().Format(time.RFC3339)
	pauseUntil := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339)
	manifest := localWorkManifest{
		Version:         1,
		RunID:           "lw-scout-paused",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          "paused",
		CurrentPhase:    "review",
		RepoRoot:        repoRoot,
		RepoName:        filepath.Base(repoRoot),
		RepoID:          localWorkRepoID(repoRoot),
		SourceBranch:    "main",
		BaselineSHA:     strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:     filepath.Join(home, "sandboxes", "lw-scout-paused"),
		SandboxRepoPath: filepath.Join(home, "sandboxes", "lw-scout-paused", "repo"),
		LastError:       "usage limit reached",
		PauseReason:     "rate limited",
		PauseUntil:      pauseUntil,
		PausedAt:        now,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	job := startWorkScoutJob{
		ID:        "proposal-1",
		Status:    startScoutJobRunning,
		RunID:     manifest.RunID,
		UpdatedAt: now,
	}
	reconcileStartWorkScoutJobRunState(&job)
	if job.Status != startScoutJobRunning {
		t.Fatalf("expected paused run to stay attached to running scout job, got %+v", job)
	}
	if job.PauseReason != manifest.PauseReason || job.PauseUntil != manifest.PauseUntil {
		t.Fatalf("expected pause fields to mirror manifest, got %+v", job)
	}
	if job.LastError != manifest.LastError {
		t.Fatalf("expected paused run error to be preserved, got %+v", job)
	}
}

func TestReconcileStartWorkScoutJobRunStateKeepsProgressedStaleCleanupFailed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repoRoot := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	now := time.Now().UTC().Format(time.RFC3339)
	manifest := localWorkManifest{
		Version:          1,
		RunID:            "lw-scout-progressed-stale",
		CreatedAt:        now,
		UpdatedAt:        now,
		CompletedAt:      now,
		Status:           "failed",
		CurrentPhase:     "verify",
		CurrentIteration: 1,
		RepoRoot:         repoRoot,
		RepoName:         filepath.Base(repoRoot),
		RepoID:           localWorkRepoID(repoRoot),
		SourceBranch:     "main",
		BaselineSHA:      strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:      filepath.Join(home, "sandboxes", "lw-scout-progressed-stale"),
		SandboxRepoPath:  filepath.Join(home, "sandboxes", "lw-scout-progressed-stale", "repo"),
		LastError:        localWorkStaleCleanupError,
		Iterations: []localWorkIterationSummary{{
			Iteration: 1,
			Status:    "failed",
		}},
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	job := startWorkScoutJob{
		ID:          "proposal-1",
		Destination: improvementDestinationLocal,
		Status:      startScoutJobFailed,
		RunID:       manifest.RunID,
		Attempts:    1,
		LastError:   localWorkStaleCleanupError,
		UpdatedAt:   now,
	}
	reconcileStartWorkScoutJobRunState(&job)
	if job.Status != startScoutJobQueued || job.RunID != "" {
		t.Fatalf("expected progressed stale cleanup to return to queued, got %+v", job)
	}
	if job.PauseReason != "" || job.PauseUntil != "" {
		t.Fatalf("expected progressed stale cleanup to avoid auto-pause, got %+v", job)
	}
	if !startWorkScoutJobHasRecoveryMetadata(job) || job.LastRecoveredRunID != manifest.RunID {
		t.Fatalf("expected progressed stale cleanup to record recovery metadata, got %+v", job)
	}
}

func TestMutateStartWorkScoutJobClearsRecoveryMetadataOnRetryAndDismiss(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	now := time.Now().UTC().Format(time.RFC3339)
	state := startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  now,
		ScoutJobs: map[string]startWorkScoutJob{
			"failed-job": {
				ID:                 "failed-job",
				Role:               improvementScoutRole,
				Title:              "Improve help text",
				Summary:            "Make help clearer",
				ArtifactPath:       ".nana/improvements/improve-test",
				ProposalPath:       ".nana/improvements/improve-test/proposals.json",
				Destination:        improvementDestinationLocal,
				TaskBody:           "Implement local scout proposal: Improve help text",
				Status:             startScoutJobFailed,
				RunID:              "lw-failed",
				LastError:          localWorkStaleCleanupError,
				PauseReason:        startWorkScoutJobStaleRetryPauseReason,
				PauseUntil:         time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339Nano),
				RecoveryCount:      1,
				LastRecoveryReason: localWorkStaleCleanupError,
				LastRecoveryAt:     now,
				LastRecoveredRunID: "lw-failed",
				UpdatedAt:          now,
				CreatedAt:          now,
			},
			"queued-job": {
				ID:                 "queued-job",
				Role:               improvementScoutRole,
				Title:              "Improve help text 2",
				Summary:            "Make help clearer",
				ArtifactPath:       ".nana/improvements/improve-test-2",
				ProposalPath:       ".nana/improvements/improve-test-2/proposals.json",
				Destination:        improvementDestinationLocal,
				TaskBody:           "Implement local scout proposal: Improve help text 2",
				Status:             startScoutJobQueued,
				LastError:          localWorkStaleCleanupError,
				PauseReason:        startWorkScoutJobStaleRetryPauseReason,
				PauseUntil:         time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339Nano),
				RecoveryCount:      1,
				LastRecoveryReason: localWorkStaleCleanupError,
				LastRecoveryAt:     now,
				LastRecoveredRunID: "lw-failed-2",
				UpdatedAt:          now,
				CreatedAt:          now,
			},
		},
	}
	if err := writeStartWorkState(state); err != nil {
		t.Fatalf("writeStartWorkState: %v", err)
	}

	if _, job, err := mutateStartWorkScoutJob(repoSlug, "failed-job", "retry"); err != nil {
		t.Fatalf("retry scout job: %v", err)
	} else if job.Status != startScoutJobQueued || job.RunID != "" || job.PauseReason != "" || job.PauseUntil != "" || startWorkScoutJobHasRecoveryMetadata(job) {
		t.Fatalf("expected retry to clear recovery metadata, got %+v", job)
	}

	if _, job, err := mutateStartWorkScoutJob(repoSlug, "queued-job", "dismiss"); err != nil {
		t.Fatalf("dismiss scout job: %v", err)
	} else if job.Status != startScoutJobDismissed || job.PauseReason != "" || job.PauseUntil != "" || startWorkScoutJobHasRecoveryMetadata(job) {
		t.Fatalf("expected dismiss to clear recovery metadata, got %+v", job)
	}
}

func TestLogStartWorkScoutJobTransitionLogsAutoRecovery(t *testing.T) {
	next := startWorkScoutJob{
		ID:                 "proposal-1",
		Destination:        improvementDestinationLocal,
		Status:             startScoutJobQueued,
		PauseReason:        startWorkScoutJobStaleRetryPauseReason,
		PauseUntil:         "2026-04-21T12:00:00Z",
		RecoveryCount:      1,
		LastRecoveryReason: localWorkStaleCleanupError,
		LastRecoveryAt:     "2026-04-21T11:45:00Z",
		LastRecoveredRunID: "lw-stale",
	}
	output, err := captureStdout(t, func() error {
		logStartWorkScoutJobTransition("acme/widget", startWorkScoutJob{ID: "proposal-1", Status: startScoutJobFailed, RunID: "lw-stale", LastError: localWorkStaleCleanupError}, next, true)
		return nil
	})
	if err != nil {
		t.Fatalf("captureStdout: %v", err)
	}
	if !strings.Contains(output, "auto-requeued after stale startup cleanup") || !strings.Contains(output, "proposal-1") || !strings.Contains(output, "lw-stale") || !strings.Contains(output, "2026-04-21T12:00:00Z") {
		t.Fatalf("expected auto-recovery log line, got %q", output)
	}
}

func TestSyncStartWorkScoutJobsKeepsBoundRunningRunWhenLegacyPlannedItemIsPrelaunch(t *testing.T) {
	for _, tc := range []struct {
		name         string
		plannedState string
	}{
		{name: "queued", plannedState: startPlannedItemQueued},
		{name: "launching_without_run_id", plannedState: startPlannedItemLaunching},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			repoSlug := "acme/widget"
			sourcePath := githubManagedPaths(repoSlug).SourcePath
			repo := createLocalWorkRepoAt(t, sourcePath)
			writeScoutPickupFixture(t, repo, improvementScoutRole, "Improve help text", "Make help clearer")
			if err := writeGithubJSON(filepath.Join(repo, ".nana", "improvements", "improve-test", "policy.json"), improvementPolicy{
				Version:          1,
				IssueDestination: improvementDestinationLocal,
				Labels:           []string{"improvement"},
			}); err != nil {
				t.Fatalf("write policy: %v", err)
			}

			proposal := scoutFinding{
				Title:             "Improve help text",
				Area:              "UX",
				Summary:           "Make help clearer",
				Rationale:         "Users need this.",
				Evidence:          "README.md",
				Impact:            "Better workflow.",
				SuggestedNextStep: "Make the smallest change.",
				Files:             []string{"README.md"},
			}
			proposalID := localScoutProposalID(improvementScoutRole, proposal)
			artifactPath := filepath.ToSlash(filepath.Join(".nana", "improvements", "improve-test"))
			now := time.Now().UTC().Format(time.RFC3339)
			if err := writeStartWorkState(startWorkState{
				Version:    startWorkStateVersion,
				SourceRepo: repoSlug,
				UpdatedAt:  now,
				PlannedItems: map[string]startWorkPlannedItem{
					"planned-scout": {
						ID:          "planned-scout",
						RepoSlug:    repoSlug,
						Title:       startUIScoutPlannedItemTitle(startUIScoutItem{Title: proposal.Title}),
						Description: "Source artifact: " + artifactPath + "\nScout role: " + improvementScoutRole,
						LaunchKind:  "local_work",
						State:       tc.plannedState,
						CreatedAt:   now,
						UpdatedAt:   now,
					},
				},
				ScoutJobs: map[string]startWorkScoutJob{
					proposalID: {
						ID:                  proposalID,
						Role:                improvementScoutRole,
						Title:               proposal.Title,
						Summary:             proposal.Summary,
						ArtifactPath:        artifactPath,
						ProposalPath:        filepath.ToSlash(filepath.Join(artifactPath, "proposals.json")),
						Destination:         improvementDestinationLocal,
						TaskBody:            "Implement local scout proposal: Improve help text",
						Status:              startScoutJobRunning,
						RunID:               "lw-existing",
						UpdatedAt:           now,
						CreatedAt:           now,
						LegacyPlannedItemID: "planned-scout",
					},
				},
			}); err != nil {
				t.Fatalf("write start state: %v", err)
			}

			_, _, err := syncStartWorkScoutJobs(repo, repoSlug)
			if err != nil {
				t.Fatalf("syncStartWorkScoutJobs: %v", err)
			}
			workState, err := readStartWorkState(repoSlug)
			if err != nil {
				t.Fatalf("read start state: %v", err)
			}
			job := workState.ScoutJobs[proposalID]
			if job.Status != startScoutJobRunning || job.RunID != "lw-existing" {
				t.Fatalf("expected running scout job to keep its bound run, got %+v", job)
			}
			if _, ok := workState.PlannedItems["planned-scout"]; ok {
				t.Fatalf("expected stale scout-derived planned item to be removed, got %+v", workState.PlannedItems)
			}
		})
	}
}
