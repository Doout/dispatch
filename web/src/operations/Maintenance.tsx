import { useEffect, useRef, useState } from "react";
import { Archive, ArrowClockwise, ArrowRight, CaretDown, CheckCircle, Clock, Database, Info, PencilSimple, ShieldCheck, Trash, WarningCircle } from "@phosphor-icons/react";
import { request, type Overview } from "../api";
import { canManageProject } from "../permissions";
import { relative } from "../presentation";
import "./Maintenance.css";

type Policy = { projectId: string; logDays: number; runDays: number; keepRuns: number };
type RetentionResult = { logs: number; runs: number; protectedRuns: number; applied: boolean };
type Review = { policy: Policy; result: RetentionResult; checkedAt: string };
type Backup = { id: string; engine: string; state: string; bytes: number; createdAt: string; verifiedAt?: string; message: string };
type BackupList = { configured: boolean; backups: Backup[] };
type Draft = Record<"logDays" | "runDays" | "keepRuns", string>;
const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error);
const count = (value: number) => new Intl.NumberFormat("en").format(value);
const policyDraft = (policy: Policy): Draft => ({ logDays: String(policy.logDays), runDays: String(policy.runDays), keepRuns: String(policy.keepRuns) });
const samePolicy = (left: Policy, right: Policy) => left.projectId === right.projectId && left.logDays === right.logDays && left.runDays === right.runDays && left.keepRuns === right.keepRuns;
const fullDate = (value: string) => Number.isFinite(Date.parse(value)) ? new Date(value).toLocaleString() : "Time unavailable";
const ago = (value: string) => Number.isFinite(Date.parse(value)) ? relative(value) : "Time unavailable";

export function RetentionControls({ overview, projectId, onChanged }: { overview: Overview; projectId?: string; onChanged?: () => void }) {
  const project = overview.projects.find(item => item.id === projectId);
  if (!projectId) return <section className="maintenance-panel" aria-label="History retention"><div className="maintenance-empty"><Archive size={22} /><h2>Select a project to manage retention</h2><p>Choose a project in the filter above to review its cleanup policy.</p></div></section>;
  if (!project || !canManageProject(overview, projectId, "project.manage")) return <section className="maintenance-panel" aria-label="History retention"><div className="maintenance-empty"><ShieldCheck size={22} /><h2>Project admin access required</h2><p>You need project management permission to review or remove retained history.</p></div></section>;
  return <ProjectRetention key={`${overview.identity?.id}:${project.id}`} projectId={project.id} projectName={project.name} onChanged={onChanged} />;
}

