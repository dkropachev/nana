package gocli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveCLIInvocation(t *testing.T) {
	testCases := []struct {
		name       string
		args       []string
		wantCmd    string
		wantLaunch []string
	}{
		{name: "empty launches", args: nil, wantCmd: "launch", wantLaunch: nil},
		{name: "leading flag launches", args: []string{"--high"}, wantCmd: "launch", wantLaunch: []string{"--high"}},
		{name: "explicit launch", args: []string{"launch", "--high"}, wantCmd: "launch", wantLaunch: []string{"--high"}},
		{name: "resume", args: []string{"resume", "latest"}, wantCmd: "resume", wantLaunch: []string{"latest"}},
		{name: "exec", args: []string{"exec", "--help"}, wantCmd: "exec", wantLaunch: []string{"--help"}},
		{name: "implement", args: []string{"implement", "https://github.com/acme/widget/issues/42", "--create-pr"}, wantCmd: "implement", wantLaunch: []string{"https://github.com/acme/widget/issues/42", "--create-pr"}},
		{name: "investigate", args: []string{"investigate", "https://github.com/acme/widget/issues/42"}, wantCmd: "investigate", wantLaunch: []string{"https://github.com/acme/widget/issues/42"}},
		{name: "sync", args: []string{"sync", "--last"}, wantCmd: "sync", wantLaunch: []string{"--last"}},
		{name: "issue", args: []string{"issue", "implement", "https://github.com/acme/widget/issues/42"}, wantCmd: "issue", wantLaunch: nil},
		{name: "review", args: []string{"review", "https://github.com/acme/widget/pull/77"}, wantCmd: "review", wantLaunch: nil},
		{name: "review-rules", args: []string{"review-rules", "scan", "acme/widget"}, wantCmd: "review-rules", wantLaunch: nil},
		{name: "repo", args: []string{"repo", "onboard"}, wantCmd: "repo", wantLaunch: nil},
		{name: "start", args: []string{"start", "help"}, wantCmd: "start", wantLaunch: nil},
		{name: "next", args: []string{"next", "--json"}, wantCmd: "next", wantLaunch: nil},
		{name: "route", args: []string{"route", "--explain", "fix build"}, wantCmd: "route", wantLaunch: nil},
		{name: "work", args: []string{"work", "help"}, wantCmd: "work", wantLaunch: nil},
		{name: "work-on", args: []string{"work-on", "help"}, wantCmd: "work-on", wantLaunch: nil},
		{name: "work-local", args: []string{"work-local", "help"}, wantCmd: "work-local", wantLaunch: nil},
		{name: "help alias", args: []string{"--help"}, wantCmd: "help", wantLaunch: nil},
		{name: "version alias", args: []string{"-v"}, wantCmd: "version", wantLaunch: nil},
		{name: "explore aliases reflect", args: []string{"explore"}, wantCmd: "reflect", wantLaunch: nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveCLIInvocation(tc.args)
			if got.Command != tc.wantCmd {
				t.Fatalf("ResolveCLIInvocation(%v).Command = %q, want %q", tc.args, got.Command, tc.wantCmd)
			}
			if strings.Join(got.LaunchArgs, "\x00") != strings.Join(tc.wantLaunch, "\x00") {
				t.Fatalf("ResolveCLIInvocation(%v).LaunchArgs = %v, want %v", tc.args, got.LaunchArgs, tc.wantLaunch)
			}
		})
	}
}

