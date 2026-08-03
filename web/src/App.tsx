import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { api, App as AppModel, Deployment, DeploymentLog, getToken, Overview, setToken } from "./api";
import { InventorySetup, OnboardingView } from "./Onboarding";
import { groupDeployments, relative, setupStage, short, stages, stageIndex, stateStage, statusTone } from "./presentation";

type Panel = "evidence" | "deploy" | "setup";

export default function DispatchApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [selectedID, setSelectedID] = useState("");
  const [logs, setLogs] = useState<DeploymentLog[]>([]);
  const [panel, setPanel] = useState<Panel>("evidence");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [needsAuth, setNeedsAuth] = useState(false);
  const [mobileNav, setMobileNav] = useState(false);

  const load = useCallback(async (quiet = false) => {
    try {
      const next = await api.overview();
      setOverview(next); setNeedsAuth(false); setError("");
      if (!selectedID && next.deployments[0]) setSelectedID(next.deployments[0].id);
    } catch (cause) {
      const e = cause as Error & { status?: number };
      if (e.status === 401) setNeedsAuth(true); else setError(e.message);
    } finally { if (!quiet) setLoading(false); }
  }, [selectedID]);

  useEffect(() => { void load(); const timer = window.setInterval(() => void load(true), 1800); return () => window.clearInterval(timer); }, [load]);
  const selected = overview?.deployments.find((item) => item.id === selectedID) ?? overview?.deployments[0];
  useEffect(() => {
    if (!selected?.id) { setLogs([]); return; }
    let active = true;
    const refresh = async () => { try { const next = await api.logs(selected.id); if (active) setLogs(next); } catch { /* inventory error already owns recovery */ } };
    void refresh(); const timer = window.setInterval(() => void refresh(), 1200);
    return () => { active = false; window.clearInterval(timer); };
  }, [selected?.id]);

  const groups = useMemo(() => groupDeployments(overview?.deployments ?? []), [overview]);
  const currentSetupStage = overview ? setupStage(overview) : "server";
  const needsSetup = currentSetupStage !== "complete";
  const primaryLabel = currentSetupStage === "server" ? "Add server" : currentSetupStage === "project" ? "Create project" : currentSetupStage === "app" ? "Define app" : "Deploy app";
  const openInventory = () => {
    if (needsSetup) {
      document.getElementById("onboarding")?.scrollIntoView({ behavior: "smooth" });
      const fieldID = currentSetupStage === "server" ? "server-name" : currentSetupStage === "project" ? "project-name" : "application-name";
      window.requestAnimationFrame(() => document.getElementById(fieldID)?.focus());
    } else setPanel("setup");
    setMobileNav(false);
  };

  if (needsAuth) return <AuthScreen onAuthenticated={() => { setNeedsAuth(false); setLoading(true); void load(); }} />;

  return (
    <div className="shell">
      <a className="skip-link" href={needsSetup ? "#onboarding" : "#movement-board"}>Skip to {needsSetup ? "setup" : "deployment board"}</a>
      <Nav open={mobileNav} onClose={() => setMobileNav(false)} demo={overview?.demo ?? false} needsSetup={needsSetup} servers={overview?.servers.length ?? 0} apps={overview?.apps.length ?? 0} deployments={overview?.deployments.length ?? 0} onInventory={openInventory} />
      <main className="workspace">
        <header className="context-bar">
          <button className="menu-button" aria-label="Open navigation" aria-controls="primary-navigation" aria-expanded={mobileNav} onClick={() => setMobileNav(true)}><span /><span /><span /></button>
          <div className="context-cell"><span>Control plane</span><strong>{error ? "Needs attention" : "Connected"}</strong></div>
          <div className="context-cell"><span>Targets</span><strong>{overview?.servers.filter((s) => s.state === "ready").length ?? 0} ready</strong></div>
          <div className="context-cell optional"><span>Applications</span><strong>{overview?.apps.length ?? 0}</strong></div>
          <div className="context-actions">
            {overview?.demo && <span className="demo-badge"><i /> Demo data</span>}
            <button className="primary-button" onClick={() => needsSetup ? openInventory() : setPanel("deploy")}>{primaryLabel}</button>
          </div>
        </header>

        {error && <div className="error-banner" role="alert"><strong>Dispatch lost the inventory feed.</strong><span>{error}</span><button onClick={() => void load()}>Retry now</button></div>}
        {loading && <div className="workbench loading-workbench"><section className="movement"><LoadingBoard /></section><aside className="dossier"><div className="dossier-empty"><Mark /><h2>Loading inventory</h2><p>Reading targets, application specs, and deployment evidence.</p></div></aside></div>}
        {!loading && overview && needsSetup && <OnboardingView data={overview} onChanged={() => load()} />}
        {!loading && overview && !needsSetup && <div className="workbench">
          <section className="movement" id="movement-board" aria-labelledby="movement-title">
            <div className="page-heading">
              <div><h1 id="movement-title">Movement board</h1><p>Revisions ordered by urgency, with every handoff visible.</p></div>
              <button className="quiet-button" onClick={() => setPanel("setup")}>Manage inventory</button>
            </div>

            {overview.deployments.length === 0 && <EmptyBoard onDeploy={() => setPanel("deploy")} />}
            {groups.attention.length > 0 && <DeploymentGroup title="Needs attention" count={groups.attention.length} deployments={groups.attention} selectedID={selected?.id} onSelect={(id) => { setSelectedID(id); setPanel("evidence"); }} />}
            {groups.history.length > 0 && <DeploymentGroup title="Deployment history" count={groups.history.length} deployments={groups.history} selectedID={selected?.id} onSelect={(id) => { setSelectedID(id); setPanel("evidence"); }} />}
          </section>

          <aside className={`dossier ${panel !== "evidence" ? "editing" : ""}`} aria-label={panel === "evidence" ? "Deployment evidence" : "Deployment action"}>
            {panel === "evidence" && <Evidence deployment={selected} logs={logs} onCancel={async () => { if (!selected) return; try { await api.cancel(selected.id); await load(); } catch (cause) { setError((cause as Error).message); } }} />}
            {panel === "deploy" && <DeployPanel apps={overview.apps} onClose={() => setPanel("evidence")} onComplete={async (id) => { setSelectedID(id); setPanel("evidence"); await load(); }} />}
            {panel === "setup" && <SetupPanel data={overview} onClose={() => setPanel("evidence")} onChanged={() => load()} />}
          </aside>
        </div>}
      </main>
    </div>
  );
}

