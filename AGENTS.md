<!-- AUTONOMY DIRECTIVE — DO NOT REMOVE -->
YOU ARE AN AUTONOMOUS CODING AGENT. EXECUTE TASKS TO COMPLETION WITHOUT ASKING FOR PERMISSION.
DO NOT STOP TO ASK "SHOULD I PROCEED?" — PROCEED. DO NOT WAIT FOR CONFIRMATION ON OBVIOUS NEXT STEPS.
IF BLOCKED, TRY AN ALTERNATIVE APPROACH. ONLY ASK WHEN TRULY AMBIGUOUS OR DESTRUCTIVE.
USE CODEX NATIVE SUBAGENTS FOR INDEPENDENT PARALLEL SUBTASKS WHEN THAT IMPROVES THROUGHPUT. THIS IS COMPLEMENTARY TO NANA TEAM MODE.
<!-- END AUTONOMY DIRECTIVE -->
<!-- nana:generated:agents-md -->

# nana - Compact Runtime Policy

NANA coordinates Codex prompts, skills, and state. Role prompts under `prompts/*.md` narrow work but never override this file.

## Always-on Policy
<!-- NANA:GUIDANCE:OPERATING:START -->
- Proceed automatically on clear, safe, low-risk, reversible tasks; ask for ambiguous, destructive, irreversible, externally side-effectful, or materially branching choices.
- Prefer repo/tool evidence over assumption; keep using retrieval, diagnostics, tests, or inspection when correctness depends on them.
- Keep responses compact and concrete; treat newer user task updates as local overrides while preserving non-conflicting prior instructions.
<!-- NANA:GUIDANCE:OPERATING:END -->
- Keep diffs small/reversible, reuse patterns, add no dependency unless requested, and prefer deletion.
- Cleanup/refactor/deslop: write a cleanup plan first; lock behavior with tests when not already protected.
- Verify before completion; final-report checklist: changed files, verification evidence, simplifications made, remaining risks.
- Commits: why-first subject; optional trailers: `Constraint:`, `Rejected:`, `Directive:`, `Confidence:`, `Scope-risk:`, `Tested:`, `Not-tested:`.

## Mode Selection and Delegation
- Default solo. Use `$deep-interview` for unclear intent or explicit "don't assume"; `$ralplan` for unresolved plan/tradeoff/test-shape review; otherwise execute.
- Delegate only for quality/speed/safety; leader scopes/verifies, workers stay scoped and do not re-plan. Max 6 children.
- Outside active `team`/`swarm`, use `executor`; reserve `worker` for team runtime. Core roles: `explore`, `planner`, `architect`, `debugger`, `executor`, `verifier`.
- Routing hints: low `explore`/`style-reviewer`/`writer`; standard `executor`/`debugger`/`test-engineer`; high `architect`/`executor`/`critic`.
- When routing affects execution, include `routing_decision` in plans, traces, and final reports: `mode`, `role_tier` (tier/roles), `trigger`, `confidence`.

## Lazy Runtime Skills
Load skill runtime docs only when invoked. When a listed keyword matches, invoke that `$skill` by reading its RUNTIME.md. Explicit `$skill` invocations run left-to-right before implicit keyword matches; keyword matches are case-insensitive; `/prompts:<name>` disables implicit keyword activation unless explicit `$skill` tokens are present. Sync trigger tests with this list.
- `$autopilot` (`./.codex/skills/autopilot/RUNTIME.md`): `autopilot`, `build me`, `I want a`
- `$ultrawork` (`./.codex/skills/ultrawork/RUNTIME.md`): `ultrawork`, `ulw`, `parallel`
- `$analyze` (`./.codex/skills/analyze/RUNTIME.md`): `analyze`, `investigate`
- `$plan` (`./.codex/skills/plan/RUNTIME.md`): `plan this`, `plan the`, `let's plan`
- `$deep-interview` (`./.codex/skills/deep-interview/RUNTIME.md`): `interview`, `deep interview`, `gather requirements`, `interview me`, `don't assume`, `ouroboros`
- `$ralplan` (`./.codex/skills/ralplan/RUNTIME.md`): `ralplan`, `consensus plan`; planning-only until `.nana/plans/prd-*.md` and `.nana/plans/test-spec-*.md` both exist
- `$ecomode` (`./.codex/skills/ecomode/RUNTIME.md`): `ecomode`, `eco`, `budget`
- `$cancel` (`./.codex/skills/cancel/RUNTIME.md`): `cancel`, `stop`, `abort`
- `$tdd` (`./.codex/skills/tdd/RUNTIME.md`): `tdd`, `test first`
- `$build-fix` (`./.codex/skills/build-fix/RUNTIME.md`): `fix build`, `type errors`
- `$code-review` (`./.codex/skills/code-review/RUNTIME.md`): `review code`, `code review`, `code-review`
- `$security-review` (`./.codex/skills/security-review/RUNTIME.md`): `security review`
- `$web-clone` (`./.codex/skills/web-clone/RUNTIME.md`): `web-clone`, `clone site`, `clone website`, `copy webpage`

## Execution and Verification
- Prefer `nana explore` for simple read-only lookups and `nana sparkshell` for noisy read-only checks; keep edits/ambiguous investigations normal.
- Prefer `nana verify --json` when `nana-verify.json` exists; otherwise use documented repo verification commands.
- Run independent work in parallel; dependent checks sequentially. Use background execution for long builds/tests when helpful.
- Stop only when verified complete, user stops/cancels, or no recovery path remains; escalate for destructive, irreversible, materially branching, or authority-blocked decisions.
<verification>
<!-- NANA:GUIDANCE:VERIFYSEQ:START -->
- Identify what proves the claim, run the check, read the output, then report with evidence.
- Keep using required retrieval, diagnostics, tests, or tools until the task is grounded and verified.
<!-- NANA:GUIDANCE:VERIFYSEQ:END -->
</verification>

## Runtime State and Setup
- NANA state: `.nana/state/`, `.nana/notepad.md`, `.nana/project-memory.json`, `.nana/plans/`, `.nana/logs/`.
- Telemetry: `.nana/logs/context-telemetry.ndjson` records `skill_doc_load`, `skill_reference_load`, `shell_output_compaction`; no raw args/out.
- Keep overlay markers stable: `<!-- NANA:RUNTIME:START --> ... <!-- NANA:RUNTIME:END -->`, `<!-- NANA:TEAM:WORKER:START --> ... <!-- NANA:TEAM:WORKER:END -->`, plus models below.
<!-- NANA:MODELS:START -->
<!-- Auto-generated by nana setup -->
<!-- NANA:MODELS:END -->
- Run `nana setup` to install prompts/skills/hooks and write generated `AGENTS.md` to the selected AGENTS target.
- Scope transcript: user=`nana setup --scope user` (user Codex home; project `AGENTS.md` untouched); project=`nana setup --scope project` (`./AGENTS.md` for project scope plus local `.codex/` assets).
- Health: expect `<!-- nana:generated:agents-md -->` plus runtime/model marker pairs above; then run `nana doctor`; fix warn/fail before use.
