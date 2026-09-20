// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { type Deployment, type Overview } from "../api";
import { type DeploymentFilters, readRoute, routePath } from "../routes";
import { useState } from "react";
import { catalogClient, type CatalogItem } from "./catalogClient";
import { DeploymentCatalogProvider, DeploymentIdentity } from "./DeploymentCatalog";
import { DeploymentWorkspace, EnvironmentComparison } from "./DeploymentWorkspace";
import { DeploymentFocusRails } from "./DeploymentFocusRail";
import { NavigationShortcuts } from "./NavigationShortcuts";

const run=(id:string,appId="app",state:Deployment["state"]="succeeded"):Deployment=>({id,appId,state,commitSha:id,specDigest:"digest",createdAt:"2026-09-19T00:00:00Z",message:""});
const current=run("running"),failed=run("failed","app","failed");
const items:CatalogItem[]=[{appId:"app",appName:"Checkout dev",projectId:"p",targetId:"cluster",targetName:"Cluster",environment:"development",resourceId:"checkout",resourceName:"Checkout",current,latest:failed,sync:{configuration:"synced",revision:"current",drift:"synced",health:"healthy",supported:true,checkedAt:"2026-09-19T00:00:00Z",message:"Saved observation"}},{appId:"prod",appName:"Checkout prod",projectId:"p",targetId:"cluster",targetName:"Cluster",environment:"production",resourceId:"checkout",resourceName:"Checkout",current:run("production","prod"),latest:run("production","prod")},{appId:"private",appName:"Other project",projectId:"q",targetId:"cluster",targetName:"Cluster",environment:"development",current:run("other","private")}];
const overview={identity:{id:"owner",systemRole:"owner"},projects:[{id:"p",name:"Platform"},{id:"q",name:"Other"}],servers:[{id:"cluster",name:"Cluster",runtime:"kubernetes"}],apps:[],deployments:[current,failed],workflowResources:[],workflowRevisions:[],workflowStageRuns:[]} as unknown as Overview;
beforeEach(()=>{localStorage.clear();vi.spyOn(catalogClient,"catalog").mockResolvedValue({items});});
afterEach(()=>{cleanup();vi.restoreAllMocks();});
it("round trips URL filters independently of selected stage and ignores invalid layouts",()=>{
 const route={view:"deployments" as const,deploymentApplicationID:"checkout",deploymentStage:"production",deploymentFilters:{query:"abc / sha",project:"p",app:"prod",environment:"production",target:"cluster",status:"failed",revision:"a1",completedFrom:"2026-09-01T00:00:00Z",completedTo:"2026-09-08T00:00:00Z",layout:"list" as const,pinned:true}};
 const url=new URL(routePath(route),"https://dispatch.test");expect(readRoute(url)).toEqual({...route,deploymentID:undefined});expect(readRoute({pathname:"/deployments",search:"?layout=malicious"}).deploymentFilters?.layout).toBeUndefined();
});
it("shows the historical saved target and preserves the running release when a later attempt fails",async()=>{
 vi.spyOn(catalogClient,"identity").mockResolvedValue({...items[0],targetName:"Original target"});const open=vi.fn();
 render(<DeploymentCatalogProvider overview={overview}><DeploymentIdentity deployment={failed} overview={overview} onNavigate={open}/></DeploymentCatalogProvider>);
 expect(await screen.findByText("Original target")).toBeTruthy();expect(screen.getByText("Latest attempt")).toBeTruthy();const running=screen.getByRole("link",{name:/Running release running/});await userEvent.setup().click(running);expect(open).toHaveBeenCalledWith("running");
});
it("filters environments and restores named filters scoped to the current browser identity",async()=>{
 const user=userEvent.setup();function Page(){const [filters,setFilters]=useState<DeploymentFilters>({});return <DeploymentWorkspace overview={overview} filters={filters} onFilters={setFilters} onOpen={()=>{}}>{visible=><div data-testid="visible">{visible?.map(i=>i.appId).join(",")}</div>}</DeploymentWorkspace>;}
 render(<DeploymentCatalogProvider overview={overview}><Page/></DeploymentCatalogProvider>);
 await screen.findByRole("option",{name:"Checkout prod"});await user.selectOptions(screen.getByLabelText("Filter environment"),"production");expect(screen.getByTestId("visible").textContent).toBe("prod");
 await user.click(screen.getByText("Save current filter"));await user.type(screen.getByLabelText("Saved filter name"),"Production");await user.click(screen.getByRole("button",{name:"Save"}));await user.click(screen.getByRole("button",{name:"Clear filters"}));expect(screen.getByTestId("visible").textContent).toContain("app");await user.selectOptions(screen.getByLabelText("Saved deployment filters"),"Production");expect(screen.getByTestId("visible").textContent).toBe("prod");
 expect(localStorage.getItem("dispatch.navigation.v1:owner")).toContain("Production");expect(localStorage.getItem("dispatch.navigation.v1:another-user")).toBeNull();
});
it("opens list logs, topology and history in place and loads older search results",async()=>{
 vi.spyOn(catalogClient,"search").mockResolvedValueOnce({items:[failed],next:"failed"}).mockResolvedValueOnce({items:[current]});const open=vi.fn();const user=userEvent.setup();
 render(<DeploymentCatalogProvider overview={overview}><DeploymentWorkspace overview={overview} filters={{layout:"list"}} onFilters={()=>{}} onOpen={open}>{()=>null}</DeploymentWorkspace></DeploymentCatalogProvider>);
 await screen.findByRole("link",{name:"Topology"});await user.click(screen.getByRole("link",{name:"Topology"}));expect(open).toHaveBeenCalledWith("failed","topology");await user.click(screen.getByRole("button",{name:"Load older deployments"}));await screen.findByText("Running release");expect(catalogClient.search).toHaveBeenLastCalledWith(expect.objectContaining({size:1}));expect((vi.mocked(catalogClient.search).mock.calls[1][0]).get("before")).toBe("failed");
});
it("compares saved environment values only within the selected project",async()=>{
 vi.spyOn(catalogClient,"compare").mockResolvedValue({fromId:"running",toId:"production",available:true,changes:[{path:"/services/db/revision",kind:"changed",before:1,after:2}],hidden:1,truncated:false,message:"Sensitive values excluded"});const user=userEvent.setup();render(<EnvironmentComparison items={items} onOpen={()=>{}}/>);
 await user.selectOptions(screen.getByLabelText("From environment"),"running");expect((screen.getByLabelText("To environment") as HTMLSelectElement).querySelector('option[value="other"]')).toBeNull();await user.selectOptions(screen.getByLabelText("To environment"),"production");expect(await screen.findByText("/services/db/revision")).toBeTruthy();expect(catalogClient.compare).toHaveBeenCalledWith("production","running");
});
it("opens command navigation with keyboard and pins an environment without leaving the page",async()=>{
 const user=userEvent.setup();const navigate=vi.fn();render(<DeploymentCatalogProvider overview={overview}><NavigationShortcuts overview={overview} route={{view:"deployments"}} onNavigate={navigate}/></DeploymentCatalogProvider>);
 fireEvent.keyDown(window,{key:"k",ctrlKey:true});const dialog=await screen.findByRole("dialog",{name:"Jump to an environment"});expect(dialog).toBeTruthy();await user.click(await screen.findByRole("button",{name:"Pin Checkout dev development"}));expect(localStorage.getItem("dispatch.navigation.v1:owner")).toContain('"pins":["app"]');await user.click(screen.getByRole("link",{name:"Open Checkout development on Cluster"}));expect(navigate).toHaveBeenCalledWith({view:"deployments",deploymentID:"running"});await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());
});

