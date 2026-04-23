---
name: backend-performance-scout
description: Compact runtime contract for backend/API CPU hotspot scouting
---

<Purpose>
Discover backend, API, worker, and other non-UI JavaScript CPU hotspots first, then fan out only the strongest candidates for deeper optimization findings.
</Purpose>

<Use_When>
- The user says `backend performance scout`, `api hot paths`, or `cpu hotspots`.
- The main task is read-only hotspot discovery before implementation.
- The target is server-side JS/TS, workers, queues, schedulers, CLIs, or shared non-UI modules.
</Use_When>

<Runtime_Rules>
- Stay out of browser and UI performance work.
- Discovery first: inventory and rank likely hot paths before delegating.
- Prefer concrete hotspot signals: repeated scans over large collections, synchronous CPU on request paths, repeated JSON or schema work, regex-heavy parsing, serialization churn, compression, crypto, redundant cloning, high-fanout orchestration, or N+1 amplification.
- Delegate only the top independent hotspots, usually 2-5.
- Use `performance-reviewer` for hotspot lanes when available; otherwise use `architect` or `debugger`.
- Keep each lane read-only and scoped to one hotspot or tight file cluster.
- Final output must separate `Discovery shortlist`, `Optimization findings`, and `Measurement gaps`.
</Runtime_Rules>

<Worker_Output>
- current behavior and likely hot path
- root cause with file:line evidence
- concrete optimization options
- expected impact or complexity shift
- tradeoffs or regression risk
- measurement or benchmark needed to confirm the win
</Worker_Output>
