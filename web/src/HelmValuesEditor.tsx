import Form from "@rjsf/core";
import validator from "@rjsf/validator-ajv8";
import { ADDITIONAL_PROPERTY_FLAG } from "@rjsf/utils";
import type { ArrayFieldItemTemplateProps, ArrayFieldTemplateProps, FieldTemplateProps, ObjectFieldTemplateProps, RJSFSchema, UiSchema, WidgetProps } from "@rjsf/utils";
import * as Switch from "@radix-ui/react-switch";
import { ArrowCounterClockwise, ArrowDown, ArrowUp, Code, MagnifyingGlass, Plus, SlidersHorizontal, Trash } from "@phosphor-icons/react";
import { useEffect, useMemo, useState } from "react";
import { HelmChartInspection, HelmValue } from "./api";

type HelmObject = Record<string, HelmValue>;
type SchemaNode = RJSFSchema & { properties?: Record<string, SchemaNode>; items?: SchemaNode };
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

function humanize(value: string) {
  return value.replace(/([a-z0-9])([A-Z])/g, "$1 $2").replace(/[-_]+/g, " ").replace(/^./, (character) => character.toUpperCase());
}

function leafCount(value: HelmValue): number {
  if (Array.isArray(value)) return value.length ? value.reduce<number>((total, item) => total + leafCount(item), 0) : 1;
  if (isObject(value)) return Object.keys(value).length ? Object.values(value).reduce<number>((total, item) => total + leafCount(item), 0) : 1;
  return 1;
}

function inferredSchema(value: HelmValue, title?: string): SchemaNode {
  if (Array.isArray(value)) return { type: "array", title, items: value.length ? inferredSchema(value[0]) : {} };
  if (isObject(value)) return {
    type: "object",
    title,
    properties: Object.fromEntries(Object.entries(value).map(([key, child]) => [key, inferredSchema(child, humanize(key))])),
    additionalProperties: true,
  };
  if (typeof value === "boolean") return { type: "boolean", title };
  if (typeof value === "number") return { type: Number.isInteger(value) ? "integer" : "number", title };
  if (value === null) return { type: ["string", "null"], title };
  return { type: "string", title };
}

function normalizedSchema(schema: SchemaNode | undefined, value: HelmValue, title: string): SchemaNode {
  const inferred = inferredSchema(value, title);
  if (!schema) return inferred;
  const result: SchemaNode = { ...inferred, ...schema, title: schema.title || title };
  if (isObject(value)) {
    result.type = "object";
    result.properties = Object.fromEntries(Object.entries(value).map(([key, child]) => [
      key,
      normalizedSchema(schema.properties?.[key], child, humanize(key)),
    ]));
    result.additionalProperties = schema.additionalProperties ?? true;
  }
  if (Array.isArray(value)) {
    result.type = "array";
    result.items = normalizedSchema(schema.items, value[0] ?? "", "Item");
  }
  return result;
}

function schemaForGroup(inspection: HelmChartInspection, key: string, value: HelmValue) {
  const root = inspection.schema as SchemaNode | undefined;
  return normalizedSchema(root?.properties?.[key], value, humanize(key));
}

function searchText(key: string, value: HelmValue, schema: SchemaNode | undefined): string {
  const own = `${key} ${humanize(key)} ${schema?.title ?? ""} ${schema?.description ?? ""}`;
  if (isObject(value)) return `${own} ${Object.entries(value).map(([childKey, child]) => searchText(childKey, child, schema?.properties?.[childKey])).join(" ")}`;
  if (Array.isArray(value)) return `${own} ${value.map((child) => searchText(key, child, schema?.items)).join(" ")}`;
  return `${own} ${String(value ?? "")}`;
}

function SwitchWidget({ id, value, disabled, readonly, onChange }: WidgetProps) {
  const checked = Boolean(value);
  return <div className="helm-switch-control">
    <Switch.Root id={id} checked={checked} disabled={disabled || readonly} onCheckedChange={onChange}>
      <Switch.Thumb />
    </Switch.Root>
    <label htmlFor={id}>{checked ? "Enabled" : "Disabled"}</label>
  </div>;
}

