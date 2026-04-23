package gocli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const TelemetryHelp = `nana telemetry - Summarize privacy-preserving context telemetry

Usage:
  nana telemetry [summary] [--json] [--all] [--run-id <id>] [--turn-id <id>] [--log <path>]
  nana telemetry help

Options:
  --json          Emit structured JSON.
  --all           Summarize all run_id values in the log.
  --run-id <id>   Summarize one run/session id.
  --turn-id <id>  Within one run, summarize one turn_id.
  --log <path>    Read an explicit telemetry log path.

By default, nana uses .nana/logs/context-telemetry.ndjson and filters to the
current run id when NANA_CONTEXT_TELEMETRY_RUN_ID, NANA_WORK_RUN_ID,
NANA_RUN_ID, or NANA_SESSION_ID is set. Use --turn-id (or
NANA_CONTEXT_TELEMETRY_TURN_ID/NANA_TURN_ID/CODEX_TURN_ID) to inspect one
turn within that run. The summary reports event counts, safe skill/reference
identifiers, and shell compaction frequency without emitting raw command
arguments or shell output. For single-run scopes it also warns when
skill/runtime context loads exceed the default budget.
`

var telemetrySummaryEvents = map[string]bool{
	"skill_doc_load":                 true,
	"skill_reference_load":           true,
	"shell_output_compaction":        true,
	"shell_output_compaction_failed": true,
}

const (
	telemetrySkillDocFileBudget       = 3
	telemetrySkillReferenceFileBudget = 5
	telemetrySkillTotalFileBudget     = 8
	telemetrySkillBudgetResumeWindow  = 4 * 1024
)

type telemetryOptions struct {
	View   string
	JSON   bool
	All    bool
	RunID  string
	TurnID string
	Log    string
	CWD    string
}

