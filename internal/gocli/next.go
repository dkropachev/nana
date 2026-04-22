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
	Kind                  string   `json:"kind"`
	Subtype               string   `json:"subtype,omitempty"`
	ID                    string   `json:"id"`
	RepoSlug              string   `json:"repo_slug,omitempty"`
	IssueNumber           int      `json:"issue_number,omitempty"`
	Summary               string   `json:"summary"`
	Reason                string   `json:"reason,omitempty"`
	Detail                string   `json:"detail,omitempty"`
	Status                string   `json:"status,omitempty"`
	Severity              string   `json:"severity,omitempty"`
	Path                  string   `json:"path,omitempty"`
	Route                 string   `json:"route,omitempty"`
	AttentionState        string   `json:"attention_state,omitempty"`
	ActionKind            string   `json:"action_kind,omitempty"`
	AvailableActions      []string `json:"available_actions,omitempty"`
	RecommendedCommand    string   `json:"recommended_command,omitempty"`
	TargetURL             string   `json:"target_url,omitempty"`
	RunID                 string   `json:"run_id,omitempty"`
	ItemID                string   `json:"item_id,omitempty"`
	PlannedItemID         string   `json:"planned_item_id,omitempty"`
	ScoutJobID            string   `json:"scout_job_id,omitempty"`
	FindingID             string   `json:"finding_id,omitempty"`
	ImportSessionID       string   `json:"import_session_id,omitempty"`
	ImportCandidateID     string   `json:"import_candidate_id,omitempty"`
	ImportCandidateStatus string   `json:"import_candidate_status,omitempty"`
	UpdatedAt             string   `json:"updated_at,omitempty"`
}

type attentionCounts struct {
	ByAttentionState map[string]int `json:"by_attention_state,omitempty"`
	ByKind           map[string]int `json:"by_kind,omitempty"`
	ByRepoSlug       map[string]int `json:"by_repo_slug,omitempty"`
}

type attentionReport struct {
	GeneratedAt  string          `json:"generated_at"`
	ActiveModeID string          `json:"active_mode_id,omitempty"`
	ActiveMode   string          `json:"active_mode,omitempty"`
	ActivePhase  string          `json:"active_phase,omitempty"`
	Counts       attentionCounts `json:"counts,omitempty"`
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
		Counts:       buildAttentionCounts(items),
		Items:        items,
	}
	if len(items) > 0 {
		next := items[0]
		report.Next = &next
	}
	return report, nil
}

