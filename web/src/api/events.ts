import { request, destructiveRequest } from "./transport";

export type EventActivity = {
 deliveryId?: string; attempts?: number; nextAttemptAt?: string;
 id: string; projectId: string; ruleId: string; name: string;
 transport: "poll" | "webhook" | "history"; kind: string; repository: string;
 branch?: string; commitSha?: string; pullRequest?: number; command?: string;
 state: string; message?: string; resourceId?: string; revisionIds?: string[];
 previewUrl?: string; createdAt: string; check?: boolean;
};

export type EventRule = {
 canEditHooks?: boolean;
 id: string; name: string; kind: "configuration" | "template" | "preview" | "trigger" | "group";
 repositories: string[]; command?: string; branch?: string; mode: string;
 intervalSeconds?: number; enabled: boolean; pullRequest?: number; error?: string; check?: EventActivity;
};

export type EventActivityPage = { items: EventActivity[]; next?: string; total: number };

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

export const eventsApi = {
  eventRules: () => request<EventRule[]>("/api/v1/events/rules"),
  eventActivity: (transport = "", before = "") => request<EventActivityPage>(`/api/v1/events/activity?${new URLSearchParams({transport,before})}`),
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
    destructiveRequest<void>(`/api/v1/event-triggers/${id}`, { method: "DELETE" }),
};
