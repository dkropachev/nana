/**
 * nana CLI
 * Multi-agent orchestration for OpenAI Codex CLI
 */

import { execFileSync, spawn } from "child_process";
import { basename, dirname, join } from "path";
import { existsSync, readFileSync } from "fs";
import { constants as osConstants } from "os";
import { setup, SETUP_SCOPES, type SetupScope } from "./setup.js";
import { uninstall } from "./uninstall.js";
import { version } from "./version.js";
import { hooksCommand } from "./hooks.js";
import { hudCommand } from "../hud/index.js";
import {
  githubCommand,
  githubPullReviewCommand,
  githubReviewRulesCommand,
  investigateGithubTarget,
  resolveGithubRunIdForTargetUrl,
} from "./github.js";
import { cleanupCommand } from "./cleanup.js";
import { exploreCommand } from "./explore.js";
import { sparkshellCommand } from "./sparkshell.js";
import { agentsInitCommand } from "./agents-init.js";
import { agentsCommand } from "./agents.js";
import { askCommand } from "./ask.js";
import { sessionCommand } from "./session-search.js";
import {
  MADMAX_FLAG,
  CODEX_BYPASS_FLAG,
  HIGH_REASONING_FLAG,
  XHIGH_REASONING_FLAG,
  SPARK_FLAG,
  MADMAX_SPARK_FLAG,
  CONFIG_FLAG,
  LONG_CONFIG_FLAG,
} from "./constants.js";
import {
  getBaseStateDir,
  getStateDir,
  listModeStateFilesWithScopePreference,
} from "../mcp/state-paths.js";
import { maybeCheckAndPromptUpdate } from "./update.js";
import { maybePromptGithubStar } from "./star-prompt.js";
import {
  generateOverlay,
  removeSessionModelInstructionsFile,
  resolveSessionOrchestrationMode,
  sessionModelInstructionsPath,
  writeSessionModelInstructionsFile,
} from "../hooks/agents-overlay.js";
import {
  readSessionState,
  writeSessionStart,
  writeSessionEnd,
  resetSessionMetrics,
} from "../hooks/session.js";
import {
  buildClientAttachedReconcileHookName,
  buildReconcileHudResizeArgs,
  buildRegisterClientAttachedReconcileArgs,
  buildRegisterResizeHookArgs,
  buildResizeHookName,
  buildResizeHookTarget,
  buildScheduleDelayedHudResizeArgs,
  buildUnregisterClientAttachedReconcileArgs,
  buildUnregisterResizeHookArgs,
  enableMouseScrolling,
  isNativeWindows,
  isTmuxAvailable,
} from "../team/tmux-session.js";
import { getPackageRoot } from "../utils/package.js";
import {
  codexAuthPath,
  codexConfigPath,
  defaultUserCodexHome,
  legacyDefaultUserCodexHome,
} from "../utils/paths.js";
import { repairConfigIfNeeded } from "../config/generator.js";
import { HUD_TMUX_HEIGHT_LINES } from "../hud/constants.js";
import {
  classifySpawnError,
  spawnPlatformCommandSync,
} from "../utils/platform-command.js";
import { buildHookEvent } from "../hooks/extensibility/events.js";
import { dispatchHookEvent } from "../hooks/extensibility/dispatcher.js";
import {
  collectInheritableTeamWorkerArgs as collectInheritableTeamWorkerArgsShared,
  resolveTeamWorkerLaunchArgs,
  resolveTeamLowComplexityDefaultModel,
} from "../team/model-contract.js";
import {
  parseWorktreeMode,
  planWorktreeTarget,
  ensureWorktree,
} from "../team/worktree.js";
import {
  NANA_NOTIFY_TEMP_CONTRACT_ENV,
  parseNotifyTempContractFromArgs,
  serializeNotifyTempContract,
  type NotifyTempContract,
  type ParseNotifyTempContractResult,
} from "../notifications/temp-contract.js";

export function resolveNotifyFallbackWatcherScript(pkgRoot = getPackageRoot()): string {
  return join(pkgRoot, "dist", "scripts", "notify-fallback-watcher.js");
}

export function resolveGoCliBinaryPath(
  pkgRoot = getPackageRoot(),
  platform: NodeJS.Platform = process.platform,
): string {
  return join(pkgRoot, "bin", platform === "win32" ? "nana.exe" : "nana");
}

export function shouldDelegateToGoCLI(
  env: NodeJS.ProcessEnv = process.env,
  options: {
    pkgRoot?: string;
    exists?: (path: string) => boolean;
    platform?: NodeJS.Platform;
  } = {},
): boolean {
  if (env[NANA_GO_SHIM_ACTIVE_ENV] === "1") return false;
  if (env[NANA_IMPL_ENV]?.trim().toLowerCase() !== "go") return false;
  const exists = options.exists ?? existsSync;
  return exists(
    resolveGoCliBinaryPath(
      options.pkgRoot ?? getPackageRoot(),
      options.platform ?? process.platform,
    ),
  );
}

export function resolveHookDerivedWatcherScript(pkgRoot = getPackageRoot()): string {
  return join(pkgRoot, "dist", "scripts", "hook-derived-watcher.js");
}

export function resolveNotifyHookScript(pkgRoot = getPackageRoot()): string {
  return join(pkgRoot, "dist", "scripts", "notify-hook.js");
}

const HELP = `
nana - Multi-agent orchestration for Codex CLI

Usage:
  nana           Launch Codex CLI (HUD auto-attaches only when already inside tmux)
  nana exec      Run codex exec non-interactively with NANA AGENTS/overlay injection
  nana setup     Install skills, prompts, MCP servers, and scope-specific AGENTS.md
  nana uninstall Remove NANA configuration and clean up installed artifacts
  nana doctor    Check installation health
  nana cleanup   Kill orphaned NANA MCP server processes and remove stale NANA /tmp directories
  nana auth pull Pull credentials from legacy ~/.codex/auth.json into the resolved NANA Codex home
  nana ask <claude|gemini> ...  Run a local provider advisor prompt
  nana implement <issue-url>    Alias for: nana issue implement <issue-url>
  nana investigate <issue-url>  Alias for: nana issue investigate <issue-url>
  nana sync [flags]             Alias for: nana issue sync [flags]
  nana issue     GitHub issue-centric aliases for implement/investigate/sync
  nana review    Review an external GitHub PR with automatic onboarding
  nana review-rules  Mine PR review history into repo-scoped persistent rules
  nana work-on   Launch GitHub-targeted issue/PR implementation and review sync
  nana resume    Resume a previous interactive Codex session
  nana reflect   Default read-only reflection entrypoint (may adaptively use sparkshell backend)
  nana session   Search prior local session transcripts and history artifacts
  nana agents-init [path]
                Bootstrap lightweight AGENTS.md files for a repo/subtree
  nana agents    Manage Codex native agent TOML files
  nana deepinit [path]
                Alias for agents-init (lightweight AGENTS bootstrap only)
  nana version   Show version information
  nana hooks     Manage hook plugins (init|status|validate|test)
  nana hud       Show HUD statusline (--watch, --json, --preset=NAME)
  nana sparkshell <command> [args...]
  nana sparkshell --tmux-pane <pane-id> [--tail-lines <100-1000>]
                Run native sparkshell sidecar for direct command execution or explicit tmux-pane summarization
                (also used as an adaptive backend for qualifying read-only reflect tasks)
  nana help      Show this help message
  nana status    Show active modes and state
  nana cancel    Cancel active execution modes
  nana reasoning Show or set model reasoning effort (low|medium|high|xhigh)

Options:
  --yolo        Launch Codex in yolo mode (shorthand for: nana launch --yolo)
  --high        Launch Codex with high reasoning effort
                (shorthand for: -c model_reasoning_effort="high")
  --xhigh       Launch Codex with xhigh reasoning effort
                (shorthand for: -c model_reasoning_effort="xhigh")
  --madmax      DANGEROUS: bypass Codex approvals and sandbox
                (alias for --dangerously-bypass-approvals-and-sandbox)
  --notify-temp  Enable temporary notification routing for this run/session only
  --discord      Select Discord provider for temporary notification mode
  --slack        Select Slack provider for temporary notification mode
  --telegram     Select Telegram provider for temporary notification mode
  --custom <name>
                Select custom/OpenClaw gateway name for temporary notification mode
  -w, --worktree[=<name>]
                Launch Codex in a git worktree (detached when no name is given)
  --force       Force reinstall (overwrite existing files)
  --dry-run     Show what would be done without doing it
  --keep-config Skip config.toml cleanup during uninstall
  --purge       Remove .nana/ cache directory during uninstall
  --verbose     Show detailed output
  --scope       Setup scope for "nana setup" only:
                user | project
  --skill-target
                User-scope skills target for "nana setup" only:
                codex-home
`;

const REASONING_KEY = "model_reasoning_effort";
const MODEL_INSTRUCTIONS_FILE_KEY = "model_instructions_file";
const TEAM_WORKER_LAUNCH_ARGS_ENV = "NANA_TEAM_WORKER_LAUNCH_ARGS";
const TEAM_INHERIT_LEADER_FLAGS_ENV = "NANA_TEAM_INHERIT_LEADER_FLAGS";
const NANA_IMPL_ENV = "NANA_IMPL";
const NANA_GO_SHIM_ACTIVE_ENV = "NANA_GO_SHIM_ACTIVE";
const NANA_BYPASS_DEFAULT_SYSTEM_PROMPT_ENV = "NANA_BYPASS_DEFAULT_SYSTEM_PROMPT";
const NANA_MODEL_INSTRUCTIONS_FILE_ENV = "NANA_MODEL_INSTRUCTIONS_FILE";
const NANA_RALPH_APPEND_INSTRUCTIONS_FILE_ENV =
  "NANA_RALPH_APPEND_INSTRUCTIONS_FILE";
const NANA_AUTORESEARCH_APPEND_INSTRUCTIONS_FILE_ENV =
  "NANA_AUTORESEARCH_APPEND_INSTRUCTIONS_FILE";
const NANA_GITHUB_APPEND_INSTRUCTIONS_FILE_ENV =
  "NANA_GITHUB_APPEND_INSTRUCTIONS_FILE";
const REASONING_MODES = ["low", "medium", "high", "xhigh"] as const;
type ReasoningMode = (typeof REASONING_MODES)[number];
const REASONING_MODE_SET = new Set<string>(REASONING_MODES);
const REASONING_USAGE = "Usage: nana reasoning <low|medium|high|xhigh>";
const ALLOWED_SHELLS = new Set([
  "/bin/sh",
  "/bin/bash",
  "/bin/zsh",
  "/bin/dash",
  "/bin/fish",
  "/usr/bin/sh",
  "/usr/bin/bash",
  "/usr/bin/zsh",
  "/usr/bin/dash",
  "/usr/bin/fish",
  "/usr/local/bin/bash",
  "/usr/local/bin/zsh",
  "/usr/local/bin/fish",
]);
const WINDOWS_DETACHED_BOOTSTRAP_DELAY_MS = 2500;
const CODEX_VERSION_FLAGS = new Set(["--version", "-V"]);

type CliCommand =
  | "launch"
  | "exec"
  | "auth"
  | "ask"
  | "implement"
  | "investigate"
  | "sync"
  | "issue"
  | "review"
  | "review-rules"
  | "setup"
  | "agents"
  | "agents-init"
  | "deepinit"
  | "uninstall"
  | "doctor"
  | "cleanup"
  | "reflect"
  | "sparkshell"
  | "session"
  | "resume"
  | "version"
  | "hooks"
  | "hud"
  | "status"
  | "cancel"
  | "help"
  | "reasoning"
  | string;

const NESTED_HELP_COMMANDS = new Set<CliCommand>([
  "cleanup",
  "implement",
  "investigate",
  "sync",
  "issue",
  "review",
  "review-rules",
  "agents",
  "agents-init",
  "deepinit",
  "exec",
  "ask",
  "work-on",
  "hooks",
  "hud",
  "reflect",
  "resume",
  "session",
  "sparkshell",
]);

