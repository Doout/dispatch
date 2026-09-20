// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import * as client from "./api";
import { type Overview } from "./api";
import { SettingsPage } from "./SettingsPage";

const owner = { identity: { id: "owner", systemRole: "owner" }, projects: [], projectPermissions: {} } as unknown as Overview;
const member: Overview = { ...owner, identity: { ...owner.identity!, systemRole: "member" } };
const withObserved = (operationsEnabled: boolean): Overview => ({ ...owner, controllerSettings: { operationsEnabled } } as Overview);
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const control = () => screen.getByRole("switch", { name: "Operations" }) as HTMLInputElement;

it("loads the persisted default-off value before allowing an owner to enable Operations", async () => {
 let finishGet!: (value: unknown) => void;
 let reads = 0;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(_path: string, init?: RequestInit) => init?.method === "PUT" || reads++ > 0 ? { operationsEnabled: true } as T : new Promise<unknown>(resolve => { finishGet = resolve; }) as Promise<T>);
 const changed = vi.fn(); const user = userEvent.setup(); render(<SettingsPage overview={owner} onChanged={changed} />);
 expect(control().disabled).toBe(true); expect(control().checked).toBe(false); expect(screen.getByText("Off by default")).toBeTruthy();
 expect(screen.getByText(/users who already have access/)).toBeTruthy();
 await act(async () => finishGet({ operationsEnabled: false }));
 expect(control().disabled).toBe(false); expect(control().checked).toBe(false);
 await user.click(control()); await screen.findByText("Operations enabled.");
 expect(control().checked).toBe(true); expect(control().disabled).toBe(false);
 expect(request).toHaveBeenCalledWith("/api/v1/settings", { method: "PUT", body: JSON.stringify({ operationsEnabled: true }) });
 expect(changed).toHaveBeenCalledTimes(1);
});

it("does not request or expose controller settings to a non-owner", () => {
 const request = vi.spyOn(client, "request"); render(<SettingsPage overview={member} />);
 expect(screen.getByText("Owner access required")).toBeTruthy(); expect(screen.queryByRole("switch")).toBeNull(); expect(request).not.toHaveBeenCalled();
});

it("guards repeated changes while saving and rolls the switch back after a failed write", async () => {
 let rejectSave!: (cause: Error) => void;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(_path: string, init?: RequestInit) => init?.method === "PUT" ? new Promise<unknown>((_resolve, reject) => { rejectSave = reject; }) as Promise<T> : { operationsEnabled: false } as T);
 const changed = vi.fn(); render(<SettingsPage overview={owner} onChanged={changed} />);
 await waitFor(() => expect(control().disabled).toBe(false));
 fireEvent.click(control()); fireEvent.click(control());
 expect(control().disabled).toBe(true); expect(control().checked).toBe(true);
 expect(request.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1);
 await act(async () => rejectSave(new Error("Controller could not save the setting")));
 expect(control().checked).toBe(false); expect(control().disabled).toBe(false); expect(screen.getByRole("alert").textContent).toContain("Controller could not save the setting"); expect(changed).not.toHaveBeenCalled();
});

it("uses the server's saved value and preserves it when overview refresh fails", async () => {
 const request = vi.spyOn(client, "request").mockResolvedValue({ operationsEnabled: true });
 const changed = vi.fn().mockRejectedValue(new Error("Overview unavailable")); const user = userEvent.setup(); render(<SettingsPage overview={owner} onChanged={changed} />);
 await waitFor(() => expect(control().disabled).toBe(false)); expect(control().checked).toBe(true);
 await user.click(control()); await screen.findByText("Setting saved. Navigation could not be refreshed.");
 expect(request).toHaveBeenCalledWith("/api/v1/settings", { method: "PUT", body: JSON.stringify({ operationsEnabled: false }) });
 expect(control().checked).toBe(true); expect(screen.queryByRole("alert")).toBeNull();
});

