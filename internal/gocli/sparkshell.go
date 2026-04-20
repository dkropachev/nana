package gocli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const SparkShellUsage = "Usage: nana sparkshell <command> [args...]\n   or: nana sparkshell --tmux-pane <pane-id> [--tail-lines <100-1000>]"

func SparkShell(repoRoot string, cwd string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command to run\n%s", SparkShellUsage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stdout, SparkShellUsage)
		return nil
	}

	binaryPath, err := resolveSparkShellPath(repoRoot)
	if err != nil {
		return err
	}

	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = withSparkShellTelemetryEnv(os.Environ(), cwd)
	return cmd.Run()
}

func withSparkShellTelemetryEnv(base []string, cwd string) []string {
	logPath := resolveContextTelemetryLogPath(cwd)
	if strings.TrimSpace(logPath) == "" {
		return base
	}
	return withSparkShellEnvOverride(base, "NANA_CONTEXT_TELEMETRY_LOG", logPath)
}

func resolveContextTelemetryLogPath(cwd string) string {
	if explicit := strings.TrimSpace(os.Getenv("NANA_CONTEXT_TELEMETRY_LOG")); explicit != "" {
		return explicit
	}
	root := resolveContextTelemetryRoot(cwd)
	if strings.TrimSpace(root) == "" {
		return ""
	}
	return filepath.Join(root, ".nana", "logs", "context-telemetry.ndjson")
}

func resolveContextTelemetryRoot(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	dir, err := filepath.Abs(cwd)
	if err != nil {
		dir = filepath.Clean(cwd)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, ".nana")); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return cwd
}

func withSparkShellEnvOverride(base []string, key string, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(base)+1)
	replaced := false
	for _, item := range base {
		if strings.HasPrefix(item, prefix) {
			out = append(out, prefix+value)
			replaced = true
			continue
		}
		out = append(out, item)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}

func resolveSparkShellPath(repoRoot string) (string, error) {
	candidates := []string{}
	if repoRoot != "" {
		candidates = append(candidates, filepath.Join(repoRoot, "bin", "go", binaryName("nana-sparkshell")))
	}
	if os.Args[0] != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(os.Args[0]), binaryName("nana-sparkshell")))
	}
	if exePath, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exePath), binaryName("nana-sparkshell")))
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("go sparkshell shim not found in repo assets or beside the nana binary")
}
