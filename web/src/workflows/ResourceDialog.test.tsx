// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

beforeEach(() => {
  vi.spyOn(api, "workflowRevision").mockResolvedValue(overview.workflowRevisions![0]);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("workflow resource run details", () => {
  it("refreshes reporting for a terminal run without legacy feedback", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    vi.mocked(api.workflowRevision).mockResolvedValue({ ...overview.workflowRevisions![0], checks: [{ id: "report", revisionId: "revision-1", resourceId: resource.id, projectId: "project-1", githubAppId: "app", repository: "example/service", commitSha: "a".repeat(40), name: "Dispatch/deployment/checkout", kind: "deployment", externalId: "dispatch-check:report", state: "retrying", attempts: 1, complete: false, updatedAt: resource.createdAt, error: "Grant Checks: write to report this result." }] });
    render(<WorkflowResourceDialog resource={resource} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    expect(await screen.findByText("Grant Checks: write to report this result.")).toBeTruthy();
    expect(screen.getByText("Retry pending")).toBeTruthy();
    expect(api.workflowRevision).toHaveBeenCalledWith("revision-1");
  });

  it("shows phase durations, reuse, and the slowest Docker step", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([
      job({ state: "succeeded", startedAt: "2026-09-01T20:01:00Z", finishedAt: "2026-09-01T20:10:00Z", log: "#4 [build 1/2] RUN install\n#4 DONE 180.0s\n#5 [build 2/2] COPY src .\n#5 CACHED\n" }),
      job({ id: "job-2", jobName: "build-ui", state: "succeeded", startedAt: "2026-09-01T20:01:00Z", finishedAt: "2026-09-01T20:01:00Z", reusedFromId: "prior-job" }),
    ]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([{ id: "stage", revisionId: "revision-1", stageName: "development", targetRef: "dev", state: "succeeded", approval: "automatic", createdAt: resource.createdAt, startedAt: "2026-09-01T20:10:00Z", finishedAt: "2026-09-01T20:11:00Z" }]);
    const timed = { ...overview, workflowRevisions: [{ ...overview.workflowRevisions![0], state: "succeeded", startedAt: "2026-09-01T20:01:00Z", finishedAt: "2026-09-01T20:11:00Z" }] } as Overview;
    vi.mocked(api.workflowRevision).mockResolvedValue(timed.workflowRevisions![0]);
    render(<WorkflowResourceDialog resource={resource} overview={timed} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    const timing = await screen.findByRole("region", { name: "Run timing" });
    await waitFor(() => expect(timing.textContent).toContain("9m"));
    expect(timing.textContent).toContain("10m");
    expect(timing.textContent).toContain("1m");
    expect(timing.textContent).toContain("1 of 2 job results reused");
    expect(timing.textContent).toContain("1 Docker build steps cached");
    await userEvent.click(screen.getByText("Slowest Docker steps · 1 cached"));
    expect(screen.getByText("build 1/2 · RUN install")).not.toBeNull();
    expect(screen.getByText("3m")).not.toBeNull();
  });

  it("does not query owner-only preview settings for a project viewer", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    const triggers = vi.spyOn(api, "workflowPreviewTriggers");
    const viewer = { ...overview, identity: { ...overview.identity!, systemRole: "member" as const } };
    render(<WorkflowResourceDialog resource={{ ...resource, temporary: true }} overview={viewer} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    await waitFor(() => expect(api.workflowStages).toHaveBeenCalled());
    expect(triggers).not.toHaveBeenCalled();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows automatic health and comment-only E2E tests without opening stage logs", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([{ id: "stage", revisionId: "revision-1", stageName: "development", targetRef: "dev", state: "succeeded", approval: "automatic", createdAt: resource.createdAt, checkRuns: { health: "health-run" } }]);
    vi.spyOn(api, "workflowRevision").mockResolvedValue({ id: "health-run", resourceId: "health-pipeline", configSha: "abc123", specDigest: "health", state: "succeeded", trigger: "checkout/development/health", sources: {}, createdAt: resource.createdAt });
    vi.spyOn(api, "workflowPreviewTriggers").mockResolvedValue([{ id: "trigger", resourceId: resource.id, githubAppId: "app", repository: "example/service", pullRequestNumber: 42, command: "/ship", createdAt: resource.createdAt }]);
    const start = vi.spyOn(api, "runWorkflowResource");
    const preview = { ...resource, temporary: true, document: "spec:\n  stages:\n    - name: development\n      checks:\n        health: {pipelineRef: readiness}\n        e2e: {pipelineRef: full-e2e, when: onDemand}\n" };
    render(<WorkflowResourceDialog resource={preview} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    expect(await screen.findByRole("region", { name: "development tests" })).not.toBeNull();
    expect(await screen.findByText("/ship test")).not.toBeNull();
    expect(screen.getByText("health")).not.toBeNull();
    expect(screen.getByText("e2e")).not.toBeNull();
    expect(screen.getByRole("button", { name: "View health test logs" })).not.toBeNull();
    expect(start).not.toHaveBeenCalled();
  });

  it("keeps the deployment healthy when an on-demand check fails", () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    const withChecks = { ...overview, workflowRevisions: [
      { id: "check", resourceId: resource.id, configSha: "abc123", specDigest: "sha256:test", state: "failed", trigger: "pull request test 17", sources: {}, createdAt: "2026-09-02T20:01:00Z" },
      { id: "deployment", resourceId: resource.id, configSha: "abc123", specDigest: "sha256:test", state: "succeeded", trigger: "pull request comment 1", sources: {}, createdAt: "2026-09-01T20:01:00Z" },
    ] } as Overview;
    render(<WorkflowResourceDialog resource={resource} overview={withChecks} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    expect(screen.getByText("succeeded")).toBeTruthy();
    expect(screen.getByRole("option", { name: /Checks · failed/ })).toBeTruthy();
  });

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
    expect(await screen.findByText(/Stage is running/)).not.toBeNull();
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
