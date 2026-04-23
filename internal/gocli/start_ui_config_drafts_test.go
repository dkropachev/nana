package gocli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartUIRepoConfigDraftReplayKeepsDependentFieldsNormalizedAndDropsObsoleteScoutRoles(t *testing.T) {
	t.Helper()

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for repo config draft replay coverage")
	}

	appPath := filepath.Join("start_ui_assets", "app.txt")
	appSource, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatalf("read start UI app asset: %v", err)
	}

	const bootSequence = `
  initLogDrawer();
  initWorkItemDrawer();
  applyHashState();
  state.hashReady = true;
  attachHandlers();
  load().then(connectEvents);
})();
`
	const exportSequence = `
  globalThis.__NANA_TEST_HOOKS = {
    state,
    clearDraftScope,
    ensureConfigEditor,
    mergeRepoSummary,
    resetConfigEditorForRepo,
    syncConfigEditorTransientDrafts,
    updateConfigDraft,
  };
})();
`

	sourceText := string(appSource)
	if !strings.HasSuffix(sourceText, bootSequence) {
		t.Fatalf("start UI app asset no longer ends with the expected boot sequence")
	}
	sourceText = strings.TrimSuffix(sourceText, bootSequence) + exportSequence

	tempDir := t.TempDir()
	instrumentedPath := filepath.Join(tempDir, "app-under-test.js")
	if err := os.WriteFile(instrumentedPath, []byte(sourceText), 0o644); err != nil {
		t.Fatalf("write instrumented app asset: %v", err)
	}

	harnessPath := filepath.Join(tempDir, "harness.js")
	harness := `global.window = { NANA_API_BASE: "" };
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

function scoutCatalog(keys) {
  const catalog = {
    improvement: {
      role: "improvement-scout",
      config_key: "improvement",
      display_label: "Improvement Scout",
      default_schedule: "when_resolved",
      default_session_limit: 0,
      supports_session_limit: false,
    },
    enhancement: {
      role: "enhancement-scout",
      config_key: "enhancement",
      display_label: "Enhancement Scout",
      default_schedule: "when_resolved",
      default_session_limit: 0,
      supports_session_limit: false,
    },
    ui: {
      role: "ui-scout",
      config_key: "ui",
      display_label: "UI Scout",
      default_schedule: "when_resolved",
      default_session_limit: 4,
      supports_session_limit: true,
    },
  };
  return keys.map((key) => ({ ...catalog[key] }));
}

function makeRepo(catalogKeys = ['improvement', 'enhancement', 'ui']) {
  const scoutsByRole = {
    improvement: {
      enabled: true,
      mode: "manual",
      schedule: "when_resolved",
      issue_destination: "local",
      fork_repo: "",
      labels: ["ui"],
    },
    enhancement: {
      enabled: false,
      mode: "manual",
      schedule: "when_resolved",
      issue_destination: "local",
      fork_repo: "",
      labels: [],
    },
  };
  if (catalogKeys.includes('ui')) {
    scoutsByRole.ui = {
      enabled: true,
      mode: "manual",
      schedule: "when_resolved",
      issue_destination: "local",
      fork_repo: "",
      labels: ["qa"],
      session_limit: 4,
    };
  }
  return {
    repo_slug: "acme/widget",
    repo_mode: "fork",
    issue_pick_mode: "auto",
    pr_forward_mode: "approve",
    fork_issues_mode: "auto",
    implement_mode: "auto",
    publish_target: "fork",
    scout_catalog: scoutCatalog(catalogKeys),
    scouts_by_role: scoutsByRole,
  };
}

function resetRepo() {
  const repo = makeRepo();
  hooks.state.selectedRepo = repo.repo_slug;
  hooks.state.overview = { repos: [repo], scout_catalog: [] };
  hooks.state.repoList = { items: [repo] };
  hooks.state.formDrafts = {};
  hooks.resetConfigEditorForRepo(repo);
  return {
    repo,
    scope: 'repo:' + repo.repo_slug + ':config',
  };
}

function setDraft(scope, field, value) {
  hooks.state.formDrafts[scope] = {
    ...(hooks.state.formDrafts[scope] || {}),
    ['config:' + field]: { kind: 'value', value },
  };
}

function currentDraft(scope) {
  return hooks.state.formDrafts[scope] || {};
}

function runRepoModeForwardCase() {
  const { repo, scope } = resetRepo();
  setDraft(scope, 'publish_target', 'repo');
  hooks.updateConfigDraft('publish_target', 'repo', { rerender: false });
  setDraft(scope, 'repo_mode', 'disabled');
  hooks.updateConfigDraft('repo_mode', 'disabled', { rerender: false });
  hooks.syncConfigEditorTransientDrafts(repo);
  return {
    repo_mode: hooks.state.configEditor.draft.repo_mode,
    publish_target: hooks.state.configEditor.draft.publish_target,
    has_publish_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:publish_target'),
  };
}

function runRepoModeReverseCase() {
  const { repo, scope } = resetRepo();
  setDraft(scope, 'repo_mode', 'local');
  hooks.updateConfigDraft('repo_mode', 'local', { rerender: false });
  setDraft(scope, 'publish_target', 'repo');
  hooks.updateConfigDraft('publish_target', 'repo', { rerender: false });
  hooks.syncConfigEditorTransientDrafts(repo);
  return {
    repo_mode: hooks.state.configEditor.draft.repo_mode,
    publish_target: hooks.state.configEditor.draft.publish_target,
    has_repo_mode_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:repo_mode'),
  };
}

function runIssuePickForwardCase() {
  const { repo, scope } = resetRepo();
  setDraft(scope, 'fork_issues_mode', 'manual');
  hooks.updateConfigDraft('fork_issues_mode', 'manual', { rerender: false });
  setDraft(scope, 'implement_mode', 'manual');
  hooks.updateConfigDraft('implement_mode', 'manual', { rerender: false });
  setDraft(scope, 'issue_pick_mode', 'label');
  hooks.updateConfigDraft('issue_pick_mode', 'label', { rerender: false });
  hooks.syncConfigEditorTransientDrafts(repo);
  return {
    issue_pick_mode: hooks.state.configEditor.draft.issue_pick_mode,
    fork_issues_mode: hooks.state.configEditor.draft.fork_issues_mode,
    implement_mode: hooks.state.configEditor.draft.implement_mode,
    has_fork_issues_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:fork_issues_mode'),
    has_implement_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:implement_mode'),
  };
}

function runIssuePickReverseCase() {
  const { repo, scope } = resetRepo();
  setDraft(scope, 'issue_pick_mode', 'label');
  hooks.updateConfigDraft('issue_pick_mode', 'label', { rerender: false });
  setDraft(scope, 'fork_issues_mode', 'manual');
  hooks.updateConfigDraft('fork_issues_mode', 'manual', { rerender: false });
  setDraft(scope, 'implement_mode', 'manual');
  hooks.updateConfigDraft('implement_mode', 'manual', { rerender: false });
  hooks.syncConfigEditorTransientDrafts(repo);
  return {
    issue_pick_mode: hooks.state.configEditor.draft.issue_pick_mode,
    fork_issues_mode: hooks.state.configEditor.draft.fork_issues_mode,
    implement_mode: hooks.state.configEditor.draft.implement_mode,
    has_issue_pick_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:issue_pick_mode'),
  };
}

function runScoutDestinationCase() {
  const { repo, scope } = resetRepo();
  setDraft(scope, 'scouts_by_role.improvement.issue_destination', 'fork');
  hooks.updateConfigDraft('scouts_by_role.improvement.issue_destination', 'fork', { rerender: false });
  setDraft(scope, 'scouts_by_role.improvement.fork_repo', 'acme/widget-stale');
  hooks.updateConfigDraft('scouts_by_role.improvement.fork_repo', 'acme/widget-stale', { rerender: false });
  setDraft(scope, 'scouts_by_role.improvement.issue_destination', 'local');
  hooks.updateConfigDraft('scouts_by_role.improvement.issue_destination', 'local', { rerender: false });
  hooks.syncConfigEditorTransientDrafts(repo);
  return {
    issue_destination: hooks.state.configEditor.draft.scouts_by_role.improvement.issue_destination,
    fork_repo: hooks.state.configEditor.draft.scouts_by_role.improvement.fork_repo,
    has_fork_repo_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:scouts_by_role.improvement.fork_repo'),
  };
}

function refreshScoutCatalog() {
  const refreshedRepo = makeRepo(['improvement', 'enhancement']);
  hooks.state.selectedRepo = refreshedRepo.repo_slug;
  hooks.state.overview = { repos: [refreshedRepo], scout_catalog: scoutCatalog(['improvement', 'enhancement']) };
  hooks.state.repoList = { items: [refreshedRepo] };
  hooks.ensureConfigEditor(refreshedRepo);
  hooks.syncConfigEditorTransientDrafts(refreshedRepo);
}

function runScoutCatalogRefreshHiddenCase() {
  const { scope } = resetRepo();
  setDraft(scope, 'scouts_by_role.ui.labels', 'qa, refreshed');
  hooks.updateConfigDraft('scouts_by_role.ui.labels', 'qa, refreshed', { rerender: false });
  refreshScoutCatalog();
  return {
    dirty: hooks.state.configEditor.dirty,
    remote_changed: hooks.state.configEditor.remoteChanged,
    has_ui_role: Object.prototype.hasOwnProperty.call(hooks.state.configEditor.draft.scouts_by_role, 'ui'),
    has_ui_compat: Object.prototype.hasOwnProperty.call(hooks.state.configEditor.draft.scouts || {}, 'ui'),
    has_ui_labels_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:scouts_by_role.ui.labels'),
  };
}

function runScoutCatalogRefreshVisibleCase() {
  const { scope } = resetRepo();
  setDraft(scope, 'scouts_by_role.improvement.labels', 'refresh, stable');
  hooks.updateConfigDraft('scouts_by_role.improvement.labels', 'refresh, stable', { rerender: false });
  setDraft(scope, 'scouts_by_role.ui.labels', 'qa, refreshed');
  hooks.updateConfigDraft('scouts_by_role.ui.labels', 'qa, refreshed', { rerender: false });
  refreshScoutCatalog();
  return {
    dirty: hooks.state.configEditor.dirty,
    remote_changed: hooks.state.configEditor.remoteChanged,
    improvement_labels: hooks.state.configEditor.draft.scouts_by_role.improvement.labels.join(','),
    has_improvement_labels_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:scouts_by_role.improvement.labels'),
    has_ui_role: Object.prototype.hasOwnProperty.call(hooks.state.configEditor.draft.scouts_by_role, 'ui'),
    has_ui_compat: Object.prototype.hasOwnProperty.call(hooks.state.configEditor.draft.scouts || {}, 'ui'),
    has_ui_labels_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:scouts_by_role.ui.labels'),
  };
}

function runSuccessfulSaveCanonicalizationCase() {
  const { scope } = resetRepo();
  setDraft(scope, 'scouts_by_role.ui.session_limit', '5');
  hooks.updateConfigDraft('scouts_by_role.ui.session_limit', '5', { rerender: false });
  const savedRepo = makeRepo();
  savedRepo.scouts_by_role.ui.session_limit = 5;
  hooks.clearDraftScope(scope);
  hooks.mergeRepoSummary(savedRepo, { resetConfigEditor: true, rerender: false });
  return {
    dirty: hooks.state.configEditor.dirty,
    remote_changed: hooks.state.configEditor.remoteChanged,
    session_limit: hooks.state.configEditor.draft.scouts_by_role.ui.session_limit,
    has_session_limit_draft: Object.prototype.hasOwnProperty.call(currentDraft(scope), 'config:scouts_by_role.ui.session_limit'),
  };
}

process.stdout.write(JSON.stringify({
  repo_mode_forward: runRepoModeForwardCase(),
  repo_mode_reverse: runRepoModeReverseCase(),
  issue_pick_forward: runIssuePickForwardCase(),
  issue_pick_reverse: runIssuePickReverseCase(),
  scout_destination: runScoutDestinationCase(),
  scout_catalog_refresh_hidden: runScoutCatalogRefreshHiddenCase(),
  scout_catalog_refresh_visible: runScoutCatalogRefreshVisibleCase(),
  successful_save_canonicalization: runSuccessfulSaveCanonicalizationCase(),
}));
`
	if err := os.WriteFile(harnessPath, []byte(harness), 0o644); err != nil {
		t.Fatalf("write node harness: %v", err)
	}

	output, err := exec.Command(nodePath, harnessPath, instrumentedPath).Output()
	if err != nil {
		t.Fatalf("run node harness: %v", err)
	}

	var got struct {
		RepoModeForward struct {
			RepoMode        string `json:"repo_mode"`
			PublishTarget   string `json:"publish_target"`
			HasPublishDraft bool   `json:"has_publish_draft"`
		} `json:"repo_mode_forward"`
		RepoModeReverse struct {
			RepoMode       string `json:"repo_mode"`
			PublishTarget  string `json:"publish_target"`
			HasRepoModeRaw bool   `json:"has_repo_mode_draft"`
		} `json:"repo_mode_reverse"`
		IssuePickForward struct {
			IssuePickMode      string `json:"issue_pick_mode"`
			ForkIssuesMode     string `json:"fork_issues_mode"`
			ImplementMode      string `json:"implement_mode"`
			HasForkIssuesDraft bool   `json:"has_fork_issues_draft"`
			HasImplementDraft  bool   `json:"has_implement_draft"`
		} `json:"issue_pick_forward"`
		IssuePickReverse struct {
			IssuePickMode     string `json:"issue_pick_mode"`
			ForkIssuesMode    string `json:"fork_issues_mode"`
			ImplementMode     string `json:"implement_mode"`
			HasIssuePickDraft bool   `json:"has_issue_pick_draft"`
		} `json:"issue_pick_reverse"`
		ScoutDestination struct {
			IssueDestination string `json:"issue_destination"`
			ForkRepo         string `json:"fork_repo"`
			HasForkRepoDraft bool   `json:"has_fork_repo_draft"`
		} `json:"scout_destination"`
		ScoutCatalogRefreshHidden struct {
			Dirty            bool `json:"dirty"`
			RemoteChanged    bool `json:"remote_changed"`
			HasUIRole        bool `json:"has_ui_role"`
			HasUICompat      bool `json:"has_ui_compat"`
			HasUILabelsDraft bool `json:"has_ui_labels_draft"`
		} `json:"scout_catalog_refresh_hidden"`
		ScoutCatalogRefreshVisible struct {
			Dirty                     bool   `json:"dirty"`
			RemoteChanged             bool   `json:"remote_changed"`
			ImprovementLabels         string `json:"improvement_labels"`
			HasImprovementLabelsDraft bool   `json:"has_improvement_labels_draft"`
			HasUIRole                 bool   `json:"has_ui_role"`
			HasUICompat               bool   `json:"has_ui_compat"`
			HasUILabelsDraft          bool   `json:"has_ui_labels_draft"`
		} `json:"scout_catalog_refresh_visible"`
		SuccessfulSaveCanonicalization struct {
			Dirty                bool `json:"dirty"`
			RemoteChanged        bool `json:"remote_changed"`
			SessionLimit         int  `json:"session_limit"`
			HasSessionLimitDraft bool `json:"has_session_limit_draft"`
		} `json:"successful_save_canonicalization"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if got.RepoModeForward.RepoMode != "disabled" || got.RepoModeForward.PublishTarget != "" || got.RepoModeForward.HasPublishDraft {
		t.Fatalf("expected repo mode replay to keep publish target cleared, got %+v", got.RepoModeForward)
	}
	if got.RepoModeReverse.RepoMode != "repo" || got.RepoModeReverse.PublishTarget != "repo" || got.RepoModeReverse.HasRepoModeRaw {
		t.Fatalf("expected publish target replay to keep repo mode normalized, got %+v", got.RepoModeReverse)
	}
	if got.IssuePickForward.IssuePickMode != "label" || got.IssuePickForward.ForkIssuesMode != "labeled" || got.IssuePickForward.ImplementMode != "labeled" || got.IssuePickForward.HasForkIssuesDraft || got.IssuePickForward.HasImplementDraft {
		t.Fatalf("expected issue pick replay to keep advanced modes normalized, got %+v", got.IssuePickForward)
	}
	if got.IssuePickReverse.IssuePickMode != "manual" || got.IssuePickReverse.ForkIssuesMode != "manual" || got.IssuePickReverse.ImplementMode != "manual" || got.IssuePickReverse.HasIssuePickDraft {
		t.Fatalf("expected advanced mode replay to keep issue pick normalized, got %+v", got.IssuePickReverse)
	}
	if got.ScoutDestination.IssueDestination != "local" || got.ScoutDestination.ForkRepo != "" || got.ScoutDestination.HasForkRepoDraft {
		t.Fatalf("expected scout destination replay to keep fork repo cleared, got %+v", got.ScoutDestination)
	}
	if got.ScoutCatalogRefreshHidden.Dirty || got.ScoutCatalogRefreshHidden.RemoteChanged || got.ScoutCatalogRefreshHidden.HasUIRole || got.ScoutCatalogRefreshHidden.HasUICompat || got.ScoutCatalogRefreshHidden.HasUILabelsDraft {
		t.Fatalf("expected removed scout roles to clear hidden replay state, got %+v", got.ScoutCatalogRefreshHidden)
	}
	if !got.ScoutCatalogRefreshVisible.Dirty || !got.ScoutCatalogRefreshVisible.RemoteChanged || got.ScoutCatalogRefreshVisible.ImprovementLabels != "refresh,stable" || !got.ScoutCatalogRefreshVisible.HasImprovementLabelsDraft || got.ScoutCatalogRefreshVisible.HasUIRole || got.ScoutCatalogRefreshVisible.HasUICompat || got.ScoutCatalogRefreshVisible.HasUILabelsDraft {
		t.Fatalf("expected catalog refresh to keep visible scout edits and drop hidden roles, got %+v", got.ScoutCatalogRefreshVisible)
	}
	if got.SuccessfulSaveCanonicalization.Dirty || got.SuccessfulSaveCanonicalization.RemoteChanged || got.SuccessfulSaveCanonicalization.SessionLimit != 5 || got.SuccessfulSaveCanonicalization.HasSessionLimitDraft {
		t.Fatalf("expected successful save canonicalization to reset the editor to the server snapshot, got %+v", got.SuccessfulSaveCanonicalization)
	}
}
