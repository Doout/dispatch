# Roadmap

This file tracks unfinished infrastructure work. The [product priorities](product-priorities.md) track the implemented UX and operational backlog and later candidates. Current behavior belongs in the README and the feature documents.

## Deployment reliability

- [x] Recover expired deployment leases and interrupted workflow execution after a controller restart.
- [x] Add retained Helm rollback with explicit confirmation.
- [x] Verify configured public URLs and TLS separately from workload readiness.
- [x] Add conservative project retention with preview and confirmed cleanup.

## Agents and private networks

- [x] Add one-use enrollment and key-bound short-lived outbound agent sessions over authenticated HTTPS.
- [ ] Add outbound mTLS for environments that require client-certificate transport.
- [ ] Add typed edge operations for repository checkout, registries, and Kubernetes APIs.
- [ ] Finish the generic Laneway application and installation contract.
- [ ] Add approved cross-network routes after single-network installation is stable.

## Source automation

- [ ] Publish GitHub check runs for repository-managed applications.
- [ ] Add per-source scheduling controls beyond the shared poll interval.
- [ ] Add a clear recovery flow for rejected repository configuration revisions.
- [x] Show promotion/approval context and application activity alongside deployment history.

## Runtime support

- [ ] Add volume backup jobs and restore verification.
- [ ] Add deployment health policies and automatic rollback rules.
- [ ] Test more Kubernetes distributions through the runtime-driver contract.

## Access

- [x] Record actor/impersonator mutation metadata and filter audit activity by project, actor, and action.
- [x] Add verified GitHub team mappings for current sign-in providers.
- [x] Add time-limited project grants.

Every change must keep source revisions immutable, credentials scoped, and destructive actions explicit.
