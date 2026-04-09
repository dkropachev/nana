export interface KeywordTriggerDefinition {
  keyword: string;
  skill: string;
  priority: number;
  guidance: string;
}

export const KEYWORD_TRIGGER_DEFINITIONS: readonly KeywordTriggerDefinition[] = [
  { keyword: 'autopilot', skill: 'autopilot', priority: 10, guidance: 'Activate autopilot skill for autonomous execution' },
  { keyword: 'build me', skill: 'autopilot', priority: 10, guidance: 'Activate autopilot skill for autonomous execution' },
  { keyword: 'I want a', skill: 'autopilot', priority: 10, guidance: 'Activate autopilot skill for autonomous execution' },

  { keyword: 'ultrawork', skill: 'ultrawork', priority: 10, guidance: 'Activate ultrawork parallel execution mode' },
  { keyword: 'ulw', skill: 'ultrawork', priority: 10, guidance: 'Activate ultrawork parallel execution mode' },
  { keyword: 'parallel', skill: 'ultrawork', priority: 10, guidance: 'Activate ultrawork parallel execution mode' },
  { keyword: 'analyze', skill: 'analyze', priority: 7, guidance: 'Activate deep analysis workflow' },
  { keyword: 'investigate', skill: 'analyze', priority: 7, guidance: 'Activate deep analysis workflow' },

  { keyword: 'deep interview', skill: 'deep-interview', priority: 8, guidance: 'Activate Ouroboros-inspired Socratic ambiguity-gated interview workflow' },
  { keyword: 'gather requirements', skill: 'deep-interview', priority: 8, guidance: 'Activate Ouroboros-inspired Socratic ambiguity-gated interview workflow' },
  { keyword: 'interview me', skill: 'deep-interview', priority: 8, guidance: 'Activate Ouroboros-inspired Socratic ambiguity-gated interview workflow' },
  { keyword: "don't assume", skill: 'deep-interview', priority: 8, guidance: 'Activate Ouroboros-inspired Socratic ambiguity-gated interview workflow' },
  { keyword: 'ouroboros', skill: 'deep-interview', priority: 8, guidance: 'Activate Ouroboros-inspired Socratic ambiguity-gated interview workflow' },
  { keyword: 'interview', skill: 'deep-interview', priority: 8, guidance: 'Activate Ouroboros-inspired Socratic ambiguity-gated interview workflow' },

  { keyword: 'plan this', skill: 'plan', priority: 8, guidance: 'Activate planning skill' },
  { keyword: 'plan the', skill: 'plan', priority: 8, guidance: 'Activate planning skill' },
  { keyword: "let's plan", skill: 'plan', priority: 8, guidance: 'Activate planning skill' },

  { keyword: 'ralplan', skill: 'ralplan', priority: 11, guidance: 'Activate consensus planning (planner + architect + critic)' },
  { keyword: 'consensus plan', skill: 'ralplan', priority: 11, guidance: 'Activate consensus planning (planner + architect + critic)' },

  { keyword: 'cancel', skill: 'cancel', priority: 5, guidance: 'Cancel active execution modes' },
  { keyword: 'stop', skill: 'cancel', priority: 5, guidance: 'Cancel active execution modes' },
  { keyword: 'abort', skill: 'cancel', priority: 5, guidance: 'Cancel active execution modes' },

  { keyword: 'tdd', skill: 'tdd', priority: 6, guidance: 'Activate test-driven workflow' },
  { keyword: 'test first', skill: 'tdd', priority: 6, guidance: 'Activate test-driven workflow' },

  { keyword: 'fix build', skill: 'build-fix', priority: 6, guidance: 'Activate build-fix workflow' },
  { keyword: 'type errors', skill: 'build-fix', priority: 6, guidance: 'Activate build-fix workflow' },

  { keyword: 'code review', skill: 'code-review', priority: 6, guidance: 'Activate code-review workflow' },
  { keyword: 'code-review', skill: 'code-review', priority: 6, guidance: 'Activate code-review workflow' },
  { keyword: 'review code', skill: 'code-review', priority: 6, guidance: 'Activate code-review workflow' },
  { keyword: 'security review', skill: 'security-review', priority: 6, guidance: 'Activate security-review workflow' },
] as const;

export function compareKeywordMatches(a: { priority: number; keyword: string }, b: { priority: number; keyword: string }): number {
  if (b.priority !== a.priority) return b.priority - a.priority;
  if (b.keyword.length !== a.keyword.length) return b.keyword.length - a.keyword.length;
  return a.keyword.localeCompare(b.keyword);
}
