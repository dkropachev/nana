import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { normalizeAutoresearchCodexArgs, parseAutoresearchArgs } from '../autoresearch.js';

function runNana(
  cwd: string,
  argv: string[],
  envOverrides: Record<string, string> = {},
): { status: number | null; stdout: string; stderr: string; error?: string } {
  const testDir = dirname(fileURLToPath(import.meta.url));
  const repoRoot = join(testDir, '..', '..', '..');
  const nanaBin = join(repoRoot, 'dist', 'cli', 'nana.js');
  const r = spawnSync(process.execPath, [nanaBin, ...argv], {
    cwd,
    encoding: 'utf-8',
    env: {
      ...process.env,
      NANA_AUTO_UPDATE: '0',
      NANA_NOTIFY_FALLBACK: '0',
      NANA_HOOK_DERIVED_SIGNALS: '0',
      ...envOverrides,
    },
  });
  return { status: r.status, stdout: r.stdout || '', stderr: r.stderr || '', error: r.error?.message };
}

describe('normalizeAutoresearchCodexArgs', () => {
  it('adds sandbox bypass by default for autoresearch workers', () => {
    assert.deepEqual(
      normalizeAutoresearchCodexArgs(['--model', 'gpt-5']),
      ['--model', 'gpt-5', '--dangerously-bypass-approvals-and-sandbox'],
    );
  });

  it('deduplicates explicit bypass flags', () => {
    assert.deepEqual(
      normalizeAutoresearchCodexArgs(['--dangerously-bypass-approvals-and-sandbox']),
      ['--dangerously-bypass-approvals-and-sandbox'],
    );
  });

  it('normalizes --madmax to the canonical bypass flag', () => {
    assert.deepEqual(
      normalizeAutoresearchCodexArgs(['--madmax']),
      ['--dangerously-bypass-approvals-and-sandbox'],
    );
  });
});

describe('parseAutoresearchArgs', () => {
  it('treats top-level topic/evaluator flags as seeded deep-interview input', () => {
    const parsed = parseAutoresearchArgs(['--topic', 'Improve docs', '--evaluator', 'node eval.js', '--slug', 'docs-run']);
    assert.equal(parsed.guided, true);
    assert.equal(parsed.seedArgs?.topic, 'Improve docs');
    assert.equal(parsed.seedArgs?.evaluatorCommand, 'node eval.js');
    assert.equal(parsed.seedArgs?.slug, 'docs-run');
  });

  it('treats bare init as guided alias and init with flags as expert init args', () => {
    const bare = parseAutoresearchArgs(['init']);
    assert.equal(bare.guided, true);
    assert.deepEqual(bare.initArgs, []);

    const flagged = parseAutoresearchArgs(['init', '--topic', 'Ship feature']);
    assert.equal(flagged.guided, true);
    assert.deepEqual(flagged.initArgs, ['--topic', 'Ship feature']);
  });

  it('parses explicit run subcommand without breaking bare mission-dir execution', () => {
    const runParsed = parseAutoresearchArgs(['run', 'missions/demo', '--model', 'gpt-5']);
    assert.equal(runParsed.runSubcommand, true);
    assert.equal(runParsed.missionDir, 'missions/demo');
    assert.deepEqual(runParsed.codexArgs, ['--model', 'gpt-5']);

    const bareParsed = parseAutoresearchArgs(['missions/demo', '--model', 'gpt-5']);
    assert.equal(bareParsed.runSubcommand, undefined);
    assert.equal(bareParsed.missionDir, 'missions/demo');
    assert.deepEqual(bareParsed.codexArgs, ['--model', 'gpt-5']);
  });
});

describe('removed autoresearch CLI surface', () => {
  it('does not advertise autoresearch in top-level help', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-autoresearch-help-'));
    try {
      const result = runNana(cwd, ['--help']);
      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.doesNotMatch(result.stdout, /\bnana autoresearch\b/i);
      assert.doesNotMatch(result.stdout, /\bnana research\b/i);
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

  it('fails for the removed autoresearch command', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-autoresearch-removed-'));
    try {
      const result = runNana(cwd, ['autoresearch', '--help']);
      assert.notEqual(result.status, 0, result.stderr || result.stdout);
      assert.match(`${result.stderr}\n${result.stdout}`, /Removed command: autoresearch/i);
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

  it('fails for the removed research alias', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-research-removed-'));
    try {
      const result = runNana(cwd, ['research', '--help']);
      assert.notEqual(result.status, 0, result.stderr || result.stdout);
      assert.match(`${result.stderr}\n${result.stdout}`, /Removed command: research/i);
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });
});
