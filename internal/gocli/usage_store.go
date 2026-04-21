package gocli

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	usageStoreSourceKindRollout      = "rollout"
	usageStoreSourceKindLocalThread  = "local-thread"
	usageStoreSourceKindGithubThread = "github-thread"
)

func loadUsageRecordsShared(options usageOptions) ([]usageRecord, int, error) {
	state, err := loadUsageSharedState(options.CWD)
	if err != nil {
		return nil, 0, err
	}
	return usageRecordsForState(state, options), state.SessionRootsScanned, nil
}

func loadUsageSharedState(cwd string) (startUIUsageIndexState, error) {
	path := startUIUsageIndexPath()
	existing := readStartUIUsageIndexState(path)
	refreshed, err := refreshUsageStore(cwd, path)
	if err != nil {
		if strings.TrimSpace(existing.Version) != "" || len(existing.Entries) > 0 {
			return existing, nil
		}
		return startUIUsageIndexState{}, err
	}
	return refreshed, nil
}

func refreshUsageStore(cwd string, path string) (startUIUsageIndexState, error) {
	state := readStartUIUsageIndexState(path)
	if state.SchemaVersion == 0 {
		state.SchemaVersion = startUIUsageIndexSchemaVersion
	}

	changed := false
	importedLegacy, err := usageStoreImportLegacyIndex(&state)
	if err != nil {
		return startUIUsageIndexState{}, err
	}
	changed = changed || importedLegacy

	syncedDirectRoots, err := usageStoreSyncDirectRoots(cwd, &state)
	if err != nil {
		return startUIUsageIndexState{}, err
	}
	changed = changed || syncedDirectRoots

	syncedWorkRuns, err := usageStoreSyncWorkRuns(&state)
	if err != nil {
		return startUIUsageIndexState{}, err
	}
	changed = changed || syncedWorkRuns

	if state.SchemaVersion != startUIUsageIndexSchemaVersion {
		state.SchemaVersion = startUIUsageIndexSchemaVersion
		changed = true
	}
	if strings.TrimSpace(state.Version) == "" {
		changed = true
	}
	if !changed {
		return state, nil
	}
	usageFinalizeStoreState(&state)
	if err := writeStartUIUsageIndexState(path, state); err != nil {
		return startUIUsageIndexState{}, err
	}
	return state, nil
}

func usageStoreImportLegacyIndex(state *startUIUsageIndexState) (bool, error) {
	if state == nil {
		return false, nil
	}
	if strings.TrimSpace(state.Version) != "" || len(state.Entries) > 0 {
		return false, nil
	}
	legacy := readStartUIUsageIndexState(legacyStartUIUsageIndexPath())
	if strings.TrimSpace(legacy.Version) == "" || len(legacy.Entries) == 0 {
		return false, nil
	}
	state.SchemaVersion = startUIUsageIndexSchemaVersion
	state.Version = legacy.Version
	state.UpdatedAt = legacy.UpdatedAt
	state.SessionRootsScanned = legacy.SessionRootsScanned
	state.WorkSyncUpdatedAt = strings.TrimSpace(legacy.UpdatedAt)
	state.LegacyImportedAt = time.Now().UTC().Format(time.RFC3339)
	state.LegacyImportedFrom = legacyStartUIUsageIndexPath()
	state.Entries = append([]startUIUsageIndexEntry(nil), legacy.Entries...)
	return true, nil
}

