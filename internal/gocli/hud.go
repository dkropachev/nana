package gocli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dkropachev/nana/internal/version"
)

const (
	HUDTMUXHeightLines = 2
)

const HUDUsage = "Usage:\n" +
	"  nana hud              Show current HUD state\n" +
	"  nana hud --watch      Poll every 1s with terminal clear\n" +
	"  nana hud --json       Output raw state as JSON\n" +
	"  nana hud --preset=X   Use preset: minimal, focused, full\n" +
	"  nana hud --tmux       Open HUD in a tmux split pane (auto-detects orientation)"

type HUDPreset string

const (
	HUDPresetMinimal HUDPreset = "minimal"
	HUDPresetFocused HUDPreset = "focused"
	HUDPresetFull    HUDPreset = "full"
)

type HUDFlags struct {
	Watch  bool
	JSON   bool
	TMUX   bool
	Preset HUDPreset
}

type HUDGitConfig struct {
	Display    string `json:"display"`
	RemoteName string `json:"remoteName"`
	RepoLabel  string `json:"repoLabel"`
}

type HUDConfig struct {
	Preset string       `json:"preset"`
	Git    HUDGitConfig `json:"git"`
}

type ResolvedHUDConfig struct {
	Preset HUDPreset
	Git    HUDGitConfig
}

type HUDModeState struct {
	Active             bool   `json:"active"`
	Iteration          int    `json:"iteration,omitempty"`
	MaxIterations      int    `json:"max_iterations,omitempty"`
	CurrentPhase       string `json:"current_phase,omitempty"`
	PlanningComplete   bool   `json:"planning_complete,omitempty"`
	InputLockActive    bool   `json:"input_lock_active,omitempty"`
	AgentCount         int    `json:"agent_count,omitempty"`
	TeamName           string `json:"team_name,omitempty"`
	ReinforcementCount int    `json:"reinforcement_count,omitempty"`
}

type hudDeepInterviewState struct {
	HUDModeState
	InputLock struct {
		Active bool `json:"active"`
	} `json:"input_lock"`
}

type HUDMetrics struct {
	TotalTurns          int      `json:"total_turns"`
	SessionTurns        int      `json:"session_turns"`
	LastActivity        string   `json:"last_activity"`
	SessionInputTokens  *int     `json:"session_input_tokens,omitempty"`
	SessionOutputTokens *int     `json:"session_output_tokens,omitempty"`
	SessionTotalTokens  *int     `json:"session_total_tokens,omitempty"`
	FiveHourLimitPct    *float64 `json:"five_hour_limit_pct,omitempty"`
	WeeklyLimitPct      *float64 `json:"weekly_limit_pct,omitempty"`
}

type HUDNotifyState struct {
	LastTurnAt      string `json:"last_turn_at"`
	TurnCount       int    `json:"turn_count"`
	LastAgentOutput string `json:"last_agent_output,omitempty"`
}

type HUDSessionState struct {
	SessionID string `json:"session_id"`
	StartedAt string `json:"started_at"`
}

type HUDAccountState struct {
	Active          string `json:"active,omitempty"`
	PendingActive   string `json:"pending_active,omitempty"`
	RestartRequired bool   `json:"restart_required,omitempty"`
	LastUsageResult string `json:"last_usage_result,omitempty"`
	Degraded        bool   `json:"degraded,omitempty"`
}

type HUDRenderContext struct {
	Version       string                 `json:"version,omitempty"`
	GitBranch     string                 `json:"gitBranch,omitempty"`
	VerifyLoop    *HUDModeState          `json:"verifyLoop,omitempty"`
	Ultrawork     *HUDModeState          `json:"ultrawork,omitempty"`
	Autopilot     *HUDModeState          `json:"autopilot,omitempty"`
	Ralplan       *HUDModeState          `json:"ralplan,omitempty"`
	DeepInterview *HUDModeState          `json:"deepInterview,omitempty"`
	Autoresearch  *HUDModeState          `json:"autoresearch,omitempty"`
	Ultraqa       *HUDModeState          `json:"ultraqa,omitempty"`
	Team          *HUDModeState          `json:"team,omitempty"`
	Account       *HUDAccountState       `json:"account,omitempty"`
	Metrics       *HUDMetrics            `json:"metrics,omitempty"`
	HUDNotify     *HUDNotifyState        `json:"hudNotify,omitempty"`
	Session       *HUDSessionState       `json:"session,omitempty"`
	Runtime       *RuntimeRecoveryStatus `json:"runtime,omitempty"`
}

