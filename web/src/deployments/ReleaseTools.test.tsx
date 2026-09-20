// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Deployment, WorkflowRevision, WorkflowStageRun } from "../api";
import { api } from "../api";
import { ReleaseTools, PromotionWorkspace } from "./ReleaseTools";
import { releaseClient, type ReleasePreview } from "./releaseClient";

vi.mock("./releaseClient", () => ({ releaseClient: { info: vi.fn(), saveNote: vi.fn(), preview: vi.fn(), rollbackPreview: vi.fn(), rollback: vi.fn(), activity: vi.fn(), diagnosis: vi.fn() } }));
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const deployment = { id: "release-a", appId: "app-a", commitSha: "abcdef1234", state: "succeeded", createdAt: "2026-09-20T00:00:00Z", app: { name: "Orders", generated: false } } as Deployment;
const note = { deploymentId: deployment.id, notes: "Release context", links: [], actor: "operator", updatedAt: "2026-09-20T00:00:00Z" };
const preview: ReleasePreview = {
  review: { expectedAppName: "Orders", projectId: "project-a", appSpecDigest: "spec-a", bindingsDigest: "bindings-a", serviceRevisions: { db: 4 } },
  ready: true, revision: "pinned123456", specDigest: "spec-a", target: "Production", namespace: "orders", release: "orders", message: "No runtime changes made.",
  checks: [{ name: "Credentials", state: "passed", message: "Resolved." }, { name: "Hooks", state: "unavailable", message: "Hooks run during deployment." }],
  resources: [], bindings: [], comparison: { available: false, changes: [] } as unknown as ReleasePreview["comparison"],
};
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(releaseClient.info).mockResolvedValue({ note, sourceLinks: [], rollbackMessage: "Review retained Helm version" });
  vi.mocked(releaseClient.activity).mockResolvedValue({ items: [], message: "" });
});

