import { requestDestructiveConfirmation, type DestructiveReview } from "../destructive";
import { setOverviewState } from "../overviewState";
import type { Overview } from "./overview";

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

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
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
      problem.detail || problem.lastError || problem.title || `Request failed (${response.status})`,
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

export async function destructiveRequest<T>(path: string, init: RequestInit, previewPath?: string): Promise<T> {
  const token = getToken(), impersonated = getImpersonatedUserID();
  const [pathname, query] = path.split("?");
  const preview = previewPath ?? (init.method === "DELETE" ? `${pathname}/delete-preview` : `${pathname}-preview`);
  const review = await request<DestructiveReview>(`${preview}${query ? `?${query}` : ""}`, { method: "POST" });
  const confirmation = await requestDestructiveConfirmation(review);
  if (token !== getToken() || impersonated !== getImpersonatedUserID()) throw new Error("Your account changed. Review this action again.");
  const body = typeof init.body === "string" ? JSON.parse(init.body) : {};
  return request<T>(path, { ...init, body: JSON.stringify({ ...body, confirmation }) });
}
