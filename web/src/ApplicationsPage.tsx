import { Fragment, ReactNode, useCallback, useEffect, useState } from "react";
import {
  AppWindow,
  ArrowClockwise,
  ArrowLeft,
  ArrowSquareOut,
  Check,
  CheckCircle,
  CircleNotch,
  Copy,
  CaretDown,
  CaretRight,
  DotsThreeVertical,
  GitBranch,
  Graph,
  Key,
  Lightning,
  PencilSimple,
  Plus,
  PlugsConnected,
  RocketLaunch,
  SlidersHorizontal,
  FileCode,
  Trash,
  WarningCircle,
  X,
} from "@phosphor-icons/react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import {
  api,
  App as AppModel,
  ConfigSource,
  HelmChartInspection,
  HelmValue,
  Overview,
  PreviewGroup,
  Secret,
  WorkflowResource,
} from "./api";
import { HookCredentialBindings, HookFields } from "./HookEditorFields";
import { HelmValuesEditor, helmValueOverrides, mergeHelmValues } from "./HelmValuesEditor";
import { AppForm } from "./Onboarding";
import { PageHeader } from "./PageHeader";
import { PreviewGroupsArea } from "./PreviewGroups";
import { relative } from "./presentation";
import { ApplicationSection, View } from "./routes";
import { useDialogFocus } from "./useDialogFocus";
import { canManageAnyProject, canManageProject } from "./permissions";
import { WorkflowConfigSourceForm } from "./workflows/ConfigSourceForm";
import { WorkflowResourceDialog } from "./workflows/ResourceDialog";
import { WorkflowTopologyPage } from "./workflows/TopologyPage";
import { workflowResourceStatus, workflowResourceStatusLabel } from "./workflows/status";

