# Contributing to nana

Thanks for contributing.

## Development setup

- Go 1.24+

```bash
go test ./...
go vet ./...
go run ./cmd/nana-build build-go-cli
```

For local CLI testing:

```bash
./bin/nana setup
nana setup
nana doctor
```

### Release-readiness local verification

Run this sequence locally:

```bash
gofmt -w $(find cmd internal -type f -name '*.go')
go vet ./...
go test ./...
```

If you were recently in a team worker session, clear team env vars first so tests do not inherit worker-specific state roots:

```bash
unset NANA_TEAM_WORKER NANA_TEAM_STATE_ROOT NANA_TEAM_LEADER_CWD NANA_TEAM_WORKER_CLI NANA_TEAM_WORKER_CLI_MAP NANA_TEAM_WORKER_LAUNCH_ARGS
```

## Project structure

- `cmd/` -- Go command entrypoints
- `internal/` -- Go implementation packages
- `prompts/` -- 30 agent prompt markdown files (installed to `~/.codex/prompts/`)
- `skills/` -- 39 skill directories with `SKILL.md` (installed to `~/.codex/skills/`)
- `templates/` -- `AGENTS.md` orchestration brain template

### Adding a new agent prompt

1. Create `prompts/my-agent.md` with the agent's system prompt
2. Run `nana setup --force` to install it to `~/.codex/prompts/`
3. Use `/prompts:my-agent` in Codex CLI

### Prompt guidance contract

Before changing `AGENTS.md`, `templates/AGENTS.md`, or `prompts/*.md`, read [`docs/prompt-guidance-contract.md`](./docs/prompt-guidance-contract.md).

That document defines the GPT-5.4 behavior contract contributors should preserve across prompt surfaces and explains how it differs from posture-aware routing metadata.

### Adding a new skill

Read the [skill contribution guide](./docs/skills.md) before adding triggers or runtime docs.

1. Create `skills/my-skill/SKILL.md` with the skill workflow
2. Check trigger wording and fallback behavior against the guide
3. Run `nana setup --force` to install it to `~/.codex/skills/`
4. Use `$my-skill` in Codex CLI

## Workflow

1. Create a branch from `main`.
2. Make focused changes.
3. Run lint, build, and tests locally.
4. Open a pull request using the provided template.

## Commit style

Use concise, intent-first commit messages. Existing history uses prefixes like:

- `feat:`
- `fix:`
- `docs:`
- `chore:`

Example:

```text
docs: clarify setup steps for Codex CLI users
```

## Pull request checklist

- [ ] Scope is focused and clearly described
- [ ] `go vet ./...` passes
- [ ] `go test ./...` passes
- [ ] `go run ./cmd/nana-build build-go-cli` passes
- [ ] Documentation updated when behavior changed
- [ ] No unrelated formatting/refactor churn

## Reporting issues

Use the GitHub issue templates for bug reports and feature requests, including reproduction steps and expected behavior.
