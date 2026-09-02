import { FormEvent, useMemo, useState } from "react";
import {
  ArrowClockwise,
  ArrowSquareOut,
  CaretDown,
  Check,
  Copy,
  GithubLogo,
  Key,
  PencilSimple,
  Plus,
  Trash,
  X,
} from "@phosphor-icons/react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { api, AuthProvider } from "./api";
import { useDialogFocus } from "./useDialogFocus";

export function SignInMethods({
  providers,
  onChanged,
}: {
  providers: AuthProvider[];
  onChanged: () => void;
}) {
  const [editor, setEditor] = useState<AuthProvider | null | undefined>(
    undefined,
  );
  const [removing, setRemoving] = useState<AuthProvider | null>(null);
  const [checking, setChecking] = useState("");
  const [error, setError] = useState("");
  async function checkHost(provider: AuthProvider) {
    setChecking(provider.id);
    setError("");
    try {
      await api.verifyAuthProvider(provider.id);
      onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setChecking("");
    }
  }
  if (!providers.length)
    return (
      <div className="signin-empty">
        <GithubLogo size={24} />
        <div>
          <strong>No external sign-in methods</strong>
          <span>Local owner access remains available.</span>
        </div>
        <button className="quiet-button" onClick={() => setEditor(null)}>
          <Plus size={15} />
          Add method
        </button>
        {editor !== undefined && (
          <ProviderEditor
            provider={editor}
            onClose={() => setEditor(undefined)}
            onSaved={() => {
              setEditor(undefined);
              onChanged();
            }}
          />
        )}
      </div>
    );
  return (
    <>
      <div className="signin-toolbar">
        {error && (
          <span className="form-error" role="alert">
            {error}
          </span>
        )}
        <button className="quiet-button" onClick={() => setEditor(null)}>
          <Plus size={15} />
          Add method
        </button>
      </div>
      <div className="access-table-wrap">
        <table className="access-table signin-table">
          <thead>
            <tr>
              <th>Method</th>
              <th>GitHub host</th>
              <th>New accounts</th>
              <th>Status</th>
              <th>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {providers.map((provider) => (
              <tr key={provider.id}>
                <td>
                  <strong>{provider.name}</strong>
                  <small>GitHub OAuth</small>
                </td>
                <td>
                  <code>{new URL(provider.baseUrl).host}</code>
                </td>
                <td>
                  {provider.provisioning === "approval"
                    ? "Owner approval"
                    : "Blocked"}
                </td>
                <td>
                  <span
                    className={`access-state ${provider.state === "ready" ? "active" : "disabled"}`}
                  >
                    {provider.state === "ready" ? "Enabled" : "Disabled"}
                  </span>
                </td>
                <td>
                  <div className="access-row-actions">
                    <button
                      aria-label={`Check ${provider.name}`}
                      title="Check GitHub host"
                      disabled={checking === provider.id}
                      onClick={() => void checkHost(provider)}
                    >
                      <ArrowClockwise size={16} />
                    </button>
                    <button
                      aria-label={`Edit ${provider.name}`}
                      title="Edit"
                      onClick={() => setEditor(provider)}
                    >
                      <PencilSimple size={16} />
                    </button>
                    <button
                      className="danger"
                      aria-label={`Remove ${provider.name}`}
                      title="Remove"
                      onClick={() => setRemoving(provider)}
                    >
                      <Trash size={16} />
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editor !== undefined && (
        <ProviderEditor
          provider={editor}
          onClose={() => setEditor(undefined)}
          onSaved={() => {
            setEditor(undefined);
            onChanged();
          }}
        />
      )}
      {removing && (
        <RemoveProvider
          provider={removing}
          onClose={() => setRemoving(null)}
          onRemoved={() => {
            setRemoving(null);
            onChanged();
          }}
        />
      )}
    </>
  );
}

function ProviderEditor({
  provider,
  onClose,
  onSaved,
}: {
  provider: AuthProvider | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const dialogRef = useDialogFocus(onClose);
  const [customHost, setCustomHost] = useState(
    provider ? new URL(provider.baseUrl).host !== "github.com" : false,
  );
  const [setupMethod, setSetupMethod] = useState<"manifest" | "manual">(
    provider ? "manual" : "manifest",
  );
  const [name, setName] = useState(provider?.name ?? "GitHub");
  const [baseUrl, setBaseUrl] = useState(
    provider?.baseUrl ?? "https://github.com",
  );
  const [clientId, setClientId] = useState(provider?.clientId ?? "");
  const [clientSecret, setClientSecret] = useState("");
  const [ownerType, setOwnerType] = useState<
    "" | "personal" | "organization"
  >("");
  const [owner, setOwner] = useState("");
  const [provisioning, setProvisioning] = useState<
    AuthProvider["provisioning"]
  >(provider?.provisioning ?? "existing");
  const [enabled, setEnabled] = useState(provider?.state !== "disabled");
  const [copied, setCopied] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const callback = `${window.location.origin}/api/v1/auth/callback`;
  const registrationURL = useMemo(
    () => `${baseUrl.replace(/\/$/, "")}/settings/applications/new`,
    [baseUrl],
  );
  const selectHost = (host: "github.com" | "custom") => {
    const isCustom = host === "custom";
    setCustomHost(isCustom);
    if (!isCustom) {
      setBaseUrl("https://github.com");
    } else if (!provider || new URL(provider.baseUrl).host === "github.com") {
      setBaseUrl("");
    }
  };
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const body = {
      name,
      type: "github",
      baseUrl,
      clientId,
      ...(clientSecret ? { clientSecret } : {}),
      provisioning,
      state: enabled ? "ready" : "disabled",
    };
    try {
      if (!provider && setupMethod === "manifest") {
        if (!ownerType) throw new Error("Choose who will own the GitHub App.");
        const flow = await api.startAuthProviderManifest({
          name,
          baseUrl,
          ownerType,
          owner: ownerType === "organization" ? owner : undefined,
          provisioning,
          state: enabled ? "ready" : "disabled",
        });
        const form = document.createElement("form");
        form.method = "post";
        form.action = flow.action;
        const manifest = document.createElement("input");
        manifest.type = "hidden";
        manifest.name = "manifest";
        manifest.value = JSON.stringify(flow.manifest);
        form.appendChild(manifest);
        document.body.appendChild(form);
        form.submit();
        return;
      }
      if (provider) await api.updateAuthProvider(provider.id, body);
      else await api.createAuthProvider(body);
      onSaved();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  }
  return (
    <div
      className="dialog-layer access-dialog-layer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="resource-dialog access-dialog signin-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="signin-editor-title"
      >
        <header>
          <div>
            <h2 id="signin-editor-title">
              {provider ? "Edit sign-in method" : "Add sign-in method"}
            </h2>
          </div>
          <button aria-label="Close dialog" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body">
          <form className="signin-form" onSubmit={submit}>
            {!provider && (
              <div
                className="connection-method signin-setup-method"
                role="group"
                aria-label="GitHub setup"
              >
                <button
                  type="button"
                  className={setupMethod === "manifest" ? "active" : ""}
                  onClick={() => setSetupMethod("manifest")}
                >
                  <GithubLogo size={19} weight="fill" />
                  <span>
                    <strong>Create in GitHub</strong>
                    <small>Dispatch configures the App.</small>
                  </span>
                </button>
                <button
                  type="button"
                  className={setupMethod === "manual" ? "active" : ""}
                  onClick={() => setSetupMethod("manual")}
                >
                  <Key size={19} />
                  <span>
                    <strong>Existing App</strong>
                    <small>Use a client ID and secret.</small>
                  </span>
                </button>
              </div>
            )}
            <div className="signin-fields">
              <label>
                <span>Name</span>
                <input
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  required
                  autoFocus
                />
              </label>
              <div className="signin-field">
                <span>GitHub host</span>
                <DropdownMenu.Root>
                  <DropdownMenu.Trigger asChild>
                    <button
                      className="signin-host-trigger"
                      type="button"
                      aria-label="GitHub host"
                    >
                      <span>
                        <GithubLogo
                          size={17}
                          weight={customHost ? "regular" : "fill"}
                        />
                        {customHost ? "Custom host" : "github.com"}
                      </span>
                      <CaretDown size={14} />
                    </button>
                  </DropdownMenu.Trigger>
                  <DropdownMenu.Portal>
                    <DropdownMenu.Content
                      className="action-menu-list signin-host-menu"
                      align="start"
                      sideOffset={5}
                      collisionPadding={12}
                    >
                      <DropdownMenu.Item
                        className="action-menu-item"
                        onSelect={() => selectHost("github.com")}
                      >
                        <GithubLogo size={16} weight="fill" />
                        <span>
                          <strong>github.com</strong>
                        </span>
                        {!customHost && (
                          <Check className="signin-host-check" size={15} />
                        )}
                      </DropdownMenu.Item>
                      <DropdownMenu.Item
                        className="action-menu-item"
                        onSelect={() => selectHost("custom")}
                      >
                        <GithubLogo size={16} />
                        <span>
                          <strong>Custom host</strong>
                        </span>
                        {customHost && (
                          <Check className="signin-host-check" size={15} />
                        )}
                      </DropdownMenu.Item>
                    </DropdownMenu.Content>
                  </DropdownMenu.Portal>
                </DropdownMenu.Root>
              </div>
              {customHost && (
                <label className="wide">
                  <span>Base URL</span>
                  <input
                    type="url"
                    value={baseUrl}
                    onChange={(event) => setBaseUrl(event.target.value)}
                    placeholder="https://github.example.com"
                    required
                  />
                </label>
              )}
              {!provider && setupMethod === "manifest" && (
                <label>
                  <span>Registration owner</span>
                  <select
                    value={ownerType}
                    onChange={(event) =>
                      setOwnerType(
                        event.target.value as
                          | ""
                          | "personal"
                          | "organization",
                      )
                    }
                    required
                  >
                    <option value="">Choose an account</option>
                    <option value="personal">Personal account</option>
                    <option value="organization">Organization</option>
                  </select>
                </label>
              )}
              {!provider &&
                setupMethod === "manifest" &&
                ownerType === "organization" && (
                  <label>
                    <span>Organization</span>
                    <input
                      value={owner}
                      onChange={(event) => setOwner(event.target.value)}
                      placeholder="platform-team"
                      required
                      spellCheck={false}
                    />
                  </label>
                )}
            </div>
            {(provider || setupMethod === "manual") && (
              <section className="signin-registration">
              <div>
                <strong>Register an OAuth app</strong>
                <span>Use this callback URL in GitHub.</span>
              </div>
              <a
                className="quiet-button"
                href={registrationURL}
                target="_blank"
                rel="noreferrer"
              >
                Open GitHub <ArrowSquareOut size={14} />
              </a>
              <div className="signin-callback">
                <code>{callback}</code>
                <button
                  type="button"
                  aria-label="Copy callback URL"
                  onClick={() => {
                    void navigator.clipboard.writeText(callback);
                    setCopied(true);
                    window.setTimeout(() => setCopied(false), 1400);
                  }}
                >
                  {copied ? <Check size={16} /> : <Copy size={16} />}
                </button>
              </div>
              </section>
            )}
            <div className="signin-fields">
              {(provider || setupMethod === "manual") && (
                <>
                  <label>
                    <span>Client ID</span>
                    <input
                      value={clientId}
                      onChange={(event) => setClientId(event.target.value)}
                      required
                      spellCheck={false}
                    />
                  </label>
                  <label>
                    <span>Client secret</span>
                    <input
                      type="password"
                      value={clientSecret}
                      onChange={(event) =>
                        setClientSecret(event.target.value)
                      }
                      placeholder={
                        provider?.clientSecretConfigured
                          ? "Leave blank to keep current secret"
                          : "Required"
                      }
                      required={!provider?.clientSecretConfigured}
                      autoComplete="new-password"
                    />
                  </label>
                </>
              )}
              <label className="wide">
                <span>New GitHub users</span>
                <select
                  value={provisioning}
                  onChange={(event) =>
                    setProvisioning(
                      event.target.value as AuthProvider["provisioning"],
                    )
                  }
                >
                  <option value="existing">Block unknown users</option>
                  <option value="approval">Send for owner approval</option>
                </select>
                <small>
                  {provisioning === "approval"
                    ? "Dispatch creates a pending account. An owner must approve it before sign-in."
                    : "Only GitHub accounts already linked to a Dispatch member can sign in."}
                </small>
              </label>
              <div className="signin-toggles">
                <label>
                  <input
                    type="checkbox"
                    checked={enabled}
                    onChange={(event) => setEnabled(event.target.checked)}
                  />
                  <span>Enabled</span>
                </label>
              </div>
            </div>
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
            <div className="dialog-actions">
              <button type="button" className="quiet-button" onClick={onClose}>
                Cancel
              </button>
              <button className="primary-button" disabled={busy}>
                {busy
                  ? setupMethod === "manifest" && !provider
                    ? "Opening GitHub..."
                    : "Saving..."
                  : setupMethod === "manifest" && !provider
                    ? "Continue to GitHub"
                    : "Save method"}
              </button>
            </div>
          </form>
        </div>
      </section>
    </div>
  );
}

function RemoveProvider({
  provider,
  onClose,
  onRemoved,
}: {
  provider: AuthProvider;
  onClose: () => void;
  onRemoved: () => void;
}) {
  const dialogRef = useDialogFocus(onClose);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function remove() {
    setBusy(true);
    setError("");
    try {
      await api.deleteAuthProvider(provider.id);
      onRemoved();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  }
  return (
    <div
      className="dialog-layer access-dialog-layer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="resource-dialog confirm-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="remove-provider-title"
      >
        <header>
          <h2 id="remove-provider-title">Remove sign-in method</h2>
          <button aria-label="Close dialog" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body">
          <p>
            <strong>{provider.name}</strong> will no longer appear on the
            sign-in screen. Linked users are not deleted.
          </p>
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <div className="dialog-actions confirm-actions">
            <button className="quiet-button" onClick={onClose}>
              Cancel
            </button>
            <button
              className="danger-button"
              disabled={busy}
              onClick={() => void remove()}
            >
              {busy ? "Removing..." : "Remove"}
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}
