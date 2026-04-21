package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
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
		WorkType:          inferScoutWorkType(item.Role, item.Proposal).WorkType,
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
		ID:                 item.ID,
		Role:               item.Role,
		Title:              item.Title,
		WorkType:           item.WorkType,
		Area:               item.Area,
		Summary:            item.Summary,
		Rationale:          item.Rationale,
		Evidence:           item.Evidence,
		Impact:             item.Impact,
		SuggestedNextStep:  item.SuggestedNextStep,
		Confidence:         item.Confidence,
		Files:              append([]string{}, item.Files...),
		Labels:             append([]string{}, item.Labels...),
		Page:               item.Page,
		Route:              item.Route,
		Severity:           item.Severity,
		TargetKind:         item.TargetKind,
		Screenshots:        append([]string{}, item.Screenshots...),
		ArtifactPath:       item.ArtifactPath,
		ProposalPath:       item.ProposalPath,
		PolicyPath:         item.PolicyPath,
		PreflightPath:      item.PreflightPath,
		IssueDraftPath:     item.IssueDraftPath,
		RawOutputPath:      item.RawOutputPath,
		GeneratedAt:        item.GeneratedAt,
		AuditMode:          item.AuditMode,
		SurfaceKind:        item.SurfaceKind,
		SurfaceTarget:      item.SurfaceTarget,
		BrowserReady:       item.BrowserReady,
		PreflightReason:    item.PreflightReason,
		Destination:        item.Destination,
		ForkRepo:           item.ForkRepo,
		Status:             item.Status,
		RunID:              item.RunID,
		PlannedItemID:      item.LegacyPlannedItemID,
		Error:              item.LastError,
		PauseReason:        item.PauseReason,
		PauseUntil:         item.PauseUntil,
		RecoveryCount:      item.RecoveryCount,
		LastRecoveryReason: item.LastRecoveryReason,
		LastRecoveryAt:     item.LastRecoveryAt,
		LastRecoveredRunID: item.LastRecoveredRunID,
		UpdatedAt:          item.UpdatedAt,
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
		WorkType:          inferScoutWorkType(item.Role, item.Proposal).WorkType,
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

func boundRunningScoutJobRun(existing startWorkScoutJob, hasExisting bool, record localScoutPickupItem, recordOK bool) (string, string, bool) {
	if hasExisting && strings.TrimSpace(existing.Status) == startScoutJobRunning {
		if runID := strings.TrimSpace(existing.RunID); runID != "" {
			return runID, strings.TrimSpace(existing.LastError), true
		}
	}
	if recordOK && localScoutPickupStatusToScoutJobStatus(record.Status) == startScoutJobRunning {
		if runID := strings.TrimSpace(record.RunID); runID != "" {
			return runID, "", true
		}
	}
	return "", "", false
}

func findLocalScoutPickupItemByPlannedItemID(pickupState localScoutPickupState, plannedItemID string) (localScoutPickupItem, bool) {
	plannedItemID = strings.TrimSpace(plannedItemID)
	if plannedItemID == "" {
		return localScoutPickupItem{}, false
	}
	for _, record := range pickupState.Items {
		if strings.TrimSpace(record.PlannedItemID) == plannedItemID {
			return record, true
		}
	}
	return localScoutPickupItem{}, false
}

func findScoutJobByPickupPlannedItemID(state *startWorkState, pickupState localScoutPickupState, plannedItemID string) (string, startWorkScoutJob, bool) {
	record, ok := findLocalScoutPickupItemByPlannedItemID(pickupState, plannedItemID)
	if !ok {
		return "", startWorkScoutJob{}, false
	}
	proposalID := strings.TrimSpace(record.ProposalID)
	if proposalID == "" || state == nil {
		return "", startWorkScoutJob{}, false
	}
	job, ok := state.ScoutJobs[proposalID]
	if !ok {
		return "", startWorkScoutJob{}, false
	}
	return proposalID, job, true
}

