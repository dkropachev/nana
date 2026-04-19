package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := os.MkdirAll(sandboxRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox repo: %v", err)
	}
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
