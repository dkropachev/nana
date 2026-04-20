package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestAppendShellTelemetrySkipsEventConstructionWhenDisabledOrMissingLog(t *testing.T) {
	cases := []struct {
		name      string
		telemetry string
		logPath   string
	}{
		{
			name:      "disabled",
			telemetry: "off",
			logPath:   filepath.Join(t.TempDir(), "context-telemetry.ndjson"),
		},
		{
			name:      "missing log path",
			telemetry: "1",
			logPath:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NANA_CONTEXT_TELEMETRY", tc.telemetry)
			t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", tc.logPath)
			called := false

			appendShellTelemetryIfEnabled(func() shellTelemetryEvent {
				called = true
				return shellTelemetryEvent{Event: "should-not-be-built"}
			})

			if called {
				t.Fatalf("telemetry event builder ran despite telemetry=%q logPath=%q", tc.telemetry, tc.logPath)
			}
			if tc.logPath != "" {
				if content, err := os.ReadFile(tc.logPath); err == nil {
					t.Fatalf("telemetry log should not be created, got %q", content)
				} else if !os.IsNotExist(err) {
					t.Fatalf("read telemetry log: %v", err)
				}
			}
		})
	}
}

func TestCountVisibleLinesMatchesShellOutputLineContract(t *testing.T) {
	cases := map[string]struct {
		input string
		want  int
	}{
		"empty":                  {"", 0},
		"single unterminated":    {"alpha", 1},
		"single terminated":      {"alpha\n", 1},
		"multiple unterminated":  {"alpha\nbeta", 2},
		"multiple terminated":    {"alpha\nbeta\n", 2},
		"trailing blank visible": {"alpha\n\n", 2},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := countVisibleLines([]byte(tc.input)); got != tc.want {
				t.Fatalf("countVisibleLines(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestCountVisibleLinesDoesNotAllocateForLargeOutput(t *testing.T) {
	output := bytes.Repeat([]byte("x\n"), 64*1024)
	if got := countVisibleLines(output); got != 64*1024 {
		t.Fatalf("countVisibleLines() = %d, want %d", got, 64*1024)
	}

	allocs := testing.AllocsPerRun(100, func() {
		_ = countVisibleLines(output)
	})

	if allocs != 0 {
		t.Fatalf("countVisibleLines allocated %.2f times per run; want 0", allocs)
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

func TestRawModeDoesNotWriteContextTelemetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell snippets use POSIX sh")
	}
	binaryPath := buildBinary(t)
	logPath := filepath.Join(t.TempDir(), ".nana", "logs", "context-telemetry.ndjson")
	cmd := runCommand(t, binaryPath, "sh", "-c", "printf 'small\\n'")
	cmd.Env = append(os.Environ(),
		"NANA_SPARKSHELL_LINES=5",
		"NANA_CONTEXT_TELEMETRY_LOG="+logPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("raw mode failed: %v\n%s", err, output)
	}
	if string(output) != "small\n" {
		t.Fatalf("unexpected raw output: %q", output)
	}
	if content, err := os.ReadFile(logPath); err == nil {
		t.Fatalf("raw mode should not create telemetry log, got %q", content)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat telemetry log: %v", err)
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
	if !strings.Contains(string(prompt), "Command: sh -c") || !strings.Contains(string(prompt), "Exit code: 0") || !strings.Contains(string(prompt), "<<<STDOUT") || !strings.Contains(string(prompt), "one\ntwo") {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
}

func TestSummaryModeWritesCompactionTelemetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell snippets use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	writeExecutable(t, filepath.Join(cwd, "codex"), "#!/bin/sh\nprintf '%s\n' '- summary: command produced long output'\n")
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	cmd := runCommand(t, binaryPath, "sh", "-c", "printf 'one\\ntwo\\n'; : secret-token-should-not-leak")
	cmd.Env = append(os.Environ(),
		"PATH="+cwd+":"+os.Getenv("PATH"),
		"NANA_SPARKSHELL_LINES=1",
		"NANA_CONTEXT_TELEMETRY=1",
		"NANA_CONTEXT_TELEMETRY_LOG="+logPath,
		"NANA_CONTEXT_TELEMETRY_RUN_ID=run-123",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("summary mode failed: %v\n%s", err, output)
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read telemetry log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one telemetry event, got %d: %q", len(lines), content)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal telemetry event: %v\n%s", err, lines[0])
	}
	if event["event"] != "shell_output_compaction" || event["tool"] != "nana-sparkshell" || event["run_id"] != "run-123" {
		t.Fatalf("unexpected telemetry identity fields: %#v", event)
	}
	if event["captured_bytes"] != float64(len("one\ntwo\n")) || event["summary_bytes"] == float64(0) || event["summarized"] != true {
		t.Fatalf("unexpected telemetry byte fields: %#v", event)
	}
	if event["stdout_lines"] != float64(2) || event["stderr_lines"] != float64(0) {
		t.Fatalf("unexpected telemetry line fields: %#v", event)
	}
	if _, ok := event["command"]; ok {
		t.Fatalf("telemetry must not persist full command arguments: %#v", event)
	}
	if event["command_name"] != "sh" || event["argument_count"] != float64(2) {
		t.Fatalf("unexpected telemetry command shape: %#v", event)
	}
	if strings.Contains(string(content), "secret-token-should-not-leak") || strings.Contains(string(content), "printf") {
		t.Fatalf("telemetry leaked command arguments: %q", content)
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

func TestSummaryFailureTelemetryStoresOnlySanitizedErrorKind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell snippets use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	writeExecutable(t, filepath.Join(cwd, "codex"), "#!/bin/sh\ncat >&2\nexit 9\n")
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	cmd := runCommand(t, binaryPath, "sh", "-c", "printf 'line-one\\nsecret-output-should-not-leak\\n'; : secret-arg-should-not-leak")
	cmd.Env = append(os.Environ(),
		"PATH="+cwd+":"+os.Getenv("PATH"),
		"NANA_SPARKSHELL_LINES=1",
		"NANA_CONTEXT_TELEMETRY=1",
		"NANA_CONTEXT_TELEMETRY_LOG="+logPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("summary fallback failed: %v\n%s", err, output)
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read telemetry log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one telemetry event, got %d: %q", len(lines), content)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal telemetry event: %v\n%s", err, lines[0])
	}
	if event["event"] != "shell_output_compaction_failed" || event["error"] != telemetryErrorCodexFailed {
		t.Fatalf("unexpected failure telemetry fields: %#v", event)
	}
	if event["command_name"] != "sh" || event["argument_count"] != float64(2) || event["summarized"] != false {
		t.Fatalf("unexpected telemetry shape: %#v", event)
	}
	for _, leaked := range []string{
		"secret-arg-should-not-leak",
		"secret-output-should-not-leak",
		"Command:",
		"STDOUT:",
		"printf",
	} {
		if strings.Contains(string(content), leaked) {
			t.Fatalf("telemetry leaked %q in %q", leaked, content)
		}
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
	if !strings.Contains(string(prompt), "Command: tmux capture-pane") || !strings.Contains(string(prompt), "Exit code: 0") || !strings.Contains(string(prompt), "line-1") {
		t.Fatalf("unexpected tmux prompt: %q", prompt)
	}
}
