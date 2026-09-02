# Edge nodes and private routes

An edge node runs beside private services and polls Dispatch over HTTPS. It
does not open an inbound port. A connection can use **Direct** or a named edge
node; IBM Cloud Secrets Manager is the first connection that supports this
route binding.

Connect a **Laneway network** when Dispatch needs to manage nodes and routes in
Laneway. The Laneway authorization screen creates or selects one network and
returns a revocable credential scoped to that network. See
[Laneway integration contract](laneway-integration.md).

When Dispatch itself is private, a **Laneway Connector** can publish its
approved controller route. The Connector and the network connection solve
different directions: the network connection lets Dispatch call Laneway, while
the Connector lets Laneway nodes reach Dispatch.

The management connection is not a data-plane route by itself. Dispatch must
also run an enrolled Laneway node before provider or repository traffic can use
that network. The authorization handoff defines the network-bound bootstrap
change needed to make that enrollment automatic.

## Connect private Dispatch to Laneway

Open **Connections → Add connection → Laneway Connector** and enter the
Laneway control-plane URL plus the private Dispatch IP or CIDR. Dispatch shows
one command to run on the Laneway control plane:

```sh
sudo laneway control invite --name dispatch-controller --docker --connector --bootstrap
```

Paste the generated one-time bootstrap back into Dispatch and select **Deploy
here**. Dispatch validates the command, downloads it from the configured
Laneway authority over TLS 1.3, and starts the Connector through its local
Docker socket. The bootstrap expires after ten minutes and Dispatch never
stores it.

After the container starts, Dispatch provides the exact Laneway commands that
publish the configured Dispatch route and create the `dispatch-edge` login.
No other private prefix is advertised.

Deleting a managed Connector connection also removes its Docker container. Its
persistent Docker volume remains available for recovery.

## Install an edge node

Open **Connections → Add connection → Edge node**, name the node, and create
it. Dispatch returns one install command containing a node-specific token. The
token is shown only in that command. Rotate it from the node row when the host
must be reinstalled.

Docker Compose is the default runtime. Clear the Docker option to install a
systemd service. Both modes run `dispatch-agent`, which long-polls Dispatch for
bounded HTTPS requests.

## Request handling

- Every node has an independent 256-bit bearer token; Dispatch stores only its
  SHA-256 hash.
- Request and response payloads are encrypted with the Dispatch master key
  while queued.
- Jobs are bounded to 1 MiB requests, 2 MiB responses, HTTPS destinations, and
  a short deadline.
- Completed jobs are deleted after the waiting operation reads the response.
- The agent follows no redirects and removes hop-by-hop HTTP headers.

The current edge executor is for provider HTTP connections. Repository
checkout, container registries, Kubernetes APIs, and generic TCP traffic need
their own typed edge operations; selecting a provider route does not silently
turn the node into an unrestricted network proxy.

## Existing Laneway clients

Existing Laneway socket routes remain supported. The Dispatch container mounts
`/run/laneway/lanewayd.sock`, and Dispatch verifies the route table before
dialing a configured private endpoint IP while retaining the service hostname
for TLS verification. Use a managed Connector when Laneway needs to expose the
private Dispatch controller. Keep the socket route only when Dispatch itself
runs as a Laneway client.
