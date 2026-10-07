import { useEffect, useRef, type ReactNode } from "react";
import { ChartBar, ClockCounterClockwise, FolderSimple, GearSix, HardDrives, Lightning, Stack, Wrench, X } from "@phosphor-icons/react";
import type { Overview } from "../api";
import { type AppRoute, routePath, shouldHandleNavigation } from "../routes";
import { Mark } from "../components/PageStates";
import { canManageAnyProject } from "../permissions";

type Group = "workloads" | "analytics" | "infrastructure" | "recovery" | "automation" | "projects" | "operations" | "settings";
type Entry = { label: string; route: AppRoute; active: boolean };

export function navigationGroup(route: AppRoute): Group {
  if (["deployments", "applications", "services", "workloads"].includes(route.view)) return "workloads";
  if (["servers", "infrastructure"].includes(route.view)) return "infrastructure";
  if (route.view === "events" || route.view === "automation") return "automation";
  if (["settings", "connections", "secrets", "access"].includes(route.view)) return "settings";
  return route.view as Group;
}

export const groupTitles: Record<Group, string> = { workloads: "Workloads", analytics: "Analytics", infrastructure: "Infrastructure", recovery: "Recovery", automation: "Automation", projects: "Projects", operations: "Operations", settings: "Settings" };

function entries(route: AppRoute, overview: Overview): Entry[] {
  const group = navigationGroup(route);
  const owner = overview.identity?.systemRole === "owner";
  if (group === "workloads") return [
    { label: "Applications", route: { view: "workloads", workloadSection: "applications" }, active: route.view === "workloads" && (!route.workloadSection || route.workloadSection === "applications") || route.view === "applications" && !!route.applicationID },
    { label: "Parallel deployments", route: { view: "workloads", workloadSection: "parallel" }, active: route.view === "workloads" && route.workloadSection === "parallel" },
    { label: "Runs", route: { view: "deployments", deploymentFilters: { layout: "list" } }, active: route.view === "deployments" || route.view === "workloads" && route.workloadSection === "runs" },
    { label: "Services", route: { view: "services" }, active: route.view === "services" },
    { label: "Configuration", route: { view: "applications" }, active: route.view === "applications" && !route.applicationID },
  ];
  if (group === "analytics") return [
    { label: "Trends", route: { ...route, analyticsFilters: { ...route.analyticsFilters, section: "overview" } }, active: route.analyticsFilters?.section !== "data" },
    { label: "Data", route: { ...route, analyticsFilters: { ...route.analyticsFilters, section: "data" } }, active: route.analyticsFilters?.section === "data" },
  ];
  if (group === "infrastructure") return [
    { label: "Servers", route: { view: "servers" }, active: route.view === "servers" },
    { label: "Machines", route: { view: "infrastructure", resourceSection: "machines" }, active: route.view === "infrastructure" && route.resourceSection !== "providers" },
    ...(owner ? [{ label: "Providers", route: { view: "infrastructure", resourceSection: "providers" } as AppRoute, active: route.resourceSection === "providers" }] : []),
  ];
  if (group === "recovery") return [
    { label: "Workload backups", route: { view: "recovery", resourceSection: "workload-backups" }, active: !route.resourceSection || route.resourceSection === "workload-backups" },
    { label: "Machine snapshots", route: { view: "recovery", resourceSection: "machine-snapshots" }, active: route.resourceSection === "machine-snapshots" },
    ...(owner ? [{ label: "Controller backups", route: { view: "recovery", resourceSection: "controller-backups" } as AppRoute, active: route.resourceSection === "controller-backups" }] : []),
  ];
  if (group === "automation") return [
    ...(owner ? [
      { label: "Credentials", route: { view: "automation", resourceSection: "credentials" } as AppRoute, active: route.view === "automation" && (!route.resourceSection || route.resourceSection === "credentials") },
    ] : []),
    ...(canManageAnyProject(overview, "infrastructure.inspect") ? [{ label: "Assignments", route: { view: "automation", resourceSection: "assignments" } as AppRoute, active: route.resourceSection === "assignments" }] : []),
    { label: "Receipts", route: { view: "automation", resourceSection: "receipts" }, active: route.resourceSection === "receipts" },
    { label: "Rules", route: { view: "events", eventSection: "rules" }, active: route.view === "events" && route.eventSection !== "activity" },
    { label: "Activity", route: { view: "events", eventSection: "activity" }, active: route.view === "events" && route.eventSection === "activity" },
  ];
  if (group === "settings") return [
    { label: "Interface", route: { view: "settings" }, active: route.view === "settings" },
    ...(owner ? [
      { label: "Connections", route: { view: "connections" } as AppRoute, active: route.view === "connections" },
      { label: "Variables", route: { view: "secrets" } as AppRoute, active: route.view === "secrets" },
      { label: "Access", route: { view: "access" } as AppRoute, active: route.view === "access" },
    ] : []),
  ];
  return [];
}

