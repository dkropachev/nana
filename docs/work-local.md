# Work-local

`nana work-local` runs long local implementation plans against a git-backed repo in a managed sandbox, without submitting anything upstream.

## Commands

```bash
nana work-local start [--repo <path>] (--task <text> | --plan-file <path>) [--max-iterations <n>] [--integration <final|always|never>] [--grouping-policy <ai|path|singleton>] [--validation-parallelism <1-8>] [-- codex-args...]
nana work-local resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
nana work-local status [--run-id <id> | --last | --global-last] [--repo <path>] [--json]
nana work-local logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>] [--json]
nana work-local retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
```

## Storage

`work-local` keeps authoritative runtime state in `~/.nana/local-work/state.db`.

This SQLite-backed control plane currently assumes the repo's Go 1.25 baseline.

Layout:

- `~/.nana/local-work/state.db`
- `~/.nana/local-work/repos/<repo-id>/runs/<run-id>/...`
- `~/.nana/local-work/sandboxes/<repo-id>/<run-id>/repo`

The source repo should not receive `work-local` runtime files.

Key run artifacts:

- `grouping-*-attempt-*.{md,log}` for grouping retries
- `validation-groups/<group>/round-*-validator-attempt-*.{md,log}` for validator retries
- `retrospective.md` for the final markdown summary

Previous JSON state files such as `manifest.json`, `runtime-state.json`, `finding-history.json`, `repo.json`, `latest-run.json`, and `index/runs.json` are not part of the current `work-local` state model and are ignored if they still exist on disk.

## Lookup And Resume

Run lookup is DB-backed:

- `--last` means the latest run for the current repo
- `--repo <path>` supplies repo context when you are outside that checkout

Global lookup is explicit:

- `--run-id <id>` resolves directly through the SQLite state store
- `--global-last` resolves the newest run across all repos from SQLite

Examples:

```bash
nana work-local status --repo ~/src/widget --last
nana work-local status --repo ~/src/widget --last --json
nana work-local logs --repo ~/src/widget --last --tail 80
nana work-local logs --repo ~/src/widget --last --json
nana work-local resume --run-id lw-123456789
nana work-local retrospective --global-last
```

Start-time validation controls:

- `--grouping-policy ai|path|singleton`
  - `ai` is the default
  - `path` groups findings by path/module prefixes without calling the grouper model
  - `singleton` validates each finding independently without calling the grouper model
- `--validation-parallelism <1-8>`
  - caps concurrent validator-group executions
  - default: `4`

## Inspecting Logs

`work-local logs` reads the current iteration artifact directory and prints the available stdout/stderr logs plus verification and review JSON artifacts in one stream.

- default tail: 80 lines per file
- `--tail 0`: print full file contents
- `--json`: return run summary, DB-backed live state summary, grouping metadata, and file payloads as JSON
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

Validation behavior:

- AI grouping retries up to 3 times if the output is malformed or incomplete
- if AI grouping still fails, `work-local` falls back to singleton grouping for that validation phase
- validator groups retry up to 3 times
- if a validator group still fails after retries, the run fails and stays resumable
- rejected finding fingerprints are stored for the current run and are filtered before later validation passes in that run

## Troubleshooting

Common failures:

- `work-local requires a clean repo before start`
  - commit, stash, or discard local changes before starting
- `work-local repo context is required for --last`
  - run inside the repo, or pass `--repo <path>`, `--run-id`, or `--global-last`
- `work-local run <id> was not found in the global index`
  - ensure the run was created by the current home-scoped `work-local` runtime, or use repo-scoped `--last`
- `validator group <group-id> failed after 3 attempt(s)`
  - inspect `status --json`, `logs --json`, and the validator attempt logs under `validation-groups/<group>/`
  - `resume` will reuse completed grouping/validator work and rerun only incomplete or failed validator groups for current-format runs

Useful recovery commands:

```bash
nana work-local status --repo ~/src/widget --last
nana work-local status --repo ~/src/widget --last --json
nana work-local logs --repo ~/src/widget --last --json
nana work-local resume --repo ~/src/widget --last
```
