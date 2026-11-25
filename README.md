# Doodship

Doodship builds images and deploys applications from Git repositories. It supports Dockerfile and Compose deployments, plus Helm releases on Kubernetes and OpenShift.

Each run saves its source commits, configuration, job outputs and logs. The controller includes a React UI, HTTP API, CLI and stdio MCP server. Operational data uses SQLite or PostgreSQL; historical reporting uses embedded DuckDB.

Run slot and PR workflows from a laptop with [doodship and its local example](examples/doodship/README.md).

Doodship is under active development. Keep the controller and its agents on a trusted network. The bundled machine provider is a mock. Allocating real servers and restoring machine snapshots require an external provider adapter and tests against that provider.

## What it does

- Run build jobs, reuse matching job results and promote the same revision through deployment stages.
- Create parallel deployments for pull requests, link changes across repositories and clean up after all linked pull requests close.
- Read live build and deployment logs, inspect saved runs and compare deployment versions.
- Connect GitHub.com or GitHub Enterprise Server through a GitHub App. Use polling, signed webhooks or an event relay.
- Register services, map their connection values into applications and store credentials encrypted.
- Use outbound agents for Docker jobs and access to private services.
- Grant access through users, teams and project roles. Issue expiring credentials for automation.
- Inspect runtime resources, back up managed services and verify restores.

See the [roadmap](docs/roadmap.md) for provider limitations and the checks required before a release.

## Install the doodship CLI

Run slot and PR workflows on macOS or Linux. Install the latest stable release with curl:

```sh
curl -fsSL https://raw.githubusercontent.com/Doout/dispatch/main/scripts/install-cli.sh | bash
```

Or use wget:

```sh
wget -qO- https://raw.githubusercontent.com/Doout/dispatch/main/scripts/install-cli.sh | bash
```

To query projects and run deployments from CI with an automation credential, use [Doodship automation commands](docs/automation-client.md).

The installer verifies the release checksum and installs `doodship` in `~/.local/bin`. If that directory is missing from your PATH, add `export PATH="$HOME/.local/bin:$PATH"` to your shell profile and run it in your terminal.

Run `doodship update` to replace the executable you are using with the latest stable release. If you installed v0.2.0, rerun the installer once after a release containing the update command is published.

To list versions available for your platform (requires jq) or install a specific version:

```sh
installer=https://raw.githubusercontent.com/Doout/dispatch/main/scripts/install-cli.sh
curl -fsSL "$installer" | bash -s -- --list-versions
curl -fsSL "$installer" | bash -s -- --version v0.2.0
```

## Install and upgrade

The installer runs Doodship in Docker on Linux, on amd64 or arm64. Install Docker and Compose 2.30 or newer first, then download the CLI:

```sh
curl -fsSL https://raw.githubusercontent.com/Doout/dispatch/main/scripts/install.sh | sh
sudo dispatch install
```

The download script checks the release archive against `SHA256SUMS`. The controller listens on `127.0.0.1:8080`. Open <http://127.0.0.1:8080> to create the owner account. For a remote server, use an SSH tunnel or configure a reverse proxy.

```sh
sudo dispatch upgrade --check
sudo dispatch upgrade
```

Upgrades preserve configuration and back up controller data before replacing the container. If the replacement fails its health check, Doodship restores the previous image and data. Upgrades interrupt the controller, so schedule them outside active deployments.

To move a managed installation, export it on the old server, copy the archive and import it on the new server:

```sh
# Old server
sudo dispatch export --output /root/dispatch-export.tar.gz
# New server
sudo dispatch import --input /root/dispatch-export.tar.gz
```

