# Assistant Workspace Manual Checklist

Use this checklist when validating the `nana start` assistant workspace in a browser.

## Startup

- Run `nana start` and open the printed `[start-ui] Web` URL.
- Confirm the sidebar shows `Attention`, `Usage`, and `Repos`, with repo entries underneath.
- Confirm quick switch offers `Attention`, `Usage`, repo targets, and recent run targets, but not retired top-level queue pages.
- Confirm the page loads without console syntax errors.

## Latency

- Restart `nana start`, then open the printed `[start-ui] Web` URL immediately.
- Confirm `Attention` or `Usage` renders without a long blank wait while the process is still warming caches.
- Reload the page once and confirm the second load is visibly faster than the first.
- Leave the workspace open on `Usage` for at least 30 seconds and confirm it refreshes only while the Usage view is visible.
- Switch away from `Usage` or hide the tab and confirm Usage polling stops while overview SSE updates continue.

## Attention

- Confirm totals update after refresh.
- Confirm pending jobs and work items render.
- Confirm repo overview opens the selected repo.
- Filter `Attention` by repo, status, and kind.
- Open an issue tile and confirm triage rationale, triage error, last run, and publication state render.
- Edit priority, schedule, and deferred reason from the issue detail pane and confirm the update persists.
- Open a blocked run tile and confirm typed detail renders for phase, publication state, next action, and lane status.
- Use `Sync Run` on a GitHub-backed run and confirm the workspace refreshes.
- Open a draft-ready work-item tile and confirm it routes to the work-item drawer.
- Run `Sync GitHub` from `Attention` and confirm review/reply work items refresh through the existing attention and drawer flows.
- Launch a planned-item approval from `Attention`.
- Confirm a scout job in one-shot stale-startup recovery does not appear as an approval tile; only a repeated stale cleanup should surface there as a failed scout approval.

## Legacy Redirects

- Open `#view=issues` and confirm the browser canonicalizes to `#view=home&kind=issue`.
- Open `#view=investigations` and confirm the browser canonicalizes to `#view=home&kind=investigation`.
- Open `#view=approvals` and confirm the browser canonicalizes to `#view=home&kind=approval`.
- Open `#view=work` and confirm the browser canonicalizes to `#view=home&kind=work_run`.
- Confirm those routes render `Attention`, not a retired page shell.

## Repo Tabs

- Open a repo from the sidebar.
- Confirm `Overview`, `Scouts`, `Config`, and `Controls` still work.
- Confirm settings save and scout actions still update the workspace state.
- Confirm an auto-recovered scout item in `Scouts` shows recovery count, recovered run id, recovery timestamp, and cooldown metadata in the detail pane.
- Confirm `Overview` stays read-only while operational scheduling/findings/import tooling lives under `Controls`.
- In `Controls`, search GitHub issues, schedule a tracked issue, edit a scheduled item, and launch a queued planned item.
- In `Controls`, schedule a repo task, then confirm it appears in `Scheduled Tasks`.
- In `Controls`, review findings, save one, dismiss or promote one, and confirm the update persists.
- In `Controls`, import findings from markdown, review candidates, and promote/drop candidates from the import review surface.
