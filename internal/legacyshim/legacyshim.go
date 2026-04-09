package legacyshim

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	GoShimActiveEnv = "NANA_GO_SHIM_ACTIVE"
	RepoRootEnv     = "NANA_REPO_ROOT"
)

var ErrLegacyEntrypointUnavailable = errors.New("legacy JS entrypoint unavailable")

func ResolveRepoRoot(explicitRepoRoot string, executablePath string) string {
	candidates := make([]string, 0, 4)
	if explicitRepoRoot != "" {
		candidates = append(candidates, explicitRepoRoot)
	}
	if executablePath != "" {
		exeDir := filepath.Dir(executablePath)
		candidates = append(candidates, filepath.Dir(exeDir), exeDir)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, "package.json")); err == nil {
			return candidate
		}
	}
	return ""
}

func ResolveLegacyCLIEntry(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	primary := filepath.Join(repoRoot, "dist", "cli", "nana.js")
	if _, err := os.Stat(primary); err == nil {
		return primary
	}
	fallback := filepath.Join(repoRoot, "dist", "cli", "index.js")
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	return ""
}

func Run(repoRoot string, args []string) error {
	entry := ResolveLegacyCLIEntry(repoRoot)
	if entry == "" {
		return fmt.Errorf("%w: run \"npm run build\" to generate dist/cli/nana.js", ErrLegacyEntrypointUnavailable)
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("node is required for the transitional Go shim: %w", err)
	}

	cmd := exec.Command(nodePath, append([]string{entry}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = append(os.Environ(),
		GoShimActiveEnv+"=1",
		RepoRootEnv+"="+repoRoot,
	)
	return cmd.Run()
}
