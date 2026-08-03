import { FormEvent, useMemo, useState } from "react";
import { api, Overview, Server } from "./api";
import { setupStage, SetupStage } from "./presentation";

type Changed = () => Promise<void>;

const stepCopy: Record<SetupStage, { kicker: string; title: string; intro: string }> = {
  server: {
    kicker: "Infrastructure first",
    title: "Give Dispatch somewhere to run.",
    intro: "Register a Docker target and confirm it is ready before defining projects or applications.",
  },
  project: {
    kicker: "Target ready",
    title: "Organize what will run there.",
    intro: "Create the first project boundary. Applications and deployment history will live inside it.",
  },
  app: {
    kicker: "Inventory ready",
    title: "Define the first application.",
    intro: "Bind a source repository to a ready target. Dispatch will capture an immutable spec before deployment.",
  },
  complete: {
    kicker: "Ready to dispatch",
    title: "The deployment chain is complete.",
    intro: "A target, project, and application are ready for the first revision.",
  },
};

export function OnboardingView({ data, onChanged }: { data: Overview; onChanged: Changed }) {
  const stage = setupStage(data);
  const copy = stepCopy[stage];
  return <section className="onboarding" id="onboarding" aria-labelledby="onboarding-title">
    <header className="onboarding-heading">
      <div><span>{copy.kicker}</span><h1 id="onboarding-title">{copy.title}</h1><p>{copy.intro}</p></div>
      <SetupTrack stage={stage} />
    </header>
    <div className="onboarding-workspace">
      <div className="activation-panel">
        {stage === "server" && <ServerActivation data={data} onChanged={onChanged} />}
        {stage === "project" && <ProjectActivation data={data} onChanged={onChanged} />}
        {stage === "app" && <AppActivation data={data} onChanged={onChanged} />}
      </div>
      <ReadinessLedger data={data} stage={stage} />
    </div>
  </section>;
}

function SetupTrack({ stage }: { stage: SetupStage }) {
  const active = stage === "server" ? 0 : stage === "project" ? 1 : 2;
  const steps = ["Server", "Project", "Application"];
  return <ol className="setup-track" aria-label="Initial setup progress">
    {steps.map((label, index) => <li className={index < active || stage === "complete" ? "complete" : index === active ? "current" : "locked"} aria-current={stage !== "complete" && index === active ? "step" : undefined} key={label}>
      <span>{index < active || stage === "complete" ? "✓" : index + 1}</span><div><strong>{label}</strong><small>{index < active || stage === "complete" ? "Ready" : index === active ? "Current step" : "Waiting"}</small></div>
    </li>)}
  </ol>;
}

function ServerActivation({ data, onChanged }: { data: Overview; onChanged: Changed }) {
  return <>
    <div className="activation-title"><span className="activation-glyph target"><i /></span><div><h2>Add a server target</h2><p>Local Docker is ready immediately. Remote hosts are recorded as pending until agent enrollment is available.</p></div></div>
    {data.servers.length > 0 && <ServerInventory servers={data.servers} />}
    <ServerForm onChanged={onChanged} />
  </>;
}

function ProjectActivation({ data, onChanged }: { data: Overview; onChanged: Changed }) {
  const ready = data.servers.filter((server) => server.state === "ready");
  return <>
    <div className="activation-title"><span className="activation-glyph project"><i /></span><div><h2>Create a project</h2><p>Projects group application specs and deployment evidence without changing the server boundary.</p></div></div>
    <SelectedTarget server={ready[0]} />
    <ProjectForm onChanged={onChanged} />
  </>;
}

function AppActivation({ data, onChanged }: { data: Overview; onChanged: Changed }) {
  return <>
    <div className="activation-title"><span className="activation-glyph app"><i /></span><div><h2>Define an application</h2><p>Choose its project and ready target, then provide the source contract Dispatch should deploy.</p></div></div>
    <AppForm data={data} onChanged={onChanged} />
  </>;
}

function ReadinessLedger({ data, stage }: { data: Overview; stage: SetupStage }) {
  const readyServers = data.servers.filter((server) => server.state === "ready");
  return <aside className="readiness-ledger" aria-label="Deployment readiness">
    <div className="ledger-heading"><span>Readiness ledger</span><strong>{stage === "complete" ? "Complete" : "In setup"}</strong></div>
    <ReadinessRow label="Server target" value={readyServers.length ? `${readyServers.length} ready` : data.servers.length ? `${data.servers.length} pending` : "Required"} done={readyServers.length > 0} />
    <ReadinessRow label="Project boundary" value={data.projects.length ? data.projects[0].name : "Locked"} done={data.projects.length > 0} />
    <ReadinessRow label="Application spec" value={data.apps.length ? data.apps[0].name : "Locked"} done={data.apps.length > 0} />
    <div className="ledger-note"><strong>Why server first</strong><p>Runtime, enrollment state, and target identity constrain every application spec that follows.</p></div>
    <div className="ledger-contract"><span>Current runtime</span><strong>Docker</strong><small>K3s and Kubernetes remain behind the runtime contract.</small></div>
  </aside>;
}

function ReadinessRow({ label, value, done }: { label: string; value: string; done: boolean }) {
  return <div className={`readiness-row ${done ? "done" : ""}`}><i>{done ? "✓" : "—"}</i><span><strong>{label}</strong><small>{value}</small></span></div>;
}

function ServerInventory({ servers }: { servers: Server[] }) {
  return <div className="server-inventory" aria-label="Registered servers">{servers.map((server) => <div key={server.id}><span className={server.state} /><strong>{server.name}</strong><code>{server.address}</code><small>{server.state}</small></div>)}</div>;
}

