import { useEffect, useId, useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import { ArrowClockwise, ArrowCounterClockwise, ArrowRight, ArrowSquareOut, CaretDown, CheckCircle, Clock, ClockCounterClockwise, GitCommit, Info, MagnifyingGlass, NotePencil, RocketLaunch, ShieldCheck, WarningCircle } from "@phosphor-icons/react";
import { api, type Deployment, type WorkflowRevision, type WorkflowStageRun } from "../api";
import { relative, short } from "../presentation";
import { routePath, shouldHandleNavigation } from "../routes";
import { releaseClient, type Activity, type Diagnosis, type ReleaseInfo, type ReleasePreview, type RollbackPreview } from "./releaseClient";
import "./release-tools.css";

type Tool = "notes" | "activity" | "diagnosis" | "deploy" | "restore";
type Props = {
  deployment: Deployment; canDeploy: boolean; canConfigure: boolean;
  onDeployment?: (deployment: Deployment) => void | Promise<void>;
  onSelectDeployment?: (id: string) => void | Promise<void>;
};
const message = (error: unknown) => error instanceof Error ? error.message : String(error);
const display = (value: unknown) => typeof value === "string" ? value : JSON.stringify(value);
const titleCase = (value: string) => value ? value[0].toUpperCase() + value.slice(1) : "Unknown";
const revisionLabel = (revision: string | undefined, id: string, length = 6) => revision?.trim() ? short(revision.trim()) : id.slice(-length);
const recordedTimestamp = (value: string | undefined) => value && !value.startsWith("0001-01-01") && Number.isFinite(Date.parse(value)) ? value : undefined;

// Switching releases starts a new workbench, including all pending confirmations.
export function ReleaseTools(props: Props) {
  return <ReleaseWorkbench key={props.deployment.id} {...props} />;
}

function ReleaseWorkbench({ deployment, canDeploy, canConfigure, onDeployment, onSelectDeployment }: Props) {
  const [tool, setTool] = useState<Tool>("notes");
  const [info, setInfo] = useState<ReleaseInfo>();
  const [editing, setEditing] = useState(false);
  const [notes, setNotes] = useState("");
  const [links, setLinks] = useState("");
  const [activity, setActivity] = useState<Activity[]>();
  const [activityLimit, setActivityLimit] = useState(10);
  const [diagnosis, setDiagnosis] = useState<Diagnosis>();
  const [preview, setPreview] = useState<ReleasePreview>();
  const [rollback, setRollback] = useState<RollbackPreview>();
  const [deployConfirmed, setDeployConfirmed] = useState(false);
  const [restoreConfirmed, setRestoreConfirmed] = useState(false);
  const [busy, setBusy] = useState("");
  const [loading, setLoading] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const alive = useRef(true);
  const action = useRef(false);
  const instance = useId();
  const canStart = canDeploy && !deployment.app?.generated;
  const selected = deployment.id;
  const tabs = [
    { id: "notes", label: "Notes", Icon: NotePencil },
    { id: "activity", label: "Activity", Icon: ClockCounterClockwise },
    { id: "diagnosis", label: "Diagnose", Icon: MagnifyingGlass },
    ...(canDeploy ? [
      { id: "deploy", label: canStart ? "Deploy" : "Preview", Icon: RocketLaunch },
      { id: "restore", label: "Restore", Icon: ArrowCounterClockwise },
    ] : []),
  ] as const;
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => {
    if (!canDeploy && (tool === "deploy" || tool === "restore")) setTool("notes");
    if (!canConfigure) setEditing(false);
  }, [canDeploy, canConfigure, tool]);

  async function perform(label: string, work: () => Promise<void>) {
    if (action.current) return;
    action.current = true; setBusy(label); setError(""); setNotice("");
    try { await work(); }
    catch (cause) {
      if (alive.current) {
        setError(message(cause));
        if (cause instanceof Error && (cause as Error & { status?: number }).status === 409) {
          setPreview(undefined); setRollback(undefined); setDeployConfirmed(false); setRestoreConfirmed(false);
        }
      }
    } finally { if (alive.current) { action.current = false; setBusy(""); } }
  }
  useEffect(() => {
    let current = true;
    setLoading(false);
    const load = tool === "notes" && !info
      ? releaseClient.info(selected).then(value => { if (current) { setInfo(value); setNotes(value.note.notes); setLinks(value.note.links.join("\n")); } })
      : tool === "activity" && !activity
        ? releaseClient.activity(deployment.appId).then(value => { if (current) setActivity(value.items); })
        : undefined;
    if (load) {
      setLoading(true);
      void load.catch(cause => { if (current) setError(message(cause)); }).finally(() => { if (current) setLoading(false); });
    }
    return () => { current = false; };
    // Cached panels should not start another request when their result arrives.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tool, selected, deployment.appId, refresh]);

  const choose = (next: Tool) => {
    setTool(next); setError(""); setNotice(""); setDeployConfirmed(false); setRestoreConfirmed(false);
  };
  const tabKey = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const next = event.key === "ArrowRight" ? (index + 1) % tabs.length : event.key === "ArrowLeft" ? (index + tabs.length - 1) % tabs.length : event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : -1;
    if (next < 0) return;
    event.preventDefault(); choose(tabs[next].id as Tool);
    document.getElementById(`${instance}-${tabs[next].id}`)?.focus();
  };
  const nav = (id: string, event: MouseEvent<HTMLAnchorElement>) => {
    if (!onSelectDeployment || !shouldHandleNavigation(event)) return;
    event.preventDefault();
    void Promise.resolve().then(() => onSelectDeployment(id)).catch(cause => { if (alive.current) setError(message(cause)); });
  };
  const changed = info && (notes !== info.note.notes || links !== info.note.links.join("\n"));
  const revision = revisionLabel(deployment.commitSha, deployment.id, 8);
  const noteUpdatedAt = recordedTimestamp(info?.note.updatedAt);
  const stateTone = deployment.state === "succeeded" ? "success" : deployment.state === "failed" ? "danger" : deployment.state === "cancelled" ? "muted" : "active";

  return <section className="release-workbench" aria-label="Release tools">
    <header className="release-workbench-header">
      <div className="release-selected"><span className="release-section-label">Release tools</span><GitCommit size={16} /><code title={deployment.commitSha?.trim() || deployment.id}>{revision}</code><span className={`release-state ${stateTone}`}><ReleaseState state={deployment.state} />{titleCase(deployment.state)}</span></div>
      <div className="release-selected-meta"><span title={deployment.app?.name}>{deployment.app?.name || "Selected deployment"}</span>{deployment.server?.name && <span>{deployment.server.name}</span>}<time dateTime={deployment.createdAt} title={new Date(deployment.createdAt).toLocaleString()}>{relative(deployment.createdAt)}</time></div>
    </header>
    <div className="release-workbench-nav" role="tablist" aria-label="Release tools">
      {tabs.map(({ id, label, Icon }, index) => <button type="button" id={`${instance}-${id}`} key={id} role="tab" aria-selected={tool === id} aria-controls={`${instance}-panel`} tabIndex={tool === id ? 0 : -1} onKeyDown={event => tabKey(event, index)} onClick={() => choose(id as Tool)}><Icon size={15} />{label}</button>)}
    </div>
    <div className="release-workbench-panel" id={`${instance}-panel`} role="tabpanel" aria-labelledby={`${instance}-${tool}`} aria-busy={loading || Boolean(busy)}>
      {busy && <div className="release-feedback" role="status"><Clock size={15} />{busy}…</div>}
      {error && <div className="release-feedback danger" role="alert"><WarningCircle size={16} /><span>{error}</span>{(tool === "notes" && !info || tool === "activity" && !activity) && <button type="button" className="release-text-button" onClick={() => { setError(""); setRefresh(value => value + 1); }}>Retry</button>}</div>}
      {notice && <div className="release-feedback success" role="status"><CheckCircle size={16} />{notice}</div>}
      {loading && <div className="release-panel-loading" role="status">Loading {tool === "notes" ? "release notes" : "activity"}…</div>}

      {tool === "notes" && info && <div className="release-notes">
        <div className="release-panel-heading"><div><h3>Release notes</h3>{noteUpdatedAt && <small>Updated {relative(noteUpdatedAt)}{info.note.actor ? ` by ${info.note.actor}` : ""}</small>}</div>{canConfigure && !editing && <button type="button" className="quiet-button" onClick={() => setEditing(true)}><NotePencil size={14} />{info.note.notes ? "Edit notes" : "Add notes"}</button>}</div>
        {editing && canConfigure ? <form className="release-note-form" onSubmit={event => { event.preventDefault(); void perform("Saving release notes", async () => {
          const note = await releaseClient.saveNote(selected, notes, links.split("\n").map(link => link.trim()).filter(Boolean));
          if (alive.current) { setInfo({ ...info, note }); setNotes(note.notes); setLinks(note.links.join("\n")); setEditing(false); setNotice("Release notes saved."); setActivity(undefined); }
        }); }}>
          <label>Release notes<textarea aria-label="Release notes" value={notes} maxLength={12000} rows={3} onChange={event => setNotes(event.target.value)} placeholder="What changed in this release?" /></label>
          <label>Related links<textarea aria-label="Source links" value={links} rows={2} onChange={event => setLinks(event.target.value)} placeholder="One HTTPS link per line" /></label>
          <footer><small>Notes are visible to everyone in this project. Leave out credentials.</small><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => { setNotes(info.note.notes); setLinks(info.note.links.join("\n")); setEditing(false); }}>Cancel</button><button type="submit" className="primary-button" disabled={Boolean(busy) || !changed}>Save notes</button></footer>
        </form> : <>
          <p className={`release-note-text${info.note.notes ? "" : " empty"}`}>{info.note.notes || "No notes for this release yet."}</p>
          {(info.sourceLinks.length > 0 || info.note.links.length > 0) && <div className="release-links">{info.sourceLinks.map(link => <a href={link} target="_blank" rel="noopener noreferrer" key={link}><GitCommit size={14} />View commit<ArrowSquareOut size={13} /></a>)}{info.note.links.map(link => <a href={link} target="_blank" rel="noopener noreferrer" key={link} title={link}><span>{link.replace(/^https:\/\//, "")}</span><ArrowSquareOut size={13} /></a>)}</div>}
        </>}
      </div>}

      {tool === "activity" && activity && <div className="release-activity">
        <div className="release-panel-heading"><div><h3>Application activity <span>{activity.length}</span></h3><small>Retained releases and operator actions</small></div><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => { setActivity(undefined); setRefresh(value => value + 1); }}><ArrowClockwise size={14} />Refresh</button></div>
        {activity.length ? <ol>{activity.slice(0, activityLimit).map(item => <li key={`${item.kind}:${item.id}`}><span className="release-timeline-icon"><ReleaseState state={item.state || ""} /></span><div><strong>{item.message}</strong><small>{item.actor || titleCase(item.kind)} · <time dateTime={item.createdAt}>{relative(item.createdAt)}</time></small></div>{item.deploymentId && <a href={routePath({ view: "deployments", deploymentID: item.deploymentId })} onClick={event => nav(item.deploymentId!, event)} aria-label={`Open deployment ${revisionLabel(item.revision, item.deploymentId)}`}><code>{revisionLabel(item.revision, item.deploymentId)}</code><ArrowRight size={13} /></a>}</li>)}</ol> : <p className="release-empty">No retained activity for this application.</p>}
        {activity.length > activityLimit && <button type="button" className="release-text-button" onClick={() => setActivityLimit(value => value + 20)}>Show {Math.min(20, activity.length - activityLimit)} more events</button>}
      </div>}

      {tool === "diagnosis" && <div className="release-diagnosis">
        <div className="release-panel-heading"><div><h3>Workload diagnosis</h3><small>Current pods and events · Dispatch controller</small></div><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => void perform("Inspecting workloads", async () => { const value = await releaseClient.diagnosis(selected); if (alive.current) setDiagnosis(value); })}><MagnifyingGlass size={14} />{diagnosis ? "Inspect again" : "Inspect now"}</button></div>
        {!diagnosis ? <p className="release-empty">Check failing containers and get the next steps to investigate.</p> : <DiagnosisResult diagnosis={diagnosis} />}
      </div>}

      {tool === "deploy" && canDeploy && <div className="release-deploy">
        <div className="release-panel-heading"><div><h3>{canStart ? "Deploy this source revision" : "Preview this source revision"}</h3><small>Current application settings · source <code>{revision}</code></small></div><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => void perform("Checking deployment inputs", async () => {
          setPreview(undefined); setDeployConfirmed(false);
          const value = await releaseClient.preview(deployment.appId, deployment.commitSha); if (alive.current) setPreview(value);
        })}><ShieldCheck size={14} />{preview ? "Check again" : "Check inputs"}</button></div>
        {!preview ? <p className="release-empty">Validate credentials, rendered resources, and saved input changes before deploying.</p> : <>
          <div className={`release-result-line ${preview.ready ? "success" : "danger"}`}><ReleaseState state={preview.ready ? "succeeded" : "failed"} /><strong>{preview.ready ? "Inputs reviewed" : "Review needs attention"}</strong><span>{preview.target}{preview.namespace ? ` / ${preview.namespace}` : ""}</span></div>
          <ValidationDetails checks={preview.checks} />
          {preview.comparison.available && <details className="release-detail"><summary><span>Saved input changes</span><small>{preview.comparison.changes.length}</small><CaretDown size={14} /></summary>{preview.comparison.changes.length ? <div className="release-diff"><table><thead><tr><th>Field</th><th>Before</th><th>After</th></tr></thead><tbody>{preview.comparison.changes.slice(0, 100).map(change => <tr key={change.path}><th><code>{change.path}</code></th><td><code>{display(change.before)}</code></td><td><code>{display(change.after)}</code></td></tr>)}</tbody></table>{preview.comparison.changes.length > 100 && <small>Showing the first 100 changes.</small>}</div> : <p>No visible input changes.</p>}</details>}
          <small className="release-scope-note">{preview.resources.length} resources validated. Sensitive values excluded. {preview.message}</small>
          {preview.ready && canStart && <div className="release-confirmation">
            <label><input type="checkbox" checked={deployConfirmed} onChange={event => setDeployConfirmed(event.target.checked)} /><span>Deploy <code>{short(preview.revision) || preview.revision}</code> with the reviewed settings{preview.release ? <> to <strong>{preview.release}</strong></> : ""}.{preview.checks.some(check => check.state !== "passed") && " I have reviewed the checks that could not run."}</span></label>
            <button type="button" className="primary-button" disabled={!deployConfirmed || Boolean(busy)} onClick={() => void perform("Starting deployment", async () => {
              const value = await api.deploy(deployment.appId, preview.revision, preview.review);
              if (alive.current) { setPreview(undefined); setDeployConfirmed(false); setNotice("Deployment accepted."); await onDeployment?.(value); }
            })}><RocketLaunch size={14} />Deploy reviewed revision</button>
          </div>}
        </>}
        {!canStart && <p className="release-inline-note"><Info size={15} />Use environment approval and promotion for this repository-managed application.</p>}
      </div>}

      {tool === "restore" && canDeploy && <div className="release-restore">
        <div className="release-panel-heading"><div><h3>Restore this release</h3><small>Retained chart, values, and credentials · <code>{revision}</code></small></div>{deployment.state === "succeeded" && <button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => void perform("Checking retained release", async () => {
          setRollback(undefined); setRestoreConfirmed(false);
          const value = await releaseClient.rollbackPreview(selected); if (alive.current) setRollback(value);
        })}><ArrowCounterClockwise size={14} />{rollback ? "Check again" : "Review restore"}</button>}</div>
        {deployment.state !== "succeeded" ? <p className="release-inline-note"><Info size={15} />Choose a successful deployment from History to review a restore.</p> : !rollback ? <p className="release-empty">Check that the original release and its credentials are still available.</p> : <>
          <div className={`release-result-line ${rollback.available ? "success" : "muted"}`}><ReleaseState state={rollback.available ? "succeeded" : "unknown"} /><strong>{rollback.available ? `Helm revision ${rollback.helmRevision} is available` : "Restore unavailable"}</strong></div>
          <p className="release-result-message">{rollback.message}</p>
          {rollback.bindings?.length > 0 && <details className="release-detail"><summary><span>Retained service credentials</span><small>{rollback.bindings.length}</small><CaretDown size={14} /></summary><ul className="release-binding-list">{rollback.bindings.map(binding => <li key={binding.alias}><strong>{binding.alias}</strong><span>{binding.serviceName}</span><small>Revision {binding.revision}</small></li>)}</ul></details>}
          {rollback.available && <>
            <div className="release-restore-path"><span>Running <code title={rollback.currentDeploymentId}>{rollback.currentDeploymentId.slice(-8)}</code></span><ArrowRight size={13} /><span>Restore <code title={deployment.commitSha || selected}>{revision}</code></span></div>
            <small className="release-scope-note">{rollback.resources.length} resources validated. Uses this version's original service credentials.</small>
            <div className="release-confirmation restore"><label><input type="checkbox" checked={restoreConfirmed} onChange={event => setRestoreConfirmed(event.target.checked)} /><span>Restore release <code>{revision}</code>. I understand this does not undo database migrations or external effects.</span></label><button type="button" className="danger-button" disabled={!restoreConfirmed || Boolean(busy)} onClick={() => void perform("Starting restore", async () => {
              const value = await releaseClient.rollback(selected, rollback.currentDeploymentId);
              if (alive.current) { setRollback(undefined); setRestoreConfirmed(false); setNotice("Restore accepted."); await onDeployment?.(value); }
            })}><ArrowCounterClockwise size={14} />Restore this version</button></div>
          </>}
        </>}
      </div>}
    </div>
  </section>;
}

function ReleaseState({ state }: { state: string }) {
  if (state === "succeeded" || state === "passed") return <CheckCircle size={15} weight="fill" className="release-icon-success" />;
  if (state === "failed" || state === "rejected") return <WarningCircle size={15} weight="fill" className="release-icon-danger" />;
  if (state === "unavailable" || state === "unknown") return <Info size={15} className="release-icon-muted" />;
  return <Clock size={15} className="release-icon-muted" />;
}
function ValidationDetails({ checks }: { checks: ReleasePreview["checks"] }) {
  const passed = checks.filter(check => check.state === "passed").length;
  return <details className="release-detail" open={checks.some(check => check.state === "failed")}><summary><span>Validation checks</span><small>{passed}/{checks.length} passed</small><CaretDown size={14} /></summary><ul className="release-checks">{checks.map(check => <li key={check.name}><ReleaseState state={check.state} /><div><strong>{check.name}</strong><p>{check.message}</p></div><span className={`release-check-state ${check.state}`}>{titleCase(check.state)}</span></li>)}</ul></details>;
}
function DiagnosisResult({ diagnosis }: { diagnosis: Diagnosis }) {
  return <>
    <div className="release-result-caption"><span>{diagnosis.message}</span><time dateTime={diagnosis.checkedAt}>{relative(diagnosis.checkedAt)}</time></div>
    {diagnosis.live && diagnosis.issues.length === 0 && <p className="release-inline-note success"><CheckCircle size={16} />No failing containers found in the inspected pods.</p>}
    {diagnosis.issues.map((issue, index) => <article className="release-issue" key={`${issue.resource}:${issue.container}:${index}`}>
      <div className="release-issue-heading"><WarningCircle size={16} /><strong>{issue.reason}</strong>{(issue.restarts ?? 0) > 0 && <small>{issue.restarts} restarts</small>}</div>
      <code className="release-issue-resource">{issue.resource}{issue.container ? ` / ${issue.container}` : ""}</code>
      <p className="release-next-step"><ArrowRight size={14} />{issue.nextStep}</p>
      {issue.events.length > 0 && <details className="release-detail"><summary><span>Related events</span><small>{issue.events.length}</small><CaretDown size={14} /></summary><ul className="release-event-list">{issue.events.map((event, i) => <li key={i}><strong>{event.reason}</strong><p>{event.message}</p></li>)}</ul></details>}
      {issue.logs.map(log => <details className="release-detail" key={log.container}><summary><span>Container logs</span><code>{log.container}</code><CaretDown size={14} /></summary><pre>{log.content || log.error || "No readable log entries."}</pre></details>)}
    </article>)}
    <details className="release-detail"><summary><span>Deployment log</span><small>{diagnosis.deploymentLogs.length} recent entries</small><CaretDown size={14} /></summary><pre>{diagnosis.deploymentLogs.map(log => `${log.createdAt} ${log.level} ${log.message}`).join("\n") || "No readable log entries."}</pre></details>
  </>;
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