func TestNormalizeCodexLaunchArgs(t *testing.T) {
	testCases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "madmax becomes codex bypass",
			args: []string{MadmaxFlag},
			want: []string{CodexBypassFlag},
		},
		{
			name: "high reasoning becomes config",
			args: []string{HighReasoningFlag},
			want: []string{ConfigFlag, `model_reasoning_effort="high"`},
		},
		{
			name: "effort flag becomes config",
			args: []string{"--effort", "xhigh"},
			want: []string{ConfigFlag, `model_reasoning_effort="xhigh"`},
		},
		{
			name: "effort equals becomes config",
			args: []string{"--effort=low"},
			want: []string{ConfigFlag, `model_reasoning_effort="low"`},
		},
		{
			name: "xhigh madmax composes",
			args: []string{XHighReasoningFlag, MadmaxFlag},
			want: []string{CodexBypassFlag, ConfigFlag, `model_reasoning_effort="xhigh"`},
		},
		{
			name: "spark leader flag is consumed",
			args: []string{SparkFlag, "--yolo"},
			want: []string{"--yolo"},
		},
		{
			name: "madmax spark only adds bypass",
			args: []string{MadmaxSparkFlag},
			want: []string{CodexBypassFlag},
		},
		{
			name: "worktree flags are stripped",
			args: []string{"--worktree=feature/demo", "--model", "gpt-5"},
			want: []string{"--model", "gpt-5"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeCodexLaunchArgs(tc.args)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("NormalizeCodexLaunchArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestNormalizeCodexLaunchArgsWithFast(t *testing.T) {
	normalized, fast := NormalizeCodexLaunchArgsWithFast([]string{CodexFastFlag, "--model", "gpt-5.4"})
	if !fast {
		t.Fatal("expected fast mode")
	}
	if strings.Join(normalized, "\x00") != strings.Join([]string{"--model", "gpt-5.4"}, "\x00") {
		t.Fatalf("unexpected normalized args: %#v", normalized)
	}
}

func TestInjectCodexFastSlashCommand(t *testing.T) {
	got := injectCodexFastSlashCommand([]string{"exec", "-C", "/repo", "do work"}, true)
	want := []string{"exec", "-C", "/repo", "/fast\n\ndo work"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("injectCodexFastSlashCommand() = %#v, want %#v", got, want)
	}
}

func TestBuildGithubCodexEnvSeedsTokensFromGhAuth(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_HOST", "")

	fakeRoot := t.TempDir()
	fakeBin := filepath.Join(fakeRoot, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	script := strings.Join([]string{
		"#!/bin/sh",
		"if [ \"$1\" = \"auth\" ] && [ \"$2\" = \"status\" ]; then",
		"  exit 0",
		"fi",
		"if [ \"$1\" = \"auth\" ] && [ \"$2\" = \"token\" ]; then",
		"  printf 'bridged-token\\n'",
		"  exit 0",
		"fi",
		"exit 1",
	}, "\n")
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	env := envListToMapForTest(buildGithubCodexEnv(NotifyTempContract{}, filepath.Join(fakeRoot, "codex-home"), "https://api.github.com"))
	if env["GH_TOKEN"] != "bridged-token" {
		t.Fatalf("expected GH_TOKEN bridge, got %q", env["GH_TOKEN"])
	}
	if env["GITHUB_TOKEN"] != "bridged-token" {
		t.Fatalf("expected GITHUB_TOKEN bridge, got %q", env["GITHUB_TOKEN"])
	}
	if env["CODEX_HOME"] != filepath.Join(fakeRoot, "codex-home") {
		t.Fatalf("expected CODEX_HOME override, got %q", env["CODEX_HOME"])
	}
}

func envListToMapForTest(entries []string) map[string]string {
	envMap := map[string]string{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		envMap[key] = value
	}
	return envMap
}

func TestInjectCodexFastSlashCommandWithoutPrompt(t *testing.T) {
	got := injectCodexFastSlashCommand([]string{"--model", "gpt-5.4"}, true)
	want := []string{"--model", "gpt-5.4", "/fast"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("injectCodexFastSlashCommand() = %#v, want %#v", got, want)
	}
}

func TestParseNotifyTempContract(t *testing.T) {
	parsed := ParseNotifyTempContract(
		[]string{
			"--notify-temp",
			"--discord",
			"--custom",
			"openclaw:ops",
			"--custom=my-hook",
			"--model",
			"gpt-5",
		},
		map[string]string{},
	)

	if !parsed.Contract.Active {
		t.Fatalf("expected notify temp to be active")
	}
	if parsed.Contract.Source != "cli" {
		t.Fatalf("ParseNotifyTempContract().Source = %q", parsed.Contract.Source)
	}
	wantSelectors := []string{"discord", "openclaw:ops", "custom:my-hook"}
	if strings.Join(parsed.Contract.CanonicalSelectors, "\x00") != strings.Join(wantSelectors, "\x00") {
		t.Fatalf("canonical selectors = %v, want %v", parsed.Contract.CanonicalSelectors, wantSelectors)
	}
	if strings.Join(parsed.PassthroughArgs, "\x00") != strings.Join([]string{"--model", "gpt-5"}, "\x00") {
		t.Fatalf("passthrough args = %v", parsed.PassthroughArgs)
	}
}

func TestParseWorktreeMode(t *testing.T) {
	testCases := []struct {
		name          string
		args          []string
		wantMode      WorktreeMode
		wantRemaining []string
	}{
		{
			name:          "detached mode from --worktree",
			args:          []string{"--worktree", "--yolo"},
			wantMode:      WorktreeMode{Enabled: true, Detached: true},
			wantRemaining: []string{"--yolo"},
		},
		{
			name:          "named mode from --worktree=value",
			args:          []string{"--worktree=feature/demo", "--model", "gpt-5"},
			wantMode:      WorktreeMode{Enabled: true, Detached: false, Name: "feature/demo"},
			wantRemaining: []string{"--model", "gpt-5"},
		},
		{
			name:          "named mode from spaced --worktree",
			args:          []string{"--worktree", "feature/demo", "--high"},
			wantMode:      WorktreeMode{Enabled: true, Detached: false, Name: "feature/demo"},
			wantRemaining: []string{"--high"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseWorktreeMode(tc.args)
			if got.Mode != tc.wantMode {
				t.Fatalf("ParseWorktreeMode(%v).Mode = %+v, want %+v", tc.args, got.Mode, tc.wantMode)
			}
			if strings.Join(got.RemainingArgs, "\x00") != strings.Join(tc.wantRemaining, "\x00") {
				t.Fatalf("ParseWorktreeMode(%v).RemainingArgs = %v, want %v", tc.args, got.RemainingArgs, tc.wantRemaining)
			}
		})
	}
}

func TestExecCreatesSessionInstructionsAndCleansUp(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")
	codexHome := DefaultUserCodexHome(home)

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("# User Instructions\n\nGlobal guidance.\n"), 0o644); err != nil {
		t.Fatalf("write user AGENTS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# Project Instructions\n\nProject guidance.\n"), 0o644); err != nil {
		t.Fatalf("write project AGENTS: %v", err)
	}
	if err := os.WriteFile(fakeCodexPath, []byte(strings.Join([]string{
		"#!/bin/sh",
		"printf 'fake-codex:%s\\n' \"$*\"",
		"for arg in \"$@\"; do",
		"  case \"$arg\" in",
		"    model_instructions_file=*)",
		"      file=$(printf %s \"$arg\" | sed 's/^model_instructions_file=\"//; s/\"$//')",
		"      printf 'instructions-path:%s\\n' \"$file\"",
		"      printf 'instructions-start\\n'",
		"      cat \"$file\"",
		"      printf 'instructions-end\\n'",
		"      ;;",
		"  esac",
		"done",
	}, "\n")), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Exec(cwd, []string{"--fast", "--model", "gpt-5", "say hi"})
	})
	if err != nil {
		t.Fatalf("Exec(): %v", err)
	}
	if !strings.Contains(output, "fake-codex:exec --model gpt-5 /fast") || !strings.Contains(output, "say hi ") {
		t.Fatalf("unexpected codex invocation: %q", output)
	}
	if !strings.Contains(output, "instructions-path:") {
		t.Fatalf("missing instructions path in %q", output)
	}
	if !strings.Contains(output, "# User Instructions") || !strings.Contains(output, "# Project Instructions") {
		t.Fatalf("missing composed AGENTS content in %q", output)
	}
	if !strings.Contains(output, "<!-- NANA:RUNTIME:START -->") {
		t.Fatalf("missing runtime overlay marker in %q", output)
	}

	sessionRoot := filepath.Join(cwd, ".nana", "state", "sessions")
	entries, err := os.ReadDir(sessionRoot)
	if err != nil {
		t.Fatalf("read session root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one session directory, got %d", len(entries))
	}
	sessionFiles, err := os.ReadDir(filepath.Join(sessionRoot, entries[0].Name()))
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	for _, entry := range sessionFiles {
		if entry.Name() == "AGENTS.md" {
			t.Fatalf("session instructions file should be cleaned up")
		}
	}
	if _, err := os.Stat(filepath.Join(cwd, ".nana", "state", "session.json")); !os.IsNotExist(err) {
		t.Fatalf("session.json should be removed, got err=%v", err)
	}
}

func TestWriteSessionModelInstructionsDedupesRepeatedBlocks(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex-home")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("# Shared\n\nRepeated block.\n\n# User Only\n\nKeep me.\n"), 0o644); err != nil {
		t.Fatalf("write user AGENTS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# Shared\n\nRepeated block.\n\n# Project Only\n\nKeep me too.\n"), 0o644); err != nil {
		t.Fatalf("write project AGENTS: %v", err)
	}
	path, err := writeSessionModelInstructions(cwd, "session-1", codexHome)
	if err != nil {
		t.Fatalf("writeSessionModelInstructions: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session instructions: %v", err)
	}
	text := string(content)
	if strings.Count(text, "# Shared") != 1 {
		t.Fatalf("expected deduped shared block once:\n%s", text)
	}
	for _, needle := range []string{"# User Only", "# Project Only", "<!-- NANA:RUNTIME:START -->"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected session instructions to contain %q:\n%s", needle, text)
		}
	}
}

