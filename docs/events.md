# Events

The Events page lists repository configurations, preview templates, one-off PR preview rules, application rules, and preview groups. Each rule shows its watched repositories, comment command or branch, delivery method, and latest polling result.

Polling remains available when GitHub webhooks are disabled. Repository configurations use their configured interval. Preview polling scans repository-wide issue comments every 30 seconds and checks PRs associated with existing previews. It does not request comments separately for every PR. A cursor advances after comments, PR updates, cleanup, and pending preview reports have completed. Failed scans retain the cursor for retry.

## Activity

Activity includes polled commits and configuration changes, preview commands, on-demand checks, PR updates, and closures. Polling failures and recovery also appear. Unchanged successful scans update the rule's latest check without creating another activity row. Repeated identical failures update the check; a new failure or recovery creates an activity entry.

Filter by Polling, Webhooks, or Earlier activity. Load more to read older entries. View run opens the selected workflow run and its job and stage logs. A delivery being processed does not mean its build or deployment succeeded. The run has its own status. Rejected test commands show their reason and do not start QA.

Polling and webhooks share comment reservations and delivery identity. Receiving the same comment through both methods starts one run and retains its original delivery method. Activity stores delivery metadata and accepted run links, never raw webhook bodies, comment arguments, job output, or credentials. Users see only activity belonging to projects they can view.

The upgrade imports existing workflow events, preview comments, and earlier preview environments. Older preview comments did not record their delivery method, so those entries are labeled Earlier activity. New deliveries record Polling or Webhook explicitly.

## API

- `GET /api/v1/events/rules` returns visible rules and their current checks.
- `GET /api/v1/events/activity` returns up to 50 entries, a `total` count, and a `next` cursor when more entries exist.
- Filter with `transport=poll`, `transport=webhook`, or `transport=history`.
- Request another page with `before=<next>`, keeping the same transport filter.

Both endpoints require authentication. Project access is applied before pagination and counting.
