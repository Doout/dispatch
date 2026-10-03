# Architecture

One Dispatch controller owns the API, web interface, reconciliation loop, and durable state. Agents and provider adapters run as separate processes.

```text
Browser -> REST/SSE -> Controller -> Store (SQLite or PostgreSQL)
                              |----> Runtime driver -> Agent -> Docker
                              |----> encrypted edge job <- outbound Edge -> private provider
GitHub -> signed webhook -----|<---- scheduled reconciliation
```

## Trust boundaries

- Secret APIs return metadata rather than stored credential values. Deployment logs and outputs contain data written by job scripts; scripts must not print credentials.
- The controller holds encrypted integration material and issues scoped work to agents.
- The API checks controller roles and project grants. Hiding an action in the web interface is not an authorization check.
- A linked external identity authenticates the same Dispatch user. Linking accounts does not merge permissions at sign-in time.
- Impersonation requires the controller owner. Credential changes are blocked while the owner views Dispatch as another user.
- SSH is for enrollment and operator-invoked recovery. Routine operations use the agent path.
- Provider adapters receive only the configuration needed for their registered provider.
- Dispatch stores edge node tokens as hashes. It encrypts provider requests and responses while queued, then deletes them after use.
- Edge nodes accept bounded typed work over an outbound HTTPS poll; they do not expose a public arbitrary-command endpoint.
- Laneway remains available as an IP-overlay transport for legacy socket routes.
- Runtime drivers expose typed operations; there is no public arbitrary-command endpoint.
- OpenShift login text is parsed as data. The controller never invokes a shell or the `oc` binary and never stores the temporary human credential.

## Deployments

- A deployment binds an immutable source revision to an immutable app-spec digest.
- Only one mutating deployment may be active for an application.
- Every transition is recorded before work continues.
- Docker execution is opt-in. Simulation is the safe development default.
- Pasted Kubernetes credentials remain API-write-only. Dispatch writes them to mode `0600` temporary files for Kubernetes operations.
- Managed OpenShift connections persist only the cluster-admin service-account kubeconfig. Repair verifies and persists a replacement token Secret before revoking the prior credential.
- A provider connection explicitly selects Direct or one edge node and stores that binding.

## Preview groups

- A group contains Helm application templates that target one Kubernetes server.
- Component aliases and repositories are unique, one component is the entrypoint, and dependencies form a directed acyclic graph.
- Every attempt snapshots the full group configuration and desired source SHAs before deployment begins.
- A run owns one generated namespace and stable release name per component.
- Components deploy by dependency level. Dispatch saves their outputs before dependent components start.
- Dispatch applies saved Helm values first, pre-hook values second, and group output bindings last.
- Deployment hooks are owned by event rules and snapshotted per attempt; group rules keep separate hooks for each component.
- Hooks receive normalized `DISPATCH_EVENT_*` context from the command that started the attempt.
- Dispatch removes a failed first attempt. After a failed update, it reruns the last successful revision set. A failed restoration leaves the run degraded for operator inspection.
- Only explicitly linked pull requests control automatic cleanup. Default-branch sources do not keep a run open.

## Repository workflows

- A configuration sync is atomic. Invalid files do not replace the last valid resource set.
- Imported Applications deploy automatically when an active configuration syncs. Pipelines are available for stage checks immediately. Explicitly paused resources remain paused across syncs.
- Every Application run resolves all source branches to exact commits before work starts.
- The poller checks remote heads before fetching repository content. Applications share credential-scoped mirrors. Each run receives a detached worktree for its immutable revision.
- A job fingerprint covers its definition, declared sources, inputs, resolved secrets, and controller platform. Applications in one configuration source share matching successful results with complete outputs. Concurrent matching builds are coordinated within one controller.
- Dispatch resolves secrets before starting a job. Jobs receive those values in their environment and are responsible for keeping them out of logs and outputs.
- Stage promotion reuses one immutable revision and its versioned outputs. It does not rebuild between targets.
- A stage starts only after the prior stage and all of its checks succeed. Required approval pauses before deployment.
- Webhooks and polling use the same event deduplication and source snapshot code. Either method can detect a source change.

## Backend responsibilities

Provisioning, services, storage, backups and temporary environments follow the same
durable operation sequence. Their approval and recovery rules differ. Keep those
rules within each domain when extracting shared code.

