# Laneway application contract

This contract covers a generic application that manages resources in Laneway.
Dispatch is one consumer. Laneway must not contain Dispatch-specific routes,
resource types, scope bundles, or UI.

Audit basis:

- Dispatch commit `e29ff81`
- Laneway commit `577d259`

Laneway already has network-scoped service principals, hashed service access
tokens, and operation checks on the management API. It does not yet have
registered applications, application installations, OAuth authorization,
refresh tokens, or a node-installer API. Dispatch has the start of the browser
flow, but its persistence and callback handling need changes before the flow is
safe to run in production.

## Decisions

1. A registered application is reusable across networks on one Laneway
   authority.
2. An application installation grants access to one network.
3. Every installation owns one service principal with
   `all_networks=false` and one network ID.
4. Registered scopes are a ceiling. An installation may receive a subset.
5. Laneway uses its current management API and authorization engine. A second
   application-only management API is not needed.
6. Client credentials do not grant network access. Installation tokens do.
7. Network creation runs under the Laneway administrator who approves an
   installation. It is not an application permission.
8. Cross-network routing is a separate feature. Installing an application in
   one network never lets it join or grant access to another network.
9. Application names are display values and do not need to be unique.

## Resources

### Registered application

Laneway stores:

- application ID
- display name
- homepage URI
- exact setup URI
- exact redirect URI allowlist
- maximum scope set
- client authentication method
- enabled state
- client credential hashes
- creator and audit timestamps

Laneway returns a client secret only when it creates or rotates that secret.
It stores a purpose-separated hash, not the plaintext secret.

### Application installation

Laneway stores:

- installation ID
- application ID
- network ID
- service principal ID
- granted scope set
- installer administrator ID
- enabled state
- creation, update, and revocation timestamps

There can be one active installation for each application and network pair.
Reauthorizing that pair updates the existing installation instead of creating
a duplicate.

### Dispatch application connection

Dispatch stores one application record for each registered Laneway client:

- local ID and display name
- Laneway authority
- application ID
- client ID
- encrypted client secret
- state and timestamps

Several network connections may reference this record. Removing the last
network connection must not remove the application or its client secret.

### Dispatch network connection

Dispatch stores:

- local ID and display name
- application connection ID
- installation ID
- network ID and cached network metadata
- encrypted access and refresh tokens
- granted scopes
- access-token expiry
- state and verification timestamps

The application client secret does not belong in this record.

### Dispatch authorization transaction

Dispatch stores each registration or installation transaction in shared
storage:

- hash of the state value
- encrypted PKCE verifier
- transaction kind
- Laneway authority
- requested local name
- application connection ID when installing
- exact callback URI
- initiating Dispatch user ID
- expiry and consumed timestamp

The transaction expires within ten minutes and is consumed atomically. It must
survive a Dispatch restart and work when more than one Dispatch replica serves
callbacks.

## Application registration

Dispatch starts a GitHub App manifest-style browser flow:

```http
POST /applications/new
Content-Type: application/x-www-form-urlencoded

manifest=<json>&state=<state>&code_challenge=<challenge>&code_challenge_method=S256
```

Example decoded manifest:

```json
{
  "name": "Example deployment controller",
  "homepage_uri": "https://client.example.com",
  "setup_uri": "https://client.example.com/api/v1/laneway-applications/setup",
  "redirect_uris": [
    "https://client.example.com/api/v1/laneway-networks/callback"
  ],
  "scopes": [
    "network.read",
    "node.read",
    "enrollment.issue",
    "route.read",
    "route.manage"
  ],
  "token_endpoint_auth_method": "client_secret_basic"
}
```

The cross-site POST is untrusted input. Laneway size-limits and rate-limits it,
stores a short-lived pending request, and redirects to a same-origin page. An
authenticated administrator reviews the application name, callbacks, and
scope ceiling. The approval mutation requires the Laneway session, same-origin
validation, and CSRF protection.

Approval creates the application and redirects to the exact setup URI:

```text
https://client.example.com/api/v1/laneway-applications/setup
  ?code=registration_code
  &state=client_state
```

Cancellation uses the same setup URI with `error=access_denied`, an optional
`error_description`, and the original state.

Dispatch verifies state, then exchanges the code from its backend:

```http
POST /v1/application-registrations/exchange
Content-Type: application/json

{
  "code": "registration_code",
  "code_verifier": "pkce_verifier"
}
```

Laneway generates the client secret during the exchange and returns it once:

```json
{
  "application_id": "app_...",
  "client_id": "client_...",
  "client_secret": "secret_...",
  "name": "Example deployment controller"
}
```

