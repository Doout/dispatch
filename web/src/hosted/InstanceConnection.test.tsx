// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { InstanceConnection } from "./InstanceConnection";
import { PlatformApp } from "./PlatformApp";
import { directInstanceDestination, type Membership } from "./client";

const membership: Membership = {
  tenant: {
    id: "instance",
    name: "example",
    slug: "example",
    state: "active",
    createdAt: "2026-10-13T00:00:00Z",
  },
  role: "owner",
  url: "https://example.doodship.test/api/v1/hosted/auth/start",
  connection: {
    state: "not_connected",
    remoteAccess: false,
    instanceUrl: "",
    backendMode: "connected",
  },
};
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
  window.history.replaceState({}, "", "/");
});

it("creates a one-use token only on owner request and keeps it out of URLs and browser storage", async () => {
  const fetcher = vi.fn(async (_path: string, options: RequestInit) =>
    options.method === "POST"
      ? json({
          enrollmentToken: "private-once",
          expiresAt: "2026-10-13T01:00:00Z",
          platformURL: "https://doodship.test",
          subject: "account-id",
        })
      : json(membership.connection),
  );
  vi.stubGlobal("fetch", fetcher);
  render(
    <InstanceConnection
      membership={membership}
      onClose={vi.fn()}
      onChanged={vi.fn().mockResolvedValue(undefined)}
    />,
  );
  const create = await screen.findByRole("button", {
    name: "Create connection token",
  });
  expect(fetcher.mock.calls.some(([, init]) => init.method === "POST")).toBe(
    false,
  );
  fireEvent.click(create);
  await screen.findByLabelText("Connection token");
  expect(
    (screen.getByLabelText("Connection token") as HTMLInputElement).type,
  ).toBe("password");
  expect(
    (screen.getByLabelText("Your Doodship account ID") as HTMLInputElement)
      .value,
  ).toBe("account-id");
  expect(window.location.href).not.toContain("private-once");
  expect(JSON.stringify({ ...localStorage, ...sessionStorage })).not.toContain(
    "private-once",
  );
});

it("does not expose enrollment or disconnect actions to a member", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        json({ ...membership.connection, state: "connected" }),
      ),
  );
  render(
    <InstanceConnection
      membership={{ ...membership, role: "member" }}
      onClose={vi.fn()}
      onChanged={vi.fn()}
    />,
  );
  await screen.findByRole("heading", { name: "Connected" });
  expect(screen.queryByRole("button", { name: /token|Disconnect/ })).toBeNull();
});

it("confirms disconnection and removes the displayed token", async () => {
  let connected = true;
  const fetcher = vi.fn(async (_path: string, options: RequestInit) => {
    if (options.method === "DELETE") {
      connected = false;
      return new Response(null, { status: 204 });
    }
    return json({
      ...membership.connection,
      state: connected ? "connected" : "not_connected",
    });
  });
  vi.stubGlobal("fetch", fetcher);
  const user = userEvent.setup();
  render(
    <InstanceConnection
      membership={membership}
      onClose={vi.fn()}
      onChanged={vi.fn().mockResolvedValue(undefined)}
    />,
  );
  await user.click(
    await screen.findByRole("button", { name: "Disconnect server" }),
  );
  expect(fetcher.mock.calls.some(([, init]) => init.method === "DELETE")).toBe(
    false,
  );
  await user.click(screen.getByRole("button", { name: "Disconnect" }));
  await screen.findByRole("heading", { name: "No server connected" });
});

it("opens the owner's direct address without attaching a managed login handoff", async () => {
  vi.stubEnv("DEV", false);
  vi.stubEnv("VITE_HOSTED_LANDING_PAGE", "false");
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/account")
      return json({
        id: "member",
        name: "Member",
        email: "member@example.test",
        emailVerified: true,
        platformAdmin: false,
        state: "active",
      });
    if (path === "/api/v1/account/tenants")
      return json({
        memberships: [
          {
            ...membership,
            role: "member",
            connection: {
              ...membership.connection,
              state: "connected",
              instanceUrl: "https://internal.example.test",
            },
          },
        ],
        invitations: [],
      });
    throw new Error(path);
  });
  vi.stubGlobal("fetch", fetcher);
  render(
    <PlatformApp
      configuration={{
        mode: "platform",
        origin: "https://doodship.test",
        registrationEnabled: false,
        backendMode: "connected",
      }}
    />,
  );
  const link = await screen.findByRole("link", { name: /Open instance/ });
  expect(link.getAttribute("href")).toBe("https://internal.example.test/");
  expect(screen.queryByRole("button", { name: "Connection" })).toBeNull();
  await waitFor(() =>
    expect(fetcher.mock.calls.some(([path]) => path.includes("handoff"))).toBe(
      false,
    ),
  );
});

it.each([
  "javascript:alert(1)",
  "https://user:secret@example.test",
  "https://example.test/?token=secret",
  "https://example.test/#token=secret",
])("rejects unsafe direct address %s", (value) => {
  expect(() => directInstanceDestination(value)).toThrow();
});
