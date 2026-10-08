// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, type WorkflowRevision } from "../api";
import { useWorkflowRevisions } from "./useWorkflowRevisions";

function revision(values: Partial<WorkflowRevision> = {}): WorkflowRevision {
  return {
    id: "run-1", resourceId: "resource-1", configSha: "abc", specDigest: "spec",
    state: "running", trigger: "manual", sources: {}, createdAt: "2026-10-08T10:00:00Z",
    ...values,
  };
}

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers(); });

it("refreshes a run missing from overview without replacing its loaded history with a loading state", async () => {
  vi.useFakeTimers();
  let finishRefresh!: (items: WorkflowRevision[]) => void;
  const request = vi.spyOn(api, "workflowRevisions")
    .mockResolvedValueOnce([revision()])
    .mockImplementationOnce(() => new Promise(resolve => { finishRefresh = resolve; }));
  const view = renderHook(() => useWorkflowRevisions("resource-1"));
  await act(async () => {});
  expect(view.result.current.revisions[0].state).toBe("running");
  expect(view.result.current.loading).toBe(false);

  await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
  expect(request).toHaveBeenCalledTimes(2);
  expect(view.result.current.loading).toBe(false);
  expect(view.result.current.revisions[0].state).toBe("running");

  await act(async () => { finishRefresh([revision({ state: "failed", error: "Build failed" })]); });
  expect(view.result.current.revisions[0].state).toBe("failed");
  expect(view.result.current.revisions[0].error).toBe("Build failed");
  view.unmount();
  await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
  expect(request).toHaveBeenCalledTimes(2);
  expect(vi.getTimerCount()).toBe(0);
});

it("ignores a pending refresh from the previous resource after navigation", async () => {
  vi.useFakeTimers();
  let finishPrevious!: (items: WorkflowRevision[]) => void;
  const next = revision({ id: "run-2", resourceId: "resource-2", state: "succeeded" });
  const request = vi.spyOn(api, "workflowRevisions")
    .mockResolvedValueOnce([revision()])
    .mockImplementationOnce(() => new Promise(resolve => { finishPrevious = resolve; }))
    .mockResolvedValue([next]);
  const view = renderHook(({ resourceID }) => useWorkflowRevisions(resourceID), { initialProps: { resourceID: "resource-1" } });
  await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
  view.rerender({ resourceID: "resource-2" });
  expect(view.result.current.revisions).toEqual([]);
  await act(async () => {});
  expect(view.result.current.revisions).toEqual([next]);

  await act(async () => { finishPrevious([revision({ state: "failed" })]); });
  expect(view.result.current.revisions).toEqual([next]);
  await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
  expect(request.mock.calls.map(call => call[0])).toEqual(["resource-1", "resource-1", "resource-2", "resource-2"]);
  view.unmount();
  expect(vi.getTimerCount()).toBe(0);
});

it("keeps loaded runs on a refresh failure and clears the error after recovery", async () => {
  vi.useFakeTimers();
  vi.spyOn(api, "workflowRevisions")
    .mockResolvedValueOnce([revision()])
    .mockRejectedValueOnce(new Error("History unavailable"))
    .mockResolvedValueOnce([revision({ state: "failed" })]);
  const view = renderHook(() => useWorkflowRevisions("resource-1"));
  await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
  expect(view.result.current.error).toBe("History unavailable");
  expect(view.result.current.revisions[0].state).toBe("running");
  expect(view.result.current.loading).toBe(false);

  await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
  expect(view.result.current.error).toBe("");
  expect(view.result.current.revisions[0].state).toBe("failed");
});

it("retains resource history while merging new overview states and runs", async () => {
  const historical = revision({ id: "historical", createdAt: "2026-10-07T10:00:00Z", state: "succeeded" });
  const current = revision();
  vi.spyOn(api, "workflowRevisions").mockResolvedValue([historical, current]);
  const view = renderHook(({ overview }) => useWorkflowRevisions("resource-1", overview), { initialProps: { overview: [] as WorkflowRevision[] } });
  await act(async () => {});

  const updated = { ...current, state: "failed" };
  const newest = revision({ id: "newest", createdAt: "2026-10-08T11:00:00Z", state: "queued" });
  const unrelated = revision({ id: "other", resourceId: "resource-2" });
  view.rerender({ overview: [newest, updated, unrelated] });
  expect(view.result.current.revisions).toEqual([newest, updated, historical]);
});
