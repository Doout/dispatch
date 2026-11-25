// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import { hasHelmOwnershipConflict, HelmTakeoverPanel } from "./HelmTakeoverPanel";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("requires a new inspection and explicit confirmation after the release changes", async () => {
  const review = { release: "slot1", namespace: "slots", target: "Cluster", revision: 432, previousAppId: "old-app", previousProjectId: "old-project", version: "first" };
  vi.spyOn(api, "reviewHelmTakeover").mockResolvedValueOnce(review).mockResolvedValueOnce({ ...review, revision: 433, version: "second" });
  const accept = vi.spyOn(api, "acceptHelmTakeover").mockRejectedValueOnce(new Error("The release changed.")).mockResolvedValueOnce(review);
  const user = userEvent.setup();
  render(<HelmTakeoverPanel appId="current" />);
  await user.click(screen.getByRole("button", { name: "Inspect release ownership" }));
  expect(screen.getByRole("heading", { name: "Take over slot1?" })).toBeTruthy();
  expect(screen.getByText("slots")).toBeTruthy();
  await user.click(screen.getByText("Ownership details"));
  expect(screen.getByText("old-project")).toBeTruthy();
  expect((screen.getByRole("button", { name: "Take over release" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Take over release" }));
  expect((await screen.findByRole("alert")).textContent).toContain("The release changed.");
  await user.click(screen.getByRole("button", { name: "Inspect release ownership" }));
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Take over release" }));
  expect((await screen.findByRole("status")).textContent).toContain("slot1 now belongs to");
  expect(accept.mock.calls).toEqual([["current", "first"], ["current", "second"]]);
});

it.each(["Dispatch", "Doodship"])("recognizes ownership conflicts recorded under %s", product => {
  expect(hasHelmOwnershipConflict(`Deployment failed: The Helm release does not belong to this ${product} application and namespace. Check the release name.`)).toBe(true);
});

it("does not offer takeover for unrelated deployment failures", () => {
  expect(hasHelmOwnershipConflict("Helm release upgrade failed.")).toBe(false);
  expect(hasHelmOwnershipConflict()).toBe(false);
});
