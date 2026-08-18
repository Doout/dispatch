# Dispatch

Dispatch is a lightweight, self-hosted deployment control plane for personal infrastructure. It pairs a Go controller and embedded React console with a small node agent, a Docker-first runtime driver, and versioned provider interfaces for private cloud automation.

> **Status:** private, early implementation. Do not expose the controller or agent to untrusted networks yet.

## What works in the first milestone

- SQLite by default, with a PostgreSQL storage adapter selected by `DATABASE_URL`.
- Projects, Docker and Kubernetes servers, applications, and immutable deployment records.
- A deployment queue with explicit stages and server-sent event updates.
- Dockerfile, Compose, and Helm application specifications.
- Direct Compose deployments from definitions pasted into the console.
- Reusable application templates for event-driven previews and preview groups.
- Kubernetes targets backed by stored or controller-mounted kubeconfig files.
- Managed OpenShift targets bootstrapped from temporary login credentials.
- A responsive deployment-dispatch console embedded into the Go binary.

The Docker executor is deliberately capability-gated. Development/demo mode can exercise the complete state machine without mutating Docker; live Docker execution is enabled only with `DISPATCH_EXECUTOR=docker`. The standard Compose deployment enables live execution and includes the Docker CLI and Compose plugin in the controller image.

Applications can use an HTTPS or SSH Git repository, or a Compose definition pasted directly into the console. Private repositories can attach one GitHub App connection, encrypted write-only GitHub token, or SSH private key. GitHub App installation tokens are minted only when repository access or event processing needs them and are cached until shortly before their one-hour expiry. Other credentials are scoped to the application source and decrypted only for Git; SSH keys are materialized as mode `0600` temporary files that are removed after checkout. Pasted Compose definitions are stored with the application, validated by Docker Compose, and applied without cloning a repository. Image-based services work directly; relative build contexts still require repository source files. The Templates tab saves Dockerfile, Compose, or Helm definitions without deploying them; event rules and preview groups instantiate runnable copies when work is triggered.

Secrets are typed as text, API tokens, GitHub tokens, SSH private keys, or registry passwords. Values can be pasted or loaded from a text credential file up to 64 KiB. For SSH, Dispatch can generate a reusable global Ed25519 deploy key: the private key is encrypted immediately and never returned, while its public key remains available in the console for copying to GitHub.

Helm applications accept OCI chart references, chart names from HTTPS Helm repositories, or relative chart directories from a Git source repository. A Git-backed source can start from a clone URL plus chart directory, or a GitHub browser URL such as `/tree/main/helm/platform`; Dispatch splits the folder URL into its repository, branch, and chart path. Before saving, the console can make a credential-scoped temporary sparse checkout and inspect `Chart.yaml`, `values.yaml`, an optional `values.schema.json`, and `values-*.yaml` profiles. The structured editor infers controls when no schema exists, supports searching nested paths, and persists only values that differ from the chart defaults. Raw YAML remains available for advanced overrides. Deployments and cleanup run through Helm's embedded Go SDK, so the controller does not require a `helm` binary. Private OCI access uses the standard Helm registry configuration. HTTPS Helm repositories can use `HELM_REPOSITORY_USERNAME` and `HELM_REPOSITORY_PASSWORD`; these values remain environment-only credentials.

## Pull request previews

An application can register a repository and comment command, which defaults to `/preview`. The recommended setup is **Connections → Add GitHub App → Create in GitHub**. Choose whether a personal account or organization owns the registration; this determines where the App appears in GitHub settings. Dispatch generates a unique App name, then uses GitHub's App manifest flow to prefill the callback, setup, webhook, repository permissions, and subscribed events. The App registration's private key and webhook secret are returned directly to Dispatch, encrypted immediately, and never shown in the console. After installing the App, Dispatch verifies the installation and discovers its selected repositories. Multiple GitHub.com and GitHub Enterprise Server connections can coexist.

