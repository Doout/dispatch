// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AccountMenu, ImpersonationBanner, Nav } from "./App";
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
    expect(screen.getByRole("button", { name: "Open account menu for Test User" }).textContent).toBe("TU");
    expect(screen.queryByText("Test User")).toBeNull();
    await user.click(
      screen.getByRole("button", {
        name: "Open account menu for Test User",
      }),
    );
    expect(screen.getByText("Test User")).not.toBeNull();
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

describe("optional Operations navigation", () => {
  it("hides Operations until enabled and limits Settings to controller owners", () => {
    const owner = overview({ id: "owner", username: "owner", displayName: "Owner", systemRole: "owner", permissions: [] });
    const props = { open: false, view: "deployments" as const, onClose: vi.fn(), onNavigate: vi.fn() };
    const view = render(<Nav {...props} overview={owner} />);
    expect(screen.queryByRole("link", { name: "Operations" })).toBeNull();
    expect(screen.getByRole("link", { name: "Settings" }).getAttribute("href")).toBe("/settings");
    view.rerender(<Nav {...props} overview={{ ...owner, controllerSettings: { operationsEnabled: true } }} />);
    expect(screen.getByRole("link", { name: "Operations" }).getAttribute("href")).toBe("/operations");
    view.rerender(<Nav {...props} overview={{ ...owner, identity: { ...owner.identity!, systemRole: "member" }, controllerSettings: { operationsEnabled: true } }} />);
    expect(screen.queryByRole("link", { name: "Settings" })).toBeNull();
    expect(screen.getByRole("link", { name: "Operations" })).toBeTruthy();
    view.rerender(<Nav {...props} overview={{ ...owner, controllerSettings: { operationsEnabled: false } }} />);
    expect(screen.queryByRole("link", { name: "Operations" })).toBeNull();
  });
});

describe("configuration navigation", () => {
  it("keeps resource and controller destinations visible when switching pages", async () => {
    const user = userEvent.setup();
    const owner = overview({ id: "owner", username: "owner", displayName: "Owner", systemRole: "owner", permissions: [] });
    const props = { open: false, onClose: vi.fn(), onNavigate: vi.fn() };
    const view = render(<Nav {...props} view="projects" overview={owner} />);

    const resources = screen.getByRole("region", { name: "Resources" });
    const controller = screen.getByRole("region", { name: "Controller" });
    expect(within(resources).getAllByRole("link").map(link => link.textContent)).toEqual(["Projects", "Services", "Servers"]);
    expect(within(controller).getAllByRole("link").map(link => link.textContent)).toEqual(["Variables", "Connections", "Access", "Settings"]);
    expect(screen.getByRole("link", { name: "Projects" }).getAttribute("aria-current")).toBe("page");
    await user.click(screen.getByRole("link", { name: "Settings" }));
    expect(props.onNavigate).toHaveBeenCalledWith("settings");

    view.rerender(<Nav {...props} view="settings" overview={owner} />);
    expect(screen.getByRole("link", { name: "Settings" }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("link", { name: "Projects" }).getAttribute("aria-current")).toBeNull();
    expect(within(resources).getAllByRole("link")).toHaveLength(3);
    expect(within(controller).getAllByRole("link")).toHaveLength(4);
  });

  it("hides controller destinations and their heading from members", () => {
    const member = overview({ id: "member", username: "member", displayName: "Member", systemRole: "member", permissions: [] });
    render(<Nav open={false} view="projects" overview={member} onClose={vi.fn()} onNavigate={vi.fn()} />);

    expect(screen.getByRole("region", { name: "Resources" })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Controller" })).toBeNull();
    for (const name of ["Variables", "Connections", "Access", "Settings"])
      expect(screen.queryByRole("link", { name })).toBeNull();
  });

  it("keeps counts out of link names and preserves modified link navigation", () => {
    const owner = overview({ id: "owner", username: "owner", displayName: "Owner", systemRole: "owner", permissions: [] });
    owner.projects = [{ id: "project", name: "Project", description: "", createdAt: "" }];
    const onNavigate = vi.fn();
    render(<Nav open={false} view="projects" overview={owner} onClose={vi.fn()} onNavigate={onNavigate} />);

    const projects = screen.getByRole("link", { name: "Projects" });
    expect(projects.getAttribute("href")).toBe("/projects");
    expect(within(projects).getByText("1").getAttribute("aria-hidden")).toBe("true");
    let preventedByNavigation: boolean | undefined;
    document.addEventListener("click", event => {
      preventedByNavigation = event.defaultPrevented;
      event.preventDefault();
    }, { once: true });
    const click = new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true });
    fireEvent(projects, click);
    expect(preventedByNavigation).toBe(false);
    expect(onNavigate).not.toHaveBeenCalled();
  });
});
