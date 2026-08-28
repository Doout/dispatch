import { ChangeEvent, FormEvent, useRef, useState } from "react";
import {
  ArrowClockwise,
  ArrowRight,
  ArrowSquareOut,
  Check,
  CheckCircle,
  Cloud,
  Copy,
  FolderSimple,
  GithubLogo,
  Key,
  LockSimple,
  PencilSimple,
  PlugsConnected,
  Plus,
  Trash,
  UploadSimple,
  WarningCircle,
  X,
} from "@phosphor-icons/react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import {
  api,
  GitHubAppConnection,
  GitHubAppInstallation,
  GitHubRepository,
  Overview,
  PrivateNetwork,
  Secret,
  SecretStore,
} from "./api";
import { PageHeader } from "./PageHeader";
import { relative } from "./presentation";
import { StatusLabel, TableIconAction } from "./ResourceTable";
import { readSecretTextFile } from "./fileUploads";
import { useDialogFocus } from "./useDialogFocus";

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

function githubAppInstallURL(connection: GitHubAppConnection) {
  if (!connection.slug) return "";
  const path = new URL(connection.webUrl).hostname.toLowerCase() === "github.com" ? "apps" : "github-apps";
  return `${connection.webUrl}/${path}/${encodeURIComponent(connection.slug)}/installations/new`;
}

