package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type repoAccessLockMode string

const (
	repoAccessLockRead  repoAccessLockMode = "read"
	repoAccessLockWrite repoAccessLockMode = "write"
)

const repoAccessLockVersion = 1
const repoAccessLockTimestampLayout = time.RFC3339Nano

var (
	repoAccessLockNow               = func() time.Time { return time.Now().UTC() }
	repoAccessLockSleep             = time.Sleep
	repoAccessLockAcquireTimeout    = 5 * time.Second
	repoAccessLockAcquirePoll       = 100 * time.Millisecond
	repoAccessLockHeartbeatInterval = 5 * time.Second
	repoAccessLockStaleThreshold    = 30 * time.Second
)

type repoAccessLockOwner struct {
	Token   string
	Backend string
	RunID   string
	Purpose string
	Label   string
}

type repoAccessLockRecord struct {
	Version   int    `json:"version"`
	Token     string `json:"token"`
	Mode      string `json:"mode"`
	RepoPath  string `json:"repo_path"`
	Backend   string `json:"backend,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Purpose   string `json:"purpose,omitempty"`
	Label     string `json:"label,omitempty"`
	Pid       int    `json:"pid,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type repoAccessLockHolderSnapshot struct {
	Label     string `json:"label,omitempty"`
	Backend   string `json:"backend,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Purpose   string `json:"purpose,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Stale     bool   `json:"stale,omitempty"`
}

type repoAccessLockStateSnapshot struct {
	Path          string                         `json:"path"`
	RequestedMode string                         `json:"requested_mode"`
	Writer        *repoAccessLockHolderSnapshot  `json:"writer,omitempty"`
	Readers       []repoAccessLockHolderSnapshot `json:"readers,omitempty"`
}

type repoAccessLockStatusSnapshot struct {
	Source  *repoAccessLockStateSnapshot `json:"source,omitempty"`
	Sandbox *repoAccessLockStateSnapshot `json:"sandbox,omitempty"`
}

type repoAccessLockHandle struct {
	record      repoAccessLockRecord
	lockRoot    string
	filePath    string
	releaseOnce sync.Once
	stopCh      chan struct{}
	doneCh      chan struct{}
}

func acquireRepoReadLock(repoPath string, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	return acquireRepoAccessLock(repoPath, repoAccessLockRead, owner)
}

func acquireRepoWriteLock(repoPath string, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	return acquireRepoAccessLock(repoPath, repoAccessLockWrite, owner)
}

func acquireSourceReadLock(repoPath string, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	return acquireRepoReadLock(repoPath, owner)
}

func acquireSourceWriteLock(repoPath string, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	return acquireRepoWriteLock(repoPath, owner)
}

func acquireManagedSourceReadLock(repoSlug string, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	return acquireSourceReadLock(githubManagedPaths(repoSlug).SourcePath, owner)
}

func acquireManagedSourceWriteLock(repoSlug string, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	return acquireSourceWriteLock(githubManagedPaths(repoSlug).SourcePath, owner)
}

func acquireSandboxReadLock(repoPath string, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	return acquireRepoReadLock(repoPath, owner)
}

func acquireSandboxWriteLock(repoPath string, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	return acquireRepoWriteLock(repoPath, owner)
}

func withRepoAccessLock(repoPath string, mode repoAccessLockMode, owner repoAccessLockOwner, fn func() error) error {
	handle, err := acquireRepoAccessLock(repoPath, mode, owner)
	if err != nil {
		return err
	}
	defer func() {
		_ = handle.Release()
	}()
	return fn()
}

func withSourceReadLock(repoPath string, owner repoAccessLockOwner, fn func() error) error {
	return withRepoAccessLock(repoPath, repoAccessLockRead, owner, fn)
}

func withSourceWriteLock(repoPath string, owner repoAccessLockOwner, fn func() error) error {
	return withRepoAccessLock(repoPath, repoAccessLockWrite, owner, fn)
}

func withSourceWriteThenReadLock(repoPath string, owner repoAccessLockOwner, writePhase func() error, readPhase func() error) error {
	handle, err := acquireSourceWriteLock(repoPath, owner)
	if err != nil {
		return err
	}
	defer func() {
		_ = handle.Release()
	}()
	if writePhase != nil {
		if err := writePhase(); err != nil {
			return err
		}
	}
	if readPhase == nil {
		return nil
	}
	handle, err = handle.DowngradeToRead()
	if err != nil {
		return err
	}
	return readPhase()
}

