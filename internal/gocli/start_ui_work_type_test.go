package gocli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateStartUIPlannedItemRequiresWorkType(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)

	_, _, err := createStartUIPlannedItem(repoSlug, startUIPlannedItemRequest{
		Title:      "Run smoke after deploy",
		LaunchKind: "local_work",
	})
	if err == nil || !strings.Contains(err.Error(), "work_type") {
		t.Fatalf("expected missing work_type error, got %v", err)
	}
}

func TestUpsertStartUITrackedIssuePlannedItemInfersWorkTypeFromLabels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)

	_, item, err := upsertStartUITrackedIssuePlannedItem(repoSlug, startUITrackedIssueScheduleRequest{
		Number:    42,
		Title:     "Fix flaky widget",
		TargetURL: "https://github.com/acme/widget/issues/42",
		Labels:    []string{"bug", "P1"},
	})
	if err != nil {
		t.Fatalf("upsertStartUITrackedIssuePlannedItem: %v", err)
	}
	if item.WorkType != workTypeBugFix {
		t.Fatalf("expected tracked issue work type bug_fix, got %+v", item)
	}
	if !strings.Contains(item.Description, "Work type: bug_fix") {
		t.Fatalf("expected tracked issue description to persist work type, got %q", item.Description)
	}
}

func TestQueueScoutItemAsPlannedWorkPersistsWorkType(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	repo := createLocalWorkRepoAt(t, sourcePath)
	writeScoutPickupFixture(t, repo, enhancementScoutRole, "Add command palette", "Introduce a command palette for the start UI")
	if err := writeGithubJSON(filepath.Join(repo, ".nana", scoutArtifactRoot(enhancementScoutRole), "enhance-test", "policy.json"), improvementPolicy{
		Version:          1,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{"enhancement"},
	}); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	items, err := listStartUIScoutItems(repoSlug)
	if err != nil {
		t.Fatalf("listStartUIScoutItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one scout item, got %+v", items)
	}
	if items[0].WorkType != workTypeFeature {
		t.Fatalf("expected scout item work type feature, got %+v", items[0])
	}
	_, workState, err := syncStartWorkScoutJobs(repo, repoSlug)
	if err != nil {
		t.Fatalf("syncStartWorkScoutJobs: %v", err)
	}
	if workState == nil {
		t.Fatalf("expected synced scout state, got nil")
	}
	job, ok := workState.ScoutJobs[items[0].ID]
	if !ok {
		t.Fatalf("expected synced scout job %s, got %+v", items[0].ID, workState.ScoutJobs)
	}
	if job.WorkType != workTypeFeature {
		t.Fatalf("expected synced scout job to persist work type, got %+v", job)
	}
}
