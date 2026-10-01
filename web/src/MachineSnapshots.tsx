import { FormEvent, useEffect, useState } from "react";
import { Overview, request } from "./api";
import { StatusLabel } from "./ResourceTable";

export type MachineSnapshot = { id: string; serverId: string; projectId: string; providerId: string; name: string; state: string; resourceId?: string; revision: number; retainUntil: string; evidence?: { image: string; consistency: string; encryption: { mode: string }; disks: { id: string; role: string; sizeBytes: number; encrypted: boolean; contentDigest: string }[] } };
type Source = { id: string; projectId: string; name: string; allocationState: string };
type Review = { id: string; name: string; action: string; digest: string; expiresAt: string; input: unknown };
const post = <T,>(path: string, input?: unknown) => request<T>(path, { method: "POST", body: JSON.stringify(input ?? {}) });
const root = "/api/v1/infrastructure/snapshots";

export function MachineSnapshots({ overview, servers, onRestore }: { overview: Overview; servers: Source[]; onRestore: (snapshot: MachineSnapshot) => void }) {
  const [project, setProject] = useState(overview.projects[0]?.id || "");
  const [items, setItems] = useState<MachineSnapshot[]>([]);
  const [source, setSource] = useState("");
  const [name, setName] = useState("");
  const [diskSet, setDiskSet] = useState("all");
  const [days, setDays] = useState(7);
  const [capture, setCapture] = useState(false);
  const [review, setReview] = useState<Review | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [requestKey, setRequestKey] = useState("");
  const [resolve, setResolve] = useState<MachineSnapshot | null>(null);
  const [resourceId, setResourceId] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const path = `/api/v1/projects/${encodeURIComponent(project)}/infrastructure/snapshots`;
  const refresh = async () => { if (!project) return; const rows = await request<MachineSnapshot[]>(path); setItems(Array.isArray(rows) ? rows : []); };
  useEffect(() => { let active = true; setItems([]); if (!project) return; const update = () => request<MachineSnapshot[]>(path).then(rows => { if (active) setItems(Array.isArray(rows) ? rows : []); }).catch(() => {}); void update(); const timer = setInterval(() => void update(), 5000); return () => { active = false; clearInterval(timer); }; }, [project, path]);
  async function act(fn: () => Promise<void>) { setBusy(true); setError(""); try { await fn(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Snapshot request failed."); } finally { setBusy(false); } }
  const prepared = (value: Review) => { setReview(value); setConfirmation(""); setRequestKey(crypto.randomUUID()); setCapture(false); };
  async function accept(event: FormEvent) { event.preventDefault(); if (!review) return; await act(async () => { await post(`${root}/accept`, { reviewId: review.id, digest: review.digest, confirmName: confirmation, requestKey }); setReview(null); await refresh(); }); }
  return <section className="server-section machine-snapshots" aria-labelledby="machine-snapshots-title">
    <div className="section-title"><div><h2 id="machine-snapshots-title">Machine snapshots</h2><p>Retain provider disks independently of their source machine. Restore into a fresh isolated clone.</p></div><button className="quiet-button" onClick={() => { setCapture(true); setReview(null); setName(""); }}>Capture snapshot</button></div>
    <label className="snapshot-project">Snapshot project<select value={project} onChange={event => { setProject(event.target.value); setSource(""); setReview(null); setCapture(false); }}>{overview.projects.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
    {error && <p role="alert" className="form-error">{error}</p>}
    {capture && <form className="connection-form inline-create" aria-label="Capture machine snapshot" onSubmit={event => { event.preventDefault(); void act(async () => { prepared(await post<Review>(`/api/v1/infrastructure/servers/${encodeURIComponent(source)}/snapshot-review`, { name, diskSet, consistency: "crash-consistent", encryption: { mode: "provider-managed" }, retainUntil: new Date(Date.now() + days * 86400000).toISOString() })); }); }}>
      <div className="connection-grid"><label>Source machine<select required value={source} onChange={event => setSource(event.target.value)}><option value="">Choose allocated machine</option>{servers.filter(item => item.projectId === project && item.allocationState === "allocated").map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><label>Snapshot name<input required maxLength={80} value={name} onChange={event => setName(event.target.value)} /></label><label>Disks<select value={diskSet} onChange={event => setDiskSet(event.target.value)}><option value="all">All disks</option><option value="boot">Boot disk only</option></select></label><label>Retain for days<input required type="number" min={1} max={365} value={days} onChange={event => setDays(Number(event.target.value))} /></label></div>
      <p>Crash-consistent capture does not quiesce databases. Applications may need recovery after restore. Provider-managed encryption is required.</p><div className="connection-actions"><button className="quiet-button" type="button" onClick={() => setCapture(false)}>Cancel</button><button className="primary-button" disabled={busy}>Review snapshot</button></div>
    </form>}
    {review && <form className="connection-form" aria-label="Confirm snapshot operation" onSubmit={event => void accept(event)}><h3>{review.action === "snapshot.delete" ? "Delete" : "Capture"} {review.name}</h3><p>Review the exact disks and retention below. This review expires at {new Date(review.expiresAt).toLocaleTimeString()}.</p><details open><summary>Reviewed snapshot</summary><pre>{JSON.stringify(review.input, null, 2)}</pre></details><label>Type {review.name} to confirm<input value={confirmation} onChange={event => setConfirmation(event.target.value)} /></label><div className="connection-actions"><button className="quiet-button" type="button" onClick={() => setReview(null)}>Cancel</button><button className="primary-button" disabled={busy || confirmation !== review.name}>Accept reviewed snapshot operation</button></div></form>}
    {!items.length && <p className="section-empty">No retained machine snapshots in this project.</p>}
    {items.map(item => <article className="connection-row" key={item.id}><div className="connection-identity"><strong>{item.name}</strong><small>{item.resourceId || "Provider snapshot pending"}</small></div><div className="connection-metadata"><StatusLabel state={item.state} /><span>Retained until {new Date(item.retainUntil).toLocaleString()}</span>{item.evidence && <span>{item.evidence.disks.length} disks · {item.evidence.consistency}</span>}</div><div className="connection-row-actions">{item.state === "ready" && <button className="table-action" disabled={busy} onClick={() => onRestore(item)}>Restore isolated clone</button>}{["ready", "corrupt", "failed"].includes(item.state) && <button className="table-action" disabled={busy || new Date(item.retainUntil).getTime() > Date.now()} onClick={() => void act(async () => prepared(await post<Review>(`${root}/${item.id}/delete-review`)))}>Review snapshot deletion</button>}{item.state === "unknown" && <button className="table-action" disabled={busy} onClick={() => { setResolve(item); setResourceId(item.resourceId || ""); setConfirmation(""); }}>Inspect uncertain snapshot</button>}</div>{item.evidence && <details><summary>Captured disk evidence</summary><p>Disk content and encryption are reported by the provider. Application integrity has not been verified.</p><pre>{JSON.stringify(item.evidence, null, 2)}</pre></details>}</article>)}
    {resolve && <form className="connection-form" onSubmit={event => { event.preventDefault(); void act(async () => { await post(`${root}/${resolve.id}/resolve`, { resourceId, revision: resolve.revision, confirmName: confirmation }); setResolve(null); await refresh(); }); }}><h3>Inspect {resolve.name}</h3><p>Inspection checks the original ownership and terminal provider evidence. It does not submit another capture.</p><label>Provider snapshot ID<input required value={resourceId} onChange={event => setResourceId(event.target.value)} /></label><label>Type {resolve.name} to confirm<input value={confirmation} onChange={event => setConfirmation(event.target.value)} /></label><button className="quiet-button" type="button" onClick={() => setResolve(null)}>Cancel</button><button className="primary-button" disabled={busy || confirmation !== resolve.name}>Verify snapshot disposition</button></form>}
  </section>;
}
