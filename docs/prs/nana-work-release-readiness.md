# Nana Work Release Readiness

## Baseline

Commit: `2a5bfa3 Unify work execution under nana work`

This readiness pass treats the committed `nana work` merge as the feature baseline. It does not add runtime behavior; it records release validation, smoke coverage, and remaining watch items.

## Automated Verification

```bash
go test ./cmd/nana ./internal/gocli/...
go vet ./cmd/nana ./internal/gocli/...
git diff --check
```

Results: all passed.

## Manual Smoke

Built a temporary local binary with:

```bash
go build -o <tmp>/nana ./cmd/nana
```

Verified:

```bash
<tmp>/nana help
<tmp>/nana help work
<tmp>/nana help work-local
<tmp>/nana help work-on
```

Expected behavior confirmed:

- `nana help work` shows the unified local and GitHub-backed runtime surface.
- `nana help work-local` shows migration guidance and the `nana work` help.
- `nana help work-on` shows migration guidance and the `nana work` help.

## Compatibility Boundaries

- `work-local` and `work-on` remain removed as live entrypoints.
- `.nana/work-on-concerns.json` and `.nana/work-on-hot-path-apis.json` remain stable compatibility filenames.
- Existing GitHub work manifests remain readable artifacts and fallback discovery sources.
- `nana review` storage remains separate from the shared work index.

## Watch Items

- Users may expect `nana work-on ...` to forward rather than fail with migration guidance.
- Existing GitHub work runs are indexed only after they are touched or discovered through fallback lookup.
- The retained `work-on-*` compatibility filenames may need clearer migration docs if users confuse them with command names.
