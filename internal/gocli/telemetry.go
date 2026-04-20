package gocli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const TelemetryHelp = `nana telemetry - Summarize privacy-preserving context telemetry

Usage:
  nana telemetry [summary] [--json] [--all] [--run-id <id>] [--log <path>]
  nana telemetry help

Options:
  --json          Emit structured JSON.
  --all           Summarize all run_id values in the log.
  --run-id <id>   Summarize one run/session id.
  --log <path>    Read an explicit telemetry log path.

By default, nana uses .nana/logs/context-telemetry.ndjson and filters to the
current run id when NANA_CONTEXT_TELEMETRY_RUN_ID, NANA_WORK_RUN_ID,
NANA_RUN_ID, or NANA_SESSION_ID is set. The summary reports event counts,
safe skill/reference identifiers, and shell compaction frequency without
emitting raw command arguments or shell output.
`

var telemetrySummaryEvents = map[string]bool{
	"skill_doc_load":                 true,
	"skill_reference_load":           true,
	"shell_output_compaction":        true,
	"shell_output_compaction_failed": true,
}

type telemetryOptions struct {
	View  string
	JSON  bool
	All   bool
	RunID string
	Log   string
	CWD   string
}

type telemetryScope struct {
	RunID          string `json:"run_id,omitempty"`
	AllRuns        bool   `json:"all_runs"`
	FromEnv        bool   `json:"from_env"`
	NoRunIDInEnv   bool   `json:"no_run_id_in_env,omitempty"`
	ExplicitRunID  bool   `json:"explicit_run_id,omitempty"`
	ExplicitAll    bool   `json:"explicit_all,omitempty"`
	FilteredByRun  bool   `json:"filtered_by_run"`
	CurrentRunHint string `json:"current_run_hint,omitempty"`
}

type telemetryEventCount struct {
	Event string `json:"event"`
	Count int    `json:"count"`
}

type telemetrySkillSummary struct {
	Skill          string `json:"skill"`
	Path           string `json:"path,omitempty"`
	Count          int    `json:"count"`
	DocLoads       int    `json:"doc_loads"`
	ReferenceLoads int    `json:"reference_loads"`
	CacheHits      int    `json:"cache_hits,omitempty"`
	CacheMisses    int    `json:"cache_misses,omitempty"`
}

type telemetryCommandSummary struct {
	Command string `json:"command"`
	Count   int    `json:"count"`
	Failed  int    `json:"failed"`
}

type telemetryShellSummary struct {
	Compactions        int                       `json:"compactions"`
	Failed             int                       `json:"failed"`
	CapturedBytes      int                       `json:"captured_bytes"`
	StdoutBytes        int                       `json:"stdout_bytes"`
	StderrBytes        int                       `json:"stderr_bytes"`
	SummaryBytes       int                       `json:"summary_bytes"`
	StdoutLines        int                       `json:"stdout_lines"`
	StderrLines        int                       `json:"stderr_lines"`
	SummaryLines       int                       `json:"summary_lines"`
	FirstTimestamp     string                    `json:"first_timestamp,omitempty"`
	LastTimestamp      string                    `json:"last_timestamp,omitempty"`
	DurationSeconds    float64                   `json:"duration_seconds,omitempty"`
	CompactionsPerHour float64                   `json:"compactions_per_hour,omitempty"`
	Commands           []telemetryCommandSummary `json:"commands,omitempty"`
}

type telemetrySummaryReport struct {
	LogPath       string                  `json:"log_path"`
	Scope         telemetryScope          `json:"scope"`
	EventsScanned int                     `json:"events_scanned"`
	EventsMatched int                     `json:"events_matched"`
	EventsIgnored int                     `json:"events_ignored"`
	InvalidLines  int                     `json:"invalid_lines"`
	ByEvent       []telemetryEventCount   `json:"by_event"`
	SkillLoads    []telemetrySkillSummary `json:"skill_loads,omitempty"`
	Shell         telemetryShellSummary   `json:"shell"`
	Privacy       string                  `json:"privacy"`
}

