package gocli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestResolveGithubWorkOnPolicyPrecedence(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(repo, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".nana", "github-workon"), 0o755); err != nil {
		t.Fatalf("mkdir global policy dir: %v", err)
	}
	if err := os.WriteFile(githubGlobalWorkOnPolicyPath(), []byte(`{"version":1,"experimental":false,"feedback_source":"manual","repo_native_strictness":"advisory","human_gate":"none"}`), 0o644); err != nil {
		t.Fatalf("write global policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".github", "nana-work-on-policy.json"), []byte(`{"version":1,"experimental":true,"feedback_source":"assigned_trusted","repo_native_strictness":"advisory","human_gate":"publish_time"}`), 0o644); err != nil {
		t.Fatalf("write .github policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "work-on-policy.json"), []byte(`{"version":1,"experimental":true,"feedback_source":"assigned_trusted","repo_native_strictness":"enforced","human_gate":"always"}`), 0o644); err != nil {
		t.Fatalf("write .nana policy: %v", err)
	}

	policy, err := resolveGithubWorkOnPolicy(repo)
	if err != nil {
		t.Fatalf("resolveGithubWorkOnPolicy: %v", err)
	}
	if !policy.Experimental || policy.RepoNativeStrictness != "enforced" || policy.HumanGate != "always" {
		t.Fatalf("unexpected resolved policy: %+v", policy)
	}
	if policy.SourceMap["human_gate"] != ".nana/work-on-policy.json" {
		t.Fatalf("expected .nana precedence, got %+v", policy.SourceMap)
	}
}

func TestGenerateGithubRepoProfileStableFingerprintAndWarnings(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/widget\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".github", "PULL_REQUEST_TEMPLATE.md"), []byte("## Custom\n\n## Bespoke\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	plan := detectGithubVerificationPlan(repo)
	first, err := generateGithubRepoProfile("acme/widget", repo, plan, []string{"qa"}, time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("generate first profile: %v", err)
	}
	second, err := generateGithubRepoProfile("acme/widget", repo, plan, []string{"qa"}, time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("generate second profile: %v", err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprint should be stable across timestamps: %s != %s", first.Fingerprint, second.Fingerprint)
	}
	if !slices.Contains(first.Warnings, "pull request template sections are unsupported; repo-native PR shaping disabled") {
		t.Fatalf("expected unsupported template warning, got %#v", first.Warnings)
	}
	body := buildDraftPullRequestBody(githubWorkonManifest{
		TargetURL:    "https://github.com/acme/widget/issues/1",
		TargetKind:   "issue",
		TargetNumber: 1,
		TargetTitle:  "Test",
		Policy:       &githubResolvedWorkOnPolicy{Experimental: true},
		RepoProfile:  &first,
	}, "nana/issue-1/test")
	if strings.Contains(body, "## Custom") || !strings.Contains(body, "Autogenerated by NANA work-on.") {
		t.Fatalf("unsupported template should fall back to generic body, got %q", body)
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

func TestGithubWorkOnExplainShowsPolicyAndProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	runID := "gh-explain-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".nana", "github-workon"), 0o755); err != nil {
		t.Fatalf("mkdir github-workon: %v", err)
	}
	profilePath := filepath.Join(managedRepoRoot, "repo-profile.json")
	if err := os.WriteFile(profilePath, []byte(`{
  "version": 1,
  "generated_at": "2026-04-10T00:00:00Z",
  "repo_slug": "acme/widget",
  "repo_path": "/tmp/widget",
  "fingerprint": "fp-1",
  "commit_style": {"kind":"conventional","confidence":0.9},
  "pull_request_template": {"path":".github/PULL_REQUEST_TEMPLATE.md"},
  "review_rules": {"approved_count":2,"pending_count":1,"disabled_count":0,"archived_count":0}
}`), 0o644); err != nil {
		t.Fatalf("write repo profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nana", "github-workon", "latest-run.json"), []byte(fmt.Sprintf(`{"repo_root":%q,"run_id":%q}`, managedRepoRoot, runID)), 0o644); err != nil {
		t.Fatalf("write latest-run: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "target_url": "https://github.com/acme/widget/issues/42",
  "repo_profile_path": %q,
  "control_plane_reviewers": ["reviewer-a","reviewer-b"],
  "needs_human": true,
  "needs_human_reason": "policy requires GitHub human feedback before completion",
  "next_action": "wait_for_github_feedback",
  "policy": {
    "version": 1,
    "experimental": true,
    "feedback_source": "assigned_trusted",
    "repo_native_strictness": "enforced",
    "human_gate": "always",
    "allowed_actions": {"commit":true,"push":true,"open_draft_pr":true,"request_review":false,"merge":false},
    "source_map": {"experimental":"global","feedback_source":".nana/work-on-policy.json","repo_native_strictness":".nana/work-on-policy.json","human_gate":".nana/work-on-policy.json"}
  }
}`, runID, profilePath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return GithubWorkOn(".", []string{"explain", "--last"})
	})
	if err != nil {
		t.Fatalf("GithubWorkOn(explain): %v", err)
	}
	if !strings.Contains(output, "Policy: experimental=true feedback_source=assigned_trusted repo_native=enforced human_gate=always") {
		t.Fatalf("missing policy line: %q", output)
	}
	if !strings.Contains(output, "Repo commit style: conventional (confidence 0.90)") {
		t.Fatalf("missing repo profile line: %q", output)
	}
	if !strings.Contains(output, "GitHub control plane: @reviewer-a, @reviewer-b") {
		t.Fatalf("missing control plane line: %q", output)
	}
}

func TestGithubWorkOnExplainHydratesLegacyManifestDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	runID := "gh-explain-legacy-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".nana", "github-workon"), 0o755); err != nil {
		t.Fatalf("mkdir github-workon: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nana", "github-workon", "latest-run.json"), []byte(fmt.Sprintf(`{"repo_root":%q,"run_id":%q}`, managedRepoRoot, runID)), 0o644); err != nil {
		t.Fatalf("write latest-run: %v", err)
	}
	manifest := `{
  "run_id": "gh-explain-legacy-1",
  "repo_slug": "acme/widget",
  "target_url": "https://github.com/acme/widget/issues/42",
  "review_reviewer": "legacy-reviewer"
}`
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return GithubWorkOn(".", []string{"explain", "--last"})
	})
	if err != nil {
		t.Fatalf("GithubWorkOn(explain legacy): %v", err)
	}
	if !strings.Contains(output, "Policy: experimental=false feedback_source=assigned_trusted repo_native=advisory human_gate=special_modes") {
		t.Fatalf("missing default legacy policy: %q", output)
	}
	if !strings.Contains(output, "GitHub control plane: @legacy-reviewer") {
		t.Fatalf("missing hydrated legacy reviewer: %q", output)
	}
	if !strings.Contains(output, "Review request state: not_requested") {
		t.Fatalf("missing hydrated review request state: %q", output)
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

func TestGithubIssueSyncExecutesNativelyFromTargetURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	runDir := filepath.Join(home, ".nana", "repos", "acme", "widget", "runs", "gh-run-2")
	repoCheckoutPath := filepath.Join(home, ".nana", "repos", "acme", "widget", "sandboxes", "issue-42", "repo")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": "gh-run-2",
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "target_url": "https://github.com/acme/widget/issues/42",
  "review_reviewer": "reviewer-a",
  "last_seen_issue_comment_id": 0,
  "last_seen_review_id": 0,
  "last_seen_review_comment_id": 0,
  "updated_at": "2026-04-08T12:30:00Z"
}`, filepath.Dir(repoCheckoutPath), repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/issues/42/comments?per_page=100":
			_, _ = w.Write([]byte(`[{"id":101,"html_url":"https://example.invalid/comment/101","body":"please update tests","updated_at":"2026-04-09T10:00:00Z","user":{"login":"reviewer-a"}}]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		_, err := GithubIssue(".", []string{"sync", "https://github.com/acme/widget/issues/42", "--resume-last"})
		return err
	})
	if err != nil {
		t.Fatalf("GithubIssue(sync): %v", err)
	}
	if !strings.Contains(output, "Stored new feedback for run gh-run-2") {
		t.Fatalf("unexpected sync output: %q", output)
	}
	if !strings.Contains(output, "fake-codex:exec -C "+repoCheckoutPath) {
		t.Fatalf("expected fake codex execution output, got %q", output)
	}
}

func TestGithubWorkOnCommandHandlesDefaultsLocally(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	result, err := GithubWorkOnCommand(".", []string{"defaults", "set", "acme/widget", "--considerations", "qa"})
	if err != nil {
		t.Fatalf("GithubWorkOnCommand(defaults): %v", err)
	}
	if !result.Handled {
		t.Fatal("expected defaults command to be handled in Go")
	}
}

func TestGithubWorkOnCommandStartExecutesNatively(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	originRepo := filepath.Join(home, "origin")
	if err := os.MkdirAll(originRepo, 0o755); err != nil {
		t.Fatalf("mkdir origin repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originRepo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originRepo, "package.json"), []byte(`{"name":"widget","scripts":{"lint":"eslint .","build":"tsc","test":"vitest"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = originRepo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	runGit("init", "-b", "main")
	runGit("add", ".")
	runGit("commit", "-m", "init")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widget":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"widget","full_name":"acme/widget","clone_url":%q,"default_branch":"main","html_url":"https://github.com/acme/widget"}`, originRepo)))
		case "/repos/acme/widget/issues/42":
			_, _ = w.Write([]byte(`{"title":"Start me","state":"open"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		_, err := GithubWorkOnCommand(".", []string{
			"start",
			"https://github.com/acme/widget/issues/42",
			"--reviewer",
			"@me",
			"--considerations",
			"qa,style",
			"--",
			"--model",
			"gpt-5.4",
		})
		return err
	})
	if err != nil {
		t.Fatalf("GithubWorkOnCommand(start): %v", err)
	}
	if !strings.Contains(output, "Starting run gh-") {
		t.Fatalf("missing start output: %q", output)
	}
	if !strings.Contains(output, "fake-codex:exec -C") {
		t.Fatalf("expected fake codex execution output, got %q", output)
	}
}

func TestGithubWorkOnCommandRejectsConflictingSyncSelectors(t *testing.T) {
	_, err := GithubWorkOnCommand(".", []string{"sync", "--run-id", "gh-1", "--last"})
	if err == nil {
		t.Fatal("expected sync selector conflict error")
	}
	if !strings.Contains(err.Error(), "Use either --run-id <id> or --last, not both.") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGithubWorkOnCommandValidatesLaneExecShape(t *testing.T) {
	_, err := GithubWorkOnCommand(".", []string{"lane-exec", "--last", "--task", "verify"})
	if err == nil {
		t.Fatal("expected lane-exec validation error")
	}
	if !strings.Contains(err.Error(), "Usage: nana work-on lane-exec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGithubWorkOnVerifyRefreshExecutesNatively(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-refresh-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoCheckoutPath, "package.json"), []byte(`{"name":"widget","scripts":{"lint":"eslint .","build":"tsc","test":"vitest"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		_, err := GithubWorkOnCommand(".", []string{"verify-refresh", "--run-id", runID})
		return err
	})
	if err != nil {
		t.Fatalf("GithubWorkOnCommand(verify-refresh): %v", err)
	}
	if !strings.Contains(output, "Verification artifacts for run gh-run-refresh-1 refreshed.") {
		t.Fatalf("unexpected verify-refresh output: %q", output)
	}

	manifestPath := filepath.Join(managedRepoRoot, "runs", runID, "manifest.json")
	var updated githubWorkonManifest
	if err := readGithubJSON(manifestPath, &updated); err != nil {
		t.Fatalf("read updated manifest: %v", err)
	}
	if updated.VerificationPlan == nil || updated.VerificationPlan.PlanFingerprint == "" {
		t.Fatalf("expected verification plan in manifest, got %+v", updated)
	}
	if updated.VerificationScriptsDir == "" {
		t.Fatalf("expected verification scripts dir in manifest, got %+v", updated)
	}
	for _, script := range []string{"refresh.sh", "all.sh", "worker-done.sh", "lint.sh", "compile.sh", "unit-tests.sh", "integration-tests.sh"} {
		if _, err := os.Stat(filepath.Join(updated.VerificationScriptsDir, script)); err != nil {
			t.Fatalf("expected verification script %s: %v", script, err)
		}
	}
	if _, err := os.Stat(filepath.Join(managedRepoRoot, "verification-plan.json")); err != nil {
		t.Fatalf("expected repo verification plan: %v", err)
	}
}

func TestGithubWorkOnLaneExecExecutesNativelyForNonPublisherLane(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-lane-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "consideration_pipeline": [
    {
      "alias": "coder",
      "role": "executor",
      "prompt_roles": ["executor"],
      "activation": "bootstrap",
      "phase": "impl",
      "mode": "execute",
      "owner": "self",
      "blocking": true,
      "purpose": "Implement the requested change."
    }
  ]
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		_, err := GithubWorkOnCommand(".", []string{"lane-exec", "--run-id", runID, "--lane", "coder", "--task", "implement", "--", "--model", "gpt-5.4"})
		return err
	})
	if err != nil {
		t.Fatalf("GithubWorkOnCommand(lane-exec): %v", err)
	}
	if !strings.Contains(output, "Lane coder completed via isolated CODEX_HOME") {
		t.Fatalf("unexpected lane-exec output: %q", output)
	}
	if !strings.Contains(output, "fake-codex:exec -C "+repoCheckoutPath) {
		t.Fatalf("expected fake codex execution output, got %q", output)
	}
	runtimeDir := filepath.Join(managedRepoRoot, "runs", runID, "lane-runtime")
	if _, err := os.Stat(filepath.Join(runtimeDir, "coder-instructions.md")); err != nil {
		t.Fatalf("expected instructions file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "coder-result.md")); err != nil {
		t.Fatalf("expected result file: %v", err)
	}
}

func TestGithubWorkOnSyncExecutesNatively(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-sync-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "target_url": "https://github.com/acme/widget/issues/42",
  "review_reviewer": "reviewer-a",
  "last_seen_issue_comment_id": 0,
  "last_seen_review_id": 0,
  "last_seen_review_comment_id": 0
}`, runID, sandboxPath, repoCheckoutPath)
	manifestPath := filepath.Join(managedRepoRoot, "runs", runID, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/issues/42/comments?per_page=100":
			_, _ = w.Write([]byte(`[{"id":101,"html_url":"https://example.invalid/comment/101","body":"please update tests","updated_at":"2026-04-09T10:00:00Z","user":{"login":"reviewer-a"}}]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		_, err := GithubWorkOnCommand(".", []string{"sync", "--run-id", runID, "--", "--model", "gpt-5.4"})
		return err
	})
	if err != nil {
		t.Fatalf("GithubWorkOnCommand(sync): %v", err)
	}
	if !strings.Contains(output, "Stored new feedback for run "+runID) {
		t.Fatalf("unexpected sync output: %q", output)
	}
	if !strings.Contains(output, "fake-codex:exec -C "+repoCheckoutPath) {
		t.Fatalf("expected fake codex execution output, got %q", output)
	}
	updatedManifest, readErr := readGithubWorkonManifest(manifestPath)
	if readErr != nil {
		t.Fatalf("read manifest: %v", readErr)
	}
	if updatedManifest.LastSeenIssueCommentID != 101 {
		t.Fatalf("expected feedback cursor update, got %+v", updatedManifest)
	}
	if _, err := os.Stat(filepath.Join(managedRepoRoot, "runs", runID, "feedback-instructions.md")); err != nil {
		t.Fatalf("expected feedback instructions: %v", err)
	}
}

func TestGithubWorkOnSyncBuildsActorSetFromAssignedTrustedAndRequestedReviewers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-sync-actors-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "published_pr_number": 77,
  "target_url": "https://github.com/acme/widget/issues/42",
  "effective_reviewer_policy": {
    "trusted_reviewers": ["trusted-a"],
    "blocked_reviewers": ["blocked"],
    "min_distinct_reviewers": 1
  },
  "policy": {
    "version": 1,
    "experimental": true,
    "feedback_source": "assigned_trusted",
    "repo_native_strictness": "advisory",
    "human_gate": "always",
    "allowed_actions": {"commit":true,"push":true,"open_draft_pr":true,"request_review":false,"merge":false},
    "source_map": {"experimental":"global"}
  },
  "last_seen_issue_comment_id": 0,
  "last_seen_review_id": 0,
  "last_seen_review_comment_id": 0
}`, runID, sandboxPath, repoCheckoutPath)
	manifestPath := filepath.Join(managedRepoRoot, "runs", runID, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/issues/42?":
			_, _ = w.Write([]byte(`{"assignees":[{"login":"assigned-a"}]}`))
		case "/repos/acme/widget/issues/42/comments?per_page=100":
			_, _ = w.Write([]byte(`[
{"id":101,"html_url":"https://example.invalid/comment/101","body":"please update tests","updated_at":"2026-04-09T10:00:00Z","user":{"login":"assigned-a"}},
{"id":102,"html_url":"https://example.invalid/comment/102","body":"ignore me","updated_at":"2026-04-09T10:01:00Z","user":{"login":"blocked"}}
]`))
		case "/repos/acme/widget/pulls/77/requested_reviewers?":
			_, _ = w.Write([]byte(`{"users":[{"login":"requested-a"},{"login":"blocked"}]}`))
		case "/repos/acme/widget/pulls/77/reviews?per_page=100":
			_, _ = w.Write([]byte(`[{"id":201,"html_url":"https://example.invalid/review/201","body":"looks good after tests","state":"COMMENTED","user":{"login":"trusted-a"}}]`))
		case "/repos/acme/widget/pulls/77/comments?per_page=100":
			_, _ = w.Write([]byte(`[{"id":301,"html_url":"https://example.invalid/review-comment/301","body":"nit","path":"README.md","line":1,"user":{"login":"requested-a"}}]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		_, err := GithubWorkOnCommand(".", []string{"sync", "--run-id", runID})
		return err
	})
	if err != nil {
		t.Fatalf("GithubWorkOnCommand(sync actors): %v", err)
	}
	if !strings.Contains(output, "Feedback actors: @trusted-a, @assigned-a, @requested-a") &&
		!strings.Contains(output, "Feedback actors: @assigned-a, @requested-a, @trusted-a") &&
		!strings.Contains(output, "Feedback actors: @assigned-a, @trusted-a, @requested-a") {
		t.Fatalf("unexpected feedback actors output: %q", output)
	}
	updatedManifest, err := readGithubWorkonManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if slices.Contains(updatedManifest.ControlPlaneReviewers, "blocked") {
		t.Fatalf("blocked reviewer should not be present: %+v", updatedManifest.ControlPlaneReviewers)
	}
	if !slices.Contains(updatedManifest.ControlPlaneReviewers, "trusted-a") || !slices.Contains(updatedManifest.ControlPlaneReviewers, "assigned-a") || !slices.Contains(updatedManifest.ControlPlaneReviewers, "requested-a") {
		t.Fatalf("missing control plane reviewers: %+v", updatedManifest.ControlPlaneReviewers)
	}
}

func TestGithubAnyHumanFeedbackFiltersBotsAuthorAndBlocked(t *testing.T) {
	manifest := githubWorkonManifest{
		RepoSlug:     "acme/widget",
		TargetKind:   "issue",
		TargetNumber: 42,
		TargetAuthor: "author-a",
		EffectiveReviewerPolicy: &githubReviewerPolicy{
			BlockedReviewers: []string{"blocked-a"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/issues/42/comments?per_page=100":
			_, _ = w.Write([]byte(`[
{"id":101,"html_url":"https://example.invalid/101","body":"use me","updated_at":"2026-04-11T10:00:00Z","user":{"login":"human-a"}},
{"id":102,"html_url":"https://example.invalid/102","body":"author","updated_at":"2026-04-11T10:00:01Z","user":{"login":"author-a"}},
{"id":103,"html_url":"https://example.invalid/103","body":"bot","updated_at":"2026-04-11T10:00:02Z","user":{"login":"dependabot[bot]"}},
{"id":104,"html_url":"https://example.invalid/104","body":"blocked","updated_at":"2026-04-11T10:00:03Z","user":{"login":"blocked-a"}}
]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	snapshot, err := fetchGithubFeedbackSnapshot(manifest, []string{"*"}, server.URL, "token", "")
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	if len(snapshot.IssueComments) != 1 || snapshot.IssueComments[0].User.Login != "human-a" {
		t.Fatalf("expected only human-a feedback, got %+v", snapshot.IssueComments)
	}
	if snapshot.IgnoredActors["author"] != 1 || snapshot.IgnoredActors["bot"] != 1 || snapshot.IgnoredActors["blocked"] != 1 {
		t.Fatalf("expected ignored actor reasons, got %+v", snapshot.IgnoredActors)
	}
}

func TestGithubWorkOnPublisherLaneExecutesNatively(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	originBare := filepath.Join(home, "origin.git")
	seedRepo := filepath.Join(home, "seed")
	if err := os.MkdirAll(seedRepo, 0o755); err != nil {
		t.Fatalf("mkdir seed repo: %v", err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	runGit(home, "init", "--bare", originBare)
	if err := os.WriteFile(filepath.Join(seedRepo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(seedRepo, "init", "-b", "main")
	runGit(seedRepo, "add", ".")
	runGit(seedRepo, "commit", "-m", "init")
	runGit(seedRepo, "remote", "add", "origin", originBare)
	runGit(seedRepo, "push", "-u", "origin", "main")

	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-publisher-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	runGit(home, "clone", originBare, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(repoCheckoutPath, "CHANGELOG.md"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "target_title": "Publish me",
  "target_url": "https://github.com/acme/widget/issues/42",
  "considerations_active": ["qa"],
  "role_layout": "split",
  "default_branch": "main",
  "create_pr_on_complete": true
}`, runID, sandboxPath, repoCheckoutPath)
	manifestPath := filepath.Join(managedRepoRoot, "runs", runID, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/pulls?state=open&head=acme%3Anana%2Fissue-42%2Fissue-42&base=main":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/acme/widget/commits/main/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case "/repos/acme/widget/actions/runs?head_sha=main&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "/repos/acme/widget/commits/HEAD/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		default:
			if r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/pulls" {
				_, _ = w.Write([]byte(`{"number":77,"html_url":"https://example.invalid/pr/77","head":{"sha":"head-sha"}}`))
				return
			}
			if strings.HasPrefix(r.URL.Path, "/repos/acme/widget/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs") {
				_, _ = w.Write([]byte(`{"check_runs":[]}`))
				return
			}
			if r.URL.Path == "/repos/acme/widget/actions/runs" {
				_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
				return
			}
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		_, err := GithubWorkOnCommand(".", []string{"lane-exec", "--run-id", runID, "--lane", "publisher"})
		return err
	})
	if err != nil {
		t.Fatalf("GithubWorkOnCommand(publisher): %v", err)
	}
	if !strings.Contains(output, "Created draft PR #77") {
		t.Fatalf("unexpected publisher output: %q", output)
	}
	if !strings.Contains(output, "Lane publisher completed via native publication flow.") {
		t.Fatalf("missing publisher completion output: %q", output)
	}
	updatedManifest, readErr := readGithubWorkonManifest(manifestPath)
	if readErr != nil {
		t.Fatalf("read manifest: %v", readErr)
	}
	if updatedManifest.PublishedPRNumber != 77 {
		t.Fatalf("expected PR number update, got %+v", updatedManifest)
	}
	if updatedManifest.PublicationState != "ci_waiting" {
		t.Fatalf("expected ci_waiting publication state, got %+v", updatedManifest)
	}
}

func TestGithubWorkOnPublisherUsesRepoNativeTemplateAndCommitStyleWhenExperimental(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	originBare := filepath.Join(home, "origin.git")
	seedRepo := filepath.Join(home, "seed")
	if err := os.MkdirAll(seedRepo, 0o755); err != nil {
		t.Fatalf("mkdir seed repo: %v", err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	runGit(home, "init", "--bare", originBare)
	if err := os.WriteFile(filepath.Join(seedRepo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(seedRepo, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedRepo, ".github", "PULL_REQUEST_TEMPLATE.md"), []byte("## Summary\n\n## Changes\n\n## Validation\n\n## Checklist\n\n## Related\n"), 0o644); err != nil {
		t.Fatalf("write pr template: %v", err)
	}
	runGit(seedRepo, "init", "-b", "main")
	runGit(seedRepo, "add", ".")
	runGit(seedRepo, "commit", "-m", "feat: init")
	runGit(seedRepo, "remote", "add", "origin", originBare)
	runGit(seedRepo, "push", "-u", "origin", "main")

	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42-native")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-publisher-native-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	runGit(home, "clone", originBare, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(repoCheckoutPath, "CHANGELOG.md"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42-native",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "target_title": "Publish me",
  "target_url": "https://github.com/acme/widget/issues/42",
  "considerations_active": ["qa"],
  "role_layout": "split",
  "default_branch": "main",
  "create_pr_on_complete": true,
  "verification_plan": {"lint":["go test ./..."],"unit":["go test ./..."]},
  "policy": {
    "version": 1,
    "experimental": true,
    "feedback_source": "assigned_trusted",
    "repo_native_strictness": "enforced",
    "human_gate": "publish_time",
    "allowed_actions": {"commit":true,"push":true,"open_draft_pr":true,"request_review":false,"merge":false},
    "source_map": {"experimental":"global"}
  },
  "repo_profile": {
    "fingerprint": "fp-native",
    "commit_style": {"kind":"conventional","confidence":0.95},
    "pull_request_template": {"path":".github/PULL_REQUEST_TEMPLATE.md","sections":["Summary","Changes","Validation","Checklist","Related"]}
  }
}`, runID, sandboxPath, repoCheckoutPath)
	manifestPath := filepath.Join(managedRepoRoot, "runs", runID, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	var postedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/pulls?state=open&head=acme%3Anana%2Fissue-42%2Fissue-42-native&base=main":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/acme/widget/commits/main/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case "/repos/acme/widget/actions/runs?head_sha=main&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		default:
			if r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/pulls" {
				var raw map[string]any
				_ = json.NewDecoder(r.Body).Decode(&raw)
				if body, ok := raw["body"].(string); ok {
					postedBody = body
				}
				_, _ = w.Write([]byte(`{"number":77,"html_url":"https://example.invalid/pr/77","head":{"sha":"head-sha"}}`))
				return
			}
			if strings.HasPrefix(r.URL.Path, "/repos/acme/widget/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs") {
				_, _ = w.Write([]byte(`{"check_runs":[]}`))
				return
			}
			if r.URL.Path == "/repos/acme/widget/actions/runs" {
				_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
				return
			}
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	if _, err := captureStdout(t, func() error {
		_, err := GithubWorkOnCommand(".", []string{"lane-exec", "--run-id", runID, "--lane", "publisher"})
		return err
	}); err != nil {
		t.Fatalf("GithubWorkOnCommand(publisher native): %v", err)
	}

	logOutput, err := githubGitOutput(repoCheckoutPath, "log", "-1", "--format=%s")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.TrimSpace(logOutput) != "chore: publish issue #42" {
		t.Fatalf("unexpected commit subject: %q", logOutput)
	}
	if !strings.Contains(postedBody, "## Summary") || !strings.Contains(postedBody, "- [x] `go test ./...`") || !strings.Contains(postedBody, "## Related") {
		t.Fatalf("expected repo-native PR body, got %q", postedBody)
	}
}

func TestGithubWorkOnPublisherRequestsControlPlaneReviewsWhenAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	originBare := filepath.Join(home, "origin.git")
	seedRepo := filepath.Join(home, "seed")
	if err := os.MkdirAll(seedRepo, 0o755); err != nil {
		t.Fatalf("mkdir seed repo: %v", err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	runGit(home, "init", "--bare", originBare)
	if err := os.WriteFile(filepath.Join(seedRepo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(seedRepo, "init", "-b", "main")
	runGit(seedRepo, "add", ".")
	runGit(seedRepo, "commit", "-m", "init")
	runGit(seedRepo, "remote", "add", "origin", originBare)
	runGit(seedRepo, "push", "-u", "origin", "main")

	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42-request-review")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-publisher-request-review-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	runGit(home, "clone", originBare, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(repoCheckoutPath, "CHANGELOG.md"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42-request-review",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "target_title": "Publish me",
  "target_url": "https://github.com/acme/widget/issues/42",
  "considerations_active": ["qa"],
  "role_layout": "split",
  "default_branch": "main",
  "create_pr_on_complete": true,
  "control_plane_reviewers": ["reviewer-a","reviewer-b","*"],
  "policy": {
    "version": 1,
    "experimental": true,
    "feedback_source": "assigned_trusted",
    "repo_native_strictness": "advisory",
    "human_gate": "publish_time",
    "allowed_actions": {"commit":true,"push":true,"open_draft_pr":true,"request_review":true,"merge":false},
    "source_map": {"experimental":"global"}
  }
}`, runID, sandboxPath, repoCheckoutPath)
	manifestPath := filepath.Join(managedRepoRoot, "runs", runID, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	var requestedPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/pulls?state=open&head=acme%3Anana%2Fissue-42%2Fissue-42-request-review&base=main":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/acme/widget/commits/main/check-runs?per_page=100":
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case "/repos/acme/widget/actions/runs?head_sha=main&per_page=100":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "/repos/acme/widget/pulls/77/requested_reviewers?":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"users":[{"login":"reviewer-a"}]}`))
				return
			}
			if r.Method == http.MethodPost {
				var raw map[string]any
				_ = json.NewDecoder(r.Body).Decode(&raw)
				payload, _ := json.Marshal(raw)
				requestedPayload = string(payload)
				_, _ = w.Write([]byte(`{"users":[{"login":"reviewer-a"},{"login":"reviewer-b"}]}`))
				return
			}
		default:
			if r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/pulls" {
				_, _ = w.Write([]byte(`{"number":77,"html_url":"https://example.invalid/pr/77","head":{"sha":"head-sha"}}`))
				return
			}
			if strings.HasPrefix(r.URL.Path, "/repos/acme/widget/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs") {
				_, _ = w.Write([]byte(`{"check_runs":[]}`))
				return
			}
			if r.URL.Path == "/repos/acme/widget/actions/runs" {
				_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
				return
			}
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		_, err := GithubWorkOnCommand(".", []string{"lane-exec", "--run-id", runID, "--lane", "publisher"})
		return err
	})
	if err != nil {
		t.Fatalf("GithubWorkOnCommand(publisher request review): %v", err)
	}
	if !strings.Contains(requestedPayload, `"reviewers":["reviewer-b"]`) {
		t.Fatalf("expected only missing eligible reviewer to be requested, got %s", requestedPayload)
	}
	updatedManifest, err := readGithubWorkonManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if updatedManifest.ReviewRequestState != "requested" {
		t.Fatalf("expected requested review state, got %+v", updatedManifest)
	}
	if !slices.Contains(updatedManifest.RequestedReviewers, "reviewer-a") || !slices.Contains(updatedManifest.RequestedReviewers, "reviewer-b") {
		t.Fatalf("expected requested reviewers in manifest, got %+v", updatedManifest.RequestedReviewers)
	}
	if !strings.Contains(output, "Lane publisher completed via native publication flow.") {
		t.Fatalf("unexpected publisher output: %q", output)
	}
}

func TestEnsureGithubMergeRequiresGreenCIAndApproval(t *testing.T) {
	manifest := githubWorkonManifest{
		RepoSlug:              "acme/widget",
		ControlPlaneReviewers: []string{"reviewer-a"},
		Policy:                &githubResolvedWorkOnPolicy{Experimental: true, MergeMethod: "squash", AllowedActions: githubWorkOnActionPolicy{Merge: true}},
	}
	pr := githubPullRequestResponse{Number: 77}

	state, _, reason, err := ensureGithubMerge(manifest, pr, "ci_waiting", "https://example.invalid", "token")
	if err != nil {
		t.Fatalf("merge waiting: %v", err)
	}
	if state != "blocked" || reason != "GitHub CI is not green" {
		t.Fatalf("expected CI gate block, got state=%s reason=%q", state, reason)
	}

	pr.Draft = true
	state, _, reason, err = ensureGithubMerge(manifest, pr, "ci_green", "https://example.invalid", "token")
	if err != nil {
		t.Fatalf("merge draft: %v", err)
	}
	if state != "blocked" || reason != "pull request is draft" {
		t.Fatalf("expected draft gate block, got state=%s reason=%q", state, reason)
	}
}

func TestEnsureGithubMergeBlocksOnChangesRequestedAndSucceedsOnApproval(t *testing.T) {
	manifest := githubWorkonManifest{
		RepoSlug:              "acme/widget",
		ControlPlaneReviewers: []string{"reviewer-a"},
		Policy:                &githubResolvedWorkOnPolicy{Experimental: true, MergeMethod: "squash", AllowedActions: githubWorkOnActionPolicy{Merge: true}},
	}
	pr := githubPullRequestResponse{Number: 77}
	pr.Head.SHA = "head-sha"
	reviewState := "CHANGES_REQUESTED"
	mergeCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget/pulls/77/reviews":
			_, _ = w.Write([]byte(fmt.Sprintf(`[{"id":1,"html_url":"https://example.invalid/review","body":"","state":%q,"user":{"login":"reviewer-a"}}]`, reviewState)))
		case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/widget/pulls/77/merge":
			mergeCalled = true
			var raw map[string]any
			_ = json.NewDecoder(r.Body).Decode(&raw)
			if raw["merge_method"] != "squash" {
				t.Fatalf("unexpected merge method payload: %#v", raw)
			}
			_, _ = w.Write([]byte(`{"merged":true,"sha":"merged-sha","message":"merged"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	state, _, reason, err := ensureGithubMerge(manifest, pr, "ci_green", server.URL, "token")
	if err != nil {
		t.Fatalf("merge changes requested: %v", err)
	}
	if state != "blocked" || !strings.Contains(reason, "requests changes") || mergeCalled {
		t.Fatalf("expected changes-requested block without merge, state=%s reason=%q mergeCalled=%t", state, reason, mergeCalled)
	}

	reviewState = "APPROVED"
	state, sha, reason, err := ensureGithubMerge(manifest, pr, "ci_green", server.URL, "token")
	if err != nil {
		t.Fatalf("merge approved: %v", err)
	}
	if state != "merged" || sha != "merged-sha" || reason != "" || !mergeCalled {
		t.Fatalf("expected successful merge, state=%s sha=%s reason=%q mergeCalled=%t", state, sha, reason, mergeCalled)
	}
}

func TestGithubIssueRejectsPRForImplementAndInvestigate(t *testing.T) {
	for _, args := range [][]string{
		{"implement", "https://github.com/acme/widget/pull/7"},
		{"investigate", "https://github.com/acme/widget/pull/7"},
	} {
		_, err := GithubIssue(".", args)
		if err == nil {
			t.Fatalf("expected error for %v", args)
		}
		if !strings.Contains(err.Error(), "expects a GitHub issue URL") {
			t.Fatalf("unexpected error for %v: %v", args, err)
		}
	}
}

func TestGithubIssueInvestigateExecutesNatively(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	originRepo := filepath.Join(home, "origin")
	if err := os.MkdirAll(originRepo, 0o755); err != nil {
		t.Fatalf("mkdir origin repo: %v", err)
	}
	writeFile := func(path string, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeFile(filepath.Join(originRepo, "README.md"), "# widget\n")
	writeFile(filepath.Join(originRepo, "package.json"), `{"name":"widget","scripts":{"lint":"eslint .","build":"tsc","test":"vitest"}}`)
	writeFile(filepath.Join(originRepo, "openapi.yaml"), "openapi: 3.0.0\n")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	run(originRepo, "init", "-b", "main")
	run(originRepo, "add", ".")
	run(originRepo, "commit", "-m", "init")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widget":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"widget","full_name":"acme/widget","clone_url":%q,"default_branch":"main","html_url":"https://github.com/acme/widget"}`, originRepo)))
		case "/repos/acme/widget/issues/42":
			_, _ = w.Write([]byte(`{"title":"Investigate me","state":"open"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		_, err := GithubIssue(".", []string{"investigate", "https://github.com/acme/widget/issues/42"})
		return err
	})
	if err != nil {
		t.Fatalf("GithubIssue(investigate): %v", err)
	}
	if !strings.Contains(output, "Investigated acme/widget issue #42") {
		t.Fatalf("missing investigate header: %q", output)
	}
	if !strings.Contains(output, "Suggested considerations: api, dependency, qa, style") {
		t.Fatalf("missing inferred considerations: %q", output)
	}
	if !strings.Contains(output, "Verification plan: lint=1 compile=1 unit=1 integration=1") {
		t.Fatalf("missing verification plan summary: %q", output)
	}

	repoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	if _, err := os.Stat(filepath.Join(repoRoot, "repo.json")); err != nil {
		t.Fatalf("expected repo metadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "settings.json")); err != nil {
		t.Fatalf("expected repo settings: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "verification-plan.json")); err != nil {
		t.Fatalf("expected verification plan: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "source", "README.md")); err != nil {
		t.Fatalf("expected source clone: %v", err)
	}
}

func TestGithubReviewCommandValidatesReviewArgsBeforeBridge(t *testing.T) {
	if _, err := parseGithubReviewExecutionArgs([]string{
		"https://github.com/acme/widget/pull/7",
		"--mode",
		"manual",
		"--reviewer",
		"@me",
		"--per-item-context",
		"isolated",
	}); err != nil {
		t.Fatalf("parseGithubReviewExecutionArgs: %v", err)
	}
}

func TestGithubReviewCommandRejectsInvalidExecutionArgs(t *testing.T) {
	_, err := parseGithubReviewExecutionArgs([]string{
		"https://github.com/acme/widget/pull/7",
		"--mode",
		"broken",
	})
	if err == nil {
		t.Fatal("expected invalid mode error")
	}
	if !strings.Contains(err.Error(), "Invalid --mode value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGithubReviewExecutesNatively(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\nprintf '{\"findings\":[{\"title\":\"Broken check\",\"severity\":\"medium\",\"path\":\"CHANGELOG.md\",\"line\":1,\"summary\":\"summary\",\"detail\":\"detail\",\"fix\":\"fix\",\"rationale\":\"why\"}]}'\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	originBare := filepath.Join(home, "origin.git")
	seedRepo := filepath.Join(home, "seed")
	if err := os.MkdirAll(seedRepo, 0o755); err != nil {
		t.Fatalf("mkdir seed repo: %v", err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	runGit(home, "init", "--bare", originBare)
	if err := os.WriteFile(filepath.Join(seedRepo, "CHANGELOG.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(seedRepo, "init", "-b", "main")
	runGit(seedRepo, "add", ".")
	runGit(seedRepo, "commit", "-m", "init")
	runGit(seedRepo, "remote", "add", "origin", originBare)
	runGit(seedRepo, "push", "-u", "origin", "main")
	runGit(seedRepo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(seedRepo, "CHANGELOG.md"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(seedRepo, "add", ".")
	runGit(seedRepo, "commit", "-m", "feature")
	headSHABytes, _ := exec.Command("git", "-C", seedRepo, "rev-parse", "HEAD").Output()
	headSHA := strings.TrimSpace(string(headSHABytes))
	baseSHABytes, _ := exec.Command("git", "-C", seedRepo, "rev-parse", "main").Output()
	baseSHA := strings.TrimSpace(string(baseSHABytes))
	runGit(seedRepo, "push", "-u", "origin", "feature")

	var postedReviewBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"reviewer-a"}`))
		case r.URL.Path == "/repos/acme/widget":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"widget","full_name":"acme/widget","clone_url":%q,"default_branch":"main","html_url":"https://github.com/acme/widget"}`, originBare)))
		case r.URL.Path == "/repos/acme/widget/issues/7":
			_, _ = w.Write([]byte(`{"title":"Review me","state":"open","pull_request":{"url":"https://api.github.com/repos/acme/widget/pulls/7"}}`))
		case r.URL.Path == "/repos/acme/widget/pulls/7":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"number":7,"html_url":"https://example.invalid/pr/7","head":{"ref":"feature","sha":%q,"repo":{"full_name":"acme/widget"}},"base":{"ref":"main","sha":%q,"repo":{"full_name":"acme/widget"}}}`, headSHA, baseSHA)))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/pulls/7/reviews":
			var raw map[string]any
			_ = json.NewDecoder(r.Body).Decode(&raw)
			payload, _ := json.Marshal(raw)
			postedReviewBody = string(payload)
			_, _ = w.Write([]byte(`{"id":91,"html_url":"https://example.invalid/review/91"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	output, err := captureStdout(t, func() error {
		_, err := GithubReview(".", []string{"https://github.com/acme/widget/pull/7"})
		return err
	})
	if err != nil {
		t.Fatalf("GithubReview(review): %v", err)
	}
	if !strings.Contains(output, "Completed review for https://github.com/acme/widget/pull/7") {
		t.Fatalf("unexpected review output: %q", output)
	}
	if !strings.Contains(postedReviewBody, `"event":"REQUEST_CHANGES"`) {
		t.Fatalf("expected review submission payload, got %s", postedReviewBody)
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

func TestGithubCommitStyleLowConfidenceFallsBackToGenericPublicationMessage(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit("init", "-b", "main")
	runGit("add", ".")
	runGit("commit", "-m", "feat: init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("update README: %v", err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "plain update")

	style := detectGithubCommitStyle(repo)
	if style.Kind != "generic" || style.Confidence != 0.5 {
		t.Fatalf("expected low-confidence generic style, got %+v", style)
	}
	message := buildGithubPublicationCommitMessage(githubWorkonManifest{
		TargetKind:   "issue",
		TargetNumber: 42,
		Policy:       &githubResolvedWorkOnPolicy{Experimental: true},
		RepoProfile:  &githubRepoProfile{CommitStyle: style},
	})
	if message != "nana: publish issue #42" {
		t.Fatalf("expected generic publication message, got %q", message)
	}
}

func TestGithubWorkOnExplainJSONIncludesReviewMergeAndIgnoredActorState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	runID := "gh-explain-state-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".nana", "github-workon"), 0o755); err != nil {
		t.Fatalf("mkdir github-workon: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nana", "github-workon", "latest-run.json"), []byte(fmt.Sprintf(`{"repo_root":%q,"run_id":%q}`, managedRepoRoot, runID)), 0o644); err != nil {
		t.Fatalf("write latest-run: %v", err)
	}
	manifest := `{
  "run_id": "gh-explain-state-1",
  "repo_slug": "acme/widget",
  "target_url": "https://github.com/acme/widget/issues/42",
  "ignored_feedback_actors": {"bot": 1, "author": 2},
  "requested_reviewers": ["reviewer-a"],
  "review_request_state": "requested",
  "merge_state": "blocked",
  "merge_error": "GitHub CI is not green",
  "merge_method": "squash"
}`
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return GithubWorkOn(".", []string{"explain", "--last", "--json"})
	})
	if err != nil {
		t.Fatalf("GithubWorkOn(explain --json): %v", err)
	}
	var payload githubExplainPayload
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal explain json: %v\n%s", err, output)
	}
	if payload.IgnoredFeedbackActors["bot"] != 1 || payload.IgnoredFeedbackActors["author"] != 2 {
		t.Fatalf("expected ignored actor counts, got %+v", payload.IgnoredFeedbackActors)
	}
	if payload.ReviewRequestState != "requested" || payload.MergeState != "blocked" || payload.MergeError != "GitHub CI is not green" {
		t.Fatalf("missing review/merge state: %+v", payload)
	}
}

func TestGithubPublisherBlocksDraftPROpenWhenPolicyDisablesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	managedRepoRoot := filepath.Join(home, ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42-block-open-pr")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-open-pr-blocked-1"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "policy": {
    "version": 1,
    "experimental": true,
    "allowed_actions": {"commit":true,"push":true,"open_draft_pr":false,"request_review":false,"merge":false}
  }
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	_, err := GithubWorkOnCommand(".", []string{"lane-exec", "--run-id", runID, "--lane", "publisher"})
	if err == nil || !strings.Contains(err.Error(), "open_draft_pr action is disabled") {
		t.Fatalf("expected open_draft_pr policy block, got %v", err)
	}
}
