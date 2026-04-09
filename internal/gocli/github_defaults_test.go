package gocli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGithubDefaultsSetAndShow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	setOutput, err := captureStdout(t, func() error {
		return GithubWorkOn(".", []string{
			"defaults", "set", "acme/widget",
			"--considerations", "style,qa,security",
			"--review-rules-mode", "automatic",
			"--review-rules-trusted-reviewers", "reviewer-a,reviewer-b",
			"--review-rules-blocked-reviewers", "reviewer-c",
			"--review-rules-min-distinct-reviewers", "2",
		})
	})
	if err != nil {
		t.Fatalf("GithubWorkOn(defaults set): %v", err)
	}
	if !strings.Contains(setOutput, "Saved default considerations for acme/widget: style, qa, security") {
		t.Fatalf("unexpected defaults set output: %q", setOutput)
	}

	showOutput, err := captureStdout(t, func() error {
		return GithubWorkOn(".", []string{"defaults", "show", "acme/widget"})
	})
	if err != nil {
		t.Fatalf("GithubWorkOn(defaults show): %v", err)
	}
	if !strings.Contains(showOutput, "Default considerations for acme/widget: style, qa, security") {
		t.Fatalf("unexpected defaults show output: %q", showOutput)
	}
	if !strings.Contains(showOutput, "Effective review-rules mode for acme/widget: automatic") {
		t.Fatalf("missing effective review-rules mode in %q", showOutput)
	}
	if !strings.Contains(showOutput, "coder -> executor [execute, owner=self, blocking]") {
		t.Fatalf("missing pipeline output in %q", showOutput)
	}
}

func TestGithubReviewRulesConfigSetAndShow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	setOutput, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{
			"config", "set",
			"--mode", "automatic",
			"--trusted-reviewers", "reviewer-a,reviewer-b",
			"--blocked-reviewers", "reviewer-c",
			"--min-distinct-reviewers", "2",
		})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(config set): %v", err)
	}
	if !strings.Contains(setOutput, "Saved global review-rules mode: automatic") {
		t.Fatalf("unexpected config set output: %q", setOutput)
	}

	showOutput, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{"config", "show", "https://github.com/acme/widget/issues/42"})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(config show): %v", err)
	}
	if !strings.Contains(showOutput, "Global review-rules mode: automatic") {
		t.Fatalf("unexpected config show output: %q", showOutput)
	}
	if !strings.Contains(showOutput, "Effective review-rules mode for acme/widget: automatic") {
		t.Fatalf("missing effective mode output: %q", showOutput)
	}

	configPath := filepath.Join(home, ".nana", "github-workon", "review-rules-config.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected review-rules global config at %s: %v", configPath, err)
	}
}

func TestGithubWorkOnStats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	statsPath := filepath.Join(home, ".nana", "repos", "acme", "widget", "issues", "issue-42.json")
	if err := os.MkdirAll(filepath.Dir(statsPath), 0o755); err != nil {
		t.Fatalf("mkdir stats dir: %v", err)
	}
	if err := os.WriteFile(statsPath, []byte(`{
  "version": 1,
  "repo_slug": "acme/widget",
  "issue_number": 42,
  "updated_at": "2026-04-03T10:15:00.000Z",
  "totals": {
    "input_tokens": 120,
    "output_tokens": 80,
    "total_tokens": 200,
    "sessions_accounted": 1
  },
  "sandboxes": {
    "issue-42-pr-123456789012": {
      "input_tokens": 120,
      "output_tokens": 80,
      "total_tokens": 200,
      "sessions_accounted": 1
    }
  }
}`), 0o644); err != nil {
		t.Fatalf("write stats file: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return GithubWorkOn(".", []string{"stats", "https://github.com/acme/widget/issues/42"})
	})
	if err != nil {
		t.Fatalf("GithubWorkOn(stats): %v", err)
	}
	if !strings.Contains(output, "Token stats for acme/widget issue #42") {
		t.Fatalf("unexpected stats output: %q", output)
	}
	if !strings.Contains(output, "issue-42-pr-123456789012: total=200 input=120 output=80 sessions=1") {
		t.Fatalf("missing sandbox rollup: %q", output)
	}

	prSandboxPath := filepath.Join(home, ".nana", "repos", "acme", "widget", "sandboxes", "pr-77")
	if err := os.MkdirAll(filepath.Join(prSandboxPath, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir pr sandbox metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prSandboxPath, ".nana", "sandbox.json"), []byte(`{
  "sandbox_id": "pr-77",
  "target_kind": "issue",
  "target_number": 42
}`), 0o644); err != nil {
		t.Fatalf("write pr sandbox metadata: %v", err)
	}

	prOutput, err := captureStdout(t, func() error {
		return GithubWorkOn(".", []string{"stats", "https://github.com/acme/widget/pull/77"})
	})
	if err != nil {
		t.Fatalf("GithubWorkOn(stats pr): %v", err)
	}
	if !strings.Contains(prOutput, "Token stats for acme/widget issue #42") {
		t.Fatalf("unexpected PR stats output: %q", prOutput)
	}
}

