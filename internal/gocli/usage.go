package gocli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const UsageHelp = `nana usage - Report token spend across NANA-managed sessions

Usage:
  nana usage [summary] [filters] [--json]
  nana usage top [--by <session|cwd|lane|model|activity|phase|day|root>] [--limit <n>] [filters] [--json]
  nana usage group [--by <cwd|lane|model|activity|phase|day|root>] [filters] [--json]
  nana usage analytics [filters] [--json]
  nana usage help

Filters:
  --since <spec>        Restrict by recency (examples: 7d, 24h, 2026-03-10)
  --project <scope>     Filter by project context: current | all | <cwd-fragment>
  --root <name>         all | main | investigate | work | local-work | start
  --activity <name>     Filter by activity classification
  --phase <name>        Filter by phase classification
  --model <name>        Filter by model string
  --json                Emit structured JSON

Examples:
  nana usage
  nana usage top
  nana usage top --by lane --since 7d
  nana usage group --by activity --root work
  nana usage analytics --project current
`

var usageViews = map[string]bool{
	"summary":   true,
	"top":       true,
	"group":     true,
	"analytics": true,
}

var usageRoots = map[string]bool{
	"all":         true,
	"main":        true,
	"investigate": true,
	"work":        true,
	"local-work":  true,
	"start":       true,
}

var usageTopByValues = map[string]bool{
	"session":  true,
	"cwd":      true,
	"lane":     true,
	"model":    true,
	"activity": true,
	"phase":    true,
	"day":      true,
	"root":     true,
}

var usageGroupByValues = map[string]bool{
	"cwd":      true,
	"lane":     true,
	"model":    true,
	"activity": true,
	"phase":    true,
	"day":      true,
	"root":     true,
}

type usageOptions struct {
	View     string
	Since    string
	Project  string
	Root     string
	Activity string
	Phase    string
	Model    string
	By       string
	Limit    int
	JSON     bool
	CWD      string
}

type usageSessionRoot struct {
	Name        string
	SessionsDir string
}

type usageScoutSignals struct {
	InspectRepo bool
	MaxFindings bool
	Improvement bool
	Enhancement bool
	UIScout     bool
	UIPreflight bool
}

type usageRecord struct {
	SessionID             string `json:"session_id"`
	Timestamp             string `json:"timestamp"`
	Day                   string `json:"day"`
	CWD                   string `json:"cwd"`
	TranscriptPath        string `json:"transcript_path"`
	Root                  string `json:"root"`
	Model                 string `json:"model,omitempty"`
	AgentRole             string `json:"agent_role,omitempty"`
	AgentNickname         string `json:"agent_nickname,omitempty"`
	Lane                  string `json:"lane,omitempty"`
	Activity              string `json:"activity"`
	Phase                 string `json:"phase"`
	InputTokens           int    `json:"input_tokens"`
	CachedInputTokens     int    `json:"cached_input_tokens"`
	OutputTokens          int    `json:"output_tokens"`
	ReasoningOutputTokens int    `json:"reasoning_output_tokens"`
	TotalTokens           int    `json:"total_tokens"`
	HasTokenUsage         bool   `json:"has_token_usage"`
}

type usageRollup struct {
	Sessions              int `json:"sessions"`
	MissingTelemetry      int `json:"missing_telemetry"`
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
	TotalTokens           int `json:"total_tokens"`
}

type usageGroupRow struct {
	Key string `json:"key"`
	usageRollup
}

type usageSummaryReport struct {
	SessionRootsScanned int             `json:"session_roots_scanned"`
	Totals              usageRollup     `json:"totals"`
	Roots               []usageGroupRow `json:"roots"`
	Activities          []usageGroupRow `json:"activities"`
	Phases              []usageGroupRow `json:"phases"`
}

type usageTopReport struct {
	By                  string          `json:"by"`
	Limit               int             `json:"limit"`
	SessionRootsScanned int             `json:"session_roots_scanned"`
	Totals              usageRollup     `json:"totals"`
	Sessions            []usageRecord   `json:"sessions,omitempty"`
	Groups              []usageGroupRow `json:"groups,omitempty"`
}

type usageAnalyticsInsight struct {
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	CommandHint string `json:"command_hint,omitempty"`
}

type usageAnalyticsReport struct {
	SessionRootsScanned int                     `json:"session_roots_scanned"`
	Totals              usageRollup             `json:"totals"`
	Insights            []usageAnalyticsInsight `json:"insights"`
}

