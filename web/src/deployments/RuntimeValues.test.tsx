// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api, type Deployment } from "../api";
import { DeploymentRuntime } from "./RuntimeTopology";
afterEach(()=>{cleanup();vi.restoreAllMocks()});
const deployment:Deployment={id:"one",appId:"app",commitSha:"sha",specDigest:"digest",state:"succeeded",message:"",createdAt:"2026-09-10T00:00:00Z"};
it("defaults to chart values and allows the complete supplied input",async()=>{
 vi.spyOn(api,"deploymentTopology").mockResolvedValue({
  target:"dev",namespace:"slots",release:"one",runtime:"kubernetes",live:true,topology:{columns:[],nodes:[],edges:[]},
  values:[{path:"unrelated.enabled",value:true},{path:"image.tag",value:"v1"}],
  chartValues:[{path:"image.tag",value:"v1",source:"Build output: build.image"},{path:"replicas",value:1,source:"Chart default"}],
  valuesAnalyzed:true,
 });
 render(<DeploymentRuntime deployment={deployment} section="values"/>);
 expect(await screen.findByText("Chart default")).not.toBeNull();
 expect(screen.queryByText("unrelated.enabled")).toBeNull();
 expect(api.deploymentTopology).toHaveBeenCalledWith("one",true);
 await userEvent.click(screen.getByRole("checkbox",{name:"Show all supplied values"}));
 expect(screen.getByText("unrelated.enabled")).not.toBeNull();
 expect(screen.queryByText("Chart default")).toBeNull();
});
it("keeps supplied values when chart analysis is unavailable",async()=>{
 vi.spyOn(api,"deploymentTopology").mockResolvedValue({
  target:"dev",namespace:"slots",release:"one",runtime:"kubernetes",live:false,topology:{columns:[],nodes:[],edges:[]},
  values:[{path:"password",value:"••••••••",redacted:true}],
  valuesAnalyzed:false,valuesNotes:["Chart analysis is unavailable."],
 });
 render(<DeploymentRuntime deployment={deployment} section="values"/>);
 expect(await screen.findByText("Chart analysis is unavailable.")).not.toBeNull();
 expect(screen.getByText("••••••••")).not.toBeNull();
});

it("shows tpl previews with their raw expression and preserves redaction", async () => {
 const raw = "{{ .Values.archer_server.model_maps.default_llm_model }}";
 vi.spyOn(api, "deploymentTopology").mockResolvedValue({
  target: "dev", namespace: "slots", release: "one", runtime: "kubernetes", live: true,
  topology: { columns: [], nodes: [], edges: [] }, values: [], valuesAnalyzed: true,
  chartValues: [
   { path: "configMap.data.MODEL", value: raw, renderedValue: "azure-openai/gpt-5.4", source: "gitops:default-values.yaml" },
   { path: "password", value: "••••••••", redacted: true, renderedValue: "must-not-display" },
   { path: "empty", value: '{{ "" }}', renderedValue: "" },
  ],
 });
 render(<DeploymentRuntime deployment={deployment} section="values" />);
 expect(await screen.findByText("azure-openai/gpt-5.4")).not.toBeNull();
 expect(screen.getByText("gitops:default-values.yaml")).not.toBeNull();
 expect(screen.getAllByText("Rendered with Helm tpl · preview")).toHaveLength(2);
 expect(screen.queryByText("must-not-display")).toBeNull();
 const rawValue = screen.getByText(raw);
 expect(rawValue.closest("details")?.open).toBe(false);
 await userEvent.click(screen.getAllByText("Raw value")[0]);
 expect(rawValue.closest("details")?.open).toBe(true);
});