func TestWriteSessionModelInstructionsIncludesSkillContextBudgetAdvisory(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex-home")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("# User Only\n"), 0o644); err != nil {
		t.Fatalf("write user AGENTS: %v", err)
	}
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	if err := os.MkdirAll(logPath, 0o755); err != nil {
		t.Fatalf("mkdir sentinel telemetry path: %v", err)
	}

	path, err := writeSessionModelInstructionsWithTelemetryScope(cwd, "session-1", codexHome, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-7"},
		loadedSkillRuntimeDoc{Skill: "plan", ActualPath: "skills/plan/RUNTIME.md"},
		loadedSkillRuntimeDoc{Skill: "tdd", ActualPath: "skills/tdd/RUNTIME.md"},
		loadedSkillRuntimeDoc{Skill: "build-fix", ActualPath: "skills/build-fix/RUNTIME.md"},
		loadedSkillRuntimeDoc{Skill: "code-review", ActualPath: "skills/code-review/RUNTIME.md"},
	)
	if err != nil {
		t.Fatalf("writeSessionModelInstructionsWithTelemetryScope: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session instructions: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"<!-- NANA:SKILL_CONTEXT_BUDGET:START -->",
		`scope="current turn_id=turn-7 within run_id=run-budget"`,
		`docs="4/3"`,
		"warning: skill runtime docs loaded 4 unique files (budget 3)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected session instructions to contain %q:\n%s", want, text)
		}
	}
}

