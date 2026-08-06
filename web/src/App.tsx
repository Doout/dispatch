import { FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AppWindow,
  ArrowClockwise,
  ArrowRight,
  FolderSimple,
  HardDrives,
  Lightning,
  List,
  LockSimple,
  PencilSimple,
  Plus,
  RocketLaunch,
  Trash,
  X,
} from "@phosphor-icons/react";
import { api, App as AppModel, Deployment, DeploymentLog, EventTrigger, Overview, PreviewGroup, PreviewGroupComponent, Project, Server, setToken } from "./api";
import { AppForm, ProjectForm, ServerForm } from "./Onboarding";
import { PreviewGroupsArea } from "./PreviewGroups";
import { groupDeployments, relative, short, stages, stageIndex, stateStage, statusTone } from "./presentation";

type View = "deployments" | "applications" | "events" | "projects" | "servers";
type Dialog = "deploy" | "project" | "server" | "repair" | null;
type DeleteTarget = { kind: "project"; item: Project } | { kind: "server"; item: Server } | { kind: "application"; item: AppModel };

const viewCopy: Record<View, { title: string; description: string }> = {
  deployments: { title: "Deployments", description: "Active revisions and deployment history." },
  applications: { title: "Applications", description: "Deployment definitions and their targets." },
  events: { title: "Events", description: "Pull request commands and the preview environments they start." },
  projects: { title: "Projects", description: "Independent groups for related applications." },
  servers: { title: "Servers", description: "Docker hosts, Kubernetes clusters, and OpenShift clusters available to this controller." },
};