func withManagedSourceReadLock(repoSlug string, owner repoAccessLockOwner, fn func() error) error {
	return withSourceReadLock(githubManagedPaths(repoSlug).SourcePath, owner, fn)
}

func withManagedSourceWriteLock(repoSlug string, owner repoAccessLockOwner, fn func() error) error {
	return withSourceWriteLock(githubManagedPaths(repoSlug).SourcePath, owner, fn)
}

func withSandboxReadLock(repoPath string, owner repoAccessLockOwner, fn func() error) error {
	return withRepoAccessLock(repoPath, repoAccessLockRead, owner, fn)
}

func withSandboxWriteLock(repoPath string, owner repoAccessLockOwner, fn func() error) error {
	return withRepoAccessLock(repoPath, repoAccessLockWrite, owner, fn)
}

func acquireRepoAccessLock(repoPath string, mode repoAccessLockMode, owner repoAccessLockOwner) (*repoAccessLockHandle, error) {
	targetPath, err := normalizeRepoAccessLockPath(repoPath)
	if err != nil {
		return nil, err
	}
	lockRoot := repoAccessLockRoot(targetPath)
	if err := ensureRepoAccessLockRoot(lockRoot, targetPath); err != nil {
		return nil, err
	}
	record := buildRepoAccessLockRecord(targetPath, mode, owner)
	deadline := repoAccessLockNow().Add(repoAccessLockAcquireTimeout)
	for {
		if err := cleanupStaleRepoAccessLocks(lockRoot, ""); err != nil {
			return nil, err
		}
		switch mode {
		case repoAccessLockRead:
			writer, ok, err := readRepoAccessWriter(lockRoot)
			if err != nil {
				return nil, err
			}
			if ok {
				if repoAccessLockNow().After(deadline) {
					return nil, repoAccessReadLockBusyError(targetPath, writer)
				}
				repoAccessLockSleep(repoAccessLockAcquirePoll)
				continue
			}
			handle, err := createRepoAccessReader(lockRoot, record)
			if err != nil {
				if os.IsExist(err) {
					if repoAccessLockNow().After(deadline) {
						return nil, repoAccessReadLockBusyError(targetPath, repoAccessLockRecord{})
					}
					repoAccessLockSleep(repoAccessLockAcquirePoll)
					continue
				}
				return nil, err
			}
			writer, ok, err = readRepoAccessWriter(lockRoot)
			if err != nil {
				_ = handle.Release()
				return nil, err
			}
			if ok {
				_ = handle.Release()
				if repoAccessLockNow().After(deadline) {
					return nil, repoAccessReadLockBusyError(targetPath, writer)
				}
				repoAccessLockSleep(repoAccessLockAcquirePoll)
				continue
			}
			handle.startHeartbeat()
			return handle, nil
		case repoAccessLockWrite:
			handle, err := createRepoAccessWriter(lockRoot, record)
			if err != nil {
				if os.IsExist(err) {
					writer, ok, readErr := readRepoAccessWriter(lockRoot)
					if readErr != nil {
						return nil, readErr
					}
					if repoAccessLockNow().After(deadline) {
						return nil, repoAccessWriteLockBusyError(targetPath, writer, nil)
					}
					if !ok {
						repoAccessLockSleep(repoAccessLockAcquirePoll)
						continue
					}
					repoAccessLockSleep(repoAccessLockAcquirePoll)
					continue
				}
				return nil, err
			}
			handle.startHeartbeat()
			for {
				if err := cleanupStaleRepoAccessLocks(lockRoot, handle.record.Token); err != nil {
					_ = handle.Release()
					return nil, err
				}
				readers, err := listRepoAccessReaders(lockRoot, handle.record.Token)
				if err != nil {
					_ = handle.Release()
					return nil, err
				}
				if len(readers) == 0 {
					return handle, nil
				}
				if repoAccessLockNow().After(deadline) {
					_ = handle.Release()
					return nil, repoAccessWriteLockBusyError(targetPath, repoAccessLockRecord{}, readers)
				}
				repoAccessLockSleep(repoAccessLockAcquirePoll)
			}
		default:
			return nil, fmt.Errorf("unsupported repo lock mode %q", mode)
		}
	}
}

func (handle *repoAccessLockHandle) Release() error {
	if handle == nil {
		return nil
	}
	var releaseErr error
	handle.releaseOnce.Do(func() {
		handle.stopHeartbeat()
		if _, err := removePathIfExists(handle.filePath); err != nil {
			releaseErr = err
			return
		}
		_, _ = pruneEmptyParentDirs(handle.filePath, workHomeRoot())
	})
	return releaseErr
}

