# Install and upgrade Dispatch

The managed installer runs the controller in Docker on Linux (amd64 or arm64). It requires a local Docker engine and Compose 2.30 or newer. It does not install Docker or manage Kubernetes.

## Get the CLI

For a public release:

```sh
curl -fsSL https://raw.githubusercontent.com/Doout/dispatch/main/scripts/install.sh | sh
sudo dispatch install
```

The download script verifies the release archive against `SHA256SUMS` before installing the CLI. Set `DISPATCH_VERSION=v0.1.0` to select a published release, or `DISPATCH_BIN_DIR` to choose its destination. Re-running the script updates the CLI binary. It does not start or upgrade a controller.

For a private repository, authenticate with GitHub CLI and download the release assets with `gh release download v0.1.0 --repo Doout/dispatch --pattern 'dispatch-linux-*.tar.gz' --pattern SHA256SUMS`. Run `sha256sum -c SHA256SUMS`, extract the archive for your architecture, and install its `dispatch` binary into `/usr/local/bin`. Authenticate to GHCR with `docker login ghcr.io` before installing a private image. The curl bootstrap above requires public release assets.

To build from this checkout and use a local image:

```sh
go build -o /tmp/dispatch ./cmd/dispatch
docker build -f Containerfile -t dispatch:local .
sudo /tmp/dispatch install --image dispatch:local --pull=false
```

`dispatch install` creates a managed installation under `/opt/dispatch`, using the `dispatch` Compose project and `dispatch-data` volume. It starts the controller on `127.0.0.1:8080`. Open that address to create the owner account; use an SSH tunnel when accessing a remote server. The Docker socket group is detected automatically.

An existing Compose project or data volume with the same name is never adopted or overwritten. The repository's older `compose.yml` installation remains supported separately. Use a different name, directory, and port for a new managed installation; do not point it at an existing production data volume.

```sh
sudo dispatch install --name dispatch-test --dir /opt/dispatch-test --port 8081
```

The installer requires the selected Docker engine to match `--docker-socket`. If using another local socket, set `DOCKER_HOST=unix:///path/to/docker.sock` and pass `--docker-socket /path/to/docker.sock`. Remote Docker contexts are not supported because backups and bind mounts use local paths.

## Configuration

Copy optional settings into the managed installation with `--env-file`. Each line is a literal `KEY=value` entry; do not add shell quotes or variable substitutions. The file is copied with mode `0600` and can be edited at `/opt/dispatch/dispatch.env` afterward. It can contain administrator credentials, provider credentials, and other controller settings.

The managed controller uses SQLite in its data volume. The installer fixes the database location, master-key path, Docker socket, listener, executor, repository-cache location, and public URL so the upgrade backup includes the database and encryption key. These values override entries in the environment file. External PostgreSQL installations are not managed by this installer.

For an existing Traefik installation:

```sh
sudo dispatch install \
  --public-url https://dispatch.example.com \
  --proxy-network web \
  --hostname dispatch.example.com \
  --tls-resolver letsencrypt
```

The proxy network and certificate resolver must already exist. Without proxy flags, no Traefik labels or external network are required. You can also use another reverse proxy forwarding to the loopback port.

The generated `compose.json` is managed by the CLI. It refuses upgrades if that file was manually changed. To apply an environment-file edit, run `dispatch recover` when there is no interrupted upgrade; this reconciles and starts the existing pinned image.

## Upgrade

```sh
sudo dispatch status
sudo dispatch upgrade --check
sudo dispatch upgrade
```

The default channel is `ghcr.io/doout/dispatch:stable`. An installation follows the image reference selected at install time. A version tag stays on that version until changed:

```sh
sudo dispatch upgrade --image ghcr.io/doout/dispatch:v0.2.0
sudo dispatch upgrade --image ghcr.io/doout/dispatch:stable
```

`--check` resolves the available image without stopping the controller. If the image ID is unchanged, a regular upgrade also leaves the controller running. `--pull=false` uses an image already present on the host.

An upgrade pulls the image before stopping the controller. It then archives the stopped controller's entire data volume, including SQLite and the master key, and starts the new image by its immutable local ID. It commits the new version only after `/healthz` succeeds. Registry failures leave the controller running. A failed replacement restores both the old image and the pre-upgrade data, including changes made by unsuccessful database migrations.

There is a maintenance interruption during backup and replacement. Plan manual upgrades outside active deployments; the installer does not drain deployment jobs. Data backups remain under `/opt/dispatch/backups/` and are not pruned automatically. Protect and back up the installation directory as well, since it contains configuration and potentially credentials.

A process or host interruption leaves an upgrade journal. Resume recovery before trying another upgrade:

```sh
sudo dispatch recover
```

Commands lock the installation directory so manual and automatic upgrades cannot run together. `recover` restores the previous version when replacement was interrupted, or completes cleanup if the new version was already committed.

## Optional automatic upgrades

Automatic upgrades are disabled by default. The updater runs in a separate Docker container:

```sh
sudo dispatch auto-upgrade enable
sudo dispatch auto-upgrade status
sudo dispatch auto-upgrade disable
```

The updater checks daily with up to 30 minutes of randomized delay, using the same backup, health-check, and rollback flow as a manual upgrade. It persists the next check time across container and host restarts. An interrupted upgrade is recovered before waiting for the next check. It follows the installation's configured image tag; use `stable` to follow stable releases. `--interval` changes the schedule, and `--pull=false` supports locally supplied images.

Disabling automatic upgrades stops and removes only the updater container. The controller stays running. If an upgrade currently holds the installation lock, retry the disable command after it finishes.

The controller and updater both run in Docker. No systemd service, timer, or cron job is created. The updater container is named `NAME-updater` and uses `restart: unless-stopped`. It runs independently of the controller so replacing the controller does not stop the upgrade process. View its logs with `docker logs dispatch-updater` and its schedule or last error with `dispatch auto-upgrade status`.

The updater mounts the local Docker socket and the managed installation directory at the same absolute path it has on the host. This lets Docker create backup helper containers with the correct host paths. It uses host networking to check the controller's loopback health endpoint. Its root filesystem is read-only, with a temporary `/tmp` filesystem. The updater runs as root inside its container to manage the installation files.

For a private registry, supply `--registry-config /path/to/docker-config` when enabling updates. This mounts that directory read-only and uses its `config.json`. Host credential helpers are not available inside the updater; use a dedicated Docker configuration containing registry credentials. Public images need no credentials.

The updater uses the currently installed controller image, which includes the CLI. Run `auto-upgrade enable` again after an upgrade to recreate the updater with the newer image. The controller's Compose file remains independent of this worker, so controller replacement leaves the worker running.

## Releases

The release workflow runs for stable `vMAJOR.MINOR.PATCH` tags. It tests the code, publishes amd64 and arm64 controller images to GHCR with both the version and `stable` tags, then publishes Linux CLI archives and `SHA256SUMS` as GitHub release assets. The GHCR package must permit public pulls for unauthenticated installation; private installations can use `docker login ghcr.io` first. No release is created merely by building the source locally.

Docker documents the raw environment-file format in its [Compose environment guide](https://docs.docker.com/compose/how-tos/environment-variables/set-environment-variables/). The image publishing workflow follows GitHub's [container publishing guide](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images).

## Validation performed

The installer tests cover registry and backup failures, rollback, interrupted-upgrade recovery, ownership checks, concurrent commands, and updater container enable/disable. An isolated Docker check also exercises a fresh install, a healthy upgrade, and a failed image that overwrites data before exiting. Recovery restores the previous image, data, and encryption key and brings the controller back to health.
