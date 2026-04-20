package gocli

import (
	"os"
	"strings"
	"testing"
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
