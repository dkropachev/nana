---
name: cancel
description: Compact runtime contract for ending active modes
---

<Purpose>
Stop active NANA modes and clean up mode state when work is complete, cancelled, or blocked.
</Purpose>

<Use_When>
- The user says `cancel`, `stop`, or `abort`.
- A running mode should be shut down cleanly.
</Use_When>

<Runtime_Rules>
- Cancel only when work is done, the user requests it, or no meaningful recovery path remains.
- Clear persisted mode state during cleanup.
- After reporting cancelled modes, print a compact recovery summary with:
  - current session id (or `n/a`)
  - affected mode state paths and previous phases
  - latest open runtime artifact, when known
  - recent pending plan artifacts under `.nana/plans/`
  - safe next commands: `nana status`, `nana doctor`, and `nana verify --json` when the repo has `nana-verify.json`
</Runtime_Rules>
