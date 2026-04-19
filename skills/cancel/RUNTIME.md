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
</Runtime_Rules>
