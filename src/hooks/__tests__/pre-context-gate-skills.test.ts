import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));

const ralplanSkill = readFileSync(
  join(__dirname, '../../../skills/ralplan/SKILL.md'),
  'utf-8',
);
const autopilotSkill = readFileSync(
  join(__dirname, '../../../skills/autopilot/SKILL.md'),
  'utf-8',
);
const plannerPrompt = readFileSync(
  join(__dirname, '../../../prompts/planner.md'),
  'utf-8',
);

describe('pre-context gate guidance in surviving planning/execution-heavy surfaces', () => {
  it('ralplan documents required context snapshot intake', () => {
    assert.match(ralplanSkill, /Pre-context Intake/i);
    assert.match(ralplanSkill, /\.nana\/context\/\{slug\}-\{timestamp\}\.md/);
    assert.match(ralplanSkill, /\$deep-interview\s+--quick/i);
  });

  it('autopilot documents required pre-context intake before expansion', () => {
    assert.match(autopilotSkill, /Pre-context Intake/i);
    assert.match(autopilotSkill, /\.nana\/context\/\{slug\}-\{timestamp\}\.md/);
    assert.match(autopilotSkill, /run `explore` first/i);
    assert.match(autopilotSkill, /\$deep-interview\s+--quick/i);
  });

  it('planner prompt keeps the same explore-first planning gate', () => {
    assert.match(plannerPrompt, /USE_NANA_EXPLORE_CMD/i);
    assert.match(plannerPrompt, /prefer `nana explore`/i);
    assert.match(plannerPrompt, /fall back normally|richer normal path/i);
  });
});