func TestWriteSessionModelInstructionsAccumulatesCurrentTurnBudgetAcrossLaunches(t *testing.T) {
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex-home")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("# User Only\n"), 0o644); err != nil {
		t.Fatalf("write user AGENTS: %v", err)
	}
	writeTelemetryLog(t, filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson"), []string{
		`{"timestamp":"2026-04-20T12:00:00Z","run_id":"run-budget","turn_id":"turn-7","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:01Z","run_id":"run-budget","turn_id":"turn-7","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:02Z","run_id":"run-budget","turn_id":"turn-7","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:01:00Z","run_id":"run-budget","turn_id":"turn-other","event":"skill_doc_load","skill":"web-clone","path":"skills/web-clone/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:01:01Z","run_id":"run-budget","turn_id":"turn-other","event":"skill_doc_load","skill":"security-review","path":"skills/security-review/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:01:02Z","run_id":"run-budget","turn_id":"turn-other","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:01:03Z","run_id":"run-budget","turn_id":"turn-other","event":"skill_doc_load","skill":"ecomode","path":"skills/ecomode/RUNTIME.md"}`,
	})

	path, err := writeSessionModelInstructionsWithTelemetryScope(cwd, "session-1", codexHome, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-7"},
		loadedSkillRuntimeDoc{Skill: "code-review", ActualPath: "skills/code-review/RUNTIME.md"},
	)
	if err != nil {
		t.Fatalf("writeSessionModelInstructionsWithTelemetryScope: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session instructions: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"<!-- NANA:SKILL_CONTEXT_BUDGET:START -->",
		`scope="current turn_id=turn-7 within run_id=run-budget"`,
		`docs="4/3"`,
		"warning: skill runtime docs loaded 4 unique files (budget 3)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected session instructions to contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `docs="7/3"`) {
		t.Fatalf("expected advisory to stay scoped to the current turn instead of the whole run:\n%s", text)
	}
}

func TestSessionSkillContextBudgetAdvisorySkipsTelemetryScanForFreshTurn(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T12:00:00Z","run_id":"run-parent","turn_id":"turn-old","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:01Z","run_id":"run-parent","turn_id":"turn-old","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:02Z","run_id":"run-parent","turn_id":"turn-old","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:03Z","run_id":"run-parent","turn_id":"turn-old","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-parent")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	scope := resolveContextTelemetryScope(contextTelemetryScope{}, "nana-session")
	if !scope.GeneratedTurnID {
		t.Fatalf("expected a generated launch turn scope, got %+v", scope)
	}

	originalOpen := openTelemetrySkillBudgetLog
	defer func() {
		openTelemetrySkillBudgetLog = originalOpen
	}()
	openCalls := 0
	openTelemetrySkillBudgetLog = func(path string) (telemetryBudgetLogReadSeeker, error) {
		openCalls++
		return originalOpen(path)
	}

	got := sessionSkillContextBudgetAdvisoryBlock(cwd, scope, []loadedSkillRuntimeDoc{
		{Skill: "plan", ActualPath: "skills/plan/RUNTIME.md"},
	})
	if got != "" {
		t.Fatalf("expected no advisory from stale sibling-turn telemetry, got %q", got)
	}
	if openCalls != 0 {
		t.Fatalf("fresh launch turn should not scan historical telemetry log, opened it %d time(s)", openCalls)
	}
	cacheFiles, err := filepath.Glob(filepath.Join(BaseStateDir(cwd), "context-telemetry-skill-budget-*.json"))
	if err != nil {
		t.Fatalf("glob telemetry skill budget cache files: %v", err)
	}
	if len(cacheFiles) != 0 {
		t.Fatalf("fresh launch turn should not create scoped skill budget cache files, got %v", cacheFiles)
	}
}

func TestExecHelpPassesThroughToCodex(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex-home"))
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(fakeCodexPath, []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Exec(cwd, []string{"--help"})
	})
	if err != nil {
		t.Fatalf("Exec(--help): %v", err)
	}
	if !strings.Contains(output, "fake-codex:exec --help ") && !strings.Contains(output, "fake-codex:exec --help") {
		t.Fatalf("unexpected help passthrough output: %q", output)
	}
}

