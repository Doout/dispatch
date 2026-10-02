import { useEffect, useRef, useState } from "react";
import { Archive, ArrowClockwise, CheckCircle, ShieldCheck, Trash, WarningCircle } from "@phosphor-icons/react";
import { request, type Overview } from "../api";
import "./Maintenance.css";

export type RetentionPolicy = { projectId: string; logDays: number; runDays: number; keepRuns: number; imageDays?: number; stoppedRevisionDays?: number; keepRollbackRevisions?: number };
export type RuntimeRetentionItem = { key: string; kind: "revision" | "image"; serverId: string; appId: string; deploymentId?: string; name: string; identity: string; createdAt: string; protected: string[] };
export type RuntimeRetentionReview = { id: string; projectId: string; digest: string; supersededBy?: string; policy: RetentionPolicy; state: "planned" | "running" | "partial" | "succeeded" | "blocked"; createdAt: string; expiresAt: string; items: RuntimeRetentionItem[]; results: { key: string; state: "removed" | "absent" | "protected" | "failed" | "pending"; message: string }[] };
type RuntimeResponse = { applied: boolean; runtime: RuntimeRetentionReview };
const failure = (value: unknown) => value instanceof Error ? value.message : String(value);
export const sameRetentionPolicy = (left: RetentionPolicy, right: RetentionPolicy) => left.projectId === right.projectId && left.logDays === right.logDays && left.runDays === right.runDays && left.keepRuns === right.keepRuns && (left.imageDays || 0) === (right.imageDays || 0) && (left.stoppedRevisionDays || 0) === (right.stoppedRevisionDays || 0) && (left.keepRollbackRevisions || 5) === (right.keepRollbackRevisions || 5);
const draftFor = (policy: RetentionPolicy) => ({ imageDays: String(policy.imageDays || 0), stoppedRevisionDays: String(policy.stoppedRevisionDays || 0), keepRollbackRevisions: String(policy.keepRollbackRevisions || 5) });
const retentionAge = (days?: number) => days ? `${days} days` : "Keep indefinitely";

