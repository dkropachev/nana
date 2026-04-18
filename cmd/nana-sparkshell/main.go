package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTmuxTailLines    = 200
	minTmuxTailLines        = 100
	maxTmuxTailLines        = 1000
	defaultMaxVisibleLines  = 12
	defaultSummaryTimeoutMS = 60_000
	defaultSparkModel       = "gpt-5.3-codex-spark"
	defaultFrontierModel    = "gpt-5.4"
	defaultSummaryMaxLines  = 400
	defaultSummaryMaxBytes  = 24_000
)

type sparkError struct {
	message  string
	exitCode int
}

func (e sparkError) Error() string {
	return e.message
}

type sparkShellInput struct {
	command   []string
	paneID    string
	tailLines int
}

type commandOutput struct {
	exitCode int
	stdout   []byte
	stderr   []byte
}

type commandFamily struct {
	key         string
	pattern     string
	description string
	whatItDoes  string
}

var (
	genericShellFamily = commandFamily{
		key:         "generic-shell",
		pattern:     "ls|cat|find|grep|sed|awk|xargs|env|echo|pwd|which|sh|bash|zsh",
		description: "General shell and filesystem inspection commands.",
		whatItDoes:  "Inspects files, text, environment state, and shell-visible system output.",
	}
	gitFamily = commandFamily{
		key:         "git",
		pattern:     "git",
		description: "Git porcelain and repository inspection commands.",
		whatItDoes:  "Reads or changes repository state, history, branches, or working tree diffs.",
	}
	nodeFamily = commandFamily{
		key:         "node-js",
		pattern:     "npm|npx|pnpm|yarn|bun|node",
		description: "Node.js package management and build/test tooling.",
		whatItDoes:  "Installs dependencies, runs scripts, builds projects, or executes JavaScript tooling.",
	}
	pythonFamily = commandFamily{
		key:         "python",
		pattern:     "python|python3|pip|uv|poetry|pytest",
		description: "Python interpreter, packaging, and test commands.",
		whatItDoes:  "Runs Python code, manages packages, or executes Python-focused tests and tooling.",
	}
	rustFamily = commandFamily{
		key:         "rust",
		pattern:     "cargo|rustc",
		description: "Rust package, build, and test commands.",
		whatItDoes:  "Builds, checks, formats, lints, runs, or tests Rust projects.",
	}
	goFamily = commandFamily{
		key:         "go",
		pattern:     "go",
		description: "Go toolchain commands.",
		whatItDoes:  "Builds, formats, manages modules, or tests Go projects.",
	}
	rubyFamily = commandFamily{
		key:         "ruby",
		pattern:     "bundle|bundler|rake|ruby",
		description: "Ruby dependency and task runner commands.",
		whatItDoes:  "Runs Ruby code, dependency workflows, or Ruby project tasks.",
	}
	javaKotlinFamily = commandFamily{
		key:         "java-kotlin",
		pattern:     "mvn|gradle|gradlew|java|kotlinc",
		description: "Java and Kotlin build commands.",
		whatItDoes:  "Builds, tests, or runs JVM-based projects and wrappers.",
	}
	cCppFamily = commandFamily{
		key:         "c-cpp",
		pattern:     "make|cmake|gcc|g++|clang|clang++",
		description: "C and C++ build tooling.",
		whatItDoes:  "Configures, compiles, or builds native C/C++ projects.",
	}
	csharpFamily = commandFamily{
		key:         "csharp",
		pattern:     "dotnet",
		description: ".NET SDK commands.",
		whatItDoes:  "Builds, restores, runs, or tests .NET applications.",
	}
	swiftFamily = commandFamily{
		key:         "swift",
		pattern:     "swift|xcodebuild",
		description: "Swift Package Manager and Xcode build commands.",
		whatItDoes:  "Builds, tests, or packages Swift and Apple-platform projects.",
	}
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stdout, usageText())
		return
	}

	exitCode, err := run(args, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nana sparkshell: %v\n", err)
		if serr, ok := err.(sparkError); ok {
			os.Exit(serr.exitCode)
		}
		os.Exit(1)
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func run(args []string, stdout io.Writer, stderr io.Writer) (int, error) {
	input, err := parseInput(args)
	if err != nil {
		return 0, err
	}

	executionArgs := input.command
	if input.paneID != "" {
		executionArgs = append([]string{"tmux"}, buildCapturePaneArgs(input.paneID, input.tailLines)...)
	}

	output, err := executeCommand(executionArgs)
	if err != nil {
		return 0, err
	}

	if combinedVisibleLines(output.stdout, output.stderr) <= readLineThreshold() {
		if err := writeRawOutput(stdout, stderr, output.stdout, output.stderr); err != nil {
			return 0, err
		}
		return output.exitCode, nil
	}

	summary, summaryErr := summarizeOutput(executionArgs, output)
	if summaryErr != nil {
		if err := writeRawOutput(stdout, stderr, output.stdout, output.stderr); err != nil {
			return 0, err
		}
		fmt.Fprintf(stderr, "nana sparkshell: summary unavailable (%v)\n", summaryErr)
		return output.exitCode, nil
	}

	if !strings.HasSuffix(summary, "\n") {
		summary += "\n"
	}
	if _, err := io.WriteString(stdout, summary); err != nil {
		return 0, sparkError{message: err.Error(), exitCode: 1}
	}
	return output.exitCode, nil
}

func usageText() string {
	return fmt.Sprintf(
		`usage: nana-sparkshell <command> [args...]
   or: nana-sparkshell --tmux-pane <pane-id> [--tail-lines <%d-%d>]

Direct command mode executes argv without shell metacharacter parsing.
Tmux pane mode captures a larger pane tail and applies the same raw-vs-summary behavior.

Summary behavior:
  stdout+stderr is emitted raw when visible output is <= NANA_SPARKSHELL_LINES (default %d).
  Output above that threshold is summarized with codex exec using low reasoning.
  If summarization fails or times out, raw output is emitted with a "summary unavailable" notice.

Environment controls:
  NANA_SPARKSHELL_LINES                 raw-vs-summary line threshold (default %d)
  NANA_SPARKSHELL_SUMMARY_TIMEOUT_MS    codex summary timeout in milliseconds (default %d)
  NANA_SPARKSHELL_MODEL                 primary summary model; then NANA_DEFAULT_SPARK_MODEL / NANA_SPARK_MODEL
  NANA_SPARKSHELL_FALLBACK_MODEL        retry model for quota/access/capacity errors; then NANA_DEFAULT_FRONTIER_MODEL
  NANA_SPARKSHELL_SUMMARY_MAX_LINES     max output lines included in summary prompt (default %d)
  NANA_SPARKSHELL_SUMMARY_MAX_BYTES     max output bytes included in summary prompt (default %d)
`,
		minTmuxTailLines,
		maxTmuxTailLines,
		defaultMaxVisibleLines,
		defaultMaxVisibleLines,
		defaultSummaryTimeoutMS,
		defaultSummaryMaxLines,
		defaultSummaryMaxBytes,
	)
}

func parseInput(args []string) (sparkShellInput, error) {
	if len(args) == 0 {
		return sparkShellInput{}, sparkError{message: usageText(), exitCode: 2}
	}

	var paneID string
	tailLines := defaultTmuxTailLines
	explicitTailLines := false
	positional := make([]string, 0, len(args))

	for index := 0; index < len(args); {
		token := args[index]
		switch {
		case token == "--tmux-pane":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return sparkShellInput{}, sparkError{message: "--tmux-pane requires a pane id", exitCode: 2}
			}
			paneID = args[index+1]
			index += 2
		case strings.HasPrefix(token, "--tmux-pane="):
			value := strings.TrimPrefix(token, "--tmux-pane=")
			if strings.TrimSpace(value) == "" {
				return sparkShellInput{}, sparkError{message: "--tmux-pane requires a pane id", exitCode: 2}
			}
			paneID = value
			index++
		case token == "--tail-lines":
			if index+1 >= len(args) {
				return sparkShellInput{}, sparkError{message: "--tail-lines requires a numeric value", exitCode: 2}
			}
			parsed, err := parseTailLines(args[index+1])
			if err != nil {
				return sparkShellInput{}, err
			}
			tailLines = parsed
			explicitTailLines = true
			index += 2
		case strings.HasPrefix(token, "--tail-lines="):
			parsed, err := parseTailLines(strings.TrimPrefix(token, "--tail-lines="))
			if err != nil {
				return sparkShellInput{}, err
			}
			tailLines = parsed
			explicitTailLines = true
			index++
		default:
			positional = append(positional, token)
			index++
		}
	}

	if paneID != "" {
		if len(positional) > 0 {
			return sparkShellInput{}, sparkError{message: "tmux pane mode does not accept an additional command", exitCode: 2}
		}
		return sparkShellInput{paneID: paneID, tailLines: tailLines}, nil
	}
	if explicitTailLines {
		return sparkShellInput{}, sparkError{message: "--tail-lines requires --tmux-pane", exitCode: 2}
	}

	return sparkShellInput{command: positional}, nil
}

