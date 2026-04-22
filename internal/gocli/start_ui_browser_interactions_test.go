package gocli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestStartUIBrowserInteractionsQuickSwitch(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=home", "#global-repo-grid")
	startUITestChromedpSetValue(t, tabCtx, "#quick-switch-input", "Usage")
	startUITestChromedpClick(t, tabCtx, "#quick-switch-button")
	startUITestChromedpWaitHash(t, tabCtx, "#view=usage")
	startUITestChromedpWaitVisible(t, tabCtx, "#usage-filters-form")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Usage Filters")
}

func TestStartUIBrowserInteractionsRepoPicker(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=home", "#repo-scope-select")
	startUITestChromedpSetValue(t, tabCtx, "#repo-scope-select", fixture.RepoSlug)
	startUITestChromedpWaitHash(t, tabCtx, "#view=repo&repo="+url.QueryEscape(fixture.RepoSlug)+"&tab=overview")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Queue Snapshot")

	startUITestChromedpSetValue(t, tabCtx, "#repo-scope-select", "__all_repos__")
	startUITestChromedpWaitHash(t, tabCtx, "#view=home")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Pending Jobs Chart")

	startUITestChromedpDispatchValue(t, tabCtx, "#repo-scope-select", "__onboard_repo__")
	startUITestChromedpWaitHash(t, tabCtx, "#view=home")
	startUITestChromedpWaitVisible(t, tabCtx, "#repo-onboard-form")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Onboard Repo")
}

