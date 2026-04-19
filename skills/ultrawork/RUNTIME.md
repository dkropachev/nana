---
name: ultrawork
description: Compact runtime contract for parallel independent work
---

<Purpose>
Use parallel agents for bounded independent tasks when parallelism materially improves throughput.
</Purpose>

<Use_When>
- The user says `ultrawork`, `ulw`, or explicitly wants parallel execution.
- The work can be split into independent slices with clear ownership.
</Use_When>

<Runtime_Rules>
- Launch independent work in parallel; keep dependent work sequential.
- Delegate concrete bounded tasks only.
- Match model/reasoning level to task difficulty.
- Keep verification lightweight but real after all parallel work completes.
</Runtime_Rules>
