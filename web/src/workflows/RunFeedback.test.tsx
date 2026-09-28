// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import type { Overview, WorkflowFeedback } from "../api";
import { RunFeedback } from "./RunFeedback";

afterEach(cleanup);
const overview = { githubApps: [{ id: "app", webUrl: "https://github.example.test" }] } as Overview;
const feedback: WorkflowFeedback = { statusContext: "Dispatch/qa", deploymentId: "deployment", complete: false, reviewOnSuccess: "approve", targets: [{ githubAppId: "app", repository: "example/service", number: 42, commitSha: "a".repeat(40), status: "success", review: "skipped", skipReason: "PR has newer commits", error: "Grant Pull requests: write" }] };
it("shows PR links, tested commits, skip reasons and actionable reporting errors", () => {
 render(<RunFeedback feedback={feedback} overview={overview} />);
 expect(screen.getByRole("link", { name: "example/service #42" }).getAttribute("href")).toBe("https://github.example.test/example/service/pull/42");
 expect(screen.getByText("a".repeat(12)).getAttribute("title")).toBe("a".repeat(40));
 expect(screen.getByText("PR has newer commits")).not.toBeNull();
 expect(screen.getByRole("alert").textContent).toContain("Grant Pull requests: write");
 expect(screen.getByRole("alert").textContent).toContain("Dispatch will retry");
});
it("leaves reviews disabled by default and distinguishes pending reporting", () => {
 render(<RunFeedback feedback={{ ...feedback, reviewOnSuccess: undefined, targets: [{ ...feedback.targets[0], status: undefined, review: undefined, skipReason: undefined, error: undefined }] }} overview={overview} />);
 expect(screen.getByText("Waiting to report")).not.toBeNull();
 expect(screen.getByText("Disabled")).not.toBeNull();
 expect(screen.getByText(/Reporting in progress/)).not.toBeNull();
});
