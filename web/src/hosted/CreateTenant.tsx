import { useEffect, useRef, useState, type FormEvent } from "react";
import { HostedError, hostedRequest, type Tenant } from "./client";

// Keep these rules aligned with tenancy.ValidSlug. The API checks them again.
const reservedNames = new Set([
  "www",
  "api",
  "admin",
  "auth",
  "login",
  "platform",
  "settings",
  "assets",
  "static",
  "healthz",
  "mail",
  "smtp",
  "ns",
  "ns1",
  "ns2",
  "_acme-challenge",
]);
const nameTakenMessage =
  "A tenant already uses this name. Choose another name.";
function nameProblem(name: string, tenants: Tenant[]) {
  if (!name) return "Enter a tenant name.";
  if (name.length > 63) return "Use 63 characters or fewer.";
  if (!/^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(name))
    return "Start with a letter. Use lowercase letters, numbers and hyphens, and end with a letter or number.";
  if (reservedNames.has(name))
    return `"${name}" is reserved. Choose another tenant name.`;
  if (tenants.some((tenant) => tenant.slug === name)) return nameTakenMessage;
  return "";
}

export function CreateTenant({
  origin,
  tenants,
  onCreated,
  onCancel,
}: {
  origin: string;
  tenants: Tenant[];
  onCreated: (tenant: Tenant) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(""),
    [ownerEmail, setOwnerEmail] = useState(""),
    [nameTouched, setNameTouched] = useState(false),
    [conflictName, setConflictName] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const nameInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    nameInput.current?.focus();
  }, []);
  useEffect(() => {
    if (conflictName && !busy) nameInput.current?.focus();
  }, [conflictName, busy]);
  const hostname = new URL(origin).host;
  const validationError =
    nameProblem(name, tenants) ||
    (name && name === conflictName ? nameTakenMessage : "");
  const nameError = nameTouched ? validationError : "";
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    setNameTouched(true);
    if (validationError) {
      nameInput.current?.focus();
      return;
    }
    setBusy(true);
    setError("");
    try {
      const tenant = await hostedRequest<Tenant>("/platform/tenants", "POST", {
        name,
        slug: name,
        ownerEmail: ownerEmail.trim(),
      });
      await onCreated(tenant);
    } catch (cause) {
      if (cause instanceof HostedError && cause.status === 409) {
        setConflictName(name);
      } else {
        setError((cause as Error).message);
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <section
      className="hosted-panel hosted-tenant-create"
      aria-labelledby="create-tenant-title"
    >
      <h2 id="create-tenant-title">Create tenant</h2>
      <p className="hosted-panel-description">
        The tenant name becomes its subdomain. Assign an owner to manage it.
      </p>
      <form
        className="hosted-form hosted-create-form"
        onSubmit={(event) => void submit(event)}
        onKeyDown={(event) => {
          if (event.key === "Escape" && !busy) {
            event.preventDefault();
            onCancel();
          }
        }}
      >
        <div className="hosted-tenant-name-field">
          <label htmlFor="tenant-name">Tenant name</label>
          <div className="hosted-domain-input">
            <input
              id="tenant-name"
              ref={nameInput}
              value={name}
              onChange={(event) => setName(event.target.value.toLowerCase())}
              onBlur={() => setNameTouched(true)}
              onInvalid={() => setNameTouched(true)}
              required
              disabled={busy}
              placeholder="agentops"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              aria-invalid={Boolean(nameError)}
              aria-describedby={`tenant-name-help${nameError ? " tenant-name-error" : ""}`}
            />
            <span aria-hidden="true">.{hostname}</span>
          </div>
          <small id="tenant-name-help" className="hosted-field-hint">
            1 to 63 characters. Start with a letter; use letters, numbers and
            hyphens.
          </small>
          {nameError && (
            <p
              id="tenant-name-error"
              className="hosted-field-error"
              role="alert"
            >
              {nameError}
            </p>
          )}
        </div>
        <div className="hosted-tenant-address">
          <span>Tenant address</span>
          <output htmlFor="tenant-name">
            {name && !validationError
              ? `${name}.${hostname}`
              : `your-tenant.${hostname}`}
          </output>
        </div>
        <label className="hosted-form-wide">
          <span id="tenant-owner-label">Owner email</span>
          <input
            type="email"
            value={ownerEmail}
            onChange={(event) => setOwnerEmail(event.target.value)}
            required
            disabled={busy}
            autoComplete="email"
            aria-labelledby="tenant-owner-label"
            aria-describedby="tenant-owner-help"
          />
          <small id="tenant-owner-help" className="hosted-field-hint">
            The owner manages deployments and members. Access is limited to
            tenant members, including the owner.
          </small>
        </label>
        {error && (
          <p role="alert" className="form-error hosted-form-wide">
            {error}
          </p>
        )}
        <div className="hosted-row-actions hosted-form-wide">
          <button className="primary-button" disabled={busy}>
            {busy ? "Creating..." : "Create tenant"}
          </button>
          <button
            type="button"
            className="quiet-button"
            onClick={onCancel}
            disabled={busy}
          >
            Cancel
          </button>
        </div>
      </form>
    </section>
  );
}