func (handle *repoAccessLockHandle) DowngradeToRead() (*repoAccessLockHandle, error) {
	if handle == nil {
		return nil, fmt.Errorf("repo lock handle is required")
	}
	switch repoAccessLockMode(handle.record.Mode) {
	case repoAccessLockRead:
		return handle, nil
	case repoAccessLockWrite:
	default:
		return nil, fmt.Errorf("cannot downgrade repo lock mode %q", handle.record.Mode)
	}

	readerRecord := handle.record
	readerRecord.Mode = string(repoAccessLockRead)
	readerRecord.UpdatedAt = repoAccessLockNow().Format(repoAccessLockTimestampLayout)
	readerPath := filepath.Join(handle.lockRoot, "readers", readerRecord.Token+".json")
	if err := createRepoAccessLockRecord(readerPath, readerRecord); err != nil {
		return nil, err
	}

	writerPath := handle.filePath
	writerRecord := handle.record
	handle.stopHeartbeat()
	if _, err := removePathIfExists(writerPath); err != nil {
		_, _ = removePathIfExists(readerPath)
		handle.record = writerRecord
		handle.filePath = writerPath
		handle.startHeartbeat()
		return nil, err
	}

	handle.record = readerRecord
	handle.filePath = readerPath
	handle.startHeartbeat()
	return handle, nil
}

func (handle *repoAccessLockHandle) startHeartbeat() {
	handle.stopHeartbeat()
	handle.stopCh = make(chan struct{})
	handle.doneCh = make(chan struct{})
	go func() {
		ticker := time.NewTicker(repoAccessLockHeartbeatInterval)
		defer func() {
			ticker.Stop()
			close(handle.doneCh)
		}()
		record := handle.record
		for {
			select {
			case <-handle.stopCh:
				return
			case <-ticker.C:
				record.UpdatedAt = repoAccessLockNow().Format(repoAccessLockTimestampLayout)
				_ = writeRepoAccessLockRecord(handle.filePath, record)
			}
		}
	}()
}

func (handle *repoAccessLockHandle) stopHeartbeat() {
	if handle == nil || handle.stopCh == nil {
		return
	}
	close(handle.stopCh)
	<-handle.doneCh
	handle.stopCh = nil
	handle.doneCh = nil
}

func normalizeRepoAccessLockPath(repoPath string) (string, error) {
	clean := strings.TrimSpace(repoPath)
	if clean == "" {
		return "", fmt.Errorf("repo lock path is required")
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		abs = filepath.Clean(resolved)
	}
	return abs, nil
}

func repoAccessLockRoot(repoPath string) string {
	return filepath.Join(workHomeRoot(), "repo-locks", sha256Hex(repoPath))
}

func ensureRepoAccessLockRoot(lockRoot string, repoPath string) error {
	if err := os.MkdirAll(filepath.Join(lockRoot, "readers"), 0o755); err != nil {
		return err
	}
	return writeRepoAccessLockRecordIfChanged(filepath.Join(lockRoot, "target.json"), map[string]string{
		"repo_path": repoPath,
	})
}

func buildRepoAccessLockRecord(repoPath string, mode repoAccessLockMode, owner repoAccessLockOwner) repoAccessLockRecord {
	now := repoAccessLockNow().Format(repoAccessLockTimestampLayout)
	token := strings.TrimSpace(owner.Token)
	if token == "" {
		token = fmt.Sprintf("%s-%d", sanitizePathToken(defaultString(strings.TrimSpace(owner.Purpose), string(mode))), repoAccessLockNow().UnixNano())
	}
	return repoAccessLockRecord{
		Version:   repoAccessLockVersion,
		Token:     token,
		Mode:      string(mode),
		RepoPath:  repoPath,
		Backend:   strings.TrimSpace(owner.Backend),
		RunID:     strings.TrimSpace(owner.RunID),
		Purpose:   strings.TrimSpace(owner.Purpose),
		Label:     strings.TrimSpace(owner.Label),
		Pid:       os.Getpid(),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func createRepoAccessReader(lockRoot string, record repoAccessLockRecord) (*repoAccessLockHandle, error) {
	path := filepath.Join(lockRoot, "readers", record.Token+".json")
	if err := createRepoAccessLockRecord(path, record); err != nil {
		return nil, err
	}
	return &repoAccessLockHandle{record: record, lockRoot: lockRoot, filePath: path}, nil
}

func createRepoAccessWriter(lockRoot string, record repoAccessLockRecord) (*repoAccessLockHandle, error) {
	path := filepath.Join(lockRoot, "writer.json")
	if err := createRepoAccessLockRecord(path, record); err != nil {
		return nil, err
	}
	return &repoAccessLockHandle{record: record, lockRoot: lockRoot, filePath: path}, nil
}

func createRepoAccessLockRecord(path string, record repoAccessLockRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, writeErr := file.Write(content); writeErr != nil {
		_ = file.Close()
		_, _ = removePathIfExists(path)
		return writeErr
	}
	if closeErr := file.Close(); closeErr != nil {
		_, _ = removePathIfExists(path)
		return closeErr
	}
	return nil
}

func writeRepoAccessLockRecord(path string, payload any) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return writeRepoAccessLockContent(path, content)
}

func writeRepoAccessLockRecordIfChanged(path string, payload any) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, content) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return writeRepoAccessLockContent(path, content)
}

