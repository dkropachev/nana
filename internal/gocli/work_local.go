package gocli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const WorkLocalHelp = `nana work-local - Autonomous local plan execution for git-backed local repos

Usage:
  nana work-local start [--repo <path>] (--task <text> | --plan-file <path>) [--max-iterations <n>] [--integration <final|always|never>] [-- codex-args...]
  nana work-local resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
  nana work-local status [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work-local logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>]
  nana work-local retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work-local help

Behavior:
  - runs only against a local git repo in an isolated managed sandbox
  - never submits, publishes, pushes, or calls GitHub APIs
  - loops through implement -> verify -> self-review -> harden -> re-verify with capped hardening rounds
  - runs lint, compile/build, and unit tests every iteration; integration runs on the final pass by default
  - persists run artifacts under ~/.nana/local-work/
`

const (
	localWorkDefaultMaxIterations  = 8
	localWorkMaxReviewRounds       = 2
	localWorkRuntimeName           = "work-local"
	localWorkPromptCharLimit       = 120000
	localWorkPromptSnippetChars    = 2000
	localWorkPromptSnippetLines    = 25
	localWorkValidationParallelism = 4
	localWorkMaxValidationGroups   = 8
	localWorkMaxFindingsPerGroup   = 10
)

type localWorkStartOptions struct {
	RepoPath          string
	Task              string
	PlanFile          string
	MaxIterations     int
	IntegrationPolicy string
	CodexArgs         []string
}

type localWorkRunSelection struct {
	RunID      string
	UseLast    bool
	GlobalLast bool
	RepoPath   string
}

type localWorkResumeOptions struct {
	RunSelection localWorkRunSelection
	CodexArgs    []string
}

type localWorkLogsOptions struct {
	RunSelection localWorkRunSelection
	TailLines    int
}

type localWorkManifest struct {
	Version                     int                         `json:"version"`
	RunID                       string                      `json:"run_id"`
	CreatedAt                   string                      `json:"created_at"`
	UpdatedAt                   string                      `json:"updated_at"`
	CompletedAt                 string                      `json:"completed_at,omitempty"`
	Status                      string                      `json:"status"`
	CurrentPhase                string                      `json:"current_phase,omitempty"`
	CurrentIteration            int                         `json:"current_iteration,omitempty"`
	RepoRoot                    string                      `json:"repo_root"`
	RepoName                    string                      `json:"repo_name"`
	RepoID                      string                      `json:"repo_id"`
	SourceBranch                string                      `json:"source_branch"`
	BaselineSHA                 string                      `json:"baseline_sha"`
	SandboxPath                 string                      `json:"sandbox_path"`
	SandboxRepoPath             string                      `json:"sandbox_repo_path"`
	VerificationPlan            *githubVerificationPlan     `json:"verification_plan,omitempty"`
	VerificationScriptsDir      string                      `json:"verification_scripts_dir,omitempty"`
	InputPath                   string                      `json:"input_path"`
	InputMode                   string                      `json:"input_mode"`
	IntegrationPolicy           string                      `json:"integration_policy"`
	MaxIterations               int                         `json:"max_iterations"`
	LastError                   string                      `json:"last_error,omitempty"`
	RejectedFindingFingerprints []string                    `json:"rejected_finding_fingerprints,omitempty"`
	Iterations                  []localWorkIterationSummary `json:"iterations,omitempty"`
}

type localWorkIterationSummary struct {
	Iteration                             int      `json:"iteration"`
	StartedAt                             string   `json:"started_at"`
	CompletedAt                           string   `json:"completed_at"`
	Status                                string   `json:"status"`
	DiffFingerprint                       string   `json:"diff_fingerprint,omitempty"`
	VerificationFingerprint               string   `json:"verification_fingerprint,omitempty"`
	ReviewFingerprint                     string   `json:"review_fingerprint,omitempty"`
	InitialReviewFingerprint              string   `json:"initial_review_fingerprint,omitempty"`
	HardeningFingerprint                  string   `json:"hardening_fingerprint,omitempty"`
	PostHardeningVerificationFingerprint  string   `json:"post_hardening_verification_fingerprint,omitempty"`
	VerificationPassed                    bool     `json:"verification_passed"`
	VerificationFailedStages              []string `json:"verification_failed_stages,omitempty"`
	VerificationSummary                   string   `json:"verification_summary,omitempty"`
	InitialReviewFindings                 int      `json:"initial_review_findings,omitempty"`
	ValidatedFindings                     int      `json:"validated_findings,omitempty"`
	ConfirmedFindings                     int      `json:"confirmed_findings,omitempty"`
	RejectedFindings                      int      `json:"rejected_findings,omitempty"`
	ModifiedFindings                      int      `json:"modified_findings,omitempty"`
	ValidationGroups                      []string `json:"validation_groups,omitempty"`
	ReviewRoundsUsed                      int      `json:"review_rounds_used,omitempty"`
	ReviewFindingsByRound                 []int    `json:"review_findings_by_round,omitempty"`
	ReviewRoundFingerprints               []string `json:"review_round_fingerprints,omitempty"`
	HardeningRoundFingerprints            []string `json:"hardening_round_fingerprints,omitempty"`
	PostHardeningVerificationFingerprints []string `json:"post_hardening_verification_fingerprints,omitempty"`
	ReviewFindings                        int      `json:"review_findings"`
	ReviewFindingTitles                   []string `json:"review_finding_titles,omitempty"`
	IntegrationRan                        bool     `json:"integration_ran,omitempty"`
}

type localWorkVerificationCommandResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
	Cached   bool   `json:"cached,omitempty"`
}

type localWorkVerificationStageResult struct {
	Name     string                               `json:"name"`
	Status   string                               `json:"status"`
	Commands []localWorkVerificationCommandResult `json:"commands,omitempty"`
}

type localWorkVerificationReport struct {
	GeneratedAt         string                             `json:"generated_at"`
	PlanFingerprint     string                             `json:"plan_fingerprint,omitempty"`
	IntegrationIncluded bool                               `json:"integration_included"`
	Passed              bool                               `json:"passed"`
	FailedStages        []string                           `json:"failed_stages,omitempty"`
	Stages              []localWorkVerificationStageResult `json:"stages,omitempty"`
}

type localWorkExecutionResult struct {
	Stdout string
	Stderr string
}

type localWorkFindingGroup struct {
	GroupID  string                    `json:"group_id"`
	Findings []githubPullReviewFinding `json:"findings"`
}

type localWorkFindingDecisionStatus string

const (
	localWorkFindingConfirmed  localWorkFindingDecisionStatus = "confirmed"
	localWorkFindingRejected   localWorkFindingDecisionStatus = "rejected"
	localWorkFindingModified   localWorkFindingDecisionStatus = "modified"
	localWorkFindingSuperseded localWorkFindingDecisionStatus = "superseded"
	localWorkFindingPending    localWorkFindingDecisionStatus = "pending"
)

type localWorkValidatedFinding struct {
	GroupID               string                         `json:"group_id"`
	OriginalFingerprint   string                         `json:"original_fingerprint"`
	CurrentFingerprint    string                         `json:"current_fingerprint"`
	Status                localWorkFindingDecisionStatus `json:"status"`
	Reason                string                         `json:"reason,omitempty"`
	SupersedesFingerprint string                         `json:"supersedes_fingerprint,omitempty"`
	Finding               *githubPullReviewFinding       `json:"finding,omitempty"`
}

type localWorkValidationDecision struct {
	Fingerprint string                         `json:"fingerprint"`
	Status      localWorkFindingDecisionStatus `json:"status"`
	Reason      string                         `json:"reason,omitempty"`
	Replacement *githubPullReviewFinding       `json:"replacement,omitempty"`
}

type localWorkValidationGroupResult struct {
	GroupID   string                        `json:"group_id"`
	Decisions []localWorkValidationDecision `json:"decisions"`
}

type localWorkGroupingResult struct {
	Groups []localWorkGroupingGroup `json:"groups"`
}

type localWorkGroupingGroup struct {
	GroupID   string   `json:"group_id"`
	Rationale string   `json:"rationale,omitempty"`
	Findings  []string `json:"findings"`
}

type localWorkRepoMetadata struct {
	Version   int    `json:"version"`
	RepoID    string `json:"repo_id"`
	RepoRoot  string `json:"repo_root"`
	RepoName  string `json:"repo_name"`
	UpdatedAt string `json:"updated_at"`
}

type localWorkLatestRunPointer struct {
	RepoID   string `json:"repo_id,omitempty"`
	RepoRoot string `json:"repo_root"`
	RunID    string `json:"run_id"`
}

type localWorkRunIndex struct {
	Version         int                               `json:"version"`
	GlobalLastRunID string                            `json:"global_last_run_id,omitempty"`
	Entries         map[string]localWorkRunIndexEntry `json:"entries,omitempty"`
}

