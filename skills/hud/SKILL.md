---
name: "hud"
description: "Show or configure the NANA HUD (two-layer statusline)"
role: "display"
scope: ".nana/**"
---

# HUD Skill

The NANA HUD uses a two-layer architecture:

1. **Layer 1 - Codex built-in statusLine**: Real-time TUI footer showing model, git branch, and context usage. Configured via `[tui] status_line` in `~/.codex/config.toml`. Zero code required.

2. **Layer 2 - `nana hud` CLI command**: Shows NANA-specific orchestration state (autopilot, ultrawork, pipeline, ecomode, turns, and internal runtime markers). Reads `.nana/state/` files.

## Quick Commands

| Command | Description |
|---------|-------------|
| `nana hud` | Show current HUD (modes, turns, activity) |
| `nana hud --watch` | Live-updating display (polls every 1s) |
| `nana hud --json` | Raw state output for scripting |
| `nana hud --preset=minimal` | Minimal display |
| `nana hud --preset=focused` | Default display |
| `nana hud --preset=full` | All elements |

## Presets

### minimal
```
[NANA] autopilot:execution | turns:42
```

### focused (default)
```
[NANA] autopilot:execution | ultrawork | turns:42 | last:5s ago
```

### full
```
[NANA] ultrawork | autopilot:execution | pipeline:exec | turns:42 | last:5s ago | total-turns:156
```

## Setup

`nana setup` automatically configures both layers:
- Adds `[tui] status_line` to `~/.codex/config.toml` (Layer 1)
- Writes `.nana/hud-config.json` with default preset (Layer 2)
- Default preset is `focused`; if HUD/statusline changes do not appear, restart Codex CLI once.

## Layer 1: Codex Built-in StatusLine

Configured in `~/.codex/config.toml`:
```toml
[tui]
status_line = ["model-with-reasoning", "git-branch", "context-remaining"]
```

Available built-in items (Codex CLI v0.101.0+):
`model-name`, `model-with-reasoning`, `current-dir`, `project-root`, `git-branch`, `context-remaining`, `context-used`, `five-hour-limit`, `weekly-limit`, `codex-version`, `context-window-size`, `used-tokens`, `total-input-tokens`, `total-output-tokens`, `session-id`

## Layer 2: NANA Orchestration HUD

The `nana hud` command reads these state files:
- `.nana/state/ultrawork-state.json` - Ultrawork mode
- `.nana/state/autopilot-state.json` - Autopilot phase
- `.nana/state/pipeline-state.json` - Pipeline stage
- `.nana/state/ecomode-state.json` - Ecomode active
- `.nana/state/hud-state.json` - Last activity (from notify hook)
- `.nana/metrics.json` - Turn counts

## Configuration

HUD config stored at `.nana/hud-config.json`:
```json
{
  "preset": "focused"
}
```

## Color Coding

- **Green**: Normal/healthy
- **Yellow**: Warning
- **Red**: Critical

## Troubleshooting

If the TUI statusline is not showing:
1. Ensure Codex CLI v0.101.0+ is installed
2. Run `nana setup` to configure `[tui]` section
3. Restart Codex CLI

If `nana hud` shows "No active modes":
- This is expected when no workflows are running
- Start a workflow (for example autopilot) and check again
