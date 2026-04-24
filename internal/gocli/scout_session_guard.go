package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var scoutSessionSandboxFailureMarkers = []struct {
	needle string
	label  string
}{
	{
		needle: "bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted",
		label:  "loopback namespace setup denied",
	},
	{
		needle: "bwrap: setting up uid map: Permission denied",
		label:  "bubblewrap uid-map setup denied",
	},
}

func detectScoutSessionSandboxFailure(runtime scoutExecutionRuntime, alias string) error {
	checkpointPath := filepath.Join(runtime.StateDir, sanitizePathToken(alias)+"-checkpoint.json")
	checkpoint, err := readCodexStepCheckpoint(checkpointPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	sessionPath := strings.TrimSpace(checkpoint.SessionPath)
	if sessionPath == "" && strings.TrimSpace(checkpoint.SessionID) != "" {
		sessionPath = findCodexSessionPathByID(runtime.CodexHome, checkpoint.SessionID)
	}
	if strings.TrimSpace(sessionPath) == "" {
		return nil
	}
	content, err := os.ReadFile(sessionPath)
	if err != nil {
		return err
	}
	text := string(content)
	for _, marker := range scoutSessionSandboxFailureMarkers {
		if strings.Contains(text, marker.needle) {
			return fmt.Errorf("%s execution hit a local Codex sandbox failure (%s); session transcript: %s", alias, marker.label, sessionPath)
		}
	}
	return nil
}
