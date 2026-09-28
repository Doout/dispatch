import { Overview } from "./api";

export type DockerProvision = { serverRef: string; image?: string; network?: string; storageMountPath?: string; [key: string]: unknown };
export type HelmProvision = { serverRef: string; namespace?: string; image?: string; storage?: string; storageClass?: string; chart?: string; [key: string]: unknown };

export function ServiceProvisionSettings({ docker, helm, overview, serviceType, onDocker, onHelm }: { docker?: DockerProvision; helm?: HelmProvision; overview: Overview; serviceType: "postgresql" | "generic"; onDocker: (value: DockerProvision) => void; onHelm: (value: HelmProvision) => void }) {
 const servers = overview.servers.filter(server => docker ? server.runtime === "docker" && ["local", "localhost", "127.0.0.1"].includes(server.address) : ["kubernetes", "openshift"].includes(server.runtime));
 const current = docker ?? helm!;
 return <section className="service-template-section">
  <label>Target server<select required value={current.serverRef} onChange={e => docker ? onDocker({ ...docker, serverRef: e.target.value }) : onHelm({ ...helm!, serverRef: e.target.value })}>
   <option value="">Choose a {docker ? "Docker" : "Kubernetes or OpenShift"} server</option>
   {current.serverRef && !servers.some(server => server.id === current.serverRef) && <option value={current.serverRef}>Unavailable server · {current.serverRef}</option>}
   {servers.map(server => <option key={server.id} value={server.id}>{server.name}</option>)}
  </select></label>
  {!servers.length && <p className="service-help">Add a {docker ? "local Docker deployment" : "Kubernetes or OpenShift"} server in Infrastructure → Servers.</p>}
  {docker ? <>
   {serviceType === "postgresql" ? <p className="service-help">Dispatch starts PostgreSQL with a persistent volume, generates its password, and waits until it is ready. The service name becomes the database name.</p> : <p className="service-help">Edit environment values, readiness commands and connection mappings in YAML.</p>}
   <details className="service-advanced"><summary>Docker settings</summary>
    <label>Image<input placeholder="postgres:17-bookworm" value={docker.image ?? ""} onChange={e => onDocker({ ...docker, image: e.target.value })} /></label>
    <label>Network<input placeholder="dispatch-services" value={docker.network ?? ""} onChange={e => onDocker({ ...docker, network: e.target.value })} /></label>
    <p className="service-help">Dispatch connects bound Docker applications to this network. The database has no published host port. Defaults are 512 MiB memory and 0.5 CPU.</p>
   </details>
  </> : <>
   <label>Namespace<input placeholder="Server default namespace" value={helm?.namespace ?? ""} onChange={e => onHelm({ ...helm!, namespace: e.target.value })} /></label>
   {helm?.chart ? <p className="service-help">Custom chart: {helm.chart}. Edit its values and connection mappings in YAML.</p> : <>
    <p className="service-help">Dispatch installs its PostgreSQL chart with persistent storage, generated credentials, and a readiness check. Applications connect through an internal cluster address.</p>
    <details className="service-advanced"><summary>Helm settings</summary>
     <label>Image<input placeholder="postgres:17-bookworm" value={helm?.image ?? ""} onChange={e => onHelm({ ...helm!, image: e.target.value })} /></label>
     <div className="service-form-grid"><label>Storage<input placeholder="10Gi" value={helm?.storage ?? ""} onChange={e => onHelm({ ...helm!, storage: e.target.value })} /></label><label>Storage class<input placeholder="Cluster default" value={helm?.storageClass ?? ""} onChange={e => onHelm({ ...helm!, storageClass: e.target.value })} /></label></div>
    </details>
   </>}
  </>}
 </section>;
}
