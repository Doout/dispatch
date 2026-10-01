# Provider API v1

Infrastructure adapters run outside the controller. The HTTP/JSON contract is
`dispatch.provider/v1`. `internal/provider` contains the Go interface, bounded HTTP
client and handler, response validation, and a reusable conformance runner. The
public mock implements the contract without creating infrastructure.

Provider registration, credential storage and controller reconciliation are
separate work. This package does not connect the controller's Add server action
to a cloud API. Private adapter source and images can remain in their own repos.

## HTTP contract

Every endpoint accepts a bearer credential when authentication is configured.
Use verified HTTPS for remote adapters. HTTP is available for a local sidecar or
isolated test network. The client accepts an injected transport for direct/private
connections, never follows redirects, and limits bodies to 1 MiB. Credentials
belong in the Authorization header, never endpoint URLs. The mock reads
`DISPATCH_PROVIDER_TOKEN`; leaving it unset disables authentication for local tests.

| Endpoint | Request | Success response |
| --- | --- | --- |
| `GET /v1/manifest` | No body | `200` manifest |
| `POST /v1/validate` | `{"config":{}}` | `200 {"valid":true}` |
| `POST /v1/options` | `{"kind":"regions","config":{}}` | `200 {"items":[{"id":"...","name":"..."}]}` |
| `POST /v1/servers` | Create-server object | `202` operation |
| `GET /v1/operations/{id}` | No body | `200` operation |
| `GET /v1/servers/{id}` | No body | `200` server |
| `DELETE /v1/servers/{id}` | No body | `202` operation |

POST requests use `application/json`. Validation and option discovery do not
allocate resources. Option kinds are `regions`, `sizes`, `images` and `networks`;
items have stable nonempty IDs and display names. Providers can use configuration
to resolve dependent options.

A manifest has `apiVersion`, stable provider `name`, `displayName`, implementation
`version`, `capabilities`, and `configurationSchema`. The lifecycle suite requires
`server.create`, `server.inspect` and `server.delete`. Additional capabilities do
not imply support in an older controller. Registration must reject unsupported
API versions before enabling mutations. The client validates identity and
object-schema structure; adapters enforce their complete configuration schema
through `/v1/validate`. The mock advertises JSON Schema 2020-12 with an optional
`testLabel` string.

Create-server fields are `name`, `region`, `size`, `image`, `network`, `sshKey`,
optional `bootstrap`, and `providerConfig`. The mock does not run bootstrap text,
use SSH keys or contact a host, and never persists those inputs. Real adapters
must keep bootstrap material and credentials out of responses, logs and fixtures.

Operations contain `id`, `state`, optional sanitized `message`, and `resourceId`.
States are `pending`, `running`, `succeeded`, `failed` and `cancelled`. Intermediate
states can be skipped but cannot move backwards. Terminal states and identities
remain stable. Success must identify the resource, including after deletion.
Server inspection returns `id`, `name`, `address`, `state` and optional `labels`;
creation exposes a `ready` server. An absent resource returns a 404 problem.

### Mutation identity and errors

Create and delete require one `Idempotency-Key`. Keys and resource/operation IDs
use 1-128 ASCII letters, digits, dots, underscores, colons or hyphens and start
with a letter or digit. Retrying the same request returns the same operation and
resource IDs. Reusing a key with a different payload, action or target returns
409. Providers retain these identities for their documented replay window,
including completed operations. The mock keeps them until its state is removed.
Repeating deletion of an absent resource succeeds.

Errors use `application/problem+json` and RFC 9457 fields `type`, `title`, `status`
and optional `detail`. Body status matches HTTP status. Unknown internal errors
become a generic 500 problem. The client rejects malformed JSON, oversized
responses, changed operation IDs, unknown states and invalid problems without
including raw bodies in transport/decoding errors. Reconcile uncertain mutations
using the original request key.

## Run the mock and conformance suite

Run the adapter in one terminal:

