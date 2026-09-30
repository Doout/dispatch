import { describe, expect, it } from "vitest";
import type { WorkflowJobResult, WorkflowRevision, WorkflowStageRun } from "../api";
import { buildLogTiming, formatRunDuration, runTiming } from "./runTiming";

const at = (minute: number) => new Date(Date.UTC(2026, 8, 1, 12, minute)).toISOString();

function job(values: Partial<WorkflowJobResult>): WorkflowJobResult {
  return { id: "job", resourceId: "resource", revisionId: "revision", jobName: "build", fingerprint: "one", state: "succeeded", sources: {}, createdAt: at(0), ...values };
}

describe("run timing", () => {
  it("measures parallel jobs by wall time and separates the deployment stage", () => {
    const revision = { id: "revision", resourceId: "resource", configSha: "one", specDigest: "one", state: "succeeded", trigger: "manual", sources: {}, createdAt: at(0), startedAt: at(1), finishedAt: at(11) } as WorkflowRevision;
    const jobs = [
      job({ startedAt: at(1), finishedAt: at(10), log: "#5 [build 1/2] RUN install\n#5 DONE 120.0s\n#6 [build 2/2] COPY src .\n#6 CACHED\n" }),
      job({ id: "ui", jobName: "build-ui", startedAt: at(1), finishedAt: at(3), reusedFromId: "previous" }),
    ];
    const stages = [{ id: "stage", revisionId: "revision", stageName: "development", targetRef: "dev", state: "succeeded", approval: "automatic", createdAt: at(0), startedAt: at(10), finishedAt: at(11) }] as WorkflowStageRun[];
    expect(runTiming(revision, jobs, stages)).toEqual({ total: 660000, queue: 60000, builds: 540000, stages: 60000, other: 0, reusedJobs: 1, cachedSteps: 1 });
  });

  it("shows the slowest BuildKit steps and ignores duplicate progress lines", () => {
    const log = "#2 [build 1/3] RUN install\n#2 DONE 90.0s\n#3 [build 2/3] RUN compile\n#3 DONE 32.5s\n#4 [build 3/3] COPY . .\n#4 CACHED\n#4 CACHED\n";
    expect(buildLogTiming(log)).toEqual({ cachedSteps: 1, slowest: [{ label: "build 1/3 · RUN install", durationMs: 90000 }, { label: "build 2/3 · RUN compile", durationMs: 32500 }] });
    expect(formatRunDuration(32500)).toBe("33s");
  });

  it("ignores missing and zero timestamps", () => {
    const revision = { id: "revision", resourceId: "resource", configSha: "one", specDigest: "one", state: "failed", trigger: "manual", sources: {}, createdAt: "0001-01-01T00:00:00Z" } as WorkflowRevision;
    expect(runTiming(revision, [job({})], [])).toMatchObject({ total: undefined, builds: undefined, stages: undefined });
  });
});
