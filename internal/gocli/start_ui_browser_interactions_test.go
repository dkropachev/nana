package gocli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	startUITestChromedpSetValue(t, tabCtx, "#issue-detail-deferred", "chromedp deferred reason")
	startUITestChromedpClick(t, tabCtx, `[data-issue-save="acme/widget#7"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "chromedp deferred reason")
	startUITestChromedpClick(t, tabCtx, `[data-issue-clear-schedule="acme/widget#7"]`)
	startUITestChromedpWaitBodyTextContains(t, tabCtx, "Not scheduled")
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
	launchSelector := fmt.Sprintf(`#approvals-grid [data-approval-launch-planned="%s"]`, fixture.PlannedApprovalID)
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
	retrySelector := fmt.Sprintf(`#approvals-grid [data-approval-retry-scout="%s"]`, fixture.FailedScoutJobID)
	startUITestChromedpClick(t, tabCtx, retrySelector)
	startUITestChromedpWaitDialogOpen(t, tabCtx, true)
	startUITestChromedpClick(t, tabCtx, "#confirm-dialog-confirm")
	startUITestChromedpWaitDialogOpen(t, tabCtx, false)
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

func startUITestChromedpSetValue(t *testing.T, ctx context.Context, selector string, value string) {
	t.Helper()
	script := fmt.Sprintf(`(() => {
		const el = document.querySelector(%q);
		if (!el) return false;
		el.focus();
		el.value = %q;
		el.dispatchEvent(new Event("input", { bubbles: true }));
		el.dispatchEvent(new Event("change", { bubbles: true }));
		return true;
	})()`, selector, value)
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &ok)); err != nil {
		t.Fatalf("set value %s: %v", selector, err)
	}
	if !ok {
		t.Fatalf("set value selector %s was not found", selector)
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