it("opens quiet generated environments whose deployment and revision are outside the overview window",async()=>{
 const quiet={...overview,deployments:[],workflowRevisions:[],workflowStageRuns:[],workflowResources:[{id:"checkout",name:"Checkout",kind:"Application",stageNames:["development"],targetRefs:["cluster"]}]} as unknown as Overview;
 const open=vi.fn();render(<DeploymentCatalogProvider overview={quiet}><DeploymentFocusRails overview={quiet} onSelectDeployment={open}/></DeploymentCatalogProvider>);
 const link=await screen.findByRole("link",{name:"Open Checkout Development latest deployment"});expect(link.getAttribute("href")).toBe("/deployments/failed");await userEvent.setup().click(link);expect(open).toHaveBeenCalledWith("failed");
});

it("keeps analytics completion bounds on pagination and clears them when switching to current releases",async()=>{
 const from="2026-09-01T00:00:00Z",to="2026-09-08T00:00:00Z";
 vi.spyOn(catalogClient,"search").mockResolvedValueOnce({items:[failed],next:"failed"}).mockResolvedValueOnce({items:[current]});
 const onFilters=vi.fn(),user=userEvent.setup();
 render(<DeploymentCatalogProvider overview={overview}><DeploymentWorkspace overview={overview} filters={{layout:"list",project:"p",completedFrom:from,completedTo:to}} onFilters={onFilters} onOpen={()=>{}}>{()=>null}</DeploymentWorkspace></DeploymentCatalogProvider>);
 await screen.findByRole("link",{name:"Topology"});
 const query=vi.mocked(catalogClient.search).mock.calls[0][0];expect(query.get("completedFrom")).toBe(from);expect(query.get("completedTo")).toBe(to);
 await user.click(screen.getByRole("button",{name:"Load older deployments"}));await screen.findByText("Running release");
 expect(vi.mocked(catalogClient.search).mock.calls[1][0].get("completedFrom")).toBe(from);
 await user.click(screen.getByRole("button",{name:"Board"}));expect(onFilters).toHaveBeenLastCalledWith({layout:"board",project:"p",completedFrom:undefined,completedTo:undefined});
});