type telemetryAccumulator struct {
	logPath       string
	scope         telemetryScope
	eventsScanned int
	eventsMatched int
	eventsIgnored int
	invalidLines  int
	byEvent       map[string]int
	skillLoads    map[string]*telemetrySkillSummary
	commands      map[string]*telemetryCommandSummary
	shell         telemetryShellSummary
	firstTime     time.Time
	lastTime      time.Time
}

func Telemetry(cwd string, args []string) error {
	if len(args) > 0 {
		if isHelpToken(args[0]) || args[0] == "help" {
			fmt.Fprint(os.Stdout, TelemetryHelp)
			return nil
		}
		if args[0] == "summary" && len(args) > 1 && isHelpToken(args[1]) {
			fmt.Fprint(os.Stdout, TelemetryHelp)
			return nil
		}
	}

	options, err := parseTelemetryArgs(args)
	if err != nil {
		return err
	}
	options.CWD = cwd
	if strings.TrimSpace(options.Log) == "" {
		options.Log = resolveContextTelemetryLogPath(cwd)
	}
	if strings.TrimSpace(options.Log) == "" {
		return fmt.Errorf("telemetry log path could not be resolved\n%s", TelemetryHelp)
	}

	report, err := buildTelemetrySummary(options)
	if err != nil {
		return err
	}
	if options.JSON {
		payload, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(payload))
		return nil
	}
	fmt.Fprintln(os.Stdout, formatTelemetrySummaryReport(report))
	return nil
}

func parseTelemetryArgs(args []string) (telemetryOptions, error) {
	options := telemetryOptions{View: "summary"}
	index := 0
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] != "summary" {
			return telemetryOptions{}, fmt.Errorf("unknown telemetry subcommand: %s\n%s", args[0], TelemetryHelp)
		}
		index = 1
	}

	for ; index < len(args); index++ {
		token := args[index]
		switch token {
		case "--json":
			options.JSON = true
		case "--all":
			options.All = true
		case "--run-id", "--log":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return telemetryOptions{}, fmt.Errorf("missing value after %s\n%s", token, TelemetryHelp)
			}
			value := strings.TrimSpace(args[index+1])
			switch token {
			case "--run-id":
				options.RunID = value
			case "--log":
				options.Log = value
			}
			index++
		default:
			switch {
			case strings.HasPrefix(token, "--run-id="):
				options.RunID = strings.TrimSpace(strings.TrimPrefix(token, "--run-id="))
			case strings.HasPrefix(token, "--log="):
				options.Log = strings.TrimSpace(strings.TrimPrefix(token, "--log="))
			default:
				return telemetryOptions{}, fmt.Errorf("unknown telemetry option: %s\n%s", token, TelemetryHelp)
			}
		}
	}
	if options.All && strings.TrimSpace(options.RunID) != "" {
		return telemetryOptions{}, fmt.Errorf("--all and --run-id cannot be combined\n%s", TelemetryHelp)
	}
	return options, nil
}

func buildTelemetrySummary(options telemetryOptions) (telemetrySummaryReport, error) {
	scope := telemetryScope{AllRuns: options.All, ExplicitAll: options.All}
	if strings.TrimSpace(options.RunID) != "" {
		scope.RunID = strings.TrimSpace(options.RunID)
		scope.ExplicitRunID = true
		scope.FilteredByRun = true
	} else if !options.All {
		if runID := currentContextTelemetryRunID(); runID != "" {
			scope.RunID = runID
			scope.FromEnv = true
			scope.FilteredByRun = true
			scope.CurrentRunHint = "from NANA_CONTEXT_TELEMETRY_RUN_ID/NANA_WORK_RUN_ID/NANA_RUN_ID/NANA_SESSION_ID"
		} else {
			scope.AllRuns = true
			scope.NoRunIDInEnv = true
		}
	}

	acc := telemetryAccumulator{
		logPath:    filepath.Clean(options.Log),
		scope:      scope,
		byEvent:    map[string]int{},
		skillLoads: map[string]*telemetrySkillSummary{},
		commands:   map[string]*telemetryCommandSummary{},
	}

	file, err := os.Open(options.Log)
	if err != nil {
		if os.IsNotExist(err) {
			return acc.report(), nil
		}
		return telemetrySummaryReport{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		acc.eventsScanned++
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			acc.invalidLines++
			continue
		}
		acc.recordEvent(event)
	}
	if err := scanner.Err(); err != nil {
		return telemetrySummaryReport{}, err
	}
	return acc.report(), nil
}

