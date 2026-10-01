import { X } from "@phosphor-icons/react";
import { useEffect, useState } from "react";
import { api, type Server, type StorageResource } from "./api";
import { useDialogFocus } from "./useDialogFocus";

export function StorageInventory({ server, canManage, onClose }: { server: Server; canManage: boolean; onClose: () => void }) {
  const ref = useDialogFocus(onClose);
  const [items, setItems] = useState<StorageResource[]>([]);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    api.storage(server.id).then(value => { if (active) setItems(value); }).catch(error => { if (active) setError(String(error.message)); }).finally(() => { if (active) setBusy(false); });
    return () => { active = false; };
  }, [server.id]);
  async function change(action: () => Promise<unknown>) {
    setBusy(true); setError("");
    try { await action(); setItems(await api.storage(server.id)); } catch (error) { setError(error instanceof Error ? error.message : "Storage action failed."); }
    finally { setBusy(false); }
  }
  return <div className="dialog-layer workflow-resource-layer"><section ref={ref} className="resource-dialog workflow-resource-dialog storage-dialog" role="dialog" aria-modal="true" aria-labelledby="storage-title">
    <header><div><h2 id="storage-title">Storage on {server.name}</h2><small>Workload removal retains data. Data deletion requires a separate review.</small></div><button type="button" aria-label="Close storage" onClick={onClose}><X size={19} /></button></header>
    <div className="dialog-body">
      {canManage && <button className="quiet-button" disabled={busy} onClick={() => void change(() => api.reconcileStorage(server.id))}>Inspect target storage</button>}
      {error && <p className="form-error" role="alert">{error}</p>}
      {busy && <p role="status">Loading storage…</p>}
      {!busy && items.length === 0 && <p>No recorded storage. {canManage ? "Inspect the target to discover its volumes and verify their owners." : "Only storage in your projects is visible."}</p>}
      <div className="resource-table-wrap"><table className="resource-table"><thead><tr><th>Storage</th><th>Owner and consumers</th><th>State</th><th>Data policy</th>{canManage && <th>Actions</th>}</tr></thead><tbody>
        {items.map(item => <tr key={item.id}>
          <td data-label="Storage"><strong>{item.name}</strong><small>{item.kind}{item.namespace ? ` · ${item.namespace}` : ""}</small></td>
          <td data-label="Owner"><span>{item.ownership === "verified" ? `${item.ownerKind} ${item.ownerId}` : "Unverified ownership"}{item.orphaned ? " · retained after removal" : ""}</span><small>{item.consumers.length} consumer(s)</small>{item.consumers.map(consumer => <small key={`${consumer.id}:${consumer.mount}`}>{consumer.id}{consumer.mount ? ` → ${consumer.mount}` : ""}</small>)}</td>
          <td data-label="State">{item.state}<small>{item.observedAt ? `Inspected ${new Date(item.observedAt).toLocaleString()}` : "Not inspected"}</small>{item.message && <small>{item.message}</small>}</td>
          <td data-label="Policy">{canManage && item.ownership === "verified" ? <select aria-label={`Data policy for ${item.name}`} disabled={busy} value={item.policy} onChange={event => void change(() => api.storagePolicy(item.id, item.revision, event.target.value as "retain" | "destroy"))}><option value="retain">Retain data</option><option value="destroy">Allow reviewed deletion</option></select> : item.policy}</td>
          {canManage && <td data-label="Actions"><button className="danger-button" disabled={busy || item.policy !== "destroy" || item.ownership !== "verified" || item.consumers.length > 0 || !["present", "absent"].includes(item.state)} onClick={() => void change(() => api.deleteStorage(item.id))}>Delete data</button></td>}
        </tr>)}
      </tbody></table></div>
    </div>
  </section></div>;
}
