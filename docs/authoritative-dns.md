# Authoritative DNS for hosted tenants

`dispatch-dns` answers authoritative queries for one delegated zone over UDP and
TCP. It accepts complete zone snapshots from the control plane over HTTPS and
keeps the last accepted snapshot on disk. It does not resolve other domains or
accept public record changes. AXFR, IXFR, and DNS UPDATE are unavailable.

This component does not change the existing installation or DNS delegation.
Run it on dedicated nameserver hosts when preparing a hosted installation.

## Delegation and network setup

Delegate `dispatch.cicd.onl` at the parent `cicd.onl` DNS provider using NS records.
Use at least two nameservers with different failure domains and durable snapshots.
Both UDP **and** TCP port 53 must reach each server. TCP is required for answers
that exceed the UDP size limit. Opening HTTPS alone does not provide DNS service.

For example, publish parent NS records for `ns1.example.net` and `ns2.example.net`
and configure matching nameservers in the Dispatch zone. Those names require
working A or AAAA records. If the nameservers are below the delegated zone, such
as `ns1.dispatch.cicd.onl`, publish their glue A/AAAA records in the parent zone
and matching address records in the child zone. Avoid a circular dependency on
the zone being delegated to resolve its own nameservers.

The nameservers serve an unsigned zone. Do not add a parent DS record unless a
DNSSEC signing deployment has been implemented and verified. This version does
not generate DNSSEC signatures, provide recursion, or support zone transfers.
Each replica instead reads the authenticated snapshot feed.

The HTTP gateway and the nameservers have different roles. DNS maps a hostname
to an ingress address; the ingress routes HTTP and terminates TLS. The exact
tenant console host routes to the control plane. Workload hosts route to the
assigned workload ingress and must never fall back to the console.

## Running a replica

Build with `go build ./cmd/dispatch-dns`. The default listener is
`127.0.0.1:5353`, so starting the binary without explicit listener configuration
does not expose port 53. A configured replica uses:

```sh
DISPATCH_DNS_ADDR=0.0.0.0:53
DISPATCH_DNS_SNAPSHOT=/var/lib/dispatch-dns/zone.json
DISPATCH_DNS_SYNC_URL=https://dispatch.cicd.onl/api/v1/internal/dns/snapshot
DISPATCH_DNS_TOKEN_FILE=/run/secrets/dispatch-dns-token
```

The hosted control plane mounts the read-only feed at
`/api/v1/internal/dns/snapshot` on the main console host. Its
`DISPATCH_HOSTED_DNS_TOKEN_FILE` must contain the same secret as each replica's
`DISPATCH_DNS_TOKEN_FILE`. Use a dedicated token containing at least 32 random characters. The token
file must have no group or other permissions, for example mode `0600`. The token
authorizes only the read-only DNS snapshot feed and must not be a tenant or
platform user credential. HTTPS redirects are rejected to prevent token leakage.

The replica polls every five seconds. A failed refresh leaves the previous
durable snapshot active. A new replica without a snapshot returns SERVFAIL until
it receives one. Monitor refresh errors, replica generation, DNS answers, and
external reachability. Do not advertise a new replica in the parent NS set before
it can answer the current zone over both transports.

Bootstrap the HTTPS listener with a valid supplied certificate before relying on
DNS-01 issuance through this feed. An external TLS proxy must reload renewed
certificates, or the hosted listener must use its ACME certificate callback.
The saved catalog fixes the managed root domain. Changing that domain requires
an explicit migration; restarting with another root domain is rejected.

Without a sync URL and token file, the process serves a prewritten local snapshot.
It reads that file at startup. Use the authenticated feed for ongoing updates.

## Snapshot ownership

The JSON snapshot contains `zone`, `generation`, `serial`, `nameservers`,
`adminMailbox`, `ttl`, and `records`. The mailbox uses DNS SOA notation, such as
`hostmaster.dispatch.cicd.onl`, rather than an email address with `@`.

Each record contains an immutable `id`, `ownerId`, `name`, and `type`, plus
`generation`, `values`, and `ttl`. Supported record types are A, AAAA, CNAME, TXT,
and child NS. Apex NS and SOA come from the snapshot configuration. A and AAAA
values are IP addresses; CNAME and NS values are DNS names. Each TXT value becomes
a separate TXT record, allowing concurrent ACME challenges at one hostname.

