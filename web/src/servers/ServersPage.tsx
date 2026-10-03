import { ServerRoutingSettings } from "../ServerRoutingSettings";
import { StorageInventory } from "../StorageInventory";
import { FormEvent, useState } from "react";
import {
  ArrowClockwise,
  ArrowSquareOut,
  Check,
  Copy,
  HardDrives,
  LockSimple,
  PencilSimple,
  Plus,
  Trash,
} from "@phosphor-icons/react";
import { api, Deployment, GitHubAppConnection, Overview, RelayWebhook, Server } from "../api";
import { relative } from "../presentation";
import { View } from "../routes";
import { InfrastructureProviders } from "../InfrastructureProviders";
import { InfrastructureQuotas } from "../InfrastructureQuotas";
import { ManagedServers } from "../ManagedServers";
import { TemporaryEnvironments } from "../TemporaryEnvironments";
import { TargetBootstraps } from "../TargetBootstraps";
import { PageHeader } from "../PageHeader";
import { StatusLabel, TableIconAction } from "../ResourceTable";
import { ResourceSummary } from "../components/PageStates";

export function ServersPage({
  overview,
  canManage = true,
  onChanged,
  onAdd,
  onEdit,
  onRepair,
  onDelete,
  onTopology,
}: {
  overview: Overview;
  canManage?: boolean;
  onChanged: () => Promise<void>;
  onAdd: () => void;
  onEdit: (server: Server) => void;
  onRepair: (server: Server) => void;
  onDelete: (server: Server) => void;
  onTopology?: (server: Server) => void;
}) {
  const [storageServer, setStorageServer] = useState<Server>();
  const [routingServer, setRoutingServer] = useState<Server>();
  const targets = overview.servers.filter(
    (server) => server.runtime !== "relay" && server.runtime !== "builder",
  );
  const builders = overview.servers.filter(
    (server) => server.runtime === "builder",
  );
  const relays = overview.servers.filter(
    (server) => server.runtime === "relay",
  );
  const ready = targets.filter((server) => server.state === "ready").length;
  const connected = relays.filter(
    (server) => server.state === "connected",
  ).length;
  return (
    <div className="page-layout">
      {routingServer && <ServerRoutingSettings server={routingServer} onClose={() => setRoutingServer(undefined)} onChanged={onChanged} />}
      {storageServer && <StorageInventory server={storageServer} canManage={canManage} onClose={() => setStorageServer(undefined)} />}
      <PageHeader
        view="servers"
        action={canManage ? { label: "Add server", onClick: onAdd } : undefined}
      />
      <ResourceSummary
        items={[
          { label: "Targets", value: targets.length },
          { label: "Target ready", value: ready },
          { label: "Builders", value: builders.length },
          { label: "Relays", value: relays.length },
          { label: "Relay connected", value: connected },
        ]}
      />
      {canManage && <InfrastructureProviders overview={overview} />}
      <InfrastructureQuotas overview={overview} canManage={canManage} />
      <TemporaryEnvironments overview={overview} canManage={canManage} />
      {canManage && <><ManagedServers overview={overview} /><TargetBootstraps overview={overview} /></>}
      <section className="server-section">
        <div className="section-title">
          <div>
            <h2>Deployment targets</h2>
          </div>
        </div>
        {targets.length ? (
          <div className="resource-table-wrap">
            <table className="resource-table server-table">
              <thead>
                <tr>
                  <th>Server</th>
                  <th>Connection</th>
                  <th>Runtime</th>
                  <th>Status</th>
                  <th className="actions-head">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {targets.map((server) => (
                  <tr key={server.id}>
                    <td data-label="Server">
                      <strong>{server.name}</strong>
                      {server.address === "local" && (
                        <small className="managed-label">
                          <LockSimple size={12} />
                          Managed
                        </small>
                      )}
                    </td>
                    <td data-label="Connection">
                      <span className="connection">
                        {server.address === "local"
                          ? "Local Docker socket"
                          : server.runtime === "openshift"
                            ? server.address
                            : server.kubernetes?.context ||
                              server.kubernetes?.kubeconfigPath ||
                              server.address}
                      </span>
                    </td>
                    <td data-label="Runtime">
                      {server.runtime === "docker"
                        ? "Docker"
                        : server.runtime === "openshift"
                          ? "OpenShift"
                          : "Kubernetes"}
                    </td>
                    <td data-label="Status">
                      <StatusLabel state={server.state} />
                    </td>
                    <td className="row-actions">
                      <div className="table-icon-actions">
                        {canManage && server.runtime === "docker" && <TableIconAction label={`Configure routing on ${server.name}`} tooltip="Routing" onClick={() => setRoutingServer(server)}><ArrowSquareOut size={16} /></TableIconAction>}
                        <TableIconAction label={`View storage on ${server.name}`} tooltip="Storage" onClick={() => setStorageServer(server)}><HardDrives size={16} /></TableIconAction>
                        {onTopology && (
                          <TableIconAction
                            label={`View deployments on ${server.name}`}
                            tooltip="Topology"
                            onClick={() => onTopology(server)}
                          >
                            <HardDrives size={16} />
                          </TableIconAction>
                        )}
                        {canManage && server.address !== "local" && (
                          <>
                            {server.runtime === "openshift" && (
                              <TableIconAction
                                label={`Repair ${server.name}`}
                                tooltip="Repair"
                                onClick={() => onRepair(server)}
                              >
                                <ArrowClockwise size={16} />
                              </TableIconAction>
                            )}
                            <TableIconAction
                              label={`Edit ${server.name}`}
                              tooltip="Edit"
                              onClick={() => onEdit(server)}
                            >
                              <PencilSimple size={16} />
                            </TableIconAction>
                            <TableIconAction
                              label={`Delete ${server.name}`}
                              tooltip="Delete"
                              danger
                              onClick={() => onDelete(server)}
                            >
                              <Trash size={16} />
                            </TableIconAction>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="section-empty">No deployment targets.</div>
        )}
      </section>
      <section className="server-section">
        <div className="section-title">
          <div>
            <h2>Docker builders</h2>
            <p>Workflow jobs with <code>builder: docker</code> use this pool.</p>
          </div>
        </div>
        {builders.length ? (
          <div className="resource-table-wrap">
            <table className="resource-table server-table">
              <thead><tr><th>Server</th><th>Docker connection</th><th>Capacity</th><th>Status</th><th className="actions-head"><span className="sr-only">Actions</span></th></tr></thead>
              <tbody>{builders.map((server) => (
                <tr key={server.id}>
                  <td data-label="Server"><strong>{server.name}</strong></td>
                  <td data-label="Docker connection"><span className="connection">{server.address}</span></td>
                  <td data-label="Capacity">{server.builder?.maxConcurrent ?? 1} jobs</td>
                  <td data-label="Status"><StatusLabel state={server.state} /></td>
                  <td className="row-actions"><div className="table-icon-actions">{canManage && <>
                    <TableIconAction label={`Edit ${server.name}`} tooltip="Edit" onClick={() => onEdit(server)}><PencilSimple size={16} /></TableIconAction>
                    <TableIconAction label={`Delete ${server.name}`} tooltip="Delete" danger onClick={() => onDelete(server)}><Trash size={16} /></TableIconAction>
                  </>}</div></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        ) : <div className="section-empty">No Docker builders.</div>}
      </section>
      {canManage && <section className="server-section relay-section">
        <div className="section-title">
          <div>
            <h2>Event relays</h2>
          </div>
        </div>
        {relays.length ? (
          <div className="relay-server-list">
            {relays.map((server) => (
              <RelayServerRow
                key={server.id}
                server={server}
                webhooks={overview.relayWebhooks.filter(
                  (hook) => hook.serverId === server.id,
                )}
                connections={overview.githubApps}
                onChanged={onChanged}
                onEdit={() => onEdit(server)}
                onDelete={() => onDelete(server)}
              />
            ))}
          </div>
        ) : (
          <div className="section-empty">No event relays.</div>
        )}
      </section>}
    </div>
  );
}

function RelayServerRow({
  server,
  webhooks,
  connections,
  onChanged,
  onEdit,
  onDelete,
}: {
  server: Server;
  webhooks: RelayWebhook[];
  connections: GitHubAppConnection[];
  onChanged: () => Promise<void>;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [provider, setProvider] = useState("github");
  const [connectionID, setConnectionID] = useState(connections[0]?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");

  async function verify() {
    setBusy(true);
    setError("");
    try {
      await api.verifyRelayServer(server.id);
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function add(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createRelayWebhook(server.id, {
        name,
        provider,
        providerConnectionId: provider === "github" ? connectionID : undefined,
      });
      setName("");
      setAdding(false);
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function remove(hook: RelayWebhook) {
    setBusy(true);
    setError("");
    try {
      await api.deleteRelayWebhook(server.id, hook.id);
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function copy(hook: RelayWebhook) {
    await navigator.clipboard.writeText(hook.url);
    setCopied(hook.id);
    window.setTimeout(() => setCopied(""), 1600);
  }

  return (
    <article className="relay-server-row">
      <div className="relay-server-summary">
        <div className="relay-server-name">
          <strong>{server.name}</strong>
          <span>{new URL(server.address).host}</span>
        </div>
        <div className="relay-server-metric">
          <span>Queue</span>
          <strong>{server.relay?.pendingEvents ?? 0} pending</strong>
        </div>
        <StatusLabel state={server.state} />
        <div className="row-actions">
          <button disabled={busy} onClick={() => void verify()}>
            <ArrowClockwise size={15} />
            Test
          </button>
          <button onClick={onEdit}>
            <PencilSimple size={15} />
            Edit
          </button>
          <button className="delete-action" onClick={onDelete}>
            <Trash size={15} />
            Delete
          </button>
        </div>
      </div>
      {server.relay?.lastError && (
        <p className="relay-error" role="status">
          {server.relay.lastError}
        </p>
      )}
      <details className="relay-webhooks">
        <summary>
          <span>Webhook endpoints</span>
          <small>{webhooks.length}</small>
        </summary>
        <div className="relay-webhook-body">
          {webhooks.length ? (
            <div className="relay-webhook-list">
              {webhooks.map((hook) => (
                <div key={hook.id}>
                  <span>
                    <strong>{hook.name}</strong>
                    <small>
                      {hook.provider}
                      {hook.lastDeliveryAt
                        ? `, last event ${relative(hook.lastDeliveryAt)}`
                        : ""}
                    </small>
                  </span>
                  <code>{hook.url}</code>
                  <button onClick={() => void copy(hook)}>
                    {copied === hook.id ? (
                      <Check size={14} />
                    ) : (
                      <Copy size={14} />
                    )}
                    {copied === hook.id ? "Copied" : "Copy URL"}
                  </button>
                  <button
                    className="delete-action"
                    disabled={busy}
                    onClick={() => void remove(hook)}
                  >
                    <Trash size={14} />
                    Remove
                  </button>
                </div>
              ))}
            </div>
          ) : (
            <p className="relay-webhook-empty">No endpoints.</p>
          )}
          {adding ? (
            <form className="relay-webhook-form" onSubmit={add}>
              <label>
                <span>Name</span>
                <input
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="Repository events"
                  required
                />
              </label>
              <label>
                <span>Provider</span>
                <input
                  value={provider}
                  onChange={(event) =>
                    setProvider(event.target.value.toLowerCase())
                  }
                  placeholder="github"
                  pattern="[a-z][a-z0-9_-]{0,31}"
                  required
                />
              </label>
              {provider === "github" && (
                <label>
                  <span>GitHub connection</span>
                  <select
                    value={connectionID}
                    onChange={(event) => setConnectionID(event.target.value)}
                    required
                  >
                    <option value="">Select a connection</option>
                    {connections.map((connection) => (
                      <option key={connection.id} value={connection.id}>
                        {connection.name}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              <div className="relay-webhook-actions">
                <button
                  type="button"
                  className="quiet-button"
                  onClick={() => setAdding(false)}
                >
                  Cancel
                </button>
                <button
                  className="primary-button"
                  disabled={
                    busy ||
                    !name.trim() ||
                    !provider.trim() ||
                    (provider === "github" && !connectionID)
                  }
                >
                  {busy ? "Creating..." : "Create endpoint"}
                </button>
              </div>
            </form>
          ) : (
            <button
              className="quiet-button relay-add-webhook"
              onClick={() => setAdding(true)}
            >
              <Plus size={15} />
              Add endpoint
            </button>
          )}
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
        </div>
      </details>
    </article>
  );
}
