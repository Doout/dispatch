# Provider API v1

Infrastructure adapters run outside the controller. The HTTP/JSON contract is
`dispatch.provider/v1`. `internal/provider` contains the Go interface, bounded HTTP
client and handler, response validation, and a reusable conformance runner. The
public mock implements the contract without creating infrastructure.

The controller uses this contract for [registered providers](infrastructure-providers.md)
and [on-demand server operations](on-demand-servers.md). Private adapter source
and images can remain in their own repositories. A real cloud adapter and its
live validation are still required to allocate machines.

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

Use the [independent provider sidecar package](provider-packaging.md) for image
pinning, endpoint/credential registration, compatibility checks and rollback.

## Optional machine and disk snapshots

Snapshot-capable adapters implement the additional `SnapshotProvider` interface;
server-only adapters retain the existing interface. The manifest declares
`snapshot.create`, `snapshot.inspect`, `snapshot.delete` and, separately,
`server.restore`, with `snapshots` policy metadata. Unsupported operations return
a 422 problem before allocation. The metadata lists supported disk sets,
consistency levels, encryption modes and restore guarantees. Application-consistent
capture must include real quiescing; the mock supports crash consistency only.

| Method | Path | Result |
| --- | --- | --- |
| POST | `/v1/snapshots` | Accepted capture operation using `Idempotency-Key`. |
| GET | `/v1/servers/{id}/snapshots` | Source-scoped retained snapshot inventory. |
| GET | `/v1/snapshots/{id}` | Snapshot metadata and captured disk evidence. |
| DELETE | `/v1/snapshots/{id}` | Accepted deletion using `Idempotency-Key`. |
| POST | `/v1/snapshots/{id}/restore` | Accepted isolated clone using `Idempotency-Key`. |

Capture binds the source machine, exact disk IDs and disk-set selection,
consistency, encryption metadata and ownership labels. Inspection includes creation
time, image, source machine/SSH identities, captured disk identities, sizes,
encryption and content digests. Encryption key references are identifiers only.
Snapshot retention is independent of source-machine and clone deletion. Deleting
a snapshot must not remove disks already materialized for a clone.

Restore binds the inspected snapshot digest to a new server request and an
isolated restore policy. It creates independent disks and fresh identities; it
never modifies the source or restores over an existing machine. Before the copied
agent, workloads or network can start, the adapter must remove the copied agent
identity and private runtime journal, regenerate machine and SSH identities,
disable copied workloads and production bindings, and apply network quarantine.
Quarantine must prevent source credentials reaching the controller before that
sanitation finishes. Cloud-init alone is insufficient when an old agent can run
first. Adapters that cannot provide these guarantees must omit `server.restore`.

Server inspection returns `restore` evidence for the snapshot, preboot sanitation,
quarantine, disabled workloads/bindings and independent disks. Restored disk IDs
map to the captured disk IDs and preserve size, encryption and content digests.
`VerifyCloneEvidence` validates that evidence and distinct machine/SSH identities.
This is provider evidence, not proof that a guest booted or an application is
healthy. Controller boot verification requires fresh enrollment for the new node
and the reviewed agent artifact; application integrity remains a separate check.
Memory capture, in-place restore and cross-provider portability are not supported.

Run the additional mutation suite explicitly:

```sh
go run ./cmd/dispatch-provider-conformance --allow-mutations --snapshots
```

It allocates a separate source, captures a snapshot, restores an isolated clone,
checks replay and disk/identity evidence, and verifies independent cleanup. It
selects a network from `restore-networks` options whose metadata has
`"quarantine": true`. The public mock exposes `mock-isolated` for this purpose.
CI runs this suite without cloud credentials. Mock evidence does not establish a
real adapter's preboot behavior or prove a real VM restore.

Additional mock faults are `--disable-snapshots`, `--fail-snapshot`,
`--corrupt-snapshot`, `--fail-restore` and `--unsafe-restore`. The latter deliberately
returns copied identities and an uncleared journal; conformance must reject it.
Mock version 2 writes state format 2, preserving snapshots across restarts. It
imports format 1. Older binaries reject format 2 instead of silently losing
snapshot records. Before this format upgrade, stop the sidecar and take a private
state backup. A rollback must use a format-compatible binary; restoring an older
backup after new operations requires reconciling those operations first.
