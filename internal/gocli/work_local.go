package gocli

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const LocalWorkHelp = `nana work - Local implementation runtime for git-backed repos

Usage:
  nana work start [--detach] [--repo <path>] [--task <text> | --plan-file <path>] --work-type <bug_fix|refactor|feature|test_only> [--max-iterations <n>] [--integration <final|always|never>] [--grouping-policy <ai|path|singleton>] [--validation-parallelism <1-8>] [-- codex-args...]
  nana work resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
  nana work resolve [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work status [--run-id <id> | --last | --global-last] [--repo <path>] [--json]
  nana work logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>] [--json]
  nana work retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work help

Behavior:
  - runs only against a local git repo in an isolated managed sandbox
  - infers a task from the current branch when --task and --plan-file are omitted
  - requires an explicit --work-type for all local launches
  - refreshes managed source checkouts before sandbox start, syncs the local source branch before final apply, commits verified sandbox changes after completion, and pushes to the tracked remote when one exists
  - resolve retries blocked final-apply runs after Nana refreshes the managed source checkout or completes a pending commit/push
  - never submits, publishes, opens PRs, or calls GitHub APIs
  - loops through implement -> verify -> self-review -> harden -> re-verify with capped hardening rounds
  - runs lint, compile/build, and unit tests every iteration; integration runs on the final pass by default
  - persists run artifacts under ~/.nana/work/
`

const (
	localWorkDefaultMaxIterations  = 8
	localWorkMaxReviewRounds       = 2
	localWorkRuntimeName           = "work"
	localWorkPromptCharLimit       = localWorkHardeningPromptCharLimit
	localWorkGroupingPromptLimit   = localWorkGroupingPromptCharLimit
	localWorkValidationPromptLimit = localWorkValidationPromptCharLimit
	localWorkPromptSnippetChars    = 2000
	localWorkPromptSnippetLines    = 25
	localWorkValidationParallelism = 4
	localWorkMaxValidationParallel = 8
	localWorkMaxValidationGroups   = 8
	localWorkMaxFindingsPerGroup   = 10
	localWorkMaxGroupingAttempts   = 3
	localWorkMaxValidatorAttempts  = 3
	localWorkDefaultGroupingPolicy = "ai"
	localWorkPathGroupingPolicy    = "path"
	localWorkSingletonPolicy       = "singleton"
	localWorkReadRetryAttempts     = 4
	localWorkWriteRetryAttempts    = 4
	localWorkStaleRunThreshold     = 5 * time.Minute
	localWorkStaleCleanupError     = "stale running run cleaned up at start: no active process found"
)

var localWorkReadRetryDelay = 200 * time.Millisecond
var localWorkRetrySleep = time.Sleep
var localWorkOpenReadStore = openLocalWorkReadDB
var localWorkProcessSnapshot = captureLocalWorkProcessSnapshot
var localWorkStartDetachedRunner = launchLocalWorkDetachedRunner
var localWorkExecuteLoop = executeLocalWorkLoop
var localWorkTokenUsagePersistMu sync.Mutex

type localWorkStartOptions struct {
	Detach                bool
	RepoPath              string
	Task                  string
	PlanFile              string
	RunID                 string
	WorkType              string
	MaxIterations         int
	IntegrationPolicy     string
	GroupingPolicy        string
	ValidationParallelism int
	CodexArgs             []string
	RateLimitPolicy       codexRateLimitPolicy
}

type localWorkStatusOptions struct {
	RunSelection localWorkRunSelection
	JSON         bool
}

type localWorkRunSelection struct {
	RunID      string
	UseLast    bool
	GlobalLast bool
	RepoPath   string
}

type localWorkResumeOptions struct {
	RunSelection    localWorkRunSelection
	CodexArgs       []string
	RateLimitPolicy codexRateLimitPolicy
}

type localWorkResolveOptions struct {
	RunSelection localWorkRunSelection
}

type localWorkLogsOptions struct {
	RunSelection localWorkRunSelection
	TailLines    int
	JSON         bool
}

type workRunIndexEntry struct {
	RunID        string
	Backend      string
	RepoKey      string
	RepoRoot     string
	RepoName     string
	RepoSlug     string
	ManifestPath string
	UpdatedAt    string
	TargetKind   string
}

type localWorkManifest struct {
	Version                        int                            `json:"version"`
	RunID                          string                         `json:"run_id"`
	CreatedAt                      string                         `json:"created_at"`
	UpdatedAt                      string                         `json:"updated_at"`
	CompletedAt                    string                         `json:"completed_at,omitempty"`
	Status                         string                         `json:"status"`
	CurrentPhase                   string                         `json:"current_phase,omitempty"`
	CurrentIteration               int                            `json:"current_iteration,omitempty"`
	RepoRoot                       string                         `json:"repo_root"`
	RepoName                       string                         `json:"repo_name"`
	RepoSlug                       string                         `json:"repo_slug,omitempty"`
	RepoID                         string                         `json:"repo_id"`
	SourceBranch                   string                         `json:"source_branch"`
	BaselineSHA                    string                         `json:"baseline_sha"`
	SandboxPath                    string                         `json:"sandbox_path"`
	SandboxRepoPath                string                         `json:"sandbox_repo_path"`
	VerificationPlan               *githubVerificationPlan        `json:"verification_plan,omitempty"`
	VerificationScriptsDir         string                         `json:"verification_scripts_dir,omitempty"`
	InputPath                      string                         `json:"input_path"`
	InputMode                      string                         `json:"input_mode"`
	WorkType                       string                         `json:"work_type,omitempty"`
	IntegrationPolicy              string                         `json:"integration_policy"`
	GroupingPolicy                 string                         `json:"grouping_policy,omitempty"`
	ValidationParallelism          int                            `json:"validation_parallelism,omitempty"`
	MaxIterations                  int                            `json:"max_iterations"`
	CurrentRound                   int                            `json:"current_round,omitempty"`
	CurrentSubphase                string                         `json:"current_subphase,omitempty"`
	TokenUsage                     *localWorkTokenUsageTotals     `json:"token_usage,omitempty"`
	LastError                      string                         `json:"last_error,omitempty"`
	PauseReason                    string                         `json:"pause_reason,omitempty"`
	PauseUntil                     string                         `json:"pause_until,omitempty"`
	PausedAt                       string                         `json:"paused_at,omitempty"`
	RateLimitPolicy                string                         `json:"-"`
	FinalApplyStatus               string                         `json:"final_apply_status,omitempty"`
	FinalApplyCommitSHA            string                         `json:"final_apply_commit_sha,omitempty"`
	FinalApplyError                string                         `json:"final_apply_error,omitempty"`
	FinalAppliedAt                 string                         `json:"final_applied_at,omitempty"`
	SupersededByRunID              string                         `json:"superseded_by_run_id,omitempty"`
	SupersededAt                   string                         `json:"superseded_at,omitempty"`
	SupersededReason               string                         `json:"superseded_reason,omitempty"`
	FinalGateStatus                string                         `json:"final_gate_status,omitempty"`
	FinalGateRoleResults           []localWorkFinalGateRoleResult `json:"final_gate_role_results,omitempty"`
	CandidateAuditStatus           string                         `json:"candidate_audit_status,omitempty"`
	CandidateBlockedPaths          []string                       `json:"candidate_blocked_paths,omitempty"`
	RejectedFindingFingerprints    []string                       `json:"rejected_finding_fingerprints,omitempty"`
	PreexistingFindingFingerprints []string                       `json:"preexisting_finding_fingerprints,omitempty"`
	PreexistingFindings            []localWorkRememberedFinding   `json:"preexisting_findings,omitempty"`
	FollowupDecision               string                         `json:"followup_decision,omitempty"`
	FollowupRounds                 []workFollowupRoundSummary     `json:"followup_rounds,omitempty"`
	Iterations                     []localWorkIterationSummary    `json:"iterations,omitempty"`
	APIBaseURL                     string                         `json:"-"`
	PauseManifestPath              string                         `json:"-"`
}

type localWorkTokenUsageTotals struct {
	InputTokens           int    `json:"input_tokens"`
	CachedInputTokens     int    `json:"cached_input_tokens"`
	OutputTokens          int    `json:"output_tokens"`
	ReasoningOutputTokens int    `json:"reasoning_output_tokens"`
	TotalTokens           int    `json:"total_tokens"`
	SessionsAccounted     int    `json:"sessions_accounted"`
	UpdatedAt             string `json:"updated_at,omitempty"`
}

type localWorkThreadUsageArtifact struct {
	Version     int                       `json:"version"`
	GeneratedAt string                    `json:"generated_at"`
	SandboxPath string                    `json:"sandbox_path"`
	Totals      localWorkTokenUsageTotals `json:"totals"`
	Threads     []localWorkThreadUsageRow `json:"threads,omitempty"`
}

type localWorkThreadUsageRow struct {
	SessionID             string `json:"session_id,omitempty"`
	Nickname              string `json:"nickname,omitempty"`
	Role                  string `json:"role,omitempty"`
	Model                 string `json:"model,omitempty"`
	CWD                   string `json:"cwd,omitempty"`
	InputTokens           int    `json:"input_tokens"`
	CachedInputTokens     int    `json:"cached_input_tokens"`
	OutputTokens          int    `json:"output_tokens"`
	ReasoningOutputTokens int    `json:"reasoning_output_tokens"`
	TotalTokens           int    `json:"total_tokens"`
	StartedAt             int64  `json:"started_at"`
	UpdatedAt             int64  `json:"updated_at"`
}

