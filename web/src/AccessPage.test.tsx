// @vitest-environment jsdom

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AccessPage } from "./AccessPage";
import { api, type AccessOverview } from "./api";

const access: AccessOverview = {
  users: [
    { id: "owner-1", username: "admin", displayName: "Controller owner", email: "owner@example.com", passwordConfigured: true, systemRole: "owner", state: "active", createdAt: "2026-08-31T00:00:00Z", updatedAt: "2026-08-31T00:00:00Z" },
    { id: "user-1", username: "alex", displayName: "Alex Morgan", email: "alex@example.com", systemRole: "member", state: "active", createdAt: "2026-08-31T00:00:00Z", updatedAt: "2026-08-31T00:00:00Z" },
  ],
  teams: [{ id: "team-1", name: "Platform", description: "Platform delivery", createdAt: "2026-08-31T00:00:00Z", updatedAt: "2026-08-31T00:00:00Z" }],
  members: [{ teamId: "team-1", userId: "user-1", role: "member", createdAt: "2026-08-31T00:00:00Z" }],
  assignments: [{ id: "grant-1", principalType: "team", principalId: "team-1", scopeType: "project", scopeId: "project-1", role: "operator", createdAt: "2026-08-31T00:00:00Z", updatedAt: "2026-08-31T00:00:00Z" }],
  roles: [
    { id: "admin", name: "Project admin", permissions: ["project.view", "project.manage"] },
    { id: "operator", name: "Operator", permissions: ["project.view", "project.configure", "deployment.run"] },
    { id: "deployer", name: "Deployer", permissions: ["project.view", "deployment.run"] },
    { id: "viewer", name: "Viewer", permissions: ["project.view"] },
  ],
  projects: [{ id: "project-1", name: "Checkout", description: "Checkout services", createdAt: "2026-08-31T00:00:00Z" }],
  providers: [],
  identities: [],
};

afterEach(() => {
  cleanup();
  window.history.replaceState({}, "", "/access");
  vi.restoreAllMocks();
});

