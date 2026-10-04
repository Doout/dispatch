# Target agent installation and recovery

Dispatch can place a reviewed cloud-init installer in a provider machine request, or install and recover an existing Docker target through verified SSH. Both paths install the same pinned agent and use its enrolled runtime for deployments, logs and lifecycle operations.

Set `DISPATCH_PUBLIC_URL` to the controller's public HTTPS origin. Install the release agent binaries under `DISPATCH_EDGE_BINARY_ROOT`, which defaults to `/usr/local/lib/dispatch-edge`, with filenames `linux-amd64` and `linux-arm64`. The review records the selected binary's SHA-256 and its content-addressed download URL. If that binary is replaced while a review or installer is pending, the old URL returns 404. Restore the reviewed binary or create a fresh review; the installer never accepts a different download or an HTTP redirect.

## New provider machines

Submit the provider's region, size, image, network and SSH public key reference to `POST /api/v1/infrastructure/servers/review`. Include a `bootstrap` plan with `method: cloud_init`, the image's `platform` and `imageFamily`, and `installRuntime`. Review the returned artifact and actions before accepting creation. The [server CLI commands](automation-client.md) support the same plan.

The selected image must support cloud-init and include Python 3, systemd, `flock` and `timeout`. Automatic prerequisite installation supports Ubuntu 24.04 and installs Docker, Compose v2, Git, curl and CA certificates. Other systemd images must already provide Docker, Compose v2 and Git. Choosing a different image never bypasses these checks.

The exact user-data is encrypted with the provider request. Public reviews contain only the installation plan, never the claim or enrollment token. The claim is inert until the machine review is accepted. Its intended project, provider registration, provider resource, target and node remain bound throughout installation. Accepting the review activates the claim in the same transaction that saves the provider operation.

The installer requests its short-lived enrollment token over HTTPS after allocation and prerequisite checks. A lost reply returns the same encrypted token receipt. An expired, unused token can be replaced for that same target without allocating another machine. The installation claim expires after 24 hours; individual enrollment tokens expire after 15 minutes. Treat provider user-data as sensitive and restrict provider access accordingly.

Allocation, installation, enrollment and runtime readiness are separate. An IP address or generic agent heartbeat does not make a target ready. Dispatch waits for the intended enrolled key, the reviewed agent artifact, and a fresh authenticated runtime check. The agent checks the Docker daemon, Compose v2 and Git before advertising its deployment runtime.

## Verified SSH installation

Use `POST /api/v1/infrastructure/bootstrap/review` with `serverId` for an existing target, or omit that ID to import an existing machine. Supply the SSH host, port and user in `plan`, plus a password or private key in the write-only `credentials` object. These credentials are encrypted for this installation and excluded from API responses. Saved private inputs are erased when installation completes, its claim expires, or a new approved recovery plan supersedes it. Builder SSH credentials remain separate.

Supply the SSH host public key and verify its fingerprint through the provider console or another trusted channel. A network scan alone is insufficient. The review shows the exact host, user, fingerprint, artifact SHA-256 and actions. A controller owner must accept the review with its `digest` and exact target name as `confirmName` before execution. SSH verifies the pinned host key before offering authentication and runs only the generated installer. Non-root users require passwordless sudo. There is no arbitrary command endpoint.

The installer preserves `/var/lib/dispatch-edge/identity.json` and the runtime receipt journal during ordinary upgrades. Setting `replaceIdentity: true` in the plan requests a separate reviewed recovery action. It retires the previous enrollment and sessions, and a local marker prevents a retry from deleting the new identity a second time. Existing identity files for another controller or node are rejected, including identities copied from another machine.

A new approved recovery plan supersedes the previous claim. A running SSH lease must finish or expire before another plan can be accepted. Approved SSH work resumes after a controller restart. Installation is bounded to 14 minutes, with a 16-minute durable lease and an on-target installer lock. Interrupted work shows a sanitized status and can be retried with the same plan and encrypted credentials. A changed host key, expired claim or changed identity requires a new review for the same target.

Cloud-init failure never retries provider allocation. Inspect the machine's cloud-init status, or choose that existing machine for SSH recovery. An image clone must disable copied workloads and erase its source agent identity and runtime receipts before boot; ordinary bootstrap intentionally refuses a copied identity.

## API

- `POST /api/v1/infrastructure/servers/review` accepts an optional `bootstrap` plan with `method: cloud_init`, `platform`, `imageFamily` and `installRuntime`. The normalized plan is returned in `input.bootstrap`; `bootstrapId` is assigned by the controller.
- `POST /api/v1/infrastructure/bootstrap/review` accepts `serverId` for recovery, or no ID to import a machine, plus `plan` and write-only `credentials` for verified SSH. It returns a review and digest.
- `POST /api/v1/infrastructure/bootstrap/{id}/accept` accepts the review `digest` and exact `confirmName`.
- `POST /api/v1/infrastructure/bootstrap/{id}/retry` accepts the original `digest` for an accepted SSH installation. It preserves the target, artifact, saved credentials and enrollment generation; it never creates a machine or approves a new plan.
- `GET /api/v1/infrastructure/bootstrap` lists visible installations, with optional `projectId` and `serverId` filters. `GET /api/v1/infrastructure/bootstrap/{id}` refreshes that installation's sanitized status and evidence without starting work.

Listing and inspection require current `infrastructure.inspect` access; retry requires `infrastructure.modify`. Project callers and automation accounts must also retain the provider assignment, or the target assignment for an ordinary project target. Reviews, imports and acceptance remain controller owner operations. Unbound installations remain owner-only. A changed enrollment generation, expired claim or active lease blocks retry; use a new owner-approved recovery plan when needed. After a lost retry response, inspect the original installation before another explicit retry. Clients do not retry automatically.

Provider creation retains the project's existing infrastructure permission, provider assignment, public SSH-key assignment and quota checks. The installer-only claim and progress routes authenticate their own scoped bearer capability and return non-cacheable responses. Their bearer credential must never be placed in a URL.
