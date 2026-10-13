import { useId } from "react";

export type InterfaceSettingsProps = {
  enabled: boolean;
  onChange: (enabled: boolean) => void;
  error?: string;
};

export function InterfaceSettings({
  enabled,
  onChange,
  error,
}: InterfaceSettingsProps) {
  const id = useId();
  return (
    <section
      className="settings-features interface-settings"
      aria-labelledby={`${id}-title`}
    >
      <header>
        <div>
          <h2 id={`${id}-title`}>Interface</h2>
          <p>Saved for your account in this browser.</p>
        </div>
      </header>
      <div className="settings-feature-row">
        <div className="settings-feature-copy">
          <h3>
            <label htmlFor={`${id}-switch`}>New interface</label>
          </h3>
          <p id={`${id}-help`}>
            Group applications, parallel deployments, and runs in the main
            navigation. Turn this off to use the original layout.
          </p>
        </div>
        <div className="settings-feature-control">
          <span>{enabled ? "On" : "Off"}</span>
          <label className="settings-switch">
            <input
              id={`${id}-switch`}
              type="checkbox"
              role="switch"
              aria-describedby={`${id}-help`}
              checked={enabled}
              onChange={(event) => onChange(event.target.checked)}
            />
            <span aria-hidden="true" />
          </label>
        </div>
      </div>
      {error && (
        <p className="settings-feedback settings-error" role="alert">
          {error}
        </p>
      )}
    </section>
  );
}
