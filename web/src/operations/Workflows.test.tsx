// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { useState } from "react";
import * as client from "../api";
import { type Overview, type AccessOverview } from "../api";
import { type OperationsFilters } from "../routes";
import { Activity, auditActionLabel } from "./Activity";
import { Ownership } from "./Ownership";
import { IdentityMappings } from "./IdentityMappings";
import { type AuditEvent, type OwnershipPage } from "./client";

const overview = { identity: { id: "owner", systemRole: "owner" }, projects: [{ id: "p", name: "Platform" }, { id: "q", name: "Payments" }], apps: [], services: [] } as unknown as Overview;
const event = (id: string, actorName = "Alice"): AuditEvent => ({ id, actorId: `actor-${id}`, actorName, projectId: "p", action: "POST /api/v1/operations/backups", outcome: "succeeded", createdAt: "2026-09-20T05:00:00Z" });
const ownership: OwnershipPage = { items: [{ appId: "generated", appName: "Orders production", projectId: "p", owner: { principalType: "user", principalId: "person", displayName: "Alice", updatedAt: "2026-09-20T00:00:00Z" } }, { appId: "other", appName: "Payments production", projectId: "q" }] };
const candidates = { users: [{ id: "person", username: "alice", displayName: "Alice" }, { id: "bob", username: "bob", displayName: "Bob" }], teams: [{ id: "team", name: "Operators" }] };
const access = { providers: [{ id: "github", name: "GitHub", type: "github", state: "ready" }], teams: [{ id: "team", name: "Operators" }] } as AccessOverview;
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
function ActivityPage({ initial = {}, navigate }: { initial?: OperationsFilters; navigate?: (route: any) => void }) { const [filters, setFilters] = useState(initial); return <Activity overview={overview} filters={filters} onFilters={setFilters} onNavigate={navigate} />; }
function OwnershipPageView({ viewer = false, initial = {}, changed }: { viewer?: boolean; initial?: OperationsFilters; changed?: () => void }) { const [filters, setFilters] = useState(initial); return <Ownership overview={viewer ? { ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { p: ["project.view"], q: ["project.view"] } } : overview} filters={filters} onFilters={setFilters} onChanged={changed} />; }

it("presents readable activity first with raw method, IDs, and SPA links inside details", async () => {
 const request = vi.spyOn(client, "request").mockResolvedValue([{ ...event("one"), action: "PUT /api/v1/apps/{id}/owner", appId: "generated", resourceId: "generated", impersonatorId: "admin" }]);
 const navigate = vi.fn(); render(<ActivityPage navigate={navigate} />);
 const title = await screen.findByText("Update application owner");
 const details = title.closest("details")!; expect(details.open).toBe(false);
 expect(details.querySelector("summary")!.textContent).not.toContain("/api/v1");
 expect(details.querySelector("summary")!.textContent).toContain("Alice");
 expect(details.querySelector("summary")!.textContent).toContain("Accepted");
 fireEvent.click(details.querySelector("summary")!);
 expect(details.open).toBe(true);
 expect(within(details).getByText("PUT /api/v1/apps/{id}/owner")).toBeTruthy();
 expect(within(details).getByText("Impersonator ID")).toBeTruthy();
 fireEvent.click(within(details).getByRole("link", { name: "View application deployments" }));
 expect(navigate).toHaveBeenCalledWith({ view: "deployments", deploymentFilters: { layout: "list", app: "generated" } });
 expect(request).toHaveBeenCalledTimes(1);
 expect(auditActionLabel("DELETE /api/v1/identity-team-mappings/{id}")).toBe("Remove identity mapping");
 expect(auditActionLabel("POST /api/v1/apps/{id}/release-preview")).toBe("Preview deployment");
 expect(auditActionLabel("POST /api/v1/deployments/{id}/rollback-preview")).toBe("Preview release rollback");
 expect(auditActionLabel("PUT /api/v1/deployments/{id}/release")).toBe("Update release notes");
 expect(auditActionLabel("POST /api/v1/config-sources/{id}/sync")).toBe("Sync repository configuration");
 expect(screen.queryByLabelText("Activity project")).toBeNull();
});

