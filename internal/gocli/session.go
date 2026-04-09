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

const SessionHelp = `nana session - Search prior local session history

Usage:
  nana session search <query> [options]

Options:
  --limit <n>          Maximum results to return (default: 10)
  --session <id>       Restrict to a specific session id or id fragment
  --since <spec>       Restrict by recency (examples: 7d, 24h, 2026-03-10)
  --project <scope>    Filter by project context: current | all | <cwd-fragment>
  --context <n>        Snippet context characters (default: 80)
  --case-sensitive     Match query using exact case
  --json               Emit structured JSON
  -h, --help           Show this help
`

type SessionSearchOptions struct {
	Query         string
	Limit         int
	Session       string
	Since         string
	Project       string
	Context       int
	CaseSensitive bool
	JSON          bool
	CWD           string
	CodexHomeDir  string
}

type SessionSearchResult struct {
	SessionID              string `json:"session_id"`
	Timestamp              string `json:"timestamp"`
	CWD                    string `json:"cwd"`
	TranscriptPath         string `json:"transcript_path"`
	TranscriptPathRelative string `json:"transcript_path_relative"`
	RecordType             string `json:"record_type"`
	LineNumber             int    `json:"line_number"`
	Snippet                string `json:"snippet"`
}

type SessionSearchReport struct {
	Query           string                `json:"query"`
	SearchedFiles   int                   `json:"searched_files"`
	MatchedSessions int                   `json:"matched_sessions"`
	Results         []SessionSearchResult `json:"results"`
}

func Session(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(os.Stdout, strings.TrimSpace(SessionHelp))
		return nil
	}
	if args[0] != "search" {
		return fmt.Errorf("unknown session subcommand: %s\n%s", args[0], strings.TrimSpace(SessionHelp))
	}
	opts, err := ParseSessionSearchArgs(args[1:])
	if err != nil {
		return err
	}
	report, err := SearchSessionHistory(opts)
	if err != nil {
		return err
	}
	if opts.JSON {
		payload, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(payload))
		return nil
	}
	fmt.Fprintln(os.Stdout, FormatSessionReport(report))
	return nil
}

func ParseSessionSearchArgs(args []string) (SessionSearchOptions, error) {
	opts := SessionSearchOptions{Limit: 10, Context: 80}
	queryParts := []string{}
	for i := 0; i < len(args); i++ {
		token := args[i]
		switch token {
		case "--json":
			opts.JSON = true
		case "--case-sensitive":
			opts.CaseSensitive = true
		case "--limit", "--session", "--since", "--project", "--context":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return SessionSearchOptions{}, fmt.Errorf("missing value after %s", token)
			}
			value := args[i+1]
			switch token {
			case "--limit":
				v, err := strconv.Atoi(value)
				if err != nil || v < 0 {
					return SessionSearchOptions{}, fmt.Errorf("invalid --limit value %q", value)
				}
				opts.Limit = v
			case "--session":
				opts.Session = value
			case "--since":
				opts.Since = value
			case "--project":
				opts.Project = value
			case "--context":
				v, err := strconv.Atoi(value)
				if err != nil || v < 0 {
					return SessionSearchOptions{}, fmt.Errorf("invalid --context value %q", value)
				}
				opts.Context = v
			}
			i++
		default:
			switch {
			case strings.HasPrefix(token, "--limit="):
				v, err := strconv.Atoi(strings.TrimPrefix(token, "--limit="))
				if err != nil || v < 0 {
					return SessionSearchOptions{}, fmt.Errorf("invalid --limit value")
				}
				opts.Limit = v
			case strings.HasPrefix(token, "--session="):
				opts.Session = strings.TrimPrefix(token, "--session=")
			case strings.HasPrefix(token, "--since="):
				opts.Since = strings.TrimPrefix(token, "--since=")
			case strings.HasPrefix(token, "--project="):
				opts.Project = strings.TrimPrefix(token, "--project=")
			case strings.HasPrefix(token, "--context="):
				v, err := strconv.Atoi(strings.TrimPrefix(token, "--context="))
				if err != nil || v < 0 {
					return SessionSearchOptions{}, fmt.Errorf("invalid --context value")
				}
				opts.Context = v
			case strings.HasPrefix(token, "-"):
				return SessionSearchOptions{}, fmt.Errorf("unknown option: %s", token)
			default:
				queryParts = append(queryParts, token)
			}
		}
	}
	opts.Query = strings.TrimSpace(strings.Join(queryParts, " "))
	if opts.Query == "" {
		return SessionSearchOptions{}, fmt.Errorf("missing search query\n%s", strings.TrimSpace(SessionHelp))
	}
	return opts, nil
}

