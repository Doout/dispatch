# Roadmap

This file records the current roadmap implementation and the evidence still needed
before release. Implemented code does not close an issue or establish support for
an untested provider. Feature documents describe the supported paths; the
[product priorities](product-priorities.md) track the broader product backlog.

## Implemented behavior

| Area | Available behavior and limits |
| --- | --- |
| Runtime and deployment recovery | A [versioned runtime contract](runtime-contract.md), immutable source commits, and [enrolled Docker agents](remote-runtime.md) support typed execution and durable receipts. Retained Docker, Compose and Helm releases have [reviewed rollback](release-tools.md). Interrupted mutations require inspection before another operation. |
| Infrastructure | [Provider registration](infrastructure-providers.md), schema discovery, [packaging](provider-packaging.md), [on-demand server operations](on-demand-servers.md), and [pinned agent bootstrap](target-bootstrap.md) share reviewed ownership and recovery records. The bundled infrastructure adapter is a mock; it allocates no real machines. |
| Automation access | [Expiring automation credentials](automation-identities.md), explicit project grants, provider/target assignments, [quotas](infrastructure-quotas.md), and [mutation receipts](mutation-receipts.md) apply to accepted operations and authorized retries. [CLI and MCP commands](automation-client.md) use those same APIs and confirmations. |
| Routing and health | [Managed Docker routes](application-routing.md) publish a candidate after its [captured health policy](deployment-health.md) passes. Workload, route and certificate evidence remain separate. Helm uses native readiness and its rollout behavior. Automatic rollback of an already promoted release is still future work. |
| Storage and services | [Storage ownership](storage.md) protects retained Docker volumes and Kubernetes PVCs. [Owned service recovery](service-resource-lifecycle.md) preserves accepted credentials and blocks deletion while consumers remain. [Runtime retention](operations.md#runtime-artifacts) reviews images and stopped revisions separately from data deletion. |
| Machine snapshots | [Snapshot capture, retention and isolated restore](machine-snapshots.md) use provider capabilities, exact disk evidence, fresh identities and quota reservations. Mock conformance exercises this contract. Real snapshot capture and safe clone boot remain release gates. |
| Workload backups | [Encrypted PostgreSQL backups](workload-backups.md) support owned PostgreSQL 17+ services on Docker, scheduled restore verification, reviewed same-target restore and archive deletion. Generic volume archives, other engines, Helm and offsite recovery are not part of this first path. |
| Temporary environments | [Finite-lifetime environments](temporary-environments.md) clone a same-project Dockerfile template onto an assigned outbound Docker target. Acceptance pins source, template and agent identity; expiry resumes durable cleanup while retaining shared servers and data. |
| PR previews and source automation | [Durable webhook receipts](events.md), [repository recovery](repository-recovery.md), [preview cleanup](preview-lifecycle.md), scoped source approval and [GitHub Check Runs](application-config.md#github-check-runs) are implemented. Previews deploy on comments by default; automatic updates remain opt-in. |
| Neon preview databases | [Project-scoped Neon connections](neon-preview-databases.md) create schema-only branches and retain a preview's database across commits. Source trust runs before provisioning or releasing connection values. The initial cleanup policy retains the branch. |
| Kubernetes targets | [Namespace-scoped registration and validation](kubernetes-targets.md) cover Kubernetes 1.35/1.36 capabilities and target identity. K3s 1.35.5 and external Kind 1.36.4 passed lifecycle, cancellation, partial-apply recovery, rollback and PVC checks. A K3s 1.35-to-1.36 upgrade preserved cluster/namespace identities, deployment history, PVC identity and data. |

## Release gates, in order

1. Validate the combined changes. Run the required race, vet, UI and installer
   checks on the final integrated revision. Preserve the focused SQLite,
   PostgreSQL, Docker and K3s evidence alongside the release. A passing component
   test does not replace the combined checks. Finish the repeatable Kubernetes
   matrix runner and retain its results with the [cluster evidence](kubernetes-targets.md).
2. Choose and validate the first real infrastructure adapter, #74. The choice of
   provider and isolated account is still pending. Complete allocation, lost-reply
   recovery, pinned bootstrap, enrolled workload execution and reviewed deletion
   through the versioned provider boundary. Mock records do not prove a real VM
   was created or became usable.
3. Prove real snapshot and clone behavior, #75. Run capture, retention, independent
   deletion and restore against that adapter. Demonstrate that copied agent keys,
   private receipts and workloads cannot start before identity reset and network
   quarantine. Verify fresh enrollment, the reviewed agent artifact and an
   isolated guest boot. Keep real-provider snapshot and clone support pending
   until this evidence exists.
4. Exercise public routing with an operator-controlled domain, #5. Local Traefik
   tests cover candidate health, switching and rollback. Public DNS, ACME
   issuance and certificate renewal need their own live checks.
5. Complete Neon lifecycle work and live validation, #1. Reviewed retain,
   suspend/delete policies and schema-only replacement are in progress. Verify
   the supported workflow in an isolated Neon project, including interrupted
   operations and retained consumers, before claiming live provider support.

## Remaining feature work

- Extend workload backups beyond the same-target PostgreSQL path: offsite
  destinations, cross-target restore, Helm databases, volume archives and other
  engines, #18.
- Add policy-driven rollback after promotion, #17. Current health policies gate
  promotion; retained rollback remains an explicit reviewed action.
- Add optional outbound mTLS, #6. Existing agents use enrolled keys and
  short-lived sessions over authenticated HTTPS.
- Extend temporary environments beyond Dockerfile templates on enrolled Docker
  targets, #78, while preserving lifetime, quota and cleanup rules.
- Finish the generic Laneway application and installation contract, then add
  approved cross-network routes. Existing private-network access does not imply
  support for arbitrary runtime or network operations.

Confirm public mock-image publication and anonymous pulls before relying on the
published sidecar instructions. The local build path remains available. Snapshot
clone promotion, in-place restore and automatic snapshot deletion are outside the
current contract.
