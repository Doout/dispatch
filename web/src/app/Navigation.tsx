import { ReactNode, useEffect, useRef } from "react";
import {
  AppWindow,
  ChartBar,
  FolderSimple,
  GearSix,
  HardDrives,
  Key,
  Lightning,
  PlugsConnected,
  RocketLaunch,
  SignOut,
  Stack,
  UserCircle,
  UserSwitch,
  UsersThree,
  X,
  Wrench,
} from "@phosphor-icons/react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { Overview } from "../api";
import { routePath, shouldHandleNavigation, View } from "../routes";
import { Mark } from "../components/PageStates";

export function Nav({
  open,
  view,
  overview,
  onClose,
  onNavigate,
}: {
  open: boolean;
  view: View;
  overview: Overview | null;
  onClose: () => void;
  onNavigate: (view: View) => void;
}) {
  const railRef = useRef<HTMLElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!open) return;
    const previous =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    const frame = window.requestAnimationFrame(() =>
      railRef.current
        ?.querySelector<HTMLElement>('[aria-current="page"]')
        ?.focus(),
    );
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") closeRef.current();
    };
    window.addEventListener("keydown", handleKey);
    return () => {
      window.cancelAnimationFrame(frame);
      window.removeEventListener("keydown", handleKey);
      previous?.focus();
    };
  }, [open]);
  type NavEntry = { id: View; label: string; icon: ReactNode; count: number };
  const groups: Array<{ id: string; label: string; entries: NavEntry[] }> = [
    {
      id: "operate",
      label: "Operate",
      entries: [
        {
          id: "deployments",
          label: "Deployments",
          icon: <RocketLaunch size={18} />,
          count: overview?.deployments.length ?? 0,
        },
        {
          id: "applications",
          label: "Applications",
          icon: <AppWindow size={18} />,
          count: overview?.apps.length ?? 0,
        },
        ...(overview?.controllerSettings?.operationsEnabled === true ? [{ id: "operations" as View, label: "Operations", icon: <Wrench size={18} />, count: 0 }] : []),
        { id: "analytics", label: "Analytics", icon: <ChartBar size={18} />, count: 0 },
        {
          id: "events",
          label: "Events",
          icon: <Lightning size={18} />,
          count:
            (overview?.eventTriggers.length ?? 0) +
            (overview?.previewGroups.length ?? 0),
        },
      ],
    },
    {
      id: "resources",
      label: "Resources",
      entries: [
        {
          id: "projects", label: "Projects", icon: <FolderSimple size={18} />,
          count: overview?.projects.length ?? 0,
        },
        {
          id: "services", label: "Services", icon: <Stack size={18} />,
          count: overview?.services?.length ?? 0,
        },
        {
          id: "servers", label: "Servers", icon: <HardDrives size={18} />,
          count: overview?.servers.length ?? 0,
        },
      ],
    },
    ...(overview?.identity?.systemRole === "owner"
      ? [{
          id: "controller",
          label: "Controller",
          entries: [
            {
              id: "secrets" as View, label: "Variables", icon: <Key size={18} />,
              count: overview?.secrets.length ?? 0,
            },
            {
              id: "connections" as View, label: "Connections", icon: <PlugsConnected size={18} />,
              count: (overview?.githubApps.length ?? 0) + (overview?.secretStores?.length ?? 0),
            },
            { id: "access" as View, label: "Access", icon: <UsersThree size={18} />, count: 0 },
            { id: "settings" as View, label: "Settings", icon: <GearSix size={18} />, count: 0 },
          ],
        }]
      : [{ id: "preferences", label: "Preferences", entries: [{ id: "settings" as View, label: "Settings", icon: <GearSix size={18} />, count: 0 }] }]),
  ];
  const renderEntry = (entry: NavEntry) => (
    <a
      key={entry.id}
      href={routePath({ view: entry.id })}
      className="nav-link"
      aria-current={view === entry.id ? "page" : undefined}
      onClick={(event) => {
        if (!shouldHandleNavigation(event)) return;
        event.preventDefault();
        onNavigate(entry.id);
      }}
    >
      {entry.icon}
      <span>{entry.label}</span>
      {entry.count > 0 && <small className="nav-count" aria-hidden="true">{entry.count}</small>}
    </a>
  );

  return (
    <>
      <div className={`nav-scrim ${open ? "visible" : ""}`} onClick={onClose} />
      <aside
        ref={railRef}
        id="primary-navigation"
        className={`rail ${open ? "open" : ""}`}
        aria-label="Primary navigation"
      >
        <div className="wordmark">
          <Mark />
          <div>
            <span>Dispatch</span>
          </div>
          <button aria-label="Close navigation" onClick={onClose}>
            <X size={20} weight="bold" />
          </button>
        </div>
        <nav className="nav-list">
          {groups.map((group) => (
            <section className="nav-group" key={group.id} aria-labelledby={`nav-${group.id}`}>
              <h2 className="nav-heading" id={`nav-${group.id}`}>{group.label}</h2>
              {group.entries.map(renderEntry)}
            </section>
          ))}
        </nav>
        <footer className="rail-foot">
          <div className="control-mark">
            <span className={overview?.demo ? "demo-dot" : "live-dot"} />
            <div>
              <strong>Control plane</strong>
              <span>{overview?.demo ? "Demonstration mode" : "Connected"}</span>
            </div>
          </div>
        </footer>
      </aside>
    </>
  );
}

