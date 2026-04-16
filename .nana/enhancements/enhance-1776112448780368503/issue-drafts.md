# Enhancement Proposals

Repo: nana2
Generated: 2026-04-13T16:33:00-04:00

These are enhancement proposals intended to help the repo move forward.

## 1. Generate a CLI command reference and add it to the docs nav

- Area: UX
- Labels: enhancement, enhancement-scout, local-scout, ux, docs
- Confidence: high

Publish a command-reference page sourced from the existing help text and make the work/automation surfaces reachable from the static docs navigation.

Rationale: The repo already has a large, structured CLI surface, but the public docs stop at high-level pages. Users currently have to bounce between README and terminal help to discover how work, start, improve, enhance, and repo automation fit together.

Evidence: cmd/nana/main.go contains a substantial top-level helpText; internal/gocli/start.go defines StartHelp; internal/gocli/improve.go defines ImproveHelp and EnhanceHelp. Meanwhile docs/index.html, docs/getting-started.html, docs/agents.html, docs/skills.html, and docs/integrations.html share a primary nav with Home/Getting Started/Agents/Skills/Integrations only, even though docs/work.md exists and README.md links to it.

Impact: Improves onboarding and discoverability for the current product surface without adding new runtime behavior.

Files: cmd/nana/main.go, internal/gocli/start.go, internal/gocli/improve.go, docs/index.html, docs/getting-started.html, docs/work.md

Suggested next step: Add one generated docs page for command reference from the existing help constants, then add CLI/Work links to the shared docs nav.

## 2. Make install and release docs version-aware

- Area: UX
- Labels: enhancement, enhancement-scout, local-scout, ux, docs
- Confidence: high

Replace placeholder download URLs and stale homepage release copy with generated install guidance tied to the current version and release manifest.

Rationale: First-run docs should be copy-pasteable and obviously current. The repo already generates the release metadata needed to keep install instructions and landing-page release text synchronized.

Evidence: docs/index.html still advertises "What's New in 0.6.0" and shows curl -L -o nana <release-binary-url>; docs/getting-started.html uses the same placeholder. VERSION is 0.11.12. .github/workflows/release.yml generates native-release-manifest.json, and internal/gocli/update.go already consumes the latest manifest URL from GitHub Releases.

Impact: Reduces install friction, increases trust that docs match the shipped binary, and avoids stale release messaging on the public docs site.

Files: docs/index.html, docs/getting-started.html, README.md, VERSION, .github/workflows/release.yml, internal/gocli/update.go

Suggested next step: Generate a canonical install snippet and current release summary from VERSION plus native-release-manifest.json, then reuse it in README.md, docs/index.html, and docs/getting-started.html.

## 3. Document the default nana start web console

- Area: UX
- Labels: enhancement, enhancement-scout, local-scout, ux
- Confidence: high

Add a dedicated operator guide for the loopback API and web console that nana start launches by default.

Rationale: The start UI already looks like a first-class operator surface, but it is only discoverable through help text and runtime stdout. That leaves a useful workflow under-explained for users adopting repo automation.

Evidence: internal/gocli/start.go says nana start "launches loopback UI services by default: REST API + web console"; internal/gocli/start_ui.go launches both servers and prints [start-ui] API and Web URLs; internal/gocli/start_ui_test.go covers overview, mutations, logs, scout items, and planned-item actions; docs/work.md and docs/getting-started.html do not provide a dedicated walkthrough for this UI.

Impact: Makes repo automation easier to operate, lowers confusion about the extra local ports, and helps users adopt an existing surface that already has meaningful test coverage.

Files: internal/gocli/start.go, internal/gocli/start_ui.go, internal/gocli/start_ui_test.go, docs/work.md, docs/getting-started.html

Suggested next step: Add a focused docs section or page for the start UI with one end-to-end flow, then link it from nana help start and the docs nav.

## 4. Cache or incrementalize start UI overview polling

- Area: Perf
- Labels: enhancement, enhancement-scout, local-scout, perf
- Confidence: medium

Reduce repeated filesystem and SQLite work in the default start UI by caching overview snapshots or invalidating them on state changes instead of rebuilding them on every SSE tick.

Rationale: The start web console is enabled by default, and its live event stream currently recomputes the full overview on a fixed interval even when nothing changed.

Evidence: internal/gocli/start_ui.go handleEvents calls buildEventsPayload() every 2 seconds per client. buildOverview() then calls listStartUIRepoSummaries(false), loadStartUIWorkRuns(10), loadStartUIWorkItemsWithHiddenCount(10), and HUD loading. listStartUIRepoSlugs() walks ~/.nana/start and onboarded repo state. docs/work.md describes nana start as a forever-running automation loop.

Impact: Cuts idle CPU and IO overhead for long-running sessions and should scale better as the number of onboarded repos and active work items grows.

Files: internal/gocli/start_ui.go, internal/gocli/start_ui_test.go, docs/work.md

Suggested next step: Add a synthetic multi-repo benchmark for buildOverview/buildEventsPayload, then introduce a cached snapshot invalidated by state-file or DB changes.

## 5. Put existing Go benchmarks on a CI or scheduled regression track

- Area: Perf
- Labels: enhancement, enhancement-scout, local-scout, perf
- Confidence: high

Start running the repo's existing Go benchmarks in automation and publish benchmem output for core CLI and runtime hot paths.

Rationale: The project already has targeted benchmarks for interactive paths, but performance regressions will currently slip until someone remembers to run them manually.

Evidence: Makefile exposes a benchmark target using go test -run=^$$ -bench=. -benchmem ./.... internal/gocli/runtime_benchmark_test.go benchmarks CLI invocation resolution, work-run index lookups, GitHub manifest resolution, and verification-plan detection. internal/gocli/session_test.go includes a session history benchmark. .github/workflows/ci.yml currently runs fmt, vet, test, docs, and build, but no benchmark job.

Impact: Improves confidence when shipping runtime changes and catches latency or allocation regressions before they accumulate in core workflows.

Files: Makefile, internal/gocli/runtime_benchmark_test.go, internal/gocli/session_test.go, .github/workflows/ci.yml

Suggested next step: Add a non-blocking benchmark workflow on PRs or a nightly schedule that runs go test -run=^$$ -bench=. -benchmem ./... and publishes the results as artifacts or step summaries.
