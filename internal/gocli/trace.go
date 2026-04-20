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
	"time"
)

const TraceUsage = `nana trace - Report NANA runtime telemetry

Usage:
  nana trace [child-agents] [--since <spec>] [--parent <workflow>] [--json]
  nana trace child-agent start --agent <id> --role <role> [--parent <workflow>] [--queue-depth <n>] [--max-concurrent <n>]
  nana trace child-agent queued --agent <id> --role <role> --queue-depth <n> [--parent <workflow>] [--max-concurrent <n>]
  nana trace child-agent complete --agent <id> --status <status> [--parent <workflow>]

Telemetry is stored as JSONL under .nana/logs/child-agents-YYYY-MM-DD.jsonl.
`

const (
	childAgentTraceSchemaVersion   = 1
	defaultChildAgentMaxConcurrent = 6
)

type childAgentTraceOptions struct {
	JSON             bool
	Since            string
	ParentWorkflowID string
	AgentID          string
	Role             string
	Status           string
	QueueDepth       int
	QueueDepthSet    bool
	MaxConcurrent    int
	MaxConcurrentSet bool
	At               string
}

type childAgentTraceEvent struct {
	SchemaVersion    int    `json:"schema_version"`
	Timestamp        string `json:"timestamp"`
	Event            string `json:"event"`
	ParentWorkflowID string `json:"parent_workflow_id,omitempty"`
	AgentID          string `json:"agent_id"`
	Role             string `json:"role,omitempty"`
	Status           string `json:"status,omitempty"`
	QueueDepth       *int   `json:"queue_depth,omitempty"`
	MaxConcurrent    int    `json:"max_concurrent,omitempty"`
}

type childAgentTraceSummary struct {
	GeneratedAt       string         `json:"generated_at"`
	Events            int            `json:"events"`
	Started           int            `json:"started"`
	Completed         int            `json:"completed"`
	Active            int            `json:"active"`
	Queued            int            `json:"queued"`
	MaxActive         int            `json:"max_active"`
	CurrentQueueDepth int            `json:"current_queue_depth"`
	MaxQueueDepth     int            `json:"max_queue_depth"`
	MaxConcurrent     int            `json:"max_concurrent"`
	AverageDurationMs int64          `json:"average_duration_ms"`
	MaxDurationMs     int64          `json:"max_duration_ms"`
	Outcomes          map[string]int `json:"outcomes,omitempty"`
}

func Trace(cwd string, args []string) error {
	if len(args) == 0 {
		return traceChildAgentSummary(cwd, nil)
	}
	if strings.HasPrefix(args[0], "-") {
		return traceChildAgentSummary(cwd, args)
	}

	switch args[0] {
	case "help", "--help", "-h":
		fmt.Fprint(os.Stdout, TraceUsage)
		return nil
	case "child-agents", "summary":
		return traceChildAgentSummary(cwd, args[1:])
	case "child-agent":
		if len(args) < 2 || isHelpToken(args[1]) {
			fmt.Fprint(os.Stdout, TraceUsage)
			return nil
		}
		return traceChildAgentEvent(cwd, args[1], args[2:])
	default:
		return fmt.Errorf("unknown trace subcommand: %s\n\n%s", args[0], TraceUsage)
	}
}

func traceChildAgentEvent(cwd string, action string, args []string) error {
	normalizedAction := normalizeChildAgentTraceAction(action)
	if normalizedAction == "" {
		return fmt.Errorf("unknown child-agent trace action: %s\n\n%s", action, TraceUsage)
	}

	if traceArgsContainHelp(args) {
		fmt.Fprint(os.Stdout, TraceUsage)
		return nil
	}
	opts, err := parseChildAgentTraceOptions(args)
	if err != nil {
		return err
	}
	if err := validateChildAgentTraceEventOptions(normalizedAction, opts); err != nil {
		return err
	}

	at := time.Now().UTC()
	if strings.TrimSpace(opts.At) != "" {
		parsedAt, err := parseTraceTime(opts.At)
		if err != nil {
			return err
		}
		at = parsedAt.UTC()
	}

	event := childAgentTraceEvent{
		SchemaVersion:    childAgentTraceSchemaVersion,
		Timestamp:        at.Format(time.RFC3339Nano),
		Event:            normalizedAction,
		ParentWorkflowID: resolveTraceParentWorkflowID(cwd, opts.ParentWorkflowID),
		AgentID:          strings.TrimSpace(opts.AgentID),
		Role:             strings.TrimSpace(opts.Role),
		Status:           strings.TrimSpace(opts.Status),
	}
	if opts.MaxConcurrentSet {
		event.MaxConcurrent = opts.MaxConcurrent
	}
	if opts.QueueDepthSet {
		queueDepth := opts.QueueDepth
		event.QueueDepth = &queueDepth
	}
	if err := appendChildAgentTraceEvent(cwd, at, event); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "recorded child-agent %s: %s\n", normalizedAction, event.AgentID)
	return nil
}

