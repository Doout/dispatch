# DNS providers

Hosted Dispatch uses an operator-created root record and wildcard for each
installation. Create `doodship.com` and `*.doodship.com` pointing to production,
plus `local.doodship.com` and `*.local.doodship.com` pointing to development.
Tenant creation and deployments do not consume DNS records.

Tenant consoles use `agentops.doodship.com`. Preview addresses use
`p223-<stable-id>-agentops.doodship.com`. The ID separates applications
with the same PR number. All workload names fit one label under the installation
root, so the installation wildcard certificate covers them too.

Dispatch calls a DNS provider only for DNS-01 challenges and cleanup of its old
per-tenant records. It does not run a DNS server. Custom DNS editing is disabled.

## Cloudflare configuration

Add these settings to the controller's environment when Dispatch renews its own
certificate:

```dotenv
DISPATCH_HOSTED_DNS_PROVIDER=cloudflare
DISPATCH_HOSTED_CLOUDFLARE_ZONE_ID=replace-with-zone-id
DISPATCH_HOSTED_CLOUDFLARE_API_TOKEN_FILE=/run/secrets/cloudflare-api-token
```

Use Zone DNS Edit and Zone Read permissions, scoped to the configured zone.
Save the token in a private file with mode `0600` and mount its directory
read-only. The controller runs as UID and GID `65532` and must be able to read
it. Keep provider credentials off tenant workers and out of Compose files.

The catalog binds the root domain, provider and zone ID. Token rotation for the
same zone is allowed. Changing the zone or provider requires a migration.

An HTTPS proxy can renew the certificate instead. Configure its DNS-01 provider
and secrets separately. Certificate renewal runs independently of deployments.

## Record ownership and retries

Dispatch-owned Cloudflare records have a `dispatch:<record-id>` comment. The
provider updates or deletes only records with the matching marker. It preserves
unrelated records and TXT proofs from other certificate issuances.

Challenge writes and cleanup are saved before publication. Provider outages leave
the operation pending for retry after recovery or restart. Delayed cleanup cannot
remove a newer challenge. Tenant creation needs no provider API request.

On upgrade, Dispatch retires only its old tenant console and workload wildcard
records. Operator records and existing tenant custom records survive. Old nested
workload addresses need an explicit route cleanup and redeployment to move to
flat addresses. The controller does not silently rewrite a published route.

## Routing and TLS

Point the installation wildcard at the control-plane ingress. DNS-only records
allow that ingress to terminate HTTPS directly. Dispatch checks hostname ownership
and the current published deployment before forwarding to the operator-configured
workload gateway. It never uses a worker's private loopback address as a central
proxy destination.

Set `DISPATCH_HOSTED_WORKLOAD_GATEWAY` to a shared ingress reachable from the
control plane. It must route the original Host to the correct worker. An explicit
HTTP origin is suitable for a private Docker network. HTTPS origins verify the
upstream certificate. This setting does not create an outbound workload tunnel.

The root and one-label tenant and workload hosts share a wildcard certificate.
Keep its private key at the control-plane ingress. The tenant certificate download
endpoint is disabled. Target Traefik routes use HTTP behind the gateway; public
HTTPS readiness still checks the control-plane certificate. Helm chart ingress
remains chart-managed.

Use a trusted supplied certificate or HTTPS proxy while testing ACME staging.
Staging certificates are not publicly trusted. See
[TLS bootstrap and renewal](hosted-tenants.md#tls-bootstrap-and-renewal).

## Adding a provider

Implement `dnsprovider.Provider` under `internal/dnsprovider`. `Target` identifies
the destination without credentials. `Ensure` publishes an owned record set,
`Delete` removes it, and `Nameservers` supplies the authoritative servers for
DNS-01 propagation checks. Mutations must be safe to retry and preserve records
owned by others.

Register the adapter in `cmd/dispatch-platform/config.go`. Certificate issuance
and cleanup depend on the interface, not Cloudflare-specific calls. Local fixtures
cover ownership conflicts, partial writes, retries and token rotation.
