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

func Verify(cwd string, args []string) error {
	options, err := parseVerifyOptions(args)
	if err != nil {
		return err
	}
	if options.Help {
		return nil
	}
	repoRoot, profilePath, profile, err := loadVerificationProfile(cwd)
	if err != nil {
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
	repoRoot, profilePath, ok := findVerificationProfile(cwd)
	if !ok {
		return "", "", verificationProfile{}, fmt.Errorf("%s not found from %s or its parents", VerifyProfileFile, cwd)
	}
	var profile verificationProfile
	if err := readGithubJSON(profilePath, &profile); err != nil {
		return "", "", verificationProfile{}, err
	}
	if err := normalizeVerificationProfile(&profile); err != nil {
		return "", "", verificationProfile{}, fmt.Errorf("invalid %s: %w", profilePath, err)
	}
	return repoRoot, profilePath, profile, nil
}

func findVerificationProfile(cwd string) (string, string, bool) {
	current, err := filepath.Abs(cwd)
	if err != nil {
		current = cwd
	}
	for {
		candidate := filepath.Join(current, VerifyProfileFile)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return current, candidate, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", false
		}
		current = parent
	}
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
