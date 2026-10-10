// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { PlatformApp } from "./PlatformApp";
import type { HostedPlatform } from "./client";

const configuration: HostedPlatform = {
  mode: "platform",
  origin: "https://dispatch.example.test",
  registrationEnabled: false,
};
const account = {
  id: "platform-admin",
  name: "Platform admin",
  email: "admin@example.test",
  emailVerified: true,
  platformAdmin: true,
  state: "active",
};
const tenant = {
  id: "agentops",
  slug: "agentops",
  name: "AgentOps",
  state: "active",
  createdAt: "2026-10-09T12:00:00Z",
};
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/");
});

it("shows the public landing page without fetching tenant data", async () => {
  const fetcher = vi.fn(async (_path: string) =>
    json({ detail: "Sign in" }, 401),
  );
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={configuration} />);
  await screen.findByRole("heading", {
    name: "Deploy from Git. Follow each run.",
  });
  expect(
    screen.getByRole("link", { name: "Open console" }).getAttribute("href"),
  ).toBe("/?view=login");
  expect(
    screen
      .getByRole("link", { name: "Installation guide" })
      .getAttribute("href"),
  ).toContain("#install-and-upgrade");
  expect(screen.queryByLabelText("Password")).toBeNull();
  expect(fetcher.mock.calls).toHaveLength(1);
  expect(fetcher.mock.calls[0][0]).toBe("/api/v1/account");
});

