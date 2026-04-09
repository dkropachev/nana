package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Yeachan-Heo/nana/internal/nativelegacy"
)

const legacyExploreEnv = "NANA_EXPLORE_LEGACY_BINARY"

func main() {
	runner := nativelegacy.New(os.Args[0], os.Args[1:])

	if binary := runner.ResolveEnvBinary(legacyExploreEnv); binary != "" {
		nativelegacy.ExitForError(runner.RunBinary(binary))
		return
	}

	if runner.RepoRoot == "" {
		fmt.Fprintln(os.Stderr, "nana-explore-harness: repo root not found; set NANA_EXPLORE_LEGACY_BINARY or run from a repo checkout")
		os.Exit(1)
	}

	name := "nana-explore-harness"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if binary := runner.ResolveExisting(
		filepath.Join(runner.RepoRoot, "bin", name),
		filepath.Join(runner.RepoRoot, "target", "release", name),
		filepath.Join(runner.RepoRoot, "target", "debug", name),
	); binary != "" {
		nativelegacy.ExitForError(runner.RunBinary(binary))
		return
	}

	manifestPath := filepath.Join(runner.RepoRoot, "crates", "nana-explore", "Cargo.toml")
	if _, err := os.Stat(manifestPath); err == nil {
		nativelegacy.ExitForError(
			runner.RunCommand("cargo", "run", "--quiet", "--manifest-path", manifestPath, "--"),
		)
		return
	}

	fmt.Fprintln(os.Stderr, "nana-explore-harness: no legacy binary or Cargo manifest found")
	os.Exit(1)
}
