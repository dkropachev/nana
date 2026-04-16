package gocli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type scoutExecutionRuntime struct {
	RepoPath    string
	ArtifactDir string
	CodexHome   string
	Cleanup     func()
}

func prepareScoutExecutionRuntime(repoPath string, artifactDir string, role string) (scoutExecutionRuntime, error) {
	runtime := scoutExecutionRuntime{
		RepoPath:    repoPath,
		ArtifactDir: artifactDir,
		Cleanup:     func() {},
	}
	if role != uiScoutRole {
		return runtime, nil
	}

	sandboxRoot, err := os.MkdirTemp("", "nana-ui-scout-*")
	if err != nil {
		return scoutExecutionRuntime{}, err
	}
	cleanup := func() {
		_ = os.RemoveAll(sandboxRoot)
	}

	sandboxRepoPath := filepath.Join(sandboxRoot, "repo")
	if err := copyUIScoutSandboxRepo(repoPath, sandboxRepoPath); err != nil {
		cleanup()
		return scoutExecutionRuntime{}, err
	}

	relativeArtifactDir, err := filepath.Rel(repoPath, artifactDir)
	if err != nil || relativeArtifactDir == "." || pathEscapesRoot(relativeArtifactDir) {
		relativeArtifactDir = filepath.Join(".nana", scoutArtifactRoot(role), filepath.Base(artifactDir))
	}

	scopedCodexHome, err := ensureScopedCodexHome(ResolveCodexHomeForLaunch(repoPath), filepath.Join(sandboxRoot, "codex-home"))
	if err != nil {
		cleanup()
		return scoutExecutionRuntime{}, err
	}

	return scoutExecutionRuntime{
		RepoPath:    sandboxRepoPath,
		ArtifactDir: filepath.Join(sandboxRepoPath, relativeArtifactDir),
		CodexHome:   scopedCodexHome,
		Cleanup:     cleanup,
	}, nil
}

func persistScoutExecutionArtifacts(runtime scoutExecutionRuntime, artifactDir string) error {
	if filepath.Clean(runtime.ArtifactDir) == filepath.Clean(artifactDir) {
		return nil
	}
	info, err := os.Stat(runtime.ArtifactDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("ui-scout artifact staging path is not a directory: %s", runtime.ArtifactDir)
	}
	return copyScoutTree(runtime.ArtifactDir, artifactDir, nil)
}

func copyUIScoutSandboxRepo(sourceRoot string, targetRoot string) error {
	return copyScoutTree(sourceRoot, targetRoot, shouldSkipUIScoutSandboxPath)
}

func copyScoutTree(sourceRoot string, targetRoot string, skip func(string, os.FileInfo) (bool, bool)) error {
	return filepath.Walk(sourceRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if relativePath == "." {
			return os.MkdirAll(targetRoot, 0o755)
		}
		if skip != nil {
			skipPath, skipDir := skip(relativePath, info)
			if skipDir && info.IsDir() {
				return filepath.SkipDir
			}
			if skipPath {
				return nil
			}
		}

		targetPath := filepath.Join(targetRoot, relativePath)
		mode := info.Mode()
		switch {
		case info.IsDir():
			return os.MkdirAll(targetPath, mode.Perm())
		case mode&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			return os.Symlink(linkTarget, targetPath)
		case mode.IsRegular():
			return copyScoutFile(path, targetPath, mode.Perm())
		default:
			return nil
		}
	})
}

func shouldSkipUIScoutSandboxPath(relativePath string, info os.FileInfo) (bool, bool) {
	normalized := filepath.ToSlash(relativePath)
	for _, blocked := range []string{
		".git",
		".codex",
		".nana/state",
		".nana/logs",
		".nana/improvements",
		".nana/enhancements",
		".nana/ui-findings",
	} {
		if normalized == blocked {
			return true, info.IsDir()
		}
		if strings.HasPrefix(normalized, blocked+"/") {
			return true, false
		}
	}
	return false, false
}

func copyScoutFile(sourcePath string, targetPath string, mode os.FileMode) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	return target.Close()
}

func pathEscapesRoot(relativePath string) bool {
	clean := filepath.Clean(strings.TrimSpace(relativePath))
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
