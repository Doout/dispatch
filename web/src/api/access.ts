import { request, destructiveRequest } from "./transport";
import type { Project } from "./projects";
import type { GitHubAppManifest } from "./connections";

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
  | "infrastructure.inspect"
  | "infrastructure.create"
  | "infrastructure.modify"
  | "infrastructure.delete"
  | "infrastructure.snapshot"
  | "infrastructure.restore"
  | "secrets.manage"
  | "connections.manage";

export type Identity = {
  kind?: "user" | "service_account";
  credentialId?: string;
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
  expiresAt?: string;
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

export type AuthStatus = {
  setupRequired: boolean;
  tokenLoginAvailable: boolean;
};

export const accessApi = {
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
    destructiveRequest<void>(`/api/v1/auth/providers/${id}/link`, {
      method: "DELETE",
    }),
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
    destructiveRequest<void>(`/api/v1/auth/providers/${id}`, { method: "DELETE" }),
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
    destructiveRequest<void>(`/api/v1/users/${id}`, { method: "DELETE" }),
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
    destructiveRequest<void>(`/api/v1/teams/${id}`, { method: "DELETE" }),
  upsertRoleAssignment: (body: {
    expiresAt?: string | null;
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
    destructiveRequest<void>(`/api/v1/role-assignments/${id}`, { method: "DELETE" }),
};
