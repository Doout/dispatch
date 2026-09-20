import { FormEvent, useEffect, useState } from "react";
import { ArrowClockwise, CheckCircle, Clock, WarningCircle } from "@phosphor-icons/react";
import { App, Overview } from "./api";
import { canManageProject } from "./permissions";
import { relative } from "./presentation";
import { ObservationConfig, ObservationInput, ObservationStatus, observationsClient } from "./observationsClient";

const date = (value?: string) => value ? new Date(value).toLocaleString() : "Not scheduled";
export function ObservationSettings({ application, overview, refreshToken = 0, onChecked, runtimeSupported = true }: { application: App; overview: Overview; refreshToken?: number; onChecked?: () => void; runtimeSupported?: boolean }) {
 const [status, setStatus] = useState<ObservationStatus>();
 const [error, setError] = useState("");
 const [editing, setEditing] = useState(false);
 const [busy, setBusy] = useState(false);
 const allowed = canManageProject(overview, application.projectId, "project.configure");
 useEffect(() => {
  let active = true;
  setStatus(undefined); setEditing(false);
  async function load() { try { const next = await observationsClient.get(application.id); if (active) { setStatus(next); setError(""); } } catch (e) { if (active) setError(e instanceof Error ? e.message : String(e)); } }
  void load(); const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 15000);
  return () => { active = false; window.clearInterval(timer); };
 }, [application.id, refreshToken]);
 async function check() { setBusy(true); setError(""); try { setStatus(await observationsClient.check(application.id)); window.dispatchEvent(new Event("dispatch-observation-updated")); onChecked?.(); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); } }
 async function save(input: ObservationInput) { setBusy(true); setError(""); try { setStatus(await observationsClient.save(application.id, input)); window.dispatchEvent(new Event("dispatch-observation-updated")); setEditing(false); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); } }
 const observation = status?.observation;
 const endpoint = observation?.endpoint;
 return <section className="observation-settings" aria-label="Observation settings">
  <header className="sync-toolbar"><div><h3>Checks and notifications</h3>{status && <span className="sync-observation">{status.freshness === "checking" ? "Checking…" : status.freshness === "stale" ? "Observation stale" : status.freshness === "fresh" ? `Observed ${relative(observation?.checkedAt ?? "")}` : "Not checked"}</span>}</div><div>{allowed && <><button className="quiet-button" disabled={busy || status?.checking} onClick={() => void check()}><ArrowClockwise size={14} />{busy ? "Working…" : runtimeSupported ? "Check runtime and endpoint" : "Check endpoint"}</button><button className="quiet-button" disabled={!status || busy} onClick={() => setEditing(!editing)}>{editing ? "Close settings" : "Configure checks"}</button></>}</div></header>
  {error && <p role="alert" className="error-message">{error}</p>}
  {status && <>
   <p className="sync-observation">{runtimeSupported ? "Successful deployments receive a fresh runtime check." : "Runtime drift and workload readiness checks are not supported for this deployment type. Configured public endpoints are checked after successful deployments."} {status.configuration.scheduled ? `Scheduled every ${status.configuration.intervalSeconds / 60} minutes. Next check: ${date(observation?.nextCheckAt)}.` : "Scheduled checks are off."} Observations become stale after {status.configuration.staleAfterSeconds / 60} minutes.</p>
   {status.configuration.endpointUrl && <div className="sync-notice">{endpoint?.state === "reachable" ? <CheckCircle size={16} weight="fill" /> : endpoint?.state === "not_configured" || !observation?.checkedAt ? <Clock size={16} /> : <WarningCircle size={16} />}<span><strong>Public endpoint</strong> · {endpoint?.state === "reachable" ? "Reachable" : !observation?.checkedAt ? "Not checked" : endpoint?.state.replaceAll("_", " ")}{endpoint?.httpStatus ? ` · HTTP ${endpoint.httpStatus}` : ""} · TLS: {endpoint?.tls.replaceAll("_", " ") || "Not checked"}<br />{endpoint?.message}{endpoint?.certificateExpiresAt && <> Certificate expires {date(endpoint.certificateExpiresAt)}.</>}<br /><small>Observed from Dispatch controller. This check is separate from workload readiness and application network access.</small></span></div>}
   {editing && allowed && <ObservationForm key={`${application.id}:${status.configuration.revision}`} config={status.configuration} busy={busy} onSave={input => void save(input)} onCancel={() => setEditing(false)} />}
   {!!status.events.length && <details className="sync-action-history"><summary>Observation and notification history <span>{status.events.length}</span></summary><ul>{status.events.map(event => <li key={event.id}><time dateTime={event.createdAt}>{date(event.createdAt)}</time> · {event.message} · {event.delivery === "disabled" ? "Delivery off" : event.delivery}{event.deliveryMessage && <> · {event.deliveryMessage}</>}{event.nextAttemptAt && <> · Retry {date(event.nextAttemptAt)}</>}</li>)}</ul></details>}
  </>}
 </section>;
}
function ObservationForm({ config, busy, onSave, onCancel }: { config: ObservationConfig; busy: boolean; onSave: (input: ObservationInput) => void; onCancel: () => void }) {
 const [scheduled, setScheduled] = useState(config.scheduled);
 const [interval, setInterval] = useState(config.intervalSeconds / 60);
 const [stale, setStale] = useState(config.staleAfterSeconds / 60);
 const [endpoint, setEndpoint] = useState(config.endpointUrl ?? "");
 const [notify, setNotify] = useState(config.notificationsEnabled);
 const [webhook, setWebhook] = useState("");
 const [remove, setRemove] = useState(false);
 const [mute, setMute] = useState(config.mutedUntil ? new Date(new Date(config.mutedUntil).getTime() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16) : "");
 function submit(e: FormEvent) { e.preventDefault(); onSave({ revision: config.revision, scheduled, intervalSeconds: interval * 60, staleAfterSeconds: stale * 60, endpointUrl: endpoint.trim(), notificationsEnabled: notify, ...(webhook ? { webhookUrl: webhook.trim() } : {}), removeWebhook: remove, ...(mute ? { mutedUntil: new Date(mute).toISOString() } : {}) }); }
 return <form className="service-form" onSubmit={submit}>
  <label><input type="checkbox" checked={scheduled} onChange={e => setScheduled(e.target.checked)} /> Enable scheduled observations</label>
  <div className="service-form-grid"><label>Interval in minutes<input type="number" min={1} max={1440} required value={interval} onChange={e => setInterval(Number(e.target.value))} /></label><label>Stale after minutes<input type="number" min={interval} max={10080} required value={stale} onChange={e => setStale(Number(e.target.value))} /></label></div>
  <label>Public endpoint URL<input type="url" value={endpoint} onChange={e => setEndpoint(e.target.value)} placeholder="https://app.example.com/health" /><span>Public HTTP or HTTPS, without credentials or query strings. Redirects are not followed.</span></label>
  <label><input type="checkbox" checked={notify} onChange={e => setNotify(e.target.checked)} /> Deliver state changes, deployment failures and recovery to a webhook</label>
  <label>{config.webhookConfigured ? "Replace notification webhook" : "Notification webhook"}<input type="password" autoComplete="new-password" value={webhook} disabled={remove} onChange={e => setWebhook(e.target.value)} placeholder={config.webhookConfigured ? "Configured. Leave blank to retain." : "https://…"} /><span>The address is encrypted and write-only. Saving does not send a test message.</span></label>
  {config.webhookConfigured && <label><input type="checkbox" checked={remove} onChange={e => { setRemove(e.target.checked); if (e.target.checked) { setWebhook(""); setNotify(false); } }} /> Remove the configured webhook</label>}
  <label>Mute notifications until<input type="datetime-local" value={mute} onChange={e => setMute(e.target.value)} /><span>Leave blank to unmute. Maximum 30 days.</span></label>
  <p className="sync-observation">Failed observations use backoff up to 24 hours. Failed webhook deliveries retry up to five times. Unchanged states do not send repeated notifications.</p>
  <div className="service-actions"><button className="primary-button" disabled={busy}>{busy ? "Saving…" : "Save observation settings"}</button><button type="button" className="quiet-button" disabled={busy} onClick={onCancel}>Cancel</button></div>
 </form>;
}