type localWorkRunIndexEntry struct {
	RunID        string `json:"run_id"`
	RepoID       string `json:"repo_id"`
	RepoRoot     string `json:"repo_root"`
	RepoName     string `json:"repo_name"`
	ManifestPath string `json:"manifest_path"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func WorkLocal(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, WorkLocalHelp)
		return nil
	}

	switch args[0] {
	case "start":
		options, err := parseLocalWorkStartArgs(args[1:])
		if err != nil {
			return err
		}
		return startLocalWork(cwd, options)
	case "resume":
		options, err := parseLocalWorkResumeArgs(args[1:])
		if err != nil {
			return err
		}
		return resumeLocalWork(cwd, options)
	case "status":
		selection, err := parseLocalWorkRunSelection(args[1:], true)
		if err != nil {
			return err
		}
		return localWorkStatus(cwd, selection)
	case "logs":
		options, err := parseLocalWorkLogsArgs(args[1:])
		if err != nil {
			return err
		}
		return localWorkLogs(cwd, options)
	case "retrospective":
		selection, err := parseLocalWorkRunSelection(args[1:], true)
		if err != nil {
			return err
		}
		return localWorkRetrospective(cwd, selection)
	case "verify-refresh":
		selection, err := parseLocalWorkRunSelection(args[1:], false)
		if err != nil {
			return err
		}
		return refreshLocalWorkVerificationArtifacts(cwd, selection)
	default:
		return fmt.Errorf("Unknown work-local subcommand: %s\n\n%s", args[0], WorkLocalHelp)
	}
}

func parseLocalWorkStartArgs(args []string) (localWorkStartOptions, error) {
	options := localWorkStartOptions{
		MaxIterations:     localWorkDefaultMaxIterations,
		IntegrationPolicy: "final",
	}
	passthroughIndex := len(args)
	for index, token := range args {
		if token == "--" {
			passthroughIndex = index
			break
		}
	}
	parseArgs := args[:passthroughIndex]
	if passthroughIndex < len(args) {
		options.CodexArgs = append([]string{}, args[passthroughIndex+1:]...)
	}

	for index := 0; index < len(parseArgs); index++ {
		token := parseArgs[index]
		switch {
		case token == "--repo":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--repo")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			options.RepoPath = value
			index++
		case strings.HasPrefix(token, "--repo="):
			options.RepoPath = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
		case token == "--task":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--task")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			options.Task = value
			index++
		case strings.HasPrefix(token, "--task="):
			options.Task = strings.TrimSpace(strings.TrimPrefix(token, "--task="))
		case token == "--plan-file":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--plan-file")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			options.PlanFile = value
			index++
		case strings.HasPrefix(token, "--plan-file="):
			options.PlanFile = strings.TrimSpace(strings.TrimPrefix(token, "--plan-file="))
		case token == "--max-iterations":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--max-iterations")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 {
				return localWorkStartOptions{}, fmt.Errorf("Invalid --max-iterations value %q.\n%s", value, WorkLocalHelp)
			}
			options.MaxIterations = parsed
			index++
		case strings.HasPrefix(token, "--max-iterations="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--max-iterations="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return localWorkStartOptions{}, fmt.Errorf("Invalid --max-iterations value %q.\n%s", value, WorkLocalHelp)
			}
			options.MaxIterations = parsed
		case token == "--integration":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--integration")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			options.IntegrationPolicy = strings.ToLower(strings.TrimSpace(value))
			index++
		case strings.HasPrefix(token, "--integration="):
			options.IntegrationPolicy = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(token, "--integration=")))
		default:
			return localWorkStartOptions{}, fmt.Errorf("Unknown work-local start option: %s\n\n%s", token, WorkLocalHelp)
		}
	}

	if (strings.TrimSpace(options.Task) == "") == (strings.TrimSpace(options.PlanFile) == "") {
		return localWorkStartOptions{}, fmt.Errorf("Specify exactly one of --task or --plan-file.\n%s", WorkLocalHelp)
	}
	switch options.IntegrationPolicy {
	case "final", "always", "never":
	default:
		return localWorkStartOptions{}, fmt.Errorf("Invalid --integration value %q. Expected final, always, or never.\n%s", options.IntegrationPolicy, WorkLocalHelp)
	}
	return options, nil
}

func parseLocalWorkResumeArgs(args []string) (localWorkResumeOptions, error) {
	passthroughIndex := len(args)
	for index, token := range args {
		if token == "--" {
			passthroughIndex = index
			break
		}
	}
	selection, err := parseLocalWorkRunSelection(args[:passthroughIndex], true)
	if err != nil {
		return localWorkResumeOptions{}, err
	}
	options := localWorkResumeOptions{RunSelection: selection}
	if passthroughIndex < len(args) {
		options.CodexArgs = append([]string{}, args[passthroughIndex+1:]...)
	}
	return options, nil
}

func parseLocalWorkLogsArgs(args []string) (localWorkLogsOptions, error) {
	options := localWorkLogsOptions{
		RunSelection: localWorkRunSelection{UseLast: true},
		TailLines:    80,
	}
	selectionArgs := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--tail":
			value, err := requireLocalWorkFlagValue(args, index, "--tail")
			if err != nil {
				return localWorkLogsOptions{}, err
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed < 0 {
				return localWorkLogsOptions{}, fmt.Errorf("Invalid --tail value %q.\n%s", value, WorkLocalHelp)
			}
			options.TailLines = parsed
			index++
		case strings.HasPrefix(token, "--tail="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--tail="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				return localWorkLogsOptions{}, fmt.Errorf("Invalid --tail value %q.\n%s", value, WorkLocalHelp)
			}
			options.TailLines = parsed
		default:
			selectionArgs = append(selectionArgs, token)
		}
	}
	selection, err := parseLocalWorkRunSelection(selectionArgs, true)
	if err != nil {
		return localWorkLogsOptions{}, err
	}
	options.RunSelection = selection
	return options, nil
}

func parseLocalWorkRunSelection(args []string, defaultLast bool) (localWorkRunSelection, error) {
	selection := localWorkRunSelection{UseLast: defaultLast}
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--last":
			selection.UseLast = true
			selection.GlobalLast = false
		case token == "--global-last":
			selection.GlobalLast = true
			selection.UseLast = false
		case token == "--repo":
			value, err := requireLocalWorkFlagValue(args, index, "--repo")
			if err != nil {
				return localWorkRunSelection{}, err
			}
			selection.RepoPath = value
			index++
		case strings.HasPrefix(token, "--repo="):
			selection.RepoPath = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
		case token == "--run-id":
			value, err := requireLocalWorkFlagValue(args, index, "--run-id")
			if err != nil {
				return localWorkRunSelection{}, err
			}
			selection.RunID = strings.TrimSpace(value)
			selection.UseLast = false
			selection.GlobalLast = false
			index++
		case strings.HasPrefix(token, "--run-id="):
			selection.RunID = strings.TrimSpace(strings.TrimPrefix(token, "--run-id="))
			selection.UseLast = false
			selection.GlobalLast = false
		default:
			return localWorkRunSelection{}, fmt.Errorf("Unknown work-local selection option: %s\n\n%s", token, WorkLocalHelp)
		}
	}
	if selection.RunID != "" && (selection.UseLast || selection.GlobalLast) {
		return localWorkRunSelection{}, fmt.Errorf("Choose only one of --run-id, --last, or --global-last.\n%s", WorkLocalHelp)
	}
	if selection.UseLast && selection.GlobalLast {
		return localWorkRunSelection{}, fmt.Errorf("Choose only one of --last or --global-last.\n%s", WorkLocalHelp)
	}
	if selection.RunID == "" && !selection.UseLast && !selection.GlobalLast {
		selection.UseLast = defaultLast
	}
	return selection, nil
}

func requireLocalWorkFlagValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, WorkLocalHelp)
	}
	return args[index+1], nil
}

func startLocalWork(cwd string, options localWorkStartOptions) error {
	repoRoot, err := resolveLocalWorkRepoRoot(cwd, options.RepoPath)
	if err != nil {
		return err
	}
	if err := ensureLocalWorkRepoClean(repoRoot); err != nil {
		return err
	}
	inputContent, inputMode, err := readLocalWorkInput(cwd, options)
	if err != nil {
		return err
	}

	baselineSHAOutput, err := githubGitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	sourceBranchOutput, err := githubGitOutput(repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	baselineSHA := strings.TrimSpace(baselineSHAOutput)
	sourceBranch := strings.TrimSpace(sourceBranchOutput)
	repoID := localWorkRepoID(repoRoot)
	repoName := filepath.Base(repoRoot)
	runID := fmt.Sprintf("lw-%d", time.Now().UnixNano())

	repoDir := localWorkRepoDirByID(repoID)
	runDir := filepath.Join(repoDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	inputPath := filepath.Join(runDir, "input-plan.md")
	if err := os.WriteFile(inputPath, []byte(strings.TrimSpace(inputContent)+"\n"), 0o644); err != nil {
		return err
	}

	sandboxPath := filepath.Join(localWorkSandboxesDir(), repoID, runID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repoRoot, sandboxRepoPath); err != nil {
		return err
	}

	verificationPlan := detectGithubVerificationPlan(sandboxRepoPath)
	verificationScriptsDir, err := writeVerificationScripts(localWorkRuntimeName, sandboxPath, sandboxRepoPath, verificationPlan, []string{"nana", "work-local", "verify-refresh", "--run-id", runID})
	if err != nil {
		return err
	}

	now := ISOTimeNow()
	manifest := localWorkManifest{
		Version:                2,
		RunID:                  runID,
		CreatedAt:              now,
		UpdatedAt:              now,
		Status:                 "running",
		CurrentPhase:           "bootstrap",
		CurrentIteration:       0,
		RepoRoot:               repoRoot,
		RepoName:               repoName,
		RepoID:                 repoID,
		SourceBranch:           sourceBranch,
		BaselineSHA:            baselineSHA,
		SandboxPath:            sandboxPath,
		SandboxRepoPath:        sandboxRepoPath,
		VerificationPlan:       &verificationPlan,
		VerificationScriptsDir: verificationScriptsDir,
		InputPath:              inputPath,
		InputMode:              inputMode,
		IntegrationPolicy:      options.IntegrationPolicy,
		MaxIterations:          options.MaxIterations,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "[local] Starting run %s for %s\n", runID, repoRoot)
	fmt.Fprintf(os.Stdout, "[local] Managed sandbox: %s\n", sandboxPath)
	fmt.Fprintf(os.Stdout, "[local] Run artifacts: %s\n", runDir)
	fmt.Fprintf(os.Stdout, "[local] Verification policy: lint=%d compile=%d unit=%d integration=%d benchmark=%d integration_policy=%s\n",
		len(verificationPlan.Lint), len(verificationPlan.Compile), len(verificationPlan.Unit), len(verificationPlan.Integration), len(verificationPlan.Benchmarks), options.IntegrationPolicy)
	for _, warning := range verificationPlan.Warnings {
		fmt.Fprintf(os.Stdout, "[local] Verification warning: %s\n", warning)
	}

	return executeLocalWorkLoop(localWorkManifestPathByID(repoID, runID), options.CodexArgs)
}

func resumeLocalWork(cwd string, options localWorkResumeOptions) error {
	manifestPath, err := resolveLocalWorkManifestPath(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	var manifest localWorkManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return err
	}
	if manifest.Status == "completed" {
		return fmt.Errorf("work-local run %s is already completed", manifest.RunID)
	}
	if len(manifest.Iterations) >= manifest.MaxIterations {
		return fmt.Errorf("work-local run %s has already exhausted max iterations (%d)", manifest.RunID, manifest.MaxIterations)
	}
	fmt.Fprintf(os.Stdout, "[local] Resuming run %s for %s\n", manifest.RunID, manifest.RepoRoot)
	return executeLocalWorkLoop(manifestPath, options.CodexArgs)
}

func executeLocalWorkLoop(manifestPath string, codexArgs []string) error {
	var manifest localWorkManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return err
	}
	nextIteration := localWorkNextIteration(manifest)
	if nextIteration <= 0 {
		nextIteration = 1
	}
	runDir := filepath.Dir(manifestPath)

	for iteration := nextIteration; iteration <= manifest.MaxIterations; iteration++ {
		startedAt := ISOTimeNow()
		manifest.Status = "running"
		manifest.CurrentIteration = iteration
		manifest.CurrentPhase = "implement"
		manifest.UpdatedAt = startedAt
		manifest.LastError = ""
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}

		iterationDir := localWorkIterationDir(runDir, iteration)
		if err := os.MkdirAll(iterationDir, 0o755); err != nil {
			return err
		}

		implementPrompt, err := buildLocalWorkImplementPrompt(manifest, iteration)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(iterationDir, "implement-prompt.md"), []byte(implementPrompt), 0o644); err != nil {
			return err
		}
		implementResult, err := runLocalWorkCodexPrompt(manifest, codexArgs, implementPrompt, "leader")
		if err := os.WriteFile(filepath.Join(iterationDir, "implement-stdout.log"), []byte(implementResult.Stdout), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(iterationDir, "implement-stderr.log"), []byte(implementResult.Stderr), 0o644); err != nil {
			return err
		}
		if err != nil {
			manifest.Status = "failed"
			manifest.CurrentPhase = "implement"
			manifest.UpdatedAt = ISOTimeNow()
			manifest.LastError = err.Error()
			if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
				return writeErr
			}
			return err
		}

		manifest.CurrentPhase = "verify-refresh"
		manifest.UpdatedAt = ISOTimeNow()
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}
		plan, scriptsDir, err := refreshLocalWorkVerificationArtifactsInPlace(&manifest)
		if err != nil {
			return err
		}
		manifest.VerificationPlan = &plan
		manifest.VerificationScriptsDir = scriptsDir
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}

		manifest.CurrentPhase = "verify"
		manifest.UpdatedAt = ISOTimeNow()
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}
		initialVerification, err := runLocalVerification(manifest.SandboxRepoPath, plan, manifest.IntegrationPolicy == "always")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(iterationDir, "verification.json"), mustMarshalJSON(initialVerification), 0o644); err != nil {
			return err
		}

		manifest.CurrentPhase = "review"
		manifest.UpdatedAt = ISOTimeNow()
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}
		reviewPrompt, err := buildLocalWorkReviewPrompt(manifest)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(iterationDir, "review-prompt.md"), []byte(reviewPrompt), 0o644); err != nil {
			return err
		}
		reviewResult, initialFindings, err := runLocalWorkReview(manifest, codexArgs, reviewPrompt)
		if err := os.WriteFile(filepath.Join(iterationDir, "review-stdout.log"), []byte(reviewResult.Stdout), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(iterationDir, "review-stderr.log"), []byte(reviewResult.Stderr), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(iterationDir, "review-findings.json"), mustMarshalJSON(initialFindings), 0o644); err != nil {
			return err
		}
		if err != nil {
			manifest.Status = "failed"
			manifest.CurrentPhase = "review"
			manifest.UpdatedAt = ISOTimeNow()
			manifest.LastError = err.Error()
			if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
				return writeErr
			}
			return err
		}

		filteredInitialFindings := filterRejectedFindings(initialFindings, manifest.RejectedFindingFingerprints)
		initialGroups, err := groupFindingsByAI(manifest, codexArgs, iterationDir, 0, filteredInitialFindings)
		if err != nil {
			return err
		}
		if err := writeJSONArtifact(filepath.Join(iterationDir, "grouped-findings-initial.json"), initialGroups); err != nil {
			return err
		}
		validatedInitialFindings, rejectedInitialFingerprints, err := validateFindingGroups(manifest, codexArgs, iterationDir, 0, initialGroups)
		if err != nil {
			return err
		}
		if err := writeJSONArtifact(filepath.Join(iterationDir, "validated-findings-initial.json"), validatedInitialFindings); err != nil {
			return err
		}
		if err := writeJSONArtifact(filepath.Join(iterationDir, "rejected-findings-initial.json"), rejectedInitialFingerprints); err != nil {
			return err
		}
		manifest.RejectedFindingFingerprints = uniqueStrings(append(manifest.RejectedFindingFingerprints, rejectedInitialFingerprints...))
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}

		finalVerification := initialVerification
		finalFindings := findingsFromValidated(validatedInitialFindings)
		validationGroupIDs := groupIDs(initialGroups)
		validatedFindingCount := len(validatedInitialFindings)
		rejectedFindingsCount := len(rejectedInitialFingerprints)
		modifiedFindingsCount := countValidatedFindingsByStatus(validatedInitialFindings, localWorkFindingModified)
		confirmedFindingsCount := countValidatedFindingsByStatus(validatedInitialFindings, localWorkFindingConfirmed)
		reviewRoundFingerprints := []string{}
		reviewFindingsByRound := []int{}
		hardeningRoundFingerprints := []string{}
		postHardeningVerificationFingerprints := []string{}
		roundsUsed := 0

		if err := os.WriteFile(filepath.Join(iterationDir, "review-initial-prompt.md"), []byte(reviewPrompt), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(iterationDir, "review-initial-findings.json"), mustMarshalJSON(initialFindings), 0o644); err != nil {
			return err
		}

		for round := 1; round <= localWorkMaxReviewRounds && (!finalVerification.Passed || len(finalFindings) > 0); round++ {
			roundsUsed = round

			manifest.CurrentPhase = "harden"
			manifest.UpdatedAt = ISOTimeNow()
			if err := writeLocalWorkManifest(manifest); err != nil {
				return err
			}

			hardeningPrompt, err := buildLocalWorkHardeningPrompt(manifest, finalVerification, finalFindings)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("hardening-round-%d-prompt.md", round)), []byte(hardeningPrompt), 0o644); err != nil {
				return err
			}
			hardeningResult, err := runLocalWorkCodexPrompt(manifest, codexArgs, hardeningPrompt, fmt.Sprintf("hardener-round-%d", round))
			if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("hardening-round-%d-stdout.log", round)), []byte(hardeningResult.Stdout), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("hardening-round-%d-stderr.log", round)), []byte(hardeningResult.Stderr), 0o644); err != nil {
				return err
			}
			if err != nil {
				manifest.Status = "failed"
				manifest.CurrentPhase = "harden"
				manifest.UpdatedAt = ISOTimeNow()
				manifest.LastError = err.Error()
				if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
					return writeErr
				}
				return err
			}
			hardeningRoundFingerprints = append(hardeningRoundFingerprints, sha256Hex(strings.TrimSpace(hardeningResult.Stdout)+"\n"+strings.TrimSpace(hardeningResult.Stderr)))

			manifest.CurrentPhase = "verify-post-hardening"
			manifest.UpdatedAt = ISOTimeNow()
			if err := writeLocalWorkManifest(manifest); err != nil {
				return err
			}
			finalVerification, err = runLocalVerification(manifest.SandboxRepoPath, plan, manifest.IntegrationPolicy == "always")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("verification-round-%d-post-hardening.json", round)), mustMarshalJSON(finalVerification), 0o644); err != nil {
				return err
			}
			postHardeningVerificationFingerprints = append(postHardeningVerificationFingerprints, fingerprintVerificationReport(finalVerification))

			manifest.CurrentPhase = "review-post-hardening"
			manifest.UpdatedAt = ISOTimeNow()
			if err := writeLocalWorkManifest(manifest); err != nil {
				return err
			}
			finalReviewPrompt, err := buildLocalWorkReviewPrompt(manifest)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-prompt.md", round)), []byte(finalReviewPrompt), 0o644); err != nil {
				return err
			}
			finalReviewResult, postHardeningFindings, err := runLocalWorkReview(manifest, codexArgs, finalReviewPrompt)
			if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-stdout.log", round)), []byte(finalReviewResult.Stdout), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-stderr.log", round)), []byte(finalReviewResult.Stderr), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-findings.json", round)), mustMarshalJSON(postHardeningFindings), 0o644); err != nil {
				return err
			}
			if err != nil {
				manifest.Status = "failed"
				manifest.CurrentPhase = "review-post-hardening"
				manifest.UpdatedAt = ISOTimeNow()
				manifest.LastError = err.Error()
				if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
					return writeErr
				}
				return err
			}
			filteredRoundFindings := filterRejectedFindings(postHardeningFindings, manifest.RejectedFindingFingerprints)
			roundGroups, err := groupFindingsByAI(manifest, codexArgs, iterationDir, round, filteredRoundFindings)
			if err != nil {
				return err
			}
			if err := writeJSONArtifact(filepath.Join(iterationDir, fmt.Sprintf("grouped-findings-round-%d.json", round)), roundGroups); err != nil {
				return err
			}
			validatedRoundFindings, rejectedRoundFingerprints, err := validateFindingGroups(manifest, codexArgs, iterationDir, round, roundGroups)
			if err != nil {
				return err
			}
			if err := writeJSONArtifact(filepath.Join(iterationDir, fmt.Sprintf("validated-findings-round-%d.json", round)), validatedRoundFindings); err != nil {
				return err
			}
			if err := writeJSONArtifact(filepath.Join(iterationDir, fmt.Sprintf("rejected-findings-round-%d.json", round)), rejectedRoundFingerprints); err != nil {
				return err
			}
			manifest.RejectedFindingFingerprints = uniqueStrings(append(manifest.RejectedFindingFingerprints, rejectedRoundFingerprints...))
			if err := writeLocalWorkManifest(manifest); err != nil {
				return err
			}
			finalFindings = findingsFromValidated(validatedRoundFindings)
			validationGroupIDs = append(validationGroupIDs, groupIDs(roundGroups)...)
			validatedFindingCount += len(validatedRoundFindings)
			rejectedFindingsCount += len(rejectedRoundFingerprints)
			modifiedFindingsCount += countValidatedFindingsByStatus(validatedRoundFindings, localWorkFindingModified)
			confirmedFindingsCount += countValidatedFindingsByStatus(validatedRoundFindings, localWorkFindingConfirmed)
			reviewRoundFingerprints = append(reviewRoundFingerprints, sha256Hex(strings.Join(reviewFindingFingerprints(finalFindings), "\n")))
			reviewFindingsByRound = append(reviewFindingsByRound, len(finalFindings))
		}

		if roundsUsed == 0 {
			if err := os.WriteFile(filepath.Join(iterationDir, "verification-round-0-post-hardening.json"), mustMarshalJSON(finalVerification), 0o644); err != nil {
				return err
			}
		}

		integrationRan := manifest.IntegrationPolicy == "always" && len(plan.Integration) > 0
		if finalVerification.Passed && len(finalFindings) == 0 && manifest.IntegrationPolicy == "final" {
			integrationReport, err := runLocalIntegrationVerification(manifest.SandboxRepoPath, plan)
			if err != nil {
				return err
			}
			if integrationReport.GeneratedAt != "" {
				finalVerification.Stages = append(finalVerification.Stages, integrationReport.Stages...)
				finalVerification.FailedStages = append(uniqueStrings(finalVerification.FailedStages), integrationReport.FailedStages...)
				finalVerification.Passed = finalVerification.Passed && integrationReport.Passed
				finalVerification.IntegrationIncluded = true
				integrationRan = true
				verificationRound := roundsUsed
				if err := os.WriteFile(filepath.Join(iterationDir, fmt.Sprintf("verification-round-%d-post-hardening.json", verificationRound)), mustMarshalJSON(finalVerification), 0o644); err != nil {
					return err
				}
				finalFingerprint := fingerprintVerificationReport(finalVerification)
				if roundsUsed == 0 {
					postHardeningVerificationFingerprints = append(postHardeningVerificationFingerprints[:0], finalFingerprint)
				} else if len(postHardeningVerificationFingerprints) > 0 {
					postHardeningVerificationFingerprints[len(postHardeningVerificationFingerprints)-1] = finalFingerprint
				}
			}
		}

		diffOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", manifest.BaselineSHA)
		if err != nil {
			return err
		}
		verificationSummary := summarizeLocalVerification(finalVerification)
		summary := localWorkIterationSummary{
			Iteration:                             iteration,
			StartedAt:                             startedAt,
			CompletedAt:                           ISOTimeNow(),
			Status:                                "retrying",
			DiffFingerprint:                       sha256Hex(diffOutput),
			VerificationFingerprint:               fingerprintVerificationReport(finalVerification),
			ReviewFingerprint:                     sha256Hex(strings.Join(reviewFindingFingerprints(finalFindings), "\n")),
			InitialReviewFingerprint:              sha256Hex(strings.Join(reviewFindingFingerprints(initialFindings), "\n")),
			HardeningFingerprint:                  strings.Join(hardeningRoundFingerprints, "|"),
			PostHardeningVerificationFingerprint:  strings.Join(postHardeningVerificationFingerprints, "|"),
			VerificationPassed:                    finalVerification.Passed,
			VerificationFailedStages:              append([]string{}, finalVerification.FailedStages...),
			VerificationSummary:                   verificationSummary,
			InitialReviewFindings:                 len(initialFindings),
			ValidatedFindings:                     validatedFindingCount,
			ConfirmedFindings:                     confirmedFindingsCount,
			RejectedFindings:                      rejectedFindingsCount,
			ModifiedFindings:                      modifiedFindingsCount,
			ValidationGroups:                      uniqueStrings(validationGroupIDs),
			ReviewRoundsUsed:                      roundsUsed,
			ReviewFindingsByRound:                 append([]int{}, reviewFindingsByRound...),
			ReviewRoundFingerprints:               append([]string{}, reviewRoundFingerprints...),
			HardeningRoundFingerprints:            append([]string{}, hardeningRoundFingerprints...),
			PostHardeningVerificationFingerprints: append([]string{}, postHardeningVerificationFingerprints...),
			ReviewFindings:                        len(finalFindings),
			ReviewFindingTitles:                   reviewFindingTitles(finalFindings),
			IntegrationRan:                        integrationRan,
		}
		if finalVerification.Passed && len(finalFindings) == 0 {
			summary.Status = "completed"
		}

		manifest.Iterations = append(manifest.Iterations, summary)
		manifest.UpdatedAt = summary.CompletedAt
		manifest.CurrentPhase = "iteration-complete"
		if summary.Status == "completed" {
			manifest.Status = "completed"
			manifest.CurrentPhase = "completed"
			manifest.CompletedAt = summary.CompletedAt
		}
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}

		fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: %s\n", iteration, manifest.MaxIterations, summary.VerificationSummary)
		if len(finalFindings) > 0 {
			fmt.Fprintf(os.Stdout, "[local] Iteration %d review findings: %d\n", iteration, len(finalFindings))
		}

		if summary.Status == "completed" {
			if _, err := writeLocalWorkRetrospective(manifest); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "[local] Completed run %s after %d iteration(s).\n", manifest.RunID, iteration)
			return nil
		}

		if stallReason := detectLocalWorkStall(manifest.Iterations); stallReason != "" {
			manifest.Status = "failed"
			manifest.CurrentPhase = "stalled"
			manifest.LastError = stallReason
			manifest.UpdatedAt = ISOTimeNow()
			if err := writeLocalWorkManifest(manifest); err != nil {
				return err
			}
			if _, err := writeLocalWorkRetrospective(manifest); err != nil {
				return err
			}
			return errors.New(stallReason)
		}
	}

	manifest.Status = "failed"
	manifest.CurrentPhase = "max-iterations"
	manifest.LastError = fmt.Sprintf("work-local run %s reached max iterations (%d)", manifest.RunID, manifest.MaxIterations)
	manifest.UpdatedAt = ISOTimeNow()
	if err := writeLocalWorkManifest(manifest); err != nil {
		return err
	}
	if _, err := writeLocalWorkRetrospective(manifest); err != nil {
		return err
	}
	return errors.New(manifest.LastError)
}

func localWorkStatus(cwd string, selection localWorkRunSelection) error {
	manifestPath, err := resolveLocalWorkManifestPath(cwd, selection)
	if err != nil {
		return err
	}
	var manifest localWorkManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[local] Run id: %s\n", manifest.RunID)
	fmt.Fprintf(os.Stdout, "[local] Repo root: %s\n", manifest.RepoRoot)
	fmt.Fprintf(os.Stdout, "[local] Run artifacts: %s\n", filepath.Dir(manifestPath))
	fmt.Fprintf(os.Stdout, "[local] Sandbox: %s\n", manifest.SandboxPath)
	fmt.Fprintf(os.Stdout, "[local] Status: %s\n", manifest.Status)
	fmt.Fprintf(os.Stdout, "[local] Iteration: %d/%d (phase=%s)\n", manifest.CurrentIteration, manifest.MaxIterations, defaultString(manifest.CurrentPhase, "n/a"))
	if len(manifest.Iterations) > 0 {
		last := manifest.Iterations[len(manifest.Iterations)-1]
		fmt.Fprintf(os.Stdout, "[local] Last verification: %s\n", last.VerificationSummary)
		fmt.Fprintf(os.Stdout, "[local] Last review findings: %d\n", last.ReviewFindings)
	}
	if strings.TrimSpace(manifest.LastError) != "" {
		fmt.Fprintf(os.Stdout, "[local] Last error: %s\n", manifest.LastError)
	}
	return nil
}

func localWorkLogs(cwd string, options localWorkLogsOptions) error {
	manifestPath, err := resolveLocalWorkManifestPath(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	var manifest localWorkManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return err
	}
	iteration := manifest.CurrentIteration
	if iteration <= 0 && len(manifest.Iterations) > 0 {
		iteration = manifest.Iterations[len(manifest.Iterations)-1].Iteration
	}
	if iteration <= 0 {
		return fmt.Errorf("work-local run %s has no iteration artifacts yet", manifest.RunID)
	}
	iterationDir := localWorkIterationDir(filepath.Dir(manifestPath), iteration)
	if _, err := os.Stat(iterationDir); err != nil {
		return fmt.Errorf("work-local run %s iteration %d logs not found at %s", manifest.RunID, iteration, iterationDir)
	}

	fmt.Fprintf(os.Stdout, "[local] Run id: %s\n", manifest.RunID)
	fmt.Fprintf(os.Stdout, "[local] Iteration: %d\n", iteration)
	fmt.Fprintf(os.Stdout, "[local] Iteration artifacts: %s\n", iterationDir)

	patterns := []string{
		"implement-stdout.log",
		"implement-stderr.log",
		"review-stdout.log",
		"review-stderr.log",
		"grouped-findings-*.json",
		"validated-findings-*.json",
		"rejected-findings-*.json",
		"hardening-round-*-stdout.log",
		"hardening-round-*-stderr.log",
		"review-round-*-stdout.log",
		"review-round-*-stderr.log",
		"verification.json",
		"verification-round-*-post-hardening.json",
		"review-initial-findings.json",
		"review-round-*-findings.json",
		"validation-groups/*/*.json",
		"validation-groups/*/*.log",
	}

	seen := map[string]bool{}
	files := []string{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(iterationDir, pattern))
		if err != nil {
			return err
		}
		for _, match := range matches {
			if seen[match] {
				continue
			}
			seen[match] = true
			files = append(files, match)
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("work-local run %s iteration %d has no log files yet", manifest.RunID, iteration)
	}
	sort.Strings(files)

	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		display := string(content)
		if options.TailLines > 0 {
			display = tailLines(display, options.TailLines)
		}
		fmt.Fprintf(os.Stdout, "\n== %s ==\n", filepath.Base(path))
		if strings.TrimSpace(display) == "" {
			fmt.Fprintln(os.Stdout, "(empty)")
			continue
		}
		fmt.Fprint(os.Stdout, display)
		if !strings.HasSuffix(display, "\n") {
			fmt.Fprintln(os.Stdout)
		}
	}
	return nil
}

func localWorkRetrospective(cwd string, selection localWorkRunSelection) error {
	manifestPath, err := resolveLocalWorkManifestPath(cwd, selection)
	if err != nil {
		return err
	}
	var manifest localWorkManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return err
	}
	content, err := writeLocalWorkRetrospective(manifest)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stdout, content)
	return nil
}

func refreshLocalWorkVerificationArtifacts(cwd string, selection localWorkRunSelection) error {
	manifestPath, err := resolveLocalWorkManifestPath(cwd, selection)
	if err != nil {
		return err
	}
	var manifest localWorkManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return err
	}
	plan, scriptsDir, err := refreshLocalWorkVerificationArtifactsInPlace(&manifest)
	if err != nil {
		return err
	}
	manifest.VerificationPlan = &plan
	manifest.VerificationScriptsDir = scriptsDir
	manifest.UpdatedAt = ISOTimeNow()
	if err := writeLocalWorkManifest(manifest); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[local] Verification artifacts for run %s refreshed.\n", manifest.RunID)
	fmt.Fprintf(os.Stdout, "[local] Verification scripts directory: %s\n", scriptsDir)
	return nil
}

func refreshLocalWorkVerificationArtifactsInPlace(manifest *localWorkManifest) (githubVerificationPlan, string, error) {
	plan := detectGithubVerificationPlan(manifest.SandboxRepoPath)
	scriptsDir, err := writeVerificationScripts(localWorkRuntimeName, manifest.SandboxPath, manifest.SandboxRepoPath, plan, []string{"nana", "work-local", "verify-refresh", "--run-id", manifest.RunID})
	if err != nil {
		return githubVerificationPlan{}, "", err
	}
	return plan, scriptsDir, nil
}

func runLocalVerification(repoPath string, plan githubVerificationPlan, includeIntegration bool) (localWorkVerificationReport, error) {
	stages := []struct {
		name     string
		commands []string
	}{
		{name: "lint", commands: append([]string{}, plan.Lint...)},
		{name: "compile", commands: append([]string{}, plan.Compile...)},
		{name: "unit", commands: append([]string{}, plan.Unit...)},
	}
	if includeIntegration {
		stages = append(stages, struct {
			name     string
			commands []string
		}{name: "integration", commands: append([]string{}, plan.Integration...)})
	}
	return runLocalVerificationStages(repoPath, plan.PlanFingerprint, includeIntegration, stages)
}

func runLocalIntegrationVerification(repoPath string, plan githubVerificationPlan) (localWorkVerificationReport, error) {
	if len(plan.Integration) == 0 {
		return localWorkVerificationReport{}, nil
	}
	return runLocalVerificationStages(repoPath, plan.PlanFingerprint, true, []struct {
		name     string
		commands []string
	}{{name: "integration", commands: append([]string{}, plan.Integration...)}})
}

func runLocalVerificationStages(repoPath string, fingerprint string, includeIntegration bool, stages []struct {
	name     string
	commands []string
}) (localWorkVerificationReport, error) {
	report := localWorkVerificationReport{
		GeneratedAt:         ISOTimeNow(),
		PlanFingerprint:     fingerprint,
		IntegrationIncluded: includeIntegration,
		Passed:              true,
	}
	cache := map[string]localWorkVerificationCommandResult{}
	for _, stage := range stages {
		stageResult := localWorkVerificationStageResult{Name: stage.name, Status: "skipped"}
		if len(stage.commands) == 0 {
			report.Stages = append(report.Stages, stageResult)
			continue
		}
		stageResult.Status = "passed"
		for _, command := range stage.commands {
			result, ok := cache[command]
			if ok {
				result.Cached = true
			} else {
				executed, err := executeLocalVerificationCommand(repoPath, command)
				if err != nil {
					return localWorkVerificationReport{}, err
				}
				result = executed
				cache[command] = result
			}
			stageResult.Commands = append(stageResult.Commands, result)
			if result.ExitCode != 0 {
				stageResult.Status = "failed"
				report.Passed = false
				report.FailedStages = append(report.FailedStages, stage.name)
				break
			}
		}
		report.Stages = append(report.Stages, stageResult)
	}
	report.FailedStages = uniqueStrings(report.FailedStages)
	return report, nil
}

func executeLocalVerificationCommand(repoPath string, command string) (localWorkVerificationCommandResult, error) {
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = repoPath
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return localWorkVerificationCommandResult{}, err
		}
	}
	output := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String())}, "\n"))
	return localWorkVerificationCommandResult{
		Command:  command,
		ExitCode: exitCode,
		Output:   output,
	}, nil
}

func runLocalWorkCodexPrompt(manifest localWorkManifest, codexArgs []string, prompt string, codexHomeAlias string) (localWorkExecutionResult, error) {
	sourceCodexHome := ResolveCodexHomeForLaunch(manifest.RepoRoot)
	scopedCodexHome, err := ensureScopedCodexHome(sourceCodexHome, filepath.Join(manifest.SandboxPath, ".nana", localWorkRuntimeName, "codex-home", sanitizePathToken(codexHomeAlias)))
	if err != nil {
		return localWorkExecutionResult{}, err
	}
	sessionID := fmt.Sprintf("%s-%d", codexHomeAlias, time.Now().UnixNano())
	sessionInstructionsPath, err := writeSessionModelInstructions(manifest.SandboxPath, sessionID, scopedCodexHome)
	if err != nil {
		return localWorkExecutionResult{}, err
	}
	defer removeSessionInstructionsFile(manifest.SandboxPath, sessionID)

	normalizedCodexArgs := normalizeLocalWorkCodexArgs(codexArgs)
	args := append([]string{"exec", "-C", manifest.SandboxRepoPath}, normalizedCodexArgs...)
	args = append(args, "-")
	args = injectModelInstructionsArgs(args, sessionInstructionsPath)

	cmd := exec.Command("codex", args...)
	cmd.Dir = manifest.SandboxPath
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(buildCodexEnv(NotifyTempContract{}, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+manifest.SandboxRepoPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return localWorkExecutionResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}, err
}

func normalizeLocalWorkCodexArgs(args []string) []string {
	normalized := NormalizeCodexLaunchArgs(args)
	if !hasCodexExecutionPolicyArg(normalized) {
		normalized = append([]string{CodexBypassFlag}, normalized...)
	}
	return normalized
}

func hasCodexExecutionPolicyArg(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == CodexBypassFlag, arg == "--full-auto":
			return true
		case arg == "--sandbox" || arg == "-s":
			return true
		case strings.HasPrefix(arg, "--sandbox="):
			return true
		}
	}
	return false
}

func runLocalWorkReview(manifest localWorkManifest, codexArgs []string, prompt string) (localWorkExecutionResult, []githubPullReviewFinding, error) {
	result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, "reviewer")
	if err != nil {
		return result, nil, err
	}
	findings, parseErr := parseLocalReviewFindings(result.Stdout)
	if parseErr != nil {
		return result, nil, parseErr
	}
	return result, findings, nil
}

func groupFindingsByAI(manifest localWorkManifest, codexArgs []string, iterationDir string, round int, findings []githubPullReviewFinding) ([]localWorkFindingGroup, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	prompt, err := buildLocalWorkGroupingPrompt(manifest, findings)
	if err != nil {
		return nil, err
	}
	prefix := "grouping-initial"
	alias := "grouper-initial"
	if round > 0 {
		prefix = fmt.Sprintf("grouping-round-%d", round)
		alias = fmt.Sprintf("grouper-round-%d", round)
	}
	if err := os.WriteFile(filepath.Join(iterationDir, prefix+"-prompt.md"), []byte(prompt), 0o644); err != nil {
		return nil, err
	}
	result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, alias)
	if err := os.WriteFile(filepath.Join(iterationDir, prefix+"-stdout.log"), []byte(result.Stdout), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(iterationDir, prefix+"-stderr.log"), []byte(result.Stderr), 0o644); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	groupingResult, err := parseLocalWorkGroupingResult(result.Stdout)
	if err != nil {
		return nil, err
	}
	if err := writeJSONArtifact(filepath.Join(iterationDir, prefix+"-result.json"), groupingResult); err != nil {
		return nil, err
	}
	return buildFindingGroupsFromGroupingResult(findings, groupingResult)
}

func validateFindingGroups(manifest localWorkManifest, codexArgs []string, iterationDir string, round int, groups []localWorkFindingGroup) ([]localWorkValidatedFinding, []string, error) {
	if len(groups) == 0 {
		return nil, nil, nil
	}

	type groupOutcome struct {
		groupID   string
		validated []localWorkValidatedFinding
		rejected  []string
		err       error
	}

	sem := make(chan struct{}, localWorkValidationParallelism)
	results := make(chan groupOutcome, len(groups))
	var wg sync.WaitGroup

	for _, group := range groups {
		group := group
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			validated, rejected, err := validateFindingGroup(manifest, codexArgs, iterationDir, round, group)
			results <- groupOutcome{
				groupID:   group.GroupID,
				validated: validated,
				rejected:  rejected,
				err:       err,
			}
		}()
	}

	wg.Wait()
	close(results)

	outcomes := make([]groupOutcome, 0, len(groups))
	for outcome := range results {
		if outcome.err != nil {
			return nil, nil, outcome.err
		}
		outcomes = append(outcomes, outcome)
	}
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].groupID < outcomes[j].groupID })

	validated := []localWorkValidatedFinding{}
	rejected := []string{}
	for _, outcome := range outcomes {
		validated = append(validated, outcome.validated...)
		rejected = append(rejected, outcome.rejected...)
	}
	return validated, uniqueStrings(rejected), nil
}

func validateFindingGroup(manifest localWorkManifest, codexArgs []string, iterationDir string, round int, group localWorkFindingGroup) ([]localWorkValidatedFinding, []string, error) {
	groupDir := filepath.Join(iterationDir, "validation-groups", sanitizePathToken(group.GroupID))
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return nil, nil, err
	}
	prompt, err := buildLocalWorkValidationPrompt(manifest, group)
	if err != nil {
		return nil, nil, err
	}
	promptPath := filepath.Join(groupDir, fmt.Sprintf("round-%d-validator-prompt.md", round))
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return nil, nil, err
	}
	result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, fmt.Sprintf("validator-%s-round-%d", sanitizePathToken(group.GroupID), round))
	if err := os.WriteFile(filepath.Join(groupDir, fmt.Sprintf("round-%d-validator-stdout.log", round)), []byte(result.Stdout), 0o644); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(groupDir, fmt.Sprintf("round-%d-validator-stderr.log", round)), []byte(result.Stderr), 0o644); err != nil {
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	groupResult, err := parseLocalWorkValidationGroupResult(result.Stdout, group)
	if err != nil {
		return nil, nil, err
	}
	if err := writeJSONArtifact(filepath.Join(groupDir, fmt.Sprintf("round-%d-validator-result.json", round)), groupResult); err != nil {
		return nil, nil, err
	}

	decisionsByFingerprint := map[string]localWorkValidationDecision{}
	for _, decision := range groupResult.Decisions {
		decisionsByFingerprint[decision.Fingerprint] = decision
	}

	validated := []localWorkValidatedFinding{}
	rejected := []string{}
	for _, finding := range group.Findings {
		decision, ok := decisionsByFingerprint[finding.Fingerprint]
		if !ok {
			validated = append(validated, localWorkValidatedFinding{
				GroupID:             group.GroupID,
				OriginalFingerprint: finding.Fingerprint,
				CurrentFingerprint:  finding.Fingerprint,
				Status:              localWorkFindingConfirmed,
				Reason:              "validator omitted explicit decision; defaulted to confirmed",
				Finding:             &finding,
			})
			continue
		}
		switch decision.Status {
		case localWorkFindingRejected:
			validated = append(validated, localWorkValidatedFinding{
				GroupID:             group.GroupID,
				OriginalFingerprint: finding.Fingerprint,
				CurrentFingerprint:  finding.Fingerprint,
				Status:              localWorkFindingRejected,
				Reason:              strings.TrimSpace(decision.Reason),
			})
			rejected = append(rejected, finding.Fingerprint)
		case localWorkFindingModified:
			replacement := cloneFindingOrOriginal(decision.Replacement, finding)
			replacementFingerprint := buildGithubPullReviewFindingFingerprint(replacement.Title, replacement.Path, replacement.Line, replacement.Summary)
			replacement.Fingerprint = replacementFingerprint
			validated = append(validated,
				localWorkValidatedFinding{
					GroupID:               group.GroupID,
					OriginalFingerprint:   finding.Fingerprint,
					CurrentFingerprint:    finding.Fingerprint,
					Status:                localWorkFindingSuperseded,
					Reason:                strings.TrimSpace(decision.Reason),
					SupersedesFingerprint: replacementFingerprint,
					Finding:               &finding,
				},
				localWorkValidatedFinding{
					GroupID:               group.GroupID,
					OriginalFingerprint:   finding.Fingerprint,
					CurrentFingerprint:    replacementFingerprint,
					Status:                localWorkFindingModified,
					Reason:                strings.TrimSpace(decision.Reason),
					SupersedesFingerprint: finding.Fingerprint,
					Finding:               &replacement,
				},
			)
		default:
			validated = append(validated, localWorkValidatedFinding{
				GroupID:             group.GroupID,
				OriginalFingerprint: finding.Fingerprint,
				CurrentFingerprint:  finding.Fingerprint,
				Status:              localWorkFindingConfirmed,
				Reason:              strings.TrimSpace(decision.Reason),
				Finding:             &finding,
			})
		}
	}

	return validated, uniqueStrings(rejected), nil
}

func cloneFindingOrOriginal(candidate *githubPullReviewFinding, original githubPullReviewFinding) githubPullReviewFinding {
	if candidate == nil {
		return original
	}
	cloned := *candidate
	if strings.TrimSpace(cloned.Title) == "" {
		cloned.Title = original.Title
	}
	if strings.TrimSpace(cloned.Path) == "" {
		cloned.Path = original.Path
	}
	if cloned.Line == 0 {
		cloned.Line = original.Line
	}
	if strings.TrimSpace(cloned.Summary) == "" {
		cloned.Summary = original.Summary
	}
	if strings.TrimSpace(cloned.Detail) == "" {
		cloned.Detail = original.Detail
	}
	if strings.TrimSpace(cloned.Fix) == "" {
		cloned.Fix = original.Fix
	}
	if strings.TrimSpace(cloned.Rationale) == "" {
		cloned.Rationale = original.Rationale
	}
	cloned.Severity = normalizeGithubSeverity(cloned.Severity)
	return cloned
}

func parseLocalReviewFindings(raw string) ([]githubPullReviewFinding, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("review output did not contain JSON object")
	}
	var payload struct {
		Findings []struct {
			Title     string `json:"title"`
			Severity  string `json:"severity"`
			Path      string `json:"path"`
			Line      int    `json:"line"`
			Summary   string `json:"summary"`
			Detail    string `json:"detail"`
			Fix       string `json:"fix"`
			Rationale string `json:"rationale"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err != nil {
		return nil, err
	}
	findings := make([]githubPullReviewFinding, 0, len(payload.Findings))
	for _, finding := range payload.Findings {
		if strings.TrimSpace(finding.Path) == "" || strings.TrimSpace(finding.Title) == "" || strings.TrimSpace(finding.Detail) == "" {
			continue
		}
		summary := strings.TrimSpace(finding.Summary)
		if summary == "" {
			summary = strings.TrimSpace(finding.Detail)
		}
		findings = append(findings, githubPullReviewFinding{
			Fingerprint: buildGithubPullReviewFindingFingerprint(finding.Title, finding.Path, finding.Line, summary),
			Title:       strings.TrimSpace(finding.Title),
			Path:        strings.TrimSpace(finding.Path),
			Line:        finding.Line,
			Severity:    normalizeGithubSeverity(finding.Severity),
			Summary:     summary,
			Detail:      strings.TrimSpace(finding.Detail),
			Fix:         strings.TrimSpace(finding.Fix),
			Rationale:   strings.TrimSpace(finding.Rationale),
		})
	}
	return findings, nil
}

