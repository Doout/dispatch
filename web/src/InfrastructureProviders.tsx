import { FormEvent, useEffect, useState } from "react";
import { Overview, request } from "./api";
import { StatusLabel } from "./ResourceTable";

export type InfrastructureProvider = {
  id: string; name: string; endpoint: string; privateNetworkId?: string;
  credentialSecretId?: string; enabled: boolean; capabilities: string[];
  manifest?: { apiVersion: string; name: string; displayName: string; version: string; capabilities: string[]; configurationSchema: Record<string, unknown> };
  state: string; lastError?: string; revision: number; manifestDigest?: string;
};
type Registration = Pick<InfrastructureProvider, "name" | "endpoint" | "privateNetworkId" | "credentialSecretId" | "enabled" | "capabilities"> & { revision?: number };
export const infrastructureApi = {
  list: () => request<InfrastructureProvider[]>("/api/v1/infrastructure/providers"),
  save: (id: string | undefined, input: Registration) => request<InfrastructureProvider>(`/api/v1/infrastructure/providers${id ? `/${encodeURIComponent(id)}` : ""}`, { method: id ? "PUT" : "POST", body: JSON.stringify(input) }),
  verify: (item: InfrastructureProvider) => request<InfrastructureProvider>(`/api/v1/infrastructure/providers/${encodeURIComponent(item.id)}/verify`, { method: "POST", body: JSON.stringify({ revision: item.revision }) }),
};
const capabilities = ["server.inspect", "server.create", "server.delete"];
const empty: Registration = { name: "", endpoint: "", privateNetworkId: "", credentialSecretId: "", enabled: true, capabilities };

