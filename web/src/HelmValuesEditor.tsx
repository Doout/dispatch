import { ArrowCounterClockwise, Code, MagnifyingGlass, SlidersHorizontal } from "@phosphor-icons/react";
import { useEffect, useMemo, useState } from "react";
import { HelmChartInspection, HelmValue } from "./api";

type HelmObject = Record<string, HelmValue>;
type ValuePath = Array<string | number>;
type SchemaNode = { description?: string; enum?: HelmValue[]; properties?: Record<string, SchemaNode>; items?: SchemaNode };
const noDifference = Symbol("no-difference");

const isObject = (value: HelmValue | undefined): value is HelmObject => Boolean(value) && typeof value === "object" && !Array.isArray(value);
const cloneValue = <T extends HelmValue>(value: T): T => structuredClone(value);
const equalValue = (left: HelmValue | undefined, right: HelmValue | undefined) => JSON.stringify(left) === JSON.stringify(right);

export function mergeHelmValues(base: HelmObject, overlay: HelmObject): HelmObject {
  const merged = cloneValue(base);
  Object.entries(overlay).forEach(([key, value]) => {
    const current = merged[key];
    merged[key] = isObject(current) && isObject(value) ? mergeHelmValues(current, value) : cloneValue(value);
  });
  return merged;
}

function diffValue(base: HelmValue | undefined, current: HelmValue): HelmValue | typeof noDifference {
  if (isObject(base) && isObject(current)) {
    const difference: HelmObject = {};
    Object.entries(current).forEach(([key, value]) => {
      const next = diffValue(base[key], value);
      if (next !== noDifference) difference[key] = next;
    });
    return Object.keys(difference).length ? difference : noDifference;
  }
  return equalValue(base, current) ? noDifference : cloneValue(current);
}

export function helmValueOverrides(defaults: HelmObject, current: HelmObject): HelmObject {
  const difference = diffValue(defaults, current);
  return difference === noDifference ? {} : difference as HelmObject;
}

function valueAt(root: HelmValue | undefined, path: ValuePath): HelmValue | undefined {
  return path.reduce<HelmValue | undefined>((value, segment) => {
    if (Array.isArray(value) && typeof segment === "number") return value[segment];
    if (isObject(value) && typeof segment === "string") return value[segment];
    return undefined;
  }, root);
}

function replaceAt(root: HelmObject, path: ValuePath, next: HelmValue): HelmObject {
  const copy = cloneValue(root);
  let cursor: HelmValue = copy;
  path.forEach((segment, index) => {
    if (index === path.length - 1) {
      if (Array.isArray(cursor) && typeof segment === "number") cursor[segment] = next;
      else if (isObject(cursor) && typeof segment === "string") cursor[segment] = next;
      return;
    }
    cursor = Array.isArray(cursor) && typeof segment === "number" ? cursor[segment] : isObject(cursor) && typeof segment === "string" ? cursor[segment] : cursor;
  });
  return copy;
}

function schemaAt(schema: unknown, path: ValuePath): SchemaNode | undefined {
  let current = schema as SchemaNode | undefined;
  path.forEach((segment) => {
    current = typeof segment === "number" ? current?.items : current?.properties?.[segment];
  });
  return current;
}

function formatPath(path: ValuePath) {
  return path.reduce((label, segment) => typeof segment === "number" ? `${label}[${segment}]` : label ? `${label}.${segment}` : segment, "");
}

function humanize(value: string) {
  return value.replace(/([a-z0-9])([A-Z])/g, "$1 $2").replace(/[-_]+/g, " ").replace(/^./, (character) => character.toUpperCase());
}

function leafCount(value: HelmValue): number {
  if (Array.isArray(value)) return value.length ? value.reduce<number>((total, item) => total + leafCount(item), 0) : 1;
  if (isObject(value)) return Object.keys(value).length ? Object.values(value).reduce<number>((total, item) => total + leafCount(item), 0) : 1;
  return 1;
}

