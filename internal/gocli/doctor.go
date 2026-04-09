package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type doctorCheck struct {
	Name    string
	Status  string
	Message string
}

func Doctor(cwd string, repoRoot string) error {
	scope, source := resolveDoctorScope(cwd)
	paths := resolveDoctorPaths(cwd, scope)

	fmt.Fprintln(os.Stdout, "nana doctor")
	fmt.Fprintln(os.Stdout, "==================")
	fmt.Fprintln(os.Stdout)
	if source == "persisted" {
		fmt.Fprintf(os.Stdout, "Resolved setup scope: %s (from .nana/setup-scope.json)\n\n", scope)
	} else {
		fmt.Fprintf(os.Stdout, "Resolved setup scope: %s\n\n", scope)
	}

	checks := []doctorCheck{
		checkCodexCLI(),
		checkNodeVersion(),
		checkExploreHarness(repoRoot),
		checkDirectory("Codex home", paths.codexHomeDir),
		checkConfig(paths.configPath),
		checkExploreRouting(paths.configPath),
		checkPrompts(paths.promptsDir),
		checkSkills(paths.skillsDir),
	}
	if scope == "user" {
		checks = append(checks, checkLegacySkillRootOverlap())
	}
	checks = append(checks,
		checkAgentsMD(scope, cwd, paths.codexHomeDir),
		checkDirectory("State dir", BaseStateDir(cwd)),
		checkMcpServers(paths.configPath),
	)

	passCount, warnCount, failCount := 0, 0, 0
	for _, check := range checks {
		icon := "[OK]"
		switch check.Status {
		case "warn":
			icon = "[!!]"
			warnCount++
		case "fail":
			icon = "[XX]"
			failCount++
		default:
			passCount++
		}
		fmt.Fprintf(os.Stdout, "  %s %s: %s\n", icon, check.Name, check.Message)
	}

	fmt.Fprintf(os.Stdout, "\nResults: %d passed, %d warnings, %d failed\n", passCount, warnCount, failCount)
	if failCount > 0 {
		fmt.Fprintln(os.Stdout, "\nRun \"nana setup\" to fix installation issues.")
	} else if warnCount > 0 {
		fmt.Fprintln(os.Stdout, "\nRun \"nana setup --force\" to refresh all components.")
	} else {
		fmt.Fprintln(os.Stdout, "\nAll checks passed! nana is ready.")
	}
	return nil
}

type teamDoctorIssue struct {
	Code     string
	Message  string
	Severity string
}

func DoctorTeam(cwd string) (bool, error) {
	fmt.Fprintln(os.Stdout, "nana doctor --team")
	fmt.Fprintln(os.Stdout, "=========================")
	fmt.Fprintln(os.Stdout)

	issues, err := collectTeamDoctorIssues(cwd)
	if err != nil {
		return false, err
	}
	if len(issues) == 0 {
		fmt.Fprintln(os.Stdout, "  [OK] team diagnostics: no issues")
		fmt.Fprintln(os.Stdout, "\nAll team checks passed.")
		return false, nil
	}

	failureCount := 0
	warningCount := 0
	for _, issue := range issues {
		icon := "[XX]"
		if issue.Severity == "warn" {
			icon = "[!!]"
			warningCount++
		} else {
			failureCount++
		}
		fmt.Fprintf(os.Stdout, "  %s %s: %s\n", icon, issue.Code, issue.Message)
	}
	fmt.Fprintf(os.Stdout, "\nResults: %d warnings, %d failed\n", warningCount, failureCount)
	return failureCount > 0, nil
}

func resolveDoctorScope(cwd string) (string, string) {
	scopePath := filepath.Join(cwd, ".nana", "setup-scope.json")
	content, err := os.ReadFile(scopePath)
	if err != nil {
		return "user", "default"
	}
	switch string(content) {
	case `{"scope":"project"}`, "{\n  \"scope\": \"project\"\n}", "{\n  \"scope\": \"project-local\"\n}":
		if strings.Contains(string(content), "project-local") {
			return "project", "persisted"
		}
		return "project", "persisted"
	}
	if strings.Contains(string(content), `"scope":"project"`) || strings.Contains(string(content), `"scope": "project"`) {
		return "project", "persisted"
	}
	if strings.Contains(string(content), `"scope":"project-local"`) || strings.Contains(string(content), `"scope": "project-local"`) {
		return "project", "persisted"
	}
	if strings.Contains(string(content), `"scope":"user"`) || strings.Contains(string(content), `"scope": "user"`) {
		return "user", "persisted"
	}
	return "user", "default"
}

