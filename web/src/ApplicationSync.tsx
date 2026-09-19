import { useEffect, useState } from "react";
import { ArrowClockwise, CaretDown, CheckCircle, CircleNotch, Info, MinusCircle, Question, WarningCircle } from "@phosphor-icons/react";
import { api, App, ApplicationSyncStatus, Overview } from "./api";
import { canManageProject } from "./permissions";
import { relative } from "./presentation";

const labels: Record<string, string> = { not_checked: "Not checked", not_supported: "Not supported", synced: "Synced", out_of_sync: "Out of sync", unknown: "Unknown", current: "Current", ready: "Synced", degraded: "Degraded", invalid: "Configuration error", paused: "Paused", deploying: "Deploying", not_deployed: "Not deployed", not_applicable: "Not applicable", redeployment_required: "Redeploy needed", newer_revision_available: "Update available", healthy: "Healthy", progressing: "Progressing", missing: "Missing" };
const label = (value: string) => labels[value] ?? value;
const date = (value?: string) => value ? new Date(value).toLocaleString() : "Never";
const value = (v: unknown) => v === null || v === undefined ? "Not present" : typeof v === "string" ? v : JSON.stringify(v);

export function SyncState({ state }: { state: string }) {
 const positive = ["synced", "current", "ready", "healthy"].includes(state);
 const warning = ["out_of_sync", "degraded", "invalid", "redeployment_required", "newer_revision_available", "missing"].includes(state);
 const active = ["deploying", "progressing", "syncing"].includes(state);
 const Icon = positive ? CheckCircle : warning ? WarningCircle : active ? CircleNotch : state === "not_applicable" || state === "paused" ? MinusCircle : Question;
 return <span className={`sync-state ${positive ? "positive" : warning ? "warning" : active ? "active" : "neutral"}`}><Icon size={16} weight={positive || warning ? "fill" : "regular"} aria-hidden="true" />{label(state)}</span>;
}

