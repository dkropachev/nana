package gocli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	usageStoreSourceKindRollout       = "rollout"
	usageStoreSourceKindLocalThread   = "local-thread"
	usageStoreSourceKindLocalHistory  = "local-history"
	usageStoreSourceKindLocalManifest = "local-manifest"
	usageStoreSourceKindGithubThread  = "github-thread"
	usageStoreSourceKindGithubHistory = "github-history"
	usageStoreSourceKindLegacyIndex   = "legacy-index"

	usageMetadataKeySessionRootsScanned = "usage_session_roots_scanned"
	usageMetadataKeyWorkSyncUpdatedAt   = "usage_work_sync_updated_at"
	usageMetadataKeyLegacyImportedAt    = "usage_legacy_imported_at"
	usageMetadataKeyLegacyImportedFrom  = "usage_legacy_imported_from"
	usageMetadataKeyImportCompletedAt   = "usage_import_completed_at"
	usageMetadataKeyDataVersion         = "usage_data_version"
	usageMetadataKeyDataUpdatedAt       = "usage_data_updated_at"
)

type usageSQLiteSourceSpec struct {
	Key             string
	Kind            string
	Path            string
	Root            string
	RunID           string
	RepoSlug        string
	Backend         string
	SandboxPath     string
	SourceUpdatedAt string
	SizeBytes       int64
	ModifiedUnix    int64
	UpdatedAt       string
}

type usageSQLiteSessionSpec struct {
	Key                   string
	SourceKey             string
	Record                usageRecord
	TimestampUnix         int64
	StartedAt             int64
	UpdatedAt             int64
	PartialWindowCoverage bool
}

type usageSQLiteCheckpointSpec struct {
	SessionKey string
	Seq        int
	Current    usageTokenCheckpoint
	Delta      usageTokenCheckpoint
}

type usageSQLiteSyncResult struct {
	SessionRootsScanned int
	WorkSyncUpdatedAt   string
	LegacyImportedAt    string
	LegacyImportedFrom  string
}

func recordManagedPromptUsage(options codexManagedPromptOptions, result codexManagedPromptResult) error {
	sessionPath := strings.TrimSpace(result.SessionPath)
	if sessionPath == "" {
		sessionPath = findCodexSessionPathByID(options.CodexHome, result.SessionID)
	}
	if sessionPath == "" {
		return nil
	}
	root := resolveManagedPromptUsageRoot(options.CommandDir, options.CodexHome)
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		_, err := store.ingestRolloutUsageSource(root, sessionPath, usageSQLiteSourceSpec{
			RunID:       strings.TrimSpace(options.UsageRunID),
			RepoSlug:    strings.TrimSpace(options.UsageRepoSlug),
			Backend:     strings.TrimSpace(options.UsageBackend),
			SandboxPath: strings.TrimSpace(options.UsageSandboxPath),
		})
		return err
	})
}

func resolveManagedPromptUsageRoot(cwd string, codexHome string) string {
	sessionsRoot := filepath.Join(strings.TrimSpace(codexHome), "sessions")
	if root := classifyUsageRootFromSessionsDir(sessionsRoot); root != "" {
		return root
	}
	cleanCodexHome := filepath.Clean(strings.TrimSpace(codexHome))
	if cleanCodexHome == filepath.Clean(DefaultUserInvestigateCodexHome(os.Getenv("HOME"))) ||
		cleanCodexHome == filepath.Clean(ResolveInvestigateCodexHome(cwd)) {
		return "investigate"
	}
	return "main"
}

func refreshUsageSQLiteStore(cwd string, path string) (startUIUsageIndexState, error) {
	_ = path
	return withLocalWorkWriteStore(func(store *localWorkDBStore) (startUIUsageIndexState, error) {
		snapshot, err := store.syncUsageSQLite(cwd)
		if err != nil {
			return startUIUsageIndexState{}, err
		}
		return store.buildUsageIndexStateFromSQLite(snapshot)
	})
}

func (s *localWorkDBStore) syncUsageSQLite(cwd string) (usageSQLiteSyncResult, error) {
	result := usageSQLiteSyncResult{}
	if s == nil || s.db == nil {
		return result, nil
	}
	importCompletedAt, err := s.usageMetadata(usageMetadataKeyImportCompletedAt)
	if err != nil {
		return result, err
	}
	importedLegacy, err := s.importLegacyUsageIndexes()
	if err != nil {
		return result, err
	}
	changed := importedLegacy
	roots, err := discoverUsageSharedSessionRoots(cwd)
	if err != nil {
		return result, err
	}
	result.SessionRootsScanned = len(roots)
	rootsChanged, err := s.ensureUsageMetadata(usageMetadataKeySessionRootsScanned, strconv.Itoa(result.SessionRootsScanned))
	if err != nil {
		return result, err
	}
	changed = changed || rootsChanged
	rolloutsChanged, err := s.syncUsageRolloutRoots(roots)
	if err != nil {
		return result, err
	}
	changed = changed || rolloutsChanged
	repoBackfillChanged, err := s.backfillUsageRunRepoSlugs()
	if err != nil {
		return result, err
	}
	changed = changed || repoBackfillChanged
	workSyncUpdatedAt, workRunsChanged, err := s.syncUsageWorkRuns()
	if err != nil {
		return result, err
	}
	result.WorkSyncUpdatedAt = workSyncUpdatedAt
	changed = changed || workRunsChanged
	workSyncMarkerChanged, err := s.ensureUsageMetadata(usageMetadataKeyWorkSyncUpdatedAt, result.WorkSyncUpdatedAt)
	if err != nil {
		return result, err
	}
	changed = changed || workSyncMarkerChanged
	if strings.TrimSpace(importCompletedAt) == "" {
		importCompletedNow := ISOTimeNow()
		importMarkerChanged, err := s.ensureUsageMetadata(usageMetadataKeyImportCompletedAt, importCompletedNow)
		if err != nil {
			return result, err
		}
		changed = changed || importMarkerChanged
	}
	if changed {
		if err := s.bumpUsageDataVersion(); err != nil {
			return result, err
		}
	}
	result.LegacyImportedAt, _ = s.usageMetadata(usageMetadataKeyLegacyImportedAt)
	result.LegacyImportedFrom, _ = s.usageMetadata(usageMetadataKeyLegacyImportedFrom)
	return result, nil
}

func (s *localWorkDBStore) usageMetadata(key string) (string, error) {
	row := s.db.QueryRow(`SELECT value FROM usage_metadata WHERE key = ?`, strings.TrimSpace(key))
	var value string
	if err := row.Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func (s *localWorkDBStore) setUsageMetadata(key string, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO usage_metadata(key, value)
		 VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		strings.TrimSpace(key),
		strings.TrimSpace(value),
	)
	return err
}

func (s *localWorkDBStore) ensureUsageMetadata(key string, value string) (bool, error) {
	current, err := s.usageMetadata(key)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(current) == strings.TrimSpace(value) {
		return false, nil
	}
	if err := s.setUsageMetadata(key, value); err != nil {
		return false, err
	}
	return true, nil
}

func (s *localWorkDBStore) bumpUsageDataVersion() error {
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	version := sha256Hex(updatedAt)
	if err := s.setUsageMetadata(usageMetadataKeyDataUpdatedAt, updatedAt); err != nil {
		return err
	}
	return s.setUsageMetadata(usageMetadataKeyDataVersion, version)
}

func (s *localWorkDBStore) usageSQLiteSnapshot() (usageSQLiteSyncResult, error) {
	result := usageSQLiteSyncResult{}
	var err error
	result.LegacyImportedAt, err = s.usageMetadata(usageMetadataKeyLegacyImportedAt)
	if err != nil {
		return result, err
	}
	result.LegacyImportedFrom, err = s.usageMetadata(usageMetadataKeyLegacyImportedFrom)
	if err != nil {
		return result, err
	}
	result.WorkSyncUpdatedAt, err = s.usageMetadata(usageMetadataKeyWorkSyncUpdatedAt)
	if err != nil {
		return result, err
	}
	sessionRootsScanned, err := s.usageMetadata(usageMetadataKeySessionRootsScanned)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(sessionRootsScanned) != "" {
		result.SessionRootsScanned, _ = strconv.Atoi(strings.TrimSpace(sessionRootsScanned))
	}
	return result, nil
}

