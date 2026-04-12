package gocli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	if policy.MaxIssues != defaultScoutIssueCap {
		t.Fatalf("expected default max issue cap of %d, got %#v", defaultScoutIssueCap, policy)
	}
}

func TestScoutLocalFromFileUsesDefaultCapForBothRoles(t *testing.T) {
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

func TestScoutLocalFromFileAllowsPolicyCapUpToFifty(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1,"max_issues":10}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	inputPath := filepath.Join(repo, "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(12, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	output, err := captureStdout(t, func() error {
		return Improve(repo, []string{"--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Improve: %v", err)
	}
	if !strings.Contains(output, "Keeping 10 proposal(s) local by policy") {
		t.Fatalf("expected max_issues 10 output, got %q", output)
	}
	matches, err := filepath.Glob(filepath.Join(repo, ".nana", "improvements", "improve-*", "proposals.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one proposals artifact, matches=%#v err=%v", matches, err)
	}
	var report improvementReport
	if err := readGithubJSON(matches[0], &report); err != nil {
		t.Fatalf("read report: %v", err)
	}
	if len(report.Proposals) != 10 {
		t.Fatalf("expected 10 capped proposals, got %d", len(report.Proposals))
	}
}

func TestImproveRunsScoutPromptAfterOptionSeparator(t *testing.T) {
	repo := t.TempDir()
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	argsPath := filepath.Join(t.TempDir(), "codex-args.txt")
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$@" > "$FAKE_CODEX_ARGS_PATH"`,
		`printf '{"version":1,"proposals":[]}\n'`,
	}, "\n"))
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_ARGS_PATH", argsPath)

	output, err := captureStdout(t, func() error {
		return Improve(repo, nil)
	})
	if err != nil {
		t.Fatalf("Improve: %v\n%s", err, output)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake codex args: %v", err)
	}
	if !strings.Contains(string(args), "\n--\n---\n") {
		t.Fatalf("expected option separator before frontmatter prompt, got:\n%s", args)
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

func TestStartAutoModeCommitsBothScoutsToDefaultBranch(t *testing.T) {
	repo := t.TempDir()
	runScoutTestGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	autoPolicy := []byte(`{"version":1,"mode":"auto"}`)
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), autoPolicy, 0o644); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "enhancement-policy.json"), autoPolicy, 0o644); err != nil {
		t.Fatalf("write enhancement policy: %v", err)
	}
	runScoutTestGit(t, repo, "add", ".")
	runScoutTestGit(t, repo, "commit", "-m", "init")
	runScoutTestGit(t, repo, "checkout", "-b", "feature")

	inputPath := filepath.Join(t.TempDir(), "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(2, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if !strings.Contains(output, "Committed scout artifacts to default branch") {
		t.Fatalf("missing auto commit output: %q", output)
	}
	if branch := strings.TrimSpace(scoutTestGitOutput(t, repo, "rev-parse", "--abbrev-ref", "HEAD")); branch != "main" {
		t.Fatalf("expected checkout on default branch, got %q", branch)
	}
	if subject := strings.TrimSpace(scoutTestGitOutput(t, repo, "log", "-1", "--pretty=%s")); subject != "Record scout startup artifacts" {
		t.Fatalf("expected scout artifact commit, got %q", subject)
	}
	tree := scoutTestGitOutput(t, repo, "ls-tree", "-r", "--name-only", "main")
	for _, expected := range []string{".nana/improvements/", ".nana/enhancements/"} {
		if !strings.Contains(tree, expected) {
			t.Fatalf("expected %s in default branch tree:\n%s", expected, tree)
		}
	}
	if status := strings.TrimSpace(scoutTestGitOutput(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("expected clean repo after auto commit, got %q", status)
	}
}

func TestStartAutoModeCommitsSingleScoutToDefaultBranch(t *testing.T) {
	repo := t.TempDir()
	runScoutTestGit(t, repo, "init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1,"mode":"auto"}`), 0o644); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	runScoutTestGit(t, repo, "add", ".")
	runScoutTestGit(t, repo, "commit", "-m", "init")
	runScoutTestGit(t, repo, "checkout", "-b", "feature")
	inputPath := filepath.Join(t.TempDir(), "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(1, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return Start(repo, []string{"--from-file", inputPath})
	}); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	tree := scoutTestGitOutput(t, repo, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, ".nana/improvements/") {
		t.Fatalf("expected improvement artifacts on default branch:\n%s", tree)
	}
	if strings.Contains(tree, ".nana/enhancements/") {
		t.Fatalf("did not expect enhancement artifacts:\n%s", tree)
	}
}

