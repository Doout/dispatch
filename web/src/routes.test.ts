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
      routePath({ view: "applications", applicationID: "resource/one" }),
      routePath({ view: "events", eventSection: "activity" }),
    ];
    expect(paths).toEqual([
      "/deployments/deployment%2Fone",
      "/deployments/deployment%2Fone/manifests",
      "/applications/templates",
      "/applications/helm-sources",
      "/applications/preview-groups",
      "/applications/resource%2Fone/topology",
      "/events/activity",
    ]);
    expect(paths.map((pathname) => readRoute({ pathname, search: "" }))).toEqual([
      { view: "deployments", deploymentID: "deployment/one" },
      { view: "deployments", deploymentID: "deployment/one", deploymentSection: "manifests" },
      { view: "applications", applicationSection: "templates" },
      { view: "applications", applicationSection: "helm" },
      { view: "applications", applicationSection: "groups" },
      { view: "applications", applicationID: "resource/one" },
      { view: "events", eventSection: "activity" },
    ]);
  });

  it("keeps legacy view query URLs readable", () => {
    expect(readRoute({ pathname: "/", search: "?view=connections" })).toEqual({ view: "connections" });
    expect(readRoute({ pathname: "/", search: "?view=applications&section=helm" })).toEqual({ view: "applications", applicationSection: "helm" });
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
