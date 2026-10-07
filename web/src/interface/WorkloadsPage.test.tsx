// @vitest-environment jsdom
import { useState } from "react";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { App, Deployment, Overview, WorkflowResource } from "../api";
import type { DeploymentFilters } from "../routes";
import { catalogClient } from "../deployments/catalogClient";
import { WorkloadsPage } from "./WorkloadsPage";

const overview = {
  identity: { id: "viewer", systemRole: "member" }, projects: [{ id: "p", name: "Platform" }, { id: "q", name: "Research" }], servers: [], apps: [], previews: [], previewGroups: [], previewGroupRuns: [], workflowResources: [],
  configSources: [{ id: "config", projectId: "p", repository: "acme/checkout" }], deployments: [],
} as unknown as Overview;
const app = (id: string, projectId = "p"): App => ({ id, name: `App ${id}`, projectId, buildType: "dockerfile", state: "ready", createdAt: "2026-10-05T10:00:00Z", sourceRepo: `acme/${id}`, serverId: "target" }) as App;
const resource = (id: string, state: string): WorkflowResource => ({ id, name: id, kind: "Application", temporary: true, active: state !== "expired", state, configSourceId: "config", updatedAt: "2026-10-05T10:00:00Z", previewPullRequests: [{ repository: "acme/checkout", number: 109, url: "https://example.com/109" }] }) as WorkflowResource;
const run = (id: string, appId = "a"): Deployment => ({ id, appId, state: "succeeded", commitSha: "abcd1234", specDigest: "digest", message: "", createdAt: "2026-10-05T10:00:00Z", startedAt: "2026-10-05T10:00:01Z", finishedAt: "2026-10-05T10:01:02Z" });
const navigate = vi.fn();
afterEach(() => { cleanup(); vi.restoreAllMocks(); navigate.mockReset(); });

function Page({ section = "runs", data = overview, initial = {} }: { section?: "applications" | "parallel" | "runs"; data?: Overview; initial?: DeploymentFilters }) {
  const [filters, setFilters] = useState(initial);
  return <WorkloadsPage overview={data} section={section} onNavigate={navigate} onCreateApplication={vi.fn()} filters={filters} onFilters={setFilters} />;
}

