package gocli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	defaultDismissedItemRetentionDays = 30
	defaultDeletedItemRetentionDays   = 30
	maxDismissedItemRetentionDays     = 3650
)

type dismissedItemLifecycleReport struct {
	ScoutItemsMarkedDeleted        int `json:"scout_items_marked_deleted,omitempty"`
	ScoutItemsPurged               int `json:"scout_items_purged,omitempty"`
	FindingsMarkedDeleted          int `json:"findings_marked_deleted,omitempty"`
	FindingsPurged                 int `json:"findings_purged,omitempty"`
	WorkItemsMarkedDeleted         int `json:"work_items_marked_deleted,omitempty"`
	WorkItemsPurged                int `json:"work_items_purged,omitempty"`
	RepoLessWorkItemsMarkedDeleted int `json:"repo_less_work_items_marked_deleted,omitempty"`
	RepoLessWorkItemsPurged        int `json:"repo_less_work_items_purged,omitempty"`
}

func (report *dismissedItemLifecycleReport) merge(other dismissedItemLifecycleReport) {
	report.ScoutItemsMarkedDeleted += other.ScoutItemsMarkedDeleted
	report.ScoutItemsPurged += other.ScoutItemsPurged
	report.FindingsMarkedDeleted += other.FindingsMarkedDeleted
	report.FindingsPurged += other.FindingsPurged
	report.WorkItemsMarkedDeleted += other.WorkItemsMarkedDeleted
	report.WorkItemsPurged += other.WorkItemsPurged
	report.RepoLessWorkItemsMarkedDeleted += other.RepoLessWorkItemsMarkedDeleted
	report.RepoLessWorkItemsPurged += other.RepoLessWorkItemsPurged
}

func (report dismissedItemLifecycleReport) total() int {
	return report.ScoutItemsMarkedDeleted +
		report.ScoutItemsPurged +
		report.FindingsMarkedDeleted +
		report.FindingsPurged +
		report.WorkItemsMarkedDeleted +
		report.WorkItemsPurged +
		report.RepoLessWorkItemsMarkedDeleted +
		report.RepoLessWorkItemsPurged
}

func (report dismissedItemLifecycleReport) actionMessages() []string {
	actions := []string{}
	appendAction := func(count int, text string) {
		if count <= 0 {
			return
		}
		actions = append(actions, fmt.Sprintf("%d %s", count, text))
	}
	appendAction(report.ScoutItemsMarkedDeleted, "scout item(s) marked deleted")
	appendAction(report.ScoutItemsPurged, "deleted scout item(s) purged")
	appendAction(report.FindingsMarkedDeleted, "finding(s) marked deleted")
	appendAction(report.FindingsPurged, "deleted finding(s) purged")
	appendAction(report.WorkItemsMarkedDeleted, "work item(s) marked deleted")
	appendAction(report.WorkItemsPurged, "deleted work item(s) purged")
	appendAction(report.RepoLessWorkItemsMarkedDeleted, "repo-less work item(s) marked deleted")
	appendAction(report.RepoLessWorkItemsPurged, "deleted repo-less work item(s) purged")
	return actions
}

func normalizeGithubDismissedItemRetentionDays(value int) (int, error) {
	switch {
	case value < 0:
		return 0, fmt.Errorf("dismissed_item_retention_days must be between 0 and %d", maxDismissedItemRetentionDays)
	case value > maxDismissedItemRetentionDays:
		return 0, fmt.Errorf("dismissed_item_retention_days must be between 0 and %d", maxDismissedItemRetentionDays)
	default:
		return value, nil
	}
}

func normalizeGithubDeletedItemRetentionDays(value int) (int, error) {
	switch {
	case value < 0:
		return 0, fmt.Errorf("deleted_item_retention_days must be between 0 and %d", maxDismissedItemRetentionDays)
	case value > maxDismissedItemRetentionDays:
		return 0, fmt.Errorf("deleted_item_retention_days must be between 0 and %d", maxDismissedItemRetentionDays)
	default:
		return value, nil
	}
}

func resolvedGithubDismissedItemRetentionDays(settings *githubRepoSettings) int {
	if settings == nil || settings.DismissedItemRetentionDays == nil {
		return defaultDismissedItemRetentionDays
	}
	switch {
	case *settings.DismissedItemRetentionDays < 0:
		return defaultDismissedItemRetentionDays
	case *settings.DismissedItemRetentionDays > maxDismissedItemRetentionDays:
		return maxDismissedItemRetentionDays
	default:
		return *settings.DismissedItemRetentionDays
	}
}