export default function DispatchApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [view, setView] = useState<View>("deployments");
  const [dialog, setDialog] = useState<Dialog>(null);
  const [creatingApplication, setCreatingApplication] = useState(false);
  const [editingProject, setEditingProject] = useState<Project | null>(null);
  const [editingServer, setEditingServer] = useState<Server | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null);
  const [deployAppID, setDeployAppID] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [logs, setLogs] = useState<DeploymentLog[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [needsAuth, setNeedsAuth] = useState(false);
  const [mobileNav, setMobileNav] = useState(false);

  const load = useCallback(async (quiet = false) => {
    try {
      const next = await api.overview();
      setOverview(next);
      setNeedsAuth(false);
      setError("");
      if (!selectedID && next.deployments[0]) setSelectedID(next.deployments[0].id);
      if (!quiet && next.servers.length === 0) setView("servers");
    } catch (cause) {
      const failure = cause as Error & { status?: number };
      if (failure.status === 401) setNeedsAuth(true); else setError(failure.message);
    } finally {
      if (!quiet) setLoading(false);
    }
  }, [selectedID]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(true), 1800);
    return () => window.clearInterval(timer);
  }, [load]);

  const selected = overview?.deployments.find((item) => item.id === selectedID) ?? overview?.deployments[0];
  useEffect(() => {
    if (!selected?.id) {
      setLogs([]);
      return;
    }
    let active = true;
    const refresh = async () => {
      try {
        const next = await api.logs(selected.id);
        if (active) setLogs(next);
      } catch {
        // The inventory error state owns recovery messaging.
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 1200);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [selected?.id]);

  const navigate = (next: View) => {
    setView(next);
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
          {!loading && overview && view === "deployments" && <DeploymentsPage overview={overview} selected={selected} logs={logs} onSelect={setSelectedID} onOpen={(next) => { setDeployAppID(""); setDialog(next); }} onCreateApplication={() => { setCreatingApplication(true); setView("applications"); }} onCancel={async () => { if (!selected) return; try { await api.cancel(selected.id); await load(); } catch (cause) { setError((cause as Error).message); } }} />}
          {!loading && overview && view === "applications" && <ApplicationsPage overview={overview} creating={creatingApplication} onToggleCreate={() => setCreatingApplication((value) => !value)} onChanged={async () => { await load(); setCreatingApplication(false); }} onComposeDeployed={async (id) => { setSelectedID(id); setCreatingApplication(false); setView("deployments"); await load(); }} onDeploy={(appID) => { setDeployAppID(appID); setDialog("deploy"); }} onDelete={(application) => setDeleteTarget({ kind: "application", item: application })} onNavigate={navigate} />}
          {!loading && overview && view === "events" && <EventsPage overview={overview} onConfigure={() => navigate("applications")} onChanged={async () => { await load(); }} />}
          {!loading && overview && view === "projects" && <ProjectsPage overview={overview} onAdd={() => { setEditingProject(null); setDialog("project"); }} onEdit={(project) => { setEditingProject(project); setDialog("project"); }} onDelete={(project) => setDeleteTarget({ kind: "project", item: project })} />}
          {!loading && overview && view === "servers" && <ServersPage overview={overview} onAdd={() => { setEditingServer(null); setDialog("server"); }} onEdit={(server) => { setEditingServer(server); setDialog("server"); }} onRepair={(server) => { setEditingServer(server); setDialog("repair"); }} onDelete={(server) => setDeleteTarget({ kind: "server", item: server })} />}
        </div>
      </main>

      {dialog && overview && <ResourceDialog kind={dialog} overview={overview} project={editingProject ?? undefined} server={editingServer ?? undefined} deployAppID={deployAppID} onClose={() => setDialog(null)} onChanged={async () => { await load(); setDialog(null); }} onDeployed={async (id) => { setSelectedID(id); setView("deployments"); setDialog(null); await load(); }} />}
      {deleteTarget && overview && <DeleteDialog target={deleteTarget} overview={overview} onClose={() => setDeleteTarget(null)} onDeleted={async () => { await load(); setDeleteTarget(null); }} />}
    </div>
  );
}

function Nav({ open, view, overview, onClose, onNavigate }: { open: boolean; view: View; overview: Overview | null; onClose: () => void; onNavigate: (view: View) => void }) {
  const entries: Array<{ id: View; label: string; icon: ReactNode; count: number }> = [
    { id: "deployments", label: "Deployments", icon: <RocketLaunch size={19} />, count: overview?.deployments.length ?? 0 },
    { id: "applications", label: "Applications", icon: <AppWindow size={19} />, count: overview?.apps.length ?? 0 },
    { id: "events", label: "Events", icon: <Lightning size={19} />, count: (overview?.eventTriggers.length ?? 0) + (overview?.previewGroups.length ?? 0) },
    { id: "projects", label: "Projects", icon: <FolderSimple size={19} />, count: overview?.projects.length ?? 0 },
    { id: "servers", label: "Servers", icon: <HardDrives size={19} />, count: overview?.servers.length ?? 0 },
  ];

  return <>
    <div className={`nav-scrim ${open ? "visible" : ""}`} onClick={onClose} />
    <aside id="primary-navigation" className={`rail ${open ? "open" : ""}`} aria-label="Primary navigation">
      <div className="wordmark"><Mark /><span>Dispatch</span><button aria-label="Close navigation" onClick={onClose}><X size={20} weight="bold" /></button></div>
      <nav className="nav-list">
        {entries.map((entry) => <button key={entry.id} className={view === entry.id ? "active" : ""} aria-current={view === entry.id ? "page" : undefined} onClick={() => onNavigate(entry.id)}>{entry.icon}<span>{entry.label}</span><small aria-hidden="true">{entry.count}</small></button>)}
        {view === "servers" && overview && overview.servers.length > 0 && <div className="nav-server-list" aria-label="Registered servers">{overview.servers.slice(0, 5).map((server) => <div key={server.id}><i className={server.state} /><span><strong>{server.name}</strong><small>{server.address === "local" ? "Local Docker" : server.runtime === "openshift" ? `OpenShift · ${server.kubernetes?.namespace || "default"}` : server.runtime === "kubernetes" ? `Kubernetes · ${server.kubernetes?.context || server.kubernetes?.namespace || "direct"}` : server.address}</small></span></div>)}</div>}
      </nav>
      <footer className="rail-foot"><div className="control-mark"><span className={overview?.demo ? "demo-dot" : "live-dot"} /><div><strong>Control plane</strong><span>{overview?.demo ? "Demonstration mode" : "Connected"}</span></div></div></footer>
    </aside>
  </>;
}

function PageHeader({ view, action }: { view: View; action?: { label: string; onClick: () => void; disabled?: boolean; icon?: ReactNode; tone?: "primary" | "quiet" } }) {
  return <header className="page-header"><div><h1>{viewCopy[view].title}</h1><p>{viewCopy[view].description}</p></div>{action && <button className={action.tone === "quiet" ? "quiet-button" : "primary-button"} disabled={action.disabled} onClick={action.onClick}>{action.icon ?? <Plus size={16} weight="bold" />}{action.label}</button>}</header>;
}

function DeploymentsPage({ overview, selected, logs, onSelect, onOpen, onCreateApplication, onCancel }: { overview: Overview; selected?: Deployment; logs: DeploymentLog[]; onSelect: (id: string) => void; onOpen: (dialog: Dialog) => void; onCreateApplication: () => void; onCancel: () => void }) {
  const groups = useMemo(() => groupDeployments(overview.deployments), [overview.deployments]);
  const runnableApps = overview.apps.filter((app) => !app.template);
  const ready = overview.servers.some((server) => server.state === "ready");
  const canAddApp = ready && overview.projects.length > 0;
  const action = runnableApps.length > 0
    ? { label: "Deploy revision", onClick: () => onOpen("deploy"), icon: <RocketLaunch size={16} /> }
    : canAddApp ? { label: "Add application", onClick: onCreateApplication } : undefined;
  return <div className="page-layout">
    <PageHeader view="deployments" action={action} />
    <div className={`deployment-workspace ${selected ? "with-evidence" : ""}`}>
      <section className="deployment-board" aria-label="Deployment activity">
        {overview.deployments.length === 0 && <EmptyState title="No deployment activity" body={runnableApps.length ? "Deploy a revision when you are ready." : "Applications will appear here after their first deployment."} />}
        {groups.attention.length > 0 && <DeploymentGroup title="Needs attention" count={groups.attention.length} deployments={groups.attention} selectedID={selected?.id} onSelect={onSelect} />}
        {groups.history.length > 0 && <DeploymentGroup title="History" count={groups.history.length} deployments={groups.history} selectedID={selected?.id} onSelect={onSelect} />}
      </section>
      {selected && <aside className="evidence-panel" aria-label="Deployment evidence"><Evidence deployment={selected} logs={logs} onCancel={onCancel} /></aside>}
    </div>
  </div>;
}

type EventHookTarget = { type: "trigger"; trigger: EventTrigger } | { type: "group"; group: PreviewGroup };

function EventsPage({ overview, onConfigure, onChanged }: { overview: Overview; onConfigure: () => void; onChanged: () => Promise<void> }) {
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

  return <div className="page-layout events-page">
    <PageHeader view="events" action={{ label: "Configure event", onClick: onConfigure }} />
    <section className="event-section" aria-labelledby="event-rules-title">
      <div className="section-toolbar"><div><h2 id="event-rules-title">Event rules</h2><p>Trusted pull request comments matched by repository and command.</p></div><span className="section-count">{rules.length}</span></div>
      {rules.length ? <div className="resource-table-wrap"><table className="resource-table event-table"><thead><tr><th>Rule</th><th>Repository</th><th>Command</th><th>Target</th><th>Hooks</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{rules.map((rule) => <tr key={rule.id}><td data-label="Rule"><strong>{rule.name}</strong></td><td data-label="Repository"><div className="event-sources">{rule.repositories.map((repository) => <code key={repository}>{repository}</code>)}</div></td><td data-label="Command"><code>{rule.command}</code></td><td data-label="Target">{rule.targetLabel}</td><td data-label="Hooks">{rule.hooks}</td><td data-label="Status"><StatusLabel state={rule.enabled ? "enabled" : "disabled"} /></td><td className="row-actions"><button aria-label={`Edit deployment hooks for ${rule.name}`} onClick={() => setHookTarget(rule.hookTarget)}><PencilSimple size={15} />Hooks</button></td></tr>)}</tbody></table></div> : <EmptyState title="No event rules" body="Configure a pull request command on an application or preview group." />}
    </section>
    {hookTarget && <EventHookEditor key={hookTarget.type === "trigger" ? hookTarget.trigger.id : hookTarget.group.id} target={hookTarget} onClose={() => setHookTarget(null)} onSaved={async () => { setHookTarget(null); await onChanged(); }} />}
    <section className="event-section" aria-labelledby="event-activity-title">
      <div className="section-toolbar"><div><h2 id="event-activity-title">Preview activity</h2><p>Environments created from pull request commands.</p></div><span className="section-count">{activity.length}</span></div>
      {activity.length ? <div className="resource-table-wrap"><table className="resource-table event-table"><thead><tr><th>Environment</th><th>Source</th><th>Status</th><th>Updated</th><th className="actions-head"><span className="sr-only">Preview URL</span></th></tr></thead><tbody>{activity.slice(0, 30).map((item) => <tr key={item.id}><td data-label="Environment"><strong>{item.name}</strong></td><td data-label="Source"><div className="event-sources">{item.sources.map((source) => <code key={source}>{source}</code>)}</div></td><td data-label="Status"><StatusLabel state={item.state} /></td><td data-label="Updated">{relative(item.updatedAt)}</td><td className="row-actions">{item.url && <a className="table-action" href={item.url} target="_blank" rel="noreferrer">Open<ArrowRight size={14} /></a>}</td></tr>)}</tbody></table></div> : <EmptyState title="No preview activity" body="Preview environments will appear here after an event rule is triggered." />}
    </section>
  </div>;
}

function EventHookEditor({ target, onClose, onSaved }: { target: EventHookTarget; onClose: () => void; onSaved: () => Promise<void> }) {
  const trigger = target.type === "trigger" ? target.trigger : undefined;
  const group = target.type === "group" ? target.group : undefined;
  const [preDeployHook, setPreDeployHook] = useState(trigger?.preDeployHook ?? "");
  const [postDeployHook, setPostDeployHook] = useState(trigger?.postDeployHook ?? "");
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
        await api.updateEventTrigger(trigger.id, { command: trigger.command, enabled: trigger.enabled, preDeployHook, postDeployHook });
      } else if (group) {
        await api.updatePreviewGroup(group.id, { name: group.name, command: group.command, enabled: group.enabled, components });
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
    <header><div><h2 id="event-hook-editor-title">Deployment hooks</h2><p>{title} · hooks run only for deployments started by this event rule.</p></div><button type="button" aria-label="Close deployment hooks" onClick={onClose}><X size={18} weight="bold" /></button></header>
    <form onSubmit={save}>
      {trigger ? <HookFields preDeployHook={preDeployHook} postDeployHook={postDeployHook} onPreDeployHook={setPreDeployHook} onPostDeployHook={setPostDeployHook} /> : components.map((component, index) => <fieldset className="event-component-hooks" key={component.id ?? component.alias}><legend>{component.alias || `Component ${index + 1}`} <span>{component.repository}</span></legend><HookFields preDeployHook={component.preDeployHook ?? ""} postDeployHook={component.postDeployHook ?? ""} onPreDeployHook={(value) => changeComponent(index, { preDeployHook: value })} onPostDeployHook={(value) => changeComponent(index, { postDeployHook: value })} /></fieldset>)}
      <div className="event-hook-context"><strong>Event context</strong><p>Available as environment variables in both hooks.</p><div>{["DISPATCH_EVENT_REPOSITORY", "DISPATCH_EVENT_PULL_REQUEST_NUMBER", "DISPATCH_EVENT_HEAD_REF", "DISPATCH_EVENT_HEAD_SHA", "DISPATCH_EVENT_ACTOR", "DISPATCH_EVENT_COMMAND", "DISPATCH_EVENT_ARGUMENTS"].map((name) => <code key={name}>{name}</code>)}</div></div>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="builder-actions"><button type="button" className="quiet-button" onClick={onClose}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? "Saving..." : "Save hooks"}</button></div>
    </form>
  </section>;
}