type doctorPaths struct {
	codexHomeDir string
	configPath   string
	promptsDir   string
	skillsDir    string
}

func resolveDoctorPaths(cwd string, scope string) doctorPaths {
	if scope == "project" {
		codexHomeDir := filepath.Join(cwd, ".codex")
		return doctorPaths{
			codexHomeDir: codexHomeDir,
			configPath:   filepath.Join(codexHomeDir, "config.toml"),
			promptsDir:   filepath.Join(codexHomeDir, "prompts"),
			skillsDir:    filepath.Join(cwd, ".codex", "skills"),
		}
	}
	return doctorPaths{
		codexHomeDir: CodexHome(),
		configPath:   CodexConfigPath(),
		promptsDir:   filepath.Join(CodexHome(), "prompts"),
		skillsDir:    filepath.Join(CodexHome(), "skills"),
	}
}

func checkCodexCLI() doctorCheck {
	output, err := exec.Command("codex", "--version").CombinedOutput()
	if err != nil {
		return doctorCheck{Name: "Codex CLI", Status: "fail", Message: "not found - install from https://github.com/openai/codex"}
	}
	return doctorCheck{Name: "Codex CLI", Status: "pass", Message: fmt.Sprintf("installed (%s)", strings.TrimSpace(string(output)))}
}

func checkNodeVersion() doctorCheck {
	output, err := exec.Command("node", "--version").CombinedOutput()
	if err != nil {
		return doctorCheck{Name: "Node.js", Status: "warn", Message: "not found"}
	}
	version := strings.TrimSpace(string(output))
	if strings.HasPrefix(version, "v") {
		majorParts := strings.Split(strings.TrimPrefix(version, "v"), ".")
		if len(majorParts) > 0 {
			if majorParts[0] >= "20" {
				return doctorCheck{Name: "Node.js", Status: "pass", Message: version}
			}
		}
	}
	return doctorCheck{Name: "Node.js", Status: "warn", Message: version}
}

func checkExploreHarness(repoRoot string) doctorCheck {
	override := strings.TrimSpace(os.Getenv("NANA_EXPLORE_BIN"))
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return doctorCheck{Name: "Explore Harness", Status: "pass", Message: fmt.Sprintf("NANA_EXPLORE_BIN configured (%s)", override)}
		}
		if repoRoot != "" {
			if _, err := os.Stat(filepath.Join(repoRoot, override)); err == nil {
				return doctorCheck{Name: "Explore Harness", Status: "pass", Message: fmt.Sprintf("NANA_EXPLORE_BIN configured (%s)", override)}
			}
		}
		return doctorCheck{Name: "Explore Harness", Status: "warn", Message: fmt.Sprintf("NANA_EXPLORE_BIN is set but path was not found (%s)", override)}
	}

	if repoRoot != "" {
		meta := filepath.Join(repoRoot, "bin", "nana-explore-harness.meta.json")
		bin := filepath.Join(repoRoot, "bin", map[bool]string{true: "nana-explore-harness.exe", false: "nana-explore-harness"}[runtime.GOOS == "windows"])
		if _, err := os.Stat(meta); err == nil {
			if _, err := os.Stat(bin); err == nil {
				return doctorCheck{Name: "Explore Harness", Status: "pass", Message: fmt.Sprintf("ready (packaged native binary: %s)", bin)}
			}
		}
	}

	if _, err := exec.LookPath("go"); err == nil {
		return doctorCheck{Name: "Explore Harness", Status: "pass", Message: "ready (go available)"}
	}
	return doctorCheck{Name: "Explore Harness", Status: "warn", Message: "Go harness sources are packaged, but no compatible packaged prebuilt or go toolchain was found (install Go or set NANA_EXPLORE_BIN for nana explore)"}
}

func checkDirectory(name string, path string) doctorCheck {
	if _, err := os.Stat(path); err == nil {
		return doctorCheck{Name: name, Status: "pass", Message: path}
	}
	return doctorCheck{Name: name, Status: "warn", Message: fmt.Sprintf("%s (not created yet)", path)}
}