export interface ResolvedCliInvocation {
  command: CliCommand;
  launchArgs: string[];
}

/**
 * Legacy scope values that may appear in persisted setup-scope.json files.
 * Both 'project-local' (renamed) and old 'project' (minimal, removed) are
 * migrated to the current 'project' scope on read.
 */
const LEGACY_SCOPE_MIGRATION_SYNC: Record<string, SetupScope> = {
  "project-local": "project",
};

export function readPersistedSetupScope(cwd: string): SetupScope | undefined {
  return readPersistedSetupPreferences(cwd)?.scope;
}

export function readPersistedSetupPreferences(
  cwd: string,
): Partial<{ scope: SetupScope }> | undefined {
  const scopePath = join(cwd, ".nana", "setup-scope.json");
  if (!existsSync(scopePath)) return undefined;
  try {
    const parsed = JSON.parse(readFileSync(scopePath, "utf-8")) as Partial<{
      scope: string;
    }>;
    const persisted: Partial<{ scope: SetupScope }> = {};
    if (typeof parsed.scope === "string") {
      if (SETUP_SCOPES.includes(parsed.scope as SetupScope)) {
        persisted.scope = parsed.scope as SetupScope;
      }
      const migrated = LEGACY_SCOPE_MIGRATION_SYNC[parsed.scope];
      if (migrated) persisted.scope = migrated;
    }
    return Object.keys(persisted).length > 0 ? persisted : undefined;
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Ignore malformed persisted scope and use defaults.
  }
  return undefined;
}

export function resolveCodexHomeForLaunch(
  cwd: string,
  env: NodeJS.ProcessEnv = process.env,
): string | undefined {
  if (env.CODEX_HOME && env.CODEX_HOME.trim() !== "") return env.CODEX_HOME;
  const persistedScope = readPersistedSetupScope(cwd);
  if (persistedScope === "project") {
    return join(cwd, ".codex");
  }
  return defaultUserCodexHome(env.HOME?.trim());
}

export interface CodexAuthImportResult {
  imported: boolean;
  sourceAuthPath: string;
  targetAuthPath: string;
  reason: "imported" | "missing-source" | "already-present" | "same-path";
}

function resolveLegacyCodexAuthSource(
  env: NodeJS.ProcessEnv = process.env,
): string {
  return codexAuthPath(legacyDefaultUserCodexHome(env.HOME?.trim()));
}

function resolveCodexAuthTarget(
  cwd: string,
  env: NodeJS.ProcessEnv = process.env,
): string {
  const targetHome =
    resolveCodexHomeForLaunch(cwd, env) ??
    defaultUserCodexHome(env.HOME?.trim());
  return codexAuthPath(targetHome);
}

export async function importLegacyCodexAuth(
  cwd: string,
  env: NodeJS.ProcessEnv = process.env,
  options: { overwrite?: boolean } = {},
): Promise<CodexAuthImportResult> {
  const { copyFile, mkdir } = await import("fs/promises");
  const sourceAuthPath = resolveLegacyCodexAuthSource(env);
  const targetAuthPath = resolveCodexAuthTarget(cwd, env);

  if (sourceAuthPath === targetAuthPath) {
    return {
      imported: false,
      sourceAuthPath,
      targetAuthPath,
      reason: "same-path",
    };
  }
  if (!existsSync(sourceAuthPath)) {
    return {
      imported: false,
      sourceAuthPath,
      targetAuthPath,
      reason: "missing-source",
    };
  }
  if (!options.overwrite && existsSync(targetAuthPath)) {
    return {
      imported: false,
      sourceAuthPath,
      targetAuthPath,
      reason: "already-present",
    };
  }

  await mkdir(dirname(targetAuthPath), { recursive: true });
  await copyFile(sourceAuthPath, targetAuthPath);
  return {
    imported: true,
    sourceAuthPath,
    targetAuthPath,
    reason: "imported",
  };
}

export async function bootstrapResolvedCodexAuth(
  cwd: string,
  env: NodeJS.ProcessEnv = process.env,
): Promise<CodexAuthImportResult> {
  if (env.CODEX_HOME && env.CODEX_HOME.trim() !== "") {
    return {
      imported: false,
      sourceAuthPath: resolveLegacyCodexAuthSource(env),
      targetAuthPath: resolveCodexAuthTarget(cwd, env),
      reason: "already-present",
    };
  }
  return importLegacyCodexAuth(cwd, env, { overwrite: false });
}

async function authCommand(
  args: string[],
  env: NodeJS.ProcessEnv = process.env,
): Promise<void> {
  const [subcommand] = args;
  if (!subcommand || subcommand === "help" || subcommand === "--help" || subcommand === "-h") {
    console.log("Usage: nana auth pull");
    return;
  }
  if (subcommand !== "pull") {
    throw new Error(`Unknown auth subcommand: ${subcommand}`);
  }
  const result = await importLegacyCodexAuth(process.cwd(), env, {
    overwrite: true,
  });
  if (result.reason === "missing-source") {
    throw new Error(
      `Legacy Codex credentials not found at ${result.sourceAuthPath}`,
    );
  }
  if (result.reason === "same-path") {
    console.log(
      `[nana] Credential source and target are the same: ${result.targetAuthPath}`,
    );
    return;
  }
  console.log(
    `[nana] Pulled Codex credentials from ${result.sourceAuthPath} to ${result.targetAuthPath}`,
  );
}

const ISSUE_HELP = `nana issue - GitHub issue-oriented aliases for the work-on runtime

Usage:
  nana issue implement <github-issue-url> [work-on start flags...]
  nana issue investigate <github-issue-url> [work-on start flags...]
  nana issue sync [work-on sync flags...]
  nana issue help

Behavior:
  - implement routes to: nana work-on start <issue-url> ...
  - investigate fetches issue + repo context, updates managed repo metadata, infers considerations, and stops before implementation
  - sync routes to: nana work-on sync ...
`;

const REVIEW_RULES_HELP = `nana review-rules - Persistent repo rules mined from PR review history

Usage:
  nana review-rules scan <owner/repo|github-issue-url|github-pr-url>
  nana review-rules list <owner/repo|github-issue-url|github-pr-url>
  nana review-rules approve <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]
  nana review-rules disable <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]
  nana review-rules enable <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]
  nana review-rules archive <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]
  nana review-rules explain <owner/repo|github-issue-url|github-pr-url> <rule-id>
  nana review-rules config set [--mode <manual|automatic>] [--trusted-reviewers <a,b>|none] [--blocked-reviewers <a,b>|none] [--min-distinct-reviewers <n>]
  nana review-rules config show [owner/repo|github-issue-url|github-pr-url]

Behavior:
  - scan mines PR reviews and review comments into pending repo rule candidates
  - global config controls the default extraction mode
  - global reviewer policy can trust, block, or require multiple distinct reviewers
  - repo-specific mode is configured via: nana work-on defaults set <owner/repo> --review-rules-mode <manual|automatic>
  - repo-specific reviewer policy is configured via: nana work-on defaults set <owner/repo> --review-rules-trusted-reviewers <a,b>|none --review-rules-blocked-reviewers <a,b>|none --review-rules-min-distinct-reviewers <n>
  - approve promotes pending candidates into approved rules
  - disable, enable, and archive manage rule lifecycle without deleting evidence
  - explain prints full rule metadata and evidence
  - approved rules are injected into related work-on role instructions
`;

async function issueCommand(args: string[]): Promise<void> {
  const [subcommand, ...rest] = args;
  if (!subcommand || subcommand === "help" || subcommand === "--help" || subcommand === "-h") {
    console.log(ISSUE_HELP);
    return;
  }
  if (rest[0] === "--help" || rest[0] === "-h" || rest[0] === "help") {
    console.log(ISSUE_HELP);
    return;
  }
  if (subcommand === "implement") {
    if (rest.length === 0) throw new Error("Usage: nana issue implement <github-issue-url> [work-on start flags...]");
    await githubCommand(["start", ...rest]);
    return;
  }
  if (subcommand === "investigate") {
    if (rest.length === 0) throw new Error("Usage: nana issue investigate <github-issue-url> [work-on start flags...]");
    await investigateGithubTarget(rest[0]!, {
      env: process.env,
      homeDir: process.env.HOME,
      fetchImpl: fetch,
      writeLine: (line) => console.log(line),
    });
    return;
  }
  if (subcommand === "sync") {
    if (rest[0]?.startsWith("https://github.com/")) {
      const runId = await resolveGithubRunIdForTargetUrl(rest[0], {
        env: process.env,
        homeDir: process.env.HOME,
      });
      if (!runId) {
        throw new Error(`No managed NANA run found for ${rest[0]}`);
      }
      await githubCommand(["sync", "--run-id", runId, rest[0], ...rest.slice(1)]);
      return;
    }
    await githubCommand(["sync", ...rest]);
    return;
  }
  throw new Error(`Unknown issue subcommand: ${subcommand}`);
}

async function reviewRulesCommand(args: string[]): Promise<void> {
  const [subcommand, ...rest] = args;
  if (!subcommand || subcommand === "help" || subcommand === "--help" || subcommand === "-h") {
    console.log(REVIEW_RULES_HELP);
    return;
  }
  if (rest[0] === "--help" || rest[0] === "-h" || rest[0] === "help") {
    console.log(REVIEW_RULES_HELP);
    return;
  }
  await githubReviewRulesCommand([subcommand, ...rest], {
    env: process.env,
    homeDir: process.env.HOME,
    fetchImpl: fetch,
    writeLine: (line) => console.log(line),
  });
}

export function resolveSetupScopeArg(args: string[]): SetupScope | undefined {
  let value: string | undefined;
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === "--scope") {
      const next = args[index + 1];
      if (!next || next.startsWith("-")) {
        throw new Error(
          `Missing setup scope value after --scope. Expected one of: ${SETUP_SCOPES.join(", ")}`,
        );
      }
      value = next;
      index += 1;
      continue;
    }
    if (arg.startsWith("--scope=")) {
      value = arg.slice("--scope=".length);
    }
  }
  if (!value) return undefined;
  if (SETUP_SCOPES.includes(value as SetupScope)) {
    return value as SetupScope;
  }
  throw new Error(
    `Invalid setup scope: ${value}. Expected one of: ${SETUP_SCOPES.join(", ")}`,
  );
}

export function resolveCliInvocation(args: string[]): ResolvedCliInvocation {
  const firstArg = args[0];
  if (firstArg === "--help" || firstArg === "-h") {
    return { command: "help", launchArgs: [] };
  }
  if (firstArg === "--version" || firstArg === "-v") {
    return { command: "version", launchArgs: [] };
  }
  if (!firstArg || firstArg.startsWith("--")) {
    return { command: "launch", launchArgs: firstArg ? args : [] };
  }
  if (firstArg === "launch") {
    return { command: "launch", launchArgs: args.slice(1) };
  }
  if (firstArg === "exec") {
    return { command: "exec", launchArgs: args.slice(1) };
  }
  if (firstArg === "ask") {
    return { command: "ask", launchArgs: args.slice(1) };
  }
  if (firstArg === "implement") {
    return { command: "implement", launchArgs: args.slice(1) };
  }
  if (firstArg === "investigate") {
    return { command: "investigate", launchArgs: args.slice(1) };
  }
  if (firstArg === "sync") {
    return { command: "sync", launchArgs: args.slice(1) };
  }
  if (firstArg === "issue") {
    return { command: "issue", launchArgs: [] };
  }
  if (firstArg === "review") {
    return { command: "review", launchArgs: [] };
  }
  if (firstArg === "review-rules") {
    return { command: "review-rules", launchArgs: [] };
  }
  if (firstArg === "resume") {
    return { command: "resume", launchArgs: args.slice(1) };
  }
  if (firstArg === "explore") {
    return { command: "reflect", launchArgs: [] };
  }
  return { command: firstArg, launchArgs: [] };
}