The registration code is single-use, expires within five minutes, and is bound
to the application, setup URI, and PKCE challenge. The response includes:

```http
Cache-Control: no-store
Pragma: no-cache
```

Redirect and setup URIs require exact matching. Production URIs must use HTTPS,
must not contain user information or fragments, and must not use wildcard
hosts. A development build may allow loopback HTTP callbacks through an
explicit setting.

## Network installation

Dispatch reuses a saved application for the selected Laneway authority and
starts an authorization code flow:

```text
GET /oauth/authorize
    ?response_type=code
    &client_id=client_...
    &redirect_uri=https%3A%2F%2Fclient.example.com%2Fapi%2Fv1%2Flaneway-networks%2Fcallback
    &scope=network.read%20node.read%20enrollment.issue%20route.read%20route.manage
    &state=...
    &code_challenge=...
    &code_challenge_method=S256
```

Laneway authenticates an administrator and lists only networks that person may
manage. A user with `network.create` may create a network before approval. The
consent page shows the selected network and each requested scope.

Approval redirects to the exact registered callback URI with a single-use
authorization code and the original state. Dispatch verifies state and
exchanges the code with HTTP Basic client authentication:

```http
POST /oauth/token
Authorization: Basic base64(client_id:client_secret)
Content-Type: application/x-www-form-urlencoded

grant_type=authorization_code&code=...&code_verifier=...&redirect_uri=...
```

The response identifies the installation and network:

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

The authorization code expires within five minutes. Laneway checks the client,
exact redirect URI, PKCE challenge, selected network, and approved scopes before
consuming it.

## Scopes

The first Dispatch integration requests only the operations used by its UI:

| OAuth scope | Laneway operation | Use |
| --- | --- | --- |
| `network.read` | `network.read` | Read the selected network |
| `node.read` | `node.read` | List nodes and endpoint status |
| `enrollment.issue` | `enrollment.issue` | Create one-time node enrollment |
| `route.read` | `route.read` | List routes |
| `route.manage` | `route.manage` | Assign routes through a selected node |

`node.manage` is not part of the first grant because Dispatch does not revoke
nodes or change their capabilities. Add it later through a new consent step if
the UI gains those actions.

The scope rules are:

- requested scopes are a subset of the registered scope ceiling
- granted scopes are a subset of the request
- the approving administrator must hold every granted operation on the
  selected network
- broader scopes require new consent
- the installation service principal is limited to the selected network
- the service principal never receives `network.create`,
  `service_principal.manage`, or another global administrator operation

Laneway service principal permissions are currently immutable. If granted
scopes change, Laneway creates a replacement principal, swaps it into the
installation, and revokes the old principal and token family in one
transaction.

Laneway adds these operations to its existing route policy:

| Operation | Scope | Default human roles |
| --- | --- | --- |
| `application.read` | global | owner |
| `application.manage` | global | owner |
| `application_installation.read` | network | owner, operator, auditor |
| `application_installation.manage` | network | owner, operator |

Approving a new application requires `application.manage`. Installing or
revoking an application requires `application_installation.manage` on the
selected network. The approver must also hold every operation represented by
the granted OAuth scopes. Internal creation of the installation service
principal does not grant the approver general `service_principal.manage`
access.

## Token lifecycle

Access tokens expire after one hour. Refresh tokens expire after 30 days and
rotate on every use. Reusing a replaced refresh token revokes that token family.

```http
POST /oauth/token
Authorization: Basic base64(client_id:client_secret)
Content-Type: application/x-www-form-urlencoded

grant_type=refresh_token&refresh_token=...
```

Laneway may keep the current `lnw_spat_v1` access-token format and service
principal authentication path. Each refresh revokes the previous access token.
Laneway must also remove or revoke expired service access tokens with a bounded,
indexed maintenance query. Otherwise the current limit of 100 unrevoked tokens
per principal will block a long-running installation.

Token, code, and client-secret records use separate hash purposes. Laneway never
stores their plaintext values. Token responses include `Cache-Control: no-store`
and `Pragma: no-cache`.

Revocation uses client authentication:

```http
POST /oauth/revoke
Authorization: Basic base64(client_id:client_secret)
Content-Type: application/x-www-form-urlencoded

token=...&token_type_hint=refresh_token
```

Revoking an installation disables its service principal and revokes all access
and refresh tokens in one transaction. Disabling an application revokes every
installation for that application. Rotating a client secret does not revoke
installations unless an administrator requests both actions.

