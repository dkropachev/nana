package gocli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type localScoutPickupState struct {
	Version int                             `json:"version"`
	Items   map[string]localScoutPickupItem `json:"items,omitempty"`
}

type localScoutPickupItem struct {
	Status        string `json:"status"`
	Title         string `json:"title"`
	Artifact      string `json:"artifact"`
	RunID         string `json:"run_id,omitempty"`
	PlannedItemID string `json:"planned_item_id,omitempty"`
	Error         string `json:"error,omitempty"`
	UpdatedAt     string `json:"updated_at"`
	ProposalID    string `json:"proposal_id"`
}

type localScoutDiscoveredItem struct {
	ID              string
	Role            string
	Title           string
	Artifact        string
	Proposal        scoutFinding
	ProposalPath    string
	PolicyPath      string
	PreflightPath   string
	IssueDraftPath  string
	RawOutputPath   string
	GeneratedAt     string
	AuditMode       string
	SurfaceKind     string
	SurfaceTarget   string
	BrowserReady    bool
	PreflightReason string
	Destination     string
	ForkRepo        string
}

var errLocalScoutPickupAlreadyClaimed = errors.New("local scout pickup already claimed")

func reconcileLocalScoutPickupPlannedItems(repoPath string, repoSlug string) (bool, error) {
	if strings.TrimSpace(repoPath) == "" || strings.TrimSpace(repoSlug) == "" {
		return false, nil
	}
	state, _, err := readLocalScoutPickupStateWithReadLock(repoPath)
	if err != nil {
		return false, err
	}
	workState, err := readStartWorkState(repoSlug)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	updated := false
	for itemID, record := range state.Items {
		plannedItemID := strings.TrimSpace(record.PlannedItemID)
		if plannedItemID == "" {
			continue
		}
		plannedItem, ok := workState.PlannedItems[plannedItemID]
		if !ok {
			continue
		}

		plannedState := strings.TrimSpace(plannedItem.State)
		nextStatus := record.Status
		nextError := record.Error
		nextRunID := record.RunID
		switch plannedState {
		case startPlannedItemQueued, startPlannedItemLaunching, startPlannedItemLaunched:
			nextStatus = "in_progress"
			nextError = ""
			if strings.TrimSpace(plannedItem.LaunchRunID) != "" {
				nextRunID = strings.TrimSpace(plannedItem.LaunchRunID)
			}
		case "done":
			nextStatus = "completed"
		case startPlannedItemFailed:
			nextStatus = "failed"
			nextError = strings.TrimSpace(plannedItem.LastError)
		}
		if nextStatus == record.Status && nextError == record.Error && nextRunID == record.RunID {
			continue
		}
		record.Status = nextStatus
		record.Error = nextError
		record.RunID = nextRunID
		record.UpdatedAt = defaultString(strings.TrimSpace(plannedItem.UpdatedAt), ISOTimeNow())
		state.Items[itemID] = record
		updated = true
	}
	if !updated {
		return false, nil
	}
	if err := updateLocalScoutPickupState(repoPath, func(current *localScoutPickupState) error {
		current.Items = state.Items
		return nil
	}); err != nil {
		return false, err
	}
	return true, nil
}

var startRunLocalScoutWork = func(repoPath string, task string, codexArgs []string) error {
	args := []string{"start", "--repo", repoPath, "--task", task}
	if len(codexArgs) > 0 {
		args = append(args, "--")
		args = append(args, codexArgs...)
	}
	return runLocalWorkCommand(repoPath, args)
}

func runLocalScoutDiscoveredItems(repoPath string, codexArgs []string) (bool, error) {
	if repoSlug := findManagedRepoSlugForSourcePath(repoPath); repoSlug != "" {
		updated, state, err := syncStartWorkScoutJobs(repoPath, repoSlug)
		if err != nil {
			return false, err
		}
		if state == nil {
			return false, nil
		}
		queued := 0
		for _, job := range state.ScoutJobs {
			if job.Destination == improvementDestinationLocal && job.Status == startScoutJobQueued {
				queued++
			}
		}
		fmt.Fprintf(os.Stdout, "[start] Local scout jobs: queued=%d.\n", queued)
		return updated || queued > 0, nil
	}
	return runLocalScoutDiscoveredItemsLegacy(repoPath, codexArgs)
}

