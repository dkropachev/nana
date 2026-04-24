package gocli

import (
	"slices"
	"testing"
	"time"
)

func TestCreateStartUIPlannedItemKeepsIdempotencyStableAcrossResolvedLocalWorkDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:        6,
		RepoMode:       "local",
		IssuePickMode:  "manual",
		PRForwardMode:  "approve",
		ForkIssuesMode: "manual",
		ImplementMode:  "manual",
		PublishTarget:  "",
	}); err != nil {
		t.Fatalf("write local repo settings: %v", err)
	}

	priority := 2
	initialRequest := startUIPlannedItemRequest{
		Title:          "Run smoke after deploy",
		Description:    "Schedule it after release",
		WorkType:       workTypeFeature,
		Priority:       &priority,
		IdempotencyKey: "planned-item-replay-stable",
	}

	_, created, err := createStartUIPlannedItem(repoSlug, initialRequest)
	if err != nil {
		t.Fatalf("createStartUIPlannedItem: %v", err)
	}
	if created.LaunchKind != "local_work" || created.FindingsHandling != startWorkFindingsHandlingManualReview {
		t.Fatalf("expected omitted local-work defaults to resolve before replay, got %+v", created)
	}
	if created.IdempotencyFingerprint != startUIPlannedItemRawRequestFingerprint(repoSlug, initialRequest) {
		t.Fatalf("expected original request fingerprint, got %+v", created)
	}

	explicitRetry := initialRequest
	explicitRetry.LaunchKind = "local_work"
	explicitRetry.FindingsHandling = startWorkFindingsHandlingManualReview
	if !slices.Contains(startUIPlannedItemLookupFingerprints(repoSlug, explicitRetry), created.IdempotencyFingerprint) {
		t.Fatalf("expected semantically identical local-work retry fingerprints to include %s", created.IdempotencyFingerprint)
	}

	_, replayed, err := createStartUIPlannedItem(repoSlug, explicitRetry)
	if err != nil {
		t.Fatalf("replay createStartUIPlannedItem with explicit defaults: %v", err)
	}
	if replayed.ID != created.ID {
		t.Fatalf("expected idempotent replay to reuse %s, got %+v", created.ID, replayed)
	}
	if replayed.LaunchKind != created.LaunchKind || replayed.FindingsHandling != created.FindingsHandling {
		t.Fatalf("expected replay to return the original planned item, got %+v", replayed)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start work state: %v", err)
	}
	if len(state.PlannedItems) != 1 {
		t.Fatalf("expected one persisted planned item after replay, got %+v", state.PlannedItems)
	}
}

func TestCreateStartUIPlannedItemKeepsIdempotencyStableAcrossRepoModeChangesWhenLaunchKindIsOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:        6,
		RepoMode:       "repo",
		IssuePickMode:  "manual",
		PRForwardMode:  "approve",
		ForkIssuesMode: "manual",
		ImplementMode:  "manual",
		PublishTarget:  "repo",
	}); err != nil {
		t.Fatalf("write repo-backed settings: %v", err)
	}

	priority := 2
	request := startUIPlannedItemRequest{
		Title:          "Run smoke after deploy",
		Description:    "Schedule it after release",
		WorkType:       workTypeFeature,
		Priority:       &priority,
		IdempotencyKey: "planned-item-repo-mode-flip",
	}

	_, created, err := createStartUIPlannedItem(repoSlug, request)
	if err != nil {
		t.Fatalf("createStartUIPlannedItem: %v", err)
	}
	if created.LaunchKind != "github_issue" {
		t.Fatalf("expected repo mode default to resolve to github_issue, got %+v", created)
	}

	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:        6,
		RepoMode:       "local",
		IssuePickMode:  "manual",
		PRForwardMode:  "approve",
		ForkIssuesMode: "manual",
		ImplementMode:  "manual",
		PublishTarget:  "",
	}); err != nil {
		t.Fatalf("write local settings: %v", err)
	}

	_, replayed, err := createStartUIPlannedItem(repoSlug, request)
	if err != nil {
		t.Fatalf("replay createStartUIPlannedItem after repo mode change: %v", err)
	}
	if replayed.ID != created.ID {
		t.Fatalf("expected replay after repo mode change to reuse %s, got %+v", created.ID, replayed)
	}
	if replayed.LaunchKind != created.LaunchKind {
		t.Fatalf("expected replay to return the original planned item, got %+v", replayed)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start work state: %v", err)
	}
	if len(state.PlannedItems) != 1 {
		t.Fatalf("expected one persisted planned item after repo mode replay, got %+v", state.PlannedItems)
	}
}

