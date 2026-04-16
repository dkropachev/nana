package gocli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ModeStateFileRef struct {
	Mode  string
	Path  string
	Scope string
}

type sessionEnvelope struct {
	SessionID string `json:"session_id"`
}

var canonicalModeStateFiles = map[string]string{
	"autopilot-state.json":      "autopilot",
	"autoresearch-state.json":   "autoresearch",
	"deep-interview-state.json": "deep-interview",
	"ecomode-state.json":        "ecomode",
	"pipeline-state.json":       "pipeline",
	"ralplan-state.json":        "ralplan",
	"team-state.json":           "team",
	"ultrawork-state.json":      "ultrawork",
	"ultraqa-state.json":        "ultraqa",
	"verify-loop-state.json":    "verify-loop",
}

func ReadCurrentSessionID(cwd string) string {
	sessionPath := filepath.Join(BaseStateDir(cwd), "session.json")
	content, err := os.ReadFile(sessionPath)
	if err != nil {
		return ""
	}
	var payload sessionEnvelope
	if err := json.Unmarshal(content, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.SessionID)
}

func listModeStateFilesInDir(dir string, scope string) ([]ModeStateFileRef, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	refs := make([]ModeStateFileRef, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		mode, ok := modeFromStateFileName(entry.Name())
		if !ok {
			continue
		}
		refs = append(refs, ModeStateFileRef{
			Mode:  mode,
			Path:  filepath.Join(dir, entry.Name()),
			Scope: scope,
		})
	}
	return refs, nil
}

func ListModeStateFilesWithScopePreference(cwd string) ([]ModeStateFileRef, error) {
	rootDir := BaseStateDir(cwd)
	readDirs := []string{rootDir}
	if sessionID := ReadCurrentSessionID(cwd); sessionID != "" {
		readDirs = append([]string{filepath.Join(rootDir, "sessions", sessionID)}, readDirs...)
	}

	preferred := map[string]ModeStateFileRef{}
	for i := len(readDirs) - 1; i >= 0; i-- {
		dir := readDirs[i]
		scope := "session"
		if dir == rootDir {
			scope = "root"
		}
		refs, err := listModeStateFilesInDir(dir, scope)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			preferred[ref.Mode] = ref
		}
	}

	refs := make([]ModeStateFileRef, 0, len(preferred))
	for _, ref := range preferred {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Mode < refs[j].Mode })
	return refs, nil
}

func ISOTimeNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func modeFromStateFileName(name string) (string, bool) {
	mode, ok := canonicalModeStateFiles[strings.TrimSpace(name)]
	return mode, ok
}
