import { request, destructiveRequest } from "./transport";

export type PrivateNetwork = {
  id: string;
  name: string;
  driver: "dispatch_agent" | "laneway" | "laneway_connector" | string;
  config: Record<string, string>;
  details: Record<string, string>;
  enrollmentToken?: string;
  credentialsConfigured?: boolean;
  state: string;
  lastVerifiedAt?: string;
  createdAt: string;
  updatedAt: string;
};

export type LanewayNode = {
  node_id: string;
  network_id: string;
  name: string;
  enabled_capabilities: number;
  ipv4_address?: string;
  ipv6_address?: string;
  enrollment_class: string;
  revoked_at_unix_seconds?: number;
};

export type LanewayEndpointStatus = {
  node_id: string;
  network_id: string;
  node_name: string;
  freshness: string;
  last_reported_at_unix_seconds?: number;
  report?: { product_version: string; platform: string; carrier_state: string; route_state: string };
};

export type LanewayRoute = {
  route_id: string;
  network_id: string;
  node_id: string;
  prefix: string;
  kind: string;
  mode: string;
  metric: number;
  state: string;
};

export type LanewayInventory = {
  network: { network_id: string; name: string; ipv4_pool: string; ipv6_pool?: string; configuration_epoch: number; created_at_unix_seconds: number };
  nodes: LanewayNode[];
  endpointStatuses: LanewayEndpointStatus[];
  routes: LanewayRoute[];
};

export type LanewayNodeInstaller = {
  installation_id: string;
  command: string;
  expires_at_unix_seconds: number;
};

export const networksApi = {
  createPrivateNetwork: (body: {
    name: string;
    driver: string;
    socketPath?: string;
    authority?: string;
    route?: string;
  }) =>
    request<PrivateNetwork>("/api/v1/private-networks", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updatePrivateNetwork: (
    id: string,
    body: {
      name: string;
      driver: string;
      socketPath?: string;
      authority?: string;
      route?: string;
    },
  ) =>
    request<PrivateNetwork>(`/api/v1/private-networks/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  verifyPrivateNetwork: (id: string) =>
    request<PrivateNetwork>(`/api/v1/private-networks/${id}/verify`, {
      method: "POST",
    }),
  rotatePrivateNetworkToken: (id: string) =>
    destructiveRequest<PrivateNetwork>(`/api/v1/private-networks/${id}/rotate-token`, {
      method: "POST",
    }),
  installLanewayConnector: (id: string, bootstrapCommand: string) =>
    request<PrivateNetwork>(
      `/api/v1/private-networks/${id}/install-connector`,
      { method: "POST", body: JSON.stringify({ bootstrapCommand }) },
    ),
  deletePrivateNetwork: (id: string) =>
    destructiveRequest<void>(`/api/v1/private-networks/${id}`, { method: "DELETE" }),
  startLanewayNetworkAuthorization: (body: { name: string; authority: string }) =>
    request<{
      method: "post" | "redirect";
      action: string;
      fields?: Record<string, string>;
    }>("/api/v1/laneway-networks/authorize", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  lanewayNetworkInventory: (id: string) =>
    request<LanewayInventory>(`/api/v1/laneway-networks/${id}/inventory`),
  createLanewayNodeInstaller: (
    id: string,
    body: { name: string; kind: "node" | "connector" | "exit"; installMode: "docker_compose" | "systemd" },
  ) =>
    request<LanewayNodeInstaller>(`/api/v1/laneway-networks/${id}/node-installers`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
  createLanewayRoute: (
    id: string,
    body: { nodeId: string; prefix: string; mode: "nat" | "routed"; metric: number },
  ) =>
    request<LanewayRoute>(`/api/v1/laneway-networks/${id}/routes`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
};
