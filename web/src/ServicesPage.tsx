import { FormEvent, useState } from "react";
import { api, Overview, ServiceConnection, ServiceInput } from "./api";
import { PageHeader } from "./PageHeader";
import { canManageAnyProject, canManageProject } from "./permissions";
import { ApplicationServices } from "./ApplicationServices";

type FieldRow = { name: string; value: string; sensitive: boolean; secretRef: string; source: "value" | "secret"; changed: boolean; saved: boolean };
const pgFields = ["host", "port", "database", "username", "password", "sslmode", "caCert"];

export function ServicesPage({ overview, onChanged }: { overview: Overview; onChanged: () => Promise<void> }) {
 const [project, setProject] = useState("");
 const [editing, setEditing] = useState<ServiceConnection | "new" | null>(null);
 const [removing, setRemoving] = useState<string | null>(null);
 const [application, setApplication] = useState<string | null>(null);
 const [busy, setBusy] = useState("");
 const [error, setError] = useState("");
 const services = (overview.services ?? []).filter(s => !project || s.projectId === project);
 async function act(id: string, action: "check" | "remove") {
  setBusy(id); setError("");
  try { if (action === "check") await api.verifyService(id); else { await api.deleteService(id); setRemoving(null); } await onChanged(); }
  catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(""); }
 }
 const app = overview.apps.find(a => a.id === application);
 if (app) return <ApplicationServices application={app} overview={overview} onChanged={onChanged} onBack={() => setApplication(null)} />;
 if (editing) return <ServiceEditor key={editing === "new" ? "new" : editing.id} item={editing === "new" ? undefined : editing} overview={overview} initialProject={project} onBack={() => setEditing(null)} onSaved={async () => { await onChanged(); setEditing(null); }} />;
 return <div className="page-layout services-page">
  <PageHeader view="services" action={canManageAnyProject(overview, "project.configure") ? { label: "Register service", onClick: () => setEditing("new") } : undefined} />
  <p>Register connections to existing databases and other application dependencies.</p>
  <label className="service-filter">Project<select value={project} onChange={e => setProject(e.target.value)}><option value="">All projects</option>{overview.projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
  {error && <p role="alert" className="error">{error}</p>}
  {!services.length && <p className="empty-state">No services registered in this project.</p>}
  <div className="service-list">{services.map(service => {
   const manage = canManageProject(overview, service.projectId, "project.configure");
   return <section className="service-card" key={service.id}>
    <div className="service-card-header"><div><h2>{service.name}</h2><p>{service.type === "postgresql" ? "PostgreSQL" : "Generic"} · {overview.projects.find(p => p.id === service.projectId)?.name} · Revision {service.revision}</p></div>{manage && <button className="quiet-button" onClick={() => setEditing(service)}>Edit</button>}</div>
    {service.description && <p>{service.description}</p>}
    <dl className="service-fields">{Object.entries(service.fields).map(([name, f]) => <div key={name}><dt>{name}</dt><dd>{f.sensitive ? f.configured ? "Configured · hidden" : "Not configured" : f.value || "Not configured"}</dd></div>)}</dl>
    <div className="service-check"><strong>{service.check?.state === "succeeded" ? "Connection succeeded" : service.check?.state === "failed" ? "Connection failed" : "Not tested"}</strong>{service.check && <p>{service.check.message}<br />{service.check.location} · {new Date(service.check.checkedAt).toLocaleString()} · {service.check.durationMs} ms</p>}<p>A controller check does not verify access from your application.</p>{manage && <button className="quiet-button" disabled={!!busy} onClick={() => void act(service.id, "check")}>{busy === service.id ? "Working…" : "Test connection"}</button>}</div>
    <h3>Applications</h3>{!service.consumers.length ? <p>No application bindings.</p> : <ul>{service.consumers.map(c => <li key={`${c.appId}/${c.alias}`}><button className="quiet-button" onClick={() => setApplication(c.appId)}>{c.appName}</button> · {c.alias} · {c.redeploymentRequired ? "Redeployment required" : `Applied revision ${c.appliedRevision}`}</li>)}</ul>}
    {manage && (removing === service.id ? <div className="service-actions"><p>Remove this connection registration? The external service remains unchanged.</p><button disabled={!!busy} className="danger-button" onClick={() => void act(service.id, "remove")}>Remove registration</button><button className="quiet-button" onClick={() => setRemoving(null)}>Cancel</button></div> : <button className="quiet-button" onClick={() => setRemoving(service.id)}>Remove registration</button>)}
   </section>;
  })}</div>
 </div>;
}

