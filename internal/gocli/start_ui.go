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
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	startUIDefaultAPIPort             = 17653
	startUIDefaultWebPort             = 17654
	startUIBindHost                   = "0.0.0.0"
	startUIOverviewRunLimit           = 10
	startUIOverviewCacheSchemaVersion = 1
	startUIUsageIndexSchemaVersion    = 1
)

type startUIRuntimeState struct {
	ProcessID int    `json:"process_id"`
	APIURL    string `json:"api_url"`
	WebURL    string `json:"web_url"`
	StartedAt string `json:"started_at"`
	StoppedAt string `json:"stopped_at,omitempty"`
}

type startUISupervisor struct {
	runtimePath   string
	apiServer     *http.Server
	webServer     *http.Server
	api           *startUIAPI
	apiURL        string
	webURL        string
	prewarmCancel context.CancelFunc
	prewarmDone   chan struct{}
}

type startUIAPI struct {
	cwd                  string
	allowedWebOrigin     string
	overviewCacheMu      sync.Mutex
	overviewCache        startUIOverviewCache
	overviewBuildCh      chan struct{}
	sectionCacheMu       sync.Mutex
	sectionCaches        startUIOverviewSectionCaches
	eventsCacheMu        sync.Mutex
	eventsCache          startUIEventsPayloadCache
	eventsBuildCh        chan struct{}
	eventsStreamMu       sync.Mutex
	eventsNotifyCh       chan struct{}
	eventsStopCh         chan struct{}
	eventsDoneCh         chan struct{}
	eventsClients        map[chan map[string]any]struct{}
	eventsLastHash       string
	eventsLastPayload    map[string]any
	localWorkReadMu      sync.Mutex
	localWorkRead        *localWorkDBStore
	localWorkReadRetired *localWorkDBStore
	localWorkReadBuildCh chan struct{}
	localWorkToken       string
	workMetaCache        startUIWorkMetadataCache
	usageIndexMu         sync.Mutex
	usageIndexCache      startUISectionCache[startUIUsageIndexState]
	usageCacheMu         sync.Mutex
	usageCache           map[string]startUIUsageCacheEntry
}

type startUIOverviewCache struct {
	valid     bool
	token     string
	version   string
	checkedAt time.Time
	deps      []string
	overview  startUIOverview
	events    map[string]any
}

var startUIOverviewCacheProbeInterval = 5 * time.Second
var startUIEventsCacheProbeInterval = 2 * time.Second

type startUIEventsPayloadCache struct {
	valid     bool
	checkedAt time.Time
	hash      string
	payload   map[string]any
}

type startUIWorkMetadataCache struct {
	indexedGithubManifestDepsToken string
	indexedGithubManifestDeps      []string
}

type startUIDependencySnapshot struct {
	deps  []string
	token string
}

type startUISectionCache[T any] struct {
	valid     bool
	token     string
	checkedAt time.Time
	deps      []string
	value     T
}

type startUIOverviewSectionCaches struct {
	repos              startUISectionCache[startUIOverviewReposSection]
	workRuns           startUISectionCache[[]startUIWorkRun]
	workItems          startUISectionCache[startUIOverviewWorkItemsSection]
	investigationCount startUISectionCache[int]
	hud                startUISectionCache[HUDRenderContext]
	hudGitBranch       startUISectionCache[string]
}

type startUIOverviewReposSection struct {
	summaries []startUIRepoSummary
}

type startUIOverviewWorkItemsSection struct {
	raw          []workItem
	items        []startUIWorkItem
	hiddenCount  int
	pendingCount int
}

type startUIUsageCacheEntry struct {
	expiresAt time.Time
	report    startUIUsageReport
}

type startUIUsageIndexEntry struct {
	Path             string      `json:"path"`
	Root             string      `json:"root"`
	Size             int64       `json:"size"`
	ModifiedUnixNano int64       `json:"modified_unix_nano"`
	SourceKind       string      `json:"source_kind,omitempty"`
	SourceUpdatedAt  string      `json:"source_updated_at,omitempty"`
	Record           usageRecord `json:"record"`
}

type startUIUsageIndexState struct {
	SchemaVersion       int                      `json:"schema_version"`
	Version             string                   `json:"version"`
	UpdatedAt           string                   `json:"updated_at"`
	SessionRootsScanned int                      `json:"session_roots_scanned"`
	WorkSyncUpdatedAt   string                   `json:"work_sync_updated_at,omitempty"`
	LegacyImportedAt    string                   `json:"legacy_imported_at,omitempty"`
	LegacyImportedFrom  string                   `json:"legacy_imported_from,omitempty"`
	Entries             []startUIUsageIndexEntry `json:"entries"`
}

type startUIPersistedOverviewCacheState struct {
	SchemaVersion   int             `json:"schema_version"`
	OverviewVersion string          `json:"overview_version"`
	GeneratedAt     string          `json:"generated_at"`
	Overview        startUIOverview `json:"overview"`
	Dependencies    []string        `json:"dependencies,omitempty"`
	DependencyToken string          `json:"dependency_token,omitempty"`
}

// startUIOverviewCacheAfterUncachedBuildHook is overridden by tests to simulate
// an external writer changing overview dependencies between build and snapshot.
var startUIOverviewCacheAfterUncachedBuildHook func()
var startUIPrewarmLogWriter io.Writer = os.Stderr
var startUIPrewarmDelay = time.Second
var startUISectionCacheProbeInterval = 5 * time.Second

// The Usage page auto-refreshes every 30 seconds in the client, so probing the
// backing usage index more aggressively just drags refresh work back onto
// foreground requests without improving visible freshness.
var startUIUsageIndexProbeInterval = 30 * time.Second

// Usage cache keys already include the indexed data version and active filters,
// so a longer TTL avoids rebuilding identical reports on repeated page loads
// while still rolling forward promptly when the usage index changes.
var startUIUsageCacheTTL = time.Hour
var startUIUsageBackgroundWarmInterval = 30 * time.Second
var startUIUsageCacheNow = time.Now

