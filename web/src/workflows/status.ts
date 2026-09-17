import type { WorkflowResource } from "../api";

export function workflowResourceStatus(resource: WorkflowResource, runState?: string): string {
  if (resource.state === "invalid") return "invalid";
  if (!resource.active) return resource.state === "paused" ? "paused" : "pending_activation";
  return runState || resource.state;
}

export function workflowResourceStatusLabel(state: string): string {
  if (state === "invalid") return "Configuration error";
  return state.replaceAll("_", " ");
}