func TestLaunchCreatesDetachedWorktreeAndNotifyContract(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex-home"))
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "launch-test@example.com"},
		{"config", "user.name", "Launch Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	stub := strings.Join([]string{
		"#!/bin/sh",
		"printf 'fake-codex-cwd:%s\\n' \"$PWD\"",
		"printf 'fake-codex:%s\\n' \"$*\"",
		"printf 'notify-temp:%s\\n' \"$NANA_NOTIFY_TEMP\"",
		"printf 'notify-contract:%s\\n' \"$NANA_NOTIFY_TEMP_CONTRACT\"",
	}, "\n")
	if err := os.WriteFile(fakeCodexPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Launch(cwd, []string{"--worktree", "--notify-temp", "--discord", "--model", "gpt-5"})
	})
	if err != nil {
		t.Fatalf("Launch(): %v", err)
	}
	if !strings.Contains(output, "fake-codex-cwd:") || !strings.Contains(output, "launch-detached") {
		t.Fatalf("expected codex to run from detached worktree, got %q", output)
	}
	if !strings.Contains(output, "fake-codex:--model gpt-5 ") && !strings.Contains(output, "fake-codex:--model gpt-5") {
		t.Fatalf("expected launch args without notify/worktree flags, got %q", output)
	}
	if strings.Contains(output, "--notify-temp") || strings.Contains(output, "--discord") || strings.Contains(output, "--worktree") {
		t.Fatalf("launch leaked nana-only flags into codex args: %q", output)
	}
	if !strings.Contains(output, "notify-temp:1") {
		t.Fatalf("expected NANA_NOTIFY_TEMP=1 in codex env, got %q", output)
	}
	if !strings.Contains(output, "\"canonicalSelectors\":[\"discord\"]") {
		t.Fatalf("expected serialized notify contract in codex env, got %q", output)
	}
}

func TestLaunchUsesProjectCodexHomeWhenPersistedScopeIsProject(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	projectCodexHome := filepath.Join(cwd, ".codex")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := os.MkdirAll(filepath.Join(cwd, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nana", "setup-scope.json"), []byte(`{"scope":"project"}`), 0o644); err != nil {
		t.Fatalf("write setup-scope: %v", err)
	}
	if err := os.MkdirAll(projectCodexHome, 0o755); err != nil {
		t.Fatalf("mkdir project codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectCodexHome, "AGENTS.md"), []byte("# Project Scope Home\n"), 0o644); err != nil {
		t.Fatalf("write project CODEX_HOME AGENTS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# Project Instructions\n"), 0o644); err != nil {
		t.Fatalf("write project AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	stub := strings.Join([]string{
		"#!/bin/sh",
		"printf 'codex-home:%s\\n' \"$CODEX_HOME\"",
		"for arg in \"$@\"; do",
		"  case \"$arg\" in",
		"    model_instructions_file=*)",
		"      file=$(printf %s \"$arg\" | sed 's/^model_instructions_file=\"//; s/\"$//')",
		"      printf 'instructions-start\\n'",
		"      cat \"$file\"",
		"      printf 'instructions-end\\n'",
		"      ;;",
		"  esac",
		"done",
	}, "\n")
	if err := os.WriteFile(fakeCodexPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Launch(cwd, []string{"--model", "gpt-5"})
	})
	if err != nil {
		t.Fatalf("Launch(): %v", err)
	}
	if !strings.Contains(output, "codex-home:"+projectCodexHome) {
		t.Fatalf("expected project CODEX_HOME in output, got %q", output)
	}
	if !strings.Contains(output, "# Project Scope Home") {
		t.Fatalf("expected project-scoped CODEX_HOME AGENTS content, got %q", output)
	}
}

func TestRunCodexSessionLoadsSkillRuntimeDocsFromScopedCodexHome(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")
	logPath := filepath.Join(t.TempDir(), "session-instructions.log")
	runtimePath := filepath.Join(scopedCodexHome, "skills", "autopilot", "RUNTIME.md")

	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir scoped runtime dir: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("scoped launch runtime rules\n"), 0o644); err != nil {
		t.Fatalf("write scoped runtime: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopedCodexHome, "AGENTS.md"), []byte("# Scoped Launch Home\n"), 0o644); err != nil {
		t.Fatalf("write scoped AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	script := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"for arg in \"$@\"; do",
		"  case \"$arg\" in",
		"    model_instructions_file=*)",
		"      file=${arg#model_instructions_file=\\\"}",
		"      file=${file%\\\"}",
		"      cat \"$file\" > \"$FAKE_CODEX_INSTRUCTIONS_LOG\"",
		"      ;;",
		"  esac",
		"done",
		"printf 'ok\\n'",
	}, "\n")
	if err := os.WriteFile(fakeCodexPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_INSTRUCTIONS_LOG", logPath)

	output, err := captureStdout(t, func() error {
		return runCodexSession(cwd, []string{"exec", "$autopilot harden launch"}, NotifyTempContract{}, scopedCodexHome)
	})
	if err != nil {
		t.Fatalf("runCodexSession: %v\n%s", err, output)
	}
	if strings.TrimSpace(output) != "ok" {
		t.Fatalf("expected fake codex output, got %q", output)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read session instructions log: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"# Scoped Launch Home",
		"<!-- NANA:SKILL_RUNTIME_DOCS:START -->",
		"scoped launch runtime rules",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected session instructions to contain %q:\n%s", want, text)
		}
	}
}

func TestResolveContextTelemetryScopeCoordinatesRunAndTurnInheritance(t *testing.T) {
	testCases := []struct {
		name          string
		scope         contextTelemetryScope
		envRunID      string
		envTurnID     string
		defaultRunID  string
		wantRunID     string
		wantSameTurn  bool
		wantFreshTurn bool
		wantGenerated bool
	}{
		{
			name:         "inherits ambient run and turn together",
			envRunID:     "run-parent",
			envTurnID:    "turn-parent",
			defaultRunID: "run-generated",
			wantRunID:    "run-parent",
			wantSameTurn: true,
		},
		{
			name:          "inherited ambient run without turn starts fresh turn",
			envRunID:      "run-parent",
			defaultRunID:  "run-generated",
			wantRunID:     "run-parent",
			wantFreshTurn: true,
			wantGenerated: true,
		},
		{
			name:          "generated run ignores stale ambient turn",
			envTurnID:     "turn-stale",
			defaultRunID:  "run-generated",
			wantRunID:     "run-generated",
			wantFreshTurn: true,
			wantGenerated: true,
		},
		{
			name:          "explicit run ignores stale ambient turn",
			scope:         contextTelemetryScope{RunID: "run-explicit"},
			envTurnID:     "turn-stale",
			defaultRunID:  "run-generated",
			wantRunID:     "run-explicit",
			wantFreshTurn: true,
			wantGenerated: true,
		},
		{
			name:         "preserves explicit run and turn pair",
			scope:        contextTelemetryScope{RunID: "run-explicit", TurnID: "turn-explicit"},
			envRunID:     "run-parent",
			envTurnID:    "turn-parent",
			defaultRunID: "run-generated",
			wantRunID:    "run-explicit",
			wantSameTurn: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", tc.envRunID)
			t.Setenv("NANA_WORK_RUN_ID", "")
			t.Setenv("NANA_RUN_ID", "")
			t.Setenv("NANA_SESSION_ID", "")
			t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", tc.envTurnID)
			t.Setenv("NANA_TURN_ID", "")
			t.Setenv("CODEX_TURN_ID", "")

			resolved := resolveContextTelemetryScope(tc.scope, tc.defaultRunID)
			if resolved.RunID != tc.wantRunID {
				t.Fatalf("expected run_id %q, got %+v", tc.wantRunID, resolved)
			}
			if resolved.TurnID == "" {
				t.Fatalf("expected turn_id to be set, got %+v", resolved)
			}
			switch {
			case tc.wantSameTurn:
				wantTurnID := firstNonEmptyString(strings.TrimSpace(tc.scope.TurnID), tc.envTurnID)
				if resolved.TurnID != wantTurnID {
					t.Fatalf("expected turn_id %q, got %+v", wantTurnID, resolved)
				}
			case tc.wantFreshTurn:
				if resolved.TurnID == tc.envTurnID {
					t.Fatalf("expected fresh turn_id instead of inherited stale value %q, got %+v", tc.envTurnID, resolved)
				}
				if !strings.HasPrefix(resolved.TurnID, "turn-") {
					t.Fatalf("expected generated turn_id prefix, got %+v", resolved)
				}
			}
			if resolved.GeneratedTurnID != tc.wantGenerated {
				t.Fatalf("expected GeneratedTurnID=%v, got %+v", tc.wantGenerated, resolved)
			}
		})
	}
}

