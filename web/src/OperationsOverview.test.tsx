// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { Overview } from "./api";
import { OperationsPage } from "./OperationsPage";
import { operationsClient, type OperationsSummary } from "./operations/client";

vi.mock("./operations/Activity", () => ({Activity: () => <div>Activity tool</div>, auditActionLabel: () => "Update application owner"}));
vi.mock("./operations/Ownership", () => ({Ownership: () => <div>Ownership tool</div>}));
vi.mock("./operations/IdentityMappings", () => ({IdentityMappings: () => <div>Mapping tool</div>}));
vi.mock("./operations/Maintenance", () => ({Backups: () => <div>Backup tool</div>, RetentionControls: () => <div>Cleanup tool</div>}));
const overview = {identity:{id:"owner",systemRole:"owner"}, projects:[{id:"p",name:"Platform"},{id:"q",name:"Commerce"}], projectPermissions:{}} as unknown as Overview;
const summary: OperationsSummary = {observedAt:"2026-09-20T12:00:00Z",audit:{since:"2026-09-19T12:00:00Z",total:12,rejected:3,recent:[{id:"event",actorId:"person",actorName:"Alex",projectId:"p",action:"PUT /apps/{id}/owner",outcome:"succeeded",createdAt:"2026-09-20T11:00:00Z"}]},ownership:{total:8,unassigned:2},backups:{configured:true,recorded:0}};
afterEach(()=>{cleanup();vi.restoreAllMocks();});

it("opens exact scoped follow-up filters from real summary observations",async()=>{
 vi.spyOn(operationsClient,"summary").mockResolvedValue(summary);const onFilters=vi.fn();
 render(<OperationsPage overview={overview} filters={{projectId:"p"}} onFilters={onFilters}/>);
 await screen.findByText("3 requests were rejected");
 expect(screen.getByText("6 / 8")).toBeTruthy();expect(screen.getByText("Update application owner")).toBeTruthy();
 fireEvent.click(screen.getByRole("button",{name:"Review activity"}));
 expect(onFilters).toHaveBeenLastCalledWith({section:"activity",projectId:"p",outcome:"rejected",since:summary.audit.since,until:summary.observedAt});
 fireEvent.click(screen.getByRole("button",{name:"Assign owners"}));
 expect(onFilters).toHaveBeenLastCalledWith({section:"ownership",projectId:"p",unassigned:true});
 fireEvent.click(screen.getByRole("button",{name:"Open backups"}));
 expect(onFilters).toHaveBeenLastCalledWith({section:"backups",projectId:"p"});
});

it("hides controller tools and backup metadata from project viewers",async()=>{
 vi.spyOn(operationsClient,"summary").mockResolvedValue(summary);
 render(<OperationsPage overview={{...overview,identity:{...overview.identity!,systemRole:"member"},projectPermissions:{p:["project.view"]}}} filters={{section:"backups",projectId:"p"}}/>);
 await screen.findByText("3 requests were rejected");
 expect(screen.queryByRole("tab",{name:"Backups"})).toBeNull();
 expect(screen.queryByRole("tab",{name:"Access mappings"})).toBeNull();
 expect(screen.queryByText("Latest controller backup")).toBeNull();
 expect(screen.queryByRole("button",{name:"Review history cleanup"})).toBeNull();
 expect(screen.getByRole("button",{name:"View ownership"})).toBeTruthy();
});

it("does not show success while loading, on errors, or after an obsolete project response",async()=>{
 let resolve!: (value:OperationsSummary)=>void;
 const request=vi.spyOn(operationsClient,"summary").mockImplementation(project=>project==="p"?new Promise(done=>{resolve=done;}):Promise.reject(new Error("Unavailable")));
 const onFilters=vi.fn(); const view=render(<OperationsPage overview={overview} filters={{projectId:"p"}} onFilters={onFilters}/>);
 expect(screen.getByText("Loading operations")).toBeTruthy();
 expect(screen.queryByText("No follow-up items in these records")).toBeNull();
 view.rerender(<OperationsPage overview={overview} filters={{projectId:"q"}} onFilters={onFilters}/>);
 await screen.findByRole("alert");
 await act(async()=>{resolve(summary);});
 expect(screen.queryByText("3 requests were rejected")).toBeNull();
 expect(screen.queryByText("No follow-up items in these records")).toBeNull();
 request.mockResolvedValue({...summary,audit:{...summary.audit,total:0,rejected:0,recent:[]},ownership:{total:0,unassigned:0},backups:undefined});
 fireEvent.click(screen.getByRole("button",{name:"Try again"}));
 await screen.findByText("No follow-up items in these records");
 expect(request).toHaveBeenLastCalledWith("q");
});

it("navigates tabs with the keyboard and preserves project selection without reloading",async()=>{
 vi.spyOn(operationsClient,"summary").mockResolvedValue(summary);
 const onFilters=vi.fn();const view=render(<OperationsPage overview={overview} filters={{projectId:"p"}} onFilters={onFilters}/>);
 await screen.findByText("3 requests were rejected");
 const tab=screen.getByRole("tab",{name:"Overview"});tab.focus();fireEvent.keyDown(tab,{key:"ArrowRight"});
 expect(onFilters).toHaveBeenCalledWith({section:"activity",projectId:"p"});
 expect(document.activeElement).toBe(screen.getByRole("tab",{name:"Activity"}));
 view.rerender(<OperationsPage overview={overview} filters={{section:"activity",projectId:"p"}} onFilters={onFilters}/>);
 expect(screen.getByRole("tabpanel").textContent).toContain("Activity tool");
 expect(screen.getByRole("tab",{name:"Activity"}).getAttribute("aria-selected")).toBe("true");
 fireEvent.change(screen.getByRole("combobox",{name:"Operations project"}),{target:{value:"q"}});
 await waitFor(()=>expect(onFilters).toHaveBeenLastCalledWith({section:"activity",projectId:"q",appId:undefined}));
});


it("flags incomplete backup records and never infers verification from a stale timestamp",async()=>{
 const latest={id:"b",engine:"sqlite",state:"creating",bytes:0,createdAt:"2026-09-20T11:00:00Z",message:""};
 const request=vi.spyOn(operationsClient,"summary").mockResolvedValue({...summary,audit:{...summary.audit,total:0,rejected:0,recent:[]},ownership:{total:0,unassigned:0},backups:{configured:true,recorded:1,latest}});
 render(<OperationsPage overview={overview}/>);
 await screen.findByText("The latest backup has not completed");
 expect(screen.queryByText("No follow-up items in these records")).toBeNull();
 request.mockResolvedValue({...summary,backups:{configured:true,recorded:1,latest:{...latest,state:"verification_failed",verifiedAt:"2026-09-20T10:00:00Z"}}});
 fireEvent.click(screen.getByRole("button",{name:"Refresh"}));
 await screen.findByText("The latest backup needs review");
 expect(screen.queryByText("Verified",{exact:true})).toBeNull();
});