func buildAttentionCounts(items []attentionItem) attentionCounts {
	counts := attentionCounts{
		ByAttentionState: map[string]int{},
		ByKind:           map[string]int{},
		ByRepoSlug:       map[string]int{},
	}
	for _, item := range items {
		state := defaultString(strings.TrimSpace(item.AttentionState), "queued")
		counts.ByAttentionState[state]++
		if kind := strings.TrimSpace(item.Kind); kind != "" {
			counts.ByKind[kind]++
		}
		counts.ByRepoSlug[strings.TrimSpace(item.RepoSlug)]++
	}
	return counts
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
	repos, err := listStartUIRepoSummaries(true)
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
	for _, repo := range repos {
		repoSlug := strings.TrimSpace(repo.RepoSlug)
		if repoSlug == "" {
			continue
		}
		findings, err := listStartUIFindings(repoSlug)
		if err != nil {
			return nil, err
		}
		for _, finding := range findings {
			if !attentionFindingNeedsOperatorAction(finding) {
				continue
			}
			items = append(items, attentionItemFromFinding(finding))
		}
		sessions, err := listStartUIFindingImportSessions(repoSlug)
		if err != nil {
			return nil, err
		}
		for _, session := range sessions {
			for _, candidate := range session.Candidates {
				if !attentionImportCandidateNeedsOperatorAction(candidate) {
					continue
				}
				items = append(items, attentionItemFromImportCandidate(repoSlug, session, candidate))
			}
		}
	}
	for _, run := range workRuns {
		if seenRunIDs[run.RunID] || !attentionWorkRunNeedsOperatorAction(run) {
			continue
		}
		items = append(items, attentionItemFromWorkRun(run))
	}
	for _, item := range workItems {
		if seenItemIDs[item.ID] || !attentionWorkItemNeedsOperatorAction(item) {
			continue
		}
		items = append(items, attentionItemFromWorkItem(item))
	}
	for _, investigation := range investigations {
		if !attentionInvestigationNeedsOperatorAction(investigation) {
			continue
		}
		items = append(items, attentionItemFromInvestigation(investigation))
	}
	for _, issue := range issues {
		if !attentionIssueNeedsOperatorAction(issue) {
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
	case "finding":
		return 1
	case "import_candidate":
		return 2
	case "work_run":
		return 3
	case "work_item":
		return 4
	case "investigation":
		return 5
	case "issue":
		return 6
	default:
		return 7
	}
}

func attentionItemFromApproval(item startUIApprovalQueueItem) attentionItem {
	summary := strings.TrimSpace(item.Subject)
	if strings.TrimSpace(item.RepoSlug) != "" {
		summary = item.RepoSlug + ": " + summary
	}
	return attentionItem{
		Kind:               "approval",
		Subtype:            strings.TrimSpace(item.Kind),
		ID:                 item.ID,
		RepoSlug:           item.RepoSlug,
		Summary:            defaultString(summary, item.ID),
		Reason:             defaultString(strings.TrimSpace(item.Reason), strings.TrimSpace(item.NextAction)),
		Detail:             strings.TrimSpace(item.NextAction),
		Status:             strings.TrimSpace(item.Status),
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), "queued"),
		ActionKind:         strings.TrimSpace(item.ActionKind),
		AvailableActions:   attentionApprovalAvailableActions(item),
		RecommendedCommand: recommendedCommandForApproval(item),
		TargetURL:          defaultString(strings.TrimSpace(item.ExternalURL), strings.TrimSpace(item.TargetURL)),
		RunID:              strings.TrimSpace(item.RunID),
		ItemID:             strings.TrimSpace(item.ItemID),
		PlannedItemID:      strings.TrimSpace(item.PlannedItemID),
		ScoutJobID:         strings.TrimSpace(item.ScoutJobID),
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func attentionApprovalAvailableActions(item startUIApprovalQueueItem) []string {
	actions := []string{}
	if action := strings.TrimSpace(item.ActionKind); action != "" {
		actions = append(actions, action)
	}
	switch strings.TrimSpace(item.Kind) {
	case "work_run":
		if strings.TrimSpace(item.RunID) != "" {
			actions = append(actions, "open_run", "drop_approval")
		}
	case "work_item":
		if strings.TrimSpace(item.ItemID) != "" {
			actions = append(actions, "open_work_item", "drop_approval")
		}
	case "planned_item":
		if strings.TrimSpace(item.PlannedItemID) != "" {
			actions = append(actions, "drop_approval")
		}
	case "scout_job":
		if strings.TrimSpace(item.ScoutJobID) != "" {
			actions = append(actions, "drop_approval")
		}
	}
	return uniqueStrings(actions)
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
	actionKind := "open_run"
	availableActions := []string{"open_run"}
	if run.ResolveAllowed {
		actionKind = "resolve_run"
		availableActions = append([]string{"resolve_run"}, availableActions...)
	} else if strings.TrimSpace(run.Backend) == "github" {
		actionKind = "sync_run"
		availableActions = append([]string{"sync_run"}, availableActions...)
	}
	return attentionItem{
		Kind:               "work_run",
		Subtype:            strings.TrimSpace(run.Backend),
		ID:                 run.RunID,
		RepoSlug:           run.RepoSlug,
		Summary:            defaultString(summary, run.RunID),
		Reason:             reason,
		Detail:             defaultString(strings.TrimSpace(run.PublicationState), strings.TrimSpace(run.CurrentPhase)),
		Status:             strings.TrimSpace(run.Status),
		AttentionState:     defaultString(strings.TrimSpace(run.AttentionState), "active"),
		ActionKind:         actionKind,
		AvailableActions:   uniqueStrings(availableActions),
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
		Subtype:            strings.TrimSpace(item.SourceKind),
		ID:                 item.ID,
		RepoSlug:           item.RepoSlug,
		Summary:            defaultString(item.Subject, item.ID),
		Reason:             reason,
		Detail:             defaultString(strings.TrimSpace(item.DraftSummary), strings.TrimSpace(item.TargetURL)),
		Status:             strings.TrimSpace(item.Status),
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), "queued"),
		ActionKind:         attentionWorkItemPrimaryAction(item),
		AvailableActions:   attentionWorkItemAvailableActions(item),
		RecommendedCommand: recommendedCommandForWorkItem(item),
		TargetURL:          strings.TrimSpace(item.TargetURL),
		ItemID:             item.ID,
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func attentionWorkItemPrimaryAction(item startUIWorkItem) string {
	if strings.TrimSpace(item.Status) == workItemStatusPaused {
		return "requeue_work_item"
	}
	return "open_work_item"
}

func attentionWorkItemAvailableActions(item startUIWorkItem) []string {
	actions := []string{"open_work_item"}
	if strings.TrimSpace(item.Status) == workItemStatusPaused {
		actions = append([]string{"requeue_work_item"}, actions...)
	}
	return uniqueStrings(actions)
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
		Detail:             defaultString(strings.TrimSpace(item.OverallShortExplanation), strings.TrimSpace(item.PauseReason)),
		Status:             strings.TrimSpace(item.Status),
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), "queued"),
		ActionKind:         "open_investigation",
		AvailableActions:   []string{"open_investigation"},
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
		IssueNumber:        item.SourceNumber,
		Summary:            summary,
		Reason:             reason,
		Detail:             defaultString(strings.TrimSpace(item.TriageRationale), strings.TrimSpace(item.DeferredReason)),
		Status:             strings.TrimSpace(item.Status),
		AttentionState:     defaultString(strings.TrimSpace(item.AttentionState), "queued"),
		ActionKind:         "investigate_issue",
		AvailableActions:   []string{"investigate_issue", "launch_issue_work", "open_issue"},
		RecommendedCommand: recommendedCommandForIssue(item),
		TargetURL:          strings.TrimSpace(item.SourceURL),
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func attentionItemFromFinding(item startWorkFinding) attentionItem {
	summary := strings.TrimSpace(item.Title)
	if strings.TrimSpace(item.RepoSlug) != "" {
		summary = item.RepoSlug + ": " + summary
	}
	reason := defaultString(strings.TrimSpace(item.Summary), "finding requires disposition")
	detail := strings.TrimSpace(item.Detail)
	if detail == "" {
		detail = "Promote this finding into planned work or dismiss it."
	}
	return attentionItem{
		Kind:               "finding",
		Subtype:            strings.TrimSpace(item.SourceKind),
		ID:                 item.ID,
		RepoSlug:           item.RepoSlug,
		Summary:            defaultString(summary, item.ID),
		Reason:             reason,
		Detail:             detail,
		Status:             normalizeStartWorkFindingStatus(item.Status),
		Severity:           normalizeGithubSeverity(item.Severity),
		Path:               strings.TrimSpace(item.Path),
		Route:              strings.TrimSpace(item.Route),
		AttentionState:     "blocked",
		ActionKind:         "promote_finding",
		AvailableActions:   []string{"promote_finding", "dismiss_finding", "open_finding"},
		RecommendedCommand: "nana findings promote --repo " + item.RepoSlug + " --finding " + item.ID,
		FindingID:          item.ID,
		UpdatedAt:          strings.TrimSpace(item.UpdatedAt),
	}
}

