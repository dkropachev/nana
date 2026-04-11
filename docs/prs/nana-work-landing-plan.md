# Nana Work Landing Plan

## Purpose

The current branch merges the local and GitHub work surfaces under `nana work`, adds a shared work run index, and cleans the public command contract. The diff is intentionally broad and touches shared files, so landing should use the slices below rather than a file-by-file split that would leave intermediate commits unbuildable.

## Landing Slices

1. Public CLI and docs contract
   - Add the `nana work` command surface and legacy migration errors.
   - Update README, live docs, and checked-in site pages to point at `nana work`.
   - Keep `work-on-*` override filenames documented as compatibility filenames.

2. Shared work runtime index
   - Add `work_run_index` to `~/.nana/work/state.db`.
   - Register local and GitHub-backed work runs in the shared index.
   - Resolve GitHub runs from the index first, with manifest and `latest-run.json` fallbacks.

3. Internal naming convergence
   - Rename active `GithubWorkOn*` internals to `GithubWork*`.
   - Rename the old exported `WorkLocal` command helper to `runLocalWorkCommand`.
   - Remove dead normalizers that still returned legacy `work-on` argv prefixes.

4. Regression hardening
   - Keep docs/help parity tests for curated live surfaces.
   - Keep shared-index tests for local and GitHub work runs.
   - Keep the active-source naming guard for `GithubWorkOn`, `githubWorkOn`, and `WorkLocal(`.

## Verification Gates

Run after each slice:

```bash
gofmt -w <touched-go-files>
go test ./cmd/nana ./internal/gocli/...
```

Before final merge:

```bash
rg -n "GithubWorkOn|githubWorkOn|WorkLocal\\(" cmd internal/gocli
rg -n "nana work-on|nana work-local" README.md docs internal/gocli cmd/nana
go test ./cmd/nana ./internal/gocli/...
```

Allowed legacy mentions:

- migration output for removed `work-on` and `work-local` commands
- `docs/work-local.md`
- compatibility filenames such as `.nana/work-on-concerns.json`
- archival/reference documentation that describes historical migration state

## Deferred Follow-Ups

- Rename lower-level `localWork*` backend internals only if they keep causing confusion.
- Unify `nana review` storage and run discovery with the shared work index in a separate runtime-focused change.
- Decide whether compatibility override filenames should ever be migrated away from `work-on-*`.
