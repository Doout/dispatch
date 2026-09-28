import { useState } from "react";
import { api, Overview, ServiceTemplate } from "./api";
import { canManageProject } from "./permissions";

export function ServiceTemplateCatalog({ templates, overview, onEdit, onUse, onChanged, onCreate }: { templates: ServiceTemplate[]; overview: Overview; onEdit: (item: ServiceTemplate) => void; onUse: (item: ServiceTemplate) => void; onChanged: () => Promise<void>; onCreate?: () => void }) {
 const [removing, setRemoving] = useState("");
 const [busy, setBusy] = useState("");
 const [error, setError] = useState<{ id: string; message: string } | null>(null);
 async function remove(item: ServiceTemplate) {
  setBusy(item.id); setError(null);
  try { await api.deleteServiceTemplate(item.id, item.revision!); setRemoving(""); await onChanged(); }
  catch (e) { setError({ id: item.id, message: e instanceof Error ? e.message : String(e) }); }
  finally { setBusy(""); }
 }
 if (!templates.length) return <div className="service-template-empty"><h2>No service templates yet</h2><p>Choose Docker or Helm, select a server, and save a reusable service template.</p>{onCreate && <button className="primary-button" onClick={onCreate}>Create template</button>}<p className="service-help">You can also sync ServiceTemplate YAML from a repository.</p></div>;
 return <div className="service-template-list">{templates.map(item => {
  const manage = canManageProject(overview, item.projectId, "project.configure");
  return <article className="service-template-card" key={item.id}>
   <div className="service-template-card-heading"><div><h2>{item.name}</h2><p>{item.serviceType === "postgresql" ? "PostgreSQL" : "Generic"} · {overview.projects.find(p => p.id === item.projectId)?.name}</p></div><span className="service-template-origin">{item.managedBy === "dispatch" ? "Saved in Dispatch" : "GitOps"}</span></div>
   {item.description && <p>{item.description}</p>}
   <p className="service-help">{item.provider === "docker" ? "Docker · " : item.provider === "helm" ? "Helm · " : ""}{Object.keys(item.inputs ?? {}).length} inputs · {Object.keys(item.outputs ?? {}).length} outputs</p>
   <div className="service-actions">
    {manage && canManageProject(overview, item.projectId, "deployment.run") && <button className="quiet-button" onClick={() => onUse(item)}>Create service</button>}
    <button className="quiet-button" onClick={() => onEdit(item)}>{item.managedBy === "dispatch" && manage ? "Edit template" : "View template"}</button>
    {manage && item.managedBy === "dispatch" && <button className="service-remove-trigger" onClick={() => setRemoving(item.id)}>Delete template</button>}
   </div>
   {removing === item.id && <div className="service-template-delete"><p>Delete {item.name}? Services already created from it will remain.</p><div className="service-actions"><button disabled={!!busy} className="danger-button" onClick={() => void remove(item)}>{busy === item.id ? "Deleting…" : "Confirm delete"}</button><button disabled={!!busy} className="quiet-button" onClick={() => setRemoving("")}>Cancel</button></div></div>}
   {error?.id === item.id && <p role="alert" className="error">{error.message}</p>}
  </article>;
 })}</div>;
}
