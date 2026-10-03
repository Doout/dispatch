import { request, destructiveRequest } from "./transport";

export type PreviewGroupBinding = { source: string; helmValuePath: string };

export type PreviewGroupComponent = {
  id?: string;
  groupId?: string;
  appId: string;
  alias: string;
  repository: string;
  defaultBranch: string;
  entrypoint: boolean;
  dependsOn: string[];
  bindings: PreviewGroupBinding[];
  preDeployHook?: string;
  postDeployHook?: string;
  secretIds: string[];
};

export type PreviewGroup = {
  id: string;
  name: string;
  githubAppId?: string;
  command: string;
  enabled: boolean;
  components: PreviewGroupComponent[];
  createdAt: string;
  updatedAt: string;
};

export type PreviewGroupSource = {
  id: string;
  runId: string;
  groupId: string;
  componentId: string;
  alias: string;
  repository: string;
  pullRequest?: number;
  headRef: string;
  sha: string;
  baseRef?: string;
  defaultBranch: boolean;
  statusCommentId?: string;
  closedAt?: string;
};

export type PreviewGroupRunComponent = {
  id: string;
  runId: string;
  componentId: string;
  alias: string;
  generatedAppId?: string;
  deploymentId?: string;
  state: string;
  url?: string;
  outputs?: Record<string, string>;
  message?: string;
};

export type PreviewGroupAttempt = {
  id: string;
  runId: string;
  sequence: number;
  state: string;
  message?: string;
  createdAt: string;
  finishedAt?: string;
};

export type PreviewGroupRun = {
  id: string;
  groupId: string;
  slug: string;
  namespace: string;
  state: string;
  message?: string;
  entrypointUrl?: string;
  attempt: number;
  sources: PreviewGroupSource[];
  components: PreviewGroupRunComponent[];
  attempts: PreviewGroupAttempt[];
  group?: PreviewGroup;
  createdAt: string;
  updatedAt: string;
  closedAt?: string;
};

export type PreviewEnvironment = {
  id: string;
  appId: string;
  repository: string;
  pullRequestNumber: number;
  state: string;
  url?: string;
  deploymentId?: string;
  message?: string;
  updatedAt: string;
};

export const previewsApi = {
  previews: (appId = "") =>
    request<PreviewEnvironment[]>(
      `/api/v1/preview-environments${appId ? `?appId=${encodeURIComponent(appId)}` : ""}`,
    ),
  createPreviewGroup: (body: {
    name: string;
    githubAppId: string;
    command: string;
    enabled?: boolean;
    components: PreviewGroupComponent[];
  }) =>
    request<PreviewGroup>("/api/v1/preview-groups", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updatePreviewGroup: (
    id: string,
    body: {
      name: string;
      githubAppId: string;
      command: string;
      enabled?: boolean;
      components: PreviewGroupComponent[];
    },
  ) =>
    request<PreviewGroup>(`/api/v1/preview-groups/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deletePreviewGroup: (id: string) =>
    destructiveRequest<void>(`/api/v1/preview-groups/${id}`, { method: "DELETE" }),
  previewGroupRun: (id: string) =>
    request<PreviewGroupRun>(`/api/v1/preview-group-runs/${id}`),
  cleanupPreviewGroupRun: (id: string) =>
    destructiveRequest<PreviewGroupRun>(`/api/v1/preview-group-runs/${id}/cleanup`, {
      method: "POST",
    }),
};
