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

func TestStartUIAPIFindingsHideDeletedByDefaultAndShowWithDeletedQuery(t *testing.T) {
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
			"finding-open": {
				ID:         "finding-open",
				RepoSlug:   repoSlug,
				SourceKind: startWorkFindingSourceKindManualImport,
				SourceID:   "import-open",
				Title:      "Visible finding",
				Status:     startWorkFindingStatusOpen,
				CreatedAt:  "2026-04-22T10:00:00Z",
				UpdatedAt:  "2026-04-22T10:00:00Z",
			},
			"finding-deleted": {
				ID:         "finding-deleted",
				RepoSlug:   repoSlug,
				SourceKind: startWorkFindingSourceKindManualImport,
				SourceID:   "import-deleted",
				Title:      "Deleted finding",
				Status:     startWorkFindingStatusDeleted,
				CreatedAt:  "2026-04-20T10:00:00Z",
				UpdatedAt:  "2026-04-20T10:00:00Z",
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
	if len(listPayload.Items) != 1 || listPayload.Items[0].ID != "finding-open" {
		t.Fatalf("expected deleted findings to stay hidden by default, got %+v", listPayload.Items)
	}

	deletedResponse, err := http.Get(server.URL + "/api/v1/repos/" + repoSlug + "/findings?deleted=1")
	if err != nil {
		t.Fatalf("GET findings with deleted=1: %v", err)
	}
	defer deletedResponse.Body.Close()
	var deletedPayload startUIFindingsResponse
	if err := json.NewDecoder(deletedResponse.Body).Decode(&deletedPayload); err != nil {
		t.Fatalf("decode deleted findings payload: %v", err)
	}
	if len(deletedPayload.Items) != 2 {
		t.Fatalf("expected deleted findings to appear with opt-in query, got %+v", deletedPayload.Items)
	}
}

func TestStartUIAPIFindingsRestoreDeletedFindingAndRejectMutationsWhileDeleted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-24T10:00:00Z",
		Issues:     map[string]startWorkIssueState{},
		Findings: map[string]startWorkFinding{
			"finding-deleted": {
				ID:                "finding-deleted",
				RepoSlug:          repoSlug,
				SourceKind:        startWorkFindingSourceKindManualImport,
				SourceID:          "import-restore",
				SourceItemID:      "cand-restore",
				Title:             "Deleted finding",
				Status:            startWorkFindingStatusDeleted,
				DeletedFromStatus: startWorkFindingStatusDismissed,
				DeletedAt:         "2026-04-23T10:00:00Z",
				DismissReason:     "operator_hidden",
				CreatedAt:         "2026-04-22T10:00:00Z",
				UpdatedAt:         "2026-04-23T10:00:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: t.TempDir(), allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	patchResponse, err := http.DefaultClient.Do(mustJSONRequest(t, http.MethodPatch, server.URL+"/api/v1/repos/"+repoSlug+"/findings/finding-deleted", `{"title":"Nope"}`))
	if err != nil {
		t.Fatalf("PATCH deleted finding: %v", err)
	}
	defer patchResponse.Body.Close()
	if patchResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected deleted finding patch to fail, got %d", patchResponse.StatusCode)
	}

	promoteResponse, err := http.Post(server.URL+"/api/v1/repos/"+repoSlug+"/findings/finding-deleted/promote", "application/json", nil)
	if err != nil {
		t.Fatalf("POST promote deleted finding: %v", err)
	}
	defer promoteResponse.Body.Close()
	if promoteResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected deleted finding promote to fail, got %d", promoteResponse.StatusCode)
	}

	restoreResponse, err := http.Post(server.URL+"/api/v1/repos/"+repoSlug+"/findings/finding-deleted/restore", "application/json", nil)
	if err != nil {
		t.Fatalf("POST restore deleted finding: %v", err)
	}
	defer restoreResponse.Body.Close()
	if restoreResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected restore status: %d", restoreResponse.StatusCode)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("readStartWorkState: %v", err)
	}
	finding := state.Findings["finding-deleted"]
	if finding.Status != startWorkFindingStatusDismissed || finding.DeletedFromStatus != "" || finding.DeletedAt != "" {
		t.Fatalf("expected deleted finding to restore to dismissed, got %+v", finding)
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

func TestStartUIAppTasksPageReferencesFindingsInboxAndMarkdownImport(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		"/api/v1/tasks",
		"/api/v1/repos/${repoSlug}/findings",
		"/api/v1/repos/${repoSlug}/finding-import-sessions",
		"Markdown import failed",
		"data-task-finding-save",
		"data-task-finding-restore",
		"data-task-findings-toggle-deleted",
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
		"function clearDraftField(field)",
		"function focusScopeForField(field)",
		"function captureDraftFocusSnapshot(root = document)",
		"function restoreDraftFocusSnapshot(snapshot, root = document)",
		"function renderWithPreservedDraftFocus(renderFn, root = document)",
		"function renderAppPreservingDraftFocus()",
		"function usageFiltersDraftScope()",
		"function issuesDetailDraftScope(issue)",
		"function taskFindingDraftScope(repo, finding)",
		"function findingImportCandidateDraftScope(repo, session, candidate)",
		`<form id="usage-filters-form" class="usage-filter-form" data-draft-scope="${escapeHTML(draftScope)}">`,
		`<form id="repo-onboard-form" class="config-form" data-focus-scope="${escapeHTML(repoOnboardingFocusScope())}">`,
		`<form id="repo-config-form" class="config-form" data-focus-scope="${escapeHTML(draftScope)}" data-draft-scope="${escapeHTML(draftScope)}">`,
		`<div class="surface" data-focus-scope="${escapeHTML(workItemFixInstructionFocusScope(item))}">`,
		`<div class="config-grid" data-draft-scope="${escapeHTML(draftScope)}">`,
		`restoreDraftScope(draftScope, document.getElementById("repo-config-form"));`,
		"captureDraftField(event.target);",
		"clearDraftScope(usageFiltersDraftScope());",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected app asset to contain %q", needle)
		}
	}
}

