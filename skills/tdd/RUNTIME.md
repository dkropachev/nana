---
name: tdd
description: Compact runtime contract for test-first execution
---

<Purpose>
Run a test-driven workflow when the user wants test-first changes or stronger regression discipline.
</Purpose>

<Use_When>
- The user says `tdd` or `test first`.
- The work benefits from writing or tightening tests before implementation.
</Use_When>

<Runtime_Rules>
- Prefer a red-green-refactor flow when practical.
- Keep tests focused on behavior and existing local patterns.
- Finish with fresh test evidence.
</Runtime_Rules>