type telemetryScope struct {
	RunID          string `json:"run_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	AllRuns        bool   `json:"all_runs"`
	FromEnv        bool   `json:"from_env"`
	NoRunIDInEnv   bool   `json:"no_run_id_in_env,omitempty"`
	ExplicitRunID  bool   `json:"explicit_run_id,omitempty"`
	ExplicitTurnID bool   `json:"explicit_turn_id,omitempty"`
	ExplicitAll    bool   `json:"explicit_all,omitempty"`
	FilteredByRun  bool   `json:"filtered_by_run"`
	FilteredByTurn bool   `json:"filtered_by_turn,omitempty"`
	TurnFromEnv    bool   `json:"turn_from_env,omitempty"`
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

type telemetryBudgetWarning struct {
	Budget  string `json:"budget"`
	Count   int    `json:"count"`
	Limit   int    `json:"limit"`
	Message string `json:"message"`
}

type telemetrySkillBudget struct {
	DocFiles            int                      `json:"doc_files"`
	ReferenceFiles      int                      `json:"reference_files"`
	TotalFiles          int                      `json:"total_files"`
	DocFileBudget       int                      `json:"doc_file_budget"`
	ReferenceFileBudget int                      `json:"reference_file_budget"`
	TotalFileBudget     int                      `json:"total_file_budget"`
	Warnings            []telemetryBudgetWarning `json:"warnings,omitempty"`
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
	SkillBudget   telemetrySkillBudget    `json:"skill_budget"`
	Shell         telemetryShellSummary   `json:"shell"`
	Privacy       string                  `json:"privacy"`
}

type telemetrySkillBudgetCache struct {
	LogPath           string                                        `json:"log_path"`
	Offset            int64                                         `json:"offset"`
	ModTime           string                                        `json:"mod_time,omitempty"`
	ResumeWindowBytes int                                           `json:"resume_window_bytes,omitempty"`
	ResumeWindowHash  string                                        `json:"resume_window_hash,omitempty"`
	Runs              map[string][]telemetrySkillSummary            `json:"runs,omitempty"`
	Turns             map[string]map[string][]telemetrySkillSummary `json:"turns,omitempty"`
}

type telemetryBudgetLogReadSeeker interface {
	io.Reader
	io.Seeker
	io.Closer
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

var openTelemetrySkillBudgetLog = func(path string) (telemetryBudgetLogReadSeeker, error) {
	return os.Open(path)
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
		case "--run-id", "--turn-id", "--log":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return telemetryOptions{}, fmt.Errorf("missing value after %s\n%s", token, TelemetryHelp)
			}
			value := strings.TrimSpace(args[index+1])
			switch token {
			case "--run-id":
				options.RunID = value
			case "--turn-id":
				options.TurnID = value
			case "--log":
				options.Log = value
			}
			index++
		default:
			switch {
			case strings.HasPrefix(token, "--run-id="):
				options.RunID = strings.TrimSpace(strings.TrimPrefix(token, "--run-id="))
			case strings.HasPrefix(token, "--turn-id="):
				options.TurnID = strings.TrimSpace(strings.TrimPrefix(token, "--turn-id="))
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
	if options.All && strings.TrimSpace(options.TurnID) != "" {
		return telemetryOptions{}, fmt.Errorf("--all and --turn-id cannot be combined\n%s", TelemetryHelp)
	}
	return options, nil
}

func buildTelemetrySummary(options telemetryOptions) (telemetrySummaryReport, error) {
	scope, err := resolveTelemetrySummaryScope(options)
	if err != nil {
		return telemetrySummaryReport{}, err
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

func resolveTelemetrySummaryScope(options telemetryOptions) (telemetryScope, error) {
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
	switch turnID := strings.TrimSpace(options.TurnID); {
	case turnID != "":
		if scope.AllRuns {
			return telemetryScope{}, fmt.Errorf("--turn-id requires --run-id or a current run id in the environment\n%s", TelemetryHelp)
		}
		scope.TurnID = turnID
		scope.ExplicitTurnID = true
		scope.FilteredByTurn = true
	case !scope.AllRuns && !scope.ExplicitRunID:
		if turnID := currentContextTelemetryTurnID(); turnID != "" {
			scope.TurnID = turnID
			scope.FilteredByTurn = true
			scope.TurnFromEnv = true
		}
	}
	return scope, nil
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
	if acc.scope.FilteredByTurn && telemetryString(event, "turn_id") != acc.scope.TurnID {
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
	skillBudget := buildTelemetrySkillBudget(skillLoads, acc.scope)

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
		SkillBudget:   skillBudget,
		Shell:         acc.shell,
		Privacy:       "Reports metadata only; raw command arguments and shell output are not emitted.",
	}
}

func buildTelemetrySkillBudget(skillLoads []telemetrySkillSummary, scope telemetryScope) telemetrySkillBudget {
	budget := telemetrySkillBudget{
		DocFileBudget:       telemetrySkillDocFileBudget,
		ReferenceFileBudget: telemetrySkillReferenceFileBudget,
		TotalFileBudget:     telemetrySkillTotalFileBudget,
	}
	docFiles := map[string]struct{}{}
	referenceFiles := map[string]struct{}{}
	totalFiles := map[string]struct{}{}
	for _, row := range skillLoads {
		key := telemetrySkillBudgetKey(row)
		if row.DocLoads > 0 {
			docFiles[key] = struct{}{}
			totalFiles[key] = struct{}{}
		}
		if row.ReferenceLoads > 0 {
			referenceFiles[key] = struct{}{}
			totalFiles[key] = struct{}{}
		}
	}
	budget.DocFiles = len(docFiles)
	budget.ReferenceFiles = len(referenceFiles)
	budget.TotalFiles = len(totalFiles)
	if scope.FilteredByRun {
		budget.Warnings = telemetrySkillBudgetWarnings(budget)
	}
	return budget
}

func sessionSkillContextBudgetAdvisoryBlock(cwd string, scope contextTelemetryScope, activatedDocs []loadedSkillRuntimeDoc) string {
	skillLoads := telemetrySkillLoadsFromLoadedDocs(activatedDocs)
	advisoryScope := skillContextBudgetScopeForLoadedDocs(scope)
	if skillContextBudgetScopeMayHavePriorTelemetry(scope) {
		if report, ok := currentSkillContextBudgetReportWithScope(cwd, scope); ok {
			skillLoads = append(skillLoads, report.SkillLoads...)
			advisoryScope = report.Scope
		}
	}
	if len(skillLoads) == 0 {
		return ""
	}
	return formatSkillContextBudgetAdvisory(advisoryScope, buildTelemetrySkillBudget(skillLoads, advisoryScope))
}

func skillContextBudgetScopeMayHavePriorTelemetry(scope contextTelemetryScope) bool {
	if scope.GeneratedRunID || scope.GeneratedTurnID {
		return false
	}
	if strings.TrimSpace(scope.RunID) != "" {
		return true
	}
	return currentContextTelemetryRunID() != ""
}

func telemetrySkillLoadsFromLoadedDocs(activatedDocs []loadedSkillRuntimeDoc) []telemetrySkillSummary {
	if len(activatedDocs) == 0 {
		return nil
	}
	skillLoads := make([]telemetrySkillSummary, 0, len(activatedDocs))
	for _, doc := range activatedDocs {
		row := telemetrySkillSummary{
			Skill:    safeTelemetryLabel(strings.TrimSpace(doc.Skill)),
			Path:     safeTelemetryPath(firstNonEmptyString(strings.TrimSpace(doc.ActualPath), strings.TrimSpace(doc.DisplayPath))),
			Count:    1,
			DocLoads: 1,
		}
		if row.Skill == "" {
			row.Skill = "(unknown)"
		}
		skillLoads = append(skillLoads, row)
	}
	return skillLoads
}

func skillContextBudgetScopeForSession(scope contextTelemetryScope) telemetryScope {
	resolved := telemetryScope{}
	if runID := strings.TrimSpace(scope.RunID); runID != "" {
		resolved.RunID = runID
		resolved.FilteredByRun = true
	}
	if turnID := strings.TrimSpace(scope.TurnID); turnID != "" {
		resolved.TurnID = turnID
		resolved.FilteredByTurn = true
	}
	return resolved
}

func skillContextBudgetScopeForLoadedDocs(scope contextTelemetryScope) telemetryScope {
	explicitRunID := strings.TrimSpace(scope.RunID)
	resolved := skillContextBudgetScopeForSession(scope)
	if !resolved.FilteredByRun {
		if runID := currentContextTelemetryRunID(); runID != "" {
			resolved.RunID = runID
			resolved.FilteredByRun = true
		}
	}
	if !resolved.FilteredByTurn && explicitRunID == "" && resolved.FilteredByRun {
		if turnID := currentContextTelemetryTurnID(); turnID != "" {
			resolved.TurnID = turnID
			resolved.FilteredByTurn = true
		}
	}
	if !resolved.FilteredByRun {
		resolved.FilteredByRun = true
	}
	return resolved
}

func currentSkillContextBudgetAdvisoryBlock(cwd string) string {
	return currentSkillContextBudgetAdvisoryBlockWithScope(cwd, contextTelemetryScope{})
}

func telemetrySkillBudgetCachePath(cwd string, logPath string) string {
	token := sha256BytesHex([]byte(filepath.Clean(logPath)))
	return filepath.Join(BaseStateDir(cwd), "context-telemetry-skill-budget-"+token+".json")
}

func telemetrySkillBudgetCachePathForScope(cwd string, logPath string, scope telemetryScope) string {
	baseToken := sha256BytesHex([]byte(filepath.Clean(logPath)))
	scopeKey := "run\x00" + strings.TrimSpace(scope.RunID)
	if scope.FilteredByTurn && strings.TrimSpace(scope.TurnID) != "" {
		scopeKey = "turn\x00" + strings.TrimSpace(scope.RunID) + "\x00" + strings.TrimSpace(scope.TurnID)
	}
	scopeToken := sha256BytesHex([]byte(scopeKey))
	return filepath.Join(BaseStateDir(cwd), "context-telemetry-skill-budget-"+baseToken+"-"+scopeToken+".json")
}

func currentSkillContextBudgetReportWithScope(cwd string, scope contextTelemetryScope) (telemetrySummaryReport, bool) {
	logPath := resolveContextTelemetryLogPath(cwd)
	if strings.TrimSpace(logPath) == "" {
		return telemetrySummaryReport{}, false
	}
	resolvedScope, err := resolveTelemetrySummaryScope(telemetryOptions{
		View:   "summary",
		RunID:  strings.TrimSpace(scope.RunID),
		TurnID: strings.TrimSpace(scope.TurnID),
		Log:    logPath,
		CWD:    cwd,
	})
	if err != nil || !resolvedScope.FilteredByRun {
		return telemetrySummaryReport{}, false
	}
	skillLoads, err := currentSkillContextBudgetSkillLoads(cwd, logPath, resolvedScope)
	if err != nil {
		return telemetrySummaryReport{}, false
	}
	report := telemetrySummaryReport{
		LogPath:     filepath.Clean(logPath),
		Scope:       resolvedScope,
		SkillLoads:  skillLoads,
		SkillBudget: buildTelemetrySkillBudget(skillLoads, resolvedScope),
		Privacy:     "Reports metadata only; raw command arguments and shell output are not emitted.",
	}
	return report, true
}

func currentSkillContextBudgetSkillLoads(cwd string, logPath string, scope telemetryScope) ([]telemetrySkillSummary, error) {
	cleanLogPath := filepath.Clean(logPath)
	cachePath := telemetrySkillBudgetCachePathForScope(cwd, cleanLogPath, scope)
	if legacyCachePath := telemetrySkillBudgetCachePath(cwd, cleanLogPath); legacyCachePath != cachePath {
		_ = os.Remove(legacyCachePath)
	}
	cache := readTelemetrySkillBudgetCache(cachePath)

	info, err := os.Stat(cleanLogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	fromOffset, shouldScan, reset := telemetrySkillBudgetCacheScanPlan(cache, cleanLogPath, info)
	if reset {
		cache = telemetrySkillBudgetCache{}
	}
	cache.LogPath = cleanLogPath
	if shouldScan {
		if err := updateTelemetrySkillBudgetCache(cleanLogPath, fromOffset, scope, &cache); err != nil {
			cache = telemetrySkillBudgetCache{LogPath: cleanLogPath}
			if err := updateTelemetrySkillBudgetCache(cleanLogPath, 0, scope, &cache); err != nil {
				return nil, err
			}
		}
		_ = writeTelemetrySkillBudgetCache(cachePath, cache)
	}
	return telemetrySkillBudgetCacheSkillLoads(cache, scope), nil
}

func telemetrySkillBudgetCacheScanPlan(cache telemetrySkillBudgetCache, cleanLogPath string, info os.FileInfo) (int64, bool, bool) {
	currentModTime := info.ModTime().UTC().Format(time.RFC3339Nano)
	hasCache := filepath.Clean(strings.TrimSpace(cache.LogPath)) == cleanLogPath &&
		(cache.Offset > 0 || cache.ModTime != "" || len(cache.Runs) > 0 || len(cache.Turns) > 0)
	if !hasCache {
		return 0, true, false
	}
	switch {
	case cache.Offset == info.Size() && cache.ModTime == currentModTime:
		return 0, false, false
	case cache.Offset >= 0 && cache.Offset < info.Size():
		if telemetrySkillBudgetCacheCanResume(cache, cleanLogPath) {
			return cache.Offset, true, false
		}
		return 0, true, true
	default:
		return 0, true, true
	}
}

func telemetrySkillBudgetCacheCanResume(cache telemetrySkillBudgetCache, cleanLogPath string) bool {
	if cache.Offset <= 0 {
		return true
	}
	if cache.ResumeWindowBytes <= 0 || strings.TrimSpace(cache.ResumeWindowHash) == "" {
		return false
	}
	resumeWindowBytes, resumeWindowHash := telemetrySkillBudgetResumeAnchor(cleanLogPath, cache.Offset)
	return resumeWindowBytes == cache.ResumeWindowBytes && resumeWindowHash == cache.ResumeWindowHash
}

func telemetrySkillBudgetResumeAnchor(path string, offset int64) (int, string) {
	file, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer file.Close()
	resumeWindowBytes, resumeWindowHash, err := telemetrySkillBudgetResumeAnchorForReader(file, offset)
	if err != nil {
		return 0, ""
	}
	return resumeWindowBytes, resumeWindowHash
}

func telemetrySkillBudgetResumeAnchorForReader(reader io.ReadSeeker, offset int64) (int, string, error) {
	if reader == nil || offset <= 0 {
		return 0, "", nil
	}
	resumeWindowBytes := int(offset)
	if resumeWindowBytes > telemetrySkillBudgetResumeWindow {
		resumeWindowBytes = telemetrySkillBudgetResumeWindow
	}
	start := offset - int64(resumeWindowBytes)
	if _, err := reader.Seek(start, io.SeekStart); err != nil {
		return 0, "", err
	}
	buffer := make([]byte, resumeWindowBytes)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return 0, "", err
	}
	return resumeWindowBytes, sha256BytesHex(buffer), nil
}

func readTelemetrySkillBudgetCache(path string) telemetrySkillBudgetCache {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return telemetrySkillBudgetCache{}
	}
	var cache telemetrySkillBudgetCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		return telemetrySkillBudgetCache{}
	}
	return cache
}

func writeTelemetrySkillBudgetCache(path string, cache telemetrySkillBudgetCache) error {
	payload, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeRuntimeBytesIfChanged(path, payload)
}

func updateTelemetrySkillBudgetCache(logPath string, fromOffset int64, scope telemetryScope, cache *telemetrySkillBudgetCache) error {
	if cache == nil {
		return nil
	}
	file, err := openTelemetrySkillBudgetLog(logPath)
	if err != nil {
		return err
	}
	defer file.Close()

	if fromOffset > 0 {
		if _, err := file.Seek(fromOffset, io.SeekStart); err != nil {
			return err
		}
	}

	bucket := telemetrySkillBudgetCacheScopedBucket(cache, scope)

	reader := bufio.NewReaderSize(file, 64*1024)
	committedOffset := fromOffset
	for {
		lineBytes, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if len(lineBytes) == 0 {
			break
		}
		if lineBytes[len(lineBytes)-1] != '\n' {
			break
		}
		committedOffset += int64(len(lineBytes))
		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			if readErr == io.EOF {
				break
			}
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		recordTelemetrySkillBudgetCacheEventForScope(bucket, scope, event)
		if readErr == io.EOF {
			break
		}
	}

	cache.LogPath = filepath.Clean(logPath)
	cache.Offset = committedOffset
	if info, err := os.Stat(logPath); err == nil {
		if info.Size() < cache.Offset {
			cache.Offset = info.Size()
		}
		cache.ModTime = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	cache.ResumeWindowBytes = 0
	cache.ResumeWindowHash = ""
	if resumeWindowBytes, resumeWindowHash := telemetrySkillBudgetResumeAnchor(logPath, cache.Offset); resumeWindowBytes > 0 && resumeWindowHash != "" {
		cache.ResumeWindowBytes = resumeWindowBytes
		cache.ResumeWindowHash = resumeWindowHash
	}
	telemetrySkillBudgetCacheStoreScopedBucket(cache, scope, bucket)
	return nil
}

func telemetrySkillBudgetCacheScopedBucket(cache *telemetrySkillBudgetCache, scope telemetryScope) map[string]*telemetrySkillSummary {
	if cache == nil {
		return map[string]*telemetrySkillSummary{}
	}
	switch {
	case scope.FilteredByTurn && scope.RunID != "" && scope.TurnID != "":
		if turns := cache.Turns[scope.RunID]; len(turns) > 0 {
			return telemetrySkillBudgetBucketToMap(turns[scope.TurnID])
		}
	case scope.FilteredByRun && scope.RunID != "":
		return telemetrySkillBudgetBucketToMap(cache.Runs[scope.RunID])
	}
	return map[string]*telemetrySkillSummary{}
}

func telemetrySkillBudgetCacheStoreScopedBucket(cache *telemetrySkillBudgetCache, scope telemetryScope, bucket map[string]*telemetrySkillSummary) {
	if cache == nil {
		return
	}
	cache.Runs = nil
	cache.Turns = nil
	rows := telemetrySkillBudgetBucketFromMap(bucket)
	switch {
	case scope.FilteredByTurn && scope.RunID != "" && scope.TurnID != "":
		cache.Turns = map[string]map[string][]telemetrySkillSummary{
			scope.RunID: {
				scope.TurnID: rows,
			},
		}
	case scope.FilteredByRun && scope.RunID != "":
		cache.Runs = map[string][]telemetrySkillSummary{
			scope.RunID: rows,
		}
	}
}

func telemetrySkillBudgetBucketToMap(rows []telemetrySkillSummary) map[string]*telemetrySkillSummary {
	out := make(map[string]*telemetrySkillSummary, len(rows))
	for _, row := range rows {
		rowCopy := row
		out[telemetrySkillBudgetKey(rowCopy)] = &rowCopy
	}
	return out
}

func telemetrySkillBudgetBucketFromMap(bucket map[string]*telemetrySkillSummary) []telemetrySkillSummary {
	if len(bucket) == 0 {
		return nil
	}
	rows := make([]telemetrySkillSummary, 0, len(bucket))
	for _, row := range bucket {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count == rows[j].Count {
			if rows[i].Skill == rows[j].Skill {
				return rows[i].Path < rows[j].Path
			}
			return rows[i].Skill < rows[j].Skill
		}
		return rows[i].Count > rows[j].Count
	})
	return rows
}

func recordTelemetrySkillBudgetCacheEventForScope(bucket map[string]*telemetrySkillSummary, scope telemetryScope, event map[string]any) {
	if bucket == nil {
		return
	}
	eventName := telemetryString(event, "event")
	if eventName != "skill_doc_load" && eventName != "skill_reference_load" {
		return
	}
	if !scope.FilteredByRun || scope.RunID == "" || telemetryString(event, "run_id") != scope.RunID {
		return
	}
	if scope.FilteredByTurn && (scope.TurnID == "" || telemetryString(event, "turn_id") != scope.TurnID) {
		return
	}
	recordTelemetrySkillBudgetBucketEvent(bucket, eventName, event)
}

func recordTelemetrySkillBudgetBucketEvent(bucket map[string]*telemetrySkillSummary, eventName string, event map[string]any) {
	skill := safeTelemetryLabel(firstTelemetryString(event, "skill", "skill_name", "skill_id", "skill_slug", "name"))
	if skill == "" {
		skill = "(unknown)"
	}
	path := safeTelemetryPath(firstTelemetryString(event, "skill_path", "reference_path", "doc_path", "path", "file", "reference"))
	key := telemetrySkillBudgetKey(telemetrySkillSummary{Skill: skill, Path: path})
	row := bucket[key]
	if row == nil {
		row = &telemetrySkillSummary{Skill: skill, Path: path}
		bucket[key] = row
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
		return
	}
	row.ReferenceLoads++
}

func telemetrySkillBudgetCacheSkillLoads(cache telemetrySkillBudgetCache, scope telemetryScope) []telemetrySkillSummary {
	var rows []telemetrySkillSummary
	switch {
	case scope.FilteredByTurn && scope.RunID != "" && scope.TurnID != "":
		if turns := cache.Turns[scope.RunID]; len(turns) > 0 {
			rows = turns[scope.TurnID]
		}
	case scope.FilteredByRun && scope.RunID != "":
		rows = cache.Runs[scope.RunID]
	}
	if len(rows) == 0 {
		return nil
	}
	out := make([]telemetrySkillSummary, len(rows))
	copy(out, rows)
	return out
}

func currentSkillContextBudgetAdvisoryBlockWithScope(cwd string, scope contextTelemetryScope) string {
	report, ok := currentSkillContextBudgetReportWithScope(cwd, scope)
	if !ok {
		return ""
	}
	return formatSkillContextBudgetAdvisory(report.Scope, report.SkillBudget)
}

func formatSkillContextBudgetAdvisory(scope telemetryScope, budget telemetrySkillBudget) string {
	if len(budget.Warnings) == 0 {
		return ""
	}
	parts := []string{
		"<!-- NANA:SKILL_CONTEXT_BUDGET:START -->",
		fmt.Sprintf(
			"<skill_context_budget source=%q scope=%q docs=%q references=%q total=%q>",
			"telemetry",
			skillContextBudgetScopeLabel(scope),
			fmt.Sprintf("%d/%d", budget.DocFiles, budget.DocFileBudget),
			fmt.Sprintf("%d/%d", budget.ReferenceFiles, budget.ReferenceFileBudget),
			fmt.Sprintf("%d/%d", budget.TotalFiles, budget.TotalFileBudget),
		),
	}
	for _, warning := range budget.Warnings {
		parts = append(parts, "warning: "+warning.Message)
	}
	parts = append(parts, "</skill_context_budget>", "<!-- NANA:SKILL_CONTEXT_BUDGET:END -->")
	return strings.Join(parts, "\n")
}

func skillContextBudgetScopeLabel(scope telemetryScope) string {
	if scope.FilteredByTurn && scope.TurnID != "" {
		if scope.RunID != "" {
			return fmt.Sprintf("current turn_id=%s within run_id=%s", scope.TurnID, scope.RunID)
		}
		return "current turn_id=" + scope.TurnID
	}
	if scope.FilteredByRun && scope.RunID != "" {
		return "current run_id=" + scope.RunID
	}
	return "current session"
}

func telemetrySkillBudgetKey(row telemetrySkillSummary) string {
	if row.Path != "" {
		return "path\x00" + row.Path
	}
	if row.Skill != "" {
		return "label\x00" + row.Skill
	}
	return "label\x00(unknown)"
}

func telemetrySkillBudgetWarnings(budget telemetrySkillBudget) []telemetryBudgetWarning {
	warnings := []telemetryBudgetWarning{}
	if budget.DocFiles > budget.DocFileBudget {
		warnings = append(warnings, telemetryBudgetWarning{
			Budget:  "skill_doc_files",
			Count:   budget.DocFiles,
			Limit:   budget.DocFileBudget,
			Message: fmt.Sprintf("skill runtime docs loaded %d unique files (budget %d); load only invoked skills and reuse cached runtime content", budget.DocFiles, budget.DocFileBudget),
		})
	}
	if budget.ReferenceFiles > budget.ReferenceFileBudget {
		warnings = append(warnings, telemetryBudgetWarning{
			Budget:  "skill_reference_files",
			Count:   budget.ReferenceFiles,
			Limit:   budget.ReferenceFileBudget,
			Message: fmt.Sprintf("skill references loaded %d unique files (budget %d); avoid deep reference chasing and open only the variant needed", budget.ReferenceFiles, budget.ReferenceFileBudget),
		})
	}
	if budget.TotalFiles > budget.TotalFileBudget {
		warnings = append(warnings, telemetryBudgetWarning{
			Budget:  "skill_total_files",
			Count:   budget.TotalFiles,
			Limit:   budget.TotalFileBudget,
			Message: fmt.Sprintf("skill/reference context loaded %d unique files (budget %d); split work or summarize long sections before loading more context", budget.TotalFiles, budget.TotalFileBudget),
		})
	}
	return warnings
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

	lines = append(lines, "Skill/reference budget:")
	lines = append(lines, fmt.Sprintf("  docs: %d/%d unique files", report.SkillBudget.DocFiles, report.SkillBudget.DocFileBudget))
	lines = append(lines, fmt.Sprintf("  references: %d/%d unique files", report.SkillBudget.ReferenceFiles, report.SkillBudget.ReferenceFileBudget))
	lines = append(lines, fmt.Sprintf("  total: %d/%d unique files", report.SkillBudget.TotalFiles, report.SkillBudget.TotalFileBudget))
	if len(report.SkillBudget.Warnings) == 0 {
		if report.Scope.FilteredByRun {
			lines = append(lines, "  warnings: (none)")
		} else {
			lines = append(lines, "  warnings: n/a for all-runs scope (use --run-id to audit one session)")
		}
	} else {
		for _, warning := range report.SkillBudget.Warnings {
			lines = append(lines, "  warning: "+warning.Message)
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
	if scope.FilteredByTurn {
		if scope.TurnFromEnv {
			return "current turn_id=" + scope.TurnID + " within run_id=" + scope.RunID
		}
		return "turn_id=" + scope.TurnID + " within run_id=" + scope.RunID
	}
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
