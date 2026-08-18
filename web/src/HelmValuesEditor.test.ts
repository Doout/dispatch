import { describe, expect, it } from "vitest";
import { helmValueOverrides, mergeHelmValues } from "./HelmValuesEditor";

describe("Helm values helpers", () => {
  it("merges a profile without mutating chart defaults", () => {
    const defaults = { backend: { replicas: 1, enabled: false }, tags: ["base"] };
    const merged = mergeHelmValues(defaults, { backend: { replicas: 3 }, tags: ["slot"] });
    expect(merged).toEqual({ backend: { replicas: 3, enabled: false }, tags: ["slot"] });
    expect(defaults.backend.replicas).toBe(1);
  });

  it("stores only values that differ from chart defaults", () => {
    const defaults = { previewId: "", backend: { replicas: 1, env: { LOG_LEVEL: "info" } } };
    const current = { previewId: "847", backend: { replicas: 1, env: { LOG_LEVEL: "debug" } } };
    expect(helmValueOverrides(defaults, current)).toEqual({ previewId: "847", backend: { env: { LOG_LEVEL: "debug" } } });
  });
});