func buildLocalWorkGroupingPrompt(manifest localWorkManifest, findings []githubPullReviewFinding) (string, error) {
	lines := []string{
		"# NANA Work-local Finding Grouping",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Sandbox repo path: %s", manifest.SandboxRepoPath),
		"",
		"Group findings by locality and shared investigative context.",
		"- Keep findings that likely share a root cause or adjacent code context together.",
		"- Favor efficient investigation and remediation grouping over deterministic path prefixes.",
		fmt.Sprintf("- Maximum groups: %d", localWorkMaxValidationGroups),
		fmt.Sprintf("- Maximum findings per group: %d", localWorkMaxFindingsPerGroup),
		"- Every finding must appear exactly once.",
		"- Use singleton groups when shared context is weak.",
		"",
		"Return JSON only.",
		`Schema: {"groups":[{"group_id":"...","rationale":"...","findings":["fingerprint-1","fingerprint-2"]}]}`,
		"",
		"Findings:",
	}
	for _, finding := range findings {
		lines = append(lines,
			fmt.Sprintf("- fingerprint: %s", finding.Fingerprint),
			fmt.Sprintf("  path: %s", finding.Path),
			fmt.Sprintf("  title: %s", finding.Title),
			fmt.Sprintf("  summary: %s", promptSnippet(finding.Summary)),
			fmt.Sprintf("  detail: %s", promptSnippet(finding.Detail)),
		)
	}
	return capPromptSize(strings.Join(lines, "\n")+"\n", localWorkPromptCharLimit), nil
}

