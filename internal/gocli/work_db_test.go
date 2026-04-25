package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenLocalWorkReadDBDoesNotCreateMissingStateDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := openLocalWorkReadDB()
	if err != nil {
		t.Fatalf("openLocalWorkReadDB: %v", err)
	}
	defer store.Close()

	if _, err := os.Stat(localWorkDBPath()); !os.IsNotExist(err) {
		t.Fatalf("expected read-only open to avoid creating %s, got err=%v", localWorkDBPath(), err)
	}
}

func TestWorkDBCheckAndRepairCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "db-check-1",
		Subject:    "Check DB repair flow",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE work_items SET pause_reason = NULL, pause_until = NULL, metadata_json = '{"pause_reason":"rate limited","pause_until":"2026-04-21T00:00:00Z"}' WHERE id = ?`, item.ID); err != nil {
		t.Fatalf("seed legacy metadata: %v", err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("downgrade schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	checkOutput, checkErr := captureStdout(t, func() error {
		return runWorkDBCheck(true)
	})
	if checkErr == nil {
		t.Fatal("expected db-check to fail while repair is required")
	}
	if !strings.Contains(checkOutput, `"repair_required": true`) {
		t.Fatalf("expected repair_required JSON output, got %q", checkOutput)
	}

	repairOutput, repairErr := captureStdout(t, func() error {
		return runWorkDBRepair(true)
	})
	if repairErr != nil {
		t.Fatalf("runWorkDBRepair: %v\n%s", repairErr, repairOutput)
	}
	if !strings.Contains(repairOutput, `"healthy": true`) {
		t.Fatalf("expected healthy repair JSON output, got %q", repairOutput)
	}

	report, err := inspectLocalWorkDB()
	if err != nil {
		t.Fatalf("inspectLocalWorkDB: %v", err)
	}
	if !report.Healthy || report.RepairRequired {
		t.Fatalf("expected repaired DB to be healthy, got %+v", report)
	}
}

func TestWorkRoutesDBCheckCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output, err := captureStdout(t, func() error {
		return Work(".", []string{"db-check", "--json"})
	})
	if err != nil {
		t.Fatalf("Work(db-check): %v\n%s", err, output)
	}
	if !strings.Contains(output, `"exists": false`) {
		t.Fatalf("expected missing DB JSON output, got %q", output)
	}
}

func TestWorkRoutesDBInspectCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output, err := captureStdout(t, func() error {
		return Work(".", []string{"db-inspect", "--json"})
	})
	if err != nil {
		t.Fatalf("Work(db-inspect): %v\n%s", err, output)
	}
	if !strings.Contains(output, `"exists": false`) {
		t.Fatalf("expected missing DB inspect JSON output, got %q", output)
	}
}

func TestWorkDBMaintainArchivesStaleUsageData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	now := time.Now().UTC()
	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home", "sessions"), usageRolloutFixture{
		SessionID:      "usage-stale",
		Timestamp:      now.Add(-40 * 24 * time.Hour).Format(time.RFC3339),
		CWD:            cwd,
		Model:          "gpt-5.4",
		TokenSnapshots: []usageTokenSnapshot{{Input: 100, Output: 20, Total: 120}},
	})
	writeUsageRollout(t, filepath.Join(home, ".nana", "codex-home", "sessions"), usageRolloutFixture{
		SessionID:      "usage-fresh",
		Timestamp:      now.Add(-2 * time.Hour).Format(time.RFC3339),
		CWD:            cwd,
		Model:          "gpt-5.4",
		TokenSnapshots: []usageTokenSnapshot{{Input: 90, Output: 10, Total: 100}},
	})

	resetUsageRolloutCache()
	defer resetUsageRolloutCache()

	if _, err := loadUsageSharedState(cwd); err != nil {
		t.Fatalf("loadUsageSharedState: %v", err)
	}

	inspectBefore, err := inspectLocalWorkDBDetailed()
	if err != nil {
		t.Fatalf("inspectLocalWorkDBDetailed(before): %v", err)
	}
	beforeSources := int64(0)
	for _, table := range inspectBefore.Tables {
		if table.Name == "usage_sources" {
			beforeSources = table.Rows
		}
	}
	if beforeSources < 2 {
		t.Fatalf("expected at least two usage sources before maintenance, got %+v", inspectBefore.Tables)
	}

	archiveDir := filepath.Join(home, "usage-archive")
	report, err := maintainLocalWorkDB(localWorkDBMaintainOptions{
		UsageRetentionDays: 30,
		ArchiveDir:         archiveDir,
	})
	if err != nil {
		t.Fatalf("maintainLocalWorkDB: %v", err)
	}
	if report.Archive.SourceRows == 0 || strings.TrimSpace(report.Archive.ArchivePath) == "" {
		t.Fatalf("expected stale usage archive to be created, got %+v", report.Archive)
	}
	if _, err := os.Stat(report.Archive.ArchivePath); err != nil {
		t.Fatalf("expected archive file at %s: %v", report.Archive.ArchivePath, err)
	}
	content, err := os.ReadFile(report.Archive.ArchivePath)
	if err != nil {
		t.Fatalf("read archive file: %v", err)
	}
	var archive localWorkDBUsageArchiveFile
	if err := json.Unmarshal(content, &archive); err != nil {
		t.Fatalf("decode archive file: %v", err)
	}
	if len(archive.Sources) != report.Archive.SourceRows || len(archive.Sessions) != report.Archive.SessionRows {
		t.Fatalf("expected archive counts to match report, archive=%+v report=%+v", archive, report.Archive)
	}
	for _, source := range archive.Sources {
		if source.SourceKey == "" {
			t.Fatalf("expected archived source keys, got %+v", archive.Sources)
		}
	}

	inspectAfter, err := inspectLocalWorkDBDetailed()
	if err != nil {
		t.Fatalf("inspectLocalWorkDBDetailed(after): %v", err)
	}
	afterSources := int64(0)
	for _, table := range inspectAfter.Tables {
		if table.Name == "usage_sources" {
			afterSources = table.Rows
		}
	}
	if afterSources >= beforeSources {
		t.Fatalf("expected fewer usage sources after archival, before=%d after=%d", beforeSources, afterSources)
	}
}

func TestWorkDBMaintainAdvancesStateOnlyDeletedLifecycleWithoutDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	oldStamp := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:                    6,
		RepoMode:                   "repo",
		IssuePickMode:              "auto",
		PRForwardMode:              "approve",
		DismissedItemRetentionDays: intPtr(7),
		DeletedItemRetentionDays:   intPtr(30),
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := writeGithubJSON(startWorkStatePath(repoSlug), startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  oldStamp,
		Issues:     map[string]startWorkIssueState{},
		ScoutJobs: map[string]startWorkScoutJob{
			"scout-1": {
				ID:          "scout-1",
				Role:        improvementScoutRole,
				Title:       "Dismissed scout",
				Summary:     "State-only lifecycle coverage",
				Destination: improvementDestinationLocal,
				TaskBody:    "Implement local scout proposal: Dismissed scout",
				Status:      startScoutJobDismissed,
				CreatedAt:   oldStamp,
				UpdatedAt:   oldStamp,
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	report, err := maintainLocalWorkDB(localWorkDBMaintainOptions{})
	if err != nil {
		t.Fatalf("maintainLocalWorkDB: %v", err)
	}
	if report.DismissedItems.ScoutItemsMarkedDeleted != 1 {
		t.Fatalf("expected state-only deleted lifecycle report, got %+v", report.DismissedItems)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("readStartWorkState: %v", err)
	}
	if state.ScoutJobs["scout-1"].Status != startScoutJobDeleted {
		t.Fatalf("expected dismissed scout job to advance to deleted, got %+v", state.ScoutJobs["scout-1"])
	}
}

func TestWorkDBMaintainAdvancesDeletedLifecycleForRepoAndRepoLessWorkItems(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:                    6,
		RepoMode:                   "repo",
		IssuePickMode:              "auto",
		PRForwardMode:              "approve",
		DismissedItemRetentionDays: intPtr(7),
		DeletedItemRetentionDays:   intPtr(30),
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldDropped, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "review_request",
		ExternalID: "maintain-old-dropped",
		RepoSlug:   repoSlug,
		Subject:    "Repo-scoped dropped item",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue old dropped item: %v", err)
	}
	oldDeleted, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "maintain-old-deleted",
		RepoSlug:   repoSlug,
		Subject:    "Repo-scoped deleted item",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue old deleted item: %v", err)
	}
	repoLessDropped, _, err := enqueueWorkItem(workItemInput{
		Source:     "slack",
		SourceKind: "task",
		ExternalID: "maintain-repoless-dropped",
		Subject:    "Repo-less dropped item",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue repo-less dropped item: %v", err)
	}

	oldStamp := time.Now().UTC().AddDate(0, 0, -8).Format(time.RFC3339)
	purgeStamp := time.Now().UTC().AddDate(0, 0, -31).Format(time.RFC3339)
	if err := withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		item, err := store.readWorkItem(oldDropped.ID)
		if err != nil {
			return err
		}
		item.Status = workItemStatusDropped
		item.Hidden = false
		item.UpdatedAt = oldStamp
		item.LatestActionAt = oldStamp
		if err := store.updateWorkItem(item); err != nil {
			return err
		}

		item, err = store.readWorkItem(oldDeleted.ID)
		if err != nil {
			return err
		}
		item.Status = workItemStatusDeleted
		item.Hidden = true
		item.HiddenReason = "deleted-retention-test"
		item.UpdatedAt = purgeStamp
		item.LatestActionAt = purgeStamp
		if err := store.updateWorkItem(item); err != nil {
			return err
		}

		item, err = store.readWorkItem(repoLessDropped.ID)
		if err != nil {
			return err
		}
		item.Status = workItemStatusDropped
		item.Hidden = false
		item.UpdatedAt = purgeStamp
		item.LatestActionAt = purgeStamp
		return store.updateWorkItem(item)
	}); err != nil {
		t.Fatalf("seed lifecycle work items: %v", err)
	}

	report, err := maintainLocalWorkDB(localWorkDBMaintainOptions{})
	if err != nil {
		t.Fatalf("maintainLocalWorkDB: %v", err)
	}
	if report.DismissedItems.WorkItemsMarkedDeleted != 1 || report.DismissedItems.WorkItemsPurged != 1 || report.DismissedItems.RepoLessWorkItemsMarkedDeleted != 1 {
		t.Fatalf("unexpected dismissed lifecycle report: %+v", report.DismissedItems)
	}

	detail, err := readWorkItemDetail(oldDropped.ID)
	if err != nil || detail.Item.Status != workItemStatusDeleted {
		t.Fatalf("expected repo-scoped dropped item to advance to deleted, got detail=%+v err=%v", detail, err)
	}
	if _, err := readWorkItemDetail(oldDeleted.ID); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("expected stale deleted item to be purged, got err=%v", err)
	}
	detail, err = readWorkItemDetail(repoLessDropped.ID)
	if err != nil || detail.Item.Status != workItemStatusDeleted {
		t.Fatalf("expected repo-less dropped item to advance to deleted, got detail=%+v err=%v", detail, err)
	}
}

func TestShowWorkItemCommandSurfacesRepairAction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "repair-read-command",
		Subject:    "Read command repair guidance",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("downgrade schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err = showWorkItemCommand(item.ID, false)
	if err == nil || !strings.Contains(err.Error(), "nana work db-repair") {
		t.Fatalf("expected repair guidance error, got %v", err)
	}
}

func TestInspectLocalWorkDBDetailedIncludesManagedPromptRecoverySummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	checkpointA := filepath.Join(home, "checkpoint-a.json")
	checkpointB := filepath.Join(home, "checkpoint-b.json")
	if err := os.WriteFile(checkpointA, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write checkpoint A: %v", err)
	}
	if err := os.WriteFile(checkpointB, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write checkpoint B: %v", err)
	}
	now := time.Now().UTC()
	if err := upsertManagedPromptRecovery(managedPromptRecoveryRecord{
		CheckpointPath: checkpointA,
		OwnerKind:      "investigate",
		OwnerID:        "inv-1",
		StepKey:        "investigator-round-1",
		Status:         managedPromptRecoveryStatusRunning,
		CWD:            home,
		ResumeArgv:     []string{"investigate", "--resume", "inv-1"},
		HeartbeatAt:    now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
		StartedAt:      now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
		UpdatedAt:      now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("upsert record A: %v", err)
	}
	if err := upsertManagedPromptRecovery(managedPromptRecoveryRecord{
		CheckpointPath: checkpointB,
		OwnerKind:      "work-item",
		OwnerID:        "wi-1",
		StepKey:        "work-item-reply",
		Status:         managedPromptRecoveryStatusPaused,
		CWD:            home,
		ResumeArgv:     []string{"work", "items", "run", "wi-1", "--attempt-dir", filepath.Join(home, "attempt-001")},
		HeartbeatAt:    now.Format(time.RFC3339Nano),
		StartedAt:      now.Add(-time.Minute).Format(time.RFC3339Nano),
		UpdatedAt:      now.Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("upsert record B: %v", err)
	}

	report, err := inspectLocalWorkDBDetailed()
	if err != nil {
		t.Fatalf("inspectLocalWorkDBDetailed: %v", err)
	}
	if report.ManagedPromptRecovery == nil {
		t.Fatalf("expected managed prompt recovery summary in inspect report")
	}
	if report.ManagedPromptRecovery.Total != 2 || report.ManagedPromptRecovery.ByStatus[managedPromptRecoveryStatusRunning] != 1 || report.ManagedPromptRecovery.ByStatus[managedPromptRecoveryStatusPaused] != 1 {
		t.Fatalf("unexpected recovery status summary: %+v", report.ManagedPromptRecovery)
	}
	if report.ManagedPromptRecovery.ByOwner["investigate"] != 1 || report.ManagedPromptRecovery.ByOwner["work-item"] != 1 {
		t.Fatalf("unexpected recovery owner summary: %+v", report.ManagedPromptRecovery)
	}
	if report.ManagedPromptRecovery.StaleCount != 1 {
		t.Fatalf("expected one stale recovery row, got %+v", report.ManagedPromptRecovery)
	}
}

func TestRepairLocalWorkDBRemovesOrphanManagedPromptRecoveryRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "managed-prompt-repair",
		Subject:    "Repair recovery rows",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	item.Status = workItemStatusDraftReady
	item.UpdatedAt = ISOTimeNow()
	item.LatestActionAt = item.UpdatedAt
	if _, err := withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
		return item, store.updateWorkItem(item)
	}); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}

	checkpointMissing := filepath.Join(home, "missing-checkpoint.json")
	checkpointTerminal := filepath.Join(home, "terminal-checkpoint.json")
	if err := os.WriteFile(checkpointTerminal, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write terminal checkpoint: %v", err)
	}
	if err := upsertManagedPromptRecovery(managedPromptRecoveryRecord{
		CheckpointPath: checkpointMissing,
		OwnerKind:      "investigate",
		OwnerID:        "inv-missing",
		StepKey:        "investigator-round-1",
		Status:         managedPromptRecoveryStatusRunning,
		CWD:            home,
		ResumeArgv:     []string{"investigate", "--resume", "inv-missing"},
		HeartbeatAt:    ISOTimeNow(),
		StartedAt:      ISOTimeNow(),
		UpdatedAt:      ISOTimeNow(),
	}); err != nil {
		t.Fatalf("upsert missing record: %v", err)
	}
	if err := upsertManagedPromptRecovery(managedPromptRecoveryRecord{
		CheckpointPath: checkpointTerminal,
		OwnerKind:      "work-item",
		OwnerID:        item.ID,
		StepKey:        "work-item-reply",
		Status:         managedPromptRecoveryStatusPaused,
		CWD:            home,
		ResumeArgv:     []string{"work", "items", "run", item.ID},
		HeartbeatAt:    ISOTimeNow(),
		StartedAt:      ISOTimeNow(),
		UpdatedAt:      ISOTimeNow(),
	}); err != nil {
		t.Fatalf("upsert terminal record: %v", err)
	}

	report, err := repairLocalWorkDB()
	if err != nil {
		t.Fatalf("repairLocalWorkDB: %v", err)
	}
	joined := strings.Join(report.Actions, "\n")
	if !strings.Contains(joined, "managed prompt recovery row") {
		t.Fatalf("expected repair actions to mention managed prompt cleanup, got %q", joined)
	}
	records, err := listManagedPromptRecoveryRecords()
	if err != nil {
		t.Fatalf("listManagedPromptRecoveryRecords: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected repaired DB to remove orphan recovery rows, got %+v", records)
	}
}
