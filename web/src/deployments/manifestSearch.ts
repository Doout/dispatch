import { paths as queryPaths } from "jsonpathly";
import { parse, parseDocument, isMap, isSeq } from "yaml";

export type ManifestPathSegment = string | number;

export type ManifestMatch = {
  key: string;
  path: string;
  preview: string;
  segments: ManifestPathSegment[];
  value: unknown;
};

export type ManifestSearchResult = {
  error?: string;
  matches: ManifestMatch[];
  mode: "jsonpath" | "key";
};

export type DocumentRange = { from: number; to: number };

export type RenderedDocument = {
  gaps: DocumentGap[];
  ranges: Map<string, DocumentRange>;
  text: string;
  valueRanges: Map<string, DocumentRange>;
};

export type DocumentGap = {
  at: number;
  expandDown: string[];
  expandUp: string[];
};

export type MatchContext = Record<string, { after: number; before: number }>;

export type PathRanges = {
  key?: DocumentRange;
  value?: DocumentRange;
};

export function parseManifest(source: string): unknown {
  return parse(source);
}

export function searchManifest(value: unknown, input: string): ManifestSearchResult {
  const query = input.trim();
  const mode = query.startsWith("$") ? "jsonpath" : "key";
  if (!query) return { matches: [], mode };

  try {
    const paths = mode === "jsonpath" ? queryPaths(value, query) : findMatchingKeyPaths(value, query);
    return {
      matches: paths.map((path) => {
        const segments = parseNormalizedPath(path);
        const matchValue = valueAtPath(value, segments);
        const last = segments.at(-1);
        return {
          key: last === undefined ? "$" : String(last),
          path,
          preview: previewValue(matchValue),
          segments,
          value: matchValue,
        };
      }),
      mode,
    };
  } catch (cause) {
    return {
      error: cause instanceof Error ? cause.message : "Invalid JSONPath.",
      matches: [],
      mode,
    };
  }
}

export function formatNormalizedPath(segments: ManifestPathSegment[]): string {
  return segments.reduce<string>((path, segment) => {
    if (typeof segment === "number") return `${path}[${segment}]`;
    return `${path}['${escapePathKey(segment)}']`;
  }, "$");
}

export function parseNormalizedPath(path: string): ManifestPathSegment[] {
  if (!path.startsWith("$")) throw new Error("JSONPath results must start with $.");
  const segments: ManifestPathSegment[] = [];
  let offset = 1;

  while (offset < path.length) {
    if (path[offset] !== "[") throw new Error(`Invalid normalized path at character ${offset + 1}.`);
    offset += 1;
    if (path[offset] === "'") {
      const result = readQuotedPathKey(path, offset + 1);
      segments.push(result.value);
      offset = result.offset;
      if (path[offset] !== "]") throw new Error(`Invalid normalized path at character ${offset + 1}.`);
      offset += 1;
      continue;
    }

    const end = path.indexOf("]", offset);
    if (end < 0) throw new Error("Unclosed JSONPath index.");
    const index = Number(path.slice(offset, end));
    if (!Number.isInteger(index)) throw new Error(`Invalid JSONPath index at character ${offset + 1}.`);
    segments.push(index);
    offset = end + 1;
  }

  return segments;
}

export function yamlRangeForPath(source: string, segments: ManifestPathSegment[]): DocumentRange | undefined {
  return yamlPathRanges(source, segments).key;
}

export function yamlPathRanges(source: string, segments: ManifestPathSegment[]): PathRanges {
  return createYamlPathLocator(source)(segments);
}

export function createYamlPathLocator(source: string): (segments: ManifestPathSegment[]) => PathRanges {
  const document = parseDocument(source);
  return (segments) => {
    let node: unknown = document.contents;
    if (!segments.length) return { key: rangeOf(node), value: rangeOf(node) };

    for (const [index, segment] of segments.entries()) {
      const isLast = index === segments.length - 1;
      if (typeof segment === "number" && isSeq(node)) {
        node = node.items[segment];
        if (isLast) return { key: rangeOf(node), value: rangeOf(node) };
        continue;
      }
      if (typeof segment === "string" && isMap(node)) {
        const pair = node.items.find((item) => scalarValue(item.key) === segment);
        if (!pair) return {};
        if (isLast) return { key: rangeOf(pair.key) ?? rangeOf(pair.value), value: rangeOf(pair.value) };
        node = pair.value;
        continue;
      }
      return {};
    }

    return { key: rangeOf(node), value: rangeOf(node) };
  };
}

