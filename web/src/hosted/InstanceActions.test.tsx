// @vitest-environment jsdom
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { InstanceActionDialog, InstanceActionsMenu } from "./InstanceActions";
import { PlatformApp } from "./PlatformApp";
import type { Membership } from "./client";

const membership: Membership = {
  tenant: {
    id: "instance",
    slug: "example",
    name: "Example",
    state: "active",
    createdAt: "2026-10-13T00:00:00Z",
  },
  role: "owner",
  url: "https://example.doodship.test/api/v1/hosted/auth/start",
  connection: {
    state: "offline",
    remoteAccess: true,
    instanceUrl: "",
    backendMode: "connected",
  },
};
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/");
});

it("requires the exact instance name and keeps a failed deletion open for retry", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(json({ detail: "Try again" }, 503))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetcher);
  const onClose = vi.fn(),
    onCompleted = vi.fn().mockResolvedValue(undefined);
  const user = userEvent.setup();
  render(
    <InstanceActionDialog
      membership={membership}
      action="delete"
      onClose={onClose}
      onCompleted={onCompleted}
    />,
  );
  const dialog = screen.getByRole("dialog", { name: "Delete Example?" });
  expect(
    within(dialog).getByText(/Your server, data and running jobs stay intact/),
  ).not.toBeNull();
  const remove = within(dialog).getByRole("button", {
    name: "Delete instance",
  });
  expect((remove as HTMLButtonElement).disabled).toBe(true);
  const name = within(dialog).getByLabelText(/Type example/);
  await user.type(name, "Example");
  expect((remove as HTMLButtonElement).disabled).toBe(true);
  await user.clear(name);
  await user.type(name, "example");
  await user.click(remove);
  await screen.findByRole("alert");
  expect(onClose).not.toHaveBeenCalled();
  expect(onCompleted).not.toHaveBeenCalled();
  await user.click(remove);
  await waitFor(() => expect(onCompleted).toHaveBeenCalledOnce());
  expect(fetcher).toHaveBeenLastCalledWith(
    "/api/v1/tenants/instance",
    expect.objectContaining({
      method: "DELETE",
      body: JSON.stringify({ slug: "example" }),
    }),
  );
  expect(onClose).toHaveBeenCalledOnce();
});

it("cancels without sending a request", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  const onClose = vi.fn();
  render(
    <InstanceActionDialog
      membership={membership}
      action="disconnect"
      onClose={onClose}
      onCompleted={vi.fn()}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(onClose).toHaveBeenCalledOnce();
  expect(fetcher).not.toHaveBeenCalled();
});

it("keeps confirmation locked while deletion is in progress", async () => {
  let complete!: (response: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          complete = resolve;
        }),
    ),
  );
  const onClose = vi.fn();
  const user = userEvent.setup();
  render(
    <InstanceActionDialog
      membership={membership}
      action="delete"
      onClose={onClose}
      onCompleted={vi.fn()}
    />,
  );
  await user.type(screen.getByLabelText(/Type example/), "example");
  await user.click(screen.getByRole("button", { name: "Delete instance" }));
  expect(
    (screen.getByRole("button", { name: "Deleting..." }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  await user.keyboard("{Escape}");
  expect(onClose).not.toHaveBeenCalled();
  complete(new Response(null, { status: 204 }));
  await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
});

it.each(["admin", "member"] as const)(
  "does not expose destructive actions to an %s",
  (role) => {
    render(
      <InstanceActionsMenu
        membership={{ ...membership, role }}
        onSelect={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button")).toBeNull();
  },
);

it("does not offer deletion of a legacy embedded backend", () => {
  render(
    <InstanceActionsMenu
      membership={{
        ...membership,
        connection: { ...membership.connection!, backendMode: "embedded" },
      }}
      onSelect={vi.fn()}
    />,
  );
  expect(screen.queryByRole("button")).toBeNull();
});

it("offers removal of inactive records when the server allows it", async () => {
  const user = userEvent.setup();
  const onSelect = vi.fn();
  render(
    <InstanceActionsMenu
      membership={{ ...membership, connection: undefined, canDelete: true }}
      onSelect={onSelect}
    />,
  );
  await user.click(screen.getByRole("button", { name: "Actions for Example" }));
  expect(
    screen.queryByRole("menuitem", { name: "Disconnect server" }),
  ).toBeNull();
  await user.click(screen.getByRole("menuitem", { name: "Delete instance" }));
  expect(onSelect).toHaveBeenCalledWith("delete");
});

it("explains that stored data stays available for recovery", () => {
  render(
    <InstanceActionDialog
      membership={{ ...membership, connection: undefined, canDelete: true }}
      action="delete"
      onClose={vi.fn()}
      onCompleted={vi.fn()}
    />,
  );
  expect(screen.getByRole("dialog")).not.toBeNull();
  expect(screen.getByText(/stored data is kept for recovery/)).not.toBeNull();
});

it("lets owners disconnect and delete from the instance list", async () => {
  let state: Membership["connection"] = membership.connection;
  let deleted = false;
  const fetcher = vi.fn(async (path: string, init: RequestInit) => {
    if (path === "/api/v1/account")
      return json({
        id: "owner",
        email: "owner@example.test",
        name: "Owner",
        emailVerified: true,
        platformAdmin: false,
        state: "active",
      });
    if (path === "/api/v1/account/tenants")
      return json({
        memberships: deleted ? [] : [{ ...membership, connection: state }],
        invitations: [],
      });
    if (
      init.method === "DELETE" &&
      path === "/api/v1/tenants/instance/connection"
    ) {
      state = {
        ...membership.connection!,
        state: "not_connected",
        remoteAccess: false,
      };
      return new Response(null, { status: 204 });
    }
    if (init.method === "DELETE" && path === "/api/v1/tenants/instance") {
      deleted = true;
      return new Response(null, { status: 204 });
    }
    throw new Error(`Unexpected request ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  const user = userEvent.setup();
  render(
    <PlatformApp
      configuration={{
        mode: "platform",
        origin: "https://doodship.test",
        registrationEnabled: false,
      }}
    />,
  );
  await user.click(
    await screen.findByRole("button", { name: "Actions for Example" }),
  );
  await user.click(
    await screen.findByRole("menuitem", { name: "Disconnect server" }),
  );
  expect(fetcher.mock.calls.some(([, init]) => init.method === "DELETE")).toBe(
    false,
  );
  await user.click(screen.getByRole("button", { name: "Disconnect" }));
  await screen.findByText("Connect a server");
  await user.click(screen.getByRole("button", { name: "Actions for Example" }));
  expect(
    screen.queryByRole("menuitem", { name: "Disconnect server" }),
  ).toBeNull();
  await user.click(
    await screen.findByRole("menuitem", { name: "Delete instance" }),
  );
  await user.type(screen.getByLabelText(/Type example/), "example");
  await user.click(screen.getByRole("button", { name: "Delete instance" }));
  await screen.findByRole("heading", { name: "No instances yet" });
  expect(screen.queryByRole("dialog")).toBeNull();
});
