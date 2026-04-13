package gocli

import (
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
	Status     string `json:"status"`
	Title      string `json:"title"`
	Artifact   string `json:"artifact"`
	RunID      string `json:"run_id,omitempty"`
	Error      string `json:"error,omitempty"`
	UpdatedAt  string `json:"updated_at"`
	ProposalID string `json:"proposal_id"`
}

type localScoutDiscoveredItem struct {
	ID       string
	Role     string
	Title    string
	Artifact string
	Proposal improvementProposal
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
	items, err := listLocalScoutDiscoveredItems(repoPath)
	if err != nil {
		return false, err
	}
	if len(items) == 0 {
		fmt.Fprintln(os.Stdout, "[start] Local discovered items: none found.")
		return false, nil
	}
	state, statePath, err := readLocalScoutPickupState(repoPath)
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
	state.Items[item.ID] = localScoutPickupItem{
		Status:     "running",
		Title:      item.Title,
		Artifact:   item.Artifact,
		UpdatedAt:  ISOTimeNow(),
		ProposalID: item.ID,
	}
	if err := writeLocalScoutPickupState(statePath, state); err != nil {
		return false, err
	}
	if err := startRunLocalScoutWork(repoPath, formatLocalScoutWorkTask(item), codexArgs); err != nil {
		record := state.Items[item.ID]
		record.Status = "failed"
		record.Error = err.Error()
		record.UpdatedAt = ISOTimeNow()
		state.Items[item.ID] = record
		if writeErr := writeLocalScoutPickupState(statePath, state); writeErr != nil {
			return true, writeErr
		}
		fmt.Fprintf(os.Stdout, "[start] Local discovered item failed: %s: %v\n", item.Title, err)
		return true, nil
	}
	record := state.Items[item.ID]
	record.Status = "completed"
	record.UpdatedAt = ISOTimeNow()
	if manifest, _, err := resolveLocalWorkRun(repoPath, localWorkRunSelection{UseLast: true, RepoPath: repoPath}); err == nil {
		record.RunID = manifest.RunID
	}
	state.Items[item.ID] = record
	if err := writeLocalScoutPickupState(statePath, state); err != nil {
		return true, err
	}
	fmt.Fprintf(os.Stdout, "[start] Local discovered item completed: %s\n", item.Title)
	return true, nil
}

func listLocalScoutDiscoveredItems(repoPath string) ([]localScoutDiscoveredItem, error) {
	items := []localScoutDiscoveredItem{}
	for _, role := range []string{improvementScoutRole, enhancementScoutRole} {
		matches, err := filepath.Glob(filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), "*", "proposals.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, path := range matches {
			var report improvementReport
			if err := readGithubJSON(path, &report); err != nil {
				continue
			}
			artifactDir := filepath.Dir(path)
			relArtifact, _ := filepath.Rel(repoPath, artifactDir)
			for _, proposal := range report.Proposals {
				title := strings.TrimSpace(proposal.Title)
				if title == "" || strings.TrimSpace(proposal.Summary) == "" {
					continue
				}
				items = append(items, localScoutDiscoveredItem{
					ID:       localScoutProposalID(role, proposal),
					Role:     role,
					Title:    title,
					Artifact: filepath.ToSlash(relArtifact),
					Proposal: proposal,
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

func localScoutProposalID(role string, proposal improvementProposal) string {
	return sha256Hex(strings.Join([]string{
		role,
		strings.TrimSpace(proposal.Title),
		strings.TrimSpace(proposal.Summary),
		strings.TrimSpace(proposal.SuggestedNextStep),
		strings.Join(proposal.Files, ","),
	}, "\n"))
}

func formatLocalScoutWorkTask(item localScoutDiscoveredItem) string {
	proposal := item.Proposal
	lines := []string{
		fmt.Sprintf("Implement local scout proposal: %s", item.Title),
		"",
		fmt.Sprintf("Source artifact: %s", item.Artifact),
		fmt.Sprintf("Scout role: %s", item.Role),
		fmt.Sprintf("Area: %s", defaultString(proposal.Area, scoutIssueHeading(item.Role))),
		"",
		"Summary:",
		strings.TrimSpace(proposal.Summary),
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

func writeLocalScoutPickupState(path string, state localScoutPickupState) error {
	if state.Items == nil {
		state.Items = map[string]localScoutPickupItem{}
	}
	state.Version = 1
	return writeGithubJSON(path, state)
}

func localScoutPickupStatePath(repoPath string) (string, error) {
	output, err := githubGitOutput(repoPath, "rev-parse", "--git-path", filepath.ToSlash(filepath.Join("nana", "scout-pickup-state.json")))
	if err != nil {
		return "", err
	}
	return filepath.Join(repoPath, strings.TrimSpace(output)), nil
}
