import { useEffect, useState } from "react";
import { Check, Copy, FileCode, RocketLaunch, TerminalWindow, WarningCircle, X } from "@phosphor-icons/react";
import { api, Overview, WorkflowJobResult, WorkflowResource, WorkflowStageRun } from "../api";
import { relative } from "../presentation";
import { useDialogFocus } from "../useDialogFocus";
import { canManageProject } from "../permissions";
import { workflowResourceStatus, workflowResourceStatusLabel } from "./status";

export function WorkflowResourceDialog({ resource, overview, onClose, onChanged, onOpenDeploymentManifests }: { resource: WorkflowResource; overview: Overview; onClose: () => void; onChanged: () => Promise<void>; onOpenDeploymentManifests: (deploymentID: string) => void }) {
  const dialogRef = useDialogFocus(onClose);
  const revisions = (overview.workflowRevisions ?? []).filter((item) => item.resourceId === resource.id).sort((left, right) => right.createdAt.localeCompare(left.createdAt));
  const [revisionID, setRevisionID] = useState(revisions[0]?.id ?? "");
  const [jobs, setJobs] = useState<WorkflowJobResult[]>([]);
  const [stages, setStages] = useState<WorkflowStageRun[]>([]);
  const [selectedJobID, setSelectedJobID] = useState("");
  const [copiedJobID, setCopiedJobID] = useState("");
  const [runLoading, setRunLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const revision = revisions.find((item) => item.id === revisionID);
  const source = (overview.configSources ?? []).find((item) => item.id === resource.configSourceId);
  const canConfigure = Boolean(source && canManageProject(overview, source.projectId, "project.configure"));
  const canRun = Boolean(source && canManageProject(overview, source.projectId, "deployment.run"));
  const canApprove = Boolean(source && canManageProject(overview, source.projectId, "stage.approve"));
  const selectedJob = jobs.find((job) => job.id === selectedJobID) ?? jobs[0];
  const resourceStatus = workflowResourceStatus(resource, revisions[0]?.state);

  useEffect(() => {
    let active = true;
    setJobs([]);
    setStages([]);
    setSelectedJobID("");
    setCopiedJobID("");
    setRunLoading(Boolean(revisionID));
    if (!revisionID) return () => { active = false; };
    void Promise.all([api.workflowJobs(revisionID), api.workflowStages(revisionID)]).then(([nextJobs, nextStages]) => {
      if (active) {
        setJobs(nextJobs);
        setStages(nextStages);
        setSelectedJobID(nextJobs.find((job) => job.state === "failed")?.id ?? nextJobs[0]?.id ?? "");
      }
    }).catch((cause: Error) => active && setError(cause.message)).finally(() => active && setRunLoading(false));
    return () => { active = false; };
  }, [revisionID]);

  async function copyJobLog(job: WorkflowJobResult) {
    await navigator.clipboard.writeText(job.log || job.error || "");
    setCopiedJobID(job.id);
    window.setTimeout(() => setCopiedJobID((current) => current === job.id ? "" : current), 1400);
  }

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
        <div className="workflow-resource-toolbar"><span className={`status-label ${resourceStatus}`}><i />{workflowResourceStatusLabel(resourceStatus)}</span>{(canRun || canConfigure) && <div>{canRun && resource.active && resource.kind === "Application" && <button className="quiet-button" disabled={busy} onClick={() => void action("run")}><RocketLaunch size={15} />Run</button>}{canConfigure && <button className="quiet-button" disabled={busy} onClick={() => void action(resource.active ? "pause" : "activate")}>{resource.active ? "Pause" : "Activate"}</button>}</div>}</div>
        {error && <p className="form-error" role="alert">{error}</p>}
        <div className="workflow-resource-columns">
          <section><div className="workflow-section-heading"><h3>Configuration</h3><span>{resource.apiVersion}</span></div>{resource.kind === "Application" && <div className="workflow-managed-guide"><strong>Add another application</strong><span>Add another YAML file under <code>{source?.path || "the watched path"}</code>. The next sync adds it as a separate row pending activation.</span></div>}<pre className="workflow-document">{resource.document}</pre></section>
          <section><div className="workflow-section-heading"><h3>Runs</h3>{revisions.length > 0 && <select aria-label="Workflow run" value={revisionID} onChange={(event) => setRevisionID(event.target.value)}>{revisions.map((item) => <option key={item.id} value={item.id}>{item.state} · {relative(item.createdAt)}</option>)}</select>}</div>
            {!revision && <p className="workflow-empty-note">No runs.</p>}
            {revision && <><dl className="workflow-run-summary"><div><dt>Status</dt><dd>{revision.state}</dd></div><div><dt>Trigger</dt><dd>{revision.trigger}</dd></div><div><dt>Sources</dt><dd>{Object.keys(revision.sources).length}</dd></div></dl>{revision.error && <p className="workflow-source-warning"><WarningCircle size={15} weight="fill" />{revision.error}</p>}
              {runLoading && <p className="workflow-empty-note">Loading run...</p>}
              {jobs.length > 0 && <div className="workflow-run-list workflow-job-list"><h4>Jobs</h4>{jobs.map((job) => <button type="button" key={job.id} className={job.id === selectedJob?.id ? "active" : ""} aria-pressed={job.id === selectedJob?.id} aria-label={`${job.jobName}, ${job.state}`} onClick={() => setSelectedJobID(job.id)}><span className={`status-label ${job.state}`}><i />{job.state}</span><strong>{job.jobName}</strong>{job.reusedFromId && <small>Reused</small>}</button>)}</div>}
              {selectedJob && <section className="workflow-job-output" aria-label={`${selectedJob.jobName} job output`}><header><div><TerminalWindow size={15} /><strong>{selectedJob.jobName}</strong><span>{selectedJob.state}</span></div><button type="button" aria-label={`Copy ${selectedJob.jobName} output`} title={copiedJobID === selectedJob.id ? "Copied" : "Copy output"} disabled={!selectedJob.log && !selectedJob.error} onClick={() => void copyJobLog(selectedJob)}>{copiedJobID === selectedJob.id ? <Check size={15} /> : <Copy size={15} />}</button></header>{selectedJob.error && <p>{selectedJob.error}</p>}<pre tabIndex={0}>{selectedJob.log || selectedJob.error || "No output was captured."}</pre></section>}
              {stages.length > 0 && <div className="workflow-run-list"><h4>Stages</h4>{stages.map((stage) => <div className="workflow-stage-row" key={stage.id}><span className={`status-label ${stage.state}`}><i />{stage.state.replaceAll("_", " ")}</span><strong>{stage.stageName}</strong><small>{stage.targetRef}</small>{canApprove && stage.state === "awaiting_approval" && <button className="quiet-button" disabled={busy} onClick={() => void approve(stage)}>Approve</button>}{stage.deploymentIds?.map((deploymentID) => { const deployment = overview.deployments.find((item) => item.id === deploymentID); return <button className="workflow-manifest-link" key={deploymentID} onClick={() => { onClose(); onOpenDeploymentManifests(deploymentID); }}><FileCode size={14} /><span>{deployment?.app?.name ?? "Managed deployment"}</span><strong>Manifests</strong></button>; })}</div>)}</div>}
            </>}
          </section>
        </div>
      </div>
    </section>
  </div>;
}
