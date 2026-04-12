package gocli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImprovementPolicyPrecedenceAndLabels(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".github", "nana-improvement-policy.json"), []byte(`{
  "version": 1,
  "issue_destination": "target",
  "labels": ["enhancement", "ux"]
}`), 0o644); err != nil {
		t.Fatalf("write .github policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{
  "version": 1,
  "issue_destination": "fork",
  "fork_repo": "me/widget",
  "labels": ["perf", "enhancement"]
}`), 0o644); err != nil {
		t.Fatalf("write .nana policy: %v", err)
	}

	policy := readImprovementPolicy(repo)
	if policy.IssueDestination != improvementDestinationFork {
		t.Fatalf("expected .nana policy to override destination, got %#v", policy)
	}
	if policy.ForkRepo != "me/widget" {
		t.Fatalf("expected fork repo from .nana policy, got %#v", policy)
	}
	if strings.Join(policy.Labels, ",") != "improvement,improvement-scout,perf" {
		t.Fatalf("expected normalized improvement labels, got %#v", policy.Labels)
	}
	if policy.MaxIssues != 5 {
		t.Fatalf("expected default max issue cap of 5, got %#v", policy)
	}
}

func TestScoutLocalFromFileCapsAtFiveForBothRoles(t *testing.T) {
	for _, tc := range []struct {
		name       string
		run        func(string, []string) error
		root       string
		globPrefix string
		wantText   string
	}{
		{
			name:       "improvement",
			run:        Improve,
			root:       "improvements",
			globPrefix: "improve-*",
			wantText:   "Labels: improvement, improvement-scout, docs",
		},
		{
			name:       "enhancement",
			run:        Enhance,
			root:       "enhancements",
			globPrefix: "enhance-*",
			wantText:   "Labels: enhancement, enhancement-scout, docs",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			inputPath := filepath.Join(repo, "proposals.json")
			if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(7, "docs")), 0o644); err != nil {
				t.Fatalf("write proposals: %v", err)
			}
			output, err := captureStdout(t, func() error {
				return tc.run(repo, []string{"--from-file", inputPath})
			})
			if err != nil {
				t.Fatalf("run scout: %v", err)
			}
			if !strings.Contains(output, "Keeping 5 proposal(s) local by policy") {
				t.Fatalf("expected capped local output, got %q", output)
			}
			matches, err := filepath.Glob(filepath.Join(repo, ".nana", tc.root, tc.globPrefix, "proposals.json"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("expected one proposals artifact, matches=%#v err=%v", matches, err)
			}
			var report improvementReport
			if err := readGithubJSON(matches[0], &report); err != nil {
				t.Fatalf("read report: %v", err)
			}
			if len(report.Proposals) != 5 {
				t.Fatalf("expected 5 capped proposals, got %d", len(report.Proposals))
			}
			draftPath := filepath.Join(filepath.Dir(matches[0]), "issue-drafts.md")
			draft, err := os.ReadFile(draftPath)
			if err != nil {
				t.Fatalf("read draft: %v", err)
			}
			if !strings.Contains(string(draft), tc.wantText) {
				t.Fatalf("missing normalized labels in draft: %s", draft)
			}
		})
	}
}

func TestImproveLocalFromFileWritesArtifactsAndKeepsLocal(t *testing.T) {
	repo := t.TempDir()
	inputPath := filepath.Join(repo, "proposals.json")
	if err := os.WriteFile(inputPath, []byte(`{
  "version": 1,
  "repo": "local-widget",
  "proposals": [{
    "title": "Clarify setup failure recovery",
    "area": "UX",
    "summary": "Make setup errors point to the exact config file to edit.",
    "evidence": "README.md documents setup but errors omit the path.",
    "labels": ["enhancement", "docs"]
  }]
}`), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Improve(repo, []string{"--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Improve(): %v", err)
	}
	if !strings.Contains(output, "Keeping 1 proposal(s) local by policy") {
		t.Fatalf("unexpected output: %q", output)
	}
	matches, err := filepath.Glob(filepath.Join(repo, ".nana", "improvements", "improve-*", "issue-drafts.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one issue draft, matches=%#v err=%v", matches, err)
	}
	draft, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	text := string(draft)
	if !strings.Contains(text, "improvement proposals, not enhancement requests") {
		t.Fatalf("missing improvement wording: %s", text)
	}
	if !strings.Contains(text, "Labels: improvement, improvement-scout, docs") {
		t.Fatalf("labels not normalized in draft: %s", text)
	}
}

func TestEnhanceParserUsesEnhanceWording(t *testing.T) {
	repo := t.TempDir()
	err := Enhance(repo, []string{"--focus", "security"})
	if err == nil {
		t.Fatal("expected invalid focus error")
	}
	text := err.Error()
	if !strings.Contains(text, "invalid enhance focus") {
		t.Fatalf("expected enhance-specific error, got %q", text)
	}
	if strings.Contains(text, "invalid improve focus") || strings.Contains(text, "nana improve") {
		t.Fatalf("enhance error leaked improve wording: %q", text)
	}
}

func TestStartRunsOnlySupportedScoutRoles(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	inputPath := filepath.Join(repo, "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(2, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if !strings.Contains(output, "improvement-scout supported") {
		t.Fatalf("expected improvement scout to run, got %q", output)
	}
	if strings.Contains(output, "enhancement-scout supported") {
		t.Fatalf("did not expect enhancement scout to run, got %q", output)
	}
	improvementMatches, _ := filepath.Glob(filepath.Join(repo, ".nana", "improvements", "improve-*", "proposals.json"))
	if len(improvementMatches) != 1 {
		t.Fatalf("expected one improvement artifact, got %#v", improvementMatches)
	}
	enhancementMatches, _ := filepath.Glob(filepath.Join(repo, ".nana", "enhancements", "enhance-*", "proposals.json"))
	if len(enhancementMatches) != 0 {
		t.Fatalf("expected no enhancement artifacts, got %#v", enhancementMatches)
	}
}

func TestStartRunsBothSupportedScoutRoles(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "enhancement-policy.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write enhancement policy: %v", err)
	}
	inputPath := filepath.Join(repo, "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(2, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if !strings.Contains(output, "improvement-scout supported") || !strings.Contains(output, "enhancement-scout supported") {
		t.Fatalf("expected both scouts to run, got %q", output)
	}
	improvementMatches, _ := filepath.Glob(filepath.Join(repo, ".nana", "improvements", "improve-*", "proposals.json"))
	enhancementMatches, _ := filepath.Glob(filepath.Join(repo, ".nana", "enhancements", "enhance-*", "proposals.json"))
	if len(improvementMatches) != 1 || len(enhancementMatches) != 1 {
		t.Fatalf("expected both artifacts, improvements=%#v enhancements=%#v", improvementMatches, enhancementMatches)
	}
}

func TestStartNoSupportedPoliciesIsNoop(t *testing.T) {
	repo := t.TempDir()
	inputPath := filepath.Join(repo, "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(1, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if !strings.Contains(output, "No supported scout policies found") {
		t.Fatalf("expected no-op output, got %q", output)
	}
	if fileExists(filepath.Join(repo, ".nana", "improvements")) || fileExists(filepath.Join(repo, ".nana", "enhancements")) {
		t.Fatalf("start without policies should not create scout artifacts")
	}
}

func TestPublishImprovementIssuesUsesForkPolicyAndImprovementLabels(t *testing.T) {
	t.Setenv("GH_TOKEN", "token")
	var capturedPath string
	var capturedPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.URL.Path != "/repos/me/widget/issues" || !strings.Contains(r.URL.RawQuery, "labels=improvement-scout") {
				t.Fatalf("unexpected open issue cap request: %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		capturedPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"html_url":"https://github.example/me/widget/issues/9"}`))
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	results, err := publishImprovementIssues("acme/widget", []improvementProposal{{
		Title:   "Reduce startup work",
		Area:    "Perf",
		Summary: "Avoid repeated config reads during command startup.",
		Labels:  []string{"enhancement", "startup"},
	}}, improvementPolicy{
		IssueDestination: improvementDestinationFork,
		ForkRepo:         "me/widget",
		Labels:           []string{"improvement", "perf"},
	}, false)
	if err != nil {
		t.Fatalf("publishImprovementIssues(): %v", err)
	}
	if len(results) != 1 || results[0].URL == "" {
		t.Fatalf("unexpected results: %#v", results)
	}
	if capturedPath != "/repos/me/widget/issues" {
		t.Fatalf("unexpected issue target: %s", capturedPath)
	}
	labels, ok := capturedPayload["labels"].([]any)
	if !ok {
		t.Fatalf("missing labels payload: %#v", capturedPayload)
	}
	joined := []string{}
	for _, label := range labels {
		joined = append(joined, label.(string))
	}
	if strings.Join(joined, ",") != "improvement,improvement-scout,perf,startup" {
		t.Fatalf("unexpected labels: %#v", joined)
	}
	if strings.Contains(strings.ToLower(capturedPayload["body"].(string)), "enhancement request") && !strings.Contains(capturedPayload["body"].(string), "not an enhancement request") {
		t.Fatalf("body should frame the issue as an improvement: %s", capturedPayload["body"])
	}
}

