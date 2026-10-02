import { FormEvent, useEffect, useState } from "react";
import { parse, stringify } from "yaml";
import { ArrowLeft } from "@phosphor-icons/react";
import { api, Overview, ServiceTemplate, NeonProvision } from "./api";
import { PageHeader } from "./PageHeader";
import { ServiceProvisionSettings, DockerProvision, HelmProvision } from "./ServiceProvisionSettings";
import { NeonProvisionSettings } from "./NeonProvisionSettings";
import { canManageProject } from "./permissions";

type Input = ServiceTemplate["inputs"][string];
type Output = ServiceTemplate["outputs"][string];
type Definition = {
 apiVersion: string;
 kind: string;
 metadata: { name: string };
 spec: {
  description?: string;
  serviceType: "postgresql" | "generic";
  inputs?: Record<string, Input>;
  sources?: Record<string, unknown>;
  provision: { neon?: NeonProvision; docker?: DockerProvision; helm?: HelmProvision; run?: string; builder?: string; outputs?: string[]; [key: string]: unknown };
  outputs: Record<string, Output>;
 };
};
const initial: Definition = { apiVersion: "dispatch/v1alpha1", kind: "ServiceTemplate", metadata: { name: "" }, spec: { serviceType: "postgresql", inputs: {}, provision: { docker: { serverRef: "" } }, outputs: { connectionUrl: { sensitive: true } } } };
const pgOutputs = { host: {}, port: {}, database: {}, username: {}, password: { sensitive: true }, sslmode: {} };

function readDefinition(text: string): Definition {
 const value = parse(text) as Definition;
 if (value?.apiVersion !== initial.apiVersion || value?.kind !== "ServiceTemplate" || typeof value?.metadata?.name !== "string" || !value.spec || !["postgresql", "generic"].includes(value.spec.serviceType) || (!value.spec.provision || !value.spec.provision.docker && !value.spec.provision.helm && !value.spec.provision.neon && typeof value.spec.provision.run !== "string") || (value.spec.outputs && (typeof value.spec.outputs !== "object" || Array.isArray(value.spec.outputs)))) throw new Error("Provide a ServiceTemplate document with a name, service type, provisioner, and connection fields.");
 value.spec.outputs ??= value.spec.serviceType === "postgresql" ? { connectionUrl: { sensitive: true } } : {};
 for (const map of [value.spec.inputs ?? {}, value.spec.outputs]) {
  if (Array.isArray(map) || typeof map !== "object" || Object.values(map).some(field => !field || typeof field !== "object" || Array.isArray(field))) throw new Error("Inputs and outputs must be named fields with an object for each field.");
 }
 return value;
}

