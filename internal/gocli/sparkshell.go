package gocli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	cmd.Env = os.Environ()
	return cmd.Run()
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
