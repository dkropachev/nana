package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoScoutEnableWritesDefaultLocalPolicies(t *testing.T) {
	repo := t.TempDir()
	output, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable"})
	})
	if err != nil {
		t.Fatalf("Repo(scout enable): %v\n%s", err, output)
	}
	for _, role := range []string{improvementScoutRole, enhancementScoutRole} {
		path := repoScoutPolicyPath(repo, role, false)
		var policy improvementPolicy
		if err := readGithubJSON(path, &policy); err != nil {
			t.Fatalf("read policy %s: %v", path, err)
		}
		if policy.Version != 1 || policy.Mode != "auto" || policy.IssueDestination != improvementDestinationLocal {
			t.Fatalf("unexpected %s policy: %#v", role, policy)
		}
	}
	if !strings.Contains(output, "Wrote scout policy") || !strings.Contains(output, "`nana start` will run") {
		t.Fatalf("unexpected output: %q", output)
	}
	content, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, expected := range []string{".codex", ".codex/", ".codex-investigate", ".codex-investigate/"} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("expected %q in .gitignore:\n%s", expected, content)
		}
	}
}

func TestRepoScoutHelpExitsCleanly(t *testing.T) {
	output, err := captureStdout(t, func() error {
		return Repo(t.TempDir(), []string{"scout", "enable", "--help"})
	})
	if err != nil {
		t.Fatalf("Repo(scout enable --help): %v", err)
	}
	if !strings.Contains(output, "nana repo scout enable") {
		t.Fatalf("unexpected help output: %q", output)
	}
}

func TestRepoScoutEnableWritesGithubEnhancementForkPolicy(t *testing.T) {
	repo := t.TempDir()
	output, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable", "--github", "--role", "enhancement", "--mode", "manual", "--issue-destination", "fork", "--fork-repo", "me/widget", "--labels", "Roadmap,UX", "--max-issues", "2"})
	})
	if err != nil {
		t.Fatalf("Repo(scout enable): %v\n%s", err, output)
	}
	path := filepath.Join(repo, ".github", "nana-enhancement-policy.json")
	var policy improvementPolicy
	if err := readGithubJSON(path, &policy); err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if policy.Version != 1 || policy.Mode != "manual" || policy.IssueDestination != improvementDestinationFork || policy.ForkRepo != "me/widget" || policy.MaxIssues != 2 {
		t.Fatalf("unexpected policy: %#v", policy)
	}
	if got := strings.Join(policy.Labels, ","); got != "enhancement,enhancement-scout,roadmap,ux" {
		t.Fatalf("unexpected labels: %q", got)
	}
	if fileExists(filepath.Join(repo, ".github", "nana-improvement-policy.json")) {
		t.Fatalf("did not expect improvement policy")
	}
}

func TestRepoScoutEnablePreservesExistingUnspecifiedFields(t *testing.T) {
	repo := t.TempDir()
	path := repoScoutPolicyPath(repo, improvementScoutRole, false)
	if err := writeGithubJSON(path, improvementPolicy{Version: 1, Mode: "manual", IssueDestination: improvementDestinationFork, ForkRepo: "me/widget", Labels: []string{"custom"}, MaxIssues: 3}); err != nil {
		t.Fatalf("write existing policy: %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable", "--role", "improvement", "--labels", "docs"})
	}); err != nil {
		t.Fatalf("Repo(scout enable): %v", err)
	}
	var policy improvementPolicy
	if err := readGithubJSON(path, &policy); err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if policy.Mode != "manual" || policy.IssueDestination != improvementDestinationFork || policy.ForkRepo != "me/widget" || policy.MaxIssues != 3 {
		t.Fatalf("unexpected preserved fields: %#v", policy)
	}
	if got := strings.Join(policy.Labels, ","); got != "improvement,improvement-scout,docs" {
		t.Fatalf("unexpected labels: %q", got)
	}
}

func TestRepoScoutEnableRequiresForkRepoForForkDestination(t *testing.T) {
	err := Repo(t.TempDir(), []string{"scout", "enable", "--issue-destination", "fork"})
	if err == nil {
		t.Fatal("expected fork repo validation error")
	}
	if !strings.Contains(err.Error(), "--fork-repo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRepoScoutEnableAllowsMaxIssuesUpToFifty(t *testing.T) {
	repo := t.TempDir()
	if _, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable", "--role", "improvement", "--max-issues", "50"})
	}); err != nil {
		t.Fatalf("Repo(scout enable): %v", err)
	}
	var policy improvementPolicy
	if err := readGithubJSON(repoScoutPolicyPath(repo, improvementScoutRole, false), &policy); err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if policy.MaxIssues != 50 {
		t.Fatalf("expected max issues 50, got %#v", policy)
	}
	err := Repo(repo, []string{"scout", "enable", "--role", "improvement", "--max-issues", "51"})
	if err == nil {
		t.Fatal("expected max issues validation error")
	}
	if !strings.Contains(err.Error(), "1 to 50") {
		t.Fatalf("unexpected error: %v", err)
	}
}