func parseTailLines(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minTmuxTailLines || value > maxTmuxTailLines {
		return 0, sparkError{
			message:  fmt.Sprintf("--tail-lines must be an integer between %d and %d", minTmuxTailLines, maxTmuxTailLines),
			exitCode: 2,
		}
	}
	return value, nil
}

func buildCapturePaneArgs(target string, visibleLines int) []string {
	return []string{"capture-pane", "-t", target, "-p", "-S", fmt.Sprintf("-%d", visibleLines)}
}

func executeCommand(argv []string) (commandOutput, error) {
	if len(argv) == 0 {
		return commandOutput{}, sparkError{message: "usage: nana-sparkshell <command> [args...]", exitCode: 2}
	}

	cmd := buildCommand(argv[0], argv[1:])
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return commandOutput{exitCode: 0, stdout: stdout.Bytes(), stderr: stderr.Bytes()}, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return commandOutput{exitCode: exitErr.ExitCode(), stdout: stdout.Bytes(), stderr: stderr.Bytes()}, nil
	}

	var pathErr *exec.Error
	if errors.As(err, &pathErr) {
		return commandOutput{}, sparkError{message: err.Error(), exitCode: 127}
	}
	return commandOutput{}, sparkError{message: err.Error(), exitCode: 1}
}

func buildCommand(commandName string, args []string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(commandName))
		switch ext {
		case ".cmd", ".bat":
			comspec := os.Getenv("ComSpec")
			if strings.TrimSpace(comspec) == "" {
				comspec = "cmd.exe"
			}
			cmd := exec.Command(comspec, "/d", "/s", "/c", commandName)
			cmd.Args = append(cmd.Args, args...)
			return cmd
		case ".ps1":
			cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", commandName)
			cmd.Args = append(cmd.Args, args...)
			return cmd
		}
	}
	cmd := exec.Command(commandName, args...)
	return cmd
}

