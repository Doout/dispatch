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
import type { HostedPlatform } from "./client";
const configuration: HostedPlatform = {
  mode: "platform",
  origin: "https://doodship.com",
  authkitEnabled: true,
  registrationEnabled: false,
  passwordLoginEnabled: false,
  waitlistEnabled: true,
  workosLoginMethods: ["email-code", "password"],
};
function reply(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
function form(config = configuration) {
  render(<AccountForm configuration={config} mode="login" onDone={vi.fn()} />);
}
function email(value = "member@example.test") {
  fireEvent.change(screen.getByLabelText("Email"), { target: { value } });
}
beforeEach(() => {
  window.history.replaceState({}, "", "/?view=login");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("returns from hosted account setup without an error or automatic authentication", () => {
  window.history.replaceState({}, "", "/?view=login&workos_return=1");
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  form();
  expect(screen.getByRole("status").textContent).toBe(
    "Enter your email to finish signing in.",
  );
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByLabelText("Email")).not.toBeNull();
  expect(fetcher).not.toHaveBeenCalled();
  expect(window.location.search).toBe("?view=login");
});

it("shows a callback error once and removes it from the address", () => {
  window.history.replaceState(
    {},
    "",
    "/?view=login&auth_error=Expired&tenant=alpha",
  );
  render(
    <AccountForm
      configuration={configuration}
      mode="login"
      initialError="Expired"
      onDone={vi.fn()}
    />,
  );
  expect(screen.getByRole("alert").textContent).toBe("Expired");
  expect(window.location.search).toBe("?view=login&tenant=alpha");
  fireEvent.click(screen.getByRole("button", { name: "Join waitlist" }));
  expect(screen.queryByRole("alert")).toBeNull();
});

it("keeps retries inside the code form and uses the rotated state", async () => {
  const onDone = vi.fn();
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(
      reply({
        step: "email-code",
        state: "first",
        email: "member@example.test",
      }),
    )
    .mockResolvedValueOnce(
      reply({ step: "email-code", state: "second", message: "Try again." }),
    )
    .mockResolvedValueOnce(reply({ step: "complete", returnTo: "/" }));
  vi.stubGlobal("fetch", fetcher);
  render(
    <AccountForm configuration={configuration} mode="login" onDone={onDone} />,
  );
  email();
  fireEvent.click(screen.getByRole("button", { name: "Send sign-in code" }));
  fireEvent.change(await screen.findByLabelText("Email code"), {
    target: { value: "123456" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Verify code" }));
  expect((await screen.findByRole("alert")).textContent).toBe("Try again.");
  fireEvent.change(screen.getByLabelText("Email code"), {
    target: { value: "654321" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Verify code" }));
  await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
  expect(JSON.parse(fetcher.mock.calls[2][1].body)).toEqual({
    state: "second",
    code: "654321",
  });
});
it("handles authenticator enrollment followed by organization selection", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(
      reply({
        step: "totp",
        state: "mfa",
        secret: "SETUPKEY",
        email: "member@example.test",
      }),
    )
    .mockResolvedValueOnce(
      reply({
        step: "organization",
        state: "choose",
        organizations: [{ id: "org_a", name: "Team A" }],
      }),
    )
    .mockResolvedValueOnce(reply({ step: "complete", returnTo: "/" }));
  vi.stubGlobal("fetch", fetcher);
  form();
  email();
  fireEvent.click(screen.getByRole("button", { name: "Use a password" }));
  fireEvent.change(screen.getByLabelText("Password"), {
    target: { value: "managed-password" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  fireEvent.change(await screen.findByLabelText("Authenticator code"), {
    target: { value: "123456" },
  });
  expect(screen.getByText("SETUPKEY")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Verify code" }));
  fireEvent.click(await screen.findByRole("button", { name: "Team A" }));
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3));
  expect(JSON.parse(fetcher.mock.calls[2][1].body)).toEqual({
    state: "choose",
    organizationId: "org_a",
  });
});
it("uses an invitation's validated email and removes its token from the address bar", async () => {
  window.history.replaceState(
    {},
    "",
    "/?view=login&invitation_token=private-invite",
  );
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(reply({ email: "invited@example.test" }))
    .mockResolvedValueOnce(reply({ step: "email-code", state: "local" }));
  vi.stubGlobal("fetch", fetcher);
  form();
  await waitFor(() =>
    expect((screen.getByLabelText("Email") as HTMLInputElement).value).toBe(
      "invited@example.test",
    ),
  );
  expect(window.location.search).not.toContain("private-invite");
  expect((screen.getByLabelText("Email") as HTMLInputElement).readOnly).toBe(
    true,
  );
  expect(screen.queryByRole("button", { name: "Join waitlist" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Send sign-in code" }));
  await screen.findByLabelText("Email code");
  expect(JSON.parse(fetcher.mock.calls[1][1].body).invitationToken).toBe(
    "private-invite",
  );
});
it("does not let an invalid invitation fall through into login", async () => {
  window.history.replaceState({}, "", "/?view=login&invitation_token=expired");
  const fetcher = vi
    .fn()
    .mockResolvedValue(reply({ detail: "Invitation expired." }, 400));
  vi.stubGlobal("fetch", fetcher);
  form();
  await screen.findByRole("alert");
  expect(
    (
      screen.getByRole("button", {
        name: "Send sign-in code",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(fetcher).toHaveBeenCalledOnce();
});
it("uses WorkOS reset tokens, requires confirmation, and does not create a session", async () => {
  window.history.replaceState(
    {},
    "",
    "/?view=reset-password&token=private-reset",
  );
  const fetcher = vi
    .fn()
    .mockResolvedValue(reply({ message: "Password changed." }));
  vi.stubGlobal("fetch", fetcher);
  form();
  expect(window.location.search).not.toContain("private-reset");
  fireEvent.change(screen.getByLabelText("New password"), {
    target: { value: "new-password-value" },
  });
  fireEvent.change(screen.getByLabelText("Confirm password"), {
    target: { value: "different-password" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save password" }));
  expect((await screen.findByRole("alert")).textContent).toBe(
    "Passwords do not match.",
  );
  expect(fetcher).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Confirm password"), {
    target: { value: "new-password-value" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save password" }));
  await screen.findByRole("status");
  expect(fetcher.mock.calls[0][0]).toBe(
    "/api/v1/account/workos/password-reset/confirm",
  );
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
    token: "private-reset",
    password: "new-password-value",
  });
});
it("does not offer local password sign-in when WorkOS is enabled", () => {
  form({ ...configuration, passwordLoginEnabled: true });
  expect(screen.queryByLabelText("Password")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Use previous login" }),
  ).toBeNull();
  expect(
    screen.getByRole("button", { name: "Send sign-in code" }),
  ).toBeTruthy();
});
it("resumes an OAuth verification step once without exposing the provider token", async () => {
  window.history.replaceState({}, "", "/?view=login&managed_state=local-state");
  const fetcher = vi.fn().mockResolvedValue(
    reply({
      step: "verify-email",
      state: "rotated",
      email: "member@example.test",
    }),
  );
  vi.stubGlobal("fetch", fetcher);
  form();
  await screen.findByLabelText("Email code");
  expect(fetcher).toHaveBeenCalledOnce();
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
    state: "local-state",
    resume: true,
  });
  expect(window.location.search).not.toContain("local-state");
});

it("connects an existing account after email verification without sending another code", async () => {
  const onDone = vi.fn();
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(
      reply({
        step: "email-code",
        state: "code",
        email: "member@example.test",
      }),
    )
    .mockResolvedValueOnce(
      reply({
        step: "link-account",
        state: "link",
        email: "member@example.test",
      }),
    )
    .mockResolvedValueOnce(
      reply({
        step: "link-account",
        state: "retry",
        message: "Your previous password was not accepted. Try again.",
      }),
    )
    .mockResolvedValueOnce(reply({ step: "complete", returnTo: "/" }));
  vi.stubGlobal("fetch", fetcher);
  render(
    <AccountForm
      configuration={{ ...configuration, passwordLoginEnabled: true }}
      mode="login"
      onDone={onDone}
      initialError="Start sign-in again."
    />,
  );
  email();
  fireEvent.click(screen.getByRole("button", { name: "Send sign-in code" }));
  fireEvent.change(await screen.findByLabelText("Email code"), {
    target: { value: "123456" },
  });
  expect(screen.queryByText("Start sign-in again.")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Verify code" }));
  fireEvent.change(await screen.findByLabelText("Previous Doodship password"), {
    target: { value: "wrong-password" },
  });
  expect(screen.queryByLabelText("Email code")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Connect account" }));
  await screen.findByRole("alert");
  fireEvent.change(screen.getByLabelText("Previous Doodship password"), {
    target: { value: "previous-password" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Connect account" }));
  await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
  expect(JSON.parse(fetcher.mock.calls[3][1].body)).toEqual({
    state: "retry",
    previousPassword: "previous-password",
  });
});

it("returns to sign in when a code continuation has expired or been consumed", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(
      reply({
        step: "email-code",
        state: "used",
        email: "member@example.test",
      }),
    )
    .mockResolvedValueOnce(
      reply({ detail: "Sign-in expired. Start again." }, 401),
    )
    .mockResolvedValueOnce(reply({ step: "email-code", state: "fresh" }));
  vi.stubGlobal("fetch", fetcher);
  form();
  email();
  fireEvent.click(screen.getByRole("button", { name: "Send sign-in code" }));
  fireEvent.change(await screen.findByLabelText("Email code"), {
    target: { value: "123456" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Verify code" }));
  await screen.findByRole("alert");
  expect(screen.queryByLabelText("Email code")).toBeNull();
  expect((screen.getByLabelText("Email") as HTMLInputElement).value).toBe(
    "member@example.test",
  );
  fireEvent.click(screen.getByRole("button", { name: "Send sign-in code" }));
  await screen.findByLabelText("Email code");
  expect(JSON.parse(fetcher.mock.calls[2][1].body).state).toBeUndefined();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("directs production-data previews to production for account connection", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(reply({ step: "email-code", state: "first" }))
    .mockResolvedValueOnce(reply({ step: "link-account", state: "link" }));
  vi.stubGlobal("fetch", fetcher);
  form({ ...configuration, dataOrigin: "https://doodship.com" });
  email();
  fireEvent.click(screen.getByRole("button", { name: "Send sign-in code" }));
  fireEvent.change(await screen.findByLabelText("Email code"), {
    target: { value: "123456" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Verify code" }));
  const link = await screen.findByRole("link", {
    name: "Connect account on production",
  });
  expect(link.getAttribute("href")).toBe("https://doodship.com/?view=login");
  expect(screen.queryByLabelText("Previous Doodship password")).toBeNull();
});
