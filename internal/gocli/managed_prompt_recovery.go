package gocli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	managedPromptRecoveryStatusRunning    = "running"
	managedPromptRecoveryStatusPaused     = "paused"
	managedPromptRecoveryStatusRecovering = "recovering"
)

type managedPromptRecoveryCleanupMode string

const (
	managedPromptRecoveryCleanupModeStart  managedPromptRecoveryCleanupMode = "start"
	managedPromptRecoveryCleanupModeRepair managedPromptRecoveryCleanupMode = "repair"
)

var (
	managedPromptRecoveryNow               = func() time.Time { return time.Now().UTC() }
	managedPromptRecoveryProcessSnapshot   = captureLocalWorkProcessSnapshot
	managedPromptRecoveryHeartbeatInterval = 10 * time.Second
	managedPromptRecoverySessionPoll       = time.Second
	managedPromptRecoveryStaleThreshold    = 2 * time.Minute
)

type codexManagedPromptRecoverySpec struct {
	OwnerKind    string
	OwnerID      string
	OwnerPayload map[string]any
	ArtifactRoot string
	LogPath      string
	CWD          string
	ResumeArgv   []string
}

type managedPromptRecoveryRecord struct {
	CheckpointPath    string
	OwnerKind         string
	OwnerID           string
	OwnerPayload      map[string]any
	StepKey           string
	Status            string
	CWD               string
	ResumeArgv        []string
	ArtifactRoot      string
	LogPath           string
	PromptFingerprint string
	SessionID         string
	SessionPath       string
	LastLaunchMode    string
	PauseReason       string
	PauseUntil        string
	LastError         string
	OwnerPID          int
	HeartbeatAt       string
	StartedAt         string
	UpdatedAt         string
}

func normalizeManagedPromptRecoverySpec(spec codexManagedPromptRecoverySpec, checkpointPath string, commandDir string) (codexManagedPromptRecoverySpec, bool) {
	if strings.TrimSpace(checkpointPath) == "" ||
		strings.TrimSpace(spec.OwnerKind) == "" ||
		strings.TrimSpace(spec.OwnerID) == "" ||
		len(spec.ResumeArgv) == 0 {
		return codexManagedPromptRecoverySpec{}, false
	}
	out := codexManagedPromptRecoverySpec{
		OwnerKind:    strings.TrimSpace(spec.OwnerKind),
		OwnerID:      strings.TrimSpace(spec.OwnerID),
		OwnerPayload: cloneManagedPromptRecoveryPayload(spec.OwnerPayload),
		ArtifactRoot: strings.TrimSpace(spec.ArtifactRoot),
		LogPath:      strings.TrimSpace(spec.LogPath),
		CWD:          strings.TrimSpace(spec.CWD),
		ResumeArgv:   append([]string{}, spec.ResumeArgv...),
	}
	if out.CWD == "" {
		out.CWD = strings.TrimSpace(commandDir)
	}
	if out.LogPath == "" {
		out.LogPath = managedPromptRecoveryDefaultLogPath(out.ArtifactRoot, checkpointPath)
	}
	if out.ArtifactRoot == "" && strings.TrimSpace(out.LogPath) != "" {
		out.ArtifactRoot = filepath.Dir(out.LogPath)
	}
	return out, true
}

func managedPromptResumeArgv(base []string, codexArgs []string) []string {
	out := append([]string{}, base...)
	if len(codexArgs) > 0 {
		out = append(out, "--")
		out = append(out, codexArgs...)
	}
	return out
}

