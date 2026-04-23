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

func TestRunManagedCodexPromptInjectsGeneratedTelemetryScopeIntoCodexEnv(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	logPath := filepath.Join(t.TempDir(), "session-instructions.log")
	telemetryLogPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")

	for _, skill := range []string{"plan", "tdd", "build-fix", "code-review"} {
		writeSkillRuntimeDocForTest(t, scopedCodexHome, skill, skill+" runtime rules\n")
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
	}, "\n"))

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

	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       cwd,
		InstructionsRoot: cwd,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", cwd},
		Prompt:           "$plan $tdd $build-fix $code-review tighten context budgets",
		PromptTransport:  codexPromptTransportArg,
		StepKey:          "managed-budget",
		Env:              append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd),
	})
	if err != nil {
		t.Fatalf("runManagedCodexPrompt: %v", err)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("expected fake codex output, got %q", result.Stdout)
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
		if eventRunID == "" || eventTurnID == "" {
			t.Fatalf("expected generated telemetry scope, got %+v", event)
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
	if !strings.HasPrefix(runID, "managed-budget-") {
		t.Fatalf("expected generated managed run_id, got %q", runID)
	}
	if !strings.HasPrefix(turnID, "turn-") {
		t.Fatalf("expected generated managed turn_id, got %q", turnID)
	}
	if !strings.Contains(result.Stdout, "session-env:"+runID) {
		t.Fatalf("expected codex env to inherit generated run_id %q, got %q", runID, result.Stdout)
	}
	if !strings.Contains(result.Stdout, "turn-env:"+turnID) {
		t.Fatalf("expected codex env to inherit generated turn_id %q, got %q", turnID, result.Stdout)
	}
}

func TestRunManagedCodexPromptReusesCheckpointTelemetryScopeAcrossResumeFallback(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	callLogPath := filepath.Join(t.TempDir(), "codex-calls.log")
	instructionsLogPath := filepath.Join(t.TempDir(), "session-instructions.log")
	telemetryLogPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	checkpointPath := filepath.Join(cwd, "checkpoint.json")
	prompt := "$plan recover telemetry scope"

	writeSkillRuntimeDocForTest(t, scopedCodexHome, "plan", "plan runtime rules\n")
	if err := os.WriteFile(filepath.Join(scopedCodexHome, "AGENTS.md"), []byte("# Scoped Codex Home\n"), 0o644); err != nil {
		t.Fatalf("write scoped AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"mode=fresh",
		`if [ "${1:-}" = "exec" ] && [ "${2:-}" = "resume" ]; then`,
		`  mode=resume`,
		`fi`,
		"printf '%s|%s|%s\\n' \"$NANA_SESSION_ID\" \"$NANA_TURN_ID\" \"$mode\" >> \"$FAKE_CODEX_CALL_LOG\"",
		"for arg in \"$@\"; do",
		"  case \"$arg\" in",
		"    model_instructions_file=*)",
		"      file=${arg#model_instructions_file=\\\"}",
		"      file=${file%\\\"}",
		"      cat \"$file\" > \"$FAKE_CODEX_INSTRUCTIONS_LOG\"",
		"      ;;",
		"  esac",
		"done",
		`if [ "$mode" = "resume" ]; then`,
		`  printf 'session not found\n' >&2`,
		`  exit 1`,
		`fi`,
		"printf 'ok\\n'",
	}, "\n"))

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CALL_LOG", callLogPath)
	t.Setenv("FAKE_CODEX_INSTRUCTIONS_LOG", instructionsLogPath)
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	const expectedRunID = "managed-resume-run"
	const expectedTurnID = "turn-managed-resume"
	if err := writeCodexStepCheckpoint(checkpointPath, codexStepCheckpoint{
		Version:           1,
		StepKey:           "managed-resume",
		SessionID:         "session-missing",
		PromptFingerprint: sha256Hex(strings.TrimSpace(prompt)),
		ResumeStrategy:    string(codexResumeSamePrompt),
		ResumeEligible:    true,
		TelemetryRunID:    expectedRunID,
		TelemetryTurnID:   expectedTurnID,
	}); err != nil {
		t.Fatalf("writeCodexStepCheckpoint: %v", err)
	}

	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       cwd,
		InstructionsRoot: cwd,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", cwd},
		Prompt:           prompt,
		PromptTransport:  codexPromptTransportArg,
		CheckpointPath:   checkpointPath,
		StepKey:          "managed-resume",
		Env:              append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd),
	})
	if err != nil {
		t.Fatalf("runManagedCodexPrompt: %v", err)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("expected fake codex output, got %q", result.Stdout)
	}

	rawCalls, err := os.ReadFile(callLogPath)
	if err != nil {
		t.Fatalf("read codex call log: %v", err)
	}
	callLines := strings.Split(strings.TrimSpace(string(rawCalls)), "\n")
	if len(callLines) != 2 {
		t.Fatalf("expected resume and fallback launch, got %d calls:\n%s", len(callLines), string(rawCalls))
	}
	for index, line := range callLines {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			t.Fatalf("unexpected codex call log line %q", line)
		}
		if parts[0] != expectedRunID || parts[1] != expectedTurnID {
			t.Fatalf("expected stable checkpoint telemetry scope on call %d, got %q", index, line)
		}
	}
	if !strings.HasSuffix(callLines[0], "|resume") {
		t.Fatalf("expected first codex call to resume the missing session, got %q", callLines[0])
	}
	if !strings.HasSuffix(callLines[1], "|fresh") {
		t.Fatalf("expected second codex call to fall back to a fresh launch, got %q", callLines[1])
	}

	events := readTelemetryEventsForTest(t, telemetryLogPath)
	skillLoadCount := 0
	for _, event := range events {
		if telemetryString(event, "event") != "skill_doc_load" {
			continue
		}
		skillLoadCount++
		if telemetryString(event, "run_id") != expectedRunID || telemetryString(event, "turn_id") != expectedTurnID {
			t.Fatalf("expected resumed step telemetry to preserve checkpoint scope, got %+v", event)
		}
	}
	if skillLoadCount != 1 {
		t.Fatalf("expected one fresh-launch skill_doc_load event, got %d", skillLoadCount)
	}

	checkpoint, err := readCodexStepCheckpoint(checkpointPath)
	if err != nil {
		t.Fatalf("readCodexStepCheckpoint: %v", err)
	}
	if checkpoint.TelemetryRunID != expectedRunID || checkpoint.TelemetryTurnID != expectedTurnID {
		t.Fatalf("expected checkpoint telemetry scope to persist after fallback, got %+v", checkpoint)
	}
}

