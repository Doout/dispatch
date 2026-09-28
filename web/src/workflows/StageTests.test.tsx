// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type WorkflowResource, type WorkflowRevision, type WorkflowStageRun } from "../api";
import { StageTests, configuredStageTests } from "./StageTests";

const resource = {
  specDigest: "current",
  document: `kind: Application
spec:
  stages:
    - name: development
      checks:
        health: {pipelineRef: readiness}
        e2e: {pipelineRef: full-e2e, when: onDemand}
`,
} as WorkflowResource;
const deployment = { id: "deployed", specDigest: "current", trigger: "pull request comment 1" } as WorkflowRevision;
const stage: WorkflowStageRun = { id: "stage", revisionId: "deployed", targetRef: "dev", approval: "automatic", createdAt: "2026-09-28T00:00:00Z", stageName: "development", state: "succeeded", checkRuns: { health: "health-run" } };
const health = { id: "health-run", state: "succeeded" } as WorkflowRevision;

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("stage tests", () => {
  it("shows health results and the actual command for E2E without starting work", async () => {
    const fetchRun = vi.spyOn(api, "workflowRevision").mockResolvedValue(health);
    const fetchJobs = vi.spyOn(api, "workflowJobs").mockResolvedValue([]);
    render(<StageTests stage={stage} definitions={configuredStageTests(resource, deployment, "development")} definitionMatches command="/ship" />);
    expect(await screen.findByText("succeeded")).not.toBeNull();
    expect(screen.getByText("e2e")).not.toBeNull();
    expect(screen.getByText("on demand")).not.toBeNull();
    expect(screen.getByText("/ship test")).not.toBeNull();
    expect(fetchRun).toHaveBeenCalledExactlyOnceWith("health-run");
    expect(fetchJobs).not.toHaveBeenCalled();
  });

  it("opens the named substeps and their logs for a failed E2E check", async () => {
    vi.spyOn(api, "workflowRevision").mockResolvedValue({ id: "e2e-run", state: "failed", error: "assertion failed" } as WorkflowRevision);
    vi.spyOn(api, "workflowJobs").mockResolvedValue([
      { id: "auth", jobName: "authentication", state: "succeeded", log: "Authenticated" },
      { id: "journey", jobName: "user-journey", state: "failed", log: "Expected result was absent" },
    ] as Awaited<ReturnType<typeof api.workflowJobs>>);
    const revision = { ...deployment, trigger: "pull request test 2" };
    render(<StageTests stage={{ ...stage, state: "failed", checkRuns: { e2e: "e2e-run" } }} definitions={configuredStageTests(resource, revision, "development")} definitionMatches />);
    expect(await screen.findByText("assertion failed")).not.toBeNull();
    expect(screen.queryByText("health")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "View e2e test logs" }));
    expect(await screen.findByRole("region", { name: "e2e / authentication test output" })).not.toBeNull();
    expect(screen.getByText("Expected result was absent")).not.toBeNull();
  });

  it("keeps other test results visible when one refresh fails", async () => {
    vi.spyOn(api, "workflowRevision").mockImplementation(async id => {
      if (id === "health-run") return health;
      throw new Error("Connection unavailable");
    });
    render(<StageTests stage={{ ...stage, checkRuns: { health: "health-run", e2e: "e2e-run" } }} definitions={configuredStageTests(resource, deployment, "development")} definitionMatches />);
    expect(await screen.findByText("succeeded")).not.toBeNull();
    expect(screen.getByRole("alert").textContent).toContain("Could not refresh e2e: Connection unavailable");
  });

  it("does not attribute the current configuration's E2E checks to an older run", async () => {
    vi.spyOn(api, "workflowRevision").mockResolvedValue(health);
    const oldRun = { ...deployment, specDigest: "older" };
    render(<StageTests stage={stage} definitions={configuredStageTests(resource, oldRun, "development")} definitionMatches={false} />);
    expect(await screen.findByText("succeeded")).not.toBeNull();
    expect(screen.queryByText("e2e")).toBeNull();
    expect(screen.queryByText("on demand")).toBeNull();
  });

  it("makes missing health and test configuration explicit", async () => {
    render(<StageTests stage={{ ...stage, checkRuns: {} }} definitions={{}} definitionMatches />);
    expect(screen.getByText("No health or test checks are configured for this stage.")).not.toBeNull();
    await waitFor(() => expect(screen.queryByText("succeeded")).toBeNull());
  });
});