func cloneManagedPromptRecoveryPayload(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	content, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(content, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func githubWorkManagedPromptRecoverySpec(manifest githubWorkManifest, runDir string, resumeArgv []string) codexManagedPromptRecoverySpec {
	return codexManagedPromptRecoverySpec{
		OwnerKind:    "github-work",
		OwnerID:      strings.TrimSpace(manifest.RunID),
		ArtifactRoot: strings.TrimSpace(runDir),
		LogPath:      filepath.Join(runDir, "runtime.log"),
		CWD:          defaultString(strings.TrimSpace(manifest.SandboxPath), strings.TrimSpace(manifest.ManagedRepoRoot)),
		ResumeArgv:   append([]string{}, resumeArgv...),
	}
}

func startTriageRecoveryOwnerID(repoSlug string, issueKey string) string {
	return strings.TrimSpace(repoSlug) + ":" + strings.TrimSpace(issueKey)
}

func startTriageManagedPromptRecoverySpec(repoSlug string, issueKey string, issue startWorkIssueState, codexArgs []string) codexManagedPromptRecoverySpec {
	checkpointDir := filepath.Join(githubManagedPaths(repoSlug).RepoRoot, ".nana", "start", "triage-checkpoints")
	return codexManagedPromptRecoverySpec{
		OwnerKind: "start-triage",
		OwnerID:   startTriageRecoveryOwnerID(repoSlug, issueKey),
		OwnerPayload: map[string]any{
			"repo_slug":    strings.TrimSpace(repoSlug),
			"issue_key":    strings.TrimSpace(issueKey),
			"issue_number": issue.SourceNumber,
		},
		ArtifactRoot: checkpointDir,
		LogPath:      filepath.Join(checkpointDir, sanitizePathToken(issueKey)+"-recovery.log"),
		CWD:          githubManagedPaths(repoSlug).SourcePath,
		ResumeArgv:   managedPromptResumeArgv([]string{"start", "__recover-triage", "--repo-slug", repoSlug, "--issue-key", issueKey}, codexArgs),
	}
}

func investigateManagedPromptRecoverySpec(manifest investigateManifest, codexArgs []string) codexManagedPromptRecoverySpec {
	return codexManagedPromptRecoverySpec{
		OwnerKind:    "investigate",
		OwnerID:      strings.TrimSpace(manifest.RunID),
		ArtifactRoot: strings.TrimSpace(manifest.RunDir),
		LogPath:      filepath.Join(manifest.RunDir, "recovery.log"),
		CWD:          strings.TrimSpace(manifest.WorkspaceRoot),
		ResumeArgv:   managedPromptResumeArgv([]string{"investigate", "--resume", manifest.RunID}, codexArgs),
	}
}

func scoutManagedPromptRecoverySpec(runtime scoutExecutionRuntime) codexManagedPromptRecoverySpec {
	return codexManagedPromptRecoverySpec{
		OwnerKind:    "scout",
		OwnerID:      strings.TrimSpace(runtime.RunID),
		ArtifactRoot: strings.TrimSpace(runtime.StateDir),
		LogPath:      filepath.Join(runtime.StateDir, "recovery.log"),
		CWD:          strings.TrimSpace(runtime.RepoPath),
		ResumeArgv:   append([]string{}, runtime.RecoveryArgv...),
	}
}

func workItemManagedPromptRecoverySpec(itemID string, attemptDir string, cwd string, codexArgs []string) codexManagedPromptRecoverySpec {
	return codexManagedPromptRecoverySpec{
		OwnerKind:    "work-item",
		OwnerID:      strings.TrimSpace(itemID),
		ArtifactRoot: strings.TrimSpace(attemptDir),
		LogPath:      filepath.Join(attemptDir, "recovery.log"),
		CWD:          strings.TrimSpace(cwd),
		ResumeArgv:   managedPromptResumeArgv([]string{"work", "items", "run", itemID, "--attempt-dir", attemptDir}, codexArgs),
	}
}

func managedPromptRecoveryDefaultLogPath(artifactRoot string, checkpointPath string) string {
	switch {
	case strings.TrimSpace(artifactRoot) != "":
		return filepath.Join(strings.TrimSpace(artifactRoot), "recovery.log")
	case strings.TrimSpace(checkpointPath) != "":
		return filepath.Join(filepath.Dir(strings.TrimSpace(checkpointPath)), "recovery.log")
	default:
		return filepath.Join(workHomeRoot(), "recovery.log")
	}
}

func (s *localWorkDBStore) upsertManagedPromptRecovery(record managedPromptRecoveryRecord) error {
	if strings.TrimSpace(record.CheckpointPath) == "" {
		return nil
	}
	payloadJSON, err := marshalNullableJSON(record.OwnerPayload)
	if err != nil {
		return err
	}
	resumeArgvJSON, err := json.Marshal(record.ResumeArgv)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO managed_prompt_recovery(
			checkpoint_path, owner_kind, owner_id, owner_payload_json, step_key, status, cwd, resume_argv_json,
			artifact_root, log_path, prompt_fingerprint, session_id, session_path, last_launch_mode,
			pause_reason, pause_until, last_error, owner_pid, heartbeat_at, started_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(checkpoint_path) DO UPDATE SET
			owner_kind=excluded.owner_kind,
			owner_id=excluded.owner_id,
			owner_payload_json=excluded.owner_payload_json,
			step_key=excluded.step_key,
			status=excluded.status,
			cwd=excluded.cwd,
			resume_argv_json=excluded.resume_argv_json,
			artifact_root=excluded.artifact_root,
			log_path=excluded.log_path,
			prompt_fingerprint=excluded.prompt_fingerprint,
			session_id=excluded.session_id,
			session_path=excluded.session_path,
			last_launch_mode=excluded.last_launch_mode,
			pause_reason=excluded.pause_reason,
			pause_until=excluded.pause_until,
			last_error=excluded.last_error,
			owner_pid=excluded.owner_pid,
			heartbeat_at=excluded.heartbeat_at,
			started_at=excluded.started_at,
			updated_at=excluded.updated_at`,
		record.CheckpointPath,
		record.OwnerKind,
		record.OwnerID,
		nullableString(payloadJSON),
		record.StepKey,
		record.Status,
		record.CWD,
		string(resumeArgvJSON),
		nullableString(record.ArtifactRoot),
		nullableString(record.LogPath),
		nullableString(record.PromptFingerprint),
		nullableString(record.SessionID),
		nullableString(record.SessionPath),
		nullableString(record.LastLaunchMode),
		nullableString(record.PauseReason),
		nullableString(record.PauseUntil),
		nullableString(record.LastError),
		record.OwnerPID,
		record.HeartbeatAt,
		record.StartedAt,
		record.UpdatedAt,
	)
	return err
}

func (s *localWorkDBStore) deleteManagedPromptRecovery(checkpointPath string) error {
	if strings.TrimSpace(checkpointPath) == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM managed_prompt_recovery WHERE checkpoint_path = ?`, checkpointPath)
	if err != nil && isMissingLocalWorkRepairTableError(err) {
		return nil
	}
	return err
}

func (s *localWorkDBStore) listManagedPromptRecoveryRecords() ([]managedPromptRecoveryRecord, error) {
	rows, err := s.db.Query(
		`SELECT checkpoint_path, owner_kind, owner_id, COALESCE(owner_payload_json, ''), step_key, status, cwd, resume_argv_json,
		        COALESCE(artifact_root, ''), COALESCE(log_path, ''), COALESCE(prompt_fingerprint, ''),
		        COALESCE(session_id, ''), COALESCE(session_path, ''), COALESCE(last_launch_mode, ''),
		        COALESCE(pause_reason, ''), COALESCE(pause_until, ''), COALESCE(last_error, ''),
		        owner_pid, heartbeat_at, started_at, updated_at
		   FROM managed_prompt_recovery
		  ORDER BY updated_at DESC`,
	)
	if err != nil {
		if isMissingLocalWorkRepairTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	records := []managedPromptRecoveryRecord{}
	for rows.Next() {
		var record managedPromptRecoveryRecord
		var ownerPayloadJSON string
		var resumeArgvJSON string
		if err := rows.Scan(
			&record.CheckpointPath,
			&record.OwnerKind,
			&record.OwnerID,
			&ownerPayloadJSON,
			&record.StepKey,
			&record.Status,
			&record.CWD,
			&resumeArgvJSON,
			&record.ArtifactRoot,
			&record.LogPath,
			&record.PromptFingerprint,
			&record.SessionID,
			&record.SessionPath,
			&record.LastLaunchMode,
			&record.PauseReason,
			&record.PauseUntil,
			&record.LastError,
			&record.OwnerPID,
			&record.HeartbeatAt,
			&record.StartedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if strings.TrimSpace(resumeArgvJSON) != "" {
			if err := json.Unmarshal([]byte(resumeArgvJSON), &record.ResumeArgv); err != nil {
				return nil, err
			}
		}
		if strings.TrimSpace(ownerPayloadJSON) != "" {
			record.OwnerPayload = map[string]any{}
			if err := json.Unmarshal([]byte(ownerPayloadJSON), &record.OwnerPayload); err != nil {
				return nil, err
			}
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func upsertManagedPromptRecovery(record managedPromptRecoveryRecord) error {
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.upsertManagedPromptRecovery(record)
	})
}

func deleteManagedPromptRecovery(checkpointPath string) error {
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.deleteManagedPromptRecovery(checkpointPath)
	})
}

func listManagedPromptRecoveryRecords() ([]managedPromptRecoveryRecord, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) ([]managedPromptRecoveryRecord, error) {
		return store.listManagedPromptRecoveryRecords()
	})
}

func managedPromptRecoveryHeartbeatAt(record managedPromptRecoveryRecord) time.Time {
	for _, candidate := range []string{record.HeartbeatAt, record.UpdatedAt, record.StartedAt} {
		if parsed, ok := parseLocalWorkManifestTime(candidate); ok {
			return parsed
		}
	}
	return time.Time{}
}

func managedPromptRecoverySnapshotHasPID(snapshot string, pid int) bool {
	if pid <= 0 {
		return false
	}
	target := strconv.Itoa(pid)
	for _, line := range strings.Split(snapshot, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		if fields[0] == target {
			return true
		}
	}
	return false
}

func managedPromptRecoveryShouldResume(record managedPromptRecoveryRecord, snapshot string, now time.Time) bool {
	if len(record.ResumeArgv) == 0 || strings.TrimSpace(record.CWD) == "" {
		return false
	}
	if managedPromptRecoverySnapshotHasPID(snapshot, record.OwnerPID) {
		return false
	}
	switch strings.TrimSpace(record.Status) {
	case managedPromptRecoveryStatusPaused:
		if retryAt, ok := parseLocalWorkManifestTime(record.PauseUntil); ok && retryAt.After(now) {
			return false
		}
		return true
	case managedPromptRecoveryStatusRunning, managedPromptRecoveryStatusRecovering:
		if record.OwnerPID > 0 {
			return true
		}
		lastSeen := managedPromptRecoveryHeartbeatAt(record)
		return !lastSeen.IsZero() && now.Sub(lastSeen) >= managedPromptRecoveryStaleThreshold
	default:
		return false
	}
}

func recoverStaleManagedPromptSteps() error {
	cleanupActions, err := cleanupManagedPromptRecoveryRows(managedPromptRecoveryCleanupModeStart)
	if err != nil {
		return err
	}
	for _, action := range cleanupActions {
		fmt.Fprintf(os.Stdout, "[start] %s\n", action)
	}
	snapshot, err := managedPromptRecoveryProcessSnapshot()
	if err != nil {
		return err
	}
	records, err := listManagedPromptRecoveryRecords()
	if err != nil {
		return err
	}
	now := managedPromptRecoveryNow()
	for _, record := range records {
		if !managedPromptRecoveryShouldResume(record, snapshot, now) {
			continue
		}
		if err := launchManagedPromptRecovery(record, now); err != nil {
			fmt.Fprintf(os.Stdout, "[start] managed prompt recovery skipped for %s %s (%s): %v\n",
				defaultString(strings.TrimSpace(record.OwnerKind), "unknown"),
				defaultString(strings.TrimSpace(record.OwnerID), "unknown"),
				defaultString(strings.TrimSpace(record.StepKey), "step"),
				err,
			)
		}
	}
	return nil
}

func cleanupManagedPromptRecoveryRows(mode managedPromptRecoveryCleanupMode) ([]string, error) {
	return withLocalWorkWriteStore(func(store *localWorkDBStore) ([]string, error) {
		return cleanupManagedPromptRecoveryRowsDB(store.db, mode)
	})
}

func cleanupManagedPromptRecoveryRowsDB(db *sql.DB, mode managedPromptRecoveryCleanupMode) ([]string, error) {
	store := &localWorkDBStore{db: db}
	records, err := store.listManagedPromptRecoveryRecords()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	actions := []string{}
	for _, record := range records {
		reason, remove, err := managedPromptRecoveryRemovalReason(db, record)
		if err != nil {
			return actions, err
		}
		if !remove {
			continue
		}
		if err := store.deleteManagedPromptRecovery(record.CheckpointPath); err != nil {
			return actions, err
		}
		switch mode {
		case managedPromptRecoveryCleanupModeRepair:
			actions = append(actions, fmt.Sprintf("removed managed prompt recovery row for %s %s (%s)", defaultString(strings.TrimSpace(record.OwnerKind), "unknown"), defaultString(strings.TrimSpace(record.OwnerID), "unknown"), reason))
		default:
			actions = append(actions, fmt.Sprintf("cleaned managed prompt recovery row for %s %s (%s)", defaultString(strings.TrimSpace(record.OwnerKind), "unknown"), defaultString(strings.TrimSpace(record.OwnerID), "unknown"), reason))
		}
	}
	return actions, nil
}

func managedPromptRecoveryRemovalReason(db *sql.DB, record managedPromptRecoveryRecord) (string, bool, error) {
	if strings.TrimSpace(record.CheckpointPath) == "" {
		return "missing checkpoint path", true, nil
	}
	if _, err := os.Stat(record.CheckpointPath); err != nil {
		if os.IsNotExist(err) {
			return "missing checkpoint file", true, nil
		}
		return "", false, err
	}
	if len(record.ResumeArgv) == 0 {
		return "empty resume argv", true, nil
	}
	terminal, reason, err := managedPromptRecoveryOwnerTerminal(db, record)
	if err != nil {
		return "", false, err
	}
	if terminal {
		return reason, true, nil
	}
	return "", false, nil
}

func managedPromptRecoveryOwnerTerminal(db *sql.DB, record managedPromptRecoveryRecord) (bool, string, error) {
	switch strings.TrimSpace(record.OwnerKind) {
	case "github-work":
		manifestPath := filepath.Join(strings.TrimSpace(record.ArtifactRoot), "manifest.json")
		var manifest githubWorkManifest
		if err := readGithubJSON(manifestPath, &manifest); err != nil {
			if os.IsNotExist(err) {
				return true, "missing github work manifest", nil
			}
			return false, "", err
		}
		switch strings.TrimSpace(manifest.ExecutionStatus) {
		case "completed", "failed", "blocked":
			return true, "github work is terminal", nil
		}
	case "investigate":
		manifestPath := filepath.Join(strings.TrimSpace(record.ArtifactRoot), "manifest.json")
		var manifest investigateManifest
		if err := readGithubJSON(manifestPath, &manifest); err != nil {
			if os.IsNotExist(err) {
				return true, "missing investigate manifest", nil
			}
			return false, "", err
		}
		switch strings.TrimSpace(manifest.Status) {
		case investigateRunStatusCompleted, investigateRunStatusFailedExecutor, investigateRunStatusFailedReportParse, investigateRunStatusFailedReadiness, investigateRunStatusFailedValidatorExhausted, investigateRunStatusFailedValidatorParse:
			return true, "investigation is terminal", nil
		}
	case "scout":
		manifestPath := scoutRunManifestPath(strings.TrimSpace(record.ArtifactRoot))
		var manifest scoutRunManifest
		if err := readGithubJSON(manifestPath, &manifest); err != nil {
			if os.IsNotExist(err) {
				return true, "missing scout manifest", nil
			}
			return false, "", err
		}
		switch strings.TrimSpace(manifest.Status) {
		case "completed", "failed":
			return true, "scout run is terminal", nil
		}
	case "work-item":
		row := db.QueryRow(`SELECT status FROM work_items WHERE id = ?`, strings.TrimSpace(record.OwnerID))
		var status string
		if err := row.Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return true, "missing work item", nil
			}
			return false, "", err
		}
		switch strings.TrimSpace(status) {
		case workItemStatusDraftReady, workItemStatusSubmitted, workItemStatusFailed, workItemStatusDropped, workItemStatusSilenced, workItemStatusNeedsRouting:
			return true, "work item is terminal", nil
		}
	case "start-triage":
		repoSlug := metadataString(record.OwnerPayload, "repo_slug")
		issueKey := metadataString(record.OwnerPayload, "issue_key")
		if repoSlug == "" || issueKey == "" {
			return true, "missing triage recovery payload", nil
		}
		state, err := readStartWorkState(repoSlug)
		if err != nil {
			if os.IsNotExist(err) {
				return true, "missing start state", nil
			}
			return false, "", err
		}
		issue, ok := state.Issues[issueKey]
		if !ok {
			return true, "missing triage issue", nil
		}
		task, ok := state.ServiceTasks[startServiceTaskKey(startTaskKindTriage, issueKey)]
		if !ok {
			return true, "missing triage service task", nil
		}
		switch strings.TrimSpace(task.Status) {
		case startWorkServiceTaskCompleted, startWorkServiceTaskFailed:
			return true, "triage service task is terminal", nil
		}
		switch strings.TrimSpace(issue.TriageStatus) {
		case startWorkTriageCompleted, startWorkTriageFailed:
			return true, "triage issue is terminal", nil
		}
	}
	return false, "", nil
}