A GitHub App uses one signed webhook for its installation. Separate repository webhooks are not needed. Open **Connections → Repositories** to see which repositories currently send events, then use **Add repositories** to change that selection in GitHub. Dispatch reads the repository list back with the installation token, so event rules and preview groups can select only repositories the connector can access.

### Durable webhook relay

When a Git provider cannot reach the controller, run `dispatch-relay` on a public node and add it from **Servers → Add server → Webhook relay**. Dispatch maintains an outbound long-poll connection to the relay. No inbound controller port is required. Creating a GitHub App can select that relay, and Dispatch supplies the relay-generated public endpoint to the App manifest automatically.

The relay is provider-neutral. Each endpoint stores the original request method, selected headers, body, and receipt time before returning `202 Accepted`. Dispatch leases the oldest pending delivery and acknowledges it only after provider signature verification and durable local event processing. If Dispatch disconnects before acknowledgement, the lease expires and the same delivery is replayed. Invalid signatures and unsupported provider adapters are retained as dead deliveries instead of blocking the live queue.

The **Add server → Webhook relay** form supports two installation paths:

- **Run a command** generates a copyable command for the relay node.
- **Install over SSH** verifies the node host-key fingerprint, transfers the correct Linux AMD64 or ARM64 binary, and installs the service without storing the SSH credential.

Both paths install a hardened systemd service, persistent SQLite storage, and automatic HTTPS. Point the relay hostname at the node and allow inbound TCP ports 80 and 443 before installing. The generated relay access token is encrypted when the server is saved and is never returned by the API.

Build and run the public node separately:

```bash
docker build -f Containerfile.relay -t dispatch-relay .
docker run --restart unless-stopped \
  -e DISPATCH_RELAY_ADDR=0.0.0.0:8090 \
  -e DISPATCH_RELAY_PUBLIC_URL=https://relay.example.com \
  -e DISPATCH_RELAY_TOKEN='replace-with-at-least-24-random-characters' \
  -e DATABASE_URL=/data/relay.db \
  -v relay-data:/data \
  dispatch-relay
```

Terminate TLS in front of the relay and keep its database on persistent storage. PostgreSQL URLs are supported for a managed database; SQLite is intended for one relay process. The access token is encrypted by Dispatch and is never returned by its API.

Removing a connection removes its encrypted credentials from Dispatch only. The registration and its reserved name remain owned by GitHub until they are deleted in GitHub settings. Use **Manage access** on the connection before removing it when the registration should also be renamed or deleted.

For an existing GitHub App, use **Connections → Add GitHub App → Existing App** and provide the GitHub base URL, App ID, RSA private-key PEM, webhook secret, and optional installation ID. Enterprise Server URLs automatically use their `/api/v3` REST base; an alternate API URL remains available under advanced settings. Configure the existing App for read access to contents and pull requests, write access to issues, and the `issue_comment` and `pull_request` events. Its unique webhook URL is available from the connection row.

The legacy shared-webhook setup remains supported. Configure a repository webhook to send `issue_comment` and `pull_request` events to `https://dispatch.example.com/api/v1/events/github` with the same secret as `DISPATCH_GITHUB_WEBHOOK_SECRET`.

When the command appears on the first line of a pull request comment from a repository owner, member, or collaborator, Dispatch creates a PR-specific application from the template, checks out the pull request revision, runs its pre-deploy hook, and starts the deployment. Each event rule is scoped to its selected GitHub connection, so another connector cannot trigger or close that preview. Helm releases receive a unique PR release name. A generated values file can be written to `$DISPATCH_VALUES_FILE` from the pre-hook and is applied after the application's saved values. After deployment, the post-hook runs and Dispatch creates or updates one status comment with the preview URL. Closing the pull request cancels active work, removes its runtime resources and generated application, and updates the status comment.