func parseLegacyScoutPlannedItemDescription(description string) (string, string, scoutFinding) {
	lines := strings.Split(strings.TrimSpace(description), "\n")
	section := ""
	sections := map[string]string{}
	appendSection := func(name string, line string) {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(line) == "" {
			return
		}
		if existing := strings.TrimSpace(sections[name]); existing != "" {
			sections[name] = existing + "\n" + strings.TrimSpace(line)
			return
		}
		sections[name] = strings.TrimSpace(line)
	}
	artifactPath := ""
	role := ""
	finding := scoutFinding{}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "Source artifact:"):
			artifactPath = strings.TrimSpace(strings.TrimPrefix(line, "Source artifact:"))
			section = ""
		case strings.HasPrefix(line, "Scout role:"):
			role = strings.TrimSpace(strings.TrimPrefix(line, "Scout role:"))
			section = ""
		case strings.HasPrefix(line, "Area:"):
			finding.Area = strings.TrimSpace(strings.TrimPrefix(line, "Area:"))
			section = ""
		case strings.HasPrefix(line, "Work type:"):
			finding.WorkType = strings.TrimSpace(strings.TrimPrefix(line, "Work type:"))
			section = ""
		case line == "Summary:":
			section = "summary"
		case line == "Rationale:":
			section = "rationale"
		case line == "Evidence:":
			section = "evidence"
		case line == "Impact:":
			section = "impact"
		case line == "Files:":
			section = "files"
		case line == "Suggested next step:":
			section = "suggested_next_step"
		default:
			appendSection(section, line)
		}
	}
	finding.Summary = strings.TrimSpace(sections["summary"])
	finding.Rationale = strings.TrimSpace(sections["rationale"])
	finding.Evidence = strings.TrimSpace(sections["evidence"])
	finding.Impact = strings.TrimSpace(sections["impact"])
	finding.SuggestedNextStep = strings.TrimSpace(sections["suggested_next_step"])
	if files := strings.TrimSpace(sections["files"]); files != "" {
		parts := strings.Split(files, ",")
		for _, part := range parts {
			if value := strings.TrimSpace(part); value != "" {
				finding.Files = append(finding.Files, value)
			}
		}
	}
	return artifactPath, role, finding
}

func startWorkScoutJobFromLegacyPlannedItem(item startWorkPlannedItem, proposalID string) startWorkScoutJob {
	title := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(item.Title), "Implement scout proposal: "))
	artifactPath, role, finding := parseLegacyScoutPlannedItemDescription(item.Description)
	finding.Title = title
	role = defaultString(role, improvementScoutRole)
	proposalID = defaultString(strings.TrimSpace(proposalID), localScoutProposalID(role, finding))
	discovered := localScoutDiscoveredItem{
		ID:          proposalID,
		Role:        role,
		Title:       title,
		Artifact:    artifactPath,
		Proposal:    finding,
		Destination: improvementDestinationLocal,
	}
	createdAt := defaultString(strings.TrimSpace(item.CreatedAt), defaultString(strings.TrimSpace(item.UpdatedAt), ISOTimeNow()))
	job := startWorkScoutJobFromDiscovered(discovered, createdAt)
	job.LegacyPlannedItemID = item.ID
	job.UpdatedAt = defaultString(strings.TrimSpace(item.UpdatedAt), createdAt)
	switch strings.TrimSpace(item.State) {
	case startPlannedItemLaunching:
		if strings.TrimSpace(item.LaunchRunID) != "" {
			job.Status = startScoutJobRunning
			job.RunID = strings.TrimSpace(item.LaunchRunID)
		}
	case startPlannedItemLaunched, "done":
		job.Status = startScoutJobCompleted
		job.RunID = strings.TrimSpace(item.LaunchRunID)
	case startPlannedItemFailed:
		job.Status = startScoutJobFailed
		job.RunID = strings.TrimSpace(item.LaunchRunID)
		job.LastError = strings.TrimSpace(item.LastError)
	default:
		job.Status = startScoutJobQueued
	}
	return job
}

