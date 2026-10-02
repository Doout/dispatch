import { useEffect, useState } from "react";
import { api, NeonProvider, NeonProvision, Overview } from "./api";

export function NeonProvisionSettings({ value, projectId, overview, onChange }: { value: NeonProvision; projectId: string; overview: Overview; onChange: (value: NeonProvision) => void }) {
 const [providers, setProviders] = useState<NeonProvider[]>([]);
 const [adding, setAdding] = useState(false);
 const [name, setName] = useState("");
 const [endpoint, setEndpoint] = useState("https://console.neon.tech/api/v2");
 const [neonProjectId, setNeonProject] = useState("");
 const [parentBranchId, setParent] = useState("");
 const [credentialRef, setCredential] = useState("");
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState("");
 const owner = overview.identity?.systemRole === "owner";
 useEffect(() => {
  let active = true;
  if (!projectId) return;
  void api.neonProviders(projectId).then(items => { if (active) setProviders(items); }).catch(e => { if (active) setError(String(e)); });
  return () => { active = false; };
 }, [projectId]);
 async function removeProvider() {
  setBusy(true);setError("");try {await api.deleteNeonProvider(value.providerRef);setProviders(items=>items.filter(p=>p.id!==value.providerRef));onChange({...value,providerRef:""});}catch(e){setError(e instanceof Error?e.message:String(e));}finally{setBusy(false);}
 }
 async function createProvider() {
  setBusy(true); setError("");
  try {
   const provider = await api.createNeonProvider({ projectId, name, endpoint, neonProjectId, parentBranchId, credentialRef });
   setProviders(items => [...items, provider]); onChange({ ...value, providerRef: provider.id }); setAdding(false);
  } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  finally { setBusy(false); }
 }
 return <section className="service-template-section" aria-label="Neon provisioning">
  <h2>Neon database</h2>
  <p className="service-help">Each service owns an isolated branch and a new database role. Preview commits reuse their branch. Preview cleanup retains database data unless an operator reviews a different policy for the created branch.</p>
  <label>Project provider<select required value={value.providerRef} onChange={e => onChange({ ...value, providerRef: e.target.value })}><option value="">Choose a provider</option>{providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
  {owner && <button type="button" className="quiet-button" onClick={() => setAdding(!adding)}>{adding ? "Cancel provider setup" : "Add Neon provider"}</button>}
  {owner && value.providerRef && <button type="button" className="quiet-button" disabled={busy} onClick={() => void removeProvider()}>Remove unused provider</button>}
  {adding && owner && <fieldset disabled={busy}><legend>Existing Neon project</legend>
   <label>Provider name<input value={name} onChange={e => setName(e.target.value)} /></label>
   <label>API endpoint<input type="url" value={endpoint} onChange={e => setEndpoint(e.target.value)} /></label>
   <label>Neon project ID<input value={neonProjectId} onChange={e => setNeonProject(e.target.value)} /></label>
   <label>Parent branch ID<input value={parentBranchId} onChange={e => setParent(e.target.value)} /></label>
   <label>Scoped API credential<select value={credentialRef} onChange={e => setCredential(e.target.value)}><option value="">Choose saved secret</option>{overview.secrets.filter(s => !s.type?.startsWith("plain")).map(s => <option key={s.id} value={s.id}>{s.name}</option>)}</select></label>
   <p className="service-help">Use a Neon API key limited to this project. The saved API destination and project assignment are immutable.</p>
   <button type="button" className="quiet-button" disabled={busy || !name || !endpoint || !neonProjectId || !parentBranchId || !credentialRef} onClick={() => void createProvider()}>{busy ? "Saving…" : "Save provider"}</button>
  </fieldset>}
  <label>Database<input required value={value.database} onChange={e => onChange({ ...value, database: e.target.value })} /></label>
  <label>Initial data<select value={value.dataMode ?? "schema-only"} onChange={e => onChange({ ...value, dataMode: e.target.value as NeonProvision["dataMode"] })}><option value="schema-only">Schema only</option><option value="parent-data">Copy parent rows (owner confirmation)</option></select></label>
  {value.dataMode === "parent-data" && <p className="service-help">Automatic preview creation rejects this mode. A controller owner must explicitly approve each named data copy.</p>}
  <label>Idle compute suspension (seconds)<input type="number" min={0} max={604800} value={value.suspendAfterSeconds ?? 0} onChange={e => onChange({ ...value, suspendAfterSeconds: Number(e.target.value) })} /></label>
  <p className="service-help">Zero uses the Neon project default. Suspended compute wakes on a new connection.</p>
  {error && <p role="alert" className="error">{error}</p>}
 </section>;
}
