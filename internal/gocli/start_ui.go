package gocli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	startUIDefaultAPIPort   = 17653
	startUIDefaultWebPort   = 17654
	startUIBindHost         = "127.0.0.1"
	startUIOverviewRunLimit = 10
)

type startUIRuntimeState struct {
	ProcessID int    `json:"process_id"`
	APIURL    string `json:"api_url"`
	WebURL    string `json:"web_url"`
	StartedAt string `json:"started_at"`
	StoppedAt string `json:"stopped_at,omitempty"`
}

type startUISupervisor struct {
	runtimePath string
	apiServer   *http.Server
	webServer   *http.Server
	apiURL      string
	webURL      string
}

type startUIAPI struct {
	cwd              string
	allowedWebOrigin string
	overviewCacheMu  sync.Mutex
	overviewCache    startUIOverviewCache
}

type startUIOverviewCache struct {
	valid     bool
	token     string
	checkedAt time.Time
	deps      []string
	overview  startUIOverview
	events    map[string]any
}

var startUIOverviewCacheProbeInterval = 5 * time.Second

type startUIOverviewDependencySnapshot struct {
	deps  []string
	token string
}

// startUIOverviewCacheAfterUncachedBuildHook is overridden by tests to simulate
// an external writer changing overview dependencies between build and snapshot.
var startUIOverviewCacheAfterUncachedBuildHook func()

type startUITotals struct {
	Repos            int `json:"repos"`
	IssuesQueued     int `json:"issues_queued"`
	IssuesInProgress int `json:"issues_in_progress"`
	ServiceQueued    int `json:"service_queued"`
	ServiceRunning   int `json:"service_running"`
	ScoutQueued      int `json:"scout_queued"`
	ScoutRunning     int `json:"scout_running"`
	ScoutFailed      int `json:"scout_failed"`
	ScoutCompleted   int `json:"scout_completed"`
	PlannedQueued    int `json:"planned_queued"`
	PlannedLaunching int `json:"planned_launching"`
	BlockedIssues    int `json:"blocked_issues"`
	ActiveWorkRuns   int `json:"active_work_runs"`
	PendingWorkItems int `json:"pending_work_items"`
	HiddenWorkItems  int `json:"hidden_work_items"`
	Investigations   int `json:"investigations"`
	ReviewItems      int `json:"review_items"`
	ReplyItems       int `json:"reply_items"`
	ApprovalItems    int `json:"approval_items"`
}

type startUIRepoSummary struct {
	RepoSlug           string                            `json:"repo_slug"`
	SettingsPath       string                            `json:"settings_path,omitempty"`
	RepoMode           string                            `json:"repo_mode,omitempty"`
	IssuePickMode      string                            `json:"issue_pick_mode,omitempty"`
	PRForwardMode      string                            `json:"pr_forward_mode,omitempty"`
	ForkIssuesMode     string                            `json:"fork_issues_mode,omitempty"`
	ImplementMode      string                            `json:"implement_mode,omitempty"`
	PublishTarget      string                            `json:"publish_target,omitempty"`
	StartParticipation bool                              `json:"start_participation"`
	UpdatedAt          string                            `json:"updated_at,omitempty"`
	StatePath          string                            `json:"state_path,omitempty"`
	SourcePath         string                            `json:"source_path,omitempty"`
	ScoutCatalog       []startUIScoutCatalogEntry        `json:"scout_catalog,omitempty"`
	ScoutsByRole       map[string]startUIRepoScoutConfig `json:"scouts_by_role,omitempty"`
	Scouts             startUIRepoScouts                 `json:"scouts"`
	IssueCounts        map[string]int                    `json:"issue_counts"`
	ServiceTaskCounts  map[string]int                    `json:"service_task_counts"`
	ScoutJobCounts     map[string]int                    `json:"scout_job_counts"`
	PlannedItemCounts  map[string]int                    `json:"planned_item_counts"`
	LastRun            *startWorkLastRun                 `json:"last_run,omitempty"`
	DefaultBranch      string                            `json:"default_branch,omitempty"`
	Settings           *githubRepoSettings               `json:"settings,omitempty"`
	State              *startWorkState                   `json:"state,omitempty"`
}

type startUIOverview struct {
	GeneratedAt  string                     `json:"generated_at"`
	Totals       startUITotals              `json:"totals"`
	ScoutCatalog []startUIScoutCatalogEntry `json:"scout_catalog,omitempty"`
	Repos        []startUIRepoSummary       `json:"repos"`
	WorkRuns     []startUIWorkRun           `json:"work_runs"`
	WorkItems    []startUIWorkItem          `json:"work_items"`
	HUD          HUDRenderContext           `json:"hud"`
}

type startUIUsageFilters struct {
	Since    string `json:"since,omitempty"`
	Project  string `json:"project,omitempty"`
	Root     string `json:"root,omitempty"`
	Activity string `json:"activity,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Model    string `json:"model,omitempty"`
}

type startUIUsageReport struct {
	GeneratedAt string                  `json:"generated_at"`
	Filters     startUIUsageFilters     `json:"filters"`
	Summary     usageSummaryReport      `json:"summary"`
	ByRoot      []usageGroupRow         `json:"by_root"`
	ByActivity  []usageGroupRow         `json:"by_activity"`
	ByPhase     []usageGroupRow         `json:"by_phase"`
	ByLane      []usageGroupRow         `json:"by_lane"`
	ByDay       []usageGroupRow         `json:"by_day"`
	ByModel     []usageGroupRow         `json:"by_model"`
	TopSessions []usageRecord           `json:"top_sessions"`
	Insights    []usageAnalyticsInsight `json:"insights"`
}

type startUIWorkRun struct {
	RunID            string `json:"run_id"`
	Backend          string `json:"backend"`
	RepoKey          string `json:"repo_key,omitempty"`
	RepoName         string `json:"repo_name,omitempty"`
	RepoSlug         string `json:"repo_slug,omitempty"`
	RepoLabel        string `json:"repo_label,omitempty"`
	Status           string `json:"status,omitempty"`
	CurrentPhase     string `json:"current_phase,omitempty"`
	CurrentIteration int    `json:"current_iteration,omitempty"`
	UpdatedAt        string `json:"updated_at"`
	TargetKind       string `json:"target_kind,omitempty"`
	TargetURL        string `json:"target_url,omitempty"`
	ArtifactPath     string `json:"artifact_path,omitempty"`
	PublicationState string `json:"publication_state,omitempty"`
	Pending          bool   `json:"pending"`
	AttentionState   string `json:"attention_state,omitempty"`
}

type startUIWorkItem struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	SourceKind     string `json:"source_kind"`
	Status         string `json:"status"`
	RepoSlug       string `json:"repo_slug,omitempty"`
	Subject        string `json:"subject"`
	TargetURL      string `json:"target_url,omitempty"`
	LinkedRunID    string `json:"linked_run_id,omitempty"`
	DraftKind      string `json:"draft_kind,omitempty"`
	DraftSummary   string `json:"draft_summary,omitempty"`
	UpdatedAt      string `json:"updated_at"`
	Hidden         bool   `json:"hidden,omitempty"`
	Pending        bool   `json:"pending"`
	AttentionState string `json:"attention_state,omitempty"`
}

type startUIWorkRunLogFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type startUIWorkRunLogsResponse struct {
	Summary      startUIWorkRun          `json:"summary"`
	ArtifactRoot string                  `json:"artifact_root"`
	DefaultPath  string                  `json:"default_path,omitempty"`
	Files        []startUIWorkRunLogFile `json:"files"`
}

type startUIIssuePatchRequest struct {
	Priority       *int    `json:"priority,omitempty"`
	ScheduleAt     *string `json:"schedule_at,omitempty"`
	DeferredReason *string `json:"deferred_reason,omitempty"`
	ClearSchedule  bool    `json:"clear_schedule,omitempty"`
}

type startUIPlannedItemRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Priority    *int   `json:"priority,omitempty"`
	ScheduleAt  string `json:"schedule_at,omitempty"`
	LaunchKind  string `json:"launch_kind,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

type startUIIssueSearchRequest struct {
	Query string `json:"query,omitempty"`
}

type startUIScoutBatchActionRequest struct {
	Action  string   `json:"action,omitempty"`
	ItemIDs []string `json:"item_ids,omitempty"`
}

type startUIScoutActionResult struct {
	ItemID string `json:"item_id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type startUIIssueSearchResponse struct {
	Query          string                     `json:"query"`
	EffectiveQuery string                     `json:"effective_query"`
	Items          []startUIIssueSearchResult `json:"items"`
}

type startUIIssueSearchResult struct {
	ID            string   `json:"id"`
	Number        int      `json:"number"`
	Title         string   `json:"title"`
	TargetURL     string   `json:"target_url"`
	Labels        []string `json:"labels,omitempty"`
	Priority      int      `json:"priority"`
	PriorityLabel string   `json:"priority_label"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
	Scheduled     bool     `json:"scheduled"`
	PlannedItemID string   `json:"planned_item_id,omitempty"`
	PlannedState  string   `json:"planned_state,omitempty"`
	ScheduleAt    string   `json:"schedule_at,omitempty"`
}

type startUITrackedIssueScheduleRequest struct {
	Number     int      `json:"number"`
	Title      string   `json:"title,omitempty"`
	TargetURL  string   `json:"target_url,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Priority   *int     `json:"priority,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"`
}

type startUIPlannedItemPatchRequest struct {
	Title         *string `json:"title,omitempty"`
	Description   *string `json:"description,omitempty"`
	Priority      *int    `json:"priority,omitempty"`
	ScheduleAt    *string `json:"schedule_at,omitempty"`
	ClearSchedule bool    `json:"clear_schedule,omitempty"`
}

type startUIIssueSearchPayload struct {
	Items []startWorkIssuePayload `json:"items"`
}

type startUIRepoScoutConfig struct {
	Enabled          bool     `json:"enabled"`
	PolicyPath       string   `json:"policy_path,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	Schedule         string   `json:"schedule,omitempty"`
	IssueDestination string   `json:"issue_destination,omitempty"`
	ForkRepo         string   `json:"fork_repo,omitempty"`
	Labels           []string `json:"labels,omitempty"`
	SessionLimit     int      `json:"session_limit,omitempty"`
}

type startUIScoutCatalogEntry struct {
	Role                 string `json:"role"`
	ConfigKey            string `json:"config_key"`
	DisplayLabel         string `json:"display_label"`
	DefaultSchedule      string `json:"default_schedule,omitempty"`
	DefaultSessionLimit  int    `json:"default_session_limit,omitempty"`
	SupportsSessionLimit bool   `json:"supports_session_limit,omitempty"`
	UsesPreflight        bool   `json:"uses_preflight,omitempty"`
}

type startUIRepoScouts struct {
	Improvement startUIRepoScoutConfig `json:"improvement"`
	Enhancement startUIRepoScoutConfig `json:"enhancement"`
	UI          startUIRepoScoutConfig `json:"ui"`
}

type startUIRepoScoutsPatchRequest struct {
	Improvement  *startUIRepoScoutConfig            `json:"improvement,omitempty"`
	Enhancement  *startUIRepoScoutConfig            `json:"enhancement,omitempty"`
	UI           *startUIRepoScoutConfig            `json:"ui,omitempty"`
	ScoutsByRole map[string]*startUIRepoScoutConfig `json:"scouts_by_role,omitempty"`
}

func startUISetRepoScoutConfig(scouts *startUIRepoScouts, role string, config startUIRepoScoutConfig) {
	if scouts == nil {
		return
	}
	switch role {
	case improvementScoutRole:
		scouts.Improvement = config
	case enhancementScoutRole:
		scouts.Enhancement = config
	case uiScoutRole:
		scouts.UI = config
	}
}

func startUIScoutCatalog() []startUIScoutCatalogEntry {
	items := make([]startUIScoutCatalogEntry, 0, len(scoutRoleRegistry))
	for _, spec := range scoutRoleRegistry {
		items = append(items, startUIScoutCatalogEntry{
			Role:                 spec.Role,
			ConfigKey:            spec.ConfigKey,
			DisplayLabel:         spec.DisplayLabel,
			DefaultSchedule:      scoutScheduleWhenResolved,
			DefaultSessionLimit:  defaultScoutSessionLimit,
			SupportsSessionLimit: spec.SupportsSessionLimit,
			UsesPreflight:        spec.UsesPreflight,
		})
		if !spec.SupportsSessionLimit {
			items[len(items)-1].DefaultSessionLimit = 0
		}
	}
	return items
}

func startUIDefaultRepoScoutsByRole(repoPath string) map[string]startUIRepoScoutConfig {
	configs := map[string]startUIRepoScoutConfig{}
	for _, role := range supportedScoutRoleOrder {
		spec := scoutRoleSpecFor(role)
		configs[spec.ConfigKey] = startUIDefaultRepoScoutConfig(repoPath, role)
	}
	return configs
}

func cloneStartUIRepoScoutsByRole(configs map[string]startUIRepoScoutConfig) map[string]startUIRepoScoutConfig {
	if configs == nil {
		return map[string]startUIRepoScoutConfig{}
	}
	cloned := make(map[string]startUIRepoScoutConfig, len(configs))
	for key, value := range configs {
		cloned[key] = value
	}
	return cloned
}

func startUIRepoScoutsCompatibility(configs map[string]startUIRepoScoutConfig) startUIRepoScouts {
	out := startUIRepoScouts{}
	for _, role := range supportedScoutRoleOrder {
		spec := scoutRoleSpecFor(role)
		config, ok := configs[spec.ConfigKey]
		if !ok {
			continue
		}
		startUISetRepoScoutConfig(&out, role, config)
	}
	return out
}

func startUIGetRepoScoutPatch(patch *startUIRepoScoutsPatchRequest, role string) *startUIRepoScoutConfig {
	if patch == nil {
		return nil
	}
	switch role {
	case improvementScoutRole:
		return patch.Improvement
	case enhancementScoutRole:
		return patch.Enhancement
	case uiScoutRole:
		return patch.UI
	default:
		return nil
	}
}

type startUIRepoSettingsPatchRequest struct {
	RepoMode       string                         `json:"repo_mode"`
	IssuePickMode  string                         `json:"issue_pick_mode"`
	PRForwardMode  string                         `json:"pr_forward_mode"`
	ForkIssuesMode string                         `json:"fork_issues_mode"`
	ImplementMode  string                         `json:"implement_mode"`
	PublishTarget  string                         `json:"publish_target"`
	Scouts         *startUIRepoScoutsPatchRequest `json:"scouts,omitempty"`
}

type startUIScoutItem struct {
	ID                string   `json:"id"`
	Role              string   `json:"role"`
	Title             string   `json:"title"`
	Area              string   `json:"area,omitempty"`
	Summary           string   `json:"summary"`
	Rationale         string   `json:"rationale,omitempty"`
	Evidence          string   `json:"evidence,omitempty"`
	Impact            string   `json:"impact,omitempty"`
	SuggestedNextStep string   `json:"suggested_next_step,omitempty"`
	Confidence        string   `json:"confidence,omitempty"`
	Files             []string `json:"files,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	Page              string   `json:"page,omitempty"`
	Route             string   `json:"route,omitempty"`
	Severity          string   `json:"severity,omitempty"`
	TargetKind        string   `json:"target_kind,omitempty"`
	Screenshots       []string `json:"screenshots,omitempty"`
	ArtifactPath      string   `json:"artifact_path"`
	ProposalPath      string   `json:"proposal_path"`
	PolicyPath        string   `json:"policy_path,omitempty"`
	PreflightPath     string   `json:"preflight_path,omitempty"`
	IssueDraftPath    string   `json:"issue_draft_path,omitempty"`
	RawOutputPath     string   `json:"raw_output_path,omitempty"`
	GeneratedAt       string   `json:"generated_at,omitempty"`
	AuditMode         string   `json:"audit_mode,omitempty"`
	SurfaceKind       string   `json:"surface_kind,omitempty"`
	SurfaceTarget     string   `json:"surface_target,omitempty"`
	BrowserReady      bool     `json:"browser_ready,omitempty"`
	PreflightReason   string   `json:"preflight_reason,omitempty"`
	Destination       string   `json:"destination"`
	ForkRepo          string   `json:"fork_repo,omitempty"`
	Status            string   `json:"status"`
	RunID             string   `json:"run_id,omitempty"`
	PlannedItemID     string   `json:"planned_item_id,omitempty"`
	Error             string   `json:"error,omitempty"`
	UpdatedAt         string   `json:"updated_at,omitempty"`
	AvailableActions  []string `json:"available_actions,omitempty"`
}

type startUIScoutItemsResponse struct {
	Repo         startUIRepoSummary         `json:"repo"`
	ScoutCatalog []startUIScoutCatalogEntry `json:"scout_catalog,omitempty"`
	Items        []startUIScoutItem         `json:"items"`
	Action       string                     `json:"action,omitempty"`
	Results      []startUIScoutActionResult `json:"results,omitempty"`
	SuccessCount int                        `json:"success_count,omitempty"`
	FailureCount int                        `json:"failure_count,omitempty"`
}

type startUIWorkItemFixRequest struct {
	Instruction string `json:"instruction"`
}

type startUIFeedbackSyncRequest struct {
	RepoSlug string `json:"repo_slug,omitempty"`
}

type startUIInvestigateRequest struct {
	Query    string `json:"query"`
	RepoSlug string `json:"repo_slug,omitempty"`
}

type startUIIssueQueueItem struct {
	ID                string   `json:"id"`
	RepoSlug          string   `json:"repo_slug"`
	SourceNumber      int      `json:"source_number"`
	SourceURL         string   `json:"source_url,omitempty"`
	Title             string   `json:"title"`
	State             string   `json:"state,omitempty"`
	Status            string   `json:"status"`
	Priority          int      `json:"priority"`
	PriorityLabel     string   `json:"priority_label"`
	PrioritySource    string   `json:"priority_source,omitempty"`
	Complexity        int      `json:"complexity,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	TriageStatus      string   `json:"triage_status,omitempty"`
	TriageRationale   string   `json:"triage_rationale,omitempty"`
	TriageUpdatedAt   string   `json:"triage_updated_at,omitempty"`
	TriageError       string   `json:"triage_error,omitempty"`
	ScheduleAt        string   `json:"schedule_at,omitempty"`
	DeferredReason    string   `json:"deferred_reason,omitempty"`
	LastRunID         string   `json:"last_run_id,omitempty"`
	LastRunUpdatedAt  string   `json:"last_run_updated_at,omitempty"`
	LastRunError      string   `json:"last_run_error,omitempty"`
	PublishedPRNumber int      `json:"published_pr_number,omitempty"`
	PublishedPRURL    string   `json:"published_pr_url,omitempty"`
	PublicationState  string   `json:"publication_state,omitempty"`
	BlockedReason     string   `json:"blocked_reason,omitempty"`
	UpdatedAt         string   `json:"updated_at"`
	AttentionState    string   `json:"attention_state,omitempty"`
}

type startUIIssueQueueResponse struct {
	Items []startUIIssueQueueItem `json:"items"`
}

type startUIInvestigationSummary struct {
	RunID                   string `json:"run_id"`
	RepoSlug                string `json:"repo_slug,omitempty"`
	WorkspaceRoot           string `json:"workspace_root"`
	Query                   string `json:"query"`
	Status                  string `json:"status"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
	CompletedAt             string `json:"completed_at,omitempty"`
	AcceptedRound           int    `json:"accepted_round,omitempty"`
	FinalReportPath         string `json:"final_report_path,omitempty"`
	LastError               string `json:"last_error,omitempty"`
	OverallStatus           string `json:"overall_status,omitempty"`
	OverallShortExplanation string `json:"overall_short_explanation,omitempty"`
	IssueCount              int    `json:"issue_count,omitempty"`
	ProofCount              int    `json:"proof_count,omitempty"`
	AttentionState          string `json:"attention_state,omitempty"`
}

type startUIInvestigationDetail struct {
	Summary               startUIInvestigationSummary `json:"summary"`
	Manifest              investigateManifest         `json:"manifest"`
	FinalReport           *investigateReport          `json:"final_report,omitempty"`
	LatestValidatorResult *investigateValidatorResult `json:"latest_validator_result,omitempty"`
}

type startUIFeedbackQueueItem struct {
	ID                   string                       `json:"id"`
	Kind                 string                       `json:"kind"`
	Source               string                       `json:"source"`
	SourceKind           string                       `json:"source_kind"`
	RepoSlug             string                       `json:"repo_slug,omitempty"`
	Subject              string                       `json:"subject"`
	TargetURL            string                       `json:"target_url,omitempty"`
	Status               string                       `json:"status"`
	UpdatedAt            string                       `json:"updated_at"`
	DraftSummary         string                       `json:"draft_summary,omitempty"`
	DraftBody            string                       `json:"draft_body,omitempty"`
	ReviewEvent          string                       `json:"review_event,omitempty"`
	SuggestedDisposition string                       `json:"suggested_disposition,omitempty"`
	DraftConfidence      float64                      `json:"draft_confidence,omitempty"`
	InlineCommentCount   int                          `json:"inline_comment_count,omitempty"`
	CommentKind          string                       `json:"comment_kind,omitempty"`
	CommentHTMLURL       string                       `json:"comment_html_url,omitempty"`
	CommentPath          string                       `json:"comment_path,omitempty"`
	CommentLine          int                          `json:"comment_line,omitempty"`
	AttentionState       string                       `json:"attention_state,omitempty"`
	InlineComments       []workItemDraftInlineComment `json:"inline_comments,omitempty"`
}

type startUIFeedbackQueueResponse struct {
	Items []startUIFeedbackQueueItem `json:"items"`
}

type startUIApprovalQueueItem struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	RepoSlug       string `json:"repo_slug,omitempty"`
	Subject        string `json:"subject"`
	Status         string `json:"status,omitempty"`
	Reason         string `json:"reason,omitempty"`
	NextAction     string `json:"next_action,omitempty"`
	ActionKind     string `json:"action_kind,omitempty"`
	ExternalURL    string `json:"external_url,omitempty"`
	TargetURL      string `json:"target_url,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	ItemID         string `json:"item_id,omitempty"`
	PlannedItemID  string `json:"planned_item_id,omitempty"`
	ScoutJobID     string `json:"scout_job_id,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	AttentionState string `json:"attention_state,omitempty"`
}

type startUIApprovalQueueResponse struct {
	Items []startUIApprovalQueueItem `json:"items"`
}

type startUIWorkRunDetail struct {
	Summary         startUIWorkRun            `json:"summary"`
	Backend         string                    `json:"backend"`
	LocalManifest   *localWorkManifest        `json:"local_manifest,omitempty"`
	GithubManifest  *githubWorkManifest       `json:"github_manifest,omitempty"`
	GithubStatus    *githubWorkStatusSnapshot `json:"github_status,omitempty"`
	NextAction      string                    `json:"next_action,omitempty"`
	HumanGateReason string                    `json:"human_gate_reason,omitempty"`
	SyncAllowed     bool                      `json:"sync_allowed,omitempty"`
	ExternalURL     string                    `json:"external_url,omitempty"`
}

type startUIBackgroundLaunch struct {
	Status        string `json:"status,omitempty"`
	Result        string `json:"result,omitempty"`
	LogPath       string `json:"log_path,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	Query         string `json:"query,omitempty"`
}

var startUISpawnIssueInvestigation = func(repoSlug string, issue startWorkIssueState) (startUIBackgroundLaunch, error) {
	return spawnStartUIIssueInvestigation(repoSlug, issue)
}

var startUISpawnInvestigateQuery = func(workspaceRoot string, query string) (startUIBackgroundLaunch, error) {
	return spawnStartUIInvestigateQuery(workspaceRoot, query)
}

var startUILaunchTrackedIssueWork = func(repoSlug string, issue startWorkIssueState) (startUIBackgroundLaunch, error) {
	return launchStartUITrackedIssueWork(repoSlug, issue)
}

var startUISyncGithubRun = func(options githubWorkSyncOptions) error {
	return syncGithubWork(options)
}

var startUIDropWorkRun = func(runID string) error {
	return dropWorkRunByID(runID)
}

var startUIDropRepo = func(repoSlug string) error {
	return repoDrop(repoSlug)
}

var startUISyncGithubFeedback = func(options workItemSyncCommandOptions) (githubWorkItemSyncResult, error) {
	return syncGithubWorkItems(options)
}

func launchStartUISupervisor(cwd string, options startOptions) (*startUISupervisor, error) {
	apiListener, apiURL, err := listenLoopbackPort(startUIBindHost, options.UIAPIPort)
	if err != nil {
		return nil, err
	}
	webListener, webURL, err := listenLoopbackPort(startUIBindHost, options.UIWebPort)
	if err != nil {
		_ = apiListener.Close()
		return nil, err
	}

	api := &startUIAPI{cwd: cwd, allowedWebOrigin: webURL}
	apiServer := &http.Server{Handler: api.routes()}
	webServer := &http.Server{Handler: startUIWebHandler(apiURL)}

	supervisor := &startUISupervisor{
		runtimePath: filepath.Join(githubNanaHome(), "start", "ui", "runtime.json"),
		apiServer:   apiServer,
		webServer:   webServer,
		apiURL:      apiURL,
		webURL:      webURL,
	}
	go func() {
		_ = apiServer.Serve(apiListener)
	}()
	go func() {
		_ = webServer.Serve(webListener)
	}()
	if err := writeGithubJSON(supervisor.runtimePath, startUIRuntimeState{
		ProcessID: os.Getpid(),
		APIURL:    apiURL,
		WebURL:    webURL,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		_ = supervisor.Close()
		return nil, err
	}
	fmt.Fprintf(os.Stdout, "[start-ui] API: %s\n", apiURL)
	fmt.Fprintf(os.Stdout, "[start-ui] Web: %s\n", webURL)
	return supervisor, nil
}

func (s *startUISupervisor) Close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.apiServer != nil {
		_ = s.apiServer.Shutdown(ctx)
	}
	if s.webServer != nil {
		_ = s.webServer.Shutdown(ctx)
	}
	runtime := startUIRuntimeState{}
	_ = readGithubJSON(s.runtimePath, &runtime)
	runtime.ProcessID = os.Getpid()
	runtime.APIURL = s.apiURL
	runtime.WebURL = s.webURL
	runtime.StoppedAt = time.Now().UTC().Format(time.RFC3339)
	return writeGithubJSON(s.runtimePath, runtime)
}

func listenLoopbackPort(host string, preferredPort int) (net.Listener, string, error) {
	if preferredPort <= 0 {
		preferredPort = 0
	}
	tryPorts := []int{}
	if preferredPort == 0 {
		tryPorts = append(tryPorts, 0)
	} else {
		for port := preferredPort; port < preferredPort+50; port++ {
			tryPorts = append(tryPorts, port)
		}
	}
	var lastErr error
	for _, port := range tryPorts {
		address := net.JoinHostPort(host, strconv.Itoa(port))
		listener, err := net.Listen("tcp", address)
		if err == nil {
			resolvedPort := listener.Addr().(*net.TCPAddr).Port
			return listener, fmt.Sprintf("http://%s:%d", host, resolvedPort), nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func (h *startUIAPI) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/overview", h.handleOverview)
	mux.HandleFunc("/api/v1/usage", h.handleUsage)
	mux.HandleFunc("/api/v1/attention", h.handleAttention)
	mux.HandleFunc("/api/v1/issues", h.handleIssues)
	mux.HandleFunc("/api/v1/investigations", h.handleInvestigations)
	mux.HandleFunc("/api/v1/investigations/", h.handleInvestigation)
	mux.HandleFunc("/api/v1/reviews", h.handleReviews)
	mux.HandleFunc("/api/v1/replies", h.handleReplies)
	mux.HandleFunc("/api/v1/feedback/sync", h.handleFeedbackSync)
	mux.HandleFunc("/api/v1/approvals", h.handleApprovals)
	mux.HandleFunc("/api/v1/repos", h.handleRepos)
	mux.HandleFunc("/api/v1/repos/", h.handleRepoRoute)
	mux.HandleFunc("/api/v1/planned-items/", h.handlePlannedItemRoute)
	mux.HandleFunc("/api/v1/work/runs", h.handleWorkRuns)
	mux.HandleFunc("/api/v1/work/runs/", h.handleWorkRun)
	mux.HandleFunc("/api/v1/work-items", h.handleWorkItems)
	mux.HandleFunc("/api/v1/work-items/", h.handleWorkItem)
	mux.HandleFunc("/api/v1/hud", h.handleHUD)
	mux.HandleFunc("/api/v1/events", h.handleEvents)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.applyCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *startUIAPI) applyCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", h.allowedWebOrigin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func (h *startUIAPI) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	overview, err := h.buildOverview()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, overview)
}

func (h *startUIAPI) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report, err := h.buildUsageReport(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONResponse(w, report)
}

func (h *startUIAPI) handleAttention(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report, err := buildAttentionReport(h.cwd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, report)
}

func (h *startUIAPI) handleIssues(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := listStartUIIssueQueue()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, startUIIssueQueueResponse{Items: items})
}

func (h *startUIAPI) handleInvestigations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := listStartUIInvestigations(h.cwd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, map[string]any{"items": items})
	case http.MethodPost:
		var payload startUIInvestigateRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		workspaceRoot := strings.TrimSpace(h.cwd)
		if repoSlug := strings.TrimSpace(payload.RepoSlug); repoSlug != "" {
			sourcePath, err := ensureStartUIRepoInvestigationWorkspace(repoSlug)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			workspaceRoot = sourcePath
		}
		query := strings.TrimSpace(payload.Query)
		if query == "" {
			http.Error(w, "query is required", http.StatusBadRequest)
			return
		}
		launch, err := startUISpawnInvestigateQuery(workspaceRoot, query)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"launch": launch})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *startUIAPI) handleInvestigation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/investigations/"), "/")
	if runID == "" {
		http.NotFound(w, r)
		return
	}
	detail, err := loadStartUIInvestigationDetail(h.cwd, runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSONResponse(w, detail)
}