type startUITotals struct {
	Repos            int `json:"repos"`
	IssuesQueued     int `json:"issues_queued"`
	IssuesInProgress int `json:"issues_in_progress"`
	ServiceQueued    int `json:"service_queued"`
	ServiceRunning   int `json:"service_running"`
	ScoutQueued      int `json:"scout_queued"`
	ScoutRunning     int `json:"scout_running"`
	ScoutFailed      int `json:"scout_failed"`
	ScoutDismissed   int `json:"scout_dismissed"`
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
	RepoSlug            string                            `json:"repo_slug"`
	SettingsPath        string                            `json:"settings_path,omitempty"`
	RepoMode            string                            `json:"repo_mode,omitempty"`
	IssuePickMode       string                            `json:"issue_pick_mode,omitempty"`
	PRForwardMode       string                            `json:"pr_forward_mode,omitempty"`
	ForkIssuesMode      string                            `json:"fork_issues_mode,omitempty"`
	ImplementMode       string                            `json:"implement_mode,omitempty"`
	PublishTarget       string                            `json:"publish_target,omitempty"`
	StartParticipation  bool                              `json:"start_participation"`
	UpdatedAt           string                            `json:"updated_at,omitempty"`
	StatePath           string                            `json:"state_path,omitempty"`
	SourcePath          string                            `json:"source_path,omitempty"`
	SourceCheckoutReady bool                              `json:"source_checkout_ready"`
	ScoutCatalog        []startUIScoutCatalogEntry        `json:"scout_catalog,omitempty"`
	ScoutsByRole        map[string]startUIRepoScoutConfig `json:"scouts_by_role,omitempty"`
	Scouts              startUIRepoScouts                 `json:"scouts"`
	IssueCounts         map[string]int                    `json:"issue_counts"`
	ServiceTaskCounts   map[string]int                    `json:"service_task_counts"`
	ScoutJobCounts      map[string]int                    `json:"scout_job_counts"`
	PlannedItemCounts   map[string]int                    `json:"planned_item_counts"`
	LastRun             *startWorkLastRun                 `json:"last_run,omitempty"`
	DefaultBranch       string                            `json:"default_branch,omitempty"`
	LockState           *repoAccessLockStateSnapshot      `json:"lock_state,omitempty"`
	Settings            *githubRepoSettings               `json:"settings,omitempty"`
	State               *startWorkState                   `json:"state,omitempty"`
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
	Repo     string `json:"repo,omitempty"`
	Root     string `json:"root,omitempty"`
	Activity string `json:"activity,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Model    string `json:"model,omitempty"`
}

type startUIUsageReport struct {
	GeneratedAt string                  `json:"generated_at"`
	Version     string                  `json:"version"`
	TimeBasis   string                  `json:"time_basis,omitempty"`
	Coverage    string                  `json:"coverage,omitempty"`
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
	Diagnostics startUIUsageDiagnostics `json:"diagnostics,omitempty"`
}

type startUIUsageDiagnostics struct {
	SampledAt        string `json:"sampled_at,omitempty"`
	DataVersion      string `json:"data_version,omitempty"`
	CacheStatus      string `json:"cache_status,omitempty"`
	CacheExpiresAt   string `json:"cache_expires_at,omitempty"`
	DefaultWindow    bool   `json:"default_window,omitempty"`
	SessionRoots     int    `json:"session_roots_scanned,omitempty"`
	IndexLoadMS      int64  `json:"index_load_ms,omitempty"`
	SourceBuildMS    int64  `json:"source_build_ms,omitempty"`
	SummaryBuildMS   int64  `json:"summary_build_ms,omitempty"`
	AnalyticsBuildMS int64  `json:"analytics_build_ms,omitempty"`
	GroupBuildMS     int64  `json:"group_build_ms,omitempty"`
	TopSessionsMS    int64  `json:"top_sessions_ms,omitempty"`
	TotalBuildMS     int64  `json:"total_build_ms,omitempty"`
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
	CurrentRound     int    `json:"current_round,omitempty"`
	CurrentIteration int    `json:"current_iteration,omitempty"`
	UpdatedAt        string `json:"updated_at"`
	TargetKind       string `json:"target_kind,omitempty"`
	TargetURL        string `json:"target_url,omitempty"`
	WorkType         string `json:"work_type,omitempty"`
	ArtifactPath     string `json:"artifact_path,omitempty"`
	PublicationState string `json:"publication_state,omitempty"`
	PauseReason      string `json:"pause_reason,omitempty"`
	PauseUntil       string `json:"pause_until,omitempty"`
	StopAllowed      bool   `json:"stop_allowed,omitempty"`
	RerunAllowed     bool   `json:"rerun_allowed,omitempty"`
	ResolveAllowed   bool   `json:"resolve_allowed,omitempty"`
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
	PauseReason    string `json:"pause_reason,omitempty"`
	PauseUntil     string `json:"pause_until,omitempty"`
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
	Title                  string   `json:"title"`
	Description            string   `json:"description,omitempty"`
	WorkType               string   `json:"work_type,omitempty"`
	Priority               *int     `json:"priority,omitempty"`
	ScheduleAt             string   `json:"schedule_at,omitempty"`
	LaunchKind             string   `json:"launch_kind,omitempty"`
	FindingsHandling       string   `json:"findings_handling,omitempty"`
	TargetURL              string   `json:"target_url,omitempty"`
	InvestigationQuery     string   `json:"investigation_query,omitempty"`
	ScoutRole              string   `json:"scout_role,omitempty"`
	ScoutDestination       string   `json:"scout_destination,omitempty"`
	ScoutSessionLimit      int      `json:"scout_session_limit,omitempty"`
	ScoutFocus             []string `json:"scout_focus,omitempty"`
	IdempotencyKey         string   `json:"idempotency_key,omitempty"`
	IdempotencyFingerprint string   `json:"idempotency_fingerprint,omitempty"`
}

type startUIIdempotencyConflictError struct {
	Key string
}

func (err startUIIdempotencyConflictError) Error() string {
	if strings.TrimSpace(err.Key) == "" {
		return "idempotency key is already used for a different task scheduling request"
	}
	return fmt.Sprintf("idempotency key %q is already used for a different task scheduling request", err.Key)
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
	WorkType      string   `json:"work_type,omitempty"`
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
	WorkType   string   `json:"work_type,omitempty"`
	Priority   *int     `json:"priority,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"`
}

type startUIPlannedItemPatchRequest struct {
	Title         *string `json:"title,omitempty"`
	Description   *string `json:"description,omitempty"`
	WorkType      *string `json:"work_type,omitempty"`
	Priority      *int    `json:"priority,omitempty"`
	ScheduleAt    *string `json:"schedule_at,omitempty"`
	ClearSchedule bool    `json:"clear_schedule,omitempty"`
}

type startUIFindingPatchRequest struct {
	Title    *string   `json:"title,omitempty"`
	Summary  *string   `json:"summary,omitempty"`
	Detail   *string   `json:"detail,omitempty"`
	Evidence *string   `json:"evidence,omitempty"`
	Severity *string   `json:"severity,omitempty"`
	WorkType *string   `json:"work_type,omitempty"`
	Files    *[]string `json:"files,omitempty"`
	Path     *string   `json:"path,omitempty"`
	Line     *int      `json:"line,omitempty"`
	Route    *string   `json:"route,omitempty"`
	Page     *string   `json:"page,omitempty"`
}

type startUIFindingImportSessionCreateRequest struct {
	FilePath string `json:"file_path,omitempty"`
	Markdown string `json:"markdown"`
}

type startUIFindingImportCandidatePatchRequest struct {
	Title      *string   `json:"title,omitempty"`
	Summary    *string   `json:"summary,omitempty"`
	Detail     *string   `json:"detail,omitempty"`
	Evidence   *string   `json:"evidence,omitempty"`
	Severity   *string   `json:"severity,omitempty"`
	WorkType   *string   `json:"work_type,omitempty"`
	Files      *[]string `json:"files,omitempty"`
	Path       *string   `json:"path,omitempty"`
	Line       *int      `json:"line,omitempty"`
	Route      *string   `json:"route,omitempty"`
	Page       *string   `json:"page,omitempty"`
	ParseNotes *string   `json:"parse_notes,omitempty"`
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
	Improvement        startUIRepoScoutConfig `json:"improvement"`
	Enhancement        startUIRepoScoutConfig `json:"enhancement"`
	BackendPerformance startUIRepoScoutConfig `json:"backend-performance"`
	UI                 startUIRepoScoutConfig `json:"ui"`
}

type startUIRepoScoutsPatchRequest struct {
	Improvement        *startUIRepoScoutConfig            `json:"improvement,omitempty"`
	Enhancement        *startUIRepoScoutConfig            `json:"enhancement,omitempty"`
	BackendPerformance *startUIRepoScoutConfig            `json:"backend-performance,omitempty"`
	UI                 *startUIRepoScoutConfig            `json:"ui,omitempty"`
	ScoutsByRole       map[string]*startUIRepoScoutConfig `json:"scouts_by_role,omitempty"`
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
	case backendPerformanceScoutRole:
		scouts.BackendPerformance = config
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
	case backendPerformanceScoutRole:
		return patch.BackendPerformance
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

type startUIRepoCreateRequest struct {
	RepoSlug       string `json:"repo_slug"`
	RepoMode       string `json:"repo_mode"`
	IssuePickMode  string `json:"issue_pick_mode"`
	PRForwardMode  string `json:"pr_forward_mode"`
	ForkIssuesMode string `json:"fork_issues_mode"`
	ImplementMode  string `json:"implement_mode"`
	PublishTarget  string `json:"publish_target"`
}

func (request startUIRepoCreateRequest) settingsPatch() startUIRepoSettingsPatchRequest {
	return startUIRepoSettingsPatchRequest{
		RepoMode:       request.RepoMode,
		IssuePickMode:  request.IssuePickMode,
		PRForwardMode:  request.PRForwardMode,
		ForkIssuesMode: request.ForkIssuesMode,
		ImplementMode:  request.ImplementMode,
		PublishTarget:  request.PublishTarget,
	}
}

type startUIScoutItem struct {
	ID                 string   `json:"id"`
	Role               string   `json:"role"`
	Title              string   `json:"title"`
	WorkType           string   `json:"work_type,omitempty"`
	Area               string   `json:"area,omitempty"`
	Summary            string   `json:"summary"`
	Rationale          string   `json:"rationale,omitempty"`
	Evidence           string   `json:"evidence,omitempty"`
	Impact             string   `json:"impact,omitempty"`
	SuggestedNextStep  string   `json:"suggested_next_step,omitempty"`
	Confidence         string   `json:"confidence,omitempty"`
	Files              []string `json:"files,omitempty"`
	Labels             []string `json:"labels,omitempty"`
	Page               string   `json:"page,omitempty"`
	Route              string   `json:"route,omitempty"`
	Severity           string   `json:"severity,omitempty"`
	TargetKind         string   `json:"target_kind,omitempty"`
	Screenshots        []string `json:"screenshots,omitempty"`
	ArtifactPath       string   `json:"artifact_path"`
	ProposalPath       string   `json:"proposal_path"`
	PolicyPath         string   `json:"policy_path,omitempty"`
	PreflightPath      string   `json:"preflight_path,omitempty"`
	IssueDraftPath     string   `json:"issue_draft_path,omitempty"`
	RawOutputPath      string   `json:"raw_output_path,omitempty"`
	GeneratedAt        string   `json:"generated_at,omitempty"`
	AuditMode          string   `json:"audit_mode,omitempty"`
	SurfaceKind        string   `json:"surface_kind,omitempty"`
	SurfaceTarget      string   `json:"surface_target,omitempty"`
	BrowserReady       bool     `json:"browser_ready,omitempty"`
	PreflightReason    string   `json:"preflight_reason,omitempty"`
	Destination        string   `json:"destination"`
	ForkRepo           string   `json:"fork_repo,omitempty"`
	Status             string   `json:"status"`
	RunID              string   `json:"run_id,omitempty"`
	PlannedItemID      string   `json:"planned_item_id,omitempty"`
	Error              string   `json:"error,omitempty"`
	PauseReason        string   `json:"pause_reason,omitempty"`
	PauseUntil         string   `json:"pause_until,omitempty"`
	RecoveryCount      int      `json:"recovery_count,omitempty"`
	LastRecoveryReason string   `json:"last_recovery_reason,omitempty"`
	LastRecoveryAt     string   `json:"last_recovery_at,omitempty"`
	LastRecoveredRunID string   `json:"last_recovered_run_id,omitempty"`
	UpdatedAt          string   `json:"updated_at,omitempty"`
	AvailableActions   []string `json:"available_actions,omitempty"`
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

type startUIFindingsResponse struct {
	Repo  startUIRepoSummary `json:"repo"`
	Items []startWorkFinding `json:"items"`
}

type startUIFindingImportSessionsResponse struct {
	Repo  startUIRepoSummary              `json:"repo"`
	Items []startWorkFindingImportSession `json:"items"`
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
	WorkType          string   `json:"work_type,omitempty"`
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
	PauseReason             string `json:"pause_reason,omitempty"`
	PauseUntil              string `json:"pause_until,omitempty"`
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
	PauseReason          string                       `json:"pause_reason,omitempty"`
	PauseUntil           string                       `json:"pause_until,omitempty"`
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
	Summary           startUIWorkRun            `json:"summary"`
	Backend           string                    `json:"backend"`
	LocalManifest     *localWorkManifest        `json:"local_manifest,omitempty"`
	GithubManifest    *githubWorkManifest       `json:"github_manifest,omitempty"`
	GithubStatus      *githubWorkStatusSnapshot `json:"github_status,omitempty"`
	StartedAt         string                    `json:"started_at,omitempty"`
	TotalTokens       int                       `json:"total_tokens,omitempty"`
	SessionsAccounted int                       `json:"sessions_accounted,omitempty"`
	HasTokenUsage     bool                      `json:"has_token_usage,omitempty"`
	NextAction        string                    `json:"next_action,omitempty"`
	HumanGateReason   string                    `json:"human_gate_reason,omitempty"`
	SyncAllowed       bool                      `json:"sync_allowed,omitempty"`
	StopAllowed       bool                      `json:"stop_allowed,omitempty"`
	RerunAllowed      bool                      `json:"rerun_allowed,omitempty"`
	ResolveAllowed    bool                      `json:"resolve_allowed,omitempty"`
	ExternalURL       string                    `json:"external_url,omitempty"`
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

var startUIResolveWorkRun = func(runID string) error {
	return resolveLocalWork("", localWorkResolveOptions{RunSelection: localWorkRunSelection{RunID: runID}})
}

var startUIStopWorkRun = func(runID string) error {
	return stopLocalWork("", localWorkStopOptions{RunSelection: localWorkRunSelection{RunID: runID}})
}

var startUIRerunWorkRun = func(runID string) (string, error) {
	return rerunLocalWork("", localWorkRunSelection{RunID: runID})
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
	api.loadPersistedOverviewCache()
	apiServer := &http.Server{Handler: api.routes()}
	webServer := &http.Server{Handler: startUIWebHandler(apiURL)}
	prewarmCtx, prewarmCancel := context.WithCancel(context.Background())
	prewarmDone := make(chan struct{})

	supervisor := &startUISupervisor{
		runtimePath:   filepath.Join(githubNanaHome(), "start", "ui", "runtime.json"),
		apiServer:     apiServer,
		webServer:     webServer,
		api:           api,
		apiURL:        apiURL,
		webURL:        webURL,
		prewarmCancel: prewarmCancel,
		prewarmDone:   prewarmDone,
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
	go api.prewarmStartUIDataAsync(prewarmCtx, prewarmDone)
	return supervisor, nil
}

func (h *startUIAPI) prewarmStartUIDataAsync(ctx context.Context, done chan struct{}) {
	if done != nil {
		defer close(done)
	}
	if startUIPrewarmDelay > 0 {
		timer := time.NewTimer(startUIPrewarmDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
	if err := h.prewarmStartUIData(); err != nil && startUIPrewarmLogWriter != nil {
		fmt.Fprintf(startUIPrewarmLogWriter, "[start-ui] prewarm failed: %v\n", err)
	}
	if startUIUsageBackgroundWarmInterval <= 0 {
		return
	}
	ticker := time.NewTicker(startUIUsageBackgroundWarmInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.prewarmDefaultUsageReport(); err != nil && startUIPrewarmLogWriter != nil {
				fmt.Fprintf(startUIPrewarmLogWriter, "[start-ui] usage prewarm failed: %v\n", err)
			}
		}
	}
}

func (h *startUIAPI) prewarmStartUIData() error {
	errs := []error{}
	if _, err := h.buildOverview(); err != nil {
		errs = append(errs, fmt.Errorf("overview: %w", err))
	}
	if err := h.prewarmDefaultUsageReport(); err != nil {
		errs = append(errs, fmt.Errorf("usage: %w", err))
	}
	return errors.Join(errs...)
}

func (h *startUIAPI) prewarmDefaultUsageReport() error {
	_, err := h.buildUsageReport(startUIDefaultUsageQuery())
	return err
}

func startUIDefaultUsageQuery() url.Values {
	return url.Values{
		"since": {"30d"},
		"root":  {"all"},
	}
}

func (s *startUISupervisor) Close() error {
	if s == nil {
		return nil
	}
	if s.prewarmCancel != nil {
		s.prewarmCancel()
	}
	if s.prewarmDone != nil {
		select {
		case <-s.prewarmDone:
		case <-time.After(100 * time.Millisecond):
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.apiServer != nil {
		_ = s.apiServer.Shutdown(ctx)
	}
	if s.webServer != nil {
		_ = s.webServer.Shutdown(ctx)
	}
	if s.api != nil {
		_ = s.api.Close()
	}
	runtime := startUIRuntimeState{}
	_ = readGithubJSON(s.runtimePath, &runtime)
	runtime.ProcessID = os.Getpid()
	runtime.APIURL = s.apiURL
	runtime.WebURL = s.webURL
	runtime.StoppedAt = time.Now().UTC().Format(time.RFC3339)
	return writeGithubJSON(s.runtimePath, runtime)
}

func (h *startUIAPI) Close() error {
	if h == nil {
		return nil
	}
	h.stopEventsBroadcaster()
	return h.closePersistentLocalWorkReadStore()
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
	mux.HandleFunc("/api/v1/tasks", h.handleTasks)
	mux.HandleFunc("/api/v1/tasks/templates", h.handleTaskTemplates)
	mux.HandleFunc("/api/v1/tasks/", h.handleTask)
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
		h.applyCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *startUIAPI) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = h.allowedWebOrigin
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
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
		writeStartUIError(w, err, http.StatusInternalServerError)
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
		if startUITaskIDLooksLikeLegacyInvestigationAlias(runID) {
			taskDetail, taskErr := loadStartUITaskDetail(h.cwd, runID)
			if taskErr == nil {
				writeJSONResponse(w, taskDetail)
				return
			}
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSONResponse(w, detail)
}

func startUITaskIDLooksLikeLegacyInvestigationAlias(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range []string{
		"issue:",
		"planned-item:",
		"scout-job:",
		"investigation:",
		"work-run:",
		"work-item:",
		"service-task:",
	} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
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
	switch r.Method {
	case http.MethodGet:
		repos, err := listStartUIRepoSummaries(true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, map[string]any{"repos": repos})
	case http.MethodPost:
		var payload startUIRepoCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		summary, created, err := createStartUIRepo(payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSONResponseWithStatus(w, status, map[string]any{
			"created": created,
			"repo":    summary,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
	case r.Method == http.MethodGet && tail == "findings":
		payload, err := loadStartUIFindings(repoSlug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, payload)
	case r.Method == http.MethodGet && tail == "finding-import-sessions":
		payload, err := loadStartUIFindingImportSessions(repoSlug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, payload)
	case r.Method == http.MethodPost && tail == "finding-import-sessions":
		var payload startUIFindingImportSessionCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		session, err := createStartUIFindingImportSession(repoSlug, payload.FilePath, payload.Markdown)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"session": session})
	case r.Method == http.MethodGet && strings.HasPrefix(tail, "findings/"):
		findingID := strings.Trim(strings.TrimPrefix(tail, "findings/"), "/")
		finding, err := loadStartUIFinding(repoSlug, findingID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"finding": finding})
	case r.Method == http.MethodPatch && strings.HasPrefix(tail, "findings/"):
		findingID := strings.Trim(strings.TrimPrefix(tail, "findings/"), "/")
		var payload startUIFindingPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		state, finding, err := patchStartUIFinding(repoSlug, findingID, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": state, "finding": finding})
	case r.Method == http.MethodPost && strings.HasPrefix(tail, "findings/"):
		findingID, action, ok := parseStartUIFindingRoute(tail)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch action {
		case "promote":
			state, finding, item, err := promoteStartUIFinding(repoSlug, findingID)
			h.invalidateOverviewCache()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSONResponse(w, map[string]any{"state": state, "finding": finding, "planned_item": item})
		case "dismiss":
			state, finding, err := dismissStartUIFinding(repoSlug, findingID, "")
			h.invalidateOverviewCache()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSONResponse(w, map[string]any{"state": state, "finding": finding})
		default:
			http.NotFound(w, r)
		}
	case r.Method == http.MethodGet && strings.HasPrefix(tail, "finding-import-sessions/"):
		sessionID, candidateID, action, ok := parseStartUIFindingImportSessionRoute(tail)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if candidateID != "" || action != "" {
			http.NotFound(w, r)
			return
		}
		session, err := loadStartUIFindingImportSession(repoSlug, sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"session": session})
	case r.Method == http.MethodPatch && strings.HasPrefix(tail, "finding-import-sessions/"):
		sessionID, candidateID, action, ok := parseStartUIFindingImportSessionRoute(tail)
		if !ok || candidateID == "" || action != "candidate" {
			http.NotFound(w, r)
			return
		}
		var payload startUIFindingImportCandidatePatchRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		state, session, err := patchStartUIFindingImportCandidate(repoSlug, sessionID, candidateID, payload)
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONResponse(w, map[string]any{"state": state, "session": session})
	case r.Method == http.MethodPost && strings.HasPrefix(tail, "finding-import-sessions/"):
		sessionID, candidateID, action, ok := parseStartUIFindingImportSessionRoute(tail)
		if !ok || candidateID == "" {
			http.NotFound(w, r)
			return
		}
		switch action {
		case "promote":
			state, session, finding, err := promoteStartUIFindingImportCandidate(repoSlug, sessionID, candidateID)
			h.invalidateOverviewCache()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSONResponse(w, map[string]any{"state": state, "session": session, "finding": finding})
		case "drop":
			state, session, err := dropStartUIFindingImportCandidate(repoSlug, sessionID, candidateID)
			h.invalidateOverviewCache()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSONResponse(w, map[string]any{"state": state, "session": session})
		default:
			http.NotFound(w, r)
		}
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
		bodyKey := strings.TrimSpace(payload.IdempotencyKey)
		headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if bodyKey != "" && headerKey != "" && bodyKey != headerKey {
			writeJSONResponseWithStatus(w, http.StatusBadRequest, map[string]any{
				"code":    "invalid_idempotency_key",
				"message": "idempotency key mismatch between request body and Idempotency-Key header",
			})
			return
		}
		idempotencyKey, err := normalizeStartUITaskIdempotencyKey(defaultString(headerKey, bodyKey))
		if err != nil {
			writeJSONResponseWithStatus(w, http.StatusBadRequest, map[string]any{
				"code":    "invalid_idempotency_key",
				"message": err.Error(),
			})
			return
		}
		payload.IdempotencyKey = idempotencyKey
		state, item, err := createStartUIPlannedItem(repoSlug, payload)
		if err != nil {
			if conflict, ok := asStartUIIdempotencyConflictError(err); ok {
				writeJSONResponseWithStatus(w, http.StatusConflict, map[string]any{
					"code":    "idempotency_conflict",
					"message": conflict.Error(),
				})
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
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
		writeStartUIError(w, err, http.StatusInternalServerError)
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
		writeStartUIError(w, err, http.StatusInternalServerError)
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
			writeStartUIError(w, err, http.StatusNotFound)
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
			writeStartUIError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, detail)
	case r.Method == http.MethodPost && tail == "requeue":
		err := requeuePausedWorkItemByID(itemID, "ui")
		h.invalidateOverviewCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		detail, err := readWorkItemDetail(itemID)
		if err != nil {
			writeStartUIError(w, err, http.StatusInternalServerError)
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
			writeStartUIError(w, err, http.StatusInternalServerError)
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
	if r.Method == http.MethodPost && tail == "rerun" {
		detail, err := loadStartUIWorkRunDetail(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if detail.Backend != "local" || !detail.RerunAllowed {
			http.Error(w, "rerun is only available for local work runs with replayable input", http.StatusBadRequest)
			return
		}
		nextRunID, err := startUIRerunWorkRun(runID)
		if err != nil {
			h.invalidateOverviewCache()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
		updated, err := loadStartUIWorkRunDetail(nextRunID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, map[string]any{"run_id": nextRunID, "detail": updated})
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
	if r.Method == http.MethodPost && tail == "stop" {
		detail, err := loadStartUIWorkRunDetail(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if detail.Backend != "local" {
			http.Error(w, "stop is only available for local work runs", http.StatusBadRequest)
			return
		}
		if err := startUIStopWorkRun(runID); err != nil {
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
	if r.Method == http.MethodPost && tail == "resolve" {
		detail, err := loadStartUIWorkRunDetail(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if !detail.ResolveAllowed {
			http.Error(w, "resolve is only available for blocked local runs with recoverable final apply state", http.StatusBadRequest)
			return
		}
		if err := startUIResolveWorkRun(runID); err != nil {
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

	payload, hash, err := h.loadCachedEventsPayload()
	if err == nil {
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
		flusher.Flush()
	}

	subscriber := h.subscribeEvents()
	defer h.unsubscribeEvents(subscriber)
	lastHash := hash
	h.notifyEventsBroadcaster()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, ok := <-subscriber:
			if !ok {
				return
			}
			hash := hashStartUIEventPayload(payload)
			if hash == lastHash {
				continue
			}
			lastHash = hash
			data, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (h *startUIAPI) buildOverview() (startUIOverview, error) {
	return h.buildCachedOverview()
}

func (h *startUIAPI) buildCachedOverview() (startUIOverview, error) {
	for {
		now := time.Now()
		h.overviewCacheMu.Lock()
		if h.overviewCache.valid && startUIOverviewCacheProbeInterval > 0 && now.Sub(h.overviewCache.checkedAt) < startUIOverviewCacheProbeInterval {
			overview := h.overviewCache.overview
			h.overviewCacheMu.Unlock()
			return overview, nil
		}
		if h.overviewBuildCh != nil {
			waitCh := h.overviewBuildCh
			h.overviewCacheMu.Unlock()
			<-waitCh
			continue
		}

		buildCh := make(chan struct{})
		h.overviewBuildCh = buildCh
		cacheDeps := append([]string(nil), h.overviewCache.deps...)
		h.overviewCacheMu.Unlock()

		overview, err := h.rebuildOverviewCache(cacheDeps)
		h.overviewCacheMu.Lock()
		h.overviewBuildCh = nil
		close(buildCh)
		h.overviewCacheMu.Unlock()
		return overview, err
	}
}

func (h *startUIAPI) rebuildOverviewCache(previousDeps []string) (startUIOverview, error) {
	if len(previousDeps) > 0 {
		h.expireOverviewSectionsForChangedDependencies(previousDeps)
	}
	overview, err := h.buildOverviewUncached()
	if err != nil {
		h.overviewCacheMu.Lock()
		h.overviewCache.valid = false
		h.overviewCache.events = nil
		h.overviewCache.token = ""
		h.overviewCache.deps = nil
		h.overviewCacheMu.Unlock()
		h.invalidateEventsCache()
		return startUIOverview{}, err
	}
	if startUIOverviewCacheAfterUncachedBuildHook != nil {
		startUIOverviewCacheAfterUncachedBuildHook()
	}
	version := startUIOverviewVersion(overview)
	events := startUIOverviewEventsPayload(overview, version, "")
	token := h.currentOverviewSectionCacheToken()
	persistedSnapshot := snapshotStartUIDependencies(h.listStartUIOverviewDependencies())
	h.overviewCacheMu.Lock()
	h.overviewCache = startUIOverviewCache{
		valid:     true,
		token:     token,
		version:   version,
		checkedAt: time.Now(),
		deps:      persistedSnapshot.deps,
		overview:  overview,
		events:    events,
	}
	h.overviewCacheMu.Unlock()
	h.invalidateEventsCache()
	_ = writeStartUIPersistedOverviewCacheState(startUIOverviewCachePath(), startUIPersistedOverviewCacheState{
		SchemaVersion:   startUIOverviewCacheSchemaVersion,
		OverviewVersion: version,
		GeneratedAt:     overview.GeneratedAt,
		Overview:        overview,
		Dependencies:    append([]string(nil), persistedSnapshot.deps...),
		DependencyToken: persistedSnapshot.token,
	})
	return overview, nil
}

func (h *startUIAPI) buildOverviewUncached() (startUIOverview, error) {
	var (
		repoSection        startUIOverviewReposSection
		workRuns           []startUIWorkRun
		workItemsSection   startUIOverviewWorkItemsSection
		investigationCount int
		hud                HUDRenderContext
		loadErr            error
		loadErrMu          sync.Mutex
		wg                 sync.WaitGroup
	)
	recordError := func(err error) {
		if err == nil {
			return
		}
		loadErrMu.Lock()
		if loadErr == nil {
			loadErr = err
		}
		loadErrMu.Unlock()
	}

	wg.Add(5)
	go func() {
		defer wg.Done()
		section, err := h.loadOverviewReposSection()
		if err != nil {
			recordError(err)
			return
		}
		repoSection = section
	}()
	go func() {
		defer wg.Done()
		runs, err := h.loadOverviewWorkRunsSection()
		if err != nil {
			recordError(err)
			return
		}
		workRuns = runs
	}()
	go func() {
		defer wg.Done()
		section, err := h.loadOverviewWorkItemsSection()
		if err != nil {
			recordError(err)
			return
		}
		workItemsSection = section
	}()
	go func() {
		defer wg.Done()
		count, err := h.loadOverviewInvestigationCountSection()
		if err != nil {
			recordError(err)
			return
		}
		investigationCount = count
	}()
	go func() {
		defer wg.Done()
		value, err := h.loadOverviewHUDSection()
		if err != nil {
			recordError(err)
			return
		}
		hud = value
	}()
	wg.Wait()
	if loadErr != nil {
		return startUIOverview{}, loadErr
	}

	repos := startUIStripRepoSummaryState(repoSection.summaries)
	reviewCount, replyCount := startUICountFeedbackItems(workItemsSection.raw)
	approvalCount := startUICountApprovals(workRuns, workItemsSection.raw, repoSection.summaries)

	totals := startUITotals{Repos: len(repos)}
	for _, repo := range repoSection.summaries {
		totals.IssuesQueued += repo.IssueCounts[startWorkStatusQueued]
		totals.IssuesInProgress += repo.IssueCounts[startWorkStatusInProgress]
		totals.BlockedIssues += repo.IssueCounts[startWorkStatusBlocked]
		totals.ServiceQueued += repo.ServiceTaskCounts[startWorkServiceTaskQueued]
		totals.ServiceRunning += repo.ServiceTaskCounts[startWorkServiceTaskRunning]
		totals.ScoutQueued += repo.ScoutJobCounts[startScoutJobQueued]
		totals.ScoutRunning += repo.ScoutJobCounts[startScoutJobRunning]
		totals.ScoutFailed += repo.ScoutJobCounts[startScoutJobFailed]
		totals.ScoutDismissed += repo.ScoutJobCounts[startScoutJobDismissed]
		totals.ScoutCompleted += repo.ScoutJobCounts[startScoutJobCompleted]
		totals.PlannedQueued += repo.PlannedItemCounts[startPlannedItemQueued]
		totals.PlannedLaunching += repo.PlannedItemCounts[startPlannedItemLaunching]
	}
	for _, run := range workRuns {
		if run.Status == "running" || run.Status == "active" || run.Status == "in_progress" {
			totals.ActiveWorkRuns++
		}
	}
	totals.PendingWorkItems = workItemsSection.pendingCount
	totals.HiddenWorkItems = workItemsSection.hiddenCount
	totals.Investigations = investigationCount
	totals.ReviewItems = reviewCount
	totals.ReplyItems = replyCount
	totals.ApprovalItems = approvalCount
	return startUIOverview{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Totals:       totals,
		ScoutCatalog: startUIScoutCatalog(),
		Repos:        repos,
		WorkRuns:     workRuns,
		WorkItems:    workItemsSection.items,
		HUD:          hud,
	}, nil
}

func (h *startUIAPI) buildUsageReport(query url.Values) (startUIUsageReport, error) {
	requestStarted := time.Now()
	options := usageOptions{
		View:     "summary",
		Limit:    10,
		CWD:      h.cwd,
		Since:    strings.TrimSpace(query.Get("since")),
		Project:  strings.TrimSpace(query.Get("project")),
		Repo:     strings.TrimSpace(query.Get("repo")),
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
	if strings.TrimSpace(options.Repo) != "" && !validRepoSlug(options.Repo) {
		return startUIUsageReport{}, fmt.Errorf("invalid usage repo %q", options.Repo)
	}
	indexLoadStarted := time.Now()
	index, err := h.loadUsageIndexForReport(options)
	if err != nil {
		return startUIUsageReport{}, err
	}
	indexLoadDuration := time.Since(indexLoadStarted)
	filters := startUIUsageFilters{
		Since:    options.Since,
		Project:  options.Project,
		Repo:     options.Repo,
		Root:     options.Root,
		Activity: options.Activity,
		Phase:    options.Phase,
		Model:    options.Model,
	}
	cacheKey := startUIUsageCacheKey(filters, startUIUsageCacheVersion(index, options))
	now := startUIUsageCacheNow()
	if startUIUsageCacheTTL > 0 {
		h.usageCacheMu.Lock()
		if entry, ok := h.usageCache[cacheKey]; ok && now.Before(entry.expiresAt) {
			report := entry.report
			h.usageCacheMu.Unlock()
			return startUIUsageReportWithDiagnostics(report, startUIUsageDiagnostics{
				SampledAt:      time.Now().UTC().Format(time.RFC3339),
				DataVersion:    index.Version,
				CacheStatus:    "hit",
				CacheExpiresAt: entry.expiresAt.UTC().Format(time.RFC3339),
				DefaultWindow:  startUIUsageQueryIsDefaultWindow(filters),
				SessionRoots:   index.SessionRootsScanned,
				IndexLoadMS:    durationMillis(indexLoadDuration),
				TotalBuildMS:   durationMillis(time.Since(requestStarted)),
			}), nil
		}
		h.usageCacheMu.Unlock()
	}

	sourceBuildStarted := time.Now()
	source, err := startUIUsageSourceForReport(index, options)
	if err != nil {
		return startUIUsageReport{}, err
	}
	sourceBuildDuration := time.Since(sourceBuildStarted)
	summaryBuildStarted := time.Now()
	summary := buildUsageSummaryReportFromSource(source)
	summaryBuildDuration := time.Since(summaryBuildStarted)
	analyticsBuildStarted := time.Now()
	analytics := buildUsageAnalyticsReportFromSource(source)
	analyticsBuildDuration := time.Since(analyticsBuildStarted)
	groupBuildStarted := time.Now()
	byRoot := buildUsageGroups(source.Records, "root")
	byActivity := buildUsageGroups(source.Records, "activity")
	byPhase := buildUsageGroups(source.Records, "phase")
	byLane := buildUsageGroups(source.Records, "lane")
	byModel := buildUsageGroups(source.Records, "model")
	groupBuildDuration := time.Since(groupBuildStarted)
	topSessionsStarted := time.Now()
	topSessions := buildUsageTopReportFromSource(source, "session", 10).Sessions
	topSessionsDuration := time.Since(topSessionsStarted)
	report := startUIUsageReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Version:     index.Version,
		TimeBasis:   source.TimeBasis,
		Coverage:    source.Coverage,
		Filters:     filters,
		Summary:     summary,
		ByRoot:      byRoot,
		ByActivity:  byActivity,
		ByPhase:     byPhase,
		ByLane:      byLane,
		ByDay:       append([]usageGroupRow(nil), source.DayGroups...),
		ByModel:     byModel,
		TopSessions: topSessions,
		Insights:    analytics.Insights,
	}
	cacheExpiresAt := time.Time{}
	if startUIUsageCacheTTL > 0 {
		h.usageCacheMu.Lock()
		if h.usageCache == nil {
			h.usageCache = map[string]startUIUsageCacheEntry{}
		}
		cacheExpiresAt = now.Add(startUIUsageCacheTTL)
		cached := report
		cached.Diagnostics = startUIUsageDiagnostics{}
		h.usageCache[cacheKey] = startUIUsageCacheEntry{
			expiresAt: cacheExpiresAt,
			report:    cached,
		}
		h.usageCacheMu.Unlock()
	}
	cacheExpiresAtValue := ""
	if !cacheExpiresAt.IsZero() {
		cacheExpiresAtValue = cacheExpiresAt.UTC().Format(time.RFC3339)
	}
	return startUIUsageReportWithDiagnostics(report, startUIUsageDiagnostics{
		SampledAt:        time.Now().UTC().Format(time.RFC3339),
		DataVersion:      index.Version,
		CacheStatus:      "miss",
		CacheExpiresAt:   cacheExpiresAtValue,
		DefaultWindow:    startUIUsageQueryIsDefaultWindow(filters),
		SessionRoots:     index.SessionRootsScanned,
		IndexLoadMS:      durationMillis(indexLoadDuration),
		SourceBuildMS:    durationMillis(sourceBuildDuration),
		SummaryBuildMS:   durationMillis(summaryBuildDuration),
		AnalyticsBuildMS: durationMillis(analyticsBuildDuration),
		GroupBuildMS:     durationMillis(groupBuildDuration),
		TopSessionsMS:    durationMillis(topSessionsDuration),
		TotalBuildMS:     durationMillis(time.Since(requestStarted)),
	}), nil
}

func startUIUsageReportWithDiagnostics(report startUIUsageReport, diagnostics startUIUsageDiagnostics) startUIUsageReport {
	clone := report
	clone.Diagnostics = diagnostics
	return clone
}

func startUIUsageQueryIsDefaultWindow(filters startUIUsageFilters) bool {
	return strings.TrimSpace(filters.Since) == "30d" &&
		defaultString(strings.TrimSpace(filters.Root), "all") == "all" &&
		strings.TrimSpace(filters.Project) == "" &&
		strings.TrimSpace(filters.Repo) == "" &&
		strings.TrimSpace(filters.Activity) == "" &&
		strings.TrimSpace(filters.Phase) == "" &&
		strings.TrimSpace(filters.Model) == ""
}

func durationMillis(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}

func startUIUsageCacheKey(filters startUIUsageFilters, version string) string {
	return strings.Join([]string{
		strings.TrimSpace(version),
		strings.TrimSpace(filters.Since),
		strings.TrimSpace(filters.Project),
		strings.TrimSpace(filters.Repo),
		defaultString(strings.TrimSpace(filters.Root), "all"),
		strings.TrimSpace(filters.Activity),
		strings.TrimSpace(filters.Phase),
		strings.TrimSpace(filters.Model),
	}, "\x00")
}

func startUIUsageCacheVersion(index startUIUsageIndexState, options usageOptions) string {
	if strings.TrimSpace(options.Since) == "" {
		return strings.TrimSpace(index.Version)
	}
	return strings.Join([]string{
		strings.TrimSpace(index.Version),
		strings.TrimSpace(index.UpdatedAt),
		strings.TrimSpace(index.WorkSyncUpdatedAt),
	}, "\x00")
}

// startUIUsageIndexPath is retained only as a compatibility-import location for
// old Start UI usage snapshots. Live usage state is read from SQLite.
func startUIUsageIndexPath() string {
	return filepath.Join(githubNanaHome(), "usage", "state.json")
}

// legacyStartUIUsageIndexPath is retained only as a compatibility-import
// location for pre-SQLite Start UI usage snapshots.
func legacyStartUIUsageIndexPath() string {
	return filepath.Join(githubNanaHome(), "start", "ui", "usage-index.json")
}

func startUIOverviewCachePath() string {
	return filepath.Join(githubNanaHome(), "start", "ui", "overview-cache.json")
}

func (h *startUIAPI) loadPersistedOverviewCache() {
	state := readStartUIPersistedOverviewCacheState(startUIOverviewCachePath())
	if strings.TrimSpace(state.OverviewVersion) == "" || strings.TrimSpace(state.DependencyToken) == "" {
		return
	}
	current := snapshotStartUIOverviewDependencies(h.cwd)
	if current.token != state.DependencyToken {
		return
	}
	overview := state.Overview
	if strings.TrimSpace(overview.GeneratedAt) == "" {
		overview.GeneratedAt = state.GeneratedAt
	}
	h.overviewCacheMu.Lock()
	defer h.overviewCacheMu.Unlock()
	h.overviewCache = startUIOverviewCache{
		valid:     true,
		token:     current.token,
		version:   state.OverviewVersion,
		checkedAt: time.Now(),
		deps:      current.deps,
		overview:  overview,
		events:    startUIOverviewEventsPayload(overview, state.OverviewVersion, ""),
	}
	h.invalidateEventsCache()
}

func (h *startUIAPI) peekUsageDataVersion() string {
	h.usageIndexMu.Lock()
	if h.usageIndexCache.valid {
		version := strings.TrimSpace(h.usageIndexCache.value.Version)
		h.usageIndexMu.Unlock()
		return version
	}
	h.usageIndexMu.Unlock()
	return loadUsageSQLiteVersion()
}

func (h *startUIAPI) loadUsageIndex() (startUIUsageIndexState, error) {
	now := time.Now()
	h.usageIndexMu.Lock()
	if h.usageIndexCache.valid && startUIUsageIndexProbeInterval > 0 && now.Sub(h.usageIndexCache.checkedAt) < startUIUsageIndexProbeInterval {
		index := h.usageIndexCache.value
		h.usageIndexMu.Unlock()
		return index, nil
	}
	h.usageIndexMu.Unlock()

	index, err := refreshStartUIUsageIndex(h.cwd, "")
	if err != nil {
		return startUIUsageIndexState{}, err
	}

	h.usageIndexMu.Lock()
	previousVersion := strings.TrimSpace(h.usageIndexCache.value.Version)
	h.usageIndexCache.valid = true
	h.usageIndexCache.checkedAt = time.Now()
	h.usageIndexCache.value = index
	h.usageIndexMu.Unlock()
	if strings.TrimSpace(index.Version) != previousVersion {
		h.clearUsageCache()
		h.invalidateEventsCache()
		h.notifyEventsBroadcaster()
	}
	return index, nil
}

func (h *startUIAPI) loadUsageIndexForReport(options usageOptions) (startUIUsageIndexState, error) {
	return h.loadUsageIndex()
}

func (h *startUIAPI) clearUsageCache() {
	h.usageCacheMu.Lock()
	defer h.usageCacheMu.Unlock()
	h.usageCache = nil
}

func refreshStartUIUsageIndex(cwd string, path string) (startUIUsageIndexState, error) {
	return refreshUsageStore(cwd, path)
}

func readStartUIUsageIndexState(path string) startUIUsageIndexState {
	content, err := os.ReadFile(path)
	if err != nil {
		return startUIUsageIndexState{}
	}
	state := startUIUsageIndexState{}
	if err := json.Unmarshal(content, &state); err != nil {
		return startUIUsageIndexState{}
	}
	if state.SchemaVersion != startUIUsageIndexSchemaVersion {
		return startUIUsageIndexState{}
	}
	return state
}

func readStartUIPersistedOverviewCacheState(path string) startUIPersistedOverviewCacheState {
	content, err := os.ReadFile(path)
	if err != nil {
		return startUIPersistedOverviewCacheState{}
	}
	state := startUIPersistedOverviewCacheState{}
	if err := json.Unmarshal(content, &state); err != nil {
		return startUIPersistedOverviewCacheState{}
	}
	if state.SchemaVersion != startUIOverviewCacheSchemaVersion {
		return startUIPersistedOverviewCacheState{}
	}
	return state
}

func writeStartUIPersistedOverviewCacheState(path string, value startUIPersistedOverviewCacheState) error {
	return writeStartUIRuntimeJSONAtomically(path, value)
}

func writeStartUIRuntimeJSONAtomically(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(content, '\n')); err != nil {
		file.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	recordRuntimeArtifactWrite(path)
	return nil
}

func startUIUsageIndexVersion(entries []startUIUsageIndexEntry, sessionRootsScanned int) string {
	return hashJSON(struct {
		SessionRootsScanned int                      `json:"session_roots_scanned"`
		Entries             []startUIUsageIndexEntry `json:"entries"`
	}{
		SessionRootsScanned: sessionRootsScanned,
		Entries:             entries,
	})
}

func startUIUsageSourceForReport(index startUIUsageIndexState, options usageOptions) (usageReportSource, error) {
	if strings.TrimSpace(options.Since) == "" {
		records := usageRecordsForState(index, options)
		return usageReportSource{
			Records:             records,
			DayGroups:           buildUsageGroups(records, "day"),
			SessionRootsScanned: index.SessionRootsScanned,
			TimeBasis:           usageTimeBasisCumulative,
			Coverage:            usageCoverageFull,
		}, nil
	}
	source, err := loadWindowedUsageReportSourceFromSQLite(options, index.SessionRootsScanned)
	if err != nil {
		return usageReportSource{}, err
	}
	source.SessionRootsScanned = index.SessionRootsScanned
	return source, nil
}

func (h *startUIAPI) buildEventsPayload() (map[string]any, error) {
	overview, err := h.buildOverview()
	if err != nil {
		return nil, err
	}
	usageVersion := h.peekUsageDataVersion()
	h.overviewCacheMu.Lock()
	if h.overviewCache.valid && h.overviewCache.overview.GeneratedAt == overview.GeneratedAt && h.overviewCache.events != nil {
		events := cloneStartUIEventPayload(h.overviewCache.events)
		events["usage_version"] = usageVersion
		h.overviewCacheMu.Unlock()
		return events, nil
	}
	h.overviewCacheMu.Unlock()
	return startUIOverviewEventsPayload(overview, startUIOverviewVersion(overview), usageVersion), nil
}

func (h *startUIAPI) loadCachedEventsPayload() (map[string]any, string, error) {
	for {
		now := time.Now()
		h.eventsCacheMu.Lock()
		if h.eventsCache.valid && startUIEventsCacheProbeInterval > 0 && now.Sub(h.eventsCache.checkedAt) < startUIEventsCacheProbeInterval {
			payload := cloneStartUIEventPayload(h.eventsCache.payload)
			hash := h.eventsCache.hash
			h.eventsCacheMu.Unlock()
			return payload, hash, nil
		}
		if h.eventsBuildCh != nil {
			waitCh := h.eventsBuildCh
			h.eventsCacheMu.Unlock()
			<-waitCh
			continue
		}
		buildCh := make(chan struct{})
		h.eventsBuildCh = buildCh
		h.eventsCacheMu.Unlock()

		payload, err := h.buildEventsPayload()
		hash := ""
		if err == nil {
			hash = hashStartUIEventPayload(payload)
		}

		h.eventsCacheMu.Lock()
		if err == nil {
			h.eventsCache = startUIEventsPayloadCache{
				valid:     true,
				checkedAt: time.Now(),
				hash:      hash,
				payload:   cloneStartUIEventPayload(payload),
			}
		} else {
			h.eventsCache.valid = false
			h.eventsCache.checkedAt = time.Time{}
		}
		h.eventsBuildCh = nil
		close(buildCh)
		h.eventsCacheMu.Unlock()
		if err != nil {
			return nil, "", err
		}
		return payload, hash, nil
	}
}

func (h *startUIAPI) ensureEventsBroadcasterStarted() {
	h.eventsStreamMu.Lock()
	defer h.eventsStreamMu.Unlock()
	if h.eventsNotifyCh != nil {
		return
	}
	h.eventsNotifyCh = make(chan struct{}, 1)
	h.eventsStopCh = make(chan struct{})
	h.eventsDoneCh = make(chan struct{})
	h.eventsClients = map[chan map[string]any]struct{}{}
	go h.runEventsBroadcaster(h.eventsNotifyCh, h.eventsStopCh, h.eventsDoneCh)
}

func (h *startUIAPI) runEventsBroadcaster(notifyCh <-chan struct{}, stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-notifyCh:
		case <-ticker.C:
		}
		payload, hash, err := h.loadCachedEventsPayload()
		if err != nil {
			continue
		}

		h.eventsStreamMu.Lock()
		if hash == h.eventsLastHash {
			h.eventsStreamMu.Unlock()
			continue
		}
		h.eventsLastHash = hash
		h.eventsLastPayload = cloneStartUIEventPayload(payload)
		subscribers := make([]chan map[string]any, 0, len(h.eventsClients))
		for subscriber := range h.eventsClients {
			subscribers = append(subscribers, subscriber)
		}
		h.eventsStreamMu.Unlock()

		for _, subscriber := range subscribers {
			cloned := cloneStartUIEventPayload(payload)
			select {
			case subscriber <- cloned:
			default:
				select {
				case <-subscriber:
				default:
				}
				select {
				case subscriber <- cloned:
				default:
				}
			}
		}
	}
}

func (h *startUIAPI) subscribeEvents() chan map[string]any {
	h.ensureEventsBroadcasterStarted()
	subscriber := make(chan map[string]any, 1)
	h.eventsStreamMu.Lock()
	h.eventsClients[subscriber] = struct{}{}
	if h.eventsLastPayload != nil {
		subscriber <- cloneStartUIEventPayload(h.eventsLastPayload)
	}
	h.eventsStreamMu.Unlock()
	return subscriber
}

func (h *startUIAPI) unsubscribeEvents(subscriber chan map[string]any) {
	if subscriber == nil {
		return
	}
	h.eventsStreamMu.Lock()
	if h.eventsClients != nil {
		delete(h.eventsClients, subscriber)
	}
	h.eventsStreamMu.Unlock()
}

func (h *startUIAPI) notifyEventsBroadcaster() {
	h.eventsStreamMu.Lock()
	notifyCh := h.eventsNotifyCh
	h.eventsStreamMu.Unlock()
	if notifyCh == nil {
		return
	}
	select {
	case notifyCh <- struct{}{}:
	default:
	}
}

func (h *startUIAPI) stopEventsBroadcaster() {
	h.eventsStreamMu.Lock()
	stopCh := h.eventsStopCh
	doneCh := h.eventsDoneCh
	clients := h.eventsClients
	h.eventsNotifyCh = nil
	h.eventsStopCh = nil
	h.eventsDoneCh = nil
	h.eventsClients = nil
	h.eventsLastHash = ""
	h.eventsLastPayload = nil
	h.eventsStreamMu.Unlock()

	for subscriber := range clients {
		close(subscriber)
	}
	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}
}

func startUIOverviewEventsPayload(overview startUIOverview, overviewVersion string, usageVersion string) map[string]any {
	return map[string]any{
		"generated_at":     overview.GeneratedAt,
		"overview_version": overviewVersion,
		"usage_version":    usageVersion,
	}
}

func (h *startUIAPI) invalidateOverviewCache() {
	h.overviewCacheMu.Lock()
	defer h.overviewCacheMu.Unlock()
	h.overviewCache.valid = false
	h.sectionCacheMu.Lock()
	h.sectionCaches = startUIOverviewSectionCaches{}
	h.sectionCacheMu.Unlock()
	h.invalidateEventsCache()
	h.notifyEventsBroadcaster()
}

func (h *startUIAPI) expireOverviewSectionCaches() {
	h.sectionCacheMu.Lock()
	defer h.sectionCacheMu.Unlock()
	h.sectionCaches.repos.checkedAt = time.Time{}
	h.sectionCaches.workRuns.checkedAt = time.Time{}
	h.sectionCaches.workItems.checkedAt = time.Time{}
	h.sectionCaches.investigationCount.checkedAt = time.Time{}
	h.sectionCaches.hud.checkedAt = time.Time{}
	h.sectionCaches.hudGitBranch.checkedAt = time.Time{}
}

func (h *startUIAPI) invalidateEventsCache() {
	h.eventsCacheMu.Lock()
	defer h.eventsCacheMu.Unlock()
	h.eventsCache = startUIEventsPayloadCache{}
}

func (h *startUIAPI) currentOverviewSectionCacheToken() string {
	h.sectionCacheMu.Lock()
	defer h.sectionCacheMu.Unlock()
	parts := []string{
		h.sectionCaches.repos.token,
		h.sectionCaches.workRuns.token,
		h.sectionCaches.workItems.token,
		h.sectionCaches.investigationCount.token,
		h.sectionCaches.hud.token,
		h.sectionCaches.hudGitBranch.token,
	}
	if strings.TrimSpace(strings.Join(parts, "")) == "" {
		return ""
	}
	return strings.Join(parts, "\x00")
}

func (h *startUIAPI) expireOverviewSectionsForChangedDependencies(previousDeps []string) {
	if len(previousDeps) == 0 {
		h.expireOverviewSectionCaches()
		return
	}
	currentLocalWorkSnapshot := h.persistentLocalWorkReadSnapshot()
	h.localWorkReadMu.Lock()
	if h.localWorkRead != nil && h.localWorkToken != currentLocalWorkSnapshot.token {
		h.localWorkToken = currentLocalWorkSnapshot.token
		h.workMetaCache = startUIWorkMetadataCache{}
	}
	h.localWorkReadMu.Unlock()
	h.sectionCacheMu.Lock()
	defer h.sectionCacheMu.Unlock()
	if snapshotStartUIDependencies(listStartUIRepoSummaryDependencies(h.cwd)).token != h.sectionCaches.repos.token {
		h.sectionCaches.repos.checkedAt = time.Time{}
	}
	if snapshotStartUIDependencies(h.listStartUIWorkRunDependencies()).token != h.sectionCaches.workRuns.token {
		h.sectionCaches.workRuns.checkedAt = time.Time{}
	}
	if snapshotStartUIDependencies(listStartUIWorkItemDependencies()).token != h.sectionCaches.workItems.token {
		h.sectionCaches.workItems.checkedAt = time.Time{}
	}
	if snapshotStartUIDependencies(listStartUIInvestigationCountDependencies(h.cwd)).token != h.sectionCaches.investigationCount.token {
		h.sectionCaches.investigationCount.checkedAt = time.Time{}
	}
	if snapshotStartUIDependencies(listStartUIHUDSectionDependencies(h.cwd)).token != h.sectionCaches.hud.token {
		h.sectionCaches.hud.checkedAt = time.Time{}
	}
	if snapshotStartUIDependencies(listStartUIHUDGitDependencies(h.cwd)).token != h.sectionCaches.hudGitBranch.token {
		h.sectionCaches.hudGitBranch.checkedAt = time.Time{}
	}
}

func (h *startUIAPI) closePersistentLocalWorkReadStore() error {
	h.localWorkReadMu.Lock()
	store := h.localWorkRead
	retired := h.localWorkReadRetired
	h.localWorkRead = nil
	h.localWorkReadRetired = nil
	h.localWorkReadBuildCh = nil
	h.localWorkToken = ""
	h.workMetaCache = startUIWorkMetadataCache{}
	h.localWorkReadMu.Unlock()

	var closeErr error
	if store != nil {
		closeErr = store.Close()
	}
	if retired != nil {
		if err := retired.Close(); closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (h *startUIAPI) persistentLocalWorkReadSnapshot() startUIDependencySnapshot {
	return snapshotStartUIDependencies(listStartUILocalWorkDBDependencies())
}

func (h *startUIAPI) persistentLocalWorkReadStore(snapshot startUIDependencySnapshot) (*localWorkDBStore, error) {
	for {
		h.localWorkReadMu.Lock()
		if h.localWorkRead != nil {
			if h.localWorkRead.emptyReadOnly {
				if _, err := os.Stat(localWorkDBPath()); err == nil {
					store := h.localWorkRead
					retired := h.localWorkReadRetired
					h.localWorkRead = nil
					h.localWorkReadRetired = nil
					h.localWorkToken = ""
					h.workMetaCache = startUIWorkMetadataCache{}
					h.localWorkReadMu.Unlock()
					_ = store.Close()
					if retired != nil {
						_ = retired.Close()
					}
					continue
				}
			}
			if h.localWorkToken != snapshot.token {
				h.workMetaCache = startUIWorkMetadataCache{}
				h.localWorkToken = snapshot.token
			}
			store := h.localWorkRead
			h.localWorkReadMu.Unlock()
			return store, nil
		}
		if h.localWorkReadBuildCh != nil {
			waitCh := h.localWorkReadBuildCh
			h.localWorkReadMu.Unlock()
			<-waitCh
			continue
		}
		buildCh := make(chan struct{})
		h.localWorkReadBuildCh = buildCh
		h.localWorkReadMu.Unlock()

		store, err := localWorkOpenReadStore()
		openedSnapshot := h.persistentLocalWorkReadSnapshot()
		h.localWorkReadMu.Lock()
		if err == nil && h.localWorkRead == nil {
			h.localWorkRead = store
			h.localWorkToken = openedSnapshot.token
		}
		existing := h.localWorkRead
		h.localWorkReadBuildCh = nil
		close(buildCh)
		h.localWorkReadMu.Unlock()
		if err != nil {
			return nil, err
		}
		if existing != store {
			_ = store.Close()
		}
		return existing, nil
	}
}

func (h *startUIAPI) resetPersistentLocalWorkReadStore() {
	_ = h.closePersistentLocalWorkReadStore()
}

func startUIWithPersistentLocalWorkReadStore[T any](h *startUIAPI, readFn func(*localWorkDBStore) (T, error)) (T, error) {
	var zero T
	attempts := localWorkReadRetryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		snapshot := h.persistentLocalWorkReadSnapshot()
		store, err := h.persistentLocalWorkReadStore(snapshot)
		if err != nil {
			if !isLocalWorkDBLockError(err) || attempt == attempts {
				return zero, err
			}
			localWorkRetrySleep(localWorkReadRetryDelay)
			continue
		}
		value, readErr := readFn(store)
		if readErr == nil {
			return value, nil
		}
		if !isLocalWorkDBLockError(readErr) || attempt == attempts {
			return zero, readErr
		}
		h.resetPersistentLocalWorkReadStore()
		localWorkRetrySleep(localWorkReadRetryDelay)
	}
	return zero, fmt.Errorf("persistent local work DB read retry exhausted")
}

func (h *startUIAPI) loadIndexedGithubManifestDependencies() []string {
	snapshot := h.persistentLocalWorkReadSnapshot()

	h.localWorkReadMu.Lock()
	if h.workMetaCache.indexedGithubManifestDepsToken == snapshot.token {
		cached := append([]string(nil), h.workMetaCache.indexedGithubManifestDeps...)
		h.localWorkReadMu.Unlock()
		return cached
	}
	h.localWorkReadMu.Unlock()

	deps, err := startUIWithPersistentLocalWorkReadStore(h, func(store *localWorkDBStore) ([]string, error) {
		rows, err := store.db.Query(`SELECT backend, manifest_path FROM work_run_index ORDER BY updated_at DESC LIMIT ?`, startUIOverviewRunLimit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		seen := map[string]bool{}
		paths := []string{}
		for rows.Next() {
			var backend string
			var manifestPath sql.NullString
			if err := rows.Scan(&backend, &manifestPath); err != nil {
				continue
			}
			if backend != "github" || !manifestPath.Valid {
				continue
			}
			path := filepath.Clean(strings.TrimSpace(manifestPath.String))
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		sort.Strings(paths)
		return paths, nil
	})
	if err != nil {
		return nil
	}

	h.localWorkReadMu.Lock()
	if h.localWorkToken == snapshot.token {
		h.workMetaCache.indexedGithubManifestDepsToken = snapshot.token
		h.workMetaCache.indexedGithubManifestDeps = append([]string(nil), deps...)
	}
	h.localWorkReadMu.Unlock()
	return deps
}

func (h *startUIAPI) listStartUIWorkRunDependencies() []string {
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
	addStartUILocalWorkDBDependencies(add)
	for _, path := range h.loadIndexedGithubManifestDependencies() {
		add(path)
	}
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "start-state.json", add)
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "settings.json", add)
	sort.Strings(paths)
	return paths
}

func (h *startUIAPI) loadOverviewReposSection() (startUIOverviewReposSection, error) {
	return startUILoadCachedSection(&h.sectionCacheMu, &h.sectionCaches.repos, startUISectionCacheProbeInterval, func() []string {
		return listStartUIRepoSummaryDependencies(h.cwd)
	}, func() (startUIOverviewReposSection, error) {
		repos, err := listStartUIRepoSummaries(true)
		if err != nil {
			return startUIOverviewReposSection{}, err
		}
		return startUIOverviewReposSection{summaries: repos}, nil
	})
}

func (h *startUIAPI) loadOverviewWorkRunsSection() ([]startUIWorkRun, error) {
	return startUILoadCachedSection(&h.sectionCacheMu, &h.sectionCaches.workRuns, startUISectionCacheProbeInterval, func() []string {
		return h.listStartUIWorkRunDependencies()
	}, func() ([]startUIWorkRun, error) {
		return h.loadStartUIWorkRunsPersistent(startUIOverviewRunLimit)
	})
}

func (h *startUIAPI) loadOverviewWorkItemsSection() (startUIOverviewWorkItemsSection, error) {
	return startUILoadCachedSection(&h.sectionCacheMu, &h.sectionCaches.workItems, startUISectionCacheProbeInterval, listStartUIWorkItemDependencies, func() (startUIOverviewWorkItemsSection, error) {
		return startUIWithPersistentLocalWorkReadStore(h, func(store *localWorkDBStore) (startUIOverviewWorkItemsSection, error) {
			displayItems, err := store.listWorkItems(workItemListOptions{
				Limit:         10,
				IncludeHidden: false,
				OnlyHidden:    false,
			})
			if err != nil {
				return startUIOverviewWorkItemsSection{}, err
			}
			rawItems, err := store.listWorkItems(workItemListOptions{
				Limit:         200,
				IncludeHidden: false,
				OnlyHidden:    false,
			})
			if err != nil {
				return startUIOverviewWorkItemsSection{}, err
			}
			hiddenCount := 0
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE hidden = 1`).Scan(&hiddenCount); err != nil {
				return startUIOverviewWorkItemsSection{}, err
			}
			pendingCount := 0
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE hidden = 0 AND status IN (?, ?, ?, ?, ?)`,
				workItemStatusQueued,
				workItemStatusRunning,
				workItemStatusDraftReady,
				workItemStatusNeedsRouting,
				workItemStatusFailed,
			).Scan(&pendingCount); err != nil {
				return startUIOverviewWorkItemsSection{}, err
			}
			items := make([]startUIWorkItem, 0, len(displayItems))
			for _, item := range displayItems {
				items = append(items, startUIWorkItemFromItem(item))
			}
			return startUIOverviewWorkItemsSection{
				raw:          rawItems,
				items:        items,
				hiddenCount:  hiddenCount,
				pendingCount: pendingCount,
			}, nil
		})
	})
}

func (h *startUIAPI) loadOverviewInvestigationCountSection() (int, error) {
	return startUILoadCachedSection(&h.sectionCacheMu, &h.sectionCaches.investigationCount, startUISectionCacheProbeInterval, func() []string {
		return listStartUIInvestigationCountDependencies(h.cwd)
	}, func() (int, error) {
		items, err := listStartUIInvestigations(h.cwd)
		if err != nil {
			return 0, err
		}
		return len(items), nil
	})
}

func (h *startUIAPI) loadOverviewHUDSection() (HUDRenderContext, error) {
	return startUILoadCachedSection(&h.sectionCacheMu, &h.sectionCaches.hud, startUISectionCacheProbeInterval, func() []string {
		return listStartUIHUDSectionDependencies(h.cwd)
	}, func() (HUDRenderContext, error) {
		return h.loadHUD()
	})
}

func startUIStripRepoSummaryState(repos []startUIRepoSummary) []startUIRepoSummary {
	if len(repos) == 0 {
		return nil
	}
	out := make([]startUIRepoSummary, 0, len(repos))
	for _, repo := range repos {
		cloned := repo
		cloned.State = nil
		out = append(out, cloned)
	}
	return out
}

func startUICountFeedbackItems(items []workItem) (int, int) {
	reviewCount := 0
	replyCount := 0
	for _, item := range items {
		if startUIFeedbackItemMatches("review", item) {
			reviewCount++
		}
		if startUIFeedbackItemMatches("reply", item) {
			replyCount++
		}
	}
	return reviewCount, replyCount
}

func startUICountApprovals(runs []startUIWorkRun, items []workItem, repos []startUIRepoSummary) int {
	count := 0
	for _, run := range runs {
		if run.AttentionState != "blocked" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(run.Status), "paused") {
			continue
		}
		count++
	}
	for _, item := range items {
		if item.Hidden || item.Status != workItemStatusDraftReady || item.LatestDraft == nil {
			continue
		}
		count++
	}
	for _, repo := range repos {
		if repo.State == nil {
			continue
		}
		for _, task := range repo.State.ServiceTasks {
			if task.Kind == startTaskKindPreflight && task.Status == startWorkServiceTaskFailed && strings.TrimSpace(task.LastError) != "" {
				count++
			}
		}
		for _, job := range repo.State.ScoutJobs {
			if startWorkScoutJobNeedsApproval(job) {
				count++
			}
		}
	}
	return count
}

func startUIOverviewVersion(overview startUIOverview) string {
	clone := overview
	clone.GeneratedAt = ""
	return hashJSON(clone)
}

func cloneStartUIEventPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
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

func listStartUIRepoSummaryDependencies(cwd string) []string {
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
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "start-state.json", add)
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "settings.json", add)
	addStartUIScoutPolicyDependencies(add)
	repoSlugs, err := listStartUIRepoSlugs()
	if err == nil {
		for _, repoSlug := range repoSlugs {
			sourcePath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
			add(sourcePath)
			addStartUIRepoLockDependencies(sourcePath, add)
		}
	}
	sort.Strings(paths)
	return paths
}

func listStartUIWorkRunDependencies(cwd string) []string {
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
	addStartUILocalWorkDBDependencies(add)
	addStartUIIndexedGithubManifestDependencies(add)
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "start-state.json", add)
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "settings.json", add)
	sort.Strings(paths)
	return paths
}

func (h *startUIAPI) listStartUIOverviewDependencies() []string {
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

	addStartUILocalWorkDBDependencies(add)
	for _, path := range h.loadIndexedGithubManifestDependencies() {
		add(path)
	}
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "start-state.json", add)
	addStartUIRepoTreeDependencies(githubWorkReposRoot(), "settings.json", add)
	addStartUIScoutPolicyDependencies(add)
	addStartUIInvestigationDependencies(h.cwd, add)
	addStartUIHUDDependencies(h.cwd, add)
	sort.Strings(paths)
	return paths
}

func listStartUIWorkItemDependencies() []string {
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
	addStartUILocalWorkDBDependencies(add)
	sort.Strings(paths)
	return paths
}

func listStartUIInvestigationCountDependencies(cwd string) []string {
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
	addStartUIInvestigationDependencies(cwd, add)
	sort.Strings(paths)
	return paths
}

func listStartUIHUDSectionDependencies(cwd string) []string {
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
	addStartUIHUDDependencies(cwd, add)
	sort.Strings(paths)
	return paths
}

func listStartUIHUDGitDependencies(cwd string) []string {
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
	add(filepath.Join(cwd, ".nana", "hud-config.json"))
	addStartUIGitHUDDependencies(cwd, add)
	sort.Strings(paths)
	return paths
}

func addStartUILocalWorkDBDependencies(add func(string)) {
	dbPath := localWorkDBPath()
	add(dbPath)
	add(dbPath + "-wal")
	add(dbPath + "-shm")
}

func listStartUILocalWorkDBDependencies() []string {
	paths := []string{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		paths = append(paths, filepath.Clean(path))
	}
	addStartUILocalWorkDBDependencies(add)
	sort.Strings(paths)
	return paths
}

func addStartUIRepoLockDependencies(repoPath string, add func(string)) {
	normalized, err := normalizeRepoAccessLockPath(repoPath)
	if err != nil {
		return
	}
	lockRoot := repoAccessLockRoot(normalized)
	add(filepath.Join(lockRoot, "target.json"))
	add(filepath.Join(lockRoot, "writer.json"))
	readersDir := filepath.Join(lockRoot, "readers")
	entries, err := os.ReadDir(readersDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		add(filepath.Join(readersDir, entry.Name()))
	}
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

func snapshotStartUIOverviewDependencies(cwd string) startUIDependencySnapshot {
	deps := listStartUIOverviewDependencies(cwd)
	return snapshotStartUIDependencies(deps)
}

func sameStartUIOverviewDependencySnapshot(left startUIDependencySnapshot, right startUIDependencySnapshot) bool {
	return sameStartUIDependencySnapshot(left, right)
}

func snapshotStartUIDependencies(deps []string) startUIDependencySnapshot {
	sorted := append([]string(nil), deps...)
	sort.Strings(sorted)
	return startUIDependencySnapshot{
		deps:  sorted,
		token: fingerprintStartUIOverviewDependencies(sorted),
	}
}

func sameStartUIDependencySnapshot(left startUIDependencySnapshot, right startUIDependencySnapshot) bool {
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

func startUILoadCachedSection[T any](mu *sync.Mutex, cache *startUISectionCache[T], probeInterval time.Duration, depsFn func() []string, loadFn func() (T, error)) (T, error) {
	var zero T
	now := time.Now()
	mu.Lock()
	if cache.valid {
		if probeInterval > 0 && now.Sub(cache.checkedAt) < probeInterval {
			value := cache.value
			mu.Unlock()
			return value, nil
		}
		snapshot := snapshotStartUIDependencies(depsFn())
		if snapshot.token == cache.token {
			cache.checkedAt = now
			value := cache.value
			mu.Unlock()
			return value, nil
		}
	}
	mu.Unlock()

	value, snapshot, stable, err := startUIBuildCachedSectionWithStableSnapshot(depsFn, loadFn)
	if err != nil {
		return zero, err
	}
	mu.Lock()
	defer mu.Unlock()
	if !stable {
		cache.valid = false
		return value, nil
	}
	cache.valid = true
	cache.token = snapshot.token
	cache.checkedAt = time.Now()
	cache.deps = snapshot.deps
	cache.value = value
	return value, nil
}

func startUIBuildCachedSectionWithStableSnapshot[T any](depsFn func() []string, loadFn func() (T, error)) (T, startUIDependencySnapshot, bool, error) {
	var zero T
	before := snapshotStartUIDependencies(depsFn())
	value, err := loadFn()
	if err != nil {
		return zero, startUIDependencySnapshot{}, false, err
	}
	after := snapshotStartUIDependencies(depsFn())
	if sameStartUIDependencySnapshot(before, after) {
		return value, after, true, nil
	}
	value, err = loadFn()
	if err != nil {
		return zero, startUIDependencySnapshot{}, false, err
	}
	finalSnapshot := snapshotStartUIDependencies(depsFn())
	return value, finalSnapshot, sameStartUIDependencySnapshot(after, finalSnapshot), nil
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
	gitBranch, err := startUILoadCachedSection(&h.sectionCacheMu, &h.sectionCaches.hudGitBranch, startUISectionCacheProbeInterval, func() []string {
		return listStartUIHUDGitDependencies(h.cwd)
	}, func() (string, error) {
		return buildGitBranchLabel(h.cwd, config), nil
	})
	if err != nil {
		return HUDRenderContext{}, err
	}
	return readAllHUDStateWithGitBranch(h.cwd, config, gitBranch)
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
	if strings.TrimSpace(issue.BlockedReason) == "work type unresolved" {
		attention = "blocked"
	}
	return startUIIssueQueueItem{
		ID:                fmt.Sprintf("%s#%d", repoSlug, issue.SourceNumber),
		RepoSlug:          repoSlug,
		SourceNumber:      issue.SourceNumber,
		SourceURL:         issue.SourceURL,
		Title:             issue.Title,
		WorkType:          issue.WorkType,
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
		PauseReason:     manifest.PauseReason,
		PauseUntil:      manifest.PauseUntil,
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
	case strings.Contains(normalized, "pause"):
		return "blocked"
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
		PauseReason:          item.PauseReason,
		PauseUntil:           item.PauseUntil,
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
		if strings.EqualFold(strings.TrimSpace(run.Status), "paused") {
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
		for _, task := range repo.State.ServiceTasks {
			if task.Kind != startTaskKindPreflight || task.Status != startWorkServiceTaskFailed || strings.TrimSpace(task.LastError) == "" {
				continue
			}
			out = append(out, startUIApprovalItemFromServiceTask(repo.RepoSlug, task))
		}
		for _, job := range repo.State.ScoutJobs {
			if !startWorkScoutJobNeedsApproval(job) {
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
			if strings.TrimSpace(item.ScheduleAt) != "" {
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
		if detail.ResolveAllowed && actionKind == "open_run" {
			actionKind = "resolve_run"
		} else if detail.SyncAllowed && actionKind == "open_run" {
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

func startUIApprovalItemFromServiceTask(repoSlug string, task startWorkServiceTask) startUIApprovalQueueItem {
	subject := "Repo automation preflight"
	if strings.TrimSpace(repoSlug) != "" {
		subject = repoSlug + " automation preflight"
	}
	return startUIApprovalQueueItem{
		ID:             "service:" + task.ID,
		Kind:           "repo_service",
		RepoSlug:       repoSlug,
		Subject:        subject,
		Status:         task.Status,
		Reason:         defaultString(strings.TrimSpace(task.LastError), "repo automation preflight blocked"),
		NextAction:     "Install/authenticate `gh` and restore managed-source origin SSH access; `nana start` will retry on the next cycle.",
		ActionKind:     "",
		UpdatedAt:      task.UpdatedAt,
		AttentionState: "failed",
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
	repoPath, ready, err := githubManagedSourceCheckoutState(repoSlug)
	if err != nil {
		return "", err
	}
	if ready {
		return repoPath, nil
	}
	return ensureGithubManagedCheckout(repoSlug, repoAccessLockOwner{
		Backend: "start-ui",
		RunID:   sanitizePathToken(repoSlug),
		Purpose: "investigation-source-setup",
		Label:   "start-ui-investigation-source",
	})
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
	return startUISpawnInvestigateQueryWithRunID(workspaceRoot, query, "")
}

func startUISpawnInvestigateQueryWithRunID(workspaceRoot string, query string, runID string) (startUIBackgroundLaunch, error) {
	trimmedRoot := strings.TrimSpace(workspaceRoot)
	if trimmedRoot == "" {
		return startUIBackgroundLaunch{}, fmt.Errorf("investigation workspace root is required")
	}
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return startUIBackgroundLaunch{}, fmt.Errorf("investigation query is required")
	}
	logPath := filepath.Join(trimmedRoot, ".nana", "logs", "investigate-launches", fmt.Sprintf("launch-%d.log", time.Now().UnixNano()))
	args := []string{"investigate"}
	if strings.TrimSpace(runID) != "" {
		args = append(args, "--run-id", strings.TrimSpace(runID))
	}
	args = append(args, trimmedQuery)
	if err := startUISpawnBackgroundNana(trimmedRoot, logPath, args); err != nil {
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
		WorkType:    issue.WorkType,
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
		fmt.Sprintf("Work type: %s", defaultString(strings.TrimSpace(issue.WorkType), "(missing work type)")),
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
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd, err := startManagedNanaCommand(args...)
	if err != nil {
		_ = logFile.Close()
		return err
	}
	cmd.Dir = workdir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := startManagedNanaStart(cmd); err != nil {
		_ = logFile.Close()
		return err
	}
	recordRuntimeArtifactWrite(logPath)
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
	sourcePath, sourceCheckoutReady, err := githubManagedSourceCheckoutState(repoSlug)
	if err != nil {
		return startUIRepoSummary{}, err
	}
	summary := startUIRepoSummary{
		RepoSlug:            repoSlug,
		SettingsPath:        githubRepoSettingsPath(repoSlug),
		StatePath:           startWorkStatePath(repoSlug),
		SourcePath:          sourcePath,
		SourceCheckoutReady: sourceCheckoutReady,
		ScoutCatalog:        startUIScoutCatalog(),
		ScoutsByRole:        startUIDefaultRepoScoutsByRole(strings.TrimSpace(sourcePath)),
		Scouts:              startUIDefaultRepoScouts(strings.TrimSpace(sourcePath)),
		IssueCounts:         map[string]int{},
		ServiceTaskCounts:   map[string]int{},
		ScoutJobCounts:      map[string]int{},
		PlannedItemCounts:   map[string]int{},
	}
	lockState, err := buildRepoAccessLockState(summary.SourcePath, repoAccessLockRead)
	if err != nil {
		return startUIRepoSummary{}, err
	}
	summary.LockState = lockState
	applyStartUIRepoSettings(&summary, settings)
	if err := applyStartUIRepoScouts(&summary); err != nil {
		return startUIRepoSummary{}, err
	}
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

func applyStartUIRepoScouts(summary *startUIRepoSummary) error {
	if summary == nil {
		return nil
	}
	repoPath := strings.TrimSpace(summary.SourcePath)
	summary.ScoutCatalog = startUIScoutCatalog()
	summary.ScoutsByRole = startUIDefaultRepoScoutsByRole(repoPath)
	if info, err := os.Stat(repoPath); err == nil && info.IsDir() {
		if startUIRepoSummarySourceReadBlocked(summary.LockState) {
			summary.Scouts = startUIRepoScoutsCompatibility(summary.ScoutsByRole)
			return nil
		}
		lockErr := withManagedSourceReadLock(summary.RepoSlug, repoAccessLockOwner{
			Backend: "start-ui",
			RunID:   sanitizePathToken(summary.RepoSlug),
			Purpose: "repo-summary-scout-config",
			Label:   "start-ui-repo-summary",
		}, func() error {
			for _, role := range supportedScoutRoleOrder {
				spec := scoutRoleSpecFor(role)
				summary.ScoutsByRole[spec.ConfigKey] = loadStartUIRepoScoutConfig(repoPath, role)
			}
			return nil
		})
		if lockErr != nil && !repoAccessLockBusy(lockErr) {
			return lockErr
		}
	} else {
		for _, role := range supportedScoutRoleOrder {
			spec := scoutRoleSpecFor(role)
			summary.ScoutsByRole[spec.ConfigKey] = loadStartUIRepoScoutConfig(repoPath, role)
		}
	}
	summary.Scouts = startUIRepoScoutsCompatibility(summary.ScoutsByRole)
	return nil
}

func startUIRepoSummarySourceReadBlocked(state *repoAccessLockStateSnapshot) bool {
	return state != nil && state.Writer != nil && !state.Writer.Stale
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

func (h *startUIAPI) loadStartUIWorkRunsPersistent(limit int) ([]startUIWorkRun, error) {
	sourcePathIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return nil, err
	}
	return startUIWithPersistentLocalWorkReadStore(h, func(store *localWorkDBStore) ([]startUIWorkRun, error) {
		return loadStartUIWorkRunsFromStore(store, limit, sourcePathIndex)
	})
}

func loadStartUIWorkRuns(limit int) ([]startUIWorkRun, error) {
	sourcePathIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return nil, err
	}
	return withLocalWorkReadStore(func(store *localWorkDBStore) ([]startUIWorkRun, error) {
		return loadStartUIWorkRunsFromStore(store, limit, sourcePathIndex)
	})
}

func loadStartUIWorkRunsFromStore(store *localWorkDBStore, limit int, sourcePathIndex map[string]string) ([]startUIWorkRun, error) {
	rows, err := store.db.Query(`SELECT run_id, backend, repo_key, repo_root, repo_name, repo_slug, manifest_path, updated_at, target_kind FROM work_run_index ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	entries := []workRunIndexEntry{}
	for rows.Next() {
		entry, err := scanWorkRunIndexEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	runs := []startUIWorkRun{}
	for _, entry := range entries {
		if entry.Backend == "local" {
			manifest, err := store.readManifest(entry.RunID)
			if err != nil {
				continue
			}
			skip, err := startUIShouldHideLocalWorkRun(store, manifest)
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			run, err := startUIWorkRunFromLocalManifest(entry, manifest, sourcePathIndex)
			if err != nil {
				continue
			}
			runs = append(runs, run)
			continue
		}
		run, err := startUIWorkRunFromIndex(entry, sourcePathIndex)
		if err != nil {
			continue
		}
		runs = append(runs, run)
	}
	return runs, nil
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
		PauseReason:    item.PauseReason,
		PauseUntil:     item.PauseUntil,
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
	case workItemStatusQueued, workItemStatusRunning, workItemStatusDraftReady, workItemStatusNeedsRouting, workItemStatusFailed, workItemStatusPaused:
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
	case workItemStatusPaused:
		return "blocked"
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
		return startUIWorkRunFromLocalManifest(entry, manifest, sourcePathIndex)
	case "github":
		manifest, err := readGithubWorkManifest(entry.ManifestPath)
		if err != nil {
			return startUIWorkRun{}, err
		}
		status := defaultString(manifest.ExecutionStatus, defaultString(manifest.PublicationState, "active"))
		if strings.EqualFold(strings.TrimSpace(manifest.MergeState), "merged") {
			status = "merged"
		}
		phase := defaultString(strings.TrimSpace(manifest.CurrentPhase), defaultString(manifest.NextAction, manifest.TargetKind))
		blocked := manifest.NeedsHuman || strings.Contains(strings.ToLower(phase), "blocked") || strings.EqualFold(strings.TrimSpace(manifest.ExecutionStatus), "paused")
		return startUIWorkRun{
			RunID:            entry.RunID,
			Backend:          entry.Backend,
			RepoKey:          manifest.RepoSlug,
			RepoName:         manifest.RepoName,
			RepoSlug:         manifest.RepoSlug,
			RepoLabel:        startUIWorkRunRepoLabel(manifest.RepoSlug, manifest.RepoName, manifest.ManagedRepoRoot),
			Status:           status,
			CurrentPhase:     phase,
			CurrentRound:     manifest.CurrentRound,
			UpdatedAt:        manifest.UpdatedAt,
			TargetKind:       manifest.TargetKind,
			TargetURL:        manifest.TargetURL,
			WorkType:         manifest.WorkType,
			ArtifactPath:     filepath.Dir(entry.ManifestPath),
			PublicationState: manifest.PublicationState,
			PauseReason:      manifest.PauseReason,
			PauseUntil:       manifest.PauseUntil,
			RerunAllowed:     false,
			Pending:          startUIWorkRunPending(status, blocked, phase),
			AttentionState:   startUIWorkRunAttentionState(status, defaultString(manifest.LastError, defaultString(manifest.PublicationError, manifest.NeedsHumanReason)), blocked),
		}, nil
	default:
		return startUIWorkRun{}, fmt.Errorf("unsupported backend %q", entry.Backend)
	}
}