func Usage(cwd string, args []string) error {
	if len(args) > 0 {
		if isHelpToken(args[0]) || args[0] == "help" {
			fmt.Fprint(os.Stdout, UsageHelp)
			return nil
		}
		if usageViews[args[0]] && len(args) > 1 && isHelpToken(args[1]) {
			fmt.Fprint(os.Stdout, UsageHelp)
			return nil
		}
	}

	options, err := parseUsageArgs(args)
	if err != nil {
		return err
	}
	options.CWD = cwd

	records, sessionRootsScanned, err := collectUsageRecords(options)
	if err != nil {
		return err
	}

	switch options.View {
	case "summary":
		report := buildUsageSummaryReport(records, sessionRootsScanned)
		if options.JSON {
			payload, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(payload))
			return nil
		}
		fmt.Fprintln(os.Stdout, formatUsageSummaryReport(report))
		return nil
	case "top":
		report := buildUsageTopReport(records, sessionRootsScanned, options.By, options.Limit)
		if options.JSON {
			payload, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(payload))
			return nil
		}
		fmt.Fprintln(os.Stdout, formatUsageTopReport(report))
		return nil
	case "group":
		report := buildUsageGroupReport(records, sessionRootsScanned, options.By)
		if options.JSON {
			payload, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(payload))
			return nil
		}
		fmt.Fprintln(os.Stdout, formatUsageGroupReport(report))
		return nil
	case "analytics":
		report := buildUsageAnalyticsReport(records, sessionRootsScanned)
		if options.JSON {
			payload, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(payload))
			return nil
		}
		fmt.Fprintln(os.Stdout, formatUsageAnalyticsReport(report))
		return nil
	default:
		return fmt.Errorf("unknown usage view %q\n%s", options.View, UsageHelp)
	}
}

func parseUsageArgs(args []string) (usageOptions, error) {
	options := usageOptions{
		View:  "summary",
		Root:  "all",
		Limit: 10,
	}
	index := 0
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if !usageViews[args[0]] {
			return usageOptions{}, fmt.Errorf("unknown usage subcommand: %s\n%s", args[0], UsageHelp)
		}
		options.View = args[0]
		index = 1
	}

	bySet := false
	for ; index < len(args); index++ {
		token := args[index]
		switch token {
		case "--json":
			options.JSON = true
		case "--since", "--project", "--root", "--activity", "--phase", "--model", "--by", "--limit":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return usageOptions{}, fmt.Errorf("missing value after %s\n%s", token, UsageHelp)
			}
			value := strings.TrimSpace(args[index+1])
			switch token {
			case "--since":
				options.Since = value
			case "--project":
				options.Project = value
			case "--root":
				options.Root = value
			case "--activity":
				options.Activity = value
			case "--phase":
				options.Phase = value
			case "--model":
				options.Model = value
			case "--by":
				options.By = value
				bySet = true
			case "--limit":
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed <= 0 {
					return usageOptions{}, fmt.Errorf("invalid --limit value %q\n%s", value, UsageHelp)
				}
				options.Limit = parsed
			}
			index++
		default:
			switch {
			case strings.HasPrefix(token, "--since="):
				options.Since = strings.TrimSpace(strings.TrimPrefix(token, "--since="))
			case strings.HasPrefix(token, "--project="):
				options.Project = strings.TrimSpace(strings.TrimPrefix(token, "--project="))
			case strings.HasPrefix(token, "--root="):
				options.Root = strings.TrimSpace(strings.TrimPrefix(token, "--root="))
			case strings.HasPrefix(token, "--activity="):
				options.Activity = strings.TrimSpace(strings.TrimPrefix(token, "--activity="))
			case strings.HasPrefix(token, "--phase="):
				options.Phase = strings.TrimSpace(strings.TrimPrefix(token, "--phase="))
			case strings.HasPrefix(token, "--model="):
				options.Model = strings.TrimSpace(strings.TrimPrefix(token, "--model="))
			case strings.HasPrefix(token, "--by="):
				options.By = strings.TrimSpace(strings.TrimPrefix(token, "--by="))
				bySet = true
			case strings.HasPrefix(token, "--limit="):
				parsed, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(token, "--limit=")))
				if err != nil || parsed <= 0 {
					return usageOptions{}, fmt.Errorf("invalid --limit value\n%s", UsageHelp)
				}
				options.Limit = parsed
			default:
				return usageOptions{}, fmt.Errorf("unknown option: %s\n%s", token, UsageHelp)
			}
		}
	}

	options.Root = strings.TrimSpace(options.Root)
	if options.Root == "" {
		options.Root = "all"
	}
	if !usageRoots[options.Root] {
		return usageOptions{}, fmt.Errorf("invalid --root value %q\n%s", options.Root, UsageHelp)
	}

	if bySet {
		options.By = strings.TrimSpace(options.By)
	}
	switch options.View {
	case "summary", "analytics":
		if bySet {
			return usageOptions{}, fmt.Errorf("--by is only supported by `nana usage top` and `nana usage group`\n%s", UsageHelp)
		}
	case "top":
		if options.By == "" {
			options.By = "session"
		}
		if !usageTopByValues[options.By] {
			return usageOptions{}, fmt.Errorf("invalid --by value %q for `nana usage top`\n%s", options.By, UsageHelp)
		}
	case "group":
		if options.By == "" {
			options.By = "activity"
		}
		if !usageGroupByValues[options.By] {
			return usageOptions{}, fmt.Errorf("invalid --by value %q for `nana usage group`\n%s", options.By, UsageHelp)
		}
	}

	if _, err := parseSinceSpec(options.Since); err != nil {
		return usageOptions{}, err
	}
	return options, nil
}

