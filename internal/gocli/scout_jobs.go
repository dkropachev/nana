package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

func ensureStartWorkStateUnlocked(repoSlug string) (*startWorkState, error) {
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err == nil {
		return state, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	now := ISOTimeNow()
	return &startWorkState{
		Version:        startWorkStateVersion,
		SourceRepo:     repoSlug,
		CreatedAt:      now,
		UpdatedAt:      now,
		Issues:         map[string]startWorkIssueState{},
		ServiceTasks:   map[string]startWorkServiceTask{},
		Promotions:     map[string]startWorkPromotion{},
		PromotionSkips: map[string]startWorkPromotionSkip{},
		PlannedItems:   map[string]startWorkPlannedItem{},
		ScoutJobs:      map[string]startWorkScoutJob{},
	}, nil
}

func findManagedRepoSlugForSourcePath(repoPath string) string {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return ""
	}
	absoluteRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		absoluteRepoPath = repoPath
	}
	repos, err := listOnboardedGithubRepos()
	if err != nil {
		return ""
	}
	for _, repoSlug := range repos {
		sourcePath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
		if sourcePath == "" {
			continue
		}
		absoluteSourcePath, sourceErr := filepath.Abs(sourcePath)
		if sourceErr != nil {
			absoluteSourcePath = sourcePath
		}
		if filepath.Clean(absoluteSourcePath) == filepath.Clean(absoluteRepoPath) {
			return repoSlug
		}
	}
	return ""
}

func startWorkScoutJobTaskBody(item localScoutDiscoveredItem) string {
	return formatLocalScoutWorkTask(item)
}

func startWorkScoutJobIsResolved(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case startScoutJobCompleted, startScoutJobDismissed:
		return true
	default:
		return false
	}
}

func startWorkScoutJobFromDiscovered(item localScoutDiscoveredItem, createdAt string) startWorkScoutJob {
	if strings.TrimSpace(createdAt) == "" {
		createdAt = defaultString(strings.TrimSpace(item.GeneratedAt), ISOTimeNow())
	}
	return startWorkScoutJob{
		ID:                item.ID,
		Role:              item.Role,
		Title:             item.Title,
		Area:              defaultString(strings.TrimSpace(item.Proposal.Area), scoutIssueHeading(item.Role)),
		Summary:           strings.TrimSpace(item.Proposal.Summary),
		Rationale:         strings.TrimSpace(item.Proposal.Rationale),
		Evidence:          strings.TrimSpace(item.Proposal.Evidence),
		Impact:            strings.TrimSpace(item.Proposal.Impact),
		SuggestedNextStep: strings.TrimSpace(item.Proposal.SuggestedNextStep),
		Confidence:        strings.TrimSpace(item.Proposal.Confidence),
		Files:             append([]string{}, item.Proposal.Files...),
		Labels:            append([]string{}, item.Proposal.Labels...),
		Page:              strings.TrimSpace(item.Proposal.Page),
		Route:             strings.TrimSpace(item.Proposal.Route),
		Severity:          strings.TrimSpace(item.Proposal.Severity),
		TargetKind:        strings.TrimSpace(item.Proposal.TargetKind),
		Screenshots:       append([]string{}, item.Proposal.Screenshots...),
		ArtifactPath:      strings.TrimSpace(item.Artifact),
		ProposalPath:      strings.TrimSpace(item.ProposalPath),
		PolicyPath:        strings.TrimSpace(item.PolicyPath),
		PreflightPath:     strings.TrimSpace(item.PreflightPath),
		IssueDraftPath:    strings.TrimSpace(item.IssueDraftPath),
		RawOutputPath:     strings.TrimSpace(item.RawOutputPath),
		GeneratedAt:       strings.TrimSpace(item.GeneratedAt),
		AuditMode:         strings.TrimSpace(item.AuditMode),
		SurfaceKind:       strings.TrimSpace(item.SurfaceKind),
		SurfaceTarget:     strings.TrimSpace(item.SurfaceTarget),
		BrowserReady:      item.BrowserReady,
		PreflightReason:   strings.TrimSpace(item.PreflightReason),
		Destination:       defaultString(strings.TrimSpace(item.Destination), improvementDestinationLocal),
		ForkRepo:          strings.TrimSpace(item.ForkRepo),
		TaskBody:          startWorkScoutJobTaskBody(item),
		Status:            startScoutJobQueued,
		UpdatedAt:         createdAt,
		CreatedAt:         createdAt,
	}
}

