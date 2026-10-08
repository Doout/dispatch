// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type Overview, type WorkflowJobResult, type WorkflowResource, type WorkflowRevision } from "../api";
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
  vi.spyOn(api, "workflowRevisions").mockResolvedValue([]);
  vi.spyOn(api, "workflowRevision").mockResolvedValue(overview.workflowRevisions![0]);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("workflow resource run details", () => {
  it("loads the latest failed build outside the overview window, ahead of newer preview checks", async () => {
    const failed = overview.workflowRevisions![0];
    const old = { ...failed, id: "old-success", state: "succeeded", error: undefined, createdAt: "2026-08-01T00:00:00Z" };
    const check = { ...failed, id: "new-check", state: "succeeded", error: undefined, trigger: "pull request test 42", createdAt: "2026-09-02T00:00:00Z" };
    vi.mocked(api.workflowRevisions).mockResolvedValue([check, failed, old]);
    vi.spyOn(api, "workflowJobs").mockResolvedValue([job({ log: "Saved dependency hash mismatch\n" })]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    render(<WorkflowResourceDialog resource={resource} overview={{ ...overview, workflowRevisions: [] }} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);

    expect(await screen.findByText(/Saved dependency hash mismatch/)).toBeTruthy();
    expect(api.workflowRevisions).toHaveBeenCalledWith(resource.id);
    expect((screen.getByRole("combobox", { name: "Workflow run" }) as HTMLSelectElement).value).toBe(failed.id);
    expect(screen.getAllByRole("option")).toHaveLength(3);
  });

  it("keeps a manually selected historical run when resource history finishes loading", async () => {
    const latest = overview.workflowRevisions![0];
    const old = { ...latest, id: "old-success", state: "succeeded", error: undefined, createdAt: "2026-08-01T00:00:00Z" };
    let finishHistory!: (revisions: WorkflowRevision[]) => void;
    vi.mocked(api.workflowRevisions).mockImplementation(() => new Promise(resolve => { finishHistory = resolve; }));
    vi.mocked(api.workflowRevision).mockImplementation(async id => id === old.id ? old : latest);
    vi.spyOn(api, "workflowJobs").mockImplementation(async id => [job({ id: `job-${id}`, revisionId: id, state: id === old.id ? "succeeded" : "failed", log: id === old.id ? "Historical image pushed" : "Latest build failed" })]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    render(<WorkflowResourceDialog resource={resource} overview={{ ...overview, workflowRevisions: [latest, old] }} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Workflow run" }), old.id);
    expect(await screen.findByText("Historical image pushed")).toBeTruthy();
    await act(async () => finishHistory([{ ...latest, id: "newer-failure", createdAt: "2026-09-03T00:00:00Z" }, latest, old]));
    expect((screen.getByRole("combobox", { name: "Workflow run" }) as HTMLSelectElement).value).toBe(old.id);
    expect(screen.getByText("Historical image pushed")).toBeTruthy();
  });

  it("retries failed history loading and selects the fresh latest run", async () => {
    const latest = overview.workflowRevisions![0];
    const old = { ...latest, id: "old-success", state: "succeeded", error: undefined, createdAt: "2026-08-01T00:00:00Z" };
    vi.mocked(api.workflowRevisions).mockRejectedValueOnce(new Error("History unavailable")).mockResolvedValue([latest, old]);
    vi.mocked(api.workflowRevision).mockImplementation(async id => id === old.id ? old : latest);
    vi.spyOn(api, "workflowJobs").mockImplementation(async id => id === latest.id ? [job({ log: "Recovered failure output" })] : []);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    render(<WorkflowResourceDialog resource={resource} overview={{ ...overview, workflowRevisions: [old] }} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    expect(await screen.findByText(/Could not load run history. History unavailable/)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Retry run history" }));
    expect(await screen.findByText("Recovered failure output")).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "Workflow run" }) as HTMLSelectElement).value).toBe(latest.id);
  });

  it("ignores pending history from a resource after switching to another resource", async () => {
    const nextResource = { ...resource, id: "next-resource", name: "Other application" };
    const nextRevision = { ...overview.workflowRevisions![0], id: "next-revision", resourceId: nextResource.id };
    let finishPrevious!: (revisions: WorkflowRevision[]) => void;
    vi.mocked(api.workflowRevisions).mockImplementationOnce(() => new Promise(resolve => { finishPrevious = resolve; })).mockResolvedValue([nextRevision]);
    vi.mocked(api.workflowRevision).mockResolvedValue(nextRevision);
    vi.spyOn(api, "workflowJobs").mockResolvedValue([job({ id: "next-job", resourceId: nextResource.id, revisionId: nextRevision.id, log: "Other application output" })]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    const props = { overview: { ...overview, workflowRevisions: [] }, onClose: vi.fn(), onChanged: vi.fn(), onOpenDeploymentManifests: vi.fn() };
    const view = render(<WorkflowResourceDialog resource={resource} {...props} />);
    view.rerender(<WorkflowResourceDialog resource={nextResource} {...props} />);
    expect(await screen.findByText("Other application output")).toBeTruthy();
    await act(async () => finishPrevious(overview.workflowRevisions!));
    expect((screen.getByRole("combobox", { name: "Workflow run" }) as HTMLSelectElement).value).toBe(nextRevision.id);
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(api.workflowJobs).not.toHaveBeenCalledWith("revision-1");
  });

  it("keeps explicit historical runs and follows a changed initial run", async () => {
    const latest = overview.workflowRevisions![0];
    const old = { ...latest, id: "old-success", state: "succeeded", error: undefined, createdAt: "2026-08-01T00:00:00Z" };
    vi.mocked(api.workflowRevisions).mockResolvedValue([latest, old]);
    vi.mocked(api.workflowRevision).mockImplementation(async id => id === old.id ? old : latest);
    vi.spyOn(api, "workflowJobs").mockImplementation(async id => [job({ id: `job-${id}`, revisionId: id, log: id === old.id ? "Historical output" : "Latest output" })]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    const props = { resource, overview, onClose: vi.fn(), onChanged: vi.fn(), onOpenDeploymentManifests: vi.fn() };
    const view = render(<WorkflowResourceDialog {...props} initialRevisionID={old.id} />);
    expect(await screen.findByText("Historical output")).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "Workflow run" }) as HTMLSelectElement).value).toBe(old.id);
    view.rerender(<WorkflowResourceDialog {...props} initialRevisionID={latest.id} />);
    expect(await screen.findByText("Latest output")).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "Workflow run" }) as HTMLSelectElement).value).toBe(latest.id);
  });

  it("refreshes reporting for a terminal run without legacy feedback", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    vi.mocked(api.workflowRevision).mockResolvedValue({ ...overview.workflowRevisions![0], checks: [{ id: "report", revisionId: "revision-1", resourceId: resource.id, projectId: "project-1", githubAppId: "app", repository: "example/service", commitSha: "a".repeat(40), name: "Dispatch/deployment/checkout", kind: "deployment", externalId: "dispatch-check:report", state: "retrying", attempts: 1, complete: false, updatedAt: resource.createdAt, error: "Grant Checks: write to report this result." }] });
    render(<WorkflowResourceDialog resource={resource} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    expect(await screen.findByText("Grant Checks: write to report this result.")).toBeTruthy();
    expect(screen.getByText("Retry pending")).toBeTruthy();
    expect(api.workflowRevision).toHaveBeenCalledWith("revision-1");
  });

  it("keeps partial cleanup visible and prevents activation during removal", async () => {
    vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    vi.spyOn(api, "workflowStages").mockResolvedValue([]);
    vi.spyOn(api, "workflowPreviewTriggers").mockResolvedValue([]);
    const stopped: WorkflowResource = { ...resource, temporary: true, active: false, state: "expiring", previewCleanups: [{ id: "cleanup", resourceId: resource.id, reason: "removed", finalState: "removed", state: "blocked", error: "Target is offline", attempts: 2, createdAt: resource.createdAt, updatedAt: resource.updatedAt, apps: [{ appId: "web", serverId: "target", jobId: "owned-cleanup", state: "blocked" }] }] };
    render(<WorkflowResourceDialog resource={stopped} overview={overview} onClose={vi.fn()} onChanged={vi.fn()} onOpenDeploymentManifests={vi.fn()} />);
    expect(await screen.findByRole("region", { name: "Preview cleanup history" })).toBeTruthy();
    expect(screen.getByText("Target is offline")).toBeTruthy();
    expect(screen.getByText("owned-cleanup")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Activate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Run" })).toBeNull();
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
    expect(screen.getByText("succeeded", { selector: ".status-label" })).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "Workflow run" }) as HTMLSelectElement).value).toBe("deployment");
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
