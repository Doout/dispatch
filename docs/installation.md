# Install and upgrade Dispatch

The managed installer runs the controller in Docker on Linux (amd64 or arm64). It requires a local Docker engine and Compose 2.30 or newer. It does not install Docker or manage Kubernetes.

## Connect a new server to Doodship

In Doodship, create an instance and open its connection page. Choose **Set up server** and copy the install command. Run it on your Linux server. Use Linux amd64 or arm64 with access to a local Docker engine at `/var/run/docker.sock`. The installer needs 2 GiB free in Docker storage and 512 MiB free on your home filesystem. Builds and application data need additional space. Docker Desktop and remote engines are not supported.

Setup uses the existing Docker installer. It creates the controller, a local owner account, and an outbound connection to Doodship. The owner who generated the token is linked to that local account. Remote access is an explicit option in the connection page. Turn it off and provide your installation's own address to keep browser access on your LAN or VPN. Webhook forwarding still uses the outbound connection.

The default directory is `$HOME/.doodship`. It contains Compose configuration, installation state, a CLI at `bin/dispatch`, and the generated recovery login in `local-owner.json`. The database, master key, analytics, and job data stay in the `doodship-data` Docker volume. Export and import use the same installer commands described below.

The installer container runs as root to manage Docker. The supplied command passes your host UID and GID so installation files belong to you. Manage the installation with:

```sh
"$HOME/.doodship/bin/dispatch" status
"$HOME/.doodship/bin/dispatch" upgrade
"$HOME/.doodship/bin/dispatch" export --output "$HOME/doodship-export.tar.gz"
```

The installed CLI finds its configuration directory automatically. Upgrades replace it with the binary from the new controller image. The host binary requires glibc and libstdc++6. On hosts without those libraries, run management commands through the controller image with the installation directory and Docker socket mounted at their original paths and host networking enabled, as in the setup command.

Setup enables daily automatic updates through a separate Docker updater. Both services restart after a host reboot. No systemd, cron, or host service is installed. Automatic upgrades use backups, health checks, and rollback. They can interrupt running jobs. Disable them with `auto-upgrade disable`, or pass `--auto-update=false` in the setup command.

The command downloads `/install.sh` from your control plane using curl. It executes the script after a successful download and passes connection JSON on standard input. Downloads have time and size limits, with two retries for temporary failures.

The script checks host tools, Linux and Docker architectures, directory ownership and available disk space. It pulls a fresh image, pins its image ID, and tests the image's Docker client and Compose against the local engine. A private temporary copy of the CLI checks host library compatibility. The script removes that copy on success or failure.

The installer image supplies Compose. Setup writes `$HOME/.doodship/compose.json` and starts the controller and updater. You do not need to download a Compose file.

The one-use token passes through standard input. It is not stored in the download URL, Docker arguments, or container environment. The copied command does contain it, so keep the command private. After enrollment, the controller stores the replacement connection credential privately in its data volume.

If connection or updater setup fails, run setup again with a current token and the same directory and name. Setup resumes its own installation, preserves the local owner, and skips enrollment when that instance is already connected. It can recreate a missing controller container while preserving its database and connection. It rejects unrelated containers, edited Compose configuration, and a missing data volume from a completed installation. An interrupted export or upgrade needs `dispatch recover` first. Existing installations should use **Settings → Connections → Doodship** instead.

To change the default port, add `--port 8081` after `sh -c "$installer" --` in the copied command, before its heredoc. Use `--name example` to select another Compose project. `--pull=false` uses a local image, and `DOODSHIP_IMAGE` selects another controller image. Digest-pinned images require `--auto-update=false`. The script owns its installation directory, socket and host ownership settings; those cannot be changed with CLI flags.

The script contains named stages for options, tools, platform, Docker, storage, image checks and setup. Add host prerequisites there. Keep account creation, data ownership, recovery and update logic in `internal/installation` so the script and CLI use the same rules. Connection JSON goes only to the final command; probes never read it.

