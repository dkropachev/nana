package gocli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const HooksHelp = `Usage:
  nana hooks init       Create .nana/hooks/sample-plugin.mjs scaffold
  nana hooks status     Show plugin directory + discovered plugins
  nana hooks validate   Validate plugin exports/signatures
  nana hooks test       Dispatch synthetic turn-complete event to plugins

Notes:
  - This is the NANA extensibility surface for event-driven plugins under .nana/hooks/*.mjs.
  - Plugins are enabled by default. Disable with NANA_HOOK_PLUGINS=0.
`

const SampleHookPlugin = `export async function onHookEvent(event, sdk) {
  if (event.event !== 'turn-complete') return;

  const current = Number((await sdk.state.read('sample-seen-count')) ?? 0);
  const next = Number.isFinite(current) ? current + 1 : 1;
  await sdk.state.write('sample-seen-count', next);

  await sdk.log.info('sample-plugin observed turn-complete', {
    turn_id: event.turn_id,
    seen_count: next,
  });
}
`

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
	return filepath.Join(hooksDir(cwd), "sample-plugin.mjs")
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
	if err := os.WriteFile(samplePath, []byte(SampleHookPlugin), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Created %s\n", samplePath)
	fmt.Fprintln(os.Stdout, "Plugins are enabled by default. Disable with NANA_HOOK_PLUGINS=0.")
	return nil
}

func hooksStatus(cwd string) error {
	dir := hooksDir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	plugins := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".mjs") {
			plugins = append(plugins, entry.Name())
		}
	}
	sort.Strings(plugins)

	fmt.Fprintln(os.Stdout, "hooks status")
	fmt.Fprintln(os.Stdout, "-----------")
	fmt.Fprintf(os.Stdout, "Directory: %s\n", dir)
	if hooksEnabled() {
		fmt.Fprintln(os.Stdout, "Plugins enabled: yes")
	} else {
		fmt.Fprintln(os.Stdout, "Plugins enabled: no (disabled with NANA_HOOK_PLUGINS=0)")
	}
	fmt.Fprintf(os.Stdout, "Discovered plugins: %d\n", len(plugins))
	for _, plugin := range plugins {
		fmt.Fprintf(os.Stdout, "- %s\n", plugin)
	}
	return nil
}

func hooksValidate(cwd string) error {
	dir := hooksDir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stdout, "No plugins found. Run: nana hooks init")
			return nil
		}
		return err
	}

	plugins := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".mjs") {
			continue
		}
		plugins = append(plugins, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(plugins)
	if len(plugins) == 0 {
		fmt.Fprintln(os.Stdout, "No plugins found. Run: nana hooks init")
		return nil
	}

	failed := 0
	for _, pluginPath := range plugins {
		content, err := os.ReadFile(pluginPath)
		name := filepath.Base(pluginPath)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stdout, "✗ %s: %v\n", name, err)
			continue
		}
		source := string(content)
		if strings.Contains(source, "export async function onHookEvent") ||
			strings.Contains(source, "export function onHookEvent") ||
			strings.Contains(source, "onHookEvent =") {
			fmt.Fprintf(os.Stdout, "✓ %s\n", name)
			continue
		}
		failed++
		fmt.Fprintf(os.Stdout, "✗ %s: missing export `onHookEvent(event, sdk)`\n", name)
	}

	if failed > 0 {
		suffix := "s"
		if failed == 1 {
			suffix = ""
		}
		return fmt.Errorf("hooks validation failed (%d plugin%s)", failed, suffix)
	}
	return nil
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
	Cwd                string            `json:"cwd"`
	PluginID           string            `json:"pluginId"`
	PluginPath         string            `json:"pluginPath"`
	Event              hookEventEnvelope `json:"event"`
	SideEffectsEnabled bool              `json:"sideEffectsEnabled"`
}

type hookRunnerResult struct {
	OK     bool   `json:"ok"`
	Plugin string `json:"plugin"`
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

const (
	hookRunnerPrefix           = "__NANA_PLUGIN_RESULT__ "
	hookPluginTimeoutEnv       = "NANA_HOOK_PLUGIN_TIMEOUT_MS"
	hookPluginDefaultTimeoutMs = 1500
	hookPluginMinimumTimeoutMs = 100
	hookPluginMaximumTimeoutMs = 60000
)

func hooksTest(cwd string, repoRoot string) error {
	dir := hooksDir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	plugins := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".mjs") {
			continue
		}
		plugins = append(plugins, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(plugins)

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
	fmt.Fprintf(os.Stdout, "plugins discovered: %d\n", len(plugins))
	fmt.Fprintln(os.Stdout, "plugins enabled: yes")
	fmt.Fprintln(os.Stdout, "dispatch reason: ok")

	runnerPath := filepath.Join(repoRoot, "dist", "hooks", "extensibility", "plugin-runner.js")
	if _, err := os.Stat(runnerPath); err != nil {
		if len(plugins) == 0 {
			return nil
		}
		return fmt.Errorf("hook plugin runner not found: %s", runnerPath)
	}

	for _, pluginPath := range plugins {
		result, runErr := runHookPluginRunner(cwd, runnerPath, pluginPath, event)
		name := filepath.Base(pluginPath)
		if runErr != nil {
			fmt.Fprintf(os.Stdout, "%s: error (%v)\n", name, runErr)
			continue
		}
		if result.OK {
			fmt.Fprintf(os.Stdout, "%s: ok\n", name)
		} else if result.Error != "" {
			fmt.Fprintf(os.Stdout, "%s: %s (%s)\n", name, result.Reason, result.Error)
		} else {
			fmt.Fprintf(os.Stdout, "%s: %s\n", name, result.Reason)
		}
	}

	logPath := filepath.Join(cwd, ".nana", "logs", fmt.Sprintf("hooks-%s.jsonl", time.Now().UTC().Format("2006-01-02")))
	if _, err := os.Stat(logPath); err == nil {
		fmt.Fprintf(os.Stdout, "log file: %s\n", logPath)
	}
	return nil
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

func runHookPluginRunner(cwd string, runnerPath string, pluginPath string, event hookEventEnvelope) (hookRunnerResult, error) {
	request := hookRunnerRequest{
		Cwd:                cwd,
		PluginID:           strings.TrimSuffix(filepath.Base(pluginPath), ".mjs"),
		PluginPath:         pluginPath,
		Event:              event,
		SideEffectsEnabled: true,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return hookRunnerResult{}, err
	}

	timeout := resolveHookPluginTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", runnerPath)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "NANA_HOOK_PLUGINS=1")
	cmd.Stdin = strings.NewReader(string(payload))
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
			Error:  fmt.Sprintf("plugin runner exceeded %dms timeout", timeout.Milliseconds()),
		}, nil
	}

	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, hookRunnerPrefix) {
			continue
		}
		var result hookRunnerResult
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, hookRunnerPrefix)), &result); err == nil {
			return result, nil
		}
	}

	if strings.TrimSpace(stderr.String()) != "" {
		return hookRunnerResult{OK: false, Plugin: request.PluginID, Reason: "runner_error", Error: strings.TrimSpace(stderr.String())}, nil
	}
	if runErr != nil {
		return hookRunnerResult{OK: false, Plugin: request.PluginID, Reason: "runner_error", Error: runErr.Error()}, nil
	}
	return hookRunnerResult{OK: true, Plugin: request.PluginID, Reason: "ok"}, nil
}
