package gocli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunScoutStartSkipsDailyScheduleWhenNotDue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	if err := writeGithubJSON(repoScoutPolicyPath(repo, improvementScoutRole, false), scoutPolicy{
		Version:  1,
		Schedule: scoutScheduleDaily,
	}); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	if err := recordSuccessfulScoutRun(repo, improvementScoutRole, time.Now().UTC()); err != nil {
		t.Fatalf("record successful run: %v", err)
	}
	inputPath := filepath.Join(t.TempDir(), "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(1, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--once", "--from-file", inputPath})
	})
	if err != nil {
		t.Fatalf("Start(): %v\n%s", err, output)
	}
	if !strings.Contains(output, "improvement-scout supported; skipped by schedule") {
		t.Fatalf("expected schedule skip output, got %q", output)
	}
	if fileExists(filepath.Join(repo, ".nana", "improvements")) {
		t.Fatalf("did not expect scout artifacts when schedule is not due")
	}
}

func TestStartRepoDueScoutRolesSkipsDailyScoutWhenNotDue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)
	if err := writeGithubJSON(repoScoutPolicyPath(sourcePath, improvementScoutRole, false), scoutPolicy{
		Version:  1,
		Schedule: scoutScheduleDaily,
	}); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	if err := recordSuccessfulScoutRun(sourcePath, improvementScoutRole, time.Now().UTC()); err != nil {
		t.Fatalf("record successful run: %v", err)
	}

	roles, err := startRepoDueScoutRoles(repoSlug, time.Now().UTC())
	if err != nil {
		t.Fatalf("startRepoDueScoutRoles: %v", err)
	}
	if len(roles) != 0 {
		t.Fatalf("expected no due scout roles, got %#v", roles)
	}
}

func TestScoutScheduleDecisionWhenResolvedUsesOpenIssues(t *testing.T) {
	repoPath := t.TempDir()
	if err := recordSuccessfulScoutRun(repoPath, improvementScoutRole, time.Now().Add(-48*time.Hour).UTC()); err != nil {
		t.Fatalf("record successful run: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widget/issues":
			_, _ = w.Write([]byte(openIssuesJSON(1)))
		default:
			t.Fatalf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)
	t.Setenv("GH_TOKEN", "token")

	policy := scoutPolicy{
		Version:          1,
		Schedule:         scoutScheduleWhenResolved,
		IssueDestination: improvementDestinationTarget,
	}
	decision, err := scoutScheduleDecisionForRole(repoPath, "acme/widget", improvementScoutRole, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("scoutScheduleDecisionForRole: %v", err)
	}
	if decision.Due || !strings.Contains(decision.Reason, "waiting for 1 previously reported Improvement item") {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestScoutScheduleDecisionWhenResolvedUsesLocalOutstandingItems(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := createLocalWorkRepoAt(t, filepath.Join(t.TempDir(), "repo"))
	proposal := improvementProposal{
		Title:   "Clarify help text",
		Area:    "UX",
		Summary: "Make help output clearer.",
	}
	report := scoutReport{
		Version:   1,
		Repo:      "widget",
		Proposals: []scoutFinding{proposal},
	}
	artifactDir := filepath.Join(repo, ".nana", scoutArtifactRoot(improvementScoutRole), "improve-1")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(artifactDir, "proposals.json"), report); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if err := recordSuccessfulScoutRun(repo, improvementScoutRole, time.Now().Add(-48*time.Hour).UTC()); err != nil {
		t.Fatalf("record successful run: %v", err)
	}

	policy := scoutPolicy{Version: 1, Schedule: scoutScheduleWhenResolved}
	decision, err := scoutScheduleDecisionForRole(repo, "", improvementScoutRole, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("scoutScheduleDecisionForRole: %v", err)
	}
	if decision.Due {
		t.Fatalf("expected outstanding local scout item to block rerun, got %+v", decision)
	}

	itemID := localScoutProposalID(improvementScoutRole, proposal)
	state, statePath, err := readLocalScoutPickupState(repo)
	if err != nil {
		t.Fatalf("read pickup state: %v", err)
	}
	state.Items[itemID] = localScoutPickupItem{
		Status:     "completed",
		Title:      proposal.Title,
		Artifact:   filepath.ToSlash(filepath.Join(".nana", scoutArtifactRoot(improvementScoutRole), "improve-1")),
		UpdatedAt:  ISOTimeNow(),
		ProposalID: itemID,
	}
	if err := writeLocalScoutPickupState(statePath, state); err != nil {
		t.Fatalf("write pickup state: %v", err)
	}

	decision, err = scoutScheduleDecisionForRole(repo, "", improvementScoutRole, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("scoutScheduleDecisionForRole: %v", err)
	}
	if !decision.Due {
		t.Fatalf("expected rerun once local scout items are resolved, got %+v", decision)
	}
}
