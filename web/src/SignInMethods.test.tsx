// @vitest-environment jsdom

import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { SignInMethods } from "./SignInMethods";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("sign-in methods", () => {
  it("creates a login App through GitHub", async () => {
    const user = userEvent.setup();
    const start = vi.spyOn(api, "startAuthProviderManifest").mockResolvedValue({
      action: "https://github.com/settings/apps/new?state=setup",
      manifest: { name: "GitHub.com", public: true },
    });
    const submit = vi
      .spyOn(HTMLFormElement.prototype, "submit")
      .mockImplementation(() => undefined);
    const changed = vi.fn();
    render(<SignInMethods providers={[]} onChanged={changed} />);

    await user.click(screen.getByRole("button", { name: "Add method" }));
    const dialog = screen.getByRole("dialog", { name: "Add sign-in method" });
    await user.selectOptions(
      within(dialog).getByLabelText("Registration owner"),
      "personal",
    );
    await user.click(
      within(dialog).getByRole("button", { name: "Continue to GitHub" }),
    );

    expect(start).toHaveBeenCalledWith(
      expect.objectContaining({
        baseUrl: "https://github.com",
        ownerType: "personal",
      }),
    );
    expect(submit).toHaveBeenCalled();
  });

  it("keeps manual OAuth credentials as a fallback", async () => {
    const user = userEvent.setup();
    const create = vi.spyOn(api, "createAuthProvider").mockResolvedValue({
      id: "provider-1",
    } as never);
    render(<SignInMethods providers={[]} onChanged={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Add method" }));
    const dialog = screen.getByRole("dialog", { name: "Add sign-in method" });
    await user.click(within(dialog).getByRole("button", { name: /Existing App/ }));
    expect(within(dialog).getByText(`${window.location.origin}/api/v1/auth/callback`)).not.toBeNull();
    expect((within(dialog).getByRole("link", { name: /Open GitHub/ }) as HTMLAnchorElement).href).toBe("https://github.com/settings/applications/new");
    await user.type(within(dialog).getByLabelText("Client ID"), "client-id");
    await user.type(within(dialog).getByLabelText("Client secret"), "client-secret");
    await user.click(within(dialog).getByRole("button", { name: "Save method" }));

    expect(create).toHaveBeenCalledWith(expect.objectContaining({ baseUrl: "https://github.com", clientId: "client-id", clientSecret: "client-secret" }));
  });

  it("uses one GitHub host selector for custom hosts", async () => {
    const user = userEvent.setup();
    const start = vi.spyOn(api, "startAuthProviderManifest").mockResolvedValue({
      action: "https://github.example.com/settings/apps/new?state=setup",
      manifest: { name: "GitHub", public: true },
    });
    vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(
      () => undefined,
    );
    render(<SignInMethods providers={[]} onChanged={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Add method" }));
    const dialog = screen.getByRole("dialog", { name: "Add sign-in method" });
    await user.click(within(dialog).getByRole("button", { name: "GitHub host" }));
    await user.click(screen.getByRole("menuitem", { name: "Custom host" }));
    await user.type(
      within(dialog).getByLabelText("Base URL"),
      "https://github.example.com",
    );
    await user.selectOptions(
      within(dialog).getByLabelText("Registration owner"),
      "personal",
    );
    await user.click(
      within(dialog).getByRole("button", { name: "Continue to GitHub" }),
    );

    expect(start).toHaveBeenCalledWith(
      expect.objectContaining({ baseUrl: "https://github.example.com" }),
    );
  });

  it("explains that new GitHub users require owner approval", async () => {
    const user = userEvent.setup();
    render(<SignInMethods providers={[]} onChanged={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Add method" }));
    const dialog = screen.getByRole("dialog", { name: "Add sign-in method" });
    await user.selectOptions(
      within(dialog).getByRole("combobox", { name: /New GitHub users/ }),
      "approval",
    );

    expect(
      within(dialog).getByText(
        "Dispatch creates a pending account. An owner must approve it before sign-in.",
      ),
    ).not.toBeNull();
    expect(within(dialog).queryByText(/Email domains/i)).toBeNull();
  });
});
