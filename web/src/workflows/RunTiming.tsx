import type { WorkflowJobResult, WorkflowRevision, WorkflowStageRun } from "../api";
import { buildLogTiming, formatRunDuration, runTiming } from "./runTiming";

export function RunTiming({ revision, jobs, stages }: { revision: WorkflowRevision; jobs: WorkflowJobResult[]; stages: WorkflowStageRun[] }) {
  const timing = runTiming(revision, jobs, stages);
  if (timing.total === undefined) return null;
  return <section className="workflow-run-timing" aria-label="Run timing">
    <h4>Time spent</h4>
    <dl>
      <div><dt>{revision.finishedAt ? "Total" : "Elapsed"}</dt><dd>{formatRunDuration(timing.total)}</dd></div>
      <div><dt>Jobs</dt><dd>{formatRunDuration(timing.builds)}</dd></div>
      <div><dt>Stages</dt><dd>{formatRunDuration(timing.stages)}</dd></div>
      {(timing.queue ?? 0) > 0 && <div><dt>Queued</dt><dd>{formatRunDuration(timing.queue)}</dd></div>}
      {(timing.other ?? 0) > 0 && <div><dt>Other time</dt><dd>{formatRunDuration(timing.other)}</dd></div>}
    </dl>
    {jobs.length > 0 && <p>{timing.reusedJobs} of {jobs.length} job results reused{timing.cachedSteps > 0 ? ` · ${timing.cachedSteps} Docker build steps cached` : ""}. Jobs can overlap, so their durations are not added.</p>}
  </section>;
}

export function BuildStepTiming({ job }: { job: WorkflowJobResult }) {
  if (job.reusedFromId) return null;
  const { slowest, cachedSteps } = buildLogTiming(job.log);
  if (slowest.length === 0) return null;
  return <details className="workflow-build-steps">
    <summary>Slowest Docker steps{cachedSteps > 0 ? ` · ${cachedSteps} cached` : ""}</summary>
    <ol>{slowest.map((step, index) => <li key={`${step.label}-${index}`}><span title={step.label}>{step.label}</span><strong>{formatRunDuration(step.durationMs)}</strong></li>)}</ol>
  </details>;
}
