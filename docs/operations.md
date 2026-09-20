# Controller operations

The Operations page has an overview with recent activity and follow-up actions,
plus separate Activity, Ownership, Cleanup, Backups, and Access mappings views.
Project visibility applies to overview counts, audit results, and application
ownership. Controller backups and identity mappings require controller owner
access. History cleanup requires project admin access.

## Activity and ownership

`GET /api/v1/operations/summary?projectId=<optional project>` returns an
`observedAt` timestamp, audit counts and recent events, and ownership counts. Audit
counts cover the exact trailing 24 hours, from `audit.since` inclusive to
`observedAt` exclusive. `audit.total` and `audit.rejected` count every matching
record; `audit.recent` contains up to five events. Ownership counts include all
active applications that are not templates, including generated applications.
The summary uses current project grants, including expiry and revocation. Without
an explicit project, members see only their visible projects. Owners also see
controller-wide audit events. An unreadable explicit project returns 403.

Authenticated mutations record the actor, impersonator when present, route action,
resource identity, outcome, and time. Request bodies, query strings, credential
values, manifests, and provider responses are not recorded. A `succeeded` audit
outcome means the HTTP request was accepted; background deployment work can still
fail later. A `rejected` outcome records an HTTP error response.

Filter `GET /api/v1/audit` with `projectId`, `appId`, `actorId`, `action`, `q`,
`outcome`, `since`, `until`, and `before`. `outcome` accepts `succeeded` or `rejected`.
The literal, case-insensitive `q` search accepts up to 200 characters and matches
actor metadata, route actions, resource/application/project IDs, and current
application or project names within scope. Missing historical names are not
reconstructed. `since` and `until` accept RFC3339 timestamps; the range includes
`since` and excludes `until`. Invalid or reversed bounds return 400. The summary's
rejection drilldown sends its exact `audit.since` and `observedAt` values.

All filters apply before pagination. Responses contain up to 100 events ordered
by descending event ID. Pass the last returned ID as `before` to continue. Events
start when audit collection is installed; historical actor identities are not
inferred. Every request rechecks access, and responses are marked `no-store`.

`GET /api/v1/operations/ownership` returns `{items, next?}` with up to 100 active
applications that are not templates, including generated applications. Each item
contains `appId`, `appName`, `projectId`, and an optional `owner` with
`principalType`, `principalId`, `displayName`, and `updatedAt`. A removed principal
retains its recorded ID and has an empty display name.

Ownership filters are `projectId`, `q`, `unassigned`, and the opaque `before`
cursor returned as `next`. Search matches application names/IDs and owner
names/IDs before pagination. Omit `unassigned` for all applications, use `true`
for applications without an owner, and `false` for assigned applications. Results
sort by application name and ID. Reset the cursor when changing filters.

Application owners are people or teams. The label records responsibility and does
not grant permissions. Project operators can choose from available project owner
candidates. The application and deployment views display the assigned name.

Project grants accept an optional `expiresAt` timestamp. The API checks expiration
on every authorization decision, including existing sessions. The Access editor
can set, preserve, or clear an expiry. An expired grant remains visible to owners
for review but grants no access.

GitHub sign-in providers can map `organization/team-slug` to a Dispatch team.
When a provider has mappings, sign-in requests `read:org`, verifies the complete
team list through the provider API, and refreshes only memberships managed by that
provider. Manual memberships remain separate. Removed mappings revoke their own
memberships immediately. Changes at GitHub take effect on the next successful
sign-in. Provider failures do not create membership grants.

## Service impact and credential rotation

Services show each consumer's environment, target, applied service revision, and
link to the running release. Select an application to open its binding editor.

Choose **Rotate credentials** to replace write-only inputs. Omitted credentials
are preserved; saving increments the service revision when inputs change and
resets the connection check. Saving never deploys consumers automatically.

The impact table allows operators with deployment permission to select consumers
and confirm a redeployment. Multiple bindings for one application create one
selected consumer. Dispatch validates all selected consumers before accepting
work. Applications with active deployments, no successful release, or changed
application inputs must be reviewed separately. Accepted runs use the last
successful source revision, current application configuration, and newly captured
service inputs. Each acceptance or refusal is reported. Partial acceptance can
occur if another operator starts a deployment during the request.