func loadUsageSQLiteState() (startUIUsageIndexState, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (startUIUsageIndexState, error) {
		snapshot, err := store.usageSQLiteSnapshot()
		if err != nil {
			return startUIUsageIndexState{}, err
		}
		return store.buildUsageIndexStateFromSQLite(snapshot)
	})
}

func loadUsageSQLiteVersion() string {
	version, err := withLocalWorkReadStore(func(store *localWorkDBStore) (string, error) {
		return store.usageMetadata(usageMetadataKeyDataVersion)
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(version)
}

// importLegacyUsageIndexes runs exactly once per workspace DB and is the only
// production path that still reads the old usage JSON snapshots.
func (s *localWorkDBStore) importLegacyUsageIndexes() (bool, error) {
	importedAt, err := s.usageMetadata(usageMetadataKeyLegacyImportedAt)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(importedAt) != "" {
		return false, nil
	}
	var sourceCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_sources`).Scan(&sourceCount); err != nil {
		return false, err
	}
	if sourceCount > 0 {
		return false, nil
	}
	candidates := []struct {
		Path   string
		Legacy bool
	}{
		{Path: startUIUsageIndexPath()},
		{Path: legacyStartUIUsageIndexPath(), Legacy: true},
	}
	for _, candidate := range candidates {
		state := readStartUIUsageIndexState(candidate.Path)
		if strings.TrimSpace(state.Version) == "" || len(state.Entries) == 0 {
			continue
		}
		for _, entry := range state.Entries {
			sourceKey := "legacy:" + strings.TrimSpace(entry.Path)
			record := entry.Record
			sessionKey := usageSQLiteSessionKey(sourceKey, record.SessionID, record.Lane, 0)
			timestampUnix := usageTimestampUnix(record.Timestamp)
			session := usageSQLiteSessionSpec{
				Key:           sessionKey,
				SourceKey:     sourceKey,
				Record:        record,
				TimestampUnix: timestampUnix,
				StartedAt:     0,
				UpdatedAt:     timestampUnix,
			}
			if record.HasTokenUsage {
				session.PartialWindowCoverage = true
			}
			payload := usageSQLiteUpsertPayload{
				Source: usageSQLiteSourceSpec{
					Key:             sourceKey,
					Kind:            usageStoreSourceKindLegacyIndex,
					Path:            strings.TrimSpace(entry.Path),
					Root:            defaultString(strings.TrimSpace(entry.Root), strings.TrimSpace(record.Root)),
					SourceUpdatedAt: strings.TrimSpace(entry.SourceUpdatedAt),
					SizeBytes:       entry.Size,
					ModifiedUnix:    entry.ModifiedUnixNano,
					UpdatedAt:       ISOTimeNow(),
				},
				Sessions: []usageSQLiteSessionSpec{session},
			}
			if _, err := s.replaceUsageSource(payload); err != nil {
				return false, err
			}
		}
		importedNow := ISOTimeNow()
		if err := s.setUsageMetadata(usageMetadataKeyLegacyImportedAt, importedNow); err != nil {
			return false, err
		}
		if err := s.setUsageMetadata(usageMetadataKeyLegacyImportedFrom, candidate.Path); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (s *localWorkDBStore) syncUsageRolloutRoots(roots []usageSessionRoot) (bool, error) {
	existing, err := s.usageSourceMetadataByKind(usageStoreSourceKindRollout)
	if err != nil {
		return false, err
	}
	seen := map[string]bool{}
	changed := false
	for _, root := range roots {
		rootName := root.Name
		err := walkRolloutFiles(root.SessionsDir, 0, func(path string) (bool, error) {
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					return false, nil
				}
				return false, err
			}
			cleanPath := filepath.Clean(path)
			seen[cleanPath] = true
			if meta, ok := existing[cleanPath]; ok &&
				meta.SizeBytes == info.Size() &&
				meta.ModifiedUnix == info.ModTime().UnixNano() &&
				strings.EqualFold(meta.Root, rootName) {
				return false, nil
			}
			replaced, err := s.ingestRolloutUsageSource(rootName, cleanPath, usageSQLiteSourceSpec{})
			changed = changed || replaced
			return false, err
		})
		if err != nil {
			return false, err
		}
	}
	for path, meta := range existing {
		if seen[path] || !usagePathUnderAnyRoot(path, roots) {
			continue
		}
		removed, err := s.deleteUsageSource(meta.Key)
		if err != nil {
			return false, err
		}
		changed = changed || removed
	}
	return changed, nil
}

func (s *localWorkDBStore) syncUsageWorkRuns() (string, bool, error) {
	updatedAfter, err := s.usageMetadata(usageMetadataKeyWorkSyncUpdatedAt)
	if err != nil {
		return "", false, err
	}
	rows, err := usageListWorkRunIndexEntriesAfter(updatedAfter)
	if err != nil {
		return updatedAfter, false, nil
	}
	latest := strings.TrimSpace(updatedAfter)
	changed := false
	for _, entry := range rows {
		if strings.TrimSpace(entry.UpdatedAt) != "" && strings.Compare(strings.TrimSpace(entry.UpdatedAt), latest) > 0 {
			latest = strings.TrimSpace(entry.UpdatedAt)
		}
		synced, err := s.syncUsageWorkRun(entry)
		if err != nil {
			return latest, changed, err
		}
		changed = changed || synced
	}
	return latest, changed, nil
}

func (s *localWorkDBStore) syncUsageWorkRun(entry workRunIndexEntry) (bool, error) {
	if strings.TrimSpace(entry.RunID) == "" {
		return false, nil
	}
	changed, err := s.updateUsageRunRepoSlug(entry.RunID, entry.RepoSlug)
	if err != nil {
		return false, err
	}
	if hasRollout, err := s.usageRunHasRolloutSource(entry.RunID); err == nil && hasRollout {
		return changed, nil
	}
	payloads, err := usageSQLitePayloadsForWorkRun(entry)
	if err != nil {
		return changed, err
	}
	replaced, err := s.replaceUsageRunSources(entry.RunID, payloads)
	return changed || replaced, err
}

func usageSQLitePayloadsForWorkRun(entry workRunIndexEntry) ([]usageSQLiteUpsertPayload, error) {
	switch strings.TrimSpace(entry.Backend) {
	case "local":
		manifest, err := readLocalWorkManifestByRunID(entry.RunID)
		if err != nil {
			return nil, err
		}
		return usageSQLitePayloadsForLocalWorkRun(manifest)
	case "github":
		manifestPath, _, err := resolveGithubRunManifestPath(entry.RunID, false)
		if err != nil {
			return nil, err
		}
		manifest := githubWorkManifest{}
		if err := readGithubJSON(manifestPath, &manifest); err != nil {
			return nil, err
		}
		return usageSQLitePayloadsForGithubWorkRun(filepath.Dir(manifestPath), manifest)
	default:
		return nil, nil
	}
}

func usageSQLitePayloadsForLocalWorkRun(manifest localWorkManifest) ([]usageSQLiteUpsertPayload, error) {
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	historyPath := usageHistoryArtifactPath(runDir)
	if history, err := readLocalWorkThreadUsageHistoryArtifact(historyPath); err == nil && len(history.Threads) > 0 {
		payload, err := usageSQLitePayloadFromLocalHistory(historyPath, manifest, history)
		if err != nil {
			return nil, err
		}
		return []usageSQLiteUpsertPayload{payload}, nil
	}
	if rows, err := usageHistoryRowsFromRoots(usageLocalWorkHistoryRoots(manifest.SandboxPath)); err == nil && len(rows) > 0 {
		payload := usageSQLitePayloadFromLocalHistoryRows(historyPath, manifest, rows)
		return []usageSQLiteUpsertPayload{payload}, nil
	}
	artifactPath := filepath.Join(runDir, "thread-usage.json")
	artifact, err := readLocalWorkThreadUsageArtifact(artifactPath)
	if err == nil && artifact != nil && len(artifact.Threads) > 0 {
		payload, err := usageSQLitePayloadFromLocalThreadArtifact(artifactPath, manifest, artifact)
		if err != nil {
			return nil, err
		}
		return []usageSQLiteUpsertPayload{payload}, nil
	}
	if manifest.TokenUsage != nil && manifest.TokenUsage.TotalTokens > 0 {
		return []usageSQLiteUpsertPayload{usageSQLitePayloadFromLocalManifest(manifest, artifactPath)}, nil
	}
	return nil, nil
}

func usageLocalWorkHistoryRoots(sandboxPath string) []string {
	if strings.TrimSpace(sandboxPath) == "" {
		return nil
	}
	roots := []usageSessionRoot{}
	seen := map[string]bool{}
	if err := discoverUsageSessionRootsRecursive(sandboxPath, &roots, seen); err != nil {
		return nil
	}
	paths := []string{}
	for _, root := range roots {
		if root.Name == "work" || root.Name == "local-work" {
			paths = append(paths, root.SessionsDir)
		}
	}
	sort.Strings(paths)
	return paths
}

func usageSQLitePayloadsForGithubWorkRun(runDir string, manifest githubWorkManifest) ([]usageSQLiteUpsertPayload, error) {
	historyPath := usageHistoryArtifactPath(runDir)
	if history, err := readGithubThreadUsageHistoryArtifact(historyPath); err == nil && len(history.Rows) > 0 {
		payload, err := usageSQLitePayloadFromGithubHistory(historyPath, manifest, history)
		if err != nil {
			return nil, err
		}
		return []usageSQLiteUpsertPayload{payload}, nil
	}
	if rows, err := usageHistoryRowsFromRoots(githubThreadUsageRoots(manifest.SandboxPath)); err == nil && len(rows) > 0 {
		payload := usageSQLitePayloadFromGithubHistoryRows(historyPath, manifest, rows)
		return []usageSQLiteUpsertPayload{payload}, nil
	}
	artifactPath := filepath.Join(runDir, "thread-usage.json")
	artifact, err := readGithubThreadUsageArtifact(artifactPath)
	if err == nil && artifact != nil && len(artifact.Rows) > 0 {
		payload, err := usageSQLitePayloadFromGithubThreadArtifact(artifactPath, manifest, artifact)
		if err != nil {
			return nil, err
		}
		return []usageSQLiteUpsertPayload{payload}, nil
	}
	return nil, nil
}

type usageSQLiteUpsertPayload struct {
	Source      usageSQLiteSourceSpec
	Sessions    []usageSQLiteSessionSpec
	Checkpoints []usageSQLiteCheckpointSpec
}

type usageSQLiteSourceMeta struct {
	Key          string
	Path         string
	Root         string
	Kind         string
	RunID        string
	SizeBytes    int64
	ModifiedUnix int64
}

func (s *localWorkDBStore) usageSourceMetadataByKind(kind string) (map[string]usageSQLiteSourceMeta, error) {
	rows, err := s.db.Query(`SELECT source_key, source_path, root, source_kind, COALESCE(run_id, ''), size_bytes, modified_unix_nano FROM usage_sources WHERE source_kind = ?`, strings.TrimSpace(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]usageSQLiteSourceMeta{}
	for rows.Next() {
		var meta usageSQLiteSourceMeta
		if err := rows.Scan(&meta.Key, &meta.Path, &meta.Root, &meta.Kind, &meta.RunID, &meta.SizeBytes, &meta.ModifiedUnix); err != nil {
			return nil, err
		}
		out[filepath.Clean(meta.Path)] = meta
	}
	return out, rows.Err()
}

func (s *localWorkDBStore) usageRunHasRolloutSource(runID string) (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_sources WHERE run_id = ? AND source_kind = ?`, strings.TrimSpace(runID), usageStoreSourceKindRollout).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *localWorkDBStore) replaceUsageRunSources(runID string, payloads []usageSQLiteUpsertPayload) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	changed := false
	result, err := tx.Exec(`DELETE FROM usage_sources WHERE run_id = ? AND source_kind != ?`, strings.TrimSpace(runID), usageStoreSourceKindRollout)
	if err != nil {
		return false, err
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		changed = true
	}
	for _, payload := range payloads {
		if err := replaceUsageSourceTx(tx, payload); err != nil {
			return false, err
		}
		changed = true
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed, nil
}

func (s *localWorkDBStore) ingestRolloutUsageSource(rootName string, path string, source usageSQLiteSourceSpec) (bool, error) {
	payload, err := usageSQLitePayloadFromRollout(rootName, path, source)
	if err != nil {
		return false, err
	}
	return s.replaceUsageSource(payload)
}

func (s *localWorkDBStore) replaceUsageSource(payload usageSQLiteUpsertPayload) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := replaceUsageSourceTx(tx, payload); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func replaceUsageSourceTx(tx *sql.Tx, payload usageSQLiteUpsertPayload) error {
	sourceKey := strings.TrimSpace(payload.Source.Key)
	if sourceKey == "" {
		return nil
	}
	if strings.EqualFold(payload.Source.Kind, usageStoreSourceKindRollout) {
		if _, err := tx.Exec(`DELETE FROM usage_sources WHERE source_kind = ? AND source_path = ?`, usageStoreSourceKindLegacyIndex, strings.TrimSpace(payload.Source.Path)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM usage_sources WHERE source_key = ?`, sourceKey); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO usage_sources(source_key, source_kind, source_path, root, run_id, repo_slug, backend, sandbox_path, source_updated_at, size_bytes, modified_unix_nano, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sourceKey,
		strings.TrimSpace(payload.Source.Kind),
		strings.TrimSpace(payload.Source.Path),
		strings.TrimSpace(payload.Source.Root),
		nullableString(payload.Source.RunID),
		nullableString(payload.Source.RepoSlug),
		nullableString(payload.Source.Backend),
		nullableString(payload.Source.SandboxPath),
		nullableString(payload.Source.SourceUpdatedAt),
		payload.Source.SizeBytes,
		payload.Source.ModifiedUnix,
		defaultString(strings.TrimSpace(payload.Source.UpdatedAt), ISOTimeNow()),
	); err != nil {
		return err
	}
	for _, session := range payload.Sessions {
		record := session.Record
		if _, err := tx.Exec(
			`INSERT INTO usage_sessions(
				session_key, source_key, session_id, timestamp, timestamp_unix, day, cwd, transcript_path, root, model,
				agent_role, agent_nickname, lane, activity, phase, input_tokens, cached_input_tokens, output_tokens,
				reasoning_output_tokens, total_tokens, has_token_usage, started_at, updated_at, partial_window_coverage
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.Key,
			sourceKey,
			nullableString(record.SessionID),
			strings.TrimSpace(record.Timestamp),
			session.TimestampUnix,
			strings.TrimSpace(record.Day),
			nullableString(record.CWD),
			strings.TrimSpace(record.TranscriptPath),
			strings.TrimSpace(record.Root),
			nullableString(record.Model),
			nullableString(record.AgentRole),
			nullableString(record.AgentNickname),
			nullableString(record.Lane),
			strings.TrimSpace(record.Activity),
			strings.TrimSpace(record.Phase),
			record.InputTokens,
			record.CachedInputTokens,
			record.OutputTokens,
			record.ReasoningOutputTokens,
			record.TotalTokens,
			usageBoolToInt(record.HasTokenUsage),
			session.StartedAt,
			session.UpdatedAt,
			usageBoolToInt(session.PartialWindowCoverage),
		); err != nil {
			return err
		}
	}
	for _, checkpoint := range payload.Checkpoints {
		currentAt := time.Unix(checkpoint.Current.Timestamp, 0).UTC().Format(time.RFC3339)
		day := time.Unix(checkpoint.Current.Timestamp, 0).UTC().Format("2006-01-02")
		if _, err := tx.Exec(
			`INSERT INTO usage_checkpoints(
				session_key, seq, checkpoint_ts, checkpoint_at, day, input_tokens, cached_input_tokens, output_tokens,
				reasoning_output_tokens, total_tokens, delta_input_tokens, delta_cached_input_tokens, delta_output_tokens,
				delta_reasoning_output_tokens, delta_total_tokens
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			checkpoint.SessionKey,
			checkpoint.Seq,
			checkpoint.Current.Timestamp,
			currentAt,
			day,
			checkpoint.Current.InputTokens,
			checkpoint.Current.CachedInputTokens,
			checkpoint.Current.OutputTokens,
			checkpoint.Current.ReasoningOutputTokens,
			checkpoint.Current.TotalTokens,
			checkpoint.Delta.InputTokens,
			checkpoint.Delta.CachedInputTokens,
			checkpoint.Delta.OutputTokens,
			checkpoint.Delta.ReasoningOutputTokens,
			checkpoint.Delta.TotalTokens,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *localWorkDBStore) updateUsageRunRepoSlug(runID string, repoSlug string) (bool, error) {
	trimmedRunID := strings.TrimSpace(runID)
	if trimmedRunID == "" {
		return false, nil
	}
	trimmedRepoSlug := strings.TrimSpace(repoSlug)
	if trimmedRepoSlug != "" && !validRepoSlug(trimmedRepoSlug) {
		trimmedRepoSlug = ""
	}
	result, err := s.db.Exec(`UPDATE usage_sources SET repo_slug = ? WHERE run_id = ? AND COALESCE(repo_slug, '') != ?`, nullableString(trimmedRepoSlug), trimmedRunID, trimmedRepoSlug)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

func (s *localWorkDBStore) backfillUsageRunRepoSlugs() (bool, error) {
	result, err := s.db.Exec(`
		UPDATE usage_sources
		SET repo_slug = (
			SELECT idx.repo_slug
			FROM work_run_index idx
			WHERE idx.run_id = usage_sources.run_id
		)
		WHERE COALESCE(usage_sources.repo_slug, '') = ''
		  AND COALESCE(usage_sources.run_id, '') != ''
		  AND EXISTS (
			SELECT 1
			FROM work_run_index idx
			WHERE idx.run_id = usage_sources.run_id
			  AND COALESCE(idx.repo_slug, '') != ''
		  )
	`)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

func (s *localWorkDBStore) deleteUsageSource(sourceKey string) (bool, error) {
	result, err := s.db.Exec(`DELETE FROM usage_sources WHERE source_key = ?`, strings.TrimSpace(sourceKey))
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

func usageSQLitePayloadFromRollout(rootName string, path string, source usageSQLiteSourceSpec) (usageSQLiteUpsertPayload, error) {
	info, err := os.Stat(path)
	if err != nil {
		return usageSQLiteUpsertPayload{}, err
	}
	record, err := parseUsageRollout(path, rootName)
	if err != nil {
		return usageSQLiteUpsertPayload{}, err
	}
	historyRow, hasHistory, err := readUsageHistoryRow(path)
	if err != nil && !os.IsNotExist(err) {
		return usageSQLiteUpsertPayload{}, err
	}
	if hasHistory {
		record.SessionID = defaultString(strings.TrimSpace(record.SessionID), strings.TrimSpace(historyRow.SessionID))
		record.Model = defaultString(strings.TrimSpace(record.Model), strings.TrimSpace(historyRow.Model))
		record.CWD = defaultString(strings.TrimSpace(record.CWD), strings.TrimSpace(historyRow.CWD))
		record.AgentRole = defaultString(strings.TrimSpace(record.AgentRole), strings.TrimSpace(historyRow.Role))
		record.AgentNickname = defaultString(strings.TrimSpace(record.AgentNickname), strings.TrimSpace(historyRow.Nickname))
		record.Lane = defaultString(strings.TrimSpace(record.Lane), strings.TrimSpace(historyRow.Role))
	}
	source.Key = filepath.Clean(path)
	source.Kind = usageStoreSourceKindRollout
	source.Path = filepath.Clean(path)
	source.Root = defaultString(strings.TrimSpace(rootName), strings.TrimSpace(record.Root))
	source.SizeBytes = info.Size()
	source.ModifiedUnix = info.ModTime().UnixNano()
	source.SourceUpdatedAt = defaultString(strings.TrimSpace(source.SourceUpdatedAt), strings.TrimSpace(record.Timestamp))
	source.UpdatedAt = defaultString(strings.TrimSpace(source.UpdatedAt), ISOTimeNow())
	startedAt := int64(0)
	updatedAt := usageTimestampUnix(record.Timestamp)
	if hasHistory {
		startedAt = historyRow.StartedAt
		updatedAt = max(updatedAt, historyRow.UpdatedAt)
	}
	sessionKey := usageSQLiteSessionKey(source.Key, record.SessionID, defaultString(record.Lane, record.AgentRole), startedAt)
	session := usageSQLiteSessionSpec{
		Key:           sessionKey,
		SourceKey:     source.Key,
		Record:        record,
		TimestampUnix: usageTimestampUnix(record.Timestamp),
		StartedAt:     startedAt,
		UpdatedAt:     updatedAt,
	}
	checkpoints := []usageSQLiteCheckpointSpec{}
	if hasHistory {
		checkpoints = usageSQLiteCheckpointSpecs(sessionKey, historyRow.Checkpoints)
	}
	return usageSQLiteUpsertPayload{
		Source:      source,
		Sessions:    []usageSQLiteSessionSpec{session},
		Checkpoints: checkpoints,
	}, nil
}

func usageSQLitePayloadFromLocalHistory(path string, manifest localWorkManifest, artifact *localWorkThreadUsageHistoryArtifact) (usageSQLiteUpsertPayload, error) {
	info, err := os.Stat(path)
	if err != nil {
		return usageSQLiteUpsertPayload{}, err
	}
	return usageSQLitePayloadFromLocalHistoryRowsWithMeta(path, manifest, artifact.Threads, info.Size(), info.ModTime().UnixNano()), nil
}

func usageSQLitePayloadFromLocalHistoryRows(path string, manifest localWorkManifest, rows []usageHistoryRow) usageSQLiteUpsertPayload {
	return usageSQLitePayloadFromLocalHistoryRowsWithMeta(path, manifest, rows, 0, 0)
}

func usageSQLitePayloadFromLocalHistoryRowsWithMeta(path string, manifest localWorkManifest, rows []usageHistoryRow, sizeBytes int64, modifiedUnix int64) usageSQLiteUpsertPayload {
	source := usageSQLiteSourceSpec{
		Key:             filepath.Clean(path),
		Kind:            usageStoreSourceKindLocalHistory,
		Path:            filepath.Clean(path),
		Root:            "work",
		RunID:           strings.TrimSpace(manifest.RunID),
		RepoSlug:        strings.TrimSpace(manifest.RepoSlug),
		Backend:         "local",
		SandboxPath:     strings.TrimSpace(manifest.SandboxPath),
		SourceUpdatedAt: strings.TrimSpace(manifest.UpdatedAt),
		SizeBytes:       sizeBytes,
		ModifiedUnix:    modifiedUnix,
		UpdatedAt:       ISOTimeNow(),
	}
	payload := usageSQLiteUpsertPayload{Source: source}
	for _, row := range rows {
		converted, ok := localWorkThreadUsageRowFromHistory(row)
		if !ok {
			continue
		}
		record := usageRecordFromLocalThreadRow(manifest, path, converted)
		sessionKey := usageSQLiteSessionKey(source.Key, record.SessionID, defaultString(record.Lane, record.AgentRole), row.StartedAt)
		payload.Sessions = append(payload.Sessions, usageSQLiteSessionSpec{
			Key:           sessionKey,
			SourceKey:     source.Key,
			Record:        record,
			TimestampUnix: usageTimestampUnix(record.Timestamp),
			StartedAt:     row.StartedAt,
			UpdatedAt:     row.UpdatedAt,
		})
		payload.Checkpoints = append(payload.Checkpoints, usageSQLiteCheckpointSpecs(sessionKey, row.Checkpoints)...)
	}
	return payload
}

func usageSQLitePayloadFromLocalThreadArtifact(path string, manifest localWorkManifest, artifact *localWorkThreadUsageArtifact) (usageSQLiteUpsertPayload, error) {
	info, err := os.Stat(path)
	if err != nil {
		return usageSQLiteUpsertPayload{}, err
	}
	source := usageSQLiteSourceSpec{
		Key:             filepath.Clean(path),
		Kind:            usageStoreSourceKindLocalThread,
		Path:            filepath.Clean(path),
		Root:            "work",
		RunID:           strings.TrimSpace(manifest.RunID),
		RepoSlug:        strings.TrimSpace(manifest.RepoSlug),
		Backend:         "local",
		SandboxPath:     strings.TrimSpace(manifest.SandboxPath),
		SourceUpdatedAt: defaultString(strings.TrimSpace(manifest.UpdatedAt), strings.TrimSpace(artifact.GeneratedAt)),
		SizeBytes:       info.Size(),
		ModifiedUnix:    info.ModTime().UnixNano(),
		UpdatedAt:       ISOTimeNow(),
	}
	payload := usageSQLiteUpsertPayload{Source: source}
	for _, row := range artifact.Threads {
		record := usageRecordFromLocalThreadRow(manifest, path, row)
		sessionKey := usageSQLiteSessionKey(source.Key, record.SessionID, defaultString(record.Lane, record.AgentRole), row.StartedAt)
		payload.Sessions = append(payload.Sessions, usageSQLiteSessionSpec{
			Key:                   sessionKey,
			SourceKey:             source.Key,
			Record:                record,
			TimestampUnix:         usageTimestampUnix(record.Timestamp),
			StartedAt:             row.StartedAt,
			UpdatedAt:             row.UpdatedAt,
			PartialWindowCoverage: record.HasTokenUsage,
		})
	}
	return payload, nil
}

func usageSQLitePayloadFromLocalManifest(manifest localWorkManifest, artifactPath string) usageSQLiteUpsertPayload {
	record := usageRecord{
		SessionID:             defaultString(strings.TrimSpace(manifest.RunID), "(unknown)"),
		Timestamp:             defaultString(strings.TrimSpace(manifest.UpdatedAt), strings.TrimSpace(manifest.CreatedAt)),
		Day:                   usageRecordDay(defaultString(strings.TrimSpace(manifest.UpdatedAt), strings.TrimSpace(manifest.CreatedAt)), artifactPath),
		CWD:                   defaultString(strings.TrimSpace(manifest.SandboxRepoPath), strings.TrimSpace(manifest.RepoRoot)),
		TranscriptPath:        artifactPath,
		Root:                  "work",
		Lane:                  "leader",
		Activity:              "work",
		Phase:                 "implementation",
		InputTokens:           manifest.TokenUsage.InputTokens,
		CachedInputTokens:     manifest.TokenUsage.CachedInputTokens,
		OutputTokens:          manifest.TokenUsage.OutputTokens,
		ReasoningOutputTokens: manifest.TokenUsage.ReasoningOutputTokens,
		TotalTokens:           manifest.TokenUsage.TotalTokens,
		HasTokenUsage:         true,
	}
	source := usageSQLiteSourceSpec{
		Key:             "local-manifest:" + strings.TrimSpace(manifest.RunID),
		Kind:            usageStoreSourceKindLocalManifest,
		Path:            artifactPath,
		Root:            "work",
		RunID:           strings.TrimSpace(manifest.RunID),
		RepoSlug:        strings.TrimSpace(manifest.RepoSlug),
		Backend:         "local",
		SandboxPath:     strings.TrimSpace(manifest.SandboxPath),
		SourceUpdatedAt: strings.TrimSpace(manifest.UpdatedAt),
		UpdatedAt:       ISOTimeNow(),
	}
	sessionKey := usageSQLiteSessionKey(source.Key, record.SessionID, record.Lane, 0)
	return usageSQLiteUpsertPayload{
		Source: source,
		Sessions: []usageSQLiteSessionSpec{{
			Key:                   sessionKey,
			SourceKey:             source.Key,
			Record:                record,
			TimestampUnix:         usageTimestampUnix(record.Timestamp),
			UpdatedAt:             usageTimestampUnix(record.Timestamp),
			PartialWindowCoverage: true,
		}},
	}
}

func usageSQLitePayloadFromGithubHistory(path string, manifest githubWorkManifest, artifact *githubThreadUsageHistoryArtifact) (usageSQLiteUpsertPayload, error) {
	info, err := os.Stat(path)
	if err != nil {
		return usageSQLiteUpsertPayload{}, err
	}
	return usageSQLitePayloadFromGithubHistoryRowsWithMeta(path, manifest, artifact.Rows, info.Size(), info.ModTime().UnixNano()), nil
}

func usageSQLitePayloadFromGithubHistoryRows(path string, manifest githubWorkManifest, rows []usageHistoryRow) usageSQLiteUpsertPayload {
	return usageSQLitePayloadFromGithubHistoryRowsWithMeta(path, manifest, rows, 0, 0)
}

func usageSQLitePayloadFromGithubHistoryRowsWithMeta(path string, manifest githubWorkManifest, rows []usageHistoryRow, sizeBytes int64, modifiedUnix int64) usageSQLiteUpsertPayload {
	source := usageSQLiteSourceSpec{
		Key:             filepath.Clean(path),
		Kind:            usageStoreSourceKindGithubHistory,
		Path:            filepath.Clean(path),
		Root:            "work",
		RunID:           strings.TrimSpace(manifest.RunID),
		RepoSlug:        strings.TrimSpace(manifest.RepoSlug),
		Backend:         "github",
		SandboxPath:     strings.TrimSpace(manifest.SandboxPath),
		SourceUpdatedAt: strings.TrimSpace(manifest.UpdatedAt),
		SizeBytes:       sizeBytes,
		ModifiedUnix:    modifiedUnix,
		UpdatedAt:       ISOTimeNow(),
	}
	payload := usageSQLiteUpsertPayload{Source: source}
	for _, row := range rows {
		converted, ok := githubThreadUsageRowFromHistory(row)
		if !ok {
			continue
		}
		record := usageRecordFromGithubThreadRow(manifest, path, converted)
		sessionKey := usageSQLiteSessionKey(source.Key, record.SessionID, defaultString(record.Lane, record.AgentRole), row.StartedAt)
		payload.Sessions = append(payload.Sessions, usageSQLiteSessionSpec{
			Key:           sessionKey,
			SourceKey:     source.Key,
			Record:        record,
			TimestampUnix: usageTimestampUnix(record.Timestamp),
			StartedAt:     row.StartedAt,
			UpdatedAt:     row.UpdatedAt,
		})
		payload.Checkpoints = append(payload.Checkpoints, usageSQLiteCheckpointSpecs(sessionKey, row.Checkpoints)...)
	}
	return payload
}

func usageSQLitePayloadFromGithubThreadArtifact(path string, manifest githubWorkManifest, artifact *githubThreadUsageArtifact) (usageSQLiteUpsertPayload, error) {
	info, err := os.Stat(path)
	if err != nil {
		return usageSQLiteUpsertPayload{}, err
	}
	source := usageSQLiteSourceSpec{
		Key:             filepath.Clean(path),
		Kind:            usageStoreSourceKindGithubThread,
		Path:            filepath.Clean(path),
		Root:            "work",
		RunID:           strings.TrimSpace(manifest.RunID),
		RepoSlug:        strings.TrimSpace(manifest.RepoSlug),
		Backend:         "github",
		SandboxPath:     strings.TrimSpace(manifest.SandboxPath),
		SourceUpdatedAt: defaultString(strings.TrimSpace(manifest.UpdatedAt), strings.TrimSpace(artifact.GeneratedAt)),
		SizeBytes:       info.Size(),
		ModifiedUnix:    info.ModTime().UnixNano(),
		UpdatedAt:       ISOTimeNow(),
	}
	payload := usageSQLiteUpsertPayload{Source: source}
	for _, row := range artifact.Rows {
		record := usageRecordFromGithubThreadRow(manifest, path, row)
		sessionKey := usageSQLiteSessionKey(source.Key, record.SessionID, defaultString(record.Lane, record.AgentRole), row.StartedAt)
		payload.Sessions = append(payload.Sessions, usageSQLiteSessionSpec{
			Key:                   sessionKey,
			SourceKey:             source.Key,
			Record:                record,
			TimestampUnix:         usageTimestampUnix(record.Timestamp),
			StartedAt:             row.StartedAt,
			UpdatedAt:             row.UpdatedAt,
			PartialWindowCoverage: record.HasTokenUsage,
		})
	}
	return payload, nil
}

func usageSQLiteSessionKey(sourceKey string, sessionID string, fallback string, startedAt int64) string {
	if strings.TrimSpace(sessionID) != "" {
		return strings.TrimSpace(sourceKey) + "#" + strings.TrimSpace(sessionID)
	}
	key := defaultString(strings.TrimSpace(fallback), "(unknown)")
	if startedAt > 0 {
		key += "-" + strconv.FormatInt(startedAt, 10)
	}
	return strings.TrimSpace(sourceKey) + "#" + key
}

func usageSQLiteCheckpointSpecs(sessionKey string, checkpoints []usageTokenCheckpoint) []usageSQLiteCheckpointSpec {
	if len(checkpoints) == 0 {
		return nil
	}
	specs := make([]usageSQLiteCheckpointSpec, 0, len(checkpoints))
	previous := usageTokenCheckpoint{}
	for index, checkpoint := range checkpoints {
		delta := usageCheckpointDelta(checkpoint, previous)
		previous = usageMaxCheckpoint(previous, checkpoint)
		specs = append(specs, usageSQLiteCheckpointSpec{
			SessionKey: sessionKey,
			Seq:        index,
			Current:    checkpoint,
			Delta:      delta,
		})
	}
	return specs
}

func usageCheckpointDelta(current usageTokenCheckpoint, previous usageTokenCheckpoint) usageTokenCheckpoint {
	return usageTokenCheckpoint{
		InputTokens:           max(0, current.InputTokens-previous.InputTokens),
		CachedInputTokens:     max(0, current.CachedInputTokens-previous.CachedInputTokens),
		OutputTokens:          max(0, current.OutputTokens-previous.OutputTokens),
		ReasoningOutputTokens: max(0, current.ReasoningOutputTokens-previous.ReasoningOutputTokens),
		TotalTokens:           max(0, current.TotalTokens-previous.TotalTokens),
	}
}

func usageMaxCheckpoint(left usageTokenCheckpoint, right usageTokenCheckpoint) usageTokenCheckpoint {
	timestamp := left.Timestamp
	if right.Timestamp > timestamp {
		timestamp = right.Timestamp
	}
	return usageTokenCheckpoint{
		Timestamp:             timestamp,
		InputTokens:           max(left.InputTokens, right.InputTokens),
		CachedInputTokens:     max(left.CachedInputTokens, right.CachedInputTokens),
		OutputTokens:          max(left.OutputTokens, right.OutputTokens),
		ReasoningOutputTokens: max(left.ReasoningOutputTokens, right.ReasoningOutputTokens),
		TotalTokens:           max(left.TotalTokens, right.TotalTokens),
	}
}

func usageTimestampUnix(timestamp string) int64 {
	timestamp = strings.TrimSpace(timestamp)
	if timestamp == "" {
		return 0
	}
	if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		return parsed.Unix()
	}
	if parsed, err := time.Parse(time.RFC3339, timestamp); err == nil {
		return parsed.Unix()
	}
	return 0
}

func usageBoolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func usageEffectiveSourceCondition(alias string) string {
	return fmt.Sprintf(`(COALESCE(%s.run_id, '') = '' OR %s.source_kind = 'rollout' OR NOT EXISTS (
		SELECT 1
		FROM usage_sources usage_rollout
		WHERE usage_rollout.run_id = %s.run_id
		  AND usage_rollout.source_kind = 'rollout'
	))`, alias, alias, alias)
}

func (s *localWorkDBStore) buildUsageIndexStateFromSQLite(snapshot usageSQLiteSyncResult) (startUIUsageIndexState, error) {
	version, err := s.usageMetadata(usageMetadataKeyDataVersion)
	if err != nil {
		return startUIUsageIndexState{}, err
	}
	updatedAt, err := s.usageMetadata(usageMetadataKeyDataUpdatedAt)
	if err != nil {
		return startUIUsageIndexState{}, err
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT
			src.root,
			src.source_kind,
			COALESCE(src.source_updated_at, ''),
			src.size_bytes,
			src.modified_unix_nano,
			COALESCE(src.repo_slug, ''),
			ses.session_id,
			ses.timestamp,
			ses.day,
			COALESCE(ses.cwd, ''),
			ses.transcript_path,
			ses.root,
			COALESCE(ses.model, ''),
			COALESCE(ses.agent_role, ''),
			COALESCE(ses.agent_nickname, ''),
			COALESCE(ses.lane, ''),
			ses.activity,
			ses.phase,
			ses.input_tokens,
			ses.cached_input_tokens,
			ses.output_tokens,
			ses.reasoning_output_tokens,
			ses.total_tokens,
			ses.has_token_usage,
			ses.session_key
		FROM usage_sessions ses
		JOIN usage_sources src ON src.source_key = ses.source_key
		WHERE %s
		ORDER BY src.source_path ASC, ses.session_key ASC
	`, usageEffectiveSourceCondition("src")))
	if err != nil {
		return startUIUsageIndexState{}, err
	}
	defer rows.Close()

	state := startUIUsageIndexState{
		SchemaVersion:       startUIUsageIndexSchemaVersion,
		Version:             strings.TrimSpace(version),
		UpdatedAt:           strings.TrimSpace(updatedAt),
		SessionRootsScanned: snapshot.SessionRootsScanned,
		WorkSyncUpdatedAt:   snapshot.WorkSyncUpdatedAt,
		LegacyImportedAt:    snapshot.LegacyImportedAt,
		LegacyImportedFrom:  snapshot.LegacyImportedFrom,
		Entries:             []startUIUsageIndexEntry{},
	}
	for rows.Next() {
		var (
			sourceRoot        string
			sourceKind        string
			sourceUpdatedAt   string
			sizeBytes         int64
			modifiedUnixNano  int64
			repoSlug          string
			sessionID         string
			timestamp         string
			day               string
			cwd               string
			transcriptPath    string
			recordRoot        string
			model             string
			agentRole         string
			agentNickname     string
			lane              string
			activity          string
			phase             string
			inputTokens       int
			cachedInputTokens int
			outputTokens      int
			reasoningTokens   int
			totalTokens       int
			hasTokenUsage     int
			sessionKey        string
		)
		if err := rows.Scan(
			&sourceRoot,
			&sourceKind,
			&sourceUpdatedAt,
			&sizeBytes,
			&modifiedUnixNano,
			&repoSlug,
			&sessionID,
			&timestamp,
			&day,
			&cwd,
			&transcriptPath,
			&recordRoot,
			&model,
			&agentRole,
			&agentNickname,
			&lane,
			&activity,
			&phase,
			&inputTokens,
			&cachedInputTokens,
			&outputTokens,
			&reasoningTokens,
			&totalTokens,
			&hasTokenUsage,
			&sessionKey,
		); err != nil {
			return startUIUsageIndexState{}, err
		}
		state.Entries = append(state.Entries, startUIUsageIndexEntry{
			Path:             sessionKey,
			Root:             sourceRoot,
			Size:             sizeBytes,
			ModifiedUnixNano: modifiedUnixNano,
			SourceKind:       sourceKind,
			SourceUpdatedAt:  sourceUpdatedAt,
			Record: usageRecord{
				SessionID:             sessionID,
				Timestamp:             timestamp,
				Day:                   day,
				CWD:                   cwd,
				TranscriptPath:        transcriptPath,
				RepoSlug:              repoSlug,
				Root:                  recordRoot,
				Model:                 model,
				AgentRole:             agentRole,
				AgentNickname:         agentNickname,
				Lane:                  lane,
				Activity:              activity,
				Phase:                 phase,
				InputTokens:           inputTokens,
				CachedInputTokens:     cachedInputTokens,
				OutputTokens:          outputTokens,
				ReasoningOutputTokens: reasoningTokens,
				TotalTokens:           totalTokens,
				HasTokenUsage:         hasTokenUsage == 1,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return startUIUsageIndexState{}, err
	}
	return state, nil
}

func loadLocalWorkTokenUsageTotalsFromSQLite(runID string) (*localWorkTokenUsageTotals, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (*localWorkTokenUsageTotals, error) {
		rows, err := store.db.Query(fmt.Sprintf(`
			SELECT
				ses.input_tokens,
				ses.cached_input_tokens,
				ses.output_tokens,
				ses.reasoning_output_tokens,
				ses.total_tokens,
				ses.updated_at
			FROM usage_sessions ses
			JOIN usage_sources src ON src.source_key = ses.source_key
			WHERE src.run_id = ?
			  AND %s
			ORDER BY ses.updated_at DESC
		`, usageEffectiveSourceCondition("src")), strings.TrimSpace(runID))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		totals := &localWorkTokenUsageTotals{}
		var latestUpdatedAt int64
		for rows.Next() {
			var (
				inputTokens       int
				cachedInputTokens int
				outputTokens      int
				reasoningTokens   int
				totalTokens       int
				updatedAt         int64
			)
			if err := rows.Scan(
				&inputTokens,
				&cachedInputTokens,
				&outputTokens,
				&reasoningTokens,
				&totalTokens,
				&updatedAt,
			); err != nil {
				return nil, err
			}
			totals.InputTokens += inputTokens
			totals.CachedInputTokens += cachedInputTokens
			totals.OutputTokens += outputTokens
			totals.ReasoningOutputTokens += reasoningTokens
			totals.TotalTokens += totalTokens
			totals.SessionsAccounted++
			if updatedAt > latestUpdatedAt {
				latestUpdatedAt = updatedAt
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if totals.SessionsAccounted == 0 {
			return nil, nil
		}
		if latestUpdatedAt > 0 {
			totals.UpdatedAt = time.Unix(latestUpdatedAt, 0).UTC().Format(time.RFC3339)
		}
		return totals, nil
	})
}

func loadGithubThreadUsageArtifactFromSQLite(runID string) (*githubThreadUsageArtifact, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (*githubThreadUsageArtifact, error) {
		rows, err := store.db.Query(fmt.Sprintf(`
			SELECT
				COALESCE(ses.session_id, ''),
				COALESCE(ses.agent_nickname, ''),
				COALESCE(ses.agent_role, ''),
				COALESCE(ses.model, ''),
				COALESCE(ses.cwd, ''),
				ses.total_tokens,
				ses.started_at,
				ses.updated_at
			FROM usage_sessions ses
			JOIN usage_sources src ON src.source_key = ses.source_key
			WHERE src.run_id = ?
			  AND %s
			ORDER BY ses.started_at ASC, ses.session_id ASC
		`, usageEffectiveSourceCondition("src")), strings.TrimSpace(runID))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		artifact := &githubThreadUsageArtifact{
			Version:     1,
			GeneratedAt: ISOTimeNow(),
			Rows:        []githubThreadUsageRow{},
		}
		for rows.Next() {
			var row githubThreadUsageRow
			if err := rows.Scan(
				&row.SessionID,
				&row.Nickname,
				&row.Role,
				&row.Model,
				&row.CWD,
				&row.TokensUsed,
				&row.StartedAt,
				&row.UpdatedAt,
			); err != nil {
				return nil, err
			}
			artifact.Rows = append(artifact.Rows, row)
			artifact.TotalTokens += row.TokensUsed
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return artifact, nil
	})
}

func syncUsageForRun(runID string) error {
	entry, err := readWorkRunIndex(strings.TrimSpace(runID))
	if err != nil {
		return err
	}
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		changed, err := store.syncUsageWorkRun(entry)
		if err != nil {
			return err
		}
		if workSyncChanged, err := store.ensureUsageMetadata(usageMetadataKeyWorkSyncUpdatedAt, strings.TrimSpace(entry.UpdatedAt)); err != nil {
			return err
		} else if workSyncChanged {
			changed = true
		}
		if changed {
			return store.bumpUsageDataVersion()
		}
		return nil
	})
}

func loadWindowedUsageReportSourceFromSQLite(options usageOptions, sessionRootsScanned int) (usageReportSource, error) {
	projectFilter := normalizeUsageProjectFilter(options.Project, options.CWD)
	projectRepoID := ""
	if projectFilter != "" {
		if info, err := os.Stat(projectFilter); err == nil && info.IsDir() {
			projectRepoID = localWorkRepoID(projectFilter)
		}
	}
	sinceCutoff, err := parseSinceSpec(options.Since)
	if err != nil {
		return usageReportSource{}, err
	}
	sinceCutoffUnix := time.UnixMilli(sinceCutoff).Unix()

	return withLocalWorkReadStore(func(store *localWorkDBStore) (usageReportSource, error) {
		records, err := store.queryWindowedUsageRecords(options, projectFilter, projectRepoID, sinceCutoffUnix)
		if err != nil {
			return usageReportSource{}, err
		}
		dayGroups, err := store.queryWindowedUsageDayGroups(options, projectFilter, projectRepoID, sinceCutoffUnix)
		if err != nil {
			return usageReportSource{}, err
		}
		partialCoverage, err := store.queryWindowedPartialCoverage(options, projectFilter, projectRepoID, sinceCutoffUnix)
		if err != nil {
			return usageReportSource{}, err
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].Timestamp == records[j].Timestamp {
				return records[i].SessionID < records[j].SessionID
			}
			return records[i].Timestamp > records[j].Timestamp
		})
		return usageReportSource{
			Records:             records,
			DayGroups:           dayGroups,
			SessionRootsScanned: sessionRootsScanned,
			TimeBasis:           usageTimeBasisWindowDelta,
			Coverage:            usageCoverageValue(partialCoverage),
		}, nil
	})
}

func usageCoverageValue(partial bool) string {
	if partial {
		return usageCoveragePartial
	}
	return usageCoverageFull
}

func (s *localWorkDBStore) queryWindowedUsageRecords(options usageOptions, projectFilter string, projectRepoID string, sinceCutoffUnix int64) ([]usageRecord, error) {
	filterSQL, filterArgs := usageWindowedSessionFilters("ses", options, projectFilter, projectRepoID)
	query := fmt.Sprintf(`
		SELECT
			COALESCE(ses.session_id, ''),
			MAX(ck.checkpoint_ts),
			COALESCE(ses.cwd, ''),
			ses.transcript_path,
			COALESCE(src.repo_slug, ''),
			ses.root,
			COALESCE(ses.model, ''),
			COALESCE(ses.agent_role, ''),
			COALESCE(ses.agent_nickname, ''),
			COALESCE(ses.lane, ''),
			ses.activity,
			ses.phase,
			SUM(ck.delta_input_tokens),
			SUM(ck.delta_cached_input_tokens),
			SUM(ck.delta_output_tokens),
			SUM(ck.delta_reasoning_output_tokens),
			SUM(ck.delta_total_tokens)
		FROM usage_sessions ses
		JOIN usage_sources src ON src.source_key = ses.source_key
		JOIN usage_checkpoints ck ON ck.session_key = ses.session_key
		WHERE %s
		  AND ck.checkpoint_ts >= ?%s
		GROUP BY ses.session_key, ses.session_id, ses.cwd, ses.transcript_path, src.repo_slug, ses.root, ses.model, ses.agent_role, ses.agent_nickname, ses.lane, ses.activity, ses.phase
	`, usageEffectiveSourceCondition("src"), filterSQL)
	args := []any{sinceCutoffUnix}
	args = append(args, filterArgs...)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []usageRecord{}
	for rows.Next() {
		var (
			sessionID         string
			latestTs          int64
			cwd               string
			transcriptPath    string
			repoSlug          string
			root              string
			model             string
			agentRole         string
			agentNickname     string
			lane              string
			activity          string
			phase             string
			inputTokens       int
			cachedInputTokens int
			outputTokens      int
			reasoningTokens   int
			totalTokens       int
		)
		if err := rows.Scan(
			&sessionID,
			&latestTs,
			&cwd,
			&transcriptPath,
			&repoSlug,
			&root,
			&model,
			&agentRole,
			&agentNickname,
			&lane,
			&activity,
			&phase,
			&inputTokens,
			&cachedInputTokens,
			&outputTokens,
			&reasoningTokens,
			&totalTokens,
		); err != nil {
			return nil, err
		}
		timestamp := time.Unix(latestTs, 0).UTC().Format(time.RFC3339)
		records = append(records, usageRecord{
			SessionID:             sessionID,
			Timestamp:             timestamp,
			Day:                   time.Unix(latestTs, 0).UTC().Format("2006-01-02"),
			CWD:                   cwd,
			TranscriptPath:        transcriptPath,
			RepoSlug:              repoSlug,
			Root:                  root,
			Model:                 model,
			AgentRole:             agentRole,
			AgentNickname:         agentNickname,
			Lane:                  lane,
			Activity:              activity,
			Phase:                 phase,
			InputTokens:           inputTokens,
			CachedInputTokens:     cachedInputTokens,
			OutputTokens:          outputTokens,
			ReasoningOutputTokens: reasoningTokens,
			TotalTokens:           totalTokens,
			HasTokenUsage:         true,
		})
	}
	return records, rows.Err()
}

func (s *localWorkDBStore) queryWindowedUsageDayGroups(options usageOptions, projectFilter string, projectRepoID string, sinceCutoffUnix int64) ([]usageGroupRow, error) {
	filterSQL, filterArgs := usageWindowedSessionFilters("ses", options, projectFilter, projectRepoID)
	query := fmt.Sprintf(`
		SELECT
			ck.day,
			COUNT(DISTINCT ses.session_key),
			SUM(ck.delta_input_tokens),
			SUM(ck.delta_cached_input_tokens),
			SUM(ck.delta_output_tokens),
			SUM(ck.delta_reasoning_output_tokens),
			SUM(ck.delta_total_tokens)
		FROM usage_sessions ses
		JOIN usage_sources src ON src.source_key = ses.source_key
		JOIN usage_checkpoints ck ON ck.session_key = ses.session_key
		WHERE %s
		  AND ck.checkpoint_ts >= ?%s
		GROUP BY ck.day
	`, usageEffectiveSourceCondition("src"), filterSQL)
	args := []any{sinceCutoffUnix}
	args = append(args, filterArgs...)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []usageGroupRow{}
	for rows.Next() {
		var group usageGroupRow
		if err := rows.Scan(
			&group.Key,
			&group.Sessions,
			&group.InputTokens,
			&group.CachedInputTokens,
			&group.OutputTokens,
			&group.ReasoningOutputTokens,
			&group.TotalTokens,
		); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].TotalTokens == groups[j].TotalTokens {
			return groups[i].Key < groups[j].Key
		}
		return groups[i].TotalTokens > groups[j].TotalTokens
	})
	return groups, nil
}

