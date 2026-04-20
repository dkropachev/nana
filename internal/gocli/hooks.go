package gocli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const HooksHelp = `Usage:
  nana hooks init       Create a sample executable hook scaffold
  nana hooks status     Show hook directory + discovered hooks
  nana hooks validate   Validate executable hooks
  nana hooks test       Dispatch synthetic turn-complete event to hooks

Notes:
  - This is the NANA extensibility surface for executable hooks under .nana/hooks/.
  - Hooks are enabled by default. Disable with NANA_HOOK_PLUGINS=0.
  - Legacy .mjs hooks are reported for migration but are not executed.
`

const sampleHookPluginPOSIX = `#!/bin/sh
set -eu

payload=$(cat)
case "$payload" in
  *'"event":"turn-complete"'*|*'"event": "turn-complete"'*)
    printf '%s\n' '{"ok":true,"reason":"ok","logs":[{"level":"info","message":"sample hook observed turn-complete","meta":{"source":"sample-hook"}}],"state":{"set":{"sample_seen":"true","last_event":"turn-complete"}}}'
    ;;
  *)
    printf '%s\n' '{"ok":true,"reason":"ignored"}'
    ;;
esac
`

const sampleHookPluginWindows = `@echo off
setlocal EnableExtensions
for /f "delims=" %%i in ('more') do set "PAYLOAD=!PAYLOAD!%%i"
echo {"ok":true,"reason":"ok","logs":[{"level":"info","message":"sample hook executed","meta":{"source":"sample-hook"}}]}
`

const (
	hookPluginTimeoutEnv       = "NANA_HOOK_PLUGIN_TIMEOUT_MS"
	hookPluginDefaultTimeoutMs = 1500
	hookPluginMinimumTimeoutMs = 100
	hookPluginMaximumTimeoutMs = 60000
	hookPluginCooldownEnv      = "NANA_HOOK_PLUGIN_COOLDOWN_MS"
	hookPluginDedupeEnv        = "NANA_HOOK_PLUGIN_DEDUPE_MS"
	hookPluginDefaultDedupeMs  = 60000
	hookPluginDefaultCooldown  = 15000
)

type hookDescriptor struct {
	Name   string
	Path   string
	Legacy bool
	Valid  bool
	Reason string
}

type hookEventEnvelope struct {
	SchemaVersion string         `json:"schema_version"`
	Event         string         `json:"event"`
	Timestamp     string         `json:"timestamp"`
	Source        string         `json:"source"`
	Context       map[string]any `json:"context"`
	SessionID     string         `json:"session_id,omitempty"`
	ThreadID      string         `json:"thread_id,omitempty"`
	TurnID        string         `json:"turn_id,omitempty"`
}

type hookRunnerRequest struct {
	Cwd                string                 `json:"cwd"`
	PluginID           string                 `json:"plugin_id"`
	PluginPath         string                 `json:"plugin_path"`
	Event              hookEventEnvelope      `json:"event"`
	SideEffectsEnabled bool                   `json:"side_effects_enabled"`
	StatePaths         map[string]string      `json:"state_paths,omitempty"`
	PluginStatePath    string                 `json:"plugin_state_path,omitempty"`
	PluginTMuxState    string                 `json:"plugin_tmux_state_path,omitempty"`
	Context            map[string]interface{} `json:"context,omitempty"`
}

type hookRunnerResult struct {
	OK          bool                   `json:"ok"`
	Plugin      string                 `json:"plugin,omitempty"`
	Reason      string                 `json:"reason"`
	Error       string                 `json:"error,omitempty"`
	Logs        []hookResultLog        `json:"logs,omitempty"`
	State       *hookResultState       `json:"state,omitempty"`
	TMUXActions []hookResultTmuxAction `json:"tmux_actions,omitempty"`
}

type hookResultLog struct {
	Level   string                 `json:"level"`
	Message string                 `json:"message"`
	Meta    map[string]interface{} `json:"meta,omitempty"`
}

type hookResultState struct {
	Set    map[string]interface{} `json:"set,omitempty"`
	Delete []string               `json:"delete,omitempty"`
}

