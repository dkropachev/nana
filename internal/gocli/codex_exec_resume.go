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
	Env                []string
}

type codexManagedPromptResult struct {
	Stdout         string
	Stderr         string
	SessionID      string
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

	promptFingerprint := sha256Hex(strings.TrimSpace(options.Prompt))
	ephemeral := hasCodexEphemeralArg(options.CommonArgs)
	checkpoint, _ := readCodexStepCheckpoint(options.CheckpointPath)
	shouldResume := !ephemeral && checkpoint.ResumeEligible && strings.TrimSpace(checkpoint.SessionID) != "" && codexCheckpointMatchesPrompt(checkpoint, promptFingerprint, options.ResumeStrategy)

	if shouldResume {
		result, err := executeManagedCodexPrompt(options, checkpoint.SessionID, options.ContinuationPrompt, true)
		if err != nil && codexResumeSessionMissing(strings.Join([]string{result.Stdout, result.Stderr, err.Error()}, "\n")) {
			result, err = executeManagedCodexPrompt(options, "", options.Prompt, false)
		}
		return finalizeManagedCodexPrompt(options, promptFingerprint, result, err)
	}

	result, err := executeManagedCodexPrompt(options, "", options.Prompt, false)
	return finalizeManagedCodexPrompt(options, promptFingerprint, result, err)
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

	promptArgs, stdinReader := buildCodexPromptInput(prompt, options.PromptTransport)
	args = append(args, promptArgs...)

	nanaSessionID := fmt.Sprintf("%s-%d", sanitizePathToken(options.StepKey), time.Now().UnixNano())
	if strings.TrimSpace(options.CodexHome) != "" {
		sessionInstructionsPath, err := writeSessionModelInstructions(options.InstructionsRoot, nanaSessionID, options.CodexHome)
		if err != nil {
			return codexManagedPromptResult{}, err
		}
		defer removeSessionInstructionsFile(options.InstructionsRoot, nanaSessionID)
		args = injectModelInstructionsArgs(args, sessionInstructionsPath)
	}

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
		result.SessionID = discoverCodexSessionID(options.CodexHome, startedAt)
	}
	result.ResumeEligible = strings.TrimSpace(result.SessionID) != "" && err != nil
	return result, err
}

func finalizeManagedCodexPrompt(options codexManagedPromptOptions, promptFingerprint string, result codexManagedPromptResult, runErr error) (codexManagedPromptResult, error) {
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
	sessionsRoot := filepath.Join(strings.TrimSpace(codexHome), "sessions")
	if strings.TrimSpace(codexHome) == "" {
		return ""
	}
	type candidate struct {
		SessionID string
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
			best = candidate{SessionID: strings.TrimSpace(meta.SessionID), When: when}
		}
		return nil
	})
	return best.SessionID
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
