import { FormEvent, useEffect, useMemo, useState } from "react";
import { ArrowSquareOut, Broom, GitPullRequest, LinkSimple, PencilSimple, Plus, Trash, X } from "@phosphor-icons/react";
import { api, App, GitHubRepository, Overview, PreviewGroup, PreviewGroupComponent, PreviewGroupRun } from "./api";
import { relative, short } from "./presentation";

type Props = { overview: Overview; onChanged: () => Promise<void>; embedded?: boolean; hideInventory?: boolean; requestedEdit?: PreviewGroup | "new" | null; onEditingChange?: (editing: boolean) => void };

const blankComponent = (app?: App, entrypoint = false): PreviewGroupComponent => ({
  appId: app?.id ?? "", alias: "", repository: "", defaultBranch: "main", entrypoint, dependsOn: [], bindings: [], preDeployHook: "", postDeployHook: "", secretIds: [],
});

export function PreviewGroupsArea({ overview, onChanged, embedded = false, hideInventory = false, requestedEdit = null, onEditingChange }: Props) {
  const helmApps = overview.apps.filter((app) => app.buildType === "helm" && ["kubernetes", "openshift"].includes(overview.servers.find((server) => server.id === app.serverId)?.runtime ?? ""));
  const readyConnections = overview.githubApps.filter((connection) => connection.installationId && connection.state !== "needs_installation");
  const [editing, setEditing] = useState<PreviewGroup | "new" | null>(null);
  const [selectedRunID, setSelectedRunID] = useState("");
  const [confirmCleanup, setConfirmCleanup] = useState("");
  const [confirmDelete, setConfirmDelete] = useState("");
  const [error, setError] = useState("");
  const selectedRun = overview.previewGroupRuns.find((run) => run.id === selectedRunID);

  useEffect(() => {
    onEditingChange?.(Boolean(editing));
    return () => onEditingChange?.(false);
  }, [editing, onEditingChange]);

  useEffect(() => {
    if (requestedEdit) setEditing(requestedEdit);
  }, [requestedEdit]);

  async function removeGroup(group: PreviewGroup) {
    setError("");
    try { await api.deletePreviewGroup(group.id); setConfirmDelete(""); await onChanged(); }
    catch (cause) { setError((cause as Error).message); }
  }

  async function cleanup(run: PreviewGroupRun) {
    setError("");
    try { await api.cleanupPreviewGroupRun(run.id); setConfirmCleanup(""); await onChanged(); }
    catch (cause) { setError((cause as Error).message); }
  }

  if (editing) return <PreviewGroupBuilder overview={overview} helmApps={helmApps} group={editing === "new" ? undefined : editing} onCancel={() => setEditing(null)} onSaved={async () => { setEditing(null); await onChanged(); }} />;
  if (selectedRun) return <div className="preview-groups-area">
    {error && <p className="form-error" role="alert">{error}</p>}
    <RunDetail run={selectedRun} confirming={confirmCleanup === selectedRun.id} onClose={() => { setSelectedRunID(""); setConfirmCleanup(""); }} onCleanup={() => setConfirmCleanup(selectedRun.id)} onCancelCleanup={() => setConfirmCleanup("")} onConfirmCleanup={() => void cleanup(selectedRun)} />
  </div>;

  if (hideInventory) return null;

  return <div className="preview-groups-area">
    {helmApps.length > 0 && readyConnections.length > 0 && <div className="section-toolbar collection-toolbar"><button className={embedded ? "quiet-button" : "primary-button"} onClick={() => setEditing("new")}><Plus size={16} weight="bold" />New group</button></div>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {overview.previewGroups.length === 0 ? <section className="compact-empty">
      <LinkSimple size={24} />
      <div><h3>{!helmApps.length ? "Helm applications required" : !readyConnections.length ? "GitHub connection required" : "No preview groups"}</h3>{!helmApps.length ? <p>Create a Helm application first.</p> : !readyConnections.length ? <p>Install a GitHub App first.</p> : null}</div>
    </section> : <div className="resource-table-wrap"><table className="resource-table preview-group-table"><thead><tr><th>Group</th><th>Entrypoint</th><th>Components</th><th>Status</th><th>Active preview</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>
      {overview.previewGroups.map((group) => {
        const run = overview.previewGroupRuns.find((item) => item.groupId === group.id && item.state !== "closed");
        const entrypoint = group.components.find((component) => component.entrypoint);
        return <tr key={group.id}>
          <td data-label="Group"><strong>{group.name}</strong><small><code>{group.command}</code></small></td>
          <td data-label="Entrypoint">{entrypoint?.alias ?? "Not set"}</td>
          <td data-label="Components">{group.components.length}</td>
          <td data-label="Status"><Status state={group.enabled ? "enabled" : "disabled"} /></td>
          <td data-label="Active preview">{run ? <button className="run-link" onClick={() => setSelectedRunID(run.id)}><Status state={run.state} />{run.entrypointUrl && <ArrowSquareOut size={14} />}</button> : <span className="muted-value">None</span>}</td>
          <td className="row-actions">
            <button aria-label={`Edit ${group.name}`} onClick={() => setEditing(group)}><PencilSimple size={15} />Edit</button>
            <button className="delete-action" aria-label={`Delete ${group.name}`} onClick={() => setConfirmDelete(group.id)}><Trash size={15} />Delete</button>
          </td>
        </tr>;
      })}
    </tbody></table></div>}

    {confirmDelete && <ConfirmBar title="Delete preview group?" body="Existing closed run history remains. Active groups must be cleaned up first." confirmLabel="Delete group" danger onCancel={() => setConfirmDelete("")} onConfirm={() => { const group = overview.previewGroups.find((item) => item.id === confirmDelete); if (group) void removeGroup(group); }} />}
  </div>;
}

function PreviewGroupBuilder({ overview, helmApps, group, onCancel, onSaved }: { overview: Overview; helmApps: App[]; group?: PreviewGroup; onCancel: () => void; onSaved: () => Promise<void> }) {
  const readyConnections = overview.githubApps.filter((connection) => connection.installationId && connection.state !== "needs_installation");
  const [name, setName] = useState(group?.name ?? "");
  const [githubAppID, setGitHubAppID] = useState(group?.githubAppId ?? readyConnections[0]?.id ?? "");
  const [command, setCommand] = useState(group?.command ?? "/preview");
  const [enabled, setEnabled] = useState(group?.enabled ?? true);
  const [components, setComponents] = useState<PreviewGroupComponent[]>(group?.components.map((item) => ({ ...item, dependsOn: [...item.dependsOn], bindings: item.bindings.map((binding) => ({ ...binding })), secretIds: [...(item.secretIds ?? [])] })) ?? [blankComponent(helmApps[0], true)]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [repositories, setRepositories] = useState<GitHubRepository[]>([]);
  const [repositoriesLoading, setRepositoriesLoading] = useState(false);

  useEffect(() => {
    let active = true;
    setRepositories([]);
    if (!githubAppID) return () => { active = false; };
    setRepositoriesLoading(true);
    void api.githubAppRepositories(githubAppID).then((items) => { if (active) setRepositories(items); }).catch((cause) => { if (active) setError((cause as Error).message); }).finally(() => { if (active) setRepositoriesLoading(false); });
    return () => { active = false; };
  }, [githubAppID]);

  function change(index: number, values: Partial<PreviewGroupComponent>) {
    setComponents((current) => {
      const previousAlias = current[index]?.alias;
      const nextAlias = values.alias;
      return current.map((component, componentIndex) => {
        if (componentIndex === index) return { ...component, ...values };
        if (!previousAlias || nextAlias === undefined || nextAlias === previousAlias) return component;
        return { ...component, dependsOn: component.dependsOn.map((alias) => alias === previousAlias ? nextAlias : alias), bindings: component.bindings.map((binding) => ({ ...binding, source: binding.source.startsWith(`${previousAlias}.`) ? `${nextAlias}${binding.source.slice(previousAlias.length)}` : binding.source })) };
      });
    });
  }

  function makeEntrypoint(index: number) {
    setComponents((current) => current.map((component, componentIndex) => ({ ...component, entrypoint: componentIndex === index })));
  }

  function removeComponent(index: number) {
    setComponents((current) => {
      const removed = current[index];
      const next = current.filter((_, componentIndex) => componentIndex !== index).map((component) => ({ ...component, dependsOn: component.dependsOn.filter((alias) => alias !== removed.alias), bindings: component.bindings.filter((binding) => !binding.source.startsWith(`${removed.alias}.`)) }));
      if (removed.entrypoint && next[0]) next[0] = { ...next[0], entrypoint: true };
      return next;
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      const body = { name, githubAppId: githubAppID, command, enabled, components: components.map(({ id: _id, groupId: _groupId, ...component }) => component) };
      if (group) await api.updatePreviewGroup(group.id, body); else await api.createPreviewGroup(body);
      await onSaved();
    } catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  }

  return <section className="group-builder" aria-labelledby="preview-group-builder-title">
    <header><div><h2 id="preview-group-builder-title">{group ? "Edit preview group" : "New preview group"}</h2></div><button aria-label="Close preview group builder" onClick={onCancel}><X size={18} weight="bold" /></button></header>
    <form onSubmit={submit}>
      <div className="group-basics">
        <label><span>Name</span><input required maxLength={80} value={name} onChange={(event) => setName(event.target.value)} /></label>
        <label><span>GitHub connection</span><select required value={githubAppID} onChange={(event) => setGitHubAppID(event.target.value)}><option value="">Choose a connection</option>{readyConnections.map((connection) => <option key={connection.id} value={connection.id}>{connection.name} ({connection.installationAccount || "installed"})</option>)}</select></label>
        <label><span>Command</span><input required value={command} onChange={(event) => setCommand(event.target.value)} /></label>
        <label className="check-field"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span>Accept preview commands</span></label>
      </div>
      <div className="linked-command-guide"><GitPullRequest size={18} /><div><strong>Link pull requests in one comment</strong><p>Post <code>{command || "/preview"}</code> on a component PR. Add the other repositories with <code>with alias=#123</code>.</p>{components.length > 1 && <code className="command-example">{command || "/preview"} with {components.slice(1).map((component, index) => `${component.alias || `component-${index + 2}`}=#${123 + index}`).join(" ")}</code>}</div></div>
      <div className="component-builder-head"><div><h3>Components</h3></div></div>
      <div className="component-builders">{components.map((component, index) => <ComponentBuilder key={component.id ?? index} index={index} component={component} components={components} helmApps={helmApps} repositories={repositories} repositoriesLoading={repositoriesLoading} onChange={(values) => change(index, values)} onEntrypoint={() => makeEntrypoint(index)} onRemove={() => removeComponent(index)} />)}</div>
      <button type="button" className="add-component-button" onClick={() => setComponents((current) => [...current, blankComponent(helmApps[0])])}><Plus size={14} />Add another component</button>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="builder-actions"><button type="button" className="quiet-button" onClick={onCancel}>Cancel</button><button className="primary-button" disabled={busy || !githubAppID || repositoriesLoading || components.length === 0}>{busy ? "Saving..." : group ? "Save group" : "Create group"}</button></div>
    </form>
  </section>;
}

function ComponentBuilder({ index, component, components, helmApps, repositories, repositoriesLoading, onChange, onEntrypoint, onRemove }: { index: number; component: PreviewGroupComponent; components: PreviewGroupComponent[]; helmApps: App[]; repositories: GitHubRepository[]; repositoriesLoading: boolean; onChange: (values: Partial<PreviewGroupComponent>) => void; onEntrypoint: () => void; onRemove: () => void }) {
  const dependencies = components.filter((candidate) => candidate !== component && candidate.alias);
  return <fieldset className="component-builder"><legend>Component {index + 1}</legend>
    <div className="component-fields">
      <label><span>Application template</span><select required value={component.appId} onChange={(event) => onChange({ appId: event.target.value })}>{helmApps.map((app) => <option key={app.id} value={app.id}>{app.name}</option>)}</select></label>
      <label><span>Alias</span><input required placeholder="service" value={component.alias} onChange={(event) => onChange({ alias: event.target.value })} /><small>Used in commands and output bindings.</small></label>
      <label><span>Repository</span><select required value={component.repository} disabled={repositoriesLoading} onChange={(event) => { const repository = repositories.find((item) => item.fullName === event.target.value); const suggestedAlias = repository?.name.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^[^a-z]+/, "").slice(0, 31); onChange({ repository: event.target.value, ...(repository?.defaultBranch ? { defaultBranch: repository.defaultBranch } : {}), ...(!component.alias && suggestedAlias ? { alias: suggestedAlias } : {}) }); }}><option value="">{repositoriesLoading ? "Loading repositories..." : "Choose a repository"}</option>{component.repository && !repositories.some((item) => item.fullName === component.repository) && <option value={component.repository}>{component.repository} (not currently installed)</option>}{repositories.map((repository) => <option key={repository.id} value={repository.fullName}>{repository.fullName}</option>)}</select></label>
      <label><span>Default branch</span><input required value={component.defaultBranch} onChange={(event) => onChange({ defaultBranch: event.target.value })} /></label>
    </div>
    <div className="component-options">
      <label className="check-field"><input type="radio" name="entrypoint" checked={component.entrypoint} onChange={onEntrypoint} /><span>Group entrypoint</span></label>
      {dependencies.length > 0 && <div className="dependency-field"><span>Deploy after</span>{dependencies.map((dependency) => <label className="check-field" key={dependency.alias}><input type="checkbox" checked={component.dependsOn.includes(dependency.alias)} onChange={(event) => onChange({ dependsOn: event.target.checked ? [...component.dependsOn, dependency.alias] : component.dependsOn.filter((alias) => alias !== dependency.alias) })} /><span>{dependency.alias}</span></label>)}</div>}
    </div>
    <div className="binding-builder"><div className="binding-heading"><span>Helm value bindings</span><button type="button" onClick={() => onChange({ bindings: [...component.bindings, { source: dependencies[0] ? `${dependencies[0].alias}.url` : "", helmValuePath: "" }] })}><Plus size={13} />Add binding</button></div>
      {component.bindings.map((binding, bindingIndex) => <div className="binding-row" key={bindingIndex}><label><span>Source output</span><input placeholder="service.url" value={binding.source} onChange={(event) => onChange({ bindings: component.bindings.map((item, index) => index === bindingIndex ? { ...item, source: event.target.value } : item) })} /></label><label><span>Helm value path</span><input placeholder="config.backendUrl" value={binding.helmValuePath} onChange={(event) => onChange({ bindings: component.bindings.map((item, index) => index === bindingIndex ? { ...item, helmValuePath: event.target.value } : item) })} /></label><button type="button" aria-label="Remove binding" onClick={() => onChange({ bindings: component.bindings.filter((_, index) => index !== bindingIndex) })}><X size={15} /></button></div>)}
    </div>
    {components.length > 1 && <button type="button" className="remove-component" onClick={onRemove}><Trash size={14} />Remove component</button>}
  </fieldset>;
}

function RunDetail({ run, confirming, onClose, onCleanup, onCancelCleanup, onConfirmCleanup }: { run: PreviewGroupRun; confirming: boolean; onClose: () => void; onCleanup: () => void; onCancelCleanup: () => void; onConfirmCleanup: () => void }) {
  const sourceByAlias = useMemo(() => new Map(run.sources.map((source) => [source.alias, source])), [run.sources]);
  const [logs, setLogs] = useState<Record<string, Array<{ id: number; message: string; level: string; createdAt: string }>>>({});
  useEffect(() => {
    let active = true;
    void Promise.all(run.components.filter((component) => component.deploymentId).map(async (component) => [component.alias, await api.logs(component.deploymentId!)] as const)).then((entries) => { if (active) setLogs(Object.fromEntries(entries)); }).catch(() => undefined);
    return () => { active = false; };
  }, [run.id, run.attempt, run.components]);
  return <section className="run-detail" aria-labelledby="run-detail-title"><header><div><h2 id="run-detail-title">{run.group?.name ?? "Preview run"}</h2><p><code>{run.namespace}</code> · attempt {run.attempt}</p></div><button aria-label="Close run details" onClick={onClose}><X size={18} weight="bold" /></button></header>
    <div className="run-summary"><Status state={run.state} />{run.entrypointUrl && <a href={run.entrypointUrl} target="_blank" rel="noreferrer">Open preview<ArrowSquareOut size={14} /></a>}<span>{relative(run.updatedAt)}</span></div>
    {run.message && <p className="run-message">{run.message}</p>}
    <div className="run-components">{run.group?.components.map((definition) => { const source = sourceByAlias.get(definition.alias); const execution = run.components.find((item) => item.alias === definition.alias); return <article key={definition.alias}><div><strong>{definition.alias}</strong><Status state={execution?.state ?? "pending"} /></div><dl><div><dt>Source</dt><dd>{source?.pullRequest ? <><GitPullRequest size={13} />PR #{source.pullRequest}</> : source?.headRef ?? definition.defaultBranch}</dd></div><div><dt>Revision</dt><dd><code>{short(source?.sha, 12)}</code></dd></div><div><dt>Release</dt><dd><code>{execution?.outputs?.release ?? "Pending"}</code></dd></div></dl>{execution?.outputs && <details><summary>Outputs</summary><dl className="output-list">{Object.entries(execution.outputs).map(([key, value]) => <div key={key}><dt>{key}</dt><dd title={value}>{value}</dd></div>)}</dl></details>}{execution?.deploymentId && <details><summary>Deployment log</summary><div className="component-log" role="log">{logs[definition.alias]?.length ? logs[definition.alias].map((entry) => <div key={entry.id} className={entry.level}><time>{new Date(entry.createdAt).toLocaleTimeString([], { hour12: false })}</time><span>{entry.message}</span></div>) : <p>Loading log.</p>}</div></details>}</article>; })}</div>
    {run.attempts.length > 0 && <details className="attempt-history"><summary>Attempt history</summary><ol>{run.attempts.map((attempt) => <li key={attempt.id}><span>Attempt {attempt.sequence}</span><Status state={attempt.state} /><time dateTime={attempt.createdAt}>{relative(attempt.createdAt)}</time><small>{attempt.message}</small></li>)}</ol></details>}
    {run.state !== "closed" && !confirming && <div className="run-actions"><button className="danger-button" onClick={onCleanup}><Broom size={15} />Clean up preview</button></div>}
    {confirming && <ConfirmBar title="Clean up this preview?" body="Cleanup deletes every release and the shared namespace. Dispatch then posts the final status to linked PRs." confirmLabel="Clean up" danger onCancel={onCancelCleanup} onConfirm={onConfirmCleanup} />}
  </section>;
}

function ConfirmBar({ title, body, confirmLabel, danger, onCancel, onConfirm }: { title: string; body: string; confirmLabel: string; danger?: boolean; onCancel: () => void; onConfirm: () => void }) {
  return <div className="confirm-bar" role="alert"><div><strong>{title}</strong><p>{body}</p></div><div><button className="quiet-button" onClick={onCancel}>Cancel</button><button className={danger ? "danger-button" : "primary-button"} onClick={onConfirm}>{confirmLabel}</button></div></div>;
}

function Status({ state }: { state: string }) { return <span className={`status-label ${state}`}><i />{state.replaceAll("_", " ")}</span>; }
