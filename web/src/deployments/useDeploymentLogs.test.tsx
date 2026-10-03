// @vitest-environment jsdom
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, type DeploymentLog } from "../api";
import { useDeploymentLogs } from "./useDeploymentLogs";

function Logs({ id, finishedAt }: { id?: string; finishedAt?: string }) {
  const { logs, logsLoading, logsError } = useDeploymentLogs(id ? { id, finishedAt } : undefined);
  return <><p>{logsLoading ? "Loading" : "Loaded"}</p><p role="alert">{logsError}</p>{logs.map(log => <p key={log.id}>{log.message}</p>)}</>;
}
const entry = (message: string): DeploymentLog[] => [{ id: 1, deploymentId: "run", level: "info", message, createdAt: "" }];
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers(); });

it("ignores the previous deployment's pending logs after navigation", async () => {
  let finishFirst!: (logs: DeploymentLog[]) => void;
  const request = vi.spyOn(api, "logs").mockImplementationOnce(() => new Promise(resolve => { finishFirst = resolve; })).mockResolvedValueOnce(entry("Current release"));
  const view = render(<Logs id="first" finishedAt="done" />);
  view.rerender(<Logs id="second" finishedAt="done" />);
  await act(async () => {});
  expect(screen.getByText("Current release")).toBeTruthy();
  await act(async () => { finishFirst(entry("Previous release")); });
  expect(screen.queryByText("Previous release")).toBeNull();
  expect(request.mock.calls.map(call => call[0])).toEqual(["first", "second"]);
  view.rerender(<Logs />);
  expect(screen.queryByText("Current release")).toBeNull();
  expect(screen.getByText("Loaded")).toBeTruthy();
});

it("pauses polling in a hidden tab and stops once the deployment finishes", async () => {
  vi.useFakeTimers();
  const request = vi.spyOn(api, "logs").mockResolvedValue(entry("Runtime output"));
  const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  const view = render(<Logs id="active" />);
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(request).toHaveBeenCalledTimes(2);
  hidden.mockReturnValue(true);
  await act(async () => { await vi.advanceTimersByTimeAsync(9000); });
  expect(request).toHaveBeenCalledTimes(2);
  hidden.mockReturnValue(false);
  view.rerender(<Logs id="active" finishedAt="done" />);
  await act(async () => { await vi.advanceTimersByTimeAsync(9000); });
  expect(request).toHaveBeenCalledTimes(3);
  view.unmount();
  expect(vi.getTimerCount()).toBe(0);
});

it("clears a polling error after the next successful log response", async () => {
  vi.useFakeTimers();
  vi.spyOn(api, "logs").mockRejectedValueOnce(new Error("Target unavailable")).mockResolvedValueOnce(entry("Recovered output"));
  render(<Logs id="active" />);
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
  expect(screen.getByRole("alert").textContent).toBe("Target unavailable");
  expect(screen.getByText("Loaded")).toBeTruthy();
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(screen.getByRole("alert").textContent).toBe("");
  expect(screen.getByText("Recovered output")).toBeTruthy();
});
