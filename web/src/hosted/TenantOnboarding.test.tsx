// @vitest-environment jsdom
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PlatformApp } from "./PlatformApp";
import { JoinTenant } from "./JoinTenant";

const configuration = {
  mode: "platform" as const,
  origin: "https://doodship.example.test",
  registrationEnabled: false,
};
const account = {
  id: "user",
  name: "New user",
  email: "user@example.test",
  emailVerified: true,
  platformAdmin: false,
  state: "active",
};
const tenant = {
  id: "tenant",
  name: "my-team",
  slug: "my-team",
  state: "pending",
  createdAt: "2026-10-11T00:00:00Z",
};
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json" },
  });
beforeEach(() => {
  vi.stubEnv("DEV", false);
  vi.stubEnv("VITE_HOSTED_LANDING_PAGE", "false");
  window.history.replaceState({}, "", "/");
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

it("updates setup status automatically and stops refreshing when the instance is active", async () => {
  vi.useFakeTimers();
  let state = "pending";
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/account") return json(account);
    if (path === "/api/v1/account/tenants")
      return json({
        memberships: [
          {
            tenant: { ...tenant, state },
            role: "owner",
            url: "https://my-team.doodship.example.test/api/v1/hosted/auth/start",
          },
        ],
        invitations: [],
      });
    throw new Error(`Unexpected request ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  await act(async () => {
    render(<PlatformApp configuration={configuration} />);
  });
  expect(screen.getByText("Setting up")).not.toBeNull();
  expect(screen.queryByRole("link", { name: /Open instance/ })).toBeNull();
  state = "provisioning";
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
  expect(screen.getByText("Setting up")).not.toBeNull();
  state = "active";
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
  expect(
    screen.getByRole("link", { name: /Open instance/ }).getAttribute("href"),
  ).toBe("https://my-team.doodship.example.test/api/v1/hosted/auth/start");
  expect(screen.queryByText("Setting up")).toBeNull();
  expect(screen.queryByText(/Instance setup is in progress/)).toBeNull();
  const calls = fetcher.mock.calls.length;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(20000);
  });
  expect(fetcher.mock.calls).toHaveLength(calls);
});

it("lets an admitted user create their own instance without choosing another owner", async () => {
  let created = false;
  const fetcher = vi.fn(async (path: string, options: RequestInit) => {
    if (path === "/api/v1/account") return json(account);
    if (path === "/api/v1/account/tenants" && options.method === "POST") {
      expect(JSON.parse(String(options.body))).toEqual({
        name: "my-team",
        slug: "my-team",
      });
      created = true;
      return json(tenant, 202);
    }
    if (path === "/api/v1/account/tenants")
      return json({
        memberships: created
          ? [
              {
                tenant,
                role: "owner",
                url: "https://my-team.doodship.example.test/api/v1/hosted/auth/start",
              },
            ]
          : [],
        invitations: [],
      });
    throw new Error(`Unexpected request ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={configuration} />);
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole("button", { name: "Create instance" }),
  );
  const name = screen.getByLabelText("Instance name");
  expect((name as HTMLInputElement).value).toBe("");
  expect(screen.queryByLabelText("Owner email")).toBeNull();
  await user.type(name, "my-team");
  await user.click(screen.getByRole("button", { name: "Create instance" }));
  await screen.findByRole("heading", { name: "My instances" });
  expect(screen.getByText("owner")).not.toBeNull();
  expect(screen.queryByRole("link", { name: /Open instance/ })).toBeNull();
  expect(fetcher.mock.calls.some(([path]) => path.includes("/platform/"))).toBe(
    false,
  );
});

it("shows a pending request without an instance link or membership", async () => {
  let requested = false;
  const fetcher = vi.fn(async (path: string, options: RequestInit) => {
    if (path === "/api/v1/account") return json(account);
    if (path === "/api/v1/account/tenants")
      return json({
        memberships: [],
        invitations: [],
        joinRequests: requested ? [tenant] : [],
      });
    if (path === "/api/v1/account/join-requests") {
      expect(JSON.parse(String(options.body))).toEqual({ slug: "my-team" });
      requested = true;
      return json(
        { message: "Your request is waiting for instance owner approval." },
        202,
      );
    }
    throw new Error(`Unexpected request ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={configuration} />);
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole("button", { name: "Join instance" }),
  );
  await user.type(
    screen.getByLabelText("Instance name or address"),
    "https://my-team.doodship.example.test",
  );
  await user.click(screen.getByRole("button", { name: "Request to join" }));
  await screen.findByText("Waiting for owner approval");
  expect(screen.queryByRole("link", { name: /Open instance/ })).toBeNull();
  expect(screen.queryByRole("button", { name: "Members" })).toBeNull();
});

it.each([
  "https://my-team.evil.test",
  "https://my-team.doodship.example.test.evil.test",
  "https://user@my-team.doodship.example.test",
  "https://my-team.doodship.example.test/path",
  "https://nested.my-team.doodship.example.test",
  "http://my-team.doodship.example.test",
])("rejects invalid instance address %s", async (address) => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  render(
    <JoinTenant
      origin={configuration.origin}
      onRequested={vi.fn()}
      onCancel={vi.fn()}
    />,
  );
  const input = screen.getByLabelText("Instance name or address");
  fireEvent.change(input, { target: { value: address } });
  fireEvent.submit(input.closest("form")!);
  await screen.findByRole("alert");
  expect(fetcher).not.toHaveBeenCalled();
});

it("keeps a failed join request open so the user can correct it", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(json({ detail: "Instance was not found." }, 404)),
  );
  const onRequested = vi.fn();
  render(
    <JoinTenant
      origin={configuration.origin}
      onRequested={onRequested}
      onCancel={vi.fn()}
    />,
  );
  const input = screen.getByLabelText("Instance name or address");
  fireEvent.change(input, { target: { value: "missing" } });
  fireEvent.submit(input.closest("form")!);
  await waitFor(() =>
    expect(screen.getByRole("alert").textContent).toContain(
      "Instance was not found.",
    ),
  );
  expect(onRequested).not.toHaveBeenCalled();
});