func collectUsageRecords(options usageOptions) ([]usageRecord, int, error) {
	sessionRoots, err := discoverUsageSessionRoots(options.CWD)
	if err != nil {
		return nil, 0, err
	}
	projectFilter := normalizeUsageProjectFilter(options.Project, options.CWD)
	projectRepoID := ""
	if projectFilter != "" {
		if info, err := os.Stat(projectFilter); err == nil && info.IsDir() {
			projectRepoID = localWorkRepoID(projectFilter)
		}
	}
	sinceCutoff, err := parseSinceSpec(options.Since)
	if err != nil {
		return nil, 0, err
	}

	records := []usageRecord{}
	for _, root := range sessionRoots {
		if options.Root != "all" && root.Name != options.Root {
			continue
		}
		err := walkRolloutFiles(root.SessionsDir, sinceCutoff, func(path string) (bool, error) {
			record, err := parseUsageRollout(path, root.Name)
			if err != nil {
				return false, err
			}
			if !usageRecordMatchesFilters(record, options, projectFilter, projectRepoID) {
				return false, nil
			}
			records = append(records, record)
			return false, nil
		})
		if err != nil {
			return nil, 0, err
		}
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Timestamp == records[j].Timestamp {
			return records[i].SessionID < records[j].SessionID
		}
		return records[i].Timestamp > records[j].Timestamp
	})
	return records, len(sessionRoots), nil
}

func discoverUsageSessionRoots(cwd string) ([]usageSessionRoot, error) {
	roots := []usageSessionRoot{}
	seen := map[string]bool{}
	addDirect := func(name string, sessionsDir string) {
		sessionsDir = filepath.Clean(strings.TrimSpace(sessionsDir))
		if sessionsDir == "" || seen[sessionsDir] {
			return
		}
		info, err := os.Lstat(sessionsDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return
		}
		seen[sessionsDir] = true
		roots = append(roots, usageSessionRoot{Name: name, SessionsDir: sessionsDir})
	}

	addDirect("main", filepath.Join(DefaultUserCodexHome(os.Getenv("HOME")), "sessions"))
	addDirect("main", filepath.Join(CodexHome(), "sessions"))
	addDirect("main", filepath.Join(ResolveCodexHomeForLaunch(cwd), "sessions"))
	addDirect("investigate", filepath.Join(DefaultUserInvestigateCodexHome(os.Getenv("HOME")), "sessions"))
	addDirect("investigate", filepath.Join(ResolveInvestigateCodexHome(cwd), "sessions"))

	for _, base := range []string{
		workHomeRoot(),
		localWorkHomeRoot(),
		filepath.Join(cwd, ".nana", "state", "investigate-probes"),
	} {
		if err := discoverUsageSessionRootsRecursive(base, &roots, seen); err != nil {
			return nil, err
		}
	}

	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Name == roots[j].Name {
			return roots[i].SessionsDir < roots[j].SessionsDir
		}
		return roots[i].Name < roots[j].Name
	})
	return roots, nil
}

func discoverUsageSessionRootsRecursive(base string, roots *[]usageSessionRoot, seen map[string]bool) error {
	info, err := os.Lstat(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() || entry.Name() != "sessions" {
			return nil
		}
		rootName := classifyUsageRootFromSessionsDir(path)
		if rootName == "" {
			return filepath.SkipDir
		}
		cleanPath := filepath.Clean(path)
		if seen[cleanPath] {
			return filepath.SkipDir
		}
		seen[cleanPath] = true
		*roots = append(*roots, usageSessionRoot{Name: rootName, SessionsDir: cleanPath})
		return filepath.SkipDir
	})
}

