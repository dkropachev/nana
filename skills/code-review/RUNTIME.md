---
name: code-review
description: Compact runtime contract for review-only evaluation
---

<Purpose>
Run a code review focused on actionable defects, risks, and missing tests.
</Purpose>

<Use_When>
- The user says `code review` or `review code`.
- The task is review-only, not implementation.
</Use_When>

<Runtime_Rules>
- Lead with findings, ordered by severity.
- Ground issues in specific files and behavior.
- Say clearly when no actionable issues are found.
</Runtime_Rules>
