// @vitest-environment jsdom
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api, type Deployment, type DeploymentComparison, type DeploymentHistoryPage } from "../api";
import { DeploymentHistory } from "./DeploymentHistory";
const current = { id: "new", appId: "app", commitSha: "new-sha", state: "succeeded", createdAt: "2026-09-19T00:00:00Z" } as Deployment;
const older = { ...current, id: "old", commitSha: "old-sha", createdAt: "2026-09-18T00:00:00Z" };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("compares saved versions, filters changes, and loads older runs", async () => {
 const history = vi.spyOn(api, "applicationHistory").mockResolvedValueOnce({ items: [current, older], next: "old" }).mockResolvedValueOnce({ items: [{ ...older, id: "oldest" }] });
 const compare = vi.spyOn(api, "compareDeployments").mockResolvedValue({ fromId: "old", toId: "new", available: true, hidden: 1, truncated: false, message: "Saved inputs.", changes: [{ path: "/values/replicas", kind: "changed", before: 2, after: 3 }, { path: "/values/image", kind: "added", before: null, after: "api:v2" }] });
 render(<DeploymentHistory deployment={current} />);
 await screen.findByText("/values/replicas"); expect(compare).toHaveBeenCalledWith("new", "old"); expect(screen.getByText("Sensitive fields excluded")).toBeTruthy();
 const user = userEvent.setup(); await user.type(screen.getByRole("textbox", { name: "Filter changed fields" }), "replicas"); expect(screen.queryByText("/values/image")).toBeNull();
 await user.click(screen.getByRole("button", { name: "Load older deployments" })); await waitFor(() => expect(history).toHaveBeenLastCalledWith("app", "old"));
 await user.selectOptions(screen.getByRole("combobox", { name: "Compare from deployment" }), "oldest"); await waitFor(() => expect(compare).toHaveBeenLastCalledWith("new", "oldest"));
});

const repeatOne = { ...older, id: "repeat-one", commitSha: "repeat-1", createdAt: "2026-09-18T23:00:00Z" };
const repeatTwo = { ...older, id: "repeat-two", commitSha: "repeat-2", createdAt: "2026-09-18T22:00:00Z" };
const distinct = { ...older, id: "distinct", commitSha: "distinct", createdAt: "2026-09-18T21:00:00Z" };
const savedComparison: DeploymentComparison = { fromId: "distinct", toId: "new", available: true, hidden: 0, truncated: false, message: "Saved inputs.", changes: [{ path: "/values/replicas", kind: "changed", before: 2, after: 3 }] };
function deferred<T>() {
 let resolve!: (value: T) => void;
 let reject!: (cause: Error) => void;
 const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
 return { promise, resolve, reject };
}

it("collapses only linked adjacent successes and compares the previous distinct group", async () => {
 const failed = { ...distinct, id: "failed", commitSha: "failed", state: "failed" } as Deployment;
 const afterFailed = { ...distinct, id: "after-failed", commitSha: "afterbad" };
 const unknown = { ...distinct, id: "unknown", commitSha: "unknown", state: "queued" } as Deployment;
 const afterUnknown = { ...distinct, id: "after-unknown", commitSha: "afterunk" };
 const unlinked = { ...distinct, id: "unlinked", commitSha: "unlinked" };
 vi.spyOn(api, "applicationHistory").mockResolvedValue({ items: [current, repeatOne, repeatTwo, failed, afterFailed, unknown, afterUnknown, unlinked], repeats: { [repeatOne.id]: current.id, [repeatTwo.id]: repeatOne.id, [afterFailed.id]: failed.id, [afterUnknown.id]: unknown.id, [unlinked.id]: current.id } });
 const compare = vi.spyOn(api, "compareDeployments").mockResolvedValue(savedComparison);
 render(<DeploymentHistory deployment={current} />);
 await screen.findByRole("button", { name: "Show 2 repeats for deployment new" });
 await waitFor(() => expect(compare).toHaveBeenCalledWith("new", "failed"));
 expect(screen.queryByRole("link", { name: "repeat-1" })).toBeNull();
 expect(screen.queryByRole("link", { name: "repeat-2" })).toBeNull();
 for (const name of ["failed", "afterbad", "unknown", "afterunk", "unlinked"]) expect(screen.getByRole("link", { name })).toBeTruthy();
 expect(screen.getByRole("combobox", { name: "Compare to deployment" }).querySelectorAll("option")).toHaveLength(8);
 expect(screen.getByText(/Hooks and other effects can differ/)).toBeTruthy();
});

