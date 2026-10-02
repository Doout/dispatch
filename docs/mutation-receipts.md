# Retrying public mutations

Send `Idempotency-Key` with these mutations:

- `POST /api/v1/apps/{appId}/deployments`
- `POST /api/v1/infrastructure/servers` after reviewing creation
- `POST /api/v1/infrastructure/servers/{id}/delete` after reviewing deletion
- `POST /api/v1/infrastructure/snapshots/accept` after reviewing capture or deletion
- `POST /api/v1/temporary-environments` after reviewing creation
- `POST /api/v1/temporary-environments/{id}/destroy` after reviewing cleanup
- `POST /api/v1/service-templates/{id}/runs`
- `POST /api/v1/service-provision-runs/{id}/resource/delete` after reviewing deletion
- `POST /api/v1/workload-backups`
- `POST /api/v1/workload-backups/{id}/verify`
- `POST /api/v1/workload-backups/{id}/restore/{destinationRunId}` after reviewing overwrite
- `POST /api/v1/workload-backups/{id}/delete` after reviewing archive deletion

Use 8 to 128 printable ASCII characters
without spaces, generated once for the intended request. An identical retry by
the same stable caller in the same project and action returns the original
operation. Changing its parameters returns `409` before another operation starts.
Different callers and projects have separate key scopes.

```http
POST /api/v1/apps/application-id/deployments
Authorization: Bearer <credential>
Idempotency-Key: release-20261001-001
Content-Type: application/json

{"commitSha":"accepted-immutable-commit"}
```

Keyed requests return a mutation receipt with `operationId`, `operationUrl`,
`state`, `retryUntil`, and `recoveryActions`. `Location` points to
`GET /api/v1/mutation-receipts/{receiptId}`. Poll this receipt while preparation is
in progress; the operation URL becomes inspectable after acceptance commits.
Endpoints with an optional key keep their existing response shapes when the
header is omitted. Temporary-environment creation and destruction require it. Existing
deployment status, logs, events and cancellation APIs continue
to work. Other endpoints do not gain this contract merely by receiving a key.

Authentication and current permissions run before replay. A rotated service
account credential can retrieve its account's original result. A revoked token,
a different account, or a caller without project access cannot retrieve it.
Receipts contain request digests and operation references, never raw credentials,
request bodies or provider responses. Acceptance and replay audit events point to
the original operation ID and record the credential used for that request.

## Acceptance and recovery

Dispatch first reserves a stable operation identity in SQLite or PostgreSQL. It
then creates the existing deployment or provisioning operation and accepts the receipt in one
transaction, before scheduling execution. Concurrent retries share that identity.
If a controller stops during preparation, retrying the same request after the
one-minute preparation lease can complete acceptance with the same operation ID.
A stale preparer cannot commit after another process claims the receipt.

After durable acceptance, receipt replay never schedules execution again. A
controller that loses an accepted execution records `unresolved`; inspect the
original operation and runtime before choosing a recovery action. Failed work
keeps its original receipt. To request another operation, deliberately choose a
new key after assessing the first outcome. Cancelling execution does not imply
that external changes were undone; the receipt reports cancellation separately.

Replay is supported for seven days from the first reservation. The deadline is
returned as `retryUntil` and `Idempotency-Retry-Until`. After that deadline, the same
key returns `409`; it cannot silently create another resource or repeat deletion.
Compact scope, request digest, operation reference and terminal outcome remain as
a permanent tombstone. Deleting workload history does not make the key reusable.
The original receipt can still be inspected subject to current authorization.

Server creation and deletion bind the receipt to the original provisioning
operation. Its provider request identity controls retries and uncertain outcomes.
A verified adoption reconciles the original receipt without creating another
machine. Send the reviewed digest and confirmation name in the request body; the
header replaces the legacy body `requestKey` for these requests.

Snapshot capture and deletion bind receipts to infrastructure operations. Isolated
clones use server creation with both create and restore permissions. Temporary
environment creation binds a receipt to its fixed deployment ID; destruction
binds it to the environment cleanup operation. Lifetime extension uses a
revision-checked update, so inspect the environment after a lost reply before
trying another extension.
