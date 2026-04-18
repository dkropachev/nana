package gocli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const SparkShellUsage = `Usage: nana sparkshell <command> [args...]
   or: nana sparkshell --tmux-pane <pane-id> [--tail-lines <100-1000>]

Direct command mode executes argv without shell metacharacter parsing.
Tmux pane mode captures a larger pane tail and applies the same raw-vs-summary behavior.

Summary behavior:
  stdout+stderr is emitted raw when visible output is <= NANA_SPARKSHELL_LINES (default 12).
  Output above that threshold is summarized with codex exec using low reasoning.
  If summarization fails or times out, raw output is emitted with a "summary unavailable" notice.

Environment controls:
  NANA_SPARKSHELL_LINES                 raw-vs-summary line threshold (default 12)
  NANA_SPARKSHELL_SUMMARY_TIMEOUT_MS    codex summary timeout in milliseconds (default 60000)
  NANA_SPARKSHELL_MODEL                 primary summary model; then NANA_DEFAULT_SPARK_MODEL / NANA_SPARK_MODEL
  NANA_SPARKSHELL_FALLBACK_MODEL        retry model for quota/access/capacity errors; then NANA_DEFAULT_FRONTIER_MODEL
  NANA_SPARKSHELL_SUMMARY_MAX_LINES     max output lines included in summary prompt (default 400)
  NANA_SPARKSHELL_SUMMARY_MAX_BYTES     max output bytes included in summary prompt (default 24000)`

func SparkShell(repoRoot string, cwd string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command to run\n%s", SparkShellUsage)
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
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
