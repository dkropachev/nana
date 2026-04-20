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
		ID:        "proposal-1",
		Status:    startScoutJobFailed,
		RunID:     manifest.RunID,
		LastError: localWorkStaleCleanupError,
		UpdatedAt: now,
	}
	reconcileStartWorkScoutJobRunState(&job)
	if job.Status != startScoutJobCompleted {
		t.Fatalf("expected completed scout job after reconcile, got %+v", job)
	}
	if job.LastError != "" {
		t.Fatalf("expected stale error to be cleared, got %+v", job)
	}
}
