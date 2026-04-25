# Assistant Workspace Manual Checklist

Use this checklist when validating the `nana start` assistant workspace in a browser.

## Startup

- Run `nana start` and open the printed `[start-ui] Web` URL.
- Confirm the sidebar shows `Overview`, `Tasks`, `Issues`, `Feedback`, `Approvals`, `Usage`, and `Repos`.
- Confirm the header repo picker defaults to `All repos`.
- Confirm the page loads without console syntax errors.

## Latency

- Restart `nana start`, then open the printed `[start-ui] Web` URL immediately.
- Confirm `Home` or `Usage` renders without a long blank wait while the process is still warming caches.
- Reload the page once and confirm the second load is visibly faster than the first.
- Leave the workspace open on `Usage` for at least 30 seconds and confirm it refreshes only while the Usage view is visible.
- Switch away from `Usage` or hide the tab and confirm Usage polling stops while overview SSE updates continue.

## Overview

- Confirm totals update after refresh.
- Confirm pending jobs and work items render.
- Open `Onboard new repo...` from the header repo picker and confirm the onboarding drawer opens without leaving the page.
- Confirm repo overview opens the selected repo.

## Usage

- Open `Usage` and confirm the existing charts and top sessions render.
- Select a repo from the header repo picker while staying on `Usage` and confirm the page stays on `Usage`.
- Confirm the Usage scope banner appears and `All repos` clears only repo scope while keeping the `Usage` page open.
- Confirm repo-scoped Usage reflects only managed Nana work for that repo, not unrelated workspace-wide sessions.
- Leave the workspace open on `Usage` for at least 30 seconds and confirm it refreshes only while the Usage view is visible.
- Switch away from `Usage` or hide the tab and confirm Usage polling stops while overview SSE updates continue.

## Issues

- Scope the page from the header repo picker and filter by status.
- Open an issue and confirm triage rationale, triage error, last run, and publication state render.
- Edit priority, schedule, and deferred reason and confirm the update persists.
- Trigger `Investigate` and `Start Work` and confirm success toasts and queue refresh.

## Investigations

- Start an investigation from the workspace.
- Confirm the run appears in the list.
- Open detail and confirm proofs, findings, and validator sections render without raw JSON dumps as the primary UI.

## Tasks Detail

- Scope `Tasks` from the header repo picker and filter by status and backend.
- Open a queue item and confirm inline QueueItem detail renders phase, publication state, next action, and lane status.
- Use `Sync Run` on a GitHub-backed run and confirm the workspace refreshes.

## Feedback

- Run `Sync GitHub` and confirm review/reply queues refresh.
- Switch between `Reviews` and `Replies`.
- Open a draft and confirm review drafts show grouped inline comments and reply drafts show thread metadata.
- Revise and submit a draft from the drawer.

## Approvals

- Scope the page from the header repo picker and filter by status and kind.
- Open a blocked run approval and confirm reason, next action, and action kind render.
- Open a draft-ready approval and confirm it routes to the work-item drawer.
- Launch a planned-item approval from the workspace.
- Confirm a scout job in one-shot stale-startup recovery does not appear in `Approvals`; only a repeated stale cleanup should surface there as a failed scout approval.

## Repo Pages

- Open a repo from the header repo picker or a repo card.
- Confirm `Overview` and `Config` work.
- Confirm settings save and update the workspace state.
- Confirm legacy `tab=scouts` and `tab=controls` links normalize back to repo `Overview`.
