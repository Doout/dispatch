import { request } from "../../api";

export type AutomationAccount = { id: string; name: string; description?: string; state: string };
export type Credential = { id: string; accountId: string; name: string; expiresAt: string; revokedAt?: string; lastUsedAt?: string };
export type Grant = { principalType: "user" | "service_account"; principalId: string; projectId: string; permissions: string[]; expiresAt?: string };
export type Assignment = { projectId: string; kind: string; resourceId: string };
export type Receipt = { id: string; projectId: string; action: string; state: string; operationId: string; operationUrl?: string; recoveryActions: string[]; message?: string; createdAt: string; updatedAt: string; retryUntil: string };
const id = encodeURIComponent;
const write = <T,>(path: string, method: string, body?: unknown) => request<T>(path, { method, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
const accounts = "/api/v1/automation-accounts";
export const automationClient = {
  accounts: () => request<AutomationAccount[]>(accounts),
  createAccount: (name: string, description: string) => write<AutomationAccount>(accounts, "POST", { name, description }),
  updateAccount: (account: AutomationAccount, state: string) => write<AutomationAccount>(`${accounts}/${id(account.id)}`, "PUT", { name: account.name, description: account.description ?? "", state }),
  credentials: (account: string) => request<Credential[]>(`${accounts}/${id(account)}/credentials`),
  issue: (account: string, name: string, expiresAt: string, rotate?: string) => write<{ credential: Credential; token: string }>(`${accounts}/${id(account)}/credentials${rotate ? `/${id(rotate)}/rotate` : ""}`, "POST", { name, expiresAt }),
  revoke: (account: string, credential: string) => write<void>(`${accounts}/${id(account)}/credentials/${id(credential)}/revoke`, "POST"),
  grants: () => request<Grant[]>("/api/v1/infrastructure/grants"),
  saveGrant: (grant: Grant) => write<Grant>("/api/v1/infrastructure/grants", "PUT", grant),
  removeGrant: (grant: Grant) => write<void>(`/api/v1/infrastructure/grants/${grant.principalType}/${id(grant.principalId)}/${id(grant.projectId)}`, "DELETE"),
  assignments: (project: string) => request<Assignment[]>(`/api/v1/infrastructure/assignments/${id(project)}`),
  assign: (project: string, kind: string, resourceId: string) => write<Assignment>(`/api/v1/infrastructure/assignments/${id(project)}`, "PUT", { kind, resourceId }),
  unassign: (assignment: Assignment) => write<void>(`/api/v1/infrastructure/assignments/${id(assignment.projectId)}/${id(assignment.kind)}/${id(assignment.resourceId)}`, "DELETE"),
  receipt: (receipt: string) => request<Receipt>(`/api/v1/mutation-receipts/${id(receipt)}`),
};
