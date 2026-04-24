package gocli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const startUIAppBootSequence = `
  initLogDrawer();
  initWorkItemDrawer();
  applyHashState();
  state.hashReady = true;
  attachHandlers();
  load().then(connectEvents);
})();
`

func startUITestWriteInstrumentedApp(t *testing.T, exportSequence string) string {
	t.Helper()

	appSource, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read start UI app asset: %v", err)
	}

	sourceText := string(appSource)
	if !strings.HasSuffix(sourceText, startUIAppBootSequence) {
		t.Fatalf("start UI app asset no longer ends with the expected boot sequence")
	}
	sourceText = strings.TrimSuffix(sourceText, startUIAppBootSequence) + exportSequence

	tempDir := t.TempDir()
	instrumentedPath := filepath.Join(tempDir, "app-under-test.js")
	if err := os.WriteFile(instrumentedPath, []byte(sourceText), 0o644); err != nil {
		t.Fatalf("write instrumented app asset: %v", err)
	}
	return instrumentedPath
}

func startUITestRunNodeHarness(t *testing.T, instrumentedPath string, harness string) []byte {
	t.Helper()

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for start UI node regressions")
	}

	harnessPath := filepath.Join(t.TempDir(), "harness.js")
	if err := os.WriteFile(harnessPath, []byte(harness), 0o644); err != nil {
		t.Fatalf("write node harness: %v", err)
	}

	output, err := exec.Command(nodePath, harnessPath, instrumentedPath).CombinedOutput()
	if err != nil {
		t.Fatalf("run node harness: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	return output
}

func TestStartUIInvestigationListRefreshPreservesExplicitLegacySelection(t *testing.T) {
	instrumentedPath := startUITestWriteInstrumentedApp(t, `
  renderInvestigationsPage = function() {};
  globalThis.__NANA_TEST_HOOKS = {
    state,
    loadInvestigations,
  };
})();
`)

	output := startUITestRunNodeHarness(t, instrumentedPath, `const calls = [];
function jsonResponse(payload) {
  return {
    ok: true,
    statusText: "OK",
    headers: { get(name) { return name === "Content-Type" ? "application/json" : ""; } },
    json() { return Promise.resolve(payload); },
    text() { return Promise.resolve(""); },
  };
}

global.window = {
  NANA_API_BASE: "",
  location: { hash: "#view=investigations&task=work-run%3Alegacy-run", pathname: "/ui" },
  history: {
    replaceCalls: [],
    replaceState(_state, _title, targetURL) {
      this.replaceCalls.push(targetURL);
      if (typeof targetURL !== "string") {
        return;
      }
      if (targetURL.startsWith("#")) {
        global.window.location.hash = targetURL;
        return;
      }
      global.window.location.pathname = targetURL;
      global.window.location.hash = "";
    },
  },
};
global.document = { getElementById() { return null; } };
global.fetch = (url) => {
  calls.push(url);
  if (url === "/api/v1/investigations") {
    return Promise.resolve(jsonResponse({ items: [{ run_id: "investigate-123", query: "fresh run" }] }));
  }
  if (url === "/api/v1/investigations/work-run%3Alegacy-run") {
    return Promise.resolve(jsonResponse({
      summary: {
        run_id: "work-run:legacy-run",
        query: "legacy task alias",
        status: "completed",
        updated_at: "2026-04-24T00:00:00Z",
      },
    }));
  }
  throw new Error("unexpected fetch " + url);
};
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

hooks.state.currentView = "investigations";
hooks.state.hashReady = true;
hooks.state.investigations.selectedRunID = "work-run:legacy-run";

hooks.loadInvestigations({ silent: true }).then(() => {
  process.stdout.write(JSON.stringify({
    selected_run_id: hooks.state.investigations.selectedRunID,
    detail_run_id: hooks.state.investigations.detail && hooks.state.investigations.detail.summary && hooks.state.investigations.detail.summary.run_id || "",
    fetch_calls: calls,
    replace_calls: global.window.history.replaceCalls,
    location_hash: global.window.location.hash,
  }));
}).catch((error) => {
  console.error(error && error.stack ? error.stack : String(error));
  process.exit(1);
});
`)

	var got struct {
		SelectedRunID string   `json:"selected_run_id"`
		DetailRunID   string   `json:"detail_run_id"`
		FetchCalls    []string `json:"fetch_calls"`
		ReplaceCalls  []string `json:"replace_calls"`
		LocationHash  string   `json:"location_hash"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if got.SelectedRunID != "work-run:legacy-run" || got.DetailRunID != "work-run:legacy-run" {
		t.Fatalf("expected explicit legacy investigation selection to survive list refresh, got %+v", got)
	}
	if len(got.FetchCalls) != 2 ||
		got.FetchCalls[0] != "/api/v1/investigations" ||
		got.FetchCalls[1] != "/api/v1/investigations/work-run%3Alegacy-run" {
		t.Fatalf("expected investigations refresh to request legacy detail after the list, got %+v", got.FetchCalls)
	}
	if len(got.ReplaceCalls) != 0 || got.LocationHash != "#view=investigations&task=work-run%3Alegacy-run" {
		t.Fatalf("expected explicit legacy investigation hash to remain stable, got %+v", got)
	}
}

func TestStartUIRenderLogDrawerPreservesScrollOnlyForSameFileRefresh(t *testing.T) {
	instrumentedPath := startUITestWriteInstrumentedApp(t, `
  globalThis.__NANA_TEST_HOOKS = {
    state,
    renderLogDrawer,
  };
})();
`)

	output := startUITestRunNodeHarness(t, instrumentedPath, `function makeHost() {
  return {
    _html: "",
    _logContent: null,
    set innerHTML(value) {
      this._html = value;
      this._logContent = String(value).includes('class="log-content"')
        ? { scrollTop: 0, scrollLeft: 0 }
        : null;
    },
    get innerHTML() {
      return this._html;
    },
    querySelector(selector) {
      if (selector === ".log-content") {
        return this._logContent;
      }
      return null;
    },
  };
}

const host = makeHost();
global.window = { NANA_API_BASE: "", location: { hash: "", pathname: "/ui" }, history: { replaceState() {} } };
global.document = {
  getElementById(id) {
    return id === "log-drawer-content" ? host : null;
  },
};
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

hooks.state.logSelection.runID = "run-1";
hooks.state.logSelection.filePath = "main.log";
hooks.state.logSelection.meta = {
  summary: {
    run_id: "run-1",
    backend: "local",
    work_type: "feature",
    updated_at: "2026-04-24T00:00:00Z",
  },
  files: [{ path: "main.log" }, { path: "other.log" }],
};
hooks.state.logSelection.detail = {
  summary: {
    run_id: "run-1",
    backend: "local",
    work_type: "feature",
    updated_at: "2026-04-24T00:00:00Z",
  },
  local_manifest: {
    sandbox_path: "/tmp/sandbox",
    sandbox_repo_path: "/tmp/sandbox/repo",
    current_iteration: 1,
  },
};
hooks.state.logSelection.content = "before";

hooks.renderLogDrawer();
host.querySelector(".log-content").scrollTop = 133;

hooks.state.logSelection.content = "after";
hooks.renderLogDrawer();
const sameFileScroll = host.querySelector(".log-content").scrollTop;

host.querySelector(".log-content").scrollTop = 222;
hooks.state.logSelection.filePath = "other.log";
hooks.state.logSelection.content = "other";
hooks.renderLogDrawer();
const differentFileScroll = host.querySelector(".log-content").scrollTop;

process.stdout.write(JSON.stringify({
  same_file_scroll: sameFileScroll,
  different_file_scroll: differentFileScroll,
  scroll_identity: hooks.state.logSelection.scrollIdentity,
}));
`)

	var got struct {
		SameFileScroll      int    `json:"same_file_scroll"`
		DifferentFileScroll int    `json:"different_file_scroll"`
		ScrollIdentity      string `json:"scroll_identity"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if got.SameFileScroll != 133 {
		t.Fatalf("expected same-file log refresh to preserve scroll, got %+v", got)
	}
	if got.DifferentFileScroll != 0 || got.ScrollIdentity != "run-1:other.log" {
		t.Fatalf("expected log drawer to reset scroll after file changes, got %+v", got)
	}
}

