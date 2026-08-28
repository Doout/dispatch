// @vitest-environment jsdom

import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApplicationsPage } from "./ApplicationsPage";
import { api, type App, type Overview } from "./api";

const application = (values: Partial<App>): App => ({
  id: "app-1",
  projectId: "project-1",
  serverId: "docker-1",
  name: "Checkout API",
  sourceRepo: "https://example.com/platform/checkout.git",
  branch: "main",
  buildType: "dockerfile",
  contextPath: ".",
  dockerfilePath: "Dockerfile",
  composePath: "",
  containerPort: 8080,
  domain: "",
  template: false,
  state: "ready",
  createdAt: "2026-08-18T11:00:00Z",
  ...values,
});

const overview: Overview = {
  demo: false,
  secretStorageConfigured: true,
  projects: [{ id: "project-1", name: "Platform", description: "", createdAt: "2026-08-18T11:00:00Z" }],
  servers: [
    { id: "docker-1", name: "build-01", address: "", runtime: "docker", state: "ready", agentMode: "", createdAt: "2026-08-18T11:00:00Z" },
    { id: "cluster-1", name: "development", address: "", runtime: "openshift", state: "ready", agentMode: "", createdAt: "2026-08-18T11:00:00Z" },
  ],
  apps: [
    application({}),
    application({ id: "helm-1", serverId: "cluster-1", name: "Storefront preview", sourceRepo: "git@example.com:platform/deployments.git", buildType: "helm", helmChart: "charts/storefront" }),
    application({ id: "template-1", name: "Worker template", template: true }),
  ],
  deployments: [],
  eventTriggers: [],
  previews: [],
  previewGroups: [{
    id: "group-1",
    name: "Checkout preview",
    githubAppId: "github-1",
    command: "/preview",
    enabled: true,
    components: [{ appId: "helm-1", alias: "checkout", repository: "platform/checkout", defaultBranch: "main", entrypoint: true, dependsOn: [], bindings: [], secretIds: [] }],
    createdAt: "2026-08-18T11:00:00Z",
    updatedAt: "2026-08-18T11:00:00Z",
  }],
  previewGroupRuns: [],
  secrets: [],
  githubApps: [],
  relayWebhooks: [],
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("applications overview", () => {
  it("opens topology through client navigation without changing the hook order", async () => {
    const configured: Overview = {
      ...overview,
      configSources: [{ id: "config-1", projectId: "project-1", githubAppId: "github-1", name: "Platform config", repository: "platform/deployments", branch: "main", path: ".dispatch", syncMode: "poll", pollIntervalSeconds: 60, active: true, state: "ready", createdAt: "2026-08-27T01:00:00Z", updatedAt: "2026-08-27T01:00:00Z" }],
      workflowResources: [{ id: "workflow-1", configSourceId: "config-1", apiVersion: "dispatch/v1alpha1", kind: "Application", name: "checkout", path: ".dispatch/checkout.yaml", document: "", specDigest: "sha256:test", configSha: "abc123", active: true, state: "ready", sourceCount: 1, jobCount: 1, stageNames: ["development"], targetRefs: ["development"], createdAt: "2026-08-27T01:00:00Z", updatedAt: "2026-08-27T01:00:00Z" }],
    };
    vi.spyOn(api, "workflowTopology").mockResolvedValue({ columns: [{ id: "source", label: "Sources" }], nodes: [{ id: "source:checkout", column: "source", kind: "source", label: "checkout" }], edges: [] });
    const props = { overview: configured, section: "applications" as const, creating: false, onToggleCreate: () => undefined, onChanged: async () => undefined, onDeploy: () => undefined, onDelete: () => undefined, onDeleteGroup: () => undefined, onNavigate: () => undefined };
    const view = render(<ApplicationsPage {...props} />);

    expect(screen.getByRole("table")).not.toBeNull();
    view.rerender(<ApplicationsPage {...props} applicationID="workflow-1" />);

    expect(await screen.findByRole("heading", { name: "checkout" })).not.toBeNull();
    expect(await screen.findByLabelText("Application topology")).not.toBeNull();
  });

  it("shows every configured application resource without a category switcher", () => {
    render(<ApplicationsPage overview={overview} section="applications" creating={false} onToggleCreate={() => undefined} onChanged={async () => undefined} onDeploy={() => undefined} onDelete={() => undefined} onDeleteGroup={() => undefined} onNavigate={() => undefined} />);

    expect(screen.queryByRole("navigation", { name: "Application resources" })).toBeNull();
    const table = screen.getByRole("table");
    expect(screen.getAllByRole("table")).toHaveLength(1);
    expect(screen.getByText("Checkout API")).not.toBeNull();
    expect(screen.getByText("Storefront preview")).not.toBeNull();
    expect(screen.getByText("Worker template")).not.toBeNull();
    expect(screen.getByText("Checkout preview")).not.toBeNull();
    expect(screen.getByRole("columnheader", { name: "Name" })).not.toBeNull();
    expect(screen.getByRole("columnheader", { name: "Type" })).not.toBeNull();
    expect(within(table).getByText("Helm source")).not.toBeNull();
    expect(within(table).getByText("Preview group")).not.toBeNull();
    expect(screen.queryByText("source.example.com")).toBeNull();
  });

  it("uses one page-level action to start another application", async () => {
    const user = userEvent.setup();
    const onToggleCreate = vi.fn();
    render(<ApplicationsPage overview={overview} section="applications" creating={false} onToggleCreate={onToggleCreate} onChanged={async () => undefined} onDeploy={() => undefined} onDelete={() => undefined} onDeleteGroup={() => undefined} onNavigate={() => undefined} />);

    await user.click(screen.getByText("Add"));
    await user.click(screen.getByRole("menuitem", { name: "Application" }));
    expect(onToggleCreate).toHaveBeenCalledOnce();
  });

  it("allows repository imports with a saved SSH key", async () => {
    const user = userEvent.setup();
    const configured = {
      ...overview,
      secrets: [{ id: "ssh-1", name: "Repository key", type: "ssh_private_key" as const, environmentVariable: "SSH_PRIVATE_KEY", createdAt: "2026-08-27T01:00:00Z", updatedAt: "2026-08-27T01:00:00Z" }],
    };
    render(<ApplicationsPage overview={configured} section="applications" creating={false} onToggleCreate={() => undefined} onChanged={async () => undefined} onDeploy={() => undefined} onDelete={() => undefined} onDeleteGroup={() => undefined} onNavigate={() => undefined} />);

    await user.click(screen.getByText("Add"));
    await user.click(screen.getByRole("menuitem", { name: "Repository configuration" }));

    expect(screen.getByRole("heading", { name: "Import configuration" })).not.toBeNull();
    expect(screen.getByLabelText("Repository access")).not.toBeNull();
  });

  it("keeps resource actions in a three-dot menu", async () => {
    const user = userEvent.setup();
    const onDeploy = vi.fn();
    render(<ApplicationsPage overview={overview} section="applications" creating={false} onToggleCreate={() => undefined} onChanged={async () => undefined} onDeploy={onDeploy} onDelete={() => undefined} onDeleteGroup={() => undefined} onNavigate={() => undefined} />);

    expect(screen.queryByRole("button", { name: "Deploy" })).toBeNull();
    const options = screen.getByLabelText("Options for Storefront preview");
    await user.click(options);
    const menu = await screen.findByRole("menu");
    expect(menu.closest(".resource-table-wrap")).toBeNull();
    await user.click(within(menu).getByRole("menuitem", { name: "Deploy" }));
    expect(onDeploy).toHaveBeenCalledWith("helm-1");
  });

  it("shows an imported application once and keeps its source controls in the row menu", async () => {
    const user = userEvent.setup();
    const configured: Overview = {
      ...overview,
      configSources: [{ id: "config-1", projectId: "project-1", githubAppId: "github-1", name: "Platform config", repository: "platform/deployments", branch: "main", path: ".dispatch", syncMode: "webhook_poll", pollIntervalSeconds: 300, active: true, state: "ready", createdAt: "2026-08-27T01:00:00Z", updatedAt: "2026-08-27T01:00:00Z" }],
      workflowResources: [{ id: "workflow-1", configSourceId: "config-1", apiVersion: "dispatch/v1alpha1", kind: "Application", name: "checkout", path: ".dispatch/checkout.yaml", document: "apiVersion: dispatch/v1alpha1\nkind: Application\n", specDigest: "sha256:test", configSha: "abc123", active: false, state: "paused", sourceCount: 2, jobCount: 2, targetRefs: ["development"], stageNames: ["development"], createdAt: "2026-08-27T01:00:00Z", updatedAt: "2026-08-27T01:00:00Z" }],
      workflowRevisions: [],
      workflowStageRuns: [],
    };
    render(<ApplicationsPage overview={configured} section="applications" creating={false} onToggleCreate={() => undefined} onChanged={async () => undefined} onDeploy={() => undefined} onDelete={() => undefined} onDeleteGroup={() => undefined} onNavigate={() => undefined} />);

    expect(screen.queryByText("Platform config")).toBeNull();
    const name = screen.getByRole("button", { name: "Open checkout" });
    expect(screen.getByText("Webhook + poll")).not.toBeNull();
    await user.click(screen.getByLabelText("Options for checkout"));
    const menu = await screen.findByRole("menu");
    expect(within(menu).queryByRole("menuitem", { name: "View" })).toBeNull();
    expect(within(menu).getByRole("menuitem", { name: "Sync configuration" })).not.toBeNull();
    expect(within(menu).getByRole("menuitem", { name: "Edit configuration" })).not.toBeNull();
    await user.keyboard("{Escape}");
    await user.click(name);
    expect(screen.getByRole("dialog", { name: "checkout" })).not.toBeNull();
    expect(screen.getByText("No runs.")).not.toBeNull();
  });

  it("opens an imported application by double-clicking its row", async () => {
    const user = userEvent.setup();
    const configured: Overview = {
      ...overview,
      configSources: [{ id: "config-1", projectId: "project-1", githubAppId: "github-1", name: "Platform config", repository: "platform/deployments", branch: "main", path: ".dispatch", syncMode: "poll", pollIntervalSeconds: 60, active: true, state: "ready", createdAt: "2026-08-27T01:00:00Z", updatedAt: "2026-08-27T01:00:00Z" }],
      workflowResources: [{ id: "workflow-1", configSourceId: "config-1", apiVersion: "dispatch/v1alpha1", kind: "Application", name: "checkout", path: ".dispatch/checkout.yaml", document: "apiVersion: dispatch/v1alpha1\nkind: Application\n", specDigest: "sha256:test", configSha: "abc123", active: true, state: "ready", sourceCount: 2, jobCount: 2, targetRefs: ["development"], stageNames: ["development"], createdAt: "2026-08-27T01:00:00Z", updatedAt: "2026-08-27T01:00:00Z" }],
      workflowRevisions: [],
      workflowStageRuns: [],
    };
    render(<ApplicationsPage overview={configured} section="applications" creating={false} onToggleCreate={() => undefined} onChanged={async () => undefined} onDeploy={() => undefined} onDelete={() => undefined} onDeleteGroup={() => undefined} onNavigate={() => undefined} />);

    const source = screen.getByText("platform/deployments");
    const secondMouseDown = new MouseEvent("mousedown", { bubbles: true, cancelable: true, detail: 2 });
    source.dispatchEvent(secondMouseDown);
    expect(secondMouseDown.defaultPrevented).toBe(true);
    await user.dblClick(source);
    expect(screen.getByRole("dialog", { name: "checkout" })).not.toBeNull();
  });
});
