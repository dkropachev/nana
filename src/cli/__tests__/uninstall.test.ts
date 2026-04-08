import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { tmpdir } from 'node:os';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

function runNana(
  cwd: string,
  argv: string[],
  envOverrides: Record<string, string> = {}
): { status: number | null; stdout: string; stderr: string; error: string } {
  const testDir = dirname(fileURLToPath(import.meta.url));
  const repoRoot = join(testDir, '..', '..', '..');
  const nanaBin = join(repoRoot, 'dist', 'cli', 'nana.js');
  const resolvedHome = envOverrides.HOME ?? process.env.HOME;
  const result = spawnSync(process.execPath, [nanaBin, ...argv], {
    cwd,
    encoding: 'utf-8',
    env: {
      ...process.env,
      ...(resolvedHome && !envOverrides.CODEX_HOME ? { CODEX_HOME: join(resolvedHome, '.codex') } : {}),
      ...envOverrides,
    },
  });
  return {
    status: result.status,
    stdout: result.stdout || '',
    stderr: result.stderr || '',
    error: result.error?.message || '',
  };
}

function shouldSkipForSpawnPermissions(err: string): boolean {
  return typeof err === 'string' && /(EPERM|EACCES)/i.test(err);
}

/** Build a realistic NANA config.toml for testing */
function buildNanaConfig(): string {
  return [
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
    '# NANA State Management MCP Server',
    '[mcp_servers.nana_state]',
    'command = "node"',
    'args = ["/path/to/state-server.js"]',
    'enabled = true',
    'startup_timeout_sec = 5',
    '',
    '# NANA Project Memory MCP Server',
    '[mcp_servers.nana_memory]',
    'command = "node"',
    'args = ["/path/to/memory-server.js"]',
    'enabled = true',
    'startup_timeout_sec = 5',
    '',
    '# NANA Code Intelligence MCP Server',
    '[mcp_servers.nana_code_intel]',
    'command = "node"',
    'args = ["/path/to/code-intel-server.js"]',
    'enabled = true',
    'startup_timeout_sec = 10',
    '',
    '# NANA Trace MCP Server',
    '[mcp_servers.nana_trace]',
    'command = "node"',
    'args = ["/path/to/trace-server.js"]',
    'enabled = true',
    'startup_timeout_sec = 5',
    '',
    '[agents.executor]',
    'description = "Code implementation"',
    'config_file = "/path/to/executor.toml"',
    '',
    '# NANA TUI StatusLine (Codex CLI v0.101.0+)',
    '[tui]',
    'status_line = ["model-with-reasoning", "git-branch"]',
    '',
    '# ============================================================',
    '# End nana',
    '',
  ].join('\n');
}

/** Build a config with NANA entries mixed with user entries */

function buildConfigWithSeededModelContext(): string {
  return [
    '# nana top-level settings (must be before any [table])',
    'notify = ["node", "/path/to/notify-hook.js"]',
    'model_reasoning_effort = "high"',
    'developer_instructions = "You have nana installed."',
    'model = "gpt-5.4"',
    'model_context_window = 1000000',
    'model_auto_compact_token_limit = 900000',
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
    'enabled = true',
    '',
    '# ============================================================',
    '# End nana',
    '',
  ].join('\n');
}

function buildMixedConfig(): string {
  return [
    '# User settings',
    'model = "o4-mini"',
    '',
    '# nana top-level settings (must be before any [table])',
    'notify = ["node", "/path/to/notify-hook.js"]',
    'model_reasoning_effort = "high"',
    'developer_instructions = "You have nana installed."',
    '',
    '[features]',
    'multi_agent = true',
    'child_agents_md = true',
    'web_search = true',
    '',
    '[mcp_servers.user_custom]',
    'command = "custom"',
    'args = ["--flag"]',
    '',
    '# ============================================================',
    '# nana (NANA) Configuration',
    '# Managed by nana setup - manual edits preserved on next setup',
    '# ============================================================',
    '',
    '[mcp_servers.nana_state]',
    'command = "node"',
    'args = ["/path/to/state-server.js"]',
    'enabled = true',
    '',
    '[mcp_servers.nana_memory]',
    'command = "node"',
    'args = ["/path/to/memory-server.js"]',
    'enabled = true',
    '',
    '[mcp_servers.nana_code_intel]',
    'command = "node"',
    'args = ["/path/to/code-intel-server.js"]',
    'enabled = true',
    '',
    '[mcp_servers.nana_trace]',
    'command = "node"',
    'args = ["/path/to/trace-server.js"]',
    'enabled = true',
    '',
    '[agents.executor]',
    'description = "Code implementation"',
    'config_file = "/path/to/executor.toml"',
    '',
    '[tui]',
    'status_line = ["model-with-reasoning"]',
    '',
    '# ============================================================',
    '# End nana',
    '',
  ].join('\n');
}

