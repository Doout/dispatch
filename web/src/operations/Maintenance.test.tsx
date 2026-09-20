// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api";
import type { Overview } from "../api";
import { Backups, RetentionControls } from "./Maintenance";

const overview = { identity: { id: "owner", systemRole: "owner" }, projects: [{ id: "p", name: "Platform" }, { id: "q", name: "Commerce" }] } as unknown as Overview;
const policy = { projectId: "p", logDays: 30, runDays: 90, keepRuns: 20 };
const preview = { logs: 100, runs: 3, protectedRuns: 12, applied: false };
const backup = { id: "backup-a", engine: "sqlite", state: "ready", bytes: 1048576, createdAt: "2026-09-20T02:00:00Z", message: "Saved" };
const verified = { ...backup, id: "backup-older", state: "verified", createdAt: "2026-09-19T02:00:00Z", verifiedAt: "2026-09-19T03:00:00Z" };
beforeEach(() => vi.restoreAllMocks());
afterEach(cleanup);

function retentionRequests() {
  return vi.spyOn(api, "request").mockImplementation(async <T,>(path: string, init?: RequestInit) => {
    if (path.endsWith("/preview")) return preview as T;
    if (path.endsWith("/apply")) return { ...preview, applied: true } as T;
    if (init?.method === "PUT") return JSON.parse(String(init.body)) as T;
    return { ...policy, projectId: path.includes("/q/") ? "q" : "p" } as T;
  });
}
async function showPreview() {
  fireEvent.click(await screen.findByRole("button", { name: "Preview cleanup" }));
  await screen.findByLabelText("Cleanup preview");
}
async function reviewRemoval() {
  await showPreview();
  fireEvent.click(screen.getByRole("button", { name: "Review removal" }));
  fireEvent.click(screen.getByRole("checkbox"));
}

