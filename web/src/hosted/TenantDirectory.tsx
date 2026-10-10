import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ArrowsClockwise, MagnifyingGlass, Plus } from "@phosphor-icons/react";
import { hostedRequest, type Tenant } from "./client";
import { HostedEmpty, TenantStatus } from "./HostedChrome";
import { UsagePanel } from "./UsagePanel";

export function TenantDirectory({ origin }: { origin: string }) {
  const [tenants, setTenants] = useState<Tenant[]>([]),
    [selected, setSelected] = useState<Tenant | null>(null),
    [creating, setCreating] = useState(false),
    [filter, setFilter] = useState(""),
    [stateFilter, setStateFilter] = useState(""),
    [limit, setLimit] = useState(50),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const load = useCallback(async () => {
    setError("");
    setLoading(true);
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
  const visible = tenants.filter(
    (tenant) =>
      `${tenant.name} ${tenant.slug}`
        .toLowerCase()
        .includes(filter.toLowerCase()) &&
      (!stateFilter || tenant.state === stateFilter),
  );
  return (
    <>
      <header className="hosted-page-heading">
        <div>
          <h1>Tenant directory</h1>
          <p>View tenant names, status and recorded usage.</p>
        </div>
        <div className="hosted-row-actions">
          <button
            className="quiet-button"
            disabled={loading}
            onClick={() => void load()}
          >
            <ArrowsClockwise size={16} aria-hidden="true" />
            Refresh
          </button>
          <button
            className="primary-button"
            disabled={creating}
            onClick={() => setCreating(true)}
          >
            <Plus size={16} aria-hidden="true" />
            Create tenant
          </button>
        </div>
      </header>
      {creating && (
        <CreateTenant
          origin={origin}
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
      {tenants.length > 0 && (
        <dl className="hosted-summary" aria-label="Tenant totals">
          <div>
            <dt>All tenants</dt>
            <dd>{tenants.length.toLocaleString()}</dd>
          </div>
          <div>
            <dt>Active</dt>
            <dd>
              {tenants
                .filter((tenant) => tenant.state === "active")
                .length.toLocaleString()}
            </dd>
          </div>
          <div>
            <dt>Pending</dt>
            <dd>
              {tenants
                .filter((tenant) => tenant.state === "pending")
                .length.toLocaleString()}
            </dd>
          </div>
        </dl>
      )}
      <div className="hosted-list-toolbar">
        <label className="hosted-search">
          <span>Find a tenant</span>
          <span className="hosted-search-field">
            <MagnifyingGlass size={18} aria-hidden="true" />
            <input
              type="search"
              value={filter}
              onChange={(event) => {
                setFilter(event.target.value);
                setLimit(50);
              }}
              placeholder="Name or subdomain"
            />
          </span>
        </label>
        <label className="hosted-filter">
          Status
          <select
            value={stateFilter}
            onChange={(event) => {
              setStateFilter(event.target.value);
              setLimit(50);
            }}
          >
            <option value="">All statuses</option>
            {[...new Set(tenants.map((tenant) => tenant.state))]
              .sort()
              .map((state) => (
                <option key={state} value={state}>
                  {state}
                </option>
              ))}
          </select>
        </label>
      </div>
      {loading && (
        <p className="hosted-loading" role="status">
          Loading tenants...
        </p>
      )}
      {tenants.length > 0 && (
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
                  <td>
                    <strong>{tenant.name}</strong>
                  </td>
                  <td>
                    <code>{tenant.slug}</code>
                  </td>
                  <td>
                    <TenantStatus state={tenant.state} />
                  </td>
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
      )}
      {!loading && !error && visible.length === 0 && (
        <HostedEmpty
          title={
            tenants.length === 0 ? "No tenants yet" : "No matching tenants"
          }
          action={
            tenants.length > 0 ? (
              <button
                className="quiet-button"
                onClick={() => {
                  setFilter("");
                  setStateFilter("");
                }}
              >
                Clear filters
              </button>
            ) : undefined
          }
        >
          {tenants.length === 0
            ? "Create a tenant and assign an owner."
            : "Try a different name, subdomain or status."}
        </HostedEmpty>
      )}
      {visible.length > limit && (
        <div className="hosted-table-footer">
          <p>
            Showing {limit} of {visible.length} tenants
          </p>
          <button
            className="quiet-button"
            onClick={() => setLimit((value) => value + 50)}
          >
            Show more
          </button>
        </div>
      )}
      {selected && (
        <UsagePanel tenant={selected} onClose={() => setSelected(null)} />
      )}
    </>
  );
}

function CreateTenant({
  origin,
  onCreated,
  onCancel,
}: {
  origin: string;
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
      <p className="hosted-panel-description">
        Choose a name, subdomain and initial owner.
      </p>
      <form
        className="hosted-form hosted-create-form"
        onSubmit={(event) => void submit(event)}
      >
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
          <span id="tenant-subdomain-label">Subdomain</span>
          <input
            value={slug}
            onChange={(event) => setSlug(event.target.value.toLowerCase())}
            required
            pattern="[a-z]([a-z0-9-]{0,61}[a-z0-9])?"
            maxLength={63}
            autoCapitalize="none"
            spellCheck={false}
            aria-describedby="tenant-address"
            aria-labelledby="tenant-subdomain-label"
          />
          <small id="tenant-address" className="hosted-field-hint">
            {slug || "your-tenant"}.{new URL(origin).host}
          </small>
        </label>
        <label className="hosted-form-wide">
          Owner email
          <input
            type="email"
            value={ownerEmail}
            onChange={(event) => setOwnerEmail(event.target.value)}
            required
          />
        </label>
        <p className="hosted-form-wide">
          The owner manages this tenant and its members.
        </p>
        {error && (
          <p role="alert" className="form-error hosted-form-wide">
            {error}
          </p>
        )}
        <div className="hosted-row-actions hosted-form-wide">
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
