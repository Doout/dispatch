// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import * as api from "./api";
import { TargetBootstraps } from "./TargetBootstraps";
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("requires a trusted host review, clears SSH credentials, and approves the saved plan", async () => {
  const requests: { path: string; body: Record<string, unknown> }[] = [];
  const plan = { targetName: "Target", method: "ssh", platform: "linux-amd64", imageFamily: "existing-systemd", sshHost: "192.0.2.10", sshPort: 22, sshUser: "root", sshFingerprint: "SHA256:verified-host", artifactSha256: "approved-agent", controllerUrl: "https://dispatch.example.com", actions: ["Preserve identity"] };
  vi.spyOn(api, "request").mockImplementation(async (path, init) => {
    if (init?.method === "POST") { const body = JSON.parse(init.body as string); requests.push({ path, body }); if (path.endsWith("/review")) return { id: "install", digest: "reviewed-digest", plan, reviewExpiresAt: "2099-01-01T00:00:00Z" } as never; return {} as never; }
    return [] as never;
  });
  render(<TargetBootstraps overview={{ servers: [] } as unknown as api.Overview} />);
  fireEvent.click(screen.getByRole("button", { name: "Install or recover agent" }));
  fireEvent.change(screen.getByLabelText("Target name"), { target: { value: "Target" } });
  fireEvent.change(screen.getByLabelText("SSH host"), { target: { value: "192.0.2.10" } });
  fireEvent.change(screen.getByLabelText("SSH password"), { target: { value: "private-password" } });
  fireEvent.change(screen.getByLabelText(/Verified SSH host public key/), { target: { value: "ssh-ed25519 verified" } });
  expect((screen.getByRole("button", { name: "Review installation" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByLabelText("I verified this host key through a trusted channel."));
  fireEvent.submit(screen.getByRole("form", { name: "Review target installation" }));
  await screen.findByText("SHA256:verified-host");
  expect(screen.queryByDisplayValue("private-password")).toBeNull();
  expect(requests[0].body.credentials).toEqual({ password: "private-password" });
  expect((screen.getByRole("button", { name: "Approve installation" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Type Target to approve"), { target: { value: "Target" } });
  fireEvent.click(screen.getByRole("button", { name: "Approve installation" }));
  await waitFor(() => expect(requests).toHaveLength(2));
  expect(requests[1]).toEqual({ path: "/api/v1/infrastructure/bootstrap/install/accept", body: { digest: "reviewed-digest", confirmName: "Target" } });
});
