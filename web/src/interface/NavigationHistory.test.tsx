// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import DispatchApp from "../App";
import { api, type Overview } from "../api";
import { catalogClient } from "../deployments/catalogClient";
import type { AppRoute, DeploymentFilters, WorkloadSection } from "../routes";
import { interfacePreferenceKey } from "./preference";

vi.mock("../overviewStream", () => ({ subscribeOverview: vi.fn(() => () => {}) }));
vi.mock("./WorkloadsPage", () => ({
  WorkloadsPage: ({ section, filters, onNavigate }: { section: WorkloadSection; filters?: DeploymentFilters; onNavigate: (route: AppRoute) => void }) => <>
    <h1>{section === "parallel" ? "Parallel deployments" : "Applications"}</h1>
    <p role="status">{filters?.project || "All projects"} / {filters?.query || "No search"}</p>
    <button onClick={() => onNavigate({ view: "applications", applicationID: "parallel-checkout" })}>Open checkout topology</button>
  </>,
}));
vi.mock("../ApplicationsPage", () => ({
  ApplicationsPage: ({ applicationID, onCloseTopology, onOpenTopology }: { applicationID?: string; onCloseTopology: () => void; onOpenTopology: (resource: { id: string }) => void }) => <>
    <h1>Topology {applicationID}</h1>
    <button onClick={onCloseTopology}>Back to workloads</button>
    <button onClick={() => onOpenTopology({ id: "related-workflow" })}>Open related topology</button>
  </>,
}));

const overview: Overview = {
  demo: false, secretStorageConfigured: true,
  identity: { id: "history-member", username: "history-member", displayName: "History member", systemRole: "member", permissions: [] },
  projects: [{ id: "platform", name: "Platform", description: "", createdAt: "2026-10-01T00:00:00Z" }],
  projectPermissions: { platform: ["project.view"] },
  servers: [], apps: [], deployments: [], eventTriggers: [], previews: [], previewGroups: [], previewGroupRuns: [], secrets: [], githubApps: [], relayWebhooks: [],
};
const origin = "/workloads/parallel?project=platform&q=checkout+109";

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem(interfacePreferenceKey("history-member"), "new");
  window.history.replaceState({ dispatchRoute: true, selection: "parallel-checkout" }, "", origin);
  vi.spyOn(api, "overview").mockResolvedValue(overview);
  vi.spyOn(catalogClient, "catalog").mockResolvedValue({ items: [] });
  vi.spyOn(window, "requestAnimationFrame").mockImplementation(callback => { callback(0); return 1; });
  vi.spyOn(window, "cancelAnimationFrame").mockImplementation(() => {});
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it.each([1, 2])("returns through %i topology history entries to the filtered Parallel deployments list", async depth => {
  const user = userEvent.setup();
  const historyGo = vi.spyOn(window.history, "go");
  render(<DispatchApp />);
  await screen.findByRole("heading", { name: "Parallel deployments" });
  expect(screen.getByRole("status").textContent).toBe("platform / checkout 109");
  const content = document.getElementById("page-content")!;
  content.scrollTop = 420;
  await user.click(screen.getByRole("button", { name: "Open checkout topology" }));
  expect(window.location.pathname).toBe("/applications/parallel-checkout/topology");
  expect(window.history.state.applicationReturnDepth).toBe(1);
  if (depth === 2) {
    await user.click(screen.getByRole("button", { name: "Open related topology" }));
    expect(window.location.pathname).toBe("/applications/related-workflow/topology");
    expect(window.history.state.applicationReturnDepth).toBe(2);
  }
  await user.click(screen.getByRole("button", { name: "Back to workloads" }));
  expect(historyGo).toHaveBeenCalledWith(-depth);
  await screen.findByRole("heading", { name: "Parallel deployments" });
  await waitFor(() => expect(`${window.location.pathname}${window.location.search}`).toBe(origin));
  expect(screen.getByRole("status").textContent).toBe("platform / checkout 109");
  expect(window.history.state).toEqual({ dispatchRoute: true, selection: "parallel-checkout", scrollTop: 420 });
  expect(document.getElementById("page-content")!.scrollTop).toBe(420);
});

it("returns a directly opened topology to Applications without traversing unrelated history", async () => {
  window.history.replaceState({}, "", "/applications/parallel-checkout/topology");
  const historyGo = vi.spyOn(window.history, "go");
  const user = userEvent.setup();
  render(<DispatchApp />);
  await screen.findByRole("heading", { name: "Topology parallel-checkout" });
  await user.click(screen.getByRole("button", { name: "Back to workloads" }));
  await screen.findByRole("heading", { name: "Applications" });
  expect(window.location.pathname).toBe("/workloads/applications");
  expect(window.location.search).toBe("");
  expect(historyGo).not.toHaveBeenCalled();
  expect(window.history.state.applicationReturnDepth).toBeUndefined();
});
