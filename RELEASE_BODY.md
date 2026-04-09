# nana Go-only migration cut

**Go-only release surface with native hooks, native update flow, and Node/TypeScript product removal**

This release completes the migration of NANA itself to a Go-only runtime and distribution model. The shipping CLI, setup assets, release tooling, and verification paths are now Go-native.

## Highlights

- native release binaries are now the only supported NANA distribution path
- `nana ask` no longer depends on a Node advisor-script bridge
- `nana hooks` now executes native executable hooks instead of `.mjs` plugins
- build/release/version/update paths are Go-native and read from `VERSION`

## What’s Changed

### Changed
- replace `package.json` version ownership with `VERSION`
- replace npm/Cargo workflow jobs with Go-native CI and release jobs
- remove the TypeScript/JavaScript product tree and root npm metadata
- regenerate embedded setup assets from the current `prompts/`, `skills/`, and `templates/` trees

## Verification

- `gofmt -w ...`
- `go vet ./...`
- `go test ./...`
- repo scan confirms no remaining tracked JS/TS/MJS/Python/shell implementation files in the live tree

## Remaining risk

- Windows self-update still falls back to a manual install path.
- Historical release notes and QA documents still contain legacy npm/Node commands as archive material.

## Contributors

- [@Yeachan-Heo](https://github.com/Yeachan-Heo) (Bellman)

**Full Changelog**: [`v0.11.11...v0.11.12`](https://github.com/Yeachan-Heo/nana/compare/v0.11.11...v0.11.12)
