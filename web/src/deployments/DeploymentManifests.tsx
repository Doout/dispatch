import { useEffect, useMemo, useState } from "react";
import { Check, Copy, FileCode, FolderOpen, GitBranch, Plus, X } from "@phosphor-icons/react";
import { api, Deployment, DeploymentManifest, DeploymentManifests as ManifestData } from "../api";

export function DeploymentManifests({ deployment }: { deployment: Deployment }) {
  const [data, setData] = useState<ManifestData | null>(null);
  const [error, setError] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [query, setQuery] = useState("");
  const [guideOpen, setGuideOpen] = useState(false);

  useEffect(() => {
    let active = true;
    setData(null);
    setError("");
    api.deploymentManifests(deployment.id).then((next) => {
      if (!active) return;
      setData(next);
      setSelectedID(manifestID(next.manifests[0]));
    }).catch((cause) => { if (active) setError((cause as Error).message); });
    return () => { active = false; };
  }, [deployment.id]);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return data?.manifests.filter((item) => !needle || `${item.kind} ${item.name}`.toLowerCase().includes(needle)) ?? [];
  }, [data, query]);
  const selected = filtered.find((item) => manifestID(item) === selectedID) ?? filtered[0];

  if (error) return <div className="runtime-error" role="alert">{error}</div>;
  if (!data) return <div className="runtime-loading">Loading manifests...</div>;
  return <section className="runtime-panel manifest-panel">
    <header className="runtime-heading manifest-heading">
      <div><h2>Release manifests</h2><p>{data.target} · {data.namespace} · {data.release}</p></div>
      <button type="button" className="quiet-button" onClick={() => setGuideOpen((open) => !open)}><Plus size={15} />Add resource</button>
    </header>
    <ManifestOrigin data={data} />
    {guideOpen && <AddResourceGuide data={data} onClose={() => setGuideOpen(false)} />}
    {data.warning && <p className="runtime-warning">{data.warning}</p>}
    {data.manifests.length ? <div className="manifest-browser">
      <aside className="manifest-index" aria-label="Release manifests">
        <label><span className="sr-only">Filter manifests</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={`Filter ${data.manifests.length} resources`} /></label>
        <div>{filtered.map((item) => <button key={manifestID(item)} type="button" aria-label={`View ${item.kind} ${item.name}`} className={manifestID(item) === manifestID(selected) ? "active" : ""} onClick={() => setSelectedID(manifestID(item))}><FileCode size={16} /><span><strong>{item.name}</strong><small>{item.kind}</small></span></button>)}</div>
      </aside>
      {selected ? <ManifestDocument manifest={selected} /> : <p className="runtime-empty">No matching manifests.</p>}
    </div> : <p className="runtime-empty">No release-owned manifests found.</p>}
  </section>;
}

function ManifestOrigin({ data }: { data: ManifestData }) {
  const origin = data.origin;
  return <section className="manifest-origin" aria-labelledby="manifest-origin-title">
    <div className="manifest-origin-title"><GitBranch size={18} /><div><strong id="manifest-origin-title">Where this came from</strong><span>{origin.managed ? "Managed by a repository application" : "Deployed from a Helm source"}</span></div></div>
    <dl>
      {origin.configPath && <div><dt>Application file</dt><dd><code>{joinRef(origin.repository, origin.configPath)}</code></dd></div>}
      <div><dt>Chart</dt><dd><code>{joinRef(origin.chartRepository || origin.repository, origin.chartPath)}</code></dd></div>
      <div><dt>Revision</dt><dd><code>{[origin.branch, origin.configRevision].filter(Boolean).join(" @ ") || "Current deployment"}</code></dd></div>
    </dl>
  </section>;
}

function AddResourceGuide({ data, onClose }: { data: ManifestData; onClose: () => void }) {
  const origin = data.origin;
  const templates = joinRef(origin.chartRepository || origin.repository, joinRef(origin.chartPath, "templates/"));
  return <section className="manifest-add-guide" aria-labelledby="manifest-add-title">
    <span className="manifest-add-icon"><FolderOpen size={19} /></span>
    <div><strong id="manifest-add-title">Add a Kubernetes resource</strong><p>Add a template under <code>{templates}</code>, then push to <code>{origin.branch || "the configured branch"}</code>. Dispatch picks up the commit and creates a new deployment.</p>{origin.configPath && <p>Change stages, targets, or chart bindings in <code>{joinRef(origin.repository, origin.configPath)}</code>.</p>}</div>
    <button type="button" aria-label="Close add resource guide" onClick={onClose}><X size={16} /></button>
  </section>;
}

function ManifestDocument({ manifest }: { manifest: DeploymentManifest }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(manifest.document);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1400);
  };
  return <section className="manifest-document" aria-labelledby="manifest-document-title">
    <header><div><span>{manifest.kind}</span><strong id="manifest-document-title">{manifest.name}</strong><small>{manifest.apiVersion}</small></div><button type="button" className="icon-button" aria-label={`Copy ${manifest.name} YAML`} title={copied ? "Copied" : "Copy YAML"} onClick={() => void copy()}>{copied ? <Check size={16} /> : <Copy size={16} />}</button></header>
    <pre tabIndex={0}><code>{manifest.document}</code></pre>
  </section>;
}

function manifestID(manifest?: DeploymentManifest) { return manifest ? `${manifest.kind}/${manifest.name}` : ""; }
function joinRef(left?: string, right?: string) { return [left?.replace(/\/$/, ""), right?.replace(/^\//, "")].filter(Boolean).join("/") || "Not recorded"; }
