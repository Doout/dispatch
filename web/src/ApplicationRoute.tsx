import { useEffect, useState } from "react";
import { api, type ApplicationRoute as Route } from "./api";

export function ApplicationRoutePanel({ appID, canConfigure }: { appID: string; canConfigure: boolean }) {
  const [route, setRoute] = useState<Route | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    setRoute(null); setError("");
    const refresh = () => { void api.applicationRoute(appID).then(value => { if (active) { setRoute(value); setError(""); } }).catch(cause => { if (active) setError((cause as Error).message); }); };
    refresh(); const timer = window.setInterval(refresh, 15000);
    return () => { active = false; window.clearInterval(timer); };
  }, [appID]);
  async function check() {
    setBusy(true); setError("");
    try { setRoute(await api.checkApplicationRoute(appID)); } catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  }
  if (!route) return error ? <p className="form-error" role="status">Route status unavailable: {error}</p> : null;
  const live = route.state === "active";
  return <section className="application-route" aria-label="Public application route">
    <header><h3>Public route</h3><span className={`status-label ${live ? "live" : "pending"}`}>{live ? "Live" : "Not live"}</span></header>
    <p>{route.message}</p>
    <dl><div><dt>Hostname</dt><dd>{live ? <a href={`${route.requireTls ? "https" : "http"}://${route.hostname}`} target="_blank" rel="noreferrer">{route.hostname}</a> : route.hostname}</dd></div>
      <div><dt>DNS</dt><dd>{route.dns}</dd></div><div><dt>Certificate</dt><dd>{route.requireTls ? route.certificate.state.replaceAll("_", " ") : "HTTP only"}</dd></div>
      <div><dt>Active deployment</dt><dd><code>{route.deploymentId || "No candidate published"}</code></dd></div>
      <div><dt>Target backend</dt><dd><code>{route.destination || "Placeholder until readiness passes"}</code></dd></div>
      {route.previousDeploymentId && <div><dt>Previous deployment</dt><dd><code>{route.previousDeploymentId}</code></dd></div>}
    </dl>
    {route.requireTls && <p>{route.certificate.message}{route.certificate.expiresAt ? ` Expires ${new Date(route.certificate.expiresAt).toLocaleString()}.` : ""}</p>}
    <small>Workload readiness and public reachability are checked separately. Public evidence refreshes every minute.</small>
    {canConfigure && <button type="button" className="quiet-button" disabled={busy} onClick={() => void check()}>{busy ? "Checking public endpoint…" : "Check DNS and certificate"}</button>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </section>;
}
