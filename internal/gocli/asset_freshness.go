package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dkropachev/nana/internal/gocliassets"
)

type managedAssetFreshness struct {
	Fingerprint   string
	AgentsFresh   bool
	PromptsFresh  bool
	SkillsFresh   bool
	AgentsPath    string
	PromptDetails []string
	SkillDetails  []string
	AgentsDetails []string
}

type managedAssetWarningState struct {
	Fingerprint string `json:"fingerprint"`
	WarnedAt    string `json:"warned_at"`
}

var (
	managedAssetWarningMemoMu sync.Mutex
	managedAssetWarningMemo   = map[string]string{}
	managedAssetWarningSeen   = map[string]bool{}
)

func evaluateManagedAssetFreshness(scope string, cwd string, codexHomeDir string, repoRoot string) (managedAssetFreshness, error) {
	agentsPath := resolveManagedAgentsPath(scope, cwd, codexHomeDir)
	expectedAgents, err := renderManagedAgentsContent(repoRoot, cwd, codexHomeDir, agentsPath)
	if err != nil {
		return managedAssetFreshness{}, err
	}
	expectedPrompts, err := readExpectedPromptAssets(repoRoot)
	if err != nil {
		return managedAssetFreshness{}, err
	}
	expectedSkills, err := readExpectedSkillAssets(repoRoot)
	if err != nil {
		return managedAssetFreshness{}, err
	}

	agentsFresh, agentsDetails := compareExpectedFile(agentsPath, expectedAgents)
	promptsFresh, promptDetails := compareExpectedFiles(filepath.Join(codexHomeDir, "prompts"), expectedPrompts)
	skillsFresh, skillDetails := compareExpectedFiles(filepath.Join(codexHomeDir, "skills"), expectedSkills)

	return managedAssetFreshness{
		Fingerprint:   managedAssetExpectedFingerprint(expectedAgents, expectedPrompts, expectedSkills),
		AgentsFresh:   agentsFresh,
		PromptsFresh:  promptsFresh,
		SkillsFresh:   skillsFresh,
		AgentsPath:    agentsPath,
		AgentsDetails: agentsDetails,
		PromptDetails: promptDetails,
		SkillDetails:  skillDetails,
	}, nil
}

func resolveManagedAgentsPath(scope string, cwd string, codexHomeDir string) string {
	if scope == "project" {
		return filepath.Join(cwd, "AGENTS.md")
	}
	return filepath.Join(codexHomeDir, "AGENTS.md")
}

func renderManagedAgentsContent(repoRoot string, cwd string, codexHomeDir string, targetPath string) (string, error) {
	content, err := readExpectedTemplateAgents(repoRoot)
	if err != nil {
		return "", err
	}
	if filepath.Clean(targetPath) == filepath.Clean(filepath.Join(cwd, "AGENTS.md")) {
		return addGeneratedAgentsMarker(strings.ReplaceAll(content, "~/.codex", "./.codex")), nil
	}
	if filepath.Clean(codexHomeDir) == filepath.Clean(filepath.Join(cwd, ".nana", "codex-home-investigate")) {
		content = strings.ReplaceAll(content, "~/.codex", "./.nana/codex-home-investigate")
	}
	return addGeneratedAgentsMarker(content), nil
}

func readExpectedTemplateAgents(repoRoot string) (string, error) {
	if root := expectedAssetsRoot(repoRoot); root != "" {
		content, err := os.ReadFile(filepath.Join(root, "templates", "AGENTS.md"))
		if err != nil {
			return "", err
		}
		return string(content), nil
	}
	templates, err := gocliassets.Templates()
	if err != nil {
		return "", err
	}
	content := templates["AGENTS.md"]
	if content == "" {
		return "", fmt.Errorf("embedded template AGENTS.md missing")
	}
	return content, nil
}

func readExpectedPromptAssets(repoRoot string) (map[string]string, error) {
	if root := expectedAssetsRoot(repoRoot); root != "" {
		return readTopLevelMarkdownFiles(filepath.Join(root, "prompts"))
	}
	prompts, err := gocliassets.Prompts()
	if err != nil {
		return nil, err
	}
	filtered := map[string]string{}
	for name, content := range prompts {
		if strings.HasSuffix(name, ".md") {
			filtered[name] = content
		}
	}
	return filtered, nil
}

func readExpectedSkillAssets(repoRoot string) (map[string]string, error) {
	if root := expectedAssetsRoot(repoRoot); root != "" {
		return readRecursiveFiles(filepath.Join(root, "skills"))
	}
	return gocliassets.Skills()
}

