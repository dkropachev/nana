import { describe, it, mock } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  detectKeywords,
  detectPrimaryKeyword,
  recordSkillActivation,
  SKILL_ACTIVE_STATE_FILE,
  DEEP_INTERVIEW_BLOCKED_APPROVAL_INPUTS,
  DEEP_INTERVIEW_INPUT_LOCK_MESSAGE,
} from '../keyword-detector.js';
import { isUnderspecifiedForExecution, applyRalplanGate } from '../keyword-detector.js';
import { KEYWORD_TRIGGER_DEFINITIONS } from '../keyword-registry.js';

describe('keyword detector surviving workflow compatibility', () => {
  it('keeps explicit $skill order in detectKeywords results (left-to-right)', () => {
    const matches = detectKeywords('$analyze $autopilot $code-review now');
    assert.deepEqual(matches.map((m) => m.skill).slice(0, 3), ['analyze', 'autopilot', 'code-review']);
  });

  it('de-duplicates repeated explicit skill tokens', () => {
    const matches = detectKeywords('$analyze $analyze root cause');
    assert.deepEqual(matches.map((m) => m.skill), ['analyze']);
  });

  it('limits explicit multi-skill invocation to the first contiguous $skill block', () => {
    const matches = detectKeywords('$ralplan Fix issue #1030 and ensure other directives ($autopilot, $deep-interview) are not affected');
    assert.deepEqual(matches.map((m) => m.skill), ['ralplan']);
  });

  it('does not merge implicit keyword matches when an explicit $skill is present', () => {
    const matches = detectKeywords('please run $autopilot and then analyze the result');
    assert.deepEqual(matches.map((m) => m.skill), ['autopilot']);
  });

  it('does not auto-detect keywords for explicit /prompts invocation without $skills', () => {
    const matches = detectKeywords('/prompts:architect analyze this issue');
    assert.deepEqual(matches, []);
    const primary = detectPrimaryKeyword('/prompts:architect analyze this issue');
    assert.equal(primary, null);
  });

  it('treats /prompts invocation with trailing punctuation as explicit command', () => {
    const matches = detectKeywords('/prompts:architect, analyze this issue');
    assert.deepEqual(matches, []);
    const primary = detectPrimaryKeyword('/prompts:architect, analyze this issue');
    assert.equal(primary, null);
  });

  it('maps analyze keyword to analyze skill', () => {
    const match = detectPrimaryKeyword('please analyze this workflow');
    assert.ok(match);
    assert.equal(match.skill, 'analyze');
  });

  it('maps code-review keyword variants to code-review skill', () => {
    const hyphen = detectPrimaryKeyword('run code-review before merge');
    assert.ok(hyphen);
    assert.equal(hyphen.skill, 'code-review');

    const spaced = detectPrimaryKeyword('please do a code review');
    assert.ok(spaced);
    assert.equal(spaced.skill, 'code-review');
  });

  it('supports explicit multi-skill invocation by prioritizing left-most $skill', () => {
    const match = detectPrimaryKeyword('$autopilot $analyze $code-review run now');
    assert.ok(match);
    assert.equal(match.skill, 'autopilot');
    assert.equal(match.keyword.toLowerCase(), '$autopilot');
  });

  it('maps "deep interview" phrase to deep-interview skill', () => {
    const match = detectPrimaryKeyword('please run a deep interview before planning');

    assert.ok(match);
    assert.equal(match.skill, 'deep-interview');
    assert.equal(match.keyword.toLowerCase(), 'deep interview');
  });

  it('maps "gather requirements" to deep-interview skill', () => {
    const match = detectPrimaryKeyword('let us gather requirements first');

    assert.ok(match);
    assert.equal(match.skill, 'deep-interview');
    assert.equal(match.keyword.toLowerCase(), 'gather requirements');
  });

  it('maps "ouroboros" to deep-interview skill', () => {
    const match = detectPrimaryKeyword('please run ouroboros before planning');

    assert.ok(match);
    assert.equal(match.skill, 'deep-interview');
    assert.equal(match.keyword.toLowerCase(), 'ouroboros');
  });

  it('maps "interview me" to deep-interview skill', () => {
    const match = detectPrimaryKeyword('interview me before we start implementation');

    assert.ok(match);
    assert.equal(match.skill, 'deep-interview');
    assert.equal(match.keyword.toLowerCase(), 'interview me');
  });

  it('maps "don\'t assume" to deep-interview skill', () => {
    const match = detectPrimaryKeyword("don't assume anything yet");

    assert.ok(match);
    assert.equal(match.skill, 'deep-interview');
    assert.equal(match.keyword.toLowerCase(), "don't assume");
  });

  it('prefers "deep interview" over "interview" for deterministic longest-match behavior', () => {
    const match = detectPrimaryKeyword('deep interview this request first');

    assert.ok(match);
    assert.equal(match.skill, 'deep-interview');
    assert.equal(match.keyword.toLowerCase(), 'deep interview');
  });
});

