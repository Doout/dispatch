// @vitest-environment jsdom
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import DispatchApp from "../App";
import * as client from "../api";
import type { WorkloadSection } from "../routes";
import { subscribeOverview } from "../overviewStream";
import { catalogClient } from "../deployments/catalogClient";
import { interfacePreferenceKey } from "./preference";

const enabledFeatures = { machineProvisioning: true, machineSnapshots: true, workloadBackups: true, automationCredentials: true, infrastructureAssignments: true, mutationReceipts: true };

const pages = vi.hoisted(() => ({ workloads: vi.fn() }));
vi.mock("../overviewStream", () => ({ subscribeOverview: vi.fn(() => () => {}) }));
vi.mock("../deployments/DeploymentsPage", () => ({ DeploymentsPage: () => <h1>Current deployment list</h1> }));
vi.mock("./WorkloadsPage", () => ({ WorkloadsPage: (props: { section: WorkloadSection }) => { pages.workloads(props); return <h1>Workloads {props.section}</h1>; } }));

function overview(id = "interface-member", owner = false): client.Overview {
  return {
    demo: false, secretStorageConfigured: true,
    identity: { id, username: id, displayName: id, systemRole: owner ? "owner" : "member", permissions: [] },
    controllerSettings: { operationsEnabled: false }, projectPermissions: {},
    projects: [], servers: [], apps: [], deployments: [], eventTriggers: [], previews: [],
    previewGroups: [], previewGroupRuns: [], secrets: [], githubApps: [], relayWebhooks: [],
  };
}

function path(value: string) { window.history.replaceState({}, "", value); }
function primary() { return within(screen.getByRole("complementary", { name: "Primary navigation" })); }

beforeEach(() => {
  localStorage.clear();
  path("/deployments");
  vi.spyOn(client.api, "overview").mockResolvedValue(overview());
  vi.spyOn(client, "request").mockResolvedValue({ operationsEnabled: false });
  vi.spyOn(catalogClient, "catalog").mockResolvedValue({ items: [] });
  vi.spyOn(window, "requestAnimationFrame").mockImplementation(callback => { callback(0); return 1; });
  vi.spyOn(window, "cancelAnimationFrame").mockImplementation(() => {});
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.clearAllMocks(); });

it("starts in the current interface and lets members switch without controller access", async () => {
  const user = userEvent.setup();
  render(<DispatchApp />);
  await screen.findByRole("heading", { name: "Current deployment list" });
  expect(primary().getByRole("link", { name: "Deployments" })).toBeTruthy();
  expect(primary().queryByRole("link", { name: "Workloads" })).toBeNull();
  expect(pages.workloads).not.toHaveBeenCalled();
  await user.click(primary().getByRole("link", { name: "Settings" }));
  const toggle = await screen.findByRole("switch", { name: "New interface" });
  expect((toggle as HTMLInputElement).checked).toBe(false);
  expect(screen.getByText("Owner access required")).toBeTruthy();
  expect(screen.queryByRole("switch", { name: "Operations" })).toBeNull();
  expect(client.request).not.toHaveBeenCalled();
  await user.click(toggle);
  expect(primary().getByRole("link", { name: "Workloads" })).toBeTruthy();
  expect(screen.getByRole("navigation", { name: "Settings pages" })).toBeTruthy();
  expect(localStorage.getItem(interfacePreferenceKey("interface-member"))).toBe("new");
  expect(client.request).not.toHaveBeenCalled();
  await user.click(screen.getByRole("switch", { name: "New interface" }));
  expect(primary().getByRole("link", { name: "Deployments" })).toBeTruthy();
  expect(localStorage.getItem(interfacePreferenceKey("interface-member"))).toBe("current");
});