Dispatch serializes refreshes for each installation. The storage update uses a
transaction or compare-and-swap on the previous refresh token so two Dispatch
replicas cannot use a rotating token at the same time.

## Errors

Authorization and registration callbacks return `error`,
`error_description`, and the original `state`. Token and registration exchange
endpoints return the same error fields as JSON. Use stable codes such as
`invalid_request`, `invalid_client`, `invalid_grant`, `invalid_scope`,
`access_denied`, and `temporarily_unavailable`.

Do not reveal whether an unknown code, client, refresh token, or disabled
installation exists. Management API errors keep Laneway's current error
envelope. Dispatch may log a local correlation ID, but it must not copy secrets
or raw exchange bodies into logs or browser query parameters.

## Management API

Installation access tokens call the existing Laneway management API:

- `GET /v1/admin/networks/{network_id}`
- `GET /v1/admin/networks/{network_id}/nodes?limit=500`
- `GET /v1/admin/networks/{network_id}/endpoint-statuses?limit=500`
- `GET /v1/admin/networks/{network_id}/routes?limit=500`
- `POST /v1/admin/routes/assign`

The `/v1/admin` prefix identifies Laneway's management surface. It does not
mean every caller is a root administrator. The current route policy already
authenticates service access tokens, resolves the canonical network from the
path, body, or referenced object, and checks the service principal on every
request. Application tokens should use that path.

Laneway also needs administrator endpoints for application management:

```text
GET    /v1/admin/applications
GET    /v1/admin/applications/{application_id}
POST   /v1/admin/applications/{application_id}/client-secrets
POST   /v1/admin/applications/{application_id}/disable
GET    /v1/admin/networks/{network_id}/application-installations
DELETE /v1/admin/application-installations/{installation_id}
```

Client-secret creation returns the secret once. Application and installation
lists never return secret material.

## Node installers

Dispatch currently calls this endpoint, but it does not exist in the audited
Laneway revision:

```http
POST /v1/admin/networks/{network_id}/node-installers
```

This should be a generic Laneway API because Laneway owns its package names,
supported deployment modes, enrollment format, and node capabilities. Dispatch
should display the returned command or files without assembling Laneway
internals.

Request:

```json
{
  "name": "private-services",
  "kind": "connector",
  "install_mode": "systemd"
}
```

`kind` maps to existing Laneway enrollment settings:

| Kind | Enrollment class | Capabilities |
| --- | --- | --- |
| `node` | `durable` | none |
| `connector` | `durable` | `subnet-router-v1` |
| `exit` | `durable` | `exit-node-v1` |

The endpoint issues a short-lived, single-use enrollment token and returns an
installer that is bound to that token, network, name, and capability set.

```json
{
  "installation_id": "installer_...",
  "command": "...",
  "expires_at_unix_seconds": 1780000000
}
```

Laneway currently supports managed Linux installation through
`laneway node install` and systemd. `docker_compose` must not be advertised
until Laneway implements and tests a persistent node container, protected state
volume, required network privileges, restart policy, upgrade path, status
checks, and uninstall path. The endpoint should return `unsupported_mode` for
an unavailable mode.

The response and any downloadable bootstrap material use
`Cache-Control: no-store`. Dispatch shows the command once and does not persist
the enrollment token.

## Cross-network access

Cross-network links are not part of the first application milestone. The
audited Laneway revision has no network-link API, and Dispatch must not assume
one exists.

When this feature is added, a source installation may submit a request, but it
cannot approve the destination side. An administrator with authority over the
destination network must approve the exact prefixes and policy. Revocation must
remove the resulting routes and access rules as one operation. Audit records
must include both networks, the actor, prefixes, reason, and resulting
configuration epochs.

## Dispatch changes

The current Dispatch implementation needs these changes before release:

1. Add a `laneway_applications` store and reference it from Laneway network
   connections. Stop finding reusable client credentials by scanning network
   records.
2. Move the client secret out of each network credential bundle. Keep only the
   installation access and refresh tokens there.
3. Store registration and authorization transactions in the database. Replace
   the process-local `lanewayStates` map.
4. Build setup, callback, and return URLs from the configured public URL. Do
   not derive them from `Host`, `X-Forwarded-Proto`, or another request header.
   Production deployments must set `DISPATCH_PUBLIC_URL` to one HTTPS origin.
5. Protect rotating refresh tokens with a per-installation transaction or
   compare-and-swap update.
6. Request the five scopes listed above. Remove `node.manage` until Dispatch
   exposes node management.
7. Revoke the Laneway installation before deleting a network connection. Keep
   the saved application available for other networks.
