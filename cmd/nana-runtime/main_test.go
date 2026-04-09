package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
	buildLog  []byte
)

const commandTimeout = 15 * time.Second

func runCommand(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	t.Cleanup(cancel)
	return exec.CommandContext(ctx, name, args...)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func buildBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		tempRoot, err := os.MkdirTemp("", "nana-runtime-main-test-")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(tempRoot, "nana-runtime")
		if runtime.GOOS == "windows" {
			buildPath += ".exe"
		}
		cmd := runCommand(t, "go", "build", "-o", buildPath, "./cmd/nana-runtime")
		cmd.Dir = repoRoot(t)
		buildLog, buildErr = cmd.CombinedOutput()
	})
	if buildErr != nil {
		t.Fatalf("go build failed: %v\n%s", buildErr, buildLog)
	}
	testBinaryPath := filepath.Join(t.TempDir(), filepath.Base(buildPath))
	content, err := os.ReadFile(buildPath)
	if err != nil {
		t.Fatalf("read shared binary: %v", err)
	}
	if err := os.WriteFile(testBinaryPath, content, 0o755); err != nil {
		t.Fatalf("copy shared binary: %v", err)
	}
	return testBinaryPath
}

func TestSchemaSubcommandPrintsContractSummary(t *testing.T) {
	binaryPath := buildBinary(t)
	output, err := runCommand(t, binaryPath, "schema").CombinedOutput()
	if err != nil {
		t.Fatalf("schema failed: %v\n%s", err, output)
	}
	stdout := string(output)
	if !strings.Contains(stdout, "runtime-schema=1") ||
		!strings.Contains(stdout, "acquire-authority") ||
		!strings.Contains(stdout, "dispatch-queued") ||
		!strings.Contains(stdout, "transport=tmux") ||
		!strings.Contains(stdout, "queue-transition=notified") {
		t.Fatalf("unexpected schema output: %q", output)
	}
}

func TestSchemaJSONSubcommandPrintsValidJSON(t *testing.T) {
	binaryPath := buildBinary(t)
	output, err := runCommand(t, binaryPath, "schema", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("schema --json failed: %v\n%s", err, output)
	}
	var parsed map[string]any
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, output)
	}
	if parsed["schema_version"].(float64) != 1 {
		t.Fatalf("unexpected schema version: %+v", parsed)
	}
}

func TestSnapshotSubcommandPrintsRuntimeSnapshot(t *testing.T) {
	binaryPath := buildBinary(t)
	output, err := runCommand(t, binaryPath, "snapshot").CombinedOutput()
	if err != nil {
		t.Fatalf("snapshot failed: %v\n%s", err, output)
	}
	stdout := string(output)
	if !strings.Contains(stdout, "authority=") || !strings.Contains(stdout, "readiness=blocked") {
		t.Fatalf("unexpected snapshot output: %q", output)
	}
}

func TestSnapshotJSONSubcommandPrintsValidJSON(t *testing.T) {
	binaryPath := buildBinary(t)
	output, err := runCommand(t, binaryPath, "snapshot", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("snapshot --json failed: %v\n%s", err, output)
	}
	var parsed map[string]any
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, output)
	}
	if parsed["schema_version"].(float64) != 1 {
		t.Fatalf("unexpected schema version: %+v", parsed)
	}
	readiness := parsed["readiness"].(map[string]any)
	if readiness["ready"].(bool) {
		t.Fatalf("expected blocked readiness: %+v", parsed)
	}
}

func TestMuxContractSubcommandReportsAdapterStatus(t *testing.T) {
	binaryPath := buildBinary(t)
	output, err := runCommand(t, binaryPath, "mux-contract").CombinedOutput()
	if err != nil {
		t.Fatalf("mux-contract failed: %v\n%s", err, output)
	}
	stdout := string(output)
	if !strings.Contains(stdout, "adapter-status=tmux adapter ready") ||
		!strings.Contains(stdout, "resolve-target") ||
		!strings.Contains(stdout, "submit-policy=enter(presses=2, delay_ms=100)") ||
		!strings.Contains(stdout, "confirmation=Confirmed") {
		t.Fatalf("unexpected mux-contract output: %q", output)
	}
}

func TestExecSubcommandProcessesJSONCommand(t *testing.T) {
	binaryPath := buildBinary(t)
	output, err := runCommand(t, binaryPath, "exec", `{"command":"CaptureSnapshot"}`).CombinedOutput()
	if err != nil {
		t.Fatalf("exec failed: %v\n%s", err, output)
	}
	var parsed map[string]any
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, output)
	}
	if parsed["event"] != "SnapshotCaptured" {
		t.Fatalf("unexpected event: %+v", parsed)
	}
}