func expectedAssetsRoot(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	required := []string{
		filepath.Join("prompts", "executor.md"),
		filepath.Join("skills", "plan", "SKILL.md"),
		filepath.Join("templates", "AGENTS.md"),
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err != nil {
			return ""
		}
	}
	return repoRoot
}

func readTopLevelMarkdownFiles(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = string(content)
	}
	return files, nil
}

func readRecursiveFiles(root string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func compareExpectedFile(path string, expected string) (bool, []string) {
	actual, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, []string{"missing"}
		}
		return false, []string{err.Error()}
	}
	if string(actual) == expected {
		return true, nil
	}
	return false, []string{"content mismatch"}
}

func compareExpectedFiles(root string, expected map[string]string) (bool, []string) {
	if len(expected) == 0 {
		return true, nil
	}
	details := []string{}
	names := make([]string, 0, len(expected))
	for rel := range expected {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		path := filepath.Join(root, filepath.FromSlash(rel))
		actual, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				details = append(details, rel+" (missing)")
				continue
			}
			details = append(details, rel+" ("+err.Error()+")")
			continue
		}
		if string(actual) != expected[rel] {
			details = append(details, rel)
		}
	}
	return len(details) == 0, details
}

func managedAssetExpectedFingerprint(expectedAgents string, expectedPrompts map[string]string, expectedSkills map[string]string) string {
	parts := []string{"AGENTS.md", expectedAgents}
	promptKeys := sortedMapKeys(expectedPrompts)
	for _, key := range promptKeys {
		parts = append(parts, "prompt:"+key, expectedPrompts[key])
	}
	skillKeys := sortedMapKeys(expectedSkills)
	for _, key := range skillKeys {
		parts = append(parts, "skill:"+key, expectedSkills[key])
	}
	return sha256Hex(strings.Join(parts, "\n"))
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (m managedAssetFreshness) staleKinds() []string {
	kinds := []string{}
	if !m.AgentsFresh {
		kinds = append(kinds, "AGENTS.md")
	}
	if !m.PromptsFresh {
		kinds = append(kinds, "prompts")
	}
	if !m.SkillsFresh {
		kinds = append(kinds, "skills")
	}
	return kinds
}

func (m managedAssetFreshness) allFresh() bool {
	return m.AgentsFresh && m.PromptsFresh && m.SkillsFresh
}

func staleAssetMessage(details []string) string {
	if len(details) == 0 {
		return "up to date"
	}
	if len(details) == 1 {
		return details[0]
	}
	if len(details) == 2 {
		return details[0] + ", " + details[1]
	}
	return fmt.Sprintf("%s, %s (+%d more)", details[0], details[1], len(details)-2)
}

func checkManagedAgentsFreshness(scope string, cwd string, codexHomeDir string, repoRoot string) doctorCheck {
	freshness, err := evaluateManagedAssetFreshness(scope, cwd, codexHomeDir, repoRoot)
	if err != nil {
		return doctorCheck{Name: "AGENTS freshness", Status: "warn", Message: err.Error()}
	}
	if freshness.AgentsFresh {
		return doctorCheck{Name: "AGENTS freshness", Status: "pass", Message: "up to date"}
	}
	if isSetupGeneratedAgentsFile(freshness.AgentsPath) {
		return doctorCheck{
			Name:    "AGENTS freshness",
			Status:  "warn",
			Message: fmt.Sprintf("stale (%s); run %s", staleAssetMessage(freshness.AgentsDetails), setupForceFixCommand(scope)),
			Remediation: &doctorRemediation{
				Path:             freshness.AgentsPath,
				SafeAutomaticFix: fmt.Sprintf("yes — run `%s`", setupForceFixCommand(scope)),
				ManualFallback:   fmt.Sprintf("inspect %s, then run `%s` to refresh setup-generated AGENTS.md content", freshness.AgentsPath, setupForceFixCommand(scope)),
			},
		}
	}
	if fileExists(freshness.AgentsPath) {
		return doctorCheck{
			Name:    "AGENTS freshness",
			Status:  "warn",
			Message: fmt.Sprintf("stale (%s); manual merge required before force-refreshing %s", staleAssetMessage(freshness.AgentsDetails), freshness.AgentsPath),
			Remediation: manualDoctorRemediation(
				freshness.AgentsPath,
				fmt.Sprintf("back up and merge custom instructions in %s with current templates/AGENTS.md guidance before any force refresh", freshness.AgentsPath),
			),
		}
	}
	return doctorCheck{
		Name:        "AGENTS freshness",
		Status:      "warn",
		Message:     fmt.Sprintf("stale (%s); run %s", staleAssetMessage(freshness.AgentsDetails), setupFixCommand(scope)),
		Remediation: setupDoctorRemediation(scope, freshness.AgentsPath, fmt.Sprintf("create %s from templates/AGENTS.md or run `%s`", freshness.AgentsPath, setupFixCommand(scope))),
	}
}

func checkManagedPromptFreshness(scope string, cwd string, codexHomeDir string, repoRoot string) doctorCheck {
	freshness, err := evaluateManagedAssetFreshness(scope, cwd, codexHomeDir, repoRoot)
	if err != nil {
		return doctorCheck{Name: "Prompt freshness", Status: "warn", Message: err.Error()}
	}
	if freshness.PromptsFresh {
		return doctorCheck{Name: "Prompt freshness", Status: "pass", Message: "up to date"}
	}
	promptRoot := filepath.Join(codexHomeDir, "prompts")
	if conflictPath, conflictCause, ok := promptFreshnessPathConflict(promptRoot, repoRoot); ok {
		return doctorCheck{
			Name:    "Prompt freshness",
			Status:  "warn",
			Message: fmt.Sprintf("stale (%s); path conflict blocks automatic setup repair: %s", staleAssetMessage(freshness.PromptDetails), conflictCause),
			Remediation: manualDoctorRemediation(
				conflictPath,
				fmt.Sprintf("move the path conflict aside or recreate it as a directory, then run `%s`", setupFixCommand(scope)),
			),
		}
	}
	return doctorCheck{
		Name:        "Prompt freshness",
		Status:      "warn",
		Message:     fmt.Sprintf("stale (%s); run %s", staleAssetMessage(freshness.PromptDetails), setupFixCommand(scope)),
		Remediation: setupDoctorRemediation(scope, promptRoot, fmt.Sprintf("run `%s` to refresh setup-managed prompts", setupFixCommand(scope))),
	}
}

func checkManagedSkillFreshness(scope string, cwd string, codexHomeDir string, repoRoot string) doctorCheck {
	freshness, err := evaluateManagedAssetFreshness(scope, cwd, codexHomeDir, repoRoot)
	if err != nil {
		return doctorCheck{Name: "Skill freshness", Status: "warn", Message: err.Error()}
	}
	if freshness.SkillsFresh {
		return doctorCheck{Name: "Skill freshness", Status: "pass", Message: "up to date"}
	}
	skillRoot := filepath.Join(codexHomeDir, "skills")
	if conflictPath, conflictCause, ok := skillFreshnessPathConflict(skillRoot, repoRoot); ok {
		return doctorCheck{
			Name:    "Skill freshness",
			Status:  "warn",
			Message: fmt.Sprintf("stale (%s); path conflict blocks automatic setup repair: %s", staleAssetMessage(freshness.SkillDetails), conflictCause),
			Remediation: manualDoctorRemediation(
				conflictPath,
				fmt.Sprintf("move the path conflict aside or recreate it as a directory, then run `%s`", setupFixCommand(scope)),
			),
		}
	}
	return doctorCheck{
		Name:        "Skill freshness",
		Status:      "warn",
		Message:     fmt.Sprintf("stale (%s); run %s", staleAssetMessage(freshness.SkillDetails), setupFixCommand(scope)),
		Remediation: setupDoctorRemediation(scope, skillRoot, fmt.Sprintf("run `%s` to refresh setup-managed skills", setupFixCommand(scope))),
	}
}

func promptFreshnessPathConflict(root string, repoRoot string) (string, string, bool) {
	expected, err := readExpectedPromptAssets(repoRoot)
	if err != nil {
		return "", "", false
	}
	return managedAssetPathConflict(root, expected)
}

func skillFreshnessPathConflict(root string, repoRoot string) (string, string, bool) {
	expected, err := readExpectedSkillAssets(repoRoot)
	if err != nil {
		return "", "", false
	}
	return managedAssetPathConflict(root, expected)
}

func managedAssetPathConflict(root string, expected map[string]string) (string, string, bool) {
	info, err := os.Stat(root)
	if err == nil {
		if !info.IsDir() {
			return root, fmt.Sprintf("%s exists and is not a directory", root), true
		}
	} else if !os.IsNotExist(err) {
		return root, fmt.Sprintf("%s cannot be inspected: %v", root, err), true
	} else {
		return "", "", false
	}

	for _, rel := range sortedMapKeys(expected) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		parent := filepath.Dir(path)
		if conflictPath, conflictCause, ok := firstManagedAssetParentConflict(root, parent); ok {
			return conflictPath, conflictCause, true
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return path, fmt.Sprintf("%s cannot be inspected: %v", path, err), true
		}
		if info.IsDir() {
			return path, fmt.Sprintf("%s exists and is a directory", path), true
		}
	}
	return "", "", false
}

