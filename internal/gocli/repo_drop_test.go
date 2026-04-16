package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoDropRemovesPersistedGithubRepoState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	paths := githubManagedPaths(repoSlug)
	sourcePath := createLocalWorkRepoAt(t, paths.SourcePath)

	if err := writeGithubJSON(paths.RepoSettingsPath, githubRepoSettings{
		Version:        6,
		RepoMode:       "fork",
		IssuePickMode:  "label",
		PRForwardMode:  "approve",
		ForkIssuesMode: "labeled",
		ImplementMode:  "labeled",
		PublishTarget:  "fork",
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Issues:     map[string]startWorkIssueState{},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	sandboxPath := filepath.Join(localWorkSandboxesDir(), "sandbox-drop-test")
	if err := os.MkdirAll(sandboxPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox: %v", err)
	}
	manifest := localWorkManifest{
		RunID:           "lw-drop-test",
		RepoRoot:        sourcePath,
		RepoName:        "widget",
		RepoID:          localWorkRepoID(sourcePath),
		CreatedAt:       time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		Status:          "completed",
		CurrentPhase:    "completed",
		SandboxPath:     sandboxPath,
		SandboxRepoPath: filepath.Join(sandboxPath, "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	item, _, err := enqueueWorkItem(workItemInput{
		Source:        "github",
		SourceKind:    "comment",
		ExternalID:    "drop-test",
		RepoSlug:      repoSlug,
		TargetURL:     "https://github.com/acme/widget/issues/1",
		Subject:       "Follow up",
		Body:          "Handle it",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339),
		SubmitProfile: &workItemSubmitProfile{Type: "github"},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	if err := writeGithubJSON(githubWorkLatestRunPath(), githubLatestRunPointer{RepoRoot: paths.RepoRoot, RunID: manifest.RunID}); err != nil {
		t.Fatalf("write latest run: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Repo(".", []string{"drop", repoSlug})
	})
	if err != nil {
		t.Fatalf("Repo(drop): %v", err)
	}
	if !strings.Contains(output, "[repo] Forgot "+repoSlug) {
		t.Fatalf("unexpected output: %q", output)
	}

	for _, path := range []string{
		paths.RepoRoot,
		filepath.Dir(paths.RepoRoot),
		filepath.Dir(startWorkStatePath(repoSlug)),
		localWorkRepoDir(sourcePath),
		sandboxPath,
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", path, err)
		}
	}
	if _, err := os.Stat(githubWorkLatestRunPath()); !os.IsNotExist(err) {
		t.Fatalf("expected latest run pointer to be removed, err=%v", err)
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	var runCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM work_run_index WHERE repo_root = ? OR repo_key = ?`, sourcePath, repoSlug).Scan(&runCount); err != nil {
		t.Fatalf("count work_run_index: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("expected work_run_index rows removed, got %d", runCount)
	}
	var itemCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE repo_slug = ?`, repoSlug).Scan(&itemCount); err != nil {
		t.Fatalf("count work_items: %v", err)
	}
	if itemCount != 0 {
		t.Fatalf("expected work_items rows removed, got %d", itemCount)
	}
	if _, err := store.readWorkItem(item.ID); err == nil {
		t.Fatalf("expected dropped repo work item to be removed")
	}
}

func TestRepoDropIsIdempotentWhenRepoIsAlreadyGone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output, err := captureStdout(t, func() error {
		return Repo(".", []string{"drop", "acme/widget"})
	})
	if err != nil {
		t.Fatalf("Repo(drop) on empty state: %v", err)
	}
	if !strings.Contains(output, "already forgotten") {
		t.Fatalf("expected idempotent message, got %q", output)
	}
}

func TestRepoDropPrunesEmptyOwnerDirWhenRepoRootIsAlreadyGone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ownerDir := filepath.Dir(githubManagedPaths("acme/widget").RepoRoot)
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatalf("mkdir owner dir: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Repo(".", []string{"drop", "acme/widget"})
	})
	if err != nil {
		t.Fatalf("Repo(drop) with empty owner dir: %v", err)
	}
	if !strings.Contains(output, "[repo] Forgot acme/widget") {
		t.Fatalf("expected cleanup message, got %q", output)
	}
	if _, err := os.Stat(ownerDir); !os.IsNotExist(err) {
		t.Fatalf("expected empty owner dir to be pruned, stat err=%v", err)
	}
}

func TestRepoDropKeepsOwnerDirWhenSiblingRepoExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	paths := githubManagedPaths("acme/widget")
	if err := os.MkdirAll(paths.RepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo root: %v", err)
	}
	siblingRoot := githubManagedPaths("acme/other").RepoRoot
	if err := os.MkdirAll(siblingRoot, 0o755); err != nil {
		t.Fatalf("mkdir sibling root: %v", err)
	}

	if _, err := captureStdout(t, func() error {
		return Repo(".", []string{"drop", "acme/widget"})
	}); err != nil {
		t.Fatalf("Repo(drop) with sibling repo: %v", err)
	}

	if _, err := os.Stat(filepath.Dir(paths.RepoRoot)); err != nil {
		t.Fatalf("expected owner dir to remain for sibling repo, stat err=%v", err)
	}
	if _, err := os.Stat(siblingRoot); err != nil {
		t.Fatalf("expected sibling repo root to remain, stat err=%v", err)
	}
}
