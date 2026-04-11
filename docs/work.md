# Work

`nana work` is the unified implementation runtime for both local repo work and GitHub-backed issue/PR work.

## Commands

```bash
nana work start [<github-issue-or-pr-url>] [--repo <path>] [--task <text> | --plan-file <path>] [--max-iterations <n>] [--integration <final|always|never>] [--grouping-policy <ai|path|singleton>] [--validation-parallelism <1-8>] [--considerations <list>] [--role-layout <split|reviewer+executor>] [--new-pr] [--create-pr | --local-only] [--reviewer <login|@me>] [-- codex-args...]
nana work resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
nana work status [--run-id <id> | --last | --global-last] [--repo <path>] [--json]
nana work logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>] [--json]
nana work retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
nana work verify-refresh [--run-id <id> | --last | --global-last] [--repo <path>]
nana work sync [--run-id <id> | --last] [--reviewer <login|@me>] [--resume-last] [-- codex-args...]
nana work lane-exec --run-id <id>|--last --lane <alias> [--task <text>] [-- codex-args...]
```

Selection rules:

- local mode is selected when `start` is driven by `--task` or `--plan-file`
- GitHub mode is selected when `start` receives a GitHub issue/PR URL
- `sync` and `lane-exec` are GitHub-only run controls

## Storage

`work` keeps authoritative runtime state in `~/.nana/work/state.db`.

This SQLite-backed control plane currently assumes the repo's Go 1.25 baseline.

Layout:

- `~/.nana/work/state.db`
- `~/.nana/work/repos/<repo-id-or-owner/repo>/runs/<run-id>/...`
- `~/.nana/work/sandboxes/<repo-id>/<run-id>/repo`

The source repo should not receive `work` runtime files.

Previous JSON state files such as `manifest.json`, `runtime-state.json`, `finding-history.json`, `repo.json`, `latest-run.json`, and `index/runs.json` are not part of the current runtime state model and are ignored if they still exist on disk.

## Local Execution Loop

Local-backed runs keep the existing iterative implementation flow:

1. implementation pass
2. shell verification
3. reviewer pass
4. hardening pass when verification or review fails
5. post-hardening verification
6. post-hardening review

Validation behavior:

- AI grouping retries up to 3 times if the output is malformed or incomplete
- if AI grouping still fails, `work` falls back to singleton grouping for that validation phase
- validator groups retry up to 3 times
- if a validator group still fails after retries, the run fails and stays resumable
- `resume` will reuse completed grouping/validator work and rerun only incomplete or failed validator groups for current-format runs

Start-time validation controls:

- `--grouping-policy ai|path|singleton`
- `--validation-parallelism <1-8>`
- `nana work status --last --json`

## GitHub Runs

GitHub-backed runs use the same shared runtime home, plus GitHub-specific wrappers:

- managed repo state: `~/.nana/work/repos/<owner>/<repo-name>`
- reviewer feedback refresh: `nana work sync`
- isolated lane execution: `nana work lane-exec`
- verification artifact refresh: `nana work verify-refresh`

Repo overrides still live in the source checkout:

- `.nana/work-on-concerns.json`
- `.github/nana-work-on-concerns.json`
- `.nana/work-on-hot-path-apis.json`
- `.github/nana-work-on-hot-path-apis.json`

## Troubleshooting

Common failures:

- `work requires a clean repo before start`
- `work repo context is required for --last`
- `validator group <group-id> failed after 3 attempt(s)`