func resolvedGithubDeletedItemRetentionDays(settings *githubRepoSettings) int {
	if settings == nil || settings.DeletedItemRetentionDays == nil {
		return defaultDeletedItemRetentionDays
	}
	switch {
	case *settings.DeletedItemRetentionDays < 0:
		return defaultDeletedItemRetentionDays
	case *settings.DeletedItemRetentionDays > maxDismissedItemRetentionDays:
		return maxDismissedItemRetentionDays
	default:
		return *settings.DeletedItemRetentionDays
	}
}

func maintainDismissedItemLifecycleForRepo(repoSlug string, settings *githubRepoSettings, now time.Time) (dismissedItemLifecycleReport, error) {
	report := dismissedItemLifecycleReport{}
	state, err := readStartWorkState(repoSlug)
	switch {
	case err == nil:
		stateReport, updated := maintainDismissedStartWorkStateItems(state, settings, now)
		report.merge(stateReport)
		if updated {
			state.UpdatedAt = now.UTC().Format(time.RFC3339)
			if err := writeStartWorkState(*state); err != nil {
				return report, err
			}
		}
	case !os.IsNotExist(err):
		return report, err
	}

	pickupReport, err := maintainDismissedScoutPickupItems(repoSlug, settings, now)
	if err != nil {
		return report, err
	}
	report.merge(pickupReport)

	workItemsReport, err := maintainDismissedWorkItemsForRepo(repoSlug, settings, now)
	if err != nil {
		return report, err
	}
	report.merge(workItemsReport)
	return report, nil
}

func maintainDismissedItemLifecycleForAllRepos(now time.Time) (dismissedItemLifecycleReport, error) {
	report := dismissedItemLifecycleReport{}
	repos, err := listOnboardedGithubRepos()
	if err != nil {
		return report, err
	}
	sort.Strings(repos)
	for _, repoSlug := range repos {
		settings, readErr := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
		if readErr != nil && !os.IsNotExist(readErr) {
			return report, readErr
		}
		repoReport, repoErr := maintainDismissedItemLifecycleForRepo(repoSlug, settings, now)
		if repoErr != nil {
			return report, repoErr
		}
		report.merge(repoReport)
	}

	repoLessReport, err := maintainRepoLessDeletedWorkItems(now)
	if err != nil {
		return report, err
	}
	report.merge(repoLessReport)
	return report, nil
}

func maintainDismissedStartWorkStateItems(state *startWorkState, settings *githubRepoSettings, now time.Time) (dismissedItemLifecycleReport, bool) {
	report := dismissedItemLifecycleReport{}
	if state == nil {
		return report, false
	}
	updated := false
	nowValue := now.UTC().Format(time.RFC3339)
	dismissedCutoff, canMarkDeleted := retentionCutoff(now, resolvedGithubDismissedItemRetentionDays(settings))
	deletedCutoff, canPurgeDeleted := retentionCutoff(now, resolvedGithubDeletedItemRetentionDays(settings))

	for key, job := range state.ScoutJobs {
		switch strings.TrimSpace(job.Status) {
		case startScoutJobDismissed:
			if canMarkDeleted && retentionTimestampExpired(job.UpdatedAt, job.CreatedAt, dismissedCutoff) {
				job.DeletedFromStatus = strings.TrimSpace(job.Status)
				job.DeletedAt = nowValue
				job.Status = startScoutJobDeleted
				job.UpdatedAt = nowValue
				state.ScoutJobs[key] = job
				report.ScoutItemsMarkedDeleted++
				updated = true
			}
		case startScoutJobDeleted:
			if canPurgeDeleted && retentionTimestampExpired(job.UpdatedAt, job.CreatedAt, deletedCutoff) {
				delete(state.ScoutJobs, key)
				report.ScoutItemsPurged++
				updated = true
			}
		}
	}

	for key, finding := range state.Findings {
		switch strings.TrimSpace(finding.Status) {
		case startWorkFindingStatusDismissed:
			if canMarkDeleted && retentionTimestampExpired(finding.UpdatedAt, finding.CreatedAt, dismissedCutoff) {
				finding.DeletedFromStatus = strings.TrimSpace(finding.Status)
				finding.DeletedAt = nowValue
				finding.Status = startWorkFindingStatusDeleted
				finding.UpdatedAt = nowValue
				state.Findings[key] = finding
				report.FindingsMarkedDeleted++
				updated = true
			}
		case startWorkFindingStatusDeleted:
			if canPurgeDeleted && retentionTimestampExpired(finding.UpdatedAt, finding.CreatedAt, deletedCutoff) {
				delete(state.Findings, key)
				report.FindingsPurged++
				updated = true
			}
		}
	}

	return report, updated
}

