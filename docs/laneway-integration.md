# Laneway integration contract

Dispatch connects to Laneway as a network-scoped automation client. A Laneway
administrator creates or selects one network during authorization. Dispatch
then receives a revocable token that can manage nodes and routes only in that
network.

This keeps ownership clear:

- Laneway owns networks, node enrollment, routes, and route approval.
- Dispatch stores the selected network identity and an encrypted scoped token.
- A Dispatch owner can inspect that network from **Connections**.
- Access between two Laneway networks requires approval from the destination
  network. Connecting Dispatch never grants access to another network.

## Authorization flow

Dispatch opens this browser URL:

```text
GET /integrations/dispatch/authorize
    ?application_name=Dispatch
    &application_url=https%3A%2F%2Fdispatch.example.com
    &redirect_uri=https%3A%2F%2Fdispatch.example.com%2Fapi%2Fv1%2Flaneway-networks%2Fcallback
    &state=...
    &code_challenge=...
    &code_challenge_method=S256
    &permission=network.read
    &permission=node.read
    &permission=node.manage
    &permission=enrollment.issue
    &permission=route.read
    &permission=route.manage
```

Laneway must require an authenticated administrator and show a consent page.
The administrator can select an existing network or create one, review the
requested permissions, and approve or cancel. Approval returns a single-use
code to the exact callback URL:

```text
HTTP/1.1 303 See Other
Location: <redirect_uri>?code=<one-time-code>&state=<state>
```

The code must expire within ten minutes and be bound to the callback URL,
selected network, requested permissions, and PKCE challenge. It must be
invalidated on first exchange, including a failed exchange.

Dispatch exchanges the code from its backend:

```http
POST /v1/integrations/dispatch/token
Content-Type: application/json

{
  "code": "one-time-code",
  "code_verifier": "pkce-verifier",
  "redirect_uri": "https://dispatch.example.com/api/v1/laneway-networks/callback"
}
```

Laneway returns a service access token and the selected network:

```json
{
  "access_token": "lnw_spat_v1_...",
  "token_type": "Bearer",
  "expires_at_unix_seconds": 0,
  "principal_id": "...",
  "permissions": [
    "network.read",
    "node.read",
    "node.manage",
    "enrollment.issue",
    "route.read",
    "route.manage"
  ],
  "network": {
    "network_id": "...",
    "name": "Production",
    "ipv4_pool": "10.42.0.0/16",
    "ipv6_pool": "",
    "configuration_epoch": 1
  }
}
```

The service principal must have `all_networks=false` and exactly one
`network_id`. Its name should identify the Dispatch instance. Laneway should
show the principal on the selected network and let an administrator revoke it.

## API used after authorization

Dispatch uses the returned token as a bearer credential with Laneway's current
management API:

- `GET /v1/admin/networks/{network_id}`
- `GET /v1/admin/networks/{network_id}/nodes?limit=500`
- `GET /v1/admin/networks/{network_id}/endpoint-statuses?limit=500`
- `GET /v1/admin/networks/{network_id}/routes?limit=500`
- `POST /v1/admin/enrollment-tokens`
- `POST /v1/admin/networks/{network_id}/node-installers`
- `POST /v1/admin/routes/assign`
- `POST /v1/admin/routes/{route_id}/approve`
- `POST /v1/admin/routes/{route_id}/withdraw`

Dispatch reads the network inventory, requests one-time node installers, and
assigns routes. The installer response is shown once and is not stored by
Dispatch:

```http
POST /v1/admin/networks/{network_id}/node-installers

{
  "name": "vpc-node",
  "kind": "exit",
  "install_mode": "docker_compose"
}
```

Laneway returns:

```json
{
  "installation_id": "...",
  "command": "docker compose ...",
  "expires_at_unix_seconds": 1780000000
}
```

`kind` is `node`, `connector`, or `exit`. `install_mode` is
`docker_compose` or `systemd`. The command must contain a short-lived,
single-use enrollment credential bound to the selected network and requested
node role.

`bootstrap_bundle.create` is intentionally not requested. Laneway's current
bootstrap bundle is global rather than network-bound. The network-bound node
installer above is the safe replacement.

## Cross-network access

Laneway needs a separate request and approval object for routes between
networks. Do not add the destination network to the Dispatch service
principal. A minimal contract is:

```http
POST /v1/admin/network-links

{
  "source_network_id": "dispatch-network",
  "destination_network_id": "services-network",
  "prefixes": ["10.70.0.0/16"],
  "reason": "Reach deployment services"
}
```

The source-network token may create and read the request. An administrator of
the destination network must approve or reject it. Approval creates only the
requested routes and ACL rules. Revocation removes them as one operation.

Recommended operations:

- `network_link.request`, scoped to the source network
- `network_link.read`, scoped to either participating network
- `network_link.approve`, scoped to the destination network
- `network_link.revoke`, scoped to either participating network

Every state change should record the actor, both network IDs, prefixes, reason,
and resulting configuration epochs in the audit log.

## Laneway implementation handoff

1. Add the authorization and code-exchange endpoints above.
2. Reuse the existing service-principal and access-token storage. Do not create
   a parallel credential type.
3. Add a table for short-lived authorization codes containing only a hash of
   the code plus the bound request fields.
4. Add the consent screen to the Laneway web app with network selection and
   network creation.
5. Add the network-bound node-installer endpoint. Reuse enrollment tokens, but
   return only a generated command and never persist the plaintext token.
6. Add tests for callback allowlisting, PKCE mismatch, expiry, replay,
   cancellation, one-network scope, and token revocation.
7. Add the cross-network link object only after the base authorization flow is
   stable. Keep destination approval mandatory.