8. Add explicit application removal. Block it while network installations
   reference it unless the user confirms that every installation will be
   revoked.
9. Treat the node-installer endpoint as unavailable until Laneway ships it.
   Show a clear capability error instead of forwarding an unexplained 404.
10. List Laneway applications and their installed networks in the connection
    UI. Adding another network should start at the saved application.
11. Restrict application registration to Dispatch owners. Canonicalize and pin
    the approved Laneway authority. Server-to-server registration and token
    requests must not follow redirects or forward credentials to another
    authority.

Suggested Dispatch tables:

```text
laneway_applications
laneway_authorization_transactions
```

Add `laneway_application_id` to `private_networks`. Enforce unique local records
for `(laneway_application_id, installation_id)` and for
`(laneway_application_id, network_id)`.

## Laneway changes

Laneway needs these generic resources and handlers:

1. Add registered application, redirect URI, scope, client credential,
   registration code, installation, installation scope, authorization code,
   and refresh-token-family records.
2. Add the registration start, consent, setup callback, and exchange flow.
3. Add OAuth authorize, token, refresh, and revoke handlers.
4. Create a one-network service principal during installation and map scopes to
   the existing operation matrix.
5. Add application and installation management pages and API handlers.
6. Add bounded cleanup for expired authorization codes, registration codes,
   refresh tokens, and service access tokens.
7. Add the generic node-installer API. Ship systemd first. Add Docker Compose
   only after the node container lifecycle is complete.
8. Record the application, installation, network, human approver, service
   principal, granted scopes, credential rotation, refresh reuse, and
   revocation in the audit log.

Recommended Laneway tables:

```text
registered_applications
registered_application_redirect_uris
registered_application_scopes
registered_application_credentials
application_registration_codes
application_installations
application_installation_scopes
application_authorization_codes
application_refresh_token_families
application_refresh_tokens
```

## Security requirements

- Use at least 256 bits of entropy for codes, client secrets, and tokens.
- Store only purpose-separated hashes of Laneway-issued secrets.
- Encrypt Dispatch client secrets and installation credentials with distinct
  record-bound contexts.
- Require PKCE S256 for registration and installation.
- Consume codes atomically and reject replay.
- Compare callback URIs exactly after validation at registration time.
- Reject duplicate form fields and conflicting authentication methods.
- Limit request bodies, callback values, names, URI counts, and scope counts.
- Rate-limit unauthenticated registration starts and token exchanges.
- Redact codes, tokens, client secrets, and form bodies from logs.
- Treat Laneway authorities as owner-managed outbound destinations. Keep the
  canonical scheme, host, port, and optional base path fixed for every request.
- Do not follow redirects for code exchange, token, refresh, revoke, or
  management API calls.
- Recheck application, installation, principal, token, and network state on
  every management call.
- Deny a token if the network in the route does not match its installation.
- Use transactions for consent, principal replacement, token rotation, and
  revocation.
- Keep registration and OAuth errors generic when more detail would expose a
  client, code, or token.

## Delivery order

1. Add Laneway application and installation storage with unit tests.
2. Add Laneway registration and consent.
3. Add Laneway authorization, token rotation, revocation, and service principal
   binding.
4. Add Dispatch application and authorization-transaction storage.
5. Switch Dispatch callbacks and refresh handling to the new storage.
6. Connect Dispatch inventory and route calls to installation tokens.
7. Add the Laneway systemd node-installer endpoint and enable it in Dispatch.
8. Add the management UI on both sides.
9. Add Docker Compose node installation as a separate Laneway milestone.
10. Design cross-network approval after the single-network flow is stable.

## Acceptance tests

The integration is ready when automated tests prove all of the following:

- registration survives a Dispatch restart between start and callback
- registration works with two Dispatch replicas
- a registration code cannot be replayed or exchanged with the wrong verifier
- callback and setup URI matching is exact
- a forged host or forwarding header cannot change a Dispatch callback URI
- the client secret is returned once and stored encrypted by Dispatch
- two applications may use the same display name
- one application can be installed in two networks without sharing access
- a token for one network is denied on every route for another network
- an installer token is bound to its network, node name, and capability set
- duplicate authorization for the same application and network does not create
  a second active installation
- concurrent refresh requests leave one valid token family
- refresh-token reuse revokes the family
- revoking one installation leaves other installations active
- deleting the last Dispatch network keeps the saved application
- removing an application revokes its installations and credentials
- expired-token cleanup remains bounded and does not exhaust the principal token
  limit
- unsupported installer modes return a stable capability error
