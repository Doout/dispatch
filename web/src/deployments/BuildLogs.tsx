import { useState } from "react";
import { useWorkflowLogs } from "../workflows/useWorkflowLogs";
import { WorkflowLogOutput } from "../workflows/WorkflowLogOutput";

export function BuildLogs({ revisionID }: { revisionID: string }) {
  const [open, setOpen] = useState(false);
  return <details className="build-logs" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>Build logs</summary>
    {open && <LiveBuildLogs key={revisionID} revisionID={revisionID} />}
  </details>;
}

function LiveBuildLogs({ revisionID }: { revisionID: string }) {
  const [follow, setFollow] = useState(true);
  const logs = useWorkflowLogs(revisionID);
  const jobs = Object.values(logs.jobs);
  return <div>
    <div className="build-log-status"><span role="status">{logs.status}</span><label><input type="checkbox" checked={follow} onChange={event => setFollow(event.target.checked)} /> Follow output</label>{logs.phase === "error" && <button type="button" onClick={logs.reconnect}>Reconnect</button>}</div>
    {jobs.length === 0 && <p>Waiting for a build job to start. It may be checking sources or waiting for a shared build.</p>}
    {jobs.map(job => <section key={job.id} aria-label={`${job.name} logs`}>
      <header><strong>{job.name}</strong><span>{job.state}</span></header>
      {job.error && <p role="alert">{job.error}</p>}
      <WorkflowLogOutput text={job.text} follow={follow} />
    </section>)}
  </div>;
}
