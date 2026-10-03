import { request, destructiveRequest } from "./transport";
import type { Deployment } from "./deployments";

export type SecretType =
  | "text"
  | "environment_variable"
  | "environment_json"
  | "json"
  | "api_token"
  | "github_token"
  | "ssh_private_key"
  | "registry_password";

export type SecretSource = "local" | "external";

export type Secret = {
  id: string;
  name: string;
  type: SecretType;
  source?: SecretSource;
  environmentVariable: string;
  publicValue?: string;
  externalStoreId?: string;
  externalSecretId?: string;
  externalField?: string;
  createdAt: string;
  updatedAt: string;
};

export type SecretConsumer = {
  id: string;
  kind: string;
  name: string;
  state?: string;
  references: string[];
  applications: { id: string; name: string; target: string; archived: boolean }[];
};

export type SecretUsage = {
  secretId: string;
  consumers: SecretConsumer[];
  archived: SecretConsumer[];
  warnings: string[];
};

export type SecretStore = {
  id: string;
  name: string;
  provider: "ibm_cloud_secrets_manager" | string;
  config: Record<string, string>;
  credentialsConfigured: boolean;
  state: string;
  lastVerifiedAt?: string;
  createdAt: string;
  updatedAt: string;
};

export const secretsApi = {
  secretUsage: () => request<SecretUsage[]>("/api/v1/secrets/usage"),
  secretUsageDeployments: (id: string, before = "") =>
    request<{ items: Deployment[]; next?: string }>(`/api/v1/secrets/${encodeURIComponent(id)}/usage/deployments${before ? `?before=${encodeURIComponent(before)}` : ""}`),
  createSecret: (body: {
    name: string;
    type: SecretType;
    source?: SecretSource;
    environmentVariable: string;
    value?: string;
    generate?: boolean;
    externalStoreId?: string;
    externalSecretId?: string;
    externalField?: string;
  }) =>
    request<Secret>("/api/v1/secrets", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateSecret: (
    id: string,
    body: {
      name: string;
      type: SecretType;
      source?: SecretSource;
      environmentVariable: string;
      value?: string;
      generate?: boolean;
      externalStoreId?: string;
      externalSecretId?: string;
      externalField?: string;
    },
  ) =>
    request<Secret>(`/api/v1/secrets/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deleteSecret: (id: string) =>
    destructiveRequest<void>(`/api/v1/secrets/${id}`, { method: "DELETE" }),
  createSecretStore: (body: {
    name: string;
    provider: string;
    serviceUrl: string;
    iamUrl?: string;
    apiKey: string;
    privateNetworkId?: string;
    serviceAddress?: string;
    iamAddress?: string;
  }) =>
    request<SecretStore>("/api/v1/secret-stores", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateSecretStore: (
    id: string,
    body: {
      name: string;
      provider: string;
      serviceUrl: string;
      iamUrl?: string;
      apiKey?: string;
      privateNetworkId?: string;
      serviceAddress?: string;
      iamAddress?: string;
    },
  ) =>
    request<SecretStore>(`/api/v1/secret-stores/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  verifySecretStore: (id: string) =>
    request<SecretStore>(`/api/v1/secret-stores/${id}/verify`, {
      method: "POST",
    }),
  deleteSecretStore: (id: string) =>
    destructiveRequest<void>(`/api/v1/secret-stores/${id}`, { method: "DELETE" }),
};