describe('keyword registry coverage', () => {
  it('includes surviving workflow aliases in runtime keyword registry', () => {
    const registryKeywords = new Set(KEYWORD_TRIGGER_DEFINITIONS.map((v) => v.keyword.toLowerCase()));
    assert.ok(registryKeywords.has('autopilot'));
    assert.ok(registryKeywords.has('build me'));
    assert.ok(registryKeywords.has('analyze'));
    assert.ok(registryKeywords.has('investigate'));
    assert.ok(registryKeywords.has('code review'));
    assert.ok(registryKeywords.has('code-review'));
    assert.ok(registryKeywords.has('ultrawork'));
    assert.ok(registryKeywords.has('parallel'));
    assert.ok(registryKeywords.has('ouroboros'));
    assert.ok(registryKeywords.has("don't assume"));
    assert.ok(registryKeywords.has('interview me'));
  });
});

describe('keyword detector skill-active-state lifecycle', () => {
  it('writes skill-active-state.json with planning phase when keyword activates', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-keyword-state-'));
    const stateDir = join(cwd, '.nana', 'state');
    try {
      await mkdir(stateDir, { recursive: true });
      const result = await recordSkillActivation({
        stateDir,
        text: 'please run autopilot and keep going',
        sessionId: 'sess-1',
        threadId: 'thread-1',
        turnId: 'turn-1',
        nowIso: '2026-02-25T00:00:00.000Z',
      });

      assert.ok(result);
      assert.equal(result.skill, 'autopilot');
      assert.equal(result.phase, 'planning');
      assert.equal(result.active, true);

      const persisted = JSON.parse(await readFile(join(stateDir, SKILL_ACTIVE_STATE_FILE), 'utf-8')) as {
        skill: string;
        phase: string;
        active: boolean;
      };
      assert.equal(persisted.skill, 'autopilot');
      assert.equal(persisted.phase, 'planning');
      assert.equal(persisted.active, true);
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

  it('acquires a deep-interview input lock immediately on activation', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-keyword-state-deep-interview-'));
    const stateDir = join(cwd, '.nana', 'state');
    try {
      await mkdir(stateDir, { recursive: true });
      const result = await recordSkillActivation({
        stateDir,
        text: 'please run a deep interview before planning',
        nowIso: '2026-02-25T00:00:00.000Z',
      });

      assert.ok(result);
      assert.equal(result.skill, 'deep-interview');
      assert.equal(result.input_lock?.active, true);
      assert.deepEqual(result.input_lock?.blocked_inputs, [...DEEP_INTERVIEW_BLOCKED_APPROVAL_INPUTS]);
      assert.equal(result.input_lock?.blocked_inputs.includes('next i should'), true);
      assert.equal(result.input_lock?.message, DEEP_INTERVIEW_INPUT_LOCK_MESSAGE);
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

  it('releases the deep-interview input lock on abort via cancel keyword', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-keyword-state-deep-interview-abort-'));
    const stateDir = join(cwd, '.nana', 'state');
    try {
      await mkdir(stateDir, { recursive: true });
      await recordSkillActivation({
        stateDir,
        text: 'please run deep interview',
        nowIso: '2026-02-25T00:00:00.000Z',
      });

      const result = await recordSkillActivation({
        stateDir,
        text: 'abort now',
        nowIso: '2026-02-25T00:05:00.000Z',
      });

      assert.ok(result);
      assert.equal(result.skill, 'deep-interview');
      assert.equal(result.active, false);
      assert.equal(result.phase, 'completing');
      assert.equal(result.input_lock?.active, false);
      assert.equal(result.input_lock?.released_at, '2026-02-25T00:05:00.000Z');
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

  it('does not write state when no keyword is present', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-keyword-state-none-'));
    const stateDir = join(cwd, '.nana', 'state');
    try {
      await mkdir(stateDir, { recursive: true });
      const result = await recordSkillActivation({
        stateDir,
        text: 'hello there, how are you',
      });
      assert.equal(result, null);
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

  it('emits a warning when skill-active-state persistence fails', async () => {
    const warnings: unknown[][] = [];
    mock.method(console, 'warn', (...args: unknown[]) => {
      warnings.push(args);
    });

    const result = await recordSkillActivation({
      stateDir: join('/definitely-missing', 'nested', 'state-dir'),
      text: 'please run autopilot',
      nowIso: '2026-02-25T00:00:00.000Z',
    });

    assert.ok(result);
    assert.equal(result.skill, 'autopilot');
    assert.equal(warnings.length, 1);
    assert.match(String(warnings[0][0]), /failed to persist keyword activation state/);
  });

  it('preserves activated_at for same-skill continuation', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-keyword-state-continuation-'));
    const stateDir = join(cwd, '.nana', 'state');
    const statePath = join(stateDir, SKILL_ACTIVE_STATE_FILE);
    try {
      await mkdir(stateDir, { recursive: true });
      await writeFile(
        statePath,
        JSON.stringify({
          version: 1,
          active: true,
          skill: 'autopilot',
          keyword: 'autopilot',
          phase: 'planning',
          activated_at: '2026-02-25T00:00:00.000Z',
          updated_at: '2026-02-25T00:10:00.000Z',
          source: 'keyword-detector',
        }),
      );

      const result = await recordSkillActivation({
        stateDir,
        text: 'autopilot keep going',
        nowIso: '2026-02-26T00:00:00.000Z',
      });

      assert.ok(result);
      assert.equal(result.activated_at, '2026-02-25T00:00:00.000Z');
      assert.equal(result.updated_at, '2026-02-26T00:00:00.000Z');
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

  it('resets activated_at when skill changes', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-keyword-state-skill-switch-'));
    const stateDir = join(cwd, '.nana', 'state');
    const statePath = join(stateDir, SKILL_ACTIVE_STATE_FILE);
    try {
      await mkdir(stateDir, { recursive: true });
      await writeFile(
        statePath,
        JSON.stringify({
          version: 1,
          active: true,
          skill: 'autopilot',
          keyword: 'autopilot',
          phase: 'planning',
          activated_at: '2026-02-25T00:00:00.000Z',
          updated_at: '2026-02-25T00:10:00.000Z',
          source: 'keyword-detector',
        }),
      );

      const result = await recordSkillActivation({
        stateDir,
        text: 'please analyze this now',
        nowIso: '2026-02-26T00:00:00.000Z',
      });

      assert.ok(result);
      assert.equal(result.skill, 'analyze');
      assert.equal(result.activated_at, '2026-02-26T00:00:00.000Z');
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

  it('resets activated_at when keyword changes within the same skill', async () => {
    const cwd = await mkdtemp(join(tmpdir(), 'nana-keyword-state-keyword-switch-'));
    const stateDir = join(cwd, '.nana', 'state');
    const statePath = join(stateDir, SKILL_ACTIVE_STATE_FILE);
    try {
      await mkdir(stateDir, { recursive: true });
      await writeFile(
        statePath,
        JSON.stringify({
          version: 1,
          active: true,
          skill: 'autopilot',
          keyword: 'autopilot',
          phase: 'planning',
          activated_at: '2026-02-25T00:00:00.000Z',
          updated_at: '2026-02-25T00:10:00.000Z',
          source: 'keyword-detector',
        }),
      );

      const result = await recordSkillActivation({
        stateDir,
        text: 'I want a starter API',
        nowIso: '2026-02-26T00:00:00.000Z',
      });

      assert.ok(result);
      assert.equal(result.skill, 'autopilot');
      assert.notEqual(result.keyword.toLowerCase(), 'autopilot');
      assert.equal(result.activated_at, '2026-02-26T00:00:00.000Z');
    } finally {
      await rm(cwd, { recursive: true, force: true });
    }
  });

});


describe('isUnderspecifiedForExecution', () => {
  it('flags vague prompt with no files or functions', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot fix this'), true);
  });

  it('flags short vague prompt', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot build the app'), true);
  });

  it('flags prompt with only keyword and generic words', () => {
    assert.equal(isUnderspecifiedForExecution('ultrawork improve performance'), true);
  });

  it('passes prompt with a file path reference', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot fix src/hooks/bridge.ts'), false);
  });

  it('passes prompt with a file extension reference', () => {
    assert.equal(isUnderspecifiedForExecution('fix the bug in auth.ts'), false);
  });

  it('passes prompt with a directory/file path', () => {
    assert.equal(isUnderspecifiedForExecution('update src/hooks/emulator.ts'), false);
  });

  it('passes prompt with a camelCase symbol', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot fix processKeywordDetector'), false);
  });

  it('passes prompt with a PascalCase symbol', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot update UserModel'), false);
  });

  it('passes prompt with snake_case symbol', () => {
    assert.equal(isUnderspecifiedForExecution('fix user_model validation'), false);
  });

  it('passes prompt with an issue number', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot implement #42'), false);
  });

  it('passes prompt with numbered steps', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot do:\n1. Add input validation\n2. Write tests\n3. Update README'), false);
  });

  it('passes prompt with acceptance criteria keyword', () => {
    assert.equal(isUnderspecifiedForExecution('add login - acceptance criteria: user sees error on bad password'), false);
  });

  it('passes prompt with a specific error reference', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot fix TypeError in auth handler'), false);
  });

  it('passes with force: escape hatch prefix', () => {
    assert.equal(isUnderspecifiedForExecution('force: autopilot refactor the auth module'), false);
  });

  it('passes with ! escape hatch prefix', () => {
    assert.equal(isUnderspecifiedForExecution('! autopilot optimize everything'), false);
  });

  it('returns true for empty string', () => {
    assert.equal(isUnderspecifiedForExecution(''), true);
  });

  it('returns true for whitespace only', () => {
    assert.equal(isUnderspecifiedForExecution('   '), true);
  });

  it('passes prompt with test runner command', () => {
    assert.equal(isUnderspecifiedForExecution('autopilot npm test && fix failures'), false);
  });

  it('passes longer prompt that exceeds word threshold', () => {
    // 16+ effective words without specific signals → passes (not underspecified by word count)
    const longVague = 'please help me improve the overall quality and performance and reliability of this system going forward';
    assert.equal(isUnderspecifiedForExecution(longVague), false);
  });

  it('false positive prevention: camelCase identifiers pass', () => {
    assert.equal(isUnderspecifiedForExecution('fix getUserById to handle null'), false);
  });
});

describe('applyRalplanGate', () => {
  it('redirects underspecified execution keywords to ralplan', () => {
    const result = applyRalplanGate(['autopilot'], 'autopilot fix this');
    assert.equal(result.gateApplied, true);
    assert.ok(result.keywords.includes('ralplan'));
    assert.ok(!result.keywords.includes('autopilot'));
  });

  it('redirects autopilot to ralplan when underspecified', () => {
    const result = applyRalplanGate(['autopilot'], 'autopilot build the app');
    assert.equal(result.gateApplied, true);
    assert.ok(result.keywords.includes('ralplan'));
  });

  it('does not gate well-specified prompts', () => {
    const result = applyRalplanGate(['autopilot'], 'autopilot fix src/hooks/bridge.ts null check');
    assert.equal(result.gateApplied, false);
    assert.ok(result.keywords.includes('autopilot'));
  });

  it('does not gate when cancel is present', () => {
    const result = applyRalplanGate(['cancel', 'autopilot'], 'cancel autopilot');
    assert.equal(result.gateApplied, false);
  });

  it('does not gate when ralplan is already present', () => {
    const result = applyRalplanGate(['ralplan'], 'ralplan add auth');
    assert.equal(result.gateApplied, false);
    assert.ok(result.keywords.includes('ralplan'));
  });

  it('does not gate non-execution keywords', () => {
    const result = applyRalplanGate(['analyze'], 'analyze this');
    assert.equal(result.gateApplied, false);
  });

  it('preserves non-execution keywords when gating', () => {
    const result = applyRalplanGate(['autopilot', 'tdd'], 'autopilot tdd fix this');
    assert.equal(result.gateApplied, true);
    assert.ok(result.keywords.includes('tdd'));
    assert.ok(result.keywords.includes('ralplan'));
    assert.ok(!result.keywords.includes('autopilot'));
  });

  it('handles force: escape hatch — does not gate', () => {
    const result = applyRalplanGate(['autopilot'], 'force: autopilot refactor the auth module');
    assert.equal(result.gateApplied, false);
  });

  it('gates multiple execution keywords at once', () => {
    const result = applyRalplanGate(['autopilot', 'ultrawork'], 'autopilot ultrawork fix this');
    assert.equal(result.gateApplied, true);
    assert.ok(result.keywords.includes('ralplan'));
    assert.ok(!result.keywords.includes('autopilot'));
    assert.ok(!result.keywords.includes('ultrawork'));
    assert.ok(result.gatedKeywords.includes('autopilot'));
    assert.ok(result.gatedKeywords.includes('ultrawork'));
  });

  it('returns empty keywords unchanged when no keywords', () => {
    const result = applyRalplanGate([], 'fix this');
    assert.equal(result.gateApplied, false);
    assert.deepEqual(result.keywords, []);
  });

  it('does not duplicate ralplan if already in filtered list', () => {
    // ultrawork is an execution keyword; after filtering, ralplan added once
    const result = applyRalplanGate(['ultrawork'], 'ultrawork do stuff');
    assert.equal(result.keywords.filter(k => k === 'ralplan').length, 1);
  });

  it('reports gatedKeywords correctly', () => {
    const result = applyRalplanGate(['autopilot', 'ultrawork'], 'autopilot ultrawork build');
    assert.ok(result.gatedKeywords.includes('autopilot'));
    assert.ok(result.gatedKeywords.includes('ultrawork'));
  });
});