func traceChildAgentSummary(cwd string, args []string) error {
	if traceArgsContainHelp(args) {
		fmt.Fprint(os.Stdout, TraceUsage)
		return nil
	}
	opts, err := parseChildAgentTraceOptions(args)
	if err != nil {
		return err
	}
	cutoff := int64(0)
	if strings.TrimSpace(opts.Since) != "" {
		cutoff, err = parseSinceSpec(opts.Since)
		if err != nil {
			return err
		}
	}
	events, err := readChildAgentTraceEvents(cwd, cutoff, opts.ParentWorkflowID)
	if err != nil {
		return err
	}
	summary := summarizeChildAgentTrace(events)
	if opts.JSON {
		payload, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(payload))
		return nil
	}
	fmt.Fprintln(os.Stdout, formatChildAgentTraceSummary(summary))
	return nil
}

func traceArgsContainHelp(args []string) bool {
	for _, arg := range args {
		if isHelpToken(arg) || arg == "help" {
			return true
		}
	}
	return false
}

func normalizeChildAgentTraceAction(action string) string {
	switch strings.TrimSpace(action) {
	case "start", "started":
		return "start"
	case "queued", "queue":
		return "queued"
	case "complete", "completed", "finish", "finished", "stop", "stopped", "done":
		return "complete"
	default:
		return ""
	}
}

func validateChildAgentTraceEventOptions(action string, opts childAgentTraceOptions) error {
	if strings.TrimSpace(opts.AgentID) == "" {
		return fmt.Errorf("child-agent %s requires --agent <id>", action)
	}
	switch action {
	case "start", "queued":
		if strings.TrimSpace(opts.Role) == "" {
			return fmt.Errorf("child-agent %s requires --role <role>", action)
		}
		if action == "queued" && !opts.QueueDepthSet {
			return fmt.Errorf("child-agent queued requires --queue-depth <n>")
		}
	case "complete":
		if strings.TrimSpace(opts.Status) == "" {
			return fmt.Errorf("child-agent complete requires --status <status>")
		}
	}
	return nil
}

func parseChildAgentTraceOptions(args []string) (childAgentTraceOptions, error) {
	opts := childAgentTraceOptions{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			opts.JSON = true
		case arg == "--agent":
			value, next, err := requiredTraceOptionValue(args, index, arg)
			if err != nil {
				return opts, err
			}
			opts.AgentID = value
			index = next
		case strings.HasPrefix(arg, "--agent="):
			opts.AgentID = strings.TrimSpace(strings.TrimPrefix(arg, "--agent="))
		case arg == "--role":
			value, next, err := requiredTraceOptionValue(args, index, arg)
			if err != nil {
				return opts, err
			}
			opts.Role = value
			index = next
		case strings.HasPrefix(arg, "--role="):
			opts.Role = strings.TrimSpace(strings.TrimPrefix(arg, "--role="))
		case arg == "--status":
			value, next, err := requiredTraceOptionValue(args, index, arg)
			if err != nil {
				return opts, err
			}
			opts.Status = value
			index = next
		case strings.HasPrefix(arg, "--status="):
			opts.Status = strings.TrimSpace(strings.TrimPrefix(arg, "--status="))
		case arg == "--parent" || arg == "--parent-workflow" || arg == "--workflow":
			value, next, err := requiredTraceOptionValue(args, index, arg)
			if err != nil {
				return opts, err
			}
			opts.ParentWorkflowID = value
			index = next
		case strings.HasPrefix(arg, "--parent="):
			opts.ParentWorkflowID = strings.TrimSpace(strings.TrimPrefix(arg, "--parent="))
		case strings.HasPrefix(arg, "--parent-workflow="):
			opts.ParentWorkflowID = strings.TrimSpace(strings.TrimPrefix(arg, "--parent-workflow="))
		case strings.HasPrefix(arg, "--workflow="):
			opts.ParentWorkflowID = strings.TrimSpace(strings.TrimPrefix(arg, "--workflow="))
		case arg == "--queue-depth":
			value, next, err := requiredTraceOptionValue(args, index, arg)
			if err != nil {
				return opts, err
			}
			parsed, err := parseTraceNonNegativeInt(value, arg)
			if err != nil {
				return opts, err
			}
			opts.QueueDepth = parsed
			opts.QueueDepthSet = true
			index = next
		case strings.HasPrefix(arg, "--queue-depth="):
			parsed, err := parseTraceNonNegativeInt(strings.TrimPrefix(arg, "--queue-depth="), "--queue-depth")
			if err != nil {
				return opts, err
			}
			opts.QueueDepth = parsed
			opts.QueueDepthSet = true
		case arg == "--max-concurrent":
			value, next, err := requiredTraceOptionValue(args, index, arg)
			if err != nil {
				return opts, err
			}
			parsed, err := parseTracePositiveInt(value, arg)
			if err != nil {
				return opts, err
			}
			opts.MaxConcurrent = parsed
			opts.MaxConcurrentSet = true
			index = next
		case strings.HasPrefix(arg, "--max-concurrent="):
			parsed, err := parseTracePositiveInt(strings.TrimPrefix(arg, "--max-concurrent="), "--max-concurrent")
			if err != nil {
				return opts, err
			}
			opts.MaxConcurrent = parsed
			opts.MaxConcurrentSet = true
		case arg == "--since":
			value, next, err := requiredTraceOptionValue(args, index, arg)
			if err != nil {
				return opts, err
			}
			opts.Since = value
			index = next
		case strings.HasPrefix(arg, "--since="):
			opts.Since = strings.TrimSpace(strings.TrimPrefix(arg, "--since="))
		case arg == "--at":
			value, next, err := requiredTraceOptionValue(args, index, arg)
			if err != nil {
				return opts, err
			}
			opts.At = value
			index = next
		case strings.HasPrefix(arg, "--at="):
			opts.At = strings.TrimSpace(strings.TrimPrefix(arg, "--at="))
		case isHelpToken(arg):
			return opts, fmt.Errorf("%s", TraceUsage)
		default:
			return opts, fmt.Errorf("unknown trace option: %s\n\n%s", arg, TraceUsage)
		}
	}
	return opts, nil
}

