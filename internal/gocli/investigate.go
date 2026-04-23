package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const InvestigateHelp = `nana investigate - Source-backed investigation with validator enforcement

Usage:
  nana investigate <user-input> [-- codex-args...]
  nana investigate --resume <run-id>|--last [-- codex-args...]
  nana investigate onboard
  nana investigate doctor
  nana investigate help

Behavior:
  - ` + "`nana investigate <user-input>`" + ` runs an investigator session, persists a JSON report, and loops with a validator until the report is accepted or the run fails
  - ` + "`nana investigate onboard`" + ` bootstraps the dedicated investigate Codex config
  - ` + "`nana investigate doctor`" + ` asks Codex to verify that configured MCPs in the investigate config are usable
  - top-level investigate is generic; GitHub issue preflight remains under ` + "`nana issue investigate <github-issue-url>`" + `

Report contract:
  - final report is JSON only
  - overall_status must be one of REFUTED | CONFIRMED | PARTIALLY_CONFIRMED
  - every claim must include proof links
  - documentation is supplementary only and cannot be a primary proof

Storage:
  - runs: .nana/logs/investigate/<run-id>
  - config: <resolved investigate CODEX_HOME>/config.toml
  - cached MCP status: <resolved investigate CODEX_HOME>/investigate-mcp-status.json
`

const (
	investigateStatusRefuted            = "REFUTED"
	investigateStatusConfirmed          = "CONFIRMED"
	investigateStatusPartiallyConfirmed = "PARTIALLY_CONFIRMED"

	investigateMaxRounds = 3

	investigateRunStatusRunning                  = "running"
	investigateRunStatusCompleted                = "completed"
	investigateRunStatusFailedReadiness          = "failed_readiness"
	investigateRunStatusFailedReportParse        = "failed_report_parse"
	investigateRunStatusFailedValidatorParse     = "failed_validator_parse"
	investigateRunStatusFailedValidatorExhausted = "failed_validator_exhausted"
	investigateRunStatusFailedExecutor           = "failed_executor"

	investigateConfigBlockHeader = "# nana investigate (NANA) Configuration"
	investigateConfigBlockEnd    = "# End nana investigate"
)

type investigateMCPServerStatus struct {
	ServerName string `json:"server_name"`
	OK         bool   `json:"ok"`
	Summary    string `json:"summary"`
}

type investigateMCPStatus struct {
	Version           int                          `json:"version"`
	CheckedAt         string                       `json:"checked_at"`
	CodexHome         string                       `json:"codex_home"`
	ConfigPath        string                       `json:"config_path"`
	ConfiguredServers []string                     `json:"configured_servers"`
	Servers           []investigateMCPServerStatus `json:"servers"`
	AllOK             bool                         `json:"all_ok"`
	ProbeSummary      string                       `json:"probe_summary,omitempty"`
	MCPListRaw        string                       `json:"mcp_list_raw,omitempty"`
}

type investigateManifest struct {
	Version         int                     `json:"version"`
	RunID           string                  `json:"run_id"`
	CreatedAt       string                  `json:"created_at"`
	UpdatedAt       string                  `json:"updated_at"`
	CompletedAt     string                  `json:"completed_at,omitempty"`
	Status          string                  `json:"status"`
	Query           string                  `json:"query"`
	WorkspaceRoot   string                  `json:"workspace_root"`
	CodexHome       string                  `json:"codex_home"`
	MCPStatusPath   string                  `json:"mcp_status_path"`
	RunDir          string                  `json:"run_dir"`
	FinalReportPath string                  `json:"final_report_path,omitempty"`
	LastError       string                  `json:"last_error,omitempty"`
	PauseReason     string                  `json:"pause_reason,omitempty"`
	PauseUntil      string                  `json:"pause_until,omitempty"`
	MaxRounds       int                     `json:"max_rounds"`
	AcceptedRound   int                     `json:"accepted_round,omitempty"`
	Rounds          []investigateRoundState `json:"rounds,omitempty"`
}

type investigateRoundState struct {
	Round               int    `json:"round"`
	InvestigatorPrompt  string `json:"investigator_prompt"`
	InvestigatorStdout  string `json:"investigator_stdout"`
	InvestigatorStderr  string `json:"investigator_stderr"`
	ReportPath          string `json:"report_path,omitempty"`
	ValidatorPrompt     string `json:"validator_prompt,omitempty"`
	ValidatorStdout     string `json:"validator_stdout,omitempty"`
	ValidatorStderr     string `json:"validator_stderr,omitempty"`
	ValidatorResultPath string `json:"validator_result_path,omitempty"`
	Status              string `json:"status"`
}

type investigateReport struct {
	OverallStatus              string             `json:"overall_status"`
	OverallShortExplanation    string             `json:"overall_short_explanation"`
	OverallDetailedExplanation string             `json:"overall_detailed_explanation"`
	OverallProofs              []investigateProof `json:"overall_proofs"`
	Issues                     []investigateIssue `json:"issues"`
}

type investigateIssue struct {
	ID                  string             `json:"id"`
	ShortExplanation    string             `json:"short_explanation"`
	DetailedExplanation string             `json:"detailed_explanation"`
	Proofs              []investigateProof `json:"proofs"`
}

type investigateProof struct {
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Link        string `json:"link"`
	WhyItProves string `json:"why_it_proves"`
	IsPrimary   bool   `json:"is_primary"`
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
}

type investigateViolation struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type investigateValidatorResult struct {
	Accepted   bool                   `json:"accepted"`
	Summary    string                 `json:"summary"`
	Violations []investigateViolation `json:"violations"`
}

type investigateExecutionResult struct {
	Stdout string
	Stderr string
}

type investigateRunOptions struct {
	Query       string
	CodexArgs   []string
	ResumeRunID string
	ResumeLast  bool
	RunID       string
}

func Investigate(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, InvestigateHelp)
		return nil
	}

	switch args[0] {
	case "help":
		fmt.Fprint(os.Stdout, InvestigateHelp)
		return nil
	case "onboard":
		if len(args) > 1 && isHelpToken(args[1]) {
			fmt.Fprint(os.Stdout, InvestigateHelp)
			return nil
		}
		return investigateOnboard(cwd, args[1:])
	case "doctor":
		if len(args) > 1 && isHelpToken(args[1]) {
			fmt.Fprint(os.Stdout, InvestigateHelp)
			return nil
		}
		return investigateDoctor(cwd)
	case "sources":
		return fmt.Errorf("nana investigate sources has been removed. Configure MCPs in the dedicated investigate CODEX_HOME and let `nana investigate doctor` probe them")
	}

	options, err := parseInvestigateRunArgs(args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(options.ResumeRunID) != "" || options.ResumeLast {
		return resumeInvestigation(cwd, options)
	}
	return runInvestigationWithOptions(cwd, options)
}