func (h *startUIAPI) handleReviews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := loadStartUIFeedbackQueue("review")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, startUIFeedbackQueueResponse{Items: items})
}

func (h *startUIAPI) handleReplies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := loadStartUIFeedbackQueue("reply")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, startUIFeedbackQueueResponse{Items: items})
}

func (h *startUIAPI) handleFeedbackSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload := startUIFeedbackSyncRequest{}
	if r.Body != nil {
		defer r.Body.Close()
		contentBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		content := strings.TrimSpace(string(contentBytes))
		if content != "" {
			if err := json.Unmarshal([]byte(content), &payload); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
		}
	}
	result, err := startUISyncGithubFeedback(workItemSyncCommandOptions{
		RepoSlug: strings.TrimSpace(payload.RepoSlug),
		Limit:    50,
		AutoRun:  false,
	})
	h.invalidateOverviewCache()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reviews, err := loadStartUIFeedbackQueue("review")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	replies, err := loadStartUIFeedbackQueue("reply")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]any{
		"result":  result,
		"reviews": reviews,
		"replies": replies,
	})
}

func (h *startUIAPI) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := loadStartUIApprovals()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, startUIApprovalQueueResponse{Items: items})
}

func (h *startUIAPI) handleRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repos, err := listStartUIRepoSummaries(true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]any{"repos": repos})
}

func (h *startUIAPI) handleRepoRoute(w http.ResponseWriter, r *http.Request) {
	repoSlug, tail, ok := parseStartUIRepoRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && tail == "start-state":
		summary, err := loadStartUIRepoSummary(repoSlug, true)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSONResponse(w, summary)
	case r.Method == http.MethodPost && tail == "drop":
		if err := startUIDropRepo(repoSlug); err != nil {
			h.invalidateOverviewCache()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
		writeJSONResponse(w, map[string]any{"repo_slug": repoSlug, "dropped": true})
	case r.Method == http.MethodPost && strings.HasPrefix(tail, "issues/"):
		issueNumber, action, ok := parseStartUIRepoIssueActionRoute(tail)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch action {
		case "investigate":
			state, err := readStartWorkState(repoSlug)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			issue, ok := state.Issues[strconv.Itoa(issueNumber)]
			if !ok {
				http.Error(w, fmt.Sprintf("issue #%d is not tracked in start state", issueNumber), http.StatusNotFound)
				return
			}
			launch, err := startUISpawnIssueInvestigation(repoSlug, issue)
			h.invalidateOverviewCache()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSONResponse(w, map[string]any{"launch": launch, "issue": issue})
		case "launch-work":
			state, err := readStartWorkState(repoSlug)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			issue, ok := state.Issues[strconv.Itoa(issueNumber)]
			if !ok {
				http.Error(w, fmt.Sprintf("issue #%d is not tracked in start state", issueNumber), http.StatusNotFound)
				return
			}
			launch, err := startUILaunchTrackedIssueWork(repoSlug, issue)
			h.invalidateOverviewCache()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSONResponse(w, map[string]any{"launch": launch, "issue": issue})
		default:
			http.NotFound(w, r)
		}
	case r.Method == http.MethodPatch && strings.HasPrefix(tail, "issues/"):
		issueNumber, err := strconv.Atoi(strings.TrimPrefix(tail, "issues/"))
		if err != nil {
			http.Error(w, "invalid issue number", http.StatusBadRequest)
			return
		}
		var payload startUIIssuePatchRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		state, issue, err := patchStartUIIssue(repoSlug, issueNumber, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": state, "issue": issue})
	case r.Method == http.MethodPatch && tail == "settings":
		var payload startUIRepoSettingsPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		summary, err := patchStartUIRepoSettings(repoSlug, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"repo": summary})
	case r.Method == http.MethodGet && tail == "scout-items":
		payload, err := loadStartUIScoutItems(repoSlug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, payload)
	case r.Method == http.MethodPost && tail == "issue-search":
		var payload startUIIssueSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		response, err := searchStartUIRepoIssues(repoSlug, payload.Query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, response)
	case r.Method == http.MethodPost && tail == "tracked-issues/schedule":
		var payload startUITrackedIssueScheduleRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		state, item, err := upsertStartUITrackedIssuePlannedItem(repoSlug, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": state, "planned_item": item})
	case r.Method == http.MethodPost && tail == "scout-items/batch":
		var payload startUIScoutBatchActionRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		response, err := mutateStartUIScoutItems(repoSlug, payload.ItemIDs, payload.Action)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, response)
	case r.Method == http.MethodPost && strings.HasPrefix(tail, "scout-items/"):
		itemID, action, ok := parseStartUIScoutItemRoute(tail)
		if !ok {
			http.NotFound(w, r)
			return
		}
		payload, err := mutateStartUIScoutItem(repoSlug, itemID, action)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, payload)
	case r.Method == http.MethodPost && tail == "planned-items":
		var payload startUIPlannedItemRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		state, item, err := createStartUIPlannedItem(repoSlug, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": state, "planned_item": item})
	default:
		http.NotFound(w, r)
	}
}

func (h *startUIAPI) handlePlannedItemRoute(w http.ResponseWriter, r *http.Request) {
	itemID, action, ok := parseStartUIPlannedItemRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodPost && action == "launch-now":
		repoSlug, state, item, err := findStartUIPlannedItem(itemID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		updatedState, updatedItem, launch, err := launchStartUIPlannedItemNow(repoSlug, state, item)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": updatedState, "planned_item": updatedItem, "launch": launch})
	case r.Method == http.MethodPatch && action == "":
		var payload startUIPlannedItemPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		updatedState, updatedItem, err := patchStartUIPlannedItem(itemID, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": updatedState, "planned_item": updatedItem})
	case r.Method == http.MethodDelete && action == "":
		updatedState, removedItem, err := deleteStartUIPlannedItem(itemID)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": updatedState, "removed_item": removedItem})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *startUIAPI) handleWorkRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runs, err := loadStartUIWorkRuns(20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]any{"runs": runs})
}

func (h *startUIAPI) handleWorkItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	includeHidden := r.URL.Query().Get("all") == "1"
	onlyHidden := r.URL.Query().Get("hidden") == "1"
	items, err := loadStartUIWorkItems(20, includeHidden, onlyHidden)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]any{"items": items})
}