func runLocalScoutDiscoveredItemsLegacy(repoPath string, codexArgs []string) (bool, error) {
	items, err := listLocalScoutDiscoveredItemsWithReadLock(repoPath)
	if err != nil {
		return false, err
	}
	if len(items) == 0 {
		fmt.Fprintln(os.Stdout, "[start] Local discovered items: none found.")
		return false, nil
	}
	state, _, err := readLocalScoutPickupStateWithReadLock(repoPath)
	if err != nil {
		return false, err
	}
	pending := []localScoutDiscoveredItem{}
	for _, item := range items {
		if _, ok := state.Items[item.ID]; ok {
			continue
		}
		pending = append(pending, item)
	}
	if len(pending) == 0 {
		fmt.Fprintf(os.Stdout, "[start] Local discovered items: 0 pending (%d already picked).\n", len(items))
		return false, nil
	}
	item := pending[0]
	fmt.Fprintf(os.Stdout, "[start] Local discovered items: %d pending; working on: %s\n", len(pending), item.Title)
	if err := updateLocalScoutPickupState(repoPath, func(current *localScoutPickupState) error {
		if _, ok := current.Items[item.ID]; ok {
			return errLocalScoutPickupAlreadyClaimed
		}
		current.Items[item.ID] = localScoutPickupItem{
			Status:     "running",
			Title:      item.Title,
			Artifact:   item.Artifact,
			UpdatedAt:  ISOTimeNow(),
			ProposalID: item.ID,
		}
		return nil
	}); err != nil {
		if errors.Is(err, errLocalScoutPickupAlreadyClaimed) {
			return false, nil
		}
		return false, err
	}
	if err := startRunLocalScoutWork(repoPath, formatLocalScoutWorkTask(item), codexArgs); err != nil {
		updateErr := updateLocalScoutPickupState(repoPath, func(current *localScoutPickupState) error {
			record := current.Items[item.ID]
			record.Status = "failed"
			record.Error = err.Error()
			record.UpdatedAt = ISOTimeNow()
			current.Items[item.ID] = record
			return nil
		})
		if updateErr != nil {
			return true, updateErr
		}
		fmt.Fprintf(os.Stdout, "[start] Local discovered item failed: %s: %v\n", item.Title, err)
		return true, nil
	}
	if err := updateLocalScoutPickupState(repoPath, func(current *localScoutPickupState) error {
		record := current.Items[item.ID]
		record.Status = "completed"
		record.UpdatedAt = ISOTimeNow()
		if manifest, _, err := resolveLocalWorkRun(repoPath, localWorkRunSelection{UseLast: true, RepoPath: repoPath}); err == nil {
			record.RunID = manifest.RunID
		}
		current.Items[item.ID] = record
		return nil
	}); err != nil {
		return true, err
	}
	fmt.Fprintf(os.Stdout, "[start] Local discovered item completed: %s\n", item.Title)
	return true, nil
}