export function ConnectionsPage({ overview, notice, onNotice, onChanged, onAddRelay = () => {} }: { overview: Overview; notice: string; onNotice: (value: string) => void; onChanged: () => Promise<void>; onAddRelay?: () => void }) {
	const edgeNetworks = (overview.privateNetworks ?? []).filter((network) => network.driver === "dispatch_agent");
  const [creating, setCreating] = useState(false);
  const [creatingSecretStore, setCreatingSecretStore] = useState(false);
  const [editingSecretStore, setEditingSecretStore] = useState<SecretStore | null>(null);
	const [creatingPrivateNetwork, setCreatingPrivateNetwork] = useState(false);
	const [editingPrivateNetwork, setEditingPrivateNetwork] = useState<PrivateNetwork | null>(null);
	const [privateNetworkDriver, setPrivateNetworkDriver] = useState<"dispatch_agent" | "laneway_connector">("dispatch_agent");
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
	const [privateNetworkID, setPrivateNetworkID] = useState("");
  const relayServers = overview.servers.filter((server) => server.runtime === "relay");
  const [eventDelivery, setEventDelivery] = useState<"none" | "direct" | "relay">("none");
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
		setPrivateNetworkID("");
    setEventDelivery("none");
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
		setPrivateNetworkID(connection.privateNetworkId || "");
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
		if (!ownerType) throw new Error("Choose the GitHub App owner.");
		const flow = await api.startGitHubAppManifest({ name, webUrl: webURL, apiUrl: apiURL, ownerType, owner: ownerType === "organization" ? owner : "", eventDelivery, relayServerId: eventDelivery === "relay" ? relayServerID : undefined, privateNetworkId: privateNetworkID || undefined });
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
		eventDelivery,
			privateNetworkId: privateNetworkID,
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
      const result = await api.verifyGitHubApp(connection.id);
      onNotice(result.verification.pushSubscribed ? connection.name + " is connected." : connection.name + " is connected. Add the push event in GitHub for workflow webhooks. Dispatch will keep polling until then.");
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
      const result = await api.verifyGitHubApp(connection.id);
      setInstallations((current) => ({ ...current, [connection.id]: [] }));
      onNotice(result.verification.pushSubscribed ? connection.name + " is installed and verified." : connection.name + " is installed. Add the push event in GitHub for workflow webhooks. Dispatch will keep polling until then.");
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
    if (!connection.webhookUrl) return;
    try {
      await navigator.clipboard.writeText(connection.webhookUrl);
      setCopied(connection.id);
      window.setTimeout(() => setCopied((current) => current === connection.id ? "" : current), 1600);
    } catch {
      setError("Could not copy the webhook URL.");
    }
  }

  const secretStores = overview.secretStores ?? [];
	const privateNetworks = overview.privateNetworks ?? [];
  const hasConnections = overview.githubApps.length + secretStores.length + privateNetworks.length > 0;
  const closeEditor = () => {
    if (creating) reset();
    setCreatingSecretStore(false);
    setEditingSecretStore(null);
		setCreatingPrivateNetwork(false);
		setEditingPrivateNetwork(null);
  };

  return <div className="page-layout connections-page">
	<PageHeader view="connections" trailing={creating || creatingSecretStore || creatingPrivateNetwork
      ? <button type="button" className="quiet-button" onClick={closeEditor}><X size={16} weight="bold" />Cancel</button>
			: <ConnectionAddMenu onGitHub={() => setCreating(true)} onSecretStore={() => { setEditingSecretStore(null); setCreatingSecretStore(true); }} onPrivateNetwork={(driver) => { setPrivateNetworkDriver(driver); setEditingPrivateNetwork(null); setCreatingPrivateNetwork(true); }} />} />
    {notice && <div className="connection-notice" role="status"><CheckCircle size={18} weight="fill" /><span>{notice}</span><button aria-label="Dismiss message" onClick={() => onNotice("")}><X size={15} /></button></div>}
    {error && <p className="form-error connection-error" role="alert">{error}</p>}
    {creating ? <section className="inline-create connection-editor" aria-labelledby="connection-editor-title">
      <header><div><h2 id="connection-editor-title">{editing ? "Edit GitHub App" : "Add GitHub App"}</h2></div></header>
      {!editing && <div className="connection-method" role="radiogroup" aria-label="GitHub App setup method">
        <button type="button" role="radio" aria-checked={method === "manifest"} className={method === "manifest" ? "active" : ""} onClick={() => setMethod("manifest")}><GithubLogo size={19} weight="fill" /><span><strong>Create in GitHub</strong><small>Set up permissions and webhooks.</small></span></button>
        <button type="button" role="radio" aria-checked={method === "manual"} className={method === "manual" ? "active" : ""} onClick={() => setMethod("manual")}><Key size={19} /><span><strong>Existing App</strong><small>Use an App ID and credentials.</small></span></button>
      </div>}
      <form className="connection-form" onSubmit={submit} aria-busy={busyID === (editing?.id || "create")}>
        <div className="connection-grid">
          <label><span>GitHub App name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="Dispatch-a1b2c3d4" maxLength={34} required /></label>
          <label><span>GitHub URL</span><input value={webURL} onChange={(event) => changeWebURL(event.target.value)} placeholder="https://github.example.com" required spellCheck={false} /></label>
			<label><span>Route</span><select value={privateNetworkID} onChange={(event) => setPrivateNetworkID(event.target.value)}><option value="">Direct</option>{edgeNetworks.map((network) => <option key={network.id} value={network.id}>Edge · {network.name}{network.state === "ready" ? "" : ` (${network.state})`}</option>)}</select></label>
          {method === "manifest" && !editing ? <><label><span>Registration owner</span><select value={ownerType} onChange={(event) => { const value = event.target.value as "" | "personal" | "organization"; setOwnerType(value); if (value !== "organization") setOwner(""); }} required><option value="">Choose an account type</option><option value="personal">My personal account</option><option value="organization">GitHub organization</option></select></label>{ownerType === "organization" && <label><span>Organization login</span><input value={owner} onChange={(event) => setOwner(event.target.value)} placeholder="platform-team" required spellCheck={false} /></label>}</> : <>
            <label><span>App ID</span><input inputMode="numeric" value={appID} onChange={(event) => setAppID(event.target.value)} placeholder="123456" required /></label>
            <label><span>Installation ID</span><input inputMode="numeric" value={installationID} onChange={(event) => setInstallationID(event.target.value)} placeholder="Add after installation" /></label>
          </>}
        </div>
        {!editing && <section className="connection-delivery" aria-labelledby="event-delivery-title">
          <div className="connection-webhook-toggle">
            <span><strong id="event-delivery-title">Event delivery</strong><small>{eventDelivery === "none" ? "Polling only" : "Webhooks enabled"}</small></span>
            <label className="connection-toggle-control"><input type="checkbox" role="switch" aria-label="Webhook events" checked={eventDelivery !== "none"} onChange={(event) => setEventDelivery(event.target.checked ? "direct" : "none")} /><span /></label>
          </div>
          {eventDelivery !== "none" && <div className="connection-delivery-route">
            <div className="connection-route-options" role="radiogroup" aria-label="Webhook route">
              <button type="button" role="radio" aria-checked={eventDelivery === "direct"} className={eventDelivery === "direct" ? "selected" : ""} onClick={() => setEventDelivery("direct")}><strong>Direct</strong><small>This controller</small></button>
              {relayServers.length ? <button type="button" role="radio" aria-checked={eventDelivery === "relay"} className={eventDelivery === "relay" ? "selected" : ""} onClick={() => setEventDelivery("relay")}><strong>Relay</strong><small>Public relay</small></button> : <button type="button" role="radio" aria-checked="false" className="add-relay-option" onClick={onAddRelay}><strong>Relay</strong><small>Add a relay server</small></button>}
            </div>
            {eventDelivery === "relay" && <label className="relay-delivery-server"><span>Relay server</span><select value={relayServerID} onChange={(event) => setRelayServerID(event.target.value)} required><option value="">Select a relay</option>{relayServers.map((server) => <option key={server.id} value={server.id}>{server.name}</option>)}</select></label>}
          </div>}
        </section>}
        <details className="connection-advanced"><summary>Advanced GitHub settings <span>Custom Enterprise API</span></summary><div className="connection-grid">
          <label><span>API URL</span><input value={apiURL} onChange={(event) => setAPIURL(event.target.value)} placeholder="https://github.example.com/api/v3" required spellCheck={false} /></label>
          {(method === "manual" || editing) && <><label><span>App slug</span><input value={slug} onChange={(event) => setSlug(event.target.value)} placeholder="Filled during verification" spellCheck={false} /></label><label><span>Client ID</span><input value={clientID} onChange={(event) => setClientID(event.target.value)} placeholder="Optional" spellCheck={false} /></label></>}
        </div></details>
        {(method === "manual" || !!editing) && <div className="connection-credentials">
          <div className="credential-heading"><div><strong>App credentials</strong>{editing && <small>Leave blank to keep the current credentials.</small>}</div><><input ref={privateKeyFile} className="sr-only" type="file" accept=".pem,.key,application/x-pem-file,text/plain" onChange={(event) => void loadPrivateKey(event)} /><button type="button" className="quiet-button" onClick={() => privateKeyFile.current?.click()}><UploadSimple size={15} />Upload PEM</button></></div>
          <div className="connection-grid">
            {(editing || eventDelivery !== "none") && <label><span>Webhook secret</span><input type="password" value={webhookSecret} onChange={(event) => setWebhookSecret(event.target.value)} placeholder={editing ? "Leave blank to keep the secret" : "At least 16 characters"} required={!editing} autoComplete="new-password" /></label>}
            <label className="private-key-field"><span>Private key</span><textarea value={privateKey} onChange={(event) => setPrivateKey(event.target.value)} placeholder={editing ? "Leave blank to keep the key" : "Paste the RSA private key PEM"} required={!editing} spellCheck={false} /></label>
          </div>
        </div>}
        {method === "manifest" && !editing && <div className="manifest-summary"><LockSimple size={18} /><p>Contents and pull requests: read. Issue comments: write.</p></div>}
        <div className="connection-actions"><button type="button" className="quiet-button" onClick={reset}>Cancel</button><button className="primary-button" disabled={!!busyID || !name.trim() || !webURL.trim() || !apiURL.trim() || (!editing && eventDelivery === "relay" && !relayServerID) || (method === "manifest" && !editing && (!ownerType || (ownerType === "organization" && !owner.trim()))) || ((method === "manual" || !!editing) && (!appID || (!editing && (!privateKey.trim() || (eventDelivery !== "none" && !webhookSecret.trim()))))) }>{busyID ? "Working..." : method === "manifest" && !editing ? "Continue to GitHub" : editing ? "Save connection" : "Add connection"}</button></div>
      </form>
	</section> : creatingSecretStore ? <SecretStoresSection stores={secretStores} secrets={overview.secrets} networks={privateNetworks} creating editing={editingSecretStore} onCreatingChange={setCreatingSecretStore} onEditingChange={setEditingSecretStore} onChanged={onChanged} /> : creatingPrivateNetwork ? <PrivateNetworksSection networks={privateNetworks} stores={secretStores} githubApps={overview.githubApps} creating editing={editingPrivateNetwork} initialDriver={privateNetworkDriver} onCreatingChange={setCreatingPrivateNetwork} onEditingChange={setEditingPrivateNetwork} onChanged={onChanged} /> : hasConnections ? <div className="connections-inventory">
      <section className="connection-provider-group" aria-labelledby="github-connections-title">
        <header className="connection-provider-header"><span className="connection-provider-icon github"><GithubLogo size={19} weight="fill" /></span><div><h2 id="github-connections-title">GitHub Apps</h2><span>Repositories and events</span></div><strong>{overview.githubApps.length}</strong></header>
        {overview.githubApps.length ? <div className="connection-list">{overview.githubApps.map((connection) => {
      const installURL = githubAppInstallURL(connection);
      const settingsURL = githubAppSettingsURL(connection);
      const installationSettingsURL = githubInstallationSettingsURL(connection);
      const choices = installations[connection.id] || [];
      const installedRepositories = repositories[connection.id] || [];
      const visibleRepositories = installedRepositories.filter((repository) => repository.fullName.toLowerCase().includes(repositoryQuery.trim().toLowerCase()));
      return <section className="connection-row" key={connection.id}>
        <div className="connection-identity"><span className="github-mark"><GithubLogo size={21} weight="fill" /></span><div><strong>{connection.name}</strong><small>{new URL(connection.webUrl).host}{connection.registrationOwner ? " / " + connection.registrationOwner : ""}{connection.privateNetworkId ? ` · via ${edgeNetworks.find((network) => network.id === connection.privateNetworkId)?.name ?? "edge"}` : ""}</small></div></div>
        <div className="connection-metadata"><div><span>App ID</span><code>{connection.appId}</code></div><div><span>Installation</span><strong>{connection.installationId || "Not installed"}</strong></div><div><span>Events</span><strong>{!connection.webhookUrl ? "Disabled" : connection.relayWebhookId ? "Relay" : "Direct"}</strong></div><div><span>Status</span><StatusLabel state={connection.state} /></div></div>
        <div className="connection-row-actions">
          {connection.state === "needs_installation" && installURL && <a className="table-action primary-link" href={installURL}>Install App<ArrowRight size={14} /></a>}
          {connection.state === "needs_installation" && <button className="table-action" disabled={busyID === connection.id} onClick={() => void findInstallations(connection)}><ArrowClockwise size={15} />Find installation</button>}
          {connection.installationId ? <button className="table-action" disabled={busyID === connection.id} onClick={() => void verify(connection)}><CheckCircle size={15} />Verify</button> : null}
          {connection.installationId ? <button className="table-action" disabled={busyID === connection.id} aria-expanded={repositoryOpen === connection.id} onClick={() => void toggleRepositories(connection)}><FolderSimple size={15} />Repositories</button> : null}
          <a className="table-action" href={connection.installationId ? installationSettingsURL : settingsURL} target="_blank" rel="noreferrer">Manage access<ArrowSquareOut size={14} /></a>
          <button className="table-action" onClick={() => edit(connection)}><PencilSimple size={15} />Edit</button>
          <button className="delete-action" onClick={() => setConfirmDelete(connection.id)}><Trash size={15} />Remove</button>
        </div>
        {confirmDelete === connection.id && <div className="connection-remove-warning" role="alert"><WarningCircle size={19} weight="fill" /><div><strong>Remove from Dispatch?</strong><p>The GitHub App and its reserved name will remain in GitHub. Delete it from GitHub settings if you no longer need it.</p></div><a className="table-action" href={settingsURL} target="_blank" rel="noreferrer">Open GitHub settings<ArrowSquareOut size={14} /></a><button className="quiet-button" onClick={() => setConfirmDelete("")}>Cancel</button><button className="danger-button" disabled={busyID === connection.id} onClick={() => void remove(connection)}>{busyID === connection.id ? "Removing..." : "Remove from Dispatch"}</button></div>}
        {choices.length > 0 && <div className="installation-picker"><div><strong>Choose an installation</strong></div>{choices.map((item) => <button type="button" key={item.id} onClick={() => void selectInstallation(connection, item.id)}><span>{item.account}</span><small>{item.target} / {item.id}</small><ArrowRight size={14} /></button>)}</div>}
        {repositoryOpen === connection.id && <div className="connection-repositories">
          <div className="repository-panel-head"><div><strong>Repository access</strong></div><div>{connection.webhookUrl && <button className="table-action" onClick={() => void copyWebhook(connection)}>{copied === connection.id ? <Check size={15} /> : <Copy size={15} />}{copied === connection.id ? "Copied" : "Copy endpoint"}</button>}<a className="table-action primary-link" href={installationSettingsURL} target="_blank" rel="noreferrer">Add repositories<ArrowSquareOut size={14} /></a></div></div>
          {installedRepositories.length > 6 && <label className="repository-search"><span className="sr-only">Filter repositories</span><input value={repositoryQuery} onChange={(event) => setRepositoryQuery(event.target.value)} placeholder="Filter repositories" /></label>}
          {busyID === connection.id ? <div className="repository-access-empty"><strong>Loading repositories</strong></div> : installedRepositories.length && visibleRepositories.length ? <div className="repository-access-list">{visibleRepositories.map((repository) => <a key={repository.id} href={repository.webUrl} target="_blank" rel="noreferrer"><span><strong>{repository.fullName}</strong><small>{repository.private ? "Private" : "Public"}</small></span><code>{repository.defaultBranch || "default branch"}</code><ArrowSquareOut size={13} /></a>)}</div> : <div className="repository-access-empty"><strong>{installedRepositories.length ? "No matching repositories" : "No repositories selected"}</strong><span>{installedRepositories.length ? "Try another name." : "Choose repository access in GitHub."}</span></div>}
        </div>}
      </section>;
    })}</div> : <div className="connection-provider-empty">No GitHub Apps</div>}
      </section>
		<SecretStoresSection stores={secretStores} secrets={overview.secrets} networks={privateNetworks} creating={false} editing={editingSecretStore} onCreatingChange={setCreatingSecretStore} onEditingChange={setEditingSecretStore} onChanged={onChanged} />
		<PrivateNetworksSection networks={privateNetworks} stores={secretStores} githubApps={overview.githubApps} creating={false} editing={editingPrivateNetwork} initialDriver={privateNetworkDriver} onCreatingChange={setCreatingPrivateNetwork} onEditingChange={setEditingPrivateNetwork} onChanged={onChanged} />
	</div> : <ConnectionsEmpty onGitHub={() => setCreating(true)} onSecretStore={() => { setEditingSecretStore(null); setCreatingSecretStore(true); }} onPrivateNetwork={(driver) => { setPrivateNetworkDriver(driver); setEditingPrivateNetwork(null); setCreatingPrivateNetwork(true); }} />}
  </div>;
}

