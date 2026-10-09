import { createContext, useContext } from "react";
import type { HostedTenant } from "./client";

export const HostedTenantContext = createContext<HostedTenant | null>(null);
export const useHostedTenant = () => useContext(HostedTenantContext);
export function accountURL(tenant: HostedTenant, view = "account") {
  const url = new URL(tenant.loginUrl);
  url.pathname = "/";
  url.search = new URLSearchParams({ view }).toString();
  url.hash = "";
  return url.href;
}