function FieldTemplate({ id, fieldPathId, classNames, label, children, description, errors, help, hidden, required, schema, formData, disabled, readonly, onKeyRenameBlur, onRemoveProperty }: FieldTemplateProps) {
  const path = fieldPathId.path.map(String).join(".");
  const arrayItem = typeof fieldPathId.path.at(-1) === "number";
  if (hidden) return <div hidden>{children}</div>;
  if (ADDITIONAL_PROPERTY_FLAG in schema) return <div className="helm-additional-property">
    <label><span>Key</span><input type="text" defaultValue={label} onBlur={onKeyRenameBlur} disabled={disabled || readonly} /></label>
    <label><span>Value</span><span className="helm-additional-value">{children}</span></label>
    <button type="button" title="Remove property" aria-label={`Remove ${label}`} disabled={disabled || readonly} onClick={onRemoveProperty}><Trash size={14} /></button>
    {errors}
  </div>;
  if (id === "root" || schema.type === "object") return <div className={classNames}>{children}{errors}</div>;
  if (schema.type === "array") return <section className="helm-composite-field"><header><div><strong>{label}</strong><code>{path}</code></div>{description}</header>{children}{errors}</section>;
  if (arrayItem) return <div className="helm-array-value">{children}{errors}</div>;
  const changed = Boolean((schema as SchemaNode)["x-dispatch-modified"]);
  return <div className={`helm-schema-field ${changed ? "modified" : ""}`}>
    <div className="helm-schema-label"><label htmlFor={id}>{label}{required && <span aria-hidden="true"> *</span>}</label><code>{path}</code></div>
    {description}
    {children}
    {errors}{help}
    {formData === undefined && <small className="helm-field-hint">Not set</small>}
  </div>;
}

function ObjectFieldTemplate({ title, description, properties, fieldPathId, schema, disabled, readonly, onAddProperty }: ObjectFieldTemplateProps) {
  const root = fieldPathId.$id === "root";
  return <section className={root ? "helm-schema-root" : "helm-object-group"}>
    {!root && <header><div><strong>{title}</strong><code>{fieldPathId.path.join(".")}</code></div>{description}</header>}
    <div className="helm-object-fields">{properties.filter((property) => !property.hidden).map((property) => <div key={property.name}>{property.content}</div>)}</div>
    {!root && Boolean(schema.additionalProperties) && !disabled && !readonly && <button type="button" className="helm-property-add" onClick={onAddProperty}><Plus size={13} />Add property</button>}
  </section>;
}

function ArrayFieldTemplate({ items, canAdd, onAddClick }: ArrayFieldTemplateProps) {
  return <div className="helm-array-control">
    {items.length ? <><div className="helm-array-list">{items}</div>{canAdd && <button type="button" className="helm-array-add" onClick={onAddClick}><Plus size={13} />Add item</button>}</> : <div className="helm-array-empty"><span>No items</span>{canAdd && <button type="button" onClick={onAddClick}><Plus size={13} />Add item</button>}</div>}
  </div>;
}

function ArrayFieldItemTemplate({ children, buttonsProps, index, hasToolbar }: ArrayFieldItemTemplateProps) {
  return <div className="helm-array-item">
    <span className="helm-array-index">{index + 1}</span>
    <div className="helm-array-item-value">{children}</div>
    {hasToolbar && <div className="helm-array-actions">
      {buttonsProps.hasMoveUp && <button type="button" title="Move up" aria-label={`Move item ${index + 1} up`} onClick={buttonsProps.onMoveUpItem}><ArrowUp size={14} /></button>}
      {buttonsProps.hasMoveDown && <button type="button" title="Move down" aria-label={`Move item ${index + 1} down`} onClick={buttonsProps.onMoveDownItem}><ArrowDown size={14} /></button>}
      {buttonsProps.hasRemove && <button type="button" title="Remove" aria-label={`Remove item ${index + 1}`} onClick={buttonsProps.onRemoveItem}><Trash size={14} /></button>}
    </div>}
  </div>;
}

function schemaWithChanges(schema: SchemaNode, baseline: HelmValue | undefined, value: HelmValue): SchemaNode {
  const result = { ...schema, "x-dispatch-modified": !equalValue(baseline, value) };
  if (isObject(value) && schema.properties) result.properties = Object.fromEntries(Object.entries(schema.properties).map(([key, childSchema]) => [
    key,
    schemaWithChanges(childSchema, isObject(baseline) ? baseline[key] : undefined, value[key] ?? null),
  ]));
  return result;
}

const uiSchema: UiSchema = {
  "ui:submitButtonOptions": { norender: true },
};