func checkConfig(configPath string) doctorCheck {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return doctorCheck{Name: "Config", Status: "warn", Message: "config.toml not found"}
	}
	text := string(content)
	if countTopLevelTable(text, "[tui]") > 1 {
		return doctorCheck{Name: "Config", Status: "fail", Message: "invalid config.toml (possible duplicate TOML table such as [tui])"}
	}
	if strings.Contains(text, "[mcp_servers.nana_") || strings.Contains(strings.ToLower(text), "managed by nana setup") || strings.Contains(text, "USE_NANA_") {
		return doctorCheck{Name: "Config", Status: "pass", Message: "config.toml has NANA entries"}
	}
	return doctorCheck{Name: "Config", Status: "warn", Message: "config.toml exists but no NANA entries yet (expected before first setup; run \"nana setup --force\" once)"}
}

func countTopLevelTable(content string, table string) int {
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == table {
			count++
		}
	}
	return count
}

func checkExploreRouting(configPath string) doctorCheck {
	envValue := strings.TrimSpace(os.Getenv("USE_NANA_EXPLORE_CMD"))
	if envValue != "" && !exploreRoutingEnabled(envValue) {
		return doctorCheck{Name: "Explore routing", Status: "warn", Message: "disabled by environment override; enable with USE_NANA_EXPLORE_CMD=1 (or remove the explicit opt-out)"}
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		return doctorCheck{Name: "Explore routing", Status: "pass", Message: "enabled by default (config.toml not found yet)"}
	}
	text := string(content)
	if strings.Contains(text, `USE_NANA_EXPLORE_CMD = "off"`) || strings.Contains(text, `USE_NANA_EXPLORE_CMD = "0"`) || strings.Contains(text, `USE_NANA_EXPLORE_CMD = "false"`) {
		return doctorCheck{Name: "Explore routing", Status: "warn", Message: "disabled in config.toml [env]; set USE_NANA_EXPLORE_CMD = \"1\" to restore default explore-first routing"}
	}
	return doctorCheck{Name: "Explore routing", Status: "pass", Message: "enabled by default"}
}

func exploreRoutingEnabled(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "0" && value != "false" && value != "off" && value != "no"
}

func checkPrompts(dir string) doctorCheck {
	count := countFilesWithExt(dir, ".md")
	if count == 0 {
		return doctorCheck{Name: "Prompts", Status: "warn", Message: "prompts directory not found"}
	}
	if count >= 25 {
		return doctorCheck{Name: "Prompts", Status: "pass", Message: fmt.Sprintf("%d agent prompts installed", count)}
	}
	return doctorCheck{Name: "Prompts", Status: "warn", Message: fmt.Sprintf("%d prompts (expected >= 25)", count)}
}

func checkSkills(dir string) doctorCheck {
	count := countSkillDirs(dir)
	if count == 0 {
		return doctorCheck{Name: "Skills", Status: "warn", Message: "skills directory not found"}
	}
	if count >= 30 {
		return doctorCheck{Name: "Skills", Status: "pass", Message: fmt.Sprintf("%d skills installed", count)}
	}
	return doctorCheck{Name: "Skills", Status: "warn", Message: fmt.Sprintf("%d skills (expected >= 30)", count)}
}

func countFilesWithExt(dir string, ext string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ext) {
			count++
		}
	}
	return count
}

func countSkillDirs(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, entry.Name(), "SKILL.md")); err == nil {
			count++
		}
	}
	return count
}

func checkLegacySkillRootOverlap() doctorCheck {
	canonicalDir := filepath.Join(CodexHome(), "skills")
	legacyDir := filepath.Join(homeDir(), ".agents", "skills")
	if _, err := os.Stat(legacyDir); err != nil {
		return doctorCheck{Name: "Legacy skill roots", Status: "pass", Message: "no ~/.agents/skills overlap detected"}
	}

	canonicalResolved, canonicalErr := filepath.EvalSymlinks(canonicalDir)
	legacyResolved, legacyErr := filepath.EvalSymlinks(legacyDir)
	if canonicalErr == nil && legacyErr == nil && canonicalResolved == legacyResolved {
		return doctorCheck{Name: "Legacy skill roots", Status: "pass", Message: fmt.Sprintf("~/.agents/skills links to canonical %s; treating both paths as one shared skill root", canonicalDir)}
	}

	canonicalSkills := readSkillHashes(canonicalDir)
	legacySkills := readSkillHashes(legacyDir)
	overlap := 0
	mismatch := 0
	for name, hash := range canonicalSkills {
		if legacyHash, ok := legacySkills[name]; ok {
			overlap++
			if legacyHash != hash {
				mismatch++
			}
		}
	}
	if overlap == 0 {
		return doctorCheck{Name: "Legacy skill roots", Status: "warn", Message: fmt.Sprintf("legacy ~/.agents/skills still exists (%d skills) alongside canonical %s; remove or archive it if Codex shows duplicate entries", len(legacySkills), canonicalDir)}
	}
	extra := ""
	if mismatch > 0 {
		extra = fmt.Sprintf("; %d differ in SKILL.md content", mismatch)
	}
	return doctorCheck{Name: "Legacy skill roots", Status: "warn", Message: fmt.Sprintf("%d overlapping skill names between %s and %s%s; Codex Enable/Disable Skills may show duplicates until ~/.agents/skills is cleaned up", overlap, canonicalDir, legacyDir, extra)}
}

