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
	PromptFingerprint string `json:"prompt_fingerprint,omitempty"`
	ResumeStrategy    string `json:"resume_strategy,omitempty"`
	ResumeEligible    bool   `json:"resume_eligible,omitempty"`
	LastLaunchMode    string `json:"last_launch_mode,omitempty"`
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
	UsageBackend       string
	UsageSandboxPath   string
	Env                []string
	RateLimitPolicy    codexRateLimitPolicy
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

	promptFingerprint := sha256Hex(strings.TrimSpace(options.Prompt))
	authManager, err := prepareManagedAuthManager(options.CommandDir, options.CodexHome)
	if err != nil {
		return codexManagedPromptResult{}, err
	}
	authSwitched := false

	for {
		ephemeral := hasCodexEphemeralArg(options.CommonArgs)
		checkpoint, _ := readCodexStepCheckpoint(options.CheckpointPath)
		shouldResume := !ephemeral && checkpoint.ResumeEligible && strings.TrimSpace(checkpoint.SessionID) != "" && codexCheckpointMatchesPrompt(checkpoint, promptFingerprint, options.ResumeStrategy)

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

func executeManagedCodexPrompt(options codexManagedPromptOptions, sessionID string, prompt string, resumed bool) (codexManagedPromptResult, error) {
	startedAt := time.Now().UTC()
	args := []string{}
	if resumed {
		args = append(args, "exec", "resume", strings.TrimSpace(sessionID))
	} else {
		args = append(args, options.FreshArgsPrefix...)
	}
	args = append(args, options.CommonArgs...)

	nanaSessionID := fmt.Sprintf("%s-%d", sanitizePathToken(options.StepKey), time.Now().UnixNano())
	if strings.TrimSpace(options.CodexHome) != "" {
		instructionsRoot := options.InstructionsRoot
		if strings.TrimSpace(instructionsRoot) == "" {
			instructionsRoot = options.CommandDir
		}
		activatedDocs := []loadedSkillRuntimeDoc(nil)
		if !resumed {
			var loadErr error
			activatedDocs, loadErr = loadActivatedSkillRuntimeDocs(defaultString(strings.TrimSpace(options.CommandDir), instructionsRoot), prompt, options.CodexHome)
			if loadErr != nil {
				return codexManagedPromptResult{}, loadErr
			}
		}
		sessionInstructionsPath, err := writeSessionModelInstructions(instructionsRoot, nanaSessionID, options.CodexHome, activatedDocs...)
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
	cmd.Env = append([]string{}, options.Env...)
	if stdinReader != nil {
		cmd.Stdin = stdinReader
	}
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	result := codexManagedPromptResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Resumed: resumed,
	}
	if resumed {
		result.SessionID = strings.TrimSpace(sessionID)
	} else if !hasCodexEphemeralArg(options.CommonArgs) {
		result.SessionID, result.SessionPath = discoverCodexSession(options.CodexHome, startedAt)
	}
	if result.SessionPath == "" && strings.TrimSpace(result.SessionID) != "" {
		result.SessionPath = findCodexSessionPathByID(options.CodexHome, result.SessionID)
	}
	result.ResumeEligible = strings.TrimSpace(result.SessionID) != "" && err != nil
	return result, err
}

func finalizeManagedCodexPrompt(options codexManagedPromptOptions, promptFingerprint string, result codexManagedPromptResult, runErr error) (codexManagedPromptResult, error) {
	if strings.TrimSpace(result.SessionID) != "" {
		_ = recordManagedPromptUsage(options, result)
	}
	if strings.TrimSpace(options.CheckpointPath) == "" {
		return result, runErr
	}
	checkpoint := codexStepCheckpoint{
		Version:           1,
		StepKey:           options.StepKey,
		SessionID:         strings.TrimSpace(result.SessionID),
		PromptFingerprint: promptFingerprint,
		ResumeStrategy:    string(options.ResumeStrategy),
		ResumeEligible:    result.ResumeEligible,
		LastLaunchMode:    "fresh",
		StartedAt:         ISOTimeNow(),
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
		PromptFingerprint: promptFingerprint,
		ResumeStrategy:    string(options.ResumeStrategy),
		ResumeEligible:    result.ResumeEligible,
		LastLaunchMode:    "fresh",
		StartedAt:         now,
		UpdatedAt:         now,
		LastError:         codexPauseInfoMessage(pauseInfo),
	}
	if result.Resumed {
		checkpoint.LastLaunchMode = "resume"
	}
	return writeCodexStepCheckpoint(options.CheckpointPath, checkpoint)
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
	cutoff := notBefore.Add(-codexSessionCaptureGracePeriod)
	_ = filepath.Walk(sessionsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		meta, ok := readCodexRolloutSessionMeta(path)
		if !ok || strings.TrimSpace(meta.SessionID) == "" {
			return nil
		}
		when := info.ModTime()
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(meta.Timestamp)); err == nil && parsed.After(when) {
			when = parsed
		}
		if when.Before(cutoff) {
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
