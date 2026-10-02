// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { InfrastructureQuotas, infrastructureQuotaApi, type QuotaView } from "./InfrastructureQuotas";
import { infrastructureApi, type InfrastructureProvider } from "./InfrastructureProviders";
import { type Overview } from "./api";
const overview = { projects: [{ id: "team", name: "Team" }], projectPermissions: { team: ["infrastructure.inspect"] } } as unknown as Overview;
const view: QuotaView = { policy: { projectId: "team", configured: true, revision: 4, maxServers: 2, maxTemporaryEnvironments: 0, maxSnapshots: 0, maxTemporaryLifetimeSeconds: 0, providers: [{ providerId: "mock", regions: ["eu-1"], sizes: ["small"], anyRegion: false, anySize: false }] }, usage: { servers: 2, reserved: 1, allocated: 0, unknown: 1 }, reservations: [{ serverId: "server", operationId: "original-create", providerId: "mock", region: "eu-1", size: "small", state: "unknown" }] };
beforeEach(() => { vi.spyOn(infrastructureQuotaApi, "get").mockResolvedValue(view); vi.spyOn(infrastructureApi, "list").mockResolvedValue([{ id: "mock", name: "Mock provider" } as InfrastructureProvider]); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("shows counted unresolved allocations without allowing an operator to raise their limit", async () => {
  render(<InfrastructureQuotas overview={overview} canManage={false} />);
  await screen.findByText(/Maximum servers: 2/);
  expect(screen.getByText(/Inspect the original operation/)).toBeTruthy();
  expect(screen.getByText(/Operation original-create/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Save resource policy" })).toBeNull();
  expect(infrastructureApi.list).not.toHaveBeenCalled();
});
it("saves the reviewed policy revision and complete region and size allowlists", async () => {
  const save = vi.spyOn(infrastructureQuotaApi, "save").mockResolvedValue({ ...view.policy, revision: 5 });
  render(<InfrastructureQuotas overview={overview} canManage />);
  const region = await screen.findByLabelText("Allowed regions for Mock provider");
  fireEvent.change(region, { target: { value: "eu-1, us-1" } });
  fireEvent.change(screen.getByLabelText("Allowed sizes for Mock provider"), { target: { value: "small, medium" } });
  fireEvent.click(screen.getByRole("button", { name: "Save resource policy" }));
  await waitFor(() => expect(save).toHaveBeenCalledWith("team", expect.objectContaining({ revision: 4, providers: [{ providerId: "mock", regions: ["eu-1", "us-1"], sizes: ["small", "medium"], anyRegion: false, anySize: false }] })));
});
it("does not fetch another project's quota when inspect permission is absent", () => {
  render(<InfrastructureQuotas overview={{ ...overview, projectPermissions: {} }} canManage={false} />);
  expect(infrastructureQuotaApi.get).not.toHaveBeenCalled();
  expect(screen.queryByText("Project resource limits")).toBeNull();
});