func MaybeHandleInvestigateHelp(command string, args []string) bool {
	if command != "investigate" {
		return false
	}
	if len(args) == 1 {
		return false
	}
	if isHelpToken(args[1]) || args[1] == "help" {
		fmt.Fprint(os.Stdout, InvestigateHelp)
		return true
	}
	return false
}

func parseInvestigateRunArgs(args []string) (investigateRunOptions, error) {
	options := investigateRunOptions{}
	taskParts := []string{}
	seenSeparator := false
	for index := 0; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			seenSeparator = true
			options.CodexArgs = append(options.CodexArgs, args[index+1:]...)
			break
		}
		if seenSeparator {
			options.CodexArgs = append(options.CodexArgs, token)
			continue
		}
		switch {
		case token == "--run-id":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return investigateRunOptions{}, fmt.Errorf("missing value after --run-id\n\n%s", InvestigateHelp)
			}
			options.RunID = strings.TrimSpace(args[index+1])
			index++
		case strings.HasPrefix(token, "--run-id="):
			options.RunID = strings.TrimSpace(strings.TrimPrefix(token, "--run-id="))
		case token == "--resume":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return investigateRunOptions{}, fmt.Errorf("missing value after --resume\n\n%s", InvestigateHelp)
			}
			options.ResumeRunID = strings.TrimSpace(args[index+1])
			index++
		case strings.HasPrefix(token, "--resume="):
			options.ResumeRunID = strings.TrimSpace(strings.TrimPrefix(token, "--resume="))
		case token == "--last":
			options.ResumeLast = true
		default:
			taskParts = append(taskParts, token)
		}
	}
	if strings.TrimSpace(options.ResumeRunID) != "" && options.ResumeLast {
		return investigateRunOptions{}, fmt.Errorf("use either --resume <run-id> or --last, not both\n\n%s", InvestigateHelp)
	}
	options.Query = strings.TrimSpace(strings.Join(taskParts, " "))
	if strings.TrimSpace(options.ResumeRunID) != "" || options.ResumeLast {
		if strings.TrimSpace(options.RunID) != "" {
			return investigateRunOptions{}, fmt.Errorf("resume does not accept --run-id\n\n%s", InvestigateHelp)
		}
		if options.Query != "" {
			return investigateRunOptions{}, fmt.Errorf("resume does not accept a new investigation query\n\n%s", InvestigateHelp)
		}
		return options, nil
	}
	if options.Query == "" {
		return investigateRunOptions{}, fmt.Errorf("Usage: nana investigate <user-input> [-- codex-args...]\n\n%s", InvestigateHelp)
	}
	return options, nil
}

func investigateOnboard(cwd string, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown investigate onboard option: %s\n\n%s", args[0], InvestigateHelp)
	}
	codexHome := ResolveInvestigateCodexHome(cwd)
	configPath := InvestigateCodexConfigPath(cwd)
	if err := writeInvestigateConfig(configPath); err != nil {
		return err
	}
	status := investigateMCPStatus{
		Version:           1,
		CheckedAt:         "",
		CodexHome:         codexHome,
		ConfigPath:        configPath,
		ConfiguredServers: []string{},
		Servers:           []investigateMCPServerStatus{},
		AllOK:             true,
		ProbeSummary:      "no MCPs checked yet",
	}

	fmt.Fprintln(os.Stdout, "nana investigate onboard")
	fmt.Fprintln(os.Stdout, "========================")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Investigate CODEX_HOME: %s\n", codexHome)
	fmt.Fprintf(os.Stdout, "Investigate config: %s\n", configPath)
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "No MCP servers are assumed or predeclared.")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Next steps:")
	fmt.Fprintln(os.Stdout, "  1. Configure any MCP servers you want in the dedicated investigate config or via `codex mcp ...` using this investigate CODEX_HOME")
	fmt.Fprintln(os.Stdout, "  2. Run `nana investigate doctor` so Codex can probe the configured MCPs")
	_ = writeInvestigateMCPStatus(investigateMCPStatusPath(codexHome), status)
	return nil
}

func investigateDoctor(cwd string) error {
	codexHome := ResolveInvestigateCodexHome(cwd)
	configPath := InvestigateCodexConfigPath(cwd)
	status, err := probeInvestigateMCPs(cwd, codexHome)
	if err != nil {
		return err
	}
	if err := writeInvestigateMCPStatus(investigateMCPStatusPath(codexHome), status); err != nil {
		return err
	}

	fmt.Fprintln(os.Stdout, "nana investigate doctor")
	fmt.Fprintln(os.Stdout, "=======================")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Investigate CODEX_HOME: %s\n", codexHome)
	fmt.Fprintf(os.Stdout, "Investigate config: %s\n", configPath)
	fmt.Fprintln(os.Stdout)

	if len(status.ConfiguredServers) == 0 {
		fmt.Fprintln(os.Stdout, "No MCP servers configured in the investigate config.")
		return nil
	}
	for _, server := range status.Servers {
		icon := "[OK]"
		if !server.OK {
			icon = "[!!]"
		}
		fmt.Fprintf(os.Stdout, "%s %s: %s\n", icon, server.ServerName, defaultString(strings.TrimSpace(server.Summary), "(no summary)"))
	}
	fmt.Fprintln(os.Stdout)
	if status.AllOK {
		fmt.Fprintln(os.Stdout, "All configured investigate MCPs are working.")
		return nil
	}
	fmt.Fprintln(os.Stdout, "One or more configured investigate MCPs are not working.")
	fmt.Fprintln(os.Stdout, "Fix the MCP configuration and rerun `nana investigate doctor`.")
	return nil
}

func runInvestigation(cwd string, query string, codexArgs []string) error {
	return runInvestigationWithOptions(cwd, investigateRunOptions{
		Query:     query,
		CodexArgs: codexArgs,
	})
}

