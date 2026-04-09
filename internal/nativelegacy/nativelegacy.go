package nativelegacy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Yeachan-Heo/nana/internal/legacyshim"
)

type Runner struct {
	RepoRoot string
	Args     []string
}

func New(executablePath string, args []string) Runner {
	return Runner{
		RepoRoot: legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), executablePath),
		Args:     args,
	}
}

func (r Runner) ResolveEnvBinary(envName string) string {
	override := os.Getenv(envName)
	if override == "" {
		return ""
	}
	if filepath.IsAbs(override) {
		return override
	}
	if r.RepoRoot != "" {
		return filepath.Join(r.RepoRoot, override)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return override
	}
	return filepath.Join(cwd, override)
}

func (r Runner) ResolveExisting(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func (r Runner) RunBinary(binary string) error {
	cmd := exec.Command(binary, r.Args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	return cmd.Run()
}

func (r Runner) RunCommand(command string, args ...string) error {
	cmd := exec.Command(command, append(args, r.Args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	return cmd.Run()
}

func ExitForError(err error) {
	if err == nil {
		return
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	fmt.Fprintf(os.Stderr, "%v\n", err)
	os.Exit(1)
}