func maintainDismissedScoutPickupItems(repoSlug string, settings *githubRepoSettings, now time.Time) (dismissedItemLifecycleReport, error) {
	report := dismissedItemLifecycleReport{}
	repoPath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
	if repoPath == "" {
		return report, nil
	}
	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		if err == nil || os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}

	dismissedCutoff, canMarkDeleted := retentionCutoff(now, resolvedGithubDismissedItemRetentionDays(settings))
	deletedCutoff, canPurgeDeleted := retentionCutoff(now, resolvedGithubDeletedItemRetentionDays(settings))
	nowValue := now.UTC().Format(time.RFC3339)

	err = withScoutPickupStateWriteLock(repoPath, func() error {
		state, path, err := readLocalScoutPickupState(repoPath)
		if err != nil {
			return err
		}
		updated := false
		for key, item := range state.Items {
			switch strings.TrimSpace(item.Status) {
			case startScoutJobDismissed:
				if canMarkDeleted && retentionTimestampExpired(item.UpdatedAt, "", dismissedCutoff) {
					item.DeletedFromStatus = strings.TrimSpace(item.Status)
					item.DeletedAt = nowValue
					item.Status = startScoutJobDeleted
					item.UpdatedAt = nowValue
					state.Items[key] = item
					report.ScoutItemsMarkedDeleted++
					updated = true
				}
			case startScoutJobDeleted:
				if canPurgeDeleted && retentionTimestampExpired(item.UpdatedAt, "", deletedCutoff) {
					delete(state.Items, key)
					report.ScoutItemsPurged++
					updated = true
				}
			}
		}
		if !updated {
			return nil
		}
		return writeLocalScoutPickupState(path, state)
	})
	return report, err
}

func maintainDismissedWorkItemsForRepo(repoSlug string, settings *githubRepoSettings, now time.Time) (dismissedItemLifecycleReport, error) {
	return maintainDismissedWorkItems(repoSlug, false, settings, now)
}

func maintainRepoLessDeletedWorkItems(now time.Time) (dismissedItemLifecycleReport, error) {
	return maintainDismissedWorkItems("", true, nil, now)
}

func maintainDismissedWorkItems(repoSlug string, repoLessOnly bool, settings *githubRepoSettings, now time.Time) (dismissedItemLifecycleReport, error) {
	report := dismissedItemLifecycleReport{}
	if _, err := os.Stat(localWorkDBPath()); err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}
	itemCounts, err := withLocalWorkWriteStore(func(store *localWorkDBStore) (dismissedItemLifecycleReport, error) {
		return store.maintainDismissedWorkItems(repoSlug, repoLessOnly, settings, now)
	})
	if err != nil {
		return report, err
	}
	report.merge(itemCounts)
	return report, nil
}

