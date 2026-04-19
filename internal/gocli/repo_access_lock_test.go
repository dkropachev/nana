package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoAccessLockReadWriteSemantics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repoPath := filepath.Join(home, "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	readOne, err := acquireRepoReadLock(repoPath, repoAccessLockOwner{Backend: "test", RunID: "read-1", Purpose: "inspect", Label: "reader-one"})
	if err != nil {
		t.Fatalf("acquire first read lock: %v", err)
	}
	defer func() { _ = readOne.Release() }()
	readTwo, err := acquireRepoReadLock(repoPath, repoAccessLockOwner{Backend: "test", RunID: "read-2", Purpose: "inspect", Label: "reader-two"})
	if err != nil {
		t.Fatalf("acquire second read lock: %v", err)
	}
	defer func() { _ = readTwo.Release() }()
	lockRoot := repoAccessLockRoot(repoPath)
	readerEntries, err := os.ReadDir(filepath.Join(lockRoot, "readers"))
	if err != nil {
		t.Fatalf("read readers dir: %v", err)
	}
	if len(readerEntries) != 2 {
		t.Fatalf("expected two reader lock files, got %d", len(readerEntries))
	}

	if _, err := acquireRepoWriteLock(repoPath, repoAccessLockOwner{Backend: "test", RunID: "write-1", Purpose: "mutate", Label: "writer"}); err == nil || !strings.Contains(err.Error(), "active read locks") {
		t.Fatalf("expected write lock conflict, got %v", err)
	}

	if err := readOne.Release(); err != nil {
		t.Fatalf("release first read lock: %v", err)
	}
	if err := readTwo.Release(); err != nil {
		t.Fatalf("release second read lock: %v", err)
	}

	writeLock, err := acquireRepoWriteLock(repoPath, repoAccessLockOwner{Backend: "test", RunID: "write-2", Purpose: "mutate", Label: "writer"})
	if err != nil {
		t.Fatalf("acquire write lock after readers released: %v", err)
	}
	defer func() { _ = writeLock.Release() }()
	if _, err := os.Stat(filepath.Join(lockRoot, "writer.json")); err != nil {
		t.Fatalf("expected writer lock file, err=%v", err)
	}
	if writer, ok, err := readRepoAccessWriter(lockRoot); err != nil {
		t.Fatalf("read writer record: %v", err)
	} else if !ok {
		t.Fatalf("expected writer record to be readable")
	} else if writer.Token == "" {
		t.Fatalf("expected writer token, got %#v", writer)
	}

	if _, err := acquireRepoReadLock(repoPath, repoAccessLockOwner{Backend: "test", RunID: "read-3", Purpose: "inspect", Label: "reader-three"}); err == nil || !strings.Contains(err.Error(), "active write lock") {
		t.Fatalf("expected read lock conflict, got %v", err)
	}
}

func TestBuildRepoAccessLockStatusIncludesHolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	sourcePath := filepath.Join(home, "source")
	sandboxPath := filepath.Join(home, "sandbox", "repo")
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.MkdirAll(sandboxPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox: %v", err)
	}

	sourceLock, err := acquireSourceWriteLock(sourcePath, repoAccessLockOwner{Backend: "test", RunID: "source-run", Purpose: "source-setup", Label: "source-writer"})
	if err != nil {
		t.Fatalf("acquire source write lock: %v", err)
	}
	defer func() { _ = sourceLock.Release() }()
	sandboxLock, err := acquireSandboxReadLock(sandboxPath, repoAccessLockOwner{Backend: "test", RunID: "sandbox-run", Purpose: "inspect", Label: "sandbox-reader"})
	if err != nil {
		t.Fatalf("acquire sandbox read lock: %v", err)
	}
	defer func() { _ = sandboxLock.Release() }()

	status, err := buildRepoAccessLockStatus(sourcePath, repoAccessLockWrite, sandboxPath, repoAccessLockRead)
	if err != nil {
		t.Fatalf("buildRepoAccessLockStatus: %v", err)
	}
	if status == nil || status.Source == nil || status.Sandbox == nil {
		t.Fatalf("expected source and sandbox lock state, got %#v", status)
	}
	if status.Source.Writer == nil || !strings.Contains(status.Source.Writer.Label, "source-writer") {
		t.Fatalf("expected source writer in snapshot, got %#v", status.Source)
	}
	if len(status.Sandbox.Readers) != 1 || !strings.Contains(status.Sandbox.Readers[0].Label, "sandbox-reader") {
		t.Fatalf("expected sandbox reader in snapshot, got %#v", status.Sandbox)
	}
}

func setRepoAccessLockTestTiming(t *testing.T, timeout time.Duration, poll time.Duration, heartbeat time.Duration, stale time.Duration) func() {
	t.Helper()
	oldTimeout := repoAccessLockAcquireTimeout
	oldPoll := repoAccessLockAcquirePoll
	oldHeartbeat := repoAccessLockHeartbeatInterval
	oldStale := repoAccessLockStaleThreshold
	repoAccessLockAcquireTimeout = timeout
	repoAccessLockAcquirePoll = poll
	repoAccessLockHeartbeatInterval = heartbeat
	repoAccessLockStaleThreshold = stale
	return func() {
		repoAccessLockAcquireTimeout = oldTimeout
		repoAccessLockAcquirePoll = oldPoll
		repoAccessLockHeartbeatInterval = oldHeartbeat
		repoAccessLockStaleThreshold = oldStale
	}
}