func HUD(cwd string, executablePath string, args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(os.Stdout, HUDUsage)
		return nil
	}

	flags := parseHUDFlags(args)
	if flags.TMUX {
		return launchHUDTMUXPane(cwd, executablePath, flags)
	}
	if flags.Watch {
		return runHUDWatchMode(cwd, flags)
	}

	return renderHUDOnce(cwd, flags)
}

func parseHUDFlags(args []string) HUDFlags {
	flags := HUDFlags{Preset: HUDPresetFocused}
	for _, arg := range args {
		switch {
		case arg == "--watch" || arg == "-w":
			flags.Watch = true
		case arg == "--json":
			flags.JSON = true
		case arg == "--tmux":
			flags.TMUX = true
		case strings.HasPrefix(arg, "--preset="):
			if preset, ok := parseHUDPreset(strings.TrimPrefix(arg, "--preset=")); ok {
				flags.Preset = preset
			}
		}
	}
	return flags
}

func parseHUDPreset(value string) (HUDPreset, bool) {
	switch value {
	case string(HUDPresetMinimal):
		return HUDPresetMinimal, true
	case string(HUDPresetFocused):
		return HUDPresetFocused, true
	case string(HUDPresetFull):
		return HUDPresetFull, true
	default:
		return HUDPresetFocused, false
	}
}

func renderHUDOnce(cwd string, flags HUDFlags) error {
	config, err := readHUDConfig(cwd)
	if err != nil {
		return err
	}
	if flags.Preset != "" {
		config.Preset = flags.Preset
	}

	ctx, err := readAllHUDState(cwd, config)
	if err != nil {
		return err
	}

	if flags.JSON {
		payload, err := json.MarshalIndent(ctx, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(payload))
		return nil
	}

	fmt.Fprintln(os.Stdout, renderHUD(ctx, config.Preset))
	return nil
}

func runHUDWatchMode(cwd string, flags HUDFlags) error {
	if !stdoutIsTTY() && os.Getenv("CI") == "" {
		return errors.New("HUD watch mode requires a TTY")
	}

	stopSignals := make(chan os.Signal, 1)
	signal.Notify(stopSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopSignals)

	fmt.Fprint(os.Stdout, "\x1b[?25l")
	defer fmt.Fprint(os.Stdout, "\x1b[?25h\x1b[2J\x1b[H")

	render := func(first bool) error {
		if first {
			fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
		} else {
			fmt.Fprint(os.Stdout, "\x1b[H")
		}
		config, err := readHUDConfig(cwd)
		if err != nil {
			return err
		}
		if flags.Preset != "" {
			config.Preset = flags.Preset
		}
		ctx, err := readAllHUDState(cwd, config)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s\x1b[K\n\x1b[J", renderHUD(ctx, config.Preset))
		return nil
	}

	if err := render(true); err != nil {
		return err
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stopSignals:
			return nil
		case <-ticker.C:
			if err := render(false); err != nil {
				return err
			}
		}
	}
}

func stdoutIsTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func shellEscape(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'"
}

func buildHUDTMUXSplitArgs(cwd string, executablePath string, preset HUDPreset) []string {
	presetArg := ""
	if preset != "" && preset != HUDPresetFocused {
		presetArg = " --preset=" + string(preset)
	}
	command := shellEscape(executablePath) + " hud --watch" + presetArg
	return []string{"split-window", "-v", "-l", fmt.Sprintf("%d", HUDTMUXHeightLines), "-c", cwd, command}
}

func launchHUDTMUXPane(cwd string, executablePath string, flags HUDFlags) error {
	if strings.TrimSpace(os.Getenv("TMUX")) == "" {
		return errors.New("Not inside a tmux session. Start tmux first, then run: nana hud --tmux")
	}

	args := buildHUDTMUXSplitArgs(cwd, executablePath, flags.Preset)
	cmd := exec.Command("tmux", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errors.New("Failed to create tmux split. Ensure tmux is available.")
	}
	fmt.Fprintln(os.Stdout, "HUD launched in tmux pane below. Close with: Ctrl+C in that pane, or `tmux kill-pane -t bottom`")
	return nil
}

func readHUDConfig(cwd string) (ResolvedHUDConfig, error) {
	config := ResolvedHUDConfig{
		Preset: HUDPresetFocused,
		Git: HUDGitConfig{
			Display: "repo-branch",
		},
	}

	path := filepath.Join(cwd, ".nana", "hud-config.json")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return config, err
	}

	var raw HUDConfig
	if err := json.Unmarshal(content, &raw); err != nil {
		return config, nil
	}
	if preset, ok := parseHUDPreset(strings.TrimSpace(raw.Preset)); ok {
		config.Preset = preset
	}
	if raw.Git.Display == "branch" || raw.Git.Display == "repo-branch" {
		config.Git.Display = raw.Git.Display
	}
	config.Git.RemoteName = strings.TrimSpace(raw.Git.RemoteName)
	config.Git.RepoLabel = strings.TrimSpace(raw.Git.RepoLabel)
	return config, nil
}

