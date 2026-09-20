export type View = "operations" | "analytics" | "deployments" | "applications" | "events" | "projects" | "servers" | "services" | "secrets" | "connections" | "access";
export type ApplicationSection = "applications" | "templates" | "helm" | "groups";
export type EventSection = "rules" | "activity";
export type DeploymentSection = "summary" | "topology" | "values" | "manifests" | "history";
export type AnalyticsFilters = { days?: 7 | 30 | 90; projectId?: string; kind?: "deployment" | "workflow" | "job"; section?: "overview" | "data" };
export type OperationsSection = "overview" | "activity" | "ownership" | "retention" | "backups" | "identity";
export type OperationsFilters = { section?: OperationsSection; projectId?: string; query?: string; outcome?: "succeeded" | "rejected"; actorId?: string; action?: string; appId?: string; unassigned?: boolean; since?: string; until?: string };

export type DeploymentFilters = { query?: string; project?: string; app?: string; environment?: string; target?: string; status?: string; revision?: string; completedFrom?: string; completedTo?: string; layout?: "board" | "list" | "compare"; pinned?: boolean };

export type AppRoute = {
  view: View;
  deploymentID?: string;
  deploymentSection?: DeploymentSection;
  deploymentApplicationID?: string;
  deploymentStage?: string;
  deploymentFilters?: DeploymentFilters;
  analyticsFilters?: AnalyticsFilters;
  operationsFilters?: OperationsFilters;
  serverID?: string;
  applicationID?: string;
  configurationSourceID?: string;
  applicationSection?: ApplicationSection;
  eventSection?: EventSection;
};

const views = new Set<View>(["operations", "analytics","deployments", "applications", "events", "projects", "servers", "services", "secrets", "connections", "access"]);

export function readRoute(location: Pick<Location, "pathname" | "search"> = window.location): AppRoute {
  const segments = location.pathname.split("/").filter(Boolean).map((segment) => decodeURIComponent(segment));
  const first = segments[0] as View | undefined;
  if (first === "deployments") {
    const params = new URLSearchParams(location.search);
    return {
      view: "deployments",
      deploymentID: segments[1] || undefined,
      ...(!segments[1] && ["q", "project", "app", "environment", "target", "status", "revision", "completedFrom", "completedTo", "layout", "pinned"].some(key => params.has(key)) ? { deploymentFilters: readDeploymentFilters(params) } : {}),
      ...(segments[2] === "topology" ? { deploymentSection: "topology" as const } : segments[2] === "values" ? { deploymentSection: "values" as const } : segments[2] === "history" ? { deploymentSection: "history" as const } : segments[2] === "manifests" ? { deploymentSection: "manifests" as const } : {}),
      ...(!segments[1] && params.get("application") ? { deploymentApplicationID: params.get("application") || undefined } : {}),
      ...(!segments[1] && params.get("stage") ? { deploymentStage: params.get("stage") || undefined } : {}),
    };
  }
  if (first === "applications") {
    if (segments[1] === "configurations" && segments[2]) return { view: "applications", configurationSourceID: segments[2] };
    if (segments[1] && !isApplicationSectionPath(segments[1])) return { view: "applications", applicationID: segments[1] };
    return { view: "applications", applicationSection: applicationSectionFromPath(segments[1]) };
  }
  if (first === "events") return { view: "events", eventSection: segments[1] === "activity" ? "activity" : "rules" };
  if (first === "servers") return { view: "servers", serverID: segments[1] || undefined };
  if (first === "operations") {
    const params = new URLSearchParams(location.search);
    const filters: OperationsFilters = {};
    const section = params.get("section");
    if (["activity", "ownership", "retention", "backups", "identity"].includes(section ?? "")) filters.section = section as OperationsSection;
    for (const key of ["projectId", "actorId", "action", "appId", "since", "until"] as const) if (params.get(key)) filters[key] = params.get(key)!;
    if (params.get("q")) filters.query = params.get("q")!;
    if (params.get("outcome") === "succeeded" || params.get("outcome") === "rejected") filters.outcome = params.get("outcome") as OperationsFilters["outcome"];
    if (["true", "false"].includes(params.get("unassigned") ?? "")) filters.unassigned = params.get("unassigned") === "true";
    return {view: "operations", ...(Object.keys(filters).length ? {operationsFilters: filters} : {})};
  }
  if (first === "analytics") {
    const params = new URLSearchParams(location.search);
    const filters: AnalyticsFilters = {};
    const days = Number(params.get("days"));
    if (days === 7 || days === 30 || days === 90) filters.days = days;
    if (params.get("projectId")) filters.projectId = params.get("projectId")!;
    const kind = params.get("kind");
    if (kind === "deployment" || kind === "workflow" || kind === "job") filters.kind = kind;
    if (params.get("section") === "data") filters.section = "data";
    return {view: "analytics", ...(Object.keys(filters).length ? {analyticsFilters: filters} : {})};
  }
  if (first && views.has(first)) return { view: first };

  const params = new URLSearchParams(location.search);
  const legacyView = params.get("view") as View | null;
  if (legacyView && views.has(legacyView)) {
    if (legacyView === "deployments") return { view: legacyView, deploymentID: params.get("deployment") || undefined };
    if (legacyView === "applications") return { view: legacyView, applicationSection: applicationSectionFromPath(params.get("section") || undefined) };
    if (legacyView === "events") return { view: legacyView, eventSection: params.get("section") === "activity" ? "activity" : "rules" };
    return { view: legacyView };
  }
  return { view: "deployments" };
}