function ConnectionAddMenu({ onGitHub, onSecretStore, onPrivateNetwork }: { onGitHub: () => void; onSecretStore: () => void; onPrivateNetwork: (driver: "dispatch_agent" | "laneway_connector") => void }) {
  return <DropdownMenu.Root>
    <DropdownMenu.Trigger asChild><button className="primary-button add-resource-menu" type="button"><Plus size={16} weight="bold" />Add connection</button></DropdownMenu.Trigger>
    <DropdownMenu.Portal><DropdownMenu.Content className="action-menu-list add-resource-options" align="end" sideOffset={6} collisionPadding={12}>
      <DropdownMenu.Label className="add-resource-group-label">Providers</DropdownMenu.Label>
      <DropdownMenu.Item className="action-menu-item" onSelect={onGitHub}><GithubLogo size={17} weight="fill" /><span><strong>GitHub App</strong><small>Source control</small></span></DropdownMenu.Item>
      <DropdownMenu.Item className="action-menu-item" onSelect={onSecretStore}><Cloud size={17} weight="fill" /><span><strong>Secret store</strong><small>External secrets</small></span></DropdownMenu.Item>
      <DropdownMenu.Separator className="action-menu-separator" />
      <DropdownMenu.Label className="add-resource-group-label">Network</DropdownMenu.Label>
		<DropdownMenu.Item className="action-menu-item" onSelect={() => onPrivateNetwork("dispatch_agent")}><PlugsConnected size={17} /><span><strong>Edge node</strong><small>Private service access</small></span></DropdownMenu.Item>
		<DropdownMenu.Item className="action-menu-item" onSelect={() => onPrivateNetwork("laneway_connector")}><PlugsConnected size={17} /><span><strong>Laneway Connector</strong><small>Private Dispatch access</small></span></DropdownMenu.Item>
    </DropdownMenu.Content></DropdownMenu.Portal>
  </DropdownMenu.Root>;
}

