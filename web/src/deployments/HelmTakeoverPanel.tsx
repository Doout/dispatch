import { useState } from "react";
import { api, type HelmTakeoverReview } from "../api";

export function hasHelmOwnershipConflict(message?: string) {
  return message?.includes("The Helm release does not belong to this Dispatch application and namespace.");
}

export function HelmTakeoverPanel({ appId, appName }: { appId: string; appName?: string }) {
  const [review, setReview] = useState<HelmTakeoverReview | null>(null);
  const [stopped, setStopped] = useState(false);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");

  async function inspect() {
    setBusy(true); setError(""); setStopped(false);
    try { setReview(await api.reviewHelmTakeover(appId)); }
    catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  }
  async function accept() {
    if (!stopped || !review || busy) return;
    setBusy(true); setError("");
    try { await api.acceptHelmTakeover(appId, review.version); setDone(true); }
    catch (cause) { setError((cause as Error).message); setReview(null); setStopped(false); }
    finally { setBusy(false); }
  }
  return <section className="deployment-evidence deployment-detail-surface" aria-label="Helm release ownership">
    <h3>Helm release ownership</h3>
    {done ? <p role="status">Ownership transferred. Retry the failed deployment or workflow stage.</p> : review ? <>
      <p>Transfer <strong>{review.namespace}/{review.release}</strong> on {review.target} to {appName || appId}?</p>
      <p>Revision {review.revision} belongs to application <code>{review.previousAppId}</code>{review.previousProjectId && <> in project <code>{review.previousProjectId}</code></>}. Takeover updates the current Helm ownership record. Running workloads keep serving.</p>
      <label><input type="checkbox" checked={stopped} disabled={busy} onChange={event => setStopped(event.target.checked)} /> The previous controller has stopped managing this release.</label>
      <div className="form-actions">
        <button className="quiet-button" disabled={busy} onClick={() => setReview(null)}>Cancel</button>
        <button className="primary-button" disabled={!stopped || busy} onClick={() => void accept()}>{busy ? "Transferring…" : "Take over release"}</button>
      </div>
    </> : <button className="quiet-button" disabled={busy} onClick={() => void inspect()}>{busy ? "Inspecting…" : "Inspect release ownership"}</button>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </section>;
}
