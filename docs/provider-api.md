# Provider API v1

Private infrastructure adapters run outside the public core and expose a small HTTP/JSON contract. Dispatch registers an adapter by base URL and bearer-secret reference.

## Endpoints

- `GET /v1/manifest` returns identity, API version, capabilities, and the configuration JSON Schema.
- `POST /v1/validate` checks credentials and connectivity without changing state.
- `POST /v1/options` lists regions, sizes, images, and networks.
- `POST /v1/servers` requests a server and returns an operation.
- `GET /v1/operations/{id}` returns progress and the resulting resource identity.
- `GET /v1/servers/{id}` returns provider state.
- `DELETE /v1/servers/{id}` requests deletion.

Mutation requests require `Idempotency-Key`. Errors use RFC 9457-style problem details. A mock adapter and conformance tests are planned for the public core; the internal-cloud adapter and its image remain private.