func parseLocalWorkGroupingResult(raw string) (localWorkGroupingResult, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return localWorkGroupingResult{}, fmt.Errorf("grouping output did not contain JSON object")
	}
	var result localWorkGroupingResult
	if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err != nil {
		return localWorkGroupingResult{}, err
	}
	return result, nil
}

func buildFindingGroupsFromGroupingResult(findings []githubPullReviewFinding, grouping localWorkGroupingResult) ([]localWorkFindingGroup, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	if len(grouping.Groups) == 0 {
		return nil, fmt.Errorf("grouping result did not contain any groups")
	}
	if len(grouping.Groups) > localWorkMaxValidationGroups {
		return nil, fmt.Errorf("grouping result exceeded max groups (%d)", localWorkMaxValidationGroups)
	}
	byFingerprint := map[string]githubPullReviewFinding{}
	for _, finding := range findings {
		byFingerprint[finding.Fingerprint] = finding
	}
	assigned := map[string]string{}
	grouped := make([]localWorkFindingGroup, 0, len(grouping.Groups))
	for _, group := range grouping.Groups {
		groupID := strings.TrimSpace(group.GroupID)
		if groupID == "" {
			return nil, fmt.Errorf("grouping result contained an empty group_id")
		}
		if len(group.Findings) == 0 {
			return nil, fmt.Errorf("group %s does not contain any findings", groupID)
		}
		if len(group.Findings) > localWorkMaxFindingsPerGroup {
			return nil, fmt.Errorf("group %s exceeded max findings (%d)", groupID, localWorkMaxFindingsPerGroup)
		}
		items := make([]githubPullReviewFinding, 0, len(group.Findings))
		for _, fingerprint := range group.Findings {
			fingerprint = strings.TrimSpace(fingerprint)
			finding, ok := byFingerprint[fingerprint]
			if !ok {
				return nil, fmt.Errorf("group %s referenced unknown finding %s", groupID, fingerprint)
			}
			if otherGroup, exists := assigned[fingerprint]; exists {
				return nil, fmt.Errorf("finding %s assigned to multiple groups (%s, %s)", fingerprint, otherGroup, groupID)
			}
			assigned[fingerprint] = groupID
			items = append(items, finding)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Fingerprint < items[j].Fingerprint })
		grouped = append(grouped, localWorkFindingGroup{
			GroupID:  sanitizePathToken(groupID),
			Findings: items,
		})
	}
	for fingerprint := range byFingerprint {
		if _, ok := assigned[fingerprint]; !ok {
			return nil, fmt.Errorf("grouping result omitted finding %s", fingerprint)
		}
	}
	sort.Slice(grouped, func(i, j int) bool { return grouped[i].GroupID < grouped[j].GroupID })
	return grouped, nil
}

