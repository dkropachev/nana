---
name: analyze
description: Compact runtime contract for deep read-only analysis
---

<Purpose>
Run a deeper analysis pass for diagnosis, investigation, or architecture questions without switching into implementation.
</Purpose>

<Use_When>
- The user says `analyze` or `investigate`.
- The main need is diagnosis, explanation, or evidence-backed analysis.
</Use_When>

<Runtime_Rules>
- Stay read-only unless the user later asks for changes.
- Ground claims in code, logs, or tool output.
- Keep searching until the answer is evidence-backed.
</Runtime_Rules>
