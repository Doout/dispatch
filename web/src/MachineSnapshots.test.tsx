// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import * as api from "./api";
import { MachineSnapshots } from "./MachineSnapshots";
const overview = { projects: [{ id: "project", name: "Project" }] } as api.Overview;
const source = { id: "source", name: "Machine", projectId: "project", allocationState: "allocated" };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("requires an exact snapshot review and keeps its request key after a lost response", async () => {
  const submitted: unknown[] = [];
  vi.spyOn(api, "request").mockImplementation(async (path, init) => {
    if (path.endsWith("/snapshot-review")) return { id: "review", name: "Release disks", action: "snapshot.create", digest: "digest", expiresAt: "2099-01-01T00:00:00Z", input: { request: { diskIds: ["boot-disk", "data-disk"], consistency: "crash-consistent" } } } as never;
    if (path.endsWith("/accept")) { submitted.push(JSON.parse(init?.body as string)); if (submitted.length === 1) throw new Error("Response lost"); return {} as never; }
    return [] as never;
  });
  render(<MachineSnapshots overview={overview} servers={[source]} onRestore={() => {}} />);
  fireEvent.click(screen.getByRole("button", { name: "Capture snapshot" }));
  fireEvent.change(screen.getByLabelText("Source machine"), { target: { value: "source" } });
  fireEvent.change(screen.getByLabelText("Snapshot name"), { target: { value: "Release disks" } });
  fireEvent.submit(screen.getByRole("form", { name: "Capture machine snapshot" }));
  const accept = await screen.findByRole("button", { name: "Accept reviewed snapshot operation" });
  expect((accept as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByText(/"boot-disk"/)).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Type Release disks to confirm"), { target: { value: "Release disks" } });
  fireEvent.click(accept); await screen.findByText("Response lost");
  fireEvent.click(screen.getByRole("button", { name: "Accept reviewed snapshot operation" }));
  await waitFor(() => expect(submitted).toHaveLength(2)); expect(submitted[0]).toEqual(submitted[1]);
});
it("keeps retained snapshots protected and hands restore a specific owned snapshot", async () => {
  const snapshot = { id: "snapshot", name: "Release", serverId: "source", projectId: "project", providerId: "provider", revision: 1, state: "ready", retainUntil: "2099-01-01T00:00:00Z", evidence: { image: "mock-linux", consistency: "crash-consistent", encryption: { mode: "provider-managed" }, disks: [] } };
  vi.spyOn(api, "request").mockResolvedValue([snapshot] as never);
  const restore = vi.fn(); render(<MachineSnapshots overview={overview} servers={[source]} onRestore={restore} />);
  const button = await screen.findByRole("button", { name: "Restore isolated clone" });
  expect((screen.getByRole("button", { name: "Review snapshot deletion" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(button); expect(restore).toHaveBeenCalledWith(snapshot);
  expect(screen.getByText(/Application integrity has not been verified/)).toBeTruthy();
});
