package gocli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	startWorkFindingStatusOpen      = "open"
	startWorkFindingStatusPromoted  = "promoted"
	startWorkFindingStatusDismissed = "dismissed"
	startWorkFindingStatusDeleted   = "deleted"
	startWorkFindingStatusResolved  = "resolved"

	startWorkFindingImportParsePending = "pending"
	startWorkFindingImportParseParsed  = "parsed"
	startWorkFindingImportParseFailed  = "failed"

	startWorkFindingCandidateStatusCandidate = "candidate"
	startWorkFindingCandidateStatusDropped   = "dropped"
	startWorkFindingCandidateStatusPromoted  = "promoted"

	startWorkFindingsHandlingManualReview = "manual_review"
	startWorkFindingsHandlingAutoPromote  = "auto_promote"

	startWorkFindingSourceKindManualScout   = "manual_scout"
	startWorkFindingSourceKindInvestigation = "investigation"
	startWorkFindingSourceKindCoding        = "coding"
	startWorkFindingSourceKindManualImport  = "manual_import"

	startWorkFindingParentTaskKindManualScout   = "manual_scout"
	startWorkFindingParentTaskKindInvestigation = "investigation"
	startWorkFindingParentTaskKindCoding        = "coding"
)

type startWorkFinding struct {
	ID                string   `json:"id"`
	RepoSlug          string   `json:"repo_slug"`
	SourceKind        string   `json:"source_kind"`
	SourceID          string   `json:"source_id"`
	SourceItemID      string   `json:"source_item_id"`
	ParentTaskKind    string   `json:"parent_task_kind,omitempty"`
	ParentTaskID      string   `json:"parent_task_id,omitempty"`
	Title             string   `json:"title"`
	Summary           string   `json:"summary,omitempty"`
	Detail            string   `json:"detail,omitempty"`
	Evidence          string   `json:"evidence,omitempty"`
	Severity          string   `json:"severity,omitempty"`
	WorkType          string   `json:"work_type,omitempty"`
	Files             []string `json:"files,omitempty"`
	Path              string   `json:"path,omitempty"`
	Line              int      `json:"line,omitempty"`
	Route             string   `json:"route,omitempty"`
	Page              string   `json:"page,omitempty"`
	Status            string   `json:"status"`
	DeletedFromStatus string   `json:"deleted_from_status,omitempty"`
	DeletedAt         string   `json:"deleted_at,omitempty"`
	PromotedTaskID    string   `json:"promoted_task_id,omitempty"`
	DismissReason     string   `json:"dismiss_reason,omitempty"`
	ResolvedAt        string   `json:"resolved_at,omitempty"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

type startWorkFindingImportCandidate struct {
	CandidateID       string   `json:"candidate_id"`
	Title             string   `json:"title"`
	Summary           string   `json:"summary,omitempty"`
	Detail            string   `json:"detail,omitempty"`
	Evidence          string   `json:"evidence,omitempty"`
	Severity          string   `json:"severity,omitempty"`
	WorkType          string   `json:"work_type,omitempty"`
	Files             []string `json:"files,omitempty"`
	Path              string   `json:"path,omitempty"`
	Line              int      `json:"line,omitempty"`
	Route             string   `json:"route,omitempty"`
	Page              string   `json:"page,omitempty"`
	ParseNotes        string   `json:"parse_notes,omitempty"`
	Status            string   `json:"status,omitempty"`
	PromotedFindingID string   `json:"promoted_finding_id,omitempty"`
}

type startWorkFindingImportSession struct {
	ID                 string                            `json:"id"`
	RepoSlug           string                            `json:"repo_slug"`
	InputFilePath      string                            `json:"input_file_path,omitempty"`
	MarkdownSnapshot   string                            `json:"markdown_snapshot,omitempty"`
	ParseStatus        string                            `json:"parse_status,omitempty"`
	ParseError         string                            `json:"parse_error,omitempty"`
	ArtifactsDir       string                            `json:"artifacts_dir,omitempty"`
	SourceMarkdownPath string                            `json:"source_markdown_path,omitempty"`
	CandidatesPath     string                            `json:"candidates_path,omitempty"`
	PreviewPath        string                            `json:"preview_path,omitempty"`
	Candidates         []startWorkFindingImportCandidate `json:"candidates,omitempty"`
	CreatedAt          string                            `json:"created_at"`
	UpdatedAt          string                            `json:"updated_at"`
}

type startWorkFindingUpsertInput struct {
	RepoSlug       string
	SourceKind     string
	SourceID       string
	SourceItemID   string
	ParentTaskKind string
	ParentTaskID   string
	Title          string
	Summary        string
	Detail         string
	Evidence       string
	Severity       string
	WorkType       string
	Files          []string
	Path           string
	Line           int
	Route          string
	Page           string
	CreatedAt      string
	UpdatedAt      string
}

func normalizeFindingsHandling(value string, legacyScoutDestination string, launchKind string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case startWorkFindingsHandlingAutoPromote:
		return startWorkFindingsHandlingAutoPromote
	case startWorkFindingsHandlingManualReview:
		return startWorkFindingsHandlingManualReview
	}
	if strings.TrimSpace(legacyScoutDestination) != "" {
		switch normalizeScoutDestination(legacyScoutDestination) {
		case improvementDestinationLocal:
			return startWorkFindingsHandlingAutoPromote
		case improvementDestinationReview:
			return startWorkFindingsHandlingManualReview
		}
	}
	switch strings.TrimSpace(launchKind) {
	case "local_work", "investigation", "manual_scout":
		return startWorkFindingsHandlingManualReview
	default:
		return ""
	}
}

func normalizeStartWorkFindingStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case startWorkFindingStatusPromoted:
		return startWorkFindingStatusPromoted
	case startWorkFindingStatusDismissed:
		return startWorkFindingStatusDismissed
	case startWorkFindingStatusDeleted:
		return startWorkFindingStatusDeleted
	case startWorkFindingStatusResolved:
		return startWorkFindingStatusResolved
	default:
		return startWorkFindingStatusOpen
	}
}

func normalizeStartWorkFindingImportParseStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case startWorkFindingImportParseParsed:
		return startWorkFindingImportParseParsed
	case startWorkFindingImportParseFailed:
		return startWorkFindingImportParseFailed
	default:
		return startWorkFindingImportParsePending
	}
}

func normalizeStartWorkFindingImportCandidate(candidate startWorkFindingImportCandidate) startWorkFindingImportCandidate {
	candidate.CandidateID = strings.TrimSpace(candidate.CandidateID)
	candidate.Title = strings.TrimSpace(candidate.Title)
	candidate.Summary = strings.TrimSpace(candidate.Summary)
	candidate.Detail = strings.TrimSpace(candidate.Detail)
	candidate.Evidence = strings.TrimSpace(candidate.Evidence)
	candidate.Severity = normalizeGithubSeverity(candidate.Severity)
	candidate.WorkType = defaultString(normalizeWorkType(candidate.WorkType), workTypeFeature)
	candidate.Path = strings.TrimSpace(candidate.Path)
	candidate.Route = strings.TrimSpace(candidate.Route)
	candidate.Page = strings.TrimSpace(candidate.Page)
	candidate.ParseNotes = strings.TrimSpace(candidate.ParseNotes)
	candidate.Status = normalizeStartWorkFindingCandidateStatus(candidate.Status)
	candidate.PromotedFindingID = strings.TrimSpace(candidate.PromotedFindingID)
	candidate.Files = uniqueStrings(candidate.Files)
	return candidate
}

func normalizeStartWorkFindingCandidateStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case startWorkFindingCandidateStatusDropped:
		return startWorkFindingCandidateStatusDropped
	case startWorkFindingCandidateStatusPromoted:
		return startWorkFindingCandidateStatusPromoted
	default:
		return startWorkFindingCandidateStatusCandidate
	}
}

func startWorkFindingNeedsSync(item startWorkPlannedItem) bool {
	if normalizeFindingsHandling(item.FindingsHandling, item.ScoutDestination, item.LaunchKind) == "" {
		return false
	}
	switch strings.TrimSpace(item.LaunchKind) {
	case "manual_scout", "investigation", "local_work":
		return true
	default:
		return false
	}
}

func startWorkFindingParentTaskKind(item startWorkPlannedItem) string {
	switch strings.TrimSpace(item.LaunchKind) {
	case "manual_scout":
		return startWorkFindingParentTaskKindManualScout
	case "investigation":
		return startWorkFindingParentTaskKindInvestigation
	case "local_work":
		return startWorkFindingParentTaskKindCoding
	default:
		return strings.TrimSpace(item.LaunchKind)
	}
}

func ensureStartWorkFindingsState(state *startWorkState) {
	if state == nil {
		return
	}
	if state.Findings == nil {
		state.Findings = map[string]startWorkFinding{}
	}
	if state.ImportSessions == nil {
		state.ImportSessions = map[string]startWorkFindingImportSession{}
	}
}

func startWorkFindingIDsSorted(state *startWorkState) []string {
	ids := make([]string, 0, len(state.Findings))
	for id := range state.Findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func startWorkPlannedItemIDsSorted(state *startWorkState) []string {
	ids := make([]string, 0, len(state.PlannedItems))
	for id := range state.PlannedItems {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func findStartWorkFindingBySourceUnlocked(state *startWorkState, sourceKind string, sourceID string, sourceItemID string) (string, startWorkFinding, bool) {
	if state == nil {
		return "", startWorkFinding{}, false
	}
	sourceKind = strings.TrimSpace(sourceKind)
	sourceID = strings.TrimSpace(sourceID)
	sourceItemID = strings.TrimSpace(sourceItemID)
	for _, findingID := range startWorkFindingIDsSorted(state) {
		finding := state.Findings[findingID]
		if strings.TrimSpace(finding.SourceKind) == sourceKind &&
			strings.TrimSpace(finding.SourceID) == sourceID &&
			strings.TrimSpace(finding.SourceItemID) == sourceItemID {
			return findingID, finding, true
		}
	}
	return "", startWorkFinding{}, false
}

func upsertStartWorkFindingUnlocked(state *startWorkState, input startWorkFindingUpsertInput) (string, bool, startWorkFinding) {
	ensureStartWorkFindingsState(state)
	now := defaultString(strings.TrimSpace(input.UpdatedAt), ISOTimeNow())
	createdAt := defaultString(strings.TrimSpace(input.CreatedAt), now)
	input.Title = strings.TrimSpace(input.Title)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Detail = strings.TrimSpace(input.Detail)
	input.Evidence = strings.TrimSpace(input.Evidence)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.SourceItemID = strings.TrimSpace(input.SourceItemID)
	input.ParentTaskKind = strings.TrimSpace(input.ParentTaskKind)
	input.ParentTaskID = strings.TrimSpace(input.ParentTaskID)
	input.Path = strings.TrimSpace(input.Path)
	input.Route = strings.TrimSpace(input.Route)
	input.Page = strings.TrimSpace(input.Page)
	input.Files = uniqueStrings(input.Files)
	input.WorkType = defaultString(normalizeWorkType(input.WorkType), workTypeFeature)
	input.Severity = normalizeGithubSeverity(input.Severity)

	findingID, existing, ok := findStartWorkFindingBySourceUnlocked(state, input.SourceKind, input.SourceID, input.SourceItemID)
	if !ok {
		findingID = fmt.Sprintf("finding-%d", timeNowUnixNano())
		existing = startWorkFinding{
			ID:        findingID,
			RepoSlug:  strings.TrimSpace(input.RepoSlug),
			CreatedAt: createdAt,
			Status:    startWorkFindingStatusOpen,
		}
	}
	next := existing
	next.RepoSlug = defaultString(strings.TrimSpace(input.RepoSlug), next.RepoSlug)
	next.SourceKind = input.SourceKind
	next.SourceID = input.SourceID
	next.SourceItemID = input.SourceItemID
	next.ParentTaskKind = input.ParentTaskKind
	next.ParentTaskID = input.ParentTaskID
	next.Title = input.Title
	next.Summary = input.Summary
	next.Detail = input.Detail
	next.Evidence = input.Evidence
	next.Severity = input.Severity
	next.WorkType = input.WorkType
	next.Files = append([]string{}, input.Files...)
	next.Path = input.Path
	next.Line = input.Line
	next.Route = input.Route
	next.Page = input.Page
	next.UpdatedAt = now
	if strings.TrimSpace(next.CreatedAt) == "" {
		next.CreatedAt = createdAt
	}
	if next.Status == startWorkFindingStatusResolved {
		next.Status = startWorkFindingStatusOpen
		next.ResolvedAt = ""
	}
	if next.Status == startWorkFindingStatusDeleted {
		next.UpdatedAt = existing.UpdatedAt
	}
	if next.Status == "" {
		next.Status = startWorkFindingStatusOpen
	}
	changed := !ok || !startWorkFindingEqual(existing, next)
	if changed {
		state.Findings[findingID] = next
	}
	return findingID, changed, next
}

func startWorkFindingEqual(left startWorkFinding, right startWorkFinding) bool {
	return left.ID == right.ID &&
		left.RepoSlug == right.RepoSlug &&
		left.SourceKind == right.SourceKind &&
		left.SourceID == right.SourceID &&
		left.SourceItemID == right.SourceItemID &&
		left.ParentTaskKind == right.ParentTaskKind &&
		left.ParentTaskID == right.ParentTaskID &&
		left.Title == right.Title &&
		left.Summary == right.Summary &&
		left.Detail == right.Detail &&
		left.Evidence == right.Evidence &&
		left.Severity == right.Severity &&
		left.WorkType == right.WorkType &&
		left.Path == right.Path &&
		left.Line == right.Line &&
		left.Route == right.Route &&
		left.Page == right.Page &&
		left.Status == right.Status &&
		left.DeletedFromStatus == right.DeletedFromStatus &&
		left.DeletedAt == right.DeletedAt &&
		left.PromotedTaskID == right.PromotedTaskID &&
		left.DismissReason == right.DismissReason &&
		left.ResolvedAt == right.ResolvedAt &&
		left.CreatedAt == right.CreatedAt &&
		left.UpdatedAt == right.UpdatedAt &&
		slicesEqual(left.Files, right.Files)
}

func slicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func markMissingStartWorkFindingsResolvedUnlocked(state *startWorkState, sourceKind string, sourceID string, seen map[string]bool) bool {
	if state == nil || len(state.Findings) == 0 {
		return false
	}
	updated := false
	now := ISOTimeNow()
	for findingID, finding := range state.Findings {
		if strings.TrimSpace(finding.SourceKind) != strings.TrimSpace(sourceKind) ||
			strings.TrimSpace(finding.SourceID) != strings.TrimSpace(sourceID) {
			continue
		}
		if seen[strings.TrimSpace(finding.SourceItemID)] {
			continue
		}
		if finding.Status == startWorkFindingStatusDismissed || finding.Status == startWorkFindingStatusDeleted || finding.Status == startWorkFindingStatusResolved {
			continue
		}
		finding.Status = startWorkFindingStatusResolved
		finding.ResolvedAt = now
		finding.UpdatedAt = now
		state.Findings[findingID] = finding
		updated = true
	}
	return updated
}

func normalizeStartWorkStateFindings(state *startWorkState) (bool, error) {
	if state == nil || strings.TrimSpace(state.SourceRepo) == "" {
		return false, nil
	}
	ensureStartWorkFindingsState(state)
	repoPath := strings.TrimSpace(githubManagedPaths(state.SourceRepo).SourcePath)
	if repoPath == "" {
		return false, nil
	}
	if info, err := os.Stat(repoPath); err != nil || !info.IsDir() {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}
	updated := false
	for _, itemID := range startWorkPlannedItemIDsSorted(state) {
		item := state.PlannedItems[itemID]
		if !startWorkFindingNeedsSync(item) {
			continue
		}
		var itemUpdated bool
		var err error
		switch strings.TrimSpace(item.LaunchKind) {
		case "manual_scout":
			itemUpdated, err = syncStartWorkManualScoutFindings(repoPath, state, item)
		case "investigation":
			itemUpdated, err = syncStartWorkInvestigationFindings(repoPath, state, item)
		case "local_work":
			itemUpdated, err = syncStartWorkCodingFindings(state, item)
		}
		if err != nil {
			return false, err
		}
		updated = updated || itemUpdated
	}
	return updated, nil
}

func syncStartWorkManualScoutFindings(repoPath string, state *startWorkState, item startWorkPlannedItem) (bool, error) {
	runID := strings.TrimSpace(item.LaunchRunID)
	role := strings.TrimSpace(item.ScoutRole)
	if runID == "" || role == "" {
		return false, nil
	}
	artifactDir := filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), runID)
	reportPath := filepath.Join(artifactDir, "proposals.json")
	if _, err := os.Stat(reportPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return markMissingStartWorkFindingsResolvedUnlocked(state, startWorkFindingSourceKindManualScout, runID, map[string]bool{}), nil
		}
		return false, err
	}
	var report scoutReport
	if err := readGithubJSON(reportPath, &report); err != nil {
		return false, err
	}
	createdAt := defaultString(strings.TrimSpace(report.GeneratedAt), defaultString(strings.TrimSpace(item.UpdatedAt), ISOTimeNow()))
	updated := false
	seen := map[string]bool{}
	for index, proposal := range report.Proposals {
		title := strings.TrimSpace(proposal.Title)
		summary := strings.TrimSpace(proposal.Summary)
		if title == "" || summary == "" {
			continue
		}
		sourceItemID := startWorkScoutFindingSourceItemID(role, index, proposal)
		seen[sourceItemID] = true
		findingID, findingUpdated, _ := upsertStartWorkFindingUnlocked(state, startWorkFindingUpsertInput{
			RepoSlug:       state.SourceRepo,
			SourceKind:     startWorkFindingSourceKindManualScout,
			SourceID:       runID,
			SourceItemID:   sourceItemID,
			ParentTaskKind: startWorkFindingParentTaskKind(item),
			ParentTaskID:   item.ID,
			Title:          title,
			Summary:        summary,
			Detail:         buildScoutFindingDetail(proposal),
			Evidence:       strings.TrimSpace(proposal.Evidence),
			Severity:       proposal.Severity,
			WorkType:       inferScoutWorkType(role, proposal).WorkType,
			Files:          append([]string{}, proposal.Files...),
			Route:          strings.TrimSpace(proposal.Route),
			Page:           strings.TrimSpace(proposal.Page),
			CreatedAt:      createdAt,
			UpdatedAt:      createdAt,
		})
		updated = updated || findingUpdated
		promoted, err := autoPromoteStartWorkFindingIfNeededUnlocked(state, item, findingID)
		if err != nil {
			return false, err
		}
		updated = updated || promoted
	}
	return updated || markMissingStartWorkFindingsResolvedUnlocked(state, startWorkFindingSourceKindManualScout, runID, seen), nil
}

func startWorkScoutFindingSourceItemID(role string, index int, proposal scoutFinding) string {
	parts := []string{
		strings.TrimSpace(role),
		strings.TrimSpace(proposal.Title),
		strings.TrimSpace(proposal.Area),
		strings.TrimSpace(proposal.Route),
		strings.TrimSpace(proposal.Page),
		strings.Join(uniqueStrings(proposal.Files), ","),
	}
	stable := strings.Join(compactNonEmptyStrings(parts), "|")
	if stable == "" {
		return fmt.Sprintf("proposal-%d", index+1)
	}
	return sha256Hex(stable)
}

func syncStartWorkInvestigationFindings(repoPath string, state *startWorkState, item startWorkPlannedItem) (bool, error) {
	runID := strings.TrimSpace(item.LaunchRunID)
	if runID == "" {
		return false, nil
	}
	manifestPath := filepath.Join(repoPath, ".nana", "logs", "investigate", runID, "manifest.json")
	var manifest investigateManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return markMissingStartWorkFindingsResolvedUnlocked(state, startWorkFindingSourceKindInvestigation, runID, map[string]bool{}), nil
		}
		return false, err
	}
	reportPath := strings.TrimSpace(manifest.FinalReportPath)
	if reportPath == "" {
		reportPath = filepath.Join(filepath.Dir(manifestPath), "final-report.json")
	}
	var report investigateReport
	if err := readGithubJSON(reportPath, &report); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	createdAt := defaultString(strings.TrimSpace(manifest.CompletedAt), defaultString(strings.TrimSpace(manifest.UpdatedAt), ISOTimeNow()))
	updated := false
	seen := map[string]bool{}
	for index, issue := range report.Issues {
		sourceItemID := strings.TrimSpace(issue.ID)
		if sourceItemID == "" {
			sourceItemID = fmt.Sprintf("issue-%d", index+1)
		}
		if strings.TrimSpace(issue.ShortExplanation) == "" && strings.TrimSpace(issue.DetailedExplanation) == "" {
			continue
		}
		seen[sourceItemID] = true
		files, path, line := investigateProofFileContext(issue.Proofs)
		title := defaultString(strings.TrimSpace(issue.ShortExplanation), sourceItemID)
		findingID, findingUpdated, _ := upsertStartWorkFindingUnlocked(state, startWorkFindingUpsertInput{
			RepoSlug:       state.SourceRepo,
			SourceKind:     startWorkFindingSourceKindInvestigation,
			SourceID:       runID,
			SourceItemID:   sourceItemID,
			ParentTaskKind: startWorkFindingParentTaskKind(item),
			ParentTaskID:   item.ID,
			Title:          title,
			Summary:        strings.TrimSpace(issue.ShortExplanation),
			Detail:         strings.TrimSpace(issue.DetailedExplanation),
			Evidence:       summarizeInvestigateProofs(issue.Proofs),
			Severity:       inferFindingSeverity(title, issue.DetailedExplanation, summarizeInvestigateProofs(issue.Proofs)),
			WorkType:       inferFindingWorkType(title, issue.DetailedExplanation, files),
			Files:          files,
			Path:           path,
			Line:           line,
			CreatedAt:      createdAt,
			UpdatedAt:      createdAt,
		})
		updated = updated || findingUpdated
		promoted, err := autoPromoteStartWorkFindingIfNeededUnlocked(state, item, findingID)
		if err != nil {
			return false, err
		}
		updated = updated || promoted
	}
	return updated || markMissingStartWorkFindingsResolvedUnlocked(state, startWorkFindingSourceKindInvestigation, runID, seen), nil
}

func syncStartWorkCodingFindings(state *startWorkState, item startWorkPlannedItem) (bool, error) {
	runID := strings.TrimSpace(item.LaunchRunID)
	if runID == "" {
		return false, nil
	}
	manifest, err := readLocalWorkManifestByRunID(runID)
	if err != nil {
		return false, nil
	}
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	iteration, iterationDir := startWorkCodingFindingIteration(manifest, runDir)
	if iteration <= 0 || strings.TrimSpace(iterationDir) == "" {
		return markMissingStartWorkFindingsResolvedUnlocked(state, startWorkFindingSourceKindCoding, runID, map[string]bool{}), nil
	}
	updated := false
	seen := map[string]bool{}
	createdAt := defaultString(strings.TrimSpace(manifest.CompletedAt), defaultString(strings.TrimSpace(manifest.UpdatedAt), ISOTimeNow()))

	if reviewPath := localWorkLatestReviewFindingsArtifact(iterationDir); reviewPath != "" {
		findings := []githubPullReviewFinding{}
		if err := readGithubJSON(reviewPath, &findings); err == nil {
			for _, reviewFinding := range findings {
				fingerprint := strings.TrimSpace(reviewFinding.Fingerprint)
				if fingerprint == "" {
					fingerprint = buildGithubPullReviewFindingFingerprint(reviewFinding.Title, reviewFinding.Path, reviewFinding.Line, reviewFinding.Summary)
				}
				if fingerprint == "" {
					continue
				}
				seen["review:"+fingerprint] = true
				findingID, findingUpdated, _ := upsertStartWorkFindingUnlocked(state, startWorkFindingUpsertInput{
					RepoSlug:       state.SourceRepo,
					SourceKind:     startWorkFindingSourceKindCoding,
					SourceID:       runID,
					SourceItemID:   "review:" + fingerprint,
					ParentTaskKind: startWorkFindingParentTaskKind(item),
					ParentTaskID:   item.ID,
					Title:          strings.TrimSpace(reviewFinding.Title),
					Summary:        strings.TrimSpace(reviewFinding.Summary),
					Detail:         strings.TrimSpace(reviewFinding.Detail),
					Evidence:       defaultString(strings.TrimSpace(reviewFinding.Rationale), strings.TrimSpace(reviewFinding.Fix)),
					Severity:       reviewFinding.Severity,
					WorkType:       inferFindingWorkType(reviewFinding.Title, reviewFinding.Detail, []string{reviewFinding.Path}),
					Files:          compactNonEmptyStrings([]string{reviewFinding.Path}),
					Path:           strings.TrimSpace(reviewFinding.Path),
					Line:           reviewFinding.Line,
					CreatedAt:      createdAt,
					UpdatedAt:      createdAt,
				})
				updated = updated || findingUpdated
				promoted, promoteErr := autoPromoteStartWorkFindingIfNeededUnlocked(state, item, findingID)
				if promoteErr != nil {
					return false, promoteErr
				}
				updated = updated || promoted
			}
		}
	}

	for _, reportPath := range localWorkVerificationArtifactPaths(iterationDir) {
		report := localWorkVerificationReport{}
		if err := readGithubJSON(reportPath, &report); err != nil {
			continue
		}
		if report.Passed {
			continue
		}
		if len(report.FailedStages) == 0 {
			report.FailedStages = []string{"verification"}
		}
		for _, failedStage := range report.FailedStages {
			stage := strings.TrimSpace(failedStage)
			if stage == "" {
				continue
			}
			sourceItemID := "verification:" + stage
			seen[sourceItemID] = true
			detail, evidence := summarizeVerificationFailure(report, stage)
			findingID, findingUpdated, _ := upsertStartWorkFindingUnlocked(state, startWorkFindingUpsertInput{
				RepoSlug:       state.SourceRepo,
				SourceKind:     startWorkFindingSourceKindCoding,
				SourceID:       runID,
				SourceItemID:   sourceItemID,
				ParentTaskKind: startWorkFindingParentTaskKind(item),
				ParentTaskID:   item.ID,
				Title:          fmt.Sprintf("Verification failed: %s", stage),
				Summary:        defaultString(strings.TrimSpace(detail), fmt.Sprintf("Verification failed in stage %s.", stage)),
				Detail:         detail,
				Evidence:       evidence,
				Severity:       "high",
				WorkType:       item.WorkType,
				CreatedAt:      createdAt,
				UpdatedAt:      createdAt,
			})
			updated = updated || findingUpdated
			promoted, promoteErr := autoPromoteStartWorkFindingIfNeededUnlocked(state, item, findingID)
			if promoteErr != nil {
				return false, promoteErr
			}
			updated = updated || promoted
		}
	}

	return updated || markMissingStartWorkFindingsResolvedUnlocked(state, startWorkFindingSourceKindCoding, runID, seen), nil
}

func autoPromoteStartWorkFindingIfNeededUnlocked(state *startWorkState, item startWorkPlannedItem, findingID string) (bool, error) {
	if normalizeFindingsHandling(item.FindingsHandling, item.ScoutDestination, item.LaunchKind) != startWorkFindingsHandlingAutoPromote {
		return false, nil
	}
	finding := state.Findings[findingID]
	if strings.TrimSpace(finding.PromotedTaskID) != "" || finding.Status == startWorkFindingStatusDismissed || finding.Status == startWorkFindingStatusDeleted {
		return false, nil
	}
	_, _, err := promoteStartWorkFindingUnlocked(state, findingID)
	if err != nil {
		return false, err
	}
	return true, nil
}

func promoteStartWorkFindingUnlocked(state *startWorkState, findingID string) (startWorkFinding, startWorkPlannedItem, error) {
	ensureStartWorkFindingsState(state)
	finding, ok := state.Findings[findingID]
	if !ok {
		return startWorkFinding{}, startWorkPlannedItem{}, fmt.Errorf("finding %s was not found", findingID)
	}
	if finding.Status == startWorkFindingStatusDeleted {
		return startWorkFinding{}, startWorkPlannedItem{}, fmt.Errorf("finding %s is deleted", findingID)
	}
	if strings.TrimSpace(finding.PromotedTaskID) != "" {
		if item, ok := state.PlannedItems[finding.PromotedTaskID]; ok {
			return finding, item, nil
		}
	}
	now := ISOTimeNow()
	itemID := fmt.Sprintf("planned-%d", timeNowUnixNano())
	item := startWorkPlannedItem{
		ID:               itemID,
		RepoSlug:         state.SourceRepo,
		Title:            startWorkFindingPlannedItemTitle(finding),
		Description:      startWorkFindingPlannedItemDescription(finding),
		WorkType:         defaultString(normalizeWorkType(finding.WorkType), workTypeFeature),
		LaunchKind:       "local_work",
		FindingsHandling: startWorkFindingsHandlingManualReview,
		Priority:         startWorkFindingPriority(finding.Severity),
		State:            startPlannedItemQueued,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if state.PlannedItems == nil {
		state.PlannedItems = map[string]startWorkPlannedItem{}
	}
	state.PlannedItems[item.ID] = item
	finding.PromotedTaskID = item.ID
	finding.Status = startWorkFindingStatusPromoted
	finding.DeletedFromStatus = ""
	finding.DeletedAt = ""
	finding.UpdatedAt = now
	finding.ResolvedAt = ""
	state.Findings[findingID] = finding
	return finding, item, nil
}

func dismissStartWorkFindingUnlocked(state *startWorkState, findingID string, reason string) (startWorkFinding, error) {
	ensureStartWorkFindingsState(state)
	finding, ok := state.Findings[findingID]
	if !ok {
		return startWorkFinding{}, fmt.Errorf("finding %s was not found", findingID)
	}
	if finding.Status == startWorkFindingStatusDeleted {
		return startWorkFinding{}, fmt.Errorf("finding %s is deleted", findingID)
	}
	finding.Status = startWorkFindingStatusDismissed
	finding.DeletedFromStatus = ""
	finding.DeletedAt = ""
	finding.DismissReason = strings.TrimSpace(reason)
	finding.UpdatedAt = ISOTimeNow()
	state.Findings[findingID] = finding
	return finding, nil
}

func restoreStartWorkFindingStatus(finding startWorkFinding) string {
	switch normalizeStartWorkFindingStatus(finding.DeletedFromStatus) {
	case startWorkFindingStatusOpen:
		return startWorkFindingStatusOpen
	case startWorkFindingStatusPromoted:
		return startWorkFindingStatusPromoted
	case startWorkFindingStatusDismissed:
		return startWorkFindingStatusDismissed
	default:
		return startWorkFindingStatusDismissed
	}
}

func restoreStartWorkFindingUnlocked(state *startWorkState, findingID string) (startWorkFinding, error) {
	ensureStartWorkFindingsState(state)
	finding, ok := state.Findings[findingID]
	if !ok {
		return startWorkFinding{}, fmt.Errorf("finding %s was not found", findingID)
	}
	if finding.Status != startWorkFindingStatusDeleted {
		return startWorkFinding{}, fmt.Errorf("finding %s is not deleted", findingID)
	}
	finding.Status = restoreStartWorkFindingStatus(finding)
	finding.DeletedFromStatus = ""
	finding.DeletedAt = ""
	finding.UpdatedAt = ISOTimeNow()
	state.Findings[findingID] = finding
	return finding, nil
}

func startWorkFindingPlannedItemTitle(finding startWorkFinding) string {
	return "Address finding: " + defaultString(strings.TrimSpace(finding.Title), finding.ID)
}

func startWorkFindingPlannedItemDescription(finding startWorkFinding) string {
	lines := []string{
		fmt.Sprintf("Finding: %s", defaultString(strings.TrimSpace(finding.Title), finding.ID)),
		fmt.Sprintf("Source: %s / %s / %s", defaultString(strings.TrimSpace(finding.SourceKind), "(unknown)"), defaultString(strings.TrimSpace(finding.SourceID), "(unknown)"), defaultString(strings.TrimSpace(finding.SourceItemID), "(unknown)")),
	}
	if strings.TrimSpace(finding.ParentTaskKind) != "" || strings.TrimSpace(finding.ParentTaskID) != "" {
		lines = append(lines, fmt.Sprintf("Parent task: %s / %s", defaultString(strings.TrimSpace(finding.ParentTaskKind), "(none)"), defaultString(strings.TrimSpace(finding.ParentTaskID), "(none)")))
	}
	if strings.TrimSpace(finding.Summary) != "" {
		lines = append(lines, "", "Summary:", strings.TrimSpace(finding.Summary))
	}
	if strings.TrimSpace(finding.Detail) != "" {
		lines = append(lines, "", "Detail:", strings.TrimSpace(finding.Detail))
	}
	if strings.TrimSpace(finding.Evidence) != "" {
		lines = append(lines, "", "Evidence:", strings.TrimSpace(finding.Evidence))
	}
	if len(finding.Files) > 0 {
		lines = append(lines, "", "Files:", strings.Join(finding.Files, ", "))
	}
	if strings.TrimSpace(finding.Path) != "" {
		location := strings.TrimSpace(finding.Path)
		if finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, finding.Line)
		}
		lines = append(lines, "", "Location:", location)
	}
	return strings.Join(lines, "\n")
}

func startWorkFindingPriority(severity string) int {
	switch normalizeGithubSeverity(severity) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	default:
		return 3
	}
}

func buildScoutFindingDetail(proposal scoutFinding) string {
	parts := []string{strings.TrimSpace(proposal.Summary)}
	for _, value := range []string{proposal.Rationale, proposal.Impact, proposal.SuggestedNextStep} {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(compactNonEmptyStrings(parts), "\n\n")
}

func investigateProofFileContext(proofs []investigateProof) ([]string, string, int) {
	files := []string{}
	path := ""
	line := 0
	for _, proof := range proofs {
		if trimmed := strings.TrimSpace(proof.Path); trimmed != "" {
			files = append(files, trimmed)
			if path == "" {
				path = trimmed
				line = proof.Line
			}
		}
	}
	return uniqueStrings(files), path, line
}

func summarizeInvestigateProofs(proofs []investigateProof) string {
	lines := []string{}
	for _, proof := range proofs {
		title := defaultString(strings.TrimSpace(proof.Title), strings.TrimSpace(proof.Kind))
		why := strings.TrimSpace(proof.WhyItProves)
		location := defaultString(strings.TrimSpace(proof.Path), strings.TrimSpace(proof.Link))
		line := strings.TrimSpace(location)
		if title == "" && why == "" && line == "" {
			continue
		}
		summary := title
		if why != "" {
			summary = defaultString(summary, why)
			if why != summary {
				summary += ": " + why
			}
		}
		if line != "" {
			if summary != "" {
				summary += " (" + line + ")"
			} else {
				summary = line
			}
		}
		lines = append(lines, summary)
	}
	return strings.Join(compactNonEmptyStrings(lines), "\n")
}

func inferFindingWorkType(title string, detail string, files []string) string {
	text := strings.ToLower(strings.Join(append([]string{title, detail}, files...), " "))
	switch {
	case strings.Contains(text, "test"), strings.Contains(text, "coverage"), strings.Contains(text, "assert"), strings.Contains(text, "_test.go"):
		return workTypeTestOnly
	case strings.Contains(text, "bug"), strings.Contains(text, "regression"), strings.Contains(text, "fail"), strings.Contains(text, "broken"), strings.Contains(text, "error"):
		return workTypeBugFix
	default:
		return workTypeFeature
	}
}

func inferFindingSeverity(title string, detail string, evidence string) string {
	text := strings.ToLower(strings.Join([]string{title, detail, evidence}, " "))
	switch {
	case strings.Contains(text, "security"), strings.Contains(text, "data loss"), strings.Contains(text, "corrupt"), strings.Contains(text, "panic"), strings.Contains(text, "crash"):
		return "high"
	case strings.Contains(text, "regression"), strings.Contains(text, "broken"), strings.Contains(text, "failure"):
		return "medium"
	default:
		return "medium"
	}
}

func compactNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func localWorkLatestReviewFindingsArtifact(iterationDir string) string {
	preferred := filepath.Join(iterationDir, "review-initial-findings.json")
	rounds, _ := filepath.Glob(filepath.Join(iterationDir, "review-round-*-findings.json"))
	if len(rounds) == 0 {
		if fileExists(preferred) {
			return preferred
		}
		return ""
	}
	sort.SliceStable(rounds, func(i, j int) bool {
		return roundArtifactNumber(rounds[i], "review-round-", "-findings.json") < roundArtifactNumber(rounds[j], "review-round-", "-findings.json")
	})
	return rounds[len(rounds)-1]
}

func roundArtifactNumber(path string, prefix string, suffix string) int {
	name := filepath.Base(path)
	name = strings.TrimPrefix(name, prefix)
	name = strings.TrimSuffix(name, suffix)
	value, err := strconv.Atoi(name)
	if err != nil {
		return -1
	}
	return value
}

func summarizeVerificationFailure(report localWorkVerificationReport, stage string) (string, string) {
	stage = strings.TrimSpace(stage)
	detail := defaultString(strings.TrimSpace(summarizeLocalVerification(report)), fmt.Sprintf("Verification failed in stage %s.", stage))
	evidence := []string{}
	for _, result := range report.Stages {
		if strings.TrimSpace(result.Name) != stage {
			continue
		}
		for _, command := range result.Commands {
			snippet := strings.TrimSpace(command.Output)
			if snippet != "" {
				evidence = append(evidence, fmt.Sprintf("%s (exit %d): %s", strings.TrimSpace(command.Command), command.ExitCode, snippet))
			}
		}
	}
	return detail, strings.Join(compactNonEmptyStrings(evidence), "\n")
}

func startWorkCodingFindingIteration(manifest localWorkManifest, runDir string) (int, string) {
	if len(manifest.Iterations) > 0 {
		last := manifest.Iterations[len(manifest.Iterations)-1]
		return last.Iteration, localWorkIterationDir(runDir, last.Iteration)
	}
	if manifest.CurrentIteration > 0 {
		return manifest.CurrentIteration, localWorkIterationDir(runDir, manifest.CurrentIteration)
	}
	return 0, ""
}

func timeNowUnixNano() int64 {
	return time.Now().UnixNano()
}

func startWorkFindingsRoot(repoSlug string) string {
	return filepath.Join(githubManagedPaths(repoSlug).RepoRoot, "findings")
}

func startWorkFindingImportSessionDir(repoSlug string, sessionID string) string {
	return filepath.Join(startWorkFindingsRoot(repoSlug), "imports", sessionID)
}

func startWorkFindingImportPreviewMarkdown(session startWorkFindingImportSession) string {
	lines := []string{
		fmt.Sprintf("# Findings Import Preview: %s", session.ID),
		"",
		fmt.Sprintf("- Repo: %s", defaultString(strings.TrimSpace(session.RepoSlug), "(unknown)")),
		fmt.Sprintf("- Source file: %s", defaultString(strings.TrimSpace(session.InputFilePath), "(inline markdown)")),
		fmt.Sprintf("- Parse status: %s", defaultString(strings.TrimSpace(session.ParseStatus), startWorkFindingImportParsePending)),
	}
	if strings.TrimSpace(session.ParseError) != "" {
		lines = append(lines, fmt.Sprintf("- Parse error: %s", strings.TrimSpace(session.ParseError)))
	}
	lines = append(lines, "", "## Candidates")
	if len(session.Candidates) == 0 {
		lines = append(lines, "", "_No candidates._")
		return strings.Join(lines, "\n") + "\n"
	}
	for _, candidate := range session.Candidates {
		lines = append(lines,
			"",
			fmt.Sprintf("### %s", defaultString(strings.TrimSpace(candidate.Title), candidate.CandidateID)),
			fmt.Sprintf("- Candidate ID: `%s`", defaultString(strings.TrimSpace(candidate.CandidateID), "(missing)")),
			fmt.Sprintf("- Status: `%s`", normalizeStartWorkFindingCandidateStatus(candidate.Status)),
			fmt.Sprintf("- Severity: `%s`", normalizeGithubSeverity(candidate.Severity)),
			fmt.Sprintf("- Work type: `%s`", defaultString(normalizeWorkType(candidate.WorkType), workTypeFeature)),
		)
		if strings.TrimSpace(candidate.Summary) != "" {
			lines = append(lines, "", candidate.Summary)
		}
		if strings.TrimSpace(candidate.Detail) != "" {
			lines = append(lines, "", candidate.Detail)
		}
		if strings.TrimSpace(candidate.Evidence) != "" {
			lines = append(lines, "", "Evidence:", candidate.Evidence)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func writeStartWorkFindingImportSessionArtifacts(session *startWorkFindingImportSession) error {
	if session == nil || strings.TrimSpace(session.RepoSlug) == "" || strings.TrimSpace(session.ID) == "" {
		return nil
	}
	artifactsDir := startWorkFindingImportSessionDir(session.RepoSlug, session.ID)
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return err
	}
	session.ArtifactsDir = artifactsDir
	session.SourceMarkdownPath = filepath.Join(artifactsDir, "source.md")
	session.CandidatesPath = filepath.Join(artifactsDir, "candidates.json")
	session.PreviewPath = filepath.Join(artifactsDir, "preview.md")
	if err := os.WriteFile(session.SourceMarkdownPath, []byte(session.MarkdownSnapshot), 0o644); err != nil {
		return err
	}
	candidatesPayload := map[string]any{
		"session_id": session.ID,
		"repo_slug":  session.RepoSlug,
		"candidates": session.Candidates,
	}
	if err := writeGithubJSON(session.CandidatesPath, candidatesPayload); err != nil {
		return err
	}
	if err := os.WriteFile(session.PreviewPath, []byte(startWorkFindingImportPreviewMarkdown(*session)), 0o644); err != nil {
		return err
	}
	return nil
}

func buildStartWorkFindingImportPrompt(repoSlug string, markdown string) string {
	lines := []string{
		"Parse this markdown into candidate findings and return JSON only.",
		`Schema: {"candidates":[{"candidate_id":"...","title":"...","summary":"...","detail":"...","evidence":"...","severity":"low|medium|high|critical","work_type":"bug_fix|refactor|feature|test_only","files":["..."],"path":"...","line":123,"route":"...","page":"...","parse_notes":"..."}]}`,
		"Extract only actionable candidate findings. Do not create tasks. Do not include parent task fields.",
		fmt.Sprintf("Repo: %s", repoSlug),
		"",
		"Markdown:",
		markdown,
	}
	return strings.Join(lines, "\n")
}

func runStartWorkFindingImportParser(repoSlug string, repoPath string, markdown string) ([]startWorkFindingImportCandidate, error) {
	prompt := buildStartWorkFindingImportPrompt(repoSlug, markdown)
	args, fastMode := normalizeLocalWorkCodexArgsWithFast(nil)
	prompt = prefixCodexFastPrompt(prompt, fastMode)
	args = append([]string{"exec", "-C", repoPath}, args...)
	args = append(args, "-")
	cmd := exec.Command("codex", args...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "NANA_PROJECT_AGENTS_ROOT="+repoPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseStartWorkFindingImportCandidatesOutput(stdout.String())
}

func parseStartWorkFindingImportCandidatesOutput(raw string) ([]startWorkFindingImportCandidate, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("findings import output did not contain a JSON object")
	}
	var payload struct {
		Candidates []startWorkFindingImportCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err != nil {
		return nil, err
	}
	return validateStartWorkFindingImportCandidates(payload.Candidates)
}

func validateStartWorkFindingImportCandidates(candidates []startWorkFindingImportCandidate) ([]startWorkFindingImportCandidate, error) {
	normalized := make([]startWorkFindingImportCandidate, 0, len(candidates))
	seen := map[string]bool{}
	for index, candidate := range candidates {
		candidate = normalizeStartWorkFindingImportCandidate(candidate)
		if candidate.CandidateID == "" {
			candidate.CandidateID = fmt.Sprintf("candidate-%d", index+1)
		}
		if seen[candidate.CandidateID] {
			return nil, fmt.Errorf("duplicate candidate_id %q", candidate.CandidateID)
		}
		seen[candidate.CandidateID] = true
		if candidate.Title == "" {
			return nil, fmt.Errorf("candidate %s is missing title", candidate.CandidateID)
		}
		if candidate.Summary == "" && candidate.Detail == "" {
			return nil, fmt.Errorf("candidate %s must include summary or detail", candidate.CandidateID)
		}
		if candidate.Summary == "" {
			candidate.Summary = candidate.Detail
		}
		if candidate.Detail == "" {
			candidate.Detail = candidate.Summary
		}
		normalized = append(normalized, candidate)
	}
	return normalized, nil
}

func listStartUIFindings(repoSlug string) ([]startWorkFinding, error) {
	return listStartUIFindingsWithDeleted(repoSlug, false)
}

func listStartUIFindingsWithDeleted(repoSlug string, includeDeleted bool) ([]startWorkFinding, error) {
	state, err := readStartWorkState(repoSlug)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []startWorkFinding{}, nil
		}
		return nil, err
	}
	items := make([]startWorkFinding, 0, len(state.Findings))
	for _, findingID := range startWorkFindingIDsSorted(state) {
		finding := state.Findings[findingID]
		if !includeDeleted && finding.Status == startWorkFindingStatusDeleted {
			continue
		}
		items = append(items, finding)
	}
	sort.SliceStable(items, func(i, j int) bool {
		leftRank := startUIFindingStatusRank(items[i].Status)
		rightRank := startUIFindingStatusRank(items[j].Status)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		return items[i].Title < items[j].Title
	})
	return items, nil
}

func startUIFindingStatusRank(status string) int {
	switch normalizeStartWorkFindingStatus(status) {
	case startWorkFindingStatusOpen:
		return 0
	case startWorkFindingStatusPromoted:
		return 1
	case startWorkFindingStatusDismissed:
		return 2
	case startWorkFindingStatusDeleted:
		return 3
	case startWorkFindingStatusResolved:
		return 4
	default:
		return 5
	}
}

func loadStartUIFindings(repoSlug string) (startUIFindingsResponse, error) {
	return loadStartUIFindingsWithDeleted(repoSlug, false)
}

func loadStartUIFindingsWithDeleted(repoSlug string, includeDeleted bool) (startUIFindingsResponse, error) {
	summary, err := loadStartUIRepoSummary(repoSlug, true)
	if err != nil {
		return startUIFindingsResponse{}, err
	}
	items, err := listStartUIFindingsWithDeleted(repoSlug, includeDeleted)
	if err != nil {
		return startUIFindingsResponse{}, err
	}
	return startUIFindingsResponse{Repo: summary, Items: items}, nil
}

func loadStartUIFinding(repoSlug string, findingID string) (startWorkFinding, error) {
	state, err := readStartWorkState(repoSlug)
	if err != nil {
		return startWorkFinding{}, err
	}
	finding, ok := state.Findings[strings.TrimSpace(findingID)]
	if !ok {
		return startWorkFinding{}, fmt.Errorf("finding %s was not found", findingID)
	}
	return finding, nil
}

func patchStartUIFinding(repoSlug string, findingID string, payload startUIFindingPatchRequest) (*startWorkState, startWorkFinding, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkFinding{}, err
	}
	finding, ok := state.Findings[strings.TrimSpace(findingID)]
	if !ok {
		return nil, startWorkFinding{}, fmt.Errorf("finding %s was not found", findingID)
	}
	if finding.Status == startWorkFindingStatusDeleted {
		return nil, startWorkFinding{}, fmt.Errorf("finding %s is deleted", findingID)
	}
	if payload.Title != nil {
		title := strings.TrimSpace(*payload.Title)
		if title == "" {
			return nil, startWorkFinding{}, fmt.Errorf("title is required")
		}
		finding.Title = title
	}
	if payload.Summary != nil {
		finding.Summary = strings.TrimSpace(*payload.Summary)
	}
	if payload.Detail != nil {
		finding.Detail = strings.TrimSpace(*payload.Detail)
	}
	if payload.Evidence != nil {
		finding.Evidence = strings.TrimSpace(*payload.Evidence)
	}
	if payload.Severity != nil {
		finding.Severity = normalizeGithubSeverity(*payload.Severity)
	}
	if payload.WorkType != nil {
		workType, err := parseRequiredWorkType(*payload.WorkType, "work_type")
		if err != nil {
			return nil, startWorkFinding{}, err
		}
		finding.WorkType = workType
	}
	if payload.Files != nil {
		finding.Files = uniqueStrings(*payload.Files)
	}
	if payload.Path != nil {
		finding.Path = strings.TrimSpace(*payload.Path)
	}
	if payload.Line != nil {
		if *payload.Line < 0 {
			return nil, startWorkFinding{}, fmt.Errorf("line must be >= 0")
		}
		finding.Line = *payload.Line
	}
	if payload.Route != nil {
		finding.Route = strings.TrimSpace(*payload.Route)
	}
	if payload.Page != nil {
		finding.Page = strings.TrimSpace(*payload.Page)
	}
	finding.UpdatedAt = ISOTimeNow()
	state.Findings[finding.ID] = finding
	state.UpdatedAt = finding.UpdatedAt
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkFinding{}, err
	}
	return state, finding, nil
}

func promoteStartUIFinding(repoSlug string, findingID string) (*startWorkState, startWorkFinding, startWorkPlannedItem, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkFinding{}, startWorkPlannedItem{}, err
	}
	finding, item, err := promoteStartWorkFindingUnlocked(state, findingID)
	if err != nil {
		return nil, startWorkFinding{}, startWorkPlannedItem{}, err
	}
	state.UpdatedAt = ISOTimeNow()
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkFinding{}, startWorkPlannedItem{}, err
	}
	return state, finding, item, nil
}

func dismissStartUIFinding(repoSlug string, findingID string, reason string) (*startWorkState, startWorkFinding, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkFinding{}, err
	}
	finding, err := dismissStartWorkFindingUnlocked(state, findingID, reason)
	if err != nil {
		return nil, startWorkFinding{}, err
	}
	state.UpdatedAt = ISOTimeNow()
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkFinding{}, err
	}
	return state, finding, nil
}

func restoreStartUIFinding(repoSlug string, findingID string) (*startWorkState, startWorkFinding, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkFinding{}, err
	}
	finding, err := restoreStartWorkFindingUnlocked(state, findingID)
	if err != nil {
		return nil, startWorkFinding{}, err
	}
	state.UpdatedAt = ISOTimeNow()
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkFinding{}, err
	}
	return state, finding, nil
}

func listStartUIFindingImportSessions(repoSlug string) ([]startWorkFindingImportSession, error) {
	state, err := readStartWorkState(repoSlug)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []startWorkFindingImportSession{}, nil
		}
		return nil, err
	}
	items := make([]startWorkFindingImportSession, 0, len(state.ImportSessions))
	for sessionID := range state.ImportSessions {
		items = append(items, state.ImportSessions[sessionID])
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		return items[i].ID > items[j].ID
	})
	return items, nil
}

func loadStartUIFindingImportSessions(repoSlug string) (startUIFindingImportSessionsResponse, error) {
	summary, err := loadStartUIRepoSummary(repoSlug, true)
	if err != nil {
		return startUIFindingImportSessionsResponse{}, err
	}
	items, err := listStartUIFindingImportSessions(repoSlug)
	if err != nil {
		return startUIFindingImportSessionsResponse{}, err
	}
	return startUIFindingImportSessionsResponse{Repo: summary, Items: items}, nil
}

func loadStartUIFindingImportSession(repoSlug string, sessionID string) (startWorkFindingImportSession, error) {
	state, err := readStartWorkState(repoSlug)
	if err != nil {
		return startWorkFindingImportSession{}, err
	}
	session, ok := state.ImportSessions[strings.TrimSpace(sessionID)]
	if !ok {
		return startWorkFindingImportSession{}, fmt.Errorf("finding import session %s was not found", sessionID)
	}
	return session, nil
}

func createStartUIFindingImportSession(repoSlug string, filePath string, markdown string) (startWorkFindingImportSession, error) {
	trimmedMarkdown := strings.TrimSpace(markdown)
	if trimmedMarkdown == "" {
		return startWorkFindingImportSession{}, fmt.Errorf("markdown is required")
	}
	repoPath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
	if repoPath == "" {
		return startWorkFindingImportSession{}, fmt.Errorf("repo %s does not have a managed source checkout", repoSlug)
	}
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return startWorkFindingImportSession{}, err
	}
	now := ISOTimeNow()
	session := startWorkFindingImportSession{
		ID:               fmt.Sprintf("import-%d", timeNowUnixNano()),
		RepoSlug:         repoSlug,
		InputFilePath:    strings.TrimSpace(filePath),
		MarkdownSnapshot: markdown,
		ParseStatus:      startWorkFindingImportParsePending,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	candidates, parseErr := runStartWorkFindingImportParser(repoSlug, repoPath, markdown)
	if parseErr != nil {
		session.ParseStatus = startWorkFindingImportParseFailed
		session.ParseError = parseErr.Error()
		session.Candidates = []startWorkFindingImportCandidate{}
	} else {
		session.ParseStatus = startWorkFindingImportParseParsed
		session.Candidates = candidates
	}
	if err := writeStartWorkFindingImportSessionArtifacts(&session); err != nil {
		return startWorkFindingImportSession{}, err
	}
	ensureStartWorkFindingsState(state)
	state.ImportSessions[session.ID] = session
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return startWorkFindingImportSession{}, err
	}
	return session, nil
}

func patchStartUIFindingImportCandidate(repoSlug string, sessionID string, candidateID string, payload startUIFindingImportCandidatePatchRequest) (*startWorkState, startWorkFindingImportSession, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkFindingImportSession{}, err
	}
	session, ok := state.ImportSessions[strings.TrimSpace(sessionID)]
	if !ok {
		return nil, startWorkFindingImportSession{}, fmt.Errorf("finding import session %s was not found", sessionID)
	}
	index := -1
	for candidateIndex := range session.Candidates {
		if session.Candidates[candidateIndex].CandidateID == strings.TrimSpace(candidateID) {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		return nil, startWorkFindingImportSession{}, fmt.Errorf("candidate %s was not found", candidateID)
	}
	candidate := session.Candidates[index]
	if payload.Title != nil {
		title := strings.TrimSpace(*payload.Title)
		if title == "" {
			return nil, startWorkFindingImportSession{}, fmt.Errorf("title is required")
		}
		candidate.Title = title
	}
	if payload.Summary != nil {
		candidate.Summary = strings.TrimSpace(*payload.Summary)
	}
	if payload.Detail != nil {
		candidate.Detail = strings.TrimSpace(*payload.Detail)
	}
	if payload.Evidence != nil {
		candidate.Evidence = strings.TrimSpace(*payload.Evidence)
	}
	if payload.Severity != nil {
		candidate.Severity = normalizeGithubSeverity(*payload.Severity)
	}
	if payload.WorkType != nil {
		workType, err := parseRequiredWorkType(*payload.WorkType, "work_type")
		if err != nil {
			return nil, startWorkFindingImportSession{}, err
		}
		candidate.WorkType = workType
	}
	if payload.Files != nil {
		candidate.Files = uniqueStrings(*payload.Files)
	}
	if payload.Path != nil {
		candidate.Path = strings.TrimSpace(*payload.Path)
	}
	if payload.Line != nil {
		if *payload.Line < 0 {
			return nil, startWorkFindingImportSession{}, fmt.Errorf("line must be >= 0")
		}
		candidate.Line = *payload.Line
	}
	if payload.Route != nil {
		candidate.Route = strings.TrimSpace(*payload.Route)
	}
	if payload.Page != nil {
		candidate.Page = strings.TrimSpace(*payload.Page)
	}
	if payload.ParseNotes != nil {
		candidate.ParseNotes = strings.TrimSpace(*payload.ParseNotes)
	}
	candidate = normalizeStartWorkFindingImportCandidate(candidate)
	session.Candidates[index] = candidate
	session.UpdatedAt = ISOTimeNow()
	if err := writeStartWorkFindingImportSessionArtifacts(&session); err != nil {
		return nil, startWorkFindingImportSession{}, err
	}
	state.ImportSessions[session.ID] = session
	state.UpdatedAt = session.UpdatedAt
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkFindingImportSession{}, err
	}
	return state, session, nil
}

func promoteStartUIFindingImportCandidate(repoSlug string, sessionID string, candidateID string) (*startWorkState, startWorkFindingImportSession, startWorkFinding, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkFindingImportSession{}, startWorkFinding{}, err
	}
	session, ok := state.ImportSessions[strings.TrimSpace(sessionID)]
	if !ok {
		return nil, startWorkFindingImportSession{}, startWorkFinding{}, fmt.Errorf("finding import session %s was not found", sessionID)
	}
	for index := range session.Candidates {
		candidate := session.Candidates[index]
		if candidate.CandidateID != strings.TrimSpace(candidateID) {
			continue
		}
		if normalizeStartWorkFindingCandidateStatus(candidate.Status) != startWorkFindingCandidateStatusCandidate {
			return nil, startWorkFindingImportSession{}, startWorkFinding{}, fmt.Errorf("candidate %s is not available for promotion", candidateID)
		}
		findingID, _, finding := upsertStartWorkFindingUnlocked(state, startWorkFindingUpsertInput{
			RepoSlug:     repoSlug,
			SourceKind:   startWorkFindingSourceKindManualImport,
			SourceID:     session.ID,
			SourceItemID: candidate.CandidateID,
			Title:        candidate.Title,
			Summary:      candidate.Summary,
			Detail:       candidate.Detail,
			Evidence:     candidate.Evidence,
			Severity:     candidate.Severity,
			WorkType:     candidate.WorkType,
			Files:        append([]string{}, candidate.Files...),
			Path:         candidate.Path,
			Line:         candidate.Line,
			Route:        candidate.Route,
			Page:         candidate.Page,
			CreatedAt:    session.CreatedAt,
			UpdatedAt:    ISOTimeNow(),
		})
		candidate.Status = startWorkFindingCandidateStatusPromoted
		candidate.PromotedFindingID = findingID
		session.Candidates[index] = candidate
		session.UpdatedAt = ISOTimeNow()
		if err := writeStartWorkFindingImportSessionArtifacts(&session); err != nil {
			return nil, startWorkFindingImportSession{}, startWorkFinding{}, err
		}
		state.ImportSessions[session.ID] = session
		state.Findings[findingID] = finding
		state.UpdatedAt = session.UpdatedAt
		if err := writeStartWorkStateUnlocked(*state); err != nil {
			return nil, startWorkFindingImportSession{}, startWorkFinding{}, err
		}
		return state, session, finding, nil
	}
	return nil, startWorkFindingImportSession{}, startWorkFinding{}, fmt.Errorf("candidate %s was not found", candidateID)
}

func dropStartUIFindingImportCandidate(repoSlug string, sessionID string, candidateID string) (*startWorkState, startWorkFindingImportSession, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkFindingImportSession{}, err
	}
	session, ok := state.ImportSessions[strings.TrimSpace(sessionID)]
	if !ok {
		return nil, startWorkFindingImportSession{}, fmt.Errorf("finding import session %s was not found", sessionID)
	}
	for index := range session.Candidates {
		if session.Candidates[index].CandidateID != strings.TrimSpace(candidateID) {
			continue
		}
		if normalizeStartWorkFindingCandidateStatus(session.Candidates[index].Status) != startWorkFindingCandidateStatusCandidate {
			return nil, startWorkFindingImportSession{}, fmt.Errorf("candidate %s is not available for drop", candidateID)
		}
		session.Candidates[index].Status = startWorkFindingCandidateStatusDropped
		session.UpdatedAt = ISOTimeNow()
		if err := writeStartWorkFindingImportSessionArtifacts(&session); err != nil {
			return nil, startWorkFindingImportSession{}, err
		}
		state.ImportSessions[session.ID] = session
		state.UpdatedAt = session.UpdatedAt
		if err := writeStartWorkStateUnlocked(*state); err != nil {
			return nil, startWorkFindingImportSession{}, err
		}
		return state, session, nil
	}
	return nil, startWorkFindingImportSession{}, fmt.Errorf("candidate %s was not found", candidateID)
}

func replaceStartUIFindingImportSessionCandidates(repoSlug string, sessionID string, candidates []startWorkFindingImportCandidate) (*startWorkState, startWorkFindingImportSession, error) {
	normalized, err := validateStartWorkFindingImportCandidates(candidates)
	if err != nil {
		return nil, startWorkFindingImportSession{}, err
	}
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkFindingImportSession{}, err
	}
	session, ok := state.ImportSessions[strings.TrimSpace(sessionID)]
	if !ok {
		return nil, startWorkFindingImportSession{}, fmt.Errorf("finding import session %s was not found", sessionID)
	}
	existingByID := map[string]startWorkFindingImportCandidate{}
	for _, existing := range session.Candidates {
		existingByID[existing.CandidateID] = existing
	}
	for index := range normalized {
		if existing, ok := existingByID[normalized[index].CandidateID]; ok {
			normalized[index].Status = existing.Status
			normalized[index].PromotedFindingID = existing.PromotedFindingID
		}
	}
	session.Candidates = normalized
	session.ParseStatus = startWorkFindingImportParseParsed
	session.ParseError = ""
	session.UpdatedAt = ISOTimeNow()
	if err := writeStartWorkFindingImportSessionArtifacts(&session); err != nil {
		return nil, startWorkFindingImportSession{}, err
	}
	state.ImportSessions[session.ID] = session
	state.UpdatedAt = session.UpdatedAt
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkFindingImportSession{}, err
	}
	return state, session, nil
}

func parseStartUIFindingRoute(tail string) (string, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(tail, "findings/"), "/")
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 && strings.TrimSpace(parts[0]) != "" {
		return strings.TrimSpace(parts[0]), "", true
	}
	if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
	}
	return "", "", false
}

func parseStartUIFindingImportSessionRoute(tail string) (string, string, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(tail, "finding-import-sessions/"), "/")
	if trimmed == "" {
		return "", "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 && strings.TrimSpace(parts[0]) != "" {
		return strings.TrimSpace(parts[0]), "", "", true
	}
	if len(parts) == 3 && strings.TrimSpace(parts[0]) != "" && parts[1] == "candidates" && strings.TrimSpace(parts[2]) != "" {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[2]), "candidate", true
	}
	if len(parts) == 4 && strings.TrimSpace(parts[0]) != "" && parts[1] == "candidates" && strings.TrimSpace(parts[2]) != "" && strings.TrimSpace(parts[3]) != "" {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[2]), strings.TrimSpace(parts[3]), true
	}
	return "", "", "", false
}
