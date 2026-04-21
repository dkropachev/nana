package gocli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	usageTimeBasisCumulative  = "cumulative"
	usageTimeBasisWindowDelta = "window_delta"
	usageCoverageFull         = "full"
	usageCoveragePartial      = "partial"
)

type usageReportSource struct {
	Records             []usageRecord
	DayGroups           []usageGroupRow
	SessionRootsScanned int
	TimeBasis           string
	Coverage            string
}

func loadUsageReportSource(options usageOptions) (usageReportSource, error) {
	if strings.TrimSpace(options.Since) == "" {
		records, sessionRootsScanned, err := loadUsageRecordsShared(options)
		if err != nil {
			return usageReportSource{}, err
		}
		return usageReportSource{
			Records:             records,
			DayGroups:           buildUsageGroups(records, "day"),
			SessionRootsScanned: sessionRootsScanned,
			TimeBasis:           usageTimeBasisCumulative,
			Coverage:            usageCoverageFull,
		}, nil
	}
	return loadWindowedUsageReportSource(options)
}

func loadWindowedUsageReportSource(options usageOptions) (usageReportSource, error) {
	sessionRoots, err := discoverUsageSharedSessionRoots(options.CWD)
	if err != nil {
		return usageReportSource{}, err
	}
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

	records := []usageRecord{}
	dayIndex := map[string]*usageGroupRow{}

	for _, root := range sessionRoots {
		if options.Root != "all" && root.Name != options.Root {
			continue
		}
		err := walkRolloutFiles(root.SessionsDir, 0, func(path string) (bool, error) {
			base, err := loadUsageRollout(path, root.Name)
			if err != nil {
				return false, err
			}
			if !usageRecordMatchesFilters(base, options, projectFilter, projectRepoID) {
				return false, nil
			}
			historyRow, ok, err := readUsageHistoryRow(path)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
			record, dayRollups, ok := usageWindowRecordFromHistory(base, historyRow, sinceCutoff)
			if !ok {
				return false, nil
			}
			records = append(records, record)
			usageMergeDayRollups(dayIndex, dayRollups)
			return false, nil
		})
		if err != nil {
			return usageReportSource{}, err
		}
	}

	partialCoverage := false
	if options.Root == "all" || options.Root == "work" {
		updatedAfter := time.UnixMilli(sinceCutoff).UTC().Format(time.RFC3339)
		workRuns, err := usageListWorkRunIndexEntriesAfter(updatedAfter)
		if err == nil {
			for _, entry := range workRuns {
				runRecords, dayRollups, partial, runErr := usageWindowRecordsForWorkRun(entry, sinceCutoff, options, projectFilter, projectRepoID)
				if runErr != nil {
					if os.IsNotExist(runErr) {
						partialCoverage = true
						continue
					}
					return usageReportSource{}, runErr
				}
				records = append(records, runRecords...)
				usageMergeDayRollups(dayIndex, dayRollups)
				partialCoverage = partialCoverage || partial
			}
		}
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Timestamp == records[j].Timestamp {
			return records[i].SessionID < records[j].SessionID
		}
		return records[i].Timestamp > records[j].Timestamp
	})

	return usageReportSource{
		Records:             records,
		DayGroups:           usageDayGroupsFromIndex(dayIndex),
		SessionRootsScanned: len(sessionRoots),
		TimeBasis:           usageTimeBasisWindowDelta,
		Coverage:            usageCoverageValue(partialCoverage),
	}, nil
}

func usageWindowRecordsForWorkRun(entry workRunIndexEntry, sinceCutoff int64, options usageOptions, projectFilter string, projectRepoID string) ([]usageRecord, map[string]usageRollup, bool, error) {
	switch strings.TrimSpace(entry.Backend) {
	case "local":
		manifest, err := readLocalWorkManifestByRunID(entry.RunID)
		if err != nil {
			return nil, nil, true, err
		}
		return usageWindowRecordsForLocalWorkRun(manifest, sinceCutoff, options, projectFilter, projectRepoID)
	case "github":
		manifestPath, _, err := resolveGithubRunManifestPath(entry.RunID, false)
		if err != nil {
			return nil, nil, true, err
		}
		manifest := githubWorkManifest{}
		if err := readGithubJSON(manifestPath, &manifest); err != nil {
			return nil, nil, true, err
		}
		return usageWindowRecordsForGithubWorkRun(filepath.Dir(manifestPath), manifest, sinceCutoff, options, projectFilter, projectRepoID)
	default:
		return nil, nil, false, nil
	}
}

