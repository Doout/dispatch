// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import * as api from "../api";
import type { Overview } from "../api";
import { RetentionControls } from "./Maintenance";
import { RuntimeRetentionPanel, type RetentionPolicy, type RuntimeRetentionReview } from "./RuntimeRetentionPanel";
const policy: RetentionPolicy = { projectId: "p", logDays: 30, runDays: 90, keepRuns: 20, imageDays: 14, stoppedRevisionDays: 7, keepRollbackRevisions: 5 };
const overview = { identity: { id: "owner", systemRole: "owner" }, projects: [{ id: "p", name: "Platform" }], servers: [{ id: "s", name: "Build host" }], apps: [{ id: "a", name: "Orders" }] } as unknown as Overview;
const review: RuntimeRetentionReview = { id: "cleanup-original", projectId: "p", digest: "original-digest", policy, state: "planned", createdAt: "2026-10-01T12:00:00Z", expiresAt: "2099-10-01T13:00:00Z", items: [{ key: "old", kind: "image", serverId: "s", appId: "a", name: "old-image", identity: "sha256:old", createdAt: "2026-01-01T00:00:00Z", protected: [] }, { key: "live", kind: "revision", serverId: "s", appId: "a", name: "current-release", identity: "container-live", createdAt: "2026-10-01T12:00:00Z", protected: ["Current serving release"] }], results: [] };
beforeEach(() => vi.restoreAllMocks()); afterEach(cleanup);
function panel() { return render(<RuntimeRetentionPanel policy={policy} overview={overview} onPolicyChanged={vi.fn()} />); }
async function preview() { fireEvent.click(screen.getByRole("button", { name: "Preview runtime cleanup" })); await screen.findByLabelText("Runtime cleanup review"); }
it("shows protected reasons and requires confirmation for the exact saved candidates", async () => {
  const request = vi.spyOn(api, "request").mockResolvedValue({ applied: false, runtime: review }); panel(); await preview();
  expect(screen.getByText("Current serving release")).toBeTruthy();
  const remove = screen.getByRole("button", { name: "Remove reviewed artifacts" }) as HTMLButtonElement; expect(remove.disabled).toBe(true);
  expect(request.mock.calls.some(([path]) => path.endsWith("/apply"))).toBe(false);
  request.mockResolvedValue({ applied: true, runtime: { ...review, state: "succeeded", results: [{ key: "old", state: "removed", message: "Owned image removed" }] } });
  fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(remove);
  await screen.findByText("Reviewed cleanup completed");
  expect(request).toHaveBeenLastCalledWith("/api/v1/projects/p/retention/apply", { method: "POST", body: JSON.stringify({ scope: "runtime", confirm: "p", expectedPolicy: policy, runtimeReviewId: review.id, runtimeReviewDigest: review.digest }) });
  expect(screen.queryByRole("checkbox")).toBeNull();
});
it("retries accepted partial work past expiry using the same review", async () => {
  const partial = { ...review, state: "partial", expiresAt: "2000-01-01T00:00:00Z", results: [{ key: "old", state: "failed", message: "Target disconnected; retry this receipt" }] };
  const request = vi.spyOn(api, "request").mockResolvedValue({ applied: false, runtime: partial }); panel(); await preview();
  expect(screen.getByText("Target disconnected; retry this receipt")).toBeTruthy();
  request.mockResolvedValue({ applied: true, runtime: { ...partial, state: "succeeded" } });
  fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(screen.getByRole("button", { name: "Retry reviewed items" }));
  await screen.findByText("Reviewed cleanup completed");
  const body = JSON.parse(String(request.mock.calls.at(-1)?.[1]?.body)); expect(body.runtimeReviewId).toBe(review.id); expect(body.runtimeReviewDigest).toBe(review.digest);
});
it("refuses expired unaccepted reviews and never sends their deletion request", async () => {
  const request = vi.spyOn(api, "request").mockResolvedValue({ applied: false, runtime: { ...review, expiresAt: "2000-01-01T00:00:00Z" } }); panel(); await preview();
  fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(screen.getByRole("button", { name: "Remove reviewed artifacts" }));
  await screen.findByText("This cleanup review expired. Preview the current candidates again."); expect(request.mock.calls.some(([path]) => path.endsWith("/apply"))).toBe(false);
});
it("reopens a saved receipt and keeps stale-policy results read-only", async () => {
  const request = vi.spyOn(api, "request").mockResolvedValue({ ...review, state: "partial", policy: { ...policy, imageDays: 30 } }); panel();
  fireEvent.change(screen.getByLabelText("Cleanup receipt ID"), { target: { value: review.id } }); fireEvent.click(screen.getByRole("button", { name: "Load receipt" }));
  await screen.findByText(/This receipt uses an older policy/); expect(screen.queryByRole("checkbox")).toBeNull();
  expect(request).toHaveBeenCalledWith("/api/v1/projects/p/retention/runtime-reviews/cleanup-original");
});
it("preserves runtime limits when saving a history edit", async () => {
  const request = vi.spyOn(api, "request").mockImplementation(async <T,>(_path: string, init?: RequestInit) => (init?.method === "PUT" ? JSON.parse(String(init.body)) : policy) as T);
  render(<RetentionControls overview={overview} projectId="p" />);
  fireEvent.click(await screen.findByRole("button", { name: "Edit policy" })); fireEvent.change(screen.getByRole("spinbutton", { name: "Keep logs for" }), { target: { value: "7" } }); fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
  await screen.findByText("Policy saved. Preview cleanup to see what is eligible.");
  const put = request.mock.calls.find(([, init]) => init?.method === "PUT"); expect(JSON.parse(String(put?.[1]?.body))).toEqual({ ...policy, logDays: 7 });
});
it("preserves history limits while changing runtime policy", async () => {
  const changed = vi.fn(); const request = vi.spyOn(api, "request").mockImplementation(async <T,>(_path: string, init?: RequestInit) => JSON.parse(String(init?.body)) as T);
  render(<RuntimeRetentionPanel policy={policy} overview={overview} onPolicyChanged={changed} />);
  fireEvent.click(screen.getByRole("button", { name: "Edit runtime policy" })); fireEvent.change(screen.getByRole("spinbutton", { name: "Retain unused images for" }), { target: { value: "0" } }); fireEvent.click(screen.getByRole("button", { name: "Save runtime policy" }));
  await waitFor(() => expect(changed).toHaveBeenCalledWith({ ...policy, imageDays: 0 })); expect(request.mock.calls.some(([path]) => path.endsWith("/apply"))).toBe(false);
});
it("ignores a review that arrives after project access is removed", async () => {
  let finish!: (value: unknown) => void;
  vi.spyOn(api, "request").mockImplementation(async <T,>(path: string) => path.endsWith("/preview") ? new Promise<T>(resolve => { finish = value => resolve(value as T); }) : policy as T);
  const view = render(<RetentionControls overview={overview} projectId="p" />);
  fireEvent.click(await screen.findByRole("button", { name: "Preview runtime cleanup" }));
  view.rerender(<RetentionControls overview={{ ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { p: ["project.view"] } }} projectId="p" />);
  finish({ runtime: review }); await screen.findByText("Project admin access required"); expect(screen.queryByLabelText("Runtime cleanup review")).toBeNull();
});
