package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const codeBinEnv = "NANA_EXPLORE_CODEX_BIN"

type exploreArgs struct {
	cwd           string
	prompt        string
	promptFile    string
	sparkModel    string
	fallbackModel string
}

type codexAttempt struct {
	statusCode int
	stderr     string
	markdown   string
}

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "[nana explore] %v\n", err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	parsed, err := parseArgs(args)
	if err != nil {
		return err
	}
	promptContract, err := os.ReadFile(parsed.promptFile)
	if err != nil {
		return fmt.Errorf("failed to read explore prompt contract %s: %w", parsed.promptFile, err)
	}

	sparkAttempt, err := invokeCodex(parsed, parsed.sparkModel, string(promptContract))
	if err != nil {
		return fmt.Errorf("spark attempt failed to launch: %w", err)
	}
	if sparkAttempt.statusCode == 0 {
		fmt.Fprint(os.Stdout, sparkAttempt.markdown)
		return nil
	}

	fmt.Fprintf(os.Stderr, "[nana explore] spark model `%s` unavailable or failed (exit %d). Falling back to `%s`.\n", parsed.sparkModel, sparkAttempt.statusCode, parsed.fallbackModel)
	if strings.TrimSpace(sparkAttempt.stderr) != "" {
		fmt.Fprintf(os.Stderr, "[nana explore] spark stderr: %s\n", strings.TrimSpace(sparkAttempt.stderr))
	}

	fallbackAttempt, err := invokeCodex(parsed, parsed.fallbackModel, string(promptContract))
	if err != nil {
		return fmt.Errorf("fallback attempt failed to launch: %w", err)
	}
	if fallbackAttempt.statusCode == 0 {
		fmt.Fprint(os.Stdout, fallbackAttempt.markdown)
		return nil
	}
	return fmt.Errorf(
		"both spark (`%s`) and fallback (`%s`) attempts failed (codes %d / %d). Last stderr: %s",
		parsed.sparkModel,
		parsed.fallbackModel,
		sparkAttempt.statusCode,
		fallbackAttempt.statusCode,
		strings.TrimSpace(fallbackAttempt.stderr),
	)
}

func parseArgs(args []string) (exploreArgs, error) {
	var parsed exploreArgs
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch token {
		case "--cwd":
			index++
			if index >= len(args) {
				return exploreArgs{}, fmt.Errorf("missing value after --cwd\n%s", usage())
			}
			parsed.cwd = strings.TrimSpace(args[index])
		case "--prompt":
			index++
			if index >= len(args) {
				return exploreArgs{}, fmt.Errorf("missing value after --prompt\n%s", usage())
			}
			parsed.prompt = strings.TrimSpace(args[index])
		case "--prompt-file":
			index++
			if index >= len(args) {
				return exploreArgs{}, fmt.Errorf("missing value after --prompt-file\n%s", usage())
			}
			parsed.promptFile = strings.TrimSpace(args[index])
		case "--model-spark":
			index++
			if index >= len(args) {
				return exploreArgs{}, fmt.Errorf("missing value after --model-spark\n%s", usage())
			}
			parsed.sparkModel = strings.TrimSpace(args[index])
		case "--model-fallback":
			index++
			if index >= len(args) {
				return exploreArgs{}, fmt.Errorf("missing value after --model-fallback\n%s", usage())
			}
			parsed.fallbackModel = strings.TrimSpace(args[index])
		case "--help", "-h":
			return exploreArgs{}, fmt.Errorf("%s", usage())
		default:
			return exploreArgs{}, fmt.Errorf("unknown argument: %s\n%s", token, usage())
		}
	}

	switch {
	case parsed.cwd == "":
		return exploreArgs{}, fmt.Errorf("missing --cwd\n%s", usage())
	case parsed.prompt == "":
		return exploreArgs{}, fmt.Errorf("missing --prompt\n%s", usage())
	case parsed.promptFile == "":
		return exploreArgs{}, fmt.Errorf("missing --prompt-file\n%s", usage())
	case parsed.sparkModel == "":
		return exploreArgs{}, fmt.Errorf("missing --model-spark\n%s", usage())
	case parsed.fallbackModel == "":
		return exploreArgs{}, fmt.Errorf("missing --model-fallback\n%s", usage())
	}
	return parsed, nil
}

func usage() string {
	return "Usage: nana-explore --cwd <dir> --prompt <text> --prompt-file <explore-prompt.md> --model-spark <model> --model-fallback <model>"
}

