import { FormEvent, useEffect, useRef, useState } from "react";
import { api, Overview, WorkloadBackup, WorkloadBackupOperation } from "./api";
import { canManageProject } from "./permissions";
import { isUIFeatureEnabled } from "./featureFlags";
import { DisabledUIFeature } from "./DisabledUIFeature";

export function WorkloadBackups(props: { overview: Overview; project: string; paginated?: boolean }) {
 if (!isUIFeatureEnabled(props.overview, "workloadBackups")) return <DisabledUIFeature overview={props.overview} feature="workloadBackups" />;
 return <EnabledWorkloadBackups {...props} />;
}

function EnabledWorkloadBackups({ overview, project, paginated = false }: { overview: Overview; project: string; paginated?: boolean }) {
 const [page,setPage]=useState(0);
 const [items,setItems]=useState<WorkloadBackup[]>([]);
 const [source,setSource]=useState("");
 const [hours,setHours]=useState("24");
 const [query,setQuery]=useState("");
 const [expected,setExpected]=useState("");
 const [busy,setBusy]=useState(false);
 const [error,setError]=useState("");
 const sources=(overview.services??[]).filter(s=>s.type==="postgresql" && s.provisionRunId && s.provisionTarget?.provider==="docker" && (!project || s.projectId===project) && canManageProject(overview,s.projectId,"project.configure") && canManageProject(overview,s.projectId,"deployment.run"));
 async function refresh(){try{setItems(await api.workloadBackups(project));setError("");}catch(e){setError(e instanceof Error?e.message:String(e));}}
 useEffect(()=>{let current=true;setSource("");setItems([]);setPage(0);setError("");const update=async()=>{try{const next=await api.workloadBackups(project);if(current){setItems(Array.isArray(next)?next:[]);setError("");}}catch(cause){if(current)setError(cause instanceof Error?cause.message:String(cause));}};void update();const timer=window.setInterval(()=>void update(),5000);return()=>{current=false;window.clearInterval(timer);};},[project]);
 async function create(e:FormEvent){e.preventDefault();setBusy(true);setError("");try{await api.createWorkloadBackup({sourceRunId:source,verificationIntervalHours:Number(hours),checks:query.trim()?[{query:query.trim(),expected}]:[]});setQuery("");setExpected("");await refresh();}catch(e){setError(e instanceof Error?e.message:String(e));}finally{setBusy(false);}}
 return <section aria-label="Workload backups">
  <p className="service-help">PostgreSQL 17 or newer database backups from owned Docker services. Encrypted archives remain on their target when a service is removed and prevent target deletion until explicitly deleted.</p>
  {sources.length>0 && <form className="service-card service-card-body" onSubmit={e=>void create(e)}>
   <h2>Create a database backup</h2>
   <label>Source service<select value={source} onChange={e=>setSource(e.target.value)} required><option value="">Choose an owned PostgreSQL service</option>{sources.map(s=><option key={s.id} value={s.provisionRunId}>{s.name}</option>)}</select></label>
   <label>Verification interval (hours)<input type="number" min="0" max="8760" value={hours} onChange={e=>setHours(e.target.value)} required/></label><p className="service-help">Use 0 for manual verification. Each check restores into a separate container with no network access.</p>
   <details><summary>Optional integrity assertion</summary><label>Read-only SELECT query<textarea value={query} onChange={e=>setQuery(e.target.value)} placeholder="SELECT count(*) FROM orders"/></label><label>Expected output<input value={expected} onChange={e=>setExpected(e.target.value)}/></label><p className="service-help">Assertions are stored encrypted and are not returned in backup details.</p></details>
   <button type="submit" className="primary-button" disabled={busy || !source}>{busy?"Accepting backup…":"Create backup"}</button>
  </form>}
  {error && <p role="alert" className="error">{error}</p>}
  {!items.length && <p className="empty-state">No workload backups in this project.</p>}
  <div className="service-list">{(paginated?items.slice(page*20,page*20+20):items).map(item=><BackupCard key={item.id} item={item} overview={overview} onChanged={refresh}/>)}</div>
  {paginated && items.length>20 && <div className="resources-pagination"><span>{page*20+1} to {Math.min(page*20+20,items.length)} of {items.length}</span><button type="button" className="quiet-button" disabled={page===0} onClick={()=>setPage(value=>value-1)}>Previous</button><button type="button" className="quiet-button" disabled={(page+1)*20>=items.length} onClick={()=>setPage(value=>value+1)}>Next</button></div>}
 </section>;
}
function BackupCard({item,overview,onChanged}:{item:WorkloadBackup;overview:Overview;onChanged:()=>Promise<void>}){
 const [open,setOpen]=useState(false);const [operations,setOperations]=useState<WorkloadBackupOperation[]>([]);const [destination,setDestination]=useState("");const [busy,setBusy]=useState(false);const [error,setError]=useState("");
 const manage=canManageProject(overview,item.projectId,"project.configure") && canManageProject(overview,item.projectId,"deployment.run");
 const confirmationScope=useRef<AbortController|null>(null);
 useEffect(()=>{const controller=new AbortController();confirmationScope.current=controller;return()=>controller.abort();},[manage]);
 const destinations=(overview.services??[]).filter(s=>s.type==="postgresql" && s.projectId===item.projectId && s.provisionRunId && s.provisionTarget?.serverId===item.serverId && s.provisionTarget.provider==="docker");
 async function load(){try{setOperations(await api.workloadBackupOperations(item.id));}catch(e){setError(e instanceof Error?e.message:String(e));}}
 useEffect(()=>{if(!open)return;void load();const timer=window.setInterval(()=>void load(),3000);return()=>window.clearInterval(timer);},[open,item.id]);
 const active=operations.some(o=>o.state==="running" || o.state==="unknown");
 async function act(action:"verify"|"restore"|"delete"|"reconcile",id?:string){setBusy(true);setError("");try{if(action==="verify")await api.verifyWorkloadBackup(item.id);else if(action==="restore")await api.restoreWorkloadBackup(item.id,destination,confirmationScope.current?.signal);else if(action==="delete")await api.deleteWorkloadBackup(item.id,confirmationScope.current?.signal);else await api.reconcileWorkloadBackup(item.id,id!);await load();await onChanged();}catch(e){if(!(e instanceof DOMException && e.name==="AbortError"))setError(e instanceof Error?e.message:String(e));}finally{setBusy(false);}}
 return <details className="service-card" open={open} onToggle={e=>setOpen(e.currentTarget.open)}>
  <summary className="service-card-summary"><span><strong>PostgreSQL backup · {new Date(item.createdAt).toLocaleString()}</strong><small>{item.state} · Verification {item.verificationState.replaceAll("_"," ")}</small></span></summary>
  {open && <div className="service-card-body"><dl className="service-fields"><div><dt>Archive</dt><dd><code>{item.id}</code></dd></div><div><dt>Consistency</dt><dd>Database-native PostgreSQL snapshot</dd></div><div><dt>Storage</dt><dd>{item.state==="deleted"?"Archive deleted":`${item.bytes.toLocaleString()} encrypted bytes · Target local · Retained`}</dd></div><div><dt>Integrity</dt><dd>{item.checkCount} configured assertions · {item.checksum?"SHA-256 recorded":"Awaiting archive"}</dd></div><div><dt>Temporary cleanup</dt><dd>{item.cleanupState}</dd></div></dl>
   {item.message && <p role="status">{item.message}</p>}
   {manage && item.state!=="deleted" && <><div className="service-actions"><button type="button" className="quiet-button" disabled={busy || active || item.state!=="ready"} onClick={()=>void act("verify")}>Verify isolated restore</button><button type="button" className="danger-button" disabled={busy || active} onClick={()=>void act("delete")}>Delete backup archive</button></div>
    {item.state==="ready" && <div className="service-card-section"><label>Restore destination<select value={destination} onChange={e=>setDestination(e.target.value)}><option value="">Choose a PostgreSQL service on this target</option>{destinations.map(s=><option key={s.id} value={s.provisionRunId}>{s.name}</option>)}</select></label><p className="service-help">Restoring overwrites objects present in the archive. Detach consumers first. The next step reviews the exact destination and consequences.</p><button type="button" className="danger-button" disabled={busy || active || !destination} onClick={()=>void act("restore")}>Review data restore</button></div>}
   </>}
   <h3>Operation history</h3>{operations.map(op=><div key={op.id} className="service-card-section"><strong>{op.action} · {op.state}</strong><p>{op.message}</p>{manage && ["running","unknown"].includes(op.state) && <button type="button" className="quiet-button" disabled={busy || Date.parse(op.recoveryAfter)>Date.now()} onClick={()=>void act("reconcile",op.id)}>Reconcile interrupted operation</button>}</div>)}
   {error && <p role="alert" className="error">{error}</p>}
  </div>}
 </details>;
}
