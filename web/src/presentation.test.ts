import { describe, expect, it } from "vitest";
import { Deployment, Overview } from "./api";
import { groupDeployments, relative, setupStage, short, stageIndex, statusTone } from "./presentation";

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

  it("unlocks inventory in server-first order", () => {
    const data: Overview = { demo: false, projects: [], servers: [], apps: [], deployments: [] };
    expect(setupStage(data)).toBe("server");
    data.servers.push({ id: "pending", name: "remote", address: "host", runtime: "docker", state: "pending", agentMode: "ssh-bootstrap", createdAt: "" });
    expect(setupStage(data)).toBe("server");
    data.servers[0].state = "ready";
    expect(setupStage(data)).toBe("project");
    data.projects.push({ id: "project", name: "Platform", description: "", createdAt: "" });
    expect(setupStage(data)).toBe("app");
    data.apps.push({ id: "app", projectId: "project", serverId: "pending", name: "api", sourceRepo: "https://example.test/api.git", branch: "main", buildType: "dockerfile", contextPath: ".", dockerfilePath: "Dockerfile", composePath: "compose.yml", containerPort: 8080, domain: "", state: "ready", createdAt: "" });
    expect(setupStage(data)).toBe("complete");
  });
});
