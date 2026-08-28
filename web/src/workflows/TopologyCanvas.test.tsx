// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { shortenTopologyLabel, TopologyCanvas } from "./TopologyCanvas";

describe("topology labels", () => {
  it("keeps the identifying suffix and exposes the complete name", () => {
    const label = "inventory-api-development-77669dcd4f-xlh6p";
    render(<TopologyCanvas topology={{ columns: [{ id: "pods", label: "Pods" }], nodes: [{ id: "pod:one", column: "pods", kind: "pod", label, state: "Running" }], edges: [] }} />);

    expect(shortenTopologyLabel(label)).toBe("inventory-api-d…d4f-xlh6p");
    expect(screen.getByText(label).getAttribute("role")).toBe("tooltip");
    expect(screen.getByLabelText(label).textContent).toBe("inventory-api-d…d4f-xlh6p");
  });
});