type localWorkIterationSummary struct {
	Iteration                             int                            `json:"iteration"`
	StartedAt                             string                         `json:"started_at"`
	CompletedAt                           string                         `json:"completed_at"`
	Status                                string                         `json:"status"`
	DiffFingerprint                       string                         `json:"diff_fingerprint,omitempty"`
	VerificationFingerprint               string                         `json:"verification_fingerprint,omitempty"`
	ReviewFingerprint                     string                         `json:"review_fingerprint,omitempty"`
	InitialReviewFingerprint              string                         `json:"initial_review_fingerprint,omitempty"`
	HardeningFingerprint                  string                         `json:"hardening_fingerprint,omitempty"`
	PostHardeningVerificationFingerprint  string                         `json:"post_hardening_verification_fingerprint,omitempty"`
	VerificationPassed                    bool                           `json:"verification_passed"`
	VerificationFailedStages              []string                       `json:"verification_failed_stages,omitempty"`
	VerificationSummary                   string                         `json:"verification_summary,omitempty"`
	InitialReviewFindings                 int                            `json:"initial_review_findings,omitempty"`
	ValidatedFindings                     int                            `json:"validated_findings,omitempty"`
	ConfirmedFindings                     int                            `json:"confirmed_findings,omitempty"`
	RejectedFindings                      int                            `json:"rejected_findings,omitempty"`
	PreexistingFindings                   int                            `json:"preexisting_findings,omitempty"`
	ModifiedFindings                      int                            `json:"modified_findings,omitempty"`
	SkippedRejectedFindings               int                            `json:"skipped_rejected_findings,omitempty"`
	SkippedPreexistingFindings            int                            `json:"skipped_preexisting_findings,omitempty"`
	ValidationGroups                      []string                       `json:"validation_groups,omitempty"`
	ValidationGroupRationales             []string                       `json:"validation_group_rationales,omitempty"`
	RequestedGroupingPolicy               string                         `json:"requested_grouping_policy,omitempty"`
	EffectiveGroupingPolicy               string                         `json:"effective_grouping_policy,omitempty"`
	GroupingFallbackReason                string                         `json:"grouping_fallback_reason,omitempty"`
	GroupingAttempts                      int                            `json:"grouping_attempts,omitempty"`
	ValidatorAttempts                     int                            `json:"validator_attempts,omitempty"`
	ReviewRoundsUsed                      int                            `json:"review_rounds_used,omitempty"`
	ReviewFindingsByRound                 []int                          `json:"review_findings_by_round,omitempty"`
	ReviewRoundFingerprints               []string                       `json:"review_round_fingerprints,omitempty"`
	HardeningRoundFingerprints            []string                       `json:"hardening_round_fingerprints,omitempty"`
	PostHardeningVerificationFingerprints []string                       `json:"post_hardening_verification_fingerprints,omitempty"`
	ReviewFindings                        int                            `json:"review_findings"`
	ReviewFindingTitles                   []string                       `json:"review_finding_titles,omitempty"`
	FinalGateFindings                     int                            `json:"final_gate_findings,omitempty"`
	FinalGateRoles                        []string                       `json:"final_gate_roles,omitempty"`
	FinalGateStatus                       string                         `json:"final_gate_status,omitempty"`
	FinalGateRoleResults                  []localWorkFinalGateRoleResult `json:"final_gate_role_results,omitempty"`
	CandidateAuditStatus                  string                         `json:"candidate_audit_status,omitempty"`
	CandidateBlockedPaths                 []string                       `json:"candidate_blocked_paths,omitempty"`
	IntegrationRan                        bool                           `json:"integration_ran,omitempty"`
	FollowupRound                         int                            `json:"followup_round,omitempty"`
	FollowupPlannerDecision               string                         `json:"followup_planner_decision,omitempty"`
	FollowupReviewDecision                string                         `json:"followup_review_decision,omitempty"`
	FollowupProposedItems                 int                            `json:"followup_proposed_items,omitempty"`
	FollowupApprovedItems                 int                            `json:"followup_approved_items,omitempty"`
	FollowupRejectedItems                 int                            `json:"followup_rejected_items,omitempty"`
	FollowupApprovedKinds                 []string                       `json:"followup_approved_kinds,omitempty"`
	ApprovedFollowupItems                 []workFollowupItem             `json:"approved_followup_items,omitempty"`
	RejectedFollowupItems                 []workFollowupRejectedItem     `json:"rejected_followup_items,omitempty"`
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

type localWorkFinalApplyResult struct {
	Status    string
	CommitSHA string
	Error     string
}

type localWorkFinalGateRoleResult struct {
	Role     string `json:"role"`
	Findings int    `json:"findings"`
}

type localWorkCandidateAuditResult struct {
	Status       string   `json:"status,omitempty"`
	BlockedPaths []string `json:"blocked_paths,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type localWorkFindingGroup struct {
	GroupID   string                    `json:"group_id"`
	Rationale string                    `json:"rationale,omitempty"`
	Findings  []githubPullReviewFinding `json:"findings"`
}

type localWorkFindingDecisionStatus string

const (
	localWorkFindingConfirmed   localWorkFindingDecisionStatus = "confirmed"
	localWorkFindingRejected    localWorkFindingDecisionStatus = "rejected"
	localWorkFindingModified    localWorkFindingDecisionStatus = "modified"
	localWorkFindingPreexisting localWorkFindingDecisionStatus = "preexisting"
	localWorkFindingSuperseded  localWorkFindingDecisionStatus = "superseded"
	localWorkFindingPending     localWorkFindingDecisionStatus = "pending"
)

type localWorkRememberedFinding struct {
	Fingerprint string `json:"fingerprint"`
	Title       string `json:"title"`
	Path        string `json:"path"`
	Line        int    `json:"line,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type localWorkValidatedFinding struct {
	GroupID               string                         `json:"group_id"`
	GroupRationale        string                         `json:"group_rationale,omitempty"`
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
	RequestedPolicy string                   `json:"requested_policy,omitempty"`
	EffectivePolicy string                   `json:"effective_policy,omitempty"`
	FallbackReason  string                   `json:"fallback_reason,omitempty"`
	Attempts        int                      `json:"attempts,omitempty"`
	Groups          []localWorkGroupingGroup `json:"groups"`
}

type localWorkGroupingGroup struct {
	GroupID   string   `json:"group_id"`
	Rationale string   `json:"rationale,omitempty"`
	Findings  []string `json:"findings"`
}

type localWorkIterationRuntimeState struct {
	Version               int                               `json:"version"`
	Iteration             int                               `json:"iteration"`
	CurrentPhase          string                            `json:"current_phase,omitempty"`
	CurrentSubphase       string                            `json:"current_subphase,omitempty"`
	CurrentRound          int                               `json:"current_round,omitempty"`
	GroupingPolicy        string                            `json:"grouping_policy,omitempty"`
	ValidationParallelism int                               `json:"validation_parallelism,omitempty"`
	ImplementCompleted    bool                              `json:"implement_completed,omitempty"`
	VerificationCompleted bool                              `json:"verification_completed,omitempty"`
	ReviewCompleted       bool                              `json:"review_completed,omitempty"`
	ValidationContexts    []localWorkValidationContextState `json:"validation_contexts,omitempty"`
	Rounds                []localWorkRoundRuntimeState      `json:"rounds,omitempty"`
}

type localWorkValidationContextState struct {
	Name               string                       `json:"name"`
	Round              int                          `json:"round"`
	RequestedPolicy    string                       `json:"requested_policy,omitempty"`
	EffectivePolicy    string                       `json:"effective_policy,omitempty"`
	FallbackReason     string                       `json:"fallback_reason,omitempty"`
	Attempts           int                          `json:"attempts,omitempty"`
	GroupingComplete   bool                         `json:"grouping_complete,omitempty"`
	ValidationComplete bool                         `json:"validation_complete,omitempty"`
	GroupStates        []localWorkRuntimeGroupState `json:"group_states,omitempty"`
}

type localWorkRoundRuntimeState struct {
	Round                     int  `json:"round"`
	HardeningCompleted        bool `json:"hardening_completed,omitempty"`
	PostVerificationCompleted bool `json:"post_verification_completed,omitempty"`
	PostReviewCompleted       bool `json:"post_review_completed,omitempty"`
}

type localWorkRuntimeGroupState struct {
	GroupID    string `json:"group_id"`
	Rationale  string `json:"rationale,omitempty"`
	Status     string `json:"status,omitempty"`
	Attempts   int    `json:"attempts,omitempty"`
	ResultPath string `json:"result_path,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

type localWorkValidationGroupProgress struct {
	GroupID    string
	Rationale  string
	Status     string
	Attempts   int
	ResultPath string
	LastError  string
}

type localWorkDBStore struct {
	db            *sql.DB
	emptyReadOnly bool
}

type localWorkFindingHistoryEvent struct {
	Iteration             int                            `json:"iteration"`
	Round                 int                            `json:"round"`
	Phase                 string                         `json:"phase"`
	GroupID               string                         `json:"group_id,omitempty"`
	GroupRationale        string                         `json:"group_rationale,omitempty"`
	OriginalFingerprint   string                         `json:"original_fingerprint"`
	CurrentFingerprint    string                         `json:"current_fingerprint"`
	Status                localWorkFindingDecisionStatus `json:"status"`
	Title                 string                         `json:"title,omitempty"`
	Path                  string                         `json:"path,omitempty"`
	Line                  int                            `json:"line,omitempty"`
	Reason                string                         `json:"reason,omitempty"`
	SupersedesFingerprint string                         `json:"supersedes_fingerprint,omitempty"`
}

func runLocalWorkCommand(cwd string, args []string) error {
	_, err := runLocalWorkCommandWithOptions(cwd, args, codexRateLimitPolicyWaitInProcess)
	return err
}

func runLocalWorkCommandWithRunID(cwd string, args []string) (string, error) {
	return runLocalWorkCommandWithOptions(cwd, args, codexRateLimitPolicyWaitInProcess)
}

func runLocalWorkCommandWithOptions(cwd string, args []string, rateLimitPolicy codexRateLimitPolicy) (string, error) {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(currentWorkStdout(), LocalWorkHelp)
		return "", nil
	}

	switch args[0] {
	case "start":
		options, err := parseLocalWorkStartArgs(args[1:])
		if err != nil {
			return "", err
		}
		options.RateLimitPolicy = rateLimitPolicy
		return startLocalWorkWithRunID(cwd, options)
	case "resume":
		options, err := parseLocalWorkResumeArgs(args[1:])
		if err != nil {
			return "", err
		}
		options.RateLimitPolicy = rateLimitPolicy
		return "", resumeLocalWork(cwd, options)
	case "resolve":
		options, err := parseLocalWorkResolveArgs(args[1:])
		if err != nil {
			return "", err
		}
		return "", resolveLocalWork(cwd, options)
	case "status":
		options, err := parseLocalWorkStatusArgs(args[1:])
		if err != nil {
			return "", err
		}
		return "", localWorkStatus(cwd, options)
	case "logs":
		options, err := parseLocalWorkLogsArgs(args[1:])
		if err != nil {
			return "", err
		}
		return "", localWorkLogs(cwd, options)
	case "retrospective":
		selection, err := parseLocalWorkRunSelection(args[1:], true)
		if err != nil {
			return "", err
		}
		return "", localWorkRetrospective(cwd, selection)
	case "verify-refresh":
		selection, err := parseLocalWorkRunSelection(args[1:], false)
		if err != nil {
			return "", err
		}
		return "", refreshLocalWorkVerificationArtifacts(cwd, selection)
	default:
		return "", fmt.Errorf("Unknown work subcommand: %s\n\n%s", args[0], LocalWorkHelp)
	}
}

func localWorkDBPath() string {
	return filepath.Join(localWorkHomeRoot(), "state.db")
}

func openLocalWorkDB() (*localWorkDBStore, error) {
	return openLocalWorkDBWithOpenFunc(openLocalWorkSQLite)
}

func openLocalWorkDBDirect() (*localWorkDBStore, error) {
	return openLocalWorkDBWithOpenFunc(openLocalWorkSQLiteDirect)
}

func openLocalWorkDBWithOpenFunc(openFunc func(string) (*sql.DB, error)) (*localWorkDBStore, error) {
	if err := os.MkdirAll(localWorkHomeRoot(), 0o755); err != nil {
		return nil, err
	}
	db, err := openFunc(localWorkDBPath())
	if err != nil {
		return nil, err
	}
	store := &localWorkDBStore{db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func openLocalWorkReadDB() (*localWorkDBStore, error) {
	path := localWorkDBPath()
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return createLocalWorkEmptyReadStore()
		}
		return nil, err
	}
	db, err := openLocalWorkSQLite(path)
	if err != nil {
		return nil, err
	}
	version, hasTables, err := localWorkDBSchemaState(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !hasTables {
		_ = db.Close()
		return createLocalWorkEmptyReadStore()
	}
	if version < localWorkDBSchemaVersion {
		_ = db.Close()
		return nil, &localWorkDBSchemaError{
			Path:           path,
			SchemaVersion:  version,
			CurrentVersion: localWorkDBSchemaVersion,
		}
	}
	if version > localWorkDBSchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("local work DB schema at %s is version %d, newer than supported version %d", path, version, localWorkDBSchemaVersion)
	}
	store := &localWorkDBStore{db: db}
	if err := store.prepareReadOnly(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *localWorkDBStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *localWorkDBStore) init() error {
	return bootstrapLocalWorkDB(s.db)
}

func (s *localWorkDBStore) normalizeLegacyWorkItemPauseState() error {
	return normalizeLegacyWorkItemPauseStateDB(s.db)
}

func (s *localWorkDBStore) prepareReadOnly() error {
	return configureLocalWorkReadPragmas(s.db)
}

func isLocalWorkDBLockError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "database is locked"):
		return true
	case strings.Contains(lower, "database table is locked"):
		return true
	case strings.Contains(lower, "database schema is locked"):
		return true
	case strings.Contains(lower, "sqlite_busy"):
		return true
	default:
		return false
	}
}

func withLocalWorkReadStore[T any](readFn func(*localWorkDBStore) (T, error)) (T, error) {
	var zero T
	attempts := localWorkReadRetryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		store, err := localWorkOpenReadStore()
		if err != nil {
			if !isLocalWorkDBLockError(err) || attempt == attempts {
				return zero, err
			}
			localWorkRetrySleep(localWorkReadRetryDelay)
			continue
		}
		value, readErr := readFn(store)
		closeErr := store.Close()
		if readErr == nil && closeErr == nil {
			return value, nil
		}
		err = readErr
		if err == nil {
			err = closeErr
		}
		if !isLocalWorkDBLockError(err) || attempt == attempts {
			return zero, err
		}
		localWorkRetrySleep(localWorkReadRetryDelay)
	}
	return zero, fmt.Errorf("local work DB read retry exhausted")
}

func withLocalWorkWriteRetry(writeFn func() error) error {
	attempts := localWorkWriteRetryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := writeFn(); err != nil {
			lastErr = err
			if !isLocalWorkDBLockError(err) || attempt == attempts {
				return err
			}
			localWorkRetrySleep(localWorkReadRetryDelay)
			continue
		}
		return nil
	}
	return lastErr
}

func withLocalWorkWriteStore[T any](writeFn func(*localWorkDBStore) (T, error)) (T, error) {
	var zero T
	var value T
	err := withLocalWorkWriteRetry(func() error {
		store, err := openLocalWorkDB()
		if err != nil {
			return err
		}
		defer store.Close()
		value, err = writeFn(store)
		return err
	})
	if err != nil {
		return zero, err
	}
	return value, nil
}

func withLocalWorkWriteStoreErr(writeFn func(*localWorkDBStore) error) error {
	_, err := withLocalWorkWriteStore(func(store *localWorkDBStore) (struct{}, error) {
		return struct{}{}, writeFn(store)
	})
	return err
}

func ensureSQLiteColumn(db *sql.DB, table string, column string, alter string) error {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(column)) {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(alter)
	return err
}

func (s *localWorkDBStore) writeManifest(manifest localWorkManifest) error {
	normalizeLocalWorkManifest(&manifest)
	s.mergePersistedTokenUsage(&manifest)
	manifest.RepoSlug = localWorkResolvedRepoSlug(manifest.RepoRoot, manifest.RepoSlug)
	if strings.TrimSpace(manifest.RepoID) == "" {
		manifest.RepoID = localWorkRepoID(manifest.RepoRoot)
	}
	if strings.TrimSpace(manifest.RepoName) == "" {
		manifest.RepoName = filepath.Base(manifest.RepoRoot)
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO repos(repo_id, repo_root, repo_name, updated_at)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo_id) DO UPDATE SET
		   repo_root=excluded.repo_root,
		   repo_name=excluded.repo_name,
		   updated_at=excluded.updated_at`,
		manifest.RepoID, manifest.RepoRoot, manifest.RepoName, manifest.UpdatedAt,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO runs(run_id, repo_id, repo_root, repo_name, created_at, updated_at, completed_at, status, current_phase, current_subphase, current_iteration, current_round, sandbox_path, sandbox_repo_path, manifest_json)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
		   repo_id=excluded.repo_id,
		   repo_root=excluded.repo_root,
		   repo_name=excluded.repo_name,
		   created_at=excluded.created_at,
		   updated_at=excluded.updated_at,
		   completed_at=excluded.completed_at,
		   status=excluded.status,
		   current_phase=excluded.current_phase,
		   current_subphase=excluded.current_subphase,
		   current_iteration=excluded.current_iteration,
		   current_round=excluded.current_round,
		   sandbox_path=excluded.sandbox_path,
		   sandbox_repo_path=excluded.sandbox_repo_path,
		   manifest_json=excluded.manifest_json`,
		manifest.RunID, manifest.RepoID, manifest.RepoRoot, manifest.RepoName, manifest.CreatedAt, manifest.UpdatedAt, nullableString(manifest.CompletedAt),
		manifest.Status, nullableString(manifest.CurrentPhase), nullableString(manifest.CurrentSubphase), manifest.CurrentIteration, manifest.CurrentRound,
		manifest.SandboxPath, manifest.SandboxRepoPath, string(content),
	); err != nil {
		return err
	}
	if err := writeWorkRunIndexTx(tx, localWorkRunIndexEntry(manifest)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return syncCanonicalLocalWorkRunTask(manifest)
}

func (s *localWorkDBStore) writeActiveState(manifest localWorkManifest, state *localWorkIterationRuntimeState) error {
	normalizeLocalWorkManifest(&manifest)
	s.mergePersistedTokenUsage(&manifest)
	manifest.RepoSlug = localWorkResolvedRepoSlug(manifest.RepoRoot, manifest.RepoSlug)
	if strings.TrimSpace(manifest.RepoID) == "" {
		manifest.RepoID = localWorkRepoID(manifest.RepoRoot)
	}
	if strings.TrimSpace(manifest.RepoName) == "" {
		manifest.RepoName = filepath.Base(manifest.RepoRoot)
	}
	manifestContent, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	var stateContent []byte
	if state != nil {
		if state.Version == 0 {
			state.Version = 1
		}
		stateContent, err = json.Marshal(state)
		if err != nil {
			return err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO repos(repo_id, repo_root, repo_name, updated_at)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo_id) DO UPDATE SET
		   repo_root=excluded.repo_root,
		   repo_name=excluded.repo_name,
		   updated_at=excluded.updated_at`,
		manifest.RepoID, manifest.RepoRoot, manifest.RepoName, manifest.UpdatedAt,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO runs(run_id, repo_id, repo_root, repo_name, created_at, updated_at, completed_at, status, current_phase, current_subphase, current_iteration, current_round, sandbox_path, sandbox_repo_path, manifest_json)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
		   repo_id=excluded.repo_id,
		   repo_root=excluded.repo_root,
		   repo_name=excluded.repo_name,
		   created_at=excluded.created_at,
		   updated_at=excluded.updated_at,
		   completed_at=excluded.completed_at,
		   status=excluded.status,
		   current_phase=excluded.current_phase,
		   current_subphase=excluded.current_subphase,
		   current_iteration=excluded.current_iteration,
		   current_round=excluded.current_round,
		   sandbox_path=excluded.sandbox_path,
		   sandbox_repo_path=excluded.sandbox_repo_path,
		   manifest_json=excluded.manifest_json`,
		manifest.RunID, manifest.RepoID, manifest.RepoRoot, manifest.RepoName, manifest.CreatedAt, manifest.UpdatedAt, nullableString(manifest.CompletedAt),
		manifest.Status, nullableString(manifest.CurrentPhase), nullableString(manifest.CurrentSubphase), manifest.CurrentIteration, manifest.CurrentRound,
		manifest.SandboxPath, manifest.SandboxRepoPath, string(manifestContent),
	); err != nil {
		return err
	}
	if state != nil {
		if _, err := tx.Exec(
			`INSERT INTO runtime_states(run_id, iteration, state_json)
			 VALUES(?, ?, ?)
			 ON CONFLICT(run_id, iteration) DO UPDATE SET state_json=excluded.state_json`,
			manifest.RunID, state.Iteration, string(stateContent),
		); err != nil {
			return err
		}
	}
	if err := writeWorkRunIndexTx(tx, localWorkRunIndexEntry(manifest)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return syncCanonicalLocalWorkRunTask(manifest)
}

func (s *localWorkDBStore) readManifest(runID string) (localWorkManifest, error) {
	row := s.db.QueryRow(`SELECT manifest_json FROM runs WHERE run_id = ?`, runID)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return localWorkManifest{}, fmt.Errorf("work run %s was not found", runID)
		}
		return localWorkManifest{}, err
	}
	var manifest localWorkManifest
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return localWorkManifest{}, err
	}
	normalizeLocalWorkManifest(&manifest)
	return manifest, nil
}

func (s *localWorkDBStore) mergePersistedTokenUsage(manifest *localWorkManifest) {
	if s == nil || manifest == nil || strings.TrimSpace(manifest.RunID) == "" {
		return
	}
	existing, err := s.readManifest(manifest.RunID)
	if err != nil {
		return
	}
	mergeLocalWorkTokenUsageTotals(manifest, existing.TokenUsage)
}

func mergeLocalWorkTokenUsageTotals(manifest *localWorkManifest, existing *localWorkTokenUsageTotals) {
	if manifest == nil || existing == nil {
		return
	}
	if manifest.TokenUsage == nil {
		copy := *existing
		manifest.TokenUsage = &copy
		return
	}
	manifest.TokenUsage.InputTokens = max(manifest.TokenUsage.InputTokens, existing.InputTokens)
	manifest.TokenUsage.CachedInputTokens = max(manifest.TokenUsage.CachedInputTokens, existing.CachedInputTokens)
	manifest.TokenUsage.OutputTokens = max(manifest.TokenUsage.OutputTokens, existing.OutputTokens)
	manifest.TokenUsage.ReasoningOutputTokens = max(manifest.TokenUsage.ReasoningOutputTokens, existing.ReasoningOutputTokens)
	manifest.TokenUsage.TotalTokens = max(manifest.TokenUsage.TotalTokens, existing.TotalTokens)
	manifest.TokenUsage.SessionsAccounted = max(manifest.TokenUsage.SessionsAccounted, existing.SessionsAccounted)
	if strings.TrimSpace(manifest.TokenUsage.UpdatedAt) == "" {
		manifest.TokenUsage.UpdatedAt = existing.UpdatedAt
	}
}

func captureLocalWorkProcessSnapshot() (string, error) {
	output, err := exec.Command("ps", "-eo", "pid,args").CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func localWorkManifestLastHeartbeat(manifest localWorkManifest) time.Time {
	for _, candidate := range []string{
		strings.TrimSpace(manifest.UpdatedAt),
		strings.TrimSpace(manifest.CreatedAt),
	} {
		if candidate == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, candidate); err == nil {
			return parsed
		}
		if parsed, err := time.Parse(time.RFC3339, candidate); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func localWorkIsStaleCleanupError(message string) bool {
	return strings.Contains(strings.TrimSpace(message), localWorkStaleCleanupError)
}

func localWorkProcessLineHasCandidate(line string, candidates ...string) bool {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && strings.Contains(line, candidate) {
			return true
		}
	}
	return false
}

func localWorkProcessLineIsCodexWorker(line string) bool {
	return strings.Contains(line, "codex exec -C ") || strings.Contains(line, "/codex/codex exec -C ")
}

func localWorkProcessLineIsDetachedRunner(line string) bool {
	return strings.Contains(line, " work resume ") && strings.Contains(line, "--run-id")
}

func localWorkManifestHasLiveProcess(manifest localWorkManifest, snapshot string) bool {
	for _, line := range strings.Split(snapshot, "\n") {
		if line == "" {
			continue
		}
		if !localWorkProcessLineIsCodexWorker(line) && !localWorkProcessLineIsDetachedRunner(line) {
			continue
		}
		if localWorkProcessLineHasCandidate(
			line,
			manifest.RunID,
			manifest.SandboxPath,
			manifest.SandboxRepoPath,
			manifest.RepoRoot,
		) {
			return true
		}
	}
	return false
}

func cleanupStaleLocalWorkRunsForRepo(repoRoot string) (int, error) {
	cleaned, _, err := cleanupStaleLocalWorkRunsForRepoDetailed(repoRoot)
	return cleaned, err
}

func cleanupStaleLocalWorkRunsForRepoDetailed(repoRoot string, codexArgs ...[]string) (int, []localWorkManifest, error) {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return 0, nil, nil
	}
	resumeCodexArgs := []string(nil)
	if len(codexArgs) > 0 {
		resumeCodexArgs = append([]string{}, codexArgs[0]...)
	}
	snapshot, err := localWorkProcessSnapshot()
	if err != nil {
		return 0, nil, err
	}
	manifests, err := withLocalWorkReadStore(func(store *localWorkDBStore) ([]localWorkManifest, error) {
		rows, err := store.db.Query(`SELECT manifest_json FROM runs WHERE repo_root = ? AND status = ?`, repoRoot, "running")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		manifests := []localWorkManifest{}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return nil, err
			}
			var manifest localWorkManifest
			if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
				return nil, err
			}
			normalizeLocalWorkManifest(&manifest)
			manifests = append(manifests, manifest)
		}
		return manifests, rows.Err()
	})
	if err != nil {
		return 0, nil, err
	}
	now := ISOTimeNow()
	nowTime := time.Now().UTC()
	cleaned := 0
	cleanedManifests := []localWorkManifest{}
	for _, manifest := range manifests {
		lastHeartbeat := localWorkManifestLastHeartbeat(manifest)
		if !lastHeartbeat.IsZero() && nowTime.Sub(lastHeartbeat) < localWorkStaleRunThreshold {
			continue
		}
		if localWorkManifestHasLiveProcess(manifest, snapshot) {
			continue
		}
		if localWorkCanResumeAfterHardRestart(manifest) {
			resumedManifest, err := resumeStaleLocalWorkRunDetached(manifest, resumeCodexArgs)
			if err == nil {
				cleaned++
				cleanedManifests = append(cleanedManifests, resumedManifest)
				continue
			}
			manifest.LastError = localWorkStaleCleanupError + ": resume failed: " + err.Error()
		} else {
			manifest.LastError = localWorkStaleCleanupError
		}
		manifest.Status = "failed"
		manifest.UpdatedAt = now
		if strings.TrimSpace(manifest.CompletedAt) == "" {
			manifest.CompletedAt = now
		}
		if err := writeLocalWorkManifest(manifest); err != nil {
			return cleaned, cleanedManifests, err
		}
		cleaned++
		cleanedManifests = append(cleanedManifests, manifest)
	}
	return cleaned, cleanedManifests, nil
}

func localWorkCanResumeAfterHardRestart(manifest localWorkManifest) bool {
	if strings.TrimSpace(manifest.RunID) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(manifest.Status), "running") {
		return false
	}
	if strings.TrimSpace(manifest.RepoRoot) == "" ||
		strings.TrimSpace(manifest.SandboxPath) == "" ||
		strings.TrimSpace(manifest.SandboxRepoPath) == "" {
		return false
	}
	if strings.TrimSpace(manifest.CompletedAt) != "" {
		return false
	}
	if manifest.MaxIterations > 0 && len(manifest.Iterations) >= manifest.MaxIterations {
		return false
	}
	return true
}

func resumeStaleLocalWorkRunDetached(manifest localWorkManifest, codexArgs []string) (localWorkManifest, error) {
	original := manifest
	manifest.Status = "running"
	manifest.CompletedAt = ""
	manifest.LastError = ""
	manifest.PauseReason = ""
	manifest.PauseUntil = ""
	manifest.PausedAt = ""
	manifest.UpdatedAt = ISOTimeNow()
	if err := writeLocalWorkManifest(manifest); err != nil {
		return original, err
	}
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	logPath := filepath.Join(runDir, "runtime.log")
	if err := localWorkStartDetachedRunner(manifest.RepoRoot, manifest.RunID, codexArgs, logPath); err != nil {
		if restoreErr := writeLocalWorkManifest(original); restoreErr != nil {
			return original, fmt.Errorf("%w (additionally failed to restore stale manifest: %v)", err, restoreErr)
		}
		return original, err
	}
	return manifest, nil
}

func localWorkRunIndexEntry(manifest localWorkManifest) workRunIndexEntry {
	normalizeLocalWorkManifest(&manifest)
	manifest.RepoSlug = localWorkResolvedRepoSlug(manifest.RepoRoot, manifest.RepoSlug)
	if strings.TrimSpace(manifest.RepoID) == "" {
		manifest.RepoID = localWorkRepoID(manifest.RepoRoot)
	}
	if strings.TrimSpace(manifest.RepoName) == "" {
		manifest.RepoName = filepath.Base(manifest.RepoRoot)
	}
	return workRunIndexEntry{
		RunID:      manifest.RunID,
		Backend:    "local",
		RepoKey:    manifest.RepoID,
		RepoRoot:   manifest.RepoRoot,
		RepoName:   manifest.RepoName,
		RepoSlug:   manifest.RepoSlug,
		UpdatedAt:  manifest.UpdatedAt,
		TargetKind: "local",
	}
}

func writeWorkRunIndexTx(tx *sql.Tx, entry workRunIndexEntry) error {
	entry.RunID = strings.TrimSpace(entry.RunID)
	entry.Backend = strings.TrimSpace(entry.Backend)
	if entry.RunID == "" || entry.Backend == "" {
		return nil
	}
	if strings.TrimSpace(entry.UpdatedAt) == "" {
		entry.UpdatedAt = ISOTimeNow()
	}
	_, err := tx.Exec(
		`INSERT INTO work_run_index(run_id, backend, repo_key, repo_root, repo_name, repo_slug, manifest_path, updated_at, target_kind)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
		   backend=excluded.backend,
		   repo_key=excluded.repo_key,
		   repo_root=excluded.repo_root,
		   repo_name=excluded.repo_name,
		   repo_slug=excluded.repo_slug,
		   manifest_path=excluded.manifest_path,
		   updated_at=excluded.updated_at,
		   target_kind=excluded.target_kind`,
		entry.RunID,
		entry.Backend,
		nullableString(entry.RepoKey),
		nullableString(entry.RepoRoot),
		nullableString(entry.RepoName),
		nullableString(entry.RepoSlug),
		nullableString(entry.ManifestPath),
		entry.UpdatedAt,
		nullableString(entry.TargetKind),
	)
	return err
}

func writeWorkRunIndex(entry workRunIndexEntry) error {
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		tx, err := store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := writeWorkRunIndexTx(tx, entry); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func readWorkRunIndex(runID string) (workRunIndexEntry, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (workRunIndexEntry, error) {
		row := store.db.QueryRow(`SELECT run_id, backend, repo_key, repo_root, repo_name, repo_slug, manifest_path, updated_at, target_kind FROM work_run_index WHERE run_id = ?`, runID)
		return scanWorkRunIndexEntry(row)
	})
}

func latestWorkRunIndex(backend string) (workRunIndexEntry, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (workRunIndexEntry, error) {
		row := store.db.QueryRow(`SELECT run_id, backend, repo_key, repo_root, repo_name, repo_slug, manifest_path, updated_at, target_kind FROM work_run_index WHERE backend = ? ORDER BY updated_at DESC LIMIT 1`, backend)
		return scanWorkRunIndexEntry(row)
	})
}

func latestAnyWorkRunIndex() (workRunIndexEntry, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (workRunIndexEntry, error) {
		row := store.db.QueryRow(`SELECT run_id, backend, repo_key, repo_root, repo_name, repo_slug, manifest_path, updated_at, target_kind FROM work_run_index ORDER BY updated_at DESC LIMIT 1`)
		return scanWorkRunIndexEntry(row)
	})
}

type workRunIndexScanner interface {
	Scan(dest ...interface{}) error
}

func scanWorkRunIndexEntry(row workRunIndexScanner) (workRunIndexEntry, error) {
	var entry workRunIndexEntry
	var repoKey, repoRoot, repoName, repoSlug, manifestPath, targetKind sql.NullString
	if err := row.Scan(&entry.RunID, &entry.Backend, &repoKey, &repoRoot, &repoName, &repoSlug, &manifestPath, &entry.UpdatedAt, &targetKind); err != nil {
		return workRunIndexEntry{}, err
	}
	entry.RepoKey = repoKey.String
	entry.RepoRoot = repoRoot.String
	entry.RepoName = repoName.String
	entry.RepoSlug = repoSlug.String
	entry.ManifestPath = manifestPath.String
	entry.TargetKind = targetKind.String
	return entry, nil
}

func readLocalWorkManifestByRunID(runID string) (localWorkManifest, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (localWorkManifest, error) {
		return store.readManifest(runID)
	})
}

func (s *localWorkDBStore) resolveRunID(cwd string, selection localWorkRunSelection) (string, error) {
	if selection.RunID != "" {
		row := s.db.QueryRow(`SELECT run_id FROM runs WHERE run_id = ?`, selection.RunID)
		var runID string
		if err := row.Scan(&runID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("work run %s was not found", selection.RunID)
			}
			return "", err
		}
		return runID, nil
	}
	if selection.GlobalLast {
		row := s.db.QueryRow(`SELECT run_id FROM runs ORDER BY updated_at DESC LIMIT 1`)
		var runID string
		if err := row.Scan(&runID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("no global work run found under %s", localWorkHomeRoot())
			}
			return "", err
		}
		return runID, nil
	}
	repoRoot, err := resolveLocalWorkRepoRootForSelection(cwd, selection.RepoPath)
	if err != nil {
		return "", err
	}
	row := s.db.QueryRow(`SELECT run_id FROM runs WHERE repo_root = ? ORDER BY updated_at DESC LIMIT 1`, repoRoot)
	var runID string
	if err := row.Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("no work run found for repo %s", repoRoot)
		}
		return "", err
	}
	return runID, nil
}

func resolveLocalWorkRun(cwd string, selection localWorkRunSelection) (localWorkManifest, string, error) {
	result, err := withLocalWorkReadStore(func(store *localWorkDBStore) (struct {
		manifest localWorkManifest
		runDir   string
	}, error) {
		runID, err := store.resolveRunID(cwd, selection)
		if err != nil {
			return struct {
				manifest localWorkManifest
				runDir   string
			}{}, err
		}
		manifest, err := store.readManifest(runID)
		if err != nil {
			return struct {
				manifest localWorkManifest
				runDir   string
			}{}, err
		}
		return struct {
			manifest localWorkManifest
			runDir   string
		}{
			manifest: manifest,
			runDir:   localWorkRunDirByID(manifest.RepoID, manifest.RunID),
		}, nil
	})
	if err != nil {
		return localWorkManifest{}, "", err
	}
	return result.manifest, result.runDir, nil
}

func nullableString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func parseLocalWorkStartArgs(args []string) (localWorkStartOptions, error) {
	options := localWorkStartOptions{
		MaxIterations:         localWorkDefaultMaxIterations,
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
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
		case token == "--detach":
			options.Detach = true
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
		case token == "--run-id":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--run-id")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			options.RunID = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--run-id="):
			options.RunID = strings.TrimSpace(strings.TrimPrefix(token, "--run-id="))
		case token == "--work-type":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--work-type")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			options.WorkType = value
			index++
		case strings.HasPrefix(token, "--work-type="):
			options.WorkType = strings.TrimSpace(strings.TrimPrefix(token, "--work-type="))
		case token == "--max-iterations":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--max-iterations")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 {
				return localWorkStartOptions{}, fmt.Errorf("Invalid --max-iterations value %q.\n%s", value, LocalWorkHelp)
			}
			options.MaxIterations = parsed
			index++
		case strings.HasPrefix(token, "--max-iterations="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--max-iterations="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return localWorkStartOptions{}, fmt.Errorf("Invalid --max-iterations value %q.\n%s", value, LocalWorkHelp)
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
		case token == "--grouping-policy":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--grouping-policy")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			options.GroupingPolicy = strings.ToLower(strings.TrimSpace(value))
			index++
		case strings.HasPrefix(token, "--grouping-policy="):
			options.GroupingPolicy = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(token, "--grouping-policy=")))
		case token == "--validation-parallelism":
			value, err := requireLocalWorkFlagValue(parseArgs, index, "--validation-parallelism")
			if err != nil {
				return localWorkStartOptions{}, err
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 || parsed > localWorkMaxValidationParallel {
				return localWorkStartOptions{}, fmt.Errorf("Invalid --validation-parallelism value %q. Expected 1-%d.\n%s", value, localWorkMaxValidationParallel, LocalWorkHelp)
			}
			options.ValidationParallelism = parsed
			index++
		case strings.HasPrefix(token, "--validation-parallelism="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--validation-parallelism="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 || parsed > localWorkMaxValidationParallel {
				return localWorkStartOptions{}, fmt.Errorf("Invalid --validation-parallelism value %q. Expected 1-%d.\n%s", value, localWorkMaxValidationParallel, LocalWorkHelp)
			}
			options.ValidationParallelism = parsed
		default:
			return localWorkStartOptions{}, fmt.Errorf("Unknown work start option: %s\n\n%s", token, LocalWorkHelp)
		}
	}

	if strings.TrimSpace(options.Task) != "" && strings.TrimSpace(options.PlanFile) != "" {
		return localWorkStartOptions{}, fmt.Errorf("Specify at most one of --task or --plan-file.\n%s", LocalWorkHelp)
	}
	if strings.TrimSpace(options.RunID) != "" && strings.Contains(strings.TrimSpace(options.RunID), string(os.PathSeparator)) {
		return localWorkStartOptions{}, fmt.Errorf("Invalid --run-id value %q.\n%s", options.RunID, LocalWorkHelp)
	}
	if _, err := parseRequiredWorkType(options.WorkType, "--work-type"); err != nil {
		return localWorkStartOptions{}, fmt.Errorf("%w.\n%s", err, LocalWorkHelp)
	}
	switch options.IntegrationPolicy {
	case "final", "always", "never":
	default:
		return localWorkStartOptions{}, fmt.Errorf("Invalid --integration value %q. Expected final, always, or never.\n%s", options.IntegrationPolicy, LocalWorkHelp)
	}
	switch options.GroupingPolicy {
	case localWorkDefaultGroupingPolicy, localWorkPathGroupingPolicy, localWorkSingletonPolicy:
	default:
		return localWorkStartOptions{}, fmt.Errorf("Invalid --grouping-policy value %q. Expected ai, path, or singleton.\n%s", options.GroupingPolicy, LocalWorkHelp)
	}
	return options, nil
}

func parseLocalWorkStatusArgs(args []string) (localWorkStatusOptions, error) {
	options := localWorkStatusOptions{
		RunSelection: localWorkRunSelection{UseLast: true},
	}
	selectionArgs := make([]string, 0, len(args))
	for _, token := range args {
		switch token {
		case "--json":
			options.JSON = true
		default:
			selectionArgs = append(selectionArgs, token)
		}
	}
	selection, err := parseLocalWorkRunSelection(selectionArgs, true)
	if err != nil {
		return localWorkStatusOptions{}, err
	}
	options.RunSelection = selection
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

func parseLocalWorkResolveArgs(args []string) (localWorkResolveOptions, error) {
	selection, err := parseLocalWorkRunSelection(args, true)
	if err != nil {
		return localWorkResolveOptions{}, err
	}
	return localWorkResolveOptions{RunSelection: selection}, nil
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
		case token == "--json":
			options.JSON = true
		case token == "--tail":
			value, err := requireLocalWorkFlagValue(args, index, "--tail")
			if err != nil {
				return localWorkLogsOptions{}, err
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed < 0 {
				return localWorkLogsOptions{}, fmt.Errorf("Invalid --tail value %q.\n%s", value, LocalWorkHelp)
			}
			options.TailLines = parsed
			index++
		case strings.HasPrefix(token, "--tail="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--tail="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				return localWorkLogsOptions{}, fmt.Errorf("Invalid --tail value %q.\n%s", value, LocalWorkHelp)
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
			return localWorkRunSelection{}, fmt.Errorf("Unknown work selection option: %s\n\n%s", token, LocalWorkHelp)
		}
	}
	if selection.RunID != "" && (selection.UseLast || selection.GlobalLast) {
		return localWorkRunSelection{}, fmt.Errorf("Choose only one of --run-id, --last, or --global-last.\n%s", LocalWorkHelp)
	}
	if selection.UseLast && selection.GlobalLast {
		return localWorkRunSelection{}, fmt.Errorf("Choose only one of --last or --global-last.\n%s", LocalWorkHelp)
	}
	if selection.RunID == "" && !selection.UseLast && !selection.GlobalLast {
		selection.UseLast = defaultLast
	}
	return selection, nil
}

func requireLocalWorkFlagValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, LocalWorkHelp)
	}
	return args[index+1], nil
}

func startLocalWork(cwd string, options localWorkStartOptions) error {
	_, err := startLocalWorkWithRunID(cwd, options)
	return err
}

func startLocalWorkWithRunID(cwd string, options localWorkStartOptions) (string, error) {
	repoRoot, err := resolveLocalWorkRepoRoot(cwd, options.RepoPath)
	if err != nil {
		return "", err
	}
	runID := strings.TrimSpace(options.RunID)
	if runID == "" {
		runID = fmt.Sprintf("lw-%d", time.Now().UnixNano())
	}
	sourceLockOwner := repoAccessLockOwner{
		Backend: "local-work",
		RunID:   runID,
		Purpose: "source-setup",
		Label:   "local-work-source-setup",
	}
	repoID := localWorkRepoID(repoRoot)
	repoName := filepath.Base(repoRoot)
	sandboxPath := filepath.Join(localWorkSandboxesDir(), repoID, runID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	baselineSHA := ""
	sourceBranch := ""
	if err := withSourceWriteThenReadLock(repoRoot, sourceLockOwner,
		func() error {
			if err := cleanupDirtyManagedLocalWorkRepo(repoRoot, "before local work start"); err != nil {
				return err
			}
			if err := ensureLocalWorkRepoClean(repoRoot); err != nil {
				return err
			}
			sourceBranchOutput, err := githubGitOutput(repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return err
			}
			sourceBranch = strings.TrimSpace(sourceBranchOutput)
			if _, err := syncLocalWorkTrackedBranch(repoRoot, sourceBranch, "before local work start"); err != nil {
				return err
			}
			if err := ensureLocalWorkRepoClean(repoRoot); err != nil {
				return err
			}
			baselineSHAOutput, err := githubGitOutput(repoRoot, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			sourceBranchOutput, err = githubGitOutput(repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return err
			}
			baselineSHA = strings.TrimSpace(baselineSHAOutput)
			sourceBranch = strings.TrimSpace(sourceBranchOutput)
			return nil
		},
		func() error {
			return cloneGithubSourceToSandbox(repoRoot, sandboxRepoPath)
		},
	); err != nil {
		return "", err
	}
	inputContent, inputMode, err := readLocalWorkInput(cwd, options, sourceBranch)
	if err != nil {
		return "", err
	}
	repoSlug := localWorkResolvedRepoSlug(repoRoot, "")

	repoDir := localWorkRepoDirByID(repoID)
	runDir := filepath.Join(repoDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", err
	}
	inputPath := filepath.Join(runDir, "input-plan.md")
	if err := os.WriteFile(inputPath, []byte(strings.TrimSpace(inputContent)+"\n"), 0o644); err != nil {
		return "", err
	}

	verificationPlan := detectGithubVerificationPlan(sandboxRepoPath)
	verificationScriptsDir, err := writeVerificationScripts(localWorkRuntimeName, sandboxPath, sandboxRepoPath, verificationPlan, []string{"nana", "work", "verify-refresh", "--run-id", runID})
	if err != nil {
		return "", err
	}

	now := ISOTimeNow()
	manifest := localWorkManifest{
		Version:                5,
		RunID:                  runID,
		CreatedAt:              now,
		UpdatedAt:              now,
		Status:                 "running",
		CurrentPhase:           "bootstrap",
		CurrentIteration:       0,
		RepoRoot:               repoRoot,
		RepoName:               repoName,
		RepoSlug:               repoSlug,
		RepoID:                 repoID,
		SourceBranch:           sourceBranch,
		BaselineSHA:            baselineSHA,
		SandboxPath:            sandboxPath,
		SandboxRepoPath:        sandboxRepoPath,
		VerificationPlan:       &verificationPlan,
		VerificationScriptsDir: verificationScriptsDir,
		InputPath:              inputPath,
		InputMode:              inputMode,
		WorkType:               normalizeWorkType(options.WorkType),
		IntegrationPolicy:      options.IntegrationPolicy,
		GroupingPolicy:         options.GroupingPolicy,
		ValidationParallelism:  options.ValidationParallelism,
		MaxIterations:          options.MaxIterations,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		return "", err
	}

	fmt.Fprintf(currentWorkStdout(), "[local] Starting run %s for %s\n", runID, repoRoot)
	fmt.Fprintf(currentWorkStdout(), "[local] Managed sandbox: %s\n", sandboxPath)
	fmt.Fprintf(currentWorkStdout(), "[local] Run artifacts: %s\n", runDir)
	fmt.Fprintf(currentWorkStdout(), "[local] Verification policy: lint=%d compile=%d unit=%d integration=%d benchmark=%d integration_policy=%s\n",
		len(verificationPlan.Lint), len(verificationPlan.Compile), len(verificationPlan.Unit), len(verificationPlan.Integration), len(verificationPlan.Benchmarks), options.IntegrationPolicy)
	fmt.Fprintf(currentWorkStdout(), "[local] Work type: %s\n", workTypeDisplayName(manifest.WorkType))
	fmt.Fprintf(currentWorkStdout(), "[local] Validation policy: grouping=%s parallelism=%d\n", options.GroupingPolicy, options.ValidationParallelism)
	for _, warning := range verificationPlan.Warnings {
		fmt.Fprintf(currentWorkStdout(), "[local] Verification warning: %s\n", warning)
	}

	if options.Detach {
		logPath := filepath.Join(runDir, "runtime.log")
		if err := localWorkStartDetachedRunner(repoRoot, runID, options.CodexArgs, logPath); err != nil {
			failedAt := ISOTimeNow()
			manifest.Status = "failed"
			manifest.LastError = "detached local work runner failed to start: " + err.Error()
			manifest.UpdatedAt = failedAt
			manifest.CompletedAt = failedAt
			setLocalWorkProgress(&manifest, nil, "failed", "launch-detached", 0)
			if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
				return runID, fmt.Errorf("%w (additionally failed to persist detached launch failure: %v)", err, writeErr)
			}
			return runID, err
		}
		fmt.Fprintf(currentWorkStdout(), "[local] Detached run %s; runtime log: %s\n", runID, logPath)
		return runID, nil
	}

	return runID, localWorkExecuteLoop(runID, options.CodexArgs, options.RateLimitPolicy)
}

func launchLocalWorkDetachedRunner(repoRoot string, runID string, codexArgs []string, logPath string) error {
	cmd, err := startManagedNanaCommand("work", "resume", "--run-id", runID, "--repo", repoRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if len(codexArgs) > 0 {
		cmd.Args = append(cmd.Args, "--")
		cmd.Args = append(cmd.Args, codexArgs...)
	}
	cmd.Dir = repoRoot
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := startManagedNanaStart(cmd); err != nil {
		_ = logFile.Close()
		return err
	}
	go func() {
		defer logFile.Close()
		_ = cmd.Wait()
	}()
	return nil
}

func resumeLocalWork(cwd string, options localWorkResumeOptions) error {
	manifest, _, err := resolveLocalWorkRun(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	if manifest.Status == "completed" {
		return fmt.Errorf("work run %s is already completed", manifest.RunID)
	}
	if supersededBy, supersededReason, err := localWorkEffectiveSupersededInfo(manifest); err != nil {
		return err
	} else if supersededBy != "" {
		completeSupersededLocalWorkRun(&manifest, supersededBy, supersededReason, ISOTimeNow())
		if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
			return writeErr
		}
	}
	if manifest.Status == "completed" && localWorkIsSuperseded(manifest) {
		fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s; superseded by %s.\n", manifest.RunID, manifest.SupersededByRunID)
		return nil
	}
	if manifest.Status == "blocked" && manifest.FinalApplyStatus == "blocked-before-apply" {
		return retryBlockedLocalWorkFinalApply(manifest)
	}
	if manifest.Status == "blocked" {
		return fmt.Errorf("work run %s is blocked: %s", manifest.RunID, defaultString(manifest.LastError, manifest.FinalApplyError))
	}
	if len(manifest.Iterations) >= manifest.MaxIterations {
		return fmt.Errorf("work run %s has already exhausted max iterations (%d)", manifest.RunID, manifest.MaxIterations)
	}
	fmt.Fprintf(currentWorkStdout(), "[local] Resuming run %s for %s\n", manifest.RunID, manifest.RepoRoot)
	return localWorkExecuteLoop(manifest.RunID, options.CodexArgs, options.RateLimitPolicy)
}

func resolveLocalWork(cwd string, options localWorkResolveOptions) error {
	manifest, _, err := resolveLocalWorkRun(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	return resolveLocalWorkManifest(manifest)
}

func resolveLocalWorkManifest(manifest localWorkManifest) error {
	if manifest.Status == "completed" {
		return fmt.Errorf("work run %s is already completed", manifest.RunID)
	}
	if supersededBy, supersededReason, err := localWorkEffectiveSupersededInfo(manifest); err != nil {
		return err
	} else if supersededBy != "" {
		completeSupersededLocalWorkRun(&manifest, supersededBy, supersededReason, ISOTimeNow())
		if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
			return writeErr
		}
		fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s; superseded by %s.\n", manifest.RunID, manifest.SupersededByRunID)
		return nil
	}
	if !localWorkResolveAllowed(manifest) {
		if manifest.Status == "blocked" {
			return fmt.Errorf("work run %s is blocked and cannot be resolved automatically: %s", manifest.RunID, defaultString(localWorkBlockedNextAction(manifest), defaultString(manifest.LastError, manifest.FinalApplyError)))
		}
		return fmt.Errorf("work run %s is not blocked in an automatically resolvable state", manifest.RunID)
	}
	switch manifest.FinalApplyStatus {
	case "blocked-after-apply":
		return retryBlockedLocalWorkPostApply(manifest)
	default:
		return retryBlockedLocalWorkFinalApply(manifest)
	}
}

func retryBlockedLocalWorkFinalApply(manifest localWorkManifest) error {
	fmt.Fprintf(currentWorkStdout(), "[local] Retrying final source commit for run %s\n", manifest.RunID)
	applyResult := applyLocalWorkFinalDiff(manifest)
	return finalizeResolvedLocalWork(manifest, applyResult)
}

func retryBlockedLocalWorkPostApply(manifest localWorkManifest) error {
	fmt.Fprintf(currentWorkStdout(), "[local] Resolving post-apply blocker for run %s\n", manifest.RunID)
	applyResult, err := continueLocalWorkPostApply(manifest)
	if err != nil {
		manifest.FinalApplyStatus = applyResult.Status
		manifest.FinalApplyCommitSHA = applyResult.CommitSHA
		manifest.FinalApplyError = applyResult.Error
		manifest.FinalAppliedAt = ISOTimeNow()
		manifest.UpdatedAt = manifest.FinalAppliedAt
		manifest.LastError = applyResult.Error
		setLocalWorkProgress(&manifest, nil, "apply-blocked", "commit-source", manifest.CurrentRound)
		if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
			return writeErr
		}
		return err
	}
	return finalizeResolvedLocalWork(manifest, applyResult)
}

func continueLocalWorkPostApply(manifest localWorkManifest) (localWorkFinalApplyResult, error) {
	sourceLock, err := acquireSourceWriteLock(manifest.RepoRoot, repoAccessLockOwner{
		Backend: "local-work",
		RunID:   manifest.RunID,
		Purpose: "source-post-apply",
		Label:   "local-work-post-apply",
	})
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-after-apply", CommitSHA: strings.TrimSpace(manifest.FinalApplyCommitSHA), Error: err.Error()}, err
	}
	defer func() {
		_ = sourceLock.Release()
	}()
	releaseFinalApplyLock, err := acquireLocalWorkFinalApplyLock(manifest, "post-apply-resolve")
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-after-apply", CommitSHA: strings.TrimSpace(manifest.FinalApplyCommitSHA), Error: err.Error()}, err
	}
	defer releaseFinalApplyLock()
	sourceStatus, err := githubGitOutput(manifest.RepoRoot, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-after-apply", CommitSHA: strings.TrimSpace(manifest.FinalApplyCommitSHA), Error: err.Error()}, err
	}
	if strings.TrimSpace(manifest.FinalApplyCommitSHA) == "" {
		if strings.TrimSpace(sourceStatus) == "" {
			err := fmt.Errorf("blocked-after-apply state has no pending source changes or recorded commit")
			return localWorkFinalApplyResult{Status: "blocked-after-apply", Error: err.Error()}, err
		}
		if err := githubRunGit(manifest.RepoRoot, "commit", "-m", fmt.Sprintf("nana work: apply %s", manifest.RunID)); err != nil {
			err = fmt.Errorf("source checkout contains staged final-apply changes, but commit retry failed: %w", err)
			return localWorkFinalApplyResult{Status: "blocked-after-apply", Error: err.Error()}, err
		}
		commitSHA, err := githubGitOutput(manifest.RepoRoot, "rev-parse", "HEAD")
		if err != nil {
			return localWorkFinalApplyResult{Status: "blocked-after-apply", Error: err.Error()}, err
		}
		manifest.FinalApplyCommitSHA = strings.TrimSpace(commitSHA)
	}
	target, err := resolveLocalWorkFinalApplyTarget(manifest.RepoRoot, manifest.SourceBranch)
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-after-apply", CommitSHA: strings.TrimSpace(manifest.FinalApplyCommitSHA), Error: err.Error()}, err
	}
	if target.RemoteName != "" {
		if err := pushLocalWorkFinalApplyTarget(manifest.RepoRoot, target); err != nil {
			err = fmt.Errorf("source checkout contains committed final-apply changes, but push retry to %s failed: %w", localWorkFinalApplyTargetLabel(target, manifest.SourceBranch), err)
			return localWorkFinalApplyResult{Status: "blocked-after-apply", CommitSHA: strings.TrimSpace(manifest.FinalApplyCommitSHA), Error: err.Error()}, err
		}
		return localWorkFinalApplyResult{Status: "pushed", CommitSHA: strings.TrimSpace(manifest.FinalApplyCommitSHA)}, nil
	}
	return localWorkFinalApplyResult{Status: "committed", CommitSHA: strings.TrimSpace(manifest.FinalApplyCommitSHA)}, nil
}

func finalizeResolvedLocalWork(manifest localWorkManifest, applyResult localWorkFinalApplyResult) error {
	manifest.FinalApplyStatus = applyResult.Status
	manifest.FinalApplyCommitSHA = applyResult.CommitSHA
	manifest.FinalApplyError = applyResult.Error
	manifest.FinalAppliedAt = ISOTimeNow()
	manifest.UpdatedAt = manifest.FinalAppliedAt
	if strings.HasPrefix(applyResult.Status, "blocked") {
		manifest.LastError = applyResult.Error
		setLocalWorkProgress(&manifest, nil, "apply-blocked", "commit-source", manifest.CurrentRound)
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}
		return errors.New(applyResult.Error)
	}
	manifest.Status = "completed"
	manifest.LastError = ""
	setLocalWorkProgress(&manifest, nil, "completed", "completed", 0)
	manifest.CompletedAt = manifest.FinalAppliedAt
	if len(manifest.Iterations) > 0 {
		manifest.Iterations[len(manifest.Iterations)-1].Status = "completed"
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		return err
	}
	if err := markSupersededLocalWorkRuns(manifest); err != nil {
		return err
	}
	if _, err := writeLocalWorkRetrospective(manifest); err != nil {
		return err
	}
	switch applyResult.Status {
	case "pushed":
		fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s; committed and pushed source branch %s at %s.\n", manifest.RunID, defaultString(manifest.SourceBranch, "HEAD"), applyResult.CommitSHA)
	case "committed":
		fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s; committed to source branch at %s.\n", manifest.RunID, applyResult.CommitSHA)
	case "no-op":
		fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s; no source changes to commit.\n", manifest.RunID)
	}
	return nil
}

func executeLocalWorkLoop(runID string, codexArgs []string, rateLimitPolicy codexRateLimitPolicy) (err error) {
	defer func() {
		if err == nil {
			return
		}
		_ = persistUnexpectedLocalWorkFailure(runID, err)
	}()

	manifest, err := readLocalWorkManifestByRunID(runID)
	if err != nil {
		return err
	}
	sandboxLock, err := acquireSandboxWriteLock(manifest.SandboxRepoPath, repoAccessLockOwner{
		Backend: "local-work",
		RunID:   manifest.RunID,
		Purpose: "sandbox-execution",
		Label:   "local-work-sandbox",
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = sandboxLock.Release()
	}()
	manifest.RateLimitPolicy = string(codexRateLimitPolicyDefault(rateLimitPolicy))
	nextIteration := localWorkNextIteration(manifest)
	if nextIteration <= 0 {
		nextIteration = 1
	}
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)

	for iteration := nextIteration; iteration <= manifest.MaxIterations; iteration++ {
		iterationDir := localWorkIterationDir(runDir, iteration)
		if err := os.MkdirAll(iterationDir, 0o755); err != nil {
			return err
		}
		freshIterationState := false
		state, err := readLocalWorkRuntimeState(manifest.RunID, iteration)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			freshIterationState = true
			state = localWorkIterationRuntimeState{
				Version:               1,
				Iteration:             iteration,
				GroupingPolicy:        manifest.GroupingPolicy,
				ValidationParallelism: manifest.ValidationParallelism,
			}
		}
		if freshIterationState {
			if _, err := refreshLocalWorkIterationBaseline(&manifest, iteration); err != nil {
				manifest.Status = "failed"
				manifest.LastError = err.Error()
				manifest.UpdatedAt = ISOTimeNow()
				if writeErr := writeLocalWorkManifest(manifest); writeErr != nil {
					return writeErr
				}
				return err
			}
		}

		startedAt := ISOTimeNow()
		if len(manifest.Iterations) >= iteration {
			startedAt = manifest.Iterations[iteration-1].StartedAt
		}
		manifest.Status = "running"
		manifest.CurrentIteration = iteration
		manifest.LastError = ""
		manifest.PauseReason = ""
		manifest.PauseUntil = ""
		manifest.PausedAt = ""

		setLocalWorkProgress(&manifest, &state, defaultString(state.CurrentPhase, "implement"), defaultString(state.CurrentSubphase, "implement"), state.CurrentRound)
		if state.CurrentPhase == "" {
			setLocalWorkProgress(&manifest, &state, "implement", "implement", 0)
		}
		if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
			return err
		}

		implementStdoutPath := filepath.Join(iterationDir, "implement-stdout.log")
		implementStderrPath := filepath.Join(iterationDir, "implement-stderr.log")
		if !state.ImplementCompleted {
			setLocalWorkProgress(&manifest, &state, "implement", "implement", 0)
			if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
				return err
			}
			fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: implementing.\n", iteration, manifest.MaxIterations)
			implementPrompt, err := buildLocalWorkImplementPrompt(manifest, iteration)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(iterationDir, "implement-prompt.md"), []byte(implementPrompt), 0o644); err != nil {
				return err
			}
			implementResult, err := runLocalWorkCodexPrompt(manifest, codexArgs, implementPrompt, "leader", filepath.Join(iterationDir, "implement-checkpoint.json"))
			if err := os.WriteFile(implementStdoutPath, []byte(implementResult.Stdout), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(implementStderrPath, []byte(implementResult.Stderr), 0o644); err != nil {
				return err
			}
			if err != nil {
				manifest.Status = "failed"
				manifest.LastError = err.Error()
				manifest.UpdatedAt = ISOTimeNow()
				if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
					return writeErr
				}
				return err
			}
			if err := refreshLocalWorkSandboxIntentToAdd(manifest.SandboxRepoPath); err != nil {
				return err
			}
			state.ImplementCompleted = true
			if err := writeLocalWorkRuntimeState(manifest.RunID, state); err != nil {
				return err
			}
			fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: implementation complete.\n", iteration, manifest.MaxIterations)
		}

		setLocalWorkProgress(&manifest, &state, "verify-refresh", "verify-refresh", 0)
		if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
			return err
		}
		fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: refreshing verification plan.\n", iteration, manifest.MaxIterations)
		plan, scriptsDir, err := refreshLocalWorkVerificationArtifactsInPlace(&manifest)
		if err != nil {
			return err
		}
		manifest.VerificationPlan = &plan
		manifest.VerificationScriptsDir = scriptsDir
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}

		verificationPath := filepath.Join(iterationDir, "verification.json")
		initialVerification := localWorkVerificationReport{}
		if state.VerificationCompleted {
			if err := readGithubJSON(verificationPath, &initialVerification); err != nil {
				state.VerificationCompleted = false
			}
		}
		if !state.VerificationCompleted {
			setLocalWorkProgress(&manifest, &state, "verify", "verify", 0)
			if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
				return err
			}
			fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: running verification.\n", iteration, manifest.MaxIterations)
			initialVerification, err = runLocalVerification(manifest.SandboxRepoPath, plan, manifest.IntegrationPolicy == "always")
			if err != nil {
				return err
			}
			if err := os.WriteFile(verificationPath, mustMarshalJSON(initialVerification), 0o644); err != nil {
				return err
			}
			state.VerificationCompleted = true
			if err := writeLocalWorkRuntimeState(manifest.RunID, state); err != nil {
				return err
			}
			fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: verification %s.\n", iteration, manifest.MaxIterations, summarizeLocalVerification(initialVerification))
		}

		reviewPromptPath := filepath.Join(iterationDir, "review-prompt.md")
		reviewStdoutPath := filepath.Join(iterationDir, "review-stdout.log")
		reviewStderrPath := filepath.Join(iterationDir, "review-stderr.log")
		reviewFindingsPath := filepath.Join(iterationDir, "review-findings.json")
		initialFindings := []githubPullReviewFinding{}
		if state.ReviewCompleted {
			if err := readGithubJSON(reviewFindingsPath, &initialFindings); err != nil {
				state.ReviewCompleted = false
			}
		}
		if !state.ReviewCompleted {
			setLocalWorkProgress(&manifest, &state, "review", "review", 0)
			if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
				return err
			}
			fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: running review.\n", iteration, manifest.MaxIterations)
			reviewPrompt, err := buildLocalWorkReviewPrompt(manifest)
			if err != nil {
				return err
			}
			if err := os.WriteFile(reviewPromptPath, []byte(reviewPrompt), 0o644); err != nil {
				return err
			}
			reviewResult, findings, err := runLocalWorkReview(manifest, codexArgs, reviewPrompt, filepath.Join(iterationDir, "review-checkpoint.json"))
			if err := os.WriteFile(reviewStdoutPath, []byte(reviewResult.Stdout), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(reviewStderrPath, []byte(reviewResult.Stderr), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(reviewFindingsPath, mustMarshalJSON(findings), 0o644); err != nil {
				return err
			}
			if err != nil {
				manifest.Status = "failed"
				manifest.LastError = err.Error()
				manifest.UpdatedAt = ISOTimeNow()
				if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
					return writeErr
				}
				return err
			}
			initialFindings = findings
			state.ReviewCompleted = true
			if err := writeLocalWorkRuntimeState(manifest.RunID, state); err != nil {
				return err
			}
			fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: review findings=%d.\n", iteration, manifest.MaxIterations, len(initialFindings))
		}

		finalGateFindingsCount := 0
		finalGateRoles := []string{}
		finalGateRoleResults := []localWorkFinalGateRoleResult{}
		finalGateStatus := ""
		candidateAuditStatus := ""
		candidateBlockedPaths := []string{}
		if initialVerification.Passed && len(initialFindings) == 0 {
			hasDiff, err := localWorkSandboxHasDiff(manifest)
			if err != nil {
				return err
			}
			if !hasDiff {
				finalGateStatus = "no-op"
				candidateAuditStatus = "no-op"
			} else {
				audit, err := auditLocalWorkCandidateFiles(manifest)
				if err != nil {
					return err
				}
				candidateAuditStatus = audit.Status
				candidateBlockedPaths = append([]string{}, audit.BlockedPaths...)
				if audit.Status == "blocked-candidate-files" {
					finalGateStatus = "blocked"
				} else {
					setLocalWorkProgress(&manifest, &state, "final-review", "final-review", 0)
					if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
						return err
					}
					gateRolesToRun, gateRolesErr := selectLocalWorkFinalGateRolesForManifest(manifest)
					if gateRolesErr != nil {
						return gateRolesErr
					}
					fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: running final review gate (%d roles).\n", iteration, manifest.MaxIterations, len(gateRolesToRun))
					gateFindings, gateRoles, gateRoleResults, gateCount, err := runLocalWorkFinalReviewGate(manifest, codexArgs, iterationDir, "initial")
					if err != nil {
						manifest.Status = "failed"
						manifest.LastError = err.Error()
						manifest.UpdatedAt = ISOTimeNow()
						if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
							return writeErr
						}
						return err
					}
					initialFindings = append(initialFindings, gateFindings...)
					finalGateFindingsCount += gateCount
					finalGateRoles = append(finalGateRoles, gateRoles...)
					finalGateRoleResults = mergeFinalGateRoleResults(finalGateRoleResults, gateRoleResults)
					if gateCount > 0 {
						finalGateStatus = "findings"
					} else {
						finalGateStatus = "passed"
					}
					fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: final review gate %s findings=%d.\n", iteration, manifest.MaxIterations, finalGateStatus, gateCount)
				}
			}
		}

		reviewPromptContent, _ := os.ReadFile(reviewPromptPath)
		if len(reviewPromptContent) > 0 {
			if err := os.WriteFile(filepath.Join(iterationDir, "review-initial-prompt.md"), reviewPromptContent, 0o644); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(iterationDir, "review-initial-findings.json"), mustMarshalJSON(initialFindings), 0o644); err != nil {
			return err
		}

		initialFilterResult := filterKnownFindings(initialFindings, manifest.RejectedFindingFingerprints, manifest.PreexistingFindingFingerprints)

		initialGroups, initialGroupingResult, initialValidatorAttempts, validatedInitialFindings, rejectedInitialFingerprints, preexistingInitialFindings, err := runLocalWorkValidationPhase(&manifest, &state, runDir, iterationDir, codexArgs, 0, initialFilterResult.Findings)
		if err != nil {
			manifest.Status = "failed"
			manifest.LastError = err.Error()
			manifest.UpdatedAt = ISOTimeNow()
			if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
				return writeErr
			}
			return err
		}
		manifest.RejectedFindingFingerprints = uniqueStrings(append(manifest.RejectedFindingFingerprints, rejectedInitialFingerprints...))
		manifest.PreexistingFindingFingerprints = uniqueStrings(append(manifest.PreexistingFindingFingerprints, rememberedFindingFingerprints(preexistingInitialFindings)...))
		manifest.PreexistingFindings = mergeRememberedFindings(manifest.PreexistingFindings, preexistingInitialFindings)
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}
		if err := appendLocalWorkFindingHistory(manifest.RunID, buildFindingHistoryEvents(iteration, 0, "initial", validatedInitialFindings)); err != nil {
			return err
		}
		if len(initialFilterResult.Findings) > 0 {
			fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: validated findings=%d rejected=%d preexisting=%d.\n", iteration, manifest.MaxIterations, len(validatedInitialFindings), len(rejectedInitialFingerprints), len(preexistingInitialFindings))
		}

		finalVerification := initialVerification
		finalFindings := findingsFromValidated(validatedInitialFindings)
		validationGroupIDs := groupIDs(initialGroups)
		validationRationales := groupRationales(initialGroups)
		validatedFindingCount := len(validatedInitialFindings)
		rejectedFindingsCount := len(rejectedInitialFingerprints)
		preexistingFindingsCount := len(preexistingInitialFindings)
		modifiedFindingsCount := countValidatedFindingsByStatus(validatedInitialFindings, localWorkFindingModified)
		confirmedFindingsCount := countValidatedFindingsByStatus(validatedInitialFindings, localWorkFindingConfirmed)
		reviewRoundFingerprints := []string{}
		reviewFindingsByRound := []int{}
		hardeningRoundFingerprints := []string{}
		postHardeningVerificationFingerprints := []string{}
		roundsUsed := 0
		requestedGroupingPolicy := manifest.GroupingPolicy
		effectiveGroupingPolicy := initialGroupingResult.EffectivePolicy
		groupingFallbackReason := initialGroupingResult.FallbackReason
		groupingAttempts := initialGroupingResult.Attempts
		validatorAttempts := initialValidatorAttempts
		skippedRejectedFindings := initialFilterResult.SkippedRejected
		skippedPreexistingFindings := initialFilterResult.SkippedPreexisting

		for round := 1; round <= localWorkMaxReviewRounds && (!finalVerification.Passed || len(finalFindings) > 0); round++ {
			roundsUsed = round
			roundState := findLocalWorkRoundState(&state, round)

			hardeningPromptPath := filepath.Join(iterationDir, fmt.Sprintf("hardening-round-%d-prompt.md", round))
			hardeningStdoutPath := filepath.Join(iterationDir, fmt.Sprintf("hardening-round-%d-stdout.log", round))
			hardeningStderrPath := filepath.Join(iterationDir, fmt.Sprintf("hardening-round-%d-stderr.log", round))
			if !roundState.HardeningCompleted {
				preHardeningUntracked, err := localWorkUntrackedFiles(manifest.SandboxRepoPath)
				if err != nil {
					return err
				}
				setLocalWorkProgress(&manifest, &state, "harden", "hardening", round)
				if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
					return err
				}
				fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d round %d: hardening %d finding(s).\n", iteration, manifest.MaxIterations, round, len(finalFindings))
				hardeningPrompt, err := buildLocalWorkHardeningPrompt(manifest, finalVerification, finalFindings)
				if err != nil {
					return err
				}
				if err := os.WriteFile(hardeningPromptPath, []byte(hardeningPrompt), 0o644); err != nil {
					return err
				}
				hardeningResult, err := runLocalWorkCodexPrompt(manifest, codexArgs, hardeningPrompt, fmt.Sprintf("hardener-round-%d", round), filepath.Join(iterationDir, fmt.Sprintf("hardening-round-%d-checkpoint.json", round)))
				if err := os.WriteFile(hardeningStdoutPath, []byte(hardeningResult.Stdout), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(hardeningStderrPath, []byte(hardeningResult.Stderr), 0o644); err != nil {
					return err
				}
				if err != nil {
					manifest.Status = "failed"
					manifest.LastError = err.Error()
					manifest.UpdatedAt = ISOTimeNow()
					if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
						return writeErr
					}
					return err
				}
				if err := refreshLocalWorkSandboxIntentToAddIgnoring(manifest.SandboxRepoPath, preHardeningUntracked); err != nil {
					return err
				}
				roundState.HardeningCompleted = true
				if err := writeLocalWorkRuntimeState(manifest.RunID, state); err != nil {
					return err
				}
			}
			hardeningStdout, _ := os.ReadFile(hardeningStdoutPath)
			hardeningStderr, _ := os.ReadFile(hardeningStderrPath)
			hardeningRoundFingerprints = append(hardeningRoundFingerprints, sha256Hex(strings.TrimSpace(string(hardeningStdout))+"\n"+strings.TrimSpace(string(hardeningStderr))))

			postVerificationPath := filepath.Join(iterationDir, fmt.Sprintf("verification-round-%d-post-hardening.json", round))
			if !roundState.PostVerificationCompleted {
				setLocalWorkProgress(&manifest, &state, "verify-post-hardening", "verify-post-hardening", round)
				if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
					return err
				}
				fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d round %d: running post-hardening verification.\n", iteration, manifest.MaxIterations, round)
				finalVerification, err = runLocalVerification(manifest.SandboxRepoPath, plan, manifest.IntegrationPolicy == "always")
				if err != nil {
					return err
				}
				if err := os.WriteFile(postVerificationPath, mustMarshalJSON(finalVerification), 0o644); err != nil {
					return err
				}
				roundState.PostVerificationCompleted = true
				if err := writeLocalWorkRuntimeState(manifest.RunID, state); err != nil {
					return err
				}
				fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d round %d: post-hardening verification %s.\n", iteration, manifest.MaxIterations, round, summarizeLocalVerification(finalVerification))
			} else if err := readGithubJSON(postVerificationPath, &finalVerification); err != nil {
				roundState.PostVerificationCompleted = false
				round--
				continue
			}
			postHardeningVerificationFingerprints = append(postHardeningVerificationFingerprints, fingerprintVerificationReport(finalVerification))

			postReviewFindingsPath := filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-findings.json", round))
			postReviewPromptPath := filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-prompt.md", round))
			postReviewStdoutPath := filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-stdout.log", round))
			postReviewStderrPath := filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-stderr.log", round))
			postHardeningFindings := []githubPullReviewFinding{}
			if !roundState.PostReviewCompleted {
				setLocalWorkProgress(&manifest, &state, "review-post-hardening", "review-post-hardening", round)
				if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
					return err
				}
				fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d round %d: running post-hardening review.\n", iteration, manifest.MaxIterations, round)
				finalReviewPrompt, err := buildLocalWorkReviewPrompt(manifest)
				if err != nil {
					return err
				}
				if err := os.WriteFile(postReviewPromptPath, []byte(finalReviewPrompt), 0o644); err != nil {
					return err
				}
				finalReviewResult, findings, err := runLocalWorkReview(manifest, codexArgs, finalReviewPrompt, filepath.Join(iterationDir, fmt.Sprintf("review-round-%d-checkpoint.json", round)))
				if err := os.WriteFile(postReviewStdoutPath, []byte(finalReviewResult.Stdout), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(postReviewStderrPath, []byte(finalReviewResult.Stderr), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(postReviewFindingsPath, mustMarshalJSON(findings), 0o644); err != nil {
					return err
				}
				if err != nil {
					manifest.Status = "failed"
					manifest.LastError = err.Error()
					manifest.UpdatedAt = ISOTimeNow()
					if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
						return writeErr
					}
					return err
				}
				postHardeningFindings = findings
				roundState.PostReviewCompleted = true
				if err := writeLocalWorkRuntimeState(manifest.RunID, state); err != nil {
					return err
				}
			} else if err := readGithubJSON(postReviewFindingsPath, &postHardeningFindings); err != nil {
				roundState.PostReviewCompleted = false
				round--
				continue
			}

			if finalVerification.Passed && len(postHardeningFindings) == 0 {
				hasDiff, err := localWorkSandboxHasDiff(manifest)
				if err != nil {
					return err
				}
				if !hasDiff {
					finalGateStatus = "no-op"
					candidateAuditStatus = "no-op"
				} else {
					audit, err := auditLocalWorkCandidateFiles(manifest)
					if err != nil {
						return err
					}
					candidateAuditStatus = audit.Status
					candidateBlockedPaths = append([]string{}, audit.BlockedPaths...)
					if audit.Status == "blocked-candidate-files" {
						finalGateStatus = "blocked"
					} else {
						setLocalWorkProgress(&manifest, &state, "final-review", "final-review", round)
						if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
							return err
						}
						gateRolesToRun, gateRolesErr := selectLocalWorkFinalGateRolesForManifest(manifest)
						if gateRolesErr != nil {
							return gateRolesErr
						}
						fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d round %d: running final review gate (%d roles).\n", iteration, manifest.MaxIterations, round, len(gateRolesToRun))
						gateFindings, gateRoles, gateRoleResults, gateCount, err := runLocalWorkFinalReviewGate(manifest, codexArgs, iterationDir, fmt.Sprintf("round-%d", round))
						if err != nil {
							manifest.Status = "failed"
							manifest.LastError = err.Error()
							manifest.UpdatedAt = ISOTimeNow()
							if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
								return writeErr
							}
							return err
						}
						postHardeningFindings = append(postHardeningFindings, gateFindings...)
						finalGateFindingsCount += gateCount
						finalGateRoles = append(finalGateRoles, gateRoles...)
						finalGateRoleResults = mergeFinalGateRoleResults(finalGateRoleResults, gateRoleResults)
						if gateCount > 0 {
							finalGateStatus = "findings"
						} else {
							finalGateStatus = "passed"
						}
						fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d round %d: final review gate %s findings=%d.\n", iteration, manifest.MaxIterations, round, finalGateStatus, gateCount)
					}
				}
			}

			filteredRoundResult := filterKnownFindings(postHardeningFindings, manifest.RejectedFindingFingerprints, manifest.PreexistingFindingFingerprints)
			roundGroups, roundGroupingResult, roundValidatorAttempts, validatedRoundFindings, rejectedRoundFingerprints, preexistingRoundFindings, err := runLocalWorkValidationPhase(&manifest, &state, runDir, iterationDir, codexArgs, round, filteredRoundResult.Findings)
			if err != nil {
				manifest.Status = "failed"
				manifest.LastError = err.Error()
				manifest.UpdatedAt = ISOTimeNow()
				if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
					return writeErr
				}
				return err
			}
			manifest.RejectedFindingFingerprints = uniqueStrings(append(manifest.RejectedFindingFingerprints, rejectedRoundFingerprints...))
			manifest.PreexistingFindingFingerprints = uniqueStrings(append(manifest.PreexistingFindingFingerprints, rememberedFindingFingerprints(preexistingRoundFindings)...))
			manifest.PreexistingFindings = mergeRememberedFindings(manifest.PreexistingFindings, preexistingRoundFindings)
			if err := writeLocalWorkManifest(manifest); err != nil {
				return err
			}
			if err := appendLocalWorkFindingHistory(manifest.RunID, buildFindingHistoryEvents(iteration, round, fmt.Sprintf("round-%d", round), validatedRoundFindings)); err != nil {
				return err
			}

			finalFindings = findingsFromValidated(validatedRoundFindings)
			validationGroupIDs = append(validationGroupIDs, groupIDs(roundGroups)...)
			validationRationales = append(validationRationales, groupRationales(roundGroups)...)
			validatedFindingCount += len(validatedRoundFindings)
			rejectedFindingsCount += len(rejectedRoundFingerprints)
			preexistingFindingsCount += len(preexistingRoundFindings)
			modifiedFindingsCount += countValidatedFindingsByStatus(validatedRoundFindings, localWorkFindingModified)
			confirmedFindingsCount += countValidatedFindingsByStatus(validatedRoundFindings, localWorkFindingConfirmed)
			reviewRoundFingerprints = append(reviewRoundFingerprints, sha256Hex(strings.Join(reviewFindingFingerprints(finalFindings), "\n")))
			reviewFindingsByRound = append(reviewFindingsByRound, len(finalFindings))
			skippedRejectedFindings += filteredRoundResult.SkippedRejected
			skippedPreexistingFindings += filteredRoundResult.SkippedPreexisting
			validatorAttempts += roundValidatorAttempts
			groupingAttempts += roundGroupingResult.Attempts
			if (len(roundGroups) > 0 || roundGroupingResult.Attempts > 0 || strings.TrimSpace(roundGroupingResult.FallbackReason) != "") && roundGroupingResult.EffectivePolicy != "" {
				effectiveGroupingPolicy = roundGroupingResult.EffectivePolicy
			}
			if groupingFallbackReason == "" && roundGroupingResult.FallbackReason != "" {
				groupingFallbackReason = roundGroupingResult.FallbackReason
			}
		}

		if roundsUsed == 0 {
			if err := os.WriteFile(filepath.Join(iterationDir, "verification-round-0-post-hardening.json"), mustMarshalJSON(finalVerification), 0o644); err != nil {
				return err
			}
		}

		followupRound := 0
		followupPlannerDecision := ""
		followupReviewDecision := ""
		followupProposedItems := 0
		followupApprovedItems := []workFollowupItem{}
		followupRejectedItems := []workFollowupRejectedItem{}
		followupApprovedKinds := []string{}
		followupMaxRoundsExceeded := false
		if finalVerification.Passed && len(finalFindings) == 0 && candidateAuditStatus != "blocked-candidate-files" {
			nextFollowupRound := len(manifest.FollowupRounds) + 1
			setLocalWorkProgress(&manifest, &state, "followup-plan", "followup-plan", nextFollowupRound)
			if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
				return err
			}
			followupSummary, approvedItems, rejectedItems, err := runLocalWorkFollowupRound(manifest, codexArgs, iterationDir, nextFollowupRound, finalVerification)
			if err != nil {
				manifest.Status = "failed"
				manifest.LastError = err.Error()
				manifest.UpdatedAt = ISOTimeNow()
				if writeErr := writeLocalWorkActiveState(runDir, &manifest, &state); writeErr != nil {
					return writeErr
				}
				return err
			}
			manifest.FollowupRounds = append(manifest.FollowupRounds, followupSummary)
			manifest.FollowupDecision = followupSummary.ReviewDecision
			if err := writeLocalWorkManifest(manifest); err != nil {
				return err
			}
			followupRound = followupSummary.Round
			followupPlannerDecision = followupSummary.PlannerDecision
			followupReviewDecision = followupSummary.ReviewDecision
			followupProposedItems = followupSummary.ProposedItems
			followupApprovedItems = append([]workFollowupItem{}, approvedItems...)
			followupRejectedItems = append([]workFollowupRejectedItem{}, rejectedItems...)
			followupApprovedKinds = append([]string{}, followupSummary.ApprovedKinds...)
			if followupReviewDecision == workFollowupDecisionApprovedFollowup && followupRound >= workFollowupMaxRounds {
				followupMaxRoundsExceeded = true
			}
		}

		integrationRan := manifest.IntegrationPolicy == "always" && len(plan.Integration) > 0
		if finalVerification.Passed && len(finalFindings) == 0 && len(followupApprovedItems) == 0 && manifest.IntegrationPolicy == "final" {
			setLocalWorkProgress(&manifest, &state, "integration", "integration", roundsUsed)
			if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
				return err
			}
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
			PreexistingFindings:                   preexistingFindingsCount,
			ModifiedFindings:                      modifiedFindingsCount,
			SkippedRejectedFindings:               skippedRejectedFindings,
			SkippedPreexistingFindings:            skippedPreexistingFindings,
			ValidationGroups:                      uniqueStrings(validationGroupIDs),
			ValidationGroupRationales:             uniqueStrings(validationRationales),
			RequestedGroupingPolicy:               requestedGroupingPolicy,
			EffectiveGroupingPolicy:               defaultString(effectiveGroupingPolicy, requestedGroupingPolicy),
			GroupingFallbackReason:                groupingFallbackReason,
			GroupingAttempts:                      groupingAttempts,
			ValidatorAttempts:                     validatorAttempts,
			ReviewRoundsUsed:                      roundsUsed,
			ReviewFindingsByRound:                 append([]int{}, reviewFindingsByRound...),
			ReviewRoundFingerprints:               append([]string{}, reviewRoundFingerprints...),
			HardeningRoundFingerprints:            append([]string{}, hardeningRoundFingerprints...),
			PostHardeningVerificationFingerprints: append([]string{}, postHardeningVerificationFingerprints...),
			ReviewFindings:                        len(finalFindings),
			ReviewFindingTitles:                   reviewFindingTitles(finalFindings),
			FinalGateFindings:                     finalGateFindingsCount,
			FinalGateRoles:                        uniqueStrings(finalGateRoles),
			FinalGateStatus:                       finalGateStatus,
			FinalGateRoleResults:                  finalGateRoleResults,
			CandidateAuditStatus:                  candidateAuditStatus,
			CandidateBlockedPaths:                 append([]string{}, candidateBlockedPaths...),
			IntegrationRan:                        integrationRan,
			FollowupRound:                         followupRound,
			FollowupPlannerDecision:               followupPlannerDecision,
			FollowupReviewDecision:                followupReviewDecision,
			FollowupProposedItems:                 followupProposedItems,
			FollowupApprovedItems:                 len(followupApprovedItems),
			FollowupRejectedItems:                 len(followupRejectedItems),
			FollowupApprovedKinds:                 append([]string{}, followupApprovedKinds...),
			ApprovedFollowupItems:                 append([]workFollowupItem{}, followupApprovedItems...),
			RejectedFollowupItems:                 append([]workFollowupRejectedItem{}, followupRejectedItems...),
		}
		manifest.FinalGateStatus = finalGateStatus
		manifest.FinalGateRoleResults = finalGateRoleResults
		manifest.CandidateAuditStatus = candidateAuditStatus
		manifest.CandidateBlockedPaths = append([]string{}, candidateBlockedPaths...)
		if strings.TrimSpace(followupReviewDecision) != "" {
			manifest.FollowupDecision = followupReviewDecision
		}
		if followupMaxRoundsExceeded {
			summary.Status = "failed"
			manifest.Status = "failed"
			manifest.LastError = fmt.Sprintf("work run %s exhausted followup rounds (%d) with approved followups still remaining", manifest.RunID, workFollowupMaxRounds)
			setLocalWorkProgress(&manifest, &state, "followup-max-rounds", "followup-review", followupRound)
		} else if finalVerification.Passed && len(finalFindings) == 0 {
			if len(followupApprovedItems) > 0 {
				setLocalWorkProgress(&manifest, &state, "followup-next-iteration", "followup-review", followupRound)
			} else if candidateAuditStatus == "blocked-candidate-files" {
				summary.Status = "blocked"
				manifest.Status = "blocked"
				manifest.LastError = localWorkCandidateBlockedMessage(candidateBlockedPaths)
				setLocalWorkProgress(&manifest, &state, "candidate-blocked", "candidate-audit", roundsUsed)
			} else {
				setLocalWorkProgress(&manifest, &state, "apply", "commit-source", roundsUsed)
				if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
					return err
				}
				fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: applying verified changes to source branch.\n", iteration, manifest.MaxIterations)
				applyResult := applyLocalWorkFinalDiff(manifest)
				manifest.FinalApplyStatus = applyResult.Status
				manifest.FinalApplyCommitSHA = applyResult.CommitSHA
				manifest.FinalApplyError = applyResult.Error
				manifest.FinalAppliedAt = ISOTimeNow()
				if strings.HasPrefix(applyResult.Status, "blocked") {
					summary.Status = "blocked"
					manifest.Status = "blocked"
					manifest.LastError = applyResult.Error
					setLocalWorkProgress(&manifest, &state, "apply-blocked", "commit-source", roundsUsed)
				} else {
					summary.Status = "completed"
				}
			}
		}

		manifest.Iterations = append(manifest.Iterations, summary)
		manifest.UpdatedAt = summary.CompletedAt
		if summary.Status != "blocked" {
			setLocalWorkProgress(&manifest, &state, "iteration-complete", "iteration-complete", 0)
		}
		if summary.Status == "completed" {
			manifest.Status = "completed"
			setLocalWorkProgress(&manifest, &state, "completed", "completed", 0)
			manifest.CompletedAt = summary.CompletedAt
		}
		if err := writeLocalWorkManifest(manifest); err != nil {
			return err
		}
		if summary.Status == "failed" {
			return errors.New(manifest.LastError)
		}
		if summary.Status == "completed" {
			if err := markSupersededLocalWorkRuns(manifest); err != nil {
				return err
			}
		}
		if err := removeLocalWorkRuntimeState(manifest.RunID, iteration); err != nil {
			return err
		}

		fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d/%d: %s\n", iteration, manifest.MaxIterations, summary.VerificationSummary)
		if len(finalFindings) > 0 {
			fmt.Fprintf(currentWorkStdout(), "[local] Iteration %d review findings: %d\n", iteration, len(finalFindings))
		}

		if summary.Status == "completed" {
			if _, err := writeLocalWorkRetrospective(manifest); err != nil {
				return err
			}
			printLocalWorkRememberedFindings(currentWorkStdout(), "Pre-existing issues excluded from propagation", manifest.PreexistingFindings)
			switch manifest.FinalApplyStatus {
			case "pushed":
				fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s after %d iteration(s); committed and pushed source branch %s at %s.\n", manifest.RunID, iteration, defaultString(manifest.SourceBranch, "HEAD"), manifest.FinalApplyCommitSHA)
			case "committed":
				fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s after %d iteration(s); committed to source branch at %s.\n", manifest.RunID, iteration, manifest.FinalApplyCommitSHA)
			case "no-op":
				fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s after %d iteration(s); no source changes to commit.\n", manifest.RunID, iteration)
			default:
				fmt.Fprintf(currentWorkStdout(), "[local] Completed run %s after %d iteration(s).\n", manifest.RunID, iteration)
			}
			return nil
		}
		if summary.Status == "blocked" {
			if _, err := writeLocalWorkRetrospective(manifest); err != nil {
				return err
			}
			blocker := defaultString(manifest.FinalApplyError, manifest.LastError)
			fmt.Fprintf(currentWorkStdout(), "[local] Blocked run %s after %d iteration(s); source commit was not created: %s\n", manifest.RunID, iteration, blocker)
			return errors.New(blocker)
		}

		if stallReason := detectLocalWorkStall(manifest.Iterations); stallReason != "" {
			manifest.Status = "failed"
			setLocalWorkProgress(&manifest, nil, "stalled", "stalled", 0)
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
	manifest.LastError = fmt.Sprintf("work run %s reached max iterations (%d)", manifest.RunID, manifest.MaxIterations)
	manifest.UpdatedAt = ISOTimeNow()
	if err := writeLocalWorkManifest(manifest); err != nil {
		return err
	}
	if _, err := writeLocalWorkRetrospective(manifest); err != nil {
		return err
	}
	return errors.New(manifest.LastError)
}

func runLocalWorkValidationPhase(manifest *localWorkManifest, state *localWorkIterationRuntimeState, runDir string, iterationDir string, codexArgs []string, round int, findings []githubPullReviewFinding) ([]localWorkFindingGroup, localWorkGroupingResult, int, []localWorkValidatedFinding, []string, []localWorkRememberedFinding, error) {
	contextName := "initial"
	if round > 0 {
		contextName = fmt.Sprintf("round-%d", round)
	}
	context := findLocalWorkValidationContext(state, contextName, round)
	setLocalWorkProgress(manifest, state, "validation", "grouping", round)
	if err := writeLocalWorkManifest(*manifest); err != nil {
		return nil, localWorkGroupingResult{}, 0, nil, nil, nil, err
	}
	if err := writeLocalWorkRuntimeState(manifest.RunID, *state); err != nil {
		return nil, localWorkGroupingResult{}, 0, nil, nil, nil, err
	}

	groupedPath := filepath.Join(iterationDir, "grouped-findings-initial.json")
	validatedPath := filepath.Join(iterationDir, "validated-findings-initial.json")
	rejectedPath := filepath.Join(iterationDir, "rejected-findings-initial.json")
	if round > 0 {
		groupedPath = filepath.Join(iterationDir, fmt.Sprintf("grouped-findings-round-%d.json", round))
		validatedPath = filepath.Join(iterationDir, fmt.Sprintf("validated-findings-round-%d.json", round))
		rejectedPath = filepath.Join(iterationDir, fmt.Sprintf("rejected-findings-round-%d.json", round))
	}

	groups := []localWorkFindingGroup{}
	groupingResult := localWorkGroupingResult{}
	if context.GroupingComplete {
		if err := readGithubJSON(groupedPath, &groups); err != nil {
			context.GroupingComplete = false
		}
		groupingResultPath := strings.Replace(groupedPath, "grouped-findings", "grouping", 1)
		groupingResultPath = strings.Replace(groupingResultPath, ".json", "-result.json", 1)
		_ = readGithubJSON(groupingResultPath, &groupingResult)
	}
	if !context.GroupingComplete {
		var err error
		groupingResult, groups, err = groupFindings(manifest, codexArgs, iterationDir, round, findings)
		if err != nil {
			return nil, localWorkGroupingResult{}, 0, nil, nil, nil, err
		}
		if err := writeJSONArtifact(groupedPath, groups); err != nil {
			return nil, localWorkGroupingResult{}, 0, nil, nil, nil, err
		}
		context.RequestedPolicy = groupingResult.RequestedPolicy
		context.EffectivePolicy = groupingResult.EffectivePolicy
		context.FallbackReason = groupingResult.FallbackReason
		context.Attempts = groupingResult.Attempts
		context.GroupingComplete = true
		context.GroupStates = make([]localWorkRuntimeGroupState, 0, len(groups))
		for _, group := range groups {
			context.GroupStates = append(context.GroupStates, localWorkRuntimeGroupState{
				GroupID:   group.GroupID,
				Rationale: group.Rationale,
			})
		}
		if err := writeLocalWorkActiveState(runDir, manifest, state); err != nil {
			return nil, localWorkGroupingResult{}, 0, nil, nil, nil, err
		}
	}

	setLocalWorkProgress(manifest, state, "validation", "validation", round)
	if err := writeLocalWorkActiveState(runDir, manifest, state); err != nil {
		return nil, localWorkGroupingResult{}, 0, nil, nil, nil, err
	}

	validated := []localWorkValidatedFinding{}
	rejected := []string{}
	validatorAttempts := 0
	if context.ValidationComplete {
		if err := readGithubJSON(validatedPath, &validated); err != nil {
			context.ValidationComplete = false
		} else {
			_ = readGithubJSON(rejectedPath, &rejected)
		}
	}
	if !context.ValidationComplete {
		var err error
		validated, rejected, validatorAttempts, err = validateFindingGroups(*manifest, codexArgs, iterationDir, round, groups, context, func() error {
			return writeLocalWorkActiveState(runDir, manifest, state)
		})
		if err != nil {
			_ = writeLocalWorkActiveState(runDir, manifest, state)
			return nil, groupingResult, validatorAttempts, nil, nil, nil, err
		}
		if err := writeJSONArtifact(validatedPath, validated); err != nil {
			return nil, groupingResult, validatorAttempts, nil, nil, nil, err
		}
		if err := writeJSONArtifact(rejectedPath, rejected); err != nil {
			return nil, groupingResult, validatorAttempts, nil, nil, nil, err
		}
		context.ValidationComplete = true
		if err := writeLocalWorkActiveState(runDir, manifest, state); err != nil {
			return nil, groupingResult, validatorAttempts, nil, nil, nil, err
		}
	}
	return groups, groupingResult, validatorAttempts, validated, rejected, rememberedFindingsFromValidated(validated, localWorkFindingPreexisting), nil
}

func buildFindingHistoryEvents(iteration int, round int, phase string, validated []localWorkValidatedFinding) []localWorkFindingHistoryEvent {
	events := make([]localWorkFindingHistoryEvent, 0, len(validated))
	for _, item := range validated {
		event := localWorkFindingHistoryEvent{
			Iteration:             iteration,
			Round:                 round,
			Phase:                 phase,
			GroupID:               item.GroupID,
			GroupRationale:        item.GroupRationale,
			OriginalFingerprint:   item.OriginalFingerprint,
			CurrentFingerprint:    item.CurrentFingerprint,
			Status:                item.Status,
			Reason:                item.Reason,
			SupersedesFingerprint: item.SupersedesFingerprint,
		}
		if item.Finding != nil {
			event.Title = item.Finding.Title
			event.Path = item.Finding.Path
			event.Line = item.Finding.Line
		}
		events = append(events, event)
	}
	return events
}

func groupRationales(groups []localWorkFindingGroup) []string {
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		if strings.TrimSpace(group.Rationale) == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s", group.GroupID, strings.TrimSpace(group.Rationale)))
	}
	return out
}

type localWorkStatusSnapshot struct {
	RunID                    string                           `json:"run_id"`
	RepoRoot                 string                           `json:"repo_root"`
	RunArtifacts             string                           `json:"run_artifacts"`
	Sandbox                  string                           `json:"sandbox"`
	Status                   string                           `json:"status"`
	Iteration                int                              `json:"iteration"`
	MaxIterations            int                              `json:"max_iterations"`
	Phase                    string                           `json:"phase,omitempty"`
	Subphase                 string                           `json:"subphase,omitempty"`
	Round                    int                              `json:"round,omitempty"`
	WorkType                 string                           `json:"work_type,omitempty"`
	LastVerification         string                           `json:"last_verification,omitempty"`
	LastReviewFindings       int                              `json:"last_review_findings,omitempty"`
	LastIteration            *localWorkIterationSummary       `json:"last_iteration,omitempty"`
	ActiveValidationContext  *localWorkValidationContextState `json:"active_validation_context,omitempty"`
	RejectedFingerprintCount int                              `json:"rejected_fingerprint_count,omitempty"`
	PreexistingFindingCount  int                              `json:"preexisting_finding_count,omitempty"`
	FinalApplyStatus         string                           `json:"final_apply_status,omitempty"`
	FinalApplyCommitSHA      string                           `json:"final_apply_commit_sha,omitempty"`
	FinalApplyError          string                           `json:"final_apply_error,omitempty"`
	FinalGateStatus          string                           `json:"final_gate_status,omitempty"`
	FinalGateRoleResults     []localWorkFinalGateRoleResult   `json:"final_gate_role_results,omitempty"`
	CandidateAuditStatus     string                           `json:"candidate_audit_status,omitempty"`
	CandidateBlockedPaths    []string                         `json:"candidate_blocked_paths,omitempty"`
	FollowupDecision         string                           `json:"followup_decision,omitempty"`
	FollowupRounds           []workFollowupRoundSummary       `json:"followup_rounds,omitempty"`
	NextAction               string                           `json:"next_action,omitempty"`
	LastError                string                           `json:"last_error,omitempty"`
	PauseReason              string                           `json:"pause_reason,omitempty"`
	PauseUntil               string                           `json:"pause_until,omitempty"`
	LockState                *repoAccessLockStatusSnapshot    `json:"lock_state,omitempty"`
}

func localWorkStatus(cwd string, options localWorkStatusOptions) error {
	return localWorkStatusWithIO(cwd, options, currentWorkStdout())
}

func localWorkStatusWithIO(cwd string, options localWorkStatusOptions, stdout io.Writer) error {
	manifest, runDir, err := resolveLocalWorkRun(cwd, options.RunSelection)
	if err != nil {
		return localWorkReadCommandError(err)
	}
	snapshot, err := localWorkBuildStatusSnapshot(manifest, runDir)
	if err != nil {
		return localWorkReadCommandError(err)
	}
	if options.JSON {
		_, err := stdout.Write(mustMarshalJSON(snapshot))
		return err
	}
	fmt.Fprintf(stdout, "[local] Run id: %s\n", snapshot.RunID)
	fmt.Fprintf(stdout, "[local] Repo root: %s\n", snapshot.RepoRoot)
	fmt.Fprintf(stdout, "[local] Run artifacts: %s\n", snapshot.RunArtifacts)
	fmt.Fprintf(stdout, "[local] Sandbox: %s\n", snapshot.Sandbox)
	fmt.Fprintf(stdout, "[local] Status: %s\n", snapshot.Status)
	if strings.TrimSpace(snapshot.WorkType) != "" {
		fmt.Fprintf(stdout, "[local] Work type: %s\n", workTypeDisplayName(snapshot.WorkType))
	}
	if strings.TrimSpace(snapshot.PauseUntil) != "" {
		fmt.Fprintf(stdout, "[local] Pause until: %s", snapshot.PauseUntil)
		if strings.TrimSpace(snapshot.PauseReason) != "" {
			fmt.Fprintf(stdout, " reason=%s", snapshot.PauseReason)
		}
		fmt.Fprintln(stdout)
	}
	if strings.TrimSpace(snapshot.FinalApplyStatus) != "" {
		fmt.Fprintf(stdout, "[local] Final apply: %s", snapshot.FinalApplyStatus)
		if strings.TrimSpace(snapshot.FinalApplyCommitSHA) != "" {
			fmt.Fprintf(stdout, " commit=%s", snapshot.FinalApplyCommitSHA)
		}
		if strings.TrimSpace(snapshot.FinalApplyError) != "" {
			fmt.Fprintf(stdout, " error=%s", snapshot.FinalApplyError)
		}
		fmt.Fprintln(stdout)
	}
	if strings.TrimSpace(snapshot.FinalGateStatus) != "" {
		fmt.Fprintf(stdout, "[local] Final gate: %s", snapshot.FinalGateStatus)
		if len(snapshot.FinalGateRoleResults) > 0 {
			parts := make([]string, 0, len(snapshot.FinalGateRoleResults))
			for _, result := range snapshot.FinalGateRoleResults {
				parts = append(parts, fmt.Sprintf("%s=%d", result.Role, result.Findings))
			}
			fmt.Fprintf(stdout, " %s", strings.Join(parts, ","))
		}
		fmt.Fprintln(stdout)
	}
	if strings.TrimSpace(snapshot.CandidateAuditStatus) != "" {
		fmt.Fprintf(stdout, "[local] Candidate audit: %s", snapshot.CandidateAuditStatus)
		if len(snapshot.CandidateBlockedPaths) > 0 {
			fmt.Fprintf(stdout, " blocked=%s", strings.Join(snapshot.CandidateBlockedPaths, ","))
		}
		fmt.Fprintln(stdout)
	}
	if strings.TrimSpace(snapshot.NextAction) != "" {
		fmt.Fprintf(stdout, "[local] Next action: %s\n", snapshot.NextAction)
	}
	if strings.TrimSpace(snapshot.FollowupDecision) != "" {
		fmt.Fprintf(stdout, "[local] Followups: %s (rounds=%d)\n", snapshot.FollowupDecision, len(snapshot.FollowupRounds))
	}
	fmt.Fprintf(stdout, "[local] Iteration: %d/%d (phase=%s", snapshot.Iteration, snapshot.MaxIterations, defaultString(snapshot.Phase, "n/a"))
	if strings.TrimSpace(snapshot.Subphase) != "" {
		fmt.Fprintf(stdout, ", subphase=%s", snapshot.Subphase)
	}
	if snapshot.Round > 0 {
		fmt.Fprintf(stdout, ", round=%d", snapshot.Round)
	}
	fmt.Fprintln(stdout, ")")
	if snapshot.LastIteration != nil {
		fmt.Fprintf(stdout, "[local] Last verification: %s\n", snapshot.LastVerification)
		fmt.Fprintf(stdout, "[local] Last review findings: %d\n", snapshot.LastReviewFindings)
		fmt.Fprintf(stdout, "[local] Last validation: groups=%d validated=%d confirmed=%d rejected=%d preexisting=%d modified=%d skipped-rejected=%d skipped-preexisting=%d policy=%s",
			len(snapshot.LastIteration.ValidationGroups),
			snapshot.LastIteration.ValidatedFindings,
			snapshot.LastIteration.ConfirmedFindings,
			snapshot.LastIteration.RejectedFindings,
			snapshot.LastIteration.PreexistingFindings,
			snapshot.LastIteration.ModifiedFindings,
			snapshot.LastIteration.SkippedRejectedFindings,
			snapshot.LastIteration.SkippedPreexistingFindings,
			defaultString(snapshot.LastIteration.EffectiveGroupingPolicy, snapshot.LastIteration.RequestedGroupingPolicy))
		if strings.TrimSpace(snapshot.LastIteration.GroupingFallbackReason) != "" {
			fmt.Fprintf(stdout, " fallback=%s", snapshot.LastIteration.GroupingFallbackReason)
		}
		fmt.Fprintln(stdout)
	}
	if snapshot.ActiveValidationContext != nil {
		fmt.Fprintf(stdout, "[local] Active validation context: %s", snapshot.ActiveValidationContext.Name)
		if snapshot.ActiveValidationContext.Round > 0 {
			fmt.Fprintf(stdout, " (round=%d)", snapshot.ActiveValidationContext.Round)
		}
		fmt.Fprintf(stdout, " policy=%s", defaultString(snapshot.ActiveValidationContext.EffectivePolicy, snapshot.ActiveValidationContext.RequestedPolicy))
		if strings.TrimSpace(snapshot.ActiveValidationContext.FallbackReason) != "" {
			fmt.Fprintf(stdout, " fallback=%s", snapshot.ActiveValidationContext.FallbackReason)
		}
		fmt.Fprintln(stdout)
		for _, group := range snapshot.ActiveValidationContext.GroupStates {
			if strings.TrimSpace(group.Status) == "" {
				continue
			}
			fmt.Fprintf(stdout, "[local] Validation group: %s status=%s attempts=%d", group.GroupID, group.Status, group.Attempts)
			if strings.TrimSpace(group.Rationale) != "" {
				fmt.Fprintf(stdout, " rationale=%s", group.Rationale)
			}
			if strings.TrimSpace(group.LastError) != "" {
				fmt.Fprintf(stdout, " error=%s", group.LastError)
			}
			fmt.Fprintln(stdout)
		}
	}
	fmt.Fprintf(stdout, "[local] Stored rejected findings: %d\n", snapshot.RejectedFingerprintCount)
	fmt.Fprintf(stdout, "[local] Stored pre-existing findings: %d\n", snapshot.PreexistingFindingCount)
	if strings.TrimSpace(snapshot.LastError) != "" {
		fmt.Fprintf(stdout, "[local] Last error: %s\n", snapshot.LastError)
	}
	if snapshot.LockState != nil {
		if repoAccessLockStateHasHolders(snapshot.LockState.Source) {
			fmt.Fprintf(stdout, "[local] Repo lock (source): %s\n", repoAccessLockStateSummary(snapshot.LockState.Source))
		}
		if repoAccessLockStateHasHolders(snapshot.LockState.Sandbox) {
			fmt.Fprintf(stdout, "[local] Repo lock (sandbox): %s\n", repoAccessLockStateSummary(snapshot.LockState.Sandbox))
		}
	}
	return nil
}

func localWorkBuildStatusSnapshot(manifest localWorkManifest, runDir string) (localWorkStatusSnapshot, error) {
	var runtimeState *localWorkIterationRuntimeState
	if manifest.CurrentIteration > 0 {
		state, err := readLocalWorkRuntimeState(manifest.RunID, manifest.CurrentIteration)
		if err != nil && !os.IsNotExist(err) {
			return localWorkStatusSnapshot{}, err
		}
		if err == nil {
			runtimeState = &state
		}
	}
	activeContext := localWorkActiveValidationContextFromRuntimeState(runtimeState)
	lockState, err := buildRepoAccessLockStatus(manifest.RepoRoot, repoAccessLockWrite, manifest.SandboxRepoPath, repoAccessLockWrite)
	if err != nil {
		return localWorkStatusSnapshot{}, err
	}
	snapshot := localWorkStatusSnapshot{
		RunID:                    manifest.RunID,
		RepoRoot:                 manifest.RepoRoot,
		RunArtifacts:             runDir,
		Sandbox:                  manifest.SandboxPath,
		Status:                   manifest.Status,
		Iteration:                manifest.CurrentIteration,
		MaxIterations:            manifest.MaxIterations,
		Phase:                    manifest.CurrentPhase,
		Subphase:                 manifest.CurrentSubphase,
		Round:                    manifest.CurrentRound,
		WorkType:                 manifest.WorkType,
		ActiveValidationContext:  activeContext,
		RejectedFingerprintCount: len(manifest.RejectedFindingFingerprints),
		PreexistingFindingCount:  len(manifest.PreexistingFindings),
		FinalApplyStatus:         manifest.FinalApplyStatus,
		FinalApplyCommitSHA:      manifest.FinalApplyCommitSHA,
		FinalApplyError:          manifest.FinalApplyError,
		FinalGateStatus:          manifest.FinalGateStatus,
		FinalGateRoleResults:     append([]localWorkFinalGateRoleResult{}, manifest.FinalGateRoleResults...),
		CandidateAuditStatus:     manifest.CandidateAuditStatus,
		CandidateBlockedPaths:    append([]string{}, manifest.CandidateBlockedPaths...),
		FollowupDecision:         manifest.FollowupDecision,
		FollowupRounds:           append([]workFollowupRoundSummary{}, manifest.FollowupRounds...),
		NextAction:               localWorkBlockedNextAction(manifest),
		LastError:                manifest.LastError,
		PauseReason:              manifest.PauseReason,
		PauseUntil:               manifest.PauseUntil,
		LockState:                lockState,
	}
	if runtimeState != nil {
		snapshot.Phase = runtimeState.CurrentPhase
		snapshot.Subphase = runtimeState.CurrentSubphase
		snapshot.Round = runtimeState.CurrentRound
	}
	if len(manifest.Iterations) > 0 {
		last := manifest.Iterations[len(manifest.Iterations)-1]
		snapshot.LastVerification = last.VerificationSummary
		snapshot.LastReviewFindings = last.ReviewFindings
		snapshot.LastIteration = &last
	}
	return snapshot, nil
}

func localWorkActiveValidationContextFromRuntimeState(runtimeState *localWorkIterationRuntimeState) *localWorkValidationContextState {
	if runtimeState == nil {
		return nil
	}
	expectedName := "initial"
	if runtimeState.CurrentRound > 0 {
		expectedName = fmt.Sprintf("round-%d", runtimeState.CurrentRound)
	}
	for _, context := range runtimeState.ValidationContexts {
		if context.Name == expectedName {
			copied := context
			return &copied
		}
	}
	for _, context := range runtimeState.ValidationContexts {
		for _, group := range context.GroupStates {
			if group.Status == "failed" || group.Status == "running" {
				copied := context
				return &copied
			}
		}
	}
	return nil
}

func loadLocalWorkActiveValidationContext(runID string, iteration int, round int) *localWorkValidationContextState {
	if iteration <= 0 {
		return nil
	}
	state, err := readLocalWorkRuntimeState(runID, iteration)
	if err != nil {
		return nil
	}
	state.CurrentRound = round
	return localWorkActiveValidationContextFromRuntimeState(&state)
}

func localWorkLogs(cwd string, options localWorkLogsOptions) error {
	return localWorkLogsWithIO(cwd, options, currentWorkStdout())
}

func localWorkLogsWithIO(cwd string, options localWorkLogsOptions, stdout io.Writer) error {
	manifest, runDir, err := resolveLocalWorkRun(cwd, options.RunSelection)
	if err != nil {
		return localWorkReadCommandError(err)
	}
	iteration := manifest.CurrentIteration
	if iteration <= 0 && len(manifest.Iterations) > 0 {
		iteration = manifest.Iterations[len(manifest.Iterations)-1].Iteration
	}
	if iteration <= 0 {
		return fmt.Errorf("work run %s has no iteration artifacts yet", manifest.RunID)
	}
	iterationDir := localWorkIterationDir(runDir, iteration)
	if _, err := os.Stat(iterationDir); err != nil {
		return fmt.Errorf("work run %s iteration %d logs not found at %s", manifest.RunID, iteration, iterationDir)
	}
	snapshot, err := localWorkBuildStatusSnapshot(manifest, runDir)
	if err != nil {
		return localWorkReadCommandError(err)
	}
	grouping := localWorkLatestGroupingResult(iterationDir)
	files, err := localWorkLogFiles(iterationDir)
	if err != nil {
		return err
	}
	entries := make([]map[string]string, 0, len(files))
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		display := string(content)
		if options.TailLines > 0 {
			display = tailLines(display, options.TailLines)
		}
		entries = append(entries, map[string]string{
			"name":    filepath.Base(path),
			"path":    path,
			"content": display,
		})
	}
	if options.JSON {
		payload := map[string]interface{}{
			"run":           snapshot,
			"iteration":     iteration,
			"artifacts":     iterationDir,
			"runtime_state": loadLocalWorkRuntimeStateSummary(manifest.RunID, iteration),
			"grouping":      grouping,
			"files":         entries,
		}
		_, err := stdout.Write(mustMarshalJSON(payload))
		return err
	}
	fmt.Fprintf(stdout, "[local] Run id: %s\n", manifest.RunID)
	fmt.Fprintf(stdout, "[local] Iteration: %d\n", iteration)
	fmt.Fprintf(stdout, "[local] Iteration artifacts: %s\n", iterationDir)
	if snapshot.LastIteration != nil {
		fmt.Fprintf(stdout, "[local] Validation summary: groups=%d validated=%d confirmed=%d rejected=%d preexisting=%d modified=%d skipped-rejected=%d skipped-preexisting=%d policy=%s",
			len(snapshot.LastIteration.ValidationGroups),
			snapshot.LastIteration.ValidatedFindings,
			snapshot.LastIteration.ConfirmedFindings,
			snapshot.LastIteration.RejectedFindings,
			snapshot.LastIteration.PreexistingFindings,
			snapshot.LastIteration.ModifiedFindings,
			snapshot.LastIteration.SkippedRejectedFindings,
			snapshot.LastIteration.SkippedPreexistingFindings,
			defaultString(snapshot.LastIteration.EffectiveGroupingPolicy, snapshot.LastIteration.RequestedGroupingPolicy))
		if strings.TrimSpace(snapshot.LastIteration.GroupingFallbackReason) != "" {
			fmt.Fprintf(stdout, " fallback=%s", snapshot.LastIteration.GroupingFallbackReason)
		}
		fmt.Fprintln(stdout)
		for _, rationale := range snapshot.LastIteration.ValidationGroupRationales {
			fmt.Fprintf(stdout, "[local] Group: %s\n", rationale)
		}
	}
	if len(grouping.Groups) > 0 {
		fmt.Fprintf(stdout, "[local] Effective grouping: %s\n", defaultString(grouping.EffectivePolicy, grouping.RequestedPolicy))
	}
	if snapshot.ActiveValidationContext != nil {
		fmt.Fprintf(stdout, "[local] Active validation context: %s", snapshot.ActiveValidationContext.Name)
		if snapshot.ActiveValidationContext.Round > 0 {
			fmt.Fprintf(stdout, " (round=%d)", snapshot.ActiveValidationContext.Round)
		}
		fmt.Fprintf(stdout, " policy=%s", defaultString(snapshot.ActiveValidationContext.EffectivePolicy, snapshot.ActiveValidationContext.RequestedPolicy))
		if strings.TrimSpace(snapshot.ActiveValidationContext.FallbackReason) != "" {
			fmt.Fprintf(stdout, " fallback=%s", snapshot.ActiveValidationContext.FallbackReason)
		}
		fmt.Fprintln(stdout)
		for _, group := range snapshot.ActiveValidationContext.GroupStates {
			if strings.TrimSpace(group.Status) == "" {
				continue
			}
			fmt.Fprintf(stdout, "[local] Validation group: %s status=%s attempts=%d", group.GroupID, group.Status, group.Attempts)
			if strings.TrimSpace(group.Rationale) != "" {
				fmt.Fprintf(stdout, " rationale=%s", group.Rationale)
			}
			if strings.TrimSpace(group.LastError) != "" {
				fmt.Fprintf(stdout, " error=%s", group.LastError)
			}
			fmt.Fprintln(stdout)
		}
	}
	for _, entry := range entries {
		fmt.Fprintf(stdout, "\n== %s ==\n", entry["name"])
		if strings.TrimSpace(entry["content"]) == "" {
			fmt.Fprintln(stdout, "(empty)")
			continue
		}
		fmt.Fprint(stdout, entry["content"])
		if !strings.HasSuffix(entry["content"], "\n") {
			fmt.Fprintln(stdout)
		}
	}
	return nil
}

func loadLocalWorkRuntimeStateSummary(runID string, iteration int) interface{} {
	if iteration <= 0 {
		return nil
	}
	state, err := readLocalWorkRuntimeState(runID, iteration)
	if err != nil {
		return nil
	}
	return state
}

func localWorkLogFiles(iterationDir string) ([]string, error) {
	patterns := []string{
		"runtime-state.json",
		"implement-stdout.log",
		"implement-stderr.log",
		"review-stdout.log",
		"review-stderr.log",
		"grouping-*.json",
		"grouping-*.log",
		"grouping-*.md",
		"grouped-findings-*.json",
		"validated-findings-*.json",
		"rejected-findings-*.json",
		"hardening-round-*-stdout.log",
		"hardening-round-*-stderr.log",
		"review-round-*-stdout.log",
		"review-round-*-stderr.log",
		"final-gate-*-prompt.md",
		"final-gate-*-stdout.log",
		"final-gate-*-stderr.log",
		"final-gate-*-findings.json",
		"verification.json",
		"verification-round-*-post-hardening.json",
		"review-initial-findings.json",
		"review-round-*-findings.json",
		"validation-groups/*/*",
	}
	seen := map[string]bool{}
	files := []string{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(iterationDir, pattern))
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if seen[match] {
				continue
			}
			seen[match] = true
			files = append(files, match)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no log files found at %s", iterationDir)
	}
	return files, nil
}

func localWorkLatestGroupingResult(iterationDir string) localWorkGroupingResult {
	patterns := []string{
		filepath.Join(iterationDir, "grouping-round-*-result.json"),
		filepath.Join(iterationDir, "grouping-initial-result.json"),
	}
	matches := []string{}
	for _, pattern := range patterns {
		found, _ := filepath.Glob(pattern)
		matches = append(matches, found...)
	}
	sort.Strings(matches)
	for i := len(matches) - 1; i >= 0; i-- {
		var result localWorkGroupingResult
		if err := readGithubJSON(matches[i], &result); err == nil {
			return result
		}
	}
	return localWorkGroupingResult{}
}

func localWorkRetrospective(cwd string, selection localWorkRunSelection) error {
	manifest, _, err := resolveLocalWorkRun(cwd, selection)
	if err != nil {
		return localWorkReadCommandError(err)
	}
	content, err := writeLocalWorkRetrospective(manifest)
	if err != nil {
		return localWorkReadCommandError(err)
	}
	fmt.Fprint(currentWorkStdout(), content)
	return nil
}

func refreshLocalWorkVerificationArtifacts(cwd string, selection localWorkRunSelection) error {
	return refreshLocalWorkVerificationArtifactsWithIO(cwd, selection, currentWorkStdout())
}

func refreshLocalWorkVerificationArtifactsWithIO(cwd string, selection localWorkRunSelection, stdout io.Writer) error {
	manifest, _, err := resolveLocalWorkRun(cwd, selection)
	if err != nil {
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
	fmt.Fprintf(stdout, "[local] Verification artifacts for run %s refreshed.\n", manifest.RunID)
	fmt.Fprintf(stdout, "[local] Verification scripts directory: %s\n", scriptsDir)
	return nil
}

func refreshLocalWorkVerificationArtifactsInPlace(manifest *localWorkManifest) (githubVerificationPlan, string, error) {
	plan := detectGithubVerificationPlan(manifest.SandboxRepoPath)
	scriptsDir, err := writeVerificationScripts(localWorkRuntimeName, manifest.SandboxPath, manifest.SandboxRepoPath, plan, []string{"nana", "work", "verify-refresh", "--run-id", manifest.RunID})
	if err != nil {
		return githubVerificationPlan{}, "", err
	}
	return plan, scriptsDir, nil
}

func normalizeLocalWorkManifest(manifest *localWorkManifest) {
	if manifest.Version == 0 {
		manifest.Version = 5
	}
	manifest.WorkType = normalizeWorkType(manifest.WorkType)
	manifest.RepoSlug = localWorkResolvedRepoSlug(manifest.RepoRoot, manifest.RepoSlug)
	if strings.TrimSpace(manifest.GroupingPolicy) == "" {
		manifest.GroupingPolicy = localWorkDefaultGroupingPolicy
	}
	if manifest.ValidationParallelism <= 0 {
		manifest.ValidationParallelism = localWorkValidationParallelism
	}
	if manifest.ValidationParallelism > localWorkMaxValidationParallel {
		manifest.ValidationParallelism = localWorkMaxValidationParallel
	}
}

func localWorkHasResolvableBlockedApply(manifest localWorkManifest) bool {
	if manifest.Status != "blocked" {
		return false
	}
	switch strings.TrimSpace(manifest.FinalApplyStatus) {
	case "blocked-before-apply", "blocked-after-apply":
		return true
	default:
		return false
	}
}

func localWorkIsSuperseded(manifest localWorkManifest) bool {
	return strings.TrimSpace(manifest.SupersededByRunID) != ""
}

func localWorkSupersededReason(manifest localWorkManifest) string {
	if reason := strings.TrimSpace(manifest.SupersededReason); reason != "" {
		return reason
	}
	if runID := strings.TrimSpace(manifest.SupersededByRunID); runID != "" {
		return "run superseded by " + runID
	}
	return ""
}

func completeSupersededLocalWorkRun(manifest *localWorkManifest, supersededBy string, reason string, supersededAt string) {
	if manifest == nil {
		return
	}
	now := defaultString(strings.TrimSpace(supersededAt), ISOTimeNow())
	manifest.SupersededByRunID = strings.TrimSpace(supersededBy)
	manifest.SupersededAt = now
	manifest.SupersededReason = strings.TrimSpace(reason)
	manifest.Status = "completed"
	manifest.CompletedAt = defaultString(strings.TrimSpace(manifest.CompletedAt), now)
	manifest.UpdatedAt = now
	manifest.LastError = ""
	manifest.FinalApplyStatus = "superseded"
	manifest.FinalApplyError = ""
	setLocalWorkProgress(manifest, nil, "completed", "superseded", 0)
	if len(manifest.Iterations) > 0 {
		manifest.Iterations[len(manifest.Iterations)-1].Status = "completed"
	}
}

func localWorkEffectiveSupersededInfo(manifest localWorkManifest) (string, string, error) {
	if localWorkIsSuperseded(manifest) {
		return strings.TrimSpace(manifest.SupersededByRunID), localWorkSupersededReason(manifest), nil
	}
	if !localWorkHasResolvableBlockedApply(manifest) {
		return "", "", nil
	}
	type supersededInfo struct {
		runID  string
		reason string
	}
	info, err := withLocalWorkReadStore(func(store *localWorkDBStore) (supersededInfo, error) {
		rows, err := store.db.Query(`SELECT manifest_json FROM runs WHERE repo_root = ? AND updated_at > ? ORDER BY updated_at DESC`, manifest.RepoRoot, manifest.UpdatedAt)
		if err != nil {
			return supersededInfo{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return supersededInfo{}, err
			}
			var candidate localWorkManifest
			if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
				return supersededInfo{}, err
			}
			normalizeLocalWorkManifest(&candidate)
			if strings.TrimSpace(candidate.RunID) == strings.TrimSpace(manifest.RunID) {
				continue
			}
			if strings.TrimSpace(candidate.SourceBranch) != strings.TrimSpace(manifest.SourceBranch) {
				continue
			}
			if strings.TrimSpace(candidate.Status) != "completed" {
				continue
			}
			switch strings.TrimSpace(candidate.FinalApplyStatus) {
			case "committed", "pushed":
				reason := fmt.Sprintf("newer completed run %s already applied branch %s", candidate.RunID, defaultString(strings.TrimSpace(candidate.SourceBranch), "HEAD"))
				return supersededInfo{runID: candidate.RunID, reason: reason}, nil
			}
		}
		return supersededInfo{}, rows.Err()
	})
	if err != nil {
		return "", "", err
	}
	return info.runID, info.reason, nil
}

func markSupersededLocalWorkRuns(completed localWorkManifest) error {
	if strings.TrimSpace(completed.Status) != "completed" {
		return nil
	}
	switch strings.TrimSpace(completed.FinalApplyStatus) {
	case "committed", "pushed":
	default:
		return nil
	}
	reason := fmt.Sprintf("newer completed run %s already applied branch %s", completed.RunID, defaultString(strings.TrimSpace(completed.SourceBranch), "HEAD"))
	updates, err := withLocalWorkReadStore(func(store *localWorkDBStore) ([]localWorkManifest, error) {
		rows, err := store.db.Query(`SELECT manifest_json FROM runs WHERE repo_root = ? AND updated_at < ? ORDER BY updated_at DESC`, completed.RepoRoot, completed.UpdatedAt)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		updates := []localWorkManifest{}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return nil, err
			}
			var candidate localWorkManifest
			if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
				return nil, err
			}
			normalizeLocalWorkManifest(&candidate)
			if strings.TrimSpace(candidate.RunID) == strings.TrimSpace(completed.RunID) {
				continue
			}
			if strings.TrimSpace(candidate.SourceBranch) != strings.TrimSpace(completed.SourceBranch) {
				continue
			}
			if !localWorkHasResolvableBlockedApply(candidate) {
				continue
			}
			if strings.TrimSpace(candidate.SupersededByRunID) == strings.TrimSpace(completed.RunID) &&
				strings.TrimSpace(candidate.SupersededReason) == reason &&
				strings.TrimSpace(candidate.Status) == "completed" {
				continue
			}
			completeSupersededLocalWorkRun(&candidate, completed.RunID, reason, defaultString(strings.TrimSpace(completed.CompletedAt), completed.UpdatedAt))
			updates = append(updates, candidate)
		}
		return updates, rows.Err()
	})
	if err != nil {
		return err
	}
	for _, candidate := range updates {
		if err := writeLocalWorkManifest(candidate); err != nil {
			return err
		}
	}
	return nil
}

func localWorkResolvedRepoSlug(repoRoot string, current string) string {
	repoSlug := strings.TrimSpace(current)
	if validRepoSlug(repoSlug) {
		return repoSlug
	}
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return ""
	}
	inferred := strings.TrimSpace(inferGithubRepoSlugFromRepo(repoRoot))
	if validRepoSlug(inferred) {
		return inferred
	}
	return ""
}

func runLocalVerification(repoPath string, plan githubVerificationPlan, includeIntegration bool) (localWorkVerificationReport, error) {
	return runLocalVerificationStages(repoPath, plan.PlanFingerprint, includeIntegration, verificationExecutionStagesFromPlan(plan, includeIntegration))
}

func runLocalIntegrationVerification(repoPath string, plan githubVerificationPlan) (localWorkVerificationReport, error) {
	if len(plan.Integration) == 0 {
		return localWorkVerificationReport{}, nil
	}
	return runLocalVerificationStages(repoPath, plan.PlanFingerprint, true, []verificationExecutionStage{{
		Name:     "integration",
		Commands: verificationExecutionCommandsFromStrings(plan.Integration),
	}})
}

func runLocalVerificationStages(repoPath string, fingerprint string, includeIntegration bool, stages []verificationExecutionStage) (localWorkVerificationReport, error) {
	report := localWorkVerificationReport{
		GeneratedAt:         ISOTimeNow(),
		PlanFingerprint:     fingerprint,
		IntegrationIncluded: includeIntegration,
		Passed:              true,
	}
	executed, err := executeVerificationStages(repoPath, stages, verificationExecutionOptions{
		UnlimitedOutput: true,
		SanitizeEnv:     true,
		DedupeCommands:  true,
	})
	if err != nil {
		return localWorkVerificationReport{}, err
	}
	for _, stage := range executed {
		stageResult := localWorkVerificationStageResult{
			Name:   stage.Name,
			Status: stage.Status,
		}
		for _, command := range stage.Commands {
			stageResult.Commands = append(stageResult.Commands, localWorkVerificationCommandResult{
				Command:  command.Command,
				ExitCode: command.ExitCode,
				Output:   command.Output,
				Cached:   command.Cached,
			})
		}
		report.Stages = append(report.Stages, stageResult)
		if stage.Status == "failed" {
			report.Passed = false
			report.FailedStages = append(report.FailedStages, stage.Name)
		}
	}
	report.FailedStages = uniqueStrings(report.FailedStages)
	return report, nil
}

func runLocalWorkCodexPrompt(manifest localWorkManifest, codexArgs []string, prompt string, codexHomeAlias string, checkpointPath string) (localWorkExecutionResult, error) {
	sourceCodexHome := ResolveCodexHomeForLaunch(manifest.RepoRoot)
	scopedCodexHome, err := ensureScopedCodexHome(sourceCodexHome, filepath.Join(manifest.SandboxPath, ".nana", localWorkRuntimeName, "codex-home", sanitizePathToken(codexHomeAlias)))
	if err != nil {
		return localWorkExecutionResult{}, err
	}

	normalizedCodexArgs, fastMode := normalizeLocalWorkCodexArgsWithFast(codexArgs)
	prompt = prefixCodexFastPrompt(prompt, fastMode)
	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       manifest.SandboxPath,
		InstructionsRoot: manifest.SandboxPath,
		CodexHome:        scopedCodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", manifest.SandboxRepoPath},
		CommonArgs:       normalizedCodexArgs,
		Prompt:           prompt,
		PromptTransport:  codexPromptTransportStdin,
		CheckpointPath:   checkpointPath,
		StepKey:          codexHomeAlias,
		ResumeStrategy:   codexResumeSamePrompt,
		UsageRunID:       manifest.RunID,
		UsageRepoSlug:    manifest.RepoSlug,
		UsageBackend:     "local",
		UsageSandboxPath: manifest.SandboxPath,
		Env:              append(localWorkPromptEnv(manifest, scopedCodexHome), "NANA_PROJECT_AGENTS_ROOT="+manifest.SandboxRepoPath),
		RateLimitPolicy:  codexRateLimitPolicyDefault(codexRateLimitPolicy(manifest.RateLimitPolicy)),
		OnPause: func(info codexRateLimitPauseInfo) {
			updateLocalWorkPausedManifest(manifest, info, true)
		},
		OnResume: func(info codexRateLimitPauseInfo) {
			updateLocalWorkPausedManifest(manifest, info, false)
		},
	})
	execResult := localWorkExecutionResult{
		Stdout: result.Stdout,
		Stderr: result.Stderr,
	}
	if persistErr := persistPromptTokenUsage(manifest); persistErr != nil && !os.IsNotExist(persistErr) {
		if err != nil {
			return execResult, fmt.Errorf("%w (also failed to persist token usage: %v)", err, persistErr)
		}
		return execResult, persistErr
	}
	return execResult, err
}

func persistPromptTokenUsage(manifest localWorkManifest) error {
	if strings.TrimSpace(manifest.PauseManifestPath) != "" {
		return persistGithubWorkTokenUsage(manifest)
	}
	return persistLocalWorkTokenUsage(manifest.RunID)
}

func persistGithubWorkTokenUsage(manifest localWorkManifest) error {
	_ = manifest
	return nil
}

func localWorkPromptEnv(manifest localWorkManifest, scopedCodexHome string) []string {
	if strings.TrimSpace(manifest.APIBaseURL) != "" {
		return buildGithubCodexEnv(NotifyTempContract{}, scopedCodexHome, manifest.APIBaseURL)
	}
	return buildCodexEnv(NotifyTempContract{}, scopedCodexHome)
}

func updateLocalWorkPausedManifest(manifest localWorkManifest, info codexRateLimitPauseInfo, paused bool) {
	if strings.TrimSpace(manifest.PauseManifestPath) != "" {
		var githubManifest githubWorkManifest
		if err := readGithubJSON(manifest.PauseManifestPath, &githubManifest); err != nil {
			return
		}
		now := ISOTimeNow()
		if paused {
			githubManifest.ExecutionStatus = "paused"
			githubManifest.PauseReason = strings.TrimSpace(info.Reason)
			githubManifest.PauseUntil = strings.TrimSpace(info.RetryAfter)
			githubManifest.PausedAt = now
			githubManifest.LastError = codexPauseInfoMessage(info)
		} else {
			githubManifest.ExecutionStatus = "running"
			githubManifest.PauseReason = ""
			githubManifest.PauseUntil = ""
			githubManifest.PausedAt = ""
			githubManifest.LastError = ""
		}
		githubManifest.UpdatedAt = now
		_ = writeGithubJSON(manifest.PauseManifestPath, githubManifest)
		_ = indexGithubWorkRunManifest(manifest.PauseManifestPath, githubManifest)
		return
	}
	updateStandaloneLocalWorkPausedManifest(manifest.RunID, info, paused)
}

func updateStandaloneLocalWorkPausedManifest(runID string, info codexRateLimitPauseInfo, paused bool) {
	manifest, err := readLocalWorkManifestByRunID(runID)
	if err != nil {
		return
	}
	now := ISOTimeNow()
	if paused {
		manifest.Status = "paused"
		manifest.PauseReason = strings.TrimSpace(info.Reason)
		manifest.PauseUntil = strings.TrimSpace(info.RetryAfter)
		manifest.PausedAt = now
		manifest.LastError = codexPauseInfoMessage(info)
	} else {
		manifest.Status = "running"
		manifest.PauseReason = ""
		manifest.PauseUntil = ""
		manifest.PausedAt = ""
		manifest.LastError = ""
	}
	manifest.UpdatedAt = now
	_ = writeLocalWorkManifest(manifest)
}

func normalizeLocalWorkCodexArgs(args []string) []string {
	normalized, _ := normalizeLocalWorkCodexArgsWithFast(args)
	return normalized
}

func normalizeLocalWorkCodexArgsWithFast(args []string) ([]string, bool) {
	normalized, fastMode := NormalizeCodexLaunchArgsWithFast(args)
	if !hasCodexExecutionPolicyArg(normalized) {
		normalized = append([]string{CodexBypassFlag}, normalized...)
	}
	return normalized, fastMode
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

func runLocalWorkReview(manifest localWorkManifest, codexArgs []string, prompt string, checkpointPath string) (localWorkExecutionResult, []githubPullReviewFinding, error) {
	return runLocalWorkReviewWithAlias(manifest, codexArgs, prompt, "reviewer", checkpointPath)
}

func runLocalWorkReviewWithAlias(manifest localWorkManifest, codexArgs []string, prompt string, alias string, checkpointPath string) (localWorkExecutionResult, []githubPullReviewFinding, error) {
	result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, alias, checkpointPath)
	if err != nil {
		return result, nil, err
	}
	findings, parseErr := parseLocalReviewFindings(result.Stdout)
	if parseErr != nil {
		return result, nil, parseErr
	}
	return result, findings, nil
}

func groupFindings(manifest *localWorkManifest, codexArgs []string, iterationDir string, round int, findings []githubPullReviewFinding) (localWorkGroupingResult, []localWorkFindingGroup, error) {
	requestedPolicy := manifest.GroupingPolicy
	if strings.TrimSpace(requestedPolicy) == "" {
		requestedPolicy = localWorkDefaultGroupingPolicy
	}
	if len(findings) == 0 {
		return localWorkGroupingResult{
			RequestedPolicy: requestedPolicy,
			EffectivePolicy: requestedPolicy,
			Groups:          []localWorkGroupingGroup{},
		}, nil, nil
	}
	switch requestedPolicy {
	case localWorkPathGroupingPolicy:
		groups := groupFindingsByModule(findings)
		return localWorkGroupingResult{
				RequestedPolicy: requestedPolicy,
				EffectivePolicy: requestedPolicy,
				Attempts:        1,
				Groups:          groupingGroupsFromFindingGroups(groups),
			}, groups, writeCanonicalGroupingArtifacts(iterationDir, round, localWorkGroupingResult{
				RequestedPolicy: requestedPolicy,
				EffectivePolicy: requestedPolicy,
				Attempts:        1,
				Groups:          groupingGroupsFromFindingGroups(groups),
			})
	case localWorkSingletonPolicy:
		groups := groupFindingsAsSingletons(findings)
		return localWorkGroupingResult{
				RequestedPolicy: requestedPolicy,
				EffectivePolicy: requestedPolicy,
				Attempts:        1,
				Groups:          groupingGroupsFromFindingGroups(groups),
			}, groups, writeCanonicalGroupingArtifacts(iterationDir, round, localWorkGroupingResult{
				RequestedPolicy: requestedPolicy,
				EffectivePolicy: requestedPolicy,
				Attempts:        1,
				Groups:          groupingGroupsFromFindingGroups(groups),
			})
	default:
		return groupFindingsByAI(manifest, codexArgs, iterationDir, round, findings)
	}
}

func writeCanonicalGroupingArtifacts(iterationDir string, round int, result localWorkGroupingResult) error {
	prefix := "grouping-initial"
	if round > 0 {
		prefix = fmt.Sprintf("grouping-round-%d", round)
	}
	return writeJSONArtifact(filepath.Join(iterationDir, prefix+"-result.json"), result)
}

func groupFindingsByAI(manifest *localWorkManifest, codexArgs []string, iterationDir string, round int, findings []githubPullReviewFinding) (localWorkGroupingResult, []localWorkFindingGroup, error) {
	requestedPolicy := localWorkDefaultGroupingPolicy
	prefix := "grouping-initial"
	alias := "grouper-initial"
	if round > 0 {
		prefix = fmt.Sprintf("grouping-round-%d", round)
		alias = fmt.Sprintf("grouper-round-%d", round)
	}
	var lastErr error
	for attempt := 1; attempt <= localWorkMaxGroupingAttempts; attempt++ {
		prompt, err := buildLocalWorkGroupingPrompt(*manifest, findings)
		if err != nil {
			return localWorkGroupingResult{}, nil, err
		}
		attemptBase := filepath.Join(iterationDir, fmt.Sprintf("%s-attempt-%d", prefix, attempt))
		if err := os.WriteFile(attemptBase+"-prompt.md", []byte(prompt), 0o644); err != nil {
			return localWorkGroupingResult{}, nil, err
		}
		result, err := runLocalWorkCodexPrompt(*manifest, codexArgs, prompt, alias, attemptBase+"-checkpoint.json")
		if err := os.WriteFile(attemptBase+"-stdout.log", []byte(result.Stdout), 0o644); err != nil {
			return localWorkGroupingResult{}, nil, err
		}
		if err := os.WriteFile(attemptBase+"-stderr.log", []byte(result.Stderr), 0o644); err != nil {
			return localWorkGroupingResult{}, nil, err
		}
		if err != nil {
			lastErr = err
			continue
		}
		groupingResult, err := parseLocalWorkGroupingResult(result.Stdout)
		if err != nil {
			lastErr = err
			continue
		}
		groupingResult.RequestedPolicy = requestedPolicy
		groupingResult.EffectivePolicy = localWorkDefaultGroupingPolicy
		groupingResult.Attempts = attempt
		groups, err := buildFindingGroupsFromGroupingResult(findings, groupingResult)
		if err != nil {
			lastErr = err
			continue
		}
		if err := writeCanonicalGroupingArtifacts(iterationDir, round, groupingResult); err != nil {
			return localWorkGroupingResult{}, nil, err
		}
		return groupingResult, groups, nil
	}
	fallbackGroups := groupFindingsAsSingletons(findings)
	groupingResult := localWorkGroupingResult{
		RequestedPolicy: requestedPolicy,
		EffectivePolicy: localWorkSingletonPolicy,
		FallbackReason:  defaultString(errorString(lastErr), "invalid AI grouping output"),
		Attempts:        localWorkMaxGroupingAttempts,
		Groups:          groupingGroupsFromFindingGroups(fallbackGroups),
	}
	if err := writeCanonicalGroupingArtifacts(iterationDir, round, groupingResult); err != nil {
		return localWorkGroupingResult{}, nil, err
	}
	return groupingResult, fallbackGroups, nil
}

func validateFindingGroups(manifest localWorkManifest, codexArgs []string, iterationDir string, round int, groups []localWorkFindingGroup, context *localWorkValidationContextState, persist func() error) ([]localWorkValidatedFinding, []string, int, error) {
	if len(groups) == 0 {
		return nil, nil, 0, nil
	}

	type groupOutcome struct {
		groupID   string
		attempts  int
		validated []localWorkValidatedFinding
		rejected  []string
		lastError string
		err       error
	}

	parallelism := manifest.ValidationParallelism
	if parallelism <= 0 {
		parallelism = localWorkValidationParallelism
	}
	sem := make(chan struct{}, parallelism)
	results := make(chan groupOutcome, len(groups))
	progress := make(chan localWorkValidationGroupProgress, len(groups)*(localWorkMaxValidatorAttempts+2))
	var wg sync.WaitGroup

	for _, group := range groups {
		state := findValidationGroupState(context, group.GroupID)
		if state == nil {
			continue
		}
		state.Rationale = group.Rationale
		if state.Status == "completed" && strings.TrimSpace(state.ResultPath) != "" {
			continue
		}
		state.Status = "queued"
		state.Attempts = 0
		state.LastError = ""
	}
	if persist != nil {
		if err := persist(); err != nil {
			return nil, nil, 0, err
		}
	}

	for _, group := range groups {
		group := group
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			progress <- localWorkValidationGroupProgress{
				GroupID:   group.GroupID,
				Rationale: group.Rationale,
				Status:    "running",
			}
			validated, rejected, attempts, err := validateFindingGroup(manifest, codexArgs, iterationDir, round, group, progress)
			results <- groupOutcome{
				groupID:   group.GroupID,
				attempts:  attempts,
				validated: validated,
				rejected:  rejected,
				lastError: errorString(err),
				err:       err,
			}
		}()
	}

	outcomes := make([]groupOutcome, 0, len(groups))
	go func() {
		wg.Wait()
		close(progress)
		close(results)
	}()

	for progress != nil || results != nil {
		select {
		case update, ok := <-progress:
			if !ok {
				progress = nil
				continue
			}
			applyValidationGroupProgress(context, update)
			if persist != nil {
				if err := persist(); err != nil {
					return nil, nil, 0, err
				}
			}
		case outcome, ok := <-results:
			if !ok {
				results = nil
				continue
			}
			if outcome.err != nil {
				applyValidationGroupProgress(context, localWorkValidationGroupProgress{
					GroupID:   outcome.groupID,
					Status:    "failed",
					Attempts:  outcome.attempts,
					LastError: outcome.lastError,
				})
				if persist != nil {
					if err := persist(); err != nil {
						return nil, nil, 0, err
					}
				}
				return nil, nil, 0, outcome.err
			}
			outcomes = append(outcomes, outcome)
		}
	}
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].groupID < outcomes[j].groupID })

	validated := []localWorkValidatedFinding{}
	rejected := []string{}
	totalAttempts := 0
	for _, outcome := range outcomes {
		validated = append(validated, outcome.validated...)
		rejected = append(rejected, outcome.rejected...)
		totalAttempts += outcome.attempts
	}
	return validated, uniqueStrings(rejected), totalAttempts, nil
}

func validateFindingGroup(manifest localWorkManifest, codexArgs []string, iterationDir string, round int, group localWorkFindingGroup, progress chan<- localWorkValidationGroupProgress) ([]localWorkValidatedFinding, []string, int, error) {
	groupDir := filepath.Join(iterationDir, "validation-groups", sanitizePathToken(group.GroupID))
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return nil, nil, 0, err
	}
	resultPath := filepath.Join(groupDir, fmt.Sprintf("round-%d-validator-result.json", round))
	if _, err := os.Stat(resultPath); err == nil {
		var groupResult localWorkValidationGroupResult
		if err := readGithubJSON(resultPath, &groupResult); err == nil {
			validated, rejected := validatedFindingsFromGroupDecision(group, groupResult)
			progress <- localWorkValidationGroupProgress{
				GroupID:    group.GroupID,
				Rationale:  group.Rationale,
				Status:     "completed",
				ResultPath: resultPath,
			}
			return validated, rejected, 0, nil
		}
	}
	var lastErr error
	for attempt := 1; attempt <= localWorkMaxValidatorAttempts; attempt++ {
		prompt, err := buildLocalWorkValidationPrompt(manifest, group)
		if err != nil {
			return nil, nil, attempt - 1, err
		}
		attemptBase := filepath.Join(groupDir, fmt.Sprintf("round-%d-validator-attempt-%d", round, attempt))
		if err := os.WriteFile(attemptBase+"-prompt.md", []byte(prompt), 0o644); err != nil {
			return nil, nil, attempt - 1, err
		}
		result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, fmt.Sprintf("validator-%s-round-%d", sanitizePathToken(group.GroupID), round), attemptBase+"-checkpoint.json")
		if err := os.WriteFile(attemptBase+"-stdout.log", []byte(result.Stdout), 0o644); err != nil {
			return nil, nil, attempt - 1, err
		}
		if err := os.WriteFile(attemptBase+"-stderr.log", []byte(result.Stderr), 0o644); err != nil {
			return nil, nil, attempt - 1, err
		}
		if err != nil {
			lastErr = err
			progress <- localWorkValidationGroupProgress{
				GroupID:   group.GroupID,
				Rationale: group.Rationale,
				Status:    "retrying",
				Attempts:  attempt,
				LastError: err.Error(),
			}
			continue
		}
		groupResult, err := parseLocalWorkValidationGroupResult(result.Stdout, group)
		if err != nil {
			lastErr = err
			progress <- localWorkValidationGroupProgress{
				GroupID:   group.GroupID,
				Rationale: group.Rationale,
				Status:    "retrying",
				Attempts:  attempt,
				LastError: err.Error(),
			}
			continue
		}
		if err := writeJSONArtifact(resultPath, groupResult); err != nil {
			return nil, nil, attempt - 1, err
		}
		progress <- localWorkValidationGroupProgress{
			GroupID:    group.GroupID,
			Rationale:  group.Rationale,
			Status:     "completed",
			Attempts:   attempt,
			ResultPath: resultPath,
		}
		validated, rejected := validatedFindingsFromGroupDecision(group, groupResult)
		return validated, uniqueStrings(rejected), attempt, nil
	}
	progress <- localWorkValidationGroupProgress{
		GroupID:   group.GroupID,
		Rationale: group.Rationale,
		Status:    "failed",
		Attempts:  localWorkMaxValidatorAttempts,
		LastError: errorString(lastErr),
	}
	return nil, nil, localWorkMaxValidatorAttempts, fmt.Errorf("validator group %s failed after %d attempt(s): %w", group.GroupID, localWorkMaxValidatorAttempts, lastErr)
}

func applyValidationGroupProgress(context *localWorkValidationContextState, update localWorkValidationGroupProgress) {
	state := findValidationGroupState(context, update.GroupID)
	if state == nil {
		return
	}
	if strings.TrimSpace(update.Rationale) != "" {
		state.Rationale = update.Rationale
	}
	if strings.TrimSpace(update.Status) != "" {
		state.Status = update.Status
	}
	if update.Attempts > 0 || update.Status == "running" || update.Status == "queued" {
		state.Attempts = update.Attempts
	}
	if strings.TrimSpace(update.ResultPath) != "" {
		state.ResultPath = update.ResultPath
	}
	if update.Status == "completed" {
		state.LastError = ""
	} else if strings.TrimSpace(update.LastError) != "" {
		state.LastError = update.LastError
	}
}

func validatedFindingsFromGroupDecision(group localWorkFindingGroup, groupResult localWorkValidationGroupResult) ([]localWorkValidatedFinding, []string) {
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
				GroupRationale:      group.Rationale,
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
				GroupRationale:      group.Rationale,
				OriginalFingerprint: finding.Fingerprint,
				CurrentFingerprint:  finding.Fingerprint,
				Status:              localWorkFindingRejected,
				Reason:              strings.TrimSpace(decision.Reason),
			})
			rejected = append(rejected, finding.Fingerprint)
		case localWorkFindingPreexisting:
			validated = append(validated, localWorkValidatedFinding{
				GroupID:             group.GroupID,
				GroupRationale:      group.Rationale,
				OriginalFingerprint: finding.Fingerprint,
				CurrentFingerprint:  finding.Fingerprint,
				Status:              localWorkFindingPreexisting,
				Reason:              strings.TrimSpace(decision.Reason),
				Finding:             &finding,
			})
		case localWorkFindingModified:
			replacement := cloneFindingOrOriginal(decision.Replacement, finding)
			replacementFingerprint := buildGithubPullReviewFindingFingerprint(replacement.Title, replacement.Path, replacement.Line, replacement.Summary)
			replacement.Fingerprint = replacementFingerprint
			validated = append(validated,
				localWorkValidatedFinding{
					GroupID:               group.GroupID,
					GroupRationale:        group.Rationale,
					OriginalFingerprint:   finding.Fingerprint,
					CurrentFingerprint:    finding.Fingerprint,
					Status:                localWorkFindingSuperseded,
					Reason:                strings.TrimSpace(decision.Reason),
					SupersedesFingerprint: replacementFingerprint,
					Finding:               &finding,
				},
				localWorkValidatedFinding{
					GroupID:               group.GroupID,
					GroupRationale:        group.Rationale,
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
				GroupRationale:      group.Rationale,
				OriginalFingerprint: finding.Fingerprint,
				CurrentFingerprint:  finding.Fingerprint,
				Status:              localWorkFindingConfirmed,
				Reason:              strings.TrimSpace(decision.Reason),
				Finding:             &finding,
			})
		}
	}
	return validated, rejected
}

func findValidationGroupState(context *localWorkValidationContextState, groupID string) *localWorkRuntimeGroupState {
	if context == nil {
		return nil
	}
	for i := range context.GroupStates {
		if context.GroupStates[i].GroupID == groupID {
			return &context.GroupStates[i]
		}
	}
	context.GroupStates = append(context.GroupStates, localWorkRuntimeGroupState{GroupID: groupID})
	return &context.GroupStates[len(context.GroupStates)-1]
}

func groupingGroupsFromFindingGroups(groups []localWorkFindingGroup) []localWorkGroupingGroup {
	out := make([]localWorkGroupingGroup, 0, len(groups))
	for _, group := range groups {
		fingerprints := make([]string, 0, len(group.Findings))
		for _, finding := range group.Findings {
			fingerprints = append(fingerprints, finding.Fingerprint)
		}
		out = append(out, localWorkGroupingGroup{
			GroupID:   group.GroupID,
			Rationale: group.Rationale,
			Findings:  fingerprints,
		})
	}
	return out
}

func groupFindingsAsSingletons(findings []githubPullReviewFinding) []localWorkFindingGroup {
	out := make([]localWorkFindingGroup, 0, len(findings))
	findings = append([]githubPullReviewFinding{}, findings...)
	sort.Slice(findings, func(i, j int) bool { return findings[i].Fingerprint < findings[j].Fingerprint })
	for _, finding := range findings {
		label := defaultString(strings.TrimSpace(finding.Title), finding.Fingerprint)
		out = append(out, localWorkFindingGroup{
			GroupID:   sanitizePathToken(label) + "-" + shortHash(finding.Fingerprint),
			Rationale: "singleton fallback grouping",
			Findings:  []githubPullReviewFinding{finding},
		})
	}
	return out
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
			fmt.Sprintf("  summary: %s", promptSnippetLimit(finding.Summary, 500)),
			fmt.Sprintf("  detail: %s", promptSnippetLimit(finding.Detail, 700)),
		)
	}
	return capPromptSize(strings.Join(lines, "\n")+"\n", localWorkGroupingPromptLimit), nil
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
			GroupID:   sanitizePathToken(groupID),
			Rationale: strings.TrimSpace(group.Rationale),
			Findings:  items,
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
	baseLines := []string{
		"# NANA Work-local Finding Validation",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Sandbox repo path: %s", manifest.SandboxRepoPath),
		fmt.Sprintf("Group: %s", group.GroupID),
		fmt.Sprintf("Group rationale: %s", defaultString(strings.TrimSpace(group.Rationale), "(none provided)")),
		"",
		"Decide each finding as one of: confirmed, rejected, modified, preexisting.",
		"- confirmed: the finding is valid as written",
		"- rejected: the finding should be dropped from later validation and hardening in this run",
		"- modified: the finding is directionally valid but must be rewritten into a better scoped or more accurate replacement",
		"- preexisting: the issue clearly existed in the baseline before this run and should be remembered, reported, and excluded from hardening/propagation",
		"",
		"Return JSON only.",
		`Schema: {"group":"...","decisions":[{"fingerprint":"...","status":"confirmed|rejected|modified|preexisting","reason":"...","replacement":{"title":"...","severity":"low|medium|high|critical","path":"...","line":123,"summary":"...","detail":"...","fix":"...","rationale":"..."}}]}`,
		"If baseline evidence shows the issue already existed before this run's changes, choose preexisting.",
		"If you are unsure, prefer confirmed over rejected or preexisting.",
		"",
		"Findings:",
	}
	seenSnippets := map[string]bool{}
	optionalLines := []string{}
	for _, finding := range group.Findings {
		baseLines = append(baseLines,
			fmt.Sprintf("- fingerprint: %s", finding.Fingerprint),
			fmt.Sprintf("  path: %s", finding.Path),
			fmt.Sprintf("  line: %d", finding.Line),
			fmt.Sprintf("  severity: %s", finding.Severity),
			fmt.Sprintf("  title: %s", finding.Title),
			fmt.Sprintf("  summary: %s", promptSnippetLimit(finding.Summary, 600)),
			fmt.Sprintf("  detail: %s", promptSnippetLimit(finding.Detail, 800)),
		)
		if strings.TrimSpace(finding.Fix) != "" {
			baseLines = append(baseLines, fmt.Sprintf("  fix: %s", promptSnippetLimit(finding.Fix, 600)))
		}
		if !seenSnippets[finding.Path] {
			seenSnippets[finding.Path] = true
			if snippet := localWorkFindingSnippet(manifest.SandboxRepoPath, finding); strings.TrimSpace(snippet) != "" {
				optionalLines = append(optionalLines, fmt.Sprintf("  code: %s", promptSnippetLimit(snippet, 1200)))
			}
			if baseline := localWorkFindingBaselineSnippet(manifest, finding); strings.TrimSpace(baseline) != "" {
				optionalLines = append(optionalLines, fmt.Sprintf("  baseline code: %s", promptSnippetLimit(baseline, 1200)))
			}
			if diff := localWorkFindingDiffSnippet(manifest, finding.Path); strings.TrimSpace(diff) != "" {
				optionalLines = append(optionalLines, fmt.Sprintf("  diff: %s", promptSnippetLimit(diff, 1800)))
			}
		}
	}
	prompt := strings.Join(baseLines, "\n") + "\n"
	omittedOptional := false
	for _, optionalLine := range optionalLines {
		candidate := prompt + optionalLine + "\n"
		if len(candidate) > localWorkValidationPromptLimit {
			omittedOptional = true
			continue
		}
		prompt = candidate
	}
	if omittedOptional {
		notice := "[Optional code snippets omitted to fit runtime limits]\n"
		if len(prompt)+len(notice) <= localWorkValidationPromptLimit {
			prompt += notice
		}
	}
	return capPromptSize(prompt, localWorkValidationPromptLimit), nil
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
		case localWorkFindingRejected, localWorkFindingModified, localWorkFindingConfirmed, localWorkFindingPreexisting:
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

type localWorkKnownFindingFilterResult struct {
	Findings           []githubPullReviewFinding
	SkippedRejected    int
	SkippedPreexisting int
}

func filterKnownFindings(findings []githubPullReviewFinding, rejectedFingerprints []string, preexistingFingerprints []string) localWorkKnownFindingFilterResult {
	result := localWorkKnownFindingFilterResult{Findings: findings}
	if len(findings) == 0 {
		return result
	}
	rejected := map[string]bool{}
	for _, fingerprint := range rejectedFingerprints {
		rejected[fingerprint] = true
	}
	preexisting := map[string]bool{}
	for _, fingerprint := range preexistingFingerprints {
		preexisting[fingerprint] = true
	}
	if len(rejected) == 0 && len(preexisting) == 0 {
		return result
	}
	filtered := make([]githubPullReviewFinding, 0, len(findings))
	for _, finding := range findings {
		switch {
		case preexisting[finding.Fingerprint]:
			result.SkippedPreexisting++
		case rejected[finding.Fingerprint]:
			result.SkippedRejected++
		default:
			filtered = append(filtered, finding)
		}
	}
	result.Findings = filtered
	return result
}

func rememberedFindingsFromValidated(validated []localWorkValidatedFinding, status localWorkFindingDecisionStatus) []localWorkRememberedFinding {
	out := []localWorkRememberedFinding{}
	seen := map[string]bool{}
	for _, item := range validated {
		if item.Status != status || item.Finding == nil {
			continue
		}
		if seen[item.CurrentFingerprint] {
			continue
		}
		seen[item.CurrentFingerprint] = true
		out = append(out, localWorkRememberedFinding{
			Fingerprint: item.CurrentFingerprint,
			Title:       item.Finding.Title,
			Path:        item.Finding.Path,
			Line:        item.Finding.Line,
			Summary:     item.Finding.Summary,
			Detail:      item.Finding.Detail,
			Reason:      item.Reason,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

func rememberedFindingFingerprints(findings []localWorkRememberedFinding) []string {
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		if strings.TrimSpace(finding.Fingerprint) == "" {
			continue
		}
		out = append(out, finding.Fingerprint)
	}
	return out
}

func mergeRememberedFindings(existing []localWorkRememberedFinding, additions []localWorkRememberedFinding) []localWorkRememberedFinding {
	if len(additions) == 0 {
		return existing
	}
	merged := append([]localWorkRememberedFinding{}, existing...)
	seen := map[string]bool{}
	for _, finding := range merged {
		seen[finding.Fingerprint] = true
	}
	for _, finding := range additions {
		if strings.TrimSpace(finding.Fingerprint) == "" || seen[finding.Fingerprint] {
			continue
		}
		seen[finding.Fingerprint] = true
		merged = append(merged, finding)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Fingerprint < merged[j].Fingerprint })
	return merged
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
		out = append(out, localWorkFindingGroup{GroupID: groupID, Rationale: "shared path/module context", Findings: findings})
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
	if err := os.WriteFile(path, mustMarshalJSON(value), 0o644); err != nil {
		return err
	}
	recordRuntimeArtifactWrite(path)
	return nil
}

func writeLocalWorkJSONAtomically(path string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := mustMarshalJSON(value)
	tempPath := path + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(tempPath, content, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func readLocalWorkRuntimeState(runID string, iteration int) (localWorkIterationRuntimeState, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (localWorkIterationRuntimeState, error) {
		row := store.db.QueryRow(`SELECT state_json FROM runtime_states WHERE run_id = ? AND iteration = ?`, runID, iteration)
		var raw string
		if err := row.Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return localWorkIterationRuntimeState{}, os.ErrNotExist
			}
			return localWorkIterationRuntimeState{}, err
		}
		var state localWorkIterationRuntimeState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return localWorkIterationRuntimeState{}, err
		}
		if state.Version == 0 {
			state.Version = 1
		}
		return state, nil
	})
}

func writeLocalWorkRuntimeState(runID string, state localWorkIterationRuntimeState) error {
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		if state.Version == 0 {
			state.Version = 1
		}
		content, err := json.Marshal(state)
		if err != nil {
			return err
		}
		_, err = store.db.Exec(
			`INSERT INTO runtime_states(run_id, iteration, state_json)
			 VALUES(?, ?, ?)
			 ON CONFLICT(run_id, iteration) DO UPDATE SET state_json=excluded.state_json`,
			runID, state.Iteration, string(content),
		)
		return err
	})
}

func removeLocalWorkRuntimeState(runID string, iteration int) error {
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		_, err := store.db.Exec(`DELETE FROM runtime_states WHERE run_id = ? AND iteration = ?`, runID, iteration)
		return err
	})
}

func appendLocalWorkFindingHistory(runID string, events []localWorkFindingHistoryEvent) error {
	if len(events) == 0 {
		return nil
	}
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		tx, err := store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, event := range events {
			content, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO finding_history(run_id, event_json) VALUES(?, ?)`, runID, string(content)); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func persistUnexpectedLocalWorkFailure(runID string, cause error) error {
	if cause == nil {
		return nil
	}
	manifest, err := readLocalWorkManifestByRunID(runID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(manifest.Status), "running") {
		return nil
	}
	now := ISOTimeNow()
	manifest.Status = "failed"
	manifest.LastError = strings.TrimSpace(cause.Error())
	manifest.CompletedAt = defaultString(strings.TrimSpace(manifest.CompletedAt), now)
	setLocalWorkProgress(&manifest, nil, "failed", "failed", manifest.CurrentRound)
	manifest.UpdatedAt = now
	return writeLocalWorkManifest(manifest)
}

func findLocalWorkValidationContext(state *localWorkIterationRuntimeState, name string, round int) *localWorkValidationContextState {
	for i := range state.ValidationContexts {
		if state.ValidationContexts[i].Name == name && state.ValidationContexts[i].Round == round {
			return &state.ValidationContexts[i]
		}
	}
	state.ValidationContexts = append(state.ValidationContexts, localWorkValidationContextState{Name: name, Round: round})
	return &state.ValidationContexts[len(state.ValidationContexts)-1]
}

func findLocalWorkRoundState(state *localWorkIterationRuntimeState, round int) *localWorkRoundRuntimeState {
	for i := range state.Rounds {
		if state.Rounds[i].Round == round {
			return &state.Rounds[i]
		}
	}
	state.Rounds = append(state.Rounds, localWorkRoundRuntimeState{Round: round})
	return &state.Rounds[len(state.Rounds)-1]
}

func setLocalWorkProgress(manifest *localWorkManifest, state *localWorkIterationRuntimeState, phase string, subphase string, round int) {
	manifest.CurrentPhase = phase
	manifest.CurrentSubphase = subphase
	manifest.CurrentRound = round
	manifest.UpdatedAt = ISOTimeNow()
	if state != nil {
		state.CurrentPhase = phase
		state.CurrentSubphase = subphase
		state.CurrentRound = round
	}
}

func writeLocalWorkActiveState(runDir string, manifest *localWorkManifest, state *localWorkIterationRuntimeState) error {
	if manifest == nil {
		return nil
	}
	if state != nil {
		if state.Iteration == 0 {
			state.Iteration = manifest.CurrentIteration
		}
		if manifest.CurrentIteration == 0 {
			manifest.CurrentIteration = state.Iteration
		}
	}
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.writeActiveState(*manifest, state)
	})
}