func inspectManagedPromptRecoveryRows(db *sql.DB) (*managedPromptRecoveryInspectSummary, error) {
	store := &localWorkDBStore{db: db}
	records, err := store.listManagedPromptRecoveryRecords()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return &managedPromptRecoveryInspectSummary{ByOwner: map[string]int{}, ByStatus: map[string]int{}}, nil
	}
	summary := &managedPromptRecoveryInspectSummary{
		Total:    len(records),
		ByStatus: map[string]int{},
		ByOwner:  map[string]int{},
	}
	now := managedPromptRecoveryNow()
	for _, record := range records {
		status := defaultString(strings.TrimSpace(record.Status), "unknown")
		owner := defaultString(strings.TrimSpace(record.OwnerKind), "unknown")
		summary.ByStatus[status]++
		summary.ByOwner[owner]++
		if lastSeen := managedPromptRecoveryHeartbeatAt(record); !lastSeen.IsZero() && now.Sub(lastSeen) >= managedPromptRecoveryStaleThreshold {
			summary.StaleCount++
		}
	}
	return summary, nil
}

func managedPromptRecoveryActiveOwnerRecords(ownerKind string) (map[string]managedPromptRecoveryRecord, error) {
	records, err := listManagedPromptRecoveryRecords()
	if err != nil {
		return nil, err
	}
	snapshot, err := managedPromptRecoveryProcessSnapshot()
	if err != nil {
		return nil, err
	}
	active := map[string]managedPromptRecoveryRecord{}
	for _, record := range records {
		if strings.TrimSpace(record.OwnerKind) != strings.TrimSpace(ownerKind) {
			continue
		}
		switch strings.TrimSpace(record.Status) {
		case managedPromptRecoveryStatusRunning, managedPromptRecoveryStatusRecovering:
			if managedPromptRecoverySnapshotHasPID(snapshot, record.OwnerPID) {
				active[record.OwnerID] = record
			}
		}
	}
	return active, nil
}

