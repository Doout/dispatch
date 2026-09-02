import { ChangeEvent, FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { Check, Copy, Cube, Key, LockKey, TerminalWindow } from "@phosphor-icons/react";
import { api, GitHubRepository, HelmChartInspection, HelmValue, KubernetesServerInput, Overview, Project, Secret, Server } from "./api";
import { readKubernetesTextFile } from "./fileUploads";
import { HelmValuesEditor, helmValueOverrides, mergeHelmValues } from "./HelmValuesEditor";

type Changed = () => Promise<void>;
const isKubernetesRuntime = (runtime: Server["runtime"]) => runtime === "kubernetes" || runtime === "openshift";

function newRelayToken() {
  const bytes = new Uint8Array(24);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
}

function shellQuote(value: string) {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

export function ServerForm({ onChanged, onCancel, server, repairing = false, secrets = [], initialRuntime = "docker" }: { onChanged: Changed; onCancel?: () => void; server?: Server; repairing?: boolean; secrets?: Secret[]; initialRuntime?: Server["runtime"] }) {
  const editing = Boolean(server);
  const savedSSHKeys = secrets.filter((secret) => secret.type === "ssh_private_key");
  const [name, setName] = useState(server?.name ?? "");
  const [runtime, setRuntime] = useState<"docker" | "kubernetes" | "openshift" | "relay">(server?.runtime ?? initialRuntime);
  const [remoteAddress, setRemoteAddress] = useState(server?.runtime === "docker" ? server.address : "");
  const [relayAddress, setRelayAddress] = useState(server?.runtime === "relay" ? server.address : "");
  const [relayAccessToken, setRelayAccessToken] = useState(() => server ? "" : newRelayToken());
  const [relayInstallMethod, setRelayInstallMethod] = useState<"manual" | "ssh">("manual");
  const [relayUseDocker, setRelayUseDocker] = useState(false);
  const [relayImage, setRelayImage] = useState("");
  const [sshHost, setSSHHost] = useState("");
  const [sshPort, setSSHPort] = useState("22");
  const [sshUser, setSSHUser] = useState("root");
  const [sshCredentialSource, setSSHCredentialSource] = useState<"saved" | "private_key" | "password">(savedSSHKeys.length ? "saved" : "private_key");
  const [sshSecretID, setSSHSecretID] = useState(savedSSHKeys[0]?.id ?? "");
  const [sshPassword, setSSHPassword] = useState("");
  const [sshPrivateKey, setSSHPrivateKey] = useState("");
  const [sshPrivateKeyPassword, setSSHPrivateKeyPassword] = useState("");
  const [sshSudoPassword, setSSHSudoPassword] = useState("");
  const [sshFingerprint, setSSHFingerprint] = useState("");
  const [scanningSSH, setScanningSSH] = useState(false);
  const [copiedCommand, setCopiedCommand] = useState(false);
  const [kubeconfigSource, setKubeconfigSource] = useState<"stored" | "path">(server?.kubernetes?.kubeconfigStored ? "stored" : server ? "path" : "stored");
  const [kubeconfigPath, setKubeconfigPath] = useState(server?.kubernetes?.kubeconfigPath ?? "");
  const [kubeconfig, setKubeconfig] = useState("");
  const [certificateAuthority, setCertificateAuthority] = useState("");
  const [removeCertificateAuthority, setRemoveCertificateAuthority] = useState(false);
  const [kubeconfigFileName, setKubeconfigFileName] = useState("");
  const [certificateFileName, setCertificateFileName] = useState("");
  const [fileError, setFileError] = useState("");
  const [loginCommand, setLoginCommand] = useState("");
  const [kubeContext, setKubeContext] = useState(server?.kubernetes?.context ?? "");
  const [namespace, setNamespace] = useState(server?.kubernetes?.namespace ?? "default");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const kubeconfigFile = useRef<HTMLInputElement>(null);
  const certificateFile = useRef<HTMLInputElement>(null);
  const relayCommand = `curl -fsSL ${shellQuote(`${window.location.origin}/relay/install.sh`)} | sudo env DISPATCH_RELAY_PUBLIC_URL=${shellQuote(relayAddress.trim())} DISPATCH_RELAY_TOKEN=${shellQuote(relayAccessToken)} DISPATCH_RELAY_DOWNLOAD_BASE=${shellQuote(window.location.origin)}${relayUseDocker ? ` DISPATCH_RELAY_INSTALL_MODE='docker'${relayImage.trim() ? ` DISPATCH_RELAY_IMAGE=${shellQuote(relayImage.trim())}` : ""}` : ""} sh`;

  async function scanRelayHost() {
    setScanningSSH(true);
    setError("");
    setSSHFingerprint("");
    try {
      const result = await api.scanRelaySSHHost(sshHost, Number(sshPort));
      setSSHFingerprint(result.fingerprint);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setScanningSSH(false);
    }
  }

  async function copyRelayCommand() {
    await navigator.clipboard.writeText(relayCommand);
    setCopiedCommand(true);
    window.setTimeout(() => setCopiedCommand(false), 1800);
  }

  async function loadCredentialFile(kind: "kubeconfig" | "certificate", event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;

    setFileError("");
    try {
      const content = await readKubernetesTextFile(file);
      if (kind === "kubeconfig") {
        setKubeconfig(content);
        setKubeconfigFileName(file.name);
      } else {
        setCertificateAuthority(content);
        setCertificateFileName(file.name);
        setRemoveCertificateAuthority(false);
      }
      event.target.value = "";
    } catch (cause) {
      setFileError((cause as Error).message);
      event.target.value = "";
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (repairing && server) {
        await api.repairServer(server.id, loginCommand);
        await onChanged();
        return;
      }
      let kubernetes: KubernetesServerInput | undefined;
      if (runtime === "kubernetes") {
        kubernetes = { source: kubeconfigSource, context: kubeContext, namespace };
        if (kubeconfigSource === "path") {
          kubernetes.kubeconfigPath = kubeconfigPath;
        } else if (kubeconfig.trim()) {
          kubernetes.kubeconfig = kubeconfig;
          kubernetes.certificateAuthority = certificateAuthority;
        } else if (certificateAuthority.trim() || removeCertificateAuthority) {
          kubernetes.certificateAuthority = removeCertificateAuthority ? "" : certificateAuthority;
        }
      } else if (runtime === "openshift") {
        kubernetes = editing
          ? { source: "stored", context: server?.kubernetes?.context, namespace }
          : { source: "openshift", loginCommand, namespace };
      }
      if (runtime === "relay" && !editing && relayInstallMethod === "ssh") {
        await api.installRelayOverSSH({
          host: sshHost, port: Number(sshPort), user: sshUser, authType: sshCredentialSource === "password" ? "password" : "private_key",
          password: sshCredentialSource === "password" ? sshPassword : undefined,
          privateKey: sshCredentialSource === "private_key" ? sshPrivateKey : undefined,
          secretId: sshCredentialSource === "saved" ? sshSecretID : undefined,
          privateKeyPassword: sshCredentialSource !== "password" ? sshPrivateKeyPassword : undefined,
          sudoPassword: sshUser === "root" ? undefined : sshSudoPassword,
          hostKeyFingerprint: sshFingerprint, relayUrl: relayAddress, relayToken: relayAccessToken,
          installMode: relayUseDocker ? "docker" : "systemd",
          relayImage: relayUseDocker && relayImage.trim() ? relayImage.trim() : undefined,
        });
      }
      const address = runtime === "relay" ? relayAddress : remoteAddress;
      const relay = runtime === "relay" && relayAccessToken.trim() ? { accessToken: relayAccessToken } : undefined;
      if (server) await api.updateServer(server.id, { name, address, kubernetes, relay });
      else await api.createServer({ name, address, runtime, kubernetes, relay });
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (repairing && server) {
    return <form className="resource-form server-form" onSubmit={submit} aria-busy={busy}>
      <div className="openshift-warning wide"><strong>Fresh administrator login required</strong><p>Dispatch replaces the cluster connection and discards this login.</p></div>
      <label className="wide openshift-login-field"><span>oc login command</span><textarea placeholder="oc login --token=… --server=https://api.cluster.example:6443" value={loginCommand} onChange={(event) => setLoginCommand(event.target.value)} required spellCheck={false} /></label>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="dialog-actions">{onCancel && <button type="button" className="quiet-button" onClick={onCancel}>Cancel</button>}<button className="primary-button" disabled={busy || !loginCommand.trim()}>{busy ? "Repairing connection..." : "Repair connection"}</button></div>
    </form>;
  }

  return <form className={`resource-form server-form server-form-${runtime}`} onSubmit={submit} aria-busy={busy}>
    {runtime !== "relay" && <div className="form-section-heading wide"><div><strong>Target</strong></div></div>}
    <label><span>Server name</span><input placeholder={runtime === "relay" ? "event-relay" : runtime === "openshift" ? "openshift-cluster" : runtime === "kubernetes" ? "preview-cluster" : "build-01"} value={name} onChange={(event) => setName(event.target.value)} required /></label>
    <label><span>Server type</span><select value={runtime} disabled={editing} onChange={(event) => setRuntime(event.target.value as "docker" | "kubernetes" | "openshift" | "relay")}><option value="docker">Docker target</option><option value="kubernetes">Kubernetes target</option><option value="openshift">OpenShift target</option><option value="relay">Webhook relay</option></select>{editing && <small>Server type cannot be changed.</small>}</label>
    {runtime !== "relay" && <div className="form-section-heading wide"><div><strong>Connection</strong></div></div>}
    {runtime === "relay" ? <>
      <label className="wide"><span>Relay URL</span><input type="url" placeholder="https://relay.example.com" value={relayAddress} onChange={(event) => setRelayAddress(event.target.value)} required spellCheck={false} /></label>
      <label className="wide relay-token-field"><span>Access token</span><div><input type="password" pattern="[A-Za-z0-9_-]{24,256}" placeholder={server?.relay?.accessTokenConfigured ? "Leave blank to keep the token" : "Relay access token"} value={relayAccessToken} onChange={(event) => setRelayAccessToken(event.target.value)} required={!server?.relay?.accessTokenConfigured} autoComplete="new-password" />{!editing && <button type="button" className="quiet-button" onClick={() => setRelayAccessToken(newRelayToken())}>Regenerate</button>}</div>{server?.relay?.accessTokenConfigured && <small>Leave blank to keep the current token.</small>}</label>
      {!editing && <fieldset className="relay-install wide"><legend><strong>Install relay</strong></legend>
        <div className="relay-install-options">
          <label className={relayInstallMethod === "manual" ? "selected" : ""}><input type="radio" name="relay-install" checked={relayInstallMethod === "manual"} onChange={() => setRelayInstallMethod("manual")} /><TerminalWindow size={17} /><span><strong>Run a command</strong></span></label>
          <label className={relayInstallMethod === "ssh" ? "selected" : ""}><input type="radio" name="relay-install" checked={relayInstallMethod === "ssh"} onChange={() => setRelayInstallMethod("ssh")} /><Key size={17} /><span><strong>Install over SSH</strong></span></label>
        </div>
        <div className={`relay-runtime-choice ${relayUseDocker ? "with-image" : ""}`}>
          <label><input type="checkbox" checked={relayUseDocker} onChange={(event) => setRelayUseDocker(event.target.checked)} /><Cube size={17} /><span><strong>Run with Docker Compose</strong><small>Leave off to install a systemd service.</small></span></label>
          {relayUseDocker && <label className="relay-runtime-image"><span>Container image</span><input placeholder="Build from controller" value={relayImage} onChange={(event) => setRelayImage(event.target.value)} spellCheck={false} /></label>}
        </div>
        {relayInstallMethod === "manual" ? <div className="relay-command-panel">
          <header><div><strong>Run on the relay node</strong><small>{relayUseDocker ? "Requires Docker Compose v2 and open ports 80 and 443." : "Requires Linux with systemd, curl, and sudo."}</small></div></header>
          <pre><code>{relayCommand}</code></pre>
          <footer>{relayUseDocker && <span>Data stays in <code>/var/lib/dispatch-relay</code>.</span>}<button type="button" className="quiet-button" disabled={!relayAddress.trim()} onClick={() => void copyRelayCommand()}>{copiedCommand ? <Check size={14} weight="bold" /> : <Copy size={14} />}{copiedCommand ? "Copied" : "Copy command"}</button></footer>
        </div> : <div className="relay-ssh-panel">
          <div className="relay-ssh-grid">
            <label className="relay-ssh-host"><span>Host</span><input placeholder="relay.example.com" value={sshHost} onChange={(event) => { setSSHHost(event.target.value); setSSHFingerprint(""); }} required /></label>
            <label><span>User</span><input placeholder="root" value={sshUser} onChange={(event) => setSSHUser(event.target.value)} required /></label>
            <label><span>Port</span><input type="number" min="1" max="65535" value={sshPort} onChange={(event) => { setSSHPort(event.target.value); setSSHFingerprint(""); }} required /></label>
          </div>
          <fieldset className="relay-authentication"><legend>Authentication</legend>
            <div className="relay-auth-options">
              <label className={sshCredentialSource === "saved" ? "selected" : ""}><input type="radio" name="ssh-credential-source" value="saved" checked={sshCredentialSource === "saved"} disabled={!savedSSHKeys.length} onChange={() => setSSHCredentialSource("saved")} /><span>Saved key</span></label>
              <label className={sshCredentialSource === "private_key" ? "selected" : ""}><input type="radio" name="ssh-credential-source" value="private_key" checked={sshCredentialSource === "private_key"} onChange={() => setSSHCredentialSource("private_key")} /><span>Paste key</span></label>
              <label className={sshCredentialSource === "password" ? "selected" : ""}><input type="radio" name="ssh-credential-source" value="password" checked={sshCredentialSource === "password"} onChange={() => setSSHCredentialSource("password")} /><span>Password</span></label>
            </div>
            {sshCredentialSource === "saved" ? <div className="relay-auth-fields"><label><span>SSH key</span><select value={sshSecretID} onChange={(event) => setSSHSecretID(event.target.value)} required><option value="">Choose a saved key</option>{savedSSHKeys.map((secret) => <option key={secret.id} value={secret.id}>{secret.name}</option>)}</select></label><label><span>Passphrase</span><input type="password" placeholder="Optional" value={sshPrivateKeyPassword} onChange={(event) => setSSHPrivateKeyPassword(event.target.value)} autoComplete="new-password" /></label></div> : sshCredentialSource === "private_key" ? <div className="relay-auth-fields relay-auth-fields-key"><label><span>Private key</span><textarea className="relay-ssh-key" placeholder="Paste an OpenSSH or PEM key" value={sshPrivateKey} onChange={(event) => setSSHPrivateKey(event.target.value)} required spellCheck={false} /></label><label><span>Passphrase</span><input type="password" placeholder="Optional" value={sshPrivateKeyPassword} onChange={(event) => setSSHPrivateKeyPassword(event.target.value)} autoComplete="new-password" /></label></div> : <div className="relay-auth-fields"><label><span>SSH password</span><input type="password" value={sshPassword} onChange={(event) => setSSHPassword(event.target.value)} required autoComplete="new-password" /></label></div>}
          </fieldset>
          {sshUser !== "root" && <label className="relay-sudo-password"><span>Sudo password</span><input type="password" placeholder="Optional" value={sshSudoPassword} onChange={(event) => setSSHSudoPassword(event.target.value)} autoComplete="new-password" /></label>}
          <div className={`relay-host-key ${sshFingerprint ? "verified" : ""}`}><div><strong>Host key</strong><code>{sshFingerprint || "Not verified"}</code></div><button type="button" className="quiet-button" disabled={scanningSSH || !sshHost.trim()} onClick={() => void scanRelayHost()}>{scanningSSH ? "Checking..." : sshFingerprint ? "Check again" : "Verify host"}</button></div>
          <small className="relay-ssh-note">Verify the host key before installing. Credentials are not copied to the relay.</small>
        </div>}
      </fieldset>}
      <div className="relay-durability-note wide"><LockKey size={16} /><p>Events stay queued until acknowledged.</p></div>
    </> : runtime === "docker" ? <>
      <label><span>Connection</span><select value="remote" disabled><option value="remote">Remote server</option></select></label>
      <label><span>Address</span><input placeholder="docker-host.example.com" value={remoteAddress} onChange={(event) => setRemoteAddress(event.target.value)} required spellCheck={false} /></label>
    </> : runtime === "openshift" ? <>
      {editing ? <div className="openshift-managed wide"><strong>Managed OpenShift connection</strong><dl><div><dt>Service account</dt><dd>{server?.kubernetes?.openShift?.serviceAccount ?? "dispatch-controller"}</dd></div><div><dt>Namespace</dt><dd>{server?.kubernetes?.openShift?.serviceAccountNamespace ?? "dispatch-system"}</dd></div><div><dt>API server</dt><dd>{server?.address}</dd></div></dl><small>Use Repair from the server list when the token, API address, or CA changes.</small></div> : <>
        <div className="openshift-warning wide"><strong>Creates a cluster administrator</strong><p>The temporary login must be allowed to create a service account and cluster-admin binding. Dispatch stores the service-account kubeconfig, not this login command.</p></div>
        <label className="wide openshift-login-field"><span>oc login command</span><textarea placeholder="oc login --token=… --server=https://api.cluster.example:6443" value={loginCommand} onChange={(event) => setLoginCommand(event.target.value)} required spellCheck={false} /><small>Paste the non-interactive login command supplied by OpenShift.</small></label>
      </>}
      <label><span>Deployment namespace</span><input placeholder="default" value={namespace} onChange={(event) => setNamespace(event.target.value)} spellCheck={false} /></label>
    </> : <>
      <label><span>Connection</span><select value={kubeconfigSource} onChange={(event) => setKubeconfigSource(event.target.value as "stored" | "path")}><option value="stored">Paste kubeconfig</option><option value="path">Mounted file</option></select></label>
      {kubeconfigSource === "stored" ? <>
        <div className="credential-upload wide">
          <input ref={kubeconfigFile} className="sr-only" type="file" accept=".yaml,.yml,.json,.conf,application/yaml,application/json,text/yaml,text/plain" aria-label="Choose a kubeconfig file" onChange={(event) => void loadCredentialFile("kubeconfig", event)} />
          <button type="button" className="quiet-button" onClick={() => kubeconfigFile.current?.click()}>Upload kubeconfig</button>
          <small aria-live="polite">{kubeconfigFileName ? `${kubeconfigFileName} loaded into the editor.` : "YAML or JSON, up to 4 MB."}</small>
        </div>
        <label className="wide kubeconfig-field"><span>Kubeconfig</span><textarea placeholder={server?.kubernetes?.kubeconfigStored ? "Paste to replace saved kubeconfig" : "Paste kubeconfig YAML"} value={kubeconfig} onChange={(event) => setKubeconfig(event.target.value)} required={!server?.kubernetes?.kubeconfigStored} spellCheck={false} />{server?.kubernetes?.kubeconfigStored && <small>Leave blank to keep saved kubeconfig.</small>}</label>
        <div className="credential-upload wide">
          <input ref={certificateFile} className="sr-only" type="file" accept=".pem,.crt,.cer,application/x-pem-file,application/pkix-cert,text/plain" aria-label="Choose a CA certificate file" onChange={(event) => void loadCredentialFile("certificate", event)} />
          <button type="button" className="quiet-button" disabled={removeCertificateAuthority} onClick={() => certificateFile.current?.click()}>Upload CA certificate</button>
          <small aria-live="polite">{certificateFileName ? `${certificateFileName} loaded into the editor.` : "Optional PEM, CRT, or CER file, up to 4 MB."}</small>
        </div>
        <label className="wide kube-ca-field"><span>CA certificate</span><textarea placeholder={server?.kubernetes?.certificateAuthorityStored ? "CA certificate saved. Paste a new PEM bundle to replace it." : "Optional PEM certificate bundle"} value={certificateAuthority} onChange={(event) => { setCertificateAuthority(event.target.value); setRemoveCertificateAuthority(false); }} disabled={removeCertificateAuthority} spellCheck={false} /><small>Add this only if the kubeconfig does not include certificate-authority-data.</small></label>
        {server?.kubernetes?.certificateAuthorityStored && <label className="clear-secret wide"><input type="checkbox" checked={removeCertificateAuthority} onChange={(event) => { setRemoveCertificateAuthority(event.target.checked); if (event.target.checked) { setCertificateAuthority(""); setCertificateFileName(""); } }} /><span>Remove the saved CA certificate</span></label>}
      </> : <label className="wide"><span>Kubeconfig path</span><input placeholder="/kubeconfigs/preview.yaml" value={kubeconfigPath} onChange={(event) => setKubeconfigPath(event.target.value)} required spellCheck={false} /></label>}
      <label><span>Context</span><input placeholder="Current context" value={kubeContext} onChange={(event) => setKubeContext(event.target.value)} spellCheck={false} /></label>
      <label><span>Namespace</span><input placeholder="default" value={namespace} onChange={(event) => setNamespace(event.target.value)} spellCheck={false} /></label>
    </>}
    {fileError && <p className="form-error" role="alert">{fileError}</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="dialog-actions">{onCancel && <button type="button" className="quiet-button" onClick={onCancel}>Cancel</button>}<button className="primary-button" disabled={busy || !name.trim() || (runtime === "relay" ? !relayAddress.trim() || (!editing && (!relayAccessToken.trim() || (relayInstallMethod === "ssh" && (!sshHost.trim() || !sshUser.trim() || !sshFingerprint || (sshCredentialSource === "password" ? !sshPassword : sshCredentialSource === "saved" ? !sshSecretID : !sshPrivateKey.trim()))))) : runtime === "docker" ? !remoteAddress.trim() : runtime === "openshift" ? !editing && !loginCommand.trim() : kubeconfigSource === "path" ? !kubeconfigPath.trim() : !kubeconfig.trim() && !server?.kubernetes?.kubeconfigStored)}>{busy ? runtime === "openshift" && !editing ? "Connecting..." : runtime === "relay" && relayInstallMethod === "ssh" && !editing ? "Installing..." : runtime === "relay" ? "Connecting..." : "Saving..." : editing ? "Save changes" : runtime === "openshift" ? "Connect OpenShift" : runtime === "relay" ? relayInstallMethod === "ssh" ? "Install and connect" : "Connect relay" : "Add server"}</button></div>
  </form>;
}

export function ProjectForm({ onChanged, onCancel, project }: { onChanged: Changed; onCancel?: () => void; project?: Project }) {
  const [name, setName] = useState(project?.name ?? "");
  const [description, setDescription] = useState(project?.description ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (project) await api.updateProject(project.id, { name, description });
      else await api.createProject({ name, description });
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <form className="resource-form" onSubmit={submit} aria-busy={busy}>
    <label><span>Project name</span><input placeholder="Platform apps" value={name} onChange={(event) => setName(event.target.value)} required /></label>
    <label><span>Description</span><input placeholder="Optional" value={description} onChange={(event) => setDescription(event.target.value)} /></label>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="dialog-actions">{onCancel && <button type="button" className="quiet-button" onClick={onCancel}>Cancel</button>}<button className="primary-button" disabled={busy || !name.trim()}>{busy ? "Saving..." : project ? "Save changes" : "Add project"}</button></div>
  </form>;
}

type ApplicationSourceType = "compose" | "repository" | "helm";

function repositoryIdentifier(value: string) {
  const trimmed = value.trim().replace(/\.git$/, "");
  try {
    const parsed = new URL(trimmed);
    return parsed.pathname.split("/").filter(Boolean).slice(0, 2).join("/").toLowerCase();
  } catch {
    const sshPath = trimmed.includes(":") ? trimmed.split(":").at(-1) ?? "" : trimmed;
    const parts = sshPath.split("/").filter(Boolean);
    return parts.length === 2 ? parts.join("/").toLowerCase() : "";
  }
}

export function AppForm({ data, onChanged, focusName = false, initialSourceType = "compose", sourceTypeLocked = false, template = false }: { data: Overview; onChanged: Changed; focusName?: boolean; initialSourceType?: ApplicationSourceType; sourceTypeLocked?: boolean; template?: boolean }) {
  const readyServers = useMemo(() => data.servers.filter((server) => server.state === "ready"), [data.servers]);
  const initialServers = readyServers.filter((server) => initialSourceType === "helm" ? isKubernetesRuntime(server.runtime) : server.runtime === "docker");
  const [projectID, setProjectID] = useState(data.projects[0]?.id ?? "");
  const [serverID, setServerID] = useState(initialServers[0]?.id ?? "");
  const [name, setName] = useState("");
  const [sourceType, setSourceType] = useState<ApplicationSourceType>(initialSourceType);
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("main");
  const [sourceAuthType, setSourceAuthType] = useState<"" | "github_app" | "github_token" | "ssh_key">("");
  const [sourceCredentialID, setSourceCredentialID] = useState("");
  const [composeContent, setComposeContent] = useState("");
  const [buildType, setBuildType] = useState("dockerfile");
  const [helmOrigin, setHelmOrigin] = useState<"direct" | "repository" | "git">("direct");
  const [helmChart, setHelmChart] = useState("");
  const [helmVersion, setHelmVersion] = useState("");
  const [helmRepository, setHelmRepository] = useState("");
  const [helmValues, setHelmValues] = useState("");
  const [helmInspection, setHelmInspection] = useState<HelmChartInspection | null>(null);
  const [helmEffectiveValues, setHelmEffectiveValues] = useState<Record<string, HelmValue>>({});
  const [helmValueBaseline, setHelmValueBaseline] = useState<Record<string, HelmValue>>({});
  const [helmValuesMode, setHelmValuesMode] = useState<"structured" | "raw">("structured");
  const [helmProfile, setHelmProfile] = useState("");
  const [inspectingHelm, setInspectingHelm] = useState(false);
  const [helmInspectionError, setHelmInspectionError] = useState("");
  const [helmNamespace, setHelmNamespace] = useState("");
  const [helmRelease, setHelmRelease] = useState("");
  const [domain, setDomain] = useState("");
  const [eventEnabled, setEventEnabled] = useState(false);
  const [eventGitHubAppID, setEventGitHubAppID] = useState("");
  const [eventRepository, setEventRepository] = useState("");
  const [eventCommand, setEventCommand] = useState("/preview");
  const [eventRepositories, setEventRepositories] = useState<GitHubRepository[]>([]);
  const [eventRepositoriesLoading, setEventRepositoriesLoading] = useState(false);
  const [eventRepositoryError, setEventRepositoryError] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const directCompose = sourceType === "compose";
      const helm = sourceType === "helm";
      const structuredHelmValues = helm && helmOrigin === "git" && helmInspection && helmValuesMode === "structured"
        ? helmValueOverrides(helmInspection.defaults, helmEffectiveValues)
        : undefined;
      const created = await api.createApp({
        projectId: projectID, serverId: serverID, name, template,
        sourceRepo: directCompose ? "" : helm ? helmOrigin === "git" ? repo : "" : repo, composeContent: directCompose ? composeContent : "",
        branch: directCompose ? "" : branch, buildType: directCompose ? "compose" : helm ? "helm" : buildType,
        sourceAuthType: !directCompose && repo.trim() ? sourceAuthType : "", sourceCredentialId: !directCompose && repo.trim() ? sourceCredentialID : "",
        contextPath: ".", dockerfilePath: "Dockerfile", composePath: "compose.yml", containerPort: 8080, domain,
        helmChart: helm ? helmChart : "", helmVersion: helm ? helmVersion : "", helmRepository: helm && helmOrigin === "repository" ? helmRepository : "",
        helmValues: helm && structuredHelmValues === undefined ? helmValues : "", helmValueOverrides: structuredHelmValues,
        helmNamespace: helm ? helmNamespace : "", helmRelease: helm ? helmRelease : "",
      });
      if (eventEnabled && !helm) {
        await api.createEventTrigger(created.id, { githubAppId: eventGitHubAppID, provider: "github", repository: eventRepository, command: eventCommand, enabled: true });
      }
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function changeSource(next: ApplicationSourceType) {
    setSourceType(next);
    const eligible = readyServers.filter((server) => next === "helm" ? isKubernetesRuntime(server.runtime) : server.runtime === "docker");
    if (!eligible.some((server) => server.id === serverID)) setServerID(eligible[0]?.id ?? "");
  }

  function invalidateHelmInspection() {
    setHelmInspection(null);
    setHelmInspectionError("");
    setHelmProfile("");
    setHelmEffectiveValues({});
    setHelmValueBaseline({});
  }

  useEffect(() => {
    if (sourceAuthType === "github_app" && sourceCredentialID && !eventGitHubAppID) setEventGitHubAppID(sourceCredentialID);
  }, [eventGitHubAppID, sourceAuthType, sourceCredentialID]);

  useEffect(() => {
    let active = true;
    setEventRepositories([]);
    setEventRepositoryError("");
    if (!eventGitHubAppID) return () => { active = false; };
    setEventRepositoriesLoading(true);
    void api.githubAppRepositories(eventGitHubAppID).then((items) => {
      if (!active) return;
      setEventRepositories(items);
    }).catch((cause) => { if (active) setEventRepositoryError((cause as Error).message); }).finally(() => { if (active) setEventRepositoriesLoading(false); });
    return () => { active = false; };
  }, [eventGitHubAppID]);

  useEffect(() => {
    const inferred = repositoryIdentifier(repo);
    const match = eventRepositories.find((repository) => repository.fullName.toLowerCase() === inferred);
    if (match) setEventRepository((current) => current || match.fullName);
  }, [eventRepositories, repo]);

  async function inspectHelmChart() {
    setInspectingHelm(true);
    setHelmInspectionError("");
    try {
      const inspection = await api.inspectHelmSource({ projectId: projectID, sourceRepo: repo, branch, chartPath: helmChart, sourceAuthType, sourceCredentialId: sourceCredentialID });
      const defaults = structuredClone(inspection.defaults);
      setHelmInspection(inspection);
      setRepo(inspection.repository);
      setBranch(inspection.branch);
      setHelmChart(inspection.chartPath);
      setHelmEffectiveValues(defaults);
      setHelmValueBaseline(structuredClone(defaults));
      setHelmValues("");
      setHelmProfile("");
      setHelmValuesMode("structured");
      if (!name.trim()) setName(inspection.chart.name);
      if (!helmRelease.trim()) setHelmRelease(inspection.chart.name);
    } catch (cause) {
      setHelmInspectionError((cause as Error).message);
    } finally {
      setInspectingHelm(false);
    }
  }

  function selectHelmProfile(path: string) {
    const profile = helmInspection?.profiles.find((item) => item.path === path);
    const baseline = helmInspection ? mergeHelmValues(helmInspection.defaults, profile?.values ?? {}) : {};
    setHelmProfile(path);
    setHelmValueBaseline(structuredClone(baseline));
    setHelmEffectiveValues(structuredClone(baseline));
    setHelmValues(profile?.valuesYaml ?? "");
  }

  const eligibleServers = readyServers.filter((server) => sourceType === "helm" ? isKubernetesRuntime(server.runtime) : server.runtime === "docker");
  const repositorySource = sourceType === "repository" || (sourceType === "helm" && helmOrigin === "git");
  const repositoryAuthentication = repositorySource && repo.trim() !== "";
  const compatibleSourceSecrets = data.secrets.filter((secret) => sourceAuthType === "ssh_key"
    ? secret.type === "ssh_private_key" || secret.type === "text"
    : sourceAuthType === "github_token"
      ? secret.type === "github_token" || secret.type === "api_token" || secret.type === "text"
      : false);
  const repositoryHost = (() => { try { return new URL(repo).host.toLowerCase(); } catch { return ""; } })();
  const compatibleGitHubApps = data.githubApps.filter((connection) => connection.state === "ready" && (() => {
    if (!repositoryHost) return true;
    try { return new URL(connection.webUrl).host.toLowerCase() === repositoryHost; } catch { return false; }
  })());
  const selectedProject = data.projects.find((project) => project.id === projectID);
  const selectedServer = eligibleServers.find((server) => server.id === serverID);

  return <form className={`resource-form application-form application-form-${sourceType}`} onSubmit={submit} aria-busy={busy}>
    <label className={sourceTypeLocked && data.projects.length <= 1 && eligibleServers.length <= 1 ? "wide" : undefined}><span>{template ? "Template name" : "Application name"}</span><input placeholder="checkout-api" value={name} onChange={(event) => setName(event.target.value)} autoFocus={focusName} required /></label>
    {!sourceTypeLocked && <label><span>Deploy from</span><select value={sourceType} onChange={(event) => changeSource(event.target.value as ApplicationSourceType)}><option value="compose">Compose file</option><option value="repository">Git repository</option>{template && <option value="helm">Helm chart</option>}</select></label>}
    {data.projects.length > 1 && <label><span>Project</span><select value={projectID} onChange={(event) => setProjectID(event.target.value)}>{data.projects.map((project) => <option value={project.id} key={project.id}>{project.name}</option>)}</select></label>}
    {eligibleServers.length > 1 && <label><span>Target server</span><select value={serverID} onChange={(event) => setServerID(event.target.value)}>{eligibleServers.map((server) => <option value={server.id} key={server.id}>{server.name}</option>)}</select></label>}
    {sourceType === "helm" && <div className="form-section-heading wide"><div><strong>Chart</strong></div></div>}
    {sourceType === "repository" && <><label className="wide"><span>Repository URL</span><input placeholder={sourceAuthType === "ssh_key" ? "git@github.com:owner/app.git" : "https://github.com/owner/app.git"} value={repo} onChange={(event) => setRepo(event.target.value)} required spellCheck={false} /></label><label><span>Branch</span><input placeholder="main" value={branch} onChange={(event) => setBranch(event.target.value)} required spellCheck={false} /></label><label><span>Build method</span><select value={buildType} onChange={(event) => setBuildType(event.target.value)}><option value="dockerfile">Dockerfile</option><option value="compose">Compose</option></select></label></>}
    {sourceType === "compose" && <label className="wide compose-field"><span>Compose definition</span><textarea rows={9} placeholder={'services:\n  app:\n    image: ghcr.io/owner/app:latest'} value={composeContent} onChange={(event) => setComposeContent(event.target.value)} required spellCheck={false} /></label>}
    {sourceType === "helm" && <>
      <fieldset className="source-selector wide"><legend>Chart origin</legend><div>
        <label><input type="radio" name="helm-origin" value="direct" checked={helmOrigin === "direct"} onChange={() => { setHelmOrigin("direct"); invalidateHelmInspection(); }} /><span><strong>Direct reference</strong></span></label>
        <label><input type="radio" name="helm-origin" value="repository" checked={helmOrigin === "repository"} onChange={() => { setHelmOrigin("repository"); invalidateHelmInspection(); }} /><span><strong>Helm repository</strong></span></label>
        <label><input type="radio" name="helm-origin" value="git" checked={helmOrigin === "git"} onChange={() => { setHelmOrigin("git"); invalidateHelmInspection(); }} /><span><strong>Git repository</strong></span></label>
      </div></fieldset>
      {helmOrigin === "repository" && <label className="wide"><span>Helm repository URL</span><input placeholder="https://charts.example.com" value={helmRepository} onChange={(event) => setHelmRepository(event.target.value)} required spellCheck={false} /></label>}
      {helmOrigin === "git" && <><label className="wide"><span>Repository or chart folder URL</span><input placeholder="https://github.example.com/owner/repo/tree/main/helm/chart" value={repo} onChange={(event) => { setRepo(event.target.value); invalidateHelmInspection(); }} required spellCheck={false} /></label><label><span>Branch</span><input placeholder="main" value={branch} onChange={(event) => { setBranch(event.target.value); invalidateHelmInspection(); }} required spellCheck={false} /></label></>}
      <label className="wide"><span>{helmOrigin === "git" ? "Chart directory" : "Chart"}</span><input placeholder={helmOrigin === "git" ? "charts/storefront" : helmOrigin === "repository" ? "service" : "oci://registry.example.com/charts/service"} value={helmChart} onChange={(event) => { setHelmChart(event.target.value); if (helmOrigin === "git") invalidateHelmInspection(); }} required={helmOrigin !== "git" || !repo.includes("/tree/")} spellCheck={false} /></label>
      {helmOrigin !== "git" && <label><span>Version</span><input placeholder="Chart default" value={helmVersion} onChange={(event) => setHelmVersion(event.target.value)} spellCheck={false} /></label>}
      <label><span>Namespace</span><input placeholder="Server default" value={helmNamespace} onChange={(event) => setHelmNamespace(event.target.value)} spellCheck={false} /></label>
      <label><span>Release name</span><input placeholder="Application name + ID" value={helmRelease} onChange={(event) => setHelmRelease(event.target.value)} spellCheck={false} /></label>
      <details className="optional-settings preview-settings wide"><summary>Advanced settings <span>Optional</span></summary><div><label><span>Preview URL</span><input placeholder="https://preview.example.com" value={domain} onChange={(event) => setDomain(event.target.value)} spellCheck={false} /></label></div></details>
    </>}
    {repositorySource && <details className="optional-settings repository-access wide"><summary>Repository access <span>Optional</span></summary><div>
      <label><span>Authentication</span><select value={sourceAuthType} onChange={(event) => { setSourceAuthType(event.target.value as "" | "github_app" | "github_token" | "ssh_key"); setSourceCredentialID(""); invalidateHelmInspection(); }}><option value="">Public repository</option><option value="github_app">GitHub App</option><option value="github_token">GitHub token</option><option value="ssh_key">SSH private key</option></select></label>
      {sourceAuthType === "github_app" ? <label><span>GitHub App</span><select value={sourceCredentialID} onChange={(event) => { setSourceCredentialID(event.target.value); invalidateHelmInspection(); }} required><option value="">Select a connection</option>{compatibleGitHubApps.map((connection) => <option value={connection.id} key={connection.id}>{connection.name} ({connection.installationAccount || new URL(connection.webUrl).host})</option>)}</select>{!compatibleGitHubApps.length && <small>Add and verify a GitHub App connection first.</small>}</label> : sourceAuthType && <label><span>Credential</span><select value={sourceCredentialID} onChange={(event) => { setSourceCredentialID(event.target.value); invalidateHelmInspection(); }} required><option value="">Select a secret</option>{compatibleSourceSecrets.map((secret) => <option value={secret.id} key={secret.id}>{secret.name}</option>)}</select>{!compatibleSourceSecrets.length && <small>Add a compatible secret first.</small>}</label>}
    </div></details>}
    {sourceType === "helm" && helmOrigin === "git" && !helmInspection && helmValuesMode !== "raw" && <section className="helm-inspection-prompt wide">
      <div><strong>Chart values</strong>{helmInspectionError && <small role="alert">{helmInspectionError}</small>}</div>
      <div><button type="button" className="quiet-button" onClick={() => setHelmValuesMode("raw")}>Raw YAML</button><button type="button" className="primary-button" disabled={inspectingHelm || !repo.trim() || (!helmChart.trim() && !repo.includes("/tree/")) || (!!sourceAuthType && !sourceCredentialID)} onClick={() => void inspectHelmChart()}>{inspectingHelm ? "Loading..." : "Load values"}</button></div>
    </section>}
    {sourceType === "helm" && helmOrigin === "git" && helmInspection && helmValuesMode === "structured" && <HelmValuesEditor inspection={helmInspection} values={helmEffectiveValues} baseline={helmValueBaseline} selectedProfile={helmProfile} onProfileChange={selectHelmProfile} onChange={setHelmEffectiveValues} onRawMode={() => setHelmValuesMode("raw")} />}
    {sourceType === "helm" && (helmOrigin !== "git" || helmValuesMode === "raw") && <section className="helm-raw-values wide">
      <header><div><strong>Helm values</strong></div>{helmOrigin === "git" && <button type="button" className="quiet-button" onClick={() => setHelmValuesMode("structured")}>{helmInspection ? "Form editor" : "Load values"}</button>}</header>
      <textarea placeholder={'image:\n  repository: registry.example.com/team/service\n  tag: latest'} value={helmValues} onChange={(event) => setHelmValues(event.target.value)} spellCheck={false} />
    </section>}
    {sourceType !== "helm" && <details className="event-config wide"><summary>Pull request previews <span>Optional</span></summary><div><label className="event-toggle"><input type="checkbox" checked={eventEnabled} onChange={(event) => setEventEnabled(event.target.checked)} /><span>Enable comment commands</span></label>{eventEnabled && <><label><span>GitHub connection</span><select value={eventGitHubAppID} onChange={(event) => { setEventGitHubAppID(event.target.value); setEventRepository(""); }} required><option value="">Choose a connection</option>{data.githubApps.filter((connection) => connection.state === "ready").map((connection) => <option key={connection.id} value={connection.id}>{connection.name} ({connection.installationAccount || "installed"})</option>)}</select></label><label><span>Repository</span><select value={eventRepository} onChange={(event) => setEventRepository(event.target.value)} disabled={!eventGitHubAppID || eventRepositoriesLoading} required><option value="">{eventRepositoriesLoading ? "Loading repositories..." : "Choose a repository"}</option>{eventRepositories.map((repository) => <option key={repository.id} value={repository.fullName}>{repository.fullName}</option>)}</select>{eventRepositoryError && <small className="field-error">{eventRepositoryError}</small>}</label><label><span>Command</span><input placeholder="/preview" value={eventCommand} onChange={(event) => setEventCommand(event.target.value)} required spellCheck={false} /></label></>}</div></details>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="dialog-actions application-actions"><span className="form-review">{selectedProject && selectedServer ? `${selectedProject.name} / ${selectedServer.name}` : "Select a target"}</span><button className="primary-button" disabled={busy || !projectID || !serverID || !name.trim() || (sourceType === "compose" ? !composeContent.trim() : sourceType === "repository" ? !repo.trim() : !helmChart.trim() || (helmOrigin === "git" && !repo.trim()) || (helmOrigin === "repository" && !helmRepository.trim())) || (repositoryAuthentication && !!sourceAuthType && !sourceCredentialID) || (sourceType !== "helm" && eventEnabled && (!eventGitHubAppID || eventRepositoriesLoading || !eventRepository.trim() || !eventCommand.trim()))}>{busy ? template ? "Saving..." : sourceType === "helm" ? "Saving..." : "Saving..." : template ? "Create template" : sourceType === "helm" ? "Save Helm source" : "Save application"}</button></div>
  </form>;
}