func runInvestigationWithOptions(cwd string, options investigateRunOptions) error {
	workspaceRoot := resolveInvestigateWorkspaceRoot(cwd)
	codexHome := ResolveInvestigateCodexHome(cwd)
	runID := strings.TrimSpace(options.RunID)
	if runID == "" {
		runID = fmt.Sprintf("investigate-%d", time.Now().UnixNano())
	}
	runDir := filepath.Join(workspaceRoot, ".nana", "logs", "investigate", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}

	manifest := investigateManifest{
		Version:       1,
		RunID:         runID,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		Status:        investigateRunStatusRunning,
		Query:         options.Query,
		WorkspaceRoot: workspaceRoot,
		CodexHome:     codexHome,
		MCPStatusPath: filepath.Join(runDir, "mcp-status.json"),
		RunDir:        runDir,
		MaxRounds:     investigateMaxRounds,
		Rounds:        []investigateRoundState{},
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	return executeInvestigationRun(manifestPath, &manifest, options.CodexArgs)
}

func resumeInvestigation(cwd string, options investigateRunOptions) error {
	manifestPath, err := resolveInvestigateRunManifestPath(resolveInvestigateWorkspaceRoot(cwd), options.ResumeRunID, options.ResumeLast)
	if err != nil {
		return err
	}
	manifest, err := readInvestigateRunManifest(manifestPath)
	if err != nil {
		return err
	}
	if manifest.Status == investigateRunStatusCompleted {
		return fmt.Errorf("investigate run %s is already completed", manifest.RunID)
	}
	return executeInvestigationRun(manifestPath, &manifest, options.CodexArgs)
}

func resolveInvestigateRunManifestPath(workspaceRoot string, runID string, useLast bool) (string, error) {
	root := filepath.Join(workspaceRoot, ".nana", "logs", "investigate")
	if strings.TrimSpace(runID) != "" {
		path := filepath.Join(root, strings.TrimSpace(runID), "manifest.json")
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("investigate run %s not found", runID)
			}
			return "", err
		}
		return path, nil
	}
	if !useLast {
		return "", fmt.Errorf("investigate resume requires --resume <run-id> or --last")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no prior investigate runs found in %s", root)
		}
		return "", err
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	candidates := []candidate{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "manifest.json")
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{path: path, modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no prior investigate runs found in %s", root)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime.After(candidates[j].modTime) })
	return candidates[0].path, nil
}

func readInvestigateRunManifest(path string) (investigateManifest, error) {
	var manifest investigateManifest
	if err := readGithubJSON(path, &manifest); err != nil {
		return investigateManifest{}, err
	}
	return manifest, nil
}

func persistInvestigateManifest(manifestPath string, manifest investigateManifest) error {
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	return syncCanonicalInvestigationTask(manifest)
}

func executeInvestigationRun(manifestPath string, manifest *investigateManifest, codexArgs []string) error {
	writeFailure := func(status string, runErr error) error {
		if pauseErr, ok := isCodexRateLimitPauseError(runErr); ok {
			manifest.Status = "paused"
			manifest.PauseReason = strings.TrimSpace(pauseErr.Info.Reason)
			manifest.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
			manifest.LastError = codexPauseInfoMessage(pauseErr.Info)
			manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			manifest.CompletedAt = ""
				_ = persistInvestigateManifest(manifestPath, *manifest)
			return runErr
		}
		manifest.Status = status
		manifest.PauseReason = ""
		manifest.PauseUntil = ""
		if runErr != nil {
			manifest.LastError = runErr.Error()
		}
		manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		manifest.CompletedAt = manifest.UpdatedAt
			_ = persistInvestigateManifest(manifestPath, *manifest)
		return runErr
	}

	mcpStatus, err := ensureInvestigateMCPStatus(*manifest)
	if err != nil {
		return writeFailure(investigateRunStatusFailedExecutor, err)
	}
	if err := persistInvestigateManifest(manifestPath, *manifest); err != nil {
		return writeFailure(investigateRunStatusFailedExecutor, err)
	}
	if len(mcpStatus.ConfiguredServers) > 0 && !mcpStatus.AllOK {
		return writeFailure(investigateRunStatusFailedReadiness, fmt.Errorf("one or more configured investigate MCPs are not working. Run `nana investigate doctor` and fix the MCP configuration"))
	}

	startRound, violations, err := investigateResumeState(*manifest)
	if err != nil {
		return writeFailure(investigateRunStatusFailedExecutor, err)
	}
	var finalReport investigateReport
	accepted := false

	for round := startRound; round <= manifest.MaxRounds; round++ {
		roundState := investigateRoundState{
			Round:               round,
			InvestigatorPrompt:  filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-investigator-prompt.md", round)),
			InvestigatorStdout:  filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-investigator-stdout.log", round)),
			InvestigatorStderr:  filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-investigator-stderr.log", round)),
			ReportPath:          filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-report.json", round)),
			ValidatorPrompt:     filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-validator-prompt.md", round)),
			ValidatorStdout:     filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-validator-stdout.log", round)),
			ValidatorStderr:     filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-validator-stderr.log", round)),
			ValidatorResultPath: filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-validator-result.json", round)),
			Status:              "running",
		}
		if len(manifest.Rounds) >= round {
			manifest.Rounds = manifest.Rounds[:round-1]
		}
		manifest.Rounds = append(manifest.Rounds, roundState)

		investigatorPrompt, err := buildInvestigatePrompt(*manifest, mcpStatus, round, violations)
		if err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}
		if err := os.WriteFile(roundState.InvestigatorPrompt, []byte(investigatorPrompt), 0o644); err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}
		result, err := runInvestigateCodexPrompt(manifestPath, *manifest, codexArgs, investigatorPrompt, fmt.Sprintf("investigator-round-%d", round), filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-investigator-checkpoint.json", round)))
		if err := os.WriteFile(roundState.InvestigatorStdout, []byte(result.Stdout), 0o644); err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}
		if err := os.WriteFile(roundState.InvestigatorStderr, []byte(result.Stderr), 0o644); err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}
		if err != nil {
			manifest.Rounds[len(manifest.Rounds)-1].Status = "failed"
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}

		report, err := parseInvestigateReport(result.Stdout)
		if err != nil {
			violations = []investigateViolation{{
				Code:    "invalid_report_json",
				Path:    "$",
				Message: err.Error(),
			}}
			validatorResult := investigateValidatorResult{
				Accepted:   false,
				Summary:    "investigator output was not valid JSON",
				Violations: violations,
			}
			manifest.Status = investigateRunStatusFailedReportParse
			if err := writeJSONArtifact(roundState.ValidatorResultPath, validatorResult); err != nil {
				return writeFailure(investigateRunStatusFailedExecutor, err)
			}
			manifest.Rounds[len(manifest.Rounds)-1].Status = "needs_revision"
			manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				if err := persistInvestigateManifest(manifestPath, *manifest); err != nil {
					return writeFailure(investigateRunStatusFailedExecutor, err)
				}
			continue
		}
		if err := writeJSONArtifact(roundState.ReportPath, report); err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}

		localViolations := validateInvestigateReport(report, manifest.WorkspaceRoot)
		validatorPrompt, err := buildInvestigateValidatorPrompt(*manifest, round, report, localViolations)
		if err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}
		if err := os.WriteFile(roundState.ValidatorPrompt, []byte(validatorPrompt), 0o644); err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}
		validatorExecResult, err := runInvestigateCodexPrompt(manifestPath, *manifest, codexArgs, validatorPrompt, fmt.Sprintf("investigation-validator-round-%d", round), filepath.Join(manifest.RunDir, fmt.Sprintf("round-%d-validator-checkpoint.json", round)))
		if err := os.WriteFile(roundState.ValidatorStdout, []byte(validatorExecResult.Stdout), 0o644); err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}
		if err := os.WriteFile(roundState.ValidatorStderr, []byte(validatorExecResult.Stderr), 0o644); err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}
		if err != nil {
			manifest.Rounds[len(manifest.Rounds)-1].Status = "failed"
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}

		validatorResult, err := parseInvestigateValidatorResult(validatorExecResult.Stdout)
		if err != nil {
			manifest.Status = investigateRunStatusFailedValidatorParse
			validatorResult = investigateValidatorResult{
				Accepted: false,
				Summary:  "validator output was not valid JSON",
				Violations: []investigateViolation{{
					Code:    "invalid_validator_json",
					Path:    "$",
					Message: err.Error(),
				}},
			}
		}
		validatorResult.Violations = append([]investigateViolation{}, append(localViolations, validatorResult.Violations...)...)
		validatorResult.Violations = uniqueInvestigateViolations(validatorResult.Violations)
		validatorResult.Accepted = validatorResult.Accepted && len(validatorResult.Violations) == 0
		if err := writeJSONArtifact(roundState.ValidatorResultPath, validatorResult); err != nil {
			return writeFailure(investigateRunStatusFailedExecutor, err)
		}

		if validatorResult.Accepted {
			accepted = true
			finalReport = report
			manifest.Status = investigateRunStatusCompleted
			manifest.AcceptedRound = round
			manifest.FinalReportPath = filepath.Join(manifest.RunDir, "final-report.json")
			manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			manifest.CompletedAt = manifest.UpdatedAt
			manifest.Rounds[len(manifest.Rounds)-1].Status = "accepted"
			if err := writeJSONArtifact(manifest.FinalReportPath, report); err != nil {
				return writeFailure(investigateRunStatusFailedExecutor, err)
			}
				if err := persistInvestigateManifest(manifestPath, *manifest); err != nil {
					return writeFailure(investigateRunStatusFailedExecutor, err)
				}
			break
		}

		violations = validatorResult.Violations
		manifest.Rounds[len(manifest.Rounds)-1].Status = "needs_revision"
		manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := persistInvestigateManifest(manifestPath, *manifest); err != nil {
				return writeFailure(investigateRunStatusFailedExecutor, err)
			}
	}

	if !accepted {
		return writeFailure(investigateRunStatusFailedValidatorExhausted, fmt.Errorf("investigation failed after %d rounds; see %s", manifest.MaxRounds, manifest.RunDir))
	}
	printInvestigationSummary(*manifest, finalReport)
	return nil
}