func (acc *telemetryAccumulator) recordEvent(event map[string]any) {
	eventName := telemetryString(event, "event")
	if !telemetrySummaryEvents[eventName] {
		acc.eventsIgnored++
		return
	}
	if acc.scope.FilteredByRun && telemetryString(event, "run_id") != acc.scope.RunID {
		acc.eventsIgnored++
		return
	}

	acc.eventsMatched++
	acc.byEvent[eventName]++

	switch eventName {
	case "skill_doc_load", "skill_reference_load":
		acc.recordSkillEvent(eventName, event)
	case "shell_output_compaction", "shell_output_compaction_failed":
		acc.recordShellEvent(eventName, event)
	}
}

func (acc *telemetryAccumulator) recordSkillEvent(eventName string, event map[string]any) {
	skill := safeTelemetryLabel(firstTelemetryString(event, "skill", "skill_name", "skill_id", "skill_slug", "name"))
	if skill == "" {
		skill = "(unknown)"
	}
	path := safeTelemetryPath(firstTelemetryString(event, "skill_path", "reference_path", "doc_path", "path", "file", "reference"))
	key := skill + "\x00" + path
	row := acc.skillLoads[key]
	if row == nil {
		row = &telemetrySkillSummary{Skill: skill, Path: path}
		acc.skillLoads[key] = row
	}
	row.Count++
	if eventName == "skill_doc_load" {
		row.DocLoads++
		switch strings.ToLower(safeTelemetryLabel(firstTelemetryString(event, "cache", "cache_status"))) {
		case "hit":
			row.CacheHits++
		case "miss":
			row.CacheMisses++
		}
	} else {
		row.ReferenceLoads++
	}
}

func (acc *telemetryAccumulator) recordShellEvent(eventName string, event map[string]any) {
	acc.shell.Compactions++
	if eventName == "shell_output_compaction_failed" {
		acc.shell.Failed++
	}
	captured := telemetryInt(event, "captured_bytes")
	if captured == 0 {
		captured = telemetryInt(event, "stdout_bytes") + telemetryInt(event, "stderr_bytes")
	}
	acc.shell.CapturedBytes += captured
	acc.shell.StdoutBytes += telemetryInt(event, "stdout_bytes")
	acc.shell.StderrBytes += telemetryInt(event, "stderr_bytes")
	acc.shell.SummaryBytes += telemetryInt(event, "summary_bytes")
	acc.shell.StdoutLines += telemetryInt(event, "stdout_lines")
	acc.shell.StderrLines += telemetryInt(event, "stderr_lines")
	acc.shell.SummaryLines += telemetryInt(event, "summary_lines")

	command := safeTelemetryCommandName(telemetryString(event, "command_name"))
	if command == "" {
		command = "(unknown)"
	}
	commandRow := acc.commands[command]
	if commandRow == nil {
		commandRow = &telemetryCommandSummary{Command: command}
		acc.commands[command] = commandRow
	}
	commandRow.Count++
	if eventName == "shell_output_compaction_failed" {
		commandRow.Failed++
	}

	if timestamp, ok := parseTelemetryTimestamp(telemetryString(event, "timestamp")); ok {
		if acc.firstTime.IsZero() || timestamp.Before(acc.firstTime) {
			acc.firstTime = timestamp
		}
		if acc.lastTime.IsZero() || timestamp.After(acc.lastTime) {
			acc.lastTime = timestamp
		}
	}
}

