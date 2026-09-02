import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { Bell, FileCode, TerminalWindow, X } from "@phosphor-icons/react";
import { api, DeploymentResource, DeploymentResourceLog, WorkflowTopologyNode } from "../api";
import { useDialogFocus } from "../useDialogFocus";

const StructuredDocumentViewer = lazy(() => import("./StructuredDocumentViewer").then((module) => ({ default: module.StructuredDocumentViewer })));

type InspectorTab = "manifest" | "logs" | "events";

export function ResourceInspector({ deploymentID, node, onClose }: { deploymentID: string; node: WorkflowTopologyNode; onClose: () => void }) {
  const dialogRef = useDialogFocus(onClose);
  const [data, setData] = useState<DeploymentResource | null>(null);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<InspectorTab>("manifest");

  useEffect(() => {
    let active = true;
    setData(null);
    setError("");
    setTab("manifest");
    void api.deploymentResource(deploymentID, node.kind, node.label).then((next) => {
      if (active) setData(next);
    }).catch((cause: Error) => {
      if (active) setError(cause.message);
    });
    return () => { active = false; };
  }, [deploymentID, node.id, node.kind, node.label]);

  const tabs = useMemo(() => [
    { id: "manifest" as const, label: "Manifest", icon: <FileCode size={15} /> },
    ...(data?.loggable ? [{ id: "logs" as const, label: "Logs", icon: <TerminalWindow size={15} /> }] : []),
    { id: "events" as const, label: "Events", icon: <Bell size={15} />, count: data?.events.length },
  ], [data]);

  return <div className="dialog-layer resource-inspector-layer" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialogRef} className="resource-dialog resource-inspector-dialog" role="dialog" aria-modal="true" aria-labelledby="resource-inspector-title">
      <header>
        <div><span>{node.kind}</span><h2 id="resource-inspector-title">{node.label}</h2>{node.state && <p>{node.state}</p>}</div>
        <button type="button" data-autofocus aria-label="Close resource inspector" title="Close" onClick={onClose}><X size={18} /></button>
      </header>
      <nav className="resource-inspector-tabs" aria-label="Resource details">
        {tabs.map((item) => <button key={item.id} type="button" className={tab === item.id ? "active" : ""} aria-current={tab === item.id ? "page" : undefined} onClick={() => setTab(item.id)}>{item.icon}<span>{item.label}</span>{item.count !== undefined && <small>{item.count}</small>}</button>)}
      </nav>
      <div className="resource-inspector-body">
        {!data && !error && <p className="resource-inspector-state">Loading resource...</p>}
        {error && <p className="resource-inspector-state error" role="alert">{error}</p>}
        {data?.warning && <p className="runtime-warning">{data.warning}</p>}
        {data && tab === "manifest" && <Suspense fallback={<p className="resource-inspector-state">Loading manifest...</p>}><StructuredDocumentViewer manifest={data.manifest} /></Suspense>}
        {data && tab === "logs" && <ResourceLogs logs={data.logs} />}
        {data && tab === "events" && <ResourceEvents data={data} />}
      </div>
    </section>
  </div>;
}

function ResourceLogs({ logs }: { logs: DeploymentResourceLog[] }) {
  const [container, setContainer] = useState(logs[0]?.container ?? "");
  useEffect(() => { setContainer(logs[0]?.container ?? ""); }, [logs]);
  const selected = logs.find((item) => item.container === container) ?? logs[0];
  if (!selected) return <p className="resource-inspector-state">No containers found.</p>;
  return <section className="resource-log-view" aria-label="Container logs">
    {logs.length > 1 && <div className="resource-container-tabs" role="tablist" aria-label="Containers">{logs.map((item) => <button key={item.container} type="button" role="tab" aria-selected={item.container === selected.container} className={item.container === selected.container ? "active" : ""} onClick={() => setContainer(item.container)}>{item.container}</button>)}</div>}
    <pre tabIndex={0}>{selected.error || selected.content || "No log entries."}</pre>
  </section>;
}

function ResourceEvents({ data }: { data: DeploymentResource }) {
  if (!data.events.length) return <p className="resource-inspector-state">No events for this resource.</p>;
  return <div className="resource-event-list" role="list">{data.events.map((event, index) => <article key={`${event.lastSeen}-${event.reason}-${index}`} role="listitem" className={event.type.toLowerCase()}>
    <span className="resource-event-mark" />
    <div><strong>{event.reason || event.type}</strong><p>{event.message}</p></div>
    <small>{formatEventTime(event.lastSeen)}{event.count > 1 ? ` · ${event.count} times` : ""}</small>
  </article>)}</div>;
}

function formatEventTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return value;
  return date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
