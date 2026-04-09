package legacyshim

import (
	"os"
	"path/filepath"
)

const (
	RepoRootEnv = "NANA_REPO_ROOT"
)

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
		if hasRepoRootMarker(candidate) {
			return candidate
		}
	}
	return ""
}

func hasRepoRootMarker(root string) bool {
	markers := []string{
		"go.mod",
		"package.json",
		filepath.Join("cmd", "nana", "main.go"),
		"prompts",
		"skills",
		"templates",
	}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			return true
		}
	}
	return false
}
