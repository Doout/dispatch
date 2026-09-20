import { useEffect, useRef, useState } from "react";
import { ArrowSquareOut, CheckCircle, ClockCounterClockwise, MagnifyingGlass, NotePencil, ShieldCheck, WarningCircle } from "@phosphor-icons/react";
import { api, request, type Deployment, type WorkflowRevision, type WorkflowStageRun } from "../api";
import { relative, short } from "../presentation";
import { routePath, shouldHandleNavigation } from "../routes";
import { releaseClient, type Activity, type Diagnosis, type ReleaseInfo, type ReleasePreview, type RollbackPreview } from "./releaseClient";
import "./release-tools.css";

type Tool = "notes" | "activity" | "diagnosis" | "preview";
const message = (error: unknown) => error instanceof Error ? error.message : String(error);
const display = (value: unknown) => typeof value === "string" ? value : JSON.stringify(value);

export function ReleaseTools({ deployment, canDeploy, canConfigure, onDeployment, onSelectDeployment }: {
  deployment: Deployment; canDeploy: boolean; canConfigure: boolean;
  onDeployment?: (deployment: Deployment) => void | Promise<void>;
  onSelectDeployment?: (id: string) => void | Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [tool, setTool] = useState<Tool>("notes");
  const [info, setInfo] = useState<ReleaseInfo>();
  const [notes, setNotes] = useState("");
  const [links, setLinks] = useState("");
  const [activity, setActivity] = useState<Activity[]>();
  const [diagnosis, setDiagnosis] = useState<Diagnosis>();
  const [preview, setPreview] = useState<ReleasePreview>();
  const [rollback, setRollback] = useState<RollbackPreview>();
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const current = useRef(deployment.id);
  useEffect(() => {
    current.current = deployment.id;
    setInfo(undefined); setActivity(undefined); setDiagnosis(undefined); setPreview(undefined); setRollback(undefined); setAcknowledged(false); setError(""); setNotice(""); setBusy("");
  }, [deployment.id]);
  async function perform(label: string, work: () => Promise<void>) {
    const selected = deployment.id; setBusy(label); setError(""); setNotice("");
    try { await work(); } catch (cause) { if (current.current === selected) { setError(message(cause)); if (cause instanceof Error && (cause as Error & { status?: number }).status === 409) { setPreview(undefined); setRollback(undefined); setAcknowledged(false); } } }
    finally { if (current.current === selected) setBusy(""); }
  }
  useEffect(() => {
    if (!open) return;
    let alive = true;
    setError("");
    if (tool === "notes" && !info) {
      setBusy("Loading notes");
      void releaseClient.info(deployment.id).then(value => { if (alive) { setInfo(value); setNotes(value.note.notes); setLinks(value.note.links.join("\n")); } }).catch(cause => { if (alive) setError(message(cause)); }).finally(() => { if (alive) setBusy(""); });
    }
    if (tool === "activity" && !activity) {
      setBusy("Loading activity");
      void releaseClient.activity(deployment.appId).then(value => { if (alive) setActivity(value.items); }).catch(cause => { if (alive) setError(message(cause)); }).finally(() => { if (alive) setBusy(""); });
    }
    return () => { alive = false; };
  }, [open, tool, deployment.id, deployment.appId, info, activity]);
  const nav = (id: string, event: React.MouseEvent<HTMLAnchorElement>) => {
    if (!onSelectDeployment || !shouldHandleNavigation(event)) return;
    event.preventDefault(); void perform("Opening deployment", async () => { await onSelectDeployment(id); });
  };
  const selected = deployment.id;
  return <section className="release-tools" aria-label="Release tools">
    <button type="button" className="release-tools-heading" aria-expanded={open} onClick={() => setOpen(value => !value)}><ShieldCheck size={18} /><strong>Release tools</strong><span>Preview, rollback, diagnosis, and activity</span><span>{open ? "Hide" : "Open"}</span></button>
    {open && <div className="release-tools-body">
      <div className="release-tool-tabs" role="tablist" aria-label="Release tools">
        {([ ["notes", "Notes & source", NotePencil], ["activity", "Activity", ClockCounterClockwise], ["diagnosis", "Diagnose", MagnifyingGlass], ...(canDeploy ? [["preview", "Preview & rollback", ShieldCheck]] : []) ] as const).map(([name, label, Icon]) => <button type="button" key={name as string} role="tab" aria-selected={tool === name} onClick={() => { setTool(name as Tool); setError(""); setNotice(""); }}><Icon size={15} />{label as string}</button>)}
      </div>
      {busy && <p role="status">{busy}…</p>}{error && <p role="alert" className="error-message">{error}</p>}{notice && <p role="status">{notice}</p>}
      {tool === "notes" && info && <div className="release-note-editor">
        {info.sourceLinks.length > 0 && <div className="release-source-links">{info.sourceLinks.map(link => <a href={link} target="_blank" rel="noopener noreferrer" key={link}>View commit {short(deployment.commitSha)}<ArrowSquareOut size={14} /></a>)}</div>}
        {canConfigure ? <form onSubmit={event => { event.preventDefault(); void perform("Saving release notes", async () => { const note = await releaseClient.saveNote(selected, notes, links.split("\n").map(link => link.trim()).filter(Boolean)); if (current.current === selected) { setInfo({ ...info, note }); setNotice("Release notes saved."); setActivity(undefined); } }); }}>
          <label>Release notes<textarea aria-label="Release notes" value={notes} maxLength={12000} rows={4} onChange={event => setNotes(event.target.value)} placeholder="What changed and what operators should know. Keep credentials out of notes." /></label>
          <label>Pull request and source links<textarea aria-label="Source links" value={links} rows={2} onChange={event => setLinks(event.target.value)} placeholder="One HTTPS link per line" /></label>
          <button type="submit" className="quiet-button" disabled={Boolean(busy)}>Save notes</button>
        </form> : <><p className="release-note-text">{info.note.notes || "No release notes recorded."}</p>{info.note.links.map(link => <p key={link}><a href={link} target="_blank" rel="noopener noreferrer">{link}<ArrowSquareOut size={14} /></a></p>)}</>}
        {info.note.updatedAt && <small>Updated {relative(info.note.updatedAt)}{info.note.actor ? ` by ${info.note.actor}` : ""}</small>}
      </div>}
      {tool === "activity" && activity && <div className="release-activity"><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => setActivity(undefined)}>Refresh activity</button>{activity.length ? <ol>{activity.map(item => <li key={`${item.kind}:${item.id}`}><ClockCounterClockwise size={15} /><div><strong>{item.message}</strong><small>{item.kind}{item.actor ? ` · ${item.actor}` : ""} · <time dateTime={item.createdAt}>{relative(item.createdAt)}</time></small>{item.deploymentId && <a href={routePath({ view: "deployments", deploymentID: item.deploymentId })} onClick={event => nav(item.deploymentId!, event)}>Open deployment {short(item.revision) || item.deploymentId.slice(-6)}</a>}</div></li>)}</ol> : <p>No retained activity.</p>}</div>}
      {tool === "diagnosis" && <div className="release-diagnosis"><p>Inspect current failing pods, events, and recent logs from the Dispatch controller.</p><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => void perform("Inspecting workloads", async () => { const value = await releaseClient.diagnosis(selected); if (current.current === selected) setDiagnosis(value); })}>Inspect now</button>{diagnosis && <><p>{diagnosis.message}</p><small>{diagnosis.location} · {new Date(diagnosis.checkedAt).toLocaleString()}</small>{diagnosis.live && diagnosis.issues.length === 0 && <p><CheckCircle size={16} /> No failing containers found in the inspected pods.</p>}{diagnosis.issues.map((issue, index) => <article key={`${issue.resource}:${issue.container}:${index}`}><h3><WarningCircle size={16} />{issue.resource}{issue.container ? ` / ${issue.container}` : ""}</h3><p><strong>{issue.reason}</strong>{issue.restarts ? ` · ${issue.restarts} restarts` : ""}</p><p>{issue.nextStep}</p>{issue.events.length > 0 && <ul>{issue.events.map((event, i) => <li key={i}><strong>{event.reason}</strong> {event.message}</li>)}</ul>}{issue.logs.map(log => <details key={log.container}><summary>Recent logs: {log.container}</summary><pre>{log.content || log.error}</pre></details>)}</article>)}<details><summary>Deployment log ({diagnosis.deploymentLogs.length} recent entries)</summary><pre>{diagnosis.deploymentLogs.map(log => `${log.createdAt} ${log.level} ${log.message}`).join("\n") || "No readable log entries."}</pre></details></>}</div>}
      {tool === "preview" && canDeploy && <div className="release-review">
        <div className="release-review-actions"><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => void perform("Validating deployment preview", async () => { const value = await releaseClient.preview(deployment.appId, deployment.commitSha); if (current.current === selected) setPreview(value); })}>Preview deployment inputs</button><button type="button" className="quiet-button" disabled={Boolean(busy) || deployment.state !== "succeeded"} onClick={() => void perform("Checking retained rollback", async () => { const value = await releaseClient.rollbackPreview(selected); if (current.current === selected) { setRollback(value); setAcknowledged(false); } })}>Review rollback to this version</button></div>
        <p>Preview uses the application's current configuration with source revision <code>{short(deployment.commitSha)}</code>. Rollback uses this version's retained chart, values, and credentials.</p>
        {preview && <section aria-label="Deployment preview"><h3>{preview.ready ? "Preview complete" : "Preview needs attention"}</h3><p>{preview.target} · {preview.namespace || "local"} · {preview.release || deployment.app?.name}</p><ul className="release-checks">{preview.checks.map(check => <li key={check.name}>{check.state === "passed" ? <CheckCircle size={16} /> : <WarningCircle size={16} />}<div><strong>{check.name}: {check.state}</strong><p>{check.message}</p></div></li>)}</ul><p>{preview.message}</p>{preview.comparison.available && <details><summary>{preview.comparison.changes.length} saved input changes</summary><div className="release-diff"><table><thead><tr><th>Field</th><th>Before</th><th>After</th></tr></thead><tbody>{preview.comparison.changes.slice(0,100).map(change => <tr key={change.path}><th><code>{change.path}</code></th><td><code>{display(change.before)}</code></td><td><code>{display(change.after)}</code></td></tr>)}</tbody></table></div></details>}<p>{preview.resources.length} resources validated · sensitive values excluded.</p>{preview.ready && !deployment.app?.generated && <button type="button" className="primary-button" disabled={Boolean(busy)} onClick={() => void perform("Starting deployment", async () => { const value = await request<Deployment>(`/api/v1/apps/${encodeURIComponent(deployment.appId)}/deployments`, { method:"POST", body:JSON.stringify({ commitSha:preview.revision, review:preview.review }) }); if (current.current === selected) { setNotice("Deployment accepted."); await onDeployment?.(value); } })}>Deploy reviewed revision</button>}{deployment.app?.generated && <p>Use the environment approval and promotion controls for this repository-managed application.</p>}</section>}
        {rollback && <section className="release-rollback-review" aria-label="Rollback review"><h3>{rollback.available ? `Restore retained Helm revision ${rollback.helmRevision}` : "Rollback unavailable"}</h3><p>{rollback.message}</p>{rollback.bindings?.length > 0 && <ul>{rollback.bindings.map(binding => <li key={binding.alias}>{binding.alias}: {binding.serviceName}, configuration revision {binding.revision}</li>)}</ul>}{rollback.available && <><p>{rollback.resources.length} resources passed server-side validation. Original immutable service Secrets remain in use.</p><label className="release-confirm"><input type="checkbox" checked={acknowledged} onChange={event => setAcknowledged(event.target.checked)} />I understand this restores the selected release and does not undo database migrations or external effects.</label><button type="button" className="danger-button" disabled={!acknowledged || Boolean(busy)} onClick={() => void perform("Starting rollback", async () => { const value = await releaseClient.rollback(selected, rollback.currentDeploymentId); if (current.current === selected) { setRollback(undefined); setNotice("Rollback accepted."); await onDeployment?.(value); } })}>Restore this version</button></>}</section>}
      </div>}
    </div>}
  </section>;
}

