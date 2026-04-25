package gocli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func serviceTestSetIsolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestRunNanaServiceClientFailsWhenServiceAbsent(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	_, err := runNanaServiceClient(t.TempDir(), []string{"status"}, serviceTestDiscard{}, serviceTestDiscard{})
	if err == nil || !strings.Contains(err.Error(), "Start it with `nana start`") {
		t.Fatalf("expected service unavailable error, got %v", err)
	}
}

func TestLaunchNanaServiceSupervisorFailsWhenOwnerAlive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	second, err := launchNanaServiceSupervisor()
	if second != nil {
		_ = second.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("expected active service failure, got %v", err)
	}
}

func TestRunNanaServiceClientStatusRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := filepath.Join(home, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "team-state.json"), []byte(`{"active":true,"current_phase":"team-exec"}`), 0o644); err != nil {
		t.Fatalf("write team state: %v", err)
	}

	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	called := 0
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		called++
		return exec.Command("sh", "-c", "exit 99"), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(home, []string{"status"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 0 {
		t.Fatalf("expected status to run in-process, subprocess count=%d", called)
	}
	if !strings.Contains(stdout.String(), "team: ACTIVE") {
		t.Fatalf("expected status output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientArtifactsRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}

	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	called := 0
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		called++
		return exec.Command("sh", "-c", "exit 99"), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(cwd, []string{"artifacts", "list"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 0 {
		t.Fatalf("expected artifacts to run in-process, subprocess count=%d", called)
	}
	if !strings.Contains(stdout.String(), "NANA artifacts in") {
		t.Fatalf("expected artifacts output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientNextRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	serviceTestSetIsolatedHome(t)

	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	called := 0
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		called++
		return exec.Command("sh", "-c", "exit 99"), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"next"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 0 {
		t.Fatalf("expected next to run in-process, subprocess count=%d", called)
	}
	if !strings.Contains(stdout.String(), "Nothing needs operator attention right now.") {
		t.Fatalf("expected next output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientUsageRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	serviceTestSetIsolatedHome(t)

	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	called := 0
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		called++
		return exec.Command("sh", "-c", "exit 99"), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"usage", "--json"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 0 {
		t.Fatalf("expected usage to run in-process, subprocess count=%d", called)
	}
	if !strings.Contains(stdout.String(), "\"session_roots_scanned\"") && !strings.Contains(stdout.String(), "\"totals\"") {
		t.Fatalf("expected usage JSON output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkStatusRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	sandboxRepoPath := createLocalWorkRepoAt(t, filepath.Join(home, "sandbox", "repo"))
	now := ISOTimeNow()
	manifest := localWorkManifest{
		RunID:            "lw-service-status",
		RepoRoot:         repoRoot,
		RepoName:         filepath.Base(repoRoot),
		RepoID:           localWorkRepoID(repoRoot),
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           "running",
		CurrentIteration: 1,
		MaxIterations:    8,
		SandboxPath:      filepath.Join(home, "sandbox"),
		SandboxRepoPath:  sandboxRepoPath,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	called := 0
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		called++
		return exec.Command("sh", "-c", "exit 99"), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(home, []string{"work", "status", "--run-id", manifest.RunID}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 0 {
		t.Fatalf("expected work status to run in-process, subprocess count=%d", called)
	}
	if !strings.Contains(stdout.String(), "[local] Run id: "+manifest.RunID) {
		t.Fatalf("expected work status output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkLogsRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	sandboxRepoPath := createLocalWorkRepoAt(t, filepath.Join(home, "sandbox", "repo"))
	now := ISOTimeNow()
	manifest := localWorkManifest{
		RunID:            "lw-service-logs",
		RepoRoot:         repoRoot,
		RepoName:         filepath.Base(repoRoot),
		RepoID:           localWorkRepoID(repoRoot),
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           "running",
		CurrentIteration: 1,
		MaxIterations:    8,
		SandboxPath:      filepath.Join(home, "sandbox"),
		SandboxRepoPath:  sandboxRepoPath,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	iterationDir := localWorkIterationDir(runDir, 1)
	if err := os.MkdirAll(iterationDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(iterationDir, "implement-stdout.log"), []byte("hello log\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	called := 0
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		called++
		return exec.Command("sh", "-c", "exit 99"), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(home, []string{"work", "logs", "--run-id", manifest.RunID}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 0 {
		t.Fatalf("expected work logs to run in-process, subprocess count=%d", called)
	}
	if !strings.Contains(stdout.String(), "== implement-stdout.log ==") || !strings.Contains(stdout.String(), "hello log") {
		t.Fatalf("expected work logs output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkExplainRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	home := t.TempDir()
	t.Setenv("HOME", home)
	repoSlug := "acme/widget"
	paths := githubManagedPaths(repoSlug)
	sandboxRepoPath := createLocalWorkRepoAt(t, filepath.Join(paths.RepoRoot, "sandboxes", "issue-42", "repo"))
	manifest := githubWorkManifest{
		RunID:           "gh-service-explain",
		RepoSlug:        repoSlug,
		RepoOwner:       "acme",
		RepoName:        "widget",
		ManagedRepoRoot: paths.RepoRoot,
		TargetURL:       "https://github.com/acme/widget/issues/42",
		TargetKind:      "issue",
		TargetNumber:    42,
		UpdatedAt:       ISOTimeNow(),
		SandboxPath:     filepath.Dir(sandboxRepoPath),
		SandboxRepoPath: sandboxRepoPath,
	}
	manifestPath := filepath.Join(paths.RepoRoot, "runs", manifest.RunID, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("writeGithubJSON: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("indexGithubWorkRunManifest: %v", err)
	}

	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	called := 0
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		called++
		return exec.Command("sh", "-c", "exit 99"), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(home, []string{"work", "explain", "--run-id", manifest.RunID}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 0 {
		t.Fatalf("expected work explain to run in-process, subprocess count=%d", called)
	}
	if !strings.Contains(stdout.String(), "# NANA Work Explain") || !strings.Contains(stdout.String(), "Run id: "+manifest.RunID) {
		t.Fatalf("expected work explain output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkVerifyRefreshRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	sandboxRepoPath := createLocalWorkRepoAt(t, filepath.Join(home, "sandbox", "repo"))
	now := ISOTimeNow()
	manifest := localWorkManifest{
		RunID:           "lw-service-refresh",
		RepoRoot:        repoRoot,
		RepoName:        filepath.Base(repoRoot),
		RepoID:          localWorkRepoID(repoRoot),
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          "running",
		MaxIterations:   8,
		SandboxPath:     filepath.Join(home, "sandbox"),
		SandboxRepoPath: sandboxRepoPath,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	oldFactory := startManagedNanaCommandFactory
	oldStart := startManagedNanaStart
	called := 0
	startManagedNanaCommandFactory = func(args ...string) (*exec.Cmd, error) {
		called++
		return exec.Command("sh", "-c", "exit 99"), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error { return cmd.Start() }
	defer func() {
		startManagedNanaCommandFactory = oldFactory
		startManagedNanaStart = oldStart
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(home, []string{"work", "verify-refresh", "--run-id", manifest.RunID}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 0 {
		t.Fatalf("expected work verify-refresh to run in-process, subprocess count=%d", called)
	}
	if !strings.Contains(stdout.String(), "[local] Verification artifacts for run "+manifest.RunID+" refreshed.") {
		t.Fatalf("expected work verify-refresh output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkStartRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)

	called := 0
	oldHandler := nanaServiceWorkStartHandler
	nanaServiceWorkStartHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "work-start-in-process\n")
		return nil
	}
	defer func() {
		nanaServiceWorkStartHandler = oldHandler
	}()

	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"work", "start", "--task", "demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if called != 1 {
		t.Fatalf("expected in-process handler, count=%d", called)
	}
	if !strings.Contains(stdout.String(), "work-start-in-process") {
		t.Fatalf("expected in-process output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkResumeRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	oldHandler := nanaServiceWorkResumeHandler
	nanaServiceWorkResumeHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "work-resume-in-process\n")
		return nil
	}
	defer func() { nanaServiceWorkResumeHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"work", "resume", "--run-id", "lw-demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "work-resume-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkResolveRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	oldHandler := nanaServiceWorkResolveHandler
	nanaServiceWorkResolveHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "work-resolve-in-process\n")
		return nil
	}
	defer func() { nanaServiceWorkResolveHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"work", "resolve", "--run-id", "lw-demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "work-resolve-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkRetrospectiveRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	oldHandler := nanaServiceWorkRetrospectiveHandler
	nanaServiceWorkRetrospectiveHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "work-retrospective-in-process\n")
		return nil
	}
	defer func() { nanaServiceWorkRetrospectiveHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"work", "retrospective", "--run-id", "lw-demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "work-retrospective-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkSyncRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	oldHandler := nanaServiceWorkSyncHandler
	nanaServiceWorkSyncHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "work-sync-in-process\n")
		return nil
	}
	defer func() { nanaServiceWorkSyncHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"work", "sync", "--run-id", "gh-demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "work-sync-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientWorkLaneExecRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	oldHandler := nanaServiceWorkLaneExecHandler
	nanaServiceWorkLaneExecHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "work-lane-exec-in-process\n")
		return nil
	}
	defer func() { nanaServiceWorkLaneExecHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"work", "lane-exec", "--run-id", "gh-demo", "--lane", "coder"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "work-lane-exec-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientRepoExplainRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	oldHandler := nanaServiceRepoHandler
	nanaServiceRepoHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "repo-explain-in-process\n")
		return nil
	}
	defer func() { nanaServiceRepoHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"repo", "explain", "acme/widget"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "repo-explain-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientRepoDropRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	oldHandler := nanaServiceRepoHandler
	nanaServiceRepoHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "repo-drop-in-process\n")
		return nil
	}
	defer func() { nanaServiceRepoHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"repo", "drop", "acme/widget"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "repo-drop-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientCleanupRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	oldHandler := nanaServiceCleanupHandler
	nanaServiceCleanupHandler = func(args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		_, _ = io.WriteString(stdout, "cleanup-in-process\n")
		return nil
	}
	defer func() { nanaServiceCleanupHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"cleanup", "--dry-run"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "cleanup-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
}

func TestRunNanaServiceClientReviewRulesRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	var gotArgs []string
	oldHandler := nanaServiceGithubReviewRulesHandler
	nanaServiceGithubReviewRulesHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		gotArgs = append([]string{}, args...)
		_, _ = io.WriteString(stdout, "review-rules-in-process\n")
		return nil
	}
	defer func() { nanaServiceGithubReviewRulesHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode, err := runNanaServiceClient(t.TempDir(), []string{"review-rules", "list", "acme/widget"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "review-rules-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
	if !reflect.DeepEqual(gotArgs, []string{"list", "acme/widget"}) {
		t.Fatalf("unexpected args %v", gotArgs)
	}
}

func TestRunNanaServiceClientReviewRunsInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	called := 0
	var gotArgs []string
	oldHandler := nanaServiceGithubReviewHandler
	nanaServiceGithubReviewHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
		called++
		gotArgs = append([]string{}, args...)
		_, _ = io.WriteString(stdout, "review-in-process\n")
		return nil
	}
	defer func() { nanaServiceGithubReviewHandler = oldHandler }()
	supervisor, err := launchNanaServiceSupervisor()
	if err != nil {
		t.Fatalf("launchNanaServiceSupervisor: %v", err)
	}
	defer supervisor.Close()
	var stdout strings.Builder
	var stderr strings.Builder
	argv := []string{"review", "https://github.com/acme/widget/pull/7"}
	exitCode, err := runNanaServiceClient(t.TempDir(), argv, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runNanaServiceClient: %v", err)
	}
	if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "review-in-process") {
		t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
	}
	if !reflect.DeepEqual(gotArgs, argv[1:]) {
		t.Fatalf("unexpected args %v", gotArgs)
	}
}

func TestRunNanaServiceClientGithubIssueCommandsRunInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	cases := []struct {
		name     string
		argv     []string
		wantArgs []string
	}{
		{
			name:     "issue implement",
			argv:     []string{"issue", "implement", "https://github.com/acme/widget/issues/42"},
			wantArgs: []string{"implement", "https://github.com/acme/widget/issues/42"},
		},
		{
			name:     "issue investigate",
			argv:     []string{"issue", "investigate", "https://github.com/acme/widget/issues/42"},
			wantArgs: []string{"investigate", "https://github.com/acme/widget/issues/42"},
		},
		{
			name:     "issue sync",
			argv:     []string{"issue", "sync", "https://github.com/acme/widget/issues/42"},
			wantArgs: []string{"sync", "https://github.com/acme/widget/issues/42"},
		},
		{
			name:     "implement alias",
			argv:     []string{"implement", "https://github.com/acme/widget/issues/42"},
			wantArgs: []string{"implement", "https://github.com/acme/widget/issues/42"},
		},
		{
			name:     "sync alias",
			argv:     []string{"sync", "--run-id", "gh-demo"},
			wantArgs: []string{"sync", "--run-id", "gh-demo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtimeDir := t.TempDir()
			t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
			called := 0
			var gotArgs []string
			oldHandler := nanaServiceGithubIssueHandler
			nanaServiceGithubIssueHandler = func(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
				called++
				gotArgs = append([]string{}, args...)
				_, _ = io.WriteString(stdout, "github-issue-in-process\n")
				return nil
			}
			defer func() { nanaServiceGithubIssueHandler = oldHandler }()
			supervisor, err := launchNanaServiceSupervisor()
			if err != nil {
				t.Fatalf("launchNanaServiceSupervisor: %v", err)
			}
			defer supervisor.Close()
			var stdout strings.Builder
			var stderr strings.Builder
			exitCode, err := runNanaServiceClient(t.TempDir(), tc.argv, &stdout, &stderr)
			if err != nil {
				t.Fatalf("runNanaServiceClient: %v", err)
			}
			if exitCode != 0 || called != 1 || !strings.Contains(stdout.String(), "github-issue-in-process") {
				t.Fatalf("unexpected result exit=%d called=%d stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Fatalf("unexpected args %v", gotArgs)
			}
		})
	}
}

func TestMaybeRunNanaServiceCommandSkipsLocalOnlyCommands(t *testing.T) {
	cases := [][]string{
		{"help"},
		{"hud", "--watch"},
		{"hud", "--tmux"},
		{"investigate", "why is CI failing?"},
		{"improve", "acme/widget"},
		{"enhance", "acme/widget"},
		{"ui-scout", "acme/widget"},
		{"review-rules"},
		{"repo"},
		{"work"},
		{"issue"},
		{"review"},
	}
	for _, argv := range cases {
		routed, _, err := MaybeRunNanaServiceCommand(argv[0], t.TempDir(), argv, serviceTestDiscard{}, serviceTestDiscard{})
		if err != nil {
			t.Fatalf("MaybeRunNanaServiceCommand(%v) returned error: %v", argv, err)
		}
		if routed {
			t.Fatalf("expected %v to remain local", argv)
		}
	}
}

func TestMaybeRunNanaServiceCommandRoutesServiceOwnedGithubCommands(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, runtimeDir)
	cases := [][]string{
		{"review-rules", "list", "acme/widget"},
		{"review", "https://github.com/acme/widget/pull/7"},
		{"issue", "sync", "https://github.com/acme/widget/issues/42"},
		{"implement", "https://github.com/acme/widget/issues/42"},
		{"sync", "--run-id", "gh-demo"},
	}
	for _, argv := range cases {
		routed, _, err := MaybeRunNanaServiceCommand(argv[0], t.TempDir(), argv, serviceTestDiscard{}, serviceTestDiscard{})
		if !routed {
			t.Fatalf("expected %v to route through the service", argv)
		}
		if err == nil || !strings.Contains(err.Error(), "Start it with `nana start`") {
			t.Fatalf("expected service unavailable error for %v, got %v", argv, err)
		}
	}
}

func TestNanaServiceRuntimeDirOverrideWinsOverHome(t *testing.T) {
	override := t.TempDir()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv(nanaServiceRuntimeDirOverrideEnv, override)
	if got := nanaServiceRuntimeDir(); got != override {
		t.Fatalf("expected service runtime dir %q, got %q", override, got)
	}
}

type serviceTestDiscard struct{}

func (serviceTestDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}
