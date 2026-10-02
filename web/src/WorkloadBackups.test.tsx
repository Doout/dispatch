// @vitest-environment jsdom
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {afterEach,expect,it,vi} from "vitest";
import {api,Overview,WorkloadBackup,WorkloadBackupOperation} from "./api";
import {WorkloadBackups} from "./WorkloadBackups";
const overview={identity:{id:"owner",systemRole:"owner",permissions:[]},projects:[{id:"p",name:"Orders"}],services:[{id:"database",name:"orders-db",projectId:"p",type:"postgresql",provisionRunId:"run",provisionTarget:{provider:"docker",serverId:"target"}}]} as unknown as Overview;
const backup:WorkloadBackup={id:"backup",projectId:"p",sourceRunId:"run",storageId:"volume",serverId:"target",state:"ready",revision:2,artifactId:"backup",consistency:"database-native",format:"postgresql-custom",checksum:"checksum",bytes:200,encryption:"AES-256-GCM-chunks-v1",location:"target-local",policy:"retain",checkCount:0,verificationState:"not_verified",cleanupState:"complete",verificationIntervalHours:24,createdAt:"2026-10-02T01:00:00Z",updatedAt:"2026-10-02T01:00:00Z"};
const op:WorkloadBackupOperation={id:"op",backupId:"backup",projectId:"p",action:"backup",state:"succeeded",revision:2,recoveryAfter:"0001-01-01T00:00:00Z"};
afterEach(()=>{cleanup();vi.restoreAllMocks()});
it("creates a native backup and requires a destination before requesting restore review",async()=>{
 vi.spyOn(api,"workloadBackups").mockResolvedValue([backup]);vi.spyOn(api,"workloadBackupOperations").mockResolvedValue([op]);const create=vi.spyOn(api,"createWorkloadBackup").mockResolvedValue(op);const restore=vi.spyOn(api,"restoreWorkloadBackup").mockResolvedValue(op);
 render(<WorkloadBackups overview={overview} project="p"/>);const user=userEvent.setup();await user.selectOptions(screen.getByLabelText("Source service"),"run");await user.click(screen.getByRole("button",{name:"Create backup"}));await waitFor(()=>expect(create).toHaveBeenCalledWith({sourceRunId:"run",verificationIntervalHours:24,checks:[]}));
 await user.click(await screen.findByText(/PostgreSQL backup ·/));const review=await screen.findByRole("button",{name:"Review data restore"});expect(review.hasAttribute("disabled")).toBe(true);await user.selectOptions(screen.getByLabelText("Restore destination"),"run");await user.click(review);expect(restore).toHaveBeenCalledWith("backup","run");expect(screen.getByText(/Restoring overwrites objects present in the archive/)).toBeTruthy();
});
it("hides mutation controls from project viewers",async()=>{
 vi.spyOn(api,"workloadBackups").mockResolvedValue([backup]);vi.spyOn(api,"workloadBackupOperations").mockResolvedValue([op]);const viewer={...overview,identity:{...overview.identity!,systemRole:"member"},projectPermissions:{p:["project.view"]}} as Overview;
 render(<WorkloadBackups overview={viewer} project="p"/>);await userEvent.click(await screen.findByText(/PostgreSQL backup ·/));expect(screen.queryByRole("button")).toBeNull();
});