export function RuntimeRetentionPanel({ policy, overview, busyElsewhere = false, onPolicyChanged, onChanged, onConflict }: { policy: RetentionPolicy; overview: Overview; busyElsewhere?: boolean; onPolicyChanged: (policy: RetentionPolicy) => void; onChanged?: () => void; onConflict?: () => void }) {
  const [draft, setDraft] = useState(() => draftFor(policy));
  const [editing, setEditing] = useState(false);
  const [review, setReview] = useState<RuntimeRetentionReview>();
  const [resumeId, setResumeId] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const alive = useRef(true);
  const locked = useRef(false);
  const latestPolicy = useRef(policy);
  latestPolicy.current = policy;
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => { setDraft(draftFor(policy)); setReview(undefined); setConfirmed(false); setEditing(false); }, [policy]);
  const base = `/api/v1/projects/${encodeURIComponent(policy.projectId)}/retention`;
  const projectName = overview.projects.find(item => item.id === policy.projectId)?.name || policy.projectId;
  const disabled = Boolean(busy) || busyElsewhere;
  const valid = Object.entries(draft).every(([key, value]) => /^\d+$/.test(value) && Number.isSafeInteger(Number(value)) && Number(value) >= (key === "keepRollbackRevisions" ? 2 : 0) && Number(value) <= (key === "keepRollbackRevisions" ? 10000 : 36500));
  const eligible = review?.items.filter(item => item.protected.length === 0) || [];
  const stale = Boolean(review && !sameRetentionPolicy(policy, review.policy));
  const results = new Map(review?.results.map(item => [item.key, item]));
  const unfinished = eligible.some(item => !["removed", "absent"].includes(results.get(item.key)?.state || "pending"));
  const canApply = Boolean(review && !review.supersededBy && !stale && unfinished && review.state !== "succeeded" && review.state !== "running");
  async function act(label: string, work: () => Promise<void>) {
    if (locked.current || busyElsewhere) return;
    locked.current = true; setBusy(label); setError(""); setNotice("");
    try { await work(); }
    catch (cause) { if (alive.current) { setError(failure(cause)); setConfirmed(false); if (cause instanceof Error && (cause as Error & { status?: number }).status === 409) onConflict?.(); } }
    finally { if (alive.current) { locked.current = false; setBusy(""); } }
  }
  function receive(value: RuntimeRetentionReview, captured: RetentionPolicy) {
    if (!alive.current) return;
    if (!value || value.projectId !== captured.projectId || !value.id || !value.digest || !Array.isArray(value.items) || !Array.isArray(value.results)) throw new Error("The cleanup receipt did not match this project. Refresh before continuing.");
    if (!sameRetentionPolicy(latestPolicy.current, captured)) throw new Error("The policy changed during inspection. Preview the current policy again.");
    setReview(value); setResumeId(value.id); setConfirmed(false);
  }
  function refresh() {
    if (!review) return;
    const captured = { ...policy }, id = review.id;
    void act("Refreshing cleanup receipt", async () => receive(await request<RuntimeRetentionReview>(`${base}/runtime-reviews/${encodeURIComponent(id)}`), captured));
  }
  return <section className="maintenance-panel runtime-retention" aria-label="Runtime artifact retention">
    <header className="maintenance-heading"><div><h2>Runtime artifact retention</h2><p>{projectName} · Review images and stopped revisions before removal.</p></div><button type="button" className="quiet-button" disabled={disabled} onClick={() => { setEditing(value => !value); setDraft(draftFor(policy)); setConfirmed(false); }}> {editing ? "Close runtime policy" : "Edit runtime policy"}</button></header>
    {error && <p className="maintenance-feedback danger" role="alert"><WarningCircle size={16} />{error}</p>}
    {notice && <p className="maintenance-feedback success" role="status"><CheckCircle size={16} />{notice}</p>}
    {busy && <p className="maintenance-loading" role="status">{busy}…</p>}
    {editing ? <form className="retention-policy-editor" onSubmit={event => { event.preventDefault(); if (!valid) return; const next = { ...policy, imageDays: Number(draft.imageDays), stoppedRevisionDays: Number(draft.stoppedRevisionDays), keepRollbackRevisions: Number(draft.keepRollbackRevisions) }; void act("Saving runtime policy", async () => { const saved = await request<RetentionPolicy>(base, { method: "PUT", body: JSON.stringify(next) }); if (!alive.current) return; if (saved.projectId !== policy.projectId) throw new Error("The saved policy did not match this project."); onPolicyChanged(saved); setEditing(false); setNotice("Runtime policy saved. Preview its candidates before removing anything."); onChanged?.(); }); }}>
      <div className="retention-fields">{([["imageDays", "Retain unused images for", "days, 0 keeps indefinitely"], ["stoppedRevisionDays", "Retain stopped revisions for", "days, 0 keeps indefinitely"], ["keepRollbackRevisions", "Protect recent rollback revisions", "per application, at least 2"]] as const).map(([key, label, help]) => <label key={key}>{label}<div><input aria-label={label} type="number" step={1} min={key === "keepRollbackRevisions" ? 2 : 0} max={key === "keepRollbackRevisions" ? 10000 : 36500} value={draft[key]} disabled={disabled} onChange={event => setDraft(value => ({ ...value, [key]: event.target.value }))} /><span>{help}</span></div></label>)}</div>
      <footer><small>Saving changes policy only. Volumes and backups use their own protection rules.</small><button type="submit" className="primary-button" disabled={disabled || !valid}>Save runtime policy</button></footer>
    </form> : <dl className="retention-policy-summary"><div><dt>Unused images</dt><dd>{retentionAge(policy.imageDays)}</dd></div><div><dt>Stopped revisions</dt><dd>{retentionAge(policy.stoppedRevisionDays)}</dd></div><div><dt>Protected rollback revisions</dt><dd>{policy.keepRollbackRevisions || 5}<span>per application</span></dd></div></dl>}
    <div className="retention-preview-action"><span><ShieldCheck size={16} />Active releases, shared images and protected rollback inputs stay retained.</span><button type="button" className="quiet-button" disabled={disabled || editing} onClick={() => { const captured = { ...policy }; setReview(undefined); setConfirmed(false); void act("Inspecting runtime artifacts", async () => { const value = await request<RuntimeResponse>(`${base}/preview`, { method: "POST", body: JSON.stringify({ scope: "runtime", expectedPolicy: captured }) }); receive(value.runtime, captured); }); }}><Archive size={14} />Preview runtime cleanup</button></div>
    <details className="maintenance-details"><summary>Reopen a cleanup receipt</summary><form className="runtime-receipt-form" onSubmit={event => { event.preventDefault(); if (!/^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,255}$/.test(resumeId)) { setError("Enter a valid cleanup receipt ID."); return; } const captured = { ...policy }; void act("Loading cleanup receipt", async () => receive(await request<RuntimeRetentionReview>(`${base}/runtime-reviews/${encodeURIComponent(resumeId)}`), captured)); }}><label>Cleanup receipt ID<input aria-label="Cleanup receipt ID" value={resumeId} disabled={disabled} onChange={event => setResumeId(event.target.value)} /></label><button className="quiet-button" type="submit" disabled={disabled || !resumeId}>Load receipt</button></form></details>
    {review && <div className="retention-preview" aria-label="Runtime cleanup review"><div className="maintenance-subheading"><h3>{review.state === "succeeded" ? "Reviewed cleanup completed" : review.state === "partial" ? "Cleanup needs attention" : "Reviewed runtime artifacts"}</h3><button type="button" className="quiet-button" disabled={disabled} onClick={refresh}><ArrowClockwise size={14} />Refresh receipt</button></div>
      <p className="maintenance-scope-note">{eligible.length} eligible · {review.items.length - eligible.length} protected · State: {review.state}. Each review contains at most 50 deletion candidates.</p>
      <code className="runtime-receipt-id">{review.id}</code>
      {stale && <p className="maintenance-feedback warning" role="status">This receipt uses an older policy. Its evidence remains available; preview the current policy before further removal.</p>}
      {review.items.length === 0 ? <p className="maintenance-scope-note">No runtime artifacts were found for this review.</p> : <ul className="runtime-retention-items">{review.items.map(item => { const result = results.get(item.key); const protectedItem = item.protected.length > 0; const target = overview.servers?.find(target => target.id === item.serverId)?.name || item.serverId; const app = overview.apps?.find(app => app.id === item.appId)?.name || item.appId; return <li key={item.key}><div className="runtime-artifact-title">{protectedItem ? <ShieldCheck size={16} /> : <Archive size={16} />}<strong>{item.kind === "image" ? "Image" : "Runtime revision"}</strong><span className={`backup-state ${protectedItem ? "success" : result?.state === "failed" ? "danger" : "muted"}`}>{protectedItem ? "Protected" : result?.state || "Eligible"}</span></div><div className="runtime-artifact-detail"><span>{target} · {app}</span><code>{item.name}</code>{item.protected.map(reason => <p key={reason}>{reason}</p>)}{result?.message && <p>{result.message}</p>}<details><summary>Resource identity</summary><code>{item.identity}</code>{item.deploymentId && <code>Deployment {item.deploymentId}</code>}</details></div></li>; })}</ul>}
      {canApply && <div className="retention-confirmation"><h3>{review.state === "partial" ? "Retry this reviewed cleanup?" : "Remove these runtime artifacts?"}</h3><p>Only eligible items in this saved review can be removed. Stopped container files and removed images may no longer be available for recovery. Volumes, networks and backups are excluded. Current references and policy are checked again before deletion.</p><label><input type="checkbox" checked={confirmed} disabled={disabled} onChange={event => setConfirmed(event.target.checked)} /><span>I approve removal of the eligible runtime artifacts shown for {projectName}.</span></label><div className="maintenance-actions"><button type="button" className="danger-button" disabled={disabled || !confirmed} onClick={() => { if (!confirmed || !canApply) return; if (review.state === "planned" && (!Number.isFinite(Date.parse(review.expiresAt)) || Date.parse(review.expiresAt) <= Date.now())) { setError("This cleanup review expired. Preview the current candidates again."); setConfirmed(false); return; } const captured = review; void act("Applying reviewed runtime cleanup", async () => { const value = await request<RuntimeResponse>(`${base}/apply`, { method: "POST", body: JSON.stringify({ scope: "runtime", confirm: policy.projectId, expectedPolicy: captured.policy, runtimeReviewId: captured.id, runtimeReviewDigest: captured.digest }) }); if (!value.applied) throw new Error("Runtime cleanup was not accepted. Refresh its receipt before retrying."); receive(value.runtime, captured.policy); onChanged?.(); }); }}><Trash size={14} />{review.state === "partial" ? "Retry reviewed items" : "Remove reviewed artifacts"}</button></div></div>}
      {review.state === "running" && <p className="maintenance-feedback" role="status">Cleanup is running. Refresh this receipt to inspect its outcome.</p>}
      {review.supersededBy && <p className="maintenance-feedback warning" role="status">This receipt was superseded by cleanup {review.supersededBy}. Reopen that receipt to continue.</p>}
      {review.state === "blocked" && !review.supersededBy && <p className="maintenance-feedback warning" role="status">Cleanup is blocked. Inspect the recorded reasons, resolve the cause and retry the remaining reviewed items.</p>}
    </div>}
  </section>;
}