func classifyUsageRootFromSessionsDir(path string) string {
	slash := filepath.ToSlash(filepath.Clean(path))
	switch {
	case strings.Contains(slash, "/.nana/start/codex-home/"):
		return "start"
	case strings.Contains(slash, "/.nana/work-local/codex-home/"):
		return "local-work"
	case strings.Contains(slash, "/.nana/work/codex-home/"):
		return "work"
	case strings.Contains(slash, "/investigate-probes/"):
		return "investigate"
	default:
		return ""
	}
}

func parseUsageRollout(filePath string, rootName string) (usageRecord, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return usageRecord{}, err
	}
	defer file.Close()

	record := usageRecord{
		TranscriptPath: filePath,
		Root:           rootName,
	}
	scoutSignals := usageScoutSignals{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		var parsed map[string]any
		_ = json.Unmarshal([]byte(line), &parsed)

		if record.SessionID == "" {
			meta := extractSessionMeta(parsed, filePath)
			record.SessionID = meta.SessionID
			record.Timestamp = meta.Timestamp
			record.CWD = meta.CWD
		}

		if parsed["type"] == "session_meta" {
			if payload, ok := parsed["payload"].(map[string]any); ok {
				if value, ok := payload["agent_role"].(string); ok && strings.TrimSpace(record.AgentRole) == "" {
					record.AgentRole = strings.TrimSpace(value)
				}
				if value, ok := payload["agent_nickname"].(string); ok && strings.TrimSpace(record.AgentNickname) == "" {
					record.AgentNickname = strings.TrimSpace(value)
				}
				if value, ok := payload["cwd"].(string); ok && strings.TrimSpace(record.CWD) == "" {
					record.CWD = strings.TrimSpace(value)
				}
			}
		}

		if parsed["type"] == "turn_context" {
			if payload, ok := parsed["payload"].(map[string]any); ok {
				if value, ok := payload["model"].(string); ok && strings.TrimSpace(record.Model) == "" {
					record.Model = strings.TrimSpace(value)
				}
			}
		}

		if parsed["type"] == "event_msg" {
			payload, _ := parsed["payload"].(map[string]any)
			if payload != nil && payload["type"] == "token_count" {
				if info, ok := payload["info"].(map[string]any); ok {
					if usage, ok := info["total_token_usage"].(map[string]any); ok {
						record.InputTokens = usageIntValue(usage["input_tokens"])
						record.CachedInputTokens = usageIntValue(usage["cached_input_tokens"])
						record.OutputTokens = usageIntValue(usage["output_tokens"])
						record.ReasoningOutputTokens = usageIntValue(usage["reasoning_output_tokens"])
						record.TotalTokens = usageIntValue(usage["total_tokens"])
						record.HasTokenUsage = true
					}
				}
			}
		}

		markUsageScoutSignals(strings.ToLower(line), &scoutSignals)
	}
	if err := scanner.Err(); err != nil {
		return usageRecord{}, err
	}

	record.Day = usageRecordDay(record.Timestamp, filePath)
	record.Lane = usageLaneFromPath(filePath, record.AgentRole)
	record.Activity = classifyUsageActivity(rootName, record, scoutSignals)
	record.Phase = classifyUsagePhase(record)
	return record, nil
}

func markUsageScoutSignals(lowerLine string, signals *usageScoutSignals) {
	if signals == nil {
		return
	}
	if strings.Contains(lowerLine, "- inspect repo:") {
		signals.InspectRepo = true
	}
	if strings.Contains(lowerLine, "- max findings/issues to emit:") {
		signals.MaxFindings = true
	}
	if strings.Contains(lowerLine, "improvement-scout") {
		signals.Improvement = true
	}
	if strings.Contains(lowerLine, "enhancement-scout") {
		signals.Enhancement = true
	}
	if strings.Contains(lowerLine, "ui-scout") {
		signals.UIScout = true
	}
	if strings.Contains(lowerLine, "you are running a short preflight for ui-scout") {
		signals.UIPreflight = true
	}
}

func usageIntValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func usageRecordDay(timestamp string, filePath string) string {
	if len(timestamp) >= 10 {
		return timestamp[:10]
	}
	dir := filepath.Dir(filePath)
	day := filepath.Base(dir)
	month := filepath.Base(filepath.Dir(dir))
	year := filepath.Base(filepath.Dir(filepath.Dir(dir)))
	if len(year) == 4 && len(month) == 2 && len(day) == 2 {
		return year + "-" + month + "-" + day
	}
	return ""
}

func usageLaneFromPath(filePath string, fallback string) string {
	slash := filepath.ToSlash(filePath)
	if index := strings.Index(slash, "/codex-home/"); index >= 0 {
		rest := slash[index+len("/codex-home/"):]
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 && parts[0] != "" && parts[1] == "sessions" {
			return parts[0]
		}
	}
	return strings.TrimSpace(fallback)
}

func classifyUsageActivity(rootName string, record usageRecord, scoutSignals usageScoutSignals) string {
	switch rootName {
	case "investigate":
		return "investigate"
	case "start":
		if strings.EqualFold(record.Lane, "triage") {
			return "triage"
		}
		return "start"
	case "work", "local-work":
		return "work"
	case "main":
		if scoutSignals.UIPreflight || (scoutSignals.InspectRepo && scoutSignals.MaxFindings && (scoutSignals.Improvement || scoutSignals.Enhancement || scoutSignals.UIScout)) {
			return "scout"
		}
		return "interactive"
	default:
		if scoutSignals.UIPreflight || (scoutSignals.InspectRepo && scoutSignals.MaxFindings && (scoutSignals.Improvement || scoutSignals.Enhancement || scoutSignals.UIScout)) {
			return "scout"
		}
		return "unknown"
	}
}

func classifyUsagePhase(record usageRecord) string {
	normalized := strings.ToLower(strings.TrimSpace(record.Lane))
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimSpace(record.AgentRole))
	}
	switch {
	case normalized == "triage":
		return "triage"
	case strings.Contains(normalized, "validator"):
		return "validation"
	case normalized == "qa-tester":
		return "qa"
	case normalized == "security-reviewer":
		return "security"
	case normalized == "performance-reviewer":
		return "performance"
	case normalized == "reviewer" || normalized == "quality-reviewer" || strings.Contains(normalized, "reviewer") || normalized == "architect":
		return "review"
	case normalized == "leader" || strings.HasPrefix(normalized, "hardener") || strings.HasPrefix(normalized, "grouper") || strings.Contains(normalized, "executor") || normalized == "coder" || normalized == "test-engineer":
		return "implementation"
	}

	switch record.Activity {
	case "scout":
		return "scout"
	case "investigate":
		return "investigate"
	case "interactive":
		return "interactive"
	case "triage":
		return "triage"
	default:
		return "other"
	}
}

func normalizeUsageProjectFilter(project string, cwd string) string {
	project = strings.TrimSpace(project)
	switch project {
	case "", "all":
		return ""
	case "current":
		return resolveInvestigateWorkspaceRoot(cwd)
	default:
		return project
	}
}

func usageRecordMatchesFilters(record usageRecord, options usageOptions, projectFilter string, projectRepoID string) bool {
	if projectFilter != "" {
		if !usageRecordMatchesProject(record, projectFilter, projectRepoID) {
			return false
		}
	}
	if options.Root != "all" && !strings.EqualFold(record.Root, options.Root) {
		return false
	}
	if strings.TrimSpace(options.Activity) != "" && !strings.EqualFold(record.Activity, options.Activity) {
		return false
	}
	if strings.TrimSpace(options.Phase) != "" && !strings.EqualFold(record.Phase, options.Phase) {
		return false
	}
	if strings.TrimSpace(options.Model) != "" && !contains(record.Model, options.Model, false) {
		return false
	}
	return true
}

func usageRecordMatchesProject(record usageRecord, projectFilter string, projectRepoID string) bool {
	if contains(record.CWD, projectFilter, false) || contains(record.TranscriptPath, projectFilter, false) {
		return true
	}
	if projectRepoID != "" {
		sandboxNeedle := "/sandboxes/" + projectRepoID + "/"
		if strings.Contains(filepath.ToSlash(record.CWD), sandboxNeedle) || strings.Contains(filepath.ToSlash(record.TranscriptPath), sandboxNeedle) {
			return true
		}
	}
	return false
}

func buildUsageSummaryReport(records []usageRecord, sessionRootsScanned int) usageSummaryReport {
	return usageSummaryReport{
		SessionRootsScanned: sessionRootsScanned,
		Totals:              buildUsageRollup(records),
		Roots:               buildUsageGroups(records, "root"),
		Activities:          buildUsageGroups(records, "activity"),
		Phases:              buildUsageGroups(records, "phase"),
	}
}

