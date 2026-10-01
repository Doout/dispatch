// @vitest-environment jsdom

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { App, Deployment, Overview } from "../api";
import { DeploymentCatalogProvider } from "./DeploymentCatalog";
import { DeploymentFocusRails } from "./DeploymentFocusRail";
import { catalogClient, type CatalogItem, type CatalogStatus } from "./catalogClient";

const application: App = {
  id: "checkout", name: "Checkout API", projectId: "platform", serverId: "development",
  sourceRepo: "example/checkout", branch: "main", buildType: "helm", contextPath: "",
  dockerfilePath: "", composePath: "", containerPort: 8080, domain: "", template: false,
  state: "ready", createdAt: "2026-09-01T12:00:00Z",
};
const deployment: Deployment = {
  id: "latest", appId: application.id, commitSha: "bd1d39e2abcdef12", specDigest: "sha256:checkout",
  state: "succeeded", message: "Deployment is live", createdAt: "2026-09-30T12:00:00Z",
};
const overview: Overview = {
  demo: false, secretStorageConfigured: true, projects: [], apps: [application], deployments: [deployment],
  servers: [{ id: "development", name: "development-cluster", runtime: "kubernetes", state: "ready", address: "", agentMode: "", createdAt: "" }],
  eventTriggers: [], previews: [], previewGroups: [], previewGroupRuns: [], secrets: [], githubApps: [], relayWebhooks: [],
};

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

async function renderRow(status: Partial<CatalogStatus> = {}, state: Deployment["state"] = "succeeded") {
  const latest = { ...deployment, state };
  const sync: CatalogStatus = {
    configuration: "synced", revision: "current", drift: "synced", health: "healthy", supported: true,
    checkedAt: new Date().toISOString(), message: "Observed runtime", ...status,
  };
  const item: CatalogItem = {
    appId: application.id, appName: application.name, projectId: application.projectId,
    targetId: "development", targetName: "development-cluster", environment: "development",
    current: state === "succeeded" ? latest : { ...deployment, id: "running" }, latest, sync,
  };
  vi.spyOn(catalogClient, "catalog").mockResolvedValue({ items: [item] });
  const onSelectDeployment = vi.fn();
  render(<DeploymentCatalogProvider overview={overview}><DeploymentFocusRails overview={{ ...overview, deployments: [latest] }} onSelectDeployment={onSelectDeployment} /></DeploymentCatalogProvider>);
  const row = screen.getByRole("article", { name: application.name });
  const header = row.querySelector(".deployment-focus-heading")!;
  await waitFor(() => expect(header.querySelector(".deployment-summary-status")?.getAttribute("title")).toContain("Observed runtime"));
  return { row, header, onSelectDeployment };
}

