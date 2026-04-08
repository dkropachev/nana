import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { loadSurface } from './prompt-guidance-test-helpers.js';

describe('prompt delivery split guidance', () => {
  for (const promptPath of [
    'prompts/architect.md',
    'prompts/code-reviewer.md',
    'prompts/quality-reviewer.md',
    'prompts/style-reviewer.md',
    'prompts/security-reviewer.md',
    'prompts/api-reviewer.md',
    'prompts/performance-reviewer.md',
    'prompts/dependency-expert.md',
  ]) {
    it(`${promptPath} encodes reviewer/executor split guidance for reviewer roles`, () => {
      const content = loadSurface(promptPath);
      assert.match(content, /In reviewer\/executor layouts:/i);
      assert.match(content, /In split mode, stop at/i);
      assert.match(content, /merged reviewer\+executor mode/i);
    });
  }

  for (const promptPath of [
    'prompts/executor.md',
    'prompts/test-engineer.md',
  ]) {
    it(`${promptPath} encodes reviewer/executor split guidance for executor roles`, () => {
      const content = loadSurface(promptPath);
      assert.match(content, /In reviewer\/executor layouts:/i);
      assert.match(content, /merged reviewer\+executor mode/i);
    });
  }

  it('executor prompt makes execution ownership explicit in split layouts', () => {
    assert.match(loadSurface('prompts/executor.md'), /you are the execution owner/i);
  });

  it('test-engineer prompt keeps tests on the executor side of the split', () => {
    assert.match(loadSurface('prompts/test-engineer.md'), /executor lane for tests/i);
  });
});