func (h *startUIAPI) handleWorkItem(w http.ResponseWriter, r *http.Request) {
	itemID, tail, ok := parseStartUIWorkItemRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && tail == "":
		detail, err := readWorkItemDetail(itemID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONResponse(w, detail)
	case r.Method == http.MethodPost && tail == "run":
		result, err := runWorkItemByID(h.cwd, itemID, nil, false)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"item": result.Item})
	case r.Method == http.MethodPost && tail == "submit":
		item, err := submitWorkItemByID(itemID, "ui")
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"item": item})
	case r.Method == http.MethodPost && tail == "drop":
		err := dropWorkItemByID(itemID, "ui")
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		detail, err := readWorkItemDetail(itemID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, detail)
	case r.Method == http.MethodPost && tail == "restore":
		err := restoreWorkItemByID(itemID, "ui")
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		detail, err := readWorkItemDetail(itemID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, detail)
	case r.Method == http.MethodPost && tail == "fix":
		var payload startUIWorkItemFixRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		item, err := fixWorkItemByID(h.cwd, workItemFixCommandOptions{
			ItemID:      itemID,
			Instruction: strings.TrimSpace(payload.Instruction),
		})
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"item": item})
	default:
		http.NotFound(w, r)
	}
}

func (h *startUIAPI) handleWorkRun(w http.ResponseWriter, r *http.Request) {
	runID, tail, ok := parseStartUIWorkRunRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost && tail == "drop" {
		if err := startUIDropWorkRun(runID); err != nil {
			h.invalidateOverviewCache()
			if strings.Contains(strings.ToLower(err.Error()), "was not found") {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
		writeJSONResponse(w, map[string]any{"run_id": runID, "dropped": true})
		return
	}
	if r.Method == http.MethodPost && tail == "sync" {
		detail, err := loadStartUIWorkRunDetail(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if !detail.SyncAllowed {
			http.Error(w, "sync is only available for GitHub-backed runs", http.StatusBadRequest)
			return
		}
		if err := startUISyncGithubRun(githubWorkSyncOptions{RunID: runID}); err != nil {
			h.invalidateOverviewCache()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
		updated, err := loadStartUIWorkRunDetail(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, map[string]any{"detail": updated})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch tail {
	case "":
		detail, err := loadStartUIWorkRunDetail(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONResponse(w, detail)
	case "logs":
		payload, err := loadStartUIWorkRunLogs(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONResponse(w, payload)
	case "logs/content":
		path := strings.TrimSpace(r.URL.Query().Get("path"))
		payload, err := loadStartUIWorkRunLogContent(runID, path, startUIParseTailLines(r.URL.Query().Get("tail")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, payload)
	default:
		http.NotFound(w, r)
	}
}

func (h *startUIAPI) handleHUD(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, err := h.loadHUD()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, ctx)
}

func (h *startUIAPI) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	lastHash := ""
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		payload, err := h.buildEventsPayload()
		if err == nil {
			hash := hashStartUIEventPayload(payload)
			if hash != lastHash {
				lastHash = hash
				data, _ := json.Marshal(payload)
				fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
				flusher.Flush()
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *startUIAPI) buildOverview() (startUIOverview, error) {
	return h.buildCachedOverview()
}

func (h *startUIAPI) buildCachedOverview() (startUIOverview, error) {
	now := time.Now()
	h.overviewCacheMu.Lock()
	defer h.overviewCacheMu.Unlock()
	if h.overviewCache.valid {
		if startUIOverviewCacheProbeInterval > 0 && now.Sub(h.overviewCache.checkedAt) < startUIOverviewCacheProbeInterval {
			return h.overviewCache.overview, nil
		}
		token := fingerprintStartUIOverviewDependencies(h.overviewCache.deps)
		if token == h.overviewCache.token {
			h.overviewCache.checkedAt = now
			return h.overviewCache.overview, nil
		}
	}

	overview, snapshot, stable, err := h.buildOverviewWithStableDependencySnapshot()
	if err != nil {
		return startUIOverview{}, err
	}
	if !stable {
		overview, snapshot, stable, err = h.buildOverviewWithStableDependencySnapshot()
		if err != nil {
			h.overviewCache.valid = false
			return startUIOverview{}, err
		}
	}
	if !stable {
		h.overviewCache.valid = false
		return overview, nil
	}

	events := startUIOverviewEventsPayload(overview)
	h.overviewCache = startUIOverviewCache{
		valid:     true,
		token:     snapshot.token,
		checkedAt: time.Now(),
		deps:      snapshot.deps,
		overview:  overview,
		events:    events,
	}
	return overview, nil
}

func (h *startUIAPI) buildOverviewWithStableDependencySnapshot() (startUIOverview, startUIOverviewDependencySnapshot, bool, error) {
	before := snapshotStartUIOverviewDependencies(h.cwd)
	overview, err := h.buildOverviewUncached()
	if err != nil {
		return startUIOverview{}, startUIOverviewDependencySnapshot{}, false, err
	}
	if startUIOverviewCacheAfterUncachedBuildHook != nil {
		startUIOverviewCacheAfterUncachedBuildHook()
	}
	after := snapshotStartUIOverviewDependencies(h.cwd)
	return overview, after, sameStartUIOverviewDependencySnapshot(before, after), nil
}

func (h *startUIAPI) buildOverviewUncached() (startUIOverview, error) {
	repos, err := listStartUIRepoSummaries(false)
	if err != nil {
		return startUIOverview{}, err
	}
	workRuns, err := loadStartUIWorkRuns(startUIOverviewRunLimit)
	if err != nil {
		return startUIOverview{}, err
	}
	workItems, hiddenCount, pendingWorkItems, err := loadStartUIWorkItemsWithHiddenCount(10)
	if err != nil {
		return startUIOverview{}, err
	}
	investigations, err := listStartUIInvestigations(h.cwd)
	if err != nil {
		return startUIOverview{}, err
	}
	reviews, err := loadStartUIFeedbackQueue("review")
	if err != nil {
		return startUIOverview{}, err
	}
	replies, err := loadStartUIFeedbackQueue("reply")
	if err != nil {
		return startUIOverview{}, err
	}
	approvals, err := loadStartUIApprovals()
	if err != nil {
		return startUIOverview{}, err
	}
	hud, err := h.loadHUD()
	if err != nil {
		return startUIOverview{}, err
	}
	totals := startUITotals{Repos: len(repos)}
	for _, repo := range repos {
		totals.IssuesQueued += repo.IssueCounts[startWorkStatusQueued]
		totals.IssuesInProgress += repo.IssueCounts[startWorkStatusInProgress]
		totals.BlockedIssues += repo.IssueCounts[startWorkStatusBlocked]
		totals.ServiceQueued += repo.ServiceTaskCounts[startWorkServiceTaskQueued]
		totals.ServiceRunning += repo.ServiceTaskCounts[startWorkServiceTaskRunning]
		totals.ScoutQueued += repo.ScoutJobCounts[startScoutJobQueued]
		totals.ScoutRunning += repo.ScoutJobCounts[startScoutJobRunning]
		totals.ScoutFailed += repo.ScoutJobCounts[startScoutJobFailed]
		totals.ScoutCompleted += repo.ScoutJobCounts[startScoutJobCompleted]
		totals.PlannedQueued += repo.PlannedItemCounts[startPlannedItemQueued]
		totals.PlannedLaunching += repo.PlannedItemCounts[startPlannedItemLaunching]
	}
	for _, run := range workRuns {
		if run.Status == "running" || run.Status == "active" || run.Status == "in_progress" {
			totals.ActiveWorkRuns++
		}
	}
	totals.PendingWorkItems = pendingWorkItems
	totals.HiddenWorkItems = hiddenCount
	totals.Investigations = len(investigations)
	totals.ReviewItems = len(reviews)
	totals.ReplyItems = len(replies)
	totals.ApprovalItems = len(approvals)
	return startUIOverview{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Totals:       totals,
		ScoutCatalog: startUIScoutCatalog(),
		Repos:        repos,
		WorkRuns:     workRuns,
		WorkItems:    workItems,
		HUD:          hud,
	}, nil
}

func (h *startUIAPI) buildUsageReport(query url.Values) (startUIUsageReport, error) {
	options := usageOptions{
		View:     "summary",
		Limit:    10,
		CWD:      h.cwd,
		Since:    strings.TrimSpace(query.Get("since")),
		Project:  strings.TrimSpace(query.Get("project")),
		Root:     defaultString(strings.TrimSpace(query.Get("root")), "all"),
		Activity: strings.TrimSpace(query.Get("activity")),
		Phase:    strings.TrimSpace(query.Get("phase")),
		Model:    strings.TrimSpace(query.Get("model")),
	}
	if !usageRoots[options.Root] {
		return startUIUsageReport{}, fmt.Errorf("invalid usage root %q", options.Root)
	}
	if _, err := parseSinceSpec(options.Since); err != nil {
		return startUIUsageReport{}, err
	}
	records, sessionRootsScanned, err := collectUsageRecords(options)
	if err != nil {
		return startUIUsageReport{}, err
	}
	summary := buildUsageSummaryReport(records, sessionRootsScanned)
	analytics := buildUsageAnalyticsReport(records, sessionRootsScanned)
	return startUIUsageReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Filters: startUIUsageFilters{
			Since:    options.Since,
			Project:  options.Project,
			Root:     options.Root,
			Activity: options.Activity,
			Phase:    options.Phase,
			Model:    options.Model,
		},
		Summary:     summary,
		ByRoot:      buildUsageGroups(records, "root"),
		ByActivity:  buildUsageGroups(records, "activity"),
		ByPhase:     buildUsageGroups(records, "phase"),
		ByLane:      buildUsageGroups(records, "lane"),
		ByDay:       buildUsageGroups(records, "day"),
		ByModel:     buildUsageGroups(records, "model"),
		TopSessions: buildUsageTopReport(records, sessionRootsScanned, "session", 10).Sessions,
		Insights:    analytics.Insights,
	}, nil
}

func (h *startUIAPI) buildEventsPayload() (map[string]any, error) {
	overview, err := h.buildOverview()
	if err != nil {
		return nil, err
	}
	h.overviewCacheMu.Lock()
	if h.overviewCache.valid && h.overviewCache.overview.GeneratedAt == overview.GeneratedAt && h.overviewCache.events != nil {
		events := h.overviewCache.events
		h.overviewCacheMu.Unlock()
		return events, nil
	}
	h.overviewCacheMu.Unlock()
	return startUIOverviewEventsPayload(overview), nil
}

func startUIOverviewEventsPayload(overview startUIOverview) map[string]any {
	return map[string]any{
		"generated_at": overview.GeneratedAt,
		"totals":       overview.Totals,
		"repos":        overview.Repos,
		"work_runs":    overview.WorkRuns,
		"work_items":   overview.WorkItems,
		"hud":          overview.HUD,
	}
}

func (h *startUIAPI) invalidateOverviewCache() {
	h.overviewCacheMu.Lock()
	defer h.overviewCacheMu.Unlock()
	h.overviewCache.valid = false
}

func listStartUIOverviewDependencies(cwd string) []string {
	seen := map[string]bool{}
	paths := []string{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		clean := filepath.Clean(path)
		if seen[clean] {
			return
		}
		seen[clean] = true
		paths = append(paths, clean)
	}

	dbPath := localWorkDBPath()
	add(dbPath)
	add(dbPath + "-wal")
	add(dbPath + "-shm")
	addStartUIIndexedGithubManifestDependencies(add)

	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "start-state.json", add)
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "settings.json", add)
	addStartUIScoutPolicyDependencies(add)
	addStartUIInvestigationDependencies(cwd, add)
	addStartUIHUDDependencies(cwd, add)

	sort.Strings(paths)
	return paths
}

func addStartUIScoutPolicyDependencies(add func(string)) {
	repoSlugs, err := listStartUIRepoSlugs()
	if err != nil {
		return
	}
	for _, repoSlug := range repoSlugs {
		repoPath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
		if repoPath == "" {
			continue
		}
		for _, role := range supportedScoutRoleOrder {
			for _, path := range append([]string{repoScoutPolicyPath(repoPath, role, false)}, repoScoutLegacyPolicyPaths(repoPath, role)...) {
				add(path)
			}
		}
	}
}

func snapshotStartUIOverviewDependencies(cwd string) startUIOverviewDependencySnapshot {
	deps := listStartUIOverviewDependencies(cwd)
	return startUIOverviewDependencySnapshot{
		deps:  deps,
		token: fingerprintStartUIOverviewDependencies(deps),
	}
}

func sameStartUIOverviewDependencySnapshot(left startUIOverviewDependencySnapshot, right startUIOverviewDependencySnapshot) bool {
	if left.token != right.token || len(left.deps) != len(right.deps) {
		return false
	}
	for i := range left.deps {
		if left.deps[i] != right.deps[i] {
			return false
		}
	}
	return true
}

func addStartUIIndexedGithubManifestDependencies(add func(string)) {
	_, _ = withLocalWorkReadStore(func(store *localWorkDBStore) (struct{}, error) {
		rows, err := store.db.Query(`SELECT backend, manifest_path FROM work_run_index ORDER BY updated_at DESC LIMIT ?`, startUIOverviewRunLimit)
		if err != nil {
			return struct{}{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var backend string
			var manifestPath sql.NullString
			if err := rows.Scan(&backend, &manifestPath); err != nil {
				continue
			}
			if backend == "github" && manifestPath.Valid {
				add(manifestPath.String)
			}
		}
		return struct{}{}, rows.Err()
	})
}

func addStartUIRepoTreeDependencies(root string, targetFile string, add func(string)) {
	add(root)
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		depth := strings.Count(filepath.ToSlash(rel), "/") + 1
		if entry.IsDir() {
			if depth <= 2 {
				add(path)
				return nil
			}
			return filepath.SkipDir
		}
		if entry.Name() == targetFile {
			add(path)
		}
		return nil
	})
}

func addStartUIHUDDependencies(cwd string, add func(string)) {
	add(filepath.Join(cwd, ".nana", "hud-config.json"))
	add(filepath.Join(cwd, ".nana", "metrics.json"))
	addStartUIGitHUDDependencies(cwd, add)

	stateDir := BaseStateDir(cwd)
	add(stateDir)
	add(filepath.Join(stateDir, "hud-state.json"))
	add(filepath.Join(stateDir, "session.json"))
	addStartUIStateDirDependencies(stateDir, add)

	sessionsDir := filepath.Join(stateDir, "sessions")
	add(sessionsDir)
	if sessionID := ReadCurrentSessionID(cwd); sessionID != "" {
		sessionDir := filepath.Join(sessionsDir, sessionID)
		add(sessionDir)
		add(filepath.Join(sessionDir, "hud-state.json"))
		add(filepath.Join(sessionDir, "session.json"))
		addStartUIStateDirDependencies(sessionDir, add)
	}

	add(filepath.Join(cwd, ".nana", "setup-scope.json"))
	codexHome := ResolveCodexHomeForLaunch(cwd)
	add(managedAuthRegistryPathForHome(codexHome))
	add(managedAuthRuntimeStatePathForHome(codexHome))
	add(managedAuthAccountsDirForHome(codexHome))
}

func addStartUIGitHUDDependencies(cwd string, add func(string)) {
	gitPath := addStartUIGitControlPathDependencies(cwd, add)
	if gitPath == "" {
		return
	}
	gitDir := resolveStartUIGitDir(gitPath)
	if gitDir == "" {
		return
	}
	add(gitDir)
	commonDir := resolveStartUIGitCommonDir(gitDir, add)
	addStartUIGitDirDependencies(gitDir, add)
	if commonDir != "" && commonDir != gitDir {
		add(commonDir)
		addStartUIGitDirDependencies(commonDir, add)
	}

	headPath := filepath.Join(gitDir, "HEAD")
	if content, err := os.ReadFile(headPath); err == nil {
		raw := strings.TrimSpace(string(content))
		if strings.HasPrefix(raw, "ref:") {
			ref := strings.TrimSpace(strings.TrimPrefix(raw, "ref:"))
			if ref == "" {
				return
			}
			refPath := filepath.FromSlash(ref)
			add(filepath.Join(gitDir, refPath))
			if commonDir != "" && commonDir != gitDir {
				add(filepath.Join(commonDir, refPath))
			}
		}
	}
}

func addStartUIGitControlPathDependencies(cwd string, add func(string)) string {
	dir := filepath.Clean(cwd)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		add(gitPath)
		if _, err := os.Stat(gitPath); err == nil {
			return gitPath
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func resolveStartUIGitDir(gitPath string) string {
	gitDir := gitPath
	if content, err := os.ReadFile(gitPath); err == nil {
		raw := strings.TrimSpace(string(content))
		if strings.HasPrefix(raw, "gitdir:") {
			value := strings.TrimSpace(strings.TrimPrefix(raw, "gitdir:"))
			if value == "" {
				return ""
			}
			if filepath.IsAbs(value) {
				gitDir = value
			} else {
				gitDir = filepath.Join(filepath.Dir(gitPath), value)
			}
		}
	}
	return filepath.Clean(gitDir)
}

func resolveStartUIGitCommonDir(gitDir string, add func(string)) string {
	commonDirPath := filepath.Join(gitDir, "commondir")
	add(commonDirPath)
	content, err := os.ReadFile(commonDirPath)
	if err != nil {
		return gitDir
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return gitDir
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(gitDir, value))
}

func addStartUIGitDirDependencies(gitDir string, add func(string)) {
	add(filepath.Join(gitDir, "HEAD"))
	add(filepath.Join(gitDir, "config"))
	add(filepath.Join(gitDir, "config.worktree"))
	add(filepath.Join(gitDir, "packed-refs"))
}

func addStartUIStateDirDependencies(dir string, add func(string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, ok := modeFromStateFileName(name); ok || name == "session.json" || name == "hud-state.json" {
			add(filepath.Join(dir, name))
		}
	}
}

func fingerprintStartUIOverviewDependencies(paths []string) string {
	hasher := sha256.New()
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(hasher, "%s\tmissing\n", path)
				continue
			}
			fmt.Fprintf(hasher, "%s\terror:%v\n", path, err)
			continue
		}
		fmt.Fprintf(hasher, "%s\t%d\t%d\t%d\n", path, info.Size(), info.ModTime().UnixNano(), info.Mode().Type())
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func hashStartUIEventPayload(payload map[string]any) string {
	if len(payload) == 0 {
		return hashJSON(payload)
	}
	clone := make(map[string]any, len(payload))
	for key, value := range payload {
		if key == "generated_at" {
			continue
		}
		clone[key] = value
	}
	return hashJSON(clone)
}

func (h *startUIAPI) loadHUD() (HUDRenderContext, error) {
	config, err := readHUDConfig(h.cwd)
	if err != nil {
		return HUDRenderContext{}, err
	}
	return readAllHUDState(h.cwd, config)
}

func listStartUIIssueQueue() ([]startUIIssueQueueItem, error) {
	repos, err := listStartUIRepoSummaries(true)
	if err != nil {
		return nil, err
	}
	items := []startUIIssueQueueItem{}
	for _, repo := range repos {
		if repo.State == nil {
			continue
		}
		for _, issue := range repo.State.Issues {
			items = append(items, startUIIssueQueueItemFromState(repo.RepoSlug, issue))
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		leftRank := startUIAttentionRank(items[i].AttentionState)
		rightRank := startUIAttentionRank(items[j].AttentionState)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		leftScheduled := strings.TrimSpace(items[i].ScheduleAt)
		rightScheduled := strings.TrimSpace(items[j].ScheduleAt)
		if leftScheduled != rightScheduled {
			if leftScheduled == "" {
				return false
			}
			if rightScheduled == "" {
				return true
			}
			return leftScheduled < rightScheduled
		}
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		if items[i].RepoSlug != items[j].RepoSlug {
			return items[i].RepoSlug < items[j].RepoSlug
		}
		return items[i].SourceNumber < items[j].SourceNumber
	})
	return items, nil
}

func startUIIssueQueueItemFromState(repoSlug string, issue startWorkIssueState) startUIIssueQueueItem {
	priority := issue.Priority
	if issue.ManualPriorityUpdatedAt != "" {
		priority = issue.ManualPriority
	}
	attention := "queued"
	switch strings.ToLower(strings.TrimSpace(issue.Status)) {
	case startWorkStatusBlocked:
		attention = "blocked"
	case startWorkStatusInProgress:
		attention = "active"
	case startWorkStatusQueued:
		attention = "queued"
	case startWorkStatusCompleted:
		attention = "completed"
	}
	if strings.TrimSpace(issue.TriageError) != "" {
		attention = "failed"
	}
	return startUIIssueQueueItem{
		ID:                fmt.Sprintf("%s#%d", repoSlug, issue.SourceNumber),
		RepoSlug:          repoSlug,
		SourceNumber:      issue.SourceNumber,
		SourceURL:         issue.SourceURL,
		Title:             issue.Title,
		State:             issue.State,
		Status:            issue.Status,
		Priority:          priority,
		PriorityLabel:     startWorkPriorityLabel(priority),
		PrioritySource:    issue.PrioritySource,
		Complexity:        issue.Complexity,
		Labels:            append([]string{}, issue.Labels...),
		TriageStatus:      issue.TriageStatus,
		TriageRationale:   issue.TriageRationale,
		TriageUpdatedAt:   issue.TriageUpdatedAt,
		TriageError:       issue.TriageError,
		ScheduleAt:        issue.ScheduleAt,
		DeferredReason:    issue.DeferredReason,
		LastRunID:         issue.LastRunID,
		LastRunUpdatedAt:  issue.LastRunUpdatedAt,
		LastRunError:      issue.LastRunError,
		PublishedPRNumber: issue.PublishedPRNumber,
		PublishedPRURL:    issue.PublishedPRURL,
		PublicationState:  issue.PublicationState,
		BlockedReason:     issue.BlockedReason,
		UpdatedAt:         issue.UpdatedAt,
		AttentionState:    attention,
	}
}

func listStartUIInvestigationRoots(cwd string) ([]string, map[string]string, error) {
	workspaceRoot := resolveInvestigateWorkspaceRoot(cwd)
	repoIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	roots := []string{}
	add := func(path string) {
		clean := filepath.Clean(strings.TrimSpace(path))
		if clean == "" || seen[clean] {
			return
		}
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			seen[clean] = true
			roots = append(roots, clean)
		}
	}
	add(workspaceRoot)
	for sourcePath := range repoIndex {
		add(sourcePath)
	}
	sort.Strings(roots)
	return roots, repoIndex, nil
}

func listStartUIInvestigations(cwd string) ([]startUIInvestigationSummary, error) {
	roots, repoIndex, err := listStartUIInvestigationRoots(cwd)
	if err != nil {
		return nil, err
	}
	summaries := []startUIInvestigationSummary{}
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, ".nana", "logs", "investigate", "*", "manifest.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, manifestPath := range matches {
			summary, err := loadStartUIInvestigationSummary(manifestPath, root, repoIndex)
			if err != nil {
				continue
			}
			summaries = append(summaries, summary)
		}
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		leftRank := startUIAttentionRank(summaries[i].AttentionState)
		rightRank := startUIAttentionRank(summaries[j].AttentionState)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if summaries[i].UpdatedAt != summaries[j].UpdatedAt {
			return summaries[i].UpdatedAt > summaries[j].UpdatedAt
		}
		return summaries[i].RunID > summaries[j].RunID
	})
	return summaries, nil
}

func loadStartUIInvestigationSummary(manifestPath string, workspaceRoot string, repoIndex map[string]string) (startUIInvestigationSummary, error) {
	var manifest investigateManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return startUIInvestigationSummary{}, err
	}
	summary := startUIInvestigationSummary{
		RunID:           manifest.RunID,
		RepoSlug:        repoIndex[filepath.Clean(workspaceRoot)],
		WorkspaceRoot:   workspaceRoot,
		Query:           manifest.Query,
		Status:          manifest.Status,
		CreatedAt:       manifest.CreatedAt,
		UpdatedAt:       manifest.UpdatedAt,
		CompletedAt:     manifest.CompletedAt,
		AcceptedRound:   manifest.AcceptedRound,
		FinalReportPath: manifest.FinalReportPath,
		LastError:       manifest.LastError,
		AttentionState:  startUIInvestigationAttentionState(manifest.Status, manifest.LastError),
	}
	if strings.TrimSpace(manifest.FinalReportPath) != "" {
		var report investigateReport
		if err := readGithubJSON(manifest.FinalReportPath, &report); err == nil {
			summary.OverallStatus = report.OverallStatus
			summary.OverallShortExplanation = report.OverallShortExplanation
			summary.IssueCount = len(report.Issues)
			summary.ProofCount = len(report.OverallProofs)
		}
	}
	return summary, nil
}