func ensureInvestigateMCPStatus(manifest investigateManifest) (investigateMCPStatus, error) {
	if status, err := readInvestigateMCPStatusFile(manifest.MCPStatusPath); err == nil {
		return status, nil
	}
	status, err := probeInvestigateMCPs(manifest.WorkspaceRoot, manifest.CodexHome)
	if err != nil {
		return investigateMCPStatus{}, err
	}
	if err := writeJSONArtifact(manifest.MCPStatusPath, status); err != nil {
		return investigateMCPStatus{}, err
	}
	if err := writeInvestigateMCPStatus(investigateMCPStatusPath(manifest.CodexHome), status); err != nil {
		return investigateMCPStatus{}, err
	}
	return status, nil
}

func readInvestigateMCPStatusFile(path string) (investigateMCPStatus, error) {
	var status investigateMCPStatus
	if err := readGithubJSON(path, &status); err != nil {
		return investigateMCPStatus{}, err
	}
	return status, nil
}

func investigateResumeState(manifest investigateManifest) (int, []investigateViolation, error) {
	if len(manifest.Rounds) == 0 {
		return 1, nil, nil
	}
	last := manifest.Rounds[len(manifest.Rounds)-1]
	switch last.Status {
	case "accepted":
		return manifest.MaxRounds + 1, nil, nil
	case "needs_revision":
		result, err := readInvestigateValidatorResult(last.ValidatorResultPath)
		if err != nil {
			return last.Round + 1, nil, nil
		}
		return last.Round + 1, result.Violations, nil
	case "failed", "running", "":
		return last.Round, nil, nil
	default:
		return last.Round + 1, nil, nil
	}
}

func readInvestigateValidatorResult(path string) (investigateValidatorResult, error) {
	var result investigateValidatorResult
	if err := readGithubJSON(path, &result); err != nil {
		return investigateValidatorResult{}, err
	}
	return result, nil
}

func printInvestigationSummary(manifest investigateManifest, report investigateReport) {
	fmt.Fprintf(os.Stdout, "[investigate] Run: %s\n", manifest.RunID)
	fmt.Fprintf(os.Stdout, "[investigate] Status: %s\n", report.OverallStatus)
	fmt.Fprintf(os.Stdout, "[investigate] Issues: %d\n", len(report.Issues))
	fmt.Fprintf(os.Stdout, "[investigate] Report: %s\n", manifest.FinalReportPath)
	if strings.TrimSpace(report.OverallShortExplanation) != "" {
		fmt.Fprintf(os.Stdout, "[investigate] Summary: %s\n", report.OverallShortExplanation)
	}
	for _, issue := range report.Issues {
		link := ""
		if len(issue.Proofs) > 0 {
			link = issue.Proofs[0].Link
		}
		fmt.Fprintf(os.Stdout, "[investigate] Issue %s: %s\n", defaultString(issue.ID, "(no id)"), issue.ShortExplanation)
		if strings.TrimSpace(link) != "" {
			fmt.Fprintf(os.Stdout, "[investigate]   proof: %s\n", link)
		}
	}
}

func investigateMCPStatusPath(codexHome string) string {
	return filepath.Join(codexHome, "investigate-mcp-status.json")
}

func writeInvestigateMCPStatus(path string, status investigateMCPStatus) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeJSONArtifact(path, status)
}

func listEnabledInvestigateMCPServers(codexHome string) ([]string, string, error) {
	output, err := runInvestigateCodexSubcommand(codexHome, "mcp", "list")
	if err != nil {
		return nil, output, err
	}
	servers := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] == "Name" {
			continue
		}
		if len(fields) >= 2 && fields[len(fields)-2] == "enabled" {
			name := fields[0]
			if !seen[name] {
				seen[name] = true
				servers = append(servers, name)
			}
		}
	}
	sort.Strings(servers)
	return servers, output, nil
}