func TestStartAutoModeUsesOriginHeadDefaultBranch(t *testing.T) {
	repo := t.TempDir()
	runScoutTestGit(t, repo, "init", "-b", "develop")
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1,"mode":"auto"}`), 0o644); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	runScoutTestGit(t, repo, "add", ".")
	runScoutTestGit(t, repo, "commit", "-m", "init")
	runScoutTestGit(t, repo, "update-ref", "refs/remotes/origin/develop", "HEAD")
	runScoutTestGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	runScoutTestGit(t, repo, "checkout", "-b", "feature")
	inputPath := filepath.Join(t.TempDir(), "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(1, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return Start(repo, []string{"--from-file", inputPath})
	}); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if branch := strings.TrimSpace(scoutTestGitOutput(t, repo, "rev-parse", "--abbrev-ref", "HEAD")); branch != "develop" {
		t.Fatalf("expected checkout on origin/HEAD default branch, got %q", branch)
	}
	tree := scoutTestGitOutput(t, repo, "ls-tree", "-r", "--name-only", "develop")
	if !strings.Contains(tree, ".nana/improvements/") {
		t.Fatalf("expected improvement artifacts on develop branch:\n%s", tree)
	}
}

func TestStartAutoModeIgnoresCodexRuntimeDirectory(t *testing.T) {
	repo := t.TempDir()
	runScoutTestGit(t, repo, "init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1,"mode":"auto"}`), 0o644); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	runScoutTestGit(t, repo, "add", ".")
	runScoutTestGit(t, repo, "commit", "-m", "init")
	if err := os.MkdirAll(filepath.Join(repo, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".codex", "config.toml"), []byte("model = \"gpt-5.4\"\n"), 0o644); err != nil {
		t.Fatalf("write .codex config: %v", err)
	}
	inputPath := filepath.Join(t.TempDir(), "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(1, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return Start(repo, []string{"--from-file", inputPath})
	}); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if status := strings.TrimSpace(scoutTestGitOutput(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("expected clean status with .codex ignored, got %q", status)
	}
	gitignore := scoutTestGitOutput(t, repo, "show", "HEAD:.gitignore")
	for _, expected := range []string{".codex", ".codex/"} {
		if !strings.Contains(gitignore, expected) {
			t.Fatalf("expected %q in committed .gitignore:\n%s", expected, gitignore)
		}
	}
}

func TestStartGithubRepoDestinationPublishesBothScoutsToTargetRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := createScoutPolicyGitRepo(t, "repo", "")
	inputPath := filepath.Join(t.TempDir(), "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(2, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	posts := []string{}
	server := scoutGithubServer(t, repo, &posts)
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)
	t.Setenv("GH_TOKEN", "token")

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"acme/widget", "--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if !strings.Contains(output, "improvement-scout supported") || !strings.Contains(output, "enhancement-scout supported") {
		t.Fatalf("expected both scouts to run, got %q", output)
	}
	if got := strings.Join(posts, ","); got != "/repos/acme/widget/issues,/repos/acme/widget/issues,/repos/acme/widget/issues,/repos/acme/widget/issues" {
		t.Fatalf("expected target repo issue posts, got %q", got)
	}
}

func TestStartGithubForkDestinationPublishesBothScoutsToForkRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := createScoutPolicyGitRepo(t, "fork", "me/widget")
	inputPath := filepath.Join(t.TempDir(), "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(2, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	posts := []string{}
	server := scoutGithubServer(t, repo, &posts)
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)
	t.Setenv("GH_TOKEN", "token")

	output, err := captureStdout(t, func() error {
		return Start(".", []string{"acme/widget", "--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if !strings.Contains(output, "improvement-scout supported") || !strings.Contains(output, "enhancement-scout supported") {
		t.Fatalf("expected both scouts to run, got %q", output)
	}
	if got := strings.Join(posts, ","); got != "/repos/me/widget/issues,/repos/me/widget/issues,/repos/me/widget/issues,/repos/me/widget/issues" {
		t.Fatalf("expected fork repo issue posts, got %q", got)
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

func createScoutPolicyGitRepo(t *testing.T, destination string, forkRepo string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	forkLine := ""
	if forkRepo != "" {
		forkLine = fmt.Sprintf(`,"fork_repo":%q`, forkRepo)
	}
	policy := []byte(fmt.Sprintf(`{"version":1,"issue_destination":%q%s}`, destination, forkLine))
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), policy, 0o644); err != nil {
		t.Fatalf("write improvement policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "enhancement-policy.json"), policy, 0o644); err != nil {
		t.Fatalf("write enhancement policy: %v", err)
	}
	runScoutTestGit(t, repo, "init", "-b", "main")
	runScoutTestGit(t, repo, "add", ".")
	runScoutTestGit(t, repo, "commit", "-m", "init")
	return repo
}

func scoutGithubServer(t *testing.T, repo string, posts *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"widget","full_name":"acme/widget","clone_url":%q,"default_branch":"main","html_url":"https://github.com/acme/widget"}`, repo)))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues"):
			if !strings.Contains(r.URL.RawQuery, "labels=improvement-scout") && !strings.Contains(r.URL.RawQuery, "labels=enhancement-scout") {
				t.Fatalf("unexpected cap query: %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues"):
			*posts = append(*posts, r.URL.Path)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"html_url":"https://github.example%s/1"}`, r.URL.Path)))
		default:
			t.Fatalf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
	}))
}

func runScoutTestGit(t *testing.T, repo string, args ...string) {
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

func scoutTestGitOutput(t *testing.T, repo string, args ...string) string {
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
	return string(output)
}
