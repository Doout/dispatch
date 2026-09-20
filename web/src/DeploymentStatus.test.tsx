// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { DeploymentDetailsPage } from "./App";
import { api, type Deployment, type Overview } from "./api";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("retains one status card and history panel across live updates and version changes", async () => {
 const deployment = {id:"run-1",appId:"app",state:"succeeded",createdAt:"2026-09-19T00:00:00Z",app:{id:"app",name:"API",projectId:"project",buildType:"helm"}} as Deployment;
 const overview = {identity:{id:"owner",systemRole:"owner"}} as Overview;
 vi.spyOn(api,"applicationSync").mockResolvedValue({appId:"app",supported:true,reapplyAvailable:true,configuration:{state:"ready",message:"Synced"},revision:{state:"current"},drift:{state:"synced",health:"healthy",checkedAt:"2026-09-19T00:00:00Z",message:"Checked",location:"Dispatch controller",resources:[]},actions:[]});
 vi.spyOn(api,"applicationHistory").mockResolvedValue({items:[]});
 const page = (run:Deployment) => <DeploymentDetailsPage overview={overview} deployment={run} section="history" logs={[]} logsLoading={false} logsError="" onBack={()=>undefined} onCancel={()=>undefined}/>;
 const view = render(page(deployment));
 await screen.findByText("Healthy");
 await userEvent.setup().click(screen.getByRole("button",{name:"Details"}));
 view.rerender(page({...deployment,message:"Updated observation"}));
 view.rerender(page({...deployment,id:"run-2"}));
 expect(screen.getAllByRole("heading",{name:"Application status"})).toHaveLength(1);
 expect(screen.getAllByRole("heading",{name:"Deployment history"})).toHaveLength(1);
 expect(screen.getByRole("button",{name:"Details"}).getAttribute("aria-expanded")).toBe("true");
 expect(screen.getAllByText("Healthy")).toHaveLength(1);
});

it.each(["dockerfile", "compose"] as const)("exposes endpoint observation settings on %s deployment details", async buildType => {
 const deployment = {id:"run-1",appId:"app",state:"succeeded",createdAt:"2026-09-19T00:00:00Z",app:{id:"app",name:"API",projectId:"project",buildType}} as Deployment;
 const overview = {identity:{id:"owner",systemRole:"owner"}} as Overview;
 vi.spyOn(api,"applicationSync").mockResolvedValue({appId:"app",supported:false,reapplyAvailable:false,configuration:{state:"not_applicable",message:"Managed in Dispatch."},revision:{state:"current"},drift:{state:"unknown",health:"unknown",message:"Runtime drift checks support Helm applications on Kubernetes and OpenShift.",location:"Dispatch controller",resources:[]},actions:[]});
 render(<DeploymentDetailsPage overview={overview} deployment={deployment} section="topology" logs={[]} logsLoading={false} logsError="" onBack={()=>undefined} onCancel={()=>undefined}/>);
 await screen.findByText("Runtime drift");
 expect(screen.getAllByText("Not supported")).toHaveLength(2);
 await userEvent.setup().click(screen.getByRole("button",{name:"Checks"}));
 expect(screen.getByRole("heading",{name:"Checks and notifications"})).toBeTruthy();
 expect(screen.getByRole("button",{name:"Check endpoint"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:"Check now"})).toBeNull();
 expect(screen.queryByRole("button",{name:"Reapply deployed configuration"})).toBeNull();
});