func buildUsageTopReport(records []usageRecord, sessionRootsScanned int, by string, limit int) usageTopReport {
	report := usageTopReport{
		By:                  by,
		Limit:               limit,
		SessionRootsScanned: sessionRootsScanned,
		Totals:              buildUsageRollup(records),
	}
	if by == "session" {
		sessions := append([]usageRecord(nil), records...)
		sort.Slice(sessions, func(i, j int) bool {
			if sessions[i].TotalTokens == sessions[j].TotalTokens {
				if sessions[i].Timestamp == sessions[j].Timestamp {
					return sessions[i].SessionID < sessions[j].SessionID
				}
				return sessions[i].Timestamp > sessions[j].Timestamp
			}
			return sessions[i].TotalTokens > sessions[j].TotalTokens
		})
		if len(sessions) > limit {
			sessions = sessions[:limit]
		}
		report.Sessions = sessions
		return report
	}
	groups := buildUsageGroups(records, by)
	if len(groups) > limit {
		groups = groups[:limit]
	}
	report.Groups = groups
	return report
}

func buildUsageGroupReport(records []usageRecord, sessionRootsScanned int, by string) usageTopReport {
	return usageTopReport{
		By:                  by,
		SessionRootsScanned: sessionRootsScanned,
		Totals:              buildUsageRollup(records),
		Groups:              buildUsageGroups(records, by),
	}
}

func buildUsageAnalyticsReport(records []usageRecord, sessionRootsScanned int) usageAnalyticsReport {
	insights := []usageAnalyticsInsight{}
	totals := buildUsageRollup(records)
	topSessions := buildUsageTopReport(records, sessionRootsScanned, "session", 1).Sessions
	if len(topSessions) > 0 {
		top := topSessions[0]
		insights = append(insights, usageAnalyticsInsight{
			Title:       "Biggest session",
			Summary:     fmt.Sprintf("%s used %s tokens in %s (%s / %s).", top.SessionID, formatGithubTokenCount(top.TotalTokens), defaultString(top.Root, "unknown"), defaultString(top.Activity, "unknown"), defaultString(top.Phase, "other")),
			CommandHint: "nana usage top",
		})
	}
	if groups := buildUsageGroups(records, "activity"); len(groups) > 0 {
		top := groups[0]
		insights = append(insights, usageAnalyticsInsight{
			Title:       "Most expensive activity",
			Summary:     fmt.Sprintf("%s accounts for %s tokens across %d session(s).", top.Key, formatGithubTokenCount(top.TotalTokens), top.Sessions),
			CommandHint: "nana usage group --by activity",
		})
	}
	if groups := buildUsageGroups(records, "day"); len(groups) > 0 {
		top := groups[0]
		insights = append(insights, usageAnalyticsInsight{
			Title:       "Most expensive day",
			Summary:     fmt.Sprintf("%s accounts for %s tokens across %d session(s).", top.Key, formatGithubTokenCount(top.TotalTokens), top.Sessions),
			CommandHint: "nana usage group --by day",
		})
	}
	if totals.TotalTokens > 0 {
		inputShare := usageSharePercent(totals.InputTokens+totals.CachedInputTokens, totals.TotalTokens)
		outputShare := usageSharePercent(totals.OutputTokens+totals.ReasoningOutputTokens, totals.TotalTokens)
		insights = append(insights, usageAnalyticsInsight{
			Title:       "Input/output mix",
			Summary:     fmt.Sprintf("Input-side tokens are %.1f%% of total; output-side tokens are %.1f%%.", inputShare, outputShare),
			CommandHint: "nana usage summary",
		})
	}
	implementation := usageRollupForPhase(records, "implementation")
	review := usageRollupForPhase(records, "review")
	validation := usageRollupForPhase(records, "validation")
	if implementation.TotalTokens > 0 || review.TotalTokens > 0 || validation.TotalTokens > 0 {
		insights = append(insights, usageAnalyticsInsight{
			Title:       "Implementation vs review burden",
			Summary:     fmt.Sprintf("Implementation=%s, review=%s, validation=%s tokens.", formatGithubTokenCount(implementation.TotalTokens), formatGithubTokenCount(review.TotalTokens), formatGithubTokenCount(validation.TotalTokens)),
			CommandHint: "nana usage group --by phase",
		})
	}
	if totals.MissingTelemetry > 0 {
		insights = append(insights, usageAnalyticsInsight{
			Title:       "Missing telemetry",
			Summary:     fmt.Sprintf("%d session(s) had no token_count telemetry; they are included in session counts but not cost totals.", totals.MissingTelemetry),
			CommandHint: "nana usage top --by root",
		})
	}
	insights = append(insights,
		usageAnalyticsInsight{
			Title:       "Other angles",
			Summary:     "Inspect by lane to find expensive reviewers or validators.",
			CommandHint: "nana usage top --by lane",
		},
		usageAnalyticsInsight{
			Title:       "Other angles",
			Summary:     "Inspect by model to confirm expensive runs stayed on the expected model mix.",
			CommandHint: "nana usage group --by model",
		},
	)
	return usageAnalyticsReport{
		SessionRootsScanned: sessionRootsScanned,
		Totals:              totals,
		Insights:            insights,
	}
}