it("applies activity query, outcome, and date filters on the server before requesting a page", async () => {
 const request = vi.spyOn(client, "request").mockResolvedValue([]); const user = userEvent.setup();
 render(<ActivityPage initial={{ projectId: "p", since: "2026-09-19T00:00:00.123456789Z", until: "2026-09-20T00:00:00Z" }} />);
 await screen.findByText("No matching activity");
 await user.type(screen.getByLabelText("Search activity"), "Alice backup"); expect(request).toHaveBeenCalledTimes(1);
 await user.click(screen.getByRole("button", { name: "Search" }));
 await waitFor(() => expect(request).toHaveBeenCalledTimes(2));
 let query = new URL(request.mock.calls.at(-1)![0], "https://dispatch.test").searchParams;
 expect(query.get("q")).toBe("Alice backup"); expect(query.get("projectId")).toBe("p"); expect(query.get("since")).toBe("2026-09-19T00:00:00.123456789Z");
 await user.selectOptions(screen.getByLabelText("Activity outcome"), "rejected");
 await waitFor(() => expect(request).toHaveBeenCalledTimes(3));
 query = new URL(request.mock.calls.at(-1)![0], "https://dispatch.test").searchParams; expect(query.get("outcome")).toBe("rejected"); expect(query.has("before")).toBe(false);
 await user.click(screen.getByRole("button", { name: "Clear activity date range" }));
 await waitFor(() => expect(request).toHaveBeenCalledTimes(4));
 query = new URL(request.mock.calls.at(-1)![0], "https://dispatch.test").searchParams; expect(query.has("since")).toBe(false); expect(query.has("until")).toBe(false); expect(query.get("outcome")).toBe("rejected");
});

it("uses the last event cursor and ignores an older page after the project scope changes", async () => {
 let finishOld!: (rows: AuditEvent[]) => void;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(path: string) => { const query = new URL(path, "https://dispatch.test").searchParams; if (query.get("projectId") === "q") return [event("payments", "Payments actor")] as T; if (query.has("before")) return new Promise<AuditEvent[]>(resolve => { finishOld = resolve; }) as Promise<T>; return Array.from({ length: 100 }, (_, index) => event(`event-${index}`, "Platform actor")) as T; });
 const user = userEvent.setup(); const { rerender } = render(<Activity overview={overview} filters={{ projectId: "p" }} onFilters={vi.fn()} />);
 await user.click(await screen.findByRole("button", { name: "Load older activity" }));
 expect(new URL(request.mock.calls.at(-1)![0], "https://dispatch.test").searchParams.get("before")).toBe("event-99");
 rerender(<Activity overview={overview} filters={{ projectId: "q" }} onFilters={vi.fn()} />);
 await screen.findByText(/Payments actor/); finishOld([event("old-page", "Stale actor")]);
 await waitFor(() => expect(screen.queryByText(/Stale actor/)).toBeNull()); expect(screen.queryByText(/Platform actor/)).toBeNull(); expect(screen.queryByRole("button", { name: "Load older activity" })).toBeNull();
});

it("lets viewers inspect generated application ownership without configuration requests", async () => {
 const request = vi.spyOn(client, "request").mockResolvedValue({ items: [...ownership.items, { appId: "removed-owner", appName: "Legacy app", projectId: "p", owner: { principalType: "team", principalId: "removed-team", displayName: "", updatedAt: "2026-09-20T00:00:00Z" } }] });
 render(<OwnershipPageView viewer />);
 await screen.findByText("Orders production"); expect(screen.getByText("Alice")).toBeTruthy(); expect(screen.getByText("Owner unavailable")).toBeTruthy(); expect(screen.getByText(/removed-team/)).toBeTruthy();
 expect(screen.queryByRole("button", { name: /Assign owner|Change owner/ })).toBeNull(); expect(request.mock.calls.some(([path]) => path.includes("owner-candidates"))).toBe(false);
});

it("does not carry eligible owner candidates across application/project switches", async () => {
 let finishPlatform!: (value: typeof candidates) => void;
 vi.spyOn(client, "request").mockImplementation(async <T,>(path: string) => { if (path.includes("/operations/ownership")) return ownership as T; if (path.includes("/projects/p/")) return new Promise<typeof candidates>(resolve => { finishPlatform = resolve; }) as Promise<T>; return { users: [{ id: "payments", displayName: "Payments operator", username: "payments" }], teams: [] } as T; });
 const user = userEvent.setup(); render(<OwnershipPageView />);
 await user.click(await screen.findByRole("button", { name: "Change owner for Orders production" }));
 await screen.findByText("Loading eligible owners…");
 await user.click(screen.getByRole("button", { name: "Assign owner for Payments production" }));
 await screen.findByRole("option", { name: "Payments operator" }); finishPlatform(candidates);
 await waitFor(() => expect(screen.queryByRole("option", { name: "Bob" })).toBeNull()); expect(screen.getByRole("form", { name: "Owner for Payments production" })).toBeTruthy();
});

