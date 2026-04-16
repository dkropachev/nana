package gocli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dkropachev/nana/internal/gocliassets"
)

const ReflectUsage = "Usage: nana reflect --prompt \"<prompt>\"\n   or: nana reflect --prompt-file <file>"

type ParsedReflectArgs struct {
	Prompt     string
	PromptFile string
}

func ParseReflectArgs(args []string) (ParsedReflectArgs, error) {
	var parsed ParsedReflectArgs
	for i := 0; i < len(args); i++ {
		token := args[i]
		switch {
		case token == "--prompt":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return ParsedReflectArgs{}, fmt.Errorf("missing text after --prompt\n%s", ReflectUsage)
			}
			if parsed.Prompt != "" || parsed.PromptFile != "" {
				return ParsedReflectArgs{}, fmt.Errorf("choose exactly one of --prompt or --prompt-file\n%s", ReflectUsage)
			}
			parsed.Prompt = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(token, "--prompt="):
			if parsed.Prompt != "" || parsed.PromptFile != "" {
				return ParsedReflectArgs{}, fmt.Errorf("choose exactly one of --prompt or --prompt-file\n%s", ReflectUsage)
			}
			parsed.Prompt = strings.TrimSpace(strings.TrimPrefix(token, "--prompt="))
		case token == "--prompt-file":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return ParsedReflectArgs{}, fmt.Errorf("missing path after --prompt-file\n%s", ReflectUsage)
			}
			if parsed.Prompt != "" || parsed.PromptFile != "" {
				return ParsedReflectArgs{}, fmt.Errorf("choose exactly one of --prompt or --prompt-file\n%s", ReflectUsage)
			}
			parsed.PromptFile = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(token, "--prompt-file="):
			if parsed.Prompt != "" || parsed.PromptFile != "" {
				return ParsedReflectArgs{}, fmt.Errorf("choose exactly one of --prompt or --prompt-file\n%s", ReflectUsage)
			}
			parsed.PromptFile = strings.TrimSpace(strings.TrimPrefix(token, "--prompt-file="))
		default:
			return ParsedReflectArgs{}, fmt.Errorf("unknown argument: %s\n%s", token, ReflectUsage)
		}
	}

	if parsed.Prompt == "" && parsed.PromptFile == "" {
		return ParsedReflectArgs{}, fmt.Errorf("missing prompt. provide --prompt or --prompt-file\n%s", ReflectUsage)
	}
	return parsed, nil
}

func LoadReflectPrompt(parsed ParsedReflectArgs) (string, error) {
	if parsed.Prompt != "" {
		return parsed.Prompt, nil
	}
	content, err := os.ReadFile(parsed.PromptFile)
	if err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(string(content))
	if prompt == "" {
		return "", fmt.Errorf("prompt file is empty: %s", parsed.PromptFile)
	}
	return prompt, nil
}

func Reflect(repoRoot string, cwd string, args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprintln(os.Stdout, ReflectUsage)
		return nil
	}
	parsed, err := ParseReflectArgs(args)
	if err != nil {
		return err
	}
	prompt, err := LoadReflectPrompt(parsed)
	if err != nil {
		return err
	}
	harnessPath, err := resolveExploreHarnessPath(repoRoot)
	if err != nil {
		return err
	}
	bridgePromptPath, cleanupPromptPath, err := resolveExploreBridgePromptPath(repoRoot)
	if err != nil {
		return err
	}
	if cleanupPromptPath != "" {
		defer os.Remove(cleanupPromptPath)
	}
	commandArgs := []string{
		"--cwd", cwd,
		"--prompt", prompt,
		"--prompt-file", bridgePromptPath,
		"--model-spark", envOr("NANA_EXPLORE_SPARK_MODEL", "gpt-5.3-codex-spark"),
		"--model-fallback", envOr("NANA_EXPLORE_MAIN_MODEL", "gpt-5.4"),
	}
	cmd := exec.Command(harnessPath, commandArgs...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}

func resolveExploreHarnessPath(repoRoot string) (string, error) {
	candidates := []string{}
	if strings.TrimSpace(repoRoot) != "" {
		candidates = append(candidates, filepath.Join(repoRoot, "bin", "go", binaryName("nana-explore-harness")))
	}
	if strings.TrimSpace(os.Args[0]) != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(os.Args[0]), binaryName("nana-explore-harness")))
	}
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates, filepath.Join(exeDir, binaryName("nana-explore-harness")))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("go explore harness not found in repo assets or beside the nana binary")
}

func resolveExploreBridgePromptPath(repoRoot string) (string, string, error) {
	if strings.TrimSpace(repoRoot) != "" {
		path := filepath.Join(repoRoot, "prompts", "explore-harness.md")
		if _, err := os.Stat(path); err == nil {
			return path, "", nil
		}
	}
	prompts, err := gocliassets.Prompts()
	if err != nil {
		return "", "", err
	}
	content, ok := prompts["explore-harness.md"]
	if !ok {
		return "", "", fmt.Errorf("embedded explore harness prompt missing")
	}
	file, err := os.CreateTemp("", "nana-explore-harness-*.md")
	if err != nil {
		return "", "", err
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return "", "", err
	}
	if err := file.Close(); err != nil {
		return "", "", err
	}
	return file.Name(), file.Name(), nil
}

func binaryName(base string) string {
	if os.PathSeparator == '\\' {
		return base + ".exe"
	}
	return base
}

func envOr(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
