import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { Mark } from "../components/PageStates";
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
import { UsagePanel } from "./UsagePanel";

type View = "mine" | "catalog" | "account";

export function PlatformApp({
  configuration,
}: {
  configuration: HostedPlatform;
}) {
  const [account, setAccount] = useState<Account | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [view, setView] = useState<View>(() =>
    new URLSearchParams(window.location.search).get("view") === "account"
      ? "account"
      : "mine",
  );
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
    } catch (cause) {
      setError((cause as Error).message);
    }
  };
  if (loading)
    return (
      <main className="hosted-auth" aria-busy="true">
        <Mark />
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
  return (
    <div className="hosted-shell">
      <header className="hosted-header">
        <a className="hosted-brand" href="/">
          <Mark />
          <strong>Dispatch</strong>
        </a>
        <div>
          <span>{account.name}</span>
          <button className="quiet-button" onClick={() => void logout()}>
            Log out
          </button>
        </div>
      </header>
      <nav className="hosted-nav" aria-label="Account navigation">
        <button
          className={view === "mine" ? "active" : ""}
          onClick={() => setView("mine")}
        >
          My tenants
        </button>
        {account.platformAdmin && (
          <button
            className={view === "catalog" ? "active" : ""}
            onClick={() => setView("catalog")}
          >
            Tenant directory
          </button>
        )}
        <button
          className={view === "account" ? "active" : ""}
          onClick={() => setView("account")}
        >
          Account
        </button>
      </nav>
      <main className="hosted-content">
        {error && (
          <p className="form-error" role="alert">
            {error}{" "}
            <button className="quiet-button" onClick={() => void load()}>
              Retry
            </button>
          </p>
        )}
        {view === "mine" && (
          <>
            <header className="hosted-page-heading">
              <h1>My tenants</h1>
              <button className="quiet-button" onClick={() => void load()}>
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
                  {memberships.map((membership) => (
                    <tr key={membership.tenant.id}>
                      <td>
                        <strong>{membership.tenant.name}</strong>
                        <small>{membership.tenant.slug}</small>
                      </td>
                      <td>{membership.role}</td>
                      <td>{membership.tenant.state}</td>
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
                            </a>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {memberships.length === 0 && (
              <p>You are not a member of a tenant yet.</p>
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
        {view === "catalog" && account.platformAdmin && <TenantDirectory />}
        {view === "account" && (
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

function AccountForm({
  configuration,
  mode: initialMode,
  verification,
  onDone,
  initialError = "",
}: {
  configuration: HostedPlatform;
  mode: "login" | "verify";
  verification?: string;
  onDone: () => void;
  initialError?: string;
}) {
  const [mode, setMode] = useState<"login" | "register" | "verify">(
      initialMode,
    ),
    [email, setEmail] = useState(""),
    [name, setName] = useState(""),
    [password, setPassword] = useState(""),
    [confirmation, setConfirmation] = useState(""),
    [error, setError] = useState(initialError),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError("");
    setNotice("");
    if (mode === "verify" && password !== confirmation) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      if (mode === "login") {
        await hostedRequest("/account/login", "POST", { email, password });
        onDone();
      } else if (mode === "register") {
        await hostedRequest("/account/register", "POST", { email, name });
        setNotice("Check your email to finish creating your account.");
      } else {
        await hostedRequest("/account/verify-email", "POST", {
          token: verification,
          name,
          password,
        });
        onDone();
      }
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="hosted-auth">
      <Mark />
      <h1>
        {mode === "login"
          ? "Sign in to Dispatch"
          : mode === "register"
            ? "Create your account"
            : "Finish creating your account"}
      </h1>
      <form className="hosted-form" onSubmit={(event) => void submit(event)}>
        {mode !== "verify" && (
          <label>
            Email
            <input
              type="email"
              autoComplete="username"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              required
            />
          </label>
        )}
        {mode !== "login" && (
          <label>
            Name
            <input
              autoComplete="name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
              maxLength={120}
            />
          </label>
        )}
        {mode !== "register" && (
          <label>
            {mode === "verify" ? "Choose a password" : "Password"}
            <input
              type="password"
              autoComplete={
                mode === "login" ? "current-password" : "new-password"
              }
              value={password}
              minLength={mode === "verify" ? 12 : undefined}
              onChange={(event) => setPassword(event.target.value)}
              required
            />
          </label>
        )}
        {mode === "verify" && (
          <label>
            Confirm password
            <input
              type="password"
              autoComplete="new-password"
              value={confirmation}
              minLength={12}
              onChange={(event) => setConfirmation(event.target.value)}
              required
            />
          </label>
        )}
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        {notice && (
          <p className="form-success" role="status">
            {notice}
          </p>
        )}
        <button className="primary-button" disabled={busy}>
          {busy
            ? "Please wait..."
            : mode === "login"
              ? "Sign in"
              : mode === "register"
                ? "Send verification email"
                : "Create account"}
        </button>
        {configuration.registrationEnabled && mode !== "verify" && (
          <button
            type="button"
            className="quiet-button"
            onClick={() => {
              setMode(mode === "login" ? "register" : "login");
              setError("");
              setNotice("");
              setPassword("");
            }}
          >
            {mode === "login" ? "Create an account" : "Back to sign in"}
          </button>
        )}
      </form>
    </main>
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

function TenantDirectory() {
  const [tenants, setTenants] = useState<Tenant[]>([]),
    [selected, setSelected] = useState<Tenant | null>(null),
    [creating, setCreating] = useState(false),
    [filter, setFilter] = useState(""),
    [limit, setLimit] = useState(50),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const load = useCallback(async () => {
    setError("");
    try {
      setTenants(await hostedRequest<Tenant[]>("/platform/tenants"));
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const visible = tenants.filter((tenant) =>
    `${tenant.name} ${tenant.slug}`
      .toLowerCase()
      .includes(filter.toLowerCase()),
  );
  return (
    <>
      <header className="hosted-page-heading">
        <h1>Tenant directory</h1>
        <div className="hosted-row-actions">
          <button className="quiet-button" onClick={() => void load()}>
            Refresh
          </button>
          <button className="primary-button" onClick={() => setCreating(true)}>
            Create tenant
          </button>
        </div>
      </header>
      {creating && (
        <CreateTenant
          onCreated={async () => {
            setCreating(false);
            await load();
          }}
          onCancel={() => setCreating(false)}
        />
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}{" "}
          <button className="quiet-button" onClick={() => void load()}>
            Retry
          </button>
        </p>
      )}
      <label className="hosted-search">
        Find a tenant
        <input
          type="search"
          value={filter}
          onChange={(event) => {
            setFilter(event.target.value);
            setLimit(50);
          }}
          placeholder="Name or subdomain"
        />
      </label>
      {loading && <p role="status">Loading tenants...</p>}
      <div className="hosted-table-wrap">
        <table className="hosted-table">
          <thead>
            <tr>
              <th>Tenant</th>
              <th>Subdomain</th>
              <th>Status</th>
              <th>Created</th>
              <th>
                <span className="sr-only">Usage</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {visible.slice(0, limit).map((tenant) => (
              <tr key={tenant.id}>
                <td>{tenant.name}</td>
                <td>{tenant.slug}</td>
                <td>{tenant.state}</td>
                <td>{new Date(tenant.createdAt).toLocaleDateString()}</td>
                <td>
                  <button
                    className="quiet-button"
                    onClick={() => setSelected(tenant)}
                  >
                    View usage
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {!loading && visible.length === 0 && <p>No tenants found.</p>}
      {visible.length > limit && (
        <button
          className="quiet-button"
          onClick={() => setLimit((value) => value + 50)}
        >
          Show more
        </button>
      )}
      {selected && (
        <UsagePanel tenant={selected} onClose={() => setSelected(null)} />
      )}
    </>
  );
}

function CreateTenant({
  onCreated,
  onCancel,
}: {
  onCreated: () => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(""),
    [slug, setSlug] = useState(""),
    [ownerEmail, setOwnerEmail] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await hostedRequest("/platform/tenants", "POST", {
        name,
        slug,
        ownerEmail,
      });
      await onCreated();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="hosted-panel">
      <h2>Create tenant</h2>
      <form className="hosted-form" onSubmit={(event) => void submit(event)}>
        <label>
          Name
          <input
            value={name}
            onChange={(event) => setName(event.target.value)}
            required
            maxLength={120}
          />
        </label>
        <label>
          Subdomain
          <input
            value={slug}
            onChange={(event) => setSlug(event.target.value.toLowerCase())}
            required
            pattern="[a-z]([a-z0-9-]{0,61}[a-z0-9])?"
            maxLength={63}
            autoCapitalize="none"
            spellCheck={false}
          />
        </label>
        <label>
          Owner email
          <input
            type="email"
            value={ownerEmail}
            onChange={(event) => setOwnerEmail(event.target.value)}
            required
          />
        </label>
        <p>
          The owner manages this tenant and its members.
        </p>
        {error && (
          <p role="alert" className="form-error">
            {error}
          </p>
        )}
        <div className="hosted-row-actions">
          <button className="primary-button" disabled={busy}>
            {busy ? "Creating..." : "Create tenant"}
          </button>
          <button
            type="button"
            className="quiet-button"
            onClick={onCancel}
            disabled={busy}
          >
            Cancel
          </button>
        </div>
      </form>
    </section>
  );
}

function AccountSettings({
  account,
  onPasswordChanged,
}: {
  account: Account;
  onPasswordChanged: () => void;
}) {
  const [oldPassword, setOldPassword] = useState(""),
    [newPassword, setNewPassword] = useState(""),
    [confirmation, setConfirmation] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (newPassword !== confirmation) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await hostedRequest("/account/password", "PUT", {
        oldPassword,
        newPassword,
      });
      onPasswordChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <h1>Account</h1>
      <p>
        {account.name} · {account.email}
      </p>
      <section className="hosted-panel">
        <h2>Change password</h2>
        <form className="hosted-form" onSubmit={(event) => void submit(event)}>
          <label>
            Current password
            <input
              type="password"
              autoComplete="current-password"
              value={oldPassword}
              onChange={(event) => setOldPassword(event.target.value)}
              required
            />
          </label>
          <label>
            New password
            <input
              type="password"
              autoComplete="new-password"
              value={newPassword}
              onChange={(event) => setNewPassword(event.target.value)}
              required
              minLength={12}
            />
          </label>
          <label>
            Confirm new password
            <input
              type="password"
              autoComplete="new-password"
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              required
              minLength={12}
            />
          </label>
          {error && (
            <p role="alert" className="form-error">
              {error}
            </p>
          )}
          <button className="primary-button" disabled={busy}>
            {busy ? "Saving..." : "Change password"}
          </button>
        </form>
      </section>
    </>
  );
}
