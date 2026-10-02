import { useEffect, useState } from "react";
import { api, ServiceResource, ServiceResourceInspection } from "./api";

export function ServiceResourcePanel({ runId, canManage, canRecover, onChanged }: { runId: string; canManage: boolean; canRecover: boolean; onChanged: () => Promise<void> }) {
 const [currentRunId,setCurrentRunId] = useState(runId);
 const [resource, setResource] = useState<ServiceResource | null>(null);
 const [inspection, setInspection] = useState<ServiceResourceInspection | null>(null);
 const [error, setError] = useState("");
 const [busy, setBusy] = useState(false);
 async function refresh() {
  try { const next = await api.serviceResource(currentRunId); setResource(next); setError(""); return next; }
  catch (e) { setError(e instanceof Error ? e.message : String(e)); return null; }
 }
 useEffect(() => { setCurrentRunId(runId); },[runId]);
 useEffect(() => { void refresh(); }, [currentRunId]);
 useEffect(() => {
  if (!resource || !["accepted", "provisioning", "recovering", "deleting"].includes(resource.state)) return;
  const timer = window.setInterval(() => { void refresh().then(next => { if (next && ["ready", "deleted", "unresolved"].includes(next.state)) void onChanged(); }); }, 2000);
  return () => window.clearInterval(timer);
 }, [resource?.state, currentRunId, onChanged]);
 async function act(action: "inspect" | "reconcile" | "retry" | "delete" | "policy-retain" | "policy-suspend" | "policy-delete" | "reset") {
  setBusy(true); setError("");
  try {
   if (action === "inspect") setInspection(await api.inspectServiceResource(currentRunId));
   else if (action === "delete") setResource(await api.deleteServiceResource(currentRunId));
   else if (action === "reconcile" || action === "retry") setResource(await api.recoverServiceResource(currentRunId, action));
   else { const next=await api.neonLifecycle(currentRunId,action);setResource(next);setCurrentRunId(next.runId);setInspection(null); }
   await onChanged();
  } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  finally { setBusy(false); }
 }
 const active = !!resource && (resource.recoveryAfter ? Date.parse(resource.recoveryAfter) > Date.now() : ["provisioning", "recovering", "deleting"].includes(resource.state));
 return <section className="service-card-section" aria-label="Owned service resource">
  <h3>Owned resource</h3>
  {resource && <><p role="status">{resource.state === "unresolved" ? "Recovery required" : resource.state === "ready" ? "Ready" : resource.state === "deleted" ? "Deleted" : "Operation in progress"}. {resource.message}</p>
   <p className="service-help">{resource.target.provider === "neon" ? `Preview cleanup policy: ${resource.policy}. Retain keeps data. Suspend keeps data and can wake on a new connection. Delete permanently removes this branch after owned workloads stop.` : "Workload deletion retains storage and encrypted recovery history. Removing the connection registration is a separate action."}</p>
   {resource.providerPhase && <p>{resource.providerPhase}</p>}
   {resource.previewId && <p className="service-help">Preview database: {resource.previewAlias} · {resource.previewId}</p>}
   {resource.replacesRunId && <p className="service-help">Schema-only replacement. The previous branch remains retained for separate review.</p>}
   {inspection && <p>Last inspection: {inspection.state === "absent" ? "Resource is absent" : inspection.state === "ready" ? "Owned resource is ready" : "Owned resource is not ready"}.</p>}
   {canManage && resource.state !== "deleted" && <div className="service-actions">
    <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("inspect")}>Inspect resource</button>
    {canRecover && <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("reconcile")}>Reconcile resource</button>}
    {canRecover && inspection?.state === "absent" && resource.operationId === resource.runId && <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("retry")}>Retry original provision</button>}
    <button type="button" className="danger-button" disabled={busy || active} onClick={() => void act("delete")}>Delete resource</button>
   </div>}
   {canManage && canRecover && resource.target.provider === "neon" && resource.previewId && !resource.replacedByRunId && resource.state === "ready" && <div className="service-actions">
    <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("policy-retain")}>Review retain policy</button>
    <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("policy-suspend")}>Review suspend policy</button>
    <button type="button" className="danger-button" disabled={busy || active} onClick={() => void act("policy-delete")}>Review delete on cleanup</button>
    <button type="button" className="quiet-button" disabled={busy || active} onClick={() => void act("reset")}>Reset to fresh schema</button>
   </div>}
  </>}
  {error && <p role="alert" className="error">{error}</p>}
 </section>;
}
