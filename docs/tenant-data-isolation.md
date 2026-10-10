# Tenant data isolation

Hosted Dispatch uses a central catalog for account identities, tenant metadata,
memberships, sessions, invitations, usage totals, DNS records and provisioning
status. Tenant operational data lives in a separate database for each tenant.

Existing operational queries were written for one installation. Each tenant
receives its own store, including background tasks and log streams. A missed
query filter cannot return rows from another tenant's database.

In development, the factory creates separate SQLite files under a private tenant
directory. Hosted provisioning requires PostgreSQL 16 or later and creates a
database and login role for each tenant. The login cannot create roles or
databases, inherit another role, bypass row security, replicate, or act as a
superuser. It has no role memberships and cannot connect to sibling tenant
databases. Public access to each tenant database is revoked. Runtime services
receive the tenant connection, never the database provisioner's connection.
Generated database credentials stay in a private local file and are excluded
from JSON.

The catalog and provisioner use separate logins. The catalog login owns only the
catalog database. The provisioner has `LOGIN`, `CREATEDB`, `CREATEROLE` and
`NOINHERIT`; it does not need superuser, replication or row-security bypass
privileges. It also needs `CONNECT` on its maintenance database.

By default, PostgreSQL 16 does not give a restricted role creator permission to
assume roles it creates. Provisioning grants membership in each tenant role
with `INHERIT FALSE, SET TRUE`. It can then create the database with that owner
and explicitly use `SET ROLE` to revoke public access before restoring its own
role. This permits the provisioner to administer tenant databases, so its
credentials remain part of the trusted control plane. Tenant roles receive no
corresponding membership in the provisioner or another tenant role.

Each tenant also gets separate cache, artifact, analytics and vault paths.
The factory accepts only generated tenant IDs, rejects directory symlinks, and
locks provisioning across processes. It applies operational migrations only to
the selected tenant database. It does not import, move or migrate an existing
single-tenant installation.

This choice increases database and connection management work as tenant counts
grow. Each PostgreSQL runtime has at most four connections, one idle connection,
and a one-minute idle timeout. Size PostgreSQL for the active tenant count.
Background pollers keep active tenants loaded; idle-runtime eviction is not
implemented. Database credentials and vault material need private backups alongside
the catalog and tenant databases. They must not appear in the platform tenant
catalog or usage API. Migrations need a resumable per-tenant rollout before large
hosted installations are upgraded.

Platform administration creates a tenant with an explicit verified initial owner
or an invitation for a verified email recipient. Creation does not enroll the
creator. Tenant owners manage memberships; tenant admins can inspect members but
cannot change memberships in this version. Removing or demoting the last active
owner is rejected. Disabled membership records retain their version so removing
and re-adding someone cannot revive old credentials.

Central-login sessions and tenant sessions have different audiences. Tenant
sessions require current active membership and a verified active account. Password
changes, role changes and revocation invalidate previously issued credentials.
Tenant handoffs expire after one minute, are consumed once, and require the exact
tenant, HTTPS origin and PKCE verifier. Invitations do not return owner-bearing
claim tokens. Self-service registration asks the recipient to choose a password
after email verification, preventing someone from pre-registering another
person's email with a password they already know.

For installations without email delivery, an operator can create an active,
verified account with the offline `create-user` command after verifying the
person's identity. It requires an existing platform administrator and grants no
platform role or tenant membership. Tenant creation can then assign that account
as its initial owner.

The offline `recover-admin` command changes only an existing active, verified
platform administrator's password. It invalidates prior sessions and sign-in
handoffs without changing tenant memberships. It cannot create an administrator,
promote another account or enable a disabled account. Both offline commands
require the controller to be stopped and a private password file. See
[hosted setup](hosted-tenants.md) for the Docker commands.

The application access boundary does not prevent an infrastructure operator with
access to the process, databases and encryption keys from reading tenant data.
Protecting against that operator requires a separate deployment and key ownership
design.
