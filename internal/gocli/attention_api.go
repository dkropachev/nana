package gocli

import (
	"encoding/json"
	"fmt"
	"strings"
)

type startUIAttentionDetailResponse struct {
	Item    attentionItem `json:"item"`
	Detail  any           `json:"detail,omitempty"`
	Actions []string      `json:"actions,omitempty"`
}

type startUIAttentionApprovalDetail struct {
	Subtype     string                    `json:"subtype,omitempty"`
	Reason      string                    `json:"reason,omitempty"`
	NextAction  string                    `json:"next_action,omitempty"`
	ExternalURL string                    `json:"external_url,omitempty"`
	TargetURL   string                    `json:"target_url,omitempty"`
	RunID       string                    `json:"run_id,omitempty"`
	ItemID      string                    `json:"item_id,omitempty"`
	PlannedItem *startWorkPlannedItem     `json:"planned_item,omitempty"`
	ScoutJob    *startWorkScoutJob        `json:"scout_job,omitempty"`
	WorkRun     *startUIAttentionWorkRun  `json:"work_run,omitempty"`
	WorkItem    *startUIAttentionWorkItem `json:"work_item,omitempty"`
}

type startUIAttentionIssueDetail struct {
	Issue          startUIIssueQueueItem `json:"issue"`
	CanInvestigate bool                  `json:"can_investigate"`
	CanLaunchWork  bool                  `json:"can_launch_work"`
}

type startUIAttentionFindingDetail struct {
	Finding      startWorkFinding      `json:"finding"`
	PromotedTask *startWorkPlannedItem `json:"promoted_task,omitempty"`
}

