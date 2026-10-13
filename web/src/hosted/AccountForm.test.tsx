// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { AccountForm } from "./AccountForm";
import type { HostedPlatform } from "./client";

const configuration: HostedPlatform = {
  mode: "platform",
  origin: "https://doodship.com",
  registrationEnabled: false,
  passwordLoginEnabled: true,
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("reveals and hides the password without submitting or changing the login value", async () => {
  const user = userEvent.setup();
  const fetcher = vi
    .fn()
    .mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetcher);
  const onDone = vi.fn();
  render(
    <AccountForm configuration={configuration} mode="login" onDone={onDone} />,
  );
  await user.type(screen.getByLabelText("Email"), "member@example.test");
  const password = screen.getByLabelText("Password");
  await user.type(password, "original-password");
  await user.click(screen.getByRole("button", { name: "Show password" }));
  expect(password.getAttribute("type")).toBe("text");
  expect(fetcher).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Hide password" }));
  expect(password.getAttribute("type")).toBe("password");
  await user.click(
    screen.getByRole("button", { name: "Sign in" }),
  );
  await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
    email: "member@example.test",
    password: "original-password",
  });
});

it("keeps password confirmation required when finishing an account", async () => {
  const user = userEvent.setup();
  const fetcher = vi
    .fn()
    .mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetcher);
  render(
    <AccountForm
      configuration={configuration}
      mode="verify"
      verification="verification-token"
      onDone={vi.fn()}
    />,
  );
  await user.type(screen.getByLabelText("Name"), "Member");
  await user.type(
    screen.getByLabelText("Choose a password"),
    "correct-password",
  );
  await user.type(
    screen.getByLabelText("Confirm password"),
    "different-password",
  );
  await user.click(
    screen.getByRole("button", { name: "Create account" }),
  );
  expect((await screen.findByRole("alert")).textContent).toBe(
    "Passwords do not match.",
  );
  expect(fetcher).not.toHaveBeenCalled();
  await user.clear(screen.getByLabelText("Confirm password"));
  await user.type(
    screen.getByLabelText("Confirm password"),
    "correct-password",
  );
  await user.click(
    screen.getByRole("button", { name: "Create account" }),
  );
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
    token: "verification-token",
    name: "Member",
    password: "correct-password",
  });
});
