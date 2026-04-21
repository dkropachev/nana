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
	approvals, err := loadStartUIApprovals()
	if err != nil {
		return nil, err
	}
	workRuns, err := loadStartUIWorkRuns(50)
	if err != nil {
		return nil, err
	}
	workItems, _, _, err := loadStartUIWorkItemsWithHiddenCount(50)
	if err != nil {
		return nil, err
	}
	investigations, err := listStartUIInvestigations(cwd)
	if err != nil {
		return nil, err
	}
	issues, err := listStartUIIssueQueue()
	if err != nil {
		return nil, err
	}

	items := []attentionItem{}
	seenRunIDs := map[string]bool{}
	seenItemIDs := map[string]bool{}

	for _, approval := range approvals {
		items = append(items, attentionItemFromApproval(approval))
		if strings.TrimSpace(approval.RunID) != "" {
			seenRunIDs[approval.RunID] = true
		}
		if strings.TrimSpace(approval.ItemID) != "" {
			seenItemIDs[approval.ItemID] = true
		}
	}
	for _, run := range workRuns {
		if !run.Pending || seenRunIDs[run.RunID] {
			continue
		}
		items = append(items, attentionItemFromWorkRun(run))
	}
	for _, item := range workItems {
		if !item.Pending || seenItemIDs[item.ID] {
			continue
		}
		items = append(items, attentionItemFromWorkItem(item))
	}
	for _, investigation := range investigations {
		if investigation.AttentionState == "completed" {
			continue
		}
		items = append(items, attentionItemFromInvestigation(investigation))
	}
	for _, issue := range issues {
		if issue.AttentionState == "completed" {
			continue
		}
		items = append(items, attentionItemFromIssue(issue))
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
	case "approval":
		return 0
	case "work_run":
		return 1
	case "work_item":
		return 2
	case "investigation":
		return 3
	case "issue":
		return 4
	default:
		return 5
	}
}

func attentionItemFromApproval(item startUIApprovalQueueItem) attentionItem {
	summary := strings.TrimSpace(item.Subject)
	if strings.TrimSpace(item.RepoSlug) != "" {
		summary = item.RepoSlug + ": " + summary
	}
	return attentionItem{
		Kind:               "approval",
		ID:                 item.ID,
		RepoSlug:           item.RepoSlug,
		Summary:            defaultString(summary, item.ID),
		Reason:             defaultString(strings.TrimSpace(item.Reason), strings.TrimSpace(item.NextAction)),
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), "queued"),
		RecommendedCommand: recommendedCommandForApproval(item),
		TargetURL:          defaultString(strings.TrimSpace(item.ExternalURL), strings.TrimSpace(item.TargetURL)),
		RunID:              strings.TrimSpace(item.RunID),
		ItemID:             strings.TrimSpace(item.ItemID),
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func recommendedCommandForApproval(item startUIApprovalQueueItem) string {
	switch {
	case strings.TrimSpace(item.RunID) != "" && item.ActionKind == "sync_run":
		return "nana work sync --run-id " + item.RunID + " --reviewer @me"
	case strings.TrimSpace(item.RunID) != "" && item.ActionKind == "review_on_github":
		return "nana work sync --run-id " + item.RunID + " --reviewer @me"
	case strings.TrimSpace(item.RunID) != "":
		return "nana work status --run-id " + item.RunID
	case strings.TrimSpace(item.ItemID) != "":
		return "nana work items show " + item.ItemID
	case strings.TrimSpace(item.PlannedItemID) != "" && strings.TrimSpace(item.RepoSlug) != "":
		return "nana start --once --repo " + item.RepoSlug
	default:
		return "nana status"
	}
}

