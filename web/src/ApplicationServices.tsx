import { FormEvent, useEffect, useState } from "react";
import { api, App, Overview, ServiceBinding } from "./api";
import { PageHeader } from "./PageHeader";
import { canManageProject } from "./permissions";

type Mapping = { destination: string; field: string; container: string };
export function ApplicationServices({ application, overview, onBack, onChanged }: { application: App; overview: Overview; onBack: () => void; onChanged: () => Promise<void> }) {
 const [bindings, setBindings] = useState<ServiceBinding[]>([]);
 const [loading, setLoading] = useState(true);
 const [error, setError] = useState("");
 const [busy, setBusy] = useState(false);
 const [editing, setEditing] = useState<ServiceBinding | "new" | null>(null);
 const writable = !application.generated && !application.template && canManageProject(overview, application.projectId, "project.configure");
 const services = (overview.services ?? []).filter(s => s.projectId === application.projectId);
 useEffect(() => { let active = true; setLoading(true); api.serviceBindings(application.id).then(items => { if (active) setBindings(items); }).catch(e => { if (active) setError(String(e)); }).finally(() => { if (active) setLoading(false); }); return () => { active = false; }; }, [application.id]);
 async function save(next: ServiceBinding[]) {
  setBusy(true); setError("");
  try { setBindings(await api.saveServiceBindings(application.id, next)); setEditing(null); await onChanged(); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); }
 }
 return <div className="page-layout services-page"><PageHeader view="services" title={`${application.name} services`} action={{ label: "Back", onClick: onBack, tone: "quiet" }} />
  {application.generated && <p>Bindings are managed in repository configuration. Edit deployment serviceBindings and stage overrides there.</p>}
  {application.template && <p>Configure bindings on a deployed application. Preview templates do not inherit service credentials.</p>}
  <p>Connection details are supplied to runtime containers. Saving bindings does not restart the application.</p>
  {error && <p role="alert" className="error">{error}</p>}
  {loading ? <p>Loading service bindings…</p> : <>
   {bindings.map(b => { const service = services.find(s => s.id === b.serviceRef); const consumer = service?.consumers.find(c => c.appId === application.id && c.alias === b.alias); return <section className="service-card" key={b.alias}><h2>{b.alias}</h2><p>{service?.name ?? "Service unavailable"} · {consumer?.redeploymentRequired ? "Redeployment required" : consumer?.appliedRevision ? `Applied revision ${consumer.appliedRevision}` : "Not deployed"}</p><dl className="service-fields">
    {Object.entries(b.environment ?? {}).map(([dest, field]) => <div key={dest}><dt>{dest}</dt><dd>{field}</dd></div>)}
    {Object.entries(b.compose ?? {}).flatMap(([container, envs]) => Object.entries(envs).map(([dest, field]) => <div key={`${container}/${dest}`}><dt>{container} / {dest}</dt><dd>{field}</dd></div>))}
    {b.helm && <><div><dt>Secret name chart values</dt><dd>{b.helm.secretNameValues.join(", ")}</dd></div>{Object.entries(b.helm.keys).map(([dest, field]) => <div key={dest}><dt>Secret key {dest}</dt><dd>{field}</dd></div>)}{Object.entries(b.helm.keyValues ?? {}).map(([path,key]) => <div key={path}><dt>{path}</dt><dd>Key name: {key}</dd></div>)}</>}
   </dl>{writable && <div className="service-actions"><button className="quiet-button" onClick={() => setEditing(b)}>Edit binding</button><button className="quiet-button" disabled={busy} onClick={() => void save(bindings.filter(item => item.alias !== b.alias))}>Remove binding</button></div>}</section>; })}
   {!bindings.length && <p>No services connected.</p>}
   {writable && !editing && <button className="primary-button" disabled={!services.length} onClick={() => setEditing("new")}>Connect service</button>}
   {writable && !services.length && <p>Register a service in this project before connecting it.</p>}
   {editing && <BindingForm key={editing === "new" ? "new" : editing.alias} binding={editing === "new" ? undefined : editing} application={application} services={services} busy={busy} onCancel={() => setEditing(null)} onSave={binding => { const oldAlias = editing === "new" ? null : editing.alias; if (bindings.some(b => b.alias === binding.alias && b.alias !== oldAlias)) { setError("Binding aliases must be unique."); return; } void save([...bindings.filter(b => b.alias !== oldAlias), binding]); }} />}
  </>}
 </div>;
}
function BindingForm({ binding, application, services, busy, onCancel, onSave }: { binding?: ServiceBinding; application: App; services: NonNullable<Overview["services"]>; busy: boolean; onCancel: () => void; onSave: (binding: ServiceBinding) => void }) {
 const [alias, setAlias] = useState(binding?.alias ?? "database");
 const [serviceRef, setServiceRef] = useState(binding?.serviceRef ?? services[0]?.id ?? "");
 const [paths, setPaths] = useState(binding?.helm?.secretNameValues.join("\n") ?? "database.existingSecret");
 const [keyPaths, setKeyPaths] = useState<{ path: string; key: string }[]>(Object.entries(binding?.helm?.keyValues ?? {}).map(([path, key]) => ({ path, key })));
 const [error, setError] = useState("");
 const [mappings, setMappings] = useState<Mapping[]>(() => {
  if (binding?.compose) return Object.entries(binding.compose).flatMap(([container, envs]) => Object.entries(envs).map(([destination, field]) => ({ destination, field, container })));
  const existing = binding?.helm?.keys ?? binding?.environment;
  return existing ? Object.entries(existing).map(([destination, field]) => ({ destination, field, container: "" })) : [{ destination: application.buildType === "helm" ? "connectionUrl" : "DATABASE_URL", field: services.find(s => s.id === (binding?.serviceRef ?? services[0]?.id))?.type === "postgresql" ? "connectionUrl" : "", container: "" }];
 });
 const service = services.find(s => s.id === serviceRef);
 function change(i: number, patch: Partial<Mapping>) { setMappings(old => old.map((m, index) => i === index ? { ...m, ...patch } : m)); }
 function submit(e: FormEvent) {
  e.preventDefault(); setError("");
  const destinations = mappings.map(m => `${m.container}/${m.destination}`);
  if (new Set(destinations).size !== mappings.length || new Set(keyPaths.map(k => k.path)).size !== keyPaths.length) { setError("Each destination must be unique."); return; }
  const b: ServiceBinding = { alias, serviceRef };
  if (application.buildType === "helm") b.helm = { keys: Object.fromEntries(mappings.map(m => [m.destination, m.field])), secretNameValues: paths.split("\n").map(p => p.trim()).filter(Boolean), keyValues: Object.fromEntries(keyPaths.map(k => [k.path, k.key])) };
  else if (application.buildType === "compose") { b.compose = {}; for (const m of mappings) { b.compose[m.container] ??= {}; b.compose[m.container][m.destination] = m.field; } }
  else b.environment = Object.fromEntries(mappings.map(m => [m.destination, m.field]));
  onSave(b);
 }
 return <form className="service-form" onSubmit={submit}><h2>{binding ? "Edit binding" : "Connect service"}</h2><div className="service-form-grid"><label>Alias<input required pattern="[a-z0-9][a-z0-9.\-]{0,62}" value={alias} onChange={e => setAlias(e.target.value)} /></label><label>Service<select required value={serviceRef} onChange={e => setServiceRef(e.target.value)}>{services.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}</select></label></div>
  {application.buildType === "helm" && <label>Chart value paths for Secret name, one per line<textarea required value={paths} onChange={e => setPaths(e.target.value)} /><span>Your chart must support references to an existing Kubernetes Secret.</span></label>}
  <fieldset><legend>{application.buildType === "helm" ? "Secret keys" : "Environment mappings"}</legend>{mappings.map((m, i) => <div className="service-field-row" key={i}>
   {application.buildType === "compose" && <label>Compose service<input required value={m.container} onChange={e => change(i, { container: e.target.value })} placeholder="api" /></label>}
   <label>{application.buildType === "helm" ? "Secret key" : "Environment variable"}<input required value={m.destination} onChange={e => change(i, { destination: e.target.value })} /></label>
   <label>Service field<select required value={m.field} onChange={e => change(i, { field: e.target.value })}><option value="">Choose field</option>{(service?.availableFields ?? []).map(f => <option key={f} value={f}>{f}</option>)}</select></label><button className="quiet-button" type="button" onClick={() => setMappings(old => old.filter((_, index) => index !== i))}>Remove mapping</button>
  </div>)}<button className="quiet-button" type="button" onClick={() => setMappings(old => [...old, { destination: "", field: "", container: "" }])}>Add mapping</button></fieldset>
  {application.buildType === "helm" && <fieldset><legend>Optional chart values for key names</legend>{keyPaths.map((k, i) => <div className="service-field-row" key={i}><label>Chart value path<input required value={k.path} onChange={e => setKeyPaths(old => old.map((v, n) => n === i ? { ...v, path: e.target.value } : v))} /></label><label>Secret key<select required value={k.key} onChange={e => setKeyPaths(old => old.map((v, n) => n === i ? { ...v, key: e.target.value } : v))}><option value="">Choose key</option>{mappings.map(m => <option key={m.destination} value={m.destination}>{m.destination}</option>)}</select></label><button type="button" className="quiet-button" onClick={() => setKeyPaths(old => old.filter((_, n) => n !== i))}>Remove key path</button></div>)}<button type="button" className="quiet-button" onClick={() => setKeyPaths(old => [...old, { path: "", key: "" }])}>Add key path</button></fieldset>}
  {error && <p role="alert">{error}</p>}<div className="service-actions"><button className="primary-button" disabled={busy || !mappings.length}>{busy ? "Saving…" : "Save binding"}</button><button type="button" className="quiet-button" onClick={onCancel}>Cancel</button></div>
 </form>;
}
