import { request, destructiveRequest } from "./transport";

export type RepositoryStatus = { state: "accessible" | "renamed" | "archived" | "disabled" | "inaccessible" | "deleted" | "identity_changed" | "unavailable"; repositoryId?: number; fullName?: string; detail: string; recovery: string; checkedAt: string };

export type GitHubBranch = { name: string; sha: string; protected: boolean };

export type ConfigSource = {
  id: string;
  projectId: string;
  githubAppId?: string;
  credentialSecretId?: string;
  name: string;
  repository: string;
  repositoryId?: number;
  repositoryStatus?: RepositoryStatus;
  branch: string;
  path: string;
  syncMode: "webhook_poll" | "webhook" | "poll";
  pollIntervalSeconds: number;
  active: boolean;
  state: string;
  lastSeenSha?: string;
  lastSyncedAt?: string;
  lastPolledAt?: string;
  lastError?: string;
  createdAt: string;
  updatedAt: string;
};

export type GitHubAppConnection = {
  id: string;
  name: string;
  webUrl: string;
  apiUrl: string;
  appId: number;
  clientId?: string;
  slug?: string;
  registrationOwner?: string;
  registrationOwnerType?: string;
  installationId?: number;
  installationAccount?: string;
  installationUrl?: string;
  webhookUrl?: string;
  relayWebhookId?: string;
  privateNetworkId?: string;
  privateKeyConfigured: boolean;
  webhookSecretConfigured: boolean;
  state: "needs_installation" | "unverified" | "ready" | string;
  lastVerifiedAt?: string;
  createdAt: string;
  updatedAt: string;
};

export type GitHubAppInstallation = {
  id: number;
  account: string;
  target: string;
 webUrl?: string;
 repositorySelection?: string;
 suspended?: boolean;
 missingPermissions?: GitHubPermissionGap[];
};

export type GitHubRepository = {
  id: number;
  fullName: string;
  name: string;
  owner: string;
  defaultBranch: string;
  private: boolean;
  archived?: boolean;
  disabled?: boolean;
  webUrl: string;
};

export type GitHubAppVerification = {
  slug: string;
  clientId: string;
  registrationOwner: string;
  registrationOwnerType: string;
  installationAccount: string;
  repositorySelection: string;
  repositoryCount: number;
  pushSubscribed: boolean;
  appPermissionsAvailable: boolean;
  installationPermissionsAvailable: boolean;
  missingAppPermissions: GitHubPermissionGap[];
  missingInstallationPermissions: GitHubPermissionGap[];
  tokenPermissionsAvailable: boolean;
  missingTokenPermissions: GitHubPermissionGap[];
};

export type GitHubPermissionGap = { name: string; required: string; granted: string };

export type GitHubAppManifest = {
  action: string;
  manifest: Record<string, unknown>;
};

export const connectionsApi = {
  githubApps: () => request<GitHubAppConnection[]>("/api/v1/github-apps"),
  createGitHubApp: (body: Record<string, unknown>) =>
    request<GitHubAppConnection>("/api/v1/github-apps", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateGitHubApp: (id: string, body: Record<string, unknown>) =>
    request<GitHubAppConnection>(`/api/v1/github-apps/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deleteGitHubApp: (id: string) =>
    destructiveRequest<void>(`/api/v1/github-apps/${id}`, { method: "DELETE" }),
  verifyGitHubApp: (id: string) =>
    request<{
      connection: GitHubAppConnection;
      verification: GitHubAppVerification;
    }>(`/api/v1/github-apps/${id}/verify`, { method: "POST" }),
  githubAppInstallations: (id: string) =>
    request<GitHubAppInstallation[]>(`/api/v1/github-apps/${id}/installations`),
  githubAppRepositories: (id: string) =>
    request<GitHubRepository[]>(`/api/v1/github-apps/${id}/repositories`),
  githubAppBranches: (id: string, repository: string, repositoryId: number) => request<GitHubBranch[]>(`/api/v1/github-apps/${id}/branches?${new URLSearchParams({repository, repositoryId: String(repositoryId)})}`),
  configSourceRepositories: (id: string) => request<GitHubRepository[]>(`/api/v1/config-sources/${id}/repositories`),
  configSourceBranches: (id: string, repository: string, repositoryId: number) => request<GitHubBranch[]>(`/api/v1/config-sources/${id}/branches?${new URLSearchParams({repository, repositoryId: String(repositoryId)})}`),
  checkConfigSourceRepository: (id: string) => request<ConfigSource>(`/api/v1/config-sources/${id}/repository-check`, {method: "POST"}),
  startGitHubAppManifest: (body: {
    name: string;
    webUrl: string;
    apiUrl?: string;
    ownerType: "personal" | "organization";
    owner?: string;
    eventDelivery?: "none" | "direct" | "relay";
    relayServerId?: string;
    privateNetworkId?: string;
  }) =>
    request<GitHubAppManifest>("/api/v1/github-apps/manifest", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  createConfigSource: (body: {
    projectId: string;
    githubAppId?: string;
    credentialSecretId?: string;
    name: string;
    repository: string;
    repositoryId?: number;
    branch: string;
    path: string;
    syncMode: ConfigSource["syncMode"];
    pollIntervalSeconds: number;
  }) =>
    request<ConfigSource>("/api/v1/config-sources", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateConfigSource: (
    id: string,
    body: {
      projectId: string;
      githubAppId?: string;
      credentialSecretId?: string;
      name: string;
      repository: string;
      repositoryId?: number;
      branch: string;
      path: string;
      syncMode: ConfigSource["syncMode"];
      pollIntervalSeconds: number;
    },
  ) =>
    request<ConfigSource>(`/api/v1/config-sources/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  syncConfigSource: (id: string) =>
    request<ConfigSource>(`/api/v1/config-sources/${id}/sync`, {
      method: "POST",
    }),
  deleteConfigSource: (id: string) =>
    destructiveRequest<void>(`/api/v1/config-sources/${id}`, { method: "DELETE" }),
};
