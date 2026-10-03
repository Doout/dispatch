# Product priorities

The deployment workspace provides the behavior below. Existing Services, manual
drift checks, deployment history, saved-input comparison and direct navigation
remain part of the same flow. New provisioning and recovery APIs are listed
separately below; their UI follows this release.

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
| Historical rollback | Reviewed rollback restores retained Docker/Compose images and encrypted runtime inputs on the original target, or a uniquely matched successful Helm release with its original immutable service Secrets. Required artifacts must remain available. Rollback preserves data and does not reverse database migrations. |
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

## Provisioning and recovery APIs

These contracts are available through the API, CLI and MCP. The future UI must
preserve their permissions, confirmation reviews and original operation IDs.

| Item | Available behavior and limits |
| --- | --- |
| Managed-server readiness, #93 | Scoped inspection and bounded waits report allocation, bootstrap, enrollment and runtime readiness separately. Timeout preserves continuation IDs. A verified isolated snapshot clone remains non-deployable; approval, failure and uncertain outcomes require inspection. |
| Scheduled offsite protection, #92 | An explicitly approved store receives encrypted exports of verified PostgreSQL captures. Independent offsite restore verification establishes a recovery point. Policies expose capture age, verification time, missed exports, failures and freshness. Notifications use an explicitly configured destination. |
| Automatic local retention, #92 | Local retirement requires separate policy opt-in and independently verified offsite preservation. Local-only policies retain their existing behavior. Offsite objects are never deleted by automatic retention. |
| Reviewed backup retirement, #88 | Retire local bytes after authenticating the preserved offsite copy. Delete only reviewed owned offsite objects through conditional unversioned requests. Keep the last usable recovery point, controller keys and history; unresolved cleanup retains protection. |
| Source-loss recovery, #90 | Verification and reviewed restore can use a fresh authorized destination without the original source. Independent-host and live object-store acceptance remain open. |

Follow [PaaS release acceptance](paas-release-acceptance.md) before claiming live
support. Real provider allocation and enrollment, safe snapshot clone boot,
public DNS and ACME lifecycle, and recovery between independent hosts remain
release gates. Local fixtures do not close them.

## Later candidates

Approved rollback policies for unhealthy promoted releases remain #89.
Progressive delivery, canary analysis, maintenance windows and resource metrics
remain later candidates. This release does not activate them. Infrastructure work
remains in [the roadmap](roadmap.md).

Provisioning arbitrary external services and connecting existing Argo CD
installations remain outside this scope. Dispatch can provision and recover
approved owned PostgreSQL services on Docker. Services also accept existing
connection information regardless of how the dependency was created.
