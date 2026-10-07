// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, type Overview } from "./api";
import { ServicesPage } from "./ServicesPage";

const overview = {
  identity: { id: "owner", systemRole: "owner", permissions: [] },
  projects: [{ id: "p", name: "Project" }], apps: [], servers: [], services: [], secrets: [],
  controllerSettings: { operationsEnabled: false },
} as unknown as Overview;

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers(); });

it("hides the Services backup entry until enabled and stops its reader when disabled while open", async () => {
  vi.useFakeTimers();
  vi.spyOn(api, "serviceTemplates").mockResolvedValue([]);
  vi.spyOn(api, "serviceProvisionRuns").mockResolvedValue([]);
  const backups = vi.spyOn(api, "workloadBackups").mockResolvedValue([]);
  const enabled = { ...overview, controllerSettings: { operationsEnabled: false, uiFeatures: { workloadBackups: true } } };
  const view = render(<ServicesPage overview={overview} onChanged={async () => {}} />);
  await act(async () => {});
  expect(screen.queryByRole("button", { name: "Backups" })).toBeNull();
  expect(backups).not.toHaveBeenCalled();
  view.rerender(<ServicesPage overview={enabled} onChanged={async () => {}} />);
  fireEvent.click(screen.getByRole("button", { name: "Backups" }));
  await act(async () => {});
  expect(backups).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("region", { name: "Workload backups" })).toBeTruthy();
  view.rerender(<ServicesPage overview={overview} onChanged={async () => {}} />);
  expect(screen.queryByRole("button", { name: "Backups" })).toBeNull();
  expect(screen.queryByRole("region", { name: "Workload backups" })).toBeNull();
  expect(screen.getByRole("button", { name: "Services" }).getAttribute("aria-pressed")).toBe("true");
  await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
  expect(backups).toHaveBeenCalledTimes(1);
});
