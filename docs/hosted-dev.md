# Hosted development instance

The Docker Compose example in `examples/hosted-dev` runs the hosted control plane
behind an existing Traefik installation. The default `connected` mode uses
PostgreSQL for the platform catalog only. Each connected self-hosted installation
keeps its operational databases and runs its own workers. See
[Connect a self-hosted instance](tenant-servers.md).

The dev console uses `local.doodship.com`. A second installation can use
`doodship.com` and pull the `main` image described in
[Hosted control plane images](hosted-images.md). Each installation needs its own
state directory, database volume and DNS configuration.

## Prerequisites

- Docker Engine and Compose.
- Traefik on a Docker network such as `web`, with a `websecure` entrypoint and a
  certificate resolver that can complete Cloudflare DNS-01 challenges.
- An A record for the root console hostname pointing to this server.
- A Cloudflare token with DNS edit and zone read access to the console's zone,
  saved in a private regular file with mode `0600`.

Create a root record and an installation wildcard pointing at this ingress.
Instance creation uses that wildcard and does not add DNS records. Cloudflare
provides DNS-01 proofs while Traefik requests the root and wildcard certificate.
The example does not run an authoritative DNS server or publish PostgreSQL ports.

## Prepare the installation

Run the preparation script from a checkout of this repository. Set the console
address to an IP reachable by the intended users. Find Traefik's address on the
shared Docker network and pass that exact address with a `/32` prefix for IPv4.

```sh
sudo python3 examples/hosted-dev/prepare.py \
  --state-dir /srv/dispatch-platform \
  --root-domain local.doodship.com \
  --console-address 192.168.2.14 \
  --cloudflare-zone-id YOUR_ZONE_ID \
  --cloudflare-token-file /private/cloudflare-token \
  --ingress-proxy-cidr 172.21.0.4/32 \
  --admin-email YOUR_EMAIL
```

The script refuses to replace an existing state directory. It copies the Compose
files, generates database and admin passwords, and writes `.env`. Credentials
stay in private files. The controller runs as UID 65532 and PostgreSQL reads its
secret files as UID 999. The database superuser password is only mounted into
PostgreSQL.

Use `--image` to select a locally built image or a published digest. For a local
build, run this first:

```sh
docker build -f Containerfile.hosted -t dispatch-platform:dev .
```

Check `.env` before starting. Keep `DISPATCH_HOSTED_BACKEND_MODE=connected` for a
new control plane. The Compose example defaults to that mode. Its compatibility
provisioner credentials are unused by connected backends. Change
`DISPATCH_INGRESS_NETWORK` or
`DISPATCH_CERT_RESOLVER` if the existing Traefik uses different names. Keep its
Docker address stable. If that address changes, update
`DISPATCH_INGRESS_PROXY_CIDR` and recreate the gateway and platform together.

```sh
cd /srv/dispatch-platform
docker compose config --quiet
docker compose up -d postgres gateway
docker compose run --rm platform bootstrap-user \
  --email YOUR_EMAIL --name "Platform administrator" \
  --password-file /run/secrets/initial-admin-password
docker compose up -d platform
```

Read `initial-admin.txt` from the state directory for the login details. Remove
the bootstrap password file from `platform-secrets` after creating the account.
Keep the login details in a password manager.

## Check the running instance

```sh
docker compose ps
curl --fail https://local.doodship.com/healthz
curl --fail https://local.doodship.com/readyz
docker compose exec platform dispatch-platform version
```

`/readyz` checks the catalog connection and schema. It does not check customer
installations, their operational databases or DNS propagation. Traefik owns console certificate
renewal, so renewal does not depend on deployment jobs.

The gateway forwards live logs and WebSocket connections without buffering. The
controller listens on loopback in the gateway's network namespace. Neither
container mounts the Docker socket.

No SMTP service is required for the initial administrator. To create other
verified users before email delivery is configured, use the offline `create-user`
command in [Hosted tenants](hosted-tenants.md). Stop the platform first. Tenant
creation does not give the platform administrator membership automatically.

Connected installations initiate WebSocket connections to the control plane.
User requests, approved node traffic and published application requests travel
through those connections. `DISPATCH_HOSTED_WORKLOAD_GATEWAY` is an embedded-mode
ingress setting and is not required for connected workloads.

## Updates and backups

The `main` image advances after CI and image checks pass. Pulling an image does
not change a running container:

```sh
docker compose pull platform
docker compose up -d --no-deps platform
curl --fail https://local.doodship.com/readyz
```

Before an update, record the current image digest and back up both PostgreSQL and
the state directory. In connected mode these are catalog and control-plane
certificate backups. Customer vault keys, operational databases and connection
files belong in backups of each self-hosted installation. Keep backups outside
the server and restrict access to them. A previous image alone cannot undo a database migration.

Use the same update commands with the second installation's hostname and Compose
directory. Its `.env` should point to `ghcr.io/doout/dispatch-platform:main`.
The repository publishes images; scheduling pulls on another server is a
separate deployment step.

## Reserved instance names

`local` is reserved on every installation, alongside names used by the platform
such as `api`, `admin` and `www`. To reserve additional names, add a comma-separated
list to the deployment's `.env` and recreate the platform container:

```dotenv
DISPATCH_HOSTED_RESERVED_TENANT_NAMES=demo,staging,support
```

These names appear in the instance form's validation. The API rejects them and
the host router refuses to serve them as instances. Reserving an existing instance
name blocks its console, so check the directory before adding names.

Production runs at `doodship.com`, and development runs at `local.doodship.com`.
The local DNS record points to `192.168.2.14` with Cloudflare proxying disabled.
Instance consoles in dev use `<instance>.local.doodship.com`.

## Existing embedded development installations

Set `DISPATCH_HOSTED_BACKEND_MODE=embedded` explicitly before upgrading a dev
installation that still serves operational backends on the control-plane host.
The new default does not move or delete those databases, but it does not start
their central runtimes. Back up their operational PostgreSQL databases and the
state directory, including tenant vault keys and generated database credentials.

Embedded workloads still require a reachable ingress configured through
`DISPATCH_HOSTED_WORKLOAD_GATEWAY`. The embedded database provisioner is described
in [Hosted instances](hosted-tenants.md#legacy-embedded-mode). Moving to connected
mode requires an export and restore onto a self-hosted installation and a fresh
connection grant. There is no automatic catalog-entry conversion.