type hookResultTmuxAction struct {
	PaneID     string `json:"pane_id,omitempty"`
	Session    string `json:"session_name,omitempty"`
	Text       string `json:"text"`
	Submit     *bool  `json:"submit,omitempty"`
	CooldownMs *int   `json:"cooldown_ms,omitempty"`
}

type hookTmuxState struct {
	LastSentAt int64            `json:"last_sent_at"`
	RecentKeys map[string]int64 `json:"recent_keys,omitempty"`
}

func Hooks(cwd string, repoRoot string, args []string) error {
	subcommand := "status"
	if len(args) > 0 {
		subcommand = args[0]
	}
	switch subcommand {
	case "help", "--help", "-h":
		fmt.Fprintln(os.Stdout, strings.TrimSpace(HooksHelp))
		return nil
	case "init":
		return hooksInit(cwd)
	case "status":
		return hooksStatus(cwd)
	case "validate":
		return hooksValidate(cwd)
	case "test":
		return hooksTest(cwd, repoRoot)
	default:
		return fmt.Errorf("unknown hooks subcommand: %s", subcommand)
	}
}

func hooksDir(cwd string) string {
	return filepath.Join(cwd, ".nana", "hooks")
}

func sampleHookPath(cwd string) string {
	name := "sample-hook.sh"
	if runtime.GOOS == "windows" {
		name = "sample-hook.cmd"
	}
	return filepath.Join(hooksDir(cwd), name)
}

func hooksEnabled() bool {
	return strings.TrimSpace(os.Getenv("NANA_HOOK_PLUGINS")) != "0"
}

func hooksInit(cwd string) error {
	dir := hooksDir(cwd)
	samplePath := sampleHookPath(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(samplePath); err == nil {
		fmt.Fprintf(os.Stdout, "hooks scaffold already exists: %s\n", samplePath)
		return nil
	}
	content := sampleHookPluginPOSIX
	mode := os.FileMode(0o755)
	if runtime.GOOS == "windows" {
		content = sampleHookPluginWindows
		mode = 0o644
	}
	if err := writeBytesWithChecksumGuard(samplePath, []byte(content), mode, false, nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Created %s\n", samplePath)
	fmt.Fprintln(os.Stdout, "Hooks are enabled by default. Disable with NANA_HOOK_PLUGINS=0.")
	return nil
}

func hooksStatus(cwd string) error {
	hooks, err := discoverHooks(cwd)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	validCount := 0
	legacyCount := 0
	for _, hook := range hooks {
		if hook.Legacy {
			legacyCount++
		}
		if hook.Valid {
			validCount++
		}
	}

	fmt.Fprintln(os.Stdout, "hooks status")
	fmt.Fprintln(os.Stdout, "-----------")
	fmt.Fprintf(os.Stdout, "Directory: %s\n", hooksDir(cwd))
	if hooksEnabled() {
		fmt.Fprintln(os.Stdout, "Hooks enabled: yes")
	} else {
		fmt.Fprintln(os.Stdout, "Hooks enabled: no (disabled with NANA_HOOK_PLUGINS=0)")
	}
	fmt.Fprintf(os.Stdout, "Executable hooks: %d\n", validCount)
	fmt.Fprintf(os.Stdout, "Legacy .mjs hooks: %d\n", legacyCount)
	for _, hook := range hooks {
		status := "invalid"
		if hook.Legacy {
			status = "legacy"
		} else if hook.Valid {
			status = "ok"
		}
		if hook.Reason != "" {
			fmt.Fprintf(os.Stdout, "- %s [%s] (%s)\n", hook.Name, status, hook.Reason)
		} else {
			fmt.Fprintf(os.Stdout, "- %s [%s]\n", hook.Name, status)
		}
	}
	return nil
}

func hooksValidate(cwd string) error {
	hooks, err := discoverHooks(cwd)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stdout, "No hooks found. Run: nana hooks init")
			return nil
		}
		return err
	}
	if len(hooks) == 0 {
		fmt.Fprintln(os.Stdout, "No hooks found. Run: nana hooks init")
		return nil
	}

	failed := 0
	for _, hook := range hooks {
		switch {
		case hook.Legacy:
			failed++
			fmt.Fprintf(os.Stdout, "✗ %s: legacy .mjs hooks are no longer executed; migrate to an executable hook\n", hook.Name)
		case hook.Valid:
			fmt.Fprintf(os.Stdout, "✓ %s\n", hook.Name)
		default:
			failed++
			reason := hook.Reason
			if reason == "" {
				reason = "not executable"
			}
			fmt.Fprintf(os.Stdout, "✗ %s: %s\n", hook.Name, reason)
		}
	}
	if failed > 0 {
		return fmt.Errorf("hooks validation failed (%d hook(s))", failed)
	}
	return nil
}

