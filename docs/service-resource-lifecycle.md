# Recovering provisioned services

New built-in Docker and Helm ServiceTemplate runs save an encrypted copy of the
accepted template, resolved inputs and generated password before starting work.
The run owns one resource name and one connection-registration ID. Removing the
template or registration leaves that ownership history available.

Open **Services** and expand a provisioned service or a failed run. The owned
resource panel provides inspection, reconciliation and explicit deletion.
Removing a registration only removes its connection entry. Deleting the owned
resource removes its workload and registration after a fresh consequence review.

## Recovery

- **Inspect resource** checks the original target, project, template and provision
  run. It reports whether the resource is ready, unready or absent.
- **Reconcile resource** recovers a ready resource's connection with its original
  credentials. It does not create a missing resource. For interrupted cleanup,
  it continues the original deletion and records its confirmed outcome.
- **Retry original provision** is offered after inspection confirms absence. It
  reuses the accepted configuration, resource name and encrypted credentials.
  Existing retained volumes must carry the same ownership labels. A ready
  resource is reused without another create operation.

If the controller stops during an operation, its mutation reservation remains
protected for up to 31 minutes. The API reports `recoveryAfter`; recovery becomes
available after that deadline. This prevents another controller from starting a
competing mutation while the original one may still run. A remote operation also
requires an inspection taken after its previous agent lease ends. An uncertain operation is
not repeated without that inspection.

Custom scripts and older runs without encrypted recovery records require manual
inspection. They do not gain an automatic retry or adoption path. Do not rerun a
script until its external result is understood. Built-in recovery cannot read or
adopt resources belonging to another project, template, run or target identity.

## Deletion and data

Deletion refuses active application bindings, workflow references, dependent
provisioned services and bindings still used by the latest successful deployment.
Detach and redeploy application consumers first. New bindings cannot be accepted
while deletion is pending. Rollback cannot reactivate a captured binding to a
deleted owned resource. The review lists retained storage and external service
dependencies; it requires the exact resource name and current version.

Docker cleanup removes the verified container ID without removing volumes. Helm
cleanup refuses charts whose volume claims cannot be retained, and disables
uninstall hooks. Volume, PVC and backup deletion are separate operations. The
accepted credentials and ownership history remain encrypted after cleanup so
retained database files do not become unusable through a lost password.

An interrupted deletion can be reconciled when the same owned resource is already
absent. Its original receipt becomes successful. Replaying a completed remote
cleanup does not remove a replacement container.

## API

| Endpoint | Purpose |
| --- | --- |
| `GET /api/v1/service-provision-runs/{id}/resource` | Ownership, lifecycle state, target and recovery deadline |
| `POST /api/v1/service-provision-runs/{id}/resource/inspect` | Read-only inspection of the owned resource |
| `POST /api/v1/service-provision-runs/{id}/resource/reconcile` | Recover the connection or finish the original cleanup |
| `POST /api/v1/service-provision-runs/{id}/resource/retry` | Explicitly retry the accepted built-in provision after inspection |
| `POST /api/v1/service-provision-runs/{id}/resource/delete-preview` | Current consequences, consumers and retained storage |
| `POST /api/v1/service-provision-runs/{id}/resource/delete` | Delete with the shared destructive confirmation body |

Template run creation and owned-resource deletion accept `Idempotency-Key` using
[the public receipt contract](mutation-receipts.md). Authentication and current
project permissions apply before replay. Receipt and resource responses never
contain resolved inputs, credentials or raw provider responses. Existing run and
connection endpoints remain available; no second execution queue is introduced.
