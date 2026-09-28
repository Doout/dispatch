// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, type Overview, type SecretUsage } from "./api";
import { SecretsPage } from "./App";
const secret = { id: "bundle", name: "App configuration", type: "json" as const, environmentVariable: "APP_CONFIG", createdAt: "", updatedAt: "" };
const overview: Overview = { demo: false, secretStorageConfigured: true, projects: [], servers: [], apps: [], deployments: [], eventTriggers: [], previews: [], previewGroups: [], previewGroupRuns: [], githubApps: [], relayWebhooks: [], secrets: [secret] };
const usage: SecretUsage = {
  secretId: "bundle", warnings: [],
  consumers: [{ id: "checkout", kind: "workflow", name: "Checkout", state: "ready", references: ["Job build → TOKEN · JSON key auth.token", "Job build → URL · JSON key connection.URL"], applications: [{ id: "checkout-development", name: "Checkout development", target: "Development", archived: false }] }],
  archived: [{ id: "old-checkout", kind: "workflow", name: "Old checkout", state: "removed", references: ["Job build → TOKEN"], applications: [] }],
};
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
function page() { render(<SecretsPage overview={overview} onChanged={async () => undefined} onDelete={() => undefined} />); }
it("shows consumer counts, key references, archived consumers, and deployment links", async () => {
  vi.spyOn(api, "secretUsage").mockResolvedValue([usage]);
  const deployments = vi.spyOn(api, "secretUsageDeployments").mockResolvedValueOnce({ items: [{ id: "deployment-1", appId: "checkout-development", state: "succeeded", specDigest: "", message: "", commitSha: "a1234567890abcdef", createdAt: "2026-09-28T10:00:00Z" }], next: "deployment-1" }).mockResolvedValueOnce({ items: [{ id: "deployment-2", appId: "checkout-development", state: "failed", specDigest: "", message: "", commitSha: "b1234567890abcdef", createdAt: "2026-09-27T10:00:00Z" }] });
  page();
  const button = await screen.findByRole("button", { name: "View usage for App configuration" });
  await screen.findByText("Used by 1");
  expect(screen.queryByText("0 uses")).toBeNull();
  fireEvent.click(button);
  const dialog = screen.getByRole("dialog", { name: "Used by" });
  expect(within(dialog).getByText("Job build → URL · JSON key connection.URL")).toBeTruthy();
  expect(within(dialog).getByRole("link", { name: /Deployment history · Checkout development/ }).getAttribute("href")).toBe("/deployments?application=checkout-development");
  expect(within(dialog).getByText("Old checkout")).toBeTruthy();
  expect(deployments).not.toHaveBeenCalled();
  fireEvent.click(within(dialog).getByRole("button", { name: "Show deployments" }));
  const run = await within(dialog).findByRole("link", { name: /Checkout development deployment succeeded/ });
  expect(run.getAttribute("href")).toBe("/deployments/deployment-1");
  fireEvent.click(within(dialog).getByRole("button", { name: "Load more" }));
  expect(await within(dialog).findByRole("link", { name: /Checkout development deployment failed/ })).toBeTruthy();
  expect(deployments).toHaveBeenLastCalledWith("bundle", "deployment-1");
  fireEvent.keyDown(window, { key: "Escape" });
  expect(screen.queryByRole("dialog")).toBeNull();
});
it("shows loading and failed lookups without asserting a zero, and supports retry", async () => {
  const load = vi.spyOn(api, "secretUsage").mockRejectedValueOnce(new Error("Usage lookup failed")).mockResolvedValueOnce([usage]);
  page();
  expect(screen.getByText("Checking usage...")).toBeTruthy();
  await screen.findByText("Usage unavailable");
  fireEvent.click(screen.getByRole("button", { name: "View usage for App configuration" }));
  expect(screen.getByRole("alert").textContent).toContain("Usage lookup failed");
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByText("Current references · 1");
  expect(load).toHaveBeenCalledTimes(2);
});
it("labels archived-only references and warns when the scan is incomplete", async () => {
  vi.spyOn(api, "secretUsage").mockResolvedValue([{ ...usage, consumers: [] }]);
  page();
  await screen.findByText("1 archived reference");
  expect(screen.queryByText("No references")).toBeNull();
  cleanup();
  vi.mocked(api.secretUsage).mockResolvedValue([{ ...usage, consumers: [], warnings: ["Could not inspect Broken workflow because its YAML is invalid."] }]);
  page();
  await screen.findByText("Usage incomplete");
  fireEvent.click(screen.getByRole("button", { name: "View usage for App configuration" }));
  expect(screen.getByRole("alert").textContent).toContain("Broken workflow");
});
