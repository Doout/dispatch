import { setOverviewState } from "./overviewState";
export type Project = {
  id: string;
  name: string;
  description: string;
  createdAt: string;
};
export type Permission =
  | "access.manage"
  | "project.view"
  | "project.manage"
  | "project.access"
  | "project.configure"
  | "deployment.run"
  | "deployment.cancel"
  | "stage.approve"
  | "infrastructure.manage"
  | "secrets.manage"
  | "connections.manage";
export type Identity = {
  id: string;
  username: string;
  displayName: string;
  systemRole: "owner" | "member";
  permissions: Permission[];
};
export type User = {
  id: string;
  username: string;
  displayName: string;
  email?: string;
  passwordConfigured?: boolean;
  systemRole: "owner" | "member";
  state: "active" | "pending" | "disabled";
  createdAt: string;
  updatedAt: string;
};
export type ExternalIdentity = {
  providerId: string;
  subject: string;
  userId: string;
  login: string;
  email?: string;
  lastLogin: string;
  createdAt: string;
};
export type Team = {
  id: string;
  name: string;
  description?: string;
  createdAt: string;
  updatedAt: string;
};
export type TeamMember = {
  teamId: string;
  userId: string;
  role: "member" | "manager";
  createdAt: string;
};
export type RoleAssignment = {
  id: string;
  principalType: "user" | "team";
  principalId: string;
  scopeType: "project";
  scopeId: string;
  role: "admin" | "operator" | "deployer" | "viewer";
  createdAt: string;
  updatedAt: string;
};
export type RoleDefinition = {
  id: RoleAssignment["role"];
  name: string;
  permissions: Permission[];
};
export type AuthProvider = {
  id: string;
  name: string;
  type: "github";
  baseUrl: string;
  apiUrl: string;
  clientId: string;
  clientSecretConfigured: boolean;
  provisioning: "existing" | "approval";
  state: "ready" | "disabled";
  lastVerifiedAt?: string;
  createdAt: string;
  updatedAt: string;
};
export type PublicAuthProvider = Pick<
  AuthProvider,
  "id" | "name" | "type" | "baseUrl"
