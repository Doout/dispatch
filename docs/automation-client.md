# DevOps and agent client

`dispatchctl` is a thin API client and stdio MCP server. It returns the same reviewed operations and durable receipts as the browser and REST API. It cannot approve a human gate or override a project grant, provider assignment, quota, source trust decision or data protection rule.

Build with `go build -o dispatchctl ./cmd/dispatchctl`. Linux release archives include the client. Run `dispatchctl --help` for its supported commands. The underscore form, such as `deployment_preview`, is also accepted and matches the MCP tool name.

An executable integration test exercises the CLI and MCP against an isolated
controller using simulated deployment and an owned service fixture. It verifies
request replay, current grants and cross-project denials. It does not establish
support for a real infrastructure provider.

## Credentials

Have a controller owner create a service account with only the required project grants. Inspection needs `project.view`; deployment needs `deployment.run`. Infrastructure uses its separate inspect, create and delete grants and assigned providers and SSH public keys. An owner credential is not needed for ordinary automation. See [automation identities](automation-identities.md).

Save the issued token in a mode 0600 file. Keep the file out of Git and shell history. Configure only its path and the controller origin:

```sh
export DISPATCH_URL=https://dispatch.example.com
export DISPATCH_TOKEN_FILE=/run/secrets/dispatch-token
```

The client accepts HTTPS, with HTTP allowed for loopback development. Redirects are not followed. It never prints the authentication header and redacts an echoed token from responses. Each response is limited to 4 MiB; request files and MCP messages are limited to 1 MiB. The default HTTP timeout is 30 seconds; use the global `--request-timeout 60s` option before a command when needed.

## Inspect and deploy

Use an existing application whose target and credentials were assigned by its owner:

```sh
dispatchctl projects list
dispatchctl apps list --project PROJECT_ID
dispatchctl deployment preview --app APP_ID --revision FULL_COMMIT_SHA > preview.json
```

Read `preview.json`, including checks and the exact source revision. A preview does not approve a protected source or a pending human gate. Supply its review unchanged after the required approval:

```sh
jq '{commitSha: .data.revision, review: .data.review}' preview.json > deployment.json
dispatchctl deployment start --app APP_ID --key ci-release-2026-001 --input deployment.json > accepted.json
jq '.data | {id, operationId, state, recoveryActions}' accepted.json
```

Save the request file, application ID and key until its outcome is resolved. If the reply is lost, repeat that exact command and key. Do not create another key merely because a network request timed out. A changed request with the same key is rejected by the server.

```sh
dispatchctl receipt wait --receipt RECEIPT_ID --timeout 120
dispatchctl deployment get --deployment DEPLOYMENT_ID
dispatchctl deployment logs --deployment DEPLOYMENT_ID --limit 100 --after 0
dispatchctl deployment diagnose --deployment DEPLOYMENT_ID
```

A wait timeout or Ctrl-C stops the client. It does not cancel accepted work. The result includes a `continuation` with the receipt or deployment ID. Resume with that ID. Log entry IDs are continuation cursors; pass the last received ID as `--after` for the next bounded page.

## Define and configure an application

After a target has enrolled and become ready, an account with `project.configure`
can create an application on that project's assigned target. Write `app.json`
with explicit project, target and source settings:

```json
{
  "projectId": "PROJECT_ID",
  "serverId": "SERVER_ID",
  "name": "example-api",
  "sourceRepo": "https://github.com/example/api.git",
  "buildType": "dockerfile",
  "branch": "main",
  "containerPort": 8080
}
```

The client also accepts inline Compose definitions and Helm chart configuration.
It excludes global source credentials and deployment hooks. Private-source
credentials must be configured through the approved owner workflow or inherited
from an existing template.

```sh
dispatchctl app create --key app-definition-001 --input app.json
dispatchctl receipt wait --receipt RECEIPT_ID --timeout 120
dispatchctl app get --app APP_ID
dispatchctl app sync --app APP_ID
```

Keep the creation file and key. Repeating the exact request returns its original
receipt and application. Creating an application does not deploy it.

Service mappings, Helm overrides and health policy have separate inspect and
replace commands:

```sh
dispatchctl app bindings get --app APP_ID
dispatchctl app bindings set --app APP_ID --input bindings.json
dispatchctl app helm values get --app APP_ID
dispatchctl app helm values set --app APP_ID --input helm-values.json
dispatchctl app health get --app APP_ID
dispatchctl app health set --app APP_ID --input health-policy.json
```

