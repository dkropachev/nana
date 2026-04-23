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

func TestResumeGithubWorkUsesLeaderSessionCheckpoint(t *testing.T) {
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	commandLogPath := filepath.Join(home, "codex-commands.log")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`printf '%s\n' "$*" >> "${FAKE_CODEX_LOG_PATH}"`,
		`if printf '%s' "$*" | grep -q "exec resume session-gh"; then`,
		`  printf 'github resumed\n'`,
		`  exit 0`,
		`fi`,
		`printf 'unexpected codex args: %s\n' "$*" >&2`,
		`exit 1`,
		"",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG_PATH", commandLogPath)

	repoRoot := githubWorkRepoRoot("acme/widget")
	runID := "gh-session-reuse"
	runDir := filepath.Join(repoRoot, "runs", runID)
	sandboxPath := filepath.Join(runDir, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	createLocalWorkRepoAt(t, sandboxRepoPath)
	manifest := githubWorkManifest{
		Version:         1,
		RunID:           runID,
		RepoSlug:        "acme/widget",
		RepoOwner:       "acme",
		RepoName:        "widget",
		TargetURL:       "https://github.com/acme/widget/issues/1",
		TargetKind:      "issue",
		TargetNumber:    1,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		UpdatedAt:       ISOTimeNow(),
		APIBaseURL:      "https://api.github.com",
	}
	if err := writeGithubJSON(filepath.Join(runDir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeCodexStepCheckpoint(filepath.Join(runDir, "leader-checkpoint.json"), codexStepCheckpoint{
		Version:        1,
		StepKey:        "github-leader",
		Status:         "failed",
		SessionID:      "session-gh",
		ResumeStrategy: string(codexResumeConversation),
		ResumeEligible: true,
	}); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return resumeGithubWork(localWorkResumeOptions{RunSelection: localWorkRunSelection{RunID: runID}})
	})
	if err != nil {
		t.Fatalf("resumeGithubWork: %v\n%s", err, output)
	}
	commandLog, err := os.ReadFile(commandLogPath)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	if !strings.Contains(string(commandLog), "exec resume session-gh") {
		t.Fatalf("expected exec resume in log, got %q", string(commandLog))
	}
	snapshot, err := buildGithubWorkStatusSnapshot(manifest, runDir)
	if err != nil {
		t.Fatalf("buildGithubWorkStatusSnapshot: %v", err)
	}
	if snapshot.LeaderSessionID != "session-gh" || snapshot.LeaderResumeEligible {
		t.Fatalf("unexpected leader checkpoint snapshot: %#v", snapshot)
	}
}

func TestResumeGithubWorkRejectsCompletionOnlyResumeWithoutBaseline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := githubWorkRepoRoot("acme/widget")
	runID := "gh-completion-no-baseline"
	runDir := filepath.Join(repoRoot, "runs", runID)
	sandboxPath := filepath.Join(runDir, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	createLocalWorkRepoAt(t, sandboxRepoPath)
	manifest := githubWorkManifest{
		Version:         1,
		RunID:           runID,
		RepoSlug:        "acme/widget",
		RepoOwner:       "acme",
		RepoName:        "widget",
		TargetURL:       "https://github.com/acme/widget/issues/1",
		TargetKind:      "issue",
		TargetNumber:    1,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		CurrentPhase:    "completion-harden",
		CurrentRound:    1,
		UpdatedAt:       ISOTimeNow(),
		APIBaseURL:      "https://api.github.com",
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	err := resumeGithubWork(localWorkResumeOptions{RunSelection: localWorkRunSelection{RunID: runID}})
	if err == nil || !strings.Contains(err.Error(), "missing baseline_sha") {
		t.Fatalf("expected missing baseline error, got %v", err)
	}
}

func TestSyncGithubWorkResumeLastUsesStoredFeedbackAndLeaderCheckpoint(t *testing.T) {
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	commandLogPath := filepath.Join(home, "codex-sync-commands.log")
	failOncePath := filepath.Join(home, "codex-sync-fail-once.marker")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`printf '%s\n' "$*" >> "${FAKE_CODEX_LOG_PATH}"`,
		`mkdir -p "$CODEX_HOME/sessions/2026/04/17"`,
		`printf '{"type":"session_meta","payload":{"id":"session-gh-sync","timestamp":"2099-01-01T00:00:00Z","cwd":"%s"}}\n' "$PWD" > "$CODEX_HOME/sessions/2026/04/17/rollout-session-gh-sync.jsonl"`,
		`if printf '%s' "$*" | grep -q "exec resume session-gh-sync"; then`,
		`  printf 'github sync resumed\n'`,
		`  exit 0`,
		`fi`,
		`if [ ! -f "${FAKE_CODEX_FAIL_ONCE_PATH}" ]; then`,
		`  : > "${FAKE_CODEX_FAIL_ONCE_PATH}"`,
		`  printf 'interrupted before feedback sync resume\n' >&2`,
		`  exit 1`,
		`fi`,
		`printf 'unexpected fresh codex args: %s\n' "$*" >&2`,
		`exit 1`,
		"",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG_PATH", commandLogPath)
	t.Setenv("FAKE_CODEX_FAIL_ONCE_PATH", failOncePath)
	t.Setenv("GH_TOKEN", "test-token")

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widget/issues/1/comments":
			_ = json.NewEncoder(w).Encode([]githubIssueCommentPayload{{
				ID:        11,
				HTMLURL:   "https://github.com/acme/widget/issues/1#issuecomment-11",
				Body:      "Please continue",
				UpdatedAt: "2026-04-23T20:00:00Z",
				User:      githubActor{Login: "reviewer-a"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	repoRoot := githubWorkRepoRoot("acme/widget")
	runID := "gh-sync-resume-last"
	runDir := filepath.Join(repoRoot, "runs", runID)
	sandboxPath := filepath.Join(runDir, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	createLocalWorkRepoAt(t, sandboxRepoPath)
	baselineSHA := strings.TrimSpace(runLocalWorkTestGitOutput(t, sandboxRepoPath, "rev-parse", "HEAD"))
	manifest := githubWorkManifest{
		Version:         1,
		RunID:           runID,
		RepoSlug:        "acme/widget",
		RepoOwner:       "acme",
		RepoName:        "widget",
		TargetURL:       "https://github.com/acme/widget/issues/1",
		TargetKind:      "issue",
		TargetNumber:    1,
		BaselineSHA:     baselineSHA,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		UpdatedAt:       ISOTimeNow(),
		APIBaseURL:      server.URL,
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	err := syncGithubWork(githubWorkSyncOptions{RunID: runID, Reviewer: "reviewer-a"})
	if err == nil {
		t.Fatalf("expected initial sync to fail and leave resume state, got %v", err)
	}
	if _, err := os.Stat(githubFeedbackResumeStatePath(runDir)); err != nil {
		t.Fatalf("expected feedback resume artifact after failed sync: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return syncGithubWork(githubWorkSyncOptions{RunID: runID, ResumeLast: true})
	})
	if err != nil {
		t.Fatalf("syncGithubWork: %v\n%s", err, output)
	}
	commandLog, err := os.ReadFile(commandLogPath)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	if !strings.Contains(string(commandLog), "exec resume session-gh-sync") {
		t.Fatalf("expected exec resume in log, got %q", string(commandLog))
	}
	if _, err := os.Stat(githubFeedbackResumeStatePath(runDir)); !os.IsNotExist(err) {
		t.Fatalf("expected feedback resume artifact to be removed after successful resume, got err=%v", err)
	}
}

func TestSyncGithubWorkResumeLastRequiresStoredFeedbackArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := githubWorkRepoRoot("acme/widget")
	runID := "gh-sync-resume-missing-artifact"
	runDir := filepath.Join(repoRoot, "runs", runID)
	sandboxPath := filepath.Join(runDir, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	createLocalWorkRepoAt(t, sandboxRepoPath)
	baselineSHA := strings.TrimSpace(runLocalWorkTestGitOutput(t, sandboxRepoPath, "rev-parse", "HEAD"))
	manifest := githubWorkManifest{
		Version:         1,
		RunID:           runID,
		RepoSlug:        "acme/widget",
		RepoOwner:       "acme",
		RepoName:        "widget",
		TargetURL:       "https://github.com/acme/widget/issues/1",
		TargetKind:      "issue",
		TargetNumber:    1,
		BaselineSHA:     baselineSHA,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		UpdatedAt:       ISOTimeNow(),
		APIBaseURL:      "https://api.github.com",
	}
	if err := writeGithubJSON(filepath.Join(runDir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeCodexStepCheckpoint(filepath.Join(runDir, "leader-checkpoint.json"), codexStepCheckpoint{
		Version:        1,
		StepKey:        "github-leader",
		Status:         "failed",
		SessionID:      "session-gh-sync",
		ResumeStrategy: string(codexResumeConversation),
		ResumeEligible: true,
	}); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	err := syncGithubWork(githubWorkSyncOptions{RunID: runID, ResumeLast: true})
	if err == nil || !strings.Contains(err.Error(), "stored feedback resume artifact") {
		t.Fatalf("expected missing artifact error, got %v", err)
	}
}

func TestSyncGithubWorkResumeLastRejectsInconsistentArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := githubWorkRepoRoot("acme/widget")
	runID := "gh-sync-resume-stale-artifact"
	runDir := filepath.Join(repoRoot, "runs", runID)
	sandboxPath := filepath.Join(runDir, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	createLocalWorkRepoAt(t, sandboxRepoPath)
	baselineSHA := strings.TrimSpace(runLocalWorkTestGitOutput(t, sandboxRepoPath, "rev-parse", "HEAD"))
	manifest := githubWorkManifest{
		Version:               1,
		RunID:                 runID,
		RepoSlug:              "acme/widget",
		RepoOwner:             "acme",
		RepoName:              "widget",
		TargetURL:             "https://github.com/acme/widget/issues/1",
		TargetKind:            "issue",
		TargetNumber:          1,
		BaselineSHA:           baselineSHA,
		SandboxPath:           sandboxPath,
		SandboxRepoPath:       sandboxRepoPath,
		UpdatedAt:             ISOTimeNow(),
		APIBaseURL:            "https://api.github.com",
		ControlPlaneReviewers: []string{"reviewer-a"},
	}
	if err := writeGithubJSON(filepath.Join(runDir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeCodexStepCheckpoint(filepath.Join(runDir, "leader-checkpoint.json"), codexStepCheckpoint{
		Version:           1,
		StepKey:           "github-leader",
		Status:            "failed",
		SessionID:         "session-gh-sync",
		PromptFingerprint: "some-other-prompt",
		ResumeStrategy:    string(codexResumeConversation),
		ResumeEligible:    true,
	}); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	if err := writeGithubJSON(githubFeedbackResumeStatePath(runDir), githubFeedbackResumeState{
		Version:           1,
		Actors:            []string{"reviewer-a"},
		NewFeedback:       githubFeedbackSnapshot{Actors: []string{"reviewer-a"}, IssueComments: []githubIssueCommentPayload{{ID: 11, Body: "Please continue"}}},
		PromptFingerprint: "mismatched-fingerprint",
		UpdatedAt:         ISOTimeNow(),
	}); err != nil {
		t.Fatalf("write feedback resume state: %v", err)
	}

	err := syncGithubWork(githubWorkSyncOptions{RunID: runID, ResumeLast: true})
	if err == nil || !strings.Contains(err.Error(), "stale or inconsistent") {
		t.Fatalf("expected inconsistent artifact error, got %v", err)
	}
}

func TestBuildGithubWorkStatusSnapshotIncludesLockState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repoRoot := githubWorkRepoRoot("acme/widget")
	runID := "gh-lock-status"
	runDir := filepath.Join(repoRoot, "runs", runID)
	sourcePath := filepath.Join(repoRoot, "source")
	sandboxPath := filepath.Join(repoRoot, "sandboxes", "issue-1-"+runID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.MkdirAll(sandboxRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox repo: %v", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	manifest := githubWorkManifest{
		Version:         1,
		RunID:           runID,
		RepoSlug:        "acme/widget",
		RepoOwner:       "acme",
		RepoName:        "widget",
		SourcePath:      sourcePath,
		TargetURL:       "https://github.com/acme/widget/issues/1",
		TargetKind:      "issue",
		TargetNumber:    1,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		UpdatedAt:       ISOTimeNow(),
	}
	sourceLock, err := acquireManagedSourceWriteLock("acme/widget", repoAccessLockOwner{
		Backend: "test",
		RunID:   "source-status",
		Purpose: "source-setup",
		Label:   "source-status",
	})
	if err != nil {
		t.Fatalf("acquire source lock: %v", err)
	}
	defer func() { _ = sourceLock.Release() }()
	sandboxLock, err := acquireSandboxReadLock(sandboxRepoPath, repoAccessLockOwner{
		Backend: "test",
		RunID:   "sandbox-status",
		Purpose: "review",
		Label:   "sandbox-status",
	})
	if err != nil {
		t.Fatalf("acquire sandbox lock: %v", err)
	}
	defer func() { _ = sandboxLock.Release() }()

	snapshot, err := buildGithubWorkStatusSnapshot(manifest, runDir)
	if err != nil {
		t.Fatalf("buildGithubWorkStatusSnapshot: %v", err)
	}
	if snapshot.LockState == nil || snapshot.LockState.Source == nil || snapshot.LockState.Sandbox == nil {
		t.Fatalf("expected lock state in snapshot, got %#v", snapshot)
	}
	if snapshot.LockState.Source.Writer == nil || !strings.Contains(snapshot.LockState.Source.Writer.Label, "source-status") {
		t.Fatalf("expected source writer in lock state, got %#v", snapshot.LockState.Source)
	}
	if len(snapshot.LockState.Sandbox.Readers) != 1 || !strings.Contains(snapshot.LockState.Sandbox.Readers[0].Label, "sandbox-status") {
		t.Fatalf("expected sandbox reader in lock state, got %#v", snapshot.LockState.Sandbox)
	}

	output, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if !strings.Contains(string(output), "\"lock_state\"") {
		t.Fatalf("expected lock_state in json snapshot: %s", output)
	}
	if snapshot.LockState.Source.Writer.Stale || snapshot.LockState.Sandbox.Readers[0].Stale {
		t.Fatalf("expected fresh lock state, got %#v", snapshot.LockState)
	}
}