>;
export type AccountAuthLink = {
  provider: PublicAuthProvider;
  identity?: ExternalIdentity;
  available: boolean;
};
export type AccountProfileTeam = {
  id: string;
  name: string;
  role: "member" | "manager";
};
export type AccountProfileAccess = {
  projectId: string;
  projectName: string;
  role: RoleAssignment["role"];
  source: "user" | "team";
  sourceName?: string;
};
export type AccountProfile = {
  user: User;
  managed: boolean;
  links: AccountAuthLink[];
  teams: AccountProfileTeam[];
  projectAccess: AccountProfileAccess[];
};
export type AuthDiscovery = {
  method: "password" | "provider" | "choose";
  provider?: PublicAuthProvider;
  providers?: PublicAuthProvider[];
  password: boolean;
};
export type AccessOverview = {
  users: User[];
  teams: Team[];
  members: TeamMember[];
  assignments: RoleAssignment[];
  roles: RoleDefinition[];
  projects: Project[];
  providers: AuthProvider[];
  identities: ExternalIdentity[];
};
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
export type Server = {
  id: string;
  name: string;
  address: string;
  runtime: "docker" | "kubernetes" | "openshift" | "relay";
  state: string;
  agentMode: string;
  kubernetes?: KubernetesServerConfig;
  relay?: RelayServerConfig;
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
export type App = {
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
export type DeploymentState =
  | "queued"
  | "fetching"
  | "building"
  | "starting"
  | "checking"
  | "routing"
  | "succeeded"
  | "failed"
  | "cancelled";
export type Deployment = {
  id: string;
  appId: string;
  commitSha: string;
  specDigest: string;
  state: DeploymentState;
  message: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  outputs?: Record<string, string>;
  app?: App;
  server?: Server;
};
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
export type DeploymentLog = {
  id: number;
  deploymentId: string;
  level: string;
  message: string;
  createdAt: string;
};
export type ConfigSource = {
  id: string;
  projectId: string;
  githubAppId?: string;
  credentialSecretId?: string;
  name: string;
  repository: string;
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
export type WorkflowResource = {
  id: string;
  configSourceId: string;
  apiVersion: string;
  kind: "Application" | "Pipeline";
  name: string;
  path: string;
  document: string;
  specDigest: string;
  configSha: string;
  active: boolean;
  state: string;
  lastError?: string;
  sourceCount: number;
  jobCount: number;
  stageNames?: string[];
  targetRefs?: string[];
  createdAt: string;
  updatedAt: string;
};
export type WorkflowSourceRevision = {
  alias: string;
  repository: string;
  branch: string;
  commitSha: string;
  path?: string;
};
export type WorkflowRevision = {
  id: string;
  resourceId: string;
  configSha: string;
  specDigest: string;
  state: string;
  trigger: string;
  sources: Record<string, WorkflowSourceRevision>;
  outputs?: Record<string, Record<string, string>>;
  error?: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
};
export type WorkflowJobResult = {
  id: string;
  resourceId: string;
  revisionId: string;
  jobName: string;
  fingerprint: string;
  reusedFromId?: string;
  state: string;
  sources: Record<string, WorkflowSourceRevision>;
  outputs?: Record<string, string>;
  log?: string;
  error?: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
};
export type WorkflowStageRun = {
  id: string;
  revisionId: string;
  stageName: string;
  targetRef: string;
  state: string;
  approval: string;
  deploymentIds?: string[];
  checkRuns?: Record<string, string>;
  error?: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
};
export type WorkflowTopologyColumn = { id: string; label: string };
export type WorkflowTopologyNode = {
  id: string;
  column: string;
  kind: "source" | "job" | "finally" | "deployment" | "stage" | string;
  label: string;
  detail?: string;
  state?: string;
  href?: string;
  metadata?: Record<string, string>;
};
export type WorkflowTopologyEdge = { from: string; to: string; kind: string };
export type WorkflowTopology = {
  columns: WorkflowTopologyColumn[];
  nodes: WorkflowTopologyNode[];
  edges: WorkflowTopologyEdge[] | null;
};
export type AppliedValue = { path: string; value: unknown; redacted?: boolean };
export type DeploymentTopology = {
  topology: WorkflowTopology;
  target: string;
  runtime: string;
  namespace: string;
  release: string;
  chart?: string;
  values: AppliedValue[];
  live: boolean;
  warning?: string;
};
export type DeploymentManifest = {
  name: string;
  kind: string;
  apiVersion: string;
  document: string;
};
export type DeploymentManifestOrigin = {
  managed: boolean;
  repository?: string;
  branch?: string;
  configPath?: string;
  configRevision?: string;
  chartRepository?: string;
  chartPath?: string;
};
export type DeploymentManifests = {
  target: string;
  namespace: string;
  release: string;
  origin: DeploymentManifestOrigin;
  manifests: DeploymentManifest[];
  warning?: string;
};
export type DeploymentResourceLog = {
  container: string;
  content?: string;
  error?: string;
};
export type DeploymentResourceEvent = {
  type: string;
  reason: string;
  message: string;
  count: number;
  lastSeen: string;
};
export type DeploymentResource = {
  manifest: DeploymentManifest;
  loggable: boolean;
  logs: DeploymentResourceLog[];
  events: DeploymentResourceEvent[];
  warning?: string;
};
export type Overview = {
  demo: boolean;
  secretStorageConfigured: boolean;
  identity?: Identity;
  impersonator?: Identity;
  projectPermissions?: Record<string, Permission[]>;
  projects: Project[];
  servers: Server[];
  apps: App[];
  deployments: Deployment[];
  eventTriggers: EventTrigger[];
  previews: PreviewEnvironment[];
  previewGroups: PreviewGroup[];
  previewGroupRuns: PreviewGroupRun[];
  secrets: Secret[];
  secretStores?: SecretStore[];
  privateNetworks?: PrivateNetwork[];
  githubApps: GitHubAppConnection[];
  relayWebhooks: RelayWebhook[];
  configSources?: ConfigSource[];
  workflowResources?: WorkflowResource[];
  workflowRevisions?: WorkflowRevision[];
  workflowStageRuns?: WorkflowStageRun[];
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
};
export type GitHubRepository = {
  id: number;
  fullName: string;
  name: string;
  owner: string;
  defaultBranch: string;
  private: boolean;
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
};
export type GitHubAppManifest = {
  action: string;
  manifest: Record<string, unknown>;
};
export type SecretType =
  | "text"
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
export type AuthStatus = {
  setupRequired: boolean;
  tokenLoginAvailable: boolean;
};
export type EventTrigger = {
  id: string;
  appId: string;
  githubAppId?: string;
  provider: "github";
  repository: string;
  command: string;
  enabled: boolean;
  preDeployHook?: string;
  postDeployHook?: string;
  secretIds: string[];
  createdAt: string;
  updatedAt: string;
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

const tokenKey = "dispatch-admin-token";
const impersonationKey = "dispatch-impersonated-user";
export const getToken = () => sessionStorage.getItem(tokenKey) ?? "";
export const getImpersonatedUserID = () =>
  sessionStorage.getItem(impersonationKey) ?? "";
export const setImpersonatedUserID = (value: string) => {
  value
    ? sessionStorage.setItem(impersonationKey, value)
    : sessionStorage.removeItem(impersonationKey);
  setOverviewState(undefined, false);
  window.dispatchEvent(new Event("dispatch-auth-change"));
};
export const setToken = (value: string) => {
  const changed = value !== getToken();
  if (value) sessionStorage.setItem(tokenKey, value);
  else sessionStorage.removeItem(tokenKey);
  if (!value || changed) sessionStorage.removeItem(impersonationKey);
  setOverviewState(undefined, false);
  window.dispatchEvent(new Event("dispatch-auth-change"));
};

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const impersonatedUserID = token ? getImpersonatedUserID() : "";
  const response = await fetch(path, {
    ...init,
    headers: {
      Accept: "application/json",
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(impersonatedUserID
        ? { "Impersonate-User": impersonatedUserID }
        : {}),
      ...init?.headers,
    },
  });
  if (!response.ok) {
    const problem = await response
      .json()
      .catch(() => ({ title: "Request failed", detail: response.statusText }));
    const error = new Error(
      problem.detail ?? problem.title ?? "Request failed",
    ) as Error & { status?: number };
    error.status = response.status;
    throw error;
  }
  if (response.status === 204) return undefined as T;
  const value = await response.json() as T;
  if (path === "/api/v1/overview" && token === getToken() && impersonatedUserID === getImpersonatedUserID()) {
    const version = response.headers.get("X-Overview-Version");
    if (version) setOverviewState({ version, value: value as Overview });
  }
  return value;
}

export const api = {
  authStatus: () => request<AuthStatus>("/api/v1/auth/status"),
  setupAdmin: (username: string, password: string) =>
    request<{ username: string }>("/api/v1/auth/setup", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  login: (username: string, password: string) =>
    request<{ token: string }>("/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  logout: () =>
    request<void>("/api/v1/auth/logout", {
      method: "POST",
    }),
  authProviders: () => request<PublicAuthProvider[]>("/api/v1/auth/providers"),
  discoverAuth: (identifier: string) =>
    request<AuthDiscovery>("/api/v1/auth/discover", {
      method: "POST",
      body: JSON.stringify({ identifier }),
    }),
  startOAuth: (id: string) =>
    request<{ authorizationUrl: string }>(
      `/api/v1/auth/providers/${id}/start`,
      { method: "POST", body: JSON.stringify({}) },
    ),
  exchangeOAuth: (code: string) =>
    request<{ token: string }>("/api/v1/auth/exchange", {
      method: "POST",
      body: JSON.stringify({ code }),
    }),
  changePassword: (currentPassword: string, newPassword: string) =>
    request<void>("/api/v1/auth/password", {
      method: "PUT",
      body: JSON.stringify({ currentPassword, newPassword }),
    }),
  accountProfile: () => request<AccountProfile>("/api/v1/auth/profile"),
  accountAuthLinks: () =>
    request<AccountAuthLink[]>("/api/v1/auth/links"),
  startOAuthLink: (id: string, returnTo: string) =>
    request<{ authorizationUrl: string }>(
      `/api/v1/auth/providers/${id}/link`,
      { method: "POST", body: JSON.stringify({ returnTo }) },
    ),
  unlinkAuthProvider: (id: string) =>
    request<void>(`/api/v1/auth/providers/${id}/link`, {
      method: "DELETE",
    }),
  overview: () => request<Overview>("/api/v1/overview"),
  access: () => request<AccessOverview>("/api/v1/access"),
  userProfile: (id: string) =>
    request<AccountProfile>(`/api/v1/users/${id}/profile`),
  createAuthProvider: (body: Record<string, unknown>) =>
    request<AuthProvider>("/api/v1/auth/providers", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  startAuthProviderManifest: (body: {
    name: string;
    baseUrl: string;
    ownerType: "personal" | "organization";
    owner?: string;
    provisioning: AuthProvider["provisioning"];
    state: AuthProvider["state"];
  }) =>
    request<GitHubAppManifest>("/api/v1/auth/providers/manifest", {
      method: "POST",
      body: JSON.stringify({ ...body, type: "github" }),
    }),
  updateAuthProvider: (id: string, body: Record<string, unknown>) =>
    request<AuthProvider>(`/api/v1/auth/providers/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  verifyAuthProvider: (id: string) =>
    request<AuthProvider>(`/api/v1/auth/providers/${id}/verify`, {
      method: "POST",
    }),
  deleteAuthProvider: (id: string) =>
    request<void>(`/api/v1/auth/providers/${id}`, { method: "DELETE" }),
  createUser: (body: {
    username: string;
    displayName: string;
    email: string;
    password: string;
    systemRole: User["systemRole"];
    state: User["state"];
  }) =>
    request<User>("/api/v1/users", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateUser: (
    id: string,
    body: {
      username: string;
      displayName: string;
      email: string;
      systemRole: User["systemRole"];
      state: User["state"];
    },
  ) =>
    request<User>(`/api/v1/users/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deleteUser: (id: string) =>
    request<void>(`/api/v1/users/${id}`, { method: "DELETE" }),
  mergeUser: (sourceID: string, targetUserId: string) =>
    request<User>(`/api/v1/users/${sourceID}/merge`, {
      method: "POST",
      body: JSON.stringify({ targetUserId }),
    }),
  createTeam: (body: {
    name: string;
    description: string;
    memberIds: string[];
  }) =>
    request<Team>("/api/v1/teams", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateTeam: (
    id: string,
    body: { name: string; description: string; memberIds: string[] },
  ) =>
    request<Team>(`/api/v1/teams/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deleteTeam: (id: string) =>
    request<void>(`/api/v1/teams/${id}`, { method: "DELETE" }),
  upsertRoleAssignment: (body: {
    principalType: RoleAssignment["principalType"];
    principalId: string;
    projectId: string;
    role: RoleAssignment["role"];
  }) =>
    request<RoleAssignment>("/api/v1/role-assignments", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  deleteRoleAssignment: (id: string) =>
    request<void>(`/api/v1/role-assignments/${id}`, { method: "DELETE" }),
  logs: (id: string) =>
    request<DeploymentLog[]>(`/api/v1/deployments/${id}/logs`),
  deploymentTopology: (id: string) =>
    request<DeploymentTopology>(`/api/v1/deployments/${id}/topology`),
  deploymentManifests: (id: string) =>
    request<DeploymentManifests>(`/api/v1/deployments/${id}/manifests`),
  deploymentResource: (deploymentID: string, kind: string, name: string) =>
    request<DeploymentResource>(
      `/api/v1/deployments/${deploymentID}/resources/${encodeURIComponent(kind)}/${encodeURIComponent(name)}`,
    ),
  serverTopology: (id: string) =>
    request<WorkflowTopology>(`/api/v1/servers/${id}/topology`, {
      cache: "no-store",
    }),
  deploy: (appId: string, commitSha: string) =>
    request<Deployment>(`/api/v1/apps/${appId}/deployments`, {
      method: "POST",
      body: JSON.stringify({ commitSha }),
    }),
  cleanup: (appId: string) =>
    request<void>(`/api/v1/apps/${appId}/cleanup`, { method: "POST" }),
  cancel: (id: string) =>
    request<void>(`/api/v1/deployments/${id}/cancel`, { method: "POST" }),
  createProject: (body: { name: string; description: string }) =>
    request<Project>("/api/v1/projects", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateProject: (id: string, body: { name: string; description: string }) =>
    request<Project>(`/api/v1/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deleteProject: (id: string) =>
    request<void>(`/api/v1/projects/${id}`, { method: "DELETE" }),
  createServer: (body: {
    name: string;
    address?: string;
    runtime: "docker" | "kubernetes" | "openshift" | "relay";
    agentMode?: string;
    kubernetes?: KubernetesServerInput;
    relay?: { accessToken?: string };
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
    request<void>(`/api/v1/servers/${serverId}/relay/webhooks/${id}`, {
      method: "DELETE",
    }),
  repairServer: (id: string, loginCommand: string) =>
    request<Server>(`/api/v1/servers/${id}/repair`, {
      method: "POST",
      body: JSON.stringify({ loginCommand }),
    }),
  deleteServer: (id: string) =>
    request<void>(`/api/v1/servers/${id}`, { method: "DELETE" }),
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
    request<void>(`/api/v1/github-apps/${id}`, { method: "DELETE" }),
  verifyGitHubApp: (id: string) =>
    request<{
      connection: GitHubAppConnection;
      verification: GitHubAppVerification;
    }>(`/api/v1/github-apps/${id}/verify`, { method: "POST" }),
  githubAppInstallations: (id: string) =>
    request<GitHubAppInstallation[]>(`/api/v1/github-apps/${id}/installations`),
  githubAppRepositories: (id: string) =>
    request<GitHubRepository[]>(`/api/v1/github-apps/${id}/repositories`),
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
    request<void>(`/api/v1/config-sources/${id}`, { method: "DELETE" }),
  activateWorkflowResource: (id: string) =>
    request<{ resource: WorkflowResource; revision?: WorkflowRevision }>(
      `/api/v1/workflow/resources/${id}/activate`,
      { method: "POST" },
    ),
  deactivateWorkflowResource: (id: string) =>
    request<WorkflowResource>(`/api/v1/workflow/resources/${id}/deactivate`, {
      method: "POST",
    }),
  runWorkflowResource: (id: string) =>
    request<WorkflowRevision>(`/api/v1/workflow/resources/${id}/runs`, {
      method: "POST",
    }),
  workflowTopology: (id: string) =>
    request<WorkflowTopology>(`/api/v1/workflow/resources/${id}/topology`),
  workflowJobs: (revisionId: string) =>
    request<WorkflowJobResult[]>(
      `/api/v1/workflow/revisions/${revisionId}/jobs`,
    ),
  workflowStages: (revisionId: string) =>
    request<WorkflowStageRun[]>(
      `/api/v1/workflow/revisions/${revisionId}/stages`,
    ),
  approveWorkflowStage: (stageId: string) =>
    request<WorkflowStageRun>(`/api/v1/workflow/stages/${stageId}/approve`, {
      method: "POST",
    }),
  deleteApp: (id: string) =>
    request<void>(`/api/v1/apps/${id}`, { method: "DELETE" }),
  createEventTrigger: (
    appId: string,
    body: {
      githubAppId?: string;
      provider: "github";
      repository: string;
      command: string;
      enabled: boolean;
      preDeployHook?: string;
      postDeployHook?: string;
      secretIds?: string[];
    },
  ) =>
    request<EventTrigger>(`/api/v1/apps/${appId}/event-triggers`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateEventTrigger: (
    id: string,
    body: {
      githubAppId?: string;
      command: string;
      enabled: boolean;
      preDeployHook: string;
      postDeployHook: string;
      secretIds: string[];
    },
  ) =>
    request<EventTrigger>(`/api/v1/event-triggers/${id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  eventTriggers: (appId = "") =>
    request<EventTrigger[]>(
      `/api/v1/event-triggers${appId ? `?appId=${encodeURIComponent(appId)}` : ""}`,
    ),
  deleteEventTrigger: (id: string) =>
    request<void>(`/api/v1/event-triggers/${id}`, { method: "DELETE" }),
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
    request<void>(`/api/v1/secrets/${id}`, { method: "DELETE" }),
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
    request<void>(`/api/v1/secret-stores/${id}`, { method: "DELETE" }),
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
    request<PrivateNetwork>(`/api/v1/private-networks/${id}/rotate-token`, {
      method: "POST",
    }),
  installLanewayConnector: (id: string, bootstrapCommand: string) =>
    request<PrivateNetwork>(
      `/api/v1/private-networks/${id}/install-connector`,
      { method: "POST", body: JSON.stringify({ bootstrapCommand }) },
    ),
  deletePrivateNetwork: (id: string) =>
    request<void>(`/api/v1/private-networks/${id}`, { method: "DELETE" }),
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
    request<void>(`/api/v1/preview-groups/${id}`, { method: "DELETE" }),
  previewGroupRun: (id: string) =>
    request<PreviewGroupRun>(`/api/v1/preview-group-runs/${id}`),
  cleanupPreviewGroupRun: (id: string) =>
    request<PreviewGroupRun>(`/api/v1/preview-group-runs/${id}/cleanup`, {
      method: "POST",
    }),
};
