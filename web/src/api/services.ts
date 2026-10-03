import { request, destructiveRequest } from "./transport";

export type ServiceField = { value?: string; sensitive: boolean; configured: boolean; secretRef?: string };

export type ServiceTemplate = { id: string; name: string; projectId: string; description: string; serviceType: "postgresql" | "generic"; provider?: "docker" | "helm" | "neon" | "script"; neon?: NeonProvision; inputs: Record<string, { label?: string; description?: string; type?: "string" | "secret" | "service"; required?: boolean; serviceType?: "postgresql" }>; outputs: Record<string, { sensitive?: boolean }>; configSha: string; managedBy: "dispatch" | "gitops"; revision?: number; configSourceId?: string; document?: string };

export type ServiceProvisionTarget = { provider: "docker" | "helm" | "neon"; providerRef?: string; serverId: string; resourceName: string; network?: string; namespace?: string };

export type ServiceProvisionRun = { target?: ServiceProvisionTarget; id: string; templateId: string; projectId: string; serviceName: string; serviceId?: string; state: "queued" | "running" | "succeeded" | "failed"; phase?: string; error?: string; createdAt: string; startedAt?: string; finishedAt?: string };

export type ServiceCheck = { state: "succeeded" | "failed" | "untested"; message: string; location: string; checkedAt: string; durationMs: number };

export type ServiceConnection = {
 id: string; projectId: string; name: string; description: string; type: "postgresql" | "generic";
 templateId?: string; templateName?: string; templateConfigSha?: string; provisionRunId?: string; provisionTarget?: ServiceProvisionTarget;
 fields: Record<string, ServiceField>; availableFields: string[]; revision: number;
 probeHost?: string; probePort?: number; check?: ServiceCheck;
 consumers: { appId: string; appName: string; alias: string; appliedRevision: number; redeploymentRequired: boolean }[];
};

export type ServiceInput = {
 projectId: string; name: string; description: string; type: ServiceConnection["type"]; revision?: number;
 connectionUrl?: string; probeHost?: string; probePort?: number;
 fields: Record<string, { value?: string; sensitive?: boolean; secretRef?: string; remove?: boolean }>;
};

export type ServiceBinding = {
 alias: string; serviceRef: string; environment?: Record<string,string>; compose?: Record<string,Record<string,string>>;
 helm?: { keys: Record<string,string>; secretNameValues: string[]; keyValues?: Record<string,string> };
};

export type ServiceResource = { replacedByRunId?: string; replacesRunId?: string; policyActorId?: string; policyApprovedAt?: string; providerPhase?: string; previewId?: string; previewAlias?: string; runId: string; projectId: string; serviceId: string; name: string; target: ServiceProvisionTarget; state: "accepted" | "provisioning" | "recovering" | "ready" | "unresolved" | "deleting" | "deleted"; resourceId?: string; policy: "retain" | "suspend" | "delete"; revision: number; operationId: string; message?: string; dependencies?: string[]; recoveryAfter?: string; createdAt: string; updatedAt: string };

export type ServiceResourceInspection = { runId: string; projectId: string; serverId: string; provider: string; resourceId?: string; state: "ready" | "unready" | "absent"; storageRetained: boolean };

export type NeonProvision = { providerRef: string; database: string; dataMode?: "schema-only" | "parent-data"; suspendAfterSeconds?: number };

export type NeonProvider = { id: string; projectId: string; name: string; endpoint: string; neonProjectId: string; parentBranchId: string; credentialRef: string; createdAt: string };

export const servicesApi = {
  services: () => request<ServiceConnection[]>("/api/v1/services"),
  neonProviders: (projectId: string) => request<NeonProvider[]>(`/api/v1/neon-providers?projectId=${encodeURIComponent(projectId)}`),
  deleteNeonProvider: (id: string) => destructiveRequest<void>(`/api/v1/neon-providers/${id}`, { method: "DELETE" }),
  createNeonProvider: (data: Omit<NeonProvider, "id" | "createdAt">) => request<NeonProvider>("/api/v1/neon-providers", { method: "POST", body: JSON.stringify(data) }),
  serviceTemplates: () => request<ServiceTemplate[]>("/api/v1/service-templates"),
  serviceTemplate: (id: string) => request<ServiceTemplate>(`/api/v1/service-templates/${id}`),
  saveServiceTemplate: (id: string | undefined, data: { projectId: string; document: string; configSourceId: string; revision?: number }) => request<{ id: string; revision: number }>(`/api/v1/service-templates${id ? `/${id}` : ""}`, { method: id ? "PUT" : "POST", body: JSON.stringify(data) }),
  deleteServiceTemplate: (id: string, revision: number) => destructiveRequest<void>(`/api/v1/service-templates/${id}?revision=${revision}`, { method: "DELETE" }),
  startServiceProvision: (id: string, data: { name: string; description: string; inputs: Record<string,string>; confirmDataCopy?: string }) => request<ServiceProvisionRun>(`/api/v1/service-templates/${id}/runs`, { method: "POST", body: JSON.stringify(data) }),
  serviceResource: (id: string) => request<ServiceResource>(`/api/v1/service-provision-runs/${id}/resource`),
  inspectServiceResource: (id: string) => request<ServiceResourceInspection>(`/api/v1/service-provision-runs/${id}/resource/inspect`, { method: "POST" }),
  recoverServiceResource: (id: string, action: "reconcile" | "retry") => request<ServiceResource>(`/api/v1/service-provision-runs/${id}/resource/${action}`, { method: "POST" }),
  neonLifecycle: (id: string, action: "policy-retain" | "policy-suspend" | "policy-delete" | "reset") => destructiveRequest<ServiceResource>(`/api/v1/service-provision-runs/${id}/resource/${action}`, { method: "POST" }),
  deleteServiceResource: (id: string) => destructiveRequest<ServiceResource>(`/api/v1/service-provision-runs/${id}/resource/delete`, { method: "POST" }),
  serviceProvisionRun: (id: string) => request<ServiceProvisionRun>(`/api/v1/service-provision-runs/${id}`),
  serviceProvisionRuns: () => request<ServiceProvisionRun[]>("/api/v1/service-provision-runs"),
  saveService: (id: string | undefined, data: ServiceInput) => request<ServiceConnection>(`/api/v1/services${id ? `/${id}` : ""}`, { method: id ? "PUT" : "POST", body: JSON.stringify(data) }),
  deleteService: (id: string) => destructiveRequest<void>(`/api/v1/services/${id}`, { method: "DELETE" }),
  verifyService: (id: string) => request<ServiceCheck>(`/api/v1/services/${id}/verify`, { method: "POST" }),
  serviceBindings: (id: string) => request<ServiceBinding[]>(`/api/v1/apps/${id}/service-bindings`),
  saveServiceBindings: (id: string, bindings: ServiceBinding[]) => request<ServiceBinding[]>(`/api/v1/apps/${id}/service-bindings`, { method: "PUT", body: JSON.stringify(bindings) }),
};