Setup follows `ghcr.io/doout/dispatch:main`. Successful CI on the current `main` commit publishes amd64 and arm64 images before advancing this tag. The `stable` channel remains tied to versioned releases. Change channels with `dispatch upgrade --image ghcr.io/doout/dispatch:stable` when that release contains the setup support you need.

## Get the CLI

For a public release:

```sh
curl -fsSL https://raw.githubusercontent.com/Doout/dispatch/main/scripts/install.sh | sh
sudo dispatch install
```

The download script verifies the release archive against `SHA256SUMS` before installing the CLI. Set `DISPATCH_VERSION=v0.1.1` to select a published release, or `DISPATCH_BIN_DIR` to choose its destination. Re-running the script updates the CLI binary. It does not start or upgrade a controller.

For a private repository, authenticate with GitHub CLI and download the release assets with `gh release download v0.1.1 --repo Doout/dispatch --pattern 'dispatch-linux-*.tar.gz' --pattern SHA256SUMS`. Run `sha256sum -c SHA256SUMS`, extract the archive for your architecture, and install its `dispatch` binary into `/usr/local/bin`. Authenticate to GHCR with `docker login ghcr.io` before installing a private image. The curl bootstrap above requires public release assets.

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

## Move a self-hosted installation

Export the managed installation on the old server:

```sh
sudo dispatch export --output /root/dispatch-export.tar.gz
```

Copy the archive to the new server, install the CLI and Docker, then import it:

```sh
sudo dispatch import --input /root/dispatch-export.tar.gz
```

Sign in with your existing account. The archive includes the SQLite database, vault key, literal environment file and everything stored in the controller data volume, including analytics, repository caches and local backup files. Import verifies database integrity and checks that the vault key can decrypt saved credentials before creating Docker resources.

Export stops the controller while copying its data, then restarts it before checking and packaging the archive. Wait for active builds and deployments to finish before exporting. For a permanent move, stop the old controller before starting the imported copy so both installations cannot schedule the same work. Redirect DNS and webhooks to the new server after it is ready. Update OAuth callback registrations if the hostname changes.

Import creates a new installation identity and data volume. It never adopts or overwrites an existing installation, Compose project or volume. To test on the same host, choose a separate directory, name and port:

```sh
sudo dispatch import \
  --input /root/dispatch-export.tar.gz \
  --dir /opt/dispatch-copy \
  --name dispatch-copy \
  --port 8081 \
  --public-url https://dispatch-new.example.com
```

The usual install options also set the Docker socket, bind address and Traefik route. Host settings come from these options rather than the old server. The archived environment file is restored as-is, so check any server-specific addresses in it. Agents may need their controller URL updated. Re-enable automatic upgrades separately if you want them on the new server.

### Export an existing Compose installation

For the repository's older Compose setup, select the running controller container:

```sh
sudo dispatch export --container dispatch-dispatch-1 --output /root/dispatch-export.tar.gz
```

The CLI uses the local Docker engine to stop the controller, copy its data and restart it. It preserves the SQLite database, master key, analytics, logs, cached repositories, local backups and literal container environment. Mounted configuration directories such as `/kubeconfigs` are included. Import restores them at their original container paths, with their original read-only setting, using subdirectories in the new data volume. The old host paths and Compose project are not adopted.

