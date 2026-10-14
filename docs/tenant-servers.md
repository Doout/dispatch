# Connect a self-hosted instance

Doodship's control plane serves the UI, accounts, instance membership, public
addresses and external events. Each connected instance runs a complete
self-hosted Dispatch installation. That installation keeps its databases,
credentials, queues, logs, artifacts and analytics, and runs its own jobs.

The self-hosted installation initiates an authenticated WebSocket over HTTPS.
The control plane forwards requests through that connection. It never opens a
connection to the customer's server and never receives its database URL or vault
key. No inbound port or public backend address is required for this connection.
All services run in Docker. There is no systemd connector or separate connector
image; the connector runs inside the self-hosted controller.

## Connect an installation

1. Sign in to Doodship and create an instance. Creation records its name,
   membership and WorkOS organization. It does not create an operational database
   or run an instance backend on the control plane.
2. As an instance owner, choose **Connect server**, then **Set up server**.
   Copy the install command for a new server, or choose **Existing installation**
   to use the connection token. The token expires after ten minutes and works
   once. Creating another token invalidates the previous unused token.
3. Sign in to your self-hosted installation as a local owner. Open **Settings →
   Connections → Doodship**. Enter the control plane address and
   connection token, then connect.
4. Set the instance's own address if members should open it on their LAN or VPN.
   The control plane uses that address as a browser navigation target. It never
   fetches it or appends a managed sign-in token.
5. Enable **Allow remote user access** to offer sign-in at the Doodship address.
   Doodship accounts require an approved local user mapping. The installer
   adds one for the owner by default. To add other Doodship users, link each account
   ID to an existing active local user.
   Account IDs are available in Doodship's Account page. Each user keeps their
   local permissions. Emails and claimed platform roles do not grant access.

The installation exchanges the bootstrap token for an installation credential.
It saves the credential and approved user mappings in a private file beside its
master key, or at `DISPATCH_INSTANCE_CONNECTION_FILE` when configured. Keep that
file in the installation's persistent volume. The token is not a shared owner
password, and the credential cannot act as an ordinary local user.

If enrollment succeeds but the response is lost before the installation saves
its credential, disconnect the server from Doodship and create a fresh token.
An existing grant cannot be replaced by another enrollment without revocation.

## Choose how members connect

| Owner's setting | Member access | Webhooks and node traffic |
| --- | --- | --- |
| Remote access off | Open the installation's own URL | Forwarded through the outbound connection |
| Remote access on | Sign in through Doodship or the installation's own login methods | Forwarded through the outbound connection |
| Disconnected | Local access continues; Doodship access stops | Unavailable until a fresh connection is approved |

The sign-in page offers Doodship login and the providers configured on the
self-hosted installation. It also accepts the installation's own username and
password. With remote access enabled, either method stays at the instance's
Doodship address. With remote access disabled, the browser opens the
installation's configured address instead.

Doodship login requires current instance membership and an owner-approved local
account mapping. Installation login uses that installation's accounts and
permissions, without creating or linking a Doodship account. Platform
administrator roles do not grant local access through either method.

Installation login creates a secure, HTTP-only cookie scoped to the instance
hostname. Its session remains in the self-hosted database. OAuth state and its
one-use exchange are bound to the browser and that hostname. Configure each
provider to accept `https://INSTANCE.doodship.com/api/v1/auth/callback` as a
callback when using remote login. Its existing callback at the installation's
own address continues to work for direct access.

Remote users cannot modify the connection grant, user mappings or provider
configuration. Manage those settings directly on the self-hosted installation.
Older controllers keep Doodship login until upgraded to a version that supports
installation login. Disconnection stops access through Doodship for both methods.

The connection carries separate operations for user APIs, GitHub webhooks,
published applications and a limited set of node enrollment and job APIs. Each
operation has its own authorization check. It is not a tunnel to arbitrary hosts
or arbitrary anonymous API endpoints. Customer workers can use the instance's
Doodship address for the supported node APIs, or use its local address. Automatic
server provisioning still supplies the installation's native controller address;
those workers must be able to reach it. Provisioning artifact and progress
endpoints are not forwarded through the connection.

GitHub webhook bodies and signatures reach the self-hosted backend unchanged.
That backend validates the signature and owns durable delivery receipts. An
offline installation returns an unavailable response so the sender can retry.
The control plane does not save webhook bodies or acknowledge an offline backend
as successful. GitHub App webhook URLs use the connected instance address;
browser setup callbacks retain the installation's own address.

