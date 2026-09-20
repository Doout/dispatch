// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { ObservationSettings } from "./ObservationSettings";
import { ObservationStatus, observationsClient } from "./observationsClient";
import { App, Overview } from "./api";
const app = { id: "app", projectId: "project", name: "API" } as App;
const overview = { identity: { id: "owner", systemRole: "owner" } } as Overview;
const status: ObservationStatus = { configuration: { appId: "app", projectId: "project", revision: 2, scheduled: false, intervalSeconds: 300, staleAfterSeconds: 900, notificationsEnabled: false, webhookConfigured: true, updatedAt: "2026-09-20T00:00:00Z" }, observation: { appId: "app", projectId: "project", configurationRevision: 2, source: "manual", state: "healthy", drift: "synced", health: "healthy", endpoint: { state: "not_configured", tls: "not_checked", durationMs: 0, message: "", location: "Dispatch controller" }, consecutiveFailures: 0, location: "Dispatch controller" }, freshness: "stale", checking: false, events: [] };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("retains write-only delivery credentials while enabling scheduled checks", async () => {
 vi.spyOn(observationsClient, "get").mockResolvedValue(status);
 const save = vi.spyOn(observationsClient, "save").mockResolvedValue(status);
 render(<ObservationSettings application={app} overview={overview} />);
 await screen.findByText("Observation stale"); const user = userEvent.setup(); await user.click(screen.getByRole("button", { name: "Configure checks" }));
 await user.click(screen.getByRole("checkbox", { name: "Enable scheduled observations" }));
 await user.click(screen.getByRole("button", { name: "Save observation settings" }));
 await waitFor(() => expect(save).toHaveBeenCalledWith("app", expect.objectContaining({ scheduled: true, revision: 2 })));
 expect(save.mock.calls[0][1]).not.toHaveProperty("webhookUrl");
});
it("checks inline and keeps endpoint reachability separate from readiness", async () => {
 const checked: ObservationStatus = { ...status, configuration: { ...status.configuration, endpointUrl: "https://example.com/health" }, freshness: "fresh", observation: { ...status.observation, checkedAt: new Date().toISOString(), endpoint: { ...status.observation.endpoint, state: "tls_failed", tls: "invalid", message: "TLS certificate validation failed." } } };
 vi.spyOn(observationsClient, "get").mockResolvedValue(status);vi.spyOn(observationsClient, "check").mockResolvedValue(checked);const onChecked = vi.fn();
 render(<ObservationSettings application={app} overview={overview} onChecked={onChecked} />);
 await screen.findByText("Observation stale");await userEvent.setup().click(screen.getByRole("button", { name: "Check runtime and endpoint" }));
 await screen.findByText(/TLS certificate validation failed/);expect(onChecked).toHaveBeenCalledOnce();expect(screen.getByText(/separate from workload readiness/)).toBeTruthy();
});
it("does not expose configuration actions to viewers", async () => {
 vi.spyOn(observationsClient, "get").mockResolvedValue(status);
 const viewer = { identity: { id: "viewer", systemRole: "member" }, projectPermissions: { project: ["project.view"] } } as unknown as Overview;
 render(<ObservationSettings application={app} overview={viewer} />);await screen.findByText("Observation stale");expect(screen.queryByRole("button", { name: "Configure checks" })).toBeNull();
});
