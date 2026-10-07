import { useEffect, useId, useRef, useState } from "react";
import { ArrowClockwise, CheckCircle, ListChecks, ShieldCheck, SlidersHorizontal, WarningCircle } from "@phosphor-icons/react";
import { request, type ControllerSettings, type Overview } from "./api";
import "./SettingsPage.css";
import { InterfaceSettings, type InterfaceSettingsProps } from "./interface/InterfaceSettings";
import { uiFeatures, type UIFeatureKey } from "./featureFlags";

type Props = { overview: Overview; onChanged?: () => void | Promise<void>; interfaceSettings?: InterfaceSettingsProps };

export function SettingsPage({ overview, onChanged, interfaceSettings }: Props) {
  const owner = overview.identity?.systemRole === "owner";
  const observed = JSON.stringify(overview.controllerSettings);
  return <div className="page-layout settings-page">
    <header className="page-header settings-header"><div><h1>Settings</h1></div></header>
    {interfaceSettings && <InterfaceSettings {...interfaceSettings} />}
    {owner ? <FeatureSettings key={overview.identity?.id} observed={observed} onChanged={onChanged} /> : <section className="settings-access" role="status"><ShieldCheck size={22} /><h2>Owner access required</h2><p>A controller owner can view and change these settings.</p></section>}
  </div>;
}

type SettingKey = "operationsEnabled" | UIFeatureKey;
type PendingSetting = { key: SettingKey; enabled: boolean };

function settingValue(settings: ControllerSettings | undefined, key: SettingKey) {
  return key === "operationsEnabled" ? settings?.operationsEnabled === true : settings?.uiFeatures?.[key] === true;
}

function FeatureSettings({ observed, onChanged }: { observed?: string; onChanged?: Props["onChanged"] }) {
  const [settings, setSettings] = useState<ControllerSettings>();
  const [pending, setPending] = useState<PendingSetting>();
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

  async function toggle(key: SettingKey, enabled: boolean) {
    if (busy.current || loading || !settings || enabled === settingValue(settings, key)) return;
    busy.current = true; setPending({ key, enabled }); setError(""); setNotice("");
    const requestID = ++sequence.current;
    const label = key === "operationsEnabled" ? "Operations" : uiFeatures.find(feature => feature.key === key)!.label;
    const patch = key === "operationsEnabled" ? { operationsEnabled: enabled } : { uiFeatures: { [key]: enabled } };
    try {
      const saved = await request<ControllerSettings>("/api/v1/settings", { method: "PUT", body: JSON.stringify(patch) });
      if (!alive.current || sequence.current !== requestID) return;
      setSettings(saved);
      let navigationFailed = false;
      try { await onChanged?.(); }
      catch { navigationFailed = true; }
      if (!alive.current || sequence.current !== requestID) return;
      // Recheck after every save, including concurrent updates that restore the initial value.
      refreshAfterSave.current = false;
      try {
        const latest = await request<ControllerSettings>("/api/v1/settings");
        if (!alive.current || sequence.current !== requestID) return;
        setSettings(latest);
        setNotice(navigationFailed ? "Setting saved. Navigation could not be refreshed." : `${label} ${settingValue(latest, key) ? "enabled" : "disabled"}.`);
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
  function featureControl(key: SettingKey, helpID: string) {
    const enabled = pending?.key === key ? pending.enabled : settingValue(settings, key);
    return <div className="settings-feature-control"><span aria-live="polite">{pending?.key === key ? "Saving…" : loading ? "Loading…" : settings ? enabled ? "On" : "Off" : "Unavailable"}</span><label className="settings-switch"><input id={`${id}-${key}`} type="checkbox" role="switch" aria-describedby={helpID} checked={enabled} disabled={loading || saving || !settings} onChange={event => void toggle(key, event.target.checked)} /><span aria-hidden="true" /></label></div>;
  }
  return <>
  <section className="settings-features" aria-labelledby={`${id}-features`}>
    <header><div><h2 id={`${id}-features`}><SlidersHorizontal size={17} />Features</h2><p>Changes save immediately and apply across this controller.</p></div><button type="button" className="quiet-button" disabled={loading || saving} onClick={() => { setNotice(""); setRefresh(value => value + 1); }}><ArrowClockwise size={14} />Refresh</button></header>
    <div className="settings-feature-row" aria-busy={loading || saving}>
      <span className="settings-feature-icon"><ListChecks size={21} /></span>
      <div className="settings-feature-copy"><h3><label htmlFor={`${id}-operationsEnabled`}>Operations</label><span>Off by default</span></h3><p id={`${id}-help`}>Enable activity history, application ownership, cleanup, and recovery tools for users who already have access. Existing roles still determine which tools each person can use.</p><p>Turning this off disables the management tools. Saved access mappings still apply at sign-in.</p></div>
      {featureControl("operationsEnabled", `${id}-help`)}
    </div>
  </section>
  <section className="settings-features settings-experimental" aria-labelledby={`${id}-experimental`}>
    <header><div><h2 id={`${id}-experimental`}><SlidersHorizontal size={17} />Experimental UI</h2><p>Off by default for this controller. Automated checks exist, but the workflows below still need validation.</p><p>These switches control pages and actions in both interfaces. Existing jobs and APIs keep running. Access still depends on each user's role.</p></div></header>
    {uiFeatures.map(feature => <div key={feature.key} className="settings-feature-row" aria-busy={loading || pending?.key === feature.key}>
      <span className="settings-feature-icon"><SlidersHorizontal size={21} /></span>
      <div className="settings-feature-copy"><h3><label htmlFor={`${id}-${feature.key}`}>{feature.label}</label><span>{feature.validation}</span></h3><p id={`${id}-${feature.key}-help`}>{feature.description}</p></div>
      {featureControl(feature.key, `${id}-${feature.key}-help`)}
    </div>)}
  </section>
  {error && <div className="settings-feedback settings-error" role="alert"><WarningCircle size={16} /><div><strong>{error}</strong><span>{settings ? "The last saved settings are shown. Try changing them again, or refresh to check the current values." : "Refresh to try loading the controller settings again."}</span></div></div>}
  {notice && !error && <p className="settings-feedback settings-success" role="status"><CheckCircle size={16} />{notice}</p>}
  </>;
}
