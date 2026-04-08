import { existsSync } from 'fs';
import { readFile } from 'fs/promises';
import type {
  HookPluginNanaHudState,
  HookPluginNanaNotifyFallbackState,
  HookPluginNanaSessionState,
  HookPluginNanaUpdateCheckState,
  HookPluginSdk,
} from '../types.js';
import { nanaRootStateFilePath } from './paths.js';

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

async function readNanaStateFile<T extends Record<string, unknown>>(
  path: string,
  normalize?: (value: Record<string, unknown>) => T | null,
): Promise<T | null> {
  if (!existsSync(path)) return null;
  try {
    const parsed = JSON.parse(await readFile(path, 'utf-8')) as unknown;
    if (!isRecord(parsed)) return null;
    return normalize ? normalize(parsed) : parsed as T;
  } catch {
    return null;
  }
}

function normalizeSessionState(value: Record<string, unknown>): HookPluginNanaSessionState | null {
  return typeof value.session_id === 'string' && value.session_id.trim()
    ? value as HookPluginNanaSessionState
    : null;
}

export function createHookPluginNanaApi(cwd: string): HookPluginSdk['nana'] {
  return {
    session: {
      read: () => readNanaStateFile<HookPluginNanaSessionState>(
        nanaRootStateFilePath(cwd, 'session.json'),
        normalizeSessionState,
      ),
    },
    hud: {
      read: () => readNanaStateFile<HookPluginNanaHudState>(
        nanaRootStateFilePath(cwd, 'hud-state.json'),
      ),
    },
    notifyFallback: {
      read: () => readNanaStateFile<HookPluginNanaNotifyFallbackState>(
        nanaRootStateFilePath(cwd, 'notify-fallback-state.json'),
      ),
    },
    updateCheck: {
      read: () => readNanaStateFile<HookPluginNanaUpdateCheckState>(
        nanaRootStateFilePath(cwd, 'update-check.json'),
      ),
    },
  };
}