function Nav({ open, onClose, demo, needsSetup, servers, apps, deployments, onInventory }: { open: boolean; onClose: () => void; demo: boolean; needsSetup: boolean; servers: number; apps: number; deployments: number; onInventory: () => void }) {
  return <><div className={`nav-scrim ${open ? "visible" : ""}`} onClick={onClose} /><nav id="primary-navigation" className={`rail ${open ? "open" : ""}`} aria-label="Primary">
    <div className="wordmark"><Mark /><span>Dispatch</span><button aria-label="Close navigation" onClick={onClose}>×</button></div>
    <div className="nav-list">
      {needsSetup ? <span className="disabled" aria-disabled="true"><NavGlyph kind="movement" />Movement board<small>Locked</small></span> : <a className="active" href="#movement-board" onClick={onClose}><NavGlyph kind="movement" />Movement board</a>}
      {deployments ? <a href="#deployment-history" onClick={onClose}><NavGlyph kind="release" />Deployments<small>{deployments}</small></a> : <span className="disabled" aria-disabled="true"><NavGlyph kind="release" />Deployments<small>Empty</small></span>}
      <button onClick={onInventory}><NavGlyph kind="app" />Applications<small>{apps || "Setup"}</small></button>
      <button className={needsSetup ? "active" : ""} onClick={onInventory}><NavGlyph kind="target" />Targets<small>{servers || "Add"}</small></button>
      <span className="disabled" aria-disabled="true"><NavGlyph kind="settings" />Settings<small>Planned</small></span>
    </div>
    <div className="rail-foot"><div className="control-mark"><span className={demo ? "demo-dot" : "live-dot"} /><div><strong>Control plane</strong><span>{demo ? "Demonstration mode" : "Private controller"}</span></div></div><div className="operator"><span>DO</span><div><strong>doout</strong><small>Administrator</small></div></div></div>
  </nav></>;
}