func TestStartUIBrowserInteractionsIssues(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=issues", "#issues-grid")
	startUITestChromedpSetValue(t, tabCtx, "#repo-scope-select", fixture.RepoSlug)
	startUITestChromedpWaitHash(t, tabCtx, "#view=issues&repo="+url.QueryEscape(fixture.RepoSlug)+"&issue="+url.QueryEscape("acme/widget#7"))
	startUITestChromedpWaitVisible(t, tabCtx, `[data-clear-workspace-scope="true"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "This page is filtered to the repo selected in the header picker.")
	startUITestChromedpClick(t, tabCtx, `[data-clear-workspace-scope="true"]`)
	startUITestChromedpWaitHash(t, tabCtx, "#view=issues&issue="+url.QueryEscape("acme/widget#7"))
	startUITestChromedpSetValue(t, tabCtx, "#issue-detail-deferred", "chromedp deferred reason")
	startUITestChromedpClick(t, tabCtx, `[data-issue-save="acme/widget#7"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "chromedp deferred reason")
	startUITestChromedpClick(t, tabCtx, `[data-issue-clear-schedule="acme/widget#7"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Not scheduled")
}

func TestStartUIBrowserInteractionsUsageScope(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=usage", "#usage-filters-form")
	startUITestChromedpSetValue(t, tabCtx, "#repo-scope-select", fixture.RepoSlug)
	startUITestChromedpWaitHash(t, tabCtx, "#view=usage&repo="+url.QueryEscape(fixture.RepoSlug))
	startUITestChromedpWaitVisible(t, tabCtx, `[data-clear-workspace-scope="true"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "This page is filtered to the repo selected in the header picker.")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Top Sessions")

	startUITestChromedpClick(t, tabCtx, `[data-clear-workspace-scope="true"]`)
	startUITestChromedpWaitHash(t, tabCtx, "#view=usage")
	startUITestChromedpWaitVisible(t, tabCtx, "#usage-filters-form")
}

func TestStartUIBrowserInteractionsRepoControls(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	githubServer, calls := startUITestGithubAPIServer(t)
	defer githubServer.Close()
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GITHUB_API_URL", githubServer.URL)

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=repo&repo=acme/widget&tab=controls", "#repo-scheduler-search-form")
	startUITestChromedpSetValue(t, tabCtx, "#repo-scheduler-search-query", "label:bug")
	startUITestChromedpClick(t, tabCtx, `#repo-scheduler-search-form button[type="submit"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Fix flaky widget")
	startUITestChromedpClick(t, tabCtx, `[data-scheduler-add="acme/widget#42"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Implement tracked issue #42: Fix flaky widget")

	updatedTitle := "Implement tracked issue #42: Fix flaky widget via chromedp"
	startUITestChromedpSetValue(t, tabCtx, "#repo-scheduler-detail-title", updatedTitle)
	startUITestChromedpSetValue(t, tabCtx, "#repo-scheduler-detail-priority", "0")
	startUITestChromedpSetValue(t, tabCtx, "#repo-scheduler-detail-work-type", "test_only")
	startUITestChromedpClick(t, tabCtx, `[data-scheduler-save]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, updatedTitle)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "test only")

	startUITestChromedpClick(t, tabCtx, `[data-scheduler-launch]`)
	startUITestChromedpWaitBodyTextAbsent(t, tabCtx, updatedTitle)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Implement tracked issue #7: Fix flaky test")
	if atomic.LoadInt32(&calls.searchIssues) != 1 {
		t.Fatalf("expected one GitHub issue search request, got %d", atomic.LoadInt32(&calls.searchIssues))
	}
}

func TestStartUIBrowserInteractionsDraftsSurviveLiveRefresh(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	type startUITestDraftRefreshCase struct {
		name          string
		hash          string
		readySelector string
		prepare       func(*testing.T, *startUITestBrowserFixture)
		edit          func(*testing.T, context.Context)
		assert        func(*testing.T, context.Context)
	}

	cases := []startUITestDraftRefreshCase{
		{
			name:          "repo-controls-planned-create",
			hash:          "#view=repo&repo=acme/widget&tab=controls",
			readySelector: "#repo-controls-planned-form-title",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-title", "Keep planned draft title")
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-description", "Keep planned draft description")
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-work_type", "refactor")
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-priority", "0")
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-schedule_at", "2026-04-23T14:30")
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-launch_kind", "github_issue")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-title", "Keep planned draft title")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-description", "Keep planned draft description")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-work_type", "refactor")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-priority", "0")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-schedule_at", "2026-04-23T14:30")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-launch_kind", "github_issue")
			},
		},
		{
			name:          "repo-controls-tracked-issue-form",
			hash:          "#view=repo&repo=acme/widget&tab=controls",
			readySelector: "#repo-controls-issue-form-priority",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#repo-controls-issue-form-priority", "4")
				startUITestChromedpSetValue(t, ctx, "#repo-controls-issue-form-schedule_at", "2026-04-24T09:45")
				startUITestChromedpSetValue(t, ctx, "#repo-controls-issue-form-deferred_reason", "Keep tracked issue draft")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-issue-form-priority", "4")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-issue-form-schedule_at", "2026-04-24T09:45")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-issue-form-deferred_reason", "Keep tracked issue draft")
			},
		},
		{
			name:          "issues-detail-form",
			hash:          "#view=issues",
			readySelector: "#issue-detail-priority",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#issue-detail-priority", "5")
				startUITestChromedpSetValue(t, ctx, "#issue-detail-schedule", "2026-04-25T11:15")
				startUITestChromedpSetValue(t, ctx, "#issue-detail-deferred", "Keep issue detail draft")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#issue-detail-priority", "5")
				startUITestChromedpWaitValue(t, ctx, "#issue-detail-schedule", "2026-04-25T11:15")
				startUITestChromedpWaitValue(t, ctx, "#issue-detail-deferred", "Keep issue detail draft")
			},
		},
		{
			name:          "repo-controls-scheduler-detail-form",
			hash:          "#view=repo&repo=acme/widget&tab=controls",
			readySelector: "#repo-scheduler-detail-title",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#repo-scheduler-detail-title", "Keep scheduler title")
				startUITestChromedpSetValue(t, ctx, "#repo-scheduler-detail-description", "Keep scheduler detail description")
				startUITestChromedpSetValue(t, ctx, "#repo-scheduler-detail-priority", "2")
				startUITestChromedpSetValue(t, ctx, "#repo-scheduler-detail-work-type", "refactor")
				startUITestChromedpSetValue(t, ctx, "#repo-scheduler-detail-schedule", "2026-04-26T13:00")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-scheduler-detail-title", "Keep scheduler title")
				startUITestChromedpWaitValue(t, ctx, "#repo-scheduler-detail-description", "Keep scheduler detail description")
				startUITestChromedpWaitValue(t, ctx, "#repo-scheduler-detail-priority", "2")
				startUITestChromedpWaitValue(t, ctx, "#repo-scheduler-detail-work-type", "refactor")
				startUITestChromedpWaitValue(t, ctx, "#repo-scheduler-detail-schedule", "2026-04-26T13:00")
			},
		},
		{
			name:          "investigations-finding-detail-form",
			hash:          "#view=investigations",
			readySelector: "#task-findings-grid",
			prepare:       startUITestSeedDraftRefreshFindingsData,
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitBodyTextContains(t, ctx, "Draft refresh finding")
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="finding"][data-row-select-id="finding-refresh"]`)
				startUITestChromedpWaitVisible(t, ctx, "#task-finding-title")
				startUITestChromedpSetValue(t, ctx, "#task-finding-title", "Keep finding draft title")
				startUITestChromedpSetValue(t, ctx, "#task-finding-summary", "Keep finding draft summary")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#task-finding-title", "Keep finding draft title")
				startUITestChromedpWaitValue(t, ctx, "#task-finding-summary", "Keep finding draft summary")
			},
		},
		{
			name:          "investigations-import-candidate-form",
			hash:          "#view=investigations",
			readySelector: "#task-import-sessions-grid",
			prepare:       startUITestSeedDraftRefreshImportSession,
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitBodyTextContains(t, ctx, "Draft refresh candidate")
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="import-candidate"][data-row-select-id="cand-refresh"]`)
				startUITestChromedpWaitVisible(t, ctx, "#task-import-candidate-title")
				startUITestChromedpSetValue(t, ctx, "#task-import-candidate-title", "Keep candidate draft title")
				startUITestChromedpSetValue(t, ctx, "#task-import-candidate-summary", "Keep candidate draft summary")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#task-import-candidate-title", "Keep candidate draft title")
				startUITestChromedpWaitValue(t, ctx, "#task-import-candidate-summary", "Keep candidate draft summary")
			},
		},
		{
			name:          "usage-filters-form",
			hash:          "#view=usage",
			readySelector: "#usage-filters-form",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#usage-filter-since", "90d")
				startUITestChromedpSetValue(t, ctx, "#usage-filter-root", "work")
				startUITestChromedpSetValue(t, ctx, "#usage-filter-activity", "draft-refresh-activity")
				startUITestChromedpSetValue(t, ctx, "#usage-filter-phase", "draft-refresh-phase")
				startUITestChromedpSetValue(t, ctx, "#usage-filter-model", "draft-refresh-model")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-since", "90d")
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-root", "work")
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-activity", "draft-refresh-activity")
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-phase", "draft-refresh-phase")
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-model", "draft-refresh-model")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := startUITestSetupBrowserFixtureWithOptions(t, startUITestBrowserFixtureOptions{LiveEvents: true})
			defer fixture.Server.Close()
			if tc.prepare != nil {
				tc.prepare(t, &fixture)
			}

			browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
			defer cancelBrowser()

			tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
			defer cancelTab()

			startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/"+tc.hash, tc.readySelector)
			tc.edit(t, tabCtx)
			startUITestChromedpWaitForBodyMutationAfter(t, tabCtx, func() {
				startUITestTriggerOverviewRefresh(t, fixture.Server.URL, tc.name)
			})
			tc.assert(t, tabCtx)
		})
	}
}

func TestStartUIBrowserInteractionsFocusSurvivesLiveRefresh(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	type startUITestFocusRefreshCase struct {
		name          string
		hash          string
		readySelector string
		prepare       func(*testing.T, *startUITestBrowserFixture)
		edit          func(*testing.T, context.Context)
		assert        func(*testing.T, context.Context)
	}

	cases := []startUITestFocusRefreshCase{
		{
			name:          "repo-controls-planned-create-title",
			hash:          "#view=repo&repo=acme/widget&tab=controls",
			readySelector: "#repo-controls-planned-form-title",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-title", "Keep planned draft title")
				startUITestChromedpSetSelectionRange(t, ctx, "#repo-controls-planned-form-title", 5, 5)
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-title", "Keep planned draft title")
				startUITestChromedpWaitFocusSelection(t, ctx, "#repo-controls-planned-form-title", 5, 5)
			},
		},
		{
			name:          "repo-controls-tracked-issue-textarea",
			hash:          "#view=repo&repo=acme/widget&tab=controls",
			readySelector: "#repo-controls-issue-form-deferred_reason",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#repo-controls-issue-form-deferred_reason", "Keep tracked issue draft")
				startUITestChromedpSetSelectionRange(t, ctx, "#repo-controls-issue-form-deferred_reason", 5, 11)
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-issue-form-deferred_reason", "Keep tracked issue draft")
				startUITestChromedpWaitFocusSelection(t, ctx, "#repo-controls-issue-form-deferred_reason", 5, 11)
			},
		},
		{
			name:          "issues-detail-textarea",
			hash:          "#view=issues",
			readySelector: "#issue-detail-deferred",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#issue-detail-deferred", "Keep issue detail draft")
				startUITestChromedpSetSelectionRange(t, ctx, "#issue-detail-deferred", 5, 10)
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#issue-detail-deferred", "Keep issue detail draft")
				startUITestChromedpWaitFocusSelection(t, ctx, "#issue-detail-deferred", 5, 10)
			},
		},
		{
			name:          "repo-scheduler-detail-title",
			hash:          "#view=repo&repo=acme/widget&tab=controls",
			readySelector: "#repo-scheduler-detail-title",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#repo-scheduler-detail-title", "Keep scheduler title")
				startUITestChromedpSetSelectionRange(t, ctx, "#repo-scheduler-detail-title", 5, 5)
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-scheduler-detail-title", "Keep scheduler title")
				startUITestChromedpWaitFocusSelection(t, ctx, "#repo-scheduler-detail-title", 5, 5)
			},
		},
		{
			name:          "finding-summary-textarea",
			hash:          "#view=investigations",
			readySelector: "#task-findings-grid",
			prepare:       startUITestSeedDraftRefreshFindingsData,
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitBodyTextContains(t, ctx, "Draft refresh finding")
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="finding"][data-row-select-id="finding-refresh"]`)
				startUITestChromedpWaitVisible(t, ctx, "#task-finding-summary")
				startUITestChromedpSetValue(t, ctx, "#task-finding-summary", "Keep finding draft summary")
				startUITestChromedpSetSelectionRange(t, ctx, "#task-finding-summary", 5, 12)
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#task-finding-summary", "Keep finding draft summary")
				startUITestChromedpWaitFocusSelection(t, ctx, "#task-finding-summary", 5, 12)
			},
		},
		{
			name:          "import-candidate-summary-textarea",
			hash:          "#view=investigations",
			readySelector: "#task-import-sessions-grid",
			prepare:       startUITestSeedDraftRefreshImportSession,
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitBodyTextContains(t, ctx, "Draft refresh candidate")
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="import-candidate"][data-row-select-id="cand-refresh"]`)
				startUITestChromedpWaitVisible(t, ctx, "#task-import-candidate-summary")
				startUITestChromedpSetValue(t, ctx, "#task-import-candidate-summary", "Keep candidate draft summary")
				startUITestChromedpSetSelectionRange(t, ctx, "#task-import-candidate-summary", 5, 13)
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#task-import-candidate-summary", "Keep candidate draft summary")
				startUITestChromedpWaitFocusSelection(t, ctx, "#task-import-candidate-summary", 5, 13)
			},
		},
		{
			name:          "usage-activity-input",
			hash:          "#view=usage",
			readySelector: "#usage-filter-activity",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#usage-filter-activity", "draft-refresh-activity")
				startUITestChromedpSetSelectionRange(t, ctx, "#usage-filter-activity", 6, 6)
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-activity", "draft-refresh-activity")
				startUITestChromedpWaitFocusSelection(t, ctx, "#usage-filter-activity", 6, 6)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := startUITestSetupBrowserFixtureWithOptions(t, startUITestBrowserFixtureOptions{LiveEvents: true})
			defer fixture.Server.Close()
			if tc.prepare != nil {
				tc.prepare(t, &fixture)
			}

			browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
			defer cancelBrowser()

			tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
			defer cancelTab()

			startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/"+tc.hash, tc.readySelector)
			tc.edit(t, tabCtx)
			startUITestChromedpWaitForBodyMutationAfter(t, tabCtx, func() {
				startUITestTriggerOverviewRefresh(t, fixture.Server.URL, tc.name)
			})
			tc.assert(t, tabCtx)
		})
	}
}

func TestStartUIBrowserInteractionsDraftsClearOnRouteLeave(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	type startUITestDraftRouteLeaveCase struct {
		name           string
		hash           string
		readySelector  string
		edit           func(*testing.T, context.Context)
		leaveAndReturn func(*testing.T, context.Context)
		assert         func(*testing.T, context.Context)
	}

	cases := []startUITestDraftRouteLeaveCase{
		{
			name:          "usage-filters",
			hash:          "#view=usage",
			readySelector: "#usage-filters-form",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#usage-filter-since", "90d")
				startUITestChromedpSetValue(t, ctx, "#usage-filter-root", "work")
				startUITestChromedpSetValue(t, ctx, "#usage-filter-activity", "transient-usage-activity")
				startUITestChromedpSetValue(t, ctx, "#usage-filter-phase", "transient-usage-phase")
				startUITestChromedpSetValue(t, ctx, "#usage-filter-model", "transient-usage-model")
				startUITestChromedpSetSelectionRange(t, ctx, "#usage-filter-activity", 4, 9)
			},
			leaveAndReturn: func(t *testing.T, ctx context.Context) {
				startUITestChromedpClick(t, ctx, `[data-nav-view="issues"]`)
				startUITestChromedpWaitVisible(t, ctx, "#issues-grid")
				startUITestChromedpClick(t, ctx, `[data-nav-view="usage"]`)
				startUITestChromedpWaitVisible(t, ctx, "#usage-filters-form")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-since", "30d")
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-root", "all")
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-activity", "")
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-phase", "")
				startUITestChromedpWaitValue(t, ctx, "#usage-filter-model", "")
				startUITestChromedpWaitActiveElementNot(t, ctx, "#usage-filter-activity")
			},
		},
		{
			name:          "issues-detail",
			hash:          "#view=issues",
			readySelector: "#issues-grid",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#issue-detail-priority", "5")
				startUITestChromedpSetValue(t, ctx, "#issue-detail-deferred", "Transient issues draft")
				startUITestChromedpSetSelectionRange(t, ctx, "#issue-detail-deferred", 4, 9)
			},
			leaveAndReturn: func(t *testing.T, ctx context.Context) {
				startUITestChromedpClick(t, ctx, `[data-nav-view="home"]`)
				startUITestChromedpWaitVisible(t, ctx, "#global-repo-grid")
				startUITestChromedpClick(t, ctx, `[data-nav-view="issues"]`)
				startUITestChromedpWaitVisible(t, ctx, "#issues-grid")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#issue-detail-priority", "1")
				startUITestChromedpWaitValue(t, ctx, "#issue-detail-deferred", "waiting for reproducible CI window")
				startUITestChromedpWaitActiveElementNot(t, ctx, "#issue-detail-deferred")
			},
		},
		{
			name:          "repo-controls",
			hash:          "#view=repo&repo=acme/widget&tab=controls",
			readySelector: "#repo-controls-planned-form-title",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-title", "Transient planned title")
				startUITestChromedpSetValue(t, ctx, "#repo-controls-planned-form-description", "Transient planned description")
				startUITestChromedpSetValue(t, ctx, "#repo-scheduler-search-query", "label:transient")
				startUITestChromedpSetSelectionRange(t, ctx, "#repo-controls-planned-form-title", 4, 8)
			},
			leaveAndReturn: func(t *testing.T, ctx context.Context) {
				startUITestChromedpClick(t, ctx, `[data-tab-group="repo"][data-tab-id="overview"]`)
				startUITestChromedpWaitVisible(t, ctx, "#repo-queue-summary")
				startUITestChromedpClick(t, ctx, `[data-tab-group="repo"][data-tab-id="controls"]`)
				startUITestChromedpWaitVisible(t, ctx, "#repo-controls-planned-form-title")
			},
			assert: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-title", "")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-description", "")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-work_type", "feature")
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-planned-form-priority", "3")
				startUITestChromedpWaitValue(t, ctx, "#repo-scheduler-search-query", "")
				startUITestChromedpWaitActiveElementNot(t, ctx, "#repo-controls-planned-form-title")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := startUITestSetupBrowserFixture(t)
			defer fixture.Server.Close()

			browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
			defer cancelBrowser()

			tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
			defer cancelTab()

			startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/"+tc.hash, tc.readySelector)
			tc.edit(t, tabCtx)
			tc.leaveAndReturn(t, tabCtx)
			tc.assert(t, tabCtx)
		})
	}
}

func TestStartUIBrowserInteractionsDraftsClearOnSelectionChange(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	type startUITestDraftSelectionCase struct {
		name          string
		hash          string
		readySelector string
		edit          func(*testing.T, context.Context)
		switchAway    func(*testing.T, context.Context)
		assertAway    func(*testing.T, context.Context)
		switchBack    func(*testing.T, context.Context)
		assertBack    func(*testing.T, context.Context)
	}

	cases := []startUITestDraftSelectionCase{
		{
			name:          "repo-controls-tracked-issue",
			hash:          "#view=repo&repo=acme/widget&tab=controls",
			readySelector: "#issue-select",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#repo-controls-issue-form-deferred_reason", "Transient tracked issue draft")
				startUITestChromedpSetSelectionRange(t, ctx, "#repo-controls-issue-form-deferred_reason", 5, 11)
			},
			switchAway: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#issue-select", "8")
			},
			assertAway: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-issue-form-deferred_reason", "second issue waits for release")
			},
			switchBack: func(t *testing.T, ctx context.Context) {
				startUITestChromedpSetValue(t, ctx, "#issue-select", "7")
			},
			assertBack: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#repo-controls-issue-form-deferred_reason", "waiting for reproducible CI window")
				startUITestChromedpWaitActiveElementNot(t, ctx, "#repo-controls-issue-form-deferred_reason")
			},
		},
		{
			name:          "investigations-finding-detail",
			hash:          "#view=investigations",
			readySelector: "#task-findings-grid",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitBodyTextContains(t, ctx, "Draft refresh finding")
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="finding"][data-row-select-id="finding-refresh"]`)
				startUITestChromedpWaitVisible(t, ctx, "#task-finding-title")
				startUITestChromedpSetValue(t, ctx, "#task-finding-title", "Transient finding title")
				startUITestChromedpSetSelectionRange(t, ctx, "#task-finding-title", 5, 9)
			},
			switchAway: func(t *testing.T, ctx context.Context) {
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="finding"][data-row-select-id="finding-other"]`)
			},
			assertAway: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#task-finding-title", "Secondary finding title")
			},
			switchBack: func(t *testing.T, ctx context.Context) {
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="finding"][data-row-select-id="finding-refresh"]`)
			},
			assertBack: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#task-finding-title", "Draft refresh finding")
				startUITestChromedpWaitActiveElementNot(t, ctx, "#task-finding-title")
			},
		},
		{
			name:          "investigations-import-candidate",
			hash:          "#view=investigations",
			readySelector: "#task-import-candidates-grid",
			edit: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitBodyTextContains(t, ctx, "Draft refresh candidate")
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="import-candidate"][data-row-select-id="cand-refresh"]`)
				startUITestChromedpWaitVisible(t, ctx, "#task-import-candidate-title")
				startUITestChromedpSetValue(t, ctx, "#task-import-candidate-title", "Transient candidate title")
				startUITestChromedpSetSelectionRange(t, ctx, "#task-import-candidate-title", 5, 9)
			},
			switchAway: func(t *testing.T, ctx context.Context) {
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="import-candidate"][data-row-select-id="cand-other"]`)
			},
			assertAway: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#task-import-candidate-title", "Secondary import candidate")
			},
			switchBack: func(t *testing.T, ctx context.Context) {
				startUITestChromedpClick(t, ctx, `[data-row-select-kind="import-candidate"][data-row-select-id="cand-refresh"]`)
			},
			assertBack: func(t *testing.T, ctx context.Context) {
				startUITestChromedpWaitValue(t, ctx, "#task-import-candidate-title", "Draft refresh candidate")
				startUITestChromedpWaitActiveElementNot(t, ctx, "#task-import-candidate-title")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := startUITestSetupBrowserFixture(t)
			defer fixture.Server.Close()
			startUITestSeedDraftSelectionData(t, &fixture)

			browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
			defer cancelBrowser()

			tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
			defer cancelTab()

			startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/"+tc.hash, tc.readySelector)
			tc.edit(t, tabCtx)
			tc.switchAway(t, tabCtx)
			tc.assertAway(t, tabCtx)
			tc.switchBack(t, tabCtx)
			tc.assertBack(t, tabCtx)
		})
	}
}