func writeRawOutput(stdoutWriter io.Writer, stderrWriter io.Writer, stdoutBytes []byte, stderrBytes []byte) error {
	if _, err := stdoutWriter.Write(stdoutBytes); err != nil {
		return sparkError{message: err.Error(), exitCode: 1}
	}
	if _, err := stderrWriter.Write(stderrBytes); err != nil {
		return sparkError{message: err.Error(), exitCode: 1}
	}
	return nil
}

func readLineThreshold() int {
	if raw := strings.TrimSpace(os.Getenv("NANA_SPARKSHELL_LINES")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultMaxVisibleLines
}

func countVisibleLines(bytes []byte) int {
	if len(bytes) == 0 {
		return 0
	}
	return len(strings.Split(strings.TrimSuffix(string(bytes), "\n"), "\n"))
}

func combinedVisibleLines(stdout []byte, stderr []byte) int {
	return countVisibleLines(stdout) + countVisibleLines(stderr)
}

func summarizeOutput(command []string, output commandOutput) (string, error) {
	prompt := buildSummaryPrompt(command, output)
	model := resolveModel()
	fallbackModel := resolveFallbackModel()
	timeoutMS := readSummaryTimeoutMS()

	stdout, stderr, ok, err := runCodexExec(prompt, model, timeoutMS)
	if err != nil {
		return "", err
	}
	if !ok {
		if fallbackModel != model && shouldRetryWithFallback(stderr) {
			fallbackStdout, fallbackStderr, fallbackOK, fallbackErr := runCodexExec(prompt, fallbackModel, timeoutMS)
			if fallbackErr != nil {
				return "", fallbackErr
			}
			if !fallbackOK {
				primaryMessage := strings.TrimSpace(stderr)
				if primaryMessage == "" {
					primaryMessage = "codex exec exited unsuccessfully"
				}
				fallbackMessage := strings.TrimSpace(fallbackStderr)
				if fallbackMessage == "" {
					fallbackMessage = "codex exec exited unsuccessfully"
				}
				return "", sparkError{message: fmt.Sprintf("codex exec failed for primary model `%s` (%s) and fallback model `%s` (%s)", model, primaryMessage, fallbackModel, fallbackMessage), exitCode: 1}
			}
			summary := normalizeSummary(fallbackStdout)
			if summary == "" {
				return "", sparkError{message: "codex exec fallback returned no valid summary sections", exitCode: 1}
			}
			return summary, nil
		}
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = "codex exec exited unsuccessfully"
		} else {
			message = "codex exec exited unsuccessfully: " + message
		}
		return "", sparkError{message: message, exitCode: 1}
	}

	summary := normalizeSummary(stdout)
	if summary == "" {
		return "", sparkError{message: "codex exec returned no valid summary sections", exitCode: 1}
	}
	return summary, nil
}

