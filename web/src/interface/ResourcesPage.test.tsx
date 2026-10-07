// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import * as api from "../api";
import { infrastructureApi } from "../InfrastructureProviders";
import { ResourcesPage } from "./ResourcesPage";
import { automationClient, Assignment } from "./resources/client";

const overview = { identity: { id: "owner", systemRole: "owner", permissions: [] }, projects: [{ id: "p", name: "Project one" }, { id: "q", name: "Project two" }], secrets: [], servers: [], services: [], privateNetworks: [], projectPermissions: {} } as unknown as api.Overview;
const changed = async () => {};
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("does not request owner-only resources for a member or disabled controller backups", () => {
  const request = vi.spyOn(api, "request");
  const accounts = vi.spyOn(automationClient, "accounts");
  const providers = vi.spyOn(infrastructureApi, "list");
  const member = { ...overview, identity: { ...overview.identity!, systemRole: "member" as const } };
  const view = render(<ResourcesPage overview={member} section="credentials" onChanged={changed} />);
  expect(screen.getByText("A controller owner manages credentials.")).toBeTruthy();
  view.rerender(<ResourcesPage overview={member} section="providers" onChanged={changed} />);
  view.rerender(<ResourcesPage overview={overview} section="controller-backups" onChanged={changed} />);
  expect(screen.getByText("Enable Operations in Settings to use controller backups.")).toBeTruthy();
  expect(request).not.toHaveBeenCalled(); expect(accounts).not.toHaveBeenCalled(); expect(providers).not.toHaveBeenCalled();
});

it("lets an owner approve snapshot capabilities without selecting them by default", async () => {
  vi.spyOn(infrastructureApi, "list").mockResolvedValue([]);
  render(<ResourcesPage overview={overview} section="providers" onChanged={changed} />);
  fireEvent.click(screen.getByRole("button", { name: "Register provider" }));
  expect((screen.getByLabelText("Capture snapshots") as HTMLInputElement).checked).toBe(false);
  expect((screen.getByLabelText("Inspect snapshots") as HTMLInputElement).checked).toBe(false);
  expect((screen.getByLabelText("Restore isolated clones") as HTMLInputElement).checked).toBe(false);
  await waitFor(() => expect(infrastructureApi.list).toHaveBeenCalled());
});

it("issues an expiring credential and clears its one-time token when dismissed", async () => {
  vi.spyOn(automationClient, "accounts").mockResolvedValue([{ id: "account", name: "Build agent", state: "active" }]);
  vi.spyOn(automationClient, "credentials").mockResolvedValue([{ id: "existing", accountId: "account", name: "Existing", expiresAt: "2099-01-01T00:00:00Z" }]);
  vi.spyOn(automationClient, "grants").mockResolvedValue([]);
  const issue = vi.spyOn(automationClient, "issue").mockResolvedValue({ credential: { id: "credential", accountId: "account", name: "CI", expiresAt: "2099-01-01T00:00:00Z" }, token: "token-shown-once" });
  render(<ResourcesPage overview={overview} section="credentials" onChanged={changed} />);
  fireEvent.click(await screen.findByRole("button", { name: "Manage Build agent" }));
  fireEvent.click(screen.getByRole("button", { name: "Issue credential" }));
  fireEvent.change(screen.getByLabelText("Credential name"), { target: { value: "CI" } });
  fireEvent.change(screen.getByLabelText("Expires in days"), { target: { value: "7" } });
  fireEvent.submit(screen.getByRole("form", { name: "Issue credential" }));
  expect((await screen.findByLabelText("New token") as HTMLTextAreaElement).value).toBe("token-shown-once");
  expect(issue).toHaveBeenCalledWith("account", "CI", expect.any(String), undefined);
  const expiry = Date.parse(issue.mock.calls[0][2]);
  expect(Math.abs(expiry - Date.now() - 7 * 86400000)).toBeLessThan(5000);
  expect(screen.getByRole("button", { name: "Disable account" }).hasAttribute("disabled")).toBe(true);
  expect((await screen.findByRole("button", { name: "Revoke Existing" })).hasAttribute("disabled")).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "I saved the token" }));
  expect(screen.queryByLabelText("New token")).toBeNull();
  expect(screen.getByRole("button", { name: "Disable account" }).hasAttribute("disabled")).toBe(false);
});

it("shows receipt errors and only loads an operation after inspection", async () => {
  const receipt = vi.spyOn(automationClient, "receipt").mockRejectedValueOnce(new Error("Receipt belongs to another account.")).mockResolvedValue({ id: "receipt", projectId: "p", action: "deployment.create", operationId: "deployment", operationUrl: "/api/v1/deployments/deployment", recoveryActions: [], state: "succeeded", createdAt: "2026-10-01", updatedAt: "2026-10-01", retryUntil: "2026-10-08" });
  const request = vi.spyOn(api, "request").mockResolvedValue({ id: "deployment", state: "succeeded" });
  render(<ResourcesPage overview={overview} section="receipts" onChanged={changed} />);
  expect(receipt).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Receipt ID"), { target: { value: "receipt" } });
  fireEvent.submit(screen.getByRole("form", { name: "Find receipt" }));
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Receipt belongs to another account.");
  fireEvent.submit(screen.getByRole("form", { name: "Find receipt" }));
  const inspect = await screen.findByRole("button", { name: "Inspect operation" });
  expect(request).not.toHaveBeenCalled();
  fireEvent.click(inspect);
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/deployments/deployment"));
});

