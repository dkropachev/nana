---
name: nana-setup
description: Setup and configure nana using current CLI behavior
---

# NANA Setup

Use this skill when users want to install or refresh nana for the **current project plus user-level NANA directories**.

## Command

```bash
nana setup [--force] [--dry-run] [--verbose] [--scope <user|project>]
```

If you only want lightweight `AGENTS.md` scaffolding for an existing repo or subtree, use `nana agents-init [path]` instead of full setup.

Supported setup flags (current implementation):
- `--force`: overwrite/reinstall managed artifacts where applicable
- `--dry-run`: print actions without mutating files
- `--verbose`: print per-file/per-step details
- `--scope`: choose install scope (`user`, `project`)

## What this setup actually does

`nana setup` performs these steps:

1. Resolve setup scope:
   - `--scope` explicit value
   - else persisted `./.nana/setup-scope.json` (with automatic migration of legacy values)
   - else interactive prompt on TTY (default `user`)
   - else default `user` (safe for CI/tests)
2. Create directories and persist effective scope
3. Install prompts, native agent configs, skills, and merge config.toml (scope determines target directories)
4. Verify Team CLI API interop markers exist in built `dist/cli/team.js`
5. Generate project-root `./AGENTS.md` from `templates/AGENTS.md` (or skip when existing and no force)
6. Configure notify hook references and write `./.nana/hud-config.json`

## Important behavior notes

- `nana setup` only prompts for scope when no scope is provided/persisted and stdin/stdout are TTY.
- Local project orchestration file is `./AGENTS.md` (project root).
- If `AGENTS.md` exists and `--force` is not used, interactive TTY runs ask whether to overwrite. Non-interactive runs preserve the file.
- Scope targets:
  - `user`: user directories (`~/.codex`, `~/.codex/skills`, `~/.nana/agents`)
  - `project`: local directories (`./.codex`, `./.codex/skills`, `./.nana/agents`)
- Migration hint: in `user` scope, if historical `~/.agents/skills` still exists alongside `${CODEX_HOME:-~/.codex}/skills`, current setup prints a cleanup hint because Codex may show duplicate skill entries until the legacy tree is removed or archived.
- If persisted scope is `project`, `nana` launch automatically uses `CODEX_HOME=./.codex` unless user explicitly overrides `CODEX_HOME`.
- With `--force`, AGENTS overwrite may still be skipped if an active NANA session is detected (safety guard).
- Legacy persisted scope values (`project-local`) are automatically migrated to `project` with a one-time warning.

## Recommended workflow

1. Run setup:

```bash
nana setup --force --verbose
```

2. Verify installation:

```bash
nana doctor
```

3. Start Codex with NANA in the target project directory.

## Expected verification indicators

From `nana doctor`, expect:
- Prompts installed (scope-dependent: user or project)
- Skills installed (scope-dependent: user or project)
- AGENTS.md found in project root
- `.nana/state` exists
- NANA MCP servers configured in scope target `config.toml` (`~/.codex/config.toml` or `./.codex/config.toml`)

## Troubleshooting

- If using local source changes, run build first:

```bash
npm run build
```

- If your global `nana` points to another install, run local entrypoint:

```bash
node bin/nana.js setup --force --verbose
node bin/nana.js doctor
```

- If AGENTS.md was not overwritten during `--force`, stop active NANA session and rerun setup.
