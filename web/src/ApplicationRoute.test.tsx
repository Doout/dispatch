// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, type ApplicationRoute } from "./api";
import { ApplicationRoutePanel } from "./ApplicationRoute";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const route = {appId:"app",projectId:"project",serverId:"target",hostname:"orders.apps.example.com",entryPoint:"websecure",requireTls:true,tlsResolver:"letsencrypt",requestedDeploymentId:"new",deploymentId:"new",destination:"http://127.0.0.1:32123",previousDeploymentId:"old",state:"pending",message:"Waiting for public certificate verification.",dns:"resolved",certificate:{state:"pending",message:"Certificate issuance is pending."},updatedAt:"2026-10-01T00:00:00Z"} satisfies ApplicationRoute;
it("keeps a healthy workload with pending TLS out of live state", async () => {
 vi.spyOn(api,"applicationRoute").mockResolvedValue(route);
 render(<ApplicationRoutePanel appID="app" canConfigure={false}/>);
 expect(await screen.findByText("Not live")).toBeTruthy();
 expect(screen.queryByRole("link")).toBeNull();
 expect(screen.getByText("Certificate issuance is pending.")).toBeTruthy();
 expect(screen.queryByRole("button")).toBeNull();
});
it("links only the controller-verified public endpoint and preserves destination evidence",async()=>{
 vi.spyOn(api,"applicationRoute").mockResolvedValue({...route,state:"active",certificate:{state:"verified",message:"Verified certificate."}});
 render(<ApplicationRoutePanel appID="app" canConfigure/>);
 expect(await screen.findByText("Live")).toBeTruthy();
 expect(screen.getByRole("link").getAttribute("href")).toBe("https://orders.apps.example.com");
 expect(screen.getByText("http://127.0.0.1:32123")).toBeTruthy();
 expect(screen.getByText("old")).toBeTruthy();
 expect(screen.getByRole("button",{name:"Check DNS and certificate"})).toBeTruthy();
});
