---
name: cancel
description: Cancel any active NANA mode (autopilot, ralph, ultrawork, ecomode, ultraqa, swarm, ultrapilot, pipeline, team)
---

# Cancel Skill

Intelligent cancellation that detects and cancels the active NANA mode.

**The cancel skill is the standard way to complete and exit any NANA mode.**
When the stop hook detects work is complete, it instructs the LLM to invoke
this skill for proper state cleanup. If cancel fails or is interrupted,
retry with `--force` flag, or wait for the 2-hour staleness timeout as
a last resort.

## What It Does

Automatically detects which mode is active and cancels it:
- **Autopilot**: Stops workflow, preserves progress for resume
- **Ralph**: Stops persistence loop, clears linked ultrawork if applicable
- **Ultrawork**: Stops parallel execution (standalone or linked)
- **Ecomode**: Stops token-efficient parallel execution (standalone or linked to ralph)
- **UltraQA**: Stops QA cycling workflow
- **Swarm**: Stops coordinated agent swarm, releases claimed tasks
- **Ultrapilot**: Stops parallel autopilot workers
- **Pipeline**: Stops sequential agent pipeline
- **Team**: Sends shutdown inbox to all workers, waits for exit, kills tmux session, and clears team state

## Usage

```
/cancel
```

Or say: "cancelomc", "stopomc"

## Auto-Detection

`/cancel` follows the session-aware state contract:
- By default the command inspects the current session via `state_list_active` and `state_get_status`, navigating `.nana/state/sessions/{sessionId}/…` to discover which mode is active.
- When a session id is provided or already known, that session-scoped path is authoritative. Legacy files in `.nana/state/*.json` are consulted only as a compatibility fallback if the session id is missing or empty.
- Swarm is a shared SQLite/marker mode (`.nana/state/swarm.db` / `.nana/state/swarm-active.marker`) and is not session-scoped.
- The default cleanup flow calls `state_clear` with the session id to remove only the matching session files; modes stay bound to their originating session.

## Normative Ralph cancellation post-conditions (MUST)

For Ralph-targeted cancellation (standalone or linked), completion is defined by post-conditions:

1. Target Ralph state is terminalized, not silently removed:
   - `active=false`
   - `current_phase='cancelled'`
   - `completed_at` is set (ISO timestamp)
2. If Ralph is linked to Ultrawork or Ecomode in the same scope, that linked mode is also terminalized/non-active.
4. Cancellation MUST remain scope-safe: no mutation of unrelated sessions.

See: `docs/contracts/ralph-cancel-contract.md`.

Active modes are still cancelled in dependency order:
1. Autopilot (includes linked ralph/ultraqa/ecomode cleanup)
2. Ralph (cleans its linked ultrawork or ecomode)
3. Ultrawork (standalone)
4. Ecomode (standalone)
5. UltraQA (standalone)
6. Swarm (standalone)
7. Ultrapilot (standalone)
8. Pipeline (standalone)
9. Team (tmux-based)
10. Plan Consensus (standalone)

## Normative Ralph post-conditions (MUST)

When cancellation targets Ralph state in a scope, completion requires all of the following:

1. Ralph state is terminal in that same scope: `active=false`, `current_phase='cancelled'` (or linked terminal phase), and `completed_at` is set.
2. Linked Ultrawork/Ecomode in the same scope is also terminal/non-active.
4. Unrelated sessions are untouched.

## Force Clear All

Use `--force` or `--all` when you need to erase every session plus legacy artifacts, e.g., to reset the workspace entirely.

```
/cancel --force
```

```
/cancel --all
```

Steps under the hood:
1. `state_list_active` enumerates `.nana/state/sessions/{sessionId}/…` to find every known session.
2. `state_clear` runs once per session to drop that session’s files.
3. A global `state_clear` without `session_id` removes legacy files under `.nana/state/*.json`, `.nana/state/swarm*.db`, and compatibility artifacts (see list).
4. Team artifacts (`.nana/state/team/*/`, tmux sessions matching `nana-team-*`) are best-effort cleared as part of the legacy fallback.

Every `state_clear` command honors the `session_id` argument, so even force mode still uses the session-aware paths first before deleting legacy files.