func TestStartUIRepoConfigSaveDisablesInputsAndIgnoresInFlightEdits(t *testing.T) {
	instrumentedPath := startUITestWriteInstrumentedApp(t, `
  globalThis.__NANA_TEST_HOOKS = {
    state,
    resetConfigEditorForRepo,
    saveConfigDraft,
    updateConfigDraft,
  };
})();
`)

	output := startUITestRunNodeHarness(t, instrumentedPath, `function jsonResponse(payload) {
  return {
    ok: true,
    statusText: "OK",
    headers: { get(name) { return name === "Content-Type" ? "application/json" : ""; } },
    json() { return Promise.resolve(payload); },
    text() { return Promise.resolve(""); },
  };
}

function makeHost() {
  return {
    innerHTML: "",
    querySelectorAll() {
      return [];
    },
  };
}

const root = makeHost();
const liveRegion = { textContent: "" };
let resolveFetch;
const fetchCalls = [];

global.window = {
  NANA_API_BASE: "",
  location: { hash: "", pathname: "/ui" },
  history: { replaceState() {} },
  addEventListener() {},
  setTimeout,
};
global.HTMLInputElement = function HTMLInputElement() {};
global.HTMLTextAreaElement = function HTMLTextAreaElement() {};
global.HTMLSelectElement = function HTMLSelectElement() {};
global.document = {
  activeElement: null,
  getElementById(id) {
    if (id === "repo-config-root") return root;
    if (id === "app-live-region") return liveRegion;
    return null;
  },
  querySelectorAll() {
    return [];
  },
};
global.fetch = (url, options = {}) => {
  fetchCalls.push({
    url,
    method: options.method || "GET",
    body: options.body ? JSON.parse(options.body) : null,
  });
  return new Promise((resolve) => {
    resolveFetch = resolve;
  });
};
global.Teryx = { toast() {} };
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

const repo = {
  repo_slug: "acme/widget",
  repo_mode: "fork",
  issue_pick_mode: "manual",
  pr_forward_mode: "approve",
  fork_issues_mode: "manual",
  implement_mode: "manual",
  publish_target: "fork",
  scout_catalog: [{
    role: "improvement-scout",
    config_key: "improvement",
    display_label: "Improvement Scout",
    default_schedule: "when_resolved",
    default_session_limit: 0,
    supports_session_limit: false,
  }],
  scouts_by_role: {
    improvement: {
      enabled: true,
      mode: "manual",
      schedule: "when_resolved",
      issue_destination: "local",
      fork_repo: "",
      labels: ["qa"],
    },
  },
};

hooks.state.selectedRepo = repo.repo_slug;
hooks.state.overview = { repos: [repo], scout_catalog: repo.scout_catalog };
hooks.state.repoList = { items: [] };
hooks.state.pageSignature = "repo:" + repo.repo_slug + ":config";
hooks.resetConfigEditorForRepo(repo);
hooks.updateConfigDraft("issue_pick_mode", "auto", { rerender: false });
hooks.saveConfigDraft(repo);

const savingMarkup = root.innerHTML;
const draftBeforeBlockedEdit = hooks.state.configEditor.draft.pr_forward_mode;
hooks.updateConfigDraft("pr_forward_mode", "auto", { rerender: false });
const draftAfterBlockedEdit = hooks.state.configEditor.draft.pr_forward_mode;

resolveFetch(jsonResponse({
  repo: {
    ...repo,
    issue_pick_mode: "auto",
    pr_forward_mode: "approve",
  },
}));

setTimeout(() => {
  process.stdout.write(JSON.stringify({
    fetch_calls: fetchCalls,
    saving: hooks.state.configEditor.saving,
    draft_before_blocked_edit: draftBeforeBlockedEdit,
    draft_after_blocked_edit: draftAfterBlockedEdit,
    overview_issue_pick_mode: hooks.state.overview && hooks.state.overview.repos && hooks.state.overview.repos[0] && hooks.state.overview.repos[0].issue_pick_mode || "",
    final_issue_pick_mode: hooks.state.configEditor.draft && hooks.state.configEditor.draft.issue_pick_mode || "",
    final_pr_forward_mode: hooks.state.configEditor.draft && hooks.state.configEditor.draft.pr_forward_mode || "",
    has_disabled_pr_forward: /name="pr_forward_mode"[^>]*disabled/.test(savingMarkup),
    has_disabled_issue_pick: /name="issue_pick_mode"[^>]*disabled/.test(savingMarkup),
    has_disabled_reset: /data-config-reset="true"[^>]*disabled/.test(savingMarkup),
  }));
}, 0);
`)

	var got struct {
		FetchCalls []struct {
			URL    string `json:"url"`
			Method string `json:"method"`
			Body   struct {
				IssuePickMode string `json:"issue_pick_mode"`
				PRForwardMode string `json:"pr_forward_mode"`
			} `json:"body"`
		} `json:"fetch_calls"`
		Saving               bool   `json:"saving"`
		DraftBeforeBlocked   string `json:"draft_before_blocked_edit"`
		DraftAfterBlocked    string `json:"draft_after_blocked_edit"`
		OverviewIssuePick    string `json:"overview_issue_pick_mode"`
		FinalIssuePickMode   string `json:"final_issue_pick_mode"`
		FinalPRForwardMode   string `json:"final_pr_forward_mode"`
		HasDisabledPRForward bool   `json:"has_disabled_pr_forward"`
		HasDisabledIssuePick bool   `json:"has_disabled_issue_pick"`
		HasDisabledReset     bool   `json:"has_disabled_reset"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if len(got.FetchCalls) != 1 || got.FetchCalls[0].URL != "/api/v1/repos/acme/widget/settings" || got.FetchCalls[0].Method != "PATCH" {
		t.Fatalf("expected one PATCH repo config request, got %+v", got.FetchCalls)
	}
	if got.FetchCalls[0].Body.IssuePickMode != "auto" || got.FetchCalls[0].Body.PRForwardMode != "approve" {
		t.Fatalf("expected save payload to snapshot the pre-submit draft, got %+v", got.FetchCalls[0].Body)
	}
	if got.Saving {
		t.Fatalf("expected save flow to settle after the mocked response, got %+v", got)
	}
	if got.DraftBeforeBlocked != "approve" || got.DraftAfterBlocked != "approve" {
		t.Fatalf("expected in-flight repo config edits to be ignored while saving, got %+v", got)
	}
	if got.OverviewIssuePick != "auto" {
		t.Fatalf("expected save response to update the repo summary snapshot, got %+v", got)
	}
	if got.FinalPRForwardMode != "approve" {
		t.Fatalf("expected blocked in-flight edit to stay out of the editor after save, got %+v", got)
	}
	if !got.HasDisabledPRForward || !got.HasDisabledIssuePick || !got.HasDisabledReset {
		t.Fatalf("expected repo config inputs and reset button to render disabled while saving, got %+v", got)
	}
}

func TestStartUILoadInvestigationDetailClearsStaleDetailDuringSwitchAndFailure(t *testing.T) {
	instrumentedPath := startUITestWriteInstrumentedApp(t, `
  renderInvestigationsPage = function() {
    renderTaskInvestigationDetail(state.investigations.detail);
  };
  globalThis.__NANA_TEST_HOOKS = {
    state,
    loadInvestigationDetail,
  };
})();
`)

	output := startUITestRunNodeHarness(t, instrumentedPath, `function makeHistory() {
  return {
    replaceCalls: [],
    replaceState(_state, _title, targetURL) {
      this.replaceCalls.push(targetURL);
      if (typeof targetURL !== "string") {
        return;
      }
      if (targetURL.startsWith("#")) {
        global.window.location.hash = targetURL;
        return;
      }
      global.window.location.pathname = targetURL;
      global.window.location.hash = "";
    },
  };
}

