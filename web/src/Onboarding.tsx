import { ChangeEvent, FormEvent, useMemo, useRef, useState } from "react";
import { api, KubernetesServerInput, Overview, Project, Server } from "./api";
import { readKubernetesTextFile } from "./fileUploads";

type Changed = () => Promise<void>;
const isKubernetesRuntime = (runtime: Server["runtime"]) => runtime === "kubernetes" || runtime === "openshift";

export function ServerForm({ onChanged, server, repairing = false }: { onChanged: Changed; server?: Server; repairing?: boolean }) {
  const editing = Boolean(server);
  const [name, setName] = useState(server?.name ?? "");
  const [runtime, setRuntime] = useState<"docker" | "kubernetes" | "openshift">(server?.runtime ?? "docker");
  const [remoteAddress, setRemoteAddress] = useState(server?.runtime === "docker" ? server.address : "");
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
      if (server) await api.updateServer(server.id, { name, address: remoteAddress, kubernetes });
      else await api.createServer({ name, address: remoteAddress, runtime, kubernetes });
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (repairing && server) {
    return <form className="resource-form server-form" onSubmit={submit}>
      <div className="openshift-warning wide"><strong>Fresh administrator login required</strong><p>Dispatch will rerun the managed service-account setup and replace its stored cluster connection. The login command is used once and is never saved.</p></div>
      <label className="wide openshift-login-field"><span>oc login command</span><textarea placeholder="oc login --token=… --server=https://api.cluster.example:6443" value={loginCommand} onChange={(event) => setLoginCommand(event.target.value)} required spellCheck={false} /><small>Paste a non-interactive command containing a temporary token, or a username and password.</small></label>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="dialog-actions"><button className="primary-button" disabled={busy || !loginCommand.trim()}>{busy ? "Repairing connection..." : "Repair connection"}</button></div>
    </form>;
  }

  return <form className="resource-form server-form" onSubmit={submit}>
    <label><span>Server name</span><input placeholder={runtime === "openshift" ? "openshift-cluster" : runtime === "kubernetes" ? "preview-cluster" : "build-01"} value={name} onChange={(event) => setName(event.target.value)} required /><small>Shown in Dispatch.</small></label>
    <label><span>Runtime</span><select value={runtime} disabled={editing} onChange={(event) => setRuntime(event.target.value as "docker" | "kubernetes" | "openshift")}><option value="docker">Docker</option><option value="kubernetes">Kubernetes</option><option value="openshift">OpenShift</option></select><small>{editing ? "Runtime cannot be changed." : "Choose where applications will run."}</small></label>
    {runtime === "docker" ? <>
      <label><span>Connection</span><select value="remote" disabled><option value="remote">Remote server</option></select><small>{editing ? "Connection type cannot be changed." : "Waits for enrollment."}</small></label>
      <label><span>Address</span><input placeholder="docker-host.example.com" value={remoteAddress} onChange={(event) => setRemoteAddress(event.target.value)} required spellCheck={false} /><small>Hostname or IP address.</small></label>
    </> : runtime === "openshift" ? <>
      {editing ? <div className="openshift-managed wide"><strong>Managed OpenShift connection</strong><dl><div><dt>Service account</dt><dd>{server?.kubernetes?.openShift?.serviceAccount ?? "dispatch-controller"}</dd></div><div><dt>Namespace</dt><dd>{server?.kubernetes?.openShift?.serviceAccountNamespace ?? "dispatch-system"}</dd></div><div><dt>API server</dt><dd>{server?.address}</dd></div></dl><small>Use Repair from the server list when the token, API address, or CA changes.</small></div> : <>
        <div className="openshift-warning wide"><strong>Creates a cluster administrator</strong><p>The temporary login must be allowed to create a service account and cluster-admin binding. Dispatch stores the service-account kubeconfig, not this login command.</p></div>
        <label className="wide openshift-login-field"><span>oc login command</span><textarea placeholder="oc login --token=… --server=https://api.cluster.example:6443" value={loginCommand} onChange={(event) => setLoginCommand(event.target.value)} required spellCheck={false} /><small>Paste the non-interactive login command supplied by OpenShift.</small></label>
      </>}
      <label><span>Deployment namespace</span><input placeholder="default" value={namespace} onChange={(event) => setNamespace(event.target.value)} spellCheck={false} /><small>Default namespace for Helm applications.</small></label>
    </> : <>
      <label><span>Connection</span><select value={kubeconfigSource} onChange={(event) => setKubeconfigSource(event.target.value as "stored" | "path")}><option value="stored">Paste kubeconfig</option><option value="path">Mounted file</option></select><small>{kubeconfigSource === "stored" ? "Saved in Dispatch storage." : "Read from the controller filesystem."}</small></label>
      {kubeconfigSource === "stored" ? <>
        <div className="credential-upload wide">
          <input ref={kubeconfigFile} className="sr-only" type="file" accept=".yaml,.yml,.json,.conf,application/yaml,application/json,text/yaml,text/plain" aria-label="Choose a kubeconfig file" onChange={(event) => void loadCredentialFile("kubeconfig", event)} />
          <button type="button" className="quiet-button" onClick={() => kubeconfigFile.current?.click()}>Upload kubeconfig</button>
          <small aria-live="polite">{kubeconfigFileName ? `${kubeconfigFileName} loaded into the editor.` : "YAML or JSON, up to 4 MB."}</small>
        </div>
        <label className="wide kubeconfig-field"><span>Kubeconfig</span><textarea placeholder={server?.kubernetes?.kubeconfigStored ? "A kubeconfig is saved. Paste a new one to replace it." : "Paste kubeconfig YAML"} value={kubeconfig} onChange={(event) => setKubeconfig(event.target.value)} required={!server?.kubernetes?.kubeconfigStored} spellCheck={false} /><small>{server?.kubernetes?.kubeconfigStored ? "The saved credential is not displayed. Leave this blank to keep it." : "Saved in Dispatch storage and materialized only while a Kubernetes command runs."}</small></label>
        <div className="credential-upload wide">
          <input ref={certificateFile} className="sr-only" type="file" accept=".pem,.crt,.cer,application/x-pem-file,application/pkix-cert,text/plain" aria-label="Choose a CA certificate file" onChange={(event) => void loadCredentialFile("certificate", event)} />
          <button type="button" className="quiet-button" disabled={removeCertificateAuthority} onClick={() => certificateFile.current?.click()}>Upload CA certificate</button>
          <small aria-live="polite">{certificateFileName ? `${certificateFileName} loaded into the editor.` : "Optional PEM, CRT, or CER file, up to 4 MB."}</small>
        </div>
        <label className="wide kube-ca-field"><span>CA certificate</span><textarea placeholder={server?.kubernetes?.certificateAuthorityStored ? "A CA certificate is saved. Paste a new PEM bundle to replace it." : "Optional PEM certificate bundle"} value={certificateAuthority} onChange={(event) => { setCertificateAuthority(event.target.value); setRemoveCertificateAuthority(false); }} disabled={removeCertificateAuthority} spellCheck={false} /><small>Only needed when certificate-authority-data is not embedded in the kubeconfig.</small></label>
        {server?.kubernetes?.certificateAuthorityStored && <label className="clear-secret wide"><input type="checkbox" checked={removeCertificateAuthority} onChange={(event) => { setRemoveCertificateAuthority(event.target.checked); if (event.target.checked) { setCertificateAuthority(""); setCertificateFileName(""); } }} /><span>Remove the saved CA certificate</span></label>}
      </> : <label className="wide"><span>Kubeconfig path</span><input placeholder="/kubeconfigs/preview.yaml" value={kubeconfigPath} onChange={(event) => setKubeconfigPath(event.target.value)} required spellCheck={false} /><small>Absolute path mounted into the Dispatch container.</small></label>}
      <label><span>Context</span><input placeholder="Current context" value={kubeContext} onChange={(event) => setKubeContext(event.target.value)} spellCheck={false} /><small>Optional kubeconfig context.</small></label>
      <label><span>Namespace</span><input placeholder="default" value={namespace} onChange={(event) => setNamespace(event.target.value)} spellCheck={false} /><small>Default deployment namespace.</small></label>
    </>}
    {fileError && <p className="form-error" role="alert">{fileError}</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="dialog-actions"><button className="primary-button" disabled={busy || !name.trim() || (runtime === "docker" ? !remoteAddress.trim() : runtime === "openshift" ? !editing && !loginCommand.trim() : kubeconfigSource === "path" ? !kubeconfigPath.trim() : !kubeconfig.trim() && !server?.kubernetes?.kubeconfigStored)}>{busy ? runtime === "openshift" && !editing ? "Connecting..." : "Saving..." : editing ? "Save changes" : runtime === "openshift" ? "Connect OpenShift" : "Add server"}</button></div>
  </form>;
}

export function ProjectForm({ onChanged, project }: { onChanged: Changed; project?: Project }) {
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

  return <form className="resource-form" onSubmit={submit}>
    <label><span>Project name</span><input placeholder="Platform apps" value={name} onChange={(event) => setName(event.target.value)} required /><small>Used to group related applications.</small></label>
    <label><span>Description</span><input placeholder="Optional" value={description} onChange={(event) => setDescription(event.target.value)} /><small>Short internal context.</small></label>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="dialog-actions"><button className="primary-button" disabled={busy || !name.trim()}>{busy ? "Saving..." : project ? "Save changes" : "Add project"}</button></div>
  </form>;
}

type ApplicationSourceType = "compose" | "repository" | "helm";

export function AppForm({ data, onChanged, onDeployed, focusName = false, initialSourceType = "compose", sourceTypeLocked = false, template = false }: { data: Overview; onChanged: Changed; onDeployed: (id: string) => Promise<void>; focusName?: boolean; initialSourceType?: ApplicationSourceType; sourceTypeLocked?: boolean; template?: boolean }) {
  const readyServers = useMemo(() => data.servers.filter((server) => server.state === "ready"), [data.servers]);
  const initialServers = readyServers.filter((server) => initialSourceType === "helm" ? isKubernetesRuntime(server.runtime) : server.runtime === "docker");
  const [projectID, setProjectID] = useState(data.projects[0]?.id ?? "");
  const [serverID, setServerID] = useState(initialServers[0]?.id ?? "");
  const [name, setName] = useState("");
  const [sourceType, setSourceType] = useState<ApplicationSourceType>(initialSourceType);
  const [repo, setRepo] = useState("");
  const [composeContent, setComposeContent] = useState("");
  const [buildType, setBuildType] = useState("dockerfile");
  const [helmChart, setHelmChart] = useState("");
  const [helmVersion, setHelmVersion] = useState("");
  const [helmRepository, setHelmRepository] = useState("");
  const [helmValues, setHelmValues] = useState("");
  const [helmNamespace, setHelmNamespace] = useState("");
  const [helmRelease, setHelmRelease] = useState("");
  const [domain, setDomain] = useState("");
  const [preDeployHook, setPreDeployHook] = useState("");
  const [postDeployHook, setPostDeployHook] = useState("");
  const [eventEnabled, setEventEnabled] = useState(false);
  const [eventRepository, setEventRepository] = useState("");
  const [eventCommand, setEventCommand] = useState("/preview");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const directCompose = sourceType === "compose";
      const helm = sourceType === "helm";
      const created = await api.createApp({
        projectId: projectID, serverId: serverID, name, template,
        sourceRepo: directCompose ? "" : repo, composeContent: directCompose ? composeContent : "",
        branch: directCompose ? "" : "main", buildType: directCompose ? "compose" : helm ? "helm" : buildType,
        contextPath: ".", dockerfilePath: "Dockerfile", composePath: "compose.yml", containerPort: 8080, domain,
        helmChart: helm ? helmChart : "", helmVersion: helm ? helmVersion : "", helmRepository: helm ? helmRepository : "",
        helmValues: helm ? helmValues : "", helmNamespace: helm ? helmNamespace : "", helmRelease: helm ? helmRelease : "",
      });
      if (eventEnabled) {
        await api.createEventTrigger(created.id, { provider: "github", repository: eventRepository, command: eventCommand, enabled: true, preDeployHook, postDeployHook });
      }
      if (directCompose && !template) {
        const deployment = await api.deploy(created.id, "inline");
        await onDeployed(deployment.id);
      } else {
        await onChanged();
      }
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

  const eligibleServers = readyServers.filter((server) => sourceType === "helm" ? isKubernetesRuntime(server.runtime) : server.runtime === "docker");

  return <form className="resource-form application-form" onSubmit={submit}>
    <label><span>{template ? "Template name" : "Application name"}</span><input placeholder="checkout-api" value={name} onChange={(event) => setName(event.target.value)} autoFocus={focusName} required /><small>{template ? "Reusable name shown in Dispatch." : "Shown in Dispatch."}</small></label>
    <label><span>Project</span><select value={projectID} onChange={(event) => setProjectID(event.target.value)}>{data.projects.map((project) => <option value={project.id} key={project.id}>{project.name}</option>)}</select><small>Groups this application.</small></label>
    <label><span>Server</span><select value={serverID} onChange={(event) => setServerID(event.target.value)}>{eligibleServers.map((server) => <option value={server.id} key={server.id}>{server.name}</option>)}</select><small>{sourceType === "helm" ? "Kubernetes servers only." : "Receives deployments."}</small></label>
    {!sourceTypeLocked && <label><span>Source</span><select value={sourceType} onChange={(event) => changeSource(event.target.value as ApplicationSourceType)}><option value="compose">Paste Compose file</option><option value="repository">Git repository</option>{template && <option value="helm">Helm chart</option>}</select><small>Choose the application definition.</small></label>}
    {sourceType === "repository" && <><label><span>Build method</span><select value={buildType} onChange={(event) => setBuildType(event.target.value)}><option value="dockerfile">Dockerfile</option><option value="compose">Compose</option></select><small>Defines the repository build contract.</small></label><label className="wide"><span>Repository URL</span><input placeholder="https://github.com/owner/app.git" value={repo} onChange={(event) => setRepo(event.target.value)} required spellCheck={false} /><small>HTTPS source repository.</small></label></>}
    {sourceType === "compose" && <label className="wide compose-field"><span>Docker Compose</span><textarea placeholder={'services:\n  app:\n    image: ghcr.io/owner/app:latest\n    ports:\n      - "8080:8080"'} value={composeContent} onChange={(event) => setComposeContent(event.target.value)} required spellCheck={false} /><small>Image-based services deploy as pasted. Relative build contexts need a repository.</small></label>}
    {sourceType === "helm" && <>
      <label className="wide"><span>Chart</span><input placeholder="oci://registry.example.com/charts/service" value={helmChart} onChange={(event) => setHelmChart(event.target.value)} required spellCheck={false} /><small>OCI or HTTPS reference. Use a chart name when a repository is set.</small></label>
      <label className="wide"><span>Helm repository</span><input placeholder="https://charts.example.com" value={helmRepository} onChange={(event) => setHelmRepository(event.target.value)} spellCheck={false} /><small>Optional HTTPS repository for a relative chart name.</small></label>
      <label><span>Version</span><input placeholder="Latest" value={helmVersion} onChange={(event) => setHelmVersion(event.target.value)} spellCheck={false} /><small>Optional chart version.</small></label>
      <label><span>Namespace</span><input placeholder="Server default" value={helmNamespace} onChange={(event) => setHelmNamespace(event.target.value)} spellCheck={false} /><small>Overrides the server namespace.</small></label>
      <label><span>Release</span><input placeholder="Generated automatically" value={helmRelease} onChange={(event) => setHelmRelease(event.target.value)} spellCheck={false} /><small>Stable name used for upgrades and cleanup.</small></label>
      <label className="wide compose-field"><span>Values</span><textarea placeholder={'image:\n  repository: registry.example.com/team/service\n  tag: latest'} value={helmValues} onChange={(event) => setHelmValues(event.target.value)} spellCheck={false} /><small>Optional values override applied during install and upgrade.</small></label>
      <label className="wide"><span>Source repository</span><input placeholder="https://github.com/owner/app.git" value={repo} onChange={(event) => setRepo(event.target.value)} spellCheck={false} /><small>Optional source checkout for build hooks and preview branches.</small></label>
      <label className="wide"><span>Preview URL</span><input placeholder="https://preview.example.com" value={domain} onChange={(event) => setDomain(event.target.value)} spellCheck={false} /><small>Published in event status updates after deployment.</small></label>
    </>}
    <details className="event-config wide"><summary>Pull request events</summary><div><label className="event-toggle"><input type="checkbox" checked={eventEnabled} onChange={(event) => setEventEnabled(event.target.checked)} /><span>Create previews from pull request comments</span></label>{eventEnabled && <><label><span>Repository</span><input placeholder="owner/repository" value={eventRepository} onChange={(event) => setEventRepository(event.target.value)} required spellCheck={false} /><small>Repository identifier used by webhook events.</small></label><label><span>Comment command</span><input placeholder="/preview" value={eventCommand} onChange={(event) => setEventCommand(event.target.value)} required spellCheck={false} /><small>The first line of a pull request comment.</small></label><label className="event-hook-field"><span>Pre-deploy hook</span><textarea placeholder={'docker build -t registry.example.com/team/app:$DISPATCH_REVISION .\ndocker push registry.example.com/team/app:$DISPATCH_REVISION'} value={preDeployHook} onChange={(event) => setPreDeployHook(event.target.value)} spellCheck={false} /><small>Runs for previews started by this event.</small></label><label className="event-hook-field"><span>Post-deploy hook</span><textarea placeholder={'echo "Ready at $DISPATCH_DEPLOYMENT_URL"'} value={postDeployHook} onChange={(event) => setPostDeployHook(event.target.value)} spellCheck={false} /><small>Runs after this event's deployment is ready.</small></label></>}</div></details>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="dialog-actions"><button className="primary-button" disabled={busy || !projectID || !serverID || !name.trim() || (sourceType === "compose" ? !composeContent.trim() : sourceType === "repository" ? !repo.trim() : !helmChart.trim()) || (eventEnabled && (!eventRepository.trim() || !eventCommand.trim()))}>{busy ? template ? "Saving template..." : sourceType === "compose" ? "Starting deployment..." : sourceType === "helm" ? "Adding Helm source..." : "Adding application..." : template ? "Create template" : sourceType === "compose" ? "Deploy Compose" : sourceType === "helm" ? "Add Helm source" : "Add application"}</button></div>
  </form>;
}
