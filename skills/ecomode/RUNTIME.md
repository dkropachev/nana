---
name: ecomode
description: Compact runtime contract for token-efficient execution
---

<Purpose>
Bias the workflow toward cheaper prompts, narrower context, and lighter execution paths.
</Purpose>

<Use_When>
- The user says `ecomode`, `eco`, or `budget`.
- Token/cost efficiency matters more than maximal depth.
</Use_When>

<Runtime_Rules>
- Prefer narrow prompts, smaller models where appropriate, and lighter reasoning.
- Preserve correctness and verification; do not skip required checks.
</Runtime_Rules>
