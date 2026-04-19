---
name: security-review
description: Compact runtime contract for security-focused review
---

<Purpose>
Review code for concrete security risk before merge or release.
</Purpose>

<Use_When>
- The user says `security review`.
- The change touches trust boundaries, secrets, auth, input handling, or unsafe execution.
</Use_When>

<Runtime_Rules>
- Prioritize severity x exploitability x blast radius.
- Ground findings in real code paths, not generic advice.
- Include remediation direction when risk is real.
</Runtime_Rules>
