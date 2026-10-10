import { useCallback, useEffect, useRef, useState } from "react";
import {
  ArrowRight,
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
  type Account,
  type HostedPlatform,
  type Membership,
  type Tenant,
} from "./client";
import { TenantMembers } from "./TenantMembers";
import { AccountForm } from "./AccountForm";
import { AccountSettings } from "./AccountSettings";
import { TenantDirectory } from "./TenantDirectory";

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
    }>("/account/tenants");
    setMemberships(result.memberships);
    setInvitations(result.invitations);
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
  if (!account)
    return (
      <AccountForm
        configuration={configuration}
        mode="login"
        onDone={() => void load()}
        initialError={error}
      />
    );
  const selected = memberships.find(
    (membership) => membership.tenant.id === selectedMembers,
  );
  const activeView =
    view === "catalog" && !account.platformAdmin ? "mine" : view;
  const navigate = (next: View) => {
    setView(next);
    setSelectedMembers("");
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
          My tenants
        </button>
        {account.platformAdmin && (
          <button
            aria-current={activeView === "catalog" ? "page" : undefined}
            onClick={() => navigate("catalog")}
          >
            <UsersThree size={18} aria-hidden="true" />
            Tenant directory
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
        {activeView === "mine" && (
          <>
            <header className="hosted-page-heading">
              <div>
                <h1>My tenants</h1>
                <p>Tenants you belong to and your role in each.</p>
              </div>
              <button className="quiet-button" onClick={() => void load()}>
                <ArrowsClockwise size={16} aria-hidden="true" />
                Refresh
              </button>
            </header>
            {handoff.tenantId && !error && (
              <p role="status">Opening your tenant...</p>
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
            {(memberships.length > 5 || membershipFilter) && (
              <label className="hosted-search">
                <span>Find a tenant</span>
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
                      <th>Tenant</th>
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
                            <small>{membership.tenant.slug}</small>
                          </td>
                          <td>{membership.role}</td>
                          <td>
                            <TenantStatus state={membership.tenant.state} />
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
                              {membership.tenant.state === "active" && (
                                <a
                                  className="quiet-button"
                                  href={tenantDestination(
                                    membership.url,
                                    configuration.origin,
                                  )}
                                >
                                  Open tenant
                                  <ArrowUpRight size={15} aria-hidden="true" />
                                </a>
                              )}
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
                  tenants
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
                title="No matching tenants"
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
                title="No tenants yet"
                action={
                  account.platformAdmin ? (
                    <button
                      className="quiet-button"
                      onClick={() => navigate("catalog")}
                    >
                      Browse tenant directory{" "}
                      <ArrowRight size={16} aria-hidden="true" />
                    </button>
                  ) : undefined
                }
              >
                You are not a member of a tenant yet.
                {!account.platformAdmin && " Ask a tenant owner to invite you."}
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
          />
        )}
        {activeView === "account" && (
          <AccountSettings
            account={account}
            onPasswordChanged={() => {
              setAccount(null);
              setError("Password changed. Sign in again.");
            }}
          />
        )}
      </main>
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
