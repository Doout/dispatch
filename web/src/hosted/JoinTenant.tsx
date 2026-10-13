import { useState, type FormEvent } from "react";
import { ArrowLeft } from "@phosphor-icons/react";
import { hostedRequest } from "./client";

function tenantName(value: string, origin: string) {
  const input = value.trim().toLowerCase();
  if (/^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(input)) return input;
  try {
    const address = new URL(input.includes("://") ? input : `https://${input}`);
    const root = new URL(origin).hostname;
    if (
      address.protocol !== "https:" ||
      address.username ||
      address.password ||
      address.port ||
      address.search ||
      address.hash ||
      address.pathname !== "/"
    )
      return "";
    const suffix = `.${root}`;
    if (!address.hostname.endsWith(suffix)) return "";
    const name = address.hostname.slice(0, -suffix.length);
    return /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(name) ? name : "";
  } catch {
    return "";
  }
}

export function JoinTenant({
  origin,
  onRequested,
  onCancel,
}: {
  origin: string;
  onRequested: () => Promise<void>;
  onCancel: () => void;
}) {
  const [address, setAddress] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    const slug = tenantName(address, origin);
    if (!slug) {
      setError("Enter an instance name or its address.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await hostedRequest("/account/join-requests", "POST", { slug });
      await onRequested();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section
      className="hosted-tenant-create"
      aria-labelledby="join-tenant-title"
    >
      <button
        type="button"
        className="hosted-back-button"
        onClick={onCancel}
        disabled={busy}
      >
        <ArrowLeft size={16} aria-hidden="true" />
        Back to instances
      </button>
      <header className="hosted-page-heading">
        <h1 id="join-tenant-title">Join instance</h1>
      </header>
      <form
        className="hosted-panel hosted-form hosted-create-form"
        onSubmit={(event) => void submit(event)}
      >
        <label className="hosted-form-wide">
          Instance name or address
          <input
            value={address}
            onChange={(event) => {
              setAddress(event.target.value);
              setError("");
            }}
            placeholder="example"
            required
            disabled={busy}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
          />
        </label>
        <p className="hosted-field-hint">
          An instance owner must approve your request before you can access it.
        </p>
        {error && (
          <p className="form-error hosted-form-wide" role="alert">
            {error}
          </p>
        )}
        <div className="hosted-row-actions hosted-form-wide">
          <button className="primary-button" disabled={busy}>
            {busy ? "Sending..." : "Request to join"}
          </button>
          <button
            type="button"
            className="quiet-button"
            disabled={busy}
            onClick={onCancel}
          >
            Cancel
          </button>
        </div>
      </form>
    </section>
  );
}