func TestGithubWorkOnRetrospective(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42-pr-123456789012")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-retro-1"

	if err := os.MkdirAll(filepath.Join(sandboxPath, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir sandbox .nana: %v", err)
	}
	sessionsDir := filepath.Join(sandboxPath, ".codex", "sessions", "2026", "04", "03")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".nana", "github-workon"), 0o755); err != nil {
		t.Fatalf("mkdir github-workon: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoCheckoutPath, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-1.jsonl"), []byte(strings.Join([]string{
		`{"timestamp":"2026-04-03T17:00:01.000Z","type":"session_meta","payload":{"agent_nickname":"","agent_role":""}}`,
		`{"timestamp":"2026-04-03T17:00:11.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1234}}}}`,
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write rollout-1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-2.jsonl"), []byte(strings.Join([]string{
		`{"timestamp":"2026-04-03T17:00:02.000Z","type":"session_meta","payload":{"agent_nickname":"Gauss","agent_role":"architect"}}`,
		`{"timestamp":"2026-04-03T17:00:09.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":4321}}}}`,
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write rollout-2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nana", "github-workon", "latest-run.json"), []byte(fmt.Sprintf(`{"repo_root":%q,"run_id":%q}`, managedRepoRoot, runID)), 0o644); err != nil {
		t.Fatalf("write latest-run: %v", err)
	}
	manifestContent := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "role_layout": "split",
  "considerations_active": ["arch", "qa"]
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifestContent), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return GithubWorkOn(".", []string{"retrospective", "--last"})
	})
	if err != nil {
		t.Fatalf("GithubWorkOn(retrospective): %v", err)
	}
	if !strings.Contains(output, "NANA Work-on Retrospective") {
		t.Fatalf("missing retrospective title: %q", output)
	}
	if !strings.Contains(output, "Role layout: split") {
		t.Fatalf("missing role layout: %q", output)
	}
	if !strings.Contains(output, "Total thread tokens: 5555") {
		t.Fatalf("missing total thread tokens: %q", output)
	}
	if !strings.Contains(output, "Gauss: role=architect class=reviewer tokens=4321") {
		t.Fatalf("missing thread usage row: %q", output)
	}
	if _, err := os.Stat(filepath.Join(managedRepoRoot, "runs", runID, "thread-usage.json")); err != nil {
		t.Fatalf("expected thread-usage artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(managedRepoRoot, "runs", runID, "retrospective.md")); err != nil {
		t.Fatalf("expected retrospective artifact: %v", err)
	}
}

func TestGithubReviewRulesLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rulesPath := filepath.Join(home, ".nana", "repos", "acme", "widget", "source", ".nana", "repo-review-rules.json")
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		t.Fatalf("mkdir rules dir: %v", err)
	}
	initial := `{
  "approved_rules": [],
  "pending_candidates": [
    {
      "id": "qa-1",
      "title": "Add regression coverage",
      "category": "qa",
      "confidence": 0.95,
      "reviewer_count": 2,
      "extraction_origin": "review_comments",
      "extraction_reason": "Repeated review comments across 2 PRs",
      "path_scopes": ["src/api/client.ts"],
      "evidence": [
        {
          "kind": "comment",
          "pr_number": 7,
          "reviewer": "reviewer-a",
          "path": "src/api/client.ts",
          "line": 1,
          "excerpt": "Please add regression tests",
          "code_context_excerpt": "1: export function searchDocuments",
          "code_context_provenance": "pr_head_sha",
          "code_context_ref": "sha-pr-7"
        }
      ]
    }
  ],
  "disabled_rules": [],
  "archived_rules": []
}`
	if err := os.WriteFile(rulesPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write rules file: %v", err)
	}

	listOutput, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{"list", "https://github.com/acme/widget/pull/7"})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(list): %v", err)
	}
	if !strings.Contains(listOutput, "pending qa-1 [qa] confidence=0.95 reviewers=2 Add regression coverage") {
		t.Fatalf("unexpected list output: %q", listOutput)
	}

	approveOutput, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{"approve", "https://github.com/acme/widget/pull/7", "qa-1"})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(approve): %v", err)
	}
	if !strings.Contains(approveOutput, "Approved 1 repo review rule(s) for acme/widget.") {
		t.Fatalf("unexpected approve output: %q", approveOutput)
	}

	disableOutput, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{"disable", "https://github.com/acme/widget/pull/7", "qa-1"})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(disable): %v", err)
	}
	if !strings.Contains(disableOutput, "Disabled 1 review rule(s) for acme/widget.") {
		t.Fatalf("unexpected disable output: %q", disableOutput)
	}

	enableOutput, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{"enable", "https://github.com/acme/widget/pull/7", "qa-1"})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(enable): %v", err)
	}
	if !strings.Contains(enableOutput, "Enabled 1 review rule(s) for acme/widget.") {
		t.Fatalf("unexpected enable output: %q", enableOutput)
	}

	explainOutput, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{"explain", "https://github.com/acme/widget/pull/7", "qa-1"})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(explain): %v", err)
	}
	if !strings.Contains(explainOutput, "Rule qa-1 (approved)") || !strings.Contains(explainOutput, "Title: Add regression coverage") {
		t.Fatalf("unexpected explain output: %q", explainOutput)
	}

	archiveOutput, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{"archive", "https://github.com/acme/widget/pull/7", "qa-1"})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(archive): %v", err)
	}
	if !strings.Contains(archiveOutput, "Archived 1 review rule(s) for acme/widget.") {
		t.Fatalf("unexpected archive output: %q", archiveOutput)
	}
}

func TestGithubReviewRulesScanIssueURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	manifestPath := filepath.Join(home, ".nana", "repos", "acme", "widget", "runs", "gh-link-1", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{
  "repo_slug": "acme/widget",
  "target_kind": "issue",
  "target_number": 42,
  "published_pr_number": 7
}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	secondManifestPath := filepath.Join(home, ".nana", "repos", "acme", "widget", "runs", "gh-link-2", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(secondManifestPath), 0o755); err != nil {
		t.Fatalf("mkdir second manifest dir: %v", err)
	}
	if err := os.WriteFile(secondManifestPath, []byte(`{
  "repo_slug": "acme/widget",
  "target_kind": "issue",
  "target_number": 42,
  "published_pr_number": 8
}`), 0o644); err != nil {
		t.Fatalf("write second manifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/pulls/7?":
			_, _ = w.Write([]byte(`{"number":7,"head":{"sha":"sha-pr-7"}}`))
		case "/repos/acme/widget/pulls/8?":
			_, _ = w.Write([]byte(`{"number":8,"head":{"sha":"sha-pr-8"}}`))
		case "/repos/acme/widget/pulls/7/reviews?per_page=100":
			_, _ = w.Write([]byte(`[{"id":701,"html_url":"https://example.invalid/review/701","body":"Please add regression tests for this behavior change before merge.","state":"CHANGES_REQUESTED","user":{"login":"reviewer-a"}}]`))
		case "/repos/acme/widget/pulls/8/reviews?per_page=100":
			_, _ = w.Write([]byte(`[{"id":702,"html_url":"https://example.invalid/review/702","body":"Needs regression coverage before we merge this.","state":"COMMENTED","user":{"login":"reviewer-b"}}]`))
		case "/repos/acme/widget/pulls/7/comments?per_page=100":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/acme/widget/pulls/8/comments?per_page=100":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected github route: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		return GithubReviewRules(".", []string{"scan", "https://github.com/acme/widget/issues/42"})
	})
	if err != nil {
		t.Fatalf("GithubReviewRules(scan): %v", err)
	}
	if !strings.Contains(output, "Scanned PR review history for acme/widget from https://github.com/acme/widget/issues/42.") {
		t.Fatalf("unexpected scan output: %q", output)
	}
	if !strings.Contains(output, "pending qa-") {
		t.Fatalf("expected pending QA candidate in output: %q", output)
	}
	rulesPath := filepath.Join(home, ".nana", "repos", "acme", "widget", "source", ".nana", "repo-review-rules.json")
	content, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatalf("read rules file: %v", err)
	}
	if !strings.Contains(string(content), `"category": "qa"`) {
		t.Fatalf("expected QA rule in document: %s", string(content))
	}
}

func TestResolveGithubRunIDForTargetURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	runDir := filepath.Join(home, ".nana", "repos", "acme", "widget", "runs", "gh-run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	manifest := `{
  "run_id": "gh-run-1",
  "repo_slug": "acme/widget",
  "target_url": "https://github.com/acme/widget/issues/42",
  "updated_at": "2026-04-08T12:00:00Z",
  "published_pr_number": 77,
  "sandbox_id": "issue-42-pr-123"
}`
	if err := os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	runID, err := ResolveGithubRunIDForTargetURL("https://github.com/acme/widget/issues/42")
	if err != nil {
		t.Fatalf("ResolveGithubRunIDForTargetURL(issue): %v", err)
	}
	if runID != "gh-run-1" {
		t.Fatalf("expected issue run id gh-run-1, got %q", runID)
	}

	runID, err = ResolveGithubRunIDForTargetURL("https://github.com/acme/widget/pull/77")
	if err != nil {
		t.Fatalf("ResolveGithubRunIDForTargetURL(pr): %v", err)
	}
	if runID != "gh-run-1" {
		t.Fatalf("expected pr run id gh-run-1, got %q", runID)
	}
}

func TestGithubIssueSyncNormalizesTargetURLToRunID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	runDir := filepath.Join(home, ".nana", "repos", "acme", "widget", "runs", "gh-run-2")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	manifest := `{
  "run_id": "gh-run-2",
  "repo_slug": "acme/widget",
  "target_url": "https://github.com/acme/widget/issues/42",
  "updated_at": "2026-04-08T12:30:00Z"
}`
	if err := os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	result, err := GithubIssue(".", []string{"sync", "https://github.com/acme/widget/issues/42", "--resume-last"})
	if err != nil {
		t.Fatalf("GithubIssue(sync): %v", err)
	}
	if result.Handled {
		t.Fatal("expected sync alias to require downstream routing")
	}
	expected := []string{"work-on", "sync", "--run-id", "gh-run-2", "https://github.com/acme/widget/issues/42", "--resume-last"}
	if strings.Join(result.LegacyArgs, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected normalized args:\nwant %#v\ngot  %#v", expected, result.LegacyArgs)
	}
}

func TestGithubReviewFollowupShowsPreexistingFindings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widget/pulls/7" {
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"number":7,"state":"closed"}`))
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	findingsPath := filepath.Join(home, ".nana", "repos", "acme", "widget", "reviews", "pr-7", "runs", "gr-1", "dropped-preexisting.json")
	if err := os.MkdirAll(filepath.Dir(findingsPath), 0o755); err != nil {
		t.Fatalf("mkdir findings dir: %v", err)
	}
	if err := os.WriteFile(findingsPath, []byte(`[
  {
    "fingerprint": "fp-1",
    "title": "Existing issue",
    "path": "src/api/client.ts",
    "line": 42,
    "detail": "Already existed on main.",
    "user_explanation": "Known pre-existing defect.",
    "main_permalink": "https://example.invalid/main"
  }
]`), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	output, err := captureStdout(t, func() error {
		_, err := GithubReview(".", []string{"followup", "https://github.com/acme/widget/pull/7"})
		return err
	})
	if err != nil {
		t.Fatalf("GithubReview(followup): %v", err)
	}
	if !strings.Contains(output, "Pre-existing findings for https://github.com/acme/widget/pull/7") {
		t.Fatalf("missing followup header: %q", output)
	}
	if !strings.Contains(output, "Existing issue (src/api/client.ts:42)") {
		t.Fatalf("missing finding reference: %q", output)
	}
	if !strings.Contains(output, "Known pre-existing defect.") {
		t.Fatalf("missing finding explanation: %q", output)
	}
	if !strings.Contains(output, "https://example.invalid/main") {
		t.Fatalf("missing finding link: %q", output)
	}
}
