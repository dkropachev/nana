package gocli

import (
	"bytes"
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
  nana work start [--repo <path>] [--task <text> | --plan-file <path>] [--max-iterations <n>] [--integration <final|always|never>] [--grouping-policy <ai|path|singleton>] [--validation-parallelism <1-8>] [-- codex-args...]
  nana work resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
  nana work status [--run-id <id> | --last | --global-last] [--repo <path>] [--json]
  nana work logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>] [--json]
  nana work retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
  nana work help

Behavior:
  - runs only against a local git repo in an isolated managed sandbox
  - infers a task from the current branch when --task and --plan-file are omitted
  - commits verified sandbox changes back to the local source branch after completion
  - never submits, publishes, pushes to remotes, or calls GitHub APIs
  - loops through implement -> verify -> self-review -> harden -> re-verify with capped hardening rounds
  - runs lint, compile/build, and unit tests every iteration; integration runs on the final pass by default
  - persists run artifacts under ~/.nana/work/
`

const (
	localWorkDefaultMaxIterations  = 8
	localWorkMaxReviewRounds       = 2
	localWorkRuntimeName           = "work"
	localWorkPromptCharLimit       = 120000
	localWorkGroupingPromptLimit   = 40000
	localWorkValidationPromptLimit = 60000
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
)

var localWorkFinalReviewGateRoles = []string{
	"quality-reviewer",
	"security-reviewer",
	"performance-reviewer",
	"qa-tester",
}

type localWorkStartOptions struct {
	RepoPath              string
	Task                  string
	PlanFile              string
	MaxIterations         int
	IntegrationPolicy     string
	GroupingPolicy        string
	ValidationParallelism int
	CodexArgs             []string
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
	RunSelection localWorkRunSelection
	CodexArgs    []string
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
	RepoID                         string                         `json:"repo_id"`
	SourceBranch                   string                         `json:"source_branch"`
	BaselineSHA                    string                         `json:"baseline_sha"`
	SandboxPath                    string                         `json:"sandbox_path"`
	SandboxRepoPath                string                         `json:"sandbox_repo_path"`
	VerificationPlan               *githubVerificationPlan        `json:"verification_plan,omitempty"`
	VerificationScriptsDir         string                         `json:"verification_scripts_dir,omitempty"`
	InputPath                      string                         `json:"input_path"`
	InputMode                      string                         `json:"input_mode"`
	IntegrationPolicy              string                         `json:"integration_policy"`
	GroupingPolicy                 string                         `json:"grouping_policy,omitempty"`
	ValidationParallelism          int                            `json:"validation_parallelism,omitempty"`
	MaxIterations                  int                            `json:"max_iterations"`
	CurrentRound                   int                            `json:"current_round,omitempty"`
	CurrentSubphase                string                         `json:"current_subphase,omitempty"`
	LastError                      string                         `json:"last_error,omitempty"`
	FinalApplyStatus               string                         `json:"final_apply_status,omitempty"`
	FinalApplyCommitSHA            string                         `json:"final_apply_commit_sha,omitempty"`
	FinalApplyError                string                         `json:"final_apply_error,omitempty"`
	FinalAppliedAt                 string                         `json:"final_applied_at,omitempty"`
	FinalGateStatus                string                         `json:"final_gate_status,omitempty"`
	FinalGateRoleResults           []localWorkFinalGateRoleResult `json:"final_gate_role_results,omitempty"`
	CandidateAuditStatus           string                         `json:"candidate_audit_status,omitempty"`
	CandidateBlockedPaths          []string                       `json:"candidate_blocked_paths,omitempty"`
	RejectedFindingFingerprints    []string                       `json:"rejected_finding_fingerprints,omitempty"`
	PreexistingFindingFingerprints []string                       `json:"preexisting_finding_fingerprints,omitempty"`
	PreexistingFindings            []localWorkRememberedFinding   `json:"preexisting_findings,omitempty"`
	Iterations                     []localWorkIterationSummary    `json:"iterations,omitempty"`
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
	db *sql.DB
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
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, LocalWorkHelp)
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
		options, err := parseLocalWorkStatusArgs(args[1:])
		if err != nil {
			return err
		}
		return localWorkStatus(cwd, options)
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
		return fmt.Errorf("Unknown work subcommand: %s\n\n%s", args[0], LocalWorkHelp)
	}
}

func localWorkDBPath() string {
	return filepath.Join(localWorkHomeRoot(), "state.db")
}

func openLocalWorkDB() (*localWorkDBStore, error) {
	if err := os.MkdirAll(localWorkHomeRoot(), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", localWorkDBPath())
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

func (s *localWorkDBStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *localWorkDBStore) init() error {
	statements := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA foreign_keys=ON;`,
		`PRAGMA busy_timeout=5000;`,
		`PRAGMA synchronous=NORMAL;`,
		`CREATE TABLE IF NOT EXISTS repos (
			repo_id TEXT PRIMARY KEY,
			repo_root TEXT NOT NULL UNIQUE,
			repo_name TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS runs (
			run_id TEXT PRIMARY KEY,
			repo_id TEXT NOT NULL,
			repo_root TEXT NOT NULL,
			repo_name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			completed_at TEXT,
			status TEXT NOT NULL,
			current_phase TEXT,
			current_subphase TEXT,
			current_iteration INTEGER,
			current_round INTEGER,
			sandbox_path TEXT NOT NULL,
			sandbox_repo_path TEXT NOT NULL,
			manifest_json TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_work_local_runs_repo_updated ON runs(repo_id, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_work_local_runs_updated ON runs(updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS runtime_states (
			run_id TEXT NOT NULL,
			iteration INTEGER NOT NULL,
			state_json TEXT NOT NULL,
			PRIMARY KEY(run_id, iteration)
		);`,
		`CREATE TABLE IF NOT EXISTS finding_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			event_json TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS work_run_index (
			run_id TEXT PRIMARY KEY,
			backend TEXT NOT NULL,
			repo_key TEXT,
			repo_root TEXT,
			repo_name TEXT,
			manifest_path TEXT,
			updated_at TEXT NOT NULL,
			target_kind TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_work_run_index_backend_updated ON work_run_index(backend, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_work_run_index_repo_updated ON work_run_index(repo_key, updated_at DESC);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *localWorkDBStore) writeManifest(manifest localWorkManifest) error {
	normalizeLocalWorkManifest(&manifest)
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
	return tx.Commit()
}

func (s *localWorkDBStore) writeActiveState(manifest localWorkManifest, state *localWorkIterationRuntimeState) error {
	normalizeLocalWorkManifest(&manifest)
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
	return tx.Commit()
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

func localWorkRunIndexEntry(manifest localWorkManifest) workRunIndexEntry {
	normalizeLocalWorkManifest(&manifest)
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
		`INSERT INTO work_run_index(run_id, backend, repo_key, repo_root, repo_name, manifest_path, updated_at, target_kind)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
		   backend=excluded.backend,
		   repo_key=excluded.repo_key,
		   repo_root=excluded.repo_root,
		   repo_name=excluded.repo_name,
		   manifest_path=excluded.manifest_path,
		   updated_at=excluded.updated_at,
		   target_kind=excluded.target_kind`,
		entry.RunID,
		entry.Backend,
		nullableString(entry.RepoKey),
		nullableString(entry.RepoRoot),
		nullableString(entry.RepoName),
		nullableString(entry.ManifestPath),
		entry.UpdatedAt,
		nullableString(entry.TargetKind),
	)
	return err
}

func writeWorkRunIndex(entry workRunIndexEntry) error {
	store, err := openLocalWorkDB()
	if err != nil {
		return err
	}
	defer store.Close()
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeWorkRunIndexTx(tx, entry); err != nil {
		return err
	}
	return tx.Commit()
}

func readWorkRunIndex(runID string) (workRunIndexEntry, error) {
	store, err := openLocalWorkDB()
	if err != nil {
		return workRunIndexEntry{}, err
	}
	defer store.Close()
	row := store.db.QueryRow(`SELECT run_id, backend, repo_key, repo_root, repo_name, manifest_path, updated_at, target_kind FROM work_run_index WHERE run_id = ?`, runID)
	return scanWorkRunIndexEntry(row)
}

func latestWorkRunIndex(backend string) (workRunIndexEntry, error) {
	store, err := openLocalWorkDB()
	if err != nil {
		return workRunIndexEntry{}, err
	}
	defer store.Close()
	row := store.db.QueryRow(`SELECT run_id, backend, repo_key, repo_root, repo_name, manifest_path, updated_at, target_kind FROM work_run_index WHERE backend = ? ORDER BY updated_at DESC LIMIT 1`, backend)
	return scanWorkRunIndexEntry(row)
}

func latestAnyWorkRunIndex() (workRunIndexEntry, error) {
	store, err := openLocalWorkDB()
	if err != nil {
		return workRunIndexEntry{}, err
	}
	defer store.Close()
	row := store.db.QueryRow(`SELECT run_id, backend, repo_key, repo_root, repo_name, manifest_path, updated_at, target_kind FROM work_run_index ORDER BY updated_at DESC LIMIT 1`)
	return scanWorkRunIndexEntry(row)
}

type workRunIndexScanner interface {
	Scan(dest ...interface{}) error
}

func scanWorkRunIndexEntry(row workRunIndexScanner) (workRunIndexEntry, error) {
	var entry workRunIndexEntry
	var repoKey, repoRoot, repoName, manifestPath, targetKind sql.NullString
	if err := row.Scan(&entry.RunID, &entry.Backend, &repoKey, &repoRoot, &repoName, &manifestPath, &entry.UpdatedAt, &targetKind); err != nil {
		return workRunIndexEntry{}, err
	}
	entry.RepoKey = repoKey.String
	entry.RepoRoot = repoRoot.String
	entry.RepoName = repoName.String
	entry.ManifestPath = manifestPath.String
	entry.TargetKind = targetKind.String
	return entry, nil
}

func readLocalWorkManifestByRunID(runID string) (localWorkManifest, error) {
	store, err := openLocalWorkDB()
	if err != nil {
		return localWorkManifest{}, err
	}
	defer store.Close()
	return store.readManifest(runID)
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
	store, err := openLocalWorkDB()
	if err != nil {
		return localWorkManifest{}, "", err
	}
	defer store.Close()
	runID, err := store.resolveRunID(cwd, selection)
	if err != nil {
		return localWorkManifest{}, "", err
	}
	manifest, err := store.readManifest(runID)
	if err != nil {
		return localWorkManifest{}, "", err
	}
	return manifest, localWorkRunDirByID(manifest.RepoID, manifest.RunID), nil
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
	repoRoot, err := resolveLocalWorkRepoRoot(cwd, options.RepoPath)
	if err != nil {
		return err
	}
	if err := ensureLocalWorkRepoClean(repoRoot); err != nil {
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
	inputContent, inputMode, err := readLocalWorkInput(cwd, options, sourceBranch)
	if err != nil {
		return err
	}
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
	verificationScriptsDir, err := writeVerificationScripts(localWorkRuntimeName, sandboxPath, sandboxRepoPath, verificationPlan, []string{"nana", "work", "verify-refresh", "--run-id", runID})
	if err != nil {
		return err
	}

	now := ISOTimeNow()
	manifest := localWorkManifest{
		Version:                4,
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
		GroupingPolicy:         options.GroupingPolicy,
		ValidationParallelism:  options.ValidationParallelism,
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
	fmt.Fprintf(os.Stdout, "[local] Validation policy: grouping=%s parallelism=%d\n", options.GroupingPolicy, options.ValidationParallelism)
	for _, warning := range verificationPlan.Warnings {
		fmt.Fprintf(os.Stdout, "[local] Verification warning: %s\n", warning)
	}

	return executeLocalWorkLoop(runID, options.CodexArgs)
}

func resumeLocalWork(cwd string, options localWorkResumeOptions) error {
	manifest, _, err := resolveLocalWorkRun(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	if manifest.Status == "completed" {
		return fmt.Errorf("work run %s is already completed", manifest.RunID)
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
	fmt.Fprintf(os.Stdout, "[local] Resuming run %s for %s\n", manifest.RunID, manifest.RepoRoot)
	return executeLocalWorkLoop(manifest.RunID, options.CodexArgs)
}

func retryBlockedLocalWorkFinalApply(manifest localWorkManifest) error {
	fmt.Fprintf(os.Stdout, "[local] Retrying final source commit for run %s\n", manifest.RunID)
	applyResult := applyLocalWorkFinalDiff(manifest)
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
	if _, err := writeLocalWorkRetrospective(manifest); err != nil {
		return err
	}
	switch applyResult.Status {
	case "committed":
		fmt.Fprintf(os.Stdout, "[local] Completed run %s; committed to source branch at %s.\n", manifest.RunID, applyResult.CommitSHA)
	case "no-op":
		fmt.Fprintf(os.Stdout, "[local] Completed run %s; no source changes to commit.\n", manifest.RunID)
	}
	return nil
}

func executeLocalWorkLoop(runID string, codexArgs []string) error {
	manifest, err := readLocalWorkManifestByRunID(runID)
	if err != nil {
		return err
	}
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
		state, err := readLocalWorkRuntimeState(manifest.RunID, iteration)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			state = localWorkIterationRuntimeState{
				Version:               1,
				Iteration:             iteration,
				GroupingPolicy:        manifest.GroupingPolicy,
				ValidationParallelism: manifest.ValidationParallelism,
			}
		}

		startedAt := ISOTimeNow()
		if len(manifest.Iterations) >= iteration {
			startedAt = manifest.Iterations[iteration-1].StartedAt
		}
		manifest.Status = "running"
		manifest.CurrentIteration = iteration
		manifest.LastError = ""

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
			fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: implementing.\n", iteration, manifest.MaxIterations)
			implementPrompt, err := buildLocalWorkImplementPrompt(manifest, iteration)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(iterationDir, "implement-prompt.md"), []byte(implementPrompt), 0o644); err != nil {
				return err
			}
			implementResult, err := runLocalWorkCodexPrompt(manifest, codexArgs, implementPrompt, "leader")
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
			fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: implementation complete.\n", iteration, manifest.MaxIterations)
		}

		setLocalWorkProgress(&manifest, &state, "verify-refresh", "verify-refresh", 0)
		if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: refreshing verification plan.\n", iteration, manifest.MaxIterations)
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
			fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: running verification.\n", iteration, manifest.MaxIterations)
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
			fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: verification %s.\n", iteration, manifest.MaxIterations, summarizeLocalVerification(initialVerification))
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
			fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: running review.\n", iteration, manifest.MaxIterations)
			reviewPrompt, err := buildLocalWorkReviewPrompt(manifest)
			if err != nil {
				return err
			}
			if err := os.WriteFile(reviewPromptPath, []byte(reviewPrompt), 0o644); err != nil {
				return err
			}
			reviewResult, findings, err := runLocalWorkReview(manifest, codexArgs, reviewPrompt)
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
			fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: review findings=%d.\n", iteration, manifest.MaxIterations, len(initialFindings))
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
					fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: running final review gate (%d roles).\n", iteration, manifest.MaxIterations, len(localWorkFinalReviewGateRoles))
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
					fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: final review gate %s findings=%d.\n", iteration, manifest.MaxIterations, finalGateStatus, gateCount)
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
			fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: validated findings=%d rejected=%d preexisting=%d.\n", iteration, manifest.MaxIterations, len(validatedInitialFindings), len(rejectedInitialFingerprints), len(preexistingInitialFindings))
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
				fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d round %d: hardening %d finding(s).\n", iteration, manifest.MaxIterations, round, len(finalFindings))
				hardeningPrompt, err := buildLocalWorkHardeningPrompt(manifest, finalVerification, finalFindings)
				if err != nil {
					return err
				}
				if err := os.WriteFile(hardeningPromptPath, []byte(hardeningPrompt), 0o644); err != nil {
					return err
				}
				hardeningResult, err := runLocalWorkCodexPrompt(manifest, codexArgs, hardeningPrompt, fmt.Sprintf("hardener-round-%d", round))
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
				fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d round %d: running post-hardening verification.\n", iteration, manifest.MaxIterations, round)
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
				fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d round %d: post-hardening verification %s.\n", iteration, manifest.MaxIterations, round, summarizeLocalVerification(finalVerification))
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
				fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d round %d: running post-hardening review.\n", iteration, manifest.MaxIterations, round)
				finalReviewPrompt, err := buildLocalWorkReviewPrompt(manifest)
				if err != nil {
					return err
				}
				if err := os.WriteFile(postReviewPromptPath, []byte(finalReviewPrompt), 0o644); err != nil {
					return err
				}
				finalReviewResult, findings, err := runLocalWorkReview(manifest, codexArgs, finalReviewPrompt)
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
						fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d round %d: running final review gate (%d roles).\n", iteration, manifest.MaxIterations, round, len(localWorkFinalReviewGateRoles))
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
						fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d round %d: final review gate %s findings=%d.\n", iteration, manifest.MaxIterations, round, finalGateStatus, gateCount)
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

		integrationRan := manifest.IntegrationPolicy == "always" && len(plan.Integration) > 0
		if finalVerification.Passed && len(finalFindings) == 0 && manifest.IntegrationPolicy == "final" {
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
		}
		manifest.FinalGateStatus = finalGateStatus
		manifest.FinalGateRoleResults = finalGateRoleResults
		manifest.CandidateAuditStatus = candidateAuditStatus
		manifest.CandidateBlockedPaths = append([]string{}, candidateBlockedPaths...)
		if finalVerification.Passed && len(finalFindings) == 0 {
			if candidateAuditStatus == "blocked-candidate-files" {
				summary.Status = "blocked"
				manifest.Status = "blocked"
				manifest.LastError = localWorkCandidateBlockedMessage(candidateBlockedPaths)
				setLocalWorkProgress(&manifest, &state, "candidate-blocked", "candidate-audit", roundsUsed)
			} else {
				setLocalWorkProgress(&manifest, &state, "apply", "commit-source", roundsUsed)
				if err := writeLocalWorkActiveState(runDir, &manifest, &state); err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "[local] Iteration %d/%d: applying verified changes to source branch.\n", iteration, manifest.MaxIterations)
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
		if err := removeLocalWorkRuntimeState(manifest.RunID, iteration); err != nil {
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
			printLocalWorkRememberedFindings(os.Stdout, "Pre-existing issues excluded from propagation", manifest.PreexistingFindings)
			switch manifest.FinalApplyStatus {
			case "committed":
				fmt.Fprintf(os.Stdout, "[local] Completed run %s after %d iteration(s); committed to source branch at %s.\n", manifest.RunID, iteration, manifest.FinalApplyCommitSHA)
			case "no-op":
				fmt.Fprintf(os.Stdout, "[local] Completed run %s after %d iteration(s); no source changes to commit.\n", manifest.RunID, iteration)
			default:
				fmt.Fprintf(os.Stdout, "[local] Completed run %s after %d iteration(s).\n", manifest.RunID, iteration)
			}
			return nil
		}
		if summary.Status == "blocked" {
			if _, err := writeLocalWorkRetrospective(manifest); err != nil {
				return err
			}
			blocker := defaultString(manifest.FinalApplyError, manifest.LastError)
			fmt.Fprintf(os.Stdout, "[local] Blocked run %s after %d iteration(s); source commit was not created: %s\n", manifest.RunID, iteration, blocker)
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
	NextAction               string                           `json:"next_action,omitempty"`
	LastError                string                           `json:"last_error,omitempty"`
}

func localWorkStatus(cwd string, options localWorkStatusOptions) error {
	manifest, runDir, err := resolveLocalWorkRun(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	snapshot, err := localWorkBuildStatusSnapshot(manifest, runDir)
	if err != nil {
		return err
	}
	if options.JSON {
		_, err := os.Stdout.Write(mustMarshalJSON(snapshot))
		return err
	}
	fmt.Fprintf(os.Stdout, "[local] Run id: %s\n", snapshot.RunID)
	fmt.Fprintf(os.Stdout, "[local] Repo root: %s\n", snapshot.RepoRoot)
	fmt.Fprintf(os.Stdout, "[local] Run artifacts: %s\n", snapshot.RunArtifacts)
	fmt.Fprintf(os.Stdout, "[local] Sandbox: %s\n", snapshot.Sandbox)
	fmt.Fprintf(os.Stdout, "[local] Status: %s\n", snapshot.Status)
	if strings.TrimSpace(snapshot.FinalApplyStatus) != "" {
		fmt.Fprintf(os.Stdout, "[local] Final apply: %s", snapshot.FinalApplyStatus)
		if strings.TrimSpace(snapshot.FinalApplyCommitSHA) != "" {
			fmt.Fprintf(os.Stdout, " commit=%s", snapshot.FinalApplyCommitSHA)
		}
		if strings.TrimSpace(snapshot.FinalApplyError) != "" {
			fmt.Fprintf(os.Stdout, " error=%s", snapshot.FinalApplyError)
		}
		fmt.Fprintln(os.Stdout)
	}
	if strings.TrimSpace(snapshot.FinalGateStatus) != "" {
		fmt.Fprintf(os.Stdout, "[local] Final gate: %s", snapshot.FinalGateStatus)
		if len(snapshot.FinalGateRoleResults) > 0 {
			parts := make([]string, 0, len(snapshot.FinalGateRoleResults))
			for _, result := range snapshot.FinalGateRoleResults {
				parts = append(parts, fmt.Sprintf("%s=%d", result.Role, result.Findings))
			}
			fmt.Fprintf(os.Stdout, " %s", strings.Join(parts, ","))
		}
		fmt.Fprintln(os.Stdout)
	}
	if strings.TrimSpace(snapshot.CandidateAuditStatus) != "" {
		fmt.Fprintf(os.Stdout, "[local] Candidate audit: %s", snapshot.CandidateAuditStatus)
		if len(snapshot.CandidateBlockedPaths) > 0 {
			fmt.Fprintf(os.Stdout, " blocked=%s", strings.Join(snapshot.CandidateBlockedPaths, ","))
		}
		fmt.Fprintln(os.Stdout)
	}
	if strings.TrimSpace(snapshot.NextAction) != "" {
		fmt.Fprintf(os.Stdout, "[local] Next action: %s\n", snapshot.NextAction)
	}
	fmt.Fprintf(os.Stdout, "[local] Iteration: %d/%d (phase=%s", snapshot.Iteration, snapshot.MaxIterations, defaultString(snapshot.Phase, "n/a"))
	if strings.TrimSpace(snapshot.Subphase) != "" {
		fmt.Fprintf(os.Stdout, ", subphase=%s", snapshot.Subphase)
	}
	if snapshot.Round > 0 {
		fmt.Fprintf(os.Stdout, ", round=%d", snapshot.Round)
	}
	fmt.Fprintln(os.Stdout, ")")
	if snapshot.LastIteration != nil {
		fmt.Fprintf(os.Stdout, "[local] Last verification: %s\n", snapshot.LastVerification)
		fmt.Fprintf(os.Stdout, "[local] Last review findings: %d\n", snapshot.LastReviewFindings)
		fmt.Fprintf(os.Stdout, "[local] Last validation: groups=%d validated=%d confirmed=%d rejected=%d preexisting=%d modified=%d skipped-rejected=%d skipped-preexisting=%d policy=%s",
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
			fmt.Fprintf(os.Stdout, " fallback=%s", snapshot.LastIteration.GroupingFallbackReason)
		}
		fmt.Fprintln(os.Stdout)
	}
	if snapshot.ActiveValidationContext != nil {
		fmt.Fprintf(os.Stdout, "[local] Active validation context: %s", snapshot.ActiveValidationContext.Name)
		if snapshot.ActiveValidationContext.Round > 0 {
			fmt.Fprintf(os.Stdout, " (round=%d)", snapshot.ActiveValidationContext.Round)
		}
		fmt.Fprintf(os.Stdout, " policy=%s", defaultString(snapshot.ActiveValidationContext.EffectivePolicy, snapshot.ActiveValidationContext.RequestedPolicy))
		if strings.TrimSpace(snapshot.ActiveValidationContext.FallbackReason) != "" {
			fmt.Fprintf(os.Stdout, " fallback=%s", snapshot.ActiveValidationContext.FallbackReason)
		}
		fmt.Fprintln(os.Stdout)
		for _, group := range snapshot.ActiveValidationContext.GroupStates {
			if strings.TrimSpace(group.Status) == "" {
				continue
			}
			fmt.Fprintf(os.Stdout, "[local] Validation group: %s status=%s attempts=%d", group.GroupID, group.Status, group.Attempts)
			if strings.TrimSpace(group.Rationale) != "" {
				fmt.Fprintf(os.Stdout, " rationale=%s", group.Rationale)
			}
			if strings.TrimSpace(group.LastError) != "" {
				fmt.Fprintf(os.Stdout, " error=%s", group.LastError)
			}
			fmt.Fprintln(os.Stdout)
		}
	}
	fmt.Fprintf(os.Stdout, "[local] Stored rejected findings: %d\n", snapshot.RejectedFingerprintCount)
	fmt.Fprintf(os.Stdout, "[local] Stored pre-existing findings: %d\n", snapshot.PreexistingFindingCount)
	if strings.TrimSpace(snapshot.LastError) != "" {
		fmt.Fprintf(os.Stdout, "[local] Last error: %s\n", snapshot.LastError)
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
		NextAction:               localWorkBlockedNextAction(manifest),
		LastError:                manifest.LastError,
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
	manifest, runDir, err := resolveLocalWorkRun(cwd, options.RunSelection)
	if err != nil {
		return err
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
		return err
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
		_, err := os.Stdout.Write(mustMarshalJSON(payload))
		return err
	}
	fmt.Fprintf(os.Stdout, "[local] Run id: %s\n", manifest.RunID)
	fmt.Fprintf(os.Stdout, "[local] Iteration: %d\n", iteration)
	fmt.Fprintf(os.Stdout, "[local] Iteration artifacts: %s\n", iterationDir)
	if snapshot.LastIteration != nil {
		fmt.Fprintf(os.Stdout, "[local] Validation summary: groups=%d validated=%d confirmed=%d rejected=%d preexisting=%d modified=%d skipped-rejected=%d skipped-preexisting=%d policy=%s",
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
			fmt.Fprintf(os.Stdout, " fallback=%s", snapshot.LastIteration.GroupingFallbackReason)
		}
		fmt.Fprintln(os.Stdout)
		for _, rationale := range snapshot.LastIteration.ValidationGroupRationales {
			fmt.Fprintf(os.Stdout, "[local] Group: %s\n", rationale)
		}
	}
	if len(grouping.Groups) > 0 {
		fmt.Fprintf(os.Stdout, "[local] Effective grouping: %s\n", defaultString(grouping.EffectivePolicy, grouping.RequestedPolicy))
	}
	if snapshot.ActiveValidationContext != nil {
		fmt.Fprintf(os.Stdout, "[local] Active validation context: %s", snapshot.ActiveValidationContext.Name)
		if snapshot.ActiveValidationContext.Round > 0 {
			fmt.Fprintf(os.Stdout, " (round=%d)", snapshot.ActiveValidationContext.Round)
		}
		fmt.Fprintf(os.Stdout, " policy=%s", defaultString(snapshot.ActiveValidationContext.EffectivePolicy, snapshot.ActiveValidationContext.RequestedPolicy))
		if strings.TrimSpace(snapshot.ActiveValidationContext.FallbackReason) != "" {
			fmt.Fprintf(os.Stdout, " fallback=%s", snapshot.ActiveValidationContext.FallbackReason)
		}
		fmt.Fprintln(os.Stdout)
		for _, group := range snapshot.ActiveValidationContext.GroupStates {
			if strings.TrimSpace(group.Status) == "" {
				continue
			}
			fmt.Fprintf(os.Stdout, "[local] Validation group: %s status=%s attempts=%d", group.GroupID, group.Status, group.Attempts)
			if strings.TrimSpace(group.Rationale) != "" {
				fmt.Fprintf(os.Stdout, " rationale=%s", group.Rationale)
			}
			if strings.TrimSpace(group.LastError) != "" {
				fmt.Fprintf(os.Stdout, " error=%s", group.LastError)
			}
			fmt.Fprintln(os.Stdout)
		}
	}
	for _, entry := range entries {
		fmt.Fprintf(os.Stdout, "\n== %s ==\n", entry["name"])
		if strings.TrimSpace(entry["content"]) == "" {
			fmt.Fprintln(os.Stdout, "(empty)")
			continue
		}
		fmt.Fprint(os.Stdout, entry["content"])
		if !strings.HasSuffix(entry["content"], "\n") {
			fmt.Fprintln(os.Stdout)
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
	fmt.Fprintf(os.Stdout, "[local] Verification artifacts for run %s refreshed.\n", manifest.RunID)
	fmt.Fprintf(os.Stdout, "[local] Verification scripts directory: %s\n", scriptsDir)
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
		manifest.Version = 4
	}
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
	return runLocalWorkReviewWithAlias(manifest, codexArgs, prompt, "reviewer")
}

func runLocalWorkReviewWithAlias(manifest localWorkManifest, codexArgs []string, prompt string, alias string) (localWorkExecutionResult, []githubPullReviewFinding, error) {
	result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, alias)
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
		result, err := runLocalWorkCodexPrompt(*manifest, codexArgs, prompt, alias)
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
		result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, fmt.Sprintf("validator-%s-round-%d", sanitizePathToken(group.GroupID), round))
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
	lines := []string{
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
	for _, finding := range group.Findings {
		lines = append(lines,
			fmt.Sprintf("- fingerprint: %s", finding.Fingerprint),
			fmt.Sprintf("  path: %s", finding.Path),
			fmt.Sprintf("  line: %d", finding.Line),
			fmt.Sprintf("  severity: %s", finding.Severity),
			fmt.Sprintf("  title: %s", finding.Title),
			fmt.Sprintf("  summary: %s", promptSnippetLimit(finding.Summary, 600)),
			fmt.Sprintf("  detail: %s", promptSnippetLimit(finding.Detail, 800)),
		)
		if strings.TrimSpace(finding.Fix) != "" {
			lines = append(lines, fmt.Sprintf("  fix: %s", promptSnippetLimit(finding.Fix, 600)))
		}
		if !seenSnippets[finding.Path] {
			seenSnippets[finding.Path] = true
			if snippet := localWorkFindingSnippet(manifest.SandboxRepoPath, finding); strings.TrimSpace(snippet) != "" {
				lines = append(lines, fmt.Sprintf("  code: %s", promptSnippetLimit(snippet, 1200)))
			}
			if baseline := localWorkFindingBaselineSnippet(manifest, finding); strings.TrimSpace(baseline) != "" {
				lines = append(lines, fmt.Sprintf("  baseline code: %s", promptSnippetLimit(baseline, 1200)))
			}
			if diff := localWorkFindingDiffSnippet(manifest, finding.Path); strings.TrimSpace(diff) != "" {
				lines = append(lines, fmt.Sprintf("  diff: %s", promptSnippetLimit(diff, 1800)))
			}
		}
	}
	return capPromptSize(strings.Join(lines, "\n")+"\n", localWorkValidationPromptLimit), nil
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
	return os.WriteFile(path, mustMarshalJSON(value), 0o644)
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
	store, err := openLocalWorkDB()
	if err != nil {
		return localWorkIterationRuntimeState{}, err
	}
	defer store.Close()
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
}

func writeLocalWorkRuntimeState(runID string, state localWorkIterationRuntimeState) error {
	store, err := openLocalWorkDB()
	if err != nil {
		return err
	}
	defer store.Close()
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
}

func removeLocalWorkRuntimeState(runID string, iteration int) error {
	store, err := openLocalWorkDB()
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = store.db.Exec(`DELETE FROM runtime_states WHERE run_id = ? AND iteration = ?`, runID, iteration)
	return err
}

func appendLocalWorkFindingHistory(runID string, events []localWorkFindingHistoryEvent) error {
	if len(events) == 0 {
		return nil
	}
	store, err := openLocalWorkDB()
	if err != nil {
		return err
	}
	defer store.Close()
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
	store, err := openLocalWorkDB()
	if err != nil {
		return err
	}
	defer store.Close()
	if state != nil {
		if state.Iteration == 0 {
			state.Iteration = manifest.CurrentIteration
		}
		if manifest.CurrentIteration == 0 {
			manifest.CurrentIteration = state.Iteration
		}
	}
	return store.writeActiveState(*manifest, state)
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

func writeLocalWorkManifest(manifest localWorkManifest) error {
	store, err := openLocalWorkDB()
	if err != nil {
		return err
	}
	defer store.Close()
	return store.writeManifest(manifest)
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
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	trimmed = tailLines(trimmed, localWorkPromptSnippetLines)
	if charLimit <= 0 || len(trimmed) <= charLimit {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:charLimit]) + "... [truncated]"
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
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	history := []localWorkFindingHistoryEvent{}
	store, err := openLocalWorkDB()
	if err == nil {
		rows, queryErr := store.db.Query(`SELECT event_json FROM finding_history WHERE run_id = ? ORDER BY id`, manifest.RunID)
		if queryErr == nil {
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					continue
				}
				var event localWorkFindingHistoryEvent
				if err := json.Unmarshal([]byte(raw), &event); err == nil {
					history = append(history, event)
				}
			}
			rows.Close()
		}
		store.Close()
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
		"## Iterations",
	}
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
	retrospectivePath := filepath.Join(runDir, "retrospective.md")
	if err := os.WriteFile(retrospectivePath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return content, nil
}
