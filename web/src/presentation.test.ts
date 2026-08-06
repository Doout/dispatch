import { describe, expect, it } from "vitest";
import { Deployment } from "./api";
import { groupDeployments, relative, short, stageIndex, statusTone } from "./presentation";

describe("deployment presentation", () => {
  it("maps runtime states onto the four visible handoffs", () => {
    expect(stageIndex("queued")).toBe(0);
    expect(stageIndex("building")).toBe(1);
    expect(stageIndex("checking")).toBe(2);
    expect(stageIndex("succeeded")).toBe(3);
  });

  it("keeps exception and success tones explicit", () => {
    expect(statusTone("failed")).toBe("danger");
    expect(statusTone("cancelled")).toBe("danger");
    expect(statusTone("succeeded")).toBe("success");
    expect(statusTone("routing")).toBe("active");
  });

  it("formats compact immutable evidence", () => {
    expect(short("0123456789abcdef")).toBe("01234567");
    expect(relative("2026-08-03T11:59:00Z", Date.parse("2026-08-03T12:00:00Z"))).toBe("1m ago");
  });

  it("keeps every deployment visible in exactly one board group", () => {
    const deployments = ["queued", "failed", "succeeded", "cancelled"].map((state, index) => ({
      id: String(index),
      appId: "app",
      commitSha: "abc123",
      specDigest: "sha256:abc",
      state,
      message: "test",
      createdAt: "2026-08-03T12:00:00Z",
    })) as Deployment[];

    const groups = groupDeployments(deployments);
    expect(groups.attention.map((item) => item.state)).toEqual(["queued", "failed"]);
    expect(groups.history.map((item) => item.state)).toEqual(["succeeded", "cancelled"]);
  });
});
