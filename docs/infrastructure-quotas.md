# Project infrastructure quotas

Server allocation is disabled for a project until a controller owner saves its
resource policy. The policy applies to administrator, operator and automation
requests. It does not stop or resize existing servers.

In Servers, use **Project resource limits** to choose the project, maximum server
count, provider registrations, regions and machine sizes. Empty region or size
lists allow no choices. The explicit `anyRegion` and `anySize` flags allow all
choices in that category. The selected provider must also be assigned to the
project, and the caller must have permission to create infrastructure.

The API uses `GET` and owner-only `PUT` at
`/api/v1/projects/{id}/infrastructure/quota`. Include the current policy `revision`
when saving; a stale revision returns `409`. Use revision `0` for the first policy.
A missing policy reports `configured: false`, a zero server limit and no allowed
providers. Counts accept `-1` only as an explicit unlimited setting.

```json
{
  "revision": 0,
  "maxServers": 3,
  "maxTemporaryEnvironments": 0,
  "maxSnapshots": 0,
  "maxTemporaryLifetimeSeconds": 0,
  "providers": [
    {
      "providerId": "registered-provider-id",
      "regions": ["eu-1"],
      "sizes": ["small"],
      "anyRegion": false,
      "anySize": false
    }
  ]
}
```

Use identifiers returned by the selected provider. Server admission enforces the
server count and provider, region and size rules. Snapshot admission enforces
`maxSnapshots` and provider assignment; pending and unresolved captures count
until verified deletion or cancellation before submission. Isolated restores also
reserve a server slot. See [Machine snapshots](machine-snapshots.md).
Temporary-environment admission enforces `maxTemporaryEnvironments` and
`maxTemporaryLifetimeSeconds`. A nonzero allowance requires a finite maximum
lifetime, capped at 365 days. These limits do not enable unsupported provider
capabilities.

## Capacity and recovery

Dispatch reserves a server slot in the transaction that accepts its creation.
The transaction records the original operation ID and immutable project,
provider, region and size. Concurrent requests serialize against the project
policy before counting reservations. Replaying one accepted operation consumes
one slot.

Reservations have four states:

| State | Counts toward the limit | Meaning |
| --- | --- | --- |
| `reserved` | Yes | Dispatch accepted the create; allocation may be pending |
| `allocated` | Yes | The provider confirmed the owned resource |
| `unknown` | Yes | The provider's outcome needs inspection |
| `released` | No | Confirmed deletion, or cancellation before submission |

Failed enrollment and failed application deployment retain the slot. Requesting
deletion does not release it. Dispatch releases capacity only when it confirms
resource absence or proves that creation was cancelled before submission.
Uncertain operations retain their original identity and recovery records across
restart. Existing managed resources are counted during migration, including
failed or uncertain allocations.

Reducing a limit below current usage blocks additional creates and leaves running
resources intact. A quota rejection includes `quota.limit`, `maximum`, `usage` and
`requested`. Disallowed allocation choices include the rejected provider, region
and size. The inventory shows the original operation and provider resource ID so
an operator can inspect the same resource before reconciling it.

The current mock provider does not supply a price estimate. Server counts and
machine-size rules are enforced limits; they are not a bill forecast.

Temporary-environment acceptance counts the environment in the same transaction
as its generated app, deployment and receipt. Failed and cleanup-blocked
environments remain counted; completed workload cleanup releases capacity.
Extensions stay within the maximum total lifetime measured from acceptance. See
[temporary environments](temporary-environments.md).