`bindings.json` is the complete service-binding array. `helm-values.json` contains
an `overrides` object. `health-policy.json` contains the explicit health policy.
Use `[]` or `{"overrides":{}}` to clear the corresponding setting. These commands
require an idle editable application and never trigger deployment. They have no
mutation receipt or retry key. Inspect the current configuration after a lost
response before making another change. Repository-managed configuration must be
edited in its source.

## Cancel or roll back a deployment

```sh
dispatchctl deployment cancel --deployment DEPLOYMENT_ID
dispatchctl deployment get --deployment DEPLOYMENT_ID
dispatchctl app deployments --app APP_ID
dispatchctl deployment rollback review --deployment RETAINED_DEPLOYMENT_ID
```

Cancellation requires `deployment.cancel`. It requests a stop and does not
promise that accepted external changes were undone. The client preserves the
original deployment ID and does not automatically retry cancellation.

Review rollback availability, retained artifacts, current release and its digest.
Write `rollback.json` with the exact review evidence and explicit acknowledgment
that the rollback does not revert database migrations:

```json
{
  "confirmDeploymentId": "RETAINED_DEPLOYMENT_ID",
  "expectedCurrentDeploymentId": "CURRENT_DEPLOYMENT_ID",
  "expectedReviewDigest": "RETURNED_REVIEW_DIGEST",
  "confirmDatabaseNotReverted": true
}
```

```sh
dispatchctl deployment rollback --deployment RETAINED_DEPLOYMENT_ID --key rollback-release-001 --input rollback.json
dispatchctl receipt wait --receipt RECEIPT_ID --timeout 120
```

Rollback requires `deployment.run`, retained supported artifacts and unchanged
review evidence. Preserve the exact file and key across response loss. The client
returns the receipt and the newly accepted rollback deployment ID.

## Review, create and remove a server

The owner must register a provider, assign it and an SSH public key to the project, and set an allocation policy. Verify those prerequisites with `quota get --project PROJECT_ID`.

Discover the assigned providers and their supported choices before preparing a review:

```sh
dispatchctl providers list --project PROJECT_ID
printf '%s\n' '{"kind":"sizes","config":{}}' | dispatchctl provider options --project PROJECT_ID --provider PROVIDER_ID --input -
```

Use `regions`, `images` and `networks` for the other option kinds. The provider manifest describes public configuration fields. Scoped clients cannot supply owner-only provider secret references.

Prepare `server.json` using provider-supported choices:

```json
{
  "projectId": "PROJECT_ID",
  "providerId": "PROVIDER_ID",
  "name": "review-environment",
  "region": "mock-region",
  "size": "mock-small",
  "image": "mock-linux",
  "network": "mock-private",
  "sshKeySecretId": "ASSIGNED_SSH_KEY_ID",
  "config": {}
}
```

Use the public mock provider from [the provider API documentation](provider-api.md) for an allocation rehearsal without cloud credentials. Its server record is simulated and is not a real VM. Supported providers can request reviewed cloud-init through an optional `bootstrap` object with `method`, `platform`, `imageFamily` and `installRuntime`. The controller supplies and pins the installer artifact and identity.

```sh
dispatchctl server review --input server.json > server-review.json
```

Read the review before writing `accept-server.json` with its `reviewId`, `digest` and exact `confirmName`. The client does not fill in those confirmation fields.

```sh
dispatchctl server create --key create-environment-001 --input accept-server.json
dispatchctl receipt wait --receipt RECEIPT_ID --timeout 120
dispatchctl servers list --project PROJECT_ID
dispatchctl server operations --server SERVER_ID
```

Allocation success is separate from agent enrollment and workload readiness. Inspect the server states before deploying. A partial allocation can still consume quota. Follow the original operation's recovery state; do not create a replacement to hide an unresolved outcome.

After detaching workloads and addressing retained data, request a deletion review:

```sh
dispatchctl server delete review --server SERVER_ID > delete-review.json
```

Read the consequences and resolve any protected storage. Supply the reviewed `digest` and exact `confirmName` in `delete-server.json`, then:

```sh
dispatchctl server delete --server SERVER_ID --key delete-environment-001 --input delete-server.json
dispatchctl receipt wait --receipt DELETE_RECEIPT_ID --timeout 120
```

## Recover server allocation and enrollment

Inspect the original server's operations before choosing a recovery action:

```sh
dispatchctl server operations --server SERVER_ID
dispatchctl server retry --server SERVER_ID --operation OPERATION_ID
dispatchctl server cancel --server SERVER_ID --operation OPERATION_ID
```

Retry and cancel require the operation's current infrastructure permission.
The client verifies that the selected operation belongs to the selected server.
Retry keeps the original provider request and deadline. Cancellation after
submission may leave an unknown allocation that still consumes quota. Neither
command automatically repeats a request after response loss. Inspect the same
server and operation again.

When provider inspection finds the originally reviewed machine, write
`adopt.json` with its exact provider resource ID, current Dispatch server revision
and reviewed name:

```json
{
  "resourceId": "ORIGINAL_PROVIDER_RESOURCE_ID",
  "revision": 3,
  "confirmName": "EXACT_REVIEWED_SERVER_NAME"
}
```

```sh
dispatchctl server adopt --server SERVER_ID --input adopt.json
dispatchctl servers list --project PROJECT_ID
```

Adoption requires `infrastructure.modify` and verified original ownership.
Active leases and foreign resources remain blocking. A restored clone also
requires `infrastructure.restore`. If the reply is lost, inspect the current
server before submitting another adoption.

An allocated machine without an approved bootstrap can request its first
enrollment credential explicitly:

```sh
dispatchctl server enrollment --server SERVER_ID > enrollment.json
```

Create the destination file privately, for example with `umask 077`, before
issuing this command. Its response contains a short-lived single-use token for
the target installer, distinct from the API credential. This action requires
`infrastructure.modify`; it cannot replace an enrolled identity. For an approved
bootstrap, recover the original installation through its owner workflow instead.
The client never retries issuance automatically. After a lost reply, inspect the
server's enrollment state before deliberately issuing a replacement unused token.

## Capture and inspect a machine snapshot

The assigned provider must advertise snapshot support and the project must permit capture and have available snapshot capacity. Prepare `snapshot.json` with explicit capture policy:

```json
{
  "name": "before-upgrade",
  "diskSet": "all",
  "consistency": "crash-consistent",
  "encryption": { "mode": "provider-managed" }
}
```

An optional `retainUntil` is an RFC3339 timestamp within the next year; omission retains the snapshot for seven days. A machine snapshot does not establish database consistency. Read the resolved disk identities and retention in the review:

```sh
dispatchctl snapshot review --server SERVER_ID --input snapshot.json > snapshot-review.json
```

Supply that review's `reviewId`, `digest` and exact `confirmName` in `accept-snapshot.json`. Use a distinct stable key for this capture:

```sh
dispatchctl snapshot accept --key snapshot-before-upgrade-001 --input accept-snapshot.json
dispatchctl receipt wait --receipt CAPTURE_RECEIPT_ID --timeout 120
dispatchctl snapshots list --project PROJECT_ID
dispatchctl snapshot get --snapshot SNAPSHOT_ID
```

To restore, add `sourceSnapshotId` to the server review request, use the provider's isolated restore network and supply a fresh `bootstrap` plan. Submit it through `server review` and `server create` with a new key. The server requires both create and restore grants. A verified clone stays isolated and does not become an ordinary deployment target. See [machine snapshots](machine-snapshots.md) for the identity checks and current provider limitations.

After retention expires and all restore operations settle, `snapshot delete review --snapshot SNAPSHOT_ID` returns the deletion consequences. Supply that review's confirmation fields to `snapshot accept` with its own deletion key. A protected or uncertain snapshot stays owned and continues to consume quota; inspect the original receipt and snapshot before retrying.

## Create and clean up a temporary environment

The project needs a finite lifetime policy, available environment quota and an
assigned ready outbound Docker target. The initial path accepts Dockerfile
Application templates. Inspection requires `project.view`; creation and extension
also require `project.configure` and `deployment.run`. Cleanup additionally needs
`deployment.cancel`.

```sh
dispatchctl environment options --project PROJECT_ID
dispatchctl environments list --project PROJECT_ID
```

Choose a listed template and target. Write `environment.json` with `projectId`,
`templateId`, `serverId`, a lowercase `name`, the full `sourceSha`, and finite
`lifetimeSeconds`. For example, use `3600` for one hour.

```sh
dispatchctl environment review --input environment.json > environment-review.json
```

Read the clone settings and omissions in the review. Production routes, service
bindings and hooks are not copied. Supply its `reviewId`, `digest` and exact
`confirmName` in `accept-environment.json`:

```sh
dispatchctl environment create --key agent-environment-001 --input accept-environment.json
dispatchctl receipt wait --receipt CREATE_RECEIPT_ID --timeout 120
dispatchctl environment get --environment ENVIRONMENT_ID
```

The environment records its deployment ID and expiration. Use the deployment
commands above for logs, diagnosis and completion. Reuse the same creation file
and key if the response is lost.

To extend the lifetime, inspect the environment and write `extension.json` with
its current numeric `revision` and an explicit RFC3339 `expiresAt`. Then run
`environment extend --environment ENVIRONMENT_ID --input extension.json`. The
server checks the maximum total lifetime. If the response is lost, inspect the
environment before trying another edit. This command returns the environment,
not a mutation receipt.

Expiry starts cleanup under the accepted policy. To request it earlier:

```sh
dispatchctl environment cleanup review --environment ENVIRONMENT_ID > cleanup-review.json
```

Read the owned and retained resource list. Supply the reviewed `revision`,
`digest` and exact `confirmName` in `cleanup.json`:

```sh
dispatchctl environment destroy --environment ENVIRONMENT_ID --key cleanup-environment-001 --input cleanup.json
dispatchctl receipt wait --receipt CLEANUP_RECEIPT_ID --timeout 120
dispatchctl environment get --environment ENVIRONMENT_ID
```

Preserve the cleanup request and key across retries. Unknown runtime outcomes
block cleanup until inspected. The shared server, named volumes, backups and
execution history remain. The client exposes no arbitrary command execution,
secret administration or human-approval tool.

## Provision an approved service

An owner must approve the exact built-in service template digest, assign its
target and enable service quota for the project. Automation needs `project.view`
and `service.provision`. Custom scripts and production-data copy remain outside
this path.

```sh
dispatchctl service templates list --project PROJECT_ID
dispatchctl service template get --template TEMPLATE_ID
```

Inspect the template's inputs and approved target before writing `service.json`:

```json
{
  "name": "review-database",
  "description": "Database for the review environment",
  "inputs": {}
}
```

Supply the exact inputs required by the chosen template. Then:

```sh
dispatchctl service provision --template TEMPLATE_ID --key provision-database-001 --input service.json
dispatchctl receipt wait --receipt RECEIPT_ID --timeout 120
dispatchctl service run get --run SERVICE_RUN_ID
dispatchctl service get --run SERVICE_RUN_ID
```

Keep the template ID, request file and key until the original run is resolved.
An identical retry returns its original receipt instead of another service.
A changed template digest needs a fresh owner approval before new provisioning.
Recovery and deletion use the owned service resource, its original credentials
and the separate permissions below.

## Recover an owned service

Use the service provision run ID, which identifies the owned workload even if
its connection registration or template has been removed:

```sh
dispatchctl service get --run SERVICE_RUN_ID
dispatchctl service inspect --run SERVICE_RUN_ID
```

Inspection requires `project.configure`. Recovery also requires `deployment.run`.
After inspecting the resource and its `recoveryAfter` deadline, choose the action
that matches the recorded outcome:

```sh
dispatchctl service reconcile --run SERVICE_RUN_ID
dispatchctl service get --run SERVICE_RUN_ID
```

Reconcile recovers the original binding or resumes its original cleanup. A
confirmed-absent built-in resource can be recreated with `service retry --run
SERVICE_RUN_ID`, using its accepted credentials and ownership. These commands
have no idempotency-key API. If a reply is lost, inspect the same run before
submitting another request. Their continuation identifies the service resource
and original operation, not a new mutation receipt.

For deletion, obtain a current review:

```sh
dispatchctl service delete review --run SERVICE_RUN_ID > service-delete-review.json
```

Read the consumers, retained storage and blockers. Write `delete-service.json`
with the returned resource ID and version, and type the exact reviewed name:

```json
{
  "confirmation": {
    "resourceId": "SERVICE_RUN_ID",
    "action": "delete",
    "expectedVersion": "RETURNED_VERSION",
    "confirmName": "EXACT_SERVICE_NAME"
  }
}
```

```sh
dispatchctl service delete --run SERVICE_RUN_ID --key service-cleanup-001 --input delete-service.json
dispatchctl receipt get --receipt RECEIPT_ID
dispatchctl service get --run SERVICE_RUN_ID
```