func requiredTraceOptionValue(args []string, index int, name string) (string, int, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
		return "", index, fmt.Errorf("missing value after %s", name)
	}
	return strings.TrimSpace(args[index+1]), index + 1, nil
}

func parseTraceNonNegativeInt(value string, name string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid %s value %q", name, value)
	}
	return parsed, nil
}

func parseTracePositiveInt(value string, name string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s value %q", name, value)
	}
	return parsed, nil
}

func parseTraceTime(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("invalid --at timestamp %q", value)
}

func resolveTraceParentWorkflowID(cwd string, explicit string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	for _, name := range []string{"NANA_PARENT_WORKFLOW_ID", "NANA_WORKFLOW_ID", "NANA_RUN_ID"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	if sessionID := ReadCurrentSessionID(cwd); sessionID != "" {
		return sessionID
	}
	return "local"
}

func appendChildAgentTraceEvent(cwd string, at time.Time, event childAgentTraceEvent) error {
	path := childAgentTraceLogPath(cwd, at)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return err
	}
	recordRuntimeArtifactWrite(path)
	return nil
}

func childAgentTraceLogPath(cwd string, at time.Time) string {
	return filepath.Join(cwd, ".nana", "logs", fmt.Sprintf("child-agents-%s.jsonl", at.UTC().Format("2006-01-02")))
}