func launchManagedPromptRecovery(record managedPromptRecoveryRecord, now time.Time) error {
	cmd, err := startManagedNanaCommand(record.ResumeArgv...)
	if err != nil {
		return err
	}
	logPath := strings.TrimSpace(record.LogPath)
	if logPath == "" {
		logPath = managedPromptRecoveryDefaultLogPath(record.ArtifactRoot, record.CheckpointPath)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd.Dir = record.CWD
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := startManagedNanaStart(cmd); err != nil {
		_ = logFile.Close()
		return err
	}
	updatedAt := now.Format(time.RFC3339Nano)
	record.Status = managedPromptRecoveryStatusRecovering
	record.LogPath = logPath
	record.OwnerPID = cmd.Process.Pid
	record.HeartbeatAt = updatedAt
	record.UpdatedAt = updatedAt
	record.PauseReason = ""
	record.PauseUntil = ""
	record.LastError = ""
	if err := upsertManagedPromptRecovery(record); err != nil {
		_ = logFile.Close()
		return err
	}
	go func() {
		defer logFile.Close()
		_ = cmd.Wait()
	}()
	fmt.Fprintf(os.Stdout, "[start] recovered managed prompt for %s %s (%s).\n",
		defaultString(strings.TrimSpace(record.OwnerKind), "unknown"),
		defaultString(strings.TrimSpace(record.OwnerID), "unknown"),
		defaultString(strings.TrimSpace(record.StepKey), "step"),
	)
	return nil
}
