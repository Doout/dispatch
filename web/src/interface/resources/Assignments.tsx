import { useEffect, useState } from "react";
import { api, Overview, ServiceTemplate } from "../../api";
import { InfrastructureProvider, infrastructureApi } from "../../InfrastructureProviders";
import { InfrastructureQuotas } from "../../InfrastructureQuotas";
import { canManageProject } from "../../permissions";
import { automationClient, Assignment } from "./client";
import { errorMessage, InventoryStatus, ResourceRows, useInventory } from "./shared";

export function Assignments({ overview }: { overview: Overview }) {
  const owner = overview.identity?.systemRole === "owner";
  const projects = overview.projects.filter(project => canManageProject(overview, project.id, "infrastructure.inspect"));
  const [project, setProject] = useState(projects[0]?.id || "");
  const chosen = projects.some(item => item.id === project) ? project : projects[0]?.id || "";
  if (!chosen) return <p className="section-empty">You need infrastructure inspection access to view project assignments.</p>;
  return <><p>Assignments let a project use specific providers, targets, SSH public keys and service templates.</p><label className="resources-project">Project<select value={chosen} onChange={event => setProject(event.target.value)}>{projects.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><ProjectAssignments key={chosen} overview={overview} project={chosen} owner={owner} /><details className="resources-policy"><summary>Project resource limits</summary><InfrastructureQuotas key={chosen} overview={{ ...overview, projects: projects.filter(item => item.id === chosen) }} canManage={owner} /></details></>;
}

function ProjectAssignments({ overview, project, owner }: { overview: Overview; project: string; owner: boolean }) {
  const assignments = useInventory(() => automationClient.assignments(project), project);
  const [providers, setProviders] = useState<InfrastructureProvider[]>([]);
  const [templates, setTemplates] = useState<ServiceTemplate[]>([]);
  const [kind, setKind] = useState("target");
  const [resource, setResource] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [removing, setRemoving] = useState<Assignment | null>(null);
  useEffect(() => {
    if (!owner) return;
    let current = true;
    Promise.all([infrastructureApi.list(), api.serviceTemplates()]).then(([nextProviders, nextTemplates]) => { if (current) { setProviders(nextProviders || []); setTemplates(nextTemplates || []); } }).catch(cause => { if (current) setError(errorMessage(cause)); });
    return () => { current = false; };
  }, [owner, project]);
  const choices = kind === "provider" ? providers.map(item => ({ id: item.id, name: item.name }))
    : kind === "ssh_key" ? overview.secrets.filter(item => item.type === "ssh_private_key" && item.publicValue).map(item => ({ id: item.id, name: item.name }))
    : kind === "service_template" ? templates.filter(item => item.projectId === project && item.serviceType === "postgresql" && ["docker", "helm"].includes(item.provider || "")).map(item => ({ id: `${item.id}@${item.configSha}`, name: `${item.name} · ${item.configSha.slice(0, 8)}` }))
    : overview.servers.filter(item => (!(item as typeof item & { projectId?: string }).projectId || (item as typeof item & { projectId?: string }).projectId === project) && item.runtime !== "relay" && item.runtime !== "builder").map(item => ({ id: item.id, name: item.name }));
  const resourceName = (item: Assignment) => item.kind === "provider" ? providers.find(value => value.id === item.resourceId)?.name || item.resourceId : item.kind === "target" ? overview.servers.find(value => value.id === item.resourceId)?.name || item.resourceId : item.kind === "ssh_key" ? overview.secrets.find(value => value.id === item.resourceId)?.name || item.resourceId : templates.find(value => `${value.id}@${value.configSha}` === item.resourceId)?.name || item.resourceId;
  async function act(action: () => Promise<unknown>, message: string) { setBusy(true); setError(""); setNotice(""); try { await action(); assignments.refresh(); setNotice(message); setRemoving(null); } catch (cause) { setError(errorMessage(cause)); } finally { setBusy(false); } }
  return <>
    <InventoryStatus {...assignments} retry={assignments.refresh} />
    {!assignments.loading && !assignments.error && <ResourceRows items={assignments.items} label="Project resource assignments" columns={["Resource", "Kind", ...(owner ? ["Actions"] : [])]} rowKey={item => `${item.kind}/${item.resourceId}`} empty="No resources assigned to this project." row={item => <><td><strong>{resourceName(item)}</strong><small>{item.resourceId}</small></td><td>{item.kind.replaceAll("_", " ")}</td>{owner && <td><button className="table-action delete-action" type="button" disabled={busy} onClick={() => setRemoving(item)}>Remove assignment</button></td>}</>} />}
    {error && <p className="form-error" role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}
    {removing && <div className="resources-confirm"><p>Remove {resourceName(removing)} from this project's assignments? Existing resources remain, but new requests may lose access.</p><div className="resources-actions"><button type="button" className="quiet-button" disabled={busy} onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="danger-button" disabled={busy} onClick={() => void act(() => automationClient.unassign(removing), "Assignment removed.")}>Remove assignment</button></div></div>}
    {owner && <form className="resources-form" aria-label="Assign project resource" onSubmit={event => { event.preventDefault(); void act(() => automationClient.assign(project, kind, resource), "Resource assigned."); }}><h2>Assign resource</h2><div className="resources-fields"><label>Resource kind<select value={kind} disabled={busy} onChange={event => { setKind(event.target.value); setResource(""); }}><option value="target">Deployment target</option><option value="provider">Provider</option><option value="ssh_key">SSH public key</option><option value="service_template">Service template</option></select></label><label>Resource<select value={resource} required disabled={busy} onChange={event => setResource(event.target.value)}><option value="">Choose resource</option>{choices.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label></div>{!choices.length && <p>No eligible resources of this kind. Register a resource before assigning it.</p>}{kind === "service_template" && <p>The assignment approves this exact template version. Editing the template requires a new assignment.</p>}<button className="primary-button" disabled={busy || !resource}>Assign resource</button></form>}
  </>;
}
