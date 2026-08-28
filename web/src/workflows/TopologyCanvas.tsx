import { CSSProperties, useCallback, useLayoutEffect, useRef, useState } from "react";
import { Cloud, Cube, Database, FlagCheckered, GitBranch, HardDrives, Minus, Package, Plus, Stack, TerminalWindow } from "@phosphor-icons/react";
import { WorkflowTopology, WorkflowTopologyNode } from "../api";

type DrawnEdge = { id: string; path: string; from: string; to: string; kind: string };

export function TopologyCanvas({ topology, label = "Application topology" }: { topology: WorkflowTopology; label?: string }) {
  const boardRef = useRef<HTMLDivElement>(null);
  const [zoom, setZoom] = useState(1);
  const [edges, setEdges] = useState<DrawnEdge[]>([]);
  const [focused, setFocused] = useState("");

  const drawEdges = useCallback(() => {
    const board = boardRef.current;
    if (!board) return;
    const bounds = board.getBoundingClientRect();
    const nodes = Array.from(board.querySelectorAll<HTMLElement>("[data-topology-node]"));
    setEdges(topology.edges.flatMap((edge, index) => {
      const from = nodes.find((node) => node.dataset.topologyNode === edge.from);
      const to = nodes.find((node) => node.dataset.topologyNode === edge.to);
      if (!from || !to) return [];
      const left = from.getBoundingClientRect();
      const right = to.getBoundingClientRect();
      const x1 = (left.right - bounds.left) / zoom;
      const y1 = (left.top + left.height / 2 - bounds.top) / zoom;
      const x2 = (right.left - bounds.left) / zoom;
      const y2 = (right.top + right.height / 2 - bounds.top) / zoom;
      const bend = Math.max(34, Math.abs(x2 - x1) * .42);
      return [{ id: `${edge.from}-${edge.to}-${index}`, from: edge.from, to: edge.to, kind: edge.kind, path: `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}` }];
    }));
  }, [topology.edges, zoom]);

  useLayoutEffect(() => {
    const frame = window.requestAnimationFrame(drawEdges);
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(drawEdges);
    if (boardRef.current) observer?.observe(boardRef.current);
    window.addEventListener("resize", drawEdges);
    return () => { window.cancelAnimationFrame(frame); observer?.disconnect(); window.removeEventListener("resize", drawEdges); };
  }, [drawEdges]);

  const edgeClass = (edge: DrawnEdge) => !focused ? "" : edge.from === focused || edge.to === focused ? "related" : "muted";
  const counts = topology.columns.map((column) => ({ ...column, count: topology.nodes.filter((node) => node.column === column.id).length }));

  return <section className="topology-surface" aria-label={label}>
    <div className="topology-toolbar">
      <dl>{counts.map((column) => <div key={column.id}><dd>{column.count}</dd><dt>{column.label.toLowerCase()}</dt></div>)}</dl>
      <div className="topology-zoom" aria-label="Topology zoom controls">
        <button aria-label="Zoom out" disabled={zoom <= .7} onClick={() => setZoom((value) => Math.max(.7, value - .1))}><Minus size={14} /></button>
        <button className="zoom-value" onClick={() => setZoom(1)}>{Math.round(zoom * 100)}%</button>
        <button aria-label="Zoom in" disabled={zoom >= 1.3} onClick={() => setZoom((value) => Math.min(1.3, value + .1))}><Plus size={14} /></button>
      </div>
    </div>
    <div className="topology-viewport">
      <div ref={boardRef} className="topology-board" style={{ "--topology-zoom": zoom } as CSSProperties}>
        <svg className="topology-lines" aria-hidden="true">
          {edges.map((edge) => <path key={edge.id} d={edge.path} className={edgeClass(edge)} />)}
        </svg>
        <div className="topology-columns" style={{ gridTemplateColumns: `repeat(${topology.columns.length}, minmax(210px, 1fr))` }}>
          {topology.columns.map((column) => <section className="topology-column" key={column.id} aria-labelledby={`topology-column-${column.id}`}>
            <header><span id={`topology-column-${column.id}`}>{column.label}</span><small>{topology.nodes.filter((node) => node.column === column.id).length}</small></header>
            <div>{topology.nodes.filter((node) => node.column === column.id).map((node) => <TopologyCard key={node.id} node={node} focused={focused === node.id} onFocus={setFocused} />)}</div>
          </section>)}
        </div>
      </div>
    </div>
  </section>;
}

function TopologyCard({ node, focused, onFocus }: { node: WorkflowTopologyNode; focused: boolean; onFocus: (id: string) => void }) {
  const Icon = node.kind === "source" ? GitBranch : node.kind === "deployment" || node.kind === "release" ? Package : node.kind === "stage" ? FlagCheckered : node.kind === "target" ? HardDrives : node.kind === "namespace" ? Stack : node.kind === "service" || node.kind === "route" || node.kind === "ingress" ? Cloud : node.kind === "pvc" ? Database : node.kind === "pod" || node.kind === "hpa" || node.kind === "statefulset" || node.kind === "daemonset" ? Cube : TerminalWindow;
  const metadata = Object.entries(node.metadata ?? {});
  const compactLabel = shortenTopologyLabel(node.label);
  const body = <><div className="topology-card-main"><span className="topology-card-icon"><Icon size={17} /></span><span className="topology-card-copy"><strong aria-label={node.label}>{compactLabel}</strong>{compactLabel !== node.label && <span className="topology-name-tooltip" role="tooltip">{node.label}</span>}{node.detail && <small title={node.detail}>{node.detail}</small>}</span>{node.state && <em className={`topology-node-state ${node.state.toLowerCase().replace(/[^a-z]+/g, "-")}`}>{node.state}</em>}</div>
    {metadata.length > 0 && <dl>{metadata.map(([label, value]) => <div key={label}><dt>{label}</dt><dd title={value}>{value}</dd></div>)}</dl>}
  </>;
  return <article className={`topology-card ${node.kind}${focused ? " focused" : ""}`} data-topology-node={node.id} tabIndex={0} onMouseEnter={() => onFocus(node.id)} onMouseLeave={() => onFocus("")} onFocus={() => onFocus(node.id)} onBlur={() => onFocus("")}>{node.href ? <a className="topology-card-link" href={node.href}>{body}</a> : body}</article>;
}

export function shortenTopologyLabel(label: string, limit = 25) {
  if (label.length <= limit) return label;
  const tail = 9;
  return `${label.slice(0, limit - tail - 1)}…${label.slice(-tail)}`;
}
