package gocli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type codexPromptTransport string

const (
	codexPromptTransportArg         codexPromptTransport = "arg"
	codexPromptTransportArgWithDash codexPromptTransport = "arg_with_dash"
	codexPromptTransportStdin       codexPromptTransport = "stdin"
	codexSessionCaptureGracePeriod                       = 2 * time.Second
)

type codexResumeStrategy string

const (
	codexResumeSamePrompt   codexResumeStrategy = "same_prompt"
	codexResumeConversation codexResumeStrategy = "conversation"
)

type codexStepCheckpoint struct {
	Version           int    `json:"version"`
	StepKey           string `json:"step_key,omitempty"`
	Status            string `json:"status,omitempty"`
	PauseReason       string `json:"pause_reason,omitempty"`
	PauseUntil        string `json:"pause_until,omitempty"`
	PausedAt          string `json:"paused_at,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	SessionPath       string `json:"session_path,omitempty"`
	PromptFingerprint string `json:"prompt_fingerprint,omitempty"`
	ResumeStrategy    string `json:"resume_strategy,omitempty"`
	ResumeEligible    bool   `json:"resume_eligible,omitempty"`
	TelemetryRunID    string `json:"telemetry_run_id,omitempty"`
	TelemetryTurnID   string `json:"telemetry_turn_id,omitempty"`
	LastLaunchMode    string `json:"last_launch_mode,omitempty"`
	OwnerPID          int    `json:"owner_pid,omitempty"`
	StartedAt         string `json:"started_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
	CompletedAt       string `json:"completed_at,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

type codexManagedPromptOptions struct {
	CommandDir         string
	InstructionsRoot   string
	CodexHome          string
	FreshArgsPrefix    []string
	CommonArgs         []string
	Prompt             string
	PromptTransport    codexPromptTransport
	ContinuationPrompt string
	CheckpointPath     string
	StepKey            string
	ResumeStrategy     codexResumeStrategy
	UsageRunID         string
	UsageRepoSlug      string
	UsageBackend       string
	UsageSandboxPath   string
	TelemetryScope     contextTelemetryScope
	Env                []string
	RateLimitPolicy    codexRateLimitPolicy
	RecoverySpec       codexManagedPromptRecoverySpec
	OnPause            func(codexRateLimitPauseInfo)
	OnResume           func(codexRateLimitPauseInfo)
}

type codexManagedPromptResult struct {
	Stdout         string
	Stderr         string
	SessionID      string
	SessionPath    string
	Resumed        bool
	ResumeEligible bool
}

func runManagedCodexPrompt(options codexManagedPromptOptions) (codexManagedPromptResult, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return codexManagedPromptResult{}, fmt.Errorf("codex is required: %w", err)
	}

	if strings.TrimSpace(options.StepKey) == "" {
		options.StepKey = "step"
	}
	if options.ResumeStrategy == "" {
		options.ResumeStrategy = codexResumeSamePrompt
	}
	if strings.TrimSpace(options.ContinuationPrompt) == "" {
		options.ContinuationPrompt = buildCodexContinuationPrompt(options.StepKey)
	}
	options.RateLimitPolicy = codexRateLimitPolicyDefault(options.RateLimitPolicy)
	if recoverySpec, ok := normalizeManagedPromptRecoverySpec(options.RecoverySpec, options.CheckpointPath, options.CommandDir); ok {
		options.RecoverySpec = recoverySpec
	} else {
		options.RecoverySpec = codexManagedPromptRecoverySpec{}
	}
	promptFingerprint := sha256Hex(strings.TrimSpace(options.Prompt))
	ephemeral := hasCodexEphemeralArg(options.CommonArgs)
	checkpoint, _ := readCodexStepCheckpoint(options.CheckpointPath)
	options.TelemetryScope = resolveManagedPromptTelemetryScope(
		options.TelemetryScope,
		checkpoint,
		options.StepKey,
		managedPromptShouldReuseCheckpointTelemetryScope(ephemeral, checkpoint, options.StepKey, promptFingerprint, options.ResumeStrategy),
	)
	authManager, err := prepareManagedAuthManager(options.CommandDir, options.CodexHome)
	if err != nil {
		return codexManagedPromptResult{}, err
	}
	authSwitched := false

	for {
		checkpoint, _ := readCodexStepCheckpoint(options.CheckpointPath)
		shouldResume := managedPromptShouldResume(ephemeral, checkpoint, promptFingerprint, options.ResumeStrategy)

		result := codexManagedPromptResult{}
		runErr := error(nil)
		if shouldResume {
			result, runErr = executeManagedCodexPrompt(options, checkpoint.SessionID, options.ContinuationPrompt, true)
			if runErr != nil && (codexResumeSessionMissing(strings.Join([]string{result.Stdout, result.Stderr, runErr.Error()}, "\n")) || (authSwitched && codexResumeNeedsFreshLaunch(strings.Join([]string{result.Stdout, result.Stderr, runErr.Error()}, "\n")))) {
				result, runErr = executeManagedCodexPrompt(options, "", options.Prompt, false)
			}
		} else {
			result, runErr = executeManagedCodexPrompt(options, "", options.Prompt, false)
		}
		authSwitched = false

		if runErr == nil || !codexOutputLooksRateLimited(strings.Join([]string{result.Stdout, result.Stderr, runErr.Error()}, "\n")) || authManager == nil {
			return finalizeManagedCodexPrompt(options, promptFingerprint, result, runErr)
		}

		decision, decisionErr := authManager.handleExecutionRateLimit(codexRateLimitReason(result.Stdout, result.Stderr, runErr))
		if decisionErr != nil {
			return finalizeManagedCodexPrompt(options, promptFingerprint, result, fmt.Errorf("%w (and failed to resolve managed account rate limit: %v)", runErr, decisionErr))
		}
		pauseInfo := codexRateLimitPauseInfo{
			Reason:     defaultString(strings.TrimSpace(decision.Reason), codexRateLimitReason(result.Stdout, result.Stderr, runErr)),
			RetryAfter: strings.TrimSpace(decision.RetryAfter),
			SwitchedTo: strings.TrimSpace(decision.SwitchedTo),
		}
		if strings.TrimSpace(decision.SwitchedTo) != "" {
			authSwitched = true
			continue
		}
		if err := writePausedCodexCheckpoint(options, promptFingerprint, result, pauseInfo); err != nil {
			return result, err
		}
		if options.RateLimitPolicy == codexRateLimitPolicyReturnPause {
			return result, &codexRateLimitPauseError{Info: pauseInfo}
		}
		if options.OnPause != nil {
			options.OnPause(pauseInfo)
		}
		waitUntil, ok := codexPauseRetryAt(pauseInfo)
		if !ok {
			waitUntil = time.Now().UTC().Add(time.Minute)
		}
		wait := time.Until(waitUntil)
		if wait > 0 {
			time.Sleep(wait)
		}
		if options.OnResume != nil {
			options.OnResume(pauseInfo)
		}
	}
}

func resolveManagedPromptTelemetryScope(scope contextTelemetryScope, checkpoint codexStepCheckpoint, stepKey string, inheritCheckpoint bool) contextTelemetryScope {
	resolved := scope
	if inheritCheckpoint {
		if strings.TrimSpace(resolved.RunID) == "" {
			resolved.RunID = strings.TrimSpace(checkpoint.TelemetryRunID)
		}
		if strings.TrimSpace(resolved.TurnID) == "" {
			resolved.TurnID = strings.TrimSpace(checkpoint.TelemetryTurnID)
		}
	}
	defaultRunID := fmt.Sprintf("%s-%d", sanitizePathToken(stepKey), time.Now().UnixNano())
	return resolveContextTelemetryScope(resolved, defaultRunID)
}

func managedPromptShouldReuseCheckpointTelemetryScope(ephemeral bool, checkpoint codexStepCheckpoint, stepKey string, promptFingerprint string, strategy codexResumeStrategy) bool {
	if ephemeral {
		return false
	}
	if strings.TrimSpace(checkpoint.TelemetryRunID) == "" && strings.TrimSpace(checkpoint.TelemetryTurnID) == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(checkpoint.Status), "completed") {
		return false
	}
	checkpointStepKey := strings.TrimSpace(checkpoint.StepKey)
	if checkpointStepKey != "" && strings.TrimSpace(stepKey) != "" && checkpointStepKey != strings.TrimSpace(stepKey) {
		return false
	}
	switch strategy {
	case codexResumeConversation:
		return true
	default:
		return strings.TrimSpace(checkpoint.PromptFingerprint) != "" && strings.TrimSpace(checkpoint.PromptFingerprint) == strings.TrimSpace(promptFingerprint)
	}
}

func managedPromptShouldResume(ephemeral bool, checkpoint codexStepCheckpoint, promptFingerprint string, strategy codexResumeStrategy) bool {
	return !ephemeral &&
		checkpoint.ResumeEligible &&
		strings.TrimSpace(checkpoint.SessionID) != "" &&
		codexCheckpointMatchesPrompt(checkpoint, promptFingerprint, strategy)
}

func executeManagedCodexPrompt(options codexManagedPromptOptions, sessionID string, prompt string, resumed bool) (codexManagedPromptResult, error) {
	startedAt := time.Now().UTC()
	promptFingerprint := sha256Hex(strings.TrimSpace(prompt))
	args := []string{}
	if resumed {
		args = append(args, "exec", "resume", strings.TrimSpace(sessionID))
	} else {
		args = append(args, options.FreshArgsPrefix...)
	}
	args = append(args, options.CommonArgs...)

	nanaSessionID := fmt.Sprintf("%s-%d", sanitizePathToken(options.StepKey), time.Now().UnixNano())
	telemetryScope := resolveContextTelemetryScope(options.TelemetryScope, nanaSessionID)
	if strings.TrimSpace(options.CodexHome) != "" {
		instructionsRoot := options.InstructionsRoot
		if strings.TrimSpace(instructionsRoot) == "" {
			instructionsRoot = options.CommandDir
		}
		activatedDocs := []loadedSkillRuntimeDoc(nil)
		if !resumed {
			var loadErr error
			activatedDocs, loadErr = loadActivatedSkillRuntimeDocsWithTelemetryScope(defaultString(strings.TrimSpace(options.CommandDir), instructionsRoot), prompt, telemetryScope, options.CodexHome)
			if loadErr != nil {
				return codexManagedPromptResult{}, loadErr
			}
		}
		sessionInstructionsPath, err := writeSessionModelInstructionsWithTelemetryScope(instructionsRoot, nanaSessionID, options.CodexHome, telemetryScope, activatedDocs...)
		if err != nil {
			return codexManagedPromptResult{}, err
		}
		defer removeSessionInstructionsFile(instructionsRoot, nanaSessionID)
		args = injectModelInstructionsArgs(args, sessionInstructionsPath)
	}

	promptArgs, stdinReader := buildCodexPromptInput(prompt, options.PromptTransport)
	args = append(args, promptArgs...)

	cmd := exec.Command("codex", args...)
	cmd.Dir = options.CommandDir
	cmd.Env = withContextTelemetryScopeEnv(options.Env, telemetryScope)
	if stdinReader != nil {
		cmd.Stdin = stdinReader
	}
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	result := codexManagedPromptResult{
		Resumed: resumed,
	}
	if resumed {
		result.SessionID = strings.TrimSpace(sessionID)
		result.SessionPath = findCodexSessionPathByID(options.CodexHome, result.SessionID)
	}
	if err := cmd.Start(); err != nil {
		return result, err
	}
	if err := syncManagedPromptInFlightState(options, telemetryScope, promptFingerprint, &result, startedAt, resumed, !resumed && !hasCodexEphemeralArg(options.CommonArgs)); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return result, err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	heartbeat := time.NewTicker(managedPromptRecoveryHeartbeatInterval)
	defer heartbeat.Stop()
	var sessionPoll *time.Ticker
	var sessionPollC <-chan time.Time
	if !resumed && !hasCodexEphemeralArg(options.CommonArgs) {
		sessionPoll = time.NewTicker(managedPromptRecoverySessionPoll)
		defer sessionPoll.Stop()
		sessionPollC = sessionPoll.C
	}
	for {
		select {
		case err := <-done:
			if syncErr := syncManagedPromptInFlightState(options, telemetryScope, promptFingerprint, &result, startedAt, resumed, !resumed && !hasCodexEphemeralArg(options.CommonArgs)); syncErr != nil {
				return result, syncErr
			}
			result.Stdout = stdout.String()
			result.Stderr = stderr.String()
			result.ResumeEligible = strings.TrimSpace(result.SessionID) != "" && err != nil
			return result, err
		case <-heartbeat.C:
			if err := syncManagedPromptInFlightState(options, telemetryScope, promptFingerprint, &result, startedAt, resumed, false); err != nil {
				_ = cmd.Process.Kill()
				<-done
				return result, err
			}
		case <-sessionPollC:
			if err := syncManagedPromptInFlightState(options, telemetryScope, promptFingerprint, &result, startedAt, resumed, true); err != nil {
				_ = cmd.Process.Kill()
				<-done
				return result, err
			}
		}
	}
}

func finalizeManagedCodexPrompt(options codexManagedPromptOptions, promptFingerprint string, result codexManagedPromptResult, runErr error) (codexManagedPromptResult, error) {
	if strings.TrimSpace(result.SessionID) != "" {
		_ = recordManagedPromptUsage(options, result)
	}
	if strings.TrimSpace(options.CheckpointPath) == "" {
		return result, runErr
	}
	existing, _ := readCodexStepCheckpoint(options.CheckpointPath)
	startedAt := strings.TrimSpace(existing.StartedAt)
	if startedAt == "" {
		startedAt = ISOTimeNow()
	}
	checkpoint := codexStepCheckpoint{
		Version:           1,
		StepKey:           options.StepKey,
		SessionID:         strings.TrimSpace(result.SessionID),
		SessionPath:       strings.TrimSpace(result.SessionPath),
		PromptFingerprint: promptFingerprint,
		ResumeStrategy:    string(options.ResumeStrategy),
		ResumeEligible:    result.ResumeEligible,
		TelemetryRunID:    strings.TrimSpace(options.TelemetryScope.RunID),
		TelemetryTurnID:   strings.TrimSpace(options.TelemetryScope.TurnID),
		LastLaunchMode:    "fresh",
		OwnerPID:          os.Getpid(),
		StartedAt:         startedAt,
		UpdatedAt:         ISOTimeNow(),
	}
	if result.Resumed {
		checkpoint.LastLaunchMode = "resume"
	}
	if runErr != nil {
		checkpoint.Status = "failed"
		checkpoint.LastError = runErr.Error()
	} else {
		checkpoint.Status = "completed"
		checkpoint.CompletedAt = ISOTimeNow()
		checkpoint.ResumeEligible = false
		checkpoint.LastError = ""
	}
	if writeErr := writeCodexStepCheckpoint(options.CheckpointPath, checkpoint); writeErr != nil {
		return result, writeErr
	}
	if deleteErr := deleteManagedPromptRecovery(options.CheckpointPath); deleteErr != nil {
		return result, deleteErr
	}
	return result, runErr
}

func writePausedCodexCheckpoint(options codexManagedPromptOptions, promptFingerprint string, result codexManagedPromptResult, pauseInfo codexRateLimitPauseInfo) error {
	if strings.TrimSpace(options.CheckpointPath) == "" {
		return nil
	}
	now := ISOTimeNow()
	checkpoint := codexStepCheckpoint{
		Version:           1,
		StepKey:           options.StepKey,
		Status:            "paused",
		PauseReason:       strings.TrimSpace(pauseInfo.Reason),
		PauseUntil:        strings.TrimSpace(pauseInfo.RetryAfter),
		PausedAt:          now,
		SessionID:         strings.TrimSpace(result.SessionID),
		SessionPath:       strings.TrimSpace(result.SessionPath),
		PromptFingerprint: promptFingerprint,
		ResumeStrategy:    string(options.ResumeStrategy),
		ResumeEligible:    result.ResumeEligible,
		TelemetryRunID:    strings.TrimSpace(options.TelemetryScope.RunID),
		TelemetryTurnID:   strings.TrimSpace(options.TelemetryScope.TurnID),
		LastLaunchMode:    "fresh",
		OwnerPID:          os.Getpid(),
		StartedAt:         now,
		UpdatedAt:         now,
		LastError:         codexPauseInfoMessage(pauseInfo),
	}
	if result.Resumed {
		checkpoint.LastLaunchMode = "resume"
	}
	if err := writeCodexStepCheckpoint(options.CheckpointPath, checkpoint); err != nil {
		return err
	}
	if recoverySpec, ok := normalizeManagedPromptRecoverySpec(options.RecoverySpec, options.CheckpointPath, options.CommandDir); ok {
		return upsertManagedPromptRecovery(managedPromptRecoveryRecord{
			CheckpointPath:    strings.TrimSpace(options.CheckpointPath),
			OwnerKind:         recoverySpec.OwnerKind,
			OwnerID:           recoverySpec.OwnerID,
			OwnerPayload:      cloneManagedPromptRecoveryPayload(recoverySpec.OwnerPayload),
			StepKey:           strings.TrimSpace(options.StepKey),
			Status:            managedPromptRecoveryStatusPaused,
			CWD:               recoverySpec.CWD,
			ResumeArgv:        append([]string{}, recoverySpec.ResumeArgv...),
			ArtifactRoot:      recoverySpec.ArtifactRoot,
			LogPath:           recoverySpec.LogPath,
			PromptFingerprint: promptFingerprint,
			SessionID:         strings.TrimSpace(result.SessionID),
			SessionPath:       strings.TrimSpace(result.SessionPath),
			LastLaunchMode:    checkpoint.LastLaunchMode,
			PauseReason:       checkpoint.PauseReason,
			PauseUntil:        checkpoint.PauseUntil,
			LastError:         checkpoint.LastError,
			OwnerPID:          os.Getpid(),
			HeartbeatAt:       now,
			StartedAt:         checkpoint.StartedAt,
			UpdatedAt:         now,
		})
	}
	return nil
}

func syncManagedPromptInFlightState(options codexManagedPromptOptions, telemetryScope contextTelemetryScope, promptFingerprint string, result *codexManagedPromptResult, startedAt time.Time, resumed bool, discoverSession bool) error {
	if result == nil {
		return nil
	}
	if discoverSession && strings.TrimSpace(result.SessionID) == "" {
		result.SessionID, result.SessionPath = discoverCodexSession(options.CodexHome, startedAt)
	}
	if result.SessionPath == "" && strings.TrimSpace(result.SessionID) != "" {
		result.SessionPath = findCodexSessionPathByID(options.CodexHome, result.SessionID)
	}
	if strings.TrimSpace(options.CheckpointPath) == "" {
		return nil
	}
	now := ISOTimeNow()
	checkpoint := codexStepCheckpoint{
		Version:           1,
		StepKey:           options.StepKey,
		Status:            managedPromptRecoveryStatusRunning,
		SessionID:         strings.TrimSpace(result.SessionID),
		SessionPath:       strings.TrimSpace(result.SessionPath),
		PromptFingerprint: promptFingerprint,
		ResumeStrategy:    string(options.ResumeStrategy),
		ResumeEligible:    strings.TrimSpace(result.SessionID) != "",
		TelemetryRunID:    strings.TrimSpace(telemetryScope.RunID),
		TelemetryTurnID:   strings.TrimSpace(telemetryScope.TurnID),
		LastLaunchMode:    map[bool]string{true: "resume", false: "fresh"}[resumed],
		OwnerPID:          os.Getpid(),
		StartedAt:         startedAt.Format(time.RFC3339Nano),
		UpdatedAt:         now,
	}
	if err := writeCodexStepCheckpoint(options.CheckpointPath, checkpoint); err != nil {
		return err
	}
	if recoverySpec, ok := normalizeManagedPromptRecoverySpec(options.RecoverySpec, options.CheckpointPath, options.CommandDir); ok {
		return upsertManagedPromptRecovery(managedPromptRecoveryRecord{
			CheckpointPath:    strings.TrimSpace(options.CheckpointPath),
			OwnerKind:         recoverySpec.OwnerKind,
			OwnerID:           recoverySpec.OwnerID,
			OwnerPayload:      cloneManagedPromptRecoveryPayload(recoverySpec.OwnerPayload),
			StepKey:           strings.TrimSpace(options.StepKey),
			Status:            managedPromptRecoveryStatusRunning,
			CWD:               recoverySpec.CWD,
			ResumeArgv:        append([]string{}, recoverySpec.ResumeArgv...),
			ArtifactRoot:      recoverySpec.ArtifactRoot,
			LogPath:           recoverySpec.LogPath,
			PromptFingerprint: promptFingerprint,
			SessionID:         strings.TrimSpace(result.SessionID),
			SessionPath:       strings.TrimSpace(result.SessionPath),
			LastLaunchMode:    checkpoint.LastLaunchMode,
			OwnerPID:          os.Getpid(),
			HeartbeatAt:       now,
			StartedAt:         checkpoint.StartedAt,
			UpdatedAt:         now,
		})
	}
	return nil
}

func buildCodexPromptInput(prompt string, transport codexPromptTransport) ([]string, io.Reader) {
	switch transport {
	case codexPromptTransportStdin:
		return []string{"-"}, strings.NewReader(prompt)
	case codexPromptTransportArgWithDash:
		return []string{"--", prompt}, nil
	default:
		return []string{prompt}, nil
	}
}

func buildCodexContinuationPrompt(stepKey string) string {
	lines := []string{
		"Continue the unfinished Nana step in this existing Codex session.",
		fmt.Sprintf("Step: %s", strings.TrimSpace(stepKey)),
		"Preserve the original task, output contract, and repo state expectations.",
	}
	return strings.Join(lines, "\n")
}

func codexCheckpointMatchesPrompt(checkpoint codexStepCheckpoint, promptFingerprint string, strategy codexResumeStrategy) bool {
	if strings.TrimSpace(checkpoint.SessionID) == "" {
		return false
	}
	switch strategy {
	case codexResumeConversation:
		return true
	default:
		return strings.TrimSpace(checkpoint.PromptFingerprint) != "" && strings.TrimSpace(checkpoint.PromptFingerprint) == strings.TrimSpace(promptFingerprint)
	}
}

func hasCodexEphemeralArg(args []string) bool {
	for _, arg := range args {
		if strings.TrimSpace(arg) == "--ephemeral" {
			return true
		}
	}
	return false
}

func codexResumeSessionMissing(output string) bool {
	normalized := strings.ToLower(strings.TrimSpace(output))
	if normalized == "" {
		return false
	}
	needles := []string{
		"session not found",
		"conversation not found",
		"no matching session",
		"could not find session",
		"unknown session",
		"session does not exist",
	}
	for _, needle := range needles {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func discoverCodexSessionID(codexHome string, notBefore time.Time) string {
	sessionID, _ := discoverCodexSession(codexHome, notBefore)
	return sessionID
}

func discoverCodexSession(codexHome string, notBefore time.Time) (string, string) {
	return discoverCodexSessionMatching(codexHome, notBefore.Add(-codexSessionCaptureGracePeriod), "")
}

func discoverLatestCodexSession(codexHome string, cwd string) (string, string) {
	return discoverCodexSessionMatching(codexHome, time.Time{}, cwd)
}

func discoverCodexSessionMatching(codexHome string, cutoff time.Time, cwd string) (string, string) {
	sessionsRoot := filepath.Join(strings.TrimSpace(codexHome), "sessions")
	if strings.TrimSpace(codexHome) == "" {
		return "", ""
	}
	type candidate struct {
		SessionID string
		Path      string
		When      time.Time
	}
	best := candidate{}
	wantCWD := strings.TrimSpace(cwd)
	_ = filepath.Walk(sessionsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		meta, ok := readCodexRolloutSessionMeta(path)
		if !ok || strings.TrimSpace(meta.SessionID) == "" {
			return nil
		}
		if wantCWD != "" && strings.TrimSpace(meta.CWD) != wantCWD {
			return nil
		}
		when := info.ModTime()
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(meta.Timestamp)); err == nil && parsed.After(when) {
			when = parsed
		}
		if !cutoff.IsZero() && when.Before(cutoff) {
			return nil
		}
		if strings.TrimSpace(best.SessionID) == "" || when.After(best.When) {
			best = candidate{SessionID: strings.TrimSpace(meta.SessionID), Path: path, When: when}
		}
		return nil
	})
	return best.SessionID, best.Path
}

func findCodexSessionPathByID(codexHome string, sessionID string) string {
	sessionsRoot := filepath.Join(strings.TrimSpace(codexHome), "sessions")
	if strings.TrimSpace(codexHome) == "" || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	found := ""
	_ = filepath.Walk(sessionsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		meta, ok := readCodexRolloutSessionMeta(path)
		if !ok || !strings.EqualFold(strings.TrimSpace(meta.SessionID), strings.TrimSpace(sessionID)) {
			return nil
		}
		found = path
		return io.EOF
	})
	if found != "" {
		return found
	}
	return ""
}

func readCodexRolloutSessionMeta(path string) (struct {
	SessionID string
	Timestamp string
	CWD       string
}, bool) {
	fallback := struct {
		SessionID string
		Timestamp string
		CWD       string
	}{}
	file, err := os.Open(path)
	if err != nil {
		return fallback, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return fallback, false
	}
	line := scanner.Text()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		return fallback, false
	}
	meta := extractSessionMeta(parsed, path)
	return meta, strings.TrimSpace(meta.SessionID) != ""
}

func readCodexStepCheckpoint(path string) (codexStepCheckpoint, error) {
	if strings.TrimSpace(path) == "" {
		return codexStepCheckpoint{}, os.ErrNotExist
	}
	var checkpoint codexStepCheckpoint
	if err := readGithubJSON(path, &checkpoint); err != nil {
		return codexStepCheckpoint{}, err
	}
	return checkpoint, nil
}

func writeCodexStepCheckpoint(path string, checkpoint codexStepCheckpoint) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return writeGithubJSON(path, checkpoint)
}
