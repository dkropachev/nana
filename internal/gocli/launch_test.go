package gocli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
