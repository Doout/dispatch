# Dispatch

Dispatch is a self-hosted deployment controller for private infrastructure. It connects repositories to Docker, Kubernetes, and OpenShift targets. The controller stores each deployment revision, configuration snapshot, log, output, and runtime resource.

> Dispatch is under active development. Keep the controller and its agents behind trusted network boundaries.

## Current support

- One Go controller with an embedded React application
- SQLite or PostgreSQL storage
- Dockerfile, Compose, and Helm deployments
- Docker, Kubernetes, and OpenShift targets
- Repository-managed Applications and Pipelines
- Selective job reuse and staged promotion
- GitHub Apps for GitHub.com and GitHub Enterprise Server
- Repository polling, signed webhooks, and a durable webhook relay
- Pull request previews across one or more repositories
- Encrypted local secrets and IBM Cloud Secrets Manager references
- Outbound edge nodes for private provider endpoints
- Laneway application and private-network connections
- Users, teams, project roles, external sign-in, account linking, and impersonation
- Deployment, application, server, and runtime topology views

## Install and upgrade

Dispatch includes a Docker installer and upgrade CLI:

```sh
sudo dispatch install
sudo dispatch upgrade
sudo dispatch auto-upgrade enable  # Optional daily checks
```

Upgrades preserve configuration and back up the controller data before replacing its container. An unhealthy replacement restores the previous image and data. Automatic upgrades are disabled by default.

See [installation](docs/installation.md) for CLI downloads, building before the first release, configuration, recovery, and release publishing.

## Run with Compose

The included Compose file exposes Dispatch on `127.0.0.1:8080` and expects an external Docker network named `web` for Traefik.

```sh
cp .env.example .env
docker network inspect web >/dev/null 2>&1 || docker network create web
docker compose up -d --build
```

Open <http://127.0.0.1:8080>. If the environment does not define an administrator, the first visit opens the owner setup screen.

Compose enables the Docker executor and mounts the host Docker socket. Set `DOCKER_GID` to the group that owns `/var/run/docker.sock`.

## Applications

An application can use an HTTPS or SSH repository, or a Compose document saved in Dispatch. Private repositories can use a GitHub App, token, or SSH key.

Templates save reusable Dockerfile, Compose, or Helm definitions without deploying them. Event rules and preview groups create applications from those templates.

Helm sources support:

- OCI chart references
- chart names from HTTPS Helm repositories
- chart directories in Git repositories
- GitHub folder URLs that contain a branch and chart path

The Helm editor reads `Chart.yaml`, `values.yaml`, optional value profiles, and `values.schema.json`. It stores values that differ from the chart defaults. Raw YAML is available when the generated fields are not enough.

Helm operations use the Go SDK. The controller does not require a `helm` binary.

## Repository configuration

Dispatch imports `dispatch/v1alpha1` YAML or JSON from a GitHub repository. One Application can watch several repositories. A source change reruns only the jobs that declare that source. Dispatch reuses a prior job result when its fingerprint matches and all declared outputs exist.

Each run resolves source branches to exact commits before any job starts. Promotion stages deploy that same revision and its saved outputs to each target in order.

Add a GitHub configuration from **Applications > Add > GitHub configuration**. Imports start paused. Polling works without a public controller URL. Webhooks can start the same sync sooner.

See [repository configuration](docs/application-config.md) for the schema and a promotion example.

## Secrets

Local secrets can contain text, API tokens, GitHub tokens, SSH private keys, or registry passwords. Paste a value or upload a text file up to 64 KiB.

Dispatch can generate an Ed25519 deploy key. It encrypts the private key and returns only the public key after creation.

IBM Cloud Secrets Manager references remain external. Dispatch stores the API key encrypted and reads the current secret when a checkout, hook, or SSH operation starts. A private provider endpoint can use an edge node in the same network.

Job definitions bind credentials by name:

```yaml
secrets:
  REGISTRY_HOST:
    secretRef: registry-host
  REGISTRY_USERNAME:
    secretRef: registry-username
  REGISTRY_PASSWORD:
    secretRef: registry-password
```

Dispatch resolves each value for the child process. It does not include secret values in job logs or outputs.

## GitHub events

The GitHub App manifest flow creates an App with the required callback, setup, repository permissions, and event subscriptions. Existing Apps can connect with an App ID, RSA private key, webhook secret, and optional installation ID.

A GitHub App uses one installation webhook. It does not require a webhook for each repository. Polling can run alone or alongside webhooks.

When GitHub cannot reach the controller, install `dispatch-relay` on a public node. The relay writes each request before returning `202 Accepted`. Dispatch polls it and acknowledges an event after signature verification and local processing. An unacknowledged lease expires and the relay sends the event again.

Create a relay under **Servers > Add server > Webhook relay**. The installer can use SSH or a command copied to the relay host. Both paths support systemd and Docker Compose.

The legacy shared webhook endpoint remains available at `/api/v1/events/github` when `DISPATCH_GITHUB_WEBHOOK_SECRET` is set.

## Pull request previews

An event rule listens for a command such as `/preview` on a pull request. A trusted repository member can start a preview. Dispatch checks out the pull request revision, runs its hooks, deploys the application, and updates one status comment.

Preview groups link components stored in different repositories. A command can select another open pull request:

```text
/preview
/preview with ui=#123
/preview with owner/repository=#123
```

Dispatch keeps a linked preview running until all explicitly linked pull requests close. An operator can also clean it from the console.

