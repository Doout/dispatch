# Install tenant workload certificates

`dispatch-certificate-sync` installs one tenant's wildcard certificate on an
nginx ingress. Run it on the ingress host from a systemd timer. It fetches over
verified HTTPS, validates the certificate chain, private key and exact tenant
wildcard, then switches the certificate and key together before reloading nginx.
Download, validation and reload failures retain the previous working bundle.

The ingress receives no platform certificate key or tenant operational access.
Its credential can download only the issuing tenant's workload bundle.

## Issue a credential

As an owner or administrator, sign into the tenant console and run this in that
console's browser developer tools:

```js
const response = await fetch('/api/v1/hosted/certificate/token', { method: 'POST' });
if (!response.ok) throw new Error('Could not create certificate credential');
const credential = await response.json();
```

Save `credential.token` into `/etc/dispatch/agentops-certificate.token` on the
nginx host. Set the file mode to `0600` and keep it out of shell history. Record
`credential.expiresAt` in the operator's credential rotation schedule. It expires
after 90 days. Issuing a replacement immediately revokes the previous credential.
Rotate it before expiry and replace the file without restarting the timer.

An owner or administrator can revoke it immediately with
`DELETE /api/v1/hosted/certificate/token`. Removing or changing the issuing
account's tenant membership also revokes it. A platform administrator needs
explicit tenant membership to issue or revoke these credentials.

The token cannot modify DNS, request another token, access deployments or download
another tenant's certificate. No browser session is needed for subsequent syncs.

## Configure nginx

Create a private directory for this tenant:

```sh
install -d -m 0700 /etc/nginx/dispatch/agentops
```

Use these paths in the tenant's workload server block:

```nginx
server {
    listen 443 ssl;
    server_name *.agentops.dispatch.cicd.onl;
    ssl_certificate /etc/nginx/dispatch/agentops/current/fullchain.pem;
    ssl_certificate_key /etc/nginx/dispatch/agentops/current/privkey.pem;

    # Add the tenant's workload routing here.
}
```

The nginx master and installer need permission to read this private directory.
For the first installation, stage the server block before running the installer.
Do not reload nginx until the certificate files exist. If validation fails,
remove the staged server block before making unrelated nginx changes.

Create a root-owned executable `/usr/local/sbin/reload-nginx`:

```sh
#!/bin/sh
set -eu
/usr/sbin/nginx -t
/bin/systemctl reload nginx
```

Run the installer once:

```sh
/usr/local/bin/dispatch-certificate-sync \
  --origin https://agentops.dispatch.cicd.onl \
  --token-file /etc/dispatch/agentops-certificate.token \
  --directory /etc/nginx/dispatch/agentops \
  -- /usr/local/sbin/reload-nginx
```

Only the active bundle and its predecessor remain on disk after successful
installation. Files use mode `0600`; directories use `0700`. A failed reload
restores the previous files and attempts to reload them. A nonzero exit status
needs attention, including failures to reload the restored files.

## Refresh automatically

Save `/etc/systemd/system/dispatch-certificate-agentops.service`:

```ini
[Unit]
Description=Refresh AgentOps workload certificate
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=root
UMask=0077
TimeoutStartSec=120
ExecStart=/usr/local/bin/dispatch-certificate-sync --origin https://agentops.dispatch.cicd.onl --token-file /etc/dispatch/agentops-certificate.token --directory /etc/nginx/dispatch/agentops -- /usr/local/sbin/reload-nginx
```

Save `/etc/systemd/system/dispatch-certificate-agentops.timer`:

```ini
[Unit]
Description=Check AgentOps workload certificate every five minutes

[Timer]
OnBootSec=1min
OnUnitActiveSec=5min
RandomizedDelaySec=30s

[Install]
WantedBy=timers.target
```

Enable it after the first successful installation:

```sh
systemctl daemon-reload
systemctl enable --now dispatch-certificate-agentops.timer
```

Monitor service failures and the installed certificate's expiry. A credential
that expires or loses permission stops refreshes; the current certificate stays
in place until it expires. Repeat this setup with a separate credential and
directory for each tenant. Keep the reload command under the ingress operator's
control because it runs with the installer's permissions.

## Test issuance

Keep the console on a publicly trusted bootstrap certificate or proxy while the
controller uses an ACME staging issuer. Staging certificates are not publicly
trusted and must never replace the console certificate used by DNS replicas.
For an isolated ingress test, `--issuer-ca-file /path/to/staging-root.pem` adds an
explicit test issuer root when validating the downloaded workload certificate.
It does not weaken HTTPS verification of the download endpoint. Remove that
option after switching to production issuance. The default verifies both the
HTTPS endpoint and downloaded certificate against the system trust store.
