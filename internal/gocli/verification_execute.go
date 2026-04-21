package gocli

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type verificationExecutionCommand struct {
	Command string
}

type verificationExecutionStage struct {
	Name        string
	Description string
	Commands    []verificationExecutionCommand
}

type verificationExecutionCommandResult struct {
	Command          string
	ExitCode         int
	Output           string
	Cached           bool
	DurationMillis   int64
	OutputBytes      int64
	OutputTruncated  bool
	OutputLimitBytes int
	StdoutBytes      int64
	StdoutTruncated  bool
	StderrBytes      int64
	StderrTruncated  bool
}

type verificationExecutionStageResult struct {
	Name           string
	Description    string
	Status         string
	DurationMillis int64
	Commands       []verificationExecutionCommandResult
}

type verificationExecutionOptions struct {
	OutputLimitBytes int
	UnlimitedOutput  bool
	Stdout           io.Writer
	Stderr           io.Writer
	SanitizeEnv      bool
	DedupeCommands   bool
}

type verificationOutputCapture interface {
	io.Writer
	String() string
	TotalBytes() int64
	Truncated() bool
	Limit() int
}

func executeVerificationStages(repoRoot string, stages []verificationExecutionStage, options verificationExecutionOptions) ([]verificationExecutionStageResult, error) {
	results := make([]verificationExecutionStageResult, 0, len(stages))
	cache := map[string]verificationExecutionCommandResult{}
	for _, stage := range stages {
		started := time.Now()
		stageResult := verificationExecutionStageResult{
			Name:        stage.Name,
			Description: stage.Description,
			Status:      "skipped",
		}
		if len(stage.Commands) == 0 {
			results = append(results, stageResult)
			continue
		}
		stageResult.Status = "passed"
		for _, command := range stage.Commands {
			result, ok := cache[command.Command]
			if ok {
				result.Cached = true
			} else {
				executed, err := executeVerificationCommand(repoRoot, command.Command, options)
				if err != nil {
					return nil, err
				}
				result = executed
				if options.DedupeCommands {
					cache[command.Command] = result
				}
			}
			stageResult.Commands = append(stageResult.Commands, result)
			if result.ExitCode != 0 {
				stageResult.Status = "failed"
				break
			}
		}
		stageResult.DurationMillis = time.Since(started).Milliseconds()
		results = append(results, stageResult)
	}
	return results, nil
}

func executeVerificationCommand(repoRoot string, command string, options verificationExecutionOptions) (verificationExecutionCommandResult, error) {
	started := time.Now()
	shell, shellArgs := verificationShellCommand(command)
	cmd := exec.Command(shell, shellArgs...)
	cmd.Dir = repoRoot
	if options.SanitizeEnv {
		cmd.Env = verificationCommandEnv(cmd.Environ())
	}

	stdoutCapture := newVerificationOutputCapture(options.OutputLimitBytes, options.UnlimitedOutput)
	stderrCapture := newVerificationOutputCapture(options.OutputLimitBytes, options.UnlimitedOutput)
	cmd.Stdout = verificationStreamWriter(stdoutCapture, options.Stdout)
	cmd.Stderr = verificationStreamWriter(stderrCapture, options.Stderr)

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return verificationExecutionCommandResult{}, err
		}
	}

	output := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(stdoutCapture.String()),
		strings.TrimSpace(stderrCapture.String()),
	}, "\n"))
	return verificationExecutionCommandResult{
		Command:          command,
		ExitCode:         exitCode,
		Output:           output,
		DurationMillis:   time.Since(started).Milliseconds(),
		OutputBytes:      stdoutCapture.TotalBytes() + stderrCapture.TotalBytes(),
		OutputTruncated:  stdoutCapture.Truncated() || stderrCapture.Truncated(),
		OutputLimitBytes: stdoutCapture.Limit(),
		StdoutBytes:      stdoutCapture.TotalBytes(),
		StdoutTruncated:  stdoutCapture.Truncated(),
		StderrBytes:      stderrCapture.TotalBytes(),
		StderrTruncated:  stderrCapture.Truncated(),
	}, nil
}

func verificationExecutionStagesFromProfile(profile verificationProfile) []verificationExecutionStage {
	stages := make([]verificationExecutionStage, 0, len(profile.Stages))
	for _, stage := range profile.Stages {
		stages = append(stages, verificationExecutionStage{
			Name:        stage.Name,
			Description: stage.Description,
			Commands: []verificationExecutionCommand{{
				Command: stage.Command,
			}},
		})
	}
	return stages
}

func verificationExecutionStagesFromPlan(plan githubVerificationPlan, includeIntegration bool) []verificationExecutionStage {
	stages := []verificationExecutionStage{
		{
			Name:     "lint",
			Commands: verificationExecutionCommandsFromStrings(plan.Lint),
		},
		{
			Name:     "compile",
			Commands: verificationExecutionCommandsFromStrings(plan.Compile),
		},
		{
			Name:     "unit",
			Commands: verificationExecutionCommandsFromStrings(plan.Unit),
		},
	}
	if includeIntegration {
		stages = append(stages, verificationExecutionStage{
			Name:     "integration",
			Commands: verificationExecutionCommandsFromStrings(plan.Integration),
		})
	}
	return stages
}

func verificationExecutionCommandsFromStrings(commands []string) []verificationExecutionCommand {
	result := make([]verificationExecutionCommand, 0, len(commands))
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		result = append(result, verificationExecutionCommand{Command: command})
	}
	return result
}

func verificationShellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "sh", []string{"-c", command}
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

func (capture *boundedOutputCapture) Limit() int {
	return capture.limit
}

type unboundedOutputCapture struct {
	buffer bytes.Buffer
	total  int64
}

func newUnboundedOutputCapture() *unboundedOutputCapture {
	return &unboundedOutputCapture{}
}

func (capture *unboundedOutputCapture) Write(p []byte) (int, error) {
	capture.total += int64(len(p))
	return capture.buffer.Write(p)
}

func (capture *unboundedOutputCapture) String() string {
	return capture.buffer.String()
}

func (capture *unboundedOutputCapture) TotalBytes() int64 {
	return capture.total
}

func (capture *unboundedOutputCapture) Truncated() bool {
	return false
}

func (capture *unboundedOutputCapture) Limit() int {
	return 0
}

func newVerificationOutputCapture(limit int, unlimited bool) verificationOutputCapture {
	if unlimited {
		return newUnboundedOutputCapture()
	}
	return newBoundedOutputCapture(limit)
}

func normalizeVerifyOutputLimit(limit int) int {
	if limit < 0 {
		return 0
	}
	return limit
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
