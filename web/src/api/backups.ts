import { request, destructiveRequest } from "./transport";

export type WorkloadBackup = {id:string;projectId:string;sourceRunId:string;storageId:string;serverId:string;state:string;revision:number;artifactId:string;consistency:string;format:string;checksum?:string;bytes:number;encryption:string;location:string;policy:string;checkCount:number;verificationState:string;cleanupState:string;verificationIntervalHours:number;nextVerificationAt?:string;verifiedAt?:string;createdAt:string;updatedAt:string;message?:string};

export type WorkloadBackupOperation = {id:string;backupId:string;projectId:string;action:string;state:string;targetRunId?:string;targetName?:string;revision:number;recoveryAfter:string;message?:string;cleanupState?:string};

export const backupsApi = {
  workloadBackups: (projectId = "") => request<WorkloadBackup[]>(`/api/v1/workload-backups${projectId ? `?projectId=${encodeURIComponent(projectId)}` : ""}`),
  createWorkloadBackup: (input: {sourceRunId: string; checks: {query:string;expected:string}[]; verificationIntervalHours:number}) => request<WorkloadBackupOperation>("/api/v1/workload-backups", {method:"POST",body:JSON.stringify(input)}),
  workloadBackupOperations: (id:string) => request<WorkloadBackupOperation[]>(`/api/v1/workload-backups/${id}/operations`),
  verifyWorkloadBackup: (id:string) => request<WorkloadBackupOperation>(`/api/v1/workload-backups/${id}/verify`,{method:"POST"}),
  reconcileWorkloadBackup: (id:string,operationId:string) => request<WorkloadBackupOperation>(`/api/v1/workload-backups/${id}/operations/${operationId}/reconcile`,{method:"POST"}),
  deleteWorkloadBackup: (id:string) => destructiveRequest<WorkloadBackupOperation>(`/api/v1/workload-backups/${id}/delete`,{method:"POST"}),
  restoreWorkloadBackup: (id:string,destination:string) => destructiveRequest<WorkloadBackupOperation>(`/api/v1/workload-backups/${id}/restore/${destination}`,{method:"POST"},`/api/v1/workload-backups/${id}/restore/${destination}/preview`),
};