func TestStartUIBrowserInteractionsDraftsClearOnSchedulerSelectionChange(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	githubServer, calls := startUITestGithubSchedulerSelectionServer(t)
	defer githubServer.Close()
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GITHUB_API_URL", githubServer.URL)

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()
	startUITestSeedDraftSelectionData(t, &fixture)

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=repo&repo=acme/widget&tab=controls", "#repo-scheduler-search-form")
	startUITestChromedpSetValue(t, tabCtx, "#repo-scheduler-search-query", "label:bug")
	startUITestChromedpClick(t, tabCtx, `#repo-scheduler-search-form button[type="submit"]`)
	startUITestChromedpWaitVisible(t, tabCtx, `[data-scheduler-open="planned-tracked-2"]`)

	startUITestChromedpSetValue(t, tabCtx, "#repo-scheduler-detail-title", "Transient scheduler title")
	startUITestChromedpSetSelectionRange(t, tabCtx, "#repo-scheduler-detail-title", 5, 9)
	startUITestChromedpClick(t, tabCtx, `[data-scheduler-open="planned-tracked-2"]`)
	startUITestChromedpWaitValue(t, tabCtx, "#repo-scheduler-detail-title", "Implement tracked issue #8: Review transient draft clearing")
	startUITestChromedpClick(t, tabCtx, `[data-scheduler-open="planned-tracked"]`)
	startUITestChromedpWaitValue(t, tabCtx, "#repo-scheduler-detail-title", "Implement tracked issue #7: Fix flaky test")
	startUITestChromedpWaitActiveElementNot(t, tabCtx, "#repo-scheduler-detail-title")

	if atomic.LoadInt32(&calls.searchIssues) != 1 {
		t.Fatalf("expected one GitHub issue search request, got %d", atomic.LoadInt32(&calls.searchIssues))
	}
}