Keep the same request and key across retries. Protected storage, credentials and
history remain. Custom scripts without an owned recovery adapter remain manual.

## Review runtime artifact retention

These commands require `runtime.cleanup` or `project.manage` and Operations
enabled. An automation account with `runtime.cleanup` can inspect and apply the
owner's saved runtime policy. It cannot change policy or delete history. Read the
saved policy and prepare a runtime review without changing it:

```sh
dispatchctl retention policy --project PROJECT_ID > policy.json
jq '{scope: "runtime", expectedPolicy: .data}' policy.json > runtime-review-input.json
dispatchctl retention review --project PROJECT_ID --input runtime-review-input.json > runtime-review.json
```

Inspect `.data.runtime`, including its exact candidates, protection reasons,
expiration and policy. Write `runtime-apply.json` with `scope: "runtime"`, the
unchanged complete `expectedPolicy`, `runtimeReviewId` from `.data.runtime.id`,
`runtimeReviewDigest` from `.data.runtime.digest`, and `confirm` explicitly set
to the selected project ID.
The client requires every saved policy field, including zero-valued runtime
ages. It never substitutes the current policy or invents confirmation.

```sh
dispatchctl retention apply --project PROJECT_ID --input runtime-apply.json
dispatchctl retention get --project PROJECT_ID --review REVIEW_ID
```

The review ID and digest are the server's retry identity; this API has no
`Idempotency-Key`. After a timeout, inspect the original review. An accepted
partial review can be retried with its unchanged request, within its original
candidate set. Follow a `supersededBy` reference instead of resuming an older
review. Storage and workload backups are excluded. History cleanup and retention
policy edits remain outside these commands.

## Schedule workload backups

Scheduled policies create fresh native PostgreSQL backups on an owned Docker
service. They use the original actor's current grants and assigned target.
Archives stay on that target, so these policies do not provide offsite recovery.

Write `capture-policy.json` with an explicit interval, retained verified count and
the exact policy name to acknowledge removal of older verified policy archives:

```json
{
  "name": "daily-postgres",
  "sourceRunId": "SERVICE_RUN_ID",
  "intervalHours": 24,
  "keepLast": 7,
  "confirmRetention": "daily-postgres",
  "checks": [{ "query": "SELECT 1", "expected": "1" }]
}
```

```sh
dispatchctl backup policy create --key daily-postgres-policy-001 --input capture-policy.json
dispatchctl receipt wait --receipt RECEIPT_ID --timeout 120
dispatchctl backup policies list --project PROJECT_ID
dispatchctl backup policy get --policy POLICY_ID
```

Creation needs `project.view`, `project.configure` and `deployment.run`, an
assigned target and verified owned source storage. Keep the original file and key
across a lost response. Inspect capture and verification timestamps, missed
captures, blockers and the latest verified archive before relying on the policy.
Failed capture or verification must not replace the last usable backup.

Pause or resume with the current revision, an explicit `enabled` value and the
exact policy name. For example, `pause-policy.json` contains:

```json
{"revision": 3, "enabled": false, "confirmName": "daily-postgres"}
```

```sh
dispatchctl backup policy set --policy POLICY_ID --input pause-policy.json
dispatchctl backup policy get --policy POLICY_ID
```

Pause and resume have no mutation receipt. Inspect the policy after response loss
before requesting another change. Resume rechecks the original actor and frozen
target. Source, cadence and retention cannot be changed through this command.

The policy keeps the credential identity that accepted it. Rotating or revoking
that credential stops unattended work. A fresh credential can pause the old
policy, then create a new explicitly reviewed policy with a new key. Resuming the
old policy does not transfer it to the fresh credential. Existing archives remain
owned by the old policy.

## Capture, verify and restore a workload backup

The current native adapter supports owned PostgreSQL 17+ services on Docker.
Archives stay encrypted on their original target and are retained by default. This is
separate from machine snapshots and controller database backups. Mutations
require `project.configure` and `deployment.run` in the owning project.

Prepare `backup.json` with the source service provision run ID:

```json
{
  "sourceRunId": "SERVICE_RUN_ID",
  "verificationIntervalHours": 24,
  "checks": [{ "query": "SELECT count(*) FROM expected_table", "expected": "10" }]
}
```

The interval is `0` for manual verification or `1` to `8760` hours. Integrity
checks are optional; at most 16 queries and expected values of 4096 bytes each
are accepted. The server validates read-only SQL and keeps assertions encrypted.

