import { request, destructiveRequest } from "./transport";

export type ApplicationRoute = { appId: string; projectId: string; serverId: string; hostname: string; entryPoint: string; tlsResolver?: string; requireTls: boolean; requestedDeploymentId: string; deploymentId?: string; destination?: string; previousDeploymentId?: string; previousDestination?: string; state: string; message: string; dns: string; certificate: { state: string; message: string; expiresAt?: string; checkedAt?: string }; publishedAt?: string; updatedAt: string };

export type HealthCheck = { id: string; kind: "container" | "http" | "tcp" | "tls"; scope: "workload" | "route" | "certificate"; service?: string; port?: number; path?: string };

export type HealthPolicy = { timeoutSeconds: number; checkTimeoutSeconds: number; intervalSeconds: number; failureThreshold: number; checks: HealthCheck[] };

export type App = {
  healthPolicy?: HealthPolicy;
  id: string;
  projectId: string;
  serverId: string;
  name: string;
  sourceRepo: string;
  branch: string;
  sourceAuthType?: "github_app" | "github_token" | "ssh_key";
  sourceCredentialId?: string;
  buildType: "dockerfile" | "compose" | "helm";
  contextPath: string;
  dockerfilePath: string;
  composePath: string;
  helmChart?: string;
  helmVersion?: string;
  helmRepository?: string;
  helmValues?: string;
  helmNamespace?: string;
  helmRelease?: string;
  preDeployHook?: string;
  postDeployHook?: string;
  hookSecretIds?: string[];
  containerPort: number;
  domain: string;
  template: boolean;
  generated?: boolean;
  state: string;
  createdAt: string;
};

export type HelmValue =
  string | number | boolean | null | HelmValue[] | { [key: string]: HelmValue };

export type HelmValuesProfile = {
  path: string;
  name: string;
  values: Record<string, HelmValue>;
  valuesYaml: string;
};

export type HelmChartInspection = {
  repository: string;
  branch: string;
  chartPath: string;
  chart: {
    name: string;
    description?: string;
    version?: string;
    appVersion?: string;
    type?: string;
  };
  defaults: Record<string, HelmValue>;
  valuesYaml: string;
  schema?: unknown;
  profiles: HelmValuesProfile[];
};

export type HelmValuesConfiguration = HelmChartInspection & {
  overrides: Record<string, HelmValue>;
};

export type ApplicationSyncStatus = {
 appId: string; deploymentId?: string; supported: boolean; reapplyAvailable: boolean;
 configuration: {state: string; message: string; sourceId?: string; lastSyncedAt?: string; lastEvaluatedAt?: string};
 revision: {state: string; applied?: string; observed?: string; evaluatedCommit?: string; evaluatedAt?: string};
 drift: {deploymentId?: string; state: string; health: string; message: string; healthMessage?: string; checkedAt?: string; lastSuccessfulCheckAt?: string; location: string;
 resources: {apiVersion: string; kind: string; namespace?: string; name: string; state: string; health: string; message?: string; truncated?: boolean; differences: {path: string; expected: unknown; actual: unknown; redacted?: boolean}[]}[]};
 actions: {id: string; state: string; message: string; actor: string; createdAt: string}[];
};

export const applicationsApi = {
  applicationRoute: (id: string) => request<ApplicationRoute | null>(`/api/v1/apps/${id}/route`),
  checkApplicationRoute: (id: string) => request<ApplicationRoute>(`/api/v1/apps/${id}/route/check`, { method: "POST" }),
  applicationSync: (id: string) => request<ApplicationSyncStatus>(`/api/v1/apps/${id}/sync`),
  checkApplicationDrift: (id: string) => request<ApplicationSyncStatus>(`/api/v1/apps/${id}/drift/check`,{method:"POST"}),
  reapplyApplication: (id: string,deploymentId: string) => request<ApplicationSyncStatus>(`/api/v1/apps/${id}/reapply`,{method:"POST",body:JSON.stringify({deploymentId})}),
  cleanup: (appId: string) =>
    destructiveRequest<void>(`/api/v1/apps/${appId}/cleanup`, { method: "POST" }),
  createApp: (body: Record<string, unknown>) =>
    request<App>("/api/v1/apps", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  appHelmValues: (id: string) =>
    request<HelmValuesConfiguration>(`/api/v1/apps/${id}/helm-values`),
  updateAppHelmValues: (id: string, overrides: Record<string, HelmValue>) =>
    request<{ overrides: Record<string, HelmValue> }>(
      `/api/v1/apps/${id}/helm-values`,
      { method: "PUT", body: JSON.stringify({ overrides }) },
    ),
  appHealthPolicy: (id: string) => request<HealthPolicy>(`/api/v1/apps/${id}/health-policy`),
  updateAppHealthPolicy: (id: string, policy: HealthPolicy) => request<HealthPolicy>(`/api/v1/apps/${id}/health-policy`, {method:"PUT",body:JSON.stringify(policy)}),
  updateAppHooks: (
    id: string,
    body: {
      preDeployHook: string;
      postDeployHook: string;
      secretIds: string[];
    },
  ) =>
    request<App>(`/api/v1/apps/${id}/hooks`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  inspectHelmSource: (body: {
    projectId: string;
    sourceRepo: string;
    branch: string;
    chartPath: string;
    sourceAuthType?: string;
    sourceCredentialId?: string;
  }) =>
    request<HelmChartInspection>("/api/v1/helm/inspect", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  deleteApp: (id: string) =>
    destructiveRequest<void>(`/api/v1/apps/${id}`, { method: "DELETE" }),
};
