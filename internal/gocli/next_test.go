package gocli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildAttentionReportPrefersBlockedGithubRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	manifestPath := filepath.Join(home, ".nana", "work", "repos", "acme", "widget", "runs", "gh-attn", "manifest.json")
	manifest := githubWorkManifest{
		RunID:            "gh-attn",
		RepoSlug:         "acme/widget",
		RepoOwner:        "acme",
		RepoName:         "widget",
		ManagedRepoRoot:  filepath.Dir(filepath.Dir(manifestPath)),
		TargetURL:        "https://github.com/acme/widget/pull/42",
		TargetKind:       "pull_request",
		TargetNumber:     42,
		UpdatedAt:        "2026-04-15T12:00:00Z",
		SandboxPath:      filepath.Join(home, "sandbox-github"),
		SandboxRepoPath:  filepath.Join(home, "sandbox-github", "repo"),
		PublicationState: "blocked",
		NeedsHuman:       true,
		NeedsHumanReason: "approval required",
		NextAction:       "wait_for_github_feedback",
		ReviewReviewer:   "@me",
	}
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write github manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index github manifest: %v", err)
	}

	if _, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "review_request",
		ExternalID: "queued-1",
		RepoSlug:   "acme/widget",
		Subject:    "Queued review draft",
		TargetURL:  "https://github.com/acme/widget/pull/77",
	}, "test"); err != nil {
		t.Fatalf("enqueue work item: %v", err)
	}

	report, err := buildAttentionReport(cwd)
	if err != nil {
		t.Fatalf("buildAttentionReport: %v", err)
	}
	if report.Next == nil {
		t.Fatalf("expected next attention item, got %+v", report)
	}
	if report.Next.RunID != "gh-attn" || report.Next.Kind != "work_run" {
		t.Fatalf("expected blocked GitHub work task to rank first, got %+v", report.Next)
	}
	if report.Next.RecommendedCommand != "nana work sync --run-id gh-attn --reviewer @me" {
		t.Fatalf("unexpected recommended command: %+v", report.Next)
	}
}

func TestNextJSONOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "verify-loop-state.json"), []byte(`{"active":true,"iteration":2,"max_iterations":5,"current_phase":"verify"}`), 0o644); err != nil {
		t.Fatalf("write verify-loop state: %v", err)
	}

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "review_request",
		ExternalID: "draft-ready-1",
		RepoSlug:   "acme/widget",
		Subject:    "Ready to submit review",
		TargetURL:  "https://github.com/acme/widget/pull/88",
	}, "test")
	if err != nil {
		t.Fatalf("enqueue work item: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	item, err = store.readWorkItem(item.ID)
	if err != nil {
		t.Fatalf("readWorkItem: %v", err)
	}
	item.Status = workItemStatusDraftReady
	item.LatestDraft = &workItemDraft{Kind: "review", Summary: "Looks good."}
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Next(cwd, []string{"--json"})
	})
	if err != nil {
		t.Fatalf("Next(--json): %v", err)
	}

	var report attentionReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("parse next json: %v\noutput=%s", err, output)
	}
	if report.ActiveModeID != "verify-loop" || report.ActiveMode != "verify-loop" || report.ActivePhase != "verify" {
		t.Fatalf("unexpected active mode payload: %+v", report)
	}
	if report.Next == nil || report.Next.ItemID == "" || report.Next.RecommendedCommand != "nana work items show "+report.Next.ItemID {
		t.Fatalf("unexpected next item payload: %+v", report.Next)
	}
}

func TestStartUIAttentionEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	manifestPath := filepath.Join(home, ".nana", "work", "repos", "acme", "widget", "runs", "gh-api-attn", "manifest.json")
	manifest := githubWorkManifest{
		RunID:            "gh-api-attn",
		RepoSlug:         "acme/widget",
		RepoOwner:        "acme",
		RepoName:         "widget",
		ManagedRepoRoot:  filepath.Dir(filepath.Dir(manifestPath)),
		TargetURL:        "https://github.com/acme/widget/pull/99",
		TargetKind:       "pull_request",
		TargetNumber:     99,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
		SandboxPath:      filepath.Join(home, "sandbox-github"),
		SandboxRepoPath:  filepath.Join(home, "sandbox-github", "repo"),
		PublicationState: "blocked",
		NeedsHuman:       true,
		NeedsHumanReason: "approval required",
		NextAction:       "wait_for_github_feedback",
	}
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write github manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("index github manifest: %v", err)
	}

	server := httptest.NewServer((&startUIAPI{cwd: cwd, allowedWebOrigin: "http://127.0.0.1:17654"}).routes())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/attention")
	if err != nil {
		t.Fatalf("GET attention: %v", err)
	}
	defer response.Body.Close()

	var report attentionReport
	if err := json.NewDecoder(response.Body).Decode(&report); err != nil {
		t.Fatalf("decode attention: %v", err)
	}
	if report.Next == nil || report.Next.RunID != "gh-api-attn" {
		t.Fatalf("unexpected attention payload: %+v", report)
	}
	if !strings.Contains(report.Next.RecommendedCommand, "nana work sync --run-id gh-api-attn") {
		t.Fatalf("unexpected attention command: %+v", report.Next)
	}
}