function accountInitials(value: string) {
  const parts = value.trim().split(/[\s_-]+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return `${parts[0][0]}${parts[parts.length - 1][0]}`.toUpperCase();
}

export function AccountMenu({
  identity,
  impersonating = false,
  onOpenProfile,
  onChangePassword,
  onLogout,
}: {
  identity: NonNullable<Overview["identity"]>;
  impersonating?: boolean;
  onOpenProfile: () => void;
  onChangePassword: () => void;
  onLogout: () => void;
}) {
  const name = identity.displayName || identity.username;
  const secondary =
    identity.username !== name
      ? identity.username
      : identity.systemRole === "owner"
        ? "Owner"
        : "Member";

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          className="account-menu-trigger"
          aria-label={`Open account menu for ${name}`}
          title={name}
        >
          <span className="account-avatar" aria-hidden="true">
            {accountInitials(name)}
          </span>
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="account-menu-content"
          align="end"
          sideOffset={8}
          collisionPadding={12}
        >
          <div className="account-menu-profile">
            <strong>{name}</strong>
            <span>{secondary}</span>
          </div>
          <DropdownMenu.Separator className="account-menu-separator" />
          <DropdownMenu.Item
            className="account-menu-item"
            onSelect={onOpenProfile}
          >
            <UserCircle size={18} />
            My profile
          </DropdownMenu.Item>
          {identity.id !== "controller-owner" && !impersonating && (
            <>
              <DropdownMenu.Item
                className="account-menu-item"
                onSelect={onChangePassword}
              >
                <Key size={18} />
                Change password
              </DropdownMenu.Item>
            </>
          )}
          <DropdownMenu.Item
            className="account-menu-item"
            onSelect={onLogout}
          >
            <SignOut size={18} />
            Log out
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

export function ImpersonationBanner({
  identity,
  impersonator,
  onExit,
}: {
  identity: NonNullable<Overview["identity"]>;
  impersonator: NonNullable<Overview["impersonator"]>;
  onExit: () => void;
}) {
  return (
    <div className="impersonation-banner" role="status">
      <UserSwitch size={17} weight="bold" />
      <span>
        Viewing as <strong>{identity.displayName || identity.username}</strong>
      </span>
      <button onClick={onExit}>
        Return to {impersonator.displayName || impersonator.username}
      </button>
    </div>
  );
}
