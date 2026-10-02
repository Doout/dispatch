import type { WorkflowCheckReport } from "../api";

export function RunChecks({ checks }: { checks: WorkflowCheckReport[] }) {
  if (!checks.length) return null;
  return <section className="workflow-feedback" aria-label="GitHub Check Runs">
    <h4>GitHub Check Runs</h4>
    <p>Reporting uses the commits and results saved for this run. Reporting errors do not change its outcome.</p>
    {checks.map(check => <div className="workflow-feedback-target" key={check.id}>
      <strong>{check.htmlUrl ? <a href={check.htmlUrl} target="_blank" rel="noreferrer">{check.name}</a> : check.name}</strong>
      <span>{check.repository}</span>
      <code title={check.commitSha}>{check.commitSha.slice(0, 12)}</code>
      <dl><div><dt>Check result</dt><dd>{(check.conclusion || check.status || "Waiting to report").replaceAll("_", " ")}</dd></div><div><dt>Reporting</dt><dd>{check.complete ? "Complete" : check.state === "retrying" ? "Retry pending" : check.status ? "Following execution" : "Queued"}</dd></div></dl>
      {check.error && <p className="form-error" role="alert">{check.error}</p>}
    </div>)}
  </section>;
}
