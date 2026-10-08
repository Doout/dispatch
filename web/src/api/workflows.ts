import { request, destructiveRequest } from "./transport";
import type { ServiceTemplate } from "./services";

export type WorkflowPreviewCleanup = {
 id: string; resourceId: string; reason: "expired" | "removed" | "closed";
 finalState: string; state: "pending" | "blocked" | "succeeded"; error?: string;
 attempts: number; createdAt: string; updatedAt: string;
 services?: {runId: string; alias: string; policy: string; resourceId: string; operationId: string; state: string; error?: string}[];
 apps: { appId: string; serverId: string; jobId: string; previousJobIds?: string[]; state: string; error?: string }[];
};

export type WorkflowResource = {
 previewCleanups?: WorkflowPreviewCleanup[];
  previewTTL?: string;
  previewExpiresAt?: string;
  id: string;
  configSourceId: string;
  apiVersion: string;
  kind: "Application" | "Pipeline" | "ServiceTemplate";
  name: string;
  path: string;
  document: string;
  specDigest: string;
  configSha: string;
  temporary?: boolean;
  previewPullRequests?: { repository: string; number: number; url: string }[];
  active: boolean;
  state: string;
  lastError?: string;
  sourceCount: number;
  jobCount: number;
  stageNames?: string[];
  targetRefs?: string[];
  lastEvaluation?: WorkflowEvaluation;
  createdAt: string;
  updatedAt: string;
};

export type WorkflowPreviewTemplateGitSource = {
  repository: string;
  branch: string;
  path: string;
  commitSha?: string;
  syncedAt?: string;
  lastError?: string;
};

export type WorkflowPreviewTemplate = {
  sourceTrustPolicy?: "same_repository" | "approval_required";
  commentOnOpen?: boolean;
  ttl?: string;
  watchRepositories?: string[];
  gitSource?: WorkflowPreviewTemplateGitSource;
  id: string;
  configSourceId: string;
  githubAppId: string;
  name: string;
  repository: string;
  command: string;
  autoDeploy?: boolean;
  liveReload?: boolean;
  maxAutoRunsPerHour?: number;
  previewUrl: string;
  document: string;
  active: boolean;
  createdAt: string;
  updatedAt: string;
};

export type WorkflowPreviewTrigger = {
  ttl?: string;
  expiresAt?: string;
  templateSource?: WorkflowPreviewTemplateGitSource;
  id: string;
  templateId?: string;
  resourceId: string;
  githubAppId: string;
  repository: string;
  pullRequestNumber: number;
  command: string;
  autoDeploy?: boolean;
  liveReload?: boolean;
  maxAutoRunsPerHour?: number;
  previewUrl?: string;
  linkedPullRequests?: Record<string, number>;
  reportCommentId?: string;
  createdAt: string;
  closedAt?: string;
};

export type WorkflowPreviewTriggerInput = Pick<WorkflowPreviewTrigger, "githubAppId" | "repository" | "pullRequestNumber" | "command" | "autoDeploy" | "liveReload" | "maxAutoRunsPerHour" | "previewUrl" | "ttl">;

export type WorkflowSourceRevision = {
  alias: string;
  repository: string;
  branch: string;
  commitSha: string;
  path?: string;
};

export type WorkflowFeedback = {
  statusContext: string;
  deploymentId: string;
  previewUrl?: string;
  complete: boolean;
  reviewOnSuccess?: string;
  reviewOnFailure?: string;
  targets: { githubAppId: string; repository: string; number: number; commitSha: string; url?: string; status?: string; review?: string; reviewId?: number; error?: string; skipReason?: string }[];
};

export type PreviewSourceTrustDecision = {
  policy: string; digest: string; allowed: boolean; reason: string;
  sources: { alias: string; repository: string; repositoryId: number; headRepository: string; headRepositoryId: number; fork: boolean; pullRequest: number; commitSha: string; githubAppId: string }[];
  credentialScope: string[]; environments: string[]; approvalId?: string; checkedAt: string;
};

export type WorkflowCheckReport = {
  id: string; revisionId: string; resourceId: string; projectId: string; githubAppId: string;
  repository: string; commitSha: string; name: string; kind: string; stage?: string; check?: string;
  previewUrl?: string; externalId: string; checkId?: number; htmlUrl?: string; status?: string; conclusion?: string;
  state: string; error?: string; attempts: number; complete: boolean; updatedAt: string; nextAttemptAt?: string;
};

export type WorkflowRevision = {
  checks?: WorkflowCheckReport[];
  sourceTrust?: PreviewSourceTrustDecision;
  feedback?: WorkflowFeedback;
  id: string;
  resourceId: string;
  configSha: string;
  specDigest: string;
  state: string;
  trigger: string;
  sources: Record<string, WorkflowSourceRevision>;
  outputs?: Record<string, Record<string, string>>;
  error?: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
};

export type WorkflowJobResult = {
  id: string;
  resourceId: string;
  revisionId: string;
  jobName: string;
  fingerprint: string;
  reusedFromId?: string;
  state: string;
  sources: Record<string, WorkflowSourceRevision>;
  outputs?: Record<string, string>;
  log?: string;
  error?: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
};

export type WorkflowStageRun = {
  id: string;
  revisionId: string;
  stageName: string;
  targetRef: string;
  state: string;
  approval: string;
  deploymentIds?: string[];
  deploymentResults?: WorkflowDeploymentResult[];
  checkRuns?: Record<string, string>;
  error?: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
};

