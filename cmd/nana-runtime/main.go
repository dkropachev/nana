package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Yeachan-Heo/nana/internal/nativelegacy"
)

const legacyRuntimeEnv = "NANA_RUNTIME_LEGACY_BINARY"

func main() {
	runner := nativelegacy.New(os.Args[0], os.Args[1:])

	if binary := runner.ResolveEnvBinary(legacyRuntimeEnv); binary != "" {
		nativelegacy.ExitForError(runner.RunBinary(binary))
		return
	}

	if runner.RepoRoot == "" {
		fmt.Fprintln(os.Stderr, "nana-runtime: repo root not found; set NANA_RUNTIME_LEGACY_BINARY or run from a repo checkout")
		os.Exit(1)
	}

	if binary := runner.ResolveExisting(
		filepath.Join(runner.RepoRoot, "target", "debug", "nana-runtime"),
		filepath.Join(runner.RepoRoot, "target", "release", "nana-runtime"),
	); binary != "" {
		nativelegacy.ExitForError(runner.RunBinary(binary))
		return
	}

	manifestPath := filepath.Join(runner.RepoRoot, "crates", "nana-runtime", "Cargo.toml")
	if _, err := os.Stat(manifestPath); err == nil {
		nativelegacy.ExitForError(
			runner.RunCommand("cargo", "run", "--quiet", "--manifest-path", manifestPath, "--"),
		)
		return
	}

	fmt.Fprintln(os.Stderr, "nana-runtime: no legacy runtime binary or Cargo manifest found")
	os.Exit(1)
}
