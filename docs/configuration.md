# Controller configuration

For a Docker installation, start with the [installation guide](installation.md). Use the tables below to configure the controller and webhook relay. Hosted installations have [separate configuration](hosted-tenants.md).

## Controller variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISPATCH_ADDR` | `127.0.0.1:8080` | Controller listen address |
| `DISPATCH_PUBLIC_URL` | none | HTTPS origin for callbacks and browser security headers |
| `DISPATCH_TRUSTED_PROXY_CIDRS` | none | Comma-separated CIDRs for reverse proxies allowed to supply client addresses and HTTPS scheme |
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

The automatically registered `local-docker` server is both a deployment target and a Docker builder. Jobs with `builder: docker` use the shared builder pool, including one local build slot. Local builds use `DISPATCH_DOCKER_SOCKET` and do not require SSH credentials. Without the socket, the local server is unavailable and cannot receive builds.

## Relay variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISPATCH_RELAY_ADDR` | `127.0.0.1:8090` | Relay listen address |
| `DISPATCH_RELAY_PUBLIC_URL` | none | Public origin used to create webhook endpoints |
| `DISPATCH_RELAY_TOKEN` | none | Controller access token with at least 24 characters |
| `DISPATCH_RELAY_TLS_MODE` | none | Set to `auto` for automatic TLS |
| `DISPATCH_RELAY_ACME_CACHE` | `/data/acme` | Certificate cache for automatic TLS |
| `DATABASE_URL` | `relay.db` | Relay SQLite path or PostgreSQL URL |

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

Dispatch passes resolved secrets to the job process. Job scripts can print those values or write them to outputs, so review scripts before granting credentials. Avoid shell tracing such as `set -x` in jobs that use secrets.

## GitHub connections and events

The GitHub App manifest flow creates an App with the required callback, setup, repository permissions, and event subscriptions. Existing Apps can connect with an App ID, RSA private key, webhook secret, and optional installation ID.

The manifest requests Contents read, Pull requests write for PR comments, Issues write, Commit statuses write, Checks write and Metadata read. In Connections, Verify checks the App registration, installation and issued token against those permissions. If the App registration lacks access, edit its permissions in GitHub first. Then approve the updated installation permissions or reinstall the App, select its repositories, and verify again.

A GitHub App uses one installation webhook. It does not require a webhook for each repository. Polling can run alone or alongside webhooks.

When GitHub cannot reach the controller, install `dispatch-relay` on a public node. The relay saves each request before returning `202 Accepted`. Dispatch polls it, verifies the signature and acknowledges the event after processing it. If the lease expires before acknowledgment, the relay sends the event again.

Create a relay under **Servers > Add server > Webhook relay**. The installer can use SSH or a command copied to the relay host. Use the Docker Compose option to run the relay in a container.

The legacy shared webhook endpoint remains available at `/api/v1/events/github` when `DISPATCH_GITHUB_WEBHOOK_SECRET` is set.

## Kubernetes and OpenShift credentials

See [Kubernetes and K3s targets](kubernetes-targets.md) for registration checks, namespace limits and validation status.

Kubernetes targets accept pasted, uploaded, or controller-mounted kubeconfig files. Stored kubeconfigs and CA bundles are write-only through the API. Dispatch creates a mode `0600` temporary file for each operation and removes it afterward.

OpenShift setup accepts a non-interactive `oc login` command or username and password. Dispatch parses the login data and calls the OpenShift API. It does not execute `oc`. Setup creates a `dispatch-controller` service account in `dispatch-system`, grants `cluster-admin`, and stores only the managed service-account kubeconfig.

The managed OpenShift credential has cluster-wide privileges. Protect Dispatch storage and owner accounts as cluster-administrator credentials.
