import { request } from "./transport";
import type { Server } from "./servers";
import type { HealthCheck, HealthPolicy, App } from "./applications";
import type { WorkflowTopology } from "./workflows";

export type DeploymentHealth = { state: string; simulated?: boolean; policy: HealthPolicy; checks: { check: HealthCheck; state: string; attempts: number; failures: number; message: string; httpStatus?: number }[]; startedAt?: string; finishedAt?: string };

export type DeploymentReview = { expectedAppName: string; projectId: string; appSpecDigest: string; bindingsDigest: string; serviceRevisions: Record<string, number> };

export type DeploymentState =
  | "queued"
  | "fetching"
  | "building"
  | "starting"
  | "checking"
  | "routing"
  | "succeeded"
  | "failed"
  | "cancelled";

export type Deployment = {
  health?: DeploymentHealth;
  id: string;
  appId: string;
  commitSha: string;
  specDigest: string;
  state: DeploymentState;
  message: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  outputs?: Record<string, string>;
  app?: App;
  server?: Server;
};

export type DeploymentLog = {
  id: number;
  deploymentId: string;
  level: string;
  message: string;
  createdAt: string;
};

export type AppliedValue = { path: string; value: unknown; redacted?: boolean; source?: string; renderedValue?: string };

export type DeploymentTopology = {
  topology: WorkflowTopology;
  target: string;
  runtime: string;
  namespace: string;
  release: string;
  chart?: string;
  values: AppliedValue[];
  chartValues?: AppliedValue[];
  valuesAnalyzed?: boolean;
  valuesNotes?: string[];
  live: boolean;
  warning?: string;
};

export type DeploymentManifest = {
  name: string;
  kind: string;
  apiVersion: string;
  document: string;
};

export type DeploymentManifestOrigin = {
  managed: boolean;
  repository?: string;
  branch?: string;
  configPath?: string;
  configRevision?: string;
  chartRepository?: string;
  chartPath?: string;
};

export type DeploymentManifests = {
  target: string;
  namespace: string;
  release: string;
  origin: DeploymentManifestOrigin;
  manifests: DeploymentManifest[];
  warning?: string;
};

export type DeploymentResourceLog = {
  container: string;
  content?: string;
  error?: string;
};

export type DeploymentResourceEvent = {
  type: string;
  reason: string;
  message: string;
  count: number;
  lastSeen: string;
};

export type DeploymentResource = {
  manifest: DeploymentManifest;
  loggable: boolean;
  defaultContainer?: string;
  logs: DeploymentResourceLog[];
  events: DeploymentResourceEvent[];
  warning?: string;
};

export type DeploymentHistoryPage = {
  items: Deployment[];
  next?: string;
  repeats?: Record<string, string>;
};

export type DeploymentComparison = {
 fromId: string; toId: string; available: boolean; hidden: number; truncated: boolean; message: string;
 changes: { path: string; kind: "added" | "removed" | "changed"; before: unknown; after: unknown }[];
};

export const deploymentsApi = {
  logs: (id: string) =>
    request<DeploymentLog[]>(`/api/v1/deployments/${id}/logs`),
  deployment: (id: string) => request<Deployment>(`/api/v1/deployments/${encodeURIComponent(id)}`),
  applicationHistory: (id: string, before = "") => request<DeploymentHistoryPage>(`/api/v1/apps/${id}/deployment-history${before ? `?before=${encodeURIComponent(before)}` : ""}`),
  compareDeployments: (to: string, from: string) => request<DeploymentComparison>(`/api/v1/deployments/${to}/compare?from=${encodeURIComponent(from)}`),
  deploymentTopology: (id: string, chartValues = false) =>
    request<DeploymentTopology>(`/api/v1/deployments/${id}/topology${chartValues ? "?values=chart" : ""}`),
  deploymentManifests: (id: string) =>
    request<DeploymentManifests>(`/api/v1/deployments/${id}/manifests`),
  deploymentResource: (deploymentID: string, kind: string, name: string) =>
    request<DeploymentResource>(
      `/api/v1/deployments/${deploymentID}/resources/${encodeURIComponent(kind)}/${encodeURIComponent(name)}`,
    ),
  deploy: (appId: string, commitSha: string, review?: DeploymentReview) =>
    request<Deployment>(`/api/v1/apps/${appId}/deployments`, {
      method: "POST",
      body: JSON.stringify({ commitSha, review }),
    }),
  cancel: (id: string) =>
    request<void>(`/api/v1/deployments/${id}/cancel`, { method: "POST" }),
};
