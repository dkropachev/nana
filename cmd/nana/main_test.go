package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

var (
	buildNanaBinaryOnce sync.Once
	buildNanaBinaryPath string
	buildNanaBinaryErr  error
	buildNanaBinaryLog  []byte
)

const commandTimeout = 15 * time.Second

func runCommand(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	t.Cleanup(cancel)
	return exec.CommandContext(ctx, name, args...)
}

func buildNanaBinary(t *testing.T) string {
	t.Helper()
	buildNanaBinaryOnce.Do(func() {
		tempRoot, err := os.MkdirTemp("", "nana-go-main-test-")
		if err != nil {
			buildNanaBinaryErr = err
			return
		}
		buildNanaBinaryPath = filepath.Join(tempRoot, "nana")
		if runtime.GOOS == "windows" {
			buildNanaBinaryPath += ".exe"
		}
		cmd := runCommand(t, "go", "build", "-o", buildNanaBinaryPath, "./cmd/nana")
		cmd.Dir = repoRoot(t)
		buildNanaBinaryLog, buildNanaBinaryErr = cmd.CombinedOutput()
	})
	if buildNanaBinaryErr != nil {
		t.Fatalf("go build failed: %v\n%s", buildNanaBinaryErr, buildNanaBinaryLog)
	}
	testBinaryPath := filepath.Join(t.TempDir(), filepath.Base(buildNanaBinaryPath))
	content, err := os.ReadFile(buildNanaBinaryPath)
	if err != nil {
		t.Fatalf("read shared binary: %v", err)
	}
	if err := os.WriteFile(testBinaryPath, content, 0o755); err != nil {
		t.Fatalf("copy shared binary: %v", err)
	}
	return testBinaryPath
}

func TestBinaryDefaultLaunchRoutesToCodex(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(fakeCodexPath, []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	cmd := runCommand(t, binaryPath)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"CODEX_HOME="+filepath.Join(home, ".codex-home"),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary launch failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "fake-codex:") {
		t.Fatalf("expected codex launch output, got %q", output)
	}
}

func TestBinaryExecRoutesToCodexExec(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(fakeCodexPath, []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	cmd := runCommand(t, binaryPath, "exec", "--help")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"CODEX_HOME="+filepath.Join(home, ".codex-home"),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary exec failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "fake-codex:exec --help") {
		t.Fatalf("expected codex exec output, got %q", output)
	}
}

func TestBinaryNestedGithubHelpRoutesLocally(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()

	testCases := []struct {
		args     []string
		expected string
	}{
		{args: []string{"implement", "--help"}, expected: "nana issue - GitHub issue-oriented aliases"},
		{args: []string{"investigate", "--help"}, expected: "nana issue - GitHub issue-oriented aliases"},
		{args: []string{"sync", "--help"}, expected: "nana issue - GitHub issue-oriented aliases"},
		{args: []string{"issue", "--help"}, expected: "nana issue - GitHub issue-oriented aliases"},
		{args: []string{"review", "--help"}, expected: "nana review - Review an external GitHub PR with deterministic persistence"},
		{args: []string{"review-rules", "--help"}, expected: "nana review-rules - Persistent repo rules mined from PR review history"},
		{args: []string{"work-on", "--help"}, expected: "nana work-on - GitHub-targeted issue/PR implementation helper"},
	}

	for _, tc := range testCases {
		cmd := runCommand(t, binaryPath, tc.args...)
		cmd.Dir = cwd
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("binary help %v failed: %v\n%s", tc.args, err, output)
		}
		if !strings.Contains(string(output), tc.expected) {
			t.Fatalf("expected %q in output for %v, got %q", tc.expected, tc.args, output)
		}
	}
}

func TestBinaryStandaloneSetupWithoutRepoRoot(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")

	cmd := runCommand(t, binaryPath, "setup", "--scope", "project")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"CODEX_HOME="+filepath.Join(home, ".codex-home"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary standalone setup failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".codex", "prompts", "executor.md")); err != nil {
		t.Fatalf("expected embedded setup prompt to be installed: %v\n%s", err, output)
	}
}

func TestBinaryStandaloneAgentsInitWithoutRepoRoot(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "src", "index.ts"), []byte("export const x = 1;\n"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}

	cmd := runCommand(t, binaryPath, "agents-init")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary standalone agents-init failed: %v\n%s", err, output)
	}
	rootAgents, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v\n%s", err, output)
	}
	if !strings.Contains(string(rootAgents), "<!-- NANA:AGENTS-INIT:MANAGED -->") {
		t.Fatalf("expected managed AGENTS output, got %q", rootAgents)
	}
}
