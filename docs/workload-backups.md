# PostgreSQL workload backups

Dispatch can back up an owned PostgreSQL 17 or newer service provisioned on a
Docker target. The Services page has a Backups tab for creating an archive,
verifying it in isolation, reviewing a restore destination, and deleting retained
archive bytes. Project viewers can inspect history. Mutations require
`project.configure` and `deployment.run`.

This first implementation supports native PostgreSQL custom-format dumps on the
same target. Generic volume archives, Helm databases, offsite destinations,
cross-target restore, and other database engines remain follow-up work in #18.
A deployment rollback changes application artifacts; it does not restore data.
[Machine snapshots](machine-snapshots.md) and controller disaster recovery have
separate lifecycles.

## Archive ownership and encryption

A backup captures the accepted service identity, verified owned volume, immutable
container image, native consistency method, creation time, encrypted archive size,
SHA-256 checksums, encryption format, retention policy, and subsequent operation
outcomes. PostgreSQL `pg_dump` supplies a consistent database snapshot while the
source stays available. The archive contains one database; cluster roles and
original ownership or access grants are not restored. Database and role names must be simple identifiers;
connection strings are rejected before Docker execution.

The target streams the dump into an AES-256-GCM archive with authenticated chunks
and an authenticated end marker. The archive limit is 256 GiB. The target encrypts
its ownership manifest and operation receipts too. Receipts bind the exact accepted
action and destination; a corrupt receipt cannot trigger repeated execution. Each backup gets a random key;
the controller stores that key, source credentials and integrity assertions in
its encrypted recovery record. Public responses expose a key identifier, never
the key, SQL assertions or credential values. Remote execution uses the existing
encrypted runtime job and agent receipt protocol.

Local archives reside in `workload-backups` beside the controller's master key.
Agent archives reside in `workload-backups` under its state directory. These
private directories are independent of runtime artifact retention. Application,
preview and service cleanup retain backup bytes. Target registration deletion
and managed server deletion stop while any retained backup remains. Deleting a
backup requires a fresh review and typed confirmation of its backup ID.
Encrypted ownership and operation history remain after archive deletion.

Target-local storage does not protect against loss of that target. Preserve the
controller database, master key, and target archive directory through your
existing offsite recovery process. This release has no managed offsite upload,
key export, or cross-target import API.

## Create and verify

```http
POST /api/v1/workload-backups
Authorization: Bearer <credential>
Idempotency-Key: database-backup-20261002-001
Content-Type: application/json

{
  "sourceRunId": "owned-service-run-id",
  "verificationIntervalHours": 24,
  "checks": [{"query": "SELECT count(*) FROM schema_version", "expected": "1"}]
}
```

`sourceRunId` identifies the owned service provision run, not its connection
registration. An interval of zero disables scheduled verification; one through
8760 hours enables it. The controller checks due policies once a minute and
records each verification through the same durable operation store. Schedules
survive restart. They verify the retained archive; they do not create new backups.
At most 16 single SELECT assertions are accepted. Dispatch executes each in a
read-only transaction and compares its output with the encrypted expected value.

With `Idempotency-Key`, create, verify, restore and delete return the shared
[mutation receipt](mutation-receipts.md). Without it, they return a
`WorkloadBackupOperation`. Poll `GET /api/v1/workload-backup-operations/{id}` or the
receipt's `operationUrl`; list a backup's history at
`GET /api/v1/workload-backups/{id}/operations`.

`POST /api/v1/workload-backups/{id}/verify` authenticates every archive byte before
starting a temporary database. The target creates a separate owned Docker volume
and a PostgreSQL container using the captured image ID, with no external network,
no published ports, a 2 GiB memory limit and one CPU. It restores the archive,
runs the configured checks, and deletes only that operation's owned temporary
container and volume. A successful verification requires successful cleanup.
Verification never connects to the live source database.

## Review an explicit restore destination

Select a ready owned PostgreSQL destination in the same project and on the same
target. The source itself can be selected deliberately. Detach all consumers
first, including saved deployment bindings and workflow references. Dispatch
blocks new bindings while a restore is running or its outcome is unknown.

Request `POST /api/v1/workload-backups/{id}/restore/{destinationRunId}/preview`.
The review binds the backup revision, destination container identity and volume
identity. Send its current version and the exact destination name:

```http
POST /api/v1/workload-backups/backup-id/restore/destination-run-id
Idempotency-Key: reviewed-restore-20261002-001
Content-Type: application/json

{
  "confirmation": {
    "resourceId": "backup-id",
    "action": "restore",
    "expectedVersion": "version-from-preview",
    "confirmName": "destination-name"
  }
}
```

The target authenticates and decrypts the complete archive before opening a
connection to the reviewed destination. Decrypted bytes use an unlinked private
file, so an interrupted process leaves no plaintext archive on disk. It then
rechecks container and volume ownership and runs `pg_restore --clean --if-exists
--single-transaction --exit-on-error`. Objects present in the archive are replaced
in one transaction. Objects absent from the archive remain in the destination.
Configured assertions run after that transaction; a failed assertion does not
undo a committed restore.

Each database command receives transaction and statement timeouts bounded by the
remaining execution deadline, with a safety margin and a maximum of 25 minutes.
Archive preparation therefore reduces the time available for a later restore.
This is why recovery requires PostgreSQL 17 or newer. PostgreSQL documents the
[transaction timeout](https://www.postgresql.org/docs/17/runtime-config-client.html)
and [restore flags](https://www.postgresql.org/docs/17/app-pgrestore.html).
The controller bounds execution at 30 minutes and retains a 31-minute recovery
lease to prevent an early retry from overlapping a database command whose Docker
client disconnected.

## Interrupted work and failures

Public outcomes distinguish archive authentication failures, inaccessible
storage, ownership changes, failed integrity checks and failed cleanup without
returning database output or secrets. A corrupt archive or unavailable key stops
before destination mutation. A completed operation receipt can be replayed after
an agent restart without repeating a restore or dump.

After a lost response, inspect the original operation and its `recoveryAfter`
time. Once its lease expires, call
`POST /api/v1/workload-backups/{id}/operations/{operationId}/reconcile`.
Reconciliation checks the original target's retained evidence. It can remove
only the original verification operation's owned temporary resources. A failed
cleanup keeps further operations blocked until cleanup succeeds.

A restore with no durable completion proof remains `unresolved`; reconciliation
does not repeat it. Inspect the destination before accepting a new reviewed
restore with a new idempotency key. A failed or interrupted restore may have
committed data even when the controller did not receive its result. Confirming
that uncertainty is resolved is an operator decision, not an automatic retry.
