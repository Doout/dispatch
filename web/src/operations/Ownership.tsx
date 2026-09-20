import { useEffect, useRef, useState } from "react";
import { ArrowClockwise, ArrowRight, CheckCircle, User, UsersThree, WarningCircle } from "@phosphor-icons/react";
import { type Overview, request } from "../api";
import { type AppRoute, type OperationsFilters, routePath, shouldHandleNavigation } from "../routes";
import { canManageProject } from "../permissions";
import { type OwnershipItem, type OwnershipPage } from "./client";
import "./workflows.css";

const message = (cause: unknown) => cause instanceof Error ? cause.message : String(cause);
type Props = { overview: Overview; filters: OperationsFilters; onFilters: (filters: OperationsFilters) => void; onChanged?: () => void | Promise<void>; onNavigate?: (route: AppRoute) => void };
export function Ownership({ overview, filters, onFilters, onChanged, onNavigate }: Props) {
  const [query, setQuery] = useState(filters.query ?? "");
  const [items, setItems] = useState<OwnershipItem[]>([]);
  const [next, setNext] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [refresh, setRefresh] = useState(0);
  const generation = useRef(0); const busy = useRef(false);
  const params = new URLSearchParams();
  if (filters.projectId) params.set("projectId", filters.projectId);
  if (filters.query) params.set("q", filters.query);
  if (filters.unassigned != null) params.set("unassigned", String(filters.unassigned));
  const scope = params.toString();
  const authorization = JSON.stringify([overview.identity?.id, overview.identity?.systemRole, overview.projectPermissions]);
  const requestScope = JSON.stringify([scope, authorization]);
  const currentScope = useRef(requestScope); currentScope.current = requestScope;
  useEffect(() => setQuery(filters.query ?? ""), [filters.query]);
  useEffect(() => {
    const requestID = ++generation.current; busy.current = true; setLoading(true); setItems([]); setNext(""); setError("");
    request<OwnershipPage>(`/api/v1/operations/ownership?${scope}`).then(page => { if (generation.current === requestID && currentScope.current === requestScope) { setItems(page.items); setNext(page.next ?? ""); } }).catch(cause => { if (generation.current === requestID && currentScope.current === requestScope) setError(message(cause)); }).finally(() => { if (generation.current === requestID) { busy.current = false; setLoading(false); } });
    return () => { if (generation.current === requestID) generation.current++; };
  }, [scope, requestScope, refresh]);
  useEffect(() => setNotice(""), [requestScope]);
  async function more() {
    if (busy.current || !next) return;
    const requestID = generation.current; busy.current = true; setLoading(true); setError("");
    try { const query = new URLSearchParams(scope); query.set("before", next); const page = await request<OwnershipPage>(`/api/v1/operations/ownership?${query}`); if (generation.current === requestID && currentScope.current === requestScope) { setItems(old => [...old, ...page.items.filter(item => !old.some(row => row.appId === item.appId))]); setNext(page.next ?? ""); } }
    catch (cause) { if (generation.current === requestID && currentScope.current === requestScope) setError(message(cause)); }
    finally { if (generation.current === requestID) { busy.current = false; setLoading(false); } }
  }
  const visibleItems = items.filter(item => canManageProject(overview, item.projectId, "project.view"));
  function change(patch: Partial<OperationsFilters>) { onFilters({ ...filters, ...patch, appId: undefined }); }
  return <section className="ops-task-panel operations-ownership">
    <header className="ops-task-heading"><div><h2>Application ownership</h2><p>Find the person or team responsible for each application. Ownership does not grant access.</p></div><button className="quiet-button" disabled={loading} onClick={() => setRefresh(value => value + 1)}><ArrowClockwise size={14} />Refresh</button></header>
    <form className="ops-task-toolbar" onSubmit={event => { event.preventDefault(); change({ query: query.trim() || undefined }); }}><label className="ops-task-search">Search ownership<input aria-label="Search ownership" value={query} maxLength={200} onChange={event => setQuery(event.target.value)} placeholder="Application or owner…" /></label><label>Assignment<select aria-label="Ownership assignment" value={filters.unassigned == null ? "all" : filters.unassigned ? "unassigned" : "assigned"} onChange={event => change({ unassigned: event.target.value === "all" ? undefined : event.target.value === "unassigned" })}><option value="all">All applications</option><option value="unassigned">Unassigned</option><option value="assigned">Assigned</option></select></label><button className="quiet-button" type="submit">Search</button></form>
    {error && <div className="ops-task-error" role="alert"><WarningCircle size={16} /><span>{error}</span><button className="quiet-button" onClick={() => items.length && next ? void more() : setRefresh(value => value + 1)}>Try again</button></div>}
    {notice && <p className="ops-task-success" role="status"><CheckCircle size={15} />{notice}</p>}
    {loading && !visibleItems.length && <p className="ops-task-empty" role="status">Loading application ownership…</p>}
    {!loading && !visibleItems.length && !error && <div className="ops-task-empty"><UsersThree size={22} /><strong>No applications match these filters</strong><p>Try another project or assignment filter.</p></div>}
    {!!visibleItems.length && <ul className="ops-owner-list">{visibleItems.map(item => {
      const canEdit = canManageProject(overview, item.projectId, "project.configure");
      const selected = canEdit && filters.appId === item.appId;
      const route: AppRoute = { view: "deployments", deploymentFilters: { layout: "list", app: item.appId, project: item.projectId } };
      const owner = item.owner; const Icon = owner?.principalType === "team" ? UsersThree : User;
      return <li key={item.appId} className={selected ? "selected" : ""}><div className={`ops-owner-row${canEdit ? "" : " readonly"}`}><div className="ops-owner-app"><strong>{item.appName}</strong><span>{overview.projects.find(project => project.id === item.projectId)?.name || "Project"}</span><a className="ops-task-link" href={routePath(route)} onClick={event => { if (onNavigate && shouldHandleNavigation(event)) { event.preventDefault(); onNavigate(route); } }}>Deployments<ArrowRight size={12} /></a></div><div className={`ops-owner-assignment ${owner ? "" : "unassigned"}`}><Icon size={17} /><div><strong>{owner ? owner.displayName || "Owner unavailable" : "Unassigned"}</strong><span>{owner ? owner.displayName ? owner.principalType === "team" ? "Team" : "Person" : `Recorded ${owner.principalType} · ${owner.principalId}` : "No owner recorded"}</span></div></div>{canEdit && <button className="quiet-button" aria-label={`${selected ? "Close owner editor" : owner ? "Change owner" : "Assign owner"} for ${item.appName}`} onClick={() => onFilters({ ...filters, appId: selected ? undefined : item.appId })}>{selected ? "Close" : owner ? "Change" : "Assign owner"}</button>}</div>{selected && <OwnerEditor key={`${item.appId}:${authorization}`} item={item} onCancel={() => onFilters({ ...filters, appId: undefined })} onSaved={async () => { if (currentScope.current !== requestScope) return; onFilters({ ...filters, appId: undefined }); setRefresh(value => value + 1); setNotice(`Owner updated for ${item.appName}.`); try { await onChanged?.(); } catch { setNotice(`Owner updated for ${item.appName}. Refresh the overview to update its summary.`); } }} />}</li>;
    })}</ul>}
    {next && <div className="ops-task-footer"><button className="quiet-button" disabled={loading} onClick={() => void more()}>{loading ? "Loading…" : "Load more applications"}</button><span>{visibleItems.length} applications shown</span></div>}
  </section>;
}

