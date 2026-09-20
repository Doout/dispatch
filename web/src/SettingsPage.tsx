import { useEffect, useId, useRef, useState } from "react";
import { ArrowClockwise, CheckCircle, ListChecks, ShieldCheck, SlidersHorizontal, WarningCircle } from "@phosphor-icons/react";
import { request, type ControllerSettings, type Overview } from "./api";
import "./SettingsPage.css";

type Props = { overview: Overview; onChanged?: () => void | Promise<void> };

export function SettingsPage({ overview, onChanged }: Props) {
  const owner = overview.identity?.systemRole === "owner";
  const observed = overview.controllerSettings?.operationsEnabled;
  return <div className="page-layout settings-page">
    <header className="page-header settings-header"><div><h1>Settings</h1><p>Manage features for this Dispatch controller.</p></div><span><ShieldCheck size={14} />Controller-wide</span></header>
    {owner ? <FeatureSettings key={overview.identity?.id} observed={observed} onChanged={onChanged} /> : <section className="settings-access" role="status"><ShieldCheck size={22} /><h2>Owner access required</h2><p>A controller owner can view and change these settings.</p></section>}
  </div>;
}

function FeatureSettings({ observed, onChanged }: { observed?: boolean; onChanged?: Props["onChanged"] }) {
  const [settings, setSettings] = useState<ControllerSettings>();
  const [pending, setPending] = useState<boolean>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [refresh, setRefresh] = useState(0);
  const alive = useRef(true);
  const busy = useRef(false);
  const refreshAfterSave = useRef(false);
  const sequence = useRef(0);
  const id = useId();
  useEffect(() => { alive.current = true; return () => { alive.current = false; sequence.current++; }; }, []);
  useEffect(() => {
    if (busy.current) { refreshAfterSave.current = true; return; }
    const requestID = ++sequence.current;
    let current = true;
    setLoading(true); setError(""); setNotice("");
    request<ControllerSettings>("/api/v1/settings").then(value => {
      if (current && alive.current && sequence.current === requestID) setSettings(value);
    }).catch(cause => {
      if (current && alive.current && sequence.current === requestID) setError(cause instanceof Error ? cause.message : "Settings could not be loaded.");
    }).finally(() => {
      if (current && alive.current && sequence.current === requestID) setLoading(false);
    });
    return () => { current = false; };
  }, [observed, refresh]);

  async function toggle(enabled: boolean) {
    if (busy.current || loading || !settings || enabled === settings.operationsEnabled) return;
    busy.current = true; setPending(enabled); setError(""); setNotice("");
    const requestID = ++sequence.current;
    try {
      const saved = await request<ControllerSettings>("/api/v1/settings", { method: "PUT", body: JSON.stringify({ operationsEnabled: enabled }) });
      if (!alive.current || sequence.current !== requestID) return;
      setSettings(saved);
      let navigationFailed = false;
      try { await onChanged?.(); }
      catch { navigationFailed = true; }
      if (!alive.current || sequence.current !== requestID) return;
      // Another owner may restore the original value before the overview refresh,
      // leaving its boolean unchanged. Read persisted settings after every save.
      refreshAfterSave.current = false;
      try {
        const latest = await request<ControllerSettings>("/api/v1/settings");
        if (!alive.current || sequence.current !== requestID) return;
        setSettings(latest);
        setNotice(navigationFailed ? "Setting saved. Navigation could not be refreshed." : `Operations ${latest.operationsEnabled ? "enabled" : "disabled"}.`);
      } catch {
        if (alive.current && sequence.current === requestID) setNotice("Setting saved. Refresh to check the latest controller value.");
      }
    } catch (cause) {
      if (alive.current && sequence.current === requestID) setError(cause instanceof Error ? cause.message : "The setting could not be saved. Try again.");
    } finally {
      busy.current = false;
      if (alive.current && sequence.current === requestID) {
        setPending(undefined);
        if (refreshAfterSave.current) { refreshAfterSave.current = false; setRefresh(value => value + 1); }
      }
    }
  }

  const saving = pending !== undefined;
  const enabled = pending ?? settings?.operationsEnabled ?? false;
  return <section className="settings-features" aria-labelledby={`${id}-features`}>
    <header><div><h2 id={`${id}-features`}><SlidersHorizontal size={17} />Features</h2><p>Changes save immediately and apply across this controller.</p></div><button type="button" className="quiet-button" disabled={loading || saving} onClick={() => { setNotice(""); setRefresh(value => value + 1); }}><ArrowClockwise size={14} />Refresh</button></header>
    <div className="settings-feature-row" aria-busy={loading || saving}>
      <span className="settings-feature-icon"><ListChecks size={21} /></span>
      <div className="settings-feature-copy"><h3><label htmlFor={`${id}-operations`}>Operations</label><span>Off by default</span></h3><p id={`${id}-help`}>Enable activity history, application ownership, cleanup, and recovery tools for users who already have access. Existing roles still determine which tools each person can use.</p><p>Turning this off disables the management tools. Saved access mappings still apply at sign-in.</p></div>
      <div className="settings-feature-control"><span aria-live="polite">{saving ? "Saving…" : loading ? "Loading…" : settings ? enabled ? "On" : "Off" : "Unavailable"}</span><label className="settings-switch"><input id={`${id}-operations`} type="checkbox" role="switch" aria-describedby={`${id}-help`} checked={enabled} disabled={loading || saving || !settings} onChange={event => void toggle(event.target.checked)} /><span aria-hidden="true" /></label></div>
    </div>
    {error && <div className="settings-feedback settings-error" role="alert"><WarningCircle size={16} /><div><strong>{error}</strong><span>{settings ? "The last saved setting is shown. Try changing it again, or refresh to check the current value." : "Refresh to try loading the controller settings again."}</span></div></div>}
    {notice && !error && <p className="settings-feedback settings-success" role="status"><CheckCircle size={16} />{notice}</p>}
  </section>;
}
