import { join, resolve } from 'path';

/**
 * Resolve the canonical NANA team state root for a leader working directory.
 */
export function resolveCanonicalTeamStateRoot(leaderCwd: string): string {
  return resolve(join(leaderCwd, '.nana', 'state'));
}