func TestExecAcquireAuthorityReturnsEvent(t *testing.T) {
	binaryPath := buildBinary(t)
	output, err := runCommand(t, binaryPath, "exec", `{"command":"AcquireAuthority","owner":"w1","lease_id":"l1","leased_until":"2026-03-19T02:00:00Z"}`).CombinedOutput()
	if err != nil {
		t.Fatalf("exec acquire failed: %v\n%s", err, output)
	}
	var parsed map[string]any
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, output)
	}
	if parsed["event"] != "AuthorityAcquired" || parsed["owner"] != "w1" {
		t.Fatalf("unexpected acquire event: %+v", parsed)
	}
}

func TestExecInvalidJSONFails(t *testing.T) {
	binaryPath := buildBinary(t)
	output, err := runCommand(t, binaryPath, "exec", "not-json").CombinedOutput()
	if err == nil {
		t.Fatalf("expected invalid json failure, got output %q", output)
	}
	if !strings.Contains(string(output), "invalid JSON") {
		t.Fatalf("unexpected invalid-json output: %q", output)
	}
}

func TestInitCreatesStateDirectory(t *testing.T) {
	binaryPath := buildBinary(t)
	dir := filepath.Join(t.TempDir(), "runtime-state")
	output, err := runCommand(t, binaryPath, "init", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("init failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "initialized state directory") {
		t.Fatalf("unexpected init output: %q", output)
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshot.json")); err != nil {
		t.Fatalf("missing snapshot.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.json")); err != nil {
		t.Fatalf("missing events.json: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}
	if parsed["schema_version"].(float64) != 1 {
		t.Fatalf("unexpected snapshot: %+v", parsed)
	}
}

func TestExecWithStateDirPersists(t *testing.T) {
	binaryPath := buildBinary(t)
	dir := filepath.Join(t.TempDir(), "runtime-state")
	cmdJSON := `{"command":"AcquireAuthority","owner":"w1","lease_id":"l1","leased_until":"2026-03-19T02:00:00Z"}`
	output, err := runCommand(t, binaryPath, "exec", cmdJSON, "--state-dir="+dir).CombinedOutput()
	if err != nil {
		t.Fatalf("exec with state dir failed: %v\n%s", err, output)
	}
	snapshotContent, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(snapshotContent, &snapshot); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}
	authority := snapshot["authority"].(map[string]any)
	if authority["owner"] != "w1" {
		t.Fatalf("unexpected persisted authority: %+v", snapshot)
	}
	readiness := snapshot["readiness"].(map[string]any)
	if !readiness["ready"].(bool) {
		t.Fatalf("expected ready snapshot: %+v", snapshot)
	}
	eventsContent, err := os.ReadFile(filepath.Join(dir, "events.json"))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var events []map[string]any
	if err := json.Unmarshal(eventsContent, &events); err != nil {
		t.Fatalf("invalid events json: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("unexpected event log length: %+v", events)
	}
}

func TestSnapshotFromStateDirReadsPersistedState(t *testing.T) {
	binaryPath := buildBinary(t)
	dir := filepath.Join(t.TempDir(), "runtime-state")
	if output, err := runCommand(t, binaryPath, "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v\n%s", err, output)
	}
	cmdJSON := `{"command":"AcquireAuthority","owner":"w1","lease_id":"l1","leased_until":"2026-03-19T02:00:00Z"}`
	if output, err := runCommand(t, binaryPath, "exec", cmdJSON, "--state-dir="+dir).CombinedOutput(); err != nil {
		t.Fatalf("exec failed: %v\n%s", err, output)
	}
	output, err := runCommand(t, binaryPath, "snapshot", "--json", "--state-dir="+dir).CombinedOutput()
	if err != nil {
		t.Fatalf("snapshot --json state-dir failed: %v\n%s", err, output)
	}
	var parsed map[string]any
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, output)
	}
	authority := parsed["authority"].(map[string]any)
	if authority["owner"] != "w1" {
		t.Fatalf("unexpected loaded snapshot: %+v", parsed)
	}
	readiness := parsed["readiness"].(map[string]any)
	if !readiness["ready"].(bool) {
		t.Fatalf("expected ready snapshot: %+v", parsed)
	}
}
