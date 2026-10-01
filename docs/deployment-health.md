# Deployment health policies

Each accepted deployment captures its application's health policy. Changing the
application later changes future deployments, not historical results or a running
candidate's checks. Policy results appear as `health` on deployment API records,
including summaries. Simulation evidence sets `simulated: true`.

Use `GET /api/v1/apps/{id}/health-policy` to read the policy. Project members with
configuration permission can update it while the application is idle:

```http
PUT /api/v1/apps/{id}/health-policy
Content-Type: application/json

{
  "timeoutSeconds": 120,
  "checkTimeoutSeconds": 5,
  "intervalSeconds": 2,
  "failureThreshold": 3,
  "checks": [
    {"id": "ready", "kind": "http", "scope": "workload", "port": 8080, "path": "/ready"},
    {"id": "certificate", "kind": "tls", "scope": "certificate"}
  ]
}
```

Application creation also accepts `healthPolicy`. Omitted values use the defaults
shown above. Timeout ranges from 1 to 600 seconds, check timeout and interval from
1 to 60 seconds, and failure threshold from 1 to 20. A policy permits at most 16
configured checks. HTTP paths cannot include queries, fragments or credentials.

Workload readiness is always required. Docker checks that each candidate container
is running and that its Docker health check, when configured, reports healthy.
Additional checks support HTTP responses in the 200–299 range and TCP connections.
A Compose check can select a service with `service`; select a service when more
than one workload exposes a checkable endpoint. An unavailable endpoint or unknown
service blocks promotion.

A `route` HTTP check contacts the candidate with the application's domain as its
Host header. A `certificate` TLS check verifies the public domain's certificate
chain, validity period and hostname. Those are separate requirements: successful
container readiness does not satisfy either check. Certificate checks need the
hostname and certificate to be prepared before candidate publication. Redirects
and insecure TLS verification are not accepted as successful checks.

The checker retries complete rounds, so a workload that becomes unhealthy while
another check is pending cannot reuse an earlier passing result. Consecutive
failures reach the configured threshold; the overall timeout bounds all rounds.
Cancellation, timeout, failure and unavailable checks have distinct saved states.
Evidence includes check identity, attempts, consecutive failures and HTTP status.
Response bodies, headers, arbitrary command output and raw network errors are
excluded. Expensive workflow QA remains opt-in and is not reported as completed by
these checks.

## Runtime behavior

Managed Docker routing checks isolated candidates before publication. Failed
checks leave the previous route serving the previous candidate. The routing driver
must use `CheckCandidateHealth` before its atomic promotion step. Private workloads
without a managed proxy receive real readiness checks but retain their existing
Docker/Compose replacement behavior; fixed host ports do not provide overlapping
traffic. A failed private deployment is never reported as successfully live.

Helm HTTP/TCP policy checks become native Kubernetes readiness probes in the saved
release manifest. `service` selects a container name for Helm. Each selected
container accepts one configured HTTP or TCP probe; conflicting probes or an
unmatched selector fail rendering before the release is applied. Explicit policy
probes replace that container's chart readiness probe. Helm uses atomic wait with
the policy timeout; chart rollout strategies and chart-owned ingress still govern
traffic. Certificate checks run before chart application. Native readiness failure
is recorded without storing raw Helm output.

Rollback retains the original deployment's health policy. It does not replace it
with the application's current settings. Policy-driven rollback after an already
promoted release is outside this capability.

Remote runtime drivers run checks at the target and forward the structured health
result to the controller. They must persist that result before reporting successful
promotion. The same checker and policy are used by local Docker and the enrolled
runtime agent.
