import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const visualVerdictSkill = readFileSync(join(__dirname, '../../../skills/visual-verdict/SKILL.md'), 'utf-8');
const rootAgents = readFileSync(join(__dirname, '../../../AGENTS.md'), 'utf-8');

describe('visual-verdict skill contract', () => {
  it('documents required JSON fields', () => {
    for (const field of ['"score"', '"verdict"', '"category_match"', '"differences"', '"suggestions"', '"reasoning"']) {
      assert.ok(visualVerdictSkill.includes(field), `missing field ${field}`);
    }
  });

  it('documents threshold and pixel diff guidance', () => {
    assert.match(visualVerdictSkill, /90\+/);
    assert.match(visualVerdictSkill, /pixel diff/i);
    assert.match(visualVerdictSkill, /pixelmatch/i);
  });
});

describe('visual loop guidance on surviving surfaces', () => {
  it('requires running $visual-verdict before the next edit', () => {
    assert.match(rootAgents, /\$visual-verdict/);
    assert.match(rootAgents, /every iteration before the next edit/i);
  });

  it('persists visual feedback to the ralph-progress ledger path', () => {
    assert.match(rootAgents, /ralph-progress\.json/);
    assert.match(rootAgents, /Persist verdict JSON/i);
  });
});