func readSkillHashes(root string) map[string]string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return map[string]string{}
	}
	result := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, entry.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		result[entry.Name()] = string(content)
	}
	return result
}

func checkAgentsMD(scope string, cwd string, codexHomeDir string) doctorCheck {
	if scope == "user" {
		path := filepath.Join(codexHomeDir, "AGENTS.md")
		if _, err := os.Stat(path); err == nil {
			return doctorCheck{Name: "AGENTS.md", Status: "pass", Message: fmt.Sprintf("found in %s", path)}
		}
		return doctorCheck{Name: "AGENTS.md", Status: "warn", Message: fmt.Sprintf("not found in %s (run nana setup --scope user)", path)}
	}
	path := filepath.Join(cwd, "AGENTS.md")
	if _, err := os.Stat(path); err == nil {
		return doctorCheck{Name: "AGENTS.md", Status: "pass", Message: "found in project root"}
	}
	return doctorCheck{Name: "AGENTS.md", Status: "warn", Message: "not found in project root (run nana agents-init . or nana setup --scope project)"}
}

func checkMcpServers(configPath string) doctorCheck {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return doctorCheck{Name: "MCP Servers", Status: "warn", Message: "config.toml not found"}
	}
	text := string(content)
	mcpCount := strings.Count(text, "[mcp_servers.")
	if mcpCount == 0 {
		if strings.Contains(text, "USE_NANA_") || strings.Contains(text, "[agents]") {
			return doctorCheck{Name: "MCP Servers", Status: "pass", Message: "no external MCP servers configured (current setup)"}
		}
		return doctorCheck{Name: "MCP Servers", Status: "warn", Message: "no MCP servers configured"}
	}
	if strings.Contains(text, "nana_state") || strings.Contains(text, "nana_memory") {
		return doctorCheck{Name: "MCP Servers", Status: "pass", Message: fmt.Sprintf("%d servers configured (NANA present)", mcpCount)}
	}
	return doctorCheck{Name: "MCP Servers", Status: "pass", Message: fmt.Sprintf("%d servers configured", mcpCount)}
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}