func writeRepoAccessLockContent(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "repo-lock-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, writeErr := tmp.Write(content); writeErr != nil {
		_ = tmp.Close()
		_, _ = removePathIfExists(tmpPath)
		return writeErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		_, _ = removePathIfExists(tmpPath)
		return closeErr
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_, _ = removePathIfExists(tmpPath)
		return err
	}
	return nil
}

func cleanupStaleRepoAccessLocks(lockRoot string, excludeToken string) error {
	writerPath := filepath.Join(lockRoot, "writer.json")
	if record, ok, err := readRepoAccessLockRecord(writerPath); err != nil {
		return err
	} else if ok && strings.TrimSpace(record.Token) != strings.TrimSpace(excludeToken) && repoAccessLockRecordStale(record) {
		if _, removeErr := removePathIfExists(writerPath); removeErr != nil {
			return removeErr
		}
	}
	readersDir := filepath.Join(lockRoot, "readers")
	entries, err := os.ReadDir(readersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(readersDir, entry.Name())
		record, ok, readErr := readRepoAccessLockRecord(path)
		if readErr != nil {
			return readErr
		}
		if !ok {
			continue
		}
		if strings.TrimSpace(record.Token) == strings.TrimSpace(excludeToken) {
			continue
		}
		if repoAccessLockRecordStale(record) {
			if _, removeErr := removePathIfExists(path); removeErr != nil {
				return removeErr
			}
		}
	}
	return nil
}

func readRepoAccessWriter(lockRoot string) (repoAccessLockRecord, bool, error) {
	return readRepoAccessLockRecord(filepath.Join(lockRoot, "writer.json"))
}

