package gocli

import (
	"encoding/json"
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

const VerifyProfileFile = "nana-verify.json"

const (
	defaultVerifyOutputLimitBytes = 64 * 1024
	maxVerifyOutputLimitBytes     = 4 * 1024 * 1024
	verifyOutputLimitEnv          = "NANA_VERIFY_OUTPUT_LIMIT_BYTES"
)

const VerifyHelp = `nana verify - Run the repository-native verification profile

Usage:
  nana verify [--json]
  nana verify --profile [--json]

Options:
  --json       Emit machine-readable JSON evidence.
  --profile    Print the detected verification profile without running commands.

Profile:
  ` + VerifyProfileFile + ` defines the canonical sequential stages for this repo.
  Optional changed_scope guidance may map changed files to targeted checks.
  A changed_scope profile must keep a full_check command as the fallback.
  Nana searches the current directory and its parents for the profile file.
  If the profile is missing or invalid, preflight output reports searched paths,
  detected fallback commands, and a minimal profile example.
  With --json, that recovery output is structured and includes config_found.
`

type verificationProfile struct {
	Version      int                        `json:"version"`
	Name         string                     `json:"name,omitempty"`
	Description  string                     `json:"description,omitempty"`
	Stages       []verificationStageProfile `json:"stages"`
	ChangedScope *verificationChangedScope  `json:"changed_scope,omitempty"`
}

type verificationStageProfile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

type verificationChangedScope struct {
	Description string                            `json:"description,omitempty"`
	FullCheck   verificationChangedScopeFullCheck `json:"full_check"`
	Paths       []verificationChangedScopePath    `json:"paths,omitempty"`
}

type verificationChangedScopeFullCheck struct {
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

type verificationChangedScopePath struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Patterns    []string `json:"patterns"`
	Stages      []string `json:"stages,omitempty"`
	Checks      []string `json:"checks,omitempty"`
}

type verificationCommandEvidence struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	Command          string `json:"command"`
	Status           string `json:"status"`
	ExitCode         int    `json:"exit_code"`
	DurationMillis   int64  `json:"duration_millis"`
	Output           string `json:"output,omitempty"`
	OutputBytes      int64  `json:"output_bytes"`
	OutputTruncated  bool   `json:"output_truncated"`
	OutputLimitBytes int    `json:"output_limit_bytes"`
	StdoutBytes      int64  `json:"stdout_bytes"`
	StdoutTruncated  bool   `json:"stdout_truncated"`
	StderrBytes      int64  `json:"stderr_bytes"`
	StderrTruncated  bool   `json:"stderr_truncated"`
}

type verificationEvidence struct {
	Version        int                           `json:"version"`
	GeneratedAt    string                        `json:"generated_at"`
	RepoRoot       string                        `json:"repo_root"`
	ProfilePath    string                        `json:"profile_path"`
	Profile        verificationProfile           `json:"profile"`
	Passed         bool                          `json:"passed"`
	FailedStages   []string                      `json:"failed_stages,omitempty"`
	DurationMillis int64                         `json:"duration_millis"`
	Stages         []verificationCommandEvidence `json:"stages"`
}

type verifyOptions struct {
	JSON        bool
	ProfileOnly bool
	Help        bool
}

type verificationRunOptions struct {
	OutputLimitBytes int
	Stdout           io.Writer
	Stderr           io.Writer
}

type verificationProfilePreflight struct {
	Status        string
	StartDir      string
	SearchedPaths []string
	ProfilePath   string
	Error         string
	Fallback      verificationFallbackSummary
}

type verificationFallbackSummary struct {
	RepoRoot string
	Source   string
	Commands []verificationFallbackCommand
	Warnings []string
}

type verificationFallbackCommand struct {
	Stage   string `json:"stage"`
	Command string `json:"command"`
}