func hooksTest(cwd string, repoRoot string) error {
	_ = repoRoot
	hooks, err := discoverHooks(cwd)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	executableHooks := make([]hookDescriptor, 0, len(hooks))
	for _, hook := range hooks {
		if hook.Valid {
			executableHooks = append(executableHooks, hook)
		}
	}

	event := hookEventEnvelope{
		SchemaVersion: "1",
		Event:         "turn-complete",
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Source:        "native",
		Context: map[string]any{
			"reason": "nana-hooks-test",
		},
		SessionID: "nana-hooks-test",
		ThreadID:  fmt.Sprintf("thread-%d", time.Now().UnixMilli()),
		TurnID:    fmt.Sprintf("turn-%d", time.Now().UnixMilli()),
	}

	fmt.Fprintln(os.Stdout, "hooks test dispatch complete")
	fmt.Fprintf(os.Stdout, "hooks discovered: %d\n", len(hooks))
	fmt.Fprintf(os.Stdout, "executable hooks: %d\n", len(executableHooks))
	if hooksEnabled() {
		fmt.Fprintln(os.Stdout, "hooks enabled: yes")
	} else {
		fmt.Fprintln(os.Stdout, "hooks enabled: no (disabled with NANA_HOOK_PLUGINS=0)")
	}
	fmt.Fprintln(os.Stdout, "dispatch reason: ok")

	for _, hook := range executableHooks {
		result, runErr := runExecutableHook(cwd, hook, event, true)
		if runErr != nil {
			fmt.Fprintf(os.Stdout, "%s: error (%v)\n", hook.Name, runErr)
			continue
		}
		if result.OK {
			fmt.Fprintf(os.Stdout, "%s: ok\n", hook.Name)
		} else if result.Error != "" {
			fmt.Fprintf(os.Stdout, "%s: %s (%s)\n", hook.Name, result.Reason, result.Error)
		} else {
			fmt.Fprintf(os.Stdout, "%s: %s\n", hook.Name, result.Reason)
		}
	}

	logPath := filepath.Join(cwd, ".nana", "logs", fmt.Sprintf("hooks-%s.jsonl", time.Now().UTC().Format("2006-01-02")))
	if _, err := os.Stat(logPath); err == nil {
		fmt.Fprintf(os.Stdout, "log file: %s\n", logPath)
	}
	return nil
}

func discoverHooks(cwd string) ([]hookDescriptor, error) {
	dir := hooksDir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	hooks := make([]hookDescriptor, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if strings.HasSuffix(entry.Name(), ".mjs") {
			hooks = append(hooks, hookDescriptor{
				Name:   entry.Name(),
				Path:   path,
				Legacy: true,
				Reason: "legacy .mjs hook",
			})
			continue
		}
		valid, reason := isSupportedHookExecutable(path, entry)
		hooks = append(hooks, hookDescriptor{
			Name:   entry.Name(),
			Path:   path,
			Valid:  valid,
			Reason: reason,
		})
	}
	sort.Slice(hooks, func(i, j int) bool { return hooks[i].Name < hooks[j].Name })
	return hooks, nil
}

func isSupportedHookExecutable(path string, entry os.DirEntry) (bool, string) {
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".exe", ".bat", ".cmd", ".ps1":
			return true, ""
		default:
			return false, "expected .exe, .bat, .cmd, or .ps1 hook"
		}
	}
	info, err := entry.Info()
	if err != nil {
		return false, err.Error()
	}
	if info.Mode()&0o111 == 0 {
		return false, "missing executable bit"
	}
	return true, ""
}

func resolveHookPluginTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(hookPluginTimeoutEnv))
	if raw == "" {
		return time.Duration(hookPluginDefaultTimeoutMs) * time.Millisecond
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || !isFinite(parsed) {
		return time.Duration(hookPluginDefaultTimeoutMs) * time.Millisecond
	}
	rounded := int(math.Floor(parsed))
	switch {
	case rounded < hookPluginMinimumTimeoutMs:
		rounded = hookPluginMinimumTimeoutMs
	case rounded > hookPluginMaximumTimeoutMs:
		rounded = hookPluginMaximumTimeoutMs
	}
	return time.Duration(rounded) * time.Millisecond
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func runExecutableHook(cwd string, hook hookDescriptor, event hookEventEnvelope, sideEffectsEnabled bool) (hookRunnerResult, error) {
	request := hookRunnerRequest{
		Cwd:                cwd,
		PluginID:           strings.TrimSuffix(hook.Name, filepath.Ext(hook.Name)),
		PluginPath:         hook.Path,
		Event:              event,
		SideEffectsEnabled: sideEffectsEnabled,
		StatePaths: map[string]string{
			"session":         filepath.Join(cwd, ".nana", "state", "session.json"),
			"hud":             filepath.Join(cwd, ".nana", "state", "hud-state.json"),
			"notify_fallback": filepath.Join(cwd, ".nana", "state", "notify-fallback-state.json"),
			"update_check":    filepath.Join(cwd, ".nana", "state", "update-check.json"),
		},
		PluginStatePath: pluginStatePath(cwd, requestPluginName(hook)),
		PluginTMuxState: pluginTMuxStatePath(cwd, requestPluginName(hook)),
		Context: map[string]interface{}{
			"hook_log_path": hooksLogPath(cwd, time.Now().UTC()),
		},
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return hookRunnerResult{}, err
	}

	timeout := resolveHookPluginTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd, err := executableHookCommand(ctx, hook.Path)
	if err != nil {
		return hookRunnerResult{}, err
	}
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "NANA_HOOK_PLUGINS=1")
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return hookRunnerResult{
			OK:     false,
			Plugin: request.PluginID,
			Reason: "timeout",
			Error:  fmt.Sprintf("hook exceeded %dms timeout", timeout.Milliseconds()),
		}, nil
	}

	result := hookRunnerResult{
		Plugin: request.PluginID,
		OK:     runErr == nil,
		Reason: "ok",
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &result); err == nil {
			break
		}
	}
	if strings.TrimSpace(stderr.String()) != "" && result.Error == "" {
		result.Error = strings.TrimSpace(stderr.String())
	}
	if runErr != nil && result.Error == "" {
		result.Error = runErr.Error()
	}
	if result.Reason == "" {
		if runErr != nil {
			result.Reason = "runner_error"
		} else {
			result.Reason = "ok"
		}
	}
	if err := applyHookSideEffects(cwd, request.PluginID, event, result, sideEffectsEnabled); err != nil {
		return result, err
	}
	return result, nil
}

func requestPluginName(hook hookDescriptor) string {
	return strings.TrimSuffix(hook.Name, filepath.Ext(hook.Name))
}

func executableHookCommand(ctx context.Context, path string) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".ps1":
			return exec.CommandContext(ctx, "powershell", "-ExecutionPolicy", "Bypass", "-File", path), nil
		case ".bat", ".cmd":
			return exec.CommandContext(ctx, "cmd", "/c", path), nil
		default:
			return exec.CommandContext(ctx, path), nil
		}
	}
	return exec.CommandContext(ctx, path), nil
}