func listLocalScoutDiscoveredItemsWithReadLock(repoPath string) ([]localScoutDiscoveredItem, error) {
	items := []localScoutDiscoveredItem{}
	err := withScoutPickupStateReadLock(repoPath, func() error {
		var innerErr error
		items, innerErr = listLocalScoutDiscoveredItems(repoPath)
		return innerErr
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func listLocalScoutDiscoveredItems(repoPath string) ([]localScoutDiscoveredItem, error) {
	items := []localScoutDiscoveredItem{}
	for _, role := range supportedScoutRoleOrder {
		matches, err := filepath.Glob(filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), "*", "proposals.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, path := range matches {
			var report scoutReport
			if err := readGithubJSON(path, &report); err != nil {
				continue
			}
			artifactDir := filepath.Dir(path)
			relArtifact, _ := filepath.Rel(repoPath, artifactDir)
			relArtifact = filepath.ToSlash(relArtifact)
			policyPath := filepath.Join(artifactDir, "policy.json")
			policy := scoutPolicy{Version: 1, IssueDestination: improvementDestinationLocal}
			_ = readGithubJSON(policyPath, &policy)
			policy.IssueDestination = normalizeScoutDestination(policy.IssueDestination)
			relPolicyPath := ""
			if _, err := os.Stat(policyPath); err == nil {
				relPolicyPath, _ = filepath.Rel(repoPath, policyPath)
				relPolicyPath = filepath.ToSlash(relPolicyPath)
			}
			relProposalPath, _ := filepath.Rel(repoPath, path)
			relProposalPath = filepath.ToSlash(relProposalPath)
			preflightPath := filepath.Join(artifactDir, "preflight.json")
			issueDraftPath := filepath.Join(artifactDir, "issue-drafts.md")
			rawOutputPath := filepath.Join(artifactDir, "raw-output.txt")
			var preflight uiScoutPreflight
			relPreflightPath := ""
			relIssueDraftPath := ""
			relRawOutputPath := ""
			if _, err := os.Stat(preflightPath); err == nil {
				relPreflightPath, _ = filepath.Rel(repoPath, preflightPath)
				relPreflightPath = filepath.ToSlash(relPreflightPath)
				_ = readGithubJSON(preflightPath, &preflight)
			}
			if _, err := os.Stat(issueDraftPath); err == nil {
				relIssueDraftPath, _ = filepath.Rel(repoPath, issueDraftPath)
				relIssueDraftPath = filepath.ToSlash(relIssueDraftPath)
			}
			if _, err := os.Stat(rawOutputPath); err == nil {
				relRawOutputPath, _ = filepath.Rel(repoPath, rawOutputPath)
				relRawOutputPath = filepath.ToSlash(relRawOutputPath)
			}
			for _, proposal := range report.Proposals {
				title := strings.TrimSpace(proposal.Title)
				if title == "" || strings.TrimSpace(proposal.Summary) == "" {
					continue
				}
				items = append(items, localScoutDiscoveredItem{
					ID:              localScoutProposalID(role, proposal),
					Role:            role,
					Title:           title,
					Artifact:        relArtifact,
					Proposal:        proposal,
					ProposalPath:    relProposalPath,
					PolicyPath:      relPolicyPath,
					PreflightPath:   relPreflightPath,
					IssueDraftPath:  relIssueDraftPath,
					RawOutputPath:   relRawOutputPath,
					GeneratedAt:     strings.TrimSpace(report.GeneratedAt),
					AuditMode:       strings.TrimSpace(preflight.Mode),
					SurfaceKind:     strings.TrimSpace(preflight.SurfaceKind),
					SurfaceTarget:   strings.TrimSpace(preflight.SurfaceTarget),
					BrowserReady:    preflight.BrowserReady,
					PreflightReason: strings.TrimSpace(preflight.Reason),
					Destination:     startUIScoutDestinationLabel(policy.IssueDestination),
					ForkRepo:        strings.TrimSpace(policy.ForkRepo),
				})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Artifact == items[j].Artifact {
			return items[i].Title < items[j].Title
		}
		return items[i].Artifact < items[j].Artifact
	})
	return items, nil
}

func localScoutProposalID(role string, proposal scoutFinding) string {
	return sha256Hex(strings.Join([]string{
		role,
		strings.TrimSpace(proposal.Title),
		strings.TrimSpace(proposal.Summary),
		strings.TrimSpace(proposal.SuggestedNextStep),
		strings.TrimSpace(proposal.Page),
		strings.TrimSpace(proposal.Route),
		strings.TrimSpace(proposal.Severity),
		strings.TrimSpace(proposal.TargetKind),
		strings.Join(proposal.Files, ","),
		strings.Join(proposal.Screenshots, ","),
	}, "\n"))
}

func formatLocalScoutWorkTask(item localScoutDiscoveredItem) string {
	proposal := item.Proposal
	lines := []string{
		fmt.Sprintf("Implement local scout proposal: %s", item.Title),
		"",
		fmt.Sprintf("Source artifact: %s", item.Artifact),
		fmt.Sprintf("Scout role: %s", item.Role),
		fmt.Sprintf("Work type: %s", defaultString(inferScoutWorkType(item.Role, proposal).WorkType, workTypeFeature)),
		fmt.Sprintf("Area: %s", defaultString(proposal.Area, scoutIssueHeading(item.Role))),
		"",
		"Summary:",
		strings.TrimSpace(proposal.Summary),
	}
	if strings.TrimSpace(proposal.Page) != "" {
		lines = append(lines, "", "Page:", strings.TrimSpace(proposal.Page))
	}
	if strings.TrimSpace(proposal.Route) != "" {
		lines = append(lines, "", "Route:", strings.TrimSpace(proposal.Route))
	}
	if strings.TrimSpace(proposal.Severity) != "" {
		lines = append(lines, "", "Severity:", strings.TrimSpace(proposal.Severity))
	}
	if strings.TrimSpace(proposal.TargetKind) != "" {
		lines = append(lines, "", "Target kind:", strings.TrimSpace(proposal.TargetKind))
	}
	if strings.TrimSpace(proposal.Rationale) != "" {
		lines = append(lines, "", "Rationale:", strings.TrimSpace(proposal.Rationale))
	}
	if strings.TrimSpace(proposal.Evidence) != "" {
		lines = append(lines, "", "Evidence:", strings.TrimSpace(proposal.Evidence))
	}
	if strings.TrimSpace(proposal.Impact) != "" {
		lines = append(lines, "", "Impact:", strings.TrimSpace(proposal.Impact))
	}
	if len(proposal.Files) > 0 {
		lines = append(lines, "", "Files:", strings.Join(proposal.Files, ", "))
	}
	if len(proposal.Screenshots) > 0 {
		lines = append(lines, "", "Screenshots:", strings.Join(proposal.Screenshots, ", "))
	}
	if strings.TrimSpace(proposal.SuggestedNextStep) != "" {
		lines = append(lines, "", "Suggested next step:", strings.TrimSpace(proposal.SuggestedNextStep))
	}
	lines = append(lines, "", "Constraints:", "- Keep the change small and repo-native.", "- Add or update targeted tests when the proposal changes behavior.", "- Verify before completion.")
	return strings.Join(lines, "\n")
}

func readLocalScoutPickupState(repoPath string) (localScoutPickupState, string, error) {
	path, err := localScoutPickupStatePath(repoPath)
	if err != nil {
		return localScoutPickupState{}, "", err
	}
	state := localScoutPickupState{Version: 1, Items: map[string]localScoutPickupItem{}}
	if err := readGithubJSON(path, &state); err != nil {
		if os.IsNotExist(err) {
			return state, path, nil
		}
		return localScoutPickupState{}, "", err
	}
	if state.Items == nil {
		state.Items = map[string]localScoutPickupItem{}
	}
	state.Version = 1
	return state, path, nil
}

func readLocalScoutPickupStateWithReadLock(repoPath string) (localScoutPickupState, string, error) {
	var (
		state localScoutPickupState
		path  string
		err   error
	)
	lockErr := withScoutPickupStateReadLock(repoPath, func() error {
		state, path, err = readLocalScoutPickupState(repoPath)
		return err
	})
	if lockErr != nil {
		return localScoutPickupState{}, "", lockErr
	}
	return state, path, nil
}

func writeLocalScoutPickupState(path string, state localScoutPickupState) error {
	if state.Items == nil {
		state.Items = map[string]localScoutPickupItem{}
	}
	state.Version = 1
	return writeGithubJSON(path, state)
}

func updateLocalScoutPickupState(repoPath string, update func(*localScoutPickupState) error) error {
	return withScoutPickupStateWriteLock(repoPath, func() error {
		state, path, err := readLocalScoutPickupState(repoPath)
		if err != nil {
			return err
		}
		if err := update(&state); err != nil {
			return err
		}
		return writeLocalScoutPickupState(path, state)
	})
}

func withScoutPickupStateReadLock(repoPath string, fn func() error) error {
	return withSourceReadLock(repoPath, repoAccessLockOwner{
		Backend: "scout-pickup",
		RunID:   sanitizePathToken(filepath.Base(repoPath)),
		Purpose: "pickup-state-read",
		Label:   "scout-pickup-state",
	}, fn)
}

func withScoutPickupStateWriteLock(repoPath string, fn func() error) error {
	return withSourceWriteLock(repoPath, repoAccessLockOwner{
		Backend: "scout-pickup",
		RunID:   sanitizePathToken(filepath.Base(repoPath)),
		Purpose: "pickup-state-write",
		Label:   "scout-pickup-state",
	}, fn)
}

func localScoutPickupStatePath(repoPath string) (string, error) {
	output, err := githubGitOutput(repoPath, "rev-parse", "--git-path", filepath.ToSlash(filepath.Join("nana", "scout-pickup-state.json")))
	if err != nil {
		return "", err
	}
	return filepath.Join(repoPath, strings.TrimSpace(output)), nil
}