func TestStartUIBrowserInteractionsFocusDoesNotRestoreAfterExplicitSaveOrReset(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	t.Run("issue-save", func(t *testing.T) {
		fixture := startUITestSetupBrowserFixture(t)
		defer fixture.Server.Close()

		browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
		defer cancelBrowser()

		tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
		defer cancelTab()

		startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=issues", "#issue-detail-deferred")
		startUITestChromedpSetValue(t, tabCtx, "#issue-detail-deferred", "focus save reason")
		startUITestChromedpSetSelectionRange(t, tabCtx, "#issue-detail-deferred", 2, 7)
		startUITestChromedpFocus(t, tabCtx, `[data-issue-save="acme/widget#7"]`)
		startUITestChromedpClick(t, tabCtx, `[data-issue-save="acme/widget#7"]`)
		startUITestChromedpWaitBodyTextContains(t, tabCtx, "focus save reason")
		startUITestChromedpWaitActiveElementNot(t, tabCtx, "#issue-detail-deferred")
	})

	t.Run("usage-reset", func(t *testing.T) {
		fixture := startUITestSetupBrowserFixture(t)
		defer fixture.Server.Close()

		browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
		defer cancelBrowser()

		tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
		defer cancelTab()

		startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=usage", "#usage-filter-activity")
		startUITestChromedpSetValue(t, tabCtx, "#usage-filter-activity", "focus-reset-activity")
		startUITestChromedpSetSelectionRange(t, tabCtx, "#usage-filter-activity", 3, 9)
		startUITestChromedpFocus(t, tabCtx, `[data-usage-reset="true"]`)
		startUITestChromedpClick(t, tabCtx, `[data-usage-reset="true"]`)
		startUITestChromedpWaitValue(t, tabCtx, "#usage-filter-activity", "")
		startUITestChromedpWaitActiveElementNot(t, tabCtx, "#usage-filter-activity")
	})
}

