import { request, type Deployment, type DeploymentComparison, type DeploymentReview } from "../api";

type AppliedServiceBinding = { alias: string; serviceId: string; serviceName: string; revision: number };
export type ReleaseNote = { deploymentId: string; notes: string; links: string[]; actor: string; updatedAt: string };
export type ReleaseInfo = { note: ReleaseNote; sourceLinks: string[]; rollbackMessage: string };
export type ReleasePreview = {
 review: DeploymentReview;
  ready: boolean; revision: string; specDigest: string; target: string; namespace: string; release: string; message: string;
  checks: { name: string; state: string; message: string }[];
  resources: { kind: string; name: string; namespace?: string }[];
  bindings: AppliedServiceBinding[]; comparison: DeploymentComparison;
};
export type RollbackPreview = { available: boolean; message: string; deploymentId: string; currentDeploymentId: string; helmRevision?: number; bindings: AppliedServiceBinding[]; resources: { kind: string; name: string }[] };
export type Activity = { id: string; kind: string; state?: string; message: string; actor?: string; deploymentId?: string; revision?: string; createdAt: string };
export type Diagnosis = {
  location: string; checkedAt: string; live: boolean; message: string;
  issues: { resource: string; container?: string; reason: string; restarts?: number; nextStep: string; logs: { container: string; content?: string; error?: string }[]; events: { type: string; reason: string; message: string; count: number; lastSeen: string }[] }[];
  deploymentLogs: { id: number; level: string; message: string; createdAt: string }[];
};
const id = encodeURIComponent;
export const releaseClient = {
  info: (deployment: string) => request<ReleaseInfo>(`/api/v1/deployments/${id(deployment)}/release`),
  saveNote: (deployment: string, notes: string, links: string[]) => request<ReleaseNote>(`/api/v1/deployments/${id(deployment)}/release`, { method: "PUT", body: JSON.stringify({ notes, links }) }),
  preview: (app: string, revision: string) => request<ReleasePreview>(`/api/v1/apps/${id(app)}/release-preview`, { method: "POST", body: JSON.stringify({ revision }) }),
  rollbackPreview: (deployment: string) => request<RollbackPreview>(`/api/v1/deployments/${id(deployment)}/rollback-preview`, { method: "POST" }),
  rollback: (deployment: string, current: string) => request<Deployment>(`/api/v1/deployments/${id(deployment)}/rollback`, { method: "POST", body: JSON.stringify({ confirmDeploymentId: deployment, expectedCurrentDeploymentId: current, confirmDatabaseNotReverted: true }) }),
  activity: (app: string) => request<{ items: Activity[]; message: string }>(`/api/v1/apps/${id(app)}/activity`),
  diagnosis: (deployment: string) => request<Diagnosis>(`/api/v1/deployments/${id(deployment)}/diagnosis`),
};
