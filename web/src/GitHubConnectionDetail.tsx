import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowClockwise, ArrowLeft, ArrowSquareOut, GithubLogo } from "@phosphor-icons/react";
import {
  api,
  GitHubAppConnection,
  GitHubAppInstallation,
  GitHubAppVerification,
  GitHubRepository,
  Overview,
} from "./api";
import { routePath, shouldHandleNavigation } from "./routes";
import { StatusLabel } from "./ResourceTable";

type Watch = { repository: string; source: string; kind: string };

function configuredWatches(overview: Overview, connectionID: string): Watch[] {
  const watches: Watch[] = [];
  for (const source of overview.configSources ?? []) {
    if (source.githubAppId === connectionID && source.active) {
      watches.push({ repository: source.repository, source: source.name, kind: "Configuration sync" });
    }
  }
  for (const trigger of overview.eventTriggers) {
    if (trigger.githubAppId === connectionID && trigger.enabled) {
      watches.push({ repository: trigger.repository, source: trigger.command, kind: "Application preview" });
    }
  }
  for (const group of overview.previewGroups) {
    if (group.githubAppId !== connectionID || !group.enabled) continue;
    for (const component of group.components.filter((item) => item.entrypoint)) {
      watches.push({ repository: component.repository, source: group.name, kind: "Preview group" });
    }
  }
  for (const template of overview.workflowPreviewTemplates ?? []) {
    if (template.githubAppId !== connectionID || !template.active) continue;
    for (const repository of template.watchRepositories?.length ? template.watchRepositories : [template.repository]) {
      watches.push({ repository, source: template.name, kind: "PR preview" });
    }
  }
  return watches;
}

function repositoryOwner(repository: GitHubRepository) {
  return (repository.owner || repository.fullName.split("/")[0] || "").toLowerCase();
}

function accessLabel(installation: GitHubAppInstallation) {
  if (installation.repositorySelection === "all") return "All repositories";
  if (installation.repositorySelection === "selected") return "Selected repositories";
  return "Repository selection unknown";
}

function missingAccess(verification: GitHubAppVerification) {
  return [
    ...(verification.missingAppPermissions ?? []),
    ...(verification.missingInstallationPermissions ?? []),
    ...(verification.missingTokenPermissions ?? []),
  ];
}

