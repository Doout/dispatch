import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ArrowClockwise, ArrowRight, CaretLeft, CaretRight, MagnifyingGlass, Plus } from "@phosphor-icons/react";
import type { Deployment, Overview, WorkflowResource, WorkflowRevision } from "../api";
import { useDeploymentCatalog } from "../deployments/DeploymentCatalog";
import { catalogClient, type CatalogItem } from "../deployments/catalogClient";
import { canManageAnyProject } from "../permissions";
import { relative, short } from "../presentation";
import { routePath, shouldHandleNavigation, type AppRoute, type DeploymentFilters } from "../routes";
import { isPreviewCheckRun, workflowResourceStatus } from "../workflows/status";
import "./WorkloadsPage.css";

type Section = "applications" | "parallel" | "runs";
type Props = {
  overview: Overview;
  section: Section;
  onNavigate: (route: AppRoute) => void;
  onCreateApplication: () => void;
  filters?: DeploymentFilters;
  onFilters?: (filters: DeploymentFilters) => void;
};
type InventoryItem = {
  id: string;
  name: string;
  projectIDs: string[];
  kind: string;
  source: string;
  target: string;
  state: string;
  message?: string;
  updatedAt: string;
  route: AppRoute;
  deployment?: Deployment;
  refs?: { label: string; url?: string }[];
};
const pageSize = 25;
const titles: Record<Section, string> = { applications: "Applications", parallel: "Parallel deployments", runs: "Runs" };
const inactive = new Set(["expired", "removed", "closed", "deleted"]);
const attention = new Set(["failed", "invalid", "error", "blocked", "degraded", "unhealthy", "cleanup_failed"]);

export function WorkloadsPage({ overview, section, onNavigate, onCreateApplication, filters: controlledFilters, onFilters }: Props) {
  const [localFilters, setLocalFilters] = useState<DeploymentFilters>({});
  const filters = controlledFilters ?? localFilters;
  const changeFilters = onFilters ?? setLocalFilters;
  const catalog = useDeploymentCatalog();
  const inventory = useMemo(() => inventoryItems(overview, catalog.items, section), [overview, catalog.items, section]);
  const canCreate = section === "applications" && canManageAnyProject(overview, "project.configure") && overview.servers.some(server => server.state === "ready" && server.runtime === "docker");

  return <div className="page-layout next-workloads">
    <header className="next-workloads-heading">
      <h1>{titles[section]}</h1>
      {canCreate && <button className="primary-button" onClick={onCreateApplication}><Plus size={16} />Add application</button>}
    </header>
    {section === "runs"
      ? <Runs overview={overview} items={catalog.items} filters={filters} onFilters={changeFilters} onNavigate={onNavigate} />
      : <Inventory key={section} overview={overview} section={section} items={inventory} filters={filters} onFilters={changeFilters} onNavigate={onNavigate} />}
    {catalog.error && <p className="next-workloads-notice" role="status">Release status is unavailable. {catalog.error}</p>}
  </div>;
}