func TestRunManagedCodexPromptMintsFreshTelemetryScopeAfterCompletedCheckpoint(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	callLogPath := filepath.Join(t.TempDir(), "codex-calls.log")
	instructionsLogPath := filepath.Join(t.TempDir(), "session-instructions.log")
	telemetryLogPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	checkpointPath := filepath.Join(cwd, "checkpoint.json")
	prompt := "$plan refresh telemetry scope"

	writeSkillRuntimeDocForTest(t, scopedCodexHome, "plan", "plan runtime rules\n")
	if err := os.WriteFile(filepath.Join(scopedCodexHome, "AGENTS.md"), []byte("# Scoped Codex Home\n"), 0o644); err != nil {
		t.Fatalf("write scoped AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"printf '%s|%s|fresh\\n' \"$NANA_SESSION_ID\" \"$NANA_TURN_ID\" >> \"$FAKE_CODEX_CALL_LOG\"",
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
	t.Setenv("FAKE_CODEX_CALL_LOG", callLogPath)
	t.Setenv("FAKE_CODEX_INSTRUCTIONS_LOG", instructionsLogPath)
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	const staleRunID = "managed-complete-run"
	const staleTurnID = "turn-managed-complete"
	if err := writeCodexStepCheckpoint(checkpointPath, codexStepCheckpoint{
		Version:           1,
		StepKey:           "managed-complete",
		Status:            "completed",
		SessionID:         "session-complete",
		PromptFingerprint: sha256Hex(strings.TrimSpace(prompt)),
		ResumeStrategy:    string(codexResumeSamePrompt),
		ResumeEligible:    false,
		TelemetryRunID:    staleRunID,
		TelemetryTurnID:   staleTurnID,
		CompletedAt:       ISOTimeNow(),
	}); err != nil {
		t.Fatalf("writeCodexStepCheckpoint: %v", err)
	}

	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       cwd,
		InstructionsRoot: cwd,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", cwd},
		Prompt:           prompt,
		PromptTransport:  codexPromptTransportArg,
		CheckpointPath:   checkpointPath,
		StepKey:          "managed-complete",
		Env:              append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd),
	})
	if err != nil {
		t.Fatalf("runManagedCodexPrompt: %v", err)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("expected fake codex output, got %q", result.Stdout)
	}

	rawCalls, err := os.ReadFile(callLogPath)
	if err != nil {
		t.Fatalf("read codex call log: %v", err)
	}
	callLines := strings.Split(strings.TrimSpace(string(rawCalls)), "\n")
	if len(callLines) != 1 {
		t.Fatalf("expected one fresh launch, got %d calls:\n%s", len(callLines), string(rawCalls))
	}
	parts := strings.SplitN(callLines[0], "|", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected codex call log line %q", callLines[0])
	}
	if parts[0] == staleRunID || parts[1] == staleTurnID {
		t.Fatalf("expected fresh telemetry scope instead of completed checkpoint scope, got %q", callLines[0])
	}
	if !strings.HasPrefix(parts[0], "managed-complete-") {
		t.Fatalf("expected fresh managed run_id, got %q", parts[0])
	}
	if !strings.HasPrefix(parts[1], "turn-") {
		t.Fatalf("expected fresh managed turn_id, got %q", parts[1])
	}
	if parts[2] != "fresh" {
		t.Fatalf("expected a fresh launch, got %q", callLines[0])
	}

	events := readTelemetryEventsForTest(t, telemetryLogPath)
	skillLoadCount := 0
	for _, event := range events {
		if telemetryString(event, "event") != "skill_doc_load" {
			continue
		}
		skillLoadCount++
		if telemetryString(event, "run_id") != parts[0] || telemetryString(event, "turn_id") != parts[1] {
			t.Fatalf("expected fresh telemetry scope on new launch, got %+v", event)
		}
	}
	if skillLoadCount != 1 {
		t.Fatalf("expected one fresh-launch skill_doc_load event, got %d", skillLoadCount)
	}

	checkpoint, err := readCodexStepCheckpoint(checkpointPath)
	if err != nil {
		t.Fatalf("readCodexStepCheckpoint: %v", err)
	}
	if checkpoint.TelemetryRunID != parts[0] || checkpoint.TelemetryTurnID != parts[1] {
		t.Fatalf("expected checkpoint telemetry scope to refresh after completed launch, got %+v", checkpoint)
	}
	if checkpoint.Status != "completed" || checkpoint.ResumeEligible {
		t.Fatalf("expected completed checkpoint to remain non-resumable, got %+v", checkpoint)
	}
}

