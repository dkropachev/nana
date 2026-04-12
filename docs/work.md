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
nana start [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
nana improve [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
nana enhance [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
```

Selection rules:

- local mode is selected when `start` is driven by `--task` or `--plan-file`
- GitHub mode is selected when `start` receives a GitHub issue/PR URL
- `sync` and `lane-exec` are GitHub-only run controls
- top-level `nana start` is proposal-only startup automation: it runs supported scout roles declared by repo policy and does not edit code
- `improve` and `enhance` are proposal-only direct scout role commands

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

## Improvement Runs

`nana improve` inspects the selected repo for UX and performance improvement proposals. `nana enhance` uses the same flow for grounded enhancements that help the repo move forward. Top-level `nana start` detects which scout policies exist and runs only those supported roles.

Local repo behavior:

- proposals are saved under `.nana/improvements/<run-id>/` or `.nana/enhancements/<run-id>/`
- no GitHub APIs are called

GitHub target behavior:

- the repo is inspected from NANA's managed source checkout
- `nana start` runs `improvement-scout` only when an improvement policy exists and `enhancement-scout` only when an enhancement policy exists
- `.github/nana-improvement-policy.json` and `.nana/improvement-policy.json` are read for `improvement-scout`
- `.github/nana-enhancement-policy.json` and `.nana/enhancement-policy.json` are read for `enhancement-scout`
- `.nana/...` takes precedence over `.github/...`
- `issue_destination` controls publication: `local`, `target`, or `fork`
- scout issue labels include the role label
- each scout role emits at most 5 proposals per run and is capped at 5 open GitHub issues at a time

Policy examples:

```json
{"version":1,"issue_destination":"target","labels":["improvement","ux","perf"]}
```

```json
{"version":1,"issue_destination":"fork","fork_repo":"my-user/widget","labels":["improvement"]}
```

## Troubleshooting

Common failures:

- `work requires a clean repo before start`
- `work repo context is required for --last`
- `validator group <group-id> failed after 3 attempt(s)`