func firstManagedAssetParentConflict(root string, targetDir string) (string, string, bool) {
	rel, err := filepath.Rel(root, targetDir)
	if err != nil || rel == "." {
		return "", "", false
	}
	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Stat(current)
		if os.IsNotExist(err) {
			return "", "", false
		}
		if err != nil {
			return current, fmt.Sprintf("%s cannot be inspected: %v", current, err), true
		}
		if !info.IsDir() {
			return current, fmt.Sprintf("%s exists and is not a directory", current), true
		}
	}
	return "", "", false
}

func isSetupGeneratedAgentsFile(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return isNanaSetupGeneratedAgentsContent(string(content))
}

func assetWarningStatePath(cwd string) string {
	return filepath.Join(BaseStateDir(cwd), "asset-freshness-warning.json")
}

func maybeWarnManagedAssetDrift(cwd string, codexHomeDir string, repoRoot string) {
	scope, _ := resolveDoctorScope(cwd)
	freshness, err := evaluateManagedAssetFreshness(scope, cwd, codexHomeDir, repoRoot)
	if err != nil {
		return
	}
	statePath := assetWarningStatePath(cwd)
	if freshness.allFresh() {
		managedAssetWarningMemoMu.Lock()
		delete(managedAssetWarningMemo, statePath)
		delete(managedAssetWarningSeen, freshness.Fingerprint)
		managedAssetWarningMemoMu.Unlock()
		_ = os.Remove(statePath)
		return
	}
	managedAssetWarningMemoMu.Lock()
	if managedAssetWarningMemo[statePath] == freshness.Fingerprint || managedAssetWarningSeen[freshness.Fingerprint] {
		managedAssetWarningMemoMu.Unlock()
		return
	}
	managedAssetWarningMemoMu.Unlock()
	state, _ := readManagedAssetWarningState(statePath)
	if state.Fingerprint == freshness.Fingerprint {
		managedAssetWarningMemoMu.Lock()
		managedAssetWarningMemo[statePath] = freshness.Fingerprint
		managedAssetWarningSeen[freshness.Fingerprint] = true
		managedAssetWarningMemoMu.Unlock()
		return
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return
	}
	fmt.Fprint(os.Stderr, managedAssetDriftWarning(scope, freshness))
	managedAssetWarningMemoMu.Lock()
	managedAssetWarningMemo[statePath] = freshness.Fingerprint
	managedAssetWarningSeen[freshness.Fingerprint] = true
	managedAssetWarningMemoMu.Unlock()
	_ = writeManagedAssetWarningState(statePath, managedAssetWarningState{
		Fingerprint: freshness.Fingerprint,
		WarnedAt:    ISOTimeNow(),
	})
}