Hooks receive `DISPATCH_EVENT_*` variables for the triggering event and `DISPATCH_COMPONENT_<ALIAS>_<OUTPUT>` variables for available component outputs. A hook should write named results with `dispatch-hook` or use `DISPATCH_OUTPUT_FILE` for legacy scripts.

## Access control

The first local account becomes the controller owner. The owner can add local users, configure external GitHub sign-in, approve pending users, link identities, create teams, and grant project roles.

External sign-in does not grant project access. A new external identity remains pending until an owner approves it or links it to an existing user.

Project roles are `admin`, `operator`, `deployer`, and `viewer`. The API checks every protected operation. The interface hides actions the current identity cannot use.

Owners can impersonate another user to inspect that user's view. Dispatch displays an impersonation banner and blocks credential changes until the owner returns to their own account.

## Private routes

An edge node polls Dispatch and runs typed HTTP work near a private service. It does not accept inbound connections or arbitrary commands.

A Laneway network connection lets Dispatch manage one Laneway network. A Laneway Connector handles the other direction when Laneway nodes need to reach a private Dispatch controller.

See [edge nodes and private routes](docs/private-networks.md).

## Configuration

### Controller

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISPATCH_ADDR` | `127.0.0.1:8080` | Controller listen address |
| `DISPATCH_PUBLIC_URL` | none | Public origin used for callbacks and install commands |
| `DISPATCH_HOST` | `dispatch.localhost` | Hostname in the Compose Traefik route |
| `DATABASE_URL` | `dispatch.db` | SQLite path or PostgreSQL URL |
| `DISPATCH_EXECUTOR` | `simulation` | `simulation` or `docker` |
| `DISPATCH_DEMO` | `false` | Add demo records to an empty store |
| `DISPATCH_ADMIN_USERNAME` | none | Environment-managed owner username |
| `DISPATCH_ADMIN_PASSWORD` | none | Environment-managed owner password |
| `DISPATCH_ADMIN_TOKEN` | none | Bearer token for API automation |
| `DISPATCH_MASTER_KEY_FILE` | none | File containing the 32-byte encryption key |
| `DISPATCH_DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket used for local execution and inventory |
| `DOCKER_GID` | `1001` | Group allowed to access the mounted Docker socket |
| `DISPATCH_KUBECONFIG_DIR` | `./kubeconfigs` | Host directory mounted read-only at `/kubeconfigs` |
| `DISPATCH_REPOSITORY_CACHE` | `repository-cache` | Credential-scoped Git mirrors |
| `DISPATCH_GITHUB_WEBHOOK_SECRET` | none | HMAC secret for the legacy shared webhook |
| `DISPATCH_PREVIEW_COMMAND` | `/preview` | Default pull request command |
| `DISPATCH_GITHUB_API_URL` | `https://api.github.com` | GitHub REST API base for legacy token access |
| `DISPATCH_GITHUB_TOKEN` | none | GitHub token for legacy event access |
| `DISPATCH_GIT_TOKEN` | none | Token for private hook checkout |
| `HELM_REPOSITORY_USERNAME` | none | HTTPS Helm repository username |
| `HELM_REPOSITORY_PASSWORD` | none | HTTPS Helm repository password |
| `DISPATCH_HOOK_*` | none | Variables passed to hooks by explicit prefix |

### Relay

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISPATCH_RELAY_ADDR` | `127.0.0.1:8090` | Relay listen address |
| `DISPATCH_RELAY_PUBLIC_URL` | none | Public origin used to create webhook endpoints |
| `DISPATCH_RELAY_TOKEN` | none | Controller access token with at least 24 characters |
| `DISPATCH_RELAY_TLS_MODE` | none | Set to `auto` for automatic TLS |
| `DISPATCH_RELAY_ACME_CACHE` | `/data/acme` | Certificate cache for automatic TLS |
| `DATABASE_URL` | `relay.db` | Relay SQLite path or PostgreSQL URL |

## Kubernetes and OpenShift credentials

Kubernetes targets accept pasted, uploaded, or controller-mounted kubeconfig files. Stored kubeconfigs and CA bundles are write-only through the API. Dispatch creates a mode `0600` temporary file for each operation and removes it afterward.

OpenShift setup accepts a non-interactive `oc login` command or username and password. Dispatch parses the login data and calls the OpenShift API. It does not execute `oc`. Setup creates a `dispatch-controller` service account in `dispatch-system`, grants `cluster-admin`, and stores only the managed service-account kubeconfig.

The managed OpenShift credential has cluster-wide privileges. Protect Dispatch storage and owner accounts as cluster-administrator credentials.

## Development

Requirements are Go 1.26 or newer, Node.js 22 or newer, Corepack, and Docker for executor tests.

```sh
make web
go test ./...
go run ./cmd/dispatch
```

Use `make check` for the race detector, `go vet`, TypeScript checks, and frontend tests. Use `make dev` to build the web application and start Dispatch with demo records.

## More documents

- [Architecture and invariants](docs/architecture.md)
- [Repository configuration](docs/application-config.md)
- [OpenAPI contract](docs/openapi.yaml)
- [Provider API](docs/provider-api.md)
- [Private routes](docs/private-networks.md)
- [Roadmap](docs/roadmap.md)

Database migrations are ordered SQL files under `internal/store/migrations`.

Dispatch uses Apache License 2.0. Keep private provider code and credentials outside this repository.
