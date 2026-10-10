// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { CreateTenant } from "./CreateTenant";

const tenant = {
  id: "agentops",
  slug: "agentops",
  name: "AgentOps",
  state: "pending",
  createdAt: "2026-10-10T12:00:00Z",
};
const json = (body: unknown, status: number) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function show(tenants = [tenant]) {
  const onCreated = vi.fn(async () => {});
  render(
    <CreateTenant
      origin="https://dispatch.example.test"
      tenants={tenants}
      onCreated={onCreated}
      onCancel={vi.fn()}
    />,
  );
  return onCreated;
}

it.each([
  "",
  "-team",
  "team-",
  "team_name",
  "two words",
  "team.example",
  "1team",
  "ténant",
  "team/path",
  " team",
  "team ",
  "a".repeat(64),
  "www",
  "api",
  "ns1",
  "agentops",
])(
  "blocks invalid or unavailable name %j before sending a request",
  async (name) => {
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    const onCreated = show();
    const input = screen.getByLabelText("Tenant name");
    fireEvent.change(input, { target: { value: name } });
    fireEvent.submit(input.closest("form")!);
    expect((await screen.findAllByRole("alert")).length).toBeGreaterThan(0);
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(document.activeElement).toBe(input);
    expect(fetcher).not.toHaveBeenCalled();
    expect(onCreated).not.toHaveBeenCalled();
  },
);

it.each(["a", "team-01", "a".repeat(63)])(
  "creates valid tenant %j with one name and the configured address",
  async (name) => {
    const fetcher = vi.fn(async (_path: string, _options: RequestInit) =>
      json({ ...tenant, slug: name, name }, 202),
    );
    vi.stubGlobal("fetch", fetcher);
    const onCreated = show([]);
    const input = screen.getByLabelText("Tenant name");
    fireEvent.change(input, { target: { value: name.toUpperCase() } });
    fireEvent.change(screen.getByLabelText("Owner email"), {
      target: { value: "owner@example.test" },
    });
    expect(screen.getByRole("status").textContent).toBe(
      `${name}.dispatch.example.test`,
    );
    fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(onCreated).toHaveBeenCalledOnce());
    expect(JSON.parse(String(fetcher.mock.calls[0][1].body))).toEqual({
      name,
      slug: name,
      ownerEmail: "owner@example.test",
    });
  },
);

it("keeps the owner email and lets the admin choose another name after an API conflict", async () => {
  const user = userEvent.setup();
  const fetcher = vi.fn(async (_path: string, options: RequestInit) =>
    JSON.parse(String(options.body)).slug === "taken"
      ? json({ detail: "The request conflicts with the current state." }, 409)
      : json({ ...tenant, slug: "team-01", name: "team-01" }, 202),
  );
  vi.stubGlobal("fetch", fetcher);
  const onCreated = show([]);
  const input = screen.getByLabelText("Tenant name");
  await user.type(input, "taken");
  await user.type(screen.getByLabelText("Owner email"), "owner@example.test");
  await user.click(screen.getByRole("button", { name: "Create tenant" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "already uses this name",
  );
  expect(document.activeElement).toBe(input);
  expect((screen.getByLabelText("Owner email") as HTMLInputElement).value).toBe(
    "owner@example.test",
  );
  await user.clear(input);
  await user.type(input, "team-01");
  expect(screen.queryByRole("alert")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Create tenant" }));
  await waitFor(() => expect(onCreated).toHaveBeenCalledOnce());
  expect(fetcher).toHaveBeenCalledTimes(2);
});

it("keeps empty fields quiet and shows the address only after entering a valid name", async () => {
  const user = userEvent.setup();
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  show([]);
  const name = screen.getByLabelText("Tenant name");
  expect((name as HTMLInputElement).value).toBe("");
  expect(screen.queryByRole("status")).toBeNull();
  await user.tab();
  expect(screen.queryByRole("alert")).toBeNull();
  expect(name.getAttribute("aria-invalid")).toBe("false");
  await user.click(name);
  await user.type(name, "example-team");
  expect(screen.getByRole("status").textContent).toBe(
    "example-team.dispatch.example.test",
  );
  await user.clear(name);
  await user.tab();
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByRole("status")).toBeNull();
  expect(fetcher).not.toHaveBeenCalled();
});

it.each(["", "not-an-email"])(
  "blocks owner email %j and focuses it after submission",
  async (email) => {
    const user = userEvent.setup();
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    show([]);
    await user.type(screen.getByLabelText("Tenant name"), "example-team");
    const owner = screen.getByLabelText("Owner email");
    if (email) await user.type(owner, email);
    await user.click(screen.getByRole("button", { name: "Create tenant" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      email ? "Enter a valid email address." : "Enter an owner email.",
    );
    expect(document.activeElement).toBe(owner);
    expect(fetcher).not.toHaveBeenCalled();
  },
);