func startUIWorkRunFromLocalManifest(entry workRunIndexEntry, manifest localWorkManifest, sourcePathIndex map[string]string) (startUIWorkRun, error) {
	if supersededBy, supersededReason, err := localWorkEffectiveSupersededInfo(manifest); err == nil && supersededBy != "" {
		manifest.SupersededByRunID = supersededBy
		manifest.SupersededReason = supersededReason
	}
	repoSlug := startUIResolvedLocalWorkRunRepoSlug(entry, manifest, sourcePathIndex)
	status := strings.TrimSpace(manifest.Status)
	blocked := strings.EqualFold(status, "paused")
	attention := startUIWorkRunAttentionState(status, manifest.LastError, blocked)
	return startUIWorkRun{
		RunID:            entry.RunID,
		Backend:          entry.Backend,
		RepoKey:          manifest.RepoID,
		RepoName:         manifest.RepoName,
		RepoSlug:         repoSlug,
		RepoLabel:        startUIWorkRunRepoLabel(repoSlug, manifest.RepoName, manifest.RepoRoot),
		Status:           status,
		CurrentPhase:     manifest.CurrentPhase,
		CurrentRound:     manifest.CurrentRound,
		CurrentIteration: manifest.CurrentIteration,
		UpdatedAt:        manifest.UpdatedAt,
		WorkType:         manifest.WorkType,
		ArtifactPath:     localWorkRunDirByID(manifest.RepoID, manifest.RunID),
		PauseReason:      manifest.PauseReason,
		PauseUntil:       manifest.PauseUntil,
		StopAllowed:      localWorkStopAllowed(manifest),
		RerunAllowed:     localWorkRerunAllowed(manifest),
		ResolveAllowed:   localWorkResolveAllowed(manifest),
		Pending:          startUIWorkRunPending(status, blocked, manifest.CurrentPhase),
		AttentionState:   attention,
	}, nil
}