func resolveModel() string {
	for _, key := range []string{"NANA_SPARKSHELL_MODEL", "NANA_DEFAULT_SPARK_MODEL", "NANA_SPARK_MODEL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return defaultSparkModel
}

func resolveFallbackModel() string {
	for _, key := range []string{"NANA_SPARKSHELL_FALLBACK_MODEL", "NANA_DEFAULT_FRONTIER_MODEL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return defaultFrontierModel
}

func readSummaryTimeoutMS() int {
	if raw := strings.TrimSpace(os.Getenv("NANA_SPARKSHELL_SUMMARY_TIMEOUT_MS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultSummaryTimeoutMS
}

func shouldRetryWithFallback(stderr string) bool {
	normalized := strings.ToLower(stderr)
	for _, needle := range []string{"quota", "rate limit", "429", "unavailable", "not available", "unknown model", "model not found", "no access", "capacity"} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func runCodexExec(prompt string, model string, timeoutMS int) (string, string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "codex", "exec", "--model", model, "--sandbox", "read-only", "-c", `model_reasoning_effort="low"`, "--skip-git-repo-check", "--color", "never", "-")
	cmd.Stdin = strings.NewReader(prompt)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", false, sparkError{message: fmt.Sprintf("codex summary timed out after %dms", timeoutMS), exitCode: 1}
	}
	if err == nil {
		return stdout.String(), stderr.String(), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), stderr.String(), false, nil
	}
	return "", "", false, sparkError{message: err.Error(), exitCode: 1}
}

func normalizeSummary(raw string) string {
	var summary []string
	var failures []string
	var warnings []string
	var current *[]string

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		normalized := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "-* ")))
		switch {
		case strings.HasPrefix(normalized, "summary:"):
			entry := strings.TrimSpace(strings.TrimPrefix(normalized, "summary:"))
			summary = append(summary, entry)
			current = &summary
			continue
		case strings.HasPrefix(normalized, "failures:"):
			entry := strings.TrimSpace(strings.TrimPrefix(normalized, "failures:"))
			failures = append(failures, entry)
			current = &failures
			continue
		case strings.HasPrefix(normalized, "warnings:"):
			entry := strings.TrimSpace(strings.TrimPrefix(normalized, "warnings:"))
			warnings = append(warnings, entry)
			current = &warnings
			continue
		}

		if strings.Contains(trimmed, ":") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			current = nil
			continue
		}
		if current != nil {
			*current = append(*current, trimmed)
		}
	}

	sections := make([]string, 0, 3)
	if len(summary) > 0 {
		sections = append(sections, renderSection("summary", summary))
	}
	if len(failures) > 0 {
		sections = append(sections, renderSection("failures", failures))
	}
	if len(warnings) > 0 {
		sections = append(sections, renderSection("warnings", warnings))
	}
	return strings.Join(sections, "\n")
}

func renderSection(name string, entries []string) string {
	lines := make([]string, 0, len(entries))
	for index, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if index == 0 {
			lines = append(lines, fmt.Sprintf("- %s: %s", name, entry))
		} else {
			lines = append(lines, fmt.Sprintf("  - %s", entry))
		}
	}
	return strings.Join(lines, "\n")
}

