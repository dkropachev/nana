package gocli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRepoScoutEnableWritesDefaultLocalPolicies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	output, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable"})
	})
	if err != nil {
		t.Fatalf("Repo(scout enable): %v\n%s", err, output)
	}
	for _, role := range []string{improvementScoutRole, enhancementScoutRole} {
		path := repoScoutPolicyPath(repo, role, false)
		if strings.HasPrefix(path, repo+string(filepath.Separator)) {
			t.Fatalf("expected managed scout policy path outside source repo, got %s", path)
		}
		var policy improvementPolicy
		if err := readGithubJSON(path, &policy); err != nil {
			t.Fatalf("read policy %s: %v", path, err)
		}
		if policy.Version != 1 || policy.Mode != "auto" || policy.Schedule != scoutScheduleWhenResolved || policy.IssueDestination != improvementDestinationLocal {
			t.Fatalf("unexpected %s policy: %#v", role, policy)
		}
	}
	if !strings.Contains(output, "Wrote scout policy") || !strings.Contains(output, "`nana start` will run") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestScoutRegistryMetadataComplete(t *testing.T) {
	if len(scoutRoleRegistry) != len(supportedScoutRoleOrder) {
		t.Fatalf("registry/order mismatch: registry=%d order=%d", len(scoutRoleRegistry), len(supportedScoutRoleOrder))
	}
	for _, role := range supportedScoutRoleOrder {
		spec := scoutRoleSpecFor(role)
		for field, value := range map[string]string{
			"role":          spec.Role,
			"config_key":    spec.ConfigKey,
			"display":       spec.DisplayLabel,
			"artifact_root": spec.ArtifactRoot,
			"output_prefix": spec.OutputPrefix,
			"base_label":    spec.BaseLabel,
			"heading":       spec.IssueHeading,
			"default_area":  spec.DefaultArea,
			"plural":        spec.ResultPlural,
			"count_noun":    spec.ItemCountNoun,
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("missing %s for role %s: %+v", field, role, spec)
			}
		}
		if spec.Role != role {
			t.Fatalf("registry lookup mismatch for %s: %+v", role, spec)
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	output, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable", "--github", "--role", "enhancement", "--mode", "manual", "--issue-destination", "fork", "--fork-repo", "me/widget", "--labels", "Roadmap,UX"})
	})
	if err != nil {
		t.Fatalf("Repo(scout enable): %v\n%s", err, output)
	}
	path := repoScoutPolicyPath(repo, enhancementScoutRole, false)
	var policy improvementPolicy
	if err := readGithubJSON(path, &policy); err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if policy.Version != 1 || policy.Mode != "manual" || policy.IssueDestination != improvementDestinationFork || policy.ForkRepo != "me/widget" {
		t.Fatalf("unexpected policy: %#v", policy)
	}
	if got := strings.Join(policy.Labels, ","); got != "enhancement,enhancement-scout,roadmap,ux" {
		t.Fatalf("unexpected labels: %q", got)
	}
	if fileExists(repoScoutPolicyPath(repo, improvementScoutRole, false)) {
		t.Fatalf("did not expect improvement policy")
	}
}

func TestRepoScoutEnablePreservesExistingUnspecifiedFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	path := repoScoutPolicyPath(repo, improvementScoutRole, false)
	if err := writeGithubJSON(path, improvementPolicy{Version: 1, Mode: "manual", IssueDestination: improvementDestinationFork, ForkRepo: "me/widget", Labels: []string{"custom"}}); err != nil {
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
	if policy.Mode != "manual" || policy.IssueDestination != improvementDestinationFork || policy.ForkRepo != "me/widget" {
		t.Fatalf("unexpected preserved fields: %#v", policy)
	}
	if got := strings.Join(policy.Labels, ","); got != "improvement,improvement-scout,docs" {
		t.Fatalf("unexpected labels: %q", got)
	}
}

func TestRepoScoutEnableRequiresForkRepoForForkDestination(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	err := Repo(t.TempDir(), []string{"scout", "enable", "--issue-destination", "fork"})
	if err == nil {
		t.Fatal("expected fork repo validation error")
	}
	if !strings.Contains(err.Error(), "--fork-repo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRepoScoutEnableDropsLegacyMaxIssuesField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	path := repoScoutPolicyPath(repo, improvementScoutRole, false)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir scout dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"mode":"manual","issue_destination":"local","max_issues":9}`), 0o644); err != nil {
		t.Fatalf("write legacy policy: %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable", "--role", "improvement", "--labels", "docs"})
	}); err != nil {
		t.Fatalf("Repo(scout enable): %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if strings.Contains(string(content), "max_issues") {
		t.Fatalf("expected max_issues to be dropped, got %s", content)
	}
}

func TestRepoScoutEnableWritesUIScoutSessionLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	if _, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable", "--role", "ui", "--mode", "manual", "--labels", "qa", "--session-limit", "6"})
	}); err != nil {
		t.Fatalf("Repo(scout enable): %v", err)
	}
	var policy improvementPolicy
	if err := readGithubJSON(repoScoutPolicyPath(repo, uiScoutRole, false), &policy); err != nil {
		t.Fatalf("read ui policy: %v", err)
	}
	if policy.Mode != "manual" || policy.SessionLimit != 6 {
		t.Fatalf("unexpected ui policy: %#v", policy)
	}
	if got := strings.Join(policy.Labels, ","); got != "ui,ui-scout,qa" {
		t.Fatalf("unexpected labels: %q", got)
	}
}

func TestRepoScoutEnableWritesSchedule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	if _, err := captureStdout(t, func() error {
		return Repo(repo, []string{"scout", "enable", "--role", "improvement", "--schedule", "weekly"})
	}); err != nil {
		t.Fatalf("Repo(scout enable): %v", err)
	}
	var policy improvementPolicy
	if err := readGithubJSON(repoScoutPolicyPath(repo, improvementScoutRole, false), &policy); err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if policy.Schedule != scoutScheduleWeekly {
		t.Fatalf("expected weekly schedule, got %#v", policy)
	}
}

func TestStartUIOverviewDependenciesIncludeAllScoutPolicies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	if err := writeGithubJSON(githubRepoSettingsPath(repoSlug), githubRepoSettings{Version: 6, RepoMode: "fork", IssuePickMode: "auto", PRForwardMode: "approve"}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	for _, role := range supportedScoutRoleOrder {
		if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, role, false), scoutPolicy{Version: 1}); err != nil {
			t.Fatalf("write policy %s: %v", role, err)
		}
	}
	deps := listStartUIOverviewDependencies(t.TempDir())
	for _, role := range supportedScoutRoleOrder {
		policyPath := repoScoutPolicyPath(sourcePath, role, false)
		if !slices.Contains(deps, filepath.Clean(policyPath)) {
			t.Fatalf("expected dependency list to include %s", policyPath)
		}
	}
}
