import { FormEvent, useMemo, useState } from "react";
import { parse as parseYAML } from "yaml";
import { api, Overview, WorkflowPreviewTemplate, WorkflowResource } from "../api";

import { hasPreviewID, previewIDVariable as placeholder } from "./previewVariables";

export function PreviewTemplateForm({ overview, template, onCancel, onSaved }: { overview: Overview; template?: WorkflowPreviewTemplate; onCancel: () => void; onSaved: () => Promise<void> }) {
  const sources = (overview.configSources ?? []).filter((item) => item.active);
  const apps = overview.githubApps.filter((item) => item.state === "ready" && item.installationId);
  const previews = (overview.workflowResources ?? []).filter((item) => item.temporary);
  const [definitionSource, setDefinitionSource] = useState(template?.gitSource ? "github" : "saved");
  const [gitRepository, setGitRepository] = useState(template?.gitSource?.repository ?? "");
  const [gitBranch, setGitBranch] = useState(template?.gitSource?.branch ?? "main");
  const [gitPath, setGitPath] = useState(template?.gitSource?.path ?? "");
  const [name, setName] = useState(template?.name ?? "");
  const [sourceId, setSourceId] = useState(template?.configSourceId ?? sources[0]?.id ?? "");
  const [githubAppId, setGithubAppId] = useState(template?.githubAppId ?? apps[0]?.id ?? "");
  const [repository, setRepository] = useState(template?.repository ?? "");
  const [command, setCommand] = useState(template?.command ?? "/preview");
  const [previewUrl, setPreviewUrl] = useState(template?.previewUrl ?? "");
  const [document, setDocument] = useState(template?.document ?? "");
  const [active, setActive] = useState(template?.active ?? true);
  const [samplePR, setSamplePR] = useState(0);
  const [configPath, setConfigPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const yamlTrigger = useMemo(() => {
    try {
      const config = parseYAML(document);
      if (config?.kind !== "WorkflowTemplate" || !config.spec?.triggers?.pullRequestComment) return undefined;
      const trigger = config.spec.triggers.pullRequestComment;
      return { command: trigger.command || "/preview", repositories: Array.isArray(trigger.sources) ? trigger.sources.map((alias: string) => config.spec.sources?.[alias]?.repository || alias) as string[] : [] };
    } catch { return undefined; }
  }, [document]);
  const fileControlsTrigger = Boolean(yamlTrigger) || definitionSource === "github";
  const watchedRepositories = yamlTrigger?.repositories ?? template?.watchRepositories ?? [];
  const displayCommand = yamlTrigger?.command ?? command;

  async function usePreview(resource: WorkflowResource) {
    setDefinitionSource("saved");
    setSourceId(resource.configSourceId);
    setName(`${resource.name} template`);
    const primary = resource.previewPullRequests?.[0];
    if (primary) {
      setRepository(primary.repository);
      setSamplePR(primary.number);
      setDocument(resource.document.replaceAll(String(primary.number), placeholder));
    } else {
      setDocument(resource.document);
    }
    try {
      const triggers = await api.workflowPreviewTriggers();
      const trigger = triggers.find((item) => item.resourceId === resource.id && !item.closedAt);
      if (trigger) {
        setGithubAppId(trigger.githubAppId);
        setCommand(trigger.command);
        setRepository(trigger.repository);
        setSamplePR(trigger.pullRequestNumber);
        setDocument(resource.document.replaceAll(String(trigger.pullRequestNumber), placeholder));
        setPreviewUrl(trigger.previewUrl?.replaceAll(String(trigger.pullRequestNumber), placeholder) ?? "");
      }
    } catch (cause) {
      setError((cause as Error).message);
    }
  }

  async function loadYamlFromPR() {
    setLoading(true);
    setError("");
    try {
      const file = await api.importWorkflowPreviewDocument({ githubAppId, repository: repository.trim(), pullRequestNumber: samplePR, path: configPath.trim() });
      setDocument(file.document);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setLoading(false);
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const body = { configSourceId: sourceId, githubAppId, name: name.trim(), repository: repository.trim(), command: command.trim(), previewUrl: previewUrl.trim(), document, active, gitSource: definitionSource === "github" ? { repository: gitRepository.trim(), branch: gitBranch.trim(), path: gitPath.trim() } : undefined };
      if (template) await api.updateWorkflowPreviewTemplate(template.id, body);
      else await api.createWorkflowPreviewTemplate(body);
      await onSaved();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <form className="resource-form temporary-preview-form" onSubmit={(event) => void save(event)} aria-busy={busy}>
    <p className="wide temporary-preview-intro">This template creates a separate Application when a trusted user posts <code>{displayCommand || "/preview"}</code> on an open PR in a watched repository. Each instance gets its own Helm release and URL. Editing the template affects future previews.</p>
    {!template && previews.length > 0 && <label className="wide"><span>Start from a saved preview</span><select value="" onChange={(event) => { const resource = previews.find((item) => item.id === event.target.value); if (resource) void usePreview(resource); }}><option value="">Choose a preview to copy its YAML and settings</option>{previews.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select><small>Review the copied YAML. Replace fixed PR IDs in Application and Helm names with <code>{placeholder}</code>, and check source refs copied from the old PR.</small></label>}
    <label><span>Template name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="Development previews" required /></label>
    <label><span>Repository access source</span><select value={sourceId} onChange={(event) => setSourceId(event.target.value)} required><option value="">Choose a configuration source</option>{sources.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.repository}</option>)}</select><small>Supplies credentials and deployment settings for generated previews.</small></label>
    <label><span>GitHub App</span><select value={githubAppId} onChange={(event) => setGithubAppId(event.target.value)} required><option value="">Choose an installed App</option>{apps.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
    {fileControlsTrigger ? <p className="wide temporary-preview-intro"><strong>Comment trigger from YAML</strong><br />{watchedRepositories.length ? <>{displayCommand} watches {watchedRepositories.join(", ")}. Other sources are dependencies.</> : <>Declare <code>spec.triggers.pullRequestComment.sources</code> and <code>command</code> in the template file. Saving loads the watched repositories from GitHub.</>}</p> : <>
    <label><span>PR repository</span><input value={repository} onChange={(event) => setRepository(event.target.value)} placeholder="owner/repository" required spellCheck={false} /></label>
    <label><span>Comment command</span><input value={command} onChange={(event) => setCommand(event.target.value)} placeholder="/preview" required spellCheck={false} /><small>For example, <code>/preview</code>. Linked PR arguments still work.</small></label>
    </>}
    <label><span>Preview URL pattern</span><input type="text" inputMode="url" value={previewUrl} onChange={(event) => setPreviewUrl(event.target.value)} placeholder="https://dev.example.com/app/preview/{{ instance.id }}" required spellCheck={false} /><small>Use <code>{placeholder}</code> where the PR ID belongs.</small></label>
    <label className="wide"><span>Template definition</span><select value={definitionSource} onChange={(event) => setDefinitionSource(event.target.value)}><option value="saved">YAML saved in Dispatch</option><option value="github">GitHub repository (GitOps)</option></select><small>GitHub definitions are checked every 30 seconds. Valid changes apply to future preview instances.</small></label>
    {definitionSource === "github" && <>
      <label><span>Template repository</span><input value={gitRepository} onChange={(event) => setGitRepository(event.target.value)} required placeholder="example/devops" spellCheck={false} /><small>The selected GitHub App needs Contents read access to this repository.</small></label>
      <label><span>Template branch</span><input value={gitBranch} onChange={(event) => setGitBranch(event.target.value)} required placeholder="main" spellCheck={false} /></label>
      <label className="wide"><span>Template YAML path</span><input value={gitPath} onChange={(event) => setGitPath(event.target.value)} required placeholder="deployment/templates/dev-preview.yaml" spellCheck={false} /><small>Saving loads and validates this file. Templates can share the deployment folder. The slot importer skips kind: WorkflowTemplate.</small></label>
      {template?.gitSource && <p className="wide temporary-preview-intro">{template.gitSource.commitSha ? `Synced commit ${template.gitSource.commitSha.slice(0, 12)}. ` : "Not synced yet. "}<a href={previewTemplateGitURL(template, overview, true)} target="_blank" rel="noreferrer">Edit YAML in GitHub</a></p>}
      {template?.gitSource?.lastError && <p className="wide form-error" role="alert">{template.gitSource.lastError} New preview creation is blocked until the template syncs successfully.</p>}
    </>}
    {definitionSource === "saved" && <div className="wide temporary-preview-file-import"><label><span>YAML file path in a sample PR</span><input value={configPath} onChange={(event) => setConfigPath(event.target.value)} placeholder=".dispatch/preview.yaml" spellCheck={false} /><small>Optional. Load a template file from one open PR branch into the editor.</small></label><label><span>Sample PR number</span><input type="number" min={1} value={samplePR || ""} onChange={(event) => setSamplePR(Number(event.target.value))} /></label><button type="button" className="quiet-button" disabled={busy || loading || !githubAppId || !repository.trim() || samplePR < 1 || !configPath.trim()} onClick={() => void loadYamlFromPR()}>{loading ? "Loading…" : "Load YAML"}</button></div>}
    <p className="wide temporary-preview-intro">Available variables: <code>{placeholder}</code> is the unique deployment ID; <code>{"{{ trigger.pullRequest.number }}"}</code> is the PR number; <code>{"{{ trigger.repository }}"}</code> is its owner/repository; <code>{"{{ trigger.pullRequest.url }}"}</code> is its GitHub link. Source and job variables resolve when the workflow runs.</p>
    <label className="wide"><span>Application YAML template</span><textarea readOnly={definitionSource === "github"} value={document} onChange={(event) => setDocument(event.target.value)} rows={20} spellCheck={false} required={definitionSource === "saved"} placeholder={"apiVersion: dispatch/v1alpha1\nkind: WorkflowTemplate\nmetadata:\n  name: dev-preview-{{ instance.id }}\nspec:\n  triggers:\n    pullRequestComment:\n      sources: [service, ui]\n      command: /preview\n  # Add sources, jobs, deployments, and stages"} /><small>Put <code>{placeholder}</code> in the Application name and every Helm release name. Dispatch allocates a suffix if the preferred PR ID is already in use.</small></label>
    <label className="wide temporary-preview-template-active"><input type="checkbox" checked={active} onChange={(event) => setActive(event.target.checked)} /><span>Watch for new <code>{displayCommand || "/preview"}</code> comments</span></label>
    {error && <p className="wide form-error" role="alert">{error}</p>}
    <div className="dialog-actions wide"><button type="button" className="quiet-button" onClick={onCancel}>Cancel</button><button className="primary-button" disabled={busy || loading || !sourceId || !githubAppId || !name.trim() || (!fileControlsTrigger && !repository.trim()) || (definitionSource === "saved" ? !hasPreviewID(document) : !gitRepository.trim() || !gitBranch.trim() || !gitPath.trim()) || !hasPreviewID(previewUrl)}>{busy ? "Saving…" : template ? "Save template" : "Create template"}</button></div>
  </form>;
}

export function previewTemplateGitURL(template: WorkflowPreviewTemplate, overview: Overview, edit = false) {
  const source = template.gitSource;
  const app = overview.githubApps.find((item) => item.id === template.githubAppId);
  if (!source || !app) return undefined;
  return `${app.webUrl.replace(/\/$/, "")}/${source.repository.split("/").map(encodeURIComponent).join("/")}/${edit ? "edit" : "blob"}/${encodeURIComponent(source.branch)}/${source.path.split("/").map(encodeURIComponent).join("/")}`;
}