describe("release workbench", () => {
  it("shows the selected release and readable notes without a disclosure or viewer edit controls", async () => {
    render(<ReleaseTools deployment={deployment} canDeploy={false} canConfigure={false} />);
    expect(await screen.findByText("Release context")).toBeTruthy();
    expect(screen.getByTitle("abcdef1234").textContent).toBe("abcdef12");
    expect(screen.getByText("Succeeded")).toBeTruthy();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit notes" })).toBeNull();
    expect(screen.queryByRole("tab", { name: "Deploy" })).toBeNull();
    expect(screen.queryByRole("tab", { name: "Restore" })).toBeNull();
    expect(screen.getByRole("tabpanel").getAttribute("aria-labelledby")).toBe(screen.getByRole("tab", { name: "Notes" }).id);
  });

  it.each(["0001-01-01T00:00:00Z", "not-a-date"])("hides an unrecorded note timestamp %s", async updatedAt => {
    vi.mocked(releaseClient.info).mockResolvedValue({ note: { ...note, notes: "", actor: "", updatedAt }, sourceLinks: [], rollbackMessage: "" });
    render(<ReleaseTools deployment={deployment} canDeploy={false} canConfigure={false} />);
    await screen.findByText("No notes for this release yet.");
    expect(screen.queryByText(/^Updated /)).toBeNull();
    expect(screen.queryByText(/NaN|Invalid Date/)).toBeNull();
  });

  it("uses deployment identifiers when header and activity revisions are empty", async () => {
    vi.mocked(releaseClient.activity).mockResolvedValue({ items: [
      { id: "event-empty", kind: "deployment", message: "Deployment succeeded", deploymentId: "deployment-ABC123", revision: "", createdAt: "2026-09-20T00:00:00Z" },
      { id: "event-whitespace", kind: "deployment", message: "Deployment cancelled", deploymentId: "deployment-DEF456", revision: "   ", createdAt: "2026-09-20T00:00:00Z" },
    ], message: "" });
    const selected = { ...deployment, id: "deployment-ABCDEFGH", commitSha: "   " };
    const onSelectDeployment = vi.fn();
    render(<ReleaseTools deployment={selected} canDeploy={false} canConfigure={false} onSelectDeployment={onSelectDeployment} />);
    await screen.findByText("Release context");
    expect(screen.getByTitle(selected.id).textContent).toBe("ABCDEFGH");
    expect(screen.getByText(/^Updated .* by operator$/)).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: "Activity" }));
    const link = await screen.findByRole("link", { name: "Open deployment ABC123" });
    expect(link.textContent).toBe("ABC123");
    expect(screen.getByRole("link", { name: "Open deployment DEF456" }).textContent).toBe("DEF456");
    fireEvent.click(link);
    await waitFor(() => expect(onSelectDeployment).toHaveBeenCalledWith("deployment-ABC123"));
  });

  it("edits notes on demand and cancels a draft without saving it", async () => {
    render(<ReleaseTools deployment={deployment} canDeploy canConfigure />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit notes" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Release notes" }), { target: { value: "Discard this draft" } });
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText("Release context")).toBeTruthy();
    expect(releaseClient.saveNote).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));
    expect((screen.getByRole("textbox", { name: "Release notes" }) as HTMLTextAreaElement).value).toBe("Release context");
    expect((screen.getByRole("button", { name: "Save notes" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByRole("textbox", { name: "Release notes" }), { target: { value: "New release notes" } });
    vi.mocked(releaseClient.saveNote).mockResolvedValue({ ...note, notes: "New release notes" });
    fireEvent.click(screen.getByRole("button", { name: "Save notes" }));
    await screen.findByText("Release notes saved.");
    expect(screen.getByText("New release notes")).toBeTruthy();
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("supports keyboard tab navigation and loads activity only when selected", async () => {
    render(<ReleaseTools deployment={deployment} canDeploy canConfigure />);
    await screen.findByText("Release context");
    expect(releaseClient.activity).not.toHaveBeenCalled();
    const notes = screen.getByRole("tab", { name: "Notes" });
    notes.focus(); fireEvent.keyDown(notes, { key: "ArrowRight" });
    const activity = screen.getByRole("tab", { name: "Activity" });
    expect(document.activeElement).toBe(activity);
    expect(activity.getAttribute("aria-selected")).toBe("true");
    await screen.findByText("No retained activity for this application.");
    fireEvent.keyDown(activity, { key: "End" });
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: "Restore" }));
  });

  it("requires reviewed current release identity and confirmation before restore", async () => {
    const onDeployment = vi.fn();
    vi.mocked(releaseClient.rollbackPreview).mockResolvedValue({ available: true, message: "Database migrations are not reverted.", deploymentId: deployment.id, currentDeploymentId: "running-b", helmRevision: 2, bindings: [], resources: [] });
    vi.mocked(releaseClient.rollback).mockResolvedValue({ ...deployment, id: "rollback-c", state: "queued" });
    render(<ReleaseTools deployment={deployment} canDeploy canConfigure onDeployment={onDeployment} />);
    fireEvent.click(screen.getByRole("tab", { name: "Restore" }));
    expect(releaseClient.rollbackPreview).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Review restore" }));
    const restore = await screen.findByRole("button", { name: "Restore this version" });
    expect((restore as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(restore);
    await waitFor(() => expect(releaseClient.rollback).toHaveBeenCalledWith("release-a", "running-b"));
    await waitFor(() => expect(onDeployment).toHaveBeenCalledWith(expect.objectContaining({ id: "rollback-c" })));
  });

  it("requires confirmation to deploy the exact preview and clears a stale review", async () => {
    vi.mocked(releaseClient.preview).mockResolvedValue(preview);
    const deploy = vi.spyOn(api, "deploy").mockRejectedValue(Object.assign(new Error("Application changed. Check inputs again."), { status: 409 }));
    render(<ReleaseTools deployment={deployment} canDeploy canConfigure />);
    fireEvent.click(screen.getByRole("tab", { name: "Deploy" }));
    fireEvent.click(screen.getByRole("button", { name: "Check inputs" }));
    const start = await screen.findByRole("button", { name: "Deploy reviewed revision" });
    expect((start as HTMLButtonElement).disabled).toBe(true);
    const validation = screen.getByText("Validation checks").closest("details");
    expect(validation?.open).toBe(false);
    fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(start);
    await waitFor(() => expect(deploy).toHaveBeenCalledWith("app-a", "pinned123456", preview.review));
    await screen.findByRole("alert");
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Deploy reviewed revision" })).toBeNull();
    expect(screen.getByRole("button", { name: "Check inputs" })).toBeTruthy();
  });

  it("clears restore confirmation when leaving the panel", async () => {
    vi.mocked(releaseClient.rollbackPreview).mockResolvedValue({ available: true, message: "Ready", deploymentId: deployment.id, currentDeploymentId: "running-b", helmRevision: 2, bindings: [], resources: [] });
    render(<ReleaseTools deployment={deployment} canDeploy canConfigure />);
    fireEvent.click(screen.getByRole("tab", { name: "Restore" }));
    fireEvent.click(screen.getByRole("button", { name: "Review restore" }));
    fireEvent.click(await screen.findByRole("checkbox"));
    fireEvent.click(screen.getByRole("tab", { name: "Notes" }));
    fireEvent.click(screen.getByRole("tab", { name: "Restore" }));
    expect((screen.getByRole("button", { name: "Restore this version" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("ignores diagnosis from a previously selected release and resets its panel", async () => {
    let resolve!: (result: Awaited<ReturnType<typeof releaseClient.diagnosis>>) => void;
    vi.mocked(releaseClient.diagnosis).mockReturnValue(new Promise(done => { resolve = done; }));
    const { rerender } = render(<ReleaseTools deployment={deployment} canDeploy canConfigure />);
    fireEvent.click(screen.getByRole("tab", { name: "Diagnose" }));
    fireEvent.click(screen.getByRole("button", { name: "Inspect now" }));
    rerender(<ReleaseTools deployment={{ ...deployment, id: "release-b", commitSha: "bbbb123456" }} canDeploy canConfigure />);
    resolve({ location: "Dispatch controller", checkedAt: "2026-09-20T00:00:00Z", live: true, message: "STALE RESULT", issues: [], deploymentLogs: [] });
    await screen.findByText("Release context");
    expect(screen.queryByText("STALE RESULT")).toBeNull();
    expect(screen.getByRole("tab", { name: "Notes" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByTitle("bbbb123456")).toBeTruthy();
  });

  it("keeps diagnosis evidence collapsed and presents the next action first", async () => {
    vi.mocked(releaseClient.diagnosis).mockResolvedValue({ location: "Dispatch controller", checkedAt: "2026-09-20T00:00:00Z", live: true, message: "Current workloads", issues: [{ resource: "orders-123", container: "app", reason: "CrashLoopBackOff", restarts: 3, nextStep: "Check startup configuration.", events: [{ type: "Warning", reason: "BackOff", message: "Restarting container", count: 3, lastSeen: "" }], logs: [{ container: "app", content: "Sanitized diagnostic output" }] }], deploymentLogs: [] });
    render(<ReleaseTools deployment={deployment} canDeploy={false} canConfigure={false} />);
    fireEvent.click(screen.getByRole("tab", { name: "Diagnose" }));
    fireEvent.click(screen.getByRole("button", { name: "Inspect now" }));
    await screen.findByText("Check startup configuration.");
    expect(screen.getByText("Related events").closest("details")?.open).toBe(false);
    expect(screen.getByText("Container logs").closest("details")?.open).toBe(false);
    expect(screen.getByText("Deployment log").closest("details")?.open).toBe(false);
  });

  it("opens activity deployments through SPA navigation", async () => {
    vi.mocked(releaseClient.activity).mockResolvedValue({ items: [{ id: "event", kind: "deployment", message: "Deployment succeeded", deploymentId: "other-release", revision: "123456abcdef", createdAt: "2026-09-20T00:00:00Z" }], message: "" });
    const onSelectDeployment = vi.fn();
    render(<ReleaseTools deployment={deployment} canDeploy={false} canConfigure={false} onSelectDeployment={onSelectDeployment} />);
    fireEvent.click(screen.getByRole("tab", { name: "Activity" }));
    const link = await screen.findByRole("link", { name: "Open deployment 123456ab" });
    const event = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
    link.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    await waitFor(() => expect(onSelectDeployment).toHaveBeenCalledWith("other-release"));
  });
});

it("shows captured revision before approving an environment", async () => {
  const approve = vi.spyOn(api, "approveWorkflowStage").mockResolvedValue({} as WorkflowStageRun);
  const stage = { id: "stage-a", revisionId: "revision-a", stageName: "production", targetRef: "production-cluster", state: "awaiting_approval", approval: "required" } as WorkflowStageRun;
  const revision = { id: "revision-a", configSha: "abcdef012345", sources: { app: { alias: "app", repository: "https://example.test/app", branch: "main", commitSha: "123456abcdef" } } } as unknown as WorkflowRevision;
  render(<PromotionWorkspace stages={[stage]} revisions={[revision]} canApprove />);
  expect(screen.queryByRole("button", { name: "Approve this revision" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Review revision" }));
  expect(screen.getByText("123456abcdef")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Approve this revision" }));
  await waitFor(() => expect(approve).toHaveBeenCalledWith("stage-a"));
});