Legacy compatibility list (removed only under `--force`/`--all`):
- `.nana/state/autopilot-state.json`
- `.nana/state/ralph-state.json`
- `.nana/state/ralph-plan-state.json`
- `.nana/state/ralph-verification.json`
- `.nana/state/ultrawork-state.json`
- `.nana/state/ecomode-state.json`
- `.nana/state/ultraqa-state.json`
- `.nana/state/swarm.db`
- `.nana/state/swarm.db-wal`
- `.nana/state/swarm.db-shm`
- `.nana/state/swarm-active.marker`
- `.nana/state/swarm-tasks.db`
- `.nana/state/ultrapilot-state.json`
- `.nana/state/ultrapilot-ownership.json`
- `.nana/state/pipeline-state.json`
- `.nana/state/plan-consensus.json`
- `.nana/state/ralplan-state.json`
- `.nana/state/boulder.json`
- `.nana/state/hud-state.json`
- `.nana/state/subagent-tracking.json`
- `.nana/state/subagent-tracker.lock`
- `.nana/state/rate-limit-daemon.pid`
- `.nana/state/rate-limit-daemon.log`
- `.nana/state/checkpoints/` (directory)
- `.nana/state/sessions/` (empty directory cleanup after clearing sessions)

## Implementation Steps

When you invoke this skill:

### 1. Parse Arguments

```bash
# Check for --force or --all flags
FORCE_MODE=false
if [[ "$*" == *"--force"* ]] || [[ "$*" == *"--all"* ]]; then
  FORCE_MODE=true
fi
```

### 2. Detect Active Modes

The skill now relies on the session-aware state contract rather than hard-coded file paths:
1. Call `state_list_active` to enumerate `.nana/state/sessions/{sessionId}/…` and discover every active session.
2. For each session id, call `state_get_status` to learn which mode is running (`autopilot`, `ralph`, `ultrawork`, etc.) and whether dependent modes exist.
3. If a `session_id` was supplied to `/cancel`, skip legacy fallback entirely and operate solely within that session path; otherwise, consult legacy files in `.nana/state/*.json` only if the state tools report no active session. Swarm remains a shared SQLite/marker mode outside session scoping.
4. Any cancellation logic in this doc mirrors the dependency order discovered via state tools (autopilot → ralph → …).

### 3A. Force Mode (if --force or --all)

Use force mode to clear every session plus legacy artifacts via `state_clear`. Direct file removal is reserved for legacy cleanup when the state tools report no active sessions.

### 3B. Smart Cancellation (default)

#### If Team Active (tmux-based)

Teams are detected by checking for config files in `.nana/state/team/`:

```bash
# Check for active teams
ls .nana/state/team/*/config.json 2>/dev/null
```

**Two-pass cancellation protocol:**

**Pass 1: Graceful Shutdown**
```
For each team found in .nana/state/team/:
  1. Read config.json to get team_name and workers list
  2. For each worker:
     a. Write shutdown inbox to .nana/state/team/{name}/workers/{worker}/inbox.md
     b. Send short trigger via tmux send-keys
     c. Wait up to 15 seconds for worker tmux pane to exit
     d. If still alive: mark as unresponsive
```

**Pass 2: Force Kill**
```
After graceful pass:
  1. For each remaining alive worker:
     a. Send C-c via tmux send-keys
     b. Wait 2 seconds
     c. Kill the tmux window if still alive
  2. Destroy the tmux session: tmux kill-session -t nana-team-{name}
```

**Cleanup:**
```
  1. Strip AGENTS.md team worker overlay (<!-- NANA:TEAM:WORKER:START/END -->)
  2. Remove team state directory: rm -rf .nana/state/team/{name}/
  3. Clear team mode state: state_clear(mode="team")
  4. Emit structured cancel report
```

**Structured Cancel Report:**
```
Team "{team_name}" cancelled:
  - Workers signaled: N
  - Graceful exits: M
  - Force killed: K
  - tmux session destroyed: yes/no
  - State cleaned up: yes/no
```

**Implementation note:** The cancel skill is executed by the LLM, not as a bash script. When you detect an active team:
1. Check `.nana/state/team/*/config.json` for active teams
2. For each worker in config.workers, write shutdown inbox and send trigger
3. Wait briefly for workers to exit (15s timeout)
4. Force kill remaining workers via tmux
5. Destroy tmux session: `tmux kill-session -t nana-team-{name}`
6. Strip AGENTS.md overlay
7. Remove state: `rm -rf .nana/state/team/{name}/`
8. `state_clear(mode="team")`
9. Report structured summary to user

