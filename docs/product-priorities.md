# Product priorities

The deployment and operations backlog is implemented in this release. The table
below links each item to its behavior and limits. Existing Services, manual drift
checks, deployment history, saved-input comparison, and direct navigation remain
part of the same flow.

| Item | Implemented behavior |
| --- | --- |
| Deployment identity | Compact application, environment, target, revision, and run-state context. |
| Running release and latest attempt | Separate links and labels, including when a newer attempt fails or the viewed run is historical. |
| Search and saved filters | Search retained history beyond the overview window; URL filters and per-user saved filter profiles. |
| Compact list | Board and list views with direct deployment, topology, and history links. |
| Application and service links | Direct running-release links and binding-editor access. |
| Navigation shortcuts | Command menu, recent entries, and pinned environments scoped to the signed-in identity. |
| Board status | Separate configuration, runtime drift, and health observations with freshness. |
| Environment comparison | Compare redacted saved deployment inputs across visible environments in the same project. |
| Pre-deployment preview | Resolve source/service inputs, render Helm, and run Kubernetes admission dry-run checks. Image builds and hooks remain deployment-time operations. |
| Historical rollback | Confirm rollback to a uniquely matched retained successful Helm release, preserving its original immutable service Secrets. Docker/Compose artifact rollback is explicitly unavailable. |
| Failure diagnosis | Current pod conditions, events, safely redacted logs, and reason-specific next steps. |
| Activity timeline | Saved deployment/workflow/check actions and authenticated mutation audit metadata. |
| Promotions and approvals | Exact revision/source context and project-scoped approval controls. Changed configuration requires a new revision. |
| Post-deployment observations | Queue one fresh drift/health observation after success and show stale results. |
| URL and TLS verification | Optional public HTTP/HTTPS endpoint check from the controller, separate from workload readiness. |
| Release notes and links | Saved release notes and validated source links associated with a deployment. |
| Scheduled observations | Opt-in intervals, bounded workers, backoff, and visible freshness. |
| Notifications | Explicitly configured encrypted webhook, transition/recovery events, deduplication, retry limits, and mute windows. |
| Service impact | Consumer environments, targets, applied revisions, and redeployment requirements. |
| Credential rotation | Write-only replacements followed by deliberately selected consumer redeployment. |
| Retention | Project policy, preview, and confirmed cleanup of eligible logs/failed history while preserving retained releases and credentials. |
| Controller recovery | Renew deployment leases and mark interrupted work explicitly without replaying uncertain side effects. |
| Backup visibility | Consistent controller database/key backups with isolated SQLite/PostgreSQL restore verification. |
| Access and ownership | Application owner labels, filtered audit events, GitHub team mappings, and expiring project grants. |
| Private execution hardening | One-use enrollment, key-bound short-lived sessions, revocation, authenticated outbound requests, and constrained operation types. |

See [the deployment workspace](deployment-workspace.md),
[release tools](release-tools.md), [observations](observations.md),
[controller operations](operations.md), and [edge node security](edge-credentials.md).

Scheduled observations and notification delivery are off until configured.
Credential edits never automatically redeploy applications. Runtime operations
remain explicit. Existing edge installations retain an identified legacy mode until
an owner rotates enrollment and installs the updated agent.

## Later candidates

Progressive delivery, canary analysis, policy-driven automatic rollback,
maintenance windows, and resource metrics remain later candidates. They are not
activated by this release. Infrastructure work outside the product backlog remains
in [the roadmap](roadmap.md).

Provisioning external dependencies, deleting external services, and connecting
existing Argo CD installations remain outside this scope. Services continue to
accept existing connection information regardless of how the dependency was
created.
