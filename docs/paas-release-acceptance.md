# PaaS release acceptance

The provisioning and recovery APIs have local acceptance tests. Live support also
requires evidence from the intended provider, hosts and public routing setup.
Keep these results separate when deciding which capabilities to expose.

## Inputs for the live run

Use an isolated project and disposable resources. Record these choices before
allocating machines or changing DNS:

| Input | Required evidence |
| --- | --- |
| Provider account | Adapter revision, account/project, allowed regions, images, sizes and networks, machine limit and spending limit. |
| Independent targets | Two separately enrolled Linux hosts with their own Docker daemons and supported OS, Docker and Compose versions. |
| Bootstrap | The controller HTTPS origin, pinned agent artifact and trusted SSH host key for installation recovery. Keep credentials write-only. |
| Public routing | A controlled disposable domain, DNS records, reachable proxy entry points and explicit ACME policy. |
| Offsite storage | An isolated bucket/prefix and scoped credential reference. Record conditional-delete support and bucket versioning before approving deletion. |

## Server creation and remote execution

Start with an already-owned disposable host to validate the remote runtime. Then
use the selected real adapter to create a machine through the reviewed API.
Follow its original operation through allocation, bootstrap and enrollment.

Use `dispatchctl server get --server SERVER_ID` and
`dispatchctl server wait --server SERVER_ID --timeout 120` to inspect readiness.
An allocation receipt alone does not establish a usable workload target. A
verified isolated snapshot clone must remain separate from deployable servers.
After a wait timeout, resume with the same server ID.

Deploy both a Dockerfile application and a supported Compose application. Exercise
inspect, bounded logs, start, stop, cancellation, reviewed rollback and cleanup
through the outbound agent. Restart the actual controller and agent processes,
interrupt connectivity and lose a completion response. Record whether the
original operation converged or requires inspection. Do not allocate a replacement
or repeat an uncertain mutation to hide that outcome.

Test revoked identities, wrong targets, foreign resources and unsupported
capabilities. An owned PostgreSQL service must retain its protected data during
application cleanup. Reviewed server deletion must account for service data,
retained volumes, backup copies and unresolved operations separately.

## Public routes and certificates

Resolve the deployed application's hostname through public DNS. Verify certificate
issuance, expiry, renewal and the active workload destination. Exercise pending
or failed issuance, hostname conflicts and an unhealthy deployment candidate.
The previously healthy route must survive failed promotion. Record proxy restart
and healthy cutover results without claiming that workload health alone establishes
public certificate readiness.

## Backup preservation and source loss

Create recognizable PostgreSQL data and a capture policy with an explicitly
approved offsite store. Wait for the downloaded archive to pass isolated restore
verification. Record captured recovery-point time separately from upload and
verification times. Exercise failed export, stale protection and notification
recovery using an explicitly configured destination.

Review retirement of the local copy. Confirm that source-server removal remains
blocked until retirement is complete and other protected data is addressed.
Preserve the controller database and master key separately; offsite ciphertext
cannot replace them.

Make the original host unavailable. On the second host, verify the offsite backup
and restore into a fresh, explicitly reviewed owned destination. Check the
recognizable data and final temporary-resource inventory. Interrupt an upload or
reply and revoke storage credentials. Inspect the same objects and operation
before selecting recovery.

Approve exact offsite deletion only when the registered store enforces conditional
unversioned deletion. A delete marker in a versioned bucket does not establish
archive removal. Keep the last usable recovery point and artifacts required by
active operations protected.

## Machine snapshots and clone boot

Use the real adapter's declared disk, consistency and encryption capabilities.
Capture and retain a snapshot, then create an isolated clone while preserving the
source. The provider must sanitize copied agent credentials, runtime receipts,
machine and SSH identities, workloads and production bindings before the copied
system can start them. Post-boot bootstrap alone cannot establish this guarantee.

Verify independent disk identities, fresh enrollment and a guest boot. Report
crash-consistent bootability separately from database or application integrity.
Exercise inaccessible snapshots, failed bootstrap, interrupted verification and
reviewed deletion with final inventory.

## Evidence boundaries

The required Docker fixtures include recovery after removing a source daemon and
its storage. Those two nested daemons share a physical host and use a TLS object
store fixture. This exercises separate image, volume and container inventories;
it does not close the independent-host or live object-store release gate.

Keep evidence in sanitized JSON or text. Include original operation IDs, artifact
digests, versions, checked outcomes and remaining owned resources. Exclude
credentials, private payloads and signed URLs. Screenshots are unnecessary.

Track live acceptance in issues #68, #15, #74, #75, #5 and #90. API readiness and
backup retirement do not close those gates by themselves.
