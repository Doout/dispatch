// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api, type ServiceResource } from "./api";
import { ServiceResourcePanel } from "./ServiceResourcePanel";
const resource: ServiceResource = {runId:"run",projectId:"project",serviceId:"service",name:"orders-db",target:{provider:"docker",serverId:"target",resourceName:"owned-db"},state:"unresolved",policy:"retain",revision:2,operationId:"run",message:"Connection could not be saved.",createdAt:"",updatedAt:""};
afterEach(() => {cleanup();vi.restoreAllMocks();});
it("offers retry only after inspection confirms absence and keeps deletion explicit",async()=>{
 vi.spyOn(api,"serviceResource").mockResolvedValue(resource);
 vi.spyOn(api,"inspectServiceResource").mockResolvedValue({runId:"run",projectId:"project",serverId:"target",provider:"docker",state:"absent",storageRetained:true});
 const recover=vi.spyOn(api,"recoverServiceResource").mockResolvedValue({...resource,state:"recovering"});
 render(<ServiceResourcePanel runId="run" canManage canRecover onChanged={async()=>{}}/>);
 expect(await screen.findByText(/Recovery required/)).toBeTruthy();
 expect(screen.queryByRole("button",{name:"Retry original provision"})).toBeNull();
 expect(screen.getByText(/Workload deletion retains storage/)).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"Inspect resource"}));
 await userEvent.click(await screen.findByRole("button",{name:"Retry original provision"}));
 expect(recover).toHaveBeenCalledWith("run","retry");
 expect(screen.getByRole("button",{name:"Delete resource"}).hasAttribute("disabled")).toBe(true);
});
it("does not offer mutations to project viewers",async()=>{
 vi.spyOn(api,"serviceResource").mockResolvedValue(resource);
 render(<ServiceResourcePanel runId="run" canManage={false} canRecover={false} onChanged={async()=>{}}/>);
 await screen.findByText(/Recovery required/);
 expect(screen.queryByRole("button")).toBeNull();
});