func buildSummaryPrompt(command []string, output commandOutput) string {
	executable := "unknown"
	if len(command) > 0 {
		executable = command[0]
	}
	family := selectCommandFamily(executable)
	stdoutText := string(output.stdout)
	stderrText := string(output.stderr)
	return fmt.Sprintf(
		"You summarize shell command output.\nReturn markdown bullets only. Allowed top-level sections: summary:, failures:, warnings:.\nDo not suggest fixes, next steps, commands, or recommendations.\nKeep the summary descriptive and grounded in the provided output.\n\nCommand: %s\nCommand family: %s\nFamily pattern: %s\nFamily description: %s\nFamily what_it_does: %s\nExit code: %d\n\nSTDOUT total lines: %d\nSTDOUT total bytes: %d\nSTDERR total lines: %d\nSTDERR total bytes: %d\n\nSTDOUT:\n<<<STDOUT\n%s\n>>>STDOUT\n\nSTDERR:\n<<<STDERR\n%s\n>>>STDERR\n",
		shellJoin(command),
		family.key,
		family.pattern,
		family.description,
		family.whatItDoes,
		output.exitCode,
		countVisibleLines(output.stdout),
		len(stdoutText),
		countVisibleLines(output.stderr),
		len(stderrText),
		truncateForPrompt(stdoutText, "stdout"),
		truncateForPrompt(stderrText, "stderr"),
	)
}

func selectCommandFamily(command string) commandFamily {
	switch filepath.Base(command) {
	case "git":
		return gitFamily
	case "npm", "npx", "pnpm", "yarn", "bun", "node":
		return nodeFamily
	case "python", "python3", "pip", "uv", "poetry", "pytest":
		return pythonFamily
	case "cargo", "rustc":
		return rustFamily
	case "go":
		return goFamily
	case "bundle", "bundler", "rake", "ruby":
		return rubyFamily
	case "mvn", "gradle", "gradlew", "java", "kotlinc":
		return javaKotlinFamily
	case "make", "cmake", "gcc", "g++", "clang", "clang++":
		return cCppFamily
	case "dotnet":
		return csharpFamily
	case "swift", "xcodebuild":
		return swiftFamily
	default:
		return genericShellFamily
	}
}

func truncateForPrompt(text string, label string) string {
	maxLines := readPositiveIntEnv("NANA_SPARKSHELL_SUMMARY_MAX_LINES", defaultSummaryMaxLines)
	maxBytes := readPositiveIntEnv("NANA_SPARKSHELL_SUMMARY_MAX_BYTES", defaultSummaryMaxBytes)
	truncated := text

	lines := 0
	if text != "" {
		lines = len(strings.Split(strings.TrimSuffix(text, "\n"), "\n"))
	}
	if lines > maxLines {
		split := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		headCount := maxLines / 2
		tailCount := maxLines - headCount
		head := append([]string{}, split[:headCount]...)
		tail := append([]string{}, split[len(split)-tailCount:]...)
		excerpt := append(head, fmt.Sprintf("[... truncated %s: omitted %d of %d total lines ...]", label, lines-maxLines, lines))
		excerpt = append(excerpt, tail...)
		truncated = strings.Join(excerpt, "\n")
		if strings.HasSuffix(text, "\n") {
			truncated += "\n"
		}
	}

	if len(truncated) > maxBytes {
		headBytes := maxBytes / 2
		tailBytes := maxBytes - headBytes
		prefix := safePrefix(truncated, headBytes)
		suffix := safeSuffix(truncated, tailBytes)
		omittedBytes := len(text) - len(prefix) - len(suffix)
		truncated = fmt.Sprintf("%s\n[... truncated %s: omitted approximately %d of %d total bytes ...]\n%s", prefix, label, omittedBytes, len(text), suffix)
	}
	return truncated
}

func readPositiveIntEnv(name string, fallback int) int {
	if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func safePrefix(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	runes := []rune(text)
	out := make([]rune, 0, len(runes))
	size := 0
	for _, r := range runes {
		rsize := len(string(r))
		if size+rsize > maxBytes {
			break
		}
		out = append(out, r)
		size += rsize
	}
	return string(out)
}

func safeSuffix(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	runes := []rune(text)
	out := make([]rune, 0, len(runes))
	size := 0
	for index := len(runes) - 1; index >= 0; index-- {
		rsize := len(string(runes[index]))
		if size+rsize > maxBytes {
			break
		}
		out = append([]rune{runes[index]}, out...)
		size += rsize
	}
	return string(out)
}

func shellJoin(command []string) string {
	parts := make([]string, 0, len(command))
	for _, part := range command {
		if part == "" {
			parts = append(parts, "''")
			continue
		}
		if strings.ContainsAny(part, " \t\n'\"\\|&;<>*?$()[]{}") {
			parts = append(parts, "'"+strings.ReplaceAll(part, "'", `'\''`)+"'")
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}
