# Architecture

Dispatch is a modular monolith: one controller owns the API, UI, reconciliation loop, and durable state. Agents and provider adapters are the only required process boundaries.

```text
Browser -> REST/SSE -> Controller -> Store (SQLite or PostgreSQL)
                              |----> Runtime driver -> Agent -> Docker
                              |----> encrypted edge job <- outbound Edge -> private provider
GitHub -> signed webhook -----|<---- scheduled reconciliation
```

## Trust boundaries

- The browser receives inventory and write-only secret metadata, never secret values.
- The controller holds encrypted integration material and issues scoped work to agents.
- SSH is for enrollment and operator-invoked recovery. Routine operations use the agent path.
- Provider adapters receive only the configuration needed for their registered provider.
- Edge node tokens are stored as hashes. Provider requests and responses are encrypted while queued and deleted after use.
- Edge nodes accept bounded typed work over an outbound HTTPS poll; they do not expose a public arbitrary-command endpoint.
- Laneway remains available as an IP-overlay transport for legacy socket routes.
- Runtime drivers expose typed operations; there is no public arbitrary-command endpoint.
- OpenShift login text is parsed as data. The controller never invokes a shell or the `oc` binary and never stores the temporary human credential.

## Deployment invariants

- A deployment binds an immutable source revision to an immutable app-spec digest.
- Only one mutating deployment may be active for an application.
- Every transition is recorded before work continues.
- A controller restart can reclaim an expired deployment lease.
- Docker execution is opt-in. Simulation is the safe development default.
- Pasted Kubernetes credentials remain API-write-only in storage and are materialized as mode `0600` temporary files for each Kubernetes operation.
- Managed OpenShift connections persist only the cluster-admin service-account kubeconfig. Repair verifies and persists a replacement token Secret before revoking the prior credential.
- A provider connection explicitly selects Direct or one edge node. The binding is stored with that connection.

## Preview group invariants

- A group contains Helm application templates that target one Kubernetes server.
- Component aliases and repositories are unique, one component is the entrypoint, and dependencies form a directed acyclic graph.
- Every attempt snapshots the full group configuration and desired source SHAs before deployment begins.
- A run owns one generated namespace and stable release name per component.
- Components deploy by dependency level. Outputs are persisted before dependent components start.
- Saved Helm values are applied first, pre-hook values second, and group output bindings last.
- Deployment hooks are owned by event rules and snapshotted per attempt; group rules keep separate hooks for each component.
- Hooks receive normalized `DISPATCH_EVENT_*` context from the command that started the attempt.
- A failed first attempt is removed. A failed update reruns the last successful revision set; a failed restoration leaves the run degraded for operator inspection.
- Only explicitly linked pull requests control automatic cleanup. Default-branch sources do not keep a run open.

## Repository workflow invariants

- A configuration sync is atomic. Invalid files do not replace the last valid resource set.
- Imported Applications and Pipelines remain paused until an operator activates them.
- Every Application run resolves all source branches to exact commits before work starts.
- Remote heads are checked before repository content is fetched. Credential-scoped mirrors are shared across applications; each run receives a detached worktree for its immutable revision.
- A job fingerprint includes only its definition, declared sources, and pipeline inputs. Reuse requires a successful prior result containing every declared output.
- Dispatch resolves secrets before starting a job and excludes them from logs and outputs.
- Stage promotion reuses one immutable revision and its versioned outputs. It does not rebuild between targets.
- A stage starts only after the prior stage and all of its checks succeed. Required approval pauses before deployment.
- Webhooks and polling use the same event deduplication and source snapshot code. Either method can detect a source change.