describe("history retention", () => {
  it("requires the global project selection and does not fetch a hidden default", () => {
    const request = retentionRequests();
    render(<RetentionControls overview={overview} />);
    expect(screen.getByRole("heading", { name: "Select a project to manage retention" })).toBeTruthy();
    expect(request).not.toHaveBeenCalled();
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  it("does not load or expose mutations to a project viewer", () => {
    const request = retentionRequests();
    render(<RetentionControls overview={{ ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { p: ["project.view"] } }} projectId="p" />);
    expect(screen.getByRole("heading", { name: "Project admin access required" })).toBeTruthy();
    expect(request).not.toHaveBeenCalled();
  });

  it("previews saved policy and separately confirms removal without saving on apply", async () => {
    const request = retentionRequests(); const onChanged = vi.fn();
    render(<RetentionControls overview={overview} projectId="p" onChanged={onChanged} />);
    expect(screen.queryByRole("spinbutton")).toBeNull();
    await showPreview();
    expect(request).toHaveBeenCalledWith("/api/v1/projects/p/retention/preview", { method: "POST", body: JSON.stringify({ expectedPolicy: policy }) });
    expect(request.mock.calls.some(([path]) => path.endsWith("/apply"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Review removal" }));
    const remove = screen.getByRole("button", { name: "Remove eligible history" });
    expect((remove as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(remove);
    await screen.findByText("Removed 100 old log lines and 3 failed or cancelled runs. 12 runs stayed protected.");
    expect(request).toHaveBeenCalledWith("/api/v1/projects/p/retention/apply", { method: "POST", body: JSON.stringify({ confirm: "p", expectedPolicy: policy }) });
    expect(request.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("invalidates a removal review when editing policy and saves without removing history", async () => {
    const request = retentionRequests(); render(<RetentionControls overview={overview} projectId="p" />);
    await reviewRemoval();
    fireEvent.click(screen.getByRole("button", { name: "Edit policy" }));
    expect(screen.queryByRole("button", { name: "Remove eligible history" })).toBeNull();
    fireEvent.change(screen.getByRole("spinbutton", { name: "Keep logs for" }), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await screen.findByText("Policy saved. Preview cleanup to see what is eligible.");
    expect(request).toHaveBeenCalledWith("/api/v1/projects/p/retention", { method: "PUT", body: JSON.stringify({ ...policy, logDays: 7 }) });
    expect(request.mock.calls.filter(([path]) => path.endsWith("/preview"))).toHaveLength(1);
    expect(request.mock.calls.some(([path]) => path.endsWith("/apply"))).toBe(false);
  });

  it("rejects invalid numeric limits before saving", async () => {
    const request = retentionRequests(); render(<RetentionControls overview={overview} projectId="p" />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit policy" }));
    fireEvent.change(screen.getByRole("spinbutton", { name: "Keep at least" }), { target: { value: "4" } });
    expect((screen.getByRole("button", { name: "Save policy" }) as HTMLButtonElement).disabled).toBe(true);
    expect(request.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it("ignores a preview response after switching projects", async () => {
    let resolve!: (value: unknown) => void;
    const request = vi.spyOn(api, "request").mockImplementation(async <T,>(path: string) => path.endsWith("/preview") ? new Promise<T>(done => { resolve = value => done(value as T); }) : { ...policy, projectId: path.includes("/q/") ? "q" : "p" } as T);
    const view = render(<RetentionControls overview={overview} projectId="p" />);
    fireEvent.click(await screen.findByRole("button", { name: "Preview cleanup" }));
    view.rerender(<RetentionControls overview={overview} projectId="q" />);
    await screen.findByText("Commerce · Cleanup runs only when you apply it.");
    resolve(preview);
    await waitFor(() => expect(screen.queryByText("Eligible for removal")).toBeNull());
    expect(request.mock.calls.some(([path]) => path.endsWith("/apply"))).toBe(false);
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("discards an external stale-policy conflict and reloads the policy before another preview", async () => {
    let reads = 0;
    const next = { ...policy, logDays: 7 };
    const request = vi.spyOn(api, "request").mockImplementation(async <T,>(path: string) => {
      if (path.endsWith("/apply")) throw Object.assign(new Error("Retention policy changed"), { status: 409 });
      if (path.endsWith("/preview")) return preview as T;
      return (++reads === 1 ? policy : next) as T;
    });
    render(<RetentionControls overview={overview} projectId="p" />);
    await reviewRemoval(); fireEvent.click(screen.getByRole("button", { name: "Remove eligible history" }));
    await screen.findByText("The policy changed. Review the current policy and preview cleanup again.");
    await waitFor(() => expect(reads).toBe(2));
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove eligible history" })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Preview cleanup" }));
    await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/projects/p/retention/preview", { method: "POST", body: JSON.stringify({ expectedPolicy: next }) }));
  });

  it("removes the review immediately when permission is revoked", async () => {
    retentionRequests(); const view = render(<RetentionControls overview={overview} projectId="p" />);
    await reviewRemoval();
    view.rerender(<RetentionControls overview={{ ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { p: ["project.view"] } }} projectId="p" />);
    expect(screen.queryByRole("button", { name: "Remove eligible history" })).toBeNull();
    expect(screen.getByRole("heading", { name: "Project admin access required" })).toBeTruthy();
  });
});

describe("controller backups", () => {
  it("shows loading instead of an empty backup claim before the first response", async () => {
    let resolve!: (value: unknown) => void;
    vi.spyOn(api, "request").mockImplementation(() => new Promise(done => { resolve = done; }));
    render(<Backups />);
    expect(screen.getByText("Loading backup records…")).toBeTruthy();
    expect(screen.queryByText("No controller backup recorded")).toBeNull();
    expect((screen.getByRole("button", { name: "Create backup" }) as HTMLButtonElement).disabled).toBe(true);
    resolve({ configured: true, backups: [] });
    await screen.findByText("No controller backup recorded");
  });

  it("distinguishes a new unverified copy from an older verified recovery point", async () => {
    vi.spyOn(api, "request").mockResolvedValue({ configured: true, backups: [verified, backup] });
    render(<Backups />);
    await screen.findByText("Latest backup has not been verified");
    expect(screen.getByText(/Newest listed verified copy:/)).toBeTruthy();
    expect(screen.queryByText("Latest backup passed its restore check")).toBeNull();
    expect(screen.getByText("Backup scope and restore checks").closest("details")?.open).toBe(false);
  });

  it("creates a copy only after the create action and leaves verification explicit", async () => {
    let created = false;
    const request = vi.spyOn(api, "request").mockImplementation(async <T,>(path: string, init?: RequestInit) => {
      if (init?.method === "POST") { created = true; return backup as T; }
      return { configured: true, backups: created ? [backup] : [] } as T;
    });
    render(<Backups />);
    await screen.findByText("No controller backup recorded");
    expect(request.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Create backup" }));
    await screen.findByText("Latest backup has not been verified");
    expect(request.mock.calls.filter(([, init]) => init?.method === "POST")).toEqual([["/api/v1/operations/backups", { method: "POST", body: "{}" }]]);
    expect(screen.getByRole("button", { name: "Verify restore" })).toBeTruthy();
  });

  it("verifies a retained copy without creating another backup or restoring over the controller", async () => {
    const checked = { ...backup, state: "verified", verifiedAt: "2026-09-20T03:00:00Z" }; let complete = false;
    const request = vi.spyOn(api, "request").mockImplementation(async <T,>(path: string, init?: RequestInit) => { if (init?.method === "POST") { complete = true; return checked as T; } return { configured: true, backups: [complete ? checked : backup] } as T; });
    const onChanged = vi.fn(); render(<Backups onChanged={onChanged} />);
    fireEvent.click(await screen.findByRole("button", { name: "Verify restore" }));
    await screen.findByText("The isolated restore check passed.");
    await screen.findByText("Latest backup passed its restore check");
    expect(request.mock.calls.filter(([, init]) => init?.method === "POST")).toEqual([["/api/v1/operations/backups/backup-a/verify", { method: "POST", body: "{}" }]]);
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("disables backup actions when the controller is not configured", async () => {
    const request = vi.spyOn(api, "request").mockResolvedValue({ configured: false, backups: [backup] });
    render(<Backups />);
    await screen.findByText(/Backup actions are unavailable/);
    expect((screen.getByRole("button", { name: "Create backup" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Verify restore" }) as HTMLButtonElement).disabled).toBe(true);
    expect(request.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
  });

  it("does not retain a passed verification claim after a failed recheck and refresh", async () => {
    let calls = 0;
    vi.spyOn(api, "request").mockImplementation(async <T,>(path: string, init?: RequestInit) => {
      if (init?.method === "POST") throw Object.assign(new Error("Restore check failed"), { status: 422 });
      if (calls++ > 0) throw new Error("Read unavailable");
      return { configured: true, backups: [verified] } as T;
    });
    render(<Backups />);
    fireEvent.click(await screen.findByRole("button", { name: "Recheck restore" }));
    await screen.findByText("Backup status could not be refreshed");
    expect(screen.queryByText("Latest backup passed its restore check")).toBeNull();
    expect(screen.getByText("Verification failed")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("Restore check failed");
    expect(screen.getByText("No listed copy has a successful restore check.")).toBeTruthy();
  });

  it("does not label a verified state without its timestamp as a verified recovery point", async () => {
    vi.spyOn(api, "request").mockResolvedValue({ configured: true, backups: [{ ...backup, state: "verified" }] });
    render(<Backups />);
    await screen.findByText("Latest backup has not been verified");
    expect(screen.getByText("No listed copy has a successful restore check.")).toBeTruthy();
  });
});