func loadStartUIInvestigationDetail(cwd string, runID string) (startUIInvestigationDetail, error) {
	roots, repoIndex, err := listStartUIInvestigationRoots(cwd)
	if err != nil {
		return startUIInvestigationDetail{}, err
	}
	for _, root := range roots {
		manifestPath := filepath.Join(root, ".nana", "logs", "investigate", runID, "manifest.json")
		if !fileExists(manifestPath) {
			continue
		}
		var manifest investigateManifest
		if err := readGithubJSON(manifestPath, &manifest); err != nil {
			return startUIInvestigationDetail{}, err
		}
		summary, err := loadStartUIInvestigationSummary(manifestPath, root, repoIndex)
		if err != nil {
			return startUIInvestigationDetail{}, err
		}
		detail := startUIInvestigationDetail{
			Summary:  summary,
			Manifest: manifest,
		}
		if strings.TrimSpace(manifest.FinalReportPath) != "" {
			var report investigateReport
			if err := readGithubJSON(manifest.FinalReportPath, &report); err == nil {
				detail.FinalReport = &report
			}
		}
		if len(manifest.Rounds) > 0 {
			round := manifest.Rounds[len(manifest.Rounds)-1]
			if strings.TrimSpace(round.ValidatorResultPath) != "" && fileExists(round.ValidatorResultPath) {
				var result investigateValidatorResult
				if err := readGithubJSON(round.ValidatorResultPath, &result); err == nil {
					detail.LatestValidatorResult = &result
				}
			}
		}
		return detail, nil
	}
	return startUIInvestigationDetail{}, fmt.Errorf("investigation run %s was not found", runID)
}

func startUIInvestigationAttentionState(status string, lastError string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.Contains(normalized, "failed"), strings.TrimSpace(lastError) != "":
		return "failed"
	case normalized == investigateRunStatusCompleted:
		return "completed"
	case normalized == investigateRunStatusRunning:
		return "active"
	default:
		return "queued"
	}
}

func loadStartUIFeedbackQueue(kind string) ([]startUIFeedbackQueueItem, error) {
	items, err := listWorkItems(workItemListOptions{Limit: 200, IncludeHidden: false})
	if err != nil {
		return nil, err
	}
	out := []startUIFeedbackQueueItem{}
	for _, item := range items {
		if !startUIFeedbackItemMatches(kind, item) {
			continue
		}
		out = append(out, startUIFeedbackQueueItemFromItem(item, kind))
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftRank := startUIAttentionRank(out[i].AttentionState)
		rightRank := startUIAttentionRank(out[j].AttentionState)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func startUIFeedbackItemMatches(kind string, item workItem) bool {
	switch kind {
	case "review":
		return valueOrEmptyDraftKind(item.LatestDraft) == "review" || item.SourceKind == "review_request"
	case "reply":
		if valueOrEmptyDraftKind(item.LatestDraft) == "reply" {
			return true
		}
		return item.SourceKind == "thread_comment"
	default:
		return false
	}
}

func startUIFeedbackQueueItemFromItem(item workItem, kind string) startUIFeedbackQueueItem {
	draft := item.LatestDraft
	commentLine := 0
	if value, ok := item.Metadata["comment_line"]; ok {
		switch typed := value.(type) {
		case float64:
			commentLine = int(typed)
		case int:
			commentLine = typed
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
				commentLine = parsed
			}
		}
	}
	inlineComments := []workItemDraftInlineComment{}
	if draft != nil {
		inlineComments = append(inlineComments, draft.InlineComments...)
	}
	return startUIFeedbackQueueItem{
		ID:                   item.ID,
		Kind:                 kind,
		Source:               item.Source,
		SourceKind:           item.SourceKind,
		RepoSlug:             item.RepoSlug,
		Subject:              item.Subject,
		TargetURL:            item.TargetURL,
		Status:               item.Status,
		UpdatedAt:            item.UpdatedAt,
		DraftSummary:         safeDraftSummary(draft),
		DraftBody:            defaultString(draftBody(draft), strings.TrimSpace(item.Body)),
		ReviewEvent:          reviewEvent(draft),
		SuggestedDisposition: suggestedDisposition(draft),
		DraftConfidence:      draftConfidence(draft),
		InlineCommentCount:   len(inlineComments),
		CommentKind:          metadataString(item.Metadata, "comment_kind"),
		CommentHTMLURL:       metadataString(item.Metadata, "comment_html_url"),
		CommentPath:          metadataString(item.Metadata, "comment_path"),
		CommentLine:          commentLine,
		AttentionState:       startUIWorkItemAttentionState(item),
		InlineComments:       inlineComments,
	}
}

func loadStartUIApprovals() ([]startUIApprovalQueueItem, error) {
	runs, err := loadStartUIWorkRuns(50)
	if err != nil {
		return nil, err
	}
	items, err := listWorkItems(workItemListOptions{Limit: 200, IncludeHidden: false})
	if err != nil {
		return nil, err
	}
	repos, err := listStartUIRepoSummaries(true)
	if err != nil {
		return nil, err
	}
	out := []startUIApprovalQueueItem{}
	for _, run := range runs {
		if run.AttentionState != "blocked" {
			continue
		}
		out = append(out, startUIApprovalItemFromRun(run))
	}
	for _, item := range items {
		if item.Hidden || item.Status != workItemStatusDraftReady || item.LatestDraft == nil {
			continue
		}
		out = append(out, startUIApprovalQueueItem{
			ID:             "work-item:" + item.ID,
			Kind:           "work_item",
			RepoSlug:       item.RepoSlug,
			Subject:        item.Subject,
			Status:         item.Status,
			Reason:         "draft ready for human review and submission",
			NextAction:     "Approve this draft to submit it now, or open it to revise first.",
			ActionKind:     "approve_work_item",
			ExternalURL:    item.TargetURL,
			TargetURL:      item.TargetURL,
			ItemID:         item.ID,
			UpdatedAt:      item.UpdatedAt,
			AttentionState: "queued",
		})
	}
	for _, repo := range repos {
		if repo.State == nil {
			continue
		}
		for _, job := range repo.State.ScoutJobs {
			if job.Status != startScoutJobFailed {
				continue
			}
			out = append(out, startUIApprovalQueueItem{
				ID:             "scout-job:" + job.ID,
				Kind:           "scout_job",
				RepoSlug:       repo.RepoSlug,
				Subject:        job.Title,
				Status:         job.Status,
				Reason:         defaultString(strings.TrimSpace(job.LastError), "scout job failed"),
				NextAction:     "Retry this scout job to requeue it, or dismiss it.",
				ActionKind:     "retry_scout_job",
				RunID:          job.RunID,
				ScoutJobID:     job.ID,
				UpdatedAt:      job.UpdatedAt,
				AttentionState: "failed",
			})
		}
		for _, item := range repo.State.PlannedItems {
			if startWorkPlannedItemLooksScoutDerived(item) {
				continue
			}
			if item.State != startPlannedItemQueued && item.State != startPlannedItemFailed {
				continue
			}
			reason := "launch requested from approvals"
			if item.State == startPlannedItemFailed && strings.TrimSpace(item.LastError) != "" {
				reason = item.LastError
			}
			out = append(out, startUIApprovalQueueItem{
				ID:             "planned:" + item.ID,
				Kind:           "planned_item",
				RepoSlug:       repo.RepoSlug,
				Subject:        item.Title,
				Status:         item.State,
				Reason:         reason,
				NextAction:     "Approve this planned item to launch it now.",
				ActionKind:     "approve_planned_item",
				ExternalURL:    item.TargetURL,
				TargetURL:      item.TargetURL,
				PlannedItemID:  item.ID,
				UpdatedAt:      item.UpdatedAt,
				AttentionState: startUIPlannedItemAttentionState(item.State),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftRank := startUIAttentionRank(out[i].AttentionState)
		rightRank := startUIAttentionRank(out[j].AttentionState)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func startUIApprovalItemFromRun(run startUIWorkRun) startUIApprovalQueueItem {
	subject := defaultString(runRepoDisplayForApproval(run), run.RunID)
	reason := defaultString(run.CurrentPhase, run.PublicationState)
	nextAction := "Open the run detail and refresh the run when safe."
	actionKind := "open_run"
	externalURL := run.TargetURL
	if strings.TrimSpace(run.TargetURL) != "" {
		nextAction = "Review or approve this on GitHub, then refresh the run here."
		actionKind = "review_on_github"
	}
	detail, err := loadStartUIWorkRunDetail(run.RunID)
	if err == nil {
		if strings.TrimSpace(detail.HumanGateReason) != "" {
			reason = detail.HumanGateReason
		}
		if strings.TrimSpace(detail.NextAction) != "" {
			nextAction = detail.NextAction
		}
		if strings.TrimSpace(detail.ExternalURL) != "" {
			externalURL = detail.ExternalURL
		}
		if detail.SyncAllowed && actionKind == "open_run" {
			actionKind = "sync_run"
		}
	}
	return startUIApprovalQueueItem{
		ID:             "run:" + run.RunID,
		Kind:           "work_run",
		RepoSlug:       run.RepoSlug,
		Subject:        subject,
		Status:         run.Status,
		Reason:         reason,
		NextAction:     nextAction,
		ActionKind:     actionKind,
		ExternalURL:    externalURL,
		TargetURL:      run.TargetURL,
		RunID:          run.RunID,
		UpdatedAt:      run.UpdatedAt,
		AttentionState: "blocked",
	}
}

func startUIPlannedItemAttentionState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case startPlannedItemFailed:
		return "failed"
	case startPlannedItemQueued:
		return "queued"
	default:
		return "completed"
	}
}

func runRepoDisplayForApproval(run startUIWorkRun) string {
	if strings.TrimSpace(run.RepoSlug) != "" && strings.TrimSpace(run.TargetURL) != "" {
		return run.RepoSlug + " -> " + run.TargetURL
	}
	if strings.TrimSpace(run.RepoSlug) != "" {
		return run.RepoSlug
	}
	return run.TargetURL
}

func draftBody(draft *workItemDraft) string {
	if draft == nil {
		return ""
	}
	return strings.TrimSpace(draft.Body)
}

func reviewEvent(draft *workItemDraft) string {
	if draft == nil {
		return ""
	}
	return strings.TrimSpace(draft.ReviewEvent)
}

func suggestedDisposition(draft *workItemDraft) string {
	if draft == nil {
		return ""
	}
	return strings.TrimSpace(draft.SuggestedDisposition)
}

func draftConfidence(draft *workItemDraft) float64 {
	if draft == nil {
		return 0
	}
	return draft.Confidence
}

func startUIAttentionRank(state string) int {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "failed":
		return 0
	case "blocked":
		return 1
	case "active":
		return 2
	case "queued":
		return 3
	case "completed":
		return 4
	default:
		return 5
	}
}

func addStartUIInvestigationDependencies(cwd string, add func(string)) {
	roots, _, err := listStartUIInvestigationRoots(cwd)
	if err != nil {
		return
	}
	for _, root := range roots {
		logRoot := filepath.Join(root, ".nana", "logs", "investigate")
		add(logRoot)
		matches, _ := filepath.Glob(filepath.Join(logRoot, "*", "manifest.json"))
		for _, manifestPath := range matches {
			add(manifestPath)
			var manifest investigateManifest
			if err := readGithubJSON(manifestPath, &manifest); err != nil {
				continue
			}
			if strings.TrimSpace(manifest.FinalReportPath) != "" {
				add(manifest.FinalReportPath)
			}
			for _, round := range manifest.Rounds {
				if strings.TrimSpace(round.ValidatorResultPath) != "" {
					add(round.ValidatorResultPath)
				}
			}
		}
	}
}

func ensureStartUIRepoInvestigationWorkspace(repoSlug string) (string, error) {
	repoPath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
	if info, err := os.Stat(repoPath); err == nil && info.IsDir() {
		return repoPath, nil
	}
	return ensureImproveGithubCheckout(repoSlug)
}

func spawnStartUIIssueInvestigation(repoSlug string, issue startWorkIssueState) (startUIBackgroundLaunch, error) {
	workspaceRoot, err := ensureStartUIRepoInvestigationWorkspace(repoSlug)
	if err != nil {
		return startUIBackgroundLaunch{}, err
	}
	queryParts := []string{
		fmt.Sprintf("Investigate GitHub issue %s#%d", repoSlug, issue.SourceNumber),
	}
	if strings.TrimSpace(issue.Title) != "" {
		queryParts = append(queryParts, "Title: "+strings.TrimSpace(issue.Title))
	}
	if strings.TrimSpace(issue.SourceURL) != "" {
		queryParts = append(queryParts, "URL: "+strings.TrimSpace(issue.SourceURL))
	}
	queryParts = append(queryParts, "Determine root cause, affected code paths, user impact, risks, and the safest next implementation step.")
	return spawnStartUIInvestigateQuery(workspaceRoot, strings.Join(queryParts, "\n"))
}

func spawnStartUIInvestigateQuery(workspaceRoot string, query string) (startUIBackgroundLaunch, error) {
	trimmedRoot := strings.TrimSpace(workspaceRoot)
	if trimmedRoot == "" {
		return startUIBackgroundLaunch{}, fmt.Errorf("investigation workspace root is required")
	}
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return startUIBackgroundLaunch{}, fmt.Errorf("investigation query is required")
	}
	logPath := filepath.Join(trimmedRoot, ".nana", "logs", "investigate-launches", fmt.Sprintf("launch-%d.log", time.Now().UnixNano()))
	if err := startUISpawnBackgroundNana(trimmedRoot, logPath, []string{"investigate", trimmedQuery}); err != nil {
		return startUIBackgroundLaunch{}, err
	}
	return startUIBackgroundLaunch{
		Status:        "spawned",
		Result:        "investigation started; logs at " + logPath,
		LogPath:       logPath,
		WorkspaceRoot: trimmedRoot,
		Query:         trimmedQuery,
	}, nil
}

func launchStartUITrackedIssueWork(repoSlug string, issue startWorkIssueState) (startUIBackgroundLaunch, error) {
	if strings.TrimSpace(issue.SourceURL) == "" {
		return startUIBackgroundLaunch{}, fmt.Errorf("issue #%d does not have a source URL", issue.SourceNumber)
	}
	priority := issue.Priority
	if issue.ManualPriorityUpdatedAt != "" {
		priority = issue.ManualPriority
	}
	state, item, err := createStartUIPlannedItem(repoSlug, startUIPlannedItemRequest{
		Title:       fmt.Sprintf("Implement tracked issue #%d: %s", issue.SourceNumber, strings.TrimSpace(issue.Title)),
		Description: startUITrackedIssuePlannedItemDescription(issue),
		Priority:    &priority,
		LaunchKind:  "tracked_issue",
		TargetURL:   issue.SourceURL,
	})
	if err != nil {
		return startUIBackgroundLaunch{}, err
	}
	_, updatedItem, launch, err := launchStartUIPlannedItemNow(repoSlug, state, item)
	if err != nil {
		return startUIBackgroundLaunch{}, err
	}
	return startUIBackgroundLaunch{
		Status: launch.Status,
		Result: defaultString(updatedItem.LaunchResult, launch.Result),
	}, nil
}

func startUITrackedIssuePlannedItemDescription(issue startWorkIssueState) string {
	lines := []string{
		fmt.Sprintf("Tracked issue: %s", defaultString(strings.TrimSpace(issue.SourceURL), "(missing url)")),
		fmt.Sprintf("Priority: %s", startWorkPriorityLabel(issue.Priority)),
	}
	if strings.TrimSpace(issue.TriageRationale) != "" {
		lines = append(lines, "", "Triage rationale:", strings.TrimSpace(issue.TriageRationale))
	}
	if strings.TrimSpace(issue.DeferredReason) != "" {
		lines = append(lines, "", "Deferred reason:", strings.TrimSpace(issue.DeferredReason))
	}
	if strings.TrimSpace(issue.SourceBody) != "" {
		lines = append(lines, "", "Issue body:", strings.TrimSpace(issue.SourceBody))
	}
	return strings.Join(lines, "\n")
}

func startUISpawnBackgroundNana(workdir string, logPath string, args []string) error {
	executablePath, err := os.Executable()
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
	cmd := exec.Command(executablePath, args...)
	cmd.Dir = workdir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	go func() {
		defer logFile.Close()
		_ = cmd.Wait()
	}()
	return nil
}

func listStartUIRepoSummaries(includeState bool) ([]startUIRepoSummary, error) {
	repoSlugs, err := listStartUIRepoSlugs()
	if err != nil {
		return nil, err
	}
	summaries := make([]startUIRepoSummary, 0, len(repoSlugs))
	for _, repoSlug := range repoSlugs {
		summary, err := loadStartUIRepoSummary(repoSlug, includeState)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			summaries = append(summaries, summary)
		}
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].RepoSlug < summaries[j].RepoSlug
	})
	return summaries, nil
}

