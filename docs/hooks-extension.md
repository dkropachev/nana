# Hooks Extension (Custom Hooks)

NANA supports an additive hooks extension point for executable user hooks under `.nana/hooks/`.

> Compatibility guarantee: `nana tmux-hook` remains fully supported and unchanged.
> The new `nana hooks` command group is additive and does **not** replace tmux-hook workflows.

## Quick start

```bash
nana hooks init
nana hooks status
nana hooks validate
nana hooks test
```

This creates a scaffold hook at:

- `.nana/hooks/sample-hook.sh` on POSIX
- `.nana/hooks/sample-hook.cmd` on Windows

## Enablement model

Hooks are **enabled by default**.

Disable plugin dispatch explicitly:

```bash
export NANA_HOOK_PLUGINS=0
```

Optional timeout tuning (default: 1500ms):

```bash
export NANA_HOOK_PLUGIN_TIMEOUT_MS=1500
```

## Native event pipeline (v1)

Native events are emitted from existing lifecycle/notify paths:

- `session-start`
- `session-end`
- `turn-complete`
- `session-idle`

Pass one keeps this existing event vocabulary; it does **not** introduce an event-taxonomy redesign.

For clawhip-oriented operational routing, see [Clawhip Event Contract](./clawhip-event-contract.md).

Envelope fields include:

- `schema_version: "1"`
- `event`
- `timestamp`
- `source` (`native` or `derived`)
- `context`
- optional IDs: `session_id`, `thread_id`, `turn_id`, `mode`

## Derived signals (opt-in)

Best-effort derived events are gated and disabled by default.

```bash
export NANA_HOOK_DERIVED_SIGNALS=1
```

Derived signals include:

- `needs-input`
- `pre-tool-use`
- `post-tool-use`

Derived events are labeled with:

- `source: "derived"`
- `confidence`
- parser-specific context hints

## Team-safety behavior

In team-worker sessions (`NANA_TEAM_WORKER` set), hook side effects are skipped by default.
This keeps the lead session as the canonical side-effect emitter and avoids duplicate sends.

## Hook contract

Each hook is an executable file. NANA sends one JSON request on `stdin` containing:

- the event envelope
- the hook id/path
- the workspace cwd
- `side_effects_enabled`
- canonical runtime state file paths
- plugin-local state/tmux state file paths

The hook returns one JSON object on `stdout`:

```json
{
  "ok": true,
  "reason": "ok",
  "logs": [{"level": "info", "message": "hook ran"}],
  "state": {
    "set": {"last_event": "turn-complete"},
    "delete": ["old_key"]
  },
  "tmux_actions": [
    {"pane_id": "%1", "text": "continue", "submit": true}
  ]
}
```

Go applies logs, plugin-local state writes, and tmux side effects after parsing the response.

Legacy `.mjs` hooks are reported by `nana hooks status` and rejected by `nana hooks validate`; they are no longer executed.

## Logs

Hook dispatch and hook logs are written to:

- `.nana/logs/hooks-YYYY-MM-DD.jsonl`