export function resolveNotifyTempContract(
  args: string[],
  env: NodeJS.ProcessEnv = process.env,
): ParseNotifyTempContractResult {
  return parseNotifyTempContractFromArgs(args, env);
}

export function commandOwnsLocalHelp(command: CliCommand): boolean {
  return NESTED_HELP_COMMANDS.has(command);
}

function isRemovedCommand(command: CliCommand): boolean {
  return (
    command === "team" ||
    command === "ralph" ||
    command === "research" ||
    command === "autoresearch"
  );
}

export type CodexLaunchPolicy = "inside-tmux" | "detached-tmux" | "direct";

export function resolveCodexLaunchPolicy(
  env: NodeJS.ProcessEnv = process.env,
  _platform: NodeJS.Platform = process.platform,
  tmuxAvailable: boolean = isTmuxAvailable(),
  nativeWindows: boolean = isNativeWindows(),
  stdinIsTTY: boolean = Boolean(process.stdin.isTTY),
  stdoutIsTTY: boolean = Boolean(process.stdout.isTTY),
): CodexLaunchPolicy {
  if (env.TMUX) return "inside-tmux";
  if (nativeWindows) return "direct";
  if (!stdinIsTTY || !stdoutIsTTY) return "direct";
  return tmuxAvailable ? "detached-tmux" : "direct";
}

type ExecFileSyncFailure = NodeJS.ErrnoException & {
  status?: number | null;
  signal?: NodeJS.Signals | null;
};

function hasErrnoCode(error: unknown, code: string): boolean {
  return Boolean(
    error &&
    typeof error === "object" &&
    "code" in error &&
    error.code === code,
  );
}

export interface CodexExecFailureClassification {
  kind: "exit" | "launch-error";
  code?: string;
  message: string;
  exitCode?: number;
  signal?: NodeJS.Signals;
}

export function resolveSignalExitCode(
  signal: NodeJS.Signals | null | undefined,
): number {
  if (!signal) return 1;
  const signalNumber = osConstants.signals[signal];
  if (typeof signalNumber === "number" && Number.isFinite(signalNumber)) {
    return 128 + signalNumber;
  }
  return 1;
}

export function classifyCodexExecFailure(
  error: unknown,
): CodexExecFailureClassification {
  if (!error || typeof error !== "object") {
    return {
      kind: "launch-error",
      message: String(error),
    };
  }

  const err = error as ExecFileSyncFailure;
  const code = typeof err.code === "string" ? err.code : undefined;
  const message =
    typeof err.message === "string" && err.message.length > 0
      ? err.message
      : "unknown codex launch failure";
  const hasExitStatus = typeof err.status === "number";
  const hasSignal = typeof err.signal === "string" && err.signal.length > 0;

  if (hasExitStatus || hasSignal) {
    return {
      kind: "exit",
      code,
      message,
      exitCode: hasExitStatus
        ? (err.status as number)
        : resolveSignalExitCode(err.signal),
      signal: hasSignal ? (err.signal as NodeJS.Signals) : undefined,
    };
  }

  return {
    kind: "launch-error",
    code,
    message,
  };
}

function runCodexBlocking(
  cwd: string,
  launchArgs: string[],
  codexEnv: NodeJS.ProcessEnv,
): void {
  const { result } = spawnPlatformCommandSync("codex", launchArgs, {
    cwd,
    stdio: "inherit",
    env: codexEnv,
    encoding: "utf-8",
  });

  if (result.error) {
    const errno = result.error as NodeJS.ErrnoException;
    const kind = classifySpawnError(errno);
    if (kind === "missing") {
      console.error(
        "[nana] failed to launch codex: executable not found in PATH",
      );
    } else if (kind === "blocked") {
      console.error(
        `[nana] failed to launch codex: executable is present but blocked in the current environment (${errno.code || "blocked"})`,
      );
    } else {
      console.error(`[nana] failed to launch codex: ${errno.message}`);
    }
    throw result.error;
  }

  if (result.status !== 0) {
    process.exitCode =
      typeof result.status === "number"
        ? result.status
        : resolveSignalExitCode(result.signal);
    if (result.signal) {
      console.error(`[nana] codex exited due to signal ${result.signal}`);
    }
  }
}

interface TmuxPaneSnapshot {
  paneId: string;
  currentCommand: string;
  startCommand: string;
}

export interface DetachedSessionTmuxStep {
  name: string;
  args: string[];
}

export function parseTmuxPaneSnapshot(output: string): TmuxPaneSnapshot[] {
  return output
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const [paneId = "", currentCommand = "", ...startCommandParts] =
        line.split("\t");
      return {
        paneId: paneId.trim(),
        currentCommand: currentCommand.trim(),
        startCommand: startCommandParts.join("\t").trim(),
      };
    })
    .filter((pane) => pane.paneId.startsWith("%"));
}

export function isHudWatchPane(pane: TmuxPaneSnapshot): boolean {
  const command = `${pane.startCommand} ${pane.currentCommand}`.toLowerCase();
  return (
    /\bhud\b/.test(command) &&
    /--watch\b/.test(command) &&
    (/\bnana(?:\.js)?\b/.test(command) || /\bnode\b/.test(command))
  );
}

export function findHudWatchPaneIds(
  panes: TmuxPaneSnapshot[],
  currentPaneId?: string,
): string[] {
  return panes
    .filter((pane) => pane.paneId !== currentPaneId)
    .filter((pane) => isHudWatchPane(pane))
    .map((pane) => pane.paneId);
}

export function buildHudPaneCleanupTargets(
  existingPaneIds: string[],
  createdPaneId: string | null,
  leaderPaneId?: string,
): string[] {
  const targets = new Set<string>(
    existingPaneIds.filter((id) => id.startsWith("%")),
  );
  if (createdPaneId && createdPaneId.startsWith("%")) {
    targets.add(createdPaneId);
  }
  // Guard: never kill the leader's own pane under any circumstances.
  if (leaderPaneId && leaderPaneId.startsWith("%")) {
    targets.delete(leaderPaneId);
  }
  return [...targets];
}

export async function main(args: string[]): Promise<void> {
  if (shouldDelegateToGoCLI(process.env)) {
    const goBinaryPath = resolveGoCliBinaryPath();
    const { result } = spawnPlatformCommandSync(goBinaryPath, args, {
      cwd: process.cwd(),
      stdio: "inherit",
      env: {
        ...process.env,
        [NANA_GO_SHIM_ACTIVE_ENV]: "1",
      },
      encoding: "utf-8",
    });
    if (result.error) {
      throw result.error;
    }
    if (typeof result.status === "number" && result.status !== 0) {
      process.exitCode = result.status;
    } else if (result.signal) {
      process.exitCode = resolveSignalExitCode(result.signal);
    }
    return;
  }

  const knownCommands = new Set([
    "launch",
    "exec",
    "ask",
    "auth",
    "implement",
    "investigate",
    "sync",
    "issue",
    "review",
    "setup",
    "agents",
    "agents-init",
    "deepinit",
    "uninstall",
    "doctor",
    "cleanup",
    "work-on",
    "reflect",
    "sparkshell",
    "session",
    "resume",
    "version",
    "hooks",
    "hud",
    "status",
    "cancel",
    "help",
    "--help",
    "-h",
  ]);
  const firstArg = args[0];
  const { command, launchArgs } = resolveCliInvocation(args);
  const flags = new Set(args.filter((a) => a.startsWith("--")));
  const options = {
    force: flags.has("--force"),
    dryRun: flags.has("--dry-run"),
    verbose: flags.has("--verbose"),
    team: flags.has("--team"),
  };

  if (flags.has("--help") && !commandOwnsLocalHelp(command) && !isRemovedCommand(command)) {
    console.log(HELP);
    return;
  }

  try {
    const launchBootstrapCommands = new Set<CliCommand>([
      "launch",
      "resume",
      "exec",
      "implement",
      "investigate",
      "sync",
      "review",
      "work-on",
      "issue",
      "review-rules",
    ]);
    if (launchBootstrapCommands.has(command)) {
      const bootstrap = await bootstrapResolvedCodexAuth(process.cwd(), process.env);
      if (bootstrap.imported) {
        console.log(
          `[nana] Imported Codex credentials from ${bootstrap.sourceAuthPath} to ${bootstrap.targetAuthPath}`,
        );
      }
    }
    switch (command) {
      case "launch":
        await launchWithHud(launchArgs);
        break;
      case "resume":
        await launchWithHud(["resume", ...launchArgs]);
        break;
      case "auth":
        await authCommand(args.slice(1), process.env);
        break;
      case "ask":
        await askCommand(args.slice(1));
        break;
      case "implement":
        await issueCommand(["implement", ...launchArgs]);
        break;
      case "investigate":
        await issueCommand(["investigate", ...launchArgs]);
        break;
      case "sync":
        await issueCommand(["sync", ...launchArgs]);
        break;
      case "issue":
        await issueCommand(args.slice(1));
        break;
      case "review":
        await githubPullReviewCommand(args.slice(1));
        break;
      case "review-rules":
        await reviewRulesCommand(args.slice(1));
        break;
      case "setup":
        await setup({
          force: options.force,
          dryRun: options.dryRun,
          verbose: options.verbose,
          scope: resolveSetupScopeArg(args.slice(1)),
        });
        break;
      case "agents":
        await agentsCommand(args.slice(1));
        break;
      case "agents-init":
        await agentsInitCommand(args.slice(1));
        break;
      case "deepinit":
        await agentsInitCommand(args.slice(1));
        break;
      case "uninstall":
        await uninstall({
          dryRun: options.dryRun,
          keepConfig: flags.has("--keep-config"),
          verbose: options.verbose,
          purge: flags.has("--purge"),
          scope: resolveSetupScopeArg(args.slice(1)),
        });
        break;
      case "doctor": {
        const { doctor } = await import("./doctor.js");
        await doctor(options);
        break;
      }
      case "work-on":
        await githubCommand(args.slice(1));
        break;
      case "cleanup":
        await cleanupCommand(args.slice(1));
        break;
      case "reflect":
        await exploreCommand(args.slice(1));
        break;
      case "exec":
        await execWithOverlay(launchArgs);
        break;
      case "sparkshell":
        await sparkshellCommand(args.slice(1));
        break;
      case "session":
        await sessionCommand(args.slice(1));
        break;
      case "version":
        version();
        break;
      case "hud":
        await hudCommand(args.slice(1));
        break;
      case "hooks":
        await hooksCommand(args.slice(1));
        break;
      case "status":
        await showStatus();
        break;
      case "cancel":
        await cancelModes();
        break;
      case "reasoning":
        await reasoningCommand(args.slice(1));
        break;
      case "help":
      case "--help":
      case "-h":
        console.log(HELP);
        break;
      case "team":
      case "ralph":
      case "research":
      case "autoresearch":
        throw new Error(`Removed command: ${command}`);
      default:
        if (
          firstArg &&
          firstArg.startsWith("-") &&
          !knownCommands.has(firstArg)
        ) {
          await launchWithHud(args);
          break;
        }
        console.error(`Unknown command: ${command}`);
        console.log(HELP);
        process.exit(1);
    }
  } catch (err) {
    console.error(`Error: ${err instanceof Error ? err.message : err}`);
    process.exit(1);
  }
}

async function showStatus(): Promise<void> {
  const { readFile } = await import("fs/promises");
  const cwd = process.cwd();
  try {
    const refs = await listModeStateFilesWithScopePreference(cwd);
    const states = refs.map((ref) => ref.path);
    if (states.length === 0) {
      console.log("No active modes.");
      return;
    }
    for (const path of states) {
      const content = await readFile(path, "utf-8");
      let state: Record<string, unknown>;
      try {
        state = JSON.parse(content) as Record<string, unknown>;
      } catch (err) {
        process.stderr.write(`[cli/index] operation failed: ${err}\n`);
        continue;
      }
      const file = basename(path);
      const mode = file.replace("-state.json", "");
      console.log(
        `${mode}: ${state.active === true ? "ACTIVE" : "inactive"} (phase: ${String(state.current_phase || "n/a")})`,
      );
    }
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    console.log("No active modes.");
  }
}