func listRepoAccessReaders(lockRoot string, excludeToken string) ([]repoAccessLockRecord, error) {
	readersDir := filepath.Join(lockRoot, "readers")
	entries, err := os.ReadDir(readersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	readers := []repoAccessLockRecord{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		record, ok, err := readRepoAccessLockRecord(filepath.Join(readersDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if !ok || strings.TrimSpace(record.Token) == strings.TrimSpace(excludeToken) {
			continue
		}
		readers = append(readers, record)
	}
	sort.Slice(readers, func(i int, j int) bool {
		left := repoAccessLockRecordLabel(readers[i])
		right := repoAccessLockRecordLabel(readers[j])
		if left != right {
			return left < right
		}
		return readers[i].Token < readers[j].Token
	})
	return readers, nil
}

func buildRepoAccessLockState(repoPath string, requestedMode repoAccessLockMode) (*repoAccessLockStateSnapshot, error) {
	if strings.TrimSpace(repoPath) == "" {
		return nil, nil
	}
	targetPath, err := normalizeRepoAccessLockPath(repoPath)
	if err != nil {
		return nil, err
	}
	lockRoot := repoAccessLockRoot(targetPath)
	writer, writerOK, err := readRepoAccessWriter(lockRoot)
	if err != nil {
		return nil, err
	}
	readers, err := listRepoAccessReaders(lockRoot, "")
	if err != nil {
		return nil, err
	}
	state := &repoAccessLockStateSnapshot{
		Path:          targetPath,
		RequestedMode: string(requestedMode),
		Readers:       make([]repoAccessLockHolderSnapshot, 0, len(readers)),
	}
	if writerOK {
		holder := repoAccessLockHolderSnapshotFromRecord(writer)
		state.Writer = &holder
	}
	for _, reader := range readers {
		state.Readers = append(state.Readers, repoAccessLockHolderSnapshotFromRecord(reader))
	}
	return state, nil
}

func buildRepoAccessLockStatus(sourcePath string, sourceMode repoAccessLockMode, sandboxPath string, sandboxMode repoAccessLockMode) (*repoAccessLockStatusSnapshot, error) {
	source, err := buildRepoAccessLockState(sourcePath, sourceMode)
	if err != nil {
		return nil, err
	}
	sandbox, err := buildRepoAccessLockState(sandboxPath, sandboxMode)
	if err != nil {
		return nil, err
	}
	if source == nil && sandbox == nil {
		return nil, nil
	}
	return &repoAccessLockStatusSnapshot{
		Source:  source,
		Sandbox: sandbox,
	}, nil
}

func repoAccessLockHolderSnapshotFromRecord(record repoAccessLockRecord) repoAccessLockHolderSnapshot {
	return repoAccessLockHolderSnapshot{
		Label:     repoAccessLockRecordLabel(record),
		Backend:   strings.TrimSpace(record.Backend),
		RunID:     strings.TrimSpace(record.RunID),
		Purpose:   strings.TrimSpace(record.Purpose),
		UpdatedAt: strings.TrimSpace(record.UpdatedAt),
		Stale:     repoAccessLockRecordStale(record),
	}
}

func repoAccessLockStateHasHolders(state *repoAccessLockStateSnapshot) bool {
	return state != nil && (state.Writer != nil || len(state.Readers) > 0)
}

func repoAccessLockStateSummary(state *repoAccessLockStateSnapshot) string {
	if state == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("path=%s", state.Path), fmt.Sprintf("requested=%s", state.RequestedMode)}
	if state.Writer != nil {
		writer := state.Writer.Label
		if state.Writer.Stale {
			writer += " (stale)"
		}
		parts = append(parts, "writer="+writer)
	}
	if len(state.Readers) > 0 {
		readers := make([]string, 0, len(state.Readers))
		for _, reader := range state.Readers {
			label := reader.Label
			if reader.Stale {
				label += " (stale)"
			}
			readers = append(readers, label)
		}
		parts = append(parts, "readers="+strings.Join(readers, ","))
	}
	return strings.Join(parts, " ")
}

func readRepoAccessLockRecord(path string) (repoAccessLockRecord, bool, error) {
	var record repoAccessLockRecord
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return repoAccessLockRecord{}, false, nil
		}
		return repoAccessLockRecord{}, false, err
	}
	if err := json.Unmarshal(content, &record); err != nil {
		if _, removeErr := removePathIfExists(path); removeErr != nil {
			return repoAccessLockRecord{}, false, removeErr
		}
		return repoAccessLockRecord{}, false, nil
	}
	return record, true, nil
}

func repoAccessLockRecordStale(record repoAccessLockRecord) bool {
	updatedAt, err := parseRepoAccessLockTime(strings.TrimSpace(record.UpdatedAt))
	if err != nil {
		return true
	}
	return repoAccessLockNow().Sub(updatedAt) > repoAccessLockStaleThreshold
}

func parseRepoAccessLockTime(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("empty lock timestamp")
	}
	if parsed, err := time.Parse(repoAccessLockTimestampLayout, trimmed); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func repoAccessReadLockBusyError(repoPath string, writer repoAccessLockRecord) error {
	holder := "another writer"
	if strings.TrimSpace(writer.Token) != "" {
		holder = repoAccessLockRecordLabel(writer)
	}
	return fmt.Errorf("repo read lock busy for %s: active write lock held by %s", repoPath, holder)
}

func repoAccessWriteLockBusyError(repoPath string, writer repoAccessLockRecord, readers []repoAccessLockRecord) error {
	if strings.TrimSpace(writer.Token) != "" {
		return fmt.Errorf("repo write lock busy for %s: active write lock held by %s", repoPath, repoAccessLockRecordLabel(writer))
	}
	holders := make([]string, 0, len(readers))
	for _, reader := range readers {
		holders = append(holders, repoAccessLockRecordLabel(reader))
	}
	if len(holders) == 0 {
		return fmt.Errorf("repo write lock busy for %s: waiting for active readers to finish", repoPath)
	}
	return fmt.Errorf("repo write lock busy for %s: active read locks held by %s", repoPath, strings.Join(holders, ", "))
}

func repoAccessLockBusy(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "repo read lock busy") || strings.Contains(message, "repo write lock busy")
}

func repoAccessLockRecordLabel(record repoAccessLockRecord) string {
	parts := []string{}
	if value := strings.TrimSpace(record.Label); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(record.Backend); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(record.RunID); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(record.Purpose); value != "" {
		parts = append(parts, value)
	}
	if len(parts) == 0 {
		parts = append(parts, defaultString(strings.TrimSpace(record.Token), "unknown-holder"))
	}
	return strings.Join(parts, "/")
}
