// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { ChangePasswordDialog } from "./ChangePasswordDialog";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("change password dialog", () => {
  it("requires matching new passwords", async () => {
    const user = userEvent.setup();
    const change = vi.spyOn(api, "changePassword").mockResolvedValue();
    render(<ChangePasswordDialog onClose={vi.fn()} />);

    await user.type(screen.getByLabelText("Current password"), "original-password-123");
    await user.type(screen.getByLabelText(/^New password/), "replacement-password-123");
    await user.type(screen.getByLabelText("Confirm new password"), "different-password-123");
    await user.click(screen.getByRole("button", { name: "Change password" }));

    expect(screen.getByRole("alert").textContent).toBe("Passwords do not match.");
    expect(change).not.toHaveBeenCalled();
  });

  it("submits the current and new password", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const change = vi.spyOn(api, "changePassword").mockResolvedValue();
    render(<ChangePasswordDialog onClose={onClose} />);

    await user.type(screen.getByLabelText("Current password"), "original-password-123");
    await user.type(screen.getByLabelText(/^New password/), "replacement-password-123");
    await user.type(screen.getByLabelText("Confirm new password"), "replacement-password-123");
    await user.click(screen.getByRole("button", { name: "Change password" }));

    expect(change).toHaveBeenCalledWith("original-password-123", "replacement-password-123");
    expect(onClose).toHaveBeenCalled();
  });
});
