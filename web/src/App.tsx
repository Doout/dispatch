import { ChangeEvent, FormEvent, ReactNode, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import {
  AppWindow,
  ArrowClockwise,
  ArrowLeft,
  ArrowRight,
  ArrowSquareOut,
  Check,
  CheckCircle,
  CircleNotch,
  Copy,
  FolderSimple,
  GithubLogo,
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
import { api, App as AppModel, Deployment, DeploymentLog, EventTrigger, GitHubAppConnection, GitHubAppInstallation, GitHubRepository, Overview, PreviewGroup, PreviewGroupComponent, Project, RelayWebhook, Secret, SecretType, Server, setToken } from "./api";
import { AppForm, ProjectForm, ServerForm } from "./Onboarding";
import { PreviewGroupsArea } from "./PreviewGroups";
import { relative, short, stages, stageIndex, stateStage, statusTone } from "./presentation";
import { readHookScriptFile, readSecretTextFile } from "./fileUploads";

type View = "deployments" | "applications" | "events" | "projects" | "servers" | "secrets" | "connections";
type Dialog = "deploy" | "project" | "server" | "repair" | null;
type DeleteTarget = { kind: "project"; item: Project } | { kind: "server"; item: Server } | { kind: "application"; item: AppModel } | { kind: "secret"; item: Secret };

const viewCopy: Record<View, { title: string; description: string }> = {
  deployments: { title: "Deployments", description: "Active revisions and deployment history." },
  applications: { title: "Applications", description: "Deployment definitions and their targets." },
  events: { title: "Events", description: "Pull request commands and the preview environments they start." },
  projects: { title: "Projects", description: "Independent groups for related applications." },
  servers: { title: "Servers", description: "Deployment targets and event relay nodes connected to this controller." },
  secrets: { title: "Secrets", description: "Encrypted credentials shared by application sources and deployment hooks." },
  connections: { title: "Connections", description: "GitHub Apps used for repository access and pull request events." },
};

export default function DispatchApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [view, setView] = useState<View>(() => {
    const requested = new URLSearchParams(window.location.search).get("view") as View | null;
    return requested && requested in viewCopy ? requested : "deployments";
  });
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
  const [connectionNotice, setConnectionNotice] = useState("");
  const connectionCallbackHandled = useRef(false);

  const load = useCallback(async (quiet = false) => {
    try {
      const next = await api.overview();
      setOverview(next);
      setNeedsAuth(false);
      setError("");
      if (!quiet && next.servers.length === 0) setView((current) => current === "deployments" ? "servers" : current);
    } catch (cause) {
      const failure = cause as Error & { status?: number };
      if (failure.status === 401) setNeedsAuth(true); else setError(failure.message);
    } finally {
      if (!quiet) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(true), 1800);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    if (connectionCallbackHandled.current) return;
    connectionCallbackHandled.current = true;
    const params = new URLSearchParams(window.location.search);
    const setupID = params.get("githubAppSetup");
    const installationID = Number(params.get("installation_id") || "0");
    const createdID = params.get("githubAppCreated");
    const callbackStatus = params.get("githubAppStatus");
    if (setupID && installationID > 0) {
      setView("connections");
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
      setView("connections");
      setConnectionNotice("GitHub App created. Install it on an account to finish the connection.");
    } else if (callbackStatus === "error") {
      setView("connections");
      setConnectionNotice(params.get("detail") || "GitHub App setup failed.");
    }
    if (setupID || createdID || callbackStatus) window.history.replaceState({}, "", "/?view=connections");
  }, [load]);

  const selected = overview?.deployments.find((item) => item.id === selectedID);
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
          {!loading && overview && view === "deployments" && <DeploymentsPage overview={overview} selected={selected} logs={logs} onSelect={setSelectedID} onCloseDetails={() => setSelectedID("")} onOpen={(next) => { setDeployAppID(""); setDialog(next); }} onCreateApplication={() => { setCreatingApplication(true); setView("applications"); }} onCancel={async () => { if (!selected) return; try { await api.cancel(selected.id); await load(); } catch (cause) { setError((cause as Error).message); } }} />}
          {!loading && overview && view === "applications" && <ApplicationsPage overview={overview} creating={creatingApplication} onToggleCreate={() => setCreatingApplication((value) => !value)} onChanged={async () => { await load(); setCreatingApplication(false); }} onDeploy={(appID) => { setDeployAppID(appID); setDialog("deploy"); }} onDelete={(application) => setDeleteTarget({ kind: "application", item: application })} onNavigate={navigate} />}
          {!loading && overview && view === "events" && <EventsPage overview={overview} onConfigure={() => navigate("applications")} onChanged={async () => { await load(); }} />}
          {!loading && overview && view === "projects" && <ProjectsPage overview={overview} onAdd={() => { setEditingProject(null); setDialog("project"); }} onEdit={(project) => { setEditingProject(project); setDialog("project"); }} onDelete={(project) => setDeleteTarget({ kind: "project", item: project })} />}
          {!loading && overview && view === "servers" && <ServersPage overview={overview} onChanged={async () => { await load(); }} onAdd={() => { setEditingServer(null); setDialog("server"); }} onEdit={(server) => { setEditingServer(server); setDialog("server"); }} onRepair={(server) => { setEditingServer(server); setDialog("repair"); }} onDelete={(server) => setDeleteTarget({ kind: "server", item: server })} />}
          {!loading && overview && view === "secrets" && <SecretsPage overview={overview} onChanged={async () => { await load(); }} onDelete={(secret) => setDeleteTarget({ kind: "secret", item: secret })} />}
          {!loading && overview && view === "connections" && <ConnectionsPage overview={overview} notice={connectionNotice} onNotice={setConnectionNotice} onChanged={async () => { await load(); }} />}
        </div>
      </main>

      {dialog && overview && <ResourceDialog kind={dialog} overview={overview} project={editingProject ?? undefined} server={editingServer ?? undefined} deployAppID={deployAppID} onClose={() => setDialog(null)} onChanged={async () => { await load(); setDialog(null); }} onDeployed={async (id) => { setSelectedID(id); setView("deployments"); setDialog(null); await load(); }} />}
      {deleteTarget && overview && <DeleteDialog target={deleteTarget} overview={overview} onClose={() => setDeleteTarget(null)} onDeleted={async () => { await load(); setDeleteTarget(null); }} />}
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
    const frame = window.requestAnimationFrame(() => railRef.current?.querySelector<HTMLElement>('button[aria-current="page"]')?.focus());
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
      { id: "connections", label: "Connections", icon: <PlugsConnected size={18} />, count: overview?.githubApps.length ?? 0 },
    ] },
  ];

  return <>
    <div className={`nav-scrim ${open ? "visible" : ""}`} onClick={onClose} />
    <aside ref={railRef} id="primary-navigation" className={`rail ${open ? "open" : ""}`} aria-label="Primary navigation">
      <div className="wordmark"><Mark /><div><span>Dispatch</span><small>Deployment control</small></div><button aria-label="Close navigation" onClick={onClose}><X size={20} weight="bold" /></button></div>
      <nav className="nav-list">
        {groups.map((group) => <div className="nav-group" key={group.label}><span className="nav-eyebrow">{group.label}</span>{group.entries.map((entry) => <button key={entry.id} className={view === entry.id ? "active" : ""} aria-current={view === entry.id ? "page" : undefined} onClick={() => onNavigate(entry.id)}>{entry.icon}<span>{entry.label}</span>{entry.count > 0 && <small aria-hidden="true">{entry.count}</small>}</button>)}</div>)}
      </nav>
      <footer className="rail-foot"><div className="control-mark"><span className={overview?.demo ? "demo-dot" : "live-dot"} /><div><strong>Control plane</strong><span>{overview?.demo ? "Demonstration mode" : "Connected"}</span></div></div></footer>
    </aside>
  </>;
}

function PageHeader({ view, action, title, showDescription = true }: { view: View; action?: { label: string; onClick: () => void; disabled?: boolean; icon?: ReactNode; tone?: "primary" | "quiet" }; title?: string; showDescription?: boolean }) {
  return <header className="page-header"><div><h1>{title ?? viewCopy[view].title}</h1>{showDescription && <p>{viewCopy[view].description}</p>}</div>{action && <button className={action.tone === "quiet" ? "quiet-button" : "primary-button"} disabled={action.disabled} onClick={action.onClick}>{action.icon ?? <Plus size={16} weight="bold" />}{action.label}</button>}</header>;
}

function DeploymentsPage({ overview, selected, logs, onSelect, onCloseDetails, onOpen, onCreateApplication, onCancel }: { overview: Overview; selected?: Deployment; logs: DeploymentLog[]; onSelect: (id: string) => void; onCloseDetails: () => void; onOpen: (dialog: Dialog) => void; onCreateApplication: () => void; onCancel: () => void }) {
  const groups = useMemo(() => ({
    active: overview.deployments.filter((item) => !["succeeded", "failed", "cancelled"].includes(item.state)),
    failed: overview.deployments.filter((item) => item.state === "failed"),
    history: overview.deployments.filter((item) => ["succeeded", "cancelled"].includes(item.state)),
  }), [overview.deployments]);
  const runnableApps = overview.apps.filter((app) => !app.template);
  const ready = overview.servers.some((server) => server.state === "ready" && server.runtime !== "relay");
  const canAddApp = ready && overview.projects.length > 0;
  const action = runnableApps.length > 0
    ? { label: "Deploy revision", onClick: () => onOpen("deploy"), icon: <RocketLaunch size={16} /> }
    : canAddApp ? { label: "Add application", onClick: onCreateApplication } : undefined;
  return <div className="page-layout">
    <PageHeader view="deployments" action={action} />
    <div className={`deployment-workspace ${selected ? "with-evidence" : ""}`}>
      <section className="deployment-board" aria-label="Deployment activity">
        {overview.deployments.length === 0 && <EmptyState title="No deployment activity" body={runnableApps.length ? "Deploy a revision when you are ready." : "Applications will appear here after their first deployment."} action={action} />}
        {groups.active.length > 0 && <DeploymentGroup title="In progress" count={groups.active.length} deployments={groups.active} selectedID={selected?.id} onSelect={onSelect} />}
        {groups.failed.length > 0 && <DeploymentGroup title="Failed" count={groups.failed.length} deployments={groups.failed} selectedID={selected?.id} onSelect={onSelect} />}
        {groups.history.length > 0 && <DeploymentGroup title="History" count={groups.history.length} deployments={groups.history} selectedID={selected?.id} onSelect={onSelect} />}
      </section>
      {selected && <aside className="evidence-panel" aria-label="Deployment evidence"><Evidence deployment={selected} logs={logs} onCancel={onCancel} onClose={onCloseDetails} /></aside>}
    </div>
  </div>;
}

