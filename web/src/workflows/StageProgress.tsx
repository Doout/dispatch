import { useEffect, useState } from "react";
import { FileCode } from "@phosphor-icons/react";
import { api, type Deployment, type WorkflowJobResult, type WorkflowStageRun } from "../api";

export function StageProgress({ stage, onOpenManifests }: { stage: WorkflowStageRun; onOpenManifests: (id: string) => void }) {
  const [deployments, setDeployments] = useState<{ deployment: Deployment; log: string }[]>([]);
  const [checks, setChecks] = useState<{ name: string; jobs: WorkflowJobResult[] }[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const deploymentIDs = JSON.stringify(stage.deploymentIds ?? []);
  const checkRuns = JSON.stringify(stage.checkRuns ?? {});

  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    setDeployments([]);
    setChecks([]);
    setLoading(true);
    async function refresh() {
      try {
        const [nextDeployments, nextChecks] = await Promise.all([
          Promise.all((JSON.parse(deploymentIDs) as string[]).map(async id => {
            const [deployment, logs] = await Promise.all([api.deployment(id), api.logs(id)]);
            return { deployment, log: logs.map(log => `${log.createdAt} [${log.level}] ${log.message}`).join("\n") };
          })),
          Promise.all(Object.entries(JSON.parse(checkRuns) as Record<string, string>).map(async ([name, id]) => ({ name, jobs: await api.workflowJobs(id) }))),
        ]);
        if (active) {
          setDeployments(nextDeployments);
          setChecks(nextChecks);
          setError("");
        }
      } catch (cause) {
        if (active) setError((cause as Error).message);
      } finally {
        if (active) {
          setLoading(false);
          timer = setTimeout(() => void refresh(), 3000);
        }
      }
    }
    void refresh();
    return () => { active = false; clearTimeout(timer); };
  }, [stage.id, deploymentIDs, checkRuns]);

  return <div className="workflow-stage-progress" role="region" aria-label={`${stage.stageName} progress`}>
    {stage.error && <p className="form-error" role="alert">{stage.error}</p>}
    {error && <p className="form-error" role="alert">Could not refresh stage progress: {error}</p>}
    {loading && <p>Loading stage progress...</p>}
    {!loading && deployments.length === 0 && checks.length === 0 && <p>{stage.state === "awaiting_approval" ? "Waiting for approval." : stage.state === "queued" ? "Waiting to start." : stage.state === "running" ? "Preparing deployments. Logs will appear when a deployment starts." : "No deployment or check output was recorded."}</p>}
    {deployments.map(({ deployment, log }) => <section className="workflow-job-output" key={deployment.id} aria-label={`${deployment.app?.name ?? deployment.id} deployment output`}>
      <header><div><strong>{deployment.app?.name ?? deployment.id}</strong><span>{deployment.state}</span></div><button title="Open manifests" aria-label="Open manifests" onClick={() => onOpenManifests(deployment.id)}><FileCode size={16} /></button></header>
      {deployment.message && <p>{deployment.message}</p>}
      <pre tabIndex={0}>{log || "Waiting for deployment output."}</pre>
    </section>)}
    {checks.map(check => <section key={check.name} aria-label={`${check.name} check`}><h5>Check: {check.name}</h5>{check.jobs.length === 0 && <p>Waiting for check jobs.</p>}{check.jobs.map(job => <section className="workflow-job-output" key={job.id}><header><div><strong>{job.jobName}</strong><span>{job.state}</span></div></header>{job.error && <p>{job.error}</p>}<pre tabIndex={0}>{job.log || job.error || "Waiting for check output."}</pre></section>)}</section>)}
  </div>;
}
