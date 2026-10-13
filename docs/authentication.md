# Managed and tenant sign-in

The hosted control plane can use WorkOS AuthKit for Doodship accounts. Each
Doodship tenant has a WorkOS organization. Members can also sign in through a
GitHub App or GitHub Enterprise provider configured inside their tenant.

Doodship remains responsible for membership and permissions. Organization
membership in WorkOS does not grant access by itself. Platform administrators
still need tenant membership to access tenant data.

## Enable AuthKit

Configure a separate WorkOS environment for production and local development,
with the login methods you want to offer. Register the callback in each environment.

Production:

```text
https://doodship.com/api/v1/account/workos/callback
```

Local development:

```text
https://local.doodship.com/api/v1/account/workos/callback
```

Set these environment variables on the platform container:

```dotenv
DISPATCH_WORKOS_CLIENT_ID=client_example
DISPATCH_WORKOS_API_KEY_FILE=/run/secrets/workos-api-key
DISPATCH_WORKOS_LOGIN_METHODS=email-code,password
```

The key file must be a private regular file readable by the platform process.
For the hosted Compose example, put it in the mounted `platform-secrets`
directory and set the variables in the Compose environment file. Keep dev and
production in separate WorkOS environments and configure each callback address.
The API key stays on the server. SMTP is not required for AuthKit login.

When both credentials are configured, Doodship renders its own login forms and
calls the WorkOS authentication APIs from the server.
Local password login, registration and password changes are disabled by default.
Public registration is closed by default, including when SMTP is configured.
Standalone
self-hosted installations do not require WorkOS.

## Login forms

`DISPATCH_WORKOS_LOGIN_METHODS` lists the methods shown and accepted by Doodship.
Supported values are `email-code`, `password`, `github`, `google`, and `sso`.
Enable the same methods in the WorkOS environment. Email codes are required for
invitation signup and account migration. Local password migration is a separate
setting. The sign-in page uses WorkOS whenever it is configured.

Doodship handles email codes, password login and recovery, email verification,
TOTP enrollment and challenges, and organization selection. Social login and
SSO redirect to the identity provider using PKCE; they do not use the AuthKit
hosted form. Provider API keys, access tokens, refresh tokens, and pending authentication
tokens are never returned to the browser. Managed-login passwords are forwarded
to WorkOS without being stored by Doodship. Previous Doodship passwords are
checked locally during account migration. The browser
receives a local, single-use continuation bound to an HTTP-only cookie. Retries
rotate that continuation without extending the original ten-minute deadline.
The TOTP setup key is displayed only during enrollment.

Public signup stays closed. A newly invited user verifies their email with a
code rather than creating a local password. WorkOS sends login, reset, and
waitlist confirmation emails, so Doodship does not need SMTP for these flows.
Unsupported provider challenges fail closed; adding other factors requires UI
support before enabling them in WorkOS.

## Closed beta

In each WorkOS environment, disable public signup and enable **Authentication →
Features → Waitlist**. Set its signup URL to the platform sign-in page, for example
`https://doodship.com/?view=login`. Users can join the waitlist from the sign-in
page. Set `userInvitationUrl` and `signUpUrl` to that sign-in address, and set
`passwordResetUrl` to `https://doodship.com/?view=reset-password`. Use the local
origin for the development environment. Review requests in WorkOS; approving a
request sends an invitation.
Invitation links that open Doodship validate the invitation and send an email
code through WorkOS. Some WorkOS-generated links open AuthKit directly. After
setup there, Doodship asks the user to finish signing in with an email code. It
does not exchange a callback code without a locally started login and its PKCE
verifier. Only a valid invitation or an existing active, verified
Doodship account can provision a new WorkOS user. Account migration also requires
proof of the existing login. Joining the waitlist creates no Doodship account.

New WorkOS accounts must have an accepted WorkOS invitation before Doodship admits
them. Existing linked accounts and explicit migration links continue to work.
An unapproved signup cannot create a Doodship account even if the provider's
signup setting changes. Keep automatic joining by verified email domain disabled.

After signing in, users without tenants can create one with themselves as owner
or request to join an existing tenant. Only a tenant owner can approve a join
request. Platform administrators cannot see or approve these requests without
tenant membership. A request does not grant access or appear in another tenant.

`DISPATCH_HOSTED_ALLOW_REGISTRATION=true` explicitly restores local public
registration when password login and SMTP are configured. Leave it unset for the
closed beta. This setting does not open WorkOS signup.

## Connect existing accounts

During migration, temporarily set:

```dotenv
DISPATCH_WORKOS_ALLOW_PASSWORD_LOGIN=true
```

Use the email-code login. After verifying the code, enter the previous Doodship
password once to connect the existing account. Both proofs are required, and the
provider session is checked again before linking. The password stays on the
Doodship server and is not sent to WorkOS. This preserves the Doodship user ID,
platform role and tenant memberships. Future logins use WorkOS without the
previous password. Repeat for existing users, verify the new
login works on both instances, then remove the migration setting and restart each
platform container. Keep migration enabled until the existing administrator
has connected their account. The production and local WorkOS identities are separate;
connect the administrator account in each instance.

