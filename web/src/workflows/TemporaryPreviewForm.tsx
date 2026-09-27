import { hasPreviewID, previewIDVariable } from "./previewVariables";
import { FormEvent, useEffect, useState } from "react";
import { api, Overview, WorkflowPreviewTrigger, WorkflowPreviewTriggerInput, WorkflowResource } from "../api";

export function TemporaryPreviewForm({ overview, resource, onCancel, onSaved }: { overview: Overview; resource?: WorkflowResource; onCancel: () => void; onSaved: () => Promise<void> }) {
  const sources = (overview.configSources ?? []).filter((item) => item.active);
  const apps = overview.githubApps.filter((item) => item.state === "ready" && item.installationId);
  const [sourceId, setSourceId] = useState(resource?.configSourceId ?? sources[0]?.id ?? "");
  const [document, setDocument] = useState(resource?.document ?? "");
  const [previewId, setPreviewId] = useState("");
  const [githubAppId, setGithubAppId] = useState(apps[0]?.id ?? "");
  const [repository, setRepository] = useState("");
  const [pullRequestNumber, setPullRequestNumber] = useState(0);
  const [command, setCommand] = useState("/preview");
  const [previewUrl, setPreviewUrl] = useState("");
  const [configPath, setConfigPath] = useState("");
  const [loadedFile, setLoadedFile] = useState("");
  const [trigger, setTrigger] = useState<WorkflowPreviewTrigger>();
  const [created, setCreated] = useState<WorkflowResource>();
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(Boolean(resource));
  const [loadingFile, setLoadingFile] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!resource) return;
    let active = true;
    void api.workflowPreviewTriggers().then((items) => {
      if (!active) return;
      const current = items.find((item) => item.resourceId === resource.id && !item.closedAt);
      setTrigger(current);
      if (current) {
        setGithubAppId(current.githubAppId);
        setRepository(current.repository);
        setPullRequestNumber(current.pullRequestNumber);
        setCommand(current.command);
        setPreviewUrl(current.previewUrl ?? "");
      }
    }).catch((cause: Error) => active && setError(cause.message)).finally(() => active && setLoading(false));
    return () => { active = false; };
  }, [resource]);

  async function loadYamlFromPR() {
    setLoadingFile(true);
    setError("");
    try {
      const file = await api.importWorkflowPreviewDocument({ githubAppId, repository: repository.trim(), pullRequestNumber, path: configPath.trim() });
      setDocument(file.document);
      setLoadedFile(`${file.path} at ${file.headSha.slice(0, 12)}`);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setLoadingFile(false);
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const body: WorkflowPreviewTriggerInput = { githubAppId, repository: repository.trim(), pullRequestNumber, command: command.trim(), previewUrl: previewUrl.trim() };
      let current = resource ?? created;
      if (current) current = await api.updateTemporaryWorkflowResource(current.id, document);
      else {
        current = await api.createTemporaryWorkflowResource({ configSourceId: sourceId, document, previewId: hasPreviewID(document) ? previewId.trim() || String(pullRequestNumber) : undefined });
        setCreated(current);
        setDocument(current.document);
      }
      if (trigger) await api.updateWorkflowPreviewTrigger(trigger.id, body);
      else setTrigger(await api.createWorkflowPreviewTrigger(current.id, body));
      await onSaved();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <form className="resource-form temporary-preview-form" onSubmit={(event) => void save(event)} aria-busy={busy}>
    <p className="wide temporary-preview-intro">Dispatch stores this Application here. The repository source supplies access to its Git repositories, and the GitHub App watches PR comments. The preview appears in its own group in Applications.</p>
    <label><span>Repository access source</span><select value={sourceId} disabled={Boolean(resource || created)} onChange={(event) => setSourceId(event.target.value)} required><option value="">Choose a configuration source</option>{sources.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.repository}</option>)}</select><small>Used for credentials only. The preview is listed separately.</small></label>
    {!resource && <label><span>Preview ID</span><input value={previewId} onChange={(event) => setPreviewId(event.target.value)} placeholder={pullRequestNumber ? String(pullRequestNumber) : "PR number"} spellCheck={false} /><small>Replaces <code>{previewIDVariable}</code> in the YAML. Defaults to the PR number; collisions get a suffix.</small></label>}
    <label><span>GitHub App for comments</span><select value={githubAppId} onChange={(event) => setGithubAppId(event.target.value)} required><option value="">Choose an installed App</option>{apps.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
    <label><span>PR repository</span><input value={repository} onChange={(event) => setRepository(event.target.value)} placeholder="owner/repository" required spellCheck={false} /><small>Must match a source repository in the YAML.</small></label>
    <label><span>PR number</span><input type="number" min={1} value={pullRequestNumber || ""} onChange={(event) => setPullRequestNumber(Number(event.target.value))} required /></label>
    <div className="wide temporary-preview-file-import"><label><span>YAML file path in PR branch</span><input value={configPath} onChange={(event) => setConfigPath(event.target.value)} placeholder=".dispatch/preview.yaml" spellCheck={false} /><small>Optional. Load one YAML or JSON file from the current head of this PR.</small></label><button type="button" className="quiet-button" disabled={busy || loading || loadingFile || !githubAppId || !repository.trim() || pullRequestNumber < 1 || !configPath.trim()} onClick={() => void loadYamlFromPR()}>{loadingFile ? "Loading…" : "Load YAML from PR"}</button>{loadedFile && <p role="status">Loaded {loadedFile}. Review and save this snapshot; later PR commits do not change it automatically.</p>}</div>
    <label className="wide"><span>Application YAML</span><textarea value={document} onChange={(event) => { setDocument(event.target.value); setLoadedFile(""); }} rows={20} spellCheck={false} required placeholder={"apiVersion: dispatch/v1alpha1\nkind: Application\nmetadata:\n  name: dev-preview-{{ instance.id }}\nspec:\n  # Add sources, jobs, deployments, and stages"} /><small>Define builds, Helm deployments, target, and release metadata here. Keep the Application name when editing an existing preview.</small></label>
    <label><span>Comment command</span><input value={command} onChange={(event) => setCommand(event.target.value)} placeholder="/preview" required spellCheck={false} /><small>Post this command on the PR to start a deployment. Add <code>with ui=#123</code> to link a UI PR.</small></label>
    <label className="wide"><span>Preview URL</span><input type="url" value={previewUrl} onChange={(event) => setPreviewUrl(event.target.value)} placeholder="https://dev.example.com/app/preview/42" required spellCheck={false} /><small>The GitHub App reports this URL and the deployed commits after a successful run.</small></label>
    {trigger?.linkedPullRequests && Object.keys(trigger.linkedPullRequests).length > 0 && <p className="wide temporary-preview-links">Linked PRs: {Object.entries(trigger.linkedPullRequests).map(([alias, number]) => `${alias} #${number}`).join(", ")}</p>}
    {loading && <p className="wide" role="status">Loading comment trigger…</p>}
    {error && <p className="wide form-error" role="alert">{error}</p>}
    <div className="dialog-actions wide"><button type="button" className="quiet-button" onClick={onCancel}>Cancel</button><button className="primary-button" disabled={busy || loading || loadingFile || !sourceId || !githubAppId || !document.trim() || !repository.trim() || pullRequestNumber < 1 || !command.trim() || !previewUrl.trim()}>{busy ? "Saving…" : resource ? "Save preview" : "Create preview"}</button></div>
  </form>;
}
