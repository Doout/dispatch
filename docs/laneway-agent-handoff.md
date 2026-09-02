# Laneway agent handoff

Implement the Laneway side of the registered application contract in
`docs/laneway-integration.md`. Keep the implementation generic. Laneway must not
import Dispatch code or add Dispatch-specific resources, routes, scopes, or UI
labels.

## Starting point

The audit used Laneway commit `577d259fc925234fe75727e937c4a373d53558cb`.
Recheck the current branch before changing it.

Laneway already has:

- network-scoped service principals
- hashed service access tokens
- per-operation checks on management routes
- one-time node enrollment tokens
- systemd installation through `laneway node install`

It does not have registered applications, application installations, OAuth
authorization codes, rotating refresh tokens, application consent pages, or
the node-installer endpoint expected by Dispatch.

The current service access-token limit counts expired but unrevoked tokens.
Add bounded cleanup before relying on hourly access-token rotation.

## Required data model

Add records for:

- registered applications
- exact redirect URIs
- registered scope ceilings
- hashed client credentials
- single-use application registration codes
- one application installation per application and network pair
- granted installation scopes
- single-use OAuth authorization codes
- refresh-token families and rotating refresh tokens

An installation owns one service principal. Set `all_networks=false` and store
exactly one network ID. Map granted OAuth scopes to the existing service
principal permissions. Client credentials alone must never authorize a network
request.

Application names are display values. Do not make them unique. Enforce one
active installation for each application and network pair.

## Browser registration

Implement:

```http
POST /applications/new
Content-Type: application/x-www-form-urlencoded

manifest=<json>&state=<state>&code_challenge=<challenge>&code_challenge_method=S256
```

Limit the body size and request rate. Parse the manifest, validate every URI,
and save a short-lived pending request. Redirect to a same-origin review page.
The approval mutation requires an authenticated Laneway administrator, CSRF
protection, and same-origin checks.

On approval, redirect to the exact `setup_uri` with a registration code and the
original state. Dispatch then calls:

```http
POST /v1/application-registrations/exchange
Content-Type: application/json

{"code":"...","code_verifier":"..."}
```

Return the application ID, client ID, client secret, and display name. Return
the client secret once. Store only a purpose-separated hash. Registration codes
expire within five minutes and are consumed atomically.

## Network installation

Implement:

```http
GET /oauth/authorize
POST /oauth/token
POST /oauth/revoke
```

`GET /oauth/authorize` authenticates a Laneway administrator, validates PKCE
S256 and the exact registered redirect URI, then asks that administrator to
select one manageable network and approve requested scopes. Requested scopes
must fit inside the application's registered scope ceiling. The administrator
must hold every approved operation on the selected network.

The authorization-code exchange uses HTTP Basic client authentication and
returns:

```json
{
  "access_token": "lnw_spat_v1....",
  "token_type": "Bearer",
  "expires_in": 3600,
  "refresh_token": "lnw_refresh_v1....",
  "scope": "network.read node.read enrollment.issue route.read route.manage",
  "installation": {
    "installation_id": "installation_...",
    "application_id": "app_...",
    "network": {
      "network_id": "network_...",
      "name": "Production",
      "ipv4_pool": "10.42.0.0/16",
      "ipv6_pool": "",
      "configuration_epoch": 1
    }
  }
}
```

Access tokens expire after one hour. Refresh tokens expire after 30 days and
rotate on use. Refresh-token reuse revokes the family. Reauthorizing an existing
application and network updates that installation instead of creating another
active installation.

`POST /oauth/revoke` accepts the refresh token with
`token_type_hint=refresh_token`. Revoke the installation, its service principal,
access tokens, and refresh family in one transaction.

## Scopes and policy

Support this first scope set:

| OAuth scope | Existing operation |
| --- | --- |
| `network.read` | `network.read` |
| `node.read` | `node.read` |
| `enrollment.issue` | `enrollment.issue` |
| `route.read` | `route.read` |
| `route.manage` | `route.manage` |

Do not grant `node.manage`. Do not grant global operations such as
`network.create` or `service_principal.manage` to the installation principal.

Add human operations for application administration:

- `application.read`
- `application.manage`
- `application_installation.read`
- `application_installation.manage`

Service principal permissions are immutable in the audited code. When approved
scopes change, create a replacement principal and swap it into the installation
in one transaction. Revoke the old principal and token family in that same
transaction.

## Management API

Installation access tokens must work on the existing network-scoped routes:

```text
GET  /v1/admin/networks/{network_id}
GET  /v1/admin/networks/{network_id}/nodes?limit=500
GET  /v1/admin/networks/{network_id}/endpoint-statuses?limit=500
GET  /v1/admin/networks/{network_id}/routes?limit=500
POST /v1/admin/routes/assign
```

Keep the current exact-network checks. A token for one installation must fail
on every route for another network.

Add application administration routes:

```text
GET    /v1/admin/applications
GET    /v1/admin/applications/{application_id}
POST   /v1/admin/applications/{application_id}/client-secrets
POST   /v1/admin/applications/{application_id}/disable
GET    /v1/admin/networks/{network_id}/application-installations
DELETE /v1/admin/application-installations/{installation_id}
```

Lists never return client secrets, access tokens, refresh tokens, code hashes,
or service token hashes.

## Node installer

Add:

```http
POST /v1/admin/networks/{network_id}/node-installers
```

Request:

```json
{
  "name": "private-services",
  "kind": "connector",
  "install_mode": "systemd"
}
```

Map `node` to no added capability, `connector` to `subnet-router-v1`, and `exit`
to `exit-node-v1`. Issue a short-lived, single-use enrollment token. Return a
command that runs the existing managed systemd installer. Set
`Cache-Control: no-store` and never persist the plaintext token after the
response is built.

Return a stable `unsupported_mode` error for `docker_compose` until Laneway has
a persistent container, protected state volume, required network privileges,
restart policy, upgrades, health checks, and uninstall behavior.

## Error and transport rules

- Require exact callback and setup URI matching.
- Require HTTPS outside an explicit loopback development mode.
- Reject URI user information, fragments, and wildcard hosts.
- Reject duplicate form fields and conflicting client authentication methods.
- Do not reveal whether an unknown client, code, token, or disabled installation
  exists.
- Return OAuth errors with stable `error` and optional `error_description`
  fields.
- Set `Cache-Control: no-store` and `Pragma: no-cache` on code and token
  responses.
- Redact codes, secrets, tokens, authorization headers, and request bodies from
  logs.
- Use at least 256 bits of entropy for codes, client secrets, and tokens.

## Tests required before handback

Cover these cases in unit or integration tests:

- registration code expiry, replay, and wrong PKCE verifier
- exact setup and redirect URI checks
- client secret returned once and stored only as a hash
- duplicate display names
- one application installed in two networks without shared access
- exact-network denial on every management route
- duplicate authorization updates one installation
- concurrent refresh rotation and refresh-token reuse
- installation revocation without affecting another network installation
- application disable revoking all installations
- bounded expired-token cleanup below the service token limit
- installer binding to its network, node name, kind, and capabilities
- stable rejection of unsupported installer modes

Update Laneway's OpenAPI document with every new JSON and form contract. Keep
the application and installation code in generic packages. The Dispatch client
should remain only one caller of those packages.