it("clears ownership explicitly once and keeps role data out of the mutation", async () => {
 let complete!: (value: unknown) => void; const changed = vi.fn();
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(path: string, init?: RequestInit) => { if (init?.method === "PUT") return new Promise<unknown>(resolve => { complete = resolve; }) as Promise<T>; return path.includes("owner-candidates") ? candidates as T : ownership as T; });
 const user = userEvent.setup(); render(<OwnershipPageView changed={changed} />);
 await user.click(await screen.findByRole("button", { name: "Change owner for Orders production" }));
 await user.selectOptions(await screen.findByLabelText("Owner", { exact: true }), "");
 const clear = screen.getByRole("button", { name: "Clear owner" }); fireEvent.click(clear); fireEvent.click(clear);
 await waitFor(() => expect(request.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1));
 expect(JSON.parse(request.mock.calls.find(([, init]) => init?.method === "PUT")![1]!.body as string)).toEqual({ principalType: "user", principalId: "" });
 complete({}); await waitFor(() => expect(changed).toHaveBeenCalledTimes(1));
});

it("searches and pages ownership on the server, clearing an editor when filters change", async () => {
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(path: string) => path.includes("before=") ? { items: [ownership.items[1]] } as T : { items: [ownership.items[0]], next: "opaque-cursor" } as T);
 const user = userEvent.setup(); render(<OwnershipPageView />);
 await user.click(await screen.findByRole("button", { name: "Load more applications" })); await screen.findByText("Payments production");
 expect(request.mock.calls.some(([path]) => new URL(path, "https://dispatch.test").searchParams.get("before") === "opaque-cursor")).toBe(true);
 await user.selectOptions(screen.getByLabelText("Ownership assignment"), "unassigned");
 await waitFor(() => expect(new URL(request.mock.calls.at(-1)![0], "https://dispatch.test").searchParams.get("unassigned")).toBe("true"));
 await user.selectOptions(screen.getByLabelText("Ownership assignment"), "assigned");
 await waitFor(() => expect(new URL(request.mock.calls.at(-1)![0], "https://dispatch.test").searchParams.get("unassigned")).toBe("false"));
 expect(screen.queryByLabelText("Ownership project")).toBeNull();
});