function ProjectRetention({ projectId, projectName, onChanged }: { projectId: string; projectName: string; onChanged?: () => void }) {
  const [policy, setPolicy] = useState<Policy>();
  const [draft, setDraft] = useState<Draft>({ logDays: "", runDays: "", keepRuns: "" });
  const [editing, setEditing] = useState(false);
  const [review, setReview] = useState<Review>();
  const [confirming, setConfirming] = useState(false);
  const [acknowledged, setAcknowledged] = useState(false);
  const [applied, setApplied] = useState<RetentionResult>();
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [reload, setReload] = useState(0);
  const alive = useRef(true);
  const locked = useRef(false);
  const base = `/api/v1/projects/${encodeURIComponent(projectId)}/retention`;
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => {
    let current = true;
    setLoading(true); setPolicy(undefined); setReview(undefined); setConfirming(false); setAcknowledged(false); setEditing(false);
    void request<Policy>(base).then(value => {
      if (!current) return;
      if (value.projectId !== projectId) throw new Error("The retention policy did not match the selected project. Refresh to try again.");
      setPolicy(value); setDraft(policyDraft(value));
    }).catch(cause => { if (current) setError(errorMessage(cause)); }).finally(() => { if (current) setLoading(false); });
    return () => { current = false; };
  }, [base, projectId, reload]);

  function clearReview() { setReview(undefined); setConfirming(false); setAcknowledged(false); setApplied(undefined); }
  async function act(label: string, work: () => Promise<void>) {
    if (locked.current) return;
    locked.current = true; setBusy(label); setError(""); setNotice("");
    try { await work(); }
    catch (cause) {
      if (!alive.current) return;
      setError(errorMessage(cause));
      if (cause instanceof Error && (cause as Error & { status?: number }).status === 409) {
        clearReview(); setNotice("The policy changed. Review the current policy and preview cleanup again."); setReload(value => value + 1);
      }
    } finally { if (alive.current) { locked.current = false; setBusy(""); } }
  }
  const draftPolicy: Policy = { projectId, logDays: Number(draft.logDays), runDays: Number(draft.runDays), keepRuns: Number(draft.keepRuns) };
  const valid = (["logDays", "runDays", "keepRuns"] as const).every(key => /^\d+$/.test(draft[key]) && Number.isSafeInteger(draftPolicy[key]) && draftPolicy[key] >= (key === "keepRuns" ? 5 : 1) && draftPolicy[key] <= (key === "keepRuns" ? 10000 : 36500));
  const changed = policy && !samePolicy(draftPolicy, policy);

  return <section className="maintenance-panel retention-controls" aria-label="History retention">
    <header className="maintenance-heading"><div><h2>History retention</h2><p>{projectName} · Cleanup runs only when you apply it.</p></div>{policy && !editing && <button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => { clearReview(); setError(""); setNotice(""); setDraft(policyDraft(policy)); setEditing(true); }}><PencilSimple size={14} />Edit policy</button>}</header>
    {loading && <p className="maintenance-loading" role="status"><Clock size={15} />Loading retention policy…</p>}
    {error && <div className="maintenance-feedback danger" role="alert"><WarningCircle size={16} /><span>{error}</span>{!policy && !loading && <button type="button" className="maintenance-text-button" onClick={() => { setError(""); setReload(value => value + 1); }}>Retry</button>}</div>}
    {notice && <p className="maintenance-feedback" role="status"><Info size={15} />{notice}</p>}
    {busy && <p className="maintenance-loading" role="status"><Clock size={15} />{busy}…</p>}
    {policy && <>
      {editing ? <form className="retention-policy-editor" onSubmit={event => { event.preventDefault(); if (!valid || !changed) return; const next = { ...draftPolicy }; void act("Saving policy", async () => {
        const saved = await request<Policy>(base, { method: "PUT", body: JSON.stringify(next) });
        if (!alive.current) return;
        if (saved.projectId !== projectId) throw new Error("The saved policy did not match the selected project.");
        setPolicy(saved); setDraft(policyDraft(saved)); setEditing(false); clearReview(); setNotice("Policy saved. Preview cleanup to see what is eligible."); onChanged?.();
      }); }}>
        <div className="retention-fields">{([ ["logDays", "Keep logs for", "days"], ["runDays", "Keep failed and cancelled runs for", "days"], ["keepRuns", "Keep at least", "runs per application"] ] as const).map(([key, label, unit]) => <label key={key}>{label}<div><input aria-label={label} type="number" min={key === "keepRuns" ? 5 : 1} max={key === "keepRuns" ? 10000 : 36500} step={1} required value={draft[key]} disabled={Boolean(busy)} onChange={event => { setDraft(value => ({ ...value, [key]: event.target.value })); clearReview(); }} /><span>{unit}</span></div></label>)}</div>
        <footer><small>Saving a policy does not remove history.</small><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => { setDraft(policyDraft(policy)); setEditing(false); }}>Cancel</button><button type="submit" className="primary-button" disabled={Boolean(busy) || !valid || !changed}>Save policy</button></footer>
      </form> : <>
        <dl className="retention-policy-summary"><div><dt>Log history</dt><dd>{count(policy.logDays)} <span>days</span></dd></div><div><dt>Failed and cancelled runs</dt><dd>{count(policy.runDays)} <span>days</span></dd></div><div><dt>Minimum retained</dt><dd>{count(policy.keepRuns)} <span>runs / application</span></dd></div></dl>
        <div className="retention-preview-action"><span><ShieldCheck size={15} />Successful releases and active runs remain protected.</span><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => { const captured = { ...policy }; clearReview(); void act("Checking eligible history", async () => {
          const result = await request<RetentionResult>(`${base}/preview`, { method: "POST", body: JSON.stringify({ expectedPolicy: captured }) });
          if (alive.current) setReview({ policy: captured, result, checkedAt: new Date().toISOString() });
        }); }}><Archive size={14} />{review ? "Refresh preview" : "Preview cleanup"}</button></div>
      </>}
      {review && !editing && <div className="retention-preview" aria-label="Cleanup preview">
        <div className="maintenance-subheading"><h3>{review.result.logs || review.result.runs ? "Eligible for removal" : "No history eligible for removal"}</h3><small>Checked <time dateTime={review.checkedAt}>{ago(review.checkedAt)}</time></small></div>
        <dl className="retention-counts"><div><dt>Old log lines</dt><dd>{count(review.result.logs)}</dd></div><div><dt>Failed or cancelled runs</dt><dd>{count(review.result.runs)}</dd></div><div className="protected"><dt>Protected runs</dt><dd><ShieldCheck size={16} />{count(review.result.protectedRuns)}</dd></div></dl>
        <p className="maintenance-scope-note">Candidates are checked again when removal starts, so counts can change. Removing a run also removes its remaining logs.</p>
        {(review.result.logs > 0 || review.result.runs > 0) && (confirming ? <div className="retention-confirmation" aria-label="Confirm history removal">
          <h3>Remove eligible history from {projectName}?</h3><p>Deleted logs and run records cannot be recovered from Dispatch. Successful releases, active runs, baselines, and workflow references stay protected.</p>
          <label><input type="checkbox" checked={acknowledged} disabled={Boolean(busy)} onChange={event => setAcknowledged(event.target.checked)} /><span>I understand this permanently removes the eligible history from {projectName}.</span></label>
          <div className="maintenance-actions"><button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => { setConfirming(false); setAcknowledged(false); }}>Cancel removal</button><button type="button" className="danger-button" disabled={!acknowledged || Boolean(busy)} onClick={() => { if (!policy || !samePolicy(policy, review.policy)) { clearReview(); return; } const captured = { ...review.policy }; void act("Removing eligible history", async () => {
            const result = await request<RetentionResult>(`${base}/apply`, { method: "POST", body: JSON.stringify({ confirm: projectId, expectedPolicy: captured }) });
            if (!result.applied) throw new Error("Removal was not applied. Preview the current policy again.");
            if (alive.current) { clearReview(); setApplied(result); onChanged?.(); }
          }); }}><Trash size={14} />Remove eligible history</button></div>
        </div> : <button type="button" className="quiet-button" disabled={Boolean(busy)} onClick={() => { setConfirming(true); setAcknowledged(false); }}>Review removal<ArrowRight size={14} /></button>)}
      </div>}
      {applied && <div className="maintenance-feedback success" role="status"><CheckCircle size={16} /><span>Removed {count(applied.logs)} old log lines and {count(applied.runs)} failed or cancelled runs. {count(applied.protectedRuns)} runs stayed protected.</span></div>}
      <details className="maintenance-details"><summary><Info size={14} /><span>What cleanup keeps</span><CaretDown size={14} /></summary><p>Successful deployment records, active runs, runtime baselines, workflow and preview references, and the minimum number of recent runs stay retained. The latest successful deployment keeps its logs. Old logs from other completed runs can be removed even when the run record is protected.</p><p>Cleanup removes controller history. It does not delete applications, external services, or running workloads.</p></details>
    </>}
  </section>;
}