const detailHost = { innerHTML: "" };
const fetchCalls = [];
let rejectDetail = null;

global.window = {
  NANA_API_BASE: "",
  location: { hash: "#view=investigations&task=run-a", pathname: "/ui" },
  history: makeHistory(),
};
global.document = {
  getElementById(id) {
    return id === "investigation-detail" ? detailHost : null;
  },
};
global.fetch = (url) => {
  fetchCalls.push(url);
  return new Promise((_resolve, reject) => {
    rejectDetail = reject;
  });
};
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

hooks.state.currentView = "investigations";
hooks.state.hashReady = true;
hooks.state.investigations.selectedRunID = "run-a";
hooks.state.investigations.detail = {
  summary: {
    run_id: "run-a",
    query: "stale run",
  },
};

const pending = hooks.loadInvestigationDetail("run-b", { silent: true });
const interim = {
  selected_run_id: hooks.state.investigations.selectedRunID,
  detail_run_id: hooks.state.investigations.detail && hooks.state.investigations.detail.summary && hooks.state.investigations.detail.summary.run_id || "",
  detail_loading: hooks.state.investigations.detailLoading,
  error: hooks.state.investigations.error,
  html: detailHost.innerHTML,
  location_hash: global.window.location.hash,
};

rejectDetail(new Error("detail failed"));
pending.then(() => {
  process.stdout.write(JSON.stringify({
    interim,
    final: {
      selected_run_id: hooks.state.investigations.selectedRunID,
      detail_run_id: hooks.state.investigations.detail && hooks.state.investigations.detail.summary && hooks.state.investigations.detail.summary.run_id || "",
      detail_loading: hooks.state.investigations.detailLoading,
      error: hooks.state.investigations.error,
      html: detailHost.innerHTML,
      location_hash: global.window.location.hash,
    },
    fetch_calls: fetchCalls,
  }));
}).catch((error) => {
  console.error(error && error.stack ? error.stack : String(error));
  process.exit(1);
});
`)

	var got struct {
		Interim struct {
			SelectedRunID string `json:"selected_run_id"`
			DetailRunID   string `json:"detail_run_id"`
			DetailLoading bool   `json:"detail_loading"`
			Error         string `json:"error"`
			HTML          string `json:"html"`
			LocationHash  string `json:"location_hash"`
		} `json:"interim"`
		Final struct {
			SelectedRunID string `json:"selected_run_id"`
			DetailRunID   string `json:"detail_run_id"`
			DetailLoading bool   `json:"detail_loading"`
			Error         string `json:"error"`
			HTML          string `json:"html"`
			LocationHash  string `json:"location_hash"`
		} `json:"final"`
		FetchCalls []string `json:"fetch_calls"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if got.Interim.SelectedRunID != "run-b" || got.Interim.DetailRunID != "" || !got.Interim.DetailLoading {
		t.Fatalf("expected pending investigation switch to clear stale detail and select the new run, got %+v", got.Interim)
	}
	if got.Interim.Error != "" || !strings.Contains(got.Interim.HTML, "Loading investigation detail") {
		t.Fatalf("expected pending investigation switch to render a loading state, got %+v", got.Interim)
	}
	if got.Interim.LocationHash != "#view=investigations&task=run-b" {
		t.Fatalf("expected pending investigation switch to sync the hash to the new run, got %+v", got.Interim)
	}
	if got.Final.SelectedRunID != "run-b" || got.Final.DetailRunID != "" || got.Final.DetailLoading {
		t.Fatalf("expected failed investigation switch to keep the new selection without stale detail, got %+v", got.Final)
	}
	if got.Final.Error != "detail failed" || !strings.Contains(got.Final.HTML, "detail failed") || strings.Contains(got.Final.HTML, "stale run") {
		t.Fatalf("expected failed investigation switch to replace stale detail with the error state, got %+v", got.Final)
	}
	if len(got.FetchCalls) != 1 || got.FetchCalls[0] != "/api/v1/investigations/run-b" {
		t.Fatalf("expected one investigation detail request for the new run, got %+v", got.FetchCalls)
	}
}

