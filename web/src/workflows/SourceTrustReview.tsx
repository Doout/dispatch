import { useEffect, useState } from "react";
import { api, type WorkflowRevision } from "../api";

export function SourceTrustReview({ revision, isOwner, onChanged }: { revision: WorkflowRevision; isOwner: boolean; onChanged: () => Promise<void> }) {
  const [review, setReview] = useState(revision.sourceTrust);
  const [confirmed, setConfirmed] = useState(false);
  const [reviewed, setReviewed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  useEffect(() => { setReview(revision.sourceTrust); setConfirmed(false); setReviewed(false); setError(""); setMessage(""); }, [revision.id]);
  if (!review) return null;
  async function act(action: "review" | "approve" | "revoke") {
    if (!review) return;
    setBusy(true); setError(""); setMessage("");
    try {
      if (action === "approve") {
        await api.approvePreviewSourceTrust(revision.id, review.digest, new Date(Date.now() + 60 * 60 * 1000).toISOString());
        setMessage("Approved for one hour. Run the preview again to execute these sources.");
      } else if (action === "revoke" && review.approvalId) {
        await api.revokePreviewSourceTrust(revision.id, review.approvalId);
        setMessage("Approval revoked. New jobs and deployments must pass source review again.");
      }
      setReview(await api.previewSourceTrust(revision.id));
      setReviewed(true); setConfirmed(false);
      if (action !== "review") await onChanged();
    } catch (cause) { setError((cause as Error).message); setReviewed(false); setConfirmed(false); }
    finally { setBusy(false); }
  }
  return <section className="workflow-source-trust" aria-label="Preview source trust">
    <h4>Preview source trust</h4><p>{review.reason}</p>
    <ul>{review.sources.map(source => <li key={source.alias}><strong>{source.alias}</strong>: {source.headRepository} → {source.repository} PR #{source.pullRequest}{source.fork ? " · Fork" : ""}<br /><code>{source.commitSha}</code></li>)}</ul>
    <details><summary>Credentials and environments covered by this review</summary><p>Credential references</p><ul>{review.credentialScope.map(value => <li key={value}><code>{value}</code></li>)}</ul><p>Environments</p><ul>{review.environments.map(value => <li key={value}><code>{value}</code></li>)}</ul><p>Review digest: <code>{review.digest || "Unavailable until source identities are verified"}</code></p></details>
    <button type="button" className="quiet-button" disabled={busy} onClick={() => void act("review")}>Refresh source review</button>
    {isOwner && !review.allowed && review.digest && <><label><input type="checkbox" checked={confirmed} disabled={!reviewed || busy} onChange={event => setConfirmed(event.target.checked)} />I reviewed these exact commits, credential references and environments.</label><button type="button" className="quiet-button" disabled={!reviewed || !confirmed || busy} onClick={() => void act("approve")}>Approve exact sources for one hour</button></>}
    {isOwner && review.approvalId && <button type="button" className="quiet-button" disabled={busy} onClick={() => void act("revoke")}>Revoke source approval</button>}
    {error && <p role="alert" className="form-error">{error}</p>}{message && <p role="status">{message}</p>}
  </section>;
}
