// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { shortenTopologyLabel, topologyDisplayLabel, TopologyCanvas } from "./TopologyCanvas";

describe("topology labels", () => {
  it("shows a deployment name instead of generated pod hashes", () => {
    const label = "inventory-api-development-77669dcd4f-xlh6p";
    const deployment = { id: "deployment:inventory", column: "workloads", kind: "deployment", label: "inventory-api-development" };
    const pod = { id: "pod:one", column: "pods", kind: "pod", label, state: "Running" };
    render(<TopologyCanvas topology={{ columns: [{ id: "workloads", label: "Workloads" }, { id: "pods", label: "Pods" }], nodes: [
      deployment,
      pod,
    ], edges: [{ from: "deployment:inventory", to: "pod:one", kind: "owns" }] }} />);

    expect(shortenTopologyLabel(label)).toBe("inventory-api-developmen…");
    expect(topologyDisplayLabel(pod, deployment, 40)).toBe("inventory-api-development");
    expect(screen.getByText(label).getAttribute("role")).toBe("tooltip");
    expect(screen.getByLabelText(label).textContent).toBe("inventory-api-development");
  });

  it("keeps a StatefulSet pod ordinal", () => {
    expect(topologyDisplayLabel(
      { id: "pod:database-2", column: "pods", kind: "pod", label: "database-primary-2" },
      { id: "statefulset:database", column: "workloads", kind: "statefulset", label: "database-primary" },
    )).toBe("database-primary-2");
  });

  it("leaves stable workload names intact", () => {
    expect(topologyDisplayLabel({ id: "deployment:api", column: "workloads", kind: "deployment", label: "api-2026" })).toBe("api-2026");
  });

  it("makes selectable resources keyboard accessible", async () => {
    const select = vi.fn();
    const node = { id: "service:api", column: "access", kind: "service", label: "api" };
    render(<TopologyCanvas topology={{ columns: [{ id: "access", label: "Access" }], nodes: [node], edges: [] }} onNodeSelect={select} />);

    const card = screen.getByRole("button", { name: "Inspect service api" });
    card.focus();
    await screen.findByRole("button", { name: "Inspect service api" });
    card.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(select).toHaveBeenCalledWith(node);
  });
});