func (s *localWorkDBStore) maintainDismissedWorkItems(repoSlug string, repoLessOnly bool, settings *githubRepoSettings, now time.Time) (dismissedItemLifecycleReport, error) {
	report := dismissedItemLifecycleReport{}
	dismissedCutoff, canMarkDeleted := retentionCutoff(now, resolvedGithubDismissedItemRetentionDays(settings))
	deletedCutoff, canPurgeDeleted := retentionCutoff(now, resolvedGithubDeletedItemRetentionDays(settings))
	nowValue := now.UTC().Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()

	clauses := []string{"status IN (?, ?, ?)"}
	args := []any{workItemStatusDropped, workItemStatusSilenced, workItemStatusDeleted}
	switch {
	case repoLessOnly:
		clauses = append(clauses, `(repo_slug IS NULL OR trim(repo_slug) = '')`)
	case strings.TrimSpace(repoSlug) != "":
		clauses = append(clauses, "repo_slug = ?")
		args = append(args, strings.TrimSpace(repoSlug))
	}
	rows, err := tx.Query(
		`SELECT id, status, updated_at, created_at FROM work_items WHERE `+strings.Join(clauses, " AND "),
		args...,
	)
	if err != nil {
		return report, err
	}
	defer rows.Close()

	toMarkDeleted := []string{}
	toPurge := []string{}
	deletedTaskIDs := []string{}
	for rows.Next() {
		var itemID string
		var status string
		var updatedAt string
		var createdAt string
		if err := rows.Scan(&itemID, &status, &updatedAt, &createdAt); err != nil {
			return report, err
		}
		switch strings.TrimSpace(status) {
		case workItemStatusDropped, workItemStatusSilenced:
			if canMarkDeleted && retentionTimestampExpired(updatedAt, createdAt, dismissedCutoff) {
				toMarkDeleted = append(toMarkDeleted, itemID)
			}
		case workItemStatusDeleted:
			deletedTaskIDs = append(deletedTaskIDs, "work-item:"+itemID)
			if canPurgeDeleted && retentionTimestampExpired(updatedAt, createdAt, deletedCutoff) {
				toPurge = append(toPurge, itemID)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	if err := rows.Close(); err != nil {
		return report, err
	}

	taskIDsToDelete := []string{}
	for _, itemID := range toMarkDeleted {
		item, err := readWorkItemTx(tx, itemID)
		if err != nil {
			return report, err
		}
		item.Metadata = setWorkItemDeletedRestoreMetadata(item.Metadata, item, nowValue)
		item.Status = workItemStatusDeleted
		item.Hidden = true
		item.HiddenReason = defaultString(strings.TrimSpace(item.HiddenReason), "deleted_retention")
		item.UpdatedAt = nowValue
		item.LatestActionAt = nowValue
		if err := writeWorkItemTx(tx, item); err != nil {
			return report, err
		}
		if err := appendWorkItemEventTx(tx, item.ID, "deleted", "retention", map[string]any{"status": item.Status}); err != nil {
			return report, err
		}
		taskIDsToDelete = append(taskIDsToDelete, "work-item:"+item.ID)
		if repoLessOnly {
			report.RepoLessWorkItemsMarkedDeleted++
		} else {
			report.WorkItemsMarkedDeleted++
		}
	}
	if len(toPurge) > 0 {
		purgeArgs := make([]any, 0, len(toPurge))
		taskIDs := make([]string, 0, len(toPurge))
		for _, itemID := range toPurge {
			purgeArgs = append(purgeArgs, itemID)
			taskIDs = append(taskIDs, "work-item:"+itemID)
		}
		taskIDsToDelete = append(taskIDsToDelete, taskIDs...)
		if _, err := tx.Exec(`DELETE FROM work_items WHERE id IN (`+sqlPlaceholders(len(purgeArgs))+`)`, purgeArgs...); err != nil {
			return report, err
		}
		if repoLessOnly {
			report.RepoLessWorkItemsPurged += len(toPurge)
		} else {
			report.WorkItemsPurged += len(toPurge)
		}
	}
	taskIDsToDelete = append(taskIDsToDelete, deletedTaskIDs...)
	if len(taskIDsToDelete) > 0 {
		taskArgs := make([]any, 0, len(taskIDsToDelete))
		for _, taskID := range taskIDsToDelete {
			taskArgs = append(taskArgs, taskID)
		}
		if _, err := tx.Exec(`DELETE FROM tasks WHERE id IN (`+sqlPlaceholders(len(taskArgs))+`)`, taskArgs...); err != nil {
			return report, err
		}
	}

	if err := tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func retentionCutoff(now time.Time, retentionDays int) (time.Time, bool) {
	if retentionDays <= 0 {
		return time.Time{}, false
	}
	return now.UTC().AddDate(0, 0, -retentionDays), true
}

func retentionTimestampExpired(updatedAt string, createdAt string, cutoff time.Time) bool {
	if parsed, ok := parseManagedAuthTime(updatedAt); ok {
		return !parsed.After(cutoff)
	}
	if parsed, ok := parseManagedAuthTime(createdAt); ok {
		return !parsed.After(cutoff)
	}
	return false
}

func sqlPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