func TestStartUIBrowserInteractionsWorkRunSync(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	oldSyncGithubRun := startUISyncGithubRun
	startUISyncGithubRun = func(options githubWorkSyncOptions) error {
		entry, err := readWorkRunIndex(options.RunID)
		if err != nil {
			return err
		}
		manifest, err := readGithubWorkManifest(entry.ManifestPath)
		if err != nil {
			return err
		}
		manifest.CurrentPhase = "publish"
		manifest.UpdatedAt = "2026-04-21T22:45:00Z"
		if err := writeGithubJSON(entry.ManifestPath, manifest); err != nil {
			return err
		}
		return indexGithubWorkRunManifest(entry.ManifestPath, manifest)
	}
	t.Cleanup(func() {
		startUISyncGithubRun = oldSyncGithubRun
	})

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=work", "#work-runs-grid")
	startUITestChromedpClick(t, tabCtx, `[data-log-run-id="gh-ui-blocked"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Run State")
	startUITestChromedpClick(t, tabCtx, `[data-run-sync="gh-ui-blocked"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "publish · Round 2")
}

func TestStartUIBrowserInteractionsApprovalLaunchCancelAndConfirm(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=approvals", "#approvals-grid")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Launch smoke run")
	startUITestChromedpClickByText(t, tabCtx, `#approvals-grid [data-row-select-kind="approval"]`, "Launch smoke run")
	launchSelector := fmt.Sprintf(`#approvals-detail [data-approval-launch-planned="%s"]`, fixture.PlannedApprovalID)
	startUITestChromedpClick(t, tabCtx, launchSelector)
	startUITestChromedpWaitDialogOpen(t, tabCtx, true)
	startUITestChromedpClick(t, tabCtx, "#confirm-dialog-cancel")
	startUITestChromedpWaitDialogOpen(t, tabCtx, false)
	startUITestChromedpWaitVisible(t, tabCtx, launchSelector)

	startUITestChromedpClick(t, tabCtx, launchSelector)
	startUITestChromedpWaitDialogOpen(t, tabCtx, true)
	startUITestChromedpClick(t, tabCtx, "#confirm-dialog-confirm")
	startUITestChromedpWaitDialogOpen(t, tabCtx, false)
	startUITestChromedpWaitSelectorAbsent(t, tabCtx, launchSelector)
}

func TestStartUIBrowserInteractionsApprovalRetryScout(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=approvals", "#approvals-grid")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Retry failed scout")
	startUITestChromedpClickByText(t, tabCtx, `#approvals-grid [data-row-select-kind="approval"]`, "Retry failed scout")
	retrySelector := fmt.Sprintf(`#approvals-detail [data-approval-retry-scout="%s"]`, fixture.FailedScoutJobID)
	startUITestChromedpClick(t, tabCtx, retrySelector)
	startUITestChromedpWaitDialogOpen(t, tabCtx, true)
	startUITestChromedpClick(t, tabCtx, "#confirm-dialog-confirm")
	startUITestChromedpWaitDialogOpen(t, tabCtx, false)
	startUITestWaitFor(t, 10*time.Second, "scout retry to persist", func() bool {
		state, err := readStartWorkState(fixture.RepoSlug)
		if err != nil {
			return false
		}
		job, ok := state.ScoutJobs[fixture.FailedScoutJobID]
		return ok && job.Status == startScoutJobQueued
	})
	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=approvals", "#approvals-grid")
	startUITestChromedpWaitSelectorAbsent(t, tabCtx, retrySelector)
}

func TestStartUIBrowserInteractionsApprovalDropRun(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=approvals", "#approvals-grid")
	dropSelector := fmt.Sprintf(`#approvals-grid [data-approval-drop-kind="work_run"][data-approval-drop-id="%s"]`, fixture.GithubRunID)
	startUITestChromedpClick(t, tabCtx, dropSelector)
	startUITestChromedpWaitDialogOpen(t, tabCtx, true)
	startUITestChromedpClick(t, tabCtx, "#confirm-dialog-confirm")
	startUITestChromedpWaitDialogOpen(t, tabCtx, false)
	startUITestChromedpWaitSelectorAbsent(t, tabCtx, dropSelector)
}

func TestStartUIBrowserInteractionsWorkItemSubmit(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	githubServer, calls := startUITestGithubAPIServer(t)
	defer githubServer.Close()
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GITHUB_API_URL", githubServer.URL)

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()
	startUITestPointReplyWorkItemAtGithubAPI(t, fixture.ReplyItemID, githubServer.URL)

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=approvals", "#approvals-grid")
	reviewSelector := fmt.Sprintf(`#approvals-grid [data-work-item-submit="%s"]`, fixture.ReviewItemID)
	startUITestChromedpClick(t, tabCtx, reviewSelector)
	startUITestChromedpWaitDialogOpen(t, tabCtx, true)
	startUITestChromedpClick(t, tabCtx, "#confirm-dialog-cancel")
	startUITestChromedpWaitDialogOpen(t, tabCtx, false)
	startUITestChromedpWaitVisible(t, tabCtx, reviewSelector)

	startUITestChromedpClick(t, tabCtx, reviewSelector)
	startUITestChromedpWaitDialogOpen(t, tabCtx, true)
	startUITestChromedpClick(t, tabCtx, "#confirm-dialog-confirm")
	startUITestChromedpWaitDialogOpen(t, tabCtx, false)
	startUITestWaitFor(t, 10*time.Second, "review work item submission", func() bool {
		return atomic.LoadInt32(&calls.submitReview) == 1 && startUITestReadWorkItem(t, fixture.ReviewItemID).Status == workItemStatusSubmitted
	})
	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=approvals", "#approvals-grid")
	startUITestChromedpWaitSelectorAbsent(t, tabCtx, reviewSelector)

	replySelector := fmt.Sprintf(`#approvals-grid [data-work-item-submit="%s"]`, fixture.ReplyItemID)
	startUITestChromedpClick(t, tabCtx, replySelector)
	startUITestChromedpWaitDialogOpen(t, tabCtx, true)
	startUITestChromedpClick(t, tabCtx, "#confirm-dialog-confirm")
	startUITestChromedpWaitDialogOpen(t, tabCtx, false)
	startUITestWaitFor(t, 10*time.Second, "reply work item submission", func() bool {
		return atomic.LoadInt32(&calls.submitReply) == 1 && startUITestReadWorkItem(t, fixture.ReplyItemID).Status == workItemStatusSubmitted
	})
	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=approvals", "#approvals-grid")
	startUITestChromedpWaitSelectorAbsent(t, tabCtx, replySelector)

	if atomic.LoadInt32(&calls.submitReview) != 1 {
		t.Fatalf("expected one review submission request, got %d", atomic.LoadInt32(&calls.submitReview))
	}
	if atomic.LoadInt32(&calls.submitReply) != 1 {
		t.Fatalf("expected one reply submission request, got %d", atomic.LoadInt32(&calls.submitReply))
	}
}

func TestStartUIBrowserInteractionsTasksFindingsAndImportFlow(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for chromedp browser interaction coverage")
	}

	fixture := startUITestSetupBrowserFixture(t)
	defer fixture.Server.Close()

	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeFindingsTestExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\ncat >/dev/null\nprintf '{\"candidates\":[{\"candidate_id\":\"cand-1\",\"title\":\"Fix retry wording\",\"summary\":\"Clarify retry scope\",\"detail\":\"The retry label should explain whether the whole worker reruns.\",\"severity\":\"medium\",\"work_type\":\"feature\"},{\"candidate_id\":\"cand-2\",\"title\":\"Drop this candidate\",\"summary\":\"Optional candidate\",\"detail\":\"This candidate should be dropped after import review.\",\"severity\":\"low\",\"work_type\":\"test_only\"}]}'\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	markdownPath := filepath.Join(t.TempDir(), "findings.md")
	if err := os.WriteFile(markdownPath, []byte("# Findings\n\n- Retry wording"), 0o644); err != nil {
		t.Fatalf("write markdown file: %v", err)
	}

	browserCtx, cancelBrowser := startUITestNewChromedpBrowser(t, chromePath)
	defer cancelBrowser()

	tabCtx, cancelTab := startUITestNewChromedpTab(t, browserCtx)
	defer cancelTab()

	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=repo&repo="+fixture.RepoSlug+"&tab=overview", "#page-body")
	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=investigations", "#task-findings-import-button")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Findings Inbox")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, fixture.RepoSlug)
	startUITestChromedpSetUploadFiles(t, tabCtx, "#task-findings-import-file", []string{markdownPath})
	startUITestChromedpClick(t, tabCtx, "#task-findings-import-button")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Import session created")
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Fix retry wording")

	startUITestChromedpSetValue(t, tabCtx, "#task-import-candidate-title", "Fix retry wording everywhere")
	startUITestChromedpSetValue(t, tabCtx, "#task-import-candidate-summary", "Clarify retry scope in all views")
	startUITestChromedpClick(t, tabCtx, `[data-task-import-candidate-save="cand-1"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Candidate saved")

	startUITestChromedpClick(t, tabCtx, `[data-task-import-candidate-promote="cand-1"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Candidate promoted")
	startUITestWaitFor(t, 10*time.Second, "candidate promotion to create finding", func() bool {
		state, err := readStartWorkState(fixture.RepoSlug)
		if err != nil {
			return false
		}
		return len(state.Findings) == 1
	})
	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=repo&repo="+fixture.RepoSlug+"&tab=overview", "#page-body")
	startUITestChromedpOpen(t, tabCtx, fixture.Server.URL+"/#view=investigations", "#task-findings-import-button")
	startUITestChromedpWaitCondition(t, tabCtx, 10*time.Second, "promote button disabled for promoted candidate", func() (bool, string, error) {
		var disabled bool
		if err := chromedp.Run(tabCtx, chromedp.Evaluate(`Boolean(document.querySelector('[data-task-import-candidate-promote="cand-1"]') && document.querySelector('[data-task-import-candidate-promote="cand-1"]').disabled)`, &disabled)); err != nil {
			return false, "", err
		}
		return disabled, strconv.FormatBool(disabled), nil
	})

	startUITestChromedpClick(t, tabCtx, `[data-row-select-kind="finding"]`)
	startUITestChromedpSetValue(t, tabCtx, "#task-finding-title", "Fix retry wording across surfaces")
	startUITestChromedpClick(t, tabCtx, `[data-task-finding-save]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Finding saved")

	startUITestChromedpClick(t, tabCtx, `[data-task-finding-dismiss]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Finding dismissed")
	startUITestWaitFor(t, 10*time.Second, "finding dismiss to persist", func() bool {
		state, err := readStartWorkState(fixture.RepoSlug)
		if err != nil {
			return false
		}
		for _, finding := range state.Findings {
			return finding.Status == startWorkFindingStatusDismissed
		}
		return false
	})
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Dismissed")

	startUITestChromedpClick(t, tabCtx, `[data-row-select-kind="import-candidate"][data-row-select-id="cand-2"]`)
	startUITestChromedpClick(t, tabCtx, `[data-task-import-candidate-drop="cand-2"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Candidate dropped")
	startUITestWaitFor(t, 10*time.Second, "candidate drop to persist", func() bool {
		state, err := readStartWorkState(fixture.RepoSlug)
		if err != nil {
			return false
		}
		for _, session := range state.ImportSessions {
			if len(session.Candidates) > 1 {
				return session.Candidates[1].Status == startWorkFindingCandidateStatusDropped
			}
		}
		return false
	})
	startUITestChromedpClick(t, tabCtx, `[data-row-select-kind="import-candidate"][data-row-select-id="cand-2"]`)
	startUITestChromedpWaitCondition(t, tabCtx, 10*time.Second, "promote button disabled for dropped candidate", func() (bool, string, error) {
		var disabled bool
		if err := chromedp.Run(tabCtx, chromedp.Evaluate(`Boolean(document.querySelector('[data-task-import-candidate-promote="cand-2"]') && document.querySelector('[data-task-import-candidate-promote="cand-2"]').disabled)`, &disabled)); err != nil {
			return false, "", err
		}
		return disabled, strconv.FormatBool(disabled), nil
	})

	state, err := readStartWorkState(fixture.RepoSlug)
	if err != nil {
		t.Fatalf("read start work state: %v", err)
	}
	if len(state.Findings) != 1 {
		t.Fatalf("expected one promoted real finding after browser flow, got %+v", state.Findings)
	}
	var finding startWorkFinding
	for _, candidate := range state.Findings {
		finding = candidate
	}
	if finding.Status != startWorkFindingStatusDismissed {
		t.Fatalf("unexpected finding after browser flow: %+v", finding)
	}
	if len(state.ImportSessions) != 1 {
		t.Fatalf("expected one import session after browser flow, got %+v", state.ImportSessions)
	}
	for _, session := range state.ImportSessions {
		if len(session.Candidates) != 2 {
			t.Fatalf("expected two candidates in import session, got %+v", session)
		}
		if session.Candidates[0].Status != startWorkFindingCandidateStatusPromoted || session.Candidates[1].Status != startWorkFindingCandidateStatusDropped {
			t.Fatalf("unexpected candidate statuses after browser flow: %+v", session.Candidates)
		}
	}
}

func TestStartUIBrowserInteractionsRepoControlsHydratesDedicatedRepoStateWhenLoadingOrderVaries(t *testing.T) {
	chromePath := startUITestChromePath(t)
	if chromePath == "" {
		t.Skip("google-chrome is required for browser interaction coverage")
	}

	cases := []struct {
		name      string
		delayPath string
	}{
		{name: "delayed-overview", delayPath: "/api/v1/overview"},
		{name: "delayed-repo-list", delayPath: "/api/v1/repos"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := startUITestSetupBrowserFixture(t)
			defer fixture.Server.Close()

			server := startUITestNewDelayedStartUIServer(t, fixture.API, tc.delayPath, 250*time.Millisecond)
			defer server.Close()

			output := startUITestDumpDOM(t, chromePath, server.URL+"/#view=repo&repo=acme/widget&tab=controls")
			startUITestRequireText(t, output, "Implement tracked issue #7: Fix flaky test", tc.name)
			startUITestRequireText(t, output, "Launch Existing", tc.name)
		})
	}
}

type startUITestGithubAPICalls struct {
	searchIssues int32
	submitReview int32
	submitReply  int32
}

func startUITestGithubAPIServer(t *testing.T) (*httptest.Server, *startUITestGithubAPICalls) {
	t.Helper()

	calls := &startUITestGithubAPICalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/issues":
			atomic.AddInt32(&calls.searchIssues, 1)
			query := r.URL.Query().Get("q")
			if !strings.Contains(query, "repo:acme/widget") || !strings.Contains(query, "is:issue") || !strings.Contains(query, "is:open") || !strings.Contains(query, "label:bug") {
				http.Error(w, "unexpected query: "+query, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"number":42,"title":"Fix flaky widget","body":"Body","state":"open","html_url":"https://github.com/acme/widget/issues/42","updated_at":"2026-04-15T12:00:00Z","labels":[{"name":"bug"},{"name":"P1"}]}]}`))
		case "/repos/acme/widget/pulls/11/reviews":
			atomic.AddInt32(&calls.submitReview, 1)
			_, _ = w.Write([]byte(`{"id":101,"html_url":"https://github.com/acme/widget/pull/11#pullrequestreview-101"}`))
		case "/repos/acme/widget/pulls/comments/22/replies":
			atomic.AddInt32(&calls.submitReply, 1)
			_, _ = w.Write([]byte(`{"id":202,"html_url":"https://github.com/acme/widget/pull/11#discussion_r202"}`))
		default:
			http.Error(w, "unexpected route: "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	return server, calls
}

func startUITestGithubSchedulerSelectionServer(t *testing.T) (*httptest.Server, *startUITestGithubAPICalls) {
	t.Helper()

	calls := &startUITestGithubAPICalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/issues":
			atomic.AddInt32(&calls.searchIssues, 1)
			query := r.URL.Query().Get("q")
			if !strings.Contains(query, "repo:acme/widget") || !strings.Contains(query, "is:issue") || !strings.Contains(query, "is:open") || !strings.Contains(query, "label:bug") {
				http.Error(w, "unexpected query: "+query, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"number":7,"title":"Fix flaky test","body":"Body","state":"open","html_url":"https://github.com/acme/widget/issues/7","updated_at":"2026-04-15T12:00:00Z","labels":[{"name":"bug"},{"name":"P1"}]},{"number":8,"title":"Review transient draft clearing","body":"Body","state":"open","html_url":"https://github.com/acme/widget/issues/8","updated_at":"2026-04-16T12:00:00Z","labels":[{"name":"bug"},{"name":"P3"}]}]}`))
		default:
			http.Error(w, "unexpected route: "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	return server, calls
}

func startUITestPointReplyWorkItemAtGithubAPI(t *testing.T, itemID string, apiBaseURL string) {
	t.Helper()
	item := startUITestReadWorkItem(t, itemID)
	if item.Metadata == nil {
		item.Metadata = map[string]any{}
	}
	item.Metadata["comment_api_url"] = strings.TrimRight(apiBaseURL, "/") + "/repos/acme/widget/pulls/comments/22"
	startUITestUpdateWorkItem(t, item)
}

func startUITestNewChromedpBrowser(t *testing.T, chromePath string) (context.Context, context.CancelFunc) {
	t.Helper()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(browserCtx); err != nil {
		cancelBrowser()
		cancelAlloc()
		t.Fatalf("start chromedp browser: %v", err)
	}
	return browserCtx, func() {
		cancelBrowser()
		cancelAlloc()
	}
}

func startUITestNewChromedpTab(t *testing.T, browserCtx context.Context) (context.Context, context.CancelFunc) {
	t.Helper()
	tabCtx, cancelTab := chromedp.NewContext(browserCtx)
	ctx, cancelTimeout := context.WithTimeout(tabCtx, 45*time.Second)
	return ctx, func() {
		cancelTimeout()
		cancelTab()
	}
}

func startUITestChromedpOpen(t *testing.T, ctx context.Context, targetURL string, readySelector string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(targetURL),
		chromedp.WaitVisible(readySelector, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open %s: %v", targetURL, err)
	}
	startUITestChromedpWaitBodyTextAbsent(t, ctx, "Loading operator state...")
}

func startUITestChromedpClick(t *testing.T, ctx context.Context, selector string) {
	t.Helper()
	script := fmt.Sprintf(`(() => {
		const el = document.querySelector(%q);
		if (!el) return false;
		el.click();
		return true;
	})()`, selector)
	var ok bool
	if err := chromedp.Run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery), chromedp.Evaluate(script, &ok)); err != nil {
		t.Fatalf("click %s: %v", selector, err)
	}
	if !ok {
		t.Fatalf("click selector %s was not found", selector)
	}
}

func startUITestChromedpClickByText(t *testing.T, ctx context.Context, selector string, needle string) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("selector %s to contain %q", selector, needle), func() (bool, string, error) {
		var found bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			return Array.from(document.querySelectorAll(%q)).some((el) => String(el.innerText || "").includes(%q));
		})()`, selector, needle), &found)); err != nil {
			return false, "", err
		}
		return found, strconv.FormatBool(found), nil
	})
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
		const match = Array.from(document.querySelectorAll(%q)).find((el) => String(el.innerText || "").includes(%q));
		if (!match) return false;
		match.click();
		return true;
	})()`, selector, needle), &ok)); err != nil {
		t.Fatalf("click by text %s (%s): %v", selector, needle, err)
	}
	if !ok {
		t.Fatalf("click selector %s containing %q was not found", selector, needle)
	}
}

func startUITestChromedpFocus(t *testing.T, ctx context.Context, selector string) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			if (!el || typeof el.focus !== "function") return false;
			el.focus();
			return document.activeElement === el;
		})()`, selector), &ok),
	); err != nil {
		t.Fatalf("focus %s: %v", selector, err)
	}
	if !ok {
		t.Fatalf("focus selector %s was not found or did not become active", selector)
	}
}

