import type { WorkflowResource } from "../api";

export function workflowResourceStatus(resource: WorkflowResource, runState?: string): string {
  if (!resource.active) return resource.state === "paused" ? "paused" : "pending_activation";
  return runState || resource.state;
}

export function workflowResourceStatusLabel(state: string): string {
  return state.replaceAll("_", " ");
}
