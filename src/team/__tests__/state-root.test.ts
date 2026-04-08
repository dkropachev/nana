import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { resolveCanonicalTeamStateRoot } from '../state-root.js';

describe('state-root', () => {
  it('resolveCanonicalTeamStateRoot resolves to leader .nana/state', () => {
    assert.equal(
      resolveCanonicalTeamStateRoot('/tmp/demo/project'),
      '/tmp/demo/project/.nana/state',
    );
  });
});

