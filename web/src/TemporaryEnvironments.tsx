import { FormEvent, useEffect, useRef, useState } from "react";
import { Overview, request } from "./api";
import { StatusLabel } from "./ResourceTable";

type Resource = { kind: string; id: string; ownership: "owned" | "shared" | "retained" };
export type TemporaryEnvironment = { id: string; projectId: string; name: string; state: string; message?: string; revision: number; sourceSha: string; templateId: string; templateDigest: string; appId: string; deploymentId: string; cleanupOperationId: string; expiresAt: string; resources: Resource[] };
type Input = { projectId: string; templateId: string; serverId: string; name: string; sourceSha: string; lifetimeSeconds: number };
export type EnvironmentReview = { id: string; environmentId: string; input: Input; templateDigest: string; digest: string; omissions: string[]; expiresAt: string };
type Options = { templates: { id: string; name: string }[]; targets: { id: string; name: string }[]; omissions: string[] };
type Cleanup = { environmentId: string; revision: number; name: string; digest: string; summary: string; resources: Resource[] };
const base = "/api/v1/temporary-environments";
const post = <T,>(path: string, value: unknown, key?: string) => request<T>(path, { method: "POST", headers: key ? { "Idempotency-Key": key } : undefined, body: JSON.stringify(value) });
export const temporaryEnvironmentApi = {
  list: (project: string) => request<TemporaryEnvironment[]>(`/api/v1/projects/${encodeURIComponent(project)}/temporary-environments`),
  options: (project: string) => request<Options>(`/api/v1/projects/${encodeURIComponent(project)}/temporary-environments/options`),
  review: (input: Input) => post<EnvironmentReview>(`${base}/review`, input),
  create: (review: EnvironmentReview, confirmation: string, key: string) => post(base, { reviewId: review.id, digest: review.digest, confirmName: confirmation }, key),
  cleanup: (id: string) => post<Cleanup>(`${base}/${encodeURIComponent(id)}/cleanup-review`, {}),
  destroy: (review: Cleanup, confirmation: string, key: string) => post(`${base}/${encodeURIComponent(review.environmentId)}/destroy`, { revision: review.revision, digest: review.digest, confirmName: confirmation }, key),
  extend: (environment: TemporaryEnvironment, expiresAt: string) => post(`${base}/${encodeURIComponent(environment.id)}/extend`, { revision: environment.revision, expiresAt }),
};
const empty = (projectId: string): Input => ({ projectId, templateId: "", serverId: "", name: "", sourceSha: "", lifetimeSeconds: 3600 });
export function TemporaryEnvironments({ overview, canManage }: { overview: Overview; canManage: boolean }) {
  const projects = overview.projects.filter(project => canManage || (overview.projectPermissions?.[project.id] || []).includes("project.view"));
  const [project, setProject] = useState(projects[0]?.id || "");
  const projectRef = useRef(project); projectRef.current = project;
  const [items, setItems] = useState<TemporaryEnvironment[]>([]);
  const [options, setOptions] = useState<Options>({ templates: [], targets: [], omissions: [] });
  const [input, setInput] = useState<Input>(empty(project));
  const [open, setOpen] = useState(false);
  const [review, setReview] = useState<EnvironmentReview | null>(null);
  const [cleanup, setCleanup] = useState<Cleanup | null>(null);
  const [extension, setExtension] = useState<TemporaryEnvironment | null>(null);
  const [newExpiration, setNewExpiration] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [requestKey, setRequestKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const permissions = overview.projectPermissions?.[project] || [];
  const canCreate = canManage || permissions.includes("project.configure") && permissions.includes("deployment.run");
  const canDestroy = canCreate && (canManage || permissions.includes("deployment.cancel"));
  const refresh = async () => { const target = project; const value = await temporaryEnvironmentApi.list(target); if (projectRef.current === target) setItems(value); };
  useEffect(() => { if (!projects.some(item => item.id === project)) setProject(projects[0]?.id || ""); }, [project, overview.projects, canManage]);
  useEffect(() => {
    let active = true; setItems([]); setOptions({ templates: [], targets: [], omissions: [] }); setInput(empty(project)); setOpen(false); setReview(null); setCleanup(null); setExtension(null); setError(""); setNotice("");
    if (!project) return;
    const update = () => temporaryEnvironmentApi.list(project).then(value => { if (active) setItems(value); }).catch(cause => { if (active) setError(cause instanceof Error ? cause.message : "Could not load environments."); });
    void update(); if (canCreate) void temporaryEnvironmentApi.options(project).then(value => { if (active) setOptions(value); }).catch(cause => { if (active) setError(cause instanceof Error ? cause.message : "Could not load environment choices."); });
    const timer = setInterval(() => void update(), 5000); return () => { active = false; clearInterval(timer); };
  }, [project, canCreate]);
  async function act(fn: () => Promise<void>) { setBusy(true); setError(""); setNotice(""); try { await fn(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Environment request failed."); } finally { setBusy(false); } }
  async function prepare(event: FormEvent) { event.preventDefault(); await act(async () => { setReview(await temporaryEnvironmentApi.review(input)); setConfirmation(""); setRequestKey(crypto.randomUUID()); }); }
  if (!projects.length) return null;
  return <section className="server-section temporary-environments" aria-labelledby="temporary-environments-title">
    <div className="section-title"><div><h2 id="temporary-environments-title">Temporary environments</h2><p>Deploy a fixed source revision for testing or investigation, with a reviewed cleanup deadline.</p></div>{canCreate && <button className="quiet-button" onClick={() => { setOpen(true); setInput(empty(project)); setReview(null); setCleanup(null); }}>Create environment</button>}</div>
    <label>Environment project<select value={project} disabled={busy} onChange={event => setProject(event.target.value)}>{projects.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
    {error && <p className="form-error" role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}
    {open && !review && <form className="connection-form inline-create" aria-label="Create temporary environment" onSubmit={event => void prepare(event)}><div className="connection-grid">
      <label>Application template<select required value={input.templateId} onChange={event => setInput({ ...input, templateId: event.target.value })}><option value="">Choose Dockerfile template</option>{options.templates.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>Assigned runtime target<select required value={input.serverId} onChange={event => setInput({ ...input, serverId: event.target.value })}><option value="">Choose ready outbound Docker target</option>{options.targets.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>Environment name<input required pattern="[a-z][a-z0-9-]{1,39}" value={input.name} onChange={event => setInput({ ...input, name: event.target.value })} /></label>
      <label>Full source commit SHA<input required pattern="([a-f0-9]{40}|[a-f0-9]{64})" value={input.sourceSha} onChange={event => setInput({ ...input, sourceSha: event.target.value })} /></label>
      <label>Lifetime in minutes<input type="number" required min={1} step={1} value={input.lifetimeSeconds / 60} onChange={event => setInput({ ...input, lifetimeSeconds: Number(event.target.value) * 60 })} /></label>
    </div><p>Local Docker, Compose and Helm targets are not supported. Production domains, hooks and service bindings are not copied.</p>{!options.targets.length && <p>No assigned target currently reports deployment, inspection and cleanup readiness.</p>}<div className="connection-actions"><button type="button" onClick={() => setOpen(false)}>Cancel</button><button className="primary-button" disabled={busy || !options.targets.length}>Review environment</button></div></form>}
    {review && <form className="connection-form inline-create" aria-label="Confirm temporary environment" onSubmit={event => { event.preventDefault(); void act(async () => { await temporaryEnvironmentApi.create(review, confirmation, requestKey); setReview(null); setOpen(false); setNotice("Environment accepted. Its deployment and cleanup deadline are saved."); await refresh(); }); }}><h3>Create {review.input.name}</h3><p>{review.input.lifetimeSeconds / 60} minutes from acceptance. Review expires {new Date(review.expiresAt).toLocaleString()}.</p><dl><dt>Source revision</dt><dd><code>{review.input.sourceSha}</code></dd><dt>Template digest</dt><dd><code>{review.templateDigest}</code></dd></dl><ul>{review.omissions.map(item => <li key={item}>{item}</li>)}</ul><label>Type {review.input.name} to confirm<input value={confirmation} onChange={event => setConfirmation(event.target.value)} /></label><div className="connection-actions"><button type="button" onClick={() => setReview(null)}>Back</button><button className="primary-button" disabled={busy || confirmation !== review.input.name}>Create reviewed environment</button></div></form>}
    {!items.length && <p className="section-empty">No temporary environments in this project.</p>}
    {items.map(item => <article className="connection-row" key={item.id}><div className="connection-identity"><strong>{item.name}</strong><small>{item.message}</small><small>Expires {new Date(item.expiresAt).toLocaleString()}</small></div><div className="connection-metadata"><StatusLabel state={item.state} /></div><div className="connection-row-actions">{canCreate && !["closing", "cleanup_blocked", "closed"].includes(item.state) && <button className="table-action" disabled={busy} onClick={() => { setExtension(item); setNewExpiration(""); }}>Extend deadline</button>}{canDestroy && item.state !== "closed" && <button className="table-action" disabled={busy} onClick={() => void act(async () => { setCleanup(await temporaryEnvironmentApi.cleanup(item.id)); setReview(null); setConfirmation(""); setRequestKey(crypto.randomUUID()); })}>Review cleanup</button>}</div><details><summary>Owned resources and operations</summary><p>Deployment: <code>{item.deploymentId}</code></p><p>Cleanup: <code>{item.cleanupOperationId}</code></p><p>Source: <code>{item.sourceSha}</code></p><ul>{item.resources.map(resource => <li key={`${resource.kind}-${resource.id}`}>{resource.kind} · {resource.ownership} · <code>{resource.id}</code></li>)}</ul></details></article>)}
    {extension && <form className="connection-form" aria-label="Extend temporary environment" onSubmit={event => { event.preventDefault(); void act(async () => { await temporaryEnvironmentApi.extend(extension, new Date(newExpiration).toISOString()); setExtension(null); setNotice("Deadline extended within the project lifetime policy."); await refresh(); }); }}><h3>Extend {extension.name}</h3><p>The total lifetime remains bounded by project policy. Expired or stopping environments cannot be extended.</p><label>New expiration (your local time)<input type="datetime-local" required value={newExpiration} onChange={event => setNewExpiration(event.target.value)} /></label><button type="button" onClick={() => setExtension(null)}>Cancel</button><button disabled={busy || !newExpiration}>Extend reviewed deadline</button></form>}
    {cleanup && <form className="connection-form" aria-label="Confirm environment cleanup" onSubmit={event => { event.preventDefault(); void act(async () => { await temporaryEnvironmentApi.destroy(cleanup, confirmation, requestKey); setCleanup(null); setNotice("Cleanup accepted. Uncertain runtime changes must be inspected before deletion continues."); await refresh(); }); }}><h3>Clean up {cleanup.name}</h3><p>{cleanup.summary}</p><ul>{cleanup.resources.map(resource => <li key={`${resource.kind}-${resource.id}`}>{resource.kind} · {resource.ownership} · <code>{resource.id}</code></li>)}</ul><label>Type {cleanup.name} to confirm<input value={confirmation} onChange={event => setConfirmation(event.target.value)} /></label><div className="connection-actions"><button type="button" onClick={() => setCleanup(null)}>Cancel</button><button disabled={busy || confirmation !== cleanup.name}>Start reviewed cleanup</button></div></form>}
  </section>;
}
