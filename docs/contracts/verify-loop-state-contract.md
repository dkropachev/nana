# Verify-Loop State Contract (Frozen)

## Canonical verify-loop state schema

Verify-loop runtime state is stored at `.nana/state/{scope}/verify-loop-state.json` and MUST use this schema:

- `active: boolean` **(required)**
- `iteration: number` **(required while active)**
- `max_iterations: number` **(required while active)**
- `current_phase: string` **(required while active)**
- `started_at: ISO8601 string` **(required while active)**
- `completed_at?: ISO8601 string`
- Optional linkage fields: `linked_ultrawork`, `linked_ecomode`, `linked_mode`

Verify-loop remains a standalone mode. Other workflows may start verify-loop later as an
explicit follow-up, but there is no built-in linked launch path anymore.

Persisted values MUST end in the frozen enum below.

## Frozen verify-loop phase vocabulary

`current_phase` for verify-loop MUST be one of:

- `starting`
- `executing`
- `verifying`
- `fixing`
- `complete`
- `failed`
- `cancelled`

Unknown phase values MUST be rejected.

Phase progression reference (illustrative):
starting
- `executing`
- `verifying`
- `fixing`
- `complete`

## Frozen scope policy

1. If `session_id` is present (explicit argument or current `.nana/state/session.json`), session scope (`.nana/state/sessions/{session_id}/...`) is authoritative.
2. Root scope (`.nana/state/*.json`) is compatibility fallback only.
3. Writes MUST target one scope (authoritative scope), never broadcast to unrelated sessions.

## Consumer compatibility matrix

| Consumer | Responsibility under frozen scope/phase contract |
|---|---|
| `internal/gocli/hud.go` | Read session scope first when current session is known; fall back to root only when scoped file is absent. |
| `internal/gocli/state.go` | Build scope-preferred mode-file lists from authoritative state paths. |
| `internal/gocli/commands.go` | Status and cancellation operate on scope-preferred mode files; cancellation does not mutate unrelated sessions. |
| `internal/gocli/start_ui.go` | Surface runtime state read-only through Start UI payloads. |
| `internal/gocli/next.go` | Rank active mode and next-step attention from scope-preferred mode files. |

## Canonical PRD/progress sources

- Canonical PRD: `.nana/plans/prd-{slug}.md`
- Canonical progress ledger: `.nana/state/{scope}/verify-loop-progress.json`