it("ignores assignments returned after switching projects", async () => {
  let resolveFirst!: (items: Assignment[]) => void;
  vi.spyOn(automationClient, "assignments").mockImplementation(project => project === "p" ? new Promise(resolve => { resolveFirst = resolve; }) : Promise.resolve([{ projectId: "q", kind: "target", resourceId: "second-target" }]));
  vi.spyOn(infrastructureApi, "list").mockResolvedValue([]);
  vi.spyOn(api.api, "serviceTemplates").mockResolvedValue([]);
  render(<ResourcesPage overview={overview} section="assignments" onChanged={changed} />);
  fireEvent.change(screen.getByLabelText("Project"), { target: { value: "q" } });
  await screen.findByRole("table", { name: "Project resource assignments" });
  await act(async () => resolveFirst([{ projectId: "p", kind: "target", resourceId: "first-target" }]));
  expect(screen.queryByText("first-target")).toBeNull();
  expect(screen.getAllByText("second-target").length).toBeGreaterThan(0);
});

it("opens the existing clone form from the snapshots tab and gates mutations", async () => {
  const snapshot = { id: "snap", projectId: "p", providerId: "provider", name: "Database disk", state: "ready", retainUntil: "2099-01-01", evidence: { image: "ubuntu", consistency: "crash-consistent", disks: [] } };
  vi.spyOn(api, "request").mockImplementation(async path => path.endsWith("/snapshots") ? [snapshot] as never : [] as never);
  const view = render(<ResourcesPage overview={overview} section="machine-snapshots" onChanged={changed} />);
  fireEvent.click(await screen.findByRole("button", { name: "Restore isolated clone" }));
  expect((screen.getByLabelText("Machine name") as HTMLInputElement).value).toBe("Database disk clone");
  expect(screen.queryByRole("button", { name: "Create server" })).toBeNull();
  view.rerender(<ResourcesPage overview={{ ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { p: ["infrastructure.inspect"] } }} section="machine-snapshots" onChanged={changed} />);
  expect((await screen.findByRole("button", { name: "Restore isolated clone" })).hasAttribute("disabled")).toBe(true);
  expect(screen.queryByRole("form", { name: "Create on-demand server" })).toBeNull();
});

it("allows a snapshot operator to inspect an uncertain snapshot without machine modification access", async () => {
  const snapshot = { id: "snap", projectId: "p", providerId: "provider", name: "Uncertain disk", state: "unknown", revision: 2, retainUntil: "2099-01-01" };
  const request = vi.spyOn(api, "request").mockImplementation(async path => path.endsWith("/snapshots") ? [snapshot] as never : [] as never);
  const operator = { ...overview, identity: { ...overview.identity!, systemRole: "member" as const }, projectPermissions: { p: ["infrastructure.inspect", "infrastructure.snapshot"] as api.Permission[] } };
  render(<ResourcesPage overview={operator} section="machine-snapshots" onChanged={changed} />);
  const inspect = await screen.findByRole("button", { name: "Inspect uncertain snapshot" });
  expect(inspect.hasAttribute("disabled")).toBe(false);
  fireEvent.click(inspect);
  fireEvent.change(screen.getByLabelText("Provider snapshot ID"), { target: { value: "provider-snapshot" } });
  fireEvent.change(screen.getByLabelText("Type Uncertain disk to confirm"), { target: { value: "Uncertain disk" } });
  fireEvent.click(screen.getByRole("button", { name: "Verify snapshot disposition" }));
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/infrastructure/snapshots/snap/resolve", { method: "POST", body: JSON.stringify({ resourceId: "provider-snapshot", revision: 2, confirmName: "Uncertain disk" }) }));
});

it("loads assigned SSH key choices for a project operator without secret access", async () => {
  vi.spyOn(api, "request").mockImplementation(async path => path.endsWith("/infrastructure/assignments/p") ? [{ kind: "ssh_key", resourceId: "assigned-public-key" }] as never : [] as never);
  const operator = { ...overview, identity: { ...overview.identity!, systemRole: "member" as const }, projectPermissions: { p: ["infrastructure.inspect", "infrastructure.create"] as api.Permission[] } };
  render(<ResourcesPage overview={operator} section="machines" onChanged={changed} />);
  fireEvent.click(screen.getByRole("button", { name: "Create server" }));
  fireEvent.change(screen.getByLabelText("Project"), { target: { value: "p" } });
  expect(await screen.findByRole("option", { name: "assigned-public-key" })).toBeTruthy();
  fireEvent.change(screen.getByLabelText(/SSH public key/), { target: { value: "assigned-public-key" } });
  expect((screen.getByLabelText(/SSH public key/) as HTMLSelectElement).value).toBe("assigned-public-key");
});

it("matches snapshot operation controls to snapshot and delete grants", async () => {
  vi.spyOn(api, "request").mockImplementation(async path => path.endsWith("/operations") ? [{ id: "capture", action: "snapshot.create", state: "paused" }, { id: "delete", action: "snapshot.delete", state: "paused" }] as never : path.endsWith("/servers") ? [{ id: "machine", projectId: "p", name: "Machine", allocationState: "allocated", enrollmentState: "enrolled", runtimeState: "ready" }] as never : [] as never);
  const operator = { ...overview, identity: { ...overview.identity!, systemRole: "member" as const }, projectPermissions: { p: ["infrastructure.inspect", "infrastructure.snapshot"] as api.Permission[] } };
  render(<ResourcesPage overview={operator} section="machines" onChanged={changed} />);
  fireEvent.click(await screen.findByRole("button", { name: "Operations" }));
  const retries = await screen.findAllByRole("button", { name: "Retry saved operation" });
  expect(retries[0].hasAttribute("disabled")).toBe(false);
  expect(retries[1].hasAttribute("disabled")).toBe(true);
  expect(screen.getByRole("button", { name: "Cancel creation" }).hasAttribute("disabled")).toBe(false);
});