function Inventory({ overview, section, items, filters, onFilters, onNavigate }: {
  overview: Overview; section: "applications" | "parallel"; items: InventoryItem[];
  filters: DeploymentFilters; onFilters: (filters: DeploymentFilters) => void; onNavigate: Props["onNavigate"];
}) {
  const [selection, setSelection] = useState<"active" | "attention" | "all">("active");
  const [pagination, setPagination] = useState({ key: "", page: 0 });
  const query = (filters.query ?? "").trim().toLocaleLowerCase();
  const filtered = useMemo(() => items.filter(item => {
    if (filters.project && !item.projectIDs.includes(filters.project)) return false;
    if (selection === "active" && inactive.has(item.state)) return false;
    if (selection === "attention" && !attention.has(item.state)) return false;
    const projectNames = item.projectIDs.map(id => overview.projects.find(project => project.id === id)?.name ?? id);
    return !query || [item.name, item.id, item.source, item.target, item.state, ...projectNames, ...(item.refs ?? []).map(ref => ref.label)].join(" ").toLocaleLowerCase().includes(query);
  }), [items, filters.project, query, selection, overview.projects]);
  const key = `${filters.project ?? ""}:${query}:${selection}`;
  const page = Math.min(pagination.key === key ? pagination.page : 0, Math.max(0, Math.ceil(filtered.length / pageSize) - 1));
  const visible = filtered.slice(page * pageSize, (page + 1) * pageSize);
  const labels = { active: "Active", attention: "Needs attention", all: "All available" };

  return <section className="next-workloads-list" aria-label={`${titles[section]} inventory`}>
    <div className="next-workloads-controls">
      <Search value={filters.query ?? ""} label={`Search ${titles[section].toLowerCase()}`} placeholder={section === "parallel" ? "Name, repository or PR number" : "Name, repository or target"} onChange={query => onFilters({ ...filters, query: query || undefined })} />
      <ProjectSelect overview={overview} value={filters.project ?? ""} onChange={project => onFilters({ ...filters, project: project || undefined })} />
    </div>
    <div className="next-workloads-view-line">
      <div className="next-workloads-views" aria-label="Inventory status">
        {(["active", "attention", "all"] as const).map(value => <button key={value} aria-pressed={selection === value} onClick={() => setSelection(value)}>{labels[value]}</button>)}
      </div>
      <span className="next-workloads-count">{filtered.length.toLocaleString()} {section === "applications" ? filtered.length === 1 ? "application" : "applications" : filtered.length === 1 ? "parallel deployment" : "parallel deployments"}</span>
    </div>
    <div className="next-workloads-table-wrap">
      <table className="next-workloads-table" aria-label={titles[section]}>
        <thead><tr><th scope="col">Name</th><th scope="col">Source</th><th scope="col">Target</th><th scope="col">Status</th><th scope="col">Updated</th><th scope="col"><span className="sr-only">Release</span></th></tr></thead>
        <tbody>{visible.map(item => <tr key={item.id}>
          <td><RouteLink route={item.route} onNavigate={onNavigate} className="next-workloads-name">{item.name}</RouteLink><small>{item.projectIDs.map(id => overview.projects.find(project => project.id === id)?.name ?? id).join(", ") || "Project unavailable"}<span className="next-workloads-separator">·</span>{item.kind}</small></td>
          <td><span className="next-workloads-truncate" title={item.source}>{item.source || "No repository"}</span>{!!item.refs?.length && <small>{item.refs.map((ref, index) => <span key={`${ref.label}:${index}`}>{index > 0 && ", "}{ref.url ? <a href={ref.url} target="_blank" rel="noopener noreferrer">{ref.label}</a> : ref.label}</span>)}</small>}</td>
          <td><span className="next-workloads-truncate" title={item.target}>{item.target || "No target"}</span></td>
          <td><Status state={item.state} message={item.message} /></td>
          <td><Timestamp value={item.updatedAt} /></td>
          <td>{item.deployment && <RouteLink route={{ view: "deployments", deploymentID: item.deployment.id }} onNavigate={onNavigate} className="next-workloads-release">Release<ArrowRight size={13} /></RouteLink>}</td>
        </tr>)}</tbody>
      </table>
    </div>
    {!visible.length && <div className="next-workloads-empty"><h2>{items.length ? "No matches" : `No ${titles[section].toLowerCase()}`}</h2><p>{items.length ? "Change the search, project or status filter." : section === "parallel" ? "Parallel deployments appear here when a workflow or pull request creates one." : "Add an application or import a repository from Configuration."}</p></div>}
    <Pagination label={filtered.length ? `${page * pageSize + 1}–${Math.min((page + 1) * pageSize, filtered.length)} of ${filtered.length.toLocaleString()}` : "0 results"} previous={page > 0} next={(page + 1) * pageSize < filtered.length} onPrevious={() => setPagination({ key, page: page - 1 })} onNext={() => setPagination({ key, page: page + 1 })} />
  </section>;
}

