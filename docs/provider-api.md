# Provider API v1

Private infrastructure adapters run outside the Dispatch controller. An adapter exposes an HTTP API that accepts and returns JSON. Dispatch stores its base URL and bearer-secret reference.

## Endpoints

- `GET /v1/manifest` returns the provider ID, API version, capabilities, and configuration JSON Schema.
- `POST /v1/validate` checks credentials and connectivity without changing provider state.
- `POST /v1/options` lists valid regions, sizes, images, and networks.
- `POST /v1/servers` requests a server and returns an operation.
- `GET /v1/operations/{id}` returns operation progress and the resulting resource ID.
- `GET /v1/servers/{id}` returns provider state.
- `DELETE /v1/servers/{id}` requests deletion.

Mutation requests require an `Idempotency-Key` header. Errors use problem details as defined by RFC 9457.

The repository does not include a mock adapter or conformance tests yet. Provider implementations and their images can remain in separate repositories.