func applyHookSideEffects(cwd, plugin string, event hookEventEnvelope, result hookRunnerResult, sideEffectsEnabled bool) error {
	for _, entry := range result.Logs {
		if err := appendHookLog(cwd, plugin, event, entry); err != nil {
			return err
		}
	}
	if result.State != nil {
		if err := applyHookState(cwd, plugin, result.State); err != nil {
			return err
		}
	}
	if sideEffectsEnabled {
		for _, action := range result.TMUXActions {
			if err := applyHookTMuxAction(cwd, plugin, action); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendHookLog(cwd, plugin string, event hookEventEnvelope, entry hookResultLog) error {
	level := strings.TrimSpace(strings.ToLower(entry.Level))
	if level == "" {
		level = "info"
	}
	record := map[string]interface{}{
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
		"type":       "hook_plugin_log",
		"plugin":     plugin,
		"level":      level,
		"message":    entry.Message,
		"hook_event": event.Event,
	}
	for key, value := range entry.Meta {
		record[key] = value
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := hooksLogPath(cwd, time.Now().UTC())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	_ = RecordRuntimeArtifact(cwd, path)
	return nil
}

func hooksLogPath(cwd string, now time.Time) string {
	return filepath.Join(cwd, ".nana", "logs", fmt.Sprintf("hooks-%s.jsonl", now.Format("2006-01-02")))
}

func pluginStatePath(cwd, plugin string) string {
	return filepath.Join(cwd, ".nana", "state", "hooks", "plugins", plugin, "data.json")
}

func pluginTMuxStatePath(cwd, plugin string) string {
	return filepath.Join(cwd, ".nana", "state", "hooks", "plugins", plugin, "tmux.json")
}

func applyHookState(cwd, plugin string, state *hookResultState) error {
	path := pluginStatePath(cwd, plugin)
	current := map[string]interface{}{}
	if content, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(content, &current)
	}
	if state.Set != nil {
		for key, value := range state.Set {
			current[key] = value
		}
	}
	for _, key := range state.Delete {
		delete(current, key)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o644)
}

func applyHookTMuxAction(cwd, plugin string, action hookResultTmuxAction) error {
	if strings.TrimSpace(action.Text) == "" {
		return nil
	}
	target, err := resolveTMuxTarget(action)
	if err != nil {
		return err
	}
	if err := hookTMuxGuard(cwd, plugin, target, action); err != nil {
		if strings.Contains(err.Error(), "cooldown") || strings.Contains(err.Error(), "duplicate") {
			return nil
		}
		return err
	}
	if err := runTMux("send-keys", "-t", target, "-l", action.Text); err != nil {
		return err
	}
	submit := true
	if action.Submit != nil {
		submit = *action.Submit
	}
	if submit {
		if err := runTMux("send-keys", "-t", target, "C-m"); err != nil {
			return err
		}
	}
	return nil
}

func resolveTMuxTarget(action hookResultTmuxAction) (string, error) {
	if strings.TrimSpace(action.PaneID) != "" {
		return strings.TrimSpace(action.PaneID), nil
	}
	if strings.TrimSpace(action.Session) == "" {
		return "", fmt.Errorf("tmux action missing pane_id or session_name")
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", action.Session, "#{pane_id}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func runTMux(args ...string) error {
	cmd := exec.Command("tmux", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return nil
}

func hookTMuxGuard(cwd, plugin, target string, action hookResultTmuxAction) error {
	path := pluginTMuxStatePath(cwd, plugin)
	state := hookTmuxState{RecentKeys: map[string]int64{}}
	if content, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(content, &state)
		if state.RecentKeys == nil {
			state.RecentKeys = map[string]int64{}
		}
	}

	now := time.Now().UnixMilli()
	cooldownMs := hookPluginDefaultCooldown
	if action.CooldownMs != nil && *action.CooldownMs >= 0 {
		cooldownMs = *action.CooldownMs
	} else if raw := strings.TrimSpace(os.Getenv(hookPluginCooldownEnv)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			cooldownMs = parsed
		}
	}
	if cooldownMs > 0 && now-state.LastSentAt < int64(cooldownMs) {
		return fmt.Errorf("cooldown active")
	}

	dedupeMs := hookPluginDefaultDedupeMs
	if raw := strings.TrimSpace(os.Getenv(hookPluginDedupeEnv)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			dedupeMs = parsed
		}
	}
	hash := sha256.Sum256([]byte(target + "\x00" + action.Text))
	key := hex.EncodeToString(hash[:])
	cutoff := now - int64(dedupeMs)
	for itemKey, ts := range state.RecentKeys {
		if ts < cutoff {
			delete(state.RecentKeys, itemKey)
		}
	}
	if _, exists := state.RecentKeys[key]; exists {
		return fmt.Errorf("duplicate action")
	}
	state.LastSentAt = now
	state.RecentKeys[key] = now
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o644)
}
