# Workflow workers

Hosted controllers send workflow commands to an enrolled worker. Commands, source
checkouts, build clients and their credentials run in disposable containers on
that worker host. The controller does not need a Docker socket for this path.

Create a private network through the tenant API with `driver: dispatch_agent` and
`workflowMode: tenant`. Set `workflowProjectId` to restrict the worker to one
project, or leave it empty for the tenant's projects. The creation response
contains a single-use enrollment token. `workflowMode: disabled` stops new work.
The project restriction also applies to Docker deployments, backups and other
typed runtime operations. A restricted worker can inspect storage only on targets
assigned to its project. Changing its project or disabling it stops active
operations at their next heartbeat.
Existing installations keep local execution unless hosted mode enables the
remote requirement.

Build `Containerfile.worker` and record its image digest. Install the
`dispatch-worker` binary on a separate Docker host with these settings:

```sh
export DISPATCH_EDGE_CONTROLLER_URL=https://agentops.dispatch.cicd.onl
export DISPATCH_EDGE_NODE_ID=<node-id>
export DISPATCH_EDGE_TOKEN=<single-use-enrollment-token>
export DISPATCH_WORKER_STATE=/var/lib/dispatch-worker
export DISPATCH_WORKER_IMAGE=<image>@sha256:<digest>
export DISPATCH_WORKER_MODE=tenant
```

The state directory must be private and persistent. The worker saves its identity,
receipt encryption key and operation receipts there. After enrollment it uses
short-lived sessions obtained with the saved key. Remove the enrollment token
from the service environment after the first successful connection.

Every execution container has a read-only root filesystem, private temporary
workspace, unprivileged user, no Linux capabilities, no additional privileges,
and CPU, memory, disk and process limits. Only the job's declared credentials
enter the execution request. Worker identity and session credentials remain
outside the container.

`DISPATCH_WORKER_DOCKER=true` explicitly mounts this worker host's Docker socket
into tenant execution containers for image builds and Docker deployments. Use a
host dedicated to that tenant. Docker access gives the job control over that
host's Docker daemon. The controller's socket is never used.

For managed simple jobs, register and run the node with `workflowMode: managed`
and `DISPATCH_WORKER_MODE=managed`. These jobs have no network, repository checkout
or Docker socket. They can run bounded scripts and process supplied inputs.
Managed execution cannot fall back to tenant mode. This first managed mode is
suited to validation and transformation tasks that do not need network access.

The worker defaults to one CPU, 1024 MiB memory, 2048 MiB temporary workspace and
256 processes. Set `DISPATCH_WORKER_CPUS`, `DISPATCH_WORKER_MEMORY_MIB`,
`DISPATCH_WORKER_DISK_MIB` and `DISPATCH_WORKER_PIDS` on the worker to change them.
`DISPATCH_WORKER_NETWORK` selects an isolated Docker network for tenant execution;
managed execution always uses `none`.

Workers claim one durable lease at a time and publish redacted output while jobs
run. Losing a session or lease cancels the container. Completed receipts can be
replayed after a lost response. An interrupted or missing receipt returns an
unknown outcome without repeating commands. Inspect effects before requesting a
new run. Worker identity replacement cannot receive the previous enrollment's
queued credentials.

Run the disposable-container checks with:

```sh
DISPATCH_WORKER_TEST_IMAGE=sha256:<local-image-id> go test ./internal/workflowrunner -run Integration
```

These tests create and remove their own execution containers. They do not enroll
against a deployed controller or modify its data.

To also build and run a test image through the host's Docker daemon, add
`DISPATCH_WORKER_TEST_DOCKER=true`. Use a dedicated test host. The test removes
its application container and image when it finishes.

Select managed execution on a source-free workflow job:

```yaml
jobs:
  validate:
    workerMode: managed
    run: printf 'ready\n'
```

`workerMode: tenant` selects the tenant worker pool. Omitting `workerMode` keeps
self-hosted behavior; hosted controllers default to tenant workers. A requested
worker mode never falls back to running commands on the controller.

A worker with `DISPATCH_WORKER_DOCKER=true` also processes the existing typed
runtime queue with the same enrolled identity. This preserves Docker service
provisioning, storage, retained rollback, backups and route publishing without
running a second agent service on the node. Set `DISPATCH_AGENT_ROUTING_DIRECTORY`
on that worker when its route publisher uses a local directory. Docker apps
without hooks use this typed runtime. Apps with hooks use the disposable executor;
retained runtime rollback and persistent managed routing for those apps are not
available through that executor.

