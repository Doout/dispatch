import { describe, expect, it } from "vitest";
import { readRoute, routePath } from "./routes";

describe("application routes", () => {
  it("creates stable URLs for every primary page", () => {
    expect(routePath({ view: "deployments" })).toBe("/deployments");
    expect(routePath({ view: "applications" })).toBe("/applications");
    expect(routePath({ view: "events" })).toBe("/events");
    expect(routePath({ view: "projects" })).toBe("/projects");
    expect(routePath({ view: "servers" })).toBe("/servers");
    expect(routePath({ view: "secrets" })).toBe("/secrets");
    expect(routePath({ view: "connections" })).toBe("/connections");
  });

  it("round trips deployment details and internal tabs", () => {
    const paths = [
      routePath({ view: "deployments", deploymentID: "deployment/one" }),
      routePath({ view: "deployments", deploymentID: "deployment/one", deploymentSection: "manifests" }),
      routePath({ view: "applications", applicationSection: "templates" }),
      routePath({ view: "applications", applicationSection: "helm" }),
      routePath({ view: "applications", applicationSection: "groups" }),
      routePath({ view: "applications", configurationSourceID: "configuration/one" }),
      routePath({ view: "applications", applicationID: "resource/one" }),
      routePath({ view: "events", eventSection: "activity" }),
    ];
    expect(paths).toEqual([
      "/deployments/deployment%2Fone",
      "/deployments/deployment%2Fone/manifests",
      "/applications/templates",
      "/applications/helm-sources",
      "/applications/preview-groups",
      "/applications/configurations/configuration%2Fone/topology",
      "/applications/resource%2Fone/topology",
      "/events/activity",
    ]);
    expect(paths.map((pathname) => readRoute({ pathname, search: "" }))).toEqual([
      { view: "deployments", deploymentID: "deployment/one" },
      { view: "deployments", deploymentID: "deployment/one", deploymentSection: "manifests" },
      { view: "applications", applicationSection: "templates" },
      { view: "applications", applicationSection: "helm" },
      { view: "applications", applicationSection: "groups" },
      { view: "applications", configurationSourceID: "configuration/one" },
      { view: "applications", applicationID: "resource/one" },
      { view: "events", eventSection: "activity" },
    ]);
  });

  it("keeps legacy view query URLs readable", () => {
    expect(readRoute({ pathname: "/", search: "?view=connections" })).toEqual({ view: "connections" });
    expect(readRoute({ pathname: "/", search: "?view=applications&section=helm" })).toEqual({ view: "applications", applicationSection: "helm" });
  });

  it("restores analytics filters from a shared link and ignores unsupported values", () => {
    const route = {view: "analytics" as const, analyticsFilters: {days: 7 as const, projectId: "team/project", kind: "job" as const, section: "data" as const}};
    const url = new URL(routePath(route), "https://dispatch.example");
    expect(readRoute(url)).toEqual(route);
    expect(readRoute({pathname: "/analytics", search: "?days=100&kind=incident&section=unknown"})).toEqual({view: "analytics"});
    expect(routePath({view: "analytics"})).toBe("/analytics");
  });

  it("preserves Operations drilldowns and rejects unsupported sections and outcomes", () => {
    const route = {view: "operations" as const, operationsFilters: {
      section: "activity" as const, projectId: "team/project", query: "release & API",
      outcome: "rejected" as const, actorId: "person", appId: "orders",
      since: "2026-09-19T12:00:00Z", until: "2026-09-20T12:00:00Z",
    }};
    expect(readRoute(new URL(routePath(route), "https://dispatch.example"))).toEqual(route);
    const ownership = {view: "operations" as const, operationsFilters: {section: "ownership" as const, unassigned: true}};
    expect(readRoute(new URL(routePath(ownership), "https://dispatch.example"))).toEqual(ownership);
    const assigned = {...ownership, operationsFilters: {...ownership.operationsFilters, unassigned: false}};
    expect(readRoute(new URL(routePath(assigned), "https://dispatch.example"))).toEqual(assigned);
    expect(readRoute({pathname: "/operations", search: "?section=invalid&outcome=failed&unassigned=no"})).toEqual({view: "operations"});
    expect(routePath({view: "operations", operationsFilters: {section: "overview"}})).toBe("/operations");
  });

  it("keeps the selected deployment stage in the URL", () => {
    const path = routePath({ view: "deployments", deploymentApplicationID: "checkout/app", deploymentStage: "quality gate" });
    const url = new URL(path, "https://dispatch.example");

    expect(path).toBe("/deployments?application=checkout%2Fapp&stage=quality+gate");
    expect(readRoute({ pathname: url.pathname, search: url.search })).toEqual({
      view: "deployments",
      deploymentApplicationID: "checkout/app",
      deploymentStage: "quality gate",
    });
  });
});