#### If Autopilot Active

Call `cancelAutopilot()` from `src/hooks/autopilot/cancel.ts:27-78`:

```bash
# Autopilot handles its own cleanup + ralph + ultraqa
# Just mark autopilot as inactive (preserves state for resume)
if [[ -f .nana/state/autopilot-state.json ]]; then
  # Clean up ralph if active
  if [[ -f .nana/state/ralph-state.json ]]; then
    RALPH_STATE=$(cat .nana/state/ralph-state.json)
    LINKED_UW=$(echo "$RALPH_STATE" | jq -r '.linked_ultrawork // false')

    # Clean linked ultrawork first
    if [[ "$LINKED_UW" == "true" ]] && [[ -f .nana/state/ultrawork-state.json ]]; then
      rm -f .nana/state/ultrawork-state.json
      echo "Cleaned up: ultrawork (linked to ralph)"
    fi

    # Clean ralph
    rm -f .nana/state/ralph-state.json
    rm -f .nana/state/ralph-verification.json
    echo "Cleaned up: ralph"
  fi

  # Clean up ultraqa if active
  if [[ -f .nana/state/ultraqa-state.json ]]; then
    rm -f .nana/state/ultraqa-state.json
    echo "Cleaned up: ultraqa"
  fi

  # Mark autopilot inactive but preserve state
  CURRENT_STATE=$(cat .nana/state/autopilot-state.json)
  CURRENT_PHASE=$(echo "$CURRENT_STATE" | jq -r '.phase // "unknown"')
  echo "$CURRENT_STATE" | jq '.active = false' > .nana/state/autopilot-state.json

  echo "Autopilot cancelled at phase: $CURRENT_PHASE. Progress preserved for resume."
  echo "Run /autopilot to resume."
fi
```

#### If Ralph Active (but not Autopilot)

Call `clearRalphState()` + `clearLinkedUltraworkState()` from `src/hooks/ralph-loop/index.ts:147-182`:

```bash
if [[ -f .nana/state/ralph-state.json ]]; then
  # Check if ultrawork is linked
  RALPH_STATE=$(cat .nana/state/ralph-state.json)
  LINKED_UW=$(echo "$RALPH_STATE" | jq -r '.linked_ultrawork // false')

  # Clean linked ultrawork first
  if [[ "$LINKED_UW" == "true" ]] && [[ -f .nana/state/ultrawork-state.json ]]; then
    UW_STATE=$(cat .nana/state/ultrawork-state.json)
    UW_LINKED=$(echo "$UW_STATE" | jq -r '.linked_to_ralph // false')

    # Only clear if it was linked to ralph
    if [[ "$UW_LINKED" == "true" ]]; then
      rm -f .nana/state/ultrawork-state.json
      echo "Cleaned up: ultrawork (linked to ralph)"
    fi
  fi

  # Clean ralph state
  rm -f .nana/state/ralph-state.json
  rm -f .nana/state/ralph-plan-state.json
  rm -f .nana/state/ralph-verification.json

  echo "Ralph cancelled. Persistent mode deactivated."
fi
```

#### If Ultrawork Active (standalone, not linked)

Call `deactivateUltrawork()` from `src/hooks/ultrawork/index.ts:150-173`:

```bash
if [[ -f .nana/state/ultrawork-state.json ]]; then
  # Check if linked to ralph
  UW_STATE=$(cat .nana/state/ultrawork-state.json)
  LINKED=$(echo "$UW_STATE" | jq -r '.linked_to_ralph // false')

  if [[ "$LINKED" == "true" ]]; then
    echo "Ultrawork is linked to Ralph. Use /cancel to cancel both."
    exit 1
  fi

  # Remove local state
  rm -f .nana/state/ultrawork-state.json

  echo "Ultrawork cancelled. Parallel execution mode deactivated."
fi
```

#### If UltraQA Active (standalone)

Call `clearUltraQAState()` from `src/hooks/ultraqa/index.ts:107-120`:

```bash
if [[ -f .nana/state/ultraqa-state.json ]]; then
  rm -f .nana/state/ultraqa-state.json
  echo "UltraQA cancelled. QA cycling workflow stopped."
fi
```

#### No Active Modes

