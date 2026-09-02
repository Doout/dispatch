import { ChangeEvent, FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AppWindow,
  ArrowClockwise,
  ArrowLeft,
  ArrowSquareOut,
  Check,
  CheckCircle,
  CircleNotch,
  Cloud,
  Copy,
  FolderSimple,
  HardDrives,
  Key,
  Lightning,
  List,
  LockSimple,
  PencilSimple,
  Plus,
  PlugsConnected,
  RocketLaunch,
  Trash,
  UploadSimple,
  WarningCircle,
  X,
} from "@phosphor-icons/react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { api, App as AppModel, Deployment, DeploymentLog, EventTrigger, GitHubAppConnection, Overview, PreviewGroup, PreviewGroupComponent, Project, RelayWebhook, Secret, SecretSource, SecretType, Server, setToken } from "./api";
import { ProjectForm, ServerForm } from "./Onboarding";
import { clusterDeploymentsByApplication, groupDeployments, relative, short, statusTone } from "./presentation";
import { readSecretTextFile } from "./fileUploads";
import { AppRoute, DeploymentSection, EventSection, readRoute, routePath, shouldHandleNavigation, View } from "./routes";
import { ApplicationsPage } from "./ApplicationsPage";
import { ConnectionsPage } from "./ConnectionsPage";
import { HookCredentialBindings, HookFields } from "./HookEditorFields";
import { PageHeader } from "./PageHeader";
import { StatusLabel, TableIconAction } from "./ResourceTable";
import { useDialogFocus } from "./useDialogFocus";
import { DeploymentRuntime, ServerRuntime } from "./deployments/RuntimeTopology";

type Dialog = "deploy" | "project" | "server" | "repair" | null;
type DeleteTarget = { kind: "project"; item: Project } | { kind: "server"; item: Server } | { kind: "application"; item: AppModel } | { kind: "previewGroup"; item: PreviewGroup } | { kind: "secret"; item: Secret };


function currentPageScroll() {
  return window.innerWidth <= 920 ? window.scrollY : document.getElementById("page-content")?.scrollTop ?? 0;
}

function restorePageScroll(scrollTop: number) {
  if (window.innerWidth <= 920) window.scrollTo(0, scrollTop);
  else {
    const page = document.getElementById("page-content");
    if (page) page.scrollTop = scrollTop;
  }
}

function rememberDeploymentListState(values: Record<string, unknown>) {
  window.history.replaceState({ ...(window.history.state ?? {}), ...values }, "", window.location.href);
}

