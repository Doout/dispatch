import { request, Deployment, DeploymentComparison } from "../api";
export type CatalogStatus = { staleAfterSeconds?: number; configuration: string; configurationMessage?: string; revision: string; drift: string; health: string; supported: boolean; checkedAt?: string; message: string; healthMessage?: string };
export type CatalogItem = { appId: string; appName: string; projectId: string; targetId: string; targetName: string; environment: string; resourceId?: string; resourceName?: string; latest?: Deployment; current?: Deployment; sync?: CatalogStatus };
export const catalogClient = {
 catalog: () => request<{items: CatalogItem[]}>("/api/v1/deployment-catalog"),
 identity: (id: string) => request<CatalogItem>(`/api/v1/deployments/${encodeURIComponent(id)}/identity`),
 search: (params: URLSearchParams) => request<{items: Deployment[]; next?: string}>(`/api/v1/deployment-search?${params}`),
 compare: (to: string, from: string) => request<DeploymentComparison>(`/api/v1/deployments/${encodeURIComponent(to)}/compare-environment?from=${encodeURIComponent(from)}`),
};
