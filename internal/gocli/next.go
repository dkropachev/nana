package gocli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const NextUsage = `nana next - Show the highest-priority item that needs operator attention

Usage:
  nana next [--json]

Behavior:
  - inspects approvals, work runs, work items, investigations, and tracked issues
  - ranks failed and blocked items ahead of active and queued items
  - prints exactly one recommended next command when work needs attention
`

type attentionItem struct {
	Kind               string `json:"kind"`
	ID                 string `json:"id"`
	RepoSlug           string `json:"repo_slug,omitempty"`
	Summary            string `json:"summary"`
	Reason             string `json:"reason,omitempty"`
	AttentionState     string `json:"attention_state,omitempty"`
	RecommendedCommand string `json:"recommended_command,omitempty"`
	TargetURL          string `json:"target_url,omitempty"`
	RunID              string `json:"run_id,omitempty"`
	ItemID             string `json:"item_id,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
}

type attentionReport struct {
	GeneratedAt  string          `json:"generated_at"`
	ActiveModeID string          `json:"active_mode_id,omitempty"`
	ActiveMode   string          `json:"active_mode,omitempty"`
	ActivePhase  string          `json:"active_phase,omitempty"`
	Items        []attentionItem `json:"items"`
	Next         *attentionItem  `json:"next,omitempty"`
}

func Next(cwd string, args []string) error {
	return nextWithIO(cwd, args, os.Stdout)
}

func nextWithIO(cwd string, args []string, stdout io.Writer) error {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--help", "-h", "help":
			fmt.Fprint(stdout, NextUsage)
			return nil
		case "--json":
			jsonOutput = true
		default:
			return fmt.Errorf("unknown next option: %s\n\n%s", arg, NextUsage)
		}
	}

	report, err := buildAttentionReport(cwd)
	if err != nil {
		return err
	}
	if jsonOutput {
		payload, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(payload))
		return nil
	}

	fmt.Fprintln(stdout, formatAttentionReport(report))
	return nil
}

func buildAttentionReport(cwd string) (attentionReport, error) {
	modeID, modePhase := resolveAttentionActiveMode(cwd)
	items, err := listAttentionItems(cwd)
	if err != nil {
		return attentionReport{}, err
	}
	report := attentionReport{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		ActiveModeID: modeID,
		ActiveMode:   strings.TrimSpace(modeID),
		ActivePhase:  modePhase,
		Items:        items,
	}
	if len(items) > 0 {
		next := items[0]
		report.Next = &next
	}
	return report, nil
}

func formatAttentionReport(report attentionReport) string {
	lines := []string{}
	if strings.TrimSpace(report.ActiveMode) != "" {
		label := report.ActiveMode
		if strings.TrimSpace(report.ActivePhase) != "" {
			label += " (" + report.ActivePhase + ")"
		}
		lines = append(lines, "Mode: "+label)
	}
	if report.Next == nil {
		if len(lines) == 0 {
			return "Nothing needs operator attention right now."
		}
		lines = append(lines, "Attention: none")
		return strings.Join(lines, "\n")
	}

	item := report.Next
	lines = append(lines,
		"Attention: "+defaultString(item.AttentionState, "queued")+" "+item.Kind,
		"Item: "+item.Summary,
	)
	if strings.TrimSpace(item.Reason) != "" {
		lines = append(lines, "Reason: "+item.Reason)
	}
	if strings.TrimSpace(item.RecommendedCommand) != "" {
		lines = append(lines, "Next: "+item.RecommendedCommand)
	}
	return strings.Join(lines, "\n")
}

func resolveAttentionActiveMode(cwd string) (string, string) {
	refs, err := ListModeStateFilesWithScopePreference(cwd)
	if err != nil {
		return "", ""
	}
	type activeMode struct {
		ID    string
		Phase string
		Rank  int
	}
	best := activeMode{Rank: 1 << 30}
	for _, ref := range refs {
		content, err := os.ReadFile(ref.Path)
		if err != nil {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(content, &payload); err != nil {
			continue
		}
		active, _ := payload["active"].(bool)
		if !active {
			continue
		}
		rank := attentionModeRank(ref.Mode)
		if rank > best.Rank {
			continue
		}
		phase, _ := payload["current_phase"].(string)
		best = activeMode{
			ID:    ref.Mode,
			Phase: strings.TrimSpace(phase),
			Rank:  rank,
		}
	}
	if best.ID == "" {
		return "", ""
	}
	return best.ID, best.Phase
}

func attentionModeRank(mode string) int {
	switch strings.TrimSpace(mode) {
	case "deep-interview":
		return 0
	case "ralplan":
		return 1
	case "team":
		return 2
	case "autopilot":
		return 3
	case "ultrawork":
		return 4
	case "verify-loop":
		return 5
	case "autoresearch":
		return 6
	case "ultraqa":
		return 7
	default:
		return 8
	}
}

func listAttentionItems(cwd string) ([]attentionItem, error) {
	tasks, err := listStartUITasks(cwd)
	if err != nil {
		return nil, err
	}

	items := []attentionItem{}
	for _, task := range tasks {
		if task.Status == startUITaskStatusCompleted || task.Status == startUITaskStatusDismissed {
			continue
		}
		items = append(items, attentionItemFromTask(task))
	}

	sort.SliceStable(items, func(i, j int) bool {
		leftRank := startUIAttentionRank(items[i].AttentionState)
		rightRank := startUIAttentionRank(items[j].AttentionState)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftKindRank := attentionKindRank(items[i].Kind)
		rightKindRank := attentionKindRank(items[j].Kind)
		if leftKindRank != rightKindRank {
			return leftKindRank < rightKindRank
		}
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		if items[i].RepoSlug != items[j].RepoSlug {
			return items[i].RepoSlug < items[j].RepoSlug
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func attentionKindRank(kind string) int {
	switch strings.TrimSpace(kind) {
	case "service_task":
		return 0
	case "planned_item":
		return 1
	case "work_run":
		return 2
	case "work_item":
		return 3
	case "investigation":
		return 4
	case "scout_job":
		return 5
	case "issue":
		return 6
	default:
		return 7
	}
}

func attentionItemFromTask(item startUITaskSummary) attentionItem {
	return attentionItem{
		Kind:               item.Kind,
		ID:                 item.ID,
		RepoSlug:           item.RepoSlug,
		Summary:            defaultString(item.Title, item.ID),
		Reason:             defaultString(strings.TrimSpace(item.Summary), strings.TrimSpace(item.RawStatus)),
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), startUITaskAttentionStateForStatus(item.Status)),
		RecommendedCommand: recommendedCommandForTask(item),
		TargetURL:          strings.TrimSpace(item.ExternalURL),
		RunID:              strings.TrimSpace(item.RunID),
		ItemID:             strings.TrimSpace(strings.TrimPrefix(item.ID, "work-item:")),
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func recommendedCommandForTask(item startUITaskSummary) string {
	switch item.Kind {
	case "work_item":
		itemID := strings.TrimSpace(strings.TrimPrefix(item.ID, "work-item:"))
		if itemID == "" {
			return "nana work items show"
		}
		if item.Status == startUITaskStatusQueued {
			return "nana work items run " + itemID
		}
		return "nana work items show " + itemID
	case "work_run":
		if strings.TrimSpace(item.RunID) != "" {
			if item.AttentionState == "blocked" && strings.Contains(strings.TrimSpace(item.ExternalURL), "github.com/") {
				return "nana work sync --run-id " + item.RunID + " --reviewer @me"
			}
			return "nana work status --run-id " + item.RunID
		}
	case "investigation":
		if strings.TrimSpace(item.Description) != "" {
			return "nana investigate " + strconv.Quote(item.Description)
		}
	case "planned_item", "service_task", "issue", "scout_job":
		if strings.TrimSpace(item.RepoSlug) != "" {
			return "nana start --once --repo " + item.RepoSlug
		}
	}
	return "nana status"
}