function rememberedAttemptClusters() {
  const value = window.history.state?.deploymentAttemptClusters;
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

export default function DispatchApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [route, setRoute] = useState<AppRoute>(() => readRoute());
  const view = route.view;
  const [dialog, setDialog] = useState<Dialog>(null);
  const [creatingApplication, setCreatingApplication] = useState(false);
  const [editingProject, setEditingProject] = useState<Project | null>(null);
  const [editingServer, setEditingServer] = useState<Server | null>(null);
  const [newServerRuntime, setNewServerRuntime] = useState<Server["runtime"]>("docker");
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null);
  const [deployAppID, setDeployAppID] = useState("");
  const [quickViewID, setQuickViewID] = useState("");
  const [logs, setLogs] = useState<DeploymentLog[]>([]);
  const [logsLoading, setLogsLoading] = useState(false);
  const [logsError, setLogsError] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [needsAuth, setNeedsAuth] = useState(false);
  const [mobileNav, setMobileNav] = useState(false);
  const [connectionNotice, setConnectionNotice] = useState("");
  const connectionCallbackHandled = useRef(false);

  const navigateRoute = useCallback((next: AppRoute, options: { replace?: boolean; state?: Record<string, unknown> } = {}) => {
    const currentScroll = currentPageScroll();
    window.history.replaceState({ ...(window.history.state ?? {}), scrollTop: currentScroll }, "", window.location.href);
    const nextState = { dispatchRoute: true, scrollTop: 0, ...(options.state ?? {}) };
    if (options.replace) window.history.replaceState(nextState, "", routePath(next));
    else window.history.pushState(nextState, "", routePath(next));
    setRoute(next);
    window.requestAnimationFrame(() => restorePageScroll(0));
  }, []);

  const load = useCallback(async (quiet = false) => {
    try {
      const next = await api.overview();
      setOverview(next);
      setNeedsAuth(false);
      setError("");
      if (!quiet && next.servers.length === 0 && readRoute().view === "deployments") navigateRoute({ view: "servers" }, { replace: true });
    } catch (cause) {
      const failure = cause as Error & { status?: number };
      if (failure.status === 401) setNeedsAuth(true); else setError(failure.message);
    } finally {
      if (!quiet) setLoading(false);
    }
  }, [navigateRoute]);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const connectionCallback = params.has("githubAppSetup") || params.has("githubAppCreated") || params.has("githubAppStatus") || params.has("lanewayStatus") || params.has("installation_id");
    const canonicalPath = routePath(readRoute());
    const currentPath = `${window.location.pathname}${window.location.search}`;
    window.history.replaceState({ ...(window.history.state ?? {}), dispatchRoute: true, scrollTop: currentPageScroll() }, "", !connectionCallback && currentPath !== canonicalPath ? canonicalPath : window.location.href);
    const handlePopState = (event: PopStateEvent) => {
      setRoute(readRoute());
      window.requestAnimationFrame(() => {
        const scrollTop = typeof event.state?.scrollTop === "number" ? event.state.scrollTop : 0;
        restorePageScroll(scrollTop);
      });
    };
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(true), 1800);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    if (!overview || needsAuth || connectionCallbackHandled.current) return;
    connectionCallbackHandled.current = true;
    const params = new URLSearchParams(window.location.search);
    const setupID = params.get("githubAppSetup");
    const installationID = Number(params.get("installation_id") || "0");
    const createdID = params.get("githubAppCreated");
    const callbackStatus = params.get("githubAppStatus");
    const lanewayStatus = params.get("lanewayStatus");
    if (lanewayStatus) {
      navigateRoute({ view: "connections" }, { replace: true });
      setConnectionNotice(lanewayStatus === "connected" ? "Laneway network connected." : params.get("detail") || "Laneway connection failed.");
      void load(true);
    } else if (setupID && installationID > 0) {
      navigateRoute({ view: "connections" }, { replace: true });
      void (async () => {
        try {
          await api.updateGitHubApp(setupID, { installationId: installationID });
          await api.verifyGitHubApp(setupID);
          setConnectionNotice("GitHub App installed and verified.");
          await load(true);
        } catch (cause) {
          setConnectionNotice((cause as Error).message);
        }
      })();
    } else if (createdID) {
      navigateRoute({ view: "connections" }, { replace: true });
      setConnectionNotice("GitHub App created. Install it on an account to finish the connection.");
    } else if (callbackStatus === "error") {
      navigateRoute({ view: "connections" }, { replace: true });
      setConnectionNotice(params.get("detail") || "GitHub App setup failed.");
    }
  }, [load, navigateRoute, needsAuth, overview]);

  const routeDeployment = overview?.deployments.find((item) => item.id === route.deploymentID);
  const routeServer = overview?.servers.find((item) => item.id === route.serverID);
  const quickViewDeployment = !route.deploymentID ? overview?.deployments.find((item) => item.id === quickViewID) : undefined;
  const selectedDeployment = routeDeployment ?? quickViewDeployment;
  useEffect(() => {
    if (!selectedDeployment?.id) {
      setLogs([]);
      setLogsLoading(false);
      setLogsError("");
      return;
    }
    let active = true;
    setLogs([]);
    setLogsLoading(true);
    setLogsError("");
    const refresh = async () => {
      try {
        const next = await api.logs(selectedDeployment.id);
        if (active) {
          setLogs(next);
          setLogsError("");
          setLogsLoading(false);
        }
      } catch (cause) {
        if (active) {
          setLogsError((cause as Error).message || "Deployment logs are unavailable.");
          setLogsLoading(false);
        }
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 1200);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [selectedDeployment?.id]);

  const navigate = (next: View) => {
    navigateRoute({ view: next });
    if (next !== "applications") setCreatingApplication(false);
    setMobileNav(false);
  };

  if (needsAuth) return <AuthScreen onAuthenticated={() => { setNeedsAuth(false); setLoading(true); void load(); }} />;

  return (
    <div className="shell">
      <a className="skip-link" href="#page-content">Skip to content</a>
      <Nav
        open={mobileNav}
        view={view}
        overview={overview}
        onClose={() => setMobileNav(false)}
        onNavigate={navigate}
      />

      <main className="workspace">
        <header className="mobile-bar">
          <button className="menu-button" aria-label="Open navigation" aria-controls="primary-navigation" aria-expanded={mobileNav} onClick={() => setMobileNav(true)}><List size={22} weight="bold" /></button>
          <div className="mobile-brand"><Mark /><strong>Dispatch</strong></div>
        </header>

        {error && <div className="error-banner" role="alert"><strong>Inventory unavailable</strong><span>{error}</span><button onClick={() => void load()}>Retry</button></div>}

        <div className="page-scroll" id="page-content">
          {loading && <PageLoading />}
          {!loading && overview && view === "deployments" && route.deploymentID && routeDeployment && <DeploymentDetailsPage deployment={routeDeployment} section={route.deploymentSection ?? "summary"} onSectionChange={(section) => navigateRoute({ view: "deployments", deploymentID: routeDeployment.id, deploymentSection: section })} logs={logs} logsLoading={logsLoading} logsError={logsError} onBack={() => { if (window.history.state?.deploymentEntry) window.history.back(); else navigateRoute({ view: "deployments" }, { replace: true }); }} onCancel={async () => { try { await api.cancel(routeDeployment.id); await load(); } catch (cause) { setError((cause as Error).message); } }} />}
          {!loading && overview && view === "deployments" && route.deploymentID && !routeDeployment && <MissingDeploymentPage onBack={() => navigateRoute({ view: "deployments" }, { replace: true })} />}
          {!loading && overview && view === "deployments" && !route.deploymentID && <DeploymentsPage overview={overview} onSelect={setQuickViewID} onOpen={(next) => { setDeployAppID(""); setDialog(next); }} onCreateApplication={() => { setCreatingApplication(true); navigateRoute({ view: "applications" }); }} />}
          {!loading && overview && view === "applications" && <ApplicationsPage overview={overview} section={route.applicationSection ?? "applications"} applicationID={route.applicationID} creating={creatingApplication} onToggleCreate={() => setCreatingApplication((value) => !value)} onChanged={async () => { await load(); setCreatingApplication(false); }} onDeploy={(appID) => { setDeployAppID(appID); setDialog("deploy"); }} onDelete={(application) => setDeleteTarget({ kind: "application", item: application })} onDeleteGroup={(group) => setDeleteTarget({ kind: "previewGroup", item: group })} onNavigate={navigate} onOpenTopology={(resource) => navigateRoute({ view: "applications", applicationID: resource.id })} onOpenDeploymentManifests={(deploymentID) => navigateRoute({ view: "deployments", deploymentID, deploymentSection: "manifests" })} onCloseTopology={() => navigateRoute({ view: "applications" })} />}
          {!loading && overview && view === "events" && <EventsPage overview={overview} section={route.eventSection ?? "rules"} onSectionChange={(section) => navigateRoute({ view: "events", eventSection: section })} onConfigure={() => navigateRoute({ view: "applications" })} onChanged={async () => { await load(); }} />}
          {!loading && overview && view === "projects" && <ProjectsPage overview={overview} onAdd={() => { setEditingProject(null); setDialog("project"); }} onEdit={(project) => { setEditingProject(project); setDialog("project"); }} onDelete={(project) => setDeleteTarget({ kind: "project", item: project })} />}
          {!loading && overview && view === "servers" && route.serverID && routeServer && <ServerRuntime server={routeServer} />}
          {!loading && overview && view === "servers" && !route.serverID && <ServersPage overview={overview} onTopology={(server) => navigateRoute({ view: "servers", serverID: server.id })} onChanged={async () => { await load(); }} onAdd={() => { setEditingServer(null); setNewServerRuntime("docker"); setDialog("server"); }} onEdit={(server) => { setEditingServer(server); setDialog("server"); }} onRepair={(server) => { setEditingServer(server); setDialog("repair"); }} onDelete={(server) => setDeleteTarget({ kind: "server", item: server })} />}
          {!loading && overview && view === "secrets" && <SecretsPage overview={overview} onChanged={async () => { await load(); }} onDelete={(secret) => setDeleteTarget({ kind: "secret", item: secret })} />}
          {!loading && overview && view === "connections" && <ConnectionsPage overview={overview} notice={connectionNotice} onNotice={setConnectionNotice} onChanged={async () => { await load(); }} onAddRelay={() => { setEditingServer(null); setNewServerRuntime("relay"); setDialog("server"); }} />}
        </div>
      </main>

      {dialog && overview && <ResourceDialog kind={dialog} overview={overview} project={editingProject ?? undefined} server={editingServer ?? undefined} serverRuntime={newServerRuntime} deployAppID={deployAppID} onClose={() => setDialog(null)} onChanged={async () => { await load(); setDialog(null); }} onDeployed={async (id) => { setDialog(null); await load(); navigateRoute({ view: "deployments", deploymentID: id }); }} />}
      {deleteTarget && overview && <DeleteDialog target={deleteTarget} overview={overview} onClose={() => setDeleteTarget(null)} onDeleted={async () => { await load(); setDeleteTarget(null); }} />}
      {quickViewDeployment && <DeploymentQuickView deployment={quickViewDeployment} logs={logs} logsLoading={logsLoading} logsError={logsError} onClose={() => setQuickViewID("")} onOpenDetails={() => { setQuickViewID(""); navigateRoute({ view: "deployments", deploymentID: quickViewDeployment.id }, { state: { deploymentEntry: true } }); }} onCancel={async () => { try { await api.cancel(quickViewDeployment.id); await load(); } catch (cause) { setError((cause as Error).message); } }} />}
    </div>
  );
}

function Nav({ open, view, overview, onClose, onNavigate }: { open: boolean; view: View; overview: Overview | null; onClose: () => void; onNavigate: (view: View) => void }) {
  const railRef = useRef<HTMLElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const frame = window.requestAnimationFrame(() => railRef.current?.querySelector<HTMLElement>('[aria-current="page"]')?.focus());
    const handleKey = (event: KeyboardEvent) => { if (event.key === "Escape") closeRef.current(); };
    window.addEventListener("keydown", handleKey);
    return () => { window.cancelAnimationFrame(frame); window.removeEventListener("keydown", handleKey); previous?.focus(); };
  }, [open]);
  const groups: Array<{ label: string; entries: Array<{ id: View; label: string; icon: ReactNode; count: number }> }> = [
    { label: "Operate", entries: [
      { id: "deployments", label: "Deployments", icon: <RocketLaunch size={18} />, count: overview?.deployments.length ?? 0 },
      { id: "applications", label: "Applications", icon: <AppWindow size={18} />, count: overview?.apps.length ?? 0 },
      { id: "events", label: "Events", icon: <Lightning size={18} />, count: (overview?.eventTriggers.length ?? 0) + (overview?.previewGroups.length ?? 0) },
    ] },
    { label: "Configure", entries: [
      { id: "projects", label: "Projects", icon: <FolderSimple size={18} />, count: overview?.projects.length ?? 0 },
      { id: "servers", label: "Servers", icon: <HardDrives size={18} />, count: overview?.servers.length ?? 0 },
      { id: "secrets", label: "Secrets", icon: <Key size={18} />, count: overview?.secrets.length ?? 0 },
      { id: "connections", label: "Connections", icon: <PlugsConnected size={18} />, count: (overview?.githubApps.length ?? 0) + (overview?.secretStores?.length ?? 0) },
    ] },
  ];

  return <>
    <div className={`nav-scrim ${open ? "visible" : ""}`} onClick={onClose} />
    <aside ref={railRef} id="primary-navigation" className={`rail ${open ? "open" : ""}`} aria-label="Primary navigation">
      <div className="wordmark"><Mark /><div><span>Dispatch</span></div><button aria-label="Close navigation" onClick={onClose}><X size={20} weight="bold" /></button></div>
      <nav className="nav-list">
        {groups.map((group) => <div className="nav-group" key={group.label}><span className="nav-eyebrow">{group.label}</span>{group.entries.map((entry) => <a key={entry.id} href={routePath({ view: entry.id })} className={view === entry.id ? "active" : ""} aria-current={view === entry.id ? "page" : undefined} onClick={(event) => { if (!shouldHandleNavigation(event)) return; event.preventDefault(); onNavigate(entry.id); }}>{entry.icon}<span>{entry.label}</span>{entry.count > 0 && <small aria-hidden="true">{entry.count}</small>}</a>)}</div>)}
      </nav>
      <footer className="rail-foot"><div className="control-mark"><span className={overview?.demo ? "demo-dot" : "live-dot"} /><div><strong>Control plane</strong><span>{overview?.demo ? "Demonstration mode" : "Connected"}</span></div></div></footer>
    </aside>
  </>;
}


export function DeploymentsPage({ overview, onSelect, onOpen, onCreateApplication }: { overview: Overview; onSelect: (id: string) => void; onOpen: (dialog: Dialog) => void; onCreateApplication: () => void }) {
  const groups = useMemo(() => groupDeployments(overview.deployments), [overview.deployments]);
  const runnableApps = overview.apps.filter((app) => !app.template);
  const ready = overview.servers.some((server) => server.state === "ready" && server.runtime !== "relay");
  const canAddApp = ready && overview.projects.length > 0;
  const action = runnableApps.length > 0
    ? { label: "Deploy revision", onClick: () => onOpen("deploy"), icon: <RocketLaunch size={16} /> }
    : canAddApp ? { label: "Add application", onClick: onCreateApplication } : undefined;

  return <div className="page-layout">
    <PageHeader view="deployments" action={action} />
    <div className="deployment-workspace">
      <section className="deployment-board" aria-label="Deployment activity">
        {overview.deployments.length === 0 && <EmptyState title="No deployment activity" body={runnableApps.length ? "Deploy a revision to start." : "Applications appear here after their first deployment."} action={action} />}
        {groups.active.length > 0 && <DeploymentGroup title="In progress" count={groups.active.length} deployments={groups.active} onSelect={onSelect} />}
        {groups.failed.length > 0 && <DeploymentGroup title="Needs attention" count={groups.failed.length} deployments={groups.failed} onSelect={onSelect} clusterLabel="failed attempts" />}
        {groups.latest.length > 0 && <DeploymentGroup title="Latest deployments" count={groups.latest.length} deployments={groups.latest} onSelect={onSelect} clusterLabel="applications" />}
        {groups.history.length > 0 && <DeploymentHistory deployments={groups.history} onSelect={onSelect} />}
      </section>
    </div>
  </div>;
}

