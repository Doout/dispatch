// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api, type Deployment } from "../api";
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
