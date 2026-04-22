# Work

`nana work` is the unified implementation runtime for both local repo work and GitHub-backed issue/PR work. Every launch now carries a work type: `bug_fix`, `refactor`, `feature`, or `test_only`.

## Commands

```bash
nana work start [<github-issue-or-pr-url>] [--repo <path>] [--task <text> | --plan-file <path>] [--work-type <bug_fix|refactor|feature|test_only>] [--max-iterations <n>] [--integration <final|always|never>] [--grouping-policy <ai|path|singleton>] [--validation-parallelism <1-8>] [--considerations <list>] [--role-layout <split|reviewer+executor>] [--new-pr] [--create-pr | --local-only] [--reviewer <login|@me>] [-- codex-args...]
nana work resume [--run-id <id> | --last | --global-last] [--repo <path>] [-- codex-args...]
nana work resolve [--run-id <id> | --last | --global-last] [--repo <path>]
nana work status [--run-id <id> | --last | --global-last] [--repo <path>] [--json]
nana work logs [--run-id <id> | --last | --global-last] [--repo <path>] [--tail <n>] [--json]
nana work retrospective [--run-id <id> | --last | --global-last] [--repo <path>]
nana work db-check [--json]
nana work db-repair [--json]
nana work verify-refresh [--run-id <id> | --last | --global-last] [--repo <path>]
nana work sync [--run-id <id> | --last] [--reviewer <login|@me>] [--resume-last] [-- codex-args...]
nana work lane-exec --run-id <id>|--last --lane <alias> [--task <text>] [-- codex-args...]
nana start [--repo <owner/repo>] [--parallel <n>] [--per-repo-workers <n>] [--max-open-prs <n>] [--once|--cycles <n>|--forever] [--interval <duration>] [--no-ui] [--ui-api-port <port>] [--ui-web-port <port>] [-- codex-args...]
nana start [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [--once|--cycles <n>|--forever] [--interval <duration>] [-- codex-args...]
nana improve [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
nana enhance [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
nana ui-scout [owner/repo|github-url] [--repo <path>] [--focus <ui,ux,a11y,perf>] [--from-file <findings.json>] [--dry-run] [--local-only] [--session-limit <1-6>] [-- codex-args...]
```

Selection rules:

- local mode is selected when `start` does not receive a GitHub issue/PR URL
- local mode uses `--task`, `--plan-file`, or an inferred task from the current branch, and always requires `--work-type`
- GitHub mode is selected when `start` receives a GitHub issue/PR URL; `--work-type` is optional there only when issue labels or metadata resolve it
- `sync` and `lane-exec` are GitHub-only run controls
- top-level `nana start` has two modes and prints a one-line `[start] Mode: ...` banner before work begins
- `nana start` automation mode mirrors issues, starts eligible work, runs scouts, and refreshes issue pickup for onboarded GitHub repos
- `nana start` scout mode runs supported scout roles for policy-backed local repos or explicit scout targets
- `improve` and `enhance` are proposal-only direct scout role commands

## Storage

`work` keeps authoritative runtime state in `~/.nana/work/state.db`.

This SQLite-backed control plane currently assumes the repo's Go 1.25 baseline.

Usage data uses the same control plane. `nana usage` and the Start UI Usage view read SQLite-backed session/checkpoint state from `state.db`; legacy usage JSON files and historical `thread-usage*.json` files are only compatibility import inputs and are not part of the live reporting model.

Layout:

- `~/.nana/work/state.db`
- `~/.nana/work/repos/<repo-id-or-owner/repo>/runs/<run-id>/...`
- `~/.nana/work/sandboxes/<repo-id>/<run-id>/repo`

The source repo should not receive `work` runtime files. For local runs, the verified sandbox diff is committed back to the source checkout after all completion gates pass.

Previous JSON state files such as `manifest.json`, `runtime-state.json`, `finding-history.json`, `repo.json`, `latest-run.json`, and `index/runs.json` are not part of the current runtime state model and are ignored if they still exist on disk.

State DB maintenance:

- `nana work db-check [--json]` reports schema version, integrity state, foreign-key state, and whether repair is required
- `nana work db-repair [--json]` migrates legacy `state.db` files, rebuilds constrained tables, repairs dangling references, and revalidates the database

## Rate-Limit Handling

Managed task runtimes and direct interactive `nana` / Codex launch sessions distinguish provider rate limits from ordinary execution failures.

- if the active managed account hits a rate limit and another enabled managed account is still eligible, Nana switches accounts and retries the step
- if every managed account is exhausted, standalone commands stay attached, wait until the earliest known reset time, and then continue automatically
- queue-managed work such as `nana start`, scout jobs, planned launches, and work items is persisted as paused instead of failed and becomes runnable again when the stored retry time passes
- `nana work status`, `nana account status`, Start UI run/work-item/investigation/scout surfaces, and work-item detail output expose the pause reason and retry timestamp when available

Paused work stays resumable and visible in the normal run/work queues. Human-gate approvals remain reserved for items that actually need a person to review, approve, or launch. Regular implementation, verification, parse, and validation failures still use the normal failed/blocked paths.

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
8. followup planner -> separate followup reviewer -> leader followup implementation loop while approved followups remain
9. source-checkout apply and local commit