async function reasoningCommand(args: string[]): Promise<void> {
  const mode = args[0];
  const configPath = codexConfigPath();

  if (!mode) {
    if (!existsSync(configPath)) {
      console.log(
        `model_reasoning_effort is not set (${configPath} does not exist).`,
      );
      console.log(REASONING_USAGE);
      return;
    }

    const { readFile } = await import("fs/promises");
    const content = await readFile(configPath, "utf-8");
    const current = readTopLevelTomlString(content, REASONING_KEY);
    if (current) {
      console.log(`Current ${REASONING_KEY}: ${current}`);
      return;
    }

    console.log(`${REASONING_KEY} is not set in ${configPath}.`);
    console.log(REASONING_USAGE);
    return;
  }

  if (!REASONING_MODE_SET.has(mode)) {
    throw new Error(
      `Invalid reasoning mode "${mode}". Expected one of: ${REASONING_MODES.join(", ")}.\n${REASONING_USAGE}`,
    );
  }

  const { mkdir, readFile, writeFile } = await import("fs/promises");
  await mkdir(dirname(configPath), { recursive: true });

  const existing = existsSync(configPath)
    ? await readFile(configPath, "utf-8")
    : "";
  const updated = upsertTopLevelTomlString(existing, REASONING_KEY, mode);
  await writeFile(configPath, updated);
  console.log(`Set ${REASONING_KEY}="${mode}" in ${configPath}`);
}

export async function launchWithHud(args: string[]): Promise<void> {
  if (isNativeWindows()) {
    const { result } = spawnPlatformCommandSync("tmux", ["-V"], {
      encoding: "utf-8",
      stdio: ["pipe", "pipe", "pipe"],
    });
    if (result.error) {
      const errno = result.error as NodeJS.ErrnoException;
      const kind = classifySpawnError(errno);
      if (kind === "missing") {
        console.warn(
          "[nana] warning: tmux was not found on native Windows. Continuing without tmux/HUD.\n" +
            "[nana] To enable tmux-backed features, install psmux:\n" +
            "[nana]   winget install psmux\n" +
            "[nana] See: https://github.com/marlocarlo/psmux",
        );
      } else {
        console.warn(
          `[nana] warning: tmux probe failed on native Windows (${errno.code || errno.message}). Continuing without tmux/HUD.`,
        );
      }
    } else if (result.status !== 0 && !isTmuxAvailable()) {
      const stderr = (result.stderr || "").trim();
      console.warn(
        `[nana] warning: tmux reported an error on native Windows${stderr ? ` (${stderr})` : ""}. Continuing without tmux/HUD.`,
      );
    }
  }

  const launchCwd = process.cwd();
  const parsedWorktree = parseWorktreeMode(args);
  const notifyTempResult = resolveNotifyTempContract(
    parsedWorktree.remainingArgs,
    process.env,
  );
  const codexHomeOverride = resolveCodexHomeForLaunch(launchCwd, process.env);
  const launchPolicy = resolveCodexLaunchPolicy(
    process.env,
    process.platform,
    undefined,
    isNativeWindows(),
  );
  const enableNotifyFallbackAuthority = launchPolicy === "direct";
  const workerSparkModel = resolveWorkerSparkModel(
    notifyTempResult.passthroughArgs,
    codexHomeOverride,
  );
  const normalizedArgs = normalizeCodexLaunchArgs(
    notifyTempResult.passthroughArgs,
  );
  let cwd = launchCwd;
  if (parsedWorktree.mode.enabled) {
    const planned = planWorktreeTarget({
      cwd: launchCwd,
      scope: "launch",
      mode: parsedWorktree.mode,
    });
    const ensured = ensureWorktree(planned);
    if (ensured.enabled) {
      cwd = ensured.worktreePath;
    }
  }
  const sessionId = `nana-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;

  try {
    await maybeCheckAndPromptUpdate(cwd);
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal: update checks must never block launch
  }

  try {
    await maybePromptGithubStar();
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal: star prompt must never block launch
  }

  // ── Phase 0.5: config repair ────────────────────────────────────────────
  // After an nana version upgrade the OLD setup code (still in memory) may
  // have written a config.toml with duplicate [tui] sections.  Codex CLI's
  // TOML parser rejects duplicates, so we repair before spawning the CLI.
  try {
    const repaired = await repairConfigIfNeeded(
      codexConfigPath(),
      getPackageRoot(),
    );
    if (repaired) {
      console.log("[nana] Repaired duplicate [tui] section in config.toml.");
    }
  } catch {
    // Non-fatal: repair failure must not block launch
  }

  // ── Phase 1: preLaunch ──────────────────────────────────────────────────
  try {
    await preLaunch(cwd, sessionId, notifyTempResult.contract, codexHomeOverride, enableNotifyFallbackAuthority);
  } catch (err) {
    // preLaunch errors must NOT prevent Codex from starting
    console.error(
      `[nana] preLaunch warning: ${err instanceof Error ? err.message : err}`,
    );
  }

  // ── Phase 2: run ────────────────────────────────────────────────────────
  try {
    const notifyTempContractRaw = notifyTempResult.contract.active
      ? serializeNotifyTempContract(notifyTempResult.contract)
      : null;
    runCodex(
      cwd,
      normalizedArgs,
      sessionId,
      workerSparkModel,
      codexHomeOverride,
      notifyTempContractRaw,
    );
  } finally {
    // ── Phase 3: postLaunch ─────────────────────────────────────────────
    await postLaunch(cwd, sessionId, codexHomeOverride, enableNotifyFallbackAuthority);
  }
}

export async function execWithOverlay(args: string[]): Promise<void> {
  const launchCwd = process.cwd();
  const parsedWorktree = parseWorktreeMode(args);
  const notifyTempResult = resolveNotifyTempContract(
    parsedWorktree.remainingArgs,
    process.env,
  );
  const codexHomeOverride = resolveCodexHomeForLaunch(launchCwd, process.env);
  const normalizedArgs = normalizeCodexLaunchArgs(
    notifyTempResult.passthroughArgs,
  );
  let cwd = launchCwd;

  if (parsedWorktree.mode.enabled) {
    const planned = planWorktreeTarget({
      cwd: launchCwd,
      scope: "launch",
      mode: parsedWorktree.mode,
    });
    const ensured = ensureWorktree(planned);
    if (ensured.enabled) {
      cwd = ensured.worktreePath;
    }
  }

  const sessionId = `nana-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;

  try {
    await maybeCheckAndPromptUpdate(cwd);
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
  }

  try {
    await maybePromptGithubStar();
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
  }

  try {
    const repaired = await repairConfigIfNeeded(
      codexConfigPath(),
      getPackageRoot(),
    );
    if (repaired) {
      console.log("[nana] Repaired duplicate [tui] section in config.toml.");
    }
  } catch {
    // Non-fatal
  }

  try {
    await preLaunch(cwd, sessionId, notifyTempResult.contract, codexHomeOverride, true);
  } catch (err) {
    console.error(
      `[nana] preLaunch warning: ${err instanceof Error ? err.message : err}`,
    );
  }

  try {
    const notifyTempContractRaw = notifyTempResult.contract.active
      ? serializeNotifyTempContract(notifyTempResult.contract)
      : null;
    const codexArgs = injectModelInstructionsBypassArgs(
      cwd,
      ["exec", ...normalizedArgs],
      process.env,
      sessionModelInstructionsPath(cwd, sessionId),
    );
    const codexEnvBase = codexHomeOverride
      ? { ...process.env, CODEX_HOME: codexHomeOverride }
      : process.env;
    const codexEnv = notifyTempContractRaw
      ? {
          ...codexEnvBase,
          [NANA_NOTIFY_TEMP_CONTRACT_ENV]: notifyTempContractRaw,
        }
      : codexEnvBase;
    runCodexBlocking(cwd, codexArgs, codexEnv);
  } finally {
    await postLaunch(cwd, sessionId, codexHomeOverride, true);
  }
}

export function normalizeCodexLaunchArgs(args: string[]): string[] {
  const parsed = parseWorktreeMode(args);
  const normalized: string[] = [];
  let wantsBypass = false;
  let hasBypass = false;
  let reasoningMode: ReasoningMode | null = null;

  for (const arg of parsed.remainingArgs) {
    if (arg === MADMAX_FLAG) {
      wantsBypass = true;
      continue;
    }

    if (arg === CODEX_BYPASS_FLAG) {
      wantsBypass = true;
      if (!hasBypass) {
        normalized.push(arg);
        hasBypass = true;
      }
      continue;
    }

    if (arg === HIGH_REASONING_FLAG) {
      reasoningMode = "high";
      continue;
    }

    if (arg === XHIGH_REASONING_FLAG) {
      reasoningMode = "xhigh";
      continue;
    }

    if (arg === SPARK_FLAG) {
      // Spark model is injected into worker env only (not the leader). Consume flag.
      continue;
    }

    if (arg === MADMAX_SPARK_FLAG) {
      // Bypass applies to leader; spark model goes to workers only. Consume flag.
      wantsBypass = true;
      continue;
    }

    normalized.push(arg);
  }

  if (wantsBypass && !hasBypass) {
    normalized.push(CODEX_BYPASS_FLAG);
  }

  if (reasoningMode) {
    normalized.push(CONFIG_FLAG, `${REASONING_KEY}="${reasoningMode}"`);
  }

  return normalized;
}

/**
 * Returns the spark model string if --spark or --madmax-spark appears in the
 * raw (pre-normalize) args, or undefined if neither flag is present.
 * Used to route the spark model to team workers without affecting the leader.
 */
export function resolveWorkerSparkModel(
  args: string[],
  codexHomeOverride?: string,
): string | undefined {
  for (const arg of args) {
    if (arg === SPARK_FLAG || arg === MADMAX_SPARK_FLAG) {
      return resolveTeamLowComplexityDefaultModel(codexHomeOverride);
    }
  }
  return undefined;
}

function isModelInstructionsOverride(value: string): boolean {
  return new RegExp(`^${MODEL_INSTRUCTIONS_FILE_KEY}\\s*=`).test(value.trim());
}

function hasModelInstructionsOverride(args: string[]): boolean {
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === CONFIG_FLAG || arg === LONG_CONFIG_FLAG) {
      const maybeValue = args[i + 1];
      if (
        typeof maybeValue === "string" &&
        isModelInstructionsOverride(maybeValue)
      ) {
        return true;
      }
      continue;
    }

    if (arg.startsWith(`${LONG_CONFIG_FLAG}=`)) {
      const inlineValue = arg.slice(`${LONG_CONFIG_FLAG}=`.length);
      if (isModelInstructionsOverride(inlineValue)) return true;
    }
  }
  return false;
}

function shouldBypassDefaultSystemPrompt(env: NodeJS.ProcessEnv): boolean {
  return env[NANA_BYPASS_DEFAULT_SYSTEM_PROMPT_ENV] !== "0";
}

function buildModelInstructionsOverride(
  cwd: string,
  env: NodeJS.ProcessEnv,
  defaultFilePath?: string,
): string {
  const filePath =
    env[NANA_MODEL_INSTRUCTIONS_FILE_ENV] ||
    defaultFilePath ||
    join(cwd, "AGENTS.md");
  return `${MODEL_INSTRUCTIONS_FILE_KEY}="${escapeTomlString(filePath)}"`;
}

function tryReadGitValue(cwd: string, args: string[]): string | undefined {
  try {
    const value = execFileSync("git", args, {
      cwd,
      encoding: "utf-8",
      stdio: ["ignore", "pipe", "ignore"],
      timeout: 2000,
    }).trim();
    return value || undefined;
  } catch {
    return undefined;
  }
}

