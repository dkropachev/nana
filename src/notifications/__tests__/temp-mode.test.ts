import { afterEach, beforeEach, describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { getNotificationConfig } from '../config.js';
import { shouldDispatchOpenClaw } from '../index.js';
import { resetOpenClawConfigCache } from '../../openclaw/config.js';

const ENV_KEYS = [
  'CODEX_HOME',
  'NANA_NOTIFY_TEMP',
  'NANA_NOTIFY_TEMP_CONTRACT',
  'NANA_NOTIFY_PROFILE',
  'NANA_DISCORD_WEBHOOK_URL',
  'NANA_DISCORD_NOTIFIER_BOT_TOKEN',
  'NANA_DISCORD_NOTIFIER_CHANNEL',
  'NANA_TELEGRAM_BOT_TOKEN',
  'NANA_TELEGRAM_CHAT_ID',
  'NANA_SLACK_WEBHOOK_URL',
  'NANA_OPENCLAW',
] as const;

let tempCodexHome: string;

async function writeCodexConfig(contents: unknown): Promise<void> {
  await mkdir(tempCodexHome, { recursive: true });
  await writeFile(join(tempCodexHome, '.nana-config.json'), JSON.stringify(contents, null, 2));
}

function clearEnv(): void {
  for (const key of ENV_KEYS) {
    delete process.env[key];
  }
}

describe('notification temp mode', () => {
  beforeEach(async () => {
    clearEnv();
    resetOpenClawConfigCache();
    tempCodexHome = await mkdtemp(join(tmpdir(), 'nana-notify-temp-'));
    process.env.CODEX_HOME = tempCodexHome;
  });

  afterEach(async () => {
    clearEnv();
    resetOpenClawConfigCache();
    if (tempCodexHome) {
      await rm(tempCodexHome, { recursive: true, force: true });
    }
  });

  it('temp contract bypasses persistent file/profile routing', async () => {
    await writeCodexConfig({
      notifications: {
        enabled: true,
        defaultProfile: 'file-profile',
        profiles: {
          'file-profile': {
            enabled: true,
            discord: { enabled: true, webhookUrl: 'https://discord.com/api/webhooks/file' },
          },
        },
      },
    });
    process.env.NANA_NOTIFY_PROFILE = 'file-profile';
    process.env.NANA_SLACK_WEBHOOK_URL = 'https://hooks.slack.com/services/temp-only';
    process.env.NANA_NOTIFY_TEMP_CONTRACT = JSON.stringify({
      active: true,
      selectors: ['slack'],
      canonicalSelectors: ['slack'],
      warnings: [],
      source: 'cli',
    });

    const config = getNotificationConfig();
    assert.ok(config);
    assert.equal(config.enabled, true);
    assert.equal(config.slack?.enabled, true);
    assert.equal(config.discord, undefined);
  });

  it('temp contract with no valid configured provider disables dispatch config', () => {
    process.env.NANA_NOTIFY_TEMP_CONTRACT = JSON.stringify({
      active: true,
      selectors: ['telegram'],
      canonicalSelectors: ['telegram'],
      warnings: [],
      source: 'cli',
    });

    const config = getNotificationConfig();
    assert.ok(config);
    assert.equal(config.enabled, false);
  });

  it('temp mode does not leak persistent openclaw/custom alias routing unless selected', async () => {
    await writeCodexConfig({
      notifications: {
        enabled: true,
        custom_cli_command: { enabled: true, command: 'echo test' },
        openclaw: {
          enabled: true,
          gateways: { g: { type: 'command', command: 'echo hi' } },
          hooks: { 'session-end': { enabled: true, gateway: 'g', instruction: 'i' } },
        },
      },
    });
    process.env.NANA_OPENCLAW = '1';
    process.env.NANA_NOTIFY_TEMP_CONTRACT = JSON.stringify({
      active: true,
      selectors: ['discord'],
      canonicalSelectors: ['discord'],
      warnings: [],
      source: 'cli',
    });

    const config = getNotificationConfig();
    assert.ok(config);
    assert.equal(config.openclaw, undefined);
  });

  it('temp mode enables openclaw config only when explicitly selected', () => {
    process.env.NANA_OPENCLAW = '1';
    process.env.NANA_NOTIFY_TEMP_CONTRACT = JSON.stringify({
      active: true,
      selectors: ['openclaw:gateway-main'],
      canonicalSelectors: ['openclaw:gateway-main'],
      warnings: [],
      source: 'providers',
    });

    const config = getNotificationConfig();
    assert.ok(config);
    assert.equal(config.openclaw?.enabled, true);
    assert.equal(config.enabled, true);
  });

  it('shouldDispatchOpenClaw enforces temp-mode explicit selection and gateway matching', async () => {
    process.env.NANA_OPENCLAW = '1';
    await writeCodexConfig({
      notifications: {
        enabled: true,
        openclaw: {
          enabled: true,
          gateways: { g1: { type: 'command', command: 'echo hi' } },
          hooks: { 'session-end': { enabled: true, gateway: 'g1', instruction: 'i' } },
        },
      },
    });

    const activeNoOpenClaw = {
      active: true,
      selectors: ['discord'],
      canonicalSelectors: ['discord'],
      warnings: [],
      source: 'cli' as const,
    };
    const activeWithOpenClaw = {
      active: true,
      selectors: ['openclaw:g1'],
      canonicalSelectors: ['openclaw:g1'],
      warnings: [],
      source: 'cli' as const,
    };

    const activeWithCustomGateway = {
      active: true,
      selectors: ['custom:g1'],
      canonicalSelectors: ['custom:g1'],
      warnings: [],
      source: 'cli' as const,
    };

    const activeWithWrongGateway = {
      active: true,
      selectors: ['custom:other'],
      canonicalSelectors: ['custom:other'],
      warnings: [],
      source: 'cli' as const,
    };

    assert.equal(
      await shouldDispatchOpenClaw('session-end', activeNoOpenClaw, process.env),
      false,
    );
    assert.equal(
      await shouldDispatchOpenClaw('session-end', activeWithOpenClaw, process.env),
      true,
    );
    assert.equal(
      await shouldDispatchOpenClaw('session-end', activeWithCustomGateway, process.env),
      true,
    );
    assert.equal(
      await shouldDispatchOpenClaw('session-end', activeWithWrongGateway, process.env),
      false,
    );
    assert.equal(
      await shouldDispatchOpenClaw('session-end', null, process.env),
      true,
    );
    assert.equal(
      await shouldDispatchOpenClaw('session-end', activeWithOpenClaw, { NANA_OPENCLAW: '0', CODEX_HOME: tempCodexHome }),
      false,
    );
  });
});