func listStartUIRepoSlugs() ([]string, error) {
	seen := map[string]bool{}
	repos, err := listOnboardedGithubRepos()
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		seen[repo] = true
	}
	repoRoot := githubWorkReposRoot()
	_ = filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path != repoRoot {
			rel, err := filepath.Rel(repoRoot, path)
			if err == nil {
				depth := strings.Count(filepath.ToSlash(rel), "/") + 1
				if entry.IsDir() {
					if depth > 2 {
						return filepath.SkipDir
					}
				} else if depth > 3 {
					return nil
				}
			}
		}
		if entry.IsDir() || entry.Name() != "start-state.json" {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, filepath.Dir(path))
		if err == nil {
			repoSlug := filepath.ToSlash(rel)
			if validRepoSlug(repoSlug) {
				seen[repoSlug] = true
			}
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for repo := range seen {
		out = append(out, repo)
	}
	sort.Strings(out)
	return out, nil
}

func loadStartUIRepoSummary(repoSlug string, includeState bool) (startUIRepoSummary, error) {
	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	state, err := readStartWorkState(repoSlug)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return startUIRepoSummary{}, err
	}
	if repoPath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath); repoPath != "" {
		if _, syncedState, syncErr := syncStartWorkScoutJobs(repoPath, repoSlug); syncErr == nil && syncedState != nil {
			state = syncedState
		}
	}
	summary := startUIRepoSummary{
		RepoSlug:          repoSlug,
		SettingsPath:      githubRepoSettingsPath(repoSlug),
		StatePath:         startWorkStatePath(repoSlug),
		SourcePath:        githubManagedPaths(repoSlug).SourcePath,
		ScoutCatalog:      startUIScoutCatalog(),
		ScoutsByRole:      startUIDefaultRepoScoutsByRole(strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)),
		Scouts:            startUIDefaultRepoScouts(strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)),
		IssueCounts:       map[string]int{},
		ServiceTaskCounts: map[string]int{},
		ScoutJobCounts:    map[string]int{},
		PlannedItemCounts: map[string]int{},
	}
	applyStartUIRepoSettings(&summary, settings)
	applyStartUIRepoScouts(&summary)
	if state == nil {
		return summary, nil
	}
	summary.UpdatedAt = state.UpdatedAt
	summary.LastRun = state.LastRun
	summary.DefaultBranch = state.DefaultBranch
	if includeState {
		summary.State = state
	}
	for _, issue := range state.Issues {
		summary.IssueCounts[issue.Status]++
	}
	for _, task := range state.ServiceTasks {
		summary.ServiceTaskCounts[task.Status]++
	}
	for _, job := range state.ScoutJobs {
		summary.ScoutJobCounts[job.Status]++
	}
	for _, item := range state.PlannedItems {
		summary.PlannedItemCounts[item.State]++
	}
	return summary, nil
}

func applyStartUIRepoSettings(summary *startUIRepoSummary, settings *githubRepoSettings) {
	if summary == nil {
		return
	}
	summary.Settings = settings
	summary.RepoMode = resolvedGithubRepoMode(settings)
	summary.IssuePickMode = resolvedGithubIssuePickMode(settings)
	summary.PRForwardMode = resolvedGithubPRForwardMode(settings)
	summary.StartParticipation = githubRepoAutomationEnabled(settings)
	summary.ForkIssuesMode = issuePickModeToAutomationMode(summary.IssuePickMode)
	summary.ImplementMode = issuePickModeToAutomationMode(summary.IssuePickMode)
	if summary.RepoMode == "disabled" {
		summary.PublishTarget = ""
	} else {
		summary.PublishTarget = repoModeToPublishTarget(summary.RepoMode)
	}
	if settings == nil {
		return
	}
	summary.ForkIssuesMode = defaultString(normalizeGithubAutomationMode(settings.ForkIssuesMode), summary.ForkIssuesMode)
	summary.ImplementMode = defaultString(normalizeGithubAutomationMode(settings.ImplementMode), summary.ImplementMode)
	if summary.RepoMode == "disabled" {
		summary.PublishTarget = ""
		return
	}
	summary.PublishTarget = defaultString(normalizeGithubPublishTarget(settings.PublishTarget), summary.PublishTarget)
}

func startUIDefaultRepoScoutConfig(repoPath string, role string) startUIRepoScoutConfig {
	config := startUIRepoScoutConfig{
		Enabled:          false,
		PolicyPath:       startUIRepoScoutPolicyPath(repoPath, role, ""),
		Mode:             "auto",
		Schedule:         scoutScheduleWhenResolved,
		IssueDestination: "local",
		Labels:           normalizeScoutLabels(nil, role),
	}
	if scoutRoleSupportsSessionLimit(role) {
		config.SessionLimit = defaultScoutSessionLimit
	}
	return config
}

func startUIDefaultRepoScouts(repoPath string) startUIRepoScouts {
	return startUIRepoScoutsCompatibility(startUIDefaultRepoScoutsByRole(repoPath))
}

func applyStartUIRepoScouts(summary *startUIRepoSummary) {
	if summary == nil {
		return
	}
	repoPath := strings.TrimSpace(summary.SourcePath)
	summary.ScoutCatalog = startUIScoutCatalog()
	summary.ScoutsByRole = startUIDefaultRepoScoutsByRole(repoPath)
	for _, role := range supportedScoutRoleOrder {
		spec := scoutRoleSpecFor(role)
		summary.ScoutsByRole[spec.ConfigKey] = loadStartUIRepoScoutConfig(repoPath, role)
	}
	summary.Scouts = startUIRepoScoutsCompatibility(summary.ScoutsByRole)
}

func loadStartUIRepoScoutConfig(repoPath string, role string) startUIRepoScoutConfig {
	config := startUIDefaultRepoScoutConfig(repoPath, role)
	canonicalPath := startUIRepoScoutPolicyPath(repoPath, role, "")
	configuredPath := repoScoutConfiguredPath(repoPath, role)
	if !fileExists(canonicalPath) && !fileExists(configuredPath) {
		return config
	}
	config.Enabled = true
	config.PolicyPath = configuredPath
	policy := readScoutPolicy(repoPath, role)
	config.Mode = defaultString(strings.TrimSpace(policy.Mode), "auto")
	config.Schedule = effectiveScoutSchedule(policy)
	config.IssueDestination = startUIScoutDestinationLabel(policy.IssueDestination)
	config.ForkRepo = strings.TrimSpace(policy.ForkRepo)
	config.Labels = append([]string{}, normalizeScoutLabels(policy.Labels, role)...)
	config.SessionLimit = effectiveScoutSessionLimit(policy, role)
	return config
}

func startUIRepoScoutPolicyPath(repoPath string, role string, scope string) string {
	_ = scope
	if strings.TrimSpace(repoPath) == "" {
		return ""
	}
	return repoScoutPolicyPath(repoPath, role, false)
}

func loadStartUIWorkRuns(limit int) ([]startUIWorkRun, error) {
	sourcePathIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return nil, err
	}
	return withLocalWorkReadStore(func(store *localWorkDBStore) ([]startUIWorkRun, error) {
		rows, err := store.db.Query(`SELECT run_id, backend, repo_key, repo_root, repo_name, repo_slug, manifest_path, updated_at, target_kind FROM work_run_index ORDER BY updated_at DESC LIMIT ?`, limit)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		defer rows.Close()
		runs := []startUIWorkRun{}
		for rows.Next() {
			entry, err := scanWorkRunIndexEntry(rows)
			if err != nil {
				return nil, err
			}
			run, err := startUIWorkRunFromIndex(entry, sourcePathIndex)
			if err != nil {
				continue
			}
			runs = append(runs, run)
		}
		return runs, rows.Err()
	})
}

func loadStartUIWorkItems(limit int, includeHidden bool, onlyHidden bool) ([]startUIWorkItem, error) {
	items, _, err := loadStartUIWorkItemsInternal(limit, includeHidden, onlyHidden)
	return items, err
}

func loadStartUIWorkItemsWithHiddenCount(limit int) ([]startUIWorkItem, int, int, error) {
	items, hiddenCount, err := loadStartUIWorkItemsInternal(limit, false, false)
	if err != nil {
		return nil, 0, 0, err
	}
	pendingCount, err := withLocalWorkReadStore(func(store *localWorkDBStore) (int, error) {
		pendingCount := 0
		row := store.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE hidden = 0 AND status IN (?, ?, ?, ?, ?)`,
			workItemStatusQueued,
			workItemStatusRunning,
			workItemStatusDraftReady,
			workItemStatusNeedsRouting,
			workItemStatusFailed,
		)
		return pendingCount, row.Scan(&pendingCount)
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return items, hiddenCount, pendingCount, nil
}

func loadStartUIWorkItemsInternal(limit int, includeHidden bool, onlyHidden bool) ([]startUIWorkItem, int, error) {
	type workItemsResult struct {
		items       []startUIWorkItem
		hiddenCount int
	}
	result, err := withLocalWorkReadStore(func(store *localWorkDBStore) (workItemsResult, error) {
		items, err := store.listWorkItems(workItemListOptions{
			Limit:         limit,
			IncludeHidden: includeHidden,
			OnlyHidden:    onlyHidden,
		})
		if err != nil {
			return workItemsResult{}, err
		}
		hiddenCount := 0
		row := store.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE hidden = 1`)
		if err := row.Scan(&hiddenCount); err != nil {
			return workItemsResult{}, err
		}
		out := make([]startUIWorkItem, 0, len(items))
		for _, item := range items {
			out = append(out, startUIWorkItemFromItem(item))
		}
		return workItemsResult{items: out, hiddenCount: hiddenCount}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return result.items, result.hiddenCount, nil
}

func startUIWorkItemFromItem(item workItem) startUIWorkItem {
	return startUIWorkItem{
		ID:             item.ID,
		Source:         item.Source,
		SourceKind:     item.SourceKind,
		Status:         item.Status,
		RepoSlug:       item.RepoSlug,
		Subject:        item.Subject,
		TargetURL:      item.TargetURL,
		LinkedRunID:    item.LinkedRunID,
		DraftKind:      valueOrEmptyDraftKind(item.LatestDraft),
		DraftSummary:   safeDraftSummary(item.LatestDraft),
		UpdatedAt:      item.UpdatedAt,
		Hidden:         item.Hidden,
		Pending:        startUIWorkItemPending(item),
		AttentionState: startUIWorkItemAttentionState(item),
	}
}

func startUIWorkItemPending(item workItem) bool {
	if item.Hidden {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(item.Status)) {
	case workItemStatusQueued, workItemStatusRunning, workItemStatusDraftReady, workItemStatusNeedsRouting, workItemStatusFailed:
		return true
	default:
		return false
	}
}

func startUIWorkItemAttentionState(item workItem) string {
	status := strings.ToLower(strings.TrimSpace(item.Status))
	switch status {
	case workItemStatusFailed:
		return "failed"
	case workItemStatusNeedsRouting:
		return "blocked"
	case workItemStatusQueued, workItemStatusRunning, workItemStatusDraftReady:
		return "active"
	case workItemStatusSilenced, workItemStatusDropped, workItemStatusSubmitted:
		return "completed"
	default:
		return "queued"
	}
}

func startUIWorkRunFromIndex(entry workRunIndexEntry, sourcePathIndex map[string]string) (startUIWorkRun, error) {
	switch entry.Backend {
	case "local":
		manifest, err := readLocalWorkManifestByRunID(entry.RunID)
		if err != nil {
			return startUIWorkRun{}, err
		}
		repoSlug := startUIResolvedLocalWorkRunRepoSlug(entry, manifest, sourcePathIndex)
		status := strings.TrimSpace(manifest.Status)
		attention := startUIWorkRunAttentionState(status, manifest.LastError, false)
		return startUIWorkRun{
			RunID:            entry.RunID,
			Backend:          entry.Backend,
			RepoKey:          manifest.RepoID,
			RepoName:         manifest.RepoName,
			RepoSlug:         repoSlug,
			RepoLabel:        startUIWorkRunRepoLabel(repoSlug, manifest.RepoName, manifest.RepoRoot),
			Status:           status,
			CurrentPhase:     manifest.CurrentPhase,
			CurrentIteration: manifest.CurrentIteration,
			UpdatedAt:        manifest.UpdatedAt,
			ArtifactPath:     localWorkRunDirByID(manifest.RepoID, manifest.RunID),
			Pending:          startUIWorkRunPending(status, false, manifest.CurrentPhase),
			AttentionState:   attention,
		}, nil
	case "github":
		manifest, err := readGithubWorkManifest(entry.ManifestPath)
		if err != nil {
			return startUIWorkRun{}, err
		}
		status := defaultString(manifest.PublicationState, "active")
		if strings.EqualFold(strings.TrimSpace(manifest.MergeState), "merged") {
			status = "merged"
		}
		phase := defaultString(manifest.NextAction, manifest.TargetKind)
		blocked := manifest.NeedsHuman || strings.Contains(strings.ToLower(phase), "blocked")
		return startUIWorkRun{
			RunID:            entry.RunID,
			Backend:          entry.Backend,
			RepoKey:          manifest.RepoSlug,
			RepoName:         manifest.RepoName,
			RepoSlug:         manifest.RepoSlug,
			RepoLabel:        startUIWorkRunRepoLabel(manifest.RepoSlug, manifest.RepoName, manifest.ManagedRepoRoot),
			Status:           status,
			CurrentPhase:     phase,
			UpdatedAt:        manifest.UpdatedAt,
			TargetKind:       manifest.TargetKind,
			TargetURL:        manifest.TargetURL,
			ArtifactPath:     filepath.Dir(entry.ManifestPath),
			PublicationState: manifest.PublicationState,
			Pending:          startUIWorkRunPending(status, blocked, phase),
			AttentionState:   startUIWorkRunAttentionState(status, defaultString(manifest.PublicationError, manifest.NeedsHumanReason), blocked),
		}, nil
	default:
		return startUIWorkRun{}, fmt.Errorf("unsupported backend %q", entry.Backend)
	}
}

func loadStartUIWorkRunDetail(runID string) (startUIWorkRunDetail, error) {
	entry, err := readWorkRunIndex(runID)
	if err != nil {
		return startUIWorkRunDetail{}, err
	}
	sourcePathIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return startUIWorkRunDetail{}, err
	}
	summary, err := startUIWorkRunFromIndex(entry, sourcePathIndex)
	if err != nil {
		return startUIWorkRunDetail{}, err
	}
	switch entry.Backend {
	case "local":
		manifest, err := readLocalWorkManifestByRunID(runID)
		if err != nil {
			return startUIWorkRunDetail{}, err
		}
		nextAction := defaultString(strings.TrimSpace(manifest.CurrentPhase), "inspect local work state")
		return startUIWorkRunDetail{
			Summary:       summary,
			Backend:       entry.Backend,
			LocalManifest: &manifest,
			NextAction:    nextAction,
			ExternalURL:   summary.TargetURL,
		}, nil
	case "github":
		manifest, err := readGithubWorkManifest(entry.ManifestPath)
		if err != nil {
			return startUIWorkRunDetail{}, err
		}
		status, err := buildGithubWorkStatusSnapshot(manifest, filepath.Dir(entry.ManifestPath))
		if err != nil {
			return startUIWorkRunDetail{}, err
		}
		return startUIWorkRunDetail{
			Summary:         summary,
			Backend:         entry.Backend,
			GithubManifest:  &manifest,
			GithubStatus:    &status,
			NextAction:      defaultString(strings.TrimSpace(manifest.NextAction), "inspect GitHub feedback and publication state"),
			HumanGateReason: defaultString(strings.TrimSpace(manifest.NeedsHumanReason), defaultString(strings.TrimSpace(manifest.PublicationError), strings.TrimSpace(manifest.PublicationDetail))),
			SyncAllowed:     true,
			ExternalURL:     githubWorkRunExternalURL(manifest),
		}, nil
	default:
		return startUIWorkRunDetail{}, fmt.Errorf("unsupported backend %q", entry.Backend)
	}
}

