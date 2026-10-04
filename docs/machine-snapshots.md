# Machine snapshots and isolated clones

Machine snapshots retain provider disks independently of the source server. They
complement application backups: the current capture mode is crash-consistent and
does not quiesce databases or establish application integrity.

Use the API workflow below or the
[snapshot CLI commands](automation-client.md#capture-and-inspect-a-machine-snapshot)
to select an allocated source, a boot/all disk set and retention period. Review
the exact disk identities, consistency and encryption before supplying the exact
snapshot name as `confirmName` to accept. The provider
must advertise and have approved snapshot capabilities. Unsupported disk policies
are rejected before allocation. No real VM provider adapter is bundled yet; the
public mock exercises the contract without creating real VMs or disks.

The API workflow is:

1. `POST /api/v1/infrastructure/servers/{id}/snapshot-review` with `name`, `diskSet`,
   `consistency: "crash-consistent"`, `encryption: {"mode":"provider-managed"}` and
   optional `retainUntil` (defaults to seven days).
2. `POST /api/v1/infrastructure/snapshots/accept` with the returned `reviewId`,
   `digest`, exact `confirmName`, and a stable `Idempotency-Key` header. Without a
   header, supply a stable `requestKey` in the body.
3. Inspect the returned receipt and the source server's `/operations`. List
   retained records with `GET /api/v1/projects/{id}/infrastructure/snapshots`.

Capture needs `infrastructure.snapshot`; inspection needs `infrastructure.inspect`.
The provider must be assigned to the project. Acceptance reserves `maxSnapshots`
capacity in the same transaction as the owned record and operation. The default
limit is zero; an owner must configure a policy. `-1` explicitly means unlimited.
Pending, retained, failed and unresolved snapshots count until verified deletion
or cancellation before submission. Source and clone servers count separately
against `maxServers`.

The controller saves the reviewed request before provider I/O and replays only
that request and operation key. Cancellation after submission may have reached
the provider and does not release quota or submit another allocation. Use
`POST /api/v1/infrastructure/snapshots/{id}/resolve` with the provider `resourceId`,
current record `revision`, and exact `confirmName` to inspect uncertain outcomes.
The original ownership labels and disk evidence must match. A 404 alone cannot
clear a lost allocation; releasing capacity requires a terminal original provider
operation and verified absence. Expired worker leases cannot change the result.

## Isolated restore

Request an isolated clone with `sourceSnapshotId`, the source project and provider
on `POST /api/v1/infrastructure/servers/review`. Select a compatible image and a
network from the provider's `restore-networks` catalog. A fresh cloud-init
bootstrap is required and pins the agent artifact before acceptance. Accept the
saved review through `POST /api/v1/infrastructure/servers`. Both `infrastructure.create` and
`infrastructure.restore` permissions are required, including receipt replay.

The provider must sanitize the copied disk **before** its old agent, copied
workloads or network can start. It must clear the source agent identity and
private runtime journal, generate new machine and SSH identities, disable copied
workloads and production bindings, and keep the clone quarantined. The controller
checks distinct identities, independent disk mappings, matching content digests,
size and encryption. A provider that cannot establish these guarantees does not
support restore. Cloud-init running after the copied agent has started is unsafe.

A clone receives a new managed server ID, node ID and bootstrap claim. Provider
allocation alone does not establish bootability. Fresh enrollment, authenticated
runtime evidence and the reviewed running agent SHA-256 are required. The final
runtime state is `verified-isolated`; the controller does not publish an ordinary
workload target or copy application/service bindings. Application integrity remains
unverified. In-place restore, memory capture and cross-provider restore are outside
this contract.

Provider evidence and mocked enrollment tests do not prove a real VM booted or
that a provider actually sanitizes copied disks. A real adapter must pass the
snapshot conformance suite and a live restore drill before those guarantees can
be claimed. Unsafe or inconsistent provider evidence remains unresolved and
retains quota for operator inspection.

## Inspect and promote a clone

`GET /api/v1/infrastructure/servers/{id}/clone` checks current provider ownership,
fresh clone identities and the reviewed disk evidence. It returns the server's
readiness, public provider resource, `verified` evidence result and
`applicationIntegrity: unverified`. Inspection requires `infrastructure.inspect`
and the project's provider assignment. It does not release quarantine or copy
application/service bindings.

An adapter may optionally support `server.promote`. Use
`POST /api/v1/infrastructure/servers/{id}/promote` with an explicit workload
`network`, current managed server `revision`, exact clone name as `confirmName`,
and a stable `Idempotency-Key` header. Both `infrastructure.modify` and
`infrastructure.restore` permissions are required. The clone must still be
isolated, running, freshly enrolled and ready in isolation. The selected network
must be advertised outside quarantine. The saved request binds its original
machine, disk and enrollment identities.

Completion requires matching provider evidence that quarantine was released on
that same clone, copied workloads remain disabled and production bindings remain
cleared. Changed enrollment credentials prevent publication. A fresh authenticated
runtime heartbeat after promotion is required before `deployable` becomes true.
Clone ancestry remains recorded and application integrity remains unverified;
promotion does not restore or deploy an application. Uncertain outcomes retain
the original operation for [explicit recovery](on-demand-servers.md#power-operations).

The bundled mock exercises inspection and promotion without a real VM. Existing
v1 providers need not implement these optional capabilities. Live sanitation,
network release and workload acceptance still require a real adapter restore drill.

## Retention and cleanup

Snapshot deletion is a separate reviewed operation:
`POST /api/v1/infrastructure/snapshots/{id}/delete-review`, followed by the shared
snapshot acceptance endpoint. It requires `infrastructure.delete`, elapsed
retention, and no unfinished clone depending on the snapshot. The controller
verifies the original ownership and confirms provider absence before releasing
quota. Deleting a source server or a completed independent clone preserves its
snapshot; deleting a snapshot preserves materialized clone disks. Existing server
storage protections still govern machine deletion.

To clean up a verified isolated clone, use the normal server deletion review and
acceptance. Its fresh agent enrollment is revoked after verified provider deletion.
Snapshot records, capture reviews and source ownership tombstones remain available
for reconciliation and clone ancestry. This feature does not automatically expire
provider snapshots; retention establishes the earliest permitted deletion time.