func startUITestChromedpSetValue(t *testing.T, ctx context.Context, selector string, value string) {
	t.Helper()
	script := fmt.Sprintf(`(() => {
		const el = document.querySelector(%q);
		if (!el) return "";
		let proto = window.HTMLInputElement && window.HTMLInputElement.prototype;
		if (el instanceof HTMLTextAreaElement) {
			proto = window.HTMLTextAreaElement.prototype;
		} else if (el instanceof HTMLSelectElement) {
			proto = window.HTMLSelectElement.prototype;
		}
		const descriptor = proto ? Object.getOwnPropertyDescriptor(proto, "value") : null;
		if (descriptor && typeof descriptor.set === "function") {
			descriptor.set.call(el, %q);
		} else {
			el.value = %q;
		}
		el.dispatchEvent(new Event("input", { bubbles: true }));
		el.dispatchEvent(new Event("change", { bubbles: true }));
		return String(el.value || "");
	})()`, selector, value, value)
	var current string
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Evaluate(script, &current),
	); err != nil {
		t.Fatalf("set value %s: %v", selector, err)
	}
	startUITestChromedpWaitCondition(t, ctx, 5*time.Second, fmt.Sprintf("value %s to become %q", selector, value), func() (bool, string, error) {
		var latest string
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			return el ? String(el.value || "") : "";
		})()`, selector), &latest)); err != nil {
			return false, "", err
		}
		return latest == value, latest, nil
	})
}

func startUITestChromedpDispatchValue(t *testing.T, ctx context.Context, selector string, value string) {
	t.Helper()
	script := fmt.Sprintf(`(() => {
		const el = document.querySelector(%q);
		if (!el) return "";
		let proto = window.HTMLInputElement && window.HTMLInputElement.prototype;
		if (el instanceof HTMLTextAreaElement) {
			proto = window.HTMLTextAreaElement.prototype;
		} else if (el instanceof HTMLSelectElement) {
			proto = window.HTMLSelectElement.prototype;
		}
		const descriptor = proto ? Object.getOwnPropertyDescriptor(proto, "value") : null;
		if (descriptor && typeof descriptor.set === "function") {
			descriptor.set.call(el, %q);
		} else {
			el.value = %q;
		}
		el.dispatchEvent(new Event("input", { bubbles: true }));
		el.dispatchEvent(new Event("change", { bubbles: true }));
		return "ok";
	})()`, selector, value, value)
	var result string
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Evaluate(script, &result),
	); err != nil {
		t.Fatalf("dispatch value %s: %v", selector, err)
	}
}

func startUITestChromedpSetSelectionRange(t *testing.T, ctx context.Context, selector string, start int, end int) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			if (!el || typeof el.focus !== "function") return false;
			el.focus();
			if (typeof el.setSelectionRange !== "function") return false;
			el.setSelectionRange(%d, %d, "forward");
			return true;
		})()`, selector, start, end), &ok),
	); err != nil {
		t.Fatalf("set selection range %s: %v", selector, err)
	}
	if !ok {
		t.Fatalf("selection range selector %s was not found or does not support selection", selector)
	}
	startUITestChromedpWaitFocusSelection(t, ctx, selector, start, end)
}

