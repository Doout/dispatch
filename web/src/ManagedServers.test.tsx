// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import * as api from "./api";
import { ManagedServers } from "./ManagedServers";
const overview = { controllerSettings: { operationsEnabled: false, uiFeatures: { machineProvisioning: true, machineSnapshots: true } }, projects: [{ id: "project", name: "Project" }], secrets: [{ id: "ssh", name: "SSH", type: "ssh_private_key" }] } as api.Overview;
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("requires a named review and preserves the request key after an uncertain response", async () => {
  const submitted: Record<string, unknown>[] = [];
  const input = { projectId: "project", providerId: "provider", name: "Machine", region: "region", size: "size", image: "image", network: "network", sshKeySecretId: "ssh", config: {}, secretRefs: {} };
  vi.spyOn(api, "request").mockImplementation(async (path, init) => {
    if (path.endsWith("/infrastructure/providers")) return [{ id: "provider", name: "Mock", manifest: { configurationSchema: { properties: {} } } }] as never;
    if (path.endsWith("/options")) { const body = JSON.parse(init?.body as string); const id = String(body.kind).slice(0, -1); return [{ id, name: id }] as never; }
    if (path.endsWith("/review")) return { id: "review", name: "Machine", digest: "review-digest", expiresAt: "2099-01-01T00:00:00Z", input } as never;
    if (init?.method === "POST") { submitted.push(JSON.parse(init.body as string)); if (submitted.length === 1) throw new Error("Response unavailable"); return {} as never; }
    return [] as never;
  });
  render(<ManagedServers overview={overview} />);
  fireEvent.click(screen.getByRole("button", { name: "Create server" }));
  fireEvent.change(screen.getByLabelText("Project"), { target: { value: "project" } });
  await screen.findByRole("option", { name: "Mock" });
  fireEvent.change(screen.getByLabelText("Infrastructure provider"), { target: { value: "provider" } });
  fireEvent.change(screen.getByLabelText("Machine name"), { target: { value: "Machine" } });
  fireEvent.change(screen.getByLabelText(/SSH public key/), { target: { value: "ssh" } });
  fireEvent.click(screen.getByRole("button", { name: "Load machine choices" }));
  await screen.findByRole("option", { name: "region" });
  for (const field of ["Region", "Size", "Image", "Network"]) fireEvent.change(screen.getByLabelText(field), { target: { value: field.toLowerCase() } });
  fireEvent.submit(screen.getByRole("form", { name: "Create on-demand server" }));
  const accept = await screen.findByRole("button", { name: "Create reviewed server" });
  expect((accept as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Type Machine to confirm"), { target: { value: "Machine" } });
  fireEvent.click(accept);
  await screen.findByText("Response unavailable");
  fireEvent.click(screen.getByRole("button", { name: "Create reviewed server" }));
  await waitFor(() => expect(submitted.length).toBe(2));
  expect(submitted[0]).toEqual(submitted[1]);
  expect(submitted[0]).toMatchObject({ reviewId: "review", digest: "review-digest", confirmName: "Machine" });
  expect(submitted[0].requestKey).toBeTruthy();
});

it("hides snapshot and clone operation controls immediately while leaving ordinary machine recovery available", async () => {
  const machines = [
    { id: "machine", projectId: "project", name: "Source machine", allocationState: "allocated", enrollmentState: "enrolled", runtimeState: "ready", revision: 1 },
    { id: "clone", projectId: "project", name: "Isolated clone", sourceSnapshotId: "snapshot", allocationState: "unknown", enrollmentState: "pending", runtimeState: "unknown", revision: 1 },
  ];
  const request = vi.spyOn(api, "request").mockImplementation(async path => {
    if (path.endsWith("/machine/operations")) return [
      { id: "capture", action: "snapshot.create", state: "paused" },
      { id: "delete", action: "snapshot.delete", state: "paused" },
      { id: "ordinary", action: "create", state: "paused" },
    ] as never;
    if (path.endsWith("/clone/operations")) return [
      { id: "restore", action: "create", state: "paused" },
      { id: "promote", action: "server.promote", state: "paused" },
      { id: "uncertain", action: "create", state: "unknown" },
    ] as never;
    return machines as never;
  });
  const disabled = { ...overview, controllerSettings: { operationsEnabled: false, uiFeatures: { machineProvisioning: true, machineSnapshots: false } } };
  const view = render(<ManagedServers overview={overview} section="machines" />);
  await screen.findByText("Source machine");
  for (const button of screen.getAllByRole("button", { name: "Operations" })) {
    fireEvent.click(button);
    await waitFor(() => expect(button.hasAttribute("disabled")).toBe(false));
  }
  await waitFor(() => expect(screen.getAllByRole("button", { name: "Retry saved operation" })).toHaveLength(5));
  expect(screen.getAllByRole("button", { name: "Cancel creation" })).toHaveLength(3);
  fireEvent.click(screen.getByRole("button", { name: "Inspect and adopt" }));
  fireEvent.change(screen.getByLabelText("Provider resource ID"), { target: { value: "provider-clone" } });
  fireEvent.change(screen.getByLabelText("Type Isolated clone to confirm"), { target: { value: "Isolated clone" } });
  expect(screen.getByRole("heading", { name: "Inspect owned resource for Isolated clone" })).toBeTruthy();
  request.mockClear();
  view.rerender(<ManagedServers overview={disabled} section="machines" />);
  const source = within(screen.getByText("Source machine").closest("article")!);
  const clone = within(screen.getByText("Isolated clone").closest("article")!);
  expect(source.getAllByRole("button", { name: "Retry saved operation" })).toHaveLength(1);
  expect(source.getAllByRole("button", { name: "Cancel creation" })).toHaveLength(1);
  expect(clone.queryByRole("button", { name: "Retry saved operation" })).toBeNull();
  expect(clone.queryByRole("button", { name: "Cancel creation" })).toBeNull();
  expect(clone.queryByRole("button", { name: "Inspect and adopt" })).toBeNull();
  expect(screen.queryByLabelText("Provider resource ID")).toBeNull();
  expect(source.getByText("snapshot.create · paused")).toBeTruthy();
  expect(request).not.toHaveBeenCalled();
  view.rerender(<ManagedServers overview={overview} section="machines" />);
  expect(screen.getByRole("button", { name: "Inspect and adopt" })).toBeTruthy();
  expect(screen.queryByLabelText("Provider resource ID")).toBeNull();
  expect(request).not.toHaveBeenCalled();
});
