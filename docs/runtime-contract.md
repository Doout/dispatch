# Runtime contract v1

`dispatch.runtime/v1` describes the behavior of a configured executor on a
specific target. The provider API creates infrastructure; this contract runs
workloads on it. A provider capability does not imply runtime support.

`GET /api/v1/contracts/runtime` lists the operation vocabulary and links to
`GET /api/v1/servers/{id}/capabilities?buildType=dockerfile`. The target endpoint
uses the same access check as its topology. It accepts `dockerfile`, `compose`
or `helm`, reports the configured driver and target mode, and supplies an explicit
supported flag and reason for every operation. Simulation identifies itself as
simulation. Clients must not interpret the vocabulary as enabled capabilities.

## Operations

| Operation | Required behavior |
| --- | --- |
| deploy | Apply one accepted application specification and source revision to its owned target. Record progress and execution evidence. |
| inspect | Observe owned workload state without changing it. Distinguish absence from an unavailable runtime. |
| logs | Read bounded, scoped logs without changing resources or returning credentials. |
| start / stop | Change only the identified owned workload. Repetition converges on the requested state. |
| rollback | Restore a retained immutable release through a new deployment record and a current reviewed confirmation. Preserve application data. |
| destroy | Remove only owned disposable resources. Preserve protected storage, and report partial cleanup. Repetition tolerates already absent owned resources. |

The initial common executors advertise deploy and destroy. Inspect, logs,
start, stop and rollback are rejected by the common dispatcher until the selected
driver implements them. Existing deployment logs, diagnosis and reviewed release
rollback remain separate APIs. Their existence does not make those operations
available through every runtime driver.

Docker currently requires a local target. Remote Docker targets advertise no
supported operations until enrolled remote execution is configured. Helm uses
the Kubernetes API. It is tested by the existing Helm tests; the broader shared
Kubernetes conformance work follows the Simulation/Docker foundation.

## Acceptance and immutable source

The live controller resolves a configured Git branch before writing the accepted
deployment. An explicitly supplied revision must be a full hexadecimal commit ID.
A missing revision or `HEAD` resolves the configured branch. Inline Compose uses
its saved specification digest; a repository-free Helm source uses chart identity.

Docker fetches the accepted commit into a detached checkout and verifies `HEAD`
before building. It never substitutes a newer branch head when the accepted
revision cannot be fetched. Source resolution failure creates no deployment.
Simulation does not contact the user's repository.

Application specification, target and resolved service inputs remain bound by
the existing deployment acceptance checks. Retained Docker/Compose artifacts
record immutable image identities and the inputs used for rollback. A source
commit, retained image and database backup represent different things.

## Failure, cancellation and recovery

Capabilities and cancellation are checked before source credentials or runtime
mutations. The shared error vocabulary distinguishes invalid requests, unsupported
operations, ownership conflicts, concurrent-operation conflicts, cancellation,
deadline expiry, runtime unavailability, uncertain outcomes and execution failure.
Errors may retain a local cause without serializing command output or credentials.

The controller persists a deployment and holds its application mutation lock
before execution. Existing leases identify interrupted work. A cancellation or
timeout does not prove the runtime made no changes; inspect the recorded target
before retrying an uncertain operation. Recovery must retain the original
operation identity and must not blindly repeat uncertain side effects. Public API
request receipts are separate from runtime resource identity.

## Conformance and compatibility

`internal/runtimecontract/conformance.Run` supplies reusable capability,
cancellation and repeated lifecycle cases. Simulation and Docker run the same
applicable cases in `internal/deploy/runtime_contract_test.go`; Docker's portable
suite uses a deterministic command fixture. The source test uses a real local Git
repository and advances its branch after acceptance. Live Docker tests remain
separate and must use disposable resources.

Each new driver must declare every v1 operation, reject unsupported operations
before mutation, and run the applicable shared suite plus its own ownership,
restart, immutable artifact and runtime integration tests. Optional operations
may be added without changing v1 semantics. An incompatible contract requires a
new version; unknown versions cannot enable mutations.