function HookFields({ preDeployHook, postDeployHook, onPreDeployHook, onPostDeployHook }: { preDeployHook: string; postDeployHook: string; onPreDeployHook: (value: string) => void; onPostDeployHook: (value: string) => void }) {
  return <div className="event-hook-fields"><label><span>Pre-deploy hook</span><textarea value={preDeployHook} onChange={(event) => onPreDeployHook(event.target.value)} placeholder={'docker build -t registry.example.com/team/app:$DISPATCH_REVISION .\ndocker push registry.example.com/team/app:$DISPATCH_REVISION'} spellCheck={false} /><small>Runs after source checkout and before deployment.</small></label><label><span>Post-deploy hook</span><textarea value={postDeployHook} onChange={(event) => onPostDeployHook(event.target.value)} placeholder={'echo "Ready at $DISPATCH_DEPLOYMENT_URL"'} spellCheck={false} /><small>Runs after the deployment becomes ready.</small></label></div>;
}

function overviewRuleName(trigger: EventTrigger) {
  return `${trigger.repository} ${trigger.command}`;
}

function ServersPage({ overview, onAdd, onEdit, onRepair, onDelete }: { overview: Overview; onAdd: () => void; onEdit: (server: Server) => void; onRepair: (server: Server) => void; onDelete: (server: Server) => void }) {
  const ready = overview.servers.filter((server) => server.state === "ready").length;
  const kubernetes = overview.servers.filter((server) => server.runtime === "kubernetes").length;
  const openShift = overview.servers.filter((server) => server.runtime === "openshift").length;
  return <div className="page-layout">
    <PageHeader view="servers" action={{ label: "Add server", onClick: onAdd }} />
    <ResourceSummary items={[{ label: "Registered", value: overview.servers.length }, { label: "Ready", value: ready }, { label: "Kubernetes", value: kubernetes }, { label: "OpenShift", value: openShift }]} />
    {overview.servers.length ? <div className="resource-table-wrap"><table className="resource-table"><thead><tr><th>Server</th><th>Connection</th><th>Runtime</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{overview.servers.map((server) => <tr key={server.id}><td data-label="Server"><strong>{server.name}</strong></td><td data-label="Connection"><span className="connection"><i className={server.state} />{server.address === "local" ? "This controller" : server.runtime === "openshift" ? server.address : server.runtime === "kubernetes" ? server.kubernetes?.context || server.kubernetes?.kubeconfigPath || server.address : server.address}</span></td><td data-label="Runtime">{server.runtime === "docker" ? "Docker" : server.runtime === "openshift" ? "OpenShift" : "Kubernetes"}</td><td data-label="Status"><StatusLabel state={server.state} /></td><td className="row-actions">{server.address === "local" ? <span className="managed-label"><LockSimple size={14} />Managed</span> : <>{server.runtime === "openshift" && <button aria-label={`Repair ${server.name}`} onClick={() => onRepair(server)}><ArrowClockwise size={15} />Repair</button>}<button aria-label={`Edit ${server.name}`} onClick={() => onEdit(server)}><PencilSimple size={15} />Edit</button><button className="delete-action" aria-label={`Delete ${server.name}`} onClick={() => onDelete(server)}><Trash size={15} />Delete</button></>}</td></tr>)}</tbody></table></div> : <EmptyState title="No servers" body="Mount this controller's Docker socket or add a remote Docker host, Kubernetes cluster, or OpenShift cluster." />}
  </div>;
}

