// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Deployment, WorkflowRevision, WorkflowStageRun } from "../api";
import { api } from "../api";
import { ReleaseTools, PromotionWorkspace } from "./ReleaseTools";
import { releaseClient } from "./releaseClient";

vi.mock("./releaseClient", () => ({ releaseClient: { info: vi.fn(), saveNote: vi.fn(), preview: vi.fn(), rollbackPreview: vi.fn(), rollback: vi.fn(), activity: vi.fn(), diagnosis: vi.fn() } }));
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const deployment = { id:"release-a", appId:"app-a", commitSha:"abcdef1234", state:"succeeded", createdAt:"2026-09-20T00:00:00Z" } as Deployment;
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(releaseClient.info).mockResolvedValue({ note:{ deploymentId:deployment.id, notes:"Release context", links:[], actor:"operator", updatedAt:"2026-09-20T00:00:00Z" }, sourceLinks:[], rollbackMessage:"Review retained Helm version" });
});

describe("release tools", () => {
  it("loads details only when expanded and keeps viewer controls read-only", async () => {
    render(<ReleaseTools deployment={deployment} canDeploy={false} canConfigure={false} />);
    expect(releaseClient.info).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button",{name:/Release tools/}));
    expect(await screen.findByText("Release context")).toBeTruthy();
    expect(screen.queryByRole("button",{name:"Save notes"})).toBeNull();
    expect(screen.queryByRole("tab",{name:"Preview & rollback"})).toBeNull();
  });

  it("requires a reviewed current release and explicit acknowledgement before rollback", async () => {
    const onDeployment=vi.fn();
    vi.mocked(releaseClient.rollbackPreview).mockResolvedValue({available:true,message:"Database migrations are not reverted.",deploymentId:deployment.id,currentDeploymentId:"running-b",helmRevision:2,bindings:[],resources:[]});
    vi.mocked(releaseClient.rollback).mockResolvedValue({...deployment,id:"rollback-c",state:"queued"});
    render(<ReleaseTools deployment={deployment} canDeploy canConfigure onDeployment={onDeployment} />);
    fireEvent.click(screen.getByRole("button",{name:/Release tools/}));
    await screen.findByRole("textbox",{name:"Release notes"});
    fireEvent.click(screen.getByRole("tab",{name:"Preview & rollback"}));
    fireEvent.click(screen.getByRole("button",{name:"Review rollback to this version"}));
    const restore=await screen.findByRole("button",{name:"Restore this version"});
    expect((restore as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(restore);
    await waitFor(()=>expect(releaseClient.rollback).toHaveBeenCalledWith("release-a","running-b"));
    await waitFor(()=>expect(onDeployment).toHaveBeenCalledWith(expect.objectContaining({id:"rollback-c"})));
  });

  it("does not show a diagnosis result from a previously selected deployment", async () => {
    let resolve!: (result: Awaited<ReturnType<typeof releaseClient.diagnosis>>) => void;
    vi.mocked(releaseClient.diagnosis).mockReturnValue(new Promise(done=>{resolve=done;}));
    const { rerender }=render(<ReleaseTools deployment={deployment} canDeploy canConfigure />);
    fireEvent.click(screen.getByRole("button",{name:/Release tools/}));
    await screen.findByRole("textbox",{name:"Release notes"});
    fireEvent.click(screen.getByRole("tab",{name:"Diagnose"}));
    fireEvent.click(screen.getByRole("button",{name:"Inspect now"}));
    rerender(<ReleaseTools deployment={{...deployment,id:"release-b"}} canDeploy canConfigure />);
    resolve({location:"Dispatch controller",checkedAt:"2026-09-20T00:00:00Z",live:true,message:"STALE RESULT",issues:[],deploymentLogs:[]});
    await waitFor(()=>expect(screen.queryByText("Inspecting workloads…")).toBeNull());
    expect(screen.queryByText("STALE RESULT")).toBeNull();
  });
});

it("shows captured revision before approving an environment", async () => {
  const approve=vi.spyOn(api,"approveWorkflowStage").mockResolvedValue({} as WorkflowStageRun);
  const stage={id:"stage-a",revisionId:"revision-a",stageName:"production",targetRef:"production-cluster",state:"awaiting_approval",approval:"required"} as WorkflowStageRun;
  const revision={id:"revision-a",configSha:"abcdef012345",sources:{app:{alias:"app",repository:"https://example.test/app",branch:"main",commitSha:"123456abcdef"}}} as unknown as WorkflowRevision;
  render(<PromotionWorkspace stages={[stage]} revisions={[revision]} canApprove />);
  expect(screen.queryByRole("button",{name:"Approve this revision"})).toBeNull();
  fireEvent.click(screen.getByRole("button",{name:"Review revision"}));
  expect(screen.getByText("123456abcdef")).toBeTruthy();
  fireEvent.click(screen.getByRole("button",{name:"Approve this revision"}));
  await waitFor(()=>expect(approve).toHaveBeenCalledWith("stage-a"));
  approve.mockRestore();
});