func TestPublishScoutIssuesEnforcesOpenIssueCapBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		openIssues  int
		proposals   int
		dryRun      bool
		wantResults int
		wantPosts   int
	}{
		{name: "zero open creates up to five", openIssues: 0, proposals: 6, wantResults: 5, wantPosts: 5},
		{name: "four open creates one", openIssues: 4, proposals: 2, wantResults: 1, wantPosts: 1},
		{name: "four open dry run returns one without post", openIssues: 4, proposals: 3, dryRun: true, wantResults: 1, wantPosts: 0},
		{name: "five open creates none", openIssues: 5, proposals: 2, wantResults: 0, wantPosts: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", "token")
			postCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					if r.URL.Path != "/repos/acme/widget/issues" || !strings.Contains(r.URL.RawQuery, "labels=enhancement-scout") {
						t.Fatalf("unexpected open issue cap request: %s?%s", r.URL.Path, r.URL.RawQuery)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(openIssuesJSON(tc.openIssues)))
				case http.MethodPost:
					postCount++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"html_url":"https://github.example/acme/widget/issues/9"}`))
				default:
					t.Fatalf("unexpected method: %s", r.Method)
				}
			}))
			defer server.Close()
			t.Setenv("GITHUB_API_URL", server.URL)

			results, err := publishScoutIssues("acme/widget", testProposals(tc.proposals), improvementPolicy{
				IssueDestination: improvementDestinationTarget,
				Labels:           []string{"enhancement"},
			}, tc.dryRun, enhancementScoutRole)
			if err != nil {
				t.Fatalf("publishScoutIssues(): %v", err)
			}
			if len(results) != tc.wantResults || postCount != tc.wantPosts {
				t.Fatalf("expected results=%d posts=%d, got results=%#v postCount=%d", tc.wantResults, tc.wantPosts, results, postCount)
			}
			for _, result := range results {
				if result.DryRun != tc.dryRun {
					t.Fatalf("result dry-run state mismatch: %#v", results)
				}
			}
		})
	}
}

func scoutProposalJSON(count int, label string) string {
	proposals := testProposals(count)
	for index := range proposals {
		proposals[index].Labels = []string{label}
	}
	report := improvementReport{Version: 1, Repo: "local-widget", Proposals: proposals}
	content, _ := json.Marshal(report)
	return string(content)
}

func testProposals(count int) []improvementProposal {
	proposals := make([]improvementProposal, 0, count)
	for index := 1; index <= count; index++ {
		proposals = append(proposals, improvementProposal{
			Title:   fmt.Sprintf("Proposal %d", index),
			Area:    "UX",
			Summary: fmt.Sprintf("Improve flow %d.", index),
		})
	}
	return proposals
}

func openIssuesJSON(count int) string {
	issues := make([]map[string]int, 0, count)
	for index := 1; index <= count; index++ {
		issues = append(issues, map[string]int{"number": index})
	}
	content, _ := json.Marshal(issues)
	return string(content)
}