func runInvestigateCodexSubcommand(codexHome string, args ...string) (string, error) {
	cmd := exec.Command("codex", args...)
	cmd.Env = envMapToList(func() map[string]string {
		env := currentEnvMap()
		env["CODEX_HOME"] = codexHome
		return env
	}())
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func probeInvestigateMCPs(cwd string, codexHome string) (investigateMCPStatus, error) {
	servers, rawList, err := listEnabledInvestigateMCPServers(codexHome)
	status := investigateMCPStatus{
		Version:           1,
		CheckedAt:         time.Now().UTC().Format(time.RFC3339),
		CodexHome:         codexHome,
		ConfigPath:        InvestigateCodexConfigPath(cwd),
		ConfiguredServers: servers,
		Servers:           []investigateMCPServerStatus{},
		AllOK:             true,
		MCPListRaw:        strings.TrimSpace(rawList),
	}
	if err != nil {
		status.AllOK = false
		status.ProbeSummary = err.Error()
		return status, err
	}
	if len(servers) == 0 {
		status.ProbeSummary = "no MCP servers configured"
		return status, nil
	}

	result, err := runInvestigateCodexHealthPrompt(cwd, codexHome, servers)
	if err != nil {
		status.AllOK = false
		status.ProbeSummary = err.Error()
		return status, err
	}
	start := strings.Index(result.Stdout, "{")
	end := strings.LastIndex(result.Stdout, "}")
	if start < 0 || end <= start {
		status.AllOK = false
		status.ProbeSummary = "MCP health probe did not return JSON"
		return status, fmt.Errorf("MCP health probe did not return JSON: %s", strings.TrimSpace(result.Stdout))
	}
	var payload struct {
		AllOK        bool                         `json:"all_ok"`
		ProbeSummary string                       `json:"probe_summary"`
		Servers      []investigateMCPServerStatus `json:"servers"`
	}
	if err := json.Unmarshal([]byte(result.Stdout[start:end+1]), &payload); err != nil {
		status.AllOK = false
		status.ProbeSummary = err.Error()
		return status, err
	}
	status.AllOK = payload.AllOK
	status.ProbeSummary = strings.TrimSpace(payload.ProbeSummary)
	status.Servers = normalizeInvestigateMCPServerStatuses(servers, payload.Servers)
	for _, server := range status.Servers {
		if !server.OK {
			status.AllOK = false
		}
	}
	return status, nil
}

func normalizeInvestigateMCPServerStatuses(expected []string, actual []investigateMCPServerStatus) []investigateMCPServerStatus {
	byName := map[string]investigateMCPServerStatus{}
	for _, server := range actual {
		name := strings.TrimSpace(server.ServerName)
		if name == "" {
			continue
		}
		server.ServerName = name
		server.Summary = strings.TrimSpace(server.Summary)
		byName[name] = server
	}
	out := make([]investigateMCPServerStatus, 0, len(expected))
	for _, name := range expected {
		if server, ok := byName[name]; ok {
			out = append(out, server)
			continue
		}
		out = append(out, investigateMCPServerStatus{
			ServerName: name,
			OK:         false,
			Summary:    "Codex probe omitted this MCP",
		})
	}
	return out
}

func runInvestigateCodexHealthPrompt(cwd string, codexHome string, servers []string) (investigateExecutionResult, error) {
	lines := []string{
		"Check whether all configured MCP servers in this session are usable.",
		"Configured MCP server names:",
	}
	for _, server := range servers {
		lines = append(lines, "- "+server)
	}
	lines = append(lines,
		"",
		"Use available MCP tools to verify each configured MCP server is working.",
		"Return JSON only.",
		`Schema: {"all_ok":true|false,"probe_summary":"...","servers":[{"server_name":"...","ok":true|false,"summary":"..."}]}`,
		"If a configured server cannot be verified or appears broken, mark ok=false for that server.",
	)
	return runInvestigateSimplePrompt(cwd, codexHome, strings.Join(lines, "\n")+"\n", "mcp-health")
}

func runInvestigateSimplePrompt(cwd string, codexHome string, prompt string, alias string) (investigateExecutionResult, error) {
	scopedCodexHome, err := ensureScopedCodexHome(codexHome, filepath.Join(cwd, ".nana", "state", "investigate-probes", sanitizePathToken(alias)))
	if err != nil {
		return investigateExecutionResult{}, err
	}
	sessionID := fmt.Sprintf("investigate-simple-%d", time.Now().UnixNano())
	sessionInstructionsPath, err := writeSessionModelInstructions(cwd, sessionID, scopedCodexHome)
	if err != nil {
		return investigateExecutionResult{}, err
	}
	defer removeSessionInstructionsFile(cwd, sessionID)

	args, fastMode := normalizeLocalWorkCodexArgsWithFast(nil)
	prompt = prefixCodexFastPrompt(prompt, fastMode)
	args = append([]string{"exec", "-C", cwd}, args...)
	args = append(args, "-")
	args = injectModelInstructionsArgs(args, sessionInstructionsPath)
	cmd := exec.Command("codex", args...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+cwd)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return investigateExecutionResult{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func writeInvestigateConfig(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		base := strings.Join([]string{
			"[agents]",
			"max_threads = 6",
			"max_depth = 2",
			"",
			"[env]",
			`USE_NANA_EXPLORE_CMD = "1"`,
			"",
		}, "\n")
		content = []byte(base)
	}
	managedBlock := renderInvestigateConfigBlock()
	updated := upsertInvestigateConfigBlock(string(content), managedBlock)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

func renderInvestigateConfigBlock() string {
	lines := []string{
		"# ============================================================",
		investigateConfigBlockHeader,
		"# Managed by nana investigate onboard",
		"# Add or remove MCP servers here or via `codex mcp ...` using this investigate CODEX_HOME.",
		"# Nana asks Codex to probe whichever MCPs are configured here when it needs MCP health.",
		"# ============================================================",
		"",
		"# ============================================================",
		investigateConfigBlockEnd,
		"",
	}
	return strings.Join(lines, "\n")
}

func upsertInvestigateConfigBlock(original string, block string) string {
	lines := strings.Split(strings.ReplaceAll(original, "\r\n", "\n"), "\n")
	out := []string{}
	inBlock := false
	for _, line := range lines {
		if strings.Contains(line, investigateConfigBlockHeader) {
			inBlock = true
			continue
		}
		if inBlock {
			if strings.Contains(line, investigateConfigBlockEnd) {
				inBlock = false
			}
			continue
		}
		out = append(out, line)
	}
	trimmed := strings.TrimSpace(strings.Join(out, "\n"))
	if trimmed == "" {
		return block
	}
	return trimmed + "\n\n" + block
}

func resolveInvestigateWorkspaceRoot(cwd string) string {
	root, err := githubGitOutput(cwd, "rev-parse", "--show-toplevel")
	if err == nil && strings.TrimSpace(root) != "" {
		return strings.TrimSpace(root)
	}
	return cwd
}

func buildInvestigatePrompt(manifest investigateManifest, mcpStatus investigateMCPStatus, round int, previousViolations []investigateViolation) (string, error) {
	rolePrompt, err := readPromptSurfaceWithFallback("investigator")
	if err != nil {
		return "", err
	}
	lines := []string{
		"# NANA Investigate",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Round: %d/%d", round, manifest.MaxRounds),
		fmt.Sprintf("Workspace root: %s", manifest.WorkspaceRoot),
		fmt.Sprintf("Query: %s", manifest.Query),
		"",
		"Use source-backed evidence only.",
		"- Prefer source code, runtime logs, and direct tool outputs.",
		"- Documentation is supplementary only and MUST NOT be marked as a primary proof.",
		"- If local source code is referenced, use an absolute file path link and include `path` plus `line` when applicable.",
		"- Every claim must be supported by proof links.",
		"- If the evidence is mixed, use PARTIALLY_CONFIRMED.",
		"- If the claim does not hold up, use REFUTED.",
		"",
		"MCP availability summary:",
		fmt.Sprintf("- Configured MCP servers: %d", len(mcpStatus.ConfiguredServers)),
		fmt.Sprintf("- Probe summary: %s", defaultString(compactPromptHeadValue(strings.TrimSpace(mcpStatus.ProbeSummary), 0, 200), "(none)")),
	}
	for _, server := range limitPromptList(mcpStatus.Servers, investigateMaxPromptServers) {
		lines = append(lines, fmt.Sprintf("- MCP %s: %s", server.ServerName, defaultString(compactPromptHeadValue(strings.TrimSpace(server.Summary), 0, 200), "(no summary)")))
	}
	if len(mcpStatus.Servers) > investigateMaxPromptServers {
		lines = append(lines, fmt.Sprintf("- ... %d additional MCP servers omitted", len(mcpStatus.Servers)-investigateMaxPromptServers))
	}
	lines = append(lines,
		"",
		"Return JSON only.",
		`Schema: {"overall_status":"REFUTED|CONFIRMED|PARTIALLY_CONFIRMED","overall_short_explanation":"...","overall_detailed_explanation":"...","overall_proofs":[{"kind":"source_code|build_log|jenkins_run|github|jira|local_artifact|documentation|other","title":"...","link":"...","why_it_proves":"...","is_primary":true,"path":"...","line":123}],"issues":[{"id":"...","short_explanation":"...","detailed_explanation":"...","proofs":[{"kind":"source_code|build_log|jenkins_run|github|jira|local_artifact|documentation|other","title":"...","link":"...","why_it_proves":"...","is_primary":true,"path":"...","line":123}]}]}`,
	)
	if len(previousViolations) > 0 {
		lines = append(lines, "", "Previous validator violations to fix:")
		for _, violation := range limitPromptList(previousViolations, investigateMaxPromptViolations) {
			lines = append(lines, fmt.Sprintf("- [%s] %s %s", compactPromptHeadValue(violation.Code, 0, 80), defaultString(compactPromptHeadValue(violation.Path, 0, 160), "$"), compactPromptHeadValue(violation.Message, 0, 200)))
		}
		if len(previousViolations) > investigateMaxPromptViolations {
			lines = append(lines, fmt.Sprintf("- ... %d additional violations omitted", len(previousViolations)-investigateMaxPromptViolations))
		}
	}
	lines = append(lines, "", compactPromptHeadValue(rolePrompt, 0, investigateRolePromptCharLimit))
	return capPromptChars(strings.Join(lines, "\n")+"\n", investigatePromptCharLimit), nil
}

func buildInvestigateValidatorPrompt(manifest investigateManifest, round int, report investigateReport, localViolations []investigateViolation) (string, error) {
	rolePrompt, err := readPromptSurfaceWithFallback("investigation-validator")
	if err != nil {
		return "", err
	}
	reportJSON := compactInvestigateValidatorReportJSON(report)
	lines := []string{
		"# NANA Investigation Validator",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Round: %d/%d", round, manifest.MaxRounds),
		fmt.Sprintf("Workspace root: %s", manifest.WorkspaceRoot),
		fmt.Sprintf("Query: %s", manifest.Query),
		"",
		"Validate the investigation report against source evidence and output JSON only.",
		"- Reject hallucinated or weak claims.",
		"- Reject documentation-primary evidence.",
		"- Prefer source code, direct logs, and direct tool output.",
		"- Re-check links and proof sufficiency.",
		"",
		`Schema: {"accepted":true|false,"summary":"...","violations":[{"code":"...","path":"...","message":"..."}]}`,
	}
	if len(localViolations) > 0 {
		lines = append(lines, "", "Supervisor structural findings:")
		for _, violation := range limitPromptList(localViolations, investigateMaxPromptViolations) {
			lines = append(lines, fmt.Sprintf("- [%s] %s %s", compactPromptHeadValue(violation.Code, 0, 80), defaultString(compactPromptHeadValue(violation.Path, 0, 160), "$"), compactPromptHeadValue(violation.Message, 0, 200)))
		}
		if len(localViolations) > investigateMaxPromptViolations {
			lines = append(lines, fmt.Sprintf("- ... %d additional structural findings omitted", len(localViolations)-investigateMaxPromptViolations))
		}
	}
	lines = append(lines,
		"",
		"Report JSON:",
		reportJSON,
		"",
		compactPromptHeadValue(rolePrompt, 0, investigateRolePromptCharLimit),
	)
	return capPromptChars(strings.Join(lines, "\n")+"\n", investigatePromptCharLimit), nil
}

func compactInvestigateValidatorReportJSON(report investigateReport) string {
	proofBudget := investigateMaxValidatorProofs
	compacted := investigateReport{
		OverallStatus:              compactPromptHeadValue(report.OverallStatus, 0, 64),
		OverallShortExplanation:    compactPromptHeadValue(report.OverallShortExplanation, 0, 300),
		OverallDetailedExplanation: compactPromptHeadValue(report.OverallDetailedExplanation, 0, 1200),
		OverallProofs:              []investigateProof{},
		Issues:                     []investigateIssue{},
	}

	for _, proof := range report.OverallProofs {
		if proofBudget <= 0 {
			break
		}
		compacted.OverallProofs = append(compacted.OverallProofs, compactInvestigateProof(proof))
		proofBudget--
	}

	for _, issue := range limitPromptList(report.Issues, investigateMaxValidatorIssues) {
		compactedIssue := investigateIssue{
			ID:                  compactPromptHeadValue(issue.ID, 0, 120),
			ShortExplanation:    compactPromptHeadValue(issue.ShortExplanation, 0, 240),
			DetailedExplanation: compactPromptHeadValue(issue.DetailedExplanation, 0, 800),
			Proofs:              []investigateProof{},
		}
		for _, proof := range issue.Proofs {
			if proofBudget <= 0 {
				break
			}
			compactedIssue.Proofs = append(compactedIssue.Proofs, compactInvestigateProof(proof))
			proofBudget--
		}
		compacted.Issues = append(compacted.Issues, compactedIssue)
	}

	for issueCount := len(compacted.Issues); issueCount >= 0; issueCount-- {
		candidate := compacted
		candidate.Issues = append([]investigateIssue(nil), compacted.Issues[:issueCount]...)
		encoded := string(mustMarshalJSON(candidate))
		if len(encoded) <= investigateValidatorPayloadCharLimit {
			return encoded
		}
	}
	return string(mustMarshalJSON(investigateReport{
		OverallStatus:              compactPromptHeadValue(report.OverallStatus, 0, 64),
		OverallShortExplanation:    compactPromptHeadValue(report.OverallShortExplanation, 0, 300),
		OverallDetailedExplanation: compactPromptHeadValue(report.OverallDetailedExplanation, 0, 800),
	}))
}

func compactInvestigateProof(proof investigateProof) investigateProof {
	return investigateProof{
		Kind:        compactPromptHeadValue(proof.Kind, 0, 64),
		Title:       compactPromptHeadValue(proof.Title, 0, 160),
		Link:        compactPromptHeadValue(proof.Link, 0, 240),
		WhyItProves: compactPromptHeadValue(proof.WhyItProves, 0, 240),
		IsPrimary:   proof.IsPrimary,
		Path:        compactPromptHeadValue(proof.Path, 0, 200),
		Line:        proof.Line,
	}
}

func readPromptSurfaceWithFallback(role string) (string, error) {
	content, err := readGithubPromptSurface(role)
	if err == nil && strings.TrimSpace(content) != "" {
		return content, nil
	}
	switch role {
	case "investigator":
		return investigatorPromptFallback, nil
	case "investigation-validator":
		return investigationValidatorPromptFallback, nil
	default:
		return "", err
	}
}

const investigatorPromptFallback = `---
description: "Source-backed investigator (READ-ONLY)"
argument-hint: "investigation task"
---
<identity>
You are Investigator. Establish what is true, false, or only partially supported using source code and source-of-truth systems.
</identity>

<constraints>
- Read-only. Do not edit files.
- Prefer source code and direct system evidence over documentation.
- Documentation can supplement context but cannot be a primary proof.
- Every claim must be backed by linked evidence.
</constraints>
`

const investigationValidatorPromptFallback = `---
description: "Investigation validator (READ-ONLY)"
argument-hint: "report to validate"
---
<identity>
You are Investigation Validator. Accept only evidence-backed reports that satisfy the reporting contract.
</identity>

<constraints>
- Read-only. Do not edit files.
- Re-check claims against source evidence.
- Reject documentation-primary evidence.
- Reject any missing or unverifiable proof links.
</constraints>
`

func runInvestigateCodexPrompt(manifestPath string, manifest investigateManifest, codexArgs []string, prompt string, codexHomeAlias string, checkpointPath string) (investigateExecutionResult, error) {
	scopedCodexHome, err := ensureScopedCodexHome(manifest.CodexHome, filepath.Join(manifest.RunDir, "codex-home", sanitizePathToken(codexHomeAlias)))
	if err != nil {
		return investigateExecutionResult{}, err
	}

	args, fastMode := normalizeLocalWorkCodexArgsWithFast(codexArgs)
	prompt = prefixCodexFastPrompt(prompt, fastMode)
	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       manifest.WorkspaceRoot,
		InstructionsRoot: manifest.WorkspaceRoot,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", manifest.WorkspaceRoot},
		CommonArgs:       args,
		Prompt:           prompt,
		PromptTransport:  codexPromptTransportStdin,
		CheckpointPath:   checkpointPath,
		StepKey:          codexHomeAlias,
		ResumeStrategy:   codexResumeSamePrompt,
		Env:              append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+manifest.WorkspaceRoot),
		OnPause: func(info codexRateLimitPauseInfo) {
			manifest.Status = "paused"
			manifest.PauseReason = strings.TrimSpace(info.Reason)
			manifest.PauseUntil = strings.TrimSpace(info.RetryAfter)
			manifest.LastError = codexPauseInfoMessage(info)
			manifest.UpdatedAt = ISOTimeNow()
			manifest.CompletedAt = ""
			_ = persistInvestigateManifest(manifestPath, manifest)
		},
		OnResume: func(info codexRateLimitPauseInfo) {
			manifest.Status = investigateRunStatusRunning
			manifest.PauseReason = ""
			manifest.PauseUntil = ""
			manifest.LastError = ""
			manifest.UpdatedAt = ISOTimeNow()
			_ = persistInvestigateManifest(manifestPath, manifest)
		},
	})
	return investigateExecutionResult{Stdout: result.Stdout, Stderr: result.Stderr}, err
}