it("restores the selected interface after reload and isolates account changes", async () => {
  const user = userEvent.setup(); path("/settings");
  const first = render(<DispatchApp />);
  await user.click(await screen.findByRole("switch", { name: "New interface" }));
  first.unmount();
  render(<DispatchApp />);
  await screen.findByRole("navigation", { name: "Settings pages" });
  expect((screen.getByRole("switch", { name: "New interface" }) as HTMLInputElement).checked).toBe(true);
  const stream = vi.mocked(subscribeOverview).mock.calls.at(-1)![0];
  act(() => stream(overview("other-member")));
  expect(primary().getByRole("link", { name: "Deployments" })).toBeTruthy();
  expect((screen.getByRole("switch", { name: "New interface" }) as HTMLInputElement).checked).toBe(false);
  expect(localStorage.getItem(interfacePreferenceKey("other-member"))).toBeNull();
  act(() => stream(overview()));
  expect(primary().getByRole("link", { name: "Workloads" })).toBeTruthy();
  expect((screen.getByRole("switch", { name: "New interface" }) as HTMLInputElement).checked).toBe(true);
});

it.each(["/workloads/parallel", "/infrastructure/machines", "/recovery/workload-backups", "/automation/receipts"])("gates direct route %s before mounting resource readers", async route => {
  path(route);
  render(<DispatchApp />);
  await screen.findByText("This page is part of the new interface. Enable it in Settings to continue.");
  expect(pages.workloads).not.toHaveBeenCalled();
  expect(client.request).not.toHaveBeenCalled();
  expect(window.location.pathname).toBe(route);
  await userEvent.setup().click(screen.getByRole("button", { name: "Open settings" }));
  expect(window.location.pathname).toBe("/settings");
  expect(screen.getByRole("switch", { name: "New interface" })).toBeTruthy();
});

it.each([
  ["/workloads/parallel", "Workloads parallel"],
  ["/infrastructure/machines", "Machines"],
  ["/recovery/workload-backups", "Workload backups"],
  ["/automation/receipts", "Receipts"],
])("opens saved direct route %s when the account has opted in", async (route, heading) => {
  vi.mocked(client.api.overview).mockResolvedValue({ ...overview(), controllerSettings: { operationsEnabled: false, uiFeatures: enabledFeatures } });
  localStorage.setItem(interfacePreferenceKey("interface-member"), "new");
  path(route);
  render(<DispatchApp />);
  await screen.findByRole("heading", { name: heading });
  expect(screen.queryByText(/Enable it in Settings to continue/)).toBeNull();
  expect(primary().getByRole("link", { name: "Workloads" })).toBeTruthy();
  expect(window.location.pathname).toBe(route);
});

it("keeps controller feature settings separate from a member's interface preference", async () => {
  path("/settings");
  vi.mocked(client.api.overview).mockResolvedValue(overview("interface-owner", true));
  render(<DispatchApp />);
  await screen.findByRole("switch", { name: "New interface" });
  const operations = screen.getByRole("switch", { name: "Operations" }) as HTMLInputElement;
  await waitFor(() => expect(operations.disabled).toBe(false));
  expect(client.request).toHaveBeenCalledWith("/api/v1/settings");
  vi.mocked(client.request).mockClear();
  await userEvent.setup().click(screen.getByRole("switch", { name: "New interface" }));
  expect(primary().getByRole("link", { name: "Workloads" })).toBeTruthy();
  expect(operations.checked).toBe(false);
  expect(client.request).not.toHaveBeenCalled();
});


