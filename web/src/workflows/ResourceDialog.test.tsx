// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Overview, type WorkflowJobResult, type WorkflowResource } from "../api";
import { WorkflowResourceDialog } from "./ResourceDialog";

const resource: WorkflowResource = {
  id: "resource-1",
  configSourceId: "source-1",
  apiVersion: "dispatch/v1alpha1",
  kind: "Application",
  name: "checkout",
  path: "deployments/checkout.yaml",
  document: "apiVersion: dispatch/v1alpha1\nkind: Application\n",
  specDigest: "sha256:test",
  configSha: "abc123",
  active: true,
  state: "ready",
  sourceCount: 2,
  jobCount: 2,
  createdAt: "2026-09-01T20:00:00Z",
  updatedAt: "2026-09-01T20:00:00Z",
};

const overview = {
  demo: false,
  secretStorageConfigured: true,
  identity: { id: "owner-1", username: "owner", displayName: "Owner", systemRole: "owner", permissions: [] },
  projects: [{ id: "project-1", name: "Platform", description: "", createdAt: "2026-09-01T20:00:00Z" }],
  servers: [],
  apps: [],
  deployments: [],
  eventTriggers: [],
  previews: [],
  previewGroups: [],
  previewGroupRuns: [],
  secrets: [],
  githubApps: [],
  relayWebhooks: [],
  configSources: [{ id: "source-1", projectId: "project-1", name: "Platform deployments", repository: "platform/deployments", branch: "main", path: "deployments", syncMode: "poll", pollIntervalSeconds: 60, active: true, state: "ready", createdAt: "2026-09-01T20:00:00Z", updatedAt: "2026-09-01T20:00:00Z" }],
  workflowRevisions: [{ id: "revision-1", resourceId: "resource-1", configSha: "abc123", specDigest: "sha256:test", state: "failed", trigger: "poll", sources: {}, error: "job build-api: command failed", createdAt: "2026-09-01T20:01:00Z" }],
} as Overview;

function job(values: Partial<WorkflowJobResult>): WorkflowJobResult {
  return {
    id: "job-1",
    resourceId: "resource-1",
    revisionId: "revision-1",
    jobName: "build-api",
    fingerprint: "sha256:job",
    state: "failed",
    sources: {},
    createdAt: "2026-09-01T20:01:00Z",
    ...values,
  };
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("workflow resource run details", () => {
  it("shows the latest unchanged source check without adding it to run history", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    const evaluated = { ...resource, lastEvaluation: { resourceId: resource.id, baselineRevisionId: "revision-1", checkedAt: "2026-09-02T20:00:00Z", sources: { chart: { alias: "chart", repository: "platform/charts", branch: "main", commitSha: "fedcba9876543210" } }, results: [] } };
    render(<WorkflowResourceDialog resource={evaluated} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    expect(screen.getByText("No deployment changes")).not.toBeNull();
    await userEvent.click(screen.getByText("No deployment changes"));
    expect(screen.getByText("fedcba987654")).not.toBeNull();
    expect(screen.getByRole("combobox", { name: "Workflow run" }).querySelectorAll("option")).toHaveLength(1);
  });

  it("links an unchanged stage result to its retained deployment", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([{ id: "stage", revisionId: "revision-1", stageName: "development", targetRef: "dev", state: "succeeded", approval: "automatic", createdAt: resource.createdAt, deploymentIds: ["retained-release"], deploymentResults: [{ deploymentName: "web", appId: "app", deploymentId: "retained-release", outcome: "unchanged", reason: "Rendered resources are unchanged.", checkedAt: resource.updatedAt }] }]);
    const open = vi.fn();
    render(<WorkflowResourceDialog resource={resource} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={open} />);
    await userEvent.click(await screen.findByRole("button", { name: /Unchanged · Manifests/ }));
    expect(open).toHaveBeenCalledWith("retained-release");
  });

  it("opens stage deployment logs and reports a stage failure", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([{ id: "stage", revisionId: "revision-1", stageName: "development", targetRef: "dev", state: "failed", approval: "automatic", createdAt: resource.createdAt, deploymentIds: ["release"], error: "Helm readiness timed out" }]);
    vi.spyOn(api, "deployment").mockResolvedValue({ id: "release", appId: "app", commitSha: "abc", specDigest: "", state: "failed", message: "Waiting for available replicas", createdAt: resource.createdAt });
    vi.spyOn(api, "logs").mockResolvedValue([{ id: 1, deploymentId: "release", level: "info", message: "Upgrading chart", createdAt: resource.createdAt }]);
    render(<WorkflowResourceDialog resource={resource} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    await userEvent.click(await screen.findByRole("button", { name: "View progress & logs" }));
    expect(await screen.findByText(/Upgrading chart/)).not.toBeNull();
    expect(screen.getByText("Waiting for available replicas")).not.toBeNull();
    expect(screen.getByRole("alert").textContent).toBe("Helm readiness timed out");
  });

  it("refreshes a running stage when a deployment becomes available", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    const stage = { id: "stage", revisionId: "revision-1", stageName: "development", targetRef: "dev", state: "running", approval: "automatic", createdAt: resource.createdAt };
    vi.spyOn(api, "workflowStages").mockResolvedValueOnce([stage]).mockResolvedValue([{ ...stage, deploymentIds: ["release"] }]);
    vi.spyOn(api, "deployment").mockResolvedValue({ id: "release", appId: "app", commitSha: "abc", specDigest: "", state: "starting", message: "Installing release", createdAt: resource.createdAt });
    vi.spyOn(api, "logs").mockResolvedValue([{ id: 1, deploymentId: "release", level: "info", message: "Helm install started", createdAt: resource.createdAt }]);
    render(<WorkflowResourceDialog resource={resource} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    await userEvent.click(await screen.findByRole("button", { name: "View progress & logs" }));
    expect(await screen.findByText(/Preparing deployments/)).not.toBeNull();
    await waitFor(() => expect(screen.getByText(/Helm install started/)).not.toBeNull(), { timeout: 4500 });
  });

  it("opens the failed job output and lets operators inspect another job", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([
      job({ log: "Installing dependencies\nauthorization denied\n", error: "command failed: exit status 1" }),
      job({ id: "job-2", jobName: "build-ui", state: "succeeded", log: "Image pushed\n", error: undefined }),
    ]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);

    render(<WorkflowResourceDialog resource={resource} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);

    expect(await screen.findByRole("region", { name: "build-api job output" })).not.toBeNull();
    expect(screen.getByText(/authorization denied/)).not.toBeNull();
    expect(screen.getByText("command failed: exit status 1")).not.toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "build-ui, succeeded" }));
    expect(screen.getByRole("region", { name: "build-ui job output" })).not.toBeNull();
    expect(screen.getByText(/Image pushed/)).not.toBeNull();
    expect(screen.queryByText(/authorization denied/)).toBeNull();
  });
});
