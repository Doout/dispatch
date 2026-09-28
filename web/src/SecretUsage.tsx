import { useEffect, useState } from "react";
import { ArrowSquareOut, X } from "@phosphor-icons/react";
import { api, type Deployment, type Overview, type Secret, type SecretConsumer, type SecretUsage } from "./api";
import { useDialogFocus } from "./useDialogFocus";

export function useSecretUsage(overview: Overview) {
  const [items, setItems] = useState<SecretUsage[]>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  // Run/log updates do not change references and should not reload the index.
  const version = JSON.stringify([
    overview.deployments.map(d => [d.id, d.appId]),
    overview.secrets.map(s => [s.id, s.updatedAt]),
    overview.apps.map(a => [a.id, a.name, a.state, a.sourceCredentialId, a.hookSecretIds]),
    overview.workflowResources?.map(r => [r.id, r.specDigest, r.state]),
    overview.workflowPreviewTemplates?.map(t => [t.id, t.updatedAt]),
    overview.configSources?.map(s => [s.id, s.credentialSecretId]),
    overview.services?.map(s => [s.id, s.revision]),
    overview.servers.map(s => [s.id, s.builder?.sshSecretId]),
    overview.eventTriggers.map(t => [t.id, t.secretIds]),
    overview.previewGroups.map(g => [g.id, g.components]),
  ]);
  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError("");
    void api.secretUsage().then(data => { if (alive) setItems(data); })
      .catch(cause => { if (alive) { setItems(undefined); setError((cause as Error).message); } })
      .finally(() => { if (alive) setLoading(false); });
    return () => { alive = false; };
  }, [version, retry]);
  return { items, loading, error, refresh: () => setRetry(value => value + 1) };
}

export function VariableUsageButton({ secret, usage, loading, error, onClick }: {
  secret: Secret; usage?: SecretUsage; loading: boolean; error: string; onClick: () => void;
}) {
  const count = usage?.consumers.length ?? 0;
  const archived = usage?.archived.length ?? 0;
  const label = loading ? "Checking usage..." : error || !usage ? "Usage unavailable" : usage.warnings.length ? "Usage incomplete" : count ? `Used by ${count}` : archived ? `${archived} archived ${archived === 1 ? "reference" : "references"}` : "No references";
  return <button type="button" className="variable-usage-button" aria-label={`View usage for ${secret.name}`} onClick={onClick}>
    {label}{!loading && !!count && !!archived && <small> · {archived} archived</small>}
  </button>;
}
const kindLabel: Record<string, string> = {
  application: "Application", workflow: "Workflow", preview_template: "Preview template", service_template: "Service template",
  configuration: "Repository configuration", builder: "Builder", service: "Service", event_rule: "Event rule", preview_group: "Preview group",
};
function consumerLink(consumer: SecretConsumer) {
  if (consumer.kind === "workflow" || consumer.kind === "application") return `/applications/${encodeURIComponent(consumer.id)}/topology`;
  if (consumer.kind === "configuration") return `/applications/configurations/${encodeURIComponent(consumer.id)}/topology`;
  if (consumer.kind === "builder") return `/servers/${encodeURIComponent(consumer.id)}/topology`;
  if (consumer.kind === "service" || consumer.kind === "service_template") return "/services";
  if (consumer.kind === "event_rule") return "/events";
  return "/applications";
}

