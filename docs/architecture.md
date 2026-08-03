# Architecture

Dispatch is a modular monolith: one controller owns the API, UI, reconciliation loop, and durable state. Agents and provider adapters are the only required process boundaries.

```text
Browser -> REST/SSE -> Controller -> Store (SQLite or PostgreSQL)
                              |----> Runtime driver -> Agent -> Docker
                              |----> Provider client -> private sidecar
GitHub -> signed webhook -----|<---- scheduled reconciliation
```

## Trust boundaries

- The browser receives inventory and write-only secret metadata, never secret values.
- The controller holds encrypted integration material and issues scoped work to agents.
- SSH is for enrollment and operator-invoked recovery. Routine operations use the agent path.
- Provider adapters receive only the configuration needed for their registered provider.
- Runtime drivers expose typed operations; there is no public arbitrary-command endpoint.

## Deployment invariants

- A deployment binds an immutable source revision to an immutable app-spec digest.
- Only one mutating deployment may be active for an application.
- Every transition is recorded before work continues.
- A controller restart can reclaim an expired deployment lease.
- Docker execution is opt-in. Simulation is the safe development default.
