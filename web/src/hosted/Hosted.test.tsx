// @vitest-environment jsdom
import { StrictMode } from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { AuthScreen } from "../app/AuthScreen";
import { AccountMenu } from "../app/Navigation";
import { HostedGate } from "./HostedGate";
import { PlatformApp } from "./PlatformApp";
import { HostedTenantContext } from "./context";
import {
  detectHosted,
  tenantDestination,
  type HostedPlatform,
  type HostedTenant,
} from "./client";
import { UsagePanel } from "./UsagePanel";

const tenant = {
  id: "tenant-a",
  slug: "agentops",
  name: "AgentOps",
  state: "active",
  createdAt: "2026-10-09T12:00:00Z",
};
const platform: HostedPlatform = {
  mode: "platform",
  origin: "https://dispatch.example.test",
  registrationEnabled: true,
};
const hostedTenant: HostedTenant = {
  mode: "tenant",
  origin: "https://agentops.dispatch.example.test",
  loginUrl: platform.origin,
  tenant,
};
const account = {
  id: "platform",
  name: "Platform admin",
  email: "admin@example.test",
  emailVerified: true,
  platformAdmin: true,
  state: "active",
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
  sessionStorage.clear();
  window.history.replaceState({}, "", "/");
});

it("falls back only for a legacy endpoint and preserves errors", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(new Response("Not found", { status: 404 }))
    .mockResolvedValueOnce(
      new Response("<html>Dispatch</html>", {
        headers: { "Content-Type": "text/html" },
      }),
    )
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValueOnce(json({ error: "unavailable" }, 503));
  vi.stubGlobal("fetch", fetcher);
  expect(await detectHosted()).toBeNull();
  expect(await detectHosted()).toBeNull();
  await expect(detectHosted()).rejects.toThrow("offline");
  await expect(detectHosted()).rejects.toThrow("Unable to reach Dispatch");
});

it("does not mount legacy UI when hosted detection fails", async () => {
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
  render(
    <HostedGate>
      <p>Legacy application</p>
    </HostedGate>,
  );
  await screen.findByRole("alert");
  expect(screen.queryByText("Legacy application")).toBeNull();
});

it("clears legacy credentials and consumes a tenant callback once", async () => {
  sessionStorage.setItem("dispatch-admin-token", "old-token");
  sessionStorage.setItem("dispatch-impersonated-user", "other-user");
  window.history.replaceState({}, "", "/#tenant_code=one-use-code");
  const fetcher = vi.fn(async (path: string) =>
    path === "/api/v1/hosted"
      ? json(hostedTenant)
      : new Response(null, { status: 204 }),
  );
  vi.stubGlobal("fetch", fetcher);
  render(
    <StrictMode>
      <HostedGate>
        <p>Tenant application</p>
      </HostedGate>
    </StrictMode>,
  );
  await screen.findByText("Tenant application");
  expect(sessionStorage.getItem("dispatch-admin-token")).toBeNull();
  expect(sessionStorage.getItem("dispatch-impersonated-user")).toBeNull();
  expect(window.location.hash).toBe("");
  const exchanges = fetcher.mock.calls.filter(
    (call) => call[0] === "/api/v1/hosted/auth/exchange",
  );
  expect(exchanges).toHaveLength(1);
});

it("uses central sign-in for tenants without showing local login forms", () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  render(
    <HostedTenantContext.Provider value={hostedTenant}>
      <AuthScreen onAuthenticated={vi.fn()} />
    </HostedTenantContext.Provider>,
  );
  expect(
    screen.getByRole("link", { name: "Sign in" }).getAttribute("href"),
  ).toBe(`${hostedTenant.origin}/api/v1/hosted/auth/start`);
  expect(screen.queryByLabelText("Password")).toBeNull();
  expect(fetcher).not.toHaveBeenCalled();
});

it("offers central account links instead of local password changes", async () => {
  const user = userEvent.setup();
  render(
    <HostedTenantContext.Provider value={hostedTenant}>
      <AccountMenu
        identity={{
          id: "member",
          username: "member",
          displayName: "Member",
          systemRole: "owner",
          permissions: [],
        }}
        onOpenProfile={vi.fn()}
        onChangePassword={vi.fn()}
        onLogout={vi.fn()}
      />
    </HostedTenantContext.Provider>,
  );
  await user.click(
    screen.getByRole("button", { name: "Open account menu for Member" }),
  );
  expect(
    screen
      .getByRole("menuitem", { name: "Account settings" })
      .getAttribute("href"),
  ).toBe(`${platform.origin}/?view=account`);
  expect(
    screen.queryByRole("menuitem", { name: "Change password" }),
  ).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "My profile" })).toBeNull();
});