func TestRunCodexSessionGeneratesSessionTelemetryScopeForBudgetAdvisories(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")
	logPath := filepath.Join(t.TempDir(), "session-instructions.log")
	telemetryLogPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")

	for _, skill := range []string{"plan", "tdd", "build-fix", "code-review"} {
		writeSkillRuntimeDocForTest(t, scopedCodexHome, skill, skill+" runtime rules\n")
	}
	if err := os.WriteFile(filepath.Join(scopedCodexHome, "AGENTS.md"), []byte("# Scoped Launch Home\n"), 0o644); err != nil {
		t.Fatalf("write scoped AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	script := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"printf 'session-env:%s\\n' \"$NANA_SESSION_ID\"",
		"printf 'turn-env:%s\\n' \"$NANA_TURN_ID\"",
		"for arg in \"$@\"; do",
		"  case \"$arg\" in",
		"    model_instructions_file=*)",
		"      file=${arg#model_instructions_file=\\\"}",
		"      file=${file%\\\"}",
		"      cat \"$file\" > \"$FAKE_CODEX_INSTRUCTIONS_LOG\"",
		"      ;;",
		"  esac",
		"done",
		"printf 'ok\\n'",
	}, "\n")
	if err := os.WriteFile(fakeCodexPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_INSTRUCTIONS_LOG", logPath)
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	output, err := captureStdout(t, func() error {
		return runCodexSession(cwd, []string{"exec", "$plan $tdd $build-fix $code-review"}, NotifyTempContract{}, scopedCodexHome)
	})
	if err != nil {
		t.Fatalf("runCodexSession: %v\n%s", err, output)
	}
	if !strings.Contains(output, "ok") {
		t.Fatalf("expected fake codex output, got %q", output)
	}

	rawInstructions, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read session instructions log: %v", err)
	}
	instructions := string(rawInstructions)
	for _, want := range []string{
		"<!-- NANA:SKILL_CONTEXT_BUDGET:START -->",
		`docs="4/3"`,
		"warning: skill runtime docs loaded 4 unique files (budget 3)",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("expected session instructions to contain %q:\n%s", want, instructions)
		}
	}

	events := readTelemetryEventsForTest(t, telemetryLogPath)
	skillLoadCount := 0
	runID := ""
	turnID := ""
	for _, event := range events {
		if telemetryString(event, "event") != "skill_doc_load" {
			continue
		}
		skillLoadCount++
		eventRunID := telemetryString(event, "run_id")
		eventTurnID := telemetryString(event, "turn_id")
		if eventRunID == "" {
			t.Fatalf("expected generated run_id in skill telemetry: %+v", event)
		}
		if eventTurnID == "" {
			t.Fatalf("expected generated turn_id in skill telemetry: %+v", event)
		}
		if runID == "" {
			runID = eventRunID
		} else if runID != eventRunID {
			t.Fatalf("expected a stable generated run_id, got %q and %q", runID, eventRunID)
		}
		if turnID == "" {
			turnID = eventTurnID
		} else if turnID != eventTurnID {
			t.Fatalf("expected a stable generated turn_id, got %q and %q", turnID, eventTurnID)
		}
	}
	if skillLoadCount != 4 {
		t.Fatalf("expected 4 skill_doc_load events, got %d", skillLoadCount)
	}
	if !strings.HasPrefix(runID, "nana-") {
		t.Fatalf("expected generated session-backed run_id, got %q", runID)
	}
	if !strings.HasPrefix(turnID, "turn-") {
		t.Fatalf("expected generated launch turn_id, got %q", turnID)
	}
	scopeLabel := `scope="current turn_id=` + turnID + ` within run_id=` + runID + `"`
	if !strings.Contains(instructions, scopeLabel) {
		t.Fatalf("expected session instructions to contain %q:\n%s", scopeLabel, instructions)
	}
	if !strings.Contains(output, "session-env:"+runID) {
		t.Fatalf("expected codex env to inherit generated session id %q, got %q", runID, output)
	}
	if !strings.Contains(output, "turn-env:"+turnID) {
		t.Fatalf("expected codex env to inherit generated turn id %q, got %q", turnID, output)
	}
}

