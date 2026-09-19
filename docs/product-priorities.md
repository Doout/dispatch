# Product priorities

This is the proposed product backlog. It builds on the existing Services, drift
checks, deployment history, version comparisons, approval gates, and immutable
stage promotion. Items below are recommendations, not a promise that they have
been implemented.

## Deployment navigation

The Deployments page now opens an environment's latest deployment directly from
its compact stage link. Recent-run links also open the full deployment page.
There is no required preview dialog. The separate chevron expands inline stage
inspection. A stage without a deployment opens its build, approval, or empty-state
information. Links retain normal open-in-new-tab behavior. Returning from details
restores the board's selected stage and scroll position.

## Next: make daily use faster

| Item | Result |
| --- | --- |
| Deployment identity in the header | Show application, environment, target, revision, and run state next to the page title. |
| Live release versus latest attempt | Keep the currently running release visible when a newer attempt fails; label historical views explicitly. |
| Search and saved filters | Filter deployments by application, project, environment, target, status, and revision; keep filters in the URL. |
| Compact list view | Offer a searchable list alongside the stage board, with direct links to a run's logs, topology, and history. |
| Application-to-deployment links | Open the relevant environment's running release from application details and service dependency lists. |
| Global navigation shortcuts | Add a command menu, recent applications, and optional pinned environments. |
| Status on the board | Show configuration sync, drift, and health separately from rollout success, including observation age and unavailable reasons. |
| Environment comparison | Compare saved deployment inputs, image versions, and service revisions across development, staging, and production. Hide credentials. |

## Next: improve deployment confidence

| Item | Result |
| --- | --- |
| Pre-deployment preview | Show the target, selected revisions, bindings, and expected changes before starting. Check rendering, credentials, and Kubernetes server-side dry-run validation where supported. |
| Historical rollback | Select a retained successful deployment, inspect its inputs and credentials, and create an audited rollback. Explain missing artifacts and database migration limits. |
| Failure diagnosis | Bring failing pods, scheduling errors, image-pull failures, restarts, events, and relevant logs together with specific next steps. |
| Unified activity timeline | Connect source changes, builds, approvals, deployments, readiness observations, drift checks, and operator actions. |
| Promotion and approval workspace | Expose the existing immutable promotion and approval capabilities in a clear environment view; show the exact revision awaiting approval. |
| One readiness check after deployment | Save a fresh observation after successful deployment. Show current progress and a stale indicator when results age. |
| Endpoint verification | Report application URL and TLS checks separately from Kubernetes workload readiness and indicate the observation location. |
| Deployment notes and source links | Attach release notes and link revisions to commits and pull requests where available. |

Pre-deployment validation can use Kubernetes' existing
[server-side dry-run behavior](https://kubernetes.io/docs/reference/using-api/api-concepts/#dry-run).
It cannot guarantee a later rollout will succeed, and supported admission checks
must not produce side effects.

## Then: reduce operational work

| Item | Result |
| --- | --- |
| Optional scheduled observations | Configure bounded drift and health checks per application with intervals, backoff, concurrency limits, and visible freshness. |
| Actionable notifications | Notify on state transitions and recovery, with deduplication, mute windows, and links to the affected deployment. Configure delivery explicitly. |
| Service impact view | Show which applications and environments use a dependency, its test location/time, and which deployments need updated service revisions. |
| Credential rotation workflow | Show affected applications, rotate write-only inputs, and let operators redeploy selected bindings deliberately. |
| Retention controls | Prune logs, runs, and snapshots by policy while preserving active releases and credentials needed by retained history and rollback. |
| Controller recovery | Recover interrupted work and expired leases without creating duplicate deployments. |
| Backup and restore verification | Make backup age and restore-test outcomes visible, with a documented recovery process. |
| Better audit and ownership tools | Add actor/action/project filters, application owners, team identity mappings, and expiring project grants. |
| Private execution hardening | Improve short-lived agent enrollment, outbound authenticated connections, and typed operations for private targets. |

## Later, after the above is reliable

Progressive delivery, canary analysis, policy-driven rollback, maintenance windows,
and richer resource metrics are later candidates. Automatic mutation needs a
separate design and explicit configuration. Scheduled observations are also a new
opt-in capability; today's checks remain manual.

Provisioning external dependencies and connecting existing Argo CD installations
remain outside this backlog's immediate scope. The Services model continues to
accept existing connection information regardless of how the dependency was
created. Infrastructure-specific unfinished work remains in [the roadmap](roadmap.md).