it("merges repeats across page boundaries and expands each group or all runs without losing raw selectors", async () => {
 const anotherRepeat = { ...older, id: "another-repeat", commitSha: "repeat-3" };
 const history = vi.spyOn(api, "applicationHistory").mockResolvedValueOnce({ items: [current, repeatOne], repeats: { [repeatOne.id]: current.id }, next: repeatOne.id }).mockResolvedValueOnce({ items: [repeatTwo, distinct, anotherRepeat], repeats: { [repeatTwo.id]: repeatOne.id, [anotherRepeat.id]: distinct.id } });
 const compare = vi.spyOn(api, "compareDeployments").mockResolvedValue(savedComparison);
 render(<DeploymentHistory deployment={current} />);
 const user = userEvent.setup();
 const loadOlder = await screen.findByRole("button", { name: "Load older deployments" });
 expect(screen.queryByRole("link", { name: "repeat-1" })).toBeNull();
 expect(compare).not.toHaveBeenCalled();
 await user.click(loadOlder);
 await screen.findByRole("button", { name: "Show 2 repeats for deployment new" });
 expect(history).toHaveBeenLastCalledWith("app", "repeat-one");
 await waitFor(() => expect(compare).toHaveBeenLastCalledWith("new", "distinct"));
 expect(screen.queryByRole("link", { name: "repeat-2" })).toBeNull();
 expect(screen.getByRole("combobox", { name: "Compare to deployment" }).querySelectorAll("option")).toHaveLength(5);
 await user.click(screen.getByRole("button", { name: "Show 2 repeats for deployment new" }));
 expect(screen.getByRole("link", { name: "repeat-1" })).toBeTruthy();
 expect(screen.getByRole("link", { name: "repeat-2" })).toBeTruthy();
 expect(screen.queryByRole("link", { name: "repeat-3" })).toBeNull();
 await user.click(screen.getByRole("button", { name: "Show all runs" }));
 expect(screen.getByRole("link", { name: "repeat-3" })).toBeTruthy();
 await user.click(screen.getByRole("button", { name: "Hide 2 repeats for deployment new" }));
 expect(screen.queryByRole("link", { name: "repeat-2" })).toBeNull();
 expect(screen.getByRole("link", { name: "repeat-3" })).toBeTruthy();
 await user.click(screen.getByRole("button", { name: "Show all runs" }));
 await user.click(screen.getByRole("button", { name: "Collapse repeats" }));
 expect(screen.queryByRole("link", { name: "repeat-1" })).toBeNull();
 expect(screen.queryByRole("link", { name: "repeat-3" })).toBeNull();
 expect(history).toHaveBeenCalledTimes(2);
});