```sh
go run ./cmd/dispatch-provider-mock --state /tmp/dispatch-provider-mock/state.json
```

Run the suite in another:

```sh
go run ./cmd/dispatch-provider-conformance \
  --endpoint http://127.0.0.1:8091 \
  --request examples/providers/mock-request.json \
  --allow-mutations
```

The suite prints JSON and exits nonzero on failure. It checks manifests, schema
structure, configuration/options, asynchronous creation, concurrent replay,
conflicting payloads, state transitions, inspection, deletion and replay after
deletion. It attempts cleanup after failure and reports uncertain outcomes or
failed cleanup for inspection. Use a disposable account and fixture for a real
adapter. The runner replaces the fixture's server name with a unique test name and
reads bearer credentials from `DISPATCH_PROVIDER_TOKEN`. It does not print the
fixture or upstream bodies. Real-provider request files stay outside this repo.

`--timeout` bounds the lifecycle and a separate failure-cleanup attempt;
`--poll-interval` controls polling. Increase them for slow providers. The Go
`RunConformance` function also accepts an implementation of the Provider interface.

### Restart and failure tests

The mock atomically saves an optional private state file before acknowledging
operations. Restart with the same `--state` path to retain identities and polling
progress. Run one mock process per state file. Without this flag, restarting
creates a fresh in-memory fixture.

| Flag | Behavior |
| --- | --- |
| `--polls N` | Complete after N operation polls; default 2, maximum 1000. |
| `--fail-create` | Accept creation, then reach a stable failed operation. |
| `--fail-delete` | Fail deletion while retaining an inspectable server. |
| `--fault unavailable` | Return a 503 problem before work. |
| `--fault malformed` | Return invalid JSON before work. |
| `--fault incompatible` | Advertise an unsupported API version. |

Tests cover restart boundaries, authentication, limits, conflicting identities,
redirects, corrupt state, malformed schemas and broken idempotency:

```sh
go test -race ./internal/provider/... ./cmd/dispatch-provider-mock ./cmd/dispatch-provider-conformance
python3 scripts/check-provider-fixtures.py
```

## Container and CI

```sh
docker build -f Containerfile.provider-mock -t dispatch-provider-mock:test .
docker run --rm -p 127.0.0.1:8091:8091 dispatch-provider-mock:test
docker run --rm --network host \
  --entrypoint /usr/local/bin/dispatch-provider-conformance \
  dispatch-provider-mock:test --allow-mutations
```

The container runs as UID 65532, includes both commands and keeps state in `/data`.
Mount a dedicated volume there for restart tests. Host networking above assumes
Linux; elsewhere use a Docker network and pass the mock container's endpoint.

Provider CI runs race tests, vet, a public-fixture credential-format check and the
packaged lifecycle suite without provider credentials. On main it publishes
amd64/arm64 images as `ghcr.io/doout/dispatch-provider-mock:main` and
`sha-<commit>` using the repository workflow token. Consumers should pin the
published digest. Private adapter images stay in their own repositories.

GitHub initially gives new container packages private visibility. On first
publication a package administrator must set this mock package to Public. CI
checks anonymous manifest access and fails with an explanation until that setting
is in place. See [GitHub's container registry documentation](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

The fixture scan catches common private-key, token and credential-bearing URL
formats. Review must also reject provider-specific private identifiers and
credential formats the scan does not recognize. Public examples use mock values.

Controller registration, capability approval and credential rotation are documented in [Infrastructure providers](infrastructure-providers.md).

### Ownership evidence

Adapters used for managed server creation advertise `server.ownership` and
accept `CreateServerRequest.labels`. They preserve every supplied Dispatch label
on the resource returned by inspection: `dispatch.provider-registration`,
`dispatch.project`, `dispatch.server`, and `dispatch.request`. These labels bind
reconciliation and adoption to one reviewed intent; resource names or public IPs
alone are insufficient. Deletion inspection must provide authoritative absence
with HTTP 404. Controller lifecycle details are in
[On-demand servers](on-demand-servers.md).