func SearchSessionHistory(opts SessionSearchOptions) (SessionSearchReport, error) {
	cwd := opts.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return SessionSearchReport{}, err
		}
	}
	codexHomeDir := opts.CodexHomeDir
	if codexHomeDir == "" {
		codexHomeDir = CodexHome()
	}

	files, err := listRolloutFiles(filepath.Join(codexHomeDir, "sessions"))
	if err != nil {
		return SessionSearchReport{}, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	context := opts.Context
	if context <= 0 {
		context = 80
	}
	sinceCutoff, err := parseSinceSpec(opts.Since)
	if err != nil {
		return SessionSearchReport{}, err
	}
	projectFilter := normalizeProjectFilter(opts.Project, cwd)

	results := []SessionSearchResult{}
	matchedSessions := map[string]bool{}
	searchedFiles := 0
	for _, filePath := range files {
		if len(results) >= limit {
			break
		}
		info, err := os.Stat(filePath)
		if err != nil {
			continue
		}
		if sinceCutoff > 0 && info.ModTime().UnixMilli() < sinceCutoff {
			continue
		}
		searchedFiles++
		fileResults, err := searchRolloutFile(filePath, opts.Query, context, limit-len(results), opts.CaseSensitive, opts.Session, projectFilter, sinceCutoff, codexHomeDir)
		if err != nil {
			return SessionSearchReport{}, err
		}
		for _, result := range fileResults {
			results = append(results, result)
			matchedSessions[result.SessionID] = true
			if len(results) >= limit {
				break
			}
		}
	}

	return SessionSearchReport{
		Query:           opts.Query,
		SearchedFiles:   searchedFiles,
		MatchedSessions: len(matchedSessions),
		Results:         results,
	}, nil
}

func FormatSessionReport(report SessionSearchReport) string {
	if len(report.Results) == 0 {
		return fmt.Sprintf("No session history matches for %q. Searched %d transcript(s).", report.Query, report.SearchedFiles)
	}
	lines := []string{
		fmt.Sprintf("Found %d match(es) across %d session(s) in %d transcript(s).", len(report.Results), report.MatchedSessions, report.SearchedFiles),
	}
	for _, result := range report.Results {
		lines = append(lines,
			"",
			fmt.Sprintf("session: %s", result.SessionID),
			fmt.Sprintf("time: %s", emptyOr(result.Timestamp, "unknown")),
			fmt.Sprintf("cwd: %s", emptyOr(result.CWD, "unknown")),
			fmt.Sprintf("source: %s:%d (%s)", result.TranscriptPath, result.LineNumber, result.RecordType),
			fmt.Sprintf("snippet: %s", result.Snippet),
		)
	}
	return strings.Join(lines, "\n")
}

func emptyOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func listRolloutFiles(root string) ([]string, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := []string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files, err
}

func parseSinceSpec(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if strings.HasSuffix(value, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid --since value %q", value)
		}
		return time.Now().Add(-time.Duration(n) * 24 * time.Hour).UnixMilli(), nil
	}
	if strings.HasSuffix(value, "h") {
		n, err := strconv.Atoi(strings.TrimSuffix(value, "h"))
		if err != nil {
			return 0, fmt.Errorf("invalid --since value %q", value)
		}
		return time.Now().Add(-time.Duration(n) * time.Hour).UnixMilli(), nil
	}
	timestamp, err := time.Parse("2006-01-02", value)
	if err == nil {
		return timestamp.UnixMilli(), nil
	}
	return 0, fmt.Errorf("invalid --since value %q", value)
}

func normalizeProjectFilter(project string, cwd string) string {
	project = strings.TrimSpace(project)
	switch project {
	case "", "all":
		return ""
	case "current":
		return cwd
	default:
		return project
	}
}