function RouteLink({ entry, onNavigate, children, className }: { entry: Entry; onNavigate: (route: AppRoute) => void; children?: ReactNode; className?: string }) {
  return <a className={className} href={routePath(entry.route)} aria-current={entry.active ? "page" : undefined} onClick={event => {
    if (!shouldHandleNavigation(event)) return;
    event.preventDefault(); onNavigate(entry.route);
  }}>{children}{entry.label}</a>;
}

export function InterfaceNav({ route, overview, open, onClose, onNavigate }: { route: AppRoute; overview: Overview | null; open: boolean; onClose: () => void; onNavigate: (route: AppRoute) => void }) {
  const rail = useRef<HTMLElement>(null);
  const close = useRef(onClose); close.current = onClose;
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const frame = requestAnimationFrame(() => rail.current?.querySelector<HTMLElement>('[aria-current="page"]')?.focus());
    const keydown = (event: KeyboardEvent) => { if (event.key === "Escape") close.current(); };
    window.addEventListener("keydown", keydown);
    return () => { cancelAnimationFrame(frame); window.removeEventListener("keydown", keydown); previous?.focus(); };
  }, [open]);
  const group = navigationGroup(route);
  const items: Array<{ id: Group; icon: ReactNode; route: AppRoute }> = [
    { id: "workloads", icon: <Stack size={18} />, route: { view: "workloads", workloadSection: "applications" } },
    { id: "analytics", icon: <ChartBar size={18} />, route: { view: "analytics" } },
    { id: "infrastructure", icon: <HardDrives size={18} />, route: { view: "servers" } },
    { id: "recovery", icon: <ClockCounterClockwise size={18} />, route: { view: "recovery", resourceSection: "workload-backups" } },
    { id: "automation", icon: <Lightning size={18} />, route: { view: "automation", resourceSection: overview?.identity?.systemRole === "owner" ? "credentials" : "receipts" } },
    { id: "projects", icon: <FolderSimple size={18} />, route: { view: "projects" } },
    ...(overview?.controllerSettings?.operationsEnabled ? [{ id: "operations" as Group, icon: <Wrench size={18} />, route: { view: "operations" } as AppRoute }] : []),
    { id: "settings", icon: <GearSix size={18} />, route: { view: "settings" } },
  ];
  return <>
    <div className={`nav-scrim ${open ? "visible" : ""}`} onClick={onClose} />
    <aside ref={rail} id="primary-navigation" className={`rail interface-rail ${open ? "open" : ""}`} aria-label="Primary navigation">
      <div className="wordmark"><Mark /><span>Dispatch</span><button aria-label="Close navigation" onClick={onClose}><X size={20} /></button></div>
      <nav className="nav-list">{items.map(item => <RouteLink key={item.id} entry={{ label: groupTitles[item.id], route: item.route, active: group === item.id }} onNavigate={onNavigate} className="nav-link">{item.icon}</RouteLink>)}</nav>
      <div className="interface-version"><span>New interface</span><a href="/settings" onClick={event => { if (!shouldHandleNavigation(event)) return; event.preventDefault(); onNavigate({ view: "settings" }); }}>Change</a></div>
    </aside>
  </>;
}

export function InterfaceTabs({ route, overview, onNavigate }: { route: AppRoute; overview: Overview; onNavigate: (route: AppRoute) => void }) {
  const tabs = entries(route, overview);
  if (!tabs.length) return null;
  return <nav className="interface-tabs" aria-label={`${groupTitles[navigationGroup(route)]} pages`}>{tabs.map(entry => <RouteLink key={entry.label} entry={entry} onNavigate={onNavigate} />)}</nav>;
}
