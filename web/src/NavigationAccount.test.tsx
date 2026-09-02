// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AccountMenu, ImpersonationBanner } from "./App";
import type { Overview } from "./api";

afterEach(cleanup);

function overview(identity: NonNullable<Overview["identity"]>): Overview {
  return {
    demo: false,
    secretStorageConfigured: true,
    identity,
    deployments: [],
    apps: [],
    eventTriggers: [],
    previews: [],
    previewGroups: [],
    previewGroupRuns: [],
    projects: [],
    servers: [],
    secrets: [],
    githubApps: [],
    relayWebhooks: [],
  };
}

describe("account menu", () => {
  it("logs out any signed-in user", async () => {
    const user = userEvent.setup();
    const onLogout = vi.fn();
    render(
      <AccountMenu
        identity={overview({
          id: "controller-owner",
          username: "owner",
          displayName: "Controller owner",
          systemRole: "owner",
          permissions: [],
        }).identity!}
        onOpenProfile={vi.fn()}
        onChangePassword={vi.fn()}
        onLogout={onLogout}
      />,
    );

    await user.click(
      screen.getByRole("button", {
        name: "Open account menu for Controller owner",
      }),
    );
    await user.click(screen.getByRole("menuitem", { name: "Log out" }));
    expect(onLogout).toHaveBeenCalledOnce();
  });

  it("shows initials and password changes for stored users", async () => {
    const user = userEvent.setup();
    render(
      <AccountMenu
        identity={overview({
          id: "user-1",
          username: "member",
          displayName: "Test User",
          systemRole: "member",
          permissions: [],
        }).identity!}
        onOpenProfile={vi.fn()}
        onChangePassword={vi.fn()}
        onLogout={vi.fn()}
      />,
    );

    expect(screen.getByText("TU")).not.toBeNull();
    await user.click(
      screen.getByRole("button", {
        name: "Open account menu for Test User",
      }),
    );
    expect(
      screen.getByRole("menuitem", { name: "My profile" }),
    ).not.toBeNull();
    expect(
      screen.getByRole("menuitem", { name: "Change password" }),
    ).not.toBeNull();
    expect(screen.getByRole("menuitem", { name: "Log out" })).not.toBeNull();
  });

  it("shows a persistent exit while viewing as another user", async () => {
    const user = userEvent.setup();
    const onExit = vi.fn();
    const identity = overview({
      id: "user-1",
      username: "alex",
      displayName: "Alex Morgan",
      systemRole: "member",
      permissions: [],
    }).identity!;
    const impersonator = overview({
      id: "owner-1",
      username: "admin",
      displayName: "Controller owner",
      systemRole: "owner",
      permissions: [],
    }).identity!;

    render(
      <ImpersonationBanner
        identity={identity}
        impersonator={impersonator}
        onExit={onExit}
      />,
    );

    expect(screen.getByText("Alex Morgan")).not.toBeNull();
    await user.click(
      screen.getByRole("button", { name: "Return to Controller owner" }),
    );
    expect(onExit).toHaveBeenCalledOnce();
  });

  it("hides password changes while impersonating", async () => {
    const user = userEvent.setup();
    render(
      <AccountMenu
        identity={overview({
          id: "user-1",
          username: "member",
          displayName: "Alex Morgan",
          systemRole: "member",
          permissions: [],
        }).identity!}
        impersonating
        onOpenProfile={vi.fn()}
        onChangePassword={vi.fn()}
        onLogout={vi.fn()}
      />,
    );

    await user.click(
      screen.getByRole("button", {
        name: "Open account menu for Alex Morgan",
      }),
    );
    expect(
      screen.queryByRole("menuitem", { name: "Change password" }),
    ).toBeNull();
  });
});
