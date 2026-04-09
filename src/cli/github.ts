import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { chmod, lstat, mkdir, readFile, readlink, readdir, rm, symlink, unlink, writeFile } from 'node:fs/promises';
import { dirname, join, relative, resolve } from 'node:path';
import { homedir } from 'node:os';
import TOML from '@iarna/toml';
import { AGENT_DEFINITIONS } from '../agents/definitions.js';
import { stripFrontmatter } from '../agents/native-config.js';
import { isSessionStale, readSessionState } from '../hooks/session.js';
import { getPackageRoot } from '../utils/package.js';
import { defaultUserCodexHome } from '../utils/paths.js';
import { CODEX_BYPASS_FLAG } from './constants.js';
import { classifySpawnError, spawnPlatformCommandSync } from '../utils/platform-command.js';
import {
  resolveConcernMatchForFiles,
  resolveGithubConcernRegistry,
  resolveGithubConcernRegistryDetails,
  resolveLaneConcernDescriptor,
  type GithubConcernDescriptorSource,
  type GithubConcernRegistryDiagnostic,
  type GithubLaneConcernDescriptor,
  type GithubLaneConcernMatchReason,
  type GithubLaneConcernMatchResult,
} from './github-workon-concerns.js';

export const GITHUB_APPEND_ENV = 'NANA_GITHUB_APPEND_INSTRUCTIONS_FILE';
const DEFAULT_GITHUB_API_BASE_URL = 'https://api.github.com';
const GITHUB_ACCEPT_HEADER = 'application/vnd.github+json';
const SANDBOX_LOCK_TTL_MS = 45_000;
const SANDBOX_LOCK_HEARTBEAT_MS = 5_000;
const DEFAULT_GITHUB_CI_POLL_INTERVAL_MS = 5_000;
const DEFAULT_GITHUB_CI_TIMEOUT_MS = 20 * 60_000;
const DEFAULT_GITHUB_CI_RERUN_LIMIT = 2;

export const GITHUB_HELP = `nana work-on - GitHub-targeted issue/PR implementation helper

Usage:
  nana work-on start <github-issue-or-pr-url> [--considerations <list>] [--role-layout <split|reviewer+executor>] [--new-pr] [--create-pr | --local-only] [--reviewer <login|@me>] [codex-args...]
  nana work-on sync [--run-id <id> | --last] [--reviewer <login|@me>] [--resume-last] [codex-args...]
  nana work-on defaults set <owner/repo> [--considerations <list>] [--role-layout <split|reviewer+executor>] [--review-rules-mode <manual|automatic>]
  nana work-on defaults show <owner/repo>
  nana work-on stats <github-issue-or-pr-url>
  nana work-on retrospective [--run-id <id> | --last]
  nana work-on help

Examples:
  nana work-on start https://github.com/dkropachev/alternator-client-java/issues/1
  nana work-on start https://github.com/dkropachev/alternator-client-java/issues/1 --considerations arch,perf,api,style,qa
  nana work-on start https://github.com/dkropachev/alternator-client-java/issues/1 --considerations security,api --role-layout reviewer+executor
  nana work-on start https://github.com/dkropachev/alternator-client-java/issues/1 --new-pr --create-pr
  nana work-on start https://github.com/openai/codex/pull/456 --reviewer @me -- --model gpt-5.4
  nana work-on defaults set dkropachev/alternator-client-java --considerations arch,perf,api --role-layout split
  nana work-on stats https://github.com/dkropachev/alternator-client-java/issues/1
  nana work-on retrospective --last
  nana work-on sync --last --resume-last

Storage:
  - managed repo state: ~/.nana/repos/<owner>/<repo-name>
  - managed sandboxes: ~/.nana/repos/<owner>/<repo-name>/sandboxes/issue-<n> or pr-<n>
  - repo concern overrides: .nana/work-on-concerns.json or .github/nana-work-on-concerns.json
  - repo hot-path API overrides: .nana/work-on-hot-path-apis.json or .github/nana-work-on-hot-path-apis.json

Override shapes:
  - concerns: {"version":1,"lanes":{"security-reviewer":{"pathPrefixes":["policies/"]}}}
  - hot-path apis: {"version":1,"hot_path_api_files":["docs/openapi/search.yaml"],"api_identifier_tokens":["searchDocuments"]}

Auth:
  Uses GH_TOKEN / GITHUB_TOKEN when set, otherwise falls back to \`gh auth token\`.
`;

export const GITHUB_REVIEW_HELP = `nana review - Review an external GitHub PR with deterministic persistence

Usage:
  nana review <github-pr-url> [--mode automatic|manual] [--reviewer <login|@me>] [--per-item-context shared|isolated]
  nana review followup <github-pr-url> [--allow-open]
  nana review help

Behavior:
  - automatically onboards the repo into ~/.nana/repos/<owner>/<repo> when needed
  - automatically resumes an unfinished review run when the same PR already has one
  - persists accepted, user-dropped, not-real, and pre-existing findings separately
  - manual mode opens an editable markdown review file and loops until no argue items remain
  - followup prints findings that predated the reviewed PR and fails when the PR is still open unless --allow-open is passed
`;

type GithubTargetKind = 'issue' | 'pr';
type GithubConsideration = 'arch' | 'perf' | 'api' | 'security' | 'dependency' | 'style' | 'qa';
type GithubRoleLayout = 'split' | 'reviewer+executor';
type GithubCodexProfileName = 'leader' | 'executor' | 'reviewer' | 'publisher';

interface GithubActor {
  login?: string;
}

interface GithubRepositoryPayload {
  name: string;
  full_name: string;
  clone_url: string;
  default_branch: string;
  html_url: string;
}

interface GithubIssuePayload {
  number: number;
  title: string;
  body: string | null;
  html_url: string;
  state: string;
  updated_at: string;
  user?: GithubActor;
  pull_request?: Record<string, unknown>;
}

interface GithubPullRequestPayload {
  number: number;
  title: string;
  body: string | null;
  html_url: string;
  state: string;
  merged_at?: string | null;
  updated_at: string;
  user?: GithubActor;
  head: {
    ref: string;
    sha: string;
    repo?: { full_name?: string | null } | null;
  };
  base: {
    ref: string;
    sha: string;
    repo?: { full_name?: string | null } | null;
  };
}

interface GithubIssueCommentPayload {
  id: number;
  html_url: string;
  body: string | null;
  created_at: string;
  updated_at: string;
  user?: GithubActor;
}

interface GithubPullReviewPayload {
  id: number;
  html_url: string;
  body: string | null;
  submitted_at: string | null;
  state: string;
  user?: GithubActor;
  commit_id?: string;
}

interface GithubPullReviewCommentPayload {
  id: number;
  html_url: string;
  body: string | null;
  created_at: string;
  updated_at: string;
  path: string;
  line?: number | null;
  original_line?: number | null;
  diff_hunk?: string | null;
  user?: GithubActor;
  pull_request_review_id?: number | null;
}

interface GithubCheckRunPayload {
  id: number;
  name: string;
  status: string;
  conclusion: string | null;
  html_url?: string;
  details_url?: string;
}

interface GithubCheckRunsResponse {
  total_count: number;
  check_runs: GithubCheckRunPayload[];
}

interface GithubWorkflowRunPayload {
  id: number;
  name?: string | null;
  head_sha: string;
  status: string;
  conclusion: string | null;
  html_url: string;
  event?: string;
  run_started_at?: string | null;
  updated_at?: string | null;
}

interface GithubWorkflowRunsResponse {
  total_count: number;
  workflow_runs: GithubWorkflowRunPayload[];
}

interface GithubContentPayload {
  content?: string | null;
  encoding?: string | null;
}

interface GithubWorkflowJobPayload {
  id: number;
  run_id?: number;
  name: string;
  status: string;
  conclusion: string | null;
  html_url: string;
  started_at?: string | null;
  completed_at?: string | null;
}

interface GithubWorkflowJobsResponse {
  total_count: number;
  jobs: GithubWorkflowJobPayload[];
}

interface GithubFeedbackSnapshot {
  issueComments: GithubIssueCommentPayload[];
  reviews: GithubPullReviewPayload[];
  reviewComments: GithubPullReviewCommentPayload[];
}

interface GithubFeedbackCursor {
  issueCommentId: number;
  reviewId: number;
  reviewCommentId: number;
}

interface ParsedGithubTargetUrl {
  owner: string;
  repoName: string;
  repoSlug: string;
  targetKind: GithubTargetKind;
  targetNumber: number;
  canonicalUrl: string;
}

export interface ManagedRepoMetadata {
  version: 1;
  repo_name: string;
  repo_slug: string;
  repo_owner: string;
  clone_url: string;
  default_branch: string;
  html_url: string;
  repo_root: string;
  source_path: string;
  updated_at: string;
}

type GithubReviewRulesMode = 'manual' | 'automatic';

interface GithubReviewRulesReviewerPolicy {
  trusted_reviewers?: string[];
  blocked_reviewers?: string[];
  min_distinct_reviewers?: number;
}

interface ManagedRepoSettings {
  version: 1 | 2 | 3 | 4;
  default_considerations: GithubConsideration[];
  default_role_layout?: GithubRoleLayout;
  review_rules_mode?: GithubReviewRulesMode;
  review_rules_reviewer_policy?: GithubReviewRulesReviewerPolicy;
  hot_path_api_profile?: GithubRepoHotPathApiProfile;
  updated_at: string;
}

interface GithubReviewRulesGlobalConfig {
  version: 1;
  default_mode: GithubReviewRulesMode;
  reviewer_policy?: GithubReviewRulesReviewerPolicy;
  updated_at: string;
}

interface GithubConsiderationInference {
  considerations: GithubConsideration[];
  reasons: Partial<Record<GithubConsideration, string[]>>;
}

interface GithubRepoHotPathApiProfile {
  version: 1;
  analyzed_at: string;
  api_surface_files: string[];
  hot_path_api_files: string[];
  api_identifier_tokens: string[];
  evidence: string[];
}

interface GithubRepoHotPathApiProfileOverride {
  version?: number;
  replace?: boolean;
  api_surface_files?: string[];
  hot_path_api_files?: string[];
  api_identifier_tokens?: string[];
  evidence?: string[];
}

type GithubReviewRuleCategory = GithubConsideration | 'process';

interface GithubRepoReviewRuleEvidence {
  kind: 'review' | 'review_comment';
  pr_number: number;
  review_id?: number;
  review_comment_id?: number;
  reviewer?: string;
  state?: string;
  path?: string;
  line?: number;
  diff_hunk?: string;
  code_context_excerpt?: string;
  code_context_provenance?: 'pr_head_sha' | 'current_checkout' | 'unknown';
  code_context_ref?: string;
  html_url: string;
  excerpt: string;
}

interface GithubRepoReviewRule {
  id: string;
  title: string;
  rule: string;
  category: GithubReviewRuleCategory;
  confidence: number;
  reviewer_count: number;
  applicable_roles: string[];
  path_scopes: string[];
  extraction_origin: 'reviews' | 'review_comments' | 'mixed';
  extraction_reason: string;
  evidence: GithubRepoReviewRuleEvidence[];
  source: 'review-scan';
  created_at: string;
  updated_at: string;
}

interface GithubRepoReviewRulesDocument {
  version: 1;
  repo_slug: string;
  generated_at: string;
  updated_at: string;
  approved_rules: GithubRepoReviewRule[];
  pending_candidates: GithubRepoReviewRule[];
  disabled_rules?: GithubRepoReviewRule[];
  archived_rules?: GithubRepoReviewRule[];
  last_scan?: {
    at: string;
    source: 'repo' | 'issue' | 'pr';
    source_target: string;
    scanned_prs: number;
    scanned_reviews: number;
    scanned_review_comments: number;
  };
}

interface GithubReviewRuleScanSource {
  repoSlug: string;
  owner: string;
  repoName: string;
  sourceKind: 'repo' | 'issue' | 'pr';
  sourceTarget: string;
  issueNumber?: number;
  prNumbers?: number[];
}

interface GithubReviewRulesCommandDependencies {
  fetchImpl?: typeof fetch;
  writeLine?: (line: string) => void;
  now?: () => Date;
  env?: NodeJS.ProcessEnv;
  execFileSyncImpl?: typeof execFileSync;
  homeDir?: string;
}

export interface SandboxLease {
  version: 1;
  sandbox_id: string;
  owner_pid: number;
  owner_run_id: string;
  target_url: string;
  acquired_at: string;
  heartbeat_at: string;
  expires_at: string;
}

interface SandboxMetadata {
  version: 1;
  sandbox_id: string;
  repo_slug: string;
  repo_name: string;
  sandbox_path: string;
  repo_checkout_path: string;
  branch_name: string;
  base_ref: string;
  target_kind: GithubTargetKind;
  target_number: number;
  created_at: string;
  updated_at: string;
}

export interface GithubWorkonManifest {
  version: 3;
  run_id: string;
  created_at: string;
  updated_at: string;
  repo_slug: string;
  repo_owner: string;
  repo_name: string;
  managed_repo_root: string;
  source_path: string;
  sandbox_id: string;
  sandbox_path: string;
  sandbox_repo_path: string;
  verification_plan?: GithubVerificationPlan;
  verification_scripts_dir?: string;
  considerations_active: GithubConsideration[];
  role_layout: GithubRoleLayout;
  consideration_pipeline: GithubPipelineLane[];
  lane_prompt_artifacts: GithubLanePromptArtifact[];
  team_resolved_aliases: string[];
  team_resolved_roles: string[];
  create_pr_on_complete: boolean;
  issue_association_number?: number;
  published_pr_number?: number;
  published_pr_url?: string;
  published_pr_head_ref?: string;
  publication_state?: GithubPublicationState;
  publication_error?: string;
  publication_updated_at?: string;
  target_kind: GithubTargetKind;
  target_number: number;
  target_title: string;
  target_url: string;
  target_state: string;
  review_reviewer: string;
  api_base_url: string;
  default_branch: string;
  last_seen_issue_comment_id: number;
  last_seen_review_id: number;
  last_seen_review_comment_id: number;
  pr_head_ref?: string;
  pr_head_sha?: string;
  pr_head_repo?: string;
  pr_base_ref?: string;
  pr_base_sha?: string;
  pr_base_repo?: string;
}

type GithubPullReviewMode = 'automatic' | 'manual';
type GithubPullReviewPerItemContext = 'shared' | 'isolated';
type GithubPullReviewStatus = 'running' | 'awaiting-manual' | 'completed';
type GithubPullReviewFindingSeverity = 'critical' | 'high' | 'medium' | 'low';
type GithubPullReviewFindingBucket = 'accepted' | 'user_dropped' | 'not_real' | 'preexisting';

interface GithubPullReviewFinding {
  id: string;
  fingerprint: string;
  title: string;
  severity: GithubPullReviewFindingSeverity;
  path: string;
  line?: number;
  summary: string;
  detail: string;
  fix: string;
  rationale: string;
  changed_in_pr: boolean;
  changed_line_in_pr: boolean;
  main_permalink?: string;
  pr_permalink?: string;
  iteration: number;
  evidence?: string[];
  user_explanation?: string;
}

interface GithubPullReviewManifest {
  version: 1;
  run_id: string;
  created_at: string;
  updated_at: string;
  status: GithubPullReviewStatus;
  repo_slug: string;
  repo_owner: string;
  repo_name: string;
  managed_repo_root: string;
  source_path: string;
  review_root: string;
  review_file_path?: string;
  mode: GithubPullReviewMode;
  per_item_context: GithubPullReviewPerItemContext;
  reviewer_login: string;
  target_url: string;
  target_number: number;
  target_title: string;
  target_state: string;
  default_branch: string;
  default_branch_sha: string;
  pr_head_ref: string;
  pr_head_sha: string;
  pr_base_ref: string;
  pr_base_sha: string;
  posted_review_id?: number;
  posted_review_url?: string;
  posted_review_event?: 'APPROVE' | 'REQUEST_CHANGES';
  iteration: number;
}

interface GithubPullReviewActiveState {
  version: 1;
  run_id: string;
  status: GithubPullReviewStatus;
  updated_at: string;
}

interface GithubPullReviewAiCandidate {
  title: string;
  severity: GithubPullReviewFindingSeverity;
  path: string;
  line?: number;
  summary: string;
  detail: string;
  fix?: string;
  rationale?: string;
}

interface GithubPullReviewAiCandidateResponse {
  findings: GithubPullReviewAiCandidate[];
}

interface GithubPullReviewAiValidationItem {
  candidate_index: number;
  verdict: 'accepted' | 'not_real' | 'preexisting';
  summary?: string;
  detail?: string;
  fix?: string;
  rationale?: string;
  explanation?: string;
}

interface GithubPullReviewAiValidationResponse {
  items: GithubPullReviewAiValidationItem[];
}

interface GithubReviewCommand {
  subcommand: 'review';
  target: ParsedGithubTargetUrl;
  mode: GithubPullReviewMode;
  reviewer: string;
  perItemContext: GithubPullReviewPerItemContext;
}

interface GithubReviewFollowupCommand {
  subcommand: 'followup';
  target: ParsedGithubTargetUrl;
  allowOpen: boolean;
}

interface GithubReviewHelpCommand {
  subcommand: 'help';
}

type ParsedGithubReviewArgs =
  | GithubReviewCommand
  | GithubReviewFollowupCommand
  | GithubReviewHelpCommand;

interface GithubPullReviewCommandDependencies {
  fetchImpl?: typeof fetch;
  writeLine?: (line: string) => void;
  now?: () => Date;
  env?: NodeJS.ProcessEnv;
  execFileSyncImpl?: typeof execFileSync;
  homeDir?: string;
  codexExec?: (prompt: string, options: {
    cwd: string;
    codexArgs?: string[];
    env: NodeJS.ProcessEnv;
  }) => Promise<string>;
  openEditor?: (path: string, options?: { cwd?: string; editor?: string }) => Promise<void>;
}

interface GithubStartCommand {
  subcommand: 'start';
  target: ParsedGithubTargetUrl;
  reviewer: string;
  requestedConsiderations: GithubConsideration[];
  roleLayout?: GithubRoleLayout;
  newPr: boolean;
  createPr: boolean;
  codexArgs: string[];
}

interface GithubSyncCommand {
  subcommand: 'sync';
  runId?: string;
  useLastRun: boolean;
  reviewer?: string;
  resumeLast: boolean;
  feedbackTargetUrl?: string;
  codexArgs: string[];
}

interface GithubHelpCommand {
  subcommand: 'help';
}

interface GithubDefaultsSetCommand {
  subcommand: 'defaults-set';
  repoSlug: string;
  considerations: GithubConsideration[];
  roleLayout?: GithubRoleLayout;
  reviewRulesMode?: GithubReviewRulesMode;
  reviewRulesTrustedReviewers?: string[];
  reviewRulesBlockedReviewers?: string[];
  reviewRulesMinDistinctReviewers?: number;
}

interface GithubDefaultsShowCommand {
  subcommand: 'defaults-show';
  repoSlug: string;
}

interface GithubStatsCommand {
  subcommand: 'stats';
  target: ParsedGithubTargetUrl;
}

interface GithubVerifyRefreshCommand {
  subcommand: 'verify-refresh';
  runId?: string;
  useLastRun: boolean;
}

interface GithubRetrospectiveCommand {
  subcommand: 'retrospective';
  runId?: string;
  useLastRun: boolean;
}

interface GithubLaneExecCommand {
  subcommand: 'lane-exec';
  runId?: string;
  useLastRun: boolean;
  laneAlias: string;
  task?: string;
  codexArgs: string[];
}

export type ParsedGithubArgs =
  | GithubStartCommand
  | GithubSyncCommand
  | GithubHelpCommand
  | GithubDefaultsSetCommand
  | GithubDefaultsShowCommand
  | GithubStatsCommand
  | GithubVerifyRefreshCommand
  | GithubRetrospectiveCommand
  | GithubLaneExecCommand;

export interface GithubCommandDependencies {
  fetchImpl?: typeof fetch;
  launchWithHud?: (args: string[]) => Promise<void>;
  writeLine?: (line: string) => void;
  now?: () => Date;
  env?: NodeJS.ProcessEnv;
  execFileSyncImpl?: typeof execFileSync;
  homeDir?: string;
  startLeaseHeartbeat?: (input: {
    lockDir: string;
    sandboxId: string;
    ownerPid: number;
    ttlMs: number;
    heartbeatMs: number;
  }) => void;
  runLaneProcess?: typeof runGithubLaneProcess;
  startSchedulerDaemon?: (input: {
    runId: string;
    homeDir?: string;
  }) => void;
}

interface GithubApiContext {
  token: string;
  apiBaseUrl: string;
  fetchImpl: typeof fetch;
}

interface GithubTargetContext {
  repository: GithubRepositoryPayload;
  issue: GithubIssuePayload;
  pullRequest?: GithubPullRequestPayload;
}

interface ManagedRepoPaths {
  nanaHome: string;
  globalGithubRoot: string;
  repoRoot: string;
  sourcePath: string;
  repoMetaPath: string;
  repoSettingsPath: string;
  repoVerificationPlanPath: string;
  repoVerificationDriftLogPath: string;
  repoUnitTestHistoryPath: string;
  repoCiSuiteDurationsPath: string;
  runsDir: string;
  sandboxesDir: string;
  sandboxLocksDir: string;
  repoLatestRunPath: string;
  globalLatestRunPath: string;
}

interface GithubRunPaths {
  runDir: string;
  manifestPath: string;
  startInstructionsPath: string;
  feedbackInstructionsPath: string;
}

interface GithubPullReviewPaths {
  prRoot: string;
  activePath: string;
  runsDir: string;
}

interface GithubPullReviewRunPaths {
  runDir: string;
  manifestPath: string;
  reviewFilePath: string;
  manualPendingPath: string;
  candidatesPath: string;
  acceptedPath: string;
  droppedUserPath: string;
  droppedNotRealPath: string;
  droppedPreexistingPath: string;
}

interface GithubVerificationSourceFile {
  path: string;
  checksum: string;
  kind: 'workflow' | 'makefile' | 'script' | 'heuristic';
}

interface GithubVerificationPlan {
  source: 'workflow' | 'makefile' | 'heuristic';
  lint: string[];
  compile: string[];
  unit: string[];
  integration: string[];
  plan_fingerprint: string;
  source_files: GithubVerificationSourceFile[];
}

type GithubTestSuiteKind = 'unit' | 'integration';
type GithubTestExecutionMode = 'none' | 'unknown' | 'every-iteration' | 'final-only' | 'ci-only';

interface GithubUnitTestSample {
  recorded_at: string;
  sandbox_id: string;
  duration_ms: number;
  status: 'pass' | 'fail';
  plan_fingerprint: string;
}

interface GithubTestSuitePolicySummary {
  suite: GithubTestSuiteKind;
  mode: GithubTestExecutionMode;
  sample_count: number;
  passing_sample_count: number;
  failing_sample_count: number;
  average_duration_ms: number | null;
  source: 'none' | 'local' | 'ci' | 'local+ci';
  plan_fingerprint: string;
}

interface GithubCiSuiteDurations {
  version: 1;
  updated_at: string;
  plan_fingerprint: string;
  suites: Record<GithubTestSuiteKind, {
    average_duration_ms: number | null;
    sample_count: number;
  }>;
}

interface GithubVerificationDriftSourceChange {
  path: string;
  kind: GithubVerificationSourceFile['kind'];
  change: 'added' | 'removed' | 'modified';
  before_checksum?: string;
  after_checksum?: string;
}

interface GithubVerificationDriftEvent {
  recorded_at: string;
  run_id: string;
  sandbox_id: string;
  reason: 'initial-bootstrap' | 'scripts-missing' | 'plan-drift';
  before_fingerprint?: string;
  after_fingerprint: string;
  changed_sources: GithubVerificationDriftSourceChange[];
}

interface GithubTokenTotals {
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
}

interface GithubSandboxTokenRollup extends GithubTokenTotals {
  last_accounted_fingerprint?: string;
  last_accounted_at?: string;
  sessions_accounted: number;
}

interface GithubIssueTokenStats {
  version: 1;
  repo_slug: string;
  issue_number: number;
  updated_at: string;
  totals: GithubTokenTotals & { sessions_accounted: number };
  sandboxes: Record<string, GithubSandboxTokenRollup>;
}

interface GithubThreadRetrospectiveRow {
  nickname: string;
  role: string;
  tokens_used: number;
  started_at: number;
  updated_at: number;
}

interface GithubThreadUsageArtifact {
  version: 1;
  generated_at: string;
  sandbox_path: string;
  rows: GithubThreadRetrospectiveRow[];
  total_tokens: number;
}

interface GithubCodexProfile {
  name: GithubCodexProfileName;
  allowedMcpServers: string[];
  enableMultiAgent: boolean;
  enableChildAgentsMd: boolean;
}

type GithubLaneRuntimeStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';
type GithubLaneFailureCategory =
  | 'launch_failure'
  | 'sandbox_conflict'
  | 'tooling_failure'
  | 'transient_io'
  | 'deterministic_task_failure'
  | 'unknown';
type GithubSchedulerWakeReason = 'startup' | 'watch' | 'poll';

interface GithubLaneRuntimeState {
  version: 1;
  lane_id: string;
  alias: string;
  role: string;
  profile: GithubCodexProfileName;
  activation: GithubPipelineActivation | 'publication';
  phase: GithubPipelinePhase | 'publication';
  blocking: boolean;
  depends_on: string[];
  status: GithubLaneRuntimeStatus;
  pid?: number;
  retry_count?: number;
  retryable?: boolean;
  retry_policy?: string;
  retry_exhausted?: boolean;
  last_attempt_at?: string;
  last_error?: string;
  failure_category?: GithubLaneFailureCategory;
  completed_head_sha?: string;
  completed_changed_files?: string[];
  completed_file_hashes?: Record<string, string>;
  completed_concern_descriptor?: GithubLaneConcernDescriptor;
  completed_concern_match?: GithubLaneConcernMatchResult;
  invalidated_at?: string;
  invalidated_reason?: string;
  invalidation_concern_match?: GithubLaneConcernMatchResult;
  started_at?: string;
  updated_at: string;
  completed_at?: string;
  instructions_path: string;
  result_path: string;
  stdout_path: string;
  stderr_path: string;
}

interface GithubLeaderRuntimeStatus {
  run_id: string;
  session_active?: boolean;
  pid?: number;
  bootstrap_complete: boolean;
  implementation_started: boolean;
  implementation_complete: boolean;
  ready_for_publication: boolean;
  blocked: boolean;
  blocked_reason?: string;
  updated_at: string;
  last_resume_at?: string;
}

interface GithubPublisherRuntimeStatus {
  run_id: string;
  session_active?: boolean;
  pid?: number;
  started: boolean;
  pr_opened: boolean;
  ci_waiting: boolean;
  ci_green: boolean;
  blocked: boolean;
  blocked_reason?: string;
  blocked_reason_category?: string;
  blocked_retryable?: boolean;
  recovery_count?: number;
  retry_count?: number;
  retry_policy?: string;
  current_head_sha?: string;
  current_branch?: string;
  current_pr_number?: number;
  diagnostics_path?: string;
  last_milestone?: string;
  milestones?: Array<{
    milestone: string;
    at: string;
    detail?: string;
    head_sha?: string;
    pr_number?: number;
  }>;
  updated_at: string;
  last_resume_at?: string;
}

interface GithubSchedulerRuntimeState {
  version: 1;
  run_id: string;
  last_processed_event_id: number;
  pass_count: number;
  startup_pass_count: number;
  watch_pass_count: number;
  poll_pass_count: number;
  watch_mode: 'watch+poll' | 'poll-only';
  last_wake_reason?: GithubSchedulerWakeReason;
  last_pass_at?: string;
  last_completed_pass_id?: number;
  blocked_reason?: string;
  replay_count?: number;
  recovery_count?: number;
  publisher_recovery_count?: number;
}

interface GithubSchedulerPassArtifact {
  version: 1;
  run_id: string;
  pass_id: number;
  wake_reason: GithubSchedulerWakeReason;
  watch_mode: 'watch+poll' | 'poll-only';
  started_at: string;
  completed_at: string;
  last_processed_event_id_before: number;
  last_processed_event_id_after: number;
  replayed_event_count: number;
  launched_lanes: string[];
  invalidated_lanes: Array<{ alias: string; reason?: string }>;
  retried_lanes: Array<{ alias: string; retry_count?: number; failure_category?: string }>;
  recovery_events: Array<{ target: string; reason: string }>;
  concern_registry_diagnostics?: GithubConcernRegistryDiagnostic[];
  blocked_reason?: string;
}

interface GithubRuntimeConsistencyReport {
  ok: boolean;
  errors: string[];
  warnings: string[];
  stats: {
    latest_event_id: number;
    scheduler_last_processed_event_id: number;
    scheduler_pass_artifacts: number;
  };
}

type GithubPublicationState =
  | 'not_started'
  | 'implemented'
  | 'committed'
  | 'pushed'
  | 'pr_opened'
  | 'ci_waiting'
  | 'ci_green'
  | 'blocked';

interface SandboxAllocation {
  sandboxId: string;
  sandboxPath: string;
  repoCheckoutPath: string;
  gitDirPath: string;
  lockDir: string;
  lease: SandboxLease;
  branchName: string;
  baseRef: string;
}

const SANDBOX_CODEX_HOME_SEED_ENTRIES = [
  'auth.json',
  'config.toml',
  'AGENTS.md',
  'agents',
  'skills',
  'prompts',
  'rules',
  'version.json',
] as const;

type GithubPipelinePhase = 'pre-impl' | 'impl' | 'post-impl' | 'final-gate';
type GithubPipelineLaneMode = 'review' | 'execute' | 'review+execute';
type GithubPipelineLaneOwner = 'self' | 'coder';
type GithubPipelineActivation = 'bootstrap' | 'hardening';

interface GithubPipelineLane {
  alias: string;
  role: string;
  prompt_roles?: string[];
  activation: GithubPipelineActivation;
  phase: GithubPipelinePhase;
  mode: GithubPipelineLaneMode;
  owner: GithubPipelineLaneOwner;
  blocking: boolean;
  purpose: string;
}

interface GithubLanePromptArtifact {
  alias: string;
  role: string;
  prompt_path: string;
  prompt_roles: string[];
}

const SUPPORTED_CONSIDERATIONS = ['arch', 'perf', 'api', 'security', 'dependency', 'style', 'qa'] as const;
const SUPPORTED_ROLE_LAYOUTS = ['split', 'reviewer+executor'] as const;
const REVIEW_RULE_ROLE_MAP: Readonly<Record<GithubReviewRuleCategory, readonly string[]>> = {
  arch: ['executor', 'architect', 'architect+executor'],
  perf: ['executor', 'perf-reviewer', 'perf-reviewer+executor'],
  api: ['executor', 'architect', 'architect+executor', 'api-reviewer', 'api-reviewer+executor'],
  security: ['executor', 'security-reviewer', 'security-reviewer+executor'],
  dependency: ['executor', 'dependency-expert', 'dependency-expert+executor'],
  style: ['executor', 'style-reviewer', 'style-reviewer+executor'],
  qa: ['executor', 'test-engineer'],
  process: [
    'executor',
    'architect',
    'architect+executor',
    'api-reviewer',
    'api-reviewer+executor',
    'perf-reviewer',
    'perf-reviewer+executor',
    'security-reviewer',
    'security-reviewer+executor',
    'dependency-expert',
    'dependency-expert+executor',
    'style-reviewer',
    'style-reviewer+executor',
    'test-engineer',
    'publisher',
  ],
};
const REVIEW_RULE_LIBRARY: ReadonlyArray<{
  category: GithubReviewRuleCategory;
  title: string;
  rule: string;
  patterns: readonly RegExp[];
}> = [
  {
    category: 'qa',
    title: 'Require regression coverage for behavior changes',
    rule: 'Add or update targeted regression coverage for behavior changes and bug fixes before considering the work complete.',
    patterns: [/\bregression\b/i, /\btest(s|ing)?\b/i, /\bcoverage\b/i, /\bassert(ion|s)?\b/i, /\bunit test\b/i],
  },
  {
    category: 'api',
    title: 'Protect public API compatibility',
    rule: 'Treat public APIs, schemas, and documented contracts as compatibility surfaces; avoid silent breakage and call out migrations explicitly.',
    patterns: [/\bpublic api\b/i, /\bbackward/i, /\bcompatib/i, /\bcontract\b/i, /\bschema\b/i, /\bsignature\b/i, /\bbreaking\b/i],
  },
  {
    category: 'perf',
    title: 'Guard hot-path latency and allocations',
    rule: 'When touching hot paths, avoid unnecessary latency or allocation regressions and justify performance-sensitive changes with focused evidence.',
    patterns: [/\bperf(ormance)?\b/i, /\bhot path\b/i, /\blatency\b/i, /\bthroughput\b/i, /\ballocat/i, /\bcache\b/i, /\bp99\b/i],
  },
  {
    category: 'style',
    title: 'Keep naming and style consistent',
    rule: 'Keep naming, structure, and formatting aligned with repository conventions; prefer clarity and consistency over cleverness.',
    patterns: [/\bnaming?\b/i, /\breadab/i, /\bstyle\b/i, /\bconsistent\b/i, /\bformat\b/i, /\brename\b/i, /\bclarity\b/i],
  },
  {
    category: 'dependency',
    title: 'Avoid unnecessary dependency expansion',
    rule: 'Prefer existing repository utilities and avoid new dependencies or version churn unless the tradeoff is explicit and justified.',
    patterns: [/\bdependenc/i, /\bthird[- ]party\b/i, /\blibrary\b/i, /\bpackage\b/i, /\bversion\b/i, /\bvendor\b/i],
  },
  {
    category: 'security',
    title: 'Validate security-sensitive changes explicitly',
    rule: 'Validate authentication, authorization, input handling, and secret exposure paths explicitly when code touches security-sensitive surfaces.',
    patterns: [/\bsecurity\b/i, /\bauth(entication|orization)?\b/i, /\bsecret\b/i, /\bsanitiz/i, /\bvalidate\b/i, /\binjection\b/i, /\bpermission\b/i],
  },
  {
    category: 'arch',
    title: 'Preserve architectural boundaries',
    rule: 'Keep module boundaries, ownership, and architectural responsibilities explicit instead of leaking concerns across layers.',
    patterns: [/\barchitect/i, /\blayer\b/i, /\bmodule\b/i, /\babstraction\b/i, /\bboundar/i, /\bseparation\b/i, /\bownership\b/i],
  },
  {
    category: 'process',
    title: 'Respond to review with explicit resolution notes',
    rule: 'When review feedback changes behavior or design, make the resolution explicit in code, tests, or PR discussion instead of leaving the rationale implicit.',
    patterns: [/\brationale\b/i, /\bexplain\b/i, /\bclarify\b/i, /\bdocument\b/i, /\bwhy\b/i, /\bnote\b/i],
  },
];

const PIPELINE_PHASE_ORDER: readonly GithubPipelinePhase[] = [
  'pre-impl',
  'impl',
  'post-impl',
  'final-gate',
];

const GITHUB_CODEX_PROFILES: Record<GithubCodexProfileName, GithubCodexProfile> = {
  leader: {
    name: 'leader',
    allowedMcpServers: ['github', 'nana_state', 'nana_memory', 'nana_code_intel', 'nana_trace'],
    enableMultiAgent: false,
    enableChildAgentsMd: false,
  },
  executor: {
    name: 'executor',
    allowedMcpServers: ['nana_code_intel'],
    enableMultiAgent: false,
    enableChildAgentsMd: false,
  },
  reviewer: {
    name: 'reviewer',
    allowedMcpServers: ['nana_code_intel'],
    enableMultiAgent: false,
    enableChildAgentsMd: false,
  },
  publisher: {
    name: 'publisher',
    allowedMcpServers: ['github'],
    enableMultiAgent: false,
    enableChildAgentsMd: false,
  },
};

const SPLIT_CONSIDERATION_PIPELINE_SPECS: Readonly<Record<GithubConsideration | 'base', readonly GithubPipelineLane[]>> = {
  base: [
    {
      alias: 'coder',
      role: 'executor',
      prompt_roles: ['executor'],
      activation: 'bootstrap',
      phase: 'impl',
      mode: 'execute',
      owner: 'self',
      blocking: true,
      purpose: 'Primary implementation lane for feature and bug work.',
    },
  ],
  arch: [
    {
      alias: 'architect',
      role: 'architect',
      prompt_roles: ['architect'],
      activation: 'bootstrap',
      phase: 'pre-impl',
      mode: 'review',
      owner: 'coder',
      blocking: true,
      purpose: 'Review design boundaries, interfaces, and long-horizon tradeoffs before implementation hardens.',
    },
  ],
  perf: [
    {
      alias: 'perf-coder',
      role: 'executor',
      prompt_roles: ['executor'],
      activation: 'hardening',
      phase: 'impl',
      mode: 'execute',
      owner: 'self',
      blocking: false,
      purpose: 'Implement performance-oriented code changes where warranted.',
    },
    {
      alias: 'perf-reviewer',
      role: 'performance-reviewer',
      prompt_roles: ['performance-reviewer'],
      activation: 'hardening',
      phase: 'post-impl',
      mode: 'review',
      owner: 'coder',
      blocking: true,
      purpose: 'Review latency, complexity, allocation, and hotspot risk after implementation.',
    },
  ],
  api: [
    {
      alias: 'api-reviewer',
      role: 'api-reviewer',
      prompt_roles: ['api-reviewer'],
      activation: 'hardening',
      phase: 'post-impl',
      mode: 'review',
      owner: 'coder',
      blocking: true,
      purpose: 'Review public API regression risk, ergonomics, and compatibility.',
    },
  ],
  security: [
    {
      alias: 'security-reviewer',
      role: 'security-reviewer',
      prompt_roles: ['security-reviewer'],
      activation: 'hardening',
      phase: 'post-impl',
      mode: 'review',
      owner: 'coder',
      blocking: true,
      purpose: 'Review trust boundaries, authn/authz, and vulnerability exposure.',
    },
  ],
  dependency: [
    {
      alias: 'dependency-expert',
      role: 'dependency-expert',
      prompt_roles: ['dependency-expert'],
      activation: 'hardening',
      phase: 'pre-impl',
      mode: 'review',
      owner: 'coder',
      blocking: true,
      purpose: 'Review dependency choices, external SDK constraints, and package risk before implementation locks in.',
    },
  ],
  style: [
    {
      alias: 'style-reviewer',
      role: 'style-reviewer',
      prompt_roles: ['style-reviewer'],
      activation: 'hardening',
      phase: 'final-gate',
      mode: 'review',
      owner: 'coder',
      blocking: false,
      purpose: 'Review formatting, naming, and lint/style consistency before closeout.',
    },
  ],
  qa: [
    {
      alias: 'test-engineer',
      role: 'test-engineer',
      prompt_roles: ['test-engineer'],
      activation: 'hardening',
      phase: 'post-impl',
      mode: 'execute',
      owner: 'self',
      blocking: true,
      purpose: 'Design/write tests and strengthen regression coverage.',
    },
  ],
};

const MERGED_CONSIDERATION_PIPELINE_SPECS: Readonly<Record<GithubConsideration | 'base', readonly GithubPipelineLane[]>> = {
  base: SPLIT_CONSIDERATION_PIPELINE_SPECS.base,
  arch: [
    {
      alias: 'architect',
      role: 'architect+executor',
      prompt_roles: ['architect', 'executor'],
      activation: 'bootstrap',
      phase: 'pre-impl',
      mode: 'review+execute',
      owner: 'self',
      blocking: true,
      purpose: 'Review architecture and implement the architectural follow-ups for this lane inside one merged agent.',
    },
  ],
  perf: [
    {
      alias: 'perf-reviewer',
      role: 'performance-reviewer+executor',
      prompt_roles: ['performance-reviewer', 'executor'],
      activation: 'hardening',
      phase: 'post-impl',
      mode: 'review+execute',
      owner: 'self',
      blocking: true,
      purpose: 'Review and implement performance follow-ups after the main implementation lands.',
    },
  ],
  api: [
    {
      alias: 'api-reviewer',
      role: 'api-reviewer+executor',
      prompt_roles: ['api-reviewer', 'executor'],
      activation: 'hardening',
      phase: 'post-impl',
      mode: 'review+execute',
      owner: 'self',
      blocking: true,
      purpose: 'Review API ergonomics and compatibility, then implement the required API-safe fixes in the same lane.',
    },
  ],
  security: [
    {
      alias: 'security-reviewer',
      role: 'security-reviewer+executor',
      prompt_roles: ['security-reviewer', 'executor'],
      activation: 'hardening',
      phase: 'post-impl',
      mode: 'review+execute',
      owner: 'self',
      blocking: true,
      purpose: 'Review security risk and implement the required remediations in the same lane.',
    },
  ],
  dependency: [
    {
      alias: 'dependency-expert',
      role: 'dependency-expert+executor',
      prompt_roles: ['dependency-expert', 'executor'],
      activation: 'hardening',
      phase: 'pre-impl',
      mode: 'review+execute',
      owner: 'self',
      blocking: true,
      purpose: 'Review dependency constraints and implement the dependency-facing adjustments in the same lane.',
    },
  ],
  style: [
    {
      alias: 'style-reviewer',
      role: 'style-reviewer+executor',
      prompt_roles: ['style-reviewer', 'executor'],
      activation: 'hardening',
      phase: 'final-gate',
      mode: 'review+execute',
      owner: 'self',
      blocking: false,
      purpose: 'Review and apply style/lint consistency fixes inside a single merged lane.',
    },
  ],
  qa: SPLIT_CONSIDERATION_PIPELINE_SPECS.qa,
};

function isHelpToken(value: string | undefined): boolean {
  return value === '--help' || value === '-h' || value === 'help';
}

function parseNumberFlag(value: string | undefined, flag: string): number {
  if (!value) throw new Error(`Missing value after ${flag}.\n${GITHUB_HELP}`);
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`Invalid ${flag} value: ${value}. Expected a positive integer.\n${GITHUB_HELP}`);
  }
  return parsed;
}

function appendCodexArg(target: string[], value: string): void {
  if (!value) return;
  target.push(value);
}

function parseConsiderations(value: string | undefined, flag = '--considerations'): GithubConsideration[] {
  const raw = value?.trim();
  if (!raw) throw new Error(`Missing value after ${flag}.\n${GITHUB_HELP}`);
  const parts = raw.split(',').map((part) => part.trim().toLowerCase()).filter(Boolean);
  const invalid = parts.filter((part) => !SUPPORTED_CONSIDERATIONS.includes(part as GithubConsideration));
  if (invalid.length > 0) {
    throw new Error(`Invalid considerations: ${invalid.join(', ')}. Expected one or more of ${SUPPORTED_CONSIDERATIONS.join(', ')}.`);
  }
  return [...new Set(parts as GithubConsideration[])];
}

function parseRoleLayout(value: string | undefined, flag = '--role-layout'): GithubRoleLayout {
  const raw = value?.trim().toLowerCase();
  if (!raw) throw new Error(`Missing value after ${flag}.\n${GITHUB_HELP}`);
  if (raw === 'merged' || raw === 'reviewer-executor' || raw === 'reviewer_executor') {
    return 'reviewer+executor';
  }
  if ((SUPPORTED_ROLE_LAYOUTS as readonly string[]).includes(raw)) {
    return raw as GithubRoleLayout;
  }
  throw new Error(`Invalid ${flag} value: ${value}. Expected one of ${SUPPORTED_ROLE_LAYOUTS.join(', ')}.\n${GITHUB_HELP}`);
}

function parseReviewRulesMode(value: string | undefined, flag = '--review-rules-mode'): GithubReviewRulesMode {
  const raw = value?.trim().toLowerCase();
  if (!raw) throw new Error(`Missing value after ${flag}.\n${GITHUB_HELP}`);
  if (raw === 'manual' || raw === 'automatic') return raw;
  throw new Error(`Invalid ${flag} value: ${value}. Expected one of manual, automatic.\n${GITHUB_HELP}`);
}

function parsePullReviewMode(value: string | undefined, flag = '--mode'): GithubPullReviewMode {
  const raw = value?.trim().toLowerCase();
  if (!raw) throw new Error(`Missing value after ${flag}.\n${GITHUB_REVIEW_HELP}`);
  if (raw === 'manual' || raw === 'automatic') return raw;
  throw new Error(`Invalid ${flag} value: ${value}. Expected one of manual, automatic.\n${GITHUB_REVIEW_HELP}`);
}

function parsePullReviewPerItemContext(
  value: string | undefined,
  flag = '--per-item-context',
): GithubPullReviewPerItemContext {
  const raw = value?.trim().toLowerCase();
  if (!raw) throw new Error(`Missing value after ${flag}.\n${GITHUB_REVIEW_HELP}`);
  if (raw === 'shared' || raw === 'isolated') return raw;
  throw new Error(`Invalid ${flag} value: ${value}. Expected one of shared, isolated.\n${GITHUB_REVIEW_HELP}`);
}

function parseLoginList(value: string | undefined, flag: string): string[] {
  const raw = value?.trim();
  if (!raw) throw new Error(`Missing value after ${flag}.\n${GITHUB_HELP}`);
  if (raw.toLowerCase() === 'none' || raw.toLowerCase() === '(none)') return [];
  return [...new Set(raw.split(',').map((part) => normalizeLogin(part)).filter((part): part is string => Boolean(part)))];
}

function parsePositiveIntFlag(value: string | undefined, flag: string): number {
  const raw = value?.trim();
  if (!raw) throw new Error(`Missing value after ${flag}.\n${GITHUB_HELP}`);
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed < 0) throw new Error(`Invalid ${flag} value: ${value}. Expected a non-negative integer.\n${GITHUB_HELP}`);
  return parsed;
}

function normalizeReviewerPolicy(
  policy: GithubReviewRulesReviewerPolicy | undefined,
): GithubReviewRulesReviewerPolicy | undefined {
  if (!policy) return undefined;
  const trusted = [...new Set((policy.trusted_reviewers ?? []).map((value) => normalizeLogin(value)).filter((value): value is string => Boolean(value)))];
  const blocked = [...new Set((policy.blocked_reviewers ?? []).map((value) => normalizeLogin(value)).filter((value): value is string => Boolean(value)))];
  const minReviewers = typeof policy.min_distinct_reviewers === 'number' && Number.isFinite(policy.min_distinct_reviewers) && policy.min_distinct_reviewers > 0
    ? Math.trunc(policy.min_distinct_reviewers)
    : undefined;
  if (trusted.length === 0 && blocked.length === 0 && !minReviewers) return undefined;
  return {
    ...(trusted.length > 0 ? { trusted_reviewers: trusted } : {}),
    ...(blocked.length > 0 ? { blocked_reviewers: blocked } : {}),
    ...(minReviewers ? { min_distinct_reviewers: minReviewers } : {}),
  };
}

export function parseGithubTargetUrl(raw: string): ParsedGithubTargetUrl {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new Error(`Invalid GitHub URL: ${raw}.\n${GITHUB_HELP}`);
  }

  if (!['github.com', 'www.github.com'].includes(url.hostname)) {
    throw new Error(`Unsupported GitHub host: ${url.hostname}. Expected github.com.\n${GITHUB_HELP}`);
  }

  const parts = url.pathname.replace(/^\/+|\/+$/g, '').split('/');
  if (parts.length < 4) {
    throw new Error(`Unsupported GitHub URL shape: ${raw}. Expected an issue or pull request URL.\n${GITHUB_HELP}`);
  }

  const [owner, repoName, kindSegment, numberSegment] = parts;
  const normalizedKind = kindSegment.toLowerCase();
  const targetKind = normalizedKind === 'issues'
    ? 'issue'
    : normalizedKind === 'pull' || normalizedKind === 'pulls'
      ? 'pr'
      : null;
  if (!owner || !repoName || !targetKind) {
    throw new Error(`Unsupported GitHub URL shape: ${raw}. Expected an issue or pull request URL.\n${GITHUB_HELP}`);
  }

  const targetNumber = Number.parseInt(numberSegment || '', 10);
  if (!Number.isFinite(targetNumber) || targetNumber <= 0) {
    throw new Error(`Invalid GitHub target number in URL: ${raw}.\n${GITHUB_HELP}`);
  }

  return {
    owner,
    repoName,
    repoSlug: `${owner}/${repoName}`,
    targetKind,
    targetNumber,
    canonicalUrl: `https://github.com/${owner}/${repoName}/${targetKind === 'issue' ? 'issues' : 'pull'}/${targetNumber}`,
  };
}

function parseGithubRepoSlug(raw: string): { owner: string; repoName: string; repoSlug: string } {
  const trimmed = raw.trim();
  const match = /^([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+)$/.exec(trimmed);
  if (!match) throw new Error(`Invalid GitHub repo slug: ${raw}`);
  const owner = match[1]!;
  const repoName = match[2]!;
  return { owner, repoName, repoSlug: `${owner}/${repoName}` };
}

export function parseGithubArgs(args: readonly string[]): ParsedGithubArgs {
  const values = [...args];
  const first = values[0];
  if (!first || isHelpToken(first)) return { subcommand: 'help' };

  if (first === 'defaults') {
    const action = values[1];
    if (action === 'set') {
      const repoSlug = values[2]?.trim();
      if (!repoSlug || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repoSlug)) {
        throw new Error(`Usage: nana work-on defaults set <owner/repo> [--considerations <list>] [--role-layout <split|reviewer+executor>] [--review-rules-mode <manual|automatic>] [--review-rules-trusted-reviewers <a,b>] [--review-rules-blocked-reviewers <a,b>] [--review-rules-min-distinct-reviewers <n>]\n\n${GITHUB_HELP}`);
      }
      let considerations: GithubConsideration[] = [];
      let roleLayout: GithubRoleLayout | undefined;
      let reviewRulesMode: GithubReviewRulesMode | undefined;
      let reviewRulesTrustedReviewers: string[] | undefined;
      let reviewRulesBlockedReviewers: string[] | undefined;
      let reviewRulesMinDistinctReviewers: number | undefined;
      for (let index = 3; index < values.length; index += 1) {
        const token = values[index];
        if (token === '--considerations') {
          considerations = parseConsiderations(values[index + 1], '--considerations');
          index += 1;
          continue;
        }
        if (token.startsWith('--considerations=')) {
          considerations = parseConsiderations(token.slice('--considerations='.length), '--considerations');
          continue;
        }
        if (token === '--role-layout') {
          roleLayout = parseRoleLayout(values[index + 1], '--role-layout');
          index += 1;
          continue;
        }
        if (token.startsWith('--role-layout=')) {
          roleLayout = parseRoleLayout(token.slice('--role-layout='.length), '--role-layout');
          continue;
        }
        if (token === '--review-rules-mode') {
          reviewRulesMode = parseReviewRulesMode(values[index + 1], '--review-rules-mode');
          index += 1;
          continue;
        }
        if (token.startsWith('--review-rules-mode=')) {
          reviewRulesMode = parseReviewRulesMode(token.slice('--review-rules-mode='.length), '--review-rules-mode');
          continue;
        }
        if (token === '--review-rules-trusted-reviewers') {
          reviewRulesTrustedReviewers = parseLoginList(values[index + 1], '--review-rules-trusted-reviewers');
          index += 1;
          continue;
        }
        if (token.startsWith('--review-rules-trusted-reviewers=')) {
          reviewRulesTrustedReviewers = parseLoginList(token.slice('--review-rules-trusted-reviewers='.length), '--review-rules-trusted-reviewers');
          continue;
        }
        if (token === '--review-rules-blocked-reviewers') {
          reviewRulesBlockedReviewers = parseLoginList(values[index + 1], '--review-rules-blocked-reviewers');
          index += 1;
          continue;
        }
        if (token.startsWith('--review-rules-blocked-reviewers=')) {
          reviewRulesBlockedReviewers = parseLoginList(token.slice('--review-rules-blocked-reviewers='.length), '--review-rules-blocked-reviewers');
          continue;
        }
        if (token === '--review-rules-min-distinct-reviewers') {
          reviewRulesMinDistinctReviewers = parsePositiveIntFlag(values[index + 1], '--review-rules-min-distinct-reviewers');
          index += 1;
          continue;
        }
        if (token.startsWith('--review-rules-min-distinct-reviewers=')) {
          reviewRulesMinDistinctReviewers = parsePositiveIntFlag(token.slice('--review-rules-min-distinct-reviewers='.length), '--review-rules-min-distinct-reviewers');
          continue;
        }
      }
      return {
        subcommand: 'defaults-set',
        repoSlug,
        considerations,
        roleLayout,
        reviewRulesMode,
        reviewRulesTrustedReviewers,
        reviewRulesBlockedReviewers,
        reviewRulesMinDistinctReviewers,
      };
    }

    if (action === 'show') {
      const repoSlug = values[2]?.trim();
      if (!repoSlug || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repoSlug)) {
        throw new Error(`Usage: nana work-on defaults show <owner/repo>\n\n${GITHUB_HELP}`);
      }
      return { subcommand: 'defaults-show', repoSlug };
    }

    return { subcommand: 'help' };
  }

  if (first === 'stats') {
    const rawTarget = values[1];
    if (!rawTarget || rawTarget.startsWith('-')) {
      throw new Error(`Usage: nana work-on stats <github-issue-or-pr-url>\n\n${GITHUB_HELP}`);
    }
    return {
      subcommand: 'stats',
      target: parseGithubTargetUrl(rawTarget),
    };
  }

  if (first === 'retrospective') {
    let runId: string | undefined;
    let useLastRun = true;
    for (let index = 1; index < values.length; index += 1) {
      const token = values[index];
      if (isHelpToken(token)) return { subcommand: 'help' };
      if (token === '--run-id') {
        runId = values[index + 1]?.trim();
        if (!runId) throw new Error(`Missing value after --run-id.\n${GITHUB_HELP}`);
        useLastRun = false;
        index += 1;
        continue;
      }
      if (token.startsWith('--run-id=')) {
        runId = token.slice('--run-id='.length).trim();
        if (!runId) throw new Error(`Missing value after --run-id.\n${GITHUB_HELP}`);
        useLastRun = false;
        continue;
      }
      if (token === '--last') {
        useLastRun = true;
        continue;
      }
    }
    return { subcommand: 'retrospective', runId, useLastRun };
  }

  if (first === 'verify-refresh') {
    let runId: string | undefined;
    let useLastRun = true;
    for (let index = 1; index < values.length; index += 1) {
      const token = values[index];
      if (isHelpToken(token)) return { subcommand: 'help' };
      if (token === '--run-id') {
        runId = values[index + 1]?.trim();
        if (!runId) throw new Error(`Missing value after --run-id.\n${GITHUB_HELP}`);
        useLastRun = false;
        index += 1;
        continue;
      }
      if (token.startsWith('--run-id=')) {
        runId = token.slice('--run-id='.length).trim();
        if (!runId) throw new Error(`Missing value after --run-id.\n${GITHUB_HELP}`);
        useLastRun = false;
        continue;
      }
      if (token === '--last') {
        useLastRun = true;
        continue;
      }
    }
    return { subcommand: 'verify-refresh', runId, useLastRun };
  }

  if (first === 'lane-exec') {
    let runId: string | undefined;
    let useLastRun = true;
    let laneAlias = '';
    let task: string | undefined;
    const codexArgs: string[] = [];
    for (let index = 1; index < values.length; index += 1) {
      const token = values[index];
      if (token === '--') {
        for (let i = index + 1; i < values.length; i += 1) appendCodexArg(codexArgs, values[i] ?? '');
        break;
      }
      if (isHelpToken(token)) return { subcommand: 'help' };
      if (token === '--run-id') {
        runId = values[index + 1]?.trim();
        if (!runId) throw new Error(`Missing value after --run-id.\n${GITHUB_HELP}`);
        useLastRun = false;
        index += 1;
        continue;
      }
      if (token.startsWith('--run-id=')) {
        runId = token.slice('--run-id='.length).trim();
        if (!runId) throw new Error(`Missing value after --run-id.\n${GITHUB_HELP}`);
        useLastRun = false;
        continue;
      }
      if (token === '--last') {
        useLastRun = true;
        continue;
      }
      if (token === '--lane') {
        laneAlias = values[index + 1]?.trim() || '';
        if (!laneAlias) throw new Error(`Missing value after --lane.\n${GITHUB_HELP}`);
        index += 1;
        continue;
      }
      if (token.startsWith('--lane=')) {
        laneAlias = token.slice('--lane='.length).trim();
        if (!laneAlias) throw new Error(`Missing value after --lane.\n${GITHUB_HELP}`);
        continue;
      }
      if (token === '--task') {
        task = values[index + 1]?.trim() || '';
        if (!task) throw new Error(`Missing value after --task.\n${GITHUB_HELP}`);
        index += 1;
        continue;
      }
      if (token.startsWith('--task=')) {
        task = token.slice('--task='.length).trim();
        if (!task) throw new Error(`Missing value after --task.\n${GITHUB_HELP}`);
        continue;
      }
    }
    if (!laneAlias) {
      throw new Error(`Usage: nana work-on lane-exec --run-id <id>|--last --lane <alias> [--task <text>] [-- codex-args...]\n\n${GITHUB_HELP}`);
    }
    return { subcommand: 'lane-exec', runId, useLastRun, laneAlias, task, codexArgs };
  }

  if (first === 'start') {
    let reviewer = '@me';
    let requestedConsiderations: GithubConsideration[] = [];
    let roleLayout: GithubRoleLayout | undefined;
    let newPr = false;
    let createPr = false;
    const codexArgs: string[] = [];
    let target: ParsedGithubTargetUrl | null = null;

    if (values[1] && !values[1].startsWith('-')) {
      target = parseGithubTargetUrl(values[1]);
    }

    let repoSlug: string | undefined;
    let issueNumber: number | undefined;
    let prNumber: number | undefined;

    for (let index = target ? 2 : 1; index < values.length; index += 1) {
      const token = values[index];
      if (token === '--') {
        for (let i = index + 1; i < values.length; i += 1) appendCodexArg(codexArgs, values[i] ?? '');
        break;
      }
      if (isHelpToken(token)) return { subcommand: 'help' };
      if (token === '--reviewer') {
        reviewer = values[index + 1]?.trim() || '';
        if (!reviewer) throw new Error(`Missing value after --reviewer.\n${GITHUB_HELP}`);
        index += 1;
        continue;
      }
      if (token.startsWith('--reviewer=')) {
        reviewer = token.slice('--reviewer='.length).trim();
        if (!reviewer) throw new Error(`Missing value after --reviewer.\n${GITHUB_HELP}`);
        continue;
      }
      if (token === '--mode' || token.startsWith('--mode=')) {
        throw new Error('`--mode` has been removed. Use `--considerations` only.');
      }
      if (token === '--considerations') {
        requestedConsiderations = parseConsiderations(values[index + 1], '--considerations');
        index += 1;
        continue;
      }
      if (token.startsWith('--considerations=')) {
        requestedConsiderations = parseConsiderations(token.slice('--considerations='.length), '--considerations');
        continue;
      }
      if (token === '--role-layout') {
        roleLayout = parseRoleLayout(values[index + 1], '--role-layout');
        index += 1;
        continue;
      }
      if (token.startsWith('--role-layout=')) {
        roleLayout = parseRoleLayout(token.slice('--role-layout='.length), '--role-layout');
        continue;
      }
      if (token === '--new-pr') {
        newPr = true;
        continue;
      }
      if (token === '--create-pr') {
        createPr = true;
        continue;
      }
      if (token === '--local-only') {
        createPr = false;
        continue;
      }

      if (target) {
        appendCodexArg(codexArgs, token);
        continue;
      }

      if (token === '--repo') {
        repoSlug = values[index + 1]?.trim();
        if (!repoSlug) throw new Error(`Missing value after --repo.\n${GITHUB_HELP}`);
        index += 1;
        continue;
      }
      if (token.startsWith('--repo=')) {
        repoSlug = token.slice('--repo='.length).trim();
        if (!repoSlug) throw new Error(`Missing value after --repo.\n${GITHUB_HELP}`);
        continue;
      }
      if (token === '--issue') {
        issueNumber = parseNumberFlag(values[index + 1], '--issue');
        index += 1;
        continue;
      }
      if (token.startsWith('--issue=')) {
        issueNumber = parseNumberFlag(token.slice('--issue='.length), '--issue');
        continue;
      }
      if (token === '--pr') {
        prNumber = parseNumberFlag(values[index + 1], '--pr');
        index += 1;
        continue;
      }
      if (token.startsWith('--pr=')) {
        prNumber = parseNumberFlag(token.slice('--pr='.length), '--pr');
        continue;
      }
      appendCodexArg(codexArgs, token);
    }

    if (!target) {
      if (!repoSlug || ((issueNumber === undefined) === (prNumber === undefined))) {
        throw new Error(`Usage: nana work-on start <github-issue-or-pr-url>\n\n${GITHUB_HELP}`);
      }
      target = parseGithubTargetUrl(
        `https://github.com/${repoSlug}/${issueNumber !== undefined ? `issues/${issueNumber}` : `pull/${prNumber}`}`,
      );
    }

    return {
      subcommand: 'start',
      target,
      reviewer,
      requestedConsiderations,
      roleLayout,
      newPr,
      createPr,
      codexArgs,
    };
  }

  if (first === 'sync') {
    let runId: string | undefined;
    let useLastRun = false;
    let reviewer: string | undefined;
    let resumeLast = false;
    let feedbackTargetUrl: string | undefined;
    const codexArgs: string[] = [];

    for (let index = 1; index < values.length; index += 1) {
      const token = values[index];
      if (token === '--') {
        for (let i = index + 1; i < values.length; i += 1) appendCodexArg(codexArgs, values[i] ?? '');
        break;
      }
      if (isHelpToken(token)) return { subcommand: 'help' };
      if (token === '--run-id') {
        runId = values[index + 1]?.trim();
        if (!runId) throw new Error(`Missing value after --run-id.\n${GITHUB_HELP}`);
        index += 1;
        continue;
      }
      if (token.startsWith('--run-id=')) {
        runId = token.slice('--run-id='.length).trim();
        if (!runId) throw new Error(`Missing value after --run-id.\n${GITHUB_HELP}`);
        continue;
      }
      if (token === '--last') {
        useLastRun = true;
        continue;
      }
      if (/^https:\/\/github\.com\/[^/]+\/[^/]+\/(issues|pull)\/\d+/i.test(token)) {
        feedbackTargetUrl = parseGithubTargetUrl(token).canonicalUrl;
        continue;
      }
      if (token === '--reviewer') {
        reviewer = values[index + 1]?.trim();
        if (!reviewer) throw new Error(`Missing value after --reviewer.\n${GITHUB_HELP}`);
        index += 1;
        continue;
      }
      if (token.startsWith('--reviewer=')) {
        reviewer = token.slice('--reviewer='.length).trim();
        if (!reviewer) throw new Error(`Missing value after --reviewer.\n${GITHUB_HELP}`);
        continue;
      }
      if (token === '--resume-last') {
        resumeLast = true;
        continue;
      }
      appendCodexArg(codexArgs, token);
    }

    if (runId && useLastRun) {
      throw new Error(`Use either --run-id <id> or --last, not both.\n${GITHUB_HELP}`);
    }

    return {
      subcommand: 'sync',
      runId,
      useLastRun: useLastRun || !runId,
      reviewer,
      resumeLast,
      feedbackTargetUrl,
      codexArgs,
    };
  }

  throw new Error(`Unknown work-on subcommand: ${first}.\n${GITHUB_HELP}`);
}

export function parseGithubReviewArgs(args: readonly string[]): ParsedGithubReviewArgs {
  const values = [...args];
  const first = values[0];
  if (!first || isHelpToken(first)) return { subcommand: 'help' };

  if (first === 'followup') {
    const rawTarget = values[1];
    if (!rawTarget || rawTarget.startsWith('-')) {
      throw new Error(`Usage: nana review followup <github-pr-url> [--allow-open]\n\n${GITHUB_REVIEW_HELP}`);
    }
    const target = parseGithubTargetUrl(rawTarget);
    if (target.targetKind !== 'pr') {
      throw new Error(`nana review followup expects a pull request URL.\n${GITHUB_REVIEW_HELP}`);
    }
    let allowOpen = false;
    for (let index = 2; index < values.length; index += 1) {
      const token = values[index];
      if (isHelpToken(token)) return { subcommand: 'help' };
      if (token === '--allow-open') {
        allowOpen = true;
        continue;
      }
      throw new Error(`Unknown review followup option: ${token}\n${GITHUB_REVIEW_HELP}`);
    }
    return { subcommand: 'followup', target, allowOpen };
  }

  const target = parseGithubTargetUrl(first);
  if (target.targetKind !== 'pr') {
    throw new Error(`nana review expects a pull request URL.\n${GITHUB_REVIEW_HELP}`);
  }
  let mode: GithubPullReviewMode = 'automatic';
  let reviewer = '@me';
  let perItemContext: GithubPullReviewPerItemContext = 'shared';
  for (let index = 1; index < values.length; index += 1) {
    const token = values[index];
    if (isHelpToken(token)) return { subcommand: 'help' };
    if (token === '--mode') {
      mode = parsePullReviewMode(values[index + 1], '--mode');
      index += 1;
      continue;
    }
    if (token.startsWith('--mode=')) {
      mode = parsePullReviewMode(token.slice('--mode='.length), '--mode');
      continue;
    }
    if (token === '--reviewer') {
      reviewer = values[index + 1]?.trim() || '';
      if (!reviewer) throw new Error(`Missing value after --reviewer.\n${GITHUB_REVIEW_HELP}`);
      index += 1;
      continue;
    }
    if (token.startsWith('--reviewer=')) {
      reviewer = token.slice('--reviewer='.length).trim();
      if (!reviewer) throw new Error(`Missing value after --reviewer.\n${GITHUB_REVIEW_HELP}`);
      continue;
    }
    if (token === '--per-item-context') {
      perItemContext = parsePullReviewPerItemContext(values[index + 1], '--per-item-context');
      index += 1;
      continue;
    }
    if (token.startsWith('--per-item-context=')) {
      perItemContext = parsePullReviewPerItemContext(token.slice('--per-item-context='.length), '--per-item-context');
      continue;
    }
    throw new Error(`Unknown review option: ${token}\n${GITHUB_REVIEW_HELP}`);
  }

  return {
    subcommand: 'review',
    target,
    mode,
    reviewer,
    perItemContext,
  };
}

function normalizeLogin(login: string | undefined): string | undefined {
  const trimmed = login?.trim();
  if (!trimmed) return undefined;
  return trimmed.replace(/^@/, '').toLowerCase();
}

function normalizeRepoSlugForCompare(slug: string): string {
  return slug.trim().toLowerCase();
}

function sanitizePathToken(value: string): string {
  const normalized = value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
  return normalized || 'default';
}

function gitExec(cwd: string, args: string[]): string {
  return execFileSync('git', args, {
    cwd,
    encoding: 'utf-8',
    stdio: ['ignore', 'pipe', 'pipe'],
  }).trim();
}

function gitExecAllowFailure(cwd: string, args: string[]): { ok: boolean; stdout: string; stderr: string } {
  try {
    return { ok: true, stdout: gitExec(cwd, args), stderr: '' };
  } catch (error) {
    const err = error as NodeJS.ErrnoException & { stderr?: string | Buffer; stdout?: string | Buffer };
    const stdout = typeof err.stdout === 'string'
      ? err.stdout
      : err.stdout instanceof Buffer
        ? err.stdout.toString('utf-8')
        : '';
    const stderr = typeof err.stderr === 'string'
      ? err.stderr
      : err.stderr instanceof Buffer
        ? err.stderr.toString('utf-8')
        : err.message;
    return { ok: false, stdout, stderr };
  }
}

function resolveNanaHomeDir(env: NodeJS.ProcessEnv, homeDirOverride?: string): string {
  const baseHome = homeDirOverride || env.HOME?.trim() || homedir();
  return join(baseHome, '.nana');
}

function resolveUserCodexHomeDir(
  env: NodeJS.ProcessEnv,
  homeDirOverride?: string,
): string {
  const explicit = env.CODEX_HOME?.trim();
  if (explicit) return explicit;
  const baseHome = homeDirOverride || env.HOME?.trim() || homedir();
  return defaultUserCodexHome(baseHome);
}

function managedRepoPaths(nanaHome: string, repoName: string): ManagedRepoPaths {
  const repoRoot = join(nanaHome, 'repos', repoName);
  return {
    nanaHome,
    globalGithubRoot: join(nanaHome, 'github-workon'),
    repoRoot,
    sourcePath: join(repoRoot, 'source'),
    repoMetaPath: join(repoRoot, 'repo.json'),
    repoSettingsPath: join(repoRoot, 'settings.json'),
    repoVerificationPlanPath: join(repoRoot, 'verification-plan.json'),
    repoVerificationDriftLogPath: join(repoRoot, 'verification-drift-events.jsonl'),
    repoUnitTestHistoryPath: join(repoRoot, 'verification-unit-history.tsv'),
    repoCiSuiteDurationsPath: join(repoRoot, 'verification-ci-suite-durations.json'),
    runsDir: join(repoRoot, 'runs'),
    sandboxesDir: join(repoRoot, 'sandboxes'),
    sandboxLocksDir: join(repoRoot, 'sandbox-locks'),
    repoLatestRunPath: join(repoRoot, 'latest-run.json'),
    globalLatestRunPath: join(nanaHome, 'github-workon', 'latest-run.json'),
  };
}

function githubReviewRulesGlobalConfigPath(nanaHome: string): string {
  return join(nanaHome, 'github-workon', 'review-rules-config.json');
}

function repoReviewRulesPathForSource(sourcePath: string): string {
  return join(sourcePath, '.nana', 'repo-review-rules.json');
}

async function readRepoReviewRulesDocument(sourcePath: string): Promise<GithubRepoReviewRulesDocument | null> {
  return readJsonFile<GithubRepoReviewRulesDocument>(repoReviewRulesPathForSource(sourcePath));
}

async function writeRepoReviewRulesDocument(sourcePath: string, value: GithubRepoReviewRulesDocument): Promise<void> {
  await writeJsonFile(repoReviewRulesPathForSource(sourcePath), value);
}

function readRepoReviewRulesDocumentSync(sourcePath: string): GithubRepoReviewRulesDocument | null {
  const path = repoReviewRulesPathForSource(sourcePath);
  if (!existsSync(path)) return null;
  try {
    return JSON.parse(readFileSync(path, 'utf-8')) as GithubRepoReviewRulesDocument;
  } catch {
    return null;
  }
}

async function readGithubReviewRulesGlobalConfig(nanaHome: string): Promise<GithubReviewRulesGlobalConfig | null> {
  return readJsonFile<GithubReviewRulesGlobalConfig>(githubReviewRulesGlobalConfigPath(nanaHome));
}

async function writeGithubReviewRulesGlobalConfig(
  nanaHome: string,
  mode: GithubReviewRulesMode,
  reviewerPolicy: GithubReviewRulesReviewerPolicy | undefined,
  now: Date,
): Promise<GithubReviewRulesGlobalConfig> {
  const config: GithubReviewRulesGlobalConfig = {
    version: 1,
    default_mode: mode,
    ...(reviewerPolicy ? { reviewer_policy: reviewerPolicy } : {}),
    updated_at: now.toISOString(),
  };
  await writeJsonFile(githubReviewRulesGlobalConfigPath(nanaHome), config);
  return config;
}

async function resolveGithubReviewRulesMode(
  nanaHome: string,
  repoSettings: ManagedRepoSettings | null | undefined,
): Promise<GithubReviewRulesMode> {
  if (repoSettings?.review_rules_mode) return repoSettings.review_rules_mode;
  const globalConfig = await readGithubReviewRulesGlobalConfig(nanaHome);
  return globalConfig?.default_mode ?? 'manual';
}

async function resolveGithubReviewRulesReviewerPolicy(
  nanaHome: string,
  repoSettings: ManagedRepoSettings | null | undefined,
): Promise<GithubReviewRulesReviewerPolicy | undefined> {
  if (repoSettings?.review_rules_reviewer_policy) return normalizeReviewerPolicy(repoSettings.review_rules_reviewer_policy);
  const globalConfig = await readGithubReviewRulesGlobalConfig(nanaHome);
  return normalizeReviewerPolicy(globalConfig?.reviewer_policy);
}

function githubRunPaths(repoRoot: string, runId: string): GithubRunPaths {
  const runDir = join(repoRoot, 'runs', runId);
  return {
    runDir,
    manifestPath: join(runDir, 'manifest.json'),
    startInstructionsPath: join(runDir, 'start-instructions.md'),
    feedbackInstructionsPath: join(runDir, 'feedback-instructions.md'),
  };
}

function githubPullReviewPaths(repoRoot: string, prNumber: number): GithubPullReviewPaths {
  const prRoot = join(repoRoot, 'reviews', `pr-${prNumber}`);
  return {
    prRoot,
    activePath: join(prRoot, 'active.json'),
    runsDir: join(prRoot, 'runs'),
  };
}

function githubPullReviewRunPaths(repoRoot: string, prNumber: number, runId: string): GithubPullReviewRunPaths {
  const reviewPaths = githubPullReviewPaths(repoRoot, prNumber);
  const runDir = join(reviewPaths.runsDir, runId);
  return {
    runDir,
    manifestPath: join(runDir, 'manifest.json'),
    reviewFilePath: join(runDir, 'review.md'),
    manualPendingPath: join(runDir, 'manual-pending.json'),
    candidatesPath: join(runDir, 'candidates.json'),
    acceptedPath: join(runDir, 'accepted.json'),
    droppedUserPath: join(runDir, 'dropped-user.json'),
    droppedNotRealPath: join(runDir, 'dropped-not-real.json'),
    droppedPreexistingPath: join(runDir, 'dropped-preexisting.json'),
  };
}

function sandboxPathFor(paths: ManagedRepoPaths, sandboxId: string): string {
  return join(paths.sandboxesDir, sandboxId);
}

function sandboxLockDirFor(paths: ManagedRepoPaths, sandboxId: string): string {
  return join(paths.sandboxLocksDir, sandboxId);
}

function sandboxLeasePath(lockDir: string): string {
  return join(lockDir, 'lease.json');
}

function sandboxMetadataPath(sandboxPath: string): string {
  return join(sandboxPath, '.nana', 'sandbox.json');
}

function sandboxMetricsPath(sandboxPath: string): string {
  return join(sandboxPath, '.nana', 'metrics.json');
}

function sandboxTokenAccountingPath(sandboxPath: string): string {
  return join(sandboxPath, '.nana', 'work-on-token-accounting.json');
}

function sandboxCodexHomePath(sandboxPath: string): string {
  return join(sandboxPath, '.codex');
}

function sandboxAgentCodexHomePath(sandboxPath: string, profileKey: string): string {
  return join(sandboxPath, '.codex-agents', sanitizePathToken(profileKey));
}

function sandboxNanaBinPath(sandboxPath: string): string {
  return join(sandboxPath, '.nana', 'bin');
}

function sandboxVerificationDir(sandboxPath: string): string {
  return join(sandboxPath, '.nana', 'work-on', 'verify');
}

function sandboxVerificationPolicyPath(sandboxPath: string): string {
  return join(sandboxVerificationDir(sandboxPath), 'unit-policy.env');
}

function sandboxVerificationUnitHistoryPath(sandboxPath: string): string {
  return join(sandboxVerificationDir(sandboxPath), 'unit-history.tsv');
}

function sandboxVerificationIntegrationPolicyPath(sandboxPath: string): string {
  return join(sandboxVerificationDir(sandboxPath), 'integration-policy.env');
}

function sandboxVerificationDriftLogPath(sandboxPath: string): string {
  return join(sandboxVerificationDir(sandboxPath), 'drift-events.jsonl');
}

function repoVerificationPlanPath(paths: ManagedRepoPaths): string {
  return paths.repoVerificationPlanPath;
}

function issueStatsPath(paths: ManagedRepoPaths, issueNumber: number): string {
  return join(paths.repoRoot, 'issues', `issue-${issueNumber}.json`);
}

function buildRunId(now: Date): string {
  return `gh-${now.getTime()}-${Math.random().toString(36).slice(2, 8)}`;
}

function buildSandboxBranch(target: ParsedGithubTargetUrl, sandboxId: string): string {
  return `nana/${target.targetKind}-${target.targetNumber}/${sanitizePathToken(sandboxId)}`;
}

function buildTargetSandboxId(targetKind: GithubTargetKind, targetNumber: number): string {
  return `${targetKind}-${targetNumber}`;
}

function buildInitialSandboxId(target: ParsedGithubTargetUrl, runId: string): string {
  if (target.targetKind === 'pr') return buildTargetSandboxId('pr', target.targetNumber);
  const token = sanitizePathToken(runId.replace(/^gh-/, '')).slice(0, 12) || 'run';
  return `issue-${target.targetNumber}-pr-${token}`;
}

function ensureGithubLaunchBypass(args: readonly string[]): string[] {
  if (args.includes(CODEX_BYPASS_FLAG)) return [...args];
  return [CODEX_BYPASS_FLAG, ...args];
}

function buildConsiderationPipeline(
  activeConsiderations: readonly GithubConsideration[],
  roleLayout: GithubRoleLayout = 'split',
): GithubPipelineLane[] {
  const resolveConsiderationLanes = (consideration: GithubConsideration): readonly GithubPipelineLane[] => {
    if (roleLayout === 'reviewer+executor') {
      return MERGED_CONSIDERATION_PIPELINE_SPECS[consideration];
    }

    const bootstrapMerged = MERGED_CONSIDERATION_PIPELINE_SPECS[consideration].filter((lane) => lane.activation === 'bootstrap');
    const splitHardening = SPLIT_CONSIDERATION_PIPELINE_SPECS[consideration].filter((lane) => lane.activation !== 'bootstrap');
    return [...bootstrapMerged, ...splitHardening];
  };

  const lanes = [
    ...SPLIT_CONSIDERATION_PIPELINE_SPECS.base,
    ...activeConsiderations.flatMap((consideration) => resolveConsiderationLanes(consideration)),
  ];
  const deduped: GithubPipelineLane[] = [];
  const seen = new Set<string>();
  const activationOrder: readonly GithubPipelineActivation[] = ['bootstrap', 'hardening'];

  for (const lane of lanes) {
    const promptRoles = lane.prompt_roles ?? [];
    const key = `${lane.alias}:${lane.role}:${lane.activation}:${lane.phase}:${lane.owner}:${lane.mode}:${lane.blocking}:${promptRoles.join('+')}`;
    if (seen.has(key)) continue;
    seen.add(key);
    deduped.push({ ...lane });
  }

  return deduped.sort((left, right) => {
    const activationCompare = activationOrder.indexOf(left.activation) - activationOrder.indexOf(right.activation);
    if (activationCompare !== 0) return activationCompare;
    const phaseCompare = PIPELINE_PHASE_ORDER.indexOf(left.phase) - PIPELINE_PHASE_ORDER.indexOf(right.phase);
    if (phaseCompare !== 0) return phaseCompare;
    return left.alias.localeCompare(right.alias);
  });
}

function lanePromptArtifactsDir(runPaths: GithubRunPaths): string {
  return join(runPaths.runDir, 'lane-prompts');
}

function promptPathForRole(role: string): string {
  return join(getPackageRoot(), 'prompts', `${role}.md`);
}

async function readPromptSurface(role: string): Promise<string> {
  const path = promptPathForRole(role);
  if (!existsSync(path)) {
    throw new Error(`Prompt file missing for merged work-on role "${role}": ${path}`);
  }
  return stripFrontmatter(await readFile(path, 'utf-8'));
}

async function composeMergedLanePrompt(lane: GithubPipelineLane): Promise<string> {
  const promptRoles = lane.prompt_roles ?? [];
  if (promptRoles.length < 2) {
    throw new Error(`Lane ${lane.alias} does not define enough source prompts to compose a merged reviewer+executor artifact.`);
  }

  const [reviewerRole, executorRole] = promptRoles;
  const [reviewerPrompt, executorPrompt] = await Promise.all([
    readPromptSurface(reviewerRole),
    readPromptSurface(executorRole),
  ]);

  return [
    `# NANA merged lane prompt: ${lane.alias}`,
    '',
    'You are a merged reviewer+executor lane for the NANA work-on workflow.',
    `Lane alias: ${lane.alias}`,
    `Synthetic role label: ${lane.role}`,
    `Source prompts: ${promptRoles.join(' + ')}`,
    `Phase: ${lane.phase}`,
    `Mode: ${lane.mode}`,
    `Blocking: ${lane.blocking ? 'yes' : 'no'}`,
    '',
    'Operating contract:',
    `- Start from the reviewer specialization in \`${reviewerRole}\` to inspect this lane's concern and identify the required changes.`,
    `- Then adopt the executor delivery contract in \`${executorRole}\` and implement the necessary fixes yourself within this lane.`,
    '- Stay within this lane’s concern and avoid freelancing into unrelated domains.',
    '- If you find an issue outside your lane, report it upward instead of silently absorbing it.',
    '- Do not stop at findings. Own the edits, verification, and closeout for the issues you decide are in-lane.',
    '',
    `## Reviewer Specialization (${reviewerRole})`,
    '',
    reviewerPrompt.trim(),
    '',
    `## Executor Delivery Contract (${executorRole})`,
    '',
    executorPrompt.trim(),
    '',
  ].join('\n');
}

async function writeLanePromptArtifacts(
  runPaths: GithubRunPaths,
  pipeline: readonly GithubPipelineLane[],
): Promise<GithubLanePromptArtifact[]> {
  const artifacts: GithubLanePromptArtifact[] = [];
  const promptDir = lanePromptArtifactsDir(runPaths);
  let createdDir = false;

  for (const lane of pipeline) {
    const promptRoles = lane.prompt_roles ?? [];
    if (lane.mode !== 'review+execute' || promptRoles.length < 2) continue;
    if (!createdDir) {
      await mkdir(promptDir, { recursive: true });
      createdDir = true;
    }

    const fileName = `${sanitizePathToken(lane.alias)}.md`;
    const promptPath = join(promptDir, fileName);
    await writeFile(promptPath, await composeMergedLanePrompt(lane), 'utf-8');
    artifacts.push({
      alias: lane.alias,
      role: lane.role,
      prompt_path: promptPath,
      prompt_roles: promptRoles,
    });
  }

  return artifacts.sort((left, right) => left.alias.localeCompare(right.alias));
}

function resolveLaneCodexProfileName(lane: GithubPipelineLane): GithubCodexProfileName {
  if (lane.alias === 'coder' || lane.role === 'executor' || lane.mode === 'review+execute' || lane.mode === 'execute') {
    return 'executor';
  }
  if (lane.role === 'test-engineer') return 'executor';
  return 'reviewer';
}

function laneExecInstructionsCommand(runId: string, laneAlias: string, task?: string): string {
  const base = `nana work-on lane-exec --run-id ${runId} --lane ${laneAlias}`;
  return task ? `${base} --task ${JSON.stringify(task)}` : base;
}

function buildConsiderationInstructionLines(
  manifest: Pick<GithubWorkonManifest, 'run_id' | 'considerations_active' | 'consideration_pipeline' | 'role_layout' | 'lane_prompt_artifacts'>,
): string[] {
  const activeConsiderations = manifest.considerations_active ?? [];
  const roleLayout = manifest.role_layout ?? 'split';
  const pipeline = manifest.consideration_pipeline ?? buildConsiderationPipeline(activeConsiderations, roleLayout);
  const lanePromptArtifacts = manifest.lane_prompt_artifacts ?? [];

  const header = activeConsiderations.length > 0
    ? `Active considerations: ${activeConsiderations.join(', ')}.`
    : 'Active considerations: none.';

  const lines = [
    header,
    `Role layout: ${roleLayout}.`,
    'Pipeline is composed from the base coder lane plus the active consideration packs.',
    'Execution is staged:',
    '- Bootstrap loop: use only the coder lane plus the architect overview lane to land the basic feature and get minimal verification working.',
    '- Hardening loop: only after the basic feature exists and basic verification passes, activate the remaining consideration lanes for API, perf, QA, security, dependency, and style follow-up.',
  ];

  if (activeConsiderations.length === 0) {
    lines.push('Stay single-owner for this run. Do not spawn native subagents or tmux team workers unless considerations are added later.');
    if (roleLayout === 'reviewer+executor') {
      lines.push('The reviewer+executor role layout is configured, but no consideration lanes are active yet, so the run still stays single-owner.');
    }
  } else {
    lines.push('Run this as a coordinated multi-lane session using isolated Codex lane processes, not native subagents.');
    lines.push('Launch lane processes with `nana work-on lane-exec --run-id <run-id> --lane <alias>` so each lane gets its own CODEX_HOME and MCP profile.');
    if (roleLayout === 'reviewer+executor') {
      lines.push('For merged lanes, run one isolated lane process per lane alias so that the same agent owns both review and implementation for that lane.');
      lines.push('Do not activate hardening lanes during bootstrap. Start with the basic feature loop, then add the extra merged lanes only after the feature exists and basic verification passes.');
    } else {
      lines.push('Do not run every lane immediately. Start with the architect overview plus coder loop, and defer the extra reviewer/executor lanes until the basic feature is implemented.');
      lines.push('In split mode, bootstrap reviewer lanes still use isolated lane processes; only the later hardening loop adds the extra reviewer/executor lanes.');
    }
  }

  lines.push('Pipeline:');
  for (const activation of ['bootstrap', 'hardening'] as const) {
    const activationLanes = pipeline.filter((lane) => lane.activation === activation);
    if (activationLanes.length === 0) continue;
    lines.push(`- ${activation}:`);
    for (const phase of PIPELINE_PHASE_ORDER) {
      const phaseLanes = activationLanes.filter((lane) => lane.phase === phase);
      if (phaseLanes.length === 0) continue;
      lines.push(`  - ${phase}:`);
      for (const lane of phaseLanes) {
        const blockingLabel = lane.blocking ? 'blocking' : 'advisory';
        lines.push(
          `    - ${lane.alias} -> ${lane.role} [${lane.mode}, owner=${lane.owner}, ${blockingLabel}] ${lane.purpose}`,
        );
        lines.push(`      lane command: ${laneExecInstructionsCommand(manifest.run_id, lane.alias, lane.purpose)}`);
        const artifact = lanePromptArtifacts.find((candidate) => candidate.alias === lane.alias && candidate.role === lane.role);
        if (artifact) {
          lines.push(`      merged prompt: ${artifact.prompt_path}`);
        }
      }
    }
  }

  lines.push('Execution policy:');
  if (roleLayout === 'reviewer+executor') {
    lines.push('- `review+execute` lanes use the merged prompt artifact for that lane and own both diagnosis and remediation inside their concern.');
    lines.push('- The coder lane still owns general feature integration and any work not claimed by a specialist lane.');
  } else {
    lines.push('- Review lanes do not directly mutate code unless explicitly stated otherwise; their findings are acted on by the lane owner.');
  }
  lines.push('- `owner=self` means that lane may perform its own edits/tests.');
  lines.push('- `owner=coder` means findings from that lane are integrated and implemented by the coder lane.');
  lines.push('- Blocking lanes must be satisfied before completion.');
  return lines;
}

function buildCiPolicyLines(
  manifest: Pick<GithubWorkonManifest, 'target_kind' | 'create_pr_on_complete'>,
): string[] {
  const needsPrCi = manifest.target_kind === 'pr' || manifest.create_pr_on_complete;
  if (!needsPrCi) return [];

  return [
    'CI policy:',
    '- After creating/updating a PR, wait for CI to complete before declaring the task done.',
    '- Keep iterating until CI is green when failures are attributable to your changes.',
    '- If a workflow failure looks environmental or transient, rerun the failed jobs/workflow instead of treating it as a product-code regression.',
    '- If a failure looks flaky, rerun it at least once before classifying it as a real regression; if flakiness persists, document it.',
    '- If a CI failure is clearly pre-existing and unrelated to your changes, do not chase it indefinitely. Record it in `.nana/pre-existing-ci-failures.md` inside the sandbox and mention it in the final report. Create a repo issue only if the repo already treats CI debt that way or the user explicitly asks.',
    '- If you document flaky failures, use `.nana/ci-flakes.md` inside the sandbox.',
    '- Environmental failures should be retried; pre-existing unrelated failures should be documented; change-caused failures should be fixed.',
  ];
}

function isPidAlive(pid: number): boolean {
  if (!Number.isFinite(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

export function isSandboxLeaseStale(lease: SandboxLease, nowMs = Date.now()): boolean {
  const expiresAt = Date.parse(lease.expires_at);
  if (Number.isFinite(expiresAt) && expiresAt <= nowMs) return true;
  return !isPidAlive(lease.owner_pid);
}

async function readJsonFile<T>(path: string): Promise<T | null> {
  if (!existsSync(path)) return null;
  try {
    return JSON.parse(await readFile(path, 'utf-8')) as T;
  } catch {
    return null;
  }
}

async function writeJsonFile(path: string, value: unknown): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, JSON.stringify(value, null, 2));
}

async function readSandboxLease(lockDir: string): Promise<SandboxLease | null> {
  return readJsonFile<SandboxLease>(sandboxLeasePath(lockDir));
}

async function writeSandboxLease(lockDir: string, lease: SandboxLease): Promise<void> {
  await mkdir(lockDir, { recursive: true });
  await writeJsonFile(sandboxLeasePath(lockDir), lease);
}

async function releaseSandboxLease(lockDir: string): Promise<void> {
  await rm(lockDir, { recursive: true, force: true });
}

function resolveGhHostname(apiBaseUrl: string): string {
  const url = new URL(apiBaseUrl);
  if (url.hostname === 'api.github.com') return 'github.com';
  return url.hostname;
}

export function resolveGithubToken(
  env: NodeJS.ProcessEnv,
  apiBaseUrl = DEFAULT_GITHUB_API_BASE_URL,
  execFileSyncImpl: typeof execFileSync = execFileSync,
): string {
  const token = env.GH_TOKEN?.trim() || env.GITHUB_TOKEN?.trim();
  if (token) return token;

  const hostname = resolveGhHostname(apiBaseUrl);
  try {
    const args = hostname === 'github.com'
      ? ['auth', 'token']
      : ['auth', 'token', '--hostname', hostname];
    const ghToken = execFileSyncImpl('gh', args, {
      encoding: 'utf-8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
    if (ghToken) return ghToken;
  } catch {
    // fall through
  }

  throw new Error(
    'GitHub API auth missing. Set GH_TOKEN/GITHUB_TOKEN or log in via `gh auth login` so `gh auth token` is available.',
  );
}

async function githubApiJson<T>(path: string, context: GithubApiContext): Promise<T> {
  const url = new URL(path, context.apiBaseUrl.endsWith('/') ? context.apiBaseUrl : `${context.apiBaseUrl}/`);
  const response = await context.fetchImpl(url, {
    headers: {
      Accept: GITHUB_ACCEPT_HEADER,
      Authorization: `Bearer ${context.token}`,
      'User-Agent': 'nana',
      'X-GitHub-Api-Version': '2022-11-28',
    },
  });
  if (!response.ok) {
    let detail = '';
    try {
      const body = await response.json() as { message?: string };
      detail = body.message ? `: ${body.message}` : '';
    } catch {
      // ignore
    }
    throw new Error(`GitHub API request failed (${response.status} ${response.statusText})${detail}`);
  }
  return await response.json() as T;
}

async function githubApiRequestJson<T>(
  method: 'POST' | 'PATCH',
  path: string,
  body: unknown,
  context: GithubApiContext,
): Promise<T> {
  const url = new URL(path, context.apiBaseUrl.endsWith('/') ? context.apiBaseUrl : `${context.apiBaseUrl}/`);
  const response = await context.fetchImpl(url, {
    method,
    headers: {
      Accept: GITHUB_ACCEPT_HEADER,
      Authorization: `Bearer ${context.token}`,
      'Content-Type': 'application/json',
      'User-Agent': 'nana',
      'X-GitHub-Api-Version': '2022-11-28',
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    let detail = '';
    try {
      const parsed = await response.json() as { message?: string };
      detail = parsed.message ? `: ${parsed.message}` : '';
    } catch {
      // ignore
    }
    throw new Error(`GitHub API request failed (${response.status} ${response.statusText})${detail}`);
  }
  return await response.json() as T;
}

async function githubApiRequestNoContent(
  method: 'POST',
  path: string,
  context: GithubApiContext,
): Promise<void> {
  const url = new URL(path, context.apiBaseUrl.endsWith('/') ? context.apiBaseUrl : `${context.apiBaseUrl}/`);
  const response = await context.fetchImpl(url, {
    method,
    headers: {
      Accept: GITHUB_ACCEPT_HEADER,
      Authorization: `Bearer ${context.token}`,
      'User-Agent': 'nana',
      'X-GitHub-Api-Version': '2022-11-28',
    },
  });
  if (!response.ok) {
    let detail = '';
    try {
      const parsed = await response.json() as { message?: string };
      detail = parsed.message ? `: ${parsed.message}` : '';
    } catch {
      // ignore
    }
    throw new Error(`GitHub API request failed (${response.status} ${response.statusText})${detail}`);
  }
}

async function resolveViewerLogin(context: GithubApiContext): Promise<string> {
  const viewer = await githubApiJson<{ login: string }>('/user', context);
  return viewer.login;
}

async function fetchTargetContext(target: ParsedGithubTargetUrl, context: GithubApiContext): Promise<GithubTargetContext> {
  const repository = await githubApiJson<GithubRepositoryPayload>(`/repos/${target.repoSlug}`, context);
  const issue = await githubApiJson<GithubIssuePayload>(
    `/repos/${target.repoSlug}/issues/${target.targetNumber}`,
    context,
  );
  if (target.targetKind === 'issue' && issue.pull_request) {
    throw new Error(`Target ${target.repoSlug}#${target.targetNumber} is a pull request. Use its pull request URL instead.`);
  }
  if (target.targetKind === 'pr') {
    const pullRequest = await githubApiJson<GithubPullRequestPayload>(
      `/repos/${target.repoSlug}/pulls/${target.targetNumber}`,
      context,
    );
    return { repository, issue, pullRequest };
  }
  return { repository, issue };
}

async function fetchRepositoryContext(repoSlug: string, context: GithubApiContext): Promise<GithubRepositoryPayload> {
  return githubApiJson<GithubRepositoryPayload>(`/repos/${repoSlug}`, context);
}

async function listRepositoryPullRequests(
  repoSlug: string,
  context: GithubApiContext,
): Promise<GithubPullRequestPayload[]> {
  const pulls: GithubPullRequestPayload[] = [];
  for (let page = 1; page <= 20; page += 1) {
    const batch = await githubApiJson<GithubPullRequestPayload[]>(
      `/repos/${repoSlug}/pulls?state=all&per_page=100&page=${page}`,
      context,
    );
    if (batch.length === 0) break;
    pulls.push(...batch);
    if (batch.length < 100) break;
  }
  return pulls;
}

async function collectReviewHistoryForPullRequests(
  repoSlug: string,
  prNumbers: readonly number[],
  context: GithubApiContext,
): Promise<{
  reviews: Array<{ prNumber: number; review: GithubPullReviewPayload }>;
  reviewComments: Array<{ prNumber: number; comment: GithubPullReviewCommentPayload }>;
  prHeadShas: Record<number, string>;
}> {
  const reviews: Array<{ prNumber: number; review: GithubPullReviewPayload }> = [];
  const reviewComments: Array<{ prNumber: number; comment: GithubPullReviewCommentPayload }> = [];
  const prHeadShas: Record<number, string> = {};

  for (const prNumber of [...new Set(prNumbers)].sort((left, right) => left - right)) {
    const [pullRequest, prReviews, prReviewComments] = await Promise.all([
      githubApiJson<GithubPullRequestPayload>(
        `/repos/${repoSlug}/pulls/${prNumber}`,
        context,
      ),
      githubApiJson<GithubPullReviewPayload[]>(
        `/repos/${repoSlug}/pulls/${prNumber}/reviews?per_page=100`,
        context,
      ),
      githubApiJson<GithubPullReviewCommentPayload[]>(
        `/repos/${repoSlug}/pulls/${prNumber}/comments?per_page=100`,
        context,
      ),
    ]);
    prHeadShas[prNumber] = pullRequest.head.sha;
    reviews.push(...prReviews.map((review) => ({ prNumber, review })));
    reviewComments.push(...prReviewComments.map((comment) => ({ prNumber, comment })));
  }

  return { reviews, reviewComments, prHeadShas };
}

async function collectIssueLinkedPullNumbers(
  nanaHome: string,
  target: ParsedGithubTargetUrl,
): Promise<number[]> {
  const paths = managedRepoPaths(nanaHome, join(target.owner, target.repoName));
  if (!existsSync(paths.runsDir)) return [];
  const entries = await readdir(paths.runsDir, { withFileTypes: true }).catch(() => []);
  const prNumbers = new Set<number>();
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const manifest = await readManifest(join(paths.runsDir, entry.name, 'manifest.json')).catch(() => null);
    if (!manifest) continue;
    if (normalizeRepoSlugForCompare(manifest.repo_slug) !== normalizeRepoSlugForCompare(target.repoSlug)) continue;
    if (manifest.target_kind !== 'issue' || manifest.target_number !== target.targetNumber) continue;
    if (typeof manifest.published_pr_number === 'number') prNumbers.add(manifest.published_pr_number);
  }
  return [...prNumbers].sort((left, right) => left - right);
}

async function buildReviewRuleScanSourceForTarget(
  nanaHome: string,
  target: ParsedGithubTargetUrl,
): Promise<GithubReviewRuleScanSource> {
  if (target.targetKind === 'pr') {
    return {
      repoSlug: target.repoSlug,
      owner: target.owner,
      repoName: target.repoName,
      sourceKind: 'pr',
      sourceTarget: target.canonicalUrl,
      prNumbers: [target.targetNumber],
    };
  }
  return {
    repoSlug: target.repoSlug,
    owner: target.owner,
    repoName: target.repoName,
    sourceKind: 'issue',
    sourceTarget: target.canonicalUrl,
    issueNumber: target.targetNumber,
    prNumbers: await collectIssueLinkedPullNumbers(nanaHome, target),
  };
}

async function fetchPullRequestsForHeadBranch(
  repoSlug: string,
  repoOwner: string,
  branchName: string,
  context: GithubApiContext,
): Promise<GithubPullRequestPayload[]> {
  const encodedHead = encodeURIComponent(`${repoOwner}:${branchName}`);
  return githubApiJson<GithubPullRequestPayload[]>(
    `/repos/${repoSlug}/pulls?state=all&head=${encodedHead}&per_page=100`,
    context,
  );
}

function buildAutomaticPublicationCommitMessage(manifest: GithubWorkonManifest): string {
  return [
    `Publish work-on results for ${manifest.repo_slug} ${manifest.target_kind} #${manifest.target_number}`,
    '',
    `Constraint: Automatic work-on publication requires a commit before draft PR creation`,
    `Confidence: medium`,
    `Scope-risk: moderate`,
    `Directive: Replace this generic publication commit message with a more specific one if follow-up manual edits are needed`,
    `Tested: Autonomous work-on run completed before publication`,
    `Not-tested: Manual review of commit narrative`,
  ].join('\n');
}

function repoHasUncommittedChanges(repoCheckoutPath: string): boolean {
  const status = gitExecAllowFailure(repoCheckoutPath, ['status', '--porcelain']);
  return status.ok && status.stdout.trim().length > 0;
}

function resolvePublicationBranch(
  manifest: GithubWorkonManifest,
  sandboxBranchName: string,
): { localBranch: string; remoteBranch: string } {
  if (
    manifest.target_kind === 'pr' &&
    manifest.pr_head_ref &&
    (!manifest.pr_head_repo || normalizeRepoSlugForCompare(manifest.pr_head_repo) === normalizeRepoSlugForCompare(manifest.repo_slug))
  ) {
    return { localBranch: sandboxBranchName, remoteBranch: manifest.pr_head_ref };
  }
  return { localBranch: sandboxBranchName, remoteBranch: sandboxBranchName };
}

async function ensureCommittedForPublication(
  repoCheckoutPath: string,
  manifest: GithubWorkonManifest,
): Promise<{ headSha: string; createdCommit: boolean }> {
  let createdCommit = false;
  if (repoHasUncommittedChanges(repoCheckoutPath)) {
    gitExec(repoCheckoutPath, ['add', '-A']);
    gitExec(repoCheckoutPath, ['commit', '-m', buildAutomaticPublicationCommitMessage(manifest)]);
    createdCommit = true;
  }
  return {
    headSha: gitExec(repoCheckoutPath, ['rev-parse', 'HEAD']),
    createdCommit,
  };
}

function pushPublicationBranch(
  repoCheckoutPath: string,
  localBranch: string,
  remoteBranch: string,
): void {
  gitExec(repoCheckoutPath, ['push', '--set-upstream', 'origin', `${localBranch}:${remoteBranch}`]);
}

function readRemoteBranchHeadSha(
  repoCheckoutPath: string,
  remoteBranch: string,
): string | undefined {
  const result = gitExecAllowFailure(repoCheckoutPath, ['ls-remote', '--heads', 'origin', `refs/heads/${remoteBranch}`]);
  if (!result.ok) return undefined;
  const firstLine = result.stdout.split('\n').map((line) => line.trim()).find(Boolean);
  if (!firstLine) return undefined;
  const [sha] = firstLine.split(/\s+/);
  return sha?.trim() || undefined;
}

async function recordPublisherMilestone(
  runPaths: GithubRunPaths,
  input: {
    runId: string;
    milestone: string;
    at: Date;
    detail?: string;
    headSha?: string;
    branch?: string;
    prNumber?: number;
    prOpened?: boolean;
    ciWaiting?: boolean;
    ciGreen?: boolean;
    blocked?: boolean;
    blockedReason?: string;
    blockedReasonCategory?: string;
    blockedRetryable?: boolean;
    diagnosticsPath?: string;
  },
): Promise<void> {
  const existing = await readPublisherStatus(runPaths) ?? {
    run_id: input.runId,
    session_active: false,
    started: true,
    pr_opened: false,
    ci_waiting: false,
    ci_green: false,
    blocked: false,
    milestones: [],
    updated_at: input.at.toISOString(),
  };
  const milestoneEntry = {
    milestone: input.milestone,
    at: input.at.toISOString(),
    detail: input.detail,
    head_sha: input.headSha,
    pr_number: input.prNumber,
  };
  await writePublisherStatus(runPaths, {
    ...existing,
    pr_opened: input.prOpened ?? existing.pr_opened,
    ci_waiting: input.ciWaiting ?? existing.ci_waiting,
    ci_green: input.ciGreen ?? existing.ci_green,
    blocked: input.blocked ?? existing.blocked,
    blocked_reason: input.blockedReason ?? (input.blocked === false ? undefined : existing.blocked_reason),
    blocked_reason_category: input.blockedReasonCategory ?? (input.blocked === false ? undefined : existing.blocked_reason_category),
    blocked_retryable: input.blockedRetryable ?? (input.blocked === false ? undefined : existing.blocked_retryable),
    current_head_sha: input.headSha ?? existing.current_head_sha,
    current_branch: input.branch ?? existing.current_branch,
    current_pr_number: input.prNumber ?? existing.current_pr_number,
    diagnostics_path: input.diagnosticsPath ?? existing.diagnostics_path,
    last_milestone: input.milestone,
    milestones: [...(existing.milestones ?? []), milestoneEntry],
    updated_at: input.at.toISOString(),
  });
}

async function readCiStateForHeadSha(
  manifest: GithubWorkonManifest,
  headSha: string,
  context: GithubApiContext,
): Promise<'ci_green' | 'ci_waiting' | 'blocked'> {
  const [checkRuns, workflowRuns] = await Promise.all([
    fetchCheckRunsForSha(manifest.repo_slug, headSha, context).catch(() => [] as GithubCheckRunPayload[]),
    fetchWorkflowRunsForSha(manifest.repo_slug, headSha, context).catch(() => [] as GithubWorkflowRunPayload[]),
  ]);
  const relevantWorkflowRuns = workflowRuns.filter((run) => run.head_sha === headSha);
  const hasAnyChecks = checkRuns.length > 0 || relevantWorkflowRuns.length > 0;
  const hasPendingChecks = checkRuns.some((check) => check.status !== 'completed');
  const hasPendingRuns = relevantWorkflowRuns.some((run) => run.status !== 'completed');
  const hasFailures = checkRuns.some((check) => !isSuccessfulConclusion(check.conclusion))
    || relevantWorkflowRuns.some((run) => !isSuccessfulConclusion(run.conclusion));
  if (hasAnyChecks && !hasPendingChecks && !hasPendingRuns && !hasFailures) return 'ci_green';
  if (hasFailures) return 'blocked';
  return 'ci_waiting';
}

function buildDraftPullRequestBody(
  manifest: GithubWorkonManifest,
  target: GithubTargetContext,
  remoteBranch: string,
): string {
  return [
    `Closes ${manifest.target_url}`,
    '',
    `Autogenerated by NANA work-on.`,
    '',
    `- Target: ${manifest.target_kind} #${manifest.target_number}`,
    `- Branch: ${remoteBranch}`,
    `- Role layout: ${manifest.role_layout}`,
    `- Considerations: ${manifest.considerations_active.join(', ') || '(none)'}`,
    '',
    '## Context',
    '',
    target.issue.body?.trim() || '(none)',
    '',
  ].join('\n');
}

async function createDraftPullRequest(
  manifest: GithubWorkonManifest,
  target: GithubTargetContext,
  remoteBranch: string,
  context: GithubApiContext,
): Promise<GithubPullRequestPayload> {
  return githubApiRequestJson<GithubPullRequestPayload>(
    'POST',
    `/repos/${manifest.repo_slug}/pulls`,
    {
      title: target.issue.title,
      head: `${manifest.repo_owner}:${remoteBranch}`,
      base: manifest.default_branch,
      body: buildDraftPullRequestBody(manifest, target, remoteBranch),
      draft: true,
    },
    context,
  );
}

async function updateDraftPullRequest(
  manifest: GithubWorkonManifest,
  prNumber: number,
  target: GithubTargetContext,
  remoteBranch: string,
  context: GithubApiContext,
): Promise<GithubPullRequestPayload> {
  return githubApiRequestJson<GithubPullRequestPayload>(
    'PATCH',
    `/repos/${manifest.repo_slug}/pulls/${prNumber}`,
    {
      title: target.issue.title,
      body: buildDraftPullRequestBody(manifest, target, remoteBranch),
      draft: true,
      base: manifest.default_branch,
    },
    context,
  );
}

async function fetchFeedbackSnapshot(
  manifest: Pick<GithubWorkonManifest, 'repo_owner' | 'repo_name' | 'target_number' | 'target_kind' | 'published_pr_number'>,
  reviewer: string,
  context: GithubApiContext,
  targetOverride?: ParsedGithubTargetUrl,
): Promise<GithubFeedbackSnapshot> {
  const reviewerLogin = normalizeLogin(reviewer);
  const effectiveIssueNumber = targetOverride?.targetKind === 'issue'
    ? targetOverride.targetNumber
    : manifest.target_number;
  const issueComments = await githubApiJson<GithubIssueCommentPayload[]>(
    `/repos/${manifest.repo_owner}/${manifest.repo_name}/issues/${effectiveIssueNumber}/comments?per_page=100`,
    context,
  );
  const filteredIssueComments = issueComments.filter((comment) => normalizeLogin(comment.user?.login) === reviewerLogin);

  const effectivePrNumber = targetOverride?.targetKind === 'pr'
    ? targetOverride.targetNumber
    : manifest.target_kind === 'pr'
      ? manifest.target_number
    : manifest.published_pr_number;

  if (effectivePrNumber == null) {
    return {
      issueComments: filteredIssueComments,
      reviews: [],
      reviewComments: [],
    };
  }

  const [reviews, reviewComments] = await Promise.all([
    githubApiJson<GithubPullReviewPayload[]>(
      `/repos/${manifest.repo_owner}/${manifest.repo_name}/pulls/${effectivePrNumber}/reviews?per_page=100`,
      context,
    ),
    githubApiJson<GithubPullReviewCommentPayload[]>(
      `/repos/${manifest.repo_owner}/${manifest.repo_name}/pulls/${effectivePrNumber}/comments?per_page=100`,
      context,
    ),
  ]);

  return {
    issueComments: filteredIssueComments,
    reviews: reviews.filter((review) => normalizeLogin(review.user?.login) === reviewerLogin),
    reviewComments: reviewComments.filter((comment) => normalizeLogin(comment.user?.login) === reviewerLogin),
  };
}

function filterNewFeedback(snapshot: GithubFeedbackSnapshot, manifest: GithubWorkonManifest): GithubFeedbackSnapshot {
  return {
    issueComments: snapshot.issueComments.filter((comment) => comment.id > manifest.last_seen_issue_comment_id),
    reviews: snapshot.reviews.filter((review) => review.id > manifest.last_seen_review_id),
    reviewComments: snapshot.reviewComments.filter((comment) => comment.id > manifest.last_seen_review_comment_id),
  };
}

function advanceFeedbackCursor(
  manifest: GithubWorkonManifest,
  snapshot: GithubFeedbackSnapshot,
): GithubFeedbackCursor {
  return {
    issueCommentId: snapshot.issueComments.reduce((max, comment) => Math.max(max, comment.id), manifest.last_seen_issue_comment_id),
    reviewId: snapshot.reviews.reduce((max, review) => Math.max(max, review.id), manifest.last_seen_review_id),
    reviewCommentId: snapshot.reviewComments.reduce((max, comment) => Math.max(max, comment.id), manifest.last_seen_review_comment_id),
  };
}

function resolveReviewerLogin(rawReviewer: string | undefined, viewerLogin: string): string {
  const normalized = rawReviewer?.trim();
  if (!normalized || normalized === '@me') return viewerLogin;
  return normalized.replace(/^@/, '');
}

function formatMarkdownBlock(title: string, body: string | null | undefined): string[] {
  return [`## ${title}`, '', body?.trim() ? body.trim() : '(none)', ''];
}

function renderFeedbackMarkdown(feedback: GithubFeedbackSnapshot, reviewer: string): string {
  const sections: string[] = [];

  if (feedback.issueComments.length > 0) {
    sections.push('## Issue comments', '');
    for (const comment of feedback.issueComments) {
      sections.push(`### Comment ${comment.id} by @${comment.user?.login ?? reviewer} at ${comment.updated_at}`);
      sections.push(comment.body?.trim() || '(no body)');
      sections.push(`Link: ${comment.html_url}`);
      sections.push('');
    }
  }

  if (feedback.reviews.length > 0) {
    sections.push('## Pull request reviews', '');
    for (const review of feedback.reviews) {
      sections.push(`### Review ${review.id} by @${review.user?.login ?? reviewer} at ${review.submitted_at ?? 'unknown time'}`);
      sections.push(`State: ${review.state}`);
      if (review.commit_id) sections.push(`Commit: ${review.commit_id}`);
      sections.push(review.body?.trim() || '(no body)');
      sections.push(`Link: ${review.html_url}`);
      sections.push('');
    }
  }

  if (feedback.reviewComments.length > 0) {
    sections.push('## Pull request review comments', '');
    for (const comment of feedback.reviewComments) {
      sections.push(`### Review comment ${comment.id} by @${comment.user?.login ?? reviewer} at ${comment.updated_at}`);
      sections.push(`Path: ${comment.path}${comment.line != null ? `:${comment.line}` : comment.original_line != null ? `:${comment.original_line}` : ''}`);
      if (comment.pull_request_review_id != null) sections.push(`Review id: ${comment.pull_request_review_id}`);
      sections.push(comment.body?.trim() || '(no body)');
      sections.push(`Link: ${comment.html_url}`);
      sections.push('');
    }
  }

  if (sections.length === 0) {
    return `## Reviewer feedback from @${reviewer}\n\n(no matching feedback found)\n`;
  }

  return [`# Reviewer feedback from @${reviewer}`, '', ...sections].join('\n');
}

interface GithubReviewRuleSignal {
  template: (typeof REVIEW_RULE_LIBRARY)[number];
  evidence: GithubRepoReviewRuleEvidence;
}

function normalizeReviewRuleEvidenceText(text: string | null | undefined): string {
  return (text ?? '')
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/`[^`]*`/g, ' ')
    .replace(/\[[^\]]+\]\([^)]+\)/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function buildReviewRuleExcerpt(text: string | null | undefined, maxLength = 180): string {
  const normalized = normalizeReviewRuleEvidenceText(text);
  if (normalized.length <= maxLength) return normalized;
  return `${normalized.slice(0, Math.max(0, maxLength - 1)).trimEnd()}…`;
}

async function readReviewCommentCodeContext(
  sourcePath: string,
  comment: Pick<GithubPullReviewCommentPayload, 'path' | 'line' | 'original_line'>,
): Promise<{ excerpt?: string; provenance: 'current_checkout' | 'unknown'; ref?: string }> {
  const lineNumber = comment.line ?? comment.original_line ?? undefined;
  if (!comment.path || lineNumber == null || lineNumber <= 0) return { provenance: 'unknown' };
  const absolutePath = join(sourcePath, comment.path);
  if (!existsSync(absolutePath)) return { provenance: 'unknown' };
  try {
    const content = await readFile(absolutePath, 'utf-8');
    const lines = content.split('\n');
    const start = Math.max(0, lineNumber - 3);
    const end = Math.min(lines.length, lineNumber + 2);
    const excerpt = lines
      .slice(start, end)
      .map((line, index) => `${start + index + 1}: ${line}`)
      .join('\n')
      .trim();
    return excerpt ? { excerpt, provenance: 'current_checkout' } : { provenance: 'unknown' };
  } catch {
    return { provenance: 'unknown' };
  }
}

function buildCodeContextExcerpt(content: string, lineNumber: number): string | undefined {
  if (lineNumber <= 0) return undefined;
  const lines = content.split('\n');
  const start = Math.max(0, lineNumber - 3);
  const end = Math.min(lines.length, lineNumber + 2);
  const excerpt = lines
    .slice(start, end)
    .map((line, index) => `${start + index + 1}: ${line}`)
    .join('\n')
    .trim();
  return excerpt || undefined;
}

async function readGithubFileContentAtRef(
  repoSlug: string,
  path: string,
  ref: string,
  context: GithubApiContext,
): Promise<string | undefined> {
  try {
    const payload = await githubApiJson<GithubContentPayload>(
      `/repos/${repoSlug}/contents/${path.split('/').map((segment) => encodeURIComponent(segment)).join('/')}?ref=${encodeURIComponent(ref)}`,
      context,
    );
    if (!payload.content || payload.encoding !== 'base64') return undefined;
    return Buffer.from(payload.content.replace(/\n/g, ''), 'base64').toString('utf-8');
  } catch {
    return undefined;
  }
}

function classifyReviewRuleTemplate(text: string | null | undefined): { template: (typeof REVIEW_RULE_LIBRARY)[number]; score: number } | null {
  const normalized = normalizeReviewRuleEvidenceText(text);
  if (!normalized) return null;
  let best: { template: (typeof REVIEW_RULE_LIBRARY)[number]; score: number } | null = null;
  for (const template of REVIEW_RULE_LIBRARY) {
    const score = template.patterns.reduce((sum, pattern) => sum + (pattern.test(normalized) ? 1 : 0), 0);
    if (score === 0) continue;
    if (!best || score > best.score) best = { template, score };
  }
  return best;
}

function buildReviewRuleId(category: GithubReviewRuleCategory, rule: string): string {
  return `${category}-${createHash('sha1').update(`${category}\n${rule}`).digest('hex').slice(0, 10)}`;
}

function deriveReviewRulePathScopes(evidence: readonly GithubRepoReviewRuleEvidence[]): string[] {
  const scopes = new Set<string>();
  for (const item of evidence) {
    if (!item.path) continue;
    const normalized = item.path.replace(/\\/g, '/');
    const slashIndex = normalized.lastIndexOf('/');
    scopes.add(slashIndex > 0 ? normalized.slice(0, slashIndex) : normalized);
    if (scopes.size >= 5) break;
  }
  return [...scopes].sort();
}

function reviewerPolicyAllowsEvidence(
  evidence: GithubRepoReviewRuleEvidence,
  policy: GithubReviewRulesReviewerPolicy | undefined,
): boolean {
  const reviewer = normalizeLogin(evidence.reviewer);
  const blocked = new Set((policy?.blocked_reviewers ?? []).map((value) => normalizeLogin(value)).filter((value): value is string => Boolean(value)));
  if (reviewer && blocked.has(reviewer)) return false;
  const trusted = [...new Set((policy?.trusted_reviewers ?? []).map((value) => normalizeLogin(value)).filter((value): value is string => Boolean(value)))];
  if (trusted.length === 0) return true;
  return Boolean(reviewer && trusted.includes(reviewer));
}

function reviewerPolicyReason(policy: GithubReviewRulesReviewerPolicy | undefined): string | undefined {
  if (!policy) return undefined;
  const parts: string[] = [];
  if ((policy.trusted_reviewers ?? []).length > 0) parts.push(`trusted reviewers=${policy.trusted_reviewers!.join(',')}`);
  if ((policy.blocked_reviewers ?? []).length > 0) parts.push(`blocked reviewers=${policy.blocked_reviewers!.join(',')}`);
  if (policy.min_distinct_reviewers) parts.push(`min distinct reviewers=${policy.min_distinct_reviewers}`);
  return parts.length > 0 ? parts.join('; ') : undefined;
}

function deriveReviewRuleExtractionOrigin(
  evidence: readonly GithubRepoReviewRuleEvidence[],
): 'reviews' | 'review_comments' | 'mixed' {
  const kinds = new Set(evidence.map((item) => item.kind));
  if (kinds.size === 1) return kinds.has('review') ? 'reviews' : 'review_comments';
  return 'mixed';
}

function buildReviewRuleExtractionReason(
  evidence: readonly GithubRepoReviewRuleEvidence[],
  origin: 'reviews' | 'review_comments' | 'mixed',
): string {
  const prCount = new Set(evidence.map((item) => item.pr_number)).size;
  const pathCount = new Set(evidence.map((item) => item.path).filter(Boolean)).size;
  const reviewCount = evidence.filter((item) => item.kind === 'review').length;
  const commentCount = evidence.filter((item) => item.kind === 'review_comment').length;
  const originLabel = origin === 'mixed' ? `mixed reviews/comments (${reviewCount} reviews, ${commentCount} comments)` : origin.replace(/_/g, ' ');
  return `Repeated ${originLabel} across ${prCount} PR${prCount === 1 ? '' : 's'}${pathCount > 0 ? ` and ${pathCount} path scope${pathCount === 1 ? '' : 's'}` : ''}.`;
}

function buildReviewRuleCandidates(
  signals: readonly GithubReviewRuleSignal[],
  now: Date,
  reviewerPolicy: GithubReviewRulesReviewerPolicy | undefined,
): GithubRepoReviewRule[] {
  const byTemplate = new Map<string, GithubReviewRuleSignal[]>();
  for (const signal of signals.filter((candidate) => reviewerPolicyAllowsEvidence(candidate.evidence, reviewerPolicy))) {
    const key = `${signal.template.category}\n${signal.template.rule}`;
    const bucket = byTemplate.get(key);
    if (bucket) bucket.push(signal);
    else byTemplate.set(key, [signal]);
  }

  const candidates: GithubRepoReviewRule[] = [];
  for (const bucket of byTemplate.values()) {
    const template = bucket[0]!.template;
    const evidence = bucket.map((signal) => signal.evidence);
    if (evidence.length < 2) continue;
    const reviewerCount = new Set(evidence.map((item) => normalizeLogin(item.reviewer)).filter((value): value is string => Boolean(value))).size;
    const minDistinctReviewers = reviewerPolicy?.min_distinct_reviewers ?? 1;
    if (reviewerCount < minDistinctReviewers) continue;
    const id = buildReviewRuleId(template.category, template.rule);
    const confidence = Math.min(0.95, 0.55 + evidence.length * 0.12);
    const extractionOrigin = deriveReviewRuleExtractionOrigin(evidence);
    candidates.push({
      id,
      title: template.title,
      rule: template.rule,
      category: template.category,
      confidence: Number(confidence.toFixed(2)),
      reviewer_count: reviewerCount,
      applicable_roles: [...(REVIEW_RULE_ROLE_MAP[template.category] ?? [])],
      path_scopes: deriveReviewRulePathScopes(evidence),
      extraction_origin: extractionOrigin,
      extraction_reason: [
        buildReviewRuleExtractionReason(evidence, extractionOrigin),
        reviewerPolicyReason(reviewerPolicy),
      ].filter(Boolean).join(' '),
      evidence,
      source: 'review-scan',
      created_at: now.toISOString(),
      updated_at: now.toISOString(),
    });
  }

  return candidates.sort((left, right) => {
    if (right.confidence !== left.confidence) return right.confidence - left.confidence;
    return left.title.localeCompare(right.title);
  });
}

function selectRepoReviewRuleSignals(
  reviews: Array<{ prNumber: number; review: GithubPullReviewPayload }>,
  reviewComments: Array<{ prNumber: number; comment: GithubPullReviewCommentPayload }>,
): GithubReviewRuleSignal[] {
  const signals: GithubReviewRuleSignal[] = [];

  for (const { prNumber, review } of reviews) {
    const classified = classifyReviewRuleTemplate(review.body);
    if (!classified) continue;
    signals.push({
      template: classified.template,
      evidence: {
        kind: 'review',
        pr_number: prNumber,
        review_id: review.id,
        reviewer: review.user?.login,
        state: review.state,
        html_url: review.html_url,
        excerpt: buildReviewRuleExcerpt(review.body),
      },
    });
  }

  for (const { prNumber, comment } of reviewComments) {
    const classified = classifyReviewRuleTemplate(comment.body);
    if (!classified) continue;
    signals.push({
      template: classified.template,
      evidence: {
        kind: 'review_comment',
        pr_number: prNumber,
        review_comment_id: comment.id,
        review_id: comment.pull_request_review_id ?? undefined,
        reviewer: comment.user?.login,
        path: comment.path,
        line: comment.line ?? comment.original_line ?? undefined,
        diff_hunk: comment.diff_hunk ?? undefined,
        html_url: comment.html_url,
        excerpt: buildReviewRuleExcerpt(comment.body),
      },
    });
  }

  return signals;
}

async function enrichReviewRuleSignalsWithCodeContext(
  sourcePath: string,
  signals: GithubReviewRuleSignal[],
  repoSlug: string,
  prHeadShas: Readonly<Record<number, string>>,
  context: GithubApiContext,
): Promise<GithubReviewRuleSignal[]> {
  const enriched: GithubReviewRuleSignal[] = [];
  for (const signal of signals) {
    if (signal.evidence.kind !== 'review_comment') {
      enriched.push(signal);
      continue;
    }
    const lineNumber = signal.evidence.line;
    const path = signal.evidence.path;
    const prHeadSha = prHeadShas[signal.evidence.pr_number];
    let codeContext: string | undefined;
    let provenance: GithubRepoReviewRuleEvidence['code_context_provenance'] = 'unknown';
    let ref: string | undefined;
    if (path && lineNumber != null && prHeadSha) {
      const prHeadContent = await readGithubFileContentAtRef(repoSlug, path, prHeadSha, context);
      const excerpt = prHeadContent ? buildCodeContextExcerpt(prHeadContent, lineNumber) : undefined;
      if (excerpt) {
        codeContext = excerpt;
        provenance = 'pr_head_sha';
        ref = prHeadSha;
      }
    }
    if (!codeContext) {
      const fallback = await readReviewCommentCodeContext(sourcePath, {
        path: path ?? '',
        line: lineNumber,
        original_line: lineNumber,
      });
      codeContext = fallback.excerpt;
      provenance = fallback.provenance;
    }
    enriched.push(codeContext
      ? {
        ...signal,
        evidence: {
          ...signal.evidence,
          code_context_excerpt: codeContext,
          code_context_provenance: provenance,
          code_context_ref: ref,
        },
      }
      : {
        ...signal,
        evidence: {
          ...signal.evidence,
          code_context_provenance: 'unknown',
        },
      });
  }
  return enriched;
}

function selectApplicableRepoReviewRules(
  doc: GithubRepoReviewRulesDocument | null,
  input: {
    lane?: Pick<GithubPipelineLane, 'alias' | 'role' | 'prompt_roles'>;
    manifest?: Pick<GithubWorkonManifest, 'considerations_active'>;
  } = {},
): GithubRepoReviewRule[] {
  const approved = doc?.approved_rules ?? [];
  if (approved.length === 0) return [];

  const laneRoles = input.lane
    ? new Set<string>([input.lane.alias, input.lane.role, ...(input.lane.prompt_roles ?? [])])
    : null;
  const activeConsiderations = new Set(input.manifest?.considerations_active ?? []);

  const scored = approved.map((rule) => {
    let score = rule.confidence;
    if (laneRoles && rule.applicable_roles.some((role) => laneRoles.has(role))) score += 1;
    if (!laneRoles && activeConsiderations.has(rule.category as GithubConsideration)) score += 0.5;
    if (!laneRoles && rule.applicable_roles.includes('executor')) score += 0.25;
    return { rule, score };
  });

  return scored
    .filter(({ rule }) => !laneRoles || rule.applicable_roles.some((role) => laneRoles.has(role)))
    .sort((left, right) => right.score - left.score || left.rule.title.localeCompare(right.rule.title))
    .map(({ rule }) => rule);
}

function buildRepoReviewRuleInstructionLines(
  manifest: Pick<GithubWorkonManifest, 'source_path' | 'considerations_active'>,
  lane?: Pick<GithubPipelineLane, 'alias' | 'role' | 'prompt_roles'>,
): string[] {
  const doc = readRepoReviewRulesDocumentSync(manifest.source_path);
  const rules = selectApplicableRepoReviewRules(doc, { lane, manifest }).slice(0, lane ? 4 : 6);
  if (rules.length === 0) return [];
  const lines = ['## Approved repo review rules', ''];
  for (const rule of rules) {
    const scopeSuffix = rule.path_scopes.length > 0 ? ` [scopes: ${rule.path_scopes.join(', ')}]` : '';
    lines.push(`- [${rule.category}] ${rule.rule}${scopeSuffix}`);
  }
  lines.push('');
  return lines;
}

async function buildLaneRepoReviewRuleInstructionLines(
  manifest: Pick<GithubWorkonManifest, 'source_path' | 'considerations_active'>,
  lane: Pick<GithubPipelineLane, 'alias' | 'role' | 'prompt_roles'>,
): Promise<string[]> {
  const doc = readRepoReviewRulesDocumentSync(manifest.source_path);
  const candidateRules = selectApplicableRepoReviewRules(doc, { lane, manifest });
  if (candidateRules.length === 0) return [];

  const registryDetails = await resolveGithubConcernRegistryDetails(manifest.source_path).catch(() => null);
  const laneDescriptor = registryDetails
    ? resolveLaneConcernDescriptor(lane.alias, registryDetails.registry)
    : undefined;

  const scopedRules = candidateRules.filter((rule) => {
    const evidencePaths = [...new Set(rule.evidence.map((evidence) => evidence.path).filter((value): value is string => Boolean(value)))];
    if (evidencePaths.length === 0) return true;
    if (!registryDetails || !laneDescriptor) return true;
    const concernMatch = resolveConcernMatchForFiles({
      descriptor: laneDescriptor,
      registry: registryDetails.registry,
      descriptorSource: registryDetails.descriptor_sources[lane.alias]?.source,
      overridePath: registryDetails.descriptor_sources[lane.alias]?.path,
      changedFiles: evidencePaths,
    });
    return concernMatch.matched_files.length > 0;
  }).slice(0, 4);

  if (scopedRules.length === 0) return [];
  const lines = ['## Approved repo review rules', ''];
  for (const rule of scopedRules) {
    const scopeSuffix = rule.path_scopes.length > 0 ? ` [scopes: ${rule.path_scopes.join(', ')}]` : '';
    lines.push(`- [${rule.category}] ${rule.rule}${scopeSuffix}`);
  }
  lines.push('');
  return lines;
}

function buildStartInstructions(
  manifest: GithubWorkonManifest,
  target: GithubTargetContext,
  reviewer: string,
  feedback: GithubFeedbackSnapshot,
): string {
  const lines = [
    '<github_workon_mode>',
    'You are in NANA GitHub workon mode.',
    `Repository target: ${manifest.repo_slug}`,
    `Managed sandbox path: ${manifest.sandbox_path}`,
    `Managed repo checkout path: ${manifest.sandbox_repo_path}`,
    `Managed sandbox id: ${manifest.sandbox_id}`,
    `Managed repo root: ${manifest.managed_repo_root}`,
    `GitHub target: ${manifest.target_kind} #${manifest.target_number}`,
    `GitHub URL: ${manifest.target_url}`,
    `Tracked reviewer for follow-up sync: @${reviewer}`,
    'Use the managed sandbox checkout as the editable source of truth.',
    manifest.verification_scripts_dir
      ? `Verification scripts directory: ${manifest.verification_scripts_dir}`
      : '',
    'Use the GitHub issue/PR context below as the external task definition and follow-up review source.',
    'If a later `nana work-on sync` run appends new reviewer feedback, treat that as an update to this task thread and continue implementation.',
    ...buildRepoReviewRuleInstructionLines(manifest),
    ...buildConsiderationInstructionLines(manifest),
    ...buildCiPolicyLines(manifest),
    manifest.target_kind === 'issue'
      ? manifest.create_pr_on_complete
        ? `When implementation and verification are complete, do not stop to ask whether to push or open a PR. Push the current sandbox branch and open a draft PR targeting \`${manifest.default_branch}\` automatically. If a PR for the current branch already exists, update/push it instead of creating a duplicate. Include the canonical issue URL \`${manifest.target_url}\` in the draft PR body so NANA can link the issue sandbox and PR sandbox later.`
        : 'Default completion mode is local-only. Do not push or open a PR automatically. Keep the work local in the sandbox unless the user explicitly asks to publish it.'
      : 'This run is attached to an existing PR context. Do not create a second PR unless the user explicitly asks for one.',
    '</github_workon_mode>',
    '',
    '# GitHub target context',
    '',
    `- Repo: ${manifest.repo_slug}`,
    `- Target: ${manifest.target_kind} #${manifest.target_number}`,
    `- URL: ${manifest.target_url}`,
    `- State: ${manifest.target_state}`,
    `- Title: ${manifest.target_title}`,
    `- Default branch: ${manifest.default_branch}`,
    target.issue.user?.login ? `- Author: @${target.issue.user.login}` : '- Author: unknown',
    '',
    ...formatMarkdownBlock('Target body', target.issue.body),
  ];

  if (target.pullRequest) {
    lines.push('## Pull request refs', '');
    lines.push(`- Head: ${target.pullRequest.head.repo?.full_name ?? manifest.repo_slug}:${target.pullRequest.head.ref} (${target.pullRequest.head.sha})`);
    lines.push(`- Base: ${target.pullRequest.base.repo?.full_name ?? manifest.repo_slug}:${target.pullRequest.base.ref} (${target.pullRequest.base.sha})`);
    lines.push('');
  }

  if (manifest.verification_plan) {
    lines.push('## Verification Plan', '');
    lines.push(`- Source: ${manifest.verification_plan.source}`);
    lines.push(`- Plan fingerprint: ${manifest.verification_plan.plan_fingerprint}`);
    lines.push(`- Lint: ${manifest.verification_plan.lint.join(' && ') || '(none detected)'}`);
    lines.push(`- Compile: ${manifest.verification_plan.compile.join(' && ') || '(none detected)'}`);
    lines.push(`- Unit tests: ${manifest.verification_plan.unit.join(' && ') || '(none detected)'}`);
    lines.push(`- Integration tests: ${manifest.verification_plan.integration.join(' && ') || '(none detected)'}`);
    if (manifest.verification_plan.source_files.length > 0) {
      lines.push('- Tracked verification source files:');
      for (const sourceFile of manifest.verification_plan.source_files) {
        lines.push(`  - ${sourceFile.path} [${sourceFile.kind}] sha256=${sourceFile.checksum}`);
      }
    }
    if (manifest.verification_scripts_dir) {
      lines.push(`- Unit-test policy file: ${sandboxVerificationPolicyPath(manifest.sandbox_path)}`);
      lines.push(`- Integration-test policy file: ${sandboxVerificationIntegrationPolicyPath(manifest.sandbox_path)}`);
      lines.push(`- Drift log: ${sandboxVerificationDriftLogPath(manifest.sandbox_path)}`);
      lines.push(`- Verification refresh gate: ${join(manifest.verification_scripts_dir, 'refresh.sh')}`);
      lines.push(`- Worker completion gate: ${join(manifest.verification_scripts_dir, 'worker-done.sh')}`);
      lines.push(`- Final verification gate: ${join(manifest.verification_scripts_dir, 'all.sh')}`);
      lines.push('- The refresh gate recalculates tracked checksums and regenerates sandbox-local verification scripts when CI/Makefile source files drift.');
      lines.push('- After each worker or lane reports completion, run the worker completion gate script before accepting the handoff.');
      lines.push('- The worker completion gate always runs lint and compile. It only runs unit/integration suites whose mode is `every-iteration` or `unknown`.');
      lines.push('- The final verification gate runs lint and compile plus any test suites whose mode is not `ci-only`.');
      lines.push('- In PR mode, suites longer than the CI-only threshold stay CI-owned and are verified by waiting for GitHub Actions to complete and then fixing any failures that appear.');
    }
    lines.push('');
  }

  lines.push(renderFeedbackMarkdown(feedback, reviewer).trim(), '');
  return `${lines.join('\n').trim()}\n`;
}

function buildFeedbackInstructions(
  manifest: GithubWorkonManifest,
  reviewer: string,
  feedback: GithubFeedbackSnapshot,
): string {
  return [
    '<github_workon_feedback>',
    'You are continuing an NANA GitHub workon session after new GitHub feedback arrived.',
    `Repository target: ${manifest.repo_slug}`,
    `Managed sandbox path: ${manifest.sandbox_path}`,
    `Managed repo checkout path: ${manifest.sandbox_repo_path}`,
    `Managed sandbox id: ${manifest.sandbox_id}`,
    `GitHub target: ${manifest.target_kind} #${manifest.target_number}`,
    `GitHub URL: ${manifest.target_url}`,
    `Reviewer: @${reviewer}`,
    'Treat the feedback below as new external review input and continue implementation plus verification accordingly.',
    manifest.verification_scripts_dir
      ? `Use ${join(manifest.verification_scripts_dir, 'refresh.sh')} to refresh verification scripts when tracked source files drift, ${join(manifest.verification_scripts_dir, 'worker-done.sh')} after each completed lane handoff, and ${join(manifest.verification_scripts_dir, 'all.sh')} before publication. The current unit/integration execution policy is published in ${sandboxVerificationPolicyPath(manifest.sandbox_path)} and ${sandboxVerificationIntegrationPolicyPath(manifest.sandbox_path)}; drift events are logged in ${sandboxVerificationDriftLogPath(manifest.sandbox_path)}.`
      : '',
    ...buildRepoReviewRuleInstructionLines(manifest),
    ...buildConsiderationInstructionLines(manifest),
    ...buildCiPolicyLines(manifest),
    manifest.target_kind === 'issue'
      ? manifest.create_pr_on_complete
        ? `Do not stop to ask about pushing or opening a PR. Once the feedback is incorporated and verification is green, push the current sandbox branch and open or update a draft PR targeting \`${manifest.default_branch}\`.`
        : 'Keep the work local in the sandbox. Do not push or open a PR unless the user explicitly requests publication.'
      : 'Continue updating the existing PR context; do not open a new PR unless explicitly asked.',
    '</github_workon_feedback>',
    '',
    renderFeedbackMarkdown(feedback, reviewer).trim(),
    '',
  ].join('\n');
}

async function ensureManagedRepoMetadata(
  paths: ManagedRepoPaths,
  target: GithubTargetContext,
  now: Date,
): Promise<ManagedRepoMetadata> {
  const existing = await readJsonFile<ManagedRepoMetadata>(paths.repoMetaPath);
  if (existing && normalizeRepoSlugForCompare(existing.repo_slug) !== normalizeRepoSlugForCompare(target.repository.full_name)) {
    throw new Error(
      `Managed repo path collision at ${paths.repoRoot}: expected ${target.repository.full_name}, found ${existing.repo_slug}.`,
    );
  }

  const metadata: ManagedRepoMetadata = {
    version: 1,
    repo_name: target.repository.name,
    repo_slug: target.repository.full_name,
    repo_owner: target.repository.full_name.split('/')[0] || '',
    clone_url: target.repository.clone_url,
    default_branch: target.repository.default_branch,
    html_url: target.repository.html_url,
    repo_root: paths.repoRoot,
    source_path: paths.sourcePath,
    updated_at: now.toISOString(),
  };
  await writeJsonFile(paths.repoMetaPath, metadata);
  return metadata;
}

async function ensureManagedRepoMetadataFromRepository(
  paths: ManagedRepoPaths,
  repository: GithubRepositoryPayload,
  now: Date,
): Promise<ManagedRepoMetadata> {
  const existing = await readJsonFile<ManagedRepoMetadata>(paths.repoMetaPath);
  if (existing && normalizeRepoSlugForCompare(existing.repo_slug) !== normalizeRepoSlugForCompare(repository.full_name)) {
    throw new Error(
      `Managed repo path collision at ${paths.repoRoot}: expected ${repository.full_name}, found ${existing.repo_slug}.`,
    );
  }

  const metadata: ManagedRepoMetadata = {
    version: 1,
    repo_name: repository.name,
    repo_slug: repository.full_name,
    repo_owner: repository.full_name.split('/')[0] || '',
    clone_url: repository.clone_url,
    default_branch: repository.default_branch,
    html_url: repository.html_url,
    repo_root: paths.repoRoot,
    source_path: paths.sourcePath,
    updated_at: now.toISOString(),
  };
  await writeJsonFile(paths.repoMetaPath, metadata);
  return metadata;
}

function runVerificationScriptIfPresent(
  manifest: GithubWorkonManifest,
  scriptName: 'all.sh' | 'worker-done.sh',
  env: NodeJS.ProcessEnv = process.env,
): void {
  if (!manifest.verification_scripts_dir) return;
  const scriptPath = join(manifest.verification_scripts_dir, scriptName);
  if (!existsSync(scriptPath)) return;
  execFileSync(scriptPath, {
    cwd: manifest.sandbox_path,
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
    encoding: 'utf-8',
  });
}

function ensureSourceClone(paths: ManagedRepoPaths, repoMeta: ManagedRepoMetadata): void {
  if (!existsSync(paths.sourcePath)) {
    execFileSync('git', ['clone', repoMeta.clone_url, paths.sourcePath], {
      encoding: 'utf-8',
      stdio: ['ignore', 'pipe', 'pipe'],
    });
  } else {
    const currentOrigin = gitExecAllowFailure(paths.sourcePath, ['remote', 'get-url', 'origin']);
    if (!currentOrigin.ok || currentOrigin.stdout.trim() !== repoMeta.clone_url.trim()) {
      gitExec(paths.sourcePath, ['remote', 'set-url', 'origin', repoMeta.clone_url]);
    }
  }

  gitExec(paths.sourcePath, ['fetch', '--prune', 'origin']);
}

function targetBaseRef(target: ParsedGithubTargetUrl, repoMeta: ManagedRepoMetadata): string {
  if (target.targetKind === 'issue') return `origin/${repoMeta.default_branch}`;
  return `refs/remotes/origin/nana-pr/${target.targetNumber}`;
}

function fetchTargetBaseRef(paths: ManagedRepoPaths, repoMeta: ManagedRepoMetadata, target: ParsedGithubTargetUrl): string {
  if (target.targetKind === 'issue') {
    gitExec(paths.sourcePath, ['fetch', '--force', 'origin', repoMeta.default_branch]);
    return targetBaseRef(target, repoMeta);
  }

  const prRef = targetBaseRef(target, repoMeta);
  gitExec(paths.sourcePath, ['fetch', '--force', 'origin', `pull/${target.targetNumber}/head:${prRef}`]);
  return prRef;
}

async function removeExistingSandboxWorktree(paths: ManagedRepoPaths, sandboxPath: string): Promise<void> {
  gitExecAllowFailure(paths.sourcePath, ['worktree', 'remove', '--force', sandboxPath]);
  await rm(sandboxPath, { recursive: true, force: true });
}

async function writeSandboxMetadata(sandboxPath: string, metadata: SandboxMetadata): Promise<void> {
  await writeJsonFile(sandboxMetadataPath(sandboxPath), metadata);
}

async function readSandboxMetadata(sandboxPath: string): Promise<SandboxMetadata | null> {
  return readJsonFile<SandboxMetadata>(sandboxMetadataPath(sandboxPath));
}

async function findLatestIssueSandbox(
  paths: ManagedRepoPaths,
  issueNumber: number,
): Promise<{ sandboxId: string; sandboxPath: string; metadata: SandboxMetadata } | null> {
  if (!existsSync(paths.sandboxesDir)) return null;
  const entries = await readdir(paths.sandboxesDir, { withFileTypes: true });
  let best: { sandboxId: string; sandboxPath: string; metadata: SandboxMetadata } | null = null;

  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const sandboxPath = join(paths.sandboxesDir, entry.name);
    const metadata = await readSandboxMetadata(sandboxPath);
    if (!metadata) continue;
    if (metadata.target_kind !== 'issue' || metadata.target_number !== issueNumber) continue;
    if (!best || Date.parse(metadata.updated_at) > Date.parse(best.metadata.updated_at)) {
      best = { sandboxId: metadata.sandbox_id, sandboxPath, metadata };
    }
  }

  return best;
}

function resolveAbsoluteGitDir(repoCheckoutPath: string): string {
  return gitExec(repoCheckoutPath, ['rev-parse', '--absolute-git-dir']);
}

async function ensureSymlink(linkPath: string, targetPath: string): Promise<void> {
  await mkdir(dirname(linkPath), { recursive: true });
  const desired = relative(dirname(linkPath), targetPath);

  try {
    const stat = await lstat(linkPath);
    if (stat.isSymbolicLink()) {
      const current = await readlink(linkPath);
      if (current === desired) return;
    }
    await unlink(linkPath);
  } catch {
    // create below
  }

  await symlink(desired, linkPath, 'dir');
}

async function ensurePathSymlink(linkPath: string, targetPath: string): Promise<void> {
  await mkdir(dirname(linkPath), { recursive: true });
  const desired = relative(dirname(linkPath), targetPath);

  try {
    const stat = await lstat(linkPath);
    if (stat.isSymbolicLink()) {
      const current = await readlink(linkPath);
      if (current === desired) return;
    }
    await unlink(linkPath);
  } catch {
    // create below
  }

  await symlink(desired, linkPath);
}

function buildCodexConfigForProfile(
  sourceConfigText: string,
  profile: GithubCodexProfile,
  trustedPaths: readonly string[],
): string {
  const parsed = sourceConfigText.trim()
    ? TOML.parse(sourceConfigText) as Record<string, any>
    : {};
  const next: Record<string, any> = {};
  const topLevelKeys = [
    'notify',
    'model_reasoning_effort',
    'developer_instructions',
    'model_context_window',
    'model_auto_compact_token_limit',
    'model',
    'personality',
    'service_tier',
    'approval_policy',
    'sandbox_mode',
  ] as const;
  for (const key of topLevelKeys) {
    if (parsed[key] !== undefined) next[key] = parsed[key];
  }
  if (parsed.notice) next.notice = parsed.notice;
  next.features = {
    ...(typeof parsed.features === 'object' && parsed.features ? parsed.features : {}),
    multi_agent: profile.enableMultiAgent,
    child_agents_md: profile.enableChildAgentsMd,
  };
  if (parsed.env) next.env = parsed.env;
  if (parsed.agents && profile.enableMultiAgent) next.agents = parsed.agents;

  const mcpServers = typeof parsed.mcp_servers === 'object' && parsed.mcp_servers ? parsed.mcp_servers : {};
  const filteredServers: Record<string, any> = {};
  for (const name of profile.allowedMcpServers) {
    if (mcpServers[name] !== undefined) filteredServers[name] = mcpServers[name];
  }
  next.mcp_servers = filteredServers;

  const projects = typeof parsed.projects === 'object' && parsed.projects ? parsed.projects : {};
  const trustedProjects: Record<string, any> = {};
  for (const path of trustedPaths) {
    const existing = projects[path];
    trustedProjects[path] = {
      ...(typeof existing === 'object' && existing ? existing : {}),
      trust_level: 'trusted',
    };
  }
  next.projects = trustedProjects;

  return TOML.stringify(next).trim() + '\n';
}

async function ensureSandboxCodexHome(
  sandboxPath: string,
  sourceCodexHome: string,
  profile: GithubCodexProfile,
  trustedPaths: readonly string[],
  profileKey: string = profile.name,
): Promise<string> {
  const codexHome = profileKey === 'leader'
    ? sandboxCodexHomePath(sandboxPath)
    : sandboxAgentCodexHomePath(sandboxPath, profileKey);
  await mkdir(codexHome, { recursive: true });

  if (existsSync(sourceCodexHome)) {
    for (const entry of SANDBOX_CODEX_HOME_SEED_ENTRIES) {
      if (entry === 'config.toml') continue;
      const sourcePath = join(sourceCodexHome, entry);
      const destinationPath = join(codexHome, entry);
      if (!existsSync(sourcePath) || existsSync(destinationPath)) continue;
      await ensurePathSymlink(destinationPath, sourcePath);
    }
  }

  const sourceConfigPath = join(sourceCodexHome, 'config.toml');
  const sourceConfigText = await readFile(sourceConfigPath, 'utf-8').catch(() => '');
  await writeFile(
    join(codexHome, 'config.toml'),
    buildCodexConfigForProfile(sourceConfigText, profile, trustedPaths),
    'utf-8',
  );
  return codexHome;
}

function resolveLaneCodexProfile(lane: GithubPipelineLane): GithubCodexProfile {
  return GITHUB_CODEX_PROFILES[resolveLaneCodexProfileName(lane)];
}

function publisherLane(manifest: GithubWorkonManifest): GithubPipelineLane {
  return {
    alias: 'publisher',
    role: 'publisher',
    prompt_roles: [],
    activation: 'hardening',
    phase: 'final-gate',
    mode: 'execute',
    owner: 'self',
    blocking: true,
    purpose: `Publish ${manifest.repo_slug} ${manifest.target_kind} #${manifest.target_number}: run final verification, push, create or update the draft PR, and keep iterating until CI is green.`,
  };
}

function buildLaneDependencies(
  manifest: GithubWorkonManifest,
  lane: GithubPipelineLane,
): string[] {
  if (lane.alias === 'coder' || lane.alias === 'architect') return [];
  if (lane.alias === 'perf-coder') return ['leader.implementation_started'];
  return lane.alias === 'publisher'
    ? ['leader.ready_for_publication', ...manifest.consideration_pipeline.filter((candidate) => candidate.blocking && candidate.alias !== 'coder').map((candidate) => `lane:${candidate.alias}`)]
    : ['leader.bootstrap_complete'];
}

function createPendingLaneRuntimeState(
  manifest: GithubWorkonManifest,
  runPaths: GithubRunPaths,
  lane: GithubPipelineLane,
  now: Date,
): GithubLaneRuntimeState {
  const profile = lane.alias === 'publisher' ? GITHUB_CODEX_PROFILES.publisher : resolveLaneCodexProfile(lane);
  const retryPolicy = resolveLaneRetryPolicy({
    version: 1,
    lane_id: `${manifest.run_id}:${lane.alias}`,
    alias: lane.alias,
    role: lane.role,
    profile: profile.name,
    activation: lane.alias === 'publisher' ? 'publication' : lane.activation,
    phase: lane.alias === 'publisher' ? 'publication' : lane.phase,
    blocking: lane.blocking,
    depends_on: buildLaneDependencies(manifest, lane),
    status: 'pending',
    updated_at: now.toISOString(),
    instructions_path: lane.alias === 'publisher' ? publisherInboxPath(runPaths) : laneInstructionsPath(runPaths, lane.alias),
    result_path: laneResultPath(runPaths, lane.alias),
    stdout_path: laneStdoutPath(runPaths, lane.alias),
    stderr_path: laneStderrPath(runPaths, lane.alias),
  }, manifest.publication_state);
  return {
    version: 1,
    lane_id: `${manifest.run_id}:${lane.alias}`,
    alias: lane.alias,
    role: lane.role,
    profile: profile.name,
    activation: lane.alias === 'publisher' ? 'publication' : lane.activation,
    phase: lane.alias === 'publisher' ? 'publication' : lane.phase,
    blocking: lane.blocking,
    depends_on: buildLaneDependencies(manifest, lane),
    status: 'pending',
    retry_count: 0,
    retryable: lane.alias === 'publisher' || lane.mode === 'execute',
    retry_policy: retryPolicy.name,
    retry_exhausted: retryPolicy.max_retries === 0,
    updated_at: now.toISOString(),
    instructions_path: lane.alias === 'publisher' ? publisherInboxPath(runPaths) : laneInstructionsPath(runPaths, lane.alias),
    result_path: laneResultPath(runPaths, lane.alias),
    stdout_path: laneStdoutPath(runPaths, lane.alias),
    stderr_path: laneStderrPath(runPaths, lane.alias),
  };
}

async function initializeLaneRuntimeArtifacts(
  manifest: GithubWorkonManifest,
  runPaths: GithubRunPaths,
  now: Date,
): Promise<void> {
  await mkdir(laneRuntimeDir(runPaths), { recursive: true });
  const lanes = [...manifest.consideration_pipeline, publisherLane(manifest)];
  await Promise.all(lanes.map(async (lane) => {
    const statePath = laneStatePath(runPaths, lane.alias);
    if (existsSync(statePath)) return;
    await writeLaneRuntimeState(runPaths, createPendingLaneRuntimeState(manifest, runPaths, lane, now));
  }));
  if (!existsSync(leaderInboxPath(runPaths))) await writeFile(leaderInboxPath(runPaths), '# Leader Inbox\n\n', 'utf-8');
  if (!existsSync(publisherInboxPath(runPaths))) await writeFile(publisherInboxPath(runPaths), '# Publisher Inbox\n\n', 'utf-8');
  if (!existsSync(leaderInboxCursorPath(runPaths))) {
    const content = await readFile(leaderInboxPath(runPaths), 'utf-8').catch(() => '');
    await advanceInboxCursor(leaderInboxCursorPath(runPaths), Buffer.byteLength(content));
  }
  if (!existsSync(publisherInboxCursorPath(runPaths))) {
    const content = await readFile(publisherInboxPath(runPaths), 'utf-8').catch(() => '');
    await advanceInboxCursor(publisherInboxCursorPath(runPaths), Buffer.byteLength(content));
  }
  if (!existsSync(leaderStatusPath(runPaths))) {
    await writeLeaderStatus(runPaths, {
      run_id: manifest.run_id,
      session_active: false,
      bootstrap_complete: false,
      implementation_started: false,
      implementation_complete: false,
      ready_for_publication: false,
      blocked: false,
      updated_at: now.toISOString(),
    });
  }
  if (!existsSync(publisherStatusPath(runPaths))) {
    await writePublisherStatus(runPaths, {
      run_id: manifest.run_id,
      session_active: false,
      started: false,
      pr_opened: false,
      ci_waiting: false,
      ci_green: false,
      blocked: false,
      recovery_count: 0,
      retry_count: 0,
      milestones: [],
      updated_at: now.toISOString(),
    });
  }
  if (!existsSync(schedulerStatusPath(runPaths))) {
    await writeSchedulerStatus(runPaths, {
      version: 1,
      run_id: manifest.run_id,
      last_processed_event_id: 0,
      pass_count: 0,
      startup_pass_count: 0,
      watch_pass_count: 0,
      poll_pass_count: 0,
      watch_mode: 'poll-only',
      replay_count: 0,
      recovery_count: 0,
      publisher_recovery_count: 0,
      last_pass_at: now.toISOString(),
    });
  }
}

async function loadLaneRuntimeStates(
  manifest: GithubWorkonManifest,
  runPaths: GithubRunPaths,
): Promise<Map<string, GithubLaneRuntimeState>> {
  const lanes = [...manifest.consideration_pipeline, publisherLane(manifest)];
  const entries = await Promise.all(lanes.map(async (lane) => [lane.alias, await readLaneRuntimeState(runPaths, lane.alias)] as const));
  return new Map(entries.filter((entry): entry is readonly [string, GithubLaneRuntimeState] => !!entry[1]));
}

async function syncLeaderOwnedLaneStates(
  manifest: GithubWorkonManifest,
  runPaths: GithubRunPaths,
  leaderStatus: GithubLeaderRuntimeStatus,
): Promise<void> {
  const coderLane = manifest.consideration_pipeline.find((lane) => lane.alias === 'coder');
  if (!coderLane) return;
  const now = leaderStatus.updated_at;
  const existing = await readLaneRuntimeState(runPaths, 'coder')
    ?? createPendingLaneRuntimeState(manifest, runPaths, coderLane, new Date(now));
  const nextStatus: GithubLaneRuntimeStatus = leaderStatus.implementation_complete
    ? 'completed'
    : leaderStatus.implementation_started
      ? 'running'
      : 'pending';
  await writeLaneRuntimeState(runPaths, {
    ...existing,
    status: nextStatus,
    completed_head_sha: nextStatus === 'completed' ? readRepoHeadSha(manifest.sandbox_repo_path) : existing.completed_head_sha,
    updated_at: now,
    started_at: existing.started_at ?? (leaderStatus.implementation_started ? now : existing.started_at),
    completed_at: nextStatus === 'completed' ? now : undefined,
  });
}

async function invalidateOutdatedCompletedLanes(
  manifest: GithubWorkonManifest,
  runPaths: GithubRunPaths,
): Promise<void> {
  const currentSnapshot = await captureLaneCompletionSnapshot(manifest, {
    alias: 'style-reviewer',
    role: 'style-reviewer',
    activation: 'hardening',
    phase: 'final-gate',
    mode: 'review',
    owner: 'coder',
    blocking: false,
    purpose: '',
  });
  const currentHead = currentSnapshot.headSha;
  if (!currentHead) return;
  const states = await loadLaneRuntimeStates(manifest, runPaths);
  const allChangedFiles = currentSnapshot.changedFiles;
  await Promise.all([...states.values()].map(async (state) => {
    if (
      state.alias === 'coder'
      || state.alias === 'publisher'
      || state.activation !== 'hardening'
      || state.status !== 'completed'
      || !state.completed_head_sha
      || state.completed_head_sha === currentHead
    ) {
      return;
    }
    const concern = await resolveLaneConcernMatch(manifest, state, allChangedFiles);
    const relevantCurrentFiles = concern.match.matched_files;
    const relevantCurrentHashes: Record<string, string> = {};
    for (const file of relevantCurrentFiles) {
      relevantCurrentHashes[file] = currentSnapshot.fileHashes[file] ?? readFileHashAtHead(manifest.sandbox_repo_path, file);
    }
    const priorHashes = state.completed_file_hashes ?? {};
    const relevantFiles = new Set([...Object.keys(priorHashes), ...Object.keys(relevantCurrentHashes)]);
    const changedRelevantFiles = [...relevantFiles].filter((file) => (priorHashes[file] ?? 'missing') !== (relevantCurrentHashes[file] ?? 'missing'));
    if (changedRelevantFiles.length === 0) {
      return;
    }
    const resetState: GithubLaneRuntimeState = {
      ...state,
      status: 'pending',
      pid: undefined,
      completed_at: undefined,
      last_error: undefined,
      invalidated_at: new Date().toISOString(),
      invalidated_reason: `Concern-relevant files changed after completion: ${changedRelevantFiles.join(', ')}`,
      invalidation_concern_match: concern.match,
      updated_at: new Date().toISOString(),
    };
    await writeLaneRuntimeState(runPaths, resetState);
    await appendLaneRuntimeEvent(runPaths, {
      type: 'lane_invalidated',
      lane_id: resetState.lane_id,
      alias: resetState.alias,
      role: resetState.role,
      at: resetState.updated_at,
      completed_head_sha: state.completed_head_sha,
      current_head_sha: currentHead,
      changed_files: changedRelevantFiles,
      concern_match: concern.match,
    });
  }));
}

function laneReady(
  state: GithubLaneRuntimeState,
  states: ReadonlyMap<string, GithubLaneRuntimeState>,
  leaderStatus: GithubLeaderRuntimeStatus,
): boolean {
  if (state.status !== 'pending') return false;
  for (const dependency of state.depends_on) {
    if (dependency === 'leader.bootstrap_complete' && !leaderStatus.bootstrap_complete) return false;
    if (dependency === 'leader.implementation_started' && !leaderStatus.implementation_started) return false;
    if (dependency === 'leader.ready_for_publication' && !leaderStatus.ready_for_publication) return false;
    if (dependency.startsWith('lane:')) {
      const laneState = states.get(dependency.slice('lane:'.length));
      if (!laneState || laneState.status !== 'completed') return false;
    }
  }
  return true;
}

function classifyLaneFailure(state: GithubLaneRuntimeState): {
  category: GithubLaneFailureCategory;
  retryable: boolean;
} {
  const error = state.last_error ?? '';
  const reviewerLike = state.profile === 'reviewer';
  const executorLike = state.profile === 'executor' || state.alias === 'publisher';
  if (/sandbox .*busy/i.test(error)) {
    return { category: 'sandbox_conflict', retryable: true };
  }
  if (/spawn|enoent|eacces|cannot find module|command not found/i.test(error)) {
    return { category: 'launch_failure', retryable: reviewerLike || executorLike };
  }
  if (/socket hang up|econnreset|etimedout|timed out|timeout|temporary failure|network/i.test(error)) {
    return { category: 'transient_io', retryable: executorLike };
  }
  if (/traceback|exception|tooling|tsc|biome|eslint|maven|gradle|cargo|npm err!/i.test(error)) {
    return { category: 'tooling_failure', retryable: reviewerLike || executorLike };
  }
  if (/assert|test failed|compilation failed|build failed|verification failed|lint failed|type error|syntax error/i.test(error)) {
    return { category: 'deterministic_task_failure', retryable: false };
  }
  return { category: 'unknown', retryable: Boolean(state.alias === 'publisher') };
}

function classifyPublisherBlockedReason(error: string): string {
  if (/CI timeout/i.test(error)) return 'ci_timeout';
  if (/failed_checks/i.test(error)) return 'ci_failed_checks';
  if (/sandbox .*busy/i.test(error)) return 'sandbox_conflict';
  if (/non-fast-forward|failed to push|already exists on remote/i.test(error)) return 'push_conflict';
  if (/draft PR|pull request/i.test(error)) return 'pr_sync';
  return 'unknown';
}

function resolveLaneRetryPolicy(
  state: GithubLaneRuntimeState,
  publicationState?: GithubPublicationState,
): { name: string; max_retries: number } {
  if (state.alias === 'publisher') {
    const publisherState = publicationState ?? 'blocked';
    const table: Record<GithubPublicationState | 'blocked', { name: string; max_retries: number }> = {
      not_started: { name: 'publisher-bootstrap', max_retries: 2 },
      implemented: { name: 'publisher-post-verification', max_retries: 2 },
      committed: { name: 'publisher-post-commit', max_retries: 3 },
      pushed: { name: 'publisher-post-push', max_retries: 3 },
      pr_opened: { name: 'publisher-post-pr', max_retries: 3 },
      ci_waiting: { name: 'publisher-ci-wait', max_retries: 3 },
      ci_green: { name: 'publisher-complete', max_retries: 0 },
      blocked: { name: 'publisher-recovery', max_retries: 3 },
    };
    return table[publisherState] ?? table.blocked;
  }
  if (state.role === 'architect') return { name: 'architect-recoverable-once', max_retries: 1 };
  if (state.profile === 'reviewer') return { name: `${state.profile}-recoverable-once`, max_retries: 1 };
  if (state.profile === 'executor') return { name: `${state.profile}-recoverable-once`, max_retries: 1 };
  return { name: `${state.profile}-no-retry`, max_retries: 0 };
}

function readRepoHeadSha(repoCheckoutPath: string): string | undefined {
  const result = gitExecAllowFailure(repoCheckoutPath, ['rev-parse', 'HEAD']);
  return result.ok ? result.stdout.trim() : undefined;
}

function readCurrentChangedFilesForInvalidation(
  repoCheckoutPath: string,
  baseRef?: string,
): string[] {
  const range = baseRef ? `${baseRef}...HEAD` : 'HEAD';
  const changed = new Set<string>();
  const include = (result: { ok: boolean; stdout: string }) => {
    if (!result.ok) return;
    for (const line of result.stdout.split('\n')) {
      const trimmed = line.trim();
      if (trimmed) changed.add(trimmed);
    }
  };
  include(gitExecAllowFailure(repoCheckoutPath, ['diff', '--name-only', range]));
  include(gitExecAllowFailure(repoCheckoutPath, ['diff', '--name-only']));
  include(gitExecAllowFailure(repoCheckoutPath, ['diff', '--cached', '--name-only']));
  include(gitExecAllowFailure(repoCheckoutPath, ['ls-files', '--others', '--exclude-standard']));
  return [...changed].sort();
}

async function laneConcernDescriptorForLane(
  lane: GithubLaneRuntimeState | GithubPipelineLane,
  repoCheckoutPath: string,
  registry?: Readonly<Record<string, GithubLaneConcernDescriptor>>,
): Promise<GithubLaneConcernDescriptor> {
  const resolvedRegistry = registry ?? await resolveGithubConcernRegistry(repoCheckoutPath);
  return resolveLaneConcernDescriptor(
    lane.alias,
    resolvedRegistry,
    'completed_concern_descriptor' in lane ? lane.completed_concern_descriptor : undefined,
  );
}

async function resolveLaneConcernMatch(
  manifest: GithubWorkonManifest,
  lane: GithubLaneRuntimeState | GithubPipelineLane,
  changedFiles?: string[],
): Promise<{
  descriptor: GithubLaneConcernDescriptor;
  match: GithubLaneConcernMatchResult;
  diagnostics: GithubConcernRegistryDiagnostic[];
}> {
  const registryDetails = await resolveGithubConcernRegistryDetails(manifest.sandbox_repo_path);
  const registry = registryDetails.registry;
  const descriptor = await laneConcernDescriptorForLane(lane, manifest.sandbox_repo_path, registry);
  const files = changedFiles ?? readCurrentChangedFilesForInvalidation(
    manifest.sandbox_repo_path,
    await readSandboxMetadataBaseRef(manifest),
  );
  if (isPerfLaneAlias(lane.alias)) {
    return {
      descriptor: {
        key: 'performance',
        pathPrefixes: [],
      },
      match: (await resolvePerfHotPathConcernMatch(manifest, files))!,
      diagnostics: [],
    };
  }
  const persistedDescriptor = 'completed_concern_descriptor' in lane ? lane.completed_concern_descriptor : undefined;
  const descriptorSource: GithubConcernDescriptorSource = persistedDescriptor
    ? 'persisted'
    : registryDetails.descriptor_sources[lane.alias]?.source ?? 'default';
  return {
    descriptor,
    match: resolveConcernMatchForFiles({
      descriptor,
      registry,
      descriptorSource,
      overridePath: registryDetails.descriptor_sources[lane.alias]?.path,
      changedFiles: files,
    }),
    diagnostics: registryDetails.diagnostics,
  };
}

function readFileHashAtHead(repoCheckoutPath: string, filePath: string): string {
  const result = gitExecAllowFailure(repoCheckoutPath, ['rev-parse', `HEAD:${filePath}`]);
  if (result.ok) return result.stdout.trim();
  return existsSync(join(repoCheckoutPath, filePath)) ? 'working-tree-untracked' : 'missing';
}

async function readSandboxMetadataBaseRef(manifest: GithubWorkonManifest): Promise<string | undefined> {
  const metadata = await readSandboxMetadata(manifest.sandbox_path);
  return metadata?.base_ref;
}

async function captureLaneCompletionSnapshot(
  manifest: GithubWorkonManifest,
  lane: GithubLaneRuntimeState | GithubPipelineLane,
): Promise<{
  changedFiles: string[];
  fileHashes: Record<string, string>;
  headSha?: string;
  descriptor: GithubLaneConcernDescriptor;
  match: GithubLaneConcernMatchResult;
}> {
  const baseRef = await readSandboxMetadataBaseRef(manifest);
  const allChangedFiles = readCurrentChangedFilesForInvalidation(manifest.sandbox_repo_path, baseRef);
  const { descriptor, match } = await resolveLaneConcernMatch(manifest, lane, allChangedFiles);
  const changedFiles = match.matched_files;
  const fileHashes: Record<string, string> = {};
  for (const file of changedFiles) {
    fileHashes[file] = readFileHashAtHead(manifest.sandbox_repo_path, file);
  }
  return {
    changedFiles,
    fileHashes,
    headSha: readRepoHeadSha(manifest.sandbox_repo_path),
    descriptor,
    match,
  };
}

async function ensureSandboxNanaShim(sandboxPath: string): Promise<string> {
  const binDir = sandboxNanaBinPath(sandboxPath);
  await mkdir(binDir, { recursive: true });
  const cliPath = join(getPackageRoot(), 'dist', 'cli', 'nana.js');
  const shimSource = [
    '#!/bin/sh',
    `exec ${JSON.stringify(process.execPath)} ${JSON.stringify(cliPath)} "$@"`,
    '',
  ].join('\n');
  for (const shimName of ['nana', 'nana']) {
    const shimPath = join(binDir, shimName);
    await writeFile(shimPath, shimSource, 'utf-8');
    await chmod(shimPath, 0o755);
  }
  return binDir;
}

async function cleanupSandbox(
  paths: ManagedRepoPaths,
  sandboxId: string,
  sandboxPath: string,
  repoCheckoutPath: string,
): Promise<void> {
  try {
    const stat = await lstat(sandboxPath);
    if (stat.isSymbolicLink()) {
      await unlink(sandboxPath);
      await releaseSandboxLease(sandboxLockDirFor(paths, sandboxId));
      return;
    }
  } catch {
    // fall through to best-effort cleanup below
  }
  await removeExistingSandboxWorktree(paths, repoCheckoutPath);
  await releaseSandboxLease(sandboxLockDirFor(paths, sandboxId));
  await rm(sandboxPath, { recursive: true, force: true });
}

async function readManagedRepoSettings(paths: ManagedRepoPaths): Promise<ManagedRepoSettings | null> {
  return readJsonFile<ManagedRepoSettings>(paths.repoSettingsPath);
}

async function writeManagedRepoSettings(
  paths: ManagedRepoPaths,
  considerations: readonly GithubConsideration[],
  roleLayout: GithubRoleLayout | undefined,
  reviewRulesMode: GithubReviewRulesMode | undefined,
  reviewRulesReviewerPolicy: GithubReviewRulesReviewerPolicy | undefined,
  hotPathApiProfile: GithubRepoHotPathApiProfile | undefined,
  now: Date,
): Promise<ManagedRepoSettings> {
  const settings: ManagedRepoSettings = {
    version: reviewRulesMode || hotPathApiProfile ? 4 : 2,
    default_considerations: [...new Set(considerations)],
    ...(roleLayout ? { default_role_layout: roleLayout } : {}),
    ...(reviewRulesMode ? { review_rules_mode: reviewRulesMode } : {}),
    ...(reviewRulesReviewerPolicy ? { review_rules_reviewer_policy: normalizeReviewerPolicy(reviewRulesReviewerPolicy) } : {}),
    ...(hotPathApiProfile ? { hot_path_api_profile: hotPathApiProfile } : {}),
    updated_at: now.toISOString(),
  };
  await writeJsonFile(paths.repoSettingsPath, settings);
  return settings;
}

async function refreshManagedRepoHotPathApiProfile(
  paths: ManagedRepoPaths,
  settings: ManagedRepoSettings | null,
  repoCheckoutPath: string,
  now: Date,
): Promise<ManagedRepoSettings> {
  const trackedFilesResult = gitExecAllowFailure(repoCheckoutPath, ['ls-files']);
  const trackedFiles = trackedFilesResult.ok
    ? trackedFilesResult.stdout.split('\n').map((line) => line.trim()).filter(Boolean)
    : [];
  const inferredProfile = await inferRepoHotPathApiProfile(repoCheckoutPath, trackedFiles, now);
  const { override, path: overridePath } = await readRepoHotPathApiProfileOverride(repoCheckoutPath);
  const hotPathApiProfile = override
    ? mergeHotPathApiProfileOverride(inferredProfile, override, overridePath)
    : inferredProfile;
  return writeManagedRepoSettings(
    paths,
    settings?.default_considerations ?? [],
    settings?.default_role_layout,
    settings?.review_rules_mode,
    settings?.review_rules_reviewer_policy,
    hotPathApiProfile,
    now,
  );
}

async function findIssueSandboxByBranch(
  paths: ManagedRepoPaths,
  branchName: string,
): Promise<{ sandboxId: string; sandboxPath: string } | null> {
  if (!existsSync(paths.sandboxesDir)) return null;
  const entries = await readdir(paths.sandboxesDir, { withFileTypes: true });
  for (const entry of entries) {
    const sandboxPath = join(paths.sandboxesDir, entry.name);
    try {
      const stat = await lstat(sandboxPath);
      if (!stat.isDirectory()) continue;
    } catch {
      continue;
    }
    const metadata = await readSandboxMetadata(sandboxPath);
    if (!metadata) continue;
    if (metadata.target_kind !== 'issue') continue;
    if (metadata.branch_name !== branchName) continue;
    return { sandboxId: metadata.sandbox_id, sandboxPath };
  }
  return null;
}

async function ensurePrSandboxLink(
  paths: ManagedRepoPaths,
  prNumber: number,
  issueSandboxPath: string,
  issueSandboxId: string,
): Promise<void> {
  const prSandboxId = buildTargetSandboxId('pr', prNumber);
  const prSandboxPath = sandboxPathFor(paths, prSandboxId);
  const issueLockPath = sandboxLockDirFor(paths, issueSandboxId);
  const prLockPath = sandboxLockDirFor(paths, prSandboxId);
  if (!existsSync(prSandboxPath)) {
    await ensureSymlink(prSandboxPath, issueSandboxPath);
  }
  if (!existsSync(prLockPath)) {
    await ensureSymlink(prLockPath, issueLockPath);
  }
}

async function maybeLinkPrSandboxFromIssue(
  paths: ManagedRepoPaths,
  issueTarget: ParsedGithubTargetUrl,
  issueSandboxId: string,
  issueSandboxPath: string,
  repoMeta: ManagedRepoMetadata,
  context: GithubApiContext,
): Promise<void> {
  if (issueTarget.targetKind !== 'issue') return;
  if (!existsSync(issueSandboxPath)) return;
  const branchName = buildSandboxBranch(issueTarget, issueSandboxId);
  const prs = await fetchPullRequestsForHeadBranch(issueTarget.repoSlug, repoMeta.repo_owner, branchName, context);
  for (const pr of prs) {
    await ensurePrSandboxLink(paths, pr.number, issueSandboxPath, issueSandboxId);
  }
}

async function listIssueAssociatedSandboxes(
  paths: ManagedRepoPaths,
  issueNumber: number,
): Promise<Array<{ sandboxId: string; sandboxPath: string }>> {
  if (!existsSync(paths.sandboxesDir)) return [];
  const entries = await readdir(paths.sandboxesDir, { withFileTypes: true });
  const matches: Array<{ sandboxId: string; sandboxPath: string }> = [];

  for (const entry of entries) {
    if (!entry.isDirectory() && !entry.isSymbolicLink()) continue;
    const sandboxPath = join(paths.sandboxesDir, entry.name);
    const linkedIssueNumber = await resolveIssueAssociationNumber(sandboxPath, undefined, undefined);
    if (linkedIssueNumber !== issueNumber) continue;
    matches.push({ sandboxId: entry.name, sandboxPath });
  }

  return matches.sort((left, right) => left.sandboxId.localeCompare(right.sandboxId));
}

async function maybeLinkPrSandboxToIssue(
  paths: ManagedRepoPaths,
  prTarget: ParsedGithubTargetUrl,
  targetContext: GithubTargetContext,
): Promise<void> {
  if (prTarget.targetKind !== 'pr') return;
  const prSandboxId = buildTargetSandboxId('pr', prTarget.targetNumber);
  const prSandboxPath = sandboxPathFor(paths, prSandboxId);
  if (existsSync(prSandboxPath)) return;
  const branchName = targetContext.pullRequest?.head.ref;
  if (!branchName) return;
  const issueSandbox = await findIssueSandboxByBranch(paths, branchName);
  if (!issueSandbox) return;
  await ensurePrSandboxLink(paths, prTarget.targetNumber, issueSandbox.sandboxPath, issueSandbox.sandboxId);
}

async function tryAcquireSandboxLease(
  paths: ManagedRepoPaths,
  sandboxId: string,
  runId: string,
  targetUrl: string,
  now: Date,
): Promise<{ ok: true; lockDir: string; lease: SandboxLease } | { ok: false; busyLease: SandboxLease | null }> {
  const lockDir = sandboxLockDirFor(paths, sandboxId);
  await mkdir(dirname(lockDir), { recursive: true });
  const lease: SandboxLease = {
    version: 1,
    sandbox_id: sandboxId,
    owner_pid: process.pid,
    owner_run_id: runId,
    target_url: targetUrl,
    acquired_at: now.toISOString(),
    heartbeat_at: now.toISOString(),
    expires_at: new Date(now.getTime() + SANDBOX_LOCK_TTL_MS).toISOString(),
  };

  try {
    await mkdir(lockDir, { recursive: false });
    await writeSandboxLease(lockDir, lease);
    return { ok: true, lockDir, lease };
  } catch {
    const existing = await readSandboxLease(lockDir);
    if (!existing || isSandboxLeaseStale(existing)) {
      await rm(lockDir, { recursive: true, force: true });
      await mkdir(lockDir, { recursive: false });
      await writeSandboxLease(lockDir, lease);
      return { ok: true, lockDir, lease };
    }
    return { ok: false, busyLease: existing };
  }
}

async function allocateSandbox(
  paths: ManagedRepoPaths,
  repoMeta: ManagedRepoMetadata,
  target: ParsedGithubTargetUrl,
  runId: string,
  now: Date,
  options: { newPr?: boolean } = {},
): Promise<SandboxAllocation> {
  await mkdir(paths.sandboxesDir, { recursive: true });
  await mkdir(paths.sandboxLocksDir, { recursive: true });
  ensureSourceClone(paths, repoMeta);

  const newPr = options.newPr === true;
  const latestIssueSandbox = !newPr && target.targetKind === 'issue'
    ? await findLatestIssueSandbox(paths, target.targetNumber)
    : null;
  const sandboxId = latestIssueSandbox?.sandboxId ?? buildInitialSandboxId(target, runId);
  const attempt = await tryAcquireSandboxLease(paths, sandboxId, runId, target.canonicalUrl, now);
  if (!attempt.ok) {
    throw new Error(
      `Sandbox ${sandboxId} is busy (owner pid ${attempt.busyLease?.owner_pid ?? 'unknown'}). Wait for it to finish or let the lease expire.`,
    );
  }

  const lockDir = attempt.lockDir;
  const lease = attempt.lease;
  const sandboxPath = sandboxPathFor(paths, sandboxId);
  const repoCheckoutPath = join(sandboxPath, 'repo');
  const branchName = buildSandboxBranch(target, sandboxId);
  const baseRef = fetchTargetBaseRef(paths, repoMeta, target);
  const existingMetadata = await readSandboxMetadata(sandboxPath);

  const shouldRecreate =
    newPr
    || 
    !existsSync(repoCheckoutPath)
    || !existsSync(join(repoCheckoutPath, '.git'))
    || existingMetadata?.target_kind !== target.targetKind
    || existingMetadata?.target_number !== target.targetNumber;

  if (shouldRecreate) {
    await removeExistingSandboxWorktree(paths, repoCheckoutPath);
    await mkdir(sandboxPath, { recursive: true });
    gitExec(paths.sourcePath, ['worktree', 'add', '--force', '-B', branchName, repoCheckoutPath, baseRef]);
  }

  const gitDirPath = resolveAbsoluteGitDir(repoCheckoutPath);
  await writeSandboxMetadata(sandboxPath, {
    version: 1,
    sandbox_id: sandboxId,
    repo_slug: repoMeta.repo_slug,
    repo_name: repoMeta.repo_name,
    sandbox_path: sandboxPath,
    repo_checkout_path: repoCheckoutPath,
    branch_name: branchName,
    base_ref: baseRef,
    target_kind: target.targetKind,
    target_number: target.targetNumber,
    created_at: existingMetadata?.created_at ?? now.toISOString(),
    updated_at: now.toISOString(),
  });

  return {
    sandboxId,
    sandboxPath,
    repoCheckoutPath,
    gitDirPath,
    lockDir,
    lease,
    branchName,
    baseRef,
  };
}

function startSandboxLeaseHeartbeat(input: {
  lockDir: string;
  sandboxId: string;
  ownerPid: number;
  ttlMs: number;
  heartbeatMs: number;
}): void {
  const scriptPath = join(getPackageRoot(), 'dist', 'scripts', 'github-sandbox-lock-heartbeat.js');
  const child = spawn(process.execPath, [
    scriptPath,
    '--lock-dir', input.lockDir,
    '--sandbox-id', input.sandboxId,
    '--owner-pid', String(input.ownerPid),
    '--ttl-ms', String(input.ttlMs),
    '--heartbeat-ms', String(input.heartbeatMs),
  ], {
    stdio: 'ignore',
    detached: true,
  });
  child.unref();
}

function startSchedulerDaemon(input: {
  runId: string;
  homeDir?: string;
}): void {
  const scriptPath = join(getPackageRoot(), 'dist', 'scripts', 'github-workon-scheduler-daemon.js');
  const args = [
    scriptPath,
    '--run-id', input.runId,
    ...(input.homeDir ? ['--home-dir', input.homeDir] : []),
  ];
  const child = spawn(process.execPath, args, {
    stdio: 'ignore',
    detached: true,
  });
  child.unref();
}

function readMetricsTotals(raw: unknown): GithubTokenTotals {
  if (!raw || typeof raw !== 'object') {
    return { input_tokens: 0, output_tokens: 0, total_tokens: 0 };
  }
  const metrics = raw as Record<string, unknown>;
  const input = Number(metrics.session_input_tokens ?? 0);
  const output = Number(metrics.session_output_tokens ?? 0);
  const total = Number(metrics.session_total_tokens ?? (input + output));
  return {
    input_tokens: Number.isFinite(input) ? input : 0,
    output_tokens: Number.isFinite(output) ? output : 0,
    total_tokens: Number.isFinite(total) ? total : 0,
  };
}

function buildMetricsFingerprint(raw: unknown): string {
  if (!raw || typeof raw !== 'object') return 'missing';
  const metrics = raw as Record<string, unknown>;
  return JSON.stringify({
    session_input_tokens: metrics.session_input_tokens ?? 0,
    session_output_tokens: metrics.session_output_tokens ?? 0,
    session_total_tokens: metrics.session_total_tokens ?? 0,
    session_turns: metrics.session_turns ?? 0,
    total_turns: metrics.total_turns ?? 0,
    last_activity: metrics.last_activity ?? '',
  });
}

async function readIssueTokenStats(
  paths: ManagedRepoPaths,
  repoSlug: string,
  issueNumber: number,
): Promise<GithubIssueTokenStats> {
  const existing = await readJsonFile<GithubIssueTokenStats>(issueStatsPath(paths, issueNumber));
  if (existing) return existing;
  return {
    version: 1,
    repo_slug: repoSlug,
    issue_number: issueNumber,
    updated_at: new Date(0).toISOString(),
    totals: {
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 0,
      sessions_accounted: 0,
    },
    sandboxes: {},
  };
}

async function writeIssueTokenStats(
  paths: ManagedRepoPaths,
  issueNumber: number,
  stats: GithubIssueTokenStats,
): Promise<void> {
  await writeJsonFile(issueStatsPath(paths, issueNumber), stats);
}

async function resolveIssueAssociationNumber(
  sandboxPath: string,
  fallbackTargetKind?: GithubTargetKind,
  fallbackTargetNumber?: number,
): Promise<number | undefined> {
  if (fallbackTargetKind === 'issue' && typeof fallbackTargetNumber === 'number') {
    return fallbackTargetNumber;
  }

  try {
    const stat = await lstat(sandboxPath);
    if (stat.isSymbolicLink()) {
      const linkTarget = await readlink(sandboxPath);
      const resolvedSandboxPath = resolve(dirname(sandboxPath), linkTarget);
      const linkedMetadata = await readSandboxMetadata(resolvedSandboxPath);
      if (linkedMetadata?.target_kind === 'issue') return linkedMetadata.target_number;
    }
  } catch {
    // ignore and fall through
  }

  const metadata = await readSandboxMetadata(sandboxPath);
  if (metadata?.target_kind === 'issue') return metadata.target_number;
  return undefined;
}

async function captureSandboxTokenUsageForIssue(
  paths: ManagedRepoPaths,
  repoSlug: string,
  sandboxId: string,
  sandboxPath: string,
  issueNumber: number | undefined,
  now: Date,
  fallbackTotals?: Partial<GithubTokenTotals>,
): Promise<GithubIssueTokenStats | null> {
  if (typeof issueNumber !== 'number') return null;

  const activeSession = await readSessionState(sandboxPath);
  if (activeSession && !isSessionStale(activeSession)) {
    return null;
  }

  const metricsRaw = await readJsonFile<Record<string, unknown>>(sandboxMetricsPath(sandboxPath));
  const totals = readMetricsTotals(metricsRaw);
  const resolvedTotals =
    totals.total_tokens > 0 || totals.input_tokens > 0 || totals.output_tokens > 0
      ? totals
      : {
          input_tokens: Number.isFinite(Number(fallbackTotals?.input_tokens)) ? Number(fallbackTotals?.input_tokens) : 0,
          output_tokens: Number.isFinite(Number(fallbackTotals?.output_tokens)) ? Number(fallbackTotals?.output_tokens) : 0,
          total_tokens: Number.isFinite(Number(fallbackTotals?.total_tokens)) ? Number(fallbackTotals?.total_tokens) : 0,
        };
  if (resolvedTotals.total_tokens <= 0 && resolvedTotals.input_tokens <= 0 && resolvedTotals.output_tokens <= 0) {
    return readIssueTokenStats(paths, repoSlug, issueNumber);
  }

  const fingerprint = buildMetricsFingerprint(
    metricsRaw ?? {
      session_input_tokens: resolvedTotals.input_tokens,
      session_output_tokens: resolvedTotals.output_tokens,
      session_total_tokens: resolvedTotals.total_tokens,
      last_activity: now.toISOString(),
    },
  );
  const accounting = await readJsonFile<GithubSandboxTokenRollup>(sandboxTokenAccountingPath(sandboxPath));
  if (accounting?.last_accounted_fingerprint === fingerprint) {
    return readIssueTokenStats(paths, repoSlug, issueNumber);
  }

  const issueStats = await readIssueTokenStats(paths, repoSlug, issueNumber);
  const sandboxRollup = issueStats.sandboxes[sandboxId] ?? {
    input_tokens: 0,
    output_tokens: 0,
    total_tokens: 0,
    sessions_accounted: 0,
  };

  sandboxRollup.input_tokens += resolvedTotals.input_tokens;
  sandboxRollup.output_tokens += resolvedTotals.output_tokens;
  sandboxRollup.total_tokens += resolvedTotals.total_tokens;
  sandboxRollup.sessions_accounted += 1;
  sandboxRollup.last_accounted_fingerprint = fingerprint;
  sandboxRollup.last_accounted_at = now.toISOString();

  issueStats.sandboxes[sandboxId] = sandboxRollup;
  issueStats.totals.input_tokens += resolvedTotals.input_tokens;
  issueStats.totals.output_tokens += resolvedTotals.output_tokens;
  issueStats.totals.total_tokens += resolvedTotals.total_tokens;
  issueStats.totals.sessions_accounted += 1;
  issueStats.updated_at = now.toISOString();

  await writeIssueTokenStats(paths, issueNumber, issueStats);
  await writeJsonFile(sandboxTokenAccountingPath(sandboxPath), sandboxRollup);
  return issueStats;
}

function formatTokenCount(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
  return String(value);
}

function buildIssueStatsLines(stats: GithubIssueTokenStats): string[] {
  const lines = [
    `[github] Token stats for ${stats.repo_slug} issue #${stats.issue_number}`,
    `[github] Total input tokens: ${stats.totals.input_tokens} (${formatTokenCount(stats.totals.input_tokens)})`,
    `[github] Total output tokens: ${stats.totals.output_tokens} (${formatTokenCount(stats.totals.output_tokens)})`,
    `[github] Total tokens: ${stats.totals.total_tokens} (${formatTokenCount(stats.totals.total_tokens)})`,
    `[github] Sessions accounted: ${stats.totals.sessions_accounted}`,
  ];

  const sandboxEntries = Object.entries(stats.sandboxes).sort((left, right) => left[0].localeCompare(right[0]));
  if (sandboxEntries.length > 0) {
    lines.push('[github] Sandbox breakdown:');
    for (const [sandboxId, sandboxStats] of sandboxEntries) {
      lines.push(
        `[github]   - ${sandboxId}: total=${sandboxStats.total_tokens} input=${sandboxStats.input_tokens} output=${sandboxStats.output_tokens} sessions=${sandboxStats.sessions_accounted}`,
      );
    }
  }

  return lines;
}

function retrospectivePath(runPaths: GithubRunPaths): string {
  return join(runPaths.runDir, 'retrospective.md');
}

function threadUsagePath(runPaths: GithubRunPaths): string {
  return join(runPaths.runDir, 'thread-usage.json');
}

function laneRuntimeDir(runPaths: GithubRunPaths): string {
  return join(runPaths.runDir, 'lane-runtime');
}

function laneInstructionsPath(runPaths: GithubRunPaths, laneAlias: string): string {
  return join(laneRuntimeDir(runPaths), `${sanitizePathToken(laneAlias)}-instructions.md`);
}

function laneResultPath(runPaths: GithubRunPaths, laneAlias: string): string {
  return join(laneRuntimeDir(runPaths), `${sanitizePathToken(laneAlias)}-result.md`);
}

function laneStdoutPath(runPaths: GithubRunPaths, laneAlias: string): string {
  return join(laneRuntimeDir(runPaths), `${sanitizePathToken(laneAlias)}-stdout.log`);
}

function laneStderrPath(runPaths: GithubRunPaths, laneAlias: string): string {
  return join(laneRuntimeDir(runPaths), `${sanitizePathToken(laneAlias)}-stderr.log`);
}

function laneStatePath(runPaths: GithubRunPaths, laneAlias: string): string {
  return join(laneRuntimeDir(runPaths), `${sanitizePathToken(laneAlias)}.json`);
}

function laneEventsPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'events.jsonl');
}

function laneEventCounterPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'event-counter.json');
}

function leaderStatusPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'leader-status.json');
}

function leaderInboxPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'leader-inbox.md');
}

function leaderInboxCursorPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'leader-inbox.cursor.json');
}

function publisherInboxPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'publisher-inbox.md');
}

function publisherInboxCursorPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'publisher-inbox.cursor.json');
}

function publisherStatusPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'publisher-status.json');
}

function schedulerStatusPath(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'scheduler-state.json');
}

function schedulerPassesDir(runPaths: GithubRunPaths): string {
  return join(laneRuntimeDir(runPaths), 'scheduler-passes');
}

function schedulerPassArtifactPath(runPaths: GithubRunPaths, passId: number): string {
  return join(schedulerPassesDir(runPaths), `pass-${String(passId).padStart(4, '0')}.json`);
}

async function readThreadUsageArtifact(runPaths: GithubRunPaths): Promise<GithubThreadUsageArtifact | null> {
  return readJsonFile<GithubThreadUsageArtifact>(threadUsagePath(runPaths));
}

async function appendLaneRuntimeEvent(
  runPaths: GithubRunPaths,
  event: Record<string, unknown>,
): Promise<{ id: number } & Record<string, unknown>> {
  const path = laneEventsPath(runPaths);
  const counterPath = laneEventCounterPath(runPaths);
  await mkdir(dirname(path), { recursive: true });
  const previous = await readJsonFile<{ next_id?: number }>(counterPath).catch(() => null);
  const id = Math.max(1, previous?.next_id ?? 1);
  await writeJsonFile(counterPath, { next_id: id + 1 });
  const fullEvent = { id, ...event };
  await writeFile(path, `${JSON.stringify(fullEvent)}\n`, { encoding: 'utf-8', flag: 'a' });
  return fullEvent;
}

async function writeLaneRuntimeState(
  runPaths: GithubRunPaths,
  state: GithubLaneRuntimeState,
): Promise<void> {
  await writeJsonFile(laneStatePath(runPaths, state.alias), state);
}

async function writeLeaderStatus(
  runPaths: GithubRunPaths,
  status: GithubLeaderRuntimeStatus,
): Promise<void> {
  await writeJsonFile(leaderStatusPath(runPaths), status);
}

async function readLeaderStatus(runPaths: GithubRunPaths): Promise<GithubLeaderRuntimeStatus | null> {
  return readJsonFile<GithubLeaderRuntimeStatus>(leaderStatusPath(runPaths));
}

async function writePublisherStatus(
  runPaths: GithubRunPaths,
  status: GithubPublisherRuntimeStatus,
): Promise<void> {
  await writeJsonFile(publisherStatusPath(runPaths), status);
}

async function readPublisherStatus(runPaths: GithubRunPaths): Promise<GithubPublisherRuntimeStatus | null> {
  return readJsonFile<GithubPublisherRuntimeStatus>(publisherStatusPath(runPaths));
}

async function writeSchedulerStatus(
  runPaths: GithubRunPaths,
  status: GithubSchedulerRuntimeState,
): Promise<void> {
  await writeJsonFile(schedulerStatusPath(runPaths), status);
}

async function readSchedulerStatus(runPaths: GithubRunPaths): Promise<GithubSchedulerRuntimeState | null> {
  return readJsonFile<GithubSchedulerRuntimeState>(schedulerStatusPath(runPaths));
}

async function readLaneRuntimeState(
  runPaths: GithubRunPaths,
  laneAlias: string,
): Promise<GithubLaneRuntimeState | null> {
  return readJsonFile<GithubLaneRuntimeState>(laneStatePath(runPaths, laneAlias));
}

function isProcessAlive(pid: number | undefined): boolean {
  if (!pid || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function recoverStaleSessionStatuses(
  manifest: GithubWorkonManifest,
  runPaths: GithubRunPaths,
): Promise<Array<{ target: string; reason: string }>> {
  const recovered: Array<{ target: string; reason: string }> = [];
  const now = new Date().toISOString();

  const leaderStatus = await readLeaderStatus(runPaths);
  if (leaderStatus?.session_active && !isProcessAlive(leaderStatus.pid)) {
    await writeLeaderStatus(runPaths, {
      ...leaderStatus,
      session_active: false,
      pid: undefined,
      updated_at: now,
    });
    await appendLaneRuntimeEvent(runPaths, {
      type: 'runtime_recovered',
      target: 'leader',
      reason: 'stale_session_reset',
      at: now,
    });
    recovered.push({ target: 'leader', reason: 'stale_session_reset' });
  }

  const publisherStatus = await readPublisherStatus(runPaths);
  if (publisherStatus?.session_active && !isProcessAlive(publisherStatus.pid)) {
    await writePublisherStatus(runPaths, {
      ...publisherStatus,
      session_active: false,
      pid: undefined,
      updated_at: now,
      blocked: manifest.publication_state === 'blocked' ? publisherStatus.blocked : false,
    });
    await appendLaneRuntimeEvent(runPaths, {
      type: 'runtime_recovered',
      target: 'publisher',
      reason: 'stale_session_reset',
      at: now,
    });
    recovered.push({ target: 'publisher', reason: 'stale_session_reset' });
  }

  return recovered;
}

async function replaySchedulerUnseenEvents(input: {
  manifest: GithubWorkonManifest;
  runPaths: GithubRunPaths;
  afterEventId: number;
  upToEventId: number;
}): Promise<Array<Record<string, unknown> & { id: number }>> {
  const unseenEvents = (await readLaneRuntimeEventsAfter(input.runPaths, input.afterEventId))
    .filter((event) => event.id <= input.upToEventId);

  for (const event of unseenEvents) {
    if (event.type === 'lane_invalidated' && typeof event.alias === 'string') {
      const existing = await readLaneRuntimeState(input.runPaths, event.alias);
      if (!existing) continue;
      const changedFiles = Array.isArray(event.changed_files)
        ? event.changed_files.map((value) => String(value)).filter(Boolean)
        : [];
      const concernMatch = typeof event.concern_match === 'object' && event.concern_match
        ? event.concern_match as GithubLaneConcernMatchResult
        : existing.invalidation_concern_match;
      const nextState: GithubLaneRuntimeState = {
        ...existing,
        status: 'pending',
        pid: undefined,
        completed_at: undefined,
        invalidated_at: typeof event.at === 'string' ? event.at : existing.invalidated_at ?? new Date().toISOString(),
        invalidated_reason: changedFiles.length > 0
          ? `Concern-relevant files changed after completion: ${changedFiles.join(', ')}`
          : existing.invalidated_reason,
        invalidation_concern_match: concernMatch,
        updated_at: typeof event.at === 'string' ? event.at : existing.updated_at,
      };
      await writeLaneRuntimeState(input.runPaths, nextState);
      continue;
    }
    if (event.type === 'lane_retried' && typeof event.alias === 'string') {
      const existing = await readLaneRuntimeState(input.runPaths, event.alias);
      if (!existing) continue;
      const retryCount = typeof event.retry_count === 'number' ? event.retry_count : existing.retry_count ?? 0;
      await writeLaneRuntimeState(input.runPaths, {
        ...existing,
        status: 'pending',
        pid: undefined,
        retry_count: Math.max(existing.retry_count ?? 0, retryCount),
        failure_category: typeof event.failure_category === 'string' ? event.failure_category as GithubLaneFailureCategory : existing.failure_category,
        updated_at: typeof event.at === 'string' ? event.at : existing.updated_at,
      });
      continue;
    }
    if (event.type === 'runtime_recovered' && event.target === 'leader') {
      const leaderStatus = await readLeaderStatus(input.runPaths);
      if (!leaderStatus) continue;
      await writeLeaderStatus(input.runPaths, {
        ...leaderStatus,
        session_active: false,
        pid: undefined,
        updated_at: typeof event.at === 'string' ? event.at : leaderStatus.updated_at,
      });
      continue;
    }
    if (event.type === 'runtime_recovered' && event.target === 'publisher') {
      const publisherStatus = await readPublisherStatus(input.runPaths);
      if (!publisherStatus) continue;
      await writePublisherStatus(input.runPaths, {
        ...publisherStatus,
        session_active: false,
        pid: undefined,
        updated_at: typeof event.at === 'string' ? event.at : publisherStatus.updated_at,
      });
    }
  }

  return unseenEvents;
}

async function appendInboxDelta(
  inboxPath: string,
  lane: GithubPipelineLane,
  content: string,
  eventId: number,
): Promise<void> {
  const trimmed = content.trim();
  if (!trimmed) return;
  await mkdir(dirname(inboxPath), { recursive: true });
  await writeFile(
    inboxPath,
    [
      `<!-- event_id:${eventId} lane:${lane.alias} -->`,
      `## Lane ${lane.alias}`,
      '',
      `Role: ${lane.role}`,
      `Phase: ${lane.phase}`,
      `Mode: ${lane.mode}`,
      `Blocking: ${lane.blocking ? 'yes' : 'no'}`,
      '',
      trimmed,
      '',
    ].join('\n'),
    { encoding: 'utf-8', flag: 'a' },
  );
}

async function readUnreadInboxContent(
  inboxPath: string,
  cursorPath: string,
): Promise<{ unread: string; cursor: number; lastEventId: number }> {
  const content = existsSync(inboxPath) ? await readFile(inboxPath, 'utf-8') : '';
  const cursor = await readJsonFile<{ offset?: number; last_event_id?: number }>(cursorPath).catch(() => null);
  const offset = Math.max(0, Math.min(cursor?.offset ?? 0, Buffer.byteLength(content)));
  const unread = Buffer.from(content, 'utf-8').subarray(offset).toString('utf-8');
  const ids = [...unread.matchAll(/<!-- event_id:(\d+) /g)].map((match) => Number(match[1] ?? 0)).filter((value) => Number.isFinite(value));
  return {
    unread,
    cursor: Buffer.byteLength(content),
    lastEventId: ids.length > 0 ? Math.max(...ids) : (cursor?.last_event_id ?? 0),
  };
}

async function advanceInboxCursor(cursorPath: string, offset: number, lastEventId: number = 0): Promise<void> {
  await writeJsonFile(cursorPath, { offset, last_event_id: lastEventId });
}

async function readLatestLaneEventId(runPaths: GithubRunPaths): Promise<number> {
  const counter = await readJsonFile<{ next_id?: number }>(laneEventCounterPath(runPaths)).catch(() => null);
  return Math.max(0, (counter?.next_id ?? 1) - 1);
}

async function readLaneRuntimeEventsAfter(
  runPaths: GithubRunPaths,
  afterEventId: number,
): Promise<Array<Record<string, unknown> & { id: number }>> {
  const path = laneEventsPath(runPaths);
  if (!existsSync(path)) return [];
  const content = await readFile(path, 'utf-8').catch(() => '');
  if (!content.trim()) return [];
  const events: Array<Record<string, unknown> & { id: number }> = [];
  for (const line of content.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    try {
      const parsed = JSON.parse(trimmed) as Record<string, unknown> & { id?: unknown };
      const id = Number(parsed.id ?? 0);
      if (!Number.isFinite(id) || id <= afterEventId) continue;
      events.push({ ...parsed, id });
    } catch {
      // Ignore malformed event rows in retrospective-only paths.
    }
  }
  return events.sort((left, right) => left.id - right.id);
}

async function writeSchedulerPassArtifact(
  runPaths: GithubRunPaths,
  artifact: GithubSchedulerPassArtifact,
): Promise<void> {
  await mkdir(schedulerPassesDir(runPaths), { recursive: true });
  await writeJsonFile(schedulerPassArtifactPath(runPaths, artifact.pass_id), artifact);
}

async function readSchedulerPassArtifacts(
  runPaths: GithubRunPaths,
): Promise<GithubSchedulerPassArtifact[]> {
  const dir = schedulerPassesDir(runPaths);
  if (!existsSync(dir)) return [];
  const entries = await readdir(dir, { withFileTypes: true });
  const artifacts: GithubSchedulerPassArtifact[] = [];
  for (const entry of entries) {
    if (!entry.isFile() || !entry.name.endsWith('.json')) continue;
    const artifact = await readJsonFile<GithubSchedulerPassArtifact>(join(dir, entry.name)).catch(() => null);
    if (artifact) artifacts.push(artifact);
  }
  return artifacts.sort((left, right) => left.pass_id - right.pass_id);
}

async function listRolloutFiles(dir: string): Promise<string[]> {
  if (!existsSync(dir)) return [];
  const entries = await readdir(dir, { withFileTypes: true });
  const files: string[] = [];
  for (const entry of entries) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...await listRolloutFiles(path));
      continue;
    }
    if (entry.isFile() && path.endsWith('.jsonl')) {
      files.push(path);
    }
  }
  return files.sort();
}

function asNumber(value: unknown): number | null {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

async function readThreadRowsFromRollouts(sandboxPath: string): Promise<GithubThreadRetrospectiveRow[]> {
  const sessionsRoot = join(sandboxPath, '.codex', 'sessions');
  const rolloutFiles = await listRolloutFiles(sessionsRoot);
  const rows: GithubThreadRetrospectiveRow[] = [];

  for (const file of rolloutFiles) {
    const content = await readFile(file, 'utf-8').catch(() => '');
    if (!content.trim()) continue;

    let nickname = '';
    let role = '';
    let startedAt = 0;
    let updatedAt = 0;
    let totalTokens = 0;

    for (const line of content.split('\n')) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      let parsed: { timestamp?: string; type?: string; payload?: any } | null = null;
      try {
        parsed = JSON.parse(trimmed) as { timestamp?: string; type?: string; payload?: any };
      } catch {
        continue;
      }

      const timestampMs = parsed?.timestamp ? Date.parse(parsed.timestamp) : NaN;
      const timestampSeconds = Number.isFinite(timestampMs) ? Math.floor(timestampMs / 1000) : 0;
      if (startedAt === 0 && timestampSeconds > 0) startedAt = timestampSeconds;
      if (timestampSeconds > updatedAt) updatedAt = timestampSeconds;

      if (parsed?.type === 'session_meta') {
        const payload = parsed.payload ?? {};
        nickname = typeof payload.agent_nickname === 'string' ? payload.agent_nickname : nickname;
        role = typeof payload.agent_role === 'string' ? payload.agent_role : role;
        continue;
      }

      if (parsed?.type === 'event_msg' && parsed.payload?.type === 'token_count') {
        const usage = parsed.payload?.info?.total_token_usage ?? parsed.payload?.info?.last_token_usage ?? {};
        const candidate = asNumber(usage.total_tokens);
        if (candidate != null) totalTokens = Math.max(totalTokens, candidate);
      }
    }

    rows.push({
      nickname,
      role,
      tokens_used: totalTokens,
      started_at: startedAt,
      updated_at: updatedAt,
    });
  }

  return rows.filter((row) => row.started_at > 0 || row.tokens_used > 0);
}

async function readLiveRolloutTokenTotals(sandboxPath: string): Promise<GithubTokenTotals | null> {
  const sessionsRoot = join(sandboxPath, '.codex', 'sessions');
  const rolloutFiles = await listRolloutFiles(sessionsRoot);
  if (rolloutFiles.length === 0) return null;

  let inputTokens = 0;
  let outputTokens = 0;
  let totalTokens = 0;

  for (const file of rolloutFiles) {
    const content = await readFile(file, 'utf-8').catch(() => '');
    if (!content.trim()) continue;
    let lastUsage: Record<string, unknown> | null = null;
    for (const line of content.split('\n')) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      let parsed: { type?: string; payload?: any } | null = null;
      try {
        parsed = JSON.parse(trimmed) as { type?: string; payload?: any };
      } catch {
        continue;
      }
      if (parsed?.type === 'event_msg' && parsed.payload?.type === 'token_count') {
        const usage = parsed.payload?.info?.total_token_usage ?? parsed.payload?.info?.last_token_usage ?? null;
        if (usage && typeof usage === 'object') lastUsage = usage as Record<string, unknown>;
      }
    }
    if (!lastUsage) continue;
    inputTokens += asNumber(lastUsage.input_tokens) ?? 0;
    outputTokens += asNumber(lastUsage.output_tokens) ?? 0;
    totalTokens += asNumber(lastUsage.total_tokens) ?? 0;
  }

  if (inputTokens <= 0 && outputTokens <= 0 && totalTokens <= 0) return null;
  return {
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    total_tokens: totalTokens,
  };
}

async function writeThreadUsageArtifact(
  runPaths: GithubRunPaths,
  sandboxPath: string,
  now: Date,
): Promise<GithubThreadUsageArtifact | null> {
  try {
    const rows = await readThreadRowsFromRollouts(sandboxPath);
    const artifact: GithubThreadUsageArtifact = {
      version: 1,
      generated_at: now.toISOString(),
      sandbox_path: sandboxPath,
      rows,
      total_tokens: rows.reduce((sum, row) => sum + row.tokens_used, 0),
    };
    await writeJsonFile(threadUsagePath(runPaths), artifact);
    return artifact;
  } catch {
    return null;
  }
}

function classifyThreadDelivery(role: string): 'reviewer' | 'executor' | 'leader' {
  const normalized = role.trim();
  if (!normalized) return 'leader';
  return AGENT_DEFINITIONS[normalized]?.deliveryClass ?? 'leader';
}

export async function checkGithubWorkonRuntimeConsistency(input: {
  manifest: GithubWorkonManifest;
  runPaths: GithubRunPaths;
}): Promise<GithubRuntimeConsistencyReport> {
  const errors: string[] = [];
  const warnings: string[] = [];
  const concernRegistry = await resolveGithubConcernRegistryDetails(input.manifest.sandbox_repo_path);
  const laneStates = [...(await loadLaneRuntimeStates(input.manifest, input.runPaths)).values()];
  const leaderStatus = await readLeaderStatus(input.runPaths);
  const publisherStatus = await readPublisherStatus(input.runPaths);
  const schedulerState = await readSchedulerStatus(input.runPaths);
  const latestEventId = await readLatestLaneEventId(input.runPaths);
  const passArtifacts = await readSchedulerPassArtifacts(input.runPaths);

  if ((schedulerState?.last_processed_event_id ?? 0) > latestEventId) {
    errors.push(`scheduler last_processed_event_id ${schedulerState?.last_processed_event_id} exceeds latest event id ${latestEventId}`);
  }
  if (leaderStatus?.session_active && !isProcessAlive(leaderStatus.pid)) {
    errors.push('leader session is marked active but pid is not live');
  }
  if (publisherStatus?.session_active && !isProcessAlive(publisherStatus.pid)) {
    errors.push('publisher session is marked active but pid is not live');
  }
  for (const state of laneStates) {
    if (state.status === 'running' && !isProcessAlive(state.pid)) {
      warnings.push(`${state.alias} is marked running but pid is not live`);
    }
    if ((state.status === 'completed' || state.status === 'failed') && !existsSync(state.result_path)) {
      warnings.push(`${state.alias} is ${state.status} but result artifact is missing`);
    }
    if (state.status === 'completed' && state.activation === 'hardening' && !state.completed_concern_match) {
      warnings.push(`${state.alias} completed without persisted concern match metadata`);
    }
  }
  if (publisherStatus?.last_milestone === 'ci_green' && input.manifest.publication_state !== 'ci_green') {
    errors.push('publisher milestone is ci_green but manifest publication_state is not ci_green');
  }
  if (publisherStatus?.blocked && publisherStatus.last_milestone === 'ci_green') {
    errors.push('publisher is blocked despite a ci_green terminal milestone');
  }
  for (const diagnostic of concernRegistry.diagnostics) {
    warnings.push(`concern override diagnostic at ${diagnostic.path}: ${diagnostic.message}`);
  }
  let previousPassId = 0;
  let previousAfterEventId = 0;
  for (const artifact of passArtifacts) {
    if (artifact.pass_id !== previousPassId + 1) {
      errors.push(`scheduler pass artifact sequence has a gap before pass ${artifact.pass_id}`);
    }
    if (artifact.last_processed_event_id_before > artifact.last_processed_event_id_after) {
      errors.push(`scheduler pass ${artifact.pass_id} has a decreasing event watermark`);
    }
    if (artifact.last_processed_event_id_before < previousAfterEventId) {
      errors.push(`scheduler pass ${artifact.pass_id} starts before the prior pass watermark`);
    }
    previousPassId = artifact.pass_id;
    previousAfterEventId = artifact.last_processed_event_id_after;
  }
  if ((schedulerState?.last_completed_pass_id ?? 0) !== previousPassId && previousPassId > 0) {
    errors.push(`scheduler last_completed_pass_id ${schedulerState?.last_completed_pass_id ?? 0} does not match last pass artifact ${previousPassId}`);
  }

  return {
    ok: errors.length === 0,
    errors,
    warnings,
    stats: {
      latest_event_id: latestEventId,
      scheduler_last_processed_event_id: schedulerState?.last_processed_event_id ?? 0,
      scheduler_pass_artifacts: passArtifacts.length,
    },
  };
}

function buildRetrospectiveMarkdown(input: {
  manifest: GithubWorkonManifest;
  sandboxMetadata: SandboxMetadata | null;
  threadRows: GithubThreadRetrospectiveRow[];
  issueStats: GithubIssueTokenStats | null;
  diffStat: string;
  changedFiles: string[];
  laneStates: GithubLaneRuntimeState[];
  leaderStatus: GithubLeaderRuntimeStatus | null;
  publisherStatus: GithubPublisherRuntimeStatus | null;
  schedulerState: GithubSchedulerRuntimeState | null;
  laneEvents: Array<Record<string, unknown> & { id: number }>;
  consistency: GithubRuntimeConsistencyReport;
}): string {
  const totalTokens = input.threadRows.reduce((sum, row) => sum + row.tokens_used, 0);
  const reviewerTokens = input.threadRows
    .filter((row) => classifyThreadDelivery(row.role) === 'reviewer')
    .reduce((sum, row) => sum + row.tokens_used, 0);
  const executorTokens = input.threadRows
    .filter((row) => classifyThreadDelivery(row.role) === 'executor')
    .reduce((sum, row) => sum + row.tokens_used, 0);
  const leaderTokens = input.threadRows
    .filter((row) => classifyThreadDelivery(row.role) === 'leader')
    .reduce((sum, row) => sum + row.tokens_used, 0);
  const architectThreads = input.threadRows.filter((row) => row.role === 'architect').length;
  const changedFileCount = input.changedFiles.length;
  const tokensPerChangedFile = changedFileCount > 0 ? Math.round(totalTokens / changedFileCount) : totalTokens;
  const laneStatusCounts = input.laneStates.reduce<Record<string, number>>((counts, state) => {
    counts[state.status] = (counts[state.status] ?? 0) + 1;
    return counts;
  }, {});
  const invalidatedLanes = input.laneStates.filter((state) => Boolean(state.invalidated_at));
  const totalRetries = input.laneStates.reduce((sum, state) => sum + (state.retry_count ?? 0), 0);
  const failedCategories = [...new Set(input.laneStates.map((state) => state.failure_category).filter(Boolean))];
  const invalidationHistogram = input.laneEvents
    .filter((event) => event.type === 'lane_invalidated')
    .reduce<Record<string, number>>((histogram, event) => {
      const concern = (event.concern_match ?? {}) as { concern_key?: unknown; reasons?: unknown };
      const reasonKinds = Array.isArray(concern.reasons)
        ? [...new Set(concern.reasons
          .map((reason) => typeof reason === 'object' && reason && 'kind' in reason ? String((reason as { kind?: unknown }).kind ?? '') : '')
          .filter(Boolean))]
        : [];
      const label = `${String(concern.concern_key ?? event.alias ?? 'unknown')}/${reasonKinds.join('+') || 'direct'}`;
      histogram[label] = (histogram[label] ?? 0) + 1;
      return histogram;
    }, {});
  const retryHistogram = input.laneEvents
    .filter((event) => event.type === 'lane_retried' || event.type === 'lane_failed')
    .reduce<Record<string, number>>((histogram, event) => {
      const label = String(event.failure_category ?? 'unknown');
      histogram[label] = (histogram[label] ?? 0) + 1;
      return histogram;
    }, {});

  const recommendations: string[] = [];
  const missingFeatures: string[] = [];

  if (architectThreads > 1) {
    recommendations.push('Architect lane respawned multiple times. Cache/reuse the first architectural verdict before spawning another architect review.');
  }
  if (reviewerTokens > executorTokens * 2 && reviewerTokens > 0) {
    recommendations.push('Reviewer token burn dominated execution. Delay hardening reviewers longer or prefer reviewer+executor for narrower specialist loops.');
  }
  if (leaderTokens > reviewerTokens + executorTokens) {
    recommendations.push('Leader token burn exceeded specialist work. Narrow the leader brief and offload more bounded slices earlier.');
  }
  if (tokensPerChangedFile > 500_000) {
    recommendations.push('Token-per-changed-file ratio is high. Add an explicit bootstrap checkpoint and reduce repeated full-file rereads in later lanes.');
  }
  if ((input.manifest.create_pr_on_complete || input.manifest.target_kind === 'pr') && !input.manifest.published_pr_number) {
    missingFeatures.push('Publication did not complete automatically; consider stronger runtime checkpoints around push/PR creation.');
  }
  if ((!input.issueStats || input.issueStats.totals.total_tokens === 0) && totalTokens === 0) {
    missingFeatures.push('Issue-level token totals were unavailable; consider persisting thread-usage totals into the issue stats rollup when session metrics are empty.');
  }
  if ((laneStatusCounts.failed ?? 0) > 0) {
    recommendations.push('One or more lanes failed. Inspect lane-runtime stderr/result artifacts to see whether retries or tighter lane scoping would reduce wasted reruns.');
  }
  if (recommendations.length === 0) {
    recommendations.push('No major orchestration inefficiency heuristics fired. Review changed files vs. token burn manually for domain-specific waste.');
  }

  const lines = [
    '# NANA Work-on Retrospective',
    '',
    `- Target: ${input.manifest.repo_slug} ${input.manifest.target_kind} #${input.manifest.target_number}`,
    `- Run id: ${input.manifest.run_id}`,
    `- Role layout: ${input.manifest.role_layout}`,
    `- Considerations: ${input.manifest.considerations_active.join(', ') || '(none)'}`,
    input.sandboxMetadata?.branch_name ? `- Branch: ${input.sandboxMetadata.branch_name}` : '',
    input.manifest.published_pr_url ? `- Published PR: ${input.manifest.published_pr_url}` : '- Published PR: (none)',
    '',
    '## Outcome',
    '',
    `- Changed files: ${changedFileCount}`,
    `- Diff stat: ${input.diffStat || '(none)'}`,
    `- Total thread tokens: ${totalTokens}`,
    `- Reviewer tokens: ${reviewerTokens}`,
    `- Executor tokens: ${executorTokens}`,
    `- Leader tokens: ${leaderTokens}`,
    `- Lane statuses: ${Object.entries(laneStatusCounts).map(([status, count]) => `${status}=${count}`).join(', ') || '(none)'}`,
    `- Lane retries: ${totalRetries}`,
    `- Lane invalidations: ${invalidatedLanes.length}`,
    failedCategories.length > 0 ? `- Failure categories seen: ${failedCategories.join(', ')}` : '',
    input.leaderStatus ? `- Leader status: bootstrap=${input.leaderStatus.bootstrap_complete} implementation=${input.leaderStatus.implementation_complete} ready_for_publication=${input.leaderStatus.ready_for_publication} blocked=${input.leaderStatus.blocked}${input.leaderStatus.blocked_reason ? ` (${input.leaderStatus.blocked_reason})` : ''}` : '',
    input.publisherStatus ? `- Publisher status: started=${input.publisherStatus.started} pr_opened=${input.publisherStatus.pr_opened} ci_green=${input.publisherStatus.ci_green} blocked=${input.publisherStatus.blocked}${input.publisherStatus.blocked_reason ? ` (${input.publisherStatus.blocked_reason})` : ''} recoveries=${input.publisherStatus.recovery_count ?? 0}` : '',
    input.schedulerState ? `- Scheduler: passes=${input.schedulerState.pass_count} watch=${input.schedulerState.watch_pass_count} poll=${input.schedulerState.poll_pass_count} startup=${input.schedulerState.startup_pass_count} last_wake=${input.schedulerState.last_wake_reason ?? 'n/a'} replays=${input.schedulerState.replay_count ?? 0} recoveries=${input.schedulerState.recovery_count ?? 0}` : '',
    '',
    '## Threads',
    '',
  ].filter(Boolean);

  if (input.threadRows.length === 0) {
    lines.push('- (no thread data available)', '');
  } else {
    for (const row of input.threadRows) {
      const label = row.nickname || row.role || 'leader';
      const delivery = classifyThreadDelivery(row.role);
      const durationSeconds = row.updated_at > row.started_at ? row.updated_at - row.started_at : 0;
      lines.push(`- ${label}: role=${row.role || 'leader'} class=${delivery} tokens=${row.tokens_used} duration_s=${durationSeconds}`);
    }
    lines.push('');
  }

  lines.push('', '## Lane Runtime', '');
  if (input.laneStates.length === 0) {
    lines.push('- (no lane runtime data available)', '');
  } else {
    for (const state of input.laneStates.sort((left, right) => left.alias.localeCompare(right.alias))) {
      lines.push(`- ${state.alias}: role=${state.role} status=${state.status} profile=${state.profile} blocking=${state.blocking} retries=${state.retry_count ?? 0}${state.failure_category ? ` failure=${state.failure_category}` : ''}${state.invalidated_reason ? ` invalidated=${state.invalidated_reason}` : ''}`);
    }
    lines.push('');
  }

  lines.push('## Efficiency Findings', '');
  for (const recommendation of recommendations) {
    lines.push(`- ${recommendation}`);
  }
  lines.push('', '## Missing Features / Angles To Investigate', '');
  for (const feature of missingFeatures) {
    lines.push(`- ${feature}`);
  }

  lines.push(
    '',
    '## Scheduler Summary',
    '',
    input.schedulerState ? `- Last processed event id: ${input.schedulerState.last_processed_event_id}` : '- (no scheduler state available)',
    input.schedulerState?.watch_mode ? `- Wake mode: ${input.schedulerState.watch_mode}` : '',
    input.schedulerState?.blocked_reason ? `- Blocked reason: ${input.schedulerState.blocked_reason}` : '',
    `- Invalidated lanes: ${invalidatedLanes.length}`,
    `- Total lane retries: ${totalRetries}`,
    `- Scheduler replay count: ${input.schedulerState?.replay_count ?? 0}`,
    `- Scheduler recovery count: ${input.schedulerState?.recovery_count ?? 0}`,
    `- Publisher recovery count: ${input.publisherStatus?.recovery_count ?? 0}`,
    `- Scheduler pass artifacts: ${input.consistency.stats.scheduler_pass_artifacts}`,
    `- Invalidation cause histogram: ${Object.entries(invalidationHistogram).map(([label, count]) => `${label}=${count}`).join(', ') || '(none)'}`,
    `- Retry cause histogram: ${Object.entries(retryHistogram).map(([label, count]) => `${label}=${count}`).join(', ') || '(none)'}`,
    '',
    '## Runtime Consistency',
    '',
    `- Status: ${input.consistency.ok ? 'ok' : 'error'}`,
    `- Latest event id: ${input.consistency.stats.latest_event_id}`,
    `- Scheduler processed event id: ${input.consistency.stats.scheduler_last_processed_event_id}`,
    `- Scheduler pass artifacts: ${input.consistency.stats.scheduler_pass_artifacts}`,
    ...input.consistency.errors.map((error) => `- Error: ${error}`),
    ...input.consistency.warnings.map((warning) => `- Warning: ${warning}`),
    '',
    '## Different Angles',
    '',
    '- Planning angle: Did the bootstrap loop lock the minimal feature before the hardening lanes expanded scope?',
    '- Role angle: Did reviewer/executor split increase re-reading and handoff overhead relative to merged lanes?',
    '- Delivery angle: Did PR/CI publication succeed autonomously, or did runtime enforcement still need manual salvage?',
    '- Test angle: Did QA/test-engineer work arrive only after the feature existed, or too early?',
    '- API angle: Did API review happen late enough to avoid over-constraining early implementation while still catching public-contract drift?',
    '',
  );

  return `${lines.join('\n').trim()}\n`;
}

function readPositiveEnvInt(
  env: NodeJS.ProcessEnv,
  key: string,
  fallback: number,
): number {
  const raw = env[key];
  if (!raw) return fallback;
  const parsed = Number(raw);
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : fallback;
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

function sha256Text(input: string): string {
  return createHash('sha256').update(input).digest('hex');
}

async function checksumFile(path: string): Promise<string> {
  const content = await readFile(path, 'utf-8');
  return sha256Text(content);
}

async function extractWorkflowRunCommands(workflowsDir: string): Promise<string[]> {
  if (!existsSync(workflowsDir)) return [];
  const entries = await readdir(workflowsDir, { withFileTypes: true });
  const commands: string[] = [];

  for (const entry of entries) {
    if (!entry.isFile()) continue;
    const path = join(workflowsDir, entry.name);
    const text = await readFile(path, 'utf-8').catch(() => '');
    if (!text) continue;
    const lines = text.split('\n');
    for (let index = 0; index < lines.length; index += 1) {
      const line = lines[index];
      const match = /^(\s*)(?:-\s*)?run:\s*(.*)$/.exec(line);
      if (!match) continue;
      const indent = match[1].length;
      const remainder = match[2].trim();
      if (remainder === '|' || remainder === '>') {
        const block: string[] = [];
        for (let inner = index + 1; inner < lines.length; inner += 1) {
          const next = lines[inner];
          const nextIndent = next.match(/^(\s*)/)?.[1].length ?? 0;
          if (next.trim() !== '' && nextIndent <= indent) break;
          if (next.trim() === '') continue;
          block.push(next.trim());
          index = inner;
        }
        if (block.length > 0) commands.push(block.join('\n'));
      } else if (remainder.length > 0) {
        commands.push(remainder);
      }
    }
  }

  return commands;
}

async function listWorkflowFiles(workflowsDir: string): Promise<string[]> {
  if (!existsSync(workflowsDir)) return [];
  const entries = await readdir(workflowsDir, { withFileTypes: true });
  return entries
    .filter((entry) => entry.isFile())
    .map((entry) => join(workflowsDir, entry.name))
    .sort();
}

async function readMakeTargets(makefilePath: string): Promise<Set<string>> {
  const text = await readFile(makefilePath, 'utf-8').catch(() => '');
  const targets = new Set<string>();
  for (const line of text.split('\n')) {
    const match = /^([A-Za-z0-9_.-]+):/.exec(line);
    if (match?.[1]) targets.add(match[1]);
  }
  return targets;
}

async function readVerificationSourceFiles(
  sourcePaths: readonly string[],
  repoCheckoutPath: string,
  kind: GithubVerificationSourceFile['kind'],
): Promise<GithubVerificationSourceFile[]> {
  const files: GithubVerificationSourceFile[] = [];
  for (const path of sourcePaths) {
    if (!existsSync(path)) continue;
    files.push({
      path: relative(repoCheckoutPath, path),
      checksum: await checksumFile(path),
      kind,
    });
  }
  return files;
}

async function readRepoFileIfPresent(path: string, maxBytes = 64_000): Promise<string> {
  if (!existsSync(path)) return '';
  const text = await readFile(path, 'utf-8').catch(() => '');
  return text.length > maxBytes ? text.slice(0, maxBytes) : text;
}

function hasAnyPathMatch(paths: readonly string[], patterns: readonly RegExp[]): boolean {
  return paths.some((path) => patterns.some((pattern) => pattern.test(path)));
}

function escapeRegExp(input: string): string {
  return input.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

const HOT_PATH_API_OVERRIDE_FILE_CANDIDATES = [
  ['.nana', 'work-on-hot-path-apis.json'],
  ['.github', 'nana-work-on-hot-path-apis.json'],
] as const;

function uniqueSortedStrings(values: readonly string[] | undefined): string[] {
  if (!values || values.length === 0) return [];
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))].sort();
}

async function readRepoHotPathApiProfileOverride(
  repoCheckoutPath: string,
): Promise<{ override: GithubRepoHotPathApiProfileOverride | null; path?: string }> {
  for (const candidate of HOT_PATH_API_OVERRIDE_FILE_CANDIDATES) {
    const path = join(repoCheckoutPath, ...candidate);
    if (!existsSync(path)) continue;
    try {
      const parsed = JSON.parse(await readFile(path, 'utf-8')) as GithubRepoHotPathApiProfileOverride;
      if (!parsed || typeof parsed !== 'object') continue;
      return { override: parsed, path };
    } catch {
      continue;
    }
  }
  return { override: null };
}

function mergeHotPathApiProfileOverride(
  base: GithubRepoHotPathApiProfile,
  override: GithubRepoHotPathApiProfileOverride,
  overridePath: string | undefined,
): GithubRepoHotPathApiProfile {
  const overrideEvidence = overridePath
    ? `repo hot-path api override applied: ${overridePath.replace(/\\/g, '/')}`
    : 'repo hot-path api override applied';
  const replacement = override.replace === true;
  const apiSurfaceFiles = replacement
    ? uniqueSortedStrings(override.api_surface_files)
    : uniqueSortedStrings([...(base.api_surface_files ?? []), ...(override.api_surface_files ?? [])]);
  const hotPathApiFiles = replacement
    ? uniqueSortedStrings(override.hot_path_api_files)
    : uniqueSortedStrings([...(base.hot_path_api_files ?? []), ...(override.hot_path_api_files ?? [])]);
  const apiIdentifierTokens = replacement
    ? uniqueSortedStrings(override.api_identifier_tokens)
    : uniqueSortedStrings([...(base.api_identifier_tokens ?? []), ...(override.api_identifier_tokens ?? [])]);
  const evidence = replacement
    ? uniqueSortedStrings([
      ...(override.evidence ?? []),
      overrideEvidence,
    ])
    : uniqueSortedStrings([
      ...(base.evidence ?? []),
      ...(override.evidence ?? []),
      overrideEvidence,
    ]);
  return {
    ...base,
    api_surface_files: apiSurfaceFiles,
    hot_path_api_files: hotPathApiFiles,
    api_identifier_tokens: apiIdentifierTokens,
    evidence,
  };
}

function extractHotPathApiTokens(input: string): string[] {
  const routeTokens = [...input.matchAll(/\/[a-z0-9._:-]+(?:\/[a-z0-9._:-]+)+/gi)]
    .map((match) => match[0]?.toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim() ?? '')
    .filter(Boolean)
    .flatMap((token) => token.split(/\s+/));
  const operationTokens = [...input.matchAll(/\b(?:operationid|rpc|route|endpoint|handler|controller|service)\s*[:=]?\s*["']?([a-z0-9._:-]+)/gi)]
    .map((match) => match[1]?.toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim() ?? '')
    .filter(Boolean)
    .flatMap((token) => token.split(/\s+/));
  return [...new Set([...routeTokens, ...operationTokens].filter((token) => token.length >= 3))];
}

function looksLikeApiSurfacePath(filePath: string): boolean {
  return /(^|\/)(api|openapi|swagger|proto|grpc|routes?|router|controllers?|handlers?|endpoints?|public)(\/|$)|\.(proto|openapi|swagger)\./i.test(filePath);
}

function containsHotPathApiSignals(text: string): boolean {
  return /\b(hot ?path|critical path|latency|throughput|qps|rps|p95|p99|perf|performance|benchmark|low latency)\b/i.test(text);
}

async function inferRepoHotPathApiProfile(
  repoCheckoutPath: string,
  trackedFiles: readonly string[],
  now: Date,
): Promise<GithubRepoHotPathApiProfile> {
  const apiSurfaceFiles = trackedFiles.filter((path) => looksLikeApiSurfacePath(path));
  const candidateFiles = [
    ...new Set([
      ...apiSurfaceFiles,
      ...trackedFiles.filter((path) => /(^|\/)(docs?|bench(mark)?s?|perf|performance)(\/|$)|README/i.test(path)),
    ]),
  ].slice(0, 64);
  const fileTexts = await Promise.all(candidateFiles.map(async (path) => ({
    path,
    text: await readRepoFileIfPresent(join(repoCheckoutPath, path), 64_000),
  })));
  const hotPathApiFiles = fileTexts
    .filter(({ path, text }) => looksLikeApiSurfacePath(path) && containsHotPathApiSignals(`${path}\n${text}`))
    .map(({ path }) => path)
    .sort();
  const apiIdentifierTokens = [...new Set(fileTexts
    .flatMap(({ path, text }) => extractHotPathApiTokens(`${path}\n${text}`))
    .filter((token) => token.length >= 3))]
    .sort();
  const evidence: string[] = [];
  if (apiSurfaceFiles.length > 0) evidence.push(`api surface files detected: ${apiSurfaceFiles.slice(0, 5).join(', ')}`);
  if (hotPathApiFiles.length > 0) evidence.push(`hot-path api files detected: ${hotPathApiFiles.slice(0, 5).join(', ')}`);
  if (apiIdentifierTokens.length > 0) evidence.push(`api identifier tokens extracted: ${apiIdentifierTokens.slice(0, 8).join(', ')}`);
  return {
    version: 1,
    analyzed_at: now.toISOString(),
    api_surface_files: apiSurfaceFiles.sort(),
    hot_path_api_files: hotPathApiFiles,
    api_identifier_tokens: apiIdentifierTokens,
    evidence,
  };
}

function isPerfLaneAlias(alias: string): boolean {
  return alias === 'perf-coder' || alias === 'perf-reviewer';
}

function hotPathApiProfileFromSettings(
  settings: ManagedRepoSettings | null,
): GithubRepoHotPathApiProfile | undefined {
  return settings?.hot_path_api_profile;
}

async function resolvePerfHotPathConcernMatch(
  manifest: GithubWorkonManifest,
  changedFiles: string[],
): Promise<GithubLaneConcernMatchResult | null> {
  const settings = await readJsonFile<ManagedRepoSettings>(join(manifest.managed_repo_root, 'settings.json'));
  const profile = hotPathApiProfileFromSettings(settings);
  if (!profile) {
    return {
      concern_key: 'performance',
      descriptor_source: 'persisted',
      matched_files: [],
      direct_files: [],
      fallback_files: [],
      unknown_files: [],
      unmatched_files: [...changedFiles].sort(),
      reasons: [],
    };
  }

  const matchedFiles = new Set<string>();
  const directFiles = new Set<string>();
  const fallbackFiles = new Set<string>();
  const unmatchedFiles = new Set<string>();
  const reasons: GithubLaneConcernMatchReason[] = [];
  const hotPathFiles = new Set(profile.hot_path_api_files.map((path) => path.replace(/\\/g, '/').toLowerCase()));
  const apiSurfaceFiles = new Set(profile.api_surface_files.map((path) => path.replace(/\\/g, '/').toLowerCase()));
  const tokenPatterns = profile.api_identifier_tokens.map((token) => new RegExp(`\\b${escapeRegExp(token)}\\b`, 'i'));

  for (const file of changedFiles) {
    const normalized = file.replace(/\\/g, '/').toLowerCase();
    if (hotPathFiles.has(normalized)) {
      matchedFiles.add(file);
      directFiles.add(file);
      reasons.push({
        file,
        kind: 'direct',
        evidence: 'persisted hot-path api file match',
        rule_source: 'persisted',
      });
      continue;
    }
    const content = await readRepoFileIfPresent(join(manifest.sandbox_repo_path, file), 64_000);
    const candidateText = `${file}\n${content}`.toLowerCase();
    const introducesHotPathApi = looksLikeApiSurfacePath(normalized)
      && containsHotPathApiSignals(candidateText);
    const touchesKnownHotPathApi = (looksLikeApiSurfacePath(normalized) || apiSurfaceFiles.has(normalized))
      && tokenPatterns.some((pattern) => pattern.test(candidateText))
      && containsHotPathApiSignals(candidateText);
    if (introducesHotPathApi || touchesKnownHotPathApi) {
      matchedFiles.add(file);
      fallbackFiles.add(file);
      reasons.push({
        file,
        kind: 'fallback',
        evidence: introducesHotPathApi
          ? 'introduced hot-path api surface'
          : 'touches persisted hot-path api identifiers',
        rule_source: 'persisted',
      });
      continue;
    }
    unmatchedFiles.add(file);
  }

  return {
    concern_key: 'performance',
    descriptor_source: 'persisted',
    matched_files: [...matchedFiles].sort(),
    direct_files: [...directFiles].sort(),
    fallback_files: [...fallbackFiles].sort(),
    unknown_files: [],
    unmatched_files: [...unmatchedFiles].sort(),
    reasons,
  };
}

function uniqueCommands(commands: readonly string[]): string[] {
  return [...new Set(commands.map((command) => command.trim()).filter(Boolean))];
}

function commandMatchesUnitTests(command: string): boolean {
  return /make\s+test-unit\b|\bunit\b.*\btest\b|\bmvn\b.*\btest\b|\bnpm\b.*\btest\b|\bpnpm\b.*\btest\b|\byarn\b.*\btest\b|\bcargo\b.*\btest\b/i.test(command)
    && !commandMatchesIntegrationTests(command)
    && !/\b(test-all|demo)\b/i.test(command);
}

function commandMatchesIntegrationTests(command: string): boolean {
  return /\b(test-integration|integration-test|integration tests?|integration\b|itest\b|e2e\b|end-to-end\b|acceptance\b|system test|failsafe)\b/i.test(command);
}

function normalizeVerificationPlanSourceFiles(
  sourceFiles: readonly GithubVerificationSourceFile[],
): GithubVerificationSourceFile[] {
  return [...sourceFiles]
    .sort((left, right) => `${left.kind}:${left.path}`.localeCompare(`${right.kind}:${right.path}`))
    .filter((file, index, files) =>
      files.findIndex((candidate) => candidate.path === file.path && candidate.kind === file.kind) === index);
}

function computeVerificationPlanFingerprint(input: Omit<GithubVerificationPlan, 'plan_fingerprint'>): string {
  return sha256Text(JSON.stringify({
    source: input.source,
    lint: uniqueCommands(input.lint),
    compile: uniqueCommands(input.compile),
    unit: uniqueCommands(input.unit),
    integration: uniqueCommands(input.integration),
    source_files: normalizeVerificationPlanSourceFiles(input.source_files),
  }));
}

function buildVerificationPlan(input: Omit<GithubVerificationPlan, 'plan_fingerprint'>): GithubVerificationPlan {
  const plan = {
    ...input,
    lint: uniqueCommands(input.lint),
    compile: uniqueCommands(input.compile),
    unit: uniqueCommands(input.unit),
    integration: uniqueCommands(input.integration),
    source_files: normalizeVerificationPlanSourceFiles(input.source_files),
  };
  return {
    ...plan,
    plan_fingerprint: computeVerificationPlanFingerprint(plan),
  };
}

function extractScriptCommandCandidates(text: string): string[] {
  return text
    .split('\n')
    .map((line) => line.replace(/#.*/, '').trim())
    .filter(Boolean);
}

function normalizeCommandPathToken(token: string): string | null {
  const normalized = token.trim().replace(/^['"`]+|['"`]+$/g, '').replace(/[;|&]+$/, '');
  if (!normalized || normalized.startsWith('/') || normalized.startsWith('$') || /^[a-z]+:\/\//i.test(normalized)) {
    return null;
  }
  if (normalized.startsWith('./') || normalized.startsWith('../') || normalized.includes('/')) return normalized;
  return null;
}

function extractExplicitVerificationSourceRefs(
  command: string,
): Array<{ token: string; kind: GithubVerificationSourceFile['kind'] }> {
  const refs: Array<{ token: string; kind: GithubVerificationSourceFile['kind'] }> = [];
  const pushMatches = (pattern: RegExp, kind: GithubVerificationSourceFile['kind']): void => {
    for (const match of command.matchAll(pattern)) {
      const token = normalizeCommandPathToken(match[1] ?? '');
      if (token) refs.push({ token, kind });
    }
  };

  pushMatches(/\bmake\s+(?:-f|--file)\s+([^\s"'`]+)/g, 'makefile');
  pushMatches(/\b(?:bash|sh|zsh|python3?|node|ruby|perl)\s+([^\s"'`]+)/g, 'script');
  pushMatches(/(?:^|[;&|]\s*)(\.[/][^\s"'`]+|[A-Za-z0-9_.-]+\/[A-Za-z0-9_./-]+\.(?:sh|bash|zsh|py|js|ts|mjs|cjs|rb|pl))(?:\s|$)/g, 'script');

  return refs;
}

async function collectVerificationSourceFilesFromCommands(
  repoCheckoutPath: string,
  commands: readonly string[],
): Promise<GithubVerificationSourceFile[]> {
  const sourceFiles: GithubVerificationSourceFile[] = [];
  const commandQueue = [...commands];
  const visitedScripts = new Set<string>();

  while (commandQueue.length > 0) {
    const command = commandQueue.shift() || '';
    const maybeAdd = async (path: string, kind: GithubVerificationSourceFile['kind']): Promise<void> => {
      if (!existsSync(path)) return;
      sourceFiles.push(...await readVerificationSourceFiles([path], repoCheckoutPath, kind));
      if (kind !== 'script') return;
      const relativePath = relative(repoCheckoutPath, path);
      if (visitedScripts.has(relativePath)) return;
      visitedScripts.add(relativePath);
      const scriptText = await readFile(path, 'utf-8').catch(() => '');
      if (!scriptText) return;
      commandQueue.push(...extractScriptCommandCandidates(scriptText));
    };

    const makefileOverrideMatch = command.match(/\bmake\s+(?:-f|--file)\s+([^\s"'`]+)/);
    if (makefileOverrideMatch?.[1]) {
      const makefileToken = normalizeCommandPathToken(makefileOverrideMatch[1]);
      if (makefileToken) await maybeAdd(resolve(repoCheckoutPath, makefileToken), 'makefile');
    } else if (/\bmake\b/i.test(command)) {
      await maybeAdd(join(repoCheckoutPath, 'Makefile'), 'makefile');
    }
    if (/\bmvn\b/i.test(command)) {
      await maybeAdd(join(repoCheckoutPath, 'pom.xml'), 'heuristic');
    }
    if (/\b(npm|pnpm|yarn)\b/i.test(command)) {
      await maybeAdd(join(repoCheckoutPath, 'package.json'), 'heuristic');
    }
    if (/\bcargo\b/i.test(command)) {
      await maybeAdd(join(repoCheckoutPath, 'Cargo.toml'), 'heuristic');
    }

    for (const ref of extractExplicitVerificationSourceRefs(command)) {
      await maybeAdd(resolve(repoCheckoutPath, ref.token), ref.kind);
    }
  }

  return normalizeVerificationPlanSourceFiles(sourceFiles);
}

async function expandVerificationCommands(
  repoCheckoutPath: string,
  commands: readonly string[],
): Promise<string[]> {
  const expanded: string[] = [];
  const queue = [...commands];
  const visitedScripts = new Set<string>();

  while (queue.length > 0) {
    const command = (queue.shift() || '').trim();
    if (!command) continue;
    expanded.push(command);
    for (const ref of extractExplicitVerificationSourceRefs(command)) {
      if (ref.kind !== 'script') continue;
      const absolutePath = resolve(repoCheckoutPath, ref.token);
      if (!existsSync(absolutePath)) continue;
      const relativePath = relative(repoCheckoutPath, absolutePath);
      if (visitedScripts.has(relativePath)) continue;
      visitedScripts.add(relativePath);
      const scriptText = await readFile(absolutePath, 'utf-8').catch(() => '');
      if (!scriptText) continue;
      queue.push(...extractScriptCommandCandidates(scriptText));
    }
  }

  return uniqueCommands(expanded);
}

async function detectWorkflowSupportingSourceFiles(
  repoCheckoutPath: string,
  workflowCommands: readonly string[],
): Promise<GithubVerificationSourceFile[]> {
  return collectVerificationSourceFilesFromCommands(repoCheckoutPath, workflowCommands);
}

async function inferInitialRepoConsiderations(
  repoCheckoutPath: string,
  repoSlug: string,
  verificationPlan: GithubVerificationPlan,
): Promise<GithubConsiderationInference> {
  const trackedFilesResult = gitExecAllowFailure(repoCheckoutPath, ['ls-files']);
  const trackedFiles = trackedFilesResult.ok
    ? trackedFilesResult.stdout.split('\n').map((line) => line.trim()).filter(Boolean)
    : [];
  const lowerPaths = trackedFiles.map((path) => path.toLowerCase());
  const readmeText = (
    await Promise.all([
      readRepoFileIfPresent(join(repoCheckoutPath, 'README.md')),
      readRepoFileIfPresent(join(repoCheckoutPath, 'README')),
      readRepoFileIfPresent(join(repoCheckoutPath, 'README.txt')),
      readRepoFileIfPresent(join(repoCheckoutPath, 'pom.xml')),
      readRepoFileIfPresent(join(repoCheckoutPath, 'package.json')),
      readRepoFileIfPresent(join(repoCheckoutPath, 'pyproject.toml')),
      readRepoFileIfPresent(join(repoCheckoutPath, 'build.gradle')),
      readRepoFileIfPresent(join(repoCheckoutPath, 'build.gradle.kts')),
      readRepoFileIfPresent(join(repoCheckoutPath, 'Cargo.toml')),
    ])
  ).join('\n').toLowerCase();
  const slugText = repoSlug.toLowerCase();
  const repoText = `${slugText}\n${readmeText}`;
  const reasons: GithubConsiderationInference['reasons'] = {};
  const inferred = new Set<GithubConsideration>();
  const add = (consideration: GithubConsideration, reason: string): void => {
    inferred.add(consideration);
    reasons[consideration] ??= [];
    if (!reasons[consideration]!.includes(reason)) reasons[consideration]!.push(reason);
  };

  if (hasAnyPathMatch(lowerPaths, [
    /(^|\/)package\.json$/,
    /(^|\/)pnpm-lock\.ya?ml$/,
    /(^|\/)yarn\.lock$/,
    /(^|\/)package-lock\.json$/,
    /(^|\/)pom\.xml$/,
    /(^|\/)build\.gradle(\.kts)?$/,
    /(^|\/)gradle\/libs\.versions\.toml$/,
    /(^|\/)cargo\.toml$/,
    /(^|\/)go\.mod$/,
    /(^|\/)pyproject\.toml$/,
    /(^|\/)requirements(\.[^.\/]+)?\.txt$/,
    /(^|\/)gemfile$/,
  ])) {
    add('dependency', 'dependency manifests detected');
  }

  if (
    verificationPlan.lint.length > 0
    || hasAnyPathMatch(lowerPaths, [
      /(^|\/)\.eslintrc/,
      /(^|\/)eslint\.config\./,
      /(^|\/)biome\.json$/,
      /(^|\/)prettier\.config\./,
      /(^|\/)\.prettierrc/,
      /(^|\/)checkstyle\.xml$/,
      /(^|\/)\.editorconfig$/,
      /(^|\/)ruff\.toml$/,
      /(^|\/)detekt\.ya?ml$/,
      /(^|\/)spotless\./,
      /(^|\/)\.clang-format$/,
    ])
  ) {
    add('style', verificationPlan.lint.length > 0 ? 'lint or style verification commands detected' : 'style/lint configuration files detected');
  }

  if (
    /\b(client|sdk|library|plugin|extension|driver|public api|backward compatibility|semver)\b/.test(repoText)
    || hasAnyPathMatch(lowerPaths, [
      /(^|\/)src\/main\//,
      /(^|\/)include\//,
      /(^|\/)lib\//,
      /(^|\/)api\//,
    ])
  ) {
    add('api', 'library or API-facing repository signals detected');
  }

  const moduleManifestCount = lowerPaths.filter((path) =>
    /(^|\/)(pom\.xml|build\.gradle(\.kts)?|package\.json|cargo\.toml|go\.mod|pyproject\.toml)$/.test(path)).length;
  if (
    /\b(architecture|design|protocol|schema|serialization|concurrency|distributed|module|multi-module|adrs?)\b/.test(repoText)
    || hasAnyPathMatch(lowerPaths, [
      /(^|\/)docs\/architecture/i,
      /(^|\/)architecture\.(md|txt)$/i,
      /(^|\/)adrs?\//i,
      /(^|\/)docs\/adrs?\//i,
    ])
    || moduleManifestCount > 1
  ) {
    add('arch', moduleManifestCount > 1 ? 'multi-module or architecture-focused repository signals detected' : 'architecture or design signals detected');
  }

  if (
    /\b(auth|oauth|jwt|token|credential|secret|tls|ssl|certificate|encrypt|decrypt|signature|signing|iam|https?|http client)\b/.test(repoText)
    || /\b(client|sdk|driver)\b/.test(slugText)
  ) {
    add('security', 'security-sensitive or network-facing repository signals detected');
  }

  if (
    /\b(perf|performance|benchmark|throughput|latency|cache|pooling|jmh|criterion|microbench)\b/.test(repoText)
    || hasAnyPathMatch(lowerPaths, [
      /(^|\/)bench(mark)?s?\//,
      /(^|\/)jmh\//,
      /(^|\/).*benchmark.*$/,
      /(^|\/).*perf.*$/,
    ])
  ) {
    add('perf', 'performance or benchmark signals detected');
  }

  if (
    verificationPlan.unit.length > 0
    || verificationPlan.integration.length > 0
    || hasAnyPathMatch(lowerPaths, [
      /(^|\/)src\/test\//,
      /(^|\/)test\//,
      /(^|\/)tests\//,
      /(^|\/)integration-tests?\//,
      /(^|\/)__tests__\//,
    ])
  ) {
    add('qa', verificationPlan.integration.length > 0 ? 'integration or unit verification commands detected' : 'test directories or unit verification commands detected');
  }

  return {
    considerations: (['arch', 'dependency', 'api', 'perf', 'style', 'security', 'qa'] as const).filter((consideration) => inferred.has(consideration)),
    reasons,
  };
}

function summarizeTestSuiteHistory(
  suite: GithubTestSuiteKind,
  samples: readonly GithubUnitTestSample[],
  planFingerprint: string,
): Omit<GithubTestSuitePolicySummary, 'mode' | 'source'> {
  const matchingSamples = samples.filter((sample) => sample.plan_fingerprint === planFingerprint).slice(-10);
  const passingSamples = matchingSamples.filter((sample) => sample.status === 'pass');
  const failingSamples = matchingSamples.filter((sample) => sample.status === 'fail');
  const averageDurationMs = passingSamples.length > 0
    ? Math.round(passingSamples.reduce((sum, sample) => sum + sample.duration_ms, 0) / passingSamples.length)
    : null;

  return {
    suite,
    sample_count: matchingSamples.length,
    passing_sample_count: passingSamples.length,
    failing_sample_count: failingSamples.length,
    average_duration_ms: averageDurationMs,
    plan_fingerprint: planFingerprint,
  };
}

function classifyWorkflowJobTestSuite(jobName: string): GithubTestSuiteKind | null {
  const normalized = jobName.toLowerCase();
  if (/(integration|itest|e2e|end-to-end|acceptance|system test|failsafe)/.test(normalized)) return 'integration';
  if (/(unit|test|surefire|pytest|jest|vitest|mocha)/.test(normalized)) return 'unit';
  return null;
}

function parseDurationMs(startedAt?: string | null, completedAt?: string | null): number | null {
  if (!startedAt || !completedAt) return null;
  const started = Date.parse(startedAt);
  const completed = Date.parse(completedAt);
  if (!Number.isFinite(started) || !Number.isFinite(completed) || completed < started) return null;
  return completed - started;
}

async function readCiSuiteDurations(path: string): Promise<GithubCiSuiteDurations | null> {
  return readJsonFile<GithubCiSuiteDurations>(path);
}

async function writeCiSuiteDurations(path: string, data: GithubCiSuiteDurations): Promise<void> {
  await writeJsonFile(path, data);
}

function buildCiSuiteDurationsFromJobs(
  planFingerprint: string,
  jobs: readonly GithubWorkflowJobPayload[],
  now: Date,
): GithubCiSuiteDurations {
  const durations: Record<GithubTestSuiteKind, number[]> = { unit: [], integration: [] };
  for (const job of jobs) {
    if (!isSuccessfulConclusion(job.conclusion)) continue;
    const suite = classifyWorkflowJobTestSuite(job.name);
    if (!suite) continue;
    const durationMs = parseDurationMs(job.started_at, job.completed_at);
    if (durationMs != null) durations[suite].push(durationMs);
  }

  return {
    version: 1,
    updated_at: now.toISOString(),
    plan_fingerprint: planFingerprint,
    suites: {
      unit: {
        average_duration_ms: durations.unit.length > 0 ? Math.round(durations.unit.reduce((sum, value) => sum + value, 0) / durations.unit.length) : null,
        sample_count: durations.unit.length,
      },
      integration: {
        average_duration_ms: durations.integration.length > 0 ? Math.round(durations.integration.reduce((sum, value) => sum + value, 0) / durations.integration.length) : null,
        sample_count: durations.integration.length,
      },
    },
  };
}

async function inferCiSuiteDurationsFromRecentRuns(
  repoSlug: string,
  branch: string,
  context: GithubApiContext,
  planFingerprint: string,
): Promise<GithubCiSuiteDurations | null> {
  try {
    const response = await githubApiJson<GithubWorkflowRunsResponse>(
      `/repos/${repoSlug}/actions/runs?branch=${encodeURIComponent(branch)}&status=completed&per_page=10`,
      context,
    );
    const successfulRuns = (response.workflow_runs ?? []).filter((run) => run.conclusion === 'success').slice(0, 5);
    if (successfulRuns.length === 0) return null;
    const jobs = (await Promise.all(successfulRuns.map((run) =>
      fetchWorkflowJobsForRun(repoSlug, run.id, context).catch(() => [] as GithubWorkflowJobPayload[]),
    ))).flat();
    return buildCiSuiteDurationsFromJobs(planFingerprint, jobs, new Date());
  } catch {
    return null;
  }
}

function resolveSuiteExecutionMode(
  suite: GithubTestSuiteKind,
  commands: readonly string[],
  localHistory: Omit<GithubTestSuitePolicySummary, 'mode' | 'source'>,
  ciDurations: GithubCiSuiteDurations | null,
  prMode: boolean,
  everyIterationThresholdMs: number,
  ciOnlyThresholdMs: number,
): GithubTestSuitePolicySummary {
  if (commands.length === 0) {
    return {
      suite,
      mode: 'none',
      sample_count: 0,
      passing_sample_count: 0,
      failing_sample_count: 0,
      average_duration_ms: null,
      source: 'none',
      plan_fingerprint: localHistory.plan_fingerprint,
    };
  }

  const ciSummary = ciDurations?.plan_fingerprint === localHistory.plan_fingerprint
    ? ciDurations.suites[suite]
    : undefined;
  const hasCi = Boolean(ciSummary && ciSummary.average_duration_ms != null && ciSummary.sample_count > 0);
  const hasLocal = localHistory.average_duration_ms != null && localHistory.sample_count > 0;
  const durationMs = hasCi
    ? ciSummary!.average_duration_ms
    : hasLocal
      ? localHistory.average_duration_ms
      : null;

  let mode: GithubTestExecutionMode;
  if (durationMs == null) {
    mode = suite === 'unit' ? 'unknown' : 'final-only';
  } else if (durationMs > ciOnlyThresholdMs) {
    mode = prMode ? 'ci-only' : 'final-only';
  } else if (durationMs > everyIterationThresholdMs) {
    mode = 'final-only';
  } else {
    mode = 'every-iteration';
  }

  return {
    suite,
    mode,
    sample_count: hasCi ? ciSummary!.sample_count : localHistory.sample_count,
    passing_sample_count: localHistory.passing_sample_count,
    failing_sample_count: localHistory.failing_sample_count,
    average_duration_ms: durationMs,
    source: hasCi && hasLocal ? 'local+ci' : hasCi ? 'ci' : hasLocal ? 'local' : 'none',
    plan_fingerprint: localHistory.plan_fingerprint,
  };
}

async function readUnitTestSamples(path: string): Promise<GithubUnitTestSample[]> {
  const text = await readFile(path, 'utf-8').catch(() => '');
  if (!text.trim()) return [];
  const samples: GithubUnitTestSample[] = [];
  for (const line of text.split('\n')) {
    const [recordedAt, sandboxId, durationMsRaw, statusRaw, planFingerprint] = line.trim().split('\t');
    if (!recordedAt || !sandboxId || !durationMsRaw || !statusRaw || !planFingerprint) continue;
    const durationMs = Number.parseInt(durationMsRaw, 10);
    if (!Number.isFinite(durationMs) || durationMs < 0) continue;
    if (statusRaw !== 'pass' && statusRaw !== 'fail') continue;
    samples.push({
      recorded_at: recordedAt,
      sandbox_id: sandboxId,
      duration_ms: durationMs,
      status: statusRaw,
      plan_fingerprint: planFingerprint,
    });
  }
  return samples;
}

async function writeVerificationPolicyArtifacts(
  sandboxPath: string,
  plan: GithubVerificationPlan,
  everyIterationThresholdMs: number,
  ciOnlyThresholdMs: number,
  repoUnitHistoryPath: string,
  repoCiSuiteDurationsPath: string,
  prMode: boolean,
): Promise<{ unit: GithubTestSuitePolicySummary; integration: GithubTestSuitePolicySummary }> {
  const verificationDir = sandboxVerificationDir(sandboxPath);
  await mkdir(verificationDir, { recursive: true });
  const sandboxHistoryPath = sandboxVerificationUnitHistoryPath(sandboxPath);
  const repoSamples = await readUnitTestSamples(repoUnitHistoryPath);
  const sandboxSamples = await readUnitTestSamples(sandboxHistoryPath);
  const localHistory = summarizeTestSuiteHistory('unit', [...repoSamples, ...sandboxSamples], plan.plan_fingerprint);
  const ciDurations = await readCiSuiteDurations(repoCiSuiteDurationsPath);
  const unitPolicy = resolveSuiteExecutionMode(
    'unit',
    plan.unit,
    localHistory,
    ciDurations,
    prMode,
    everyIterationThresholdMs,
    ciOnlyThresholdMs,
  );
  const integrationPolicy = resolveSuiteExecutionMode(
    'integration',
    plan.integration,
    { ...localHistory, suite: 'integration' },
    ciDurations,
    prMode,
    everyIterationThresholdMs,
    ciOnlyThresholdMs,
  );

  await writeFile(sandboxVerificationPolicyPath(sandboxPath), [
    `NANA_WORKON_UNIT_TESTS_MODE=${unitPolicy.mode}`,
    `NANA_WORKON_UNIT_TESTS_SAMPLE_COUNT=${unitPolicy.sample_count}`,
    `NANA_WORKON_UNIT_TESTS_PASSING_SAMPLE_COUNT=${unitPolicy.passing_sample_count}`,
    `NANA_WORKON_UNIT_TESTS_FAILING_SAMPLE_COUNT=${unitPolicy.failing_sample_count}`,
    `NANA_WORKON_UNIT_TESTS_AVERAGE_DURATION_MS=${unitPolicy.average_duration_ms ?? ''}`,
    `NANA_WORKON_UNIT_TESTS_PLAN_FINGERPRINT=${unitPolicy.plan_fingerprint}`,
    `NANA_WORKON_UNIT_TESTS_SOURCE=${unitPolicy.source}`,
  ].join('\n').trim() + '\n', 'utf-8');

  await writeFile(sandboxVerificationIntegrationPolicyPath(sandboxPath), [
    `NANA_WORKON_INTEGRATION_TESTS_MODE=${integrationPolicy.mode}`,
    `NANA_WORKON_INTEGRATION_TESTS_SAMPLE_COUNT=${integrationPolicy.sample_count}`,
    `NANA_WORKON_INTEGRATION_TESTS_AVERAGE_DURATION_MS=${integrationPolicy.average_duration_ms ?? ''}`,
    `NANA_WORKON_INTEGRATION_TESTS_PLAN_FINGERPRINT=${integrationPolicy.plan_fingerprint}`,
    `NANA_WORKON_INTEGRATION_TESTS_SOURCE=${integrationPolicy.source}`,
  ].join('\n').trim() + '\n', 'utf-8');

  return { unit: unitPolicy, integration: integrationPolicy };
}

async function appendVerificationDriftEvent(
  repoPaths: ManagedRepoPaths,
  sandboxPath: string,
  event: GithubVerificationDriftEvent,
): Promise<void> {
  const line = `${JSON.stringify(event)}\n`;
  await mkdir(dirname(repoPaths.repoVerificationDriftLogPath), { recursive: true });
  await mkdir(sandboxVerificationDir(sandboxPath), { recursive: true });
  await writeFile(repoPaths.repoVerificationDriftLogPath, line, { encoding: 'utf-8', flag: 'a' });
  await writeFile(sandboxVerificationDriftLogPath(sandboxPath), line, { encoding: 'utf-8', flag: 'a' });
}

function diffVerificationSourceFiles(
  beforeFiles: readonly GithubVerificationSourceFile[],
  afterFiles: readonly GithubVerificationSourceFile[],
): GithubVerificationDriftSourceChange[] {
  const beforeMap = new Map(beforeFiles.map((file) => [`${file.kind}:${file.path}`, file]));
  const afterMap = new Map(afterFiles.map((file) => [`${file.kind}:${file.path}`, file]));
  const keys = [...new Set([...beforeMap.keys(), ...afterMap.keys()])].sort();
  const changes: GithubVerificationDriftSourceChange[] = [];
  for (const key of keys) {
    const before = beforeMap.get(key);
    const after = afterMap.get(key);
    if (before && after && before.checksum === after.checksum) continue;
    changes.push({
      path: after?.path ?? before?.path ?? key,
      kind: after?.kind ?? before?.kind ?? 'heuristic',
      change: !before ? 'added' : !after ? 'removed' : 'modified',
      before_checksum: before?.checksum,
      after_checksum: after?.checksum,
    });
  }
  return changes;
}

export async function detectVerificationPlan(repoCheckoutPath: string): Promise<GithubVerificationPlan> {
  const workflowsDir = join(repoCheckoutPath, '.github', 'workflows');
  const workflowCommands = await extractWorkflowRunCommands(workflowsDir);
  const effectiveWorkflowCommands = await expandVerificationCommands(repoCheckoutPath, workflowCommands);
  const workflowFiles = await listWorkflowFiles(workflowsDir);
  const makefilePath = join(repoCheckoutPath, 'Makefile');
  const makeTargets = await readMakeTargets(makefilePath);

  const workflowLint = effectiveWorkflowCommands.filter((command) => /\bmake\s+lint\b|\blint\b/i.test(command));
  const workflowCompile = effectiveWorkflowCommands.filter((command) =>
    /\bmake\s+compile(-test|-demo)?\b|\bcompile\b/i.test(command)
    && !commandMatchesUnitTests(command)
    && !commandMatchesIntegrationTests(command));
  const workflowUnit = effectiveWorkflowCommands.filter((command) => commandMatchesUnitTests(command));
  const workflowIntegration = effectiveWorkflowCommands.filter((command) => commandMatchesIntegrationTests(command));
  if (workflowLint.length > 0 || workflowCompile.length > 0 || workflowUnit.length > 0 || workflowIntegration.length > 0) {
    const sourceFiles = [
      ...await readVerificationSourceFiles(workflowFiles, repoCheckoutPath, 'workflow'),
      ...await detectWorkflowSupportingSourceFiles(repoCheckoutPath, workflowCommands),
    ];
    return buildVerificationPlan({
      source: 'workflow',
      lint: workflowLint,
      compile: workflowCompile,
      unit: workflowUnit,
      integration: workflowIntegration,
      source_files: sourceFiles,
    });
  }

  if (existsSync(makefilePath)) {
    const lint = makeTargets.has('lint') ? ['make lint'] : [];
    const compile = [
      ...(makeTargets.has('compile') ? ['make compile'] : []),
      ...(makeTargets.has('compile-test') ? ['make compile-test'] : []),
      ...(makeTargets.has('compile-demo') ? ['make compile-demo'] : []),
    ];
    const unit = makeTargets.has('test-unit') ? ['make test-unit'] : [];
    const integration = [
      ...(makeTargets.has('test-integration') ? ['make test-integration'] : []),
      ...(makeTargets.has('integration-test') ? ['make integration-test'] : []),
      ...(makeTargets.has('itest') ? ['make itest'] : []),
    ];
    const makefileSourceFiles = await readVerificationSourceFiles([makefilePath], repoCheckoutPath, 'makefile');
    if (lint.length > 0 || compile.length > 0 || unit.length > 0 || integration.length > 0) {
      return buildVerificationPlan({
        source: 'makefile',
        lint,
        compile,
        unit,
        integration,
        source_files: makefileSourceFiles,
      });
    }
  }

  if (existsSync(join(repoCheckoutPath, 'pom.xml'))) {
    const pomPath = join(repoCheckoutPath, 'pom.xml');
    const sourceFiles = await readVerificationSourceFiles([pomPath], repoCheckoutPath, 'heuristic');
    return buildVerificationPlan({
      source: 'heuristic',
      lint: [],
      compile: ['mvn -q -DskipTests compile', 'mvn -q test-compile'],
      unit: ['mvn -q test'],
      integration: [],
      source_files: sourceFiles,
    });
  }

  if (existsSync(join(repoCheckoutPath, 'package.json'))) {
    const packageJsonPath = join(repoCheckoutPath, 'package.json');
    const sourceFiles = await readVerificationSourceFiles([packageJsonPath], repoCheckoutPath, 'heuristic');
    return buildVerificationPlan({
      source: 'heuristic',
      lint: ['npm run lint --if-present'],
      compile: ['npm run build --if-present'],
      unit: ['npm test -- --runInBand'],
      integration: ['npm run test:integration --if-present'],
      source_files: sourceFiles,
    });
  }

  if (existsSync(join(repoCheckoutPath, 'Cargo.toml'))) {
    const cargoTomlPath = join(repoCheckoutPath, 'Cargo.toml');
    const sourceFiles = await readVerificationSourceFiles([cargoTomlPath], repoCheckoutPath, 'heuristic');
    return buildVerificationPlan({
      source: 'heuristic',
      lint: ['cargo fmt --check', 'cargo clippy --all-targets -- -D warnings'],
      compile: ['cargo check'],
      unit: ['cargo test --lib'],
      integration: [],
      source_files: sourceFiles,
    });
  }

  return buildVerificationPlan({
    source: 'heuristic',
    lint: [],
    compile: [],
    unit: [],
    integration: [],
    source_files: [],
  });
}

export async function writeVerificationScripts(
  sandboxPath: string,
  repoCheckoutPath: string,
  plan: GithubVerificationPlan,
  runId: string,
  options: {
    managedRepoRoot: string;
    sandboxId: string;
    prMode: boolean;
    env?: NodeJS.ProcessEnv;
  },
): Promise<string> {
  const dir = sandboxVerificationDir(sandboxPath);
  await mkdir(dir, { recursive: true });
  const env = options.env ?? process.env;
  const everyIterationThresholdMs = readPositiveEnvInt(env, 'NANA_WORKON_EVERY_ITERATION_TEST_THRESHOLD_MS', 180_000);
  const ciOnlyThresholdMs = readPositiveEnvInt(env, 'NANA_WORKON_LOCAL_FINAL_TEST_THRESHOLD_MS', 900_000);
  const repoUnitHistoryPath = join(options.managedRepoRoot, 'verification-unit-history.tsv');
  const sandboxUnitHistoryPath = sandboxVerificationUnitHistoryPath(sandboxPath);
  const suitePolicies = await writeVerificationPolicyArtifacts(
    sandboxPath,
    plan,
    everyIterationThresholdMs,
    ciOnlyThresholdMs,
    repoUnitHistoryPath,
    join(options.managedRepoRoot, 'verification-ci-suite-durations.json'),
    options.prMode,
  );
  const writeScript = async (name: string, bodyLines: string[]): Promise<void> => {
    const path = join(dir, name);
    await writeFile(path, `${bodyLines.join('\n').trim()}\n`, 'utf-8');
    await chmod(path, 0o755);
  };

  const buildCommandScript = (commands: readonly string[], emptyMessage: string): string[] => [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    `cd ${shellQuote(repoCheckoutPath)}`,
    ...(commands.length > 0 ? commands : [`echo ${shellQuote(emptyMessage)}`]),
  ];

  await writeScript('lint.sh', buildCommandScript(plan.lint, 'No lint command detected for this repo.'));
  await writeScript('compile.sh', buildCommandScript(plan.compile, 'No compile command detected for this repo.'));
  await writeScript('unit-tests.sh', buildCommandScript(plan.unit, 'No unit-test command detected for this repo.'));
  await writeScript('integration-tests.sh', buildCommandScript(plan.integration, 'No integration-test command detected for this repo.'));

  await writeScript('all.sh', [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    `DIR=${shellQuote(dir)}`,
    `UNIT_POLICY_FILE=${shellQuote(sandboxVerificationPolicyPath(sandboxPath))}`,
    `INTEGRATION_POLICY_FILE=${shellQuote(sandboxVerificationIntegrationPolicyPath(sandboxPath))}`,
    '"$DIR/refresh.sh"',
    'if [[ -f "$UNIT_POLICY_FILE" ]]; then source "$UNIT_POLICY_FILE"; fi',
    'if [[ -f "$INTEGRATION_POLICY_FILE" ]]; then source "$INTEGRATION_POLICY_FILE"; fi',
    '"$DIR/lint.sh"',
    '"$DIR/compile.sh"',
    'if [[ "${NANA_WORKON_UNIT_TESTS_MODE:-unknown}" != "ci-only" && "${NANA_WORKON_UNIT_TESTS_MODE:-unknown}" != "none" ]]; then',
    '  "$DIR/unit-tests.sh"',
    'else',
    '  echo "Skipping local unit tests at final gate because mode=${NANA_WORKON_UNIT_TESTS_MODE:-unknown}."',
    'fi',
    'if [[ "${NANA_WORKON_INTEGRATION_TESTS_MODE:-none}" != "ci-only" && "${NANA_WORKON_INTEGRATION_TESTS_MODE:-none}" != "none" ]]; then',
    '  "$DIR/integration-tests.sh"',
    'else',
    '  echo "Skipping local integration tests at final gate because mode=${NANA_WORKON_INTEGRATION_TESTS_MODE:-none}."',
    'fi',
  ]);

  await writeScript('worker-done.sh', [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    `DIR=${shellQuote(dir)}`,
    `UNIT_POLICY_FILE=${shellQuote(sandboxVerificationPolicyPath(sandboxPath))}`,
    `INTEGRATION_POLICY_FILE=${shellQuote(sandboxVerificationIntegrationPolicyPath(sandboxPath))}`,
    `REPO_HISTORY_FILE=${shellQuote(repoUnitHistoryPath)}`,
    `SANDBOX_HISTORY_FILE=${shellQuote(sandboxUnitHistoryPath)}`,
    `PLAN_FINGERPRINT=${shellQuote(plan.plan_fingerprint)}`,
    `SANDBOX_ID=${shellQuote(options.sandboxId)}`,
    '"$DIR/refresh.sh"',
    'if [[ -f "$UNIT_POLICY_FILE" ]]; then source "$UNIT_POLICY_FILE"; fi',
    'if [[ -f "$INTEGRATION_POLICY_FILE" ]]; then source "$INTEGRATION_POLICY_FILE"; fi',
    '"$DIR/lint.sh"',
    '"$DIR/compile.sh"',
    'if [[ ! -x "$DIR/unit-tests.sh" ]]; then exit 0; fi',
    'UNIT_MODE="${NANA_WORKON_UNIT_TESTS_MODE:-unknown}"',
    'INTEGRATION_MODE="${NANA_WORKON_INTEGRATION_TESTS_MODE:-none}"',
    'if [[ "$UNIT_MODE" != "every-iteration" && "$UNIT_MODE" != "unknown" ]]; then',
    '  echo "Skipping unit tests on worker completion because mode=$UNIT_MODE."',
    'else',
    '  START_MS=$(date +%s%3N)',
    '  STATUS_LABEL=pass',
    '  if ! "$DIR/unit-tests.sh"; then STATUS_LABEL=fail; STATUS=$?; else STATUS=0; fi',
    '  END_MS=$(date +%s%3N)',
    '  DURATION_MS=$((END_MS - START_MS))',
    '  RECORDED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)',
    '  printf "%s\t%s\t%s\t%s\t%s\n" "$RECORDED_AT" "$SANDBOX_ID" "$DURATION_MS" "$STATUS_LABEL" "$PLAN_FINGERPRINT" >>"$SANDBOX_HISTORY_FILE"',
    '  mkdir -p "$(dirname "$REPO_HISTORY_FILE")"',
    '  printf "%s\t%s\t%s\t%s\t%s\n" "$RECORDED_AT" "$SANDBOX_ID" "$DURATION_MS" "$STATUS_LABEL" "$PLAN_FINGERPRINT" >>"$REPO_HISTORY_FILE"',
    '  if [[ "$STATUS" -ne 0 ]]; then exit "$STATUS"; fi',
    'fi',
    'if [[ "$INTEGRATION_MODE" == "every-iteration" ]]; then',
    '  "$DIR/integration-tests.sh"',
    'else',
    '  echo "Skipping integration tests on worker completion because mode=$INTEGRATION_MODE."',
    'fi',
  ]);

  await writeScript('refresh.sh', [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    `cd ${shellQuote(sandboxPath)}`,
    `${shellQuote(process.execPath)} ${shellQuote(join(getPackageRoot(), 'dist', 'cli', 'nana.js'))} work-on verify-refresh --run-id ${shellQuote(runId)}`,
  ]);

  await writeJsonFile(join(dir, 'plan.json'), {
    version: 1,
    repo_checkout_path: repoCheckoutPath,
    ...plan,
    every_iteration_threshold_ms: everyIterationThresholdMs,
    ci_only_threshold_ms: ciOnlyThresholdMs,
    unit_policy: suitePolicies.unit,
    integration_policy: suitePolicies.integration,
    scripts: {
      lint: join(dir, 'lint.sh'),
      compile: join(dir, 'compile.sh'),
      unit: join(dir, 'unit-tests.sh'),
      integration: join(dir, 'integration-tests.sh'),
      unit_policy: sandboxVerificationPolicyPath(sandboxPath),
      integration_policy: sandboxVerificationIntegrationPolicyPath(sandboxPath),
      refresh: join(dir, 'refresh.sh'),
      worker_done: join(dir, 'worker-done.sh'),
      all: join(dir, 'all.sh'),
    },
  });

  return dir;
}

async function writeRepoVerificationPlan(
  paths: ManagedRepoPaths,
  repoCheckoutPath: string,
  plan: GithubVerificationPlan,
): Promise<void> {
  await writeJsonFile(repoVerificationPlanPath(paths), {
    version: 1,
    repo_checkout_path: repoCheckoutPath,
    ...plan,
  });
}

async function verificationPlanHasDrift(
  repoCheckoutPath: string,
  plan: GithubVerificationPlan | undefined,
): Promise<boolean> {
  if (!plan || plan.source_files.length === 0) return true;
  for (const sourceFile of plan.source_files) {
    const absolutePath = join(repoCheckoutPath, sourceFile.path);
    if (!existsSync(absolutePath)) return true;
    const currentChecksum = await checksumFile(absolutePath).catch(() => '');
    if (currentChecksum !== sourceFile.checksum) return true;
  }
  return false;
}

async function ensureVerificationArtifactsCurrent(
  manifest: GithubWorkonManifest,
  repoPaths: ManagedRepoPaths,
  runPaths: GithubRunPaths,
  env: NodeJS.ProcessEnv,
): Promise<GithubWorkonManifest> {
  const repoBaselinePlan = await detectVerificationPlan(repoPaths.sourcePath);
  await writeRepoVerificationPlan(repoPaths, repoPaths.sourcePath, repoBaselinePlan);

  const drifted = await verificationPlanHasDrift(manifest.sandbox_repo_path, manifest.verification_plan);
  const scriptsMissing = !manifest.verification_scripts_dir || !existsSync(join(manifest.verification_scripts_dir, 'all.sh'));
  const previousPlan = manifest.verification_plan;
  let currentPlan = previousPlan;
  if (drifted || scriptsMissing || !currentPlan) {
    currentPlan = await detectVerificationPlan(manifest.sandbox_repo_path);
  }

  const verificationScriptsDir = await writeVerificationScripts(
    manifest.sandbox_path,
    manifest.sandbox_repo_path,
    currentPlan,
    manifest.run_id,
    {
      managedRepoRoot: repoPaths.repoRoot,
      sandboxId: manifest.sandbox_id,
      prMode: manifest.create_pr_on_complete || manifest.target_kind === 'pr',
      env,
    },
  );
  if (drifted || scriptsMissing || !previousPlan) {
    const driftEvent: GithubVerificationDriftEvent = {
      recorded_at: new Date().toISOString(),
      run_id: manifest.run_id,
      sandbox_id: manifest.sandbox_id,
      reason: !previousPlan ? 'initial-bootstrap' : drifted ? 'plan-drift' : 'scripts-missing',
      before_fingerprint: previousPlan?.plan_fingerprint,
      after_fingerprint: currentPlan.plan_fingerprint,
      changed_sources: diffVerificationSourceFiles(previousPlan?.source_files ?? [], currentPlan.source_files),
    };
    await appendVerificationDriftEvent(repoPaths, manifest.sandbox_path, driftEvent);
  }
  const updatedManifest: GithubWorkonManifest = {
    ...manifest,
    verification_plan: currentPlan,
    verification_scripts_dir: verificationScriptsDir,
  };
  await writeManifest(runPaths, updatedManifest);
  return updatedManifest;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function fetchCheckRunsForSha(
  repoSlug: string,
  sha: string,
  context: GithubApiContext,
): Promise<GithubCheckRunPayload[]> {
  const encodedSha = encodeURIComponent(sha);
  const response = await githubApiJson<GithubCheckRunsResponse>(
    `/repos/${repoSlug}/commits/${encodedSha}/check-runs?per_page=100`,
    context,
  );
  return response.check_runs ?? [];
}

async function fetchWorkflowRunsForSha(
  repoSlug: string,
  sha: string,
  context: GithubApiContext,
): Promise<GithubWorkflowRunPayload[]> {
  const encodedSha = encodeURIComponent(sha);
  const response = await githubApiJson<GithubWorkflowRunsResponse>(
    `/repos/${repoSlug}/actions/runs?head_sha=${encodedSha}&per_page=100`,
    context,
  );
  return response.workflow_runs ?? [];
}

function isSuccessfulConclusion(conclusion: string | null | undefined): boolean {
  return conclusion === 'success' || conclusion === 'neutral' || conclusion === 'skipped';
}

export function classifyGithubCiFailureEvidence(
  jobName: string,
  logText: string,
): { category: 'environmental' | 'flaky' | 'code' | 'unknown'; evidence: string[] } {
  const lines = logText.split('\n');
  const collect = (patterns: readonly RegExp[]): string[] => {
    const hits = new Set<string>();
    for (const line of lines) {
      for (const pattern of patterns) {
        if (pattern.test(line)) {
          hits.add(line.trim());
          break;
        }
      }
      if (hits.size >= 3) break;
    }
    return [...hits];
  };

  const environmentalPatterns = [
    /no space left on device/i,
    /service unavailable/i,
    /\b(502|503|504)\b/i,
    /network is unreachable/i,
    /connection reset/i,
    /connection timed out/i,
    /timed out while/i,
    /temporary failure in name resolution/i,
    /runner has received a shutdown signal/i,
    /resource temporarily unavailable/i,
    /\bECONNRESET\b|\bETIMEDOUT\b|\bEAI_AGAIN\b/i,
  ] as const;
  const flakyPatterns = [
    /\bflaky\b/i,
    /\bintermittent\b/i,
    /passed on retry/i,
    /retrying/i,
    /timed out waiting/i,
  ] as const;
  const codePatterns = [
    /compilation error/i,
    /\bbuild failure\b/i,
    /there are test failures/i,
    /tests run: .*failures: [1-9]/i,
    /checkstyle/i,
    /cannot find symbol/i,
    /assertion(?:failed|error)/i,
    /expected .* but was/i,
  ] as const;

  const environmentalEvidence = collect(environmentalPatterns);
  if (environmentalEvidence.length > 0) {
    return { category: 'environmental', evidence: environmentalEvidence };
  }
  const flakyEvidence = collect(flakyPatterns);
  if (flakyEvidence.length > 0) {
    return { category: 'flaky', evidence: flakyEvidence };
  }
  const codeEvidence = collect(codePatterns);
  if (codeEvidence.length > 0 || /build|test|lint/i.test(jobName)) {
    return { category: codeEvidence.length > 0 ? 'code' : 'unknown', evidence: codeEvidence };
  }
  return { category: 'unknown', evidence: [] };
}

async function fetchWorkflowJobsForRun(
  repoSlug: string,
  runId: number,
  context: GithubApiContext,
): Promise<GithubWorkflowJobPayload[]> {
  const response = await githubApiJson<GithubWorkflowJobsResponse>(
    `/repos/${repoSlug}/actions/runs/${runId}/jobs?per_page=100`,
    context,
  );
  return response.jobs ?? [];
}

async function fetchWorkflowJobLogText(
  repoSlug: string,
  jobId: number,
  context: GithubApiContext,
): Promise<string> {
  const url = new URL(
    `/repos/${repoSlug}/actions/jobs/${jobId}/logs`,
    context.apiBaseUrl.endsWith('/') ? context.apiBaseUrl : `${context.apiBaseUrl}/`,
  );
  const response = await context.fetchImpl(url, {
    headers: {
      Accept: 'application/vnd.github+json',
      Authorization: `Bearer ${context.token}`,
      'User-Agent': 'nana',
      'X-GitHub-Api-Version': '2022-11-28',
    },
  });
  if (!response.ok) return '';
  return response.text();
}

async function writeCiDiagnostics(
  sandboxPath: string,
  sha: string,
  checkRuns: readonly GithubCheckRunPayload[],
  workflowRuns: readonly GithubWorkflowRunPayload[],
  note: string,
  jobClassifications: Array<{ job: string; category: string; evidence: string[] }> = [],
): Promise<string> {
  const diagnosticsPath = join(sandboxPath, '.nana', 'ci-diagnostics.md');
  const lines = [
    '# CI Diagnostics',
    '',
    `- SHA: ${sha}`,
    `- Note: ${note}`,
    '',
    '## Check Runs',
    '',
  ];

  if (checkRuns.length === 0) {
    lines.push('(none)', '');
  } else {
    for (const check of checkRuns) {
      lines.push(
        `- ${check.name}: status=${check.status} conclusion=${check.conclusion ?? 'null'} ${check.details_url ?? check.html_url ?? ''}`.trim(),
      );
    }
    lines.push('');
  }

  lines.push('## Workflow Runs', '');
  if (workflowRuns.length === 0) {
    lines.push('(none)', '');
  } else {
    for (const run of workflowRuns) {
      lines.push(
        `- ${run.name ?? `run-${run.id}`}: status=${run.status} conclusion=${run.conclusion ?? 'null'} ${run.html_url}`.trim(),
      );
    }
    lines.push('');
  }

  if (jobClassifications.length > 0) {
    lines.push('## Failure Classification', '');
    for (const item of jobClassifications) {
      lines.push(`- ${item.job}: category=${item.category}`);
      for (const evidence of item.evidence) {
        lines.push(`  - ${evidence}`);
      }
    }
    lines.push('');
  }

  await writeFile(diagnosticsPath, `${lines.join('\n').trim()}\n`, 'utf-8');
  return diagnosticsPath;
}

async function rerunFailedWorkflowRun(
  repoSlug: string,
  runId: number,
  context: GithubApiContext,
): Promise<void> {
  try {
    await githubApiRequestNoContent('POST', `/repos/${repoSlug}/actions/runs/${runId}/rerun-failed-jobs`, context);
  } catch {
    await githubApiRequestNoContent('POST', `/repos/${repoSlug}/actions/runs/${runId}/rerun`, context);
  }
}

async function waitForGithubCiGreen(input: {
  manifest: GithubWorkonManifest;
  sandboxPath: string;
  headSha: string;
  context: GithubApiContext;
  env: NodeJS.ProcessEnv;
  writeLine: (line: string) => void;
}): Promise<
  | { ok: true }
  | { ok: false; diagnosticsPath: string; reason: string; retryable: boolean }
> {
  const pollMs = readPositiveEnvInt(input.env, 'NANA_GITHUB_CI_POLL_INTERVAL_MS', DEFAULT_GITHUB_CI_POLL_INTERVAL_MS);
  const timeoutMs = readPositiveEnvInt(input.env, 'NANA_GITHUB_CI_TIMEOUT_MS', DEFAULT_GITHUB_CI_TIMEOUT_MS);
  const rerunLimit = readPositiveEnvInt(input.env, 'NANA_GITHUB_CI_RERUN_LIMIT', DEFAULT_GITHUB_CI_RERUN_LIMIT);
  const startedAt = Date.now();
  const rerunCounts = new Map<number, number>();
  let sawAnyChecks = false;

  while (Date.now() - startedAt <= timeoutMs) {
    const [checkRuns, workflowRuns] = await Promise.all([
      fetchCheckRunsForSha(input.manifest.repo_slug, input.headSha, input.context),
      fetchWorkflowRunsForSha(input.manifest.repo_slug, input.headSha, input.context),
    ]);

    const relevantWorkflowRuns = workflowRuns.filter((run) => run.head_sha === input.headSha);
    if (checkRuns.length > 0 || relevantWorkflowRuns.length > 0) {
      sawAnyChecks = true;
    }

    const hasPendingChecks = checkRuns.some((check) => check.status !== 'completed');
    const hasPendingRuns = relevantWorkflowRuns.some((run) => run.status !== 'completed');

    if (!sawAnyChecks || hasPendingChecks || hasPendingRuns) {
      await sleep(pollMs);
      continue;
    }

    const failedChecks = checkRuns.filter((check) => !isSuccessfulConclusion(check.conclusion));
    const failedRuns = relevantWorkflowRuns.filter((run) => !isSuccessfulConclusion(run.conclusion));
    if (failedChecks.length === 0 && failedRuns.length === 0) {
      input.writeLine(`[github] CI is green for ${input.manifest.repo_slug} ${input.headSha}.`);
      return { ok: true };
    }

    const classifications: Array<{ job: string; category: string; evidence: string[]; runId: number }> = [];
    for (const run of failedRuns) {
      const jobs = await fetchWorkflowJobsForRun(input.manifest.repo_slug, run.id, input.context).catch(() => [] as GithubWorkflowJobPayload[]);
      const failedJobs = jobs.filter((job) => !isSuccessfulConclusion(job.conclusion));
      if (failedJobs.length === 0) {
        classifications.push({ job: run.name ?? `run-${run.id}`, category: 'unknown', evidence: [], runId: run.id });
        continue;
      }
      for (const job of failedJobs) {
        const logText = await fetchWorkflowJobLogText(input.manifest.repo_slug, job.id, input.context).catch(() => '');
        const classified = classifyGithubCiFailureEvidence(job.name, logText);
        classifications.push({
          job: job.name,
          category: classified.category,
          evidence: classified.evidence,
          runId: run.id,
        });
      }
    }

    let reran = false;
    for (const run of failedRuns) {
      const runClassifications = classifications.filter((classification) => classification.runId === run.id);
      const retryable = runClassifications.length > 0
        && runClassifications.every((classification) => classification.category === 'environmental' || classification.category === 'flaky');
      if (!retryable) continue;
      const current = rerunCounts.get(run.id) ?? 0;
      if (current >= rerunLimit) continue;
      await rerunFailedWorkflowRun(input.manifest.repo_slug, run.id, input.context);
      rerunCounts.set(run.id, current + 1);
      reran = true;
      input.writeLine(`[github] Reran failed workflow run ${run.id} for ${input.headSha} (attempt ${current + 1}/${rerunLimit}).`);
    }

    if (reran) {
      await sleep(pollMs);
      continue;
    }

    const diagnosticsPath = await writeCiDiagnostics(
      input.sandboxPath,
      input.headSha,
      checkRuns,
      relevantWorkflowRuns,
      'Checks failed after automatic rerun attempts were exhausted.',
      classifications.map(({ runId: _, ...rest }) => rest),
    );
    const retryable = classifications.length > 0
      && classifications.every((classification) => classification.category === 'environmental' || classification.category === 'flaky');
    return { ok: false, diagnosticsPath, reason: 'failed_checks', retryable };
  }

  const finalChecks = await fetchCheckRunsForSha(input.manifest.repo_slug, input.headSha, input.context).catch(() => [] as GithubCheckRunPayload[]);
  const finalRuns = await fetchWorkflowRunsForSha(input.manifest.repo_slug, input.headSha, input.context).catch(() => [] as GithubWorkflowRunPayload[]);
  const diagnosticsPath = await writeCiDiagnostics(
    input.sandboxPath,
    input.headSha,
    finalChecks,
    finalRuns.filter((run) => run.head_sha === input.headSha),
    'Timed out waiting for CI to reach a green terminal state.',
  );
  return { ok: false, diagnosticsPath, reason: 'timeout', retryable: true };
}

async function writeLatestRunPointers(paths: ManagedRepoPaths, runId: string): Promise<void> {
  await writeJsonFile(paths.repoLatestRunPath, { run_id: runId });
  await writeJsonFile(paths.globalLatestRunPath, { repo_root: paths.repoRoot, run_id: runId });
}

async function readGlobalLatestRun(nanaHome: string): Promise<{ repo_root?: string; run_id?: string } | null> {
  return readJsonFile<{ repo_root?: string; run_id?: string }>(join(nanaHome, 'github-workon', 'latest-run.json'));
}

async function findRunManifestPathByRunId(nanaHome: string, runId: string): Promise<string | null> {
  const reposRoot = join(nanaHome, 'repos');
  if (!existsSync(reposRoot)) return null;
  const owners = await readdir(reposRoot, { withFileTypes: true });
  for (const ownerEntry of owners) {
    if (!ownerEntry.isDirectory()) continue;
    const repoEntries = await readdir(join(reposRoot, ownerEntry.name), { withFileTypes: true });
    for (const repoEntry of repoEntries) {
      if (!repoEntry.isDirectory()) continue;
      const manifestPath = join(reposRoot, ownerEntry.name, repoEntry.name, 'runs', runId, 'manifest.json');
      if (existsSync(manifestPath)) return manifestPath;
    }
  }
  return null;
}

async function findLatestRunManifestForTargetUrl(
  nanaHome: string,
  targetUrl: string,
): Promise<GithubWorkonManifest | null> {
  const parsedTarget = parseGithubTargetUrl(targetUrl);
  const normalizedTargetUrl = parsedTarget.canonicalUrl;
  const reposRoot = join(nanaHome, 'repos');
  if (!existsSync(reposRoot)) return null;
  const owners = await readdir(reposRoot, { withFileTypes: true });
  let latest: GithubWorkonManifest | null = null;
  for (const ownerEntry of owners) {
    if (!ownerEntry.isDirectory()) continue;
    const repoEntries = await readdir(join(reposRoot, ownerEntry.name), { withFileTypes: true });
    for (const repoEntry of repoEntries) {
      if (!repoEntry.isDirectory()) continue;
      const runsDir = join(reposRoot, ownerEntry.name, repoEntry.name, 'runs');
      if (!existsSync(runsDir)) continue;
      const runEntries = await readdir(runsDir, { withFileTypes: true });
      for (const runEntry of runEntries) {
        if (!runEntry.isDirectory()) continue;
        const manifestPath = join(runsDir, runEntry.name, 'manifest.json');
        if (!existsSync(manifestPath)) continue;
        const manifest = await readManifest(manifestPath).catch(() => null);
        if (!manifest) continue;
        const exactTargetMatch = manifest.target_url === normalizedTargetUrl;
        const linkedPrMatch = parsedTarget.targetKind === 'pr'
          && manifest.repo_slug === parsedTarget.repoSlug
          && manifest.published_pr_number === parsedTarget.targetNumber;
        if (!exactTargetMatch && !linkedPrMatch) continue;
        if (!latest || Date.parse(manifest.updated_at) > Date.parse(latest.updated_at)) {
          latest = manifest;
        }
      }
    }
  }
  return latest;
}

async function findLatestRunManifestForPrSandboxLink(
  paths: ManagedRepoPaths,
  prNumber: number,
): Promise<GithubWorkonManifest | null> {
  const prSandboxPath = sandboxPathFor(paths, buildTargetSandboxId('pr', prNumber));
  if (!existsSync(prSandboxPath)) return null;
  try {
    const stat = await lstat(prSandboxPath);
    if (stat.isSymbolicLink()) {
      const linkTarget = await readlink(prSandboxPath);
      const resolvedSandboxPath = resolve(dirname(prSandboxPath), linkTarget);
      const linkedMetadata = await readSandboxMetadata(resolvedSandboxPath);
      if (linkedMetadata?.sandbox_id) {
        return findLatestRunManifestForSandbox(paths.repoRoot, linkedMetadata.sandbox_id);
      }
    }
  } catch {
    return null;
  }
  return null;
}

export async function resolveGithubRunIdForTargetUrl(
  targetUrl: string,
  input: { env?: NodeJS.ProcessEnv; homeDir?: string } = {},
): Promise<string | null> {
  const env = input.env ?? process.env;
  const nanaHome = resolveNanaHomeDir(env, input.homeDir);
  const parsedTarget = parseGithubTargetUrl(targetUrl);
  let manifest = await findLatestRunManifestForTargetUrl(nanaHome, parsedTarget.canonicalUrl);
  if (!manifest && parsedTarget.targetKind === 'pr') {
    const paths = managedRepoPaths(nanaHome, join(parsedTarget.owner, parsedTarget.repoName));
    manifest = await findLatestRunManifestForPrSandboxLink(paths, parsedTarget.targetNumber);
  }
  return manifest?.run_id ?? null;
}

async function findLatestRunManifestForSandbox(
  repoRoot: string,
  sandboxId: string,
): Promise<GithubWorkonManifest | null> {
  const runsDir = join(repoRoot, 'runs');
  if (!existsSync(runsDir)) return null;
  const runEntries = await readdir(runsDir, { withFileTypes: true });
  let latest: GithubWorkonManifest | null = null;
  for (const entry of runEntries) {
    if (!entry.isDirectory()) continue;
    const manifestPath = join(runsDir, entry.name, 'manifest.json');
    if (!existsSync(manifestPath)) continue;
    const manifest = await readManifest(manifestPath).catch(() => null);
    if (!manifest || manifest.sandbox_id !== sandboxId) continue;
    if (!latest || Date.parse(manifest.updated_at) > Date.parse(latest.updated_at)) {
      latest = manifest;
    }
  }
  return latest;
}

async function readManifest(manifestPath: string): Promise<GithubWorkonManifest> {
  return JSON.parse(await readFile(manifestPath, 'utf-8')) as GithubWorkonManifest;
}

async function resolveManifestForSync(
  nanaHome: string,
  parsed: GithubSyncCommand,
): Promise<GithubWorkonManifest> {
  if (parsed.runId) {
    const manifestPath = await findRunManifestPathByRunId(nanaHome, parsed.runId);
    if (!manifestPath) {
      throw new Error(`Run ${parsed.runId} was not found under ${join(nanaHome, 'repos')}.`);
    }
    return await readManifest(manifestPath);
  }

  const latest = await readGlobalLatestRun(nanaHome);
  if (!latest?.repo_root || !latest.run_id) {
    throw new Error(`No GitHub work-on run found in ${join(nanaHome, 'repos')}. Start one first with \`nana work-on start <url>\`.`);
  }

  return await readManifest(join(latest.repo_root, 'runs', latest.run_id, 'manifest.json'));
}

async function writeManifest(paths: GithubRunPaths, manifest: GithubWorkonManifest): Promise<void> {
  await mkdir(paths.runDir, { recursive: true });
  await writeJsonFile(paths.manifestPath, manifest);
}

async function reconcileManifestWithGithubState(
  manifest: GithubWorkonManifest,
  repoPaths: ManagedRepoPaths,
  context: GithubApiContext,
  now: Date,
): Promise<GithubWorkonManifest> {
  if (!(manifest.create_pr_on_complete || manifest.target_kind === 'pr' || manifest.published_pr_number)) {
    return manifest;
  }

  const runPaths = githubRunPaths(repoPaths.repoRoot, manifest.run_id);
  const sandboxMetadata = await readSandboxMetadata(manifest.sandbox_path);
  if (!sandboxMetadata) return manifest;
  const { remoteBranch } = resolvePublicationBranch(manifest, sandboxMetadata.branch_name);

  let pr: GithubPullRequestPayload | undefined;
  if (manifest.target_kind === 'pr') {
    pr = await githubApiJson<GithubPullRequestPayload>(
      `/repos/${manifest.repo_slug}/pulls/${manifest.target_number}`,
      context,
    ).catch(() => undefined);
  } else {
    const existing = await fetchPullRequestsForHeadBranch(
      manifest.repo_slug,
      manifest.repo_owner,
      remoteBranch,
      context,
    ).catch(() => [] as GithubPullRequestPayload[]);
    pr = existing[0];
  }

  if (!pr) return manifest;

  let publicationState: GithubPublicationState = manifest.publication_state ?? 'pr_opened';
  const [checkRuns, workflowRuns] = await Promise.all([
    fetchCheckRunsForSha(manifest.repo_slug, pr.head.sha, context).catch(() => [] as GithubCheckRunPayload[]),
    fetchWorkflowRunsForSha(manifest.repo_slug, pr.head.sha, context).catch(() => [] as GithubWorkflowRunPayload[]),
  ]);
  const relevantWorkflowRuns = workflowRuns.filter((run) => run.head_sha === pr!.head.sha);
  const hasAnyChecks = checkRuns.length > 0 || relevantWorkflowRuns.length > 0;
  const hasPendingChecks = checkRuns.some((check) => check.status !== 'completed');
  const hasPendingRuns = relevantWorkflowRuns.some((run) => run.status !== 'completed');
  const hasFailures = checkRuns.some((check) => !isSuccessfulConclusion(check.conclusion))
    || relevantWorkflowRuns.some((run) => !isSuccessfulConclusion(run.conclusion));

  if (hasAnyChecks && !hasPendingChecks && !hasPendingRuns && !hasFailures) {
    publicationState = 'ci_green';
  } else if (hasFailures) {
    publicationState = 'blocked';
  } else if (hasAnyChecks) {
    publicationState = 'ci_waiting';
  }

  const updated: GithubWorkonManifest = {
    ...manifest,
    updated_at: now.toISOString(),
    published_pr_number: pr.number,
    published_pr_url: pr.html_url,
    published_pr_head_ref: remoteBranch,
    publication_state: publicationState,
    publication_updated_at: now.toISOString(),
    publication_error: publicationState === 'blocked' ? manifest.publication_error ?? 'External CI has failing checks' : undefined,
  };

  if (updated.verification_plan && publicationState === 'ci_green') {
    const successfulJobs = (await Promise.all(
      relevantWorkflowRuns
        .filter((run) => run.conclusion === 'success')
        .map((run) => fetchWorkflowJobsForRun(manifest.repo_slug, run.id, context).catch(() => [] as GithubWorkflowJobPayload[])),
    )).flat();
    const ciDurations = buildCiSuiteDurationsFromJobs(updated.verification_plan.plan_fingerprint, successfulJobs, now);
    await writeCiSuiteDurations(repoPaths.repoCiSuiteDurationsPath, ciDurations).catch(() => {});
  }

  await writeManifest(runPaths, updated);
  return updated;
}

async function setGithubRepoDefaults(
  parsed: GithubDefaultsSetCommand,
  dependencies: Pick<GithubCommandDependencies, 'writeLine' | 'now' | 'env' | 'homeDir'> & {
    writeLine: (line: string) => void;
    now: () => Date;
    env: NodeJS.ProcessEnv;
  },
): Promise<void> {
  const [owner, repoName] = parsed.repoSlug.split('/');
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const paths = managedRepoPaths(nanaHome, join(owner, repoName));
  const existing = await readManagedRepoSettings(paths);
  const resolvedConsiderations = parsed.considerations.length > 0
    ? parsed.considerations
    : (existing?.default_considerations ?? []);
  const resolvedRoleLayout = parsed.roleLayout ?? existing?.default_role_layout;
  const resolvedReviewerPolicy = normalizeReviewerPolicy({
    trusted_reviewers: parsed.reviewRulesTrustedReviewers ?? existing?.review_rules_reviewer_policy?.trusted_reviewers,
    blocked_reviewers: parsed.reviewRulesBlockedReviewers ?? existing?.review_rules_reviewer_policy?.blocked_reviewers,
    min_distinct_reviewers: parsed.reviewRulesMinDistinctReviewers ?? existing?.review_rules_reviewer_policy?.min_distinct_reviewers,
  });
  const settings = await writeManagedRepoSettings(
    paths,
    resolvedConsiderations,
    resolvedRoleLayout,
    parsed.reviewRulesMode ?? existing?.review_rules_mode,
    resolvedReviewerPolicy,
    existing?.hot_path_api_profile,
    dependencies.now(),
  );
  dependencies.writeLine(
    `[github] Saved default considerations for ${parsed.repoSlug}: ${settings.default_considerations.join(', ') || '(none)'}`,
  );
  dependencies.writeLine(`[github] Saved role layout for ${parsed.repoSlug}: ${settings.default_role_layout ?? 'split'}`);
  dependencies.writeLine(`[github] Saved review-rules mode for ${parsed.repoSlug}: ${settings.review_rules_mode ?? 'manual'}`);
  dependencies.writeLine(`[github] Saved review-rules trusted reviewers for ${parsed.repoSlug}: ${settings.review_rules_reviewer_policy?.trusted_reviewers?.join(', ') || '(none)'}`);
  dependencies.writeLine(`[github] Saved review-rules blocked reviewers for ${parsed.repoSlug}: ${settings.review_rules_reviewer_policy?.blocked_reviewers?.join(', ') || '(none)'}`);
  dependencies.writeLine(`[github] Saved review-rules min distinct reviewers for ${parsed.repoSlug}: ${settings.review_rules_reviewer_policy?.min_distinct_reviewers ?? '(none)'}`);
  dependencies.writeLine(`[github] Settings path: ${paths.repoSettingsPath}`);
}

async function showGithubRepoDefaults(
  parsed: GithubDefaultsShowCommand,
  dependencies: Pick<GithubCommandDependencies, 'writeLine' | 'env' | 'homeDir'> & {
    writeLine: (line: string) => void;
    env: NodeJS.ProcessEnv;
  },
): Promise<void> {
  const [owner, repoName] = parsed.repoSlug.split('/');
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const paths = managedRepoPaths(nanaHome, join(owner, repoName));
  const settings = await readManagedRepoSettings(paths);
  const defaults = settings?.default_considerations ?? [];
  const roleLayout = settings?.default_role_layout ?? 'split';
  const reviewRulesMode = settings?.review_rules_mode;
  const effectiveReviewRulesMode = await resolveGithubReviewRulesMode(nanaHome, settings);
  const effectiveReviewerPolicy = await resolveGithubReviewRulesReviewerPolicy(nanaHome, settings);
  dependencies.writeLine(
    `[github] Default considerations for ${parsed.repoSlug}: ${defaults.length > 0 ? defaults.join(', ') : '(none)'}`,
  );
  dependencies.writeLine(`[github] Default role layout for ${parsed.repoSlug}: ${roleLayout}`);
  dependencies.writeLine(`[github] Repo review-rules mode for ${parsed.repoSlug}: ${reviewRulesMode ?? '(none)'}`);
  dependencies.writeLine(`[github] Effective review-rules mode for ${parsed.repoSlug}: ${effectiveReviewRulesMode}`);
  dependencies.writeLine(`[github] Repo reviewer policy for ${parsed.repoSlug}: ${formatReviewerPolicySummary(settings?.review_rules_reviewer_policy)}`);
  dependencies.writeLine(`[github] Effective reviewer policy for ${parsed.repoSlug}: ${formatReviewerPolicySummary(effectiveReviewerPolicy)}`);
  dependencies.writeLine('[github] Resolved default pipeline:');
  for (const line of buildConsiderationInstructionLines({
    run_id: '<defaults>',
    considerations_active: defaults,
    role_layout: roleLayout,
    consideration_pipeline: buildConsiderationPipeline(defaults, roleLayout),
    lane_prompt_artifacts: [],
  })) {
    dependencies.writeLine(line);
  }
  dependencies.writeLine(`[github] Settings path: ${paths.repoSettingsPath}`);
}

async function showGithubWorkonStats(
  parsed: GithubStatsCommand,
  dependencies: Pick<GithubCommandDependencies, 'writeLine' | 'env' | 'homeDir' | 'now'> & {
    writeLine: (line: string) => void;
    env: NodeJS.ProcessEnv;
    now: () => Date;
  },
): Promise<void> {
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const paths = managedRepoPaths(nanaHome, join(parsed.target.owner, parsed.target.repoName));
  const apiBaseUrl = dependencies.env.GITHUB_API_URL?.trim() || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(dependencies.env, apiBaseUrl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: fetch };

  let issueNumber: number | undefined;
  if (parsed.target.targetKind === 'issue') {
    issueNumber = parsed.target.targetNumber;
  } else {
    const prSandboxPath = sandboxPathFor(paths, buildTargetSandboxId('pr', parsed.target.targetNumber));
    issueNumber = await resolveIssueAssociationNumber(prSandboxPath, undefined, undefined);
    if (!issueNumber) {
      throw new Error(`PR #${parsed.target.targetNumber} is not currently linked to an NANA-managed issue sandbox, so no issue token stats are available.`);
    }
    await captureSandboxTokenUsageForIssue(
      paths,
      parsed.target.repoSlug,
      buildTargetSandboxId('pr', parsed.target.targetNumber),
      prSandboxPath,
      issueNumber,
      dependencies.now(),
    );
  }

  const associatedSandboxes = await listIssueAssociatedSandboxes(paths, issueNumber);
  const liveSnapshots: Array<{ sandboxId: string; totals: GithubTokenTotals }> = [];
  for (const sandbox of associatedSandboxes) {
    const manifest = await findLatestRunManifestForSandbox(paths.repoRoot, sandbox.sandboxId);
    if (manifest) {
      await reconcileManifestWithGithubState(manifest, paths, apiContext, dependencies.now()).catch(() => {});
    }
    await captureSandboxTokenUsageForIssue(
      paths,
      parsed.target.repoSlug,
      sandbox.sandboxId,
      sandbox.sandboxPath,
      issueNumber,
      dependencies.now(),
    );
    const issueStatsSnapshot = await readIssueTokenStats(paths, parsed.target.repoSlug, issueNumber);
    const persistedSandbox = issueStatsSnapshot.sandboxes[sandbox.sandboxId];
    const liveTotals = await readLiveRolloutTokenTotals(sandbox.sandboxPath).catch(() => null);
    if (liveTotals && (!persistedSandbox || persistedSandbox.total_tokens <= 0)) {
      liveSnapshots.push({ sandboxId: sandbox.sandboxId, totals: liveTotals });
    }
  }

  const stats = await readIssueTokenStats(paths, parsed.target.repoSlug, issueNumber);
  for (const line of buildIssueStatsLines(stats)) {
    dependencies.writeLine(line);
  }
  for (const snapshot of liveSnapshots) {
    dependencies.writeLine(
      `[github] Live tokens for ${snapshot.sandboxId} (session still active, not finalized): ${snapshot.totals.total_tokens} total (${snapshot.totals.input_tokens} input, ${snapshot.totals.output_tokens} output)`,
    );
  }
}

function describeSyncFeedbackScope(
  manifest: GithubWorkonManifest,
  feedbackTarget?: ParsedGithubTargetUrl,
): string {
  if (feedbackTarget?.targetKind === 'pr') {
    if (manifest.target_kind === 'issue') {
      return `${manifest.repo_slug} issue #${manifest.target_number} (linked PR #${feedbackTarget.targetNumber})`;
    }
    return `${manifest.repo_slug} pr #${feedbackTarget.targetNumber}`;
  }
  return `${manifest.repo_slug} ${manifest.target_kind} #${manifest.target_number}`;
}

async function resolveSyncCiStatus(
  manifest: GithubWorkonManifest,
  context: GithubApiContext,
  feedbackTarget?: ParsedGithubTargetUrl,
): Promise<{ prNumber?: number; state?: GithubPublicationState }> {
  if (feedbackTarget?.targetKind === 'pr') {
    const pr = await githubApiJson<GithubPullRequestPayload>(
      `/repos/${manifest.repo_slug}/pulls/${feedbackTarget.targetNumber}`,
      context,
    ).catch(() => null);
    if (!pr) return { prNumber: feedbackTarget.targetNumber };
    return {
      prNumber: pr.number,
      state: await readCiStateForHeadSha(manifest, pr.head.sha, context),
    };
  }

  if (typeof manifest.published_pr_number === 'number') {
    return {
      prNumber: manifest.published_pr_number,
      state: manifest.publication_state,
    };
  }

  return {};
}

export async function investigateGithubTarget(
  targetUrl: string,
  dependencies: Pick<GithubCommandDependencies, 'fetchImpl' | 'writeLine' | 'env' | 'homeDir' | 'execFileSyncImpl'> & {
    writeLine: (line: string) => void;
    env: NodeJS.ProcessEnv;
  },
): Promise<void> {
  const parsedTarget = parseGithubTargetUrl(targetUrl);
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const apiBaseUrl = dependencies.env.GITHUB_API_URL?.trim() || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(dependencies.env, apiBaseUrl, dependencies.execFileSyncImpl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: dependencies.fetchImpl ?? fetch };
  const now = new Date();
  const target = await fetchTargetContext(parsedTarget, apiContext);
  const paths = managedRepoPaths(nanaHome, join(parsedTarget.owner, parsedTarget.repoName));
  const repoMeta = await ensureManagedRepoMetadata(paths, target, now);
  ensureSourceClone(paths, repoMeta);
  const verificationPlan = await detectVerificationPlan(paths.sourcePath);
  await writeRepoVerificationPlan(paths, paths.sourcePath, verificationPlan);
  let settings = await readManagedRepoSettings(paths);
  if (!settings) {
    const inferred = await inferInitialRepoConsiderations(paths.sourcePath, repoMeta.repo_slug, verificationPlan);
    settings = await writeManagedRepoSettings(paths, inferred.considerations, undefined, undefined, undefined, undefined, now);
  }
  settings = await refreshManagedRepoHotPathApiProfile(paths, settings, paths.sourcePath, now);
  await maybeAutoRefreshRepoReviewRulesForTarget({
    nanaHome,
    target: parsedTarget,
    paths,
    repoSettings: settings,
    apiContext,
    now,
    writeLine: dependencies.writeLine,
  });
  const considerations = settings.default_considerations ?? [];
  const roleLayout = settings.default_role_layout ?? 'split';
  const reviewRulesMode = await resolveGithubReviewRulesMode(nanaHome, settings);
  const pipeline = buildConsiderationPipeline(considerations, roleLayout);
  const hotPathProfile = settings.hot_path_api_profile;

  dependencies.writeLine(`[github] Investigated ${repoMeta.repo_slug} ${parsedTarget.targetKind} #${parsedTarget.targetNumber}`);
  dependencies.writeLine(`[github] Title: ${target.issue.title}`);
  dependencies.writeLine(`[github] Managed repo root: ${paths.repoRoot}`);
  dependencies.writeLine(`[github] Source path: ${paths.sourcePath}`);
  dependencies.writeLine(`[github] Default branch: ${repoMeta.default_branch}`);
  dependencies.writeLine(`[github] Suggested considerations: ${considerations.join(', ') || '(none)'}`);
  dependencies.writeLine(`[github] Suggested role layout: ${roleLayout}`);
  dependencies.writeLine(`[github] Review-rules mode: ${reviewRulesMode}`);
  dependencies.writeLine(`[github] Verification plan: lint=${verificationPlan.lint.length} compile=${verificationPlan.compile.length} unit=${verificationPlan.unit.length} integration=${verificationPlan.integration.length}`);
  dependencies.writeLine(`[github] Hot-path API files: ${hotPathProfile?.hot_path_api_files.join(', ') || '(none detected)'}`);
  dependencies.writeLine(`[github] Hot-path API tokens: ${hotPathProfile?.api_identifier_tokens.join(', ') || '(none detected)'}`);
  dependencies.writeLine('[github] Suggested pipeline:');
  for (const line of buildConsiderationInstructionLines({
    run_id: '<investigate>',
    considerations_active: considerations,
    role_layout: roleLayout,
    consideration_pipeline: pipeline,
    lane_prompt_artifacts: [],
  })) {
    dependencies.writeLine(line);
  }
  dependencies.writeLine(`[github] Next: nana implement ${parsedTarget.canonicalUrl}`);
}

function buildRepoReviewRulesUsage(): string {
  return [
    'Usage:',
    '  nana review-rules scan <owner/repo|github-issue-url|github-pr-url>',
    '  nana review-rules list <owner/repo|github-issue-url|github-pr-url>',
    '  nana review-rules approve <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]',
    '  nana review-rules disable <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]',
    '  nana review-rules enable <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]',
    '  nana review-rules archive <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]',
    '  nana review-rules explain <owner/repo|github-issue-url|github-pr-url> <rule-id>',
    '  nana review-rules config set --mode <manual|automatic>',
    '  nana review-rules config show [owner/repo|github-issue-url|github-pr-url]',
  ].join('\n');
}

function formatReviewerPolicySummary(policy: GithubReviewRulesReviewerPolicy | undefined): string {
  return reviewerPolicyReason(policy) ?? '(none)';
}

async function resolveReviewRuleScanSource(
  locator: string,
  context: GithubApiContext,
  env: NodeJS.ProcessEnv,
  homeDir: string | undefined,
  now: Date,
): Promise<{ source: GithubReviewRuleScanSource; paths: ManagedRepoPaths; repoMeta: ManagedRepoMetadata }> {
  const nanaHome = resolveNanaHomeDir(env, homeDir);

  if (locator.startsWith('https://github.com/')) {
    const target = parseGithubTargetUrl(locator);
    const targetContext = await fetchTargetContext(target, context);
    const paths = managedRepoPaths(nanaHome, join(target.owner, target.repoName));
    const repoMeta = await ensureManagedRepoMetadata(paths, targetContext, now);
    ensureSourceClone(paths, repoMeta);
    if (target.targetKind === 'pr') {
      return {
        source: {
          repoSlug: target.repoSlug,
          owner: target.owner,
          repoName: target.repoName,
          sourceKind: 'pr',
          sourceTarget: target.canonicalUrl,
          prNumbers: [target.targetNumber],
        },
        paths,
        repoMeta,
      };
    }
    const linkedPrNumbers = await collectIssueLinkedPullNumbers(nanaHome, target);
    return {
      source: {
        repoSlug: target.repoSlug,
        owner: target.owner,
        repoName: target.repoName,
        sourceKind: 'issue',
        sourceTarget: target.canonicalUrl,
        issueNumber: target.targetNumber,
        prNumbers: linkedPrNumbers,
      },
      paths,
      repoMeta,
    };
  }

  const repo = parseGithubRepoSlug(locator);
  const repository = await fetchRepositoryContext(repo.repoSlug, context);
  const paths = managedRepoPaths(nanaHome, join(repo.owner, repo.repoName));
  const repoMeta = await ensureManagedRepoMetadataFromRepository(paths, repository, now);
  ensureSourceClone(paths, repoMeta);
  return {
    source: {
      repoSlug: repo.repoSlug,
      owner: repo.owner,
      repoName: repo.repoName,
      sourceKind: 'repo',
      sourceTarget: repo.repoSlug,
    },
    paths,
    repoMeta,
  };
}

function mergeReviewRuleScanResults(
  existing: GithubRepoReviewRulesDocument | null,
  repoSlug: string,
  candidates: readonly GithubRepoReviewRule[],
  lastScan: NonNullable<GithubRepoReviewRulesDocument['last_scan']>,
  now: Date,
  mode: GithubReviewRulesMode,
): GithubRepoReviewRulesDocument {
  const approvedById = new Map((existing?.approved_rules ?? []).map((rule) => [rule.id, rule] as const));
  const pendingById = new Map((existing?.pending_candidates ?? []).map((rule) => [rule.id, rule] as const));
  const disabledById = new Map((existing?.disabled_rules ?? []).map((rule) => [rule.id, rule] as const));
  const archivedById = new Map((existing?.archived_rules ?? []).map((rule) => [rule.id, rule] as const));
  const normalizedCandidates = candidates.map((rule) => {
    const previous = pendingById.get(rule.id) ?? approvedById.get(rule.id) ?? disabledById.get(rule.id) ?? archivedById.get(rule.id);
    return previous
      ? {
        ...rule,
        created_at: previous.created_at,
      }
      : rule;
  });
  const pendingCandidates = mode === 'automatic'
    ? []
    : normalizedCandidates.filter((rule) => !approvedById.has(rule.id) && !disabledById.has(rule.id) && !archivedById.has(rule.id));
  const approvedRules = mode === 'automatic'
    ? [...new Map([...approvedById.values(), ...normalizedCandidates.filter((rule) => !disabledById.has(rule.id) && !archivedById.has(rule.id))].map((rule) => [rule.id, rule] as const)).values()]
    : [...approvedById.values()];
  const disabledRules = [...new Map(
    [...disabledById.values(), ...normalizedCandidates.filter((rule) => disabledById.has(rule.id))]
      .map((rule) => [rule.id, rule] as const),
  ).values()];
  const archivedRules = [...new Map(
    [...archivedById.values(), ...normalizedCandidates.filter((rule) => archivedById.has(rule.id))]
      .map((rule) => [rule.id, rule] as const),
  ).values()];

  return normalizeRuleBuckets({
    version: 1,
    repo_slug: repoSlug,
    generated_at: existing?.generated_at ?? now.toISOString(),
    updated_at: now.toISOString(),
    approved_rules: approvedRules,
    pending_candidates: pendingCandidates,
    disabled_rules: disabledRules,
    archived_rules: archivedRules,
    last_scan: lastScan,
  });
}

function formatReviewRuleSummary(rule: GithubRepoReviewRule): string {
  const scopeSuffix = rule.path_scopes.length > 0 ? ` scopes=${rule.path_scopes.join(',')}` : '';
  const evidenceTarget = rule.evidence[0]?.path
    ? ` path=${rule.evidence[0].path}${rule.evidence[0].line != null ? `:${rule.evidence[0].line}` : ''}`
    : '';
  const contextSource = rule.evidence[0]?.code_context_provenance ? ` ctx=${rule.evidence[0].code_context_provenance}` : '';
  return `${rule.id} [${rule.category}] confidence=${rule.confidence.toFixed(2)} evidence=${rule.evidence.length} origin=${rule.extraction_origin}${scopeSuffix}${evidenceTarget}${contextSource} :: ${rule.rule} (${rule.extraction_reason})`;
}

function findReviewRuleById(
  document: GithubRepoReviewRulesDocument,
  ruleId: string,
): { state: 'approved' | 'pending' | 'disabled' | 'archived'; rule: GithubRepoReviewRule } | null {
  for (const [state, bucket] of [
    ['approved', document.approved_rules],
    ['pending', document.pending_candidates],
    ['disabled', document.disabled_rules ?? []],
    ['archived', document.archived_rules ?? []],
  ] as const) {
    const rule = bucket.find((candidate) => candidate.id === ruleId);
    if (rule) return { state, rule };
  }
  return null;
}

function normalizeRuleBuckets(document: GithubRepoReviewRulesDocument): GithubRepoReviewRulesDocument {
  return {
    ...document,
    approved_rules: [...document.approved_rules].sort((left, right) => left.title.localeCompare(right.title)),
    pending_candidates: [...document.pending_candidates].sort((left, right) => left.title.localeCompare(right.title)),
    disabled_rules: [...(document.disabled_rules ?? [])].sort((left, right) => left.title.localeCompare(right.title)),
    archived_rules: [...(document.archived_rules ?? [])].sort((left, right) => left.title.localeCompare(right.title)),
  };
}

function rewriteRuleLifecycleState(
  document: GithubRepoReviewRulesDocument,
  targetState: 'approved' | 'disabled' | 'archived',
  selectors: readonly string[],
  now: Date,
): { document: GithubRepoReviewRulesDocument; moved: number } {
  const approveAll = selectors.includes('all');
  const selectedIds = new Set(selectors.filter((value) => value !== 'all'));
  const buckets = {
    approved: [...document.approved_rules],
    pending: [...document.pending_candidates],
    disabled: [...(document.disabled_rules ?? [])],
    archived: [...(document.archived_rules ?? [])],
  };

  const take = (state: keyof typeof buckets): GithubRepoReviewRule[] => {
    const kept: GithubRepoReviewRule[] = [];
    const moved: GithubRepoReviewRule[] = [];
    for (const rule of buckets[state]) {
      if (approveAll || selectedIds.has(rule.id)) moved.push({ ...rule, updated_at: now.toISOString() });
      else kept.push(rule);
    }
    buckets[state] = kept;
    return moved;
  };

  const moved = [
    ...take('approved'),
    ...take('pending'),
    ...take('disabled'),
    ...take('archived'),
  ];
  if (moved.length === 0) return { document, moved: 0 };

  if (targetState === 'approved') buckets.approved.push(...moved);
  if (targetState === 'disabled') buckets.disabled.push(...moved);
  if (targetState === 'archived') buckets.archived.push(...moved);

  return {
    moved: moved.length,
    document: normalizeRuleBuckets({
      ...document,
      approved_rules: buckets.approved,
      pending_candidates: buckets.pending,
      disabled_rules: buckets.disabled,
      archived_rules: buckets.archived,
      updated_at: now.toISOString(),
    }),
  };
}

async function scanRepoReviewRulesForSource(
  source: GithubReviewRuleScanSource,
  paths: ManagedRepoPaths,
  apiContext: GithubApiContext,
  now: Date,
  mode: GithubReviewRulesMode,
  reviewerPolicy: GithubReviewRulesReviewerPolicy | undefined,
): Promise<GithubRepoReviewRulesDocument> {
  const prNumbers = source.sourceKind === 'repo'
    ? (await listRepositoryPullRequests(source.repoSlug, apiContext)).map((pr) => pr.number)
    : (source.prNumbers ?? []);
  const { reviews, reviewComments, prHeadShas } = await collectReviewHistoryForPullRequests(source.repoSlug, prNumbers, apiContext);
  const signals = await enrichReviewRuleSignalsWithCodeContext(
    paths.sourcePath,
    selectRepoReviewRuleSignals(reviews, reviewComments),
    source.repoSlug,
    prHeadShas,
    apiContext,
  );
  const candidates = buildReviewRuleCandidates(signals, now, reviewerPolicy);
  const document = mergeReviewRuleScanResults(
    await readRepoReviewRulesDocument(paths.sourcePath),
    source.repoSlug,
    candidates,
    {
      at: now.toISOString(),
      source: source.sourceKind,
      source_target: source.sourceTarget,
      scanned_prs: [...new Set(prNumbers)].length,
      scanned_reviews: reviews.length,
      scanned_review_comments: reviewComments.length,
    },
    now,
    mode,
  );
  await writeRepoReviewRulesDocument(paths.sourcePath, document);
  return document;
}

async function maybeAutoRefreshRepoReviewRulesForTarget(
  input: {
    nanaHome: string;
    target: ParsedGithubTargetUrl;
    paths: ManagedRepoPaths;
    repoSettings: ManagedRepoSettings | null | undefined;
    apiContext: GithubApiContext;
    now: Date;
    writeLine?: (line: string) => void;
  },
): Promise<GithubRepoReviewRulesDocument | null> {
  const mode = await resolveGithubReviewRulesMode(input.nanaHome, input.repoSettings);
  if (mode !== 'automatic') return null;
  const source = await buildReviewRuleScanSourceForTarget(input.nanaHome, input.target);
  const reviewerPolicy = await resolveGithubReviewRulesReviewerPolicy(input.nanaHome, input.repoSettings);
  const document = await scanRepoReviewRulesForSource(source, input.paths, input.apiContext, input.now, mode, reviewerPolicy);
  input.writeLine?.(`[github] Review-rules automatic refresh for ${source.repoSlug}: approved=${document.approved_rules.length} pending=${document.pending_candidates.length}.`);
  return document;
}

export async function githubReviewRulesCommand(
  args: readonly string[],
  dependencies: GithubReviewRulesCommandDependencies = {},
): Promise<void> {
  const writeLine = dependencies.writeLine ?? ((line: string) => console.log(line));
  const env = dependencies.env ?? process.env;
  const now = dependencies.now?.() ?? new Date();
  const first = args[0];
  if (!first || isHelpToken(first)) {
    writeLine(buildRepoReviewRulesUsage());
    return;
  }

  if (first === 'config') {
    const action = args[1];
    const nanaHome = resolveNanaHomeDir(env, dependencies.homeDir);
    if (!action || isHelpToken(action)) {
      writeLine(buildRepoReviewRulesUsage());
      return;
    }
    if (action === 'set') {
      const existing = await readGithubReviewRulesGlobalConfig(nanaHome);
      let mode = existing?.default_mode ?? 'manual';
      let reviewerPolicy = normalizeReviewerPolicy(existing?.reviewer_policy);
      for (let index = 2; index < args.length; index += 1) {
        const token = args[index];
        if (token === '--mode') {
          mode = parseReviewRulesMode(args[index + 1], '--mode');
          index += 1;
          continue;
        }
        if (token?.startsWith('--mode=')) {
          mode = parseReviewRulesMode(token.slice('--mode='.length), '--mode');
          continue;
        }
        if (token === '--trusted-reviewers') {
          reviewerPolicy = normalizeReviewerPolicy({
            ...reviewerPolicy,
            trusted_reviewers: parseLoginList(args[index + 1], '--trusted-reviewers'),
          });
          index += 1;
          continue;
        }
        if (token?.startsWith('--trusted-reviewers=')) {
          reviewerPolicy = normalizeReviewerPolicy({
            ...reviewerPolicy,
            trusted_reviewers: parseLoginList(token.slice('--trusted-reviewers='.length), '--trusted-reviewers'),
          });
          continue;
        }
        if (token === '--blocked-reviewers') {
          reviewerPolicy = normalizeReviewerPolicy({
            ...reviewerPolicy,
            blocked_reviewers: parseLoginList(args[index + 1], '--blocked-reviewers'),
          });
          index += 1;
          continue;
        }
        if (token?.startsWith('--blocked-reviewers=')) {
          reviewerPolicy = normalizeReviewerPolicy({
            ...reviewerPolicy,
            blocked_reviewers: parseLoginList(token.slice('--blocked-reviewers='.length), '--blocked-reviewers'),
          });
          continue;
        }
        if (token === '--min-distinct-reviewers') {
          reviewerPolicy = normalizeReviewerPolicy({
            ...reviewerPolicy,
            min_distinct_reviewers: parsePositiveIntFlag(args[index + 1], '--min-distinct-reviewers'),
          });
          index += 1;
          continue;
        }
        if (token?.startsWith('--min-distinct-reviewers=')) {
          reviewerPolicy = normalizeReviewerPolicy({
            ...reviewerPolicy,
            min_distinct_reviewers: parsePositiveIntFlag(token.slice('--min-distinct-reviewers='.length), '--min-distinct-reviewers'),
          });
          continue;
        }
      }
      const config = await writeGithubReviewRulesGlobalConfig(nanaHome, mode, reviewerPolicy, now);
      writeLine(`[github] Saved global review-rules mode: ${config.default_mode}`);
      writeLine(`[github] Saved global reviewer policy: ${formatReviewerPolicySummary(config.reviewer_policy)}`);
      writeLine(`[github] Config path: ${githubReviewRulesGlobalConfigPath(nanaHome)}`);
      return;
    }
    if (action === 'show') {
      const globalConfig = await readGithubReviewRulesGlobalConfig(nanaHome);
      writeLine(`[github] Global review-rules mode: ${globalConfig?.default_mode ?? 'manual'}`);
      writeLine(`[github] Global reviewer policy: ${formatReviewerPolicySummary(globalConfig?.reviewer_policy)}`);
      writeLine(`[github] Config path: ${githubReviewRulesGlobalConfigPath(nanaHome)}`);
      const locator = args[2];
      if (!locator) return;
      const apiBaseUrl = env.GITHUB_API_URL?.trim() || DEFAULT_GITHUB_API_BASE_URL;
      const token = resolveGithubToken(env, apiBaseUrl, dependencies.execFileSyncImpl);
      const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: dependencies.fetchImpl ?? fetch };
      const { source, paths } = await resolveReviewRuleScanSource(locator, apiContext, env, dependencies.homeDir, now);
      const repoSettings = await readManagedRepoSettings(paths);
      writeLine(`[github] Repo review-rules mode for ${source.repoSlug}: ${repoSettings?.review_rules_mode ?? '(none)'}`);
      writeLine(`[github] Effective review-rules mode for ${source.repoSlug}: ${await resolveGithubReviewRulesMode(nanaHome, repoSettings)}`);
      writeLine(`[github] Repo reviewer policy for ${source.repoSlug}: ${formatReviewerPolicySummary(repoSettings?.review_rules_reviewer_policy)}`);
      writeLine(`[github] Effective reviewer policy for ${source.repoSlug}: ${formatReviewerPolicySummary(await resolveGithubReviewRulesReviewerPolicy(nanaHome, repoSettings))}`);
      return;
    }
    throw new Error(`Unknown review-rules config action: ${action}\n${buildRepoReviewRulesUsage()}`);
  }

  const locator = args[1];
  if (!locator) {
    throw new Error(`${buildRepoReviewRulesUsage()}\n\nMissing review-rules target.`);
  }

  const apiBaseUrl = env.GITHUB_API_URL?.trim() || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(env, apiBaseUrl, dependencies.execFileSyncImpl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: dependencies.fetchImpl ?? fetch };
  const { source, paths } = await resolveReviewRuleScanSource(locator, apiContext, env, dependencies.homeDir, now);
  const rulesPath = repoReviewRulesPathForSource(paths.sourcePath);
  const repoSettings = await readManagedRepoSettings(paths);
  const effectiveMode = await resolveGithubReviewRulesMode(resolveNanaHomeDir(env, dependencies.homeDir), repoSettings);
  const effectiveReviewerPolicy = await resolveGithubReviewRulesReviewerPolicy(resolveNanaHomeDir(env, dependencies.homeDir), repoSettings);

  if (first === 'scan') {
    const document = await scanRepoReviewRulesForSource(source, paths, apiContext, now, effectiveMode, effectiveReviewerPolicy);
    writeLine(`[github] Scanned PR review history for ${source.repoSlug} from ${source.sourceTarget}.`);
    writeLine(`[github] Rules file: ${rulesPath}`);
    writeLine(`[github] Effective review-rules mode: ${effectiveMode}`);
    if (effectiveReviewerPolicy) {
      writeLine(`[github] Effective reviewer policy: ${reviewerPolicyReason(effectiveReviewerPolicy)}`);
    }
    writeLine(`[github] Reviewed PRs=${document.last_scan?.scanned_prs ?? 0} reviews=${document.last_scan?.scanned_reviews ?? 0} review-comments=${document.last_scan?.scanned_review_comments ?? 0}.`);
    writeLine(`[github] Approved rules=${document.approved_rules.length} pending candidates=${document.pending_candidates.length}.`);
    if (document.pending_candidates.length === 0) {
      if (effectiveMode === 'automatic' && document.approved_rules.length > 0) {
        writeLine('[github] Automatic mode approved extracted review rules immediately.');
      } else {
        writeLine('[github] No repeated high-signal review guidance produced new pending candidates.');
      }
      return;
    }
    for (const rule of document.pending_candidates) writeLine(`[github] pending ${formatReviewRuleSummary(rule)}`);
    return;
  }

  const document = await readRepoReviewRulesDocument(paths.sourcePath);
  if (!document) {
    writeLine(`[github] No repo review rules file present for ${source.repoSlug}.`);
    writeLine(`[github] Run: nana review-rules scan ${locator}`);
    return;
  }

  if (first === 'list') {
    writeLine(`[github] Repo review rules for ${source.repoSlug}`);
    writeLine(`[github] Rules file: ${rulesPath}`);
    writeLine(`[github] Effective review-rules mode: ${effectiveMode}`);
    if (effectiveReviewerPolicy) {
      writeLine(`[github] Effective reviewer policy: ${reviewerPolicyReason(effectiveReviewerPolicy)}`);
    }
    writeLine(`[github] Approved rules=${document.approved_rules.length} pending candidates=${document.pending_candidates.length} disabled=${document.disabled_rules?.length ?? 0} archived=${document.archived_rules?.length ?? 0}.`);
    for (const rule of document.approved_rules) writeLine(`[github] approved ${formatReviewRuleSummary(rule)}`);
    for (const rule of document.pending_candidates) writeLine(`[github] pending ${formatReviewRuleSummary(rule)}`);
    for (const rule of document.disabled_rules ?? []) writeLine(`[github] disabled ${formatReviewRuleSummary(rule)}`);
    for (const rule of document.archived_rules ?? []) writeLine(`[github] archived ${formatReviewRuleSummary(rule)}`);
    return;
  }

  if (first === 'approve') {
    const selectors = args.slice(2).map((value) => value.trim()).filter(Boolean);
    if (selectors.length === 0) throw new Error(`${buildRepoReviewRulesUsage()}\n\nMissing rule id(s) to approve.`);
    const approveAll = selectors.includes('all');
    const selectedIds = new Set(selectors.filter((value) => value !== 'all'));
    const approved: GithubRepoReviewRule[] = [...document.approved_rules];
    const remaining: GithubRepoReviewRule[] = [];
    let promoted = 0;
    for (const rule of document.pending_candidates) {
      if (approveAll || selectedIds.has(rule.id)) {
        approved.push({
          ...rule,
          updated_at: now.toISOString(),
        });
        promoted += 1;
      } else {
        remaining.push(rule);
      }
    }
    if (promoted === 0) {
      throw new Error(`No pending review rules matched ${selectors.join(', ')}.`);
    }
    await writeRepoReviewRulesDocument(paths.sourcePath, {
      ...document,
      approved_rules: approved.sort((left, right) => left.title.localeCompare(right.title)),
      pending_candidates: remaining,
      updated_at: now.toISOString(),
    });
    writeLine(`[github] Approved ${promoted} repo review rule(s) for ${source.repoSlug}.`);
    writeLine(`[github] Rules file: ${rulesPath}`);
    return;
  }

  if (first === 'disable' || first === 'enable' || first === 'archive') {
    const selectors = args.slice(2).map((value) => value.trim()).filter(Boolean);
    if (selectors.length === 0) throw new Error(`${buildRepoReviewRulesUsage()}\n\nMissing rule id(s) for ${first}.`);
    const targetState = first === 'disable' ? 'disabled' : first === 'enable' ? 'approved' : 'archived';
    const moved = rewriteRuleLifecycleState(document, targetState, selectors, now);
    if (moved.moved === 0) throw new Error(`No review rules matched ${selectors.join(', ')}.`);
    await writeRepoReviewRulesDocument(paths.sourcePath, moved.document);
    writeLine(`[github] ${first === 'enable' ? 'Enabled' : first === 'disable' ? 'Disabled' : 'Archived'} ${moved.moved} review rule(s) for ${source.repoSlug}.`);
    writeLine(`[github] Rules file: ${rulesPath}`);
    return;
  }

  if (first === 'explain') {
    const ruleId = args[2]?.trim();
    if (!ruleId) throw new Error(`${buildRepoReviewRulesUsage()}\n\nMissing rule id for explain.`);
    const found = findReviewRuleById(document, ruleId);
    if (!found) throw new Error(`No review rule found for ${ruleId}.`);
    writeLine(`[github] Rule ${found.rule.id} (${found.state})`);
    writeLine(`[github] Title: ${found.rule.title}`);
    writeLine(`[github] Category: ${found.rule.category}`);
    writeLine(`[github] Confidence: ${found.rule.confidence.toFixed(2)}`);
    writeLine(`[github] Reviewer count: ${found.rule.reviewer_count}`);
    writeLine(`[github] Extraction origin: ${found.rule.extraction_origin}`);
    writeLine(`[github] Extraction reason: ${found.rule.extraction_reason}`);
    writeLine(`[github] Path scopes: ${found.rule.path_scopes.join(', ') || '(none)'}`);
    for (const evidence of found.rule.evidence) {
      writeLine(`[github] Evidence: kind=${evidence.kind} pr=#${evidence.pr_number} reviewer=@${evidence.reviewer ?? 'unknown'} path=${evidence.path ?? '(none)'} line=${evidence.line ?? '(none)'} provenance=${evidence.code_context_provenance ?? 'unknown'} ref=${evidence.code_context_ref ?? '(none)'}`);
      writeLine(`[github]   excerpt: ${evidence.excerpt}`);
      if (evidence.code_context_excerpt) {
        for (const line of evidence.code_context_excerpt.split('\n')) writeLine(`[github]   code: ${line}`);
      }
    }
    return;
  }

  throw new Error(`Unknown review-rules subcommand: ${first}\n${buildRepoReviewRulesUsage()}`);
}

function readGitDiffStat(repoCheckoutPath: string, baseRef: string | undefined): { diffStat: string; changedFiles: string[] } {
  const range = baseRef ? `${baseRef}...HEAD` : 'HEAD';
  const diffStatResult = gitExecAllowFailure(repoCheckoutPath, ['diff', '--shortstat', range]);
  const changedFilesResult = gitExecAllowFailure(repoCheckoutPath, ['diff', '--name-only', range]);
  return {
    diffStat: diffStatResult.ok ? diffStatResult.stdout.trim() : '',
    changedFiles: changedFilesResult.ok
      ? changedFilesResult.stdout.split('\n').map((line) => line.trim()).filter(Boolean)
      : [],
  };
}

async function buildRetrospectiveForManifest(
  manifest: GithubWorkonManifest,
  repoPaths: ManagedRepoPaths,
): Promise<string> {
  const runPaths = githubRunPaths(repoPaths.repoRoot, manifest.run_id);
  const sandboxMetadata = await readSandboxMetadata(manifest.sandbox_path);
  const threadUsage = await readThreadUsageArtifact(runPaths)
    ?? await writeThreadUsageArtifact(runPaths, manifest.sandbox_path, new Date());
  const threadRows = threadUsage?.rows ?? [];
  const issueStats = typeof manifest.issue_association_number === 'number'
    ? await readIssueTokenStats(repoPaths, manifest.repo_slug, manifest.issue_association_number)
    : null;
  const { diffStat, changedFiles } = readGitDiffStat(manifest.sandbox_repo_path, sandboxMetadata?.base_ref);
  const laneStates = [...(await loadLaneRuntimeStates(manifest, runPaths)).values()];
  const leaderStatus = await readLeaderStatus(runPaths);
  const publisherStatus = await readPublisherStatus(runPaths);
  const schedulerState = await readSchedulerStatus(runPaths);
  const laneEvents = await readLaneRuntimeEventsAfter(runPaths, 0);
  const consistency = await checkGithubWorkonRuntimeConsistency({ manifest, runPaths });
  const markdown = buildRetrospectiveMarkdown({
    manifest,
    sandboxMetadata,
    threadRows,
    issueStats,
    diffStat,
    changedFiles,
    laneStates,
    leaderStatus,
    publisherStatus,
    schedulerState,
    laneEvents,
    consistency,
  });
  await writeFile(retrospectivePath(runPaths), markdown, 'utf-8');
  return markdown;
}

async function showGithubRetrospective(
  parsed: GithubRetrospectiveCommand,
  dependencies: Pick<GithubCommandDependencies, 'writeLine' | 'env' | 'homeDir'> & {
    writeLine: (line: string) => void;
    env: NodeJS.ProcessEnv;
  },
): Promise<void> {
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const manifest = await resolveManifestForSync(nanaHome, {
    subcommand: 'sync',
    runId: parsed.runId,
    useLastRun: parsed.useLastRun || !parsed.runId,
    reviewer: undefined,
    resumeLast: false,
    codexArgs: [],
  });
  const repoPaths = managedRepoPaths(nanaHome, join(manifest.repo_owner, manifest.repo_name));
  const apiBaseUrl = dependencies.env.GITHUB_API_URL?.trim() || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(dependencies.env, apiBaseUrl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: fetch };
  const reconciledManifest = await reconcileManifestWithGithubState(manifest, repoPaths, apiContext, new Date()).catch(() => manifest);
  const markdown = await buildRetrospectiveForManifest(reconciledManifest, repoPaths);
  for (const line of markdown.trim().split('\n')) {
    dependencies.writeLine(line);
  }
}

async function refreshGithubVerificationArtifacts(
  parsed: GithubVerifyRefreshCommand,
  dependencies: Pick<GithubCommandDependencies, 'writeLine' | 'env' | 'homeDir'> & {
    writeLine: (line: string) => void;
    env: NodeJS.ProcessEnv;
  },
): Promise<void> {
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const manifest = await resolveManifestForSync(nanaHome, {
    subcommand: 'sync',
    runId: parsed.runId,
    useLastRun: parsed.useLastRun || !parsed.runId,
    reviewer: undefined,
    resumeLast: false,
    codexArgs: [],
  });
  const repoPaths = managedRepoPaths(nanaHome, join(manifest.repo_owner, manifest.repo_name));
  const runPaths = githubRunPaths(repoPaths.repoRoot, manifest.run_id);
  const beforeFingerprint = JSON.stringify({
    plan: manifest.verification_plan,
    dir: manifest.verification_scripts_dir,
  });
  const updatedManifest = await ensureVerificationArtifactsCurrent(manifest, repoPaths, runPaths, dependencies.env);
  const afterFingerprint = JSON.stringify({
    plan: updatedManifest.verification_plan,
    dir: updatedManifest.verification_scripts_dir,
  });
  const refreshed = beforeFingerprint !== afterFingerprint;
  dependencies.writeLine(
    `[github] Verification artifacts for run ${manifest.run_id} ${refreshed ? 'refreshed' : 'already current'}.`,
  );
  if (updatedManifest.verification_plan) {
    dependencies.writeLine(
      `[github] Verification source files: ${updatedManifest.verification_plan.source_files.map((file) => `${file.path}:${file.checksum}`).join(', ') || '(none)'}`,
    );
  }
  if (updatedManifest.verification_scripts_dir) {
    dependencies.writeLine(`[github] Verification scripts directory: ${updatedManifest.verification_scripts_dir}`);
  }
}

export async function buildLaneExecutionInstructions(
  manifest: GithubWorkonManifest,
  lane: GithubPipelineLane,
  task: string | undefined,
): Promise<string> {
  const artifact = manifest.lane_prompt_artifacts.find((candidate) => candidate.alias === lane.alias && candidate.role === lane.role);
  const promptRoles = lane.prompt_roles ?? [];
  const promptBody = artifact
    ? await readFile(artifact.prompt_path, 'utf-8')
    : promptRoles.length > 0
      ? await readPromptSurface(promptRoles[0]!)
      : '';
  return [
    '# NANA Work-on Lane',
    '',
    `Run id: ${manifest.run_id}`,
    `Repo: ${manifest.repo_slug}`,
    `Sandbox path: ${manifest.sandbox_path}`,
    `Repo checkout path: ${manifest.sandbox_repo_path}`,
    `Lane alias: ${lane.alias}`,
    `Lane role: ${lane.role}`,
    `Lane phase: ${lane.phase}`,
    `Lane mode: ${lane.mode}`,
    `Lane owner: ${lane.owner}`,
    `Lane purpose: ${lane.purpose}`,
    '',
    'Operating contract:',
    '- This lane runs in a separate Codex process with its own CODEX_HOME and MCP profile.',
    '- Stay inside this lane concern and do not broaden scope.',
    lane.mode === 'review'
      ? '- Review only. Do not edit files. Return concrete findings with file references.'
      : '- Implement or remediate only the work that belongs to this lane, then run the worker-done verification gate.',
    task ? `- Caller task: ${task}` : '',
    '',
    ...(await buildLaneRepoReviewRuleInstructionLines(manifest, lane)),
    promptBody.trim(),
    '',
  ].filter(Boolean).join('\n');
}

async function executeGithubLane(
  parsed: GithubLaneExecCommand,
  dependencies: Pick<GithubCommandDependencies, 'writeLine' | 'env' | 'homeDir'> & {
    writeLine: (line: string) => void;
    env: NodeJS.ProcessEnv;
  },
): Promise<void> {
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const manifest = await resolveManifestForSync(nanaHome, {
    subcommand: 'sync',
    runId: parsed.runId,
    useLastRun: parsed.useLastRun || !parsed.runId,
    reviewer: undefined,
    resumeLast: false,
    codexArgs: [],
  });
  const apiBaseUrl = dependencies.env.GITHUB_API_URL?.trim() || manifest.api_base_url || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(dependencies.env, apiBaseUrl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: fetch };
  const currentTarget = await fetchTargetContext(parseGithubTargetUrl(manifest.target_url), apiContext);
  const lane = parsed.laneAlias === 'publisher'
    ? publisherLane(manifest)
    : manifest.consideration_pipeline.find((candidate) => candidate.alias === parsed.laneAlias);
  if (!lane) {
    throw new Error(`Lane ${parsed.laneAlias} is not present in run ${manifest.run_id}.`);
  }
  const sourceCodexHome = resolveUserCodexHomeDir(dependencies.env, dependencies.homeDir);
  const repoPaths = managedRepoPaths(nanaHome, join(manifest.repo_owner, manifest.repo_name));
  const runPaths = githubRunPaths(repoPaths.repoRoot, manifest.run_id);
  if (parsed.laneAlias === 'publisher') {
    const updated = await publishSandboxAndAwaitCi({
      manifest,
      target: currentTarget,
      repoPaths,
      runPaths,
      apiContext,
      writeLine: dependencies.writeLine,
      env: dependencies.env,
      homeDir: dependencies.homeDir,
      now: new Date(),
    });
    dependencies.writeLine(`[github] Publisher lane completed for run ${updated.run_id}.`);
    return;
  }
  const result = await runGithubLaneProcess({
    manifest,
    lane,
    task: parsed.task,
    codexArgs: parsed.codexArgs,
    sourceCodexHome,
    env: dependencies.env,
    homeDir: dependencies.homeDir,
    repoPaths,
    runPaths,
  });
  dependencies.writeLine(`[github] Lane ${lane.alias} ${result.status} via isolated CODEX_HOME ${result.laneCodexHome}.`);
  dependencies.writeLine(`[github] Lane result: ${result.resultPath}`);
  if (result.output.trim()) {
    for (const line of result.output.trim().split('\n')) dependencies.writeLine(line);
  }
}

async function runGithubLaneProcess(input: {
  manifest: GithubWorkonManifest;
  lane: GithubPipelineLane;
  task?: string;
  codexArgs?: string[];
  sourceCodexHome: string;
  env: NodeJS.ProcessEnv;
  homeDir?: string;
  repoPaths: ManagedRepoPaths;
  runPaths: GithubRunPaths;
}): Promise<{ output: string; resultPath: string; laneCodexHome: string; status: GithubLaneRuntimeStatus }> {
  const profile = resolveLaneCodexProfile(input.lane);
  const laneCodexHome = await ensureSandboxCodexHome(
    input.manifest.sandbox_path,
    input.sourceCodexHome,
    profile,
    [input.manifest.sandbox_repo_path, input.manifest.sandbox_path],
    `lane-${input.lane.alias}`,
  );
  const sandboxNanaBin = await ensureSandboxNanaShim(input.manifest.sandbox_path);
  const instructionsPath = laneInstructionsPath(input.runPaths, input.lane.alias);
  const resultPath = laneResultPath(input.runPaths, input.lane.alias);
  const stdoutPath = laneStdoutPath(input.runPaths, input.lane.alias);
  const stderrPath = laneStderrPath(input.runPaths, input.lane.alias);
  await mkdir(dirname(instructionsPath), { recursive: true });
  await writeFile(
    instructionsPath,
    await buildLaneExecutionInstructions(input.manifest, input.lane, input.task),
    'utf-8',
  );
  const nowIso = new Date().toISOString();
  const baseLaneState = await readLaneRuntimeState(input.runPaths, input.lane.alias)
    ?? createPendingLaneRuntimeState(input.manifest, input.runPaths, input.lane, new Date());
  const lanePrompt = input.task?.trim() || `Execute the ${input.lane.alias} lane for ${input.manifest.repo_slug} ${input.manifest.target_kind} #${input.manifest.target_number}`;
  const laneEnv = {
    ...input.env,
    CODEX_HOME: laneCodexHome,
    [GITHUB_APPEND_ENV]: instructionsPath,
    NANA_PROJECT_AGENTS_ROOT: input.manifest.sandbox_repo_path,
    PATH: input.env.PATH ? `${sandboxNanaBin}:${input.env.PATH}` : sandboxNanaBin,
  };
  const nanaCliPath = join(getPackageRoot(), 'dist', 'cli', 'nana.js');
  const execArgs = [
    nanaCliPath,
    'exec',
    '-C',
    input.manifest.sandbox_repo_path,
    ...ensureGithubLaunchBypass(input.codexArgs ?? []),
    lanePrompt,
  ];
  let output = '';
  let stdout = '';
  let stderr = '';
  let status: GithubLaneRuntimeStatus | null = null;
  const child = spawn(process.execPath, execArgs, {
    cwd: input.manifest.sandbox_path,
    env: laneEnv,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const laneState: GithubLaneRuntimeState = {
    ...baseLaneState,
    status: 'running',
    pid: child.pid,
    started_at: nowIso,
    updated_at: nowIso,
    instructions_path: instructionsPath,
    result_path: resultPath,
    stdout_path: stdoutPath,
    stderr_path: stderrPath,
  };
  await writeLaneRuntimeState(input.runPaths, laneState);
  await appendLaneRuntimeEvent(input.runPaths, {
    type: 'lane_started',
    lane_id: laneState.lane_id,
    alias: laneState.alias,
    role: laneState.role,
    profile: laneState.profile,
    activation: laneState.activation,
    phase: laneState.phase,
    at: laneState.started_at,
  });
  await new Promise<void>((resolve) => {
    child.stdout?.on('data', (chunk: Buffer | string) => {
      stdout += typeof chunk === 'string' ? chunk : chunk.toString('utf-8');
    });
    child.stderr?.on('data', (chunk: Buffer | string) => {
      stderr += typeof chunk === 'string' ? chunk : chunk.toString('utf-8');
    });
    child.on('error', (error) => {
      status = 'failed';
      stderr += `${stderr ? '\n' : ''}${error.message}`;
      resolve();
    });
    child.on('close', (code) => {
      status = code === 0 ? 'completed' : 'failed';
      resolve();
    });
  });
  output = [stdout.trim(), stderr.trim()].filter(Boolean).join('\n\n');
  const finalStatus = String(status ?? 'failed') as GithubLaneRuntimeStatus;
  await mkdir(dirname(resultPath), { recursive: true });
  await writeFile(resultPath, output, 'utf-8');
  await writeFile(stdoutPath, stdout, 'utf-8');
  await writeFile(stderrPath, stderr, 'utf-8');
  const completedState: GithubLaneRuntimeState = {
    ...laneState,
    status: finalStatus,
    last_error: finalStatus === 'failed' ? stderr.trim() || output.trim() : undefined,
    completed_head_sha: finalStatus === 'completed' ? readRepoHeadSha(input.manifest.sandbox_repo_path) : undefined,
    updated_at: new Date().toISOString(),
    completed_at: new Date().toISOString(),
  };
  await writeLaneRuntimeState(input.runPaths, completedState);
  await appendLaneRuntimeEvent(input.runPaths, {
    type: finalStatus === 'completed' ? 'lane_completed' : 'lane_failed',
    lane_id: completedState.lane_id,
    alias: completedState.alias,
    role: completedState.role,
    profile: completedState.profile,
    at: completedState.completed_at,
    result_path: resultPath,
  });
  return { output, resultPath, laneCodexHome, status: finalStatus };
}

async function runGithubLaneProcessNoop(input: {
  manifest: GithubWorkonManifest;
  lane: GithubPipelineLane;
  task?: string;
  codexArgs?: string[];
  sourceCodexHome: string;
  env: NodeJS.ProcessEnv;
  homeDir?: string;
  repoPaths: ManagedRepoPaths;
  runPaths: GithubRunPaths;
}): Promise<{ output: string; resultPath: string; laneCodexHome: string; status: GithubLaneRuntimeStatus }> {
  const resultPath = laneResultPath(input.runPaths, input.lane.alias);
  await mkdir(dirname(resultPath), { recursive: true });
  await writeFile(resultPath, '', 'utf-8');
  return { output: '', resultPath, laneCodexHome: 'test://lane-noop', status: 'completed' };
}

async function runGithubPublisherLane(
  input: {
    manifest: GithubWorkonManifest;
    target: GithubTargetContext;
    repoPaths: ManagedRepoPaths;
    runPaths: GithubRunPaths;
    sourceCodexHome: string;
    env: NodeJS.ProcessEnv;
    homeDir?: string;
    writeLine: (line: string) => void;
    now: Date;
    apiContext: GithubApiContext;
  },
): Promise<GithubWorkonManifest> {
  const profile = GITHUB_CODEX_PROFILES.publisher;
  const laneCodexHome = await ensureSandboxCodexHome(
    input.manifest.sandbox_path,
    input.sourceCodexHome,
    profile,
    [input.manifest.sandbox_repo_path, input.manifest.sandbox_path],
    'lane-publisher',
  );
  const sandboxNanaBin = await ensureSandboxNanaShim(input.manifest.sandbox_path);
  const inboxPath = publisherInboxPath(input.runPaths);
  await mkdir(dirname(inboxPath), { recursive: true });
  await writeFile(
    inboxPath,
    `# Publisher Inbox\n\nRun id: ${input.manifest.run_id}\nTarget: ${input.manifest.target_url}\nAction: publish branch, create/update draft PR, and wait for CI until green.\n`,
    'utf-8',
  );
  const laneState: GithubLaneRuntimeState = {
    ...(await readLaneRuntimeState(input.runPaths, 'publisher') ?? createPendingLaneRuntimeState(input.manifest, input.runPaths, publisherLane(input.manifest), input.now)),
    status: 'running',
    pid: process.pid,
    started_at: input.now.toISOString(),
    updated_at: input.now.toISOString(),
  };
  await writeLaneRuntimeState(input.runPaths, laneState);
  const existingPublisherStatus = await readPublisherStatus(input.runPaths);
  const isRecoveryRun = Boolean(existingPublisherStatus?.started || (input.manifest.publication_state && input.manifest.publication_state !== 'not_started'));
  const publisherRetryPolicy = resolveLaneRetryPolicy(laneState, input.manifest.publication_state);
  await writePublisherStatus(input.runPaths, {
    run_id: input.manifest.run_id,
    session_active: true,
    pid: process.pid,
    started: true,
    pr_opened: Boolean(input.manifest.published_pr_number),
    ci_waiting: input.manifest.publication_state === 'ci_waiting',
    ci_green: input.manifest.publication_state === 'ci_green',
    blocked: false,
    blocked_reason: undefined,
    blocked_reason_category: undefined,
    blocked_retryable: undefined,
    recovery_count: (existingPublisherStatus?.recovery_count ?? 0) + (isRecoveryRun ? 1 : 0),
    retry_count: existingPublisherStatus?.retry_count ?? 0,
    retry_policy: publisherRetryPolicy.name,
    current_head_sha: readRepoHeadSha(input.manifest.sandbox_repo_path),
    current_branch: input.manifest.published_pr_head_ref,
    current_pr_number: input.manifest.published_pr_number,
    milestones: existingPublisherStatus?.milestones ?? [],
    updated_at: input.now.toISOString(),
  });
  await appendLaneRuntimeEvent(input.runPaths, {
    type: 'lane_started',
    lane_id: laneState.lane_id,
    alias: laneState.alias,
    role: laneState.role,
    profile: laneState.profile,
    activation: laneState.activation,
    phase: laneState.phase,
    at: laneState.started_at,
  });
  const publisherEnv = {
    ...input.env,
    CODEX_HOME: laneCodexHome,
    NANA_PROJECT_AGENTS_ROOT: input.manifest.sandbox_repo_path,
    PATH: input.env.PATH ? `${sandboxNanaBin}:${input.env.PATH}` : sandboxNanaBin,
  };
  try {
    const updated = await publishSandboxAndAwaitCi({
      manifest: input.manifest,
      target: input.target,
      repoPaths: input.repoPaths,
      runPaths: input.runPaths,
      apiContext: input.apiContext,
      writeLine: input.writeLine,
      env: publisherEnv,
      homeDir: input.homeDir,
      now: input.now,
    });
    const latestPublisherStatus = await readPublisherStatus(input.runPaths);
    await writeFile(laneResultPath(input.runPaths, 'publisher'), `published_pr=${updated.published_pr_number ?? ''}\nurl=${updated.published_pr_url ?? ''}\nstate=${updated.publication_state ?? ''}\n`, 'utf-8');
    await writeLaneRuntimeState(input.runPaths, {
      ...laneState,
      status: 'completed',
      updated_at: input.now.toISOString(),
      completed_at: input.now.toISOString(),
    });
    await writePublisherStatus(input.runPaths, {
      run_id: input.manifest.run_id,
      session_active: false,
      pid: undefined,
      started: true,
      pr_opened: Boolean(updated.published_pr_number),
      ci_waiting: false,
      ci_green: updated.publication_state === 'ci_green',
      blocked: false,
      blocked_reason: undefined,
      blocked_reason_category: undefined,
      blocked_retryable: undefined,
      recovery_count: (existingPublisherStatus?.recovery_count ?? 0) + (isRecoveryRun ? 1 : 0),
      retry_count: await readLaneRuntimeState(input.runPaths, 'publisher').then((state) => state?.retry_count ?? 0).catch(() => 0),
      retry_policy: publisherRetryPolicy.name,
      current_head_sha: readRepoHeadSha(input.manifest.sandbox_repo_path),
      current_branch: updated.published_pr_head_ref,
      current_pr_number: updated.published_pr_number,
      diagnostics_path: undefined,
      last_milestone: updated.publication_state === 'ci_green' ? 'ci_green' : updated.publication_state,
      milestones: latestPublisherStatus?.milestones ?? existingPublisherStatus?.milestones ?? [],
      updated_at: input.now.toISOString(),
      last_resume_at: input.now.toISOString(),
    });
    await appendLaneRuntimeEvent(input.runPaths, {
      type: 'lane_completed',
      lane_id: laneState.lane_id,
      alias: laneState.alias,
      role: laneState.role,
      profile: laneState.profile,
      at: input.now.toISOString(),
      result_path: laneResultPath(input.runPaths, 'publisher'),
    });
    return updated;
  } catch (error) {
    const latestBlockedStatus = await readPublisherStatus(input.runPaths).catch(() => null);
    await writeFile(
      laneResultPath(input.runPaths, 'publisher'),
      error instanceof Error ? error.message : String(error),
      'utf-8',
    );
    await writeLaneRuntimeState(input.runPaths, {
      ...laneState,
      status: 'failed',
      last_error: error instanceof Error ? error.message : String(error),
      updated_at: input.now.toISOString(),
      completed_at: input.now.toISOString(),
    });
    await writePublisherStatus(input.runPaths, {
      run_id: input.manifest.run_id,
      session_active: false,
      pid: undefined,
      started: true,
      pr_opened: Boolean(input.manifest.published_pr_number),
      ci_waiting: input.manifest.publication_state === 'ci_waiting',
      ci_green: false,
      blocked: true,
      blocked_reason: error instanceof Error ? error.message : String(error),
      blocked_reason_category: classifyPublisherBlockedReason(error instanceof Error ? error.message : String(error)),
      blocked_retryable: latestBlockedStatus?.blocked_retryable,
      recovery_count: (existingPublisherStatus?.recovery_count ?? 0) + (isRecoveryRun ? 1 : 0),
      retry_count: (await readLaneRuntimeState(input.runPaths, 'publisher').then((state) => state?.retry_count ?? 0).catch(() => 0)),
      retry_policy: publisherRetryPolicy.name,
      current_head_sha: readRepoHeadSha(input.manifest.sandbox_repo_path),
      current_branch: input.manifest.published_pr_head_ref,
      current_pr_number: input.manifest.published_pr_number,
      diagnostics_path: latestBlockedStatus?.diagnostics_path,
      last_milestone: latestBlockedStatus?.last_milestone,
      milestones: latestBlockedStatus?.milestones ?? existingPublisherStatus?.milestones,
      updated_at: input.now.toISOString(),
      last_resume_at: input.now.toISOString(),
    });
    await appendLaneRuntimeEvent(input.runPaths, {
      type: 'lane_failed',
      lane_id: laneState.lane_id,
      alias: laneState.alias,
      role: laneState.role,
      profile: laneState.profile,
      at: input.now.toISOString(),
      result_path: laneResultPath(input.runPaths, 'publisher'),
    });
    throw error;
  }
}

async function resumeLeaderFromInboxIfNeeded(
  input: {
    runPaths: GithubRunPaths;
    manifest: GithubWorkonManifest;
    env: NodeJS.ProcessEnv;
    launchWithHud: (args: string[]) => Promise<void>;
  },
): Promise<boolean> {
  const { unread, cursor, lastEventId } = await readUnreadInboxContent(
    leaderInboxPath(input.runPaths),
    leaderInboxCursorPath(input.runPaths),
  );
  if (!unread.trim()) return false;
  const statusBeforeResume = await readLeaderStatus(input.runPaths);
  if (statusBeforeResume?.session_active) return false;
  const resumeInstructionsPath = join(input.runPaths.runDir, 'leader-inbox-resume.md');
  await writeFile(
    resumeInstructionsPath,
    [
      '<github_workon_leader_inbox>',
      `Run id: ${input.manifest.run_id}`,
      `Repo: ${input.manifest.repo_slug}`,
      `Target: ${input.manifest.target_kind} #${input.manifest.target_number}`,
      'Unread isolated lane deltas follow. Integrate only the newly completed lane results, update the implementation as needed, then continue verification.',
      '</github_workon_leader_inbox>',
      '',
      unread.trim(),
      '',
    ].join('\n'),
    'utf-8',
  );
  const previousAppendix = input.env[GITHUB_APPEND_ENV];
  input.env[GITHUB_APPEND_ENV] = resumeInstructionsPath;
  try {
    if (statusBeforeResume) {
      await writeLeaderStatus(input.runPaths, {
        ...statusBeforeResume,
        session_active: true,
        pid: process.pid,
        updated_at: new Date().toISOString(),
      });
    }
    await input.launchWithHud(ensureGithubLaunchBypass([
      'resume',
      '--last',
      `Integrate newly completed isolated lane results for ${input.manifest.repo_slug} ${input.manifest.target_kind} #${input.manifest.target_number}.`,
    ]));
  } finally {
    if (typeof previousAppendix === 'string') input.env[GITHUB_APPEND_ENV] = previousAppendix;
    else delete input.env[GITHUB_APPEND_ENV];
  }
  await advanceInboxCursor(leaderInboxCursorPath(input.runPaths), cursor, lastEventId);
  const previousStatus = await readLeaderStatus(input.runPaths);
  if (previousStatus) {
    await writeLeaderStatus(input.runPaths, {
      ...previousStatus,
      session_active: false,
      pid: undefined,
      last_resume_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    });
  }
  return true;
}

async function runGithubLaneCycle(
  input: {
    manifest: GithubWorkonManifest;
    repoPaths: ManagedRepoPaths;
    runPaths: GithubRunPaths;
    activation: GithubPipelineActivation;
    sourceCodexHome: string;
    env: NodeJS.ProcessEnv;
    homeDir?: string;
    writeLine: (line: string) => void;
    runLaneProcess: typeof runGithubLaneProcess;
  },
): Promise<GithubPipelineLane[]> {
  const leaderStatus = await readLeaderStatus(input.runPaths) ?? {
    run_id: input.manifest.run_id,
    bootstrap_complete: false,
    implementation_started: false,
    implementation_complete: false,
    ready_for_publication: false,
    blocked: false,
    updated_at: new Date().toISOString(),
  };
  await syncLeaderOwnedLaneStates(input.manifest, input.runPaths, leaderStatus);
  await invalidateOutdatedCompletedLanes(input.manifest, input.runPaths);
  const states = await loadLaneRuntimeStates(input.manifest, input.runPaths);
  for (const state of states.values()) {
    if (state.status === 'running' && !isProcessAlive(state.pid)) {
      const classified = classifyLaneFailure(state);
      const retryPolicy = resolveLaneRetryPolicy(state, input.manifest.publication_state);
      const retryCount = state.retry_count ?? 0;
      const nextState: GithubLaneRuntimeState = classified.retryable && retryCount < retryPolicy.max_retries
        ? {
          ...state,
          status: 'pending',
          pid: undefined,
          retry_count: retryCount + 1,
          last_error: state.last_error ?? 'Lane process exited unexpectedly.',
          failure_category: classified.category,
          updated_at: new Date().toISOString(),
          completed_at: undefined,
          retry_policy: retryPolicy.name,
          retry_exhausted: false,
          last_attempt_at: new Date().toISOString(),
        }
        : {
          ...state,
          status: 'failed',
          pid: undefined,
          last_error: state.last_error ?? 'Lane process exited unexpectedly.',
          failure_category: classified.category,
          updated_at: new Date().toISOString(),
          completed_at: new Date().toISOString(),
          retry_policy: retryPolicy.name,
          retry_exhausted: retryCount >= retryPolicy.max_retries,
          last_attempt_at: new Date().toISOString(),
        };
      await writeLaneRuntimeState(input.runPaths, nextState);
      await appendLaneRuntimeEvent(input.runPaths, {
        type: 'runtime_recovered',
        target: nextState.alias,
        reason: 'dead_lane_pid',
        at: nextState.updated_at,
      });
      await appendLaneRuntimeEvent(input.runPaths, {
        type: nextState.status === 'pending' ? 'lane_retried' : 'lane_failed',
        lane_id: nextState.lane_id,
        alias: nextState.alias,
        role: nextState.role,
        profile: nextState.profile,
        at: nextState.updated_at,
        result_path: nextState.result_path,
        retry_count: nextState.retry_count ?? 0,
      });
      states.set(nextState.alias, nextState);
      continue;
    }
    if (state.status === 'failed') {
      const classified = classifyLaneFailure(state);
      const retryPolicy = resolveLaneRetryPolicy(state, input.manifest.publication_state);
      const retryCount = state.retry_count ?? 0;
      if (classified.retryable && retryCount < retryPolicy.max_retries) {
        const retryState: GithubLaneRuntimeState = {
          ...state,
          status: 'pending',
          pid: undefined,
          retry_count: retryCount + 1,
          failure_category: classified.category,
          updated_at: new Date().toISOString(),
          completed_at: undefined,
          retry_policy: retryPolicy.name,
          retry_exhausted: false,
          last_attempt_at: new Date().toISOString(),
        };
        await writeLaneRuntimeState(input.runPaths, retryState);
        await appendLaneRuntimeEvent(input.runPaths, {
          type: 'lane_retried',
          lane_id: retryState.lane_id,
          alias: retryState.alias,
          role: retryState.role,
          profile: retryState.profile,
          at: retryState.updated_at,
          result_path: retryState.result_path,
          retry_count: retryState.retry_count ?? 0,
        });
        states.set(retryState.alias, retryState);
      }
    }
  }
  const lanes = input.manifest.consideration_pipeline.filter((lane) =>
    lane.activation === input.activation
    && lane.alias !== 'coder'
    && laneReady(states.get(lane.alias) ?? createPendingLaneRuntimeState(input.manifest, input.runPaths, lane, new Date()), states, leaderStatus));
  const results = await Promise.all(lanes.map(async (lane) => {
    const result = await input.runLaneProcess({
      manifest: input.manifest,
      lane,
      task: lane.purpose,
      env: input.env,
      homeDir: input.homeDir,
      repoPaths: input.repoPaths,
      runPaths: input.runPaths,
      sourceCodexHome: input.sourceCodexHome,
    });
    const existingState = await readLaneRuntimeState(input.runPaths, lane.alias)
      ?? createPendingLaneRuntimeState(input.manifest, input.runPaths, lane, new Date());
    const normalizedState: GithubLaneRuntimeState = {
      ...existingState,
      status: result.status,
      updated_at: new Date().toISOString(),
      completed_at: result.status === 'completed' ? new Date().toISOString() : existingState.completed_at,
      last_error: result.status === 'failed' ? result.output.trim() || existingState.last_error : undefined,
      failure_category: result.status === 'failed'
        ? classifyLaneFailure({
          ...existingState,
          last_error: result.output.trim() || existingState.last_error,
        }).category
        : undefined,
      retry_policy: existingState.retry_policy,
    };
    if (result.status === 'completed') {
      const snapshot = await captureLaneCompletionSnapshot(input.manifest, lane);
      normalizedState.completed_head_sha = snapshot.headSha;
      normalizedState.completed_changed_files = snapshot.changedFiles;
      normalizedState.completed_file_hashes = snapshot.fileHashes;
      normalizedState.completed_concern_descriptor = snapshot.descriptor;
      normalizedState.completed_concern_match = snapshot.match;
      normalizedState.invalidated_at = undefined;
      normalizedState.invalidated_reason = undefined;
      normalizedState.invalidation_concern_match = undefined;
      normalizedState.retry_exhausted = false;
      normalizedState.last_attempt_at = new Date().toISOString();
    } else {
      const classified = classifyLaneFailure({
        ...normalizedState,
        retryable: existingState.retryable,
      });
      const retryPolicy = resolveLaneRetryPolicy(normalizedState, input.manifest.publication_state);
      const retryCount = existingState.retry_count ?? 0;
      if (classified.retryable && retryCount < retryPolicy.max_retries) {
        normalizedState.status = 'pending';
        normalizedState.retry_count = retryCount + 1;
        normalizedState.completed_at = undefined;
        normalizedState.retry_policy = retryPolicy.name;
        normalizedState.retry_exhausted = false;
      } else {
        normalizedState.status = 'failed';
        normalizedState.retry_policy = retryPolicy.name;
        normalizedState.retry_exhausted = true;
      }
      normalizedState.last_attempt_at = new Date().toISOString();
    }
    await writeLaneRuntimeState(input.runPaths, normalizedState);
    if (result.status === 'completed') {
      input.writeLine(`[github] ${input.activation} lane ${lane.alias} completed with isolated profile ${resolveLaneCodexProfile(lane).name}.`);
      if (result.output.trim()) {
        input.writeLine(`[github] ${lane.alias} result saved to ${result.resultPath}`);
      }
      const resultEvent = await appendLaneRuntimeEvent(input.runPaths, {
        type: 'lane_result_available',
        lane_id: normalizedState.lane_id,
        alias: normalizedState.alias,
        role: normalizedState.role,
        at: normalizedState.updated_at,
        result_path: normalizedState.result_path,
      });
      await appendInboxDelta(leaderInboxPath(input.runPaths), lane, result.output, Number(resultEvent.id));
      return lane;
    }
    await appendLaneRuntimeEvent(input.runPaths, {
      type: normalizedState.status === 'pending' ? 'lane_retried' : 'lane_failed',
      lane_id: normalizedState.lane_id,
      alias: normalizedState.alias,
      role: normalizedState.role,
      profile: normalizedState.profile,
      at: normalizedState.updated_at,
      result_path: normalizedState.result_path,
      retry_count: normalizedState.retry_count ?? 0,
      failure_category: normalizedState.failure_category,
    });
    if (normalizedState.status === 'failed' && lane.blocking) {
      const leaderStatus = await readLeaderStatus(input.runPaths);
      if (leaderStatus) {
        await writeLeaderStatus(input.runPaths, {
          ...leaderStatus,
          blocked: true,
          blocked_reason: `${lane.alias} failed permanently: ${normalizedState.failure_category ?? 'unknown'}`,
          updated_at: new Date().toISOString(),
        });
      }
      const publisherStatus = await readPublisherStatus(input.runPaths);
      if (publisherStatus) {
        await writePublisherStatus(input.runPaths, {
          ...publisherStatus,
          blocked: true,
          blocked_reason: `${lane.alias} failed permanently: ${normalizedState.failure_category ?? 'unknown'}`,
          updated_at: new Date().toISOString(),
        });
      }
    }
    input.writeLine(`[github] ${input.activation} lane ${lane.alias} ${normalizedState.status}; result saved to ${result.resultPath}.`);
    return null;
  }));
  return results.filter((lane): lane is GithubPipelineLane => lane !== null);
}

async function runGithubSchedulerPass(
  input: {
    manifest: GithubWorkonManifest;
    target: GithubTargetContext;
    repoPaths: ManagedRepoPaths;
    runPaths: GithubRunPaths;
    sourceCodexHome: string;
    env: NodeJS.ProcessEnv;
    homeDir?: string;
    writeLine: (line: string) => void;
    runLaneProcess: typeof runGithubLaneProcess;
    launchWithHud: (args: string[]) => Promise<void>;
    now: Date;
    apiContext: GithubApiContext;
  },
): Promise<GithubWorkonManifest> {
  let manifest = input.manifest;
  await initializeLaneRuntimeArtifacts(manifest, input.runPaths, input.now);
  const bootstrapCompleted = await runGithubLaneCycle({
    manifest,
    repoPaths: input.repoPaths,
    runPaths: input.runPaths,
    activation: 'bootstrap',
    sourceCodexHome: input.sourceCodexHome,
    env: input.env,
    homeDir: input.homeDir,
    writeLine: input.writeLine,
    runLaneProcess: input.runLaneProcess,
  });
  if (bootstrapCompleted.length > 0) {
    const resumed = await resumeLeaderFromInboxIfNeeded({
      runPaths: input.runPaths,
      manifest,
      env: input.env,
      launchWithHud: input.launchWithHud,
    });
    if (resumed) {
      await invalidateOutdatedCompletedLanes(manifest, input.runPaths);
    }
  }
  const hardeningCompleted = await runGithubLaneCycle({
    manifest,
    repoPaths: input.repoPaths,
    runPaths: input.runPaths,
    activation: 'hardening',
    sourceCodexHome: input.sourceCodexHome,
    env: input.env,
    homeDir: input.homeDir,
    writeLine: input.writeLine,
    runLaneProcess: input.runLaneProcess,
  });
  if (hardeningCompleted.length > 0) {
    const resumed = await resumeLeaderFromInboxIfNeeded({
      runPaths: input.runPaths,
      manifest,
      env: input.env,
      launchWithHud: input.launchWithHud,
    });
    if (resumed) {
      await invalidateOutdatedCompletedLanes(manifest, input.runPaths);
    }
  }
  const leaderStatus = await readLeaderStatus(input.runPaths);
  const states = await loadLaneRuntimeStates(manifest, input.runPaths);
  const publisherState = states.get('publisher');
  const blockingSatisfied = manifest.consideration_pipeline
    .filter((lane) => lane.blocking && lane.alias !== 'coder')
    .every((lane) => states.get(lane.alias)?.status === 'completed');
  if (
    (manifest.create_pr_on_complete || manifest.target_kind === 'pr')
    && leaderStatus?.ready_for_publication
    && blockingSatisfied
    && publisherState
    && laneReady(publisherState, states, leaderStatus)
  ) {
    manifest = await runGithubPublisherLane({
      manifest,
      target: input.target,
      repoPaths: input.repoPaths,
      runPaths: input.runPaths,
      sourceCodexHome: input.sourceCodexHome,
      writeLine: input.writeLine,
      env: input.env,
      homeDir: input.homeDir,
      now: input.now,
      apiContext: input.apiContext,
    });
  }
  return manifest;
}

async function schedulerHasRemainingWork(
  manifest: GithubWorkonManifest,
  runPaths: GithubRunPaths,
): Promise<boolean> {
  const states = await loadLaneRuntimeStates(manifest, runPaths);
  if ([...states.values()].some((state) => state.alias !== 'publisher' && (state.status === 'pending' || state.status === 'running'))) {
    return true;
  }
  const { unread } = await readUnreadInboxContent(leaderInboxPath(runPaths), leaderInboxCursorPath(runPaths));
  if (unread.trim()) return true;
  if (manifest.create_pr_on_complete || manifest.target_kind === 'pr') {
    const publisherState = states.get('publisher');
    if (!publisherState || publisherState.status === 'pending' || publisherState.status === 'running') return true;
    if (manifest.publication_state !== 'ci_green' && await shouldContinueBlockedPublication(manifest, runPaths)) return true;
  }
  return false;
}

async function withGithubRunRuntimeEnv<T>(
  input: {
    manifest: GithubWorkonManifest;
    sourceCodexHome: string;
    env: NodeJS.ProcessEnv;
    homeDir?: string;
  },
  fn: (runtimeEnv: {
    sandboxCodexHome: string;
    sandboxNanaBin: string;
  }) => Promise<T>,
): Promise<T> {
  const previousCodexHome = input.env.CODEX_HOME;
  const previousProjectAgentsRoot = input.env.NANA_PROJECT_AGENTS_ROOT;
  const previousPath = input.env.PATH;
  const previousCwd = process.cwd();
  const sandboxCodexHome = await ensureSandboxCodexHome(
    input.manifest.sandbox_path,
    input.sourceCodexHome,
    GITHUB_CODEX_PROFILES.leader,
    [input.manifest.sandbox_repo_path, input.manifest.sandbox_path],
  );
  const sandboxNanaBin = await ensureSandboxNanaShim(input.manifest.sandbox_path);
  input.env.CODEX_HOME = sandboxCodexHome;
  input.env.NANA_PROJECT_AGENTS_ROOT = input.manifest.sandbox_repo_path;
  input.env.PATH = previousPath ? `${sandboxNanaBin}:${previousPath}` : sandboxNanaBin;
  process.chdir(input.manifest.sandbox_path);
  try {
    return await fn({ sandboxCodexHome, sandboxNanaBin });
  } finally {
    process.chdir(previousCwd);
    if (typeof previousCodexHome === 'string') input.env.CODEX_HOME = previousCodexHome;
    else delete input.env.CODEX_HOME;
    if (typeof previousProjectAgentsRoot === 'string') input.env.NANA_PROJECT_AGENTS_ROOT = previousProjectAgentsRoot;
    else delete input.env.NANA_PROJECT_AGENTS_ROOT;
    if (typeof previousPath === 'string') input.env.PATH = previousPath;
    else delete input.env.PATH;
  }
}

async function setPublicationState(
  runPaths: GithubRunPaths,
  manifest: GithubWorkonManifest,
  now: Date,
  publicationState: GithubPublicationState,
  publicationError?: string,
): Promise<GithubWorkonManifest> {
  const updated: GithubWorkonManifest = {
    ...manifest,
    updated_at: now.toISOString(),
    publication_state: publicationState,
    publication_updated_at: now.toISOString(),
    publication_error: publicationError,
  };
  await writeManifest(runPaths, updated);
  return updated;
}

async function publishSandboxAndAwaitCi(input: {
  manifest: GithubWorkonManifest;
  target: GithubTargetContext;
  repoPaths: ManagedRepoPaths;
  runPaths: GithubRunPaths;
  apiContext: GithubApiContext;
  writeLine: (line: string) => void;
  env: NodeJS.ProcessEnv;
  homeDir?: string;
  now: Date;
}): Promise<GithubWorkonManifest> {
  let manifest = await ensureVerificationArtifactsCurrent(
    input.manifest,
    input.repoPaths,
    input.runPaths,
    input.env,
  );
  try {
    const sandboxMetadata = await readSandboxMetadata(input.manifest.sandbox_path);
    if (!sandboxMetadata) {
      throw new Error(`Sandbox metadata missing for publication: ${input.manifest.sandbox_path}`);
    }

    runVerificationScriptIfPresent(
      manifest,
      'all.sh',
      input.homeDir ? { ...input.env, HOME: input.homeDir } : input.env,
    );
    await recordPublisherMilestone(input.runPaths, {
      runId: manifest.run_id,
      milestone: 'local_gate_passed',
      at: input.now,
      headSha: readRepoHeadSha(input.manifest.sandbox_repo_path),
      blocked: false,
    });
    manifest = await setPublicationState(
      input.runPaths,
      manifest,
      input.now,
      manifest.publication_state ?? 'implemented',
    );
    const { localBranch, remoteBranch } = resolvePublicationBranch(input.manifest, sandboxMetadata.branch_name);
    const { headSha, createdCommit } = await ensureCommittedForPublication(input.manifest.sandbox_repo_path, manifest);
    await recordPublisherMilestone(input.runPaths, {
      runId: manifest.run_id,
      milestone: 'commit_created',
      at: input.now,
      headSha,
      branch: remoteBranch,
      detail: createdCommit ? 'created publication commit' : 'reused existing head commit',
      blocked: false,
    });
    if (createdCommit) {
      input.writeLine(`[github] Created automatic publication commit on ${localBranch}.`);
    }
    manifest = await setPublicationState(input.runPaths, manifest, input.now, 'committed');

    const remoteHeadSha = readRemoteBranchHeadSha(input.manifest.sandbox_repo_path, remoteBranch);
    if (remoteHeadSha === headSha) {
      input.writeLine(`[github] Branch ${remoteBranch} already points at ${headSha}; skipping push.`);
    } else {
      pushPublicationBranch(input.manifest.sandbox_repo_path, localBranch, remoteBranch);
      input.writeLine(`[github] Pushed ${localBranch} to origin/${remoteBranch}.`);
    }
    await recordPublisherMilestone(input.runPaths, {
      runId: manifest.run_id,
      milestone: 'push_completed',
      at: input.now,
      headSha,
      branch: remoteBranch,
      detail: remoteHeadSha === headSha ? 'branch already pushed' : 'pushed branch to origin',
      blocked: false,
    });
    manifest = await setPublicationState(input.runPaths, manifest, input.now, 'pushed');

    let pr: GithubPullRequestPayload;
    if (manifest.target_kind === 'pr') {
      pr = await githubApiJson<GithubPullRequestPayload>(
        `/repos/${manifest.repo_slug}/pulls/${manifest.target_number}`,
        input.apiContext,
      );
    } else {
      const existing = await fetchPullRequestsForHeadBranch(
        manifest.repo_slug,
        manifest.repo_owner,
        remoteBranch,
        input.apiContext,
      );
      pr = existing[0]
        ? await updateDraftPullRequest(manifest, existing[0].number, input.target, remoteBranch, input.apiContext)
        : await createDraftPullRequest(manifest, input.target, remoteBranch, input.apiContext);
      input.writeLine(
        existing[0]
          ? `[github] Updated draft PR #${pr.number}: ${pr.html_url}`
          : `[github] Created draft PR #${pr.number}: ${pr.html_url}`,
      );
      await recordPublisherMilestone(input.runPaths, {
        runId: manifest.run_id,
        milestone: existing[0] ? 'draft_pr_updated' : 'draft_pr_opened',
        at: input.now,
        headSha,
        branch: remoteBranch,
        prNumber: pr.number,
        prOpened: true,
        blocked: false,
      });
    }

    if (manifest.target_kind === 'issue') {
      await ensurePrSandboxLink(input.repoPaths, pr.number, manifest.sandbox_path, manifest.sandbox_id);
    } else {
      await recordPublisherMilestone(input.runPaths, {
        runId: manifest.run_id,
        milestone: 'draft_pr_updated',
        at: input.now,
        headSha,
        branch: remoteBranch,
        prNumber: pr.number,
        prOpened: true,
        blocked: false,
        detail: 'reused existing PR target',
      });
    }

    manifest = await setPublicationState(input.runPaths, {
      ...manifest,
      published_pr_number: pr.number,
      published_pr_url: pr.html_url,
      published_pr_head_ref: remoteBranch,
    }, input.now, 'pr_opened');

    const ciState = await readCiStateForHeadSha(manifest, headSha, input.apiContext);
    if (ciState === 'ci_green') {
      await recordPublisherMilestone(input.runPaths, {
        runId: manifest.run_id,
        milestone: 'ci_green',
        at: input.now,
        headSha,
        branch: remoteBranch,
        prNumber: pr.number,
        prOpened: true,
        ciGreen: true,
        ciWaiting: false,
        blocked: false,
        detail: 'CI already green for current head; skipped polling',
      });
      manifest = await setPublicationState(input.runPaths, manifest, input.now, 'ci_green');
      return manifest;
    }
    manifest = await setPublicationState(input.runPaths, manifest, input.now, 'ci_waiting');
    await recordPublisherMilestone(input.runPaths, {
      runId: manifest.run_id,
      milestone: 'ci_waiting',
      at: input.now,
      headSha,
      branch: remoteBranch,
      prNumber: pr.number,
      prOpened: true,
      ciWaiting: true,
      blocked: false,
    });
    const ciResult = await waitForGithubCiGreen({
      manifest,
      sandboxPath: manifest.sandbox_path,
      headSha,
      context: input.apiContext,
      env: input.env,
      writeLine: input.writeLine,
    });
    if (!ciResult.ok) {
      const publicationError = `CI ${ciResult.reason}: ${ciResult.diagnosticsPath}`;
      manifest = await setPublicationState(input.runPaths, manifest, input.now, 'blocked', publicationError);
      await recordPublisherMilestone(input.runPaths, {
        runId: manifest.run_id,
        milestone: 'ci_blocked',
        at: input.now,
        headSha,
        branch: remoteBranch,
        prNumber: pr.number,
        prOpened: true,
        ciWaiting: false,
        blocked: true,
        blockedReason: publicationError,
        blockedReasonCategory: classifyPublisherBlockedReason(publicationError),
        blockedRetryable: ciResult.retryable,
        diagnosticsPath: ciResult.diagnosticsPath,
      });
      throw new Error(manifest.publication_error);
    }

    if (manifest.verification_plan) {
      const successfulRuns = await fetchWorkflowRunsForSha(manifest.repo_slug, headSha, input.apiContext).catch(() => [] as GithubWorkflowRunPayload[]);
      const successfulJobs = (await Promise.all(
        successfulRuns
          .filter((run) => run.head_sha === headSha && run.conclusion === 'success')
          .map((run) => fetchWorkflowJobsForRun(manifest.repo_slug, run.id, input.apiContext).catch(() => [] as GithubWorkflowJobPayload[])),
      )).flat();
      const ciDurations = buildCiSuiteDurationsFromJobs(manifest.verification_plan.plan_fingerprint, successfulJobs, input.now);
      await writeCiSuiteDurations(input.repoPaths.repoCiSuiteDurationsPath, ciDurations).catch(() => {});
    }

    manifest = await setPublicationState(input.runPaths, manifest, input.now, 'ci_green');
    await recordPublisherMilestone(input.runPaths, {
      runId: manifest.run_id,
      milestone: 'ci_green',
      at: input.now,
      headSha,
      branch: remoteBranch,
      prNumber: pr.number,
      prOpened: true,
      ciWaiting: false,
      ciGreen: true,
      blocked: false,
    });
    return manifest;
  } catch (error) {
    const hadBlockedState = manifest.publication_state === 'blocked' && Boolean(manifest.publication_error);
    if (manifest.publication_state !== 'blocked' || !manifest.publication_error) {
      manifest = await setPublicationState(
        input.runPaths,
        manifest,
        input.now,
        'blocked',
        error instanceof Error ? error.message : String(error),
      ).catch(() => manifest);
    }
    if (!hadBlockedState) {
      await recordPublisherMilestone(input.runPaths, {
        runId: manifest.run_id,
        milestone: 'ci_blocked',
        at: input.now,
        headSha: readRepoHeadSha(input.manifest.sandbox_repo_path),
        branch: manifest.published_pr_head_ref,
        prNumber: manifest.published_pr_number,
        prOpened: Boolean(manifest.published_pr_number),
        ciWaiting: false,
        blocked: true,
        blockedReason: manifest.publication_error,
        blockedReasonCategory: classifyPublisherBlockedReason(manifest.publication_error ?? ''),
        blockedRetryable: false,
      }).catch(() => {});
    }
    throw error;
  }
}

async function shouldContinueBlockedPublication(
  manifest: GithubWorkonManifest,
  runPaths?: GithubRunPaths,
): Promise<boolean> {
  const state = manifest.publication_state;
  const error = manifest.publication_error ?? '';
  if (state === 'ci_green') return false;
  const publisherStatus = runPaths ? await readPublisherStatus(runPaths).catch(() => null) : null;
  if (publisherStatus) {
    const currentHeadSha = readRepoHeadSha(manifest.sandbox_repo_path);
    const sameHead = !publisherStatus.current_head_sha || !currentHeadSha || publisherStatus.current_head_sha === currentHeadSha;
    if (publisherStatus.last_milestone === 'ci_green') return false;
    if (!publisherStatus.blocked) {
      if (publisherStatus.session_active) return true;
      if (publisherStatus.last_milestone && publisherStatus.last_milestone !== 'ci_green') return true;
    } else {
      if (!sameHead) return true;
      if (typeof publisherStatus.blocked_retryable === 'boolean') return publisherStatus.blocked_retryable;
      if (publisherStatus.blocked_reason_category && publisherStatus.blocked_reason_category !== 'ci_failed_checks') {
        return true;
      }
      if (publisherStatus.blocked_reason_category === 'ci_failed_checks') return false;
    }
  }
  if (!state || state === 'not_started' || state === 'implemented' || state === 'committed' || state === 'pushed' || state === 'pr_opened' || state === 'ci_waiting') {
    return true;
  }
  if (state === 'blocked') {
    if (/CI timeout/i.test(error) || /sandbox .*busy/i.test(error)) return true;
    const failedChecksMatch = error.match(/CI failed_checks: (.+)$/i);
    if (failedChecksMatch?.[1]) {
      const diagnostics = await readFile(failedChecksMatch[1], 'utf-8').catch(() => '');
      const categories = [...diagnostics.matchAll(/category=([a-z]+)/g)].map((match) => match[1]);
      if (categories.length === 0) return true;
      return categories.every((category) => category === 'environmental' || category === 'flaky');
    }
  }
  return false;
}

export async function continueGithubPublicationLoop(
  input: {
    runId: string;
    env?: NodeJS.ProcessEnv;
    homeDir?: string;
    fetchImpl?: typeof fetch;
    execFileSyncImpl?: typeof execFileSync;
    writeLine?: (line: string) => void;
    now?: () => Date;
  },
): Promise<GithubWorkonManifest> {
  const env = input.env ?? process.env;
  const homeDir = input.homeDir;
  const now = input.now ?? (() => new Date());
  const writeLine = input.writeLine ?? (() => {});
  const nanaHome = resolveNanaHomeDir(env, homeDir);
  const manifest = await resolveManifestForSync(nanaHome, {
    subcommand: 'sync',
    runId: input.runId,
    useLastRun: false,
    reviewer: undefined,
    resumeLast: false,
    codexArgs: [],
  });

  if (!(manifest.create_pr_on_complete || manifest.target_kind === 'pr')) {
    return manifest;
  }

  const repoPaths = managedRepoPaths(nanaHome, join(manifest.repo_owner, manifest.repo_name));
  const runPaths = githubRunPaths(repoPaths.repoRoot, manifest.run_id);
  if (!(await shouldContinueBlockedPublication(manifest, runPaths))) {
    return manifest;
  }

  const apiBaseUrl = env.GITHUB_API_URL?.trim() || manifest.api_base_url || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(env, apiBaseUrl, input.execFileSyncImpl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: input.fetchImpl ?? fetch };
  const target = await fetchTargetContext(parseGithubTargetUrl(manifest.target_url), apiContext);
  return publishSandboxAndAwaitCi({
    manifest,
    target,
    repoPaths,
    runPaths,
    apiContext,
    writeLine,
    env,
    homeDir,
    now: now(),
  });
}

export async function continueGithubSchedulerLoop(
  input: {
    runId: string;
    env?: NodeJS.ProcessEnv;
    homeDir?: string;
    fetchImpl?: typeof fetch;
    execFileSyncImpl?: typeof execFileSync;
    writeLine?: (line: string) => void;
    now?: () => Date;
    wakeReason?: GithubSchedulerWakeReason;
    watchMode?: 'watch+poll' | 'poll-only';
    launchWithHud?: (args: string[]) => Promise<void>;
    runLaneProcess?: typeof runGithubLaneProcess;
  },
): Promise<{ manifest: GithubWorkonManifest; hasRemainingWork: boolean }> {
  const env = input.env ?? process.env;
  const homeDir = input.homeDir;
  const now = input.now ?? (() => new Date());
  const writeLine = input.writeLine ?? (() => {});
  const nanaHome = resolveNanaHomeDir(env, homeDir);
  const manifest = await resolveManifestForSync(nanaHome, {
    subcommand: 'sync',
    runId: input.runId,
    useLastRun: false,
    reviewer: undefined,
    resumeLast: false,
    codexArgs: [],
  });
  const apiBaseUrl = env.GITHUB_API_URL?.trim() || manifest.api_base_url || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(env, apiBaseUrl, input.execFileSyncImpl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: input.fetchImpl ?? fetch };
  const repoPaths = managedRepoPaths(nanaHome, join(manifest.repo_owner, manifest.repo_name));
  const runPaths = githubRunPaths(repoPaths.repoRoot, manifest.run_id);
  const sourceCodexHome = resolveUserCodexHomeDir(env, homeDir);
  const launchWithHud = input.launchWithHud ?? (await import('./index.js')).launchWithHud;
  const runLaneProcess = input.runLaneProcess ?? runGithubLaneProcess;
  const target = await fetchTargetContext(parseGithubTargetUrl(manifest.target_url), apiContext);
  const refreshedManifest = await ensureVerificationArtifactsCurrent(manifest, repoPaths, runPaths, env);
  const passStartedAt = now();
  await initializeLaneRuntimeArtifacts(refreshedManifest, runPaths, passStartedAt);
  const previousSchedulerState = await readSchedulerStatus(runPaths);
  const previouslyProcessedEventId = previousSchedulerState?.last_processed_event_id ?? 0;
  const concernRegistryDetails = await resolveGithubConcernRegistryDetails(refreshedManifest.sandbox_repo_path);
  await recoverStaleSessionStatuses(refreshedManifest, runPaths);
  const latestEventIdBeforePass = await readLatestLaneEventId(runPaths);
  const replayedEvents = await replaySchedulerUnseenEvents({
    manifest: refreshedManifest,
    runPaths,
    afterEventId: previouslyProcessedEventId,
    upToEventId: latestEventIdBeforePass,
  });
  const nextManifest = await withGithubRunRuntimeEnv(
    {
      manifest: refreshedManifest,
      sourceCodexHome,
      env,
      homeDir,
    },
    async () => runGithubSchedulerPass({
      manifest: refreshedManifest,
      target,
      repoPaths,
      runPaths,
      sourceCodexHome,
      env,
      homeDir,
      writeLine,
      runLaneProcess,
      launchWithHud,
      now: now(),
      apiContext,
    }),
  );
  const latestManifest = await readManifest(runPaths.manifestPath).catch(() => nextManifest);
  const leaderStatus = await readLeaderStatus(runPaths);
  const publisherStatus = await readPublisherStatus(runPaths);
  const latestEventIdAfterPass = await readLatestLaneEventId(runPaths);
  const unseenEvents = await readLaneRuntimeEventsAfter(runPaths, previouslyProcessedEventId);
  const currentPassEvents = unseenEvents.filter((event) => event.id > latestEventIdBeforePass);
  const wakeReason = input.wakeReason ?? 'startup';
  const passId = (previousSchedulerState?.last_completed_pass_id ?? 0) + 1;
  const launchedLanes = [...new Set(
    currentPassEvents
      .filter((event) => event.type === 'lane_started')
      .map((event) => String(event.alias ?? ''))
      .filter(Boolean),
  )];
  const invalidatedLanes = currentPassEvents
    .filter((event) => event.type === 'lane_invalidated')
    .map((event) => ({
      alias: String(event.alias ?? ''),
      reason: Array.isArray(event.changed_files)
        ? `files: ${event.changed_files.map((value) => String(value)).join(', ')}`
        : undefined,
    }))
    .filter((event) => event.alias);
  const retriedLanes = currentPassEvents
    .filter((event) => event.type === 'lane_retried')
    .map((event) => ({
      alias: String(event.alias ?? ''),
      retry_count: typeof event.retry_count === 'number' ? event.retry_count : undefined,
      failure_category: typeof event.failure_category === 'string' ? event.failure_category : undefined,
    }))
    .filter((event) => event.alias);
  const recoveryEvents = currentPassEvents
    .filter((event) => event.type === 'runtime_recovered')
    .map((event) => ({
      target: String(event.target ?? ''),
      reason: String(event.reason ?? 'unknown'),
    }))
    .filter((event) => event.target);
  const allRecoveryEvents = [
    ...replayedEvents
      .filter((event) => event.type === 'runtime_recovered')
      .map((event) => ({
        target: String(event.target ?? ''),
        reason: String(event.reason ?? 'unknown'),
      }))
      .filter((event) => event.target),
    ...recoveryEvents,
  ];
  await writeSchedulerPassArtifact(runPaths, {
    version: 1,
    run_id: latestManifest.run_id,
    pass_id: passId,
    wake_reason: wakeReason,
    watch_mode: input.watchMode ?? previousSchedulerState?.watch_mode ?? 'poll-only',
    started_at: passStartedAt.toISOString(),
    completed_at: now().toISOString(),
    last_processed_event_id_before: previouslyProcessedEventId,
    last_processed_event_id_after: Math.max(latestEventIdBeforePass, latestEventIdAfterPass),
    replayed_event_count: replayedEvents.length,
    launched_lanes: launchedLanes,
    invalidated_lanes: invalidatedLanes,
    retried_lanes: retriedLanes,
    recovery_events: allRecoveryEvents,
    concern_registry_diagnostics: concernRegistryDetails.diagnostics,
    blocked_reason: publisherStatus?.blocked_reason ?? leaderStatus?.blocked_reason ?? latestManifest.publication_error,
  });
  await writeSchedulerStatus(runPaths, {
    version: 1,
    run_id: latestManifest.run_id,
    last_processed_event_id: Math.max(latestEventIdBeforePass, latestEventIdAfterPass),
    pass_count: (previousSchedulerState?.pass_count ?? 0) + 1,
    startup_pass_count: (previousSchedulerState?.startup_pass_count ?? 0) + (wakeReason === 'startup' ? 1 : 0),
    watch_pass_count: (previousSchedulerState?.watch_pass_count ?? 0) + (wakeReason === 'watch' ? 1 : 0),
    poll_pass_count: (previousSchedulerState?.poll_pass_count ?? 0) + (wakeReason === 'poll' ? 1 : 0),
    watch_mode: input.watchMode ?? previousSchedulerState?.watch_mode ?? 'poll-only',
    last_wake_reason: wakeReason,
    last_completed_pass_id: passId,
    last_pass_at: now().toISOString(),
    blocked_reason: publisherStatus?.blocked_reason ?? leaderStatus?.blocked_reason ?? latestManifest.publication_error,
    replay_count: (previousSchedulerState?.replay_count ?? 0) + (replayedEvents.length > 0 ? 1 : 0),
    recovery_count: (previousSchedulerState?.recovery_count ?? 0) + allRecoveryEvents.length,
    publisher_recovery_count: publisherStatus?.recovery_count ?? previousSchedulerState?.publisher_recovery_count ?? 0,
  });
  return {
    manifest: latestManifest,
    hasRemainingWork: await schedulerHasRemainingWork(latestManifest, runPaths),
  };
}

export async function resolveGithubSchedulerWatchPaths(
  input: {
    runId: string;
    env?: NodeJS.ProcessEnv;
    homeDir?: string;
  },
): Promise<string[]> {
  const env = input.env ?? process.env;
  const nanaHome = resolveNanaHomeDir(env, input.homeDir);
  const manifest = await resolveManifestForSync(nanaHome, {
    subcommand: 'sync',
    runId: input.runId,
    useLastRun: false,
    reviewer: undefined,
    resumeLast: false,
    codexArgs: [],
  });
  const repoPaths = managedRepoPaths(nanaHome, join(manifest.repo_owner, manifest.repo_name));
  const runPaths = githubRunPaths(repoPaths.repoRoot, manifest.run_id);
  return [
    laneRuntimeDir(runPaths),
    runPaths.manifestPath,
  ];
}

async function startGithubWorkon(
  parsed: GithubStartCommand,
  dependencies: Required<Pick<GithubCommandDependencies, 'fetchImpl' | 'launchWithHud' | 'writeLine' | 'now' | 'env' | 'runLaneProcess'>> & Pick<GithubCommandDependencies, 'execFileSyncImpl' | 'homeDir' | 'startLeaseHeartbeat' | 'startSchedulerDaemon'>,
): Promise<void> {
  const apiBaseUrl = dependencies.env.GITHUB_API_URL?.trim() || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(dependencies.env, apiBaseUrl, dependencies.execFileSyncImpl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: dependencies.fetchImpl };
  const viewerLogin = await resolveViewerLogin(apiContext);
  const reviewer = resolveReviewerLogin(parsed.reviewer, viewerLogin);
  const target = await fetchTargetContext(parsed.target, apiContext);
  const now = dependencies.now();
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const paths = managedRepoPaths(nanaHome, join(parsed.target.owner, parsed.target.repoName));
  const repoMeta = await ensureManagedRepoMetadata(paths, target, now);
  await maybeLinkPrSandboxToIssue(paths, parsed.target, target);
  const runId = buildRunId(now);
  const runPaths = githubRunPaths(paths.repoRoot, runId);
  const allocation = await allocateSandbox(paths, repoMeta, parsed.target, runId, now, { newPr: parsed.newPr });
  const repoVerificationPlan = await detectVerificationPlan(paths.sourcePath);
  await writeRepoVerificationPlan(paths, paths.sourcePath, repoVerificationPlan);
  const inferredCiDurations = await inferCiSuiteDurationsFromRecentRuns(
    repoMeta.repo_slug,
    repoMeta.default_branch,
    apiContext,
    repoVerificationPlan.plan_fingerprint,
  );
  if (inferredCiDurations) {
    await writeCiSuiteDurations(paths.repoCiSuiteDurationsPath, inferredCiDurations);
  }
  let repoSettings = await readManagedRepoSettings(paths);
  let inferredDefaults: GithubConsiderationInference | null = null;
  if (!repoSettings) {
    inferredDefaults = await inferInitialRepoConsiderations(paths.sourcePath, repoMeta.repo_slug, repoVerificationPlan);
    repoSettings = await writeManagedRepoSettings(
      paths,
      inferredDefaults.considerations,
      undefined,
      undefined,
      undefined,
      undefined,
      now,
    );
    dependencies.writeLine(
      `[github] First managed run for ${repoMeta.repo_slug}; inferred default considerations: ${inferredDefaults.considerations.join(', ') || '(none)'}.`,
    );
    for (const consideration of inferredDefaults.considerations) {
      const reason = inferredDefaults.reasons[consideration]?.join('; ');
      if (reason) dependencies.writeLine(`[github]   - ${consideration}: ${reason}`);
    }
  }
  repoSettings = await refreshManagedRepoHotPathApiProfile(paths, repoSettings, paths.sourcePath, now);
  await maybeAutoRefreshRepoReviewRulesForTarget({
    nanaHome,
    target: parsed.target,
    paths,
    repoSettings,
    apiContext,
    now,
    writeLine: dependencies.writeLine,
  });
  const roleLayout = parsed.roleLayout ?? repoSettings?.default_role_layout ?? 'split';
  const activeConsiderations = [...new Set([
    ...(repoSettings?.default_considerations ?? []),
    ...parsed.requestedConsiderations,
  ])];
  const considerationPipeline = buildConsiderationPipeline(activeConsiderations, roleLayout);
  const verificationPlan = await detectVerificationPlan(allocation.repoCheckoutPath);
  const verificationScriptsDir = await writeVerificationScripts(
    allocation.sandboxPath,
    allocation.repoCheckoutPath,
    verificationPlan,
    runId,
    {
      managedRepoRoot: paths.repoRoot,
      sandboxId: allocation.sandboxId,
      prMode: parsed.createPr || parsed.target.targetKind === 'pr',
      env: dependencies.env,
    },
  );
  await maybeLinkPrSandboxFromIssue(paths, parsed.target, allocation.sandboxId, allocation.sandboxPath, repoMeta, apiContext);
  const issueAssociationNumber = await resolveIssueAssociationNumber(
    allocation.sandboxPath,
    parsed.target.targetKind,
    parsed.target.targetNumber,
  );
  const feedback = await fetchFeedbackSnapshot({
    repo_owner: repoMeta.repo_owner,
    repo_name: repoMeta.repo_name,
    target_number: parsed.target.targetNumber,
    target_kind: parsed.target.targetKind,
  }, reviewer, apiContext);

  const lanePromptArtifacts = await writeLanePromptArtifacts(runPaths, considerationPipeline);

  const seedManifest: GithubWorkonManifest = {
    version: 3,
    run_id: runId,
    created_at: now.toISOString(),
    updated_at: now.toISOString(),
    repo_slug: repoMeta.repo_slug,
    repo_owner: repoMeta.repo_owner,
    repo_name: repoMeta.repo_name,
    managed_repo_root: paths.repoRoot,
    source_path: paths.sourcePath,
    sandbox_id: allocation.sandboxId,
    sandbox_path: allocation.sandboxPath,
    sandbox_repo_path: allocation.repoCheckoutPath,
    verification_plan: verificationPlan,
    verification_scripts_dir: verificationScriptsDir,
    considerations_active: activeConsiderations,
    role_layout: roleLayout,
    consideration_pipeline: considerationPipeline,
    lane_prompt_artifacts: lanePromptArtifacts,
    team_resolved_aliases: considerationPipeline.map((lane) => lane.alias),
    team_resolved_roles: considerationPipeline.map((lane) => lane.role),
    create_pr_on_complete: parsed.createPr,
    issue_association_number: issueAssociationNumber,
    publication_state: parsed.createPr || parsed.target.targetKind === 'pr' ? 'not_started' : undefined,
    target_kind: parsed.target.targetKind,
    target_number: parsed.target.targetNumber,
    target_title: target.issue.title,
    target_url: parsed.target.canonicalUrl,
    target_state: target.issue.state,
    review_reviewer: reviewer,
    api_base_url: apiBaseUrl,
    default_branch: repoMeta.default_branch,
    last_seen_issue_comment_id: 0,
    last_seen_review_id: 0,
    last_seen_review_comment_id: 0,
    ...(target.pullRequest ? {
      pr_head_ref: target.pullRequest.head.ref,
      pr_head_sha: target.pullRequest.head.sha,
      pr_head_repo: target.pullRequest.head.repo?.full_name ?? undefined,
      pr_base_ref: target.pullRequest.base.ref,
      pr_base_sha: target.pullRequest.base.sha,
      pr_base_repo: target.pullRequest.base.repo?.full_name ?? undefined,
    } : {}),
  };
  const cursor = advanceFeedbackCursor(seedManifest, feedback);
  const manifest: GithubWorkonManifest = {
    ...seedManifest,
    last_seen_issue_comment_id: cursor.issueCommentId,
    last_seen_review_id: cursor.reviewId,
    last_seen_review_comment_id: cursor.reviewCommentId,
  };

  let finalManifest = manifest;
  await writeManifest(runPaths, manifest);
  finalManifest = await ensureVerificationArtifactsCurrent(manifest, paths, runPaths, dependencies.env);
  const sourceCodexHome = resolveUserCodexHomeDir(dependencies.env, dependencies.homeDir);
  await initializeLaneRuntimeArtifacts(finalManifest, runPaths, now);
  await writeFile(runPaths.startInstructionsPath, buildStartInstructions(finalManifest, target, reviewer, feedback), 'utf-8');
  await writeLatestRunPointers(paths, runId);

  dependencies.writeLine(`[github] Starting run ${runId} for ${manifest.repo_slug} ${manifest.target_kind} #${manifest.target_number}`);
  dependencies.writeLine(`[github] Managed repo root: ${paths.repoRoot}`);
  dependencies.writeLine(`[github] Managed sandbox: ${allocation.sandboxId} -> ${allocation.sandboxPath}`);
  dependencies.writeLine(`[github] Managed repo checkout: ${allocation.repoCheckoutPath}`);
  dependencies.writeLine(`[github] Reviewer sync user: @${reviewer}`);

  const previousEnv = dependencies.env[GITHUB_APPEND_ENV];
  const previousCodexHome = dependencies.env.CODEX_HOME;
  const previousGitDir = dependencies.env.GIT_DIR;
  const previousGitWorkTree = dependencies.env.GIT_WORK_TREE;
  const previousProjectAgentsRoot = dependencies.env.NANA_PROJECT_AGENTS_ROOT;
  const previousPath = dependencies.env.PATH;
  const previousCwd = process.cwd();
  const sandboxCodexHome = await ensureSandboxCodexHome(
    allocation.sandboxPath,
    sourceCodexHome,
    GITHUB_CODEX_PROFILES.leader,
    [allocation.repoCheckoutPath, allocation.sandboxPath],
  );
  const sandboxNanaBin = await ensureSandboxNanaShim(allocation.sandboxPath);
  dependencies.env[GITHUB_APPEND_ENV] = runPaths.startInstructionsPath;
  dependencies.env.CODEX_HOME = sandboxCodexHome;
  dependencies.env.GIT_DIR = allocation.gitDirPath;
  dependencies.env.GIT_WORK_TREE = allocation.repoCheckoutPath;
  dependencies.env.NANA_PROJECT_AGENTS_ROOT = allocation.repoCheckoutPath;
  dependencies.env.PATH = previousPath ? `${sandboxNanaBin}:${previousPath}` : sandboxNanaBin;
  (dependencies.startLeaseHeartbeat ?? startSandboxLeaseHeartbeat)({
    lockDir: allocation.lockDir,
    sandboxId: allocation.sandboxId,
    ownerPid: process.pid,
    ttlMs: SANDBOX_LOCK_TTL_MS,
    heartbeatMs: SANDBOX_LOCK_HEARTBEAT_MS,
  });
  process.chdir(allocation.sandboxPath);
  try {
    await writeLeaderStatus(runPaths, {
      run_id: finalManifest.run_id,
      session_active: true,
      pid: process.pid,
      bootstrap_complete: false,
      implementation_started: true,
      implementation_complete: false,
      ready_for_publication: false,
      blocked: false,
      updated_at: dependencies.now().toISOString(),
    });
    const bootstrapLanePromise = runGithubLaneCycle({
      manifest: finalManifest,
      repoPaths: paths,
      runPaths,
      activation: 'bootstrap',
      sourceCodexHome,
      env: dependencies.env,
      homeDir: dependencies.homeDir,
      writeLine: dependencies.writeLine,
      runLaneProcess: dependencies.runLaneProcess,
    });
    await dependencies.launchWithHud(ensureGithubLaunchBypass([
      ...parsed.codexArgs,
      `Implement GitHub ${parsed.target.targetKind} #${parsed.target.targetNumber} for ${parsed.target.repoSlug}`,
    ]));
    await bootstrapLanePromise;
    await writeLeaderStatus(runPaths, {
      run_id: finalManifest.run_id,
      session_active: false,
      pid: undefined,
      bootstrap_complete: true,
      implementation_started: true,
      implementation_complete: true,
      ready_for_publication: true,
      blocked: false,
      updated_at: dependencies.now().toISOString(),
    });
    try {
      finalManifest = await runGithubSchedulerPass({
        manifest: finalManifest,
        target,
        repoPaths: paths,
        runPaths,
        sourceCodexHome,
        env: dependencies.env,
        homeDir: dependencies.homeDir,
        writeLine: dependencies.writeLine,
        runLaneProcess: dependencies.runLaneProcess,
        launchWithHud: dependencies.launchWithHud,
        now: dependencies.now(),
        apiContext,
      });
      if (await schedulerHasRemainingWork(finalManifest, runPaths)) {
        (dependencies.startSchedulerDaemon ?? startSchedulerDaemon)({
          runId,
          homeDir: dependencies.homeDir,
        });
        dependencies.writeLine(`[github] Remaining scheduler work detected for run ${runId}. Started background scheduler daemon.`);
      }
    } catch (error) {
      const latestManifest = await readManifest(runPaths.manifestPath).catch(() => finalManifest);
      finalManifest = latestManifest;
      if (await shouldContinueBlockedPublication(latestManifest, runPaths)) {
        (dependencies.startSchedulerDaemon ?? startSchedulerDaemon)({
          runId,
          homeDir: dependencies.homeDir,
        });
        dependencies.writeLine(`[github] Publication blocked in recoverable state (${latestManifest.publication_state}). Started background scheduler daemon for run ${runId}.`);
      } else {
        throw error;
      }
    }
  } finally {
    process.chdir(previousCwd);
    if (typeof previousEnv === 'string') dependencies.env[GITHUB_APPEND_ENV] = previousEnv;
    else delete dependencies.env[GITHUB_APPEND_ENV];
    if (typeof previousCodexHome === 'string') dependencies.env.CODEX_HOME = previousCodexHome;
    else delete dependencies.env.CODEX_HOME;
    if (typeof previousGitDir === 'string') dependencies.env.GIT_DIR = previousGitDir;
    else delete dependencies.env.GIT_DIR;
    if (typeof previousGitWorkTree === 'string') dependencies.env.GIT_WORK_TREE = previousGitWorkTree;
    else delete dependencies.env.GIT_WORK_TREE;
    if (typeof previousProjectAgentsRoot === 'string') dependencies.env.NANA_PROJECT_AGENTS_ROOT = previousProjectAgentsRoot;
    else delete dependencies.env.NANA_PROJECT_AGENTS_ROOT;
    if (typeof previousPath === 'string') dependencies.env.PATH = previousPath;
    else delete dependencies.env.PATH;
    await releaseSandboxLease(allocation.lockDir);
    const threadUsageArtifact = await writeThreadUsageArtifact(runPaths, allocation.sandboxPath, dependencies.now()).catch(() => null);
    const tokenStats = await captureSandboxTokenUsageForIssue(
      paths,
      finalManifest.repo_slug,
      allocation.sandboxId,
      allocation.sandboxPath,
      finalManifest.issue_association_number,
      dependencies.now(),
      threadUsageArtifact ? { total_tokens: threadUsageArtifact.total_tokens } : undefined,
    ).catch(() => null);
    if (tokenStats) {
      for (const line of buildIssueStatsLines(tokenStats)) dependencies.writeLine(line);
    }
    const retrospective = await buildRetrospectiveForManifest(finalManifest, paths).catch(() => '');
    if (retrospective) {
      dependencies.writeLine('[github] Wrote retrospective artifact.');
    }
  }
}

async function syncGithubWorkon(
  parsed: GithubSyncCommand,
  dependencies: Required<Pick<GithubCommandDependencies, 'fetchImpl' | 'launchWithHud' | 'writeLine' | 'now' | 'env' | 'runLaneProcess'>> & Pick<GithubCommandDependencies, 'execFileSyncImpl' | 'homeDir' | 'startLeaseHeartbeat' | 'startSchedulerDaemon'>,
): Promise<void> {
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const manifest = await resolveManifestForSync(nanaHome, parsed);
  const apiBaseUrl = dependencies.env.GITHUB_API_URL?.trim() || manifest.api_base_url || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(dependencies.env, apiBaseUrl, dependencies.execFileSyncImpl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: dependencies.fetchImpl };
  const viewerLogin = await resolveViewerLogin(apiContext);
  const reviewer = resolveReviewerLogin(parsed.reviewer, viewerLogin);
  const requestedFeedbackTarget = parsed.feedbackTargetUrl ? parseGithubTargetUrl(parsed.feedbackTargetUrl) : undefined;
  const currentTarget = await fetchTargetContext(parseGithubTargetUrl(manifest.target_url), apiContext);
  let effectiveManifest = manifest;
  const repoPaths = managedRepoPaths(nanaHome, join(manifest.repo_owner, manifest.repo_name));
  effectiveManifest = await reconcileManifestWithGithubState(effectiveManifest, repoPaths, apiContext, dependencies.now()).catch(() => effectiveManifest);
  const repoSettings = await readManagedRepoSettings(repoPaths);
  await maybeAutoRefreshRepoReviewRulesForTarget({
    nanaHome,
    target: requestedFeedbackTarget ?? parseGithubTargetUrl(manifest.target_url),
    paths: repoPaths,
    repoSettings,
    apiContext,
    now: dependencies.now(),
    writeLine: dependencies.writeLine,
  });
  const syncCiStatus = await resolveSyncCiStatus(effectiveManifest, apiContext, requestedFeedbackTarget);
  const feedbackTarget = parsed.feedbackTargetUrl ? parseGithubTargetUrl(parsed.feedbackTargetUrl) : undefined;
  const allFeedback = await fetchFeedbackSnapshot(effectiveManifest, reviewer, apiContext, feedbackTarget);
  const newFeedback = filterNewFeedback(allFeedback, effectiveManifest);
  const now = dependencies.now();
  const parsedTarget = parseGithubTargetUrl(manifest.target_url);
  const repoMeta: ManagedRepoMetadata = {
    version: 1,
    repo_name: manifest.repo_name,
    repo_slug: manifest.repo_slug,
    repo_owner: manifest.repo_owner,
    clone_url: currentTarget.repository.clone_url,
    default_branch: manifest.default_branch,
    html_url: currentTarget.repository.html_url,
    repo_root: repoPaths.repoRoot,
    source_path: repoPaths.sourcePath,
    updated_at: now.toISOString(),
  };
  await maybeLinkPrSandboxFromIssue(repoPaths, parsedTarget, manifest.sandbox_id, manifest.sandbox_path, repoMeta, apiContext);
  await maybeLinkPrSandboxToIssue(repoPaths, parsedTarget, currentTarget);

  if (manifest.target_kind === 'pr' && currentTarget.pullRequest?.merged_at) {
    await cleanupSandbox(repoPaths, manifest.sandbox_id, manifest.sandbox_path, manifest.sandbox_repo_path);
    dependencies.writeLine(
      `[github] PR ${manifest.target_number} is merged (${currentTarget.pullRequest.merged_at}); dropped sandbox ${manifest.sandbox_id}.`,
    );
    return;
  }

  if (newFeedback.issueComments.length === 0 && newFeedback.reviews.length === 0 && newFeedback.reviewComments.length === 0) {
    const feedbackScope = describeSyncFeedbackScope(effectiveManifest, requestedFeedbackTarget);
    dependencies.writeLine(
      `[github] No new feedback from @${reviewer} for ${feedbackScope}.`,
    );
    if (typeof syncCiStatus.prNumber === 'number') {
      dependencies.writeLine(
        `[github] CI/publication status for PR #${syncCiStatus.prNumber}: ${syncCiStatus.state ?? 'unknown'}.`,
      );
    }
    return;
  }

  const runPaths = githubRunPaths(repoPaths.repoRoot, manifest.run_id);
  const roleLayout = manifest.role_layout ?? 'split';
  const considerationPipeline = manifest.consideration_pipeline ?? buildConsiderationPipeline(manifest.considerations_active ?? [], roleLayout);
  const lanePromptArtifacts = await writeLanePromptArtifacts(runPaths, considerationPipeline);
  const cursor = advanceFeedbackCursor(effectiveManifest, allFeedback);
  const updatedManifest: GithubWorkonManifest = {
    ...effectiveManifest,
    verification_plan: effectiveManifest.verification_plan,
    verification_scripts_dir: effectiveManifest.verification_scripts_dir,
    considerations_active: effectiveManifest.considerations_active ?? [],
    role_layout: roleLayout,
    consideration_pipeline: considerationPipeline,
    lane_prompt_artifacts: lanePromptArtifacts,
    team_resolved_aliases: effectiveManifest.team_resolved_aliases ?? considerationPipeline.map((lane) => lane.alias),
    team_resolved_roles: effectiveManifest.team_resolved_roles ?? considerationPipeline.map((lane) => lane.role),
    create_pr_on_complete: effectiveManifest.create_pr_on_complete ?? false,
    issue_association_number: effectiveManifest.issue_association_number ?? await resolveIssueAssociationNumber(
      effectiveManifest.sandbox_path,
      effectiveManifest.target_kind,
      effectiveManifest.target_kind === 'issue' ? effectiveManifest.target_number : undefined,
    ),
    publication_state: effectiveManifest.publication_state ?? ((effectiveManifest.create_pr_on_complete ?? false) || effectiveManifest.target_kind === 'pr' ? 'not_started' : undefined),
    updated_at: now.toISOString(),
    review_reviewer: reviewer,
    api_base_url: apiBaseUrl,
    last_seen_issue_comment_id: cursor.issueCommentId,
    last_seen_review_id: cursor.reviewId,
    last_seen_review_comment_id: cursor.reviewCommentId,
  };
  await writeManifest(runPaths, updatedManifest);
  const refreshedManifest = await ensureVerificationArtifactsCurrent(updatedManifest, repoPaths, runPaths, dependencies.env);
  await writeFile(runPaths.feedbackInstructionsPath, buildFeedbackInstructions(refreshedManifest, reviewer, newFeedback), 'utf-8');
  await writeLatestRunPointers(repoPaths, manifest.run_id);

  const leaseAttempt = await tryAcquireSandboxLease(
    repoPaths,
    manifest.sandbox_id,
    manifest.run_id,
    manifest.target_url,
    now,
  );

  if (!leaseAttempt.ok) {
    dependencies.writeLine(
      `[github] New feedback stored for run ${manifest.run_id}, but sandbox ${manifest.sandbox_id} is busy (owner pid ${leaseAttempt.busyLease?.owner_pid ?? 'unknown'}).`,
    );
    dependencies.writeLine(`[github] Feedback file: ${runPaths.feedbackInstructionsPath}`);
    return;
  }

  const previousEnv = dependencies.env[GITHUB_APPEND_ENV];
  const previousCodexHome = dependencies.env.CODEX_HOME;
  const previousProjectAgentsRoot = dependencies.env.NANA_PROJECT_AGENTS_ROOT;
  const previousPath = dependencies.env.PATH;
  const previousCwd = process.cwd();
  const sourceCodexHome = resolveUserCodexHomeDir(dependencies.env, dependencies.homeDir);
  const sandboxCodexHome = await ensureSandboxCodexHome(
    updatedManifest.sandbox_path,
    sourceCodexHome,
    GITHUB_CODEX_PROFILES.leader,
    [updatedManifest.sandbox_repo_path, updatedManifest.sandbox_path],
  );
  const sandboxNanaBin = await ensureSandboxNanaShim(updatedManifest.sandbox_path);
  let finalManifest = refreshedManifest;
  dependencies.env[GITHUB_APPEND_ENV] = runPaths.feedbackInstructionsPath;
  dependencies.env.CODEX_HOME = sandboxCodexHome;
  dependencies.env.NANA_PROJECT_AGENTS_ROOT = updatedManifest.sandbox_repo_path;
  dependencies.env.PATH = previousPath ? `${sandboxNanaBin}:${previousPath}` : sandboxNanaBin;
  (dependencies.startLeaseHeartbeat ?? startSandboxLeaseHeartbeat)({
    lockDir: leaseAttempt.lockDir,
    sandboxId: manifest.sandbox_id,
    ownerPid: process.pid,
    ttlMs: SANDBOX_LOCK_TTL_MS,
    heartbeatMs: SANDBOX_LOCK_HEARTBEAT_MS,
  });
  process.chdir(manifest.sandbox_path);
  try {
    await initializeLaneRuntimeArtifacts(finalManifest, runPaths, now);
    await writeLeaderStatus(runPaths, {
      run_id: finalManifest.run_id,
      session_active: true,
      pid: process.pid,
      bootstrap_complete: true,
      implementation_started: true,
      implementation_complete: false,
      ready_for_publication: false,
      blocked: false,
      updated_at: dependencies.now().toISOString(),
    });
    if (parsed.resumeLast) {
      await dependencies.launchWithHud(ensureGithubLaunchBypass([
        'resume',
        '--last',
        ...parsed.codexArgs,
        `Incorporate new GitHub feedback for ${manifest.repo_slug} ${manifest.target_kind} #${manifest.target_number}`,
      ]));
    } else {
      await dependencies.launchWithHud(ensureGithubLaunchBypass([
        ...parsed.codexArgs,
        `Incorporate new GitHub feedback for ${manifest.repo_slug} ${manifest.target_kind} #${manifest.target_number}`,
      ]));
    }
    await writeLeaderStatus(runPaths, {
      run_id: finalManifest.run_id,
      session_active: false,
      pid: undefined,
      bootstrap_complete: true,
      implementation_started: true,
      implementation_complete: true,
      ready_for_publication: true,
      blocked: false,
      updated_at: dependencies.now().toISOString(),
    });
    try {
      finalManifest = await runGithubSchedulerPass({
        manifest: finalManifest,
        target: currentTarget,
        repoPaths,
        runPaths,
        sourceCodexHome,
        env: dependencies.env,
        homeDir: dependencies.homeDir,
        writeLine: dependencies.writeLine,
        runLaneProcess: dependencies.runLaneProcess,
        launchWithHud: dependencies.launchWithHud,
        now: dependencies.now(),
        apiContext,
      });
      if (await schedulerHasRemainingWork(finalManifest, runPaths)) {
        (dependencies.startSchedulerDaemon ?? startSchedulerDaemon)({
          runId: manifest.run_id,
          homeDir: dependencies.homeDir,
        });
        dependencies.writeLine(`[github] Remaining scheduler work detected for run ${manifest.run_id}. Started background scheduler daemon.`);
      }
    } catch (error) {
      const latestManifest = await readManifest(runPaths.manifestPath).catch(() => finalManifest);
      finalManifest = latestManifest;
      if (await shouldContinueBlockedPublication(latestManifest, runPaths)) {
        (dependencies.startSchedulerDaemon ?? startSchedulerDaemon)({
          runId: manifest.run_id,
          homeDir: dependencies.homeDir,
        });
        dependencies.writeLine(`[github] Publication blocked in recoverable state (${latestManifest.publication_state}). Started background scheduler daemon for run ${manifest.run_id}.`);
      } else {
        throw error;
      }
    }
  } finally {
    process.chdir(previousCwd);
    if (typeof previousEnv === 'string') dependencies.env[GITHUB_APPEND_ENV] = previousEnv;
    else delete dependencies.env[GITHUB_APPEND_ENV];
    if (typeof previousCodexHome === 'string') dependencies.env.CODEX_HOME = previousCodexHome;
    else delete dependencies.env.CODEX_HOME;
    if (typeof previousProjectAgentsRoot === 'string') dependencies.env.NANA_PROJECT_AGENTS_ROOT = previousProjectAgentsRoot;
    else delete dependencies.env.NANA_PROJECT_AGENTS_ROOT;
    if (typeof previousPath === 'string') dependencies.env.PATH = previousPath;
    else delete dependencies.env.PATH;
    await releaseSandboxLease(leaseAttempt.lockDir);
    const threadUsageArtifact = await writeThreadUsageArtifact(runPaths, finalManifest.sandbox_path, dependencies.now()).catch(() => null);
    const tokenStats = await captureSandboxTokenUsageForIssue(
      repoPaths,
      finalManifest.repo_slug,
      finalManifest.sandbox_id,
      finalManifest.sandbox_path,
      finalManifest.issue_association_number,
      dependencies.now(),
      threadUsageArtifact ? { total_tokens: threadUsageArtifact.total_tokens } : undefined,
    ).catch(() => null);
    if (tokenStats) {
      for (const line of buildIssueStatsLines(tokenStats)) dependencies.writeLine(line);
    }
    const retrospective = await buildRetrospectiveForManifest(finalManifest, repoPaths).catch(() => '');
    if (retrospective) {
      dependencies.writeLine('[github] Wrote retrospective artifact.');
    }
  }
}

export async function githubCommand(
  args: string[],
  dependencies: GithubCommandDependencies = {},
): Promise<void> {
  const parsed = parseGithubArgs(args);
  const writeLine = dependencies.writeLine ?? ((line: string) => console.log(line));
  const now = dependencies.now ?? (() => new Date());
  const fetchImpl = dependencies.fetchImpl ?? fetch;
  const env = dependencies.env ?? process.env;
  const execFileSyncImpl = dependencies.execFileSyncImpl;
  const homeDir = dependencies.homeDir;
  const startLeaseHeartbeat = dependencies.startLeaseHeartbeat;
  const runLaneProcess = dependencies.runLaneProcess ?? (dependencies.launchWithHud ? runGithubLaneProcessNoop : runGithubLaneProcess);
  const startSchedulerDaemon = dependencies.startSchedulerDaemon;

  if (parsed.subcommand === 'help') {
    writeLine(GITHUB_HELP);
    return;
  }

  if (parsed.subcommand === 'defaults-set') {
    await setGithubRepoDefaults(parsed, { writeLine, now, env, homeDir });
    return;
  }

  if (parsed.subcommand === 'defaults-show') {
    await showGithubRepoDefaults(parsed, { writeLine, env, homeDir });
    return;
  }

  if (parsed.subcommand === 'stats') {
    await showGithubWorkonStats(parsed, { writeLine, env, homeDir, now });
    return;
  }

  if (parsed.subcommand === 'retrospective') {
    await showGithubRetrospective(parsed, { writeLine, env, homeDir });
    return;
  }

  if (parsed.subcommand === 'verify-refresh') {
    await refreshGithubVerificationArtifacts(parsed, { writeLine, env, homeDir });
    return;
  }

  if (parsed.subcommand === 'lane-exec') {
    await executeGithubLane(parsed, { writeLine, env, homeDir });
    return;
  }

  const launchWithHud = dependencies.launchWithHud ?? (await import('./index.js')).launchWithHud;
  const deps = { fetchImpl, launchWithHud, writeLine, now, env, execFileSyncImpl, homeDir, startLeaseHeartbeat, runLaneProcess, startSchedulerDaemon };

  if (parsed.subcommand === 'start') {
    await startGithubWorkon(parsed, deps);
    return;
  }

  await syncGithubWorkon(parsed, deps);
}

function buildPullReviewRunId(now: Date): string {
  return `gr-${now.getTime()}-${Math.random().toString(36).slice(2, 8)}`;
}

function normalizePullReviewSeverity(value: string | undefined): GithubPullReviewFindingSeverity {
  const normalized = value?.trim().toLowerCase();
  if (normalized === 'critical' || normalized === 'high' || normalized === 'medium' || normalized === 'low') {
    return normalized;
  }
  return 'medium';
}

function buildPullReviewFindingFingerprint(input: {
  title: string;
  path: string;
  line?: number;
  summary: string;
}): string {
  return createHash('sha1')
    .update([
      input.path.trim().toLowerCase(),
      String(input.line ?? ''),
      input.title.trim().toLowerCase(),
      input.summary.trim().toLowerCase(),
    ].join('\n'))
    .digest('hex');
}

async function readPullReviewManifest(manifestPath: string): Promise<GithubPullReviewManifest> {
  return JSON.parse(await readFile(manifestPath, 'utf-8')) as GithubPullReviewManifest;
}

async function writePullReviewManifest(paths: GithubPullReviewRunPaths, manifest: GithubPullReviewManifest): Promise<void> {
  await mkdir(paths.runDir, { recursive: true });
  await writeJsonFile(paths.manifestPath, manifest);
}

async function writePullReviewActive(paths: GithubPullReviewPaths, active: GithubPullReviewActiveState): Promise<void> {
  await mkdir(paths.prRoot, { recursive: true });
  await writeJsonFile(paths.activePath, active);
}

async function clearPullReviewActive(paths: GithubPullReviewPaths): Promise<void> {
  await rm(paths.activePath, { force: true });
}

async function readPullReviewBucket(path: string): Promise<GithubPullReviewFinding[]> {
  return (await readJsonFile<GithubPullReviewFinding[]>(path)) ?? [];
}

async function writePullReviewBucket(path: string, findings: readonly GithubPullReviewFinding[]): Promise<void> {
  await writeJsonFile(path, findings);
}

async function loadPersistedPullReviewBuckets(
  reviewPaths: GithubPullReviewPaths,
): Promise<{
  userDropped: Map<string, GithubPullReviewFinding>;
  notReal: Map<string, GithubPullReviewFinding>;
  preexisting: Map<string, GithubPullReviewFinding>;
}> {
  const userDropped = new Map<string, GithubPullReviewFinding>();
  const notReal = new Map<string, GithubPullReviewFinding>();
  const preexisting = new Map<string, GithubPullReviewFinding>();
  if (!existsSync(reviewPaths.runsDir)) {
    return { userDropped, notReal, preexisting };
  }
  const entries = await readdir(reviewPaths.runsDir, { withFileTypes: true });
  const runIds = entries.filter((entry) => entry.isDirectory()).map((entry) => entry.name).sort();
  const prNumber = Number.parseInt(reviewPaths.prRoot.split('pr-').pop() || '', 10);
  const repoRoot = dirname(dirname(reviewPaths.prRoot));
  for (const runId of runIds) {
    const runPaths = githubPullReviewRunPaths(repoRoot, prNumber, runId);
    for (const finding of await readPullReviewBucket(runPaths.droppedUserPath)) userDropped.set(finding.fingerprint, finding);
    for (const finding of await readPullReviewBucket(runPaths.droppedNotRealPath)) notReal.set(finding.fingerprint, finding);
    for (const finding of await readPullReviewBucket(runPaths.droppedPreexistingPath)) preexisting.set(finding.fingerprint, finding);
  }
  return { userDropped, notReal, preexisting };
}

async function resolveActivePullReviewRun(
  paths: GithubPullReviewPaths,
): Promise<{ active: GithubPullReviewActiveState; manifest: GithubPullReviewManifest; runPaths: GithubPullReviewRunPaths } | null> {
  const active = await readJsonFile<GithubPullReviewActiveState>(paths.activePath);
  if (!active?.run_id) return null;
  const runPaths = githubPullReviewRunPaths(dirname(dirname(paths.prRoot)), Number.parseInt(paths.prRoot.split('pr-').pop() || '', 10), active.run_id);
  if (!existsSync(runPaths.manifestPath)) return null;
  const manifest = await readPullReviewManifest(runPaths.manifestPath);
  return { active, manifest, runPaths };
}

function escapeMarkdownText(value: string): string {
  return value.replace(/[*_`]/g, '').trim();
}

function convertBackticksToItalics(value: string): string {
  return value.replace(/`([^`]+)`/g, '*$1*');
}

function renderFindingReference(finding: Pick<GithubPullReviewFinding, 'path' | 'line'>): string {
  return finding.line != null ? `${finding.path}:${finding.line}` : finding.path;
}

function renderFindingLink(finding: Pick<GithubPullReviewFinding, 'changed_line_in_pr' | 'pr_permalink' | 'main_permalink'>): string | undefined {
  return finding.changed_line_in_pr ? finding.pr_permalink : finding.main_permalink;
}

function formatPullReviewFindingForGithub(finding: GithubPullReviewFinding): string {
  const reference = `*${escapeMarkdownText(renderFindingReference(finding))}*`;
  const detail = convertBackticksToItalics(escapeMarkdownText(finding.detail));
  const fix = convertBackticksToItalics(escapeMarkdownText(finding.fix));
  const link = renderFindingLink(finding);
  return [
    `[${finding.severity.toUpperCase()}] ${convertBackticksToItalics(escapeMarkdownText(finding.title))}`,
    `${reference} - ${detail}`,
    fix ? `Fix: ${fix}` : '',
    link ? `Reference: ${link}` : '',
  ].filter(Boolean).join('\n');
}

function formatPullReviewSummary(findings: readonly GithubPullReviewFinding[], event: 'APPROVE' | 'REQUEST_CHANGES'): string {
  if (event === 'APPROVE') {
    return 'Reviewed the PR. No actionable issues found.';
  }
  const lines = [
    'Found actionable issues that should be fixed before merge.',
    '',
  ];
  for (const [index, finding] of findings.entries()) {
    lines.push(`${index + 1}. ${formatPullReviewFindingForGithub(finding)}`);
    lines.push('');
  }
  return lines.join('\n').trim();
}

function buildPullReviewMarkdown(findings: readonly GithubPullReviewFinding[]): string {
  const lines = [
    '# NANA PR Review',
    '',
    'Fill every finding before closing the editor.',
    '',
    '- `Action:` must be one of `drop`, `accept`, or `argue`.',
    '- `Explanation:` is required for `drop` and `argue`.',
    '- Accepted findings will be posted to GitHub when the loop finishes.',
    '- Dropped findings are remembered and suppressed in later review iterations for this PR.',
    '',
  ];
  for (const [index, finding] of findings.entries()) {
    lines.push(`${index + 1}. [${finding.severity.toUpperCase()}] ${finding.title}`);
    lines.push(`   - Code: ${renderFindingReference(finding)}`);
    lines.push(`   - Summary: ${finding.summary}`);
    lines.push('   - Detail: |');
    for (const detailLine of finding.detail.split('\n')) {
      lines.push(`     ${detailLine}`);
    }
    if (finding.fix.trim()) {
      lines.push('   - Fix: |');
      for (const fixLine of finding.fix.split('\n')) {
        lines.push(`     ${fixLine}`);
      }
    }
    lines.push('   - Action: accept');
    lines.push('   - Explanation:');
    lines.push('');
  }
  return lines.join('\n').trimEnd() + '\n';
}

function parsePullReviewMarkdown(content: string, findings: readonly GithubPullReviewFinding[]): Array<{
  finding: GithubPullReviewFinding;
  action: 'drop' | 'accept' | 'argue';
  explanation: string;
}> {
  const sections = content.split(/^\d+\.\s/m).slice(1);
  if (sections.length !== findings.length) {
    throw new Error(`Manual review file is malformed: expected ${findings.length} items, found ${sections.length}.`);
  }
  return sections.map((section, index) => {
    const actionMatch = section.match(/^\s+- Action:\s*(drop|accept|argue)\s*$/m);
    if (!actionMatch) {
      throw new Error(`Manual review file is malformed: item ${index + 1} is missing Action.`);
    }
    const action = actionMatch[1] as 'drop' | 'accept' | 'argue';
    const explanationStart = section.match(/^\s+- Explanation:\s*$/m);
    let explanation = '';
    if (explanationStart?.index != null) {
      const slice = section.slice(explanationStart.index + explanationStart[0].length);
      const lines = slice
        .split('\n')
        .map((line) => line.replace(/^\s+/, ''))
        .filter((line) => line.trim().length > 0);
      explanation = lines.join('\n').trim();
    }
    if ((action === 'drop' || action === 'argue') && !explanation) {
      throw new Error(`Manual review file is malformed: item ${index + 1} requires Explanation for action ${action}.`);
    }
    return { finding: findings[index]!, action, explanation };
  });
}

async function defaultOpenEditor(
  path: string,
  options: { cwd?: string; editor?: string } = {},
): Promise<void> {
  const editor = options.editor ?? process.env.EDITOR ?? process.env.VISUAL ?? 'vi';
  const result = spawnSync(editor, [path], {
    cwd: options.cwd,
    stdio: 'inherit',
    env: process.env,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(`editor exited with status ${result.status ?? 'unknown'}`);
  }
}

async function defaultCodexExec(
  prompt: string,
  options: { cwd: string; codexArgs?: string[]; env: NodeJS.ProcessEnv },
): Promise<string> {
  const args = ['exec', CODEX_BYPASS_FLAG, ...(options.codexArgs ?? []), '-'];
  const { result } = spawnPlatformCommandSync('codex', args, {
    cwd: options.cwd,
    env: options.env,
    encoding: 'utf-8',
    stdio: ['pipe', 'pipe', 'pipe'],
    input: prompt,
  });
  if (result.error) {
    const kind = classifySpawnError(result.error as NodeJS.ErrnoException);
    if (kind === 'missing') throw new Error('codex executable not found in PATH');
    if (kind === 'blocked') throw new Error(`codex executable is blocked in the current environment (${(result.error as NodeJS.ErrnoException).code || 'blocked'})`);
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`codex review exec failed (${result.status ?? 'unknown'}): ${(result.stderr || '').trim()}`);
  }
  return `${result.stdout || ''}`.trim();
}

function extractJsonObject(text: string): string {
  const fenced = text.match(/```(?:json)?\s*([\s\S]*?)```/i);
  if (fenced?.[1]) return fenced[1].trim();
  const start = text.indexOf('{');
  const end = text.lastIndexOf('}');
  if (start >= 0 && end > start) return text.slice(start, end + 1).trim();
  throw new Error(`AI output did not contain JSON: ${text.slice(0, 200)}`);
}

function parsePullReviewAiCandidateResponse(text: string): GithubPullReviewAiCandidateResponse {
  return JSON.parse(extractJsonObject(text)) as GithubPullReviewAiCandidateResponse;
}

function parsePullReviewAiValidationResponse(text: string): GithubPullReviewAiValidationResponse {
  return JSON.parse(extractJsonObject(text)) as GithubPullReviewAiValidationResponse;
}

function pullReviewCheckoutPath(runPaths: GithubPullReviewRunPaths): string {
  return join(runPaths.runDir, 'checkout');
}

async function ensurePullReviewCheckout(
  paths: ManagedRepoPaths,
  repoMeta: ManagedRepoMetadata,
  target: ParsedGithubTargetUrl,
  targetContext: GithubTargetContext,
  runPaths: GithubPullReviewRunPaths,
): Promise<string> {
  ensureSourceClone(paths, repoMeta);
  if (!targetContext.pullRequest) throw new Error('Review target is missing pull request metadata.');
  const checkoutPath = pullReviewCheckoutPath(runPaths);
  const headRef = `refs/remotes/origin/nana-review-pr/${target.targetNumber}`;
  const baseRef = `refs/remotes/origin/nana-review-base/${target.targetNumber}`;
  gitExec(paths.sourcePath, ['fetch', '--force', 'origin', targetContext.pullRequest.base.ref]);
  gitExec(paths.sourcePath, ['fetch', '--force', 'origin', `${targetContext.pullRequest.base.ref}:${baseRef}`]);
  gitExec(paths.sourcePath, ['fetch', '--force', 'origin', `pull/${target.targetNumber}/head:${headRef}`]);
  await removeExistingSandboxWorktree(paths, checkoutPath);
  await mkdir(runPaths.runDir, { recursive: true });
  gitExec(paths.sourcePath, ['worktree', 'add', '--force', '--detach', checkoutPath, targetContext.pullRequest.head.sha]);
  return checkoutPath;
}

function resolveDefaultBranchHeadSha(repoPath: string, defaultBranch: string): string {
  return readGitText(repoPath, ['rev-parse', `origin/${defaultBranch}`]);
}

function readGitText(cwd: string, args: string[]): string {
  return execFileSync('git', args, {
    cwd,
    encoding: 'utf-8',
    stdio: ['ignore', 'pipe', 'pipe'],
  }).trimEnd();
}

function listPullReviewChangedFiles(repoPath: string, baseSha: string, headSha: string): string[] {
  const output = readGitText(repoPath, ['diff', '--name-only', `${baseSha}..${headSha}`]);
  return output.split('\n').map((line) => line.trim()).filter(Boolean);
}

function readPullReviewDiff(repoPath: string, baseSha: string, headSha: string, path?: string): string {
  const args = ['diff', '--no-color', '--unified=5', `${baseSha}..${headSha}`];
  if (path) args.push('--', path);
  return readGitText(repoPath, args);
}

function buildChangedLineMap(repoPath: string, baseSha: string, headSha: string): Map<string, Array<{ start: number; end: number }>> {
  const diff = readGitText(repoPath, ['diff', '--no-color', '--unified=0', `${baseSha}..${headSha}`]);
  const map = new Map<string, Array<{ start: number; end: number }>>();
  let currentPath: string | null = null;
  for (const line of diff.split('\n')) {
    if (line.startsWith('+++ b/')) {
      currentPath = line.slice('+++ b/'.length).trim();
      if (!map.has(currentPath)) map.set(currentPath, []);
      continue;
    }
    const match = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@/.exec(line);
    if (!match || !currentPath) continue;
    const start = Number.parseInt(match[1] || '0', 10);
    const count = Number.parseInt(match[2] || '1', 10);
    if (count <= 0) continue;
    map.get(currentPath)?.push({ start, end: start + count - 1 });
  }
  return map;
}

function isChangedLine(
  changedLineMap: Map<string, Array<{ start: number; end: number }>>,
  path: string,
  line: number | undefined,
): boolean {
  if (line == null) return false;
  const ranges = changedLineMap.get(path);
  if (!ranges) return false;
  return ranges.some((range) => line >= range.start && line <= range.end);
}

function buildGithubBlobPermalink(repoSlug: string, sha: string, path: string, line?: number): string {
  const encodedPath = path.split('/').map((segment) => encodeURIComponent(segment)).join('/');
  return `https://github.com/${repoSlug}/blob/${sha}/${encodedPath}${line != null ? `#L${line}` : ''}`;
}

function readFileAtGitRef(repoPath: string, sha: string, path: string): string | undefined {
  const result = gitExecAllowFailure(repoPath, ['show', `${sha}:${path}`]);
  if (!result.ok) return undefined;
  return result.stdout;
}

function buildReviewSuppressionDigest(
  buckets: {
    userDropped: Map<string, GithubPullReviewFinding>;
    notReal: Map<string, GithubPullReviewFinding>;
    preexisting: Map<string, GithubPullReviewFinding>;
  },
): string {
  const lines: string[] = [];
  for (const bucket of [buckets.userDropped, buckets.notReal, buckets.preexisting]) {
    for (const finding of bucket.values()) {
      lines.push(`- ${finding.title} @ ${renderFindingReference(finding)} :: ${finding.user_explanation ?? finding.summary}`);
    }
  }
  return lines.join('\n');
}

function buildPullReviewGenerationPrompt(input: {
  manifest: GithubPullReviewManifest;
  changedFiles: readonly string[];
  diff: string;
  suppressionDigest: string;
}): string {
  return [
    'Review this pull request and return JSON only.',
    '',
    'Output schema:',
    '{"findings":[{"title":"string","severity":"critical|high|medium|low","path":"string","line":123,"summary":"string","detail":"string","fix":"string","rationale":"string"}]}',
    '',
    'Rules:',
    '- Be concise and concrete.',
    '- Report only actionable code issues.',
    '- Assume the reader already knows the repository.',
    '- Prefer findings tied to concrete file paths and line numbers in changed files.',
    '- Do not include markdown fences.',
    '- If there are no issues, return {"findings":[]}.',
    '',
    `PR: ${input.manifest.target_url}`,
    `Title: ${input.manifest.target_title}`,
    `Base: ${input.manifest.pr_base_ref} (${input.manifest.pr_base_sha})`,
    `Head: ${input.manifest.pr_head_ref} (${input.manifest.pr_head_sha})`,
    '',
    'Changed files:',
    ...input.changedFiles.map((file) => `- ${file}`),
    '',
    input.suppressionDigest ? `Previously dropped/suppressed findings for this PR:\n${input.suppressionDigest}\n` : '',
    'Diff:',
    input.diff,
    '',
  ].join('\n');
}

function buildPullReviewValidationPrompt(input: {
  manifest: GithubPullReviewManifest;
  candidates: readonly GithubPullReviewFinding[];
  contexts: ReadonlyArray<{
    headExcerpt?: string;
    baseExcerpt?: string;
    pathDiff: string;
  }>;
}): string {
  const items = input.candidates.map((candidate, index) => {
    const context = input.contexts[index]!;
    return [
      `Candidate ${index}:`,
      JSON.stringify({
        candidate_index: index,
        title: candidate.title,
        severity: candidate.severity,
        path: candidate.path,
        line: candidate.line,
        summary: candidate.summary,
        detail: candidate.detail,
        fix: candidate.fix,
        rationale: candidate.rationale,
      }, null, 2),
      'Head excerpt:',
      context.headExcerpt ?? '(none)',
      'Base excerpt:',
      context.baseExcerpt ?? '(none)',
      'Path diff:',
      context.pathDiff || '(none)',
      '',
    ].join('\n');
  }).join('\n');
  return [
    'Validate these PR review findings and return JSON only.',
    '',
    'Output schema:',
    '{"items":[{"candidate_index":0,"verdict":"accepted|not_real|preexisting","summary":"string","detail":"string","fix":"string","rationale":"string","explanation":"string"}]}',
    '',
    'Rules:',
    '- Classify each candidate as accepted, not_real, or preexisting.',
    '- Use preexisting only when the underlying problem clearly existed in the base version before this PR.',
    '- Keep text concise.',
    '- Do not omit any candidate.',
    '- Do not include markdown fences.',
    '',
    `PR: ${input.manifest.target_url}`,
    items,
  ].join('\n');
}

function buildPullReviewReconsiderPrompt(input: {
  finding: GithubPullReviewFinding;
  explanation: string;
  headExcerpt?: string;
  baseExcerpt?: string;
  pathDiff: string;
}): string {
  return [
    'Reconsider this PR review finding using the human explanation and return JSON only.',
    '',
    'Output schema:',
    '{"items":[{"candidate_index":0,"verdict":"accepted|not_real|preexisting","summary":"string","detail":"string","fix":"string","rationale":"string","explanation":"string"}]}',
    '',
    'Human explanation:',
    input.explanation,
    '',
    'Finding:',
    JSON.stringify({
      candidate_index: 0,
      title: input.finding.title,
      severity: input.finding.severity,
      path: input.finding.path,
      line: input.finding.line,
      summary: input.finding.summary,
      detail: input.finding.detail,
      fix: input.finding.fix,
      rationale: input.finding.rationale,
    }, null, 2),
    'Head excerpt:',
    input.headExcerpt ?? '(none)',
    'Base excerpt:',
    input.baseExcerpt ?? '(none)',
    'Path diff:',
    input.pathDiff || '(none)',
    '',
  ].join('\n');
}

async function collectPullReviewValidationContexts(
  repoPath: string,
  baseSha: string,
  headSha: string,
  findings: readonly GithubPullReviewFinding[],
): Promise<Array<{ headExcerpt?: string; baseExcerpt?: string; pathDiff: string }>> {
  return findings.map((finding) => ({
    headExcerpt: finding.line != null ? buildCodeContextExcerpt(readFileAtGitRef(repoPath, headSha, finding.path) ?? '', finding.line) : undefined,
    baseExcerpt: finding.line != null ? buildCodeContextExcerpt(readFileAtGitRef(repoPath, baseSha, finding.path) ?? '', finding.line) : undefined,
    pathDiff: readPullReviewDiff(repoPath, baseSha, headSha, finding.path),
  }));
}

async function generatePullReviewCandidates(
  manifest: GithubPullReviewManifest,
  repoPath: string,
  buckets: {
    userDropped: Map<string, GithubPullReviewFinding>;
    notReal: Map<string, GithubPullReviewFinding>;
    preexisting: Map<string, GithubPullReviewFinding>;
  },
  codexExec: NonNullable<GithubPullReviewCommandDependencies['codexExec']>,
  env: NodeJS.ProcessEnv,
): Promise<GithubPullReviewFinding[]> {
  const changedFiles = listPullReviewChangedFiles(repoPath, manifest.pr_base_sha, manifest.pr_head_sha);
  const diff = readPullReviewDiff(repoPath, manifest.pr_base_sha, manifest.pr_head_sha);
  const suppressionDigest = buildReviewSuppressionDigest(buckets);
  const response = parsePullReviewAiCandidateResponse(await codexExec(
    buildPullReviewGenerationPrompt({ manifest, changedFiles, diff, suppressionDigest }),
    { cwd: repoPath, env },
  ));
  const changedLineMap = buildChangedLineMap(repoPath, manifest.pr_base_sha, manifest.pr_head_sha);
  const changedFilesSet = new Set(changedFiles);
  const findings: GithubPullReviewFinding[] = [];
  const seenFingerprints = new Set<string>();
  for (const [index, rawFinding] of (response.findings ?? []).entries()) {
    const path = rawFinding.path?.trim();
    if (!path || !changedFilesSet.has(path)) continue;
    const line = typeof rawFinding.line === 'number' && Number.isFinite(rawFinding.line) && rawFinding.line > 0
      ? Math.trunc(rawFinding.line)
      : undefined;
    const summary = rawFinding.summary?.trim();
    const title = rawFinding.title?.trim();
    const detail = rawFinding.detail?.trim();
    if (!summary || !title || !detail) continue;
    const fingerprint = buildPullReviewFindingFingerprint({ title, path, line, summary });
    if (seenFingerprints.has(fingerprint) || buckets.userDropped.has(fingerprint) || buckets.notReal.has(fingerprint) || buckets.preexisting.has(fingerprint)) {
      continue;
    }
    seenFingerprints.add(fingerprint);
    const changedLineInPr = isChangedLine(changedLineMap, path, line);
    findings.push({
      id: `${manifest.run_id}-cand-${index + 1}`,
      fingerprint,
      title,
      severity: normalizePullReviewSeverity(rawFinding.severity),
      path,
      line,
      summary,
      detail,
      fix: rawFinding.fix?.trim() || '',
      rationale: rawFinding.rationale?.trim() || summary,
      changed_in_pr: true,
      changed_line_in_pr: changedLineInPr,
      main_permalink: buildGithubBlobPermalink(manifest.repo_slug, manifest.default_branch_sha, path, line),
      pr_permalink: buildGithubBlobPermalink(manifest.repo_slug, manifest.pr_head_sha, path, line),
      iteration: manifest.iteration,
    });
  }
  return findings;
}

async function validatePullReviewFindings(
  manifest: GithubPullReviewManifest,
  repoPath: string,
  candidates: readonly GithubPullReviewFinding[],
  codexExec: NonNullable<GithubPullReviewCommandDependencies['codexExec']>,
  env: NodeJS.ProcessEnv,
  perItemContext: GithubPullReviewPerItemContext,
): Promise<{
  accepted: GithubPullReviewFinding[];
  notReal: GithubPullReviewFinding[];
  preexisting: GithubPullReviewFinding[];
}> {
  const contexts = await collectPullReviewValidationContexts(repoPath, manifest.pr_base_sha, manifest.pr_head_sha, candidates);
  const accepted: GithubPullReviewFinding[] = [];
  const notReal: GithubPullReviewFinding[] = [];
  const preexisting: GithubPullReviewFinding[] = [];

  const applyResponse = (items: readonly GithubPullReviewAiValidationItem[]): void => {
    for (const item of items) {
      const candidate = candidates[item.candidate_index];
      if (!candidate) continue;
      const refined: GithubPullReviewFinding = {
        ...candidate,
        summary: item.summary?.trim() || candidate.summary,
        detail: item.detail?.trim() || candidate.detail,
        fix: item.fix?.trim() || candidate.fix,
        rationale: item.rationale?.trim() || candidate.rationale,
        user_explanation: item.explanation?.trim() || undefined,
      };
      if (item.verdict === 'accepted') {
        accepted.push(refined);
      } else if (item.verdict === 'preexisting') {
        preexisting.push(refined);
      } else {
        notReal.push(refined);
      }
    }
  };

  if (perItemContext === 'shared') {
    const response = parsePullReviewAiValidationResponse(await codexExec(
      buildPullReviewValidationPrompt({ manifest, candidates, contexts }),
      { cwd: repoPath, env },
    ));
    applyResponse(response.items ?? []);
    return { accepted, notReal, preexisting };
  }

  for (const [index, candidate] of candidates.entries()) {
    const response = parsePullReviewAiValidationResponse(await codexExec(
      buildPullReviewValidationPrompt({
        manifest,
        candidates: [candidate],
        contexts: [contexts[index]!],
      }),
      { cwd: repoPath, env },
    ));
    const normalized = (response.items ?? []).map((item) => ({ ...item, candidate_index: index }));
    applyResponse(normalized);
  }
  return { accepted, notReal, preexisting };
}

async function reconsiderPullReviewArguedFindings(
  manifest: GithubPullReviewManifest,
  repoPath: string,
  argued: Array<{ finding: GithubPullReviewFinding; explanation: string }>,
  codexExec: NonNullable<GithubPullReviewCommandDependencies['codexExec']>,
  env: NodeJS.ProcessEnv,
  perItemContext: GithubPullReviewPerItemContext,
): Promise<{
  accepted: GithubPullReviewFinding[];
  notReal: GithubPullReviewFinding[];
  preexisting: GithubPullReviewFinding[];
}> {
  const contexts = await collectPullReviewValidationContexts(repoPath, manifest.pr_base_sha, manifest.pr_head_sha, argued.map((entry) => entry.finding));
  const accepted: GithubPullReviewFinding[] = [];
  const notReal: GithubPullReviewFinding[] = [];
  const preexisting: GithubPullReviewFinding[] = [];
  const apply = (entry: { finding: GithubPullReviewFinding; explanation: string }, item: GithubPullReviewAiValidationItem): void => {
    const refined: GithubPullReviewFinding = {
      ...entry.finding,
      summary: item.summary?.trim() || entry.finding.summary,
      detail: item.detail?.trim() || entry.finding.detail,
      fix: item.fix?.trim() || entry.finding.fix,
      rationale: item.rationale?.trim() || entry.finding.rationale,
      user_explanation: entry.explanation,
    };
    if (item.verdict === 'accepted') accepted.push(refined);
    else if (item.verdict === 'preexisting') preexisting.push(refined);
    else notReal.push(refined);
  };

  if (perItemContext === 'shared') {
    const response = parsePullReviewAiValidationResponse(await codexExec(
      buildPullReviewValidationPrompt({
        manifest,
        candidates: argued.map((entry) => entry.finding),
        contexts,
      }),
      { cwd: repoPath, env },
    ));
    for (const item of response.items ?? []) {
      const entry = argued[item.candidate_index];
      if (entry) apply(entry, item);
    }
    return { accepted, notReal, preexisting };
  }

  for (const [index, entry] of argued.entries()) {
    const response = parsePullReviewAiValidationResponse(await codexExec(
      buildPullReviewReconsiderPrompt({
        finding: entry.finding,
        explanation: entry.explanation,
        ...contexts[index]!,
      }),
      { cwd: repoPath, env },
    ));
    const item = response.items?.[0];
    if (item) apply(entry, item);
  }
  return { accepted, notReal, preexisting };
}

async function submitGithubPullReview(
  manifest: GithubPullReviewManifest,
  findings: readonly GithubPullReviewFinding[],
  context: GithubApiContext,
): Promise<{ id?: number; html_url?: string; event: 'APPROVE' | 'REQUEST_CHANGES' }> {
  const event: 'APPROVE' | 'REQUEST_CHANGES' = findings.length > 0 ? 'REQUEST_CHANGES' : 'APPROVE';
  const body = formatPullReviewSummary(findings, event);
  const commentable = findings
    .filter((finding) => finding.changed_line_in_pr && finding.line != null)
    .map((finding) => ({
      path: finding.path,
      line: finding.line!,
      side: 'RIGHT',
      body: formatPullReviewFindingForGithub(finding),
    }));
  try {
    const payload = await githubApiRequestJson<{ id?: number; html_url?: string }>(
      'POST',
      `/repos/${manifest.repo_slug}/pulls/${manifest.target_number}/reviews`,
      {
        body,
        event,
        ...(commentable.length > 0 ? { comments: commentable } : {}),
      },
      context,
    );
    return { ...payload, event };
  } catch (error) {
    if (commentable.length === 0) throw error;
    const payload = await githubApiRequestJson<{ id?: number; html_url?: string }>(
      'POST',
      `/repos/${manifest.repo_slug}/pulls/${manifest.target_number}/reviews`,
      { body, event },
      context,
    );
    return { ...payload, event };
  }
}

async function runPullReviewManualLoop(
  manifest: GithubPullReviewManifest,
  runPaths: GithubPullReviewRunPaths,
  reviewPaths: GithubPullReviewPaths,
  repoPath: string,
  acceptedSeed: GithubPullReviewFinding[],
  codexExec: NonNullable<GithubPullReviewCommandDependencies['codexExec']>,
  openEditor: NonNullable<GithubPullReviewCommandDependencies['openEditor']>,
  env: NodeJS.ProcessEnv,
): Promise<{
  accepted: GithubPullReviewFinding[];
  userDropped: GithubPullReviewFinding[];
  notReal: GithubPullReviewFinding[];
  preexisting: GithubPullReviewFinding[];
}> {
  let pending = [...acceptedSeed];
  const accepted: GithubPullReviewFinding[] = [];
  const userDropped: GithubPullReviewFinding[] = [];
  const notReal: GithubPullReviewFinding[] = [];
  const preexisting: GithubPullReviewFinding[] = [];

  while (pending.length > 0) {
    await writePullReviewBucket(runPaths.manualPendingPath, pending);
    await writeFile(runPaths.reviewFilePath, buildPullReviewMarkdown(pending), 'utf-8');
    await writePullReviewManifest(runPaths, {
      ...manifest,
      status: 'awaiting-manual',
      updated_at: new Date().toISOString(),
      review_file_path: runPaths.reviewFilePath,
    });
    await writePullReviewActive(reviewPaths, {
      version: 1,
      run_id: manifest.run_id,
      status: 'awaiting-manual',
      updated_at: new Date().toISOString(),
    });
    await openEditor(runPaths.reviewFilePath, { cwd: repoPath });
    const content = await readFile(runPaths.reviewFilePath, 'utf-8');
    const decisions = parsePullReviewMarkdown(content, pending);
    const argued = decisions.filter((entry) => entry.action === 'argue').map((entry) => ({
      finding: entry.finding,
      explanation: entry.explanation,
    }));
    for (const entry of decisions.filter((item) => item.action === 'drop')) {
      userDropped.push({ ...entry.finding, user_explanation: entry.explanation });
    }
    accepted.push(...decisions.filter((item) => item.action === 'accept').map((item) => item.finding));
    if (argued.length === 0) break;
    const reconsidered = await reconsiderPullReviewArguedFindings(
      manifest,
      repoPath,
      argued,
      codexExec,
      env,
      manifest.per_item_context,
    );
    notReal.push(...reconsidered.notReal);
    preexisting.push(...reconsidered.preexisting);
    pending = reconsidered.accepted;
  }

  await rm(runPaths.manualPendingPath, { force: true });
  return { accepted, userDropped, notReal, preexisting };
}

async function showGithubPullReviewFollowup(
  parsed: GithubReviewFollowupCommand,
  dependencies: Required<Pick<GithubPullReviewCommandDependencies, 'fetchImpl' | 'writeLine' | 'env'>> & Pick<GithubPullReviewCommandDependencies, 'execFileSyncImpl' | 'homeDir'>,
): Promise<void> {
  const apiBaseUrl = dependencies.env.GITHUB_API_URL?.trim() || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(dependencies.env, apiBaseUrl, dependencies.execFileSyncImpl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl: dependencies.fetchImpl };
  const targetContext = await fetchTargetContext(parsed.target, apiContext);
  if (!targetContext.pullRequest) throw new Error('Followup requires a pull request target.');
  if (!parsed.allowOpen && targetContext.issue.state !== 'closed') {
    throw new Error(`PR #${parsed.target.targetNumber} is still open. Re-run with --allow-open to inspect pre-existing findings before closure.`);
  }
  const nanaHome = resolveNanaHomeDir(dependencies.env, dependencies.homeDir);
  const repoRoot = managedRepoPaths(nanaHome, join(parsed.target.owner, parsed.target.repoName)).repoRoot;
  const reviewPaths = githubPullReviewPaths(repoRoot, parsed.target.targetNumber);
  const buckets = await loadPersistedPullReviewBuckets(reviewPaths);
  const findings = [...buckets.preexisting.values()];
  if (findings.length === 0) {
    dependencies.writeLine(`[review] No persisted pre-existing findings for ${parsed.target.canonicalUrl}.`);
    return;
  }
  dependencies.writeLine(`[review] Pre-existing findings for ${parsed.target.canonicalUrl}:`);
  for (const finding of findings) {
    dependencies.writeLine(`- ${finding.title} (${renderFindingReference(finding)})`);
    dependencies.writeLine(`  ${finding.user_explanation ?? finding.detail}`);
    const link = renderFindingLink(finding);
    if (link) dependencies.writeLine(`  ${link}`);
  }
}

export async function githubPullReviewCommand(
  args: string[],
  dependencies: GithubPullReviewCommandDependencies = {},
): Promise<void> {
  const parsed = parseGithubReviewArgs(args);
  const writeLine = dependencies.writeLine ?? ((line: string) => console.log(line));
  const now = dependencies.now ?? (() => new Date());
  const fetchImpl = dependencies.fetchImpl ?? fetch;
  const env = dependencies.env ?? process.env;
  const execFileSyncImpl = dependencies.execFileSyncImpl;
  const homeDir = dependencies.homeDir;
  const codexExec = dependencies.codexExec ?? defaultCodexExec;
  const openEditor = dependencies.openEditor ?? defaultOpenEditor;

  if (parsed.subcommand === 'help') {
    writeLine(GITHUB_REVIEW_HELP);
    return;
  }

  if (parsed.subcommand === 'followup') {
    await showGithubPullReviewFollowup(parsed, { fetchImpl, writeLine, env, execFileSyncImpl, homeDir });
    return;
  }

  const apiBaseUrl = env.GITHUB_API_URL?.trim() || DEFAULT_GITHUB_API_BASE_URL;
  const token = resolveGithubToken(env, apiBaseUrl, execFileSyncImpl);
  const apiContext: GithubApiContext = { token, apiBaseUrl, fetchImpl };
  const viewerLogin = await resolveViewerLogin(apiContext);
  const reviewerLogin = resolveReviewerLogin(parsed.reviewer, viewerLogin);
  const targetContext = await fetchTargetContext(parsed.target, apiContext);
  if (!targetContext.pullRequest) throw new Error('nana review requires a pull request URL.');
  const nowValue = now();
  const nanaHome = resolveNanaHomeDir(env, homeDir);
  const managedPaths = managedRepoPaths(nanaHome, join(parsed.target.owner, parsed.target.repoName));
  const repoMeta = await ensureManagedRepoMetadata(managedPaths, targetContext, nowValue);
  const reviewPaths = githubPullReviewPaths(managedPaths.repoRoot, parsed.target.targetNumber);
  const persistedBuckets = await loadPersistedPullReviewBuckets(reviewPaths);

  let manifest: GithubPullReviewManifest;
  let runPaths: GithubPullReviewRunPaths;
  const activeRun = await resolveActivePullReviewRun(reviewPaths);
  if (activeRun) {
    manifest = activeRun.manifest;
    runPaths = activeRun.runPaths;
    writeLine(`[review] Resuming active review run ${manifest.run_id} for ${parsed.target.canonicalUrl}.`);
  } else {
    const runId = buildPullReviewRunId(nowValue);
    runPaths = githubPullReviewRunPaths(managedPaths.repoRoot, parsed.target.targetNumber, runId);
    manifest = {
      version: 1,
      run_id: runId,
      created_at: nowValue.toISOString(),
      updated_at: nowValue.toISOString(),
      status: 'running',
      repo_slug: repoMeta.repo_slug,
      repo_owner: repoMeta.repo_owner,
      repo_name: repoMeta.repo_name,
      managed_repo_root: managedPaths.repoRoot,
      source_path: managedPaths.sourcePath,
      review_root: runPaths.runDir,
      mode: parsed.mode,
      per_item_context: parsed.perItemContext,
      reviewer_login: reviewerLogin,
      target_url: parsed.target.canonicalUrl,
      target_number: parsed.target.targetNumber,
      target_title: targetContext.issue.title,
      target_state: targetContext.issue.state,
      default_branch: repoMeta.default_branch,
      default_branch_sha: '',
      pr_head_ref: targetContext.pullRequest.head.ref,
      pr_head_sha: targetContext.pullRequest.head.sha,
      pr_base_ref: targetContext.pullRequest.base.ref,
      pr_base_sha: targetContext.pullRequest.base.sha,
      iteration: 1,
    };
    await writePullReviewManifest(runPaths, manifest);
    await writePullReviewActive(reviewPaths, {
      version: 1,
      run_id: runId,
      status: 'running',
      updated_at: nowValue.toISOString(),
    });
  }

  const repoPath = await ensurePullReviewCheckout(managedPaths, repoMeta, parsed.target, targetContext, runPaths);
  const defaultBranchSha = resolveDefaultBranchHeadSha(managedPaths.sourcePath, repoMeta.default_branch);
  if (manifest.default_branch_sha !== defaultBranchSha) {
    manifest = {
      ...manifest,
      default_branch_sha: defaultBranchSha,
      updated_at: new Date().toISOString(),
    };
    await writePullReviewManifest(runPaths, manifest);
  }
  let accepted: GithubPullReviewFinding[];
  const droppedUser: GithubPullReviewFinding[] = [];
  let droppedNotReal: GithubPullReviewFinding[];
  let droppedPreexisting: GithubPullReviewFinding[];

  if (activeRun && manifest.mode === 'manual' && manifest.status === 'awaiting-manual' && existsSync(runPaths.manualPendingPath)) {
    accepted = await readPullReviewBucket(runPaths.manualPendingPath);
    droppedNotReal = await readPullReviewBucket(runPaths.droppedNotRealPath);
    droppedPreexisting = await readPullReviewBucket(runPaths.droppedPreexistingPath);
  } else {
    const candidates = await generatePullReviewCandidates(manifest, repoPath, persistedBuckets, codexExec, env);
    await writePullReviewBucket(runPaths.candidatesPath, candidates);
    const validated = await validatePullReviewFindings(
      manifest,
      repoPath,
      candidates,
      codexExec,
      env,
      manifest.per_item_context,
    );
    accepted = validated.accepted;
    droppedNotReal = [...validated.notReal];
    droppedPreexisting = [...validated.preexisting];
  }

  if (manifest.mode === 'manual') {
    const manualResult = await runPullReviewManualLoop(
      manifest,
      runPaths,
      reviewPaths,
      repoPath,
      accepted,
      codexExec,
      openEditor,
      env,
    );
    accepted = manualResult.accepted;
    droppedUser.push(...manualResult.userDropped);
    droppedNotReal.push(...manualResult.notReal);
    droppedPreexisting.push(...manualResult.preexisting);
  } else {
    await rm(runPaths.manualPendingPath, { force: true });
  }

  await writePullReviewBucket(runPaths.acceptedPath, accepted);
  await writePullReviewBucket(runPaths.droppedUserPath, droppedUser);
  await writePullReviewBucket(runPaths.droppedNotRealPath, droppedNotReal);
  await writePullReviewBucket(runPaths.droppedPreexistingPath, droppedPreexisting);

  const posted = await submitGithubPullReview(manifest, accepted, apiContext);
  manifest = {
    ...manifest,
    status: 'completed',
    updated_at: new Date().toISOString(),
    posted_review_event: posted.event,
    posted_review_id: posted.id,
    posted_review_url: posted.html_url,
  };
  await writePullReviewManifest(runPaths, manifest);
  await clearPullReviewActive(reviewPaths);

  writeLine(`[review] Completed review for ${manifest.target_url}.`);
  writeLine(`[review] Accepted=${accepted.length} user-dropped=${droppedUser.length} not-real=${droppedNotReal.length} pre-existing=${droppedPreexisting.length}.`);
  if (manifest.posted_review_url) {
    writeLine(`[review] GitHub review: ${manifest.posted_review_url}`);
  }
}

export async function listGithubWorkonRunIds(nanaHome: string): Promise<string[]> {
  const reposRoot = join(nanaHome, 'repos');
  if (!existsSync(reposRoot)) return [];
  const owners = await readdir(reposRoot, { withFileTypes: true });
  const runIds: string[] = [];
  for (const ownerEntry of owners) {
    if (!ownerEntry.isDirectory()) continue;
    const repoEntries = await readdir(join(reposRoot, ownerEntry.name), { withFileTypes: true });
    for (const repoEntry of repoEntries) {
      if (!repoEntry.isDirectory()) continue;
      const runsDir = join(reposRoot, ownerEntry.name, repoEntry.name, 'runs');
      if (!existsSync(runsDir)) continue;
      const runs = await readdir(runsDir, { withFileTypes: true });
      for (const runEntry of runs) {
        if (runEntry.isDirectory()) runIds.push(runEntry.name);
      }
    }
  }
  return runIds.sort();
}

export function describeGithubRunTarget(manifest: GithubWorkonManifest): string {
  return `${manifest.repo_slug} ${manifest.target_kind} #${manifest.target_number} (${manifest.target_title})`;
}