func readAllHUDState(cwd string, config ResolvedHUDConfig) (HUDRenderContext, error) {
	return readAllHUDStateWithGitBranch(cwd, config, buildGitBranchLabel(cwd, config))
}

func readAllHUDStateWithGitBranch(cwd string, config ResolvedHUDConfig, gitBranch string) (HUDRenderContext, error) {
	verifyLoop, _ := readHUDModeState(cwd, "verify-loop")
	ultrawork, _ := readHUDModeState(cwd, "ultrawork")
	autopilot, _ := readHUDModeState(cwd, "autopilot")
	ralplan, _ := readHUDModeState(cwd, "ralplan")
	deepInterview, _ := readHUDDeepInterviewState(cwd)
	autoresearch, _ := readHUDModeState(cwd, "autoresearch")
	ultraqa, _ := readHUDModeState(cwd, "ultraqa")
	team, _ := readHUDModeState(cwd, "team")
	account, _ := readHUDAccountState(cwd)
	metrics, _ := readHUDMetrics(cwd)
	hudNotify, _ := readHUDNotifyState(cwd)
	session, _ := readHUDSessionState(cwd)
	runtimeRecovery, _ := BuildRuntimeRecoveryStatus(cwd)

	ctx := HUDRenderContext{
		Version:       readHUDVersion(),
		GitBranch:     gitBranch,
		VerifyLoop:    verifyLoop,
		Ultrawork:     ultrawork,
		Autopilot:     autopilot,
		Ralplan:       ralplan,
		DeepInterview: deepInterview,
		Autoresearch:  autoresearch,
		Ultraqa:       ultraqa,
		Team:          team,
		Account:       account,
		Metrics:       metrics,
		HUDNotify:     hudNotify,
		Session:       session,
		Runtime:       runtimeRecovery,
	}
	return ctx, nil
}

func readHUDVersion() string {
	if strings.TrimSpace(version.Version) == "" || version.Version == "dev" {
		return ""
	}
	return version.Version
}

func readHUDModeState(cwd string, mode string) (*HUDModeState, error) {
	refs, err := ListModeStateFilesWithScopePreference(cwd)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.Mode != mode {
			continue
		}
		var state HUDModeState
		if err := readHUDJSON(ref.Path, &state); err != nil {
			return nil, err
		}
		if !state.Active {
			return nil, nil
		}
		return &state, nil
	}
	return nil, nil
}

func readHUDDeepInterviewState(cwd string) (*HUDModeState, error) {
	refs, err := ListModeStateFilesWithScopePreference(cwd)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.Mode != "deep-interview" {
			continue
		}
		var state hudDeepInterviewState
		if err := readHUDJSON(ref.Path, &state); err != nil {
			return nil, err
		}
		if !state.Active {
			return nil, nil
		}
		normalized := state.HUDModeState
		if !normalized.InputLockActive && state.InputLock.Active {
			normalized.InputLockActive = true
		}
		return &normalized, nil
	}
	return nil, nil
}

