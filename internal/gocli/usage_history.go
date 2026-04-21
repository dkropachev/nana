package gocli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const threadUsageHistoryArtifactName = "thread-usage-history.json"

type usageTokenCheckpoint struct {
	Timestamp             int64 `json:"timestamp"`
	InputTokens           int   `json:"input_tokens"`
	CachedInputTokens     int   `json:"cached_input_tokens"`
	OutputTokens          int   `json:"output_tokens"`
	ReasoningOutputTokens int   `json:"reasoning_output_tokens"`
	TotalTokens           int   `json:"total_tokens"`
}

type usageHistoryRow struct {
	SessionID   string                 `json:"session_id,omitempty"`
	Nickname    string                 `json:"nickname,omitempty"`
	Role        string                 `json:"role,omitempty"`
	Model       string                 `json:"model,omitempty"`
	CWD         string                 `json:"cwd,omitempty"`
	StartedAt   int64                  `json:"started_at"`
	UpdatedAt   int64                  `json:"updated_at"`
	Checkpoints []usageTokenCheckpoint `json:"checkpoints,omitempty"`
}

type localWorkThreadUsageHistoryArtifact struct {
	Version     int               `json:"version"`
	GeneratedAt string            `json:"generated_at"`
	SandboxPath string            `json:"sandbox_path"`
	Threads     []usageHistoryRow `json:"threads,omitempty"`
}

type githubThreadUsageHistoryArtifact struct {
	Version     int               `json:"version"`
	GeneratedAt string            `json:"generated_at"`
	SandboxPath string            `json:"sandbox_path"`
	Rows        []usageHistoryRow `json:"rows,omitempty"`
}

func usageHistoryArtifactPath(runDir string) string {
	return filepath.Join(runDir, threadUsageHistoryArtifactName)
}