func startUITestChromedpSetUploadFiles(t *testing.T, ctx context.Context, selector string, files []string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.SetUploadFiles(selector, files, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("set upload files %s: %v", selector, err)
	}
}

func startUITestChromedpWaitVisible(t *testing.T, ctx context.Context, selector string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery)); err != nil {
		t.Fatalf("wait visible %s: %v", selector, err)
	}
}

func startUITestChromedpWaitSelectorAbsent(t *testing.T, ctx context.Context, selector string) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("selector %s to disappear", selector), func() (bool, string, error) {
		var exists bool
		expr := fmt.Sprintf(`Boolean(document.querySelector(%q))`, selector)
		if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &exists)); err != nil {
			return false, "", err
		}
		return !exists, strconv.FormatBool(exists), nil
	})
}

func startUITestChromedpWaitBodyTextContains(t *testing.T, ctx context.Context, needle string) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("body to contain %q", needle), func() (bool, string, error) {
		var body string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &body)); err != nil {
			return false, "", err
		}
		return strings.Contains(body, needle), body, nil
	})
}

func startUITestChromedpWaitBodyTextAbsent(t *testing.T, ctx context.Context, needle string) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("body to omit %q", needle), func() (bool, string, error) {
		var body string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &body)); err != nil {
			return false, "", err
		}
		return !strings.Contains(body, needle), body, nil
	})
}

func startUITestChromedpWaitHash(t *testing.T, ctx context.Context, expected string) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("hash to become %q", expected), func() (bool, string, error) {
		var hash string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.location.hash`, &hash)); err != nil {
			return false, "", err
		}
		return hash == expected, hash, nil
	})
}

func startUITestChromedpWaitValue(t *testing.T, ctx context.Context, selector string, expected string) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("value %s to become %q", selector, expected), func() (bool, string, error) {
		var value string
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			return el ? String(el.value ?? "") : "";
		})()`, selector), &value)); err != nil {
			return false, "", err
		}
		return value == expected, value, nil
	})
}

func startUITestChromedpWaitActiveElement(t *testing.T, ctx context.Context, selector string) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("active element to be %s", selector), func() (bool, string, error) {
		var active bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			return Boolean(el && document.activeElement === el);
		})()`, selector), &active)); err != nil {
			return false, "", err
		}
		return active, strconv.FormatBool(active), nil
	})
}

func startUITestChromedpWaitActiveElementNot(t *testing.T, ctx context.Context, selector string) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("active element to not be %s", selector), func() (bool, string, error) {
		var active bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			return Boolean(el && document.activeElement === el);
		})()`, selector), &active)); err != nil {
			return false, "", err
		}
		return !active, strconv.FormatBool(active), nil
	})
}

func startUITestChromedpWaitFocusSelection(t *testing.T, ctx context.Context, selector string, start int, end int) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("focus selection for %s to become %d:%d", selector, start, end), func() (bool, string, error) {
		var state struct {
			Focused bool `json:"focused"`
			Start   int  `json:"start"`
			End     int  `json:"end"`
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			if (!el) return { focused: false, start: -1, end: -1 };
			return {
				focused: document.activeElement === el,
				start: typeof el.selectionStart === "number" ? el.selectionStart : -1,
				end: typeof el.selectionEnd === "number" ? el.selectionEnd : -1,
			};
		})()`, selector), &state)); err != nil {
			return false, "", err
		}
		return state.Focused && state.Start == start && state.End == end, fmt.Sprintf("%t:%d:%d", state.Focused, state.Start, state.End), nil
	})
}

func startUITestChromedpWaitDialogOpen(t *testing.T, ctx context.Context, expected bool) {
	t.Helper()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, fmt.Sprintf("confirm dialog open=%t", expected), func() (bool, string, error) {
		var open bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(`Boolean(document.getElementById("confirm-dialog") && document.getElementById("confirm-dialog").open)`, &open)); err != nil {
			return false, "", err
		}
		return open == expected, strconv.FormatBool(open), nil
	})
}

func startUITestChromedpWaitCondition(t *testing.T, ctx context.Context, timeout time.Duration, description string, fn func() (bool, string, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ok, current, err := fn()
		if err == nil && ok {
			return
		}
		if err != nil {
			last = err.Error()
		} else {
			last = current
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (last=%q)", description, last)
}

func startUITestWaitFor(t *testing.T, timeout time.Duration, description string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func startUITestChromedpWaitForBodyMutationAfter(t *testing.T, ctx context.Context, action func()) {
	t.Helper()
	key := fmt.Sprintf("body-mutation-%d", time.Now().UnixNano())
	var baseline float64
	script := fmt.Sprintf(`(() => {
		window.__startUITestMutationObservers = window.__startUITestMutationObservers || {};
		const target = document.querySelector(%q);
		if (!target) return -1;
		let entry = window.__startUITestMutationObservers[%q];
		if (!entry) {
			entry = { count: 0 };
			entry.observer = new MutationObserver((records) => {
				entry.count += records.length || 1;
			});
			entry.observer.observe(target, {
				childList: true,
				subtree: true,
				attributes: true,
				characterData: true,
			});
			window.__startUITestMutationObservers[%q] = entry;
		}
		return entry.count || 0;
	})()`, "#page-body", key, key)
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &baseline)); err != nil {
		t.Fatalf("install body mutation observer: %v", err)
	}
	if baseline < 0 {
		t.Fatalf("page body was not available for mutation tracking")
	}
	baseline = startUITestChromedpWaitForMutationCountToStabilize(t, ctx, key, time.Second)
	action()
	startUITestChromedpWaitCondition(t, ctx, 10*time.Second, "page body to rerender", func() (bool, string, error) {
		count, err := startUITestChromedpMutationCount(ctx, key)
		if err != nil {
			return false, "", err
		}
		return count > baseline, strconv.FormatFloat(count, 'f', 0, 64), nil
	})
}

func startUITestChromedpWaitForMutationCountToStabilize(t *testing.T, ctx context.Context, key string, timeout time.Duration) float64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := -1.0
	lastChangeAt := time.Now()
	for time.Now().Before(deadline) {
		current, err := startUITestChromedpMutationCount(ctx, key)
		if err != nil {
			t.Fatalf("read mutation count: %v", err)
		}
		if current != last {
			last = current
			lastChangeAt = time.Now()
		}
		if time.Since(lastChangeAt) >= 250*time.Millisecond {
			return current
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for mutation count to stabilize for %s", key)
	return 0
}

func startUITestChromedpMutationCount(ctx context.Context, key string) (float64, error) {
	var count float64
	err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
		const registry = window.__startUITestMutationObservers || {};
		const entry = registry[%q];
		return entry ? Number(entry.count || 0) : -1;
	})()`, key), &count))
	return count, err
}

