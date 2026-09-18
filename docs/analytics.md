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
Initial history imports run in successive batches. The UI displays the last
update time and whether import is still catching up.

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
