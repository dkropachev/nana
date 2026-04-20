package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const nanaCancelCommandHint = "$cancel"
const runtimeLatestArtifactFileName = "latest-artifact.json"

type RuntimeModeStatus struct {
	Mode      string `json:"mode"`
	StateFile string `json:"state_file"`
	Scope     string `json:"scope,omitempty"`
	Phase     string `json:"phase,omitempty"`
}

type RuntimeRecoveryStatus struct {
	ActiveMode     string              `json:"active_mode,omitempty"`
	ActiveModes    []RuntimeModeStatus `json:"active_modes,omitempty"`
	StateFile      string              `json:"state_file,omitempty"`
	StateScope     string              `json:"state_scope,omitempty"`
	LatestArtifact string              `json:"latest_artifact,omitempty"`
	CancelHint     string              `json:"cancel_hint,omitempty"`
	RecoveryHint   string              `json:"recovery_hint,omitempty"`
}

type runtimeArtifactPointer struct {
	Path      string `json:"path"`
	UpdatedAt string `json:"updated_at,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

var runtimeModeDisplayPriority = map[string]int{
	"verify-loop":    0,
	"autopilot":      1,
	"autoresearch":   2,
	"ultrawork":      3,
	"ultraqa":        4,
	"ralplan":        5,
	"deep-interview": 6,
	"pipeline":       7,
	"team":           8,
	"ecomode":        9,
}

func BuildRuntimeRecoveryStatus(cwd string) (*RuntimeRecoveryStatus, error) {
	refs, err := ListModeStateFilesWithScopePreference(cwd)
	if err != nil {
		return nil, err
	}

	activeModes := make([]RuntimeModeStatus, 0, len(refs))
	for _, ref := range refs {
		content, err := os.ReadFile(ref.Path)
		if err != nil {
			continue
		}
		var state map[string]any
		if err := json.Unmarshal(content, &state); err != nil {
			continue
		}
		active, _ := state["active"].(bool)
		if !active {
			continue
		}
		phase, _ := state["current_phase"].(string)
		activeModes = append(activeModes, RuntimeModeStatus{
			Mode:      ref.Mode,
			StateFile: relativeRuntimePath(cwd, ref.Path),
			Scope:     ref.Scope,
			Phase:     strings.TrimSpace(phase),
		})
	}
	if len(activeModes) == 0 {
		return nil, nil
	}

	sort.SliceStable(activeModes, func(i, j int) bool {
		left := runtimeModePriority(activeModes[i].Mode)
		right := runtimeModePriority(activeModes[j].Mode)
		if left != right {
			return left < right
		}
		return activeModes[i].Mode < activeModes[j].Mode
	})

	primary := activeModes[0]
	return &RuntimeRecoveryStatus{
		ActiveMode:     primary.Mode,
		ActiveModes:    activeModes,
		StateFile:      primary.StateFile,
		StateScope:     primary.Scope,
		LatestArtifact: latestRuntimeArtifactPath(cwd),
		CancelHint:     nanaCancelCommandHint,
		RecoveryHint:   "Run " + nanaCancelCommandHint + " to stop active NANA state safely; inspect details with `nana hud --json`.",
	}, nil
}

func runtimeModePriority(mode string) int {
	if priority, ok := runtimeModeDisplayPriority[mode]; ok {
		return priority
	}
	return len(runtimeModeDisplayPriority) + 1
}

func RecordRuntimeArtifact(cwd string, artifactPath string) error {
	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" {
		return nil
	}
	pointer := runtimeArtifactPointer{
		Path:      relativeRuntimePath(cwd, artifactPath),
		UpdatedAt: ISOTimeNow(),
		SessionID: ReadCurrentSessionID(cwd),
	}
	content, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return err
	}
	path := latestRuntimeArtifactPointerPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o644)
}

func recordRuntimeArtifactWrite(artifactPath string) {
	cwd, ok := runtimeArtifactWorkspaceRoot(artifactPath)
	if !ok {
		return
	}
	_ = RecordRuntimeArtifact(cwd, artifactPath)
}

func runtimeArtifactWorkspaceRoot(artifactPath string) (string, bool) {
	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(artifactPath))
	if strings.HasPrefix(clean, ".nana/logs/") {
		return ".", true
	}
	const marker = "/.nana/logs/"
	index := strings.Index(clean, marker)
	if index < 0 {
		return "", false
	}
	root := clean[:index]
	if root == "" {
		root = "/"
	}
	return filepath.FromSlash(root), true
}

func latestRuntimeArtifactPath(cwd string) string {
	sessionID := ReadCurrentSessionID(cwd)
	for _, path := range latestRuntimeArtifactPointerReadPaths(cwd, sessionID) {
		pointer, ok := readRuntimeArtifactPointer(path)
		if !ok {
			continue
		}
		pointerSessionID := strings.TrimSpace(pointer.SessionID)
		if sessionID != "" && pointerSessionID != "" && pointerSessionID != sessionID {
			continue
		}
		if sessionID != "" && path == legacyLatestRuntimeArtifactPointerPath(cwd) && pointerSessionID != sessionID {
			continue
		}
		return relativeRuntimePath(cwd, pointer.Path)
	}
	return ""
}

func latestRuntimeArtifactPointerPath(cwd string) string {
	if sessionID := ReadCurrentSessionID(cwd); sessionID != "" {
		return filepath.Join(BaseStateDir(cwd), "sessions", sessionID, runtimeLatestArtifactFileName)
	}
	return legacyLatestRuntimeArtifactPointerPath(cwd)
}

func latestRuntimeArtifactPointerReadPaths(cwd string, sessionID string) []string {
	legacyPath := legacyLatestRuntimeArtifactPointerPath(cwd)
	if sessionID == "" {
		return []string{legacyPath}
	}
	sessionPath := filepath.Join(BaseStateDir(cwd), "sessions", sessionID, runtimeLatestArtifactFileName)
	return []string{sessionPath, legacyPath}
}

func legacyLatestRuntimeArtifactPointerPath(cwd string) string {
	return filepath.Join(BaseStateDir(cwd), runtimeLatestArtifactFileName)
}

func readRuntimeArtifactPointer(path string) (runtimeArtifactPointer, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return runtimeArtifactPointer{}, false
	}
	var pointer runtimeArtifactPointer
	if err := json.Unmarshal(content, &pointer); err != nil {
		return runtimeArtifactPointer{}, false
	}
	return pointer, true
}

func relativeRuntimePath(cwd string, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	rel, err := filepath.Rel(cwd, path)
	if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return rel
	}
	return path
}