func startUITestNewDelayedStartUIServer(t *testing.T, api *startUIAPI, delayPath string, delay time.Duration) *httptest.Server {
	t.Helper()

	routes := api.routes()
	var apiBase string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == delayPath {
			time.Sleep(delay)
		}
		routes.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		startUIWebHandler(apiBase).ServeHTTP(w, r)
	})

	server := httptest.NewServer(mux)
	apiBase = server.URL
	api.allowedWebOrigin = server.URL
	return server
}

func startUITestTriggerOverviewRefresh(t *testing.T, baseURL string, testName string) {
	t.Helper()
	title := fmt.Sprintf("Warm staging environment refresh %s %d", testName, time.Now().UnixNano())
	response, err := http.DefaultClient.Do(mustJSONRequest(t, http.MethodPatch, baseURL+"/api/v1/planned-items/planned-manual", fmt.Sprintf(`{"title":%q}`, title)))
	if err != nil {
		t.Fatalf("PATCH planned item for live refresh: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected live refresh patch status 200, got %d", response.StatusCode)
	}
}

func startUITestSeedDraftRefreshFindingsData(t *testing.T, fixture *startUITestBrowserFixture) {
	t.Helper()
	state, err := readStartWorkState(fixture.RepoSlug)
	if err != nil {
		t.Fatalf("read start work state: %v", err)
	}
	now := "2026-04-22T12:00:00Z"
	if state.Findings == nil {
		state.Findings = map[string]startWorkFinding{}
	}
	state.Findings["finding-refresh"] = startWorkFinding{
		ID:           "finding-refresh",
		RepoSlug:     fixture.RepoSlug,
		SourceKind:   startWorkFindingSourceKindManualImport,
		SourceID:     "import-refresh",
		SourceItemID: "cand-refresh",
		Title:        "Draft refresh finding",
		Summary:      "Original finding summary",
		Detail:       "Original finding detail",
		Evidence:     "Original finding evidence",
		Severity:     "medium",
		WorkType:     workTypeFeature,
		Status:       startWorkFindingStatusOpen,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := writeStartWorkState(*state); err != nil {
		t.Fatalf("write start work state: %v", err)
	}
	fixture.API.invalidateOverviewCache()
}

func startUITestSeedDraftRefreshImportSession(t *testing.T, fixture *startUITestBrowserFixture) {
	t.Helper()
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeFindingsTestExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\ncat >/dev/null\nprintf '{\"candidates\":[{\"candidate_id\":\"cand-refresh\",\"title\":\"Draft refresh candidate\",\"summary\":\"Original candidate summary\",\"detail\":\"Original candidate detail\",\"evidence\":\"Original candidate evidence\",\"severity\":\"medium\",\"work_type\":\"feature\"}]}'\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	if _, err := createStartUIFindingImportSession(fixture.RepoSlug, "draft-refresh.md", "# Findings\n\n- Draft refresh candidate"); err != nil {
		t.Fatalf("create finding import session: %v", err)
	}
	fixture.API.invalidateOverviewCache()
}

func startUITestSeedDraftSelectionData(t *testing.T, fixture *startUITestBrowserFixture) {
	t.Helper()
	startUITestSeedDraftRefreshFindingsData(t, fixture)
	startUITestSeedDraftRefreshImportSession(t, fixture)

	state, err := readStartWorkState(fixture.RepoSlug)
	if err != nil {
		t.Fatalf("read start work state: %v", err)
	}
	now := "2026-04-22T12:30:00Z"

	if state.Issues == nil {
		state.Issues = map[string]startWorkIssueState{}
	}
	state.Issues["8"] = startWorkIssueState{
		SourceNumber:      8,
		SourceURL:         "https://github.com/acme/widget/issues/8",
		Title:             "Review transient draft clearing",
		State:             "open",
		Status:            startWorkStatusBlocked,
		WorkType:          workTypeFeature,
		Priority:          3,
		PrioritySource:    "triage",
		Complexity:        2,
		Labels:            []string{"enhancement", "P3"},
		TriageStatus:      startWorkTriageCompleted,
		TriageRationale:   "Selection switching should discard stale drafts.",
		TriageUpdatedAt:   now,
		ScheduleAt:        "2026-04-27T09:00:00Z",
		DeferredReason:    "second issue waits for release",
		LastRunID:         "gh-ui-blocked",
		LastRunUpdatedAt:  now,
		PublishedPRNumber: 12,
		PublishedPRURL:    "https://github.com/acme/widget/pull/12",
		PublicationState:  "ci_waiting",
		BlockedReason:     "waiting for reviewer availability",
		UpdatedAt:         now,
	}

	if state.PlannedItems == nil {
		state.PlannedItems = map[string]startWorkPlannedItem{}
	}
	state.PlannedItems["planned-tracked-2"] = startWorkPlannedItem{
		ID:          "planned-tracked-2",
		RepoSlug:    fixture.RepoSlug,
		Title:       "Implement tracked issue #8: Review transient draft clearing",
		Description: "Tracked issue: https://github.com/acme/widget/issues/8\nWork type: feature\nPriority: P3",
		WorkType:    workTypeFeature,
		LaunchKind:  "tracked_issue",
		TargetURL:   "https://github.com/acme/widget/issues/8",
		Priority:    3,
		State:       startPlannedItemQueued,
		ScheduleAt:  "2026-04-29T09:00:00Z",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if state.Findings == nil {
		state.Findings = map[string]startWorkFinding{}
	}
	state.Findings["finding-other"] = startWorkFinding{
		ID:           "finding-other",
		RepoSlug:     fixture.RepoSlug,
		SourceKind:   startWorkFindingSourceKindManualImport,
		SourceID:     "import-refresh",
		SourceItemID: "cand-other",
		Title:        "Secondary finding title",
		Summary:      "Secondary finding summary",
		Detail:       "Secondary finding detail",
		Evidence:     "Secondary finding evidence",
		Severity:     "high",
		WorkType:     workTypeBugFix,
		Status:       startWorkFindingStatusOpen,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	sessionID := ""
	for id, session := range state.ImportSessions {
		if len(session.Candidates) == 1 && session.Candidates[0].CandidateID == "cand-refresh" {
			sessionID = id
			break
		}
	}
	if sessionID == "" {
		t.Fatalf("seeded import session with cand-refresh was not found")
	}
	session := state.ImportSessions[sessionID]
	session.Candidates = append(session.Candidates, startWorkFindingImportCandidate{
		CandidateID: "cand-other",
		Title:       "Secondary import candidate",
		Summary:     "Secondary candidate summary",
		Detail:      "Secondary candidate detail",
		Evidence:    "Secondary candidate evidence",
		Severity:    "high",
		WorkType:    workTypeBugFix,
		Status:      startWorkFindingCandidateStatusCandidate,
	})
	session.UpdatedAt = now
	state.ImportSessions[sessionID] = session

	if err := writeStartWorkState(*state); err != nil {
		t.Fatalf("write start work state: %v", err)
	}
	fixture.API.invalidateOverviewCache()
}