function SelectedTarget({ server }: { server?: Server }) {
  if (!server) return null;
  return <div className="selected-target"><span className="ready-dot" /><div><small>Ready target</small><strong>{server.name}</strong></div><code>{server.address}</code><b>{server.runtime}</b></div>;
}

export function InventorySetup({ data, onChanged }: { data: Overview; onChanged: Changed }) {
  const ready = data.servers.some((server) => server.state === "ready");
  return <div className="setup-sequence">
    <SetupStep title="Server target" detail={`${data.servers.length} registered`} done={ready}><ServerForm onChanged={onChanged} compact /></SetupStep>
    <SetupStep title="Project" detail={`${data.projects.length} created`} done={data.projects.length > 0} locked={!ready}><ProjectForm onChanged={onChanged} compact /></SetupStep>
    <SetupStep title="Application" detail={`${data.apps.length} defined`} done={data.apps.length > 0} locked={!ready || data.projects.length === 0}><AppForm data={data} onChanged={onChanged} compact /></SetupStep>
  </div>;
}

function SetupStep({ title, detail, done, locked = false, children }: { title: string; detail: string; done: boolean; locked?: boolean; children: React.ReactNode }) {
  return <section className={`setup-step ${done ? "done" : ""} ${locked ? "locked" : ""}`}><div><span>{done ? "✓" : locked ? "—" : "○"}</span><h3>{title}</h3><small>{locked ? "Complete the prior step" : detail}</small></div>{!locked && children}</section>;
}

function ServerForm({ onChanged, compact = false }: { onChanged: Changed; compact?: boolean }) {
  const [name, setName] = useState("");
  const [address, setAddress] = useState("local");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try { await api.createServer({ name, address, runtime: "docker", agentMode: address === "local" ? "local" : "ssh-bootstrap" }); setName(""); await onChanged(); }
    catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  }
  return <form className={`setup-form ${compact ? "compact" : ""}`} onSubmit={submit}>
    <label>Server name<input id={compact ? undefined : "server-name"} placeholder="build-01" value={name} onChange={(event) => setName(event.target.value)} autoFocus={!compact} required /></label>
    <label>Address<input placeholder="local or hostname" value={address} onChange={(event) => setAddress(event.target.value)} required spellCheck={false} /><small>{address === "local" ? "Uses the controller's Docker socket." : "Will remain pending until agent enrollment completes."}</small></label>
    <label>Runtime<select value="docker" disabled><option value="docker">Docker</option></select></label>
    {error && <p className="form-error" role="alert">{error}</p>}
    <button className="primary-button" disabled={busy || !name.trim() || !address.trim()}>{busy ? "Adding server…" : "Add server"}</button>
  </form>;
}

function ProjectForm({ onChanged, compact = false }: { onChanged: Changed; compact?: boolean }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try { await api.createProject({ name, description }); setName(""); setDescription(""); await onChanged(); }
    catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  }
  return <form className={`setup-form ${compact ? "compact" : ""}`} onSubmit={submit}>
    <label>Project name<input id={compact ? undefined : "project-name"} placeholder="Platform apps" value={name} onChange={(event) => setName(event.target.value)} autoFocus={!compact} required /></label>
    <label>Purpose<input placeholder="Optional description" value={description} onChange={(event) => setDescription(event.target.value)} /></label>
    {error && <p className="form-error" role="alert">{error}</p>}
    <button className="primary-button" disabled={busy || !name.trim()}>{busy ? "Creating project…" : "Create project"}</button>
  </form>;
}

function AppForm({ data, onChanged, compact = false }: { data: Overview; onChanged: Changed; compact?: boolean }) {
  const readyServers = useMemo(() => data.servers.filter((server) => server.state === "ready"), [data.servers]);
  const [projectID, setProjectID] = useState(data.projects[0]?.id ?? "");
  const [serverID, setServerID] = useState(readyServers[0]?.id ?? "");
  const [name, setName] = useState("");
  const [repo, setRepo] = useState("");
  const [buildType, setBuildType] = useState("dockerfile");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      await api.createApp({ projectId: projectID, serverId: serverID, name, sourceRepo: repo, branch: "main", buildType, contextPath: ".", dockerfilePath: "Dockerfile", composePath: "compose.yml", containerPort: 8080, domain: "" });
      setName(""); setRepo(""); await onChanged();
    } catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  }
  return <form className={`setup-form app-setup-form ${compact ? "compact" : ""}`} onSubmit={submit}>
    <label>Project<select value={projectID} onChange={(event) => setProjectID(event.target.value)}>{data.projects.map((project) => <option value={project.id} key={project.id}>{project.name}</option>)}</select></label>
    <label>Ready target<select value={serverID} onChange={(event) => setServerID(event.target.value)}>{readyServers.map((server) => <option value={server.id} key={server.id}>{server.name}</option>)}</select></label>
    <label>Application name<input id={compact ? undefined : "application-name"} placeholder="checkout-api" value={name} onChange={(event) => setName(event.target.value)} required /></label>
    <label className="wide">HTTPS repository URL<input placeholder="https://github.com/doout/app.git" value={repo} onChange={(event) => setRepo(event.target.value)} required spellCheck={false} /></label>
    <label>Build contract<select value={buildType} onChange={(event) => setBuildType(event.target.value)}><option value="dockerfile">Dockerfile</option><option value="compose">Compose</option></select></label>
    {error && <p className="form-error" role="alert">{error}</p>}
    <button className="primary-button" disabled={busy || !projectID || !serverID || !name.trim() || !repo.trim()}>{busy ? "Defining application…" : "Define application"}</button>
  </form>;
}