it("keeps mappings list-first and requires a separate removal review before access changes", async () => {
 vi.spyOn(client.api, "access").mockResolvedValue(access);
 const mapping = { id: "map", providerId: "github", externalGroup: "org/platform", teamId: "team" };
 const request = vi.spyOn(client, "request").mockResolvedValue([mapping]); const user = userEvent.setup(); render(<IdentityMappings />);
 await screen.findByText("org/platform"); expect(screen.queryByRole("form")).toBeNull();
 await user.click(screen.getByRole("button", { name: "Review removal of org/platform mapping" }));
 expect(request.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
 expect(screen.getByText(/Memberships supplied by this mapping are revoked immediately/)).toBeTruthy();
 await user.click(screen.getByRole("button", { name: "Cancel" })); expect(request.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
 await user.click(screen.getByRole("button", { name: "Review removal of org/platform mapping" })); await user.click(screen.getByRole("button", { name: "Remove mapping now" }));
 await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/identity-team-mappings/map", { method: "DELETE" })); await screen.findByText("No identity mappings yet");
});

it("guards mapping saves against repeated submissions and exposes failures without losing the form", async () => {
 vi.spyOn(client.api, "access").mockResolvedValue(access);
 let reject!: (cause: Error) => void;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(_path: string, init?: RequestInit) => init?.method === "POST" ? new Promise((_resolve, fail) => { reject = fail; }) as Promise<T> : [] as T);
 const user = userEvent.setup(); render(<IdentityMappings />);
 await user.click(await screen.findByRole("button", { name: "Add mapping" }));
 await user.selectOptions(screen.getByLabelText("Sign-in provider"), "github"); await user.selectOptions(screen.getByLabelText("Dispatch team"), "team"); await user.type(screen.getByLabelText("GitHub team"), "org/platform");
 const form = screen.getByRole("form", { name: "Add identity mapping" }); fireEvent.submit(form); fireEvent.submit(form);
 await waitFor(() => expect(request.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
 reject(new Error("Mapping could not be saved")); await screen.findByRole("alert"); expect((screen.getByLabelText("GitHub team") as HTMLInputElement).value).toBe("org/platform"); expect((screen.getByRole("button", { name: "Save mapping" }) as HTMLButtonElement).disabled).toBe(false);
});

it("does not close a new owner editor when an earlier application save finishes", async () => {
 let complete!: (value: unknown) => void;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(path: string, init?: RequestInit) => init?.method === "PUT" ? new Promise<unknown>(resolve => { complete = resolve; }) as Promise<T> : path.includes("owner-candidates") ? candidates as T : ownership as T);
 const user = userEvent.setup(); render(<OwnershipPageView />);
 await user.click(await screen.findByRole("button", { name: "Change owner for Orders production" }));
 await user.selectOptions(await screen.findByLabelText("Owner", { exact: true }), "bob");
 await user.click(screen.getByRole("button", { name: "Save owner" }));
 await user.click(screen.getByRole("button", { name: "Assign owner for Payments production" }));
 await screen.findByRole("form", { name: "Owner for Payments production" }); complete({});
 await waitFor(() => expect(screen.getByRole("form", { name: "Owner for Payments production" })).toBeTruthy());
 const mutations = request.mock.calls.filter(([, init]) => init?.method === "PUT");
 expect(mutations).toHaveLength(1); expect(mutations[0][0]).toBe("/api/v1/apps/generated/owner");
});


it("hides project and controller events when owner access is revoked and ignores pending pages", async () => {
 let finishOld!: (rows: AuditEvent[]) => void;
 let restricted = false;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(path: string) => {
  if (path.includes("before=")) return new Promise<AuditEvent[]>(resolve => { finishOld = resolve; }) as Promise<T>;
  if (restricted) return [{ ...event("allowed", "Allowed actor"), projectId: "q" }] as T;
  return Array.from({ length: 100 }, (_, index) => ({ ...event(`event-${index}`, index === 0 ? "Controller owner" : "Revoked project actor"), projectId: index === 0 ? undefined : "p" })) as T;
 });
 const user = userEvent.setup(); const { rerender } = render(<Activity overview={overview} filters={{}} onFilters={vi.fn()} />);
 await user.click(await screen.findByRole("button", { name: "Load older activity" }));
 restricted = true;
 const member: Overview = { ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { q: ["project.view"] } };
 rerender(<Activity overview={member} filters={{}} onFilters={vi.fn()} />);
 expect(screen.queryByText(/Controller owner/)).toBeNull(); expect(screen.queryByText(/Revoked project actor/)).toBeNull();
 await screen.findByText(/Allowed actor/); finishOld([event("stale", "Stale page actor")]);
 await waitFor(() => expect(screen.queryByText(/Stale page actor/)).toBeNull());
 expect(request).toHaveBeenCalledTimes(3);
 rerender(<Activity overview={{ ...member, projectPermissions: {} }} filters={{}} onFilters={vi.fn()} />);
 expect(screen.queryByText(/Allowed actor/)).toBeNull();
 await waitFor(() => expect(request).toHaveBeenCalledTimes(4));
 expect(screen.queryByText(/Allowed actor/)).toBeNull();
});

it("refreshes ownership when grants change and keeps revoked records out of late responses", async () => {
 let finishOld!: (value: OwnershipPage) => void;
 let restricted = false;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(path: string) => {
  if (path.includes("before=")) return new Promise<OwnershipPage>(resolve => { finishOld = resolve; }) as Promise<T>;
  return (restricted ? { items: [ownership.items[1]] } : { ...ownership, next: "older" }) as T;
 });
 const viewer: Overview = { ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { p: ["project.view"], q: ["project.view"] } };
 const user = userEvent.setup(); const { rerender } = render(<Ownership overview={viewer} filters={{}} onFilters={vi.fn()} />);
 await user.click(await screen.findByRole("button", { name: "Load more applications" }));
 restricted = true;
 rerender(<Ownership overview={{ ...viewer, projectPermissions: { q: ["project.view"] } }} filters={{}} onFilters={vi.fn()} />);
 expect(screen.queryByText("Orders production")).toBeNull();
 await screen.findByText("Payments production"); finishOld({ items: [{ ...ownership.items[0], appName: "Late revoked application" }] });
 await waitFor(() => expect(screen.queryByText("Late revoked application")).toBeNull());
 expect(request).toHaveBeenCalledTimes(3);
});

it("closes the owner editor immediately when configuration permission is removed", async () => {
 let finishCandidates!: (value: typeof candidates) => void;
 const request = vi.spyOn(client, "request").mockImplementation(async <T,>(path: string) => path.includes("owner-candidates") ? new Promise<typeof candidates>(resolve => { finishCandidates = resolve; }) as Promise<T> : ownership as T);
 const operator: Overview = { ...overview, identity: { ...overview.identity!, systemRole: "member" }, projectPermissions: { p: ["project.view", "project.configure"] } };
 const { rerender } = render(<Ownership overview={operator} filters={{ appId: "generated" }} onFilters={vi.fn()} />);
 await screen.findByText("Loading eligible owners…");
 rerender(<Ownership overview={{ ...operator, projectPermissions: { p: ["project.view"] } }} filters={{ appId: "generated" }} onFilters={vi.fn()} />);
 expect(screen.queryByRole("form", { name: "Owner for Orders production" })).toBeNull();
 finishCandidates(candidates);
 await screen.findByText("Orders production");
 expect(screen.queryByRole("button", { name: /Change owner|Save owner/ })).toBeNull();
 expect(request.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(0);
});