func startWorkScoutJobFromItem(item startWorkScoutJob) startUIScoutItem {
	return startUIScoutItem{
		ID:                item.ID,
		Role:              item.Role,
		Title:             item.Title,
		Area:              item.Area,
		Summary:           item.Summary,
		Rationale:         item.Rationale,
		Evidence:          item.Evidence,
		Impact:            item.Impact,
		SuggestedNextStep: item.SuggestedNextStep,
		Confidence:        item.Confidence,
		Files:             append([]string{}, item.Files...),
		Labels:            append([]string{}, item.Labels...),
		Page:              item.Page,
		Route:             item.Route,
		Severity:          item.Severity,
		TargetKind:        item.TargetKind,
		Screenshots:       append([]string{}, item.Screenshots...),
		ArtifactPath:      item.ArtifactPath,
		ProposalPath:      item.ProposalPath,
		PolicyPath:        item.PolicyPath,
		PreflightPath:     item.PreflightPath,
		IssueDraftPath:    item.IssueDraftPath,
		RawOutputPath:     item.RawOutputPath,
		GeneratedAt:       item.GeneratedAt,
		AuditMode:         item.AuditMode,
		SurfaceKind:       item.SurfaceKind,
		SurfaceTarget:     item.SurfaceTarget,
		BrowserReady:      item.BrowserReady,
		PreflightReason:   item.PreflightReason,
		Destination:       item.Destination,
		ForkRepo:          item.ForkRepo,
		Status:            item.Status,
		RunID:             item.RunID,
		PlannedItemID:     item.LegacyPlannedItemID,
		Error:             item.LastError,
		UpdatedAt:         item.UpdatedAt,
	}
}

func startUIScoutItemFromDiscovered(item localScoutDiscoveredItem) startUIScoutItem {
	status := "pending"
	if strings.TrimSpace(item.Destination) != improvementDestinationLocal {
		status = defaultString(strings.TrimSpace(item.Destination), "external")
	}
	return startUIScoutItem{
		ID:                item.ID,
		Role:              item.Role,
		Title:             item.Title,
		Area:              defaultString(strings.TrimSpace(item.Proposal.Area), scoutIssueHeading(item.Role)),
		Summary:           strings.TrimSpace(item.Proposal.Summary),
		Rationale:         strings.TrimSpace(item.Proposal.Rationale),
		Evidence:          strings.TrimSpace(item.Proposal.Evidence),
		Impact:            strings.TrimSpace(item.Proposal.Impact),
		SuggestedNextStep: strings.TrimSpace(item.Proposal.SuggestedNextStep),
		Confidence:        strings.TrimSpace(item.Proposal.Confidence),
		Files:             append([]string{}, item.Proposal.Files...),
		Labels:            append([]string{}, item.Proposal.Labels...),
		Page:              strings.TrimSpace(item.Proposal.Page),
		Route:             strings.TrimSpace(item.Proposal.Route),
		Severity:          strings.TrimSpace(item.Proposal.Severity),
		TargetKind:        strings.TrimSpace(item.Proposal.TargetKind),
		Screenshots:       append([]string{}, item.Proposal.Screenshots...),
		ArtifactPath:      item.Artifact,
		ProposalPath:      item.ProposalPath,
		PolicyPath:        item.PolicyPath,
		PreflightPath:     item.PreflightPath,
		IssueDraftPath:    item.IssueDraftPath,
		RawOutputPath:     item.RawOutputPath,
		GeneratedAt:       item.GeneratedAt,
		AuditMode:         item.AuditMode,
		SurfaceKind:       item.SurfaceKind,
		SurfaceTarget:     item.SurfaceTarget,
		BrowserReady:      item.BrowserReady,
		PreflightReason:   item.PreflightReason,
		Destination:       item.Destination,
		ForkRepo:          item.ForkRepo,
		Status:            status,
	}
}