func parseInvestigateReport(raw string) (investigateReport, error) {
	content, err := extractJSONObject(raw)
	if err != nil {
		return investigateReport{}, err
	}
	var report investigateReport
	if err := json.Unmarshal([]byte(content), &report); err != nil {
		return investigateReport{}, err
	}
	report.OverallStatus = normalizeInvestigateStatus(report.OverallStatus)
	for issueIndex := range report.Issues {
		report.Issues[issueIndex].ID = strings.TrimSpace(report.Issues[issueIndex].ID)
	}
	return report, nil
}

func parseInvestigateValidatorResult(raw string) (investigateValidatorResult, error) {
	content, err := extractJSONObject(raw)
	if err != nil {
		return investigateValidatorResult{}, err
	}
	var result investigateValidatorResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return investigateValidatorResult{}, err
	}
	result.Violations = uniqueInvestigateViolations(result.Violations)
	return result, nil
}

func extractJSONObject(raw string) (string, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return "", fmt.Errorf("output did not contain a JSON object")
	}
	return raw[start : end+1], nil
}

func normalizeInvestigateStatus(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case investigateStatusRefuted:
		return investigateStatusRefuted
	case investigateStatusConfirmed:
		return investigateStatusConfirmed
	case investigateStatusPartiallyConfirmed:
		return investigateStatusPartiallyConfirmed
	default:
		return strings.ToUpper(strings.TrimSpace(value))
	}
}