func githubWorkRunExternalURL(manifest githubWorkManifest) string {
	if strings.TrimSpace(manifest.PublishedPRURL) != "" {
		return strings.TrimSpace(manifest.PublishedPRURL)
	}
	return strings.TrimSpace(manifest.TargetURL)
}

func loadStartUIWorkRunLogs(runID string) (startUIWorkRunLogsResponse, error) {
	response, _, err := loadStartUIWorkRunLogSet(runID)
	return response, err
}

func patchStartUIRepoSettings(repoSlug string, payload startUIRepoSettingsPatchRequest) (startUIRepoSummary, error) {
	repoMode, err := parseGithubRepoMode(payload.RepoMode, "repo_mode")
	if err != nil {
		return startUIRepoSummary{}, err
	}
	issuePickMode, err := parseGithubIssuePickMode(payload.IssuePickMode, "issue_pick_mode")
	if err != nil {
		return startUIRepoSummary{}, err
	}
	prForwardMode, err := parseGithubPRForwardMode(payload.PRForwardMode, "pr_forward_mode")
	if err != nil {
		return startUIRepoSummary{}, err
	}
	forkIssuesMode, err := parseGithubAutomationMode(payload.ForkIssuesMode, "fork_issues_mode")
	if err != nil {
		return startUIRepoSummary{}, err
	}
	implementMode, err := parseGithubAutomationMode(payload.ImplementMode, "implement_mode")
	if err != nil {
		return startUIRepoSummary{}, err
	}
	publishTarget := ""
	if repoMode != "disabled" {
		publishTargetValue := strings.TrimSpace(payload.PublishTarget)
		if publishTargetValue == "" {
			publishTargetValue = repoModeToPublishTarget(repoMode)
		}
		publishTarget, err = parseGithubPublishTarget(publishTargetValue, "publish_target")
		if err != nil {
			return startUIRepoSummary{}, err
		}
	}
	scoutPlans, err := buildStartUIRepoScoutWritePlans(repoSlug, payload.Scouts)
	if err != nil {
		return startUIRepoSummary{}, err
	}

	settingsPath := githubRepoSettingsPath(repoSlug)
	existing, readErr := readGithubRepoSettings(settingsPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return startUIRepoSummary{}, readErr
	}
	if existing == nil {
		existing = &githubRepoSettings{}
	}
	updated := *existing
	updated.Version = 6
	updated.RepoMode = repoMode
	updated.IssuePickMode = issuePickMode
	updated.PRForwardMode = prForwardMode
	updated.ForkIssuesMode = forkIssuesMode
	updated.ImplementMode = implementMode
	updated.PublishTarget = normalizeGithubPublishTarget(publishTarget)
	updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if repoMode == "disabled" {
		updated.PublishTarget = ""
	}
	if err := applyStartUIRepoScoutWritePlans(scoutPlans); err != nil {
		return startUIRepoSummary{}, err
	}
	if err := writeGithubJSON(settingsPath, updated); err != nil {
		return startUIRepoSummary{}, err
	}
	return loadStartUIRepoSummary(repoSlug, true)
}

type startUIRepoScoutWritePlan struct {
	Enabled     bool
	Role        string
	PolicyPath  string
	LegacyPaths []string
	Policy      scoutPolicy
}

