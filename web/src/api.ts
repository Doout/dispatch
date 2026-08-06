export type Project = { id: string; name: string; description: string; createdAt: string };
export type OpenShiftServerConfig = { managed: boolean; serviceAccount: string; serviceAccountNamespace: string; tokenSecret: string; connectedAt?: string };
export type KubernetesServerConfig = { kubeconfigPath?: string; kubeconfigStored: boolean; certificateAuthorityStored: boolean; context?: string; namespace?: string; openShift?: OpenShiftServerConfig };
export type KubernetesServerInput = { source: "stored" | "path" | "openshift"; kubeconfigPath?: string; kubeconfig?: string; certificateAuthority?: string; context?: string; namespace?: string; loginCommand?: string };
export type Server = { id: string; name: string; address: string; runtime: "docker" | "kubernetes" | "openshift"; state: string; agentMode: string; kubernetes?: KubernetesServerConfig; createdAt: string };
export type App = {
  id: string; projectId: string; serverId: string; name: string; sourceRepo: string; branch: string;
  buildType: "dockerfile" | "compose" | "helm"; contextPath: string; dockerfilePath: string; composePath: string;
  helmChart?: string; helmVersion?: string; helmRepository?: string; helmValues?: string; helmNamespace?: string; helmRelease?: string;
  containerPort: number; domain: string; template: boolean; state: string; createdAt: string;
};
export type DeploymentState = "queued" | "fetching" | "building" | "starting" | "checking" | "routing" | "succeeded" | "failed" | "cancelled";
export type Deployment = {
  id: string; appId: string; commitSha: string; specDigest: string; state: DeploymentState; message: string;
  createdAt: string; startedAt?: string; finishedAt?: string; app?: App; server?: Server;
};
export type PreviewGroupBinding = { source: string; helmValuePath: string };
export type PreviewGroupComponent = {
  id?: string; groupId?: string; appId: string; alias: string; repository: string; defaultBranch: string;
  entrypoint: boolean; dependsOn: string[]; bindings: PreviewGroupBinding[]; preDeployHook?: string; postDeployHook?: string;
};
export type PreviewGroup = {
  id: string; name: string; command: string; enabled: boolean; components: PreviewGroupComponent[]; createdAt: string; updatedAt: string;
};
export type PreviewGroupSource = {
  id: string; runId: string; groupId: string; componentId: string; alias: string; repository: string; pullRequest?: number;
  headRef: string; sha: string; baseRef?: string; defaultBranch: boolean; statusCommentId?: string; closedAt?: string;
};
export type PreviewGroupRunComponent = {
  id: string; runId: string; componentId: string; alias: string; generatedAppId?: string; deploymentId?: string;
  state: string; url?: string; outputs?: Record<string, string>; message?: string;
};
export type PreviewGroupAttempt = { id: string; runId: string; sequence: number; state: string; message?: string; createdAt: string; finishedAt?: string };
export type PreviewGroupRun = {
  id: string; groupId: string; slug: string; namespace: string; state: string; message?: string; entrypointUrl?: string;
  attempt: number; sources: PreviewGroupSource[]; components: PreviewGroupRunComponent[]; attempts: PreviewGroupAttempt[]; group?: PreviewGroup;
  createdAt: string; updatedAt: string; closedAt?: string;
};
export type DeploymentLog = { id: number; deploymentId: string; level: string; message: string; createdAt: string };
export type Overview = {
  demo: boolean; projects: Project[]; servers: Server[]; apps: App[]; deployments: Deployment[];
  eventTriggers: EventTrigger[]; previews: PreviewEnvironment[];
  previewGroups: PreviewGroup[]; previewGroupRuns: PreviewGroupRun[];
};
export type AuthStatus = { setupRequired: boolean; tokenLoginAvailable: boolean };
export type EventTrigger = { id: string; appId: string; provider: "github"; repository: string; command: string; enabled: boolean; preDeployHook?: string; postDeployHook?: string; createdAt: string; updatedAt: string };
export type PreviewEnvironment = { id: string; appId: string; repository: string; pullRequestNumber: number; state: string; url?: string; deploymentId?: string; message?: string; updatedAt: string };

