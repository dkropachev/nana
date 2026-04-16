# Improvement Proposals

Repo: nana2
Generated: 2026-04-13T00:13:34Z

These are improvement proposals, not enhancement requests.

## 1. Make the README install path copyable

- Area: UX
- Labels: improvement, improvement-scout, local-scout, ux
- Confidence: high

Tighten the README's recommended default flow into one balanced, copyable install block and keep the first-session commands visually separate.

Rationale: The README is the first onboarding surface for a CLI tool. A malformed or visually confusing install block increases the chance that new users copy partial commands or miss setup steps.

Evidence: README.md:47-59 opens a bash fence at line 51, then starts another ```bash fence at line 55 before closing the first block. The same section is the primary recommended default flow for new users.

Impact: Fewer onboarding mistakes and a more trustworthy first-run path for users installing the native binary and Codex dependency.

Files: README.md

Suggested next step: Rewrite README.md:47-59 as one shell block for install/setup, then add a lightweight markdown fence check to the docs or CI path.

## 2. Make doctor validate executable toolchains

- Area: UX
- Labels: improvement, improvement-scout, local-scout, ux
- Confidence: high

Improve `nana doctor` so toolchain readiness checks execute and parse the tools they report as ready, instead of relying on path presence or string comparison.

Rationale: Doctor output is a recovery path. If it reports an unusable Go or Node install as ready, users get sent into later failures with less context.

Evidence: internal/gocli/doctor.go:179-193 parses Node's major version with string comparison, so versions like v9 can compare incorrectly against "20". internal/gocli/doctor.go:220-221 reports the Explore Harness ready when any `go` is on PATH. In this environment, `which go` returns /snap/bin/go, but `go version` exits with a snap-confine permissions error.

Impact: More accurate setup diagnostics and faster recovery for users with broken PATH, snap, or too-old runtime installations.

Files: internal/gocli/doctor.go

Suggested next step: Change Node version parsing to numeric semver parsing, and replace the Go `LookPath` readiness branch with a bounded `go version` or minimal harness command execution check.

## 3. Speed up session history search on large Codex homes

- Area: Perf
- Labels: improvement, improvement-scout, local-scout, perf
- Confidence: medium

Reduce startup and scan cost in `nana session search` by pruning candidate rollout files before full sorting and per-line JSON scanning.

Rationale: Session history grows over time, and search is an interactive CLI path. Walking every transcript and sorting all matching paths can make common targeted searches slower than necessary.

Evidence: internal/gocli/session.go:174 calls listRolloutFiles over the entire sessions tree. internal/gocli/session.go:255-277 walks every file and reverse-sorts all rollout JSONL paths. internal/gocli/session.go:195-207 then stats and scans files until the result limit is met, while `--session`, `--project`, and `--since` filtering happen after the full file list is collected.

Impact: Lower latency and less disk I/O for users with months of Codex transcripts, especially when using `--session`, `--since`, or small result limits.

Files: internal/gocli/session.go

Suggested next step: Add a benchmark fixture with many rollout files, then change candidate selection to prune by date/session path where possible and avoid full-list sorting when only the newest limited results are needed.

## 4. Make sparkshell summary latency predictable

- Area: Perf
- Labels: improvement, improvement-scout, local-scout, perf, ux
- Confidence: medium

Document and expose the existing sparkshell summary thresholds so users know when a command will be summarized by Codex and how to tune that behavior.

Rationale: `nana sparkshell` is used around shell commands where responsiveness matters. Today, output over a small threshold silently switches from raw output to a Codex summarization path with a long timeout.

Evidence: cmd/nana-sparkshell/main.go:22-27 sets a 12-line raw-output threshold, 60,000 ms summary timeout, and 24 KB summary prompt cap. cmd/nana-sparkshell/main.go:163-170 invokes summarization whenever visible output exceeds the threshold. internal/gocli/sparkshell.go:10 and cmd/nana-sparkshell/main.go:188-193 show usage text that does not mention summary triggering, `NANA_SPARKSHELL_LINES`, or `NANA_SPARKSHELL_SUMMARY_TIMEOUT_MS`.

Impact: More predictable command latency and fewer surprises when wrapping noisy test/build output or tmux pane capture.

Files: cmd/nana-sparkshell/main.go, internal/gocli/sparkshell.go

Suggested next step: Update both sparkshell usage surfaces to state the summary trigger and existing environment controls, then add help-output tests that keep the latency controls discoverable.

## 5. Run real Go tests in CI before native builds

- Area: Perf
- Labels: improvement, improvement-scout, local-scout, perf
- Confidence: high

Make the CI `Go Test` job execute the repository's Go test suite, and consider a small benchmark-smoke target for performance-sensitive CLI paths.

Rationale: This repo has performance-aware workflows and benchmark detection logic, but the current CI test job only sets up Go. Running tests before builds keeps regressions in CLI behavior, runtime state, and performance-related code paths from reaching release packaging.

Evidence: .github/workflows/ci.yml:40-48 defines a `Go Test` job with checkout and setup-go steps but no `go test` run step. internal/gocli/work_local_test.go contains benchmark-plan coverage, and internal/gocli/verification_scripts.go:44 writes benchmark verification scripts, so performance/benchmark paths already exist but are not exercised by this CI job.

Impact: Higher confidence in release artifacts and earlier detection of regressions in UX/performance-sensitive command paths.

Files: .github/workflows/ci.yml, internal/gocli/work_local_test.go, internal/gocli/verification_scripts.go

Suggested next step: Add `go test ./...` to the CI test job, then evaluate a bounded benchmark smoke such as targeted tests around verification-plan benchmark detection rather than full expensive benchmarks.