type verificationProfileRecoveryEvidence struct {
	Version                   int                           `json:"version"`
	GeneratedAt               string                        `json:"generated_at"`
	Status                    string                        `json:"status"`
	ConfigFound               bool                          `json:"config_found"`
	ConfigValid               bool                          `json:"config_valid"`
	Explanation               string                        `json:"explanation"`
	Error                     string                        `json:"error,omitempty"`
	StartDir                  string                        `json:"start_dir"`
	SearchedPaths             []string                      `json:"searched_paths"`
	ProfilePath               string                        `json:"profile_path,omitempty"`
	RepoRoot                  string                        `json:"repo_root,omitempty"`
	FallbackSource            string                        `json:"fallback_source,omitempty"`
	SuggestedFallbackCommands []verificationFallbackCommand `json:"suggested_fallback_commands"`
	Warnings                  []string                      `json:"warnings,omitempty"`
	Suggestion                string                        `json:"suggestion"`
	ProfileExample            string                        `json:"profile_example"`
}

func Verify(cwd string, args []string) error {
	options, err := parseVerifyOptions(args)
	if err != nil {
		return err
	}
	if options.Help {
		return nil
	}
	repoRoot, profilePath, profile, preflight, err := loadVerificationProfileWithPreflight(cwd)
	if err != nil {
		if printErr := printVerificationProfileRecovery(preflight, options.JSON); printErr != nil {
			return printErr
		}
		return err
	}
	if options.ProfileOnly {
		return printVerificationProfile(profilePath, profile, options.JSON)
	}

	runOptions := defaultVerificationRunOptions()
	if !options.JSON {
		runOptions.Stdout = os.Stdout
		runOptions.Stderr = os.Stderr
	}
	report, err := runVerificationProfileWithOptions(repoRoot, profilePath, profile, runOptions)
	if err != nil {
		return err
	}
	if err := printVerificationEvidence(report, options.JSON); err != nil {
		return err
	}
	if !report.Passed {
		return fmt.Errorf("verification failed: %s", strings.Join(report.FailedStages, ", "))
	}
	return nil
}

func parseVerifyOptions(args []string) (verifyOptions, error) {
	var options verifyOptions
	for _, token := range args {
		switch token {
		case "--json", "-j":
			options.JSON = true
		case "--profile", "profile", "list":
			options.ProfileOnly = true
		case "--help", "-h", "help":
			fmt.Fprint(os.Stdout, VerifyHelp)
			options.Help = true
			return options, nil
		default:
			return options, fmt.Errorf("unknown verify option: %s\n\n%s", token, VerifyHelp)
		}
	}
	return options, nil
}

func loadVerificationProfile(cwd string) (string, string, verificationProfile, error) {
	repoRoot, profilePath, profile, _, err := loadVerificationProfileWithPreflight(cwd)
	return repoRoot, profilePath, profile, err
}

func loadVerificationProfileWithPreflight(cwd string) (string, string, verificationProfile, verificationProfilePreflight, error) {
	repoRoot, profilePath, ok, searchedPaths := findVerificationProfileWithSearch(cwd)
	preflight := verificationProfilePreflight{
		Status:        "valid",
		StartDir:      absolutePathOrInput(cwd),
		SearchedPaths: searchedPaths,
		ProfilePath:   profilePath,
	}
	if !ok {
		preflight.Status = "missing"
		preflight.Fallback = buildVerificationFallbackSummary(cwd, "")
		err := fmt.Errorf("%s not found from %s or its parents", VerifyProfileFile, cwd)
		preflight.Error = err.Error()
		return "", "", verificationProfile{}, preflight, err
	}
	var profile verificationProfile
	if err := readGithubJSON(profilePath, &profile); err != nil {
		profileErr := err
		err := fmt.Errorf("invalid %s: %w", profilePath, profileErr)
		preflight.Status = "invalid"
		preflight.Error = profileErr.Error()
		preflight.Fallback = buildVerificationFallbackSummary(cwd, profilePath)
		return "", "", verificationProfile{}, preflight, err
	}
	if err := normalizeVerificationProfile(&profile); err != nil {
		profileErr := err
		err := fmt.Errorf("invalid %s: %w", profilePath, profileErr)
		preflight.Status = "invalid"
		preflight.Error = profileErr.Error()
		preflight.Fallback = buildVerificationFallbackSummary(cwd, profilePath)
		return "", "", verificationProfile{}, preflight, err
	}
	return repoRoot, profilePath, profile, preflight, nil
}

