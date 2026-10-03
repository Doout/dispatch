import { request, destructiveRequest } from "./transport";
import type { WorkflowTopology } from "./workflows";

export type OpenShiftServerConfig = {
  managed: boolean;
  serviceAccount: string;
  serviceAccountNamespace: string;
  tokenSecret: string;
  connectedAt?: string;
};

export type KubernetesServerConfig = {
  kubeconfigPath?: string;
  kubeconfigStored: boolean;
  certificateAuthorityStored: boolean;
  context?: string;
  namespace?: string;
  openShift?: OpenShiftServerConfig;
};

export type KubernetesServerInput = {
  source: "stored" | "path" | "openshift";
  kubeconfigPath?: string;
  kubeconfig?: string;
  certificateAuthority?: string;
  context?: string;
  namespace?: string;
  loginCommand?: string;
};

export type RelayServerConfig = {
  accessTokenConfigured: boolean;
  pendingEvents: number;
  oldestPendingAt?: string;
  lastConnectedAt?: string;
  lastError?: string;
};

export type RoutingConfig = { baseDomain: string; entryPoint: string; tlsResolver?: string; requireTls: boolean; composeService?: string };

export type Server = {
  routing?: RoutingConfig;
  id: string;
  name: string;
  address: string;
  runtime: "docker" | "kubernetes" | "openshift" | "builder" | "relay";
  state: string;
  agentMode: string;
  kubernetes?: KubernetesServerConfig;
  relay?: RelayServerConfig;
  builder?: { sshSecretId: string; hostKey: string; maxConcurrent: number };
  createdAt: string;
};

export type RelayWebhook = {
  id: string;
  serverId: string;
  name: string;
  provider: string;
  providerConnectionId?: string;
  remoteId: string;
  url: string;
  state: string;
  lastDeliveryAt?: string;
  lastError?: string;
  createdAt: string;
  updatedAt: string;
};

export type RelaySSHInstallInput = {
  host: string;
  port: number;
  user: string;
  authType: "password" | "private_key";
  password?: string;
  privateKey?: string;
  secretId?: string;
  privateKeyPassword?: string;
  sudoPassword?: string;
  hostKeyFingerprint: string;
  relayUrl: string;
  relayToken: string;
  installMode?: "systemd" | "docker";
  relayImage?: string;
};

export type StorageResource = {
  id: string; serverId: string; kind: string; name: string; namespace?: string;
  identity: string; projectId?: string; ownerKind?: string; ownerId?: string;
  ownership: string; orphaned: boolean; policy: "retain" | "destroy"; state: string;
  consumers: { id: string; mount?: string; active: boolean }[];
  revision: number; observedAt: string; message?: string;
};

export const serversApi = {
  updateServerRouting: (id: string, routing: RoutingConfig | null) => request<Server>(`/api/v1/servers/${id}/routing`, { method: "PUT", body: JSON.stringify({ routing }) }),
  storage: (serverId: string) => request<StorageResource[]>(`/api/v1/storage?serverId=${encodeURIComponent(serverId)}`),
  reconcileStorage: (serverId: string) => request<StorageResource[]>(`/api/v1/servers/${encodeURIComponent(serverId)}/storage/reconcile`, { method: "POST" }),
  storagePolicy: (id: string, revision: number, policy: "retain" | "destroy") => request<StorageResource>(`/api/v1/storage/${id}/policy`, { method: "PUT", body: JSON.stringify({ revision, policy }) }),
  deleteStorage: (id: string) => destructiveRequest<void>(`/api/v1/storage/${id}`, { method: "DELETE" }),
  serverTopology: (id: string) =>
    request<WorkflowTopology>(`/api/v1/servers/${id}/topology`, {
      cache: "no-store",
    }),
  createServer: (body: {
    name: string;
    address?: string;
    runtime: "docker" | "kubernetes" | "openshift" | "builder" | "relay";
    agentMode?: string;
    kubernetes?: KubernetesServerInput;
    relay?: { accessToken?: string };
    builder?: { sshSecretId: string; hostKey: string; maxConcurrent: number };
  }) =>
    request<Server>("/api/v1/servers", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  scanRelaySSHHost: (host: string, port: number) =>
    request<{ fingerprint: string }>("/api/v1/relay/ssh/scan", {
      method: "POST",
      body: JSON.stringify({ host, port }),
    }),
  scanBuilderSSHHost: (address: string) =>
    request<{ fingerprint: string; hostKey: string }>("/api/v1/builders/ssh/scan", {
      method: "POST",
      body: JSON.stringify({ address }),
    }),
  installRelayOverSSH: (body: RelaySSHInstallInput) =>
    request<{ status: string; output: string }>("/api/v1/relay/ssh/install", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateServer: (
    id: string,
    body: {
      name: string;
      address?: string;
      kubernetes?: KubernetesServerInput;
      relay?: { accessToken?: string };
      builder?: { sshSecretId: string; hostKey: string; maxConcurrent: number };
    },
  ) =>
    request<Server>(`/api/v1/servers/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  verifyRelayServer: (id: string) =>
    request<Server>(`/api/v1/servers/${id}/relay/verify`, { method: "POST" }),
  relayWebhooks: (id: string) =>
    request<RelayWebhook[]>(`/api/v1/servers/${id}/relay/webhooks`),
  createRelayWebhook: (
    id: string,
    body: { name: string; provider: string; providerConnectionId?: string },
  ) =>
    request<RelayWebhook>(`/api/v1/servers/${id}/relay/webhooks`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
  deleteRelayWebhook: (serverId: string, id: string) =>
    destructiveRequest<void>(`/api/v1/servers/${serverId}/relay/webhooks/${id}`, {
      method: "DELETE",
    }),
  repairServer: (id: string, loginCommand: string) =>
    request<Server>(`/api/v1/servers/${id}/repair`, {
      method: "POST",
      body: JSON.stringify({ loginCommand }),
    }),
  deleteServer: (id: string) =>
    destructiveRequest<void>(`/api/v1/servers/${id}`, { method: "DELETE" }),
};
