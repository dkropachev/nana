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
	"time"
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
}

func TestScoutLocalFromFileKeepsAllProposalsForBothRoles(t *testing.T) {
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
			if !strings.Contains(output, "Keeping 7 proposal(s) local by policy") {
				t.Fatalf("expected uncapped local output, got %q", output)
			}
			matches, err := filepath.Glob(filepath.Join(repo, ".nana", tc.root, tc.globPrefix, "proposals.json"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("expected one proposals artifact, matches=%#v err=%v", matches, err)
			}
			var report improvementReport
			if err := readGithubJSON(matches[0], &report); err != nil {
				t.Fatalf("read report: %v", err)
			}
			if len(report.Proposals) != 7 {
				t.Fatalf("expected 7 proposals, got %d", len(report.Proposals))
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

func TestScoutLocalFromFileIgnoresLegacyMaxIssuesPolicy(t *testing.T) {
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
	if !strings.Contains(output, "Keeping 12 proposal(s) local by policy") {
		t.Fatalf("expected uncapped output, got %q", output)
	}
	matches, err := filepath.Glob(filepath.Join(repo, ".nana", "improvements", "improve-*", "proposals.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one proposals artifact, matches=%#v err=%v", matches, err)
	}
	var report improvementReport
	if err := readGithubJSON(matches[0], &report); err != nil {
		t.Fatalf("read report: %v", err)
	}
	if len(report.Proposals) != 12 {
		t.Fatalf("expected 12 proposals, got %d", len(report.Proposals))
	}
	policyArtifact, err := os.ReadFile(filepath.Join(filepath.Dir(matches[0]), "policy.json"))
	if err != nil {
		t.Fatalf("read policy artifact: %v", err)
	}
	if strings.Contains(string(policyArtifact), "max_issues") {
		t.Fatalf("expected legacy max_issues to be dropped from artifacts, got %s", policyArtifact)
	}
}

func TestRunScoutStartBlocksWhenSourceWriteLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repo := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	lock, err := acquireSourceWriteLock(repo, repoAccessLockOwner{
		Backend: "test",
		RunID:   "scout-start-writer",
		Purpose: "source-setup",
		Label:   "scout-start-writer",
	})
	if err != nil {
		t.Fatalf("acquire source write lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	err = runScoutStart(repo, ImproveOptions{})
	if err == nil || !strings.Contains(err.Error(), "repo read lock busy") {
		t.Fatalf("expected repo read lock busy, got %v", err)
	}
}

func TestRunScoutStartBlocksAutoModeWriteWhenReaderHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repo := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1,"mode":"auto"}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	inputPath := filepath.Join(home, "proposals.json")
	if err := os.WriteFile(inputPath, []byte(scoutProposalJSON(1, "docs")), 0o644); err != nil {
		t.Fatalf("write proposals: %v", err)
	}

	lock, err := acquireSourceReadLock(repo, repoAccessLockOwner{
		Backend: "test",
		RunID:   "scout-start-reader",
		Purpose: "inspect",
		Label:   "scout-start-reader",
	})
	if err != nil {
		t.Fatalf("acquire source read lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	err = runScoutStart(repo, ImproveOptions{FromFile: inputPath})
	if err == nil || !strings.Contains(err.Error(), "repo write lock busy") {
		t.Fatalf("expected repo write lock busy, got %v", err)
	}
}

func TestWriteLocalScoutArtifactsBlocksWhenSourceReadLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repo := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	dir := filepath.Join(repo, ".nana", scoutArtifactRoot(improvementScoutRole), "improve-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	lock, err := acquireSourceReadLock(repo, repoAccessLockOwner{
		Backend: "test",
		RunID:   "scout-artifact-reader",
		Purpose: "inspect",
		Label:   "scout-artifact-reader",
	})
	if err != nil {
		t.Fatalf("acquire source read lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	_, err = writeLocalScoutArtifacts(dir, scoutReport{
		Version:     1,
		Repo:        filepath.Base(repo),
		GeneratedAt: ISOTimeNow(),
		Proposals: []scoutFinding{{
			Title:   "Improve help text",
			Summary: "Make help clearer",
		}},
	}, scoutPolicy{Version: 1, IssueDestination: improvementDestinationLocal}, []byte("raw"), improvementScoutRole, nil)
	if err == nil || !strings.Contains(err.Error(), "repo write lock busy") {
		t.Fatalf("expected repo write lock busy, got %v", err)
	}
}

func TestPersistScoutExecutionArtifactsBlocksWhenSourceReadLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repo := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	artifactDir := filepath.Join(repo, ".nana", scoutArtifactRoot(uiScoutRole), "ui-scout-test")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	staging := filepath.Join(home, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("mkdir staging dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "settings.png"), []byte("evidence"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	lock, err := acquireSourceReadLock(repo, repoAccessLockOwner{
		Backend: "test",
		RunID:   "scout-persist-reader",
		Purpose: "inspect",
		Label:   "scout-persist-reader",
	})
	if err != nil {
		t.Fatalf("acquire source read lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	err = persistScoutExecutionArtifacts(scoutExecutionRuntime{ArtifactDir: staging}, repo, artifactDir)
	if err == nil || !strings.Contains(err.Error(), "repo write lock busy") {
		t.Fatalf("expected repo write lock busy, got %v", err)
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
		`printf 'codex transcript noise\n' >&2`,
		`printf '{"version":1,"proposals":[]}\n'`,
	}, "\n"))
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_ARGS_PATH", argsPath)

	output, err := captureStdout(t, func() error {
		return Improve(repo, []string{"--", "--fast"})
	})
	if err != nil {
		t.Fatalf("Improve: %v\n%s", err, output)
	}
	if strings.Contains(output, "codex transcript noise") {
		t.Fatalf("successful scout run should not print codex stderr, got %q", output)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake codex args: %v", err)
	}
	if !strings.Contains(string(args), "\n--\n/fast\n\n---\n") {
		t.Fatalf("expected option separator before frontmatter prompt, got:\n%s", args)
	}
	if !strings.Contains(string(args), "\n/fast\n\n---\n") {
		t.Fatalf("expected --fast to inject /fast before prompt, got:\n%s", args)
	}
}

func TestImproveResumeUsesExecResume(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	commandLogPath := filepath.Join(home, "codex-commands.log")
	failOncePath := filepath.Join(home, "improve-fail-once.marker")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`printf '%s\n' "$*" >> "${FAKE_CODEX_LOG_PATH}"`,
		`mkdir -p "$CODEX_HOME/sessions/2026/04/17"`,
		`printf '{"type":"session_meta","payload":{"id":"session-scout","timestamp":"2099-01-01T00:00:00Z","cwd":"%s"}}\n' "$PWD" > "$CODEX_HOME/sessions/2026/04/17/rollout-session-scout.jsonl"`,
		`if printf '%s' "$*" | grep -q "exec resume session-scout"; then`,
		`  printf '{"version":1,"repo":"widget","proposals":[{"title":"Clarify empty states","area":"UX","summary":"Explain what to do when a list is empty."}]}\n'`,
		`  exit 0`,
		`fi`,
		`if [ ! -f "${FAKE_IMPROVE_FAIL_ONCE_PATH}" ]; then`,
		`  : > "${FAKE_IMPROVE_FAIL_ONCE_PATH}"`,
		`  printf 'rate limited\n' >&2`,
		`  exit 1`,
		`fi`,
		`  printf '{"version":1,"repo":"widget","proposals":[{"title":"Clarify empty states","area":"UX","summary":"Explain what to do when a list is empty."}]}\n'`,
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG_PATH", commandLogPath)
	t.Setenv("FAKE_IMPROVE_FAIL_ONCE_PATH", failOncePath)

	if err := Improve(repo, nil); err == nil {
		t.Fatal("expected initial improve run to fail")
	}
	output, err := captureStdout(t, func() error {
		return Improve(repo, []string{"--last"})
	})
	if err != nil {
		t.Fatalf("Improve(resume): %v\n%s", err, output)
	}
	commandLog, err := os.ReadFile(commandLogPath)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	if !strings.Contains(string(commandLog), "exec resume session-scout") {
		t.Fatalf("expected exec resume in log, got %q", string(commandLog))
	}
	matches, err := filepath.Glob(filepath.Join(repo, ".nana", "improvements", "improve-*", "manifest.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one scout manifest, matches=%#v err=%v", matches, err)
	}
	var manifest scoutRunManifest
	if err := readGithubJSON(matches[0], &manifest); err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.Status != "completed" {
		t.Fatalf("expected completed manifest, got %#v", manifest)
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

func TestUIScoutRunsPreflightAndWritesArtifact(t *testing.T) {
	repo := t.TempDir()
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "codex-count.txt")
	promptDir := filepath.Join(t.TempDir(), "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatalf("mkdir prompts dir: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		`count=0`,
		`if [ -f "$FAKE_CODEX_COUNT_PATH" ]; then count=$(cat "$FAKE_CODEX_COUNT_PATH"); fi`,
		`count=$((count + 1))`,
		`printf '%s' "$count" > "$FAKE_CODEX_COUNT_PATH"`,
		`pwd > "$FAKE_CODEX_PROMPT_DIR/cwd-$count.txt"`,
		`printf '%s\n' "$CODEX_HOME" > "$FAKE_CODEX_PROMPT_DIR/codex-home-$count.txt"`,
		`printf '%s\n' "$@" > "$FAKE_CODEX_PROMPT_DIR/args-$count.txt"`,
		`if [ "$count" -eq 1 ]; then`,
		`  printf '{"version":1,"browser_ready":false,"mode":"repo_only","surface_kind":"storybook","surface_target":"storybook","reason":"browser tools unavailable"}\n'`,
		`else`,
		`  touch ui-scout-sandbox-sentinel.txt`,
		`  artifact_dir=$(printf '%s\n' "$@" | awk '/^- Artifact directory: /{sub("^- Artifact directory: ",""); print; exit}')`,
		`  if [ -n "$artifact_dir" ]; then mkdir -p "$artifact_dir/evidence"; printf 'png' > "$artifact_dir/evidence/settings.png"; fi`,
		`  printf '{"version":1,"repo":"widget","proposals":[{"title":"Settings layout breaks","area":"UI","summary":"The settings page collapses unevenly.","evidence":"storybook settings mock","page":"Settings","route":"/settings","severity":"major","target_kind":"mock","screenshots":["evidence/settings.png"]}]}\n'`,
		`fi`,
	}, "\n"))
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_COUNT_PATH", countPath)
	t.Setenv("FAKE_CODEX_PROMPT_DIR", promptDir)

	output, err := captureStdout(t, func() error {
		return UIScout(repo, []string{"--session-limit", "6", "--", "--fast"})
	})
	if err != nil {
		t.Fatalf("UIScout: %v\n%s", err, output)
	}
	if !strings.Contains(output, "Preflight: mode=repo_only surface=storybook target=storybook session-limit=6") {
		t.Fatalf("missing ui-scout preflight banner: %q", output)
	}
	secondPrompt, err := os.ReadFile(filepath.Join(promptDir, "args-2.txt"))
	if err != nil {
		t.Fatalf("read second prompt: %v", err)
	}
	promptText := string(secondPrompt)
	if !strings.Contains(promptText, "hard cap of 6 concurrent sessions") || !strings.Contains(promptText, "Preflight mode: repo_only") {
		t.Fatalf("full ui-scout prompt missing preflight context:\n%s", promptText)
	}
	secondCWD, err := os.ReadFile(filepath.Join(promptDir, "cwd-2.txt"))
	if err != nil {
		t.Fatalf("read second cwd: %v", err)
	}
	if strings.TrimSpace(string(secondCWD)) == repo {
		t.Fatalf("expected ui-scout to run in an isolated sandbox, got cwd %q", strings.TrimSpace(string(secondCWD)))
	}
	secondCodexHome, err := os.ReadFile(filepath.Join(promptDir, "codex-home-2.txt"))
	if err != nil {
		t.Fatalf("read second CODEX_HOME: %v", err)
	}
	codexHomePath := strings.TrimSpace(string(secondCodexHome))
	if codexHomePath == "" || !strings.Contains(codexHomePath, string(filepath.Separator)+".nana"+string(filepath.Separator)+"ui-findings"+string(filepath.Separator)) || !strings.HasSuffix(codexHomePath, filepath.Join("_runtime", "codex-home")) {
		t.Fatalf("expected persisted isolated CODEX_HOME under ui-findings runtime, got %q", codexHomePath)
	}
	if fileExists(filepath.Join(repo, "ui-scout-sandbox-sentinel.txt")) {
		t.Fatalf("ui-scout wrote into the source repo instead of the sandbox")
	}
	preflightMatches, err := filepath.Glob(filepath.Join(repo, ".nana", "ui-findings", "ui-scout-*", "preflight.json"))
	if err != nil || len(preflightMatches) != 1 {
		t.Fatalf("expected one preflight artifact, matches=%#v err=%v", preflightMatches, err)
	}
	var preflight uiScoutPreflight
	if err := readGithubJSON(preflightMatches[0], &preflight); err != nil {
		t.Fatalf("read preflight artifact: %v", err)
	}
	if preflight.Mode != "repo_only" || preflight.SurfaceTarget != "storybook" {
		t.Fatalf("unexpected preflight artifact: %+v", preflight)
	}
	draftPath := filepath.Join(filepath.Dir(preflightMatches[0]), "issue-drafts.md")
	draft, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatalf("read issue draft: %v", err)
	}
	draftText := string(draft)
	if !strings.Contains(draftText, "Audit mode: repo_only") || !strings.Contains(draftText, "Surface target: storybook") {
		t.Fatalf("missing preflight context in issue draft:\n%s", draftText)
	}
	evidencePath := filepath.Join(filepath.Dir(preflightMatches[0]), "evidence", "settings.png")
	if content, err := os.ReadFile(evidencePath); err != nil || string(content) != "png" {
		t.Fatalf("expected ui-scout evidence copied back from sandbox, err=%v content=%q", err, string(content))
	}
}

