import { useEffect, useId, useRef, useState } from "react";
import {
  ArrowClockwise,
  CheckCircle,
  ShieldCheck,
  WarningCircle,
} from "@phosphor-icons/react";
import { request, type ControllerSettings, type Overview } from "./api";
import "./SettingsPage.css";
import {
  InterfaceSettings,
  type InterfaceSettingsProps,
} from "./interface/InterfaceSettings";
import { uiFeatures, type UIFeatureKey } from "./featureFlags";
import { InstanceConnectionSettings } from "./InstanceConnectionSettings";
import { useHostedTenant } from "./hosted/context";
import type { SettingsSection } from "./routes";

type Props = {
  overview: Overview;
  onChanged?: () => void | Promise<void>;
  interfaceSettings?: InterfaceSettingsProps;
  section?: SettingsSection;
  onSectionChange?: (section: SettingsSection) => void;
  navigationInShell?: boolean;
};

export function SettingsPage(props: Props) {
  // Keep account-specific form state out of the next user's settings.
  return <SettingsContent key={props.overview.identity?.id} {...props} />;
}

function SettingsContent({
  overview,
  onChanged,
  interfaceSettings,
  section: routeSection,
  onSectionChange,
  navigationInShell = false,
}: Props) {
  const owner = overview.identity?.systemRole === "owner";
  const hosted = useHostedTenant();
  const id = useId();
  const [section, setSection] = useState<SettingsSection>(
    interfaceSettings ? "interface" : "features",
  );
  const [connectionVisited, setConnectionVisited] = useState(false);
  const tabs: { key: SettingsSection; label: string }[] = [
    ...(interfaceSettings
      ? [{ key: "interface" as const, label: "Interface" }]
      : []),
    ...(owner && !hosted
      ? [{ key: "connection" as const, label: "Doodship connection" }]
      : []),
    ...(owner ? [{ key: "features" as const, label: "Features" }] : []),
  ];
  const selected =
    tabs.find((tab) => tab.key === (routeSection ?? section))?.key ??
    tabs[0]?.key;
  useEffect(() => {
    if (selected === "connection") setConnectionVisited(true);
  }, [selected]);
  function select(next: SettingsSection) {
    if (onSectionChange) onSectionChange(next);
    else setSection(next);
    if (next === "connection") setConnectionVisited(true);
  }
  return (
    <div
      className={`page-layout settings-page${navigationInShell ? " settings-page-integrated" : ""}`}
    >
      {!navigationInShell && (
        <header className="page-header settings-header">
          <div>
            <h1>Settings</h1>
            <p>Interface preferences and installation settings.</p>
          </div>
        </header>
      )}
      {!navigationInShell && tabs.length > 0 && (
        <div
          className="settings-tabs"
          role="tablist"
          aria-label="Settings sections"
        >
          {tabs.map((tab, index) => (
            <button
              key={tab.key}
              id={`${id}-${tab.key}-tab`}
              type="button"
              role="tab"
              aria-selected={selected === tab.key}
              aria-controls={`${id}-${tab.key}-panel`}
              tabIndex={selected === tab.key ? 0 : -1}
              onClick={() => select(tab.key)}
              onKeyDown={(event) => {
                let next: number;
                if (event.key === "ArrowRight")
                  next = (index + 1) % tabs.length;
                else if (event.key === "ArrowLeft")
                  next = (index + tabs.length - 1) % tabs.length;
                else if (event.key === "Home") next = 0;
                else if (event.key === "End") next = tabs.length - 1;
                else return;
                event.preventDefault();
                select(tabs[next].key);
                document.getElementById(`${id}-${tabs[next].key}-tab`)?.focus();
              }}
            >
              {tab.label}
            </button>
          ))}
        </div>
      )}
      {interfaceSettings && (
        <div
          className="settings-panel"
          id={`${id}-interface-panel`}
          role={navigationInShell ? "region" : "tabpanel"}
          aria-label={navigationInShell ? "Interface" : undefined}
          aria-labelledby={
            navigationInShell ? undefined : `${id}-interface-tab`
          }
          hidden={selected !== "interface"}
          tabIndex={0}
        >
          <InterfaceSettings {...interfaceSettings} />
        </div>
      )}
      {owner && !hosted && (
        <div
          className="settings-panel"
          id={`${id}-connection-panel`}
          role={navigationInShell ? "region" : "tabpanel"}
          aria-label={navigationInShell ? "Doodship connection" : undefined}
          aria-labelledby={
            navigationInShell ? undefined : `${id}-connection-tab`
          }
          hidden={selected !== "connection"}
          tabIndex={0}
        >
          {connectionVisited && (
            <InstanceConnectionSettings
              initialExpanded
              active={selected === "connection"}
            />
          )}
        </div>
      )}
      {owner ? (
        <div
          className="settings-panel"
          id={`${id}-features-panel`}
          role={navigationInShell ? "region" : "tabpanel"}
          aria-label={navigationInShell ? "Features" : undefined}
          aria-labelledby={navigationInShell ? undefined : `${id}-features-tab`}
          hidden={selected !== "features"}
          tabIndex={0}
        >
          <FeatureSettings
            observed={JSON.stringify(overview.controllerSettings)}
            onChanged={onChanged}
          />
        </div>
      ) : (
        <section className="settings-access" role="status">
          <ShieldCheck size={20} />
          <div>
            <h2>Owner access required</h2>
            <p>A controller owner can change installation settings.</p>
          </div>
        </section>
      )}
    </div>
  );
}