type EventHookTarget = { type: "trigger"; trigger: EventTrigger } | { type: "group"; group: PreviewGroup };

function EventsPage({ overview, onConfigure, onChanged }: { overview: Overview; onConfigure: () => void; onChanged: () => Promise<void> }) {
  const [hookTarget, setHookTarget] = useState<EventHookTarget | null>(null);
  const [section, setSection] = useState<"rules" | "activity">("rules");
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
      <button aria-pressed={section === "rules"} className={section === "rules" ? "active" : ""} onClick={() => setSection("rules")}>Rules <span>{rules.length}</span></button>
      <button aria-pressed={section === "activity"} className={section === "activity" ? "active" : ""} onClick={() => setSection("activity")}>Activity <span>{activity.length}</span></button>
    </nav>
    {section === "rules" && <section className="event-section" aria-labelledby="event-rules-title">
      <div className="section-toolbar"><div><h2 id="event-rules-title">Pull request rules</h2><p>Commands accepted from trusted pull request comments.</p></div></div>
      {rules.length ? <div className="resource-table-wrap"><table className="resource-table event-table"><thead><tr><th>Rule</th><th>Trigger</th><th>Target</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{rules.map((rule) => <tr key={rule.id}><td data-label="Rule"><strong>{rule.name}</strong><small>{rule.hooks === "None" ? "No deployment hooks" : `${rule.hooks} hooks`}</small></td><td data-label="Trigger"><code>{rule.command}</code><div className="event-sources">{rule.repositories.map((repository) => <small key={repository}>{repository}</small>)}</div></td><td data-label="Target">{rule.targetLabel}</td><td data-label="Status"><StatusLabel state={rule.enabled ? "enabled" : "disabled"} /></td><td className="row-actions"><button className="table-action" aria-label={`Edit deployment hooks for ${rule.name}`} onClick={() => setHookTarget(rule.hookTarget)}><PencilSimple size={15} />Hooks</button></td></tr>)}</tbody></table></div> : <EmptyState title="No event rules" body="Configure a pull request command on an application or preview group." action={{ label: "Configure in applications", onClick: onConfigure }} />}
    </section>}
    {section === "activity" && <section className="event-section" aria-labelledby="event-activity-title">
      <div className="section-toolbar"><div><h2 id="event-activity-title">Preview activity</h2><p>Latest 30 environments created from pull request commands.</p></div></div>
      {activity.length ? <div className="resource-table-wrap"><table className="resource-table event-table"><thead><tr><th>Environment</th><th>Source</th><th>Status</th><th>Updated</th><th className="actions-head"><span className="sr-only">Preview URL</span></th></tr></thead><tbody>{activity.slice(0, 30).map((item) => <tr key={item.id}><td data-label="Environment"><strong>{item.name}</strong></td><td data-label="Source"><div className="event-sources">{item.sources.map((source) => <code key={source}>{source}</code>)}</div></td><td data-label="Status"><StatusLabel state={item.state} /></td><td data-label="Updated">{relative(item.updatedAt)}</td><td className="row-actions">{item.url && <a className="table-action" aria-label={`Open preview for ${item.name}`} href={item.url} target="_blank" rel="noreferrer">Open<ArrowRight size={14} /></a>}</td></tr>)}</tbody></table></div> : <EmptyState title="No preview activity" body="Preview environments will appear here after an event rule is triggered." />}
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
    <header><div><h2 id="event-hook-editor-title">Deployment hooks: {title}</h2><p>Hooks run only for deployments started by this event rule.</p></div></header>
    <form onSubmit={save} aria-busy={busy}>
      {trigger ? <HookFields preDeployHook={preDeployHook} postDeployHook={postDeployHook} onPreDeployHook={setPreDeployHook} onPostDeployHook={setPostDeployHook} /> : components.map((component, index) => <fieldset className="event-component-hooks" key={component.id ?? component.alias}><legend>{component.alias || `Component ${index + 1}`} <span>{component.repository}</span></legend><HookFields preDeployHook={component.preDeployHook ?? ""} postDeployHook={component.postDeployHook ?? ""} onPreDeployHook={(value) => changeComponent(index, { preDeployHook: value })} onPostDeployHook={(value) => changeComponent(index, { postDeployHook: value })} /><HookCredentialBindings secrets={secrets} selected={component.secretIds ?? []} onChange={(secretIds) => changeComponent(index, { secretIds })} /></fieldset>)}
	  {trigger && <HookCredentialBindings secrets={secrets} selected={secretIds} onChange={setSecretIds} />}
      <details className="event-hook-context"><summary>Available event variables (8)</summary><p>Available as environment variables in both hooks.</p><div>{["DISPATCH_PREVIEW_TAG", "DISPATCH_EVENT_REPOSITORY", "DISPATCH_EVENT_PULL_REQUEST_NUMBER", "DISPATCH_EVENT_HEAD_REF", "DISPATCH_EVENT_HEAD_SHA", "DISPATCH_EVENT_ACTOR", "DISPATCH_EVENT_COMMAND", "DISPATCH_EVENT_ARGUMENTS"].map((name) => <code key={name}>{name}</code>)}</div></details>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="builder-actions"><button type="button" className="quiet-button" onClick={onClose}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? "Saving..." : "Save hooks"}</button></div>
    </form>
  </section>;
}

function HookFields({ preDeployHook, postDeployHook, onPreDeployHook, onPostDeployHook }: { preDeployHook: string; postDeployHook: string; onPreDeployHook: (value: string) => void; onPostDeployHook: (value: string) => void }) {
  const id = useId();
  const [preFileName, setPreFileName] = useState("");
  const [postFileName, setPostFileName] = useState("");
  const [uploadError, setUploadError] = useState("");

  async function loadScript(event: ChangeEvent<HTMLInputElement>, onChange: (value: string) => void, onFileName: (value: string) => void) {
    const file = event.target.files?.[0];
    if (!file) return;
    setUploadError("");
    try {
      onChange(await readHookScriptFile(file));
      onFileName(file.name);
    } catch (cause) {
      setUploadError((cause as Error).message);
    } finally {
      event.target.value = "";
    }
  }

  return <div className="event-hook-fields">
    <section className="hook-script-field"><header><div><label htmlFor={`${id}-pre`}>Build and publish</label><small>Runs in Bash before deployment. The generated preview tag is passed as $1.</small></div><label className="hook-upload"><UploadSimple size={14} /><span>Upload script</span><input className="sr-only" type="file" accept=".sh,.bash,text/x-shellscript,text/plain" onChange={(event) => void loadScript(event, onPreDeployHook, setPreFileName)} /></label></header><textarea id={`${id}-pre`} value={preDeployHook} onChange={(event) => { onPreDeployHook(event.target.value); setPreFileName(""); }} placeholder={'#!/usr/bin/env bash\nset -euo pipefail\n\ndocker buildx build --push --tag "$REGISTRY/app:$1" .'} spellCheck={false} />{preFileName && <small className="hook-file-name">Loaded {preFileName}</small>}</section>
    <section className="hook-script-field"><header><div><label htmlFor={`${id}-post`}>After deployment</label><small>Optional Bash script that runs after the release becomes ready.</small></div><label className="hook-upload"><UploadSimple size={14} /><span>Upload script</span><input className="sr-only" type="file" accept=".sh,.bash,text/x-shellscript,text/plain" onChange={(event) => void loadScript(event, onPostDeployHook, setPostFileName)} /></label></header><textarea id={`${id}-post`} value={postDeployHook} onChange={(event) => { onPostDeployHook(event.target.value); setPostFileName(""); }} placeholder={'echo "Ready at $DISPATCH_DEPLOYMENT_URL"'} spellCheck={false} />{postFileName && <small className="hook-file-name">Loaded {postFileName}</small>}</section>
    {uploadError && <p className="form-error hook-upload-error" role="alert">{uploadError}</p>}
    <details className="hook-output-contract"><summary>Build output contract</summary><div><dl><div><dt>Preview tag</dt><dd><code>$1</code> and <code>$DISPATCH_PREVIEW_TAG</code></dd></div><div><dt>Named outputs</dt><dd>Persisted with the deployment and exposed to later hooks as <code>DISPATCH_OUTPUT_*</code>.</dd></div><div><dt>Deployment values</dt><dd>Structured Helm overrides applied only to this deployment.</dd></div></dl><pre>{`dispatch-hook output set backendImage "$backend_image"
dispatch-hook output set uiImage "$ui_image"
dispatch-hook output set imageTag "$preview_tag"

dispatch-hook helm set images.registry "$registry_host"
dispatch-hook helm set images.namespace "$registry_namespace"
dispatch-hook helm set images.backend.tag "$preview_tag"
dispatch-hook helm set images.ui.tag "$preview_tag"`}</pre><small>The helper updates a versioned result atomically. Existing <code>DISPATCH_OUTPUT_FILE</code>, <code>DISPATCH_VALUES_FILE</code>, and recognized summary output remain supported for older scripts. Do not publish credentials as outputs.</small></div></details>
  </div>;
}

function HookCredentialBindings({ secrets, selected, onChange }: { secrets: Secret[]; selected: string[]; onChange: (ids: string[]) => void }) {
  return <fieldset className="event-secret-bindings"><legend>Build credentials</legend><p>Selected values are decrypted only for this build. An attached <code>SSH_PRIVATE_KEY</code> also configures Git automatically.</p>{secrets.length ? <div>{secrets.map((secret) => <label key={secret.id}><input type="checkbox" checked={selected.includes(secret.id)} onChange={(event) => onChange(event.target.checked ? [...selected, secret.id] : selected.filter((id) => id !== secret.id))} /><span><strong>{secret.name}</strong><code>{secret.environmentVariable}</code></span></label>)}</div> : <small>Create credentials on the Secrets page, then return here to attach them.</small>}</fieldset>;
}

