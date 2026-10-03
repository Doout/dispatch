# Temporary environments

Temporary environments deploy an existing Application template without requiring a pull request. The initial path supports a same-project Dockerfile template on an assigned, ready outbound Docker target. It pins the full Git commit SHA and template specification digest and current enrolled agent identity before acceptance. Replacing that enrollment closes the environment to new work. Cleanup also keeps the original binding; the initial path cannot adopt another machine or agent generation. Such a replacement remains blocked for operator recovery, with ownership and quota retained.

Compose, Helm, local Docker, and snapshot restores are not supported by this path. The choices endpoint omits unavailable targets. Templates with route or certificate health checks are rejected because this path does not create a production domain. Workload health checks are preserved, and a real passing result is required before the environment becomes ready. When the target has a managed routing zone, the review shows a unique generated hostname and pins its routing configuration. The controller also waits for public DNS and required certificate verification before reporting readiness.

The review lists omitted deployment hooks, hook variables, template service bindings, copied data and production domains. Selected same-project service bindings are shared explicitly; the environment never creates or copies their data. Source repository credentials come only from the reviewed template; selecting a template with a global credential still requires controller-owner access. The generated application cannot be edited or deployed independently. Inspect and logs remain available through its existing runtime API.

## Policy and permissions

Creation and extension require `project.view`, `project.configure`, and `deployment.run`, plus an assigned target. Destruction also requires `deployment.cancel`. Current authorization is checked before receipt replay. The original creator's current grants and automation credential are checked again before dispatch and agent lease renewal. Revocation closes the environment to new work; cleanup can continue under its already accepted ownership.

A controller owner must configure `maxTemporaryEnvironments` and `maxTemporaryLifetimeSeconds` in the project's infrastructure quota policy. Missing policy and a zero count disable creation. A count of `-1` explicitly allows unlimited environments; the lifetime must always be finite and at most 365 days. Extensions are measured from the original acceptance time, cannot revive expired environments, and do not reset the creation time. Reads and retries never extend a deadline.

Accepted, deploying, ready, failed, and cleanup-blocked records count toward quota. Capacity is released only when owned workload cleanup finishes. Acceptance atomically saves the generated application, fixed deployment identity, environment ownership, quota reservation, and public mutation receipt. A concurrent caller cannot consume the same review twice or bypass the project count.

## API