function Runs({ overview, items, filters, onFilters, onNavigate }: {
  overview: Overview; items: CatalogItem[]; filters: DeploymentFilters; onFilters: (filters: DeploymentFilters) => void; onNavigate: Props["onNavigate"];
}) {
  const [result, setResult] = useState<{ items: Deployment[]; next?: string }>({ items: [] });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [pagination, setPagination] = useState<{ query: string; cursors: string[] }>({ query: "", cursors: [""] });
  const params = new URLSearchParams();
  for (const key of ["query", "project", "app", "environment", "target", "status", "revision", "completedFrom", "completedTo"] as const) {
    if (filters[key]) params.set(key === "query" ? "q" : key === "app" ? "application" : key, filters[key]!);
  }
  const query = params.toString();
  const cursors = pagination.query === query ? pagination.cursors : [""];
  const cursor = cursors[cursors.length - 1];
  const activity = overview.deployments.map(run => `${run.id}:${run.state}:${run.finishedAt ?? ""}`).join(",");
  const applications = useMemo(() => {
    const values = new Map<string, string>();
    for (const item of items) if (!filters.project || item.projectId === filters.project) values.set(item.resourceId || item.appId, item.resourceName || item.appName);
    for (const app of overview.apps) if (!app.template && !app.generated && (!filters.project || app.projectId === filters.project)) values.set(app.id, app.name);
    return [...values].sort((a, b) => a[1].localeCompare(b[1]));
  }, [items, overview.apps, filters.project]);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError("");
    const timer = window.setTimeout(() => {
      const request = new URLSearchParams(query);
      if (cursor) request.set("before", cursor);
      void catalogClient.search(request).then(value => {
        if (alive) setResult(value);
      }).catch(reason => {
        if (alive) { setError(reason instanceof Error ? reason.message : "Runs are unavailable."); setResult({ items: [] }); }
      }).finally(() => { if (alive) setLoading(false); });
    }, 180);
    return () => { alive = false; window.clearTimeout(timer); };
  }, [query, cursor, refresh, activity, overview.identity?.id]);

  const extraFilters = [filters.environment && `Environment: ${filters.environment}`, filters.target && `Target: ${overview.servers.find(server => server.id === filters.target)?.name ?? filters.target}`, filters.revision && `Revision: ${filters.revision}`, filters.completedFrom && `Completed from ${new Date(filters.completedFrom).toLocaleString()}`, filters.completedTo && `Before ${new Date(filters.completedTo).toLocaleString()}`].filter(Boolean);
  return <section className="next-workloads-list" aria-label="Deployment run history" aria-busy={loading}>
    <div className="next-workloads-controls">
      <Search value={filters.query ?? ""} label="Search runs" placeholder="Application, run ID or revision" onChange={query => onFilters({ ...filters, query: query || undefined })} />
      <ProjectSelect overview={overview} value={filters.project ?? ""} onChange={project => onFilters({ ...filters, project: project || undefined, app: undefined })} />
      <label className="next-workloads-field"><span>Application</span><select value={filters.app ?? ""} onChange={event => onFilters({ ...filters, app: event.target.value || undefined })}><option value="">All applications</option>{filters.app && !applications.some(([id]) => id === filters.app) && <option value={filters.app}>{filters.app}</option>}{applications.map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label>
      <label className="next-workloads-field"><span>Status</span><select value={filters.status ?? ""} onChange={event => onFilters({ ...filters, status: event.target.value || undefined })}><option value="">All statuses</option>{["queued", "fetching", "building", "starting", "checking", "routing", "succeeded", "failed", "cancelled"].map(state => <option key={state} value={state}>{statusLabel(state)}</option>)}</select></label>
      <button className="quiet-button next-workloads-refresh" disabled={loading} onClick={() => { setPagination({ query, cursors: [""] }); setRefresh(value => value + 1); }}><ArrowClockwise size={15} />Refresh</button>
    </div>
    {!!extraFilters.length && <div className="next-workloads-filter-summary">{extraFilters.map(filter => <span key={String(filter)}>{filter}</span>)}<button onClick={() => onFilters({})}>Clear filters</button></div>}
    <div className="next-workloads-view-line"><span>Deployment runs</span><span className="next-workloads-count">Newest first</span></div>
    {error && <div className="next-workloads-error" role="alert"><span>{error}</span><button className="quiet-button" onClick={() => setRefresh(value => value + 1)}>Retry</button></div>}
    <div className="next-workloads-table-wrap">
      <table className="next-workloads-table next-workloads-runs" aria-label="Deployment runs">
        <thead><tr><th scope="col">Application</th><th scope="col">Revision</th><th scope="col">Status</th><th scope="col">Started</th><th scope="col">Duration</th><th scope="col">Run</th></tr></thead>
        <tbody>{!loading && result.items.map(run => {
          const item = items.find(item => item.appId === run.appId);
          const app = run.app ?? overview.apps.find(app => app.id === run.appId);
          const name = item?.resourceName || item?.appName || app?.name || run.appId;
          const projectID = item?.projectId || app?.projectId;
          const project = overview.projects.find(project => project.id === projectID)?.name;
          return <tr key={run.id}>
            <td><RouteLink route={{ view: "deployments", deploymentID: run.id }} onNavigate={onNavigate} className="next-workloads-name">{name}</RouteLink><small>{[project, item?.environment].filter(Boolean).join(" · ") || "Deployment"}</small></td>
            <td><code title={run.commitSha}>{short(run.commitSha) || "No revision"}</code></td>
            <td><Status state={run.state} /></td>
            <td><Timestamp value={run.startedAt || run.createdAt} /></td>
            <td><span className="next-workloads-duration">{duration(run)}</span></td>
            <td><RouteLink route={{ view: "deployments", deploymentID: run.id }} onNavigate={onNavigate}><code title={run.id}>{run.id.slice(-8)}</code><ArrowRight size={13} /></RouteLink></td>
          </tr>;
        })}</tbody>
      </table>
    </div>
    {loading && <p className="next-workloads-loading" role="status">Loading runs…</p>}
    {!loading && !error && !result.items.length && <div className="next-workloads-empty"><h2>No runs match</h2><p>Change the filters or deploy an application.</p></div>}
    <Pagination label={`Page ${cursors.length}${loading ? "" : ` · ${result.items.length} ${result.items.length === 1 ? "run" : "runs"}`}`} previous={cursors.length > 1} next={Boolean(result.next)} disabled={loading} onPrevious={() => setPagination({ query, cursors: cursors.slice(0, -1) })} onNext={() => { if (result.next) setPagination({ query, cursors: [...cursors, result.next] }); }} />
  </section>;
}