func validateInvestigateReport(report investigateReport, workspaceRoot string) []investigateViolation {
	violations := []investigateViolation{}
	switch report.OverallStatus {
	case investigateStatusRefuted, investigateStatusConfirmed, investigateStatusPartiallyConfirmed:
	default:
		violations = append(violations, investigateViolation{Code: "invalid_overall_status", Path: "overall_status", Message: "overall_status must be REFUTED, CONFIRMED, or PARTIALLY_CONFIRMED"})
	}
	if strings.TrimSpace(report.OverallShortExplanation) == "" {
		violations = append(violations, investigateViolation{Code: "missing_overall_short_explanation", Path: "overall_short_explanation", Message: "overall_short_explanation is required"})
	}
	if strings.TrimSpace(report.OverallDetailedExplanation) == "" {
		violations = append(violations, investigateViolation{Code: "missing_overall_detailed_explanation", Path: "overall_detailed_explanation", Message: "overall_detailed_explanation is required"})
	}
	violations = append(violations, validateInvestigateProofList(report.OverallProofs, "overall_proofs", workspaceRoot)...)
	if !hasNonDocumentationPrimaryProof(report.OverallProofs) {
		violations = append(violations, investigateViolation{Code: "missing_primary_overall_proof", Path: "overall_proofs", Message: "overall_proofs must contain at least one primary non-documentation proof"})
	}
	for index, issue := range report.Issues {
		prefix := fmt.Sprintf("issues[%d]", index)
		if strings.TrimSpace(issue.ID) == "" {
			violations = append(violations, investigateViolation{Code: "missing_issue_id", Path: prefix + ".id", Message: "issue id is required"})
		}
		if strings.TrimSpace(issue.ShortExplanation) == "" {
			violations = append(violations, investigateViolation{Code: "missing_issue_short_explanation", Path: prefix + ".short_explanation", Message: "short_explanation is required"})
		}
		if strings.TrimSpace(issue.DetailedExplanation) == "" {
			violations = append(violations, investigateViolation{Code: "missing_issue_detailed_explanation", Path: prefix + ".detailed_explanation", Message: "detailed_explanation is required"})
		}
		violations = append(violations, validateInvestigateProofList(issue.Proofs, prefix+".proofs", workspaceRoot)...)
		if !hasNonDocumentationPrimaryProof(issue.Proofs) {
			violations = append(violations, investigateViolation{Code: "missing_primary_issue_proof", Path: prefix + ".proofs", Message: "each issue must contain at least one primary non-documentation proof"})
		}
	}
	return uniqueInvestigateViolations(violations)
}

