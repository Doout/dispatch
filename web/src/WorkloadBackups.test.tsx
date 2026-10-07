// @vitest-environment jsdom
import {act,cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {afterEach,expect,it,vi} from "vitest";
import {api,Overview,WorkloadBackup,WorkloadBackupOperation} from "./api";
import {WorkloadBackups} from "./WorkloadBackups";
import {DestructiveConfirmations} from "./DestructiveConfirmations";
const overview={controllerSettings:{operationsEnabled:false,uiFeatures:{workloadBackups:true}},identity:{id:"owner",systemRole:"owner",permissions:[]},projects:[{id:"p",name:"Orders"}],services:[{id:"database",name:"orders-db",projectId:"p",type:"postgresql",provisionRunId:"run",provisionTarget:{provider:"docker",serverId:"target"}}]} as unknown as Overview;
const backup:WorkloadBackup={id:"backup",projectId:"p",sourceRunId:"run",storageId:"volume",serverId:"target",state:"ready",revision:2,artifactId:"backup",consistency:"database-native",format:"postgresql-custom",checksum:"checksum",bytes:200,encryption:"AES-256-GCM-chunks-v1",location:"target-local",policy:"retain",checkCount:0,verificationState:"not_verified",cleanupState:"complete",verificationIntervalHours:24,createdAt:"2026-10-02T01:00:00Z",updatedAt:"2026-10-02T01:00:00Z"};
const op:WorkloadBackupOperation={id:"op",backupId:"backup",projectId:"p",action:"backup",state:"succeeded",revision:2,recoveryAfter:"0001-01-01T00:00:00Z"};
afterEach(()=>{cleanup();vi.restoreAllMocks();vi.useRealTimers();vi.unstubAllGlobals()});

it("does not read backups without an enabled controller feature",()=>{
 const read=vi.spyOn(api,"workloadBackups");
 render(<WorkloadBackups overview={{...overview,controllerSettings:{operationsEnabled:false}}} project="p"/>);
 expect(screen.getByRole("heading",{name:"Workload backups is disabled"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:"Create backup"})).toBeNull();
 expect(read).not.toHaveBeenCalled();
});

it("stops polling and discards pending results when disabled",async()=>{
 vi.useFakeTimers();
 let resolve!: (items:WorkloadBackup[])=>void;
 const read=vi.spyOn(api,"workloadBackups").mockImplementation(()=>new Promise(done=>{resolve=done}));
 const view=render(<WorkloadBackups overview={overview} project="p"/>);
 expect(read).toHaveBeenCalledTimes(1);
 view.rerender(<WorkloadBackups overview={{...overview,controllerSettings:{operationsEnabled:false,uiFeatures:{workloadBackups:false}}}} project="p"/>);
 await act(async()=>{resolve([backup]);await vi.advanceTimersByTimeAsync(15000)});
 expect(screen.getByRole("heading",{name:"Workload backups is disabled"})).toBeTruthy();
 expect(screen.queryByText(/PostgreSQL backup ·/)).toBeNull();
 expect(read).toHaveBeenCalledTimes(1);
});
it("creates a native backup and requires a destination before requesting restore review",async()=>{
 vi.spyOn(api,"workloadBackups").mockResolvedValue([backup]);vi.spyOn(api,"workloadBackupOperations").mockResolvedValue([op]);const create=vi.spyOn(api,"createWorkloadBackup").mockResolvedValue(op);const restore=vi.spyOn(api,"restoreWorkloadBackup").mockResolvedValue(op);
 render(<WorkloadBackups overview={overview} project="p"/>);const user=userEvent.setup();await user.selectOptions(screen.getByLabelText("Source service"),"run");await user.click(screen.getByRole("button",{name:"Create backup"}));await waitFor(()=>expect(create).toHaveBeenCalledWith({sourceRunId:"run",verificationIntervalHours:24,checks:[]}));
 await user.click(await screen.findByText(/PostgreSQL backup ·/));const review=await screen.findByRole("button",{name:"Review data restore"});expect(review.hasAttribute("disabled")).toBe(true);await user.selectOptions(screen.getByLabelText("Restore destination"),"run");await user.click(review);expect(restore).toHaveBeenCalledWith("backup","run",expect.any(AbortSignal));expect(screen.getByText(/Restoring overwrites objects present in the archive/)).toBeTruthy();
});
it("hides mutation controls from project viewers",async()=>{
 vi.spyOn(api,"workloadBackups").mockResolvedValue([backup]);vi.spyOn(api,"workloadBackupOperations").mockResolvedValue([op]);const viewer={...overview,identity:{...overview.identity!,systemRole:"member"},projectPermissions:{p:["project.view"]}} as Overview;
 render(<WorkloadBackups overview={viewer} project="p"/>);await userEvent.click(await screen.findByText(/PostgreSQL backup ·/));expect(screen.queryByRole("button")).toBeNull();
});

it("cancels an open backup deletion confirmation when its UI feature is disabled",async()=>{
 vi.spyOn(api,"workloadBackups").mockResolvedValue([backup]);vi.spyOn(api,"workloadBackupOperations").mockResolvedValue([op]);
 const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify({resourceId:"backup",resourceType:"workload_backup",name:"Orders backup",action:"delete",version:"2",summary:"Delete archive",resources:[]}),{status:200}));vi.stubGlobal("fetch",fetch);
 const content=(value:Overview)=><><WorkloadBackups overview={value} project="p"/><DestructiveConfirmations/></>;
 const view=render(content(overview));const user=userEvent.setup();
 await user.click(await screen.findByText(/PostgreSQL backup ·/));await user.click(screen.getByRole("button",{name:"Delete backup archive"}));
 await screen.findByRole("dialog");
 view.rerender(content({...overview,controllerSettings:{operationsEnabled:false,uiFeatures:{workloadBackups:false}}}));
 await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());
 expect(screen.getByRole("heading",{name:"Workload backups is disabled"})).toBeTruthy();expect(fetch).toHaveBeenCalledTimes(1);
 expect(fetch.mock.calls[0][0]).toBe("/api/v1/workload-backups/backup/delete-preview");
});

it("discards a restore review that arrives after its UI feature is disabled",async()=>{
 vi.spyOn(api,"workloadBackups").mockResolvedValue([backup]);vi.spyOn(api,"workloadBackupOperations").mockResolvedValue([op]);
 let complete!:(response:Response)=>void;const fetch=vi.fn().mockImplementation(()=>new Promise<Response>(resolve=>{complete=resolve}));vi.stubGlobal("fetch",fetch);
 const content=(value:Overview)=><><WorkloadBackups overview={value} project="p"/><DestructiveConfirmations/></>;
 const view=render(content(overview));const user=userEvent.setup();
 await user.click(await screen.findByText(/PostgreSQL backup ·/));await user.selectOptions(screen.getByLabelText("Restore destination"),"run");await user.click(screen.getByRole("button",{name:"Review data restore"}));
 expect(fetch.mock.calls[0][0]).toBe("/api/v1/workload-backups/backup/restore/run/preview");
 view.rerender(content({...overview,controllerSettings:{operationsEnabled:false,uiFeatures:{workloadBackups:false}}}));
 expect(fetch.mock.calls[0][1].signal.aborted).toBe(true);
 await act(async()=>complete(new Response(JSON.stringify({resourceId:"backup",resourceType:"workload_backup",name:"Orders backup",action:"restore",version:"2",summary:"Restore archive",resources:[]}),{status:200})));
 expect(screen.queryByRole("dialog")).toBeNull();expect(fetch).toHaveBeenCalledTimes(1);
});
