import { useEffect, useState } from "react";
import { api, ServiceResource, ServiceResourceInspection } from "./api";

export function ServiceResourcePanel({ runId, canManage, canRecover, onChanged }: { runId: string; canManage: boolean; canRecover: boolean; onChanged: () => Promise<void> }) {
 const [resource, setResource] = useState<ServiceResource | null>(null);
 const [inspection, setInspection] = useState<ServiceResourceInspection | null>(null);
 const [error, setError] = useState("");
 const [busy, setBusy] = useState(false);
 async function refresh() {
  try { const next = await api.serviceResource(runId); setResource(next); setError(""); return next; }
  catch (e) { setError(e instanceof Error ? e.message : String(e)); return null; }
 }
 useEffect(() => { void refresh(); }, [runId]);
 useEffect(() => {
  if (!resource || !["accepted", "provisioning", "recovering", "deleting"].includes(resource.state)) return;
  const timer = window.setInterval(() => { void refresh().then(next => { if (next && ["ready", "deleted", "unresolved"].includes(next.state)) void onChanged(); }); }, 2000);
  return () => window.clearInterval(timer);
 }, [resource?.state, runId, onChanged]);
 async function act(action: "inspect" | "reconcile" | "retry" | "delete") {
  setBusy(true); setError("");
  try {
   if (action === "inspect") setInspection(await api.inspectServiceResource(runId));
   else if (action === "delete") setResource(await api.deleteServiceResource(runId));
   else setResource(await api.recoverServiceResource(runId, action));
   await onChanged();
  } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  finally { setBusy(false); }
 }
 const active = !!resource && (resource.recoveryAfter ? Date.parse(resource.recoveryAfter) > Date.now() : ["provisioning", "recovering", "deleting"].includes(resource.state));
 return <section className="service-card-section" aria-label="Owned service resource">
  <h3>Owned resource</h3>
  {resource && <><p role="status">{resource.state === "unresolved" ? "Recovery required" : resource.state === "ready" ? "Ready" : resource.state === "deleted" ? "Deleted" : "Operation in progress"}. {resource.message}</p>
   <p className="service-help">Workload deletion retains storage and encrypted recovery history. Removing the connection registration is a separate action.</p>
   {inspection && <p>Last inspection: {inspection.state === "absent" ? "Resource is absent" : inspection.state === "ready" ? "Owned resource is ready" : "Owned resource is not ready"}.</p>}
   {canManage && resource.state !== "deleted" && <div className="service-actions">
    <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("inspect")}>Inspect resource</button>
    {canRecover && <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("reconcile")}>Reconcile resource</button>}
    {canRecover && inspection?.state === "absent" && resource.operationId === resource.runId && <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("retry")}>Retry original provision</button>}
    <button type="button" className="danger-button" disabled={busy || active} onClick={() => void act("delete")}>Delete resource</button>
   </div>}
  </>}
  {error && <p role="alert" className="error">{error}</p>}
 </section>;
}
