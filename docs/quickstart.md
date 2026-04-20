# nana CLI Command-Discovery Quickstart

Use this when `nana` is installed and you want the shortest path from a fresh shell to a useful first session. It maps the common commands to the workflows they support instead of listing every option.

## Five-minute path

From the repository you want NANA to understand:

```bash
nana setup
nana doctor
nana help workflows
if [ -f nana-verify.json ]; then
  nana verify --json
else
  echo "No nana-verify.json found; run this project's native checks instead."
fi
nana explore --prompt "find the repo verification profile"
nana sparkshell git status --short
```

Then start a Codex session with NANA guidance:

```bash
nana --madmax --high
```

Inside that session, invoke skills explicitly with `$name` syntax:

```text
$deep-interview "clarify the authentication change"
$ralplan "approve the implementation plan and test shape"
```

If the active NANA run needs to stop cleanly, cancel it instead of deleting state by hand:

```text
$cancel
```

## Command map

| If you want to... | Start with | Why |
| --- | --- | --- |
| Install or refresh NANA guidance | `nana setup` | Writes prompts, skills, hooks, config, and generated `AGENTS.md` guidance for the selected scope. |
| Check whether the install is healthy | `nana doctor` | Validates the local setup before you debug workflow symptoms. |
| Discover safe entry points | `nana help workflows` | Shows modes, trigger phrases, common skills, and support commands in one compact index. |
| Run this repo's profiled checks | `nana verify --json` when `nana-verify.json` exists | Uses the repo-native verification profile and returns machine-readable evidence; otherwise use the project's native checks. |
| Inspect code without editing | `nana explore --prompt "..."` | Runs a read-only repository lookup when you need a fast answer before changing files. |
| Summarize noisy shell output | `nana sparkshell <command>` | Runs bounded command inspection with compact summaries; use it for read-only diagnostics and verification output. |
| See the next operator action | `nana next` | Reduces queue/status noise to one suggested next step. |
| Check active local runtime state | `nana status` or `nana hud --watch` | Shows active modes and status when a session or runtime feels stuck. |
| Stop the current NANA run safely | `nana cancel` or `$cancel` | Cancels current session-scoped modes; prefer this over manually deleting `.nana/state/*`. |
| Browse or invoke skills | `/skills`, then `$skill-name "..."` | Skills wrap repeatable workflows such as `$deep-interview`, `$ralplan`, `$build-fix`, and `$code-review`. |

## First-use sequence explained

1. **Set up once per scope.** Run `nana setup` after installation, after upgrading NANA, or when prompts/skills/config look stale.
2. **Diagnose before guessing.** Run `nana doctor` before editing generated files or deleting runtime state.
3. **Learn the workflow surface.** Run `nana help workflows` for the command and skill discovery index.
4. **Prove the repo baseline.** Run `nana verify --json` from a repo with a verification profile before asking NANA to change behavior.
5. **Inspect read-only first.** Use `nana explore --prompt "..."` for source lookups and `nana sparkshell <command>` for bounded shell diagnostics.
6. **Use skills for intent.** In Codex, prefer explicit `$skill` calls when you know the workflow you want; explicit skill names run before natural-language trigger phrases.
7. **Cancel safely.** If an autonomous mode, parallel helper, or planning flow is active and no longer wanted, use `nana cancel` from the shell or `$cancel` in-session.

## Common skill starters

```text
$deep-interview "gather requirements for ..."
$ralplan "review the plan for ..."
$analyze "investigate why ..."
$build-fix "fix build/type errors"
$code-review "review current branch"
$security-review "review this change for security issues"
```

Use `$cancel` for active NANA state cleanup. It is the supported shutdown path for current session-scoped runtime state and should be tried before manual cleanup.

## What not to do first

- Do not manually delete `.nana/state/*` when `nana cancel` or `$cancel` can clear active state.
- Do not use `nana sparkshell` for mutating commands; keep it for inspection and bounded verification output.
- Do not skip `nana doctor` when setup, prompt, skill, or hook behavior looks wrong.
