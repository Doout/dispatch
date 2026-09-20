# Embedded historical analytics

Dispatch keeps operational state in SQLite or PostgreSQL and runs DuckDB inside
the controller process. No Python dependency or additional service is required.
The Analytics page shows completed deployments, workflow durations, and job reuse
for 7, 30, or 90 days.

## Request performance

Operational pages never query DuckDB. Analytics requests read immutable,
precomputed summaries filtered by current project permissions. The page fetches
on navigation, range changes, and explicit refresh. It does not poll.

The worker uses one DuckDB thread, a 128 MiB database memory budget, and a 512 MiB
temporary spill limit. These are database limits, not total process memory limits.
It imports 250 events per batch and checks for completions every 15 seconds.
Initial history imports run in successive batches. During a backlog the worker
refreshes aggregates at most once every 15 seconds, then immediately after the
final partial or empty batch. This keeps full history scans out of the batch
import loop. The UI displays the last update time and whether import is still
catching up.

## Export and recovery

The transaction completing a deployment, workflow, or job also inserts a compact
outbox event. Events contain identity, project, state, timestamps, and reuse flags.
They contain no logs, credentials, configuration, build outputs, or snapshots.

The worker upserts facts into history.duckdb and writes compressed Parquet batches
under parquet/. It acknowledges exact outbox IDs only after both writes succeed.
Retries do not double-count entities. Later corrections supersede older events.
When querying Parquet directly, use the highest event_id for each kind/entity_id.

The operational migration backfills existing completed records once. Running
records enter history only when they finish. If analytics fails, deployments
continue, the outbox retains events, and the worker retries. Cached summaries
remain available with an updates-paused notice.

A missing DuckDB file is rebuilt from Parquet on startup. Back up the operational
database and the entire analytics directory together. Never delete the DuckDB
file while the controller is running.

This feature does not automatically delete operational records or Parquet files.
Old job results can still be needed for build reuse, and deployment snapshots for
rollback. Retention must account for these references before deleting data.

## Configuration

- DISPATCH_ANALYTICS_ENABLED defaults to true. Set false to pause the worker.
  Pausing does not discard the export queue, which still needs disk space.
- DISPATCH_ANALYTICS_DIRECTORY defaults to analytics beside the master key file.
  The standard Docker installation uses /data/analytics in its existing volume.

## Native builds

The controller uses the official DuckDB Go driver and requires CGO and a C++
toolchain. Build with CGO_ENABLED=1 go build ./cmd/dispatch. Linux controller
binaries require glibc and libstdc++; the Docker image provides both. Edge, relay,
and hook binaries remain pure Go. Release builds require x86_64 and aarch64 GNU
C/C++ cross-compilers, installed by the container build and CI.

The internal/analytics.Reader interface separates HTTP handlers from the embedded
engine. A future external backend can provide the same project-scoped cached
summaries without changing the UI.

## Dashboard metrics

`GET /api/v1/analytics?days=7|30|90&projectId=<optional project>` returns a cached
snapshot. Every request applies the caller's current project grants, including
revocations. An explicitly selected unreadable project returns 403. Empty grants
produce empty metrics, rankings, and coverage. No database query runs on the HTTP
request path.

The dashboard reports completed deployment, workflow, and job outcomes, daily
volume, success rates, execution duration, job reuse, failure hotspots, and slow
workloads. Current and previous periods are adjacent equal rolling windows ending
at the snapshot's `updatedAt`, expressed as `[start, end)`. A 7-day selection means
the previous 168 hours, rather than seven complete calendar dates. UTC daily bins
include partial first and last dates and zero-fill inactive dates. When updates
pause, the periods stay pinned to the last successfully published snapshot.

Success rate is `succeeded / (succeeded + failed)`. Cancelled runs remain visible
in volume and outcome charts but are excluded from this denominator. A period
with no succeeded or failed runs has a null success rate, not zero percent.

Mean, median, and p95 durations measure successful, non-reused runs with valid
start and finish timestamps. Failed, cancelled, reused, and invalid-duration runs
do not enter those duration samples. Zero-duration successful runs are valid.
The sample count accompanies every duration metric; an empty sample has null
metrics. Malformed timestamps represented by Go zero-time sentinels are excluded
from duration samples and coverage. Historical records with missing original
timestamps were backfilled using their creation time. Their recorded duration
can therefore include waiting before execution or be zero when both timestamps
were absent; the original timing cannot be reconstructed. Workflow duration is
recorded elapsed time and can include approval waiting. It is not a sum of build
or deployment stage timings. The legacy
`durationSeconds` field remains total valid elapsed time across completed outcomes;
clients should use `meanDurationSeconds` instead of dividing it by runs.

Median and p95 are estimates from mergeable logarithmic histograms, using the
nearest-rank quantile and its bucket's upper bound. Positive estimates round up
by at most 5%, with a 10ms floor for smaller nonzero samples. Zero remains zero.
Histograms merge across authorized projects before percentiles are calculated;
project percentiles are never averaged. The response advertises the method,
resolution, and duration population in `capabilities`.

Failure hotspots group retained facts by project, run kind, and recorded name,
ranked by failure count and then failure rate. Slow workloads use the same groups,
ranked by successful non-reused mean duration and then sample count. Each list
contains at most ten entries per run kind after project filtering. A recorded
name is not an immutable application identifier: renames may create separate
groups, and reused names may share a group. Only deployment groups expose a real
recorded deployment ID for navigation. Historical links can be unavailable after
operational retention removes that deployment.

`coverage.firstCompletedAt` and `coverage.lastCompletedAt` are the earliest and
latest retained completed facts in the visible projects. They describe observed
records, not a guarantee of gap-free collection. Initial catch-up and unavailable
states remain explicit. A zero-event previous window does not establish a
historical performance baseline.

The retained event schema does not contain environment associations, immutable
application identities, build-stage timings, incident records, commit lead time,
or change-failure attribution. The dashboard does not infer these from names or
present them as DORA metrics.
