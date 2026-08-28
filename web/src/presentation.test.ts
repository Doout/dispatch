import { describe, expect, it } from "vitest";
import { Deployment } from "./api";
import { clusterDeploymentsByApplication, groupDeployments, relative, short, stageIndex, statusTone } from "./presentation";

describe("deployment presentation", () => {
  it("maps runtime states onto the four visible handoffs", () => {
    expect(stageIndex("queued")).toBe(0);
    expect(stageIndex("building")).toBe(1);
    expect(stageIndex("checking")).toBe(2);
    expect(stageIndex("succeeded")).toBe(3);
  });

  it("keeps exception and success tones explicit", () => {
    expect(statusTone("failed")).toBe("danger");
    expect(statusTone("cancelled")).toBe("danger");
    expect(statusTone("succeeded")).toBe("success");
    expect(statusTone("routing")).toBe("active");
  });

  it("formats compact immutable evidence", () => {
    expect(short("0123456789abcdef")).toBe("01234567");
    expect(relative("2026-08-03T11:59:00Z", Date.parse("2026-08-03T12:00:00Z"))).toBe("1m ago");
  });

  it("moves failures behind a newer success into deployment history", () => {
    const deployments = [
      { id: "failure", appId: "app", state: "failed", createdAt: "2026-08-03T10:00:00Z" },
      { id: "success", appId: "app", state: "succeeded", createdAt: "2026-08-03T12:00:00Z" },
    ] as Deployment[];

    const groups = groupDeployments(deployments);
    expect(groups.failed).toEqual([]);
    expect(groups.latest.map((item) => item.id)).toEqual(["success"]);
    expect(groups.history.map((item) => item.id)).toEqual(["failure"]);
  });

  it("keeps only the newest unresolved failure sequence under attention", () => {
    const deployments = [
      { id: "success", appId: "app", state: "succeeded", createdAt: "2026-08-03T09:00:00Z" },
      { id: "failure-1", appId: "app", state: "failed", createdAt: "2026-08-03T10:00:00Z" },
      { id: "failure-2", appId: "app", state: "failed", createdAt: "2026-08-03T11:00:00Z" },
    ] as Deployment[];

    const groups = groupDeployments(deployments);
    expect(groups.failed.map((item) => item.id)).toEqual(["failure-2"]);
    expect(groups.history.map((item) => item.id)).toEqual(["failure-1", "success"]);
  });

  it("moves a failed attempt into history while its newer retry is running", () => {
    const deployments = [
      { id: "failure", appId: "app", state: "failed", createdAt: "2026-08-03T10:00:00Z" },
      { id: "retry", appId: "app", state: "checking", createdAt: "2026-08-03T11:00:00Z" },
    ] as Deployment[];

    const groups = groupDeployments(deployments);
    expect(groups.active.map((item) => item.id)).toEqual(["retry"]);
    expect(groups.failed).toEqual([]);
    expect(groups.history.map((item) => item.id)).toEqual(["failure"]);
  });

  it("surfaces the newest terminal deployment for every application", () => {
    const deployments = [
      { id: "checkout-old", appId: "checkout", state: "succeeded", createdAt: "2026-08-03T09:00:00Z" },
      { id: "catalog-latest", appId: "catalog", state: "succeeded", createdAt: "2026-08-03T11:00:00Z" },
      { id: "checkout-latest", appId: "checkout", state: "succeeded", createdAt: "2026-08-03T12:00:00Z" },
    ] as Deployment[];

    const groups = groupDeployments(deployments);
    expect(groups.latest.map((item) => item.id)).toEqual(["checkout-latest", "catalog-latest"]);
    expect(groups.history.map((item) => item.id)).toEqual(["checkout-old"]);
  });

  it("collapses repeated application runs while keeping newest attempts first", () => {
    const deployments = [
      { id: "older", appId: "checkout", createdAt: "2026-08-03T10:00:00Z" },
      { id: "other", appId: "catalog", createdAt: "2026-08-03T11:00:00Z" },
      { id: "newer", appId: "checkout", createdAt: "2026-08-03T12:00:00Z" },
    ] as Deployment[];

    const clusters = clusterDeploymentsByApplication(deployments);
    expect(clusters.map((cluster) => cluster.key)).toEqual(["checkout", "catalog"]);
    expect(clusters[0].deployments.map((item) => item.id)).toEqual(["newer", "older"]);
  });
});
