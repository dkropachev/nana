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

func TestStartUIAPIFindingsRoutesPromotePersistedFinding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-22T10:00:00Z",
		Issues:     map[string]startWorkIssueState{},
		Findings: map[string]startWorkFinding{
			"finding-1": {
				ID:           "finding-1",
				RepoSlug:     repoSlug,
				SourceKind:   startWorkFindingSourceKindManualImport,
				SourceID:     "import-1",
				SourceItemID: "cand-1",
				Title:        "Clarify retry wording",
				Summary:      "The retry wording should be explicit.",
				Detail:       "Use precise copy in the queue and drawer.",
				Severity:     "medium",
				WorkType:     workTypeFeature,
				Status:       startWorkFindingStatusOpen,
				CreatedAt:    "2026-04-22T10:00:00Z",
				UpdatedAt:    "2026-04-22T10:00:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: t.TempDir(), allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/repos/" + repoSlug + "/findings")
	if err != nil {
		t.Fatalf("GET findings: %v", err)
	}
	defer response.Body.Close()
	var listPayload startUIFindingsResponse
	if err := json.NewDecoder(response.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode findings payload: %v", err)
	}
	if len(listPayload.Items) != 1 || listPayload.Items[0].ID != "finding-1" {
		t.Fatalf("unexpected findings payload: %+v", listPayload)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/findings/finding-1/promote", nil)
	if err != nil {
		t.Fatalf("new promote request: %v", err)
	}
	promoteResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST promote finding: %v", err)
	}
	defer promoteResponse.Body.Close()
	var promotePayload struct {
		State       *startWorkState      `json:"state"`
		Finding     startWorkFinding     `json:"finding"`
		PlannedItem startWorkPlannedItem `json:"planned_item"`
	}
	if err := json.NewDecoder(promoteResponse.Body).Decode(&promotePayload); err != nil {
		t.Fatalf("decode promote payload: %v", err)
	}
	if promotePayload.Finding.Status != startWorkFindingStatusPromoted || promotePayload.PlannedItem.ID == "" {
		t.Fatalf("unexpected promote payload: %+v", promotePayload)
	}
}