function backupStatus(backup: Backup) {
  if (backup.state === "verified" && backup.verifiedAt && Number.isFinite(Date.parse(backup.verifiedAt))) return { label: "Restore verified", tone: "success", Icon: CheckCircle };
  if (backup.state === "verification_failed") return { label: "Verification failed", tone: "danger", Icon: WarningCircle };
  if (backup.state === "failed") return { label: "Backup failed", tone: "danger", Icon: WarningCircle };
  if (backup.state === "creating") return { label: "Creation incomplete", tone: "muted", Icon: Clock };
  return { label: "Not verified", tone: "warning", Icon: Info };
}
export function Backups({ onChanged }: { onChanged?: () => void }) {
  const [data, setData] = useState<BackupList>();
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [stale, setStale] = useState(false);
  const [visible, setVisible] = useState(5);
  const alive = useRef(true);
  const locked = useRef(false);
  const sequence = useRef(0);
  async function load() {
    const token = ++sequence.current;
    setLoading(true);
    try {
      const result = await request<BackupList>("/api/v1/operations/backups");
      if (alive.current && sequence.current === token) { setData(result); setStale(false); }
    } catch (cause) {
      if (alive.current && sequence.current === token) { setStale(true); throw cause; }
    } finally { if (alive.current && sequence.current === token) setLoading(false); }
  }
  useEffect(() => {
    alive.current = true;
    void load().catch(cause => { if (alive.current) setError(errorMessage(cause)); });
    return () => { alive.current = false; sequence.current++; };
  }, []);
  async function act(id?: string) {
    if (locked.current || !data?.configured || loading) return;
    locked.current = true; setBusy(id || "create"); setError(""); setNotice("");
    try {
      const saved = await request<Backup>(`/api/v1/operations/backups${id ? `/${encodeURIComponent(id)}/verify` : ""}`, { method: "POST", body: "{}" });
      if (alive.current) {
        if (saved.id) setData(previous => previous && ({ ...previous, backups: [saved, ...previous.backups.filter(item => item.id !== saved.id)] }));
        setNotice(id ? "The isolated restore check passed." : "Backup created. Verify its restore before relying on it.");
      }
    } catch (cause) {
      if (alive.current) {
        setError(errorMessage(cause));
        if (id && cause instanceof Error && (cause as Error & { status?: number }).status === 422) {
          setData(previous => previous && ({ ...previous, backups: previous.backups.map(item => item.id === id ? { ...item, state: "verification_failed", verifiedAt: undefined, message: "The latest restore check failed. Refresh for record details." } : item) }));
        }
      }
    }
    finally {
      if (alive.current) {
        onChanged?.();
        try { await load(); } catch { if (alive.current) setError(value => value ? `${value} The backup list could not be refreshed.` : "The backup list could not be refreshed. Refresh to see the latest result."); }
        if (alive.current) { locked.current = false; setBusy(""); }
      }
    }
  }
  const backups = [...(data?.backups ?? [])].sort((a, b) => (Date.parse(b.createdAt) || 0) - (Date.parse(a.createdAt) || 0) || b.id.localeCompare(a.id));
  const latest = backups[0];
  const verified = backups.find(item => backupStatus(item).tone === "success");
  const status = latest ? backupStatus(latest) : undefined;
  const disabled = loading || Boolean(busy) || !data?.configured;
  return <section className="maintenance-panel controller-backups" aria-label="Controller backups">
    <header className="maintenance-heading"><div><h2>Controller backups</h2><p>Save a recovery copy, then check that it restores.</p></div><div className="maintenance-actions"><button type="button" className="quiet-button" disabled={loading || Boolean(busy)} onClick={() => { setError(""); void load().catch(cause => { if (alive.current) setError(errorMessage(cause)); }); }}><ArrowClockwise size={14} />Refresh</button><button type="button" className="primary-button" disabled={disabled} onClick={() => void act()}><Database size={14} />{busy === "create" ? "Creating backup…" : "Create backup"}</button></div></header>
    {error && <div className="maintenance-feedback danger" role="alert"><WarningCircle size={16} /><span>{error}</span></div>}
    {notice && <p className="maintenance-feedback success" role="status"><CheckCircle size={16} />{notice}</p>}
    {loading && !data && <p className="maintenance-loading" role="status"><Clock size={15} />Loading backup records…</p>}
    {busy && busy !== "create" && <p className="maintenance-loading" role="status"><Clock size={15} />Checking the restore in an isolated database…</p>}
    {data && <>
      {!data.configured && <div className="maintenance-feedback warning" role="status"><Info size={16} /><span>Backup actions are unavailable. Configure the controller master key and backup directory.</span></div>}
      <div className={`backup-recovery-status ${stale ? "muted" : status?.tone || "muted"}`}>
        {stale ? <Info size={22} /> : status ? <status.Icon size={22} /> : <Archive size={22} />}
        <div><h3>{stale ? "Backup status could not be refreshed" : !latest ? "No controller backup recorded" : status?.tone === "success" ? "Latest backup passed its restore check" : latest.state === "failed" ? "Latest backup attempt failed" : latest.state === "verification_failed" ? "Latest backup failed its restore check" : latest.state === "creating" ? "Latest backup has not completed" : "Latest backup has not been verified"}</h3>
          {latest ? <p>{stale ? "Previously recorded copy created " : "Copy created "}<time dateTime={latest.createdAt} title={fullDate(latest.createdAt)}>{ago(latest.createdAt)}</time>{status?.tone === "success" && latest.verifiedAt && <> · Restore checked <time dateTime={latest.verifiedAt} title={fullDate(latest.verifiedAt)}>{ago(latest.verifiedAt)}</time></>}</p> : <p>Create a backup of the controller database and matching vault key.</p>}
          {verified && verified.id !== latest?.id && <small>Newest listed verified copy: <time dateTime={verified.createdAt} title={fullDate(verified.createdAt)}>{ago(verified.createdAt)}</time> · Check passed <time dateTime={verified.verifiedAt} title={fullDate(verified.verifiedAt!)}>{ago(verified.verifiedAt!)}</time>.</small>}
          {!verified && latest && <small>No listed copy has a successful restore check.</small>}
        </div>
      </div>
      {backups.length > 0 && <div className="backup-history"><div className="maintenance-subheading"><h3>Backup history</h3><small>{backups.length === 100 ? "Latest 100 records" : `${backups.length} ${backups.length === 1 ? "record" : "records"}`}{loading ? " · Refreshing…" : ""}</small></div><ol>{backups.slice(0, visible).map(item => { const state = backupStatus(item); return <li key={item.id}>
        <state.Icon size={17} className={state.tone} /><div className="backup-record"><div><time dateTime={item.createdAt} title={fullDate(item.createdAt)}>{fullDate(item.createdAt)}</time><span className={`backup-state ${state.tone}`}>{state.label}</span></div><small>{item.engine === "sqlite" ? "SQLite" : item.engine === "postgresql" ? "PostgreSQL" : item.engine}{item.bytes > 0 ? ` · ${(item.bytes / 1048576).toFixed(1)} MiB` : ""}{state.tone === "success" && item.verifiedAt ? ` · Checked ${ago(item.verifiedAt)}` : ""}</small><details className="backup-record-details"><summary>Record details<CaretDown size={12} /></summary><code>{item.id}</code><p>{item.message}</p></details></div>
        <button type="button" className="quiet-button" disabled={disabled || item.state === "failed" || item.state === "creating"} onClick={() => void act(item.id)}>{busy === item.id ? "Checking restore…" : item.state === "verification_failed" ? "Retry verification" : state.tone === "success" ? "Recheck restore" : "Verify restore"}</button>
      </li>; })}</ol>{backups.length > visible && <button type="button" className="maintenance-text-button" onClick={() => setVisible(value => value + 10)}>Show {Math.min(10, backups.length - visible)} older records</button>}</div>}
    </>}
    <details className="maintenance-details"><summary><Info size={14} /><span>Backup scope and restore checks</span><CaretDown size={14} /></summary><p>Includes the Dispatch controller database and its matching vault key. Verification checks an isolated restore, schema compatibility, and saved credentials. It does not replace the running controller.</p><p>The analytics archive, external application databases, and application volumes are not included. Keep a protected copy outside this server and back up application data separately.</p><p>PostgreSQL verification requires compatible client tools and permission to create and drop a temporary database. A successful check describes that recorded observation.</p></details>
  </section>;
}
