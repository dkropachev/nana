# Skill Contribution Guide

Use this checklist when adding or changing a nana skill. Skills are a high-leverage UX surface: a vague trigger, oversized runtime document, or eager reference load can make unrelated prompts slower or less predictable.

## Source paths

- Add source skills under `skills/<skill-name>/SKILL.md`.
- Add `skills/<skill-name>/RUNTIME.md` only when the workflow is too detailed for the short skill entry or is listed in the lazy runtime table in `AGENTS.md` / `templates/AGENTS.md`.
- Keep generated or installed copies out of the repo. `nana setup --force` installs skills to the active Codex skills directory for local testing.

## New skill checklist

- **Name:** use lowercase kebab-case, and keep it distinct from existing skills and prompts.
- **Purpose:** state the user-facing outcome in one sentence before implementation details.
- **Activation:** document when to use the skill and when not to use it.
- **Trigger wording:** prefer explicit phrases over common verbs. Avoid generic triggers such as `fix`, `make`, `run`, `review`, or `debug` unless they are scoped by a second word.
- **Conflict check:** compare proposed triggers with existing entries in `AGENTS.md`, `templates/AGENTS.md`, `skills/*/SKILL.md`, and `prompts/*.md`.
- **Lazy runtime wiring:** if the skill needs implicit keyword activation, update both `templates/AGENTS.md` and the generated `AGENTS.md` entry in the same change. Explicit-only skills do not need a lazy trigger row.
- **Runtime size:** keep the initially loaded `SKILL.md` concise. Move long workflows, tables, examples, or role prompts into `RUNTIME.md` or targeted reference files.
- **Reference loading:** if a skill has `references/`, instruct agents to open only the specific file needed for the current variant. Do not require bulk-loading a whole folder.
- **Telemetry:** preserve the existing expectation that skill and reference loads are recorded as `skill_doc_load` and `skill_reference_load` events in `.nana/logs/context-telemetry.ndjson`. Do not log raw user arguments, tool output, secrets, or large prompt bodies. Run `nana telemetry summary` after runtime/reference changes and resolve skill-load budget warnings.
- **Fallback behavior:** say what the agent should do when the skill file, runtime document, script, or external CLI is missing. Prefer a safe degraded workflow over failing silently.
- **Verification:** include a minimal local check that proves the skill can be discovered and that repo diagnostics still pass.

## Trigger design examples

Prefer narrow phrases that describe intent:

| Good | Risky | Why |
| --- | --- | --- |
| `security review` | `security` | Reduces accidental activation during ordinary security discussions. |
| `fix build`, `type errors` | `fix` | Leaves general fixes on the normal execution path. |
| `clone website`, `web-clone` | `clone` | Avoids collisions with git or data-copy tasks. |
| `deep interview`, `gather requirements` | `interview` only | Keeps broad clarification opt-in unless the word is already part of an accepted skill contract. |

Before adding a trigger, run a quick scan:

```bash
grep -RIn "trigger phrase\|proposed-trigger\|\$skill-name" AGENTS.md templates/AGENTS.md skills prompts docs
```

Replace the sample terms with the exact trigger words and the proposed `$skill-name`.

## Recommended skill template

````markdown
---
name: my-skill
description: One concise sentence describing the outcome.
argument-hint: "<target> [options]"
---

# My Skill

## Purpose

State the job this skill performs and the user benefit.

## Use when

- The user explicitly invokes `$my-skill`.
- The user asks for a narrow task that matches these trigger phrases: `specific phrase`, `specific noun phrase`.

## Do not use when

- The request only mentions a related word casually.
- Another existing skill owns the workflow.
- The task needs user preference choices before execution.

## Workflow

1. Inspect the minimum repo context needed.
2. Load only the relevant runtime/reference file for the detected variant.
3. Execute the smallest safe change.
4. Verify with the command below.

## Fallback

If the optional runtime/reference/script is unavailable, continue with the documented manual workflow and report the missing artifact in the final note.

## Verification

```bash
go test ./...
```
````

For large workflows, keep the template as the short entry point and put detailed steps in `RUNTIME.md`.

## Performance and context budget

- Keep `SKILL.md` short enough to scan quickly; use links to specific runtime/reference files for variant detail.
- Treat `nana telemetry summary` warnings as regressions unless the workflow intentionally needs more context. Defaults warn above 8 skill/reference load events or above 3 reference loads in the selected run/session; use `--skill-load-budget` or `--reference-load-budget` to tune local checks.
- Prefer reusable scripts or templates over pasting long generated examples into the skill body.
- Make optional network calls, external CLIs, and expensive checks explicit in the workflow, with a documented fallback when unavailable.
- Do not add broad implicit triggers just to improve discoverability; document explicit `$skill-name` usage instead.

## Minimal verification for a new skill

Use the lightest command set that covers the change:

```bash
# Confirm the skill source exists and the trigger/name appears where expected.
test -f skills/my-skill/SKILL.md
grep -RIn "my-skill\|specific trigger" skills/my-skill AGENTS.md templates/AGENTS.md

# Run repo diagnostics before handoff.
make lint
```

When the skill changes generated setup behavior or installer output, also build and test the CLI in a disposable local environment:

```bash
go run ./cmd/nana-build build-go-cli
./bin/nana setup --force
./bin/nana doctor
```

Document any skipped heavy checks in the final report so the runtime verifier can cover them.
