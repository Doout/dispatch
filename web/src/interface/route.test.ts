import { expect, it } from "vitest";
import { type AppRoute, isNewInterfaceRoute, readRoute, routePath } from "../routes";

function roundTrip(route: AppRoute) {
  const location = new URL(routePath(route), "https://dispatch.test");
  return readRoute(location);
}

it("keeps new workload sections and run filters in shared links", () => {
  for (const workloadSection of ["applications", "parallel", "runs"] as const) {
    const route: AppRoute = { view: "workloads", workloadSection, deploymentFilters: {
      query: "checkout / worker", project: "project-1", app: "application-1", status: "failed", target: "cluster-1", environment: "development", revision: "abc123", layout: "list", pinned: true,
      completedFrom: "2026-10-01T00:00:00.000000123Z", completedTo: "2026-10-02T00:00:00.000000456Z",
    } };
    expect(roundTrip(route)).toEqual(route);
    expect(isNewInterfaceRoute(route)).toBe(true);
  }
});

it("round trips each infrastructure, recovery, and automation page", () => {
  const routes: AppRoute[] = [
    { view: "infrastructure", resourceSection: "machines" },
    { view: "infrastructure", resourceSection: "providers" },
    { view: "recovery", resourceSection: "workload-backups" },
    { view: "recovery", resourceSection: "machine-snapshots" },
    { view: "recovery", resourceSection: "controller-backups" },
    { view: "automation", resourceSection: "credentials" },
    { view: "automation", resourceSection: "assignments" },
    { view: "automation", resourceSection: "receipts" },
  ];
  for (const route of routes) {
    expect(roundTrip(route)).toEqual(route);
    expect(isNewInterfaceRoute(route)).toBe(true);
  }
});

it("normalizes unknown and cross-group sections to an allowed page", () => {
  expect(readRoute({ pathname: "/workloads/unknown", search: "" })).toEqual({ view: "workloads", workloadSection: "applications" });
  expect(readRoute({ pathname: "/recovery/providers", search: "" })).toEqual({ view: "recovery", resourceSection: "workload-backups" });
  expect(routePath({ view: "automation", resourceSection: "machines" })).toBe("/automation/credentials");
});

it("keeps existing pages available independently of the new interface", () => {
  for (const view of ["analytics", "settings", "deployments", "applications", "servers", "services", "operations"] as const) {
    expect(isNewInterfaceRoute({ view })).toBe(false);
    expect(roundTrip({ view }).view).toBe(view);
  }
});