func startWorkPlannedItemLooksScoutDerived(item startWorkPlannedItem) bool {
	title := strings.TrimSpace(item.Title)
	description := strings.TrimSpace(item.Description)
	return strings.HasPrefix(title, "Implement scout proposal: ") &&
		strings.Contains(description, "Source artifact: ") &&
		strings.Contains(description, "Scout role: ")
}

func findScoutJobByLegacyPlannedItemID(state *startWorkState, plannedItemID string) (string, startWorkScoutJob, bool) {
	if state == nil {
		return "", startWorkScoutJob{}, false
	}
	plannedItemID = strings.TrimSpace(plannedItemID)
	if plannedItemID == "" {
		return "", startWorkScoutJob{}, false
	}
	for jobID, job := range state.ScoutJobs {
		if strings.TrimSpace(job.LegacyPlannedItemID) == plannedItemID {
			return jobID, job, true
		}
	}
	return "", startWorkScoutJob{}, false
}

func localScoutPickupStatusToScoutJobStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return startScoutJobCompleted
	case "dismissed":
		return startScoutJobDismissed
	case "failed":
		return startScoutJobFailed
	case "running", "in_progress":
		return startScoutJobRunning
	default:
		return startScoutJobQueued
	}
}

func findLegacyScoutPlannedItem(state *startWorkState, existing startWorkScoutJob, record localScoutPickupItem, recordOK bool, item localScoutDiscoveredItem) (startWorkPlannedItem, bool) {
	if state == nil {
		return startWorkPlannedItem{}, false
	}
	for _, plannedItemID := range []string{
		strings.TrimSpace(record.PlannedItemID),
		strings.TrimSpace(existing.LegacyPlannedItemID),
	} {
		if plannedItemID == "" {
			continue
		}
		if planned, ok := state.PlannedItems[plannedItemID]; ok {
			return planned, true
		}
	}
	expectedTitle := startUIScoutPlannedItemTitle(startUIScoutItem{Title: item.Title})
	expectedArtifact := "Source artifact: " + strings.TrimSpace(item.Artifact)
	for _, planned := range state.PlannedItems {
		if !startWorkPlannedItemLooksScoutDerived(planned) {
			continue
		}
		if strings.TrimSpace(planned.Title) != expectedTitle {
			continue
		}
		if strings.Contains(strings.TrimSpace(planned.Description), expectedArtifact) {
			return planned, true
		}
	}
	return startWorkPlannedItem{}, false
}

