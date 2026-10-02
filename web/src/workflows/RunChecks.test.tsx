// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import type { WorkflowCheckReport } from "../api";
import { RunChecks } from "./RunChecks";

afterEach(cleanup);
const check: WorkflowCheckReport = { id: "report", revisionId: "run", resourceId: "resource", projectId: "project", githubAppId: "app", repository: "example/service", commitSha: "a".repeat(40), name: "Dispatch/qa/service", kind: "qa", externalId: "dispatch-check:report", state: "retrying", attempts: 2, complete: false, updatedAt: "2026-10-02T00:00:00Z", status: "in_progress", error: "Grant Checks: write and approve the installation update. Reporting will retry." };
it("shows exact commit, retry action and independent reporting state", () => {
 render(<RunChecks checks={[check]} />);
 expect(screen.getByText("a".repeat(12)).getAttribute("title")).toBe(check.commitSha);
 expect(screen.getByText("in progress")).toBeTruthy();
 expect(screen.getByText("Retry pending")).toBeTruthy();
 expect(screen.getByRole("alert").textContent).toContain("Checks: write");
 expect(screen.getByText(/Reporting errors do not change its outcome/)).toBeTruthy();
});
it("links a completed check without hiding a failed execution", () => {
 render(<RunChecks checks={[{ ...check, error: undefined, complete: true, state: "reported", status: "completed", conclusion: "failure", htmlUrl: "https://github.example.test/example/service/runs/10" }]} />);
 expect(screen.getByRole("link", { name: check.name }).getAttribute("href")).toContain("/runs/10");
 expect(screen.getByText("failure")).toBeTruthy();
 expect(screen.getByText("Complete")).toBeTruthy();
 expect(screen.queryByRole("alert")).toBeNull();
});
it("does not invent reports for historical runs", () => {
 const { container } = render(<RunChecks checks={[]} />);
 expect(container.textContent).toBe("");
});