export function renderJSON(value: unknown): RenderedDocument {
  let text = "";
  const ranges = new Map<string, DocumentRange>();
  const valueRanges = new Map<string, DocumentRange>();

  const write = (current: unknown, segments: ManifestPathSegment[], depth: number) => {
    const path = formatNormalizedPath(segments);
    const start = text.length;
    if (Array.isArray(current)) {
      text += "[";
      if (current.length) text += "\n";
      current.forEach((item, index) => {
        text += "  ".repeat(depth + 1);
        write(item === undefined ? null : item, [...segments, index], depth + 1);
        text += index === current.length - 1 ? "\n" : ",\n";
      });
      text += `${"  ".repeat(depth)}]`;
    } else if (isRecord(current)) {
      const entries = Object.entries(current).filter(([, item]) => item !== undefined);
      text += "{";
      if (entries.length) text += "\n";
      entries.forEach(([key, item], index) => {
        text += "  ".repeat(depth + 1);
        const keyStart = text.length;
        text += JSON.stringify(key);
        const keyEnd = text.length;
        text += ": ";
        const childSegments = [...segments, key];
        write(item, childSegments, depth + 1);
        ranges.set(formatNormalizedPath(childSegments), { from: keyStart, to: keyEnd });
        text += index === entries.length - 1 ? "\n" : ",\n";
      });
      text += `${"  ".repeat(depth)}}`;
    } else {
      text += JSON.stringify(current) ?? "null";
    }
    valueRanges.set(path, { from: start, to: text.length });
    if (!ranges.has(path)) ranges.set(path, { from: start, to: text.length });
  };

  write(value, [], 0);
  return { gaps: [], ranges, text: `${text}\n`, valueRanges };
}

export function filterDocument(
  source: string,
  matches: ManifestMatch[],
  context: MatchContext,
  locate: (segments: ManifestPathSegment[]) => PathRanges,
): RenderedDocument {
  if (!matches.length) return { gaps: [], ranges: new Map(), text: "", valueRanges: new Map() };

  const candidates = matches.flatMap((match) => {
    const reveal = context[match.path] ?? { after: 0, before: 0 };
    const focus = locate(match.segments);
    const raw = combinedRange(focus.key, focus.value);
    if (!raw) return [];
    return [{
      from: lineStart(source, raw.from, reveal.before),
      match,
      selection: focus.key ?? focus.value ?? raw,
      to: lineEnd(source, raw.to, reveal.after),
      value: focus.value,
    }];
  }).sort((left, right) => left.from - right.from || left.to - right.to);

  const groups: Array<{ from: number; items: typeof candidates; to: number }> = [];
  candidates.forEach((candidate) => {
    const current = groups.at(-1);
    if (current && candidate.from <= current.to) {
      current.to = Math.max(current.to, candidate.to);
      current.items.push(candidate);
      return;
    }
    groups.push({ from: candidate.from, items: [candidate], to: candidate.to });
  });

  let text = "";
  const gaps: DocumentGap[] = [];
  const ranges = new Map<string, DocumentRange>();
  const valueRanges = new Map<string, DocumentRange>();
  groups.forEach((group, index) => {
    const previous = groups[index - 1];
    if (group.from > (previous?.to ?? 0)) {
      gaps.push({
        at: text.length,
        expandDown: previous ? uniquePaths(previous.items) : [],
        expandUp: uniquePaths(group.items),
      });
    }
    const outputStart = text.length;
    text += source.slice(group.from, group.to);
    group.items.forEach((item) => {
      const from = outputStart + Math.max(0, item.selection.from - group.from);
      const to = outputStart + Math.max(item.selection.to - group.from, item.selection.from - group.from);
      ranges.set(item.match.path, { from, to });
      if (item.value) {
        valueRanges.set(item.match.path, {
          from: outputStart + Math.max(0, item.value.from - group.from),
          to: outputStart + Math.max(item.value.to - group.from, item.value.from - group.from),
        });
      }
    });
  });

  const last = groups.at(-1);
  if (last && last.to < source.length) {
    gaps.push({ at: text.length, expandDown: uniquePaths(last.items), expandUp: [] });
  }

  return { gaps, ranges, text, valueRanges };
}

function uniquePaths(items: Array<{ match: ManifestMatch }>): string[] {
  return [...new Set(items.map((item) => item.match.path))];
}

