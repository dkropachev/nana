# Work-local

`nana work-local` runs long local implementation plans against a git-backed repo in a managed sandbox, without submitting anything upstream.

## Commands

```bash
nana work-local start [--repo <path>] (--task <text> | --plan-file <path>) [--max-iterations <n>] [--integration <final|always|never>] [-- codex-args...]
nana work-local resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
nana work-local status [--run-id <id> | --last | --global-last] [--repo <path>]
nana work-local logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>]
nana work-local retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
```

## Storage

`work-local` keeps runtime state under `~/.nana/local-work/`.

Layout:

- `~/.nana/local-work/repos/<repo-id>/repo.json`
- `~/.nana/local-work/repos/<repo-id>/latest-run.json`
- `~/.nana/local-work/repos/<repo-id>/runs/<run-id>/...`
- `~/.nana/local-work/sandboxes/<repo-id>/<run-id>/repo`
- `~/.nana/local-work/index/runs.json`

The source repo should not receive `work-local` runtime files.

## Lookup And Resume

Default lookup is repo-scoped:

- `--last` means the latest run for the current repo
- `--repo <path>` supplies repo context when you are outside that checkout

Global lookup is explicit:

- `--run-id <id>` resolves directly through the global index
- `--global-last` resolves the newest indexed run across all repos

Examples:

```bash
nana work-local status --repo ~/src/widget --last
nana work-local logs --repo ~/src/widget --last --tail 80
nana work-local resume --run-id lw-123456789
nana work-local retrospective --global-last
```

## Inspecting Logs

`work-local logs` reads the current iteration artifact directory and prints the available stdout/stderr logs plus verification and review JSON artifacts in one stream.

- default tail: 80 lines per file
- `--tail 0`: print full file contents
- works with the same `--run-id`, `--last`, `--global-last`, and `--repo` selectors as `status`

## Review And Hardening Loop

Each outer iteration runs:

1. implementation pass
2. shell verification
3. reviewer pass
4. hardening pass when verification or review fails
5. post-hardening verification
6. post-hardening review

`work-local` caps hardening/review rounds per outer iteration. If findings or failures still remain, the run either retries another outer iteration or stops on stall/max-iteration conditions.

## Troubleshooting

Common failures:

- `work-local requires a clean repo before start`
  - commit, stash, or discard local changes before starting
- `work-local repo context is required for --last`
  - run inside the repo, or pass `--repo <path>`, `--run-id`, or `--global-last`
- `work-local run <id> was not found in the global index`
  - ensure the run was created by the current home-scoped `work-local` runtime, or use repo-scoped `--last`

Useful recovery commands:

```bash
nana work-local status --repo ~/src/widget --last
nana work-local resume --repo ~/src/widget --last
```