function matchesValue(value: HelmValue, path: ValuePath, schema: SchemaNode | undefined, query: string): boolean {
  if (!query) return true;
  if (`${formatPath(path)} ${schema?.description ?? ""}`.toLowerCase().includes(query)) return true;
  if (isObject(value)) return Object.entries(value).some(([key, child]) => matchesValue(child, [...path, key], schema?.properties?.[key], query));
  if (Array.isArray(value)) return value.some((child, index) => matchesValue(child, [...path, index], schema?.items, query));
  return String(value ?? "").toLowerCase().includes(query);
}

function JSONValueField({ value, onChange }: { value: HelmValue; onChange: (value: HelmValue) => void }) {
  const [draft, setDraft] = useState(() => JSON.stringify(value, null, 2));
  const [invalid, setInvalid] = useState(false);
  useEffect(() => { setDraft(JSON.stringify(value, null, 2)); setInvalid(false); }, [value]);
  return <div className="helm-json-field">
    <textarea value={draft} spellCheck={false} aria-invalid={invalid} onChange={(event) => {
      const next = event.target.value;
      setDraft(next);
      try {
        onChange(JSON.parse(next) as HelmValue);
        setInvalid(false);
      } catch {
        setInvalid(true);
      }
    }} />
    {invalid && <small role="alert">Enter valid JSON before saving.</small>}
  </div>;
}

function HelmValueField({ path, value, baseline, schema, onChange }: { path: ValuePath; value: HelmValue; baseline: HelmValue | undefined; schema?: SchemaNode; onChange: (value: HelmValue) => void }) {
  const name = typeof path[path.length - 1] === "string" ? humanize(path[path.length - 1] as string) : `Item ${Number(path[path.length - 1]) + 1}`;
  const modified = !equalValue(value, baseline);
  const reset = baseline === undefined ? undefined : () => onChange(cloneValue(baseline));
  const field = (() => {
    if (schema?.enum?.length) return <select value={String(value ?? "")} onChange={(event) => {
      const selected = schema.enum?.find((item) => String(item) === event.target.value);
      if (selected !== undefined) onChange(cloneValue(selected));
    }}>{schema.enum.map((item) => <option key={String(item)} value={String(item)}>{String(item)}</option>)}</select>;
    if (typeof value === "boolean") return <label className="helm-boolean-control"><input type="checkbox" checked={value} onChange={(event) => onChange(event.target.checked)} /><span>{value ? "Enabled" : "Disabled"}</span></label>;
    if (typeof value === "number") return <input type="number" value={value} onChange={(event) => event.target.value !== "" && onChange(Number(event.target.value))} />;
    if (Array.isArray(value) || isObject(value)) return <JSONValueField value={value} onChange={onChange} />;
    const text = value === null ? "" : String(value);
    return text.includes("\n") ? <textarea value={text} onChange={(event) => onChange(event.target.value)} /> : <input value={text} onChange={(event) => onChange(event.target.value)} />;
  })();
  return <div className={`helm-value-field ${modified ? "modified" : ""}`}>
    <div className="helm-value-label"><div><strong>{name}</strong><code>{formatPath(path)}</code>{schema?.description && <small>{schema.description}</small>}</div>{modified && reset && <button type="button" aria-label={`Reset ${formatPath(path)}`} title="Reset to loaded value" onClick={reset}><ArrowCounterClockwise size={14} /></button>}</div>
    {field}
  </div>;
}

function ValueTree({ value, baseline, path, schema, query, onChange }: { value: HelmValue; baseline: HelmValue | undefined; path: ValuePath; schema?: SchemaNode; query: string; onChange: (path: ValuePath, value: HelmValue) => void }) {
  if (isObject(value) && Object.keys(value).length) return <div className="helm-value-tree">{Object.entries(value).filter(([key, child]) => matchesValue(child, [...path, key], schema?.properties?.[key], query)).map(([key, child]) => {
    const childPath = [...path, key];
    const childBaseline = valueAt(baseline, [key]);
    const childSchema = schema?.properties?.[key];
    if (isObject(child) && Object.keys(child).length) return <details className="helm-value-nested" open key={key}><summary><span>{humanize(key)}</span><code>{formatPath(childPath)}</code></summary><ValueTree value={child} baseline={childBaseline} path={childPath} schema={childSchema} query={query} onChange={onChange} /></details>;
    return <HelmValueField key={key} path={childPath} value={child} baseline={childBaseline} schema={childSchema} onChange={(next) => onChange(childPath, next)} />;
  })}</div>;
  return <HelmValueField path={path} value={value} baseline={baseline} schema={schema} onChange={(next) => onChange(path, next)} />;
}