func usageHistoryRowsFromRollouts(sessionsRoot string) ([]usageHistoryRow, error) {
	files := []string{}
	err := filepath.Walk(sessionsRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info != nil && !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Strings(files)

	rows := make([]usageHistoryRow, 0, len(files))
	for _, path := range files {
		row, ok, err := readUsageHistoryRow(path)
		if err != nil {
			return nil, err
		}
		if ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func usageHistoryRowsFromRoots(roots []string) ([]usageHistoryRow, error) {
	rows := []usageHistoryRow{}
	for _, root := range roots {
		found, err := usageHistoryRowsFromRollouts(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		rows = append(rows, found...)
	}
	return mergeUsageHistoryRows(rows), nil
}

func readUsageHistoryRow(filePath string) (usageHistoryRow, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return usageHistoryRow{}, false, err
	}
	defer file.Close()

	row := usageHistoryRow{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		var parsed map[string]any
		_ = json.Unmarshal([]byte(line), &parsed)

		if row.SessionID == "" {
			meta := extractSessionMeta(parsed, filePath)
			row.SessionID = meta.SessionID
		}
		parsedTime, hasTime := usageHistoryEventTime(parsed)
		if hasTime {
			if row.StartedAt == 0 || parsedTime.Unix() < row.StartedAt {
				row.StartedAt = parsedTime.Unix()
			}
			if parsedTime.Unix() > row.UpdatedAt {
				row.UpdatedAt = parsedTime.Unix()
			}
		}
		if parsed["type"] == "session_meta" {
			if payload, ok := parsed["payload"].(map[string]any); ok {
				if sessionID, ok := payload["id"].(string); ok && strings.TrimSpace(sessionID) != "" {
					row.SessionID = strings.TrimSpace(sessionID)
				}
				if cwd, ok := payload["cwd"].(string); ok && strings.TrimSpace(row.CWD) == "" {
					row.CWD = strings.TrimSpace(cwd)
				}
				if nickname, ok := payload["agent_nickname"].(string); ok && strings.TrimSpace(row.Nickname) == "" {
					row.Nickname = strings.TrimSpace(nickname)
				}
				if role, ok := payload["agent_role"].(string); ok && strings.TrimSpace(row.Role) == "" {
					row.Role = strings.TrimSpace(role)
				}
			}
		}
		if parsed["type"] == "turn_context" {
			if payload, ok := parsed["payload"].(map[string]any); ok {
				if model, ok := payload["model"].(string); ok && strings.TrimSpace(row.Model) == "" {
					row.Model = strings.TrimSpace(model)
				}
			}
		}
		if parsed["type"] == "event_msg" {
			if payload, ok := parsed["payload"].(map[string]any); ok && payload["type"] == "token_count" {
				if info, ok := payload["info"].(map[string]any); ok {
					if usage, ok := info["total_token_usage"].(map[string]any); ok && hasTime {
						row.Checkpoints = append(row.Checkpoints, usageTokenCheckpoint{
							Timestamp:             parsedTime.Unix(),
							InputTokens:           usageIntValue(usage["input_tokens"]),
							CachedInputTokens:     usageIntValue(usage["cached_input_tokens"]),
							OutputTokens:          usageIntValue(usage["output_tokens"]),
							ReasoningOutputTokens: usageIntValue(usage["reasoning_output_tokens"]),
							TotalTokens:           usageIntValue(usage["total_tokens"]),
						})
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usageHistoryRow{}, false, err
	}

	row.Checkpoints = normalizeUsageCheckpoints(row.Checkpoints)
	if len(row.Checkpoints) == 0 {
		return usageHistoryRow{}, false, nil
	}
	if row.StartedAt == 0 {
		row.StartedAt = row.Checkpoints[0].Timestamp
	}
	if row.UpdatedAt == 0 {
		row.UpdatedAt = row.Checkpoints[len(row.Checkpoints)-1].Timestamp
	}
	return row, true, nil
}

func usageHistoryEventTime(parsed map[string]any) (time.Time, bool) {
	timestamp, ok := parsed["timestamp"].(string)
	if !ok || strings.TrimSpace(timestamp) == "" {
		return time.Time{}, false
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return parsedTime, true
}

func normalizeUsageCheckpoints(checkpoints []usageTokenCheckpoint) []usageTokenCheckpoint {
	if len(checkpoints) == 0 {
		return nil
	}
	sort.Slice(checkpoints, func(i, j int) bool {
		if checkpoints[i].Timestamp == checkpoints[j].Timestamp {
			return checkpoints[i].TotalTokens < checkpoints[j].TotalTokens
		}
		return checkpoints[i].Timestamp < checkpoints[j].Timestamp
	})
	merged := make([]usageTokenCheckpoint, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		if len(merged) == 0 || merged[len(merged)-1].Timestamp != checkpoint.Timestamp {
			merged = append(merged, checkpoint)
			continue
		}
		last := &merged[len(merged)-1]
		last.InputTokens = max(last.InputTokens, checkpoint.InputTokens)
		last.CachedInputTokens = max(last.CachedInputTokens, checkpoint.CachedInputTokens)
		last.OutputTokens = max(last.OutputTokens, checkpoint.OutputTokens)
		last.ReasoningOutputTokens = max(last.ReasoningOutputTokens, checkpoint.ReasoningOutputTokens)
		last.TotalTokens = max(last.TotalTokens, checkpoint.TotalTokens)
	}
	return merged
}

func usageHistoryRowKey(row usageHistoryRow) string {
	if strings.TrimSpace(row.SessionID) != "" {
		return strings.TrimSpace(row.SessionID)
	}
	return strings.TrimSpace(row.Nickname) + "|" + strings.TrimSpace(row.Role) + "|" + strconv.FormatInt(row.StartedAt, 10)
}

func mergeUsageHistoryRows(rows []usageHistoryRow) []usageHistoryRow {
	if len(rows) == 0 {
		return nil
	}
	index := map[string]usageHistoryRow{}
	order := []string{}
	for _, row := range rows {
		key := usageHistoryRowKey(row)
		existing, ok := index[key]
		if !ok {
			row.Checkpoints = normalizeUsageCheckpoints(row.Checkpoints)
			index[key] = row
			order = append(order, key)
			continue
		}
		if existing.SessionID == "" {
			existing.SessionID = row.SessionID
		}
		if existing.Nickname == "" {
			existing.Nickname = row.Nickname
		}
		if existing.Role == "" {
			existing.Role = row.Role
		}
		if existing.Model == "" {
			existing.Model = row.Model
		}
		if existing.CWD == "" {
			existing.CWD = row.CWD
		}
		if existing.StartedAt == 0 || (row.StartedAt > 0 && row.StartedAt < existing.StartedAt) {
			existing.StartedAt = row.StartedAt
		}
		if row.UpdatedAt > existing.UpdatedAt {
			existing.UpdatedAt = row.UpdatedAt
		}
		existing.Checkpoints = normalizeUsageCheckpoints(append(existing.Checkpoints, row.Checkpoints...))
		index[key] = existing
	}
	merged := make([]usageHistoryRow, 0, len(order))
	for _, key := range order {
		merged = append(merged, index[key])
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].StartedAt == merged[j].StartedAt {
			return usageHistoryRowKey(merged[i]) < usageHistoryRowKey(merged[j])
		}
		if merged[i].StartedAt == 0 {
			return false
		}
		if merged[j].StartedAt == 0 {
			return true
		}
		return merged[i].StartedAt < merged[j].StartedAt
	})
	return merged
}

func usageHistoryLatestCheckpoint(row usageHistoryRow) (usageTokenCheckpoint, bool) {
	if len(row.Checkpoints) == 0 {
		return usageTokenCheckpoint{}, false
	}
	return row.Checkpoints[len(row.Checkpoints)-1], true
}

func usageHistoryTimestampString(updatedAt int64, startedAt int64, fallback string) string {
	switch {
	case updatedAt > 0:
		return time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
	case startedAt > 0:
		return time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
	default:
		return strings.TrimSpace(fallback)
	}
}
