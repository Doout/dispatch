// @vitest-environment jsdom

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import { ResourceInspector } from "./ResourceInspector";

describe("resource inspector", () => {
  beforeAll(() => {
    Object.defineProperty(Range.prototype, "getClientRects", { configurable: true, value: () => [] });
    Object.defineProperty(Range.prototype, "getBoundingClientRect", { configurable: true, value: () => new DOMRect() });
  });
  afterEach(() => vi.restoreAllMocks());

  it("shows live YAML, pod logs, and Kubernetes events without resizing the topology", async () => {
    vi.spyOn(api, "deploymentResource").mockResolvedValue({
      manifest: { name: "api-7b8d9f-x2k4m", kind: "Pod", apiVersion: "v1", document: "kind: Pod\nmetadata:\n  name: api-7b8d9f-x2k4m\nspec:\n  containers:\n    - name: api\n      image: registry.example.com/api:1.0.0\n    - name: metrics\n      image: registry.example.com/metrics:2.0.0\n" },
      loggable: true,
      logs: [{ container: "api", content: "2026-08-31T01:00:00Z server ready" }],
      events: [{ type: "Normal", reason: "Started", message: "Started container api", count: 1, lastSeen: "2026-08-31T01:00:00Z" }],
    });

    render(<ResourceInspector deploymentID="deployment-1" node={{ id: "pod:api", column: "pods", kind: "pod", label: "api-7b8d9f-x2k4m", state: "Running" }} onClose={() => undefined} />);

    const yaml = await screen.findByRole("textbox", { name: "YAML manifest" });
    expect(yaml.textContent).toContain("kind: Pod");
    await userEvent.type(screen.getByRole("searchbox", { name: "Filter manifest by key or JSONPath" }), "image");
    expect(screen.getByRole("listitem", { name: /image.*registry\.example\.com\/api:1\.0\.0/i })).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByRole("textbox", { name: "YAML manifest" }).textContent).toContain("image: registry.example.com/api:1.0.0");
      expect(screen.getByRole("textbox", { name: "YAML manifest" }).textContent).not.toContain("kind: Pod");
    });
    expect(screen.queryByText("Before")).toBeNull();
    expect(screen.queryByText("After")).toBeNull();
    expect(screen.queryByText("Scope")).toBeNull();
    await userEvent.click(screen.getAllByRole("button", { name: "Show lines above" })[0]);
    await waitFor(() => expect(screen.getByRole("textbox", { name: "YAML manifest" }).textContent).toContain("containers:"));
    const search = screen.getByRole("searchbox", { name: "Filter manifest by key or JSONPath" });
    await userEvent.clear(search);
    await userEvent.type(search, "containers");
    await waitFor(() => expect(window.document.querySelector(".cm-foldPlaceholder")).toBeTruthy());
    expect(screen.getByRole("textbox", { name: "YAML manifest" }).textContent).toContain("containers:");
    await userEvent.clear(search);
    await waitFor(() => expect(screen.getByRole("textbox", { name: "YAML manifest" }).textContent).toContain("kind: Pod"));
    await userEvent.click(screen.getByRole("button", { name: "JSON" }));
    expect(screen.getByRole("textbox", { name: "JSON manifest" }).textContent).toContain('"kind": "Pod"');
    await userEvent.click(screen.getByRole("button", { name: "Logs" }));
    expect(screen.getByText("2026-08-31T01:00:00Z server ready")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /^Events/ }));
    expect(screen.getByText("Started container api")).toBeTruthy();
  });
});
