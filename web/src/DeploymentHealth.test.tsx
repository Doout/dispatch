// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { DeploymentHealth } from "./DeploymentHealth";

afterEach(cleanup);
it("distinguishes failed route evidence from passed workload checks", () => {
 render(<DeploymentHealth health={{ state: "failed", policy: {timeoutSeconds:30,checkTimeoutSeconds:5,intervalSeconds:2,failureThreshold:3,checks:[]}, checks: [
  {check:{id:"workload-readiness",kind:"container",scope:"workload"},state:"passed",attempts:3,failures:0,message:"Check passed."},
  {check:{id:"virtual-host",kind:"http",scope:"route"},state:"failed",attempts:3,failures:3,httpStatus:503,message:"Check reached its failure threshold."},
 ]}} />);
 expect(screen.getByText("workload · container")).toBeTruthy();
 expect(screen.getByText("route · http")).toBeTruthy();
 expect(screen.getByText(/HTTP 503/)).toBeTruthy();
 expect(screen.getByText(/Workflow tests are reported separately/)).toBeTruthy();
});
it("omits health claims for old deployments without evidence",()=>{
 const {container}=render(<DeploymentHealth />);expect(container.textContent).toBe("");
});
