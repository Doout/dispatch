# Install a hosted release

The hosted release includes the controller, DNS server, worker, certificate
installer and edge agents for Linux amd64 and arm64. Run the controller under
its own Unix account. DNS replicas and tenant workers have separate accounts
and state directories. A worker with Docker access belongs on a dedicated worker
host, because jobs can control that host's daemon.

Use Debian 12 or a compatible Linux distribution with systemd and glibc. The
controller requires `ca-certificates`, `git`, `openssh-client` and `libstdc++6`.
Worker hosts require Docker Engine and the Docker CLI. PostgreSQL, the HTTPS
proxy and SMTP are configured separately.

## Download and verify

Choose an explicit published release. Do not use a moving `stable` or `latest`
image tag in a saved deployment configuration. Run these commands in an empty
working directory with the GitHub CLI installed:

```sh
DISPATCH_RELEASE=v1.2.3 # Replace with the release being installed.
DISPATCH_ARCH=amd64   # Use arm64 on ARM hosts.
gh release download "$DISPATCH_RELEASE" --repo Doout/dispatch \
  --pattern "dispatch-platform-linux-$DISPATCH_ARCH.tar.gz" \
  --pattern dispatch-hosted-config.tar.gz --pattern SHA256SUMS --pattern images.txt
sha256sum --check --ignore-missing SHA256SUMS
sudo install -d -m 0755 "/opt/dispatch/$DISPATCH_RELEASE"
sudo tar -xzf "dispatch-platform-linux-$DISPATCH_ARCH.tar.gz" -C "/opt/dispatch/$DISPATCH_RELEASE"
sudo tar -xzf dispatch-hosted-config.tar.gz -C "/opt/dispatch/$DISPATCH_RELEASE"
```

The archive checksums cover the binaries, configuration bundle and image manifest.
`images.txt` records full `image:version@sha256:digest` references for the legacy
controller, hosted controller, DNS server and worker execution image. Save it
alongside the installation's version and configuration. Pull the worker image
before accepting jobs, and put that exact reference in `DISPATCH_WORKER_IMAGE`.

For an unreleased build, record the source commit, build with
`scripts/build-release.sh <commit>`, and run `python3 scripts/check-release.py release`.
Build each container with its `release` target to package those same binaries.
An unreleased local image can use its full `docker image inspect` ID as the worker
image pin. It must exist on that worker's Docker daemon.

## Configure the controller

The files in `deploy/hosted` are service templates. They do not initialize a
database, request certificates or change DNS delegation. Complete the settings
in [Hosted tenants](hosted-tenants.md) before starting them.

1. Create a `dispatch-platform` system user with no login shell. Create
   `/etc/dispatch-platform` and `/var/lib/dispatch-platform`, owned by that user
   with mode `0700`.
2. Copy `platform.env.example` to `/etc/dispatch-platform/platform.env` and set
   the public domain, console IPs, nameservers and credential-file paths. The
   example uses HTTPS termination on the same host and listens on loopback.
   The proxy must preserve Host, forward streaming responses without buffering,
   and set trusted client-address headers. Use the TLS setup in
   [Hosted tenants](hosted-tenants.md#tls-bootstrap-and-renewal).
3. Put the catalog URL, provisioner URL and DNS token in separate files owned by
   `dispatch-platform` with mode `0600`. Use a dedicated catalog database and
   provisioner credentials that can create the isolated tenant roles and
   databases. These credentials must never go into a tenant's environment.
4. Point `/opt/dispatch/current` at the selected release directory. The service
   uses that path for its executable and downloadable edge agents.
5. Bootstrap the first account before enabling the controller service. Run
   `dispatch-platform bootstrap-user` as `dispatch-platform`, supplying
   `DISPATCH_HOSTED_DATA_DIR` and `DISPATCH_HOSTED_CATALOG_URL_FILE` from the
   configuration. Keep the initial password in a private file and remove it
   after the administrator has signed in.
6. Install `dispatch-platform.service` in `/etc/systemd/system`. Run
   `systemctl daemon-reload`, then `systemctl enable --now dispatch-platform`.

The controller unit cannot access the host's Docker socket. It has write access
to its own state directory and private temporary files. Do not add the controller
account to the `docker` group.

## Configure DNS replicas

On each nameserver, create a `dispatch-dns` system user. Copy `dns.env.example`
to `/etc/dispatch-dns/dns.env` and install the matching service unit. Give that
account a private copy of the DNS feed token and a private state directory at
`/var/lib/dispatch-dns`. The URL must point to the root console's
`/api/v1/internal/dns/snapshot` endpoint over trusted HTTPS.

The unit grants only the capability needed to bind port 53. Open both UDP and
TCP 53. Check authoritative answers directly against each replica before adding
NS delegation or changing glue records. Replicas retain their last accepted
snapshot when the controller is unavailable. Follow [Authoritative DNS](authoritative-dns.md)
for delegation and bootstrap details. Configure the workload certificate installer
using [Certificate installation](certificate-installation.md).

## Configure tenant workers

Install a worker on a separate Docker host, using the same release archive and
`/opt/dispatch/current` layout. Create a `dispatch-worker` system account, private
`/etc/dispatch-worker` and `/var/lib/dispatch-worker` directories, and install
`dispatch-worker.service`. The unit joins the host's `docker` group to create
execution containers, including managed containers without network access.

Copy `worker.env.example` to `/etc/dispatch-worker/worker.env`. Set the tenant
HTTPS origin, enrolled node ID, one-use enrollment token and pinned execution
image. The environment file must have mode `0600`. Pull the image on this worker
host before starting the unit. Remove the enrollment token from the file after
the first successful connection.

`DISPATCH_WORKER_DOCKER=false` keeps the socket out of execution containers.
Set it to `true` only on a host dedicated to that tenant when builds or Docker
runtime operations need it. The controller always runs elsewhere. For simple
managed tasks, select managed mode in both the enrollment and worker settings.
See [Workflow workers](workflow-workers.md) for supported operations and limits.

## Upgrade and recovery

Keep each installed release in its own versioned directory. Stop the controller,
back up the catalog, all tenant databases, service configuration and the complete
controller state directory together, then change the `current` symlink and
restart. Preserve DNS snapshots and worker identity state as well. A previous
binary may not understand a newer database schema. A rollback may require
restoring the matching backups instead of changing only the symlink.

Check the root and tenant consoles, tenant authorization, authoritative DNS over
both transports, a worker job and its live logs, cancellation, and certificate
installation after each upgrade. Rehearse a restore on a separate installation
before relying on the backups for recovery.