it("keeps sign-in and logout usable when registration is disabled", async () => {
  window.history.replaceState({}, "", "/?view=login");
  const user = userEvent.setup();
  let authenticated = false;
  const fetcher = vi.fn(async (path: string, options: RequestInit) => {
    if (path === "/api/v1/account/login") {
      expect(JSON.parse(String(options.body))).toEqual({
        email: account.email,
        password: "account-password",
      });
      authenticated = true;
      return new Response(null, { status: 204 });
    }
    if (path === "/api/v1/account/logout") {
      authenticated = false;
      return new Response(null, { status: 204 });
    }
    if (path === "/api/v1/account")
      return authenticated ? json(account) : json({ detail: "Sign in" }, 401);
    if (path === "/api/v1/account/tenants")
      return json({ memberships: [], invitations: [] });
    throw new Error(`Unexpected request: ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={configuration} />);
  await screen.findByRole("heading", { name: "Sign in to Dispatch" });
  expect(
    screen.queryByRole("button", { name: "Create an account" }),
  ).toBeNull();
  await user.type(screen.getByLabelText("Email"), account.email);
  await user.type(screen.getByLabelText("Password"), "account-password");
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  await screen.findByRole("heading", { name: "My tenants" });
  await user.click(screen.getByRole("button", { name: "Log out" }));
  await screen.findByRole("heading", { name: "Sign in to Dispatch" });
  expect(window.location.search).toBe("?view=login");
});

it("opens sign-in directly for a tenant handoff instead of the landing page", async () => {
  window.history.replaceState({}, "", "/?tenant=agentops&challenge=challenge");
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => json({ detail: "Sign in" }, 401)),
  );
  render(<PlatformApp configuration={configuration} />);
  await screen.findByRole("heading", { name: "Sign in to Dispatch" });
  expect(screen.queryByRole("link", { name: "Open console" })).toBeNull();
  expect(new URLSearchParams(window.location.search).get("challenge")).toBe(
    "challenge",
  );
});

it("limits directory rows, filters by status and resets filters without extra API calls", async () => {
  window.history.replaceState({}, "", "/?view=catalog");
  const user = userEvent.setup();
  const tenants = Array.from({ length: 57 }, (_, index) => ({
    ...tenant,
    id: `tenant-${index}`,
    name: `AgentOps ${index}`,
    slug: `agentops-${index}`,
    state: index % 2 ? "pending" : "active",
  }));
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/account") return json(account);
    if (path === "/api/v1/account/tenants")
      return json({ memberships: [], invitations: [] });
    if (path === "/api/v1/platform/tenants") return json(tenants);
    throw new Error(`Unexpected request: ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={configuration} />);
  await screen.findByText("agentops-0.dispatch.example.test");
  expect(screen.getAllByRole("row")).toHaveLength(51);
  await user.click(screen.getByRole("button", { name: "Show more" }));
  expect(screen.getAllByRole("row")).toHaveLength(58);
  await user.selectOptions(screen.getByLabelText("Status"), "pending");
  expect(screen.getAllByRole("row")).toHaveLength(29);
  await user.type(
    screen.getByRole("searchbox", { name: "Find a tenant" }),
    "agentops-56",
  );
  expect(
    screen.getByRole("heading", { name: "No matching tenants" }),
  ).not.toBeNull();
  await user.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(screen.getAllByRole("row")).toHaveLength(51);
  await user.type(
    screen.getByRole("searchbox", { name: "Find a tenant" }),
    "agentops-56.dispatch.example.test",
  );
  expect(screen.getAllByRole("row")).toHaveLength(2);
  expect(
    fetcher.mock.calls.filter((call) => call[0] === "/api/v1/platform/tenants"),
  ).toHaveLength(1);
});

it("keeps a non-admin out of the directory even with its view in the URL", async () => {
  window.history.replaceState({}, "", "/?view=catalog");
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/account")
      return json({ ...account, platformAdmin: false });
    if (path === "/api/v1/account/tenants")
      return json({ memberships: [], invitations: [] });
    throw new Error(`Unexpected request: ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={configuration} />);
  await screen.findByRole("heading", { name: "My tenants" });
  expect(screen.queryByRole("button", { name: "Tenant directory" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Create tenant" })).toBeNull();
  expect(
    fetcher.mock.calls.some((call) => call[0].includes("/platform/")),
  ).toBe(false);
});

it("does not turn a directory error into an empty tenant list", async () => {
  window.history.replaceState({}, "", "/?view=catalog");
  const user = userEvent.setup();
  let attempts = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/account") return json(account);
      if (path === "/api/v1/account/tenants")
        return json({ memberships: [], invitations: [] });
      if (path === "/api/v1/platform/tenants")
        return ++attempts === 1
          ? json({ detail: "Catalog unavailable" }, 503)
          : json([]);
      throw new Error(`Unexpected request: ${path}`);
    }),
  );
  render(<PlatformApp configuration={configuration} />);
  await screen.findByRole("alert");
  expect(screen.queryByRole("heading", { name: "No tenants yet" })).toBeNull();
  expect(screen.queryByRole("definition")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByRole("heading", { name: "No tenants yet" });
  expect(attempts).toBe(2);
});

it("shows a membership error without claiming the account has no tenants", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/account") return json(account);
      if (path === "/api/v1/account/tenants")
        return json({ detail: "Tenant list unavailable" }, 503);
      throw new Error(`Unexpected request: ${path}`);
    }),
  );
  render(<PlatformApp configuration={configuration} />);
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Tenant list unavailable",
  );
  expect(screen.queryByRole("heading", { name: "No tenants yet" })).toBeNull();
});

it("previews the tenant address and submits the existing creation fields", async () => {
  window.history.replaceState({}, "", "/?view=catalog");
  const user = userEvent.setup();
  let created = false;
  const fetcher = vi.fn(async (path: string, options: RequestInit) => {
    if (path === "/api/v1/account") return json(account);
    if (path === "/api/v1/account/tenants")
      return json({ memberships: [], invitations: [] });
    if (path === "/api/v1/platform/tenants") {
      if (options.method === "POST") {
        created = true;
        return json(tenant, 202);
      }
      return json(created ? [tenant] : []);
    }
    throw new Error(`Unexpected request: ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={configuration} />);
  await screen.findByRole("heading", { name: "Tenant directory" });
  await user.click(screen.getByRole("button", { name: "Create tenant" }));
  await user.type(screen.getByLabelText("Tenant name"), "AgentOps");
  await user.type(screen.getByLabelText("Owner email"), "owner@example.test");
  expect(screen.getByText("agentops.dispatch.example.test")).not.toBeNull();
  await user.click(
    screen
      .getAllByRole("button", { name: "Create tenant" })
      .find((button) => !button.hasAttribute("disabled"))!,
  );
  await waitFor(() =>
    expect(
      fetcher.mock.calls.some(
        ([path, options]) =>
          path === "/api/v1/platform/tenants" && options.method === "POST",
      ),
    ).toBe(true),
  );
  const creation = fetcher.mock.calls.find(
    ([path, options]) =>
      path === "/api/v1/platform/tenants" && options.method === "POST",
  )!;
  expect(JSON.parse(String(creation[1].body))).toEqual({
    name: "agentops",
    slug: "agentops",
    ownerEmail: "owner@example.test",
  });
  await screen.findByText(
    "Created agentops.dispatch.example.test.",
  );
  expect(screen.queryByLabelText("Tenant name")).toBeNull();
  await screen.findByText("agentops.dispatch.example.test");
  expect((screen.getByRole("searchbox") as HTMLInputElement).value).toBe(
    "agentops.dispatch.example.test",
  );
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Create tenant" }),
  );
});

it("focuses the tenant name and returns to the directory when creation is cancelled", async () => {
  window.history.replaceState({}, "", "/?view=catalog");
  const user = userEvent.setup();
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/account") return json(account);
    if (path === "/api/v1/account/tenants")
      return json({ memberships: [], invitations: [] });
    if (path === "/api/v1/platform/tenants") return json([]);
    throw new Error(`Unexpected request: ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={configuration} />);
  await screen.findByRole("heading", { name: "Tenant directory" });
  const trigger = screen.getByRole("button", { name: "Create tenant" });
  await user.click(trigger);
  expect(document.activeElement).toBe(screen.getByLabelText("Tenant name"));
  await user.keyboard("{Escape}");
  expect(screen.queryByLabelText("Tenant name")).toBeNull();
  expect(document.activeElement).toBe(trigger);
  expect(fetcher.mock.calls).toHaveLength(3);
});