it("keeps the switch unavailable after a read failure and lets the owner retry", async () => {
 const request = vi.spyOn(client, "request").mockRejectedValueOnce(new Error("Settings unavailable")).mockResolvedValue({ operationsEnabled: false }); const user = userEvent.setup(); render(<SettingsPage overview={owner} />);
 await screen.findByRole("alert"); expect(control().disabled).toBe(true);
 await user.click(screen.getByRole("button", { name: "Refresh" })); await waitFor(() => expect(control().disabled).toBe(false));
 expect(screen.queryByRole("alert")).toBeNull(); expect(request).toHaveBeenCalledTimes(2);
});

it("hides the controls immediately after owner access is revoked and ignores an unfinished save", async () => {
 let completeSave!: (value: unknown) => void;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(_path: string, init?: RequestInit) => init?.method === "PUT" ? new Promise<unknown>(resolve => { completeSave = resolve; }) as Promise<T> : { operationsEnabled: false } as T);
 const changed = vi.fn(); const user = userEvent.setup(); const { rerender } = render(<SettingsPage overview={owner} onChanged={changed} />);
 await waitFor(() => expect(control().disabled).toBe(false)); await user.click(control());
 rerender(<SettingsPage overview={member} onChanged={changed} />); expect(screen.queryByRole("switch")).toBeNull();
 await act(async () => completeSave({ operationsEnabled: true }));
 expect(changed).not.toHaveBeenCalled(); expect(request).toHaveBeenCalledTimes(2); expect(screen.getByText("Owner access required")).toBeTruthy();
});

it("ignores an older read when a newer overview update has triggered a settings refresh", async () => {
 let finishOld!: (value: unknown) => void;
 const request = vi.spyOn(client, "request").mockImplementationOnce(async <T,>() => new Promise<unknown>(resolve => { finishOld = resolve; }) as Promise<T>).mockResolvedValue({ operationsEnabled: true });
 const { rerender } = render(<SettingsPage overview={withObserved(false)} />);
 rerender(<SettingsPage overview={withObserved(true)} />);
 await waitFor(() => expect(control().disabled).toBe(false)); expect(control().checked).toBe(true);
 await act(async () => finishOld({ operationsEnabled: false }));
 expect(control().checked).toBe(true); expect(request).toHaveBeenCalledTimes(2);
});

it("rechecks the persisted value after an overview update received during a save", async () => {
 let completeSave!: (value: unknown) => void;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(_path: string, init?: RequestInit) => init?.method === "PUT" ? new Promise<unknown>(resolve => { completeSave = resolve; }) as Promise<T> : { operationsEnabled: false } as T);
 const user = userEvent.setup(); const { rerender } = render(<SettingsPage overview={withObserved(false)} />);
 await waitFor(() => expect(control().disabled).toBe(false)); await user.click(control());
 rerender(<SettingsPage overview={withObserved(true)} />); expect(request).toHaveBeenCalledTimes(2);
 await act(async () => completeSave({ operationsEnabled: true }));
 await waitFor(() => expect(request).toHaveBeenCalledTimes(3)); await waitFor(() => expect(control().disabled).toBe(false)); expect(control().checked).toBe(false);
});

it("reconciles a concurrent owner restoring the initial value even when the overview boolean stays unchanged", async () => {
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(_path: string, init?: RequestInit) => ({ operationsEnabled: init?.method === "PUT" }) as T);
 const user = userEvent.setup(); let updateOverview!: () => void;
 const changed = vi.fn(async () => { updateOverview(); });
 const { rerender } = render(<SettingsPage overview={withObserved(false)} onChanged={changed} />);
 updateOverview = () => rerender(<SettingsPage overview={withObserved(false)} onChanged={changed} />);
 await waitFor(() => expect(control().disabled).toBe(false));
 await user.click(control());
 await screen.findByText("Operations disabled.");
 expect(control().checked).toBe(false); expect(control().disabled).toBe(false);
 expect(request.mock.calls.map(([, init]) => init?.method ?? "GET")).toEqual(["GET", "PUT", "GET"]);
 expect(changed).toHaveBeenCalledTimes(1);
});