func usageWindowRecordsForLocalWorkRun(manifest localWorkManifest, sinceCutoff int64, options usageOptions, projectFilter string, projectRepoID string) ([]usageRecord, map[string]usageRollup, bool, error) {
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	rows, transcriptPath, partial, err := usageHistoryRowsForLocalWorkRun(manifest, runDir)
	if err != nil {
		return nil, nil, partial, err
	}
	records, dayRollups, _, err := usageWindowRecordsForHistoryRows(rows, transcriptPath, sinceCutoff, options, projectFilter, projectRepoID, func(row usageHistoryRow) usageRecord {
		record := usageRecord{
			SessionID:      defaultString(strings.TrimSpace(row.SessionID), usageAnonymousWorkSessionID(manifest.RunID, row.Role, row.StartedAt)),
			CWD:            defaultString(strings.TrimSpace(row.CWD), defaultString(strings.TrimSpace(manifest.SandboxRepoPath), strings.TrimSpace(manifest.RepoRoot))),
			TranscriptPath: transcriptPath,
			Root:           "work",
			Model:          strings.TrimSpace(row.Model),
			AgentRole:      strings.TrimSpace(row.Role),
			AgentNickname:  strings.TrimSpace(row.Nickname),
			Lane:           strings.TrimSpace(row.Role),
			Activity:       "work",
		}
		record.Phase = classifyUsagePhase(record)
		return record
	})
	return records, dayRollups, partial, err
}

func usageWindowRecordsForGithubWorkRun(runDir string, manifest githubWorkManifest, sinceCutoff int64, options usageOptions, projectFilter string, projectRepoID string) ([]usageRecord, map[string]usageRollup, bool, error) {
	rows, transcriptPath, partial, err := usageHistoryRowsForGithubWorkRun(runDir, manifest)
	if err != nil {
		return nil, nil, partial, err
	}
	records, dayRollups, _, err := usageWindowRecordsForHistoryRows(rows, transcriptPath, sinceCutoff, options, projectFilter, projectRepoID, func(row usageHistoryRow) usageRecord {
		record := usageRecord{
			SessionID:      defaultString(strings.TrimSpace(row.SessionID), usageAnonymousWorkSessionID(manifest.RunID, row.Role, row.StartedAt)),
			CWD:            defaultString(strings.TrimSpace(row.CWD), defaultString(strings.TrimSpace(manifest.SandboxRepoPath), strings.TrimSpace(manifest.ManagedRepoRoot))),
			TranscriptPath: transcriptPath,
			Root:           "work",
			Model:          strings.TrimSpace(row.Model),
			AgentRole:      strings.TrimSpace(row.Role),
			AgentNickname:  strings.TrimSpace(row.Nickname),
			Lane:           strings.TrimSpace(row.Role),
			Activity:       "work",
		}
		record.Phase = classifyUsagePhase(record)
		return record
	})
	return records, dayRollups, partial, err
}

func usageWindowRecordsForHistoryRows(rows []usageHistoryRow, transcriptPath string, sinceCutoff int64, options usageOptions, projectFilter string, projectRepoID string, buildBase func(usageHistoryRow) usageRecord) ([]usageRecord, map[string]usageRollup, bool, error) {
	records := []usageRecord{}
	dayIndex := map[string]usageRollup{}
	for _, row := range rows {
		base := buildBase(row)
		base.TranscriptPath = transcriptPath
		if !usageRecordMatchesFilters(base, options, projectFilter, projectRepoID) {
			continue
		}
		record, dayRollups, ok := usageWindowRecordFromHistory(base, row, sinceCutoff)
		if !ok {
			continue
		}
		records = append(records, record)
		for day, rollup := range dayRollups {
			merged := dayIndex[day]
			merged.Sessions += rollup.Sessions
			merged.InputTokens += rollup.InputTokens
			merged.CachedInputTokens += rollup.CachedInputTokens
			merged.OutputTokens += rollup.OutputTokens
			merged.ReasoningOutputTokens += rollup.ReasoningOutputTokens
			merged.TotalTokens += rollup.TotalTokens
			dayIndex[day] = merged
		}
	}
	return records, dayIndex, false, nil
}