func TestStartUIRenderInvestigationsPageHighlightsSelectedRunIDWhileDetailLags(t *testing.T) {
	instrumentedPath := startUITestWriteInstrumentedApp(t, `
  let investigationSelections = [];
  ensureTaskFindingsState = function() {};
  ensureFindingImportsState = function() {};
  ensureBodyStructure = function() {};
  renderTaskInvestigationDetail = function() {};
  renderTaskFindingDetail = function() {};
  renderFindingImportDetail = function() {};
  taskScheduledRows = function() { return []; };
  taskFindingRows = function() { return []; };
  findingImportSessionRows = function() { return []; };
  findingImportCandidateRows = function() { return []; };
  selectedTaskFinding = function() { return null; };
  selectedFindingImportSession = function() { return null; };
  selectedFindingImportCandidate = function() { return null; };
  renderGrid = function(target, columns, data, options) {
    if (target !== "investigations-grid") {
      return;
    }
    investigationSelections = data.map((row) => options.rowSelect(row));
  };
  globalThis.__NANA_TEST_HOOKS = {
    state,
    renderInvestigationsPage,
    getInvestigationSelections() {
      return investigationSelections;
    },
  };
})();
`)

	output := startUITestRunNodeHarness(t, instrumentedPath, `global.window = {
  NANA_API_BASE: "",
  location: { hash: "", pathname: "/ui" },
  history: { replaceState() {} },
};
global.document = { getElementById() { return null; } };
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

hooks.state.investigations.items = [
  { run_id: "run-a", repo_slug: "acme/ui", status: "completed", overall_status: "completed", query: "stale run", updated_at: "2026-04-24T00:00:00Z" },
  { run_id: "run-b", repo_slug: "acme/ui", status: "running", overall_status: "active", query: "pending run", updated_at: "2026-04-24T00:00:01Z" },
];
hooks.state.investigations.selectedRunID = "run-b";
hooks.state.investigations.detail = {
  summary: {
    run_id: "run-a",
  },
};

hooks.renderInvestigationsPage();
process.stdout.write(JSON.stringify({ selections: hooks.getInvestigationSelections() }));
`)

	var got struct {
		Selections []struct {
			Kind     string `json:"kind"`
			Value    string `json:"value"`
			Selected bool   `json:"selected"`
		} `json:"selections"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if len(got.Selections) != 2 {
		t.Fatalf("expected two captured investigation selections, got %+v", got.Selections)
	}
	if got.Selections[0].Value != "run-a" || got.Selections[0].Selected {
		t.Fatalf("expected stale detail run to be unselected, got %+v", got.Selections)
	}
	if got.Selections[1].Value != "run-b" || !got.Selections[1].Selected {
		t.Fatalf("expected selectedRunID to drive the highlighted investigation row, got %+v", got.Selections)
	}
}

func TestStartUITaskComposerReconcilesRepoScopedScoutRolesBeforeSubmit(t *testing.T) {
	instrumentedPath := startUITestWriteInstrumentedApp(t, `
  globalThis.__NANA_TEST_HOOKS = {
    state,
    reconcileTaskComposerScoutRole,
  };
})();
`)

	output := startUITestRunNodeHarness(t, instrumentedPath, `global.window = {
  NANA_API_BASE: "",
  location: { hash: "", pathname: "/ui" },
  history: { replaceState() {} },
};
global.document = {
  getElementById() { return null; },
  querySelectorAll() { return []; },
};
global.HTMLInputElement = function HTMLInputElement() {};
global.HTMLTextAreaElement = function HTMLTextAreaElement() {};
global.HTMLSelectElement = function HTMLSelectElement() {};
global.fetch = () => Promise.resolve({
  ok: true,
  statusText: "OK",
  headers: { get() { return "application/json"; } },
  json() { return Promise.resolve({}); },
  text() { return Promise.resolve(""); },
});
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

