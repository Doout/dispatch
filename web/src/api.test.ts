// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  api,
  getImpersonatedUserID,
  setImpersonatedUserID,
  setToken,
} from "./api";

afterEach(() => {
  sessionStorage.clear();
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
