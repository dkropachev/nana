package gocli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildWorkItemReplyPromptOmitsEmptyDefaultsAndCompactsBody(t *testing.T) {
	prompt := buildWorkItemReplyPrompt(workItem{
		ID:         "wi-1",
		Source:     "email",
		SourceKind: "task",
		Subject:    "Reply to this thread",
		Body:       strings.Repeat("body\n", 2000),
	}, nil, "")
	if strings.Contains(prompt, "Repo: (none)") || strings.Contains(prompt, "Target URL: (none)") {
		t.Fatalf("expected empty default fields to be omitted:\n%s", prompt)
	}
	if !strings.Contains(prompt, "... [truncated]") {
		t.Fatalf("expected large body to be truncated:\n%s", prompt)
	}
}

func TestWorkItemDropSilencesAndRestoreRequeues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, created, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "mail-1",
		Subject:    "Triage this note",
		Body:       "This is only FYI.",
		Metadata: map[string]any{
			"task_mode": "reply",
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	if !created {
		t.Fatalf("expected created item")
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.LatestDraft = &workItemDraft{
		Kind:                 "reply",
		Body:                 "No response needed.",
		Summary:              "Ignore this item.",
		SuggestedDisposition: "ignore",
		Confidence:           0.95,
	}
	item.Status = workItemStatusDraftReady
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
	store.Close()

	if err := dropWorkItemByID(item.ID, "test"); err != nil {
		t.Fatalf("dropWorkItemByID: %v", err)
	}
	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail after drop: %v", err)
	}
	if detail.Item.Status != workItemStatusSilenced || !detail.Item.Hidden {
		t.Fatalf("expected silenced hidden item, got %+v", detail.Item)
	}

	visible, err := listWorkItems(workItemListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("listWorkItems visible: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("expected hidden item to be excluded, got %+v", visible)
	}

	hidden, err := listWorkItems(workItemListOptions{Limit: 10, OnlyHidden: true, IncludeHidden: true})
	if err != nil {
		t.Fatalf("listWorkItems hidden: %v", err)
	}
	if len(hidden) != 1 || hidden[0].ID != item.ID {
		t.Fatalf("expected hidden item in hidden view, got %+v", hidden)
	}

	if err := restoreWorkItemByID(item.ID, "test"); err != nil {
		t.Fatalf("restoreWorkItemByID: %v", err)
	}
	restored, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail after restore: %v", err)
	}
	if restored.Item.Status != workItemStatusDraftReady || restored.Item.Hidden {
		t.Fatalf("expected restored draft-ready item, got %+v", restored.Item)
	}
}

func TestSyncGithubWorkItemsQueuesReviewRequestAndComment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			threads := []map[string]any{
				{
					"id":         "100",
					"reason":     "review_requested",
					"updated_at": "2026-04-13T10:00:00Z",
					"subject": map[string]any{
						"title": "Refine work items",
						"url":   server.URL + "/repos/acme/widget/pulls/7",
						"type":  "PullRequest",
					},
					"repository": map[string]any{"full_name": "acme/widget", "name": "widget", "owner": map[string]any{"login": "acme"}},
				},
				{
					"id":         "101",
					"reason":     "comment",
					"updated_at": "2026-04-13T11:00:00Z",
					"subject": map[string]any{
						"title":              "Refine work items",
						"url":                server.URL + "/repos/acme/widget/pulls/7",
						"latest_comment_url": server.URL + "/repos/acme/widget/issues/comments/11",
						"type":               "PullRequest",
					},
					"repository": map[string]any{"full_name": "acme/widget", "name": "widget", "owner": map[string]any{"login": "acme"}},
				},
			}
			_ = json.NewEncoder(w).Encode(threads)
		case "/repos/acme/widget/issues/comments/11":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         11,
				"html_url":   "https://github.com/acme/widget/issues/7#issuecomment-11",
				"body":       "Please answer in the thread.",
				"updated_at": "2026-04-13T11:00:00Z",
				"user":       map[string]any{"login": "reviewer-a"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("GITHUB_API_URL", server.URL)

	result, err := syncGithubWorkItems(workItemSyncCommandOptions{Limit: 20})
	if err != nil {
		t.Fatalf("syncGithubWorkItems: %v", err)
	}
	if result.Queued != 2 || result.Created != 2 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	items, err := listWorkItems(workItemListOptions{Limit: 10, IncludeHidden: true})
	if err != nil {
		t.Fatalf("listWorkItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
	}
	kinds := []string{items[0].SourceKind, items[1].SourceKind}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "review_request") || !strings.Contains(joined, "thread_comment") {
		t.Fatalf("expected review_request and thread_comment, got %+v", kinds)
	}
	for _, item := range items {
		if item.TargetURL != "https://github.com/acme/widget/pull/7" {
			t.Fatalf("unexpected target url for %+v", item)
		}
	}
}

