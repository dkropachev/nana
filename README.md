# nana

<p align="center">
  <img src="https://yeachan-heo.github.io/nana-website/nana-character-nobg.png" alt="nana character" width="280">
  <br>
  <em>Start Codex stronger, then let NANA add better prompts, workflows, and runtime help when the work grows.</em>
</p>

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/go-1.24%2B-00ADD8)](https://go.dev)
[![Discord](https://img.shields.io/discord/1452487457085063218?color=5865F2&logo=discord&logoColor=white&label=Discord)](https://discord.gg/PUwSMR9XNk)

**Website:** https://yeachan-heo.github.io/nana-website/  
**Docs:** [Getting Started](./docs/getting-started.html) · [Agents](./docs/agents.html) · [Skills](./docs/skills.html) · [Integrations](./docs/integrations.html) · [Demo](./DEMO.md) · [OpenClaw guide](./docs/openclaw-integration.md)

`nana` is a workflow layer for [OpenAI Codex CLI](https://github.com/openai/codex).

It keeps Codex as the execution engine and makes it easier to:
- start a stronger Codex session by default
- run one consistent workflow from clarification to completion
- invoke the canonical skills with `$deep-interview` and `$ralplan`
- keep project guidance, plans, logs, and state in `.nana/`

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

```bash
curl -L -o nana <release-binary-url>
chmod +x nana
sudo mv nana /usr/local/bin/nana
```bash
npm install -g @openai/codex
nana setup
nana --madmax --high
```

Then work normally inside Codex:

```text
$deep-interview "clarify the authentication change"
$ralplan "approve the auth plan and review tradeoffs"
nana review https://github.com/acme/widget/pull/77
nana work-on start https://github.com/acme/widget/issues/42
```

That is the main path.
Start NANA strongly, clarify first when needed, approve the plan, then move into direct implementation, GitHub review, or `work-on`.

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

Then try the canonical workflow:

```text
$deep-interview "clarify the authentication change"
$ralplan "approve the safest implementation path"
nana review https://github.com/acme/widget/pull/77
nana work-on start https://github.com/acme/widget/issues/42
```

## A simple mental model

NANA does **not** replace Codex.

It adds a better working layer around it:
- **Codex** does the actual agent work
- **NANA role keywords** make useful roles reusable
- **NANA skills** make common workflows reusable
- **`.nana/`** stores plans, logs, memory, and runtime state

Most users should think of NANA as **better task routing + better workflow + better runtime**, not as a command surface to operate manually all day.

## Start here if you are new

1. Run `nana setup`
2. Launch with `nana --madmax --high`
3. Use `$deep-interview "..."` when the request or boundaries are still unclear
4. Use `$ralplan "..."` to approve the plan and review tradeoffs
5. Continue with direct implementation, `nana review`, or `nana work-on`

## Recommended workflow

1. `$deep-interview` — clarify scope when the request or boundaries are still vague.
2. `$ralplan` — turn that clarified scope into an approved architecture and implementation plan.
3. Continue with direct implementation or the GitHub-oriented surfaces like `nana review` and `nana work-on`.

## Common in-session surfaces

| Surface | Use it for |
| --- | --- |
| `$deep-interview "..."` | clarifying intent, boundaries, and non-goals |
| `$ralplan "..."` | approving the implementation plan and tradeoffs |
| `nana review <pr-url>` | reviewing external pull requests with persisted findings |
| `nana work-on start <issue-or-pr-url>` | GitHub-targeted implementation and review-sync workflows |
| `/skills` | browsing installed skills and supporting helpers |

## GitHub Work-on Overrides

If you use `nana work-on`, you can tune repo-specific lane invalidation without changing NANA code.

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
- `.nana/...` takes precedence over `.github/...`
- malformed override files are ignored for execution but show up as diagnostics in `work-on` runtime artifacts and retrospectives

## PR Review Rules

NANA can mine repeated PR review guidance into repo-scoped rules and feed approved rules back into related `work-on` roles.

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
- per-repo mode is set with `nana work-on defaults set <owner/repo> --review-rules-mode <manual|automatic>`
- per-repo reviewer policy is set with `nana work-on defaults set <owner/repo> --review-rules-trusted-reviewers <a,b>|none --review-rules-blocked-reviewers <a,b>|none --review-rules-min-distinct-reviewers <n>`
- `manual` mode keeps extracted rules as pending candidates until you approve them
- `automatic` mode auto-approves extracted rules and refreshes them during `investigate`, `work-on start`, and `work-on sync`
- only repeated high-signal guidance becomes a pending candidate
- extracted rules persist an origin/reason summary plus code-context provenance (`pr_head_sha`, `current_checkout`, or `unknown`)
- `approve` promotes candidates into approved rules
- `disable`, `enable`, and `archive` manage rule lifecycle without deleting evidence
- `explain` shows the stored evidence and provenance for a rule
- approved rules are injected into relevant `work-on` start, feedback, and lane instructions
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

[![Star History Chart](https://api.star-history.com/svg?repos=Yeachan-Heo/nana&type=date&legend=top-left)](https://www.star-history.com/#Yeachan-Heo/nana&type=date&legend=top-left)

## License

MIT
