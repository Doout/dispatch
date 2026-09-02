// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DeploymentDetailsPage, DeploymentQuickView, DeploymentsPage } from "./App";
import type { Deployment, Overview } from "./api";

const deployment: Deployment = {
  id: "deployment-1",
  appId: "app-1",
  commitSha: "0123456789abcdef",
  specDigest: "sha256:dispatch-test",
  state: "succeeded",
  message: "Deployment is live",
  createdAt: "2026-08-18T12:00:00Z",
  finishedAt: "2026-08-18T12:01:00Z",
  outputs: { IMAGE_TAG: "preview-main" },
  app: {
    id: "app-1", projectId: "project-1", serverId: "server-1", name: "Checkout API", sourceRepo: "repository", branch: "main",
    buildType: "helm", contextPath: "", dockerfilePath: "", composePath: "", containerPort: 8080, domain: "", template: false, state: "ready", createdAt: "2026-08-18T11:00:00Z",
  },
  server: { id: "server-1", name: "development", address: "", runtime: "openshift", state: "ready", agentMode: "", createdAt: "2026-08-18T11:00:00Z" },
};

const overview: Overview = {
  demo: false,
  secretStorageConfigured: true,
  projects: [{ id: "project-1", name: "Platform", description: "", createdAt: "2026-08-18T11:00:00Z" }],
  servers: [deployment.server!],
  apps: [deployment.app!],
  deployments: [deployment],
  eventTriggers: [], previews: [], previewGroups: [], previewGroupRuns: [], secrets: [], githubApps: [], relayWebhooks: [],
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  window.history.replaceState({}, "", "/deployments");
});

function DeploymentList({ data = overview, selectedApplicationID, selectedStageName, onSelect = () => undefined, onSelectStage = () => undefined }: { data?: Overview; selectedApplicationID?: string; selectedStageName?: string; onSelect?: (id: string) => void; onSelectStage?: (applicationID?: string, stageName?: string) => void }) {
  return <DeploymentsPage
    overview={data}
    selectedApplicationID={selectedApplicationID}
    selectedStageName={selectedStageName}
    onSelect={onSelect}
    onSelectStage={onSelectStage}
    onOpen={() => undefined}
    onCreateApplication={() => undefined}
  />;
}

function DeploymentDetail({ logsLoading = false, logsError = "", onBack = () => undefined }: { logsLoading?: boolean; logsError?: string; onBack?: () => void }) {
  return <DeploymentDetailsPage deployment={deployment} logs={[]} logsLoading={logsLoading} logsError={logsError} onBack={onBack} onCancel={() => undefined} />;
}

function DeploymentPreview({ onClose = () => undefined, onOpenDetails = () => undefined }: { onClose?: () => void; onOpenDetails?: () => void }) {
  return <DeploymentQuickView deployment={deployment} logs={[]} logsLoading={false} logsError="" onClose={onClose} onOpenDetails={onOpenDetails} onCancel={() => undefined} />;
}