func TestSubmitWorkItemViaShellUsesDraftEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outputPath := filepath.Join(home, "submit.log")
	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "slack-adapter",
		SourceKind: "task",
		ExternalID: "slack-1",
		Subject:    "Send a reply",
		Body:       "Need a follow-up",
		SubmitProfile: &workItemSubmitProfile{
			Type:    "shell",
			Command: "printf '%s\\n%s\\n' \"$NANA_WORK_ITEM_BODY\" \"$NANA_WORK_ITEM_SUMMARY\" > " + outputPath,
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.LatestDraft = &workItemDraft{Kind: "reply", Body: "Adapter reply", Summary: "Reply summary"}
	item.Status = workItemStatusDraftReady
	item.LatestArtifactRoot = workItemAttemptDir(item.ID, 1)
	if err := os.MkdirAll(item.LatestArtifactRoot, 0o755); err != nil {
		t.Fatalf("mkdir attempt root: %v", err)
	}
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
	store.Close()

	submitted, err := submitWorkItemByID(item.ID, "test")
	if err != nil {
		t.Fatalf("submitWorkItemByID: %v", err)
	}
	if submitted.Status != workItemStatusSubmitted {
		t.Fatalf("expected submitted status, got %+v", submitted)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read submit output: %v", err)
	}
	if string(content) != "Adapter reply\nReply summary\n" {
		t.Fatalf("unexpected submit output: %q", content)
	}
}

func TestWorkItemCodexTargetForItemUsesManagedSourceCheckout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("mkdir source path: %v", err)
	}

	target, err := workItemCodexTargetForItem("", workItem{RepoSlug: repoSlug})
	if err != nil {
		t.Fatalf("workItemCodexTargetForItem: %v", err)
	}
	if target.RepoPath != sourcePath || target.LockKind != workItemCodexLockSource {
		t.Fatalf("unexpected Codex target: %+v", target)
	}
}

