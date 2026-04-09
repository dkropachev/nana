import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, writeFile, copyFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { spawn, spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

interface CompatRunResult {
  status: number | null;
  stdout: string;
  stderr: string;
  error?: string;
}

const testDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(testDir, "..", "..", "..");
const asyncCompatTimeoutMs = 15000;
const syncCompatTimeoutMs = 15000;
let cachedBuiltTargetPromise: Promise<string> | undefined;

function shouldSkipForSpawnPermissions(err?: string): boolean {
  return typeof err === "string" && /(EPERM|EACCES)/i.test(err);
}

async function buildSharedGoTarget(): Promise<string> {
  const buildDir = await mkdtemp(join(tmpdir(), "nana-go-compat-bin-"));
  const binaryPath = join(buildDir, process.platform === "win32" ? "nana.exe" : "nana");
  const result = spawnSync("go", ["build", "-o", binaryPath, "./cmd/nana"], {
    cwd: repoRoot,
    encoding: "utf-8",
    timeout: syncCompatTimeoutMs,
    killSignal: "SIGKILL",
  });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  return binaryPath;
}

async function buildGoTarget(): Promise<string> {
  cachedBuiltTargetPromise ??= buildSharedGoTarget();
  const sharedBinaryPath = await cachedBuiltTargetPromise;
  const targetDir = await mkdtemp(join(tmpdir(), "nana-go-compat-copy-"));
  const targetPath = join(targetDir, process.platform === "win32" ? "nana.exe" : "nana");
  await copyFile(sharedBinaryPath, targetPath);
  return targetPath;
}

function runCompatTarget(
  targetPath: string,
  cwd: string,
  argv: string[],
  envOverrides: Record<string, string> = {},
): CompatRunResult {
  const command = targetPath.endsWith(".js") ? process.execPath : targetPath;
  const argsPrefix = targetPath.endsWith(".js") ? [targetPath] : [];
  const result = spawnSync(command, [...argsPrefix, ...argv], {
    cwd,
    encoding: "utf-8",
    env: { ...process.env, NANA_REPO_ROOT: repoRoot, ...envOverrides },
    timeout: syncCompatTimeoutMs,
    killSignal: "SIGKILL",
  });
  return {
    status: result.status,
    stdout: result.stdout || "",
    stderr: result.stderr || "",
    error: result.error?.message,
  };
}

async function runCompatTargetAsync(
  targetPath: string,
  cwd: string,
  argv: string[],
  envOverrides: Record<string, string> = {},
): Promise<CompatRunResult> {
  const command = targetPath.endsWith(".js") ? process.execPath : targetPath;
  const argsPrefix = targetPath.endsWith(".js") ? [targetPath] : [];
  return await new Promise<CompatRunResult>((resolve) => {
    const child = spawn(command, [...argsPrefix, ...argv], {
      cwd,
      env: { ...process.env, NANA_REPO_ROOT: repoRoot, ...envOverrides },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    let settled = false;
    const finish = (result: CompatRunResult) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      resolve(result);
    };
    const timeout = setTimeout(() => {
      child.kill("SIGTERM");
      setTimeout(() => child.kill("SIGKILL"), 1000).unref();
      finish({
        status: null,
        stdout,
        stderr,
        error: `timed out after ${asyncCompatTimeoutMs}ms: ${command} ${[...argsPrefix, ...argv].join(" ")}`,
      });
    }, asyncCompatTimeoutMs);
    child.stdout.setEncoding("utf-8");
    child.stderr.setEncoding("utf-8");
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("close", (status) => finish({ status, stdout, stderr }));
    child.on("error", (error) => finish({ status: null, stdout, stderr, error: error.message }));
  });
}

describe("Go CLI contract", () => {
  it("keeps session-scoped status and cancel behavior", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-session-"));
    try {
      const stateDir = join(wd, ".nana", "state");
      const scopedDir = join(stateDir, "sessions", "sess1");
      await mkdir(scopedDir, { recursive: true });
      await writeFile(join(stateDir, "session.json"), JSON.stringify({ session_id: "sess1" }));
      await writeFile(
        join(scopedDir, "team-state.json"),
        JSON.stringify({ active: true, current_phase: "team-exec" }),
      );

      const statusResult = runCompatTarget(target, wd, ["status"]);
      if (shouldSkipForSpawnPermissions(statusResult.error)) return;
      assert.equal(statusResult.status, 0, statusResult.stderr || statusResult.stdout);
      assert.match(statusResult.stdout, /team: ACTIVE \(phase: team-exec\)/);

      const cancelResult = runCompatTarget(target, wd, ["cancel"]);
      assert.equal(cancelResult.status, 0, cancelResult.stderr || cancelResult.stdout);
      assert.match(cancelResult.stdout, /Cancelled: team/);

      const updated = JSON.parse(await readFile(join(scopedDir, "team-state.json"), "utf-8")) as {
        active: boolean;
        current_phase: string;
      };
      assert.equal(updated.active, false);
      assert.equal(updated.current_phase, "cancelled");
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps hooks init/status behavior on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-hooks-"));
    try {
      const initResult = runCompatTarget(target, wd, ["hooks", "init"]);
      if (shouldSkipForSpawnPermissions(initResult.error)) return;
      assert.equal(initResult.status, 0, initResult.stderr || initResult.stdout);
      assert.match(initResult.stdout, /Plugins are enabled by default\. Disable with NANA_HOOK_PLUGINS=0\./);
      assert.equal(existsSync(join(wd, ".nana", "hooks", "sample-plugin.mjs")), true);

      const statusResult = runCompatTarget(target, wd, ["hooks", "status"]);
      assert.equal(statusResult.status, 0, statusResult.stderr || statusResult.stdout);
      assert.match(statusResult.stdout, /hooks status/);
      assert.match(statusResult.stdout, /Plugins enabled: yes/);
      assert.match(statusResult.stdout, /sample-plugin\.mjs/);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps hud help on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-hud-"));
    try {
      const result = runCompatTarget(target, wd, ["hud", "--help"]);
      if (shouldSkipForSpawnPermissions(result.error)) return;
      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /Usage:\s*\n\s*nana hud\s+Show current HUD state/);
      assert.match(result.stdout, /--watch/);
      assert.match(result.stdout, /--json/);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps nested GitHub help routing on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-github-help-"));
    try {
      const cases: Array<[string[], RegExp]> = [
        [["implement", "--help"], /nana issue - GitHub issue-oriented aliases/i],
        [["investigate", "--help"], /nana issue - GitHub issue-oriented aliases/i],
        [["sync", "--help"], /nana issue - GitHub issue-oriented aliases/i],
        [["issue", "--help"], /nana issue - GitHub issue-oriented aliases/i],
        [["review", "--help"], /nana review - Review an external GitHub PR with deterministic persistence/i],
        [["review-rules", "--help"], /nana review-rules - Persistent repo rules mined from PR review history/i],
        [["work-on", "--help"], /nana work-on - GitHub-targeted issue\/PR implementation helper/i],
      ];

      for (const [argv, expected] of cases) {
        const result = runCompatTarget(target, wd, argv);
        if (shouldSkipForSpawnPermissions(result.error)) return;
        assert.equal(result.status, 0, result.stderr || result.stdout);
        assert.match(result.stdout, expected);
      }
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps launch-only worktree and notify-temp flags off the codex argv on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-launch-worktree-"));
    try {
      const home = join(wd, "home");
      const fakeBin = join(wd, "bin");
      const fakeCodexPath = join(fakeBin, process.platform === "win32" ? "codex.cmd" : "codex");
      await mkdir(fakeBin, { recursive: true });

      const gitEnv = { ...process.env, HOME: home };
      assert.equal(spawnSync("git", ["init"], { cwd: wd, encoding: "utf-8", env: gitEnv }).status, 0);
      assert.equal(spawnSync("git", ["config", "user.email", "compat@example.com"], { cwd: wd, encoding: "utf-8", env: gitEnv }).status, 0);
      assert.equal(spawnSync("git", ["config", "user.name", "Compat Test"], { cwd: wd, encoding: "utf-8", env: gitEnv }).status, 0);
      assert.equal(spawnSync("git", ["commit", "--allow-empty", "-m", "init"], { cwd: wd, encoding: "utf-8", env: gitEnv }).status, 0);

      if (process.platform === "win32") {
        await writeFile(
          fakeCodexPath,
          [
            "@echo off",
            "echo fake-codex-cwd:%CD%",
            "echo fake-codex:%*",
            "echo notify-temp:%NANA_NOTIFY_TEMP%",
            "echo notify-contract:%NANA_NOTIFY_TEMP_CONTRACT%",
          ].join("\r\n"),
          "utf-8",
        );
      } else {
        await writeFile(
          fakeCodexPath,
          [
            "#!/bin/sh",
            "printf 'fake-codex-cwd:%s\\n' \"$PWD\"",
            "printf 'fake-codex:%s\\n' \"$*\"",
            "printf 'notify-temp:%s\\n' \"$NANA_NOTIFY_TEMP\"",
            "printf 'notify-contract:%s\\n' \"$NANA_NOTIFY_TEMP_CONTRACT\"",
          ].join("\n"),
          "utf-8",
        );
        spawnSync("chmod", ["+x", fakeCodexPath], { cwd: wd, encoding: "utf-8" });
      }

      const result = runCompatTarget(target, wd, ["--worktree", "--notify-temp", "--discord", "--model", "gpt-5"], {
        HOME: home,
        CODEX_HOME: join(home, ".codex-home"),
        PATH: `${fakeBin}${process.platform === "win32" ? ";" : ":"}${process.env.PATH ?? ""}`,
      });
      if (shouldSkipForSpawnPermissions(result.error)) return;
      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /fake-codex-cwd:.*launch-detached/);
      assert.match(result.stdout, /fake-codex:.*--model gpt-5/);
      assert.doesNotMatch(result.stdout, /--notify-temp|--discord|--worktree/);
      assert.match(result.stdout, /notify-temp:1/);
      assert.match(result.stdout, /"canonicalSelectors":\["discord"\]/);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps session search JSON output behavior on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-search-"));
    const codexHomeDir = join(wd, ".codex-home");
    try {
      const dir = join(codexHomeDir, "sessions", "2026", "03", "10");
      await mkdir(dir, { recursive: true });
      await writeFile(
        join(dir, "rollout-2026-03-10T12-00-00-session-a.jsonl"),
        [
          JSON.stringify({
            type: "session_meta",
            payload: { id: "session-a", timestamp: "2026-03-10T12:00:00.000Z", cwd: wd },
          }),
          JSON.stringify({
            type: "event_msg",
            payload: { type: "user_message", message: "Show previous discussions of team api in recent runs." },
          }),
          "",
        ].join("\n"),
        "utf-8",
      );

      const result = runCompatTarget(
        target,
        wd,
        ["session", "search", "team api", "--project", "current", "--json"],
        { CODEX_HOME: codexHomeDir },
      );
      if (shouldSkipForSpawnPermissions(result.error)) return;
      assert.equal(result.status, 0, result.stderr || result.stdout);
      const parsed = JSON.parse(result.stdout) as {
        query: string;
        results: Array<{ session_id: string; cwd: string; snippet: string }>;
      };
      assert.equal(parsed.query, "team api");
      assert.equal(parsed.results.length, 1);
      assert.equal(parsed.results[0]?.session_id, "session-a");
      assert.equal(parsed.results[0]?.cwd, wd);
      assert.match(parsed.results[0]?.snippet ?? "", /team api/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps ask advisor passthrough behavior on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-ask-"));
    try {
      const result = runCompatTarget(target, wd, ["ask", "claude", "pass-through"], {
        NANA_ASK_ADVISOR_SCRIPT: "dist/scripts/fixtures/ask-advisor-stub.js",
        NANA_ASK_STUB_STDOUT: "artifact-path-from-stub.md\n",
        NANA_ASK_STUB_STDERR: "stub-warning-line\n",
        NANA_ASK_STUB_EXIT_CODE: "7",
      });
      if (shouldSkipForSpawnPermissions(result.error)) return;

      assert.equal(result.status, 7, result.stderr || result.stdout);
      assert.equal(result.stdout, "artifact-path-from-stub.md\n");
      assert.equal(result.stderr, "stub-warning-line\n");
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps agents-init and deepinit help on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-agents-init-"));
    try {
      const helpResult = runCompatTarget(target, wd, ["agents-init", "--help"]);
      if (shouldSkipForSpawnPermissions(helpResult.error)) return;
      assert.equal(helpResult.status, 0, helpResult.stderr || helpResult.stdout);
      assert.match(helpResult.stdout, /Usage: nana agents-init/);

      const aliasResult = runCompatTarget(target, wd, ["deepinit", "--help"]);
      assert.equal(aliasResult.status, 0, aliasResult.stderr || aliasResult.stdout);
      assert.match(aliasResult.stdout, /Usage: nana agents-init/);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps uninstall dry-run behavior on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-uninstall-"));
    try {
      const home = join(wd, "home");
      const codexDir = join(home, ".codex");
      await mkdir(codexDir, { recursive: true });
      await writeFile(
        join(codexDir, "config.toml"),
        [
          '# nana top-level settings (must be before any [table])',
          'notify = ["node", "/path/to/notify-hook.js"]',
          'model_reasoning_effort = "high"',
          'developer_instructions = "You have nana installed."',
          '',
          '[features]',
          'multi_agent = true',
          'child_agents_md = true',
          '',
          '# ============================================================',
          '# nana (NANA) Configuration',
          '# Managed by nana setup - manual edits preserved on next setup',
          '# ============================================================',
          '',
          '[mcp_servers.nana_state]',
          'command = "node"',
          'args = ["/path/to/state-server.js"]',
          '',
          '# ============================================================',
          '# End nana',
          '',
        ].join("\n"),
      );

      const result = runCompatTarget(target, wd, ["uninstall", "--dry-run"], { HOME: home, CODEX_HOME: codexDir });
      if (shouldSkipForSpawnPermissions(result.error)) return;
      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /dry-run mode/i);
      assert.match(result.stdout, /Resolved scope: user/);
      assert.match(result.stdout, /NANA configuration block/);

      const config = await readFile(join(codexDir, "config.toml"), "utf-8");
      assert.match(config, /nana \(NANA\) Configuration/);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps work-on defaults set/show behavior on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-workon-defaults-"));
    const home = join(wd, "home");
    try {
      const setResult = runCompatTarget(target, wd, [
        "work-on",
        "defaults",
        "set",
        "acme/widget",
        "--considerations",
        "style,qa,security",
        "--review-rules-mode",
        "automatic",
      ], { HOME: home });
      if (shouldSkipForSpawnPermissions(setResult.error)) return;
      assert.equal(setResult.status, 0, setResult.stderr || setResult.stdout);
      assert.match(setResult.stdout, /Saved default considerations for acme\/widget: style, qa, security/i);

      const showResult = runCompatTarget(target, wd, [
        "work-on",
        "defaults",
        "show",
        "acme/widget",
      ], { HOME: home });
      assert.equal(showResult.status, 0, showResult.stderr || showResult.stdout);
      assert.match(showResult.stdout, /Default considerations for acme\/widget: style, qa, security/i);
      assert.match(showResult.stdout, /Effective review-rules mode for acme\/widget: automatic/i);
      assert.match(showResult.stdout, /Resolved default pipeline:/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps review-rules config set/show behavior on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-review-rules-config-"));
    const home = join(wd, "home");
    try {
      const setResult = runCompatTarget(target, wd, [
        "review-rules",
        "config",
        "set",
        "--mode",
        "automatic",
        "--trusted-reviewers",
        "reviewer-a,reviewer-b",
      ], { HOME: home });
      if (shouldSkipForSpawnPermissions(setResult.error)) return;
      assert.equal(setResult.status, 0, setResult.stderr || setResult.stdout);
      assert.match(setResult.stdout, /Saved global review-rules mode: automatic/i);

      const showResult = runCompatTarget(target, wd, [
        "review-rules",
        "config",
        "show",
        "https://github.com/acme/widget/issues/42",
      ], { HOME: home });
      assert.equal(showResult.status, 0, showResult.stderr || showResult.stdout);
      assert.match(showResult.stdout, /Global review-rules mode: automatic/i);
      assert.match(showResult.stdout, /Effective review-rules mode for acme\/widget: automatic/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps work-on stats behavior on the Go CLI path for issue targets", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-workon-stats-"));
    const home = join(wd, "home");
    try {
      const statsPath = join(home, ".nana", "repos", "acme", "widget", "issues", "issue-42.json");
      await mkdir(dirname(statsPath), { recursive: true });
      await writeFile(
        statsPath,
        JSON.stringify({
          version: 1,
          repo_slug: "acme/widget",
          issue_number: 42,
          updated_at: "2026-04-03T10:15:00.000Z",
          totals: {
            input_tokens: 120,
            output_tokens: 80,
            total_tokens: 200,
            sessions_accounted: 1,
          },
          sandboxes: {
            "issue-42-pr-123456789012": {
              input_tokens: 120,
              output_tokens: 80,
              total_tokens: 200,
              sessions_accounted: 1,
            },
          },
        }, null, 2),
      );

      const result = runCompatTarget(target, wd, [
        "work-on",
        "stats",
        "https://github.com/acme/widget/issues/42",
      ], { HOME: home });
      if (shouldSkipForSpawnPermissions(result.error)) return;
      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /Token stats for acme\/widget issue #42/i);
      assert.match(result.stdout, /Total input tokens: 120/i);
      assert.match(result.stdout, /issue-42-pr-123456789012: total=200 input=120 output=80 sessions=1/i);

      const prSandboxPath = join(home, ".nana", "repos", "acme", "widget", "sandboxes", "pr-77");
      await mkdir(join(prSandboxPath, ".nana"), { recursive: true });
      await writeFile(
        join(prSandboxPath, ".nana", "sandbox.json"),
        JSON.stringify({
          sandbox_id: "pr-77",
          target_kind: "issue",
          target_number: 42,
        }, null, 2),
      );

      const prResult = runCompatTarget(target, wd, [
        "work-on",
        "stats",
        "https://github.com/acme/widget/pull/77",
      ], { HOME: home });
      assert.equal(prResult.status, 0, prResult.stderr || prResult.stdout);
      assert.match(prResult.stdout, /Token stats for acme\/widget issue #42/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps work-on retrospective behavior on the Go CLI path", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-workon-retrospective-"));
    const home = join(wd, "home");
    const managedRepoRoot = join(home, ".nana", "repos", "acme", "widget");
    const sandboxPath = join(managedRepoRoot, "sandboxes", "issue-42-pr-123456789012");
    const repoCheckoutPath = join(sandboxPath, "repo");
    const runId = "gh-retro-1";
    try {
      await mkdir(join(sandboxPath, ".nana"), { recursive: true });
      const sessionsDir = join(sandboxPath, ".codex", "sessions", "2026", "04", "03");
      await mkdir(sessionsDir, { recursive: true });
      await mkdir(repoCheckoutPath, { recursive: true });
      await writeFile(join(repoCheckoutPath, "README.md"), "# widget\n", "utf-8");
      await mkdir(join(managedRepoRoot, "runs", runId), { recursive: true });
      await mkdir(join(home, ".nana", "github-workon"), { recursive: true });
      await writeFile(join(home, ".nana", "github-workon", "latest-run.json"), JSON.stringify({
        repo_root: managedRepoRoot,
        run_id: runId,
      }, null, 2));
      await writeFile(join(sessionsDir, "rollout-1.jsonl"), [
        JSON.stringify({
          timestamp: "2026-04-03T17:00:01.000Z",
          type: "session_meta",
          payload: { agent_nickname: "", agent_role: "" },
        }),
        JSON.stringify({
          timestamp: "2026-04-03T17:00:11.000Z",
          type: "event_msg",
          payload: { type: "token_count", info: { total_token_usage: { total_tokens: 1234 } } },
        }),
        "",
      ].join("\n"), "utf-8");
      await writeFile(join(sessionsDir, "rollout-2.jsonl"), [
        JSON.stringify({
          timestamp: "2026-04-03T17:00:02.000Z",
          type: "session_meta",
          payload: { agent_nickname: "Gauss", agent_role: "architect" },
        }),
        JSON.stringify({
          timestamp: "2026-04-03T17:00:09.000Z",
          type: "event_msg",
          payload: { type: "token_count", info: { total_token_usage: { total_tokens: 4321 } } },
        }),
        "",
      ].join("\n"), "utf-8");
      await writeFile(join(managedRepoRoot, "runs", runId, "manifest.json"), JSON.stringify({
        run_id: runId,
        repo_slug: "acme/widget",
        repo_owner: "acme",
        repo_name: "widget",
        sandbox_path: sandboxPath,
        sandbox_repo_path: repoCheckoutPath,
        role_layout: "split",
        considerations_active: ["arch", "qa"],
      }, null, 2));

      const result = runCompatTarget(target, wd, ["work-on", "retrospective", "--last"], { HOME: home });
      if (shouldSkipForSpawnPermissions(result.error)) return;
      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /NANA Work-on Retrospective/i);
      assert.match(result.stdout, /Role layout: split/i);
      assert.match(result.stdout, /Total thread tokens: 5555/i);
      assert.match(result.stdout, /Gauss: role=architect class=reviewer tokens=4321/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps review-rules lifecycle behavior on the Go CLI path for repo-slug targets", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-review-rules-lifecycle-"));
    const home = join(wd, "home");
    try {
      const rulesPath = join(home, ".nana", "repos", "acme", "widget", "source", ".nana", "repo-review-rules.json");
      await mkdir(dirname(rulesPath), { recursive: true });
      await writeFile(
        rulesPath,
        JSON.stringify({
          approved_rules: [],
          pending_candidates: [
            {
              id: "qa-1",
              title: "Add regression coverage",
              category: "qa",
              confidence: 0.95,
              reviewer_count: 2,
              extraction_origin: "review_comments",
              extraction_reason: "Repeated review comments across 2 PRs",
              path_scopes: ["src/api/client.ts"],
              evidence: [],
            },
          ],
          disabled_rules: [],
          archived_rules: [],
        }, null, 2),
      );

      const listResult = runCompatTarget(target, wd, ["review-rules", "list", "https://github.com/acme/widget/pull/7"], { HOME: home });
      if (shouldSkipForSpawnPermissions(listResult.error)) return;
      assert.equal(listResult.status, 0, listResult.stderr || listResult.stdout);
      assert.match(listResult.stdout, /pending qa-1 \[qa\] confidence=0\.95 reviewers=2 Add regression coverage/i);

      const approveResult = runCompatTarget(target, wd, ["review-rules", "approve", "https://github.com/acme/widget/pull/7", "qa-1"], { HOME: home });
      assert.equal(approveResult.status, 0, approveResult.stderr || approveResult.stdout);
      assert.match(approveResult.stdout, /Approved 1 repo review rule\(s\) for acme\/widget/i);

      const explainResult = runCompatTarget(target, wd, ["review-rules", "explain", "https://github.com/acme/widget/pull/7", "qa-1"], { HOME: home });
      assert.equal(explainResult.status, 0, explainResult.stderr || explainResult.stdout);
      assert.match(explainResult.stdout, /Rule qa-1 \(approved\)/i);

      const archiveResult = runCompatTarget(target, wd, ["review-rules", "archive", "https://github.com/acme/widget/pull/7", "qa-1"], { HOME: home });
      assert.equal(archiveResult.status, 0, archiveResult.stderr || archiveResult.stdout);
      assert.match(archiveResult.stdout, /Archived 1 review rule\(s\) for acme\/widget/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });

  it("keeps review-rules scan behavior on the Go CLI path for repo-slug targets", async () => {
    const target = await buildGoTarget();
    const wd = await mkdtemp(join(tmpdir(), "nana-go-compat-review-rules-scan-"));
    const home = join(wd, "home");
    const routes = new Map<string, unknown>([
      ["/repos/acme/widget/pulls?state=all&per_page=100&page=1", [
        { number: 7, head: { sha: "sha-pr-7" } },
        { number: 8, head: { sha: "sha-pr-8" } },
      ]],
      ["/repos/acme/widget/pulls?state=all&per_page=100&page=2", []],
      ["/repos/acme/widget/pulls/7", { number: 7, head: { sha: "sha-pr-7" } }],
      ["/repos/acme/widget/pulls/8", { number: 8, head: { sha: "sha-pr-8" } }],
      ["/repos/acme/widget/pulls/7/reviews?per_page=100", [
        {
          id: 701,
          html_url: "https://example.invalid/review/701",
          body: "Please add regression tests for this behavior change before merge.",
          state: "CHANGES_REQUESTED",
          user: { login: "reviewer-a" },
        },
      ]],
      ["/repos/acme/widget/pulls/8/reviews?per_page=100", [
        {
          id: 702,
          html_url: "https://example.invalid/review/702",
          body: "Needs regression coverage before we merge this.",
          state: "COMMENTED",
          user: { login: "reviewer-b" },
        },
      ]],
      ["/repos/acme/widget/pulls/7/comments?per_page=100", []],
      ["/repos/acme/widget/pulls/8/comments?per_page=100", []],
    ]);
    const server = (await import("node:http")).createServer((req, res) => {
      const key = req.url || "/";
      if (!routes.has(key)) {
        res.writeHead(500);
        res.end(`unexpected route: ${key}`);
        return;
      }
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify(routes.get(key)));
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    try {
      assert.ok(address && typeof address !== "string");
      const baseUrl = `http://127.0.0.1:${address.port}`;
      await mkdir(join(home, ".nana", "repos", "acme", "widget", "runs", "gh-link-1"), { recursive: true });
      await writeFile(join(home, ".nana", "repos", "acme", "widget", "runs", "gh-link-1", "manifest.json"), JSON.stringify({
        repo_slug: "acme/widget",
        target_kind: "issue",
        target_number: 42,
        published_pr_number: 7,
      }, null, 2));
      await mkdir(join(home, ".nana", "repos", "acme", "widget", "runs", "gh-link-2"), { recursive: true });
      await writeFile(join(home, ".nana", "repos", "acme", "widget", "runs", "gh-link-2", "manifest.json"), JSON.stringify({
        repo_slug: "acme/widget",
        target_kind: "issue",
        target_number: 42,
        published_pr_number: 8,
      }, null, 2));

      const result = await runCompatTargetAsync(target, wd, ["review-rules", "scan", "https://github.com/acme/widget/issues/42"], {
        HOME: home,
        GH_TOKEN: "test-token",
        GITHUB_API_URL: baseUrl,
      });
      if (shouldSkipForSpawnPermissions(result.error)) return;
      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /Scanned PR review history for acme\/widget from https:\/\/github.com\/acme\/widget\/issues\/42/i);
      assert.match(result.stdout, /pending qa-/i);
      const rulesPath = join(home, ".nana", "repos", "acme", "widget", "source", ".nana", "repo-review-rules.json");
      assert.equal(existsSync(rulesPath), true);
    } finally {
      server.closeAllConnections?.();
      server.closeIdleConnections?.();
      await new Promise<void>((resolve, reject) => server.close((err) => err ? reject(err) : resolve()));
      await rm(wd, { recursive: true, force: true });
      await rm(dirname(target), { recursive: true, force: true });
    }
  });
});
