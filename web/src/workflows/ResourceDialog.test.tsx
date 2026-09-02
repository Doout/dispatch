// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
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