func TestRunManagedCodexPromptReusesCheckpointTelemetryScopeWithoutResumeEligibility(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	callLogPath := filepath.Join(t.TempDir(), "codex-calls.log")
	instructionsLogPath := filepath.Join(t.TempDir(), "session-instructions.log")
	telemetryLogPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	checkpointPath := filepath.Join(cwd, "checkpoint.json")
	prompt := "$plan retry telemetry scope"

	writeSkillRuntimeDocForTest(t, scopedCodexHome, "plan", "plan runtime rules\n")
	if err := os.WriteFile(filepath.Join(scopedCodexHome, "AGENTS.md"), []byte("# Scoped Codex Home\n"), 0o644); err != nil {
		t.Fatalf("write scoped AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"printf '%s|%s|fresh\\n' \"$NANA_SESSION_ID\" \"$NANA_TURN_ID\" >> \"$FAKE_CODEX_CALL_LOG\"",
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
	t.Setenv("FAKE_CODEX_CALL_LOG", callLogPath)
	t.Setenv("FAKE_CODEX_INSTRUCTIONS_LOG", instructionsLogPath)
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	const expectedRunID = "managed-retry-run"
	const expectedTurnID = "turn-managed-retry"
	if err := writeCodexStepCheckpoint(checkpointPath, codexStepCheckpoint{
		Version:           1,
		StepKey:           "managed-retry",
		Status:            "failed",
		PromptFingerprint: sha256Hex(strings.TrimSpace(prompt)),
		ResumeStrategy:    string(codexResumeSamePrompt),
		ResumeEligible:    false,
		TelemetryRunID:    expectedRunID,
		TelemetryTurnID:   expectedTurnID,
		LastError:         "previous attempt failed before session capture",
		UpdatedAt:         ISOTimeNow(),
	}); err != nil {
		t.Fatalf("writeCodexStepCheckpoint: %v", err)
	}

	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       cwd,
		InstructionsRoot: cwd,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", cwd},
		Prompt:           prompt,
		PromptTransport:  codexPromptTransportArg,
		CheckpointPath:   checkpointPath,
		StepKey:          "managed-retry",
		Env:              append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd),
	})
	if err != nil {
		t.Fatalf("runManagedCodexPrompt: %v", err)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("expected fake codex output, got %q", result.Stdout)
	}

	rawCalls, err := os.ReadFile(callLogPath)
	if err != nil {
		t.Fatalf("read codex call log: %v", err)
	}
	callLines := strings.Split(strings.TrimSpace(string(rawCalls)), "\n")
	if len(callLines) != 1 {
		t.Fatalf("expected one fresh launch, got %d calls:\n%s", len(callLines), string(rawCalls))
	}
	parts := strings.SplitN(callLines[0], "|", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected codex call log line %q", callLines[0])
	}
	if parts[0] != expectedRunID || parts[1] != expectedTurnID || parts[2] != "fresh" {
		t.Fatalf("expected checkpoint telemetry scope on fresh retry, got %q", callLines[0])
	}

	events := readTelemetryEventsForTest(t, telemetryLogPath)
	skillLoadCount := 0
	for _, event := range events {
		if telemetryString(event, "event") != "skill_doc_load" {
			continue
		}
		skillLoadCount++
		if telemetryString(event, "run_id") != expectedRunID || telemetryString(event, "turn_id") != expectedTurnID {
			t.Fatalf("expected fresh retry telemetry to preserve checkpoint scope, got %+v", event)
		}
	}
	if skillLoadCount != 1 {
		t.Fatalf("expected one fresh-launch skill_doc_load event, got %d", skillLoadCount)
	}

	checkpoint, err := readCodexStepCheckpoint(checkpointPath)
	if err != nil {
		t.Fatalf("readCodexStepCheckpoint: %v", err)
	}
	if checkpoint.TelemetryRunID != expectedRunID || checkpoint.TelemetryTurnID != expectedTurnID {
		t.Fatalf("expected checkpoint telemetry scope to persist across fresh retry, got %+v", checkpoint)
	}
	if checkpoint.Status != "completed" || checkpoint.ResumeEligible {
		t.Fatalf("expected fresh retry to complete and remain non-resumable, got %+v", checkpoint)
	}
}

