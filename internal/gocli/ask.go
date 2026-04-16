package gocli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const AskUsage = `Usage: nana ask <claude|gemini> <question or task>
   or: nana ask <claude|gemini> -p "<prompt>"
   or: nana ask claude --print "<prompt>"
   or: nana ask gemini --prompt "<prompt>"
   or: nana ask <claude|gemini> --agent-prompt <role> "<prompt>"
   or: nana ask <claude|gemini> --agent-prompt=<role> --prompt "<prompt>"`

var askProviders = map[string]bool{
	"claude": true,
	"gemini": true,
}

var safeRolePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type ParsedAskArgs struct {
	Provider        string
	Prompt          string
	AgentPromptRole string
}

func Ask(repoRoot string, cwd string, args []string) error {
	_ = repoRoot
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(os.Stdout, AskUsage)
		return nil
	}

	parsed, err := ParseAskArgs(args)
	if err != nil {
		return err
	}

	finalPrompt := parsed.Prompt
	if parsed.AgentPromptRole != "" {
		promptContent, err := resolveAgentPromptContent(cwd, parsed.AgentPromptRole)
		if err != nil {
			return err
		}
		finalPrompt = promptContent + "\n\n" + parsed.Prompt
	}

	return runAskProvider(cwd, parsed.Provider, parsed.Prompt, finalPrompt)
}

func runAskProvider(cwd string, provider string, originalPrompt string, finalPrompt string) error {
	binary := provider
	if !askProviders[provider] {
		return fmt.Errorf("invalid provider %q. expected one of: claude, gemini.\n%s", provider, AskUsage)
	}

	probe := exec.Command(binary, "--version")
	probe.Stdout = nil
	probe.Stderr = nil
	if err := probe.Run(); err != nil {
		if _, ok := err.(*exec.Error); ok {
			return fmt.Errorf("[ask-%s] Missing required local CLI binary: %s\n[ask-%s] Install/configure %s CLI, then verify with: %s --version", binary, binary, binary, binary, binary)
		}
	}

	cmd := exec.Command(binary, "-p", finalPrompt)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return err
		}
	}

	artifactPath, artifactErr := writeAskArtifact(cwd, provider, originalPrompt, finalPrompt, string(output), exitCode)
	if artifactErr != nil {
		return artifactErr
	}
	fmt.Fprintln(os.Stdout, artifactPath)
	if exitCode != 0 {
		if len(output) > 0 {
			fmt.Fprint(os.Stderr, string(output))
		}
		return &exec.ExitError{}
	}
	return nil
}

func ParseAskArgs(args []string) (ParsedAskArgs, error) {
	if len(args) == 0 {
		return ParsedAskArgs{}, fmt.Errorf("missing provider\n%s", AskUsage)
	}
	provider := strings.ToLower(strings.TrimSpace(args[0]))
	if !askProviders[provider] {
		return ParsedAskArgs{}, fmt.Errorf("invalid provider %q. expected one of: claude, gemini.\n%s", args[0], AskUsage)
	}

	rest := args[1:]
	if len(rest) == 0 {
		return ParsedAskArgs{}, fmt.Errorf("missing prompt text\n%s", AskUsage)
	}

	var parsed ParsedAskArgs
	parsed.Provider = provider
	var promptParts []string

	for i := 0; i < len(rest); i++ {
		token := rest[i]
		switch {
		case token == "--agent-prompt":
			if i+1 >= len(rest) || strings.HasPrefix(rest[i+1], "-") {
				return ParsedAskArgs{}, fmt.Errorf("missing role after --agent-prompt\n%s", AskUsage)
			}
			parsed.AgentPromptRole = strings.TrimSpace(rest[i+1])
			i++
		case strings.HasPrefix(token, "--agent-prompt="):
			parsed.AgentPromptRole = strings.TrimSpace(strings.TrimPrefix(token, "--agent-prompt="))
		case token == "-p" || token == "--print" || token == "--prompt":
			promptParts = append(promptParts, rest[i+1:]...)
			i = len(rest)
		case strings.HasPrefix(token, "-p="):
			promptParts = append(promptParts, strings.TrimSpace(strings.TrimPrefix(token, "-p=")))
		case strings.HasPrefix(token, "--print="):
			promptParts = append(promptParts, strings.TrimSpace(strings.TrimPrefix(token, "--print=")))
		case strings.HasPrefix(token, "--prompt="):
			promptParts = append(promptParts, strings.TrimSpace(strings.TrimPrefix(token, "--prompt=")))
		default:
			promptParts = append(promptParts, token)
		}
	}

	parsed.Prompt = strings.TrimSpace(strings.Join(promptParts, " "))
	if parsed.Prompt == "" {
		return ParsedAskArgs{}, fmt.Errorf("missing prompt text\n%s", AskUsage)
	}
	return parsed, nil
}