type Candidates = { users: { id: string; username: string; displayName: string }[]; teams: { id: string; name: string }[] };
function OwnerEditor({ item, onSaved, onCancel }: { item: OwnershipItem; onSaved: () => void | Promise<void>; onCancel: () => void }) {
  const [candidates, setCandidates] = useState<Candidates>();
  const [kind, setKind] = useState(item.owner?.principalType === "team" ? "team" : "user");
  const [principal, setPrincipal] = useState(item.owner?.principalId ?? "");
  const [error, setError] = useState(""); const [saving, setSaving] = useState(false); const [retry, setRetry] = useState(0);
  const alive = useRef(true); const savingRef = useRef(false);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => { let current = true; setCandidates(undefined); setError(""); request<Candidates>(`/api/v1/projects/${encodeURIComponent(item.projectId)}/owner-candidates`).then(value => { if (current) setCandidates(value); }).catch(cause => { if (current) setError(message(cause)); }); return () => { current = false; }; }, [item.projectId, retry]);
  const choices = kind === "team" ? candidates?.teams.map(team => ({ id: team.id, name: team.name })) : candidates?.users.map(user => ({ id: user.id, name: user.displayName || user.username }));
  const missing = principal && candidates && !choices?.some(choice => choice.id === principal);
  const changed = principal !== (item.owner?.principalId ?? "") || (principal && kind !== item.owner?.principalType);
  async function save() {
    if (savingRef.current || !candidates || !changed || missing) return;
    savingRef.current = true; setSaving(true); setError("");
    try { await request(`/api/v1/apps/${encodeURIComponent(item.appId)}/owner`, { method: "PUT", body: JSON.stringify({ principalType: kind, principalId: principal }) }); if (alive.current) await onSaved(); }
    catch (cause) { if (alive.current) setError(message(cause)); }
    finally { savingRef.current = false; if (alive.current) setSaving(false); }
  }
  return <form className="ops-owner-editor" aria-label={`Owner for ${item.appName}`} onSubmit={event => { event.preventDefault(); void save(); }}><p>Choose an existing person or team eligible for this project. Clearing the owner removes responsibility metadata only.</p>{error && <div className="ops-task-error" role="alert"><span>{error}</span>{!candidates && <button type="button" className="quiet-button" onClick={() => setRetry(value => value + 1)}>Retry candidates</button>}</div>}{!candidates && !error ? <p role="status">Loading eligible owners…</p> : <div className="ops-owner-fields"><label>Owner type<select aria-label="Owner type" disabled={saving} value={kind} onChange={event => { setKind(event.target.value); setPrincipal(""); }}><option value="user">Person</option><option value="team">Team</option></select></label><label>Owner<select aria-label="Owner" disabled={saving || !candidates} value={principal} onChange={event => setPrincipal(event.target.value)}><option value="">Unassigned</option>{missing && <option value={principal} disabled>Current owner is no longer eligible</option>}{choices?.map(choice => <option key={choice.id} value={choice.id}>{choice.name}</option>)}</select></label><button className="primary-button" disabled={saving || !candidates || !changed || !!missing}>{saving ? "Saving…" : principal ? "Save owner" : "Clear owner"}</button><button type="button" className="quiet-button" disabled={saving} onClick={onCancel}>Cancel</button></div>}</form>;
}
