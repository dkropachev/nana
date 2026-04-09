package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Yeachan-Heo/nana/internal/nativelegacy"
)

const legacySparkShellEnv = "NANA_SPARKSHELL_LEGACY_BINARY"

func legacyArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	default:
		return runtime.GOARCH
	}
}

func main() {
	runner := nativelegacy.New(os.Args[0], os.Args[1:])

	if binary := runner.ResolveEnvBinary(legacySparkShellEnv); binary != "" {
		nativelegacy.ExitForError(runner.RunBinary(binary))
		return
	}

	if runner.RepoRoot == "" {
		fmt.Fprintln(os.Stderr, "nana-sparkshell: repo root not found; set NANA_SPARKSHELL_LEGACY_BINARY or run from a repo checkout")
		os.Exit(1)
	}

	name := "nana-sparkshell"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	if binary := runner.ResolveExisting(
		filepath.Join(runner.RepoRoot, "bin", "native", fmt.Sprintf("%s-%s-musl", runtime.GOOS, legacyArch()), name),
		filepath.Join(runner.RepoRoot, "bin", "native", fmt.Sprintf("%s-%s-glibc", runtime.GOOS, legacyArch()), name),
		filepath.Join(runner.RepoRoot, "bin", "native", fmt.Sprintf("%s-%s", runtime.GOOS, legacyArch()), name),
		filepath.Join(runner.RepoRoot, "target", "release", name),
		filepath.Join(runner.RepoRoot, "native", "nana-sparkshell", "target", "release", name),
	); binary != "" {
		nativelegacy.ExitForError(runner.RunBinary(binary))
		return
	}

	manifestPath := filepath.Join(runner.RepoRoot, "crates", "nana-sparkshell", "Cargo.toml")
	if _, err := os.Stat(manifestPath); err == nil {
		nativelegacy.ExitForError(
			runner.RunCommand("cargo", "run", "--quiet", "--manifest-path", manifestPath, "--"),
		)
		return
	}

	fmt.Fprintln(os.Stderr, "nana-sparkshell: no legacy binary or Cargo manifest found")
	os.Exit(1)
}