describe('nana uninstall', () => {
  it('removes NANA block from config.toml with --dry-run', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      const codexDir = join(home, '.codex');
      await mkdir(codexDir, { recursive: true });
      await writeFile(join(codexDir, 'config.toml'), buildNanaConfig());

      const res = runNana(wd, ['uninstall', '--dry-run'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.match(res.stdout, /dry-run mode/);
      assert.match(res.stdout, /NANA configuration block/);
      assert.match(res.stdout, /nana_state/);

      // Config should NOT have been modified
      const config = await readFile(join(codexDir, 'config.toml'), 'utf-8');
      assert.match(config, /nana \(NANA\) Configuration/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('removes NANA block from config.toml', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      const codexDir = join(home, '.codex');
      await mkdir(codexDir, { recursive: true });
      await writeFile(join(codexDir, 'config.toml'), buildNanaConfig());

      const res = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.match(res.stdout, /Removed NANA configuration block/);

      const config = await readFile(join(codexDir, 'config.toml'), 'utf-8');
      assert.doesNotMatch(config, /nana \(NANA\) Configuration/);
      assert.doesNotMatch(config, /nana_state/);
      assert.doesNotMatch(config, /nana_memory/);
      assert.doesNotMatch(config, /nana_code_intel/);
      assert.doesNotMatch(config, /nana_trace/);
      assert.doesNotMatch(config, /\[agents\.executor\]/);
      assert.doesNotMatch(config, /\[tui\]/);
      assert.doesNotMatch(config, /notify\s*=/);
      assert.doesNotMatch(config, /model_reasoning_effort\s*=/);
      assert.doesNotMatch(config, /developer_instructions\s*=/);
      assert.doesNotMatch(config, /multi_agent\s*=/);
      assert.doesNotMatch(config, /child_agents_md\s*=/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });


  it('preserves user config entries when removing NANA', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      const codexDir = join(home, '.codex');
      await mkdir(codexDir, { recursive: true });
      await writeFile(join(codexDir, 'config.toml'), buildMixedConfig());

      const res = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);

      const config = await readFile(join(codexDir, 'config.toml'), 'utf-8');
      // User settings preserved
      assert.match(config, /model = "o4-mini"/);
      assert.match(config, /\[mcp_servers\.user_custom\]/);
      assert.match(config, /web_search = true/);
      // NANA entries removed
      assert.doesNotMatch(config, /nana_state/);
      assert.doesNotMatch(config, /nana_memory/);
      assert.doesNotMatch(config, /notify\s*=.*node/);
      assert.doesNotMatch(config, /multi_agent/);
      assert.doesNotMatch(config, /child_agents_md/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });


  it('preserves seeded model/context keys during uninstall', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      const codexDir = join(home, '.codex');
      await mkdir(codexDir, { recursive: true });
      await writeFile(join(codexDir, 'config.toml'), buildConfigWithSeededModelContext());

      const res = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);

      const config = await readFile(join(codexDir, 'config.toml'), 'utf-8');
      assert.match(config, /^model = "gpt-5\.4"$/m);
      assert.match(config, /^model_context_window = 1000000$/m);
      assert.match(config, /^model_auto_compact_token_limit = 900000$/m);
      assert.doesNotMatch(config, /notify\s*=/);
      assert.doesNotMatch(config, /model_reasoning_effort\s*=/);
      assert.doesNotMatch(config, /developer_instructions\s*=/);
      assert.doesNotMatch(config, /nana \(NANA\) Configuration/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('--keep-config skips config.toml cleanup', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      const codexDir = join(home, '.codex');
      await mkdir(codexDir, { recursive: true });
      await writeFile(join(codexDir, 'config.toml'), buildNanaConfig());

      const res = runNana(wd, ['uninstall', '--keep-config'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.match(res.stdout, /--keep-config/);

      // Config should NOT have been modified
      const config = await readFile(join(codexDir, 'config.toml'), 'utf-8');
      assert.match(config, /nana \(NANA\) Configuration/);
      assert.match(config, /nana_state/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('--purge removes .nana/ cache directory', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      await mkdir(home, { recursive: true });
      // Create .nana/ directory with some files
      const nanaDir = join(wd, '.nana');
      await mkdir(join(nanaDir, 'state'), { recursive: true });
      await writeFile(join(nanaDir, 'setup-scope.json'), JSON.stringify({ scope: 'user' }));
      await writeFile(join(nanaDir, 'notepad.md'), '# notes');
      await writeFile(join(nanaDir, 'state', 'ralph-state.json'), '{}');

      const res = runNana(wd, ['uninstall', '--keep-config', '--purge'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.match(res.stdout, /\.nana\/ cache directory/);

      assert.equal(existsSync(nanaDir), false, '.nana/ directory should be removed');
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('works with project scope', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      await mkdir(home, { recursive: true });

      // Create project-scoped setup
      const nanaDir = join(wd, '.nana');
      const codexDir = join(wd, '.codex');
      await mkdir(nanaDir, { recursive: true });
      await mkdir(join(codexDir, 'prompts'), { recursive: true });
      await writeFile(join(nanaDir, 'setup-scope.json'), JSON.stringify({ scope: 'project' }));
      await writeFile(join(codexDir, 'config.toml'), buildNanaConfig());
      // Install a prompt
      await writeFile(join(codexDir, 'prompts', 'executor.md'), '# executor');

      const res = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.match(res.stdout, /Resolved scope: project/);

      // Project-local config.toml should be cleaned
      const config = await readFile(join(codexDir, 'config.toml'), 'utf-8');
      assert.doesNotMatch(config, /nana \(NANA\) Configuration/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('handles missing config.toml gracefully', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      await mkdir(home, { recursive: true });

      const res = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.match(res.stdout, /Nothing to remove/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('shows summary of what was removed', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      const codexDir = join(home, '.codex');
      await mkdir(codexDir, { recursive: true });
      await writeFile(join(codexDir, 'config.toml'), buildNanaConfig());

      const res = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.match(res.stdout, /Uninstall summary/);
      assert.match(res.stdout, /MCP servers: nana_state, nana_memory, nana_code_intel, nana_trace/);
      assert.match(res.stdout, /Agent entries: 1/);
      assert.match(res.stdout, /TUI status line section/);
      assert.match(res.stdout, /Top-level keys/);
      assert.match(res.stdout, /Feature flags/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('--dry-run --purge does not actually remove .nana/ directory', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      await mkdir(home, { recursive: true });
      const nanaDir = join(wd, '.nana');
      await mkdir(join(nanaDir, 'state'), { recursive: true });
      await writeFile(join(nanaDir, 'setup-scope.json'), JSON.stringify({ scope: 'user' }));
      await writeFile(join(nanaDir, 'notepad.md'), '# notes');

      const res = runNana(wd, ['uninstall', '--keep-config', '--purge', '--dry-run'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.match(res.stdout, /dry-run mode/);
      assert.match(res.stdout, /\.nana\/ cache directory/);

      // .nana/ should still exist
      assert.equal(existsSync(nanaDir), true, '.nana/ should NOT be removed in dry-run');
      assert.equal(existsSync(join(nanaDir, 'notepad.md')), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('second uninstall run reports nothing to remove (idempotent)', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      const codexDir = join(home, '.codex');
      await mkdir(codexDir, { recursive: true });
      await writeFile(join(codexDir, 'config.toml'), buildNanaConfig());

      const first = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(first.error)) return;
      assert.equal(first.status, 0, first.stderr || first.stdout);
      assert.match(first.stdout, /Removed NANA configuration block/);

      const second = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(second.error)) return;
      assert.equal(second.status, 0, second.stderr || second.stdout);
      assert.match(second.stdout, /Nothing to remove/);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('does not delete user AGENTS.md that merely mentions nana', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      await mkdir(home, { recursive: true });
      const userAgentsMd = '# My Agents\n\nDo not use nana for this project.\n';
      await writeFile(join(wd, 'AGENTS.md'), userAgentsMd);

      const res = runNana(wd, ['uninstall'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);

      // User AGENTS.md should be preserved
      assert.equal(existsSync(join(wd, 'AGENTS.md')), true);
      const content = await readFile(join(wd, 'AGENTS.md'), 'utf-8');
      assert.equal(content, userAgentsMd);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('removes managed user-scope AGENTS.md from CODEX_HOME', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      const codexHome = join(home, '.codex');
      await mkdir(codexHome, { recursive: true });
      await mkdir(join(wd, '.nana'), { recursive: true });
      await writeFile(join(wd, '.nana', 'setup-scope.json'), JSON.stringify({ scope: 'user' }));
      await writeFile(
        join(codexHome, 'AGENTS.md'),
        '<!-- AUTONOMY DIRECTIVE — DO NOT REMOVE -->\n'
          + 'YOU ARE AN AUTONOMOUS CODING AGENT. EXECUTE TASKS TO COMPLETION WITHOUT ASKING FOR PERMISSION.\n'
          + 'DO NOT STOP TO ASK "SHOULD I PROCEED?" — PROCEED. DO NOT WAIT FOR CONFIRMATION ON OBVIOUS NEXT STEPS.\n'
          + 'IF BLOCKED, TRY AN ALTERNATIVE APPROACH. ONLY ASK WHEN TRULY AMBIGUOUS OR DESTRUCTIVE.\n'
          + '<!-- END AUTONOMY DIRECTIVE -->\n'
          + '<!-- nana:generated:agents-md -->\n'
          + '# nana - Intelligent Multi-Agent Orchestration\n',
      );

      const res = runNana(wd, ['uninstall', '--keep-config'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);
      assert.equal(existsSync(join(codexHome, 'AGENTS.md')), false);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('removes setup-scope.json and hud-config.json without --purge', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-uninstall-'));
    try {
      const home = join(wd, 'home');
      await mkdir(home, { recursive: true });
      const nanaDir = join(wd, '.nana');
      await mkdir(nanaDir, { recursive: true });
      await writeFile(join(nanaDir, 'setup-scope.json'), JSON.stringify({ scope: 'user' }));
      await writeFile(join(nanaDir, 'hud-config.json'), JSON.stringify({ preset: 'focused' }));
      await writeFile(join(nanaDir, 'notepad.md'), '# keep this');

      const res = runNana(wd, ['uninstall', '--keep-config'], { HOME: home });
      if (shouldSkipForSpawnPermissions(res.error)) return;
      assert.equal(res.status, 0, res.stderr || res.stdout);

      assert.equal(existsSync(join(nanaDir, 'setup-scope.json')), false);
      assert.equal(existsSync(join(nanaDir, 'hud-config.json')), false);
      // notepad.md should still exist (not purged)
      assert.equal(existsSync(join(nanaDir, 'notepad.md')), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });
});

describe('stripNanaFeatureFlags', () => {
  it('removes NANA feature flags and preserves user flags', async () => {
    const { stripNanaFeatureFlags } = await import('../../config/generator.js');

    const config = [
      '[features]',
      'multi_agent = true',
      'child_agents_md = true',
      'web_search = true',
      '',
    ].join('\n');

    const result = stripNanaFeatureFlags(config);
    assert.doesNotMatch(result, /multi_agent/);
    assert.doesNotMatch(result, /child_agents_md/);
    assert.match(result, /web_search = true/);
    assert.match(result, /\[features\]/);
  });

  it('removes [features] section if it becomes empty', async () => {
    const { stripNanaFeatureFlags } = await import('../../config/generator.js');

    const config = [
      '[features]',
      'multi_agent = true',
      'child_agents_md = true',
      '',
    ].join('\n');

    const result = stripNanaFeatureFlags(config);
    assert.doesNotMatch(result, /\[features\]/);
    assert.doesNotMatch(result, /multi_agent/);
  });

  it('handles config without [features] section', async () => {
    const { stripNanaFeatureFlags } = await import('../../config/generator.js');

    const config = 'model = "o4-mini"\n';
    const result = stripNanaFeatureFlags(config);
    assert.equal(result, config);
  });
});
