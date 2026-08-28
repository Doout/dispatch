import { useEffect, useMemo, useState } from "react";
import { ArrowClockwise, MagnifyingGlass } from "@phosphor-icons/react";
import { api, Deployment, DeploymentTopology, Server, WorkflowTopology } from "../api";
import { TopologyCanvas } from "../workflows/TopologyCanvas";
import { DeploymentManifests } from "./DeploymentManifests";

export function DeploymentRuntime({ deployment, section }: { deployment: Deployment; section: "topology" | "values" | "manifests" }) {
  const [data, setData] = useState<DeploymentTopology | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState("");
  const load = async () => { if (section === "manifests") return; setLoading(true); setError(""); try { setData(await api.deploymentTopology(deployment.id)); } catch (cause) { setError((cause as Error).message); } finally { setLoading(false); } };
  useEffect(() => { void load(); }, [deployment.id, section]);
  const values = useMemo(() => data?.values.filter((item) => item.path.toLowerCase().includes(query.trim().toLowerCase())) ?? [], [data, query]);
  if (section === "manifests") return <DeploymentManifests deployment={deployment} />;
  if (loading) return <div className="runtime-loading">Loading deployment inventory…</div>;
  if (error || !data) return <div className="runtime-error" role="alert">{error || "Deployment inventory is unavailable."}<button onClick={() => void load()}>Retry</button></div>;
  return <section className="runtime-panel">
    <header className="runtime-heading"><div><h2>{section === "topology" ? "Runtime topology" : "Applied values"}</h2><p>{data.target} · {data.namespace} · {data.release}</p></div><button className="icon-button" title="Refresh" aria-label="Refresh runtime inventory" onClick={() => void load()}><ArrowClockwise size={17} /></button></header>
    {data.warning && <p className="runtime-warning">{data.warning}</p>}
    {section === "topology" ? <TopologyCanvas topology={data.topology} label="Deployment runtime topology" /> : <>
      <label className="value-search"><MagnifyingGlass size={17} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Filter values" aria-label="Filter applied values" /></label>
      {values.length ? <div className="applied-values"><div className="applied-values-head"><span>Path</span><span>Value</span></div>{values.map((item) => <div key={item.path}><code>{item.path}</code><code className={item.redacted ? "redacted" : ""}>{renderValue(item.value)}</code></div>)}</div> : <p className="runtime-empty">No matching values.</p>}
    </>}
  </section>;
}

export function ServerRuntime({ server }: { server: Server }) {
  const [topology, setTopology] = useState<WorkflowTopology | null>(null); const [error,setError]=useState("");
  useEffect(()=>{let active=true;api.serverTopology(server.id).then((value)=>{if(active)setTopology(value)}).catch((cause)=>{if(active)setError((cause as Error).message)});return()=>{active=false}},[server.id]);
  return <div className="page-layout"><div className="server-topology-title"><div><h1>{server.name}</h1><p>Deployment inventory</p></div><a className="quiet-button" href="/servers">Back to servers</a></div>{error?<div className="runtime-error" role="alert">{error}</div>:topology?<TopologyCanvas topology={topology} label={`${server.name} deployment inventory`} />:<div className="runtime-loading">Loading target inventory…</div>}</div>;
}

function renderValue(value: unknown) { if (typeof value === "string") return value; if (value === null) return "null"; return JSON.stringify(value); }
