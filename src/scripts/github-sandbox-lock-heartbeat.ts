import { existsSync } from 'node:fs';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

interface SandboxLease {
  version: 1;
  sandbox_id: string;
  owner_pid: number;
  owner_run_id: string;
  target_url: string;
  acquired_at: string;
  heartbeat_at: string;
  expires_at: string;
}

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

function isPidAlive(pid: number): boolean {
  if (!Number.isFinite(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function tick(lockDir: string, ownerPid: number, ttlMs: number): Promise<boolean> {
  if (!existsSync(lockDir)) return false;

  const leasePath = join(lockDir, 'lease.json');
  let lease: SandboxLease;
  try {
    lease = JSON.parse(await readFile(leasePath, 'utf-8')) as SandboxLease;
  } catch {
    return false;
  }

  if (lease.owner_pid !== ownerPid) return false;
  if (!isPidAlive(ownerPid)) return false;

  const now = new Date();
  lease.heartbeat_at = now.toISOString();
  lease.expires_at = new Date(now.getTime() + ttlMs).toISOString();
  await mkdir(lockDir, { recursive: true });
  await writeFile(leasePath, JSON.stringify(lease, null, 2));
  return true;
}

async function main(): Promise<void> {
  const lockDir = parseRequiredFlag(process.argv.slice(2), '--lock-dir');
  const ownerPid = Number.parseInt(parseRequiredFlag(process.argv.slice(2), '--owner-pid'), 10);
  const ttlMs = Math.max(1_000, Number.parseInt(parseRequiredFlag(process.argv.slice(2), '--ttl-ms'), 10));
  const heartbeatMs = Math.max(500, Number.parseInt(parseRequiredFlag(process.argv.slice(2), '--heartbeat-ms'), 10));

  const alive = await tick(lockDir, ownerPid, ttlMs).catch(() => false);
  if (!alive) return;

  const interval = setInterval(() => {
    void tick(lockDir, ownerPid, ttlMs)
      .then((keepRunning) => {
        if (!keepRunning) {
          clearInterval(interval);
          process.exit(0);
        }
      })
      .catch(() => {
        clearInterval(interval);
        process.exit(0);
      });
  }, heartbeatMs);
  interval.unref();
}

await main().catch(() => {
  process.exit(0);
});
