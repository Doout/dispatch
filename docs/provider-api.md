# Provider API v1

Private infrastructure adapters run outside the public core and expose a small HTTP/JSON contract. Dispatch registers an adapter by base URL and bearer-secret reference.

## Endpoints

- `GET /v1/manifest` — identity, API version, capabilities, and configuration JSON Schema.
- `POST /v1/validate` — validate credentials and connectivity without mutation.
- `POST /v1/options` — list dynamic regions, sizes, images, and networks.
- `POST /v1/servers` — idempotently request a server and return an operation.
- `GET /v1/operations/{id}` — inspect asynchronous progress and resulting resource identity.
- `GET /v1/servers/{id}` — read provider state.
- `DELETE /v1/servers/{id}` — idempotently request deletion.

Mutation requests require `Idempotency-Key`. Errors use RFC 9457-style problem details. A mock adapter and conformance tests are planned for the public core; the internal-cloud adapter and its image remain private.