it("requests automation credentials only after opting in and opening their page", async () => {
  path("/automation/credentials");
  vi.mocked(client.api.overview).mockResolvedValue({ ...overview("credentials-owner", true), controllerSettings: { operationsEnabled: false, uiFeatures: { automationCredentials: true } } });
  vi.mocked(client.request).mockImplementation(async <T,>(url: string) => (url === "/api/v1/settings" ? { operationsEnabled: false } : []) as T);
  const user = userEvent.setup();
  render(<DispatchApp />);
  await screen.findByText("This page is part of the new interface. Enable it in Settings to continue.");
  expect(client.request).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Open settings" }));
  await user.click(await screen.findByRole("switch", { name: "New interface" }));
  expect(vi.mocked(client.request).mock.calls.map(([url]) => url)).toEqual(["/api/v1/settings"]);
  await user.click(primary().getByRole("link", { name: "Automation" }));
  await screen.findByRole("heading", { name: "Credentials" });
  await waitFor(() => expect(client.request).toHaveBeenCalledWith("/api/v1/automation-accounts"));
  expect(window.location.pathname).toBe("/automation/credentials");
  await user.click(primary().getByRole("link", { name: "Settings" }));
  await user.click(screen.getByRole("switch", { name: "New interface" }));
  vi.mocked(client.request).mockClear();
  act(() => { path("/automation/credentials"); window.dispatchEvent(new PopStateEvent("popstate")); });
  await screen.findByText("This page is part of the new interface. Enable it in Settings to continue.");
  expect(client.request).not.toHaveBeenCalled();
});

it("keeps owner-only resource readers unavailable to members even with the new interface on", async () => {
  vi.mocked(client.api.overview).mockResolvedValue({ ...overview(), controllerSettings: { operationsEnabled: false, uiFeatures: { automationCredentials: true } } });
  localStorage.setItem(interfacePreferenceKey("interface-member"), "new");
  path("/automation/credentials");
  render(<DispatchApp />);
  await screen.findByText("A controller owner manages credentials.");
  expect(screen.getByRole("heading", { name: "Credentials" })).toBeTruthy();
  expect(within(screen.getByRole("navigation", { name: "Automation pages" })).queryByRole("link", { name: "Credentials" })).toBeNull();
  expect(client.request).not.toHaveBeenCalled();
});

it.each([
  ["/infrastructure/machines", "Machine provisioning"],
  ["/infrastructure/providers", "Machine provisioning"],
  ["/recovery/machine-snapshots", "Machine snapshots"],
  ["/recovery/workload-backups", "Workload backups"],
  ["/automation/credentials", "Automation credentials"],
  ["/automation/assignments", "Project assignments"],
  ["/automation/receipts", "Request receipts"],
])("does not let the new interface bypass a disabled feature at %s", async (route, label) => {
  localStorage.setItem(interfacePreferenceKey("interface-owner"), "new");
  vi.mocked(client.api.overview).mockResolvedValue(overview("interface-owner", true));
  path(route);
  render(<DispatchApp />);
  await screen.findByRole("heading", { name: `${label} is disabled` });
  expect(client.request).not.toHaveBeenCalled();
  expect(window.location.pathname).toBe(route);
});

it("applies a controller feature disable from the overview stream without reloading", async () => {
  const enabled = { ...overview("credentials-owner", true), controllerSettings: { operationsEnabled: false, uiFeatures: { automationCredentials: true } } };
  localStorage.setItem(interfacePreferenceKey("credentials-owner"), "new");
  vi.mocked(client.api.overview).mockResolvedValue(enabled);
  vi.mocked(client.request).mockResolvedValue([]);
  path("/automation/credentials");
  render(<DispatchApp />);
  await screen.findByRole("button", { name: "Create account" });
  await waitFor(() => expect(client.request).toHaveBeenCalledWith("/api/v1/automation-accounts"));
  vi.mocked(client.request).mockClear();
  const stream = vi.mocked(subscribeOverview).mock.calls.at(-1)![0];
  act(() => stream({ ...enabled, controllerSettings: { operationsEnabled: false, uiFeatures: { automationCredentials: false } } }));
  expect(screen.getByRole("heading", { name: "Automation credentials is disabled" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Create account" })).toBeNull();
  expect(primary().getByRole("link", { name: "Automation" }).getAttribute("href")).toBe("/events");
  expect(client.request).not.toHaveBeenCalled();
});
