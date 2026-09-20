# Controller operations

The Operations page brings together the controller audit trail, application owners,
project retention, backups, and identity team mappings. Project visibility applies
to audit results. Controller backups and identity mappings require controller owner
access. History cleanup requires project admin access.

## Activity and ownership

Authenticated mutations record the actor, impersonator when present, route action,
resource identity, outcome, and time. Request bodies, query strings, credential
values, manifests, and provider responses are not recorded. Filter `/api/v1/audit`
by `projectId`, `appId`, `actorId`, `action`, and the `before` cursor. Each response
contains up to 100 events. Events start when this release is installed; historical
actor identities are not inferred.

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
application. Saving a policy does not schedule cleanup. **Save and preview
cleanup** reports the eligible counts. Applying it requires explicit confirmation.

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
the API.

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

Backups cover controller state and its vault. External databases, workload volumes,
kubeconfig files stored outside controller state, and other externally supplied
files require separate backup arrangements.

For actual disaster recovery:

1. Stop the controller and preserve the damaged database for investigation.
2. Restore the database and matching master key from the same verified backup.
3. Restore external kubeconfig files and controller configuration separately.
4. Start one controller with the restored database, verify access and project
   inventory, then inspect the currently running releases before deploying.

An actual restore is an operator action. The verification API only creates
isolated temporary restores.
