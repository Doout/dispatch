import type { ReactNode } from "react";
import { Buildings } from "@phosphor-icons/react";
import { Mark } from "../components/PageStates";

export function HostedBrand({ detail }: { detail?: string }) {
  return (
    <a className="hosted-brand" href="/" aria-label="Dispatch home">
      <span className="hosted-brand-mark">
        <Mark />
      </span>
      <strong>Dispatch</strong>
      {detail && <span className="hosted-brand-detail">{detail}</span>}
    </a>
  );
}

export function TenantStatus({ state }: { state: string }) {
  return (
    <span className="hosted-status" data-state={state}>
      {state}
    </span>
  );
}

export function HostedEmpty({
  title,
  children,
  action,
}: {
  title: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <section className="hosted-empty">
      <span className="hosted-empty-icon">
        <Buildings size={28} aria-hidden="true" />
      </span>
      <h2>{title}</h2>
      <p>{children}</p>
      {action}
    </section>
  );
}

export function accountInitials(name: string) {
  return (
    name
      .trim()
      .split(/\s+/)
      .slice(0, 2)
      .map((part) => part[0])
      .join("")
      .toUpperCase() || "D"
  );
}
