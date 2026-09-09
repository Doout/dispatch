import { ConfigSourceError } from "./ConfigSourceError";
import { FormEvent, useEffect, useId, useState } from "react";
import { api, ConfigSource, GitHubRepository, Overview } from "../api";

export function WorkflowConfigSourceForm({ overview, source, onCancel, onSaved }: { overview: Overview; source?: ConfigSource; onCancel: () => void; onSaved: () => Promise<void> }) {
  const readyApps = overview.githubApps.filter((connection) => connection.state === "ready" || connection.id === source?.githubAppId);
  const credentials = overview.secrets.filter((secret) => secret.type === "ssh_private_key" || secret.type === "github_token" || secret.type === "api_token");
  const [projectID, setProjectID] = useState(source?.projectId ?? overview.projects[0]?.id ?? "");
  const [access, setAccess] = useState(source?.githubAppId ? `github:${source.githubAppId}` : source?.credentialSecretId ? `secret:${source.credentialSecretId}` : readyApps[0] ? `github:${readyApps[0].id}` : credentials[0] ? `secret:${credentials[0].id}` : "");
  const githubAppID = access.startsWith("github:") ? access.slice(7) : "";
  const credentialSecretID = access.startsWith("secret:") ? access.slice(7) : "";
  const [name, setName] = useState(source?.name ?? "");
  const [repository, setRepository] = useState(source?.repository ?? "");
  const [branch, setBranch] = useState(source?.branch ?? "main");
  const [path, setPath] = useState(source?.path ?? ".dispatch");
  const [syncMode, setSyncMode] = useState<ConfigSource["syncMode"]>(source?.syncMode ?? (credentialSecretID ? "poll" : "webhook_poll"));
  const [pollInterval, setPollInterval] = useState(source?.pollIntervalSeconds ?? 300);
  const [repositories, setRepositories] = useState<GitHubRepository[]>([]);
  const [loadingRepositories, setLoadingRepositories] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const repositoryListID = useId();

  useEffect(() => {
    let active = true;
    setRepositories([]);
    if (!githubAppID) return () => {
      active = false;
    };
    setLoadingRepositories(true);
    void api.githubAppRepositories(githubAppID).then((items) => {
      if (active) setRepositories(items);
    }).catch((cause: Error) => {
      if (active) setError(cause.message);
    }).finally(() => {
      if (active) setLoadingRepositories(false);
    });
    return () => { active = false; };
  }, [githubAppID]);

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const body = {
      projectId: projectID,
      githubAppId: githubAppID || undefined,
      credentialSecretId: credentialSecretID || undefined,
      name: name.trim(),
      repository: repository.trim(),
      branch: branch.trim(),
      path: path.trim(),
      syncMode: credentialSecretID ? "poll" as const : syncMode,
      pollIntervalSeconds: pollInterval,
    };
    try {
      if (source) await api.updateConfigSource(source.id, body);
      else await api.createConfigSource(body);
      await onSaved();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <form className="resource-form workflow-config-form" onSubmit={save} aria-busy={busy}>
    <label>
      <span>Name</span>
      <input autoFocus value={name} onChange={(event) => setName(event.target.value)} placeholder="Production applications" required />
    </label>
    <label>
      <span>Project</span>
      <select value={projectID} onChange={(event) => setProjectID(event.target.value)} required>
        <option value="">Choose a project</option>
        {overview.projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
      </select>
    </label>
    <label>
      <span>Repository access</span>
      <select value={access} onChange={(event) => {
        const value = event.target.value;
        setAccess(value);
        if (value.startsWith("secret:")) setSyncMode("poll");
        setRepository("");
        setError("");
      }} required>
        <option value="">Choose access</option>
        {readyApps.map((connection) => <option key={connection.id} value={`github:${connection.id}`}>{connection.name}</option>)}
        {credentials.map((secret) => <option key={secret.id} value={`secret:${secret.id}`}>{secret.name}</option>)}
      </select>
    </label>
    <label>
      <span>Repository</span>
      <input list={githubAppID ? repositoryListID : undefined} value={repository} onChange={(event) => setRepository(event.target.value)} placeholder={loadingRepositories ? "Loading repositories..." : githubAppID ? "owner/repository" : "git@host:owner/repository.git"} required spellCheck={false} />
      <datalist id={repositoryListID}>{repositories.map((item) => <option key={item.id} value={item.fullName} />)}</datalist>
    </label>
    <label>
      <span>Branch</span>
      <input value={branch} onChange={(event) => setBranch(event.target.value)} placeholder="main" required spellCheck={false} />
    </label>
    <label>
      <span>Configuration path</span>
      <input value={path} onChange={(event) => setPath(event.target.value)} placeholder=".dispatch" required spellCheck={false} />
    </label>
    <label>
      <span>Updates</span>
      <select value={credentialSecretID ? "poll" : syncMode} onChange={(event) => setSyncMode(event.target.value as ConfigSource["syncMode"])} disabled={Boolean(credentialSecretID)}>
        <option value="webhook_poll">Webhook and polling</option>
        <option value="webhook">Webhook only</option>
        <option value="poll">Polling only</option>
      </select>
    </label>
    <label>
      <span>Poll interval</span>
      <input type="number" min={30} max={86400} value={pollInterval} onChange={(event) => setPollInterval(Number(event.target.value))} disabled={syncMode === "webhook"} />
      <small>Seconds between repository checks.</small>
    </label>
    {source && <div className="wide"><ConfigSourceError source={source} /></div>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="dialog-actions">
      <button type="button" className="quiet-button" onClick={onCancel}>Cancel</button>
      <button className="primary-button" disabled={busy || !projectID || !access || !name.trim() || !repository.trim() || !branch.trim() || !path.trim()}>{busy ? "Saving..." : source ? "Save changes" : "Import"}</button>
    </div>
  </form>;
}
