# Go Migration Parity Matrix

This matrix is the baseline contract for the full Nana migration to a Go-only codebase.
It tracks the current implementation owner, the required Go destination, and the migration gate for each major subsystem.

End-state rules:
- No Rust implementation code remains.
- No JavaScript or TypeScript implementation code remains.
- No Python or shell implementation code remains.
- Full current CLI and workflow parity is preserved.
- Distribution is Go binaries only.

| Area | Current owner | Required Go destination | End-state requirement | Migration gate |
|---|---|---|---|---|
| Top-level CLI routing, launch, setup, doctor, agents, session | `cmd/nana`, `internal/gocli`, `src/cli`, `src/session-history`, `src/agents` | `cmd/nana`, `internal/gocli`, new Go support packages under `internal/` | No command path delegates to JS; no npm wrapper remains | Go command parity tests cover help, routing, exit codes, install/setup/doctor/session flows |
| GitHub review/work-on/review-rules runtime | `src/cli/github.ts` | new Go GitHub runtime packages under `internal/` | `review`, `work-on`, `issue`, and `review-rules` are fully Go-native | Go tests cover onboarding, start, sync, followup, rule mining, persisted artifacts, and PR review execution |
| Runtime bridge and runtime state IO | `src/runtime`, `src/state`, `src/compat`, `cmd/nana-runtime` | `cmd/nana-runtime`, Go runtime/state packages under `internal/` | No Rust runtime binary, no TS bridge layer | Go tests cover snapshot/command/event/state compatibility using legacy fixtures |
| Explore and sparkshell helpers | `src/cli/explore.ts`, `src/cli/sparkshell.ts`, `cmd/nana-explore`, `cmd/nana-sparkshell` | `cmd/nana-explore`, `cmd/nana-sparkshell`, Go support packages under `internal/` | No Cargo build path, no Rust helper binaries | Go tests cover help, prompt routing, packaging, and native binary behavior |
| Team runtime, mux/tmux, HUD, notifications, hooks | `src/team`, `src/hud`, `src/notifications`, `src/hooks` | Go runtime/team/mux packages under `internal/` | Team and tmux surfaces are fully Go-native | Go tests cover session scope, tmux state, worker lifecycle, HUD rendering, and hook execution |
| Planning, pipeline, modes, workflow routing | `src/planning`, `src/pipeline`, `src/modes`, `src/hooks/keyword-*`, `src/verify-loop`, `src/ralplan`, `src/autoresearch` | Go planning/workflow packages under `internal/` | Workflow selection and persisted mode state are Go-native | Go tests cover keyword routing, mode lifecycle, planning/workflow persistence, and parity contracts |
| Catalog, prompts, skills, generated assets | `src/catalog`, `src/scripts`, `internal/gocliassets/generated` | Go asset/catalog generation packages under `internal/` and checked-in generated artifacts | No TS generators remain; Go owns prompt/skill/template generation | Go tests verify generated catalogs/assets and prompt/skill/template install behavior |
| MCP and integration servers | `src/mcp`, `src/openclaw`, `src/subagents`, `src/visual` | Go integration packages under `internal/` or dedicated `cmd/` entrypoints | No Node-based MCP server/runtime code remains | Go tests cover server lifecycle, protocol contracts, and integration fixtures |
| Build, release, packaging, verification | `package.json`, npm scripts, Cargo workspace, `.github/workflows`, `scripts/*.mjs`, `src/verification` | Go-native build/release tooling, Go tests, and Go-based verification helpers | No npm/Cargo build or publish flow remains | CI uses Go-only build/test/release jobs and repo scan gates reject JS/TS/Rust implementation code |

## Deletion Gates

- `src/` can be deleted only after all rows that currently depend on TS/JS are green under Go parity tests.
- `package.json`, TS configs, Node workflows, and JS/TS tests can be deleted only after Go replaces build, release, verification, and distribution.
- Any temporary compatibility reader for legacy `.nana` artifacts must be explicitly called out in tests before legacy writers are removed.
