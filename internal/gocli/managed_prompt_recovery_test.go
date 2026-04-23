package gocli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRecoverStaleManagedPromptStepsLaunchesRecoveryCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Date(2026, 4, 23, 20, 0, 0, 0, time.UTC)
	oldNow := managedPromptRecoveryNow
	oldSnapshot := managedPromptRecoveryProcessSnapshot
	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	managedPromptRecoveryNow = func() time.Time { return now }
	managedPromptRecoveryProcessSnapshot = func() (string, error) { return "", nil }
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		managedPromptRecoveryNow = oldNow
		managedPromptRecoveryProcessSnapshot = oldSnapshot
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	capturedArgs := [][]string{}
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		capturedArgs = append(capturedArgs, append([]string{}, args...))
		return exec.Command("sh", "-c", "sleep 0.1"), nil
	}

	artifactRoot := filepath.Join(home, "artifacts")
	checkpointPath := filepath.Join(home, "checkpoint.json")
	if err := os.WriteFile(checkpointPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	record := managedPromptRecoveryRecord{
		CheckpointPath: checkpointPath,
		OwnerKind:      "test",
		OwnerID:        "test-owner",
		StepKey:        "investigator-round-1",
		Status:         managedPromptRecoveryStatusRunning,
		CWD:            home,
		ResumeArgv:     []string{"investigate", "--resume", "inv-1"},
		ArtifactRoot:   artifactRoot,
		LogPath:        filepath.Join(artifactRoot, "recovery.log"),
		OwnerPID:       424242,
		HeartbeatAt:    now.Add(-time.Minute).Format(time.RFC3339Nano),
		StartedAt:      now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
		UpdatedAt:      now.Add(-time.Minute).Format(time.RFC3339Nano),
	}
	if err := upsertManagedPromptRecovery(record); err != nil {
		t.Fatalf("upsertManagedPromptRecovery: %v", err)
	}

	if err := recoverStaleManagedPromptSteps(); err != nil {
		t.Fatalf("recoverStaleManagedPromptSteps: %v", err)
	}
	if !reflect.DeepEqual(capturedArgs, [][]string{{"investigate", "--resume", "inv-1"}}) {
		t.Fatalf("unexpected recovery command args: %+v", capturedArgs)
	}
	if _, err := os.Stat(filepath.Join(artifactRoot, "recovery.log")); err != nil {
		t.Fatalf("expected recovery log to be created: %v", err)
	}
	records, err := listManagedPromptRecoveryRecords()
	if err != nil {
		t.Fatalf("listManagedPromptRecoveryRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one recovery row, got %+v", records)
	}
	if records[0].Status != managedPromptRecoveryStatusRecovering || records[0].OwnerPID <= 0 {
		t.Fatalf("expected recovering row with child pid, got %+v", records[0])
	}
}