func TestWorkItemCodexTargetForItemUsesLinkedSandbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runID := "gh-linked-run"
	repoRoot := githubWorkRepoRoot("acme/widget")
	runDir := filepath.Join(repoRoot, "runs", runID)
	sandboxPath := filepath.Join(repoRoot, "sandboxes", "issue-1-"+runID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(sandboxRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox repo: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(runDir, "manifest.json"), githubWorkManifest{
		Version:         1,
		RunID:           runID,
		RepoSlug:        "acme/widget",
		RepoOwner:       "acme",
		RepoName:        "widget",
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		TargetKind:      "issue",
		TargetNumber:    1,
		TargetURL:       "https://github.com/acme/widget/issues/1",
		UpdatedAt:       ISOTimeNow(),
	}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	target, err := workItemCodexTargetForItem("", workItem{LinkedRunID: runID})
	if err != nil {
		t.Fatalf("workItemCodexTargetForItem: %v", err)
	}
	if target.RepoPath != sandboxRepoPath || target.LockKind != workItemCodexLockSandbox {
		t.Fatalf("unexpected linked-run target: %+v", target)
	}
}

func TestRunWorkItemCodexPromptBlocksWhenSourceWriteLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repoPath := filepath.Join(home, "repo")
	attemptDir := filepath.Join(home, "attempt")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("mkdir attempt dir: %v", err)
	}

	oldRunner := workItemRunManagedPrompt
	called := false
	workItemRunManagedPrompt = func(options codexManagedPromptOptions) (codexManagedPromptResult, error) {
		called = true
		return codexManagedPromptResult{}, nil
	}
	defer func() { workItemRunManagedPrompt = oldRunner }()

	lock, err := acquireSourceWriteLock(repoPath, repoAccessLockOwner{
		Backend: "test",
		RunID:   "work-item-source-writer",
		Purpose: "source-setup",
		Label:   "work-item-source-writer",
	})
	if err != nil {
		t.Fatalf("acquire source lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	_, err = runWorkItemCodexPrompt(workItemCodexTarget{RepoPath: repoPath, LockKind: workItemCodexLockSource}, attemptDir, "draft a reply", nil)
	if err == nil || !strings.Contains(err.Error(), "repo read lock busy") {
		t.Fatalf("expected source lock conflict, got %v", err)
	}
	if called {
		t.Fatalf("expected prompt runner not to be called while source lock is held")
	}
}

func TestWorkItemPauseFieldsHydrateFromMetadataAndDelayAutoRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "mail-pause-1",
		Subject:    "Wait for retry window",
		AutoRun:    true,
		Metadata: map[string]any{
			"task_mode": "reply",
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	legacyMetadata := map[string]any{
		"task_mode":    "reply",
		"pause_reason": "rate limited",
		"pause_until":  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	encoded, err := json.Marshal(legacyMetadata)
	if err != nil {
		t.Fatalf("marshal legacy metadata: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE work_items SET status = ?, metadata_json = ?, pause_reason = NULL, pause_until = NULL WHERE id = ?`,
		workItemStatusPaused, string(encoded), item.ID); err != nil {
		t.Fatalf("seed legacy paused row: %v", err)
	}
	store.Close()

	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail: %v", err)
	}
	if detail.Item.PauseReason != "rate limited" || detail.Item.PauseUntil == "" {
		t.Fatalf("expected hydrated pause fields, got %+v", detail.Item)
	}

	store, err = openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB (second): %v", err)
	}
	reloaded, err := store.readWorkItem(item.ID)
	if err != nil {
		t.Fatalf("readWorkItem after legacy rewrite: %v", err)
	}
	if reloaded.Metadata != nil {
		if _, ok := reloaded.Metadata["pause_reason"]; ok {
			t.Fatalf("expected legacy pause_reason metadata to be cleared, got %+v", reloaded.Metadata)
		}
		if _, ok := reloaded.Metadata["pause_until"]; ok {
			t.Fatalf("expected legacy pause_until metadata to be cleared, got %+v", reloaded.Metadata)
		}
	}
	runnable, err := store.listAutoRunnableWorkItems("", 10)
	store.Close()
	if err != nil {
		t.Fatalf("listAutoRunnableWorkItems: %v", err)
	}
	if len(runnable) != 0 {
		t.Fatalf("expected paused item with future retry to stay unrunnable, got %+v", runnable)
	}
}

func TestWorkItemPauseFieldsPersistExplicitlyInDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "mail-pause-2",
		Subject:    "Persist explicit pause fields",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.Status = workItemStatusPaused
	item.PauseReason = "rate limited"
	item.PauseUntil = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	item.Metadata = nil
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
	reloaded, err := store.readWorkItem(item.ID)
	store.Close()
	if err != nil {
		t.Fatalf("readWorkItem: %v", err)
	}
	if reloaded.PauseReason != "rate limited" || reloaded.PauseUntil == "" {
		t.Fatalf("expected explicit pause fields from DB, got %+v", reloaded)
	}
	if reloaded.Metadata != nil {
		if _, ok := reloaded.Metadata["pause_reason"]; ok {
			t.Fatalf("expected new rows to avoid pause_reason metadata dual-write, got %+v", reloaded.Metadata)
		}
		if _, ok := reloaded.Metadata["pause_until"]; ok {
			t.Fatalf("expected new rows to avoid pause_until metadata dual-write, got %+v", reloaded.Metadata)
		}
	}
}