func deriveScoutJobLegacyState(job *startWorkScoutJob, existing startWorkScoutJob, hasExisting bool, record localScoutPickupItem, recordOK bool, planned startWorkPlannedItem, plannedOK bool) {
	if hasExisting {
		job.Status = defaultString(strings.TrimSpace(existing.Status), job.Status)
		job.RunID = strings.TrimSpace(existing.RunID)
		job.Attempts = existing.Attempts
		job.LastError = strings.TrimSpace(existing.LastError)
		job.UpdatedAt = defaultString(strings.TrimSpace(existing.UpdatedAt), job.UpdatedAt)
		job.CreatedAt = defaultString(strings.TrimSpace(existing.CreatedAt), job.CreatedAt)
		job.LegacyPlannedItemID = defaultString(strings.TrimSpace(existing.LegacyPlannedItemID), job.LegacyPlannedItemID)
	}
	if plannedOK {
		job.LegacyPlannedItemID = planned.ID
		switch strings.TrimSpace(planned.State) {
		case startPlannedItemLaunching:
			job.Status = startScoutJobRunning
			job.LastError = ""
		case startPlannedItemQueued:
			if !hasExisting || !startWorkScoutJobIsResolved(existing.Status) {
				job.Status = startScoutJobQueued
				job.LastError = ""
				job.RunID = ""
			}
		case startPlannedItemLaunched:
			fallthrough
		case "done":
			job.Status = startScoutJobCompleted
			job.LastError = ""
		case startPlannedItemFailed:
			job.Status = startScoutJobFailed
			job.LastError = strings.TrimSpace(planned.LastError)
		}
		if strings.TrimSpace(planned.LaunchRunID) != "" {
			job.RunID = strings.TrimSpace(planned.LaunchRunID)
		}
		job.UpdatedAt = defaultString(strings.TrimSpace(planned.UpdatedAt), job.UpdatedAt)
		return
	}
	if !recordOK {
		if !hasExisting {
			job.Status = startScoutJobQueued
		}
		return
	}
	mappedStatus := localScoutPickupStatusToScoutJobStatus(record.Status)
	switch mappedStatus {
	case startScoutJobCompleted, startScoutJobDismissed, startScoutJobFailed:
		job.Status = mappedStatus
	case startScoutJobRunning:
		if !hasExisting || existing.Status == "" || existing.Status == startScoutJobQueued || existing.Status == startScoutJobRunning {
			job.Status = startScoutJobRunning
		}
	default:
		if !hasExisting || !startWorkScoutJobIsResolved(existing.Status) {
			job.Status = startScoutJobQueued
		}
	}
	if strings.TrimSpace(record.RunID) != "" {
		job.RunID = strings.TrimSpace(record.RunID)
	}
	if mappedStatus == startScoutJobFailed {
		job.LastError = strings.TrimSpace(record.Error)
	} else if mappedStatus != startScoutJobFailed && job.Status != startScoutJobFailed {
		job.LastError = ""
	}
	job.UpdatedAt = defaultString(strings.TrimSpace(record.UpdatedAt), job.UpdatedAt)
}

func syncStartWorkScoutJobsIntoState(repoPath string, state *startWorkState) (bool, error) {
	if strings.TrimSpace(repoPath) == "" || state == nil || strings.TrimSpace(state.SourceRepo) == "" {
		return false, nil
	}
	discoveredItems, err := listLocalScoutDiscoveredItems(repoPath)
	if err != nil {
		return false, err
	}
	pickupState := localScoutPickupState{Version: 1, Items: map[string]localScoutPickupItem{}}
	if stateValue, _, readErr := readLocalScoutPickupState(repoPath); readErr == nil {
		pickupState = stateValue
	}
	if pickupState.Items == nil {
		pickupState.Items = map[string]localScoutPickupItem{}
	}
	if state.ScoutJobs == nil {
		state.ScoutJobs = map[string]startWorkScoutJob{}
	}
	now := ISOTimeNow()
	updated := false
	for _, item := range discoveredItems {
		if strings.TrimSpace(item.Destination) != improvementDestinationLocal {
			continue
		}
		existing, hasExisting := state.ScoutJobs[item.ID]
		job := startWorkScoutJobFromDiscovered(item, defaultString(strings.TrimSpace(existing.CreatedAt), now))
		record, recordOK := pickupState.Items[item.ID]
		planned, plannedOK := findLegacyScoutPlannedItem(state, existing, record, recordOK, item)
		deriveScoutJobLegacyState(&job, existing, hasExisting, record, recordOK, planned, plannedOK)
		if !hasExisting || !reflect.DeepEqual(existing, job) {
			state.ScoutJobs[item.ID] = job
			updated = true
		}
	}
	return updated, nil
}

func syncStartWorkScoutJobs(repoPath string, repoSlug string) (bool, *startWorkState, error) {
	if strings.TrimSpace(repoPath) == "" || strings.TrimSpace(repoSlug) == "" {
		return false, nil, nil
	}
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()

	state, err := ensureStartWorkStateUnlocked(repoSlug)
	if err != nil {
		return false, nil, err
	}
	updated, err := syncStartWorkScoutJobsIntoState(repoPath, state)
	if err != nil {
		return false, nil, err
	}
	if !updated {
		return false, state, nil
	}
	now := ISOTimeNow()
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return false, nil, err
	}
	return true, state, nil
}