function Search({ value, label, placeholder, onChange }: { value: string; label: string; placeholder: string; onChange: (value: string) => void }) {
  return <label className="next-workloads-search"><span className="sr-only">{label}</span><MagnifyingGlass size={16} /><input type="search" value={value} placeholder={placeholder} maxLength={200} onChange={event => onChange(event.target.value)} /></label>;
}
function ProjectSelect({ overview, value, onChange }: { overview: Overview; value: string; onChange: (value: string) => void }) {
  return <label className="next-workloads-field"><span>Project</span><select value={value} onChange={event => onChange(event.target.value)}><option value="">All projects</option>{value && !overview.projects.some(project => project.id === value) && <option value={value}>Unavailable project</option>}{overview.projects.map(project => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>;
}
function RouteLink({ route, onNavigate, children, className }: { route: AppRoute; onNavigate: Props["onNavigate"]; children: ReactNode; className?: string }) {
  return <a href={routePath(route)} className={className} onClick={event => { if (shouldHandleNavigation(event)) { event.preventDefault(); onNavigate(route); } }}>{children}</a>;
}
function Pagination({ label, previous, next, disabled = false, onPrevious, onNext }: { label: string; previous: boolean; next: boolean; disabled?: boolean; onPrevious: () => void; onNext: () => void }) {
  return <nav className="next-workloads-pagination" aria-label="Results pages"><span aria-live="polite">{label}</span><div><button className="quiet-button" disabled={disabled || !previous} onClick={onPrevious}><CaretLeft size={14} />Previous</button><button className="quiet-button" disabled={disabled || !next} onClick={onNext}>Next<CaretRight size={14} /></button></div></nav>;
}
function Status({ state, message }: { state: string; message?: string }) {
  const tone = attention.has(state) ? "attention" : ["ready", "healthy", "succeeded", "running"].includes(state) ? "ready" : ["expiring", "queued", "building", "starting", "checking", "routing", "fetching", "pending", "cleaning"].includes(state) ? "progress" : "neutral";
  return <span className={`next-workloads-status ${tone}`} title={message}><i />{statusLabel(state)}</span>;
}
function statusLabel(state: string) {
  if (state === "expiring") return "Cleanup pending";
  if (state === "invalid") return "Configuration error";
  return state.replaceAll("_", " ").replace(/^\w/, letter => letter.toUpperCase());
}
function Timestamp({ value }: { value: string }) {
  return value ? <time dateTime={value} title={new Date(value).toLocaleString()}>{relative(value)}</time> : <span>Not recorded</span>;
}
function duration(run: Deployment) {
  if (!run.startedAt) return "Not started";
  if (!run.finishedAt) return ["succeeded", "failed", "cancelled"].includes(run.state) ? "Not recorded" : "In progress";
  const seconds = Math.max(0, Math.round((Date.parse(run.finishedAt) - Date.parse(run.startedAt)) / 1000));
  if (!Number.isFinite(seconds)) return "Not recorded";
  return seconds < 60 ? `${seconds}s` : seconds < 3600 ? `${Math.floor(seconds / 60)}m ${seconds % 60}s` : `${Math.floor(seconds / 3600)}h ${Math.floor(seconds % 3600 / 60)}m`;
}

function inventoryItems(overview: Overview, catalog: CatalogItem[], section: Section): InventoryItem[] {
  if (section === "runs") return [];
  const sources = new Map((overview.configSources ?? []).map(source => [source.id, source]));
  const apps = new Map(overview.apps.map(app => [app.id, app]));
  const targets = new Map(overview.servers.map(server => [server.id, server.name]));
  const byApp = new Map(catalog.map(item => [item.appId, item]));
  const resourceCatalog = new Map<string, CatalogItem[]>();
  for (const item of catalog) if (item.resourceId) resourceCatalog.set(item.resourceId, [...(resourceCatalog.get(item.resourceId) ?? []), item]);
  const revisions = new Map<string, WorkflowRevision>();
  for (const revision of overview.workflowRevisions ?? []) if (!isPreviewCheckRun(revision) && (!revisions.has(revision.resourceId) || revisions.get(revision.resourceId)!.createdAt < revision.createdAt)) revisions.set(revision.resourceId, revision);
  const resourceItem = (resource: WorkflowResource): InventoryItem => {
    const source = sources.get(resource.configSourceId);
    const deployments = resourceCatalog.get(resource.id) ?? [];
    const revision = revisions.get(resource.id);
    const cleanup = resource.state === "expiring" ? resource.previewCleanups?.find(cleanup => cleanup.state === "blocked") : undefined;
    return {
      id: resource.id, name: resource.name, projectIDs: source ? [source.projectId] : [], kind: resource.kind === "Pipeline" ? "Pipeline" : "Application",
      source: source?.repository || resource.path, target: [...new Set(deployments.map(item => item.targetName || item.targetId))].join(", ") || (resource.targetRefs ?? []).join(", "),
      state: cleanup ? "cleanup_failed" : inactive.has(resource.state) ? resource.state : workflowResourceStatus(resource, revision?.state), message: cleanup?.error || resource.lastError || revision?.error,
      updatedAt: revision && revision.createdAt > resource.updatedAt ? revision.createdAt : resource.updatedAt,
      route: { view: "applications", applicationID: resource.id }, deployment: deployments.find(item => item.current)?.current || deployments.find(item => item.latest)?.latest,
      refs: resource.previewPullRequests?.map(pr => ({ label: `${pr.repository} #${pr.number}`, url: pr.url })),
    };
  };
  const rows = (overview.workflowResources ?? []).filter(resource => Boolean(resource.temporary) === (section === "parallel") && resource.kind !== "ServiceTemplate").map(resourceItem);
  if (section === "applications") {
    for (const app of overview.apps) {
      if (app.template || app.generated) continue;
      const item = byApp.get(app.id);
      const deployment = item?.current ?? item?.latest;
      rows.push({ id: app.id, name: app.name, projectIDs: [app.projectId], kind: app.buildType === "helm" ? "Helm" : app.buildType === "compose" ? "Compose" : "Docker", source: app.sourceRepo || app.helmRepository || "", target: targets.get(app.serverId) || app.serverId, state: item?.latest?.state || app.state, updatedAt: item?.latest?.createdAt || app.createdAt, route: deployment ? { view: "deployments", deploymentID: deployment.id } : { view: "deployments", deploymentFilters: { app: app.id } }, deployment });
    }
  } else {
    for (const preview of overview.previews) {
      const app = apps.get(preview.appId);
      rows.push({ id: `preview:${preview.id}`, name: app?.name || preview.repository, projectIDs: app ? [app.projectId] : [], kind: "Pull request", source: preview.repository, target: app ? targets.get(app.serverId) || app.serverId : "", state: preview.state, message: preview.message, updatedAt: preview.updatedAt, refs: [{ label: `PR #${preview.pullRequestNumber}` }], route: preview.deploymentId ? { view: "deployments", deploymentID: preview.deploymentId } : { view: "deployments", deploymentFilters: { app: preview.appId } } });
    }
    for (const run of overview.previewGroupRuns) {
      const group = overview.previewGroups.find(group => group.id === run.groupId);
      const componentApps = (group?.components ?? run.group?.components ?? []).map(component => apps.get(component.appId)).filter(app => app !== undefined);
      const deployment = run.components.find(component => component.deploymentId)?.deploymentId;
      rows.push({ id: `group:${run.id}`, name: run.slug || group?.name || run.id, projectIDs: [...new Set(componentApps.map(app => app.projectId))], kind: "Group", source: run.sources.map(source => source.repository).join(", "), target: [...new Set(componentApps.map(app => targets.get(app.serverId) || app.serverId))].join(", "), state: run.state, message: run.message, updatedAt: run.updatedAt, refs: run.sources.filter(source => source.pullRequest).map(source => ({ label: `${source.alias} #${source.pullRequest}` })), route: deployment ? { view: "deployments", deploymentID: deployment } : { view: "applications", applicationSection: "groups" } });
    }
  }
  return rows.sort((a, b) => section === "applications" ? a.name.localeCompare(b.name) : b.updatedAt.localeCompare(a.updatedAt) || a.name.localeCompare(b.name));
}
