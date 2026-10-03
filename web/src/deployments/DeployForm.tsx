import { releaseClient, type ReleasePreview } from "./releaseClient";
import { FormEvent, useEffect, useState } from "react";
import { api, App as AppModel, Deployment } from "../api";

export function DeployForm({
  apps,
  initialAppID,
  onComplete,
  onCancel,
}: {
  apps: AppModel[];
  initialAppID?: string;
  onComplete: (id: string) => Promise<void>;
  onCancel: () => void;
}) {
  const [appID, setAppID] = useState(
    initialAppID && apps.some((app) => app.id === initialAppID)
      ? initialAppID
      : (apps[0]?.id ?? ""),
  );
  const [commit, setCommit] = useState("HEAD");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<ReleasePreview>();
  const [acknowledged, setAcknowledged] = useState(false);
  const selectedConfiguration = JSON.stringify(apps.find(app => app.id === appID));
  useEffect(() => { setPreview(undefined); setAcknowledged(false); }, [appID, commit, selectedConfiguration]);
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const app = apps.find((item) => item.id === appID);
      const revision =
        app?.buildType === "helm" && !app.sourceRepo
          ? "chart"
          : app?.sourceRepo
            ? commit
            : "inline";
      if (!preview || !preview.ready) {
        const result = await releaseClient.preview(appID, revision);
        setPreview(result);
        setAcknowledged(false);
        return;
      }
      if (preview.checks.some(check => check.state === "unavailable") && !acknowledged) return;
      if (!preview.review) { setError("Refresh the preview before deployment."); setPreview(undefined); return; }
      const created = await api.deploy(appID, preview.revision || revision, preview.review);
      await onComplete(created.id);
    } catch (cause) {
      setError((cause as Error).message);
      if ((cause as Error & { status?: number }).status === 409) { setPreview(undefined); setAcknowledged(false); }
    } finally {
      setBusy(false);
    }
  }
  const selectedApp = apps.find((app) => app.id === appID);
  const fixedSource = selectedApp && !selectedApp.sourceRepo;
  const sourceLabel =
    selectedApp?.buildType === "helm" ? "Helm chart" : "Compose file";
  return (
    <form className="resource-form" onSubmit={submit} aria-busy={busy}>
      <label>
        <span>Application</span>
        <select
          disabled={busy}
          value={appID}
          onChange={(event) => setAppID(event.target.value)}
        >
          {apps.map((app) => (
            <option value={app.id} key={app.id}>
              {app.name}
            </option>
          ))}
        </select>
      </label>
      {fixedSource ? (
        <label>
          <span>Source</span>
          <input value={sourceLabel} disabled />
        </label>
      ) : (
        <label>
          <span>Source revision</span>
          <input
            disabled={busy}
            value={commit}
            onChange={(event) => setCommit(event.target.value)}
            placeholder="Branch, tag, or commit"
            required
            spellCheck={false}
          />
        </label>
      )}
      {preview && <section className="deploy-preview wide" aria-label="Deployment preview"><h3>{preview.ready ? "Review deployment" : "Validation needs attention"}</h3><p>{preview.target} · {preview.namespace || "Local runtime"}{preview.release ? ` · ${preview.release}` : ""}</p><code>{preview.revision}</code><ul>{preview.checks.map(check => <li key={check.name} className={check.state}><strong>{check.name}: {check.state}</strong><span>{check.message}</span></li>)}</ul>{preview.bindings?.length > 0 && <p>Services: {preview.bindings.map(binding => `${binding.alias} (r${binding.revision})`).join(", ")}</p>}{preview.comparison?.available && <details><summary>{preview.comparison.changes.length} saved input changes</summary><div className="history-diff"><table><thead><tr><th>Field</th><th>Before</th><th>After</th></tr></thead><tbody>{preview.comparison.changes.map(change => <tr key={change.path}><td><code>{change.path}</code></td><td><code>{typeof change.before === "string" ? change.before : JSON.stringify(change.before)}</code></td><td><code>{typeof change.after === "string" ? change.after : JSON.stringify(change.after)}</code></td></tr>)}</tbody></table></div></details>}<p>{preview.message}</p>{preview.ready && preview.checks.some(check => check.state === "unavailable") && <label className="deploy-preview-acknowledge"><input type="checkbox" checked={acknowledged} onChange={event => setAcknowledged(event.target.checked)} />I reviewed the checks that can only run during deployment.</label>}<button type="button" className="quiet-button" disabled={busy} onClick={() => {setPreview(undefined);setAcknowledged(false);}}>Change inputs</button></section>}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions">
        <button type="button" className="quiet-button" onClick={onCancel}>
          Cancel
        </button>
        <button className="primary-button" disabled={busy || !appID || !!(preview?.ready && preview.checks.some(check => check.state === "unavailable") && !acknowledged)}>
          {busy ? preview?.ready ? "Starting deployment…" : "Checking deployment…" : preview?.ready ? "Deploy reviewed revision" : preview ? "Retry preview" : "Preview deployment"}
        </button>
      </div>
    </form>
  );
}