Preview URLs may contain `{pr}`, `{branch}`, and `{sha}` placeholders. Hooks run with Bash and receive `DISPATCH_APP_ID`, `DISPATCH_APP_NAME`, `DISPATCH_REVISION`, `DISPATCH_SOURCE_REPOSITORY`, `DISPATCH_SOURCE_BRANCH`, `DISPATCH_SERVER_NAME`, `DISPATCH_DEPLOYMENT_URL`, `DISPATCH_PREVIEW_TAG`, `DISPATCH_VALUES_FILE`, and `DISPATCH_OUTPUT_FILE`. The preview tag is also passed as the script's first positional argument, so a pull request such as `#847` receives `preview-847` as both `$1` and `$DISPATCH_PREVIEW_TAG`.

Hooks do not inherit controller credentials. Variables deliberately prefixed with `DISPATCH_HOOK_` are passed through for build-specific credentials. Operators can also add write-only values on the **Secrets** page and attach them to an application event rule under **Events → Hooks**. Attached values are encrypted at rest, snapshotted as ciphertext for the event attempt, and decrypted only into that hook process under the configured environment-variable name. Secret storage requires `DISPATCH_MASTER_KEY_FILE`.

An attached `GIT_TOKEN` or `GITHUB_TOKEN` is also used for the event hook's HTTPS source checkout, allowing private GitHub repositories without granting that token to other rules. An attached `SSH_PRIVATE_KEY` is written to an isolated temporary file and configures Git for the duration of the build. Preview group components can attach their own credential set from the Events hook editor.

A registry rule can attach `REGISTRY`, `REGISTRY_USERNAME`, and `REGISTRY_PASSWORD`, then build and publish an image without modifying the saved application:

```sh
printf '%s' "$REGISTRY_PASSWORD" | docker login "$REGISTRY" \
  --username "$REGISTRY_USERNAME" --password-stdin
image="$REGISTRY/team/service:$DISPATCH_REVISION"
docker build --tag "$image" .
docker push "$image"
dispatch-hook output set image "$image"
dispatch-hook output set imageTag "$DISPATCH_REVISION"
dispatch-hook helm set image.repository "$REGISTRY/team/service"
dispatch-hook helm set image.tag "$DISPATCH_REVISION"
```

`dispatch-hook` atomically updates the versioned result at `$DISPATCH_RESULT_FILE`. Named string outputs appear in deployment details, remain available when a later step fails, and are exposed to the post-deploy hook as normalized variables such as `DISPATCH_OUTPUT_IMAGE_TAG`. Structured Helm values are applied to that deployment only. Output keys that would normalize to the same environment-variable name are rejected.

The legacy `$DISPATCH_OUTPUT_FILE` JSON object and `$DISPATCH_VALUES_FILE` YAML document remain supported. They are merged into the versioned result during migration, with versioned values taking precedence.

For existing build scripts, Dispatch also recognizes a `Published paired preview images:` section with `backend:` and `ui:` image lines, followed by a `Helm image overrides:` section containing `--set-string path=value` lines. When the explicit handoff files do not already exist, those summary sections are converted into deployment outputs and temporary Helm values automatically.

### Linked preview groups

Preview groups coordinate one or more Helm application templates on the same Kubernetes server. Every group is scoped to one GitHub App connection, and every component selects a repository installed for that connection. A run receives one stable namespace and release name per component. The configured dependency graph controls deployment order, while independent components at the same level deploy concurrently. Group value bindings are applied after saved values and pre-hook generated values.

A command in any component repository can start the group. This supports service and UI work split across separate pull requests. Components without an explicit pull request use the exact SHA from their configured default branch. Reference the other open pull requests in the first comment without changing the preview URL:

```text
/preview
/preview with ui=#123
/preview with repository-name=#123
/preview with owner/repository=#123
```

Deployment hooks belong to event rules, so different commands and repositories can use different build and publish steps. Preview-group rules configure hooks per component. Each attempt snapshots its hooks and receives event context through normalized `DISPATCH_EVENT_*` variables such as `DISPATCH_EVENT_REPOSITORY`, `DISPATCH_EVENT_PULL_REQUEST_NUMBER`, `DISPATCH_EVENT_HEAD_REF`, and `DISPATCH_EVENT_HEAD_SHA`.