it("keeps deep-linked and compared repeats visible and preserves loaded state when navigating between them", async () => {
 const history = vi.spyOn(api, "applicationHistory").mockResolvedValue({ items: [current, repeatOne, repeatTwo, distinct], repeats: { [repeatOne.id]: current.id, [repeatTwo.id]: repeatOne.id } });
 const compare = vi.spyOn(api, "compareDeployments").mockResolvedValue(savedComparison);
 const navigate = vi.fn();
 const view = render(<DeploymentHistory deployment={repeatOne} onSelectDeployment={navigate} />);
 const user = userEvent.setup();
 await screen.findByRole("button", { name: "Hide 2 repeats for deployment new" });
 expect(screen.getByRole("link", { name: "repeat-1 Viewing" }).getAttribute("aria-current")).toBe("page");
 await user.type(await screen.findByRole("textbox", { name: "Filter changed fields" }), "replicas");
 await user.click(screen.getByRole("button", { name: "Collapse repeats" }));
 expect(screen.getByRole("link", { name: "repeat-1 Viewing" })).toBeTruthy();
 expect(screen.queryByRole("link", { name: "repeat-2" })).toBeNull();
 await user.selectOptions(screen.getByRole("combobox", { name: "Compare from deployment" }), repeatTwo.id);
 expect(screen.getByRole("link", { name: "repeat-2" })).toBeTruthy();
 await waitFor(() => expect(compare).toHaveBeenLastCalledWith(repeatOne.id, repeatTwo.id));
 await user.click(screen.getByRole("button", { name: "Collapse repeats" }));
 expect(screen.getByRole("link", { name: "repeat-2" })).toBeTruthy();
 const list = document.querySelector(".history-runs")!; list.scrollTop = 100;
 await user.click(screen.getByRole("link", { name: "repeat-2" }));
 expect(navigate).toHaveBeenCalledWith(repeatTwo.id);
 view.rerender(<DeploymentHistory deployment={repeatTwo} onSelectDeployment={navigate} />);
 await screen.findByRole("link", { name: "repeat-2 Viewing" });
 expect(screen.getByRole("button", { name: "Hide 2 repeats for deployment new" })).toBeTruthy();
 expect(document.querySelector(".history-runs")).toBe(list);
 expect(list.scrollTop).toBe(100);
 expect((screen.getByRole("textbox", { name: "Filter changed fields" }) as HTMLInputElement).value).toBe("replicas");
 expect(history).toHaveBeenCalledTimes(1);
});

it("shows a deep-linked run outside the loaded page and retains explicit comparison choices after loading more", async () => {
 const more = deferred<DeploymentHistoryPage>();
 const history = vi.spyOn(api, "applicationHistory").mockResolvedValueOnce({ items: [current, repeatOne], next: repeatOne.id }).mockReturnValueOnce(more.promise);
 const compare = vi.spyOn(api, "compareDeployments").mockResolvedValue(savedComparison);
 render(<DeploymentHistory deployment={older} />);
 await screen.findByRole("link", { name: "old-sha Viewing" });
 const user = userEvent.setup();
 await user.selectOptions(screen.getByRole("combobox", { name: "Compare from deployment" }), current.id);
 await user.click(screen.getByRole("button", { name: "Load older deployments" }));
 expect(screen.getByRole("link", { name: "old-sha Viewing" })).toBeTruthy();
 await act(async () => more.resolve({ items: [repeatTwo, distinct] }));
 await screen.findByRole("link", { name: "distinct" });
 expect((screen.getByRole("combobox", { name: "Compare from deployment" }) as HTMLSelectElement).value).toBe(current.id);
 expect(compare).toHaveBeenLastCalledWith(older.id, current.id);
 expect(history).toHaveBeenCalledTimes(2);
});

it("discards an initial history response after switching applications", async () => {
 const pending = deferred<DeploymentHistoryPage>();
 const other = { ...current, id: "other", appId: "another-app", commitSha: "otherapp" };
 vi.spyOn(api, "applicationHistory").mockReturnValueOnce(pending.promise).mockResolvedValueOnce({ items: [other] });
 const compare = vi.spyOn(api, "compareDeployments").mockResolvedValue(savedComparison);
 const view = render(<DeploymentHistory deployment={current} />);
 view.rerender(<DeploymentHistory deployment={other} />);
 await screen.findByRole("link", { name: "otherapp Viewing" });
 await act(async () => pending.resolve({ items: [current, repeatOne], repeats: { [repeatOne.id]: current.id } }));
 expect(screen.queryByRole("link", { name: /new-sha/ })).toBeNull();
 expect(screen.queryByRole("button", { name: "Show all runs" })).toBeNull();
 expect(compare).not.toHaveBeenCalled();
});