func searchRolloutFile(filePath string, query string, context int, limit int, caseSensitive bool, sessionFilter string, projectFilter string, sinceCutoff int64, codexHomeDir string) ([]SessionSearchResult, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	type meta struct {
		SessionID string
		Timestamp string
		CWD       string
	}
	var session meta
	lineNumber := 0
	results := []SessionSearchResult{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		var parsed map[string]any
		_ = json.Unmarshal([]byte(line), &parsed)
		if lineNumber == 1 {
			session = extractSessionMeta(parsed, filePath)
			if sessionFilter != "" && !contains(session.SessionID, sessionFilter, caseSensitive) {
				return nil, nil
			}
			if projectFilter != "" && !contains(session.CWD, projectFilter, caseSensitive) {
				return nil, nil
			}
			if sinceCutoff > 0 && session.Timestamp != "" {
				if ts, err := time.Parse(time.RFC3339, session.Timestamp); err == nil && ts.UnixMilli() < sinceCutoff {
					return nil, nil
				}
			}
		}

		for _, candidate := range extractSearchableTexts(parsed, line) {
			snippet := buildSnippet(candidate, query, context, caseSensitive)
			if snippet == "" {
				continue
			}
			results = append(results, SessionSearchResult{
				SessionID:              session.SessionID,
				Timestamp:              session.Timestamp,
				CWD:                    session.CWD,
				TranscriptPath:         filePath,
				TranscriptPathRelative: strings.TrimPrefix(strings.TrimPrefix(filePath, codexHomeDir), string(filepath.Separator)),
				RecordType:             "raw",
				LineNumber:             lineNumber,
				Snippet:                snippet,
			})
			if len(results) >= limit {
				return results, nil
			}
		}
	}
	return results, scanner.Err()
}

func extractSessionMeta(parsed map[string]any, filePath string) struct {
	SessionID string
	Timestamp string
	CWD       string
} {
	fallback := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(filePath), "rollout-"), ".jsonl")
	meta := struct {
		SessionID string
		Timestamp string
		CWD       string
	}{SessionID: fallback}
	if parsed["type"] != "session_meta" {
		return meta
	}
	payload, _ := parsed["payload"].(map[string]any)
	if payload == nil {
		return meta
	}
	if value, ok := payload["id"].(string); ok && strings.TrimSpace(value) != "" {
		meta.SessionID = value
	}
	if value, ok := payload["timestamp"].(string); ok {
		meta.Timestamp = value
	}
	if value, ok := payload["cwd"].(string); ok {
		meta.CWD = value
	}
	return meta
}

func extractSearchableTexts(parsed map[string]any, rawLine string) []string {
	if parsed == nil {
		return []string{rawLine}
	}
	fragments := []string{}
	collectTextFragments(parsed, &fragments)
	if len(fragments) == 0 {
		return []string{rawLine}
	}
	return []string{strings.Join(fragments, " \n ")}
}

func collectTextFragments(value any, fragments *[]string) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			*fragments = append(*fragments, typed)
		}
	case []any:
		for _, item := range typed {
			collectTextFragments(item, fragments)
		}
	case map[string]any:
		for key, child := range typed {
			if key == "base_instructions" || key == "developer_instructions" {
				continue
			}
			collectTextFragments(child, fragments)
		}
	}
}

func buildSnippet(text string, query string, context int, caseSensitive bool) string {
	haystack := text
	needle := query
	if !caseSensitive {
		haystack = strings.ToLower(text)
		needle = strings.ToLower(query)
	}
	index := strings.Index(haystack, needle)
	if index < 0 {
		return ""
	}
	start := index - context
	if start < 0 {
		start = 0
	}
	end := index + len(query) + context
	if end > len(text) {
		end = len(text)
	}
	prefix := ""
	suffix := ""
	if start > 0 {
		prefix = "..."
	}
	if end < len(text) {
		suffix = "..."
	}
	return prefix + strings.Join(strings.Fields(text[start:end]), " ") + suffix
}

func contains(value string, filter string, caseSensitive bool) bool {
	if !caseSensitive {
		value = strings.ToLower(value)
		filter = strings.ToLower(filter)
	}
	return strings.Contains(value, filter)
}
