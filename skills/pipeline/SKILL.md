---
name: pipeline
description: Configurable pipeline orchestrator for sequencing stages
---

# Pipeline Skill

`$pipeline` is the configurable pipeline orchestrator for NANA. It sequences stages
through a uniform `PipelineStage` interface, with state persistence and resume support.

## Default Autopilot Pipeline

The canonical NANA pipeline sequences these internal stages:

```
RALPLAN (consensus planning) -> team-exec (Codex CLI workers) -> ralph-verify (architect verification)
```

## Configuration

Pipeline parameters are configurable per run:

| Parameter | Default | Description |
|-----------|---------|-------------|
| `maxRalphIterations` | 10 | Ralph verification iteration ceiling |
| `workerCount` | 2 | Number of Codex CLI execution workers |
| `agentType` | `executor` | Agent type for execution workers |

## Stage Interface

Every stage implements the `PipelineStage` interface:

```typescript
interface PipelineStage {
  readonly name: string;
  run(ctx: StageContext): Promise<StageResult>;
  canSkip?(ctx: StageContext): boolean;
}
```

Stages receive a `StageContext` with accumulated artifacts from prior stages and
return a `StageResult` with status, artifacts, and duration.

## Built-in Stages

- **ralplan**: Consensus planning (planner + architect + critic). Skips only when both `prd-*.md` and `test-spec-*.md` planning artifacts already exist, and carries any `deep-interview-*.md` spec paths forward for traceability.
- **team-exec**: Coordinated execution via Codex CLI workers. Internal stage id retained for compatibility.
- **ralph-verify**: Verification loop with configurable iteration count. Internal stage id retained for compatibility.

## State Management

Pipeline state persists via the ModeState system at `.nana/state/pipeline-state.json`.
The HUD renders pipeline phase automatically. Resume is supported from the last incomplete stage.

- **On start**: `state_write({mode: "pipeline", active: true, current_phase: "stage:ralplan"})`
- **On stage transitions**: `state_write({mode: "pipeline", current_phase: "stage:<name>"})`
- **On completion**: `state_write({mode: "pipeline", active: false, current_phase: "complete"})`

## API

```typescript
import {
  runPipeline,
  createAutopilotPipelineConfig,
  createRalplanStage,
  createTeamExecStage,
  createRalphVerifyStage,
} from './pipeline/index.js';

const config = createAutopilotPipelineConfig('build feature X', {
  stages: [
    createRalplanStage(),
    createTeamExecStage({ workerCount: 3, agentType: 'executor' }),
    createRalphVerifyStage({ maxIterations: 15 }),
  ],
});

const result = await runPipeline(config);
```

## Relationship to Other Modes

- **autopilot**: Autopilot can use pipeline as its execution engine (v0.8+)
- **execution backend**: Pipeline delegates the execution stage to the internal coordinated-worker runtime
- **verification backend**: Pipeline delegates the verification stage to the internal persistence verifier
- **ralplan**: Pipeline's first stage runs RALPLAN consensus planning