func buildUsageRollup(records []usageRecord) usageRollup {
	rollup := usageRollup{}
	for _, record := range records {
		rollup.Sessions++
		if !record.HasTokenUsage {
			rollup.MissingTelemetry++
		}
		rollup.InputTokens += record.InputTokens
		rollup.CachedInputTokens += record.CachedInputTokens
		rollup.OutputTokens += record.OutputTokens
		rollup.ReasoningOutputTokens += record.ReasoningOutputTokens
		rollup.TotalTokens += record.TotalTokens
	}
	return rollup
}

func buildUsageGroups(records []usageRecord, by string) []usageGroupRow {
	index := map[string]*usageGroupRow{}
	for _, record := range records {
		key := usageGroupKey(record, by)
		if strings.TrimSpace(key) == "" {
			key = "(unknown)"
		}
		row := index[key]
		if row == nil {
			row = &usageGroupRow{Key: key}
			index[key] = row
		}
		row.Sessions++
		if !record.HasTokenUsage {
			row.MissingTelemetry++
		}
		row.InputTokens += record.InputTokens
		row.CachedInputTokens += record.CachedInputTokens
		row.OutputTokens += record.OutputTokens
		row.ReasoningOutputTokens += record.ReasoningOutputTokens
		row.TotalTokens += record.TotalTokens
	}
	groups := make([]usageGroupRow, 0, len(index))
	for _, row := range index {
		groups = append(groups, *row)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].TotalTokens == groups[j].TotalTokens {
			return groups[i].Key < groups[j].Key
		}
		return groups[i].TotalTokens > groups[j].TotalTokens
	})
	return groups
}

func usageGroupKey(record usageRecord, by string) string {
	switch by {
	case "session":
		return record.SessionID
	case "cwd":
		return defaultString(strings.TrimSpace(record.CWD), "(unknown)")
	case "lane":
		return defaultString(strings.TrimSpace(record.Lane), "(unknown)")
	case "model":
		return defaultString(strings.TrimSpace(record.Model), "(unknown)")
	case "activity":
		return defaultString(strings.TrimSpace(record.Activity), "(unknown)")
	case "phase":
		return defaultString(strings.TrimSpace(record.Phase), "(unknown)")
	case "day":
		return defaultString(strings.TrimSpace(record.Day), "(unknown)")
	case "root":
		return defaultString(strings.TrimSpace(record.Root), "(unknown)")
	default:
		return record.SessionID
	}
}

func usageRollupForPhase(records []usageRecord, phase string) usageRollup {
	filtered := []usageRecord{}
	for _, record := range records {
		if strings.EqualFold(record.Phase, phase) {
			filtered = append(filtered, record)
		}
	}
	return buildUsageRollup(filtered)
}

func usageSharePercent(part int, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) * 100.0 / float64(total)
}

func formatUsageSummaryReport(report usageSummaryReport) string {
	lines := []string{
		fmt.Sprintf("Scanned %d session root(s); found %d session(s).", report.SessionRootsScanned, report.Totals.Sessions),
		fmt.Sprintf("Total tokens: %d (%s)", report.Totals.TotalTokens, formatGithubTokenCount(report.Totals.TotalTokens)),
		fmt.Sprintf("Input tokens: %d (%s)", report.Totals.InputTokens, formatGithubTokenCount(report.Totals.InputTokens)),
		fmt.Sprintf("Cached input tokens: %d (%s)", report.Totals.CachedInputTokens, formatGithubTokenCount(report.Totals.CachedInputTokens)),
		fmt.Sprintf("Output tokens: %d (%s)", report.Totals.OutputTokens, formatGithubTokenCount(report.Totals.OutputTokens)),
		fmt.Sprintf("Reasoning output tokens: %d (%s)", report.Totals.ReasoningOutputTokens, formatGithubTokenCount(report.Totals.ReasoningOutputTokens)),
		fmt.Sprintf("Missing token telemetry: %d", report.Totals.MissingTelemetry),
	}
	lines = append(lines, "", "Top roots:")
	lines = append(lines, formatUsageGroupLines(report.Roots, 5)...)
	lines = append(lines, "", "Top activities:")
	lines = append(lines, formatUsageGroupLines(report.Activities, 5)...)
	lines = append(lines, "", "Top phases:")
	lines = append(lines, formatUsageGroupLines(report.Phases, 5)...)
	return strings.Join(lines, "\n")
}

