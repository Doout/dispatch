import { useEffect, useState } from "react";
import { ArrowRight, ArrowsLeftRight, CheckCircle, ClockCounterClockwise, CircleNotch, WarningCircle } from "@phosphor-icons/react";
import { api, Deployment, DeploymentComparison } from "../api";
import { relative, short } from "../presentation";
import { routePath } from "../routes";

const versionLabel = (d: Deployment) => `${short(d.commitSha) || "No revision"} · ${new Date(d.createdAt).toLocaleString()} · ${d.id.slice(-6)}`;
const display = (v: unknown) => typeof v === "string" ? v : JSON.stringify(v);

export function DeploymentHistory({ deployment }: { deployment: Deployment }) {
 const [items, setItems] = useState<Deployment[]>([]);
 const [next, setNext] = useState("");
 const [loading, setLoading] = useState(true);
 const [error, setError] = useState("");
 const [from, setFrom] = useState("");
 const [to, setTo] = useState(deployment.id);
 const [comparison, setComparison] = useState<DeploymentComparison>();
 const [comparing, setComparing] = useState(false);
 const [compareError, setCompareError] = useState("");
 const [filter, setFilter] = useState("");
 useEffect(() => {
  let alive = true;
  void api.applicationHistory(deployment.appId).then(page => {
   if (!alive) return;
   setItems(page.items); setNext(page.next ?? "");
   const older = page.items.find(item => item.id !== deployment.id && item.createdAt <= deployment.createdAt);
   setFrom(older?.id ?? "");
  }).catch(e => { if (alive) setError(e.message); }).finally(() => { if (alive) setLoading(false); });
  return () => { alive = false; };
 }, [deployment.appId, deployment.id, deployment.createdAt]);
 useEffect(() => {
  let alive = true; setComparison(undefined); setCompareError("");
  if (!from || !to || from === to) { setComparing(false); return; }
  setComparing(true);
  void api.compareDeployments(to, from).then(result => { if (alive) setComparison(result); }).catch(e => { if (alive) setCompareError(e.message); }).finally(() => { if (alive) setComparing(false); });
  return () => { alive = false; };
 }, [from, to]);
 async function loadMore() {
  setLoading(true); setError("");
  try { const page = await api.applicationHistory(deployment.appId, next); setItems(old => [...old, ...page.items.filter(item => !old.some(existing => existing.id === item.id))]); setNext(page.next ?? ""); }
  catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  finally { setLoading(false); }
 }
 const versions = items.some(item => item.id === deployment.id) ? items : [deployment, ...items];
 const changes = comparison?.changes.filter(change => change.path.toLowerCase().includes(filter.toLowerCase())) ?? [];
 return <section className="deployment-history" aria-label="Deployment history">
  <header className="history-heading"><div><ClockCounterClockwise size={18} aria-hidden="true" /><h2>Deployment history</h2><span>{items.length}{next ? "+" : ""} runs</span></div><p>Saved versions of this application</p></header>
  {error && <p role="alert" className="error-message">{error}<button className="quiet-button" onClick={() => void loadMore()}>Retry</button></p>}
  <div className="history-layout"><div className="history-runs">
   {!items.length && loading && <p role="status">Loading deployments…</p>}
   {!items.length && !loading && !error && <p>No deployments recorded.</p>}
   {items.map(item => {
    const Icon = item.state === "succeeded" ? CheckCircle : item.state === "failed" || item.state === "cancelled" ? WarningCircle : CircleNotch;
    return <div key={item.id} className={`history-run${item.id === to ? " selected" : ""}`}>
     <Icon className={`history-state ${item.state}`} size={17} weight="fill" aria-hidden="true" />
     <div><a href={routePath({ view: "deployments", deploymentID: item.id, deploymentSection: "history" })} title={item.id}><code>{short(item.commitSha) || "No revision"}</code>{item.id === deployment.id && <small>Viewing</small>}</a><span>{item.state} · <time dateTime={item.createdAt} title={new Date(item.createdAt).toLocaleString()}>{relative(item.createdAt)}</time></span></div>
     <button title="Compare with this deployment" aria-label={`Compare deployment ${item.id}`} onClick={() => { setTo(item.id); if (from === item.id) setFrom(""); }}><ArrowsLeftRight size={15} aria-hidden="true" /></button>
    </div>;
   })}
   {next && <button className="quiet-button history-more" disabled={loading} onClick={() => void loadMore()}>{loading ? "Loading…" : "Load older deployments"}</button>}
  </div><div className="history-comparison">
   <h3>Compare versions</h3>
   <div className="history-selectors"><label>From<select aria-label="Compare from deployment" value={from} onChange={event => setFrom(event.target.value)}><option value="">Select a version</option>{versions.map(item => <option key={item.id} value={item.id} disabled={item.id === to}>{versionLabel(item)}</option>)}</select></label><ArrowRight size={16} aria-hidden="true" /><label>To<select aria-label="Compare to deployment" value={to} onChange={event => { setTo(event.target.value); if (event.target.value === from) setFrom(""); }}>{versions.map(item => <option key={item.id} value={item.id}>{versionLabel(item)}</option>)}</select></label></div>
   {compareError && <p role="alert" className="error-message">{compareError}</p>}
   {comparing && <p role="status" className="history-empty">Comparing saved inputs…</p>}
   {!from && !comparing && <p className="history-empty">Select two deployments to compare their saved values, release settings, and service revisions. Load older deployments to find more versions.</p>}
   {comparison && <>
    <div className="history-comparison-meta"><span>{comparison.available ? `${comparison.changes.length}${comparison.truncated ? "+" : ""} changes` : "Comparison unavailable"}</span>{comparison.hidden > 0 && <span>Sensitive fields excluded</span>}</div>
    <p className="history-note">{comparison.message}</p>
    {comparison.available && comparison.changes.length > 0 && <><input className="history-filter" aria-label="Filter changed fields" placeholder="Filter changed fields…" value={filter} onChange={event => setFilter(event.target.value)} />
     <div className="history-diff"><table><thead><tr><th>Field</th><th>Before</th><th>After</th></tr></thead><tbody>{changes.map(change => <tr key={change.path}><td data-label="Field"><span className={`change-kind ${change.kind}`}>{change.kind}</span><code>{change.path}</code></td><td data-label="Before" className="change-before"><code>{change.kind === "added" ? "Not present" : display(change.before)}</code></td><td data-label="After" className="change-after"><code>{change.kind === "removed" ? "Not present" : display(change.after)}</code></td></tr>)}</tbody></table></div>
     {!changes.length && <p className="history-empty">No fields match this filter.</p>}
    </>}
    {comparison.available && !comparison.changes.length && <p className="history-empty">No visible input changes between these deployments.</p>}
    {comparison.truncated && <p className="history-note">Showing the first 1,000 changed fields.</p>}
   </>}
  </div></div>
 </section>;
}