Doodship never connects accounts solely because their email addresses match.
An existing account must prove control of both logins. A new WorkOS user receives
no tenant membership or platform administrator privileges automatically.

## Organizations and members

Tenant creation creates and maps the WorkOS organization during the request.
Membership changes also synchronize WorkOS immediately. A background check once
a minute repairs interrupted requests and provider outages. A tenant stays
pending while its organization is unavailable. Tenant IDs
and the control plane hostname form the WorkOS organization external IDs, so a
retry can recover an organization created before a process restart. Unchanged
memberships do not trigger repeated WorkOS API updates.

Tenant-initiated AuthKit login selects the mapped WorkOS organization. First-time
invited owners should sign in at the main Doodship address and accept their
invitation before opening the tenant.

Manage members in Doodship. WorkOS receives the active members who have connected
WorkOS identities. Doodship roles remain authoritative; WorkOS roles do not
promote platform administrators or tenant owners. Manage these organizations
through Doodship rather than editing their membership in the WorkOS dashboard.

Custom-provider users do not need WorkOS accounts and are not copied to WorkOS.
Local membership removal takes effect immediately, even if WorkOS is unavailable.

## Sync role definitions

The hosted role catalog is in `internal/tenancy/roles/catalog.go`. It defines `owner`,
`admin`, `member` and `platform-admin`, with permissions that describe their
current access. Project roles stay in the tenant runtime.

Build the sync tool and preview the changes using the target environment's key:

```sh
docker build -f Containerfile.workos-roles -t doodship-workos-roles .
docker run --rm \
  --mount type=bind,src=/path/to/platform-secrets/workos-api-key,dst=/run/secrets/workos-api-key,readonly \
  doodship-workos-roles --api-key-file /run/secrets/workos-api-key
```

Add `--apply` to create missing roles and permissions, update names and
descriptions, and synchronize Doodship permissions on catalog roles. Review the
preview before applying, since `tenant:` and `platform:` grants omitted from the
catalog are removed from those roles. Other grants, including WorkOS widget
permissions, are preserved. Repeating an unchanged sync makes no writes.

The key must be a regular file with mode `0600` or `0400`, readable by container
user `65532`. The tool uses the key's environment; a client ID is not required.
Run it separately with the local and production keys. It does not need access
to the application database or a running control plane.

For development with Go installed:

```sh
scripts/sync-workos-roles.sh --api-key-file /path/to/workos-api-key --dry-run
scripts/sync-workos-roles.sh --api-key-file /path/to/workos-api-key --apply
scripts/sync-workos-roles.sh --print-definitions
```

Add or change definitions in the catalog, test the corresponding authorization
changes, then rerun the sync. New tenant roles also require updating the catalog
database's membership constraint. Defining a permission does not implement its
API checks.

The tool only manages environment roles listed in the catalog. It does not
delete roles or permissions, change the default role or priority order, create
organizations, or assign roles to people. Roles removed from the catalog remain
in WorkOS until explicitly retired. If a write fails, rerun the command to finish
the remaining changes.

Use `platform-admin` only in the internal staff organization. Doodship still
checks its local platform administrator flag and tenant memberships; WorkOS
claims alone do not grant access. Membership reconciliation currently syncs
active status, not role assignments. The sync tool prepares the role definitions
without changing that behavior.

The tool uses the WorkOS [Roles API](https://workos.com/docs/reference/roles/role)
and [Permissions API](https://workos.com/docs/reference/roles/permission).

## Tenant GitHub login

A tenant owner can add a sign-in method under **Access**, using the existing
GitHub App registration flow or client credentials. This supports github.com
and GitHub Enterprise. Provider secrets remain in the tenant's encrypted store.

Existing members connect the provider from their profile while signed in to the
tenant. After linking, they can use that provider directly from the tenant login
page. The callback rechecks the current member session before connecting an
account.

For people without a Doodship account, set the provider's new-account policy to
**Send for owner approval**. Their first GitHub login creates an access request,
not a membership. A tenant owner opens **My tenants**, selects **Members**, and
approves the request. The person can then sign in again using GitHub, without
creating a WorkOS account. Requests from an email already owned by a Doodship
account require explicit account linking instead.

A tenant-provider login creates only a tenant session. It cannot authenticate to
the platform or a sibling tenant. Disabling a provider, removing its identity
link, or removing membership invalidates access through that session.

## Sessions

AuthKit authorization uses PKCE and a browser-bound, single-use state record.
The server exchanges the code with WorkOS, requires a verified email, and stores
only the WorkOS user and session identifiers alongside the local session. It
does not persist WorkOS access or refresh tokens.

Doodship checks WorkOS session status through the authenticated API and caches
successful checks for at most 30 seconds. Upstream revocation therefore takes
effect within that interval. After the cache expires, an upstream failure denies
access. Tenant handoffs retain the upstream session association. Platform logout
revokes the WorkOS session as well as the local platform session. Tenant logout
ends that tenant session only.

Doodship sessions last at most twelve hours. AuthKit may ask the user to
reauthenticate sooner according to its own session settings.

## Validation before enabling production

Automated tests use a simulated WorkOS API and GitHub responses. With real
credentials, verify signup, existing-account linking, organization sync, logout,
and a tenant handoff in dev before enabling AuthKit in production.
