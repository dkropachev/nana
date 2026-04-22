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
