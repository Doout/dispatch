// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ServerForm } from "./Onboarding";
import { api } from "./api";

afterEach(cleanup);

describe("relay installation", () => {
  it("uses Docker Compose as a runtime choice for either install method", async () => {
    const user = userEvent.setup();
    render(<ServerForm onChanged={async () => undefined} />);

    await user.selectOptions(screen.getByRole("combobox", { name: /^Server type/ }), "relay");
    expect(screen.queryByRole("radio", { name: "Docker Compose" })).toBeNull();
    const docker = screen.getByRole("checkbox", { name: /Run with Docker Compose/ });
    await user.click(docker);

    expect(screen.getByRole("textbox", { name: /^Container image/ })).not.toBeNull();
    expect(screen.getByText(/DISPATCH_RELAY_INSTALL_MODE='docker'/)).not.toBeNull();

    await user.click(screen.getByRole("radio", { name: "Install over SSH" }));
    expect((docker as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("textbox", { name: /^Container image/ })).not.toBeNull();
  });

  it("uses a saved SSH key without exposing its value", async () => {
    const user = userEvent.setup();
    render(<ServerForm onChanged={async () => undefined} secrets={[{
      id: "secret-relay-key", name: "Relay operator key", type: "ssh_private_key", environmentVariable: "SSH_PRIVATE_KEY",
      publicValue: "ssh-ed25519 AAAA", createdAt: "2026-08-19T00:00:00Z", updatedAt: "2026-08-19T00:00:00Z",
    }]} />);

    await user.selectOptions(screen.getByRole("combobox", { name: /^Server type/ }), "relay");
    await user.click(screen.getByRole("radio", { name: "Install over SSH" }));

    expect((screen.getByRole("radio", { name: "Saved key" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("combobox", { name: /^SSH key/ }) as HTMLSelectElement).value).toBe("secret-relay-key");
    expect(screen.queryByRole("textbox", { name: /^Private key/ })).toBeNull();

    await user.click(screen.getByRole("radio", { name: "Paste key" }));
    expect(screen.getByRole("textbox", { name: /^Private key/ })).not.toBeNull();
  });
});

describe("Docker builder setup", () => {
  it("fills the pinned key after checking the SSH host", async () => {
    const scan = vi.spyOn(api, "scanBuilderSSHHost").mockResolvedValue({ fingerprint: "SHA256:verified", hostKey: "ssh-ed25519 AAAAhost" });
    try {
      const user = userEvent.setup();
      render(<ServerForm onChanged={async () => undefined} />);
      await user.selectOptions(screen.getByRole("combobox", { name: /^Server type/ }), "builder");
      await user.type(screen.getByRole("textbox", { name: "Docker SSH URL" }), "ssh://build@example.com");
      await user.click(screen.getByRole("button", { name: "Check host" }));
      expect(scan).toHaveBeenCalledWith("ssh://build@example.com");
      expect((screen.getByRole("textbox", { name: "SSH host public key" }) as HTMLTextAreaElement).value).toBe("ssh-ed25519 AAAAhost");
      expect(screen.getByText("SHA256:verified")).not.toBeNull();
    } finally {
      scan.mockRestore();
    }
  });

  it("asks for a saved key, pinned host key, and capacity", async () => {
    const user = userEvent.setup();
    render(<ServerForm onChanged={async () => undefined} secrets={[{
      id: "builder-key", name: "Builder key", type: "ssh_private_key", environmentVariable: "BUILDER_KEY",
      createdAt: "2026-08-19T00:00:00Z", updatedAt: "2026-08-19T00:00:00Z",
    }]} />);
    await user.selectOptions(screen.getByRole("combobox", { name: /^Server type/ }), "builder");
    expect(screen.getByRole("textbox", { name: /^Docker SSH URL/ })).not.toBeNull();
    expect(screen.getByRole("combobox", { name: "SSH key" })).not.toBeNull();
    expect(screen.getByRole("spinbutton", { name: "Concurrent jobs" })).not.toBeNull();
    expect(screen.getByRole("textbox", { name: /^SSH host public key/ })).not.toBeNull();
    expect(screen.queryByRole("textbox", { name: "Private key" })).toBeNull();
  });
});