func collectTeamDoctorIssues(cwd string) ([]teamDoctorIssue, error) {
	stateDir := BaseStateDir(cwd)
	teamsRoot := filepath.Join(stateDir, "team")
	now := time.Now()
	lagThreshold := time.Minute
	shutdownThreshold := 30 * time.Second
	leaderStaleThreshold := 3 * time.Minute

	teamEntries, err := os.ReadDir(teamsRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	tmuxSessions, tmuxUnavailable := listTeamTmuxSessions()
	knownTeamSessions := map[string]bool{}
	var issues []teamDoctorIssue

	for _, entry := range teamEntries {
		if !entry.IsDir() {
			continue
		}
		teamName := entry.Name()
		teamDir := filepath.Join(teamsRoot, teamName)
		tmuxSession := "nana-team-" + teamName

		for _, configName := range []string{"manifest.v2.json", "config.json"} {
			path := filepath.Join(teamDir, configName)
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var parsed map[string]any
			if json.Unmarshal(content, &parsed) == nil {
				if value, ok := parsed["tmux_session"].(string); ok && strings.TrimSpace(value) != "" {
					tmuxSession = value
					break
				}
			}
		}

		knownTeamSessions[tmuxSession] = true
		if !tmuxUnavailable && !tmuxSessions[tmuxSession] {
			issues = append(issues, teamDoctorIssue{
				Code:     "resume_blocker",
				Message:  fmt.Sprintf("%s references missing tmux session %s", teamName, tmuxSession),
				Severity: "fail",
			})
		}

		workersRoot := filepath.Join(teamDir, "workers")
		workers, _ := os.ReadDir(workersRoot)
		for _, worker := range workers {
			if !worker.IsDir() {
				continue
			}
			workerDir := filepath.Join(workersRoot, worker.Name())
			statusPath := filepath.Join(workerDir, "status.json")
			heartbeatPath := filepath.Join(workerDir, "heartbeat.json")
			shutdownReqPath := filepath.Join(workerDir, "shutdown-request.json")
			shutdownAckPath := filepath.Join(workerDir, "shutdown-ack.json")

			if fileExists(statusPath) && fileExists(heartbeatPath) {
				statusRaw, statusErr := os.ReadFile(statusPath)
				hbRaw, hbErr := os.ReadFile(heartbeatPath)
				if statusErr == nil && hbErr == nil {
					var status map[string]any
					var heartbeat map[string]any
					if json.Unmarshal(statusRaw, &status) == nil && json.Unmarshal(hbRaw, &heartbeat) == nil {
						state, _ := status["state"].(string)
						lastTurn, _ := heartbeat["last_turn_at"].(string)
						if state == "working" {
							if ts, err := time.Parse(time.RFC3339, lastTurn); err == nil && now.Sub(ts) > lagThreshold {
								issues = append(issues, teamDoctorIssue{
									Code:     "delayed_status_lag",
									Message:  fmt.Sprintf("%s/%s working with stale heartbeat", teamName, worker.Name()),
									Severity: "fail",
								})
							}
						}
					}
				}
			}

			if fileExists(shutdownReqPath) && !fileExists(shutdownAckPath) {
				content, err := os.ReadFile(shutdownReqPath)
				if err == nil {
					var parsed map[string]any
					if json.Unmarshal(content, &parsed) == nil {
						if requestedAt, ok := parsed["requested_at"].(string); ok {
							if ts, err := time.Parse(time.RFC3339, requestedAt); err == nil && now.Sub(ts) > shutdownThreshold {
								issues = append(issues, teamDoctorIssue{
									Code:     "slow_shutdown",
									Message:  fmt.Sprintf("%s/%s has stale shutdown request without ack", teamName, worker.Name()),
									Severity: "fail",
								})
							}
						}
					}
				}
			}
		}
	}

	if teamLeaderIsStale(stateDir, leaderStaleThreshold, now) && !tmuxUnavailable {
		for session := range tmuxSessions {
			if knownTeamSessions[session] {
				issues = append(issues, teamDoctorIssue{
					Code:     "stale_leader",
					Message:  fmt.Sprintf("%s has active tmux session but leader has no recent activity", strings.TrimPrefix(session, "nana-team-")),
					Severity: "fail",
				})
			}
		}
	}

	if !tmuxUnavailable {
		for session := range tmuxSessions {
			if !knownTeamSessions[session] {
				issues = append(issues, teamDoctorIssue{
					Code:     "orphan_tmux_session",
					Message:  fmt.Sprintf("%s exists without matching team state (possibly external project)", session),
					Severity: "warn",
				})
			}
		}
	}

	return dedupeTeamIssues(issues), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func listTeamTmuxSessions() (map[string]bool, bool) {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.ToLower(string(output))
		if strings.Contains(text, "no server running") || strings.Contains(text, "failed to connect to server") {
			return map[string]bool{}, false
		}
		return map[string]bool{}, true
	}
	sessions := map[string]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nana-team-") {
			sessions[line] = true
		}
	}
	return sessions, false
}

func teamLeaderIsStale(stateDir string, threshold time.Duration, now time.Time) bool {
	latest := time.Time{}
	for _, path := range []string{
		filepath.Join(stateDir, "hud-state.json"),
		filepath.Join(stateDir, "leader-runtime-activity.json"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed map[string]any
		if json.Unmarshal(content, &parsed) != nil {
			continue
		}
		for _, key := range []string{"last_activity_at", "last_turn_at"} {
			if raw, ok := parsed[key].(string); ok {
				if ts, err := time.Parse(time.RFC3339, raw); err == nil && ts.After(latest) {
					latest = ts
				}
			}
		}
	}
	if latest.IsZero() {
		return false
	}
	return now.Sub(latest) > threshold
}

func dedupeTeamIssues(issues []teamDoctorIssue) []teamDoctorIssue {
	seen := map[string]bool{}
	out := make([]teamDoctorIssue, 0, len(issues))
	for _, issue := range issues {
		key := issue.Code + ":" + issue.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, issue)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code == out[j].Code {
			return out[i].Message < out[j].Message
		}
		return out[i].Code < out[j].Code
	})
	return out
}