func attentionItemFromImportCandidate(repoSlug string, session startWorkFindingImportSession, candidate startWorkFindingImportCandidate) attentionItem {
	summary := strings.TrimSpace(candidate.Title)
	if strings.TrimSpace(repoSlug) != "" {
		summary = repoSlug + ": " + summary
	}
	reason := defaultString(strings.TrimSpace(candidate.Summary), "imported finding candidate awaiting review")
	detail := strings.TrimSpace(candidate.Detail)
	if detail == "" {
		detail = "Promote this candidate into the findings inbox or drop it."
	}
	return attentionItem{
		Kind:                  "import_candidate",
		Subtype:               startWorkFindingSourceKindManualImport,
		ID:                    session.ID + ":" + candidate.CandidateID,
		RepoSlug:              repoSlug,
		Summary:               defaultString(summary, candidate.CandidateID),
		Reason:                reason,
		Detail:                detail,
		Status:                normalizeStartWorkFindingImportParseStatus(session.ParseStatus),
		Severity:              normalizeGithubSeverity(candidate.Severity),
		Path:                  strings.TrimSpace(candidate.Path),
		Route:                 strings.TrimSpace(candidate.Route),
		AttentionState:        "queued",
		ActionKind:            "promote_import_candidate",
		AvailableActions:      []string{"promote_import_candidate", "drop_import_candidate", "open_import_candidate"},
		RecommendedCommand:    "nana findings import review --repo " + repoSlug + " --session " + session.ID + " --promote " + candidate.CandidateID,
		ImportSessionID:       session.ID,
		ImportCandidateID:     candidate.CandidateID,
		ImportCandidateStatus: normalizeStartWorkFindingCandidateStatus(candidate.Status),
		UpdatedAt:             strings.TrimSpace(session.UpdatedAt),
	}
}

func attentionFindingNeedsOperatorAction(item startWorkFinding) bool {
	return normalizeStartWorkFindingStatus(item.Status) == startWorkFindingStatusOpen
}

func attentionImportCandidateNeedsOperatorAction(candidate startWorkFindingImportCandidate) bool {
	return normalizeStartWorkFindingCandidateStatus(candidate.Status) == startWorkFindingCandidateStatusCandidate
}

func attentionWorkRunNeedsOperatorAction(item startUIWorkRun) bool {
	if !item.Pending {
		return false
	}
	switch strings.TrimSpace(item.AttentionState) {
	case "failed", "blocked":
		return true
	default:
		return false
	}
}

func attentionWorkItemNeedsOperatorAction(item startUIWorkItem) bool {
	if !item.Pending {
		return false
	}
	switch strings.TrimSpace(item.Status) {
	case workItemStatusPaused, workItemStatusNeedsRouting, workItemStatusFailed:
		return true
	default:
		return false
	}
}

func attentionInvestigationNeedsOperatorAction(item startUIInvestigationSummary) bool {
	return strings.TrimSpace(item.RunID) != ""
}

func attentionIssueNeedsOperatorAction(item startUIIssueQueueItem) bool {
	switch strings.TrimSpace(item.AttentionState) {
	case "failed", "blocked":
		return true
	default:
		return false
	}
}

func recommendedCommandForIssue(item startUIIssueQueueItem) string {
	if strings.TrimSpace(item.RepoSlug) != "" {
		return "nana start --once --repo " + item.RepoSlug
	}
	return "nana start --once"
}
