import { FormEvent, useState } from "react";
import {
  ArrowClockwise,
  ArrowSquareOut,
  CaretDown,
  CaretRight,
  PlugsConnected,
  Plus,
  Trash,
} from "@phosphor-icons/react";
import { api, LanewayInventory, PrivateNetwork } from "./api";
import { LanewayNodeInstallerForm, LanewayRouteForm } from "./LanewayNetworkActions";
import { StatusLabel, TableIconAction } from "./ResourceTable";

export function LanewayNetworksSection({
  networks,
  creating,
  onCreatingChange,
  onChanged,
}: {
  networks: PrivateNetwork[];
  creating: boolean;
  onCreatingChange: (value: boolean) => void;
  onChanged: () => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [authority, setAuthority] = useState("");
  const [busyID, setBusyID] = useState("");
  const [expandedID, setExpandedID] = useState("");
  const [inventories, setInventories] = useState<Record<string, LanewayInventory>>({});
  const [actionID, setActionID] = useState("");
  const [confirmDelete, setConfirmDelete] = useState("");
  const [error, setError] = useState("");

  async function connect(event: FormEvent) {
    event.preventDefault();
    setBusyID("connect");
    setError("");
    try {
      const response = await api.startLanewayNetworkAuthorization({ name, authority });
      if (response.method === "redirect") {
        window.location.assign(response.action);
        return;
      }
      const form = document.createElement("form");
      form.method = "post";
      form.action = response.action;
      Object.entries(response.fields || {}).forEach(([fieldName, value]) => {
        const input = document.createElement("input");
        input.type = "hidden";
        input.name = fieldName;
        input.value = value;
        form.appendChild(input);
      });
      document.body.appendChild(form);
      form.submit();
    } catch (cause) {
      setError((cause as Error).message);
      setBusyID("");
    }
  }

  async function toggle(network: PrivateNetwork) {
    if (expandedID === network.id) {
      setExpandedID("");
      return;
    }
    setExpandedID(network.id);
    if (inventories[network.id]) return;
    setBusyID(network.id);
    setError("");
    try {
      const inventory = await api.lanewayNetworkInventory(network.id);
      setInventories((current) => ({ ...current, [network.id]: inventory }));
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function refresh(network: PrivateNetwork) {
    setBusyID(network.id);
    setError("");
    try {
      const inventory = await api.lanewayNetworkInventory(network.id);
      setInventories((current) => ({ ...current, [network.id]: inventory }));
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  async function remove(network: PrivateNetwork) {
    setBusyID(network.id);
    setError("");
    try {
      await api.deletePrivateNetwork(network.id);
      setConfirmDelete("");
      setExpandedID("");
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  if (creating) {
    return <section className="inline-create connection-editor laneway-network-editor" aria-labelledby="laneway-network-editor-title">
      <header><div><h2 id="laneway-network-editor-title">Connect Laneway</h2></div></header>
      {error && <p className="form-error" role="alert">{error}</p>}
      <form className="laneway-network-form" onSubmit={connect}>
        <div className="private-network-provider"><span><PlugsConnected size={19} /></span><div><strong>Laneway network</strong><small>Authorize this application, then choose a network.</small></div></div>
        <div className="private-network-grid">
          <label><span>Name</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="Production network" required maxLength={80} /></label>
          <label><span>Laneway URL</span><input type="url" value={authority} onChange={(event) => setAuthority(event.target.value)} placeholder="https://lane.example.com" required spellCheck={false} /></label>
        </div>
        <div className="laneway-access-summary"><span>Each connection is limited to one network.</span><strong>Scoped access</strong></div>
        <div className="connection-actions"><button type="button" className="quiet-button" onClick={() => onCreatingChange(false)}>Cancel</button><button type="submit" className="primary-button" disabled={busyID === "connect" || !name.trim() || !authority.trim()}>{busyID === "connect" ? "Opening Laneway..." : "Continue to Laneway"}</button></div>
      </form>
    </section>;
  }

  return <section className="connection-provider-group laneway-network-section" aria-labelledby="laneway-networks-title">
    <header className="connection-provider-header"><span className="connection-provider-icon network"><PlugsConnected size={19} /></span><div><h2 id="laneway-networks-title">Laneway networks</h2><span>Nodes and routes</span></div><strong>{networks.length}</strong></header>
    {error && <p className="form-error connection-group-error" role="alert">{error}</p>}
    {networks.length ? <div className="laneway-network-list">{networks.map((network) => {
      const expanded = expandedID === network.id;
      const inventory = inventories[network.id];
      return <article className={`laneway-network-row${expanded ? " expanded" : ""}`} key={network.id}>
        <div className="laneway-network-summary">
          <button type="button" className="laneway-network-toggle" aria-expanded={expanded} onClick={() => void toggle(network)}>
            {expanded ? <CaretDown size={15} /> : <CaretRight size={15} />}
            <span><strong>{network.name}</strong><small>{network.config.networkName || network.config.networkId}</small></span>
          </button>
          <div className="laneway-network-stats"><span><small>Pool</small><code>{network.config.ipv4Pool || "Not set"}</code></span><span><small>Nodes</small><strong>{network.details.nodeCount || "0"}</strong></span><span><small>Routes</small><strong>{network.details.routeCount || "0"}</strong></span><StatusLabel state={network.state} /></div>
          <div className="table-icon-actions">
            <TableIconAction label={`Refresh ${network.name}`} tooltip="Refresh" onClick={() => void refresh(network)}><ArrowClockwise size={16} /></TableIconAction>
            <a className="table-icon-action tooltip-trigger" aria-label={`Open ${network.name} in Laneway`} data-tooltip="Open Laneway" href={network.config.authority} target="_blank" rel="noreferrer"><ArrowSquareOut size={16} /></a>
            <TableIconAction label={`Remove ${network.name}`} tooltip="Remove" danger onClick={() => setConfirmDelete(network.id)}><Trash size={16} /></TableIconAction>
          </div>
        </div>
        {confirmDelete === network.id && <div className="laneway-remove-confirm"><span>Disconnect this network?</span><button type="button" onClick={() => setConfirmDelete("")}>Cancel</button><button type="button" className="danger-button" disabled={busyID === network.id} onClick={() => void remove(network)}>Disconnect</button></div>}
        {expanded && <div className="laneway-network-inventory">
          {busyID === network.id && !inventory ? <p className="laneway-inventory-state">Loading network...</p> : inventory ? <>
            <section><header><div><h3>Nodes</h3><span>{inventory.nodes.length}</span></div>{actionID !== `${network.id}:node` && <button type="button" className="laneway-inline-add" onClick={() => setActionID(`${network.id}:node`)}><Plus size={14} />Add node</button>}</header><LanewayNodeInstallerForm network={network} open={actionID === `${network.id}:node`} onOpenChange={(open) => setActionID(open ? `${network.id}:node` : "")} onError={setError} />{inventory.nodes.length ? <div>{inventory.nodes.map((node) => {
              const status = inventory.endpointStatuses.find((item) => item.node_id === node.node_id);
              return <div className="laneway-inventory-item" key={node.node_id}><span><strong>{node.name}</strong><small>{node.ipv4_address || node.ipv6_address || node.enrollment_class}</small></span><StatusLabel state={node.revoked_at_unix_seconds ? "revoked" : status?.freshness === "current" ? "ready" : status?.freshness || "unknown"} /></div>;
            })}</div> : <p>No nodes</p>}</section>
            <section><header><div><h3>Routes</h3><span>{inventory.routes.length}</span></div>{actionID !== `${network.id}:route` && <button type="button" className="laneway-inline-add" disabled={!inventory.nodes.length} onClick={() => setActionID(`${network.id}:route`)}><Plus size={14} />Add route</button>}</header><LanewayRouteForm network={network} inventory={inventory} open={actionID === `${network.id}:route`} onOpenChange={(open) => setActionID(open ? `${network.id}:route` : "")} onCreated={() => refresh(network)} onError={setError} />{inventory.routes.length ? <div>{inventory.routes.map((route) => <div className="laneway-inventory-item" key={route.route_id}><span><strong>{route.prefix}</strong><small>{route.kind} through {inventory.nodes.find((node) => node.node_id === route.node_id)?.name || route.node_id}</small></span><StatusLabel state={route.state} /></div>)}</div> : <p>No routes</p>}</section>
          </> : <p className="laneway-inventory-state">Network details unavailable.</p>}
        </div>}
      </article>;
    })}</div> : <div className="connection-provider-empty">No Laneway networks</div>}
  </section>;
}