function Mark() { return <svg viewBox="0 0 36 36" aria-hidden="true"><path d="M5 8.5 18 2l13 6.5v18L18 34 5 26.5Z" fill="none" stroke="currentColor" strokeWidth="2"/><path d="m5 8.5 13 7 13-7M18 15.5V34" fill="none" stroke="currentColor" strokeWidth="2"/><path d="m10 11 8-4 8 4-8 4Z" fill="currentColor"/></svg>; }
function NavGlyph({ kind }: { kind: string }) { return <span className={`nav-glyph nav-${kind}`} aria-hidden="true"><i /><b /></span>; }

function DeploymentGroup({ title, count, deployments, selectedID, onSelect }: { title: string; count: number; deployments: Deployment[]; selectedID?: string; onSelect: (id: string) => void }) {
  return <div className="deployment-group" id={title === "Deployment history" ? "deployment-history" : undefined}>
    <div className="group-heading"><h2>{title}</h2><span>{count}</span></div>
    <div className="strips">{deployments.map((deployment) => <DeploymentStrip key={deployment.id} deployment={deployment} selected={selectedID === deployment.id} onSelect={() => onSelect(deployment.id)} />)}</div>
  </div>;
}

function DeploymentStrip({ deployment, selected, onSelect }: { deployment: Deployment; selected: boolean; onSelect: () => void }) {
  const tone = statusTone(deployment.state), current = stageIndex(deployment.state);
  return <button className={`dispatch-strip ${tone} ${selected ? "selected" : ""}`} onClick={onSelect} aria-pressed={selected}>
    <span className="strip-status" aria-hidden="true">{tone === "success" ? "✓" : tone === "danger" ? "!" : "→"}</span>
    <span className="strip-identity"><strong>{deployment.app?.name ?? "Unknown app"}</strong><span><code>{short(deployment.commitSha)}</code> <i /> {deployment.server?.name ?? "No target"}</span></span>
    <span className="stage-track">{stages.map((stage, index) => <span className={`stage ${index < current || (index === current && deployment.state === "succeeded") ? "complete" : ""} ${index === current ? "current" : ""}`} key={stage}><small>{stage}</small><i>{index < current || (index === current && deployment.state === "succeeded") ? "✓" : index === current ? tone === "danger" ? "!" : "•" : ""}</i></span>)}</span>
    <span className="strip-result"><small>{stateStage[deployment.state]}</small><strong>{deployment.state}</strong><time dateTime={deployment.createdAt}>{relative(deployment.createdAt)}</time></span>
  </button>;
}

function Evidence({ deployment, logs, onCancel }: { deployment?: Deployment; logs: DeploymentLog[]; onCancel: () => void }) {
  if (!deployment) return <div className="dossier-empty"><Mark /><h2>No deployment selected</h2><p>Choose a movement record to inspect its immutable source and target evidence.</p></div>;
  const active = !["succeeded", "failed", "cancelled"].includes(deployment.state);
  return <>
    <div className="dossier-head"><div><span className={`selection-dot ${statusTone(deployment.state)}`} /><h2>{deployment.app?.name}</h2></div><span className={`stamp ${statusTone(deployment.state)}`}>{deployment.state}</span></div>
    <div className="dossier-tabs"><button className="active">Deployment evidence</button><button disabled>History</button></div>
    <dl className="evidence-grid">
      <div><dt>Commit</dt><dd><code>{deployment.commitSha}</code></dd></div><div><dt>Created</dt><dd>{relative(deployment.createdAt)}</dd></div>
      <div className="wide"><dt>App spec digest</dt><dd><code title={deployment.specDigest}>{short(deployment.specDigest, 28)}</code></dd></div>
      <div><dt>Build type</dt><dd>{deployment.app?.buildType}</dd></div><div><dt>Branch</dt><dd>{deployment.app?.branch}</dd></div>
      <div><dt>Target</dt><dd>{deployment.server?.name}</dd></div><div><dt>Runtime</dt><dd>{deployment.server?.runtime}</dd></div>
      <div className="wide"><dt>Source</dt><dd className="truncate" title={deployment.app?.sourceRepo}>{deployment.app?.sourceRepo}</dd></div>
    </dl>
    <div className="health-block"><div className="section-label"><h3>Current evidence</h3><span className={statusTone(deployment.state)}>{deployment.message}</span></div><div className="evidence-line"><i className={statusTone(deployment.state)}>{deployment.state === "succeeded" ? "✓" : deployment.state === "failed" ? "×" : "•"}</i><span><strong>{deployment.message}</strong><small>Reported by the deployment state machine</small></span><time>{relative(deployment.finishedAt ?? deployment.startedAt)}</time></div></div>
    <div className="log-block"><div className="section-label"><h3>Live log</h3><span className={active ? "active" : ""}>{active ? "Streaming" : "Complete"}</span></div><div className="terminal" role="log" aria-live="polite">{logs.length ? logs.map((entry) => <div key={entry.id} className={entry.level}><time>{new Date(entry.createdAt).toLocaleTimeString([], { hour12: false })}</time><span>{entry.message}</span></div>) : <p>No log entries have been recorded.</p>}</div></div>
    {active && <div className="dossier-actions"><button className="danger-button" onClick={onCancel}>Cancel deployment</button></div>}
  </>;
}

