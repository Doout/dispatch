# Managed application routes

Docker targets can publish stable application hostnames through a target-local
Traefik file provider. Enable routing in **Servers → Routing** or with the owner-only
`PUT /api/v1/servers/{id}/routing` endpoint. Kubernetes ingress remains chart-managed.

```json
{"routing":{"baseDomain":"apps.example.com","entryPoint":"websecure","requireTls":true,"tlsResolver":"letsencrypt"}}
```

An application with no explicit domain receives `app-<stable-id>.apps.example.com`.
Explicit domains must be within the target's managed domain. The controller reserves
each hostname globally; another application or target cannot claim it. Configure the
application's container port, and point wildcard DNS at the target's public proxy.
DNS records and firewall rules remain operator-managed.

## Target setup

Configure an absolute, dedicated provider directory on the process that executes
Docker jobs. For local execution use `DISPATCH_ROUTING_DIRECTORY`; on an enrolled
agent use `DISPATCH_AGENT_ROUTING_DIRECTORY`. For example:

```sh
DISPATCH_AGENT_RUNTIME=true
DISPATCH_AGENT_ROUTING_DIRECTORY=/var/lib/dispatch/routes
```

The setting is host configuration, never a request-supplied filesystem path. Keep
the directory persistent and writable by the executor, and mount the **directory**
read-only into Traefik. Do not mount individual route files: publication uses atomic
rename and the proxy must watch the parent directory.

A minimal Traefik static configuration is:

```yaml
entryPoints:
  web:
    address: ':80'
  websecure:
    address: ':443'
providers:
  file:
    directory: /etc/traefik/dispatch-routes
    watch: true
certificatesResolvers:
  letsencrypt:
    acme:
      email: operator@example.com
      storage: /var/lib/traefik/acme.json
      httpChallenge:
        entryPoint: web
```

Map the executor's provider directory to `/etc/traefik/dispatch-routes` and persist
Traefik's ACME storage separately. For HTTP-01 issuance, public port 80 must reach
the `web` entry point; application HTTPS uses port 443. Use Traefik's staging CA
while testing issuance. [Traefik documents the ACME resolver options](https://doc.traefik.io/traefik/v3.5/reference/install-configuration/tls/certificate-resolvers/acme/).

Candidates bind only to an ephemeral port on the Docker host's `127.0.0.1`. The
executor and Traefik must share that host's network namespace, either as host
processes or containers with host networking. A bridge-networked controller or
proxy cannot reach those loopback ports; changing the bind address to expose them
publicly defeats the readiness boundary. The default installation does not enable
this managed provider automatically.

Reserve the managed DNS zone for this provider. Dispatch checks its own directory
and controller reservations, but cannot arbitrate competing routes configured in
other Traefik providers. Do not edit generated route files: changed or foreign
files cause deployment to fail instead of being overwritten. Remove owned routes
through application cleanup before changing their listener, TLS policy or hostname.

## Deployment, recovery and public evidence

A deployment prepares an owned route, then starts a separate candidate with an
immutable image and a dynamically allocated loopback port. The previous destination
continues serving while the captured health policy checks the candidate. A first
deployment has an empty-backend placeholder, which permits certificate issuance
without exposing the candidate. Failed readiness leaves the previous route intact.

After health passes, one atomic file update publishes the new destination and records
the previous deployment. The immediately previous candidate stays running through
proxy reload; older stateless candidate containers are removed on later successful
deployments. Rollback uses retained runtime inputs to start and check a new candidate
before switching the route. Application cleanup removes the owned route and workloads;
durable storage follows its separate deletion policy.

Publication and public readiness are separate. The controller resolves DNS, verifies
the public certificate when HTTPS is required, and checks that the proxy responds
with the expected deployment identity. Proxy 5xx responses never count as live.
Certificates report pending, invalid, verified or renewal-due evidence; Traefik owns
issuance and renewal. A running workload can therefore remain **Not live** while DNS
or TLS is unavailable. The controller retries once a minute. A required certificate
check in the deployment health policy also gates the candidate switch itself.

Inspect `GET /api/v1/apps/{id}/route` with `project.view`; refresh evidence with
`POST /api/v1/apps/{id}/route/check` and `project.configure`. The deployment detail
panel displays the current and previous deployment, destination, DNS and certificate
evidence. Enrolled agents return owned route evidence in their durable operation
receipts; only the controller attests public readiness. Retrying a completed operation
reuses its receipt. An interrupted operation remains uncertain and requires review.

Without configured routing, private Docker deployments continue to work. An explicit
public hostname without a configured target provider fails with a setup error.
Simulation does not publish routes or claim verified public reachability.

## Compose applications

Managed Compose routing supports stateless candidates. A single service is selected
automatically. For multiple services set `x-dispatch-ingress-service: web` at the top
of the Compose definition, or configure the target's default ingress service. Only
that service may publish the application's configured TCP port; Dispatch replaces
the published binding with an allocated loopback port.

Fixed container names, host networking, external links, multiple replicas, writable
mounts and extra published ports are rejected before apply. Use a separately bound
service for durable data. Read-only mounts and existing service networks remain
available. These restrictions prevent candidate staging from replacing an active
workload or opening an unchecked public port.

## Verification

Focused tests cover hostname conflicts, retry ownership, immutable candidate identity,
health failure, route rollback, stale observations, certificate delay and proxy errors.
`DISPATCH_RUNTIME_INTEGRATION=1 go test ./internal/agentruntime -run TestRemoteManagedRouteCandidateIntegration`
uses disposable real Docker Compose candidates and Traefik v3.6 to check healthy
publication, receipt replay, failed HTTP readiness preserving the serving endpoint,
a healthy switch and retained rollback through the file provider. DNS is locally resolved by the test.
Public ACME issuance requires an operator-controlled domain and is not exercised by
that local integration test.
