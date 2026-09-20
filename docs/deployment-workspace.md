# Deployment workspace

The board and compact list open a deployment directly. Logs, topology, history,
and comparisons navigate in place. Browser Back restores the originating filters,
selected environment, and scroll position. Deployment headers identify the saved
target and revision and link the last successful release and latest attempt
separately. Historical targets come from saved snapshots, not today's app settings.

The **Running release** label identifies the last successful recorded deployment.
It does not independently establish current workload health. Configuration sync,
runtime drift, and resource health appear separately, with the saved observation's
age and a reason when unavailable. A failed newer attempt does not hide the last
successful release. The catalog includes quiet applications even when their last
successful run falls outside the overview's recent-run window.

Use **Board**, **List**, and **Compare environments** from the same filter bar.
Filters for application, project, environment, target, rollout state, and source
revision are encoded in the URL. List search pages through retained history; it is
not limited to overview's most recent 100 deployments. Board filters apply to the
current environment summaries. Pinning a list filters the loaded pages; load older
pages to find more retained runs for pinned applications.

Named filters, pinned environments, and recent applications are stored only in the
current browser and are separated by signed-in identity. The **Jump to** menu
(`Ctrl+K` / `Cmd+K`) searches accessible applications and environments, prioritizes
pins and recents, and opens the last successful release with one selection. It
falls back to the latest attempt when there has been no successful deployment.
Service impact rows and application inventory rows also link directly to releases.

Environment comparisons use two last-successful deployment snapshots within the
same project. They support repository-managed stages and separately registered
applications. The comparison includes saved chart inputs, image values, target
settings, and service configuration revisions. Credentials, chart defaults, and
live cluster values are excluded. Missing snapshots are reported as unavailable.
Use deployment History to select retained historical versions of one application.

## API

All routes require authentication and enforce project visibility before returning
records. GET operations below do not perform runtime checks.

| Route | Result |
| --- | --- |
| `GET /api/v1/deployment-catalog` | Accessible applications with current/latest runs, environment and target metadata, and saved status summaries. |
| `GET /api/v1/deployment-search` | At most 50 deployment summaries and an optional `next` cursor. Accepts `q`, `project`, `application`, `environment`, `target`, `status`, `revision`, and `before`. |
| `GET /api/v1/deployments/{id}/identity` | Saved deployment target identity, environment, latest attempt, and last successful release. |
| `GET /api/v1/deployments/{id}/compare-environment?from={id}` | Sanitized snapshot comparison across visible applications within the same project. |

The existing same-application history and comparison APIs remain available. The
full request/response contract is in [OpenAPI](openapi.yaml).
