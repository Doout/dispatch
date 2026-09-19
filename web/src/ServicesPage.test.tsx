// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { ServicesPage } from "./ServicesPage";
import { ApplicationServices } from "./ApplicationServices";
import { api, type App, type Overview, type ServiceConnection } from "./api";

const service: ServiceConnection = { id: "db", projectId: "p", name: "orders-db", description: "", type: "postgresql", fields: { host: { value: "localhost", sensitive: false, configured: true }, port: { value: "5432", sensitive: false, configured: true }, database: { value: "orders", sensitive: false, configured: true }, username: { value: "app", sensitive: false, configured: true }, password: { sensitive: true, configured: true }, sslmode: { value: "verify-full", sensitive: false, configured: true } }, availableFields: ["host", "password", "connectionUrl"], revision: 1, consumers: [] };
const overview: Overview = { demo: false, secretStorageConfigured: true, identity: { id: "owner", username: "owner", displayName: "Owner", systemRole: "owner", permissions: [] }, projects: [{ id: "p", name: "Project", description: "", createdAt: "" }], services: [service], apps: [], servers: [], deployments: [], eventTriggers: [], previews: [], previewGroups: [], previewGroupRuns: [], secrets: [], githubApps: [], relayWebhooks: [] };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("preserves a saved password when editing ordinary service metadata", async () => {
 const save = vi.spyOn(api, "saveService").mockResolvedValue(service);
 render(<ServicesPage overview={overview} onChanged={async () => {}} />);
 const user = userEvent.setup(); await user.click(screen.getByRole("button", { name: "Edit" }));
 expect((screen.getByLabelText("password") as HTMLInputElement).value).toBe("");
 await user.type(screen.getByLabelText("Description"), "Application database");
 await user.click(screen.getByRole("button", { name: "Save service" }));
 await waitFor(() => expect(save).toHaveBeenCalled());
 expect(save.mock.calls[0][1].fields.password).toBeUndefined();
 expect(save.mock.calls[0][1].description).toBe("Application database");
});
it("shows check location and prevents viewers from running checks", () => {
 const member: Overview = { ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { p: ["project.view"] }, services: [{ ...service, check: { state: "succeeded", message: "Connected", location: "Dispatch controller", checkedAt: "2026-09-19T00:00:00Z", durationMs: 4 } }] };
 render(<ServicesPage overview={member} onChanged={async () => {}} />);
 expect(screen.getByText(/Dispatch controller/)).not.toBeNull();
 expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();
 expect(screen.queryByRole("button", { name: "Register service" })).toBeNull();
});
it("creates a Docker runtime binding without copying credentials into it", async () => {
 vi.spyOn(api, "serviceBindings").mockResolvedValue([]);
 const save = vi.spyOn(api, "saveServiceBindings").mockResolvedValue([]);
 const app = { id: "app", name: "API", projectId: "p", buildType: "dockerfile", template: false } as App;
 render(<ApplicationServices application={app} overview={overview} onBack={() => {}} onChanged={async () => {}} />);
 const user = userEvent.setup(); await user.click(await screen.findByRole("button", { name: "Connect service" }));
 await user.click(screen.getByRole("button", { name: "Save binding" }));
 await waitFor(() => expect(save).toHaveBeenCalledWith("app", [{ alias: "database", serviceRef: "db", environment: { DATABASE_URL: "connectionUrl" } }]));
});
it("keeps repository-managed bindings read-only", async () => {
 vi.spyOn(api, "serviceBindings").mockResolvedValue([]);
 const app = { id: "app", name: "API", projectId: "p", buildType: "helm", generated: true } as App;
 render(<ApplicationServices application={app} overview={overview} onBack={() => {}} onChanged={async () => {}} />);
 expect(await screen.findByText("No services connected.")).not.toBeNull();
 expect(screen.queryByRole("button", { name: "Connect service" })).toBeNull();
});