Followup rules:

- followups may only be `functional_change` or `test_coverage`
- the followup reviewer is the stop/go signal for the loop
- unrelated cleanup, docs-only work, style-only churn, infra work, and scope expansion are rejected
- `test_only` work may only produce `test_coverage` followups

Repo lock behavior:

- source and sandbox checkouts use heartbeat-backed repo locks
- multiple readers may share the same checkout
- one writer excludes all readers and other writers on that checkout
- stale lock holders are recovered automatically after the heartbeat timeout
- separate sandboxes can still run concurrently because locks are checkout-path based, not issue-identity based

Validation behavior:

- AI grouping retries up to 3 times if the output is malformed or incomplete
- if AI grouping still fails, `work` falls back to singleton grouping for that validation phase
- validator groups retry up to 3 times
- if a validator group still fails after retries, the run fails and stays resumable
- `resume` will reuse completed grouping/validator work and rerun only incomplete or failed validator groups for current-format runs
- when a Codex-backed step fails after the session transcript exists, `resume` reuses that Codex session for the failed step instead of restarting it cold
- if final source commit is blocked because the source checkout is dirty or no longer at the run baseline, the run remains blocked until `resolve` refreshes the source checkout and retries final apply
- if source apply succeeds but commit creation or push fails, the source checkout remains blocked until `resolve` retries the pending commit/push path

Final commit recovery:

- `blocked-before-apply`: run `nana work resolve --run-id <id>` to refresh the source checkout and retry final apply.
- `blocked-after-apply`: run `nana work resolve --run-id <id>` to retry the pending commit/push of the final-applied source changes.
- local work commits verified source changes and pushes to the tracked remote when one exists.

Start-time validation controls:

- `--grouping-policy ai|path|singleton`
- `--validation-parallelism <1-8>`
- `nana work status --last --json`

## GitHub Runs

GitHub-backed runs use the same shared runtime home, plus GitHub-specific wrappers:

- managed repo state: `~/.nana/work/repos/<owner>/<repo-name>`
- leader `resume` / `sync` reruns reuse the stored leader Codex session when one is available
- `lane-exec` reruns reuse the stored session for that lane alias when one is available
- `nana work status` and `nana work explain` surface publication state/detail/error for published-PR runs
- `nana work status --json` includes `lock_state` for the managed source checkout and sandbox checkout when available
- reviewer feedback refresh: `nana work sync`
- isolated lane execution: `nana work lane-exec`
- verification artifact refresh: `nana work verify-refresh`

Repo overrides still live in the source checkout:

- `.nana/work-on-concerns.json`
- `.github/nana-work-on-concerns.json`
- `.nana/work-on-hot-path-apis.json`
- `.github/nana-work-on-hot-path-apis.json`

## Improvement Runs

`nana improve` inspects the selected repo for UX and performance improvement proposals. `nana enhance` uses the same flow for grounded enhancements that help the repo move forward. `nana ui-scout` is the direct operator-facing command for page-by-page UI audit findings, and the same role also runs through managed scout policy plus `nana start`. Top-level `nana start` enters scout mode when scout-specific flags are provided, a positional scout target is provided, a path-like `--repo` value is provided, or a bare local repo declares scout policies. `nana start` prints `[start] Mode: scout (policy-backed scout startup).` before scout execution begins. Bare scout-mode `nana start` loops indefinitely until interrupted; use `--once` or `--cycles <n>` for bounded runs.

Local repo behavior:

- scout artifacts are saved under `.nana/improvements/<run-id>/`, `.nana/enhancements/<run-id>/`, or `.nana/ui-findings/<run-id>/`
- direct scout runs support `--resume <run-id>` and `--last` to continue an interrupted scout in the same artifact directory and scoped Codex session
- direct scout policy reads, repo-local scout artifact writes, and auto-mode source mutations participate in the same repo lock model as other work surfaces
- auto-mode local start picks up one pending discovered item for local implementation per cycle
- no GitHub APIs are called

GitHub target behavior:

- the repo is inspected from NANA's managed source checkout
- GitHub onboarding writes repo settings first; the managed source checkout and derived repo metadata are hydrated lazily by source-prep flows that need them
- scout startup runs `improvement-scout`, `enhancement-scout`, and `ui-scout` only when their matching policies exist
- managed scout policies live under `~/.nana/work/repos/<owner>/<repo>/scouts/<role>-policy.json`
- repo-native `.github/nana-*-policy.json` and `.nana/*-policy.json` are legacy fallback read paths
- `issue_destination` controls publication: `local`, `repo`/`target`, or `fork`
- `schedule` controls reruns: `always`, `daily`, `weekly`, or `when_resolved`
- when `schedule` is omitted, scouts default to `when_resolved` and rerun only after their previously reported local or GitHub issues are completed or dropped
- scout issue labels include the role label
- scouts return and publish every grounded proposal or finding they produce
- `ui-scout` also accepts `session_limit` to cap parallel page-audit sessions
- direct `nana ui-scout` runs perform a short preflight first and persist `preflight.json` beside the findings artifact
- local `mode: "auto"` in every supported scout policy makes `nana start` switch to the repo's default branch, commit generated scout artifacts there, and run `nana work start --task ... --work-type ...` for one pending local discovered item per cycle; this requires a clean worktree and a resolvable local default branch
- `nana repo scout enable` creates or updates the managed scout policies; Start UI scout-settings save also hydrates a missing checkout before writing the managed policy file

