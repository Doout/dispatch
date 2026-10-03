import { SecretUsageDialog, useSecretUsage, VariableUsageButton } from "../SecretUsage";
import { ChangeEvent, FormEvent, useRef, useState } from "react";
import {
  Check,
  Cloud,
  Copy,
  Key,
  LockSimple,
  PencilSimple,
  Trash,
  UploadSimple,
  X,
} from "@phosphor-icons/react";
import { api, Overview, Secret, SecretSource, SecretType } from "../api";
import { readSecretTextFile } from "../fileUploads";
import { View } from "../routes";
import { PageHeader } from "../PageHeader";
import { useDialogFocus } from "../useDialogFocus";
import { EmptyState } from "../components/PageStates";

const secretTypeOptions: Array<{
  value: SecretType;
  label: string;
  defaultName: string;
  environmentVariable: string;
  placeholder: string;
}> = [
  {
    value: "text",
    label: "Text",
    defaultName: "",
    environmentVariable: "SECRET_VALUE",
    placeholder: "Enter a secret value",
  },
  {
    value: "json",
    label: "JSON",
    defaultName: "",
    environmentVariable: "JSON_VALUE",
    placeholder: '{"APIKEY":"...","URL":"https://example.com"}',
  },
  {
    value: "api_token",
    label: "API token",
    defaultName: "API token",
    environmentVariable: "API_TOKEN",
    placeholder: "Paste an API token",
  },
  {
    value: "github_token",
    label: "GitHub token",
    defaultName: "GitHub token",
    environmentVariable: "GITHUB_TOKEN",
    placeholder: "Paste a GitHub personal access token",
  },
  {
    value: "ssh_private_key",
    label: "SSH private key",
    defaultName: "Global deploy key",
    environmentVariable: "SSH_PRIVATE_KEY",
    placeholder: "Paste an OpenSSH or PEM private key",
  },
  {
    value: "registry_password",
    label: "Registry password",
    defaultName: "Registry password",
    environmentVariable: "REGISTRY_PASSWORD",
    placeholder: "Enter the registry password",
  },
];

const secretTypeLabel = (type: SecretType) =>
  secretTypeOptions.find((option) => option.value === type)?.label ?? "Text";

