// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api, type Server, type StorageResource } from "./api";
import { StorageInventory } from "./StorageInventory";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const server = { id: "target", name: "Docker target" } as Server;
const volume: StorageResource = { id: "volume", serverId: "target", kind: "docker_volume", name: "orders-data", identity: "runtime-object", projectId: "project", ownerKind: "service", ownerId: "database", ownership: "verified", orphaned: false, policy: "retain", state: "present", consumers: [], revision: 3, observedAt: "2026-10-01T12:00:00Z" };

it("requires a policy change before exposing reviewed data deletion", async () => {
  const user = userEvent.setup();
  vi.spyOn(api, "storage").mockResolvedValueOnce([volume]).mockResolvedValue([{ ...volume, policy: "destroy", revision: 4 }]);
  const policy = vi.spyOn(api, "storagePolicy").mockResolvedValue({ ...volume, policy: "destroy", revision: 4 });
  const remove = vi.spyOn(api, "deleteStorage").mockResolvedValue();
  render(<StorageInventory server={server} canManage onClose={() => {}} />);
  await screen.findByText("orders-data");
  expect((screen.getByRole("button", { name: "Delete data" }) as HTMLButtonElement).disabled).toBe(true);
  await user.selectOptions(screen.getByLabelText("Data policy for orders-data"), "destroy");
  await waitFor(() => expect(policy).toHaveBeenCalledWith("volume", 3, "destroy"));
  await user.click(screen.getByRole("button", { name: "Delete data" }));
  await waitFor(() => expect(remove).toHaveBeenCalledWith("volume"));
});

it("shows retained consumers and disables mutation for read-only users", async () => {
  vi.spyOn(api, "storage").mockResolvedValue([{ ...volume, consumers: [{ id: "container:worker", mount: "/data", active: false }], orphaned: true }]);
  render(<StorageInventory server={server} canManage={false} onClose={() => {}} />);
  await screen.findByText("container:worker → /data");
  expect(screen.queryByRole("button", { name: "Delete data" })).toBeNull();
  expect(screen.queryByLabelText("Data policy for orders-data")).toBeNull();
  expect(screen.queryByRole("button", { name: "Inspect target storage" })).toBeNull();
});