export function RuntimeSyncDisclosure({ application, overview }: { application: App; overview: Overview }) {
 return <ApplicationSync application={application} overview={overview} compact />;
}
export function ApplicationSync({ application, overview, onBack, compact = false }: { application: App; overview: Overview; onBack?: () => void; compact?: boolean }) {
 const [status, setStatus] = useState<ApplicationSyncStatus>();
 const [error, setError] = useState("");
 const [busy, setBusy] = useState(false);
 const [confirm, setConfirm] = useState(false);
 const [expanded, setExpanded] = useState(!compact);
 const canCheck = canManageProject(overview, application.projectId, "project.configure");
 const canApply = canManageProject(overview, application.projectId, "deployment.run");
 useEffect(() => { let alive = true; setStatus(undefined); setError(""); void api.applicationSync(application.id).then(next => { if (alive) setStatus(next); }).catch(e => { if (alive) setError(String(e.message ?? e)); }); return () => { alive = false; }; }, [application.id]);
 async function run(reapply: boolean) {
  if (!status || busy) return;
  setBusy(true); setError(""); setConfirm(false);
  try { setStatus(await (reapply ? api.reapplyApplication(application.id, status.deploymentId!) : api.checkApplicationDrift(application.id))); }
  catch (e) { setError(e instanceof Error ? e.message : String(e)); try { setStatus(await api.applicationSync(application.id)); } catch { /* Keep the timestamp of the last observation. */ } }
  finally { setBusy(false); }
 }
 return <section className={`application-sync${compact ? " compact" : ""}`} aria-label={`${application.name} sync and drift`}>
  <header className="sync-toolbar"><div><h2>Application status</h2>{!compact && <span className="sync-app-name" title={application.name}>{application.name}</span>}<span className="sync-observation" title={`Last check: ${date(status?.drift.checkedAt)}. Last successful check: ${date(status?.drift.lastSuccessfulCheckAt)}. Checks are manual observations from the Dispatch controller.`}>{status?.drift.checkedAt ? `Checked ${relative(status.drift.checkedAt)}` : "Not checked"}</span></div><div>
   {canCheck && status?.supported && <button className="quiet-button sync-check" disabled={busy || status.revision.state === "deploying"} onClick={() => void run(false)}><ArrowClockwise size={14} aria-hidden="true" />{busy ? "Checking…" : "Check now"}</button>}
   <button className="sync-details-toggle" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>Details<CaretDown size={13} aria-hidden="true" /></button>
   {onBack && <button className="quiet-button" onClick={onBack}>Back to applications</button>}
  </div></header>
  {error && <p role="alert" className="error-message">{error}</p>}
  {!status && !error && <p className="sync-loading" role="status">Loading saved status…</p>}
  {status && <>
   <dl className="sync-indicators">
    <div><dt>Configuration sync</dt><dd><SyncState state={status.configuration.state} /></dd></div>
    <div><dt>Deployment revision</dt><dd><SyncState state={status.revision.state} /></dd></div>
    <div><dt>Runtime drift</dt><dd><SyncState state={!status.supported ? "not_supported" : !status.drift.checkedAt ? "not_checked" : status.drift.state} /></dd></div>
    <div><dt>Resource health</dt><dd><span title={status.drift.healthMessage}><SyncState state={!status.supported ? "not_supported" : !status.drift.checkedAt ? "not_checked" : status.drift.health} /></span></dd></div>
   </dl>
   {(status.drift.state === "unknown" || status.drift.state === "out_of_sync") && <div className="sync-notice"><Info size={14} aria-hidden="true" /><span>{status.drift.message}</span></div>}
   {status.drift.health === "unknown" && status.drift.checkedAt && status.drift.healthMessage && <div className="sync-notice"><Info size={14} aria-hidden="true" /><span>{status.drift.healthMessage}</span></div>}
   {expanded && <div className="sync-detail-body">
    <dl className="sync-metadata"><div><dt>Configuration</dt><dd>{status.configuration.message}{status.configuration.sourceId && <> Last sync: {date(status.configuration.lastSyncedAt)}.</>}</dd></div>
     <div><dt>Applied revision</dt><dd><code>{status.revision.applied || "Not deployed"}</code></dd></div>
     {status.revision.observed && <div><dt>Observed revision</dt><dd><code>{status.revision.observed}</code></dd></div>}
     <div><dt>Observation</dt><dd>Last check: {date(status.drift.checkedAt)} · Last successful check: {date(status.drift.lastSuccessfulCheckAt)} · {status.drift.location}</dd></div>
    </dl>
    {status.drift.healthMessage && <p className="sync-observation">{status.drift.healthMessage}</p>}
    <p className="sync-observation">Manual observation against the last successful deployment. Health reports readiness separately. The runtime may have changed since this check.</p>
    {canApply && status.supported && <button className="quiet-button" disabled={busy || !status.reapplyAvailable || status.drift.state !== "out_of_sync"} onClick={() => setConfirm(true)}>Reapply deployed configuration</button>}
    {confirm && <section className="sync-confirm" role="group" aria-label="Confirm reapply"><h3>Reapply the last successful deployment?</h3><p>This restores its saved resource fields and recreates missing resources. It can restart workloads. New Git changes and updated service credentials require a separate deployment.</p><div className="service-actions"><button className="primary-button" disabled={busy} onClick={() => void run(true)}>Confirm reapply</button><button className="quiet-button" onClick={() => setConfirm(false)}>Cancel</button></div></section>}
    <div className="sync-resources">{status.drift.resources.map(resource => <details key={`${resource.apiVersion}/${resource.kind}/${resource.namespace}/${resource.name}`} className="sync-resource" open={resource.differences.length > 0}>
     <summary><span><strong>{resource.name}</strong><small>{resource.kind} · {resource.namespace || "Cluster scoped"}</small></span><span title="Runtime drift"><SyncState state={resource.state} /></span></summary>
     <p className="sync-observation">Health: {label(resource.health)}{resource.message && <> · {resource.message}</>}</p>
     {resource.truncated && <p>Showing the first 200 differences.</p>}
     {!!resource.differences.length && <div className="sync-table-scroll"><table><thead><tr><th>Field</th><th>Deployed configuration</th><th>Live value</th></tr></thead><tbody>{resource.differences.map(diff => <tr key={diff.path}><td data-label="Field"><code>{diff.path}</code></td><td data-label="Deployed configuration">{value(diff.expected)}</td><td data-label="Live value">{value(diff.actual)}</td></tr>)}</tbody></table></div>}
    </details>)}</div>
    {!!status.actions.length && <details className="sync-action-history"><summary>Reapply history <span>{status.actions.length}</span></summary><ul>{status.actions.map(action => <li key={action.id}>{date(action.createdAt)} · {action.state} · {action.message}</li>)}</ul></details>}
   </div>}
  </>}
 </section>;
}
