package gocli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareGithubWorkSourceDowngradesSourceLockBeforeReadPhase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	paths, repoMeta := createGithubManagedSourceFixture(t, home, "acme/widget")
	oldPreflight := githubManagedOriginPreflight
	githubManagedOriginPreflight = func(repoPath string, repoMeta *githubManagedRepoMetadata) error { return nil }
	defer func() { githubManagedOriginPreflight = oldPreflight }()

	sentinel := errors.New("stop work start after source read phase")
	_, err := prepareGithubWorkSource(paths, repoMeta, repoAccessLockOwner{
		Backend: "test",
		RunID:   "github-work-source",
		Purpose: "source-setup",
		Label:   "github-work-source",
	}, time.Now().UTC(), filepath.Join(paths.RepoRoot, "sandboxes", "issue-1-test", "repo"), func(sourcePath string) error {
		return assertGithubSourceReadPhaseAllowsSharedReaders(t, sourcePath, sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel hook error, got %v", err)
	}
}

func TestPrepareGithubPullReviewSourceDowngradesSourceLockBeforeSandboxClone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	paths, repoMeta := createGithubManagedSourceFixture(t, home, "acme/widget")
	oldPreflight := githubManagedOriginPreflight
	githubManagedOriginPreflight = func(repoPath string, repoMeta *githubManagedRepoMetadata) error { return nil }
	defer func() { githubManagedOriginPreflight = oldPreflight }()

	sentinel := errors.New("stop pull review after source read phase")
	err := prepareGithubPullReviewSource(paths, repoMeta, repoAccessLockOwner{
		Backend: "test",
		RunID:   "github-review-source",
		Purpose: "source-setup",
		Label:   "github-review-source",
	}, filepath.Join(home, "review", "repo"), func(sourcePath string) error {
		return assertGithubSourceReadPhaseAllowsSharedReaders(t, sourcePath, sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel hook error, got %v", err)
	}
}

func TestPrepareGithubInvestigateSourceDowngradesSourceLockBeforeRepoScans(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	paths, repoMeta := createGithubManagedSourceFixture(t, home, "acme/widget")
	oldPreflight := githubManagedOriginPreflight
	githubManagedOriginPreflight = func(repoPath string, repoMeta *githubManagedRepoMetadata) error { return nil }
	defer func() { githubManagedOriginPreflight = oldPreflight }()

	sentinel := errors.New("stop investigate after source read phase")
	_, err := prepareGithubInvestigateSource(paths, repoMeta, repoAccessLockOwner{
		Backend: "test",
		RunID:   "github-investigate-source",
		Purpose: "source-inspect",
		Label:   "github-investigate-source",
	}, time.Now().UTC(), func(sourcePath string) error {
		return assertGithubSourceReadPhaseAllowsSharedReaders(t, sourcePath, sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel hook error, got %v", err)
	}
}

func TestPrepareGithubReviewRequestSourceDowngradesSourceLockBeforeSandboxClone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	paths, repoMeta := createGithubManagedSourceFixture(t, home, "acme/widget")
	oldPreflight := githubManagedOriginPreflight
	githubManagedOriginPreflight = func(repoPath string, repoMeta *githubManagedRepoMetadata) error { return nil }
	defer func() { githubManagedOriginPreflight = oldPreflight }()

	sentinel := errors.New("stop review work item after source read phase")
	err := prepareGithubReviewRequestSource(paths, repoMeta, repoAccessLockOwner{
		Backend: "test",
		RunID:   "work-item-review-request-source",
		Purpose: "review-request-source-setup",
		Label:   "work-item-review-request-source",
	}, filepath.Join(home, "attempt", "repo"), func(sourcePath string) error {
		return assertGithubSourceReadPhaseAllowsSharedReaders(t, sourcePath, sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel hook error, got %v", err)
	}
}

func assertGithubSourceReadPhaseAllowsSharedReaders(t *testing.T, sourcePath string, sentinel error) error {
	t.Helper()

	lockState, err := buildRepoAccessLockState(sourcePath, repoAccessLockRead)
	if err != nil {
		t.Fatalf("build source lock state: %v", err)
	}
	if lockState == nil || lockState.Writer != nil || len(lockState.Readers) == 0 {
		t.Fatalf("expected source read phase to expose shared readers, got %+v", lockState)
	}

	readLock, err := acquireSourceReadLock(sourcePath, repoAccessLockOwner{
		Backend: "test",
		RunID:   "shared-reader",
		Purpose: "inspect",
		Label:   "shared-reader",
	})
	if err != nil {
		t.Fatalf("acquire concurrent read lock: %v", err)
	}
	defer func() { _ = readLock.Release() }()

	if _, err := acquireSourceWriteLock(sourcePath, repoAccessLockOwner{
		Backend: "test",
		RunID:   "blocked-writer",
		Purpose: "mutate",
		Label:   "blocked-writer",
	}); err == nil || !strings.Contains(err.Error(), "repo write lock busy") {
		t.Fatalf("expected write lock conflict while source read phase is active, got %v", err)
	}

	lockState, err = buildRepoAccessLockState(sourcePath, repoAccessLockRead)
	if err != nil {
		t.Fatalf("build source lock state after concurrent read: %v", err)
	}
	if lockState == nil || lockState.Writer != nil || len(lockState.Readers) < 2 {
		t.Fatalf("expected two readers during source read phase, got %+v", lockState)
	}
	return sentinel
}