func TestCreateStartUIPlannedItemKeepsIdempotencyStableAcrossResolvedManualScoutDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)

	initialRequest := startUIPlannedItemRequest{
		Title:          "Audit approvals surface",
		Description:    "Capture follow-up findings",
		LaunchKind:     "manual_scout",
		ScoutRole:      uiScoutRole,
		ScoutFocus:     []string{" approvals ", "approvals", "filters"},
		IdempotencyKey: "planned-manual-scout-defaults",
	}

	_, created, err := createStartUIPlannedItem(repoSlug, initialRequest)
	if err != nil {
		t.Fatalf("createStartUIPlannedItem manual_scout: %v", err)
	}
	if created.ScoutDestination != improvementDestinationReview || created.FindingsHandling != startWorkFindingsHandlingManualReview {
		t.Fatalf("expected omitted manual-scout defaults to resolve before replay, got %+v", created)
	}

	explicitRetry := initialRequest
	explicitRetry.ScoutDestination = improvementDestinationReview
	explicitRetry.FindingsHandling = startWorkFindingsHandlingManualReview
	if !slices.Contains(startUIPlannedItemLookupFingerprints(repoSlug, explicitRetry), created.IdempotencyFingerprint) {
		t.Fatalf("expected semantically identical manual-scout retry fingerprints to include %s", created.IdempotencyFingerprint)
	}

	_, replayed, err := createStartUIPlannedItem(repoSlug, explicitRetry)
	if err != nil {
		t.Fatalf("replay createStartUIPlannedItem manual_scout with explicit defaults: %v", err)
	}
	if replayed.ID != created.ID {
		t.Fatalf("expected idempotent replay to reuse %s, got %+v", created.ID, replayed)
	}
	if replayed.ScoutDestination != created.ScoutDestination || replayed.FindingsHandling != created.FindingsHandling {
		t.Fatalf("expected replay to return the original manual scout item, got %+v", replayed)
	}
}

func TestCreateStartUIPlannedItemUsesStoredFingerprintAfterQueuedItemEdits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)

	priority := 2
	initialRequest := startUIPlannedItemRequest{
		Title:          "Audit approvals messaging",
		Description:    "Capture the original wording before editing the queue item.",
		WorkType:       workTypeFeature,
		Priority:       &priority,
		LaunchKind:     "local_work",
		IdempotencyKey: "fingerprinted-planned-item",
	}

	_, created, err := createStartUIPlannedItem(repoSlug, initialRequest)
	if err != nil {
		t.Fatalf("createStartUIPlannedItem: %v", err)
	}

	editedTitle := "Audit approvals messaging (edited in queue)"
	if _, _, err := patchStartUIPlannedItem(created.ID, startUIPlannedItemPatchRequest{
		Title: &editedTitle,
	}); err != nil {
		t.Fatalf("patchStartUIPlannedItem: %v", err)
	}

	_, replayed, err := createStartUIPlannedItem(repoSlug, initialRequest)
	if err != nil {
		t.Fatalf("replay createStartUIPlannedItem with original request: %v", err)
	}
	if replayed.ID != created.ID || replayed.Title != editedTitle {
		t.Fatalf("expected original fingerprint replay to return the edited queued item, got %+v", replayed)
	}

	editedRequest := initialRequest
	editedRequest.Title = editedTitle
	if _, _, err := createStartUIPlannedItem(repoSlug, editedRequest); err == nil {
		t.Fatal("expected edited request fingerprint to conflict with the original stored fingerprint")
	} else if _, ok := asStartUIIdempotencyConflictError(err); !ok {
		t.Fatalf("expected idempotency conflict for edited fingerprint, got %v", err)
	}
}

func TestCreateStartUIPlannedItemKeepsLegacyBlankFingerprintItemsIdempotentAfterEdits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)

	now := time.Now().UTC().Format(time.RFC3339)
	if err := writeStartWorkState(startWorkState{
		Version:        startWorkStateVersion,
		SourceRepo:     repoSlug,
		CreatedAt:      now,
		UpdatedAt:      now,
		Issues:         map[string]startWorkIssueState{},
		ServiceTasks:   map[string]startWorkServiceTask{},
		Promotions:     map[string]startWorkPromotion{},
		PromotionSkips: map[string]startWorkPromotionSkip{},
		PlannedItems: map[string]startWorkPlannedItem{
			"planned-legacy": {
				ID:             "planned-legacy",
				RepoSlug:       repoSlug,
				Title:          "Legacy queued task",
				Description:    "Original legacy description",
				WorkType:       workTypeFeature,
				LaunchKind:     "local_work",
				Priority:       2,
				IdempotencyKey: "legacy-planned-item",
				State:          startPlannedItemQueued,
				CreatedAt:      now,
				UpdatedAt:      now,
			},
		},
		TaskTemplates:  map[string]startWorkTaskTemplate{},
		ScoutJobs:      map[string]startWorkScoutJob{},
		Findings:       map[string]startWorkFinding{},
		ImportSessions: map[string]startWorkFindingImportSession{},
	}); err != nil {
		t.Fatalf("write start work state: %v", err)
	}

	editedTitle := "Legacy queued task (edited after create)"
	if _, _, err := patchStartUIPlannedItem("planned-legacy", startUIPlannedItemPatchRequest{
		Title: &editedTitle,
	}); err != nil {
		t.Fatalf("patchStartUIPlannedItem: %v", err)
	}

	priority := 2
	_, replayed, err := createStartUIPlannedItem(repoSlug, startUIPlannedItemRequest{
		Title:          "Legacy queued task",
		Description:    "Original legacy description",
		WorkType:       workTypeFeature,
		Priority:       &priority,
		LaunchKind:     "local_work",
		IdempotencyKey: "legacy-planned-item",
	})
	if err != nil {
		t.Fatalf("replay createStartUIPlannedItem for legacy blank fingerprint item: %v", err)
	}
	if replayed.ID != "planned-legacy" || replayed.Title != editedTitle {
		t.Fatalf("expected replay to reuse the edited legacy planned item, got %+v", replayed)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start work state: %v", err)
	}
	if len(state.PlannedItems) != 1 {
		t.Fatalf("expected legacy replay to avoid duplicate planned items, got %+v", state.PlannedItems)
	}
}
