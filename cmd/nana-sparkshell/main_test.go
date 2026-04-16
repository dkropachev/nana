package main

import (
	"context"
	"errors"
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
		tempRoot, err := os.MkdirTemp("", "nana-sparkshell-main-test-")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(tempRoot, "nana-sparkshell")
		if runtime.GOOS == "windows" {
			buildPath += ".exe"
		}
		cmd := runCommand(t, "go", "build", "-o", buildPath, "./cmd/nana-sparkshell")
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

func writeExecutable(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func TestRawModePreservesStdoutAndStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell snippets use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cmd := runCommand(t, binaryPath, "sh", "-c", "printf 'alpha\\n'; printf 'warn\\n' >&2")
	cmd.Env = append(os.Environ(), "NANA_SPARKSHELL_LINES=5")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("raw mode failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "alpha\n") || !strings.Contains(string(output), "warn\n") {
		t.Fatalf("unexpected raw output: %q", output)
	}
}

func TestSummaryModeUsesCodexExecAndModelOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell snippets use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	codex := filepath.Join(cwd, "codex")
	argsLog := filepath.Join(cwd, "args.log")
	promptLog := filepath.Join(cwd, "prompt.log")
	writeExecutable(t, codex, "#!/bin/sh\nprintf '%s\n' \"$@\" > '"+argsLog+"'\ncat > '"+promptLog+"'\nprintf '%s\n' '- summary: command produced long output' '- warnings: stderr was empty'\n")
	cmd := runCommand(t, binaryPath, "sh", "-c", "printf 'one\\ntwo\\n'")
	cmd.Env = append(os.Environ(),
		"PATH="+cwd+":"+os.Getenv("PATH"),
		"NANA_SPARKSHELL_LINES=1",
		"NANA_SPARKSHELL_MODEL=spark-test-model",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("summary mode failed: %v\n%s", err, output)
	}
	stdout := string(output)
	if !strings.Contains(stdout, "- summary: command produced long output") || !strings.Contains(stdout, "- warnings: stderr was empty") {
		t.Fatalf("unexpected summary output: %q", output)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	if !strings.Contains(string(args), "exec") || !strings.Contains(string(args), "--model") || !strings.Contains(string(args), "spark-test-model") || !strings.Contains(string(args), `model_reasoning_effort="low"`) {
		t.Fatalf("unexpected codex args: %q", args)
	}
	prompt, err := os.ReadFile(promptLog)
	if err != nil {
		t.Fatalf("read prompt log: %v", err)
	}
	if !strings.Contains(string(prompt), "Command family: generic-shell") || !strings.Contains(string(prompt), "<<<STDOUT") || !strings.Contains(string(prompt), "one\ntwo") {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
}

func TestSummaryFailureFallsBackToRawOutputWithNotice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell snippets use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	writeExecutable(t, filepath.Join(cwd, "codex"), "#!/bin/sh\nprintf '%s\n' 'bridge failed' >&2\nexit 9\n")
	cmd := runCommand(t, binaryPath, "/bin/sh", "-c", "printf 'one\\ntwo\\n'; printf 'child-err\\n' >&2")
	cmd.Env = append(os.Environ(), "PATH="+cwd+":"+os.Getenv("PATH"), "NANA_SPARKSHELL_LINES=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("summary fallback failed: %v\n%s", err, output)
	}
	rendered := string(output)
	if !strings.Contains(rendered, "one\ntwo\n") || !strings.Contains(rendered, "child-err") || !strings.Contains(rendered, "summary unavailable") {
		t.Fatalf("unexpected fallback output: %q", output)
	}
}

func TestSummaryModePreservesChildExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell snippets use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	writeExecutable(t, filepath.Join(cwd, "codex"), "#!/bin/sh\nprintf '%s\n' '- failures: command exited non-zero'\n")
	cmd := runCommand(t, binaryPath, "sh", "-c", "printf 'one\\ntwo\\n'; exit 7")
	cmd.Env = append(os.Environ(), "PATH="+cwd+":"+os.Getenv("PATH"), "NANA_SPARKSHELL_LINES=1")
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil || !strings.Contains(string(output), "- failures: command exited non-zero") {
		t.Fatalf("expected summarized non-zero exit, err=%v output=%q", err, output)
	}
	if !strings.Contains(string(output), "- failures: command exited non-zero") {
		t.Fatalf("unexpected output: %q", output)
	}
	if !strings.Contains(err.Error(), "exit status 7") && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 7) {
		t.Fatalf("expected exit status 7, got %v", err)
	}
	if errors.As(err, &exitErr) && exitErr.ExitCode() != 7 {
		t.Fatalf("expected exit status 7, got %d", exitErr.ExitCode())
	}
}

func TestTmuxPaneModeCapturesTailAndSummarizes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	argsLog := filepath.Join(cwd, "tmux-args.log")
	promptLog := filepath.Join(cwd, "prompt.log")
	writeExecutable(t, filepath.Join(cwd, "tmux"), "#!/bin/sh\nprintf '%s\n' \"$@\" > '"+argsLog+"'\nprintf 'line-1\nline-2\nline-3\nline-4\n'\n")
	writeExecutable(t, filepath.Join(cwd, "codex"), "#!/bin/sh\ncat > '"+promptLog+"'\nprintf '%s\n' '- summary: tmux pane summarized' '- warnings: tail captured'\n")
	cmd := runCommand(t, binaryPath, "--tmux-pane", "%17", "--tail-lines", "400")
	cmd.Env = append(os.Environ(), "PATH="+cwd+":"+os.Getenv("PATH"), "NANA_SPARKSHELL_LINES=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tmux pane mode failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "- summary: tmux pane summarized") || !strings.Contains(string(output), "- warnings: tail captured") {
		t.Fatalf("unexpected tmux summary output: %q", output)
	}
	tmuxArgs, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read tmux args: %v", err)
	}
	if !strings.Contains(string(tmuxArgs), "capture-pane") || !strings.Contains(string(tmuxArgs), "%17") || !strings.Contains(string(tmuxArgs), "-400") {
		t.Fatalf("unexpected tmux args: %q", tmuxArgs)
	}
	prompt, err := os.ReadFile(promptLog)
	if err != nil {
		t.Fatalf("read prompt log: %v", err)
	}
	if !strings.Contains(string(prompt), "Command: tmux capture-pane") || !strings.Contains(string(prompt), "line-1") {
		t.Fatalf("unexpected tmux prompt: %q", prompt)
	}
}