describe("deployment row summaries", () => {
  it("keeps targets and running-release navigation in the expanded multi-environment details", async () => {
    const development = { ...deployment, id: "development", appId: "checkout-development" };
    const staging = { ...deployment, id: "staging", appId: "checkout-staging", state: "checking" as const };
    const running = { ...staging, id: "running", state: "succeeded" as const };
    const data: Overview = {
      ...overview, apps: [], deployments: [development, staging, running],
      workflowResources: [{ id: "workflow", configSourceId: "config", apiVersion: "dispatch/v1alpha1", kind: "Application", name: "Checkout API", path: "checkout.yaml", document: "", specDigest: "sha256:checkout", configSha: "config", active: true, state: "ready", sourceCount: 1, jobCount: 1, stageNames: ["development", "staging", "production"], targetRefs: ["development-cluster", "staging-cluster", "production-cluster"], createdAt: "", updatedAt: "" }],
      workflowRevisions: [{ id: "revision", resourceId: "workflow", configSha: "config", specDigest: "sha256:checkout", state: "succeeded", trigger: "poll", sources: { api: { alias: "api", repository: "example/api", branch: "main", commitSha: deployment.commitSha } }, createdAt: deployment.createdAt }],
      workflowStageRuns: [
        { id: "dev", revisionId: "revision", stageName: "development", targetRef: "development-cluster", state: "succeeded", approval: "automatic", deploymentIds: [development.id], createdAt: deployment.createdAt },
        { id: "stage", revisionId: "revision", stageName: "staging", targetRef: "staging-cluster", state: "checking", approval: "automatic", deploymentIds: [staging.id], createdAt: deployment.createdAt },
        { id: "prod", revisionId: "revision", stageName: "production", targetRef: "production-cluster", state: "awaiting_approval", approval: "manual", deploymentIds: [], createdAt: deployment.createdAt },
      ],
    };
    vi.spyOn(catalogClient, "catalog").mockResolvedValue({ items: [{ appId: staging.appId, appName: application.name, projectId: application.projectId, resourceId: "workflow", environment: "staging", targetId: "cluster", targetName: "staging-cluster", current: running, latest: staging, sync: { configuration: "synced", revision: "current", drift: "synced", health: "healthy", supported: true, checkedAt: new Date().toISOString(), message: "Existing release is running" } }] });
    const onSelectDeployment = vi.fn();
    render(<DeploymentCatalogProvider overview={data}><DeploymentFocusRails overview={data} onSelectDeployment={onSelectDeployment} /></DeploymentCatalogProvider>);
    const row = screen.getByRole("article", { name: "Checkout API" });
    const header = row.querySelector(".deployment-focus-heading") as HTMLElement;
    await waitFor(() => expect(header.querySelector(".deployment-summary-status.active")?.getAttribute("title")).toContain("Existing release is running"));
    expect(header.textContent).toContain("Checking");
    expect(header.textContent).toContain("Awaiting approval");
    expect(header.textContent).not.toMatch(/development-cluster|staging-cluster|production-cluster/);
    expect(within(header).queryByRole("link", { name: "Running release" })).toBeNull();
    expect(within(header).getByRole("link", { name: "Open Checkout API Staging latest deployment" }).getAttribute("href")).toBe("/deployments/staging");

    await userEvent.setup().click(screen.getByRole("button", { name: "Expand Checkout API deployment" }));
    const details = screen.getByRole("region", { name: "Checkout API Staging deployment details" });
    expect(within(details).getByText("staging-cluster")).toBeTruthy();
    const link = within(details).getByRole("link", { name: "Open Checkout API Staging running release" });
    expect(link.getAttribute("href")).toBe("/deployments/running");
    await userEvent.setup().click(link);
    expect(onSelectDeployment).toHaveBeenCalledWith("running");
  });

  it("shows one high-level status and keeps revisions and diagnostics in details", async () => {
    const { row, header } = await renderRow();
    expect(within(header as HTMLElement).getAllByText("Ready")).toHaveLength(1);
    expect(header.textContent).toContain("development-cluster");
    expect(header.textContent).not.toContain("bd1d39e2");
    expect(header.textContent).not.toMatch(/Source|Sync:|Drift:|Health:|1\/1/);

    await userEvent.setup().click(screen.getByRole("button", { name: "Expand Checkout API deployment" }));
    expect(within(row).getByText("Source revision")).toBeTruthy();
    expect(within(row).getByText("Deployed revision")).toBeTruthy();
    expect(within(row).getByText("Sync: Synced")).toBeTruthy();
    expect(within(row).getByText("Drift: Synced")).toBeTruthy();
    expect(within(row).getByText("Health: Healthy")).toBeTruthy();
    expect(row.textContent).toContain("bd1d39e2");
  });

  it.each([
    { status: { drift: "out_of_sync" }, label: "Drift detected", tone: "danger" },
    { status: { health: "unhealthy", drift: "out_of_sync" }, label: "Unhealthy", tone: "danger" },
    { status: { configuration: "invalid", configurationMessage: "Branch not found" }, label: "Configuration error", tone: "danger" },
    { status: { checkedAt: new Date(Date.now() - 3600_000).toISOString() }, label: "Check overdue", tone: "warning" },
    { status: { checkedAt: undefined }, label: "Not checked", tone: "muted" },
    { status: { health: "unknown" }, label: "Status unknown", tone: "muted" },
    { status: { supported: false, checkedAt: undefined, health: "unknown", drift: "unknown" }, label: "Ready", tone: "success" },
  ])("summarizes $label without a row of diagnostic badges", async ({ status, label, tone }) => {
    const { header } = await renderRow(status);
    const summary = header.querySelector(".deployment-summary-status")!;
    expect(summary.textContent).toBe(label);
    expect(summary.classList.contains(tone)).toBe(true);
    expect(header.querySelectorAll(".deployment-summary-status")).toHaveLength(1);
    expect(header.querySelector(".catalog-status-group")).toBeNull();
  });

  it.each(["building", "failed"] as const)("keeps a %s attempt separate from the running release", async state => {
    const { header, onSelectDeployment } = await renderRow({ drift: "out_of_sync" }, state);
    expect(header.querySelector(".deployment-summary-status")?.textContent).toBe(state === "building" ? "Building" : "Failed");
    const running = within(header as HTMLElement).getByRole("link", { name: "Running release" });
    expect(running.getAttribute("href")).toBe("/deployments/running");
    await userEvent.setup().click(running);
    expect(onSelectDeployment).toHaveBeenCalledWith("running");
  });
});