func startUIShouldHideLocalWorkRun(store *localWorkDBStore, manifest localWorkManifest) (bool, error) {
	status := strings.ToLower(strings.TrimSpace(manifest.Status))
	switch status {
	case "failed":
		return true, nil
	case "blocked":
		if !localWorkResolveAllowed(manifest) {
			return true, nil
		}
		return startUILocalRunSuperseded(store, manifest)
	default:
		return false, nil
	}
}

func startUILocalRunSuperseded(store *localWorkDBStore, manifest localWorkManifest) (bool, error) {
	rows, err := store.db.Query(`SELECT manifest_json FROM runs WHERE repo_root = ? AND updated_at > ? ORDER BY updated_at DESC`, manifest.RepoRoot, manifest.UpdatedAt)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var candidate localWorkManifest
		if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
			return false, err
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
			return true, nil
		}
	}
	return false, rows.Err()
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
		if supersededBy, supersededReason, err := localWorkEffectiveSupersededInfo(manifest); err == nil && supersededBy != "" {
			manifest.SupersededByRunID = supersededBy
			manifest.SupersededReason = supersededReason
		}
		nextAction := defaultString(localWorkBlockedNextAction(manifest), defaultString(strings.TrimSpace(manifest.CurrentPhase), "inspect local work state"))
		if strings.EqualFold(strings.TrimSpace(manifest.Status), "stopped") {
			nextAction = fmt.Sprintf("Run nana work resume --run-id %s to continue this stopped run.", manifest.RunID)
		}
		totalTokens := 0
		sessionsAccounted := 0
		hasTokenUsage := false
		if manifest.TokenUsage != nil {
			totalTokens = manifest.TokenUsage.TotalTokens
			sessionsAccounted = manifest.TokenUsage.SessionsAccounted
			hasTokenUsage = manifest.TokenUsage.SessionsAccounted > 0 || manifest.TokenUsage.TotalTokens > 0
		}
		if !hasTokenUsage {
			runDir := localWorkRunDirByID(defaultString(strings.TrimSpace(manifest.RepoID), localWorkRepoID(manifest.RepoRoot)), manifest.RunID)
			if artifact, artifactErr := readLocalWorkThreadUsageArtifact(filepath.Join(runDir, "thread-usage.json")); artifactErr == nil && artifact != nil {
				totalTokens = artifact.Totals.TotalTokens
				sessionsAccounted = artifact.Totals.SessionsAccounted
				hasTokenUsage = artifact.Totals.SessionsAccounted > 0 || artifact.Totals.TotalTokens > 0
			}
		}
		return startUIWorkRunDetail{
			Summary:           summary,
			Backend:           entry.Backend,
			LocalManifest:     &manifest,
			StartedAt:         defaultString(strings.TrimSpace(manifest.CreatedAt), strings.TrimSpace(summary.UpdatedAt)),
			TotalTokens:       totalTokens,
			SessionsAccounted: sessionsAccounted,
			HasTokenUsage:     hasTokenUsage,
			NextAction:        nextAction,
			StopAllowed:       localWorkStopAllowed(manifest),
			RerunAllowed:      localWorkRerunAllowed(manifest),
			ResolveAllowed:    localWorkResolveAllowed(manifest),
			ExternalURL:       summary.TargetURL,
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
		totalTokens := 0
		sessionsAccounted := 0
		hasTokenUsage := false
		if artifact, artifactErr := readGithubThreadUsageArtifact(filepath.Join(filepath.Dir(entry.ManifestPath), "thread-usage.json")); artifactErr == nil && artifact != nil {
			totalTokens = artifact.TotalTokens
			sessionsAccounted = len(artifact.Rows)
			hasTokenUsage = artifact.TotalTokens > 0 || len(artifact.Rows) > 0
		}
		return startUIWorkRunDetail{
			Summary:           summary,
			Backend:           entry.Backend,
			GithubManifest:    &manifest,
			GithubStatus:      &status,
			StartedAt:         defaultString(strings.TrimSpace(manifest.CreatedAt), strings.TrimSpace(summary.UpdatedAt)),
			TotalTokens:       totalTokens,
			SessionsAccounted: sessionsAccounted,
			HasTokenUsage:     hasTokenUsage,
			NextAction:        defaultString(strings.TrimSpace(manifest.NextAction), "inspect GitHub feedback and publication state"),
			HumanGateReason:   defaultString(strings.TrimSpace(manifest.NeedsHumanReason), defaultString(strings.TrimSpace(manifest.PublicationError), strings.TrimSpace(manifest.PublicationDetail))),
			SyncAllowed:       true,
			RerunAllowed:      false,
			ExternalURL:       githubWorkRunExternalURL(manifest),
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

func createStartUIRepo(payload startUIRepoCreateRequest) (startUIRepoSummary, bool, error) {
	repoSlug, err := resolveGithubRepoSlugLocator(payload.RepoSlug)
	if err != nil {
		return startUIRepoSummary{}, false, fmt.Errorf("repo_slug must be owner/repo or a GitHub repo URL")
	}
	repoSlugs, err := listStartUIRepoSlugs()
	if err != nil {
		return startUIRepoSummary{}, false, err
	}
	if slices.Contains(repoSlugs, repoSlug) {
		summary, err := loadStartUIRepoSummary(repoSlug, true)
		if err != nil {
			return startUIRepoSummary{}, false, err
		}
		return summary, false, nil
	}
	summary, err := patchStartUIRepoSettings(repoSlug, payload.settingsPatch())
	if err != nil {
		return startUIRepoSummary{}, false, err
	}
	return summary, true, nil
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
	repoPath, ready, err := githubManagedSourceCheckoutState(repoSlug)
	if err != nil {
		return "", err
	}
	if repoPath == "" {
		return "", fmt.Errorf("repo %s does not have a managed source checkout", repoSlug)
	}
	if ready {
		return repoPath, nil
	}
	return ensureGithubManagedCheckout(repoSlug, repoAccessLockOwner{
		Backend: "start-ui",
		RunID:   sanitizePathToken(repoSlug),
		Purpose: "scout-policy-source-setup",
		Label:   "start-ui-scout-policy-source",
	})
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
	pickupState, _, err := readLocalScoutPickupStateWithReadLock(repoPath)
	if err != nil {
		return nil, err
	}
	if pickupState.Items == nil {
		pickupState.Items = map[string]localScoutPickupItem{}
	}
	items := []startUIScoutItem{}
	if err := withManagedSourceReadLock(repoSlug, repoAccessLockOwner{
		Backend: "start-ui",
		RunID:   sanitizePathToken(repoSlug),
		Purpose: "list-scout-items",
		Label:   "start-ui-scout-items",
	}, func() error {
		for _, role := range supportedScoutRoleOrder {
			matches, err := filepath.Glob(filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), "*", "proposals.json"))
			if err != nil {
				return err
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
					if record, ok := pickupState.Items[itemID]; ok {
						switch strings.TrimSpace(record.Status) {
						case "dismissed":
							scoutItem.Status = "dismissed"
						case "failed":
							scoutItem.Status = "failed"
							scoutItem.Error = strings.TrimSpace(record.Error)
						case "completed":
							scoutItem.Status = "completed"
						case "in_progress":
							scoutItem.Status = "planned"
							scoutItem.RunID = strings.TrimSpace(record.RunID)
							scoutItem.PlannedItemID = strings.TrimSpace(record.PlannedItemID)
						}
						if strings.TrimSpace(record.UpdatedAt) != "" {
							scoutItem.UpdatedAt = strings.TrimSpace(record.UpdatedAt)
						}
					}
					if workState != nil && (scoutItem.Destination == improvementDestinationLocal || scoutItem.Destination == improvementDestinationReview) {
						if job, ok := workState.ScoutJobs[itemID]; ok {
							scoutItem = startWorkScoutJobFromItem(job)
						}
					}
					scoutItem.AvailableActions = startUIScoutAvailableActions(scoutItem)
					items = append(items, scoutItem)
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
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

func promoteStartUIScoutItem(repoSlug string, item startUIScoutItem) (*startWorkState, startWorkScoutJob, error) {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()

	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkScoutJob{}, err
	}
	if state.ScoutJobs == nil {
		state.ScoutJobs = map[string]startWorkScoutJob{}
	}
	now := ISOTimeNow()
	existing := state.ScoutJobs[item.ID]
	job := startWorkScoutJob{
		ID:                item.ID,
		Role:              item.Role,
		Title:             item.Title,
		WorkType:          item.WorkType,
		Area:              item.Area,
		Summary:           item.Summary,
		Rationale:         item.Rationale,
		Evidence:          item.Evidence,
		Impact:            item.Impact,
		SuggestedNextStep: item.SuggestedNextStep,
		Confidence:        item.Confidence,
		Files:             append([]string{}, item.Files...),
		Labels:            append([]string{}, item.Labels...),
		Page:              item.Page,
		Route:             item.Route,
		Severity:          item.Severity,
		TargetKind:        item.TargetKind,
		Screenshots:       append([]string{}, item.Screenshots...),
		ArtifactPath:      item.ArtifactPath,
		ProposalPath:      item.ProposalPath,
		PolicyPath:        item.PolicyPath,
		PreflightPath:     item.PreflightPath,
		IssueDraftPath:    item.IssueDraftPath,
		RawOutputPath:     item.RawOutputPath,
		GeneratedAt:       item.GeneratedAt,
		AuditMode:         item.AuditMode,
		SurfaceKind:       item.SurfaceKind,
		SurfaceTarget:     item.SurfaceTarget,
		BrowserReady:      item.BrowserReady,
		PreflightReason:   item.PreflightReason,
		Destination:       improvementDestinationLocal,
		Status:            startScoutJobQueued,
		UpdatedAt:         now,
		CreatedAt:         defaultString(strings.TrimSpace(existing.CreatedAt), defaultString(strings.TrimSpace(item.GeneratedAt), now)),
	}
	if strings.TrimSpace(job.WorkType) == "" {
		job.WorkType = inferScoutWorkType(item.Role, scoutFinding{
			Title:    item.Title,
			Summary:  item.Summary,
			Labels:   append([]string{}, item.Labels...),
			Severity: item.Severity,
			Files:    append([]string{}, item.Files...),
		}).WorkType
	}
	state.ScoutJobs[item.ID] = job
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkScoutJob{}, err
	}
	return state, job, nil
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
	if selected.Destination == improvementDestinationReview && action == "promote" {
		if _, _, err := promoteStartUIScoutItem(repoSlug, *selected); err != nil {
			return startUIScoutItemsResponse{}, err
		}
		return loadStartUIScoutItems(repoSlug)
	}
	if _, _, readErr := readLocalScoutPickupStateWithReadLock(repoPath); readErr != nil {
		return startUIScoutItemsResponse{}, readErr
	}
	switch action {
	case "dismiss":
		if selected.Status == "running" {
			return startUIScoutItemsResponse{}, fmt.Errorf("running scout items cannot be dismissed")
		}
		if err := updateLocalScoutPickupState(repoPath, func(current *localScoutPickupState) error {
			current.Items[itemID] = localScoutPickupItem{
				Status:     "dismissed",
				Title:      selected.Title,
				Artifact:   selected.ArtifactPath,
				UpdatedAt:  ISOTimeNow(),
				ProposalID: selected.ID,
			}
			return nil
		}); err != nil {
			return startUIScoutItemsResponse{}, err
		}
	case "reset":
		if selected.Status == "pending" || selected.Status == "external" {
			return startUIScoutItemsResponse{}, fmt.Errorf("scout item %s is already pending", itemID)
		}
		if err := updateLocalScoutPickupState(repoPath, func(current *localScoutPickupState) error {
			delete(current.Items, itemID)
			return nil
		}); err != nil {
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
		if err := updateLocalScoutPickupState(repoPath, func(current *localScoutPickupState) error {
			_, plannedItem, err := createStartUIPlannedItem(repoSlug, startUIPlannedItemRequest{
				Title:       startUIScoutPlannedItemTitle(*selected),
				Description: startUIScoutPlannedItemDescription(*selected),
				WorkType:    selected.WorkType,
				Priority:    &priority,
				LaunchKind:  "local_work",
			})
			if err != nil {
				return err
			}
			current.Items[itemID] = localScoutPickupItem{
				Status:        "in_progress",
				Title:         selected.Title,
				Artifact:      selected.ArtifactPath,
				PlannedItemID: plannedItem.ID,
				UpdatedAt:     ISOTimeNow(),
				ProposalID:    selected.ID,
			}
			return nil
		}); err != nil {
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
	case "completed", "success", "succeeded", "merged", "closed", "done", "no-op", "stopped", "cancelled", "canceled":
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
	case blocked, strings.Contains(normalizedStatus, "block"), strings.Contains(normalizedStatus, "human"), strings.Contains(normalizedStatus, "pause"), strings.Contains(normalizedDetail, "block"):
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
			WorkType:      inferIssueWorkType(labels, item.Title, item.Body).WorkType,
			Priority:      priority,
			PriorityLabel: startWorkPriorityLabel(priority),
			UpdatedAt:     strings.TrimSpace(item.UpdatedAt),
		}
		if tracked, found := startUITrackedIssuePlannedItemForURL(state, result.TargetURL); found {
			result.Scheduled = true
			result.PlannedItemID = tracked.ID
			result.PlannedState = tracked.State
			result.ScheduleAt = tracked.ScheduleAt
			if strings.TrimSpace(tracked.WorkType) != "" {
				result.WorkType = tracked.WorkType
			}
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

func startUIPlannedItemForIdempotencyKey(state *startWorkState, idempotencyKey string, fingerprint string) (startWorkPlannedItem, bool, error) {
	trimmedKey := strings.TrimSpace(idempotencyKey)
	trimmedFingerprint := strings.TrimSpace(fingerprint)
	if state == nil || trimmedKey == "" {
		return startWorkPlannedItem{}, false, nil
	}
	var best startWorkPlannedItem
	found := false
	for _, item := range state.PlannedItems {
		if strings.TrimSpace(item.IdempotencyKey) != trimmedKey {
			continue
		}
		itemFingerprint := strings.TrimSpace(item.IdempotencyFingerprint)
		if trimmedFingerprint != "" && itemFingerprint != "" && itemFingerprint != trimmedFingerprint {
			return startWorkPlannedItem{}, false, startUIIdempotencyConflictError{Key: trimmedKey}
		}
		if !found || strings.TrimSpace(item.UpdatedAt) > strings.TrimSpace(best.UpdatedAt) {
			best = item
			found = true
		}
	}
	return best, found, nil
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
	workType, err := inferTrackedIssueWorkType(payload.WorkType, payload.Labels, title)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	plannedTitle := startUITrackedIssuePlannedItemTitle(issueNumber, title)
	description := startUITrackedIssuePlannedItemDescriptionFromSearch(repoSlug, targetURL, payload.Labels, priority, workType)

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
		item.WorkType = workType
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
			WorkType:    workType,
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

func startUITrackedIssuePlannedItemDescriptionFromSearch(repoSlug string, targetURL string, labels []string, priority int, workType string) string {
	lines := []string{
		fmt.Sprintf("Tracked issue: %s", strings.TrimSpace(targetURL)),
		fmt.Sprintf("Source repo: %s", repoSlug),
		fmt.Sprintf("Work type: %s", normalizeWorkType(workType)),
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
	idempotencyKey := strings.TrimSpace(payload.IdempotencyKey)
	idempotencyFingerprint := strings.TrimSpace(payload.IdempotencyFingerprint)
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
	if existing, found, err := startUIPlannedItemForIdempotencyKey(state, idempotencyKey, idempotencyFingerprint); err != nil {
		return nil, startWorkPlannedItem{}, err
	} else if found {
		return state, existing, nil
	}
	launchKind := strings.TrimSpace(payload.LaunchKind)
	if launchKind == "" {
		settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
		if resolvedGithubRepoMode(settings) == "local" {
			launchKind = "local_work"
		} else {
			launchKind = "github_issue"
		}
	}
	switch launchKind {
	case "local_work", "github_issue", "tracked_issue", "investigation", "manual_scout":
	default:
		return nil, startWorkPlannedItem{}, fmt.Errorf("unsupported launch_kind %q", payload.LaunchKind)
	}
	workType := ""
	if launchKind == "local_work" || launchKind == "github_issue" || launchKind == "tracked_issue" {
		workType, err = parseRequiredWorkType(payload.WorkType, "work_type")
		if err != nil {
			return nil, startWorkPlannedItem{}, err
		}
	}
	investigationQuery := ""
	scoutRole := ""
	scoutDestination := ""
	scoutSessionLimit := 0
	scoutFocus := []string{}
	findingsHandling := ""
	switch launchKind {
	case "investigation":
		investigationQuery = strings.TrimSpace(payload.InvestigationQuery)
		if investigationQuery == "" {
			return nil, startWorkPlannedItem{}, fmt.Errorf("investigation_query is required")
		}
		findingsHandling = normalizeFindingsHandling(payload.FindingsHandling, "", launchKind)
	case "manual_scout":
		scoutRole = strings.TrimSpace(payload.ScoutRole)
		if !scoutRoleListIncludes(supportedScoutRoleOrder, scoutRole) {
			return nil, startWorkPlannedItem{}, fmt.Errorf("unsupported scout_role %q", payload.ScoutRole)
		}
		scoutDestination = normalizeScoutDestination(defaultString(payload.ScoutDestination, improvementDestinationReview))
		if scoutDestination != improvementDestinationLocal && scoutDestination != improvementDestinationReview {
			return nil, startWorkPlannedItem{}, fmt.Errorf("unsupported scout_destination %q", payload.ScoutDestination)
		}
		if payload.ScoutSessionLimit > 0 {
			if !scoutRoleSupportsSessionLimit(scoutRole) {
				return nil, startWorkPlannedItem{}, fmt.Errorf("scout_role %s does not support session limits", scoutRole)
			}
			if payload.ScoutSessionLimit < 1 || payload.ScoutSessionLimit > maxScoutSessionLimit {
				return nil, startWorkPlannedItem{}, fmt.Errorf("scout_session_limit must be between 1 and %d", maxScoutSessionLimit)
			}
			scoutSessionLimit = payload.ScoutSessionLimit
		}
		scoutFocus = make([]string, 0, len(payload.ScoutFocus))
		for _, focus := range payload.ScoutFocus {
			trimmed := strings.TrimSpace(focus)
			if trimmed == "" {
				continue
			}
			scoutFocus = append(scoutFocus, trimmed)
		}
		scoutFocus = uniqueStrings(scoutFocus)
		findingsHandling = normalizeFindingsHandling(payload.FindingsHandling, scoutDestination, launchKind)
	case "local_work":
		findingsHandling = normalizeFindingsHandling(payload.FindingsHandling, "", launchKind)
	}
	itemID := fmt.Sprintf("planned-%d", time.Now().UnixNano())
	item := startWorkPlannedItem{
		ID:                     itemID,
		RepoSlug:               repoSlug,
		Title:                  title,
		Description:            strings.TrimSpace(payload.Description),
		WorkType:               workType,
		LaunchKind:             launchKind,
		FindingsHandling:       findingsHandling,
		TargetURL:              strings.TrimSpace(payload.TargetURL),
		InvestigationQuery:     investigationQuery,
		ScoutRole:              scoutRole,
		ScoutDestination:       scoutDestination,
		ScoutSessionLimit:      scoutSessionLimit,
		ScoutFocus:             append([]string{}, scoutFocus...),
		Priority:               priority,
		ScheduleAt:             scheduleAt,
		IdempotencyKey:         idempotencyKey,
		IdempotencyFingerprint: idempotencyFingerprint,
		State:                  startPlannedItemQueued,
		CreatedAt:              now,
		UpdatedAt:              now,
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
	if payload.WorkType != nil {
		workType, err := parseRequiredWorkType(*payload.WorkType, "work_type")
		if err != nil {
			return nil, startWorkPlannedItem{}, err
		}
		item.WorkType = workType
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
		TaskTemplates:  map[string]startWorkTaskTemplate{},
		ScoutJobs:      map[string]startWorkScoutJob{},
		Findings:       map[string]startWorkFinding{},
		ImportSessions: map[string]startWorkFindingImportSession{},
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
	case improvementDestinationReview:
		return "review"
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
	if item.Destination == improvementDestinationReview {
		switch status {
		case improvementDestinationReview, "pending":
			actions = append(actions, "promote", "dismiss")
		case "failed", "dismissed", "planned", "completed":
			actions = append(actions, "reset")
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
		fmt.Sprintf("Work type: %s", defaultString(strings.TrimSpace(item.WorkType), "(unknown)")),
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
	writeJSONResponseWithStatus(w, http.StatusOK, value)
}

func writeJSONResponseWithStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func writeStartUIError(w http.ResponseWriter, err error, defaultStatus int) {
	if _, ok := asLocalWorkDBSchemaError(err); ok {
		writeJSONResponseWithStatus(w, http.StatusServiceUnavailable, map[string]any{
			"code":           "work_db_repair_required",
			"message":        localWorkReadCommandError(err).Error(),
			"repair_command": "nana work db-repair",
		})
		return
	}
	http.Error(w, err.Error(), defaultStatus)
}

func hashJSON(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