export function PromotionWorkspace({ stages, revisions, canApprove, onRefresh }: { stages: WorkflowStageRun[]; revisions: WorkflowRevision[]; canApprove: boolean; onRefresh?: () => void | Promise<void> }) {
  const [selected, setSelected] = useState<string>();
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const pending = stages.filter(stage => stage.state === "waiting_approval" || stage.state === "awaiting_approval" || stage.approval === "pending");
  if (!pending.length) return null;
  return <section className="promotion-workspace" aria-label="Environment approvals"><h2>Ready for approval</h2><p>Approval promotes the exact captured revision and reuses its build outputs.</p>{error && <p role="alert">{error}</p>}{pending.map(stage => {
    const revision = revisions.find(item => item.id === stage.revisionId);
    return <article key={stage.id}><div><strong>{stage.stageName}</strong><span>{stage.targetRef}</span><code>{short(revision?.configSha || stage.revisionId)}</code></div>{selected === stage.id ? <><dl><dt>Workflow revision</dt><dd><code>{stage.revisionId}</code></dd><dt>Configuration revision</dt><dd><code>{revision?.configSha || "Unavailable"}</code></dd></dl>{revision && <ul>{Object.entries(revision.sources).map(([name, source]) => <li key={name}>{name}: <code>{source.commitSha}</code></li>)}</ul>}<button type="button" className="primary-button" disabled={!canApprove || Boolean(busy) || !revision} onClick={async () => { setBusy(stage.id); setError(""); try { await api.approveWorkflowStage(stage.id); setSelected(undefined); await onRefresh?.(); } catch(cause) { setError(message(cause)); } finally { setBusy(""); } }}>{busy === stage.id ? "Approving…" : "Approve this revision"}</button><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => setSelected(undefined)}>Cancel</button></> : <button type="button" className="quiet-button" onClick={() => setSelected(stage.id)}>Review revision</button>}</article>;
  })}</section>;
}
