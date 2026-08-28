export type View = "deployments" | "applications" | "events" | "projects" | "servers" | "secrets" | "connections";
export type ApplicationSection = "applications" | "templates" | "helm" | "groups";
export type EventSection = "rules" | "activity";
export type DeploymentSection = "summary" | "topology" | "values" | "manifests";

export type AppRoute = {
  view: View;
  deploymentID?: string;
  deploymentSection?: DeploymentSection;
  serverID?: string;
  applicationID?: string;
  applicationSection?: ApplicationSection;
  eventSection?: EventSection;
};

const views = new Set<View>(["deployments", "applications", "events", "projects", "servers", "secrets", "connections"]);

export function readRoute(location: Pick<Location, "pathname" | "search"> = window.location): AppRoute {
  const segments = location.pathname.split("/").filter(Boolean).map((segment) => decodeURIComponent(segment));
  const first = segments[0] as View | undefined;
  if (first === "deployments") return { view: "deployments", deploymentID: segments[1] || undefined, ...(segments[2] === "topology" ? { deploymentSection: "topology" as const } : segments[2] === "values" ? { deploymentSection: "values" as const } : segments[2] === "manifests" ? { deploymentSection: "manifests" as const } : {}) };
  if (first === "applications") {
    if (segments[1] && !isApplicationSectionPath(segments[1])) return { view: "applications", applicationID: segments[1] };
    return { view: "applications", applicationSection: applicationSectionFromPath(segments[1]) };
  }
  if (first === "events") return { view: "events", eventSection: segments[1] === "activity" ? "activity" : "rules" };
  if (first === "servers") return { view: "servers", serverID: segments[1] || undefined };
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
  if (route.view === "deployments") return route.deploymentID ? `/deployments/${encodeURIComponent(route.deploymentID)}${route.deploymentSection && route.deploymentSection !== "summary" ? `/${route.deploymentSection}` : ""}` : "/deployments";
  if (route.view === "applications") {
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