```sh
dispatchctl backup create --key database-backup-001 --input backup.json
dispatchctl receipt get --receipt RECEIPT_ID
dispatchctl backups list --project PROJECT_ID
dispatchctl backup get --backup BACKUP_ID
dispatchctl backup verify --backup BACKUP_ID --key database-verify-001
dispatchctl backup operations --backup BACKUP_ID
dispatchctl backup operation get --operation OPERATION_ID
```

Verification restores into isolated temporary storage and requires integrity
checks and owned-resource cleanup. Inspect verification freshness and cleanup
state; a retained archive alone does not establish a successful restore.

After an interrupted operation's `recoveryAfter` deadline:

```sh
dispatchctl backup reconcile --backup BACKUP_ID --operation ORIGINAL_OPERATION_ID
```

Reconciliation inspects the original evidence and cleans owned interrupted
verification resources. It does not repeat a restore whose outcome is unknown.
It has no idempotency-key API and returns the original backup operation. Inspect
that operation again if the response is lost.

To restore, explicitly select a ready owned destination service on the same
project and original target. Resolve its active consumers first:

```sh
dispatchctl backup restore review --backup BACKUP_ID --destination DESTINATION_SERVICE_RUN_ID > restore-review.json
```

Read the overwrite consequences. Write `restore.json` using the same
`confirmation` object shown for service deletion, with `resourceId` set to the
backup ID, `action` set to `restore`, `expectedVersion` copied from the review's
`version`, and the exact destination service name as `confirmName`:

```sh
dispatchctl backup restore --backup BACKUP_ID --destination DESTINATION_SERVICE_RUN_ID --key database-restore-001 --input restore.json
```

Save the destination, input file and key. If the response is lost, recover the
same receipt and inspect its operation; a new key would request another restore.
An unresolved result requires inspection before any new reviewed restore.

To remove retained archive bytes, run `backup delete review --backup BACKUP_ID`.
After reviewing blockers, supply its version and backup ID confirmation in
`delete-backup.json`, with `action: "delete"`, then run:

```sh
dispatchctl backup delete --backup BACKUP_ID --key database-archive-delete-001 --input delete-backup.json
```

The server retains encrypted operation history. An unsupported provider,
protected target, changed review or active lease remains an explicit error;
these commands provide no force, approval or arbitrary execution option.

## MCP

Launch `dispatchctl mcp` as a stdio server with the same scoped credential configuration. For clients using an `mcpServers` configuration:

```json
{
  "mcpServers": {
    "dispatch": {
      "command": "/usr/local/bin/dispatchctl",
      "args": ["mcp"],
      "env": {
        "DISPATCH_URL": "https://dispatch.example.com",
        "DISPATCH_TOKEN_FILE": "/run/secrets/dispatch-token"
      }
    }
  }
}
```

The transport follows the MCP [stdio](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports), [tool](https://modelcontextprotocol.io/specification/2025-11-25/server/tools) and [lifecycle](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle) contracts. It supports protocol 2025-11-25 and the compatible 2025-06-18 and 2025-03-26 versions. Only protocol messages go to stdout. Tool schemas restrict the supported operations; arguments cannot select an arbitrary URL or approval endpoint. Results include both structured JSON and its text representation. Cancellation stops the request or wait, while the server retains any accepted operation.

## JSON and exit codes

Every command returns `version: "dispatch.client/v1"`, `ok`, HTTP `status` when available, and `data` or a structured `error`. Receipts preserve resource and operation IDs, replay status, the retry deadline, recovery actions and server review references. Server problem details remain available in `error.evidence`. Recovery continuations distinguish `receipt`, `service_resource`, `runtime_retention_review`, `workload_backup` and `workload_backup_operation`. They carry the actual returned ID and, when available, its project, resource and operation IDs. Use the matching inspect command; an accepted HTTP response does not establish successful recovery.

| Exit | Meaning |
| --- | --- |
| 0 | Request succeeded, or wait reached a successful result or explicit paused state |
| 1 | Controller, protocol, capability, quota or other request failure |
| 2 | Invalid arguments or credential configuration |
| 3 | Authentication or project permission denied |
| 4 | Client timeout or cancellation, accepted work may continue |
| 5 | Operation failed, was cancelled or has an unresolved external outcome |

No convenience flag converts a paused approval into permission to execute. Use the server's review identifier and approval process, then inspect the same operation again.