type startUIAttentionImportSessionSummary struct {
	ID            string `json:"id"`
	RepoSlug      string `json:"repo_slug,omitempty"`
	InputFilePath string `json:"input_file_path,omitempty"`
	ParseStatus   string `json:"parse_status,omitempty"`
	ParseError    string `json:"parse_error,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	PreviewPath   string `json:"preview_path,omitempty"`
}

type startUIAttentionImportCandidateDetail struct {
	Session   startUIAttentionImportSessionSummary `json:"session"`
	Candidate startWorkFindingImportCandidate      `json:"candidate"`
}

type startUIAttentionWorkRun struct {
	Run  startUIWorkRunDetail             `json:"run"`
	Logs *startUIAttentionWorkRunLogState `json:"logs,omitempty"`
}

type startUIAttentionWorkRunLogState struct {
	ArtifactRoot string                  `json:"artifact_root,omitempty"`
	DefaultPath  string                  `json:"default_path,omitempty"`
	Files        []startUIWorkRunLogFile `json:"files,omitempty"`
	TailPath     string                  `json:"tail_path,omitempty"`
	TailLines    int                     `json:"tail_lines,omitempty"`
	TailContent  string                  `json:"tail_content,omitempty"`
}

type startUIAttentionWorkItem struct {
	WorkItem workItemDetail `json:"work_item"`
}

type startUIAttentionBatchRequest struct {
	Action  string   `json:"action,omitempty"`
	ItemIDs []string `json:"item_ids,omitempty"`
}

type startUIAttentionBatchResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type startUIAttentionBatchResponse struct {
	Action       string                        `json:"action"`
	Results      []startUIAttentionBatchResult `json:"results"`
	SuccessCount int                           `json:"success_count"`
	FailureCount int                           `json:"failure_count"`
}

func attentionActionSupported(action string) bool {
	switch strings.TrimSpace(action) {
	case "approve_work_item",
		"submit_work_item",
		"approve_planned_item",
		"retry_scout_job",
		"resolve_run",
		"sync_run",
		"promote_finding",
		"dismiss_finding",
		"save_finding",
		"promote_import_candidate",
		"drop_import_candidate",
		"save_import_candidate",
		"drop_approval",
		"requeue_work_item",
		"run_work_item",
		"fix_work_item",
		"drop_work_item",
		"restore_work_item",
		"investigate_issue",
		"launch_issue_work",
		"save_issue",
		"clear_issue_schedule",
		"drop_run":
		return true
	default:
		return false
	}
}

func attentionBatchActionSupported(action string) bool {
	switch strings.TrimSpace(action) {
	case "approve_work_item",
		"approve_planned_item",
		"retry_scout_job",
		"resolve_run",
		"sync_run",
		"promote_finding",
		"dismiss_finding",
		"promote_import_candidate",
		"drop_import_candidate",
		"drop_approval",
		"requeue_work_item":
		return true
	default:
		return false
	}
}

func loadAttentionItemIndex(cwd string) (map[string]attentionItem, error) {
	items, err := listAttentionItems(cwd)
	if err != nil {
		return nil, err
	}
	index := make(map[string]attentionItem, len(items))
	for _, item := range items {
		index[item.ID] = item
	}
	return index, nil
}

func resolveAttentionItemByID(cwd string, itemID string) (attentionItem, error) {
	index, err := loadAttentionItemIndex(cwd)
	if err != nil {
		return attentionItem{}, err
	}
	item, ok := index[strings.TrimSpace(itemID)]
	if !ok {
		return attentionItem{}, fmt.Errorf("attention item %s was not found", itemID)
	}
	return item, nil
}

func loadAttentionDetailResponse(cwd string, item attentionItem) (startUIAttentionDetailResponse, error) {
	detail, err := loadAttentionItemDetail(cwd, item)
	if err != nil {
		return startUIAttentionDetailResponse{}, err
	}
	return startUIAttentionDetailResponse{
		Item:    item,
		Detail:  detail,
		Actions: attentionDetailActions(item, detail),
	}, nil
}

func loadAttentionItemDetail(cwd string, item attentionItem) (any, error) {
	switch strings.TrimSpace(item.Kind) {
	case "approval":
		return loadAttentionApprovalDetail(item)
	case "finding":
		return loadAttentionFindingDetail(item)
	case "import_candidate":
		return loadAttentionImportCandidateDetail(item)
	case "issue":
		return loadAttentionIssueDetail(item)
	case "work_run":
		return loadAttentionWorkRunDetail(item.RunID)
	case "work_item":
		return loadAttentionWorkItemDetail(attentionWorkItemID(item))
	case "investigation":
		return loadStartUIInvestigationDetail(cwd, defaultString(strings.TrimSpace(item.RunID), strings.TrimSpace(item.ID)))
	default:
		return nil, fmt.Errorf("unsupported attention kind %q", item.Kind)
	}
}

func attentionDetailActions(item attentionItem, detail any) []string {
	switch strings.TrimSpace(item.Kind) {
	case "approval":
		return attentionApprovalDetailActions(item)
	case "finding":
		findingDetail, _ := detail.(startUIAttentionFindingDetail)
		actions := []string{"save_finding"}
		if normalizeStartWorkFindingStatus(findingDetail.Finding.Status) == startWorkFindingStatusOpen {
			actions = append(actions, "promote_finding", "dismiss_finding")
		}
		return actions
	case "import_candidate":
		candidateDetail, _ := detail.(startUIAttentionImportCandidateDetail)
		actions := []string{"save_import_candidate"}
		if normalizeStartWorkFindingCandidateStatus(candidateDetail.Candidate.Status) == startWorkFindingCandidateStatusCandidate {
			actions = append(actions, "promote_import_candidate", "drop_import_candidate")
		}
		return actions
	case "issue":
		return []string{"investigate_issue", "launch_issue_work", "save_issue", "clear_issue_schedule"}
	case "work_run":
		runDetail, _ := detail.(startUIAttentionWorkRun)
		actions := []string{}
		if runDetail.Run.ResolveAllowed {
			actions = append(actions, "resolve_run")
		}
		if runDetail.Run.SyncAllowed {
			actions = append(actions, "sync_run")
		}
		return append(actions, "drop_run")
	case "work_item":
		workItemDetail, _ := detail.(startUIAttentionWorkItem)
		actions := []string{}
		status := strings.TrimSpace(workItemDetail.WorkItem.Item.Status)
		if status == workItemStatusDraftReady {
			actions = append(actions, "submit_work_item")
		}
		if status == workItemStatusPaused {
			actions = append(actions, "requeue_work_item")
		}
		if status == workItemStatusQueued || status == workItemStatusNeedsRouting || status == workItemStatusFailed {
			actions = append(actions, "run_work_item")
		}
		actions = append(actions, "fix_work_item", "drop_work_item")
		if workItemDetail.WorkItem.Item.Hidden || status == workItemStatusDropped {
			actions = append(actions, "restore_work_item")
		}
		return uniqueStrings(actions)
	case "investigation":
		return []string{}
	default:
		return []string{}
	}
}

func attentionApprovalDetailActions(item attentionItem) []string {
	switch strings.TrimSpace(item.Subtype) {
	case "work_run":
		actions := []string{}
		if strings.TrimSpace(item.ActionKind) == "review_on_github" && strings.TrimSpace(item.TargetURL) != "" {
			actions = append(actions, "review_on_github")
		}
		if strings.TrimSpace(item.ActionKind) == "resolve_run" || strings.TrimSpace(item.ActionKind) == "sync_run" {
			actions = append(actions, item.ActionKind)
		}
		if (strings.TrimSpace(item.ActionKind) == "review_on_github" || strings.TrimSpace(item.ActionKind) == "sync_run") &&
			strings.TrimSpace(item.RunID) != "" &&
			!slicesContainsString(actions, "sync_run") {
			actions = append(actions, "sync_run")
		}
		return append(uniqueStrings(actions), "drop_approval")
	case "work_item":
		return []string{"approve_work_item", "fix_work_item", "drop_approval"}
	case "planned_item":
		return []string{"approve_planned_item", "drop_approval"}
	case "scout_job":
		return []string{"retry_scout_job", "drop_approval"}
	default:
		return []string{}
	}
}

func loadAttentionApprovalDetail(item attentionItem) (startUIAttentionApprovalDetail, error) {
	detail := startUIAttentionApprovalDetail{
		Subtype:     item.Subtype,
		Reason:      strings.TrimSpace(item.Reason),
		NextAction:  strings.TrimSpace(item.Detail),
		ExternalURL: strings.TrimSpace(item.TargetURL),
		TargetURL:   strings.TrimSpace(item.TargetURL),
		RunID:       strings.TrimSpace(item.RunID),
		ItemID:      strings.TrimSpace(item.ItemID),
	}
	switch strings.TrimSpace(item.Subtype) {
	case "work_run":
		workRun, err := loadAttentionWorkRunDetail(item.RunID)
		if err != nil {
			return startUIAttentionApprovalDetail{}, err
		}
		detail.WorkRun = &workRun
	case "work_item":
		workItem, err := loadAttentionWorkItemDetail(item.ItemID)
		if err != nil {
			return startUIAttentionApprovalDetail{}, err
		}
		detail.WorkItem = &workItem
	case "planned_item":
		_, _, plannedItem, err := findStartUIPlannedItem(item.PlannedItemID)
		if err != nil {
			return startUIAttentionApprovalDetail{}, err
		}
		detail.PlannedItem = &plannedItem
	case "scout_job":
		state, err := readStartWorkState(item.RepoSlug)
		if err != nil {
			return startUIAttentionApprovalDetail{}, err
		}
		scoutJob, ok := state.ScoutJobs[strings.TrimSpace(item.ScoutJobID)]
		if !ok {
			return startUIAttentionApprovalDetail{}, fmt.Errorf("scout job %s was not found", item.ScoutJobID)
		}
		detail.ScoutJob = &scoutJob
	}
	return detail, nil
}

func loadAttentionFindingDetail(item attentionItem) (startUIAttentionFindingDetail, error) {
	finding, err := loadStartUIFinding(item.RepoSlug, attentionFindingID(item))
	if err != nil {
		return startUIAttentionFindingDetail{}, err
	}
	detail := startUIAttentionFindingDetail{Finding: finding}
	if plannedID := strings.TrimSpace(finding.PromotedTaskID); plannedID != "" {
		if _, _, plannedItem, plannedErr := findStartUIPlannedItem(plannedID); plannedErr == nil {
			detail.PromotedTask = &plannedItem
		}
	}
	return detail, nil
}

func loadAttentionImportCandidateDetail(item attentionItem) (startUIAttentionImportCandidateDetail, error) {
	session, err := loadStartUIFindingImportSession(item.RepoSlug, strings.TrimSpace(item.ImportSessionID))
	if err != nil {
		return startUIAttentionImportCandidateDetail{}, err
	}
	candidateID := strings.TrimSpace(item.ImportCandidateID)
	for _, candidate := range session.Candidates {
		if candidate.CandidateID != candidateID {
			continue
		}
		return startUIAttentionImportCandidateDetail{
			Session: startUIAttentionImportSessionSummary{
				ID:            session.ID,
				RepoSlug:      session.RepoSlug,
				InputFilePath: session.InputFilePath,
				ParseStatus:   session.ParseStatus,
				ParseError:    session.ParseError,
				UpdatedAt:     session.UpdatedAt,
				PreviewPath:   session.PreviewPath,
			},
			Candidate: candidate,
		}, nil
	}
	return startUIAttentionImportCandidateDetail{}, fmt.Errorf("candidate %s was not found", candidateID)
}

func loadAttentionIssueDetail(item attentionItem) (startUIAttentionIssueDetail, error) {
	items, err := listStartUIIssueQueue()
	if err != nil {
		return startUIAttentionIssueDetail{}, err
	}
	for _, issue := range items {
		if issue.ID != strings.TrimSpace(item.ID) {
			continue
		}
		return startUIAttentionIssueDetail{
			Issue:          issue,
			CanInvestigate: strings.TrimSpace(issue.RepoSlug) != "" && issue.SourceNumber > 0,
			CanLaunchWork:  strings.TrimSpace(issue.RepoSlug) != "" && issue.SourceNumber > 0,
		}, nil
	}
	return startUIAttentionIssueDetail{}, fmt.Errorf("issue %s was not found", item.ID)
}

func loadAttentionWorkRunDetail(runID string) (startUIAttentionWorkRun, error) {
	detail, err := loadStartUIWorkRunDetail(runID)
	if err != nil {
		return startUIAttentionWorkRun{}, err
	}
	logs, err := loadStartUIWorkRunLogs(runID)
	if err != nil {
		return startUIAttentionWorkRun{Run: detail}, nil
	}
	state := &startUIAttentionWorkRunLogState{
		ArtifactRoot: logs.ArtifactRoot,
		DefaultPath:  logs.DefaultPath,
		Files:        logs.Files,
	}
	tailPath := strings.TrimSpace(logs.DefaultPath)
	if tailPath == "" && len(logs.Files) > 0 {
		tailPath = strings.TrimSpace(logs.Files[0].Path)
	}
	if tailPath != "" {
		content, err := loadStartUIWorkRunLogContent(runID, tailPath, 200)
		if err == nil {
			state.TailPath = tailPath
			state.TailLines = 200
			state.TailContent, _ = content["content"].(string)
		}
	}
	return startUIAttentionWorkRun{Run: detail, Logs: state}, nil
}

func loadAttentionWorkItemDetail(itemID string) (startUIAttentionWorkItem, error) {
	detail, err := readWorkItemDetail(itemID)
	if err != nil {
		return startUIAttentionWorkItem{}, err
	}
	return startUIAttentionWorkItem{WorkItem: detail}, nil
}

func executeAttentionItemAction(cwd string, item attentionItem, action string, body []byte) (any, error) {
	switch strings.TrimSpace(action) {
	case "approve_work_item", "submit_work_item":
		return submitWorkItemByID(attentionWorkItemID(item), "ui")
	case "approve_planned_item":
		repoSlug, state, plannedItem, err := findStartUIPlannedItem(strings.TrimSpace(item.PlannedItemID))
		if err != nil {
			return nil, err
		}
		updatedState, updatedItem, launch, err := launchStartUIPlannedItemNow(repoSlug, state, plannedItem)
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": updatedState, "planned_item": updatedItem, "launch": launch}, nil
	case "retry_scout_job":
		return mutateStartUIScoutItem(item.RepoSlug, strings.TrimSpace(item.ScoutJobID), "retry")
	case "resolve_run":
		if err := startUIResolveAttentionRun(item); err != nil {
			return nil, err
		}
		return loadAttentionWorkRunDetail(item.RunID)
	case "sync_run":
		if err := startUISyncGithubRun(githubWorkSyncOptions{RunID: strings.TrimSpace(item.RunID)}); err != nil {
			return nil, err
		}
		return loadAttentionWorkRunDetail(item.RunID)
	case "promote_finding":
		state, finding, plannedItem, err := promoteStartUIFinding(item.RepoSlug, attentionFindingID(item))
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "finding": finding, "planned_item": plannedItem}, nil
	case "dismiss_finding":
		state, finding, err := dismissStartUIFinding(item.RepoSlug, attentionFindingID(item), "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "finding": finding}, nil
	case "save_finding":
		var payload startUIFindingPatchRequest
		if err := decodeAttentionActionPayload(body, &payload); err != nil {
			return nil, err
		}
		state, finding, err := patchStartUIFinding(item.RepoSlug, attentionFindingID(item), payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "finding": finding}, nil
	case "promote_import_candidate":
		state, session, finding, err := promoteStartUIFindingImportCandidate(item.RepoSlug, strings.TrimSpace(item.ImportSessionID), strings.TrimSpace(item.ImportCandidateID))
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "session": session, "finding": finding}, nil
	case "drop_import_candidate":
		state, session, err := dropStartUIFindingImportCandidate(item.RepoSlug, strings.TrimSpace(item.ImportSessionID), strings.TrimSpace(item.ImportCandidateID))
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "session": session}, nil
	case "save_import_candidate":
		var payload startUIFindingImportCandidatePatchRequest
		if err := decodeAttentionActionPayload(body, &payload); err != nil {
			return nil, err
		}
		state, session, err := patchStartUIFindingImportCandidate(item.RepoSlug, strings.TrimSpace(item.ImportSessionID), strings.TrimSpace(item.ImportCandidateID), payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "session": session}, nil
	case "drop_approval":
		return executeAttentionDropApproval(item)
	case "requeue_work_item":
		if err := requeuePausedWorkItemByID(attentionWorkItemID(item), "ui"); err != nil {
			return nil, err
		}
		return loadAttentionWorkItemDetail(attentionWorkItemID(item))
	case "run_work_item":
		result, err := runWorkItemByID(cwd, attentionWorkItemID(item), nil, false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"item": result.Item, "draft": result.Draft, "links": result.Links}, nil
	case "fix_work_item":
		var payload startUIWorkItemFixRequest
		if err := decodeAttentionActionPayload(body, &payload); err != nil {
			return nil, err
		}
		item, err := fixWorkItemByID(cwd, workItemFixCommandOptions{
			ItemID:      attentionWorkItemID(item),
			Instruction: strings.TrimSpace(payload.Instruction),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"item": item}, nil
	case "drop_work_item":
		if err := dropWorkItemByID(attentionWorkItemID(item), "ui"); err != nil {
			return nil, err
		}
		return loadAttentionWorkItemDetail(attentionWorkItemID(item))
	case "restore_work_item":
		if err := restoreWorkItemByID(attentionWorkItemID(item), "ui"); err != nil {
			return nil, err
		}
		return loadAttentionWorkItemDetail(attentionWorkItemID(item))
	case "investigate_issue":
		return executeAttentionIssueInvestigation(item)
	case "launch_issue_work":
		return executeAttentionIssueLaunch(item)
	case "save_issue":
		var payload startUIIssuePatchRequest
		if err := decodeAttentionActionPayload(body, &payload); err != nil {
			return nil, err
		}
		state, issue, err := patchStartUIIssue(item.RepoSlug, item.IssueNumber, payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "issue": issue}, nil
	case "clear_issue_schedule":
		state, issue, err := patchStartUIIssue(item.RepoSlug, item.IssueNumber, startUIIssuePatchRequest{
			ClearSchedule:  true,
			DeferredReason: stringPtr(""),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "issue": issue}, nil
	case "drop_run":
		if err := startUIDropWorkRun(strings.TrimSpace(item.RunID)); err != nil {
			return nil, err
		}
		return map[string]any{"run_id": item.RunID, "dropped": true}, nil
	default:
		return nil, fmt.Errorf("unsupported attention action %q", action)
	}
}

func executeAttentionDropApproval(item attentionItem) (any, error) {
	switch strings.TrimSpace(item.Subtype) {
	case "work_run":
		if err := startUIDropWorkRun(strings.TrimSpace(item.RunID)); err != nil {
			return nil, err
		}
		return map[string]any{"run_id": item.RunID, "dropped": true}, nil
	case "work_item":
		if err := dropWorkItemByID(strings.TrimSpace(item.ItemID), "ui"); err != nil {
			return nil, err
		}
		return map[string]any{"item_id": item.ItemID, "dropped": true}, nil
	case "planned_item":
		state, removedItem, err := deleteStartUIPlannedItem(strings.TrimSpace(item.PlannedItemID))
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": state, "removed_item": removedItem}, nil
	case "scout_job":
		return mutateStartUIScoutItem(item.RepoSlug, strings.TrimSpace(item.ScoutJobID), "dismiss")
	default:
		return nil, fmt.Errorf("approval subtype %q cannot be dropped", defaultString(item.Subtype, "(unknown)"))
	}
}

func executeAttentionIssueInvestigation(item attentionItem) (any, error) {
	state, err := readStartWorkState(item.RepoSlug)
	if err != nil {
		return nil, err
	}
	issue, ok := state.Issues[fmt.Sprintf("%d", item.IssueNumber)]
	if !ok {
		return nil, fmt.Errorf("issue #%d is not tracked in start state", item.IssueNumber)
	}
	launch, err := startUISpawnIssueInvestigation(item.RepoSlug, issue)
	if err != nil {
		return nil, err
	}
	return map[string]any{"launch": launch, "issue": issue}, nil
}

func executeAttentionIssueLaunch(item attentionItem) (any, error) {
	state, err := readStartWorkState(item.RepoSlug)
	if err != nil {
		return nil, err
	}
	issue, ok := state.Issues[fmt.Sprintf("%d", item.IssueNumber)]
	if !ok {
		return nil, fmt.Errorf("issue #%d is not tracked in start state", item.IssueNumber)
	}
	launch, err := startUILaunchTrackedIssueWork(item.RepoSlug, issue)
	if err != nil {
		return nil, err
	}
	return map[string]any{"launch": launch, "issue": issue}, nil
}

func startUIResolveAttentionRun(item attentionItem) error {
	detail, err := loadStartUIWorkRunDetail(strings.TrimSpace(item.RunID))
	if err != nil {
		return err
	}
	if !detail.ResolveAllowed {
		return fmt.Errorf("resolve is only available for blocked local runs with recoverable final apply state")
	}
	return startUIResolveWorkRun(strings.TrimSpace(item.RunID))
}

func decodeAttentionActionPayload(body []byte, target any) error {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("invalid json")
	}
	return nil
}

func attentionWorkItemID(item attentionItem) string {
	return defaultString(strings.TrimSpace(item.ItemID), strings.TrimSpace(item.ID))
}

func attentionFindingID(item attentionItem) string {
	return defaultString(strings.TrimSpace(item.FindingID), strings.TrimSpace(item.ID))
}

func slicesContainsString(items []string, expected string) bool {
	for _, item := range items {
		if item == expected {
			return true
		}
	}
	return false
}

func stringPtr(value string) *string {
	return &value
}
