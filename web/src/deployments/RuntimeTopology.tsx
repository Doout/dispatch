import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowClockwise, MagnifyingGlass } from "@phosphor-icons/react";
import { api, Deployment, DeploymentTopology, Server, WorkflowTopology, WorkflowTopologyNode } from "../api";
import { TopologyCanvas } from "../workflows/TopologyCanvas";
import { DeploymentManifests } from "./DeploymentManifests";
import { ResourceInspector } from "./ResourceInspector";

export function DeploymentRuntime({ deployment, section }: { deployment: Deployment; section: "topology" | "values" | "manifests" }) {
  const [data, setData] = useState<DeploymentTopology | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState("");
  const [showAll, setShowAll] = useState(false);
  const [selectedNode, setSelectedNode] = useState<WorkflowTopologyNode | null>(null);
  const load = async () => { if (section === "manifests") return; setLoading(true); setError(""); try { setData(await api.deploymentTopology(deployment.id, section === "values")); } catch (cause) { setError((cause as Error).message); } finally { setLoading(false); } };
  useEffect(() => { setShowAll(false); void load(); }, [deployment.id, section]);
  const values = useMemo(() => (data?.valuesAnalyzed && !showAll ? data.chartValues ?? [] : data?.values ?? []).filter((item) => item.path.toLowerCase().includes(query.trim().toLowerCase())) ?? [], [data, query, showAll]);
  if (section === "manifests") return <DeploymentManifests deployment={deployment} />;
  if (loading) return <div className="runtime-loading">Loading deployment inventory…</div>;
  if (error || !data) return <div className="runtime-error" role="alert">{error || "Deployment inventory is unavailable."}<button onClick={() => void load()}>Retry</button></div>;
  return <section className="runtime-panel">
    <header className="runtime-heading"><div><h2>{section === "topology" ? "Runtime topology" : "Chart values"}</h2><p>{data.target} · {data.namespace} · {data.release}</p></div><button className="icon-button" title="Refresh" aria-label="Refresh runtime inventory" onClick={() => void load()}><ArrowClockwise size={17} /></button></header>
    {data.warning && <p className="runtime-warning">{data.warning}</p>}
    {section === "topology" ? <><TopologyCanvas topology={data.topology} label="Deployment runtime topology" isNodeSelectable={(node) => inspectableKinds.has(node.kind)} onNodeSelect={setSelectedNode} />{selectedNode && <ResourceInspector deploymentID={deployment.id} node={selectedNode} onClose={() => setSelectedNode(null)} />}</> : <>
      {data.valuesAnalyzed && <p className="values-summary">{data.chartValues?.length ?? 0} chart values · {data.values.length} supplied values</p>}
      <label className="values-scope"><input type="checkbox" checked={showAll} onChange={(event) => setShowAll(event.target.checked)} /> Show all supplied values</label>
      {data.valuesNotes?.map((note) => <p key={note} className="runtime-warning">{note}</p>)}
      <label className="value-search"><MagnifyingGlass size={17} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Filter values" aria-label="Filter applied values" /></label>
      {values.length ? <div className="applied-values"><div className="applied-values-head"><span>Path</span><span>Value</span></div>{values.map((item) => <div key={item.path}><code>{item.path}{item.source && <small className="value-origin">{item.source}</small>}</code><div className="value-content"><code className={item.redacted ? "redacted" : ""}>{renderValue(!item.redacted && item.renderedValue !== undefined ? item.renderedValue : item.value)}</code>{!item.redacted && item.renderedValue !== undefined && <><small className="value-origin">Rendered with Helm tpl · preview</small><details className="value-raw"><summary>Raw value</summary><code>{renderValue(item.value)}</code></details></>}</div></div>)}</div> : <p className="runtime-empty">No matching values.</p>}
    </>}
  </section>;
}

export function ServerRuntime({ server }: { server: Server }) {
  const [topology, setTopology] = useState<WorkflowTopology | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setTopology(await api.serverTopology(server.id));
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setLoading(false);
    }
  }, [server.id]);
  useEffect(() => { void load(); }, [load]);
  return <div className="page-layout">
    <div className="server-topology-title"><div><h1>{server.name}</h1><p>Deployment inventory</p></div><div className="server-topology-actions"><button className="icon-button" title="Refresh" aria-label="Refresh deployment inventory" disabled={loading} onClick={() => void load()}><ArrowClockwise size={17} /></button><a className="quiet-button" href="/servers">Back to servers</a></div></div>
    {error ? <div className="runtime-error" role="alert">{error}<button onClick={() => void load()}>Retry</button></div> : topology ? <TopologyCanvas topology={topology} label={`${server.name} deployment inventory`} /> : <div className="runtime-loading">Loading target inventory…</div>}
  </div>;
}

function renderValue(value: unknown) { if (typeof value === "string") return value; if (value === null) return "null"; return JSON.stringify(value); }

const inspectableKinds = new Set(["deployment", "statefulset", "daemonset", "hpa", "pod", "service", "ingress", "route", "pvc"]);