func invokeCodex(args exploreArgs, model string, promptContract string) (codexAttempt, error) {
	allowlist, err := prepareAllowlistEnvironment()
	if err != nil {
		return codexAttempt{}, err
	}
	defer os.RemoveAll(allowlist.root)

	outputFile, err := os.CreateTemp("", "nana-explore-output-*.md")
	if err != nil {
		return codexAttempt{}, err
	}
	outputPath := outputFile.Name()
	if err := outputFile.Close(); err != nil {
		return codexAttempt{}, err
	}
	defer os.Remove(outputPath)

	finalPrompt := composeExecPrompt(args.prompt, promptContract)
	cmd := exec.Command(resolveCodexBinary(), "exec", "-C", args.cwd, "-m", model, "-s", "read-only", "-c", `model_reasoning_effort="low"`, "-c", "shell_environment_policy.inherit=all", "--skip-git-repo-check", "-o", outputPath, finalPrompt)
	cmd.Env = withEnv(
		os.Environ(),
		"PATH="+allowlist.binDir,
		"SHELL="+allowlist.shellPath,
	)
	output, err := cmd.CombinedOutput()
	statusCode := 1
	if cmd.ProcessState != nil {
		statusCode = cmd.ProcessState.ExitCode()
	}
	if err == nil {
		statusCode = 0
	}
	markdown := ""
	if content, readErr := os.ReadFile(outputPath); readErr == nil {
		markdown = string(content)
	}
	return codexAttempt{
		statusCode: statusCode,
		stderr:     string(output),
		markdown:   markdown,
	}, nil
}

type allowlistEnv struct {
	root      string
	binDir    string
	shellPath string
}

func prepareAllowlistEnvironment() (allowlistEnv, error) {
	root, err := os.MkdirTemp("", "nana-explore-allowlist-")
	if err != nil {
		return allowlistEnv{}, err
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return allowlistEnv{}, err
	}

	shellTarget, err := exec.LookPath("bash")
	if err != nil {
		shellTarget, err = exec.LookPath("sh")
		if err != nil {
			return allowlistEnv{}, err
		}
	}
	shellPath := filepath.Join(binDir, "bash")
	if runtime.GOOS == "windows" {
		shellPath += ".cmd"
		err = os.WriteFile(shellPath, []byte("@echo off\r\n\""+shellTarget+"\" %*\r\n"), 0o755)
	} else {
		err = os.WriteFile(shellPath, []byte("#!/bin/sh\nexec "+shellQuote(shellTarget)+" \"$@\"\n"), 0o755)
	}
	if err != nil {
		return allowlistEnv{}, err
	}

	for _, name := range []string{"rg", "grep", "ls", "find", "wc", "cat", "head", "tail", "pwd", "printf"} {
		if target, lookErr := exec.LookPath(name); lookErr == nil {
			wrapperPath := filepath.Join(binDir, name)
			if runtime.GOOS == "windows" {
				wrapperPath += ".cmd"
				err = os.WriteFile(wrapperPath, []byte("@echo off\r\n\""+target+"\" %*\r\n"), 0o755)
			} else {
				err = os.WriteFile(wrapperPath, []byte("#!/bin/sh\nexec "+shellQuote(target)+" \"$@\"\n"), 0o755)
			}
			if err != nil {
				return allowlistEnv{}, err
			}
		}
	}

	return allowlistEnv{root: root, binDir: binDir, shellPath: shellPath}, nil
}

func resolveCodexBinary() string {
	if value := strings.TrimSpace(os.Getenv(codeBinEnv)); value != "" {
		return value
	}
	return "codex"
}

func composeExecPrompt(prompt string, promptContract string) string {
	return strings.TrimSpace(promptContract) + "\n\nTask:\n" + strings.TrimSpace(prompt)
}

func withEnv(base []string, overrides ...string) []string {
	envMap := map[string]string{}
	for _, item := range base {
		if key, value, ok := strings.Cut(item, "="); ok {
			envMap[key] = value
		}
	}
	for _, item := range overrides {
		if key, value, ok := strings.Cut(item, "="); ok {
			envMap[key] = value
		}
	}
	out := make([]string, 0, len(envMap))
	for key, value := range envMap {
		out = append(out, key+"="+value)
	}
	return out
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	encoded, _ := json.Marshal(value)
	return strings.Trim(string(encoded), "\"")
}