func TestRunManagedCodexPromptEphemeralLaunchMintsFreshTelemetryScopeInsteadOfCheckpointScope(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	scopedCodexHome := filepath.Join(t.TempDir(), "scoped-codex-home")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	callLogPath := filepath.Join(t.TempDir(), "codex-calls.log")
	instructionsLogPath := filepath.Join(t.TempDir(), "session-instructions.log")
	telemetryLogPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	checkpointPath := filepath.Join(cwd, "checkpoint.json")
	prompt := "$plan ephemeral telemetry scope"

	writeSkillRuntimeDocForTest(t, scopedCodexHome, "plan", "plan runtime rules\n")
	if err := os.WriteFile(filepath.Join(scopedCodexHome, "AGENTS.md"), []byte("# Scoped Codex Home\n"), 0o644); err != nil {
		t.Fatalf("write scoped AGENTS: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"mode=fresh",
		`if [ "${1:-}" = "exec" ] && [ "${2:-}" = "resume" ]; then`,
		`  mode=resume`,
		`fi`,
		"printf '%s|%s|%s\\n' \"$NANA_SESSION_ID\" \"$NANA_TURN_ID\" \"$mode\" >> \"$FAKE_CODEX_CALL_LOG\"",
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
	t.Setenv("FAKE_CODEX_CALL_LOG", callLogPath)
	t.Setenv("FAKE_CODEX_INSTRUCTIONS_LOG", instructionsLogPath)
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	const staleRunID = "managed-ephemeral-run"
	const staleTurnID = "turn-managed-ephemeral"
	if err := writeCodexStepCheckpoint(checkpointPath, codexStepCheckpoint{
		Version:           1,
		StepKey:           "managed-ephemeral",
		Status:            "failed",
		SessionID:         "session-previous",
		PromptFingerprint: sha256Hex(strings.TrimSpace(prompt)),
		ResumeStrategy:    string(codexResumeSamePrompt),
		ResumeEligible:    true,
		TelemetryRunID:    staleRunID,
		TelemetryTurnID:   staleTurnID,
		LastError:         "previous attempt should not leak telemetry into ephemeral retry",
		UpdatedAt:         ISOTimeNow(),
	}); err != nil {
		t.Fatalf("writeCodexStepCheckpoint: %v", err)
	}

	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       cwd,
		InstructionsRoot: cwd,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", cwd},
		CommonArgs:       []string{"--ephemeral"},
		Prompt:           prompt,
		PromptTransport:  codexPromptTransportArg,
		CheckpointPath:   checkpointPath,
		StepKey:          "managed-ephemeral",
		Env:              append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd),
	})
	if err != nil {
		t.Fatalf("runManagedCodexPrompt: %v", err)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("expected fake codex output, got %q", result.Stdout)
	}

	rawCalls, err := os.ReadFile(callLogPath)
	if err != nil {
		t.Fatalf("read codex call log: %v", err)
	}
	callLines := strings.Split(strings.TrimSpace(string(rawCalls)), "\n")
	if len(callLines) != 1 {
		t.Fatalf("expected one ephemeral fresh launch, got %d calls:\n%s", len(callLines), string(rawCalls))
	}
	parts := strings.SplitN(callLines[0], "|", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected codex call log line %q", callLines[0])
	}
	if parts[2] != "fresh" {
		t.Fatalf("expected ephemeral launch to skip resume, got %q", callLines[0])
	}
	if parts[0] == staleRunID || parts[1] == staleTurnID {
		t.Fatalf("expected fresh telemetry scope instead of stale checkpoint scope, got %q", callLines[0])
	}
	if !strings.HasPrefix(parts[0], "managed-ephemeral-") {
		t.Fatalf("expected fresh managed run_id, got %q", parts[0])
	}
	if !strings.HasPrefix(parts[1], "turn-") {
		t.Fatalf("expected fresh managed turn_id, got %q", parts[1])
	}

	events := readTelemetryEventsForTest(t, telemetryLogPath)
	skillLoadCount := 0
	for _, event := range events {
		if telemetryString(event, "event") != "skill_doc_load" {
			continue
		}
		skillLoadCount++
		if telemetryString(event, "run_id") != parts[0] || telemetryString(event, "turn_id") != parts[1] {
			t.Fatalf("expected ephemeral launch to emit fresh telemetry scope, got %+v", event)
		}
	}
	if skillLoadCount != 1 {
		t.Fatalf("expected one fresh-launch skill_doc_load event, got %d", skillLoadCount)
	}

	checkpoint, err := readCodexStepCheckpoint(checkpointPath)
	if err != nil {
		t.Fatalf("readCodexStepCheckpoint: %v", err)
	}
	if checkpoint.TelemetryRunID != parts[0] || checkpoint.TelemetryTurnID != parts[1] {
		t.Fatalf("expected checkpoint telemetry scope to refresh after ephemeral launch, got %+v", checkpoint)
	}
	if checkpoint.SessionID != "" {
		t.Fatalf("expected ephemeral launch to avoid session capture, got %+v", checkpoint)
	}
	if checkpoint.Status != "completed" || checkpoint.ResumeEligible {
		t.Fatalf("expected ephemeral launch to complete and remain non-resumable, got %+v", checkpoint)
	}
}