function ProjectsPage({ overview, onAdd, onEdit, onDelete }: { overview: Overview; onAdd: () => void; onEdit: (project: Project) => void; onDelete: (project: Project) => void }) {
  return <div className="page-layout">
    <PageHeader view="projects" action={{ label: "Add project", onClick: onAdd }} />
    <ResourceSummary items={[{ label: "Projects", value: overview.projects.length }, { label: "Applications", value: overview.apps.length }]} />
    {overview.projects.length ? <div className="resource-table-wrap"><table className="resource-table"><thead><tr><th>Project</th><th>Description</th><th>Applications</th><th>Created</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{overview.projects.map((project) => <tr key={project.id}><td data-label="Project"><strong>{project.name}</strong></td><td data-label="Description">{project.description || "No description"}</td><td data-label="Applications">{overview.apps.filter((app) => app.projectId === project.id).length}</td><td data-label="Created">{new Date(project.createdAt).toLocaleDateString()}</td><td className="row-actions"><button aria-label={`Edit ${project.name}`} onClick={() => onEdit(project)}><PencilSimple size={15} />Edit</button><button className="delete-action" aria-label={`Delete ${project.name}`} onClick={() => onDelete(project)}><Trash size={15} />Delete</button></td></tr>)}</tbody></table></div> : <EmptyState title="No projects" body="Create a project when you want to group applications." />}
  </div>;
}