The controller must publish its HTTP port locally and keep its SQLite database and master key at `/data/dispatch.db` and `/data/dispatch-master.key`. Additional mounts must be configuration directories. Sockets, external PostgreSQL databases and system paths cannot be exported this way. Importing mounted configuration requires a Docker engine that supports volume subpaths. [Docker volume subpaths](https://docs.docker.com/engine/storage/volumes/#mount-a-volume-subdirectory).

Run the same command through SSH when exporting another server:

```sh
ssh root@old-server 'dispatch export --container dispatch-dispatch-1 --output /root/dispatch-export.tar.gz'
scp root@old-server:/root/dispatch-export.tar.gz ./dispatch-export.tar.gz
```

The administrator needs Docker access on the source. The CLI does not expose a database or key download endpoint in the application API. Concurrent container exports share a Docker lock, and `dispatch recover --dir DIRECTORY` restarts a controller after an interrupted export. Use the same CLI directory for export and recovery.

Check the archive before importing it:

```sh
dispatch verify --input ./dispatch-export.tar.gz --dir ./dispatch-verification
sudo dispatch import --input ./dispatch-export.tar.gz --public-url https://dispatch-new.example.com
```

Verification checks checksums, safe archive paths, database integrity and schema compatibility, and decrypts saved credentials with the archived master key. It runs offline without starting a controller or contacting deployment targets. Archives with mounted configuration use format 2 and require a CLI that supports it; format 1 archives remain supported.

When the source image has a registry digest, import uses that exact image. For a local image, copy or rebuild it on the destination and use `--image IMAGE --pull=false`. Use `dispatch upgrade --image ghcr.io/doout/dispatch:stable` afterward to follow stable releases. Moving between CPU architectures requires an image published for both architectures; architecture-specific caches may need rebuilding.

If an export or import is interrupted, run `dispatch recover --dir DIRECTORY`. An unfinished import retains its staged data and blocks upgrades until recovery completes. A failed health check also leaves the installation available for recovery.

The archive contains the vault key and configuration secrets. It has mode `0600` and is not encrypted. Import only archives you trust. Transfer them over a trusted connection and protect them like database credentials. The archive format currently accepts up to 100 GiB of data and one million entries. Unsafe paths, links outside the data volume, device files, duplicate entries, incompatible manifests and checksum mismatches are rejected.

This flow supports installations created by `dispatch install` and compatible self-hosted Compose controllers selected with `--container`. External PostgreSQL databases and hosted tenant conversion require a separate migration. Docker images, application containers and application volumes that are not mounted in the controller are not copied. Keep deployment targets and external secret stores available, or reconnect them before running deployments on the new server.

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

The release workflow runs for stable `vMAJOR.MINOR.PATCH` tags. It requires successful main-branch CI for the exact tagged commit, reuses that run's tested frontend, publishes amd64 and arm64 controller images to GHCR with both the version and `stable` tags, then publishes Linux CLI archives and `SHA256SUMS` as GitHub release assets. The GHCR package must permit public pulls for unauthenticated installation; private installations can use `docker login ghcr.io` first. No release is created merely by building the source locally.

Docker documents the raw environment-file format in its [Compose environment guide](https://docs.docker.com/compose/how-tos/environment-variables/set-environment-variables/). The image publishing workflow follows GitHub's [container publishing guide](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images).

## Build caching

CI runs eight balanced API race shards and six balanced shards for every other discovered Go package. Coverage checks require every test root to execute exactly once across each matrix. PostgreSQL and Docker fixtures, frontend checks, security checks and release builds run in separate jobs. Each test job has a ten-minute limit, with an eight-minute Go test timeout. The existing `test` status requires every validation job to succeed. Superseded CI, provider and secret-scan runs on the same pull request or branch are cancelled. Cancelled race jobs skip artifact uploads and coverage processing. Provider conformance jobs also have a ten-minute limit.

CI and releases share a Go module and compiled-package cache. Race and release compiler caches refresh per commit, with one writer per cache family. Main-branch CI builds both architectures so subsequent release tags can reuse that cache. Release tags reuse completed main CI instead of repeating the race suite. If CI has not passed for the tagged commit or its frontend artifact has expired, the release fails before publishing; complete or rerun main CI first. Release images package the same binaries as the CLI archives; they do not compile Go again inside Docker. Docker runtime layers are also cached for both architectures. Local controller, agent, and relay image builds use BuildKit cache mounts for package downloads and compiled output.

Dockerfile application deployments keep an immutable image tag for each deployment and a stable, per-application cache tag. A rebuild imports the last successful cache tag and replaces it only after the new image builds successfully. Compose rebuilds use Docker's project and service image cache on the same Docker daemon.

## Validation performed

The installer tests cover registry and backup failures, rollback, interrupted-upgrade recovery, ownership checks, concurrent commands, and updater container enable/disable. An isolated Docker check also exercises a fresh install, a healthy upgrade, and a failed image that overwrites data before exiting. Recovery restores the previous image, data, and encryption key and brings the controller back to health.
