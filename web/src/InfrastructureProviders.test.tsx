// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { InfrastructureProviders, infrastructureApi, type InfrastructureProvider } from "./InfrastructureProviders";
import { type Overview } from "./api";
const overview = { secrets: [{ id: "credential", name: "Adapter credential", type: "api_token" }], privateNetworks: [] } as unknown as Overview;
const provider: InfrastructureProvider = { id: "mock", name: "Mock adapter", endpoint: "https://provider.example", enabled: true, capabilities: ["server.inspect", "server.create", "server.delete"], credentialSecretId: "credential", state: "ready", revision: 3, manifest: { name: "mock", displayName: "Mock", version: "1", apiVersion: "dispatch.provider/v1", capabilities: ["server.inspect", "server.create", "server.delete"], configurationSchema: { type: "object", properties: { region: { type: "string" } } } } };
beforeEach(() => { vi.spyOn(infrastructureApi, "list").mockResolvedValue([provider]); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("disables the registration with its revision while retaining its manifest", async () => {
  const save = vi.spyOn(infrastructureApi, "save").mockResolvedValue({ ...provider, enabled: false, state: "disabled", revision: 4 });
  render(<InfrastructureProviders overview={overview} />);
  await screen.findByText("Mock adapter");
  fireEvent.click(screen.getByRole("button", { name: "Disable provider" }));
  await waitFor(() => expect(save).toHaveBeenCalledWith("mock", { name: "Mock adapter", endpoint: "https://provider.example", privateNetworkId: undefined, credentialSecretId: "credential", capabilities: provider.capabilities, enabled: false, revision: 3 }));
  expect(await screen.findByRole("button", { name: "Enable provider" })).toBeTruthy();
  expect(screen.getByText(/dispatch.provider\/v1/)).toBeTruthy();
});
it("edits credential references without requesting or displaying credential values", async () => {
  render(<InfrastructureProviders overview={overview} />);
  fireEvent.click(await screen.findByRole("button", { name: "Edit provider" }));
  expect((screen.getByLabelText(/Authentication secret/) as HTMLSelectElement).value).toBe("credential");
  expect((screen.getByLabelText("Adapter endpoint") as HTMLInputElement).disabled).toBe(true);
  expect(screen.queryByLabelText(/password|token value/i)).toBeNull();
});