const tokenKey = "dispatch-admin-token";
export const getToken = () => sessionStorage.getItem(tokenKey) ?? "";
export const setToken = (value: string) => value ? sessionStorage.setItem(tokenKey, value) : sessionStorage.removeItem(tokenKey);

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const response = await fetch(path, {
    ...init,
    headers: {
      Accept: "application/json",
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...init?.headers,
    },
  });
  if (!response.ok) {
    const problem = await response.json().catch(() => ({ title: "Request failed", detail: response.statusText }));
    const error = new Error(problem.detail ?? problem.title ?? "Request failed") as Error & { status?: number };
    error.status = response.status;
    throw error;
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export const api = {
  authStatus: () => request<AuthStatus>("/api/v1/auth/status"),
  setupAdmin: (username: string, password: string) => request<{ username: string }>("/api/v1/auth/setup", { method: "POST", body: JSON.stringify({ username, password }) }),
  login: (username: string, password: string) => request<{ token: string }>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  overview: () => request<Overview>("/api/v1/overview"),
  logs: (id: string) => request<DeploymentLog[]>(`/api/v1/deployments/${id}/logs`),
  deploy: (appId: string, commitSha: string) => request<Deployment>(`/api/v1/apps/${appId}/deployments`, { method: "POST", body: JSON.stringify({ commitSha }) }),
  cleanup: (appId: string) => request<void>(`/api/v1/apps/${appId}/cleanup`, { method: "POST" }),
  cancel: (id: string) => request<void>(`/api/v1/deployments/${id}/cancel`, { method: "POST" }),
  createProject: (body: { name: string; description: string }) => request<Project>("/api/v1/projects", { method: "POST", body: JSON.stringify(body) }),
  updateProject: (id: string, body: { name: string; description: string }) => request<Project>(`/api/v1/projects/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  deleteProject: (id: string) => request<void>(`/api/v1/projects/${id}`, { method: "DELETE" }),
  createServer: (body: { name: string; address?: string; runtime: "docker" | "kubernetes" | "openshift"; agentMode?: string; kubernetes?: KubernetesServerInput }) => request<Server>("/api/v1/servers", { method: "POST", body: JSON.stringify(body) }),
  updateServer: (id: string, body: { name: string; address?: string; kubernetes?: KubernetesServerInput }) => request<Server>(`/api/v1/servers/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  repairServer: (id: string, loginCommand: string) => request<Server>(`/api/v1/servers/${id}/repair`, { method: "POST", body: JSON.stringify({ loginCommand }) }),
  deleteServer: (id: string) => request<void>(`/api/v1/servers/${id}`, { method: "DELETE" }),
  createApp: (body: Record<string, unknown>) => request<App>("/api/v1/apps", { method: "POST", body: JSON.stringify(body) }),
  deleteApp: (id: string) => request<void>(`/api/v1/apps/${id}`, { method: "DELETE" }),
  createEventTrigger: (appId: string, body: { provider: "github"; repository: string; command: string; enabled: boolean; preDeployHook?: string; postDeployHook?: string }) => request<EventTrigger>(`/api/v1/apps/${appId}/event-triggers`, { method: "POST", body: JSON.stringify(body) }),
  updateEventTrigger: (id: string, body: { command: string; enabled: boolean; preDeployHook: string; postDeployHook: string }) => request<EventTrigger>(`/api/v1/event-triggers/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  eventTriggers: (appId = "") => request<EventTrigger[]>(`/api/v1/event-triggers${appId ? `?appId=${encodeURIComponent(appId)}` : ""}`),
  deleteEventTrigger: (id: string) => request<void>(`/api/v1/event-triggers/${id}`, { method: "DELETE" }),
  previews: (appId = "") => request<PreviewEnvironment[]>(`/api/v1/preview-environments${appId ? `?appId=${encodeURIComponent(appId)}` : ""}`),
  createPreviewGroup: (body: { name: string; command: string; enabled?: boolean; components: PreviewGroupComponent[] }) => request<PreviewGroup>("/api/v1/preview-groups", { method: "POST", body: JSON.stringify(body) }),
  updatePreviewGroup: (id: string, body: { name: string; command: string; enabled?: boolean; components: PreviewGroupComponent[] }) => request<PreviewGroup>(`/api/v1/preview-groups/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  deletePreviewGroup: (id: string) => request<void>(`/api/v1/preview-groups/${id}`, { method: "DELETE" }),
  previewGroupRun: (id: string) => request<PreviewGroupRun>(`/api/v1/preview-group-runs/${id}`),
  cleanupPreviewGroupRun: (id: string) => request<PreviewGroupRun>(`/api/v1/preview-group-runs/${id}/cleanup`, { method: "POST" }),
};