function extractIssueNumber(text: string): number | undefined {
  const explicit = text.match(/\bissue\s*#(\d+)\b/i);
  if (explicit) return Number.parseInt(explicit[1], 10);
  const generic = text.match(/(^|[^\w/])#(\d+)\b/);
  return generic ? Number.parseInt(generic[2], 10) : undefined;
}

function resolveNativeSessionName(cwd: string, sessionId: string): string {
  if (process.env.TMUX) {
    try {
      const tmuxSession = execFileSync(
        "tmux",
        ["display-message", "-p", "#S"],
        {
          encoding: "utf-8",
          stdio: ["ignore", "pipe", "ignore"],
          timeout: 2000,
        },
      ).trim();
      if (tmuxSession) return tmuxSession;
    } catch {
      // best effort only
    }
  }
  return buildTmuxSessionName(cwd, sessionId);
}

function buildNativeHookBaseContext(
  cwd: string,
  sessionId: string,
  normalizedEvent: "started" | "blocked" | "finished" | "failed",
  extra: Record<string, unknown> = {},
): Record<string, unknown> {
  const repoPath =
    tryReadGitValue(cwd, ["rev-parse", "--show-toplevel"]) || cwd;
  const branch = tryReadGitValue(cwd, ["rev-parse", "--abbrev-ref", "HEAD"]);
  const issueNumber = extractIssueNumber(
    [branch, basename(cwd)].filter(Boolean).join(" "),
  );

  return {
    normalized_event: normalizedEvent,
    session_name: resolveNativeSessionName(cwd, sessionId),
    repo_path: repoPath,
    repo_name: basename(repoPath),
    worktree_path: cwd,
    ...(branch ? { branch } : {}),
    ...(issueNumber !== undefined ? { issue_number: issueNumber } : {}),
    ...extra,
  };
}

export function injectModelInstructionsBypassArgs(
  cwd: string,
  args: string[],
  env: NodeJS.ProcessEnv = process.env,
  defaultFilePath?: string,
): string[] {
  if (!shouldBypassDefaultSystemPrompt(env)) return [...args];
  if (hasModelInstructionsOverride(args)) return [...args];
  return [
    ...args,
    CONFIG_FLAG,
    buildModelInstructionsOverride(cwd, env, defaultFilePath),
  ];
}

export function collectInheritableTeamWorkerArgs(
  codexArgs: string[],
): string[] {
  return collectInheritableTeamWorkerArgsShared(codexArgs);
}

export function resolveTeamWorkerLaunchArgsEnv(
  existingRaw: string | undefined,
  codexArgs: string[],
  inheritLeaderFlags = true,
  defaultModel?: string,
): string | null {
  const inheritedArgs = inheritLeaderFlags
    ? collectInheritableTeamWorkerArgs(codexArgs)
    : [];
  const normalized = resolveTeamWorkerLaunchArgs({
    existingRaw,
    inheritedArgs,
    fallbackModel: defaultModel,
  });
  if (normalized.length === 0) return null;
  return normalized.join(" ");
}

export function readTopLevelTomlString(
  content: string,
  key: string,
): string | null {
  let inTopLevel = true;
  const lines = content.split(/\r?\n/);
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    if (/^\[[^[\]]+\]\s*(#.*)?$/.test(trimmed)) {
      inTopLevel = false;
      continue;
    }
    if (!inTopLevel) continue;
    const match = line.match(/^\s*([A-Za-z0-9_.-]+)\s*=\s*(.*?)\s*(?:#.*)?$/);
    if (!match || match[1] !== key) continue;
    return parseTomlStringValue(match[2]);
  }
  return null;
}

export function upsertTopLevelTomlString(
  content: string,
  key: string,
  value: string,
): string {
  const eol = content.includes("\r\n") ? "\r\n" : "\n";
  const assignment = `${key} = "${escapeTomlString(value)}"`;

  if (!content.trim()) {
    return assignment + eol;
  }

  const lines = content.split(/\r?\n/);
  let replaced = false;
  let inTopLevel = true;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    if (/^\[[^[\]]+\]\s*(#.*)?$/.test(trimmed)) {
      inTopLevel = false;
      continue;
    }
    if (!inTopLevel) continue;
    const match = line.match(/^\s*([A-Za-z0-9_.-]+)\s*=/);
    if (match && match[1] === key) {
      lines[i] = assignment;
      replaced = true;
      break;
    }
  }

  if (!replaced) {
    const firstTableIndex = lines.findIndex((line) =>
      /^\s*\[[^[\]]+\]\s*(#.*)?$/.test(line.trim()),
    );
    if (firstTableIndex >= 0) {
      lines.splice(firstTableIndex, 0, assignment);
    } else {
      lines.push(assignment);
    }
  }

  let out = lines.join(eol);
  if (!out.endsWith(eol)) out += eol;
  return out;
}

function parseTomlStringValue(value: string): string {
  const trimmed = value.trim();
  if (trimmed.startsWith('"') && trimmed.endsWith('"') && trimmed.length >= 2) {
    return trimmed.slice(1, -1);
  }
  if (trimmed.startsWith("'") && trimmed.endsWith("'") && trimmed.length >= 2) {
    return trimmed.slice(1, -1);
  }
  return trimmed;
}

function escapeTomlString(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

function sanitizeTmuxToken(value: string): string {
  const cleaned = value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return cleaned || "unknown";
}

export function buildTmuxSessionName(cwd: string, sessionId: string): string {
  const parentPath = dirname(cwd);
  const parentDir = basename(parentPath);
  const dirName = basename(cwd);
  const grandparentPath = dirname(parentPath);
  const grandparentDir = basename(grandparentPath);
  const repoDir = parentDir.endsWith(".nana-worktrees")
    ? parentDir.slice(0, -".nana-worktrees".length)
    : parentDir === "worktrees" && grandparentDir === ".nana"
      ? basename(dirname(grandparentPath))
      : null;
  const dirToken = repoDir
    ? sanitizeTmuxToken(`${repoDir}-${dirName}`)
    : sanitizeTmuxToken(dirName);
  let branchToken = "detached";
  const branch = tryReadGitValue(cwd, ["rev-parse", "--abbrev-ref", "HEAD"]);
  if (branch) branchToken = sanitizeTmuxToken(branch);
  const sessionToken = sanitizeTmuxToken(sessionId.replace(/^nana-/, ""));
  const name = `nana-${dirToken}-${branchToken}-${sessionToken}`;
  return name.length > 120 ? name.slice(0, 120) : name;
}

function parsePaneIdFromTmuxOutput(rawOutput: string): string | null {
  const paneId = rawOutput.split("\n")[0]?.trim() || "";
  return paneId.startsWith("%") ? paneId : null;
}

function parseWindowIndexFromTmuxOutput(rawOutput: string): string | null {
  const windowIndex = rawOutput.split("\n")[0]?.trim() || "";
  return /^[0-9]+$/.test(windowIndex) ? windowIndex : null;
}

function detectDetachedSessionWindowIndex(sessionName: string): string | null {
  try {
    const output = execFileSync(
      "tmux",
      ["display-message", "-p", "-t", sessionName, "#{window_index}"],
      { encoding: "utf-8" },
    );
    return parseWindowIndexFromTmuxOutput(output);
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    return null;
  }
}

function escapeShellDoubleQuotedValue(value: string): string {
  return value.replace(/["\\$`]/g, "\\$&");
}

function buildDetachedSessionLeaderCommand(
  sessionName: string,
  codexCmd: string,
  shellPath: string | undefined = process.env.SHELL,
): string {
  const cleanupTrap = [
    "status=$?;",
    "trap - 0 INT TERM HUP;",
    `tmux kill-session -t "${escapeShellDoubleQuotedValue(sessionName)}" >/dev/null 2>&1 || true;`,
    "exit $status;",
  ].join(" ");
  const wrapped = [`trap '${cleanupTrap}' 0 INT TERM HUP;`, codexCmd].join(" ");
  const rawShell =
    shellPath && shellPath.trim() !== "" ? shellPath.trim() : "/bin/sh";
  const shellBin =
    /\/(zsh|bash|sh|dash)$/i.test(rawShell) && ALLOWED_SHELLS.has(rawShell)
      ? rawShell
      : existsSync("/bin/bash")
        ? "/bin/bash"
        : "/bin/sh";
  return `${quoteShellArg(shellBin)} -c ${quoteShellArg(wrapped)}`;
}

export function buildDetachedSessionBootstrapSteps(
  sessionName: string,
  cwd: string,
  codexCmd: string,
  hudCmd: string,
  workerLaunchArgs: string | null,
  codexHomeOverride?: string,
  notifyTempContractRaw?: string | null,
  nativeWindows = false,
  sessionId?: string,
): DetachedSessionTmuxStep[] {
  const detachedLeaderCmd = nativeWindows
    ? "powershell.exe"
    : buildDetachedSessionLeaderCommand(sessionName, codexCmd);
  const newSessionArgs: string[] = [
    "new-session",
    "-d",
    "-P",
    "-F",
    "#{pane_id}",
    "-s",
    sessionName,
    "-c",
    cwd,
    ...(workerLaunchArgs
      ? ["-e", `${TEAM_WORKER_LAUNCH_ARGS_ENV}=${workerLaunchArgs}`]
      : []),
    ...(sessionId ? ["-e", `NANA_SESSION_ID=${sessionId}`] : []),
    ...(codexHomeOverride ? ["-e", `CODEX_HOME=${codexHomeOverride}`] : []),
    ...(notifyTempContractRaw
      ? ["-e", `${NANA_NOTIFY_TEMP_CONTRACT_ENV}=${notifyTempContractRaw}`]
      : []),
    detachedLeaderCmd,
  ];
  const splitCaptureArgs: string[] = [
    "split-window",
    "-v",
    "-l",
    String(HUD_TMUX_HEIGHT_LINES),
    "-d",
    "-t",
    sessionName,
    "-c",
    cwd,
    "-P",
    "-F",
    "#{pane_id}",
    hudCmd,
  ];
  return [
    { name: "new-session", args: newSessionArgs },
    { name: "split-and-capture-hud-pane", args: splitCaptureArgs },
  ];
}

async function readLaunchAppendInstructions(): Promise<string> {
  const appendixCandidates = [
    process.env[NANA_RALPH_APPEND_INSTRUCTIONS_FILE_ENV]?.trim(),
    process.env[NANA_AUTORESEARCH_APPEND_INSTRUCTIONS_FILE_ENV]?.trim(),
    process.env[NANA_GITHUB_APPEND_INSTRUCTIONS_FILE_ENV]?.trim(),
  ].filter(
    (value): value is string => typeof value === "string" && value.length > 0,
  );
  if (appendixCandidates.length === 0) return "";
  const appendixPath = appendixCandidates[0];
  if (!existsSync(appendixPath)) {
    throw new Error(`launch instructions file not found: ${appendixPath}`);
  }
  const { readFile } = await import("fs/promises");
  return (await readFile(appendixPath, "utf-8")).trim();
}

export function buildDetachedSessionFinalizeSteps(
  sessionName: string,
  hudPaneId: string | null,
  hookWindowIndex: string | null,
  enableMouse: boolean,
  nativeWindows = false,
): DetachedSessionTmuxStep[] {
  const steps: DetachedSessionTmuxStep[] = [];
  if (!nativeWindows && hudPaneId && hookWindowIndex) {
    const hookTarget = buildResizeHookTarget(sessionName, hookWindowIndex);
    const hookName = buildResizeHookName(
      "launch",
      sessionName,
      hookWindowIndex,
      hudPaneId,
    );
    const clientAttachedHookName = buildClientAttachedReconcileHookName(
      "launch",
      sessionName,
      hookWindowIndex,
      hudPaneId,
    );
    steps.push({
      name: "register-resize-hook",
      args: buildRegisterResizeHookArgs(
        hookTarget,
        hookName,
        hudPaneId,
        HUD_TMUX_HEIGHT_LINES,
      ),
    });
    steps.push({
      name: "register-client-attached-reconcile",
      args: buildRegisterClientAttachedReconcileArgs(
        hookTarget,
        clientAttachedHookName,
        hudPaneId,
        HUD_TMUX_HEIGHT_LINES,
      ),
    });
    steps.push({
      name: "schedule-delayed-resize",
      args: buildScheduleDelayedHudResizeArgs(
        hudPaneId,
        undefined,
        HUD_TMUX_HEIGHT_LINES,
      ),
    });
    steps.push({
      name: "reconcile-hud-resize",
      args: buildReconcileHudResizeArgs(hudPaneId, HUD_TMUX_HEIGHT_LINES),
    });
  }

  if (enableMouse) {
    steps.push({
      name: "set-mouse",
      args: ["set-option", "-t", sessionName, "mouse", "on"],
    });
  }
  steps.push({
    name: "attach-session",
    args: ["attach-session", "-t", sessionName],
  });
  return steps;
}

export function buildDetachedSessionRollbackSteps(
  sessionName: string,
  hookTarget: string | null,
  hookName: string | null,
  clientAttachedHookName: string | null,
): DetachedSessionTmuxStep[] {
  const steps: DetachedSessionTmuxStep[] = [];
  if (hookTarget && clientAttachedHookName) {
    steps.push({
      name: "unregister-client-attached-reconcile",
      args: buildUnregisterClientAttachedReconcileArgs(
        hookTarget,
        clientAttachedHookName,
      ),
    });
  }
  if (hookTarget && hookName) {
    steps.push({
      name: "unregister-resize-hook",
      args: buildUnregisterResizeHookArgs(hookTarget, hookName),
    });
  }
  steps.push({
    name: "kill-session",
    args: ["kill-session", "-t", sessionName],
  });
  return steps;
}

export function buildNotifyTempStartupMessages(
  contract: NotifyTempContract,
  hasValidProviders: boolean,
): { infoLines: string[]; warningLines: string[] } {
  const providers =
    contract.canonicalSelectors.length > 0
      ? contract.canonicalSelectors.join(",")
      : "none";
  const infoLines = [
    `notify temp: active | providers=${providers} | persistent-routing=bypassed`,
  ];
  const warningLines = [...contract.warnings];
  if (!hasValidProviders) {
    warningLines.push(
      "notify temp: no valid providers resolved; notifications skipped",
    );
  }
  return { infoLines, warningLines };
}

export function buildNotifyFallbackWatcherEnv(
  env: NodeJS.ProcessEnv = process.env,
  options: {
    codexHomeOverride?: string;
    enableAuthority?: boolean;
    sessionId?: string;
  } = {},
): NodeJS.ProcessEnv {
  const nextEnv = { ...env };
  delete nextEnv.TMUX;
  delete nextEnv.TMUX_PANE;
  return {
    ...nextEnv,
    ...(options.codexHomeOverride ? { CODEX_HOME: options.codexHomeOverride } : {}),
    ...(options.sessionId ? { NANA_SESSION_ID: options.sessionId } : {}),
    NANA_HUD_AUTHORITY: options.enableAuthority ? "1" : "0",
  };
}

/**
 * preLaunch: Prepare environment before Codex starts.
 * 1. Generate runtime overlay + write session-scoped model instructions file
 * 2. Write session.json
 *
 * Automatic stale-session cleanup is intentionally disabled here. Destructive
 * cleanup must be explicit via `nana cleanup` so normal launches never reap
 * files or processes from other NANA sessions.
 */
async function preLaunch(
  cwd: string,
  sessionId: string,
  notifyTempContract?: NotifyTempContract,
  codexHomeOverride?: string,
  enableNotifyFallbackAuthority: boolean = false,
): Promise<void> {
  // 1. Generate runtime overlay + write session-scoped model instructions file
  const orchestrationMode = await resolveSessionOrchestrationMode(
    cwd,
    sessionId,
  );
  const overlay = await generateOverlay(cwd, sessionId, { orchestrationMode });
  const launchAppendix = await readLaunchAppendInstructions();
  const sessionInstructions =
    launchAppendix.trim().length > 0
      ? `${overlay}

${launchAppendix}`
      : overlay;
  await writeSessionModelInstructionsFile(cwd, sessionId, sessionInstructions);

  // 2. Write session state
  await resetSessionMetrics(cwd);
  await writeSessionStart(cwd, sessionId);

  // 3. Start notify fallback watcher (best effort)
  try {
    await startNotifyFallbackWatcher(cwd, { codexHomeOverride, enableAuthority: enableNotifyFallbackAuthority, sessionId });
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }

  // 4. Start derived watcher (best effort, opt-in)
  try {
    await startHookDerivedWatcher(cwd);
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }

  // 5. Emit temp notification startup summary + warnings, then send session-start lifecycle notification (best effort)
  try {
    if (notifyTempContract?.active) {
      process.env[NANA_NOTIFY_TEMP_CONTRACT_ENV] =
        serializeNotifyTempContract(notifyTempContract);
      const { getNotificationConfig } =
        await import("../notifications/config.js");
      const resolved = getNotificationConfig();
      const startup = buildNotifyTempStartupMessages(
        notifyTempContract,
        Boolean(resolved?.enabled),
      );
      for (const info of startup.infoLines) {
        console.log(`[nana] ${info}`);
      }
      for (const warning of startup.warningLines) {
        console.warn(`[nana] ${warning}`);
      }
    } else {
      delete process.env[NANA_NOTIFY_TEMP_CONTRACT_ENV];
    }
    const { notifyLifecycle } = await import("../notifications/index.js");
    await notifyLifecycle("session-start", {
      sessionId,
      projectPath: cwd,
      projectName: basename(cwd),
    });
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal: notification failures must never block launch
  }

  // 6. Dispatch native hook event (best effort)
  try {
    await emitNativeHookEvent(cwd, "session-start", {
      session_id: sessionId,
      context: buildNativeHookBaseContext(cwd, sessionId, "started", {
        project_path: cwd,
        project_name: basename(cwd),
        status: "started",
      }),
    });
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }
}

/**
 * runCodex: Launch Codex CLI (blocks until exit).
 * All 3 paths (new tmux, existing tmux, no tmux) block via execSync/execFileSync.
 */
function runCodex(
  cwd: string,
  args: string[],
  sessionId: string,
  workerDefaultModel?: string,
  codexHomeOverride?: string,
  notifyTempContractRaw?: string | null,
): void {
  const launchArgs = injectModelInstructionsBypassArgs(
    cwd,
    args,
    process.env,
    sessionModelInstructionsPath(cwd, sessionId),
  );
  const nativeWindows = isNativeWindows();
  const nanaBin = process.argv[1];
  const hudCmd = nativeWindows
    ? buildWindowsPromptCommand("node", [nanaBin, "hud", "--watch"])
    : buildTmuxPaneCommand("node", [nanaBin, "hud", "--watch"]);
  const inheritLeaderFlags = process.env[TEAM_INHERIT_LEADER_FLAGS_ENV] !== "0";
  const workerLaunchArgs = resolveTeamWorkerLaunchArgsEnv(
    process.env[TEAM_WORKER_LAUNCH_ARGS_ENV],
    launchArgs,
    inheritLeaderFlags,
    workerDefaultModel,
  );
  const codexBaseEnv = codexHomeOverride
    ? { ...process.env, CODEX_HOME: codexHomeOverride }
    : process.env;
  const codexEnvWithSession = { ...codexBaseEnv, NANA_SESSION_ID: sessionId };
  const codexEnv = workerLaunchArgs
    ? { ...codexEnvWithSession, [TEAM_WORKER_LAUNCH_ARGS_ENV]: workerLaunchArgs }
    : codexEnvWithSession;
  const codexEnvWithNotify = notifyTempContractRaw
    ? { ...codexEnv, [NANA_NOTIFY_TEMP_CONTRACT_ENV]: notifyTempContractRaw }
    : codexEnv;

  const launchPolicy = resolveCodexLaunchPolicy(
    process.env,
    process.platform,
    undefined,
    nativeWindows,
  );

  if (isCodexVersionRequest(launchArgs)) {
    runCodexBlocking(cwd, launchArgs, codexEnvWithNotify);
    return;
  }

  if (launchPolicy === "inside-tmux") {
    // Already in tmux: launch codex in current pane, HUD in bottom split
    const currentPaneId = process.env.TMUX_PANE;
    const staleHudPaneIds = listHudWatchPaneIdsInCurrentWindow(currentPaneId);
    for (const paneId of staleHudPaneIds) {
      killTmuxPane(paneId);
    }

    let hudPaneId: string | null = null;
    try {
      hudPaneId = createHudWatchPane(cwd, hudCmd);
    } catch (err) {
      process.stderr.write(`[cli/index] operation failed: ${err}\n`);
      // HUD split failed, continue without it
    }

    // Enable mouse scrolling at session start so scroll works before team
    // expansion. Previously this was only called from createTeamSession().
    // Opt-out: set NANA_MOUSE=0. (closes #128)
    if (process.env.NANA_MOUSE !== "0") {
      try {
        const tmuxPaneTarget = process.env.TMUX_PANE;
        const displayArgs = tmuxPaneTarget
          ? ["display-message", "-p", "-t", tmuxPaneTarget, "#S"]
          : ["display-message", "-p", "#S"];
        const tmuxSession = execFileSync("tmux", displayArgs, {
          encoding: "utf-8",
        }).trim();
        if (tmuxSession) enableMouseScrolling(tmuxSession);
      } catch (err) {
        process.stderr.write(`[cli/index] operation failed: ${err}\n`);
        // Non-fatal: mouse scrolling is a convenience feature
      }
    }

    const activePaneId = process.env.TMUX_PANE?.trim();
    if (activePaneId) {
      try {
        execFileSync("tmux", ["display-message", "-p", "-t", activePaneId, "#S"], {
          encoding: "utf-8",
        });
      } catch {}
    }

    try {
      runCodexBlocking(cwd, launchArgs, codexEnvWithNotify);
    } finally {
      const cleanupPaneIds = buildHudPaneCleanupTargets(
        listHudWatchPaneIdsInCurrentWindow(currentPaneId),
        hudPaneId,
        currentPaneId,
      );
      for (const paneId of cleanupPaneIds) {
        killTmuxPane(paneId);
      }
    }
  } else if (launchPolicy === "direct") {
    // Detached HUD sessions require tmux. Skip the bootstrap entirely when the
    // binary is unavailable so direct launches do not emit noisy ENOENT logs.
    runCodexBlocking(cwd, launchArgs, codexEnvWithNotify);
  } else {
    // Not in tmux: create a new tmux session with codex + HUD pane
    const codexCmd = buildTmuxPaneCommand("codex", launchArgs);
    const detachedWindowsCodexCmd = nativeWindows
      ? buildWindowsPromptCommand("codex", launchArgs)
      : null;
    const tmuxSessionId = `nana-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const sessionName = buildTmuxSessionName(cwd, tmuxSessionId);
    let createdDetachedSession = false;
    let registeredHookTarget: string | null = null;
    let registeredHookName: string | null = null;
    let registeredClientAttachedHookName: string | null = null;
    try {
      const bootstrapSteps = buildDetachedSessionBootstrapSteps(
        sessionName,
        cwd,
        codexCmd,
        hudCmd,
        workerLaunchArgs,
        codexHomeOverride,
        notifyTempContractRaw,
        nativeWindows,
        sessionId,
      );
      for (const step of bootstrapSteps) {
        const output = execFileSync("tmux", step.args, {
          stdio: "pipe",
          encoding: "utf-8",
        });
        if (step.name === "new-session") {
          createdDetachedSession = true;
          parsePaneIdFromTmuxOutput(output || "");
        }
        if (step.name === "split-and-capture-hud-pane") {
          const hudPaneId = parsePaneIdFromTmuxOutput(output || "");
          const hookWindowIndex = hudPaneId
            ? detectDetachedSessionWindowIndex(sessionName)
            : null;
          const hookTarget =
            hudPaneId && hookWindowIndex
              ? buildResizeHookTarget(sessionName, hookWindowIndex)
              : null;
          const hookName =
            hudPaneId && hookWindowIndex
              ? buildResizeHookName(
                  "launch",
                  sessionName,
                  hookWindowIndex,
                  hudPaneId,
                )
              : null;
          const clientAttachedHookName =
            hudPaneId && hookWindowIndex
              ? buildClientAttachedReconcileHookName(
                  "launch",
                  sessionName,
                  hookWindowIndex,
                  hudPaneId,
                )
              : null;
          const finalizeSteps = buildDetachedSessionFinalizeSteps(
            sessionName,
            hudPaneId,
            hookWindowIndex,
            process.env.NANA_MOUSE !== "0",
            nativeWindows,
          );
          if (nativeWindows && detachedWindowsCodexCmd) {
            scheduleDetachedWindowsCodexLaunch(
              sessionName,
              detachedWindowsCodexCmd,
            );
          }
          for (const finalizeStep of finalizeSteps) {
            const stdio =
              finalizeStep.name === "attach-session" ? "inherit" : "ignore";
            try {
              execFileSync("tmux", finalizeStep.args, { stdio });
            } catch (err) {
              process.stderr.write(`[cli/index] operation failed: ${err}\n`);
              if (finalizeStep.name === "attach-session")
                throw new Error("failed to attach detached tmux session");
              continue;
            }
            if (
              finalizeStep.name === "register-resize-hook" &&
              hookTarget &&
              hookName
            ) {
              registeredHookTarget = hookTarget;
              registeredHookName = hookName;
            }
            if (
              finalizeStep.name === "register-client-attached-reconcile" &&
              clientAttachedHookName
            ) {
              registeredClientAttachedHookName = clientAttachedHookName;
            }
          }
        }
      }
    } catch (err) {
      process.stderr.write(`[cli/index] operation failed: ${err}\n`);
      if (createdDetachedSession) {
        const rollbackSteps = buildDetachedSessionRollbackSteps(
          sessionName,
          registeredHookTarget,
          registeredHookName,
          registeredClientAttachedHookName,
        );
        for (const rollbackStep of rollbackSteps) {
          try {
            execFileSync("tmux", rollbackStep.args, { stdio: "ignore" });
          } catch (err) {
            process.stderr.write(`[cli/index] operation failed: ${err}\n`);
            // best-effort rollback only
          }
        }
      }
      // tmux not available or failed, just run codex directly
      runCodexBlocking(cwd, launchArgs, codexEnvWithNotify);
    }
  }
}

function listHudWatchPaneIdsInCurrentWindow(currentPaneId?: string): string[] {
  try {
    const output = execFileSync(
      "tmux",
      [
        "list-panes",
        "-F",
        "#{pane_id}\t#{pane_current_command}\t#{pane_start_command}",
      ],
      { encoding: "utf-8" },
    );
    return findHudWatchPaneIds(parseTmuxPaneSnapshot(output), currentPaneId);
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    return [];
  }
}

function createHudWatchPane(cwd: string, hudCmd: string): string | null {
  const output = execFileSync(
    "tmux",
    [
      "split-window",
      "-v",
      "-l",
      String(HUD_TMUX_HEIGHT_LINES),
      "-d",
      "-c",
      cwd,
      "-P",
      "-F",
      "#{pane_id}",
      hudCmd,
    ],
    { encoding: "utf-8" },
  );
  return parsePaneIdFromTmuxOutput(output);
}

function killTmuxPane(paneId: string): void {
  if (!paneId.startsWith("%")) return;
  try {
    execFileSync("tmux", ["kill-pane", "-t", paneId], { stdio: "ignore" });
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Pane may already be gone; ignore.
  }
}

export function buildTmuxShellCommand(command: string, args: string[]): string {
  return [quoteShellArg(command), ...args.map(quoteShellArg)].join(" ");
}

function encodePowerShellCommand(commandText: string): string {
  return Buffer.from(commandText, "utf16le").toString("base64");
}

function isCodexVersionRequest(args: string[]): boolean {
  return args.some((arg) => CODEX_VERSION_FLAGS.has(arg));
}

export function buildWindowsPromptCommand(
  command: string,
  args: string[],
): string {
  const invocation = [
    "&",
    quotePowerShellArg(command),
    ...args.map(quotePowerShellArg),
  ].join(" ");
  const wrappedCommand = [
    "$ErrorActionPreference = 'Stop'",
    `& { ${invocation} }`,
  ].join("; ");
  return `powershell.exe -NoLogo -NoExit -EncodedCommand ${encodePowerShellCommand(wrappedCommand)}`;
}

/**
 * Wrap a command for tmux pane execution so the user's shell profile is
 * sourced.  Without this, tmux runs `default-shell -c "cmd"` which is
 * non-interactive/non-login and skips .zshrc / .bashrc.
 */
export function buildTmuxPaneCommand(
  command: string,
  args: string[],
  shellPath: string | undefined = process.env.SHELL,
): string {
  const bareCmd = buildTmuxShellCommand(command, args);
  let rcSource = "";
  if (shellPath && /\/zsh$/i.test(shellPath)) {
    rcSource = "if [ -f ~/.zshrc ]; then source ~/.zshrc; fi; ";
  } else if (shellPath && /\/bash$/i.test(shellPath)) {
    rcSource = "if [ -f ~/.bashrc ]; then source ~/.bashrc; fi; ";
  }
  const rawShell =
    shellPath && shellPath.trim() !== "" ? shellPath.trim() : "/bin/sh";
  const shellBin = ALLOWED_SHELLS.has(rawShell) ? rawShell : "/bin/sh";
  const inner = `${rcSource}exec ${bareCmd}`;
  return `${quoteShellArg(shellBin)} -lc ${quoteShellArg(inner)}`;
}

function quoteShellArg(value: string): string {
  return `'${value.replace(/'/g, `'\"'\"'`)}'`;
}

function quotePowerShellArg(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

function buildDetachedWindowsBootstrapScript(
  sessionName: string,
  commandText: string,
  delayMs: number = WINDOWS_DETACHED_BOOTSTRAP_DELAY_MS,
): string {
  const delay =
    Number.isFinite(delayMs) && delayMs > 0
      ? Math.floor(delayMs)
      : WINDOWS_DETACHED_BOOTSTRAP_DELAY_MS;
  const targetLiteral = JSON.stringify(`${sessionName}:0.0`);
  const commandLiteral = JSON.stringify(commandText);

  return [
    "const { execFileSync } = require('child_process');",
    `setTimeout(() => {`,
    `try { execFileSync('tmux', ['send-keys', '-t', ${targetLiteral}, '-l', '--', ${commandLiteral}], { stdio: 'ignore' }); } catch {}`,
    `try { execFileSync('tmux', ['send-keys', '-t', ${targetLiteral}, 'C-m'], { stdio: 'ignore' }); } catch {}`,
    `}, ${delay});`,
  ].join("");
}

function scheduleDetachedWindowsCodexLaunch(
  sessionName: string,
  commandText: string,
): void {
  const child = spawn(
    process.execPath,
    ["-e", buildDetachedWindowsBootstrapScript(sessionName, commandText)],
    {
      detached: true,
      stdio: "ignore",
      windowsHide: true,
    },
  );
  child.unref();
}

/**
 * postLaunch: Clean up after Codex exits.
 * Each step is independently fault-tolerant (try/catch per step).
 */
async function postLaunch(
  cwd: string,
  sessionId: string,
  codexHomeOverride?: string,
  enableNotifyFallbackAuthority: boolean = false,
): Promise<void> {
  // Capture session start time before cleanup (writeSessionEnd deletes session.json)
  let sessionStartedAt: string | undefined;
  try {
    const sessionState = await readSessionState(cwd);
    sessionStartedAt = sessionState?.started_at;
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }

  // 0. Flush fallback watcher once to reduce race with fast codex exit.
  try {
    await flushNotifyFallbackOnce(cwd, { codexHomeOverride, enableAuthority: enableNotifyFallbackAuthority, sessionId });
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }

  // 0. Stop notify fallback watcher first.
  try {
    await stopNotifyFallbackWatcher(cwd);
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }

  // 0. Flush derived watcher once on shutdown (opt-in, best effort).
  try {
    await flushHookDerivedWatcherOnce(cwd);
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }

  // 0.1 Stop derived watcher first (opt-in, best effort).
  try {
    await stopHookDerivedWatcher(cwd);
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }

  // 1. Remove session-scoped model instructions file
  try {
    await removeSessionModelInstructionsFile(cwd, sessionId);
  } catch (err) {
    console.error(
      `[nana] postLaunch: model instructions cleanup failed: ${err instanceof Error ? err.message : err}`,
    );
  }

  // 2. Archive session (write history, delete session.json)
  try {
    await writeSessionEnd(cwd, sessionId);
  } catch (err) {
    console.error(
      `[nana] postLaunch: session archive failed: ${err instanceof Error ? err.message : err}`,
    );
  }

  // 3. Cancel any still-active modes
  try {
    const { readdir, writeFile, readFile } = await import("fs/promises");
    const scopedDirs = [getBaseStateDir(cwd), getStateDir(cwd, sessionId)];
    for (const stateDir of scopedDirs) {
      const files = await readdir(stateDir).catch(() => [] as string[]);
      for (const file of files) {
        if (!file.endsWith("-state.json") || file === "session.json") continue;
        const path = join(stateDir, file);
        const content = await readFile(path, "utf-8");
        const state = JSON.parse(content);
        if (state.active) {
          state.active = false;
          state.completed_at = new Date().toISOString();
          await writeFile(path, JSON.stringify(state, null, 2));
        }
      }
    }
  } catch (err) {
    console.error(
      `[nana] postLaunch: mode cleanup failed: ${err instanceof Error ? err.message : err}`,
    );
  }

  // 4. Send session-end lifecycle notification (best effort)
  try {
    const { notifyLifecycle } = await import("../notifications/index.js");
    const durationMs = sessionStartedAt
      ? Date.now() - new Date(sessionStartedAt).getTime()
      : undefined;
    await notifyLifecycle("session-end", {
      sessionId,
      projectPath: cwd,
      projectName: basename(cwd),
      durationMs,
      reason: "session_exit",
    });
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal: notification failures must never block session cleanup
  }

  // 5. Dispatch native hook event (best effort)
  try {
    const durationMs = sessionStartedAt
      ? Date.now() - new Date(sessionStartedAt).getTime()
      : undefined;
    const normalizedEvent =
      process.exitCode && process.exitCode !== 0 ? "failed" : "finished";
    const errorSummary =
      normalizedEvent === "failed"
        ? `codex exited with code ${process.exitCode}`
        : undefined;
    await emitNativeHookEvent(cwd, "session-end", {
      session_id: sessionId,
      context: buildNativeHookBaseContext(cwd, sessionId, normalizedEvent, {
        project_path: cwd,
        project_name: basename(cwd),
        duration_ms: durationMs,
        reason: "session_exit",
        status: normalizedEvent === "failed" ? "failed" : "finished",
        ...(process.exitCode !== undefined
          ? { exit_code: process.exitCode }
          : {}),
        ...(errorSummary ? { error_summary: errorSummary } : {}),
      }),
    });
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    // Non-fatal
  }
}

async function emitNativeHookEvent(
  cwd: string,
  event: "session-start" | "session-end" | "session-idle" | "turn-complete",
  opts: {
    session_id?: string;
    thread_id?: string;
    turn_id?: string;
    mode?: string;
    context?: Record<string, unknown>;
  } = {},
): Promise<void> {
  const payload = buildHookEvent(event, {
    source: "native",
    context: opts.context || {},
    session_id: opts.session_id,
    thread_id: opts.thread_id,
    turn_id: opts.turn_id,
    mode: opts.mode,
  });
  await dispatchHookEvent(payload, {
    cwd,
    enabled: true,
  });
}

function notifyFallbackPidPath(cwd: string): string {
  return join(cwd, ".nana", "state", "notify-fallback.pid");
}

function hookDerivedWatcherPidPath(cwd: string): string {
  return join(cwd, ".nana", "state", "hook-derived-watcher.pid");
}

function parseWatcherPidFile(content: string): number | null {
  const trimmed = content.trim();
  if (!trimmed) return null;
  try {
    const parsed = JSON.parse(trimmed) as { pid?: unknown };
    return typeof parsed.pid === "number" &&
      Number.isFinite(parsed.pid) &&
      parsed.pid > 0
      ? parsed.pid
      : null;
  } catch {
    const pid = Number.parseInt(trimmed, 10);
    return Number.isFinite(pid) && pid > 0 ? pid : null;
  }
}

function tryKillPid(pid: number, signal: NodeJS.Signals = "SIGTERM"): boolean {
  try {
    process.kill(pid, signal);
    return true;
  } catch (error: unknown) {
    const code = (error as NodeJS.ErrnoException).code;
    if (code === "ESRCH") return false;
    throw error;
  }
}

async function startNotifyFallbackWatcher(
  cwd: string,
  options: { codexHomeOverride?: string; enableAuthority?: boolean; sessionId?: string } = {},
): Promise<void> {
  if (process.env.NANA_NOTIFY_FALLBACK === "0") return;

  const { mkdir, writeFile, readFile } = await import("fs/promises");
  const pidPath = notifyFallbackPidPath(cwd);
  const pkgRoot = getPackageRoot();
  const watcherScript = resolveNotifyFallbackWatcherScript(pkgRoot);
  const notifyScript = resolveNotifyHookScript(pkgRoot);
  if (!existsSync(watcherScript) || !existsSync(notifyScript)) return;

  // Stop stale watcher from a previous run.
  if (existsSync(pidPath)) {
    try {
      const prevPid = parseWatcherPidFile(await readFile(pidPath, "utf-8"));
      if (prevPid) {
        tryKillPid(prevPid, "SIGTERM");
      }
    } catch (error: unknown) {
      if (!hasErrnoCode(error, "ESRCH")) {
        console.warn(
          "[nana] warning: failed to stop stale notify fallback watcher",
          {
            path: pidPath,
            error: error instanceof Error ? error.message : String(error),
          },
        );
      }
    }
  }

  await mkdir(join(cwd, ".nana", "state"), { recursive: true }).catch(
    (error: unknown) => {
      console.warn(
        "[nana] warning: failed to create notify fallback watcher state directory",
        {
          cwd,
          error: error instanceof Error ? error.message : String(error),
        },
      );
    },
  );
  const child = spawn(
    process.execPath,
    [
      watcherScript,
      "--cwd",
      cwd,
      "--notify-script",
      notifyScript,
      "--pid-file",
      pidPath,
      "--parent-pid",
      String(process.pid),
      ...(process.env.NANA_NOTIFY_FALLBACK_MAX_LIFETIME_MS
        ? ["--max-lifetime-ms", process.env.NANA_NOTIFY_FALLBACK_MAX_LIFETIME_MS]
        : []),
    ],
    {
      cwd,
      detached: true,
      stdio: "ignore",
      env: buildNotifyFallbackWatcherEnv(process.env, {
        codexHomeOverride: options.codexHomeOverride,
        enableAuthority: options.enableAuthority === true,
        sessionId: options.sessionId,
      }),
    },
  );
  child.unref();

  await writeFile(
    pidPath,
    JSON.stringify(
      { pid: child.pid, started_at: new Date().toISOString() },
      null,
      2,
    ),
  ).catch((error: unknown) => {
    console.warn(
      "[nana] warning: failed to write notify fallback watcher pid file",
      {
        path: pidPath,
        error: error instanceof Error ? error.message : String(error),
      },
    );
  });
}

async function startHookDerivedWatcher(cwd: string): Promise<void> {
  if (process.env.NANA_HOOK_DERIVED_SIGNALS !== "1") return;

  const { mkdir, writeFile, readFile } = await import("fs/promises");
  const pidPath = hookDerivedWatcherPidPath(cwd);
  const pkgRoot = getPackageRoot();
  const watcherScript = resolveHookDerivedWatcherScript(pkgRoot);
  if (!existsSync(watcherScript)) return;

  if (existsSync(pidPath)) {
    try {
      const prev = JSON.parse(await readFile(pidPath, "utf-8")) as {
        pid?: number;
      };
      if (prev && typeof prev.pid === "number") {
        process.kill(prev.pid, "SIGTERM");
      }
    } catch (error: unknown) {
      console.warn("[nana] warning: failed to stop stale hook-derived watcher", {
        path: pidPath,
        error: error instanceof Error ? error.message : String(error),
      });
    }
  }

  await mkdir(join(cwd, ".nana", "state"), { recursive: true }).catch(
    (error: unknown) => {
      console.warn(
        "[nana] warning: failed to create hook-derived watcher state directory",
        {
          cwd,
          error: error instanceof Error ? error.message : String(error),
        },
      );
    },
  );
  const child = spawn(process.execPath, [watcherScript, "--cwd", cwd], {
    cwd,
    detached: true,
    stdio: "ignore",
    env: process.env,
  });
  child.unref();

  await writeFile(
    pidPath,
    JSON.stringify(
      { pid: child.pid, started_at: new Date().toISOString() },
      null,
      2,
    ),
  ).catch((error: unknown) => {
    console.warn(
      "[nana] warning: failed to write hook-derived watcher pid file",
      {
        path: pidPath,
        error: error instanceof Error ? error.message : String(error),
      },
    );
  });
}

async function stopNotifyFallbackWatcher(cwd: string): Promise<void> {
  const { readFile, unlink } = await import("fs/promises");
  const pidPath = notifyFallbackPidPath(cwd);
  if (!existsSync(pidPath)) return;

  try {
    const pid = parseWatcherPidFile(await readFile(pidPath, "utf-8"));
    if (pid) {
      tryKillPid(pid, "SIGTERM");
    }
  } catch (error: unknown) {
    if (!hasErrnoCode(error, "ESRCH")) {
      console.warn(
        "[nana] warning: failed to stop notify fallback watcher process",
        {
          path: pidPath,
          error: error instanceof Error ? error.message : String(error),
        },
      );
    }
  }

  await unlink(pidPath).catch((error: unknown) => {
    console.warn(
      "[nana] warning: failed to remove notify fallback watcher pid file",
      {
        path: pidPath,
        error: error instanceof Error ? error.message : String(error),
      },
    );
  });
}

async function stopHookDerivedWatcher(cwd: string): Promise<void> {
  const { readFile, unlink } = await import("fs/promises");
  const pidPath = hookDerivedWatcherPidPath(cwd);
  if (!existsSync(pidPath)) return;

  try {
    const parsed = JSON.parse(await readFile(pidPath, "utf-8")) as {
      pid?: number;
    };
    if (parsed && typeof parsed.pid === "number") {
      process.kill(parsed.pid, "SIGTERM");
    }
  } catch (error: unknown) {
    console.warn("[nana] warning: failed to stop hook-derived watcher process", {
      path: pidPath,
      error: error instanceof Error ? error.message : String(error),
    });
  }

  await unlink(pidPath).catch((error: unknown) => {
    console.warn(
      "[nana] warning: failed to remove hook-derived watcher pid file",
      {
        path: pidPath,
        error: error instanceof Error ? error.message : String(error),
      },
    );
  });
}

async function flushNotifyFallbackOnce(
  cwd: string,
  options: { codexHomeOverride?: string; enableAuthority?: boolean; sessionId?: string } = {},
): Promise<void> {
  const { spawnSync } = await import("child_process");
  const pkgRoot = getPackageRoot();
  const watcherScript = resolveNotifyFallbackWatcherScript(pkgRoot);
  const notifyScript = resolveNotifyHookScript(pkgRoot);
  if (!existsSync(watcherScript) || !existsSync(notifyScript)) return;
  spawnSync(
    process.execPath,
    [watcherScript, "--once", "--cwd", cwd, "--notify-script", notifyScript],
    {
      cwd,
      stdio: "ignore",
      timeout: 3000,
      env: buildNotifyFallbackWatcherEnv(process.env, {
        codexHomeOverride: options.codexHomeOverride,
        enableAuthority: options.enableAuthority === true,
        sessionId: options.sessionId,
      }),
    },
  );
}

async function flushHookDerivedWatcherOnce(cwd: string): Promise<void> {
  if (process.env.NANA_HOOK_DERIVED_SIGNALS !== "1") return;
  const { spawnSync } = await import("child_process");
  const pkgRoot = getPackageRoot();
  const watcherScript = resolveHookDerivedWatcherScript(pkgRoot);
  if (!existsSync(watcherScript)) return;
  spawnSync(process.execPath, [watcherScript, "--once", "--cwd", cwd], {
    cwd,
    stdio: "ignore",
    timeout: 3000,
    env: {
      ...process.env,
      NANA_HOOK_DERIVED_SIGNALS: "1",
    },
  });
}

async function cancelModes(): Promise<void> {
  const { writeFile, readFile } = await import("fs/promises");
  const cwd = process.cwd();
  const nowIso = new Date().toISOString();
  try {
    const refs = await listModeStateFilesWithScopePreference(cwd);
    const states = new Map<
      string,
      {
        path: string;
        scope: "root" | "session";
        state: Record<string, unknown>;
      }
    >();

    for (const ref of refs) {
      const content = await readFile(ref.path, "utf-8");
      let parsedState: Record<string, unknown>;
      try {
        parsedState = JSON.parse(content) as Record<string, unknown>;
      } catch (err) {
        process.stderr.write(`[cli/index] operation failed: ${err}\n`);
        continue;
      }
      states.set(ref.mode, {
        path: ref.path,
        scope: ref.scope,
        state: parsedState,
      });
    }

    const changed = new Set<string>();
    const reported = new Set<string>();

    const cancelMode = (
      mode: string,
      phase: string = "cancelled",
      reportIfWasActive: boolean = true,
    ): void => {
      const entry = states.get(mode);
      if (!entry) return;
      const wasActive = entry.state.active === true;
      const needsChange =
        entry.state.active !== false ||
        entry.state.current_phase !== phase ||
        typeof entry.state.completed_at !== "string" ||
        String(entry.state.completed_at).trim() === "";
      if (!needsChange) return;
      entry.state.active = false;
      entry.state.current_phase = phase;
      entry.state.completed_at = nowIso;
      entry.state.last_turn_at = nowIso;
      changed.add(mode);
      if (reportIfWasActive && wasActive) reported.add(mode);
    };

    const ralphLinksUltrawork = (state: Record<string, unknown>): boolean =>
      state.linked_ultrawork === true || state.linked_mode === "ultrawork";

    const ralph = states.get("ralph");
    const hadActiveRalph = !!(ralph && ralph.state.active === true);
    if (ralph && ralph.state.active === true) {
      cancelMode("ralph", "cancelled", true);
      if (ralphLinksUltrawork(ralph.state))
        cancelMode("ultrawork", "cancelled", true);
    }

    if (!hadActiveRalph) {
      for (const [mode, entry] of states.entries()) {
        if (entry.state.active === true) cancelMode(mode, "cancelled", true);
      }
    }

    for (const [mode, entry] of states.entries()) {
      if (!changed.has(mode)) continue;
      await writeFile(entry.path, JSON.stringify(entry.state, null, 2));
    }

    for (const mode of reported) {
      console.log(`Cancelled: ${mode}`);
    }

    if (reported.size === 0) {
      console.log("No active modes to cancel.");
    }
  } catch (err) {
    process.stderr.write(`[cli/index] operation failed: ${err}\n`);
    console.log("No active modes to cancel.");
  }
}
