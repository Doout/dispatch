import { useId } from "react";
import { Browsers } from "@phosphor-icons/react";

export type InterfaceSettingsProps = { enabled: boolean; onChange: (enabled: boolean) => void; error?: string };

export function InterfaceSettings({ enabled, onChange, error }: InterfaceSettingsProps) {
  const id = useId();
  return <section className="settings-features interface-settings" aria-labelledby={`${id}-title`}>
    <header><div><h2 id={`${id}-title`}><Browsers size={17} />Interface</h2><p>Saved for your account in this browser.</p></div></header>
    <div className="settings-feature-row">
      <span className="settings-feature-icon"><Browsers size={21} /></span>
      <div className="settings-feature-copy"><h3><label htmlFor={`${id}-switch`}>New interface</label></h3><p id={`${id}-help`}>Use grouped navigation and tables for applications, parallel deployments, and runs. Turn this off to return to the current interface.</p></div>
      <div className="settings-feature-control"><span>{enabled ? "On" : "Off"}</span><label className="settings-switch"><input id={`${id}-switch`} type="checkbox" role="switch" aria-describedby={`${id}-help`} checked={enabled} onChange={event => onChange(event.target.checked)} /><span aria-hidden="true" /></label></div>
    </div>
    {error && <p className="settings-feedback settings-error" role="alert">{error}</p>}
  </section>;
}
