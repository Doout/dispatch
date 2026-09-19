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
