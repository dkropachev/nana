# Improvement Proposals

Repo: nana2
Generated: 2026-04-13T16:30:14-04:00

These are improvement proposals, not enhancement requests.

## 1. Refresh the public docs surface to the current 0.11.x command contract

- Area: UX
- Labels: improvement, improvement-scout, local-scout, ux
- Confidence: high

Update the README and GitHub Pages docs so the first-run story, release framing, and launch guidance all match the current binary and reasoning defaults.

Rationale: The highest-friction path in this repo is onboarding, and the public docs currently give conflicting guidance about which launch flags to use and what release users are looking at.

Evidence: docs/index.html still headlines "What's New in 0.6.0" and recommends `nana --xhigh --madmax`; docs/getting-started.html repeats that launch shape. README.md recommends `nana --madmax --high`, also documents `nana --fast`, and explains `nana config set --effort xhigh` plus `--effort`. The current built binary reports `nana v0.11.12`, so the public site and README are no longer presenting one coherent 0.11.x story.

Impact: Lower first-run drop-off, fewer support loops about `--high` vs `--xhigh`, and better docs trustworthiness for new users.

Files: README.md, docs/index.html, docs/getting-started.html

Suggested next step: Pick one canonical install snippet and one canonical first-launch command for 0.11.x, update README.md plus docs/index.html and docs/getting-started.html together, and move alternate launch flags into a short tuning section.

## 2. Make `nana start` mode selection explicit in help and runtime output

- Area: UX
- Labels: improvement, improvement-scout, local-scout, ux
- Confidence: high

Expose `nana start` as a dual-purpose command with clearly named automation and scout modes, instead of relying on implicit argument-shape routing.

Rationale: Today the command surface is harder to learn than it needs to be because users can cross from automation into scout execution by changing flags, without any visible mode confirmation.

Evidence: internal/gocli/start.go defines StartHelp only for onboarded-repo automation. internal/gocli/improve.go defines a separate ScoutStartHelp for scout startup. internal/gocli/start.go:startShouldRunScouts() silently reroutes based on flags like `--focus`, `--from-file`, `--dry-run`, `--local-only`, and repo-path-shaped arguments. docs/work.md has to explain both meanings of `nana start` in separate sections, which matches the command ambiguity in the code.

Impact: Better CLI discoverability, fewer accidental long-running runs, and fewer cases where users invoke the wrong `start` behavior.

Files: internal/gocli/start.go, internal/gocli/improve.go, docs/work.md

Suggested next step: Rewrite `nana help start` to show two explicit modes with one example each, and print a one-line mode banner before execution begins.

## 3. Turn `nana doctor` results into direct remediation commands

- Area: UX
- Labels: improvement, improvement-scout, local-scout, ux
- Confidence: high

Have doctor checks emit the next command or fix path for that specific failure, instead of ending mostly with generic setup rerun advice.

Rationale: The repo already knows the right recovery action for several failure classes, but the top-level summary collapses them into `nana setup` or `nana setup --force`, which is often the wrong operational next step.

Evidence: internal/gocli/doctor.go prints generic summary advice after all checks: rerun setup on failures or `setup --force` on warnings. The same file already has check-specific remediation knowledge: explore harness checks reference Go or `NANA_EXPLORE_BIN`; investigate checks point to `nana investigate onboard` and `nana investigate doctor`; legacy skill overlap checks point at duplicate skill roots. docs/getting-started.html troubleshooting is similarly broad and mostly routes users back to setup or doctor.

Impact: Faster issue recovery, fewer repeated setup attempts, and a more credible diagnostics experience for first-time users.

Files: internal/gocli/doctor.go, docs/getting-started.html

Suggested next step: Add a `next_step` field to doctor checks, surface the highest-priority command in the summary block, and mirror those actions in the Getting Started troubleshooting table.

## 4. Stop rebuilding the full `nana start` overview on every SSE heartbeat

- Area: Perf
- Labels: improvement, improvement-scout, local-scout, perf
- Confidence: high

Cache or delta-update the start UI overview instead of recomputing it from disk and SQLite every two seconds even when nothing changed.

Rationale: The dashboard is launched by default for `nana start`, so idle refresh cost matters. The current event stream does repeated filesystem and database work just to discover that the payload hash is unchanged.

Evidence: internal/gocli/start_ui.go:handleEvents uses a 2-second ticker and calls buildEventsPayload() on every tick. buildEventsPayload() calls buildOverview(), which calls listStartUIRepoSummaries(), loadStartUIWorkRuns(), loadStartUIWorkItemsWithHiddenCount(), and h.loadHUD(). listStartUIRepoSummaries() walks `~/.nana/start`; loadStartUIWorkRuns() reopens SQLite and reads manifests; loadStartUIWorkItemsWithHiddenCount() opens SQLite again; internal/gocli/hud.go:readAllHUDState() repeatedly rescans mode-state files for each HUD mode.

Impact: Lower idle CPU and disk churn, smoother web console responsiveness, and better scaling as repo count, run history, and state files grow.

Files: internal/gocli/start_ui.go, internal/gocli/hud.go, internal/gocli/work_local.go, internal/gocli/work_items.go

Suggested next step: Build a cached overview keyed by state mtimes or last-updated markers, batch HUD state discovery into one pass, and reuse the prior payload until an actual state change occurs.

## 5. Reuse one repo scan across GitHub work-start profiling

- Area: Perf
- Labels: improvement, improvement-scout, local-scout, perf
- Confidence: high

Create a single repo-scan snapshot for GitHub work start and reuse it across verification-plan detection, default consideration inference, and hot-path API profiling.

Rationale: The current work-start path rescans the same checkout several times before any user work begins, which adds fixed startup latency on medium and large repos.

Evidence: internal/gocli/github_start.go:startGithubWork() runs detectGithubVerificationPlan(paths.SourcePath), inferGithubInitialRepoConsiderations(paths.SourcePath, ...), and inferGithubHotPathProfile(paths.SourcePath, ...) back-to-back, then re-runs detectGithubVerificationPlan() after cloning into the sandbox. internal/gocli/github_investigate.go shows that both inferGithubInitialRepoConsiderations() and inferGithubHotPathProfile() call trackedRepoFiles(), which shells out to `git ls-files` and falls back to a full tree walk.

Impact: Faster `nana work start` and related GitHub onboarding flows, fewer subprocess launches, and less repo-wide I/O before the actual task starts.

Files: internal/gocli/github_start.go, internal/gocli/github_investigate.go

Suggested next step: Introduce a shared repo-scan snapshot that captures tracked files and derived path metadata once per checkout state, then thread it through verification-plan and profiling helpers.