func TestUIScoutBlockedPreflightFailsFast(t *testing.T) {
	repo := t.TempDir()
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\ncat >/dev/null\nprintf '{\"version\":1,\"browser_ready\":false,\"mode\":\"blocked\",\"surface_kind\":\"unknown\",\"surface_target\":\"\",\"reason\":\"no UI entrypoint found\"}\\n'\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	err := UIScout(repo, nil)
	if err == nil {
		t.Fatal("expected blocked ui-scout preflight error")
	}
	if !strings.Contains(err.Error(), "could not find a runnable UI surface") {
		t.Fatalf("unexpected ui-scout error: %v", err)
	}
}

func TestStartRunsUIScoutInIsolatedSandbox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "ui-policy.json"), []byte(`{"version":1,"session_limit":2}`), 0o644); err != nil {
		t.Fatalf("write ui policy: %v", err)
	}
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "codex-count.txt")
	cwdPath := filepath.Join(t.TempDir(), "codex-cwd.txt")
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		`count=0`,
		`if [ -f "$FAKE_CODEX_COUNT_PATH" ]; then count=$(cat "$FAKE_CODEX_COUNT_PATH"); fi`,
		`count=$((count + 1))`,
		`printf '%s' "$count" > "$FAKE_CODEX_COUNT_PATH"`,
		`if [ "$count" -eq 2 ]; then pwd > "$FAKE_CODEX_CWD_PATH"; touch ui-scout-start-sentinel.txt; fi`,
		`if [ "$count" -eq 1 ]; then`,
		`  printf '{"version":1,"browser_ready":false,"mode":"repo_only","surface_kind":"app","surface_target":"dev","reason":"preflight ok"}\n'`,
		`else`,
		`  printf '{"version":1,"repo":"widget","proposals":[]}\n'`,
		`fi`,
	}, "\n"))
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_COUNT_PATH", countPath)
	t.Setenv("FAKE_CODEX_CWD_PATH", cwdPath)

	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--repo", repo, "--once"})
	})
	if err != nil {
		t.Fatalf("Start(): %v\n%s", err, output)
	}
	if !strings.Contains(output, "ui-scout supported; running") {
		t.Fatalf("expected ui-scout to run through nana start, got %q", output)
	}
	cwd, err := os.ReadFile(cwdPath)
	if err != nil {
		t.Fatalf("read ui-scout cwd: %v", err)
	}
	if strings.TrimSpace(string(cwd)) == repo {
		t.Fatalf("expected nana start ui-scout to run in an isolated sandbox, got cwd %q", strings.TrimSpace(string(cwd)))
	}
	if fileExists(filepath.Join(repo, "ui-scout-start-sentinel.txt")) {
		t.Fatalf("nana start ui-scout wrote into the source repo instead of the sandbox")
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

func TestStartAutoModePicksExistingLocalDiscoveryBeforeRunningScouts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	runScoutTestGit(t, repo, "init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nana", "improvement-policy.json"), []byte(`{"version":1,"mode":"auto"}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	writeScoutPickupFixture(t, repo, improvementScoutRole, "Existing local item", "Handle existing item")
	runScoutTestGit(t, repo, "add", ".")
	runScoutTestGit(t, repo, "commit", "-m", "init")

	oldRun := startRunLocalScoutWork
	defer func() { startRunLocalScoutWork = oldRun }()
	picked := []string{}
	startRunLocalScoutWork = func(repoPath string, task string, codexArgs []string) error {
		picked = append(picked, task)
		return nil
	}

	output, err := captureStdout(t, func() error {
		return Start(repo, []string{"--once"})
	})
	if err != nil {
		t.Fatalf("Start(): %v\n%s", err, output)
	}
	if len(picked) != 1 || !strings.Contains(picked[0], "Existing local item") {
		t.Fatalf("expected existing item pickup before scouts, got %#v", picked)
	}
	if !strings.Contains(output, "Local discovered items: 1 pending; working on: Existing local item") {
		t.Fatalf("missing pickup output: %q", output)
	}
	if strings.Contains(output, "improvement-scout supported; running") {
		t.Fatalf("scouts should not run when an existing local item was picked first: %q", output)
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
	if subject := strings.TrimSpace(scoutTestGitOutput(t, repo, "log", "-1", "--pretty=%s")); subject != "Record 4 scout items: Proposal 1" {
		t.Fatalf("expected scout artifact commit, got %q", subject)
	}
	body := scoutTestGitOutput(t, repo, "log", "-1", "--pretty=%B")
	for _, expected := range []string{"Scout items:", "- Improvement: Proposal 1", "- Improvement: Proposal 2", "- Enhancement: Proposal 1", "- Enhancement: Proposal 2", "Artifact: .nana/improvements/", "Artifact: .nana/enhancements/"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected commit body to contain %q:\n%s", expected, body)
		}
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
	configureTestGitInsteadOf(t, "git@github.com:acme/widget.git", repo)
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
	configureTestGitInsteadOf(t, "git@github.com:acme/widget.git", repo)
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

func TestEnsureImproveGithubCheckoutCreatesManagedSourceAndRepairsOrigin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "token")

	paths, repoMeta := createGithubManagedSourceFixture(t, home, "acme/widget")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widget" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(githubRepositoryPayload{
			Name:          repoMeta.RepoName,
			FullName:      repoMeta.RepoSlug,
			CloneURL:      repoMeta.CloneURL,
			DefaultBranch: repoMeta.DefaultBranch,
			HTMLURL:       repoMeta.HTMLURL,
		})
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	oldPreflight := githubManagedOriginPreflight
	githubManagedOriginPreflight = func(repoPath string, repoMeta *githubManagedRepoMetadata) error { return nil }
	defer func() { githubManagedOriginPreflight = oldPreflight }()

	repoPath, err := ensureImproveGithubCheckout("acme/widget")
	if err != nil {
		t.Fatalf("ensureImproveGithubCheckout: %v", err)
	}
	if repoPath != paths.SourcePath {
		t.Fatalf("expected managed source path %q, got %q", paths.SourcePath, repoPath)
	}
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("stat managed source path: %v", err)
	}
	gotOrigin := strings.TrimSpace(runLocalWorkTestGitOutput(t, repoPath, "config", "--get", "remote.origin.url"))
	if gotOrigin != repoMeta.CanonicalOriginURL {
		t.Fatalf("expected managed source origin repaired to %q, got %q", repoMeta.CanonicalOriginURL, gotOrigin)
	}
	head := strings.TrimSpace(runLocalWorkTestGitOutput(t, repoPath, "rev-parse", "HEAD"))
	remoteHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, "", "--git-dir", repoMeta.CloneURL, "rev-parse", "refs/heads/main"))
	if head != remoteHead {
		t.Fatalf("expected managed source head %q to match origin head %q", head, remoteHead)
	}
}

func TestPublishImprovementIssuesUsesForkPolicyAndImprovementLabels(t *testing.T) {
	t.Setenv("GH_TOKEN", "token")
	var capturedPath string
	var capturedPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestPublishScoutIssuesPublishesAllProposals(t *testing.T) {
	for _, tc := range []struct {
		name        string
		proposals   int
		dryRun      bool
		wantResults int
		wantPosts   int
	}{
		{name: "publishes every proposal", proposals: 6, wantResults: 6, wantPosts: 6},
		{name: "publishes all when already small", proposals: 2, wantResults: 2, wantPosts: 2},
		{name: "dry run returns every proposal without post", proposals: 3, dryRun: true, wantResults: 3, wantPosts: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", "token")
			postCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
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
