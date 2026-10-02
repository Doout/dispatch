import { FormEvent, useEffect, useRef, useState } from "react";
import { Overview, request } from "./api";
import { InfrastructureProvider, infrastructureApi } from "./InfrastructureProviders";

type Rule = { providerId: string; regions: string[]; sizes: string[]; anyRegion: boolean; anySize: boolean };
export type QuotaPolicy = { projectId: string; configured: boolean; revision: number; maxServers: number; maxTemporaryEnvironments: number; maxSnapshots: number; maxTemporaryLifetimeSeconds: number; providers: Rule[] };
type Reservation = { serverId: string; operationId: string; providerId: string; region: string; size: string; state: string; resourceId?: string };
export type QuotaView = { policy: QuotaPolicy; usage: { snapshots?: number; servers: number; reserved: number; allocated: number; unknown: number }; reservations: Reservation[] };
export const infrastructureQuotaApi = {
  get: (id: string) => request<QuotaView>(`/api/v1/projects/${encodeURIComponent(id)}/infrastructure/quota`),
  save: (id: string, policy: QuotaPolicy) => request<QuotaPolicy>(`/api/v1/projects/${encodeURIComponent(id)}/infrastructure/quota`, { method: "PUT", body: JSON.stringify(policy) }),
};
const list = (value: string) => value.split(",");

export function InfrastructureQuotas({ overview, canManage }: { overview: Overview; canManage: boolean }) {
  const projects = overview.projects.filter(project => canManage || (overview.projectPermissions?.[project.id] || []).includes("infrastructure.inspect"));
  const [project, setProject] = useState(projects[0]?.id || "");
  const currentProject = useRef(project);
  currentProject.current = project;
  const [view, setView] = useState<QuotaView | null>(null);
  const [policy, setPolicy] = useState<QuotaPolicy | null>(null);
  const [providers, setProviders] = useState<InfrastructureProvider[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (!projects.some(item => item.id === project)) setProject(projects[0]?.id || ""); }, [overview.projects, project, canManage]);
  useEffect(() => {
    let current = true;
    setView(null); setPolicy(null); setError(""); setNotice("");
    if (project) infrastructureQuotaApi.get(project).then(value => { if (current) { setView(value); setPolicy(value.policy); } }).catch(cause => { if (current) setError(cause instanceof Error ? cause.message : "Could not load resource policy."); });
    return () => { current = false; };
  }, [project]);
  useEffect(() => {
    let current = true;
    if (canManage) infrastructureApi.list().then(value => { if (current) setProviders(value); }).catch(() => { if (current) setError("Could not load provider choices."); });
    return () => { current = false; };
  }, [canManage]);
  function changeRule(id: string, fields: Partial<Rule>) {
    if (policy) setPolicy({ ...policy, providers: policy.providers.map(rule => rule.providerId === id ? { ...rule, ...fields } : rule) });
  }
  async function save(event: FormEvent) {
    event.preventDefault(); if (!policy || policy.projectId !== project) return;
    const target = project; setBusy(true); setError(""); setNotice("");
    try {
      const saved = await infrastructureQuotaApi.save(target, { ...policy, providers: policy.providers.map(rule => ({ ...rule, regions: rule.regions.map(value => value.trim()).filter(Boolean), sizes: rule.sizes.map(value => value.trim()).filter(Boolean) })) });
      const latest = await infrastructureQuotaApi.get(target);
      if (currentProject.current !== target) return;
      setPolicy(saved); setView(latest); setNotice("Resource policy saved. Existing servers keep running.");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save resource policy."); }
    finally { setBusy(false); }
  }
  if (!projects.length) return null;
  return <section className="server-section infrastructure-quota" aria-labelledby="infrastructure-quotas-title">
    <div className="section-title"><div><h2 id="infrastructure-quotas-title">Project resource limits</h2><p>Count accepted allocations and unresolved operations before another server can be created.</p></div></div>
    <label>Resource policy project<select value={project} disabled={busy} onChange={event => setProject(event.target.value)}>{projects.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
    {error && <p role="alert" className="form-error">{error}</p>}
    {notice && <p role="status">{notice}</p>}
    {view && policy && <>
      <p><strong>{view.usage.servers}</strong> servers counted · {view.usage.reserved} reserved · {view.usage.allocated} allocated · {view.usage.unknown} unresolved · {view.usage.snapshots ?? 0} retained or pending snapshots</p>
      {!policy.configured && <p>Self-service allocation is disabled until an owner saves a resource policy.</p>}
      {canManage ? <form className="connection-form inline-create" onSubmit={event => void save(event)} aria-label="Project resource policy">
        <label>Maximum snapshots<input type="number" required min={-1} step={1} value={policy.maxSnapshots} onChange={event => setPolicy({ ...policy, maxSnapshots: Number(event.target.value) })} /><small>Pending and unresolved snapshots count until verified deletion. Use -1 for unlimited.</small></label>
        <label>Maximum servers<input type="number" required min={-1} step={1} value={policy.maxServers} onChange={event => setPolicy({ ...policy, maxServers: Number(event.target.value) })} /><small>Use 0 to block new allocations, or -1 for unlimited servers.</small></label>
        <fieldset><legend>Allowed providers and machines</legend>
          {providers.map(provider => {
            const rule = policy.providers.find(item => item.providerId === provider.id);
            return <div key={provider.id}>
              <label className="quota-switch"><input type="checkbox" checked={!!rule} onChange={event => setPolicy({ ...policy, providers: event.target.checked ? [...policy.providers, { providerId: provider.id, regions: [], sizes: [], anyRegion: false, anySize: false }] : policy.providers.filter(item => item.providerId !== provider.id) })} />{provider.name}</label>
              {rule && <div className="connection-grid">
                <label>Allowed regions for {provider.name}<input value={rule.regions.join(",")} disabled={rule.anyRegion} onChange={event => changeRule(provider.id, { regions: list(event.target.value) })} placeholder="eu-1, us-1" /></label>
                <label className="quota-switch"><input type="checkbox" checked={rule.anyRegion} onChange={event => changeRule(provider.id, { anyRegion: event.target.checked, regions: [] })} />Allow every region for {provider.name}</label>
                <label>Allowed sizes for {provider.name}<input value={rule.sizes.join(",")} disabled={rule.anySize} onChange={event => changeRule(provider.id, { sizes: list(event.target.value) })} placeholder="small, medium" /></label>
                <label className="quota-switch"><input type="checkbox" checked={rule.anySize} onChange={event => changeRule(provider.id, { anySize: event.target.checked, sizes: [] })} />Allow every size for {provider.name}</label>
              </div>}
            </div>;
          })}
          <p>Empty region or size lists allow no choices. Project provider assignments and identity permissions also apply.</p>
        </fieldset>
        <div className="connection-actions"><button className="primary-button" disabled={busy}>{busy ? "Saving..." : "Save resource policy"}</button></div>
      </form> : <p>Maximum servers: {policy.maxServers === -1 ? "Unlimited" : policy.maxServers}. A controller owner manages this policy.</p>}
      {!!view.reservations.length && <details><summary>Allocation and recovery records</summary>{view.reservations.map(item => <article className="connection-row" key={item.serverId}><div><strong>{item.serverId}</strong><p>{item.region} · {item.size} · {item.state}</p><small>Operation {item.operationId}{item.resourceId ? ` · Provider resource ${item.resourceId}` : ""}</small>{item.state === "unknown" && <p>Inspect the original operation and owned resource before retrying. This reservation still counts toward the limit.</p>}</div></article>)}</details>}
    </>}
  </section>;
}
