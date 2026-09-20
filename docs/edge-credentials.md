# Edge identities and sessions

New edge nodes use a one-time enrollment token that expires after 15 minutes. The agent generates an Ed25519 identity key before enrolling and saves it in a protected file. Enrollment binds the public key to the node and returns a ten-minute runtime session.

The agent renews its session by signing a controller challenge with its saved private key. Challenges expire after one minute, are bound to the node and credential generation, and can be consumed once. Renewal replaces the previous runtime session. Sessions are kept in memory, and the controller stores their hashes. Enrollment tokens cannot lease or complete jobs.

On restart, the agent loads its identity and requests a new signed session. It does not need a reusable enrollment secret. If enrollment was accepted but its response was lost, the saved identity can still obtain a session.

## Installation and recovery

Use the install command shown by the Connections page within its 15-minute enrollment window. Docker installations persist the identity under `/opt/dispatch-edge/state` on the node, mounted at `/var/lib/dispatch-edge`. The directory belongs to UID 65532 with mode 0700. Systemd installations use a private `StateDirectory=dispatch-edge` with the same persistent path.

The identity file has mode 0600. The agent refuses a symlink, public permissions, invalid key material or an identity saved for another controller/node. `DISPATCH_EDGE_IDENTITY_FILE` can select another protected persistent location for a manual installation. Keep this directory when updating the agent binary or container.

If the identity is lost, the controller owner can create a new enrollment token and reinstall. Creating a replacement token invalidates the previous identity and sessions immediately. The UI asks for confirmation because private connection requests stop until re-enrollment completes.

Revoking credentials stops new jobs and completions for that node, including legacy tokens. Revocation does not remove provider connections or resources. A new enrollment token is required to reconnect.

## Existing installations

Nodes created before this credential model retain their existing token until the owner rotates or revokes it. The UI labels them **Legacy token · re-enroll to upgrade**. Existing installed agent binaries continue to work. Upgrading an old binary requires re-enrollment; a manual transitional installation can explicitly set `DISPATCH_EDGE_LEGACY_TOKEN=true` to retain the old token protocol. Secure enrollment is the default for the new agent.

No production credentials are automatically rotated during a controller upgrade.

## Transport and operations

The agent connects outbound over verified HTTPS, with TLS 1.2 or newer for its controller connection. It does not follow controller redirects or accept a controller URL with credentials, a query or a fragment. No inbound service is required for private provider access.

Queued operations are explicitly typed `https_request`. The agent permits the ordinary HTTP methods GET, HEAD, POST, PUT, PATCH, DELETE and OPTIONS. It rejects tunnels, arbitrary commands, plaintext targets, URL user credentials, requests larger than 1 MiB and headers larger than 32 KiB. Responses are limited to 2 MiB. Redirects are not followed, and upstream certificates are verified. Controller-owned explicit provider requests can carry their required authentication headers; operation payloads and results remain encrypted while queued.

Node failures are reported with sanitized connectivity/TLS errors. They do not include upstream response bodies or credential-bearing URLs.

## API

Enrollment and session exchange authenticate with the node credentials, rather than a user session:

- `POST /api/v1/edge/nodes/{id}/enroll` accepts `publicKey` and a Bearer enrollment token.
- `POST /api/v1/edge/nodes/{id}/challenge` accepts the enrolled `publicKey`.
- `POST /api/v1/edge/nodes/{id}/session` accepts `challenge`, `generation` and the Ed25519 `signature`.

The response from enrollment/session exchange contains `token` and `expiresAt` and uses `Cache-Control: no-store`. Subsequent job requests use that session token.

Only the controller owner may call `POST /api/v1/private-networks/{id}/rotate-token` or `POST /api/v1/private-networks/{id}/revoke`.
