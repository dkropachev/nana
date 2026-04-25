package gocli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	startUITaskStatusRunning   = "running"
	startUITaskStatusFailed    = "failed"
	startUITaskStatusQueued    = "queued"
	startUITaskStatusPaused    = "paused"
	startUITaskStatusInReview  = "in_review"
	startUITaskStatusBlocked   = "blocked"
	startUITaskStatusDismissed = "dismissed"
	startUITaskStatusCompleted = "completed"
)

type startUITaskSummary struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	RepoSlug       string `json:"repo_slug,omitempty"`
	Title          string `json:"title"`
	Summary        string `json:"summary,omitempty"`
	Description    string `json:"description,omitempty"`
	Status         string `json:"status"`
	RawStatus      string `json:"raw_status,omitempty"`
	AttentionState string `json:"attention_state,omitempty"`
	Priority       int    `json:"priority,omitempty"`
	PriorityLabel  string `json:"priority_label,omitempty"`
	ScheduleAt     string `json:"schedule_at,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	ExternalURL    string `json:"external_url,omitempty"`
	WorkType       string `json:"work_type,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	CanOpenJob     bool   `json:"can_open_job,omitempty"`
}

type startUITaskDetail struct {
	Summary       startUITaskSummary          `json:"summary"`
	Issue         *startUIIssueQueueItem      `json:"issue,omitempty"`
	PlannedItem   *startWorkPlannedItem       `json:"planned_item,omitempty"`
	ScoutJob      *startWorkScoutJob          `json:"scout_job,omitempty"`
	Investigation *startUIInvestigationDetail `json:"investigation,omitempty"`
	WorkRun       *startUIWorkRunDetail       `json:"work_run,omitempty"`
	WorkItem      *workItemDetail             `json:"work_item,omitempty"`
	ServiceTask   *startWorkServiceTask       `json:"service_task,omitempty"`
	RelatedRun    *startUIWorkRunDetail       `json:"related_run,omitempty"`
}

type startUITaskTemplate struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description,omitempty"`
	ScoutPromptPreview string `json:"scout_prompt_preview,omitempty"`
	RepoSlug           string `json:"repo_slug,omitempty"`
	BuiltIn            bool   `json:"built_in,omitempty"`
	LaunchKindHint     string `json:"launch_kind_hint,omitempty"`
	ScoutRoleHint      string `json:"scout_role_hint,omitempty"`
	WorkTypeHint       string `json:"work_type_hint,omitempty"`
	DefaultPriority    int    `json:"default_priority,omitempty"`
}

