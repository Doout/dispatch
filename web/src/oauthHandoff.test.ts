import { describe, expect, it } from "vitest";
import { hasOAuthHandoff } from "./oauthHandoff";

describe("OAuth handoff", () => {
  it("preserves a GitHub authorization code until the sign-in screen consumes it", () => {
    expect(hasOAuthHandoff(new URLSearchParams("auth_code=one-time-code"))).toBe(true);
  });

  it("preserves an OAuth error so the sign-in screen can display it", () => {
    expect(hasOAuthHandoff(new URLSearchParams("auth_error=Access+denied"))).toBe(true);
  });

  it("does not preserve unrelated route parameters", () => {
    expect(hasOAuthHandoff(new URLSearchParams("deployment=123"))).toBe(false);
  });
});
