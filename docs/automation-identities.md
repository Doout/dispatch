# Automation identities

A controller owner creates an automation account, gives it explicit project grants,
and issues a token with an expiry. CI jobs and agents use the token as a bearer
credential. Target-agent enrollment tokens remain separate and cannot call these APIs.

## Issue and rotate a token

1. `POST /api/v1/automation-accounts` with `name` and optional `description`.
2. `PUT /api/v1/infrastructure/grants` with `principalType: service_account`,
   `principalId`, `projectId`, and `permissions`. An optional `expiresAt` limits
   the grant independently of token expiry.
3. `POST /api/v1/automation-accounts/{id}/credentials` with `name` and `expiresAt`.
   Expiry must fall within the next 365 days. The response contains `credential`
   metadata and the only copy of the plaintext `token`. Store it in the caller's
   secret store. The response uses `Cache-Control: no-store`.
4. Send `Authorization: Bearer <token>` on subsequent requests.

`POST /automation-accounts/{id}/credentials/{credentialId}/rotate` accepts the
same body and returns a replacement token. It retires the old credential and
issues the new one in one transaction. Rotation preserves the account ID, project
grants and audit history. Only one concurrent rotation of a credential can succeed.

`POST /automation-accounts/{id}/credentials/{credentialId}/revoke` revokes a token.
`PUT /automation-accounts/{id}` with `name`, `description` and `state: disabled`
disables all of the account's tokens. Authentication checks current account state,
revocation and expiry on every request. Revocation does not claim to undo work
already accepted by Dispatch.

Owner-only account and credential lists contain expiry, revocation and last-use
timestamps. They never contain token values or hashes. Mutation audit records
include the stable account ID and the credential ID used for that request.

## Project grants and targets

The grant API accepts these explicit permissions:

| Permission | Allowed action |
| --- | --- |
| `project.view` | Read the project's applications, deployments and logs |
| `project.configure` | Configure applications and their permitted bindings |
| `deployment.run` | Start deployments and supported runtime mutations |
| `deployment.cancel` | Request cancellation |
| `service.provision` | Provision an approved built-in service on an assigned target |
| `runtime.cleanup` | Review and confirm runtime artifact cleanup under the saved project policy |
| `infrastructure.inspect` | Inspect assigned providers and targets |
| `infrastructure.create` | Request server allocation through an assigned provider |
| `infrastructure.modify` | Request supported changes to owned infrastructure |
| `infrastructure.delete` | Request confirmed infrastructure deletion |
| `infrastructure.snapshot` | Request supported snapshot creation |
| `infrastructure.restore` | Request supported restoration |

Permissions authorize their action only when that action is implemented by the
selected runtime or provider. They do not enable an unsupported capability.
Existing project roles do not gain infrastructure permissions. The grant endpoint
also accepts `principalType: user` for explicit grants to human operators.

A controller owner assigns resources with
`PUT /api/v1/infrastructure/assignments/{projectId}` and a body containing
`kind: provider`, `kind: target`, or `kind: ssh_key` plus `resourceId`.
SSH key assignments allow only the stored public key to be included in a server
create request. The private key stays in the secret store. Project callers cannot
attach other global secret references to provider configuration. A project-owned target is
available only to its owning project and cannot be reassigned through this API.
Unowned, administrator-managed targets may be explicitly shared with projects.
An operator needs a target assignment before creating an application on that target.
Existing applications retain their configured target.

Use `GET /api/v1/servers?projectId=...` to list assigned targets with
`infrastructure.inspect`. Provider registration, provider credential administration,
account issuance and grant changes remain owner-only. `DELETE
/infrastructure/grants/{kind}/{principalId}/{projectId}` removes a direct grant.

Accounts cannot impersonate users, modify sign-in credentials, grant their own
permissions, or approve a stage requiring human approval. Destructive-action
confirmations still apply. A confirmation supplies the reviewed action's intent;
it does not grant missing permissions or count as human approval.

The project grant must still be valid when a prepared infrastructure review is
accepted. The immutable project owner accompanies the accepted operation and
its resulting server. Requests and operation inspection use current project
permissions, even when a token was rotated after the operation began.

## Scoped service provisioning

Automation may call `POST /api/v1/service-templates/{id}/runs` with `project.view`
and `service.provision`. The controller owner first assigns the exact template
with `kind: service_template` and `resourceId: "<template-id>@<configSha>"` through
the project assignment endpoint. Read `configSha` from the template API. An edited
definition requires a new approval. The target must also be assigned to the project.

Only built-in PostgreSQL Docker or Helm templates are supported. Scripts,
repository execution, Neon provisioning and template editing remain unavailable
through this grant. The approved template fixes the provisioner and target;
the caller supplies only its declared inputs and the service name.

Every automation request needs a stable `Idempotency-Key`. The controller saves
its encrypted request, owned resource and receipt together before runtime work.
A repeated acceptance returns the original operation. Admission failures retain a
terminal rejected receipt. After the owner fixes approval or quota, inspect that
receipt and use a new key for a new attempt. A changed template digest
or changed request cannot reuse that key. Inspect the run and resource when an
outcome needs recovery; retrying acceptance does not allocate a replacement.

The owner must configure `maxServices` in the project's infrastructure policy.
It defaults to zero. Pending, ready, failed and unresolved owned resources count
until verified deletion, including resources created by people. The limit applies
to new automation provisioning; existing human provisioning behavior is unchanged.
Use the existing resource inspection, reviewed deletion and recovery permissions
for lifecycle management. Provisioning alone does not grant those permissions.

`runtime.cleanup` permits reading the saved retention policy, requesting a runtime
review, inspecting its receipt and applying that exact reviewed candidate set.
Apply still requires the project-ID confirmation field, saved policy, review ID
and digest. This grant cannot change policy or prune deployment history. Active
releases, shared images, protected rollback revisions, volumes and backups keep
their existing protections. Operations must be enabled by the controller owner.
