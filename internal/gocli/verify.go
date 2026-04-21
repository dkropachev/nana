package gocli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const VerifyProfileFile = managedVerificationPlanFile

const (
	defaultVerifyOutputLimitBytes = 64 * 1024
	maxVerifyOutputLimitBytes     = 4 * 1024 * 1024
	verifyOutputLimitEnv          = "NANA_VERIFY_OUTPUT_LIMIT_BYTES"
)

const VerifyHelp = `nana verify - Run the managed verification plan for an onboarded repo

Usage:
  nana verify [--json]
  nana verify --profile [--json]

Options:
  --json       Emit machine-readable JSON evidence.
  --profile    Print the detected verification profile without running commands.

Profile:
  ` + VerifyProfileFile + ` defines the canonical sequential stages for this repo.
  The file lives in Nana-managed repo state under ~/.nana/work/repos/...
  and is materialized by ` + "`nana repo onboard`" + `.
  Optional changed_scope guidance may map changed files to targeted checks.
  A changed_scope profile must keep a full_check command as the fallback.
  ` + "`nana verify`" + ` requires the current repo to be onboarded first.
`

type verificationProfile = managedVerificationPlan
type verificationStageProfile = managedVerificationStage
type verificationChangedScope = managedVerificationChangedScope
type verificationChangedScopeFullCheck = managedVerificationChangedScopeFullCheck
type verificationChangedScopePath = managedVerificationChangedScopePath

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
	RepoRoot      string
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
	Stage   string
	Command string
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
		printVerificationProfileRecovery(preflight)
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
	repoRoot, profilePath, ok, searchedPaths, repoErr := findVerificationProfileWithSearch(cwd)
	preflight := verificationProfilePreflight{
		Status:        "valid",
		StartDir:      absolutePathOrInput(cwd),
		RepoRoot:      repoRoot,
		SearchedPaths: searchedPaths,
		ProfilePath:   profilePath,
	}
	if repoErr != nil {
		preflight.Status = "missing"
		preflight.Error = repoErr.Error()
		return "", "", verificationProfile{}, preflight, repoErr
	}
	if !ok {
		preflight.Status = "missing"
		err := fmt.Errorf("managed verification plan not found at %s", profilePath)
		preflight.Error = err.Error()
		return "", "", verificationProfile{}, preflight, err
	}
	content, err := os.ReadFile(profilePath)
	if err != nil {
		profileErr := err
		err := fmt.Errorf("invalid %s: %w", profilePath, profileErr)
		preflight.Status = "invalid"
		preflight.Error = profileErr.Error()
		return "", "", verificationProfile{}, preflight, err
	}
	profile, err := decodeVerificationProfile(content)
	if err != nil {
		profileErr := err
		err := fmt.Errorf("invalid %s: %w", profilePath, profileErr)
		preflight.Status = "invalid"
		preflight.Error = profileErr.Error()
		return "", "", verificationProfile{}, preflight, err
	}
	return repoRoot, profilePath, profile, preflight, nil
}

func decodeVerificationProfile(content []byte) (verificationProfile, error) {
	fields, err := parseJSONObject(content)
	if err != nil {
		return verificationProfile{}, err
	}
	if err := validateVerificationProfileVersionField(fields); err != nil {
		return verificationProfile{}, err
	}
	var profile verificationProfile
	if err := json.Unmarshal(content, &profile); err != nil {
		return verificationProfile{}, err
	}
	if err := normalizeVerificationProfile(&profile); err != nil {
		return verificationProfile{}, err
	}
	return profile, nil
}

func validateVerificationProfileVersionField(fields map[string]json.RawMessage) error {
	raw, ok := fields["version"]
	if !ok {
		return nil
	}
	value, err := requiredInteger(raw, "version")
	if err != nil {
		return err
	}
	if value < 1 {
		return fmt.Errorf("version must be >= 1")
	}
	return nil
}

func findVerificationProfile(cwd string) (string, string, bool) {
	repoRoot, profilePath, ok, _, _ := findVerificationProfileWithSearch(cwd)
	return repoRoot, profilePath, ok
}

func findVerificationProfileWithSearch(cwd string) (string, string, bool, []string, error) {
	repoRoot, profilePath, err := managedVerificationPlanPathForCWD(cwd)
	if err != nil {
		return "", "", false, nil, fmt.Errorf("verify requires a git-backed repo: %w", err)
	}
	if info, statErr := os.Stat(profilePath); statErr == nil && !info.IsDir() {
		return repoRoot, profilePath, true, []string{profilePath}, nil
	}
	return repoRoot, profilePath, false, []string{profilePath}, nil
}

