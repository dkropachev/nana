# Assistant Workspace Manual Checklist

Use this checklist when validating the `nana start` assistant workspace in a browser.

## Startup

- Run `nana start` and open the printed `[start-ui] Web` URL.
- Confirm the sidebar shows `Home`, `Issues`, `Investigations`, `Work`, `Feedback`, `Approvals`, and `Repos`.
- Confirm the page loads without console syntax errors.

## Home

- Confirm totals update after refresh.
- Confirm pending jobs and work items render.
- Confirm repo overview opens the selected repo.

## Issues

- Filter by repo and status.
- Open an issue and confirm triage rationale, triage error, last run, and publication state render.
- Edit priority, schedule, and deferred reason and confirm the update persists.
- Trigger `Investigate` and `Start Work` and confirm success toasts and queue refresh.

## Investigations

- Start an investigation from the workspace.
- Confirm the run appears in the list.
- Open detail and confirm proofs, findings, and validator sections render without raw JSON dumps as the primary UI.

## Work

- Filter by repo, status, and backend.
- Open a run and confirm typed detail renders for phase, publication state, next action, and lane status.
- Use `Sync Run` on a GitHub-backed run and confirm the workspace refreshes.

## Feedback

- Run `Sync GitHub` and confirm review/reply queues refresh.
- Switch between `Reviews` and `Replies`.
- Open a draft and confirm review drafts show grouped inline comments and reply drafts show thread metadata.
- Revise and submit a draft from the drawer.

## Approvals

- Filter by repo, status, and kind.
- Open a blocked run approval and confirm reason, next action, and action kind render.
- Open a draft-ready approval and confirm it routes to the work-item drawer.
- Launch a planned-item approval from the workspace.

## Repo Tabs

- Open a repo from the sidebar.
- Confirm `Overview`, `Scouts`, `Config`, and `Controls` still work.
- Confirm settings save and scout actions still update the workspace state.
