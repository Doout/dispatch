# Enrolled Docker runtime

A Docker server can bind to an enrolled Dispatch agent using `agentNodeId` in
`POST /api/v1/servers`. The binding is immutable and unique. The node uses its
existing persistent enrollment key and short-lived HTTPS session. Legacy bearer
tokens cannot execute runtime jobs. The controller needs encrypted storage and
`DISPATCH_EXECUTOR=docker` for application deployment.

Run the agent on the target with these additional settings:

```sh
DISPATCH_AGENT_RUNTIME=true
DISPATCH_AGENT_RUNTIME_STATE=/var/lib/dispatch-edge/runtime
```

Keep the existing `DISPATCH_EDGE_CONTROLLER_URL`, `DISPATCH_EDGE_NODE_ID`, and
persistent `DISPATCH_EDGE_IDENTITY_FILE` configuration. The initial enrollment
token is single-use. The runtime directory must be private to the agent and
persist across upgrades. It contains the encryption key, operation receipts,
credential redaction history, and retained deployment artifacts. Back up that
directory together with the node identity. Do not copy it to a different target.

The agent needs Git, Docker CLI with Compose, and access to its local Docker
socket. A container installation must explicitly mount the socket and the
persistent state directory. Existing proxy-only agent installations do not gain
Docker access automatically. Runtime work can occupy a node for up to thirty
minutes; use a dedicated node when proxy request latency matters. Routine runtime
operations use outbound HTTPS and require no inbound management port or SSH.

## Operations and evidence

The controller and agent negotiate `dispatch.agent.runtime/v1` and an operation
list on every poll. Dockerfile and Compose deployments use the accepted immutable
Git commit or saved inline definition. The target records immutable image IDs
and encrypted runtime inputs before changing the workload. Inspect, bounded
logs, start, stop, retained rollback, and cleanup use typed requests. There is no
host shell endpoint. Built-in PostgreSQL ServiceTemplates use the same queue,
with project, template, target and provision-run ownership checked before lease
and completion. Generated connection values remain encrypted until the controller
saves the service connection. Other remote service provisioners are rejected.

Storage inventory and reviewed volume deletion use target-scoped typed jobs.
They support retained storage even after the original application is removed.
Ownership and policy are checked at submission, lease and completion; the worker
re-inspects identity and consumers before removal. Existing storage permission
and destructive-review requirements still apply. See [storage](storage.md).

The worker encrypts and fsyncs final health evidence in its running operation
receipt before the readiness gate returns. A candidate cannot be promoted if
that write fails. Completed and interrupted receipts return the captured health
evidence, including when subsequent route publication fails; the controller
saves it against the accepted deployment or rollback policy.

`GET /api/v1/servers/{id}/capabilities` describes the configured runtime driver.
The node's `runtimeVersion` and `runtimeCapabilities` details describe its last
advertisement. A queued unsupported operation fails before its inputs are sent.

For application inspection, logs, start or stop:

```http
POST /api/v1/apps/{id}/runtime/inspect
Authorization: Bearer <user-token>
Idempotency-Key: <unique-request-key>
Content-Type: application/json

{}
```

Use `logs`, `start`, or `stop` in place of `inspect`; logs accept `logLimit` from
1 to 1000 and default to 200. The response is a durable operation receipt and a
`Location` for `GET /api/v1/apps/{id}/runtime/jobs/{jobId}`. Reuse the same key only
for identical inputs. Viewing requires project access; start, stop and recovery
acknowledgment require deployment permission. Deployment, cleanup and rollback
continue to use their existing application actions and reviews.

A target executes one leased job at a time. Requests are limited to 2 MiB and
results to 1 MiB. Credentials are encrypted in the controller queue and removed
when the job finishes or expires, including when a node never reconnects. The
agent renews a 45-second lease every ten seconds and cancels commands on lease or
session loss. Enrollment generation is part of job ownership: a replacement key
cannot receive an old enrollment's inputs.

## Recovery

A completed target receipt returns the same result after a lost completion or
restart. An interrupted mutation, missing receipt on redelivery, or unproven
Docker outcome is `unknown`; the agent does not repeat it. This blocks another
mutation of the application. Read-only inspection remains available.

After inspecting an unknown outcome, acknowledge it with the application name
and the successful inspection receipt:

```http
POST /api/v1/apps/{id}/runtime/jobs/{jobId}/acknowledge
Content-Type: application/json

{"confirmName":"application-name","inspectionId":"inspection-receipt-id"}
```

The inspection must have been created after the unknown result and belong to the
same project, application and target under its current enrollment. The old lease
must have expired. Acknowledgment retains the recorded unknown result and allows
an explicitly requested replacement operation.

Keep retained volumes and the state directory during recovery. Application
cleanup removes owned containers and networks, preserving volumes and rollback
artifacts. If credential redaction history is missing, logs remain unavailable
for the existing workload. Restore the original state directory to recover that
history. Service provisioning refuses pre-existing container or data-volume
identities instead of replacing their credentials.

## Validation

The broker tests exercise encrypted input/output storage, stale leases, target
ownership, cancellation, enrollment replacement, and inspection before recovery.
Agent tests cover durable replay, interrupted execution, missing state, exclusive
worker ownership, and redaction history. API tests cover enrolled sessions,
unsupported versions, legacy-token rejection and revocation.

Run `DISPATCH_RUNTIME_INTEGRATION=1 go test ./internal/agentruntime` to exercise
Compose deployment, process restart, replay, logs, start/stop, retained-image
rollback and cleanup against a disposable Docker daemon. Test resources receive
unique owned names and are removed after the test.