Kubernetes targets require an embedded kubeconfig and a selected project with
an enrolled tenant worker. The worker inspects the target under a 90-second
request deadline. The controller rechecks the requesting owner's tenant
membership and session before dispatch and before accepting its result. Kubeconfig
exec plugins and host credential paths are rejected.

Hosted execution supports workflow commands, Git checkouts, Docker builds,
Docker deployments, Helm deployments, hooks, script service templates, Helm
service provisioning/inspection/cleanup and Kubernetes storage inspection/deletion
on workers. Storage deletion keeps the existing ownership, policy, consumer and
resource identity checks. Live Helm chart inspection, cluster topology and drift
repair still require worker operations that are not implemented. Hosted requests
for those operations fail explicitly;
they do not use a controller Kubernetes client. SSH installation, Laneway
controller installation and controller backup/restore endpoints are unavailable
within tenants. Existing self-hosted installations retain those operations.

Infrastructure provider adapters must use an enrolled tenant relay. Tenant
object backup stores use HTTPS S3 endpoints; filesystem backup destinations are
not accepted. Run the hosted controller behind an egress firewall that denies
private and platform addresses for Git HTTPS/SSH configuration fetches. Go HTTP
clients use the hosted public-address transport, but Git uses its own transport.

Private GitHub APIs, external secret stores and infrastructure provider adapters
use the HTTP relay queue. Run `dispatch-agent` under a separate node enrollment
for that queue. `dispatch-worker` polls workflow and typed runtime operations;
it does not poll HTTP relay requests. Do not run both programs with the same
node identity. A workflow worker registration cannot serve as a hosted
infrastructure provider relay.

Hosted Helm rollback, release previews and live resource diagnostics also await
worker implementations. Docker retained rollback remains available through the
typed runtime. Deployment logs and workflow logs remain available while work
runs and after it completes.

## Customer server setup

The customer server runs Docker and `dispatch-worker`. It does not need a
Dispatch console, tenant database, authoritative DNS service or public domain.
Worker management uses outbound HTTPS to the tenant's control plane.

The [systemd service](../examples/worker/dispatch-worker.service) uses a dedicated
`dispatch-worker` account with access to the local Docker daemon. Install Docker
with its Buildx and Compose plugins, plus Git, OpenSSH client and CA certificates
on the host for typed runtime operations. Install the worker binary at
`/usr/local/bin/dispatch-worker`, and build or load the matching worker image.
Keep that image locally and select its immutable ID with
`docker image inspect --format '{{.Id}}' IMAGE`.

Prepare the service on a systemd host:

```sh
sudo useradd --system --user-group --home-dir /var/lib/dispatch-worker \
  --shell /usr/sbin/nologin dispatch-worker
sudo usermod --append --groups docker dispatch-worker
sudo install -d -m 0700 /etc/dispatch-worker
sudo install -d -m 0700 -o dispatch-worker -g dispatch-worker \
  /var/lib/dispatch-worker /var/lib/dispatch-worker/tmp
sudo install -m 0600 examples/worker/worker.env.example /etc/dispatch-worker/worker.env
sudo install -m 0600 examples/worker/controller.env.example /etc/dispatch-worker/controller.env.example
sudo install -m 0644 examples/worker/dispatch-worker.service /etc/systemd/system/dispatch-worker.service
sudo systemctl daemon-reload
```

Set `DISPATCH_WORKER_IMAGE` in `/etc/dispatch-worker/worker.env` to the local
image ID. Leave the service disabled while the new control plane is being
prepared. Without `/etc/dispatch-worker/controller.env`, systemd skips startup.

Once the tenant and node registration exist, create that file from
`controller.env.example` with mode `0600`. Replace all three values with the
tenant HTTPS origin, node ID and enrollment token, then run:

```sh
sudo systemctl enable --now dispatch-worker
sudo journalctl -u dispatch-worker -f
```

Confirm that the control plane reports the worker online and can run a job.
Then remove `DISPATCH_EDGE_TOKEN` from `controller.env` and restart the service.
A running process or an `identity.json` file alone does not prove enrollment
succeeded. Keep `/var/lib/dispatch-worker` across upgrades and restarts. Its
identity and receipts prevent repeated execution after a lost connection.

The service puts temporary files under its persistent state directory so Docker
can access host paths used by runtime operations. Docker builds and containers
started through the daemon have their own resource usage; the execution
container's limits do not cap the whole host.

Public application traffic still requires reachable ingress at the deployment
target. The worker connection does not tunnel HTTP, WebSockets or other preview
traffic. Customers without inbound access will need an outbound application
tunnel through the managed gateway; that transport is not implemented yet.
