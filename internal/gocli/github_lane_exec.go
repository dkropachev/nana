package gocli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dkropachev/nana/internal/gocliassets"
)

type githubLaneRuntimeState struct {
	Version               int    `json:"version"`
	LaneID                string `json:"lane_id"`
	Alias                 string `json:"alias"`
	Role                  string `json:"role"`
	Activation            string `json:"activation,omitempty"`
	Phase                 string `json:"phase,omitempty"`
	Blocking              bool   `json:"blocking,omitempty"`
	Status                string `json:"status"`
	Pid                   int    `json:"pid,omitempty"`
	StartedAt             string `json:"started_at,omitempty"`
	UpdatedAt             string `json:"updated_at"`
	CompletedAt           string `json:"completed_at,omitempty"`
	InstructionsPath      string `json:"instructions_path"`
	ResultPath            string `json:"result_path"`
	StdoutPath            string `json:"stdout_path"`
	StderrPath            string `json:"stderr_path"`
	SessionCheckpointPath string `json:"session_checkpoint_path,omitempty"`
	SessionID             string `json:"session_id,omitempty"`
	ResumeEligible        bool   `json:"resume_eligible,omitempty"`
	LastError             string `json:"last_error,omitempty"`
}

func executeGithubLane(runID string, useLast bool, laneAlias string, task string, codexArgs []string) error {
	manifestPath, repoRoot, err := resolveGithubRunManifestPath(runID, useLast)
	if err != nil {
		return err
	}
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		return err
	}
	lane := findGithubPipelineLane(manifest.ConsiderationPipeline, laneAlias)
	if lane == nil {
		return fmt.Errorf("Lane %s is not present in run %s.", laneAlias, manifest.RunID)
	}
	lockMode := repoAccessLockWrite
	if lane.Mode == "review" {
		lockMode = repoAccessLockRead
	}
	sandboxLock, err := acquireRepoAccessLock(manifest.SandboxRepoPath, lockMode, repoAccessLockOwner{
		Backend: "github-lane",
		RunID:   manifest.RunID,
		Purpose: "lane-" + sanitizeLanePathToken(lane.Alias),
		Label:   "github-lane-" + sanitizeLanePathToken(lane.Alias),
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = sandboxLock.Release()
	}()
	runDir := filepath.Dir(manifestPath)
	runtimeDir := filepath.Join(runDir, "lane-runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	instructionsPath := filepath.Join(runtimeDir, fmt.Sprintf("%s-instructions.md", sanitizeLanePathToken(laneAlias)))
	resultPath := filepath.Join(runtimeDir, fmt.Sprintf("%s-result.md", sanitizeLanePathToken(laneAlias)))
	stdoutPath := filepath.Join(runtimeDir, fmt.Sprintf("%s-stdout.log", sanitizeLanePathToken(laneAlias)))
	stderrPath := filepath.Join(runtimeDir, fmt.Sprintf("%s-stderr.log", sanitizeLanePathToken(laneAlias)))
	checkpointPath := filepath.Join(runtimeDir, fmt.Sprintf("%s-checkpoint.json", sanitizeLanePathToken(laneAlias)))
	statePath := filepath.Join(runtimeDir, fmt.Sprintf("%s.json", sanitizeLanePathToken(laneAlias)))
	eventsPath := filepath.Join(runtimeDir, "events.jsonl")

	instructions, err := buildGithubLaneExecutionInstructions(manifest, *lane, task)
	if err != nil {
		return err
	}
	if err := os.WriteFile(instructionsPath, []byte(instructions), 0o644); err != nil {
		return err
	}

	laneCodexHome, err := ensureGithubLaneCodexHome(manifest.SandboxPath, laneAlias)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	state := githubLaneRuntimeState{
		Version:               1,
		LaneID:                fmt.Sprintf("%s:%s", manifest.RunID, lane.Alias),
		Alias:                 lane.Alias,
		Role:                  lane.Role,
		Activation:            lane.Activation,
		Phase:                 lane.Phase,
		Blocking:              lane.Blocking,
		Status:                "running",
		UpdatedAt:             now,
		StartedAt:             now,
		InstructionsPath:      instructionsPath,
		ResultPath:            resultPath,
		StdoutPath:            stdoutPath,
		StderrPath:            stderrPath,
		SessionCheckpointPath: checkpointPath,
	}

	prompt := strings.TrimSpace(task)
	if prompt == "" {
		prompt = fmt.Sprintf("Execute the %s lane for %s %s #%d", lane.Alias, manifest.RepoSlug, manifest.TargetKind, manifest.TargetNumber)
	}
	finalPrompt := instructions + "\n\nTask:\n" + prompt
	normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(codexArgs)
	finalPrompt = prefixCodexFastPrompt(finalPrompt, fastMode)
	transport := promptTransportForSize(finalPrompt, structuredPromptStdinThreshold)
	if err := writeGithubJSON(statePath, state); err != nil {
		return err
	}
	if err := appendGithubLaneEvent(eventsPath, map[string]any{
		"type":    "lane_started",
		"lane_id": state.LaneID,
		"alias":   state.Alias,
		"role":    state.Role,
		"at":      state.StartedAt,
	}); err != nil {
		return err
	}

	result, runErr := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       manifest.SandboxPath,
		InstructionsRoot: manifest.SandboxPath,
		CodexHome:        laneCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", manifest.SandboxRepoPath},
		CommonArgs:       normalizedCodexArgs,
		Prompt:           finalPrompt,
		PromptTransport:  transport,
		CheckpointPath:   checkpointPath,
		StepKey:          fmt.Sprintf("github-lane-%s", lane.Alias),
		ResumeStrategy:   codexResumeConversation,
		Env: append(buildGithubCodexEnv(NotifyTempContract{}, laneCodexHome, manifest.APIBaseURL),
			"NANA_PROJECT_AGENTS_ROOT="+manifest.SandboxRepoPath,
		),
		OnPause: func(info codexRateLimitPauseInfo) {
			state.Status = "paused"
			state.LastError = codexPauseInfoMessage(info)
			state.UpdatedAt = ISOTimeNow()
			_ = writeGithubJSON(statePath, state)
		},
		OnResume: func(info codexRateLimitPauseInfo) {
			state.Status = "running"
			state.LastError = ""
			state.UpdatedAt = ISOTimeNow()
			_ = writeGithubJSON(statePath, state)
		},
	})
	combined := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(result.Stdout), strings.TrimSpace(result.Stderr)}, "\n\n"))
	if err := os.WriteFile(resultPath, []byte(combined), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(stdoutPath, []byte(result.Stdout), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(stderrPath, []byte(result.Stderr), 0o644); err != nil {
		return err
	}

	state.Status = "completed"
	state.SessionID = strings.TrimSpace(result.SessionID)
	state.ResumeEligible = result.ResumeEligible
	if runErr != nil {
		if pauseErr, ok := isCodexRateLimitPauseError(runErr); ok {
			state.Status = "paused"
			state.LastError = codexPauseInfoMessage(pauseErr.Info)
		} else {
			state.Status = "failed"
			state.LastError = strings.TrimSpace(result.Stderr)
			if state.LastError == "" {
				state.LastError = runErr.Error()
			}
		}
	}
	state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	state.UpdatedAt = state.CompletedAt
	if err := writeGithubJSON(statePath, state); err != nil {
		return err
	}
	eventType := "lane_completed"
	if state.Status == "failed" {
		eventType = "lane_failed"
	}
	if err := appendGithubLaneEvent(eventsPath, map[string]any{
		"type":        eventType,
		"lane_id":     state.LaneID,
		"alias":       state.Alias,
		"role":        state.Role,
		"at":          state.CompletedAt,
		"result_path": resultPath,
	}); err != nil {
		return err
	}

	fmt.Fprintf(currentWorkStdout(), "[github] Lane %s %s via isolated CODEX_HOME %s.\n", lane.Alias, state.Status, laneCodexHome)
	fmt.Fprintf(currentWorkStdout(), "[github] Lane result: %s\n", resultPath)
	if combined != "" {
		fmt.Fprintln(currentWorkStdout(), combined)
	}
	if runErr != nil {
		return runErr
	}
	_ = repoRoot
	return nil
}

func findGithubPipelineLane(lanes []githubPipelineLane, alias string) *githubPipelineLane {
	for index := range lanes {
		if lanes[index].Alias == alias {
			return &lanes[index]
		}
	}
	return nil
}

func buildGithubLaneExecutionInstructions(manifest githubWorkManifest, lane githubPipelineLane, task string) (string, error) {
	promptBody := ""
	for _, artifact := range manifest.LanePromptArtifacts {
		if artifact.Alias == lane.Alias && artifact.Role == lane.Role && strings.TrimSpace(artifact.PromptPath) != "" {
			content, err := os.ReadFile(artifact.PromptPath)
			if err == nil {
				promptBody = string(content)
				break
			}
		}
	}
	if promptBody == "" && len(lane.PromptRoles) > 0 {
		var parts []string
		for _, role := range lane.PromptRoles {
			content, err := readGithubEmbeddedPromptSurface(role)
			if err == nil && strings.TrimSpace(content) != "" {
				parts = append(parts, strings.TrimSpace(content))
			}
		}
		promptBody = compactPromptHeadValue(strings.Join(parts, "\n\n"), 0, githubRolePromptCharLimit)
	} else if strings.TrimSpace(promptBody) != "" {
		promptBody = compactPromptHeadValue(promptBody, 0, githubRolePromptCharLimit)
	}
	lines := []string{
		"# NANA Work Lane",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo: %s", manifest.RepoSlug),
		fmt.Sprintf("Sandbox path: %s", manifest.SandboxPath),
		fmt.Sprintf("Repo checkout path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Lane alias: %s", lane.Alias),
		fmt.Sprintf("Lane role: %s", lane.Role),
		fmt.Sprintf("Lane phase: %s", lane.Phase),
		fmt.Sprintf("Lane mode: %s", lane.Mode),
		fmt.Sprintf("Lane owner: %s", lane.Owner),
		fmt.Sprintf("Lane purpose: %s", lane.Purpose),
		"",
	}
	lines = append(lines, buildGithubRuntimeContextLines(manifest)...)
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	lines = append(lines,
		"Operating contract:",
		"- This lane runs in a separate Codex process with its own CODEX_HOME and MCP profile.",
		"- Stay inside this lane concern and do not broaden scope.",
	)
	if lane.Mode == "review" {
		lines = append(lines, "- Review only. Do not edit files. Return concrete findings with file references.")
	} else {
		lines = append(lines, "- Implement or remediate only the work that belongs to this lane, then run the worker-done verification gate.")
		lines = append(lines, "- Treat follow-up work inside this lane as complete only when no obvious in-scope fixups remain for the touched surface after verification.")
	}
	if strings.TrimSpace(task) != "" {
		lines = append(lines, fmt.Sprintf("- Caller task: %s", task))
	}
	if strings.TrimSpace(promptBody) != "" {
		lines = append(lines, "", strings.TrimSpace(promptBody))
	}
	return capPromptChars(strings.Join(lines, "\n")+"\n", githubInstructionCharLimit), nil
}

func readGithubPromptSurface(role string) (string, error) {
	prompts, err := gocliassets.Prompts()
	if err != nil {
		return "", err
	}
	content := prompts[promptSurfaceFileName(role)]
	if content == "" {
		return "", fmt.Errorf("prompt not found for role %s", role)
	}
	return content, nil
}

func readGithubEmbeddedPromptSurface(role string) (string, error) {
	prompts, err := gocliassets.Prompts()
	if err != nil {
		return "", err
	}
	if content := prompts[embeddedPromptSurfaceFileName(role)]; content != "" {
		return content, nil
	}
	return readGithubPromptSurface(role)
}

func promptSurfaceFileName(role string) string {
	return role + ".md"
}

func embeddedPromptSurfaceFileName(role string) string {
	return role + "-embedded.md"
}

func ensureGithubLaneCodexHome(sandboxPath string, laneAlias string) (string, error) {
	sourceCodexHome := ResolveCodexHomeForLaunch(sandboxPath)
	laneCodexHome := filepath.Join(sandboxPath, ".nana", "work", "codex-home", sanitizeLanePathToken(laneAlias))
	return ensureScopedCodexHome(sourceCodexHome, laneCodexHome)
}

func ensureScopedCodexHome(sourceCodexHome string, scopedCodexHome string) (string, error) {
	if strings.TrimSpace(sourceCodexHome) == "" {
		sourceCodexHome = ResolveCodexHomeForLaunch(".")
	}
	if err := os.MkdirAll(scopedCodexHome, 0o755); err != nil {
		return "", err
	}
	for _, entry := range []string{"auth.json", authRegistryFileName, authRuntimeStateFileName, authAccountsDirName, "config.toml", "prompts", "skills", "agents"} {
		source := filepath.Join(sourceCodexHome, entry)
		target := filepath.Join(scopedCodexHome, entry)
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if err := os.Symlink(source, target); err != nil && !os.IsExist(err) {
			if info, statErr := os.Stat(source); statErr == nil && !info.IsDir() {
				content, readErr := os.ReadFile(source)
				if readErr == nil {
					if writeErr := os.WriteFile(target, content, 0o644); writeErr == nil {
						continue
					}
				}
			}
		}
	}
	return scopedCodexHome, nil
}

func sanitizeLanePathToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "/", "-")
	value = strings.ReplaceAll(value, "\\", "-")
	value = strings.ReplaceAll(value, " ", "-")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	value = strings.Trim(value, "-")
	if value == "" {
		return "lane"
	}
	return value
}

func appendGithubLaneEvent(path string, event map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = f.Write(append(encoded, '\n'))
	return err
}

// Keep the imported context package alive for future lane timeout work.
var _ = context.Background
