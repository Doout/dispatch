import { useEffect, useRef, useState } from "react";
import { CheckCircle, WarningCircle } from "@phosphor-icons/react";
import { api, type HelmTakeoverReview } from "../api";
import "./HelmTakeoverPanel.css";

export function hasHelmOwnershipConflict(message?: string) {
  return /The Helm release does not belong to this (?:Dispatch|Doodship) application and namespace\./.test(message ?? "");
}

type Props = {
  appId: string;
  appName?: string;
  stageName?: string;
  onRunAgain?: () => void;
  running?: boolean;
};

export function HelmTakeoverPanel({ appId, appName, stageName, onRunAgain, running }: Props) {
  const [review, setReview] = useState<HelmTakeoverReview | null>(null);
  const [stopped, setStopped] = useState(false);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");
  const title = useRef<HTMLHeadingElement>(null);
  const inspectButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (review) title.current?.focus();
  }, [review, done]);

  async function inspect() {
    setBusy(true);
    setError("");
    setStopped(false);
    try {
      setReview(await api.reviewHelmTakeover(appId));
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function closeReview() {
    setReview(null);
    setStopped(false);
    requestAnimationFrame(() => inspectButton.current?.focus());
  }

  async function accept() {
    if (!stopped || !review || busy) return;
    setBusy(true);
    setError("");
    try {
      await api.acceptHelmTakeover(appId, review.version);
      setDone(true);
    } catch (cause) {
      setError((cause as Error).message);
      closeReview();
    } finally {
      setBusy(false);
    }
  }

  const destination = appName || "this application";
  const heading = done ? "Release ownership transferred" : review ? `Take over ${review.release}?` : "Helm release ownership conflict";
  return <section className={`helm-takeover${done ? " is-complete" : ""}`} aria-label="Helm release ownership">
    <div className="helm-takeover-header">
      {done ? <CheckCircle size={20} weight="fill" aria-hidden="true" /> : <WarningCircle size={20} weight="fill" aria-hidden="true" />}
      <div>
        <h3 ref={title} tabIndex={-1}>{heading}</h3>
        {done ? <p role="status">{review?.release} now belongs to {destination}. Retry the deployment when ready.</p>
          : review ? <p>Assign the release to <strong>{destination}</strong>. Running workloads stay in place.</p>
          : <p>{stageName ? `The ${stageName} stage is blocked.` : "Deployment is blocked."} Check who owns the release before retrying.</p>}
      </div>
      {!review && <button ref={inspectButton} type="button" className="quiet-button" disabled={busy} onClick={() => void inspect()}>{busy ? "Inspecting…" : "Inspect release ownership"}</button>}
      {done && onRunAgain && <button type="button" className="quiet-button" disabled={running} onClick={onRunAgain}>{running ? "Starting…" : "Run workflow again"}</button>}
    </div>
    {review && !done && <div className="helm-takeover-review">
      <dl className="helm-takeover-facts">
        <div><dt>Target</dt><dd>{review.target}</dd></div>
        <div><dt>Namespace</dt><dd>{review.namespace}</dd></div>
        <div><dt>Revision</dt><dd>{review.revision}</dd></div>
      </dl>
      <details className="helm-takeover-ids">
        <summary>Ownership details</summary>
        <dl>
          <div><dt>Previous application ID</dt><dd><code>{review.previousAppId}</code></dd></div>
          {review.previousProjectId && <div><dt>Previous project ID</dt><dd><code>{review.previousProjectId}</code></dd></div>}
          <div><dt>New application ID</dt><dd><code>{appId}</code></dd></div>
        </dl>
      </details>
      <div className="helm-takeover-footer">
        <label><input type="checkbox" checked={stopped} disabled={busy} onChange={event => setStopped(event.target.checked)} />The previous controller has stopped managing this release.</label>
        <div className="helm-takeover-actions">
          <button type="button" className="quiet-button" disabled={busy} onClick={closeReview}>Cancel</button>
          <button type="button" className="primary-button" disabled={!stopped || busy} onClick={() => void accept()}>{busy ? "Transferring…" : "Take over release"}</button>
        </div>
      </div>
    </div>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </section>;
}