export function HelmValuesEditor({ inspection, values, baseline, selectedProfile, onProfileChange, onChange, onRawMode }: { inspection: HelmChartInspection; values: HelmObject; baseline: HelmObject; selectedProfile: string; onProfileChange: (path: string) => void; onChange: (values: HelmObject) => void; onRawMode?: () => void }) {
  const [search, setSearch] = useState("");
  const [selectedGroup, setSelectedGroup] = useState(() => Object.keys(values)[0] ?? "");
  const overrides = useMemo(() => helmValueOverrides(inspection.defaults, values), [inspection.defaults, values]);
  const schemas = useMemo(() => Object.fromEntries(Object.entries(values).map(([key, value]) => [key, schemaForGroup(inspection, key, inspection.defaults[key] ?? (isObject(value) ? {} : value))])), [inspection, values]);
  const query = search.trim().toLowerCase();
  const groups = Object.entries(values).filter(([key, value]) => !query || searchText(key, value, schemas[key]).toLowerCase().includes(query));
  const activeKey = groups.some(([key]) => key === selectedGroup) ? selectedGroup : groups[0]?.[0] ?? "";
  const activeValue = values[activeKey];
  const activeSchema = activeKey && activeValue !== undefined ? schemaWithChanges(schemas[activeKey], baseline[activeKey], activeValue) : undefined;
  const defaultCount = leafCount(inspection.defaults);
  const overrideCount = Object.keys(overrides).length ? leafCount(overrides) : 0;
  const hasUnsavedStructuredChanges = !equalValue(values, baseline);

  useEffect(() => {
    if (activeKey && activeKey !== selectedGroup) setSelectedGroup(activeKey);
  }, [activeKey, selectedGroup]);

  return <section className="helm-values-workspace wide" aria-labelledby="helm-values-title">
    <header className="helm-values-header"><div><span className="helm-values-icon"><SlidersHorizontal size={17} /></span><div><strong id="helm-values-title">Chart values</strong><small>{defaultCount} values</small></div></div><div><span>{overrideCount} overridden</span>{onRawMode && <button type="button" className="quiet-button" disabled={hasUnsavedStructuredChanges} title={hasUnsavedStructuredChanges ? "Reset structured changes before switching editors" : "Use raw YAML overrides"} onClick={onRawMode}><Code size={15} />Raw YAML</button>}</div></header>
    <div className="helm-chart-summary"><div><strong>{inspection.chart.name}</strong>{inspection.chart.description && <span>{inspection.chart.description}</span>}</div><dl><div><dt>Chart</dt><dd>{inspection.chart.version || "Unknown"}</dd></div><div><dt>App</dt><dd>{inspection.chart.appVersion || "Unknown"}</dd></div><div><dt>Path</dt><dd>{inspection.chartPath}</dd></div></dl></div>
    <div className="helm-values-toolbar">
      <label><span>Value profile</span><select value={selectedProfile} onChange={(event) => onProfileChange(event.target.value)}><option value="">Chart defaults</option>{inspection.profiles.map((profile) => <option value={profile.path} key={profile.path}>{humanize(profile.name)} ({profile.path})</option>)}</select></label>
      <label className="helm-values-search"><span className="sr-only">Search chart values</span><MagnifyingGlass size={15} /><input type="search" placeholder="Find a section or value" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
      <button type="button" className="quiet-button" disabled={equalValue(values, baseline)} onClick={() => onChange(cloneValue(baseline))}><ArrowCounterClockwise size={15} />Reset changes</button>
    </div>
    <div className="helm-values-layout">
      <nav className="helm-section-nav" aria-label="Chart value sections">
        <div className="helm-section-nav-title"><span>Sections</span><span>{groups.length}</span></div>
        {groups.map(([key, value]) => {
          const groupOverrides = isObject(overrides) ? overrides[key] : undefined;
          const changedCount = groupOverrides === undefined ? 0 : leafCount(groupOverrides);
          return <button type="button" className={activeKey === key ? "active" : ""} aria-current={activeKey === key ? "page" : undefined} key={key} onClick={() => setSelectedGroup(key)}>
            <span><strong>{humanize(key)}</strong><code>{key}</code></span>
            <span>{changedCount ? <em>{changedCount}</em> : null}<small>{leafCount(value)}</small></span>
          </button>;
        })}
      </nav>
      <div className="helm-section-editor">
        {activeKey && activeSchema && activeValue !== undefined ? <>
          <header><div><strong>{humanize(activeKey)}</strong><code>{activeKey}</code></div><span>{leafCount(activeValue)} value{leafCount(activeValue) === 1 ? "" : "s"}</span></header>
          <Form schema={activeSchema} formData={activeValue} validator={validator} uiSchema={uiSchema} widgets={{ CheckboxWidget: SwitchWidget }} templates={{ FieldTemplate, ObjectFieldTemplate, ArrayFieldTemplate, ArrayFieldItemTemplate }} onChange={({ formData }) => onChange({ ...values, [activeKey]: formData as HelmValue })} liveValidate={false} showErrorList={false}>
            <></>
          </Form>
        </> : <div className="helm-values-empty">No values match "{search.trim()}".</div>}
      </div>
    </div>
  </section>;
}