func findVerificationProfile(cwd string) (string, string, bool) {
	repoRoot, profilePath, ok, _ := findVerificationProfileWithSearch(cwd)
	return repoRoot, profilePath, ok
}

func findVerificationProfileWithSearch(cwd string) (string, string, bool, []string) {
	current, err := filepath.Abs(cwd)
	if err != nil {
		current = cwd
	}
	searchedPaths := []string{}
	for {
		candidate := filepath.Join(current, VerifyProfileFile)
		searchedPaths = append(searchedPaths, candidate)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return current, candidate, true, searchedPaths
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", false, searchedPaths
		}
		current = parent
	}
}

func absolutePathOrInput(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func buildVerificationFallbackSummary(cwd string, profilePath string) verificationFallbackSummary {
	repoRoot := verificationFallbackRepoRoot(cwd, profilePath)
	plan := detectGithubVerificationPlan(repoRoot)
	return verificationFallbackSummary{
		RepoRoot: repoRoot,
		Source:   defaultString(plan.Source, "heuristic"),
		Commands: flattenVerificationFallbackCommands(plan),
		Warnings: append([]string{}, plan.Warnings...),
	}
}

func verificationFallbackRepoRoot(cwd string, profilePath string) string {
	if strings.TrimSpace(profilePath) != "" {
		return filepath.Dir(profilePath)
	}
	if output, err := readGitOutput(cwd, "rev-parse", "--show-toplevel"); err == nil {
		if root := strings.TrimSpace(output); root != "" {
			return filepath.Clean(root)
		}
	}
	return absolutePathOrInput(cwd)
}

func flattenVerificationFallbackCommands(plan githubVerificationPlan) []verificationFallbackCommand {
	var commands []verificationFallbackCommand
	add := func(stage string, values []string) {
		for _, command := range values {
			command = strings.TrimSpace(command)
			if command != "" {
				commands = append(commands, verificationFallbackCommand{Stage: stage, Command: command})
			}
		}
	}
	add("lint", plan.Lint)
	add("compile", plan.Compile)
	add("unit", plan.Unit)
	add("integration", plan.Integration)
	add("benchmark", plan.Benchmarks)
	return commands
}

func printVerificationProfileRecovery(preflight verificationProfilePreflight, jsonOutput bool) error {
	if jsonOutput {
		evidence, ok := verificationProfileRecovery(preflight)
		if !ok {
			return nil
		}
		return writeIndentedJSON(evidence)
	}
	message := formatVerificationProfileRecovery(preflight)
	if strings.TrimSpace(message) == "" {
		return nil
	}
	fmt.Fprint(os.Stderr, message)
	return nil
}

func verificationProfileRecovery(preflight verificationProfilePreflight) (verificationProfileRecoveryEvidence, bool) {
	switch preflight.Status {
	case "missing", "invalid":
	default:
		return verificationProfileRecoveryEvidence{}, false
	}

	fallback := preflight.Fallback
	return verificationProfileRecoveryEvidence{
		Version:                   1,
		GeneratedAt:               ISOTimeNow(),
		Status:                    preflight.Status,
		ConfigFound:               preflight.Status != "missing",
		ConfigValid:               false,
		Explanation:               verificationProfileRecoveryExplanation(preflight),
		Error:                     preflight.Error,
		StartDir:                  preflight.StartDir,
		SearchedPaths:             append([]string{}, preflight.SearchedPaths...),
		ProfilePath:               preflight.ProfilePath,
		RepoRoot:                  fallback.RepoRoot,
		FallbackSource:            fallback.Source,
		SuggestedFallbackCommands: append([]verificationFallbackCommand{}, fallback.Commands...),
		Warnings:                  append([]string{}, fallback.Warnings...),
		Suggestion:                verificationProfileRecoverySuggestion(fallback),
		ProfileExample:            `{"version":1,"stages":[{"name":"test","command":"make test"}]}`,
	}, true
}

func verificationProfileRecoveryExplanation(preflight verificationProfilePreflight) string {
	if preflight.Status == "missing" {
		return fmt.Sprintf("%s was not found from %s or its parents.", VerifyProfileFile, defaultString(preflight.StartDir, "."))
	}
	return fmt.Sprintf("%s was found but could not be used: %s", defaultString(preflight.ProfilePath, VerifyProfileFile), defaultString(preflight.Error, "invalid profile"))
}

func verificationProfileRecoverySuggestion(fallback verificationFallbackSummary) string {
	if len(fallback.Commands) == 0 {
		return fmt.Sprintf("No automatic fallback commands were detected at %s; use this repo's documented verification commands or add %s.", defaultString(fallback.RepoRoot, "."), VerifyProfileFile)
	}
	return fmt.Sprintf("Run the suggested fallback commands in order, or add %s at the repo root to define the canonical verification profile.", VerifyProfileFile)
}

func formatVerificationProfileRecovery(preflight verificationProfilePreflight) string {
	switch preflight.Status {
	case "missing", "invalid":
	default:
		return ""
	}
	var builder strings.Builder
	if preflight.Status == "missing" {
		fmt.Fprintf(&builder, "[verify] preflight: %s was not found.\n", VerifyProfileFile)
	} else {
		fmt.Fprintf(&builder, "[verify] preflight: cannot use %s: %s\n", defaultString(preflight.ProfilePath, VerifyProfileFile), preflight.Error)
	}
	if len(preflight.SearchedPaths) > 0 {
		fmt.Fprintf(&builder, "[verify] searched for %s from %s:\n", VerifyProfileFile, defaultString(preflight.StartDir, "."))
		for _, path := range preflight.SearchedPaths {
			fmt.Fprintf(&builder, "[verify]   %s\n", path)
		}
	}
	fallback := preflight.Fallback
	if len(fallback.Commands) > 0 {
		fmt.Fprintf(&builder, "[verify] fallback: detected repo-native checks from %s at %s; run these if continuing without a usable profile:\n", defaultString(fallback.Source, "heuristic"), defaultString(fallback.RepoRoot, "."))
		for _, command := range fallback.Commands {
			fmt.Fprintf(&builder, "[verify]   %s: %s\n", command.Stage, command.Command)
		}
	} else {
		fmt.Fprintf(&builder, "[verify] fallback: no automatic checks were detected at %s; use this repo's documented verification commands.\n", defaultString(fallback.RepoRoot, defaultString(preflight.StartDir, ".")))
	}
	for _, warning := range fallback.Warnings {
		warning = strings.TrimSpace(warning)
		if warning != "" {
			fmt.Fprintf(&builder, "[verify] warning: %s\n", warning)
		}
	}
	fmt.Fprintf(&builder, "[verify] define: add %s at the repo root with canonical stages, for example:\n", VerifyProfileFile)
	fmt.Fprintf(&builder, "[verify]   {\"version\":1,\"stages\":[{\"name\":\"test\",\"command\":\"make test\"}]}\n")
	return builder.String()
}

func normalizeVerificationProfile(profile *verificationProfile) error {
	if profile.Version == 0 {
		profile.Version = 1
	}
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Description = strings.TrimSpace(profile.Description)
	if len(profile.Stages) == 0 {
		return fmt.Errorf("at least one stage is required")
	}
	seen := map[string]bool{}
	for index := range profile.Stages {
		stage := &profile.Stages[index]
		stage.Name = strings.TrimSpace(stage.Name)
		stage.Description = strings.TrimSpace(stage.Description)
		stage.Command = strings.TrimSpace(stage.Command)
		if stage.Name == "" {
			return fmt.Errorf("stage %d is missing name", index+1)
		}
		if stage.Command == "" {
			return fmt.Errorf("stage %q is missing command", stage.Name)
		}
		if seen[stage.Name] {
			return fmt.Errorf("duplicate stage %q", stage.Name)
		}
		seen[stage.Name] = true
	}
	if profile.ChangedScope != nil {
		if err := normalizeVerificationChangedScope(profile.ChangedScope, seen); err != nil {
			return err
		}
	}
	return nil
}

func normalizeVerificationChangedScope(scope *verificationChangedScope, stageNames map[string]bool) error {
	scope.Description = strings.TrimSpace(scope.Description)
	scope.FullCheck.Description = strings.TrimSpace(scope.FullCheck.Description)
	scope.FullCheck.Command = strings.TrimSpace(scope.FullCheck.Command)
	if scope.FullCheck.Command == "" {
		return fmt.Errorf("changed_scope.full_check is missing command")
	}
	seen := map[string]bool{}
	for index := range scope.Paths {
		pathScope := &scope.Paths[index]
		pathScope.Name = strings.TrimSpace(pathScope.Name)
		pathScope.Description = strings.TrimSpace(pathScope.Description)
		if pathScope.Name == "" {
			return fmt.Errorf("changed_scope.paths[%d] is missing name", index)
		}
		if seen[pathScope.Name] {
			return fmt.Errorf("duplicate changed_scope path %q", pathScope.Name)
		}
		seen[pathScope.Name] = true
		pathScope.Patterns = trimNonEmptyStrings(pathScope.Patterns)
		pathScope.Stages = trimNonEmptyStrings(pathScope.Stages)
		pathScope.Checks = trimNonEmptyStrings(pathScope.Checks)
		if len(pathScope.Patterns) == 0 {
			return fmt.Errorf("changed_scope path %q is missing patterns", pathScope.Name)
		}
		if len(pathScope.Stages)+len(pathScope.Checks) == 0 {
			return fmt.Errorf("changed_scope path %q is missing stages or checks", pathScope.Name)
		}
		for _, stage := range pathScope.Stages {
			if !stageNames[stage] {
				return fmt.Errorf("changed_scope path %q references unknown stage %q", pathScope.Name, stage)
			}
		}
	}
	return nil
}

func trimNonEmptyStrings(values []string) []string {
	trimmed := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return trimmed
}

func defaultVerificationRunOptions() verificationRunOptions {
	return verificationRunOptions{OutputLimitBytes: verifyOutputCaptureLimitBytes()}
}

func verifyOutputCaptureLimitBytes() int {
	raw := strings.TrimSpace(os.Getenv(verifyOutputLimitEnv))
	if raw == "" {
		return defaultVerifyOutputLimitBytes
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		return defaultVerifyOutputLimitBytes
	}
	if limit > maxVerifyOutputLimitBytes {
		return maxVerifyOutputLimitBytes
	}
	return limit
}

func normalizeVerifyOutputLimit(limit int) int {
	if limit < 0 {
		return 0
	}
	return limit
}

type boundedOutputCapture struct {
	limit  int
	total  int64
	buffer []byte
	start  int
	size   int
}

func newBoundedOutputCapture(limit int) *boundedOutputCapture {
	return &boundedOutputCapture{limit: normalizeVerifyOutputLimit(limit)}
}

func (capture *boundedOutputCapture) Write(p []byte) (int, error) {
	capture.total += int64(len(p))
	if capture.limit == 0 || len(p) == 0 {
		return len(p), nil
	}
	if capture.buffer == nil {
		capture.buffer = make([]byte, capture.limit)
	}
	if len(p) >= capture.limit {
		copy(capture.buffer, p[len(p)-capture.limit:])
		capture.start = 0
		capture.size = capture.limit
		return len(p), nil
	}

	if overflow := capture.size + len(p) - capture.limit; overflow > 0 {
		capture.start = (capture.start + overflow) % capture.limit
		capture.size -= overflow
	}
	end := (capture.start + capture.size) % capture.limit
	first := min(len(p), capture.limit-end)
	copy(capture.buffer[end:end+first], p[:first])
	copy(capture.buffer, p[first:])
	capture.size += len(p)
	return len(p), nil
}

func (capture *boundedOutputCapture) String() string {
	if capture.size == 0 {
		return ""
	}
	if capture.start+capture.size <= capture.limit {
		return string(capture.buffer[capture.start : capture.start+capture.size])
	}
	tail := make([]byte, capture.size)
	n := copy(tail, capture.buffer[capture.start:])
	copy(tail[n:], capture.buffer[:capture.size-n])
	return string(tail)
}

func (capture *boundedOutputCapture) TotalBytes() int64 {
	return capture.total
}

func (capture *boundedOutputCapture) Truncated() bool {
	return capture.total > int64(capture.size)
}

func verificationStreamWriter(capture io.Writer, stream io.Writer) io.Writer {
	if stream == nil {
		return capture
	}
	return io.MultiWriter(ignoreWriteErrors{Writer: stream}, capture)
}

type ignoreWriteErrors struct {
	Writer io.Writer
}

func (writer ignoreWriteErrors) Write(p []byte) (int, error) {
	_, _ = writer.Writer.Write(p)
	return len(p), nil
}

func printVerificationProfile(profilePath string, profile verificationProfile, jsonOutput bool) error {
	if jsonOutput {
		payload := struct {
			ProfilePath string              `json:"profile_path"`
			Profile     verificationProfile `json:"profile"`
		}{ProfilePath: profilePath, Profile: profile}
		return writeIndentedJSON(payload)
	}
	fmt.Fprintf(os.Stdout, "[verify] profile: %s (%s)\n", defaultString(profile.Name, "repository"), profilePath)
	for _, stage := range profile.Stages {
		fmt.Fprintf(os.Stdout, "[verify] %s: %s\n", stage.Name, stage.Command)
	}
	if profile.ChangedScope != nil {
		fmt.Fprintf(os.Stdout, "[verify] changed-scope full-check: %s\n", profile.ChangedScope.FullCheck.Command)
		for _, pathScope := range profile.ChangedScope.Paths {
			targets := append([]string{}, pathScope.Stages...)
			targets = append(targets, pathScope.Checks...)
			fmt.Fprintf(os.Stdout, "[verify] changed-scope %s: %s -> %s\n", pathScope.Name, strings.Join(pathScope.Patterns, ", "), strings.Join(targets, "; "))
		}
	}
	return nil
}

func runVerificationProfile(repoRoot string, profilePath string, profile verificationProfile) (verificationEvidence, error) {
	return runVerificationProfileWithOptions(repoRoot, profilePath, profile, defaultVerificationRunOptions())
}

func runVerificationProfileWithOptions(repoRoot string, profilePath string, profile verificationProfile, options verificationRunOptions) (verificationEvidence, error) {
	started := time.Now()
	report := verificationEvidence{
		Version:     1,
		GeneratedAt: ISOTimeNow(),
		RepoRoot:    repoRoot,
		ProfilePath: profilePath,
		Profile:     profile,
		Passed:      true,
	}
	for _, stage := range profile.Stages {
		result, err := runVerificationStageWithOptions(repoRoot, stage, options)
		if err != nil {
			return verificationEvidence{}, err
		}
		report.Stages = append(report.Stages, result)
		if result.ExitCode != 0 {
			report.Passed = false
			report.FailedStages = append(report.FailedStages, stage.Name)
		}
	}
	report.DurationMillis = time.Since(started).Milliseconds()
	return report, nil
}

func runVerificationStage(repoRoot string, stage verificationStageProfile) (verificationCommandEvidence, error) {
	return runVerificationStageWithOptions(repoRoot, stage, defaultVerificationRunOptions())
}

func verificationShellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "sh", []string{"-c", command}
}

