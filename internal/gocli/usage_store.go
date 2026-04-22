package gocli

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func loadUsageRecordsShared(options usageOptions) ([]usageRecord, int, error) {
	state, err := loadUsageSharedState(options.CWD)
	if err != nil {
		return nil, 0, err
	}
	return usageRecordsForState(state, options), state.SessionRootsScanned, nil
}

func loadUsageSharedState(cwd string) (startUIUsageIndexState, error) {
	refreshed, err := refreshUsageStore(cwd, "")
	if err != nil {
		existing, readErr := loadUsageSQLiteState()
		if readErr == nil && (strings.TrimSpace(existing.Version) != "" || len(existing.Entries) > 0) {
			return existing, nil
		}
		return startUIUsageIndexState{}, err
	}
	return refreshed, nil
}

func refreshUsageStore(cwd string, path string) (startUIUsageIndexState, error) {
	return refreshUsageSQLiteStore(cwd, path)
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