export function DeploymentDetailsPage({ deployment, section = "summary", onSectionChange = () => undefined, logs, logsLoading, logsError, onBack, onCancel }: { deployment: Deployment; section?: DeploymentSection; onSectionChange?: (section: DeploymentSection) => void; logs: DeploymentLog[]; logsLoading: boolean; logsError: string; onBack: () => void; onCancel: () => void }) {
  const titleID = `${deploymentEvidenceID(deployment.id)}-title`;
  return <div className="page-layout deployment-details-page">
    <PageHeader view="deployments" title="Deployment details" action={{ label: "Back to deployments", href: routePath({ view: "deployments" }), onClick: onBack, icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <nav className="application-sections deployment-sections" aria-label="Deployment details"><DeploymentSectionLink id="summary" label="Summary" current={section} deploymentID={deployment.id} onSelect={onSectionChange} /><DeploymentSectionLink id="topology" label="Topology" current={section} deploymentID={deployment.id} onSelect={onSectionChange} /><DeploymentSectionLink id="values" label="Values" current={section} deploymentID={deployment.id} onSelect={onSectionChange} /><DeploymentSectionLink id="manifests" label="Manifests" current={section} deploymentID={deployment.id} onSelect={onSectionChange} /></nav>
    {section === "summary" && <section id={deploymentEvidenceID(deployment.id)} className="deployment-evidence deployment-detail-surface" aria-labelledby={titleID}>
      <Evidence deployment={deployment} logs={logs} logsLoading={logsLoading} logsError={logsError} titleID={titleID} onCancel={onCancel} />
    </section>}
    {section !== "summary" && <DeploymentRuntime deployment={deployment} section={section} />}
  </div>;
}

function DeploymentSectionLink({ id, label, current, deploymentID, onSelect }: { id: DeploymentSection; label: string; current: DeploymentSection; deploymentID: string; onSelect: (section: DeploymentSection) => void }) {
  return <a className={current === id ? "active" : ""} aria-current={current === id ? "page" : undefined} href={routePath({ view: "deployments", deploymentID, deploymentSection: id })} onClick={(event) => { if (!shouldHandleNavigation(event)) return; event.preventDefault(); onSelect(id); }}>{label}</a>;
}

export function DeploymentQuickView({ deployment, logs, logsLoading, logsError, onClose, onOpenDetails, onCancel }: { deployment: Deployment; logs: DeploymentLog[]; logsLoading: boolean; logsError: string; onClose: () => void; onOpenDetails: () => void; onCancel: () => void }) {
  const titleID = `${deploymentEvidenceID(deployment.id)}-quick-title`;
  const dialogRef = useDialogFocus(onClose);
  return <div className="dialog-layer deployment-quick-layer" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialogRef} className="deployment-evidence deployment-quick-dialog" role="dialog" aria-modal="true" aria-labelledby={titleID}>
      <Evidence deployment={deployment} logs={logs} logsLoading={logsLoading} logsError={logsError} titleID={titleID} onCancel={onCancel} quickView headerActions={<>
        <a className="evidence-icon-action" href={routePath({ view: "deployments", deploymentID: deployment.id })} aria-label="Open deployment details page" title="Open full page" onClick={(event) => { if (!shouldHandleNavigation(event)) return; event.preventDefault(); onOpenDetails(); }}><ArrowSquareOut size={17} /></a>
        <button type="button" data-autofocus aria-label="Close deployment preview" title="Close" onClick={onClose}><X size={18} /></button>
      </>} />
    </section>
  </div>;
}

function MissingDeploymentPage({ onBack }: { onBack: () => void }) {
  return <div className="page-layout"><PageHeader view="deployments" title="Deployment not found" action={{ label: "Back to deployments", href: routePath({ view: "deployments" }), onClick: onBack, icon: <ArrowLeft size={16} />, tone: "quiet" }} /><EmptyState title="This deployment is unavailable" body="This deployment no longer exists, or the URL is incomplete." /></div>;
}

type EventHookTarget = { type: "trigger"; trigger: EventTrigger } | { type: "group"; group: PreviewGroup };

function EventsPage({ overview, section, onSectionChange, onConfigure, onChanged }: { overview: Overview; section: EventSection; onSectionChange: (section: EventSection) => void; onConfigure: () => void; onChanged: () => Promise<void> }) {
  const [hookTarget, setHookTarget] = useState<EventHookTarget | null>(null);
  const rules = [
    ...overview.previewGroups.map((group) => ({
      id: `group-${group.id}`,
      hookTarget: { type: "group", group } as EventHookTarget,
      name: group.name,
      repositories: group.components.map((component) => component.repository),
      command: group.command,
      targetLabel: `${group.components.length} component${group.components.length === 1 ? "" : "s"}`,
      hooks: `${group.components.filter((component) => component.preDeployHook || component.postDeployHook).length} of ${group.components.length}`,
      enabled: group.enabled,
    })),
    ...overview.eventTriggers.map((trigger) => ({
      id: `trigger-${trigger.id}`,
      hookTarget: { type: "trigger", trigger } as EventHookTarget,
      name: overview.apps.find((application) => application.id === trigger.appId)?.name ?? "Application event",
      repositories: [trigger.repository],
      command: trigger.command,
      targetLabel: "Application",
      hooks: trigger.preDeployHook || trigger.postDeployHook ? "Configured" : "None",
      enabled: trigger.enabled,
    })),
  ];
  const activity = [
    ...overview.previewGroupRuns.map((run) => ({
      id: `group-run-${run.id}`,
      name: run.group?.name ?? overview.previewGroups.find((group) => group.id === run.groupId)?.name ?? run.slug,
      sources: run.sources.map((source) => source.pullRequest ? `${source.repository} #${source.pullRequest}` : `${source.repository} ${source.headRef}`),
      state: run.state,
      url: run.entrypointUrl,
      updatedAt: run.updatedAt,
    })),
    ...overview.previews.map((preview) => ({
      id: `preview-${preview.id}`,
      name: overview.apps.find((application) => application.id === preview.appId)?.name ?? preview.repository,
      sources: [`${preview.repository} #${preview.pullRequestNumber}`],
      state: preview.state,
      url: preview.url,
      updatedAt: preview.updatedAt,
    })),
  ].sort((left, right) => new Date(right.updatedAt).getTime() - new Date(left.updatedAt).getTime());

  if (hookTarget) return <div className="page-layout editor-page">
    <PageHeader view="events" action={{ label: "Back to events", onClick: () => setHookTarget(null), icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <EventHookEditor key={hookTarget.type === "trigger" ? hookTarget.trigger.id : hookTarget.group.id} target={hookTarget} secrets={overview.secrets} onClose={() => setHookTarget(null)} onSaved={async () => { setHookTarget(null); await onChanged(); }} />
  </div>;

  return <div className="page-layout events-page">
    <PageHeader view="events" action={section === "rules" ? { label: "Configure in applications", onClick: onConfigure } : undefined} />
    <nav className="application-sections" aria-label="Event views">
      <a href={routePath({ view: "events" })} aria-current={section === "rules" ? "page" : undefined} className={section === "rules" ? "active" : ""} onClick={(event) => { if (!shouldHandleNavigation(event)) return; event.preventDefault(); onSectionChange("rules"); }}>Rules <span>{rules.length}</span></a>
      <a href={routePath({ view: "events", eventSection: "activity" })} aria-current={section === "activity" ? "page" : undefined} className={section === "activity" ? "active" : ""} onClick={(event) => { if (!shouldHandleNavigation(event)) return; event.preventDefault(); onSectionChange("activity"); }}>Activity <span>{activity.length}</span></a>
    </nav>
    {section === "rules" && <section className="event-section" aria-labelledby="event-rules-title">
      <div className="section-toolbar"><div><h2 id="event-rules-title">Pull request rules</h2></div></div>
      {rules.length ? <div className="resource-table-wrap"><table className="resource-table event-table"><thead><tr><th>Rule</th><th>Trigger</th><th>Target</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{rules.map((rule) => <tr key={rule.id}><td data-label="Rule"><strong>{rule.name}</strong>{rule.hooks !== "None" && <small>{rule.hooks} hooks</small>}</td><td data-label="Trigger"><code>{rule.command}</code><div className="event-sources">{rule.repositories.map((repository) => <small key={repository}>{repository}</small>)}</div></td><td data-label="Target">{rule.targetLabel}</td><td data-label="Status"><StatusLabel state={rule.enabled ? "enabled" : "disabled"} /></td><td className="row-actions"><TableIconAction label={`Edit deployment hooks for ${rule.name}`} tooltip="Hooks" onClick={() => setHookTarget(rule.hookTarget)}><Lightning size={16} /></TableIconAction></td></tr>)}</tbody></table></div> : <EmptyState title="No event rules" action={{ label: "Configure in applications", onClick: onConfigure }} />}
    </section>}
    {section === "activity" && <section className="event-section" aria-labelledby="event-activity-title">
      <div className="section-toolbar"><div><h2 id="event-activity-title">Preview activity</h2></div></div>
      {activity.length ? <div className="resource-table-wrap"><table className="resource-table event-table"><thead><tr><th>Environment</th><th>Source</th><th>Status</th><th>Updated</th><th className="actions-head"><span className="sr-only">Preview URL</span></th></tr></thead><tbody>{activity.slice(0, 30).map((item) => <tr key={item.id}><td data-label="Environment"><strong>{item.name}</strong></td><td data-label="Source"><div className="event-sources">{item.sources.map((source) => <code key={source}>{source}</code>)}</div></td><td data-label="Status"><StatusLabel state={item.state} /></td><td data-label="Updated">{relative(item.updatedAt)}</td><td className="row-actions">{item.url && <a className="table-action table-icon-action" data-tooltip="Open" aria-label={`Open preview for ${item.name}`} href={item.url} target="_blank" rel="noreferrer"><ArrowSquareOut size={16} /></a>}</td></tr>)}</tbody></table></div> : <EmptyState title="No preview activity" body="Preview environments will appear here after an event rule is triggered." />}
    </section>}
  </div>;
}

function EventHookEditor({ target, secrets, onClose, onSaved }: { target: EventHookTarget; secrets: Secret[]; onClose: () => void; onSaved: () => Promise<void> }) {
  const trigger = target.type === "trigger" ? target.trigger : undefined;
  const group = target.type === "group" ? target.group : undefined;
  const [preDeployHook, setPreDeployHook] = useState(trigger?.preDeployHook ?? "");
  const [postDeployHook, setPostDeployHook] = useState(trigger?.postDeployHook ?? "");
	const [secretIds, setSecretIds] = useState<string[]>(trigger?.secretIds ?? []);
  const [components, setComponents] = useState<PreviewGroupComponent[]>(group?.components.map((component) => ({ ...component })) ?? []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  function changeComponent(index: number, values: Partial<PreviewGroupComponent>) {
    setComponents((current) => current.map((component, componentIndex) => componentIndex === index ? { ...component, ...values } : component));
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (trigger) {
        await api.updateEventTrigger(trigger.id, { command: trigger.command, enabled: trigger.enabled, preDeployHook, postDeployHook, secretIds });
      } else if (group) {
        await api.updatePreviewGroup(group.id, { name: group.name, githubAppId: group.githubAppId ?? "", command: group.command, enabled: group.enabled, components });
      }
      await onSaved();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const title = trigger ? overviewRuleName(trigger) : group!.name;
  return <section className="event-hook-editor" aria-labelledby="event-hook-editor-title">
    <header><div><h2 id="event-hook-editor-title">Deployment hooks: {title}</h2></div></header>
    <form onSubmit={save} aria-busy={busy}>
      {trigger ? <HookFields preDeployHook={preDeployHook} postDeployHook={postDeployHook} onPreDeployHook={setPreDeployHook} onPostDeployHook={setPostDeployHook} /> : components.map((component, index) => <fieldset className="event-component-hooks" key={component.id ?? component.alias}><legend>{component.alias || `Component ${index + 1}`} <span>{component.repository}</span></legend><HookFields preDeployHook={component.preDeployHook ?? ""} postDeployHook={component.postDeployHook ?? ""} onPreDeployHook={(value) => changeComponent(index, { preDeployHook: value })} onPostDeployHook={(value) => changeComponent(index, { postDeployHook: value })} /><HookCredentialBindings secrets={secrets} selected={component.secretIds ?? []} onChange={(secretIds) => changeComponent(index, { secretIds })} /></fieldset>)}
	  {trigger && <HookCredentialBindings secrets={secrets} selected={secretIds} onChange={setSecretIds} />}
      <details className="event-hook-context"><summary>Event variables (8)</summary><div>{["DISPATCH_PREVIEW_TAG", "DISPATCH_EVENT_REPOSITORY", "DISPATCH_EVENT_PULL_REQUEST_NUMBER", "DISPATCH_EVENT_HEAD_REF", "DISPATCH_EVENT_HEAD_SHA", "DISPATCH_EVENT_ACTOR", "DISPATCH_EVENT_COMMAND", "DISPATCH_EVENT_ARGUMENTS"].map((name) => <code key={name}>{name}</code>)}</div></details>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="builder-actions"><button type="button" className="quiet-button" onClick={onClose}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? "Saving..." : "Save hooks"}</button></div>
    </form>
  </section>;
}


function overviewRuleName(trigger: EventTrigger) {
  return `${trigger.repository} ${trigger.command}`;
}


export function ServersPage({ overview, onChanged, onAdd, onEdit, onRepair, onDelete, onTopology }: { overview: Overview; onChanged: () => Promise<void>; onAdd: () => void; onEdit: (server: Server) => void; onRepair: (server: Server) => void; onDelete: (server: Server) => void; onTopology?: (server: Server) => void }) {
  const targets = overview.servers.filter((server) => server.runtime !== "relay");
  const relays = overview.servers.filter((server) => server.runtime === "relay");
  const ready = targets.filter((server) => server.state === "ready").length;
  const connected = relays.filter((server) => server.state === "connected").length;
  return <div className="page-layout">
    <PageHeader view="servers" action={{ label: "Add server", onClick: onAdd }} />
    <ResourceSummary items={[{ label: "Targets", value: targets.length }, { label: "Target ready", value: ready }, { label: "Relays", value: relays.length }, { label: "Relay connected", value: connected }]} />
    <section className="server-section"><div className="section-title"><div><h2>Deployment targets</h2></div></div>
      {targets.length ? <div className="resource-table-wrap"><table className="resource-table server-table"><thead><tr><th>Server</th><th>Connection</th><th>Runtime</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{targets.map((server) => <tr key={server.id}><td data-label="Server"><strong>{server.name}</strong>{server.address === "local" && <small className="managed-label"><LockSimple size={12} />Managed</small>}</td><td data-label="Connection"><span className="connection">{server.address === "local" ? "Local Docker socket" : server.runtime === "openshift" ? server.address : server.kubernetes?.context || server.kubernetes?.kubeconfigPath || server.address}</span></td><td data-label="Runtime">{server.runtime === "docker" ? "Docker" : server.runtime === "openshift" ? "OpenShift" : "Kubernetes"}</td><td data-label="Status"><StatusLabel state={server.state} /></td><td className="row-actions"><div className="table-icon-actions">{onTopology && <TableIconAction label={`View deployments on ${server.name}`} tooltip="Topology" onClick={() => onTopology(server)}><HardDrives size={16} /></TableIconAction>}{server.address !== "local" && <>{server.runtime === "openshift" && <TableIconAction label={`Repair ${server.name}`} tooltip="Repair" onClick={() => onRepair(server)}><ArrowClockwise size={16} /></TableIconAction>}<TableIconAction label={`Edit ${server.name}`} tooltip="Edit" onClick={() => onEdit(server)}><PencilSimple size={16} /></TableIconAction><TableIconAction label={`Delete ${server.name}`} tooltip="Delete" danger onClick={() => onDelete(server)}><Trash size={16} /></TableIconAction></>}</div></td></tr>)}</tbody></table></div> : <div className="section-empty">No deployment targets.</div>}
    </section>
    <section className="server-section relay-section"><div className="section-title"><div><h2>Event relays</h2></div></div>
      {relays.length ? <div className="relay-server-list">{relays.map((server) => <RelayServerRow key={server.id} server={server} webhooks={overview.relayWebhooks.filter((hook) => hook.serverId === server.id)} connections={overview.githubApps} onChanged={onChanged} onEdit={() => onEdit(server)} onDelete={() => onDelete(server)} />)}</div> : <div className="section-empty">No event relays.</div>}
    </section>
  </div>;
}

function RelayServerRow({ server, webhooks, connections, onChanged, onEdit, onDelete }: { server: Server; webhooks: RelayWebhook[]; connections: GitHubAppConnection[]; onChanged: () => Promise<void>; onEdit: () => void; onDelete: () => void }) {
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [provider, setProvider] = useState("github");
  const [connectionID, setConnectionID] = useState(connections[0]?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");

  async function verify() { setBusy(true); setError(""); try { await api.verifyRelayServer(server.id); await onChanged(); } catch (cause) { setError((cause as Error).message); } finally { setBusy(false); } }
  async function add(event: FormEvent) { event.preventDefault(); setBusy(true); setError(""); try { await api.createRelayWebhook(server.id, { name, provider, providerConnectionId: provider === "github" ? connectionID : undefined }); setName(""); setAdding(false); await onChanged(); } catch (cause) { setError((cause as Error).message); } finally { setBusy(false); } }
  async function remove(hook: RelayWebhook) { setBusy(true); setError(""); try { await api.deleteRelayWebhook(server.id, hook.id); await onChanged(); } catch (cause) { setError((cause as Error).message); } finally { setBusy(false); } }
  async function copy(hook: RelayWebhook) { await navigator.clipboard.writeText(hook.url); setCopied(hook.id); window.setTimeout(() => setCopied(""), 1600); }

  return <article className="relay-server-row">
    <div className="relay-server-summary"><div className="relay-server-name"><strong>{server.name}</strong><span>{new URL(server.address).host}</span></div><div className="relay-server-metric"><span>Queue</span><strong>{server.relay?.pendingEvents ?? 0} pending</strong></div><StatusLabel state={server.state} /><div className="row-actions"><button disabled={busy} onClick={() => void verify()}><ArrowClockwise size={15} />Test</button><button onClick={onEdit}><PencilSimple size={15} />Edit</button><button className="delete-action" onClick={onDelete}><Trash size={15} />Delete</button></div></div>
    {server.relay?.lastError && <p className="relay-error" role="status">{server.relay.lastError}</p>}
    <details className="relay-webhooks"><summary><span>Webhook endpoints</span><small>{webhooks.length}</small></summary><div className="relay-webhook-body">
      {webhooks.length ? <div className="relay-webhook-list">{webhooks.map((hook) => <div key={hook.id}><span><strong>{hook.name}</strong><small>{hook.provider}{hook.lastDeliveryAt ? `, last event ${relative(hook.lastDeliveryAt)}` : ""}</small></span><code>{hook.url}</code><button onClick={() => void copy(hook)}>{copied === hook.id ? <Check size={14} /> : <Copy size={14} />}{copied === hook.id ? "Copied" : "Copy URL"}</button><button className="delete-action" disabled={busy} onClick={() => void remove(hook)}><Trash size={14} />Remove</button></div>)}</div> : <p className="relay-webhook-empty">No endpoints.</p>}
      {adding ? <form className="relay-webhook-form" onSubmit={add}><label><span>Name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="Repository events" required /></label><label><span>Provider</span><input value={provider} onChange={(event) => setProvider(event.target.value.toLowerCase())} placeholder="github" pattern="[a-z][a-z0-9_-]{0,31}" required /></label>{provider === "github" && <label><span>GitHub connection</span><select value={connectionID} onChange={(event) => setConnectionID(event.target.value)} required><option value="">Select a connection</option>{connections.map((connection) => <option key={connection.id} value={connection.id}>{connection.name}</option>)}</select></label>}<div className="relay-webhook-actions"><button type="button" className="quiet-button" onClick={() => setAdding(false)}>Cancel</button><button className="primary-button" disabled={busy || !name.trim() || !provider.trim() || (provider === "github" && !connectionID)}>{busy ? "Creating..." : "Create endpoint"}</button></div></form> : <button className="quiet-button relay-add-webhook" onClick={() => setAdding(true)}><Plus size={15} />Add endpoint</button>}
      {error && <p className="form-error" role="alert">{error}</p>}
    </div></details>
  </article>;
}

function ProjectsPage({ overview, onAdd, onEdit, onDelete }: { overview: Overview; onAdd: () => void; onEdit: (project: Project) => void; onDelete: (project: Project) => void }) {
  return <div className="page-layout">
    <PageHeader view="projects" action={{ label: "Add project", onClick: onAdd }} />
    <ResourceSummary items={[{ label: "Projects", value: overview.projects.length }, { label: "Applications", value: overview.apps.length }]} />
    {overview.projects.length ? <div className="resource-table-wrap"><table className="resource-table"><thead><tr><th>Project</th><th>Description</th><th>Applications</th><th>Created</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{overview.projects.map((project) => <tr key={project.id}><td data-label="Project"><strong>{project.name}</strong></td><td data-label="Description">{project.description || <span className="muted-value">None</span>}</td><td data-label="Applications">{overview.apps.filter((app) => app.projectId === project.id).length}</td><td data-label="Created">{new Date(project.createdAt).toLocaleDateString()}</td><td className="row-actions"><div className="table-icon-actions"><TableIconAction label={`Edit ${project.name}`} tooltip="Edit" onClick={() => onEdit(project)}><PencilSimple size={16} /></TableIconAction><TableIconAction label={`Delete ${project.name}`} tooltip="Delete" danger onClick={() => onDelete(project)}><Trash size={16} /></TableIconAction></div></td></tr>)}</tbody></table></div> : <EmptyState title="No projects" action={{ label: "Add project", onClick: onAdd }} />}
  </div>;
}

const secretTypeOptions: Array<{ value: SecretType; label: string; defaultName: string; environmentVariable: string; placeholder: string }> = [
  { value: "text", label: "Text", defaultName: "", environmentVariable: "SECRET_VALUE", placeholder: "Enter a secret value" },
  { value: "api_token", label: "API token", defaultName: "API token", environmentVariable: "API_TOKEN", placeholder: "Paste an API token" },
  { value: "github_token", label: "GitHub token", defaultName: "GitHub token", environmentVariable: "GITHUB_TOKEN", placeholder: "Paste a GitHub personal access token" },
  { value: "ssh_private_key", label: "SSH private key", defaultName: "Global deploy key", environmentVariable: "SSH_PRIVATE_KEY", placeholder: "Paste an OpenSSH or PEM private key" },
  { value: "registry_password", label: "Registry password", defaultName: "Registry password", environmentVariable: "REGISTRY_PASSWORD", placeholder: "Enter the registry password" },
];

const secretTypeLabel = (type: SecretType) => secretTypeOptions.find((option) => option.value === type)?.label ?? "Text";


export function SecretsPage({ overview, onChanged, onDelete }: { overview: Overview; onChanged: () => Promise<void>; onDelete: (secret: Secret) => void }) {
  const [editing, setEditing] = useState<Secret | null>(null);
  const [creating, setCreating] = useState(false);
  const [secretType, setSecretType] = useState<SecretType>("text");
  const [secretSource, setSecretSource] = useState<SecretSource>("local");
  const [name, setName] = useState("");
  const [environmentVariable, setEnvironmentVariable] = useState("SECRET_VALUE");
  const [value, setValue] = useState("");
  const [externalStoreID, setExternalStoreID] = useState("");
  const [externalSecretID, setExternalSecretID] = useState("");
  const [externalField, setExternalField] = useState("");
  const [sshSource, setSSHSource] = useState<"generate" | "existing">("generate");
  const [fileName, setFileName] = useState("");
  const [copiedID, setCopiedID] = useState("");
  const [publicKeySecret, setPublicKeySecret] = useState<Secret | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fileInput = useRef<HTMLInputElement>(null);

  function open(secret?: Secret) {
    const type = secret?.type ?? "text";
    setEditing(secret ?? null);
    setCreating(true);
    setSecretType(type);
    setSecretSource(secret?.source ?? "local");
    setName(secret?.name ?? "");
    setEnvironmentVariable(secret?.environmentVariable ?? secretTypeOptions.find((option) => option.value === type)?.environmentVariable ?? "SECRET_VALUE");
    setValue("");
    setExternalStoreID(secret?.externalStoreId ?? overview.secretStores?.[0]?.id ?? "");
    setExternalSecretID(secret?.externalSecretId ?? "");
    setExternalField(secret?.externalField ?? "");
    setSSHSource(secret ? "existing" : "generate");
    setFileName("");
    setError("");
  }

  function closeEditor() {
    setCreating(false);
    setEditing(null);
    setValue("");
    setExternalSecretID("");
    setExternalField("");
    setFileName("");
    setError("");
  }

  function changeType(next: SecretType) {
    const previous = secretTypeOptions.find((option) => option.value === secretType);
    const selected = secretTypeOptions.find((option) => option.value === next)!;
    if (!name.trim() || name === previous?.defaultName) setName(selected.defaultName);
    if (!environmentVariable.trim() || environmentVariable === previous?.environmentVariable) setEnvironmentVariable(selected.environmentVariable);
    setSecretType(next);
    setSSHSource(next === "ssh_private_key" && !editing ? "generate" : "existing");
    setValue("");
    setFileName("");
    setError("");
  }

  async function loadSecretFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    setError("");
    try {
      setValue(await readSecretTextFile(file));
      setFileName(file.name);
      setSSHSource("existing");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      event.target.value = "";
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const generate = secretSource === "local" && secretType === "ssh_private_key" && sshSource === "generate";
    const reference = secretSource === "external" ? { externalStoreId: externalStoreID, externalSecretId: externalSecretID, ...(externalField.trim() ? { externalField } : {}) } : {};
    try {
      const saved = editing
        ? await api.updateSecret(editing.id, { name, type: secretType, source: secretSource, environmentVariable, ...reference, ...(generate ? { generate: true } : secretSource === "local" && value ? { value } : {}) })
        : await api.createSecret({ name, type: secretType, source: secretSource, environmentVariable, ...reference, ...(generate ? { generate: true } : secretSource === "local" ? { value } : {}) });
      setCreating(false);
      setEditing(null);
      setValue("");
      setFileName("");
      await onChanged();
      if (generate && saved.publicValue) setPublicKeySecret(saved);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function copyPublicKey(secret: Secret) {
    if (!secret.publicValue) return;
    try {
      await navigator.clipboard.writeText(secret.publicValue);
      setCopiedID(secret.id);
      window.setTimeout(() => setCopiedID((current) => current === secret.id ? "" : current), 1800);
    } catch {
      setError("Could not copy the public key. Select and copy it manually.");
    }
  }

  const selectedType = secretTypeOptions.find((option) => option.value === secretType)!;
  const usesGeneratedKey = secretSource === "local" && secretType === "ssh_private_key" && sshSource === "generate";
  const accept = secretType === "ssh_private_key" ? ".key,.pem,text/plain,application/x-pem-file" : ".txt,.env,.token,.key,.pem,text/plain";

  return <div className="page-layout">
    <PageHeader view="secrets" action={creating
      ? { label: "Cancel", onClick: closeEditor, icon: <X size={16} weight="bold" />, tone: "quiet" }
      : { label: "Add secret", onClick: () => open(), disabled: !overview.secretStorageConfigured }} />
    {!overview.secretStorageConfigured && <div className="error-banner secret-storage-notice" role="status"><strong>Encrypted storage is not configured</strong><span>Set DISPATCH_MASTER_KEY_FILE and restart the controller before adding secrets.</span></div>}
    {creating && <section className="inline-create secret-editor" aria-labelledby="secret-editor-title">
      <header><h2 id="secret-editor-title">{editing ? "Update secret" : "New secret"}</h2></header>
      <div className="inline-create-body"><form className="resource-form secret-form" onSubmit={save}>
        <div className="secret-form-main">
          <fieldset className="secret-storage-source"><legend>Source</legend><div>
            <label><input type="radio" name="secret-source" checked={secretSource === "local"} onChange={() => setSecretSource("local")} /><span><LockSimple size={16} /><strong>Dispatch</strong></span></label>
            <label><input type="radio" name="secret-source" checked={secretSource === "external"} onChange={() => { setSecretSource("external"); setSSHSource("existing"); setValue(""); if (!externalStoreID) setExternalStoreID(overview.secretStores?.[0]?.id ?? ""); }} disabled={!overview.secretStores?.length} /><span><Cloud size={16} /><strong>External store</strong></span></label>
          </div></fieldset>
          <div className="secret-identity-fields">
            <label className="secret-type-field"><span>Type</span><select value={secretType} onChange={(event) => changeType(event.target.value as SecretType)}>{secretTypeOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label>
            <label><span>Name</span><input required maxLength={80} value={name} onChange={(event) => setName(event.target.value)} placeholder={selectedType.defaultName || "Production secret"} /></label>
            <label className="secret-env-field"><span>Environment variable</span><input required maxLength={128} value={environmentVariable} onChange={(event) => setEnvironmentVariable(event.target.value)} placeholder={selectedType.environmentVariable} spellCheck={false} /></label>
          </div>
          {secretSource === "local" && secretType === "ssh_private_key" && <fieldset className="secret-key-source"><legend>Key source</legend><div>
            <label><input type="radio" name="ssh-source" value="generate" checked={sshSource === "generate"} onChange={() => { setSSHSource("generate"); setValue(""); setFileName(""); }} /><span><strong>Generate new key</strong></span></label>
            <label><input type="radio" name="ssh-source" value="existing" checked={sshSource === "existing"} onChange={() => setSSHSource("existing")} /><span><strong>Use existing key</strong></span></label>
          </div></fieldset>}
          {secretSource === "external" ? <div className="external-secret-fields">
            <label><span>Secret store</span><select value={externalStoreID} onChange={(event) => setExternalStoreID(event.target.value)} required><option value="">Choose a store</option>{(overview.secretStores ?? []).map((store) => <option key={store.id} value={store.id}>{store.name}</option>)}</select></label>
            <label><span>Secret ID</span><input value={externalSecretID} onChange={(event) => setExternalSecretID(event.target.value)} placeholder="Secret UUID" required spellCheck={false} /></label>
            <label><span>Value field</span><input value={externalField} onChange={(event) => setExternalField(event.target.value)} placeholder="Optional, for example credentials.password" spellCheck={false} /></label>
          </div> : !usesGeneratedKey && <div className="secret-value-field">
            <div className="secret-value-heading"><div><label htmlFor="secret-value">{editing ? "New value (optional)" : "Secret value"}</label>{editing && <small>Leave blank to keep the current value.</small>}</div><div className="secret-upload"><input ref={fileInput} className="sr-only" type="file" accept={accept} aria-label="Choose a secret file" onChange={(event) => void loadSecretFile(event)} /><button type="button" className="quiet-button" onClick={() => fileInput.current?.click()}><UploadSimple size={15} />Upload file</button><span aria-live="polite">{fileName || "64 KiB max"}</span></div></div>
            <textarea id="secret-value" required={!editing} value={value} onChange={(event) => { setValue(event.target.value); setFileName(""); }} placeholder={editing ? "Leave blank to keep the value" : selectedType.placeholder} spellCheck={false} />
          </div>}
          {error && <p className="form-error" role="alert">{error}</p>}
          <div className="secret-form-actions">{usesGeneratedKey && <p><LockSimple size={15} weight="bold" /><span>{editing ? "This replaces the current key." : "Only the public key remains visible."}</span></p>}<button type="button" className="quiet-button" onClick={closeEditor}>Cancel</button><button className="primary-button" disabled={busy || !name.trim() || !environmentVariable.trim() || (secretSource === "external" ? !externalStoreID || !externalSecretID.trim() : !editing && !usesGeneratedKey && !value.trim())}>{busy ? "Saving..." : usesGeneratedKey ? editing ? "Replace key" : "Generate key" : editing ? "Update secret" : "Save secret"}</button></div>
        </div>
      </form></div>
    </section>}
    {!creating && error && <p className="form-error" role="alert">{error}</p>}
    {!creating && (overview.secrets.length ? <div className="resource-table-wrap"><table className="resource-table secret-table"><thead><tr><th>Secret</th><th>Type</th><th>Source</th><th>Environment variable</th><th>Used by</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{overview.secrets.map((secret) => { const hookUses = overview.eventTriggers.filter((trigger) => trigger.secretIds.includes(secret.id)).length + overview.previewGroups.flatMap((group) => group.components).filter((component) => component.secretIds?.includes(secret.id)).length + overview.apps.filter((app) => app.hookSecretIds?.includes(secret.id)).length; const sourceUses = overview.apps.filter((app) => app.sourceCredentialId === secret.id).length; const uses = hookUses + sourceUses; const store = overview.secretStores?.find((item) => item.id === secret.externalStoreId); return <tr key={secret.id}><td data-label="Secret"><strong>{secret.name}</strong></td><td data-label="Type"><span className="secret-type-label">{secretTypeLabel(secret.type)}</span></td><td data-label="Source"><span className="secret-source-label">{secret.source === "external" ? store?.name ?? "External" : "Dispatch"}</span></td><td data-label="Environment variable"><code>{secret.environmentVariable}</code></td><td data-label="Used by">{uses}</td><td className="row-actions"><div className="table-icon-actions">{secret.publicValue && <TableIconAction label={`View public key for ${secret.name}`} tooltip="Public key" onClick={() => setPublicKeySecret(secret)}><Key size={16} /></TableIconAction>}<TableIconAction label={`Edit ${secret.name}`} tooltip="Edit" onClick={() => open(secret)}><PencilSimple size={16} /></TableIconAction><TableIconAction label={`Delete ${secret.name}`} tooltip="Delete" danger onClick={() => onDelete(secret)}><Trash size={16} /></TableIconAction></div></td></tr>; })}</tbody></table></div> : <div><EmptyState title="No secrets" action={overview.secretStorageConfigured ? { label: "Add secret", onClick: () => open() } : undefined} /></div>)}
    {publicKeySecret && <PublicKeyDialog secret={publicKeySecret} copied={copiedID === publicKeySecret.id} onCopy={() => void copyPublicKey(publicKeySecret)} onClose={() => setPublicKeySecret(null)} />}
  </div>;
}


function ResourceSummary({ items }: { items: Array<{ label: string; value: number }> }) {
  return <dl className="resource-summary" aria-label="Resource totals">{items.map((item) => <div key={item.label}><dd>{item.value}</dd><dt>{item.label}</dt></div>)}</dl>;
}


function EmptyState({ title, body, action }: { title: string; body?: string; action?: { label: string; onClick: () => void } }) {
  return <section className="empty-state"><Mark /><div><h2>{title}</h2>{body && <p>{body}</p>}</div>{action && <button className="primary-button" onClick={action.onClick}><Plus size={15} weight="bold" />{action.label}</button>}</section>;
}



function PublicKeyDialog({ secret, copied, onCopy, onClose }: { secret: Secret; copied: boolean; onCopy: () => void; onClose: () => void }) {
  const dialogRef = useDialogFocus(onClose);
  return <div className="dialog-layer drawer-layer">
    <section ref={dialogRef} className="resource-dialog resource-drawer public-key-dialog" role="dialog" aria-modal="true" aria-labelledby="public-key-title" aria-describedby="public-key-description">
      <header><div><h2 id="public-key-title">Public key</h2><p id="public-key-description">Add this to your Git host.</p></div><button aria-label="Close public key" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        <div className="public-key-summary"><Key size={20} /><div><strong>{secret.name}</strong><span>Ed25519 deploy key</span></div></div>
        <label className="public-key-value"><span>Public key</span><textarea readOnly value={secret.publicValue ?? ""} aria-label={`Public key for ${secret.name}`} /></label>
        <p className="key-privacy-note"><LockSimple size={16} />Private key unavailable.</p>
        <div className="dialog-actions"><button className="primary-button" onClick={onCopy}>{copied ? <Check size={15} weight="bold" /> : <Copy size={15} />}{copied ? "Copied" : "Copy public key"}</button></div>
      </div>
    </section>
  </div>;
}

function ResourceDialog({ kind, overview, project, server, serverRuntime, deployAppID, onClose, onChanged, onDeployed }: { kind: Exclude<Dialog, null>; overview: Overview; project?: Project; server?: Server; serverRuntime?: Server["runtime"]; deployAppID?: string; onClose: () => void; onChanged: () => Promise<void>; onDeployed: (id: string) => Promise<void> }) {
  const dialogRef = useDialogFocus(onClose);
  const copy = {
    server: server ? { title: "Edit server" } : { title: "Add server" },
    repair: { title: "Repair OpenShift connection" },
    project: project ? { title: "Edit project" } : { title: "Add project" },
    deploy: { title: "Deploy revision" },
  }[kind];
  return <div className="dialog-layer drawer-layer">
    <section ref={dialogRef} className={`resource-dialog resource-drawer ${kind}-drawer`} role="dialog" aria-modal="true" aria-labelledby="dialog-title">
      <header><div><h2 id="dialog-title">{copy.title}</h2></div><button aria-label="Close dialog" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        {kind === "server" && <ServerForm onChanged={onChanged} onCancel={onClose} server={server} secrets={overview.secrets} initialRuntime={serverRuntime} />}
        {kind === "repair" && <ServerForm onChanged={onChanged} onCancel={onClose} server={server} repairing secrets={overview.secrets} />}
        {kind === "project" && <ProjectForm onChanged={onChanged} onCancel={onClose} project={project} />}
        {kind === "deploy" && <DeployForm apps={overview.apps.filter((app) => !app.template)} initialAppID={deployAppID} onComplete={onDeployed} onCancel={onClose} />}
      </div>
    </section>
  </div>;
}

function DeleteDialog({ target, overview, onClose, onDeleted }: { target: DeleteTarget; overview: Overview; onClose: () => void; onDeleted: () => Promise<void> }) {
  const dialogRef = useDialogFocus(onClose);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dependencies = target.kind === "application" || target.kind === "previewGroup" ? 0
    : target.kind === "secret"
      ? overview.apps.filter((app) => app.sourceCredentialId === target.item.id || app.hookSecretIds?.includes(target.item.id)).length + overview.eventTriggers.filter((trigger) => trigger.secretIds.includes(target.item.id)).length + overview.previewGroups.flatMap((group) => group.components).filter((component) => component.secretIds?.includes(target.item.id)).length
      : overview.apps.filter((app) => target.kind === "server" ? app.serverId === target.item.id : app.projectId === target.item.id).length;
  const resource = target.kind === "server" ? "server" : target.kind === "project" ? "project" : target.kind === "secret" ? "secret" : target.kind === "previewGroup" ? "preview group" : target.item.template ? "template" : "application";

  async function remove() {
    setBusy(true);
    setError("");
    try {
      if (target.kind === "server") await api.deleteServer(target.item.id);
      else if (target.kind === "project") await api.deleteProject(target.item.id);
      else if (target.kind === "secret") await api.deleteSecret(target.item.id);
      else if (target.kind === "previewGroup") await api.deletePreviewGroup(target.item.id);
      else await api.deleteApp(target.item.id);
      await onDeleted();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <div className="dialog-layer confirm-layer" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialogRef} className="resource-dialog confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-dialog-title" aria-describedby="delete-dialog-description">
      <header><div><h2 id="delete-dialog-title">Delete {resource}</h2><p>{target.kind === "application" && !target.item.template ? "Dispatch removes deployed resources and history first." : "You cannot undo this action."}</p></div><button aria-label="Close dialog" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        <p className="confirm-copy" id="delete-dialog-description">Delete <strong>{target.item.name}</strong> from Dispatch?</p>
        {dependencies > 0 && <div className="dependency-warning" role="status"><strong>Cannot delete this {resource}</strong><p>{target.kind === "secret" ? `${dependencies} application source or event rule${dependencies === 1 ? " uses" : "s use"} it. Detach ${dependencies === 1 ? "that dependency" : "those dependencies"} first.` : `${dependencies} ${dependencies === 1 ? "application uses" : "applications use"} it. Remove ${dependencies === 1 ? "that application" : "those applications"} first.`}</p></div>}
        {error && <p className="form-error" role="alert">{error}</p>}
        <div className="dialog-actions confirm-actions"><button className="quiet-button" onClick={onClose}>Cancel</button><button className="danger-button" disabled={busy || dependencies > 0} onClick={() => void remove()}>{busy ? "Deleting..." : `Delete ${resource}`}</button></div>
      </div>
    </section>
  </div>;
}

type DeploymentSelectionHandler = (id: string) => void;

function deploymentEvidenceID(id: string) {
  return `deployment-evidence-${id.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
}

function DeploymentGroup({ title, count, deployments, onSelect, clusterLabel = "attempts" }: { title: string; count: number; deployments: Deployment[]; onSelect: DeploymentSelectionHandler; clusterLabel?: string }) {
  const clusters = clusterDeploymentsByApplication(deployments);
  return <section className="deployment-group" aria-labelledby={`deployment-${title.toLowerCase().replaceAll(" ", "-")}`}>
    <div className="group-heading"><h2 id={`deployment-${title.toLowerCase().replaceAll(" ", "-")}`}>{title}</h2><span>{count}</span></div>
    <div className="deployment-clusters">{clusters.map((cluster) => <DeploymentCluster key={cluster.key} clusterKey={cluster.key} deployments={cluster.deployments} onSelect={onSelect} clusterLabel={clusterLabel} />)}</div>
  </section>;
}

function DeploymentHistory({ deployments, onSelect }: { deployments: Deployment[]; onSelect: DeploymentSelectionHandler }) {
  const clusters = clusterDeploymentsByApplication(deployments);
  const applications = clusters.length;
  const [open, setOpen] = useState(() => window.history.state?.deploymentHistoryOpen === true);
  return <details className="deployment-history" open={open} onToggle={(event) => { const next = event.currentTarget.open; setOpen(next); rememberDeploymentListState({ deploymentHistoryOpen: next }); }}>
    <summary><span><strong>Previous deployments</strong><small>{deployments.length} across {applications} application{applications === 1 ? "" : "s"}</small></span><span className="history-toggle">View history</span></summary>
    <div className="deployment-clusters">{clusters.map((cluster) => <DeploymentCluster key={cluster.key} clusterKey={cluster.key} deployments={cluster.deployments} onSelect={onSelect} clusterLabel="previous deployments" />)}</div>
  </details>;
}

function DeploymentCluster({ clusterKey, deployments, onSelect, clusterLabel }: { clusterKey: string; deployments: Deployment[]; onSelect: DeploymentSelectionHandler; clusterLabel: string }) {
  const [latest, ...earlier] = deployments;
  const disclosureKey = `${clusterLabel}:${clusterKey}`;
  const [open, setOpen] = useState(() => rememberedAttemptClusters().includes(disclosureKey));
  return <article className="deployment-cluster">
    <DeploymentStrip deployment={latest} onSelect={() => onSelect(latest.id)} count={deployments.length} countLabel={clusterLabel} />
    {earlier.length > 0 && <details className="attempt-disclosure" open={open} onToggle={(event) => { const next = event.currentTarget.open; setOpen(next); const remembered = rememberedAttemptClusters(); const deploymentAttemptClusters = next ? [...new Set([...remembered, disclosureKey])] : remembered.filter((item) => item !== disclosureKey); rememberDeploymentListState({ deploymentAttemptClusters }); }}>
      <summary>Show {earlier.length} earlier {earlier.length === 1 ? "attempt" : "attempts"}</summary>
      <div>{earlier.map((deployment) => <DeploymentStrip key={deployment.id} deployment={deployment} onSelect={() => onSelect(deployment.id)} nested />)}</div>
    </details>}
  </article>;
}

function DeploymentStrip({ deployment, onSelect, count = 1, countLabel = "attempts", nested = false }: { deployment: Deployment; onSelect: () => void; count?: number; countLabel?: string; nested?: boolean }) {
  const tone = statusTone(deployment.state);
  const message = deployment.message?.trim();
  return <a data-deployment-id={deployment.id} href={routePath({ view: "deployments", deploymentID: deployment.id })} className={`dispatch-strip ${tone} ${nested ? "nested" : ""}`} onClick={(event) => { if (!shouldHandleNavigation(event)) return; event.preventDefault(); onSelect(); }} aria-label={`${deployment.app?.name ?? "Unknown application"}, ${deployment.state}, commit ${short(deployment.commitSha)}, ${deployment.server?.name ?? "no server"}, ${relative(deployment.createdAt)}`}>
    <span className="strip-status" aria-hidden="true">{tone === "success" ? <CheckCircle size={19} weight="fill" /> : tone === "danger" ? <WarningCircle size={19} weight="fill" /> : <CircleNotch size={19} weight="bold" />}</span>
    <span className="strip-identity"><span className="strip-title"><strong>{deployment.app?.name ?? "Unknown app"}</strong>{count > 1 && <small>{count} {countLabel}</small>}</span><span><code>{short(deployment.commitSha)}</code><span>{deployment.server?.name ?? "No server"}</span></span>{message && message.toLowerCase() !== deployment.state && <small className="strip-message" title={message}>{message}</small>}</span>
    <span className="strip-result"><strong>{deployment.state}</strong><time dateTime={deployment.finishedAt ?? deployment.createdAt}>{relative(deployment.finishedAt ?? deployment.createdAt)}</time></span>
  </a>;
}

function Evidence({ deployment, logs, logsLoading, logsError, titleID, onCancel, headerActions, quickView = false }: { deployment: Deployment; logs: DeploymentLog[]; logsLoading: boolean; logsError: string; titleID: string; onCancel: () => void; headerActions?: ReactNode; quickView?: boolean }) {
  const active = !["succeeded", "failed", "cancelled"].includes(deployment.state);
  const [copied, setCopied] = useState("");
  const [logOpen, setLogOpen] = useState(active || deployment.state === "failed");
  const logTitleID = `${titleID}-log`;
  async function copy(kind: string, value: string) {
    await navigator.clipboard.writeText(value);
    setCopied(kind);
    window.setTimeout(() => setCopied((current) => current === kind ? "" : current), 1600);
  }
  const logStatus = <span className={logsError ? "danger" : active ? "active" : ""}>{logsLoading ? "Loading" : logsError ? "Unavailable" : active ? "Streaming" : `${logs.length} ${logs.length === 1 ? "entry" : "entries"}`}</span>;
  const logBody = <div className="log-block"><div className="terminal" role="log" aria-live="polite" aria-busy={logsLoading}>{logsLoading ? <p className="log-state">Loading log entries...</p> : logsError ? <p className="log-state error">Dispatch could not load logs. Retrying automatically.</p> : logs.length ? logs.map((entry) => <div key={entry.id} className={entry.level}><time>{new Date(entry.createdAt).toLocaleTimeString([], { hour12: false })}</time><span>{entry.message}</span></div>) : <p>{active ? "Waiting for the first log entry." : "This deployment has no log entries."}</p>}</div></div>;
  const logPanel = quickView
    ? <section className="evidence-log evidence-log-fixed" aria-labelledby={logTitleID}><div className="evidence-log-heading"><span id={logTitleID}>Deployment log</span>{logStatus}</div>{logBody}</section>
    : <details className="evidence-log" open={logOpen} onToggle={(event) => setLogOpen(event.currentTarget.open)}><summary><span id={logTitleID}>Deployment log</span>{logStatus}</summary>{logBody}</details>;
  const facts = <section className="evidence-facts"><h3>Configuration</h3><dl className="evidence-grid">
    <div><dt>Commit</dt><dd><code>{short(deployment.commitSha, 18)}</code><button type="button" aria-label="Copy commit" onClick={() => void copy("commit", deployment.commitSha)}>{copied === "commit" ? <Check size={13} /> : <Copy size={13} />}</button></dd></div><div><dt>Created</dt><dd>{relative(deployment.createdAt)}</dd></div>
    <div className="digest"><dt>Spec digest</dt><dd><code title={deployment.specDigest}>{short(deployment.specDigest, 28)}</code><button type="button" aria-label="Copy spec digest" onClick={() => void copy("digest", deployment.specDigest)}>{copied === "digest" ? <Check size={13} /> : <Copy size={13} />}</button></dd></div>
    <div><dt>Build</dt><dd>{deployment.app?.buildType}</dd></div><div><dt>Branch</dt><dd>{deployment.app?.branch}</dd></div>
    <div><dt>Server</dt><dd>{deployment.server?.name}</dd></div><div><dt>Runtime</dt><dd>{deployment.server?.runtime}</dd></div>
  </dl></section>;
  const outputs = deployment.outputs && Object.keys(deployment.outputs).length > 0 ? <section className="evidence-outputs"><h3>Published outputs</h3><dl className="evidence-grid">{Object.entries(deployment.outputs).map(([key, value]) => <div key={key}><dt>{key}</dt><dd><code title={value}>{value}</code><button type="button" aria-label={`Copy ${key}`} onClick={() => void copy(`output-${key}`, value)}>{copied === `output-${key}` ? <Check size={13} /> : <Copy size={13} />}</button></dd></div>)}</dl></section> : null;
  return <>
    <div className="evidence-head"><div><span className={`selection-dot ${statusTone(deployment.state)}`} /><div><h2 id={titleID}>{deployment.app?.name}</h2><small><code>{short(deployment.commitSha)}</code> on {deployment.server?.name}</small></div></div><div className="evidence-head-actions"><span className={`stamp ${statusTone(deployment.state)}`}>{deployment.state}</span>{headerActions}</div></div>
    {quickView ? <div className="evidence-content evidence-content-quick"><div className="evidence-metadata-scroll">{facts}{outputs}</div>{logPanel}</div> : <div className="evidence-content">{logPanel}{facts}{outputs}</div>}
    {active && <div className="evidence-actions"><button className="danger-button" onClick={onCancel}>Cancel deployment</button></div>}
  </>;
}

function DeployForm({ apps, initialAppID, onComplete, onCancel }: { apps: AppModel[]; initialAppID?: string; onComplete: (id: string) => Promise<void>; onCancel: () => void }) {
  const [appID, setAppID] = useState(initialAppID && apps.some((app) => app.id === initialAppID) ? initialAppID : apps[0]?.id ?? "");
  const [commit, setCommit] = useState("HEAD");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const app = apps.find((item) => item.id === appID);
      const revision = app?.buildType === "helm" && !app.sourceRepo ? "chart" : app?.sourceRepo ? commit : "inline";
      const created = await api.deploy(appID, revision);
      await onComplete(created.id);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const selectedApp = apps.find((app) => app.id === appID);
  const fixedSource = selectedApp && !selectedApp.sourceRepo;
  const sourceLabel = selectedApp?.buildType === "helm" ? "Helm chart" : "Compose file";
  return <form className="resource-form" onSubmit={submit} aria-busy={busy}><label><span>Application</span><select value={appID} onChange={(event) => setAppID(event.target.value)}>{apps.map((app) => <option value={app.id} key={app.id}>{app.name}</option>)}</select></label>{fixedSource ? <label><span>Source</span><input value={sourceLabel} disabled /></label> : <label><span>Source revision</span><input value={commit} onChange={(event) => setCommit(event.target.value)} placeholder="Branch, tag, or commit" required spellCheck={false} /></label>}{error && <p className="form-error" role="alert">{error}</p>}<div className="dialog-actions"><button type="button" className="quiet-button" onClick={onCancel}>Cancel</button><button className="primary-button" disabled={busy || !appID}>{busy ? "Starting deployment..." : "Deploy"}</button></div></form>;
}

function AuthScreen({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [setupRequired, setSetupRequired] = useState<boolean | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const loadStatus = useCallback(async () => {
    setSetupRequired(null);
    setError("");
    try { const status = await api.authStatus(); setSetupRequired(status.setupRequired); }
    catch { setError("Unable to reach the controller."); }
  }, []);
  useEffect(() => { void loadStatus(); }, [loadStatus]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (setupRequired && password !== confirmation) { setError("Passwords do not match."); return; }
    setBusy(true);
    try {
      if (setupRequired) await api.setupAdmin(username, password);
      const session = await api.login(username, password);
      setToken(session.token);
      await api.overview();
      onAuthenticated();
    } catch (cause) {
      setToken("");
      const failure = cause as Error & { status?: number };
      if (failure.status === 409) { setSetupRequired(false); setError("Administrator already exists. Sign in instead."); }
      else setError(setupRequired ? failure.message : "Incorrect username or password.");
    } finally { setBusy(false); }
  }

  return <main className="auth-screen"><section className="auth-card" aria-labelledby="auth-title"><div className="auth-brand"><Mark /><strong>Dispatch</strong></div>{setupRequired === null ? error ? <div className="auth-connection-error"><h1 id="auth-title">Controller unavailable</h1><p>{error}</p><button className="quiet-button" onClick={() => void loadStatus()}>Retry connection</button></div> : <div className="auth-loading" aria-label="Connecting"><span /><span /><span /></div> : <><header><h1 id="auth-title">{setupRequired ? "Create administrator" : "Sign in"}</h1></header><form onSubmit={submit} aria-busy={busy}><label><span>Username</span><input value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" autoFocus required minLength={3} maxLength={64} disabled={busy} /></label><label><span>Password</span><input type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete={setupRequired ? "new-password" : "current-password"} required minLength={setupRequired ? 12 : undefined} disabled={busy} />{setupRequired && <small>Use at least 12 characters.</small>}</label>{setupRequired && <label><span>Confirm password</span><input type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="new-password" required minLength={12} disabled={busy} /></label>}{error && <p className="auth-error" role="alert">{error}</p>}<button className="primary-button" disabled={busy}>{busy ? "Please wait..." : setupRequired ? "Create account" : "Sign in"}</button></form></>}</section></main>;
}

function PageLoading() { return <div className="page-layout"><div className="page-loading"><span /><span /><span /></div></div>; }
function Mark() { return <svg viewBox="0 0 36 36" aria-hidden="true"><path d="M5 8.5 18 2l13 6.5v18L18 34 5 26.5Z" fill="none" stroke="currentColor" strokeWidth="2"/><path d="m5 8.5 13 7 13-7M18 15.5V34" fill="none" stroke="currentColor" strokeWidth="2"/><path d="m10 11 8-4 8 4-8 4Z" fill="currentColor"/></svg>; }
