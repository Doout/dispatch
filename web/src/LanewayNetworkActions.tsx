import { FormEvent, useState } from "react";
import { Check, Copy, X } from "@phosphor-icons/react";
import { api, LanewayInventory, LanewayNodeInstaller, PrivateNetwork } from "./api";

export function LanewayNodeInstallerForm({
  network,
  open,
  onOpenChange,
  onError,
}: {
  network: PrivateNetwork;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onError: (message: string) => void;
}) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"node" | "connector" | "exit">("node");
  const [installMode, setInstallMode] = useState<"docker_compose" | "systemd">("docker_compose");
  const [busy, setBusy] = useState(false);
  const [installer, setInstaller] = useState<LanewayNodeInstaller | null>(null);
  const [copied, setCopied] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    onError("");
    try {
      setInstaller(await api.createLanewayNodeInstaller(network.id, { name, kind, installMode }));
    } catch (cause) {
      onError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function copy() {
    if (!installer) return;
    await navigator.clipboard.writeText(installer.command);
    setCopied(true);
  }

  if (!open) {
    return null;
  }

  return <form className="laneway-action-form" onSubmit={submit}>
    <header><strong>{installer ? "Run on the new node" : "Add node"}</strong><button type="button" aria-label="Close node form" onClick={() => { setInstaller(null); onOpenChange(false); }}><X size={15} /></button></header>
    {installer ? <div className="laneway-installer-command">
      <code>{installer.command}</code>
      <button type="button" className="quiet-button" onClick={() => void copy()}>{copied ? <Check size={14} /> : <Copy size={14} />}{copied ? "Copied" : "Copy"}</button>
    </div> : <>
      <div className="laneway-action-fields">
        <label><span>Name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="vpc-node" required maxLength={253} /></label>
        <label><span>Role</span><select value={kind} onChange={(event) => setKind(event.target.value as typeof kind)}><option value="node">Node</option><option value="connector">Connector</option><option value="exit">Exit</option></select></label>
        <label><span>Install with</span><select value={installMode} onChange={(event) => setInstallMode(event.target.value as typeof installMode)}><option value="docker_compose">Docker Compose</option><option value="systemd">systemd</option></select></label>
      </div>
      <footer><button type="submit" className="primary-button" disabled={busy || !name.trim()}>{busy ? "Creating..." : "Create installer"}</button></footer>
    </>}
  </form>;
}

export function LanewayRouteForm({
  network,
  inventory,
  open,
  onOpenChange,
  onCreated,
  onError,
}: {
  network: PrivateNetwork;
  inventory: LanewayInventory;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => Promise<void>;
  onError: (message: string) => void;
}) {
  const [nodeID, setNodeID] = useState("");
  const [prefix, setPrefix] = useState("");
  const [mode, setMode] = useState<"nat" | "routed">("nat");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    onError("");
    try {
      await api.createLanewayRoute(network.id, { nodeId: nodeID, prefix, mode, metric: 100 });
      setPrefix("");
      onOpenChange(false);
      await onCreated();
    } catch (cause) {
      onError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return null;
  }

  return <form className="laneway-action-form" onSubmit={submit}>
    <header><strong>Add route</strong><button type="button" aria-label="Close route form" onClick={() => onOpenChange(false)}><X size={15} /></button></header>
    <div className="laneway-action-fields route">
      <label><span>Node</span><select value={nodeID} onChange={(event) => setNodeID(event.target.value)} required><option value="">Choose a node</option>{inventory.nodes.map((node) => <option key={node.node_id} value={node.node_id}>{node.name}</option>)}</select></label>
      <label><span>Prefix</span><input value={prefix} onChange={(event) => setPrefix(event.target.value)} placeholder="10.40.0.0/16" required spellCheck={false} /></label>
      <label><span>Mode</span><select value={mode} onChange={(event) => setMode(event.target.value as typeof mode)}><option value="nat">NAT</option><option value="routed">Routed</option></select></label>
    </div>
    <footer><button type="submit" className="primary-button" disabled={busy || !nodeID || !prefix.trim()}>{busy ? "Adding..." : "Add route"}</button></footer>
  </form>;
}