func formatUsageTopReport(report usageTopReport) string {
	lines := []string{
		fmt.Sprintf("Scanned %d session root(s); found %d session(s).", report.SessionRootsScanned, report.Totals.Sessions),
		fmt.Sprintf("Top %d by %s:", report.Limit, report.By),
	}
	if report.By == "session" {
		if len(report.Sessions) == 0 {
			lines = append(lines, "- no matching sessions")
			return strings.Join(lines, "\n")
		}
		for index, session := range report.Sessions {
			lines = append(lines, fmt.Sprintf("%d. %s tokens=%d (%s) root=%s activity=%s phase=%s lane=%s model=%s",
				index+1,
				defaultString(session.SessionID, "(unknown)"),
				session.TotalTokens,
				formatGithubTokenCount(session.TotalTokens),
				defaultString(session.Root, "(unknown)"),
				defaultString(session.Activity, "(unknown)"),
				defaultString(session.Phase, "(unknown)"),
				defaultString(session.Lane, "(unknown)"),
				defaultString(session.Model, "(unknown)"),
			))
		}
		return strings.Join(lines, "\n")
	}
	if len(report.Groups) == 0 {
		lines = append(lines, "- no matching groups")
		return strings.Join(lines, "\n")
	}
	for _, group := range report.Groups {
		lines = append(lines, fmt.Sprintf("- %s: tokens=%d (%s) sessions=%d missing=%d", group.Key, group.TotalTokens, formatGithubTokenCount(group.TotalTokens), group.Sessions, group.MissingTelemetry))
	}
	return strings.Join(lines, "\n")
}

func formatUsageGroupReport(report usageTopReport) string {
	lines := []string{
		fmt.Sprintf("Scanned %d session root(s); found %d session(s).", report.SessionRootsScanned, report.Totals.Sessions),
		fmt.Sprintf("Grouped by %s:", report.By),
	}
	if len(report.Groups) == 0 {
		lines = append(lines, "- no matching groups")
		return strings.Join(lines, "\n")
	}
	for _, group := range report.Groups {
		lines = append(lines, fmt.Sprintf("- %s: tokens=%d (%s) sessions=%d missing=%d", group.Key, group.TotalTokens, formatGithubTokenCount(group.TotalTokens), group.Sessions, group.MissingTelemetry))
	}
	return strings.Join(lines, "\n")
}

func formatUsageAnalyticsReport(report usageAnalyticsReport) string {
	lines := []string{
		fmt.Sprintf("Scanned %d session root(s); found %d session(s).", report.SessionRootsScanned, report.Totals.Sessions),
		fmt.Sprintf("Total tokens: %d (%s)", report.Totals.TotalTokens, formatGithubTokenCount(report.Totals.TotalTokens)),
		"",
		"Analytics:",
	}
	if len(report.Insights) == 0 {
		lines = append(lines, "- no insights available")
		return strings.Join(lines, "\n")
	}
	for _, insight := range report.Insights {
		line := fmt.Sprintf("- %s: %s", insight.Title, insight.Summary)
		if strings.TrimSpace(insight.CommandHint) != "" {
			line += fmt.Sprintf(" [%s]", insight.CommandHint)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func formatUsageGroupLines(groups []usageGroupRow, limit int) []string {
	if len(groups) == 0 {
		return []string{"- no matching groups"}
	}
	if limit > 0 && len(groups) > limit {
		groups = groups[:limit]
	}
	lines := make([]string, 0, len(groups))
	for _, group := range groups {
		lines = append(lines, fmt.Sprintf("- %s: tokens=%d (%s) sessions=%d missing=%d", group.Key, group.TotalTokens, formatGithubTokenCount(group.TotalTokens), group.Sessions, group.MissingTelemetry))
	}
	return lines
}