func readHUDMetrics(cwd string) (*HUDMetrics, error) {
	path := filepath.Join(cwd, ".nana", "metrics.json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var metrics HUDMetrics
	if err := readHUDJSON(path, &metrics); err != nil {
		return nil, err
	}
	return &metrics, nil
}

func readHUDNotifyState(cwd string) (*HUDNotifyState, error) {
	path := filepath.Join(BaseStateDir(cwd), "hud-state.json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state HUDNotifyState
	if err := readHUDJSON(path, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func readHUDSessionState(cwd string) (*HUDSessionState, error) {
	path := filepath.Join(BaseStateDir(cwd), "session.json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state HUDSessionState
	if err := readHUDJSON(path, &state); err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.SessionID) == "" {
		return nil, nil
	}
	return &state, nil
}

func readHUDAccountState(cwd string) (*HUDAccountState, error) {
	codexHome := ResolveCodexHomeForLaunch(cwd)
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return nil, err
	}
	if len(registry.Accounts) == 0 {
		return nil, nil
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return nil, err
	}
	account := &HUDAccountState{
		Active:          strings.TrimSpace(state.Active),
		PendingActive:   strings.TrimSpace(state.PendingActive),
		RestartRequired: state.RestartRequired,
		Degraded:        state.Degraded,
	}
	if account.Active != "" {
		account.LastUsageResult = strings.TrimSpace(state.Accounts[account.Active].LastUsageResult)
	}
	if account.Active == "" && account.PendingActive == "" && !account.RestartRequired {
		return nil, nil
	}
	return account, nil
}

func readHUDJSON(path string, target interface{}) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func runGit(cwd string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func readGitBranch(cwd string) string {
	return runGit(cwd, "rev-parse", "--abbrev-ref", "HEAD")
}

func extractRepoName(remoteURL string) string {
	if remoteURL == "" {
		return ""
	}
	index := strings.LastIndexAny(remoteURL, ":/")
	if index < 0 || index+1 >= len(remoteURL) {
		return ""
	}
	repo := remoteURL[index+1:]
	repo = strings.TrimSuffix(repo, ".git")
	return repo
}

func readFirstRemoteName(cwd string) string {
	remotes := runGit(cwd, "remote")
	if remotes == "" {
		return ""
	}
	for _, line := range strings.Split(remotes, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func buildGitBranchLabel(cwd string, config ResolvedHUDConfig) string {
	branch := readGitBranch(cwd)
	if branch == "" {
		return ""
	}
	if config.Git.Display == "branch" {
		return branch
	}

	repoLabel := strings.TrimSpace(config.Git.RepoLabel)
	if repoLabel == "" && strings.TrimSpace(config.Git.RemoteName) != "" {
		repoLabel = extractRepoName(runGit(cwd, "remote", "get-url", config.Git.RemoteName))
	}
	if repoLabel == "" {
		repoLabel = extractRepoName(runGit(cwd, "remote", "get-url", "origin"))
	}
	if repoLabel == "" {
		if remoteName := readFirstRemoteName(cwd); remoteName != "" {
			repoLabel = extractRepoName(runGit(cwd, "remote", "get-url", remoteName))
		}
	}
	if repoLabel == "" {
		repoLabel = filepath.Base(runGit(cwd, "rev-parse", "--show-toplevel"))
	}
	if repoLabel == "" {
		return branch
	}
	return repoLabel + "/" + branch
}

func renderHUD(ctx HUDRenderContext, preset HUDPreset) string {
	label := "[NANA]"
	if ctx.Version != "" {
		label = "[NANA#" + strings.TrimPrefix(ctx.Version, "v") + "]"
	}

	parts := make([]string, 0, 16)
	appendIf := func(value string) {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, value)
		}
	}

	appendIf(ctx.GitBranch)
	appendIf(renderHUDVerifyLoop(ctx.VerifyLoop))
	if preset != HUDPresetMinimal {
		appendIf(renderHUDUltrawork(ctx.Ultrawork))
		appendIf(renderHUDAutopilot(ctx.Autopilot))
	}
	appendIf(renderHUDRalplan(ctx.Ralplan))
	appendIf(renderHUDDeepInterview(ctx.DeepInterview))
	if preset != HUDPresetMinimal {
		appendIf(renderHUDAutoresearch(ctx.Autoresearch))
		appendIf(renderHUDUltraqa(ctx.Ultraqa))
	}
	appendIf(renderHUDTeam(ctx.Team))
	appendIf(renderHUDRuntimeRecovery(ctx.Runtime))
	appendIf(renderHUDAccount(ctx.Account))
	appendIf(renderHUDTurns(ctx))
	if preset != HUDPresetMinimal {
		appendIf(renderHUDTokens(ctx))
		appendIf(renderHUDQuota(ctx))
		appendIf(renderHUDSessionDuration(ctx))
		appendIf(renderHUDLastActivity(ctx))
	}
	if preset == HUDPresetFull {
		appendIf(renderHUDTotalTurns(ctx))
	}

	if len(parts) == 0 {
		return label + " No active modes."
	}
	return label + " " + strings.Join(parts, " | ")
}

func renderHUDVerifyLoop(state *HUDModeState) string {
	if state == nil {
		return ""
	}
	return fmt.Sprintf("verify-loop:%d/%d", state.Iteration, state.MaxIterations)
}

func renderHUDUltrawork(state *HUDModeState) string {
	if state == nil {
		return ""
	}
	return "ultrawork"
}

func renderHUDAutopilot(state *HUDModeState) string {
	if state == nil {
		return ""
	}
	phase := strings.TrimSpace(state.CurrentPhase)
	if phase == "" {
		phase = "active"
	}
	return "autopilot:" + phase
}

func renderHUDRalplan(state *HUDModeState) string {
	if state == nil {
		return ""
	}
	if state.Iteration > 0 {
		max := "?"
		if state.PlanningComplete {
			max = fmt.Sprintf("%d", state.Iteration)
		}
		return fmt.Sprintf("ralplan:%d/%s", state.Iteration, max)
	}
	phase := strings.TrimSpace(state.CurrentPhase)
	if phase == "" {
		phase = "active"
	}
	return "ralplan:" + phase
}

func renderHUDDeepInterview(state *HUDModeState) string {
	if state == nil {
		return ""
	}
	phase := strings.TrimSpace(state.CurrentPhase)
	if phase == "" {
		phase = "active"
	}
	if state.InputLockActive {
		return "interview:" + phase + ":lock"
	}
	return "interview:" + phase
}

func renderHUDAutoresearch(state *HUDModeState) string {
	if state == nil {
		return ""
	}
	phase := strings.TrimSpace(state.CurrentPhase)
	if phase == "" {
		phase = "active"
	}
	return "research:" + phase
}

func renderHUDUltraqa(state *HUDModeState) string {
	if state == nil {
		return ""
	}
	phase := strings.TrimSpace(state.CurrentPhase)
	if phase == "" {
		phase = "active"
	}
	return "qa:" + phase
}

func renderHUDTeam(state *HUDModeState) string {
	if state == nil {
		return ""
	}
	if state.AgentCount > 0 {
		return fmt.Sprintf("team:%d workers", state.AgentCount)
	}
	if strings.TrimSpace(state.TeamName) != "" {
		return "team:" + strings.TrimSpace(state.TeamName)
	}
	return "team"
}

func renderHUDRuntimeRecovery(state *RuntimeRecoveryStatus) string {
	if state == nil || strings.TrimSpace(state.ActiveMode) == "" {
		return ""
	}
	mode := strings.TrimSpace(state.ActiveMode)
	if len(state.ActiveModes) > 1 {
		mode = fmt.Sprintf("%s+%d", mode, len(state.ActiveModes)-1)
	}
	parts := []string{"mode:" + mode}
	if strings.TrimSpace(state.StateFile) != "" {
		parts = append(parts, "state:"+strings.TrimSpace(state.StateFile))
	}
	if strings.TrimSpace(state.LatestArtifact) != "" {
		parts = append(parts, "artifact:"+strings.TrimSpace(state.LatestArtifact))
	}
	if strings.TrimSpace(state.CancelHint) != "" {
		parts = append(parts, "cancel:"+strings.TrimSpace(state.CancelHint))
	}
	return strings.Join(parts, " ")
}

func renderHUDAccount(state *HUDAccountState) string {
	if state == nil || strings.TrimSpace(state.Active) == "" {
		return ""
	}
	label := "account:" + strings.TrimSpace(state.Active)
	if strings.TrimSpace(state.PendingActive) != "" && strings.TrimSpace(state.PendingActive) != strings.TrimSpace(state.Active) {
		label += "->" + strings.TrimSpace(state.PendingActive)
	}
	switch strings.TrimSpace(state.LastUsageResult) {
	case accountUsageResultTransient, accountUsageResultPermanent:
		label += ":apierr"
	case accountUsageResultStale:
		label += ":stale"
	}
	if state.Degraded {
		label += ":degraded"
	}
	if state.RestartRequired {
		label += ":restart"
	}
	return label
}

func renderHUDTurns(ctx HUDRenderContext) string {
	if ctx.Metrics == nil {
		return ""
	}
	return fmt.Sprintf("turns:%d", ctx.Metrics.SessionTurns)
}

func renderHUDTokens(ctx HUDRenderContext) string {
	if ctx.Metrics == nil {
		return ""
	}
	total := 0
	if ctx.Metrics.SessionTotalTokens != nil {
		total = *ctx.Metrics.SessionTotalTokens
	} else if ctx.Metrics.SessionInputTokens != nil || ctx.Metrics.SessionOutputTokens != nil {
		if ctx.Metrics.SessionInputTokens != nil {
			total += *ctx.Metrics.SessionInputTokens
		}
		if ctx.Metrics.SessionOutputTokens != nil {
			total += *ctx.Metrics.SessionOutputTokens
		}
	}
	if total <= 0 {
		return ""
	}
	return "tokens:" + formatHUDTokenCount(total)
}

func renderHUDQuota(ctx HUDRenderContext) string {
	if ctx.Metrics == nil {
		return ""
	}
	parts := []string{}
	if ctx.Metrics.FiveHourLimitPct != nil && *ctx.Metrics.FiveHourLimitPct > 0 {
		parts = append(parts, fmt.Sprintf("5h:%d%%", int(*ctx.Metrics.FiveHourLimitPct+0.5)))
	}
	if ctx.Metrics.WeeklyLimitPct != nil && *ctx.Metrics.WeeklyLimitPct > 0 {
		parts = append(parts, fmt.Sprintf("wk:%d%%", int(*ctx.Metrics.WeeklyLimitPct+0.5)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "quota:" + strings.Join(parts, ",")
}

func renderHUDLastActivity(ctx HUDRenderContext) string {
	if ctx.HUDNotify == nil || strings.TrimSpace(ctx.HUDNotify.LastTurnAt) == "" {
		return ""
	}
	lastAt, err := time.Parse(time.RFC3339Nano, ctx.HUDNotify.LastTurnAt)
	if err != nil {
		lastAt, err = time.Parse(time.RFC3339, ctx.HUDNotify.LastTurnAt)
		if err != nil {
			return ""
		}
	}
	seconds := int(time.Since(lastAt).Round(time.Second).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return fmt.Sprintf("last:%ds ago", seconds)
	}
	return fmt.Sprintf("last:%dm ago", int((seconds+30)/60))
}

func renderHUDSessionDuration(ctx HUDRenderContext) string {
	if ctx.Session == nil || strings.TrimSpace(ctx.Session.StartedAt) == "" {
		return ""
	}
	startedAt, err := time.Parse(time.RFC3339Nano, ctx.Session.StartedAt)
	if err != nil {
		startedAt, err = time.Parse(time.RFC3339, ctx.Session.StartedAt)
		if err != nil {
			return ""
		}
	}
	seconds := int(time.Since(startedAt).Round(time.Second).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return fmt.Sprintf("session:%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("session:%dm", int((seconds+30)/60))
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	return fmt.Sprintf("session:%dh%dm", hours, minutes)
}

func renderHUDTotalTurns(ctx HUDRenderContext) string {
	if ctx.Metrics == nil || ctx.Metrics.TotalTurns <= 0 {
		return ""
	}
	return fmt.Sprintf("total-turns:%d", ctx.Metrics.TotalTurns)
}

func formatHUDTokenCount(value int) string {
	switch {
	case value >= 1000000:
		return fmt.Sprintf("%.1fM", float64(value)/1000000.0)
	case value >= 1000:
		return fmt.Sprintf("%.1fk", float64(value)/1000.0)
	default:
		return fmt.Sprintf("%d", value)
	}
}
