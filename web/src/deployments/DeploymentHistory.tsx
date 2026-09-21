import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowRight, ArrowsLeftRight, CaretDown, CaretRight, CheckCircle, ClockCounterClockwise, CircleNotch, WarningCircle } from "@phosphor-icons/react";
import { api, Deployment, DeploymentComparison } from "../api";
import { relative, short } from "../presentation";
import { routePath, shouldHandleNavigation } from "../routes";

const versionLabel = (d: Deployment) => `${short(d.commitSha) || "No revision"} · ${new Date(d.createdAt).toLocaleString()} · ${d.id.slice(-6)}`;
const display = (v: unknown) => typeof v === "string" ? v : JSON.stringify(v);

type HistoryProps = { deployment: Deployment; onSelectDeployment?: (id: string) => void | Promise<void> };

// A repeat link is valid only across adjacent, loaded successful runs. Unknown
// evidence and failed runs remain separate even if a later link crosses them.
function groupHistory(items: Deployment[], repeats: Record<string, string>) {
 const groups: Deployment[][] = [];
 for (const item of items) {
  const group = groups[groups.length - 1];
  const newer = group?.[group.length - 1];
  if (newer?.state === "succeeded" && item.state === "succeeded" && repeats[item.id] === newer.id) group.push(item);
  else groups.push([item]);
 }
 return groups;
}

export function DeploymentHistory(props: HistoryProps) {
 // A different application gets fresh state; switching versions of the same
 // application retains its loaded history, scroll position, and field filter.
 return <ApplicationDeploymentHistory key={props.deployment.appId} {...props} />;
}

