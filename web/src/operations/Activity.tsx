import { useEffect, useRef, useState } from "react";
import { Archive, ArrowClockwise, ArrowRight, CaretDown, CheckCircle, Database, Gear, GitBranch, NotePencil, RocketLaunch, ShieldCheck, UsersThree, WarningCircle, X } from "@phosphor-icons/react";
import { type Overview, request } from "../api";
import { type AppRoute, type OperationsFilters, routePath, shouldHandleNavigation } from "../routes";
import { useDeploymentCatalog } from "../deployments/DeploymentCatalog";
import { type AuditEvent } from "./client";
import { canManageProject } from "../permissions";
import "./workflows.css";

const descriptions: Record<string, string> = {
  "POST /projects": "Create project",
  "PUT /projects/{id}": "Update project",
  "DELETE /projects/{id}": "Remove project",
  "POST /users": "Create user",
  "PUT /users/{id}": "Update user",
  "DELETE /users/{id}": "Remove user",
  "POST /teams": "Create team",
  "PUT /teams/{id}": "Update team",
  "DELETE /teams/{id}": "Remove team",
  "POST /role-assignments": "Update access grant",
  "DELETE /role-assignments/{id}": "Remove access grant",
  "POST /apps": "Create application",
  "DELETE /apps/{id}": "Remove application",
  "PUT /apps/{id}/helm-values": "Update Helm values",
  "PUT /apps/{id}/hooks": "Update application hooks",
  "POST /servers": "Register server",
  "PUT /servers/{id}": "Update server",
  "DELETE /servers/{id}": "Remove server",
  "POST /operations/backups": "Create controller backup",
  "POST /operations/backups/{id}/verify": "Verify backup restore",
  "PUT /apps/{id}/owner": "Update application owner",
  "POST /apps/{id}/deployments": "Start deployment",
  "POST /deployments/{id}/cancel": "Cancel deployment",
  "POST /deployments/{id}/rollback": "Restore release",
  "POST /apps/{id}/release-preview": "Preview deployment",
  "POST /deployments/{id}/rollback-preview": "Preview release rollback",
  "PUT /deployments/{id}/release": "Update release notes",
  "PUT /apps/{id}/service-bindings": "Update service bindings",
  "POST /apps/{id}/drift/check": "Check runtime drift",
  "POST /apps/{id}/observations/check": "Check application",
  "PUT /apps/{id}/observations": "Update application checks",
  "POST /apps/{id}/reapply": "Reapply deployed configuration",
  "POST /services": "Register service",
  "PUT /services/{id}": "Update service connection",
  "DELETE /services/{id}": "Remove service registration",
  "POST /services/{id}/verify": "Test service connection",
  "POST /services/{id}/redeploy": "Redeploy service consumers",
  "PUT /projects/{id}/retention": "Update history retention",
  "POST /projects/{id}/retention/preview": "Preview history cleanup",
  "POST /projects/{id}/retention/apply": "Clean up eligible history",
  "POST /identity-team-mappings": "Add identity mapping",
  "DELETE /identity-team-mappings/{id}": "Remove identity mapping",
  "POST /workflow/stages/{id}/approve": "Approve environment promotion",
  "POST /config-sources/{id}/sync": "Sync repository configuration",
};
export function auditActionLabel(action: string) {
  const normalized = action.trim().replace(/^(\S+)\s+(?:\/api\/v\d+)?(\/.*)$/, "$1 $2");
  if (descriptions[normalized]) return descriptions[normalized];
  const [method, path = ""] = normalized.split(" ");
  const resource = path.split("/").filter(part => part && !part.includes("{")).at(0)?.replaceAll("-", " ");
  const verb = method === "DELETE" ? "Remove" : method === "POST" ? "Run" : method === "PUT" || method === "PATCH" ? "Update" : "Access";
  return resource ? `${verb} ${resource}` : "Recorded operation";
}
function ActionIcon({ action }: { action: string }) {
  const Icon = action.includes("backup") ? Archive : action.includes("owner") || action.includes("team") || action.includes("access") ? UsersThree : action.includes("service") ? Database : action.endsWith("/release") ? NotePencil : action.includes("workflow") || action.includes("config-source") ? GitBranch : action.includes("deployment") ? RocketLaunch : action.includes("check") || action.includes("observations") ? ShieldCheck : Gear;
  return <Icon size={17} />;
}
function dayTitle(value: string) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime()) || date.getFullYear() <= 1) return "Date unavailable";
  const today = new Date(); const yesterday = new Date(); yesterday.setDate(yesterday.getDate() - 1);
  if (date.toDateString() === today.toDateString()) return "Today";
  if (date.toDateString() === yesterday.toDateString()) return "Yesterday";
  return date.toLocaleDateString("en", { month: "short", day: "numeric", year: "numeric" });
}
const failure = (cause: unknown) => cause instanceof Error ? cause.message : String(cause);
export function Activity({ overview, filters, onFilters, onNavigate }: { overview: Overview; filters: OperationsFilters; onFilters: (filters: OperationsFilters) => void; onNavigate?: (route: AppRoute) => void }) {
  const { items: catalog } = useDeploymentCatalog();
  const [query, setQuery] = useState(filters.query ?? "");
  const [actor, setActor] = useState(filters.actorId ?? "");
  const [action, setAction] = useState(filters.action ?? "");
  const [items, setItems] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(false);
  const [more, setMore] = useState(false);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const generation = useRef(0);
  const inFlight = useRef(false);
  const params = new URLSearchParams();
  for (const key of ["projectId", "outcome", "actorId", "action", "appId", "since", "until"] as const) if (filters[key]) params.set(key, filters[key]!);
  if (filters.query) params.set("q", filters.query);
  const scope = params.toString();
  const authorization = JSON.stringify([overview.identity?.id, overview.identity?.systemRole, overview.projectPermissions]);
  const requestScope = JSON.stringify([scope, authorization]);
  const currentScope = useRef(requestScope); currentScope.current = requestScope;
  useEffect(() => { setQuery(filters.query ?? ""); setActor(filters.actorId ?? ""); setAction(filters.action ?? ""); }, [filters.query, filters.actorId, filters.action]);
  useEffect(() => {
    let alive = true; const requestID = ++generation.current; inFlight.current = true;
    setItems([]); setMore(false); setError(""); setLoading(true);
    request<AuditEvent[]>(`/api/v1/audit?${scope}`).then(rows => { if (alive && currentScope.current === requestScope && generation.current === requestID) { setItems(rows); setMore(rows.length === 100); } }).catch(cause => { if (alive && currentScope.current === requestScope) setError(failure(cause)); }).finally(() => { if (alive && generation.current === requestID) { inFlight.current = false; setLoading(false); } });
    return () => { alive = false; if (generation.current === requestID) generation.current++; };
  }, [scope, requestScope, refresh]);
  async function loadMore() {
    if (inFlight.current || !items.length) return;
    const requestID = generation.current; inFlight.current = true; setLoading(true); setError("");
    try {
      const query = new URLSearchParams(scope); query.set("before", items.at(-1)!.id);
      const rows = await request<AuditEvent[]>(`/api/v1/audit?${query}`);
      if (currentScope.current === requestScope && generation.current === requestID) { setItems(old => [...old, ...rows.filter(row => !old.some(item => item.id === row.id))]); setMore(rows.length === 100); }
    } catch (cause) { if (currentScope.current === requestScope && generation.current === requestID) setError(failure(cause)); }
    finally { if (generation.current === requestID) { inFlight.current = false; setLoading(false); } }
  }
  const visibleItems = items.filter(item => item.projectId ? canManageProject(overview, item.projectId, "project.view") : overview.identity?.systemRole === "owner");
  const groups = new Map<string, AuditEvent[]>();
  for (const item of visibleItems) { const day = dayTitle(item.createdAt); groups.set(day, [...(groups.get(day) ?? []), item]); }
  return <section className="ops-task-panel operations-activity">
    <header className="ops-task-heading"><div><h2>Activity</h2><p>Accepted and rejected requests across the projects you can view. Runtime results appear in Deployments.</p></div><button className="quiet-button" disabled={loading} onClick={() => setRefresh(value => value + 1)}><ArrowClockwise size={14} />Refresh</button></header>
    <form className="ops-task-toolbar" onSubmit={event => { event.preventDefault(); onFilters({ ...filters, query: query.trim() || undefined, actorId: actor.trim() || undefined, action: action.trim() || undefined }); }}>
      <label className="ops-task-search">Search activity<input aria-label="Search activity" value={query} maxLength={200} placeholder="Actor, application, or action…" onChange={event => setQuery(event.target.value)} /></label>
      <label>Outcome<select aria-label="Activity outcome" value={filters.outcome ?? ""} onChange={event => onFilters({ ...filters, outcome: event.target.value as OperationsFilters["outcome"] || undefined })}><option value="">All outcomes</option><option value="succeeded">Accepted</option><option value="rejected">Rejected</option></select></label>
      <button className="quiet-button" type="submit">Search</button>
      <details className="ops-activity-advanced"><summary>More filters<CaretDown size={13} /></summary><div><label>Actor ID<input value={actor} onChange={event => setActor(event.target.value)} /></label><label>Exact action<input value={action} placeholder="METHOD /api/v1/route" onChange={event => setAction(event.target.value)} /></label><small>Apply these filters with Search.</small></div></details>
    </form>
    {(filters.since || filters.until || filters.appId) && <div className="ops-task-scope">{(filters.since || filters.until) && <span>{filters.since ? new Date(filters.since).toLocaleString() : "Start of history"} – {filters.until ? new Date(filters.until).toLocaleString() : "Now"}<button aria-label="Clear activity date range" onClick={() => onFilters({ ...filters, since: undefined, until: undefined })}><X size={12} /></button></span>}{filters.appId && <span>{catalog.find(item => item.appId === filters.appId)?.appName || overview.apps.find(item => item.id === filters.appId)?.name || "Selected application"}<button aria-label="Clear activity application" onClick={() => onFilters({ ...filters, appId: undefined })}><X size={12} /></button></span>}</div>}
    {error && <div className="ops-task-error" role="alert"><WarningCircle size={16} /><span>{error}</span><button className="quiet-button" onClick={() => items.length ? void loadMore() : setRefresh(value => value + 1)}>Try again</button></div>}
    {loading && !visibleItems.length && <p className="ops-task-empty" role="status">Loading activity…</p>}
    {!loading && !visibleItems.length && !error && <div className="ops-task-empty"><CheckCircle size={21} /><strong>No matching activity</strong><p>Try another project, outcome, or search. New actions appear here as they happen.</p></div>}
    {[...groups].map(([day, rows]) => <section className="ops-activity-day" key={day}><h3>{day}</h3><ol>{rows.map(item => {
      const appName = catalog.find(app => app.appId === item.appId)?.appName || overview.apps.find(app => app.id === item.appId)?.name;
      const projectName = overview.projects.find(project => project.id === item.projectId)?.name;
      const serviceName = item.action.includes("/services/") ? overview.services?.find(service => service.id === item.resourceId)?.name : undefined;
      const resource = appName || serviceName || projectName || (item.projectId ? "Recorded project resource" : "Controller");
      const route: AppRoute | undefined = item.action.includes("/deployments/{id}") && item.resourceId ? { view: "deployments", deploymentID: item.resourceId } : item.appId ? { view: "deployments", deploymentFilters: { layout: "list", app: item.appId } } : undefined;
      const validTime = Number.isFinite(Date.parse(item.createdAt)) && !item.createdAt.startsWith("0001-");
      return <li key={item.id}><details className="ops-activity-event"><summary><span className={`ops-activity-icon ${item.outcome === "rejected" ? "rejected" : ""}`}><ActionIcon action={item.action} /></span><span className="ops-activity-copy"><strong>{auditActionLabel(item.action)}</strong><span>{item.actorName || "Unnamed actor"} · {resource}{item.impersonatorId && " · Impersonated session"}</span></span><span className={`ops-activity-outcome ${item.outcome}`}>{item.outcome === "succeeded" ? <CheckCircle size={13} /> : <WarningCircle size={13} />}{item.outcome === "succeeded" ? "Accepted" : item.outcome === "rejected" ? "Rejected" : item.outcome || "Recorded"}</span><time dateTime={validTime ? item.createdAt : undefined} title={validTime ? new Date(item.createdAt).toLocaleString() : undefined}>{validTime ? new Date(item.createdAt).toLocaleTimeString("en", { hour: "numeric", minute: "2-digit" }) : "No time"}</time><CaretDown className="ops-activity-chevron" size={13} /></summary><div className="ops-activity-details"><dl><div><dt>Recorded action</dt><dd><code>{item.action}</code></dd></div><div><dt>Actor ID</dt><dd><code>{item.actorId}</code></dd></div>{item.impersonatorId && <div><dt>Impersonator ID</dt><dd><code>{item.impersonatorId}</code></dd></div>}{item.resourceId && <div><dt>Resource ID</dt><dd><code>{item.resourceId}</code></dd></div>}<div><dt>Event ID</dt><dd><code>{item.id}</code></dd></div></dl>{route && <a className="ops-task-link" href={routePath(route)} onClick={event => { if (onNavigate && shouldHandleNavigation(event)) { event.preventDefault(); onNavigate(route); } }}>View {route.deploymentID ? "deployment" : "application deployments"}<ArrowRight size={13} /></a>}</div></details></li>;
    })}</ol></section>)}
    {more && <div className="ops-task-footer"><button className="quiet-button" disabled={loading} onClick={() => void loadMore()}>{loading ? "Loading…" : "Load older activity"}</button><span>{visibleItems.length} events shown</span></div>}
  </section>;
}
