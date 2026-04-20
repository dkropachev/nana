---
name: ralplan
description: Compact runtime contract for consensus planning
---

<Purpose>
Run a consensus planning loop when architecture, tradeoffs, or test strategy need review before execution.
</Purpose>

<Use_When>
- The user says `ralplan` or `consensus plan`.
- The task is high-risk enough that planner/architect/critic review is worthwhile.
</Use_When>

<Runtime_Rules>
- Keep planning read-only.
- Require explicit tradeoffs, concrete verification, and actionable acceptance criteria.
- Output the approved plan without implementing it.
- Include `routing_decision` with mode, role_tier, trigger, and confidence when the consensus path or model tier shaped the plan.
</Runtime_Rules>