func buildLocalWorkValidationPrompt(manifest localWorkManifest, group localWorkFindingGroup) (string, error) {
	lines := []string{
		"# NANA Work-local Finding Validation",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Sandbox repo path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Group: %s", group.GroupID),
		"",
		"Decide each finding as one of: confirmed, rejected, modified.",
		"- confirmed: the finding is valid as written",
		"- rejected: the finding should be dropped from later validation and hardening in this run",
		"- modified: the finding is directionally valid but must be rewritten into a better scoped or more accurate replacement",
		"",
		"Return JSON only.",
		`Schema: {"group":"...","decisions":[{"fingerprint":"...","status":"confirmed|rejected|modified","reason":"...","replacement":{"title":"...","severity":"low|medium|high|critical","path":"...","line":123,"summary":"...","detail":"...","fix":"...","rationale":"..."}}]}`,
		"If you are unsure, prefer confirmed over rejected.",
		"",
		"Findings:",
	}
	for _, finding := range group.Findings {
		lines = append(lines,
			fmt.Sprintf("- fingerprint: %s", finding.Fingerprint),
			fmt.Sprintf("  path: %s", finding.Path),
			fmt.Sprintf("  line: %d", finding.Line),
			fmt.Sprintf("  severity: %s", finding.Severity),
			fmt.Sprintf("  title: %s", finding.Title),
			fmt.Sprintf("  summary: %s", promptSnippet(finding.Summary)),
			fmt.Sprintf("  detail: %s", promptSnippet(finding.Detail)),
		)
		if strings.TrimSpace(finding.Fix) != "" {
			lines = append(lines, fmt.Sprintf("  fix: %s", promptSnippet(finding.Fix)))
		}
		if snippet := localWorkFindingSnippet(manifest.SandboxRepoPath, finding); strings.TrimSpace(snippet) != "" {
			lines = append(lines, fmt.Sprintf("  code: %s", promptSnippet(snippet)))
		}
	}
	return capPromptSize(strings.Join(lines, "\n")+"\n", localWorkPromptCharLimit), nil
}

