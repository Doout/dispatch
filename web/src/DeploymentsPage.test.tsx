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

function DeploymentList({ data = overview, onSelect = () => undefined }: { data?: Overview; onSelect?: (id: string) => void }) {
  return <DeploymentsPage
    overview={data}
    onSelect={onSelect}
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
  it("shows a failure behind a newer success only in previous deployments", async () => {
    const user = userEvent.setup();
    const olderFailure = { ...deployment, id: "older-failure", state: "failed", createdAt: "2026-08-18T10:00:00Z", finishedAt: "2026-08-18T10:01:00Z" } as Deployment;
    const newerSuccess = { ...deployment, id: "newer-success", createdAt: "2026-08-18T12:00:00Z", finishedAt: "2026-08-18T12:01:00Z" };
    render(<DeploymentList data={{ ...overview, deployments: [olderFailure, newerSuccess] }} />);

    expect(screen.queryByRole("heading", { name: "Needs attention" })).toBeNull();
    expect(screen.getByRole("heading", { name: "Latest deployments" })).not.toBeNull();
    expect(screen.getByText("1 across 1 application")).not.toBeNull();
    await user.click(screen.getByText("Previous deployments"));
    expect(screen.getByRole("link", { name: /Checkout API, failed/ })).not.toBeNull();
  });

  it("keeps the latest deployment for every application visible", () => {
    const catalog = { ...deployment, id: "catalog-latest", appId: "app-2", app: { ...deployment.app!, id: "app-2", name: "Catalog API" } };
    render(<DeploymentList data={{ ...overview, deployments: [deployment, catalog] }} />);

    expect(screen.getByRole("heading", { name: "Latest deployments" })).not.toBeNull();
    expect(screen.getByRole("link", { name: /Checkout API, succeeded/ })).not.toBeNull();
    expect(screen.getByRole("link", { name: /Catalog API, succeeded/ })).not.toBeNull();
    expect(screen.queryByText("Previous deployments")).toBeNull();
  });

  it("exposes every deployment as a deep link", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const failedDeployment = { ...deployment, id: "deployment-failed", state: "failed", message: "Pre-deploy hook failed" } as Deployment;
    render(<DeploymentList data={{ ...overview, deployments: [failedDeployment] }} onSelect={onSelect} />);
    const row = screen.getByRole("link", { name: /Checkout API, failed/ });

    expect(row.getAttribute("href")).toBe("/deployments/deployment-failed");
    await user.click(row);
    expect(onSelect).toHaveBeenCalledWith("deployment-failed");
  });

  it("restores expanded deployment history after returning from a deployment", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const earlierDeployment = { ...deployment, id: "deployment-earlier", createdAt: "2026-08-18T10:00:00Z", finishedAt: "2026-08-18T10:01:00Z" };
    const oldestDeployment = { ...deployment, id: "deployment-oldest", createdAt: "2026-08-18T09:00:00Z", finishedAt: "2026-08-18T09:01:00Z" };
    const data = { ...overview, deployments: [oldestDeployment, earlierDeployment, deployment] };
    const view = render(<DeploymentList data={data} onSelect={onSelect} />);

    await user.click(screen.getByText("Previous deployments"));
    await user.click(screen.getByText("Show 1 earlier attempt"));
    await user.click(screen.getAllByRole("link", { name: /Checkout API, succeeded/ })[0]);

    expect(onSelect).toHaveBeenCalled();
    expect(window.history.state.deploymentHistoryOpen).toBe(true);
    expect(window.history.state.deploymentAttemptClusters).toContain("previous deployments:app-1");

    view.unmount();
    render(<DeploymentList data={data} />);

    expect(screen.getByText("Previous deployments").closest("details")?.open).toBe(true);
    expect(screen.getByText("Show 1 earlier attempt").closest("details")?.open).toBe(true);
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