func TestRunCodexSessionFiltersSkillContextBudgetAdvisoryToCurrentLaunchTurn(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")
	logPath := filepath.Join(t.TempDir(), "session-instructions.log")
	telemetryLogPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")

	writeSkillRuntimeDocForTest(t, scopedCodexHome, "plan", "plan runtime rules\n")
	if err := os.WriteFile(filepath.Join(scopedCodexHome, "AGENTS.md"), []byte("# Scoped Launch Home\n"), 0o644); err != nil {
		t.Fatalf("write scoped AGENTS: %v", err)
	}
	writeTelemetryLog(t, telemetryLogPath, []string{
		`{"timestamp":"2026-04-20T12:00:00Z","run_id":"run-parent","turn_id":"turn-old","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:01Z","run_id":"run-parent","turn_id":"turn-old","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:02Z","run_id":"run-parent","turn_id":"turn-old","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:03Z","run_id":"run-parent","turn_id":"turn-old","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
	})
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	script := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"printf 'session-env:%s\\n' \"$NANA_SESSION_ID\"",
		"printf 'turn-env:%s\\n' \"$NANA_TURN_ID\"",
		"for arg in \"$@\"; do",
		"  case \"$arg\" in",
		"    model_instructions_file=*)",
		"      file=${arg#model_instructions_file=\\\"}",
		"      file=${file%\\\"}",
		"      cat \"$file\" > \"$FAKE_CODEX_INSTRUCTIONS_LOG\"",
		"      ;;",
		"  esac",
		"done",
		"printf 'ok\\n'",
	}, "\n")
	if err := os.WriteFile(fakeCodexPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_INSTRUCTIONS_LOG", logPath)
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-parent")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	output, err := captureStdout(t, func() error {
		return runCodexSession(cwd, []string{"exec", "$plan"}, NotifyTempContract{}, scopedCodexHome)
	})
	if err != nil {
		t.Fatalf("runCodexSession: %v\n%s", err, output)
	}
	if !strings.Contains(output, "ok") {
		t.Fatalf("expected fake codex output, got %q", output)
	}

	rawInstructions, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read session instructions log: %v", err)
	}
	instructions := string(rawInstructions)
	if strings.Contains(instructions, "<!-- NANA:SKILL_CONTEXT_BUDGET:START -->") {
		t.Fatalf("expected current-launch turn to stay under budget despite stale run history:\n%s", instructions)
	}

	events := readTelemetryEventsForTest(t, telemetryLogPath)
	if len(events) == 0 {
		t.Fatal("expected telemetry events")
	}
	lastEvent := events[len(events)-1]
	if telemetryString(lastEvent, "event") != "skill_doc_load" {
		t.Fatalf("expected appended skill_doc_load event, got %+v", lastEvent)
	}
	if telemetryString(lastEvent, "run_id") != "run-parent" {
		t.Fatalf("expected launch telemetry to reuse current run id, got %+v", lastEvent)
	}
	if gotTurnID := telemetryString(lastEvent, "turn_id"); gotTurnID == "" || gotTurnID == "turn-old" {
		t.Fatalf("expected a fresh launch turn_id, got %+v", lastEvent)
	} else {
		if !strings.Contains(output, "session-env:run-parent") {
			t.Fatalf("expected codex env to inherit selected run id, got %q", output)
		}
		if !strings.Contains(output, "turn-env:"+gotTurnID) {
			t.Fatalf("expected codex env to inherit selected turn id %q, got %q", gotTurnID, output)
		}
	}
}

func TestRunCodexSessionSwitchesManagedAccountAfterRateLimit(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	codexHome := filepath.Join(home, ".codex")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")
	logPath := filepath.Join(cwd, "tokens.log")
	failOncePath := filepath.Join(cwd, "fail-once")

	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   healthyUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_TOKEN_LOG", logPath)
	t.Setenv("FAKE_CODEX_FAIL_ONCE_PATH", failOncePath)
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	script := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`token=$(grep -o '"access_token"[[:space:]]*:[[:space:]]*"[^"]*"' "$CODEX_HOME/auth.json" | sed 's/.*"access_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')`,
		`printf '%s\n' "$token" >> "$FAKE_CODEX_TOKEN_LOG"`,
		`if [ ! -f "$FAKE_CODEX_FAIL_ONCE_PATH" ]; then`,
		`  : > "$FAKE_CODEX_FAIL_ONCE_PATH"`,
		`  printf 'rate limited\n' >&2`,
		`  exit 1`,
		`fi`,
		`printf 'ok\n'`,
	}, "\n")
	if err := os.WriteFile(fakeCodexPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return runCodexSession(cwd, []string{"exec", "say hi"}, NotifyTempContract{}, codexHome)
	})
	if err != nil {
		t.Fatalf("runCodexSession: %v\n%s", err, output)
	}
	if !strings.Contains(output, "ok") {
		t.Fatalf("expected successful retry output, got %q", output)
	}
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read token log: %v", err)
	}
	lines := strings.Fields(string(logRaw))
	if len(lines) != 2 || lines[0] != "primary-token" || lines[1] != "secondary-token" {
		t.Fatalf("expected primary then secondary token usage, got %#v", lines)
	}
}

func writeSkillRuntimeDocForTest(t *testing.T, codexHome string, skill string, content string) {
	t.Helper()
	runtimePath := filepath.Join(codexHome, "skills", skill, "RUNTIME.md")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir runtime dir for %s: %v", skill, err)
	}
	if err := os.WriteFile(runtimePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write runtime doc for %s: %v", skill, err)
	}
}

func readTelemetryEventsForTest(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read telemetry log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal telemetry line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestRunCodexSessionWaitsForRetryAfterWhenNoAlternateAccountExists(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	codexHome := filepath.Join(home, ".codex")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")
	logPath := filepath.Join(cwd, "invocations.log")
	failOncePath := filepath.Join(cwd, "fail-once")

	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token": {
				statusCode: 200,
				body:       `{"plan_type":"pro","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_after_seconds":1,"reset_at":0},"secondary_window":{"used_percent":20,"limit_window_seconds":604800,"reset_after_seconds":1,"reset_at":0}},"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":"0"},"spend_control":{"reached":false}}`,
			},
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_RUN_LOG", logPath)
	t.Setenv("FAKE_CODEX_FAIL_ONCE_PATH", failOncePath)
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary": {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
		},
		Active: "primary",
	})

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	script := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`date +%s >> "$FAKE_CODEX_RUN_LOG"`,
		`if [ ! -f "$FAKE_CODEX_FAIL_ONCE_PATH" ]; then`,
		`  : > "$FAKE_CODEX_FAIL_ONCE_PATH"`,
		`  printf 'rate limited\n' >&2`,
		`  exit 1`,
		`fi`,
		`printf 'ok\n'`,
	}, "\n")
	if err := os.WriteFile(fakeCodexPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	started := time.Now()
	output, err := captureStdout(t, func() error {
		return runCodexSession(cwd, []string{"exec", "say hi"}, NotifyTempContract{}, codexHome)
	})
	if err != nil {
		t.Fatalf("runCodexSession: %v\n%s", err, output)
	}
	if time.Since(started) < 900*time.Millisecond {
		t.Fatalf("expected cooldown wait before retry, elapsed=%s", time.Since(started))
	}
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read invocation log: %v", err)
	}
	lines := strings.Fields(string(logRaw))
	if len(lines) != 2 {
		t.Fatalf("expected two invocations, got %#v", lines)
	}
}