function DeployPanel({ apps, onClose, onComplete }: { apps: AppModel[]; onClose: () => void; onComplete: (id: string) => void }) {
  const [appID, setAppID] = useState(apps[0]?.id ?? ""), [commit, setCommit] = useState("HEAD"), [busy, setBusy] = useState(false), [error, setError] = useState("");
  async function submit(event: FormEvent) { event.preventDefault(); setBusy(true); setError(""); try { const created = await api.deploy(appID, commit); onComplete(created.id); } catch (cause) { setError((cause as Error).message); } finally { setBusy(false); } }
  return <ActionPanel title="Dispatch a revision" intro="Bind an exact source revision to the app's current immutable specification." onClose={onClose}>
    <form className="action-form" onSubmit={submit}><label>Application<select value={appID} onChange={(e) => setAppID(e.target.value)}>{apps.map((app) => <option value={app.id} key={app.id}>{app.name}</option>)}</select></label><label>Commit SHA or ref<input value={commit} onChange={(e) => setCommit(e.target.value)} required spellCheck={false} /></label>{error && <p className="form-error" role="alert">{error}</p>}<div className="form-actions"><button type="button" className="quiet-button" onClick={onClose}>Keep inspecting</button><button className="primary-button" disabled={busy || !appID}>{busy ? "Accepting…" : "Deploy revision"}</button></div></form>
  </ActionPanel>;
}

function SetupPanel({ data, onClose, onChanged }: { data: Overview; onClose: () => void; onChanged: () => Promise<void> }) {
  return <ActionPanel title="Deployment inventory" intro="Infrastructure comes first: add a ready target, then projects and applications." onClose={onClose}>
    <InventorySetup data={data} onChanged={onChanged} />
  </ActionPanel>;
}

function ActionPanel({ title, intro, onClose, children }: { title: string; intro: string; onClose: () => void; children: React.ReactNode }) { return <><div className="action-head"><button onClick={onClose} aria-label="Close action panel">×</button><span>Operator action</span><h2>{title}</h2><p>{intro}</p></div>{children}</>; }

function AuthScreen({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [token, update] = useState(getToken()), [error, setError] = useState("");
  return <main className="auth-screen"><div className="auth-mark"><Mark /></div><div className="auth-copy"><span>Private control plane</span><h1>Authenticate to Dispatch</h1><p>Use the administrator token configured on this controller. It remains in this browser tab only.</p><form onSubmit={async (e) => { e.preventDefault(); setToken(token); try { await api.overview(); onAuthenticated(); } catch { setError("The controller rejected this token."); } }}><label>Administrator token<input type="password" value={token} onChange={(e) => update(e.target.value)} autoFocus required /></label>{error && <p role="alert">{error}</p>}<button className="primary-button">Open control plane</button></form></div></main>;
}
function LoadingBoard() { return <div className="loading-board" aria-label="Loading deployment inventory"><span /><span /><span /></div>; }
function EmptyBoard({ onDeploy }: { onDeploy: () => void }) { return <div className="empty-board"><div className="empty-path"><i>source</i><span /><i>target</i><span /><i>live</i></div><h2>The chain is ready</h2><p>The application contract and target are in place. Dispatch the first revision to begin its evidence trail.</p><button className="primary-button" onClick={onDeploy}>Deploy first revision</button></div>; }