All paths below start with `/api/v1`.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/projects/{id}/temporary-environments/options` | Eligible templates and ready assigned outbound targets |
| GET | `/projects/{id}/temporary-environments` | Durable environment records |
| POST | `/temporary-environments/review` | Render a pinned template clone and cleanup consequences |
| POST | `/temporary-environments` | Accept the saved review with `Idempotency-Key` |
| GET | `/temporary-environments/{environmentId}` | State, deadline, owned resources, and operation identities |
| POST | `/temporary-environments/{environmentId}/extend` | Explicit revision-checked deadline extension |
| POST | `/temporary-environments/{environmentId}/cleanup-review` | Review owned and retained resources |
| POST | `/temporary-environments/{environmentId}/destroy` | Accept cleanup with `Idempotency-Key` |

Review creation takes:

```json
{
  "projectId": "project-id",
  "templateId": "application-template-id",
  "serverId": "assigned-target-id",
  "name": "investigation",
  "sourceSha": "0123456789abcdef0123456789abcdef01234567",
  "lifetimeSeconds": 3600,
  "serviceBindings": [
    {
      "alias": "db",
      "serviceRef": "same-project-service-id",
      "environment": { "DATABASE_URL": "connectionUrl" }
    }
  ]
}
```

Acceptance takes `{ "reviewId": "…", "digest": "…", "confirmName": "investigation" }`. It returns the common mutation receipt: `operationKind` is `deployment`, `operationId` is the fixed deployment ID, and `resourceId` is the environment ID. Reuse the same key and body after an uncertain response. Unsupported templates and targets return 422 at review; changed reviews or admission limits return 409 at acceptance.

Extension takes `{ "revision": 3, "expiresAt": "2026-10-02T18:00:00Z" }`. Cleanup review returns the current revision, digest, resource ownership, and consequences. Destruction takes `{ "revision": 3, "digest": "…", "confirmName": "investigation" }` and returns a receipt with `operationKind: "temporary_environment"`. Same-key retries replay that cleanup operation before checking the consumed revision. A different key cannot replace pending or unknown work.

## Expiry and recovery

The database rejects new deployments and unauthorized runtime mutations for generated environment applications. Agent requests and leases cannot exceed the accepted execution deadline. Closing an environment cancels never-leased work and fences running leases; any interrupted mutation with an uncertain outcome must be inspected and acknowledged through the existing application runtime recovery API. Late success reports cannot make a stopped environment ready.

The controller reconciles the original deployment and runtime job after restart. It does not generate another deployment when a response is lost. If no remote request was saved before a restart, dispatch waits for the previous controller execution lease to expire. Cleanup uses a stable remote job identity and the same storage ownership checks as preview cleanup. It removes the owned workload and routing, then closes the generated application while retaining deployment history. The assigned server, named volumes, and backup archives remain independently owned and retained.

An offline target or failed cleanup remains visible as `cleanup_blocked` and continues counting against quota. Reconciliation follows the same saved cleanup operation. After a known failed or cancelled cleanup, or after inspection and acknowledgment of an uncertain cleanup, request a fresh cleanup review and use a new key for the explicit retry. If an interrupted deployment is blocking cleanup, inspect and acknowledge that original operation; the saved cleanup intent then continues.

PR-triggered workflow previews use the same owned-application cleanup helper. Temporary environments do not fabricate a pull request, event, or preview trigger.

To recover an uncertain mutation, submit `POST /apps/{appId}/runtime/inspect` with `{}` and a new inspection `Idempotency-Key`, then poll `GET /apps/{appId}/runtime/jobs/{inspectionId}`. After the inspection succeeds and the old mutation lease has elapsed, acknowledge `POST /apps/{appId}/runtime/jobs/{jobId}/acknowledge` with `{ "inspectionId": "…", "confirmName": "investigation" }`. The initial deployment job is `deploy-{deploymentId}`; the environment record exposes `cleanupJobId` for cleanup recovery. Acknowledgment records uncertainty and does not itself declare resources removed.

## Selected services and managed hostnames

`serviceBindings` is optional and accepts at most 32 existing Dockerfile bindings.
Each selected service must belong to the environment's project and expose every
requested field. Built-in Docker or Helm service resources must use the same
target; externally reachable registered connections may also be selected. Bindings from the original template are never copied implicitly.
The review records service revisions and field mappings without credential values.
Acceptance rejects changed service revisions and captures encrypted deployment
bindings in the same transaction as the environment and request receipt. Credential
rotation afterward does not change the accepted deployment's captured inputs.
Encrypted inline fields and local secret references support this freezing. External
secret-store references require immutable version support and are rejected for
this environment path before runtime work.

Selected services appear as `shared` resources. Expiry removes their workload
consumer bindings after verified workload cleanup and preserves the service,
its data and frozen deployment history. Service provisioning and deletion remain
separate approved operations.

A target's configured routing zone supplies a unique hostname derived from the
new application ID. The review shows the hostname, entry point, TLS policy and
certificate resolver. It copies no production domain. Acceptance locks the target
configuration and reserves that exact global hostname before returning its receipt.
A changed routing configuration or hostname conflict rejects acceptance without
leaving a generated application or consuming environment capacity.

The environment record exposes `hostname`, the pinned `routing`, owned route and
shared service IDs. Workload health and public route readiness remain separate.
Cleanup removes the owned route before closing the environment or releasing its
quota. An unavailable route or uncertain workload deletion keeps cleanup visible
and retryable. The shared target, selected services, retained volumes and backups
remain untouched.
