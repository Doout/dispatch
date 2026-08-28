import { useEffect, useState } from "react";
import { FileCode, RocketLaunch, WarningCircle, X } from "@phosphor-icons/react";
import { api, Overview, WorkflowJobResult, WorkflowResource, WorkflowStageRun } from "../api";
import { relative } from "../presentation";
import { useDialogFocus } from "../useDialogFocus";

export function WorkflowResourceDialog({ resource, overview, onClose, onChanged, onOpenDeploymentManifests }: { resource: WorkflowResource; overview: Overview; onClose: () => void; onChanged: () => Promise<void>; onOpenDeploymentManifests: (deploymentID: string) => void }) {
  const dialogRef = useDialogFocus(onClose);
  const revisions = (overview.workflowRevisions ?? []).filter((item) => item.resourceId === resource.id).sort((left, right) => right.createdAt.localeCompare(left.createdAt));
  const [revisionID, setRevisionID] = useState(revisions[0]?.id ?? "");
  const [jobs, setJobs] = useState<WorkflowJobResult[]>([]);
  const [stages, setStages] = useState<WorkflowStageRun[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const revision = revisions.find((item) => item.id === revisionID);

  useEffect(() => {
    let active = true;
    setJobs([]);
    setStages([]);
    if (!revisionID) return () => { active = false; };
    void Promise.all([api.workflowJobs(revisionID), api.workflowStages(revisionID)]).then(([nextJobs, nextStages]) => {
      if (active) { setJobs(nextJobs); setStages(nextStages); }
    }).catch((cause: Error) => active && setError(cause.message));
    return () => { active = false; };
  }, [revisionID]);

  async function action(kind: "activate" | "pause" | "run") {
    setBusy(true);
    setError("");
    try {
      if (kind === "activate") await api.activateWorkflowResource(resource.id);
      else if (kind === "pause") await api.deactivateWorkflowResource(resource.id);
      else await api.runWorkflowResource(resource.id);
      await onChanged();
      onClose();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function approve(stage: WorkflowStageRun) {
    setBusy(true);
    setError("");
    try {
      await api.approveWorkflowStage(stage.id);
      setStages(await api.workflowStages(stage.revisionId));
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <div className="dialog-layer workflow-resource-layer" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialogRef} className="resource-dialog workflow-resource-dialog" role="dialog" aria-modal="true" aria-labelledby="workflow-resource-title">
      <header><div><h2 id="workflow-resource-title">{resource.name}</h2><p>{resource.kind} from {resource.path}</p></div><button data-autofocus aria-label="Close workflow details" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body workflow-resource-body">
        <div className="workflow-resource-toolbar"><span className={`status-label ${resource.active ? resource.state : "disabled"}`}><i />{resource.active ? resource.state : "paused"}</span><div>{resource.active && resource.kind === "Application" && <button className="quiet-button" disabled={busy} onClick={() => void action("run")}><RocketLaunch size={15} />Run</button>}<button className="quiet-button" disabled={busy} onClick={() => void action(resource.active ? "pause" : "activate")}>{resource.active ? "Pause" : "Activate"}</button></div></div>
        {error && <p className="form-error" role="alert">{error}</p>}
        <div className="workflow-resource-columns">
          <section><div className="workflow-section-heading"><h3>Configuration</h3><span>{resource.apiVersion}</span></div>{resource.kind === "Application" && <div className="workflow-managed-guide"><strong>Add another managed deployment</strong><span>Add it under <code>spec.deployments</code>, include its name in <code>stages[].deploy</code>, then commit this file. The next poll imports it.</span></div>}<pre className="workflow-document">{resource.document}</pre></section>
          <section><div className="workflow-section-heading"><h3>Runs</h3>{revisions.length > 0 && <select aria-label="Workflow run" value={revisionID} onChange={(event) => setRevisionID(event.target.value)}>{revisions.map((item) => <option key={item.id} value={item.id}>{item.state} · {relative(item.createdAt)}</option>)}</select>}</div>
            {!revision && <p className="workflow-empty-note">No runs.</p>}
            {revision && <><dl className="workflow-run-summary"><div><dt>Status</dt><dd>{revision.state}</dd></div><div><dt>Trigger</dt><dd>{revision.trigger}</dd></div><div><dt>Sources</dt><dd>{Object.keys(revision.sources).length}</dd></div></dl>{revision.error && <p className="workflow-source-warning"><WarningCircle size={15} weight="fill" />{revision.error}</p>}
              {jobs.length > 0 && <div className="workflow-run-list"><h4>Jobs</h4>{jobs.map((job) => <div key={job.id}><span className={`status-label ${job.state}`}><i />{job.state}</span><strong>{job.jobName}</strong>{job.reusedFromId && <small>Reused</small>}</div>)}</div>}
              {stages.length > 0 && <div className="workflow-run-list"><h4>Stages</h4>{stages.map((stage) => <div className="workflow-stage-row" key={stage.id}><span className={`status-label ${stage.state}`}><i />{stage.state.replaceAll("_", " ")}</span><strong>{stage.stageName}</strong><small>{stage.targetRef}</small>{stage.state === "awaiting_approval" && <button className="quiet-button" disabled={busy} onClick={() => void approve(stage)}>Approve</button>}{stage.deploymentIds?.map((deploymentID) => { const deployment = overview.deployments.find((item) => item.id === deploymentID); return <button className="workflow-manifest-link" key={deploymentID} onClick={() => { onClose(); onOpenDeploymentManifests(deploymentID); }}><FileCode size={14} /><span>{deployment?.app?.name ?? "Managed deployment"}</span><strong>Manifests</strong></button>; })}</div>)}</div>}
            </>}
          </section>
        </div>
      </div>
    </section>
  </div>;
}
