import { request } from "../api";

export type AuditEvent = { id: string; actorId: string; actorName: string; impersonatorId?: string; projectId?: string; appId?: string; action: string; resourceId?: string; outcome: string; createdAt: string };
export type BackupRecord = { id: string; engine: string; state: string; bytes: number; createdAt: string; verifiedAt?: string; message: string };
export type ApplicationOwner = { principalType: string; principalId: string; displayName?: string; updatedAt: string };
export type OwnershipItem = { appId: string; appName: string; projectId: string; owner?: ApplicationOwner };
export type OwnershipPage = { items: OwnershipItem[]; next?: string };
export type OperationsSummary = {
  observedAt: string;
  audit: { since: string; total: number; rejected: number; recent: AuditEvent[] };
  ownership: { total: number; unassigned: number };
  backups?: { configured: boolean; recorded: number; latest?: BackupRecord; latestVerified?: BackupRecord };
};
export const operationsClient = {
  summary: (projectId = "") => request<OperationsSummary>(`/api/v1/operations/summary${projectId ? `?${new URLSearchParams({projectId})}` : ""}`),
};