type SettingKey = "operationsEnabled" | UIFeatureKey;
type PendingSetting = { key: SettingKey; enabled: boolean };

function settingValue(
  settings: ControllerSettings | undefined,
  key: SettingKey,
) {
  return key === "operationsEnabled"
    ? settings?.operationsEnabled === true
    : settings?.uiFeatures?.[key] === true;
}

function FeatureSettings({
  observed,
  onChanged,
}: {
  observed?: string;
  onChanged?: Props["onChanged"];
}) {
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
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      sequence.current++;
    };
  }, []);
  useEffect(() => {
    if (busy.current) {
      refreshAfterSave.current = true;
      return;
    }
    const requestID = ++sequence.current;
    let current = true;
    setLoading(true);
    setError("");
    setNotice("");
    request<ControllerSettings>("/api/v1/settings")
      .then((value) => {
        if (current && alive.current && sequence.current === requestID)
          setSettings(value);
      })
      .catch((cause) => {
        if (current && alive.current && sequence.current === requestID)
          setError(
            cause instanceof Error
              ? cause.message
              : "Settings could not be loaded.",
          );
      })
      .finally(() => {
        if (current && alive.current && sequence.current === requestID)
          setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [observed, refresh]);

  async function toggle(key: SettingKey, enabled: boolean) {
    if (
      busy.current ||
      loading ||
      !settings ||
      enabled === settingValue(settings, key)
    )
      return;
    busy.current = true;
    setPending({ key, enabled });
    setError("");
    setNotice("");
    const requestID = ++sequence.current;
    const label =
      key === "operationsEnabled"
        ? "Operations"
        : uiFeatures.find((feature) => feature.key === key)!.label;
    const patch =
      key === "operationsEnabled"
        ? { operationsEnabled: enabled }
        : { uiFeatures: { [key]: enabled } };
    try {
      const saved = await request<ControllerSettings>("/api/v1/settings", {
        method: "PUT",
        body: JSON.stringify(patch),
      });
      if (!alive.current || sequence.current !== requestID) return;
      setSettings(saved);
      let navigationFailed = false;
      try {
        await onChanged?.();
      } catch {
        navigationFailed = true;
      }
      if (!alive.current || sequence.current !== requestID) return;
      // Recheck after every save, including concurrent updates that restore the initial value.
      refreshAfterSave.current = false;
      try {
        const latest = await request<ControllerSettings>("/api/v1/settings");
        if (!alive.current || sequence.current !== requestID) return;
        setSettings(latest);
        setNotice(
          navigationFailed
            ? "Setting saved. Navigation could not be refreshed."
            : `${label} ${settingValue(latest, key) ? "enabled" : "disabled"}.`,
        );
      } catch {
        if (alive.current && sequence.current === requestID)
          setNotice(
            "Setting saved. Refresh to check the latest controller value.",
          );
      }
    } catch (cause) {
      if (alive.current && sequence.current === requestID)
        setError(
          cause instanceof Error
            ? cause.message
            : "The setting could not be saved. Try again.",
        );
    } finally {
      busy.current = false;
      if (alive.current && sequence.current === requestID) {
        setPending(undefined);
        if (refreshAfterSave.current) {
          refreshAfterSave.current = false;
          setRefresh((value) => value + 1);
        }
      }
    }
  }

  const saving = pending !== undefined;
  function featureControl(key: SettingKey, helpID: string) {
    const enabled =
      pending?.key === key ? pending.enabled : settingValue(settings, key);
    return (
      <div className="settings-feature-control">
        <span aria-live="polite">
          {pending?.key === key
            ? "Saving…"
            : loading
              ? "Loading…"
              : settings
                ? enabled
                  ? "On"
                  : "Off"
                : "Unavailable"}
        </span>
        <label className="settings-switch">
          <input
            id={`${id}-${key}`}
            type="checkbox"
            role="switch"
            aria-describedby={helpID}
            checked={enabled}
            disabled={loading || saving || !settings}
            onChange={(event) => void toggle(key, event.target.checked)}
          />
          <span aria-hidden="true" />
        </label>
      </div>
    );
  }
  return (
    <>
      <section className="settings-features" aria-labelledby={`${id}-features`}>
        <header>
          <div>
            <h2 id={`${id}-features`}>Features</h2>
            <p>Changes save immediately and apply to all users.</p>
          </div>
          <button
            type="button"
            className="quiet-button"
            disabled={loading || saving}
            onClick={() => {
              setNotice("");
              setRefresh((value) => value + 1);
            }}
          >
            <ArrowClockwise size={14} />
            Refresh
          </button>
        </header>
        <div className="settings-feature-row" aria-busy={loading || saving}>
          <div className="settings-feature-copy">
            <h3>
              <label htmlFor={`${id}-operationsEnabled`}>Operations</label>
              <span>Off by default</span>
            </h3>
            <p id={`${id}-help`}>
              Show activity history, application ownership, cleanup, and
              recovery tools. Existing permissions still apply.
            </p>
            <p>
              Turning this off disables these tools. Saved sign-in access rules
              still apply.
            </p>
          </div>
          {featureControl("operationsEnabled", `${id}-help`)}
        </div>
      </section>
      <section
        className="settings-features settings-experimental"
        aria-labelledby={`${id}-experimental`}
      >
        <header>
          <div>
            <h2 id={`${id}-experimental`}>Experimental features</h2>
            <p>
              Show new pages and controls in both interfaces. Off by default.
            </p>
            <p>
              These switches do not stop running jobs or restrict API access.
            </p>
          </div>
        </header>
        {uiFeatures.map((feature) => (
          <div
            key={feature.key}
            className="settings-feature-row"
            aria-busy={loading || pending?.key === feature.key}
          >
            <div className="settings-feature-copy">
              <h3>
                <label htmlFor={`${id}-${feature.key}`}>{feature.label}</label>
              </h3>
              <p id={`${id}-${feature.key}-help`}>{feature.description}</p>
            </div>
            {featureControl(feature.key, `${id}-${feature.key}-help`)}
          </div>
        ))}
      </section>
      {error && (
        <div className="settings-feedback settings-error" role="alert">
          <WarningCircle size={16} />
          <div>
            <strong>{error}</strong>
            <span>
              {settings
                ? "The last saved settings are shown. Try changing them again, or refresh to check the current values."
                : "Refresh to try loading the controller settings again."}
            </span>
          </div>
        </div>
      )}
      {notice && !error && (
        <p className="settings-feedback settings-success" role="status">
          <CheckCircle size={16} />
          {notice}
        </p>
      )}
    </>
  );
}
