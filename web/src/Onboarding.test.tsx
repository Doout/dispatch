// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { ServerForm } from "./Onboarding";

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
