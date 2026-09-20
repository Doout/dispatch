# Application observations

Dispatch records a runtime observation after a successful deployment. Runtime drift still compares the saved successful release with live resources. Workload health reports supported Kubernetes readiness separately. An unsupported resource kind, denied permission, missing release baseline or unreachable target remains explicit; a connection attempt does not make it healthy.

Application status refreshes inline every 15 seconds. Its Details panel contains **Checks and notifications**, the current observation age, optional public endpoint checks and recent notification outcomes. Old observations become stale after the configured threshold. Observation times describe what the Dispatch controller could see at that moment.

Project viewers can read settings and sanitized results. Project admins and operators can configure checks and run them manually. Deployers do not gain configuration permission. All background work rechecks the application's project against the saved settings.

## Optional scheduling

Scheduling is off until enabled for an application. Choose an interval from 60 seconds to 24 hours and a stale threshold at least as long as that interval, up to seven days. Dispatch runs at most two observations at once and one per application. A deployment or runtime operation takes precedence; checks wait until the application is idle.

Repeated unsuccessful observations increase the next interval exponentially, capped at 24 hours. A healthy observation resets this backoff. Controller restarts resume saved schedules. A successful deployment missed during a restart receives a check on startup if its revision has no saved observation. Checks never repair drift, restart containers or deploy a release.

## Public endpoint and TLS checks

Configure the final public HTTP or HTTPS endpoint, such as `https://app.example.com/health`. Dispatch makes an unauthenticated GET with a ten-second timeout, verifies HTTPS certificate trust and hostname, and reports HTTP status, certificate expiry and duration. It does not store response bodies. Reading stops at 64 KiB; response headers are limited to 32 KiB.

The endpoint cannot include credentials, a query string or a fragment. Dispatch does not forward cookies or credentials, use environment proxies, or follow redirects. Hostnames are resolved and all returned addresses checked immediately before dialing; local, private, link-local, metadata and special-use destinations are refused. Private network probes are outside this public endpoint check.

The result is labeled **Dispatch controller**. Reachability from the controller does not prove that a workload can reach a service or that users in another network can reach the application.

## Notification delivery

Notifications are off by default. A project operator can explicitly configure a public HTTPS webhook and enable delivery. The webhook address may contain a delivery token, so Dispatch encrypts it with the vault and makes it write-only. Leaving it out of an update preserves it; explicit replacement and removal are available. Saving does not send a message.

Events include changes to observed state, recovery, deployment failures and recovery after a failed deployment. Unchanged observed states do not generate repeated events. A mute window suppresses delivery until its end, up to 30 days. Notifications contain sanitized status and deployment links, never credential fields or runtime response bodies.

Delivery has a persistent outbox and retries five times with exponential delays. A delivery includes an `Idempotency-Key` equal to the stable event ID. Receivers should deduplicate this key: a controller crash after remote acceptance but before recording the acknowledgment can cause a retry. Disabling delivery or changing its configuration cancels pending events from the previous configuration. Muted events remain in history and are not replayed after the mute ends.

## API

- `GET /api/v1/apps/{id}/observations` returns configuration, observation, `freshness`, `checking` and the latest 30 events.
- `PUT /api/v1/apps/{id}/observations` updates configuration. Include the last read `revision` to prevent overwriting concurrent edits.
- `POST /api/v1/apps/{id}/observations/check` runs a manual runtime and endpoint observation.

Example configuration:

```json
{
  "revision": 0,
  "scheduled": true,
  "intervalSeconds": 300,
  "staleAfterSeconds": 900,
  "endpointUrl": "https://app.example.com/health",
  "notificationsEnabled": false
}
```

`webhookUrl` is accepted only on writes. `removeWebhook: true` removes its encrypted value and requires notifications to be disabled. `mutedUntil` accepts a future RFC 3339 timestamp. Each edit advances the configuration revision; results from an older configuration are not shown as current.