Policy examples:

```json
{"version":1,"mode":"auto","issue_destination":"repo","labels":["improvement","ux","perf"]}
```

```json
{"version":1,"mode":"auto","schedule":"weekly","issue_destination":"repo","labels":["ui"],"session_limit":4}
```

```json
{"version":1,"issue_destination":"fork","fork_repo":"my-user/widget","labels":["improvement"]}
```

## Start Automation

`nana start` automation mode is the global automation command for onboarded GitHub repos when run without scout-specific flags or positional scout targets. It prints `[start] Mode: automation (onboarded repo automation).` before automation execution begins. By default it also launches a loopback REST API and assistant workspace on `127.0.0.1` (default API port `17653`, default Web port `17654`) and prints the resolved `[start-ui]` URLs. Use `--no-ui` for headless runs, or `--ui-api-port` / `--ui-web-port` to request specific local ports. See the [Start UI guide](./start-ui.html) for the end-to-end flow. Configure each repo first:

```bash
nana repo defaults set --repo-mode fork --issue-pick label --pr-forward approve
nana repo onboard owner/repo
nana repo config owner/repo --repo-mode repo --issue-pick auto --pr-forward auto
nana repo explain owner/repo
```

`repo-mode` controls how Nana works with the repository: `disabled` keeps it onboarded for observation only and blocks work launch, `local` keeps changes on a local branch and is the default, `fork` pushes implementation work to your fork, and `repo` pushes implementation work to the target repo. For repos with development enabled, `issue-pick` controls automatic issue selection with `manual`, `label`, or `auto`; label mode picks issues with the single opt-in label `nana`, and also picks Nana-generated scout proposal issues labeled `improvement-scout`, `enhancement-scout`, or `ui-scout`. `pr-forward` controls what happens after a PR exists: `approve` waits for approval, while `auto` goes forward automatically. In `fork` mode, going forward creates the matching PR on the target repo. In `repo` mode, going forward means merging the PR. `nana repo explain` reports the mapped source path, whether the managed checkout is ready yet, and any actual managed scout policy files that exist.

A `nana start` automation run scans `~/.nana/work/repos`, skips repos where `repo-mode` is `disabled` or `local`, or where `issue-pick` is `manual`, mirrors eligible issues, triages them locally before implementation pickup, and schedules work through one shared worker queue across all selected repos. `--parallel` now limits total workers across that shared queue, `--per-repo-workers` is accepted as a deprecated alias for `--parallel`, and the ten-open-PR cap remains per repo for PR-producing implementation work. Nana only auto-triages `P1` through `P5`; manual `P0` labels always sort first and stay user-controlled. Service tasks such as scout runs, issue-sync, triage, planned launches, and implementation reconciliation are persisted in start state, carry explicit dependencies, retry conservatively on transient failures, and previously running service tasks are requeued on restart. Scout runs are scheduled through the repo service queue, feed an issue-sync pass, and scout-created proposal issues can be mirrored, triaged, and become eligible for implementation in the same cycle. Managed-source scout policy reads participate in the same checkout read/write lock model as other work surfaces, so active writers can temporarily block automation-side reads. `nana start` now also owns a per-user Nana service control socket used by shared control-plane CLI commands such as `status`, `next`, `usage`, `artifacts`, `repo`, `cleanup`, `work ...`, `review`, `review-rules`, and GitHub `issue`/`implement`/`sync` flows. Interactive/local-only tools such as `nana hud`, top-level `nana investigate`, and scout commands remain direct local CLI commands. If the service socket is not running, routed control-plane commands fail with an instruction to run `nana start`. The DB proxy socket remains a start-owned implementation detail for centralized work-state access. Bare `nana start` repeats forever with a one-minute target cadence between cycle starts; use `--once` for one pass, `--cycles <n>` for a bounded run, or `--interval <duration>` to change that target cadence. Reconcile refreshes published-PR CI state live, treats repos with no CI as green, defers true pending publication until the next outer cycle, and surfaces GitHub CI API failures as explicit blocked publication errors. State is persisted under `~/.nana/work/repos/<owner>/<repo>/start-state.json`. The default assistant workspace caches its overview snapshot and refreshes it only after API mutations or detected start-state/work-database/HUD dependency changes, avoiding a full filesystem and SQLite rebuild on every live-events tick.


## Troubleshooting

Common failures:

- `work requires a clean repo before start`
- `work repo context is required for --last`
- `validator group <group-id> failed after 3 attempt(s)`
- `source checkout has local changes`
- `source checkout HEAD changed since work started`
- `source checkout contains staged final-apply changes, but commit failed`
- `repo read lock busy`
- `repo write lock busy`