func runVerificationStageWithOptions(repoRoot string, stage verificationStageProfile, options verificationRunOptions) (verificationCommandEvidence, error) {
	started := time.Now()
	shell, shellArgs := verificationShellCommand(stage.Command)
	cmd := exec.Command(shell, shellArgs...)
	cmd.Dir = repoRoot
	cmd.Env = verificationCommandEnv(cmd.Environ())

	limit := normalizeVerifyOutputLimit(options.OutputLimitBytes)
	stdoutCapture := newBoundedOutputCapture(limit)
	stderrCapture := newBoundedOutputCapture(limit)
	cmd.Stdout = verificationStreamWriter(stdoutCapture, options.Stdout)
	cmd.Stderr = verificationStreamWriter(stderrCapture, options.Stderr)

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return verificationCommandEvidence{}, err
		}
	}
	output := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(stdoutCapture.String()), strings.TrimSpace(stderrCapture.String())}, "\n"))
	status := "passed"
	if exitCode != 0 {
		status = "failed"
	}
	stdoutBytes := stdoutCapture.TotalBytes()
	stderrBytes := stderrCapture.TotalBytes()
	return verificationCommandEvidence{
		Name:             stage.Name,
		Description:      stage.Description,
		Command:          stage.Command,
		Status:           status,
		ExitCode:         exitCode,
		DurationMillis:   time.Since(started).Milliseconds(),
		Output:           output,
		OutputBytes:      stdoutBytes + stderrBytes,
		OutputTruncated:  stdoutCapture.Truncated() || stderrCapture.Truncated(),
		OutputLimitBytes: limit,
		StdoutBytes:      stdoutBytes,
		StdoutTruncated:  stdoutCapture.Truncated(),
		StderrBytes:      stderrBytes,
		StderrTruncated:  stderrCapture.Truncated(),
	}, nil
}

