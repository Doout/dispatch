import { useEffect, useState } from "react";
import { ArrowLeft, RocketLaunch, TerminalWindow, WarningCircle } from "@phosphor-icons/react";
import { api, ConfigSource, WorkflowResource, WorkflowRevision, WorkflowTopology } from "../api";
import { HelmTakeoverPanel, hasHelmOwnershipConflict } from "../deployments/HelmTakeoverPanel";
import { PageHeader } from "../PageHeader";
import { TopologyCanvas } from "./TopologyCanvas";
import { isPreviewCheckRun, workflowResourceStatus, workflowResourceStatusLabel } from "./status";
import { useWorkflowRevisions } from "./useWorkflowRevisions";

export function WorkflowTopologyPage({ resource, source, revisions, canRun = true, canTakeOver = false, onBack, onRun, onOpenRuns }: { resource: WorkflowResource; source?: ConfigSource; revisions?: WorkflowRevision[]; canRun?: boolean; canTakeOver?: boolean; onBack: () => void; onRun: () => Promise<void>; onOpenRuns: () => void }) {
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

  const ownershipConflict = latestRevision?.state === "failed" && hasHelmOwnershipConflict(latestRevision.error);
  return <div className="page-layout topology-page">
    <PageHeader view="applications" title={resource.name} action={{ label: "Back", onClick: onBack, icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <div className="topology-context">
      <div><span className={`status-label ${resourceStatus}`}><i />{workflowResourceStatusLabel(resourceStatus)}</span><span>{source?.name ?? "Repository configuration"}</span><code>{resource.path}</code></div>
      <div><button className="quiet-button" onClick={onOpenRuns}><TerminalWindow size={15} />Runs &amp; logs</button>{canRun && resource.active && resource.kind === "Application" && <button className="primary-button" disabled={running} onClick={() => void run()}><RocketLaunch size={15} />{running ? "Starting" : "Run"}</button>}</div>
    </div>
    {latestRevision?.state === "failed" && !ownershipConflict && <p className="topology-error" role="status"><WarningCircle size={16} weight="fill" />Latest run failed. {latestRevision.error || "Open Runs & logs to inspect the failure."}</p>}
    {ownershipConflict && (canTakeOver
      ? <WorkflowHelmTakeover key={latestRevision.id} revisionId={latestRevision.id} resourceName={resource.name} onOpenRuns={onOpenRuns}
          onRunAgain={canRun && resource.active && resource.kind === "Application" ? run : undefined} running={running} />
      : <p className="topology-error" role="status">Helm release ownership conflict. Ask a controller owner to inspect the release.</p>)}
    {history.error && <p className="topology-error" role="alert">Could not load run history. {history.error} <button className="quiet-button" onClick={history.retry}>Retry run history</button></p>}
    {error && <p className="topology-error" role="alert"><WarningCircle size={16} weight="fill" />{error}</p>}
    {!error && !topology && <div className="topology-loading"><span /><span /><span /></div>}
    {topology && <TopologyCanvas topology={topology} />}
  </div>;
}

function WorkflowHelmTakeover({ revisionId, resourceName, onOpenRuns, onRunAgain, running }: {
  revisionId: string; resourceName: string; onOpenRuns: () => void; onRunAgain?: () => void; running: boolean;
}) {
  const [destination, setDestination] = useState<{ appId: string; appName: string; stageName: string } | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    async function findApplication() {
      const stages = await api.workflowStages(revisionId);
      const stage = stages.find(item => item.state === "failed" && hasHelmOwnershipConflict(item.error));
      const id = stage?.deploymentIds?.at(-1);
      if (!id || !stage) throw new Error("Missing failed deployment");
      const deployment = await api.deployment(id);
      if (!hasHelmOwnershipConflict(deployment.message)) throw new Error("Different deployment failure");
      if (active) setDestination({ appId: deployment.appId, appName: deployment.app?.name || resourceName, stageName: stage.stageName });
    }
    void findApplication().catch(() => { if (active) setError("Could not load the failed deployment."); });
    return () => { active = false; };
  }, [revisionId, resourceName]);
  if (error) return <p className="topology-error" role="alert">Helm release ownership conflict. {error} <button className="quiet-button" onClick={onOpenRuns}>Runs &amp; logs</button></p>;
  return destination ? <HelmTakeoverPanel {...destination} onRunAgain={onRunAgain} running={running} /> : <p role="status">Loading the blocked deployment…</p>;
}
