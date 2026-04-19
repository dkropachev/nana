package gocli

import (
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