A newer snapshot generation and SOA serial are required for changes. When serial
is zero, the lower 32 bits of the generation supply it. Changing values or TTL
requires a newer record generation. Replaying the same generation with different
content fails. Ownership cannot change under an existing record ID. The catalog
must authorize record creation and removal before producing a snapshot; the DNS
replica does not accept tenant-submitted snapshots.

Snapshots are validated before replacing the active zone, written through an
atomic rename with file and directory sync, and stored with mode `0600`. Query
handlers read an immutable snapshot while the next one is prepared.

Wildcard answers use the closest existing ancestor, including empty intermediate
names. Existing records suppress wildcard synthesis. Negative answers contain the
zone SOA and a 60-second negative-cache TTL. UDP answers are capped at 1232 bytes
when EDNS is requested, or 512 bytes otherwise; clients retry truncated answers
over TCP. ANY queries are refused to limit amplification.

## Certificates

`authoritativedns.Reconciler` issues DNS-01 certificates with `x/crypto/acme`.
The caller supplies an explicit HTTPS ACME directory, accepted terms, a private
state directory, and a `ChallengeSolver`. The library does not contact a CA until
the caller invokes `Ensure`. Use an ACME staging directory for integration work.

The solver persists a separate record for each challenge ID, owner, generation,
and value. `Present` must be idempotent. `Wait` must confirm every advertised
authoritative nameserver serves that exact TXT value; `WaitForTXT` implements
that check for explicit nameserver addresses. `Cleanup` must remove only the
matching challenge, preserving other concurrent values at the same name.

The reconciler records pending challenges before publication. It retries cleanup
after interruptions, holds a file lock during issuance, and stores account keys,
certificate keys, certificates, and request ownership in private files. Key
material uses mode `0600`; newly created state directories use mode `0700`.
State is replaced atomically. A stale request cannot replace a newer generation.
Periodic `Ensure` calls reuse the current certificate until two thirds of its
lifetime has passed, then renew it. Failed issuance preserves the previous stored
certificate. The gateway must retain its last working certificate when renewal
returns an error and must alert before expiry.

Saved certificates also record the ACME directory that issued them. Changing
from a staging directory to production requires a new certificate immediately.
The reconciler never serves a staging certificate as a production fallback.

The hosted gateway loads a still-valid saved certificate before attempting
renewal, including after a restart. Renewal runs every five minutes independently
of deployment jobs. Tenant owners and administrators can retrieve their own
workload bundle from `GET /api/v1/hosted/certificate` on their tenant console host.
The endpoint requires a tenant user session and never returns the platform key.
An ingress installer must replace its local bundle and reload TLS when that
certificate changes. Ordinary tenant members, platform-only accounts, workers,
and the DNS replication token cannot retrieve these private keys.

Use separate certificate requests for the platform console and each tenant's
workload namespace. A certificate for `*.dispatch.cicd.onl` covers tenant console
names but not `pr-109.agentops.dispatch.cicd.onl`. That workload needs
`*.agentops.dispatch.cicd.onl` or an exact certificate. Keep the platform wildcard
key off tenant nodes and builders. The DNS server never receives certificate
private keys.

Wildcard certificates match one label, while DNS wildcard lookup follows different
rules. See [RFC 9525](https://www.rfc-editor.org/rfc/rfc9525.html#section-6.3),
[RFC 4592](https://www.rfc-editor.org/rfc/rfc4592.html#section-3.3.1), and
[Let's Encrypt DNS-01 documentation](https://letsencrypt.org/docs/challenge-types/#dns-01-challenge).

## Local verification

`go test -race ./internal/authoritativedns ./cmd/dispatch-dns` exercises real local
UDP/TCP queries, authenticated HTTPS replication, wildcard and delegation answers,
negative caching, oversized responses, stale ownership, and durable reloads. The
ACME fixture signs local certificates, publishes TXT values through the real zone,
queries both transports before validation, renews certificates, and checks cleanup.
It makes no public DNS changes and requests no publicly trusted certificates.