func (acc *telemetryAccumulator) report() telemetrySummaryReport {
	byEvent := make([]telemetryEventCount, 0, len(acc.byEvent))
	for event, count := range acc.byEvent {
		byEvent = append(byEvent, telemetryEventCount{Event: event, Count: count})
	}
	sort.Slice(byEvent, func(i, j int) bool {
		return byEvent[i].Event < byEvent[j].Event
	})

	skillLoads := make([]telemetrySkillSummary, 0, len(acc.skillLoads))
	for _, row := range acc.skillLoads {
		skillLoads = append(skillLoads, *row)
	}
	sort.Slice(skillLoads, func(i, j int) bool {
		if skillLoads[i].Count == skillLoads[j].Count {
			if skillLoads[i].Skill == skillLoads[j].Skill {
				return skillLoads[i].Path < skillLoads[j].Path
			}
			return skillLoads[i].Skill < skillLoads[j].Skill
		}
		return skillLoads[i].Count > skillLoads[j].Count
	})

	commands := make([]telemetryCommandSummary, 0, len(acc.commands))
	for _, row := range acc.commands {
		commands = append(commands, *row)
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Count == commands[j].Count {
			return commands[i].Command < commands[j].Command
		}
		return commands[i].Count > commands[j].Count
	})
	acc.shell.Commands = commands
	if !acc.firstTime.IsZero() {
		acc.shell.FirstTimestamp = acc.firstTime.UTC().Format(time.RFC3339)
	}
	if !acc.lastTime.IsZero() {
		acc.shell.LastTimestamp = acc.lastTime.UTC().Format(time.RFC3339)
	}
	if !acc.firstTime.IsZero() && !acc.lastTime.IsZero() && acc.lastTime.After(acc.firstTime) {
		duration := acc.lastTime.Sub(acc.firstTime)
		acc.shell.DurationSeconds = duration.Seconds()
		if duration > 0 {
			acc.shell.CompactionsPerHour = math.Round((float64(acc.shell.Compactions)/duration.Hours())*10) / 10
		}
	}

	return telemetrySummaryReport{
		LogPath:       acc.logPath,
		Scope:         acc.scope,
		EventsScanned: acc.eventsScanned,
		EventsMatched: acc.eventsMatched,
		EventsIgnored: acc.eventsIgnored,
		InvalidLines:  acc.invalidLines,
		ByEvent:       byEvent,
		SkillLoads:    skillLoads,
		Shell:         acc.shell,
		Privacy:       "Reports metadata only; raw command arguments and shell output are not emitted.",
	}
}

