import { request } from "./api";

export type ObservationConfig = {
 appId: string; projectId: string; revision: number;
 scheduled: boolean; intervalSeconds: number; staleAfterSeconds: number;
 endpointUrl?: string; notificationsEnabled: boolean; webhookConfigured: boolean;
 mutedUntil?: string; updatedAt: string; updatedBy?: string;
};
export type EndpointObservation = {
 state: string; tls: string; httpStatus?: number; certificateExpiresAt?: string;
 durationMs: number; message: string; location: string;
};
export type ApplicationObservation = {
 appId: string; projectId: string; configurationRevision: number; deploymentId?: string;
 source: string; state: string; drift: string; health: string;
 endpoint: EndpointObservation; checkedAt?: string; nextCheckAt?: string;
 consecutiveFailures: number; message?: string; location: string;
};
export type ObservationEvent = {
 id: string; appId: string; projectId: string; deploymentId?: string;
 kind: string; previousState?: string; state: string; message: string; link: string;
 createdAt: string; delivery: string; attempts: number; nextAttemptAt?: string;
 deliveredAt?: string; deliveryMessage?: string;
};
export type ObservationStatus = {
 configuration: ObservationConfig; observation: ApplicationObservation;
 freshness: "not_checked" | "fresh" | "stale" | "checking"; checking: boolean; events: ObservationEvent[];
};
export type ObservationInput = Pick<ObservationConfig, "revision" | "scheduled" | "intervalSeconds" | "staleAfterSeconds" | "notificationsEnabled" | "mutedUntil"> & { endpointUrl: string; webhookUrl?: string; removeWebhook?: boolean };
const path = (id: string) => `/api/v1/apps/${encodeURIComponent(id)}/observations`;
export const observationsClient = {
 get: (id: string) => request<ObservationStatus>(path(id)),
 save: (id: string, input: ObservationInput) => request<ObservationStatus>(path(id), { method: "PUT", body: JSON.stringify(input) }),
 check: (id: string) => request<ObservationStatus>(`${path(id)}/check`, { method: "POST" }),
};
