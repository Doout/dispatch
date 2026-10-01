# On-demand servers

Dispatch can create, inspect, adopt, and delete machines through a registered
`dispatch.provider/v1` adapter. The runnable mock supports the whole workflow
without cloud credentials or billing. A real provider adapter is a separate
integration; the mock does not allocate a VM.

In **Servers → On-demand servers**, select a project and approved provider,
load the provider's region/size/image/network choices, and choose a stored SSH
key. Only the public SSH key is sent. Provider configuration follows the
advertised JSON Schema; undeclared fields are rejected and write-only fields
require secret references. Review the rendered selection and type the machine
name to accept it. Accepted inputs, including resolved field secrets, are
frozen in encrypted storage. Credential rotation does not change a pending
request's payload.

Creation has three independent states: allocation, enrollment, and runtime.
A provider address confirms allocation only. Issue a one-use enrollment token,
install the runtime agent on that machine, and enroll the allocated node ID.
The ordinary deployment target appears only after that enrolled node advertises
the typed deploy runtime with fresh runtime evidence. The project and node
binding are fixed. Issuing an enrollment token cannot replace an already
currently enrolled identity. Pinned cloud-init/bootstrap and verified SSH
recovery are handled by the bootstrap integration.

## HTTP workflow

All endpoints below currently require a controller owner. The manager exposes
project/provider permission hooks and the acceptance store exposes transactional
admission hooks for scoped automation grants and quotas.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/projects/{id}/infrastructure/providers` | Approved provider catalog and schemas; excludes connection and credential references |
| POST | `/api/v1/projects/{id}/infrastructure/providers/{providerId}/options` | Discover a `kind` with `config` and optional `secretRefs` |
| POST | `/api/v1/infrastructure/servers/review` | Save an encrypted, 15-minute creation review |
| POST | `/api/v1/infrastructure/servers` | Accept `reviewId`, `digest`, `confirmName`, and stable `requestKey` |
| GET | `/api/v1/infrastructure/servers` | List allocation, enrollment, and runtime state |
| GET | `/api/v1/infrastructure/servers/{id}/operations` | Durable operation history and recovery state |
| POST | `/api/v1/infrastructure/servers/{id}/enrollment` | Issue a node-scoped 15-minute one-use token |
| POST | `/api/v1/infrastructure/operations/{id}/retry` | Resume a paused transport failure using the original request identity |
| POST | `/api/v1/infrastructure/operations/{id}/cancel` | Stop a creation request without resubmitting it |
| POST | `/api/v1/infrastructure/servers/{id}/adopt` | Inspect `resourceId`, verify original ownership, and accept `revision` plus `confirmName` |
| POST | `/api/v1/infrastructure/servers/{id}/delete-review` | Refresh storage evidence and review permanent removal |
| POST | `/api/v1/infrastructure/servers/{id}/delete` | Accept deletion with `digest`, `confirmName`, and stable `requestKey` |

Creation input includes `projectId`, `providerId`, `name`, `region`, `size`,
`image`, `network`, `sshKeySecretId`, `config`, and optional `secretRefs`.
A second key cannot consume the same review. Repeated acceptance with the same
key returns the original operation. Shared public mutation receipts can provide
a preallocated operation identity; it is bound to acceptance in the same database
transaction. Operation history remains durable after machine deletion.

## Recovery and deletion

The controller checkpoints the intent before contacting the provider and the
provider operation ID before polling. A process or adapter restart reuses the
same encrypted request and provider idempotency key. Transient failures back off
and pause after eight attempts; the operation deadline is 30 minutes. A fresh
manual retry does not extend the deadline or change the request.

Cancellation or expiry before submission cannot allocate a machine. After
submission may have started, cancellation preserves an unknown outcome and
never replays creation. Inspect the provider, then adopt only a ready resource
with the exact registration, project, server, and original request labels. A
foreign resource, changed operation/resource identity, unverified provider
manifest, or contradictory state stops reconciliation. Adoption waits for the
previous lease to expire. Unknown outcomes retain ownership and quota reservations.

Deletion requires explicit name confirmation and a current review. Applications,
services, outstanding runtime work, and any machine-local or unclassified
storage block deletion. Fresh runtime storage inspection runs before acceptance.
Database guards prevent new applications, services, or runtime jobs from being
admitted once deletion starts, and prevent ordinary server editing/removal from
bypassing provider ownership. An independent storage classification must mean
that the data survives machine deletion; a volume inside the machine is not
independent. Successful deletion requires provider-confirmed absence and revokes
the node before removing its deployment-target registration.

## Validation

Focused tests cover controller and mock restarts after a lost mutation response,
concurrent/repeated acceptance, frozen secret references, cancelled and expired
requests, inconsistent provider evidence, foreign-resource adoption, readiness
gating, admission rollback, and protected deletion on SQLite and PostgreSQL.

`DISPATCH_RUNTIME_INTEGRATION=1 go test ./internal/api -run
TestInfrastructureManagedRuntimeIntegration` creates a mock machine, enrolls its
node, publishes the target through runtime readiness, deploys a real Docker
workload through the authenticated typed agent API, replays its durable receipt
after an agent restart, reads logs, and removes the owned workload. It requires a
local Docker daemon and uses isolated labelled test resources.
