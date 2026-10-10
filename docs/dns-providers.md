# DNS providers

Hosted Dispatch publishes DNS records through Cloudflare. Use an existing
Cloudflare zone containing the installation's root domain, such as `cicd.onl`
for `dispatch.cicd.onl`. The controller calls the provider API; it does not run
a DNS server.

Create the root console record yourself and point it at the control-plane
ingress. Dispatch manages tenant console records, tenant workload wildcards,
ACME TXT records, and records created through the tenant DNS API. See
[hosted tenants](hosted-tenants.md) for the controller container setup.

## Cloudflare configuration

Add these settings to the controller's environment:

```dotenv
DISPATCH_HOSTED_DNS_PROVIDER=cloudflare
DISPATCH_HOSTED_CLOUDFLARE_ZONE_ID=replace-with-zone-id
DISPATCH_HOSTED_CLOUDFLARE_API_TOKEN_FILE=/run/secrets/cloudflare-api-token
```

Create an API token with Zone DNS Edit and Zone Read permissions, scoped only
to the configured zone. The controller needs DNS Edit to publish records and
Zone Read to validate the zone and discover its authoritative nameservers. See
[Cloudflare API permissions](https://developers.cloudflare.com/fundamentals/api/reference/permissions/).

Save the token in a file with mode `0600`. Bind-mount its directory read-only
into the container. The controller image runs as UID and GID `65532`; that
account must be able to read the file. Do not place the token in an image,
Compose file, or tenant configuration. The provider uses Cloudflare's HTTPS API
and rejects redirects.

The root domain must belong to the configured zone. The catalog binds the
installation to its root domain, provider and zone ID. Rotating a token for the
same zone is allowed. Changing the zone or provider is rejected for a bound
catalog and requires an explicit migration.

## Record ownership and publication

| Name | Destination | Managed by |
| --- | --- | --- |
| `dispatch.cicd.onl` | Control-plane ingress | Operator |
| `agentops.dispatch.cicd.onl` | Control-plane ingress | Dispatch |
| `*.agentops.dispatch.cicd.onl` | Workload ingress | Dispatch |
| `api.agentops.dispatch.cicd.onl` | Tenant-selected address | Tenant DNS API |
| `_acme-challenge.agentops.dispatch.cicd.onl` | Certificate validation TXT | Dispatch |

Dispatch marks its Cloudflare records with a `dispatch:<record-id>` comment.
It updates and deletes only records with the matching ownership marker.
Conflicting records created outside Dispatch block publication instead of being
overwritten. Unrelated TXT values at the same name remain intact. Keep these
comments unchanged and manage Dispatch-owned records through Dispatch. The
controller publishes saved changes and retries failures. It does not detect or
repair later edits made directly in Cloudflare.

A DNS mutation saves the desired record and a pending provider change in the
same catalog transaction. Timeouts, rate limits and provider failures leave the
change pending. The controller retries pending changes during reconciliation,
including after a restart. It never reports a provider failure as a successful
publication.

The tenant DNS API reports publication separately from the desired values:

| Request | Response | Meaning |
| --- | --- | --- |
| `PUT /api/v1/hosted/dns` | `200`, `syncState: synced` | Provider accepted the record |
| `PUT /api/v1/hosted/dns` | `202`, `syncState: pending` | Saved; publication will be retried |
| `DELETE /api/v1/hosted/dns` | `204` | Provider confirmed removal |
| `DELETE /api/v1/hosted/dns` | `202`, `syncState: pending` | Saved; provider removal will be retried |

`GET /api/v1/hosted/dns` lists desired records and their `syncState`. A deleted
record disappears from this list even while its provider removal is pending.
The pending delete remains in the catalog until the provider confirms it.
`Synced` means API acceptance, not that every recursive resolver has refreshed
its cache. ACME issuance separately checks the challenge TXT value on every
nameserver reported by the provider.

Tenant owners and admins can manage A, AAAA, CNAME and TXT records one label
below their tenant console name. Console, wildcard, ACME and other tenants'
records are reserved. The [hosted API specification](hosted-openapi.yaml)
describes request generations and responses.

## Routing and TLS

Dispatch publishes address records with Cloudflare proxying disabled. Use
DNS-only mode for the operator-created root console record too. Traffic reaches
the configured ingress directly, which must terminate HTTPS and route the
requested hostname. See [Cloudflare proxy status](https://developers.cloudflare.com/dns/proxy-status/).

DNS publication does not create an ingress route or a preview tunnel. Configure
workload routing on the deployment target. A tenant wildcard directs hostnames
to that ingress but does not give the controller an HTTP endpoint for them.

The console certificate covers `dispatch.cicd.onl` and `*.dispatch.cicd.onl`.
A workload such as `pr-109.agentops.dispatch.cicd.onl` needs the tenant's separate
`*.agentops.dispatch.cicd.onl` certificate or an exact certificate. Wildcard
certificates cover one label. See [RFC 9525](https://www.rfc-editor.org/rfc/rfc9525.html#section-6.3).

Use a trusted supplied certificate or an HTTPS proxy while testing ACME staging
issuance. Staging certificates are not publicly trusted. After validating DNS-01,
switch to the production ACME directory and verify issuance before enabling ACME
serving. See [TLS bootstrap and renewal](hosted-tenants.md#tls-bootstrap-and-renewal)
for the container settings.

Renewal runs independently of deployment jobs. Pending challenge cleanup survives
restarts and removes only that challenge's TXT value. A renewal failure retains
the previous working certificate until it expires. Monitor certificate expiry
and publication failures.

Tenant owners and admins can retrieve their workload certificate and private
key from `GET /api/v1/hosted/certificate` on the tenant console host. The ingress
must install refreshed bundles and reload TLS. Keep console wildcard keys off
tenant nodes and builders. Cloudflare receives DNS records, never certificate
private keys.

## Moving an existing installation

An installation using Dispatch-operated nameservers needs a planned DNS and
catalog migration. Create the root console record in Cloudflare yourself, then
transfer the tenant records and ownership information before moving DNS traffic.
This version does not provide an automatic migration.

Remove the old parent NS delegation pointing at Dispatch nameservers when the
Cloudflare zone is ready to answer. If Cloudflare hosts the parent zone, those
old child NS records otherwise keep sending requests to the retired servers.
If Cloudflare instead hosts a separate child zone, use its assigned nameservers
for that delegation. Verify public answers and HTTPS before retiring the old
DNS containers. Remove any glue records used only by the retired servers.

Keep a backup of the catalog and certificate state. Changing the configured zone
ID does not move records or rewrite the catalog's provider binding. A migration
must handle those changes together; merely restarting with a new token is not a
migration.

## Adding a provider

Implement `dnsprovider.Provider` in a new adapter under `internal/dnsprovider`.
`Target` returns a stable destination identifier without credentials. `Ensure`
publishes a Dispatch-owned record set, `Delete` removes it, and `Nameservers`
returns the authoritative servers used for DNS-01 propagation checks. Both
mutation methods must be safe to retry and preserve records owned by others.

Register the adapter and its configuration in `cmd/dispatch-platform/config.go`.
Tenant provisioning, publication retries and certificate issuance depend on the
interface and do not need provider-specific changes. Use HTTP fixtures to check
ownership, partial writes, retries and credential rotation before enabling a
new adapter.

## Validation

Local HTTP fixtures cover Cloudflare ownership, conflicts, pagination and failed
requests. Catalog and controller tests cover durable publication retries. ACME
fixtures issue local certificates and exercise DNS-01 propagation, renewal and
cleanup without changing public DNS.

Live Cloudflare publication has not been validated here. Before accepting users,
use an operator-supplied token to create a test tenant, inspect its public DNS
records, complete staging certificate issuance and verify recovery after a
provider failure.