function ConnectionsEmpty({ onGitHub, onSecretStore, onPrivateNetwork }: { onGitHub: () => void; onSecretStore: () => void; onPrivateNetwork: (driver: "dispatch_agent" | "laneway_connector") => void }) {
  return <section className="connections-empty" aria-labelledby="connections-empty-title">
		<header className="connections-empty-head"><span><PlugsConnected size={20} /></span><div><h2 id="connections-empty-title">No connections</h2><p>Add a provider or edge route.</p></div></header>
		<div className="connection-empty-groups">
			<section className="connection-empty-group" aria-labelledby="provider-connections-title"><h3 id="provider-connections-title">Providers</h3><div>
				<button type="button" onClick={onGitHub}><span className="connection-provider-icon github"><GithubLogo size={18} weight="fill" /></span><span><strong>GitHub App</strong><small>Source control</small></span><ArrowRight size={15} /></button>
				<button type="button" onClick={onSecretStore}><span className="connection-provider-icon cloud"><Cloud size={18} weight="fill" /></span><span><strong>Secret store</strong><small>External secrets</small></span><ArrowRight size={15} /></button>
			</div></section>
			<section className="connection-empty-group" aria-labelledby="network-connections-title"><h3 id="network-connections-title">Network</h3><div>
				<button type="button" onClick={() => onPrivateNetwork("dispatch_agent")}><span className="connection-provider-icon network"><PlugsConnected size={18} /></span><span><strong>Edge node</strong><small>Private service access</small></span><ArrowRight size={15} /></button>
				<button type="button" onClick={() => onPrivateNetwork("laneway_connector")}><span className="connection-provider-icon network"><PlugsConnected size={18} /></span><span><strong>Laneway Connector</strong><small>Private Dispatch access</small></span><ArrowRight size={15} /></button>
			</div></section>
		</div>
  </section>;
}

