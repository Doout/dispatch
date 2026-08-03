import { Deployment, DeploymentState } from "./api";

export const stages = ["planned", "building", "checking", "live"] as const;
type Stage = typeof stages[number];

export const stateStage: Record<DeploymentState, Stage> = {
  queued: "planned",
  fetching: "planned",
  building: "building",
  starting: "building",
  checking: "checking",
  routing: "checking",
  succeeded: "live",
  failed: "checking",
  cancelled: "planned",
};

export function short(value?: string, length = 8) {
  return value && value.length > length ? value.slice(0, length) : value || "—";
}

export function relative(value?: string, now = Date.now()) {
  if (!value) return "Not started";
  const seconds = Math.max(0, Math.floor((now - new Date(value).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function statusTone(state: DeploymentState) {
  if (state === "failed" || state === "cancelled") return "danger";
  if (state === "succeeded") return "success";
  return "active";
}

export function stageIndex(state: DeploymentState) {
  return stages.indexOf(stateStage[state]);
}

export function groupDeployments(deployments: Deployment[]) {
  return {
    attention: deployments.filter((item) => item.state !== "succeeded" && item.state !== "cancelled"),
    history: deployments.filter((item) => item.state === "succeeded" || item.state === "cancelled"),
  };
}
