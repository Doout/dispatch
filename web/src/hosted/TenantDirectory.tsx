import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowsClockwise, MagnifyingGlass, Plus } from "@phosphor-icons/react";
import { hostedRequest, type Tenant } from "./client";
import { HostedEmpty, TenantStatus } from "./HostedChrome";
import { UsagePanel } from "./UsagePanel";
import { CreateTenant } from "./CreateTenant";

export function TenantDirectory({ origin }: { origin: string }) {
  const [tenants, setTenants] = useState<Tenant[]>([]),
    [selected, setSelected] = useState<Tenant | null>(null),
    [created, setCreated] = useState<Tenant | null>(null),
    [creating, setCreating] = useState(false),
    [filter, setFilter] = useState(""),
    [stateFilter, setStateFilter] = useState(""),
    [limit, setLimit] = useState(50),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const hostname = new URL(origin).host;
  const createButton = useRef<HTMLButtonElement>(null);
  const formWasOpen = useRef(false);
  useEffect(() => {
    if (!creating && formWasOpen.current) createButton.current?.focus();
    formWasOpen.current = creating;
  }, [creating]);
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
      `${tenant.name} ${tenant.slug}.${hostname}`
        .toLowerCase()
        .includes(filter.toLowerCase()) &&
      (!stateFilter || tenant.state === stateFilter),
  );
  const openCreate = () => {
    setCreated(null);
    setCreating(true);
  };
  if (creating)
    return (
      <CreateTenant
        origin={origin}
        tenants={tenants}
        onCreated={async (tenant) => {
          setCreated(tenant);
          setFilter(`${tenant.slug}.${hostname}`);
          setStateFilter("");
          setLimit(50);
          setTenants((items) => [
            tenant,
            ...items.filter((item) => item.id !== tenant.id),
          ]);
          setCreating(false);
          await load();
        }}
        onCancel={() => setCreating(false)}
      />
    );
  return (
    <>
      <header className="hosted-page-heading">
        <div>
          <h1>Tenant directory</h1>
          <p>Find tenants by address and view their usage.</p>
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
          {(tenants.length > 0 || loading || error) && (
            <button
              ref={createButton}
              className="primary-button"
              onClick={openCreate}
            >
              <Plus size={16} aria-hidden="true" />
              Create tenant
            </button>
          )}
        </div>
      </header>
      {created && (
        <p className="form-success" role="status">
          Created {created.slug}.{hostname}.
        </p>
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
      {tenants.length > 0 && (
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
                placeholder="Tenant name or address"
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
      )}
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
                    <strong>{tenant.slug}</strong>
                    <small>
                      {tenant.slug}.{hostname}
                    </small>
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
            ) : (
              <button
                ref={createButton}
                className="primary-button"
                onClick={openCreate}
              >
                <Plus size={16} aria-hidden="true" />
                Create tenant
              </button>
            )
          }
        >
          {tenants.length === 0
            ? "Create a tenant and assign an owner."
            : "Try a different tenant name, address or status."}
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
