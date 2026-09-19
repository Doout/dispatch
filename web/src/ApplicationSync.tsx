import { useEffect, useState } from "react";
import { api, App, ApplicationSyncStatus, Overview } from "./api";
import { canManageProject } from "./permissions";

const labels: Record<string,string> = { synced: "Synced", out_of_sync: "Out of sync", unknown: "Unknown", current: "Current", ready: "Synced", degraded: "Degraded", invalid: "Configuration error", paused: "Paused", deploying: "Deploying", not_deployed: "Not deployed", not_applicable: "Not applicable", redeployment_required: "Redeployment required", newer_revision_available: "Newer revision available", healthy: "Healthy", progressing: "Progressing", missing: "Missing" };
const label = (value: string) => labels[value] ?? value;
const date = (value?: string) => value ? new Date(value).toLocaleString() : "Never";
const value = (v: unknown) => v === null || v === undefined ? "Not present" : typeof v === "string" ? v : JSON.stringify(v);

export function RuntimeSyncDisclosure({ application, overview }: { application: App; overview: Overview }) {
 const [open,setOpen] = useState(false);
 return <section className="runtime-sync-disclosure"><button className="quiet-button" aria-expanded={open} onClick={() => setOpen(!open)}>Sync and drift</button>{open && <ApplicationSync key={application.id} application={application} overview={overview} />}</section>;
}
export function ApplicationSync({ application, overview, onBack }: { application: App; overview: Overview; onBack?: () => void }) {
 const [status,setStatus] = useState<ApplicationSyncStatus>();
 const [error,setError] = useState("");
 const [busy,setBusy] = useState(false);
 const [confirm,setConfirm] = useState(false);
 const canCheck = canManageProject(overview,application.projectId,"project.configure");
 const canApply = canManageProject(overview,application.projectId,"deployment.run");
 useEffect(() => { let alive=true;setStatus(undefined);setError("");void api.applicationSync(application.id).then(next => {if(alive)setStatus(next);}).catch(e => {if(alive)setError(String(e.message ?? e));});return () => {alive=false;}; },[application.id]);
 async function run(reapply: boolean) {
  if (!status || busy) return;
  setBusy(true);setError("");setConfirm(false);
  try {setStatus(await (reapply ? api.reapplyApplication(application.id,status.deploymentId!) : api.checkApplicationDrift(application.id)));}
  catch(e) {setError(e instanceof Error ? e.message : String(e));try{setStatus(await api.applicationSync(application.id));}catch{ /* Keep the last observation, with its timestamp. */ }}
  finally{setBusy(false);}
 }
 return <section className="application-sync" aria-label={`${application.name} sync and drift`}>
  <header><div><h2>Sync and drift</h2><p>{application.name} · Current application state</p></div>{onBack && <button className="quiet-button" onClick={onBack}>Back to applications</button>}</header>
  {error && <p role="alert" className="error-message">{error}</p>}
  {!status && !error && <p role="status">Loading saved status…</p>}
  {status && <>
   <dl className="sync-indicators">
    <div><dt>Configuration sync</dt><dd>{label(status.configuration.state)}</dd><small>{status.configuration.message}</small>{status.configuration.sourceId && <small>Last sync: {date(status.configuration.lastSyncedAt)}</small>}</div>
    <div><dt>Deployment revision</dt><dd>{label(status.revision.state)}</dd>{status.revision.applied && <small>Applied: <code>{status.revision.applied}</code></small>}{status.revision.observed && <small>Observed: <code>{status.revision.observed}</code></small>}</div>
    <div><dt>Runtime drift</dt><dd>{label(status.drift.state)}</dd><small>Compared with the last successful deployment.</small></div>
    <div><dt>Resource health</dt><dd>{label(status.drift.health)}</dd><small>Observed readiness, separate from configuration differences.</small></div>
   </dl>
   <p>{status.drift.message}</p>
   <p className="sync-observation">Last check: {date(status.drift.checkedAt)} · Last successful check: {date(status.drift.lastSuccessfulCheckAt)} · {status.drift.location}</p>
   <p className="sync-observation">Checks are manual observations. The runtime may have changed since the last check.</p>
   <div className="service-actions">
    {canCheck && status.supported && <button className="primary-button" disabled={busy || status.revision.state === "deploying"} onClick={() => void run(false)}>{busy ? "Working…" : "Check now"}</button>}
    {canApply && status.supported && <button className="quiet-button" disabled={busy || !status.reapplyAvailable || status.drift.state !== "out_of_sync"} onClick={() => setConfirm(true)}>Reapply deployed configuration</button>}
   </div>
   {confirm && <section className="sync-confirm" role="group" aria-label="Confirm reapply"><h3>Reapply the last successful deployment?</h3><p>This restores its saved resource fields and recreates missing resources. It can restart workloads. New Git changes and updated service credentials will require a separate deployment.</p><code>{status.deploymentId}</code><div className="service-actions"><button className="primary-button" onClick={() => void run(true)}>Confirm reapply</button><button className="quiet-button" onClick={() => setConfirm(false)}>Cancel</button></div></section>}
   <div className="sync-resources">{status.drift.resources.map(resource => <section key={`${resource.apiVersion}/${resource.kind}/${resource.namespace}/${resource.name}`} className="sync-resource">
    <h3>{resource.kind} / {resource.name}</h3><p>{resource.namespace || "Cluster scoped"} · {label(resource.state)} · Health: {label(resource.health)}</p>
    {resource.truncated && <p>Showing the first 200 differences. Reapply restores all saved fields.</p>}
    {!!resource.differences.length && <div className="sync-table-scroll"><table><thead><tr><th>Field</th><th>Deployed configuration</th><th>Live value</th></tr></thead><tbody>{resource.differences.map(diff => <tr key={diff.path}><td data-label="Field"><code>{diff.path}</code></td><td data-label="Deployed configuration">{value(diff.expected)}</td><td data-label="Live value">{value(diff.actual)}</td></tr>)}</tbody></table></div>}
   </section>)}</div>
   {!!status.actions.length && <section><h3>Reapply history</h3><ul>{status.actions.map(action => <li key={action.id}>{date(action.createdAt)} · {action.state} · {action.message}</li>)}</ul></section>}
  </>}
 </section>;
}
