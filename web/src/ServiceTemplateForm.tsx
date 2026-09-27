import { FormEvent, useEffect, useState } from "react";
import { api, Overview, ServiceProvisionRun, ServiceTemplate } from "./api";
import { PageHeader } from "./PageHeader";

export function ServiceTemplateForm({ template, overview, onBack, onSaved }: { template: ServiceTemplate; overview: Overview; onBack: () => void; onSaved: () => Promise<void> }) {
 const [name, setName] = useState("");
 const [description, setDescription] = useState("");
 const [inputs, setInputs] = useState<Record<string,string>>({});
 const [run, setRun] = useState<ServiceProvisionRun | null>(null);
 const [error, setError] = useState("");
 const [busy, setBusy] = useState(false);
 useEffect(() => {
  if (!run || !["queued", "running"].includes(run.state)) return;
  const timer = window.setInterval(() => { void api.serviceProvisionRun(run.id).then(next => {
   setRun(next);
   if (next.state === "succeeded") void onSaved();
  }).catch(e => setError(e instanceof Error ? e.message : String(e))); }, 2000);
  return () => window.clearInterval(timer);
 }, [run?.id, run?.state, onSaved]);
 async function submit(e: FormEvent) {
  e.preventDefault(); setBusy(true); setError("");
  try { setRun(await api.startServiceProvision(template.id, { name, description, inputs })); setInputs({}); }
  catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  finally { setBusy(false); }
 }
 const project = overview.projects.find(p => p.id === template.projectId);
 const entries = Object.entries(template.inputs);
 return <div className="page-layout services-page editor-page"><PageHeader view="services" title={template.name} action={{ label: "Back", onClick: onBack, tone: "quiet" }} />
  <p className="service-intro">{template.description || `Create a ${template.serviceType === "postgresql" ? "PostgreSQL" : "generic"} service from this template.`}</p>
  <p className="service-help">Project: {project?.name ?? template.projectId} · Configuration: {template.configSha.slice(0, 12)}</p>
  <form className="service-form" onSubmit={e => void submit(e)}>
   <div className="service-form-grid"><label>Service name<input required pattern="[a-z0-9][a-z0-9.\-]{0,62}" disabled={!!run} value={name} onChange={e => setName(e.target.value)} placeholder="orders-db" /></label><label>Description<input disabled={!!run} value={description} onChange={e => setDescription(e.target.value)} /></label></div>
   {entries.length > 0 && <fieldset><legend>Provisioning inputs</legend><div className="service-form-grid">{entries.map(([key, field]) => <label key={key}>{field.label || key}{field.type === "service" ? <select required={field.required} disabled={!!run} value={inputs[key] || ""} onChange={e => setInputs(old => ({ ...old, [key]: e.target.value }))}><option value="">Choose a service</option>{(overview.services ?? []).filter(service => service.projectId === template.projectId && service.type === field.serviceType).map(service => <option key={service.id} value={service.id}>{service.name}</option>)}</select> : <input required={field.required} disabled={!!run} type={field.type === "secret" ? "password" : "text"} autoComplete={field.type === "secret" ? "new-password" : "off"} value={inputs[key] || ""} onChange={e => setInputs(old => ({ ...old, [key]: e.target.value }))} />}{field.description && <small className="service-help">{field.description}</small>}</label>)}</div></fieldset>}
   <p>The template provisions the resource and saves its connection details as a Service. Credentials stay hidden after saving.</p>
   {run && <div className="service-provision-status" role="status"><strong>{run.state === "succeeded" ? "Service ready" : run.state === "failed" ? "Provisioning failed" : "Provisioning in progress"}</strong><p>{run.error || (run.state === "succeeded" ? "The service is available to applications in this project." : run.phase || "Waiting for the provider to finish.")}</p>{run.state === "failed" && <p>Check the provider before trying again; it may already have created the resource.</p>}</div>}
   {error && <p role="alert" className="error">{error}</p>}
   {!run && <button className="primary-button" disabled={busy}>{busy ? "Starting…" : "Create service"}</button>}
  </form>
 </div>;
}