func (s *localWorkDBStore) queryWindowedPartialCoverage(options usageOptions, projectFilter string, projectRepoID string, sinceCutoffUnix int64) (bool, error) {
	filterSQL, filterArgs := usageWindowedSessionFilters("ses", options, projectFilter, projectRepoID)
	query := fmt.Sprintf(`
		SELECT 1
		FROM usage_sessions ses
		JOIN usage_sources src ON src.source_key = ses.source_key
		WHERE %s
		  AND ses.partial_window_coverage = 1
		  AND ses.has_token_usage = 1
		  AND ses.updated_at >= ?%s
		LIMIT 1
	`, usageEffectiveSourceCondition("src"), filterSQL)
	args := []any{sinceCutoffUnix}
	args = append(args, filterArgs...)
	row := s.db.QueryRow(query, args...)
	var marker int
	if err := row.Scan(&marker); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func usageWindowedSessionFilters(alias string, options usageOptions, projectFilter string, projectRepoID string) (string, []any) {
	conditions := []string{}
	args := []any{}
	if options.Root != "all" && strings.TrimSpace(options.Root) != "" {
		conditions = append(conditions, fmt.Sprintf(` AND lower(%s.root) = lower(?)`, alias))
		args = append(args, strings.TrimSpace(options.Root))
	}
	if strings.TrimSpace(options.Activity) != "" {
		conditions = append(conditions, fmt.Sprintf(` AND lower(%s.activity) = lower(?)`, alias))
		args = append(args, strings.TrimSpace(options.Activity))
	}
	if strings.TrimSpace(options.Phase) != "" {
		conditions = append(conditions, fmt.Sprintf(` AND lower(%s.phase) = lower(?)`, alias))
		args = append(args, strings.TrimSpace(options.Phase))
	}
	if strings.TrimSpace(options.Model) != "" {
		conditions = append(conditions, fmt.Sprintf(` AND instr(lower(COALESCE(%s.model, '')), lower(?)) > 0`, alias))
		args = append(args, strings.TrimSpace(options.Model))
	}
	if strings.TrimSpace(options.Repo) != "" {
		conditions = append(conditions, ` AND lower(COALESCE(src.repo_slug, '')) = lower(?)`)
		args = append(args, strings.TrimSpace(options.Repo))
	}
	if strings.TrimSpace(projectFilter) != "" {
		projectNeedle := strings.ToLower(strings.TrimSpace(projectFilter))
		condition := fmt.Sprintf(` AND (
			instr(lower(COALESCE(%s.cwd, '')), ?) > 0
			OR instr(lower(COALESCE(%s.transcript_path, '')), ?) > 0`,
			alias,
			alias,
		)
		args = append(args, projectNeedle, projectNeedle)
		if strings.TrimSpace(projectRepoID) != "" {
			sandboxNeedle := strings.ToLower("/sandboxes/" + strings.TrimSpace(projectRepoID) + "/")
			condition += fmt.Sprintf(`
			OR instr(lower(replace(COALESCE(%s.cwd, ''), '\\', '/')), ?) > 0
			OR instr(lower(replace(COALESCE(%s.transcript_path, ''), '\\', '/')), ?) > 0`,
				alias,
				alias,
			)
			args = append(args, sandboxNeedle, sandboxNeedle)
		}
		condition += `)`
		conditions = append(conditions, condition)
	}
	return strings.Join(conditions, ""), args
}