it("ignores stale pagination, comparison, and navigation failures after switching applications", async () => {
 const more = deferred<DeploymentHistoryPage>();
 const comparison = deferred<DeploymentComparison>();
 const navigation = deferred<void>();
 const other = { ...current, id: "other", appId: "another-app", commitSha: "otherapp" };
 vi.spyOn(api, "applicationHistory").mockResolvedValueOnce({ items: [current, older], next: older.id }).mockReturnValueOnce(more.promise).mockResolvedValueOnce({ items: [other] });
 vi.spyOn(api, "compareDeployments").mockReturnValue(comparison.promise);
 const navigate = vi.fn(() => navigation.promise);
 const view = render(<DeploymentHistory deployment={current} onSelectDeployment={navigate} />);
 const user = userEvent.setup();
 await user.click(await screen.findByRole("link", { name: "old-sha" }));
 await user.click(screen.getByRole("button", { name: "Load older deployments" }));
 view.rerender(<DeploymentHistory deployment={other} />);
 await screen.findByRole("link", { name: "otherapp Viewing" });
 await act(async () => { more.resolve({ items: [repeatOne], repeats: { [repeatOne.id]: older.id } }); comparison.resolve(savedComparison); navigation.reject(new Error("Old navigation failed")); });
 expect(screen.queryByRole("link", { name: "repeat-1" })).toBeNull();
 expect(screen.queryByText("/values/replicas")).toBeNull();
 expect(screen.queryByRole("alert")).toBeNull();
});
it("does not claim no changes when a snapshot is unavailable", async () => {
 vi.spyOn(api, "applicationHistory").mockResolvedValue({ items: [current, older] });
 vi.spyOn(api, "compareDeployments").mockResolvedValue({ fromId: "old", toId: "new", available: false, hidden: 0, truncated: false, message: "Saved inputs are unavailable.", changes: [] });
 render(<DeploymentHistory deployment={current} />); await screen.findByText("Comparison unavailable"); expect(screen.queryByText(/No visible input changes/)).toBeNull();
});
it("navigates in-app while retaining loaded history, filters, and the list element", async () => {
 const oldest = { ...older, id: "oldest", commitSha: "oldest-sha", createdAt: "2026-09-17T00:00:00Z" };
 const history = vi.spyOn(api, "applicationHistory").mockResolvedValueOnce({ items: [current, older], next: "old" }).mockResolvedValueOnce({ items: [oldest] });
 vi.spyOn(api, "compareDeployments").mockResolvedValue({ fromId: "old", toId: "new", available: true, hidden: 0, truncated: false, message: "Saved inputs.", changes: [{ path: "/values/replicas", kind: "changed", before: 2, after: 3 }] });
 const navigate = vi.fn();
 const view = render(<DeploymentHistory deployment={current} onSelectDeployment={navigate} />);
 const user = userEvent.setup(); await screen.findByText("/values/replicas");
 await user.type(screen.getByRole("textbox", { name: "Filter changed fields" }), "replicas");
 await user.click(screen.getByRole("button", { name: "Load older deployments" }));
 const link = await screen.findByRole("link", { name: "oldest-s" });
 const list = document.querySelector(".history-runs")!; list.scrollTop = 170;
 let prevented = false;
 const observeClick = (event: MouseEvent) => { prevented = event.defaultPrevented; event.preventDefault(); };
 document.addEventListener("click", observeClick);
 try {
  await user.click(link); expect(prevented).toBe(true); expect(navigate).toHaveBeenCalledWith("oldest");
  navigate.mockClear();
  link.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true }));
  expect(prevented).toBe(false); expect(navigate).not.toHaveBeenCalled();
 } finally { document.removeEventListener("click", observeClick); }
 view.rerender(<DeploymentHistory deployment={older} onSelectDeployment={navigate} />);
 await waitFor(() => expect((screen.getByRole("combobox", { name: "Compare to deployment" }) as HTMLSelectElement).value).toBe("old"));
 expect(history).toHaveBeenCalledTimes(2);
 expect(document.querySelector(".history-runs")).toBe(list); expect(list.scrollTop).toBe(170);
 expect((screen.getByRole("textbox", { name: "Filter changed fields" }) as HTMLInputElement).value).toBe("replicas");
 expect(screen.getByRole("link", { name: "oldest-s" })).toBeTruthy();
});
