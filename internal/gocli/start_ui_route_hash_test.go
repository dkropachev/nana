package gocli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartUIRouteHashNormalizesLegacyFindingsLinksAndClearsTasklessInvestigations(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for start UI route hash coverage")
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
    applyHashState,
    buildRouteHash,
    syncHashState,
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
	harness := `global.window = {
  NANA_API_BASE: "",
  location: { hash: "", pathname: "/ui" },
  history: {
    replaceState(_state, _title, targetURL) {
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
global.globalThis = global;
require(process.argv[2]);

const hooks = global.__NANA_TEST_HOOKS;
if (!hooks) {
  throw new Error("start UI hooks were not exported");
}

function resetFilters() {
  Object.keys(hooks.state.filters || {}).forEach((view) => {
    hooks.state.filters[view] = {
      ...(hooks.state.filters[view] || {}),
      query: "",
      repo: "",
      attention: "all",
      kind: "all",
    };
  });
}

function resetState() {
  hooks.state.currentView = "home";
  hooks.state.selectedRepo = "";
  hooks.state.repoTab = "overview";
  hooks.state.feedbackTab = "reviews";
  hooks.state.taskComposer.repoSlug = "";
  hooks.state.issueQueue.selectedID = "";
  hooks.state.investigations.selectedRunID = "";
  hooks.state.approvals.selectedID = "";
  hooks.state.hashReady = true;
  resetFilters();
  global.window.location.hash = "";
  global.window.location.pathname = "/ui";
}

function runLegacyRepoFindingsCase() {
  resetState();
  global.window.location.hash = "#view=repo&repo=acme%2Fwidget&tab=findings";
  hooks.applyHashState();
  const routeHash = hooks.buildRouteHash();
  hooks.syncHashState();
  return {
    current_view: hooks.state.currentView,
    selected_repo: hooks.state.selectedRepo,
    repo_tab: hooks.state.repoTab,
    task_composer_repo: hooks.state.taskComposer.repoSlug,
    route_hash: routeHash,
    location_hash: global.window.location.hash,
    investigations_repo_filter: hooks.state.filters.investigations.repo,
  };
}

function runTasklessInvestigationsCase() {
  resetState();
  hooks.state.currentView = "investigations";
  hooks.state.investigations.selectedRunID = "work-run:stale-selection";
  global.window.location.hash = "#view=investigations";
  hooks.applyHashState();
  const routeHash = hooks.buildRouteHash();
  hooks.syncHashState();
  return {
    current_view: hooks.state.currentView,
    selected_run_id: hooks.state.investigations.selectedRunID,
    route_hash: routeHash,
    location_hash: global.window.location.hash,
  };
}

process.stdout.write(JSON.stringify({
  legacy_repo_findings: runLegacyRepoFindingsCase(),
  taskless_investigations: runTasklessInvestigationsCase(),
}));
`
	if err := os.WriteFile(harnessPath, []byte(harness), 0o644); err != nil {
		t.Fatalf("write node harness: %v", err)
	}

	output, err := exec.Command(nodePath, harnessPath, instrumentedPath).CombinedOutput()
	if err != nil {
		t.Fatalf("run node harness: %v\n%s", err, strings.TrimSpace(string(output)))
	}

	var got struct {
		LegacyRepoFindings struct {
			CurrentView              string `json:"current_view"`
			SelectedRepo             string `json:"selected_repo"`
			RepoTab                  string `json:"repo_tab"`
			TaskComposerRepo         string `json:"task_composer_repo"`
			RouteHash                string `json:"route_hash"`
			LocationHash             string `json:"location_hash"`
			InvestigationsRepoFilter string `json:"investigations_repo_filter"`
		} `json:"legacy_repo_findings"`
		TasklessInvestigations struct {
			CurrentView   string `json:"current_view"`
			SelectedRunID string `json:"selected_run_id"`
			RouteHash     string `json:"route_hash"`
			LocationHash  string `json:"location_hash"`
		} `json:"taskless_investigations"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node harness output %q: %v", strings.TrimSpace(string(output)), err)
	}

	if got.LegacyRepoFindings.CurrentView != "investigations" ||
		got.LegacyRepoFindings.SelectedRepo != "acme/widget" ||
		got.LegacyRepoFindings.TaskComposerRepo != "acme/widget" ||
		got.LegacyRepoFindings.InvestigationsRepoFilter != "acme/widget" {
		t.Fatalf("expected legacy repo findings links to normalize into the investigations workspace, got %+v", got.LegacyRepoFindings)
	}
	if got.LegacyRepoFindings.RepoTab != "overview" {
		t.Fatalf("expected legacy repo findings links to clear the removed findings tab, got %+v", got.LegacyRepoFindings)
	}
	if got.LegacyRepoFindings.RouteHash != "view=investigations&repo=acme%2Fwidget" ||
		got.LegacyRepoFindings.LocationHash != "#view=investigations&repo=acme%2Fwidget" {
		t.Fatalf("expected legacy repo findings links to canonicalize to the investigations hash, got %+v", got.LegacyRepoFindings)
	}

	if got.TasklessInvestigations.CurrentView != "investigations" || got.TasklessInvestigations.SelectedRunID != "" {
		t.Fatalf("expected task-less investigations links to clear stale selections, got %+v", got.TasklessInvestigations)
	}
	if got.TasklessInvestigations.RouteHash != "view=investigations" || got.TasklessInvestigations.LocationHash != "#view=investigations" {
		t.Fatalf("expected task-less investigations links to stay task-less until a fresh selection is chosen, got %+v", got.TasklessInvestigations)
	}
}
