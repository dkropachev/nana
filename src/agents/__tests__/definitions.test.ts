import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import {
  AGENT_DEFINITIONS,
  getAgent,
  getAgentsByDeliveryClass,
  getAgentNames,
  getAgentsByCategory,
  type AgentDefinition,
} from '../definitions.js';

describe('agents/definitions', () => {
  it('returns known agents and undefined for unknown names', () => {
    assert.equal(getAgent('executor'), AGENT_DEFINITIONS.executor);
    assert.equal(getAgent('does-not-exist'), undefined);
  });

  it('keeps key/name contract aligned', () => {
    const names = getAgentNames();
    assert.ok(names.length > 20, 'expected non-trivial agent catalog');

    for (const name of names) {
      const agent = AGENT_DEFINITIONS[name];
      assert.equal(agent.name, name);
      assert.ok(agent.description.length > 0);
      assert.ok(agent.deliveryClass.length > 0);
      assert.ok(agent.reasoningEffort.length > 0);
      assert.ok(agent.posture.length > 0);
      assert.ok(agent.modelClass.length > 0);
      assert.ok(agent.routingRole.length > 0);
    }
  });

  it('filters agents by category', () => {
    const buildAgents = getAgentsByCategory('build');
    assert.ok(buildAgents.length > 0);
    assert.ok(buildAgents.some((agent) => agent.name === 'executor'));

    const allowed: AgentDefinition['category'][] = [
      'build',
      'review',
      'domain',
      'product',
      'coordination',
    ];

    for (const category of allowed) {
      const agents = getAgentsByCategory(category);
      assert.ok(agents.every((agent) => agent.category === category));
    }
  });

  it('classifies every agent as either reviewer or executor', () => {
    const reviewers = getAgentsByDeliveryClass('reviewer');
    const executors = getAgentsByDeliveryClass('executor');

    assert.ok(reviewers.length > 0);
    assert.ok(executors.length > 0);
    assert.ok(reviewers.every((agent) => agent.deliveryClass === 'reviewer'));
    assert.ok(executors.every((agent) => agent.deliveryClass === 'executor'));

    assert.equal(AGENT_DEFINITIONS.architect.deliveryClass, 'reviewer');
    assert.equal(AGENT_DEFINITIONS['code-reviewer'].deliveryClass, 'reviewer');
    assert.equal(AGENT_DEFINITIONS.executor.deliveryClass, 'executor');
    assert.equal(AGENT_DEFINITIONS['test-engineer'].deliveryClass, 'executor');
    assert.equal(AGENT_DEFINITIONS.writer.deliveryClass, 'executor');
  });

  it('keeps the installable agent model split aligned with the NANA subagent matrix', () => {
    assert.equal(AGENT_DEFINITIONS.architect.modelClass, 'frontier');
    assert.equal(AGENT_DEFINITIONS['security-reviewer'].modelClass, 'frontier');
    assert.equal(AGENT_DEFINITIONS['test-engineer'].modelClass, 'frontier');
    assert.equal(AGENT_DEFINITIONS.vision.modelClass, 'frontier');

    assert.equal(AGENT_DEFINITIONS.explore.modelClass, 'fast');

    for (const name of [
      'researcher',
      'debugger',
      'designer',
      'writer',
      'git-master',
      'build-fixer',
      'executor',
      'verifier',
      'dependency-expert',
    ] as const) {
      assert.equal(AGENT_DEFINITIONS[name].modelClass, 'standard');
      assert.equal(AGENT_DEFINITIONS[name].reasoningEffort, 'high');
    }
  });
});