export function SecretUsageDialog({ secret, usage, loading, error, onRetry, onClose }: {
  secret: Secret; usage?: SecretUsage; loading: boolean; error: string; onRetry: () => void; onClose: () => void;
}) {
  const dialog = useDialogFocus(onClose);
  const [runs, setRuns] = useState<Deployment[]>();
  const [next, setNext] = useState("");
  const [runError, setRunError] = useState("");
  const [runLoading, setRunLoading] = useState(false);
  async function loadRuns(before = "") {
    setRunLoading(true); setRunError("");
    try {
      const page = await api.secretUsageDeployments(secret.id, before);
      setRuns(old => before ? [...(old ?? []), ...page.items.filter(item => !old?.some(run => run.id === item.id))] : page.items);
      setNext(page.next ?? "");
    } catch (cause) { setRunError((cause as Error).message); }
    finally { setRunLoading(false); }
  }
  const applications = new Map([...usage?.consumers ?? [], ...usage?.archived ?? []].flatMap(c => c.applications).map(a => [a.id, a]));
  function consumers(items: SecretConsumer[], archived = false) {
    return <ul className="variable-consumers">{items.map(consumer => <li key={`${consumer.kind}:${consumer.id}`}>
      <div className="variable-consumer-heading">
        {archived ? <strong>{consumer.name}</strong> : <a href={consumerLink(consumer)}>{consumer.name}<ArrowSquareOut size={13} /></a>}
        <span>{kindLabel[consumer.kind] ?? consumer.kind}{consumer.state ? ` · ${consumer.state}` : ""}</span>
      </div>
      <ul className="variable-reference-list">{consumer.references.map(reference => <li key={reference}>{reference}</li>)}</ul>
      {consumer.applications.map(app => <a className="variable-consumer-deployments" href={`/deployments?application=${encodeURIComponent(app.id)}`} key={app.id}>
        Deployment history · {app.name}{app.target ? ` · ${app.target}` : ""}{app.archived ? " · archived" : ""}<ArrowSquareOut size={13} />
      </a>)}
    </li>)}</ul>;
  }
  return <div className="dialog-layer access-dialog-layer" onMouseDown={event => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialog} className="resource-dialog variable-usage-dialog" role="dialog" aria-modal="true" aria-labelledby="variable-usage-title">
      <header><div><h2 id="variable-usage-title">Used by</h2><code>{secret.environmentVariable}</code></div><button type="button" onClick={onClose} aria-label="Close usage" data-autofocus><X size={19} /></button></header>
      <div className="dialog-body">
        {loading ? <p role="status">Checking references...</p> : error || !usage ? <div role="alert"><p>{error || "Usage unavailable."}</p><button type="button" className="quiet-button" onClick={onRetry}>Retry</button></div> : <>
          <p>Counts refer to applications, workflows, templates, and other saved configurations. Each consumer is counted once, even if it references several keys.</p>
          {usage.warnings.map(warning => <p className="form-error" role="alert" key={warning}>{warning}</p>)}
          <h3>Current references · {usage.consumers.length}</h3>
          {usage.consumers.length ? consumers(usage.consumers) : <p>No current configuration references found.</p>}
          {!!usage.archived.length && <details className="variable-archived"><summary>Archived references · {usage.archived.length}</summary><p>These references remain in removed workflow configurations.</p>{consumers(usage.archived, true)}</details>}
          {!!applications.size && <section className="variable-usage-history">
            <h3>Related deployments</h3>
            <p>Deployment history for the referenced applications. Current references do not prove that a value was used in every past run.</p>
            {runs === undefined && <button type="button" className="quiet-button" disabled={runLoading} onClick={() => void loadRuns()}>{runLoading ? "Loading deployments..." : "Show deployments"}</button>}
            {runError && <p className="form-error" role="alert">{runError}{runs !== undefined && <button type="button" onClick={() => void loadRuns(next)}>Retry</button>}</p>}
            {runs && (runs.length ? <ul className="variable-deployment-list">{runs.map(run => <li key={run.id}>
              <a aria-label={`Open ${applications.get(run.appId)?.name ?? run.appId} deployment ${run.state} ${run.commitSha?.slice(0, 12) || run.id}`} href={`/deployments/${encodeURIComponent(run.id)}`}><strong>{applications.get(run.appId)?.name ?? run.appId}</strong><span>{run.state} · {applications.get(run.appId)?.target}</span><code>{run.commitSha?.slice(0, 12) || run.id}</code><time dateTime={run.createdAt}>{new Date(run.createdAt).toLocaleString()}</time></a>
            </li>)}</ul> : <p>No retained deployments for these applications.</p>)}
            {next && <button type="button" className="quiet-button" disabled={runLoading} onClick={() => void loadRuns(next)}>{runLoading ? "Loading..." : "Load more"}</button>}
          </section>}
        </>}
      </div>
    </section>
  </div>;
}