function overviewRuleName(trigger: EventTrigger) {
  return `${trigger.repository} ${trigger.command}`;
}

function githubAPIFor(webURL: string) {
  try {
    const parsed = new URL(webURL);
    return parsed.hostname.toLowerCase() === "github.com" ? "https://api.github.com" : parsed.origin + "/api/v3";
  } catch {
    return "";
  }
}

const defaultGitHubWebURL = "https://github.com";
const defaultGitHubAPIURL = "https://api.github.com";

function suggestedGitHubAppName() {
  const value = new Uint32Array(1);
  crypto.getRandomValues(value);
  return `Dispatch-${value[0].toString(16).padStart(8, "0")}`;
}

function githubAppSettingsURL(connection: GitHubAppConnection) {
  const ownerIsOrganization = connection.registrationOwnerType?.toLowerCase() === "organization";
  const root = ownerIsOrganization && connection.registrationOwner
    ? `${connection.webUrl}/organizations/${encodeURIComponent(connection.registrationOwner)}/settings/apps`
    : `${connection.webUrl}/settings/apps`;
  return connection.slug ? `${root}/${encodeURIComponent(connection.slug)}` : root;
}

function githubInstallationSettingsURL(connection: GitHubAppConnection) {
  if (!connection.installationId) return githubAppSettingsURL(connection);
  if (connection.installationUrl) return connection.installationUrl;
  if (connection.registrationOwnerType?.toLowerCase() === "organization" && connection.installationAccount) {
    return `${connection.webUrl}/organizations/${encodeURIComponent(connection.installationAccount)}/settings/installations/${connection.installationId}`;
  }
  return `${connection.webUrl}/settings/installations/${connection.installationId}`;
}

