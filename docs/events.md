# Events

The Events page lists repository configurations, preview templates, one-off PR preview rules, application rules, and preview groups. Each rule shows its watched repositories, comment command or branch, delivery method, and latest polling result.

Polling remains available when GitHub webhooks are disabled. Repository configurations use their configured interval. Preview polling scans repository-wide issue comments every 30 seconds and checks PRs associated with existing previews. It does not request comments separately for every PR. A cursor advances after comments, PR updates, cleanup, and pending preview reports have completed. Failed scans retain the cursor for retry.

## Webhook receipts and recovery

A supported, correctly signed webhook receives `202 Accepted` after Dispatch commits an encrypted delivery receipt. GitHub installation lookups, builds, preview commands, and cleanup run in the background. The response contains `receipt` and `duplicate`; it does not report deployment completion. A repeated delivery or identical signed body returns `200` with the original receipt. Reusing a delivery ID for a different body returns `409`. Unsupported events receive `204` without changing state; malformed payloads and invalid signatures never enter the queue. Encrypted storage is required.

The worker retries failed handling with exponential backoff from 10 seconds to one hour, for up to seven days after receipt. An attempt has a four-minute timeout and a five-minute lease. After a controller restart, queued deliveries resume and interrupted attempts become available when their leases expire. Missing encryption keys, repository access failures, and cleanup failures remain visible as retries. Correct the underlying problem before the retry window ends. A rejected installation or unbound repository deletion is terminal. Repository deletion requires the previously pinned repository ID and the App that received the signed event; it never relies on a post-deletion GitHub lookup.

Preview commands and push scheduling keep durable per-resource receipts. Saving a revision also saves its command receipt; retries return that run without cancelling newer work. Polling shares the same preview comment identity. New generated previews and their lifecycle/source-trust owner are committed together. Accepted workflow runs retain their own recovery rules: interrupted external build or deployment work is not blindly rerun.

Successful and rejected deliveries erase their encrypted payload immediately. The worker erases remaining payloads in its next retention pass after the seven-day retry window ends; passes run at startup and every 15 minutes. Terminal activity expires after 30 days; current polling checks and unfinished activity remain. Compact delivery IDs, signed-body digests, command receipts, PR close markers, and polling cursors remain, so retention cannot turn an old webhook into a new deployment. An expired receipt is evidence only; a new explicit command or manual workflow run is needed to request new work.

## Activity

Webhook delivery rows show queue state, attempt count, and next retry time. Logical command and workflow rows show the resulting runs and preview links; receiving a webhook for an already-polled command adds receipt evidence without adding a run.

Activity includes polled commits and configuration changes, preview commands, on-demand checks, PR updates, and closures. Polling failures and recovery also appear. Unchanged successful scans update the rule's latest check without creating another activity row. Repeated identical failures update the check; a new failure or recovery creates an activity entry.

Repository scans appear as Source changed when they start a workflow run. Scans that find no change or lose a scheduling race do not appear in Activity. Older scan records without a run are also excluded from the feed and its count. The Rules tab still shows the latest polling result and check time.

Filter by Polling, Webhooks, or Earlier activity. Load more to read older entries. View run opens the selected workflow run and its job and stage logs. A delivery being processed does not mean its build or deployment succeeded. The run has its own status. Rejected test commands show their reason and do not start QA.

Polling and webhooks share comment reservations and delivery identity. Receiving the same comment through both methods starts one run and retains its original delivery method. Activity stores delivery metadata and accepted run links, never raw webhook bodies, comment arguments, job output, or credentials. Users see only activity belonging to projects they can view.

The upgrade imports existing workflow events, preview comments, and earlier preview environments. Older preview comments did not record their delivery method, so those entries are labeled Earlier activity. New deliveries record Polling or Webhook explicitly.

## API

- `GET /api/v1/events/rules` returns visible rules and their current checks.
- `GET /api/v1/events/activity` returns up to 50 entries, a `total` count, and a `next` cursor when more entries exist.
- Filter with `transport=poll`, `transport=webhook`, or `transport=history`.
- Request another page with `before=<next>`, keeping the same transport filter.

These endpoints require authentication. Project access is applied before pagination and counting.

- `GET /api/v1/events/deliveries` is an owner-only inventory of up to 50 delivery receipts, including events without a matching rule. Use `before=<last-receipt-id>` for another page. Results contain safe action IDs or a lifecycle outcome, never the private payload, body digest, or lease token. After 30 days, only compact receipt metadata remains.
