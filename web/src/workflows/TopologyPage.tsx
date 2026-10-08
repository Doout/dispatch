import { useEffect, useState } from "react";
import { ArrowLeft, RocketLaunch, TerminalWindow, WarningCircle } from "@phosphor-icons/react";
import { api, ConfigSource, WorkflowResource, WorkflowRevision, WorkflowTopology } from "../api";
import { PageHeader } from "../PageHeader";
import { TopologyCanvas } from "./TopologyCanvas";
import { isPreviewCheckRun, workflowResourceStatus, workflowResourceStatusLabel } from "./status";
import { useWorkflowRevisions } from "./useWorkflowRevisions";

export function WorkflowTopologyPage({ resource, source, revisions, canRun = true, onBack, onRun, onOpenRuns }: { resource: WorkflowResource; source?: ConfigSource; revisions?: WorkflowRevision[]; canRun?: boolean; onBack: () => void; onRun: () => Promise<void>; onOpenRuns: () => void }) {
  const [topology, setTopology] = useState<WorkflowTopology | null>(null);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);
  const history = useWorkflowRevisions(resource.id, revisions);
  const latestRevision = history.revisions.find(revision => !isPreviewCheckRun(revision));
  const resourceStatus = workflowResourceStatus(resource, latestRevision?.state);

  useEffect(() => {
    let active = true;
    setTopology(null);
    setError("");
    void api.workflowTopology(resource.id).then((next) => active && setTopology(next)).catch((cause: Error) => active && setError(cause.message));
    return () => { active = false; };
  }, [resource.id]);

  async function run() {
    setRunning(true);
    setError("");
    try { await onRun(); } catch (cause) { setError((cause as Error).message); } finally { setRunning(false); }
  }

  return <div className="page-layout topology-page">
    <PageHeader view="applications" title={resource.name} action={{ label: "Back", onClick: onBack, icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <div className="topology-context">
      <div><span className={`status-label ${resourceStatus}`}><i />{workflowResourceStatusLabel(resourceStatus)}</span><span>{source?.name ?? "Repository configuration"}</span><code>{resource.path}</code></div>
      <div><button className="quiet-button" onClick={onOpenRuns}><TerminalWindow size={15} />Runs &amp; logs</button>{canRun && resource.active && resource.kind === "Application" && <button className="primary-button" disabled={running} onClick={() => void run()}><RocketLaunch size={15} />{running ? "Starting" : "Run"}</button>}</div>
    </div>
    {latestRevision?.state === "failed" && <p className="topology-error" role="status"><WarningCircle size={16} weight="fill" />Latest run failed. {latestRevision.error || "Open Runs & logs to inspect the failure."}</p>}
    {history.error && <p className="topology-error" role="alert">Could not load run history. {history.error} <button className="quiet-button" onClick={history.retry}>Retry run history</button></p>}
    {error && <p className="topology-error" role="alert"><WarningCircle size={16} weight="fill" />{error}</p>}
    {!error && !topology && <div className="topology-loading"><span /><span /><span /></div>}
    {topology && <TopologyCanvas topology={topology} />}
  </div>;
}