func resolveAskPromptsDir(cwd string) string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, "prompts")
	}
	scopePath := filepath.Join(cwd, ".nana", "setup-scope.json")
	if content, err := os.ReadFile(scopePath); err == nil {
		text := string(content)
		if strings.Contains(text, `"scope":"project"`) || strings.Contains(text, `"scope": "project"`) ||
			strings.Contains(text, `"scope":"project-local"`) || strings.Contains(text, `"scope": "project-local"`) {
			return filepath.Join(cwd, ".codex", "prompts")
		}
	}
	return filepath.Join(CodexHome(), "prompts")
}

func resolveAgentPromptContent(cwd string, role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if !safeRolePattern.MatchString(role) {
		return "", fmt.Errorf("[ask] invalid --agent-prompt role %q. Expected lowercase role names like \"executor\" or \"test-engineer\".", role)
	}
	promptsDir := resolveAskPromptsDir(cwd)
	path := filepath.Join(promptsDir, role+".md")
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("[ask] --agent-prompt role %q not found in %s", role, promptsDir)
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return "", fmt.Errorf("[ask] --agent-prompt role %q is empty: %s", role, path)
	}
	return trimmed, nil
}

func askSlugify(value string) string {
	value = strings.ToLower(value)
	value = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 60 {
		value = value[:60]
	}
	if value == "" {
		return "task"
	}
	return value
}

func askTimestampToken(date time.Time) string {
	return strings.NewReplacer(":", "-", ".", "-").Replace(date.UTC().Format(time.RFC3339Nano))
}

func writeAskArtifact(cwd string, provider string, originalTask string, finalPrompt string, rawOutput string, exitCode int) (string, error) {
	artifactDir := filepath.Join(cwd, ".nana", "artifacts")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", err
	}
	artifactPath := filepath.Join(artifactDir, fmt.Sprintf("%s-%s-%s.md", provider, askSlugify(originalTask), askTimestampToken(time.Now())))
	summary := buildAskSummary(exitCode, rawOutput)
	actionItems := buildAskActionItems(exitCode)
	body := strings.Join([]string{
		fmt.Sprintf("# %s advisor artifact", provider),
		"",
		fmt.Sprintf("- Provider: %s", provider),
		fmt.Sprintf("- Exit code: %d", exitCode),
		fmt.Sprintf("- Created at: %s", time.Now().UTC().Format(time.RFC3339)),
		"",
		"## Original task",
		"",
		originalTask,
		"",
		"## Final prompt",
		"",
		finalPrompt,
		"",
		"## Raw output",
		"",
		"```text",
		emptyIfBlank(rawOutput, "(no output)"),
		"```",
		"",
		"## Concise summary",
		"",
		summary,
		"",
		"## Action items",
		"",
		"- " + strings.Join(actionItems, "\n- "),
		"",
	}, "\n")
	return artifactPath, os.WriteFile(artifactPath, []byte(body), 0o644)
}

func buildAskSummary(exitCode int, output string) string {
	if exitCode == 0 {
		return "Provider completed successfully. Review the raw output for details."
	}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return fmt.Sprintf("Provider command failed (exit %d): %s", exitCode, trimmed)
		}
	}
	return fmt.Sprintf("Provider command failed with exit code %d.", exitCode)
}

func buildAskActionItems(exitCode int) []string {
	if exitCode == 0 {
		return []string{
			"Review the response and extract decisions you want to apply.",
			"Capture follow-up implementation tasks if needed.",
		}
	}
	return []string{
		"Inspect the raw output error details.",
		"Fix CLI/auth/environment issues and rerun the command.",
	}
}

func emptyIfBlank(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