## Published applications

On a Docker deployment target, choose the control plane's root domain in its
managed routing settings to publish addresses through Doodship. The backend
assigns the instance's routing scope and reserves each address with the control
plane. Existing targets using local domains retain their local routing.

The control plane stores hostname ownership and opaque application, project and
server IDs. The self-hosted backend owns the live destination and validates it
before forwarding application traffic. It must be able to reach that destination
on the customer's network. Application traffic is separate from remote console
access, so turning off remote user access does not disable published applications.

The shared wildcard DNS and certificate cover these single-label addresses.
Custom managed domains are not enabled. Wildcard certificate keys stay on the
control plane. HTTP and server-sent events are supported through the connection.
WebSocket upgrades from applications are currently rejected; the outbound
connector itself uses WebSocket.

## Disconnect and reconnect

In **My instances**, open the instance's actions menu and choose **Disconnect
server**. The connection page has the same action. A local owner can disconnect
from the installation's Settings page. Disconnect cancels active requests and log
streams. Doodship revokes the installation credential, increments its connection
generation and revokes instance sessions. An old credential cannot reconnect.
Local disconnect removes the usable credential before closing the connection,
including when the control plane cannot be reached.

Local accounts, databases and running jobs remain available. Reconnecting after
revocation requires a new one-use token and local owner approval. A temporary
network interruption instead reconnects with the existing grant. Heartbeats
close an unresponsive connection within 45 seconds. Long-running user requests
also recheck membership and local user access while they run.

When moving an installation with export/import, stop the old installation before
starting the restored copy. The connection file belongs to the installation
backup. Running both copies with the same credential would repeatedly replace
their outbound connection.

## Delete an instance

In **My instances**, open the instance's actions menu and choose **Delete
instance**. Type its name to confirm. Deletion is available to instance owners,
including when no server is connected or the server is offline.

Deletion removes the instance, memberships, connection tokens, sessions,
application addresses and usage records from Doodship. It also closes the outbound
connection. Your server, databases, local accounts and running jobs stay intact.
The name stays reserved for its former owners, so another account cannot receive
webhooks still using the old address. An owner can reuse the name for a new
instance. Connecting the same server requires a new token and local owner approval.

WorkOS organization and DNS provider cleanup retry in the background if a
provider is unavailable. The old credentials stop working as soon as deletion
commits. When embedded backends are disabled, owners can also remove older
inactive instance records. Their stored data and recovery metadata stay on the
server. Active embedded backends do not expose this action.

## Control plane data and limits

The catalog stores accounts, instance membership, grant hashes, connection
generations, direct addresses, hostname ownership and daily aggregate usage.
Operational data stays on the self-hosted server. The backend reports current and
previous-day counts every minute using stable daily periods. Reports contain no
repository names, secrets or logs and replace earlier totals for the same day.
The control plane retains connected-instance usage for 90 days. Treat customer
reports as reported usage, not independently verified billing evidence.

Each connected instance can reserve up to 10,000 application hostnames, including
routes waiting for deployment. Reusing a reservation does not consume another
slot. Removing a route frees its slot. Reservations enforce the limit in one
transaction. An inventory beyond the limit returns an error instead of a partial
list. Application, project and server IDs are limited to 256 bytes in these
records. Existing embedded instances retain their previous routing behavior.

Requests pass through memory without a durable central relay queue. The bridge
uses a versioned protocol, 16 KiB chunks, a 64 MiB request-body limit, at most 64
requests per connection and 128 concurrent requests across the control plane.
The connection cap is 8,192. Slow response writes have a ten-second deadline;
flow control and cancellation bound buffering. The ingress proxy may impose a
smaller request-body limit. These are admission limits, not a capacity guarantee
for a particular server size.

## Existing hosted installations

`dispatch-platform` defaults to `DISPATCH_HOSTED_BACKEND_MODE=connected`. It
creates no per-instance operational store or runtime in that mode. Instances
created by the older embedded setup retain their recorded storage mode and data;
the new default does not migrate or delete those databases.

Set `DISPATCH_HOSTED_BACKEND_MODE=embedded` when upgrading an installation that
must continue serving its existing central backends. Migration requires moving
the operational data onto a self-hosted installation, creating a connected
instance and enrolling it. Automatic conversion of existing embedded catalog
entries is not implemented. Keep the old installation available until the
restored data has been checked. No managed workers or central execution fallback
are enabled.
