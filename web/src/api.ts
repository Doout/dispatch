export type Project = { id: string; name: string; description: string; createdAt: string };
export type Server = { id: string; name: string; address: string; runtime: string; state: string; agentMode: string; createdAt: string };
export type App = {
  id: string; projectId: string; serverId: string; name: string; sourceRepo: string; branch: string;
  buildType: "dockerfile" | "compose"; contextPath: string; dockerfilePath: string; composePath: string;
  containerPort: number; domain: string; state: string; createdAt: string;
};
export type DeploymentState = "queued" | "fetching" | "building" | "starting" | "checking" | "routing" | "succeeded" | "failed" | "cancelled";
export type Deployment = {
  id: string; appId: string; commitSha: string; specDigest: string; state: DeploymentState; message: string;
  createdAt: string; startedAt?: string; finishedAt?: string; app?: App; server?: Server;
};
export type DeploymentLog = { id: number; deploymentId: string; level: string; message: string; createdAt: string };
export type Overview = { demo: boolean; projects: Project[]; servers: Server[]; apps: App[]; deployments: Deployment[] };

const tokenKey = "dispatch-admin-token";
export const getToken = () => sessionStorage.getItem(tokenKey) ?? "";
export const setToken = (value: string) => sessionStorage.setItem(tokenKey, value);

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
  overview: () => request<Overview>("/api/v1/overview"),
  logs: (id: string) => request<DeploymentLog[]>(`/api/v1/deployments/${id}/logs`),
  deploy: (appId: string, commitSha: string) => request<Deployment>(`/api/v1/apps/${appId}/deployments`, { method: "POST", body: JSON.stringify({ commitSha }) }),
  cancel: (id: string) => request<void>(`/api/v1/deployments/${id}/cancel`, { method: "POST" }),
  createProject: (body: { name: string; description: string }) => request<Project>("/api/v1/projects", { method: "POST", body: JSON.stringify(body) }),
  createServer: (body: { name: string; address: string; runtime: string }) => request<Server>("/api/v1/servers", { method: "POST", body: JSON.stringify(body) }),
  createApp: (body: Record<string, unknown>) => request<App>("/api/v1/apps", { method: "POST", body: JSON.stringify(body) }),
};
