import { useState, type FormEvent } from "react";
import { useDialogFocus } from "./useDialogFocus";
import "./UiPreviewToolbar.css";

type Source = "development" | "production";
export type UiPreview = {
  source: Source;
  upstream: string;
  readOnly: boolean;
  productionAccess: boolean;
};

export function uiPreviewConfiguration(): UiPreview | null {
  const contents = document.getElementById("dispatch-ui-preview")?.textContent;
  if (!contents) return null;
  try {
    const value = JSON.parse(contents);
    if (
      (value.source === "development" || value.source === "production") &&
      typeof value.upstream === "string" &&
      typeof value.readOnly === "boolean" &&
      typeof value.productionAccess === "boolean" &&
      new URL(value.upstream).protocol === "https:"
    )
      return value;
  } catch {
    /* Normal installations have no preview metadata. */
  }
  return null;
}

async function previewRequest(path: string, method: string, body?: object) {
  const response = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  if (!response.ok) {
    const problem = await response.json().catch(() => null);
    throw new Error(
      problem?.detail ?? "Unable to update preview access. Try again.",
    );
  }
}

export function UiPreviewToolbar({
  configuration,
}: {
  configuration: UiPreview;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [unlock, setUnlock] = useState(
    configuration.source === "production" && !configuration.productionAccess,
  );
  const selectSource = async (source: Source, propagate = false) => {
    setBusy(true);
    setError("");
    try {
      await previewRequest("/__dispatch_ui/data-source", "POST", { source });
      window.location.reload();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
      if (propagate) throw cause;
    }
  };
  const lock = async () => {
    setBusy(true);
    setError("");
    try {
      await previewRequest("/__dispatch_ui/access", "DELETE");
      window.location.reload();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  };
  const cancelUnlock = () => {
    if (configuration.source === "production") void selectSource("development");
    else setUnlock(false);
  };
  return (
    <>
      <aside
        className="ui-preview-toolbar"
        aria-label="Development UI data source"
      >
        <div>
          <strong>Dev UI</strong>
          <label>
            Data source
            <select
              aria-label="Data source"
              value={configuration.source}
              disabled={busy || unlock}
              onChange={(event) => {
                const source = event.target.value as Source;
                if (source === "production" && !configuration.productionAccess)
                  setUnlock(true);
                else void selectSource(source);
              }}
            >
              <option value="development">Development</option>
              <option value="production">Production</option>
            </select>
          </label>
          {configuration.readOnly && <span>Read only</span>}
        </div>
        {configuration.productionAccess && (
          <button
            type="button"
            aria-label="Lock production access"
            disabled={busy}
            onClick={() => void lock()}
          >
            Lock
          </button>
        )}
        {error ? (
          <span role="alert">{error}</span>
        ) : (
          <span className="ui-preview-upstream">
            {new URL(configuration.upstream).host}
          </span>
        )}
      </aside>
      {unlock && (
        <UnlockProduction
          onCancel={cancelUnlock}
          onUnlocked={() => selectSource("production", true)}
        />
      )}
    </>
  );
}

function UnlockProduction({
  onCancel,
  onUnlocked,
}: {
  onCancel: () => void;
  onUnlocked: () => Promise<void>;
}) {
  const [key, setKey] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const ref = useDialogFocus(() => {
    if (!busy) onCancel();
  });
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await previewRequest("/__dispatch_ui/access", "POST", { key });
      setKey("");
      await onUnlocked();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  };
  return (
    <div className="ui-preview-access-layer">
      <section
        ref={ref}
        className="ui-preview-access"
        role="dialog"
        aria-modal="true"
        aria-labelledby="ui-preview-access-title"
      >
        <h2 id="ui-preview-access-title">Unlock production data</h2>
        <p>
          Enter the preview access key. Access lasts one hour and still requires
          your production login.
        </p>
        <form onSubmit={(event) => void submit(event)} aria-busy={busy}>
          <label>
            Preview access key
            <input
              data-autofocus
              type="password"
              autoComplete="off"
              spellCheck={false}
              value={key}
              onChange={(event) => setKey(event.target.value)}
              required
              disabled={busy}
            />
          </label>
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <div>
            <button
              type="button"
              className="quiet-button"
              onClick={onCancel}
              disabled={busy}
            >
              Cancel
            </button>
            <button className="primary-button" disabled={busy}>
              {busy ? "Unlocking..." : "Unlock production"}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}
