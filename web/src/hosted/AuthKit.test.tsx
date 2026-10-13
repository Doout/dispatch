// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AccountForm } from "./AccountForm";
import { AccountSettings } from "./AccountSettings";
import { type HostedPlatform } from "./client";

const configuration: HostedPlatform = {
  mode: "platform",
  origin: "https://dispatch.example.test",
  registrationEnabled: false,
  authkitEnabled: true,
  passwordLoginEnabled: false,
};
it("collects the waitlist email inside Doodship without offering public registration", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify({ message: "Request received." }), {
        status: 202,
        headers: { "Content-Type": "application/json" },
      }),
    );
  vi.stubGlobal("fetch", fetcher);
  render(
    <AccountForm
      configuration={{ ...configuration, waitlistEnabled: true }}
      mode="login"
      onDone={vi.fn()}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Join waitlist" }));
  fireEvent.change(screen.getByLabelText("Email"), {
    target: { value: "member@example.test" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Request invitation" }));
  await waitFor(() => expect(fetcher).toHaveBeenCalledOnce());
  expect(fetcher.mock.calls[0][0]).toBe("/api/v1/account/workos/waitlist");
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
    email: "member@example.test",
  });
  expect(
    screen.queryByRole("button", { name: "Create an account" }),
  ).toBeNull();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
beforeEach(() => {
  window.history.replaceState({}, "", "/");
  vi.restoreAllMocks();
});
it("uses WorkOS email codes inside Doodship and preserves instance handoff inputs", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValue(
      new Response(
        JSON.stringify({
          step: "email-code",
          state: "local-state",
          email: "member@example.test",
        }),
        { headers: { "Content-Type": "application/json" } },
      ),
    );
  vi.stubGlobal("fetch", fetcher);
  window.history.replaceState(
    {},
    "",
    "/?tenant=tenant-1&challenge=challenge-1",
  );
  render(
    <AccountForm configuration={configuration} mode="login" onDone={vi.fn()} />,
  );
  expect(screen.queryByLabelText("Password")).toBeNull();
  fireEvent.change(screen.getByLabelText("Email"), {
    target: { value: "member@example.test" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Continue with email" }));
  await screen.findByLabelText("Email code");
  expect(fetcher.mock.calls[0][0]).toBe("/api/v1/account/workos/authenticate");
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
    method: "email-code",
    email: "member@example.test",
    tenantId: "tenant-1",
    codeChallenge: "challenge-1",
  });
});
it("offers an explicit connection for an existing account", async () => {
  const fetch = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ detail: "Try again later." }), {
      status: 503,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  render(
    <AccountSettings
      configuration={configuration}
      account={{
        id: "owner",
        name: "Owner",
        email: "owner@example.test",
        emailVerified: true,
        platformAdmin: true,
        state: "active",
      }}
      onPasswordChanged={vi.fn()}
    />,
  );
  expect(screen.queryByLabelText("Current password")).toBeNull();
  expect(
    screen.getByRole("link", { name: "Connect WorkOS" }).getAttribute("href"),
  ).toBe("/?view=login&link=workos");
  expect(fetch).not.toHaveBeenCalled();
});

it("lets an instance owner approve a provider user without a WorkOS account", async () => {
  const fetcher = vi.fn(async (path: string) => {
    const data = path.endsWith("/join-requests")
      ? [
          {
            id: "external",
            name: "GitHub member",
            email: "external@example.test",
          },
        ]
      : [];
    return new Response(JSON.stringify(data), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetcher);
  const { TenantMembers } = await import("./TenantMembers");
  render(
    <TenantMembers
      membership={{
        tenant: {
          id: "team",
          slug: "team",
          name: "Team",
          state: "active",
          createdAt: "",
        },
        role: "owner",
        url: "",
      }}
      account={{
        id: "owner",
        name: "Owner",
        email: "owner@example.test",
        emailVerified: true,
        platformAdmin: false,
        state: "active",
      }}
      onClose={vi.fn()}
      onChanged={vi.fn().mockResolvedValue(undefined)}
    />,
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Approve GitHub member" }),
  );
  await waitFor(() =>
    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/tenants/team/members",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          email: "external@example.test",
          role: "member",
        }),
      }),
    ),
  );
});
