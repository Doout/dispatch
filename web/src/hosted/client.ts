export type Tenant = {
  id: string;
  slug: string;
  name: string;
  state: string;
  createdAt: string;
};
export type Account = {
  id: string;
  name: string;
  email: string;
  emailVerified: boolean;
  platformAdmin: boolean;
  state: string;
};
export type Membership = {
  tenant: Tenant;
  role: "owner" | "admin" | "member";
  url: string;
};
export type TenantMember = {
  tenantId: string;
  userId: string;
  role: "owner" | "admin" | "member";
  state: string;
  user: Pick<Account, "id" | "name" | "email">;
};
export type HostedPlatform = {
  mode: "platform";
  origin: string;
  registrationEnabled: boolean;
};
export type HostedTenant = {
  mode: "tenant";
  origin: string;
  loginUrl: string;
  tenant: Tenant;
};
export type HostedConfiguration = HostedPlatform | HostedTenant;
export type Usage = {
  periodStart: string;
  periodEnd: string;
  measured: string[];
  [name: string]: number | string | string[];
};

export class HostedError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
export async function hostedRequest<T>(
  path: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    method,
    credentials: "same-origin",
    cache: "no-store",
    headers: {
      Accept: "application/json",
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (!response.ok) {
    const problem = await response.json().catch(() => ({}));
    throw new HostedError(
      problem.detail || `Request failed (${response.status}).`,
      response.status,
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export async function detectHosted(): Promise<HostedConfiguration | null> {
  const response = await fetch("/api/v1/hosted", {
    credentials: "same-origin",
    cache: "no-store",
    headers: { Accept: "application/json" },
  });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error("Unable to reach Dispatch. Try again.");
  if (!response.headers.get("Content-Type")?.includes("application/json"))
    return null;
  const value = (await response.json()) as HostedConfiguration;
  if (value.mode !== "platform" && value.mode !== "tenant")
    throw new Error("Dispatch returned an invalid sign-in configuration.");
  return value;
}

// Handoff targets come from the server, but must still remain in the tenant
// namespace before the browser navigates with a one-use code.
export function tenantDestination(
  value: string,
  platformOrigin: string,
): string {
  const target = new URL(value),
    platform = new URL(platformOrigin);
  if (
    target.protocol !== "https:" ||
    target.username ||
    target.password ||
    target.port !== platform.port ||
    !target.hostname.endsWith(`.${platform.hostname}`) ||
    target.hostname.slice(0, -platform.hostname.length - 1).includes(".")
  )
    throw new Error("Dispatch returned an invalid tenant address.");
  return target.href;
}