The archive preserves accounts, encrypted credentials and controller data. See the [installation guide](docs/installation.md#move-a-self-hosted-installation) for cutover and recovery instructions.

Automatic updates are optional and run in a separate Docker container:

```sh
sudo dispatch auto-upgrade enable
sudo dispatch auto-upgrade status
sudo dispatch auto-upgrade disable
```

See the [installation guide](docs/installation.md) for image tags, proxy settings, backups, recovery and private registry access.

### Build and run with Compose

From a checkout of this repository:

```sh
cp .env.example .env
# Set DOCKER_GID in .env to the group shown by this command:
stat -c '%g' /var/run/docker.sock
docker network inspect web >/dev/null 2>&1 || docker network create web
docker compose up -d --build
```

The Compose file binds the controller to `127.0.0.1:8080`, mounts the host Docker socket and joins the external `web` network for Traefik. If `.env` has no administrator credentials, the first visit opens owner setup. Set `DISPATCH_HOST` and `DISPATCH_PUBLIC_URL` when using a public hostname.

### GitHub App proxy

Open **Connections** on your self-hosted controller or through its connected instance in the Doodship portal. Add or edit a GitHub App and select **Outbound proxy → Use an App proxy**. Enter the HTTP or HTTPS proxy URL and select **Test connection** under **Network access**. This checks whether the controller can reach the GitHub API with the current settings. Doodship also checks before opening GitHub for registration. Your browser opens GitHub for approval. Doodship uses the proxy to exchange the registration code, then uses it for that App's API requests and HTTPS repository checkouts, including worker fetches. The controller and each worker that checks out code must be able to reach the proxy. Update enrolled runtime agents before running deployments that need it.

To share a default across Apps, set **Settings → Features → Repository proxy** and choose **Use controller settings** on each App. The default also covers GitHub sign-in, GitHub API calls without an App, and HTTP(S) repository checkouts without an App. Changes apply to new requests and runs. Use hostnames or domains in the bypass list when the same rule must cover both API calls and Git checkouts. Git uses the worker’s libcurl bypass syntax. Port-specific entries work for API calls, and Git support for IP ranges depends on the installed libcurl version. Bypassed API calls use the App’s edge route when one is configured.

### Doodship control plane

The separate `dispatch-platform` controller handles accounts, instance membership and public ingress. For a new server, open an instance in Doodship and copy its curl install command. Setup installs under `$HOME/.doodship`, links the owner, and enables Docker updates. See [server setup](docs/installation.md#connect-a-new-server-to-doodship). Connect an existing installation through Settings → Connections → Doodship. Its outbound WebSocket carries webhooks, approved user requests and published application traffic without an inbound firewall port. Databases, logs and execution stay on the customer's server. Remote user access is off until a local owner enables it and links users.

See [instance connections](docs/tenant-servers.md) for setup, disconnect behavior and upgrade compatibility, [hosted setup](docs/hosted-tenants.md) for the Docker control plane, and [DNS providers](docs/dns-providers.md) for provider credentials.

## Configure a deployment

Add an application from a repository or save a Compose document in Doodship. Private repositories can use a GitHub App, token or SSH key. Helm sources can use an OCI chart, an HTTPS chart repository or a chart directory in Git.

For jobs and promotion stages, add a repository configuration under Applications. Doodship reads `dispatch/v1alpha1` YAML or JSON, resolves source branches to exact commits and runs jobs for the sources that changed. Each stage deploys that revision and its saved outputs. Polling works without a public controller URL; webhooks can start the same sync sooner.

The [repository configuration guide](docs/application-config.md) has the schema, a complete Application example, build-output bindings and Helm values syntax. The [deployment workspace guide](docs/deployment-workspace.md) covers the UI.

### Parallel deployments for pull requests

An event rule can start a deployment from a trusted repository member's comment:

```text
/preview
/preview with ui=#123
/preview with owner/repository=#123
```

Linked deployments stay active until all explicitly linked pull requests close. Commands can also supply Helm values for one deployment. Doodship keeps run history after cleanup.

See [pull request commands](docs/preview-commands.md) for setup, inline Applications, value overrides and reporting APIs. The [preview lifecycle guide](docs/preview-lifecycle.md) explains expiry, cleanup retries and recovery.

### Services and secrets

Register a PostgreSQL database or generic dependency under Services. Map connection values into Docker or Compose environment variables, or Kubernetes Secrets for Helm. Each deployment saves its bindings so connection changes can identify applications that need redeployment.

Local credentials are encrypted and write-only. IBM Cloud Secrets Manager references stay external and are read when an operation starts. Job scripts receive resolved secrets and can print them, so review scripts before granting credentials and avoid `set -x` in jobs that use secrets.

See [services and application bindings](docs/services.md) and [controller configuration](docs/configuration.md) for setup.

## Access and interface settings

The first local account becomes the controller owner. Owners manage users, external sign-in, teams and project access. New external identities stay pending until an owner approves or links them. Project roles are `admin`, `operator`, `deployer` and `viewer`; the API checks permissions for each protected operation.

Under Settings, turn on New interface for grouped navigation, parallel deployment tables, paginated run history and Analytics. This preference is saved for each account in the current browser. Turn it off to use the existing layout.

Owners can enable recent resource pages under Settings > Experimental UI. These switches control UI access for everyone on the controller. They do not change API permissions or stop accepted jobs. See [experimental UI settings](docs/ui-feature-flags.md) for the available switches and checks still needed.

OpenShift setup creates a managed service account with `cluster-admin`. Protect the controller's storage and owner accounts as cluster-administrator credentials. See [Kubernetes and K3s targets](docs/kubernetes-targets.md) and the [security policy](SECURITY.md).

## Development

Use the Go version in `go.mod`, Node.js 22 and Corepack. Native builds need a C/C++ toolchain for DuckDB; see [analytics](docs/analytics.md). Docker is needed for executor integration tests.

```sh
make web
go run ./cmd/dispatch
```

The local controller simulates deployments by default. `make dev` builds the UI and starts it with demo records. `make check` runs Go tests with the race detector, `go vet`, TypeScript checks and frontend tests.

To view UI changes with live production data, run the [UI preview proxy](docs/ui-preview.md). It requires a separate access key, keeps development and production sessions separate and blocks production changes.

See [contributing](CONTRIBUTING.md) for setup, migrations and review guidance.

## Documentation

| Topic | Guides |
| --- | --- |
| Installation and configuration | [Install and upgrade](docs/installation.md), [controller settings](docs/configuration.md), [hosted tenants](docs/hosted-tenants.md), [DNS providers](docs/dns-providers.md) |
| Applications | [Repository configuration](docs/application-config.md), [deployment workspace](docs/deployment-workspace.md), [release previews and rollback](docs/release-tools.md), [sync and drift checks](docs/application-drift.md), [health policies](docs/deployment-health.md) |
| Pull request deployments | [Commands and reporting](docs/preview-commands.md), [lifecycle and cleanup](docs/preview-lifecycle.md) |
| Services and recovery | [Application bindings](docs/services.md), [PostgreSQL backups](docs/workload-backups.md), [Neon preview databases](docs/neon-preview-databases.md), [repository recovery](docs/repository-recovery.md), [storage ownership](docs/storage.md) |
| Servers | [On-demand servers](docs/on-demand-servers.md), [target installation](docs/target-bootstrap.md), [machine snapshots](docs/machine-snapshots.md), [temporary environments](docs/temporary-environments.md) |
| Networking | [Application routes](docs/application-routing.md), [private routes](docs/private-networks.md), [remote Docker runtime](docs/remote-runtime.md), [edge credentials](docs/edge-credentials.md) |
| Automation and operations | [CLI and MCP tools](docs/automation-client.md), [controller operations](docs/operations.md), [observations and notifications](docs/observations.md), [analytics](docs/analytics.md) |
| Internals | [Architecture](docs/architecture.md), [HTTP API](docs/openapi.yaml), [provider API](docs/provider-api.md), [runtime contract](docs/runtime-contract.md), [roadmap](docs/roadmap.md) |

Doodship uses the [Apache 2.0 license](LICENSE). Keep private provider code and credentials outside this repository.
