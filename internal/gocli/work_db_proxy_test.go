package gocli

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalWorkDBProxyRoundTripReadWriteViaSocketPresence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	home := setLocalWorkDBProxyTestHome(t)

	supervisor, err := launchLocalWorkDBProxySupervisor()
	if err != nil {
		t.Fatalf("launchLocalWorkDBProxySupervisor: %v", err)
	}
	defer supervisor.Close()
	setActiveStartLocalWorkDBProxySocketForTest(t, "")

	now := time.Now().UTC().Format(time.RFC3339)
	manifest := localWorkManifest{
		RunID:           "proxy-run",
		RepoRoot:        filepath.Join(home, "repo"),
		RepoName:        "repo",
		RepoID:          "repo-1",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          "running",
		SandboxPath:     filepath.Join(home, "sandbox"),
		SandboxRepoPath: filepath.Join(home, "sandbox", "repo"),
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB via socket presence: %v", err)
	}
	if err := store.writeManifest(manifest); err != nil {
		_ = store.Close()
		t.Fatalf("writeManifest via socket presence: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close write store: %v", err)
	}

	readStore, err := openLocalWorkReadDB()
	if err != nil {
		t.Fatalf("openLocalWorkReadDB via socket presence: %v", err)
	}
	defer readStore.Close()

	loaded, err := readStore.readManifest(manifest.RunID)
	if err != nil {
		t.Fatalf("readManifest via socket presence: %v", err)
	}
	if loaded.RunID != manifest.RunID || loaded.RepoRoot != manifest.RepoRoot || loaded.Status != manifest.Status {
		t.Fatalf("unexpected loaded manifest: %+v", loaded)
	}
}

func TestOpenLocalWorkDBUsesDirectSQLiteWhenSocketAbsent(t *testing.T) {
	_ = setLocalWorkDBProxyTestHome(t)
	setActiveStartLocalWorkDBProxySocketForTest(t, "")

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()

	if _, err := os.Stat(localWorkDBPath()); err != nil {
		t.Fatalf("expected direct local DB file, got err=%v", err)
	}
}

func TestOpenLocalWorkDBFailsWhenSocketPresentButUnreachable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	_ = setLocalWorkDBProxyTestHome(t)
	setActiveStartLocalWorkDBProxySocketForTest(t, "")

	socketPath := localWorkDBProxySocketPath()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	_, err = openLocalWorkDB()
	if err == nil || !strings.Contains(err.Error(), "DB proxy socket present at") || !strings.Contains(err.Error(), socketPath) {
		t.Fatalf("expected proxy connection failure, got %v", err)
	}
}

func TestLaunchLocalWorkDBProxySupervisorWritesRuntimeState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	_ = setLocalWorkDBProxyTestHome(t)

	supervisor, err := launchLocalWorkDBProxySupervisor()
	if err != nil {
		t.Fatalf("launchLocalWorkDBProxySupervisor: %v", err)
	}

	var runtimeState localWorkDBProxyRuntimeState
	if err := readGithubJSON(localWorkDBProxyRuntimePath(), &runtimeState); err != nil {
		supervisor.Close()
		t.Fatalf("read runtime state: %v", err)
	}
	if runtimeState.Status != localWorkDBProxyActiveState || runtimeState.SocketPath != localWorkDBProxySocketPath() || runtimeState.StartedAt == "" || runtimeState.StoppedAt != "" {
		supervisor.Close()
		t.Fatalf("unexpected active runtime state: %+v", runtimeState)
	}

	if err := supervisor.Close(); err != nil {
		t.Fatalf("close supervisor: %v", err)
	}
	if err := readGithubJSON(localWorkDBProxyRuntimePath(), &runtimeState); err != nil {
		t.Fatalf("read stopped runtime state: %v", err)
	}
	if runtimeState.Status != localWorkDBProxyStoppedState || runtimeState.SocketPath != localWorkDBProxySocketPath() || runtimeState.StoppedAt == "" || runtimeState.StartedAt == "" {
		t.Fatalf("unexpected stopped runtime state: %+v", runtimeState)
	}
}