export function ApplicationsPage({ overview, section, applicationID, creating, onToggleCreate, onChanged, onDeploy, onDelete, onDeleteGroup, onNavigate, onOpenTopology = () => {}, onOpenDeploymentManifests = () => {}, onCloseTopology = () => {} }: { overview: Overview; section: ApplicationSection; applicationID?: string; creating: boolean; onToggleCreate: () => void; onChanged: () => Promise<void>; onDeploy: (appID: string) => void; onDelete: (application: AppModel) => void; onDeleteGroup: (group: PreviewGroup) => void; onNavigate: (view: View) => void; onOpenTopology?: (resource: WorkflowResource) => void; onOpenDeploymentManifests?: (deploymentID: string) => void; onCloseTopology?: () => void }) {
  const [creatingHelm, setCreatingHelm] = useState(false);
  const [creatingTemplate, setCreatingTemplate] = useState(false);
  const [configSourceEdit, setConfigSourceEdit] = useState<ConfigSource | "new" | null>(null);
  const [workflowDetail, setWorkflowDetail] = useState<WorkflowResource | null>(null);
  const [hookApplication, setHookApplication] = useState<AppModel | null>(null);
  const [helmValuesApplication, setHelmValuesApplication] = useState<AppModel | null>(null);
  const [previewGroupEditing, setPreviewGroupEditing] = useState(false);
  const [previewGroupEdit, setPreviewGroupEdit] = useState<PreviewGroup | "new" | null>(null);
  const handlePreviewGroupEditing = useCallback((editing: boolean) => { setPreviewGroupEditing(editing); if (!editing) setPreviewGroupEdit(null); }, []);
  const helmSources = overview.apps.filter((application) => !application.template && application.buildType === "helm");
  const dockerReady = overview.servers.some((server) => server.state === "ready" && server.runtime === "docker");
  const kubernetesReady = overview.servers.some((server) => server.state === "ready" && (server.runtime === "kubernetes" || server.runtime === "openshift"));
  const hasProject = overview.projects.length > 0;
  const canConfigure = canManageAnyProject(overview, "project.configure");
  const canAddApplication = dockerReady && hasProject && canConfigure;
  const canAddTemplate = (dockerReady || kubernetesReady) && hasProject && canConfigure;
  const canAddHelm = kubernetesReady && hasProject && canConfigure;
  const canAddGroup = overview.identity?.systemRole === "owner" && helmSources.length > 0 && overview.githubApps.some((connection) => connection.installationId && connection.state !== "needs_installation");
  const repositoryCredentials = overview.secrets.some((secret) => secret.type === "ssh_private_key" || secret.type === "github_token" || secret.type === "api_token");
  const canAddConfig = overview.identity?.systemRole === "owner" && canConfigure && hasProject && (repositoryCredentials || overview.githubApps.some((connection) => connection.state === "ready"));
  const editorTitle = creating ? "New application" : creatingTemplate ? "New template" : creatingHelm ? "New Helm source" : configSourceEdit ? configSourceEdit === "new" ? "Import configuration" : "Edit configuration" : undefined;
  const action = creating
    ? { label: "Cancel", onClick: onToggleCreate, icon: <X size={16} weight="bold" />, tone: "quiet" as const }
    : creatingTemplate
      ? { label: "Cancel", onClick: () => setCreatingTemplate(false), icon: <X size={16} weight="bold" />, tone: "quiet" as const }
      : creatingHelm
        ? { label: "Cancel", onClick: () => setCreatingHelm(false), icon: <X size={16} weight="bold" />, tone: "quiet" as const }
        : configSourceEdit
          ? { label: "Cancel", onClick: () => setConfigSourceEdit(null), icon: <X size={16} weight="bold" />, tone: "quiet" as const }
        : undefined;

  useEffect(() => {
    if (applicationID || editorTitle || previewGroupEditing || section === "applications") return;
    const frame = window.requestAnimationFrame(() => document.getElementById("application-resources")?.scrollIntoView({ block: "start" }));
    return () => window.cancelAnimationFrame(frame);
  }, [applicationID, editorTitle, previewGroupEditing, section]);

  if (applicationID) {
    const resource = (overview.workflowResources ?? []).find((item) => item.id === applicationID);
    if (!resource) return <div className="page-layout topology-page"><PageHeader view="applications" title="Application unavailable" action={{ label: "Back", onClick: onCloseTopology, icon: <ArrowLeft size={16} />, tone: "quiet" }} /><p className="topology-error">This application is no longer available.</p></div>;
    const source = (overview.configSources ?? []).find((item) => item.id === resource.configSourceId);
    return <WorkflowTopologyPage resource={resource} source={source} canRun={Boolean(source && canManageProject(overview, source.projectId, "deployment.run"))} onBack={onCloseTopology} onRun={async () => { await api.runWorkflowResource(resource.id); await onChanged(); }} />;
  }

  if (hookApplication) return <div className="page-layout applications-page editor-page">
    <PageHeader view="applications" title={`Build hook: ${hookApplication.name}`} action={{ label: "Back to applications", onClick: () => setHookApplication(null), icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <ApplicationHookEditor application={hookApplication} secrets={overview.secrets} onClose={() => setHookApplication(null)} onSaved={async () => { await onChanged(); setHookApplication(null); }} onDeploy={onDeploy} />
  </div>;

  if (helmValuesApplication) return <div className="page-layout applications-page editor-page">
    <PageHeader view="applications" title={`Helm values: ${helmValuesApplication.name}`} action={{ label: "Back to applications", onClick: () => setHelmValuesApplication(null), icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <HelmSourceValues application={helmValuesApplication} canEdit={canManageProject(overview, helmValuesApplication.projectId, "project.configure")} onChanged={onChanged} />
  </div>;

  return <div className="page-layout applications-page">
    <PageHeader view="applications" action={previewGroupEditing ? undefined : action} trailing={!editorTitle && !previewGroupEditing && (canAddApplication || canAddHelm || canAddTemplate || canAddGroup || canAddConfig) ? <ApplicationAddMenu canAddApplication={canAddApplication} canAddHelm={canAddHelm} canAddTemplate={canAddTemplate} canAddGroup={canAddGroup} canAddConfig={canAddConfig} onApplication={onToggleCreate} onHelm={() => setCreatingHelm(true)} onTemplate={() => setCreatingTemplate(true)} onGroup={() => setPreviewGroupEdit("new")} onConfig={() => setConfigSourceEdit("new")} /> : undefined} title={editorTitle ?? (previewGroupEditing ? "Preview group" : undefined)} />
    {creating && <section className="inline-create focused-editor compact-editor" aria-labelledby="new-application-title"><div className="inline-create-body"><h2 className="sr-only" id="new-application-title">New application</h2><AppForm data={overview} onChanged={onChanged} focusName /></div></section>}
    {creatingTemplate && <section className="inline-create focused-editor compact-editor" aria-labelledby="new-template-title"><div className="inline-create-body"><h2 className="sr-only" id="new-template-title">New template</h2><AppForm data={overview} onChanged={async () => { await onChanged(); setCreatingTemplate(false); }} focusName initialSourceType="repository" template /></div></section>}
    {creatingHelm && <section className="inline-create focused-editor compact-editor" aria-labelledby="new-helm-source-title"><div className="inline-create-body"><h2 className="sr-only" id="new-helm-source-title">New Helm source</h2><AppForm data={overview} onChanged={async () => { await onChanged(); setCreatingHelm(false); }} focusName initialSourceType="helm" sourceTypeLocked /></div></section>}
    {configSourceEdit && <section className="inline-create focused-editor compact-editor" aria-labelledby="configuration-source-title"><div className="inline-create-body"><h2 className="sr-only" id="configuration-source-title">Repository configuration</h2><WorkflowConfigSourceForm overview={overview} source={configSourceEdit === "new" ? undefined : configSourceEdit} onCancel={() => setConfigSourceEdit(null)} onSaved={async () => { await onChanged(); setConfigSourceEdit(null); }} /></div></section>}
    {!editorTitle && !previewGroupEditing && <ApplicationInventory overview={overview} onDeploy={onDeploy} onHooks={setHookApplication} onValues={setHelmValuesApplication} onEditGroup={setPreviewGroupEdit} onEditConfig={setConfigSourceEdit} onOpenWorkflow={setWorkflowDetail} onOpenTopology={onOpenTopology} onOpenDeploymentManifests={onOpenDeploymentManifests} onDelete={onDelete} onDeleteGroup={onDeleteGroup} onChanged={onChanged} hasReadyServer={dockerReady || kubernetesReady} hasProject={hasProject} onNavigate={onNavigate} />}
    {!editorTitle && <div className="preview-group-editor-host"><PreviewGroupsArea overview={overview} onChanged={onChanged} embedded hideInventory requestedEdit={previewGroupEdit} onEditingChange={handlePreviewGroupEditing} /></div>}
    {workflowDetail && <WorkflowResourceDialog resource={workflowDetail} overview={overview} onClose={() => setWorkflowDetail(null)} onChanged={onChanged} onOpenDeploymentManifests={onOpenDeploymentManifests} />}
  </div>;
}

function repositoryLabel(repository: string) {
  return repository.replace(/^[^:]+@[^:]+:/, "").replace(/^https?:\/\/[^/]+\//, "").replace(/\.git$/, "").split("/").filter(Boolean).slice(-2).join("/");
}

function configSourceMethod(source: ConfigSource) {
  if (source.syncMode === "webhook_poll") return "Webhook + poll";
  if (source.syncMode === "webhook") return "Webhook";
  if (source.pollIntervalSeconds === 60) return "Poll every minute";
  return `Poll every ${source.pollIntervalSeconds}s`;
}

function workflowResourceCountLabel(resources: WorkflowResource[]) {
  if (!resources.length) return "No applications found";
  const applications = resources.filter((resource) => resource.kind === "Application").length;
  const pipelines = resources.length - applications;
  return [
    applications ? `${applications} application${applications === 1 ? "" : "s"}` : "",
    pipelines ? `${pipelines} pipeline${pipelines === 1 ? "" : "s"}` : "",
  ].filter(Boolean).join(", ");
}

function workflowResourceSummary(source: ConfigSource, resources: WorkflowResource[], overview: Overview) {
  const synced = source.lastSyncedAt ? `Synced ${relative(source.lastSyncedAt)}` : "Configuration not synced";
  if (!resources.length) return { state: source.state, label: source.state, detail: synced };
  const statuses = resources.map((resource) => {
    const latest = (overview.workflowRevisions ?? []).find((revision) => revision.resourceId === resource.id);
    return workflowResourceStatus(resource, latest?.state);
  });
  const failed = statuses.filter((status) => status === "failed" || status === "degraded").length;
  const pending = statuses.filter((status) => status === "pending_activation").length;
  const paused = statuses.filter((status) => status === "paused").length;
  const ready = statuses.filter((status) => status === "succeeded" || status === "ready").length;
  if (failed) return { state: "failed", label: `${failed} failed`, detail: synced };
  if (pending) return { state: "pending_activation", label: `${pending} pending`, detail: synced };
  if (ready === resources.length) return { state: "succeeded", label: `${ready}/${resources.length} ready`, detail: synced };
  if (paused === resources.length) return { state: "paused", label: `${paused} paused`, detail: synced };
  const active = statuses.find((status) => !["succeeded", "ready", "paused"].includes(status));
  return { state: active ?? "paused", label: active ? workflowResourceStatusLabel(active) : `${ready}/${resources.length} ready`, detail: synced };
}

function ApplicationAddMenu({ canAddApplication, canAddHelm, canAddTemplate, canAddGroup, canAddConfig, onApplication, onHelm, onTemplate, onGroup, onConfig }: { canAddApplication: boolean; canAddHelm: boolean; canAddTemplate: boolean; canAddGroup: boolean; canAddConfig: boolean; onApplication: () => void; onHelm: () => void; onTemplate: () => void; onGroup: () => void; onConfig: () => void }) {
  return <DropdownMenu.Root>
    <DropdownMenu.Trigger asChild><button className="primary-button add-resource-menu" type="button"><Plus size={16} weight="bold" />Add</button></DropdownMenu.Trigger>
    <DropdownMenu.Portal>
      <DropdownMenu.Content className="action-menu-list add-resource-options" align="end" sideOffset={6} collisionPadding={12}>
        <MenuAction icon={<AppWindow size={16} />} label="Application" disabled={!canAddApplication} onClick={onApplication} />
        <MenuAction icon={<SlidersHorizontal size={16} />} label="Helm source" disabled={!canAddHelm} onClick={onHelm} />
        <MenuAction icon={<Copy size={16} />} label="Template" disabled={!canAddTemplate} onClick={onTemplate} />
        <MenuAction icon={<PlugsConnected size={16} />} label="Preview group" disabled={!canAddGroup} onClick={onGroup} />
        <MenuAction icon={<GitBranch size={16} />} label="Repository configuration" disabled={!canAddConfig} onClick={onConfig} />
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  </DropdownMenu.Root>;
}

function MenuAction({ icon, label, danger = false, disabled = false, onClick }: { icon: ReactNode; label: string; danger?: boolean; disabled?: boolean; onClick: () => void }) {
  return <DropdownMenu.Item className={`action-menu-item${danger ? " danger" : ""}`} disabled={disabled} onSelect={onClick}>{icon}<span><strong>{label}</strong></span></DropdownMenu.Item>;
}



function ApplicationInventory({ overview, onDeploy, onHooks, onValues, onEditGroup, onEditConfig, onOpenWorkflow, onOpenTopology, onOpenDeploymentManifests, onDelete, onDeleteGroup, onChanged, hasReadyServer, hasProject, onNavigate }: { overview: Overview; onDeploy: (id: string) => void; onHooks: (application: AppModel) => void; onValues: (application: AppModel) => void; onEditGroup: (group: PreviewGroup) => void; onEditConfig: (source: ConfigSource) => void; onOpenWorkflow: (resource: WorkflowResource) => void; onOpenTopology: (resource: WorkflowResource) => void; onOpenDeploymentManifests: (deploymentID: string) => void; onDelete: (application: AppModel) => void; onDeleteGroup: (group: PreviewGroup) => void; onChanged: () => Promise<void>; hasReadyServer: boolean; hasProject: boolean; onNavigate: (view: View) => void }) {
  const [busyID, setBusyID] = useState("");
  const [error, setError] = useState("");
  const [deleteSource, setDeleteSource] = useState<ConfigSource | null>(null);
  const [expandedSourceIDs, setExpandedSourceIDs] = useState<Set<string>>(() => new Set());
  const applications = overview.apps.filter((application) => !application.generated);
  const configSources = overview.configSources ?? [];
  const workflowResources = overview.workflowResources ?? [];
  const sourceIDs = new Set(configSources.map((source) => source.id));
  const orphanWorkflowResources = workflowResources.filter((resource) => !sourceIDs.has(resource.configSourceId));
  const resourceCount = applications.length + overview.previewGroups.length + configSources.length + orphanWorkflowResources.length;

  function toggleSource(sourceID: string) {
    setExpandedSourceIDs((current) => {
      const next = new Set(current);
      if (next.has(sourceID)) next.delete(sourceID);
      else next.add(sourceID);
      return next;
    });
  }

  async function sourceAction(source: ConfigSource, action: "sync" | "delete") {
    setBusyID(source.id);
    setError("");
    try {
      if (action === "sync") await api.syncConfigSource(source.id);
      else await api.deleteConfigSource(source.id);
      await onChanged();
      if (action === "delete") setDeleteSource(null);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function workflowAction(resource: WorkflowResource, action: "activate" | "pause" | "run") {
    setBusyID(resource.id);
    setError("");
    try {
      if (action === "activate") await api.activateWorkflowResource(resource.id);
      else if (action === "pause") await api.deactivateWorkflowResource(resource.id);
      else await api.runWorkflowResource(resource.id);
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  function workflowResourceRow(resource: WorkflowResource, source?: ConfigSource, nested = false) {
    const latest = (overview.workflowRevisions ?? []).find((item) => item.resourceId === resource.id);
    const target = resource.targetRefs?.join(", ") || (resource.kind === "Pipeline" ? "Stage check" : "No stage");
    const status = workflowResourceStatus(resource, latest?.state);
    const latestDeploymentID = latestWorkflowDeploymentID(overview, resource.id);
    const canConfigureResource = Boolean(source && canManageProject(overview, source.projectId, "project.configure"));
    const canRunResource = Boolean(source && canManageProject(overview, source.projectId, "deployment.run"));
    return <tr className={`inspectable-resource-row${nested ? " configuration-resource-row" : ""}`} key={`workflow-${resource.id}`} onMouseDown={(event) => {
      if (event.detail > 1) event.preventDefault();
    }} onDoubleClick={(event) => {
      if (event.target instanceof Element && event.target.closest("button, a, input, select, textarea, [role='menuitem']")) return;
      onOpenWorkflow(resource);
    }}><td data-label="Name"><button type="button" className="resource-name-button" aria-label={`Open ${resource.name}`} onClick={() => onOpenWorkflow(resource)}><strong>{resource.name}</strong><small>{resource.sourceCount} source{resource.sourceCount === 1 ? "" : "s"}, {resource.jobCount} job{resource.jobCount === 1 ? "" : "s"}</small></button></td><td data-label="Type"><strong className="cell-secondary-heading">{resource.kind}</strong><small>{source ? "Managed application" : "Imported"}</small></td><td data-label="Source"><span className="truncate-cell" title={resource.path}>{resource.path}</span><small className="truncate-cell" title={resource.configSha}>Config {resource.configSha.slice(0, 8)}</small></td><td data-label="Target"><span className="truncate-cell" title={target}>{target}</span></td><td data-label="Status"><span className={`status-label ${status}`}><i />{workflowResourceStatusLabel(status)}</span></td><td className="row-actions"><RowActionMenu name={resource.name}>
      <DropdownMenu.Label className="row-action-group-label">{resource.kind}</DropdownMenu.Label>
      {canConfigureResource && !resource.active && <MenuAction icon={<Check size={16} />} label="Activate" disabled={busyID === resource.id} onClick={() => void workflowAction(resource, "activate")} />}
      {canRunResource && resource.active && resource.kind === "Application" && <MenuAction icon={<RocketLaunch size={16} />} label="Run now" disabled={busyID === resource.id} onClick={() => void workflowAction(resource, "run")} />}
      <MenuAction icon={<Graph size={16} />} label="Topology" onClick={() => onOpenTopology(resource)} />
      {latestDeploymentID && <MenuAction icon={<FileCode size={16} />} label="Manifests" onClick={() => onOpenDeploymentManifests(latestDeploymentID)} />}
      {canConfigureResource && resource.active && <MenuAction icon={<X size={16} />} label="Pause" disabled={busyID === resource.id} onClick={() => void workflowAction(resource, "pause")} />}
    </RowActionMenu></td></tr>;
  }

  if (!resourceCount) return <section id="application-resources">{hasReadyServer && hasProject ? <ApplicationCollectionEmpty>No applications.</ApplicationCollectionEmpty> : <PrerequisiteState hasReadyServer={hasReadyServer} hasProject={hasProject} onNavigate={onNavigate} />}</section>;
  return <section id="application-resources" aria-labelledby="application-resources-title"><h2 className="sr-only" id="application-resources-title">Configured resources</h2>{error && <p className="form-error application-inventory-error" role="alert">{error}</p>}<div className="resource-table-wrap application-inventory-table-wrap"><table className="resource-table application-inventory-table"><thead><tr><th>Name</th><th>Type</th><th>Source</th><th>Target</th><th>Status</th><th className="actions-head"><span className="sr-only">Options</span></th></tr></thead><tbody>
    {configSources.map((source) => {
      const project = overview.projects.find((item) => item.id === source.projectId)?.name ?? "Unknown project";
      const resources = workflowResources.filter((resource) => resource.configSourceId === source.id);
      const expanded = expandedSourceIDs.has(source.id);
      const summary = workflowResourceSummary(source, resources, overview);
      const canConfigureSource = canManageProject(overview, source.projectId, "project.configure");
      return <Fragment key={`config-${source.id}`}>
        <tr className="configuration-source-row" onMouseDown={(event) => { if (event.detail > 1) event.preventDefault(); }} onClick={(event) => {
          if (event.target instanceof Element && event.target.closest("button, a, input, select, textarea, [role='menuitem']")) return;
          toggleSource(source.id);
        }}>
          <td data-label="Name"><button type="button" className="configuration-source-toggle" aria-label={`${expanded ? "Collapse" : "Expand"} ${source.name}`} aria-expanded={expanded} onClick={() => toggleSource(source.id)}>{expanded ? <CaretDown size={16} weight="bold" /> : <CaretRight size={16} weight="bold" />}<span><strong>{source.name}</strong><small>{workflowResourceCountLabel(resources)}</small></span></button></td>
          <td data-label="Type"><strong className="cell-secondary-heading">Repository configuration</strong><small>{configSourceMethod(source)}</small></td>
          <td data-label="Source"><span className="truncate-cell" title={source.repository}>{repositoryLabel(source.repository)}</span><small className="truncate-cell" title={source.branch}>{source.branch}</small></td>
          <td data-label="Target"><span className="truncate-cell" title={source.path}>{source.path || "Repository root"}</span><small>{project}</small></td>
          <td data-label="Status"><span className={`status-label ${summary.state}`} title={source.lastError}><i />{summary.label}</span><small>{summary.detail}</small></td>
          <td className="row-actions">{canConfigureSource && <RowActionMenu name={source.name}>
            <DropdownMenu.Label className="row-action-group-label">Configuration</DropdownMenu.Label>
            <MenuAction icon={<ArrowClockwise size={16} />} label={busyID === source.id ? "Syncing" : "Sync configuration"} disabled={busyID === source.id} onClick={() => void sourceAction(source, "sync")} />
            <MenuAction icon={<PencilSimple size={16} />} label="Edit configuration" onClick={() => onEditConfig(source)} />
            <DropdownMenu.Separator className="action-menu-separator" />
            <MenuAction icon={<Trash size={16} />} label="Delete configuration" danger onClick={() => setDeleteSource(source)} />
          </RowActionMenu>}</td>
        </tr>
        {expanded && resources.map((resource) => workflowResourceRow(resource, source, true))}
      </Fragment>;
    })}
    {orphanWorkflowResources.map((resource) => workflowResourceRow(resource))}
    {applications.map((application) => {
      const project = overview.projects.find((item) => item.id === application.projectId)?.name ?? "Unknown project";
      const target = overview.servers.find((server) => server.id === application.serverId)?.name ?? "Unknown target";
      const type = application.template ? "Template" : application.buildType === "helm" ? "Helm source" : "Application";
      const method = application.buildType === "helm" ? "Helm" : application.buildType === "compose" ? "Compose" : "Dockerfile";
      const source = application.buildType === "helm" ? application.helmChart || "Chart reference" : application.sourceRepo ? repositoryLabel(application.sourceRepo) : "Saved definition";
      const sourceDetail = application.sourceRepo ? `${repositoryLabel(application.sourceRepo)} / ${application.branch}` : application.buildType === "helm" ? application.helmRepository || application.helmVersion || "Chart default" : method;
      const canConfigureApplication = canManageProject(overview, application.projectId, "project.configure");
      const canDeployApplication = canManageProject(overview, application.projectId, "deployment.run");
      return <tr key={application.id}><td data-label="Name"><strong>{application.name}</strong><small>{project}</small></td><td data-label="Type"><strong className="cell-secondary-heading">{type}</strong><small>{method}</small></td><td data-label="Source"><span className="truncate-cell" title={source}>{source}</span><small className="truncate-cell" title={sourceDetail}>{sourceDetail}</small></td><td data-label="Target">{target}</td><td data-label="Status"><span className={`status-label ${application.state}`}><i />{application.state}</span></td><td className="row-actions">{(canDeployApplication || canConfigureApplication || (!application.template && application.buildType === "helm")) && <RowActionMenu name={application.name}>
        {canDeployApplication && !application.template && <MenuAction icon={<RocketLaunch size={16} />} label="Deploy" onClick={() => onDeploy(application.id)} />}
        {!application.template && application.buildType === "helm" && <MenuAction icon={<SlidersHorizontal size={16} />} label="Helm values" onClick={() => onValues(application)} />}
        {canConfigureApplication && !application.template && <MenuAction icon={<Lightning size={16} />} label="Build hook" onClick={() => onHooks(application)} />}
        {canConfigureApplication && <MenuAction icon={<Trash size={16} />} label="Delete" danger onClick={() => onDelete(application)} />}
      </RowActionMenu>}</td></tr>;
    })}
    {overview.previewGroups.map((group) => {
      const entrypoint = group.components.find((component) => component.entrypoint);
      const targets = [...new Set(group.components.map((component) => overview.apps.find((app) => app.id === component.appId)?.serverId).map((serverID) => overview.servers.find((server) => server.id === serverID)?.name).filter((name): name is string => Boolean(name)))];
      const activeRun = overview.previewGroupRuns.find((run) => run.groupId === group.id && run.state !== "closed");
      return <tr key={`group-${group.id}`}><td data-label="Name"><strong>{group.name}</strong><small>{group.components.length} component{group.components.length === 1 ? "" : "s"}</small></td><td data-label="Type"><strong className="cell-secondary-heading">Preview group</strong><small><code>{group.command}</code></small></td><td data-label="Source"><span className="truncate-cell" title={entrypoint?.repository}>{entrypoint?.repository ?? "No entrypoint"}</span><small>{group.components.length} linked repositor{group.components.length === 1 ? "y" : "ies"}</small></td><td data-label="Target"><span className="truncate-cell" title={targets.join(", ")}>{targets.join(", ") || "Not set"}</span></td><td data-label="Status"><span className={`status-label ${group.enabled ? "enabled" : "disabled"}`}><i />{group.enabled ? "enabled" : "disabled"}</span></td><td className="row-actions">{(activeRun?.entrypointUrl || overview.identity?.systemRole === "owner") && <RowActionMenu name={group.name}>
        {activeRun?.entrypointUrl && <DropdownMenu.Item asChild><a className="action-menu-item" href={activeRun.entrypointUrl} target="_blank" rel="noreferrer"><ArrowSquareOut size={16} /><span><strong>Open preview</strong></span></a></DropdownMenu.Item>}
        {overview.identity?.systemRole === "owner" && <MenuAction icon={<PencilSimple size={16} />} label="Edit" onClick={() => onEditGroup(group)} />}
        {overview.identity?.systemRole === "owner" && <MenuAction icon={<Trash size={16} />} label="Delete" danger onClick={() => onDeleteGroup(group)} />}
      </RowActionMenu>}</td></tr>;
    })}
  </tbody></table></div>{deleteSource && <ConfigSourceDeleteDialog source={deleteSource} busy={busyID === deleteSource.id} error={error} onClose={() => { setDeleteSource(null); setError(""); }} onDelete={() => void sourceAction(deleteSource, "delete")} />}</section>;
}

function RowActionMenu({ name, children }: { name: string; children: ReactNode }) {
  return <DropdownMenu.Root>
    <DropdownMenu.Trigger asChild><button className="row-action-menu" type="button" aria-label={`Options for ${name}`} title={`Options for ${name}`}><DotsThreeVertical size={18} weight="bold" /></button></DropdownMenu.Trigger>
    <DropdownMenu.Portal><DropdownMenu.Content className="action-menu-list row-action-menu-list" align="end" sideOffset={6} collisionPadding={12}>{children}</DropdownMenu.Content></DropdownMenu.Portal>
  </DropdownMenu.Root>;
}

function latestWorkflowDeploymentID(overview: Overview, resourceID: string) {
  const revisionIDs = new Set((overview.workflowRevisions ?? []).filter((revision) => revision.resourceId === resourceID).map((revision) => revision.id));
  const stages = (overview.workflowStageRuns ?? []).filter((stage) => revisionIDs.has(stage.revisionId) && stage.deploymentIds?.length).sort((left, right) => right.createdAt.localeCompare(left.createdAt));
  return stages[0]?.deploymentIds?.at(-1);
}

function ConfigSourceDeleteDialog({ source, busy, error, onClose, onDelete }: { source: ConfigSource; busy: boolean; error: string; onClose: () => void; onDelete: () => void }) {
  const dialogRef = useDialogFocus(onClose);
  return <div className="dialog-layer confirm-layer" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialogRef} className="resource-dialog confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-config-title" aria-describedby="delete-config-description">
      <header><div><h2 id="delete-config-title">Delete configuration</h2><p>This removes its imported applications and pipelines.</p></div><button aria-label="Close dialog" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body"><p className="confirm-copy" id="delete-config-description">Delete <strong>{source.name}</strong>?</p>{error && <p className="form-error" role="alert">{error}</p>}<div className="dialog-actions confirm-actions"><button className="quiet-button" onClick={onClose}>Cancel</button><button className="danger-button" disabled={busy} onClick={onDelete}>{busy ? "Deleting..." : "Delete configuration"}</button></div></div>
    </section>
  </div>;
}

function ApplicationCollectionEmpty({ children }: { children: ReactNode }) {
  return <div className="application-collection-empty">{children}</div>;
}

type HelmObject = Record<string, HelmValue>;

function HelmSourceValues({ application, canEdit = true, onChanged }: { application: AppModel; canEdit?: boolean; onChanged: () => Promise<void> }) {
  const [inspection, setInspection] = useState<HelmChartInspection | null>(null);
  const [savedOverrides, setSavedOverrides] = useState<HelmObject>({});
  const [values, setValues] = useState<HelmObject>({});
  const [baseline, setBaseline] = useState<HelmObject>({});
  const [savedValues, setSavedValues] = useState<HelmObject>({});
  const [profile, setProfile] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    void api.appHelmValues(application.id).then((configuration) => {
      if (!active) return;
      const next = mergeHelmValues(configuration.defaults, configuration.overrides);
      setInspection(configuration);
      setSavedOverrides(configuration.overrides);
      setValues(next);
      setBaseline(structuredClone(configuration.defaults));
      setSavedValues(structuredClone(next));
    }).catch((cause: Error) => active && setError(cause.message)).finally(() => active && setLoading(false));
    return () => { active = false; };
  }, [application.id, reload]);

  function selectProfile(path: string) {
    if (!inspection) return;
    const selected = inspection.profiles.find((item) => item.path === path);
    const defaults = selected ? mergeHelmValues(inspection.defaults, selected.values) : inspection.defaults;
    const next = mergeHelmValues(defaults, savedOverrides);
    setProfile(path);
    setValues(next);
    setBaseline(structuredClone(defaults));
    setSavedValues(structuredClone(next));
    setNotice("");
  }

  async function save() {
    if (!inspection) return;
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const overrides = helmValueOverrides(inspection.defaults, values);
      await api.updateAppHelmValues(application.id, overrides);
      setSavedOverrides(overrides);
      setSavedValues(structuredClone(values));
      setNotice("Helm values saved.");
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setSaving(false);
    }
  }

  if (loading) return <section className="helm-values-loading" aria-busy="true"><CircleNotch size={20} weight="bold" /><div><strong>Loading chart values</strong><span>Reading values from the chart.</span></div></section>;
  if (!inspection) return <section className="helm-values-error"><WarningCircle size={20} weight="fill" /><div><strong>Dispatch could not load chart values</strong><p>{error}</p><button className="quiet-button" onClick={() => setReload((value) => value + 1)}>Try again</button></div></section>;
  const changed = JSON.stringify(values) !== JSON.stringify(savedValues);
  return <div className="helm-values-editor-page">
    <div className="helm-values-savebar"><div><strong>Chart values</strong><span>{canEdit ? "Blue marks values that differ from the chart defaults." : "Read-only access."}</span></div><div>{notice && <span className="save-notice" role="status"><CheckCircle size={15} weight="fill" />{notice}</span>}{canEdit && <button className="primary-button" disabled={saving || !changed} onClick={() => void save()}>{saving ? "Saving..." : "Save values"}</button>}</div></div>
    {error && <p className="form-error" role="alert">{error}</p>}
    <HelmValuesEditor inspection={inspection} values={values} baseline={baseline} selectedProfile={profile} onProfileChange={selectProfile} onChange={canEdit ? (next) => { setValues(next); setNotice(""); } : () => undefined} readOnly={!canEdit} />
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
    <header><div><h2 id="application-hook-editor-title">Build and deployment hooks</h2></div></header>
    <form onSubmit={(event) => { event.preventDefault(); void save(false); }} aria-busy={busy}>
      <HookFields preDeployHook={preDeployHook} postDeployHook={postDeployHook} onPreDeployHook={setPreDeployHook} onPostDeployHook={setPostDeployHook} />
      <HookCredentialBindings secrets={secrets} selected={secretIds} onChange={setSecretIds} />
      {application.sourceAuthType === "ssh_key" && application.sourceCredentialId && <div className="manifest-summary application-hook-source-note"><Key size={17} /><p>Source SSH key is available to Git commands.</p></div>}
      <details className="event-hook-context"><summary>Deployment variables</summary><div>{["DISPATCH_PREVIEW_TAG", "DISPATCH_APP_ID", "DISPATCH_APP_NAME", "DISPATCH_REVISION", "DISPATCH_SERVER_NAME", "DISPATCH_VALUES_FILE", "DISPATCH_OUTPUT_FILE"].map((name) => <code key={name}>{name}</code>)}</div></details>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="builder-actions"><button type="button" className="quiet-button" onClick={onClose}>Cancel</button><button type="submit" className="quiet-button" disabled={busy}>{busy ? "Saving..." : "Save hook"}</button><button type="button" className="primary-button" disabled={busy || !preDeployHook.trim()} onClick={() => void save(true)}>{busy ? "Saving..." : "Save and deploy"}</button></div>
    </form>
  </section>;
}

function PrerequisiteState({ hasReadyServer, hasProject, onNavigate }: { hasReadyServer: boolean; hasProject: boolean; onNavigate: (view: View) => void }) {
  return <section className="prerequisite-state"><h2>Setup required</h2><p>Add a project and a ready server before creating an application.</p><div>{!hasReadyServer && <button className="quiet-button" onClick={() => onNavigate("servers")}>View servers</button>}{!hasProject && <button className="quiet-button" onClick={() => onNavigate("projects")}>View projects</button>}</div></section>;
}