func buildStartUIRepoScoutWritePlans(repoSlug string, patch *startUIRepoScoutsPatchRequest) ([]startUIRepoScoutWritePlan, error) {
	if patch == nil {
		return nil, nil
	}
	repoPath, err := ensureStartUIRepoSourceCheckout(repoSlug)
	if err != nil {
		return nil, err
	}
	plans := []startUIRepoScoutWritePlan{}
	for _, role := range supportedScoutRoleOrder {
		request := (*startUIRepoScoutConfig)(nil)
		spec := scoutRoleSpecFor(role)
		if patch.ScoutsByRole != nil {
			request = patch.ScoutsByRole[spec.ConfigKey]
		}
		if request == nil {
			request = startUIGetRepoScoutPatch(patch, role)
		}
		if request == nil {
			continue
		}
		plan, err := buildStartUIRepoScoutWritePlan(repoPath, role, request)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func ensureStartUIRepoSourceCheckout(repoSlug string) (string, error) {
	repoPath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
	if repoPath == "" {
		return "", fmt.Errorf("repo %s does not have a managed source checkout", repoSlug)
	}
	info, err := os.Stat(repoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("repo %s does not have a managed source checkout", repoSlug)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repo %s source checkout is not a directory", repoSlug)
	}
	return repoPath, nil
}

func buildStartUIRepoScoutWritePlan(repoPath string, role string, request *startUIRepoScoutConfig) (startUIRepoScoutWritePlan, error) {
	if request == nil {
		return startUIRepoScoutWritePlan{}, fmt.Errorf("missing %s config", role)
	}
	if !request.Enabled {
		return startUIRepoScoutWritePlan{
			Enabled:     false,
			Role:        role,
			PolicyPath:  startUIRepoScoutPolicyPath(repoPath, role, ""),
			LegacyPaths: repoScoutLegacyPolicyPaths(repoPath, role),
		}, nil
	}
	mode, err := parseStartUIRepoScoutMode(request.Mode)
	if err != nil {
		return startUIRepoScoutWritePlan{}, err
	}
	schedule, err := parseStartUIRepoScoutSchedule(request.Schedule)
	if err != nil {
		return startUIRepoScoutWritePlan{}, err
	}
	destination, err := parseStartUIRepoScoutDestination(request.IssueDestination)
	if err != nil {
		return startUIRepoScoutWritePlan{}, err
	}
	forkRepo := strings.TrimSpace(request.ForkRepo)
	if destination == improvementDestinationFork && !validRepoSlug(forkRepo) {
		return startUIRepoScoutWritePlan{}, fmt.Errorf("%s fork_repo must be owner/repo when destination is fork", role)
	}
	if destination != improvementDestinationFork {
		forkRepo = ""
	}
	policy := scoutPolicy{
		Version:          1,
		Mode:             mode,
		Schedule:         schedule,
		IssueDestination: destination,
		ForkRepo:         forkRepo,
		Labels:           normalizeScoutLabels(request.Labels, role),
	}
	if scoutRoleSupportsSessionLimit(role) {
		sessionLimit, err := parseStartUIRepoScoutSessionLimit(request.SessionLimit)
		if err != nil {
			return startUIRepoScoutWritePlan{}, err
		}
		policy.SessionLimit = sessionLimit
	}
	return startUIRepoScoutWritePlan{
		Enabled:     true,
		Role:        role,
		PolicyPath:  startUIRepoScoutPolicyPath(repoPath, role, ""),
		LegacyPaths: repoScoutLegacyPolicyPaths(repoPath, role),
		Policy:      policy,
	}, nil
}

func parseStartUIRepoScoutMode(value string) (string, error) {
	normalized, err := parseRepoScoutMode(defaultString(value, "auto"))
	if err != nil {
		return "", fmt.Errorf("invalid scout mode %q; expected auto or manual", value)
	}
	return normalized, nil
}

func parseStartUIRepoScoutSchedule(value string) (string, error) {
	normalized, err := parseRepoScoutSchedule(defaultString(value, scoutScheduleWhenResolved))
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func parseStartUIRepoScoutDestination(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(defaultString(value, "local"))) {
	case "local":
		return improvementDestinationLocal, nil
	case "repo", "target":
		return improvementDestinationTarget, nil
	case "fork":
		return improvementDestinationFork, nil
	default:
		return "", fmt.Errorf("invalid scout destination %q; expected local, repo, or fork", value)
	}
}

func parseStartUIRepoScoutSessionLimit(value int) (int, error) {
	if value <= 0 {
		return defaultScoutSessionLimit, nil
	}
	if value > maxScoutSessionLimit {
		return 0, fmt.Errorf("invalid scout session_limit %d; expected 1-%d", value, maxScoutSessionLimit)
	}
	return value, nil
}

func applyStartUIRepoScoutWritePlans(plans []startUIRepoScoutWritePlan) error {
	if len(plans) == 0 {
		return nil
	}
	for _, plan := range plans {
		if !plan.Enabled {
			if _, err := removePathIfExists(plan.PolicyPath); err != nil {
				return err
			}
			for _, legacyPath := range plan.LegacyPaths {
				if _, err := removePathIfExists(legacyPath); err != nil {
					return err
				}
			}
			continue
		}
		if err := writeGithubJSON(plan.PolicyPath, plan.Policy); err != nil {
			return err
		}
		for _, legacyPath := range plan.LegacyPaths {
			if _, err := removePathIfExists(legacyPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func loadStartUIScoutItems(repoSlug string) (startUIScoutItemsResponse, error) {
	summary, err := loadStartUIRepoSummary(repoSlug, true)
	if err != nil {
		return startUIScoutItemsResponse{}, err
	}
	items, err := listStartUIScoutItems(repoSlug)
	if err != nil {
		return startUIScoutItemsResponse{}, err
	}
	return startUIScoutItemsResponse{Repo: summary, ScoutCatalog: startUIScoutCatalog(), Items: items}, nil
}

func listStartUIScoutItems(repoSlug string) ([]startUIScoutItem, error) {
	repoPath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
	if repoPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(repoPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	_, workState, err := syncStartWorkScoutJobs(repoPath, repoSlug)
	if err != nil {
		return nil, err
	}
	items := []startUIScoutItem{}
	for _, role := range supportedScoutRoleOrder {
		matches, err := filepath.Glob(filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), "*", "proposals.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, proposalPath := range matches {
			var report scoutReport
			if err := readGithubJSON(proposalPath, &report); err != nil {
				continue
			}
			artifactDir := filepath.Dir(proposalPath)
			relArtifactDir, _ := filepath.Rel(repoPath, artifactDir)
			relArtifactDir = filepath.ToSlash(relArtifactDir)
			policyPath := filepath.Join(artifactDir, "policy.json")
			policy := scoutPolicy{Version: 1, IssueDestination: improvementDestinationLocal}
			_ = readGithubJSON(policyPath, &policy)
			policy.IssueDestination = normalizeScoutDestination(policy.IssueDestination)
			relPolicyPath := ""
			if _, err := os.Stat(policyPath); err == nil {
				relPolicyPath, _ = filepath.Rel(repoPath, policyPath)
				relPolicyPath = filepath.ToSlash(relPolicyPath)
			}
			relProposalPath, _ := filepath.Rel(repoPath, proposalPath)
			relProposalPath = filepath.ToSlash(relProposalPath)
			preflightPath := filepath.Join(artifactDir, "preflight.json")
			issueDraftPath := filepath.Join(artifactDir, "issue-drafts.md")
			rawOutputPath := filepath.Join(artifactDir, "raw-output.txt")
			var preflight uiScoutPreflight
			relPreflightPath := ""
			relIssueDraftPath := ""
			relRawOutputPath := ""
			if _, err := os.Stat(preflightPath); err == nil {
				relPreflightPath, _ = filepath.Rel(repoPath, preflightPath)
				relPreflightPath = filepath.ToSlash(relPreflightPath)
				_ = readGithubJSON(preflightPath, &preflight)
			}
			if _, err := os.Stat(issueDraftPath); err == nil {
				relIssueDraftPath, _ = filepath.Rel(repoPath, issueDraftPath)
				relIssueDraftPath = filepath.ToSlash(relIssueDraftPath)
			}
			if _, err := os.Stat(rawOutputPath); err == nil {
				relRawOutputPath, _ = filepath.Rel(repoPath, rawOutputPath)
				relRawOutputPath = filepath.ToSlash(relRawOutputPath)
			}
			for _, proposal := range report.Proposals {
				title := strings.TrimSpace(proposal.Title)
				summary := strings.TrimSpace(proposal.Summary)
				if title == "" || summary == "" {
					continue
				}
				itemID := localScoutProposalID(role, proposal)
				discovered := localScoutDiscoveredItem{
					ID:              itemID,
					Role:            role,
					Title:           title,
					Artifact:        relArtifactDir,
					Proposal:        proposal,
					ProposalPath:    relProposalPath,
					PolicyPath:      relPolicyPath,
					PreflightPath:   relPreflightPath,
					IssueDraftPath:  relIssueDraftPath,
					RawOutputPath:   relRawOutputPath,
					GeneratedAt:     strings.TrimSpace(report.GeneratedAt),
					AuditMode:       strings.TrimSpace(preflight.Mode),
					SurfaceKind:     strings.TrimSpace(preflight.SurfaceKind),
					SurfaceTarget:   strings.TrimSpace(preflight.SurfaceTarget),
					BrowserReady:    preflight.BrowserReady,
					PreflightReason: strings.TrimSpace(preflight.Reason),
					Destination:     startUIScoutDestinationLabel(policy.IssueDestination),
					ForkRepo:        strings.TrimSpace(policy.ForkRepo),
				}
				scoutItem := startUIScoutItemFromDiscovered(discovered)
				if workState != nil && scoutItem.Destination == improvementDestinationLocal {
					if job, ok := workState.ScoutJobs[itemID]; ok {
						scoutItem = startWorkScoutJobFromItem(job)
					}
				}
				scoutItem.AvailableActions = startUIScoutAvailableActions(scoutItem)
				items = append(items, scoutItem)
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		leftRank := startUIScoutStatusRank(items[i].Status)
		rightRank := startUIScoutStatusRank(items[j].Status)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftTime := items[i].UpdatedAt
		if strings.TrimSpace(leftTime) == "" {
			leftTime = items[i].GeneratedAt
		}
		rightTime := items[j].UpdatedAt
		if strings.TrimSpace(rightTime) == "" {
			rightTime = items[j].GeneratedAt
		}
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		if items[i].ArtifactPath != items[j].ArtifactPath {
			return items[i].ArtifactPath < items[j].ArtifactPath
		}
		return items[i].Title < items[j].Title
	})
	return items, nil
}

func mutateStartUIScoutItem(repoSlug string, itemID string, action string) (startUIScoutItemsResponse, error) {
	repoPath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
	if repoPath == "" {
		return startUIScoutItemsResponse{}, fmt.Errorf("repo %s does not have a managed source checkout", repoSlug)
	}
	items, err := listStartUIScoutItems(repoSlug)
	if err != nil {
		return startUIScoutItemsResponse{}, err
	}
	var selected *startUIScoutItem
	for index := range items {
		if items[index].ID == itemID {
			selected = &items[index]
			break
		}
	}
	if selected == nil {
		return startUIScoutItemsResponse{}, fmt.Errorf("scout item %s was not found", itemID)
	}
	if selected.Destination == improvementDestinationLocal {
		switch action {
		case "dismiss", "retry", "reset":
			if _, _, err := mutateStartWorkScoutJob(repoSlug, itemID, action); err != nil {
				return startUIScoutItemsResponse{}, err
			}
			return loadStartUIScoutItems(repoSlug)
		case "queue-planned":
			return startUIScoutItemsResponse{}, fmt.Errorf("local scout jobs no longer support queue-planned; use retry or dismiss")
		}
	}
	state, statePath, err := readLocalScoutPickupState(repoPath)
	if err != nil {
		return startUIScoutItemsResponse{}, err
	}
	switch action {
	case "dismiss":
		if selected.Status == "running" {
			return startUIScoutItemsResponse{}, fmt.Errorf("running scout items cannot be dismissed")
		}
		state.Items[itemID] = localScoutPickupItem{
			Status:     "dismissed",
			Title:      selected.Title,
			Artifact:   selected.ArtifactPath,
			UpdatedAt:  ISOTimeNow(),
			ProposalID: selected.ID,
		}
		if err := writeLocalScoutPickupState(statePath, state); err != nil {
			return startUIScoutItemsResponse{}, err
		}
	case "reset":
		if selected.Status == "pending" || selected.Status == "external" {
			return startUIScoutItemsResponse{}, fmt.Errorf("scout item %s is already pending", itemID)
		}
		delete(state.Items, itemID)
		if err := writeLocalScoutPickupState(statePath, state); err != nil {
			return startUIScoutItemsResponse{}, err
		}
	case "queue-planned":
		if selected.Destination != improvementDestinationLocal {
			return startUIScoutItemsResponse{}, fmt.Errorf("only local scout items can be queued as planned work")
		}
		switch selected.Status {
		case "running", "completed", "planned":
			return startUIScoutItemsResponse{}, fmt.Errorf("scout item %s cannot be queued from status %s", itemID, selected.Status)
		}
		settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
		if resolvedGithubRepoMode(settings) == "disabled" {
			return startUIScoutItemsResponse{}, fmt.Errorf("repo %s is configured with repo-mode disabled; update the repo config before queueing scout work", repoSlug)
		}
		priority := 3
		_, plannedItem, err := createStartUIPlannedItem(repoSlug, startUIPlannedItemRequest{
			Title:       startUIScoutPlannedItemTitle(*selected),
			Description: startUIScoutPlannedItemDescription(*selected),
			Priority:    &priority,
			LaunchKind:  "local_work",
		})
		if err != nil {
			return startUIScoutItemsResponse{}, err
		}
		state.Items[itemID] = localScoutPickupItem{
			Status:        "in_progress",
			Title:         selected.Title,
			Artifact:      selected.ArtifactPath,
			PlannedItemID: plannedItem.ID,
			UpdatedAt:     ISOTimeNow(),
			ProposalID:    selected.ID,
		}
		if err := writeLocalScoutPickupState(statePath, state); err != nil {
			return startUIScoutItemsResponse{}, err
		}
	default:
		return startUIScoutItemsResponse{}, fmt.Errorf("unsupported scout item action %q", action)
	}
	return loadStartUIScoutItems(repoSlug)
}

func mutateStartUIScoutItems(repoSlug string, itemIDs []string, action string) (startUIScoutItemsResponse, error) {
	normalizedAction := strings.TrimSpace(action)
	if normalizedAction == "" {
		return startUIScoutItemsResponse{}, fmt.Errorf("scout batch action is required")
	}
	uniqueIDs := make([]string, 0, len(itemIDs))
	seen := make(map[string]struct{}, len(itemIDs))
	for _, itemID := range itemIDs {
		trimmed := strings.TrimSpace(itemID)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		uniqueIDs = append(uniqueIDs, trimmed)
	}
	if len(uniqueIDs) == 0 {
		return startUIScoutItemsResponse{}, fmt.Errorf("at least one scout item is required")
	}

	results := make([]startUIScoutActionResult, 0, len(uniqueIDs))
	successCount := 0
	failureCount := 0
	var lastPayload startUIScoutItemsResponse
	loaded := false
	for _, itemID := range uniqueIDs {
		payload, err := mutateStartUIScoutItem(repoSlug, itemID, normalizedAction)
		if err != nil {
			results = append(results, startUIScoutActionResult{
				ItemID: itemID,
				Status: "error",
				Error:  err.Error(),
			})
			failureCount++
			continue
		}
		lastPayload = payload
		loaded = true
		results = append(results, startUIScoutActionResult{
			ItemID: itemID,
			Status: "ok",
		})
		successCount++
	}
	if !loaded {
		payload, err := loadStartUIScoutItems(repoSlug)
		if err != nil {
			return startUIScoutItemsResponse{}, err
		}
		lastPayload = payload
	}
	lastPayload.Action = normalizedAction
	lastPayload.Results = results
	lastPayload.SuccessCount = successCount
	lastPayload.FailureCount = failureCount
	return lastPayload, nil
}

func loadStartUIWorkRunLogContent(runID string, relativePath string, tail int) (map[string]any, error) {
	response, filesByPath, err := loadStartUIWorkRunLogSet(runID)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(relativePath)
	if path == "" {
		path = response.DefaultPath
	}
	absolutePath, ok := filesByPath[path]
	if !ok {
		return nil, fmt.Errorf("log path %q was not found for run %s", path, runID)
	}
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return nil, err
	}
	display := string(content)
	if tail > 0 {
		display = tailLines(display, tail)
	}
	return map[string]any{
		"summary":       response.Summary,
		"artifact_root": response.ArtifactRoot,
		"path":          path,
		"tail_lines":    tail,
		"content":       display,
	}, nil
}

func loadStartUIWorkRunLogSet(runID string) (startUIWorkRunLogsResponse, map[string]string, error) {
	entry, err := readWorkRunIndex(runID)
	if err != nil {
		return startUIWorkRunLogsResponse{}, nil, err
	}
	sourcePathIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return startUIWorkRunLogsResponse{}, nil, err
	}
	summary, err := startUIWorkRunFromIndex(entry, sourcePathIndex)
	if err != nil {
		return startUIWorkRunLogsResponse{}, nil, err
	}
	artifactRoot, files, err := startUIWorkRunLogFiles(entry)
	if err != nil {
		return startUIWorkRunLogsResponse{}, nil, err
	}
	filesByPath := make(map[string]string, len(files))
	metadata := make([]startUIWorkRunLogFile, 0, len(files))
	for _, absolutePath := range files {
		relativePath, err := filepath.Rel(artifactRoot, absolutePath)
		if err != nil {
			return startUIWorkRunLogsResponse{}, nil, err
		}
		relativePath = filepath.ToSlash(relativePath)
		if strings.HasPrefix(relativePath, "../") || relativePath == ".." {
			return startUIWorkRunLogsResponse{}, nil, fmt.Errorf("log path %s escapes artifact root", relativePath)
		}
		info, err := os.Stat(absolutePath)
		if err != nil {
			return startUIWorkRunLogsResponse{}, nil, err
		}
		filesByPath[relativePath] = absolutePath
		metadata = append(metadata, startUIWorkRunLogFile{
			Path:      relativePath,
			Name:      filepath.Base(absolutePath),
			SizeBytes: info.Size(),
			UpdatedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return startUIWorkRunLogsResponse{
		Summary:      summary,
		ArtifactRoot: artifactRoot,
		DefaultPath:  startUIDefaultLogPath(metadata),
		Files:        metadata,
	}, filesByPath, nil
}

func startUIWorkRunLogFiles(entry workRunIndexEntry) (string, []string, error) {
	switch entry.Backend {
	case "local":
		manifest, err := readLocalWorkManifestByRunID(entry.RunID)
		if err != nil {
			return "", nil, err
		}
		iteration := manifest.CurrentIteration
		if iteration <= 0 && len(manifest.Iterations) > 0 {
			iteration = manifest.Iterations[len(manifest.Iterations)-1].Iteration
		}
		if iteration <= 0 {
			return "", nil, fmt.Errorf("work run %s has no iteration artifacts yet", manifest.RunID)
		}
		runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
		artifactRoot := localWorkIterationDir(runDir, iteration)
		files, err := localWorkLogFiles(artifactRoot)
		if err != nil {
			return "", nil, err
		}
		return artifactRoot, files, nil
	case "github":
		artifactRoot := filepath.Dir(entry.ManifestPath)
		files, err := githubWorkLogFiles(artifactRoot)
		if err != nil {
			return "", nil, err
		}
		return artifactRoot, files, nil
	default:
		return "", nil, fmt.Errorf("unsupported backend %q", entry.Backend)
	}
}

func startUIDefaultLogPath(files []startUIWorkRunLogFile) string {
	if len(files) == 0 {
		return ""
	}
	preferred := []string{
		"implement-stdout.log",
		"review-stdout.log",
		"stdout.log",
		"result.md",
		"manifest.json",
	}
	for _, candidate := range preferred {
		for _, file := range files {
			if file.Name == candidate || strings.HasSuffix(file.Path, candidate) {
				return file.Path
			}
		}
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name, "-stdout.log") || strings.HasSuffix(file.Path, "-stdout.log") {
			return file.Path
		}
	}
	return files[0].Path
}

func startUIParseTailLines(value string) int {
	tail := 200
	if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed > 0 {
		tail = parsed
	}
	if tail > 2000 {
		tail = 2000
	}
	return tail
}

func listStartUIRepoSourcePathIndex() (map[string]string, error) {
	repoSlugs, err := listStartUIRepoSlugs()
	if err != nil {
		return nil, err
	}
	index := make(map[string]string, len(repoSlugs))
	for _, repoSlug := range repoSlugs {
		sourcePath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
		if sourcePath == "" {
			continue
		}
		index[filepath.Clean(sourcePath)] = repoSlug
	}
	return index, nil
}

func startUIRepoSlugForRoot(repoRoot string, sourcePathIndex map[string]string) string {
	if len(sourcePathIndex) == 0 || strings.TrimSpace(repoRoot) == "" {
		return ""
	}
	return sourcePathIndex[filepath.Clean(repoRoot)]
}

func startUIResolvedLocalWorkRunRepoSlug(entry workRunIndexEntry, manifest localWorkManifest, sourcePathIndex map[string]string) string {
	if repoSlug := strings.TrimSpace(manifest.RepoSlug); validRepoSlug(repoSlug) {
		return repoSlug
	}
	if repoSlug := strings.TrimSpace(entry.RepoSlug); validRepoSlug(repoSlug) {
		return repoSlug
	}
	if repoSlug := strings.TrimSpace(startUIRepoSlugForRoot(manifest.RepoRoot, sourcePathIndex)); validRepoSlug(repoSlug) {
		return repoSlug
	}
	if repoSlug := strings.TrimSpace(localWorkResolvedRepoSlug(manifest.RepoRoot, "")); validRepoSlug(repoSlug) {
		return repoSlug
	}
	return ""
}

func startUIWorkRunRepoLabel(repoSlug string, repoName string, repoRoot string) string {
	if strings.TrimSpace(repoSlug) != "" {
		return strings.TrimSpace(repoSlug)
	}
	if strings.TrimSpace(repoName) != "" {
		return strings.TrimSpace(repoName)
	}
	if strings.TrimSpace(repoRoot) != "" {
		return filepath.Base(repoRoot)
	}
	return "Standalone"
}

func startUIWorkRunPending(status string, blocked bool, phase string) bool {
	normalizedStatus := strings.ToLower(strings.TrimSpace(status))
	if normalizedStatus == "" {
		return true
	}
	if blocked {
		return true
	}
	switch normalizedStatus {
	case "completed", "success", "succeeded", "merged", "closed", "done", "no-op":
		return false
	}
	normalizedPhase := strings.ToLower(strings.TrimSpace(phase))
	return normalizedPhase != "completed" && normalizedPhase != "done"
}

func startUIWorkRunAttentionState(status string, detail string, blocked bool) string {
	normalizedStatus := strings.ToLower(strings.TrimSpace(status))
	normalizedDetail := strings.ToLower(strings.TrimSpace(detail))
	switch {
	case strings.Contains(normalizedStatus, "fail"), strings.Contains(normalizedStatus, "error"), strings.Contains(normalizedDetail, "fail"), strings.Contains(normalizedDetail, "error"):
		return "failed"
	case blocked, strings.Contains(normalizedStatus, "block"), strings.Contains(normalizedStatus, "human"), strings.Contains(normalizedDetail, "block"):
		return "blocked"
	case strings.Contains(normalizedStatus, "queue"), strings.Contains(normalizedStatus, "pending"), strings.Contains(normalizedStatus, "wait"):
		return "queued"
	case !startUIWorkRunPending(status, blocked, ""):
		return "completed"
	default:
		return "active"
	}
}

func patchStartUIIssue(repoSlug string, issueNumber int, payload startUIIssuePatchRequest) (*startWorkState, startWorkIssueState, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	startWorkStateFileMu.Lock()
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		startWorkStateFileMu.Unlock()
		return nil, startWorkIssueState{}, err
	}
	key := strconv.Itoa(issueNumber)
	issue, ok := state.Issues[key]
	if !ok {
		startWorkStateFileMu.Unlock()
		return nil, startWorkIssueState{}, fmt.Errorf("issue #%d is not tracked in start state", issueNumber)
	}
	if payload.Priority != nil {
		if *payload.Priority < 0 || *payload.Priority > 5 {
			startWorkStateFileMu.Unlock()
			return nil, startWorkIssueState{}, fmt.Errorf("priority must be between P0 and P5")
		}
		issue.ManualPriority = *payload.Priority
		issue.ManualPriorityUpdatedAt = now
		issue.Priority = *payload.Priority
		issue.PrioritySource = "manual_override"
		issue.TriageStatus = startWorkTriageCompleted
		issue.TriageRationale = "manual override " + startWorkPriorityLabel(*payload.Priority)
		issue.TriageFingerprint = issue.SourceFingerprint
		issue.TriageUpdatedAt = now
	}
	if payload.ClearSchedule {
		issue.ScheduleAt = ""
		issue.ScheduleUpdatedAt = now
		issue.DeferredReason = ""
	}
	if payload.ScheduleAt != nil {
		value := strings.TrimSpace(*payload.ScheduleAt)
		if value != "" {
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				startWorkStateFileMu.Unlock()
				return nil, startWorkIssueState{}, fmt.Errorf("schedule_at must be RFC3339")
			}
		}
		issue.ScheduleAt = value
		issue.ScheduleUpdatedAt = now
	}
	if payload.DeferredReason != nil {
		issue.DeferredReason = strings.TrimSpace(*payload.DeferredReason)
	}
	issue.UpdatedAt = now
	state.Issues[key] = issue
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		startWorkStateFileMu.Unlock()
		return nil, startWorkIssueState{}, err
	}
	startWorkStateFileMu.Unlock()
	if payload.Priority != nil {
		if nextLabels, mirrorErr := mirrorStartWorkIssuePriority(repoSlug, issue.SourceNumber, issue.Labels, *payload.Priority); mirrorErr == nil && len(nextLabels) > 0 {
			startWorkStateFileMu.Lock()
			defer startWorkStateFileMu.Unlock()
			refreshedState, readErr := readStartWorkStateUnlocked(repoSlug)
			if readErr == nil {
				refreshedIssue := refreshedState.Issues[key]
				refreshedIssue.Labels = nextLabels
				refreshedIssue.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				refreshedState.Issues[key] = refreshedIssue
				refreshedState.UpdatedAt = refreshedIssue.UpdatedAt
				_ = writeStartWorkStateUnlocked(*refreshedState)
				state = refreshedState
				issue = refreshedIssue
			}
		}
	}
	return state, issue, nil
}

func searchStartUIRepoIssues(repoSlug string, rawQuery string) (startUIIssueSearchResponse, error) {
	trimmedQuery := strings.TrimSpace(rawQuery)
	if err := validateStartUIIssueSearchQuery(trimmedQuery); err != nil {
		return startUIIssueSearchResponse{}, err
	}
	apiBaseURL := defaultGithubAPIBaseURL()
	token, err := resolveGithubToken()
	if err != nil {
		return startUIIssueSearchResponse{}, err
	}
	queryParts := []string{
		"repo:" + repoSlug,
		"is:issue",
		"is:open",
	}
	if trimmedQuery != "" {
		queryParts = append(queryParts, trimmedQuery)
	}
	effectiveQuery := strings.Join(queryParts, " ")
	path := fmt.Sprintf("/search/issues?q=%s&sort=updated&order=desc&per_page=50", url.QueryEscape(effectiveQuery))
	var payload startUIIssueSearchPayload
	if err := githubAPIGetJSON(apiBaseURL, token, path, &payload); err != nil {
		return startUIIssueSearchResponse{}, err
	}
	state, err := readStartWorkState(repoSlug)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return startUIIssueSearchResponse{}, err
	}
	results := make([]startUIIssueSearchResult, 0, len(payload.Items))
	for _, item := range payload.Items {
		if item.PullRequest != nil || strings.TrimSpace(item.State) != "open" {
			continue
		}
		labels := startWorkIssueLabelNames(item.Labels)
		priority, ok := startWorkManualPriority(labels)
		if !ok {
			priority = 3
		}
		result := startUIIssueSearchResult{
			ID:            fmt.Sprintf("%s#%d", repoSlug, item.Number),
			Number:        item.Number,
			Title:         strings.TrimSpace(item.Title),
			TargetURL:     strings.TrimSpace(item.HTMLURL),
			Labels:        labels,
			Priority:      priority,
			PriorityLabel: startWorkPriorityLabel(priority),
			UpdatedAt:     strings.TrimSpace(item.UpdatedAt),
		}
		if tracked, found := startUITrackedIssuePlannedItemForURL(state, result.TargetURL); found {
			result.Scheduled = true
			result.PlannedItemID = tracked.ID
			result.PlannedState = tracked.State
			result.ScheduleAt = tracked.ScheduleAt
		}
		results = append(results, result)
	}
	return startUIIssueSearchResponse{
		Query:          trimmedQuery,
		EffectiveQuery: effectiveQuery,
		Items:          results,
	}, nil
}

func validateStartUIIssueSearchQuery(query string) error {
	for _, token := range strings.Fields(strings.ToLower(query)) {
		switch {
		case strings.HasPrefix(token, "repo:"):
			return fmt.Errorf("repo qualifiers are not allowed here; the search is already scoped to the selected repo")
		case strings.HasPrefix(token, "org:"), strings.HasPrefix(token, "user:"):
			return fmt.Errorf("org and user qualifiers are not allowed here; search stays inside the selected repo")
		case token == "is:pr", token == "is:pull-request", token == "is:pullrequest":
			return fmt.Errorf("pull request search is not supported here; only GitHub issues can be scheduled")
		case token == "is:closed", token == "state:closed", token == "is:merged":
			return fmt.Errorf("closed issues are not supported here; only open GitHub issues can be scheduled")
		}
	}
	return nil
}

func startUITrackedIssuePlannedItemForURL(state *startWorkState, targetURL string) (startWorkPlannedItem, bool) {
	if state == nil || strings.TrimSpace(targetURL) == "" {
		return startWorkPlannedItem{}, false
	}
	var best startWorkPlannedItem
	found := false
	for _, item := range state.PlannedItems {
		if strings.TrimSpace(item.LaunchKind) != "tracked_issue" || strings.TrimSpace(item.TargetURL) != strings.TrimSpace(targetURL) {
			continue
		}
		if strings.TrimSpace(item.State) != startPlannedItemQueued && strings.TrimSpace(item.State) != startPlannedItemFailed {
			continue
		}
		if !found || strings.TrimSpace(item.UpdatedAt) > strings.TrimSpace(best.UpdatedAt) {
			best = item
			found = true
		}
	}
	return best, found
}

func upsertStartUITrackedIssuePlannedItem(repoSlug string, payload startUITrackedIssueScheduleRequest) (*startWorkState, startWorkPlannedItem, error) {
	targetURL, issueNumber, err := resolveStartUITrackedIssueTarget(repoSlug, payload.Number, payload.TargetURL)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	scheduleAt := strings.TrimSpace(payload.ScheduleAt)
	if scheduleAt != "" {
		if _, err := time.Parse(time.RFC3339, scheduleAt); err != nil {
			return nil, startWorkPlannedItem{}, fmt.Errorf("schedule_at must be RFC3339")
		}
	}
	priority := 3
	if payload.Priority != nil {
		priority = *payload.Priority
	} else if labelPriority, ok := startWorkManualPriority(payload.Labels); ok {
		priority = labelPriority
	}
	if priority < 0 || priority > 5 {
		return nil, startWorkPlannedItem{}, fmt.Errorf("priority must be between P0 and P5")
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		title = fmt.Sprintf("Issue #%d", issueNumber)
	}
	plannedTitle := startUITrackedIssuePlannedItemTitle(issueNumber, title)
	description := startUITrackedIssuePlannedItemDescriptionFromSearch(repoSlug, targetURL, payload.Labels, priority)

	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	item, found := startUITrackedIssuePlannedItemForURL(state, targetURL)
	if found {
		item.Title = plannedTitle
		item.Description = description
		item.Priority = priority
		item.ScheduleAt = scheduleAt
		item.State = startPlannedItemQueued
		item.LastError = ""
		item.LaunchRunID = ""
		item.LaunchIssueURL = ""
		item.LaunchIssueNumber = 0
		item.LaunchResult = ""
		item.UpdatedAt = now
		state.PlannedItems[item.ID] = item
	} else {
		itemID := fmt.Sprintf("planned-%d", time.Now().UnixNano())
		item = startWorkPlannedItem{
			ID:          itemID,
			RepoSlug:    repoSlug,
			Title:       plannedTitle,
			Description: description,
			LaunchKind:  "tracked_issue",
			TargetURL:   targetURL,
			Priority:    priority,
			ScheduleAt:  scheduleAt,
			State:       startPlannedItemQueued,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if state.PlannedItems == nil {
			state.PlannedItems = map[string]startWorkPlannedItem{}
		}
		state.PlannedItems[item.ID] = item
	}
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	return state, item, nil
}

func resolveStartUITrackedIssueTarget(repoSlug string, issueNumber int, targetURL string) (string, int, error) {
	trimmedURL := strings.TrimSpace(targetURL)
	if issueNumber > 0 && trimmedURL == "" {
		trimmedURL = fmt.Sprintf("https://github.com/%s/issues/%d", repoSlug, issueNumber)
	}
	if trimmedURL == "" {
		return "", 0, fmt.Errorf("target_url is required")
	}
	target, err := parseGithubTargetURL(trimmedURL)
	if err != nil {
		return "", 0, err
	}
	if target.kind != "issue" {
		return "", 0, fmt.Errorf("target_url must point to a GitHub issue")
	}
	if target.repoSlug != repoSlug {
		return "", 0, fmt.Errorf("target_url must stay inside repo %s", repoSlug)
	}
	if issueNumber > 0 && target.number != issueNumber {
		return "", 0, fmt.Errorf("issue number does not match target_url")
	}
	return trimmedURL, target.number, nil
}

func startUITrackedIssuePlannedItemTitle(issueNumber int, title string) string {
	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle == "" {
		return fmt.Sprintf("Implement tracked issue #%d", issueNumber)
	}
	return fmt.Sprintf("Implement tracked issue #%d: %s", issueNumber, trimmedTitle)
}

func startUITrackedIssuePlannedItemDescriptionFromSearch(repoSlug string, targetURL string, labels []string, priority int) string {
	lines := []string{
		fmt.Sprintf("Tracked issue: %s", strings.TrimSpace(targetURL)),
		fmt.Sprintf("Source repo: %s", repoSlug),
		fmt.Sprintf("Priority: %s", startWorkPriorityLabel(priority)),
	}
	cleanLabels := uniqueStrings(labels)
	if len(cleanLabels) > 0 {
		sort.Strings(cleanLabels)
		lines = append(lines, fmt.Sprintf("Labels: %s", strings.Join(cleanLabels, ", ")))
	}
	lines = append(lines, "", "Imported from the start UI scheduler.")
	return strings.Join(lines, "\n")
}

func createStartUIPlannedItem(repoSlug string, payload startUIPlannedItemRequest) (*startWorkState, startWorkPlannedItem, error) {
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		return nil, startWorkPlannedItem{}, fmt.Errorf("title is required")
	}
	scheduleAt := strings.TrimSpace(payload.ScheduleAt)
	if scheduleAt != "" {
		if _, err := time.Parse(time.RFC3339, scheduleAt); err != nil {
			return nil, startWorkPlannedItem{}, fmt.Errorf("schedule_at must be RFC3339")
		}
	}
	priority := 3
	if payload.Priority != nil {
		priority = *payload.Priority
	}
	if priority < 0 || priority > 5 {
		priority = 3
	}
	now := time.Now().UTC().Format(time.RFC3339)
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	itemID := fmt.Sprintf("planned-%d", time.Now().UnixNano())
	item := startWorkPlannedItem{
		ID:          itemID,
		RepoSlug:    repoSlug,
		Title:       title,
		Description: strings.TrimSpace(payload.Description),
		LaunchKind:  strings.TrimSpace(payload.LaunchKind),
		TargetURL:   strings.TrimSpace(payload.TargetURL),
		Priority:    priority,
		ScheduleAt:  scheduleAt,
		State:       startPlannedItemQueued,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if item.LaunchKind == "" {
		settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
		if resolvedGithubRepoMode(settings) == "local" {
			item.LaunchKind = "local_work"
		} else {
			item.LaunchKind = "github_issue"
		}
	}
	if state.PlannedItems == nil {
		state.PlannedItems = map[string]startWorkPlannedItem{}
	}
	state.PlannedItems[item.ID] = item
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	return state, item, nil
}

func patchStartUIPlannedItem(itemID string, payload startUIPlannedItemPatchRequest) (*startWorkState, startWorkPlannedItem, error) {
	repoSlug, _, _, err := findStartUIPlannedItem(itemID)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	item, ok := state.PlannedItems[itemID]
	if !ok {
		return nil, startWorkPlannedItem{}, fmt.Errorf("planned item %s was not found", itemID)
	}
	if strings.TrimSpace(item.State) != startPlannedItemQueued && strings.TrimSpace(item.State) != startPlannedItemFailed {
		return nil, startWorkPlannedItem{}, fmt.Errorf("planned item %s is not editable from state %s", item.ID, item.State)
	}
	if payload.Priority != nil && (*payload.Priority < 0 || *payload.Priority > 5) {
		return nil, startWorkPlannedItem{}, fmt.Errorf("priority must be between P0 and P5")
	}
	if payload.ScheduleAt != nil {
		value := strings.TrimSpace(*payload.ScheduleAt)
		if value != "" {
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return nil, startWorkPlannedItem{}, fmt.Errorf("schedule_at must be RFC3339")
			}
		}
		item.ScheduleAt = value
	}
	if payload.ClearSchedule {
		item.ScheduleAt = ""
	}
	if payload.Title != nil {
		title := strings.TrimSpace(*payload.Title)
		if title == "" {
			return nil, startWorkPlannedItem{}, fmt.Errorf("title is required")
		}
		item.Title = title
	}
	if payload.Description != nil {
		item.Description = strings.TrimSpace(*payload.Description)
	}
	if payload.Priority != nil {
		item.Priority = *payload.Priority
	}
	if strings.TrimSpace(item.State) == startPlannedItemFailed {
		item.State = startPlannedItemQueued
		item.LastError = ""
		item.LaunchRunID = ""
		item.LaunchIssueURL = ""
		item.LaunchIssueNumber = 0
		item.LaunchResult = ""
	}
	now := time.Now().UTC().Format(time.RFC3339)
	item.UpdatedAt = now
	state.PlannedItems[itemID] = item
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	return state, item, nil
}

func deleteStartUIPlannedItem(itemID string) (*startWorkState, startWorkPlannedItem, error) {
	repoSlug, _, _, err := findStartUIPlannedItem(itemID)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	item, ok := state.PlannedItems[itemID]
	if !ok {
		return nil, startWorkPlannedItem{}, fmt.Errorf("planned item %s was not found", itemID)
	}
	if strings.TrimSpace(item.State) != startPlannedItemQueued && strings.TrimSpace(item.State) != startPlannedItemFailed {
		return nil, startWorkPlannedItem{}, fmt.Errorf("planned item %s cannot be removed from state %s", item.ID, item.State)
	}
	delete(state.PlannedItems, itemID)
	for key, task := range state.ServiceTasks {
		if strings.TrimSpace(task.PlannedItemID) == itemID {
			delete(state.ServiceTasks, key)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	return state, item, nil
}

func findStartUIPlannedItem(itemID string) (string, *startWorkState, startWorkPlannedItem, error) {
	repos, err := listStartUIRepoSlugs()
	if err != nil {
		return "", nil, startWorkPlannedItem{}, err
	}
	for _, repoSlug := range repos {
		state, stateErr := readStartWorkState(repoSlug)
		if stateErr != nil {
			continue
		}
		if item, ok := state.PlannedItems[itemID]; ok {
			return repoSlug, state, item, nil
		}
	}
	return "", nil, startWorkPlannedItem{}, fmt.Errorf("planned item %s was not found", itemID)
}

func queueStartUIPlannedItemLaunchUnlocked(state *startWorkState, item startWorkPlannedItem, now string) {
	if state.ServiceTasks == nil {
		state.ServiceTasks = map[string]startWorkServiceTask{}
	}
	taskID := startServiceTaskKey(startTaskKindPlannedLaunch, item.ID)
	state.ServiceTasks[taskID] = startWorkServiceTask{
		ID:            taskID,
		Kind:          startTaskKindPlannedLaunch,
		Queue:         startTaskQueueService,
		Status:        startWorkServiceTaskQueued,
		PlannedItemID: item.ID,
		Fingerprint:   startWorkPlannedItemFingerprint(item),
		UpdatedAt:     now,
	}
}

func launchStartUIPlannedItemNow(repoSlug string, state *startWorkState, item startWorkPlannedItem) (*startWorkState, startWorkPlannedItem, startPlannedLaunchResult, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	freshState, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkPlannedItem{}, startPlannedLaunchResult{}, err
	}
	freshItem := freshState.PlannedItems[item.ID]
	if strings.TrimSpace(freshItem.State) != startPlannedItemQueued && strings.TrimSpace(freshItem.State) != startPlannedItemFailed {
		return nil, startWorkPlannedItem{}, startPlannedLaunchResult{}, fmt.Errorf("planned item %s is not launchable from state %s", item.ID, freshItem.State)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	freshItem.State = startPlannedItemLaunching
	freshItem.LastError = ""
	freshItem.LaunchRunID = ""
	freshItem.LaunchIssueURL = ""
	freshItem.LaunchIssueNumber = 0
	freshItem.LaunchResult = ""
	freshItem.UpdatedAt = now
	freshState.PlannedItems[item.ID] = freshItem
	freshState.UpdatedAt = now
	queueStartUIPlannedItemLaunchUnlocked(freshState, freshItem, now)
	if err := writeStartWorkStateUnlocked(*freshState); err != nil {
		return nil, startWorkPlannedItem{}, startPlannedLaunchResult{}, err
	}
	return freshState, freshItem, startPlannedLaunchResult{
		Status: "queued",
		Result: "planned item queued for bounded scheduler launch",
	}, nil
}

func ensureStartUIStateUnlocked(repoSlug string) (*startWorkState, error) {
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	state = &startWorkState{
		Version:        startWorkStateVersion,
		SourceRepo:     repoSlug,
		CreatedAt:      now,
		UpdatedAt:      now,
		Issues:         map[string]startWorkIssueState{},
		ServiceTasks:   map[string]startWorkServiceTask{},
		Promotions:     map[string]startWorkPromotion{},
		PromotionSkips: map[string]startWorkPromotionSkip{},
		PlannedItems:   map[string]startWorkPlannedItem{},
	}
	return state, nil
}

func parseStartUIRepoRoute(path string) (string, string, bool) {
	trimmed := strings.TrimPrefix(path, "/api/v1/repos/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 {
		return "", "", false
	}
	repoSlug := parts[0] + "/" + parts[1]
	return repoSlug, strings.Join(parts[2:], "/"), true
}

func parseStartUIRepoIssueActionRoute(tail string) (int, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(tail, "issues/"), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 {
		return 0, "", false
	}
	issueNumber, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || issueNumber <= 0 {
		return 0, "", false
	}
	action := strings.TrimSpace(parts[1])
	if action == "" {
		return 0, "", false
	}
	return issueNumber, action, true
}

func parseStartUIScoutItemRoute(tail string) (string, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(tail, "scout-items/"), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseStartUIPlannedItemRoute(path string) (string, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api/v1/planned-items/"), "/")
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0]), "", strings.TrimSpace(parts[0]) != ""
	}
	if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
	}
	return "", "", false
}

func parseStartUIWorkRunRoute(path string) (string, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api/v1/work/runs/"), "/")
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	return parts[0], strings.Join(parts[1:], "/"), true
}

func startUIScoutDestinationLabel(destination string) string {
	switch normalizeScoutDestination(destination) {
	case improvementDestinationTarget:
		return "repo"
	case improvementDestinationFork:
		return "fork"
	default:
		return "local"
	}
}

func startUIScoutStatusRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case startScoutJobRunning:
		return 0
	case startScoutJobQueued:
		return 1
	case startScoutJobFailed:
		return 2
	case startScoutJobDismissed:
		return 3
	case startScoutJobCompleted:
		return 4
	case "external":
		return 5
	default:
		return 6
	}
}

