// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AccountProfileDialog } from "./AccountProfileDialog";
import { api, type AccountProfile } from "./api";

const profile: AccountProfile = {
  user: {
    id: "user-1",
    username: "alex",
    displayName: "Alex Morgan",
    email: "alex@example.com",
    passwordConfigured: true,
    systemRole: "member",
    state: "active",
    createdAt: "2026-09-02T00:00:00Z",
    updatedAt: "2026-09-02T00:00:00Z",
  },
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
        lastLogin: "2026-09-02T00:00:00Z",
        createdAt: "2026-09-02T00:00:00Z",
      },
    },
    {
      available: true,
      provider: {
        id: "provider-2",
        name: "Public GitHub",
        type: "github",
        baseUrl: "https://github.com",
      },
    },
  ],
  teams: [{ id: "team-1", name: "Platform", role: "member" }],
  projectAccess: [
    {
      projectId: "project-1",
      projectName: "Checkout",
      role: "operator",
      source: "team",
      sourceName: "Platform",
    },
  ],
};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("account profile", () => {
  it("shows the signed-in user's identity, teams, and access", async () => {
    vi.spyOn(api, "accountProfile").mockResolvedValue(profile);
    render(<AccountProfileDialog onClose={vi.fn()} />);

    expect(await screen.findByText("Alex Morgan")).not.toBeNull();
    expect(screen.getByText("Company GitHub")).not.toBeNull();
    expect(screen.getByText("Platform")).not.toBeNull();
    expect(screen.getByText("Checkout")).not.toBeNull();
    expect(screen.getByText("Operator through Platform")).not.toBeNull();
    expect(
      screen.getByRole("button", { name: "Link Public GitHub" }),
    ).not.toBeNull();
  });

  it("removes a linked method without closing the profile", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "accountProfile").mockResolvedValue(profile);
    const unlink = vi.spyOn(api, "unlinkAuthProvider").mockResolvedValue();
    render(<AccountProfileDialog onClose={vi.fn()} />);

    await screen.findByText("Company GitHub");
    await user.click(screen.getByRole("button", { name: "Remove" }));

    expect(unlink).toHaveBeenCalledWith("provider-1");
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Link Company GitHub" }),
      ).not.toBeNull();
    });
  });

  it("uses the administrator profile endpoint and keeps identities read-only", async () => {
    vi.spyOn(api, "userProfile").mockResolvedValue(profile);
    render(<AccountProfileDialog userID="user-1" onClose={vi.fn()} />);

    expect(await screen.findByText("Company GitHub")).not.toBeNull();
    expect(api.userProfile).toHaveBeenCalledWith("user-1");
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
    expect(screen.getByText("Linked")).not.toBeNull();
  });

  it("keeps the impersonated user's credentials read-only", async () => {
    vi.spyOn(api, "accountProfile").mockResolvedValue(profile);
    render(
      <AccountProfileDialog
        readOnly
        onChangePassword={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    expect(await screen.findByText("Company GitHub")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Link Public GitHub" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Change password" }),
    ).toBeNull();
  });
});
