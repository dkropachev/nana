# `nana investigate` report contract

`nana investigate` is a source-backed investigation runtime with a machine-validated JSON report and a validator loop.

## Runtime shape

Commands:

```bash
nana investigate <user-input>
nana investigate onboard
nana investigate doctor
```

The generic top-level command is distinct from:

```bash
nana issue investigate <github-issue-url>
```

That nested issue command remains the GitHub issue preflight surface.

## Onboarding config

`nana investigate onboard` writes:

- `<resolved investigate CODEX_HOME>/config.toml`
- `<resolved investigate CODEX_HOME>/investigate-mcp-status.json` after doctor/probe runs

Onboarding manages the dedicated investigate config directly. It does not rely on the main Codex config, and it does not maintain a separate source registry.

## MCP health model

- Nana asks Codex to probe whichever MCPs are currently configured in the dedicated investigate Codex home.
- If no MCPs are configured there, investigate runs in local-source-only mode.
- If MCPs are configured there, `nana investigate doctor` verifies that they are usable and caches the result in `investigate-mcp-status.json`.

## Final report schema

The accepted report is JSON only.

```json
{
  "overall_status": "REFUTED|CONFIRMED|PARTIALLY_CONFIRMED",
  "overall_short_explanation": "string",
  "overall_detailed_explanation": "string",
  "overall_proofs": [
    {
      "kind": "source_code|build_log|jenkins_run|github|jira|local_artifact|documentation|other",
      "title": "string",
      "link": "string",
      "why_it_proves": "string",
      "is_primary": true,
      "path": "/abs/path/when-applicable",
      "line": 123
    }
  ],
  "issues": [
    {
      "id": "string",
      "short_explanation": "string",
      "detailed_explanation": "string",
      "proofs": []
    }
  ]
}
```

## Evidence rules

- Every important claim must have proof links.
- Every issue must include at least one primary non-documentation proof.
- `overall_proofs` must include at least one primary non-documentation proof.
- Documentation is supplementary only.
- If source code or runtime evidence is available, documentation must not be accepted as a primary proof.

## Link validation rules

- `source_code`: absolute local file path or valid code URL; local paths must exist and the line must be in range when present.
- `github`: must use a `https://github.com/...` URL.
- `jira`: must use an Atlassian URL or Jira ARI.
- `jenkins_run`: must use an `http(s)` URL.
- `build_log` and `local_artifact`: must use an `http(s)` URL or an existing local artifact path.

## Validator loop

- The supervisor runs structural validation first.
- Then a validator agent re-checks the report against source evidence.
- Violations are fed back into a new investigator round.
- The loop is bounded to 3 rounds.
- Only an accepted report is written to `final-report.json`.

## Run artifacts

Each run stores artifacts under:

```text
.nana/logs/investigate/<run-id>/
```

Including:
- `manifest.json`
- `readiness.json`
- `round-N-investigator-prompt.md`
- `round-N-investigator-stdout.log`
- `round-N-report.json`
- `round-N-validator-prompt.md`
- `round-N-validator-stdout.log`
- `round-N-validator-result.json`
- `final-report.json` on acceptance
