---
name: autopilot
description: Compact runtime contract for autonomous end-to-end execution
---

<Purpose>
Run an autonomous execution pipeline when the user wants a complete build/fix/delivery flow rather than a narrow edit.
</Purpose>

<Use_When>
- The user says `autopilot`, `build me`, or clearly wants end-to-end delivery.
- The task benefits from multi-step execution with verification and follow-through.
</Use_When>

<Runtime_Rules>
- Act directly unless a destructive or materially branching decision requires escalation.
- Prefer the lightest path that preserves quality.
- Keep verification mandatory.
- Use broader orchestration only when it materially improves throughput or safety.
</Runtime_Rules>

<Stop>
Finish only when the requested outcome is implemented and verified, or when a hard blocker leaves no meaningful recovery path.
</Stop>