func usageStoreSyncDirectRoots(cwd string, state *startUIUsageIndexState) (bool, error) {
	if state == nil {
		return false, nil
	}
	roots, err := discoverUsageSharedSessionRoots(cwd)
	if err != nil {
		return false, err
	}
	changed := false
	if state.SessionRootsScanned == 0 {
		state.SessionRootsScanned = len(roots)
		changed = len(roots) > 0
	}

	entriesByPath := map[string]startUIUsageIndexEntry{}
	for _, entry := range state.Entries {
		entriesByPath[filepath.Clean(entry.Path)] = entry
	}
	seen := map[string]bool{}
	for _, root := range roots {
		err := walkRolloutFiles(root.SessionsDir, 0, func(path string) (bool, error) {
			info, err := os.Stat(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return false, nil
				}
				return false, err
			}
			cleanPath := filepath.Clean(path)
			seen[cleanPath] = true
			size := info.Size()
			modified := info.ModTime().UnixNano()
			if existing, ok := entriesByPath[cleanPath]; ok &&
				existing.Root == root.Name &&
				existing.Size == size &&
				existing.ModifiedUnixNano == modified &&
				(existing.SourceKind == "" || existing.SourceKind == usageStoreSourceKindRollout) {
				return false, nil
			}
			record, err := loadUsageRollout(cleanPath, root.Name)
			if err != nil {
				return false, err
			}
			entriesByPath[cleanPath] = startUIUsageIndexEntry{
				Path:             cleanPath,
				Root:             root.Name,
				Size:             size,
				ModifiedUnixNano: modified,
				SourceKind:       usageStoreSourceKindRollout,
				Record:           record,
			}
			changed = true
			return false, nil
		})
		if err != nil {
			return false, err
		}
	}

	for path, entry := range entriesByPath {
		if entry.SourceKind != usageStoreSourceKindRollout {
			continue
		}
		if !usagePathUnderAnyRoot(path, roots) {
			continue
		}
		if seen[path] {
			continue
		}
		delete(entriesByPath, path)
		changed = true
	}

	if !changed {
		return false, nil
	}
	state.Entries = usageEntriesFromMap(entriesByPath)
	return true, nil
}

func discoverUsageSharedSessionRoots(cwd string) ([]usageSessionRoot, error) {
	roots := []usageSessionRoot{}
	seen := map[string]bool{}
	addDirect := func(name string, sessionsDir string) {
		sessionsDir = filepath.Clean(strings.TrimSpace(sessionsDir))
		if sessionsDir == "" || seen[sessionsDir] {
			return
		}
		info, err := os.Lstat(sessionsDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return
		}
		seen[sessionsDir] = true
		roots = append(roots, usageSessionRoot{Name: name, SessionsDir: sessionsDir})
	}

	addDirect("main", filepath.Join(DefaultUserCodexHome(os.Getenv("HOME")), "sessions"))
	addDirect("main", filepath.Join(CodexHome(), "sessions"))
	addDirect("main", filepath.Join(ResolveCodexHomeForLaunch(cwd), "sessions"))
	addDirect("investigate", filepath.Join(DefaultUserInvestigateCodexHome(os.Getenv("HOME")), "sessions"))
	addDirect("investigate", filepath.Join(ResolveInvestigateCodexHome(cwd), "sessions"))

	for _, repoSlug := range usageSharedManagedRepoSlugs() {
		codexHomeRoot := filepath.Join(githubManagedPaths(repoSlug).SourcePath, ".nana", "start", "codex-home")
		entries, err := os.ReadDir(codexHomeRoot)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			addDirect("start", filepath.Join(codexHomeRoot, entry.Name(), "sessions"))
		}
	}

	probesRoot := filepath.Join(cwd, ".nana", "state", "investigate-probes")
	if err := discoverUsageSessionRootsRecursive(probesRoot, &roots, seen); err != nil {
		return nil, err
	}

	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Name == roots[j].Name {
			return roots[i].SessionsDir < roots[j].SessionsDir
		}
		return roots[i].Name < roots[j].Name
	})
	return roots, nil
}

func usageSharedManagedRepoSlugs() []string {
	repoSlugs, err := listStartUIRepoSlugs()
	if err != nil {
		return nil
	}
	return repoSlugs
}