it("gives a platform administrator metadata and usage without tenant controls", async () => {
  const user = userEvent.setup();
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/account") return json(account);
    if (path === "/api/v1/account/tenants")
      return json({ memberships: [], invitations: [] });
    if (path === "/api/v1/platform/tenants") return json([tenant]);
    return json([]);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={platform} />);
  await screen.findByText("You are not a member of a tenant yet.");
  await user.click(screen.getByRole("button", { name: "Tenant directory" }));
  await screen.findByRole("button", { name: "View usage" });
  expect(screen.queryByRole("link", { name: "Open tenant" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Members" })).toBeNull();
  expect(
    screen.queryByRole("button", { name: /Delete|Suspend|Edit tenant/ }),
  ).toBeNull();
  await user.click(screen.getByRole("button", { name: "View usage" }));
  await screen.findByText("No usage has been recorded for this period.");
  expect(
    fetcher.mock.calls.every((call) => !call[0].includes("/overview")),
  ).toBe(true);
});

it("requires the email recipient to choose a password before verification", async () => {
  window.history.replaceState({}, "", "/#verify_email=recipient-token");
  const fetcher = vi.fn(async (path: string) =>
    path === "/api/v1/account"
      ? json({ detail: "Sign in" }, 401)
      : json({ memberships: [], invitations: [] }),
  );
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={platform} />);
  await screen.findByRole("heading", { name: "Finish creating your account" });
  expect(window.location.hash).toBe("");
  expect(
    fetcher.mock.calls.some((call) => call[0].includes("verify-email")),
  ).toBe(false);
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Recipient" },
  });
  fireEvent.change(screen.getByLabelText("Choose a password"), {
    target: { value: "recipient-password" },
  });
  fireEvent.change(screen.getByLabelText("Confirm password"), {
    target: { value: "different-password" },
  });
  fireEvent.submit(
    screen.getByRole("button", { name: "Create account" }).closest("form")!,
  );
  await screen.findByText("Passwords do not match.");
  expect(
    fetcher.mock.calls.some((call) => call[0].includes("verify-email")),
  ).toBe(false);
});

it("does not present missing usage as measured zero", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        json([
          {
            measured: ["builds"],
            builds: 0,
            periodStart: "2026-10-08",
            periodEnd: "2026-10-09",
          },
        ]),
      ),
  );
  render(<UsagePanel tenant={tenant} onClose={vi.fn()} />);
  await waitFor(() =>
    expect(screen.getAllByText("Not measured").length).toBe(8),
  );
  expect(screen.getByText("0")).not.toBeNull();
});

it("rejects handoff addresses outside an exact tenant host", () => {
  expect(
    tenantDestination(
      "https://agentops.dispatch.example.test/#tenant_code=one",
      platform.origin,
    ),
  ).toContain("agentops.dispatch.example.test");
  for (const url of [
    "https://dispatch.example.test.evil.test/",
    "https://deep.agentops.dispatch.example.test/",
    "http://agentops.dispatch.example.test/",
    "https://user:password@agentops.dispatch.example.test/",
  ])
    expect(() => tenantDestination(url, platform.origin)).toThrow();
});

it("retries tenant sign-in after a failed handoff", async () => {
  window.history.replaceState({}, "", "/?tenant=tenant-a&challenge=challenge");
  const user = userEvent.setup();
  let handoffs = 0;
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/account") return json(account);
    if (path === "/api/v1/account/tenants")
      return json({ memberships: [], invitations: [] });
    if (path === "/api/v1/account/handoffs") {
      handoffs++;
      if (handoffs === 1)
        return json({ detail: "Sign-in is temporarily unavailable." }, 503);
      // Hold the retried request before navigation so the browser stays here.
      return new Promise<Response>(() => {});
    }
    throw new Error(`Unexpected request: ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  render(<PlatformApp configuration={platform} />);
  await screen.findByText("Sign-in is temporarily unavailable.");
  expect(handoffs).toBe(1);
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(handoffs).toBe(2));
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByText("Opening your tenant...")).not.toBeNull();
});
