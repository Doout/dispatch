// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { InstanceInstall, installCommand } from "./InstanceInstall";

const enrollment = {
  enrollmentToken: "one-use-secret",
  platformURL: "https://doodship.test",
  subject: "owner",
  expiresAt: "2099-01-01T00:00:00Z",
};
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
it("fetches the installer without sending secrets in its URL or arguments", () => {
  const command = installCommand(enrollment, "instance-id", true, "");
  const invocation = command.split("<<'DOODSHIP_CONNECTION'")[0];
  expect(invocation).not.toContain(enrollment.enrollmentToken);
  expect(invocation).toContain("https://doodship.test/install.sh");
  expect(invocation).toContain('&& sh -c "$installer"');
  const payload = JSON.parse(
    command
      .split("<<'DOODSHIP_CONNECTION'\n")[1]
      .split("\nDOODSHIP_CONNECTION")[0],
  );
  expect(payload).toEqual({
    controlPlaneURL: enrollment.platformURL,
    enrollmentToken: enrollment.enrollmentToken,
    instanceID: "instance-id",
    remoteAccess: true,
  });
  expect(
    installCommand(enrollment, "instance-id", false, " https://internal.test "),
  ).toContain('"directURL":"https://internal.test"');
});

it("makes remote access explicit and requires a direct address when it is disabled", async () => {
  const user = userEvent.setup();
  render(
    <InstanceInstall
      enrollment={enrollment}
      instanceID="instance-id"
      onError={vi.fn()}
    />,
  );
  await user.click(screen.getByRole("checkbox"));
  expect(
    (
      screen.getByRole("button", {
        name: "Copy install command",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  await user.type(
    screen.getByRole("textbox", { name: /Instance address/ }),
    "https://internal.example.test",
  );
  expect(
    (
      screen.getByRole("button", {
        name: "Copy install command",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(false);
  expect(screen.queryByRole("button", { name: /Download/ })).toBeNull();
  expect(screen.getByRole("tab", { name: "New installation" })).toBeTruthy();
  await user.click(screen.getByRole("tab", { name: "Existing installation" }));
  expect(
    (screen.getByLabelText("Connection token") as HTMLInputElement).type,
  ).toBe("password");
});
it("does not copy commands with an expired token", () => {
  render(
    <InstanceInstall
      enrollment={{ ...enrollment, expiresAt: "2000-01-01T00:00:00Z" }}
      instanceID="instance-id"
      onError={vi.fn()}
    />,
  );
  expect(screen.getByRole("alert").textContent).toMatch(/expired/);
  expect(
    (
      screen.getByRole("button", {
        name: "Copy install command",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
