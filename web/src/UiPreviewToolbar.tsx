import { useState } from "react";
import "./UiPreviewToolbar.css";

type Source = "development" | "production";
export type UiPreview = { source: Source; upstream: string; readOnly: boolean };

export function uiPreviewConfiguration(): UiPreview | null {
  const contents = document.getElementById("dispatch-ui-preview")?.textContent;
  if (!contents) return null;
  try {
    const value = JSON.parse(contents);
    if (
      (value.source === "development" || value.source === "production") &&
      typeof value.upstream === "string" &&
      typeof value.readOnly === "boolean" &&
      new URL(value.upstream).protocol === "https:"
    )
      return value;
  } catch {
    /* Normal installations have no preview metadata. */
  }
  return null;
}

export function UiPreviewToolbar({
  configuration,
}: {
  configuration: UiPreview;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const changeSource = async (source: Source) => {
    setBusy(true);
    setError("");
    try {
      const response = await fetch("/__dispatch_ui/data-source", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ source }),
      });
      if (!response.ok)
        throw new Error("Unable to switch data sources. Try again.");
      window.location.reload();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  };
  return (
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
            disabled={busy}
            onChange={(event) =>
              void changeSource(event.target.value as Source)
            }
          >
            <option value="development">Development</option>
            <option value="production">Production</option>
          </select>
        </label>
        {configuration.readOnly && <span>Read only</span>}
      </div>
      {error ? (
        <span role="alert">{error}</span>
      ) : (
        <span className="ui-preview-upstream">
          {new URL(configuration.upstream).host}
        </span>
      )}
    </aside>
  );
}