func managedAssetDriftWarning(scope string, freshness managedAssetFreshness) string {
	staleKinds := freshness.staleKinds()
	prefix := fmt.Sprintf("[nana] installed %s look stale; ", strings.Join(staleKinds, ", "))
	if freshness.AgentsFresh {
		return prefix + fmt.Sprintf("run `%s` to refresh setup-managed runtime assets.\n", setupFixCommand(scope))
	}
	if isSetupGeneratedAgentsFile(freshness.AgentsPath) {
		command := setupFixCommand(scope)
		if scope == "project" {
			command = setupForceFixCommand(scope)
		}
		return prefix + fmt.Sprintf("run `%s` to refresh setup-managed runtime assets.\n", command)
	}
	if fileExists(freshness.AgentsPath) {
		refreshableKinds := staleNonAgentsKinds(freshness)
		if len(refreshableKinds) == 0 {
			return prefix + fmt.Sprintf("custom AGENTS.md at %s requires a manual merge; run `nana doctor` for AGENTS-specific remediation.\n", freshness.AgentsPath)
		}
		return prefix + fmt.Sprintf("run `%s` to refresh %s. Custom AGENTS.md at %s requires a manual merge; run `nana doctor` for AGENTS-specific remediation.\n", setupFixCommand(scope), strings.Join(refreshableKinds, ", "), freshness.AgentsPath)
	}
	return prefix + fmt.Sprintf("run `%s` to refresh setup-managed runtime assets.\n", setupFixCommand(scope))
}

func staleNonAgentsKinds(freshness managedAssetFreshness) []string {
	kinds := []string{}
	if !freshness.PromptsFresh {
		kinds = append(kinds, "prompts")
	}
	if !freshness.SkillsFresh {
		kinds = append(kinds, "skills")
	}
	return kinds
}

func readManagedAssetWarningState(path string) (managedAssetWarningState, error) {
	var state managedAssetWarningState
	if err := readGithubJSON(path, &state); err != nil {
		return managedAssetWarningState{}, err
	}
	return state, nil
}

func writeManagedAssetWarningState(path string, state managedAssetWarningState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
