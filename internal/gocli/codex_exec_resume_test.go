package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunManagedCodexPromptReturnsPauseErrorOnRateLimit(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token": nearLimitUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)

	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary": {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
		},
		Active: "primary",
	})

	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"printf 'rate limited\\n' >&2",
		"exit 1",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	cwd := t.TempDir()
	checkpointPath := filepath.Join(cwd, "checkpoint.json")
	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       cwd,
		InstructionsRoot: cwd,
		CodexHome:        codexHome,
		FreshArgsPrefix:  []string{"exec", "-C", cwd},
		Prompt:           "say hello",
		PromptTransport:  codexPromptTransportArg,
		CheckpointPath:   checkpointPath,
		StepKey:          "test-step",
		Env:              append(buildCodexEnv(NotifyTempContract{}, codexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd),
		RateLimitPolicy:  codexRateLimitPolicyReturnPause,
	})
	if err == nil {
		t.Fatal("expected rate-limit pause error")
	}
	pauseErr, ok := isCodexRateLimitPauseError(err)
	if !ok {
		t.Fatalf("expected pause error, got %T %v", err, err)
	}
	if strings.TrimSpace(pauseErr.Info.RetryAfter) == "" {
		t.Fatalf("expected retry_after in pause info, got %#v", pauseErr.Info)
	}
	if strings.TrimSpace(result.Stderr) == "" {
		t.Fatalf("expected stderr to be captured")
	}

	checkpoint, readErr := readCodexStepCheckpoint(checkpointPath)
	if readErr != nil {
		t.Fatalf("readCodexStepCheckpoint: %v", readErr)
	}
	if checkpoint.Status != "paused" {
		t.Fatalf("expected paused checkpoint, got %#v", checkpoint)
	}
	if strings.TrimSpace(checkpoint.PauseUntil) == "" {
		t.Fatalf("expected pause_until, got %#v", checkpoint)
	}
}

func TestCodexOutputLooksRateLimitedMatchesUsageLimitWording(t *testing.T) {
	message := "ERROR: You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again later."
	if !codexOutputLooksRateLimited(message) {
		t.Fatalf("expected usage-limit wording to be treated as rate-limited")
	}
	if got := codexRateLimitReason("", message, nil); !strings.Contains(strings.ToLower(got), "usage limit") {
		t.Fatalf("expected rate-limit reason to preserve usage-limit wording, got %q", got)
	}
}

func TestRunManagedCodexPromptInjectsModelInstructionsBeforePromptPlaceholder(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	fakeBin := filepath.Join(home, "bin")
	logPath := filepath.Join(home, "codex-args.log")

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"printf '%s\\n' \"$@\" > \"$FAKE_CODEX_ARGS_LOG\"",
		"cat >/dev/null",
		"printf 'ok\\n'",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_ARGS_LOG", logPath)

	cwd := t.TempDir()
	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       cwd,
		InstructionsRoot: cwd,
		CodexHome:        codexHome,
		FreshArgsPrefix:  []string{"exec", "-C", cwd},
		CommonArgs:       []string{"--dangerously-bypass-approvals-and-sandbox"},
		Prompt:           "say hello",
		PromptTransport:  codexPromptTransportStdin,
		StepKey:          "test-step",
		Env:              append(buildCodexEnv(NotifyTempContract{}, codexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd),
	})
	if err != nil {
		t.Fatalf("runManagedCodexPrompt: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "ok" {
		t.Fatalf("expected fake codex output, got %q", result.Stdout)
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(content)), "\n")
	configIndex := -1
	placeholderIndex := -1
	for index, arg := range args {
		switch {
		case arg == ConfigFlag || arg == LongConfigFlag:
			configIndex = index
		case arg == "-":
			placeholderIndex = index
		}
	}
	if configIndex < 0 {
		t.Fatalf("expected model instructions config flag in args, got %v", args)
	}
	if placeholderIndex < 0 {
		t.Fatalf("expected stdin prompt placeholder in args, got %v", args)
	}
	if configIndex >= placeholderIndex {
		t.Fatalf("expected config args before prompt placeholder, got %v", args)
	}
	if configIndex+1 >= len(args) || !strings.HasPrefix(args[configIndex+1], ModelInstructionsFileKey+"=") {
		t.Fatalf("expected model instructions config payload after config flag, got %v", args)
	}
}

func TestRunManagedCodexPromptLoadsSkillRuntimeDocsFromScopedCodexHome(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	logPath := filepath.Join(t.TempDir(), "session-instructions.log")
	runtimePath := filepath.Join(scopedCodexHome, "skills", "autopilot", "RUNTIME.md")

	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("mkdir scoped runtime dir: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("scoped runtime contract\n"), 0o644); err != nil {
		t.Fatalf("write scoped runtime: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopedCodexHome, "AGENTS.md"), []byte("# Scoped Codex Home\n"), 0o644); err != nil {
		t.Fatalf("write scoped AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
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
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_INSTRUCTIONS_LOG", logPath)

	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       cwd,
		InstructionsRoot: cwd,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", cwd},
		Prompt:           "$autopilot harden this",
		PromptTransport:  codexPromptTransportArg,
		StepKey:          "scoped-runtime-doc",
		Env:              append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd),
	})
	if err != nil {
		t.Fatalf("runManagedCodexPrompt: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "ok" {
		t.Fatalf("expected fake codex output, got %q", result.Stdout)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read session instructions log: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"# Scoped Codex Home",
		"<!-- NANA:SKILL_RUNTIME_DOCS:START -->",
		`<skill name="autopilot" doc="runtime"`,
		"scoped runtime contract",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected session instructions to contain %q:\n%s", want, text)
		}
	}
}
