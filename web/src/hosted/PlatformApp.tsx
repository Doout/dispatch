import { useCallback, useEffect, useRef, useState } from "react";
import {
  ArrowUpRight,
  ArrowsClockwise,
  Buildings,
  MagnifyingGlass,
  SignOut,
  UserCircle,
  UsersThree,
} from "@phosphor-icons/react";
import {
  HostedBrand,
  HostedEmpty,
  TenantStatus,
  accountInitials,
} from "./HostedChrome";
import { LandingPage, signInAddress } from "./LandingPage";
import {
  HostedError,
  hostedRequest,
  tenantDestination,
  directInstanceDestination,
  type Account,
  type HostedPlatform,
  type Membership,
  type Tenant,
} from "./client";
import { TenantMembers } from "./TenantMembers";
import { AccountForm } from "./AccountForm";
import { AccountSettings } from "./AccountSettings";
import { TenantDirectory } from "./TenantDirectory";
import { CreateTenant } from "./CreateTenant";
import { JoinTenant } from "./JoinTenant";
import { InstanceConnection } from "./InstanceConnection";
import {
  InstanceActionDialog,
  InstanceActionsMenu,
  type InstanceAction,
} from "./InstanceActions";
import { instanceNeedsRefresh, useSetupRefresh } from "./useSetupRefresh";

type View = "mine" | "catalog" | "account";

