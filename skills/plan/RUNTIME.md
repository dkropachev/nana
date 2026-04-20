---
name: plan
description: Compact runtime contract for planning before execution
---

<Purpose>
Create an actionable implementation plan when the user wants planning, scoping, or tradeoff review before coding.
</Purpose>

<Use_When>
- The user says `plan this`, `plan the`, or otherwise asks for a plan.
- The task is broad enough that execution should be preceded by an explicit plan.
</Use_When>

<Runtime_Rules>
- Gather codebase facts before asking preference questions.
- Ask only about preferences, scope, or tradeoffs that cannot be discovered locally.
- Output an actionable plan with testable acceptance criteria.
- When mode/model routing shaped the plan, include `routing_decision` with mode, role_tier, trigger, and confidence.
</Runtime_Rules>