export function routePath(route: AppRoute) {
  if (route.view === "operations") {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(route.operationsFilters ?? {})) if (value !== undefined && value !== "" && value !== "overview") params.set(key === "query" ? "q" : key, String(value));
    return `/operations${params.size ? `?${params}` : ""}`;
  }
  if (route.view === "analytics") {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(route.analyticsFilters ?? {})) if (value && value !== "overview") params.set(key, String(value));
    return `/analytics${params.size ? `?${params}` : ""}`;
  }
  if (route.view === "deployments") {
    if (route.deploymentID) return `/deployments/${encodeURIComponent(route.deploymentID)}${route.deploymentSection && route.deploymentSection !== "summary" ? `/${route.deploymentSection}` : ""}`;
    const params = new URLSearchParams();
    if (route.deploymentApplicationID) params.set("application", route.deploymentApplicationID);
    if (route.deploymentStage) params.set("stage", route.deploymentStage);
    const filters = route.deploymentFilters;
    if (filters) for (const [key, value] of Object.entries(filters)) if (value && value !== "board") params.set(key === "query" ? "q" : key, String(value));
    return `/deployments${params.size ? `?${params.toString()}` : ""}`;
  }
  if (route.view === "applications") {
    if (route.configurationSourceID) return `/applications/configurations/${encodeURIComponent(route.configurationSourceID)}/topology`;
    if (route.applicationID) return `/applications/${encodeURIComponent(route.applicationID)}/topology`;
    const section = route.applicationSection ?? "applications";
    if (section === "templates") return "/applications/templates";
    if (section === "helm") return "/applications/helm-sources";
    if (section === "groups") return "/applications/preview-groups";
    return "/applications";
  }
  if (route.view === "events") return route.eventSection === "activity" ? "/events/activity" : "/events";
  if (route.view === "servers" && route.serverID) return `/servers/${encodeURIComponent(route.serverID)}/topology`;
  return `/${route.view}`;
}

function isApplicationSectionPath(value: string) {
  return ["templates", "helm", "helm-sources", "groups", "preview-groups"].includes(value);
}

function applicationSectionFromPath(value?: string): ApplicationSection {
  if (value === "templates") return "templates";
  if (value === "helm" || value === "helm-sources") return "helm";
  if (value === "groups" || value === "preview-groups") return "groups";
  return "applications";
}

export function shouldHandleNavigation(event: { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean }) {
  return event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
}

function readDeploymentFilters(params: URLSearchParams): DeploymentFilters {
 const result: DeploymentFilters = {};
 for (const key of ["project", "app", "environment", "target", "status", "revision", "completedFrom", "completedTo"] as const) if (params.get(key)) result[key] = params.get(key)!;
 if(params.get("q")) result.query=params.get("q")!;
 if(params.get("layout")==="list"||params.get("layout")==="compare") result.layout=params.get("layout") as "list"|"compare";
 if(params.get("pinned")==="true") result.pinned=true;
 return result;
}
