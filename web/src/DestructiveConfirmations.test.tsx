// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { DestructiveConfirmations } from "./DestructiveConfirmations";
import { destructiveRequest, setToken } from "./api";

const review = { resourceId: "application-id", resourceType: "application", name: "Orders", action: "delete", version: "current-version", summary: "Remove runtime and history. Database volumes remain.", resources: ["Docker container dispatch-application-id on Local target"] };
afterEach(() => { cleanup(); sessionStorage.clear(); vi.unstubAllGlobals(); });
function setup() {
  setToken("owner-token");
  const fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify(review), { status: 200 })).mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  render(<DestructiveConfirmations />);
  return fetch;
}
it("requires the exact name and sends the reviewed resource identity to the API", async () => {
  const fetch = setup();
  const removing = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE" });
  await screen.findByRole("dialog");
  expect(screen.getByText(review.summary)).toBeTruthy();
  expect(screen.getByText(review.resources[0])).toBeTruthy();
  expect(fetch).toHaveBeenCalledTimes(1);
  expect((screen.getByRole("button", { name: "Delete" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Wrong name" } });
  expect((screen.getByRole("button", { name: "Delete" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Orders" } });
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await removing;
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/apps/application-id/delete-preview");
  expect(JSON.parse(fetch.mock.calls[1][1].body).confirmation).toEqual({ resourceId: review.resourceId, action: "delete", expectedVersion: review.version, confirmName: "Orders" });
});
it("cancels without sending the destructive request", async () => {
  const fetch = setup();
  const cancelled = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE" }).catch(error => error);
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(await cancelled).toBeInstanceOf(Error);
  expect(fetch).toHaveBeenCalledTimes(1);
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});
it("rejects a confirmation if the signed-in actor changed", async () => {
  const fetch = setup();
  const changed = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE" }).catch(error => error);
  await screen.findByRole("dialog");
  setToken("different-user-token");
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Orders" } });
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  expect(((await changed) as Error).message).toContain("account changed");
  expect(fetch).toHaveBeenCalledTimes(1);
});
it("shows a stale-state rejection and never retries a delete automatically", async () => {
  const fetch = setup();
  fetch.mockReset().mockResolvedValueOnce(new Response(JSON.stringify(review), { status: 200 })).mockResolvedValueOnce(new Response(JSON.stringify({ detail: "Resource changed. Review again." }), { status: 409 }));
  const changed = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE" }).catch(error => error);
  await screen.findByRole("dialog");
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Orders" } });
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  expect(((await changed) as Error & { status: number }).status).toBe(409);
  expect(fetch).toHaveBeenCalledTimes(2);
});
it("lists references and blocks deletion when a resource is in use", async () => {
  const fetch = setup();
  fetch.mockReset().mockResolvedValueOnce(new Response(JSON.stringify({ ...review, resources: ["Application Orders production"], blockedReason: "Remove these references before deleting this resource." }), { status: 200 }));
  const cancelled = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE" }).catch(error => error);
  await screen.findByRole("alert");
  expect(screen.getByText("Application Orders production")).toBeTruthy();
  expect(screen.queryByRole("textbox")).toBeNull();
  expect((screen.getByRole("button", { name: "Delete" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await cancelled;
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("closes an aborted confirmation and allows the next action to be reviewed", async () => {
  const fetch = setup();
  const controller = new AbortController();
  const cancelled = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE", signal: controller.signal }).catch(error => error);
  await screen.findByRole("dialog");
  act(() => controller.abort());
  expect(await cancelled).toMatchObject({ name: "AbortError" });
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(1);
  fetch.mockReset().mockResolvedValueOnce(new Response(JSON.stringify(review), { status: 200 }));
  const next = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE" }).catch(error => error);
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await next;
});

it("never opens a confirmation when its preview completes after cancellation", async () => {
  const fetch = setup();
  let complete!: (response: Response) => void;
  fetch.mockReset().mockImplementation(() => new Promise<Response>(resolve => { complete = resolve; }));
  const controller = new AbortController();
  const cancelled = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE", signal: controller.signal }).catch(error => error);
  controller.abort();
  await act(async () => complete(new Response(JSON.stringify(review), { status: 200 })));
  expect(await cancelled).toMatchObject({ name: "AbortError" });
  expect(fetch.mock.calls[0][1].signal).toBe(controller.signal);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("checks cancellation again before sending a confirmed mutation", async () => {
  const fetch = setup();
  const controller = new AbortController();
  const cancelled = destructiveRequest("/api/v1/apps/application-id", { method: "DELETE", signal: controller.signal }).catch(error => error);
  await screen.findByRole("dialog");
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Orders" } });
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  controller.abort();
  expect(await cancelled).toMatchObject({ name: "AbortError" });
  expect(fetch).toHaveBeenCalledTimes(1);
});
