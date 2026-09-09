import { ReactNode } from "react";
import { Plus } from "@phosphor-icons/react";
import { shouldHandleNavigation, View } from "./routes";

export const pageTitles: Record<View, string> = {
  deployments: "Deployments",
  applications: "Applications",
  events: "Events",
  projects: "Projects",
  servers: "Servers",
  secrets: "Secrets",
  connections: "Connections",
  access: "Access",
};

export type PageAction = {
  label: string;
  onClick: () => void;
  href?: string;
  disabled?: boolean;
  icon?: ReactNode;
  tone?: "primary" | "quiet";
};

export function PageHeader({ view, action, trailing, title }: { view: View; action?: PageAction; trailing?: ReactNode; title?: string }) {
  const actionClass = action?.tone === "quiet" ? "quiet-button" : "primary-button";
  return <header className="page-header"><div><h1>{title ?? pageTitles[view]}</h1></div>{trailing ?? (action && (action.href ? <a className={actionClass} href={action.href} aria-disabled={action.disabled || undefined} onClick={(event) => { if (action.disabled) { event.preventDefault(); return; } if (!shouldHandleNavigation(event)) return; event.preventDefault(); action.onClick(); }}>{action.icon ?? <Plus size={16} weight="bold" />}{action.label}</a> : <button className={actionClass} disabled={action.disabled} onClick={action.onClick}>{action.icon ?? <Plus size={16} weight="bold" />}{action.label}</button>))}</header>;
}