func validateInvestigateProofList(proofs []investigateProof, prefix string, workspaceRoot string) []investigateViolation {
	violations := []investigateViolation{}
	if len(proofs) == 0 {
		violations = append(violations, investigateViolation{Code: "missing_proofs", Path: prefix, Message: "at least one proof is required"})
		return violations
	}
	for index, proof := range proofs {
		path := fmt.Sprintf("%s[%d]", prefix, index)
		if strings.TrimSpace(proof.Title) == "" {
			violations = append(violations, investigateViolation{Code: "missing_proof_title", Path: path + ".title", Message: "proof title is required"})
		}
		if strings.TrimSpace(proof.Link) == "" {
			violations = append(violations, investigateViolation{Code: "missing_proof_link", Path: path + ".link", Message: "proof link is required"})
		}
		if strings.TrimSpace(proof.WhyItProves) == "" {
			violations = append(violations, investigateViolation{Code: "missing_proof_explanation", Path: path + ".why_it_proves", Message: "why_it_proves is required"})
		}
		if strings.EqualFold(strings.TrimSpace(proof.Kind), "documentation") && proof.IsPrimary {
			violations = append(violations, investigateViolation{Code: "documentation_is_not_primary", Path: path + ".is_primary", Message: "documentation cannot be a primary proof"})
		}
		if fileErr := validateInvestigateProofLink(proof, workspaceRoot); fileErr != "" {
			violations = append(violations, investigateViolation{Code: "invalid_proof_link", Path: path + ".link", Message: fileErr})
		}
	}
	return violations
}

func validateInvestigateProofLink(proof investigateProof, workspaceRoot string) string {
	switch strings.ToLower(strings.TrimSpace(proof.Kind)) {
	case "source_code":
		return validateInvestigateSourceCodeLink(proof, workspaceRoot)
	case "github":
		if !strings.HasPrefix(strings.TrimSpace(proof.Link), "https://github.com/") {
			return "github proof must use a github.com URL"
		}
	case "jira":
		link := strings.TrimSpace(proof.Link)
		if !strings.Contains(link, "atlassian.net") && !strings.HasPrefix(link, "ari:cloud:jira:") {
			return "jira proof must use an Atlassian URL or ARI"
		}
	case "jenkins_run":
		if !strings.HasPrefix(strings.TrimSpace(proof.Link), "http://") && !strings.HasPrefix(strings.TrimSpace(proof.Link), "https://") {
			return "jenkins_run proof must use an http(s) URL"
		}
	case "build_log", "local_artifact":
		return validateInvestigateLocalArtifactLink(proof.Link, workspaceRoot)
	}
	return ""
}

func validateInvestigateLocalArtifactLink(link string, workspaceRoot string) string {
	trimmed := strings.TrimSpace(link)
	if trimmed == "" {
		return "artifact link is empty"
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return ""
	}
	path, _, ok := investigateParseLocalPathLink(trimmed)
	if !ok {
		path = trimmed
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspaceRoot, path)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return fmt.Sprintf("artifact path does not exist: %s", path)
	}
	return ""
}

func validateInvestigateSourceCodeLink(proof investigateProof, workspaceRoot string) string {
	path := strings.TrimSpace(proof.Path)
	line := proof.Line
	if path == "" {
		parsedPath, parsedLine, ok := investigateParseLocalPathLink(proof.Link)
		if ok {
			path = parsedPath
			line = parsedLine
		}
	}
	if path == "" {
		if strings.HasPrefix(proof.Link, "http://") || strings.HasPrefix(proof.Link, "https://") {
			return ""
		}
		return "source_code proof must provide a resolvable local path or an http(s) code link"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspaceRoot, path)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return fmt.Sprintf("source code path does not exist: %s", path)
	}
	if line > 0 {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Sprintf("failed to read source code path: %s", path)
		}
		lines := strings.Split(string(content), "\n")
		if line > len(lines) {
			return fmt.Sprintf("line %d is outside the file: %s", line, path)
		}
	}
	return ""
}

var localFileWithLinePattern = regexp.MustCompile(`^(?P<path>/[^#]+?)(?:#L(?P<line>\d+)|:(?P<line2>\d+))?$`)

func investigateParseLocalPathLink(link string) (string, int, bool) {
	matches := localFileWithLinePattern.FindStringSubmatch(strings.TrimSpace(link))
	if matches == nil {
		return "", 0, false
	}
	line := 0
	rawLine := strings.TrimSpace(matches[2])
	if rawLine == "" {
		rawLine = strings.TrimSpace(matches[3])
	}
	if rawLine != "" {
		parsed, err := strconv.Atoi(rawLine)
		if err == nil {
			line = parsed
		}
	}
	return strings.TrimSpace(matches[1]), line, true
}

func hasNonDocumentationPrimaryProof(proofs []investigateProof) bool {
	for _, proof := range proofs {
		if proof.IsPrimary && !strings.EqualFold(strings.TrimSpace(proof.Kind), "documentation") {
			return true
		}
	}
	return false
}

func uniqueInvestigateViolations(violations []investigateViolation) []investigateViolation {
	if len(violations) == 0 {
		return nil
	}
	seen := map[string]bool{}
	unique := make([]investigateViolation, 0, len(violations))
	for _, violation := range violations {
		key := violation.Code + "\x00" + violation.Path + "\x00" + violation.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, violation)
	}
	return unique
}