func usagePathUnderAnyRoot(path string, roots []usageSessionRoot) bool {
	cleanPath := filepath.Clean(path)
	for _, root := range roots {
		base := filepath.Clean(root.SessionsDir)
		if cleanPath == base || strings.HasPrefix(cleanPath, base+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func usageEntriesFromMap(entriesByPath map[string]startUIUsageIndexEntry) []startUIUsageIndexEntry {
	entries := make([]startUIUsageIndexEntry, 0, len(entriesByPath))
	for _, entry := range entriesByPath {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries
}

func usageStoreSyncWorkRuns(state *startUIUsageIndexState) (bool, error) {
	if state == nil {
		return false, nil
	}
	rows, err := usageListWorkRunIndexEntriesAfter(strings.TrimSpace(state.WorkSyncUpdatedAt))
	if err != nil {
		return false, nil
	}
	changed := false
	latestUpdatedAt := strings.TrimSpace(state.WorkSyncUpdatedAt)
	for _, entry := range rows {
		if strings.TrimSpace(entry.UpdatedAt) != "" && strings.Compare(strings.TrimSpace(entry.UpdatedAt), latestUpdatedAt) > 0 {
			latestUpdatedAt = strings.TrimSpace(entry.UpdatedAt)
		}
		synced, err := usageStoreSyncWorkRun(state, entry)
		if err != nil {
			continue
		}
		changed = changed || synced
	}
	if latestUpdatedAt != strings.TrimSpace(state.WorkSyncUpdatedAt) {
		state.WorkSyncUpdatedAt = latestUpdatedAt
		changed = true
	}
	return changed, nil
}

func usageListWorkRunIndexEntriesAfter(updatedAfter string) ([]workRunIndexEntry, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) ([]workRunIndexEntry, error) {
		query := `SELECT run_id, backend, repo_key, repo_root, repo_name, repo_slug, manifest_path, updated_at, target_kind FROM work_run_index`
		args := []any{}
		if strings.TrimSpace(updatedAfter) != "" {
			query += ` WHERE updated_at > ?`
			args = append(args, strings.TrimSpace(updatedAfter))
		}
		query += ` ORDER BY updated_at ASC`
		rows, err := store.db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []workRunIndexEntry{}
		for rows.Next() {
			entry, err := scanWorkRunIndexEntry(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
		return out, rows.Err()
	})
}

func usageStoreSyncWorkRun(state *startUIUsageIndexState, entry workRunIndexEntry) (bool, error) {
	switch strings.TrimSpace(entry.Backend) {
	case "local":
		manifest, err := readLocalWorkManifestByRunID(entry.RunID)
		if err != nil {
			return false, err
		}
		entries, artifactPath, err := usageEntriesForLocalWorkRun(manifest)
		if err != nil {
			return false, err
		}
		return usageReplaceWorkEntries(state, manifest.SandboxPath, artifactPath, entries), nil
	case "github":
		manifestPath, _, err := resolveGithubRunManifestPath(entry.RunID, false)
		if err != nil {
			return false, err
		}
		manifest := githubWorkManifest{}
		if err := readGithubJSON(manifestPath, &manifest); err != nil {
			return false, err
		}
		entries, artifactPath, err := usageEntriesForGithubWorkRun(filepath.Dir(manifestPath), manifest)
		if err != nil {
			return false, err
		}
		return usageReplaceWorkEntries(state, manifest.SandboxPath, artifactPath, entries), nil
	default:
		return false, nil
	}
}

func usageReplaceWorkEntries(state *startUIUsageIndexState, sandboxPath string, artifactPath string, entries []startUIUsageIndexEntry) bool {
	if state == nil {
		return false
	}
	cleanSandbox := filepath.Clean(strings.TrimSpace(sandboxPath))
	artifactPrefix := strings.TrimSpace(artifactPath)
	next := make([]startUIUsageIndexEntry, 0, len(state.Entries)+len(entries))
	changed := false
	for _, entry := range state.Entries {
		if artifactPrefix != "" && strings.HasPrefix(strings.TrimSpace(entry.Path), artifactPrefix+"#") {
			changed = true
			continue
		}
		if cleanSandbox != "" && strings.Contains(filepath.Clean(strings.TrimSpace(entry.Record.TranscriptPath)), cleanSandbox) {
			changed = true
			continue
		}
		next = append(next, entry)
	}
	if len(entries) > 0 {
		next = append(next, entries...)
		changed = true
	}
	if !changed {
		return false
	}
	sort.Slice(next, func(i, j int) bool {
		return next[i].Path < next[j].Path
	})
	state.Entries = next
	return true
}

func usageEntriesForLocalWorkRun(manifest localWorkManifest) ([]startUIUsageIndexEntry, string, error) {
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	artifactPath := filepath.Join(runDir, "thread-usage.json")
	artifact, err := readLocalWorkThreadUsageArtifact(artifactPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, "", err
		}
		artifact = nil
	}
	if artifact != nil && len(artifact.Threads) > 0 {
		entries := make([]startUIUsageIndexEntry, 0, len(artifact.Threads))
		for _, row := range artifact.Threads {
			record := usageRecordFromLocalThreadRow(manifest, artifactPath, row)
			entries = append(entries, startUIUsageIndexEntry{
				Path:            usageWorkEntryKey(artifactPath, record.SessionID, record.Lane, row.StartedAt),
				Root:            record.Root,
				SourceKind:      usageStoreSourceKindLocalThread,
				SourceUpdatedAt: defaultString(strings.TrimSpace(manifest.UpdatedAt), strings.TrimSpace(artifact.GeneratedAt)),
				Record:          record,
			})
		}
		return entries, artifactPath, nil
	}
	if manifest.TokenUsage == nil || manifest.TokenUsage.TotalTokens == 0 {
		return nil, artifactPath, nil
	}
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
	return []startUIUsageIndexEntry{{
		Path:            usageWorkEntryKey(artifactPath, record.SessionID, record.Lane, 0),
		Root:            record.Root,
		SourceKind:      usageStoreSourceKindLocalThread,
		SourceUpdatedAt: strings.TrimSpace(manifest.UpdatedAt),
		Record:          record,
	}}, artifactPath, nil
}

func usageRecordFromLocalThreadRow(manifest localWorkManifest, artifactPath string, row localWorkThreadUsageRow) usageRecord {
	timestamp := usageUsageTimestamp(row.UpdatedAt, row.StartedAt, defaultString(strings.TrimSpace(manifest.UpdatedAt), strings.TrimSpace(manifest.CreatedAt)))
	record := usageRecord{
		SessionID:             defaultString(strings.TrimSpace(row.SessionID), usageAnonymousWorkSessionID(manifest.RunID, row.Role, row.StartedAt)),
		Timestamp:             timestamp,
		Day:                   usageRecordDay(timestamp, artifactPath),
		CWD:                   defaultString(strings.TrimSpace(row.CWD), defaultString(strings.TrimSpace(manifest.SandboxRepoPath), strings.TrimSpace(manifest.RepoRoot))),
		TranscriptPath:        artifactPath,
		Root:                  "work",
		Model:                 strings.TrimSpace(row.Model),
		AgentRole:             strings.TrimSpace(row.Role),
		AgentNickname:         strings.TrimSpace(row.Nickname),
		Lane:                  strings.TrimSpace(row.Role),
		Activity:              "work",
		InputTokens:           row.InputTokens,
		CachedInputTokens:     row.CachedInputTokens,
		OutputTokens:          row.OutputTokens,
		ReasoningOutputTokens: row.ReasoningOutputTokens,
		TotalTokens:           row.TotalTokens,
		HasTokenUsage:         row.TotalTokens > 0 || row.InputTokens > 0 || row.CachedInputTokens > 0 || row.OutputTokens > 0 || row.ReasoningOutputTokens > 0,
	}
	record.Phase = classifyUsagePhase(record)
	return record
}

func usageEntriesForGithubWorkRun(runDir string, manifest githubWorkManifest) ([]startUIUsageIndexEntry, string, error) {
	artifactPath := filepath.Join(runDir, "thread-usage.json")
	artifact, err := readGithubThreadUsageArtifact(artifactPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, "", err
		}
		return nil, artifactPath, nil
	}
	if artifact == nil || len(artifact.Rows) == 0 {
		return nil, artifactPath, nil
	}
	entries := make([]startUIUsageIndexEntry, 0, len(artifact.Rows))
	for _, row := range artifact.Rows {
		record := usageRecordFromGithubThreadRow(manifest, artifactPath, row)
		entries = append(entries, startUIUsageIndexEntry{
			Path:            usageWorkEntryKey(artifactPath, record.SessionID, record.Lane, row.StartedAt),
			Root:            record.Root,
			SourceKind:      usageStoreSourceKindGithubThread,
			SourceUpdatedAt: defaultString(strings.TrimSpace(manifest.UpdatedAt), strings.TrimSpace(artifact.GeneratedAt)),
			Record:          record,
		})
	}
	return entries, artifactPath, nil
}

func usageRecordFromGithubThreadRow(manifest githubWorkManifest, artifactPath string, row githubThreadUsageRow) usageRecord {
	timestamp := usageUsageTimestamp(row.UpdatedAt, row.StartedAt, strings.TrimSpace(manifest.UpdatedAt))
	record := usageRecord{
		SessionID:      defaultString(strings.TrimSpace(row.SessionID), usageAnonymousWorkSessionID(manifest.RunID, row.Role, row.StartedAt)),
		Timestamp:      timestamp,
		Day:            usageRecordDay(timestamp, artifactPath),
		CWD:            defaultString(strings.TrimSpace(row.CWD), defaultString(strings.TrimSpace(manifest.SandboxRepoPath), strings.TrimSpace(manifest.ManagedRepoRoot))),
		TranscriptPath: artifactPath,
		Root:           "work",
		Model:          strings.TrimSpace(row.Model),
		AgentRole:      strings.TrimSpace(row.Role),
		AgentNickname:  strings.TrimSpace(row.Nickname),
		Lane:           strings.TrimSpace(row.Role),
		Activity:       "work",
		TotalTokens:    row.TokensUsed,
		HasTokenUsage:  row.TokensUsed > 0,
	}
	record.Phase = classifyUsagePhase(record)
	return record
}

func usageWorkEntryKey(artifactPath string, sessionID string, lane string, startedAt int64) string {
	return strings.TrimSpace(artifactPath) + "#" + defaultString(strings.TrimSpace(sessionID), defaultString(strings.TrimSpace(lane), "(unknown)")+"-"+strconv.FormatInt(startedAt, 10))
}

func usageAnonymousWorkSessionID(runID string, role string, startedAt int64) string {
	base := defaultString(strings.TrimSpace(runID), "work")
	if strings.TrimSpace(role) != "" {
		base += "-" + strings.TrimSpace(role)
	}
	if startedAt > 0 {
		base += "-" + strconv.FormatInt(startedAt, 10)
	}
	return base
}

func usageUsageTimestamp(updatedAt int64, startedAt int64, fallback string) string {
	switch {
	case updatedAt > 0:
		return time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
	case startedAt > 0:
		return time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
	default:
		return strings.TrimSpace(fallback)
	}
}

func usageFinalizeStoreState(state *startUIUsageIndexState) {
	if state == nil {
		return
	}
	state.SchemaVersion = startUIUsageIndexSchemaVersion
	sort.Slice(state.Entries, func(i, j int) bool {
		return state.Entries[i].Path < state.Entries[j].Path
	})
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	state.Version = startUIUsageIndexVersion(state.Entries, state.SessionRootsScanned)
}

func usageRecordsForState(index startUIUsageIndexState, options usageOptions) []usageRecord {
	projectFilter := normalizeUsageProjectFilter(options.Project, options.CWD)
	projectRepoID := ""
	if projectFilter != "" {
		if info, err := os.Stat(projectFilter); err == nil && info.IsDir() {
			projectRepoID = localWorkRepoID(projectFilter)
		}
	}
	records := make([]usageRecord, 0, len(index.Entries))
	for _, entry := range index.Entries {
		record := entry.Record
		if !usageRecordMatchesFilters(record, options, projectFilter, projectRepoID) {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Timestamp == records[j].Timestamp {
			return records[i].SessionID < records[j].SessionID
		}
		return records[i].Timestamp > records[j].Timestamp
	})
	return records
}