export function GitHubConnectionDetail({ connection, overview, installURL, settingsURL, onBack, onEdit, onChanged }: {
  connection: GitHubAppConnection;
  overview: Overview;
  installURL: string;
  settingsURL: string;
  onBack: () => void;
  onEdit: () => void;
  onChanged: () => Promise<void>;
}) {
  const [installations, setInstallations] = useState<GitHubAppInstallation[] | null>(null);
  const [repositories, setRepositories] = useState<GitHubRepository[] | null>(null);
  const [installationError, setInstallationError] = useState("");
  const [repositoryError, setRepositoryError] = useState("");
  const [verification, setVerification] = useState<GitHubAppVerification | null>(null);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [query, setQuery] = useState("");
  const [expandedAccounts, setExpandedAccounts] = useState<number[]>([]);
  const requestID = useRef(0);
  const watches = useMemo(() => configuredWatches(overview, connection.id), [overview, connection.id]);
  const watchedRepositories = useMemo(() => {
    const groups = new Map<string, { repository: string; uses: Watch[] }>();
    for (const watch of watches) {
      const key = watch.repository.trim().toLowerCase();
      if (!key) continue;
      const existing = groups.get(key);
      if (existing) existing.uses.push(watch);
      else groups.set(key, { repository: watch.repository, uses: [watch] });
    }
    return [...groups.values()].sort((a, b) => a.repository.localeCompare(b.repository));
  }, [watches]);
  const accessibleNames = new Set((repositories ?? []).map((item) => item.fullName.toLowerCase()));
  const watchedNames = new Set(watchedRepositories.map((item) => item.repository.toLowerCase()));

  const refresh = useCallback(async () => {
    const currentRequest = ++requestID.current;
    setBusy(true);
    setError("");
    const [foundInstallations, foundRepositories] = await Promise.allSettled([
      api.githubAppInstallations(connection.id),
      api.githubAppRepositories(connection.id),
    ]);
    if (currentRequest !== requestID.current) return;
    if (foundInstallations.status === "fulfilled") {
      setInstallations(foundInstallations.value);
      setInstallationError("");
    } else {
      setInstallations(null);
      setInstallationError(foundInstallations.reason instanceof Error ? foundInstallations.reason.message : String(foundInstallations.reason));
    }
    if (foundRepositories.status === "fulfilled") {
      setRepositories(foundRepositories.value);
      setRepositoryError("");
    } else {
      setRepositories(null);
      setRepositoryError(foundRepositories.reason instanceof Error ? foundRepositories.reason.message : String(foundRepositories.reason));
    }
    setBusy(false);
  }, [connection.id]);

  useEffect(() => {
    void refresh();
    return () => { requestID.current++; };
  }, [refresh]);

  async function verify() {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const result = await api.verifyGitHubApp(connection.id);
      setVerification(result.verification);
      setMessage(missingAccess(result.verification).length ? "GitHub reports missing permissions. Review the access check below." : "GitHub access checked.");
      await onChanged();
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  }

  const installationList = installations ?? [];
  const installationSettingsURL = installationList.find((item) => item.account.toLowerCase() === verification?.installationAccount?.toLowerCase())?.webUrl || connection.installationUrl || installURL;
  const knownOwners = new Set(installationList.map((item) => item.account.toLowerCase()));
  const otherRepositories = (repositories ?? []).filter((item) => !knownOwners.has(repositoryOwner(item)));
  const eventDelivery = !connection.webhookUrl ? "Polling" : connection.relayWebhookId ? "Webhook via relay" : "Direct webhook";

  return <div className="github-detail">
    <a className="github-detail-back" href={routePath({ view: "connections" })} onClick={(event) => {
      if (!shouldHandleNavigation(event)) return;
      event.preventDefault();
      onBack();
    }}><ArrowLeft size={16} />Connections</a>
    <header className="github-detail-heading">
      <div className="github-detail-title"><span className="github-mark"><GithubLogo size={22} weight="fill" /></span><div><h1>{connection.name}</h1><p>{new URL(connection.webUrl).host}</p></div></div>
      <StatusLabel state={connection.state} />
    </header>
    <div className="github-detail-toolbar">
      <button className="quiet-button" disabled={busy} onClick={() => void refresh()}><ArrowClockwise size={16} />{busy ? "Checking…" : "Refresh access"}</button>
      <button className="quiet-button" disabled={busy} onClick={() => void verify()}>Verify permissions</button>
      <button className="quiet-button" onClick={onEdit}>Edit connection</button>
      <a className="quiet-button" href={settingsURL} target="_blank" rel="noreferrer">App settings<ArrowSquareOut size={15} /></a>
    </div>
    {message && <p className="github-detail-message" role="status">{message}</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <dl className="github-detail-facts">
      <div><dt>App ID</dt><dd>{connection.appId}</dd></div>
      <div><dt>Accounts</dt><dd>{installations === null ? installationError ? "Unavailable" : "Loading" : installationList.length}</dd></div>
      <div><dt>Accessible repositories</dt><dd>{repositories === null ? repositoryError ? "Unavailable" : "Loading" : repositories.length}</dd></div>
      <div><dt>Event delivery</dt><dd>{eventDelivery}</dd></div>
    </dl>

    <section className="github-detail-section" aria-labelledby="github-installations-heading">
      <div className="github-detail-section-head"><div><h2 id="github-installations-heading">Connected accounts</h2><p>GitHub controls which repositories this App can access in each account.</p></div>{installURL && <a className="quiet-button" href={installURL} target="_blank" rel="noreferrer">Add account<ArrowSquareOut size={15} /></a>}</div>
      {installationError && <p className="form-error" role="alert">Could not load installations. {installationError}</p>}
      {repositoryError && <p className="form-error" role="alert">Could not check repository access. {repositoryError}</p>}
      {repositories && (repositories.length > 8 || watchedRepositories.length > 6) && <label className="github-watch-search">Filter repositories<input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="owner/repository" /></label>}
      {installations !== null && !installationList.length && !installationError && <p className="github-detail-empty">No installations found. Install the App on an account to grant repository access.</p>}
      {installationList.length > 0 && <div className="github-installation-list">{installationList.map((installation) => {
        const ownedRepositories = (repositories ?? []).filter((item) => repositoryOwner(item) === installation.account.toLowerCase());
        const matches = ownedRepositories.filter((item) => item.fullName.toLowerCase().includes(query.trim().toLowerCase()));
        const showAll = expandedAccounts.includes(installation.id) || !!query.trim();
        const visible = showAll ? matches : matches.slice(0, 8);
        return <article className="github-installation" key={installation.id}>
          <div className="github-installation-head"><div><h3>{installation.account}</h3><p>{installation.target || "GitHub account"} · {accessLabel(installation)} · {repositories === null ? "Repositories loading" : `${ownedRepositories.length} accessible`}</p></div><span className={installation.suspended || installation.missingPermissions?.length ? "github-installation-warning" : "github-installation-ready"}>{installation.suspended ? "Suspended" : installation.missingPermissions?.length ? "Needs access" : "Connected"}</span></div>
          {!!installation.missingPermissions?.length && <p className="github-installation-permissions" role="alert">Missing permissions: {installation.missingPermissions.map((gap) => `${gap.name} ${gap.required}${gap.granted ? `, has ${gap.granted}` : ""}`).join("; ")}.</p>}
          {installation.suspended && <p className="github-installation-permissions">GitHub has suspended this installation. Re-enable it before using these repositories.</p>}
          {repositories !== null && <div className="github-installation-repos">
            {visible.length ? visible.map((repository) => <a key={repository.id} href={repository.webUrl} target="_blank" rel="noreferrer"><span>{repository.fullName}<small>{watchedNames.has(repository.fullName.toLowerCase()) ? "Watched by Dispatch" : repository.private ? "Private" : "Public"}</small></span><ArrowSquareOut size={14} /></a>) : <p>{query.trim() ? "No matching repositories." : installation.suspended ? "Repositories are unavailable while suspended." : "No repositories returned for this account."}</p>}
            {!showAll && matches.length > 8 && <button className="github-show-more" onClick={() => setExpandedAccounts((current) => [...current, installation.id])}>Show all {matches.length} repositories</button>}
          </div>}
          {installation.webUrl && <a className="github-installation-manage" href={installation.webUrl} target="_blank" rel="noreferrer">Manage {installation.account} access<ArrowSquareOut size={14} /></a>}
        </article>;
      })}</div>}
      {repositories !== null && otherRepositories.length > 0 && <div className="github-other-repos"><h3>Other accessible repositories</h3><p>GitHub returned these repositories without a matching installation account.</p>{otherRepositories.filter((repository) => repository.fullName.toLowerCase().includes(query.trim().toLowerCase())).map((repository) => <a key={repository.id} href={repository.webUrl} target="_blank" rel="noreferrer">{repository.fullName}<ArrowSquareOut size={14} /></a>)}</div>}
    </section>

    <section className="github-detail-section" aria-labelledby="github-watches-heading">
      <div className="github-detail-section-head"><div><h2 id="github-watches-heading">Watched by Dispatch</h2><p>Active syncs and comment commands using this App.</p></div><strong>{watchedRepositories.length} repositories</strong></div>
      {!watchedRepositories.length ? <p className="github-detail-empty">Nothing is watching repositories through this App yet.</p> : <div className="github-watch-list">{watchedRepositories.filter((item) => item.repository.toLowerCase().includes(query.trim().toLowerCase())).map((item) => <div key={item.repository} className="github-watch-row"><div><strong>{item.repository}</strong><small>{item.uses.map((use) => `${use.kind}: ${use.source}`).join(" · ")}</small></div><span className={repositories === null ? "" : accessibleNames.has(item.repository.toLowerCase()) ? "github-watch-accessible" : "github-watch-missing"}>{repositories === null ? "Access not checked" : accessibleNames.has(item.repository.toLowerCase()) ? "App access confirmed" : "No App access"}</span></div>)}</div>}
    </section>

    {verification && <section className="github-detail-section" aria-labelledby="github-verification-heading"><div className="github-detail-section-head"><div><h2 id="github-verification-heading">Permission check</h2><p>Results from the latest GitHub check.</p></div></div><div className="github-verification">
      <p>{verification.pushSubscribed ? "Push events are enabled." : "Push events are not subscribed. Dispatch can still poll."}</p>
      {!verification.appPermissionsAvailable && <p>GitHub did not return App registration permissions.</p>}
      {verification.missingAppPermissions.map((gap) => <p key={`app-${gap.name}`}>App registration needs {gap.required} for {gap.name}; GitHub grants {gap.granted || "none"}.</p>)}
      {(!verification.appPermissionsAvailable || verification.missingAppPermissions.length > 0) && <a href={settingsURL} target="_blank" rel="noreferrer">Change App permissions<ArrowSquareOut size={14} /></a>}
      {!verification.installationPermissionsAvailable && <p>GitHub did not return account installation permissions.</p>}
      {!verification.tokenPermissionsAvailable && <p>GitHub did not return installation token permissions.</p>}
      {verification.missingInstallationPermissions.map((gap) => <p key={`installation-${gap.name}`}>Installation needs {gap.required} for {gap.name}; GitHub grants {gap.granted || "none"}.</p>)}
      {verification.missingTokenPermissions.map((gap) => <p key={`token-${gap.name}`}>Installation token needs {gap.required} for {gap.name}; GitHub grants {gap.granted || "none"}.</p>)}
      {(!verification.installationPermissionsAvailable || !verification.tokenPermissionsAvailable || verification.missingInstallationPermissions.length > 0 || verification.missingTokenPermissions.length > 0) && installationSettingsURL && <a href={installationSettingsURL} target="_blank" rel="noreferrer">Approve account access<ArrowSquareOut size={14} /></a>}
      {!missingAccess(verification).length && verification.appPermissionsAvailable && verification.installationPermissionsAvailable && verification.tokenPermissionsAvailable && <p>Required repository permissions are present.</p>}
    </div></section>}
  </div>;
}