func verificationCommandEnv(environ []string) []string {
	cleaned := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if ok && isVerificationControlEnvKey(key) {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

func isVerificationControlEnvKey(key string) bool {
	return isMakeControlEnvKey(key) || envKeyEqual(key, "GOFLAGS")
}

func isMakeControlEnvKey(key string) bool {
	for _, makeKey := range []string{"MAKEFLAGS", "MFLAGS", "GNUMAKEFLAGS", "MAKEFILES"} {
		if envKeyEqual(key, makeKey) {
			return true
		}
	}
	return false
}

func envKeyEqual(key string, want string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(key, want)
	}
	return key == want
}

func printVerificationEvidence(report verificationEvidence, jsonOutput bool) error {
	if jsonOutput {
		return writeIndentedJSON(report)
	}
	fmt.Fprintf(os.Stdout, "[verify] profile: %s (%s)\n", defaultString(report.Profile.Name, "repository"), report.ProfilePath)
	for _, stage := range report.Stages {
		fmt.Fprintf(os.Stdout, "[verify] %s: %s (%dms)\n", stage.Name, stage.Status, stage.DurationMillis)
		if stage.Status != "passed" {
			if stage.OutputTruncated {
				fmt.Fprintf(os.Stdout, "[verify]   output truncated to last %d bytes per stream (%d bytes total)\n", stage.OutputLimitBytes, stage.OutputBytes)
			}
			if strings.TrimSpace(stage.Output) != "" {
				for _, line := range strings.Split(stage.Output, "\n") {
					fmt.Fprintf(os.Stdout, "[verify]   %s\n", line)
				}
			}
		}
	}
	if report.Passed {
		fmt.Fprintf(os.Stdout, "[verify] passed (%dms)\n", report.DurationMillis)
	} else {
		fmt.Fprintf(os.Stdout, "[verify] failed: %s (%dms)\n", strings.Join(report.FailedStages, ", "), report.DurationMillis)
	}
	return nil
}

func writeIndentedJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
