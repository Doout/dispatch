// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { DeployForm } from "../App";
import { api, type App } from "../api";
import { releaseClient, type ReleasePreview } from "./releaseClient";
afterEach(()=>{cleanup();vi.restoreAllMocks();});
it("previews a first deployment, requires review of partial checks, and deploys the resolved revision",async()=>{
 const app={id:"api",name:"API",sourceRepo:"https://example.com/app",buildType:"dockerfile"} as App;
 const preview={review:{expectedAppName:"application",projectId:"p",appSpecDigest:"spec",bindingsDigest:"bindings",serviceRevisions:{}},ready:true,revision:"immutable-sha",target:"Local",namespace:"",release:"",checks:[{name:"Runtime validation",state:"unavailable",message:"Image checks run during deployment"}],bindings:[],comparison:{available:false,changes:[]},message:"Point-in-time preview"} as unknown as ReleasePreview;
 vi.spyOn(releaseClient,"preview").mockResolvedValue(preview);const deploy=vi.spyOn(api,"deploy").mockResolvedValue({id:"new-run"} as Awaited<ReturnType<typeof api.deploy>>);const complete=vi.fn().mockResolvedValue(undefined);const user=userEvent.setup();render(<DeployForm apps={[app]} onComplete={complete} onCancel={()=>{}}/>);
 await user.click(screen.getByRole("button",{name:"Preview deployment"}));await screen.findByRole("heading",{name:"Review deployment"});expect(deploy).not.toHaveBeenCalled();expect((screen.getByRole("button",{name:"Deploy reviewed revision"}) as HTMLButtonElement).disabled).toBe(true);await user.click(screen.getByRole("checkbox"));await user.click(screen.getByRole("button",{name:"Deploy reviewed revision"}));await waitFor(()=>expect(deploy).toHaveBeenCalledWith("api","immutable-sha",preview.review));expect(complete).toHaveBeenCalledWith("new-run");
});
it("never starts a rollout after a failed preflight",async()=>{
 vi.spyOn(releaseClient,"preview").mockResolvedValue({ready:false,revision:"HEAD",checks:[{name:"Credentials",state:"failed",message:"Missing credential"}],bindings:[],comparison:{available:false,changes:[]}} as unknown as ReleasePreview);const deploy=vi.spyOn(api,"deploy");const user=userEvent.setup();render(<DeployForm apps={[{id:"api",name:"API",sourceRepo:"repository",buildType:"helm"} as App]} onComplete={async()=>{}} onCancel={()=>{}}/>);await user.click(screen.getByRole("button",{name:"Preview deployment"}));await screen.findByText("Missing credential");expect(screen.queryByRole("button",{name:"Deploy reviewed revision"})).toBeNull();expect(deploy).not.toHaveBeenCalled();
});
it("requires a fresh preview when reviewed inputs changed before acceptance",async()=>{
 const review={expectedAppName:"application",projectId:"p",appSpecDigest:"spec",bindingsDigest:"bindings",serviceRevisions:{db:1}};
 vi.spyOn(releaseClient,"preview").mockResolvedValue({review,ready:true,revision:"sha",checks:[],bindings:[],comparison:{available:false,changes:[]}} as unknown as ReleasePreview);
 vi.spyOn(api,"deploy").mockRejectedValue(Object.assign(new Error("Service configuration changed. Preview again."),{status:409}));const user=userEvent.setup();render(<DeployForm apps={[{id:"api",name:"API",sourceRepo:"repository",buildType:"helm"} as App]} onComplete={async()=>{}} onCancel={()=>{}}/>);
 await user.click(screen.getByRole("button",{name:"Preview deployment"}));await user.click(await screen.findByRole("button",{name:"Deploy reviewed revision"}));await screen.findByText("Service configuration changed. Preview again.");expect(screen.queryByRole("button",{name:"Deploy reviewed revision"})).toBeNull();expect(screen.getByRole("button",{name:"Preview deployment"})).toBeTruthy();
});