func readChildAgentTraceEvents(cwd string, cutoffMillis int64, parentWorkflowID string) ([]childAgentTraceEvent, error) {
	logsDir := filepath.Join(cwd, ".nana", "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	filterParent := strings.TrimSpace(parentWorkflowID)
	events := []childAgentTraceEvent{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "child-agents-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(logsDir, entry.Name())
		fileEvents, err := readChildAgentTraceEventsFile(path, cutoffMillis, filterParent)
		if err != nil {
			return nil, err
		}
		events = append(events, fileEvents...)
	}
	sort.SliceStable(events, func(i, j int) bool {
		left, leftErr := parseTraceTime(events[i].Timestamp)
		right, rightErr := parseTraceTime(events[j].Timestamp)
		if leftErr != nil || rightErr != nil {
			return events[i].Timestamp < events[j].Timestamp
		}
		return left.Before(right)
	})
	return events, nil
}

func readChildAgentTraceEventsFile(path string, cutoffMillis int64, parentWorkflowID string) ([]childAgentTraceEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	events := []childAgentTraceEvent{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event childAgentTraceEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.SchemaVersion != childAgentTraceSchemaVersion || strings.TrimSpace(event.Event) == "" || strings.TrimSpace(event.AgentID) == "" {
			continue
		}
		at, err := parseTraceTime(event.Timestamp)
		if err != nil {
			continue
		}
		if cutoffMillis > 0 && at.UnixMilli() < cutoffMillis {
			continue
		}
		if parentWorkflowID != "" && event.ParentWorkflowID != parentWorkflowID {
			continue
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func summarizeChildAgentTrace(events []childAgentTraceEvent) childAgentTraceSummary {
	summary := childAgentTraceSummary{
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Events:        len(events),
		MaxConcurrent: defaultChildAgentMaxConcurrent,
		Outcomes:      map[string]int{},
	}
	running := map[string]time.Time{}
	queued := map[string]time.Time{}
	durationCount := int64(0)
	durationTotal := int64(0)
	// queue_depth is an explicit budget-pressure snapshot from callers; it can
	// be larger than the unique queued agents visible in this filtered log. Keep
	// the latest snapshot fresh by applying queue transitions that happen after
	// it when callers do not emit a newer snapshot.
	currentQueueDepth := 0
	currentQueueDepthSet := false

	for _, event := range events {
		at, err := parseTraceTime(event.Timestamp)
		if err != nil {
			continue
		}
		key := childAgentTraceKey(event)
		action := normalizeChildAgentTraceAction(event.Event)
		if (action == "queued" || action == "start") && event.QueueDepth != nil {
			currentQueueDepth = *event.QueueDepth
			currentQueueDepthSet = true
			if currentQueueDepth > summary.MaxQueueDepth {
				summary.MaxQueueDepth = currentQueueDepth
			}
		}
		if (action == "start" || action == "queued") && event.MaxConcurrent > 0 {
			summary.MaxConcurrent = event.MaxConcurrent
		}
		switch action {
		case "queued":
			queued[key] = at
			summary.MaxQueueDepth = maxInt(summary.MaxQueueDepth, len(queued))
		case "start":
			summary.Started++
			if _, wasQueued := queued[key]; wasQueued && event.QueueDepth == nil {
				currentQueueDepth = decrementTraceQueueDepth(currentQueueDepth)
			}
			delete(queued, key)
			running[key] = at
			if len(running) > summary.MaxActive {
				summary.MaxActive = len(running)
			}
		case "complete":
			summary.Completed++
			status := strings.TrimSpace(event.Status)
			if status == "" {
				status = "completed"
			}
			summary.Outcomes[status]++
			if startedAt, ok := running[key]; ok {
				durationMs := at.Sub(startedAt).Milliseconds()
				if durationMs < 0 {
					durationMs = 0
				}
				durationTotal += durationMs
				durationCount++
				if durationMs > summary.MaxDurationMs {
					summary.MaxDurationMs = durationMs
				}
				delete(running, key)
			}
			if _, wasQueued := queued[key]; wasQueued && currentQueueDepthSet {
				currentQueueDepth = decrementTraceQueueDepth(currentQueueDepth)
			}
			delete(queued, key)
		}
	}

	summary.Active = len(running)
	summary.Queued = len(queued)
	if currentQueueDepthSet {
		summary.CurrentQueueDepth = maxInt(currentQueueDepth, summary.Queued)
	} else {
		summary.CurrentQueueDepth = summary.Queued
	}
	summary.MaxQueueDepth = maxInt(summary.MaxQueueDepth, summary.CurrentQueueDepth)
	if durationCount > 0 {
		summary.AverageDurationMs = durationTotal / durationCount
	}
	if len(summary.Outcomes) == 0 {
		summary.Outcomes = nil
	}
	return summary
}

func childAgentTraceKey(event childAgentTraceEvent) string {
	return strings.TrimSpace(event.ParentWorkflowID) + "\x00" + strings.TrimSpace(event.AgentID)
}

func decrementTraceQueueDepth(depth int) int {
	if depth <= 0 {
		return 0
	}
	return depth - 1
}

func formatChildAgentTraceSummary(summary childAgentTraceSummary) string {
	if summary.Events == 0 {
		return "No child-agent telemetry found. Record events with `nana trace child-agent start --agent <id> --role <role>` and inspect them with `nana trace child-agents`."
	}
	lines := []string{
		"Child-agent telemetry",
		fmt.Sprintf("events=%d started=%d completed=%d", summary.Events, summary.Started, summary.Completed),
		fmt.Sprintf("active=%d/%d max_active=%d queued=%d queue_depth=%d max_queue_depth=%d", summary.Active, summary.MaxConcurrent, summary.MaxActive, summary.Queued, summary.CurrentQueueDepth, summary.MaxQueueDepth),
	}
	if summary.Completed > 0 {
		lines = append(lines, fmt.Sprintf("outcomes=%s avg_duration=%s max_duration=%s", formatTraceOutcomes(summary.Outcomes), formatTraceDuration(summary.AverageDurationMs), formatTraceDuration(summary.MaxDurationMs)))
	}
	return strings.Join(lines, "\n")
}

func formatTraceOutcomes(outcomes map[string]int) string {
	if len(outcomes) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(outcomes))
	for key := range outcomes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, outcomes[key]))
	}
	return strings.Join(parts, ",")
}

func formatTraceDuration(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	duration := time.Duration(ms) * time.Millisecond
	if duration < time.Second {
		return duration.String()
	}
	return duration.Round(time.Second).String()
}
