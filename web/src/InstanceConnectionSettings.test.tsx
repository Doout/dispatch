// @vitest-environment jsdom
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import * as client from "./api";
import { InstanceConnectionSettings } from "./InstanceConnectionSettings";

const empty = {
  configured: false,
  connected: false,
  controlPlaneURL: "",
  instanceID: "",
  installationID: "install",
  directURL: "",
  remoteAccess: false,
  userMappings: [],
};
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

it("updates live connection state without replacing unsaved owner settings", async () => {
  vi.useFakeTimers();
  let connected = false;
  vi.spyOn(client, "request").mockImplementation(
    async <T,>() =>
      ({
        ...empty,
        configured: true,
        connected,
        controlPlaneURL: "https://doodship.test",
        directURL: "https://original.example.test",
      }) as T,
  );
  vi.spyOn(client.api, "access").mockResolvedValue({
    users: [],
  } as unknown as Awaited<ReturnType<typeof client.api.access>>);
  render(<InstanceConnectionSettings />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Manage connection" }));
  });
  expect(screen.getByText("Waiting for connection")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Instance address"), {
    target: { value: "https://edited.example.test" },
  });
  connected = true;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
  expect(screen.getByText("Connected")).toBeTruthy();
  expect(
    (screen.getByLabelText("Instance address") as HTMLInputElement).value,
  ).toBe("https://edited.example.test");
});

it("requires owner interaction before loading the connection or local users", async () => {
  const request = vi.spyOn(client, "request").mockResolvedValue(empty);
  const access = vi
    .spyOn(client.api, "access")
    .mockResolvedValue({ users: [] } as unknown as Awaited<
      ReturnType<typeof client.api.access>
    >);
  render(<InstanceConnectionSettings />);
  expect(request).not.toHaveBeenCalled();
  expect(access).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Manage connection" }));
  await screen.findByLabelText("Connection token");
  expect(
    (screen.getByLabelText("Allow remote user access") as HTMLInputElement)
      .checked,
  ).toBe(false);
});

it("saves only explicit mappings and clears the enrollment token after connection", async () => {
  const request = vi
    .spyOn(client, "request")
    .mockImplementation(async <T,>(_path: string, options?: RequestInit) =>
      options?.method === "PUT"
        ? ({
            ...empty,
            configured: true,
            controlPlaneURL: "https://doodship.test",
            userMappings: [{ subject: "cp-account", userID: "local-user" }],
          } as T)
        : (empty as T),
    );
  vi.spyOn(client.api, "access").mockResolvedValue({
    users: [
      {
        id: "local-user",
        username: "owner",
        displayName: "Local owner",
        state: "active",
      },
    ],
  } as unknown as Awaited<ReturnType<typeof client.api.access>>);
  const user = userEvent.setup();
  render(<InstanceConnectionSettings />);
  await user.click(screen.getByRole("button", { name: "Manage connection" }));
  await screen.findByLabelText("Connection token");
  await user.type(
    screen.getByLabelText("Control plane address"),
    "https://doodship.test",
  );
  await user.type(
    screen.getByLabelText("Connection token"),
    "single-use-secret",
  );
  await user.click(screen.getByRole("button", { name: "Link a user" }));
  await user.type(screen.getByLabelText("Doodship account ID 1"), "cp-account");
  await user.selectOptions(screen.getByLabelText("Local user 1"), "local-user");
  await user.click(
    screen.getByRole("button", { name: "Connect installation" }),
  );
  await screen.findByText(/Connection saved/);
  expect(screen.queryByLabelText("Connection token")).toBeNull();
  const write = request.mock.calls.find(
    ([, options]) => options?.method === "PUT",
  )!;
  expect(JSON.parse(String(write[1]?.body))).toEqual({
    controlPlaneURL: "https://doodship.test",
    enrollmentToken: "single-use-secret",
    directURL: "",
    remoteAccess: false,
    userMappings: [{ subject: "cp-account", userID: "local-user" }],
  });
});