Built-in component outputs are `url`, `host`, `namespace`, and `release`. Group hooks receive available outputs as normalized `DISPATCH_COMPONENT_<ALIAS>_<OUTPUT>` variables. A post-hook may write a JSON object of string values to `$DISPATCH_OUTPUT_FILE`; custom values are persisted for dependent components and status evidence. The file is limited to 64 KiB. `url` may be replaced, while `namespace` and `release` are immutable.

Closing one linked pull request keeps the environment running while another linked pull request remains open. Dispatch cleans every release and the owned namespace after all explicitly linked pull requests close. An authenticated operator can also clean a run from the console or `POST /api/v1/preview-group-runs/{id}/cleanup`.

Automation can create the same group through the authenticated API:

```bash
curl --fail-with-body --user "$DISPATCH_USER:$DISPATCH_PASSWORD" \
  --header 'Content-Type: application/json' \
  --data @preview-group.json \
  https://dispatch.example.com/api/v1/preview-groups
```

Automation can register the event trigger after creating an application:

```bash
curl --fail-with-body --user "$DISPATCH_USER:$DISPATCH_PASSWORD" \
  --header 'Content-Type: application/json' \
  --data '{"provider":"github","repository":"owner/repository","command":"/preview","enabled":true,"preDeployHook":"./scripts/build-preview.sh","postDeployHook":"./scripts/publish-preview.sh"}' \
  "https://dispatch.example.com/api/v1/apps/$APP_ID/event-triggers"
```

## Development

Requirements: Go 1.26+, Node.js 22+, Corepack, and Docker for live executor testing.

```bash
corepack pnpm --dir web install
corepack pnpm --dir web build
go test ./...
go run ./cmd/dispatch
# Separate public relay process:
go run ./cmd/dispatch-relay
```

Open <http://localhost:8080>. Set `DISPATCH_DEMO=true` to load clearly labeled local demonstration records.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISPATCH_ADDR` | `127.0.0.1:8080` | Controller listen address |
| `DATABASE_URL` | `dispatch.db` | SQLite path or PostgreSQL URL |
| `DISPATCH_EXECUTOR` | `simulation` | `simulation` or explicitly enabled `docker` |
| `DISPATCH_DEMO` | `false` | Seed local demo records when the store is empty |
| `DISPATCH_ADMIN_USERNAME` | none | Administrator username; set with `DISPATCH_ADMIN_PASSWORD` or leave both empty for first-run setup |
| `DISPATCH_ADMIN_PASSWORD` | none | Administrator password supplied by the environment |
| `DISPATCH_ADMIN_TOKEN` | none | Optional bearer token for API automation |
| `DISPATCH_MASTER_KEY_FILE` | none | File containing the 32-byte secret-encryption key |
| `DISPATCH_DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket used to discover this controller as a managed local server |
| `DOCKER_GID` | `1001` | Host group allowed to use the mounted Docker socket |
| `DISPATCH_KUBECONFIG_DIR` | `./kubeconfigs` | Host directory mounted read-only at `/kubeconfigs` |
| `HELM_REPOSITORY_USERNAME` | none | Optional username for HTTPS Helm repositories |
| `HELM_REPOSITORY_PASSWORD` | none | Optional password for HTTPS Helm repositories |
| `DISPATCH_GITHUB_WEBHOOK_SECRET` | none | HMAC secret for signed pull request webhooks |
| `DISPATCH_PREVIEW_COMMAND` | `/preview` | Default pull request comment command |
| `DISPATCH_GITHUB_API_URL` | `https://api.github.com` | Provider API base used for pull request lookup and comments |
| `DISPATCH_GITHUB_TOKEN` | none | Provider token used for pull request lookup and status comments |
| `DISPATCH_GIT_TOKEN` | none | Optional bearer token for private source checkout before hooks |
| `DISPATCH_HOOK_*` | none | Explicitly scoped variables exposed to pre/post hooks |