func formatTelemetrySummaryReport(report telemetrySummaryReport) string {
	lines := []string{
		"Context telemetry summary",
		fmt.Sprintf("Log: %s", report.LogPath),
		fmt.Sprintf("Scope: %s", formatTelemetryScope(report.Scope)),
		fmt.Sprintf("Events: scanned=%d matched=%d ignored=%d invalid=%d", report.EventsScanned, report.EventsMatched, report.EventsIgnored, report.InvalidLines),
		"Events by type:",
	}
	if len(report.ByEvent) == 0 {
		lines = append(lines, "  (none)")
	} else {
		for _, row := range report.ByEvent {
			lines = append(lines, fmt.Sprintf("  %s: %d", row.Event, row.Count))
		}
	}

	lines = append(lines, "Skill/reference loads:")
	if len(report.SkillLoads) == 0 {
		lines = append(lines, "  (none)")
	} else {
		for _, row := range report.SkillLoads {
			label := row.Skill
			if row.Path != "" {
				label += " @ " + row.Path
			}
			line := fmt.Sprintf("  %s: %d (doc=%d reference=%d)", label, row.Count, row.DocLoads, row.ReferenceLoads)
			if row.CacheHits > 0 || row.CacheMisses > 0 {
				line += fmt.Sprintf(" cache(hit=%d miss=%d)", row.CacheHits, row.CacheMisses)
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "Shell output compactions:")
	lines = append(lines, fmt.Sprintf("  total: %d (failed=%d)", report.Shell.Compactions, report.Shell.Failed))
	if report.Shell.Compactions > 0 {
		lines = append(lines, fmt.Sprintf("  captured: %d bytes across %d lines", report.Shell.CapturedBytes, report.Shell.StdoutLines+report.Shell.StderrLines))
		lines = append(lines, fmt.Sprintf("  summaries: %d bytes across %d lines", report.Shell.SummaryBytes, report.Shell.SummaryLines))
		if report.Shell.FirstTimestamp != "" && report.Shell.LastTimestamp != "" {
			lines = append(lines, fmt.Sprintf("  window: %s to %s", report.Shell.FirstTimestamp, report.Shell.LastTimestamp))
		}
		if report.Shell.CompactionsPerHour > 0 {
			lines = append(lines, fmt.Sprintf("  frequency: %.1f/hour over %s", report.Shell.CompactionsPerHour, telemetryDurationString(report.Shell.DurationSeconds)))
		} else {
			lines = append(lines, "  frequency: n/a (need at least two timestamps)")
		}
		if len(report.Shell.Commands) > 0 {
			parts := []string{}
			for _, command := range report.Shell.Commands {
				part := fmt.Sprintf("%s=%d", command.Command, command.Count)
				if command.Failed > 0 {
					part += fmt.Sprintf(" failed=%d", command.Failed)
				}
				parts = append(parts, part)
			}
			lines = append(lines, "  commands: "+strings.Join(parts, ", "))
		}
	}
	lines = append(lines, "Privacy: "+report.Privacy)
	return strings.Join(lines, "\n")
}

func formatTelemetryScope(scope telemetryScope) string {
	if scope.FilteredByRun {
		if scope.FromEnv {
			return "current run_id=" + scope.RunID
		}
		return "run_id=" + scope.RunID
	}
	if scope.NoRunIDInEnv {
		return "all runs (no current run id in environment)"
	}
	return "all runs"
}

func telemetryDurationString(seconds float64) string {
	if seconds <= 0 {
		return "0s"
	}
	duration := time.Duration(seconds * float64(time.Second)).Round(time.Second)
	return duration.String()
}

func currentContextTelemetryRunID() string {
	for _, key := range []string{"NANA_CONTEXT_TELEMETRY_RUN_ID", "NANA_WORK_RUN_ID", "NANA_RUN_ID", "NANA_SESSION_ID"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstTelemetryString(event map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := telemetryString(event, key); value != "" {
			return value
		}
	}
	return ""
}

func telemetryString(event map[string]any, key string) string {
	value, ok := event[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func telemetryInt(event map[string]any, key string) int {
	value, ok := event[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func parseTelemetryTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func safeTelemetryPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	slash := filepath.ToSlash(filepath.Clean(raw))
	if slash == "." || strings.Contains(slash, "\x00") {
		return ""
	}
	if index := strings.LastIndex(slash, "/skills/"); index >= 0 {
		rel := strings.TrimPrefix(slash[index+1:], "/")
		if isSafeTelemetryRelativePath(rel) {
			return rel
		}
	}
	if strings.HasPrefix(slash, "skills/") && isSafeTelemetryRelativePath(slash) {
		return slash
	}
	if (strings.HasPrefix(slash, "references/") || strings.HasPrefix(slash, "reference/")) && isSafeTelemetryRelativePath(slash) {
		return slash
	}
	if slash == "SKILL.md" || slash == "RUNTIME.md" {
		return slash
	}
	return ""
}

func isSafeTelemetryRelativePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "../") || path == ".." || strings.Contains(path, "/../") {
		return false
	}
	return !strings.Contains(path, "\x00")
}

func safeTelemetryCommandName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "\x00") {
		return ""
	}
	base := filepath.Base(raw)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return safeTelemetryLabel(base)
}

func safeTelemetryLabel(raw string) string {
	label := strings.TrimSpace(raw)
	if label == "" || len(label) > 96 || strings.Contains(label, "\x00") {
		return ""
	}
	if strings.ContainsAny(label, "/\\~") {
		return ""
	}
	for _, r := range label {
		if r < 32 || r == 127 {
			return ""
		}
	}
	return label
}