func latestCompletedLocalWorkIteration(manifest localWorkManifest) int {
	if len(manifest.Iterations) == 0 {
		return 0
	}
	return manifest.Iterations[len(manifest.Iterations)-1].Iteration
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
	return localWorkSnippetFromContent(string(content), finding.Line)
}

func localWorkFindingBaselineSnippet(manifest localWorkManifest, finding githubPullReviewFinding) string {
	path := strings.TrimSpace(strings.TrimPrefix(finding.Path, "/"))
	if path == "" || strings.TrimSpace(manifest.BaselineSHA) == "" {
		return ""
	}
	content, err := githubGitOutput(manifest.SandboxRepoPath, "show", fmt.Sprintf("%s:%s", manifest.BaselineSHA, path))
	if err != nil {
		return ""
	}
	return localWorkSnippetFromContent(content, finding.Line)
}

func localWorkSnippetFromContent(content string, line int) string {
	lines := strings.Split(content, "\n")
	if line <= 0 || line > len(lines) {
		return strings.Join(lines[:minInt(len(lines), localWorkPromptSnippetLines)], "\n")
	}
	start := line - 3
	if start < 0 {
		start = 0
	}
	end := line + 2
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
		fmt.Sprintf("Work type: %s", workTypeDisplayName(manifest.WorkType)),
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
		if len(last.ApprovedFollowupItems) > 0 {
			lines = append(lines, fmt.Sprintf("- Approved followups from round %d: %d", last.FollowupRound, len(last.ApprovedFollowupItems)))
			for _, item := range last.ApprovedFollowupItems {
				lines = append(lines, fmt.Sprintf("- Followup item: [%s] %s", item.Kind, item.Title))
			}
		}
		for _, title := range limitPromptList(last.ReviewFindingTitles, 10) {
			lines = append(lines, "- Review item: "+title)
		}
		if len(last.ReviewFindingTitles) > 10 {
			lines = append(lines, fmt.Sprintf("- ... %d additional review items omitted", len(last.ReviewFindingTitles)-10))
		}
	}
	if promptSurface, err := readGithubEmbeddedPromptSurface("executor"); err == nil && strings.TrimSpace(promptSurface) != "" {
		lines = append(lines, "", compactPromptHeadValue(promptSurface, 0, localWorkPromptSurfaceCharLimit))
	}
	lines = append(lines, "", "Plan:", compactPromptHeadValue(string(inputContent), 0, localWorkPlanPayloadCharLimit))
	return capPromptChars(strings.Join(lines, "\n")+"\n", localWorkImplementPromptCharLimit), nil
}

