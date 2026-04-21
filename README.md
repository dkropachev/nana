# nana

<p align="center">
  <img src="./docs/shared/nana-character-spark-initiative.jpg" alt="nana character" width="280">
  <br>
  <em>A personal AI assistant for developers that automates daily engineering work with minimal supervision.</em>
</p>

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8)](https://go.dev)
[![Discord](https://img.shields.io/discord/1452487457085063218?color=5865F2&logo=discord&logoColor=white&label=Discord)](https://discord.gg/PUwSMR9XNk)

**Website:** https://dkropachev.github.io/nana/  
**Docs:** [Getting Started](./docs/getting-started.html) · [CLI Quickstart](./docs/quickstart.md) · [Start UI](./docs/start-ui.html) · [Agents](./docs/agents.html) · [Skills](./docs/skills.html) · [Integrations](./docs/integrations.html) · [Work](./docs/work.md) · [Demo](./DEMO.md) · [OpenClaw guide](./docs/openclaw-integration.md)

`nana` is a personal AI assistant for developers, built on top of [OpenAI Codex CLI](https://github.com/openai/codex).

Its goal is to automate recurring development work with little to no oversight, while still allowing human gates at the points that matter most. Over time, it learns the preferences of the owner or repository owner so the code, reviews, and decisions it produces align more closely with how that project is run.

It is designed to help with daily engineering work such as:
- issue investigation
- feature implementation
- pull request review
- bug fixing
- triaging
- replying to review feedback
- repository maintenance and ongoing development

## Core Maintainers

| Role | Name | GitHub |
| --- | --- | --- |
| Creator & Lead | Yeachan Heo | [@Yeachan-Heo](https://github.com/Yeachan-Heo) |
| Maintainer | HaD0Yun | [@HaD0Yun](https://github.com/HaD0Yun) |

## Ambassadors

| Name | GitHub |
| --- | --- |
| Sigrid Jin | [@sigridjineth](https://github.com/sigridjineth) |

## Top Collaborators

| Name | GitHub |
| --- | --- |
| HaD0Yun | [@HaD0Yun](https://github.com/HaD0Yun) |
| Junho Yeo | [@junhoyeo](https://github.com/junhoyeo) |
| JiHongKim98 | [@JiHongKim98](https://github.com/JiHongKim98) |
| Lor | — |
| HyunjunJeon | [@HyunjunJeon](https://github.com/HyunjunJeon) |

## Recommended default flow

If you want the default NANA experience, start here:

<!-- NANA:INSTALL:START -->
```bash
set -euo pipefail

NANA_VERSION=0.11.12
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) nana_target="x86_64-unknown-linux-musl" ;;
  Linux-aarch64|Linux-arm64) nana_target="aarch64-unknown-linux-musl" ;;
  Darwin-x86_64) nana_target="x86_64-apple-darwin" ;;
  Darwin-arm64) nana_target="aarch64-apple-darwin" ;;
  *) echo "Unsupported platform: $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

nana_archive="nana-${nana_target}.tar.gz"
nana_base="https://github.com/dkropachev/nana/releases/download/v${NANA_VERSION}"
curl -fsSL -o native-release-manifest.json "${nana_base}/native-release-manifest.json"
if ! grep -q "\"archive\": \"${nana_archive}\"" native-release-manifest.json; then
  echo "Release manifest does not list ${nana_archive}" >&2
  exit 1
fi
expected_sha256="$(
  awk -v archive="${nana_archive}" '
    index($0, "\"archive\": \"" archive "\"") { found=1 }
    found && index($0, "\"sha256\":") {
      sha=$0
      sub(/^.*"sha256": "/, "", sha)
      sub(/".*$/, "", sha)
      print sha
      exit
    }
  ' native-release-manifest.json
)"
if [ -z "$expected_sha256" ]; then
  echo "Release manifest is missing sha256 for ${nana_archive}" >&2
  exit 1
fi
curl -fsSL -o "${nana_archive}" "${nana_base}/${nana_archive}"
if command -v sha256sum >/dev/null 2>&1; then
  actual_sha256="$(sha256sum "${nana_archive}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual_sha256="$(shasum -a 256 "${nana_archive}" | awk '{print $1}')"
else
  echo "No SHA-256 checksum tool found (expected sha256sum or shasum)" >&2
  exit 1
fi
if [ "$actual_sha256" != "$expected_sha256" ]; then
  echo "Checksum mismatch for ${nana_archive}" >&2
  exit 1
fi
tar -xzf "${nana_archive}" nana
chmod +x nana
sudo mv nana /usr/local/bin/nana
```
<!-- NANA:INSTALL:END -->

Then install Codex CLI and set up NANA:

```bash
npm install -g @openai/codex
nana setup
nana help workflows
```

Then start the first session separately:

```bash
nana --madmax --high
```

Then work normally inside Codex:

```text
nana next
$deep-interview "clarify the authentication change"
$ralplan "approve the auth plan and review tradeoffs"
nana investigate "why is CI failing?"
nana review https://github.com/acme/widget/pull/77
nana work start https://github.com/acme/widget/issues/42
```

That is the main path.
Start NANA strongly, clarify first when needed, approve the plan, then move into direct implementation, GitHub review, or `work`.

## What NANA is for

Use NANA if you already like Codex and want a better day-to-day runtime around it:
- a standard workflow built around `$deep-interview` and `$ralplan`
- specialist roles and supporting skills when the task needs them
- project guidance through scoped `AGENTS.md`
- durable state under `.nana/` for plans, logs, memory, and mode tracking

If you want plain Codex with no extra workflow layer, you probably do not need NANA.

## Quick start

### Requirements

- Codex CLI installed: `npm install -g @openai/codex`
- Codex auth configured
- NANA installed from the native release binary

### A good first session

Launch NANA the recommended way:

```bash
nana --madmax --high
```

To have NANA start Codex by issuing the Codex `/fast` slash command first, use:

```bash
nana --fast
```

NANA's generated Codex config defaults to extra-high reasoning. Control the user-level default used by future `nana setup` runs with:

```bash
nana config set --effort xhigh
```

Use a one-off Codex effort for a launch with `--effort`, for example `nana --effort xhigh` or `nana exec --effort high "..."`.

Then try the canonical workflow:

```text
$deep-interview "clarify the authentication change"
$ralplan "approve the safest implementation path"
nana review https://github.com/acme/widget/pull/77
nana work start https://github.com/acme/widget/issues/42
nana work start --task "execute the approved local refactor plan" --work-type refactor
```

Local work always requires `--work-type`. GitHub issue/PR work can infer the
type from labels when available, or accept an explicit `--work-type`.

## A simple mental model

NANA does **not** replace Codex.

It adds a better working layer around it:
- **Codex** does the actual agent work
- **NANA role keywords** make useful roles reusable
- **NANA skills** make common workflows reusable
- **`.nana/`** stores plans, logs, memory, and runtime state; see [`docs/reference/nana-state-files.md`](docs/reference/nana-state-files.md) for schemas and examples

Most users should think of NANA as **better task routing + better workflow + better runtime**, not as a command surface to operate manually all day.

## Start here if you are new

1. Run `nana setup`
2. Run `nana help workflows` or read the [CLI Quickstart](./docs/quickstart.md) for a compact index of modes, trigger phrases, skills, and safe support commands
3. Launch with `nana --madmax --high` for thorough work or `nana --fast` to start by issuing Codex `/fast`
4. Use `$deep-interview "..."` when the request or boundaries are still unclear
5. Use `$ralplan "..."` to approve the plan and review tradeoffs
6. Continue with direct implementation, `nana review`, or `nana work`

## Recommended workflow

1. `$deep-interview` — clarify scope when the request or boundaries are still vague.
2. `$ralplan` — turn that clarified scope into an approved architecture and implementation plan.
3. Continue with direct implementation, `nana work`, or the GitHub-oriented surfaces like `nana review`.

## Common in-session surfaces

| Surface | Use it for |
| --- | --- |
| `nana help workflows` | discovering modes, trigger phrases, safe entry commands, and common utility skills |
| `nana next` | showing the top item that needs attention and the next command to run |
| `$deep-interview "..."` | clarifying intent, boundaries, and non-goals |
| `$ralplan "..."` | approving the implementation plan and tradeoffs |
| `nana investigate "..."` | source-backed investigation with proof-linked JSON reports and validator enforcement |
| `nana investigate onboard` | bootstrap the dedicated investigate Codex config |
| `nana investigate doctor` | ask Codex to probe the MCPs configured in the investigate config |
| `nana start` | run onboarded repo automation, including issue pickup, scouts, a post-scout pickup pass, and the default loopback assistant workspace; with scout flags or policy-backed local repos, run supported scout startup automation |
| `nana improve [owner/repo]` | run the improvement-scout role for UX/perf proposals and route them by repo policy |
| `nana enhance [owner/repo]` | run the enhancement-scout role for forward-looking repo proposals with the same policy routing |
| `nana work start --task "..." --work-type <bug_fix|refactor|feature|test_only>` | long-running local plan execution in a managed sandbox, ending with verified changes committed to the local branch |
| `nana work start --task "..." --work-type refactor --grouping-policy path` | force deterministic path-based validation grouping |
| `nana work logs --last` | inspect the current iteration logs and verification artifacts in one view |
| `nana work status --last --json` | inspect machine-readable run and validation state from SQLite, including publication state/detail for GitHub-backed runs |
| `nana review <pr-url>` | reviewing external pull requests with persisted findings |
| `nana work start <issue-or-pr-url>` | GitHub-targeted implementation and review-sync workflows |
| `nana repo explain <owner/repo>` | show exactly how `nana start` will treat an onboarded repo |
| `nana work explain --last [--json]` | inspect the effective GitHub work policy, repo profile, publication state/detail, and human-gate state for a run |
| `nana repo onboard --json` | inspect the detected verification split and derived repo profile for a local checkout |
| `nana verify --json` | run this repo's canonical lint, typecheck, test, and static-analysis profile with machine-readable evidence |
| `/skills` | browsing installed skills and supporting helpers |

`nana work` stores its authoritative runtime state in `~/.nana/work/state.db`, not inside the source repo. Local runs work in a managed sandbox, run expanded completion reviews, and then create a local commit with the verified diff on the source branch; they do not push to remotes. Run artifacts live under `~/.nana/work/repos/<repo-id-or-owner/repo>/runs/<run-id>/...`, and old JSON state files such as `manifest.json`, `runtime-state.json`, `finding-history.json`, `repo.json`, `latest-run.json`, and `index/runs.json` are ignored if they still exist on disk. When a Codex-backed step fails after its session has started, `nana work resume` reuses that stored Codex session instead of restarting the step cold. Managed task runtimes and direct interactive Nana/Codex launch sessions also distinguish rate limits from ordinary failures: Nana switches to another eligible managed account when possible, otherwise it pauses queued work or waits in-process until the next known reset time. Paused work stays visible in the normal run/work surfaces, while approvals remain reserved for human-actionable review, publication, or launch decisions. Use `nana work db-check` to inspect the SQLite control plane and `nana work db-repair` to migrate or repair legacy `state.db` files. This SQLite-backed state layer currently assumes the repo’s Go 1.25 baseline. See [docs/work.md](./docs/work.md) for storage, resume, validation grouping controls, rate-limit handling, and GitHub-backed run details.

## Investigate

Use `nana investigate "<question>"` when you need a source-backed answer rather than a code change or a PR review.

The runtime:
- blocks until required investigation sources are ready
- runs an investigator agent first, then a validator agent
- persists run artifacts under `.nana/logs/investigate/<run-id>/`
- supports `nana investigate --resume <run-id>` or `nana investigate --last` for interrupted runs
- accepts only JSON reports with proof links

Status values:
- `REFUTED`
- `CONFIRMED`
- `PARTIALLY_CONFIRMED`

Evidence precedence:
- source code, logs, and source-system outputs are primary
- documentation is supplementary only and is rejected as a primary proof when source evidence is available

Setup flow:

```bash
nana investigate onboard
# configure whichever MCPs you want in the dedicated investigate config
nana investigate doctor
nana investigate "why is CI failing?"
```

GitHub issue preflight still exists separately:

```bash
nana issue investigate https://github.com/acme/widget/issues/42
```

## GitHub Work Overrides

If you use `nana work`, you can tune repo-specific lane invalidation without changing NANA code.

Concern overrides:
- `.nana/work-on-concerns.json`
- `.github/nana-work-on-concerns.json`

Example:

```json
{
  "version": 1,
  "lanes": {
    "security-reviewer": {
      "pathPrefixes": ["policies/"]
    }
  }
}
```

Hot-path API overrides:
- `.nana/work-on-hot-path-apis.json`
- `.github/nana-work-on-hot-path-apis.json`

Policy overrides:
- `.nana/work-on-policy.json`
- `.github/nana-work-on-policy.json`

Example:

```json
{
  "version": 1,
  "hot_path_api_files": ["docs/openapi/search.yaml"],
  "api_identifier_tokens": ["searchDocuments"]
}
```

What they do:
- concern overrides refine which files invalidate a hardening lane
- hot-path API overrides refine when `perf` lanes rerun
- policy overrides shape experimental repo-native work-on behavior, GitHub human-gate behavior, and allowed publication actions
- experimental merge automation only runs when policy enables `allowed_actions.merge`, local verification has passed, and GitHub CI is green; `pr-forward=approve` also requires a control-plane reviewer approval without a later changes-requested review
- `any_human` feedback mode accepts non-bot GitHub actors except the target author and blocked reviewers

Minimal policy examples:

```json
{
  "version": 1,
  "experimental": true,
  "allowed_actions": {"commit": true, "push": true, "open_draft_pr": true, "request_review": true, "merge": false},
  "feedback_source": "assigned_trusted",
  "human_gate": "publish_time"
}
```

```json
{
  "version": 1,
  "experimental": true,
  "allowed_actions": {"commit": true, "push": true, "open_draft_pr": true, "request_review": true, "merge": true},
  "feedback_source": "assigned_trusted",
  "human_gate": "publish_time",
  "merge_method": "squash"
}
```
- `.nana/...` takes precedence over `.github/...`
- malformed override files are ignored for execution but show up as diagnostics in `work` runtime artifacts and retrospectives

Repo profile:
- managed GitHub repos persist a derived repo profile at `~/.nana/work/repos/<owner>/<repo>/repo-profile.json`
- the profile records detected verification shape, commit-style heuristics, PR template presence, workflow files, CODEOWNERS presence, review-rule summary, and warnings for ambiguous signals
- low-confidence repo-native signals fall back to generic commit/PR behavior rather than blocking execution
- `nana work explain --last` shows the effective policy, repo profile, GitHub control-plane reviewers, review-request state, merge state, and next action for the active run

## Improvement Proposals

`nana improve` runs the `improvement-scout` role to inspect a repo and produce evidence-backed UX/performance improvement proposals. `nana enhance` runs the `enhancement-scout` role for grounded repo-forward enhancements. `nana start` also runs scout startup automation when scout-specific flags are provided or local scout policies are present. Bare `nana start` loops indefinitely until interrupted; use `--once` or `--cycles <n>` for bounded runs. Local repo runs keep drafts under `.nana/improvements/<run-id>/` or `.nana/enhancements/<run-id>/`, and auto-mode local start picks up one pending discovered item for local implementation per cycle.

For GitHub targets, repo policy controls whether proposals stay local or become issues:

```json
{
  "version": 1,
  "mode": "auto",
  "issue_destination": "repo",
  "labels": ["improvement", "ux", "perf"]
}
```

```json
{
  "version": 1,
  "issue_destination": "fork",
  "fork_repo": "my-user/widget",
  "labels": ["improvement"]
}
```

Policy files:
- `.nana/improvement-policy.json`
- `.github/nana-improvement-policy.json`
- `.nana/enhancement-policy.json`
- `.github/nana-enhancement-policy.json`

Create or update local scout startup policies with:

```bash
nana repo scout enable --role both --mode auto --issue-destination local
```

Use `--github` to write shareable `.github/nana-*-policy.json` files instead of `.nana/*-policy.json`.

`.nana/...` takes precedence. Improvement labels are normalized to include `improvement` and `improvement-scout` while excluding `enhancement`. Enhancement labels include `enhancement` and `enhancement-scout`. Scouts return and publish every grounded proposal or finding they produce.

For GitHub targets, `issue_destination: "repo"` publishes to the target repo and `issue_destination: "fork"` publishes to `fork_repo`. For local repos, `nana start` treats `mode: "auto"` in every supported scout policy as permission to switch to the repo's default branch, commit generated scout artifacts there, and run `nana work start --task ... --work-type ...` for one pending local discovered item per cycle. Auto mode requires a clean worktree and a resolvable local default branch.

## Start Automation

Use `nana repo defaults set --repo-mode disabled|local|fork|repo --issue-pick manual|label|auto --pr-forward approve|auto` to set defaults for future manual GitHub repo onboarding. Use `nana repo onboard <owner/repo> ...` or `nana repo config <owner/repo> ...` to opt a specific GitHub repo into global automation. Then run one command:

```bash
nana start
```

Automatically onboarded repos use system defaults, meaning `repo-mode=local`, `issue-pick=manual`, and `pr-forward=approve` unless later configured. `repo-mode=disabled` keeps a repo onboarded for observation only and blocks any work launch until the mode changes. `nana start` scans all onboarded GitHub repos under `~/.nana/work/repos`, skips repos where `repo-mode` is `disabled` or `local`, or where `issue-pick` is `manual`, triages mirrored issues locally before implementation pickup, keeps separate per-repo service and implementation queues, and runs up to three repos at a time by default while each repo may use up to three workers. Manual `P0` labels always sort first; Nana only auto-triages `P1` through `P5`. The ten-open-PR cap still applies per repo to PR-producing implementation work. Service tasks such as scout runs, issue-sync, triage, and implementation reconciliation are persisted in start state, retried conservatively on transient failures, and requeued safely after restart so stale `in_progress` issues can be reconciled instead of occupying capacity forever. When the managed source checkout declares scout policies, scout runs enter the repo service queue, feed an issue-sync pass, and newly-created proposal issues are mirrored, triaged, and become eligible for implementation in the same cycle. Bare `nana start` repeats forever with a one-minute target cadence between cycle starts; use `--once` for one pass, `--cycles <n>` for a bounded run, or `--interval <duration>` to change that target cadence. Published PR reconcile now refreshes live CI state, treats repos with no CI as green, and surfaces GitHub CI API failures as explicit blocked publication errors instead of passive waiting.

Mode behavior:
- `repo-mode disabled`: keep the repo onboarded for observation only and never launch work
- `repo-mode local`: keep work on a local branch and do not open a PR
- `repo-mode fork`: push work to your fork
- `repo-mode repo`: push work to the target repo
- `issue-pick manual`: do not automatically pick issues
- `issue-pick label`: pick issues with the `nana` label, plus Nana-generated scout proposal issues labeled `improvement-scout` or `enhancement-scout`
- `issue-pick auto`: pick all eligible open issues
- `pr-forward approve`: wait for approval before going forward with the PR
- `pr-forward auto`: go forward automatically; fork creates the target-repo PR, repo merges the PR

Use `nana repo explain <owner/repo>` to see the effective settings, label gate, repo mode, state paths, and caps for a repo.


## PR Review Rules

NANA can mine repeated PR review guidance into repo-scoped rules and feed approved rules back into related `work` roles.

Commands:

```bash
nana review-rules scan <owner/repo|issue-url|pr-url>
nana review-rules list <owner/repo|issue-url|pr-url>
nana review-rules approve <owner/repo|issue-url|pr-url> <rule-id|all>
nana review-rules disable <owner/repo|issue-url|pr-url> <rule-id|all>
nana review-rules enable <owner/repo|issue-url|pr-url> <rule-id|all>
nana review-rules archive <owner/repo|issue-url|pr-url> <rule-id|all>
nana review-rules explain <owner/repo|issue-url|pr-url> <rule-id>
nana review-rules config set [--mode <manual|automatic>] [--trusted-reviewers <a,b>|none] [--blocked-reviewers <a,b>|none] [--min-distinct-reviewers <n>]
nana review-rules config show [owner/repo|issue-url|pr-url]
```

Storage:
- `.nana/repo-review-rules.json` inside the managed repo source checkout

Behavior:
- `scan` reads PR reviews and review comments, includes reviewed file/path context when available, and writes pending rule candidates
- global config controls the default extraction mode for all repos
- global and per-repo reviewer policy can trust specific reviewers, block specific reviewers, and require a minimum number of distinct reviewers
- per-repo mode is set with `nana work defaults set <owner/repo> --review-rules-mode <manual|automatic>`
- per-repo reviewer policy is set with `nana work defaults set <owner/repo> --review-rules-trusted-reviewers <a,b>|none --review-rules-blocked-reviewers <a,b>|none --review-rules-min-distinct-reviewers <n>`
- `manual` mode keeps extracted rules as pending candidates until you approve them
- `automatic` mode auto-approves extracted rules and refreshes them during `issue investigate`, `work start`, and `work sync`
- only repeated high-signal guidance becomes a pending candidate
- extracted rules persist an origin/reason summary plus code-context provenance (`pr_head_sha`, `current_checkout`, or `unknown`)
- `approve` promotes candidates into approved rules
- `disable`, `enable`, and `archive` manage rule lifecycle without deleting evidence
- `explain` shows the stored evidence and provenance for a rule
- approved rules are injected into relevant `work` start, feedback, and lane instructions
- pending candidates are not injected until approved

## Advanced / operator surfaces

These are useful, but they are not the main onboarding path.

### Setup, doctor, and HUD

These are operator/support surfaces:
- `nana setup` installs prompts, skills, config, and AGENTS scaffolding
- `nana doctor` verifies the install when something seems wrong
- `nana hud --watch` is a monitoring/status surface, not the primary user workflow

### Explore and sparkshell

- `nana explore --prompt "..."` is for read-only repository lookup
- `nana sparkshell <command>` is for shell-native inspection and bounded verification

Examples:

```bash
nana explore --prompt "find where GitHub review state is written"
nana sparkshell git status
nana sparkshell --tmux-pane %12 --tail-lines 400
```

## Known issues

### Intel Mac: high `syspolicyd` / `trustd` CPU during startup

On some Intel Macs, NANA startup — especially with `--madmax --high` — can spike `syspolicyd` / `trustd` CPU usage while macOS Gatekeeper validates many concurrent process launches.

If this happens, try:
- `xattr -dr com.apple.quarantine $(which nana)`
- adding your terminal app to the Developer Tools allowlist in macOS Security settings
- using lower concurrency (for example, avoid `--madmax --high`)

## Documentation

- [Getting Started](./docs/getting-started.html)
- [CLI command-discovery quickstart](./docs/quickstart.md)
- [Start UI assistant workspace guide](./docs/start-ui.html)
- [Demo guide](./DEMO.md)
- [Agent catalog](./docs/agents.html)
- [Skills reference](./docs/skills.html)
- [Integrations](./docs/integrations.html)
- [OpenClaw / notification gateway guide](./docs/openclaw-integration.md)
- [Contributing](./CONTRIBUTING.md)
- [Changelog](./CHANGELOG.md)

## Languages

- [English](./README.md)
- [한국어](./README.ko.md)
- [日本語](./README.ja.md)
- [简体中文](./README.zh.md)
- [繁體中文](./README.zh-TW.md)
- [Tiếng Việt](./README.vi.md)
- [Español](./README.es.md)
- [Português](./README.pt.md)
- [Русский](./README.ru.md)
- [Türkçe](./README.tr.md)
- [Deutsch](./README.de.md)
- [Français](./README.fr.md)
- [Italiano](./README.it.md)
- [Ελληνικά](./README.el.md)
- [Polski](./README.pl.md)

## Contributors

| Role | Name | GitHub |
| --- | --- | --- |
| Creator & Lead | Yeachan Heo | [@Yeachan-Heo](https://github.com/Yeachan-Heo) |
| Maintainer | HaD0Yun | [@HaD0Yun](https://github.com/HaD0Yun) |

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=dkropachev/nana&type=date&legend=top-left)](https://www.star-history.com/#dkropachev/nana&type=date&legend=top-left)

## License

MIT
