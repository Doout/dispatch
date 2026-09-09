import type { Overview } from "./api";

export type OverviewState = { version: string; value: Overview };
let current: OverviewState | undefined;
export const getOverviewState = () => current;
export function setOverviewState(next: OverviewState | undefined, notify = true) {
  current = next;
  if (notify) window.dispatchEvent(new Event("dispatch-overview-baseline"));
}

export type PatchOperation = { op: "add" | "remove" | "replace" | "move"; path: string; from?: string; value?: unknown };
export type OverviewDelta = { base: string; version: string; ops: PatchOperation[] };

export function applyOverviewDelta(state: OverviewState, delta: OverviewDelta): OverviewState {
  if (delta.base !== state.version || !delta.version) throw new Error("Overview version mismatch");
  let result = structuredClone(state.value);
  const locate = (path: string) => {
    if (!path.startsWith("/")) throw new Error("Invalid patch path");
    const parts = path.slice(1).split("/").map(key => key.replace(/~1/g, "/").replace(/~0/g, "~"));
    let parent: any = result;
    for (const key of parts.slice(0, -1)) {
      if (!parent || !Object.hasOwn(parent, key)) throw new Error("Missing patch parent");
      parent = parent[key];
    }
    if (!parent || typeof parent !== "object") throw new Error("Invalid patch parent");
    return { parent, key: parts[parts.length - 1] };
  };
  const remove = (path: string) => {
    const { parent, key } = locate(path);
    if (!Object.hasOwn(parent, key)) throw new Error("Missing patch value");
    const value = parent[key];
    if (Array.isArray(parent)) parent.splice(Number(key), 1);
    else delete parent[key];
    return value;
  };
  for (const op of delta.ops) {
    if (op.path === "" && op.op === "replace") { result = op.value as Overview; continue; }
    if (op.op === "remove") { remove(op.path); continue; }
    if (!["add", "replace", "move"].includes(op.op)) throw new Error("Unknown patch operation");
    const value = op.op === "move" ? remove(op.from ?? "") : op.value;
    const { parent, key } = locate(op.path);
    if (op.op === "replace" && !Object.hasOwn(parent, key)) throw new Error("Missing patch value");
    if (Array.isArray(parent)) {
      const index = Number(key);
      if (!/^\d+$/.test(key) || index > parent.length) throw new Error("Invalid array index");
      if (op.op === "replace") parent[index] = value;
      else parent.splice(index, 0, value);
    } else Object.defineProperty(parent, key, { value, enumerable: true, writable: true, configurable: true });
  }
  return { version: delta.version, value: result };
}
