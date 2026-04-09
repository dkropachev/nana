package gocli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func LegacyCodexAuthPath(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".codex", "auth.json")
}

func ResolvedCodexAuthPath() string {
	return filepath.Join(CodexHome(), "auth.json")
}

func AuthPull() error {
	source := LegacyCodexAuthPath(os.Getenv("HOME"))
	target := ResolvedCodexAuthPath()

	if source == target {
		fmt.Fprintf(os.Stdout, "[nana] Credential source and target are the same: %s\n", target)
		return nil
	}

	sourceFile, err := os.Open(source)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("legacy Codex credentials not found at %s", source)
		}
		return err
	}
	defer sourceFile.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	targetFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer targetFile.Close()
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "[nana] Pulled Codex credentials from %s to %s\n", source, target)
	return nil
}