describe("deployment navigation", () => {
  it("keeps an older failure in recent runs without replacing the current state", async () => {
    const user = userEvent.setup();
    const olderFailure = { ...deployment, id: "older-failure", state: "failed", createdAt: "2026-08-18T10:00:00Z", finishedAt: "2026-08-18T10:01:00Z" } as Deployment;
    const newerSuccess = { ...deployment, id: "newer-success", createdAt: "2026-08-18T12:00:00Z", finishedAt: "2026-08-18T12:01:00Z" };
    render(<DeploymentList data={{ ...overview, deployments: [olderFailure, newerSuccess] }} />);

    const checkoutDisclosure = screen.getByRole("button", { name: "Expand Checkout API deployment" });
    expect(checkoutDisclosure.closest(".deployment-focus-heading")).not.toBeNull();
    expect(checkoutDisclosure.closest(".deployment-focus-heading")?.textContent).not.toMatch(/expand|collapse/i);
    await user.click(checkoutDisclosure);
    expect(screen.getByRole("button", { name: /Development, Ready/ })).not.toBeNull();
    expect(screen.getByRole("link", { name: "Open Checkout API Development deployment" }).getAttribute("href")).toBe("/deployments/newer-success");
    expect(screen.getAllByRole("link", { name: "Checkout API Development 01234567 deployment" })).toHaveLength(2);
    expect(screen.getAllByText("Failed").length).toBeGreaterThan(0);
  });

  it("keeps a focus rail visible for every application", () => {
    const catalog = { ...deployment, id: "catalog-latest", appId: "app-2", app: { ...deployment.app!, id: "app-2", name: "Catalog API" } };
    render(<DeploymentList data={{ ...overview, apps: [deployment.app!, catalog.app], deployments: [deployment, catalog] }} />);

    expect(screen.getByRole("heading", { name: "Checkout API" })).not.toBeNull();
    expect(screen.getByRole("heading", { name: "Catalog API" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "Expand Checkout API deployment" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "Expand Catalog API deployment" })).not.toBeNull();
    expect(screen.queryByLabelText(/promotion stages/)).toBeNull();
  });

  it("keeps every recent run available and can reveal the full list", async () => {
    const user = userEvent.setup();
    const deployments = Array.from({ length: 6 }, (_, index) => ({
      ...deployment,
      id: `deployment-${index}`,
      commitSha: `${index}123456789abcdef`,
      createdAt: `2026-08-18T1${index}:00:00Z`,
      finishedAt: `2026-08-18T1${index}:01:00Z`,
    }));
    render(<DeploymentList data={{ ...overview, deployments }} />);

    await user.click(screen.getByRole("button", { name: "Expand Checkout API deployment" }));
    const runList = screen.getByRole("list", { name: "Checkout API Development recent runs" });
    expect(runList.querySelectorAll("li")).toHaveLength(6);
    expect(runList.classList.contains("all")).toBe(false);

    await user.click(screen.getByRole("button", { name: "View all" }));
    expect(runList.classList.contains("all")).toBe(true);
    expect(screen.getByRole("button", { name: "Show less" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("opens the selected deployment from a real deep link", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const failedDeployment = { ...deployment, id: "deployment-failed", state: "failed", message: "Pre-deploy hook failed" } as Deployment;
    render(<DeploymentList data={{ ...overview, deployments: [failedDeployment] }} onSelect={onSelect} />);
    await user.click(screen.getByRole("button", { name: "Expand Checkout API deployment" }));
    const row = screen.getByRole("link", { name: "Open Checkout API Development deployment" });

    expect(row.getAttribute("href")).toBe("/deployments/deployment-failed");
    await user.click(row);
    expect(onSelect).toHaveBeenCalledWith("deployment-failed");
  });

  it("shows configured promotion stages and exposes revision drift", async () => {
    const user = userEvent.setup();
    const onSelectStage = vi.fn();
    const developmentDeployment = { ...deployment, id: "development-deployment", commitSha: "current123456789" };
    const stagingDeployment = { ...deployment, id: "staging-deployment", commitSha: "older123456789" };
    const data: Overview = {
      ...overview,
      apps: [{ ...deployment.app!, generated: true }],
      deployments: [developmentDeployment, stagingDeployment],
      workflowResources: [{ id: "workflow-1", configSourceId: "config-1", apiVersion: "dispatch/v1alpha1", kind: "Application", name: "checkout", path: "deployment/checkout.yaml", document: "", specDigest: "sha256:workflow", configSha: "config-current", active: true, state: "ready", sourceCount: 2, jobCount: 2, stageNames: ["development", "staging", "production"], targetRefs: ["dev-005", "staging", "production"], createdAt: "2026-08-18T09:00:00Z", updatedAt: "2026-08-18T12:00:00Z" }],
      workflowRevisions: [
        { id: "revision-current", resourceId: "workflow-1", configSha: "config-current", specDigest: "sha256:current", state: "succeeded", trigger: "poll", sources: { service: { alias: "service", repository: "service", branch: "main", commitSha: "current123456789" }, ui: { alias: "ui", repository: "ui", branch: "main", commitSha: "ui-current123456" } }, createdAt: "2026-08-18T12:00:00Z" },
        { id: "revision-older", resourceId: "workflow-1", configSha: "config-older", specDigest: "sha256:older", state: "succeeded", trigger: "poll", sources: { service: { alias: "service", repository: "service", branch: "main", commitSha: "older123456789" }, ui: { alias: "ui", repository: "ui", branch: "main", commitSha: "ui-older123456" } }, createdAt: "2026-08-18T10:00:00Z" },
      ],
      workflowStageRuns: [
        { id: "stage-development", revisionId: "revision-current", stageName: "development", targetRef: "dev-005", state: "succeeded", approval: "automatic", deploymentIds: ["development-deployment"], createdAt: "2026-08-18T12:00:00Z", finishedAt: "2026-08-18T12:01:00Z" },
        { id: "stage-staging", revisionId: "revision-older", stageName: "staging", targetRef: "staging", state: "succeeded", approval: "manual", deploymentIds: ["staging-deployment"], createdAt: "2026-08-18T10:00:00Z", finishedAt: "2026-08-18T10:01:00Z" },
        { id: "stage-production", revisionId: "revision-current", stageName: "production", targetRef: "production", state: "awaiting_approval", approval: "manual", createdAt: "2026-08-18T12:02:00Z" },
      ],
    };
    render(<DeploymentList data={data} onSelectStage={onSelectStage} />);

    await user.click(screen.getByRole("button", { name: "Expand checkout deployment" }));
    expect(screen.getByRole("button", { name: /Development, Ready, target dev-005/ })).not.toBeNull();
    expect(screen.getByRole("button", { name: /Production, Awaiting approval/ }).getAttribute("aria-pressed")).toBe("true");
    await user.click(screen.getByRole("button", { name: /Staging, Ready/ }));
    expect(onSelectStage).toHaveBeenCalledWith("workflow-1", "staging");
    expect(screen.getAllByText("Older revision")).not.toHaveLength(0);
    expect(screen.getByText("Behind source")).not.toBeNull();
  });

  it("restores an expanded deployment from the route selection", () => {
    render(<DeploymentList selectedApplicationID="app-1" selectedStageName="development" />);

    expect(screen.getByRole("button", { name: "Collapse Checkout API deployment" }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByLabelText("Checkout API promotion stages")).not.toBeNull();
  });

  it("opens deployment evidence in a dialog with a full-page action", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const onOpenDetails = vi.fn();
    render(<DeploymentPreview onClose={onClose} onOpenDetails={onOpenDetails} />);

    expect(screen.getByRole("dialog", { name: "Checkout API" })).not.toBeNull();
    expect(screen.getByRole("log")).not.toBeNull();
    expect(screen.getByText("Deployment log").closest("details")).toBeNull();
    expect(screen.getByText("Deployment log").closest(".evidence-log-fixed")).not.toBeNull();
    const openPage = screen.getByRole("link", { name: "Open deployment details page" });
    expect(openPage.getAttribute("href")).toBe("/deployments/deployment-1");
    await user.click(openPage);
    expect(onOpenDetails).toHaveBeenCalledOnce();

    await user.click(screen.getByRole("button", { name: "Close deployment preview" }));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("renders deployment evidence as its own page with a back action", async () => {
    const user = userEvent.setup();
    const onBack = vi.fn();
    render(<DeploymentDetail onBack={onBack} />);

    expect(screen.getByRole("heading", { name: "Deployment details", level: 1 })).not.toBeNull();
    expect(screen.getByRole("heading", { name: "Checkout API" })).not.toBeNull();
    expect(screen.queryByRole("button", { name: /Expand deployment details/ })).toBeNull();
    const back = screen.getByRole("link", { name: "Back to deployments" });
    expect(back.getAttribute("href")).toBe("/deployments");
    await user.click(back);
    expect(onBack).toHaveBeenCalledOnce();
  });

  it("shows an explicit log loading state", async () => {
    const user = userEvent.setup();
    render(<DeploymentDetail logsLoading />);
    await user.click(screen.getByText("Deployment log"));

    expect(await screen.findByText("Loading log entries...")).not.toBeNull();
    expect(screen.getByRole("log").getAttribute("aria-busy")).toBe("true");
  });

  it("distinguishes a log fetch failure from an empty log", async () => {
    const user = userEvent.setup();
    render(<DeploymentDetail logsError="network unavailable" />);
    expect(await screen.findByText("Unavailable")).not.toBeNull();
    await user.click(screen.getByText("Deployment log"));
    expect(await screen.findByText("Dispatch could not load logs. Retrying automatically.")).not.toBeNull();
  });
});
