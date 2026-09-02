# Laneway application integration

Laneway exposes applications and network installations. It does not contain
Dispatch-specific routes, models, scopes, or UI. Dispatch is one client of the
generic protocol and supplies its name in the application manifest.

The model has two parts:

- An application registration identifies a client, its exact callback URLs,
  and the maximum scopes it may request.
- An application installation grants that application access to one Laneway
  network. One application can have several installations.

Laneway owns networks, node enrollment, routes, consent, and revocation.
Dispatch stores the application identity, network installation identity, and
encrypted credentials returned by Laneway. Connecting one network does not
grant access to any other network.

## Register an application

Dispatch submits an application manifest to Laneway:

```http
POST /applications/new
Content-Type: application/x-www-form-urlencoded

manifest=<json>&state=<state>&code_challenge=<challenge>&code_challenge_method=S256
```

The decoded manifest is:

```json
{
  "name": "Dispatch",
  "homepage_uri": "https://client.example.com",
  "setup_uri": "https://client.example.com/api/v1/laneway-applications/setup",
  "redirect_uris": [
    "https://client.example.com/api/v1/laneway-networks/callback"
  ],
  "scopes": [
    "network.read",
    "node.read",
    "node.manage",
    "enrollment.issue",
    "route.read",
    "route.manage"
  ],
  "token_endpoint_auth_method": "client_secret_basic"
}
```

Laneway requires an authenticated administrator, shows the application name,
callbacks, and requested scopes, then asks for approval. Approval redirects to
the exact `setup_uri` with a single-use code and the original state.

Dispatch exchanges that code from its backend:

```http
POST /v1/application-registrations/exchange
Content-Type: application/json

{
  "code": "one-time-code",
  "code_verifier": "pkce-verifier"
}
```

The response contains the client secret once:

```json
{
  "application_id": "app_...",
  "client_id": "client_...",
  "client_secret": "secret_...",
  "name": "Dispatch"
}
```

The code must expire within ten minutes. It must be bound to the manifest,
setup URI, state, and PKCE challenge, and invalidated on its first exchange.
The exchange response must use `Cache-Control: no-store`.

## Install the application into a network

After registration, or when reusing an application registered with the same
Laneway authority, Dispatch starts a standard OAuth authorization code flow:

```text
GET /oauth/authorize
    ?response_type=code
    &client_id=client_...
    &redirect_uri=https%3A%2F%2Fclient.example.com%2Fapi%2Fv1%2Flaneway-networks%2Fcallback
    &scope=network.read%20node.read%20node.manage%20enrollment.issue%20route.read%20route.manage
    &state=...
    &code_challenge=...
    &code_challenge_method=S256
```

Laneway asks an administrator to select or create one network and approve the
requested scopes. Approval returns a single-use authorization code to the
registered callback URL.

Dispatch exchanges the code with HTTP Basic client authentication:

```http
POST /oauth/token
Authorization: Basic base64(client_id:client_secret)
Content-Type: application/x-www-form-urlencoded

grant_type=authorization_code&code=...&code_verifier=...&redirect_uri=...
```

Laneway returns the network installation and rotating credentials:

```json
{
  "access_token": "access_...",
  "token_type": "Bearer",
  "expires_in": 3600,
  "refresh_token": "refresh_...",
  "scope": "network.read node.read node.manage enrollment.issue route.read route.manage",
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

Access tokens should expire after one hour. Refresh tokens should rotate on
use and expire after 30 to 90 days. Revoking an installation invalidates its
access and refresh tokens without affecting other installations.

Each installation can use an existing service principal with
`all_networks=false` and exactly one network ID. The application and
installation remain generic Laneway resources.

## Network management API

Dispatch uses the installation access token with these existing Laneway APIs:

- `GET /v1/admin/networks/{network_id}`
- `GET /v1/admin/networks/{network_id}/nodes?limit=500`
- `GET /v1/admin/networks/{network_id}/endpoint-statuses?limit=500`
- `GET /v1/admin/networks/{network_id}/routes?limit=500`
- `POST /v1/admin/networks/{network_id}/node-installers`
- `POST /v1/admin/routes/assign`

The node installer endpoint accepts:

```json
{
  "name": "vpc-node",
  "kind": "exit",
  "install_mode": "docker_compose"
}
```

`kind` is `node`, `connector`, or `exit`. `install_mode` is
`docker_compose` or `systemd`. The response contains a short-lived, single-use
command bound to the selected network and node role. Dispatch shows the
command once and does not store it.

## Cross-network access

Network linking is separate from application installation. A client with
access to one network cannot grant itself access to another network. A source
network can request a link, but an administrator for the destination network
must approve it.

```http
POST /v1/admin/network-links

{
  "source_network_id": "network_a",
  "destination_network_id": "network_b",
  "prefixes": ["10.70.0.0/16"],
  "reason": "Reach deployment services"
}
```

Approval creates only the requested routes and access rules. Revocation removes
them as one operation. Audit records must include the actor, both network IDs,
prefixes, reason, and resulting configuration epochs.

## Laneway implementation handoff

1. Add generic `Application` and `ApplicationInstallation` resources. Do not
   add product-named resource types or endpoints.
2. Add `POST /applications/new` and the application registration consent UI.
3. Add `POST /v1/application-registrations/exchange`. Store only a hash of
   each one-time code.
4. Add `/oauth/authorize`, `/oauth/token`, and `/oauth/revoke` using exact
   redirect URI matching, PKCE, HTTP Basic client authentication, and rotating
   refresh tokens.
5. Let an administrator select or create one network during installation.
6. Back each installation with a one-network service principal. Reuse the
   current principal and access-token storage instead of creating a parallel
   permission system.
7. Add the network-bound node installer endpoint if it is not already present.
8. Reject expired tokens during authentication and clean them with a bounded,
   indexed maintenance query. Avoid deleting expired tokens in an unbounded
   insert-path transaction.
9. Test redirect allowlisting, PKCE mismatch, expiry, replay, cancellation,
   one-network scope, refresh rotation, installation revocation, and multiple
   installations for one application.
