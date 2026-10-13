// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { InstanceInstall, installationFiles } from "./InstanceInstall";

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
it("keeps secrets out of Compose and Docker settings and sends them through stdin", () => {
  const files = installationFiles(enrollment, "instance-id", true, "");
  expect(files.compose).not.toContain(enrollment.enrollmentToken);
  expect(files.compose).toContain("${HOME}/.doodship");
  const invocation = files.docker.split("<<'DOODSHIP_CONNECTION'")[0];
  expect(invocation).not.toContain(enrollment.enrollmentToken);
  expect(invocation).toContain("--rm -i");
  expect(invocation).toContain("--network host");
  expect(files.composeCommand).toContain("run --rm -T setup");
  const payload = JSON.parse(
    files.docker
      .split("<<'DOODSHIP_CONNECTION'\n")[1]
      .split("\nDOODSHIP_CONNECTION")[0],
  );
  expect(payload).toEqual({
    controlPlaneURL: enrollment.platformURL,
    enrollmentToken: enrollment.enrollmentToken,
    instanceID: "instance-id",
    directURL: "",
    remoteAccess: true,
  });
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
  await user.click(screen.getByRole("tab", { name: "Docker Compose" }));
  expect(
    screen.getByRole("button", { name: "Download Compose file" }),
  ).toBeTruthy();
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