func absolutePathOrInput(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func printVerificationProfileRecovery(preflight verificationProfilePreflight) {
	message := formatVerificationProfileRecovery(preflight)
	if strings.TrimSpace(message) == "" {
		return
	}
	fmt.Fprint(os.Stderr, message)
}

func formatVerificationProfileRecovery(preflight verificationProfilePreflight) string {
	switch preflight.Status {
	case "missing", "invalid":
	default:
		return ""
	}
	var builder strings.Builder
	if preflight.Status == "missing" {
		if strings.TrimSpace(preflight.ProfilePath) != "" {
			fmt.Fprintf(&builder, "[verify] preflight: managed verification plan was not found at %s.\n", preflight.ProfilePath)
		} else {
			fmt.Fprintf(&builder, "[verify] preflight: managed verification plan was not found.\n")
		}
	} else {
		fmt.Fprintf(&builder, "[verify] preflight: cannot use managed verification plan %s: %s\n", defaultString(preflight.ProfilePath, VerifyProfileFile), preflight.Error)
	}
	if strings.TrimSpace(preflight.RepoRoot) != "" {
		fmt.Fprintf(&builder, "[verify] repo root: %s\n", preflight.RepoRoot)
	}
	if len(preflight.SearchedPaths) > 0 {
		fmt.Fprintf(&builder, "[verify] expected managed verification plan path(s):\n")
		for _, path := range preflight.SearchedPaths {
			fmt.Fprintf(&builder, "[verify]   %s\n", path)
		}
	}
	if strings.TrimSpace(preflight.RepoRoot) != "" {
		fmt.Fprintf(&builder, "[verify] run: nana repo onboard --repo %s\n", preflight.RepoRoot)
	} else {
		fmt.Fprintf(&builder, "[verify] run: nana repo onboard\n")
	}
	return builder.String()
}

func normalizeVerificationProfile(profile *verificationProfile) error {
	if profile.Version == 0 {
		profile.Version = 1
	} else if profile.Version < 1 {
		return fmt.Errorf("version must be >= 1")
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
	executed, err := executeVerificationStages(repoRoot, verificationExecutionStagesFromProfile(profile), verificationExecutionOptions{
		OutputLimitBytes: options.OutputLimitBytes,
		Stdout:           options.Stdout,
		Stderr:           options.Stderr,
		SanitizeEnv:      true,
		DedupeCommands:   false,
	})
	if err != nil {
		return verificationEvidence{}, err
	}
	for _, stage := range executed {
		if len(stage.Commands) == 0 {
			continue
		}
		result := verificationCommandEvidenceFromExecution(stage)
		report.Stages = append(report.Stages, result)
		if result.ExitCode != 0 {
			report.Passed = false
			report.FailedStages = append(report.FailedStages, result.Name)
		}
	}
	report.DurationMillis = time.Since(started).Milliseconds()
	return report, nil
}

func runVerificationStage(repoRoot string, stage verificationStageProfile) (verificationCommandEvidence, error) {
	return runVerificationStageWithOptions(repoRoot, stage, defaultVerificationRunOptions())
}

func runVerificationStageWithOptions(repoRoot string, stage verificationStageProfile, options verificationRunOptions) (verificationCommandEvidence, error) {
	executed, err := executeVerificationStages(repoRoot, []verificationExecutionStage{{
		Name:        stage.Name,
		Description: stage.Description,
		Commands: []verificationExecutionCommand{{
			Command: stage.Command,
		}},
	}}, verificationExecutionOptions{
		OutputLimitBytes: options.OutputLimitBytes,
		Stdout:           options.Stdout,
		Stderr:           options.Stderr,
		SanitizeEnv:      true,
		DedupeCommands:   false,
	})
	if err != nil {
		return verificationCommandEvidence{}, err
	}
	if len(executed) == 0 || len(executed[0].Commands) == 0 {
		return verificationCommandEvidence{}, fmt.Errorf("verification stage %q did not execute", stage.Name)
	}
	return verificationCommandEvidenceFromExecution(executed[0]), nil
}

func verificationCommandEvidenceFromExecution(stage verificationExecutionStageResult) verificationCommandEvidence {
	command := stage.Commands[0]
	return verificationCommandEvidence{
		Name:             stage.Name,
		Description:      stage.Description,
		Command:          command.Command,
		Status:           stage.Status,
		ExitCode:         command.ExitCode,
		DurationMillis:   stage.DurationMillis,
		Output:           command.Output,
		OutputBytes:      command.OutputBytes,
		OutputTruncated:  command.OutputTruncated,
		OutputLimitBytes: command.OutputLimitBytes,
		StdoutBytes:      command.StdoutBytes,
		StdoutTruncated:  command.StdoutTruncated,
		StderrBytes:      command.StderrBytes,
		StderrTruncated:  command.StderrTruncated,
	}
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
