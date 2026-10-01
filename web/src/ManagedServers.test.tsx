// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import * as api from "./api";
import { ManagedServers } from "./ManagedServers";
const overview = { projects: [{ id: "project", name: "Project" }], secrets: [{ id: "ssh", name: "SSH", type: "ssh_private_key" }] } as api.Overview;
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("requires a named review and preserves the request key after an uncertain response", async () => {
  const submitted: Record<string, unknown>[] = [];
  const input = { projectId: "project", providerId: "provider", name: "Machine", region: "region", size: "size", image: "image", network: "network", sshKeySecretId: "ssh", config: {}, secretRefs: {} };
  vi.spyOn(api, "request").mockImplementation(async (path, init) => {
    if (path.endsWith("/infrastructure/providers")) return [{ id: "provider", name: "Mock", manifest: { configurationSchema: { properties: {} } } }] as never;
    if (path.endsWith("/options")) { const body = JSON.parse(init?.body as string); const id = String(body.kind).slice(0, -1); return [{ id, name: id }] as never; }
    if (path.endsWith("/review")) return { id: "review", name: "Machine", digest: "review-digest", expiresAt: "2099-01-01T00:00:00Z", input } as never;
    if (init?.method === "POST") { submitted.push(JSON.parse(init.body as string)); if (submitted.length === 1) throw new Error("Response unavailable"); return {} as never; }
    return [] as never;
  });
  render(<ManagedServers overview={overview} />);
  fireEvent.click(screen.getByRole("button", { name: "Create server" }));
  fireEvent.change(screen.getByLabelText("Project"), { target: { value: "project" } });
  await screen.findByRole("option", { name: "Mock" });
  fireEvent.change(screen.getByLabelText("Infrastructure provider"), { target: { value: "provider" } });
  fireEvent.change(screen.getByLabelText("Machine name"), { target: { value: "Machine" } });
  fireEvent.change(screen.getByLabelText(/SSH public key/), { target: { value: "ssh" } });
  fireEvent.click(screen.getByRole("button", { name: "Load machine choices" }));
  await screen.findByRole("option", { name: "region" });
  for (const field of ["Region", "Size", "Image", "Network"]) fireEvent.change(screen.getByLabelText(field), { target: { value: field.toLowerCase() } });
  fireEvent.submit(screen.getByRole("form", { name: "Create on-demand server" }));
  const accept = await screen.findByRole("button", { name: "Create reviewed server" });
  expect((accept as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Type Machine to confirm"), { target: { value: "Machine" } });
  fireEvent.click(accept);
  await screen.findByText("Response unavailable");
  fireEvent.click(screen.getByRole("button", { name: "Create reviewed server" }));
  await waitFor(() => expect(submitted.length).toBe(2));
  expect(submitted[0]).toEqual(submitted[1]);
  expect(submitted[0]).toMatchObject({ reviewId: "review", digest: "review-digest", confirmName: "Machine" });
  expect(submitted[0].requestKey).toBeTruthy();
});
