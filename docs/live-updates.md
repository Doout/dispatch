# Overview updates

The UI loads `/api/v1/overview` once, retaining its `X-Overview-Version` header. It then opens an authenticated fetch stream at `/api/v1/overview/watch` with that version in `Last-Event-ID`.

The stream sends only `patch` events containing a base version, a resulting version, and JSON Patch operations (`add`, `remove`, `replace`, `move`). Object changes include only changed fields. Arrays of records are aligned by ID, so inserting, deleting, or moving a record does not resend unchanged records. There is no initial snapshot event and no application data while idle. Small SSE comments keep the connection alive every 30 seconds.

The browser applies each patch atomically to its retained overview and checks the base version before applying it. Explicit overview refreshes replace the baseline and restart the stream; reconnects use the latest applied version. Hidden tabs close their connection. Authentication or impersonation changes discard the old baseline immediately.

The server retains recent permission-filtered baselines for ten minutes, bounded to 64 entries and 64 MiB of serialized data (a single oversized baseline is retained). Baselines are scoped to the bearer credential and impersonated account. A reconnect sends only the changes since the requested baseline. If a baseline has expired, been evicted, or been lost in a restart, the server responds with HTTP 409; the client makes one ordinary overview fetch and opens a new versioned stream. The stream never falls back to sending a full snapshot.

SQLStore broadcasts committed writes. Handlers batch bursts for 500 ms and compare the same filtered view used by the ordinary overview endpoint. Every 30 seconds they also revalidate authentication and check for writes outside the process. Failed writes and rolled-back transactions do not notify subscribers. Proxies must allow streaming; responses include `X-Accel-Buffering: no`.

Failed connections retry with exponential backoff from one to 30 seconds, plus jitter. Authentication failures use the existing login/account recovery flow. Deployment logs are fetched every three seconds while the selected deployment is active and visible; finished deployments fetch logs once.
