# Verify-Loop Cancellation Contract (Normative)

This contract defines required post-conditions for verify-loop cancellation when
verify-loop was started directly or as a standalone follow-up after other workflows.

## Required post-conditions

After cancelling verify-loop, implementations MUST ensure:

1. Targeted verify-loop state is terminal and non-active:
   - `active=false`
   - `current_phase='cancelled'`
   - `completed_at` is set (ISO8601)
2. Linked mode behavior:
   - If verify-loop is linked to Ultrawork/Ecomode in the same scope, that linked mode MUST also be terminal/non-active.
   - Unrelated unlinked modes in the same scope SHOULD remain unchanged.
3. Cross-session safety:
   - Cancellation MUST NOT mutate mode state in unrelated sessions.

## Implementation alignment points

- `internal/gocli/commands.go` enforces scoped cancellation and linked cleanup ordering.
- `internal/gocli/state.go` recognizes only canonical mode-state filenames during mode discovery.
- `skills/cancel/SKILL.md` should document scope-aware cancellation behavior against `verify-loop`.