function ApplicationDeploymentHistory({ deployment, onSelectDeployment }: HistoryProps) {
 const [items, setItems] = useState<Deployment[]>([]);
 const [repeats, setRepeats] = useState<Record<string, string>>({});
 const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
 const [showAll, setShowAll] = useState(false);
 const [next, setNext] = useState("");
 const [loading, setLoading] = useState(true);
 const [error, setError] = useState("");
 const [from, setFrom] = useState("");
 const [to, setTo] = useState(deployment.id);
 const [comparison, setComparison] = useState<DeploymentComparison>();
 const [comparing, setComparing] = useState(false);
 const [compareError, setCompareError] = useState("");
 const [filter, setFilter] = useState("");
 const selectedVersion = useRef("");
 const manualComparison = useRef(false);
 const mounted = useRef(true);
 const paging = useRef(false);
 const openingVersion = useRef("");
 const [opening, setOpening] = useState("");
 const groups = useMemo(() => groupHistory(items, repeats), [items, repeats]);
 const repeatGroups = groups.filter(group => group.length > 1);
 const allExpanded = repeatGroups.length > 0 && repeatGroups.every(group => showAll || expanded.has(group[0].id));
 const viewedGroup = groups.find(group => group.slice(1).some(item => item.id === deployment.id))?.[0].id;

 useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

 function revealVersion(id: string) {
  const group = groups.find(group => group.slice(1).some(item => item.id === id));
  if (group) setExpanded(old => new Set(old).add(group[0].id));
 }
 function selectTo(id: string) {
  manualComparison.current = true;
  revealVersion(id); setTo(id);
  if (from === id) setFrom("");
 }
 function toggleGroup(id: string) {
  setExpanded(old => {
   const next = showAll ? new Set(repeatGroups.map(group => group[0].id)) : new Set(old);
   if (next.has(id)) next.delete(id); else next.add(id);
   return next;
  });
  setShowAll(false);
 }
 async function openVersion(id: string) {
  if (!onSelectDeployment || openingVersion.current) return;
  openingVersion.current = id;
  revealVersion(id);
  setOpening(id); setError("");
  try { await onSelectDeployment(id); }
  catch (cause) { if (mounted.current) setError(cause instanceof Error ? cause.message : String(cause)); }
  finally { if (mounted.current) { openingVersion.current = ""; setOpening(""); } }
 }

 useEffect(() => {
  let alive = true;
  void api.applicationHistory(deployment.appId).then(page => {
   if (!alive) return;
   setItems(page.items); setRepeats(page.repeats ?? {}); setNext(page.next ?? "");

  }).catch(e => { if (alive) setError(e.message); }).finally(() => { if (alive) setLoading(false); });
  return () => { alive = false; };
 }, [deployment.appId]);
 useEffect(() => {
  if (!items.length) return;
  if (selectedVersion.current !== deployment.id) {
   selectedVersion.current = deployment.id; manualComparison.current = false;
   setTo(deployment.id);
  }
  if (manualComparison.current) return;
  const groupIndex = groups.findIndex(group => group.some(item => item.id === deployment.id));
  const older = groupIndex >= 0 ? groups[groupIndex + 1]?.[0] : items.find(item => item.createdAt < deployment.createdAt);
  setFrom(older?.id ?? "");
 }, [deployment.id, deployment.createdAt, items, groups]);
 useEffect(() => {
  if (viewedGroup) setExpanded(old => new Set(old).add(viewedGroup));
 }, [deployment.id, viewedGroup]);
 useEffect(() => {
  let alive = true; setComparison(undefined); setCompareError("");
  if (!from || !to || from === to) { setComparing(false); return; }
  setComparing(true);
  void api.compareDeployments(to, from).then(result => { if (alive) setComparison(result); }).catch(e => { if (alive) setCompareError(e.message); }).finally(() => { if (alive) setComparing(false); });
  return () => { alive = false; };
 }, [from, to]);
 async function loadMore() {
  if (paging.current || loading) return;
  paging.current = true;
  setLoading(true); setError("");
  try {
   const page = await api.applicationHistory(deployment.appId, next);
   if (!mounted.current) return;
   setItems(old => [...old, ...page.items.filter(item => !old.some(existing => existing.id === item.id))]);
   setRepeats(old => ({ ...old, ...page.repeats })); setNext(page.next ?? "");
  }
  catch (e) { if (mounted.current) setError(e instanceof Error ? e.message : String(e)); }
  finally { if (mounted.current) { paging.current = false; setLoading(false); } }
 }
 const versions = items.some(item => item.id === deployment.id) ? items : [deployment, ...items];
 const changes = comparison?.changes.filter(change => change.path.toLowerCase().includes(filter.toLowerCase())) ?? [];
 function runRow(item: Deployment, repeated = false) {
  const Icon = item.state === "succeeded" ? CheckCircle : item.state === "failed" || item.state === "cancelled" ? WarningCircle : CircleNotch;
  return <div key={item.id} className={`history-run${item.id === to ? " selected" : ""}${repeated ? " history-repeat-run" : ""}`}>
   <Icon className={`history-state ${item.state}`} size={17} weight="fill" aria-hidden="true" />
   <div><a href={routePath({ view: "deployments", deploymentID: item.id, deploymentSection: "history" })} title={item.id} aria-current={item.id === deployment.id ? "page" : undefined} aria-busy={opening === item.id || undefined} onClick={event => {
    if (!onSelectDeployment || !shouldHandleNavigation(event)) return;
    event.preventDefault(); void openVersion(item.id);
   }}><code>{short(item.commitSha) || "No revision"}</code>{item.id === deployment.id && <> <small>Viewing</small></>}</a><span>{item.state} · <time dateTime={item.createdAt} title={new Date(item.createdAt).toLocaleString()}>{relative(item.createdAt)}</time>{item.id === from && <small className="history-compared">From</small>}{item.id === to && item.id !== deployment.id && <small className="history-compared">To</small>}</span></div>
   <button title="Compare with this deployment" aria-label={`Compare deployment ${item.id}`} onClick={() => selectTo(item.id)}><ArrowsLeftRight size={15} aria-hidden="true" /></button>
  </div>;
 }
 return <section className="deployment-history" aria-label="Deployment history">
  <header className="history-heading"><div><ClockCounterClockwise size={18} aria-hidden="true" /><h2>Deployment history</h2><span>{items.length}{next ? "+" : ""} runs</span></div>{repeatGroups.length > 0 ? <button className="quiet-button history-show-all" onClick={() => { setShowAll(!allExpanded); if (allExpanded) setExpanded(new Set()); }}>{allExpanded ? "Collapse repeats" : "Show all runs"}</button> : <p>Saved versions of this application</p>}</header>
  {repeatGroups.length > 0 && <p className="history-grouping-note">Grouped by the same recorded resources. Hooks and other effects can differ. Viewed and compared runs stay visible.</p>}
  {error && <p role="alert" className="error-message">{error}<button className="quiet-button" onClick={() => void loadMore()}>Retry</button></p>}
  <div className="history-layout"><div className="history-runs">
   {!items.length && loading && <p role="status">Loading deployments…</p>}
   {!items.length && !loading && !error && <p>No deployments recorded.</p>}
   {!items.some(item => item.id === deployment.id) && runRow(deployment)}
   {groups.map(group => {
    const head = group[0];
    if (group.length === 1) return runRow(head);
    const open = showAll || expanded.has(head.id);
    const count = group.length - 1;
    const listID = `history-repeats-${head.id}`;
    return <div className="history-repeat-group" key={head.id}>
     {runRow(head)}
     <button className="history-repeat-toggle" aria-expanded={open} aria-controls={listID} aria-label={`${open ? "Hide" : "Show"} ${count} repeat${count === 1 ? "" : "s"} for deployment ${head.id}`} onClick={() => toggleGroup(head.id)}>{open ? <CaretDown size={13} /> : <CaretRight size={13} />}<span><strong>{count} repeat{count === 1 ? "" : "s"}</strong><small>Same recorded resources</small></span></button>
     <div id={listID}>{group.slice(1).filter(item => open || item.id === deployment.id || item.id === from || item.id === to).map(item => runRow(item, true))}</div>
    </div>;
   })}
   {next && <button className="quiet-button history-more" disabled={loading} onClick={() => void loadMore()}>{loading ? "Loading…" : "Load older deployments"}</button>}
  </div><div className="history-comparison">
   <h3>Compare versions</h3>
   <div className="history-selectors"><label>From<select aria-label="Compare from deployment" value={from} onChange={event => { manualComparison.current = true; revealVersion(event.target.value); setFrom(event.target.value); }}><option value="">Select a version</option>{versions.map(item => <option key={item.id} value={item.id} disabled={item.id === to}>{versionLabel(item)}</option>)}</select></label><ArrowRight size={16} aria-hidden="true" /><label>To<select aria-label="Compare to deployment" value={to} onChange={event => selectTo(event.target.value)}>{versions.map(item => <option key={item.id} value={item.id}>{versionLabel(item)}</option>)}</select></label></div>
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