function ApplicationsPage({ overview, creating, onToggleCreate, onChanged, onComposeDeployed, onDeploy, onDelete, onNavigate }: { overview: Overview; creating: boolean; onToggleCreate: () => void; onChanged: () => Promise<void>; onComposeDeployed: (id: string) => Promise<void>; onDeploy: (appID: string) => void; onDelete: (application: AppModel) => void; onNavigate: (view: View) => void }) {
  const [section, setSection] = useState<"applications" | "templates" | "helm" | "groups">("applications");
  const [creatingHelm, setCreatingHelm] = useState(false);
  const [creatingTemplate, setCreatingTemplate] = useState(false);
  const applications = overview.apps.filter((application) => !application.template && application.buildType !== "helm");
  const templates = overview.apps.filter((application) => application.template);
  const helmSources = overview.apps.filter((application) => !application.template && application.buildType === "helm");
  const dockerReady = overview.servers.some((server) => server.state === "ready" && server.runtime === "docker");
  const kubernetesReady = overview.servers.some((server) => server.state === "ready" && (server.runtime === "kubernetes" || server.runtime === "openshift"));
  const hasProject = overview.projects.length > 0;
  const canAdd = dockerReady && hasProject;
  const canAddTemplate = (dockerReady || kubernetesReady) && hasProject;
  const canAddHelm = kubernetesReady && hasProject;

  function selectSection(next: "applications" | "templates" | "helm" | "groups") {
    if (next !== "applications" && creating) onToggleCreate();
    if (next !== "templates") setCreatingTemplate(false);
    if (next !== "helm") setCreatingHelm(false);
    setSection(next);
  }

  const action = section === "applications" && canAdd
    ? creating ? { label: "Cancel", onClick: onToggleCreate, icon: <X size={16} weight="bold" />, tone: "quiet" as const } : { label: "Add application", onClick: onToggleCreate }
    : section === "templates" && canAddTemplate
      ? creatingTemplate ? { label: "Cancel", onClick: () => setCreatingTemplate(false), icon: <X size={16} weight="bold" />, tone: "quiet" as const } : { label: "Add template", onClick: () => setCreatingTemplate(true) }
      : section === "helm" && canAddHelm
        ? creatingHelm ? { label: "Cancel", onClick: () => setCreatingHelm(false), icon: <X size={16} weight="bold" />, tone: "quiet" as const } : { label: "Add Helm source", onClick: () => setCreatingHelm(true) }
        : undefined;

  return <div className="page-layout">
    <PageHeader view="applications" action={action} />
    <div className="application-sections" role="tablist" aria-label="Application resources">
      <button id="applications-tab" role="tab" aria-controls="applications-panel" aria-selected={section === "applications"} className={section === "applications" ? "active" : ""} onClick={() => selectSection("applications")}>Applications <span>{applications.length}</span></button>
      <button id="templates-tab" role="tab" aria-controls="templates-panel" aria-selected={section === "templates"} className={section === "templates" ? "active" : ""} onClick={() => selectSection("templates")}>Templates <span>{templates.length}</span></button>
      <button id="helm-sources-tab" role="tab" aria-controls="helm-sources-panel" aria-selected={section === "helm"} className={section === "helm" ? "active" : ""} onClick={() => selectSection("helm")}>Helm sources <span>{helmSources.length}</span></button>
      <button id="preview-groups-tab" role="tab" aria-controls="preview-groups-panel" aria-selected={section === "groups"} className={section === "groups" ? "active" : ""} onClick={() => selectSection("groups")}>Preview groups <span>{overview.previewGroups.length}</span></button>
    </div>
    {section === "applications" && <div id="applications-panel" role="tabpanel" aria-labelledby="applications-tab">
      {creating && <section className="inline-create" aria-labelledby="new-application-title"><header><h2 id="new-application-title">New application</h2><p>Choose a project, target, and deployment source.</p></header><div className="inline-create-body"><AppForm data={overview} onChanged={onChanged} onDeployed={onComposeDeployed} focusName /></div></section>}
      {applications.length ? <div className="resource-table-wrap"><table className="resource-table"><thead><tr><th>Application</th><th>Project</th><th>Server</th><th>Source</th><th /></tr></thead><tbody>{applications.map((application) => { const source = application.sourceRepo || "Pasted Compose"; return <tr key={application.id}><td data-label="Application"><strong>{application.name}</strong><small>{application.sourceRepo ? application.branch : "Compose"}</small></td><td data-label="Project">{overview.projects.find((project) => project.id === application.projectId)?.name ?? "Unknown"}</td><td data-label="Server">{overview.servers.find((server) => server.id === application.serverId)?.name ?? "Unknown"}</td><td data-label="Source"><span className="truncate-cell" title={source}>{source}</span></td><td className="row-actions"><button className="table-action" onClick={() => onDeploy(application.id)}>Deploy<ArrowRight size={14} /></button><button className="delete-action" aria-label={`Delete ${application.name}`} onClick={() => onDelete(application)}><Trash size={15} />Delete</button></td></tr>; })}</tbody></table></div> : creating ? null : canAdd ? <EmptyState title="No applications" body="Paste Compose or connect a repository." /> : <PrerequisiteState hasReadyServer={dockerReady} hasProject={hasProject} onNavigate={onNavigate} />}
    </div>}
    {section === "templates" && <div id="templates-panel" role="tabpanel" aria-labelledby="templates-tab">
      {creatingTemplate && <section className="inline-create" aria-labelledby="new-template-title"><header><h2 id="new-template-title">New application template</h2><p>Save a reusable definition for events and preview groups.</p></header><div className="inline-create-body"><AppForm data={overview} onChanged={async () => { await onChanged(); setCreatingTemplate(false); }} onDeployed={onComposeDeployed} focusName initialSourceType="repository" template /></div></section>}
      {templates.length ? <div className="resource-table-wrap"><table className="resource-table"><thead><tr><th>Template</th><th>Type</th><th>Project</th><th>Target</th><th>Source</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{templates.map((template) => { const source = template.buildType === "helm" ? template.helmChart || "Helm chart" : template.sourceRepo || "Pasted Compose"; return <tr key={template.id}><td data-label="Template"><strong>{template.name}</strong><small>Reusable definition</small></td><td data-label="Type">{template.buildType === "helm" ? "Helm" : template.buildType === "compose" ? "Compose" : "Dockerfile"}</td><td data-label="Project">{overview.projects.find((project) => project.id === template.projectId)?.name ?? "Unknown"}</td><td data-label="Target">{overview.servers.find((server) => server.id === template.serverId)?.name ?? "Unknown"}</td><td data-label="Source"><span className="truncate-cell" title={source}>{source}</span></td><td className="row-actions"><button className="delete-action" aria-label={`Delete ${template.name}`} onClick={() => onDelete(template)}><Trash size={15} />Delete</button></td></tr>; })}</tbody></table></div> : creatingTemplate ? null : canAddTemplate ? <EmptyState title="No application templates" body="Create a reusable definition for event-driven previews and preview groups." /> : <TemplatePrerequisiteState hasReadyServer={dockerReady || kubernetesReady} hasProject={hasProject} onNavigate={onNavigate} />}
    </div>}
    {section === "helm" && <div id="helm-sources-panel" role="tabpanel" aria-labelledby="helm-sources-tab" className="helm-sources-panel">
      {creatingHelm && <section className="inline-create" aria-labelledby="new-helm-source-title"><header><h2 id="new-helm-source-title">New Helm source</h2><p>Configure the chart, repository, values, hooks, and Kubernetes target.</p></header><div className="inline-create-body"><AppForm data={overview} onChanged={async () => { await onChanged(); setCreatingHelm(false); }} onDeployed={onComposeDeployed} focusName initialSourceType="helm" sourceTypeLocked /></div></section>}
      {helmSources.length ? <div className="resource-table-wrap"><table className="resource-table helm-source-table"><thead><tr><th>Source</th><th>Chart</th><th>Repository</th><th>Version</th><th>Target</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{helmSources.map((source) => <tr key={source.id}><td data-label="Source"><strong>{source.name}</strong><small>{overview.projects.find((project) => project.id === source.projectId)?.name ?? "Unknown project"}</small></td><td data-label="Chart"><span className="truncate-cell" title={source.helmChart}>{source.helmChart}</span></td><td data-label="Repository"><span className="truncate-cell" title={source.helmRepository || "Direct chart reference"}>{source.helmRepository || "Direct chart reference"}</span></td><td data-label="Version"><code>{source.helmVersion || "Latest"}</code></td><td data-label="Target">{overview.servers.find((server) => server.id === source.serverId)?.name ?? "Unknown"}</td><td className="row-actions"><button className="table-action" onClick={() => onDeploy(source.id)}>Deploy<ArrowRight size={14} /></button><button className="delete-action" aria-label={`Delete ${source.name}`} onClick={() => onDelete(source)}><Trash size={15} />Delete</button></td></tr>)}</tbody></table></div> : creatingHelm ? null : canAddHelm ? <EmptyState title="No Helm sources" body="Add a chart source to use for Kubernetes deployments and preview groups." /> : <HelmPrerequisiteState hasKubernetes={kubernetesReady} hasProject={hasProject} onNavigate={onNavigate} />}
    </div>}
    {section === "groups" && <div id="preview-groups-panel" role="tabpanel" aria-labelledby="preview-groups-tab"><PreviewGroupsArea overview={overview} onChanged={onChanged} /></div>}
  </div>;
}