function ServiceEditor({ item, overview, initialProject, onBack, onSaved }: { item?: ServiceConnection; overview: Overview; initialProject: string; onBack: () => void; onSaved: () => Promise<void> }) {
 const allowed = overview.projects.filter(p => canManageProject(overview, p.id, "project.configure"));
 const [projectId, setProject] = useState(item?.projectId ?? (allowed.some(p => p.id === initialProject) ? initialProject : allowed[0]?.id ?? ""));
 const [name, setName] = useState(item?.name ?? "");
 const [description, setDescription] = useState(item?.description ?? "");
 const [type, setType] = useState<ServiceConnection["type"]>(item?.type ?? "postgresql");
 const [urlMode, setUrlMode] = useState(false);
 const [url, setUrl] = useState("");
 const [probeHost, setProbeHost] = useState(item?.probeHost ?? "");
 const [probePort, setProbePort] = useState(item?.probePort?.toString() ?? "");
 const [removed, setRemoved] = useState<string[]>([]);
 const [rows, setRows] = useState<FieldRow[]>(() => initialRows(item?.type ?? "postgresql", item));
 const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
 const owner = overview.identity?.systemRole === "owner";
 function change(index: number, patch: Partial<FieldRow>) { setRows(current => current.map((row, i) => i === index ? { ...row, ...patch, changed: true } : row)); }
 async function save(e: FormEvent) {
  e.preventDefault(); setBusy(true); setError("");
  try {
   const fields: ServiceInput["fields"] = {};
   for (const key of removed) fields[key] = { remove: true };
   for (const row of rows) {
    if (urlMode && pgFields.includes(row.name) && row.name !== "caCert") continue;
    if (!row.name) throw new Error("Give each field a name.");
    if (rows.filter(r => r.name === row.name).length > 1) throw new Error("Field names must be unique.");
    if (!row.changed && row.saved) continue;
    if (!row.saved && row.value === "" && row.secretRef === "" && ["password", "caCert"].includes(row.name)) continue;
    fields[row.name] = row.source === "secret" ? { secretRef: row.secretRef } : { value: row.value, sensitive: row.sensitive };
   }
   await api.saveService(item?.id, { projectId, name, description, type, revision: item?.revision, fields, ...(urlMode ? { connectionUrl: url } : {}), probeHost: type === "generic" ? probeHost : "", probePort: type === "generic" && probePort ? Number(probePort) : 0 });
   await onSaved();
  } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); }
 }
 return <div className="page-layout services-page editor-page"><PageHeader view="services" title={item ? `Edit ${item.name}` : "Register service"} action={{ label: "Back", onClick: onBack, tone: "quiet" }} />
  <form className="service-form" onSubmit={e => void save(e)}>
   <div className="service-form-grid"><label>Project<select disabled={!!item} value={projectId} onChange={e => setProject(e.target.value)}>{allowed.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label><label>Name<input required pattern="[a-z0-9][a-z0-9.\-]{0,62}" disabled={!!item} value={name} onChange={e => setName(e.target.value)} placeholder="orders-db-production" /></label><label>Type<select disabled={!!item} value={type} onChange={e => { const t = e.target.value as ServiceConnection["type"]; setType(t); setRows(initialRows(t)); setRemoved([]); setUrlMode(false); }}><option value="postgresql">PostgreSQL</option><option value="generic">Generic</option></select></label><label>Description<input value={description} onChange={e => setDescription(e.target.value)} /></label></div>
   {type === "postgresql" && <><label>Connection input<select value={urlMode ? "url" : "fields"} onChange={e => setUrlMode(e.target.value === "url")}><option value="fields">Individual fields</option><option value="url">Connection URL</option></select></label>{urlMode && <label>PostgreSQL connection URL<input type="password" autoComplete="new-password" required value={url} onChange={e => setUrl(e.target.value)} placeholder="postgresql://user:password@host:5432/database" /></label>}</>}
   <fieldset><legend>Connection fields</legend>{rows.map((row, index) => urlMode && row.name !== "caCert" ? null : <div className="service-field-row" key={index}>
    <label>Field{type === "generic" && !row.saved ? <input aria-label={`Field ${index + 1} name`} required value={row.name} onChange={e => change(index, { name: e.target.value })} /> : <strong>{row.name}</strong>}</label>
    {owner && <label>Source<select value={row.source} onChange={e => change(index, { source: e.target.value as FieldRow["source"] })}><option value="value">Value</option><option value="secret">Global secret</option></select></label>}
    {row.source === "secret" ? <label>Secret<select disabled={!owner} required value={row.secretRef} onChange={e => change(index, { secretRef: e.target.value })}><option value="">Choose secret</option>{overview.secrets.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}</select></label> : <label>{row.sensitive ? "Credential" : "Value"}{row.name === "sslmode" ? <select value={row.value} onChange={e => change(index, { value: e.target.value })}>{["verify-full", "verify-ca", "require", "disable"].map(mode => <option key={mode}>{mode}</option>)}</select> : row.name === "caCert" ? <textarea value={row.value} onChange={e => change(index, { value: e.target.value })} placeholder="Optional PEM CA certificate" /> : <input aria-label={row.name || `Field ${index + 1} value`} type={row.sensitive ? "password" : "text"} autoComplete={row.sensitive ? "new-password" : "off"} value={row.value} onChange={e => change(index, { value: e.target.value })} placeholder={row.saved && row.sensitive ? "Configured. Leave blank to keep." : ""} />}</label>}
    {type === "generic" && <label className="service-checkbox"><input type="checkbox" checked={row.sensitive} disabled={row.saved && row.sensitive} onChange={e => change(index, { sensitive: e.target.checked })} />Sensitive</label>}
    <button type="button" className="quiet-button" onClick={() => { if (row.saved) setRemoved(old => [...old, row.name]); setRows(old => old.filter((_, i) => i !== index)); }}>Remove field</button>
   </div>)}</fieldset>
   {type === "generic" && <><button type="button" className="quiet-button" onClick={() => setRows(old => [...old, { name: "", value: "", sensitive: false, secretRef: "", source: "value", changed: true, saved: false }])}>Add field</button><div className="service-form-grid"><label>Optional TCP check host<input value={probeHost} onChange={e => setProbeHost(e.target.value)} /></label><label>TCP check port<input type="number" min="1" max="65535" value={probePort} onChange={e => setProbePort(e.target.value)} /></label></div></>}
   <p>Credentials stay hidden after saving. Changes require redeploying dependent applications.</p>
   {error && <p role="alert" className="error">{error}</p>}<button className="primary-button" disabled={busy || !projectId}>{busy ? "Saving…" : "Save service"}</button>
  </form>
 </div>;
}
function initialRows(type: ServiceConnection["type"], item?: ServiceConnection): FieldRow[] {
 return (type === "postgresql" ? pgFields : Object.keys(item?.fields ?? {})).map(name => { const f = item?.fields[name]; return { name, value: f?.value ?? (name === "port" ? "5432" : name === "sslmode" ? "verify-full" : ""), sensitive: f?.sensitive ?? name === "password", secretRef: f?.secretRef ?? "", source: f?.secretRef ? "secret" : "value", changed: false, saved: !!f }; });
}