export function SecretsPage({
  overview,
  onChanged,
  onDelete,
}: {
  overview: Overview;
  onChanged: () => Promise<void>;
  onDelete: (secret: Secret) => void;
}) {
  const [editing, setEditing] = useState<Secret | null>(null);
  const [usageSecret, setUsageSecret] = useState<Secret | null>(null);
  const usage = useSecretUsage(overview);
  const [creating, setCreating] = useState(false);
  const [valueKind, setValueKind] = useState<"secret" | "plain">(
    overview.secretStorageConfigured ? "secret" : "plain",
  );
  const [valueFilter, setValueFilter] = useState<"all" | "secret" | "plain">("all");
  const [secretType, setSecretType] = useState<SecretType>("text");
  const [plainFormat, setPlainFormat] = useState<"text" | "json">("text");
  const [secretSource, setSecretSource] = useState<SecretSource>("local");
  const [name, setName] = useState("");
  const [environmentVariable, setEnvironmentVariable] =
    useState("SECRET_VALUE");
  const [value, setValue] = useState("");
  const [externalStoreID, setExternalStoreID] = useState("");
  const [externalSecretID, setExternalSecretID] = useState("");
  const [externalField, setExternalField] = useState("");
  const [sshSource, setSSHSource] = useState<"generate" | "existing">(
    "generate",
  );
  const [fileName, setFileName] = useState("");
  const [copiedID, setCopiedID] = useState("");
  const [publicKeySecret, setPublicKeySecret] = useState<Secret | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fileInput = useRef<HTMLInputElement>(null);

  function open(secret?: Secret) {
    const plain = secret?.type === "environment_variable" || secret?.type === "environment_json";
    const type = plain ? "text" : secret?.type ?? "text";
    setEditing(secret ?? null);
    setCreating(true);
    setValueKind(secret ? (plain ? "plain" : "secret") : (overview.secretStorageConfigured ? "secret" : "plain"));
    setSecretType(type);
    setPlainFormat(secret?.type === "environment_json" ? "json" : "text");
    setSecretSource(secret?.source ?? "local");
    setName(secret?.name ?? "");
    setEnvironmentVariable(
      secret?.environmentVariable ??
        (plain || !overview.secretStorageConfigured ? "VARIABLE_VALUE" :
          secretTypeOptions.find((option) => option.value === type)?.environmentVariable ?? "SECRET_VALUE"),
    );
    setValue(plain ? secret?.publicValue ?? "" : "");
    setExternalStoreID(
      secret?.externalStoreId ?? overview.secretStores?.[0]?.id ?? "",
    );
    setExternalSecretID(secret?.externalSecretId ?? "");
    setExternalField(secret?.externalField ?? "");
    setSSHSource(secret ? "existing" : "generate");
    setFileName("");
    setError("");
  }

  function closeEditor() {
    setCreating(false);
    setEditing(null);
    setValue("");
    setExternalSecretID("");
    setExternalField("");
    setFileName("");
    setError("");
  }

  function changeType(next: SecretType) {
    const previous = secretTypeOptions.find(
      (option) => option.value === secretType,
    );
    const selected = secretTypeOptions.find((option) => option.value === next)!;
    if (!name.trim() || name === previous?.defaultName)
      setName(selected.defaultName);
    if (
      !environmentVariable.trim() ||
      environmentVariable === previous?.environmentVariable
    )
      setEnvironmentVariable(selected.environmentVariable);
    setSecretType(next);
    setSSHSource(
      next === "ssh_private_key" && !editing ? "generate" : "existing",
    );
    setValue("");
    setFileName("");
    setError("");
  }

  async function loadSecretFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    setError("");
    try {
      setValue(await readSecretTextFile(file));
      setFileName(file.name);
      setSSHSource("existing");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      event.target.value = "";
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const plain = valueKind === "plain";
    const type = plain ? (plainFormat === "json" ? "environment_json" : "environment_variable") : secretType;
    const source = plain ? "local" : secretSource;
    const generate =
      !plain &&
      secretSource === "local" &&
      secretType === "ssh_private_key" &&
      sshSource === "generate";
    const reference =
      source === "external"
        ? {
            externalStoreId: externalStoreID,
            externalSecretId: externalSecretID,
            ...(externalField.trim() ? { externalField } : {}),
          }
        : {};
    try {
      if ((type === "json" || type === "environment_json") && value) {
        let parsed: unknown;
        try {
          parsed = JSON.parse(value);
        } catch {
          throw new Error("Enter a valid JSON object.");
        }
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed) || !Object.keys(parsed).length)
          throw new Error("Enter a non-empty JSON object.");
      }
      const saved = editing
        ? await api.updateSecret(editing.id, {
            name,
            type,
            source,
            environmentVariable,
            ...reference,
            ...(generate
              ? { generate: true }
              : source === "local" && value
                ? { value }
                : {}),
          })
        : await api.createSecret({
            name,
            type,
            source,
            environmentVariable,
            ...reference,
            ...(generate
              ? { generate: true }
              : source === "local"
                ? { value }
                : {}),
          });
      setCreating(false);
      setEditing(null);
      setValue("");
      setFileName("");
      await onChanged();
      if (generate && saved.publicValue) setPublicKeySecret(saved);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function copyPublicKey(secret: Secret) {
    if (!secret.publicValue) return;
    try {
      await navigator.clipboard.writeText(secret.publicValue);
      setCopiedID(secret.id);
      window.setTimeout(
        () => setCopiedID((current) => (current === secret.id ? "" : current)),
        1800,
      );
    } catch {
      setError("Could not copy the public key. Select and copy it manually.");
    }
  }

  const selectedType = secretTypeOptions.find(
    (option) => option.value === secretType,
  )!;
  const usesGeneratedKey =
    valueKind === "secret" &&
    secretSource === "local" &&
    secretType === "ssh_private_key" &&
    sshSource === "generate";
  const accept =
    secretType === "ssh_private_key"
      ? ".key,.pem,text/plain,application/x-pem-file"
      : secretType === "json"
        ? ".json,application/json,text/plain"
      : ".txt,.env,.token,.key,.pem,text/plain";
  const isPlain = (item: Secret) => item.type === "environment_variable" || item.type === "environment_json";
  const plainCount = overview.secrets.filter(isPlain).length;
  const visibleValues = overview.secrets.filter((item) =>
    valueFilter === "all" || isPlain(item) === (valueFilter === "plain"),
  );

  async function copyPlainValue(secret: Secret) {
    try {
      await navigator.clipboard.writeText(secret.publicValue ?? "");
      setCopiedID(secret.id);
      window.setTimeout(() => setCopiedID((current) => current === secret.id ? "" : current), 1800);
    } catch {
      setError("Could not copy the value.");
    }
  }

  return (
    <div className="page-layout">
      <PageHeader
        view="secrets"
        action={
          creating
            ? {
                label: "Cancel",
                onClick: closeEditor,
                icon: <X size={16} weight="bold" />,
                tone: "quiet",
              }
            : {
                label: "Add value",
                onClick: () => open(),
              }
        }
      />
      {!overview.secretStorageConfigured && (
        <div className="error-banner variable-storage-notice" role="status">
          <strong>Secret storage is not configured</strong>
          <span>
            Set DISPATCH_MASTER_KEY_FILE and restart the controller to add secrets. Plain values remain available.
          </span>
        </div>
      )}
      {creating && (
        <section
          className="inline-create secret-editor"
          aria-labelledby="secret-editor-title"
        >
          <header>
            <h2 id="secret-editor-title">
              {editing ? "Edit value" : "New value"}
            </h2>
          </header>
          <div className="inline-create-body">
            <form className="resource-form secret-form" onSubmit={save}>
              <div className="secret-form-main">
                <fieldset className="secret-value-kind">
                  <legend>How should Dispatch store this value?</legend>
                  <div>
                    <label>
                      <input type="radio" name="value-kind" checked={valueKind === "secret"} disabled={!overview.secretStorageConfigured} onChange={() => { setValueKind("secret"); setValue(""); }} />
                      <span><LockSimple size={17} /><strong>Secret</strong><small>Encrypted and hidden after saving</small></span>
                    </label>
                    <label>
                      <input type="radio" name="value-kind" checked={valueKind === "plain"} onChange={() => { setValueKind("plain"); setSecretType("text"); setSecretSource("local"); setValue(""); if (environmentVariable === "SECRET_VALUE") setEnvironmentVariable("VARIABLE_VALUE"); }} />
                      <span><Key size={17} /><strong>Plain value</strong><small>Stored unencrypted; visible to owners</small></span>
                    </label>
                  </div>
                </fieldset>
                {valueKind === "secret" && <fieldset className="secret-storage-source">
                  <legend>Source</legend>
                  <div>
                    <label>
                      <input
                        type="radio"
                        name="secret-source"
                        checked={secretSource === "local"}
                        onChange={() => setSecretSource("local")}
                      />
                      <span>
                        <LockSimple size={16} />
                        <strong>Dispatch</strong>
                      </span>
                    </label>
                    <label>
                      <input
                        type="radio"
                        name="secret-source"
                        checked={secretSource === "external"}
                        onChange={() => {
                          setSecretSource("external");
                          setSSHSource("existing");
                          setValue("");
                          if (!externalStoreID)
                            setExternalStoreID(
                              overview.secretStores?.[0]?.id ?? "",
                            );
                        }}
                        disabled={!overview.secretStores?.length}
                      />
                      <span>
                        <Cloud size={16} />
                        <strong>External store</strong>
                      </span>
                    </label>
                  </div>
                </fieldset>}
                <div className="secret-identity-fields">
                  {valueKind === "plain" && <label className="secret-type-field">
                    <span>Type</span>
                    <select value={plainFormat} onChange={(event) => { setPlainFormat(event.target.value as "text" | "json"); setValue(""); }}>
                      <option value="text">Text</option>
                      <option value="json">JSON</option>
                    </select>
                  </label>}
                  {valueKind === "secret" && <label className="secret-type-field">
                    <span>Type</span>
                    <select
                      value={secretType}
                      onChange={(event) =>
                        changeType(event.target.value as SecretType)
                      }
                    >
                      {secretTypeOptions.map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </select>
                  </label>}
                  <label>
                    <span>Name</span>
                    <input
                      required
                      maxLength={80}
                      value={name}
                      onChange={(event) => setName(event.target.value)}
                      placeholder={
                        selectedType.defaultName || "Production secret"
                      }
                    />
                  </label>
                  <label className="secret-env-field">
                    <span>Environment variable</span>
                    <input
                      required
                      maxLength={128}
                      value={environmentVariable}
                      onChange={(event) =>
                        setEnvironmentVariable(event.target.value)
                      }
                      placeholder={selectedType.environmentVariable}
                      spellCheck={false}
                    />
                  </label>
                </div>
                {secretSource === "local" &&
                  valueKind === "secret" && secretType === "ssh_private_key" && (
                    <fieldset className="secret-key-source">
                      <legend>Key source</legend>
                      <div>
                        <label>
                          <input
                            type="radio"
                            name="ssh-source"
                            value="generate"
                            checked={sshSource === "generate"}
                            onChange={() => {
                              setSSHSource("generate");
                              setValue("");
                              setFileName("");
                            }}
                          />
                          <span>
                            <strong>Generate new key</strong>
                          </span>
                        </label>
                        <label>
                          <input
                            type="radio"
                            name="ssh-source"
                            value="existing"
                            checked={sshSource === "existing"}
                            onChange={() => setSSHSource("existing")}
                          />
                          <span>
                            <strong>Use existing key</strong>
                          </span>
                        </label>
                      </div>
                    </fieldset>
                  )}
                {valueKind === "secret" && secretSource === "external" ? (
                  <div className="external-secret-fields">
                    <label>
                      <span>Secret store</span>
                      <select
                        value={externalStoreID}
                        onChange={(event) =>
                          setExternalStoreID(event.target.value)
                        }
                        required
                      >
                        <option value="">Choose a store</option>
                        {(overview.secretStores ?? []).map((store) => (
                          <option key={store.id} value={store.id}>
                            {store.name}
                          </option>
                        ))}
                      </select>
                    </label>
                    <label>
                      <span>Secret ID</span>
                      <input
                        value={externalSecretID}
                        onChange={(event) =>
                          setExternalSecretID(event.target.value)
                        }
                        placeholder="Secret UUID"
                        required
                        spellCheck={false}
                      />
                    </label>
                    <label>
                      <span>Value field</span>
                      <input
                        value={externalField}
                        onChange={(event) =>
                          setExternalField(event.target.value)
                        }
                        placeholder="Optional, for example credentials.password"
                        spellCheck={false}
                      />
                    </label>
                  </div>
                ) : (
                  !usesGeneratedKey && (
                    <div className="secret-value-field">
                      <div className="secret-value-heading">
                        <div>
                          <label htmlFor="secret-value">
                            {valueKind === "plain" ? "Value" : editing ? "New value (optional)" : "Secret value"}
                          </label>
                          {editing && valueKind === "secret" && (
                            <small>
                              Leave blank to keep the current value.
                            </small>
                          )}
                        </div>
                        {valueKind === "secret" && <div className="secret-upload">
                          <input
                            ref={fileInput}
                            className="sr-only"
                            type="file"
                            accept={accept}
                            aria-label="Choose a secret file"
                            onChange={(event) => void loadSecretFile(event)}
                          />
                          <button
                            type="button"
                            className="quiet-button"
                            onClick={() => fileInput.current?.click()}
                          >
                            <UploadSimple size={15} />
                            Upload file
                          </button>
                          <span aria-live="polite">
                            {fileName || "64 KiB max"}
                          </span>
                        </div>}
                      </div>
                      {valueKind === "plain" && plainFormat === "text" ? <input
                        id="secret-value"
                        required
                        value={value}
                        onChange={(event) => setValue(event.target.value)}
                        placeholder="https://example.com/api"
                        spellCheck={false}
                      /> : <textarea
                        id="secret-value"
                        required={!editing || isPlain(editing)}
                        value={value}
                        onChange={(event) => {
                          setValue(event.target.value);
                          setFileName("");
                        }}
                        placeholder={
                          editing && !isPlain(editing)
                            ? "Leave blank to keep the value"
                            : (valueKind === "plain" ? '{"APIKEY":"...","URL":"https://example.com"}' : selectedType.placeholder)
                        }
                        spellCheck={false}
                      />}
                      {(secretType === "json" && valueKind === "secret" || plainFormat === "json" && valueKind === "plain") &&
                        <small>In workflow YAML, bind a key with <code>secretRef: {name.trim() || environmentVariable}</code> and <code>key: APIKEY</code>. {valueKind === "plain" && "Choose Secret if any key is a credential."}</small>}
                      {editing && isPlain(editing) !== (valueKind === "plain") &&
                        <small className="variable-conversion-note">{valueKind === "plain" ? "Enter a new plain value. Dispatch cannot show the current secret." : "Enter a new secret value before saving."}</small>}
                    </div>
                  )
                )}
                {error && (
                  <p className="form-error" role="alert">
                    {error}
                  </p>
                )}
                <div className="secret-form-actions">
                  {usesGeneratedKey && (
                    <p>
                      <LockSimple size={15} weight="bold" />
                      <span>
                        {editing
                          ? "This replaces the current key."
                          : "Only the public key remains visible."}
                      </span>
                    </p>
                  )}
                  <button
                    type="button"
                    className="quiet-button"
                    onClick={closeEditor}
                  >
                    Cancel
                  </button>
                  <button
                    className="primary-button"
                    disabled={
                      busy ||
                      !name.trim() ||
                      !environmentVariable.trim() ||
                      (valueKind === "secret" && !overview.secretStorageConfigured) ||
                      (valueKind === "secret" && secretSource === "external"
                        ? !externalStoreID || !externalSecretID.trim()
                        : (!editing || isPlain(editing) !== (valueKind === "plain") || (valueKind === "plain" && plainFormat === "json" && editing.type !== "environment_json") || (valueKind === "secret" && secretType === "json" && editing.type !== "json")) && !usesGeneratedKey && !value.trim())
                    }
                  >
                    {busy
                      ? "Saving..."
                      : usesGeneratedKey
                        ? editing
                          ? "Replace key"
                          : "Generate key"
                        : editing
                          ? "Save changes"
                          : valueKind === "plain" ? "Add variable" : "Add secret"}
                  </button>
                </div>
              </div>
            </form>
          </div>
        </section>
      )}
      {!creating && error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {!creating &&
        (overview.secrets.length ? (
          <section className="variables-inventory" aria-label="Saved values">
            <div className="variables-toolbar">
              <div className="variables-filters" role="group" aria-label="Filter values">
                <button type="button" aria-pressed={valueFilter === "all"} onClick={() => setValueFilter("all")}>All <span>{overview.secrets.length}</span></button>
                <button type="button" aria-pressed={valueFilter === "plain"} onClick={() => setValueFilter("plain")}>Plain values <span>{plainCount}</span></button>
                <button type="button" aria-pressed={valueFilter === "secret"} onClick={() => setValueFilter("secret")}>Secrets <span>{overview.secrets.length - plainCount}</span></button>
              </div>
            </div>
            {visibleValues.length ? <div className="variables-list">{visibleValues.map((secret) => {
              const plain = isPlain(secret);
              const store = overview.secretStores?.find((item) => item.id === secret.externalStoreId);
              return <article className="variable-row" key={secret.id}>
                <div className="variable-identity">
                  <div><strong>{secret.name}</strong><span className={plain ? "variable-kind plain" : "variable-kind secret"}>{plain ? "Plain value" : "Secret"}</span></div>
                  <code>{secret.environmentVariable}</code>
                </div>
                <div className="variable-preview">
                  <span>Value</span>
                  {plain ? <code>{secret.publicValue}</code> : <span className="variable-hidden"><LockSimple size={14} />Hidden after saving</span>}
                </div>
                <div className="variable-details">
                  <span>{plain ? "Dispatch" : secret.source === "external" ? (store?.name ?? "External store") : "Dispatch"}</span>
                  <span>{secret.type === "environment_json" ? "JSON" : plain ? "Environment variable" : secretTypeLabel(secret.type)}</span>
                  <VariableUsageButton secret={secret} usage={usage.items?.find(item => item.secretId === secret.id)} loading={usage.loading} error={usage.error} onClick={() => setUsageSecret(secret)} />
                </div>
                <div className="variable-actions">
                  {plain && <button type="button" onClick={() => void copyPlainValue(secret)} aria-label={`Copy ${secret.name}`}><Copy size={16} />{copiedID === secret.id ? "Copied" : "Copy"}</button>}
                  {secret.type === "ssh_private_key" && secret.publicValue && <button type="button" onClick={() => setPublicKeySecret(secret)} aria-label={`View public key for ${secret.name}`}><Key size={16} />Public key</button>}
                  <button type="button" onClick={() => open(secret)} aria-label={`Edit ${secret.name}`}><PencilSimple size={16} />Edit</button>
                  <button type="button" className="variable-delete" onClick={() => onDelete(secret)} aria-label={`Delete ${secret.name}`}><Trash size={16} />Delete</button>
                </div>
              </article>;
            })}</div> : <p className="variables-filter-empty">No {valueFilter === "plain" ? "plain values" : "secrets"} yet.</p>}
          </section>
        ) : (
          <div>
            <EmptyState
              title="No variables"
              action={{ label: "Add value", onClick: () => open() }}
            />
          </div>
        ))}
      {usageSecret && <SecretUsageDialog secret={usageSecret} usage={usage.items?.find(item => item.secretId === usageSecret.id)} loading={usage.loading} error={usage.error} onRetry={usage.refresh} onClose={() => setUsageSecret(null)} />}
      {publicKeySecret && (
        <PublicKeyDialog
          secret={publicKeySecret}
          copied={copiedID === publicKeySecret.id}
          onCopy={() => void copyPublicKey(publicKeySecret)}
          onClose={() => setPublicKeySecret(null)}
        />
      )}
    </div>
  );
}

function PublicKeyDialog({
  secret,
  copied,
  onCopy,
  onClose,
}: {
  secret: Secret;
  copied: boolean;
  onCopy: () => void;
  onClose: () => void;
}) {
  const dialogRef = useDialogFocus(onClose);
  return (
    <div className="dialog-layer drawer-layer">
      <section
        ref={dialogRef}
        className="resource-dialog resource-drawer public-key-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="public-key-title"
        aria-describedby="public-key-description"
      >
        <header>
          <div>
            <h2 id="public-key-title">Public key</h2>
            <p id="public-key-description">Add this to your Git host.</p>
          </div>
          <button aria-label="Close public key" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body">
          <div className="public-key-summary">
            <Key size={20} />
            <div>
              <strong>{secret.name}</strong>
              <span>Ed25519 deploy key</span>
            </div>
          </div>
          <label className="public-key-value">
            <span>Public key</span>
            <textarea
              readOnly
              value={secret.publicValue ?? ""}
              aria-label={`Public key for ${secret.name}`}
            />
          </label>
          <p className="key-privacy-note">
            <LockSimple size={16} />
            Private key unavailable.
          </p>
          <div className="dialog-actions">
            <button className="primary-button" onClick={onCopy}>
              {copied ? <Check size={15} weight="bold" /> : <Copy size={15} />}
              {copied ? "Copied" : "Copy public key"}
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}
