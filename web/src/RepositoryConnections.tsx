import { GitBranch, PencilSimple } from "@phosphor-icons/react";
import { useState, type FormEvent } from "react";
import { api, type Overview, type RepositoryConnection } from "./api";
import { TableIconAction } from "./ResourceTable";
import { routePath } from "./routes";

function repositoryOrigin(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" && !url.username && !url.password ? url.origin : "";
  } catch {
    return "";
  }
}

export function RepositoryConnectionForm({ overview, connection, onCancel, onSaved }: {
  overview: Overview;
  connection?: RepositoryConnection;
  onCancel: () => void;
  onSaved: (saved: RepositoryConnection) => void;
}) {
  const [projectId, setProjectId] = useState(connection?.projectId ?? (overview.projects.length === 1 ? overview.projects[0].id : ""));
  const [name, setName] = useState(connection?.name ?? "");
  const [repositoryUrl, setRepositoryUrl] = useState(connection?.repositoryUrl ?? "");
  const [provider, setProvider] = useState<"pat" | "app">(connection?.githubAppId ? "app" : "pat");
  const [secretId, setSecretId] = useState(connection?.credentialSecretId ?? "");
  const [appId, setAppId] = useState(connection?.githubAppId ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const tokens = overview.secrets.filter(secret => secret.type === "github_token");
  const origin = repositoryOrigin(repositoryUrl);
  const apps = overview.githubApps.filter(app => origin && app.state === "ready" && Number(app.installationId) > 0 && repositoryOrigin(app.webUrl) === origin);
  const validCredential = provider === "pat" ? tokens.some(token => token.id === secretId) : apps.some(app => app.id === appId);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (busy || !validCredential) return;
    setError("");
    setBusy(true);
    const body = {
      projectId, name: name.trim(), repositoryUrl: repositoryUrl.trim(),
      ...(provider === "pat" ? { credentialSecretId: secretId } : { githubAppId: appId }),
    };
    try {
      onSaved(connection ? await api.updateRepositoryConnection(connection.id, body) : await api.createRepositoryConnection(body));
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <section className="inline-create connection-editor" aria-labelledby="repository-connection-heading">
    <header><h2 id="repository-connection-heading">{connection ? "Edit repository connection" : "Add repository connection"}</h2></header>
    <div className="inline-create-body">
      <form className="resource-form" onSubmit={save} aria-busy={busy}>
        <label><span>Project</span>
          <select required disabled={!!connection || busy} value={projectId} onChange={event => setProjectId(event.target.value)}>
            <option value="">Choose a project</option>
            {overview.projects.map(project => <option key={project.id} value={project.id}>{project.name}</option>)}
          </select>
          {!overview.projects.length && <small><a href={routePath({ view: "projects" })}>Add a project</a> to use this connection.</small>}
        </label>
        <label><span>Connection name</span>
          <input required maxLength={80} disabled={!!connection || busy} value={name} placeholder="gitops-read" onChange={event => setName(event.target.value)} />
          <small>Workflow source YAML uses <code>connection: {JSON.stringify(name.trim() || "gitops-read")}</code>.</small>
        </label>
        <label className="wide"><span>Repository URL</span>
          <input type="url" required disabled={!!connection || busy} placeholder="https://github.example.com/owner/repository" spellCheck={false}
            value={repositoryUrl} onChange={event => setRepositoryUrl(event.target.value)} />
          <small>Enter the HTTPS clone URL for this repository.</small>
        </label>
        <label><span>Authentication</span>
          <select disabled={busy} value={provider} onChange={event => setProvider(event.target.value === "app" ? "app" : "pat")}>
            <option value="pat">Personal access token</option><option value="app">GitHub App</option>
          </select>
        </label>
        {provider === "pat" ? <label><span>Token variable</span>
          <select required disabled={busy} value={secretId} onChange={event => setSecretId(event.target.value)}>
            <option value="">Choose a GitHub token</option>
            {tokens.map(token => <option key={token.id} value={token.id}>{token.name}</option>)}
          </select>
          <small>Add or rotate a GitHub token in <a href={routePath({ view: "secrets" })}>Variables</a>.</small>
        </label> : <label><span>GitHub App</span>
          <select required disabled={busy} value={apps.some(app => app.id === appId) ? appId : ""} onChange={event => setAppId(event.target.value)}>
            <option value="">Choose an installed App</option>
            {apps.map(app => <option key={app.id} value={app.id}>{app.name}</option>)}
          </select>
          {!apps.length && <small>{origin ? `Install and verify a GitHub App on ${new URL(origin).host} for this repository.` : "Enter the repository URL to see Apps on that host."}</small>}
        </label>}
        {error && <p className="form-error wide" role="alert">{error}</p>}
        <div className="dialog-actions wide">
          <button type="button" className="quiet-button" disabled={busy} onClick={onCancel}>Cancel</button>
          <button className="primary-button" disabled={busy || !projectId || !name.trim() || !origin || !validCredential}>
            {busy ? "Saving…" : connection ? "Save connection" : "Add connection"}
          </button>
        </div>
      </form>
    </div>
  </section>;
}

export function RepositoryConnectionList({ connections, overview, onEdit }: {
  connections: RepositoryConnection[];
  overview: Overview;
  onEdit: (connection: RepositoryConnection) => void;
}) {
  return <section className="connection-provider-group" aria-labelledby="repository-connections-title">
    <header className="connection-provider-header"><span className="connection-provider-icon"><GitBranch size={19} /></span>
      <div><h2 id="repository-connections-title">Repository connections</h2></div><strong>{connections.length}</strong></header>
    {connections.length ? <div className="resource-table-wrap"><table className="resource-table"><thead><tr>
      <th>Connection name</th><th>Project</th><th>Repository</th><th>Authentication</th><th><span className="sr-only">Actions</span></th>
    </tr></thead><tbody>{connections.map(item => <tr key={item.id}>
      <td data-label="Connection name"><strong>{item.name}</strong></td>
      <td data-label="Project">{overview.projects.find(project => project.id === item.projectId)?.name ?? item.projectId}</td>
      <td data-label="Repository">{item.repositoryUrl}</td>
      <td data-label="Authentication">{item.githubAppId ? overview.githubApps.find(app => app.id === item.githubAppId)?.name ?? "GitHub App" : "Personal access token"}</td>
      <td className="row-actions"><TableIconAction label={`Edit ${item.name}`} tooltip="Edit" onClick={() => onEdit(item)}><PencilSimple size={16} /></TableIconAction></td>
    </tr>)}</tbody></table></div> : <div className="connection-provider-empty">No repository connections</div>}
  </section>;
}
