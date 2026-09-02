# Roadmap

This file tracks unfinished work. Current behavior belongs in the README and the feature documents.

## Deployment reliability

- [ ] Recover expired deployment leases after a controller restart.
- [ ] Add rollback with a destructive-action confirmation step.
- [ ] Verify Traefik routes and certificates after deployment.
- [ ] Add retention rules for runs, logs, and runtime snapshots.

## Agents and private networks

- [ ] Replace long-lived enrollment credentials with short-lived tokens and outbound mTLS.
- [ ] Add typed edge operations for repository checkout, registries, and Kubernetes APIs.
- [ ] Finish the generic Laneway application and installation contract.
- [ ] Add approved cross-network routes after single-network installation is stable.

## Source automation

- [ ] Publish GitHub check runs for repository-managed applications.
- [ ] Add per-source scheduling controls beyond the shared poll interval.
- [ ] Add a clear recovery flow for rejected repository configuration revisions.
- [ ] Show promotion history across every stage and target.

## Runtime support

- [ ] Add volume backup jobs and restore verification.
- [ ] Add deployment health policies and automatic rollback rules.
- [ ] Test more Kubernetes distributions through the runtime-driver contract.

## Access

- [ ] Add audit-log filters for impersonation, account links, role changes, and provider credentials.
- [ ] Add group mapping for external identity providers.
- [ ] Add time-limited project grants.

Every change must keep source revisions immutable, credentials scoped, and destructive actions explicit.