type startUITaskCreateRequest struct {
	RepoSlug       string `json:"repo_slug"`
	Description    string `json:"description"`
	TemplateID     string `json:"template_id,omitempty"`
	ScheduleAt     string `json:"schedule_at,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Priority       *int   `json:"priority,omitempty"`
}

type startUITaskTemplateCreateRequest struct {
	RepoSlug       string `json:"repo_slug"`
	Name           string `json:"name,omitempty"`
	Description    string `json:"description"`
	BaseTemplateID string `json:"base_template_id,omitempty"`
}

type startUITaskInferenceResult struct {
	Title              string   `json:"title"`
	LaunchKind         string   `json:"launch_kind"`
	WorkType           string   `json:"work_type,omitempty"`
	InvestigationQuery string   `json:"investigation_query,omitempty"`
	ScoutRole          string   `json:"scout_role,omitempty"`
	ScoutFocus         []string `json:"scout_focus,omitempty"`
	FindingsHandling   string   `json:"findings_handling,omitempty"`
}

var startUIInferTaskPlan = inferStartUITaskPlan

func (h *startUIAPI) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := listStartUITasks(h.cwd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, map[string]any{"items": items})
	case http.MethodPost:
		var payload startUITaskCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		repoSlug := strings.TrimSpace(payload.RepoSlug)
		if repoSlug == "" {
			http.Error(w, "repo_slug is required", http.StatusBadRequest)
			return
		}
		idempotencyKey, err := resolveStartUITaskIdempotencyKey(r, payload)
		if err != nil {
			writeJSONResponseWithStatus(w, http.StatusBadRequest, map[string]any{
				"code":    "invalid_idempotency_key",
				"message": err.Error(),
			})
			return
		}
		payload.IdempotencyKey = idempotencyKey
		state, item, inference, err := createStartUITask(repoSlug, payload)
		if err != nil {
			if conflict, ok := asStartUIIdempotencyConflictError(err); ok {
				writeJSONResponseWithStatus(w, http.StatusConflict, map[string]any{
					"code":    "idempotency_conflict",
					"message": conflict.Error(),
				})
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
		writeJSONResponse(w, map[string]any{
			"state":        state,
			"planned_item": item,
			"inference":    inference,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *startUIAPI) handleTask(w http.ResponseWriter, r *http.Request) {
	taskID, action, ok := parseStartUITaskRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		detail, err := loadStartUITaskDetail(h.cwd, taskID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONResponse(w, detail)
	case r.Method == http.MethodPatch && action == "":
		var payload startUIPlannedItemPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		response, err := patchStartUITask(h.cwd, taskID, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, response)
	case r.Method == http.MethodPost && action != "":
		payload, err := mutateStartUITask(h.cwd, taskID, action)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, payload)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *startUIAPI) handleTaskTemplates(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		repoSlug := strings.TrimSpace(r.URL.Query().Get("repo_slug"))
		templates, err := listStartUITaskTemplates(repoSlug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"items": templates})
	case http.MethodPost:
		var payload startUITaskTemplateCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		repoSlug := strings.TrimSpace(payload.RepoSlug)
		if repoSlug == "" {
			http.Error(w, "repo_slug is required", http.StatusBadRequest)
			return
		}
		state, template, err := createStartUITaskTemplate(repoSlug, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": state, "template": template})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func listStartUITasks(cwd string) ([]startUITaskSummary, error) {
	return listCanonicalTasks(cwd)
}

func loadStartUITaskDetail(cwd string, taskID string) (startUITaskDetail, error) {
	summary, err := findStartUITaskSummary(cwd, taskID)
	if err != nil {
		return startUITaskDetail{}, err
	}
	detail := startUITaskDetail{Summary: summary}
	if summary.CanOpenJob && strings.TrimSpace(summary.RunID) != "" {
		if relatedRun, runErr := loadStartUIWorkRunDetail(summary.RunID); runErr == nil {
			detail.RelatedRun = &relatedRun
		}
	}

	switch {
	case strings.HasPrefix(taskID, "issue:"):
		repoSlug, issueNumber, err := parseStartUITaskIssueID(taskID)
		if err != nil {
			return startUITaskDetail{}, err
		}
		state, err := readStartWorkState(repoSlug)
		if err != nil {
			return startUITaskDetail{}, err
		}
		issue, ok := state.Issues[strconv.Itoa(issueNumber)]
		if !ok {
			return startUITaskDetail{}, fmt.Errorf("issue task %s was not found", taskID)
		}
		item := startUIIssueQueueItemFromState(repoSlug, issue)
		detail.Issue = &item
	case strings.HasPrefix(taskID, "planned-item:"):
		itemID := strings.TrimSpace(strings.TrimPrefix(taskID, "planned-item:"))
		repoSlug, _, item, err := findStartUIPlannedItem(itemID)
		if err != nil {
			return startUITaskDetail{}, err
		}
		item.RepoSlug = defaultString(strings.TrimSpace(item.RepoSlug), repoSlug)
		detail.PlannedItem = &item
	case strings.HasPrefix(taskID, "scout-job:"):
		repoSlug, scoutJobID, err := parseStartUITaskScopedID(taskID, "scout-job:")
		if err != nil {
			return startUITaskDetail{}, err
		}
		state, err := readStartWorkState(repoSlug)
		if err != nil {
			return startUITaskDetail{}, err
		}
		job, ok := state.ScoutJobs[scoutJobID]
		if !ok {
			return startUITaskDetail{}, fmt.Errorf("scout job task %s was not found", taskID)
		}
		detail.ScoutJob = &job
	case strings.HasPrefix(taskID, "investigation:"):
		runID := strings.TrimSpace(strings.TrimPrefix(taskID, "investigation:"))
		investigation, err := loadStartUIInvestigationDetail(cwd, runID)
		if err != nil {
			return startUITaskDetail{}, err
		}
		detail.Investigation = &investigation
	case strings.HasPrefix(taskID, "work-run:"):
		runID := strings.TrimSpace(strings.TrimPrefix(taskID, "work-run:"))
		run, err := loadStartUIWorkRunDetail(runID)
		if err != nil {
			return startUITaskDetail{}, err
		}
		detail.WorkRun = &run
		detail.RelatedRun = &run
	case strings.HasPrefix(taskID, "work-item:"):
		itemID := strings.TrimSpace(strings.TrimPrefix(taskID, "work-item:"))
		item, err := readWorkItemDetail(itemID)
		if err != nil {
			return startUITaskDetail{}, err
		}
		detail.WorkItem = &item
		if detail.RelatedRun == nil && strings.TrimSpace(item.Item.LinkedRunID) != "" {
			if relatedRun, runErr := loadStartUIWorkRunDetail(item.Item.LinkedRunID); runErr == nil {
				detail.RelatedRun = &relatedRun
			}
		}
	case strings.HasPrefix(taskID, "service-task:"):
		repoSlug, serviceTaskID, err := parseStartUITaskScopedID(taskID, "service-task:")
		if err != nil {
			return startUITaskDetail{}, err
		}
		state, err := readStartWorkState(repoSlug)
		if err != nil {
			return startUITaskDetail{}, err
		}
		task, ok := state.ServiceTasks[serviceTaskID]
		if !ok {
			return startUITaskDetail{}, fmt.Errorf("service task %s was not found", taskID)
		}
		detail.ServiceTask = &task
	default:
		return startUITaskDetail{}, fmt.Errorf("task %s is not supported", taskID)
	}
	return detail, nil
}

func findStartUITaskSummary(cwd string, taskID string) (startUITaskSummary, error) {
	items, err := listStartUITasks(cwd)
	if err != nil {
		return startUITaskSummary{}, err
	}
	for _, item := range items {
		if item.ID == taskID {
			return item, nil
		}
	}
	return startUITaskSummary{}, fmt.Errorf("task %s was not found", taskID)
}

func parseStartUITaskRoute(path string) (string, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api/v1/tasks/"), "/")
	if trimmed == "" {
		return "", "", false
	}
	if lastSlash := strings.LastIndex(trimmed, "/"); lastSlash >= 0 {
		taskID := strings.TrimSpace(trimmed[:lastSlash])
		action := strings.TrimSpace(trimmed[lastSlash+1:])
		if taskID != "" && startUITaskActionName(action) {
			return taskID, action, true
		}
	}
	return strings.TrimSpace(trimmed), "", strings.TrimSpace(trimmed) != ""
}

func startUITaskActionName(value string) bool {
	switch strings.TrimSpace(value) {
	case "run-now", "dismiss", "retry", "recover":
		return true
	default:
		return false
	}
}

func mutateStartUITask(cwd string, taskID string, action string) (map[string]any, error) {
	detail, err := loadStartUITaskDetail(cwd, taskID)
	if err != nil {
		return nil, err
	}
	switch {
	case detail.PlannedItem != nil:
		switch action {
		case "run-now":
			repoSlug, state, item, err := findStartUIPlannedItem(detail.PlannedItem.ID)
			if err != nil {
				return nil, err
			}
			updatedState, updatedItem, launch, err := launchStartUIPlannedItemNow(repoSlug, state, item)
			if err != nil {
				return nil, err
			}
			return map[string]any{"state": updatedState, "planned_item": updatedItem, "launch": launch}, nil
		case "dismiss":
			updatedState, removedItem, err := deleteStartUIPlannedItem(detail.PlannedItem.ID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"state": updatedState, "removed_item": removedItem}, nil
		}
	case detail.ScoutJob != nil:
		switch action {
		case "retry", "dismiss":
			updatedState, updatedJob, err := mutateStartWorkScoutJob(detail.Summary.RepoSlug, detail.ScoutJob.ID, action)
			if err != nil {
				return nil, err
			}
			return map[string]any{"state": updatedState, "scout_job": updatedJob}, nil
		}
	case detail.WorkItem != nil:
		switch action {
		case "run-now":
			result, err := runWorkItemByID(cwd, detail.WorkItem.Item.ID, nil, false)
			if err != nil {
				return nil, err
			}
			return map[string]any{"item": result.Item}, nil
		case "recover":
			item, err := recoverWorkItemByID(cwd, detail.WorkItem.Item.ID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"item": item}, nil
		case "dismiss":
			if err := dropWorkItemByID(detail.WorkItem.Item.ID, "ui"); err != nil {
				return nil, err
			}
			return map[string]any{"item_id": detail.WorkItem.Item.ID, "dismissed": true}, nil
		case "retry":
			if err := requeuePausedWorkItemByID(detail.WorkItem.Item.ID, "ui"); err != nil {
				return nil, err
			}
			return map[string]any{"item_id": detail.WorkItem.Item.ID, "requeued": true}, nil
		}
	}
	return nil, fmt.Errorf("task %s does not support action %q", taskID, action)
}

func patchStartUITask(cwd string, taskID string, payload startUIPlannedItemPatchRequest) (map[string]any, error) {
	switch {
	case strings.HasPrefix(taskID, "planned-item:"):
		itemID := strings.TrimSpace(strings.TrimPrefix(taskID, "planned-item:"))
		updatedState, updatedItem, err := patchStartUIPlannedItem(itemID, payload)
		if err != nil {
			return nil, err
		}
		detail, err := loadStartUITaskDetail(cwd, taskID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": updatedState, "planned_item": updatedItem, "detail": detail}, nil
	case strings.HasPrefix(taskID, "work-item:"):
		if payload.WorkType == nil {
			return nil, fmt.Errorf("task %s only supports patching work_type", taskID)
		}
		itemID := strings.TrimSpace(strings.TrimPrefix(taskID, "work-item:"))
		if _, err := patchWorkItemByID(itemID, payload.WorkType, "ui"); err != nil {
			return nil, err
		}
		detail, err := loadStartUITaskDetail(cwd, taskID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"detail": detail}, nil
	default:
		return nil, fmt.Errorf("task %s is not patchable", taskID)
	}
}

func listStartUITaskTemplates(repoSlug string) ([]startUITaskTemplate, error) {
	templates := startUIBuiltinTaskTemplates(repoSlug)
	if strings.TrimSpace(repoSlug) == "" {
		return templates, nil
	}
	custom, err := withLocalWorkReadStore(func(store *localWorkDBStore) ([]startWorkTaskTemplate, error) {
		return store.listTaskTemplates(repoSlug)
	})
	if err != nil {
		return nil, err
	}
	items := make([]startUITaskTemplate, 0, len(custom))
	for _, template := range custom {
		items = append(items, startUITaskTemplate{
			ID:              template.ID,
			Name:            template.Name,
			Description:     template.Description,
			RepoSlug:        repoSlug,
			LaunchKindHint:  template.LaunchKindHint,
			ScoutRoleHint:   template.ScoutRoleHint,
			WorkTypeHint:    template.WorkTypeHint,
			DefaultPriority: template.DefaultPriority,
		})
	}
	slices.SortFunc(items, func(a, b startUITaskTemplate) int {
		if a.Name != b.Name {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		return strings.Compare(a.ID, b.ID)
	})
	return append(templates, items...), nil
}

func createStartUITaskTemplate(repoSlug string, payload startUITaskTemplateCreateRequest) (*startWorkState, startWorkTaskTemplate, error) {
	description := strings.TrimSpace(payload.Description)
	if description == "" {
		return nil, startWorkTaskTemplate{}, fmt.Errorf("description is required")
	}
	base, _ := resolveStartUITaskTemplate(repoSlug, strings.TrimSpace(payload.BaseTemplateID))
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		name = startUITaskTemplateName(description)
	}
	now := ISOTimeNow()
	template := startWorkTaskTemplate{
		ID:              fmt.Sprintf("task-template-%d", time.Now().UnixNano()),
		Name:            name,
		Description:     description,
		LaunchKindHint:  base.LaunchKindHint,
		ScoutRoleHint:   base.ScoutRoleHint,
		WorkTypeHint:    base.WorkTypeHint,
		DefaultPriority: clampTaskPriority(base.DefaultPriority),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.writeTaskTemplate(repoSlug, template)
	}); err != nil {
		return nil, startWorkTaskTemplate{}, err
	}
	return nil, template, nil
}

func createStartUITask(repoSlug string, payload startUITaskCreateRequest) (*startWorkState, startWorkPlannedItem, startUITaskInferenceResult, error) {
	idempotencyKey, err := normalizeStartUITaskIdempotencyKey(payload.IdempotencyKey)
	if err != nil {
		return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, err
	}
	payload.IdempotencyKey = idempotencyKey
	template, err := resolveStartUITaskTemplate(repoSlug, strings.TrimSpace(payload.TemplateID))
	if err != nil {
		return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, err
	}
	description := strings.TrimSpace(payload.Description)
	fixedScoutTemplate := startUITaskTemplateIsFixedScout(template)
	if description == "" && !fixedScoutTemplate {
		return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, fmt.Errorf("description is required")
	}
	fingerprintDescription := description
	if fixedScoutTemplate {
		fingerprintDescription = ""
	}
	requestFingerprint := startUITaskCreateFingerprint(repoSlug, template, fingerprintDescription, payload.ScheduleAt, payload.Priority)
	if idempotencyKey != "" {
		state, item, inference, found, err := findStartUITaskIdempotentReplay(repoSlug, idempotencyKey, requestFingerprint)
		if err != nil {
			return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, err
		}
		if found {
			return state, item, inference, nil
		}
	}
	var inference startUITaskInferenceResult
	if fixedScoutTemplate {
		inference = fixedScoutTemplateInference(template)
		description = ""
	} else {
		inference, err = startUIInferTaskPlan(repoSlug, description, template)
		if err != nil {
			return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, err
		}
	}
	scheduleAt := strings.TrimSpace(payload.ScheduleAt)
	if scheduleAt == "" {
		scheduleAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		if _, err := time.Parse(time.RFC3339, scheduleAt); err != nil {
			return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, fmt.Errorf("schedule_at must be RFC3339")
		}
	}
	priority := template.DefaultPriority
	if payload.Priority != nil {
		priority = *payload.Priority
	}
	priority = clampTaskPriority(priority)
	request := startUIPlannedItemRequest{
		Title:                  inference.Title,
		Description:            description,
		WorkType:               inference.WorkType,
		Priority:               &priority,
		ScheduleAt:             scheduleAt,
		LaunchKind:             inference.LaunchKind,
		FindingsHandling:       inference.FindingsHandling,
		InvestigationQuery:     inference.InvestigationQuery,
		ScoutRole:              inference.ScoutRole,
		ScoutDestination:       improvementDestinationReview,
		ScoutFocus:             append([]string{}, inference.ScoutFocus...),
		IdempotencyKey:         idempotencyKey,
		IdempotencyFingerprint: requestFingerprint,
	}
	state, item, err := createStartUIPlannedItem(repoSlug, request)
	if err != nil {
		return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, err
	}
	return state, item, inference, nil
}

func resolveStartUITaskIdempotencyKey(r *http.Request, payload startUITaskCreateRequest) (string, error) {
	bodyKey := strings.TrimSpace(payload.IdempotencyKey)
	headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if bodyKey != "" && headerKey != "" && bodyKey != headerKey {
		return "", fmt.Errorf("idempotency key mismatch between request body and Idempotency-Key header")
	}
	return normalizeStartUITaskIdempotencyKey(defaultString(headerKey, bodyKey))
}

func normalizeStartUITaskIdempotencyKey(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > 200 {
		return "", fmt.Errorf("idempotency key must be between 1 and 200 characters")
	}
	return trimmed, nil
}

func startUITaskCreateFingerprint(repoSlug string, template startUITaskTemplate, description string, scheduleAt string, priority *int) string {
	request := map[string]any{
		"repo_slug":         strings.TrimSpace(repoSlug),
		"template_id":       defaultString(strings.TrimSpace(template.ID), "template:custom"),
		"description":       strings.TrimSpace(description),
		"schedule_at":       strings.TrimSpace(scheduleAt),
		"priority_explicit": priority != nil,
	}
	if priority != nil {
		request["priority"] = clampTaskPriority(*priority)
	}
	return hashJSON(request)
}

func findStartUITaskIdempotentReplay(repoSlug string, idempotencyKey string, fingerprint string) (*startWorkState, startWorkPlannedItem, startUITaskInferenceResult, bool, error) {
	state, err := readStartWorkState(repoSlug)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, false, nil
		}
		return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, false, err
	}
	item, found, err := startUIPlannedItemForIdempotencyKey(state, idempotencyKey, []string{fingerprint})
	if err != nil {
		return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, false, err
	}
	if !found {
		return nil, startWorkPlannedItem{}, startUITaskInferenceResult{}, false, nil
	}
	return state, item, startUITaskInferenceFromPlannedItem(item), true, nil
}

func startUITaskInferenceFromPlannedItem(item startWorkPlannedItem) startUITaskInferenceResult {
	return startUITaskInferenceResult{
		Title:              strings.TrimSpace(item.Title),
		LaunchKind:         strings.TrimSpace(item.LaunchKind),
		WorkType:           normalizeWorkType(item.WorkType),
		InvestigationQuery: strings.TrimSpace(item.InvestigationQuery),
		ScoutRole:          strings.TrimSpace(item.ScoutRole),
		ScoutFocus:         append([]string{}, item.ScoutFocus...),
		FindingsHandling:   normalizeFindingsHandling(item.FindingsHandling, item.ScoutDestination, item.LaunchKind),
	}
}

func asStartUIIdempotencyConflictError(err error) (startUIIdempotencyConflictError, bool) {
	var target startUIIdempotencyConflictError
	ok := errors.As(err, &target)
	return target, ok
}

func startUITaskTemplateIsFixedScout(template startUITaskTemplate) bool {
	return template.BuiltIn &&
		strings.HasPrefix(strings.TrimSpace(template.ID), "template:scout:") &&
		strings.TrimSpace(template.LaunchKindHint) == "manual_scout" &&
		scoutRoleListIncludes(supportedScoutRoleOrder, strings.TrimSpace(template.ScoutRoleHint))
}

func fixedScoutTemplateInference(template startUITaskTemplate) startUITaskInferenceResult {
	role := strings.TrimSpace(template.ScoutRoleHint)
	label := defaultString(strings.TrimSpace(template.Name), scoutDisplayLabel(role))
	return startUITaskInferenceResult{
		Title:            "Run " + label,
		LaunchKind:       "manual_scout",
		ScoutRole:        role,
		FindingsHandling: startWorkFindingsHandlingManualReview,
	}
}

func resolveStartUITaskTemplate(repoSlug string, templateID string) (startUITaskTemplate, error) {
	for _, template := range startUIBuiltinTaskTemplates(repoSlug) {
		if template.ID == templateID {
			return template, nil
		}
	}
	if strings.TrimSpace(templateID) == "" {
		for _, template := range startUIBuiltinTaskTemplates(repoSlug) {
			if template.ID == "template:custom" {
				return template, nil
			}
		}
	}
	if strings.TrimSpace(repoSlug) == "" {
		return startUITaskTemplate{}, fmt.Errorf("task template %q was not found", templateID)
	}
	custom, err := withLocalWorkReadStore(func(store *localWorkDBStore) ([]startWorkTaskTemplate, error) {
		return store.listTaskTemplates(repoSlug)
	})
	if err != nil {
		return startUITaskTemplate{}, err
	}
	for _, template := range custom {
		if template.ID == templateID {
			return startUITaskTemplate{
				ID:              template.ID,
				Name:            template.Name,
				Description:     template.Description,
				RepoSlug:        repoSlug,
				LaunchKindHint:  template.LaunchKindHint,
				ScoutRoleHint:   template.ScoutRoleHint,
				WorkTypeHint:    template.WorkTypeHint,
				DefaultPriority: template.DefaultPriority,
			}, nil
		}
	}
	return startUITaskTemplate{}, fmt.Errorf("task template %q was not found", templateID)
}

func startUIBuiltinTaskTemplates(repoSlug string) []startUITaskTemplate {
	templates := []startUITaskTemplate{
		{
			ID:              "template:custom",
			Name:            "Custom task",
			Description:     "Let Nana infer the right task kind from your description.",
			RepoSlug:        repoSlug,
			BuiltIn:         true,
			DefaultPriority: 3,
		},
		{
			ID:              "template:implementation",
			Name:            "Implementation",
			Description:     "Bias this description toward a coding or execution task.",
			RepoSlug:        repoSlug,
			BuiltIn:         true,
			LaunchKindHint:  "local_work",
			DefaultPriority: 3,
		},
		{
			ID:              "template:investigation",
			Name:            "Investigation",
			Description:     "Bias this description toward source-backed investigation work.",
			RepoSlug:        repoSlug,
			BuiltIn:         true,
			LaunchKindHint:  "investigation",
			DefaultPriority: 3,
		},
	}
	for _, entry := range startUIScoutCatalog() {
		templates = append(templates, startUITaskTemplate{
			ID:                 "template:scout:" + entry.Role,
			Name:               entry.DisplayLabel,
			ScoutPromptPreview: startUITaskScoutPromptPreview(entry.Role),
			RepoSlug:           repoSlug,
			BuiltIn:            true,
			LaunchKindHint:     "manual_scout",
			ScoutRoleHint:      entry.Role,
			DefaultPriority:    3,
		})
	}
	return templates
}

func startUITaskScoutPromptPreview(role string) string {
	content, err := readScoutPrompt(role)
	if err == nil && strings.TrimSpace(content) != "" {
		return strings.TrimSpace(content)
	}
	return fmt.Sprintf("Run %s for this repo.", scoutDisplayLabel(role))
}

func inferStartUITaskPlan(repoSlug string, description string, template startUITaskTemplate) (startUITaskInferenceResult, error) {
	fallback := heuristicStartUITaskInference(description, template)
	commandDir := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
	if commandDir == "" {
		commandDir = strings.TrimSpace(githubManagedPaths(repoSlug).RepoRoot)
	}
	if commandDir == "" {
		return fallback, nil
	}
	repoRoot := strings.TrimSpace(githubManagedPaths(repoSlug).RepoRoot)
	if repoRoot == "" {
		repoRoot = commandDir
	}
	scopedCodexHome, err := ensureScopedCodexHome(
		ResolveCodexHomeForLaunch(commandDir),
		filepath.Join(repoRoot, ".nana", "start", "codex-home", "task-inference"),
	)
	if err != nil {
		return fallback, nil
	}
	prompt := buildStartUITaskInferencePrompt(repoSlug, description, template)
	transport := promptTransportForSize(prompt, structuredPromptStdinThreshold)
	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       commandDir,
		InstructionsRoot: commandDir,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", commandDir},
		Prompt:           prompt,
		PromptTransport:  transport,
		CheckpointPath:   filepath.Join(repoRoot, ".nana", "start", "task-inference-checkpoints", sanitizePathToken(template.ID)+"-"+sanitizePathToken(startUITaskTemplateName(description))+".json"),
		StepKey:          "start-ui-task-inference",
		ResumeStrategy:   codexResumeSamePrompt,
		Env:              append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+commandDir),
		RateLimitPolicy:  codexRateLimitPolicyReturnPause,
	})
	if err != nil {
		return fallback, nil
	}
	parsed, err := parseStartUITaskInferenceResult([]byte(result.Stdout), description, template)
	if err != nil {
		return fallback, nil
	}
	return parsed, nil
}

func buildStartUITaskInferencePrompt(repoSlug string, description string, template startUITaskTemplate) string {
	lines := []string{
		"You are classifying a Nana task from a short operator description.",
		"Return JSON only with this schema:",
		`{"title":"...","launch_kind":"local_work|investigation|manual_scout","work_type":"bug_fix|refactor|feature|test_only","investigation_query":"...","scout_role":"improvement-scout|enhancement-scout|backend-performance-scout|ui-scout","scout_focus":["..."],"findings_handling":"manual_review|auto_promote"}`,
		"Rules:",
		"- Pick the single best launch_kind for the described work.",
		"- Use `investigation` when the user wants diagnosis, explanation, or evidence gathering.",
		"- Use `manual_scout` only for scout-style audits or when the template strongly hints a scout role.",
		"- Use `backend-performance-scout` for backend, API, worker, queue, latency, throughput, CPU, hot-path, or performance audits.",
		"- Use `ui-scout` for UI, UX, interface, or screen audits.",
		"- Otherwise prefer `local_work`.",
		"- Keep `title` short and specific.",
		"- Keep `investigation_query` empty unless launch_kind is `investigation`.",
		"- Keep `scout_role` and `scout_focus` empty unless launch_kind is `manual_scout`.",
		"- Keep `findings_handling` to `manual_review` unless auto-promote is explicitly implied.",
		fmt.Sprintf("Repo: %s", repoSlug),
		fmt.Sprintf("Template: %s", defaultString(template.Name, "Custom task")),
	}
	if hint := strings.TrimSpace(template.LaunchKindHint); hint != "" {
		lines = append(lines, fmt.Sprintf("Launch hint: %s", hint))
	}
	if hint := strings.TrimSpace(template.ScoutRoleHint); hint != "" {
		lines = append(lines, fmt.Sprintf("Scout role hint: %s", hint))
	}
	if hint := strings.TrimSpace(template.WorkTypeHint); hint != "" {
		lines = append(lines, fmt.Sprintf("Work type hint: %s", hint))
	}
	if template.Description != "" {
		lines = append(lines, fmt.Sprintf("Template description: %s", compactPromptValue(template.Description, 0, 240)))
	}
	lines = append(lines, "Task description:", compactPromptValue(description, 0, 1200), "Respond with JSON only.")
	return strings.Join(lines, "\n")
}

func parseStartUITaskInferenceResult(content []byte, description string, template startUITaskTemplate) (startUITaskInferenceResult, error) {
	trimmed := bytes.TrimSpace(extractImprovementJSONObject(content))
	if len(trimmed) == 0 {
		trimmed = bytes.TrimSpace(content)
	}
	jsonText, err := extractJSONObject(string(trimmed))
	if err == nil {
		trimmed = []byte(jsonText)
	}
	var result startUITaskInferenceResult
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return startUITaskInferenceResult{}, fmt.Errorf("task inference output did not match the JSON schema")
	}
	return normalizeStartUITaskInferenceResult(result, description, template)
}

func normalizeStartUITaskInferenceResult(result startUITaskInferenceResult, description string, template startUITaskTemplate) (startUITaskInferenceResult, error) {
	fallback := heuristicStartUITaskInference(description, template)
	result.Title = strings.TrimSpace(result.Title)
	result.LaunchKind = strings.TrimSpace(result.LaunchKind)
	result.WorkType = normalizeWorkType(result.WorkType)
	result.InvestigationQuery = strings.TrimSpace(result.InvestigationQuery)
	result.ScoutRole = strings.TrimSpace(result.ScoutRole)
	result.FindingsHandling = normalizeFindingsHandling(result.FindingsHandling, improvementDestinationReview, result.LaunchKind)
	result.ScoutFocus = uniqueStrings(trimAndFilterStrings(result.ScoutFocus))

	switch result.LaunchKind {
	case "local_work":
		if result.WorkType == "" {
			result.WorkType = fallback.WorkType
		}
	case "investigation":
		if result.InvestigationQuery == "" {
			result.InvestigationQuery = strings.TrimSpace(description)
		}
		result.WorkType = ""
		result.ScoutRole = ""
		result.ScoutFocus = nil
	case "manual_scout":
		if !scoutRoleListIncludes(supportedScoutRoleOrder, result.ScoutRole) {
			result.ScoutRole = fallback.ScoutRole
		}
		result.WorkType = ""
		result.InvestigationQuery = ""
	default:
		return fallback, nil
	}
	if result.Title == "" {
		result.Title = fallback.Title
	}
	if result.FindingsHandling == "" {
		result.FindingsHandling = startWorkFindingsHandlingManualReview
	}
	return result, nil
}

func heuristicStartUITaskInference(description string, template startUITaskTemplate) startUITaskInferenceResult {
	text := strings.TrimSpace(description)
	lower := strings.ToLower(text)
	result := startUITaskInferenceResult{
		Title:            startUITaskTitleFromDescription(text),
		FindingsHandling: startWorkFindingsHandlingManualReview,
	}
	switch strings.TrimSpace(template.LaunchKindHint) {
	case "investigation":
		result.LaunchKind = "investigation"
	case "manual_scout":
		result.LaunchKind = "manual_scout"
		result.ScoutRole = defaultString(strings.TrimSpace(template.ScoutRoleHint), improvementScoutRole)
	case "local_work":
		result.LaunchKind = "local_work"
	}
	if result.LaunchKind == "" {
		switch {
		case strings.Contains(lower, "investigat"), strings.Contains(lower, "root cause"), strings.Contains(lower, "why "), strings.Contains(lower, "diagnos"), strings.Contains(lower, "analy"):
			result.LaunchKind = "investigation"
		case strings.Contains(lower, "scout"), strings.Contains(lower, "audit"), strings.Contains(lower, "ui review"), strings.Contains(lower, "ux"), strings.Contains(lower, "interface"):
			result.LaunchKind = "manual_scout"
		default:
			result.LaunchKind = "local_work"
		}
	}
	switch result.LaunchKind {
	case "investigation":
		result.InvestigationQuery = text
	case "manual_scout":
		result.ScoutRole = defaultString(strings.TrimSpace(result.ScoutRole), startUITaskScoutRoleFromDescription(lower))
		result.ScoutFocus = startUITaskFocusFromDescription(text)
	default:
		result.WorkType = defaultString(normalizeWorkType(template.WorkTypeHint), inferWorkTypeFromText(text).WorkType)
		if result.WorkType == "" {
			result.WorkType = workTypeFeature
		}
	}
	return result
}

func compareStartUITaskSummary(left startUITaskSummary, right startUITaskSummary) int {
	leftRank := startUITaskStatusRank(left.Status)
	rightRank := startUITaskStatusRank(right.Status)
	if leftRank != rightRank {
		return leftRank - rightRank
	}
	leftPriority := taskPriorityOrDefault(left.Priority)
	rightPriority := taskPriorityOrDefault(right.Priority)
	if leftPriority != rightPriority {
		return leftPriority - rightPriority
	}
	leftSchedule := strings.TrimSpace(left.ScheduleAt)
	rightSchedule := strings.TrimSpace(right.ScheduleAt)
	if leftSchedule != rightSchedule {
		if leftSchedule == "" {
			return 1
		}
		if rightSchedule == "" {
			return -1
		}
		if leftSchedule < rightSchedule {
			return -1
		}
		return 1
	}
	if left.UpdatedAt != right.UpdatedAt {
		if left.UpdatedAt > right.UpdatedAt {
			return -1
		}
		return 1
	}
	return strings.Compare(left.ID, right.ID)
}

func startUITaskStatusRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case startUITaskStatusRunning:
		return 0
	case startUITaskStatusFailed:
		return 1
	case startUITaskStatusBlocked:
		return 2
	case startUITaskStatusQueued:
		return 3
	case startUITaskStatusPaused:
		return 4
	case startUITaskStatusInReview:
		return 5
	case startUITaskStatusDismissed:
		return 6
	case startUITaskStatusCompleted:
		return 7
	default:
		return 8
	}
}

func startUITaskSummaryFromIssue(item startUIIssueQueueItem) startUITaskSummary {
	status := startUITaskStatusQueued
	switch strings.ToLower(strings.TrimSpace(item.Status)) {
	case startWorkStatusInProgress, startWorkStatusReconciling:
		status = startUITaskStatusRunning
	case startWorkStatusBlocked:
		status = startUITaskStatusBlocked
	case startWorkStatusCompleted, startWorkStatusCopied, startWorkStatusPromoted:
		status = startUITaskStatusCompleted
	case startWorkStatusNotActioned:
		status = startUITaskStatusDismissed
	}
	if strings.TrimSpace(item.ScheduleAt) != "" && status == startUITaskStatusQueued {
		status = startUITaskStatusQueued
	}
	return startUITaskSummary{
		ID:             "issue:" + item.ID,
		Kind:           "issue",
		RepoSlug:       item.RepoSlug,
		Title:          fmt.Sprintf("#%d %s", item.SourceNumber, strings.TrimSpace(item.Title)),
		Summary:        defaultString(strings.TrimSpace(item.TriageRationale), strings.TrimSpace(item.BlockedReason)),
		Status:         status,
		RawStatus:      item.Status,
		AttentionState: item.AttentionState,
		Priority:       item.Priority,
		PriorityLabel:  startWorkPriorityLabel(item.Priority),
		ScheduleAt:     item.ScheduleAt,
		UpdatedAt:      item.UpdatedAt,
		ExternalURL:    defaultString(strings.TrimSpace(item.PublishedPRURL), item.SourceURL),
		WorkType:       item.WorkType,
		RunID:          item.LastRunID,
		CanOpenJob:     strings.TrimSpace(item.LastRunID) != "",
	}
}

func startUITaskSummaryFromPlannedItem(item startWorkPlannedItem) startUITaskSummary {
	status := startUITaskStatusQueued
	switch strings.ToLower(strings.TrimSpace(item.State)) {
	case startPlannedItemLaunching:
		status = startUITaskStatusRunning
	case startPlannedItemFailed:
		status = startUITaskStatusBlocked
	case startPlannedItemLaunched:
		status = startUITaskStatusCompleted
	}
	return startUITaskSummary{
		ID:             "planned-item:" + item.ID,
		Kind:           "planned_item",
		RepoSlug:       item.RepoSlug,
		Title:          item.Title,
		Summary:        defaultString(strings.TrimSpace(item.Description), strings.TrimSpace(item.LaunchResult)),
		Description:    item.Description,
		Status:         status,
		RawStatus:      item.State,
		AttentionState: startUIPlannedItemAttentionState(item.State),
		Priority:       item.Priority,
		PriorityLabel:  startWorkPriorityLabel(item.Priority),
		ScheduleAt:     item.ScheduleAt,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		ExternalURL:    item.TargetURL,
		WorkType:       item.WorkType,
		RunID:          item.LaunchRunID,
		CanOpenJob:     strings.TrimSpace(item.LaunchRunID) != "",
	}
}

func startUITaskSummaryFromScoutJob(repoSlug string, item startWorkScoutJob) startUITaskSummary {
	status := startUITaskStatusQueued
	switch strings.ToLower(strings.TrimSpace(item.Status)) {
	case startScoutJobRunning:
		status = startUITaskStatusRunning
	case startScoutJobFailed:
		if strings.TrimSpace(item.PauseUntil) != "" {
			status = startUITaskStatusPaused
		} else {
			status = startUITaskStatusBlocked
		}
	case startScoutJobDismissed:
		status = startUITaskStatusDismissed
	case startScoutJobCompleted:
		status = startUITaskStatusCompleted
	}
	title := defaultString(strings.TrimSpace(item.Title), startUITaskTitleFromDescription(item.TaskBody))
	title = defaultString(title, defaultString(strings.TrimSpace(item.Role), "Scout job"))
	return startUITaskSummary{
		ID:             "scout-job:" + repoSlug + ":" + item.ID,
		Kind:           "scout_job",
		RepoSlug:       repoSlug,
		Title:          title,
		Summary:        defaultString(strings.TrimSpace(item.Summary), strings.TrimSpace(item.LastError)),
		Description:    item.TaskBody,
		Status:         status,
		RawStatus:      item.Status,
		AttentionState: startUITaskAttentionStateForStatus(status),
		Priority:       taskPriorityFromSeverity(item.Severity),
		PriorityLabel:  startWorkPriorityLabel(taskPriorityFromSeverity(item.Severity)),
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		WorkType:       item.WorkType,
		RunID:          item.RunID,
		CanOpenJob:     strings.TrimSpace(item.RunID) != "",
	}
}

func startUITaskSummaryFromInvestigation(item startUIInvestigationSummary) startUITaskSummary {
	status := startUITaskStatusQueued
	switch strings.ToLower(strings.TrimSpace(item.Status)) {
	case investigateRunStatusRunning:
		status = startUITaskStatusRunning
	case investigateRunStatusCompleted:
		status = startUITaskStatusCompleted
	default:
		if strings.TrimSpace(item.PauseUntil) != "" || strings.Contains(strings.ToLower(item.Status), "pause") {
			status = startUITaskStatusPaused
		} else if strings.TrimSpace(item.LastError) != "" || strings.Contains(strings.ToLower(item.Status), "fail") {
			status = startUITaskStatusBlocked
		}
	}
	return startUITaskSummary{
		ID:             "investigation:" + item.RunID,
		Kind:           "investigation",
		RepoSlug:       item.RepoSlug,
		Title:          defaultString(startUITaskTitleFromDescription(item.Query), "Investigation"),
		Summary:        defaultString(item.OverallShortExplanation, item.Query),
		Description:    item.Query,
		Status:         status,
		RawStatus:      item.Status,
		AttentionState: item.AttentionState,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		RunID:          item.RunID,
	}
}

func startUITaskSummaryFromInvestigationManifest(manifest investigateManifest) (startUITaskSummary, error) {
	sourcePathIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return startUITaskSummary{}, err
	}
	item := startUIInvestigationSummary{
		RunID:           manifest.RunID,
		RepoSlug:        startUIRepoSlugForRoot(manifest.WorkspaceRoot, sourcePathIndex),
		WorkspaceRoot:   manifest.WorkspaceRoot,
		Query:           manifest.Query,
		Status:          manifest.Status,
		CreatedAt:       manifest.CreatedAt,
		UpdatedAt:       manifest.UpdatedAt,
		CompletedAt:     manifest.CompletedAt,
		AcceptedRound:   manifest.AcceptedRound,
		FinalReportPath: manifest.FinalReportPath,
		LastError:       manifest.LastError,
		PauseReason:     manifest.PauseReason,
		PauseUntil:      manifest.PauseUntil,
		AttentionState:  startUIInvestigationAttentionState(manifest.Status, manifest.LastError),
	}
	return startUITaskSummaryFromInvestigation(item), nil
}

func startUITaskSummaryFromWorkRun(item startUIWorkRun) startUITaskSummary {
	status := startUITaskStatusQueued
	switch strings.ToLower(strings.TrimSpace(item.AttentionState)) {
	case "active":
		if strings.TrimSpace(item.PauseUntil) != "" {
			status = startUITaskStatusPaused
		} else {
			status = startUITaskStatusRunning
		}
	case "failed":
		if strings.TrimSpace(item.PauseUntil) != "" {
			status = startUITaskStatusPaused
		} else {
			status = startUITaskStatusFailed
		}
	case "blocked":
		if strings.TrimSpace(item.PauseUntil) != "" {
			status = startUITaskStatusPaused
		} else {
			status = startUITaskStatusBlocked
		}
	case "completed":
		status = startUITaskStatusCompleted
	case "queued":
		status = startUITaskStatusQueued
	}
	title := defaultString(item.RepoLabel, item.RunID)
	if strings.TrimSpace(item.TargetURL) != "" {
		title = fmt.Sprintf("%s -> %s", title, item.TargetURL)
	}
	return startUITaskSummary{
		ID:             "work-run:" + item.RunID,
		Kind:           "work_run",
		RepoSlug:       item.RepoSlug,
		Title:          title,
		Summary:        defaultString(strings.TrimSpace(item.CurrentPhase), strings.TrimSpace(item.Status)),
		Status:         status,
		RawStatus:      item.Status,
		AttentionState: item.AttentionState,
		UpdatedAt:      item.UpdatedAt,
		ExternalURL:    item.TargetURL,
		WorkType:       item.WorkType,
		RunID:          item.RunID,
		CanOpenJob:     true,
	}
}

func startUITaskSummaryFromWorkItem(item startUIWorkItem) startUITaskSummary {
	status := startUITaskStatusQueued
	switch strings.ToLower(strings.TrimSpace(item.Status)) {
	case workItemStatusRunning:
		status = startUITaskStatusRunning
	case workItemStatusDraftReady:
		status = startUITaskStatusInReview
	case workItemStatusPaused:
		status = startUITaskStatusPaused
	case workItemStatusDropped, workItemStatusSilenced:
		status = startUITaskStatusDismissed
	case workItemStatusSubmitted:
		status = startUITaskStatusCompleted
	case workItemStatusFailed, workItemStatusNeedsRouting:
		status = startUITaskStatusBlocked
	}
	return startUITaskSummary{
		ID:             "work-item:" + item.ID,
		Kind:           "work_item",
		RepoSlug:       item.RepoSlug,
		Title:          item.Subject,
		Summary:        defaultString(item.DraftSummary, item.SourceKind),
		Status:         status,
		RawStatus:      item.Status,
		AttentionState: item.AttentionState,
		UpdatedAt:      item.UpdatedAt,
		ExternalURL:    item.TargetURL,
		WorkType:       item.WorkType,
		RunID:          item.LinkedRunID,
		CanOpenJob:     strings.TrimSpace(item.LinkedRunID) != "",
	}
}

func startUITaskSummaryFromWorkItemState(item workItem) startUITaskSummary {
	status := startUITaskStatusQueued
	switch strings.ToLower(strings.TrimSpace(item.Status)) {
	case workItemStatusRunning:
		status = startUITaskStatusRunning
	case workItemStatusDraftReady:
		status = startUITaskStatusInReview
	case workItemStatusPaused:
		status = startUITaskStatusPaused
	case workItemStatusDropped, workItemStatusSilenced:
		status = startUITaskStatusDismissed
	case workItemStatusSubmitted:
		status = startUITaskStatusCompleted
	case workItemStatusFailed, workItemStatusNeedsRouting:
		status = startUITaskStatusBlocked
	}
	return startUITaskSummary{
		ID:             "work-item:" + item.ID,
		Kind:           "work_item",
		RepoSlug:       item.RepoSlug,
		Title:          item.Subject,
		Summary:        defaultString(safeDraftSummary(item.LatestDraft), item.SourceKind),
		Description:    item.Body,
		Status:         status,
		RawStatus:      item.Status,
		AttentionState: startUIWorkItemAttentionState(item),
		Priority:       defaultInt(item.Priority, 3),
		PriorityLabel:  startWorkPriorityLabel(defaultInt(item.Priority, 3)),
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		ExternalURL:    item.TargetURL,
		WorkType:       item.WorkType,
		RunID:          item.LinkedRunID,
		CanOpenJob:     strings.TrimSpace(item.LinkedRunID) != "",
	}
}

func startUITaskSummaryFromServiceTask(repoSlug string, state *startWorkState, task startWorkServiceTask) startUITaskSummary {
	status := startUITaskStatusQueued
	switch strings.ToLower(strings.TrimSpace(task.Status)) {
	case startWorkServiceTaskRunning:
		status = startUITaskStatusRunning
	case startWorkServiceTaskFailed:
		status = startUITaskStatusBlocked
	case startWorkServiceTaskCompleted:
		status = startUITaskStatusCompleted
	}
	priority := startUIServiceTaskPriorityValue(state, task)
	title := startUITaskTitleForServiceTask(repoSlug, task)
	summary := defaultString(strings.TrimSpace(task.LastError), strings.TrimSpace(task.ResultSummary))
	runID := strings.TrimSpace(task.RunID)
	if runID == "" && task.PlannedItemID != "" {
		if item, ok := state.PlannedItems[task.PlannedItemID]; ok {
			runID = strings.TrimSpace(item.LaunchRunID)
		}
	}
	return startUITaskSummary{
		ID:             "service-task:" + repoSlug + ":" + task.ID,
		Kind:           "service_task",
		RepoSlug:       repoSlug,
		Title:          title,
		Summary:        summary,
		Status:         status,
		RawStatus:      task.Status,
		AttentionState: startUITaskAttentionStateForStatus(status),
		Priority:       priority,
		PriorityLabel:  startWorkPriorityLabel(priority),
		ScheduleAt:     task.WaitUntil,
		CreatedAt:      task.StartedAt,
		UpdatedAt:      task.UpdatedAt,
		RunID:          runID,
		CanOpenJob:     strings.TrimSpace(runID) != "",
	}
}

func startUIServiceTaskPriorityValue(state *startWorkState, task startWorkServiceTask) int {
	if state == nil {
		return 3
	}
	if task.PlannedItemID != "" {
		if item, ok := state.PlannedItems[task.PlannedItemID]; ok {
			return clampTaskPriority(item.Priority)
		}
	}
	if task.IssueKey != "" {
		if issue, ok := state.Issues[task.IssueKey]; ok {
			return clampTaskPriority(startWorkEffectivePriority(issue))
		}
	}
	return 3
}

func startUITaskTitleForServiceTask(repoSlug string, task startWorkServiceTask) string {
	switch task.Kind {
	case startTaskKindIssueSync:
		return defaultString(repoSlug, "workspace") + " issue sync"
	case startTaskKindTriage:
		return defaultString(repoSlug, "workspace") + " triage"
	case startTaskKindReconcile:
		return defaultString(repoSlug, "workspace") + " reconcile"
	case startTaskKindPlannedLaunch:
		return defaultString(repoSlug, "workspace") + " scheduled launch"
	case startTaskKindScout:
		if task.ScoutRole != "" {
			return defaultString(repoSlug, "workspace") + " " + task.ScoutRole
		}
		return defaultString(repoSlug, "workspace") + " scout"
	default:
		return defaultString(repoSlug, "workspace") + " " + defaultString(task.Kind, "task")
	}
}

func startUITaskAttentionStateForStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case startUITaskStatusRunning:
		return "active"
	case startUITaskStatusFailed:
		return "failed"
	case startUITaskStatusPaused, startUITaskStatusInReview, startUITaskStatusBlocked:
		return "blocked"
	case startUITaskStatusDismissed, startUITaskStatusCompleted:
		return "completed"
	default:
		return "queued"
	}
}

func parseStartUITaskIssueID(taskID string) (string, int, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(taskID, "issue:"))
	parts := strings.Split(rest, "#")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid issue task id %q", taskID)
	}
	number, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", 0, fmt.Errorf("invalid issue task id %q", taskID)
	}
	return strings.TrimSpace(parts[0]), number, nil
}

func parseStartUITaskScopedID(taskID string, prefix string) (string, string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(taskID, prefix))
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid task id %q", taskID)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func clampTaskPriority(priority int) int {
	if priority < 0 || priority > 5 {
		return 3
	}
	return priority
}

func taskPriorityOrDefault(priority int) int {
	if priority < 0 || priority > 5 {
		return 3
	}
	return priority
}

func taskPriorityFromSeverity(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 3
	}
}

func trimAndFilterStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func startUITaskTitleFromDescription(description string) string {
	trimmed := strings.TrimSpace(description)
	if trimmed == "" {
		return "Untitled task"
	}
	line := strings.Split(trimmed, "\n")[0]
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = strings.TrimSpace(line[:80])
	}
	line = strings.TrimRight(line, ".,;: ")
	if line == "" {
		return "Untitled task"
	}
	return line
}

func startUITaskTemplateName(description string) string {
	title := startUITaskTitleFromDescription(description)
	fields := strings.Fields(title)
	if len(fields) > 4 {
		fields = fields[:4]
	}
	return strings.Join(fields, " ")
}

func startUITaskScoutRoleFromDescription(lower string) string {
	switch {
	case strings.Contains(lower, "ui"), strings.Contains(lower, "ux"), strings.Contains(lower, "layout"), strings.Contains(lower, "css"), strings.Contains(lower, "screen"):
		return uiScoutRole
	case strings.Contains(lower, "backend"), strings.Contains(lower, "api "), strings.Contains(lower, "api-"), strings.Contains(lower, "hot path"), strings.Contains(lower, "hotpath"), strings.Contains(lower, "hotspot"), strings.Contains(lower, "cpu"), strings.Contains(lower, "throughput"), strings.Contains(lower, "latency"), strings.Contains(lower, "performance"), strings.Contains(lower, "peformance"), strings.Contains(lower, "worker"), strings.Contains(lower, "queue"):
		return backendPerformanceScoutRole
	case strings.Contains(lower, "enhanc"):
		return enhancementScoutRole
	default:
		return improvementScoutRole
	}
}

func startUITaskFocusFromDescription(description string) []string {
	parts := strings.FieldsFunc(description, func(r rune) bool {
		switch r {
		case '\n', ',', ';':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 80 {
			trimmed = strings.TrimSpace(trimmed[:80])
		}
		out = append(out, trimmed)
	}
	return uniqueStrings(out)
}