func usageHistoryRowsForLocalWorkRun(manifest localWorkManifest, runDir string) ([]usageHistoryRow, string, bool, error) {
	historyPath := usageHistoryArtifactPath(runDir)
	if history, err := readLocalWorkThreadUsageHistoryArtifact(historyPath); err == nil && len(history.Threads) > 0 {
		return history.Threads, historyPath, false, nil
	} else if err != nil && !os.IsNotExist(err) {
		if historyMissingForUsedLocalRun(manifest, filepath.Join(runDir, "thread-usage.json")) {
			return nil, historyPath, true, nil
		}
		return nil, historyPath, false, nil
	}

	roots := usageLocalWorkHistoryRoots(manifest.SandboxPath)
	if len(roots) > 0 {
		rows, err := usageHistoryRowsFromRoots(roots)
		if err != nil {
			return nil, historyPath, false, err
		}
		if len(rows) > 0 {
			return rows, historyPath, false, nil
		}
	}

	threadPath := filepath.Join(runDir, "thread-usage.json")
	if historyMissingForUsedLocalRun(manifest, threadPath) {
		return nil, historyPath, true, nil
	}
	return nil, historyPath, false, nil
}

func usageHistoryRowsForGithubWorkRun(runDir string, manifest githubWorkManifest) ([]usageHistoryRow, string, bool, error) {
	historyPath := usageHistoryArtifactPath(runDir)
	if history, err := readGithubThreadUsageHistoryArtifact(historyPath); err == nil && len(history.Rows) > 0 {
		return history.Rows, historyPath, false, nil
	} else if err != nil && !os.IsNotExist(err) {
		if historyMissingForUsedGithubRun(filepath.Join(runDir, "thread-usage.json")) {
			return nil, historyPath, true, nil
		}
		return nil, historyPath, false, nil
	}

	rows, err := usageHistoryRowsFromRoots(githubThreadUsageRoots(manifest.SandboxPath))
	if err != nil {
		return nil, historyPath, false, err
	}
	if len(rows) > 0 {
		return rows, historyPath, false, nil
	}

	threadPath := filepath.Join(runDir, "thread-usage.json")
	if historyMissingForUsedGithubRun(threadPath) {
		return nil, historyPath, true, nil
	}
	return nil, historyPath, false, nil
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

func historyMissingForUsedLocalRun(manifest localWorkManifest, threadArtifactPath string) bool {
	if manifest.TokenUsage != nil && manifest.TokenUsage.TotalTokens > 0 {
		return true
	}
	artifact, err := readLocalWorkThreadUsageArtifact(threadArtifactPath)
	return err == nil && artifact != nil && artifact.Totals.TotalTokens > 0
}

func historyMissingForUsedGithubRun(threadArtifactPath string) bool {
	artifact, err := readGithubThreadUsageArtifact(threadArtifactPath)
	return err == nil && artifact != nil && artifact.TotalTokens > 0
}

func usageWindowRecordFromHistory(base usageRecord, row usageHistoryRow, sinceCutoff int64) (usageRecord, map[string]usageRollup, bool) {
	dayRollups := map[string]usageRollup{}
	lastCheckpoint := usageTokenCheckpoint{}
	latestTimestamp := int64(0)
	sinceCutoffUnix := time.UnixMilli(sinceCutoff).Unix()
	record := base
	record.SessionID = defaultString(strings.TrimSpace(record.SessionID), strings.TrimSpace(row.SessionID))
	record.CWD = defaultString(strings.TrimSpace(record.CWD), strings.TrimSpace(row.CWD))
	record.Model = defaultString(strings.TrimSpace(record.Model), strings.TrimSpace(row.Model))
	record.AgentRole = defaultString(strings.TrimSpace(record.AgentRole), strings.TrimSpace(row.Role))
	record.AgentNickname = defaultString(strings.TrimSpace(record.AgentNickname), strings.TrimSpace(row.Nickname))
	record.Lane = defaultString(strings.TrimSpace(record.Lane), strings.TrimSpace(row.Role))
	record.InputTokens = 0
	record.CachedInputTokens = 0
	record.OutputTokens = 0
	record.ReasoningOutputTokens = 0
	record.TotalTokens = 0
	record.HasTokenUsage = false
	if strings.TrimSpace(record.Activity) == "" {
		record.Activity = classifyUsageActivity(record.Root, record, usageScoutSignals{})
	}
	if strings.TrimSpace(record.Phase) == "" {
		record.Phase = classifyUsagePhase(record)
	}

	for _, checkpoint := range row.Checkpoints {
		if checkpoint.Timestamp < sinceCutoffUnix {
			lastCheckpoint = usageMaxCheckpoint(lastCheckpoint, checkpoint)
			continue
		}
		delta := usageCheckpointDelta(checkpoint, lastCheckpoint)
		lastCheckpoint = usageMaxCheckpoint(lastCheckpoint, checkpoint)
		if !usageCheckpointHasTokens(delta) {
			continue
		}
		latestTimestamp = checkpoint.Timestamp
		record.InputTokens += delta.InputTokens
		record.CachedInputTokens += delta.CachedInputTokens
		record.OutputTokens += delta.OutputTokens
		record.ReasoningOutputTokens += delta.ReasoningOutputTokens
		record.TotalTokens += delta.TotalTokens

		dayKey := time.Unix(checkpoint.Timestamp, 0).UTC().Format("2006-01-02")
		rollup := dayRollups[dayKey]
		if rollup.Sessions == 0 {
			rollup.Sessions = 1
		}
		rollup.InputTokens += delta.InputTokens
		rollup.CachedInputTokens += delta.CachedInputTokens
		rollup.OutputTokens += delta.OutputTokens
		rollup.ReasoningOutputTokens += delta.ReasoningOutputTokens
		rollup.TotalTokens += delta.TotalTokens
		dayRollups[dayKey] = rollup
	}

	if !record.HasTokenUsage && record.TotalTokens == 0 && record.InputTokens == 0 && record.CachedInputTokens == 0 && record.OutputTokens == 0 && record.ReasoningOutputTokens == 0 {
		return usageRecord{}, nil, false
	}
	record.HasTokenUsage = true
	record.Timestamp = usageHistoryTimestampString(latestTimestamp, row.StartedAt, record.Timestamp)
	record.Day = usageRecordDay(record.Timestamp, record.TranscriptPath)
	return record, dayRollups, true
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

func usageCheckpointHasTokens(checkpoint usageTokenCheckpoint) bool {
	return checkpoint.TotalTokens > 0 || checkpoint.InputTokens > 0 || checkpoint.CachedInputTokens > 0 || checkpoint.OutputTokens > 0 || checkpoint.ReasoningOutputTokens > 0
}

func usageMergeDayRollups(index map[string]*usageGroupRow, rollups map[string]usageRollup) {
	for day, rollup := range rollups {
		group := index[day]
		if group == nil {
			group = &usageGroupRow{Key: day}
			index[day] = group
		}
		group.Sessions += rollup.Sessions
		group.InputTokens += rollup.InputTokens
		group.CachedInputTokens += rollup.CachedInputTokens
		group.OutputTokens += rollup.OutputTokens
		group.ReasoningOutputTokens += rollup.ReasoningOutputTokens
		group.TotalTokens += rollup.TotalTokens
	}
}

func usageDayGroupsFromIndex(index map[string]*usageGroupRow) []usageGroupRow {
	groups := make([]usageGroupRow, 0, len(index))
	for _, group := range index {
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].TotalTokens == groups[j].TotalTokens {
			return groups[i].Key < groups[j].Key
		}
		return groups[i].TotalTokens > groups[j].TotalTokens
	})
	return groups
}

func usageCoverageValue(partial bool) string {
	if partial {
		return usageCoveragePartial
	}
	return usageCoverageFull
}
