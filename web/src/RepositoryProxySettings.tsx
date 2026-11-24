import { useEffect, useState, type FormEvent } from "react";
import { CaretDown, CheckCircle, Globe, WarningCircle } from "@phosphor-icons/react";
import { request } from "./api";

const endpoint = "/api/v1/settings/repository-proxy";
type ProxySettings = { url: string; noProxy: string; authenticated: boolean };

export function RepositoryProxySettings() {
  const [opened, setOpened] = useState(false);
  return <details className="settings-features settings-proxy" onToggle={event => {
    if (event.currentTarget.open) setOpened(true);
  }}>
    <summary><h2><Globe size={17} />Repository proxy</h2><CaretDown size={16} aria-hidden="true" /></summary>
    {opened && <RepositoryProxyForm />}
  </details>;
}

function RepositoryProxyForm() {
  const [settings, setSettings] = useState<ProxySettings>();
  const [url, setURL] = useState("");
  const [noProxy, setNoProxy] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    request<ProxySettings>(endpoint, { signal: controller.signal }).then(value => {
      setSettings(value);
      setNoProxy(value.noProxy);
    }).catch(cause => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Could not load proxy settings.");
    });
    return () => controller.abort();
  }, [reload]);

  async function save(remove = false) {
    if (!settings || saving) return;
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const proxyURL = url.trim();
      const input = remove ? { url: "", noProxy: "" } : { url: proxyURL || undefined, noProxy };
      const saved = await request<ProxySettings>(endpoint, { method: "PUT", body: JSON.stringify(input) });
      setSettings(saved);
      setURL("");
      setNoProxy(saved.noProxy);
      setNotice(remove ? "Repository proxy removed." : "Repository proxy saved.");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save proxy settings.");
    } finally {
      setSaving(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void save();
  }

  const changed = Boolean(url.trim() || (settings?.url && noProxy !== settings.noProxy));
  return <form className="connection-form settings-proxy-form" onSubmit={submit} aria-busy={saving || (!settings && !error)}>
    <p>Route GitHub API calls and HTTP or HTTPS Git checkouts through a forward proxy. Changes apply to new API requests and worker runs.</p>
    {!settings && !error && <p role="status">Loading proxy settings…</p>}
    {settings && <>
      <div className="connection-grid settings-proxy-fields">
        <label>Proxy URL
          <input type="url" autoComplete="off" spellCheck={false} maxLength={2048} value={url} disabled={saving}
            placeholder={settings.url || "http://proxy.example.com:3128"} onChange={event => setURL(event.target.value)} />
          <small>{settings.url
            ? `Saved proxy ${settings.url}${settings.authenticated ? " with authentication." : "."} Enter a new URL to replace it.`
            : "Dispatch uses the controller environment until you save a proxy here."}</small>
        </label>
        <label>Bypass list
          <input type="text" spellCheck={false} maxLength={2048} value={noProxy} disabled={saving}
            placeholder="localhost,.internal.example.com" onChange={event => setNoProxy(event.target.value)} />
          <small>Separate hosts, domains, or IP ranges with commas.</small>
        </label>
      </div>
      <div className="settings-proxy-actions">
        <button type="submit" className="primary-button" disabled={saving || !changed}>{saving ? "Saving…" : "Save proxy"}</button>
        {settings.url && <button type="button" className="quiet-button" disabled={saving} onClick={() => void save(true)}>Remove proxy</button>}
      </div>
    </>}
    {error && <p className="settings-feedback settings-error" role="alert"><WarningCircle size={16} />{error}</p>}
    {error && !settings && <button type="button" className="quiet-button" onClick={() => { setError(""); setReload(value => value + 1); }}>Retry proxy settings</button>}
    {notice && <p className="settings-feedback settings-success" role="status"><CheckCircle size={16} />{notice}</p>}
  </form>;
}
