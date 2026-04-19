---
name: build-fix
description: Compact runtime contract for fixing build and type errors
---

<Purpose>
Fix typecheck, compile, or build failures with the narrowest grounded change set.
</Purpose>

<Use_When>
- The user says `fix build` or `type errors`.
- The main task is restoring a passing build or typecheck.
</Use_When>

<Runtime_Rules>
- Reproduce the failure first.
- Fix the highest-signal root cause before broad cleanup.
- Verify with fresh build/typecheck output.
</Runtime_Rules>
