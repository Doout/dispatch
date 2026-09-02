import { describe, expect, it } from "vitest";
import {
  filterDocument,
  formatNormalizedPath,
  parseManifest,
  parseNormalizedPath,
  renderJSON,
  searchManifest,
  yamlPathRanges,
  yamlRangeForPath,
} from "./manifestSearch";

const manifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout-api
  labels:
    app.kubernetes.io/name: checkout
spec:
  template:
    spec:
      containers:
        - name: api
          image: registry.example.com/checkout:1.4.0
          imagePullPolicy: IfNotPresent
        - name: metrics
          image: registry.example.com/metrics:2.0.0
`;

describe("manifest search", () => {
  it("finds every key containing a partial term", () => {
    const result = searchManifest(parseManifest(manifest), "image");

    expect(result.error).toBeUndefined();
    expect(result.matches.map((match) => match.key)).toEqual(["image", "imagePullPolicy", "image"]);
    expect(result.matches[0].path).toBe("$['spec']['template']['spec']['containers'][0]['image']");
    expect(result.matches[2].preview).toBe("registry.example.com/metrics:2.0.0");
  });

  it("evaluates JSONPath and returns normalized paths", () => {
    const result = searchManifest(parseManifest(manifest), "$.spec.template.spec.containers[*].image");

    expect(result.error).toBeUndefined();
    expect(result.matches).toHaveLength(2);
    expect(result.matches.map((match) => match.preview)).toEqual([
      "registry.example.com/checkout:1.4.0",
      "registry.example.com/metrics:2.0.0",
    ]);
  });

  it("reports invalid JSONPath without throwing", () => {
    const result = searchManifest(parseManifest(manifest), "$[invalid");

    expect(result.matches).toEqual([]);
    expect(result.error).toBeTruthy();
  });

  it("round-trips normalized paths with Kubernetes label keys", () => {
    const segments = ["metadata", "labels", "app.kubernetes.io/name", "it's-safe", 0];
    const path = formatNormalizedPath(segments);

    expect(parseNormalizedPath(path)).toEqual(segments);
  });

  it("maps results to both YAML and rendered JSON ranges", () => {
    const value = parseManifest(manifest);
    const segments = ["spec", "template", "spec", "containers", 0, "image"];
    const yamlRange = yamlRangeForPath(manifest, segments);
    const json = renderJSON(value);
    const path = formatNormalizedPath(segments);

    expect(manifest.slice(yamlRange?.from, yamlRange?.to)).toBe("image");
    expect(json.text.slice(json.ranges.get(path)?.from, json.ranges.get(path)?.to)).toBe('"image"');
    expect(JSON.parse(json.text)).toEqual(value);
  });

  it("projects each matching key with its value", () => {
    const matches = searchManifest(parseManifest(manifest), "image").matches;
    const locate = (segments: Array<string | number>) => yamlPathRanges(manifest, segments);
    const focused = filterDocument(manifest, matches, {}, locate);

    expect(focused.text).toContain("image: registry.example.com/checkout:1.4.0");
    expect(focused.text).toContain("imagePullPolicy: IfNotPresent");
    expect(focused.text).not.toContain("apiVersion");
    expect(focused.text).not.toContain("kind: Deployment");
    expect(focused.text).not.toContain("containers:");
    expect(focused.text).not.toContain("name: api");
    expect(focused.gaps[0].expandUp).toContain(matches[0].path);

    const json = renderJSON(parseManifest(manifest));
    const jsonFocused = filterDocument(json.text, matches, {}, (segments) => {
      const path = formatNormalizedPath(segments);
      return { key: json.ranges.get(path), value: json.valueRanges.get(path) };
    });
    expect(jsonFocused.text).toContain('"image": "registry.example.com/checkout:1.4.0"');
    expect(jsonFocused.text).not.toContain('"kind": "Deployment"');
  });

  it("includes a matched list and maps its projected value range", () => {
    const [match] = searchManifest(parseManifest(manifest), "containers").matches;
    const projected = filterDocument(manifest, [match], {}, (segments) => yamlPathRanges(manifest, segments));
    const valueRange = projected.valueRanges.get(match.path);

    expect(projected.text).toContain("containers:");
    expect(projected.text).toContain("name: api");
    expect(projected.text).toContain("name: metrics");
    expect(projected.text.slice(valueRange?.from, valueRange?.to)).toContain("registry.example.com/metrics:2.0.0");
  });

  it("adds neighboring lines independently around a match", () => {
    const match = searchManifest(parseManifest(manifest), "imagePullPolicy").matches;
    const locate = (segments: Array<string | number>) => yamlPathRanges(manifest, segments);
    const before = filterDocument(manifest, match, { [match[0].path]: { before: 1, after: 0 } }, locate);
    const after = filterDocument(manifest, match, { [match[0].path]: { before: 0, after: 1 } }, locate);

    expect(before.text).toContain("image: registry.example.com/checkout:1.4.0");
    expect(before.text).not.toContain("name: metrics");
    expect(after.text).toContain("- name: metrics");
    expect(after.text).not.toContain("image: registry.example.com/checkout:1.4.0");
  });
});