export type WorkflowDeploymentResult = {
  deploymentName: string;
  appId: string;
  deploymentId: string;
  outcome: "unchanged" | "deployed";
  reason?: string;
  checkedAt: string;
};

export type WorkflowEvaluation = {
  resourceId: string;
  baselineRevisionId: string;
  sources: Record<string, WorkflowSourceRevision>;
  results: WorkflowDeploymentResult[];
  checkedAt: string;
};

export type WorkflowTopologyColumn = { id: string; label: string };

export type WorkflowTopologyNode = {
  id: string;
  column: string;
  kind: "source" | "job" | "finally" | "deployment" | "stage" | string;
  label: string;
  detail?: string;
  state?: string;
  href?: string;
  metadata?: Record<string, string>;
};

export type WorkflowTopologyEdge = { from: string; to: string; kind: string };

export type WorkflowTopology = {
  columns: WorkflowTopologyColumn[];
  nodes: WorkflowTopologyNode[];
  edges: WorkflowTopologyEdge[] | null;
};

export const workflowsApi = {
  previewSourceTrust: (id: string) => request<PreviewSourceTrustDecision>(`/api/v1/workflow/revisions/${id}/source-trust`),
  approvePreviewSourceTrust: (id: string, confirmDigest: string, expiresAt: string) => request(`/api/v1/workflow/revisions/${id}/source-trust/approvals`, { method: "POST", body: JSON.stringify({ confirmDigest, expiresAt }) }),
  revokePreviewSourceTrust: (id: string, approvalId: string) => request(`/api/v1/workflow/revisions/${id}/source-trust/approvals/${approvalId}`, { method: "DELETE" }),
  workflowRevision: (id: string) => request<WorkflowRevision>(`/api/v1/workflow/revisions/${id}`),
  workflowRevisions: (resourceId: string) => request<WorkflowRevision[]>(`/api/v1/workflow/revisions?${new URLSearchParams({ resourceId })}`),
  workflowPreviewTriggers: () =>
    request<WorkflowPreviewTrigger[]>("/api/v1/workflow/preview-triggers"),
  createWorkflowPreviewTemplate: (body: Omit<WorkflowPreviewTemplate, "id" | "createdAt" | "updatedAt">) =>
    request<WorkflowPreviewTemplate>("/api/v1/workflow/preview-templates", { method: "POST", body: JSON.stringify(body) }),
  updateWorkflowPreviewTemplate: (id: string, body: Omit<WorkflowPreviewTemplate, "id" | "createdAt" | "updatedAt">) =>
    request<WorkflowPreviewTemplate>(`/api/v1/workflow/preview-templates/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  syncWorkflowPreviewTemplate: (id: string) =>
    request<WorkflowPreviewTemplate>(`/api/v1/workflow/preview-templates/${id}/sync`, { method: "POST" }),
  deleteWorkflowPreviewTemplate: (id: string) =>
    destructiveRequest<void>(`/api/v1/workflow/preview-templates/${id}`, { method: "DELETE" }),
  createTemporaryWorkflowResource: (body: { configSourceId: string; document: string; previewId?: string }) =>
    request<WorkflowResource>("/api/v1/workflow/temporary-resources", { method: "POST", body: JSON.stringify(body) }),
  importWorkflowPreviewDocument: (body: { githubAppId: string; repository: string; pullRequestNumber: number; path: string }) =>
    request<{ document: string; path: string; headSha: string }>("/api/v1/workflow/temporary-resources/import", { method: "POST", body: JSON.stringify(body) }),
  updateTemporaryWorkflowResource: (id: string, document: string) =>
    request<WorkflowResource>(`/api/v1/workflow/temporary-resources/${id}`, { method: "PUT", body: JSON.stringify({ document }) }),
  deleteTemporaryWorkflowResource: (id: string) =>
    destructiveRequest<void>(`/api/v1/workflow/temporary-resources/${id}`, { method: "DELETE" }),
  createWorkflowPreviewTrigger: (resourceId: string, body: WorkflowPreviewTriggerInput) =>
    request<WorkflowPreviewTrigger>(`/api/v1/workflow/temporary-resources/${resourceId}/preview-trigger`, { method: "POST", body: JSON.stringify(body) }),
  updateWorkflowPreviewTrigger: (id: string, body: WorkflowPreviewTriggerInput) =>
    request<WorkflowPreviewTrigger>(`/api/v1/workflow/preview-triggers/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  activateWorkflowResource: (id: string) =>
    request<{ resource: WorkflowResource; revision?: WorkflowRevision }>(
      `/api/v1/workflow/resources/${id}/activate`,
      { method: "POST" },
    ),
  deactivateWorkflowResource: (id: string) =>
    request<WorkflowResource>(`/api/v1/workflow/resources/${id}/deactivate`, {
      method: "POST",
    }),
  runWorkflowResource: (id: string) =>
    request<WorkflowRevision>(`/api/v1/workflow/resources/${id}/runs`, {
      method: "POST",
    }),
  workflowTopology: (id: string) =>
    request<WorkflowTopology>(`/api/v1/workflow/resources/${id}/topology`),
  workflowJobs: (revisionId: string) =>
    request<WorkflowJobResult[]>(
      `/api/v1/workflow/revisions/${revisionId}/jobs`,
    ),
  workflowStages: (revisionId: string) =>
    request<WorkflowStageRun[]>(
      `/api/v1/workflow/revisions/${revisionId}/stages`,
    ),
  approveWorkflowStage: (stageId: string) =>
    request<WorkflowStageRun>(`/api/v1/workflow/stages/${stageId}/approve`, {
      method: "POST",
    }),
};
