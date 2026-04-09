---
name: cancel
description: Cancel active NANA session/runtime state safely
---

# Cancel Skill

Use `$cancel` to stop active NANA execution state and clean up the current session safely.

## What It Does

`$cancel` is the standard shutdown path for active NANA state. It:
- detects the active session-scoped modes
- clears or terminalizes the current session state
- performs best-effort cleanup of related runtime artifacts
- reports what was cancelled

Typical affected modes include:
- `autopilot`
- `ultrawork`
- `ecomode`
- `pipeline`
- `deep-interview`
- other session-scoped execution/planning state that is currently active

It may also perform **best-effort cleanup** of internal tmux/team runtime artifacts when they are present, but those are implementation details rather than a primary user workflow.

## Usage

```text
$cancel
```

Natural language equivalents like `stop`, `cancel`, or `abort` should route here.

## Current Cancellation Model

`$cancel` follows the session-aware state contract:
- enumerate active session-scoped state first
- clear only the current session by default
- avoid mutating unrelated sessions
- fall back to legacy/global cleanup only when needed

If forceful cleanup is required:

```text
$cancel --force
```

Force mode may additionally remove stale legacy artifacts after session-scoped cleanup has been attempted.

## Cleanup Order

When multiple active states are linked, cancel in dependency order:
1. autonomous / top-level execution state
2. parallel execution helpers
3. lightweight modifier state
4. pipeline state
5. best-effort internal team/tmux artifacts

The key invariant is: clear broader owner state before or together with its dependent helper state.

## Best-Effort Internal Cleanup

Some internal artifacts are still compatibility/runtime-only and may be cleaned up by `$cancel` when present:
- `.nana/state/team/*`
- tmux sessions matching internal NANA worker/runtime naming
- legacy compatibility files under `.nana/state/*.json`
- shared marker/SQLite artifacts used by older runtime paths

These are internal implementation details. Users should think of `$cancel` as “stop the current NANA run cleanly,” not as a low-level runtime management command.

## Execution Steps

1. Detect active state for the current session.
2. Clear or terminalize the active mode state.
3. Clear linked helper state when applicable.
4. Perform best-effort runtime cleanup for leftover internal artifacts.
5. Report what was cancelled.

## Good Output

Example:

```text
Cancelled: autopilot
Cancelled: ultrawork
Best-effort cleanup: internal runtime artifacts
```

## Notes

- Use `$cancel` instead of manually deleting `.nana/state/*`.
- By default, cancellation should be scope-safe and session-safe.
- If cleanup is interrupted, rerun `$cancel` before attempting manual deletion.
