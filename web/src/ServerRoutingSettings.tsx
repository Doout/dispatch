import { X } from "@phosphor-icons/react";
import { useState, type FormEvent } from "react";
import { api, type Server } from "./api";
import { useDialogFocus } from "./useDialogFocus";

export function ServerRoutingSettings({ server, onClose, onChanged }: { server: Server; onClose: () => void; onChanged: () => Promise<void> }) {
  const ref = useDialogFocus(onClose);
  const [enabled, setEnabled] = useState(Boolean(server.routing));
  const [domain, setDomain] = useState(server.routing?.baseDomain ?? "");
  const [entryPoint, setEntryPoint] = useState(server.routing?.entryPoint ?? "websecure");
  const [resolver, setResolver] = useState(server.routing?.tlsResolver ?? "letsencrypt");
  const [requiredTLS, setRequiredTLS] = useState(server.routing?.requireTls ?? true);
  const [composeService, setComposeService] = useState(server.routing?.composeService ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try { await api.updateServerRouting(server.id, enabled ? { baseDomain: domain, entryPoint, tlsResolver: resolver, requireTls: requiredTLS, composeService } : null); await onChanged(); onClose(); }
    catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  }
  return <div className="dialog-layer workflow-resource-layer"><section ref={ref} className="resource-dialog server-routing-dialog" role="dialog" aria-modal="true" aria-labelledby="routing-title">
    <header><h2 id="routing-title">Routing on {server.name}</h2><button type="button" aria-label="Close routing settings" onClick={onClose}><X size={19} /></button></header>
    <form className="dialog-body" onSubmit={event => void save(event)}>
      <p>Use a dedicated Traefik file provider on this target. Configure its routing directory and loopback connectivity before enabling public routes.</p>
      <label className="routing-checkbox"><input type="checkbox" checked={enabled} onChange={event => setEnabled(event.target.checked)} />Enable managed application routes</label>
      {enabled && <div className="form-grid">
        <label>Managed domain<input required value={domain} onChange={event => setDomain(event.target.value)} placeholder="apps.example.com" /><small>Blank application hostnames get a stable name below this domain. Point wildcard DNS at the target proxy.</small></label>
        <label>Traefik entry point<input required value={entryPoint} onChange={event => setEntryPoint(event.target.value)} placeholder="websecure" /></label>
        <label className="routing-checkbox"><input type="checkbox" checked={requiredTLS} onChange={event => setRequiredTLS(event.target.checked)} />Require verified HTTPS before reporting live</label>
        {requiredTLS && <label>Certificate resolver<input required value={resolver} onChange={event => setResolver(event.target.value)} placeholder="letsencrypt" /></label>}
        <label>Default Compose ingress service<input value={composeService} onChange={event => setComposeService(event.target.value)} placeholder="Optional for a single-service app" /><small>A Compose definition can select its service with x-dispatch-ingress-service.</small></label>
      </div>}
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="form-actions"><button type="button" className="quiet-button" onClick={onClose}>Cancel</button><button type="submit" disabled={busy}>{busy ? "Saving…" : "Save routing"}</button></div>
    </form>
  </section></div>;
}
