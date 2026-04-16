# Performance TODO

Reviewed on 2026-04-15 at commit `761cf4f`.

## P1 — fix soon

- [x] Rework `cmd/nana-runtime` state persistence (`cmd/nana-runtime/main.go:198-219`, `cmd/nana-runtime/main.go:707-770`) so `exec` does not reload, replay, and rewrite the full event log plus multiple JSON views on every command. The current flow is O(history) per command and trends toward O(n²) total work for long-lived sessions; switch to append-only event writes, incremental snapshots, and dirty-file updates.
- [x] Stop using `go test ./...` as the Go "compile" heuristic (`internal/gocli/github_investigate.go:623-627`). Even though later stages cache exact command strings, the compile phase still executes the entire test suite instead of a build-only check, which makes every verification pass much slower than necessary. Use `go test -run '^$' ./...`, `go build ./...`, or collapse the redundant stage.
- [x] Cache tracked repo files during GitHub investigation (`internal/gocli/github_investigate.go:128-136`, `internal/gocli/github_investigate.go:355-460`). `inferGithubInitialRepoConsiderations` and `inferGithubHotPathProfile` each call `trackedRepoFiles`, so large repos pay for two separate `git ls-files`/filesystem scans during onboarding. Pass one shared slice through both passes, and precompile the token-splitting regexp used inside the hot-path loop.

## P2 — worthwhile follow-ups

- [x] Reuse a shared `http.Client`/transport for GitHub API reads (`internal/gocli/github_defaults.go:2003-2022`). Constructing a fresh client on every request prevents connection reuse and adds avoidable latency in API-heavy flows like repo profiling and sync.
- [x] Hoist regex compilation out of hot paths in text normalization and uninstall cleanup (`internal/gocli/github_defaults.go:2137-2140`, `internal/gocli/uninstall.go:277-287`). These helpers compile the same patterns on every call or every line, which is a low-risk but measurable waste under review-heavy or large-config workloads.
