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

Applications can use an HTTPS Git repository or a Compose definition pasted directly into the console. Pasted definitions are stored with the application, validated by Docker Compose, and applied without cloning a repository. Image-based services work directly; relative build contexts still require repository source files. The Templates tab saves Dockerfile, Compose, or Helm definitions without deploying them; event rules and preview groups instantiate runnable copies when work is triggered.

Helm applications accept OCI chart references or chart names from HTTPS repositories. Deployments and cleanup run through Helm's embedded Go SDK, so the controller does not require a `helm` binary. Upgrade/install operations support optional chart versions and layered values overrides, and cleanup gives preview automation a deterministic close path. Private OCI access uses the standard Helm registry configuration. HTTPS repositories can use `HELM_REPOSITORY_USERNAME` and `HELM_REPOSITORY_PASSWORD`; these values remain environment-only credentials.

## Pull request previews

An application can register a repository and comment command, which defaults to `/preview`. Configure the repository webhook to send `issue_comment` and `pull_request` events to `https://dispatch.example.com/api/v1/events/github` with the same secret as `DISPATCH_GITHUB_WEBHOOK_SECRET`.

When the command appears on the first line of a pull request comment from a repository owner, member, or collaborator, Dispatch creates a PR-specific application from the template, checks out the pull request revision, runs its pre-deploy hook, and starts the deployment. Helm releases receive a unique PR release name. A generated values file can be written to `$DISPATCH_VALUES_FILE` from the pre-hook and is applied after the application's saved values. After deployment, the post-hook runs and Dispatch creates or updates one status comment with the preview URL. Closing the pull request cancels active work, removes its runtime resources and generated application, and updates the status comment.

Preview URLs may contain `{pr}`, `{branch}`, and `{sha}` placeholders. Hooks receive `DISPATCH_APP_ID`, `DISPATCH_APP_NAME`, `DISPATCH_REVISION`, `DISPATCH_SOURCE_REPOSITORY`, `DISPATCH_SOURCE_BRANCH`, `DISPATCH_SERVER_NAME`, `DISPATCH_DEPLOYMENT_URL`, and `DISPATCH_VALUES_FILE`.

Hooks do not inherit controller credentials. Variables deliberately prefixed with `DISPATCH_HOOK_` are passed through for build-specific credentials. Operators can also add write-only values on the **Secrets** page and attach them to an application event rule under **Events → Hooks**. Attached values are encrypted at rest, snapshotted as ciphertext for the event attempt, and decrypted only into that hook process under the configured environment-variable name. Secret storage requires `DISPATCH_MASTER_KEY_FILE`.

An attached `GIT_TOKEN` or `GITHUB_TOKEN` is also used for the event hook's HTTPS source checkout, allowing private GitHub repositories without granting that token to other rules.

A registry rule can attach `REGISTRY`, `REGISTRY_USERNAME`, and `REGISTRY_PASSWORD`, then build and publish an image without modifying the saved application:

```sh
printf '%s' "$REGISTRY_PASSWORD" | docker login "$REGISTRY" \
  --username "$REGISTRY_USERNAME" --password-stdin
image="$REGISTRY/team/service:$DISPATCH_REVISION"
docker build --tag "$image" .
docker push "$image"
printf 'image:\n  repository: %s\n  tag: %s\n' \
  "$REGISTRY/team/service" "$DISPATCH_REVISION" > "$DISPATCH_VALUES_FILE"
```

### Linked preview groups

Preview groups coordinate one or more Helm application templates on the same Kubernetes server. Every run receives one stable namespace and release name per component. The configured dependency graph controls deployment order, while independent components at the same level deploy concurrently. Group value bindings are applied after saved values and pre-hook generated values.

A command in any component repository can start the group. Components without an explicit pull request use the exact SHA from their configured default branch. Another pull request can be attached later without changing the preview URL:

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
