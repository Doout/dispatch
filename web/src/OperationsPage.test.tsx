// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import * as client from "./api";
import * as catalog from "./deployments/DeploymentCatalog";
import type { CatalogItem } from "./deployments/catalogClient";
import { type Overview, type ServiceConnection } from "./api";
import { ServiceOperations } from "./ServiceOperations";
import { ServicesPage } from "./ServicesPage";

const overview={identity:{id:"owner",systemRole:"owner"},projects:[{id:"p",name:"Platform"}],apps:[],deployments:[],servers:[],services:[],secrets:[]} as unknown as Overview;
afterEach(()=>{cleanup();vi.restoreAllMocks();});



it("deduplicates selected consumers and keeps protected workflow consumers unavailable",async()=>{
 const service={id:"db",projectId:"p",revision:2,consumers:[]} as unknown as ServiceConnection;const consumer={appId:"app",appName:"API",appliedRevision:1,redeploymentRequired:true,targetName:"Local",environment:"development",canRedeploy:true};const calls:{path:string;body?:RequestInit["body"]}[]=[];
 vi.spyOn(client,"request").mockImplementation(async <T,>(path:string,init?:RequestInit)=>{calls.push({path,body:init?.body});return path.endsWith("/impact")?{serviceId:"db",revision:2,consumers:[{...consumer,alias:"primary"},{...consumer,alias:"analytics"},{...consumer,alias:"managed",appId:"managed",appName:"Managed",canRedeploy:false,reason:"Use workflow promotion"}]} as T:[{appId:"app",deployment:{id:"new"}}] as T;});
 const bindings=vi.fn(),changed=vi.fn().mockResolvedValue(undefined);const user=userEvent.setup();render(<ServiceOperations service={service} overview={overview} onChanged={changed} onBindings={bindings}/>);await user.click(await screen.findByRole("checkbox",{name:"Redeploy API binding primary"}));expect((screen.getByRole("checkbox",{name:"Redeploy API binding analytics"}) as HTMLInputElement).checked).toBe(true);expect((screen.getByRole("checkbox",{name:"Redeploy Managed binding managed"}) as HTMLInputElement).disabled).toBe(true);await user.click(screen.getByRole("button",{name:"Redeploy selected (1)"}));expect(calls.some(c=>c.path.endsWith("/redeploy"))).toBe(false);await user.click(screen.getByRole("button",{name:"Confirm redeploy"}));await waitFor(()=>expect(changed).toHaveBeenCalled());const sent=JSON.parse(calls.find(c=>c.path.endsWith("/redeploy"))!.body as string);expect(sent).toEqual({revision:2,appIds:["app"],confirm:true});await user.click(screen.getAllByRole("button",{name:"Bindings"})[0]);expect(bindings).toHaveBeenCalledWith("app");
});
it("rotates write-only credentials without automatically redeploying consumers",async()=>{
 const service={id:"db",projectId:"p",name:"database",description:"",type:"generic",revision:4,fields:{password:{sensitive:true,configured:true}},availableFields:["password"],consumers:[]} as ServiceConnection;
 const request=vi.spyOn(client,"request").mockResolvedValue({serviceId:"db",revision:4,consumers:[]});const save=vi.spyOn(client.api,"saveService").mockResolvedValue(service);const user=userEvent.setup();render(<ServicesPage overview={{...overview,services:[service]}} onChanged={async()=>{}}/>);await user.click(screen.getByRole("button",{name:"Rotate credentials"}));expect((screen.getByLabelText("password") as HTMLInputElement).value).toBe("");await user.type(screen.getByLabelText("password"),"replacement-value");await user.click(screen.getByRole("button",{name:/Save/}));await waitFor(()=>expect(save).toHaveBeenCalled());expect(save.mock.calls[0][1].fields.password.value).toBe("replacement-value");expect(request.mock.calls.some(([path])=>path.endsWith("/redeploy"))).toBe(false);
});
it("refreshes service impact for consumer deployments while retaining selection across unrelated updates",async()=>{
 const consumer={appId:"app",appName:"API",alias:"db",appliedRevision:1,redeploymentRequired:true};const service={id:"db",projectId:"p",revision:2,consumers:[consumer]} as unknown as ServiceConnection;
 const impact={serviceId:"db",revision:2,consumers:[{...consumer,targetName:"Local",environment:"development",canRedeploy:true}]};const request=vi.spyOn(client,"request").mockResolvedValue(impact);const user=userEvent.setup();const props={service,overview,onChanged:async()=>{}};const view=render(<ServiceOperations {...props}/>);
 await user.click(await screen.findByRole("checkbox",{name:"Redeploy API binding db"}));expect(request).toHaveBeenCalledTimes(1);
 const unrelated={id:"unrelated",appId:"another-app",state:"queued",createdAt:"2026-09-20T00:00:00Z"} as client.Deployment;
 view.rerender(<ServiceOperations {...props} overview={{...overview,deployments:[unrelated]}}/>);expect(request).toHaveBeenCalledTimes(1);expect((screen.getByRole("checkbox",{name:"Redeploy API binding db"}) as HTMLInputElement).checked).toBe(true);
 request.mockResolvedValue({...impact,consumers:[{...impact.consumers[0],canRedeploy:false,reason:"Deployment active"}]});const active={...unrelated,id:"active",appId:"app"};view.rerender(<ServiceOperations {...props} overview={{...overview,deployments:[unrelated,active]}}/>);await waitFor(()=>expect(request).toHaveBeenCalledTimes(2));await waitFor(()=>expect((screen.getByRole("checkbox",{name:"Redeploy API binding db"}) as HTMLInputElement).disabled).toBe(true));
 request.mockResolvedValue({...impact,consumers:[{...impact.consumers[0],appliedRevision:2,redeploymentRequired:false}]});view.rerender(<ServiceOperations {...props} service={{...service,consumers:[{...consumer,appliedRevision:2,redeploymentRequired:false}]}} overview={{...overview,deployments:[{...active,state:"succeeded"}]}}/>);await waitFor(()=>expect(request).toHaveBeenCalledTimes(3));await screen.findByText("Current",{exact:true});
});


it("opens generated consumer bindings as repository managed read-only details",async()=>{
 vi.spyOn(catalog,"useDeploymentCatalog").mockReturnValue({items:[{appId:"generated",appName:"Orders production",projectId:"p"} as CatalogItem],loading:false,error:""});
 const service={id:"db",projectId:"p",name:"Database",type:"generic",revision:2,fields:{},consumers:[]} as unknown as ServiceConnection;
 vi.spyOn(client,"request").mockResolvedValue({serviceId:"db",revision:2,consumers:[{appId:"generated",appName:"Orders production",alias:"database",appliedRevision:2,canRedeploy:false}]});vi.spyOn(client.api,"serviceBindings").mockResolvedValue([{alias:"database",serviceRef:"db",environment:{DATABASE_URL:"connectionUrl"}}]);
 const user=userEvent.setup();render(<ServicesPage overview={{...overview,services:[service]}} onChanged={async()=>{}}/>);await user.click(await screen.findByRole("button",{name:"Bindings"}));await screen.findByRole("heading",{name:"Orders production services"});expect(await screen.findByText("DATABASE_URL")).toBeTruthy();expect(screen.getByText(/Bindings are managed in repository configuration/)).toBeTruthy();expect(screen.queryByRole("button",{name:"Edit binding"})).toBeNull();expect(screen.queryByRole("button",{name:"Connect service"})).toBeNull();
});