function ResourceSummary({ items }: { items: Array<{ label: string; value: number }> }) {
  return <dl className="resource-summary" aria-label="Resource totals">{items.map((item) => <div key={item.label}><dd>{item.value}</dd><dt>{item.label}</dt></div>)}</dl>;
}

function StatusLabel({ state }: { state: string }) {
  return <span className={`status-label ${state}`}><i />{state}</span>;
}

function EmptyState({ title, body }: { title: string; body: string }) {
  return <section className="empty-state"><Mark /><div><h2>{title}</h2><p>{body}</p></div></section>;
}

function PrerequisiteState({ hasReadyServer, hasProject, onNavigate }: { hasReadyServer: boolean; hasProject: boolean; onNavigate: (view: View) => void }) {
  return <section className="prerequisite-state"><h2>Application requirements</h2><p>An application needs one ready server and one project.</p><div>{!hasReadyServer && <button className="quiet-button" onClick={() => onNavigate("servers")}>View servers</button>}{!hasProject && <button className="quiet-button" onClick={() => onNavigate("projects")}>View projects</button>}</div></section>;
}

function HelmPrerequisiteState({ hasKubernetes, hasProject, onNavigate }: { hasKubernetes: boolean; hasProject: boolean; onNavigate: (view: View) => void }) {
  return <section className="prerequisite-state"><h2>Helm source requirements</h2><p>A Helm source needs one ready Kubernetes server and one project.</p><div>{!hasKubernetes && <button className="quiet-button" onClick={() => onNavigate("servers")}>View servers</button>}{!hasProject && <button className="quiet-button" onClick={() => onNavigate("projects")}>View projects</button>}</div></section>;
}

function TemplatePrerequisiteState({ hasReadyServer, hasProject, onNavigate }: { hasReadyServer: boolean; hasProject: boolean; onNavigate: (view: View) => void }) {
  return <section className="prerequisite-state"><h2>Template requirements</h2><p>A template needs one ready server and one project.</p><div>{!hasReadyServer && <button className="quiet-button" onClick={() => onNavigate("servers")}>View servers</button>}{!hasProject && <button className="quiet-button" onClick={() => onNavigate("projects")}>View projects</button>}</div></section>;
}

