# NANA state file reference

NANA keeps project-local runtime state under `.nana/` and managed verification
configuration under `~/.nana/work/repos/.../verification-plan.json`. These
files are intended to be inspectable and safe to regenerate, migrate, or
validate in CI.

Managed implementation runtimes also persist state outside the repo under
`~/.nana/work/`. The key work manifests now include `work_type`, and completed
or in-flight runs may also expose `followup_decision` plus `followup_rounds`
when the post-implementation followup planner/reviewer loop has run.

## Files and expected shapes

| Path | Shape | Validation |
| --- | --- | --- |
| `.nana/project-memory.json` | JSON object. `{}` is valid; common optional keys are `version`, `updated_at`, `decisions`, `facts`, `constraints`, `preferences`, and `notes`. | JSON Schema: [`../schemas/nana-project-memory.schema.json`](../schemas/nana-project-memory.schema.json). `nana doctor` checks the top-level object, known timestamp fields, and known array fields. |
| `.nana/notepad.md` | UTF-8 Markdown scratchpad for durable human/agent notes. Setup seeds `# NANA Notepad`. | `nana doctor` verifies it is UTF-8 text when present. |
| `.nana/logs/context-telemetry.ndjson` | NDJSON: one telemetry JSON object per line. Events record metadata only, never raw args or raw command output. | Event schema: [`../schemas/context-telemetry-event.schema.json`](../schemas/context-telemetry-event.schema.json). `nana doctor` validates a bounded prefix so long-running logs stay cheap to inspect, and rejects raw `command`, `args`, `arguments`, `raw_args`, `stdout`, `stderr`, or `output` fields. |
| `.nana/plans/*.md` | UTF-8 Markdown plans. Common names are `prd-<slug>.md`, `test-spec-<slug>.md`, and `open-questions.md`. | `nana doctor` verifies Markdown plan files are UTF-8 text when present. |
| `~/.nana/work/repos/<repo-id-or-owner/repo>/verification-plan.json` | JSON object with a sequential `stages` array plus categorized `lint` / `compile` / `unit` / `integration` arrays. | JSON Schema: [`../schemas/verification-plan.schema.json`](../schemas/verification-plan.schema.json). `nana doctor` reuses the same profile normalization as `nana verify`. |

## Managed work manifests

These files live under `~/.nana/work/` rather than the source repo:

| Path | Shape | Notes |
| --- | --- | --- |
| `~/.nana/work/repos/<repo-id>/runs/<run-id>/manifest.json` | Local-work manifest JSON object. | Includes runtime fields such as `status`, `current_phase`, `work_type`, `iterations`, and optional `followup_decision` / `followup_rounds`. |
| `~/.nana/work/repos/<owner>/<repo>/runs/<run-id>/manifest.json` | GitHub-work manifest JSON object. | Includes `target_url`, `target_kind`, `work_type`, completion summaries, and optional `followup_decision` / `followup_rounds`. |
| `~/.nana/work/repos/<owner>/<repo>/start-state.json` | Start automation export/recovery JSON object. | Repo metadata plus derived task/export details used for visibility and recovery; canonical task state and saved task templates now live in `~/.nana/work/state.db`. |

## Minimal examples

### `.nana/project-memory.json`

```json
{}
```

Structured memory can add known arrays while remaining extension-friendly:

```json
{
  "$schema": "../docs/schemas/nana-project-memory.schema.json",
  "version": 1,
  "updated_at": "2026-04-20T00:00:00Z",
  "decisions": [
    {
      "text": "Use nana verify as the canonical local completion gate.",
      "source": "AGENTS.md"
    }
  ],
  "facts": ["Runtime state lives under .nana/."]
}
```

### `.nana/logs/context-telemetry.ndjson`

```jsonl
{"timestamp":"2026-04-20T00:00:00Z","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}
{"timestamp":"2026-04-20T00:00:01Z","tool":"nana-sparkshell","event":"shell_output_compaction","command_name":"go","argument_count":2,"exit_code":0,"stdout_bytes":1200,"stderr_bytes":0,"captured_bytes":1200,"stdout_lines":80,"stderr_lines":0,"summary_bytes":320,"summary_lines":6,"summarized":true}
```

Telemetry events intentionally store `command_name` and counts instead of full
commands, arguments, stdout, stderr, or raw output. `tool` is optional for
skill/reference events so existing local logs remain valid.
Use `nana telemetry summary --run-id <id> [--turn-id <id>]` to inspect
privacy-preserving skill/reference counts and single-run or single-turn
context-budget warnings. When the current turn or run exceeds the same
thresholds, session instructions include an advisory skill-context budget block
sourced from this telemetry.

### `.nana/notepad.md`

```markdown
# NANA Notepad

- Current focus: tighten runtime state validation.
- Follow-up: migrate old memory entries after schema warnings are clean.
```

### `.nana/plans/prd-example.md`

```markdown
# PRD: Example

## Requirements Summary
- The behavior being implemented.

## Acceptance Criteria
- [ ] The expected outcome is testable.

## Verification Steps
- Run `nana repo onboard --repo .` once, then `nana verify --json`.
```

### `verification-plan.json`

```json
{
  "$schema": "docs/schemas/verification-plan.schema.json",
  "version": 1,
  "name": "example",
  "source": "heuristic",
  "stages": [
    {
      "name": "test",
      "command": "go test ./..."
    }
  ],
  "lint": [],
  "compile": [],
  "unit": ["go test ./..."],
  "integration": []
}
```