| Code | Responsibility |
| --- | --- |
| `internal/api` | Authenticate public requests, enforce roles and project grants, review mutations, retain idempotency receipts and coordinate domain operations. The API currently also owns backup scheduling and reconciliation. |
| `internal/core` | Define persisted records and public data shapes. Keep command execution and database access out of these types. |
| `internal/store` | Enforce atomic admission, revision checks, lease ownership, protected-resource constraints and durable transitions for SQLite and PostgreSQL. |
| `internal/remoteruntime` | Bind encrypted requests to a node and operation, recheck ownership at execution boundaries, validate typed evidence and retain uncertain outcomes. |
| `internal/deploy` and `internal/agentruntime` | Execute bounded operations, record effects and cleanup evidence, and journal outcomes before acknowledging them. An interrupted mutation requires inspection. |
| `internal/provider`, `internal/provision` and `internal/bootstrap` | Allocate through provider adapters, track the original operation and enroll pinned agent artifacts. Snapshot isolation and deployable target readiness have separate acceptance rules. |
| `internal/workflow` | Capture source revisions, schedule jobs and stage promotion, and preserve the workflow's owned resources and outputs. |
| `internal/automationclient` and `cmd/dispatchctl` | Use the public API contracts, retain continuation IDs and report approval or reconciliation requirements to callers. |

SQL helpers may share a locked read or a compare-and-swap write. Their callers
must continue to own the transaction, lock order and domain predicates. A helper
must not silently expand which operations block admission or which recovery
points qualify for deletion.

Runtime execution validates current service, storage and retention ownership
through `Broker.executionRequest` before lease payload release, renewal and
completion. Request admission keeps its existing validation sequence. API checks
for temporary environments, backup policy authority and agent capabilities remain
separate at their existing boundaries. Do not replace these fresh checks with the
approval captured when an operation was first accepted.

## Maintainability review

The review starting at `0d5debe` on 2026-10-03 found these priorities:

| Order | Finding and next change |
| --- | --- |
| First pass | Remove repeated backup policy authority checks, scheduled backup SQL primitives and runtime ownership validation. Preserve error precedence, lock scope, fresh checks after waiting and uncertain-operation recovery. |
| First pass | Split the complete API race suite into bounded CI shards and verify discovered roots against executed roots. The previous passing API run took 2,299.959 seconds under a 40-minute timeout. Required Docker and PostgreSQL fixtures remain separate checks. |
| Next | Separate backup operation preparation, execution and reconciliation from HTTP handlers. `workload_backups.go` has 893 lines and mixes those responsibilities. Start with one domain service and consumer-owned interfaces while retaining the public review and receipt contracts. |
| Next | Batch backup policy inspection inputs per visible project. Policy listing currently reloads project backups and each backup's operations for each policy. Keep the projection read-only and retain project filtering and cleanup-aware freshness. |
| Next | Reduce dependencies on the base `store.Store`, which has 255 directly declared context methods plus embedded domain interfaces. New domain services should depend on the smallest interface they use. Preserve the current optional-feature error behavior during migration. |
| Next | Define runtime capability advertisement and controller bounds together. Adding an operation currently requires changes to request validation, executor dispatch, the agent advertisement and controller admission limits. Check old and current agent contracts in the same regression suite. |
| Before expanding the UI | Extract feature state and dialogs from `web/src/App.tsx`, which has 3,996 lines, and group the 1,708-line `web/src/api.ts` by domain behind its existing exports. Keep route handling, authentication and overview subscriptions in the application shell. |
| As contracts grow | Check OpenAPI, shipped clients and wire examples together. A JSON shape change can affect saved encrypted requests and agent digest checks even when a UI does not use the endpoint. |

These sizes describe the reviewed revision. Use them to locate responsibilities,
then assess coupling and repeated rules before deciding what to move. File size
alone does not justify a refactor.

Each refactor should preserve observable behavior and include the relevant
existing lifecycle tests. Add a regression when a boundary lacks coverage, such
as revocation while execution waits for a target lock. PostgreSQL tests must
exercise transaction and concurrency changes. Recovery tests must continue to
retain original IDs, keys, receipts and protected data. Keep schema migrations
and public contract changes separate from behavior-preserving extraction.