func buildLocalWorkReviewPrompt(manifest localWorkManifest) (string, error) {
	context, err := buildReviewPromptContext(manifest.SandboxRepoPath, []string{manifest.BaselineSHA}, reviewPromptContextOptions{
		ChangedFilesLimit: reviewPromptChangedFilesLimit,
		MaxHunksPerFile:   reviewPromptMaxHunksPerFile,
		MaxLinesPerFile:   reviewPromptMaxLinesPerFile,
		MaxCharsPerFile:   reviewPromptMaxCharsPerFile,
	})
	if err != nil {
		return "", err
	}
	lines := []string{
		"Review this local implementation and return JSON only.",
		`Schema: {"findings":[{"title":"...","severity":"low|medium|high|critical","path":"...","line":123,"summary":"...","detail":"...","fix":"...","rationale":"..."}]}`,
		"If there are no actionable issues, return {\"findings\":[]}.",
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Baseline SHA: %s", manifest.BaselineSHA),
		fmt.Sprintf("Changed files: %s", context.ChangedFilesText),
	}
	if promptSurface, err := readGithubEmbeddedPromptSurface("critic"); err == nil && strings.TrimSpace(promptSurface) != "" {
		lines = append(lines, "", strings.TrimSpace(promptSurface))
	}
	if context.Shortstat != "" {
		lines = append(lines, "Shortstat:", context.Shortstat)
	}
	lines = append(lines, "Diff summary:", context.DiffSummary)
	return capPromptChars(strings.Join(lines, "\n\n"), reviewPromptLocalCharLimit), nil
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
	if promptSurface, err := readGithubEmbeddedPromptSurface("test-engineer"); err == nil && strings.TrimSpace(promptSurface) != "" {
		lines = append(lines, "", compactPromptHeadValue(promptSurface, 0, localWorkPromptSurfaceCharLimit))
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
				lines = append(lines, fmt.Sprintf("  output: %s", compactPromptValue(command.Output, localWorkPromptSnippetLines, 600)))
			}
			failedCommandCount++
		}
	}
	if len(findings) > 0 {
		lines = append(lines, "", "Review findings:")
		for index, finding := range findings {
			if index >= 6 {
				lines = append(lines, "- additional findings omitted for brevity")
				break
			}
			ref := finding.Path
			if finding.Line > 0 {
				ref = fmt.Sprintf("%s:%d", finding.Path, finding.Line)
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s (%s)", strings.ToUpper(finding.Severity), finding.Title, ref))
			lines = append(lines, "  "+compactPromptValue(finding.Detail, localWorkPromptSnippetLines, 600))
			if strings.TrimSpace(finding.Fix) != "" {
				lines = append(lines, "  Fix hint: "+compactPromptValue(finding.Fix, localWorkPromptSnippetLines, 400))
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
		return "", fmt.Errorf("work requires a git-backed repo: %w", err)
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
	return fmt.Errorf("work requires a clean repo before start; found local changes:\n%s", strings.Join(remaining, "\n"))
}

func inferLocalWorkTaskFromBranch(sourceBranch string) (string, error) {
	branch := strings.TrimSpace(sourceBranch)
	if branch == "" || branch == "HEAD" {
		return "", localWorkTaskInferenceError(branch)
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	branch = strings.TrimPrefix(branch, "origin/")

	genericBranches := map[string]bool{
		"main":        true,
		"master":      true,
		"trunk":       true,
		"develop":     true,
		"development": true,
		"dev":         true,
		"staging":     true,
		"stage":       true,
		"production":  true,
		"prod":        true,
		"release":     true,
		"releases":    true,
	}
	if genericBranches[strings.ToLower(branch)] {
		return "", localWorkTaskInferenceError(branch)
	}

	normalized := strings.NewReplacer("/", " ", "-", " ", "_", " ", ".", " ").Replace(branch)
	words := strings.Fields(normalized)
	prefixes := map[string]bool{
		"feature":  true,
		"feat":     true,
		"bugfix":   true,
		"fix":      true,
		"hotfix":   true,
		"chore":    true,
		"docs":     true,
		"doc":      true,
		"refactor": true,
		"task":     true,
		"issue":    true,
		"pr":       true,
		"wip":      true,
		"work":     true,
	}
	for len(words) > 0 && prefixes[strings.ToLower(words[0])] {
		words = words[1:]
	}
	meaningful := make([]string, 0, len(words))
	for _, word := range words {
		cleaned := strings.Trim(word, " #[](){}")
		if cleaned == "" {
			continue
		}
		if genericBranches[strings.ToLower(cleaned)] {
			continue
		}
		meaningful = append(meaningful, cleaned)
	}
	if len(meaningful) == 0 {
		return "", localWorkTaskInferenceError(branch)
	}
	summary := strings.Join(meaningful, " ")
	return fmt.Sprintf("Continue work on local branch %q. Inferred task from branch name: %s.", branch, summary), nil
}

func localWorkTaskInferenceError(branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = "(unknown)"
	}
	return fmt.Errorf("could not infer a local work task from branch %q; provide --task yourself because inference failed", branch)
}

func readLocalWorkInput(cwd string, options localWorkStartOptions, sourceBranch string) (string, string, error) {
	if strings.TrimSpace(options.Task) != "" {
		return options.Task, "task", nil
	}
	if strings.TrimSpace(options.PlanFile) == "" {
		task, err := inferLocalWorkTaskFromBranch(sourceBranch)
		if err != nil {
			return "", "", err
		}
		return task, "inferred-branch", nil
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

func resolveLocalWorkRepoRootForSelection(cwd string, repoPath string) (string, error) {
	repoRoot, err := resolveLocalWorkRepoRoot(cwd, repoPath)
	if err == nil {
		return repoRoot, nil
	}
	if strings.TrimSpace(repoPath) != "" {
		return "", err
	}
	return "", fmt.Errorf("work repo context is required for --last; use --repo <path>, --run-id <id>, or --global-last")
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
	return workHomeRoot()
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

func localWorkRunsDir(repoRoot string) string {
	return filepath.Join(localWorkRepoDir(repoRoot), "runs")
}

func localWorkRunsDirByID(repoID string) string {
	return filepath.Join(localWorkRepoDirByID(repoID), "runs")
}

func localWorkRunDirByID(repoID string, runID string) string {
	return filepath.Join(localWorkRunsDirByID(repoID), runID)
}

func localWorkIterationDir(runDir string, iteration int) string {
	return filepath.Join(runDir, "iterations", fmt.Sprintf("iter-%02d", iteration))
}

func localWorkSandboxesDir() string {
	return filepath.Join(localWorkHomeRoot(), "sandboxes")
}

func localWorkRepoID(repoRoot string) string {
	base := sanitizePathToken(filepath.Base(repoRoot))
	if base == "" {
		base = "repo"
	}
	return base + "-" + shortHash(filepath.Clean(repoRoot))
}

func persistLocalWorkTokenUsage(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	localWorkTokenUsagePersistMu.Lock()
	defer localWorkTokenUsagePersistMu.Unlock()

	manifest, err := readLocalWorkManifestByRunID(runID)
	if err != nil {
		return err
	}
	totals, err := loadLocalWorkTokenUsageTotalsFromSQLite(runID)
	if err != nil {
		return err
	}
	if totals == nil || totals.SessionsAccounted == 0 {
		return nil
	}
	manifest.TokenUsage = totals
	return writeLocalWorkManifest(manifest)
}

func readLocalWorkThreadUsageArtifact(path string) (*localWorkThreadUsageArtifact, error) {
	var artifact localWorkThreadUsageArtifact
	if err := readGithubJSON(path, &artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
}

func readLocalWorkThreadUsageHistoryArtifact(path string) (*localWorkThreadUsageHistoryArtifact, error) {
	var artifact localWorkThreadUsageHistoryArtifact
	if err := readGithubJSON(path, &artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
}

func readLocalWorkThreadUsageRowsFromRollouts(sessionsRoot string) ([]localWorkThreadUsageRow, error) {
	historyRows, err := usageHistoryRowsFromRollouts(sessionsRoot)
	if err != nil {
		return nil, err
	}
	rows := make([]localWorkThreadUsageRow, 0, len(historyRows))
	for _, row := range historyRows {
		converted, ok := localWorkThreadUsageRowFromHistory(row)
		if ok {
			rows = append(rows, converted)
		}
	}
	return rows, nil
}

func readLocalWorkThreadUsageRow(filePath string) (localWorkThreadUsageRow, bool, error) {
	historyRow, ok, err := readUsageHistoryRow(filePath)
	if err != nil {
		return localWorkThreadUsageRow{}, false, err
	}
	if !ok {
		return localWorkThreadUsageRow{}, false, nil
	}
	row, ok := localWorkThreadUsageRowFromHistory(historyRow)
	if !ok {
		return localWorkThreadUsageRow{}, false, nil
	}
	return row, true, nil
}

func localWorkThreadUsageRowFromHistory(row usageHistoryRow) (localWorkThreadUsageRow, bool) {
	latest, ok := usageHistoryLatestCheckpoint(row)
	if !ok {
		return localWorkThreadUsageRow{}, false
	}
	return localWorkThreadUsageRow{
		SessionID:             strings.TrimSpace(row.SessionID),
		Nickname:              strings.TrimSpace(row.Nickname),
		Role:                  strings.TrimSpace(row.Role),
		Model:                 strings.TrimSpace(row.Model),
		CWD:                   strings.TrimSpace(row.CWD),
		InputTokens:           latest.InputTokens,
		CachedInputTokens:     latest.CachedInputTokens,
		OutputTokens:          latest.OutputTokens,
		ReasoningOutputTokens: latest.ReasoningOutputTokens,
		TotalTokens:           latest.TotalTokens,
		StartedAt:             row.StartedAt,
		UpdatedAt:             row.UpdatedAt,
	}, true
}

func writeLocalWorkManifest(manifest localWorkManifest) error {
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.writeManifest(manifest)
	})
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
		return fmt.Sprintf("work run stalled after iteration %d; diff and failure signals repeated unchanged", current.Iteration)
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

func printLocalWorkRememberedFindings(writer io.Writer, label string, findings []localWorkRememberedFinding) {
	if writer == nil || len(findings) == 0 {
		return
	}
	fmt.Fprintf(writer, "[local] %s: %d\n", label, len(findings))
	for index, finding := range findings {
		if index >= 5 {
			fmt.Fprintf(writer, "[local] ... %d additional pre-existing issue(s) omitted\n", len(findings)-index)
			return
		}
		ref := strings.TrimSpace(finding.Path)
		if finding.Line > 0 {
			ref = fmt.Sprintf("%s:%d", ref, finding.Line)
		}
		if ref == "" {
			ref = finding.Fingerprint
		}
		fmt.Fprintf(writer, "[local] Pre-existing: %s (%s)\n", defaultString(strings.TrimSpace(finding.Title), finding.Fingerprint), ref)
	}
}

func promptSnippet(value string) string {
	return promptSnippetLimit(value, localWorkPromptSnippetChars)
}

func promptSnippetLimit(value string, charLimit int) string {
	return compactPromptValue(value, localWorkPromptSnippetLines, charLimit)
}

func capPromptSize(value string, limit int) string {
	return capPromptChars(value, limit)
}

func tailLines(content string, limit int) string {
	return tailLinesLimited(content, limit)
}

func localWorkFindingDiffSnippet(manifest localWorkManifest, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	output, err := githubGitOutput(manifest.SandboxRepoPath, "diff", manifest.BaselineSHA, "--", path)
	if err != nil {
		return ""
	}
	return output
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
	changedFilesOutput, _ := githubGitOutput(manifest.SandboxRepoPath, "diff", "--name-only", manifest.BaselineSHA)
	changedFiles := collectTrimmedLines(changedFilesOutput)
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	history := []localWorkFindingHistoryEvent{}
	if loaded, err := withLocalWorkReadStore(func(store *localWorkDBStore) ([]localWorkFindingHistoryEvent, error) {
		rows, err := store.db.Query(`SELECT event_json FROM finding_history WHERE run_id = ? ORDER BY id`, manifest.RunID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		history := []localWorkFindingHistoryEvent{}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return nil, err
			}
			var event localWorkFindingHistoryEvent
			if err := json.Unmarshal([]byte(raw), &event); err == nil {
				history = append(history, event)
			}
		}
		return history, rows.Err()
	}); err == nil {
		history = loaded
	}
	lines := []string{
		"# NANA Work-local Retrospective",
		"",
		fmt.Sprintf("- Run id: %s", manifest.RunID),
		fmt.Sprintf("- Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("- Run artifacts: %s", runDir),
		fmt.Sprintf("- Sandbox: %s", manifest.SandboxPath),
		fmt.Sprintf("- Status: %s", manifest.Status),
		fmt.Sprintf("- Iterations: %d/%d", len(manifest.Iterations), manifest.MaxIterations),
		fmt.Sprintf("- Integration policy: %s", manifest.IntegrationPolicy),
		fmt.Sprintf("- Grouping policy: %s", manifest.GroupingPolicy),
		fmt.Sprintf("- Validation parallelism: %d", manifest.ValidationParallelism),
		fmt.Sprintf("- Stored rejected findings: %d", len(manifest.RejectedFindingFingerprints)),
		fmt.Sprintf("- Stored pre-existing findings: %d", len(manifest.PreexistingFindings)),
		fmt.Sprintf("- Final source apply: %s", defaultString(manifest.FinalApplyStatus, "(not attempted)")),
		fmt.Sprintf("- Final source commit: %s", defaultString(manifest.FinalApplyCommitSHA, "(none)")),
		fmt.Sprintf("- Candidate audit: %s", defaultString(manifest.CandidateAuditStatus, "(not attempted)")),
		fmt.Sprintf("- Finding history events: %d", len(history)),
		fmt.Sprintf("- Final diff: %s", defaultString(strings.TrimSpace(diffShortstat), "(no diff)")),
		"",
		"## Changed files",
	}
	if len(changedFiles) == 0 {
		lines = append(lines, "- (none)")
	} else {
		for _, path := range changedFiles {
			lines = append(lines, "- "+path)
		}
	}
	lines = append(lines,
		"",
		"## Verification evidence",
	)
	lines = append(lines, localWorkRetrospectiveVerificationLines(manifest, runDir)...)
	lines = append(lines,
		"",
		"## Simplifications made",
		"- Runtime summary: no explicit simplification notes were recorded by the work runtime; review the changed-files list and implementation logs for any agent-authored simplification narrative.",
		"",
		"## Remaining risks",
	)
	riskLines := localWorkRetrospectiveRiskLines(manifest)
	if len(riskLines) == 0 {
		lines = append(lines, "- No runtime-detected remaining risks.")
	} else {
		lines = append(lines, riskLines...)
	}
	lines = append(lines,
		"",
		"## routing_decision",
		"- mode: work-local",
		"- role_tier: standard executor with runtime verification and final review gates",
		"- trigger: nana work local orchestration selected an executor/reviewer completion loop",
		"- confidence: high",
		"",
		"## Report quality checklist",
		"- [x] Changed files",
		"- [x] Verification evidence",
		"- [x] Simplifications made",
		"- [x] Remaining risks",
		"- [x] routing_decision",
		"",
		"## Iterations",
	)
	for _, iteration := range manifest.Iterations {
		lines = append(lines, fmt.Sprintf("- %d: %s; initial review=%d; validated=%d; confirmed=%d; rejected=%d; preexisting=%d; modified=%d; final review=%d; groups=%d; policy=%s; integration=%t",
			iteration.Iteration,
			iteration.VerificationSummary,
			iteration.InitialReviewFindings,
			iteration.ValidatedFindings,
			iteration.ConfirmedFindings,
			iteration.RejectedFindings,
			iteration.PreexistingFindings,
			iteration.ModifiedFindings,
			iteration.ReviewFindings,
			len(iteration.ValidationGroups),
			defaultString(iteration.EffectiveGroupingPolicy, iteration.RequestedGroupingPolicy),
			iteration.IntegrationRan))
		if len(iteration.ValidationGroupRationales) > 0 {
			lines = append(lines, "  Group rationales:")
			for _, rationale := range iteration.ValidationGroupRationales {
				lines = append(lines, "  - "+rationale)
			}
		}
		if strings.TrimSpace(iteration.GroupingFallbackReason) != "" {
			lines = append(lines, "  - grouping fallback: "+iteration.GroupingFallbackReason)
		}
		if iteration.FinalGateFindings > 0 || len(iteration.FinalGateRoles) > 0 {
			lines = append(lines, fmt.Sprintf("  - final review gate: findings=%d roles=%s", iteration.FinalGateFindings, strings.Join(iteration.FinalGateRoles, ", ")))
		}
	}
	if strings.TrimSpace(manifest.FinalApplyError) != "" {
		lines = append(lines, "", "## Final source apply blocker", "", manifest.FinalApplyError)
	}
	if len(manifest.CandidateBlockedPaths) > 0 {
		lines = append(lines, "", "## Candidate file blocker")
		for _, path := range manifest.CandidateBlockedPaths {
			lines = append(lines, "- "+path)
		}
	}
	if nextAction := localWorkBlockedNextAction(manifest); strings.TrimSpace(nextAction) != "" {
		lines = append(lines, "", "## Next action", "", nextAction)
	}
	if len(manifest.PreexistingFindings) > 0 {
		lines = append(lines, "", "## Pre-existing issues excluded")
		for _, finding := range manifest.PreexistingFindings {
			ref := strings.TrimSpace(finding.Path)
			if finding.Line > 0 {
				ref = fmt.Sprintf("%s:%d", ref, finding.Line)
			}
			entry := fmt.Sprintf("- %s (%s)", defaultString(strings.TrimSpace(finding.Title), finding.Fingerprint), defaultString(ref, finding.Fingerprint))
			if strings.TrimSpace(finding.Reason) != "" {
				entry += ": " + strings.TrimSpace(finding.Reason)
			}
			lines = append(lines, entry)
		}
	}
	if strings.TrimSpace(manifest.LastError) != "" {
		lines = append(lines, "", "## Failure", "- "+manifest.LastError)
		if active := loadLocalWorkActiveValidationContext(manifest.RunID, manifest.CurrentIteration, manifest.CurrentRound); active != nil {
			lines = append(lines, fmt.Sprintf("- validation context: %s", active.Name))
			lines = append(lines, fmt.Sprintf("- effective policy: %s", defaultString(active.EffectivePolicy, active.RequestedPolicy)))
			if strings.TrimSpace(active.FallbackReason) != "" {
				lines = append(lines, "- grouping fallback: "+active.FallbackReason)
			}
			for _, group := range active.GroupStates {
				if group.Status != "failed" {
					continue
				}
				lines = append(lines, fmt.Sprintf("- failing group: %s", group.GroupID))
				if strings.TrimSpace(group.Rationale) != "" {
					lines = append(lines, "- group rationale: "+group.Rationale)
				}
				lines = append(lines, fmt.Sprintf("- attempts exhausted: %d", group.Attempts))
				if strings.TrimSpace(group.LastError) != "" {
					lines = append(lines, "- validator error: "+group.LastError)
				}
				break
			}
		}
	}
	content := strings.Join(lines, "\n") + "\n"
	if issues := lintFinalReportQuality(content, finalReportQualityLintOptions{RequireRoutingDecision: true}); len(issues) > 0 {
		return "", fmt.Errorf("retrospective report quality lint failed: %s", formatFinalReportQualityIssues(issues))
	}
	retrospectivePath := filepath.Join(runDir, "retrospective.md")
	if err := os.WriteFile(retrospectivePath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return content, nil
}

func localWorkRetrospectiveVerificationLines(manifest localWorkManifest, runDir string) []string {
	lines := []string{}
	completedIterations := map[int]bool{}
	for _, iteration := range manifest.Iterations {
		completedIterations[iteration.Iteration] = true
		artifact := localWorkCompletedIterationVerificationArtifact(localWorkIterationDir(runDir, iteration.Iteration), iteration.ReviewRoundsUsed)
		lines = append(lines, fmt.Sprintf("- Iteration %d: %s; artifact=%s", iteration.Iteration, defaultString(iteration.VerificationSummary, "(none)"), defaultString(artifact, "(not found)")))
		if len(iteration.VerificationFailedStages) > 0 {
			lines = append(lines, "  - failed stages: "+strings.Join(iteration.VerificationFailedStages, ", "))
		}
	}
	for _, evidence := range localWorkRetrospectiveArtifactVerificationEvidence(manifest, runDir, completedIterations) {
		lines = append(lines, fmt.Sprintf("- Iteration %d: %s; artifact=%s", evidence.Iteration, evidence.Summary, evidence.Artifact))
		if len(evidence.FailedStages) > 0 {
			lines = append(lines, "  - failed stages: "+strings.Join(evidence.FailedStages, ", "))
		}
	}
	if len(lines) == 0 {
		return []string{"- (not run)"}
	}
	return lines
}

func localWorkCompletedIterationVerificationArtifact(iterationDir string, reviewRoundsUsed int) string {
	preferred := filepath.Join(iterationDir, fmt.Sprintf("verification-round-%d-post-hardening.json", reviewRoundsUsed))
	if fileExists(preferred) {
		return preferred
	}
	initial := filepath.Join(iterationDir, "verification.json")
	latestRound := localWorkLatestVerificationRoundArtifact(iterationDir)
	if reviewRoundsUsed <= 0 {
		if fileExists(initial) {
			return initial
		}
		return latestRound
	}
	if latestRound != "" {
		return latestRound
	}
	if fileExists(initial) {
		return initial
	}
	return ""
}

func localWorkLatestVerificationRoundArtifact(iterationDir string) string {
	rounds, _ := filepath.Glob(filepath.Join(iterationDir, "verification-round-*-post-hardening.json"))
	if len(rounds) == 0 {
		return ""
	}
	sort.SliceStable(rounds, func(i, j int) bool {
		left := localWorkVerificationRoundArtifactNumber(rounds[i])
		right := localWorkVerificationRoundArtifactNumber(rounds[j])
		if left == right {
			return rounds[i] < rounds[j]
		}
		return left < right
	})
	return rounds[len(rounds)-1]
}

func localWorkVerificationRoundArtifactNumber(path string) int {
	name := filepath.Base(path)
	name = strings.TrimPrefix(name, "verification-round-")
	name = strings.TrimSuffix(name, "-post-hardening.json")
	round, err := strconv.Atoi(name)
	if err != nil {
		return -1
	}
	return round
}

type localWorkRetrospectiveVerificationEvidence struct {
	Iteration    int
	Summary      string
	FailedStages []string
	Artifact     string
}

func localWorkRetrospectiveArtifactVerificationEvidence(manifest localWorkManifest, runDir string, completedIterations map[int]bool) []localWorkRetrospectiveVerificationEvidence {
	iterationDirs, _ := filepath.Glob(filepath.Join(runDir, "iterations", "iter-*"))
	if len(iterationDirs) == 0 && manifest.CurrentIteration > 0 {
		iterationDirs = append(iterationDirs, localWorkIterationDir(runDir, manifest.CurrentIteration))
	}
	sort.Strings(iterationDirs)
	evidence := []localWorkRetrospectiveVerificationEvidence{}
	for _, iterationDir := range iterationDirs {
		iteration := parseLocalWorkIterationDirNumber(iterationDir)
		if iteration <= 0 || completedIterations[iteration] {
			continue
		}
		for _, artifact := range localWorkVerificationArtifactPaths(iterationDir) {
			report := localWorkVerificationReport{}
			if err := readGithubJSON(artifact, &report); err != nil || localWorkVerificationReportIsEmpty(report) {
				continue
			}
			evidence = append(evidence, localWorkRetrospectiveVerificationEvidence{
				Iteration:    iteration,
				Summary:      summarizeLocalVerification(report),
				FailedStages: append([]string{}, report.FailedStages...),
				Artifact:     artifact,
			})
		}
	}
	return evidence
}

func parseLocalWorkIterationDirNumber(iterationDir string) int {
	base := filepath.Base(iterationDir)
	if !strings.HasPrefix(base, "iter-") {
		return 0
	}
	iteration, err := strconv.Atoi(strings.TrimPrefix(base, "iter-"))
	if err != nil {
		return 0
	}
	return iteration
}

func localWorkVerificationArtifactPaths(iterationDir string) []string {
	paths := []string{}
	initial := filepath.Join(iterationDir, "verification.json")
	if _, err := os.Stat(initial); err == nil {
		paths = append(paths, initial)
	}
	rounds, _ := filepath.Glob(filepath.Join(iterationDir, "verification-round-*-post-hardening.json"))
	sort.Strings(rounds)
	paths = append(paths, rounds...)
	return paths
}

func localWorkVerificationReportIsEmpty(report localWorkVerificationReport) bool {
	return strings.TrimSpace(report.GeneratedAt) == "" &&
		strings.TrimSpace(report.PlanFingerprint) == "" &&
		len(report.FailedStages) == 0 &&
		len(report.Stages) == 0 &&
		!report.IntegrationIncluded &&
		!report.Passed
}

func localWorkRetrospectiveRiskLines(manifest localWorkManifest) []string {
	lines := []string{}
	if strings.TrimSpace(manifest.LastError) != "" {
		lines = append(lines, "- Failure: "+strings.TrimSpace(manifest.LastError))
	}
	if strings.TrimSpace(manifest.FinalApplyError) != "" {
		lines = append(lines, "- Final source apply: "+strings.TrimSpace(manifest.FinalApplyError))
	}
	if strings.TrimSpace(manifest.CandidateAuditStatus) != "" && manifest.CandidateAuditStatus != "passed" && manifest.CandidateAuditStatus != "no-op" {
		lines = append(lines, "- Candidate audit: "+manifest.CandidateAuditStatus)
	}
	if len(manifest.CandidateBlockedPaths) > 0 {
		lines = append(lines, "- Candidate blocked paths: "+strings.Join(manifest.CandidateBlockedPaths, ", "))
	}
	if strings.TrimSpace(manifest.FinalGateStatus) != "" && manifest.FinalGateStatus != "passed" && manifest.FinalGateStatus != "no-op" {
		lines = append(lines, "- Final review gate: "+manifest.FinalGateStatus)
	}
	if len(manifest.PreexistingFindings) > 0 {
		lines = append(lines, fmt.Sprintf("- Pre-existing issues excluded from this run: %d", len(manifest.PreexistingFindings)))
	}
	for _, iteration := range localWorkRetrospectiveRiskIterations(manifest) {
		if len(iteration.VerificationFailedStages) > 0 {
			lines = append(lines, fmt.Sprintf("- Iteration %d verification failed stages: %s", iteration.Iteration, strings.Join(iteration.VerificationFailedStages, ", ")))
		}
		if iteration.ReviewFindings > 0 {
			lines = append(lines, fmt.Sprintf("- Iteration %d remaining review findings: %d", iteration.Iteration, iteration.ReviewFindings))
		}
	}
	return uniqueStrings(lines)
}

func localWorkRetrospectiveRiskIterations(manifest localWorkManifest) []localWorkIterationSummary {
	if len(manifest.Iterations) == 0 || manifest.Status == "completed" {
		return nil
	}
	latest := manifest.Iterations[len(manifest.Iterations)-1]
	if latest.Status == "completed" {
		return nil
	}
	return []localWorkIterationSummary{latest}
}