func countOutstandingLocalScoutItems(repoPath string, repoSlug string, role string) (int, error) {
	if strings.TrimSpace(repoSlug) == "" {
		repoSlug = findManagedRepoSlugForSourcePath(repoPath)
	}
	if strings.TrimSpace(repoSlug) == "" {
		return countOutstandingLegacyLocalScoutItems(repoPath, role)
	}
	_, state, err := syncStartWorkScoutJobs(repoPath, repoSlug)
	if err != nil {
		return 0, err
	}
	if state == nil {
		return 0, nil
	}
	outstanding := 0
	for _, job := range state.ScoutJobs {
		if job.Role != role {
			continue
		}
		if !startWorkScoutJobIsResolved(job.Status) {
			outstanding++
		}
	}
	return outstanding, nil
}

func countOutstandingLegacyLocalScoutItems(repoPath string, role string) (int, error) {
	items, err := listLocalScoutDiscoveredItems(repoPath)
	if err != nil {
		return 0, err
	}
	pickupState := localScoutPickupState{Version: 1, Items: map[string]localScoutPickupItem{}}
	if statePath, statePathErr := localScoutPickupStatePath(repoPath); statePathErr == nil {
		if err := readGithubJSON(statePath, &pickupState); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		if pickupState.Items == nil {
			pickupState.Items = map[string]localScoutPickupItem{}
		}
	}
	byID := map[string]localScoutDiscoveredItem{}
	for _, item := range items {
		if item.Role != role {
			continue
		}
		byID[item.ID] = item
	}
	ids := make([]string, 0, len(byID))
	for itemID := range byID {
		ids = append(ids, itemID)
	}
	sort.Strings(ids)
	outstanding := 0
	for _, itemID := range ids {
		record, ok := pickupState.Items[itemID]
		if !ok || !localScoutPickupStatusIsResolved(record.Status) {
			outstanding++
		}
	}
	return outstanding, nil
}

func mutateStartWorkScoutJob(repoSlug string, jobID string, action string) (*startWorkState, startWorkScoutJob, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()

	state, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkScoutJob{}, err
	}
	job, ok := state.ScoutJobs[jobID]
	if !ok {
		return nil, startWorkScoutJob{}, fmt.Errorf("scout job %s was not found", jobID)
	}
	now := ISOTimeNow()
	switch action {
	case "dismiss":
		switch job.Status {
		case startScoutJobQueued, startScoutJobFailed:
			job.Status = startScoutJobDismissed
			job.LastError = ""
			job.UpdatedAt = now
		default:
			return nil, startWorkScoutJob{}, fmt.Errorf("scout job %s cannot be dismissed from status %s", jobID, job.Status)
		}
	case "retry", "reset":
		switch job.Status {
		case startScoutJobFailed, startScoutJobDismissed:
			job.Status = startScoutJobQueued
			job.RunID = ""
			job.LastError = ""
			job.UpdatedAt = now
		default:
			return nil, startWorkScoutJob{}, fmt.Errorf("scout job %s cannot be retried from status %s", jobID, job.Status)
		}
	default:
		return nil, startWorkScoutJob{}, fmt.Errorf("unsupported scout job action %q", action)
	}
	state.ScoutJobs[jobID] = job
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkScoutJob{}, err
	}
	return state, job, nil
}

func runStartWorkScoutJob(repoSlug string, job startWorkScoutJob, codexArgs []string) (startWorkLaunchResult, error) {
	repoPath, err := resolveStartPlannedRepoPath(repoSlug)
	if err != nil {
		return startWorkLaunchResult{}, err
	}
	args := []string{"start", "--repo", repoPath, "--task", job.TaskBody}
	if len(codexArgs) > 0 {
		args = append(args, "--")
		args = append(args, codexArgs...)
	}
	runID, err := runLocalWorkCommandWithRunID(repoPath, args)
	if err != nil {
		return startWorkLaunchResult{RunID: runID}, err
	}
	if strings.TrimSpace(runID) == "" {
		return startWorkLaunchResult{}, fmt.Errorf("scout job %s completed without a run id", job.ID)
	}
	return startWorkLaunchResult{RunID: runID}, nil
}
