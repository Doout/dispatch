import { ConfigSourceError } from "./ConfigSourceError";
import { FormEvent, useEffect, useId, useState } from "react";
import { api, ConfigSource, GitHubBranch, GitHubRepository, Overview } from "../api";

export function WorkflowConfigSourceForm({ overview, source, onCancel, onSaved }: { overview: Overview; source?: ConfigSource; onCancel: () => void; onSaved: () => Promise<void> }) {
  const readyApps = overview.githubApps.filter((connection) => connection.state === "ready" || connection.id === source?.githubAppId);
  const credentials = overview.secrets.filter((secret) => secret.type === "ssh_private_key" || secret.type === "github_token" || secret.type === "api_token");
  const [projectID, setProjectID] = useState(source?.projectId ?? overview.projects[0]?.id ?? "");
  const [access, setAccess] = useState(source?.githubAppId ? `github:${source.githubAppId}` : source?.credentialSecretId ? `secret:${source.credentialSecretId}` : readyApps[0] ? `github:${readyApps[0].id}` : credentials[0] ? `secret:${credentials[0].id}` : "");
  const githubAppID = access.startsWith("github:") ? access.slice(7) : "";
  const credentialSecretID = access.startsWith("secret:") ? access.slice(7) : "";
  const [name, setName] = useState(source?.name ?? "");
  const [repository, setRepository] = useState(source?.repository ?? "");
  const [repositoryID, setRepositoryID] = useState(source?.repositoryId ?? 0);
  const [sourceStatus, setSourceStatus] = useState(source);
  const [branch, setBranch] = useState(source?.branch ?? "main");
  const [path, setPath] = useState(source?.path ?? ".dispatch");
  const [syncMode, setSyncMode] = useState<ConfigSource["syncMode"]>(source?.syncMode ?? (credentialSecretID ? "poll" : "webhook_poll"));
  const [pollInterval, setPollInterval] = useState(source?.pollIntervalSeconds ?? 300);
  const [repositories, setRepositories] = useState<GitHubRepository[]>([]);
  const [loadingRepositories, setLoadingRepositories] = useState(false);
  const [repositoryError, setRepositoryError] = useState("");
  const [reload, setReload] = useState(0);
  const [branches, setBranches] = useState<GitHubBranch[]>([]);
  const [loadingBranches, setLoadingBranches] = useState(false);
  const [branchError, setBranchError] = useState("");
  const [checking, setChecking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const repositoryListID = useId();
  const branchListID = useId();
  const selected = repositories.find(item => item.fullName.toLowerCase() === repository.trim().toLowerCase());
  const knownIdentity = repositoryID || selected?.id || 0;
  const replacement = !!(selected && repositoryID && selected.id !== repositoryID);
  const unavailableSelection = selected?.archived || selected?.disabled;

  useEffect(() => {
    let active = true;
    setRepositories([]);
    setRepositoryError("");
    setLoadingRepositories(false);
    if (!githubAppID) return () => {
      active = false;
    };
    setLoadingRepositories(true);
    const pending = source && githubAppID === source.githubAppId ? api.configSourceRepositories(source.id) : api.githubAppRepositories(githubAppID);
    void pending.then((items) => {
      if (active) setRepositories(items);
    }).catch((cause: Error) => {
      if (active) setRepositoryError(cause.message);
    }).finally(() => {
      if (active) setLoadingRepositories(false);
    });
    return () => { active = false; };
  }, [githubAppID, reload, source?.id, source?.githubAppId]);

  useEffect(() => {
    let active = true;
    setBranches([]);
    setBranchError("");
    setLoadingBranches(false);
    if (!githubAppID || !selected || !knownIdentity || unavailableSelection) return () => { active = false; };
    setLoadingBranches(true);
    const pending = source && githubAppID === source.githubAppId ? api.configSourceBranches(source.id, repository.trim(), knownIdentity) : api.githubAppBranches(githubAppID, repository.trim(), knownIdentity);
    void pending.then(items => { if (active) setBranches(items); }).catch((cause: Error) => { if (active) setBranchError(cause.message); }).finally(() => { if (active) setLoadingBranches(false); });
    return () => { active = false; };
  }, [githubAppID, repository, knownIdentity, selected?.id, unavailableSelection, reload, source?.id, source?.githubAppId]);

  async function checkAccess() {
    if (!source) return;
    setChecking(true); setError("");
    try { setSourceStatus(await api.checkConfigSourceRepository(source.id)); setReload(value => value + 1); }
    catch (cause) { setError((cause as Error).message); }
    finally { setChecking(false); }
  }

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
      repositoryId: githubAppID ? knownIdentity || undefined : undefined,
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
        setRepositoryID(0);
        setError("");
      }} required>
        <option value="">Choose access</option>
        {readyApps.map((connection) => <option key={connection.id} value={`github:${connection.id}`}>{connection.name}</option>)}
        {credentials.map((secret) => <option key={secret.id} value={`secret:${secret.id}`}>{secret.name}</option>)}
      </select>
    </label>
    <label>
      <span>Repository</span>
      <input aria-label="Repository" list={githubAppID ? repositoryListID : undefined} value={repository} onChange={(event) => { setRepository(event.target.value); setRepositoryID(repositories.find(item => item.fullName.toLowerCase() === event.target.value.trim().toLowerCase())?.id ?? 0); }} placeholder={loadingRepositories ? "Loading repositories..." : githubAppID ? "owner/repository" : "git@host:owner/repository.git"} required spellCheck={false} />
      <datalist id={repositoryListID}>{repositories.map((item) => <option key={item.id} value={item.fullName}>{item.archived ? "Archived" : item.disabled ? "Disabled" : `Repository ${item.id}`}</option>)}</datalist>
      {githubAppID && loadingRepositories && <small role="status">Loading repositories...</small>}
      {githubAppID && !loadingRepositories && !repositoryError && !repositories.length && <small>No accessible repositories. Check the App installation and try again.</small>}
      {repositoryError && <small role="alert">{repositoryError}</small>}
      {selected && <small>Repository ID {selected.id}{selected.archived ? " · Archived. Unarchive before syncing." : selected.disabled ? " · Disabled by GitHub." : " · App access confirmed"}</small>}
      {replacement && <small role="alert">This name belongs to repository {selected!.id}. The saved source uses repository {repositoryID}.</small>}
      {replacement && <button type="button" className="quiet-button" onClick={() => setRepositoryID(selected!.id)}>Select replacement repository {selected!.id}</button>}
      {githubAppID && <button type="button" className="quiet-button" onClick={() => setReload(value => value + 1)} disabled={loadingRepositories}>Reload repositories</button>}
    </label>
    <label>
      <span>Branch</span>
      <input aria-label="Branch" list={githubAppID ? branchListID : undefined} value={branch} onChange={(event) => setBranch(event.target.value)} placeholder="main" required spellCheck={false} />
      <datalist id={branchListID}>{branches.map(item => <option key={item.name} value={item.name}>{item.protected ? "Protected" : item.sha.slice(0, 12)}</option>)}</datalist>
      {loadingBranches && <small role="status">Loading branches...</small>}
      {branchError && <small role="alert">{branchError} The selected branch is unchanged.</small>}
      {githubAppID && selected && !unavailableSelection && !loadingBranches && !branchError && !branches.length && <small>No branches returned. Enter the exact branch name or populate the repository before syncing.</small>}
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
    {sourceStatus && <div className="wide"><ConfigSourceError source={sourceStatus} />
      {sourceStatus.githubAppId && <button type="button" className="quiet-button" onClick={() => void checkAccess()} disabled={checking || busy}>{checking ? "Checking access..." : "Check repository access"}</button>}
      {sourceStatus.repositoryStatus?.state === "renamed" && <button type="button" className="quiet-button" onClick={() => { setRepository(sourceStatus.repositoryStatus!.fullName!); setRepositoryID(sourceStatus.repositoryStatus!.repositoryId!); }}>Use {sourceStatus.repositoryStatus.fullName}</button>}
      {sourceStatus.repositoryStatus?.state === "renamed" && <p>Save changes to apply the new name. The branch and past commit records stay unchanged.</p>}
    </div>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="dialog-actions">
      <button type="button" className="quiet-button" onClick={onCancel}>Cancel</button>
      <button className="primary-button" disabled={busy || replacement || !!unavailableSelection || !projectID || !access || !name.trim() || !repository.trim() || !branch.trim() || !path.trim()}>{busy ? "Saving..." : source ? "Save changes" : "Import"}</button>
    </div>
  </form>;
}
