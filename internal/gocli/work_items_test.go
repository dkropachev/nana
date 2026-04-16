package gocli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
