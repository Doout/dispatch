// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  api,
  getImpersonatedUserID,
  setImpersonatedUserID,
  setToken,
} from "./api";
import { servicesApi } from "./api/services";
import { backupsApi } from "./api/backups";
import { overviewApi } from "./api/overview";
import { getOverviewState, setOverviewState } from "./overviewState";
import { registerDestructiveConfirmation } from "./destructive";

afterEach(() => {
  sessionStorage.clear();
  setOverviewState(undefined, false);
  vi.unstubAllGlobals();
});

describe("API impersonation", () => {
  it("sends the selected user with authenticated requests", async () => {
    const fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      headers: new Headers({"X-Overview-Version":"v1"}),
      json: async () => ({}),
    });
    vi.stubGlobal("fetch", fetch);
    setToken("owner-token");
    setImpersonatedUserID("user-1");

    await api.overview();

    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/overview",
      expect.objectContaining({
        headers: expect.objectContaining({
          Authorization: "Bearer owner-token",
          "Impersonate-User": "user-1",
        }),
      }),
    );
  });

  it("clears impersonation when the authenticated session changes", () => {
    setToken("owner-token");
    setImpersonatedUserID("user-1");
    setToken("another-token");

    expect(getImpersonatedUserID()).toBe("");
  });
});

it.each([
  { detail: "deployment/slot2.yaml and deployment/slot3.yaml both define Application/slot2" },
  { lastError: "deployment/slot2.yaml and deployment/slot3.yaml both define Application/slot2" },
])("preserves configuration sync error details", async (body) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 422, json: async () => body }));
  await expect(api.syncConfigSource("slots")).rejects.toThrow("deployment/slot2.yaml and deployment/slot3.yaml both define Application/slot2");
});

it("shares the current session across domain clients and clears impersonation on account changes", async () => {
  const fetcher = vi.fn().mockImplementation(async () => new Response("[]", { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  setToken("owner-token");
  setImpersonatedUserID("member");
  await servicesApi.neonProviders("project / one");
  await api.deployment("release / one");
  for (const [, init] of fetcher.mock.calls) {
    expect(init.headers).toEqual(expect.objectContaining({
      Authorization: "Bearer owner-token",
      "Impersonate-User": "member",
    }));
  }
  expect(fetcher.mock.calls.map(call => call[0])).toEqual([
    "/api/v1/neon-providers?projectId=project%20%2F%20one",
    "/api/v1/deployments/release%20%2F%20one",
  ]);
  setToken("another-token");
  await backupsApi.workloadBackups();
  expect(fetcher.mock.calls[2][1].headers.Authorization).toBe("Bearer another-token");
  expect(fetcher.mock.calls[2][1].headers["Impersonate-User"]).toBeUndefined();
});

it("keeps one overview baseline and rejects an in-flight response from the previous viewed account", async () => {
  let finish!: (response: Response) => void;
  const fetcher = vi.fn().mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve; }));
  vi.stubGlobal("fetch", fetcher);
  setToken("owner-token");
  setImpersonatedUserID("member-one");
  const pending = overviewApi.overview();
  setImpersonatedUserID("member-two");
  finish(new Response('{"projects":[{"id":"old-account"}]}', { status: 200, headers: { "X-Overview-Version": "old-version" } }));
  await pending;
  expect(getOverviewState()).toBeUndefined();
  fetcher.mockResolvedValueOnce(new Response('{"projects":[{"id":"current-account"}]}', { status: 200, headers: { "X-Overview-Version": "current-version" } }));
  await api.overview();
  expect(getOverviewState()).toEqual({ version: "current-version", value: { projects: [{ id: "current-account" }] } });
});

it("uses the shared confirmation handler for a domain restore and rejects a changed actor", async () => {
  setToken("owner-token");
  const review = { resourceId: "backup", resourceType: "backup", name: "Database backup", action: "restore", version: "r4", summary: "Restore the database", resources: [] };
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify(review), { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  const unregister = registerDestructiveConfirmation(async value => {
    setImpersonatedUserID("different-member");
    return { resourceId: value.resourceId, action: value.action, expectedVersion: value.version, confirmName: value.name };
  });
  try {
    await expect(backupsApi.restoreWorkloadBackup("backup", "destination")).rejects.toThrow("account changed");
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/workload-backups/backup/restore/destination/preview");
    expect(fetcher.mock.calls[0][1].method).toBe("POST");
  } finally {
    unregister();
  }
});

it("preserves status and server details from a domain authentication failure", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response('{"detail":"Your session expired"}', { status: 401 })));
  await expect(servicesApi.services()).rejects.toMatchObject({ message: "Your session expired", status: 401 });
});