func TestStartUIAppSyncsRerenderSensitiveDraftsOnInput(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)

	for _, needle := range []string{
		`if (configField && typeof event.target.matches === "function" && event.target.matches("input[data-config-field], textarea[data-config-field]")) {`,
		`updateConfigDraft(configField, String(event.target.value || ""));`,
		"clearDraftField(event.target);",
		`document.body.addEventListener("change", (event) => {
      if (event.target.id === "work-item-fix-instruction") {`,
		`document.body.addEventListener("input", (event) => {
      if (event.target.id !== "work-item-fix-instruction") return;`,
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected app asset to contain %q", needle)
		}
	}
}

func TestStartUIAppAvoidsRedundantManualRefreshesAndPreservesWorkItemDrafts(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)

	refreshStart := strings.Index(content, `document.getElementById("refresh-button").addEventListener("click", () => {`)
	if refreshStart == -1 {
		t.Fatalf("refresh button handler missing from app asset")
	}
	refreshEnd := strings.Index(content[refreshStart:], `document.getElementById("repo-picker-host").addEventListener("change", (event) => {`)
	if refreshEnd == -1 {
		t.Fatalf("repo picker handler missing after refresh button handler")
	}
	refreshBlock := content[refreshStart : refreshStart+refreshEnd]
	if strings.Contains(refreshBlock, `refreshCurrentView({ silent: true })`) {
		t.Fatalf("refresh button should rely on load() for current-view refreshes, got block=%s", refreshBlock)
	}

	workItemStart := strings.Index(content, `function loadWorkItemDetail(itemID, options = {}) {`)
	if workItemStart == -1 {
		t.Fatalf("loadWorkItemDetail options signature missing from app asset")
	}
	workItemEnd := strings.Index(content[workItemStart:], `function refreshOpenWorkItemDrawerIfNeeded() {`)
	if workItemEnd == -1 {
		t.Fatalf("refreshOpenWorkItemDrawerIfNeeded missing after loadWorkItemDetail")
	}
	workItemBlock := content[workItemStart : workItemStart+workItemEnd]
	for _, needle := range []string{
		`if (options.resetFixInstruction) {`,
		`loadWorkItemDetail(itemID, { resetFixInstruction: true });`,
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected app asset to contain %q", needle)
		}
	}
	if strings.Contains(workItemBlock, "state.workItemSelection.fixInstruction = \"\";\n      renderWorkItemDrawer();") {
		t.Fatalf("work item detail refresh should not clear revision instructions unconditionally, got block=%s", workItemBlock)
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
