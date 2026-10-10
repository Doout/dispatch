# Tenant data isolation

Hosted Dispatch uses a central catalog for account identities, tenant metadata,
memberships, sessions, invitations, usage totals, DNS records and provisioning
status. Tenant operational data lives in a separate database for each tenant.

Existing operational queries were written for one installation. Each tenant
receives its own store, including background tasks and log streams. A missed
query filter cannot return rows from another tenant's database.

In development, the factory creates separate SQLite files under a private tenant
directory. Hosted PostgreSQL provisioning requires PostgreSQL 16 or later and
creates a database and login role for each tenant. The dedicated provisioner
needs `LOGIN CREATEDB CREATEROLE NOINHERIT`, with no superuser, replication or
row-security bypass privileges. It grants itself permission to assume each
tenant owner while creating the database and revoking public access. Tenant
roles do not receive membership in the provisioner or any other role. Tenant
logins cannot create roles or databases, bypass row security, replicate, or act
as a superuser. Public access to each
tenant database is revoked. Runtime services receive the tenant connection, never
the database provisioner's connection. Generated database credentials stay in a
private local file and are excluded from JSON.

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
claim tokens. New account passwords are chosen by the recipient after email
verification, preventing someone from pre-registering another person's email with
a password they already know.

The application access boundary does not prevent an infrastructure operator with
access to the process, databases and encryption keys from reading tenant data.
Protecting against that operator requires a separate deployment and key ownership
design.