function findMatchingKeyPaths(value: unknown, input: string): string[] {
  const query = input.toLocaleLowerCase();
  const matches: string[] = [];

  const visit = (current: unknown, segments: ManifestPathSegment[]) => {
    if (Array.isArray(current)) {
      current.forEach((item, index) => visit(item, [...segments, index]));
      return;
    }
    if (!isRecord(current)) return;
    Object.entries(current).forEach(([key, item]) => {
      const childSegments = [...segments, key];
      if (key.toLocaleLowerCase().includes(query)) matches.push(formatNormalizedPath(childSegments));
      visit(item, childSegments);
    });
  };

  visit(value, []);
  return matches;
}

function valueAtPath(value: unknown, segments: ManifestPathSegment[]): unknown {
  return segments.reduce<unknown>((current, segment) => {
    if (typeof segment === "number" && Array.isArray(current)) return current[segment];
    if (typeof segment === "string" && isRecord(current)) return current[segment];
    return undefined;
  }, value);
}

function previewValue(value: unknown): string {
  if (Array.isArray(value)) return `${value.length} ${value.length === 1 ? "item" : "items"}`;
  if (isRecord(value)) {
    const count = Object.keys(value).length;
    return `${count} ${count === 1 ? "key" : "keys"}`;
  }
  if (value === null) return "null";
  if (value === undefined) return "undefined";
  const text = String(value).replace(/\s+/g, " ").trim();
  return text.length > 88 ? `${text.slice(0, 85)}...` : text;
}

function combinedRange(key?: DocumentRange, value?: DocumentRange): DocumentRange | undefined {
  if (!key) return value;
  if (!value) return key;
  return { from: Math.min(key.from, value.from), to: Math.max(key.to, value.to) };
}

function lineStart(source: string, offset: number, count: number): number {
  let start = source.lastIndexOf("\n", Math.max(0, offset) - 1) + 1;
  for (let index = 0; index < count && start > 0; index += 1) {
    start = source.lastIndexOf("\n", start - 2) + 1;
  }
  return start;
}

function lineEnd(source: string, offset: number, count: number): number {
  let end = source.indexOf("\n", Math.max(0, offset - 1));
  end = end < 0 ? source.length : end + 1;
  for (let index = 0; index < count && end < source.length; index += 1) {
    const next = source.indexOf("\n", end);
    end = next < 0 ? source.length : next + 1;
  }
  return end;
}

function readQuotedPathKey(path: string, start: number): { offset: number; value: string } {
  let value = "";
  let offset = start;
  while (offset < path.length) {
    const char = path[offset];
    if (char === "'") return { offset: offset + 1, value };
    if (char !== "\\") {
      value += char;
      offset += 1;
      continue;
    }

    const escaped = path[offset + 1];
    if (escaped === "u") {
      const code = path.slice(offset + 2, offset + 6);
      if (!/^[0-9a-f]{4}$/i.test(code)) throw new Error(`Invalid Unicode escape at character ${offset + 1}.`);
      value += String.fromCharCode(Number.parseInt(code, 16));
      offset += 6;
      continue;
    }
    const escapes: Record<string, string> = { "'": "'", "\\": "\\", b: "\b", f: "\f", n: "\n", r: "\r", t: "\t" };
    if (!(escaped in escapes)) throw new Error(`Invalid escape at character ${offset + 1}.`);
    value += escapes[escaped];
    offset += 2;
  }
  throw new Error("Unclosed JSONPath key.");
}

function escapePathKey(value: string): string {
  return value
    .replace(/\\/g, "\\\\")
    .replace(/'/g, "\\'")
    .replace(/\u0008/g, "\\b")
    .replace(/\u000c/g, "\\f")
    .replace(/\n/g, "\\n")
    .replace(/\r/g, "\\r")
    .replace(/\t/g, "\\t");
}

function scalarValue(value: unknown): string | undefined {
  if (!value || typeof value !== "object" || !("value" in value)) return undefined;
  const scalar = value as { value?: unknown };
  return typeof scalar.value === "string" ? scalar.value : String(scalar.value);
}

function rangeOf(value: unknown): DocumentRange | undefined {
  if (!value || typeof value !== "object" || !("range" in value)) return undefined;
  const range = (value as { range?: readonly number[] }).range;
  if (!range || range.length < 2) return undefined;
  return { from: range[0], to: range[1] };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