func parseLocalWorkValidationGroupResult(raw string, group localWorkFindingGroup) (localWorkValidationGroupResult, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return localWorkValidationGroupResult{}, fmt.Errorf("validation output did not contain JSON object")
	}
	var payload struct {
		Group     string                        `json:"group"`
		Decisions []localWorkValidationDecision `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err != nil {
		return localWorkValidationGroupResult{}, err
	}
	result := localWorkValidationGroupResult{
		GroupID:   defaultString(strings.TrimSpace(payload.Group), group.GroupID),
		Decisions: make([]localWorkValidationDecision, 0, len(payload.Decisions)),
	}
	for _, decision := range payload.Decisions {
		if strings.TrimSpace(decision.Fingerprint) == "" {
			continue
		}
		switch decision.Status {
		case localWorkFindingRejected, localWorkFindingModified, localWorkFindingConfirmed:
		default:
			decision.Status = localWorkFindingConfirmed
		}
		result.Decisions = append(result.Decisions, decision)
	}
	return result, nil
}

func filterRejectedFindings(findings []githubPullReviewFinding, rejectedFingerprints []string) []githubPullReviewFinding {
	if len(findings) == 0 || len(rejectedFingerprints) == 0 {
		return findings
	}
	rejected := map[string]bool{}
	for _, fingerprint := range rejectedFingerprints {
		rejected[fingerprint] = true
	}
	filtered := make([]githubPullReviewFinding, 0, len(findings))
	for _, finding := range findings {
		if rejected[finding.Fingerprint] {
			continue
		}
		filtered = append(filtered, finding)
	}
	return filtered
}

func groupFindingsByModule(findings []githubPullReviewFinding) []localWorkFindingGroup {
	if len(findings) == 0 {
		return nil
	}
	grouped := map[string][]githubPullReviewFinding{}
	for _, finding := range findings {
		groupID := deriveFindingGroupID(finding.Path)
		grouped[groupID] = append(grouped[groupID], finding)
	}
	groupIDs := make([]string, 0, len(grouped))
	for groupID := range grouped {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	out := make([]localWorkFindingGroup, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		findings := append([]githubPullReviewFinding{}, grouped[groupID]...)
		sort.Slice(findings, func(i, j int) bool {
			return findings[i].Fingerprint < findings[j].Fingerprint
		})
		out = append(out, localWorkFindingGroup{GroupID: groupID, Findings: findings})
	}
	return out
}

func deriveFindingGroupID(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return "misc"
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "misc"
	}
	switch parts[0] {
	case "migrator", "tests", "project":
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return parts[0]
}

func findingsFromValidated(validated []localWorkValidatedFinding) []githubPullReviewFinding {
	out := []githubPullReviewFinding{}
	seen := map[string]bool{}
	for _, item := range validated {
		if item.Finding == nil {
			continue
		}
		if item.Status != localWorkFindingConfirmed && item.Status != localWorkFindingModified {
			continue
		}
		if seen[item.CurrentFingerprint] {
			continue
		}
		seen[item.CurrentFingerprint] = true
		out = append(out, *item.Finding)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out
}

func countValidatedFindingsByStatus(validated []localWorkValidatedFinding, status localWorkFindingDecisionStatus) int {
	count := 0
	for _, item := range validated {
		if item.Status == status {
			count++
		}
	}
	return count
}

func groupIDs(groups []localWorkFindingGroup) []string {
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.GroupID)
	}
	return out
}

func writeJSONArtifact(path string, value interface{}) error {
	return os.WriteFile(path, mustMarshalJSON(value), 0o644)
}

func localWorkFindingSnippet(repoPath string, finding githubPullReviewFinding) string {
	if strings.TrimSpace(finding.Path) == "" {
		return ""
	}
	fullPath := filepath.Join(repoPath, filepath.FromSlash(strings.TrimPrefix(finding.Path, "/")))
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(content), "\n")
	if finding.Line <= 0 || finding.Line > len(lines) {
		return strings.Join(lines[:minInt(len(lines), localWorkPromptSnippetLines)], "\n")
	}
	start := finding.Line - 3
	if start < 0 {
		start = 0
	}
	end := finding.Line + 2
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func buildLocalWorkImplementPrompt(manifest localWorkManifest, iteration int) (string, error) {
	inputContent, err := os.ReadFile(manifest.InputPath)
	if err != nil {
		return "", err
	}
	lines := []string{
		"# NANA Work-local Iteration",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Sandbox repo path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Baseline SHA: %s", manifest.BaselineSHA),
		fmt.Sprintf("Source branch: %s", manifest.SourceBranch),
		fmt.Sprintf("Iteration: %d/%d", iteration, manifest.MaxIterations),
		fmt.Sprintf("Integration policy: %s", manifest.IntegrationPolicy),
		"",
		"Contract:",
		"- Work only inside the sandbox repo.",
		"- Never submit, publish, push, or open PRs.",
		"- Leave verification execution to the runtime; focus on code and tests needed to satisfy the plan.",
		"- Treat previous verification failures and review findings as blocking.",
	}
	if manifest.IntegrationPolicy == "final" && iteration == 1 {
		lines = append(lines, "- Avoid running integration/container-heavy checks manually in the first iteration unless they are strictly necessary; rely on runtime verification later.")
	}
	if len(manifest.Iterations) > 0 {
		last := manifest.Iterations[len(manifest.Iterations)-1]
		lines = append(lines,
			"",
			"Previous iteration feedback:",
			fmt.Sprintf("- Verification: %s", defaultString(last.VerificationSummary, "(none)")),
			fmt.Sprintf("- Final review findings: %d", last.ReviewFindings),
		)
		for _, title := range last.ReviewFindingTitles {
			lines = append(lines, "- Review item: "+title)
		}
	}
	if promptSurface, err := readGithubPromptSurface("executor"); err == nil && strings.TrimSpace(promptSurface) != "" {
		lines = append(lines, "", strings.TrimSpace(promptSurface))
	}
	lines = append(lines, "", "Plan:", strings.TrimSpace(string(inputContent)))
	return strings.Join(lines, "\n") + "\n", nil
}

func buildLocalWorkReviewPrompt(manifest localWorkManifest) (string, error) {
	changedFilesOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", "--name-only", manifest.BaselineSHA)
	if err != nil {
		return "", err
	}
	diffOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", manifest.BaselineSHA)
	if err != nil {
		return "", err
	}
	changedFiles := []string{}
	for _, line := range strings.Split(changedFilesOutput, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			changedFiles = append(changedFiles, line)
		}
	}
	lines := []string{
		"Review this local implementation and return JSON only.",
		`Schema: {"findings":[{"title":"...","severity":"low|medium|high|critical","path":"...","line":123,"summary":"...","detail":"...","fix":"...","rationale":"..."}]}`,
		"If there are no actionable issues, return {\"findings\":[]}.",
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Baseline SHA: %s", manifest.BaselineSHA),
		fmt.Sprintf("Changed files: %s", strings.Join(changedFiles, ", ")),
		"Diff:",
		diffOutput,
	}
	if promptSurface, err := readGithubPromptSurface("critic"); err == nil && strings.TrimSpace(promptSurface) != "" {
		lines = append(lines, "", strings.TrimSpace(promptSurface))
	}
	return strings.Join(lines, "\n\n"), nil
}

func buildLocalWorkHardeningPrompt(manifest localWorkManifest, verification localWorkVerificationReport, findings []githubPullReviewFinding) (string, error) {
	lines := []string{
		"# NANA Work-local Hardening Pass",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Sandbox repo path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Baseline SHA: %s", manifest.BaselineSHA),
		"",
		"Task:",
		"- Fix the verification failures and review findings listed below.",
		"- Add or update targeted tests/regressions when needed.",
		"- Do not submit, publish, or push anything.",
		"- Do not describe fixes only; make the code changes in the sandbox repo.",
	}
	if manifest.IntegrationPolicy == "final" && manifest.CurrentIteration <= 1 {
		lines = append(lines, "- Avoid rerunning full integration/container-heavy checks manually in this early iteration unless the fix specifically requires them.")
	}
	if promptSurface, err := readGithubPromptSurface("test-engineer"); err == nil && strings.TrimSpace(promptSurface) != "" {
		lines = append(lines, "", strings.TrimSpace(promptSurface))
	}
	lines = append(lines, "", "Verification status:", summarizeLocalVerification(verification))
	failedCommandCount := 0
	for _, stage := range verification.Stages {
		if stage.Status != "failed" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- Failed stage: %s", stage.Name))
		for _, command := range stage.Commands {
			if command.ExitCode == 0 {
				continue
			}
			if failedCommandCount >= 3 {
				lines = append(lines, "  output: additional failed command output omitted for brevity")
				break
			}
			lines = append(lines, fmt.Sprintf("  command: %s", command.Command))
			if strings.TrimSpace(command.Output) != "" {
				lines = append(lines, fmt.Sprintf("  output: %s", promptSnippet(command.Output)))
			}
			failedCommandCount++
		}
	}
	if len(findings) > 0 {
		lines = append(lines, "", "Review findings:")
		for index, finding := range findings {
			if index >= 10 {
				lines = append(lines, "- additional findings omitted for brevity")
				break
			}
			ref := finding.Path
			if finding.Line > 0 {
				ref = fmt.Sprintf("%s:%d", finding.Path, finding.Line)
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s (%s)", strings.ToUpper(finding.Severity), finding.Title, ref))
			lines = append(lines, "  "+promptSnippet(finding.Detail))
			if strings.TrimSpace(finding.Fix) != "" {
				lines = append(lines, "  Fix hint: "+promptSnippet(finding.Fix))
			}
		}
	}
	return capPromptSize(strings.Join(lines, "\n")+"\n", localWorkPromptCharLimit), nil
}

func resolveLocalWorkRepoRoot(cwd string, repoPath string) (string, error) {
	target := cwd
	if strings.TrimSpace(repoPath) != "" {
		if filepath.IsAbs(repoPath) {
			target = repoPath
		} else {
			target = filepath.Join(cwd, repoPath)
		}
	}
	target = filepath.Clean(target)
	root, err := githubGitOutput(target, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("work-local requires a git-backed repo: %w", err)
	}
	return strings.TrimSpace(root), nil
}

func ensureLocalWorkRepoClean(repoRoot string) error {
	output, err := githubGitOutput(repoRoot, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	remaining := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		remaining = append(remaining, line)
	}
	if len(remaining) == 0 {
		return nil
	}
	return fmt.Errorf("work-local requires a clean repo before start; found local changes:\n%s", strings.Join(remaining, "\n"))
}

func readLocalWorkInput(cwd string, options localWorkStartOptions) (string, string, error) {
	if strings.TrimSpace(options.Task) != "" {
		return options.Task, "task", nil
	}
	path := options.PlanFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("plan file not found: %s", path)
		}
		return "", "", err
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("plan file is a directory, expected a file: %s", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return "", "", fmt.Errorf("plan file is empty: %s", path)
	}
	return trimmed, "plan-file", nil
}

func resolveLocalWorkManifestPath(cwd string, selection localWorkRunSelection) (string, error) {
	if selection.RunID != "" {
		if entry, err := lookupLocalWorkRunIndexEntry(selection.RunID); err == nil {
			return entry.ManifestPath, nil
		}
		return "", fmt.Errorf("work-local run %s was not found in the global index", selection.RunID)
	}

	if selection.GlobalLast {
		index, err := readLocalWorkRunIndex()
		if err != nil || strings.TrimSpace(index.GlobalLastRunID) == "" {
			return "", fmt.Errorf("no global work-local run found under %s", localWorkHomeRoot())
		}
		entry, ok := index.Entries[index.GlobalLastRunID]
		if !ok {
			return "", fmt.Errorf("global work-local run %s is missing from the index", index.GlobalLastRunID)
		}
		return entry.ManifestPath, nil
	}

	repoRoot, err := resolveLocalWorkRepoRootForSelection(cwd, selection.RepoPath)
	if err != nil {
		return "", err
	}

	var latest localWorkLatestRunPointer
	if err := readGithubJSON(localWorkLatestRunPath(repoRoot), &latest); err != nil {
		return "", fmt.Errorf("no work-local run found for repo %s", repoRoot)
	}
	if strings.TrimSpace(latest.RunID) == "" {
		return "", fmt.Errorf("no work-local run found for repo %s", repoRoot)
	}
	repoID := latest.RepoID
	if strings.TrimSpace(repoID) == "" {
		repoID = localWorkRepoID(repoRoot)
	}
	return localWorkManifestPathByID(repoID, latest.RunID), nil
}

func resolveLocalWorkRepoRootForSelection(cwd string, repoPath string) (string, error) {
	repoRoot, err := resolveLocalWorkRepoRoot(cwd, repoPath)
	if err == nil {
		return repoRoot, nil
	}
	if strings.TrimSpace(repoPath) != "" {
		return "", err
	}
	return "", fmt.Errorf("work-local repo context is required for --last; use --repo <path>, --run-id <id>, or --global-last")
}

func localWorkNextIteration(manifest localWorkManifest) int {
	lastCompleted := 0
	if len(manifest.Iterations) > 0 {
		lastCompleted = manifest.Iterations[len(manifest.Iterations)-1].Iteration
	}
	if manifest.CurrentIteration > lastCompleted {
		return manifest.CurrentIteration
	}
	return lastCompleted + 1
}

func localWorkHomeRoot() string {
	return filepath.Join(githubNanaHome(), "local-work")
}

func localWorkReposDir() string {
	return filepath.Join(localWorkHomeRoot(), "repos")
}

func localWorkRepoDirByID(repoID string) string {
	return filepath.Join(localWorkReposDir(), repoID)
}

func localWorkRepoDir(repoRoot string) string {
	return localWorkRepoDirByID(localWorkRepoID(repoRoot))
}

func localWorkRepoMetadataPath(repoRoot string) string {
	return filepath.Join(localWorkRepoDir(repoRoot), "repo.json")
}

func localWorkRepoMetadataPathByID(repoID string) string {
	return filepath.Join(localWorkRepoDirByID(repoID), "repo.json")
}

func localWorkRunsDir(repoRoot string) string {
	return filepath.Join(localWorkRepoDir(repoRoot), "runs")
}

func localWorkRunsDirByID(repoID string) string {
	return filepath.Join(localWorkRepoDirByID(repoID), "runs")
}

func localWorkLatestRunPath(repoRoot string) string {
	return filepath.Join(localWorkRepoDir(repoRoot), "latest-run.json")
}

func localWorkLatestRunPathByID(repoID string) string {
	return filepath.Join(localWorkRepoDirByID(repoID), "latest-run.json")
}

func localWorkManifestPath(repoRoot string, runID string) string {
	return filepath.Join(localWorkRunsDir(repoRoot), runID, "manifest.json")
}

func localWorkManifestPathByID(repoID string, runID string) string {
	return filepath.Join(localWorkRunsDirByID(repoID), runID, "manifest.json")
}

func localWorkIterationDir(runDir string, iteration int) string {
	return filepath.Join(runDir, "iterations", fmt.Sprintf("iter-%02d", iteration))
}

func localWorkSandboxesDir() string {
	return filepath.Join(localWorkHomeRoot(), "sandboxes")
}

func localWorkIndexPath() string {
	return filepath.Join(localWorkHomeRoot(), "index", "runs.json")
}

func localWorkRepoID(repoRoot string) string {
	base := sanitizePathToken(filepath.Base(repoRoot))
	if base == "" {
		base = "repo"
	}
	return base + "-" + shortHash(filepath.Clean(repoRoot))
}

func writeLocalWorkManifest(manifest localWorkManifest) error {
	if strings.TrimSpace(manifest.RepoID) == "" {
		manifest.RepoID = localWorkRepoID(manifest.RepoRoot)
	}
	if strings.TrimSpace(manifest.RepoName) == "" {
		manifest.RepoName = filepath.Base(manifest.RepoRoot)
	}
	manifestPath := localWorkManifestPathByID(manifest.RepoID, manifest.RunID)
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	if err := writeGithubJSON(localWorkRepoMetadataPathByID(manifest.RepoID), localWorkRepoMetadata{
		Version:   1,
		RepoID:    manifest.RepoID,
		RepoRoot:  manifest.RepoRoot,
		RepoName:  manifest.RepoName,
		UpdatedAt: manifest.UpdatedAt,
	}); err != nil {
		return err
	}
	if err := writeGithubJSON(localWorkLatestRunPathByID(manifest.RepoID), localWorkLatestRunPointer{
		RepoID:   manifest.RepoID,
		RepoRoot: manifest.RepoRoot,
		RunID:    manifest.RunID,
	}); err != nil {
		return err
	}
	return upsertLocalWorkRunIndex(manifest, manifestPath)
}

func upsertLocalWorkRunIndex(manifest localWorkManifest, manifestPath string) error {
	index, err := readLocalWorkRunIndex()
	if err != nil {
		return err
	}
	if index.Entries == nil {
		index.Entries = map[string]localWorkRunIndexEntry{}
	}
	index.Entries[manifest.RunID] = localWorkRunIndexEntry{
		RunID:        manifest.RunID,
		RepoID:       manifest.RepoID,
		RepoRoot:     manifest.RepoRoot,
		RepoName:     manifest.RepoName,
		ManifestPath: manifestPath,
		Status:       manifest.Status,
		CreatedAt:    manifest.CreatedAt,
		UpdatedAt:    manifest.UpdatedAt,
	}
	index.GlobalLastRunID = newestLocalWorkRunID(index.Entries)
	return writeGithubJSON(localWorkIndexPath(), index)
}

func readLocalWorkRunIndex() (localWorkRunIndex, error) {
	index := localWorkRunIndex{Version: 1, Entries: map[string]localWorkRunIndexEntry{}}
	if err := readGithubJSON(localWorkIndexPath(), &index); err != nil {
		if os.IsNotExist(err) {
			return index, nil
		}
		return localWorkRunIndex{}, err
	}
	if index.Entries == nil {
		index.Entries = map[string]localWorkRunIndexEntry{}
	}
	return index, nil
}

func lookupLocalWorkRunIndexEntry(runID string) (localWorkRunIndexEntry, error) {
	index, err := readLocalWorkRunIndex()
	if err != nil {
		return localWorkRunIndexEntry{}, err
	}
	entry, ok := index.Entries[runID]
	if !ok {
		return localWorkRunIndexEntry{}, fmt.Errorf("run %s not found", runID)
	}
	return entry, nil
}

func newestLocalWorkRunID(entries map[string]localWorkRunIndexEntry) string {
	bestID := ""
	bestAt := time.Time{}
	for runID, entry := range entries {
		parsed, err := time.Parse(time.RFC3339Nano, entry.UpdatedAt)
		if err != nil {
			parsed = time.Time{}
		}
		if bestID == "" || parsed.After(bestAt) {
			bestID = runID
			bestAt = parsed
		}
	}
	return bestID
}

func detectLocalWorkStall(iterations []localWorkIterationSummary) string {
	if len(iterations) < 2 {
		return ""
	}
	current := iterations[len(iterations)-1]
	previous := iterations[len(iterations)-2]
	if current.Status == "completed" {
		return ""
	}
	if current.DiffFingerprint == previous.DiffFingerprint &&
		current.VerificationFingerprint == previous.VerificationFingerprint &&
		current.ReviewFingerprint == previous.ReviewFingerprint &&
		current.InitialReviewFingerprint == previous.InitialReviewFingerprint &&
		current.HardeningFingerprint == previous.HardeningFingerprint &&
		current.PostHardeningVerificationFingerprint == previous.PostHardeningVerificationFingerprint &&
		strings.Join(current.ReviewRoundFingerprints, "|") == strings.Join(previous.ReviewRoundFingerprints, "|") &&
		strings.Join(current.HardeningRoundFingerprints, "|") == strings.Join(previous.HardeningRoundFingerprints, "|") &&
		strings.Join(current.PostHardeningVerificationFingerprints, "|") == strings.Join(previous.PostHardeningVerificationFingerprints, "|") &&
		intSlicesEqual(current.ReviewFindingsByRound, previous.ReviewFindingsByRound) {
		return fmt.Sprintf("work-local run stalled after iteration %d; diff and failure signals repeated unchanged", current.Iteration)
	}
	return ""
}

func summarizeLocalVerification(report localWorkVerificationReport) string {
	if len(report.FailedStages) == 0 {
		if report.IntegrationIncluded {
			return "verification passed (lint, compile, unit, integration)"
		}
		return "verification passed (lint, compile, unit)"
	}
	return "verification failed: " + strings.Join(report.FailedStages, ", ")
}

func fingerprintVerificationReport(report localWorkVerificationReport) string {
	return sha256Hex(summarizeLocalVerification(report) + "\n" + strings.Join(report.FailedStages, ","))
}

func reviewFindingTitles(findings []githubPullReviewFinding) []string {
	titles := make([]string, 0, len(findings))
	for _, finding := range findings {
		ref := finding.Path
		if finding.Line > 0 {
			ref = fmt.Sprintf("%s:%d", finding.Path, finding.Line)
		}
		titles = append(titles, fmt.Sprintf("%s [%s] %s", finding.Title, strings.ToUpper(finding.Severity), ref))
	}
	return titles
}

func reviewFindingFingerprints(findings []githubPullReviewFinding) []string {
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		out = append(out, finding.Fingerprint)
	}
	return out
}

func promptSnippet(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	trimmed = tailLines(trimmed, localWorkPromptSnippetLines)
	if len(trimmed) <= localWorkPromptSnippetChars {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:localWorkPromptSnippetChars]) + "... [truncated]"
}

func capPromptSize(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	const notice = "\n\n[Prompt truncated to fit runtime limits]\n"
	if limit <= len(notice) {
		return notice[:limit]
	}
	return value[:limit-len(notice)] + notice
}

func tailLines(content string, limit int) string {
	if limit <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= limit {
		return content
	}
	start := len(lines) - limit
	return strings.Join(lines[start:], "\n")
}

func intSlicesEqual(left []int, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mustMarshalJSON(value interface{}) []byte {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []byte("{}\n")
	}
	return append(content, '\n')
}

func writeLocalWorkRetrospective(manifest localWorkManifest) (string, error) {
	diffShortstat, _ := githubGitOutput(manifest.SandboxRepoPath, "diff", "--shortstat", manifest.BaselineSHA)
	lines := []string{
		"# NANA Work-local Retrospective",
		"",
		fmt.Sprintf("- Run id: %s", manifest.RunID),
		fmt.Sprintf("- Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("- Run artifacts: %s", filepath.Dir(localWorkManifestPathByID(manifest.RepoID, manifest.RunID))),
		fmt.Sprintf("- Sandbox: %s", manifest.SandboxPath),
		fmt.Sprintf("- Status: %s", manifest.Status),
		fmt.Sprintf("- Iterations: %d/%d", len(manifest.Iterations), manifest.MaxIterations),
		fmt.Sprintf("- Integration policy: %s", manifest.IntegrationPolicy),
		fmt.Sprintf("- Final diff: %s", defaultString(strings.TrimSpace(diffShortstat), "(no diff)")),
		"",
		"## Iterations",
	}
	for _, iteration := range manifest.Iterations {
		lines = append(lines, fmt.Sprintf("- %d: %s; initial review=%d; final review=%d; integration=%t", iteration.Iteration, iteration.VerificationSummary, iteration.InitialReviewFindings, iteration.ReviewFindings, iteration.IntegrationRan))
	}
	if strings.TrimSpace(manifest.LastError) != "" {
		lines = append(lines, "", "## Failure", "- "+manifest.LastError)
	}
	content := strings.Join(lines, "\n") + "\n"
	retrospectivePath := filepath.Join(filepath.Dir(localWorkManifestPathByID(manifest.RepoID, manifest.RunID)), "retrospective.md")
	if err := os.WriteFile(retrospectivePath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return content, nil
}