describe("access page", () => {
  it("offers distinct local, external, and approval workflows", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "access").mockResolvedValue(access);
    render(<AccessPage />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: "Manage users" }));

    expect(screen.getByRole("menuitem", { name: /Add local user/ })).not.toBeNull();
    expect(screen.getByRole("menuitem", { name: /Invite external user/ })).not.toBeNull();
    expect(screen.getByRole("menuitem", { name: /Review pending users/ })).not.toBeNull();

    await user.click(screen.getByRole("menuitem", { name: /Add local user/ }));
    expect(screen.getByRole("dialog", { name: "Add local user" })).not.toBeNull();
  });

  it("creates a provider-specific external sign-in link", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "access").mockResolvedValue({
      ...access,
      providers: [
        {
          id: "provider-1",
          name: "Company GitHub",
          type: "github",
          baseUrl: "https://github.example.com",
          apiUrl: "https://github.example.com/api/v3",
          clientId: "client-id",
          clientSecretConfigured: true,
          provisioning: "approval",
          state: "ready",
          createdAt: "2026-09-01T00:00:00Z",
          updatedAt: "2026-09-01T00:00:00Z",
        },
      ],
    });
    render(<AccessPage />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: "Manage users" }));
    await user.click(screen.getByRole("menuitem", { name: /Invite external user/ }));

    const dialog = screen.getByRole("dialog", { name: "Invite external user" });
    expect(within(dialog).getByText("Company GitHub")).not.toBeNull();
    expect(
      (within(dialog).getByLabelText("Sign-in link") as HTMLInputElement).value,
    ).toContain("authProvider=provider-1");
    expect(within(dialog).getByRole("button", { name: "Copy invite link" })).not.toBeNull();
  });

  it("shows controller roles, team membership, and direct access separately", async () => {
    vi.spyOn(api, "access").mockResolvedValue(access);
    render(<AccessPage />);

    expect(await screen.findByText("Controller owner")).not.toBeNull();
    const alex = screen.getByText("Alex Morgan").closest("tr");
    expect(alex).not.toBeNull();
    expect(within(alex as HTMLElement).getByText("Platform")).not.toBeNull();
    expect(within(alex as HTMLElement).getByText("Team grants only")).not.toBeNull();
  });

  it("edits a team with members and one role per project", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "access").mockResolvedValue(access);
    render(<AccessPage />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: /Teams/ }));
    await user.click(screen.getByRole("button", { name: "Edit Platform" }));

    const dialog = screen.getByRole("dialog", { name: "Edit team" });
    expect((within(dialog).getByRole("checkbox", { name: /Alex Morgan/ }) as HTMLInputElement).checked).toBe(true);
    expect((within(dialog).getByRole("combobox", { name: "Checkout role" }) as HTMLSelectElement).value).toBe("operator");
  });

  it("uses a product dialog before removing a user", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "access").mockResolvedValue(access);
    render(<AccessPage />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: "Remove Alex Morgan" }));

    expect(screen.getByRole("dialog", { name: "Remove user" })).not.toBeNull();
    expect(
      screen.getByText(/team memberships and project grants/),
    ).not.toBeNull();
  });

  it("merges a duplicate into the signed-in user", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "access").mockResolvedValue(access);
    const merge = vi
      .spyOn(api, "mergeUser")
      .mockResolvedValue(access.users[0]);
    render(<AccessPage identityID="owner-1" />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: "Merge Alex Morgan" }));

    const dialog = screen.getByRole("dialog", { name: "Merge user" });
    expect(
      (within(dialog).getByRole("combobox", {
        name: "User to keep",
      }) as HTMLSelectElement).value,
    ).toBe("owner-1");
    await user.click(within(dialog).getByRole("button", { name: "Merge users" }));

    expect(merge).toHaveBeenCalledWith("user-1", "owner-1");
  });

  it("does not offer a merge between two local users", async () => {
    vi.spyOn(api, "access").mockResolvedValue({
      ...access,
      users: access.users.map((user) => ({
        ...user,
        passwordConfigured: true,
      })),
    });
    render(<AccessPage identityID="owner-1" />);

    await screen.findByText("Controller owner");
    expect(
      screen.queryByRole("button", { name: "Merge Alex Morgan" }),
    ).toBeNull();
  });

  it("removes GitHub users from GitHub merge targets", async () => {
    const user = userEvent.setup();
    const githubAccess: AccessOverview = {
      ...access,
      users: [
        access.users[0],
        access.users[1],
        {
          id: "user-2",
          username: "sam",
          displayName: "Sam Rivera",
          systemRole: "member",
          state: "active",
          createdAt: "2026-08-31T00:00:00Z",
          updatedAt: "2026-08-31T00:00:00Z",
        },
      ],
      providers: [
        {
          id: "github-public",
          name: "GitHub.com",
          type: "github",
          baseUrl: "https://github.com",
          apiUrl: "https://api.github.com",
          clientId: "public-client",
          clientSecretConfigured: true,
          provisioning: "existing",
          state: "ready",
          createdAt: "2026-08-31T00:00:00Z",
          updatedAt: "2026-08-31T00:00:00Z",
        },
        {
          id: "github-enterprise",
          name: "Company GitHub",
          type: "github",
          baseUrl: "https://github.example.com",
          apiUrl: "https://github.example.com/api/v3",
          clientId: "enterprise-client",
          clientSecretConfigured: true,
          provisioning: "existing",
          state: "ready",
          createdAt: "2026-08-31T00:00:00Z",
          updatedAt: "2026-08-31T00:00:00Z",
        },
      ],
      identities: [
        {
          providerId: "github-public",
          subject: "42",
          userId: "user-1",
          login: "alex",
          lastLogin: "2026-09-01T00:00:00Z",
          createdAt: "2026-09-01T00:00:00Z",
        },
        {
          providerId: "github-enterprise",
          subject: "84",
          userId: "user-2",
          login: "sam",
          lastLogin: "2026-09-01T00:00:00Z",
          createdAt: "2026-09-01T00:00:00Z",
        },
      ],
    };
    vi.spyOn(api, "access").mockResolvedValue(githubAccess);
    render(<AccessPage identityID="owner-1" />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: "Merge Alex Morgan" }));

    const dialog = screen.getByRole("dialog", { name: "Merge user" });
    expect(within(dialog).getByRole("option", { name: /Controller owner/ })).not.toBeNull();
    expect(within(dialog).queryByRole("option", { name: /Sam Rivera/ })).toBeNull();
  });

  it("opens a user profile with every connected sign-in method", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "access").mockResolvedValue(access);
    vi.spyOn(api, "userProfile").mockResolvedValue({
      user: access.users[1],
      managed: false,
      links: [
        {
          available: true,
          provider: {
            id: "provider-1",
            name: "Company GitHub",
            type: "github",
            baseUrl: "https://github.example.com",
          },
          identity: {
            providerId: "provider-1",
            subject: "42",
            userId: "user-1",
            login: "alex",
            email: "alex@example.com",
            lastLogin: "2026-09-01T00:00:00Z",
            createdAt: "2026-09-01T00:00:00Z",
          },
        },
      ],
      teams: [{ id: "team-1", name: "Platform", role: "member" }],
      projectAccess: [],
    });
    render(<AccessPage identityID="owner-1" />);

    await screen.findByText("Controller owner");
    await user.click(
      screen.getByRole("button", { name: "View Alex Morgan profile" }),
    );

    const dialog = await screen.findByRole("dialog", { name: "Alex Morgan" });
    expect(within(dialog).getByText("Company GitHub")).not.toBeNull();
    expect(within(dialog).getByText("Platform")).not.toBeNull();
  });

  it("only offers password changes for the signed-in user", async () => {
    const user = userEvent.setup();
    const onChangePassword = vi.fn();
    vi.spyOn(api, "access").mockResolvedValue(access);
    render(<AccessPage identityID="owner-1" onChangePassword={onChangePassword} />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: "Edit Controller owner" }));
    expect(screen.queryByLabelText("New password")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Change password" }));
    expect(onChangePassword).toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Edit Alex Morgan" }));
    expect(screen.queryByRole("button", { name: "Change password" })).toBeNull();
  });

  it("lets the owner view Dispatch as another active user", async () => {
    const user = userEvent.setup();
    const onImpersonate = vi.fn();
    vi.spyOn(api, "access").mockResolvedValue(access);
    render(
      <AccessPage
        identityID="owner-1"
        onImpersonate={onImpersonate}
      />,
    );

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: "View as Alex Morgan" }));
    expect(onImpersonate).toHaveBeenCalledWith(access.users[1]);
    expect(
      screen.queryByRole("button", { name: "View as Controller owner" }),
    ).toBeNull();
  });

  it("keeps external accounts pending until an owner approves them", async () => {
    const user = userEvent.setup();
    const pending = {
      ...access,
      users: [
        ...access.users,
        {
          id: "pending-1",
          username: "sam",
          displayName: "Sam Rivera",
          email: "sam@example.com",
          systemRole: "member" as const,
          state: "pending" as const,
          createdAt: "2026-09-01T00:00:00Z",
          updatedAt: "2026-09-01T00:00:00Z",
        },
      ],
      providers: [
        {
          id: "provider-1",
          name: "Company GitHub",
          type: "github" as const,
          baseUrl: "https://github.example.com",
          apiUrl: "https://github.example.com/api/v3",
          clientId: "client-id",
          clientSecretConfigured: true,
          provisioning: "approval" as const,
          state: "ready" as const,
          createdAt: "2026-09-01T00:00:00Z",
          updatedAt: "2026-09-01T00:00:00Z",
        },
      ],
      identities: [
        {
          providerId: "provider-1",
          subject: "42",
          userId: "pending-1",
          login: "sam",
          email: "sam@example.com",
          lastLogin: "2026-09-01T00:00:00Z",
          createdAt: "2026-09-01T00:00:00Z",
        },
      ],
    };
    vi.spyOn(api, "access")
      .mockResolvedValueOnce(pending)
      .mockResolvedValue(access);
    const update = vi.spyOn(api, "updateUser").mockResolvedValue({
      ...pending.users[2],
      state: "active",
    });
    render(<AccessPage />);

    expect(await screen.findByText("Pending approval")).not.toBeNull();
    expect(screen.getByText("Review project access before approval.")).not.toBeNull();
    expect(screen.getByText(/via Company GitHub/)).not.toBeNull();
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(update).toHaveBeenCalledWith(
      "pending-1",
      expect.objectContaining({ state: "active", systemRole: "member" }),
    );
    await waitFor(() => {
      expect(screen.queryByText("Sam Rivera")).toBeNull();
    });
  });

  it("opens a focused pending-user review from the user menu", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "access").mockResolvedValue({
      ...access,
      users: [
        ...access.users,
        {
          id: "pending-1",
          username: "sam",
          displayName: "Sam Rivera",
          email: "sam@example.com",
          systemRole: "member",
          state: "pending",
          createdAt: "2026-09-01T00:00:00Z",
          updatedAt: "2026-09-01T00:00:00Z",
        },
      ],
    });
    render(<AccessPage />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: "Manage users" }));
    await user.click(screen.getByRole("menuitem", { name: /Review pending users/ }));

    expect(new URLSearchParams(window.location.search).get("accessReview")).toBe("pending");
    expect(screen.getByText("Sam Rivera")).not.toBeNull();
    expect(screen.queryByText("Alex Morgan")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Show all users" }));
    expect(screen.getByText("Alex Morgan")).not.toBeNull();
    expect(new URLSearchParams(window.location.search).has("accessReview")).toBe(false);
  });

  it("stores the selected access section in the URL", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "access").mockResolvedValue(access);
    render(<AccessPage />);

    await screen.findByText("Controller owner");
    await user.click(screen.getByRole("button", { name: /Teams/ }));
    expect(new URLSearchParams(window.location.search).get("accessSection")).toBe("teams");

    window.history.replaceState({}, "", "/access?accessSection=users");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(await screen.findByRole("button", { name: "Manage users" })).not.toBeNull();
  });
});
