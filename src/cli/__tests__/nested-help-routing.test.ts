import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

function runNana(cwd: string, argv: string[]) {
  const testDir = dirname(fileURLToPath(import.meta.url));
  const repoRoot = join(testDir, '..', '..', '..');
  const nanaBin = join(repoRoot, 'dist', 'cli', 'nana.js');
  return spawnSync(process.execPath, [nanaBin, ...argv], {
    cwd,
    encoding: 'utf-8',
    env: {
      ...process.env,
      NANA_AUTO_UPDATE: '0',
      NANA_NOTIFY_FALLBACK: '0',
      NANA_HOOK_DERIVED_SIGNALS: '0',
    },
  });
}

describe('nested help routing', () => {
  for (const [argv, expectedUsage] of [
    [['investigate', '--help'], /nana issue - GitHub issue-oriented aliases/i],
    [['implement', '--help'], /nana issue - GitHub issue-oriented aliases/i],
    [['sync', '--help'], /nana issue - GitHub issue-oriented aliases/i],
    [['review-rules', '--help'], /nana review-rules - Persistent repo rules mined from PR review history/i],
    [['research', '--help'], /Usage:[\s\S]*nana research <mission-dir>/i],
    [['issue', '--help'], /nana issue - GitHub issue-oriented aliases/i],
    [['work-on', '--help'], /nana work-on - GitHub-targeted issue\/PR implementation helper/i],
    [['reflect', '--help'], /Usage:\s*nana reflect --prompt/i],
    [['hud', '--help'], /Usage:\s*\n\s*nana hud\s+Show current HUD state/i],
    [['hooks', '--help'], /Usage:\s*\n\s*nana hooks init/i],
    [['ralph', '--help'], /nana ralph - Launch Codex with ralph persistence mode active/i],
  ] satisfies Array<[string[], RegExp]>) {
    it(`routes ${argv.join(' ')} to command-local help`, async () => {
      const cwd = await mkdtemp(join(tmpdir(), 'nana-nested-help-'));
      try {
        const result = runNana(cwd, argv);
        assert.equal(result.status, 0, result.stderr || result.stdout);
        assert.match(result.stdout, expectedUsage);
        assert.doesNotMatch(result.stdout, /nana \(nana\) - Multi-agent orchestration for Codex CLI/i);
      } finally {
        await rm(cwd, { recursive: true, force: true });
      }
    });
  }
});