export function InfrastructureProviders({ overview, extended = false }: { overview: Overview; extended?: boolean }) {
  const [page, setPage] = useState(0);
  const approvedOperations = extended ? [...capabilities, "snapshot.create", "snapshot.inspect", "snapshot.delete", "server.restore"] : capabilities;
  const operationLabels: Record<string, string> = { "server.create": "Create servers", "server.delete": "Delete servers", "server.inspect": "Inspect servers", "snapshot.create": "Capture snapshots", "snapshot.inspect": "Inspect snapshots", "snapshot.delete": "Delete snapshots", "server.restore": "Restore isolated clones" };
  const [items, setItems] = useState<InfrastructureProvider[]>([]);
  const [input, setInput] = useState<Registration>(empty);
  const [editing, setEditing] = useState<InfrastructureProvider | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  useEffect(() => {
    let current = true;
    infrastructureApi.list().then(value => { if (current) setItems(value); }).catch(() => { if (current) setError("Could not load infrastructure providers."); });
    return () => { current = false; };
  }, []);
  function keep(value: InfrastructureProvider) {
    setItems(previous => [...previous.filter(item => item.id !== value.id), value]);
    setMessage(value.state === "failed" ? "Provider saved. Resolve the verification failure before enabling it." : "Provider saved.");
  }
  async function save(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try { keep(await infrastructureApi.save(editing?.id, input)); setOpen(false); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save provider."); }
    finally { setBusy(false); }
  }
  async function change(item: InfrastructureProvider, verify: boolean) {
    setBusy(true); setError("");
    try { keep(verify ? await infrastructureApi.verify(item) : await infrastructureApi.save(item.id, { name: item.name, endpoint: item.endpoint, privateNetworkId: item.privateNetworkId, credentialSecretId: item.credentialSecretId, capabilities: item.capabilities, revision: item.revision, enabled: !item.enabled })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not update provider."); }
    finally { setBusy(false); }
  }
  return <section className="server-section" aria-labelledby="infrastructure-providers-title">
    <div className="section-title"><div><h2 id="infrastructure-providers-title">Infrastructure providers</h2><p>Create servers through an approved provider adapter.</p></div><button className="quiet-button" type="button" onClick={() => { setEditing(null); setInput(empty); setOpen(true); setError(""); }}>Register provider</button></div>
    {error && <p className="form-error" role="alert">{error}</p>}
    {message && <p role="status">{message}</p>}
    {open && <form className="connection-form inline-create" onSubmit={event => void save(event)} aria-label={editing ? "Edit infrastructure provider" : "Register infrastructure provider"}>
      <div className="connection-grid">
        <label>Provider name<input value={input.name} onChange={event => setInput({ ...input, name: event.target.value })} required maxLength={80} /></label>
        <label>Adapter endpoint<input type="url" value={input.endpoint} disabled={!!editing} onChange={event => setInput({ ...input, endpoint: event.target.value })} placeholder="https://provider.example.com" required /></label>
        <label>Provider route<select value={input.privateNetworkId || ""} disabled={!!editing} onChange={event => setInput({ ...input, privateNetworkId: event.target.value })}><option value="">Direct</option>{(overview.privateNetworks || []).filter(node => node.driver === "dispatch_agent").map(node => <option key={node.id} value={node.id}>{node.name}</option>)}</select></label>
        <label>Authentication secret<select value={input.credentialSecretId || ""} onChange={event => setInput({ ...input, credentialSecretId: event.target.value })}><option value="">No authentication</option>{overview.secrets.filter(secret => !["environment_variable", "environment_json", "json", "ssh_private_key"].includes(secret.type)).map(secret => <option key={secret.id} value={secret.id}>{secret.name}</option>)}</select><small>Create or rotate credentials in Secrets. Values are never displayed here.</small></label>
      </div>
      <fieldset><legend>Approved operations</legend>{approvedOperations.map(capability => <label key={capability}><input type="checkbox" checked={input.capabilities.includes(capability)} onChange={event => setInput({ ...input, capabilities: event.target.checked ? [...input.capabilities, capability] : input.capabilities.filter(value => value !== capability) })} />{operationLabels[capability]}</label>)}</fieldset>
      <label><input type="checkbox" checked={input.enabled} onChange={event => setInput({ ...input, enabled: event.target.checked })} />Enable approved operations after verification</label>
      <div className="connection-actions"><button className="quiet-button" type="button" onClick={() => setOpen(false)}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? "Verifying..." : "Save provider"}</button></div>
    </form>}
    {!items.length && !open && <p className="section-empty">No infrastructure providers registered.</p>}
    {(extended ? items.slice(page * 20, page * 20 + 20) : items).map(item => <article className="connection-row" key={item.id}>
      <div className="connection-identity"><div><strong>{item.name}</strong><small>{item.endpoint}</small></div></div>
      <div className="connection-metadata"><StatusLabel state={item.state} /><span>{item.manifest?.displayName || "Manifest unverified"}{item.manifest ? ` · ${item.manifest.version}` : ""}</span></div>
      {item.lastError && <p className="form-error" role="alert">{item.lastError}</p>}
      <div className="connection-row-actions"><button type="button" className="table-action" onClick={() => { setEditing(item); setInput({ name: item.name, endpoint: item.endpoint, privateNetworkId: item.privateNetworkId || "", credentialSecretId: item.credentialSecretId || "", enabled: item.enabled, capabilities: item.capabilities, revision: item.revision }); setOpen(true); }}>Edit provider</button><button type="button" className="table-action" disabled={busy} onClick={() => void change(item, true)}>Verify provider</button><button type="button" className="table-action" disabled={busy} onClick={() => void change(item, false)}>{item.enabled ? "Disable provider" : "Enable provider"}</button></div>
      {item.manifest && <details><summary>Configuration schema and capabilities</summary><p>{item.manifest.apiVersion} · {item.capabilities.join(", ")}</p><pre>{JSON.stringify(item.manifest.configurationSchema, null, 2)}</pre></details>}
    </article>)}
    {extended && items.length > 20 && <div className="resources-pagination"><span>{page * 20 + 1} to {Math.min(page * 20 + 20, items.length)} of {items.length}</span><button className="quiet-button" type="button" disabled={page === 0} onClick={() => setPage(value => value - 1)}>Previous</button><button className="quiet-button" type="button" disabled={(page + 1) * 20 >= items.length} onClick={() => setPage(value => value + 1)}>Next</button></div>}
  </section>;
}
