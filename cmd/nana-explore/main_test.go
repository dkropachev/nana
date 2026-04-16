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
		tempRoot, err := os.MkdirTemp("", "nana-explore-main-test-")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(tempRoot, "nana-explore-harness")
		if runtime.GOOS == "windows" {
			buildPath += ".exe"
		}
		cmd := runCommand(t, "go", "build", "-o", buildPath, "./cmd/nana-explore")
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

func TestHarnessInvokesCodexExecAndWritesMarkdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	capturePath := filepath.Join(cwd, "capture.txt")
	codexStub := filepath.Join(cwd, "codex-stub.sh")
	writeExecutable(t, codexStub, "#!/bin/sh\nset -eu\noutput_path=''\n: > '"+capturePath+"'\nwhile [ \"$#\" -gt 0 ]; do\n  printf '%s\n' \"$1\" >> '"+capturePath+"'\n  if [ \"$1\" = '-o' ] && [ \"$#\" -ge 2 ]; then\n    output_path=\"$2\"\n    shift 2\n    continue\n  fi\n  shift\ndone\nprintf '# Files\n- demo\n' > \"$output_path\"\n")
	promptPath := filepath.Join(cwd, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("# contract\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	cmd := runCommand(t, binaryPath,
		"--cwd", cwd,
		"--prompt", "find auth",
		"--prompt-file", promptPath,
		"--model-spark", "gpt-5.3-codex-spark",
		"--model-fallback", "gpt-5.4",
	)
	cmd.Env = append(os.Environ(), "NANA_EXPLORE_CODEX_BIN="+codexStub)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("explore harness failed: %v\n%s", err, output)
	}
	if string(output) != "# Files\n- demo\n" {
		t.Fatalf("unexpected harness output: %q", output)
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	rendered := string(captured)
	if !strings.Contains(rendered, "exec") || !strings.Contains(rendered, "-m") || !strings.Contains(rendered, "gpt-5.3-codex-spark") || !strings.Contains(rendered, "find auth") {
		t.Fatalf("unexpected codex argv capture: %q", captured)
	}
}

func TestHarnessSupportsPromptFileContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	capturePath := filepath.Join(cwd, "capture.txt")
	codexStub := filepath.Join(cwd, "codex-stub.sh")
	writeExecutable(t, codexStub, "#!/bin/sh\nset -eu\noutput_path=''\n: > '"+capturePath+"'\nwhile [ \"$#\" -gt 0 ]; do\n  printf '%s\n' \"$1\" >> '"+capturePath+"'\n  if [ \"$1\" = '-o' ] && [ \"$#\" -ge 2 ]; then\n    output_path=\"$2\"\n    shift 2\n    continue\n  fi\n  shift\ndone\nprintf '# Answer\nHarness completed\n' > \"$output_path\"\n")
	promptPath := filepath.Join(cwd, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("# contract\n"), 0o644); err != nil {
		t.Fatalf("write prompt contract: %v", err)
	}
	cmd := runCommand(t, binaryPath,
		"--cwd", cwd,
		"--prompt", "find prompt-file support",
		"--prompt-file", promptPath,
		"--model-spark", "gpt-5.3-codex-spark",
		"--model-fallback", "gpt-5.4",
	)
	cmd.Env = append(os.Environ(), "NANA_EXPLORE_CODEX_BIN="+codexStub)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("explore harness failed: %v\n%s", err, output)
	}
	if string(output) != "# Answer\nHarness completed\n" {
		t.Fatalf("unexpected harness output: %q", output)
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !strings.Contains(string(captured), "find prompt-file support") {
		t.Fatalf("prompt not forwarded to codex argv: %q", captured)
	}
}

func TestHarnessUsesAllowlistEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs use POSIX sh")
	}
	binaryPath := buildBinary(t)
	cwd := t.TempDir()
	testBin := filepath.Join(cwd, "test-bin")
	if err := os.MkdirAll(testBin, 0o755); err != nil {
		t.Fatalf("mkdir test bin: %v", err)
	}
	writeExecutable(t, filepath.Join(testBin, "rg"), "#!/bin/sh\necho 'ripgrep 14.0.0'\n")
	capturePath := filepath.Join(cwd, "capture.txt")
	codexStub := filepath.Join(cwd, "codex-stub.sh")
	writeExecutable(t, codexStub, "#!/bin/sh\nset -eu\noutput_path=''\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = '-o' ] && [ \"$#\" -ge 2 ]; then\n    output_path=\"$2\"\n    shift 2\n    continue\n  fi\n  shift\ndone\nbash -lc 'rg --version' > '"+filepath.Join(cwd, "allowed.out")+"' 2> '"+filepath.Join(cwd, "allowed.err")+"'\nallowed_status=$?\nset +e\nbash -lc 'node --version' > '"+filepath.Join(cwd, "blocked.out")+"' 2> '"+filepath.Join(cwd, "blocked.err")+"'\nblocked_status=$?\nset -e\n{\n  printf 'PATH=%s\n' \"$PATH\"\n  printf 'SHELL=%s\n' \"${SHELL:-}\"\n  printf 'ALLOWED_STATUS=%s\n' \"$allowed_status\"\n  printf 'BLOCKED_STATUS=%s\n' \"$blocked_status\"\n} > '"+capturePath+"'\nprintf '# Answer\nHarness completed\n' > \"$output_path\"\n")
	promptPath := filepath.Join(cwd, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("# contract\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	cmd := runCommand(t, binaryPath,
		"--cwd", cwd,
		"--prompt", "find buildTmuxPaneCommand",
		"--prompt-file", promptPath,
		"--model-spark", "gpt-5.3-codex-spark",
		"--model-fallback", "gpt-5.4",
	)
	cmd.Env = append(os.Environ(),
		"NANA_EXPLORE_CODEX_BIN="+codexStub,
		"PATH="+testBin+":"+os.Getenv("PATH"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("explore harness failed: %v\n%s", err, output)
	}
	if string(output) != "# Answer\nHarness completed\n" {
		t.Fatalf("unexpected harness output: %q", output)
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	rendered := string(captured)
	if !strings.Contains(rendered, "PATH=") || !strings.Contains(rendered, "nana-explore-allowlist-") || !strings.Contains(rendered, "SHELL=") || !strings.Contains(rendered, "/bin/bash") {
		t.Fatalf("unexpected allowlist env capture: %q", captured)
	}
	if !strings.Contains(rendered, "ALLOWED_STATUS=0") {
		t.Fatalf("expected allowlisted rg to succeed: %q", captured)
	}
	if strings.Contains(rendered, "BLOCKED_STATUS=0") {
		t.Fatalf("expected blocked node command to fail: %q", captured)
	}
}