it("paginates the application inventory and resets its page when the project or search changes", async () => {
  const user = userEvent.setup();
  render(<Page section="applications" data={{ ...overview, apps: [...Array.from({ length: 27 }, (_, index) => app(String(index).padStart(2, "0"))), app("research", "q")] }} />);
  const table = screen.getByRole("table", { name: "Applications" });
  expect(within(table).getAllByRole("row")).toHaveLength(26);
  await user.click(screen.getByRole("button", { name: "Next" }));
  expect(within(table).getAllByRole("row")).toHaveLength(4);
  await user.selectOptions(screen.getByLabelText("Project"), "q");
  expect(within(table).getByRole("link", { name: "App research" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Previous" }).hasAttribute("disabled")).toBe(true);
  await user.type(screen.getByRole("searchbox", { name: "Search applications" }), "absent");
  expect(screen.getByRole("heading", { name: "No matches" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "App research" })).toBeNull();
});

it("keeps cleanup pending in active parallel deployments and links to real workflow topology", async () => {
  const user = userEvent.setup();
  render(<Page section="parallel" data={{ ...overview, workflowResources: [resource("awaiting-cleanup", "expiring"), resource("old-preview", "expired")] }} />);
  expect(screen.getByText("Cleanup pending")).toBeTruthy();
  expect(screen.queryByRole("link", { name: "old-preview" })).toBeNull();
  await user.click(screen.getByRole("link", { name: "awaiting-cleanup" }));
  expect(navigate).toHaveBeenLastCalledWith({ view: "applications", applicationID: "awaiting-cleanup" });
  await user.click(screen.getByRole("button", { name: "All available" }));
  expect(screen.getByRole("link", { name: "old-preview" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Deleted" })).toBeNull();
  await user.type(screen.getByRole("searchbox", { name: "Search parallel deployments" }), "109");
  expect(screen.getByRole("link", { name: "awaiting-cleanup" })).toBeTruthy();
});

it("shows blocked cleanup as needing attention without treating it as removed", async () => {
  const blocked = { ...resource("blocked-cleanup", "expiring"), previewCleanups: [{ state: "blocked", error: "Target unavailable" }] } as WorkflowResource;
  const recovered = { ...blocked, id: "recovered", name: "recovered", state: "ready" };
  const user = userEvent.setup();
  render(<Page section="parallel" data={{ ...overview, workflowResources: [blocked, recovered] }} />);
  await user.click(screen.getByRole("button", { name: "Needs attention" }));
  expect(screen.getByRole("link", { name: "blocked-cleanup" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "recovered" })).toBeNull();
  expect(screen.getByText("Cleanup failed").title).toBe("Target unavailable");
});

it("queries retained run pages and preserves Analytics completion filters through pagination", async () => {
  const search = vi.spyOn(catalogClient, "search").mockImplementation(async params => params.has("before") ? { items: [run("older-run")] } : { items: [run("newer-run")], next: "newer-run" });
  const user = userEvent.setup();
  const initial = { project: "p", status: "succeeded", completedFrom: "2026-10-01T00:00:00Z", completedTo: "2026-10-06T00:00:00Z" };
  render(<Page data={{ ...overview, apps: [app("a")] }} initial={initial} />);
  await screen.findByRole("link", { name: "ewer-run" });
  const first = search.mock.calls[0][0];
  expect(Object.fromEntries(first)).toEqual(initial);
  await user.click(screen.getByRole("button", { name: "Next" }));
  await screen.findByRole("link", { name: "lder-run" });
  expect(Object.fromEntries(search.mock.calls.at(-1)![0])).toEqual({ ...initial, before: "newer-run" });
  expect(screen.queryByRole("link", { name: "ewer-run" })).toBeNull();
  expect(screen.getByText("1m 1s")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Previous" }));
  await screen.findByRole("link", { name: "ewer-run" });
  await user.selectOptions(screen.getByLabelText("Status"), "failed");
  await waitFor(() => expect(search.mock.calls.at(-1)![0].get("status")).toBe("failed"));
  expect(search.mock.calls.at(-1)![0].has("before")).toBe(false);
  expect(search.mock.calls.at(-1)![0].get("completedFrom")).toBe(initial.completedFrom);
});

it("ignores a stale request after a project filter changes", async () => {
  let resolveFirst!: (value: { items: Deployment[] }) => void;
  const search = vi.spyOn(catalogClient, "search").mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; })).mockResolvedValue({ items: [run("research-run", "research")] });
  const user = userEvent.setup();
  render(<Page data={{ ...overview, apps: [app("a"), app("research", "q")] }} />);
  await waitFor(() => expect(search).toHaveBeenCalledTimes(1));
  await user.selectOptions(screen.getByLabelText("Project"), "q");
  await screen.findByRole("link", { name: "arch-run" });
  resolveFirst({ items: [run("stale-run")] });
  await waitFor(() => expect(screen.queryByRole("link", { name: "tale-run" })).toBeNull());
  expect(screen.getByRole("link", { name: "arch-run" })).toBeTruthy();
});

it("retries a failed history request and leaves modified-click navigation to the browser", async () => {
  vi.spyOn(catalogClient, "search").mockRejectedValueOnce(new Error("History unavailable")).mockResolvedValue({ items: [run("loaded-run")] });
  const user = userEvent.setup();
  render(<Page data={{ ...overview, apps: [app("a")] }} />);
  expect(await screen.findByRole("alert")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Retry" }));
  const link = await screen.findByRole("link", { name: "aded-run" });
  document.addEventListener("click", event => { expect(event.defaultPrevented).toBe(false); event.preventDefault(); }, { once: true });
  fireEvent.click(link, { ctrlKey: true });
  expect(navigate).not.toHaveBeenCalled();
  await user.click(link);
  expect(navigate).toHaveBeenLastCalledWith({ view: "deployments", deploymentID: "loaded-run" });
});

it("shows creation only for a project manager with a ready Docker target", () => {
  const create = vi.fn();
  const data = { ...overview, servers: [{ id: "docker", state: "ready", runtime: "docker" }] } as Overview;
  const page = render(<WorkloadsPage overview={data} section="applications" onNavigate={navigate} onCreateApplication={create} />);
  expect(screen.queryByRole("button", { name: "Add application" })).toBeNull();
  page.rerender(<WorkloadsPage overview={{ ...data, projectPermissions: { p: ["project.configure"] } }} section="applications" onNavigate={navigate} onCreateApplication={create} />);
  fireEvent.click(screen.getByRole("button", { name: "Add application" }));
  expect(create).toHaveBeenCalledTimes(1);
  page.rerender(<WorkloadsPage overview={{ ...data, projectPermissions: { p: ["project.configure"] }, servers: [{ ...data.servers[0], runtime: "kubernetes" }] }} section="applications" onNavigate={navigate} onCreateApplication={create} />);
  expect(screen.queryByRole("button", { name: "Add application" })).toBeNull();
});