const coreRepo = {
  repo_slug: "acme/core",
  scout_catalog: [{
    role: "improvement-scout",
    config_key: "improvement",
    display_label: "Improvement Scout",
    default_schedule: "when_resolved",
    default_session_limit: 0,
    supports_session_limit: false,
  }],
};

hooks.state.taskComposer.repoSlug = coreRepo.repo_slug;
hooks.state.taskComposer.kind = "manual_scout";
hooks.state.taskComposer.scoutRole = "ui-scout";
hooks.state.taskComposer.scoutSessionLimit = "6";

const clamped = hooks.reconcileTaskComposerScoutRole(coreRepo);
process.stdout.write(JSON.stringify({
  role: clamped.role,
  session_limit: hooks.state.taskComposer.scoutSessionLimit,
}));
`)

	var got struct {
		Role         string `json:"role"`
		SessionLimit string `json:"session_limit"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if got.Role != "improvement-scout" || got.SessionLimit != "" {
		t.Fatalf("expected repo-scoped scout reconciliation to clamp the role and clear the hidden limit, got %+v", got)
	}

	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		`const scoutComposerSelection = reconcileTaskComposerScoutRole(repo);`,
		`const sessionLimitInput = scoutComposerSelection.meta.supportsSessionLimit`,
		`if (scoutComposerSelection.meta.supportsSessionLimit) {`,
		`payload.scout_session_limit = Number.parseInt(String(state.taskComposer.scoutSessionLimit || "0"), 10) || 0;`,
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected manual scout submit flow to keep repo-scoped scout-role revalidation via %q", needle)
		}
	}
}

