package gocli

import (
	"strings"
	"testing"
)

func TestStartUIAppRenderGridPreservesScrollByDefault(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		`const preserveScroll = options.preserveScroll !== false;`,
		`const currentTableWrap = preserveScroll ? host.querySelector(".data-table-wrap") : null;`,
		`const currentCards = preserveScroll ? host.querySelector(".data-cards") : null;`,
		`if (preserveScroll) {`,
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected app asset to preserve grid scroll by default via %q", needle)
		}
	}
}

func TestStartUIAppSidebarOmitsWorkNavItem(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		`data-nav-view="work"`,
		`<span class="nav-label">Work</span>`,
	} {
		if strings.Contains(content, needle) {
			t.Fatalf("expected task workspace nav contract to omit %q", needle)
		}
	}
}

func TestStartUIAppWorkRunSurfacesKeepStopControlsWired(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		`can_stop: Boolean(run.stop_allowed),`,
		`label: "Stop",`,
		`data-run-stop="${escapeHTML(row.run_id)}"`,
		`data-run-stop="${escapeHTML(summary.run_id)}">Stop Run</button>`,
		`const runStopButton = event.target.closest("[data-run-stop]");`,
		"`/api/v1/work/runs/${encodeURIComponent(runID)}/stop`",
		`toast("success", "Run stopped", runID);`,
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected app asset to keep stop-run UI wiring for %q", needle)
		}
	}
}

func TestStartUIAppWorkRunSurfacesKeepRerunControlsWired(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		`can_rerun: Boolean(run.rerun_allowed),`,
		`label: "Re-run",`,
		`data-run-rerun="${escapeHTML(row.run_id)}"`,
		`data-run-rerun="${escapeHTML(summary.run_id)}">Re-run</button>`,
		`function rerunWorkRunFromUI(runID) {`,
		`const runRerunButton = event.target.closest("[data-run-rerun]");`,
		"`/api/v1/work/runs/${encodeURIComponent(runID)}/rerun`",
		`toast("success", "Run re-started", defaultString(nextRunID, runID));`,
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected app asset to keep rerun UI wiring for %q", needle)
		}
	}
}

func TestStartUIAppTaskSchedulerKeepsPresetTemplateControlsWired(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		`templateID: "template:custom",`,
		`savingTemplate: false,`,
		`function loadTaskTemplates(options = {}) {`,
		"api(`/api/v1/tasks/templates${query}`)",
		`function saveTaskTemplateFromComposer() {`,
		`api("/api/v1/tasks/templates", {`,
		`base_template_id: taskComposerPresetBaseTemplateID(),`,
		`<select id="task-composer-template"`,
		`id="task-save-template-button"`,
		`applyTaskComposerTemplate(template);`,
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected task scheduler preset wiring for %q", needle)
		}
	}
}

func TestStartUIAppRepoConfigSaveKeepsSavingGuardsWired(t *testing.T) {
	appBody, err := startUIAssetsFS.ReadFile("start_ui_assets/app.txt")
	if err != nil {
		t.Fatalf("read app asset: %v", err)
	}
	content := string(appBody)
	for _, needle := range []string{
		`if (!state.configEditor.draft || state.configEditor.saving) return;`,
		`const configInputsDisabled = Boolean(editor.saving);`,
		`const resetDisabled = editor.saving || (!editor.dirty && !editor.remoteChanged);`,
		`renderConfigSelect("pr_forward_mode", "PR Forward", draft.pr_forward_mode, configFieldOptions("pr_forward_mode"), configInputsDisabled)`,
		`renderScoutConfigSection(repo, entry.config_key, draft.scouts_by_role[entry.config_key], configInputsDisabled)`,
		`data-config-reset="true" ${resetDisabled ? "disabled" : ""}`,
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("expected repo config save guard wiring for %q", needle)
		}
	}
}
