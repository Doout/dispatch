# Security

## Reporting a vulnerability

Use GitHub's [private vulnerability report](https://github.com/Doout/dispatch/security/advisories/new) to send a description, affected version, and reproduction steps. Remove credentials and personal data from attachments.

If private reporting is unavailable, open an issue asking a maintainer for a private reporting channel. Do not include vulnerability details or credentials in that issue.

Security fixes target the latest release. Older versions do not have a separate maintenance branch.

## Trust model

Dispatch controls deployment infrastructure and runs repository-defined scripts. Only grant configuration access to people you trust to execute code with the credentials and infrastructure access assigned to those jobs. Review pull request code before running a preview that has secrets or access to private services.

The Docker executor and managed updater mount the Docker socket. Access to that socket can grant control of the Docker host. A container boundary does not make an untrusted job safe to run there.

Keep the controller and agents on a trusted network. Use HTTPS when accessing the controller remotely, and complete owner setup before exposing an installation. The Compose Traefik route terminates TLS but does not add network access control or another login gate. Restrict the route at the network or proxy before using it outside a trusted network.

Password login and Basic authentication share database-backed attempt limits. Discovery and other public sign-in endpoints have separate per-client limits. Forwarded client addresses are ignored unless `DISPATCH_TRUSTED_PROXY_CIDRS` names the immediate proxy network. Configure that value narrowly; a broad trusted range lets clients spoof their address. If unset behind a proxy, the limits see the proxy as one client. Set `DISPATCH_PUBLIC_URL` to the HTTPS origin so callbacks and HSTS use the public scheme. Protect owner accounts, provider credentials, the database, and the encryption key. Backups can contain all of these credentials.

Local secrets are encrypted at rest. Jobs receive the resolved values and can print them in logs or save them as outputs. Avoid `set -x` around secrets, review job scripts, and redact logs before sharing them.

OpenShift setup creates a service account with cluster-admin privileges. Treat access to that managed credential as cluster-administrator access.

## Dependency checks

CI runs Go vulnerability checks and an audit of production frontend dependencies. These checks identify known advisories; they do not replace review of deployment permissions, scripts, or configuration.