func logStartWorkScoutJobTransition(repoSlug string, previous startWorkScoutJob, next startWorkScoutJob, hadPrevious bool) {
	if !hadPrevious {
		return
	}
	if strings.TrimSpace(previous.Status) == strings.TrimSpace(next.Status) &&
		strings.TrimSpace(previous.RunID) == strings.TrimSpace(next.RunID) &&
		strings.TrimSpace(previous.LastError) == strings.TrimSpace(next.LastError) &&
		strings.TrimSpace(previous.PauseReason) == strings.TrimSpace(next.PauseReason) &&
		strings.TrimSpace(previous.PauseUntil) == strings.TrimSpace(next.PauseUntil) &&
		previous.RecoveryCount == next.RecoveryCount &&
		strings.TrimSpace(previous.LastRecoveryReason) == strings.TrimSpace(next.LastRecoveryReason) &&
		strings.TrimSpace(previous.LastRecoveryAt) == strings.TrimSpace(next.LastRecoveryAt) &&
		strings.TrimSpace(previous.LastRecoveredRunID) == strings.TrimSpace(next.LastRecoveredRunID) {
		return
	}
	if startWorkScoutJobInRecoveryCooldown(next) {
		fmt.Fprintf(
			os.Stdout,
			"[start] %s: scout job %s auto-requeued after stale startup cleanup for run %s; retry after %s.\n",
			repoSlug,
			next.ID,
			defaultString(strings.TrimSpace(next.LastRecoveredRunID), "-"),
			defaultString(strings.TrimSpace(next.PauseUntil), "-"),
		)
		return
	}
	switch strings.TrimSpace(next.Status) {
	case startScoutJobCompleted:
		fmt.Fprintf(os.Stdout, "[start] %s: scout job %s run %s completed.\n", repoSlug, next.ID, defaultString(strings.TrimSpace(next.RunID), "-"))
	case startScoutJobFailed:
		fmt.Fprintf(os.Stdout, "[start] %s: scout job %s run %s failed: %s.\n", repoSlug, next.ID, defaultString(strings.TrimSpace(next.RunID), "-"), defaultString(strings.TrimSpace(next.LastError), "-"))
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
		job.PauseReason = strings.TrimSpace(existing.PauseReason)
		job.PauseUntil = strings.TrimSpace(existing.PauseUntil)
		job.RecoveryCount = existing.RecoveryCount
		job.LastRecoveryReason = strings.TrimSpace(existing.LastRecoveryReason)
		job.LastRecoveryAt = strings.TrimSpace(existing.LastRecoveryAt)
		job.LastRecoveredRunID = strings.TrimSpace(existing.LastRecoveredRunID)
		job.UpdatedAt = defaultString(strings.TrimSpace(existing.UpdatedAt), job.UpdatedAt)
		job.CreatedAt = defaultString(strings.TrimSpace(existing.CreatedAt), job.CreatedAt)
		job.LegacyPlannedItemID = defaultString(strings.TrimSpace(existing.LegacyPlannedItemID), job.LegacyPlannedItemID)
	}
	boundRunID, boundRunLastError, hasBoundRun := boundRunningScoutJobRun(existing, hasExisting, record, recordOK)
	preserveExistingRun := hasExisting && strings.TrimSpace(existing.Status) == startScoutJobRunning && strings.TrimSpace(existing.RunID) != ""
	preserveExistingFailure := hasExisting && strings.TrimSpace(existing.Status) == startScoutJobFailed
	if plannedOK {
		job.LegacyPlannedItemID = planned.ID
		switch strings.TrimSpace(planned.State) {
		case startPlannedItemLaunching:
			if strings.TrimSpace(planned.LaunchRunID) != "" {
				job.Status = startScoutJobRunning
				job.LastError = ""
			} else if hasBoundRun {
				job.Status = startScoutJobRunning
				job.RunID = boundRunID
				job.LastError = boundRunLastError
			} else if !hasExisting || !startWorkScoutJobIsResolved(existing.Status) {
				job.Status = startScoutJobQueued
				job.LastError = ""
				job.RunID = ""
			}
		case startPlannedItemQueued:
			if hasBoundRun {
				job.Status = startScoutJobRunning
				job.RunID = boundRunID
				job.LastError = boundRunLastError
			} else if !hasExisting || !startWorkScoutJobIsResolved(existing.Status) {
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
		} else if preserveExistingFailure {
			job.Status = existing.Status
			job.RunID = existing.RunID
			job.LastError = existing.LastError
			job.UpdatedAt = defaultString(strings.TrimSpace(existing.UpdatedAt), job.UpdatedAt)
		}
		return
	}
	if preserveExistingFailure {
		job.Status = existing.Status
		job.RunID = existing.RunID
		job.LastError = existing.LastError
		job.UpdatedAt = defaultString(strings.TrimSpace(existing.UpdatedAt), job.UpdatedAt)
		return
	}
	if preserveExistingRun && strings.TrimSpace(record.RunID) == "" {
		job.Status = existing.Status
		job.RunID = existing.RunID
		job.LastError = existing.LastError
		job.UpdatedAt = defaultString(strings.TrimSpace(existing.UpdatedAt), job.UpdatedAt)
		return
	}
	mappedStatus := localScoutPickupStatusToScoutJobStatus(record.Status)
	switch mappedStatus {
	case startScoutJobCompleted, startScoutJobDismissed, startScoutJobFailed:
		job.Status = mappedStatus
	case startScoutJobRunning:
		if strings.TrimSpace(record.RunID) == "" {
			if hasExisting && strings.TrimSpace(existing.RunID) != "" && strings.TrimSpace(existing.Status) == startScoutJobRunning {
				job.Status = existing.Status
				job.RunID = existing.RunID
			} else if !hasExisting || !startWorkScoutJobIsResolved(existing.Status) {
				job.Status = startScoutJobQueued
				job.RunID = ""
			}
		} else if !hasExisting || existing.Status == "" || existing.Status == startScoutJobQueued || existing.Status == startScoutJobRunning {
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

func startWorkScoutJobShouldReconcileRunState(job *startWorkScoutJob) bool {
	if job == nil || strings.TrimSpace(job.RunID) == "" {
		return false
	}
	switch strings.TrimSpace(job.Status) {
	case startScoutJobRunning:
		return true
	case startScoutJobFailed:
		return localWorkIsStaleCleanupError(job.LastError)
	default:
		return false
	}
}

const (
	startWorkScoutJobStaleRetryPauseReason = "auto-retrying after stale startup cleanup"
	startWorkScoutJobStaleRetryCooldown    = 15 * time.Minute
)

func startWorkScoutJobClearRecovery(job *startWorkScoutJob) {
	if job == nil {
		return
	}
	job.RecoveryCount = 0
	job.LastRecoveryReason = ""
	job.LastRecoveryAt = ""
	job.LastRecoveredRunID = ""
}

func startWorkScoutJobHasRecoveryMetadata(job startWorkScoutJob) bool {
	return job.RecoveryCount > 0 ||
		strings.TrimSpace(job.LastRecoveryReason) != "" ||
		strings.TrimSpace(job.LastRecoveryAt) != "" ||
		strings.TrimSpace(job.LastRecoveredRunID) != ""
}

func startWorkScoutJobInRecoveryCooldown(job startWorkScoutJob) bool {
	return normalizeScoutDestination(job.Destination) == improvementDestinationLocal &&
		strings.TrimSpace(job.PauseReason) == startWorkScoutJobStaleRetryPauseReason &&
		strings.TrimSpace(job.PauseUntil) != "" &&
		strings.TrimSpace(job.LastRecoveryReason) == localWorkStaleCleanupError &&
		strings.TrimSpace(job.LastRecoveredRunID) != ""
}

func startWorkScoutJobNormalizeRecovery(job *startWorkScoutJob) bool {
	if job == nil {
		return false
	}
	updated := false
	if job.RecoveryCount < 0 {
		job.RecoveryCount = 0
		updated = true
	}
	if startWorkScoutJobIsResolved(job.Status) && startWorkScoutJobHasRecoveryMetadata(*job) {
		startWorkScoutJobClearRecovery(job)
		updated = true
	}
	if startWorkScoutJobIsResolved(job.Status) && strings.TrimSpace(job.PauseReason) == startWorkScoutJobStaleRetryPauseReason {
		job.PauseReason = ""
		job.PauseUntil = ""
		updated = true
	}
	return updated
}

func startWorkScoutJobRecordStaleRecovery(job *startWorkScoutJob, manifest localWorkManifest, now time.Time) {
	if job == nil {
		return
	}
	recoveryReason := defaultString(strings.TrimSpace(manifest.LastError), localWorkStaleCleanupError)
	job.Status = startScoutJobQueued
	job.RunID = ""
	job.LastError = recoveryReason
	job.PauseReason = startWorkScoutJobStaleRetryPauseReason
	job.PauseUntil = now.Add(startWorkScoutJobStaleRetryCooldown).Format(time.RFC3339Nano)
	job.RecoveryCount++
	job.LastRecoveryReason = recoveryReason
	job.LastRecoveryAt = now.Format(time.RFC3339Nano)
	job.LastRecoveredRunID = strings.TrimSpace(manifest.RunID)
	job.UpdatedAt = now.Format(time.RFC3339Nano)
}

func startWorkScoutJobNeedsApproval(job startWorkScoutJob) bool {
	if startWorkScoutJobInRecoveryCooldown(job) {
		return false
	}
	return strings.TrimSpace(job.Status) == startScoutJobFailed
}

func localWorkManifestEndedBeforeFirstIteration(manifest localWorkManifest) bool {
	return manifest.CurrentIteration <= 0 && len(manifest.Iterations) == 0
}

func startWorkScoutJobShouldAutoRetryStaleStartup(job *startWorkScoutJob, manifest localWorkManifest) bool {
	if job == nil {
		return false
	}
	if normalizeScoutDestination(job.Destination) != improvementDestinationLocal {
		return false
	}
	if job.RecoveryCount > 0 {
		return false
	}
	if job.Attempts > 1 {
		return false
	}
	if !localWorkIsStaleCleanupError(manifest.LastError) {
		return false
	}
	return localWorkManifestEndedBeforeFirstIteration(manifest)
}

func startWorkScoutJobCanResumeAfterStaleCleanup(job *startWorkScoutJob, manifest localWorkManifest) bool {
	if job == nil {
		return false
	}
	if normalizeScoutDestination(job.Destination) != improvementDestinationLocal {
		return false
	}
	if strings.TrimSpace(job.RunID) == "" || strings.TrimSpace(job.RunID) != strings.TrimSpace(manifest.RunID) {
		return false
	}
	if !localWorkIsStaleCleanupError(manifest.LastError) {
		return false
	}
	if strings.TrimSpace(manifest.Status) == "completed" {
		return false
	}
	if len(manifest.Iterations) >= manifest.MaxIterations {
		return false
	}
	return true
}

func startWorkScoutJobRecordRecoveredRun(job *startWorkScoutJob, manifest localWorkManifest, now time.Time) {
	if job == nil {
		return
	}
	recoveryReason := defaultString(strings.TrimSpace(manifest.LastError), localWorkStaleCleanupError)
	job.Status = startScoutJobRunning
	job.RunID = strings.TrimSpace(manifest.RunID)
	job.LastError = ""
	job.PauseReason = ""
	job.PauseUntil = ""
	job.RecoveryCount++
	job.LastRecoveryReason = recoveryReason
	job.LastRecoveryAt = now.Format(time.RFC3339Nano)
	job.LastRecoveredRunID = strings.TrimSpace(manifest.RunID)
	job.UpdatedAt = now.Format(time.RFC3339Nano)
}

func startWorkScoutJobRequeueAfterStaleCleanup(job *startWorkScoutJob, manifest localWorkManifest, now time.Time) {
	if job == nil {
		return
	}
	recoveryReason := defaultString(strings.TrimSpace(manifest.LastError), localWorkStaleCleanupError)
	job.Status = startScoutJobQueued
	job.RunID = ""
	job.LastError = recoveryReason
	job.PauseReason = ""
	job.PauseUntil = ""
	job.RecoveryCount++
	job.LastRecoveryReason = recoveryReason
	job.LastRecoveryAt = now.Format(time.RFC3339Nano)
	job.LastRecoveredRunID = strings.TrimSpace(manifest.RunID)
	job.UpdatedAt = now.Format(time.RFC3339Nano)
}

func resumeStartWorkScoutJobDetached(manifest localWorkManifest, codexArgs []string) error {
	original := manifest
	manifest.Status = "running"
	manifest.CompletedAt = ""
	manifest.LastError = ""
	manifest.UpdatedAt = ISOTimeNow()
	if err := writeLocalWorkManifest(manifest); err != nil {
		return err
	}
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	logPath := filepath.Join(runDir, "runtime.log")
	if err := localWorkStartDetachedRunner(manifest.RepoRoot, manifest.RunID, codexArgs, logPath); err != nil {
		if restoreErr := writeLocalWorkManifest(original); restoreErr != nil {
			return fmt.Errorf("%w (additionally failed to restore stale manifest: %v)", err, restoreErr)
		}
		return err
	}
	return nil
}

func recoverStartWorkScoutJobsFromStaleManifests(repoSlug string, state *startWorkState, manifests []localWorkManifest, codexArgs []string) (int, int, map[string]bool, bool, error) {
	if state == nil || len(manifests) == 0 || len(state.ScoutJobs) == 0 {
		return 0, 0, nil, false, nil
	}
	manifestByRunID := make(map[string]localWorkManifest, len(manifests))
	for _, manifest := range manifests {
		runID := strings.TrimSpace(manifest.RunID)
		if runID == "" {
			continue
		}
		manifestByRunID[runID] = manifest
	}
	if len(manifestByRunID) == 0 {
		return 0, 0, nil, false, nil
	}

	now := time.Now().UTC()
	resumed := 0
	requeued := 0
	handled := map[string]bool{}
	updated := false

	for jobID, job := range state.ScoutJobs {
		manifest, ok := manifestByRunID[strings.TrimSpace(job.RunID)]
		if !ok {
			continue
		}
		if startWorkScoutJobCanResumeAfterStaleCleanup(&job, manifest) {
			if err := resumeStartWorkScoutJobDetached(manifest, codexArgs); err == nil {
				startWorkScoutJobRecordRecoveredRun(&job, manifest, now)
				state.ScoutJobs[jobID] = job
				handled[manifest.RunID] = true
				resumed++
				updated = true
				fmt.Fprintf(os.Stdout, "[start] %s: scout job %s resumed stale local work run %s.\n", repoSlug, job.ID, manifest.RunID)
				continue
			}
		}
		startWorkScoutJobRequeueAfterStaleCleanup(&job, manifest, now)
		state.ScoutJobs[jobID] = job
		handled[manifest.RunID] = true
		requeued++
		updated = true
		fmt.Fprintf(os.Stdout, "[start] %s: scout job %s requeued after stale local work run %s cleanup.\n", repoSlug, job.ID, manifest.RunID)
	}
	return resumed, requeued, handled, updated, nil
}

func reconcileStartWorkScoutJobRunState(job *startWorkScoutJob) {
	if !startWorkScoutJobShouldReconcileRunState(job) {
		return
	}
	manifest, err := readLocalWorkManifestByRunID(job.RunID)
	if err != nil {
		if localWorkIsStaleCleanupError(job.LastError) && normalizeScoutDestination(job.Destination) == improvementDestinationLocal {
			startWorkScoutJobRequeueAfterStaleCleanup(job, localWorkManifest{
				RunID:     job.RunID,
				LastError: defaultString(strings.TrimSpace(job.LastError), localWorkStaleCleanupError),
			}, time.Now().UTC())
		}
		return
	}
	if startWorkScoutJobShouldAutoRetryStaleStartup(job, manifest) {
		startWorkScoutJobRecordStaleRecovery(job, manifest, time.Now().UTC())
		return
	}
	if localWorkIsStaleCleanupError(manifest.LastError) {
		startWorkScoutJobRequeueAfterStaleCleanup(job, manifest, time.Now().UTC())
		return
	}
	switch strings.TrimSpace(manifest.Status) {
	case "running":
		job.LastError = ""
		job.PauseUntil = ""
		job.PauseReason = ""
	case "paused":
		job.Status = startScoutJobRunning
		job.LastError = defaultString(strings.TrimSpace(manifest.LastError), "rate limited")
		job.PauseReason = defaultString(strings.TrimSpace(manifest.PauseReason), "rate limited")
		job.PauseUntil = strings.TrimSpace(manifest.PauseUntil)
	case "completed":
		job.Status = startScoutJobCompleted
		job.LastError = ""
		job.PauseUntil = ""
		job.PauseReason = ""
		startWorkScoutJobClearRecovery(job)
	case "failed", "blocked":
		job.Status = startScoutJobFailed
		job.LastError = defaultString(strings.TrimSpace(manifest.LastError), fmt.Sprintf("local work run %s ended with status %s", job.RunID, manifest.Status))
		job.PauseUntil = ""
		job.PauseReason = ""
	default:
		if strings.TrimSpace(manifest.CompletedAt) != "" {
			job.Status = startScoutJobFailed
			job.LastError = defaultString(strings.TrimSpace(manifest.LastError), fmt.Sprintf("local work run %s ended with status %s", job.RunID, manifest.Status))
			job.PauseUntil = ""
			job.PauseReason = ""
		}
	}
	job.UpdatedAt = defaultString(strings.TrimSpace(manifest.CompletedAt), defaultString(strings.TrimSpace(manifest.UpdatedAt), job.UpdatedAt))
}

func syncStartWorkScoutJobsIntoState(repoPath string, state *startWorkState) (bool, error) {
	if state == nil || strings.TrimSpace(state.SourceRepo) == "" {
		return false, nil
	}
	discoveredItems := []localScoutDiscoveredItem{}
	repoPathAvailable := false
	if strings.TrimSpace(repoPath) != "" {
		if info, statErr := os.Stat(repoPath); statErr == nil && info.IsDir() {
			repoPathAvailable = true
			items, err := listLocalScoutDiscoveredItemsWithReadLock(repoPath)
			if err != nil {
				return false, err
			}
			discoveredItems = items
		}
	}
	pickupState := localScoutPickupState{Version: 1, Items: map[string]localScoutPickupItem{}}
	if repoPathAvailable {
		if stateValue, _, readErr := readLocalScoutPickupStateWithReadLock(repoPath); readErr == nil {
			pickupState = stateValue
		} else if repoAccessLockBusy(readErr) {
			return false, readErr
		}
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
		reconcileStartWorkScoutJobRunState(&job)
		startWorkScoutJobNormalizeRecovery(&job)
		if !hasExisting || !reflect.DeepEqual(existing, job) {
			logStartWorkScoutJobTransition(state.SourceRepo, existing, job, hasExisting)
			state.ScoutJobs[item.ID] = job
			updated = true
		}
	}
	legacyPlannedItemIDs := []string{}
	for plannedItemID, planned := range state.PlannedItems {
		if !startWorkPlannedItemLooksScoutDerived(planned) {
			continue
		}
		legacyPlannedItemIDs = append(legacyPlannedItemIDs, plannedItemID)
		jobID, existing, hasExisting := findScoutJobByLegacyPlannedItemID(state, plannedItemID)
		if !hasExisting {
			jobID, existing, hasExisting = findScoutJobByPickupPlannedItemID(state, pickupState, plannedItemID)
		}
		record, recordOK := findLocalScoutPickupItemByPlannedItemID(pickupState, plannedItemID)
		proposalID := strings.TrimSpace(jobID)
		if proposalID == "" && recordOK {
			proposalID = strings.TrimSpace(record.ProposalID)
		}
		if !hasExisting {
			existing = startWorkScoutJobFromLegacyPlannedItem(planned, proposalID)
			hasExisting = false
		}
		job := existing
		if strings.TrimSpace(job.ID) == "" {
			job = startWorkScoutJobFromLegacyPlannedItem(planned, proposalID)
		}
		if strings.TrimSpace(job.LegacyPlannedItemID) == "" {
			job.LegacyPlannedItemID = planned.ID
		}
		if recordOK && strings.TrimSpace(record.RunID) != "" {
			job.RunID = strings.TrimSpace(record.RunID)
		}
		boundRunID, boundRunLastError, hasBoundRun := boundRunningScoutJobRun(existing, hasExisting, record, recordOK)
		switch strings.TrimSpace(planned.State) {
		case startPlannedItemLaunching:
			if strings.TrimSpace(job.RunID) != "" {
				job.Status = startScoutJobRunning
				job.LastError = ""
			} else if hasBoundRun {
				job.Status = startScoutJobRunning
				job.RunID = boundRunID
				job.LastError = boundRunLastError
			} else if !startWorkScoutJobIsResolved(job.Status) {
				job.Status = startScoutJobQueued
				job.LastError = ""
			}
		case startPlannedItemLaunched, "done":
			job.Status = startScoutJobCompleted
			job.LastError = ""
		case startPlannedItemFailed:
			job.Status = startScoutJobFailed
			job.LastError = defaultString(strings.TrimSpace(planned.LastError), strings.TrimSpace(job.LastError))
		default:
			if hasBoundRun {
				job.Status = startScoutJobRunning
				job.RunID = boundRunID
				job.LastError = boundRunLastError
			} else if !startWorkScoutJobIsResolved(job.Status) {
				job.Status = startScoutJobQueued
				job.LastError = ""
			}
		}
		if strings.TrimSpace(planned.LaunchRunID) != "" {
			job.RunID = strings.TrimSpace(planned.LaunchRunID)
		}
		reconcileStartWorkScoutJobRunState(&job)
		startWorkScoutJobNormalizeRecovery(&job)
		if !hasExisting || !reflect.DeepEqual(existing, job) {
			logStartWorkScoutJobTransition(state.SourceRepo, existing, job, hasExisting)
			state.ScoutJobs[job.ID] = job
			updated = true
		}
	}
	for _, plannedItemID := range legacyPlannedItemIDs {
		delete(state.PlannedItems, plannedItemID)
		updated = true
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

func normalizeStartWorkStateScoutJobs(state *startWorkState) (bool, error) {
	if state == nil || strings.TrimSpace(state.SourceRepo) == "" {
		return false, nil
	}
	repoPath := strings.TrimSpace(githubManagedPaths(state.SourceRepo).SourcePath)
	return syncStartWorkScoutJobsIntoState(repoPath, state)
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
	items, err := listLocalScoutDiscoveredItemsWithReadLock(repoPath)
	if err != nil {
		return 0, err
	}
	pickupState := localScoutPickupState{Version: 1, Items: map[string]localScoutPickupItem{}}
	if stateValue, _, err := readLocalScoutPickupStateWithReadLock(repoPath); err == nil {
		pickupState = stateValue
	} else if repoAccessLockBusy(err) {
		return 0, err
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
			job.PauseReason = ""
			job.PauseUntil = ""
			startWorkScoutJobClearRecovery(&job)
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
			job.PauseReason = ""
			job.PauseUntil = ""
			startWorkScoutJobClearRecovery(&job)
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
	if strings.TrimSpace(job.WorkType) == "" {
		return startWorkLaunchResult{}, fmt.Errorf("scout job %s is missing work_type", job.ID)
	}
	args := []string{"start", "--detach", "--repo", repoPath, "--task", job.TaskBody, "--work-type", job.WorkType}
	if len(codexArgs) > 0 {
		args = append(args, "--")
		args = append(args, codexArgs...)
	}
	runID, err := runLocalWorkCommandWithRunID(repoPath, args)
	if err != nil {
		return startWorkLaunchResult{RunID: runID}, err
	}
	if strings.TrimSpace(runID) == "" {
		return startWorkLaunchResult{}, fmt.Errorf("scout job %s started without a run id", job.ID)
	}
	return startWorkLaunchResult{RunID: runID}, nil
}