function useDialogFocus(onClose: () => void) {
  const dialogRef = useRef<HTMLElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusFrame = window.requestAnimationFrame(() => {
      dialogRef.current?.querySelector<HTMLElement>('.dialog-body input:not([disabled]), .dialog-body select:not([disabled]), .dialog-body textarea:not([disabled]), .dialog-body button:not([disabled])')?.focus();
    });
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onCloseRef.current();
        return;
      }
      if (event.key !== "Tab" || !dialogRef.current) return;
      const focusable = [...dialogRef.current.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')];
      if (!focusable.length) return;
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", handleKey);
    return () => {
      window.cancelAnimationFrame(focusFrame);
      window.removeEventListener("keydown", handleKey);
      previousFocus?.focus();
    };
  }, []);
  return dialogRef;
}

function ResourceDialog({ kind, overview, project, server, deployAppID, onClose, onChanged, onDeployed }: { kind: Exclude<Dialog, null>; overview: Overview; project?: Project; server?: Server; deployAppID?: string; onClose: () => void; onChanged: () => Promise<void>; onDeployed: (id: string) => Promise<void> }) {
  const dialogRef = useDialogFocus(onClose);
  const copy = {
    server: server ? { title: "Edit server", description: "Update this server's connection settings." } : { title: "Add server", description: "Register a remote Docker host or Kubernetes cluster." },
    repair: { title: "Repair OpenShift connection", description: "Use a fresh temporary login to rebuild the managed connection." },
    project: project ? { title: "Edit project", description: "Update this project's name or description." } : { title: "Add project", description: "Create an independent group for related applications." },
    deploy: { title: "Deploy revision", description: "Choose an application and source revision." },
  }[kind];
  return <div className="dialog-layer" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialogRef} className="resource-dialog" role="dialog" aria-modal="true" aria-labelledby="dialog-title">
      <header><div><h2 id="dialog-title">{copy.title}</h2><p>{copy.description}</p></div><button aria-label="Close dialog" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        {kind === "server" && <ServerForm onChanged={onChanged} server={server} />}
        {kind === "repair" && <ServerForm onChanged={onChanged} server={server} repairing />}
        {kind === "project" && <ProjectForm onChanged={onChanged} project={project} />}
        {kind === "deploy" && <DeployForm apps={overview.apps.filter((app) => !app.template)} initialAppID={deployAppID} onComplete={onDeployed} />}
      </div>
    </section>
  </div>;
}

