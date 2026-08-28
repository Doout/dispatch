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
  return value && value.length > length ? value.slice(0, length) : value || "-";
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
  const groups = { active: [] as Deployment[], failed: [] as Deployment[], latest: [] as Deployment[], history: [] as Deployment[] };
  for (const cluster of clusterDeploymentsByApplication(deployments)) {
    const [latest, ...earlier] = cluster.deployments;
    if (!latest) continue;
    if (!isTerminal(latest.state)) {
      groups.active.push(latest);
    } else if (latest.state === "failed") {
      groups.failed.push(latest);
    } else {
      groups.latest.push(latest);
    }
    groups.history.push(...earlier);
  }
  return groups;
}

function isTerminal(state: DeploymentState) {
  return state === "succeeded" || state === "failed" || state === "cancelled";
}

export function clusterDeploymentsByApplication(deployments: Deployment[]) {
  const clusters = new Map<string, Deployment[]>();
  for (const deployment of deployments) {
    const key = deployment.appId || deployment.app?.id || deployment.id;
    clusters.set(key, [...(clusters.get(key) ?? []), deployment]);
  }
  return [...clusters.entries()].map(([key, items]) => ({
    key,
    deployments: [...items].sort((left, right) => new Date(right.createdAt).getTime() - new Date(left.createdAt).getTime()),
  })).sort((left, right) => new Date(right.deployments[0].createdAt).getTime() - new Date(left.deployments[0].createdAt).getTime());
}