```bash
echo "No active NANA modes detected."
echo ""
echo "Checked for:"
echo "  - Autopilot (.nana/state/autopilot-state.json)"
echo "  - Ralph (.nana/state/ralph-state.json)"
echo "  - Ultrawork (.nana/state/ultrawork-state.json)"
echo "  - UltraQA (.nana/state/ultraqa-state.json)"
echo ""
echo "Use --force to clear all state files anyway."
```

## Implementation Notes

The cancel skill runs as follows:
1. Parse the `--force` / `--all` flags, tracking whether cleanup should span every session or stay scoped to the current session id.
2. Use `state_list_active` to enumerate known session ids and `state_get_status` to learn the active mode (`autopilot`, `ralph`, `ultrawork`, etc.) for each session.
3. When operating in default mode, call `state_clear` with that session_id to remove only the session’s files, then run mode-specific cleanup (autopilot → ralph → …) based on the state tool signals.
4. In force mode, iterate every active session, call `state_clear` per session, then run a global `state_clear` without `session_id` to drop legacy files (`.nana/state/*.json`, compatibility artifacts) and report success. Swarm remains a shared SQLite/marker mode outside session scoping.
5. Team artifacts (`.nana/state/team/*/`, tmux sessions matching `nana-team-*`) remain best-effort cleanup items invoked during the legacy/global pass.

State tools always honor the `session_id` argument, so even force mode still clears the session-scoped paths before deleting compatibility-only legacy state.

Mode-specific subsections below describe what extra cleanup each handler performs after the state-wide operations finish.
## Messages Reference

| Mode | Success Message |
|------|-----------------|
| Autopilot | "Autopilot cancelled at phase: {phase}. Progress preserved for resume." |
| Ralph | "Ralph cancelled. Persistent mode deactivated." |
| Ultrawork | "Ultrawork cancelled. Parallel execution mode deactivated." |
| Ecomode | "Ecomode cancelled. Token-efficient execution mode deactivated." |
| UltraQA | "UltraQA cancelled. QA cycling workflow stopped." |
| Swarm | "Swarm cancelled. Coordinated agents stopped." |
| Ultrapilot | "Ultrapilot cancelled. Parallel autopilot workers stopped." |
| Pipeline | "Pipeline cancelled. Sequential agent chain stopped." |
| Team | "Team cancelled. Teammates shut down and cleaned up." |
| Plan Consensus | "Plan Consensus cancelled. Planning session ended." |
| Force | "All NANA modes cleared. You are free to start fresh." |
| None | "No active NANA modes detected." |

## What Gets Preserved

| Mode | State Preserved | Resume Command |
|------|-----------------|----------------|
| Autopilot | Yes (phase, files, spec, plan, verdicts) | `/autopilot` |
| Ralph | No | N/A |
| Ultrawork | No | N/A |
| UltraQA | No | N/A |
| Swarm | No | N/A |
| Ultrapilot | No | N/A |
| Pipeline | No | N/A |
| Plan Consensus | Yes (plan file path preserved) | N/A |

## Notes

- **Dependency-aware**: Autopilot cancellation cleans up Ralph and UltraQA
- **Link-aware**: Ralph cancellation cleans up linked Ultrawork or Ecomode
- **Safe**: Only clears linked Ultrawork, preserves standalone Ultrawork
- **Local-only**: Clears state files in `.nana/state/` directory
- **Resume-friendly**: Autopilot state is preserved for seamless resume
- **Team-aware**: Detects tmux-based teams and performs graceful shutdown with force-kill fallback

## Tmux Team Cleanup

When cancelling team mode, the cancel skill should:

1. **Kill all team tmux sessions**: `tmux list-sessions -F '#{session_name}' 2>/dev/null | grep '^nana-team-'` and kill each
2. **Remove team state directories**: `rm -rf .nana/state/team/*/`
3. **Strip AGENTS.md overlay**: Remove content between `<!-- NANA:TEAM:WORKER:START -->` and `<!-- NANA:TEAM:WORKER:END -->`

### Force Clear Addition

When `--force` is used, also clean up:
```bash
rm -rf .nana/state/team/                  # All team state
# Kill all nana-team-* tmux sessions
tmux list-sessions -F '#{session_name}' 2>/dev/null | grep '^nana-team-' | while read s; do tmux kill-session -t "$s" 2>/dev/null; done
```