export function ServiceTemplateEditor({ item, overview, initialProject, onBack, onSaved }: { item?: ServiceTemplate; overview: Overview; initialProject: string; onBack: () => void; onSaved: () => Promise<void> }) {
 const allowed = overview.projects.filter(p => canManageProject(overview, p.id, "project.configure"));
 const [projectId, setProject] = useState(item?.projectId ?? (allowed.some(p => p.id === initialProject) ? initialProject : allowed[0]?.id ?? ""));
 const [sourceId, setSourceId] = useState(item?.configSourceId ?? "");
 const [definition, setDefinition] = useState<Definition>(initial);
 const [yaml, setYAML] = useState("");
 const [mode, setMode] = useState<"form" | "yaml">("form");
 const [loading, setLoading] = useState(!!item);
 const [loadFailed, setLoadFailed] = useState(false);
 const [revision, setRevision] = useState(item?.revision);
 const [error, setError] = useState("");
 const [busy, setBusy] = useState(false);
 const provider = definition.spec.provision.neon ? "neon" : definition.spec.provision.docker ? "docker" : definition.spec.provision.helm ? "helm" : "script";
 const readOnly = !!item && (item.managedBy === "gitops" || !canManageProject(overview, item.projectId, "project.configure"));
 useEffect(() => {
  if (!item) return;
  let active = true;
  void api.serviceTemplate(item.id).then(saved => {
   if (!active) return;
   const document = saved.document ?? "";
   setYAML(document); setRevision(saved.revision); setSourceId(saved.configSourceId ?? "");
   // The YAML editor can still show a valid server document with fields that
   // the simple form does not yet support.
   try { setDefinition(readDefinition(document)); } catch { setMode("yaml"); }
  }).catch(e => { if (active) { setError(e instanceof Error ? e.message : String(e)); setLoadFailed(true); } }).finally(() => { if (active) setLoading(false); });
  return () => { active = false; };
 }, [item]);

 function spec(patch: Partial<Definition["spec"]>) { setDefinition(d => ({ ...d, spec: { ...d.spec, ...patch } })); }
 function switchMode(next: "form" | "yaml") {
  if (next === mode) return;
  setError("");
  if (next === "yaml") setYAML(stringify(definition));
  else { try { setDefinition(readDefinition(yaml)); } catch (e) { setError(e instanceof Error ? e.message : String(e)); return; } }
  setMode(next);
 }
 function inputs(rows: [string, Input][]) { spec({ inputs: Object.fromEntries(rows) }); }
 function updateInput(index: number, name: string, field: Input) {
  const rows = Object.entries(definition.spec.inputs ?? {});
  if (rows.some(([key], i) => key === name && i !== index)) { setError("Each input needs a unique name."); return; }
  rows[index] = [name, field]; inputs(rows);
 }
 function addInput() {
  const rows = Object.entries(definition.spec.inputs ?? {});
  let suffix = rows.length + 1;
  while (rows.some(([key]) => key === `input${suffix}`)) suffix++;
  inputs([...rows, [`input${suffix}`, { type: "string", required: true }]]);
 }
 function outputs(rows: [string, Output][]) { spec({ outputs: Object.fromEntries(rows), provision: { ...definition.spec.provision, outputs: rows.map(([name]) => name) } }); }
 async function save(event: FormEvent) {
  event.preventDefault(); setError(""); setBusy(true);
  try {
   const document = mode === "yaml" ? yaml : stringify(definition);
   await api.saveServiceTemplate(item?.id, { projectId, configSourceId: sourceId, document, revision });
   await onSaved();
  } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  finally { setBusy(false); }
 }
 if (loading) return <div className="page-layout services-page"><PageHeader view="services" title="Service template" action={{ label: "Back", tone: "quiet", icon: <ArrowLeft size={16} />, onClick: onBack }} /><p role="status">Loading template…</p></div>;
 return <div className="page-layout services-page editor-page">
  <PageHeader view="services" title={readOnly ? item.name : item ? "Edit service template" : "Create service template"} action={{ label: "Back", tone: "quiet", icon: <ArrowLeft size={16} />, onClick: onBack }} />
  <p className="service-intro">{item?.managedBy === "gitops" ? "This template is managed in a repository. Edit its YAML there." : "Choose how Dispatch creates the service and where it runs."}</p>
  <form className="service-form service-template-editor" onSubmit={e => void save(e)}>
   <fieldset disabled={readOnly || busy || loadFailed} className="service-template-basics">
    <label>Project<select disabled={!!item} value={projectId} onChange={e => { setProject(e.target.value); setSourceId(""); }}>{(readOnly ? overview.projects : allowed).map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
   </fieldset>
   <div className="service-mode" role="group" aria-label="Template editor"><button type="button" disabled={loadFailed} aria-pressed={mode === "form"} onClick={() => switchMode("form")}>Form</button><button type="button" disabled={loadFailed} aria-pressed={mode === "yaml"} onClick={() => switchMode("yaml")}>YAML</button></div>
   <fieldset disabled={(readOnly && mode === "form") || busy || loadFailed} className="service-template-definition">
    {mode === "yaml" ? <label>Template YAML<textarea readOnly={readOnly} className="service-template-code" rows={24} spellCheck={false} value={yaml} onChange={e => setYAML(e.target.value)} /></label> : <>
     <div className="service-form-grid"><label>Template name<input required pattern="[a-z0-9]([a-z0-9.\-]{0,61}[a-z0-9])?" maxLength={63} placeholder="postgres-database" value={definition.metadata.name} onChange={e => setDefinition(d => ({ ...d, metadata: { ...d.metadata, name: e.target.value } }))} /></label><label>Service type<select value={definition.spec.serviceType} onChange={e => {
      const serviceType = e.target.value as Definition["spec"]["serviceType"];
      const fields: Record<string, Output> = serviceType === "postgresql" ? { connectionUrl: { sensitive: true } } : { endpoint: {} };
      spec({ serviceType, outputs: fields, provision: serviceType === "generic" ? { run: "", outputs: Object.keys(fields) } : { ...definition.spec.provision, outputs: Object.keys(fields) } });
     }}><option value="postgresql">PostgreSQL</option><option value="generic">Generic</option></select></label></div>
     <label>Description<input placeholder="Create a database and return its connection details" value={definition.spec.description ?? ""} onChange={e => spec({ description: e.target.value })} /></label>
     <label>Provisioner<select aria-label="Provisioner" value={provider} onChange={e => {
      const next = e.target.value;
      spec({ inputs: next === "neon" ? {} : definition.spec.inputs, provision: next === "neon" ? { neon: {providerRef: "", database: "neondb", dataMode: "schema-only"} } : next === "docker" ? { docker: { serverRef: "" } } : next === "helm" ? { helm: { serverRef: "" } } : { run: "" } });
     }}><option value="docker" disabled={definition.spec.serviceType === "generic" && provider !== "docker"}>Docker</option><option value="helm" disabled={definition.spec.serviceType === "generic" && provider !== "helm"}>Helm</option><option value="neon" disabled={definition.spec.serviceType !== "postgresql"}>Neon</option><option value="script">Custom script (advanced)</option></select></label>
     {provider === "neon" && definition.spec.provision.neon && <NeonProvisionSettings value={definition.spec.provision.neon} projectId={projectId} overview={overview} onChange={neon => spec({provision: {...definition.spec.provision, neon}})} />}
     {(provider === "docker" || provider === "helm") && <ServiceProvisionSettings docker={definition.spec.provision.docker} helm={definition.spec.provision.helm} serviceType={definition.spec.serviceType} overview={overview}
      onDocker={docker => spec({ provision: { ...definition.spec.provision, docker } })}
      onHelm={helm => spec({ provision: { ...definition.spec.provision, helm } })} />}
     {provider === "script" && <section className="service-template-section"><h2>Custom script</h2><p className="service-help">Write each output as name=value to "$DISPATCH_OUTPUT_FILE". Use secret references in YAML for provider credentials.</p>
      <label>Script<textarea required className="service-template-code" rows={8} spellCheck={false} value={definition.spec.provision.run ?? ""} onChange={e => spec({ provision: { ...definition.spec.provision, run: e.target.value } })} placeholder={'# Create the resource, then return its connection\nprintf \'connectionUrl=%s\\n\' "$DATABASE_URL" > "$DISPATCH_OUTPUT_FILE"'} /></label>
      <label className="service-template-check"><input type="checkbox" checked={definition.spec.provision.builder === "docker"} onChange={e => spec({ provision: { ...definition.spec.provision, builder: e.target.checked ? "docker" : undefined } })} />Use a Docker builder</label>
     </section>}
     <details className="service-advanced" open={provider === "script" ? true : undefined}><summary>Inputs and connection fields</summary>
     <section className="service-template-section"><h2>Inputs</h2><p className="service-help">{provider === "script" ? "Scripts read these values as DISPATCH_INPUT_NAME environment variables." : "Optional values people fill in when creating a service. Add database or username to override the PostgreSQL defaults."}</p>
      {Object.entries(definition.spec.inputs ?? {}).map(([name, field], i) => <div className="service-template-input" key={i}>
       <label>Name<input required pattern="[a-z][a-z0-9\-]{0,62}" aria-label={`Input ${i + 1} name`} value={name} onChange={e => updateInput(i, e.target.value, field)} /></label>
       <label>Label<input aria-label={`Input ${i + 1} label`} value={field.label ?? ""} onChange={e => updateInput(i, name, { ...field, label: e.target.value })} /></label>
       <label>Type<select aria-label={`Input ${i + 1} type`} value={field.type ?? "string"} onChange={e => {
        const type = e.target.value as Input["type"];
        updateInput(i, name, { ...field, type, serviceType: type === "service" ? "postgresql" : undefined });
       }}><option value="string">Text</option><option value="secret">Secret</option><option value="service">PostgreSQL service</option></select></label>
       <label className="service-template-check"><input type="checkbox" checked={field.required ?? false} onChange={e => updateInput(i, name, { ...field, required: e.target.checked })} />Required</label>
       <button type="button" className="quiet-button" aria-label={`Remove input ${i + 1}`} onClick={() => inputs(Object.entries(definition.spec.inputs ?? {}).filter((_, index) => index !== i))}>Remove</button>
      </div>)}
      <button type="button" className="quiet-button" disabled={provider === "neon"} onClick={addInput}>Add input</button>
     </section>
     <section className="service-template-section"><h2>Outputs</h2><p className="service-help">Dispatch saves these fields on the new service and hides sensitive values.</p>
      {definition.spec.serviceType === "postgresql" && <label>Connection format<select value={"connectionUrl" in definition.spec.outputs ? "url" : "fields"} onChange={e => outputs(Object.entries(e.target.value === "url" ? { connectionUrl: { sensitive: true } } : pgOutputs))}><option value="url">PostgreSQL connection URL</option><option value="fields">Individual connection fields</option></select></label>}
      {Object.entries(definition.spec.outputs).map(([name, field], i) => <div className="service-template-output" key={i}>
       {definition.spec.serviceType === "generic" ? <label>Field name<input required aria-label={`Output ${i + 1} name`} value={name} onChange={e => {
        const rows = Object.entries(definition.spec.outputs);
        if (rows.some(([key], index) => key === e.target.value && index !== i)) { setError("Each output needs a unique name."); return; }
        rows[i] = [e.target.value, field]; outputs(rows);
       }} /></label> : <code>{name}</code>}
       <label className="service-template-check"><input type="checkbox" disabled={name === "password" || name === "connectionUrl"} checked={field.sensitive || name === "password" || name === "connectionUrl"} onChange={e => outputs(Object.entries(definition.spec.outputs).map(([key, value]) => [key, key === name ? { ...value, sensitive: e.target.checked } : value]))} />Sensitive</label>
       {definition.spec.serviceType === "generic" && <button type="button" className="quiet-button" aria-label={`Remove output ${i + 1}`} onClick={() => outputs(Object.entries(definition.spec.outputs).filter((_, index) => index !== i))}>Remove</button>}
      </div>)}
      {definition.spec.serviceType === "generic" && <button type="button" className="quiet-button" onClick={() => { let suffix = Object.keys(definition.spec.outputs).length + 1; while (`output${suffix}` in definition.spec.outputs) suffix++; outputs([...Object.entries(definition.spec.outputs), [`output${suffix}`, {}]]); }}>Add output</button>}
     </section>
     </details>
    </>}
   </fieldset>
   <details className="service-advanced"><summary>Repository access{sourceId ? " · Configured" : ""}</summary>
    <label>Repository connection<select disabled={readOnly || busy || loadFailed} value={sourceId} onChange={e => setSourceId(e.target.value)}><option value="">None needed for Docker, Helm or an inline script</option>{(overview.configSources ?? []).filter(s => s.projectId === projectId).map(s => <option key={s.id} value={s.id}>{s.name} · {s.repository}</option>)}</select></label>
    <p className="service-help">Select a connection when the YAML includes repository sources.</p>
   </details>
   {error && <p role="alert" className="error">{error}</p>}
   {!readOnly && <div className="service-actions"><button className="primary-button" disabled={busy || !projectId || loadFailed}>{busy ? "Saving…" : "Save template"}</button><button type="button" className="quiet-button" onClick={onBack}>Cancel</button></div>}
  </form>
 </div>;
}
