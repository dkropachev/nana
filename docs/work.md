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
nana start [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [--once|--cycles <n>|--forever] [--interval <duration>] [-- codex-args...]
nana improve [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
nana enhance [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
```

Selection rules:

- local mode is selected when `start` does not receive a GitHub issue/PR URL
- local mode uses `--task`, `--plan-file`, or an inferred task from the current branch
- GitHub mode is selected when `start` receives a GitHub issue/PR URL
- `sync` and `lane-exec` are GitHub-only run controls
- top-level `nana start` runs supported scout roles for local scout startup, and for onboarded GitHub repos it also mirrors issues, starts eligible work, runs scouts, and refreshes issue pickup after scouts finish
- `improve` and `enhance` are proposal-only direct scout role commands

## Storage

`work` keeps authoritative runtime state in `~/.nana/work/state.db`.

This SQLite-backed control plane currently assumes the repo's Go 1.25 baseline.

Layout:

- `~/.nana/work/state.db`
- `~/.nana/work/repos/<repo-id-or-owner/repo>/runs/<run-id>/...`
- `~/.nana/work/sandboxes/<repo-id>/<run-id>/repo`

The source repo should not receive `work` runtime files. For local runs, the verified sandbox diff is committed back to the source checkout after all completion gates pass.

Previous JSON state files such as `manifest.json`, `runtime-state.json`, `finding-history.json`, `repo.json`, `latest-run.json`, and `index/runs.json` are not part of the current runtime state model and are ignored if they still exist on disk.

## Local Execution Loop

Local-backed runs keep the existing iterative implementation flow. If neither `--task` nor `--plan-file` is supplied, `nana work start` tries to infer the task from the current branch name and fails with a prompt to provide `--task` when the branch is generic, detached, or otherwise not useful.

Local-backed runs keep this loop:

1. implementation pass
2. shell verification
3. reviewer pass
4. hardening pass when verification or review fails
5. post-hardening verification
6. post-hardening review
7. expanded final quality/security/performance review gate
8. source-checkout apply and local commit

Validation behavior:

- AI grouping retries up to 3 times if the output is malformed or incomplete
- if AI grouping still fails, `work` falls back to singleton grouping for that validation phase
- validator groups retry up to 3 times
- if a validator group still fails after retries, the run fails and stays resumable
- `resume` will reuse completed grouping/validator work and rerun only incomplete or failed validator groups for current-format runs
- if final source commit is blocked because the source checkout is dirty or no longer at the run baseline, the run remains blocked and `resume` retries the final commit after the checkout is restored
- if source apply succeeds but commit creation fails, the source checkout is left with staged changes and the run stays blocked for manual recovery

Final commit recovery:

- `blocked-before-apply`: clean or restore the source checkout, then run `nana work resume --run-id <id>`.
- `blocked-after-apply`: inspect the staged source checkout changes and either commit or reset them manually; `resume` will not retry this state automatically.
- local work only creates local commits; it does not push to remotes.

Start-time validation controls:

- `--grouping-policy ai|path|singleton`
- `--validation-parallelism <1-8>`
- `nana work status --last --json`

## GitHub Runs

GitHub-backed runs use the same shared runtime home, plus GitHub-specific wrappers:

- managed repo state: `~/.nana/work/repos/<owner>/<repo-name>`
- `nana work status` and `nana work explain` surface publication state/detail/error for published-PR runs
- reviewer feedback refresh: `nana work sync`
- isolated lane execution: `nana work lane-exec`
- verification artifact refresh: `nana work verify-refresh`

Repo overrides still live in the source checkout:

- `.nana/work-on-concerns.json`
- `.github/nana-work-on-concerns.json`
- `.nana/work-on-hot-path-apis.json`
- `.github/nana-work-on-hot-path-apis.json`

## Improvement Runs

`nana improve` inspects the selected repo for UX and performance improvement proposals. `nana enhance` uses the same flow for grounded enhancements that help the repo move forward. Top-level `nana start` runs scout startup automation when scout-specific flags are provided or local scout policies are present. Bare `nana start` loops indefinitely until interrupted; use `--once` or `--cycles <n>` for bounded runs.

Local repo behavior:

- proposals are saved under `.nana/improvements/<run-id>/` or `.nana/enhancements/<run-id>/`
- auto-mode local start picks up one pending discovered item for local implementation per cycle
- no GitHub APIs are called

GitHub target behavior:

- the repo is inspected from NANA's managed source checkout
- scout startup runs `improvement-scout` only when an improvement policy exists and `enhancement-scout` only when an enhancement policy exists
- `.github/nana-improvement-policy.json` and `.nana/improvement-policy.json` are read for `improvement-scout`
- `.github/nana-enhancement-policy.json` and `.nana/enhancement-policy.json` are read for `enhancement-scout`
- `.nana/...` takes precedence over `.github/...`
- `issue_destination` controls publication: `local`, `repo`/`target`, or `fork`
- scout issue labels include the role label
- scout policy defaults to 5 proposals per run and allows `max_issues` up to 50
- local `mode: "auto"` in every supported scout policy makes `nana start` switch to the repo's default branch, commit generated scout artifacts there, and run `nana work start --task ...` for one pending local discovered item per cycle; this requires a clean worktree and a resolvable local default branch
- `nana repo scout enable` creates or updates these policy files; by default it writes `.nana` policies for both scouts with local auto mode

Policy examples:

```json
{"version":1,"mode":"auto","issue_destination":"repo","labels":["improvement","ux","perf"]}
```

```json
{"version":1,"issue_destination":"fork","fork_repo":"my-user/widget","labels":["improvement"]}
```

## Start Automation

`nana start` is also the global automation command for onboarded GitHub repos when run without scout-specific flags or positional scout targets. Configure each repo first:

```bash
nana repo defaults set --repo-mode fork --issue-pick label --pr-forward approve
nana repo onboard owner/repo
nana repo config owner/repo --repo-mode repo --issue-pick auto --pr-forward auto
nana repo explain owner/repo
```

`repo-mode` controls how Nana works with the repository: `local` keeps changes on a local branch and is the default, `fork` pushes implementation work to your fork, and `repo` pushes implementation work to the target repo. For `fork` and `repo`, `issue-pick` controls automatic issue selection with `manual`, `label`, or `auto`; label mode picks issues with the single opt-in label `nana`, and also picks Nana-generated scout proposal issues labeled `improvement-scout` or `enhancement-scout`. `pr-forward` controls what happens after a PR exists: `approve` waits for approval, while `auto` goes forward automatically. In `fork` mode, going forward creates the matching PR on the target repo. In `repo` mode, going forward means merging the PR.

A `nana start` automation run scans `~/.nana/work/repos`, skips repos where `repo-mode` is `local` or `issue-pick` is `manual`, mirrors eligible issues, triages them locally before implementation pickup, and schedules work through separate per-repo service and implementation queues. `--parallel` now limits active repos, `--per-repo-workers` limits workers inside each repo, and the ten-open-PR cap remains per repo for PR-producing implementation work. Nana only auto-triages `P1` through `P5`; manual `P0` labels always sort first and stay user-controlled. Service tasks such as scout runs, issue-sync, triage, and implementation reconciliation are persisted in start state, carry explicit dependencies, retry conservatively on transient failures, and previously running service tasks are requeued on restart. Scout runs are scheduled through the repo service queue, feed an issue-sync pass, and scout-created proposal issues can be mirrored, triaged, and become eligible for implementation in the same cycle. Bare `nana start` repeats forever with a one-minute target cadence between cycle starts; use `--once` for one pass, `--cycles <n>` for a bounded run, or `--interval <duration>` to change that target cadence. Reconcile refreshes published-PR CI state live, treats repos with no CI as green, defers true pending publication until the next outer cycle, and surfaces GitHub CI API failures as explicit blocked publication errors. State is persisted under `~/.nana/start/<owner>/<repo>/state.json`.

The loopback start UI is enabled by default. Its overview and event-stream payloads reuse a cached snapshot while the watched start-state files, repo settings, work SQLite database, HUD files read by the overview, auth state, and current Git ref are unchanged, so idle browser clients do not force full overview rebuilds on every SSE tick.


## Troubleshooting

Common failures:

- `work requires a clean repo before start`
- `work repo context is required for --last`
- `validator group <group-id> failed after 3 attempt(s)`
- `source checkout has local changes`
- `source checkout HEAD changed since work started`
- `source checkout contains staged final-apply changes, but commit failed`