## Retention

Each project stores log age, failed-run age, and a minimum number of runs per
application. Saving a policy does not schedule cleanup. The Cleanup view uses
three separate steps:

1. Edit the limits and choose **Save policy**.
2. Choose **Preview cleanup** to inspect eligible and protected counts.
3. Choose **Review removal**, confirm the selected project, and apply cleanup.

Preview and apply accept an optional `expectedPolicy` containing the reviewed
`projectId`, `logDays`, `runDays`, and `keepRuns`. Both compare it with the saved
policy in the same transaction that evaluates cleanup. A changed policy returns
409 and requires a fresh review. Apply also requires `confirm` to equal the
project ID. The UI sends the policy captured for preview and never writes a policy
with PUT during apply. Eligibility is checked again at apply, so counts may change
after preview. Existing API clients may omit `expectedPolicy`; an empty preview
body and confirm-only apply remain supported.

Cleanup preserves:

- Active and successful deployments, including historical rollback candidates.
- Encrypted drift baselines and the runs they belong to.
- Runs referenced by workflow stages or preview groups.
- The configured minimum number of recent runs for each application.
- Logs from the latest successful release and active runs.

Eligible old logs and unreferenced failed or cancelled deployments can be removed.
Successful snapshots and their service credentials remain retained because Helm
history may still reference them. This controller cleanup does not uninstall Helm
releases or delete runtime resources. Audit events and analytics archives have
independent retention and are not pruned by this action.

## Interrupted work

The controller renews leases for executions it owns, including long image builds
with no progress messages. Expired orphaned deployments become failed with an
explanation. Recovery never replays runtime writes whose outcome is uncertain.
Inspect the running release before deliberately retrying.

At startup, unfinished workflow execution is marked interrupted before pollers or
HTTP requests can accept more work. Successful results and pending approvals are
preserved. Approvals serialize per stage and reject a revision when its source
configuration has changed. Start a new revision to review changed configuration.
The execution controller remains a single active process; this is not active-active
controller failover.

## Backups and restore checks

The default backup directory is `backups` beside the configured master key. Files
are stored in private directories and include a consistent database snapshot, the
vault key, and a checksum manifest. Protect and copy the entire backup directory
to separate storage. Backups contain credentials and are not downloadable through
the API. The overview's owner-only backup summary counts all recorded backups,
even when a project is selected. Its latest backup is ordered by creation time;
its latest verified backup is ordered by successful verification time. The
`GET /api/v1/operations/backups` inventory returns up to 100 records. A record is
metadata, not a fresh check that its files are still present. Paths, backup
contents, and credentials are never returned.

SQLite uses `VACUUM INTO` for a consistent snapshot. PostgreSQL uses `pg_dump` in
custom archive format. The runtime image includes PostgreSQL client tools; their
major version must support the database server being backed up. Database
connection credentials are supplied through process environment variables, never
command arguments or generated logs.

**Verify restore** checks checksums, restores to an isolated temporary database,
applies schema migrations there, reads project/application data, and decrypts
saved global and service credentials using the copied vault key. Only a successful
restore check sets `verifiedAt`. A failed recheck clears that timestamp and keeps
the backup for inspection.

SQLite verification uses a private temporary file. PostgreSQL verification creates
a uniquely named temporary database on the configured database server, restores
the archive, and removes that temporary database afterward. The database account
needs permission to create and drop databases. Verification never replaces the
live database. A server outage during cleanup can leave a `dispatch_restore_*`
test database that an owner must remove after checking its identity.

These backups cover the controller database and vault key. They exclude the
analytics directory, including DuckDB and Parquet history. Back up that directory
separately if historical analytics must survive recovery; see
[historical analytics](analytics.md). External databases, workload volumes,
kubeconfig files stored outside controller state, and other externally supplied
files require separate backup arrangements.

For actual disaster recovery:

1. Stop the controller and preserve the damaged database for investigation.
2. Restore the database and matching master key from the same verified backup.
3. Restore external kubeconfig files, controller configuration, and the separately
   backed-up analytics directory when needed.
4. Start one controller with the restored database, verify access and project
   inventory, then inspect the currently running releases before deploying.

An actual restore is an operator action. The verification API only creates
isolated temporary restores.
