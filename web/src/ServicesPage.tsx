import { FormEvent, useEffect, useState } from "react";
import { CaretDown } from "@phosphor-icons/react";
import { api, Overview, ServiceConnection, ServiceInput, ServiceTemplate } from "./api";
import { PageHeader } from "./PageHeader";
import { canManageAnyProject, canManageProject } from "./permissions";
import { ApplicationServices } from "./ApplicationServices";
import { useDeploymentCatalog } from "./deployments/DeploymentCatalog";
import { ServiceOperations } from "./ServiceOperations";
import { ServiceTemplateForm } from "./ServiceTemplateForm";
import { ServiceTemplateEditor } from "./ServiceTemplateEditor";
import { WorkloadBackups } from "./WorkloadBackups";
import { ServiceResourcePanel } from "./ServiceResourcePanel";
import { ServiceTemplateCatalog } from "./ServiceTemplateCatalog";

type FieldRow = { name: string; value: string; sensitive: boolean; secretRef: string; source: "value" | "secret"; changed: boolean; saved: boolean };
const pgFields = ["host", "port", "database", "username", "password", "sslmode", "caCert"];

export function ServicesPage({ overview, onChanged }: { overview: Overview; onChanged: () => Promise<void> }) {
 const {items:catalog}=useDeploymentCatalog();
 const [project, setProject] = useState("");
 const [rotation,setRotation] = useState(false);
 const [editing, setEditing] = useState<ServiceConnection | "new" | null>(null);
 const [adding, setAdding] = useState(false);
 const [tab, setTab] = useState<"services" | "templates" | "backups">("services");
 const [templateEditing, setTemplateEditing] = useState<ServiceTemplate | "new" | null>(null);
 const [templatesLoading, setTemplatesLoading] = useState(true);
 const [template, setTemplate] = useState<ServiceTemplate | null>(null);
 const [templates, setTemplates] = useState<ServiceTemplate[]>([]);
 const [runs, setRuns] = useState<import("./api").ServiceProvisionRun[]>([]);
 const [templatesError, setTemplatesError] = useState("");
 async function refreshTemplates() {
  try { setTemplates(await api.serviceTemplates()); setTemplatesError(""); }
  catch (e) { setTemplatesError(e instanceof Error ? e.message : String(e)); }
  finally { setTemplatesLoading(false); }
 }
 useEffect(() => { void refreshTemplates(); }, []);
 useEffect(() => { const refresh = () => { void api.serviceProvisionRuns().then(setRuns).catch(() => {}); }; refresh(); const timer = window.setInterval(refresh, 5000); return () => window.clearInterval(timer); }, []);
 const [removing, setRemoving] = useState<string | null>(null);
 const [expanded, setExpanded] = useState<string[]>([]);
 const [impacts, setImpacts] = useState<string[]>([]);
 const [application, setApplication] = useState<string | null>(null);
 const [busy, setBusy] = useState("");
 const [error, setError] = useState<{ serviceId: string; message: string } | null>(null);
 const services = (overview.services ?? []).filter(s => !project || s.projectId === project);
 async function act(id: string, action: "check" | "remove") {
  setBusy(id); setError(null);
  try { if (action === "check") await api.verifyService(id); else { await api.deleteService(id); setRemoving(null); } await onChanged(); }
  catch (e) { setError({ serviceId: id, message: e instanceof Error ? e.message : String(e) }); } finally { setBusy(""); }
 }
 const managed=catalog.find(a=>a.appId===application);
 const app = overview.apps.find(a => a.id === application) ?? (managed ? {id:managed.appId,name:managed.appName,projectId:managed.projectId,generated:true,template:false} : undefined);
 if (app) return <ApplicationServices application={app} overview={overview} onChanged={onChanged} onBack={() => setApplication(null)} />;
 if (templateEditing) return <ServiceTemplateEditor key={templateEditing === "new" ? "new" : templateEditing.id} item={templateEditing === "new" ? undefined : templateEditing} overview={overview} initialProject={project} onBack={() => setTemplateEditing(null)} onSaved={async () => { await refreshTemplates(); setTemplateEditing(null); setTab("templates"); }} />;
 if (template) return <ServiceTemplateForm template={template} overview={overview} onBack={() => setTemplate(null)} onSaved={onChanged} />;
 if (editing) return <ServiceEditor rotation={rotation} key={editing === "new" ? "new" : editing.id} item={editing === "new" ? undefined : editing} overview={overview} initialProject={project} onBack={() => setEditing(null)} onSaved={async () => { await onChanged(); setEditing(null); }} />;
 if (adding) return <div className="page-layout services-page editor-page"><PageHeader view="services" title="Add service" action={{ label: "Back", onClick: () => setAdding(false), tone: "quiet" }} />
  <p className="service-intro">Choose how to add a service to a project.</p>
  <div className="service-add-options"><button className="service-add-option" onClick={() => {setAdding(false);setEditing("new")}}><strong>Connect an existing service</strong><span>Enter a connection URL or connection fields.</span></button>
  {templates.filter(item => (!project || item.projectId === project) && canManageProject(overview, item.projectId, "project.configure") && canManageProject(overview, item.projectId, "deployment.run")).map(item => <button key={item.id} className="service-add-option" onClick={() => {setAdding(false);setTemplate(item)}}><strong>{item.name}</strong><span>{item.description || `Create ${item.serviceType === "postgresql" ? "a PostgreSQL database" : "a service"} from a template.`}</span><small>{overview.projects.find(p => p.id === item.projectId)?.name}</small></button>)}</div>
  {!templates.length && <p className="service-help">No service templates yet. Create one here or sync ServiceTemplate YAML from a repository.</p>}
  {canManageAnyProject(overview, "project.configure") && <button className="quiet-button" onClick={() => {setAdding(false);setTemplateEditing("new")}}>Create template</button>}
  {templatesError && <p role="alert" className="error">{templatesError}</p>}
 </div>;
 return <div className="page-layout services-page">
  <PageHeader view="services" action={tab !== "backups" && canManageAnyProject(overview, "project.configure") ? { label: tab === "templates" ? "Create template" : "Add service", onClick: () => tab === "templates" ? setTemplateEditing("new") : setAdding(true) } : undefined} />
  <p className="service-intro">Create a service from a template or connect one that already exists.</p>
  <div className="service-mode service-tabs" role="group" aria-label="Services view"><button aria-pressed={tab === "services"} onClick={() => setTab("services")}>Services</button><button aria-pressed={tab === "templates"} onClick={() => setTab("templates")}>Templates</button><button aria-pressed={tab === "backups"} onClick={() => setTab("backups")}>Backups</button></div>
  {overview.projects.length > 1 && <label className="service-filter">Project<select value={project} onChange={e => setProject(e.target.value)}><option value="">All projects</option>{overview.projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>}
  {tab === "backups" ? <WorkloadBackups overview={overview} project={project} /> : tab === "templates" ? <>
   {templatesError ? <div role="alert" className="error">{templatesError}<button className="quiet-button" onClick={() => void refreshTemplates()}>Retry</button></div> : templatesLoading ? <p role="status">Loading templates…</p> : <ServiceTemplateCatalog templates={templates.filter(t => !project || t.projectId === project)} overview={overview} onEdit={setTemplateEditing} onUse={setTemplate} onChanged={refreshTemplates} />}
  </> : <>
  {runs.filter(run => (!project || run.projectId === project) && (run.state === "queued" || run.state === "running" || run.state === "failed")).slice(-5).reverse().map(run => <div key={run.id} className="service-provision-status" role="status"><strong>{run.serviceName} · {run.state === "failed" ? "Provisioning failed" : "Provisioning"}</strong><p>{run.error || run.phase || "Waiting for the provider to finish."}</p>{run.target && <ServiceResourcePanel runId={run.id} canManage={canManageProject(overview, run.projectId, "project.configure")} canRecover={canManageProject(overview, run.projectId, "deployment.run")} onChanged={onChanged} />}</div>)}
  {runs.filter(run => run.target && run.state === "succeeded" && (!project || run.projectId === project) && !services.some(service => service.provisionRunId === run.id)).slice(-10).reverse().map(run => <details className="service-card" key={run.id}><summary className="service-card-summary">{run.serviceName} · Retained resource history</summary><ServiceResourcePanel runId={run.id} canManage={canManageProject(overview, run.projectId, "project.configure")} canRecover={canManageProject(overview, run.projectId, "deployment.run")} onChanged={onChanged} /></details>)}
  {!services.length && <p className="empty-state">No services registered{project ? " in this project" : " yet"}.</p>}
  <div className="service-list">{services.map(service => {
   const manage = canManageProject(overview, service.projectId, "project.configure");
   const isExpanded = expanded.includes(service.id);
   const impactOpen = impacts.includes(service.id);
   return <details className="service-card" key={service.id} open={isExpanded} onToggle={e => { const open = e.currentTarget.open; setExpanded(current => open ? [...new Set([...current, service.id])] : current.filter(id => id !== service.id)); }}>
    <summary className="service-card-summary"><span><strong>{service.name}</strong><small>{service.type === "postgresql" ? "PostgreSQL" : "Generic"} · {overview.projects.find(p => p.id === service.projectId)?.name}{service.templateName ? ` · ${service.templateName}` : ""}</small></span><span className={`service-status service-status-${service.check?.state ?? "untested"}`}>{service.check?.state === "succeeded" ? "Check passed" : service.check?.state === "failed" ? "Check failed" : "Not tested"}</span><CaretDown size={18} aria-hidden="true" /></summary>
    {isExpanded && <div className="service-card-body">
     {service.description && <p>{service.description}</p>}
     {service.provisionTarget && <p className="service-help">{service.provisionTarget.provider === "neon" ? "Neon branch" : service.provisionTarget.provider === "docker" ? "Docker container" : "Helm release"} · <code>{service.provisionTarget.resourceName}</code>{service.provisionTarget.namespace ? ` · ${service.provisionTarget.namespace}` : ""} · {overview.servers.find(server => server.id === service.provisionTarget?.serverId)?.name ?? service.provisionTarget.serverId}</p>}
     {manage && <div className="service-actions"><button className="quiet-button" onClick={() => {setRotation(false);setEditing(service)}}>Edit</button><button className="quiet-button" onClick={() => {setRotation(true);setEditing(service)}}>Rotate credentials</button><button className="quiet-button" disabled={!!busy} onClick={() => void act(service.id, "check")}>{busy === service.id ? "Working…" : "Test connection"}</button></div>}
     {error?.serviceId === service.id && <p role="alert" className="error">{error.message}</p>}
     {service.check && <p className="service-check-result">{service.check.message} · {service.check.location} · {new Date(service.check.checkedAt).toLocaleString()}</p>}
     <p className="service-help">Connection checks run from the Dispatch controller. Application access can differ.</p>
     <details className="service-card-section"><summary>Connection details</summary><dl className="service-fields">{Object.entries(service.fields).map(([name, f]) => <div key={name}><dt>{name}</dt><dd>{f.sensitive ? f.configured ? "Configured · hidden" : "Not configured" : f.value || "Not configured"}</dd></div>)}</dl></details>
     <details className="service-card-section" open={impactOpen} onToggle={e => { const open = e.currentTarget.open; setImpacts(current => open ? [...new Set([...current, service.id])] : current.filter(id => id !== service.id)); }}><summary>Application impact{service.consumers.length ? ` · ${service.consumers.length}` : ""}</summary>{impactOpen && <ServiceOperations onBindings={setApplication} service={service} overview={overview} onChanged={onChanged} />}</details>
     {service.provisionRunId && service.provisionTarget && <ServiceResourcePanel runId={service.provisionRunId} canManage={manage} canRecover={canManageProject(overview, service.projectId, "deployment.run")} onChanged={onChanged} />}
     {manage && (removing === service.id ? <div className="service-actions"><p>Remove this registration? The external service remains unchanged.</p><button disabled={!!busy} className="danger-button" onClick={() => void act(service.id, "remove")}>Remove registration</button><button className="quiet-button" onClick={() => setRemoving(null)}>Cancel</button></div> : <button className="service-remove-trigger" onClick={() => setRemoving(service.id)}>Remove registration</button>)}
    </div>}
   </details>;
  })}</div>
  </>}
 </div>;
}

function ServiceEditor({ item, overview, initialProject, onBack, onSaved, rotation }: { rotation?: boolean; item?: ServiceConnection; overview: Overview; initialProject: string; onBack: () => void; onSaved: () => Promise<void> }) {
 const allowed = overview.projects.filter(p => canManageProject(overview, p.id, "project.configure"));
 const [projectId, setProject] = useState(item?.projectId ?? (allowed.some(p => p.id === initialProject) ? initialProject : allowed[0]?.id ?? ""));
 const [name, setName] = useState(item?.name ?? "");
 const [description, setDescription] = useState(item?.description ?? "");
 const [type, setType] = useState<ServiceConnection["type"]>(item?.type ?? "postgresql");
 const [urlMode, setUrlMode] = useState(!item);
 const [url, setUrl] = useState("");
 const [probeHost, setProbeHost] = useState(item?.probeHost ?? "");
 const [probePort, setProbePort] = useState(item?.probePort?.toString() ?? "");
 const [removed, setRemoved] = useState<string[]>([]);
 const [rows, setRows] = useState<FieldRow[]>(() => initialRows(item?.type ?? "postgresql", item));
 const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
 const owner = overview.identity?.systemRole === "owner";
 function change(index: number, patch: Partial<FieldRow>) { setRows(current => current.map((row, i) => i === index ? { ...row, ...patch, changed: true } : row)); }
 function pgControl(name: string) {
  const index = rows.findIndex(row => row.name === name);
  const row = rows[index];
  const label = ({ host: "Host", port: "Port", database: "Database", username: "Username", password: "Password", sslmode: "TLS mode", caCert: "CA certificate" } as Record<string, string>)[name];
  return <label key={name}>{label}
   {row.source === "secret" ? <select aria-label={`${label} secret`} disabled={!owner} required value={row.secretRef} onChange={e => change(index, { secretRef: e.target.value })}><option value="">Choose global secret</option>{overview.secrets.map(secret => <option key={secret.id} value={secret.id}>{secret.name}</option>)}</select>
    : name === "sslmode" ? <select value={row.value} onChange={e => change(index, { value: e.target.value })}>{["verify-full", "verify-ca", "require", "disable"].map(mode => <option key={mode}>{mode}</option>)}</select>
    : name === "caCert" ? <textarea aria-label={name} value={row.value} onChange={e => change(index, { value: e.target.value })} placeholder="Optional PEM certificate" />
    : <input aria-label={name} required={["host", "database", "username"].includes(name)} type={row.sensitive ? "password" : "text"} autoComplete={row.sensitive ? "new-password" : "off"} value={row.value} onChange={e => change(index, { value: e.target.value })} placeholder={row.saved && row.sensitive ? "Configured. Leave blank to keep." : name === "host" ? "db.example.com" : ""} />}
   {row.saved && row.sensitive && row.source === "value" && <small className="service-help">Leave blank to keep the saved value.</small>}
  </label>;
 }
 function sourceChoice(name: string) {
  const index = rows.findIndex(row => row.name === name);
  return <label key={name}>{name} source<select value={rows[index].source} onChange={e => change(index, { source: e.target.value as FieldRow["source"] })}><option value="value">Entered value</option><option value="secret">Global secret</option></select></label>;
 }
 async function save(e: FormEvent) {
  e.preventDefault(); setBusy(true); setError("");
  try {
   const fields: ServiceInput["fields"] = {};
   for (const key of removed) fields[key] = { remove: true };
   for (const row of rows) {
    if (removed.includes(row.name)) continue;
    if (urlMode && pgFields.includes(row.name) && row.name !== "caCert") continue;
    if (!row.name) throw new Error("Give each field a name.");
    if (rows.filter(r => r.name === row.name).length > 1) throw new Error("Field names must be unique.");
    if (!row.changed && row.saved) continue;
    if (row.source === "secret" && !row.secretRef) throw new Error(`Choose a global secret for ${row.name}.`);
    if (row.saved && row.sensitive && row.source === "value" && !row.secretRef && !row.value) continue;
    if (!row.saved && row.value === "" && row.secretRef === "" && ["password", "caCert"].includes(row.name)) continue;
    if (row.source === "value" && row.secretRef && !row.value) throw new Error(`Enter a replacement value for ${row.name}.`);
    fields[row.name] = row.source === "secret" ? { secretRef: row.secretRef } : { value: row.value, sensitive: row.sensitive };
   }
   await api.saveService(item?.id, { projectId, name, description, type, revision: item?.revision, fields, ...(urlMode ? { connectionUrl: url } : {}), probeHost: type === "generic" ? probeHost : "", probePort: type === "generic" && probePort ? Number(probePort) : 0 });
   await onSaved();
  } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); }
 }
 return <div className="page-layout services-page editor-page"><PageHeader view="services" title={item ? `${rotation ? "Rotate credentials for" : "Edit"} ${item.name}` : "Connect existing service"} action={{ label: "Back", onClick: onBack, tone: "quiet" }} />
  <form className="service-form" onSubmit={e => void save(e)}>
   <div className="service-form-grid"><label>Project<select disabled={!!item} value={projectId} onChange={e => setProject(e.target.value)}>{allowed.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label><label>Name<input required pattern="[a-z0-9][a-z0-9.\-]{0,62}" disabled={!!item} value={name} onChange={e => setName(e.target.value)} placeholder="orders-db-production" /></label><label>Type<select disabled={!!item} value={type} onChange={e => { const t = e.target.value as ServiceConnection["type"]; setType(t); setRows(initialRows(t)); setRemoved([]); setUrlMode(false); }}><option value="postgresql">PostgreSQL</option><option value="generic">Generic</option></select></label><label>Description<input value={description} onChange={e => setDescription(e.target.value)} /></label></div>
   {type === "postgresql" ? <section className="service-pg-form">
    <h2>PostgreSQL connection</h2>
    <p>Paste a connection URL from your database provider, or enter the details yourself.</p>
    <div className="service-mode" role="group" aria-label="Connection input"><button type="button" aria-pressed={urlMode} onClick={() => setUrlMode(true)}>Connection URL</button><button type="button" aria-pressed={!urlMode} onClick={() => setUrlMode(false)}>Enter details</button></div>
    {urlMode ? <>
     <label>PostgreSQL URL<input type="password" autoComplete="new-password" required value={url} onChange={e => setUrl(e.target.value)} placeholder="postgresql://user:password@host:5432/database" /></label>
     {item && <p className="service-help">Saving a URL replaces the saved host, database, username, password, and TLS mode.</p>}
     <details className="service-advanced"><summary>CA certificate and secret source</summary>{!removed.includes("caCert") && pgControl("caCert")}{item?.fields.caCert && <button type="button" className="quiet-button" onClick={() => setRemoved(current => current.includes("caCert") ? current.filter(name => name !== "caCert") : [...current, "caCert"])}>{removed.includes("caCert") ? "Keep CA certificate" : "Remove CA certificate"}</button>}{owner && sourceChoice("caCert")}</details>
    </> : <>
     <div className="service-form-grid">{["host", "database", "username", "password"].map(pgControl)}</div>
     <details className="service-advanced"><summary>Port, TLS, and secret sources</summary><div className="service-form-grid">{["port", "sslmode", "caCert"].filter(name => !removed.includes(name)).map(pgControl)}</div>{item?.fields.caCert && <button type="button" className="quiet-button" onClick={() => setRemoved(current => current.includes("caCert") ? current.filter(name => name !== "caCert") : [...current, "caCert"])}>{removed.includes("caCert") ? "Keep CA certificate" : "Remove CA certificate"}</button>}{owner && <div className="service-source-grid"><h3>Global secret sources</h3><p>Use a global secret instead of an entered value.</p>{["database", "username", "password", "caCert"].map(sourceChoice)}</div>}</details>
    </>}
   </section> : <fieldset><legend>Connection fields</legend>{rows.map((row, index) => <div className="service-field-row" key={index}>
    <label>Field{type === "generic" && !row.saved ? <input aria-label={`Field ${index + 1} name`} required value={row.name} onChange={e => change(index, { name: e.target.value })} /> : <strong>{row.name}</strong>}</label>
    {owner && <label>Source<select value={row.source} onChange={e => change(index, { source: e.target.value as FieldRow["source"] })}><option value="value">Value</option><option value="secret">Global secret</option></select></label>}
    {row.source === "secret" ? <label>Secret<select disabled={!owner} required value={row.secretRef} onChange={e => change(index, { secretRef: e.target.value })}><option value="">Choose secret</option>{overview.secrets.filter(s => s.type !== "environment_variable" && s.type !== "environment_json").map(s => <option key={s.id} value={s.id}>{s.name}</option>)}</select></label> : <label>{row.sensitive ? "Credential" : "Value"}{row.name === "sslmode" ? <select value={row.value} onChange={e => change(index, { value: e.target.value })}>{["verify-full", "verify-ca", "require", "disable"].map(mode => <option key={mode}>{mode}</option>)}</select> : row.name === "caCert" ? <textarea value={row.value} onChange={e => change(index, { value: e.target.value })} placeholder="Optional PEM CA certificate" /> : <input aria-label={row.name || `Field ${index + 1} value`} type={row.sensitive ? "password" : "text"} autoComplete={row.sensitive ? "new-password" : "off"} value={row.value} onChange={e => change(index, { value: e.target.value })} placeholder={row.saved && row.sensitive ? "Configured. Leave blank to keep." : ""} />}</label>}
    {type === "generic" && <label className="service-checkbox"><input type="checkbox" checked={row.sensitive} disabled={row.saved && row.sensitive} onChange={e => change(index, { sensitive: e.target.checked })} />Sensitive</label>}
    <button type="button" className="quiet-button" onClick={() => { if (row.saved) setRemoved(old => [...old, row.name]); setRows(old => old.filter((_, i) => i !== index)); }}>Remove field</button>
   </div>)}</fieldset>}
   {type === "generic" && <><button type="button" className="quiet-button" onClick={() => setRows(old => [...old, { name: "", value: "", sensitive: false, secretRef: "", source: "value", changed: true, saved: false }])}>Add field</button><details className="service-advanced"><summary>Connection check</summary><div className="service-form-grid"><label>TCP host<input value={probeHost} onChange={e => setProbeHost(e.target.value)} /></label><label>TCP port<input type="number" min="1" max="65535" value={probePort} onChange={e => setProbePort(e.target.value)} /></label></div></details></>}
   {rotation && <p>Replace the credential fields that changed. After saving, select affected applications in the impact table to redeploy them.</p>}
   <p>Credentials stay hidden after saving. Changes require redeploying dependent applications.</p>
   {error && <p role="alert" className="error">{error}</p>}<button className="primary-button" disabled={busy || !projectId}>{busy ? "Saving…" : "Save service"}</button>
  </form>
 </div>;
}
function initialRows(type: ServiceConnection["type"], item?: ServiceConnection): FieldRow[] {
 return (type === "postgresql" ? pgFields : Object.keys(item?.fields ?? {})).map(name => { const f = item?.fields[name]; return { name, value: f?.value ?? (name === "port" ? "5432" : name === "sslmode" ? "verify-full" : ""), sensitive: f?.sensitive ?? name === "password", secretRef: f?.secretRef ?? "", source: f?.secretRef ? "secret" : "value", changed: false, saved: !!f }; });
}
