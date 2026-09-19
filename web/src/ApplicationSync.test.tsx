// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { ApplicationSync } from "./ApplicationSync";
import { api, type App, type Overview, type ApplicationSyncStatus } from "./api";
const app = {id:"app",name:"API",projectId:"p",buildType:"helm"} as App;
const overview = {identity:{id:"owner",systemRole:"owner"}} as Overview;
const status:ApplicationSyncStatus={appId:"app",deploymentId:"success-1",supported:true,reapplyAvailable:true,configuration:{state:"ready",message:"Last observed configuration."},revision:{state:"current",applied:"abc"},drift:{state:"out_of_sync",health:"healthy",message:"Observed differences.",location:"Dispatch controller",checkedAt:"2026-09-19T00:00:00Z",lastSuccessfulCheckAt:"2026-09-19T00:00:00Z",resources:[{apiVersion:"apps/v1",kind:"Deployment",name:"api",state:"out_of_sync",health:"healthy",differences:[{path:"/spec/replicas",expected:2,actual:3}]}]},actions:[]};
afterEach(()=>{cleanup();vi.restoreAllMocks();});
it("separates runtime drift from health and asks before reapply",async()=>{
 vi.spyOn(api,"applicationSync").mockResolvedValue(status);const apply=vi.spyOn(api,"reapplyApplication").mockResolvedValue({...status,drift:{...status.drift,state:"synced"}});
 render(<ApplicationSync application={app} overview={overview}/>);expect(await screen.findByText("Runtime drift")).toBeTruthy();expect(screen.getByText("Resource health")).toBeTruthy();expect(screen.getAllByText("Out of sync").length).toBeGreaterThan(0);expect(screen.getByText("Healthy")).toBeTruthy();expect(screen.getByText("/spec/replicas")).toBeTruthy();
 const user=userEvent.setup();await user.click(screen.getByRole("button",{name:"Reapply deployed configuration"}));expect(apply).not.toHaveBeenCalled();await user.click(screen.getByRole("button",{name:"Confirm reapply"}));await waitFor(()=>expect(apply).toHaveBeenCalledWith("app","success-1"));
});
it("lets viewers inspect saved results without mutation controls",async()=>{
 vi.spyOn(api,"applicationSync").mockResolvedValue(status);render(<ApplicationSync application={app} overview={{...overview,identity:{...overview.identity!,systemRole:"member"},projectPermissions:{p:["project.view"]}}}/>);
 await screen.findByText("Runtime drift");expect(screen.queryByRole("button",{name:"Check now"})).toBeNull();expect(screen.queryByRole("button",{name:"Reapply deployed configuration"})).toBeNull();
});
it("shows Unknown after a failed check while keeping the prior successful time",async()=>{
 vi.spyOn(api,"applicationSync").mockResolvedValue(status);vi.spyOn(api,"checkApplicationDrift").mockResolvedValue({...status,drift:{...status.drift,state:"unknown",health:"unknown",message:"Target unavailable.",resources:[]}});
 render(<ApplicationSync application={app} overview={overview}/>);const user=userEvent.setup();await user.click(await screen.findByRole("button",{name:"Check now"}));await screen.findByText("Target unavailable.");expect(screen.getAllByText("Unknown")).toHaveLength(2);expect(screen.getByText(/Last successful check:/)).toBeTruthy();expect((screen.getByRole("button",{name:"Reapply deployed configuration"}) as HTMLButtonElement).disabled).toBe(true);
});

it("keeps compact status icons visible and moves details behind a disclosure", async () => {
 vi.spyOn(api,"applicationSync").mockResolvedValue(status);
 render(<ApplicationSync application={app} overview={overview} compact />);
 await screen.findByText("Runtime drift");
 expect(screen.getAllByText("Out of sync")).toHaveLength(1);
 expect(screen.queryByText("/spec/replicas")).toBeNull();
 expect(document.querySelectorAll(".sync-indicators .sync-state svg")).toHaveLength(4);
 await userEvent.setup().click(screen.getByRole("button", {name:"Details"}));
 expect(screen.getByText("/spec/replicas")).toBeTruthy();
});

it("distinguishes untested resources and preserves health without a drift baseline", async () => {
 vi.spyOn(api,"applicationSync").mockResolvedValue({...status,reapplyAvailable:false,drift:{...status.drift,state:"unknown",health:"unknown",checkedAt:undefined,lastSuccessfulCheckAt:undefined,resources:[]}});
 vi.spyOn(api,"checkApplicationDrift").mockResolvedValue({...status,reapplyAvailable:false,drift:{...status.drift,state:"unknown",health:"healthy",message:"No matching stored revision.",healthMessage:"Read readiness for 2 resources.",resources:[]}});
 render(<ApplicationSync application={app} overview={overview} compact />);
 await screen.findByText("Runtime drift");
 expect(screen.getAllByText("Not checked")).toHaveLength(3);
 expect(screen.queryByText("Unknown")).toBeNull();
 await userEvent.setup().click(screen.getByRole("button",{name:"Check now"}));
 expect(await screen.findByText("Healthy")).toBeTruthy();
 expect(screen.getAllByText("Unknown")).toHaveLength(1);
 expect(screen.getByText("No matching stored revision.")).toBeTruthy();
 expect(screen.queryByText("Not checked")).toBeNull();
});
it("explains unreadable resources even with compact details collapsed", async () => {
 vi.spyOn(api,"applicationSync").mockResolvedValue({...status,drift:{...status.drift,state:"unknown",health:"unknown",message:"Target unavailable.",healthMessage:"Readiness could not be read from the deployment target.",resources:[]}});
 render(<ApplicationSync application={app} overview={overview} compact />);
 expect(await screen.findByText("Readiness could not be read from the deployment target.")).toBeTruthy();
 expect(screen.getAllByText("Unknown")).toHaveLength(2);
});
