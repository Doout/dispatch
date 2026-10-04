# On-demand servers

Dispatch can create, inspect, adopt, and delete machines through a registered
`dispatch.provider/v1` adapter. The runnable mock supports the whole workflow
without cloud credentials or billing. A real provider adapter is a separate
integration; the mock does not allocate a VM.

Use the [HTTP workflow](#http-workflow) below or the
[CLI and MCP commands](automation-client.md) to select a project and approved
provider, load its region/size/image/network choices, and reference a stored SSH
key. Only the public SSH key is sent. Provider configuration follows the
advertised JSON Schema; undeclared fields are rejected and write-only fields
require secret references. Inspect the saved review and supply the exact machine
name as `confirmName` to accept it. Accepted inputs, including resolved field secrets, are
frozen in encrypted storage. Credential rotation does not change a pending
request's payload.

Creation tracks allocation, enrollment and runtime readiness separately. A
provider address confirms allocation only. A reviewed cloud-init plan can install
the pinned agent and enroll the allocated node; verified SSH supports installation
and recovery on an existing machine. A target appears only after fresh
authenticated runtime evidence. A bootstrap plan also requires the reviewed agent
artifact. The project and node binding are fixed. A new enrollment token cannot
replace an enrolled identity. See [target installation](target-bootstrap.md).

## HTTP workflow

Project operators and automation accounts need current infrastructure grants,
an assigned provider and an assigned SSH public key. Creation requires available
quota for every caller. Global provider secret references remain owner-only. See
[automation grants](automation-identities.md) and [project quotas](infrastructure-quotas.md).

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/projects/{id}/infrastructure/providers` | Approved provider catalog and schemas; excludes connection and credential references |
| POST | `/api/v1/projects/{id}/infrastructure/providers/{providerId}/options` | Discover a `kind` with `config` and optional `secretRefs` |
| POST | `/api/v1/infrastructure/servers/review` | Save an encrypted, 15-minute creation review |
| POST | `/api/v1/infrastructure/servers` | Accept `reviewId`, `digest`, `confirmName`, and stable `requestKey` |
| GET | `/api/v1/infrastructure/servers` | List allocation, enrollment, and runtime state |
| GET | `/api/v1/infrastructure/servers/{id}` | Refresh this authorized server's readiness and inspect its original operation |
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
key returns the original operation. An `Idempotency-Key` header uses the
[public receipt contract](mutation-receipts.md) and replaces the body `requestKey`.
Acceptance binds that receipt to the operation in the same database transaction.
Operation history remains durable after machine deletion.

## Wait for a usable server

After accepting creation, save both the managed server ID and the mutation
receipt. A successful allocation receipt confirms the provider operation. Use
the server endpoint to check whether the intended node has enrolled and passed
a fresh runtime check:

```sh
dispatchctl server get --server SERVER_ID
dispatchctl server wait --server SERVER_ID --timeout 120
```

`server_get` and `server_wait` are also MCP tools. They require current
`infrastructure.inspect` access and the assigned provider on every request.
Reading status refreshes only that server. It never starts installation or issues
an enrollment token. SSH review and approval remain restricted to the controller
owner.

The response keeps `allocationState`, `enrollmentState` and `runtimeState`
separate. It also includes `waitState`, `deployable`, a small `bootstrap` summary
when applicable and the latest create, restore or delete operation. The summary
excludes installer plans, claim tokens, credentials and provider connection
details. The response uses `Cache-Control: no-store`.

A wait succeeds with `waitState: ready` only when `deployable` is true and the
ordinary workload target matches this server's enrolled identity. A snapshot
clone returns `verified-isolated` with `deployable: false`. That result ends the
wait but does not permit deployment. Pending installation approval also returns
for operator review.

Failure, cancellation, uncertain allocation or installation, expired enrollment,
revoked identity and deletion end a wait with a structured recovery error. A
paused provider operation also ends the wait. Its `waitState: paused` and
`operation_paused` error require inspection and an explicit retry. A stale
runtime check keeps the server waiting. Timeout or Ctrl-C stops the client
without cancelling accepted work. The `continuation` preserves the server,
project and original operation IDs. Resume with the same server ID.

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