function ConnectionsPage({ overview, notice, onNotice, onChanged }: { overview: Overview; notice: string; onNotice: (value: string) => void; onChanged: () => Promise<void> }) {
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<GitHubAppConnection | null>(null);
  const [method, setMethod] = useState<"manifest" | "manual">("manifest");
  const [name, setName] = useState(suggestedGitHubAppName);
  const [webURL, setWebURL] = useState(defaultGitHubWebURL);
  const [apiURL, setAPIURL] = useState(defaultGitHubAPIURL);
  const [owner, setOwner] = useState("");
  const [ownerType, setOwnerType] = useState<"" | "personal" | "organization">("");
  const [appID, setAppID] = useState("");
  const [clientID, setClientID] = useState("");
  const [slug, setSlug] = useState("");
  const [installationID, setInstallationID] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [webhookSecret, setWebhookSecret] = useState("");
  const relayServers = overview.servers.filter((server) => server.runtime === "relay");
  const [eventDelivery, setEventDelivery] = useState<"direct" | "relay">(relayServers.length ? "relay" : "direct");
  const [relayServerID, setRelayServerID] = useState(relayServers[0]?.id ?? "");
  const [busyID, setBusyID] = useState("");
  const [installations, setInstallations] = useState<Record<string, GitHubAppInstallation[]>>({});
  const [repositories, setRepositories] = useState<Record<string, GitHubRepository[]>>({});
  const [repositoryOpen, setRepositoryOpen] = useState("");
  const [repositoryQuery, setRepositoryQuery] = useState("");
  const [confirmDelete, setConfirmDelete] = useState("");
  const [copied, setCopied] = useState("");
  const [error, setError] = useState("");
  const privateKeyFile = useRef<HTMLInputElement>(null);

  function reset() {
    setCreating(false);
    setEditing(null);
    setMethod("manifest");
    setName(suggestedGitHubAppName());
    setWebURL(defaultGitHubWebURL);
    setAPIURL(defaultGitHubAPIURL);
    setOwner("");
    setOwnerType("");
    setAppID("");
    setClientID("");
    setSlug("");
    setInstallationID("");
    setPrivateKey("");
    setWebhookSecret("");
    setEventDelivery(relayServers.length ? "relay" : "direct");
    setRelayServerID(relayServers[0]?.id ?? "");
    setError("");
  }

  function edit(connection: GitHubAppConnection) {
    setEditing(connection);
    setCreating(true);
    setMethod("manual");
    setName(connection.name);
    setWebURL(connection.webUrl);
    setAPIURL(connection.apiUrl);
    setOwner(connection.registrationOwner || "");
    setOwnerType(connection.registrationOwnerType?.toLowerCase() === "organization" ? "organization" : "personal");
    setAppID(String(connection.appId));
    setClientID(connection.clientId || "");
    setSlug(connection.slug || "");
    setInstallationID(connection.installationId ? String(connection.installationId) : "");
    setPrivateKey("");
    setWebhookSecret("");
    setError("");
  }

  function changeWebURL(value: string) {
    const previousDerived = githubAPIFor(webURL);
    setWebURL(value);
    if (!apiURL || apiURL === previousDerived) setAPIURL(githubAPIFor(value));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    onNotice("");
    setBusyID(editing?.id || "create");
    try {
      if (method === "manifest" && !editing) {
		if (!ownerType) throw new Error("Choose where the GitHub App should be created.");
        const flow = await api.startGitHubAppManifest({ name, webUrl: webURL, apiUrl: apiURL, ownerType, owner: ownerType === "organization" ? owner : "", relayServerId: eventDelivery === "relay" ? relayServerID : undefined });
        const form = document.createElement("form");
        form.method = "post";
        form.action = flow.action;
        const manifest = document.createElement("input");
        manifest.type = "hidden";
        manifest.name = "manifest";
        manifest.value = JSON.stringify(flow.manifest);
        form.appendChild(manifest);
        document.body.appendChild(form);
        form.submit();
        return;
      }
      const body: Record<string, unknown> = {
        name, webUrl: webURL, apiUrl: apiURL, appId: Number(appID), clientId: clientID, slug,
        installationId: installationID ? Number(installationID) : 0,
      };
      if (!editing && eventDelivery === "relay") body.relayServerId = relayServerID;
      if (privateKey || webhookSecret) {
        body.privateKey = privateKey;
        body.webhookSecret = webhookSecret;
      }
      if (editing) await api.updateGitHubApp(editing.id, body);
      else await api.createGitHubApp(body);
      await onChanged();
      reset();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function loadPrivateKey(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    try {
      setPrivateKey(await readSecretTextFile(file));
      setError("");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      event.target.value = "";
    }
  }

  async function verify(connection: GitHubAppConnection) {
    setBusyID(connection.id);
    setError("");
    try {
      await api.verifyGitHubApp(connection.id);
      onNotice(connection.name + " is connected.");
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function findInstallations(connection: GitHubAppConnection) {
    setBusyID(connection.id);
    setError("");
    try {
      const items = await api.githubAppInstallations(connection.id);
      setInstallations((current) => ({ ...current, [connection.id]: items }));
      if (!items.length) onNotice("No installation was found. Install the App in GitHub first.");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function selectInstallation(connection: GitHubAppConnection, id: number) {
    setBusyID(connection.id);
    setError("");
    try {
      await api.updateGitHubApp(connection.id, { installationId: id });
      await api.verifyGitHubApp(connection.id);
      setInstallations((current) => ({ ...current, [connection.id]: [] }));
      onNotice(connection.name + " is installed and verified.");
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function toggleRepositories(connection: GitHubAppConnection) {
    if (repositoryOpen === connection.id) {
      setRepositoryOpen("");
      setRepositoryQuery("");
      return;
    }
    setRepositoryOpen(connection.id);
    setRepositoryQuery("");
    setBusyID(connection.id);
    setError("");
    try {
      const items = await api.githubAppRepositories(connection.id);
      setRepositories((current) => ({ ...current, [connection.id]: items }));
    } catch (cause) {
      setRepositoryOpen("");
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function remove(connection: GitHubAppConnection) {
    setBusyID(connection.id);
    setError("");
    try {
      await api.deleteGitHubApp(connection.id);
      setConfirmDelete("");
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function copyWebhook(connection: GitHubAppConnection) {
    try {
      await navigator.clipboard.writeText(connection.webhookUrl);
      setCopied(connection.id);
      window.setTimeout(() => setCopied((current) => current === connection.id ? "" : current), 1600);
    } catch {
      setError("Could not copy the webhook URL.");
    }
  }

  return <div className="page-layout connections-page">
    <PageHeader view="connections" action={creating
      ? { label: "Cancel", onClick: reset, icon: <X size={16} weight="bold" />, tone: "quiet" }
      : { label: "Add GitHub App", onClick: () => setCreating(true), icon: <GithubLogo size={17} weight="fill" /> }} />
    {notice && <div className="connection-notice" role="status"><CheckCircle size={18} weight="fill" /><span>{notice}</span><button aria-label="Dismiss message" onClick={() => onNotice("")}><X size={15} /></button></div>}
    {error && <p className="form-error connection-error" role="alert">{error}</p>}
    {creating ? <section className="inline-create connection-editor" aria-labelledby="connection-editor-title">
      <header><div><h2 id="connection-editor-title">{editing ? "Edit GitHub App" : "Add GitHub App"}</h2><p>{editing ? "Update the registration or installation used by this connection." : "Create a preconfigured App or connect an existing registration."}</p></div></header>
      {!editing && <div className="connection-method" role="radiogroup" aria-label="GitHub App setup method">
        <button type="button" role="radio" aria-checked={method === "manifest"} className={method === "manifest" ? "active" : ""} onClick={() => setMethod("manifest")}><GithubLogo size={19} weight="fill" /><span><strong>Create in GitHub</strong><small>Permissions and webhooks are filled in for you.</small></span></button>
        <button type="button" role="radio" aria-checked={method === "manual"} className={method === "manual" ? "active" : ""} onClick={() => setMethod("manual")}><Key size={19} /><span><strong>Existing App</strong><small>Enter an App ID and its credentials.</small></span></button>
      </div>}
      <form className="connection-form" onSubmit={submit} aria-busy={busyID === (editing?.id || "create")}>
        <div className="connection-grid">
          <label><span>GitHub App name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="Dispatch-a1b2c3d4" maxLength={34} required /><small>Generated to be unique on this GitHub host.</small></label>
          <label><span>GitHub URL</span><input value={webURL} onChange={(event) => changeWebURL(event.target.value)} placeholder="https://github.example.com" required spellCheck={false} /></label>
          {method === "manifest" && !editing ? <><label><span>Registration owner</span><select value={ownerType} onChange={(event) => { const value = event.target.value as "" | "personal" | "organization"; setOwnerType(value); if (value !== "organization") setOwner(""); }} required><option value="">Choose an account type</option><option value="personal">My personal account</option><option value="organization">GitHub organization</option></select><small>This controls where the App appears in GitHub settings.</small></label>{ownerType === "organization" && <label><span>Organization login</span><input value={owner} onChange={(event) => setOwner(event.target.value)} placeholder="platform-team" required spellCheck={false} /></label>}</> : <>
            <label><span>App ID</span><input inputMode="numeric" value={appID} onChange={(event) => setAppID(event.target.value)} placeholder="123456" required /></label>
            <label><span>Installation ID</span><input inputMode="numeric" value={installationID} onChange={(event) => setInstallationID(event.target.value)} placeholder="Add after installation" /></label>
          </>}
        </div>
        {!editing && <fieldset className="connection-delivery"><legend>Event delivery</legend><label><input type="radio" name="event-delivery" checked={eventDelivery === "relay"} onChange={() => setEventDelivery("relay")} disabled={!relayServers.length} /><span><strong>Webhook relay</strong><small>{relayServers.length ? "Recommended when GitHub cannot reach this controller." : "Add a relay server to enable this option."}</small></span></label><label><input type="radio" name="event-delivery" checked={eventDelivery === "direct"} onChange={() => setEventDelivery("direct")} /><span><strong>Direct webhook</strong><small>GitHub sends events to this controller's public URL.</small></span></label>{eventDelivery === "relay" && <label className="relay-delivery-server"><span>Relay server</span><select value={relayServerID} onChange={(event) => setRelayServerID(event.target.value)} required><option value="">Select a relay</option>{relayServers.map((server) => <option key={server.id} value={server.id}>{server.name}</option>)}</select></label>}</fieldset>}
        <details className="connection-advanced"><summary>Advanced GitHub settings <span>Custom Enterprise API</span></summary><div className="connection-grid">
          <label><span>API URL</span><input value={apiURL} onChange={(event) => setAPIURL(event.target.value)} placeholder="https://github.example.com/api/v3" required spellCheck={false} /></label>
          {(method === "manual" || editing) && <><label><span>App slug</span><input value={slug} onChange={(event) => setSlug(event.target.value)} placeholder="Filled during verification" spellCheck={false} /></label><label><span>Client ID</span><input value={clientID} onChange={(event) => setClientID(event.target.value)} placeholder="Optional" spellCheck={false} /></label></>}
        </div></details>
        {(method === "manual" || !!editing) && <div className="connection-credentials">
          <div className="credential-heading"><div><strong>App credentials</strong><small>{editing ? "Leave both blank to retain the saved credentials." : "Values are encrypted and become write-only after saving."}</small></div><><input ref={privateKeyFile} className="sr-only" type="file" accept=".pem,.key,application/x-pem-file,text/plain" onChange={(event) => void loadPrivateKey(event)} /><button type="button" className="quiet-button" onClick={() => privateKeyFile.current?.click()}><UploadSimple size={15} />Upload PEM</button></></div>
          <div className="connection-grid">
            <label><span>Webhook secret</span><input type="password" value={webhookSecret} onChange={(event) => setWebhookSecret(event.target.value)} placeholder={editing ? "Saved" : "At least 16 characters"} required={!editing} autoComplete="new-password" /></label>
            <label className="private-key-field"><span>Private key</span><textarea value={privateKey} onChange={(event) => setPrivateKey(event.target.value)} placeholder={editing ? "Saved" : "Paste the RSA private key PEM"} required={!editing} spellCheck={false} /></label>
          </div>
        </div>}
        {method === "manifest" && !editing && <div className="manifest-summary"><LockSimple size={18} /><p>Dispatch requests repository contents read, pull requests read, and issue comments write. GitHub returns the private key and webhook secret directly to this controller.</p></div>}
        <div className="connection-actions"><button type="button" className="quiet-button" onClick={reset}>Cancel</button><button className="primary-button" disabled={!!busyID || !name.trim() || !webURL.trim() || !apiURL.trim() || (!editing && eventDelivery === "relay" && !relayServerID) || (method === "manifest" && !editing && (!ownerType || (ownerType === "organization" && !owner.trim()))) || ((method === "manual" || !!editing) && (!appID || (!editing && (!privateKey.trim() || !webhookSecret.trim()))))}>{busyID ? "Working..." : method === "manifest" && !editing ? "Continue to GitHub" : editing ? "Save connection" : "Add connection"}</button></div>
      </form>
    </section> : overview.githubApps.length ? <div className="connection-list">{overview.githubApps.map((connection) => {
      const installURL = connection.slug ? connection.webUrl + "/apps/" + connection.slug + "/installations/new" : "";
      const settingsURL = githubAppSettingsURL(connection);
      const installationSettingsURL = githubInstallationSettingsURL(connection);
      const choices = installations[connection.id] || [];
      const installedRepositories = repositories[connection.id] || [];
      const visibleRepositories = installedRepositories.filter((repository) => repository.fullName.toLowerCase().includes(repositoryQuery.trim().toLowerCase()));
      return <section className="connection-row" key={connection.id}>
        <div className="connection-identity"><span className="github-mark"><GithubLogo size={21} weight="fill" /></span><div><strong>{connection.name}</strong><small>{new URL(connection.webUrl).host}{connection.registrationOwner ? " / " + connection.registrationOwner : ""}</small></div></div>
        <div className="connection-metadata"><div><span>App ID</span><code>{connection.appId}</code></div><div><span>Installation</span><strong>{connection.installationId || "Not installed"}</strong></div><div><span>Events</span><strong>{connection.relayWebhookId ? "Relay" : "Direct"}</strong></div><div><span>Status</span><StatusLabel state={connection.state} /></div></div>
        <div className="connection-row-actions">
          {connection.state === "needs_installation" && installURL && <a className="table-action primary-link" href={installURL} target="_blank" rel="noreferrer">Install App<ArrowRight size={14} /></a>}
          {connection.state === "needs_installation" && <button className="table-action" disabled={busyID === connection.id} onClick={() => void findInstallations(connection)}><ArrowClockwise size={15} />Find installation</button>}
          {connection.installationId ? <button className="table-action" disabled={busyID === connection.id} onClick={() => void verify(connection)}><CheckCircle size={15} />Verify</button> : null}
          {connection.installationId ? <button className="table-action" disabled={busyID === connection.id} aria-expanded={repositoryOpen === connection.id} onClick={() => void toggleRepositories(connection)}><FolderSimple size={15} />Repositories</button> : null}
          <a className="table-action" href={connection.installationId ? installationSettingsURL : settingsURL} target="_blank" rel="noreferrer">Manage access<ArrowSquareOut size={14} /></a>
          <button className="table-action" onClick={() => edit(connection)}><PencilSimple size={15} />Edit</button>
          <button className="delete-action" onClick={() => setConfirmDelete(connection.id)}><Trash size={15} />Remove</button>
        </div>
        {confirmDelete === connection.id && <div className="connection-remove-warning" role="alert"><WarningCircle size={19} weight="fill" /><div><strong>Remove from Dispatch?</strong><p>The GitHub App and its reserved name will remain in GitHub. Delete it from GitHub settings if you no longer need it.</p></div><a className="table-action" href={settingsURL} target="_blank" rel="noreferrer">Open GitHub settings<ArrowSquareOut size={14} /></a><button className="quiet-button" onClick={() => setConfirmDelete("")}>Cancel</button><button className="danger-button" disabled={busyID === connection.id} onClick={() => void remove(connection)}>{busyID === connection.id ? "Removing..." : "Remove from Dispatch"}</button></div>}
        {choices.length > 0 && <div className="installation-picker"><div><strong>Choose an installation</strong><small>Select the account this connection should use.</small></div>{choices.map((item) => <button type="button" key={item.id} onClick={() => void selectInstallation(connection, item.id)}><span>{item.account}</span><small>{item.target} / {item.id}</small><ArrowRight size={14} /></button>)}</div>}
        {repositoryOpen === connection.id && <div className="connection-repositories">
          <div className="repository-panel-head"><div><strong>Repository access</strong><small>The App webhook receives pull request events for every selected repository.</small></div><div><button className="table-action" onClick={() => void copyWebhook(connection)}>{copied === connection.id ? <Check size={15} /> : <Copy size={15} />}{copied === connection.id ? "Copied" : "Copy endpoint"}</button><a className="table-action primary-link" href={installationSettingsURL} target="_blank" rel="noreferrer">Add repositories<ArrowSquareOut size={14} /></a></div></div>
          {installedRepositories.length > 6 && <label className="repository-search"><span className="sr-only">Filter repositories</span><input value={repositoryQuery} onChange={(event) => setRepositoryQuery(event.target.value)} placeholder="Filter repositories" /></label>}
          {busyID === connection.id ? <div className="repository-access-empty"><strong>Loading repository access</strong><span>Reading the repositories selected for this installation.</span></div> : installedRepositories.length && visibleRepositories.length ? <div className="repository-access-list">{visibleRepositories.map((repository) => <a key={repository.id} href={repository.webUrl} target="_blank" rel="noreferrer"><span><strong>{repository.fullName}</strong><small>{repository.private ? "Private" : "Public"}</small></span><code>{repository.defaultBranch || "default branch"}</code><ArrowSquareOut size={13} /></a>)}</div> : <div className="repository-access-empty"><strong>{installedRepositories.length ? "No matching repositories" : "No repositories selected"}</strong><span>{installedRepositories.length ? "Try another repository name." : "Choose repository access in GitHub, then open this list again."}</span></div>}
        </div>}
      </section>;
    })}</div> : <EmptyState title="No GitHub Apps" body="Connect GitHub.com or GitHub Enterprise for short-lived repository access and signed events." action={{ label: "Add GitHub App", onClick: () => setCreating(true) }} />}
  </div>;
}

function ServersPage({ overview, onChanged, onAdd, onEdit, onRepair, onDelete }: { overview: Overview; onChanged: () => Promise<void>; onAdd: () => void; onEdit: (server: Server) => void; onRepair: (server: Server) => void; onDelete: (server: Server) => void }) {
  const targets = overview.servers.filter((server) => server.runtime !== "relay");
  const relays = overview.servers.filter((server) => server.runtime === "relay");
  const ready = targets.filter((server) => server.state === "ready").length;
  const connected = relays.filter((server) => server.state === "connected").length;
  return <div className="page-layout">
    <PageHeader view="servers" action={{ label: "Add server", onClick: onAdd }} />
    <ResourceSummary items={[{ label: "Targets", value: targets.length }, { label: "Target ready", value: ready }, { label: "Relays", value: relays.length }, { label: "Relay connected", value: connected }]} />
    <section className="server-section"><div className="section-title"><div><h2>Deployment targets</h2><p>Servers that build or run applications.</p></div></div>
      {targets.length ? <div className="resource-table-wrap"><table className="resource-table server-table"><thead><tr><th>Server</th><th>Connection</th><th>Runtime</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{targets.map((server) => <tr key={server.id}><td data-label="Server"><strong>{server.name}</strong>{server.address === "local" && <small className="managed-label"><LockSimple size={12} />Managed by this controller</small>}</td><td data-label="Connection"><span className="connection">{server.address === "local" ? "Local Docker socket" : server.runtime === "openshift" ? server.address : server.kubernetes?.context || server.kubernetes?.kubeconfigPath || server.address}</span></td><td data-label="Runtime">{server.runtime === "docker" ? "Docker" : server.runtime === "openshift" ? "OpenShift" : "Kubernetes"}</td><td data-label="Status"><StatusLabel state={server.state} /></td><td className="row-actions">{server.address !== "local" && <>{server.runtime === "openshift" && <button aria-label={`Repair ${server.name}`} onClick={() => onRepair(server)}><ArrowClockwise size={15} />Repair</button>}<button aria-label={`Edit ${server.name}`} onClick={() => onEdit(server)}><PencilSimple size={15} />Edit</button><button className="delete-action" aria-label={`Delete ${server.name}`} onClick={() => onDelete(server)}><Trash size={15} />Delete</button></>}</td></tr>)}</tbody></table></div> : <div className="section-empty">No deployment targets registered.</div>}
    </section>
    <section className="server-section relay-section"><div className="section-title"><div><h2>Event relays</h2><p>Public webhook receivers that replay events over outbound connections.</p></div></div>
      {relays.length ? <div className="relay-server-list">{relays.map((server) => <RelayServerRow key={server.id} server={server} webhooks={overview.relayWebhooks.filter((hook) => hook.serverId === server.id)} connections={overview.githubApps} onChanged={onChanged} onEdit={() => onEdit(server)} onDelete={() => onDelete(server)} />)}</div> : <div className="section-empty">Add a webhook relay when this controller cannot accept inbound traffic.</div>}
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
      {webhooks.length ? <div className="relay-webhook-list">{webhooks.map((hook) => <div key={hook.id}><span><strong>{hook.name}</strong><small>{hook.provider}{hook.lastDeliveryAt ? `, last event ${relative(hook.lastDeliveryAt)}` : ""}</small></span><code>{hook.url}</code><button onClick={() => void copy(hook)}>{copied === hook.id ? <Check size={14} /> : <Copy size={14} />}{copied === hook.id ? "Copied" : "Copy URL"}</button><button className="delete-action" disabled={busy} onClick={() => void remove(hook)}><Trash size={14} />Remove</button></div>)}</div> : <p className="relay-webhook-empty">No endpoints yet. Each provider connection can use its own URL.</p>}
      {adding ? <form className="relay-webhook-form" onSubmit={add}><label><span>Name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="Repository events" required /></label><label><span>Provider</span><input value={provider} onChange={(event) => setProvider(event.target.value.toLowerCase())} placeholder="github" pattern="[a-z][a-z0-9_-]{0,31}" required /><small>Adapters are independent from relay transport.</small></label>{provider === "github" && <label><span>GitHub connection</span><select value={connectionID} onChange={(event) => setConnectionID(event.target.value)} required><option value="">Select a connection</option>{connections.map((connection) => <option key={connection.id} value={connection.id}>{connection.name}</option>)}</select></label>}<div className="relay-webhook-actions"><button type="button" className="quiet-button" onClick={() => setAdding(false)}>Cancel</button><button className="primary-button" disabled={busy || !name.trim() || !provider.trim() || (provider === "github" && !connectionID)}>{busy ? "Creating..." : "Create endpoint"}</button></div></form> : <button className="quiet-button relay-add-webhook" onClick={() => setAdding(true)}><Plus size={15} />Add endpoint</button>}
      {error && <p className="form-error" role="alert">{error}</p>}
    </div></details>
  </article>;
}

function ProjectsPage({ overview, onAdd, onEdit, onDelete }: { overview: Overview; onAdd: () => void; onEdit: (project: Project) => void; onDelete: (project: Project) => void }) {
  return <div className="page-layout">
    <PageHeader view="projects" action={{ label: "Add project", onClick: onAdd }} />
    <ResourceSummary items={[{ label: "Projects", value: overview.projects.length }, { label: "Applications", value: overview.apps.length }]} />
    {overview.projects.length ? <div className="resource-table-wrap"><table className="resource-table"><thead><tr><th>Project</th><th>Description</th><th>Applications</th><th>Created</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{overview.projects.map((project) => <tr key={project.id}><td data-label="Project"><strong>{project.name}</strong></td><td data-label="Description">{project.description || <span className="muted-value">No description</span>}</td><td data-label="Applications">{overview.apps.filter((app) => app.projectId === project.id).length}</td><td data-label="Created">{new Date(project.createdAt).toLocaleDateString()}</td><td className="row-actions"><button aria-label={`Edit ${project.name}`} onClick={() => onEdit(project)}><PencilSimple size={15} />Edit</button><button className="delete-action" aria-label={`Delete ${project.name}`} onClick={() => onDelete(project)}><Trash size={15} />Delete</button></td></tr>)}</tbody></table></div> : <EmptyState title="No projects" body="Create a project when you want to group applications." action={{ label: "Add project", onClick: onAdd }} />}
  </div>;
}

const secretTypeOptions: Array<{ value: SecretType; label: string; defaultName: string; environmentVariable: string; placeholder: string; help: string }> = [
  { value: "text", label: "Text", defaultName: "", environmentVariable: "SECRET_VALUE", placeholder: "Enter a secret value", help: "General-purpose text for application hooks." },
  { value: "api_token", label: "API token", defaultName: "API token", environmentVariable: "API_TOKEN", placeholder: "Paste an API token", help: "A token issued by an external service." },
  { value: "github_token", label: "GitHub token", defaultName: "GitHub token", environmentVariable: "GITHUB_TOKEN", placeholder: "Paste a GitHub personal access token", help: "Used for private GitHub repositories or hooks." },
  { value: "ssh_private_key", label: "SSH private key", defaultName: "Global deploy key", environmentVariable: "SSH_PRIVATE_KEY", placeholder: "Paste a complete OpenSSH or PEM private key", help: "Used for SSH repository checkout. The public key remains visible after saving." },
  { value: "registry_password", label: "Registry password", defaultName: "Registry password", environmentVariable: "REGISTRY_PASSWORD", placeholder: "Enter the registry password", help: "Used by build hooks that authenticate to a container registry." },
];

const secretTypeLabel = (type: SecretType) => secretTypeOptions.find((option) => option.value === type)?.label ?? "Text";

function SecretsPage({ overview, onChanged, onDelete }: { overview: Overview; onChanged: () => Promise<void>; onDelete: (secret: Secret) => void }) {
  const [editing, setEditing] = useState<Secret | null>(null);
  const [creating, setCreating] = useState(false);
  const [secretType, setSecretType] = useState<SecretType>("text");
  const [name, setName] = useState("");
  const [environmentVariable, setEnvironmentVariable] = useState("SECRET_VALUE");
  const [value, setValue] = useState("");
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
    setName(secret?.name ?? "");
    setEnvironmentVariable(secret?.environmentVariable ?? secretTypeOptions.find((option) => option.value === type)?.environmentVariable ?? "SECRET_VALUE");
    setValue("");
    setSSHSource(secret ? "existing" : "generate");
    setFileName("");
    setError("");
  }

  function closeEditor() {
    setCreating(false);
    setEditing(null);
    setValue("");
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
    const generate = secretType === "ssh_private_key" && sshSource === "generate";
    try {
      const saved = editing
        ? await api.updateSecret(editing.id, { name, type: secretType, environmentVariable, ...(generate ? { generate: true } : value ? { value } : {}) })
        : await api.createSecret({ name, type: secretType, environmentVariable, ...(generate ? { generate: true } : { value }) });
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
  const usesGeneratedKey = secretType === "ssh_private_key" && sshSource === "generate";
  const accept = secretType === "ssh_private_key" ? ".key,.pem,text/plain,application/x-pem-file" : ".txt,.env,.token,.key,.pem,text/plain";

  return <div className="page-layout">
    <PageHeader view="secrets" action={creating
      ? { label: "Cancel", onClick: closeEditor, icon: <X size={16} weight="bold" />, tone: "quiet" }
      : { label: "Add secret", onClick: () => open(), disabled: !overview.secretStorageConfigured }} />
    {!overview.secretStorageConfigured && <div className="error-banner secret-storage-notice" role="status"><strong>Encrypted storage is not configured</strong><span>Set DISPATCH_MASTER_KEY_FILE and restart the controller before adding secrets.</span></div>}
    {creating && <section className="inline-create secret-editor" aria-labelledby="secret-editor-title">
      <header><h2 id="secret-editor-title">{editing ? "Update secret" : "New secret"}</h2><p>Credentials are encrypted and become write-only after saving.</p></header>
      <div className="inline-create-body"><form className="resource-form secret-form" onSubmit={save}>
        <div className="secret-form-main">
          <div className="secret-identity-fields">
            <label className="secret-type-field"><span>Type</span><select value={secretType} onChange={(event) => changeType(event.target.value as SecretType)}>{secretTypeOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select><small>{selectedType.help}</small></label>
            <label><span>Name</span><input required maxLength={80} value={name} onChange={(event) => setName(event.target.value)} placeholder={selectedType.defaultName || "Production secret"} /><small>A recognizable label for operators.</small></label>
            <label className="secret-env-field"><span>Environment variable</span><input required maxLength={128} value={environmentVariable} onChange={(event) => setEnvironmentVariable(event.target.value)} placeholder={selectedType.environmentVariable} spellCheck={false} /><small>Available when attached to a deployment hook.</small></label>
          </div>
          {secretType === "ssh_private_key" && <fieldset className="secret-key-source"><legend>Key source</legend><div>
            <label><input type="radio" name="ssh-source" value="generate" checked={sshSource === "generate"} onChange={() => { setSSHSource("generate"); setValue(""); setFileName(""); }} /><span><strong>Generate new key</strong><small>Global Ed25519 deploy key</small></span></label>
            <label><input type="radio" name="ssh-source" value="existing" checked={sshSource === "existing"} onChange={() => setSSHSource("existing")} /><span><strong>Use existing key</strong><small>Paste or upload a private key</small></span></label>
          </div></fieldset>}
          {!usesGeneratedKey && <div className="secret-value-field">
            <div className="secret-value-heading"><div><label htmlFor="secret-value">{editing ? "New value (optional)" : "Secret value"}</label><small>{editing ? "Leave blank to keep the current value." : "This value cannot be viewed after saving."}</small></div><div className="secret-upload"><input ref={fileInput} className="sr-only" type="file" accept={accept} aria-label="Choose a secret file" onChange={(event) => void loadSecretFile(event)} /><button type="button" className="quiet-button" onClick={() => fileInput.current?.click()}><UploadSimple size={15} />Upload file</button><span aria-live="polite">{fileName || "64 KiB max"}</span></div></div>
            <textarea id="secret-value" required={!editing} value={value} onChange={(event) => { setValue(event.target.value); setFileName(""); }} placeholder={editing ? "Leave blank to retain the saved value" : selectedType.placeholder} spellCheck={false} />
          </div>}
          {error && <p className="form-error" role="alert">{error}</p>}
          <div className="secret-form-actions">{usesGeneratedKey && <p><LockSimple size={15} weight="bold" /><span>{editing ? "A new public key will replace the current one." : "Only the public key will be available after saving."}</span></p>}<button type="button" className="quiet-button" onClick={closeEditor}>Cancel</button><button className="primary-button" disabled={busy || !name.trim() || !environmentVariable.trim() || (!editing && !usesGeneratedKey && !value.trim())}>{busy ? "Saving..." : usesGeneratedKey ? editing ? "Replace key" : "Generate key" : editing ? "Update secret" : "Save secret"}</button></div>
        </div>
      </form></div>
    </section>}
    {!creating && error && <p className="form-error" role="alert">{error}</p>}
    {!creating && (overview.secrets.length ? <div className="resource-table-wrap"><table className="resource-table secret-table"><thead><tr><th>Secret</th><th>Type</th><th>Environment variable</th><th>Used by</th><th>Updated</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{overview.secrets.map((secret) => { const hookUses = overview.eventTriggers.filter((trigger) => trigger.secretIds.includes(secret.id)).length + overview.previewGroups.flatMap((group) => group.components).filter((component) => component.secretIds?.includes(secret.id)).length + overview.apps.filter((app) => app.hookSecretIds?.includes(secret.id)).length; const sourceUses = overview.apps.filter((app) => app.sourceCredentialId === secret.id).length; const uses = hookUses + sourceUses; return <tr key={secret.id}><td data-label="Secret"><strong>{secret.name}</strong><small>{secret.publicValue ? "Generated deploy key" : "Private value stored"}</small></td><td data-label="Type"><span className="secret-type-label">{secretTypeLabel(secret.type)}</span></td><td data-label="Environment variable"><code>{secret.environmentVariable}</code></td><td data-label="Used by">{uses} source or event rule{uses === 1 ? "" : "s"}</td><td data-label="Updated">{relative(secret.updatedAt)}</td><td className="row-actions">{secret.publicValue && <button aria-label={`View public key for ${secret.name}`} onClick={() => setPublicKeySecret(secret)}><Key size={15} />Public key</button>}<button aria-label={`Edit ${secret.name}`} onClick={() => open(secret)}><PencilSimple size={15} />Edit</button><button className="delete-action" aria-label={`Delete ${secret.name}`} onClick={() => onDelete(secret)}><Trash size={15} />Delete</button></td></tr>; })}</tbody></table></div> : <div><EmptyState title="No secrets" body="Add a token, SSH deploy key, registry password, or general text credential." action={overview.secretStorageConfigured ? { label: "Add secret", onClick: () => open() } : undefined} /></div>)}
    {publicKeySecret && <PublicKeyDialog secret={publicKeySecret} copied={copiedID === publicKeySecret.id} onCopy={() => void copyPublicKey(publicKeySecret)} onClose={() => setPublicKeySecret(null)} />}
  </div>;
}

function ApplicationsPage({ overview, creating, onToggleCreate, onChanged, onDeploy, onDelete, onNavigate }: { overview: Overview; creating: boolean; onToggleCreate: () => void; onChanged: () => Promise<void>; onDeploy: (appID: string) => void; onDelete: (application: AppModel) => void; onNavigate: (view: View) => void }) {
  const [section, setSection] = useState<"applications" | "templates" | "helm" | "groups">("applications");
  const [creatingHelm, setCreatingHelm] = useState(false);
  const [creatingTemplate, setCreatingTemplate] = useState(false);
  const [hookApplication, setHookApplication] = useState<AppModel | null>(null);
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
  const editorTitle = creating ? "New application" : creatingTemplate ? "New template" : creatingHelm ? "New Helm source" : undefined;

  if (hookApplication) return <div className="page-layout applications-page editor-page">
    <PageHeader view="applications" title={`Build hook: ${hookApplication.name}`} showDescription={false} action={{ label: "Back to applications", onClick: () => setHookApplication(null), icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <ApplicationHookEditor application={hookApplication} secrets={overview.secrets} onClose={() => setHookApplication(null)} onSaved={async () => { await onChanged(); setHookApplication(null); }} onDeploy={onDeploy} />
  </div>;

  return <div className="page-layout applications-page">
    <PageHeader view="applications" action={action} title={editorTitle} showDescription={false} />
    {!editorTitle && <nav className="application-sections" aria-label="Application resources">
      <button aria-pressed={section === "applications"} className={section === "applications" ? "active" : ""} onClick={() => selectSection("applications")}>Applications <span>{applications.length}</span></button>
      <button aria-pressed={section === "templates"} className={section === "templates" ? "active" : ""} onClick={() => selectSection("templates")}>Templates <span>{templates.length}</span></button>
      <button aria-pressed={section === "helm"} className={section === "helm" ? "active" : ""} onClick={() => selectSection("helm")}>Helm sources <span>{helmSources.length}</span></button>
      <button aria-pressed={section === "groups"} className={section === "groups" ? "active" : ""} onClick={() => selectSection("groups")}>Preview groups <span>{overview.previewGroups.length}</span></button>
    </nav>}
    {section === "applications" && <div>
      {creating ? <section className="inline-create focused-editor compact-editor" aria-labelledby="new-application-title"><div className="inline-create-body"><h2 className="sr-only" id="new-application-title">New application</h2><AppForm data={overview} onChanged={onChanged} focusName /></div></section> : applications.length ? <div className="resource-table-wrap"><table className="resource-table application-table"><thead><tr><th>Application</th><th>Source</th><th>Target</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{applications.map((application) => { const project = overview.projects.find((item) => item.id === application.projectId)?.name ?? "Unknown project"; const server = overview.servers.find((item) => item.id === application.serverId)?.name ?? "Unknown server"; const source = application.sourceRepo ? application.sourceRepo.split("/").slice(-2).join("/").replace(/\.git$/, "") : "Compose"; const sourceType = application.sourceRepo ? application.buildType === "compose" ? "Git Compose" : "Git Dockerfile" : "Compose file"; return <tr key={application.id}><td data-label="Application"><strong>{application.name}</strong><small>{project}</small></td><td data-label="Source"><strong className="cell-secondary-heading">{sourceType}</strong><small className="truncate-cell" title={application.sourceRepo || "Compose file"}>{application.sourceRepo ? `${source} / ${application.branch}` : source}</small></td><td data-label="Target"><span>{server}</span></td><td className="row-actions"><button className="table-action" onClick={() => setHookApplication(application)}><Lightning size={15} />Build hook</button><button className="table-action" onClick={() => onDeploy(application.id)}>Deploy<ArrowRight size={14} /></button><button className="delete-action" aria-label={`Delete ${application.name}`} onClick={() => onDelete(application)}><Trash size={15} />Delete</button></td></tr>; })}</tbody></table></div> : canAdd ? <EmptyState title="No applications" body="Paste Compose or connect a repository." action={{ label: "Add application", onClick: onToggleCreate }} /> : <PrerequisiteState hasReadyServer={dockerReady} hasProject={hasProject} onNavigate={onNavigate} />}
    </div>}
    {section === "templates" && <div>
      {creatingTemplate ? <section className="inline-create focused-editor compact-editor" aria-labelledby="new-template-title"><div className="inline-create-body"><h2 className="sr-only" id="new-template-title">New template</h2><AppForm data={overview} onChanged={async () => { await onChanged(); setCreatingTemplate(false); }} focusName initialSourceType="repository" template /></div></section> : templates.length ? <div className="resource-table-wrap"><table className="resource-table"><thead><tr><th>Template</th><th>Runtime</th><th>Source</th><th>Default target</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{templates.map((template) => { const source = template.buildType === "helm" ? template.helmChart || "Helm chart" : template.sourceRepo || "Compose"; return <tr key={template.id}><td data-label="Template"><strong>{template.name}</strong><small>{overview.projects.find((project) => project.id === template.projectId)?.name ?? "Unknown project"}</small></td><td data-label="Runtime">{template.buildType === "helm" ? "Helm" : template.buildType === "compose" ? "Compose" : "Dockerfile"}</td><td data-label="Source"><span className="truncate-cell" title={source}>{source}</span><small>{template.sourceRepo ? template.branch : "Saved definition"}</small></td><td data-label="Default target">{overview.servers.find((server) => server.id === template.serverId)?.name ?? "Unknown"}</td><td className="row-actions"><button className="delete-action" aria-label={`Delete ${template.name}`} onClick={() => onDelete(template)}><Trash size={15} />Delete</button></td></tr>; })}</tbody></table></div> : canAddTemplate ? <EmptyState title="No application templates" body="Create a reusable definition for event-driven previews and preview groups." action={{ label: "Add template", onClick: () => setCreatingTemplate(true) }} /> : <TemplatePrerequisiteState hasReadyServer={dockerReady || kubernetesReady} hasProject={hasProject} onNavigate={onNavigate} />}
    </div>}
    {section === "helm" && <div className="helm-sources-panel">
      {creatingHelm ? <section className="inline-create focused-editor compact-editor" aria-labelledby="new-helm-source-title"><div className="inline-create-body"><h2 className="sr-only" id="new-helm-source-title">New Helm source</h2><AppForm data={overview} onChanged={async () => { await onChanged(); setCreatingHelm(false); }} focusName initialSourceType="helm" sourceTypeLocked /></div></section> : helmSources.length ? <div className="resource-table-wrap"><table className="resource-table helm-source-table"><thead><tr><th>Source</th><th>Chart origin</th><th>Version</th><th>Target</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{helmSources.map((source) => { const origin = source.sourceRepo ? "Git repository" : source.helmRepository ? "Helm repository" : source.helmChart?.startsWith("oci://") ? "OCI registry" : "Direct reference"; const repository = source.sourceRepo || source.helmRepository || source.helmChart || "Chart reference"; const version = source.sourceRepo ? source.branch : source.helmVersion || "Chart default"; return <tr key={source.id}><td data-label="Source"><strong>{source.name}</strong><small>{overview.projects.find((project) => project.id === source.projectId)?.name ?? "Unknown project"}</small></td><td data-label="Chart origin"><strong className="cell-secondary-heading">{origin}</strong><small className="truncate-cell" title={repository}>{source.helmChart || "Chart reference"}</small></td><td data-label="Version"><code>{version}</code></td><td data-label="Target">{overview.servers.find((server) => server.id === source.serverId)?.name ?? "Unknown"}</td><td className="row-actions"><button className="table-action" onClick={() => setHookApplication(source)}><Lightning size={15} />Build hook</button><button className="table-action" onClick={() => onDeploy(source.id)}>Deploy<ArrowRight size={14} /></button><button className="delete-action" aria-label={`Delete ${source.name}`} onClick={() => onDelete(source)}><Trash size={15} />Delete</button></td></tr>; })}</tbody></table></div> : canAddHelm ? <EmptyState title="No Helm sources" body="Add a chart source to use for Kubernetes deployments and preview groups." action={{ label: "Add Helm source", onClick: () => setCreatingHelm(true) }} /> : <HelmPrerequisiteState hasKubernetes={kubernetesReady} hasProject={hasProject} onNavigate={onNavigate} />}
    </div>}
    {section === "groups" && <div><PreviewGroupsArea overview={overview} onChanged={onChanged} /></div>}
  </div>;
}

function ApplicationHookEditor({ application, secrets, onClose, onSaved, onDeploy }: { application: AppModel; secrets: Secret[]; onClose: () => void; onSaved: () => Promise<void>; onDeploy: (appID: string) => void }) {
  const [preDeployHook, setPreDeployHook] = useState(application.preDeployHook ?? "");
  const [postDeployHook, setPostDeployHook] = useState(application.postDeployHook ?? "");
  const [secretIds, setSecretIds] = useState<string[]>(application.hookSecretIds ?? []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(deployAfter: boolean) {
    setBusy(true);
    setError("");
    try {
      await api.updateAppHooks(application.id, { preDeployHook, postDeployHook, secretIds });
      await onSaved();
      if (deployAfter) onDeploy(application.id);
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  }

  return <section className="event-hook-editor application-hook-editor" aria-labelledby="application-hook-editor-title">
    <header><div><h2 id="application-hook-editor-title">Build and deployment hooks</h2><p>Run a build before manually deploying {application.name}, then optionally run a script after it becomes ready.</p></div></header>
    <form onSubmit={(event) => { event.preventDefault(); void save(false); }} aria-busy={busy}>
      <HookFields preDeployHook={preDeployHook} postDeployHook={postDeployHook} onPreDeployHook={setPreDeployHook} onPostDeployHook={setPostDeployHook} />
      <HookCredentialBindings secrets={secrets} selected={secretIds} onChange={setSecretIds} />
      {application.sourceAuthType === "ssh_key" && application.sourceCredentialId && <div className="manifest-summary application-hook-source-note"><Key size={17} /><p>The SSH key selected for this application source is also available to Git commands in the build hook. It does not need to be attached again.</p></div>}
      <details className="event-hook-context"><summary>Available deployment variables</summary><p>Available as environment variables in both hooks.</p><div>{["DISPATCH_PREVIEW_TAG", "DISPATCH_APP_ID", "DISPATCH_APP_NAME", "DISPATCH_REVISION", "DISPATCH_SERVER_NAME", "DISPATCH_VALUES_FILE", "DISPATCH_OUTPUT_FILE"].map((name) => <code key={name}>{name}</code>)}</div></details>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="builder-actions"><button type="button" className="quiet-button" onClick={onClose}>Cancel</button><button type="submit" className="quiet-button" disabled={busy}>{busy ? "Saving..." : "Save hook"}</button><button type="button" className="primary-button" disabled={busy || !preDeployHook.trim()} onClick={() => void save(true)}>{busy ? "Saving..." : "Save and deploy"}</button></div>
    </form>
  </section>;
}

function ResourceSummary({ items }: { items: Array<{ label: string; value: number }> }) {
  return <dl className="resource-summary" aria-label="Resource totals">{items.map((item) => <div key={item.label}><dd>{item.value}</dd><dt>{item.label}</dt></div>)}</dl>;
}

function StatusLabel({ state }: { state: string }) {
  return <span className={`status-label ${state}`}><i />{state}</span>;
}

function EmptyState({ title, body, action }: { title: string; body: string; action?: { label: string; onClick: () => void } }) {
  return <section className="empty-state"><Mark /><div><h2>{title}</h2><p>{body}</p></div>{action && <button className="primary-button" onClick={action.onClick}><Plus size={15} weight="bold" />{action.label}</button>}</section>;
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

function PublicKeyDialog({ secret, copied, onCopy, onClose }: { secret: Secret; copied: boolean; onCopy: () => void; onClose: () => void }) {
  const dialogRef = useDialogFocus(onClose);
  return <div className="dialog-layer drawer-layer">
    <section ref={dialogRef} className="resource-dialog resource-drawer public-key-dialog" role="dialog" aria-modal="true" aria-labelledby="public-key-title" aria-describedby="public-key-description">
      <header><div><h2 id="public-key-title">Public key</h2><p id="public-key-description">Add this key to GitHub or another Git host.</p></div><button aria-label="Close public key" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        <div className="public-key-summary"><Key size={20} /><div><strong>{secret.name}</strong><span>Ed25519 deploy key</span></div></div>
        <label className="public-key-value"><span>Public key</span><textarea readOnly value={secret.publicValue ?? ""} aria-label={`Public key for ${secret.name}`} /></label>
        <p className="key-privacy-note"><LockSimple size={16} />The private key is encrypted and cannot be viewed or downloaded.</p>
        <div className="dialog-actions"><button className="primary-button" onClick={onCopy}>{copied ? <Check size={15} weight="bold" /> : <Copy size={15} />}{copied ? "Copied" : "Copy public key"}</button></div>
      </div>
    </section>
  </div>;
}

function ResourceDialog({ kind, overview, project, server, deployAppID, onClose, onChanged, onDeployed }: { kind: Exclude<Dialog, null>; overview: Overview; project?: Project; server?: Server; deployAppID?: string; onClose: () => void; onChanged: () => Promise<void>; onDeployed: (id: string) => Promise<void> }) {
  const dialogRef = useDialogFocus(onClose);
  const copy = {
    server: server ? { title: "Edit server", description: "Update this server's connection settings." } : { title: "Add server", description: "Register a deployment target or event relay." },
    repair: { title: "Repair OpenShift connection", description: "Use a fresh temporary login to rebuild the managed connection." },
    project: project ? { title: "Edit project", description: "Update this project's name or description." } : { title: "Add project", description: "Create an independent group for related applications." },
    deploy: { title: "Deploy revision", description: "Choose an application and source revision." },
  }[kind];
  return <div className="dialog-layer drawer-layer">
    <section ref={dialogRef} className={`resource-dialog resource-drawer ${kind}-drawer`} role="dialog" aria-modal="true" aria-labelledby="dialog-title" aria-describedby="dialog-description">
      <header><div><h2 id="dialog-title">{copy.title}</h2><p id="dialog-description">{copy.description}</p></div><button aria-label="Close dialog" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        {kind === "server" && <ServerForm onChanged={onChanged} onCancel={onClose} server={server} />}
        {kind === "repair" && <ServerForm onChanged={onChanged} onCancel={onClose} server={server} repairing />}
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
  const dependencies = target.kind === "application" ? 0
    : target.kind === "secret"
      ? overview.apps.filter((app) => app.sourceCredentialId === target.item.id || app.hookSecretIds?.includes(target.item.id)).length + overview.eventTriggers.filter((trigger) => trigger.secretIds.includes(target.item.id)).length + overview.previewGroups.flatMap((group) => group.components).filter((component) => component.secretIds?.includes(target.item.id)).length
      : overview.apps.filter((app) => target.kind === "server" ? app.serverId === target.item.id : app.projectId === target.item.id).length;
  const resource = target.kind === "server" ? "server" : target.kind === "project" ? "project" : target.kind === "secret" ? "secret" : target.item.template ? "template" : "application";

  async function remove() {
    setBusy(true);
    setError("");
    try {
      if (target.kind === "server") await api.deleteServer(target.item.id);
      else if (target.kind === "project") await api.deleteProject(target.item.id);
      else if (target.kind === "secret") await api.deleteSecret(target.item.id);
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
      <header><div><h2 id="delete-dialog-title">Delete {resource}</h2><p>{target.kind === "application" && !target.item.template ? "Deployed resources and history are removed first." : "This action cannot be undone."}</p></div><button aria-label="Close dialog" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        <p className="confirm-copy" id="delete-dialog-description">Delete <strong>{target.item.name}</strong> from Dispatch?</p>
        {dependencies > 0 && <div className="dependency-warning" role="status"><strong>Cannot delete this {resource}</strong><p>{target.kind === "secret" ? `${dependencies} application source or event rule${dependencies === 1 ? " uses" : "s use"} it. Detach ${dependencies === 1 ? "that dependency" : "those dependencies"} first.` : `${dependencies} ${dependencies === 1 ? "application uses" : "applications use"} it. Remove ${dependencies === 1 ? "that application" : "those applications"} first.`}</p></div>}
        {error && <p className="form-error" role="alert">{error}</p>}
        <div className="dialog-actions confirm-actions"><button className="quiet-button" onClick={onClose}>Cancel</button><button className="danger-button" disabled={busy || dependencies > 0} onClick={() => void remove()}>{busy ? "Deleting..." : `Delete ${resource}`}</button></div>
      </div>
    </section>
  </div>;
}

function DeploymentGroup({ title, count, deployments, selectedID, onSelect }: { title: string; count: number; deployments: Deployment[]; selectedID?: string; onSelect: (id: string) => void }) {
  return <div className="deployment-group"><div className="group-heading"><h2>{title}</h2><span>{count}</span></div><div className="strips">{deployments.map((deployment) => <DeploymentStrip key={deployment.id} deployment={deployment} selected={selectedID === deployment.id} onSelect={() => onSelect(deployment.id)} />)}</div></div>;
}

function DeploymentStrip({ deployment, selected, onSelect }: { deployment: Deployment; selected: boolean; onSelect: () => void }) {
  const tone = statusTone(deployment.state), current = stageIndex(deployment.state), active = !["succeeded", "failed", "cancelled"].includes(deployment.state);
  return <button className={`dispatch-strip ${tone} ${selected ? "selected" : ""} ${!active && !selected ? "compact" : ""}`} onClick={onSelect} aria-pressed={selected} aria-label={`${deployment.app?.name ?? "Unknown application"}, ${deployment.state}, commit ${short(deployment.commitSha)}, ${deployment.server?.name ?? "no server"}, ${relative(deployment.createdAt)}`}>
    <span className="strip-status" aria-hidden="true">{tone === "success" ? <CheckCircle size={19} weight="fill" /> : tone === "danger" ? <WarningCircle size={19} weight="fill" /> : <CircleNotch size={19} weight="bold" />}</span>
    <span className="strip-identity"><strong>{deployment.app?.name ?? "Unknown app"}</strong><span><code>{short(deployment.commitSha)}</code><i />{deployment.server?.name ?? "No server"}</span></span>
    {(active || selected) && <span className="stage-track" aria-hidden="true">{stages.map((stage, index) => <span className={`stage ${index < current || (index === current && deployment.state === "succeeded") ? "complete" : ""} ${index === current ? "current" : ""}`} key={stage}><small>{stage}</small><i /></span>)}</span>}
    <span className="strip-result"><small>{stateStage[deployment.state]}</small><strong>{deployment.state}</strong><time dateTime={deployment.createdAt}>{relative(deployment.createdAt)}</time></span>
  </button>;
}

function Evidence({ deployment, logs, onCancel, onClose }: { deployment: Deployment; logs: DeploymentLog[]; onCancel: () => void; onClose: () => void }) {
  const active = !["succeeded", "failed", "cancelled"].includes(deployment.state);
  const [copied, setCopied] = useState("");
  async function copy(kind: string, value: string) {
    await navigator.clipboard.writeText(value);
    setCopied(kind);
    window.setTimeout(() => setCopied((current) => current === kind ? "" : current), 1600);
  }
  return <>
    <div className="evidence-head"><div><span className={`selection-dot ${statusTone(deployment.state)}`} /><div><h2>{deployment.app?.name}</h2><small><code>{short(deployment.commitSha)}</code> on {deployment.server?.name}</small></div></div><div className="evidence-head-actions"><span className={`stamp ${statusTone(deployment.state)}`}>{deployment.state}</span><button type="button" aria-label="Close deployment details" onClick={onClose}><X size={17} /></button></div></div>
    <div className="log-block"><div className="section-label"><h3>Deployment log</h3><span className={active ? "active" : ""}>{active ? "Streaming" : "Complete"}</span></div><div className="terminal" role="log" aria-live="polite">{logs.length ? logs.map((entry) => <div key={entry.id} className={entry.level}><time>{new Date(entry.createdAt).toLocaleTimeString([], { hour12: false })}</time><span>{entry.message}</span></div>) : <p>No log entries.</p>}</div></div>
    <details className="deployment-details"><summary>Deployment details</summary><dl className="evidence-grid">
      <div><dt>Commit</dt><dd><code>{short(deployment.commitSha, 18)}</code><button type="button" aria-label="Copy commit" onClick={() => void copy("commit", deployment.commitSha)}>{copied === "commit" ? <Check size={13} /> : <Copy size={13} />}</button></dd></div><div><dt>Created</dt><dd>{relative(deployment.createdAt)}</dd></div>
      <div className="wide"><dt>Spec digest</dt><dd><code title={deployment.specDigest}>{short(deployment.specDigest, 28)}</code><button type="button" aria-label="Copy spec digest" onClick={() => void copy("digest", deployment.specDigest)}>{copied === "digest" ? <Check size={13} /> : <Copy size={13} />}</button></dd></div>
      <div><dt>Build</dt><dd>{deployment.app?.buildType}</dd></div><div><dt>Branch</dt><dd>{deployment.app?.branch}</dd></div>
      <div><dt>Server</dt><dd>{deployment.server?.name}</dd></div><div><dt>Runtime</dt><dd>{deployment.server?.runtime}</dd></div>
    </dl></details>
    {deployment.outputs && Object.keys(deployment.outputs).length > 0 && <details className="deployment-details deployment-outputs" open><summary>Published outputs</summary><dl className="evidence-grid">{Object.entries(deployment.outputs).map(([key, value]) => <div className="wide" key={key}><dt>{key}</dt><dd><code title={value}>{value}</code><button type="button" aria-label={`Copy ${key}`} onClick={() => void copy(`output-${key}`, value)}>{copied === `output-${key}` ? <Check size={13} /> : <Copy size={13} />}</button></dd></div>)}</dl></details>}
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
  const sourceLabel = selectedApp?.buildType === "helm" ? "Saved Helm chart" : "Saved Compose file";
  const sourceHelp = selectedApp?.buildType === "helm" ? "Installs the configured chart and values." : "Applies the stored Compose definition.";
  return <form className="resource-form" onSubmit={submit} aria-busy={busy}><label><span>Application</span><select value={appID} onChange={(event) => setAppID(event.target.value)}>{apps.map((app) => <option value={app.id} key={app.id}>{app.name}</option>)}</select><small>Definition to deploy.</small></label>{fixedSource ? <label><span>Source</span><input value={sourceLabel} disabled /><small>{sourceHelp}</small></label> : <label><span>Source revision</span><input value={commit} onChange={(event) => setCommit(event.target.value)} required spellCheck={false} /><small>Branch, tag, or commit.</small></label>}{error && <p className="form-error" role="alert">{error}</p>}<div className="dialog-actions"><button type="button" className="quiet-button" onClick={onCancel}>Cancel</button><button className="primary-button" disabled={busy || !appID}>{busy ? "Starting deployment..." : "Deploy"}</button></div></form>;
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

  return <main className="auth-screen"><section className="auth-card" aria-labelledby="auth-title"><div className="auth-brand"><Mark /><strong>Dispatch</strong></div>{setupRequired === null ? error ? <div className="auth-connection-error"><h1 id="auth-title">Controller unavailable</h1><p>{error}</p><button className="quiet-button" onClick={() => void loadStatus()}>Retry connection</button></div> : <div className="auth-loading" aria-label="Connecting"><span /><span /><span /></div> : <><header><h1 id="auth-title">{setupRequired ? "Create administrator" : "Sign in"}</h1><p>{setupRequired ? "Create the account used to manage this controller." : "Sign in to manage applications and deployments."}</p></header><form onSubmit={submit} aria-busy={busy}><label><span>Username</span><input value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" autoFocus required minLength={3} maxLength={64} disabled={busy} /></label><label><span>Password</span><input type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete={setupRequired ? "new-password" : "current-password"} required minLength={setupRequired ? 12 : undefined} disabled={busy} />{setupRequired && <small>Use at least 12 characters.</small>}</label>{setupRequired && <label><span>Confirm password</span><input type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="new-password" required minLength={12} disabled={busy} /></label>}{error && <p className="auth-error" role="alert">{error}</p>}<button className="primary-button" disabled={busy}>{busy ? "Please wait..." : setupRequired ? "Create account" : "Sign in"}</button></form></>}</section></main>;
}

function PageLoading() { return <div className="page-layout"><div className="page-loading"><span /><span /><span /></div></div>; }
function Mark() { return <svg viewBox="0 0 36 36" aria-hidden="true"><path d="M5 8.5 18 2l13 6.5v18L18 34 5 26.5Z" fill="none" stroke="currentColor" strokeWidth="2"/><path d="m5 8.5 13 7 13-7M18 15.5V34" fill="none" stroke="currentColor" strokeWidth="2"/><path d="m10 11 8-4 8 4-8 4Z" fill="currentColor"/></svg>; }
