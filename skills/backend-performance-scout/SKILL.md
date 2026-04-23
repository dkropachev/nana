---
name: backend-performance-scout
description: Discover backend/API/JS CPU hotspots first, then fan out read-only optimization findings
argument-hint: "<path|service|repo>"
---

# Backend Performance Scout

Use this skill for backend, API, worker, or non-UI JavaScript performance scouting. It is discovery-first: rank likely CPU hotspots before delegating the strongest candidates to separate read-only optimization-finding lanes.

## Use when

- The user asks for backend or API performance scouting, hot paths, CPU-heavy processes, or JavaScript hotspots.
- The target is server-side JS/TS, workers, queues, schedulers, CLIs, or shared non-UI modules.
- The right first step is to decide where optimization work belongs before changing code.

## Do not use when

- The task is browser or UI performance: rendering, paint, layout, animation smoothness, Core Web Vitals, or page-load tuning.
- The user wants immediate implementation instead of a scouting pass.
- The answer depends primarily on unavailable production metrics or profilers.

## Workflow

1. Inventory likely hot surfaces: request handlers, middleware, background jobs, parsers, serializers, transforms, compression, crypto, and other CPU-sensitive paths.
2. Rank a hotspot shortlist with repo evidence before delegation.
3. Delegate only the top independent candidates to `performance-reviewer` lanes when available.
4. Return a compact report with discovery shortlist, merged optimization findings, and measurement gaps.

Detailed runtime behavior, routing expectations, and lane contract live in [RUNTIME.md](/extra/dkropachev/nana/skills/backend-performance-scout/RUNTIME.md).

## Fallback

If delegation is unavailable, perform the same discovery pass locally and return provisional optimization findings yourself. If the repo lacks backend or non-UI JavaScript code, say so explicitly and stop instead of drifting into UI analysis.

## Verification

```bash
test -f skills/backend-performance-scout/SKILL.md
test -f skills/backend-performance-scout/RUNTIME.md
rg -n "backend-performance-scout|backend performance scout|api hot paths|cpu hotspots" skills/backend-performance-scout cmd/nana/main.go internal/gocli/lazy_skill_triggers.json templates/AGENTS.md AGENTS.md
go test ./cmd/nana ./internal/gocli ./internal/gocliassets
```