func startUIScoutAvailableActions(item startUIScoutItem) []string {
	actions := []string{}
	status := strings.ToLower(strings.TrimSpace(item.Status))
	if strings.TrimSpace(item.RunID) != "" {
		actions = append(actions, "open-run")
	}
	if item.Destination == improvementDestinationLocal {
		switch status {
		case startScoutJobQueued:
			actions = append(actions, "dismiss")
		case startScoutJobFailed:
			actions = append(actions, "retry", "dismiss")
		case startScoutJobDismissed:
			actions = append(actions, "retry")
		}
		return actions
	}
	if status == "pending" || status == "external" || status == "failed" {
		actions = append(actions, "dismiss")
	}
	if status == "failed" || status == "dismissed" || status == "planned" || status == "completed" {
		actions = append(actions, "reset")
	}
	return actions
}

func startUIScoutPlannedItemTitle(item startUIScoutItem) string {
	return "Implement scout proposal: " + strings.TrimSpace(item.Title)
}

func startUIScoutPlannedItemDescription(item startUIScoutItem) string {
	lines := []string{
		fmt.Sprintf("Source artifact: %s", defaultString(strings.TrimSpace(item.ArtifactPath), "(unknown)")),
		fmt.Sprintf("Scout role: %s", defaultString(strings.TrimSpace(item.Role), "(unknown)")),
		fmt.Sprintf("Area: %s", defaultString(strings.TrimSpace(item.Area), scoutIssueHeading(item.Role))),
		"",
		"Summary:",
		strings.TrimSpace(item.Summary),
	}
	if strings.TrimSpace(item.Rationale) != "" {
		lines = append(lines, "", "Rationale:", strings.TrimSpace(item.Rationale))
	}
	if strings.TrimSpace(item.Evidence) != "" {
		lines = append(lines, "", "Evidence:", strings.TrimSpace(item.Evidence))
	}
	if strings.TrimSpace(item.Impact) != "" {
		lines = append(lines, "", "Impact:", strings.TrimSpace(item.Impact))
	}
	if len(item.Files) > 0 {
		lines = append(lines, "", "Files:", strings.Join(item.Files, ", "))
	}
	if strings.TrimSpace(item.SuggestedNextStep) != "" {
		lines = append(lines, "", "Suggested next step:", strings.TrimSpace(item.SuggestedNextStep))
	}
	return strings.Join(lines, "\n")
}

func parseStartUIWorkItemRoute(path string) (string, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api/v1/work-items/"), "/")
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	if len(parts) == 1 {
		return parts[0], "", true
	}
	return parts[0], strings.Join(parts[1:], "/"), true
}

func writeJSONResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func hashJSON(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