func TestStartUIAPIFindingsRoutesPatchAndDismissPersistedFinding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-22T10:00:00Z",
		Issues:     map[string]startWorkIssueState{},
		Findings: map[string]startWorkFinding{
			"finding-1": {
				ID:           "finding-1",
				RepoSlug:     repoSlug,
				SourceKind:   startWorkFindingSourceKindManualImport,
				SourceID:     "import-1",
				SourceItemID: "cand-1",
				Title:        "Clarify retry wording",
				Summary:      "The retry wording should be explicit.",
				Detail:       "Use precise copy in the queue and drawer.",
				Evidence:     "current UI copy",
				Severity:     "medium",
				WorkType:     workTypeFeature,
				Status:       startWorkFindingStatusOpen,
				CreatedAt:    "2026-04-22T10:00:00Z",
				UpdatedAt:    "2026-04-22T10:00:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: t.TempDir(), allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	patchResponse, err := http.DefaultClient.Do(mustJSONRequest(t, http.MethodPatch, server.URL+"/api/v1/repos/"+repoSlug+"/findings/finding-1", `{
  "title": "Clarify retry wording everywhere",
  "summary": "The queue and drawer retry wording should match.",
  "detail": "Use the same explicit retry copy across all operator surfaces.",
  "evidence": "queue + drawer copy",
  "severity": "high",
  "work_type": "bug_fix",
  "files": ["internal/gocli/start_ui_assets/app.txt"],
  "path": "internal/gocli/start_ui_assets/app.txt",
  "line": 6500,
  "route": "/approvals",
  "page": "approvals"
}`))
	if err != nil {
		t.Fatalf("PATCH finding: %v", err)
	}
	defer patchResponse.Body.Close()
	var patchPayload struct {
		Finding startWorkFinding `json:"finding"`
	}
	if err := json.NewDecoder(patchResponse.Body).Decode(&patchPayload); err != nil {
		t.Fatalf("decode patch payload: %v", err)
	}
	if patchPayload.Finding.Title != "Clarify retry wording everywhere" || patchPayload.Finding.WorkType != workTypeBugFix || patchPayload.Finding.Line != 6500 {
		t.Fatalf("unexpected patched finding: %+v", patchPayload.Finding)
	}

	dismissResponse, err := http.DefaultClient.Do(mustJSONRequest(t, http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/findings/finding-1/dismiss", ""))
	if err != nil {
		t.Fatalf("POST dismiss finding: %v", err)
	}
	defer dismissResponse.Body.Close()
	var dismissPayload struct {
		Finding startWorkFinding `json:"finding"`
	}
	if err := json.NewDecoder(dismissResponse.Body).Decode(&dismissPayload); err != nil {
		t.Fatalf("decode dismiss payload: %v", err)
	}
	if dismissPayload.Finding.Status != startWorkFindingStatusDismissed {
		t.Fatalf("unexpected dismissed finding: %+v", dismissPayload.Finding)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if state.Findings["finding-1"].Status != startWorkFindingStatusDismissed {
		t.Fatalf("expected dismissed finding in state, got %+v", state.Findings["finding-1"])
	}
}

func TestStartUIAPIFindingImportSessionRoutesListDetailPatchPromoteDrop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeFindingsTestExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\ncat >/dev/null\nprintf '{\"candidates\":[{\"candidate_id\":\"cand-1\",\"title\":\"Fix retry wording\",\"summary\":\"Clarify retry scope\",\"detail\":\"The retry label should explain whether the whole worker reruns.\",\"severity\":\"medium\",\"work_type\":\"feature\"},{\"candidate_id\":\"cand-2\",\"title\":\"Drop this\",\"summary\":\"Optional\",\"detail\":\"This candidate will be dropped.\",\"severity\":\"low\",\"work_type\":\"test_only\"}]}'\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	server := httptest.NewServer((&startUIAPI{cwd: t.TempDir(), allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	createResponse, err := http.DefaultClient.Do(mustJSONRequest(t, http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/finding-import-sessions", `{
  "file_path": "notes.md",
  "markdown": "# Findings\n\n- Retry wording"
}`))
	if err != nil {
		t.Fatalf("POST import session: %v", err)
	}
	defer createResponse.Body.Close()
	var createPayload struct {
		Session startWorkFindingImportSession `json:"session"`
	}
	if err := json.NewDecoder(createResponse.Body).Decode(&createPayload); err != nil {
		t.Fatalf("decode create payload: %v", err)
	}
	if createPayload.Session.ID == "" || len(createPayload.Session.Candidates) != 2 {
		t.Fatalf("unexpected created import session: %+v", createPayload.Session)
	}

	listResponse, err := http.Get(server.URL + "/api/v1/repos/" + repoSlug + "/finding-import-sessions")
	if err != nil {
		t.Fatalf("GET import sessions: %v", err)
	}
	defer listResponse.Body.Close()
	var listPayload startUIFindingImportSessionsResponse
	if err := json.NewDecoder(listResponse.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list payload: %v", err)
	}
	if len(listPayload.Items) != 1 || listPayload.Items[0].ID != createPayload.Session.ID {
		t.Fatalf("unexpected import session list: %+v", listPayload)
	}

	detailResponse, err := http.Get(server.URL + "/api/v1/repos/" + repoSlug + "/finding-import-sessions/" + createPayload.Session.ID)
	if err != nil {
		t.Fatalf("GET import session detail: %v", err)
	}
	defer detailResponse.Body.Close()
	var detailPayload struct {
		Session startWorkFindingImportSession `json:"session"`
	}
	if err := json.NewDecoder(detailResponse.Body).Decode(&detailPayload); err != nil {
		t.Fatalf("decode detail payload: %v", err)
	}
	if detailPayload.Session.ID != createPayload.Session.ID {
		t.Fatalf("unexpected import session detail: %+v", detailPayload.Session)
	}

	patchResponse, err := http.DefaultClient.Do(mustJSONRequest(t, http.MethodPatch, server.URL+"/api/v1/repos/"+repoSlug+"/finding-import-sessions/"+createPayload.Session.ID+"/candidates/cand-1", `{
  "title": "Fix retry wording everywhere",
  "summary": "Clarify retry scope in all views",
  "work_type": "bug_fix"
}`))
	if err != nil {
		t.Fatalf("PATCH import candidate: %v", err)
	}
	defer patchResponse.Body.Close()
	var patchPayload struct {
		Session startWorkFindingImportSession `json:"session"`
	}
	if err := json.NewDecoder(patchResponse.Body).Decode(&patchPayload); err != nil {
		t.Fatalf("decode patch candidate payload: %v", err)
	}
	if got := patchPayload.Session.Candidates[0]; got.Title != "Fix retry wording everywhere" || got.WorkType != workTypeBugFix {
		t.Fatalf("unexpected patched candidate: %+v", got)
	}

	promoteResponse, err := http.DefaultClient.Do(mustJSONRequest(t, http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/finding-import-sessions/"+createPayload.Session.ID+"/candidates/cand-1/promote", ""))
	if err != nil {
		t.Fatalf("POST promote candidate: %v", err)
	}
	defer promoteResponse.Body.Close()
	var promotePayload struct {
		Session startWorkFindingImportSession `json:"session"`
		Finding startWorkFinding              `json:"finding"`
	}
	if err := json.NewDecoder(promoteResponse.Body).Decode(&promotePayload); err != nil {
		t.Fatalf("decode promote candidate payload: %v", err)
	}
	if promotePayload.Finding.SourceKind != startWorkFindingSourceKindManualImport || promotePayload.Session.Candidates[0].Status != startWorkFindingCandidateStatusPromoted {
		t.Fatalf("unexpected promote payload: %+v", promotePayload)
	}

	dropResponse, err := http.DefaultClient.Do(mustJSONRequest(t, http.MethodPost, server.URL+"/api/v1/repos/"+repoSlug+"/finding-import-sessions/"+createPayload.Session.ID+"/candidates/cand-2/drop", ""))
	if err != nil {
		t.Fatalf("POST drop candidate: %v", err)
	}
	defer dropResponse.Body.Close()
	var dropPayload struct {
		Session startWorkFindingImportSession `json:"session"`
	}
	if err := json.NewDecoder(dropResponse.Body).Decode(&dropPayload); err != nil {
		t.Fatalf("decode drop candidate payload: %v", err)
	}
	if dropPayload.Session.Candidates[1].Status != startWorkFindingCandidateStatusDropped {
		t.Fatalf("unexpected dropped candidate: %+v", dropPayload.Session.Candidates[1])
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if len(state.ImportSessions) != 1 || len(state.Findings) != 1 {
		t.Fatalf("expected one import session and one promoted finding, got sessions=%+v findings=%+v", state.ImportSessions, state.Findings)
	}
}

func TestStartUIAppRepoControlsReferencesFindingsInboxAndMarkdownImport(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		"/api/v1/repos/${repoSlug}/findings",
		"/api/v1/repos/${repoSlug}/finding-import-sessions",
		"Import Findings from Markdown",
		"Schedule Task",
		"Findings Inbox",
		"data-task-finding-save",
		"data-task-import-candidate-save",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected app asset to contain %q", needle)
		}
	}
}

func TestStartUIAppPreservesDraftsForRefreshableForms(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		"formDrafts: {}",
		"function captureDraftField(field)",
		"function restoreDraftScope(scope, root = document)",
		"function clearDraftScope(scope)",
		"function captureDraftFocusSnapshot(root = document)",
		"function restoreDraftFocusSnapshot(snapshot, root = document)",
		"function renderAppPreservingDraftFocus()",
		"function usageFiltersDraftScope()",
		"function issuesDetailDraftScope(issue)",
		"function repoControlsIssueDraftScope()",
		"function repoControlsPlannedDraftScope()",
		"function repoSchedulerSearchDraftScope(repo)",
		"function repoSchedulerDetailDraftScope(repo, item)",
		"function taskFindingDraftScope(repo, finding)",
		"function findingImportCandidateDraftScope(repo, session, candidate)",
		`<form id="usage-filters-form" class="usage-filter-form" data-draft-scope="${escapeHTML(draftScope)}">`,
		`state.issueForm.el.setAttribute("data-draft-scope", draftScope);`,
		`state.plannedForm.el.setAttribute("data-draft-scope", draftScope);`,
		"captureDraftField(event.target);",
		"clearDraftScope(usageFiltersDraftScope());",
		"clearDraftScope(repoSchedulerSearchDraftScope(repo));",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected app asset to contain %q", needle)
		}
	}
}

func mustJSONRequest(t *testing.T, method string, url string, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}
