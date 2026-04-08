import { watch } from 'node:fs';
import { continueGithubSchedulerLoop, resolveGithubSchedulerWatchPaths } from '../cli/github.js';

function parseRequiredFlag(args: readonly string[], flag: string): string {
  const directIndex = args.indexOf(flag);
  if (directIndex >= 0) {
    const value = args[directIndex + 1];
    if (value) return value;
  }
  const inline = args.find((token) => token.startsWith(`${flag}=`));
  if (inline) return inline.slice(flag.length + 1);
  throw new Error(`Missing required flag ${flag}`);
}

function parseOptionalFlag(args: readonly string[], flag: string): string | undefined {
  const directIndex = args.indexOf(flag);
  if (directIndex >= 0) {
    const value = args[directIndex + 1];
    if (value) return value;
  }
  const inline = args.find((token) => token.startsWith(`${flag}=`));
  if (inline) return inline.slice(flag.length + 1);
  return undefined;
}

function readPositiveEnvInt(key: string, fallback: number): number {
  const raw = process.env[key];
  if (!raw) return fallback;
  const parsed = Number(raw);
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : fallback;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitForWakeOrInterval(
  runId: string,
  homeDir: string | undefined,
  intervalMs: number,
): Promise<{ reason: 'watch' | 'poll'; watchMode: 'watch+poll' | 'poll-only' }> {
  const watchPaths = await resolveGithubSchedulerWatchPaths({
    runId,
    homeDir,
    env: process.env,
  }).catch(() => [] as string[]);
  if (watchPaths.length === 0) {
    await sleep(intervalMs);
    return { reason: 'poll', watchMode: 'poll-only' };
  }

  return await new Promise<{ reason: 'watch' | 'poll'; watchMode: 'watch+poll' | 'poll-only' }>((resolve) => {
    let settled = false;
    const cleanup: Array<() => void> = [];
    let watchAttached = false;
    let wakeReason: 'watch' | 'poll' = 'poll';
    const finish = () => {
      if (settled) return;
      settled = true;
      for (const close of cleanup) close();
      resolve({
        reason: wakeReason,
        watchMode: watchAttached ? 'watch+poll' : 'poll-only',
      });
    };
    const timer = setTimeout(finish, intervalMs);
    cleanup.push(() => clearTimeout(timer));

    for (const path of watchPaths) {
      try {
        const watcher = watch(path, () => {
          wakeReason = 'watch';
          finish();
        });
        watchAttached = true;
        cleanup.push(() => watcher.close());
      } catch {
        // Ignore watch failures; timer fallback still applies.
      }
    }
  });
}

async function main(): Promise<void> {
  const args = process.argv.slice(2);
  const runId = parseRequiredFlag(args, '--run-id');
  const homeDir = parseOptionalFlag(args, '--home-dir');
  const intervalMs = readPositiveEnvInt('NANA_GITHUB_SCHEDULER_DAEMON_INTERVAL_MS', 15_000);
  const timeoutMs = readPositiveEnvInt('NANA_GITHUB_SCHEDULER_DAEMON_TIMEOUT_MS', 2 * 60 * 60_000);
  const startedAt = Date.now();
  let nextWakeReason: 'startup' | 'watch' | 'poll' = 'startup';
  let nextWatchMode: 'watch+poll' | 'poll-only' = 'poll-only';

  while (Date.now() - startedAt <= timeoutMs) {
    try {
      const result = await continueGithubSchedulerLoop({
        runId,
        homeDir,
        env: process.env,
        writeLine: () => {},
        wakeReason: nextWakeReason,
        watchMode: nextWatchMode,
      });
      if (!result.hasRemainingWork) {
        return;
      }
    } catch {
      // Retry on the next interval. Lane/runtime state persists across attempts.
    }
    const wake = await waitForWakeOrInterval(runId, homeDir, intervalMs);
    nextWakeReason = wake.reason;
    nextWatchMode = wake.watchMode;
  }
}

await main().catch(() => {
  process.exit(0);
});
