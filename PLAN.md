# Complete Migration To A Go-Only Nana

## Summary
Migrate Nana to a **Go-only codebase and Go-only distribution model** while preserving the full current user-facing feature set. The end state removes all Rust, JavaScript, TypeScript, Python, and shell implementation code from the repo; static assets such as Markdown, HTML, and CSS may remain. The final product ships as Go binaries only, with no npm package, no Node runtime dependency, no Cargo workspace, and no JS/TS wrapper or bridge layer.

Important public contract decisions:
- Preserve the full existing CLI surface and behavior, including `review`, `work-on`, `review-rules`, `explore`, `sparkshell`, `setup`, `doctor`, team/runtime flows, and persisted `.nana` state behavior.
- Preserve current on-disk/runtime contracts unless a one-time migration is required; Go must read existing state/artifact formats produced by the TS/Rust implementation during the transition.
- Replace the current npm-first and Cargo-based build/release flow defined in `package.json`, `Cargo.toml`, and `.github/workflows` with Go-native build, test, and release pipelines.

## Key Changes
### 1. Freeze contracts and build a migration matrix
- Treat the current TS/Rust implementation under `src` and `crates` as the behavior baseline; create a parity matrix by subsystem before deleting anything.
- Lock user-facing contracts from README/help text, compat fixtures, runtime contract docs, and current Go compat tests into explicit Go test cases before each subsystem rewrite.
- Group the rewrite by subsystem, not file type:
  - CLI/router/install/setup/doctor/agents/session
  - GitHub `review` / `work-on` / `review-rules`
  - runtime/team/hud/hooks/notifications/state
  - explore/sparkshell/mux/runtime helpers currently split across Go wrappers and Rust crates
  - planning/pipeline/keyword routing/catalog generation/support tooling

### 2. Finish the product rewrite in Go
- Move the remaining CLI ownership out of the legacy bridge in `cmd/nana/main.go` until no command path delegates to JS.
- Replace the TS GitHub runtime with Go-native packages covering:
  - `work-on start/sync`
  - full `review <pr-url>` execution
  - `issue investigate`
  - all run discovery, repo onboarding, persisted review/work-on artifacts, and GitHub API access
- Replace the Rust-backed seams with native Go implementations so `cmd/nana-runtime`, `cmd/nana-explore`, and `cmd/nana-sparkshell` stop shelling out to Cargo/Rust binaries.
- Port TS-only subsystems into Go packages under `internal/` or `pkg/` and delete the TS originals only after parity is verified:
  - state/session/history/runtime bridge logic
  - team runtime, tmux/mux integration, HUD, hook execution, notification routing
  - planning/pipeline/workflow orchestration
  - asset generation for prompts/skills/templates/catalogs
- Replace shell and Python helper scripts with Go subcommands or internal Go helpers so the repo contains no non-Go implementation code.

### 3. Replace the build, test, and release system
- Remove npm/Cargo as build-time requirements:
  - delete npm scripts, Node-based test runners, TS build steps, cargo build/test steps, and Rust coverage lanes
  - replace them with `go test`, `gofmt`, `go vet`, and Go-native release/smoke commands
- Replace npm publishing with Go binary releases only:
  - update README and docs to install from Go binaries exclusively
  - simplify release artifacts to cross-compiled Go binaries plus checksums/manifest
  - remove the JS wrapper, npm package contract, and Cargo release asset generation
- Rewrite all remaining TS/Rust verification into Go tests, with a small number of shell-free smoke checks executed from Go test helpers where needed.

### 4. Delete legacy code only behind hard gates
- Delete `src` in subsystem slices only after the matching Go implementation, tests, and docs are complete.
- Delete `crates`, Rust CI lanes, Cargo manifests, and Rust compatibility docs only after the Go explore/runtime/sparkshell/team paths fully replace them.
- Delete `package.json`, Node lockfiles, TS configs, JS/TS test fixtures, Node-centric docs, and npm workflow jobs only after Go-native build/release/docs generation paths are green.
- Keep a single temporary compatibility window where Go can read old `.nana` artifacts; after the migration release, write only the final Go-owned canonical formats.

## Test Plan
- Add subsystem parity tests before deletion:
  - CLI help/smoke/exit-code coverage for every public command
  - persisted `.nana` state compatibility tests using legacy TS/Rust-produced fixtures
  - GitHub review/work-on lifecycle tests covering onboarding, start, sync, followup, rule mining, and persisted artifacts
  - runtime/team/hud/hooks/notifications tests covering session scope, tmux integration, runtime snapshots, and shutdown behavior
  - explore/sparkshell/runtime helper tests covering the current Rust-owned behavior
- Add Go-only distribution tests:
  - binary install smoke
  - release artifact validation
  - cross-platform command smoke for Linux/macOS/Windows
  - “no Node/Cargo required” environment smoke
- Add deletion gates:
  - repo scan test that fails if `.ts`, `.js`, `.rs`, `.mjs`, `.py`, or `.sh` implementation files remain outside approved non-code/static asset exceptions
  - CI gate that fails if Node or Cargo workflows/jobs remain
  - docs/readme checks ensuring install and verification instructions are Go-only

## Assumptions
- Final distribution is **Go binaries only**; npm publishing is removed, not preserved.
- Final implementation language is **Go only**; Markdown/HTML/CSS/static assets may remain, but no Rust, JS/TS, Python, or shell implementation code remains in the repo.
- Full current feature parity is required; no user-facing workflows are intentionally dropped.
- Existing `.nana` data and current CLI/state contracts are preserved unless a one-time migration is explicitly required to complete the rewrite safely.