export function PlatformApp({
  configuration,
}: {
  configuration: HostedPlatform;
}) {
  const [account, setAccount] = useState<Account | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [view, setView] = useState<View>(() => {
    const requested = new URLSearchParams(window.location.search).get("view");
    return requested === "account" || requested === "catalog"
      ? requested
      : "mine";
  });
  const [membershipFilter, setMembershipFilter] = useState("");
  const [membershipLimit, setMembershipLimit] = useState(50);
  const [memberships, setMemberships] = useState<Membership[]>([]),
    [invitations, setInvitations] = useState<Tenant[]>([]);
  const [selectedMembers, setSelectedMembers] = useState("");
  const [selectedConnection, setSelectedConnection] = useState("");
  const [instanceAction, setInstanceAction] = useState<{
    membership: Membership;
    action: InstanceAction;
  } | null>(null);
  const [tenantAction, setTenantAction] = useState<"create" | "join" | null>(
    null,
  );
  const [joinRequests, setJoinRequests] = useState<Tenant[]>([]);
  const [verification, setVerification] = useState(
    () =>
      new URLSearchParams(window.location.hash.slice(1)).get("verify_email") ??
      "",
  );
  const [handoff] = useState(() => {
    const query = new URLSearchParams(window.location.search);
    return {
      tenantId: query.get("tenant"),
      codeChallenge: query.get("challenge"),
    };
  });
  const transferring = useRef(false);
  const loadMemberships = useCallback(async () => {
    const result = await hostedRequest<{
      memberships: Membership[];
      invitations: Tenant[];
      joinRequests?: Tenant[];
    }>("/account/tenants");
    setMemberships(result.memberships);
    setInvitations(result.invitations);
    setJoinRequests(result.joinRequests ?? []);
  }, []);
  const load = useCallback(async () => {
    setError("");
    try {
      const user = await hostedRequest<Account>("/account");
      setAccount(user);
      await loadMemberships();
    } catch (cause) {
      if (cause instanceof HostedError && cause.status === 401)
        setAccount(null);
      else setError((cause as Error).message);
    } finally {
      setLoading(false);
    }
  }, [loadMemberships]);
  const refreshSetup = useCallback(async () => {
    try {
      await loadMemberships();
      setError("");
    } catch (cause) {
      if (cause instanceof HostedError && cause.status === 401) await load();
      else setError((cause as Error).message);
    }
  }, [loadMemberships, load]);
  useSetupRefresh(
    !!account &&
      memberships.some(
        ({ tenant, connection }) =>
          instanceNeedsRefresh(tenant.state) || connection !== undefined,
      ),
    refreshSetup,
  );
  useEffect(() => {
    if (verification)
      window.history.replaceState(
        window.history.state,
        "",
        `${window.location.pathname}${window.location.search}`,
      );
    void load();
  }, [load, verification]);
  useEffect(() => {
    if (
      !account ||
      !handoff.tenantId ||
      !handoff.codeChallenge ||
      transferring.current
    )
      return;
    transferring.current = true;
    void hostedRequest<{ url: string }>("/account/handoffs", "POST", handoff)
      .then((result) =>
        window.location.assign(
          tenantDestination(result.url, configuration.origin),
        ),
      )
      .catch((cause) => {
        transferring.current = false;
        setError((cause as Error).message);
      });
  }, [account, handoff, configuration.origin]);
  const logout = async () => {
    try {
      await hostedRequest("/account/logout", "POST");
      setAccount(null);
      setMemberships([]);
      setInvitations([]);
      setSelectedMembers("");
      setSelectedConnection("");
      setTenantAction(null);
      setJoinRequests([]);
      window.history.replaceState(window.history.state, "", signInAddress);
    } catch (cause) {
      setError((cause as Error).message);
    }
  };
  if (loading)
    return (
      <main className="hosted-auth" aria-busy="true">
        <HostedBrand />
        <p role="status">Loading your account...</p>
      </main>
    );
  if (verification)
    return (
      <AccountForm
        mode="verify"
        verification={verification}
        configuration={configuration}
        onDone={() => {
          setVerification("");
          void load();
        }}
      />
    );
  const query = new URLSearchParams(window.location.search);
  if (
    (import.meta.env.DEV ||
      import.meta.env.VITE_HOSTED_LANDING_PAGE === "true") &&
    !account &&
    !error &&
    !query.get("view") &&
    !handoff.tenantId &&
    !handoff.codeChallenge
  )
    return <LandingPage />;
  if (
    account &&
    (query.get("link") === "workos" ||
      query.get("view") === "reset-password" ||
      query.has("managed_state"))
  ) {
    return (
      <AccountForm
        configuration={configuration}
        mode="login"
        onDone={() => window.location.assign("/?view=account")}
      />
    );
  }
  if (!account)
    return (
      <AccountForm
        configuration={configuration}
        mode="login"
        onDone={() => void load()}
        initialError={error || query.get("auth_error") || ""}
      />
    );
  const selected = memberships.find(
    (membership) => membership.tenant.id === selectedMembers,
  );
  const connectionMembership = memberships.find(
    (membership) => membership.tenant.id === selectedConnection,
  );
  const activeView =
    view === "catalog" && !account.platformAdmin ? "mine" : view;
  const navigate = (next: View) => {
    setView(next);
    setSelectedMembers("");
    setSelectedConnection("");
    setTenantAction(null);
    const url = new URL(window.location.href);
    url.searchParams.set("view", next);
    window.history.replaceState(
      window.history.state,
      "",
      `${url.pathname}${url.search}${url.hash}`,
    );
  };
  const visibleMemberships = memberships.filter(({ tenant }) =>
    `${tenant.name} ${tenant.slug}`
      .toLowerCase()
      .includes(membershipFilter.toLowerCase()),
  );
  return (
    <div className="hosted-shell">
      <a className="skip-link" href="#hosted-content">
        Skip to content
      </a>
      <header className="hosted-header">
        <HostedBrand detail="Control plane" />
        <div className="hosted-header-account">
          <button
            className="hosted-account-trigger"
            aria-label={`${accountInitials(account.name)} ${account.name}, account settings`}
            onClick={() => navigate("account")}
          >
            <span className="hosted-avatar" aria-hidden="true">
              {accountInitials(account.name)}
            </span>
            <span>
              <strong>{account.name}</strong>
              <small>
                {account.platformAdmin ? "Platform admin" : "Member"}
              </small>
            </span>
          </button>
          <button className="quiet-button" onClick={() => void logout()}>
            <SignOut size={16} aria-hidden="true" />
            Log out
          </button>
        </div>
      </header>
      <nav className="hosted-nav" aria-label="Account navigation">
        <button
          aria-current={activeView === "mine" ? "page" : undefined}
          onClick={() => navigate("mine")}
        >
          <Buildings size={18} aria-hidden="true" />
          My instances
        </button>
        {account.platformAdmin && (
          <button
            aria-current={activeView === "catalog" ? "page" : undefined}
            onClick={() => navigate("catalog")}
          >
            <UsersThree size={18} aria-hidden="true" />
            Instance directory
          </button>
        )}
        <button
          aria-current={activeView === "account" ? "page" : undefined}
          onClick={() => navigate("account")}
        >
          <UserCircle size={18} aria-hidden="true" />
          Account
        </button>
      </nav>
      <main className="hosted-content" id="hosted-content" tabIndex={-1}>
        {error && (
          <p className="form-error" role="alert">
            {error}{" "}
            <button className="quiet-button" onClick={() => void load()}>
              Retry
            </button>
          </p>
        )}
        {activeView === "mine" && tenantAction === "create" && (
          <CreateTenant
            origin={configuration.dataOrigin ?? configuration.origin}
            reservedTenantNames={configuration.reservedTenantNames}
            tenants={memberships.map(({ tenant }) => tenant)}
            selfService
            onCancel={() => setTenantAction(null)}
            onCreated={async () => {
              await loadMemberships();
              setTenantAction(null);
            }}
          />
        )}
        {activeView === "mine" && tenantAction === "join" && (
          <JoinTenant
            origin={configuration.dataOrigin ?? configuration.origin}
            onCancel={() => setTenantAction(null)}
            onRequested={async () => {
              await loadMemberships();
              setTenantAction(null);
            }}
          />
        )}
        {activeView === "mine" && connectionMembership && (
          <InstanceConnection
            key={connectionMembership.tenant.id}
            membership={connectionMembership}
            onClose={() => setSelectedConnection("")}
            onChanged={refreshSetup}
          />
        )}
        {activeView === "mine" && !tenantAction && !connectionMembership && (
          <>
            <header className="hosted-page-heading">
              <div>
                <h1>My instances</h1>
                <p>Your instances and your role in each.</p>
              </div>
              <div className="hosted-row-actions">
                {memberships.length > 0 && (
                  <>
                    <button
                      className="quiet-button"
                      onClick={() => setTenantAction("join")}
                    >
                      Join instance
                    </button>
                    <button
                      className="primary-button"
                      onClick={() => setTenantAction("create")}
                    >
                      Create instance
                    </button>
                  </>
                )}
                <button className="quiet-button" onClick={() => void load()}>
                  <ArrowsClockwise size={16} aria-hidden="true" />
                  Refresh
                </button>
              </div>
            </header>
            {handoff.tenantId && !error && (
              <p role="status">Opening your instance...</p>
            )}
            {memberships.some(({ tenant }) =>
              ["pending", "provisioning"].includes(tenant.state),
            ) && (
              <p role="status">
                Instance setup is in progress. This page updates automatically.
              </p>
            )}
            {memberships.some(({ tenant }) => tenant.state === "failed") && (
              <p role="status">
                Instance setup failed. We will retry automatically.
              </p>
            )}
            {invitations.length > 0 && (
              <section className="hosted-panel">
                <h2>Invitations</h2>
                {invitations.map((tenant) => (
                  <Invitation
                    key={tenant.id}
                    tenant={tenant}
                    onAccepted={loadMemberships}
                  />
                ))}
              </section>
            )}
            {joinRequests.length > 0 && (
              <section
                className="hosted-panel"
                aria-label="Pending join requests"
              >
                <h2>Requests to join</h2>
                {joinRequests.map((tenant) => (
                  <div className="hosted-invitation" key={tenant.id}>
                    <div>
                      <strong>{tenant.slug}</strong>
                      <p>
                        {
                          new URL(
                            configuration.dataOrigin ?? configuration.origin,
                          ).protocol
                        }
                        //{tenant.slug}.
                        {
                          new URL(
                            configuration.dataOrigin ?? configuration.origin,
                          ).host
                        }
                      </p>
                    </div>
                    <span>Waiting for owner approval</span>
                  </div>
                ))}
              </section>
            )}
            {(memberships.length > 5 || membershipFilter) && (
              <label className="hosted-search">
                <span>Find an instance</span>
                <span className="hosted-search-field">
                  <MagnifyingGlass size={18} aria-hidden="true" />
                  <input
                    type="search"
                    placeholder="Name or subdomain"
                    value={membershipFilter}
                    onChange={(event) => {
                      setMembershipFilter(event.target.value);
                      setMembershipLimit(50);
                    }}
                  />
                </span>
              </label>
            )}
            {memberships.length > 0 && (
              <div className="hosted-table-wrap">
                <table className="hosted-table">
                  <thead>
                    <tr>
                      <th>Instance</th>
                      <th>Your role</th>
                      <th>Status</th>
                      <th>
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {visibleMemberships
                      .slice(0, membershipLimit)
                      .map((membership) => (
                        <tr key={membership.tenant.id}>
                          <td>
                            <strong>{membership.tenant.name}</strong>
                            <small>
                              {membership.tenant.slug}.
                              {new URL(configuration.origin).host}
                            </small>
                          </td>
                          <td>{membership.role}</td>
                          <td>
                            {membership.connection ? (
                              <span className="hosted-status">
                                {membership.connection.state === "connected"
                                  ? "Connected"
                                  : membership.connection.state === "offline"
                                    ? "Offline"
                                    : "Connect a server"}
                              </span>
                            ) : (
                              <TenantStatus state={membership.tenant.state} />
                            )}
                          </td>
                          <td>
                            <div className="hosted-row-actions">
                              {["owner", "admin"].includes(membership.role) && (
                                <button
                                  className="quiet-button"
                                  onClick={() =>
                                    setSelectedMembers(membership.tenant.id)
                                  }
                                >
                                  Members
                                </button>
                              )}
                              {membership.role === "owner" &&
                                membership.connection && (
                                  <button
                                    className="quiet-button"
                                    onClick={() =>
                                      setSelectedConnection(
                                        membership.tenant.id,
                                      )
                                    }
                                  >
                                    {membership.connection.state ===
                                    "not_connected"
                                      ? "Connect server"
                                      : "Connection"}
                                  </button>
                                )}
                              {membership.connection &&
                                !membership.connection.remoteAccess &&
                                membership.connection.instanceUrl && (
                                  <a
                                    className="quiet-button"
                                    href={directInstanceDestination(
                                      membership.connection.instanceUrl,
                                    )}
                                    rel="noreferrer"
                                  >
                                    Open instance
                                    <ArrowUpRight
                                      size={15}
                                      aria-hidden="true"
                                    />
                                  </a>
                                )}
                              {membership.tenant.state === "active" &&
                                (!membership.connection ||
                                  (membership.connection.remoteAccess &&
                                    membership.connection.state ===
                                      "connected")) && (
                                  <a
                                    className="quiet-button"
                                    href={tenantDestination(
                                      membership.url,
                                      configuration.origin,
                                    )}
                                  >
                                    Open instance
                                    <ArrowUpRight
                                      size={15}
                                      aria-hidden="true"
                                    />
                                  </a>
                                )}
                              <InstanceActionsMenu
                                membership={membership}
                                onSelect={(action) =>
                                  setInstanceAction({ membership, action })
                                }
                              />
                            </div>
                          </td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              </div>
            )}
            {visibleMemberships.length > membershipLimit && (
              <div className="hosted-table-footer">
                <p>
                  Showing {membershipLimit} of {visibleMemberships.length}{" "}
                  instances
                </p>
                <button
                  className="quiet-button"
                  onClick={() => setMembershipLimit((value) => value + 50)}
                >
                  Show more
                </button>
              </div>
            )}
            {memberships.length > 0 && visibleMemberships.length === 0 && (
              <HostedEmpty
                title="No matching instances"
                action={
                  <button
                    className="quiet-button"
                    onClick={() => setMembershipFilter("")}
                  >
                    Clear search
                  </button>
                }
              >
                Try a different name or subdomain.
              </HostedEmpty>
            )}
            {memberships.length === 0 && !error && (
              <HostedEmpty
                title="No instances yet"
                action={
                  <div className="hosted-row-actions">
                    <button
                      className="primary-button"
                      onClick={() => setTenantAction("create")}
                    >
                      Create instance
                    </button>
                    <button
                      className="quiet-button"
                      onClick={() => setTenantAction("join")}
                    >
                      Join instance
                    </button>
                  </div>
                }
              >
                Create an instance for your team or ask to join an existing one.
              </HostedEmpty>
            )}
            {selected && (
              <TenantMembers
                membership={selected}
                account={account}
                onClose={() => setSelectedMembers("")}
                onChanged={loadMemberships}
              />
            )}
          </>
        )}
        {activeView === "catalog" && account.platformAdmin && (
          <TenantDirectory
            origin={configuration.dataOrigin ?? configuration.origin}
            reservedTenantNames={configuration.reservedTenantNames}
          />
        )}
        {activeView === "account" && (
          <AccountSettings
            configuration={configuration}
            account={account}
            onPasswordChanged={() => {
              setAccount(null);
              setError("Password changed. Sign in again.");
            }}
          />
        )}
      </main>
      {instanceAction && (
        <InstanceActionDialog
          membership={instanceAction.membership}
          action={instanceAction.action}
          onClose={() => setInstanceAction(null)}
          onCompleted={refreshSetup}
        />
      )}
    </div>
  );
}

function Invitation({
  tenant,
  onAccepted,
}: {
  tenant: Tenant;
  onAccepted: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const accept = async () => {
    setBusy(true);
    setError("");
    try {
      await hostedRequest(
        `/account/invitations/${encodeURIComponent(tenant.id)}/accept`,
        "POST",
      );
      await onAccepted();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="hosted-invitation">
      <span>
        <strong>{tenant.name}</strong>
        <small>Join as owner</small>
      </span>
      <button
        className="primary-button"
        disabled={busy}
        onClick={() => void accept()}
      >
        {busy ? "Joining..." : "Accept invitation"}
      </button>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
    </div>
  );
}