func attentionItemFromWorkRun(run startUIWorkRun) attentionItem {
	summary := strings.TrimSpace(run.RepoLabel)
	if strings.TrimSpace(run.TargetURL) != "" {
		summary = defaultString(run.RepoSlug, run.RepoLabel) + ": " + run.TargetURL
	}
	reason := defaultString(strings.TrimSpace(run.CurrentPhase), strings.TrimSpace(run.PublicationState))
	if reason == "" {
		reason = strings.TrimSpace(run.Status)
	}
	return attentionItem{
		Kind:               "work_run",
		ID:                 run.RunID,
		RepoSlug:           run.RepoSlug,
		Summary:            defaultString(summary, run.RunID),
		Reason:             reason,
		AttentionState:     defaultString(strings.TrimSpace(run.AttentionState), "active"),
		RecommendedCommand: recommendedCommandForWorkRun(run),
		TargetURL:          strings.TrimSpace(run.TargetURL),
		RunID:              run.RunID,
		UpdatedAt:          strings.TrimSpace(run.UpdatedAt),
	}
}

func recommendedCommandForWorkRun(run startUIWorkRun) string {
	switch run.Backend {
	case "github":
		if run.AttentionState == "blocked" {
			return "nana work sync --run-id " + run.RunID + " --reviewer @me"
		}
		if run.AttentionState == "failed" {
			return "nana work logs --run-id " + run.RunID + " --tail 200"
		}
		return "nana work status --run-id " + run.RunID
	default:
		if run.AttentionState == "failed" {
			return "nana work logs --run-id " + run.RunID + " --tail 200"
		}
		return "nana work status --run-id " + run.RunID
	}
}

func attentionItemFromWorkItem(item startUIWorkItem) attentionItem {
	reason := strings.TrimSpace(item.Status)
	switch strings.TrimSpace(item.Status) {
	case workItemStatusDraftReady:
		reason = "draft ready for review and submission"
	case workItemStatusNeedsRouting:
		reason = "needs routing"
	case workItemStatusFailed:
		reason = "work item execution failed"
	}
	return attentionItem{
		Kind:               "work_item",
		ID:                 item.ID,
		RepoSlug:           item.RepoSlug,
		Summary:            defaultString(item.Subject, item.ID),
		Reason:             reason,
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), "queued"),
		RecommendedCommand: recommendedCommandForWorkItem(item),
		TargetURL:          strings.TrimSpace(item.TargetURL),
		ItemID:             item.ID,
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func recommendedCommandForWorkItem(item startUIWorkItem) string {
	switch strings.TrimSpace(item.Status) {
	case workItemStatusQueued:
		return "nana work items run " + item.ID
	default:
		return "nana work items show " + item.ID
	}
}

func attentionItemFromInvestigation(item startUIInvestigationSummary) attentionItem {
	reason := defaultString(strings.TrimSpace(item.LastError), strings.TrimSpace(item.OverallShortExplanation))
	if reason == "" {
		reason = strings.TrimSpace(item.Status)
	}
	return attentionItem{
		Kind:               "investigation",
		ID:                 item.RunID,
		RepoSlug:           item.RepoSlug,
		Summary:            defaultString(item.Query, item.RunID),
		Reason:             reason,
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), "queued"),
		RecommendedCommand: "nana investigate " + strconv.Quote(item.Query),
		RunID:              item.RunID,
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func attentionItemFromIssue(item startUIIssueQueueItem) attentionItem {
	reason := defaultString(strings.TrimSpace(item.BlockedReason), strings.TrimSpace(item.TriageError))
	if reason == "" {
		reason = defaultString(strings.TrimSpace(item.TriageRationale), strings.TrimSpace(item.Status))
	}
	summary := fmt.Sprintf("%s#%d: %s", defaultString(item.RepoSlug, "repo"), item.SourceNumber, item.Title)
	return attentionItem{
		Kind:               "issue",
		ID:                 item.ID,
		RepoSlug:           item.RepoSlug,
		Summary:            summary,
		Reason:             reason,
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), "queued"),
		RecommendedCommand: recommendedCommandForIssue(item),
		TargetURL:          strings.TrimSpace(item.SourceURL),
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func recommendedCommandForIssue(item startUIIssueQueueItem) string {
	if strings.TrimSpace(item.RepoSlug) != "" {
		return "nana start --once --repo " + item.RepoSlug
	}
	return "nana start --once"
}