function SecretStoresSection({ stores, secrets, networks, creating, editing, onCreatingChange, onEditingChange, onChanged }: { stores: SecretStore[]; secrets: Secret[]; networks: PrivateNetwork[]; creating: boolean; editing: SecretStore | null; onCreatingChange: (creating: boolean) => void; onEditingChange: (store: SecretStore | null) => void; onChanged: () => Promise<void> }) {
  const [name, setName] = useState(() => editing?.name ?? "");
  const [serviceURL, setServiceURL] = useState(() => editing?.config.serviceUrl ?? "");
  const [iamURL, setIAMURL] = useState(() => editing?.config.iamUrl ?? "https://iam.cloud.ibm.com");
  const [apiKey, setAPIKey] = useState("");
	const [privateNetworkID, setPrivateNetworkID] = useState(() => editing?.config.privateNetworkId ?? "");
	const [serviceAddress, setServiceAddress] = useState(() => editing?.config.serviceAddress ?? "");
	const [iamAddress, setIAMAddress] = useState(() => editing?.config.iamAddress ?? "");
  const [busyID, setBusyID] = useState("");
  const [confirmDelete, setConfirmDelete] = useState("");
  const [error, setError] = useState("");

  function open(store?: SecretStore) {
    onEditingChange(store ?? null);
    onCreatingChange(true);
    setName(store?.name ?? "");
    setServiceURL(store?.config.serviceUrl ?? "");
    setIAMURL(store?.config.iamUrl ?? "https://iam.cloud.ibm.com");
    setAPIKey("");
		setPrivateNetworkID(store?.config.privateNetworkId ?? "");
		setServiceAddress(store?.config.serviceAddress ?? "");
		setIAMAddress(store?.config.iamAddress ?? "");
    setError("");
  }

  function close() {
    onEditingChange(null);
    onCreatingChange(false);
    setAPIKey("");
    setError("");
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusyID(editing?.id ?? "create");
    setError("");
    try {
		const body = { name, provider: "ibm_cloud_secrets_manager", serviceUrl: serviceURL, iamUrl: iamURL, privateNetworkId: privateNetworkID, serviceAddress, iamAddress, ...(apiKey ? { apiKey } : {}) };
      if (editing) await api.updateSecretStore(editing.id, body);
      else await api.createSecretStore({ ...body, apiKey });
      await onChanged();
      close();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function verify(store: SecretStore) {
    setBusyID(store.id);
    setError("");
    try {
      await api.verifySecretStore(store.id);
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function remove(store: SecretStore) {
    setBusyID(store.id);
    setError("");
    try {
      await api.deleteSecretStore(store.id);
      setConfirmDelete("");
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  if (creating) return <section className="inline-create connection-editor secret-store-editor" aria-labelledby="secret-store-title">
    <header><div><h2 id="secret-store-title">{editing ? "Edit secret store" : "Add secret store"}</h2></div></header>
    {error && <p className="form-error" role="alert">{error}</p>}
    <form className="secret-store-form" onSubmit={save} aria-busy={!!busyID}>
      <div className="secret-store-provider"><span><Cloud size={19} weight="fill" /></span><div><strong>IBM Cloud Secrets Manager</strong></div></div>
      <div className="secret-store-grid">
        <label><span>Name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="Production secrets" required maxLength={80} /></label>
        <label><span>Service URL</span><input type="url" value={serviceURL} onChange={(event) => setServiceURL(event.target.value)} placeholder="https://…secrets-manager.appdomain.cloud" required spellCheck={false} /></label>
        <label className="secret-store-api-key"><span>IBM Cloud API key</span><input type="password" value={apiKey} onChange={(event) => setAPIKey(event.target.value)} placeholder={editing ? "Leave blank to keep the key" : "API key"} required={!editing} autoComplete="new-password" /></label>
      </div>
		<div className="secret-store-access">
			<label><span>Route</span><select value={privateNetworkID} onChange={(event) => { setPrivateNetworkID(event.target.value); if (!event.target.value) { setServiceAddress(""); setIAMAddress(""); } }}><option value="">Direct</option>{networks.filter((network) => network.driver !== "laneway_connector").map((network) => <option key={network.id} value={network.id}>{network.driver === "dispatch_agent" ? "Edge · " : "Laneway · "}{network.name}{network.state === "ready" ? "" : ` (${network.state})`}</option>)}</select></label>
			{networks.find((network) => network.id === privateNetworkID)?.driver === "laneway" && <><label><span>Secrets Manager IP</span><input value={serviceAddress} onChange={(event) => setServiceAddress(event.target.value)} placeholder="192.0.2.20" inputMode="decimal" spellCheck={false} /></label><label><span>IAM IP</span><input value={iamAddress} onChange={(event) => setIAMAddress(event.target.value)} placeholder="Optional" inputMode="decimal" spellCheck={false} /></label></>}
		</div>
      <details className="connection-advanced"><summary>IAM endpoint</summary><label><span>IAM URL</span><input type="url" value={iamURL} onChange={(event) => setIAMURL(event.target.value)} required spellCheck={false} /></label></details>
      <div className="connection-actions"><button type="button" className="quiet-button" onClick={close}>Cancel</button><button className="primary-button" disabled={!!busyID || !name.trim() || !serviceURL.trim() || (!editing && !apiKey.trim())}>{busyID ? "Connecting..." : editing ? "Save connection" : "Connect store"}</button></div>
    </form>
  </section>;

  return <section className="connection-provider-group secret-store-section" aria-labelledby="secret-store-title">
    <header className="connection-provider-header"><span className="connection-provider-icon cloud"><Cloud size={19} weight="fill" /></span><div><h2 id="secret-store-title">Secret stores</h2><span>External credentials</span></div><strong>{stores.length}</strong></header>
    {error && <p className="form-error connection-group-error" role="alert">{error}</p>}
	{stores.length ? <div className="resource-table-wrap"><table className="resource-table secret-store-table"><thead><tr><th>Store</th><th>Provider</th><th>Network</th><th>Secrets</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{stores.map((store) => <tr key={store.id}><td data-label="Store"><strong>{store.name}</strong></td><td data-label="Provider">IBM Cloud</td><td data-label="Network">{networks.find((network) => network.id === store.config.privateNetworkId)?.name ?? "Direct"}</td><td data-label="Secrets">{secrets.filter((secret) => secret.externalStoreId === store.id).length}</td><td data-label="Status"><StatusLabel state={store.state} /></td><td className="row-actions">{confirmDelete === store.id ? <div className="inline-confirm"><span>Remove?</span><button type="button" onClick={() => setConfirmDelete("")}>Cancel</button><button type="button" className="danger" disabled={busyID === store.id} onClick={() => void remove(store)}>Remove</button></div> : <div className="table-icon-actions"><TableIconAction label={`Verify ${store.name}`} tooltip="Verify" onClick={() => void verify(store)}><ArrowClockwise size={16} /></TableIconAction><TableIconAction label={`Edit ${store.name}`} tooltip="Edit" onClick={() => open(store)}><PencilSimple size={16} /></TableIconAction><TableIconAction label={`Delete ${store.name}`} tooltip="Delete" danger onClick={() => setConfirmDelete(store.id)}><Trash size={16} /></TableIconAction></div>}</td></tr>)}</tbody></table></div> : <div className="connection-provider-empty">No secret stores</div>}
  </section>;
}

function PrivateNetworksSection({ networks, stores, githubApps, creating, editing, initialDriver, onCreatingChange, onEditingChange, onChanged }: { networks: PrivateNetwork[]; stores: SecretStore[]; githubApps: GitHubAppConnection[]; creating: boolean; editing: PrivateNetwork | null; initialDriver: "dispatch_agent" | "laneway_connector"; onCreatingChange: (creating: boolean) => void; onEditingChange: (network: PrivateNetwork | null) => void; onChanged: () => Promise<void> }) {
	const [name, setName] = useState(() => editing?.name ?? "");
	const [driver, setDriver] = useState<"dispatch_agent" | "laneway_connector">(() => editing?.driver === "laneway_connector" ? "laneway_connector" : initialDriver);
	const [authority, setAuthority] = useState(() => editing?.config.authority ?? "");
	const [routePrefix, setRoutePrefix] = useState(() => editing?.config.route ?? "");
	const [installNode, setInstallNode] = useState<PrivateNetwork | null>(null);
	const [bootstrapCommand, setBootstrapCommand] = useState("");
	const [docker, setDocker] = useState(true);
	const [copied, setCopied] = useState("");
	const [busyID, setBusyID] = useState("");
	const [confirmDelete, setConfirmDelete] = useState("");
	const [error, setError] = useState("");

	function open(network?: PrivateNetwork, install = false) {
		const nextDriver = network?.driver === "laneway_connector" ? "laneway_connector" : "dispatch_agent";
		onEditingChange(network ?? null);
		onCreatingChange(true);
		setName(network?.name ?? "");
		setDriver(nextDriver);
		setAuthority(network?.config.authority ?? "");
		setRoutePrefix(network?.config.route ?? "");
		setInstallNode(install ? network ?? null : null);
		setBootstrapCommand("");
		setError("");
	}

	function close() {
		onEditingChange(null);
		onCreatingChange(false);
		setInstallNode(null);
		setBootstrapCommand("");
		setError("");
	}

	async function save(event: FormEvent) {
		event.preventDefault();
		setBusyID(editing?.id ?? "create");
		setError("");
		try {
			const selectedDriver = editing?.driver ?? driver;
			const body = { name, driver: selectedDriver, socketPath: editing?.config.socketPath, authority: selectedDriver === "laneway_connector" ? authority : undefined, route: selectedDriver === "laneway_connector" ? routePrefix : undefined };
			const saved = editing ? await api.updatePrivateNetwork(editing.id, body) : await api.createPrivateNetwork(body);
			await onChanged();
			if (!editing && (saved.enrollmentToken || saved.driver === "laneway_connector")) setInstallNode(saved);
			else close();
		} catch (cause) {
			setError((cause as Error).message);
		} finally {
			setBusyID("");
		}
	}

	async function rotate(network: PrivateNetwork) {
		setBusyID(network.id);
		setError("");
		try {
			const saved = await api.rotatePrivateNetworkToken(network.id);
			setInstallNode(saved);
			onEditingChange(network);
			onCreatingChange(true);
			setName(network.name);
			setDriver("dispatch_agent");
			await onChanged();
		} catch (cause) { setError((cause as Error).message); }
		finally { setBusyID(""); }
	}

	async function installConnector() {
		if (!installNode) return;
		setBusyID(installNode.id);
		setError("");
		try {
			const saved = await api.installLanewayConnector(installNode.id, bootstrapCommand);
			setInstallNode(saved);
			setBootstrapCommand("");
			await onChanged();
		} catch (cause) { setError((cause as Error).message); }
		finally { setBusyID(""); }
	}

	const installCommand = installNode?.enrollmentToken ? `curl -fsSL '${window.location.origin}/edge/install.sh' | sudo env DISPATCH_EDGE_CONTROLLER_URL='${window.location.origin}' DISPATCH_EDGE_NODE_ID='${installNode.id}' DISPATCH_EDGE_TOKEN='${installNode.enrollmentToken}' DISPATCH_EDGE_INSTALL_MODE='${docker ? "docker" : "systemd"}' DISPATCH_EDGE_DOWNLOAD_BASE='${window.location.origin}' sh` : "";
	const connectorName = `dispatch-${(installNode?.name ?? name).toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "") || "controller"}`;
	const inviteCommand = `sudo laneway control invite --name '${connectorName}' --docker --connector --bootstrap`;
	const installedConnectorName = installNode?.config.containerName?.replace(/^laneway-connector-/, "") ?? connectorName;
	const routeCommand = installNode?.config.containerName ? `sudo laneway control route add --connector '${installedConnectorName}' --to '${installNode.config.route}' --allow 'dispatch-edge'\nsudo laneway control user-token --name 'dispatch-edge'` : "";

	async function copy(value: string, target: string) {
		try {
			await navigator.clipboard.writeText(value);
			setCopied(target);
			window.setTimeout(() => setCopied((current) => current === target ? "" : current), 1600);
		} catch { setError("Could not copy the command."); }
	}

	async function verify(network: PrivateNetwork) {
		setBusyID(network.id);
		setError("");
		try { await api.verifyPrivateNetwork(network.id); await onChanged(); }
		catch (cause) { setError((cause as Error).message); await onChanged(); }
		finally { setBusyID(""); }
	}

	async function remove(network: PrivateNetwork) {
		setBusyID(network.id);
		setError("");
		try { await api.deletePrivateNetwork(network.id); setConfirmDelete(""); await onChanged(); }
		catch (cause) { setError((cause as Error).message); }
		finally { setBusyID(""); }
	}

	if (creating) {
		const installingConnector = installNode?.driver === "laneway_connector";
		return <section className="inline-create connection-editor private-network-editor" aria-labelledby="private-network-title">
			<header><div><h2 id="private-network-title">{installNode ? installingConnector ? "Connect Laneway" : "Deploy edge node" : editing ? `Edit ${editing.driver === "laneway_connector" ? "Connector" : "edge node"}` : driver === "laneway_connector" ? "Add Laneway Connector" : "Add edge node"}</h2></div></header>
			{error && <p className="form-error" role="alert">{error}</p>}
			{installNode && installingConnector ? <div className="laneway-install">
				<div className="edge-install-summary"><span><PlugsConnected size={19} /></span><div><strong>{installNode.name}</strong><small>{installNode.config.containerName ? "Connector installed on this Dispatch host" : "One-time enrollment"}</small></div></div>
				{installNode.config.containerName ? <><div className="connector-ready"><CheckCircle size={18} weight="fill" /><div><strong>{installNode.config.containerName}</strong><span>{installNode.state === "ready" ? "Connected" : "Waiting for the connector check."}</span></div></div><div className="laneway-finish"><strong>Finish in Laneway</strong><small>Run this on the control plane. It publishes only the Dispatch route and creates the edge login.</small><div className="compact-command"><code>{routeCommand}</code><button type="button" aria-label="Copy route commands" onClick={() => void copy(routeCommand, "route")}><Copy size={15} />{copied === "route" ? "Copied" : "Copy"}</button></div></div></> : <>
					<ol className="laneway-steps">
						<li><span>1</span><div><strong>Create an invite</strong><small>Run this on the Laneway control plane.</small><div className="compact-command"><code>{inviteCommand}</code><button type="button" aria-label="Copy invite command" onClick={() => void copy(inviteCommand, "invite")}><Copy size={15} />{copied === "invite" ? "Copied" : "Copy"}</button></div></div></li>
						<li><span>2</span><label><strong>Paste the result</strong><small>The command expires after ten minutes and is never saved.</small><textarea aria-label="Laneway bootstrap command" value={bootstrapCommand} onChange={(event) => setBootstrapCommand(event.target.value)} placeholder="curl --fail ... | sudo bash ..." spellCheck={false} /></label></li>
					</ol>
					<div className="edge-install-note"><LockSimple size={16} /><span>Dispatch accepts only Laneway's encrypted Connector bootstrap format.</span></div>
				</>}
				<div className="connection-actions"><button type="button" className="quiet-button" onClick={close}>{installNode.config.containerName ? "Close" : "Cancel"}</button>{!installNode.config.containerName && <button type="button" className="primary-button" disabled={!!busyID || !bootstrapCommand.trim()} onClick={() => void installConnector()}>{busyID ? "Deploying..." : "Deploy here"}</button>}</div>
			</div> : installNode ? <div className="edge-install">
				<div className="edge-install-summary"><span><PlugsConnected size={19} /></span><div><strong>{installNode.name}</strong><small>Waiting for its first outbound connection</small></div></div>
				<label className="edge-runtime"><input type="checkbox" checked={docker} onChange={(event) => setDocker(event.target.checked)} /><span><strong>Run with Docker Compose</strong><small>Turn off to install a systemd service.</small></span></label>
				<div className="edge-command"><code>{installCommand}</code><button type="button" className="quiet-button" onClick={() => void copy(installCommand, "edge")}><Copy size={16} />{copied === "edge" ? "Copied" : "Copy command"}</button></div>
				<div className="edge-install-note"><LockSimple size={16} /><span>The node connects to Dispatch. No inbound port is required.</span></div>
				<div className="connection-actions"><button type="button" className="primary-button" onClick={close}>Done</button></div>
			</div> : <form className="private-network-form" onSubmit={save} aria-busy={!!busyID}>
				{!editing && <div className="connection-method network-method" role="radiogroup" aria-label="Private network type">
					<button type="button" role="radio" aria-checked={driver === "dispatch_agent"} className={driver === "dispatch_agent" ? "active" : ""} onClick={() => setDriver("dispatch_agent")}><PlugsConnected size={19} /><span><strong>Edge node</strong><small>Runs requests near private services.</small></span></button>
					<button type="button" role="radio" aria-checked={driver === "laneway_connector"} className={driver === "laneway_connector" ? "active" : ""} onClick={() => setDriver("laneway_connector")}><PlugsConnected size={19} /><span><strong>Laneway Connector</strong><small>Makes this Dispatch reachable.</small></span></button>
				</div>}
				<div className="private-network-provider"><span><PlugsConnected size={19} /></span><div><strong>{(editing?.driver ?? driver) === "laneway_connector" ? "Laneway Connector" : "Dispatch Edge"}</strong><small>{(editing?.driver ?? driver) === "laneway_connector" ? "Installed on this controller host" : "Installed near private services"}</small></div></div>
				<div className="private-network-grid"><label><span>Name</span><input aria-label="Name" value={name} onChange={(event) => setName(event.target.value)} placeholder={(editing?.driver ?? driver) === "laneway_connector" ? "Dispatch network" : "Private network"} required maxLength={80} /></label>{(editing?.driver ?? driver) === "laneway_connector" && <><label><span>Laneway URL</span><input aria-label="Laneway URL" type="url" value={authority} onChange={(event) => setAuthority(event.target.value)} placeholder="https://lane.example.com" required spellCheck={false} /></label><label><span>Dispatch private IP or CIDR</span><input aria-label="Dispatch private IP or CIDR" value={routePrefix} onChange={(event) => setRoutePrefix(event.target.value)} placeholder="192.0.2.10/32" required spellCheck={false} /><small>Only this address is published to edge hosts.</small></label></>}</div>
				<div className="edge-route-preview"><span>{(editing?.driver ?? driver) === "laneway_connector" ? "Dispatch will run the Connector through its local Docker socket." : "Connections can route through this node after it comes online."}</span><strong>{(editing?.driver ?? driver) === "laneway_connector" ? "Managed here" : "Outbound only"}</strong></div>
				<div className="connection-actions"><button type="button" className="quiet-button" onClick={close}>Cancel</button><button className="primary-button" disabled={!!busyID || !name.trim() || ((editing?.driver ?? driver) === "laneway_connector" && (!authority.trim() || !routePrefix.trim()))}>{busyID ? "Saving..." : editing ? "Save" : "Continue"}</button></div>
			</form>}
		</section>;
	}

	return <section className="connection-provider-group private-network-section" aria-labelledby="private-networks-title">
		<header className="connection-provider-header"><span className="connection-provider-icon network"><PlugsConnected size={19} /></span><div><h2 id="private-networks-title">Private network</h2><span>Edge workers and controller access</span></div><strong>{networks.length}</strong></header>
		{error && <p className="form-error connection-group-error" role="alert">{error}</p>}
		{networks.length ? <div className="resource-table-wrap"><table className="resource-table private-network-table"><thead><tr><th>Name</th><th>Role</th><th>Endpoint</th><th>Connections</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead><tbody>{networks.map((network) => {
			const usage = stores.filter((store) => store.config.privateNetworkId === network.id).length + githubApps.filter((connection) => connection.privateNetworkId === network.id).length;
			const connector = network.driver === "laneway_connector";
			return <tr key={network.id}><td data-label="Name"><strong>{network.name}</strong></td><td data-label="Role">{connector ? "Dispatch Connector" : network.driver === "dispatch_agent" ? "Edge worker" : "Laneway client"}</td><td data-label="Endpoint"><span className="connection">{connector ? network.config.authority : network.driver === "dispatch_agent" ? relative(network.details.lastSeenAt) : network.details.path || "None"}</span></td><td data-label="Connections">{connector ? "Controller access" : usage}</td><td data-label="Status"><StatusLabel state={network.state} /></td><td className="row-actions">{confirmDelete === network.id ? <div className="inline-confirm"><span>Remove?</span><button type="button" onClick={() => setConfirmDelete("")}>Cancel</button><button type="button" className="danger" disabled={busyID === network.id} onClick={() => void remove(network)}>Remove</button></div> : <div className="table-icon-actions"><TableIconAction label={`Verify ${network.name}`} tooltip="Verify" onClick={() => void verify(network)}><ArrowClockwise size={16} /></TableIconAction>{network.driver === "dispatch_agent" && <TableIconAction label={`Create install token for ${network.name}`} tooltip="Install" onClick={() => void rotate(network)}><Key size={16} /></TableIconAction>}{connector && !network.config.containerName && <TableIconAction label={`Install ${network.name}`} tooltip="Install" onClick={() => open(network, true)}><Key size={16} /></TableIconAction>}<TableIconAction label={`Edit ${network.name}`} tooltip="Edit" onClick={() => open(network)}><PencilSimple size={16} /></TableIconAction><TableIconAction label={`Delete ${network.name}`} tooltip="Delete" danger onClick={() => setConfirmDelete(network.id)}><Trash size={16} /></TableIconAction></div>}</td></tr>;
		})}</tbody></table></div> : <div className="connection-provider-empty">No private network connections</div>}
	</section>;
}