The relay process has its own configuration:

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISPATCH_RELAY_ADDR` | `127.0.0.1:8090` | Relay listen address |
| `DISPATCH_RELAY_TLS_MODE` | none | Set to `auto` to obtain and renew a certificate for the public relay hostname |
| `DISPATCH_RELAY_ACME_CACHE` | `/data/acme` | Persistent automatic TLS certificate cache |
| `DISPATCH_RELAY_PUBLIC_URL` | none | Required public base URL used to generate webhook endpoints |
| `DISPATCH_RELAY_TOKEN` | none | Required controller access token, minimum 24 characters |
| `DATABASE_URL` | `relay.db` | Relay SQLite path or PostgreSQL URL |

When the Docker socket is available, Dispatch automatically registers this controller as a ready `local-docker` server. That inventory record is reconciled at startup, becomes unavailable when the socket is absent, and cannot be edited or deleted. The standard Compose deployment mounts the socket automatically; the **Add server** flow is for remote Docker hosts and Kubernetes clusters. Set `DOCKER_GID` to the host group that owns the socket when it differs from the Compose default of `1001`.

Kubernetes servers accept pasted or uploaded kubeconfig YAML, or a controller-mounted file. An optional CA bundle can also be pasted or uploaded. Stored kubeconfigs and CA bundles are never returned by the API; Dispatch materializes them as private temporary files only for the duration of a Kubernetes operation. If the selected cluster already includes `certificate-authority-data`, no separate CA is needed. Stored kubeconfigs must embed client certificates, client keys, and token data instead of referencing external files.

Mounted kubeconfig files remain available through `DISPATCH_KUBECONFIG_DIR`, which is exposed read-only at `/kubeconfigs`. Dispatch verifies that the file and selected context are readable before marking the target ready.

### OpenShift connections

OpenShift servers accept the familiar non-interactive `oc login` command as write-only input, but Dispatch does not execute or bundle the `oc` binary. It parses the API server and temporary token, or performs the OpenShift OAuth challenge for a supplied username and password, then uses HTTPS APIs directly.

Bootstrap creates the `dispatch-controller` service account in `dispatch-system`, binds it to `cluster-admin`, creates a persistent service-account token Secret, and stores a kubeconfig containing only that managed identity. The temporary human login is never saved. This is intentionally a cluster-wide privileged credential; access to Dispatch storage and administrator accounts must be treated as cluster-administrator access.

Use **Repair** on an OpenShift server when its API address, CA, or managed credential changes. Repair requires a fresh `oc login` command, rotates the managed token Secret so the current CA bundle is regenerated, verifies cluster-admin access, and replaces the stored kubeconfig only after the new connection succeeds.

## Administrator setup

When no username and password are supplied through the environment, Dispatch opens a one-time administrator setup screen. Provisioning scripts can perform the same setup call:

```bash
curl --fail-with-body https://dispatch.example.com/api/v1/auth/setup \
  --header 'Content-Type: application/json' \
  --data '{"username":"admin","password":"replace-with-a-long-password"}'
```

The first successful request stores a bcrypt password hash. Later setup requests return `409 Conflict`. Protected API calls accept HTTP Basic credentials or the optional bearer token. The web console exchanges the password for a 12-hour session token, stores the token in browser session storage, and persists only its SHA-256 hash in the controller database so container restarts do not invalidate active sessions.

Schema changes live as ordered SQL files under `internal/store/migrations`; Go only discovers and applies them. See [docs/architecture.md](docs/architecture.md) for system boundaries, [docs/openapi.yaml](docs/openapi.yaml) for the controller contract, [docs/provider-api.md](docs/provider-api.md) for the private provider contract, and [docs/roadmap.md](docs/roadmap.md) for staged scope.

## Privacy and licensing

The GitHub repository is private until its owner explicitly approves publication. The code is prepared under Apache-2.0 so a later public release has a clear license boundary. Private provider implementations and credentials do not belong in this repository.
