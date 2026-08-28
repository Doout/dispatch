# Roadmap

Dispatch grows by completing one operational loop at a time. The public core stays provider-neutral; environment-specific automation remains behind versioned contracts.

## Milestone 1: deployment spine

- [x] Single Go controller with embedded React console
- [x] SQLite WAL default and PostgreSQL-compatible store
- [x] Projects, Docker targets, applications, deployments, evidence logs, and SSE
- [x] Simulation executor and opt-in local Dockerfile/Compose executor
- [x] Secret vault primitive and provider/runtime contracts
- [x] Small target agent status surface
- [ ] Agent enrollment with short-lived tokens and outbound mTLS work stream
- [ ] Durable deployment lease recovery after controller restart
- [ ] Traefik route creation and certificate verification
- [ ] Rollback and destructive-action confirmation flows

## Milestone 2: source automation

- GitHub App authentication scoped to selected repositories
- Signed webhook ingestion with delivery deduplication
- Repository installation and branch selection
- Pull-request preview policy, create/update lifecycle, and teardown on close
- Check-run status and deployment links posted back to GitHub

## Milestone 3: private infrastructure providers

- Provider registration and encrypted credential records
- Conformance-tested mock sidecar
- Idempotent create, poll, adopt, and delete server workflows
- Private provider image and configuration kept outside this repository
- SSH bootstrap as an explicit enrollment/recovery operation

## Milestone 4: runtime depth

- Health policies, domain routing, wildcard TLS, rollback, and retention
- Volumes, managed backup jobs, and restore verification
- K3s runtime driver, followed by general Kubernetes support

Each milestone must preserve immutable deployment evidence, scoped credentials, explicit destructive actions, and a safe simulation path.