func TestStartUITaskTemplateSelectionAppliesFixedScoutHints(t *testing.T) {
	instrumentedPath := startUITestWriteInstrumentedApp(t, `
  renderInvestigationsPage = function() {};
  globalThis.__NANA_TEST_HOOKS = {
    state,
    loadTaskTemplates,
    applyTaskComposerTemplate,
  };
})();
`)

	output := startUITestRunNodeHarness(t, instrumentedPath, `function jsonResponse(payload) {
  return {
    ok: true,
    statusText: "OK",
    headers: { get(name) { return name === "Content-Type" ? "application/json" : ""; } },
    json() { return Promise.resolve(payload); },
    text() { return Promise.resolve(""); },
  };
}

global.window = {
  NANA_API_BASE: "",
  location: { hash: "", pathname: "/ui" },
  history: { replaceState() {} },
};
global.document = {
  getElementById() { return null; },
  querySelectorAll() { return []; },
};
global.fetch = (url) => {
  if (url === "/api/v1/tasks/templates?repo_slug=acme%2Fcore") {
    return Promise.resolve(jsonResponse({
      items: [
        { id: "template:custom", name: "Custom task", built_in: true },
        {
          id: "template:scout:ui-scout",
          name: "UI Scout",
          built_in: true,
          launch_kind_hint: "manual_scout",
          scout_role_hint: "ui-scout",
          scout_prompt_preview: "Audit the current UI surface.",
          default_priority: 3,
        },
      ],
    }));
  }
  throw new Error("unexpected fetch " + url);
};
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

hooks.state.overview = {
  repos: [{
    repo_slug: "acme/core",
    scout_catalog: [{
      role: "ui-scout",
      config_key: "ui",
      display_label: "UI Scout",
      default_schedule: "when_resolved",
      default_session_limit: 4,
      supports_session_limit: true,
    }],
  }],
};
hooks.state.taskComposer.repoSlug = "acme/core";
hooks.state.taskComposer.kind = "coding";

hooks.loadTaskTemplates({ silent: true }).then(() => {
  const scoutTemplate = hooks.state.investigations.templates.find((item) => item.id === "template:scout:ui-scout");
  if (!scoutTemplate) {
    throw new Error("scout template was not loaded");
  }
  hooks.applyTaskComposerTemplate(scoutTemplate);
  process.stdout.write(JSON.stringify({
    template_id: hooks.state.taskComposer.templateID,
    kind: hooks.state.taskComposer.kind,
    scout_role: hooks.state.taskComposer.scoutRole,
    session_limit: hooks.state.taskComposer.scoutSessionLimit,
    template_count: hooks.state.investigations.templates.length,
  }));
}).catch((error) => {
  console.error(error && error.stack ? error.stack : String(error));
  process.exit(1);
});
`)

	var got struct {
		TemplateID    string `json:"template_id"`
		Kind          string `json:"kind"`
		ScoutRole     string `json:"scout_role"`
		SessionLimit  string `json:"session_limit"`
		TemplateCount int    `json:"template_count"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if got.TemplateCount != 2 {
		t.Fatalf("expected fixed scout templates to load alongside custom, got %+v", got)
	}
	if got.TemplateID != "template:scout:ui-scout" || got.Kind != "manual_scout" || got.ScoutRole != "ui-scout" || got.SessionLimit != "4" {
		t.Fatalf("expected fixed scout template selection to seed manual scout hints, got %+v", got)
	}
}