func TestLaunchLocalWorkDBProxySupervisorOverwritesStaleRuntimeState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	_ = setLocalWorkDBProxyTestHome(t)

	if err := writeGithubJSON(localWorkDBProxyRuntimePath(), localWorkDBProxyRuntimeState{
		ProcessID:  999,
		SocketPath: "/tmp/stale.sock",
		Status:     localWorkDBProxyStoppedState,
		StartedAt:  "2000-01-01T00:00:00Z",
		StoppedAt:  "2000-01-01T00:00:01Z",
	}); err != nil {
		t.Fatalf("write stale runtime state: %v", err)
	}

	supervisor, err := launchLocalWorkDBProxySupervisor()
	if err != nil {
		t.Fatalf("launchLocalWorkDBProxySupervisor: %v", err)
	}
	defer supervisor.Close()

	var runtimeState localWorkDBProxyRuntimeState
	if err := readGithubJSON(localWorkDBProxyRuntimePath(), &runtimeState); err != nil {
		t.Fatalf("read runtime state: %v", err)
	}
	if runtimeState.Status != localWorkDBProxyActiveState || runtimeState.SocketPath != localWorkDBProxySocketPath() || runtimeState.SocketPath == "/tmp/stale.sock" || runtimeState.StoppedAt != "" {
		t.Fatalf("expected fresh active runtime state, got %+v", runtimeState)
	}
}

func TestLaunchLocalWorkDBProxySupervisorFailsWhenSocketAlreadyActive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	_ = setLocalWorkDBProxyTestHome(t)

	supervisor, err := launchLocalWorkDBProxySupervisor()
	if err != nil {
		t.Fatalf("launchLocalWorkDBProxySupervisor: %v", err)
	}
	defer supervisor.Close()

	second, err := launchLocalWorkDBProxySupervisor()
	if second != nil {
		_ = second.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("expected active-socket launch failure, got %v", err)
	}
}

func setStartManagedNanaStartForTest(t *testing.T, hook func(*exec.Cmd) error) {
	t.Helper()
	old := startManagedNanaStart
	startManagedNanaStart = hook
	t.Cleanup(func() { startManagedNanaStart = old })
}

func setLocalWorkDBProxyTestHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "nana-db-")
	if err != nil {
		t.Fatalf("mktemp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	return home
}

func setActiveStartLocalWorkDBProxySocketForTest(t *testing.T, socketPath string) {
	t.Helper()
	old := activeStartLocalWorkDBProxySocket()
	startLocalWorkDBProxyActiveMu.Lock()
	startLocalWorkDBProxyActiveSocket = strings.TrimSpace(socketPath)
	startLocalWorkDBProxyActiveMu.Unlock()
	t.Cleanup(func() {
		startLocalWorkDBProxyActiveMu.Lock()
		startLocalWorkDBProxyActiveSocket = old
		startLocalWorkDBProxyActiveMu.Unlock()
	})
}

func assertStartManagedNanaLaunchUsesSocketPresence(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	socketPath := localWorkDBProxySocketPath()
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("expected fixed DB proxy socket at %q: %v", socketPath, err)
	}
	foundInternalServiceBypass := false
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "NANA_WORK_DB_PROXY_SOCKET=") {
			t.Fatalf("expected no DB proxy socket env injection, got %q", entry)
		}
		if strings.HasPrefix(entry, "NANA_START_DB_PROXY_REQUIRED=") {
			t.Fatalf("expected no DB proxy required env injection, got %q", entry)
		}
		if entry == nanaServiceInternalEnv+"=1" {
			foundInternalServiceBypass = true
		}
	}
	if !foundInternalServiceBypass {
		t.Fatalf("expected managed Nana launch to set %s=1, got env=%#v", nanaServiceInternalEnv, cmd.Env)
	}
}