export function HelmValuesEditor({ inspection, values, baseline, selectedProfile, onProfileChange, onChange, onRawMode }: { inspection: HelmChartInspection; values: HelmObject; baseline: HelmObject; selectedProfile: string; onProfileChange: (path: string) => void; onChange: (values: HelmObject) => void; onRawMode: () => void }) {
  const [search, setSearch] = useState("");
  const query = search.trim().toLowerCase();
  const overrides = useMemo(() => helmValueOverrides(inspection.defaults, values), [inspection.defaults, values]);
  const hasUnsavedStructuredChanges = !equalValue(values, baseline);
  const defaultCount = leafCount(inspection.defaults);
  const overrideCount = Object.keys(overrides).length ? leafCount(overrides) : 0;
  const update = (path: ValuePath, value: HelmValue) => onChange(replaceAt(values, path, value));
  const groups = Object.entries(values).filter(([key, value]) => matchesValue(value, [key], schemaAt(inspection.schema, [key]), query));

  return <section className="helm-values-workspace wide" aria-labelledby="helm-values-title">
    <header className="helm-values-header"><div><span className="helm-values-icon"><SlidersHorizontal size={17} /></span><div><strong id="helm-values-title">Chart values</strong><small>{defaultCount} defaults loaded from {inspection.chart.name}</small></div></div><div><span>{overrideCount} overridden</span><button type="button" className="quiet-button" disabled={hasUnsavedStructuredChanges} title={hasUnsavedStructuredChanges ? "Reset structured changes before switching editors" : "Use raw YAML overrides"} onClick={onRawMode}><Code size={15} />Raw YAML</button></div></header>
    <div className="helm-chart-summary"><div><strong>{inspection.chart.name}</strong><span>{inspection.chart.description || "Helm application chart"}</span></div><dl><div><dt>Chart</dt><dd>{inspection.chart.version || "Unknown"}</dd></div><div><dt>App</dt><dd>{inspection.chart.appVersion || "Unknown"}</dd></div><div><dt>Path</dt><dd>{inspection.chartPath}</dd></div></dl></div>
    <div className="helm-values-toolbar">
      <label><span>Value profile</span><select value={selectedProfile} onChange={(event) => onProfileChange(event.target.value)}><option value="">Chart defaults</option>{inspection.profiles.map((profile) => <option value={profile.path} key={profile.path}>{humanize(profile.name)} ({profile.path})</option>)}</select></label>
      <label className="helm-values-search"><span className="sr-only">Search chart values</span><MagnifyingGlass size={15} /><input type="search" placeholder="Search value paths" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
      <button type="button" className="quiet-button" disabled={equalValue(values, baseline)} onClick={() => onChange(cloneValue(baseline))}><ArrowCounterClockwise size={15} />Reset changes</button>
    </div>
    <div className="helm-value-groups">{groups.map(([key, value]) => <details className="helm-value-group" open={Boolean(query) || groups.length < 6} key={key}><summary><div><strong>{humanize(key)}</strong><code>{key}</code></div><span>{leafCount(value)} value{leafCount(value) === 1 ? "" : "s"}</span></summary><ValueTree value={value} baseline={baseline[key]} path={[key]} schema={schemaAt(inspection.schema, [key])} query={query} onChange={update} /></details>)}</div>
    {!groups.length && <div className="helm-values-empty">No values match “{search.trim()}”.</div>}
  </section>;
}