function DeleteDialog({ target, overview, onClose, onDeleted }: { target: DeleteTarget; overview: Overview; onClose: () => void; onDeleted: () => Promise<void> }) {
  const dialogRef = useDialogFocus(onClose);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const applications = target.kind === "application" ? 0 : overview.apps.filter((app) => target.kind === "server" ? app.serverId === target.item.id : app.projectId === target.item.id).length;
  const resource = target.kind === "server" ? "server" : target.kind === "project" ? "project" : target.item.template ? "template" : "application";

  async function remove() {
    setBusy(true);
    setError("");
    try {
      if (target.kind === "server") await api.deleteServer(target.item.id);
      else if (target.kind === "project") await api.deleteProject(target.item.id);
      else await api.deleteApp(target.item.id);
      await onDeleted();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <div className="dialog-layer" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialogRef} className="resource-dialog confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-dialog-title" aria-describedby="delete-dialog-description">
      <header><div><h2 id="delete-dialog-title">Delete {resource}</h2><p>{target.kind === "application" && !target.item.template ? "Deployed resources and history are removed first." : "This action cannot be undone."}</p></div><button aria-label="Close dialog" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        <p className="confirm-copy" id="delete-dialog-description">Delete <strong>{target.item.name}</strong> from Dispatch?</p>
        {applications > 0 && <div className="dependency-warning" role="status"><strong>Cannot delete this {resource}</strong><p>{applications} {applications === 1 ? "application uses" : "applications use"} it. Remove {applications === 1 ? "that application" : "those applications"} first.</p></div>}
        {error && <p className="form-error" role="alert">{error}</p>}
        <div className="dialog-actions confirm-actions"><button className="quiet-button" onClick={onClose}>Cancel</button><button className="danger-button" disabled={busy || applications > 0} onClick={() => void remove()}>{busy ? "Deleting..." : `Delete ${resource}`}</button></div>
      </div>
    </section>
  </div>;
}

function DeploymentGroup({ title, count, deployments, selectedID, onSelect }: { title: string; count: number; deployments: Deployment[]; selectedID?: string; onSelect: (id: string) => void }) {
  return <div className="deployment-group"><div className="group-heading"><h2>{title}</h2><span>{count}</span></div><div className="strips">{deployments.map((deployment) => <DeploymentStrip key={deployment.id} deployment={deployment} selected={selectedID === deployment.id} onSelect={() => onSelect(deployment.id)} />)}</div></div>;
}

function DeploymentStrip({ deployment, selected, onSelect }: { deployment: Deployment; selected: boolean; onSelect: () => void }) {
  const tone = statusTone(deployment.state), current = stageIndex(deployment.state);
  return <button className={`dispatch-strip ${tone} ${selected ? "selected" : ""}`} onClick={onSelect} aria-pressed={selected}>
    <span className="strip-status" aria-hidden="true">{tone === "success" ? "✓" : tone === "danger" ? "!" : "→"}</span>
    <span className="strip-identity"><strong>{deployment.app?.name ?? "Unknown app"}</strong><span><code>{short(deployment.commitSha)}</code><i />{deployment.server?.name ?? "No server"}</span></span>
    <span className="stage-track">{stages.map((stage, index) => <span className={`stage ${index < current || (index === current && deployment.state === "succeeded") ? "complete" : ""} ${index === current ? "current" : ""}`} key={stage}><small>{stage}</small><i>{index < current || (index === current && deployment.state === "succeeded") ? "✓" : index === current ? tone === "danger" ? "!" : "•" : ""}</i></span>)}</span>
    <span className="strip-result"><small>{stateStage[deployment.state]}</small><strong>{deployment.state}</strong><time dateTime={deployment.createdAt}>{relative(deployment.createdAt)}</time></span>
  </button>;
}

function Evidence({ deployment, logs, onCancel }: { deployment: Deployment; logs: DeploymentLog[]; onCancel: () => void }) {
  const active = !["succeeded", "failed", "cancelled"].includes(deployment.state);
  return <>
    <div className="evidence-head"><div><span className={`selection-dot ${statusTone(deployment.state)}`} /><h2>{deployment.app?.name}</h2></div><span className={`stamp ${statusTone(deployment.state)}`}>{deployment.state}</span></div>
    <dl className="evidence-grid">
      <div><dt>Commit</dt><dd><code>{deployment.commitSha}</code></dd></div><div><dt>Created</dt><dd>{relative(deployment.createdAt)}</dd></div>
      <div className="wide"><dt>Spec digest</dt><dd><code title={deployment.specDigest}>{short(deployment.specDigest, 28)}</code></dd></div>
      <div><dt>Build</dt><dd>{deployment.app?.buildType}</dd></div><div><dt>Branch</dt><dd>{deployment.app?.branch}</dd></div>
      <div><dt>Server</dt><dd>{deployment.server?.name}</dd></div><div><dt>Runtime</dt><dd>{deployment.server?.runtime}</dd></div>
    </dl>
    <div className="log-block"><div className="section-label"><h3>Log</h3><span className={active ? "active" : ""}>{active ? "Streaming" : "Complete"}</span></div><div className="terminal" role="log" aria-live="polite">{logs.length ? logs.map((entry) => <div key={entry.id} className={entry.level}><time>{new Date(entry.createdAt).toLocaleTimeString([], { hour12: false })}</time><span>{entry.message}</span></div>) : <p>No log entries.</p>}</div></div>
    {active && <div className="evidence-actions"><button className="danger-button" onClick={onCancel}>Cancel deployment</button></div>}
  </>;
}

function DeployForm({ apps, initialAppID, onComplete }: { apps: AppModel[]; initialAppID?: string; onComplete: (id: string) => Promise<void> }) {
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
  const sourceLabel = selectedApp?.buildType === "helm" ? "Saved Helm chart" : "Saved Compose file";
  const sourceHelp = selectedApp?.buildType === "helm" ? "Installs the configured chart and values." : "Applies the stored Compose definition.";
  return <form className="resource-form" onSubmit={submit}><label><span>Application</span><select value={appID} onChange={(event) => setAppID(event.target.value)}>{apps.map((app) => <option value={app.id} key={app.id}>{app.name}</option>)}</select><small>Definition to deploy.</small></label>{fixedSource ? <label><span>Source</span><input value={sourceLabel} disabled /><small>{sourceHelp}</small></label> : <label><span>Source revision</span><input value={commit} onChange={(event) => setCommit(event.target.value)} required spellCheck={false} /><small>Branch, tag, or commit.</small></label>}{error && <p className="form-error" role="alert">{error}</p>}<div className="dialog-actions"><button className="primary-button" disabled={busy || !appID}>{busy ? "Starting deployment..." : "Deploy"}</button></div></form>;
}

function AuthScreen({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [setupRequired, setSetupRequired] = useState<boolean | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => { void api.authStatus().then((status) => setSetupRequired(status.setupRequired)).catch(() => setError("Unable to reach the controller.")); }, []);

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

  return <main className="auth-screen"><section className="auth-card" aria-labelledby="auth-title"><div className="auth-brand"><Mark /><strong>Dispatch</strong></div>{setupRequired === null ? <div className="auth-loading">Connecting...</div> : <><header><h1 id="auth-title">{setupRequired ? "Create administrator" : "Sign in"}</h1>{setupRequired && <p>Create the account used to manage Dispatch.</p>}</header><form onSubmit={submit}><label><span>Username</span><input value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" autoFocus required minLength={3} maxLength={64} /></label><label><span>Password</span><input type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete={setupRequired ? "new-password" : "current-password"} required minLength={setupRequired ? 12 : undefined} /></label>{setupRequired && <label><span>Confirm password</span><input type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="new-password" required minLength={12} /></label>}{error && <p className="auth-error" role="alert">{error}</p>}<button className="primary-button" disabled={busy}>{busy ? "Please wait..." : setupRequired ? "Create account" : "Sign in"}</button></form></>}</section></main>;
}

function PageLoading() { return <div className="page-layout"><div className="page-loading"><span /><span /><span /></div></div>; }
function Mark() { return <svg viewBox="0 0 36 36" aria-hidden="true"><path d="M5 8.5 18 2l13 6.5v18L18 34 5 26.5Z" fill="none" stroke="currentColor" strokeWidth="2"/><path d="m5 8.5 13 7 13-7M18 15.5V34" fill="none" stroke="currentColor" strokeWidth="2"/><path d="m10 11 8-4 8 4-8 4Z" fill="currentColor"/></svg>; }
