package gocli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStartUIAttentionWorkItemDetailActionAndBatchEndpoints(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	first, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "paused-1",
		RepoSlug:   "acme/widget",
		Subject:    "Paused reply",
		TargetURL:  "https://github.com/acme/widget/pull/11",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue first work item: %v", err)
	}
	second, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "thread_comment",
		ExternalID: "paused-2",
		RepoSlug:   "acme/widget",
		Subject:    "Paused follow-up",
		TargetURL:  "https://github.com/acme/widget/pull/11",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue second work item: %v", err)
	}
	markAttentionWorkItemPaused(t, first.ID)
	markAttentionWorkItemPaused(t, second.ID)

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/attention/items/" + first.ID)
	if err != nil {
		t.Fatalf("GET attention detail: %v", err)
	}
	defer response.Body.Close()

	var detail startUIAttentionDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Item.Kind != "work_item" {
		t.Fatalf("expected work_item detail, got %+v", detail.Item)
	}
	if !containsString(detail.Actions, "requeue_work_item") || !containsString(detail.Actions, "fix_work_item") {
		t.Fatalf("expected requeue+fix actions, got %+v", detail.Actions)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/attention/items/"+first.ID+"/actions/not-supported", nil)
	if err != nil {
		t.Fatalf("build unsupported request: %v", err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST unsupported attention action: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported action, got %d", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodPost, server.URL+"/api/v1/attention/items/"+first.ID+"/actions/requeue_work_item", nil)
	if err != nil {
		t.Fatalf("build requeue request: %v", err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST attention action: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for requeue action, got %d", response.StatusCode)
	}
	if item := readAttentionTestWorkItem(t, first.ID); item.Status != workItemStatusQueued {
		t.Fatalf("expected first item to be requeued, got %+v", item)
	}

	payload, err := json.Marshal(startUIAttentionBatchRequest{
		Action:  "requeue_work_item",
		ItemIDs: []string{second.ID, "missing-item"},
	})
	if err != nil {
		t.Fatalf("marshal batch payload: %v", err)
	}
	response, err = http.Post(server.URL+"/api/v1/attention/batch", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST attention batch: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for attention batch, got %d", response.StatusCode)
	}
	var batch startUIAttentionBatchResponse
	if err := json.NewDecoder(response.Body).Decode(&batch); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if batch.SuccessCount != 1 || batch.FailureCount != 1 {
		t.Fatalf("expected partial success batch response, got %+v", batch)
	}
	if item := readAttentionTestWorkItem(t, second.ID); item.Status != workItemStatusQueued {
		t.Fatalf("expected second item to be requeued, got %+v", item)
	}
}

func TestStartUIAttentionImportCandidatePromoteActionEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	repoSlug := "acme/widget"
	repoPath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, repoPath)
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{
		Version:       6,
		RepoMode:      "local",
		IssuePickMode: "manual",
		PRForwardMode: "approve",
	}); err != nil {
		t.Fatalf("write repo settings: %v", err)
	}

	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeFindingsTestExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\ncat >/dev/null\nprintf '{\"candidates\":[{\"candidate_id\":\"cand-1\",\"title\":\"Fix retry wording\",\"summary\":\"Clarify retry scope\",\"detail\":\"The retry label should explain whether the whole worker reruns.\",\"severity\":\"medium\",\"work_type\":\"feature\"}]}'\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	session, err := createStartUIFindingImportSession(repoSlug, "findings.md", "# Findings\n\n- Retry wording")
	if err != nil {
		t.Fatalf("createStartUIFindingImportSession: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/attention/items/"+session.ID+":cand-1/actions/promote_import_candidate", nil)
	if err != nil {
		t.Fatalf("new promote request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST promote import candidate: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for promote import candidate, got %d", response.StatusCode)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("readStartWorkState: %v", err)
	}
	if len(state.Findings) != 1 {
		t.Fatalf("expected promoted finding, got %+v", state.Findings)
	}
}

func markAttentionWorkItemPaused(t *testing.T, itemID string) {
	t.Helper()
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	item, err := store.readWorkItem(itemID)
	if err != nil {
		t.Fatalf("readWorkItem(%s): %v", itemID, err)
	}
	item.Status = workItemStatusPaused
	item.PauseReason = "rate limited"
	item.PauseUntil = "2026-04-22T13:15:00Z"
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem(%s): %v", itemID, err)
	}
}

func readAttentionTestWorkItem(t *testing.T, itemID string) workItem {
	t.Helper()
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	item, err := store.readWorkItem(itemID)
	if err != nil {
		t.Fatalf("readWorkItem(%s): %v", itemID, err)
	}
	return item
}

func containsString(items []string, expected string) bool {
	taken := expected
	for _, item := range items {
		if item == taken {
			return true
		}
	}
	return false
}
